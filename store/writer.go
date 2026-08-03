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

// lockFileName guards the single-writer invariant. Two ingest processes appending to the same
// gzip stream would interleave members and corrupt the file, so the second one must fail loudly.
const lockFileName = ".ingest.lock"

// Writer appends reports to the current UTC day's segment. It is safe for concurrent use.
//
// A Writer never reopens a segment written by another session: the first Append of a day
// creates a new segment, so a previous process's unterminated gzip member is left alone
// instead of being appended to, which would corrupt the rest of that day.
//
// A gzip.Writer latches its first write error forever: every later Write, Flush and Close
// returns that same error even once the underlying cause (a full disk, a transient EIO) is
// gone, and openFor keeps the broken stream because the day has not changed. That state is
// unrecoverable from inside the Writer, so it is reported through Fatal and Err instead of
// being retried silently. The caller is expected to shut the process down; see cmd/ingest.
type Writer struct {
	mu       sync.Mutex
	folder   string
	lock     *os.File
	file     *os.File
	gz       *gzip.Writer
	day      string // UTC date of the open segment, consts.DateFormat
	closed   bool
	fatalErr error

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

// fail records an unrecoverable gzip error and wakes everything waiting on Fatal. Only the
// first one is kept: later calls return the same error the caller passed in, unchanged.
// Callers must hold w.mu.
func (w *Writer) fail(err error) error {
	if w.fatalErr == nil {
		w.fatalErr = err
		close(w.fatal)
	}
	return err
}

// Fatal returns a channel that is closed once the Writer hits an error it cannot recover
// from. Nothing appended after that point can be written, so the caller must stop the
// process rather than keep rejecting reports: a restart opens a fresh segment.
func (w *Writer) Fatal() <-chan struct{} {
	return w.fatal
}

// Err returns the unrecoverable error behind Fatal, or nil while the Writer is healthy.
func (w *Writer) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fatalErr
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
		// Index exhaustion is unrecoverable from inside the Writer, exactly like a latched
		// gzip error: every later Append picks the same day and gets the same refusal, so
		// without the latch ingest answers 500 to every report until UTC midnight while
		// /healthz stays green and nothing restarts the container.
		//
		// The honest tradeoff: a restart does not free indexes, so the process comes back,
		// fails on its first Append and exits again — a crash-loop until the day rolls over,
		// rather than a silent one. That is the intended trade. A crash-loop is observable in
		// `docker compose ps`, in the exit status and in the restart count; 500s behind a
		// green /healthz are not. Reaching 999 writer sessions in one day already means
		// something is badly wrong and needs an operator either way.
		return w.fail(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), consts.DirPermissions); err != nil {
		return fmt.Errorf("creating day dir: %w", err)
	}
	// O_EXCL: NextSegmentPath picked an unused index, so an existing file here means another
	// process is writing this day. Truncating or appending to it would corrupt both streams.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, consts.FilePermissions) //#nosec G304 -- path built from controlled env var and constants
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}

	w.file = f
	w.gz = gzip.NewWriter(f)
	w.day = day
	return nil
}

// closeFile terminates the current gzip member and closes the segment. Callers must hold w.mu.
func (w *Writer) closeFile() error {
	if w.gz == nil {
		return nil
	}
	gzErr := w.gz.Close()
	fileErr := w.file.Close()
	w.gz, w.file, w.day = nil, nil, ""
	if gzErr != nil {
		return fmt.Errorf("closing gzip stream: %w", gzErr)
	}
	if fileErr != nil {
		return fmt.Errorf("closing report file: %w", fileErr)
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
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("syncing report file: %w", err)
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
		// Marked closed under the lock so an Append racing shutdown fails instead of
		// reopening a segment with no lock held and no flush loop running.
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
