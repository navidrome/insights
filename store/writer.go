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

// Writer appends reports to the current UTC day's file. It is safe for concurrent use.
type Writer struct {
	mu     sync.Mutex
	folder string
	lock   *os.File
	file   *os.File
	gz     *gzip.Writer
	day    string // UTC date of the open file, consts.DateFormat

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

	w := &Writer{folder: dataFolder, lock: lock, stop: make(chan struct{})}
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

// Append writes one report as a JSON line into the file for t's UTC day.
func (w *Writer) Append(data insights.Data, t time.Time) error {
	line, err := json.Marshal(Record{Time: t.UTC(), Data: data})
	if err != nil {
		return fmt.Errorf("marshalling record: %w", err)
	}
	line = append(line, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.openFor(t); err != nil {
		return err
	}
	if _, err := w.gz.Write(line); err != nil {
		return fmt.Errorf("writing record: %w", err)
	}
	return nil
}

// openFor ensures the file for t's UTC day is open, rolling over if the day changed.
// Callers must hold w.mu.
func (w *Writer) openFor(t time.Time) error {
	day := t.UTC().Format(consts.DateFormat)
	if w.gz != nil && w.day == day {
		return nil
	}
	if err := w.closeFile(); err != nil {
		return err
	}

	path := DayFilePath(w.folder, t)
	if err := os.MkdirAll(filepath.Dir(path), consts.DirPermissions); err != nil {
		return fmt.Errorf("creating day dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, consts.FilePermissions) //#nosec G304 -- path built from controlled env var and constants
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}

	w.file = f
	w.gz = gzip.NewWriter(f)
	w.day = day
	return nil
}

// closeFile terminates the current gzip member and closes the file. Callers must hold w.mu.
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

// Flush makes everything appended so far readable and durable.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gz == nil {
		return nil
	}
	if err := w.gz.Flush(); err != nil {
		return fmt.Errorf("flushing gzip stream: %w", err)
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
		err = w.closeFile()
		if w.lock != nil {
			_ = syscall.Flock(int(w.lock.Fd()), syscall.LOCK_UN)
			_ = w.lock.Close()
			w.lock = nil
		}
	})
	return err
}
