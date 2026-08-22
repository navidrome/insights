package store

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/navidrome/insights/consts"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

// lockFileName guards the single-writer invariant. Two processes appending to one gzip stream
// would interleave members and corrupt the file.
const lockFileName = ".ingest.lock"

// syncFile is a variable so tests can simulate a writeback failure.
var syncFile = (*os.File).Sync

// Writer appends reports to the current UTC day's segment. It is safe for concurrent use and
// never reopens another session's segment. gzip.Writer latches its first write error forever,
// so an unrecoverable write surfaces through Fatal and Err for the caller to stop the process.
type Writer struct {
	mu       sync.Mutex
	folder   string
	lock     *os.File
	file     *os.File
	gz       *gzip.Writer
	day      string // UTC date of the open segment, consts.DateFormat
	closed   bool
	fatalErr error

	// createFailedAt is when the current run of segment-creation failures started, zero while
	// creation is working.
	createFailedAt time.Time

	fatal     chan struct{}
	stop      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// NewWriter takes the exclusive writer lock and starts the periodic flush loop.
func NewWriter(dataFolder string) (*Writer, error) {
	dir := filepath.Join(dataFolder, consts.ReportsDir)
	if err := os.MkdirAll(dir, consts.DirPermissions); err != nil {
		return nil, fmt.Errorf("creating reports dir: %w", err)
	}

	lockPath := filepath.Join(dir, lockFileName)
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, consts.FilePermissions) //#nosec G304 -- path built from controlled env var and constants
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("another ingest instance holds the lock on %s: %w", dir, err)
	}

	w := &Writer{folder: dataFolder, lock: lock, stop: make(chan struct{}), fatal: make(chan struct{})}
	w.wg.Add(1)
	go w.flushLoop()
	return w, nil
}

func (w *Writer) flushLoop() {
	defer w.wg.Done()
	t := time.NewTicker(consts.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := w.Flush(); err != nil {
				log.Printf("Error flushing report file: %s", err)
			}
		case <-w.stop:
			return
		}
	}
}

// Append writes one report as a JSON line into the segment for t's UTC day.
// It returns os.ErrClosed once the Writer has been closed.
func (w *Writer) Append(data insights.Data, t time.Time) error {
	line, err := json.Marshal(Record{Time: t.UTC(), Data: data})
	if err != nil {
		return fmt.Errorf("marshalling record: %w", err)
	}
	line = append(line, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fmt.Errorf("appending to a closed writer: %w", os.ErrClosed)
	}
	if err := w.openFor(t); err != nil {
		return err
	}
	if _, err := w.gz.Write(line); err != nil {
		return w.fail(fmt.Errorf("writing record: %w", err))
	}
	return nil
}

// fail records an unrecoverable error and wakes everything waiting on Fatal. Only the first is
// kept. Callers must hold w.mu.
func (w *Writer) fail(err error) error {
	if w.fatalErr == nil {
		w.fatalErr = err
		close(w.fatal)
	}
	return err
}

// Fatal closes once the Writer hits an error it cannot recover from. The caller must stop the
// process rather than keep rejecting reports; a restart opens a fresh segment.
func (w *Writer) Fatal() <-chan struct{} {
	return w.fatal
}

// Err returns the unrecoverable error behind Fatal, or nil while the Writer is healthy.
func (w *Writer) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fatalErr
}

// segmentCreateGrace is how long creation may keep failing before the Writer latches it. A
// transient ENOSPC must not kill a healthy process; a permanent one must not 500 every report
// behind a green /healthz. 30s is ~48 refusals at production's 1.6 reports/s.
const segmentCreateGrace = 30 * time.Second

// creationFailed records a segment-creation failure and latches it once the run has lasted
// longer than segmentCreateGrace. It returns err either way. Callers must hold w.mu.
func (w *Writer) creationFailed(err error) error {
	if w.createFailedAt.IsZero() {
		w.createFailedAt = time.Now()
		return err
	}
	if time.Since(w.createFailedAt) < segmentCreateGrace {
		return err
	}
	return w.fail(err)
}

// openFor ensures a segment for t's UTC day is open, rolling over to a new segment if the
// day changed. Callers must hold w.mu.
func (w *Writer) openFor(t time.Time) error {
	day := t.UTC().Format(consts.DateFormat)
	if w.gz != nil && w.day == day {
		return nil
	}
	if err := w.closeFile(); err != nil {
		return err
	}

	path, err := NextSegmentPath(w.folder, t)
	if err != nil {
		// Every later Append gets the same refusal until UTC midnight. A restart does not free
		// indexes, so this trades a silent 500-forever outage for a visible crash-loop.
		return w.fail(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), consts.DirPermissions); err != nil {
		return w.creationFailed(fmt.Errorf("creating day dir: %w", err))
	}
	// O_EXCL: an existing file at an unused index means another process is writing this day.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, consts.FilePermissions) //#nosec G304 -- path built from controlled env var and constants
	if err != nil {
		return w.creationFailed(fmt.Errorf("creating %s: %w", path, err))
	}

	// Creation works again, so the failure run starts over.
	w.createFailedAt = time.Time{}
	w.file = f
	w.gz = gzip.NewWriter(f)
	w.day = day
	return nil
}

// closeFile terminates the current gzip member and closes the segment. Callers must hold w.mu.
//
// Both failures are latched. Close writes the buffered tail and the trailer, so a failure here
// lost records, and it has already cleared the segment state: without the latch the next Append
// opens a fresh segment and answers 200 behind a green /healthz.
func (w *Writer) closeFile() error {
	if w.gz == nil {
		return nil
	}
	gzErr := w.gz.Close()
	fileErr := w.file.Close()
	w.gz, w.file, w.day = nil, nil, ""
	if gzErr != nil {
		return w.fail(fmt.Errorf("closing gzip stream: %w", gzErr))
	}
	if fileErr != nil {
		return w.fail(fmt.Errorf("closing report file: %w", fileErr))
	}
	return nil
}

// Flush makes everything appended so far readable and durable. It does not close the gzip
// member. It returns os.ErrClosed once the Writer has been closed.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fmt.Errorf("flushing a closed writer: %w", os.ErrClosed)
	}
	if w.gz == nil {
		return nil
	}
	if err := w.gz.Flush(); err != nil {
		return w.fail(fmt.Errorf("flushing gzip stream: %w", err))
	}
	if err := syncFile(w.file); err != nil {
		// A sync failure means records believed durable may be gone and the deflate stream
		// they belong to is damaged. Nothing afterwards can tell a lost write from a good one.
		return w.fail(fmt.Errorf("syncing report file: %w", err))
	}
	return nil
}

// Close stops the flush loop, terminates the gzip member cleanly, and releases the lock.
// It is safe to call more than once.
func (w *Writer) Close() error {
	var err error
	w.closeOnce.Do(func() {
		close(w.stop)
		w.wg.Wait()

		w.mu.Lock()
		defer w.mu.Unlock()
		// Under the lock, so an Append racing shutdown fails instead of reopening a segment.
		w.closed = true
		err = w.closeFile()
		if w.lock != nil {
			_ = syscall.Flock(int(w.lock.Fd()), syscall.LOCK_UN)
			_ = w.lock.Close()
			w.lock = nil
		}
	})
	return err
}
