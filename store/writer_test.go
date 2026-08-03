package store

import (
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/navidrome/insights/consts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// readAllLines returns the lines of every segment of a UTC day, in segment order. Every
// segment must decode cleanly, trailer included, so a writer that stops terminating its
// gzip member is caught here rather than silently tolerated.
func readAllLines(dataFolder string, date time.Time) []string {
	GinkgoHelper()
	return readSegments(dataFolder, date, false)
}

// readAllLinesLive is readAllLines for a day a writer still holds open. It tolerates
// io.ErrUnexpectedEOF, which is how a flushed-but-unterminated member ends: the trailer is
// only written on Close. Everything decoded before that point is intact, which is what the
// reader in Task 3 relies on for both live reads and crash-truncated segments.
func readAllLinesLive(dataFolder string, date time.Time) []string {
	GinkgoHelper()
	return readSegments(dataFolder, date, true)
}

func readSegments(dataFolder string, date time.Time, tolerateOpenMember bool) []string {
	GinkgoHelper()

	// An unterminated member reports io.ErrUnexpectedEOF from both ReadAll and Close.
	check := func(err error, path string) {
		GinkgoHelper()
		if tolerateOpenMember && errors.Is(err, io.ErrUnexpectedEOF) {
			return
		}
		Expect(err).ToNot(HaveOccurred(), "decoding %s", path)
	}

	var lines []string
	for _, path := range DaySegmentPaths(dataFolder, date) {
		f, err := os.Open(path) //#nosec G304 -- test-only path from the suite's TempDir
		Expect(err).ToNot(HaveOccurred())
		gz, err := gzip.NewReader(f)
		Expect(err).ToNot(HaveOccurred())
		b, readErr := io.ReadAll(gz)
		check(readErr, path)
		check(gz.Close(), path)
		Expect(f.Close()).To(Succeed())

		if s := strings.TrimSuffix(string(b), "\n"); s != "" {
			lines = append(lines, strings.Split(s, "\n")...)
		}
	}
	return lines
}

// randomID returns an incompressible string of about n bytes. Writing one forces the deflate
// writer to spill to the underlying file instead of buffering the record.
func randomID(n int) string {
	GinkgoHelper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	Expect(err).ToNot(HaveOccurred())
	return base64.StdEncoding.EncodeToString(b)
}

var _ = Describe("Writer", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	It("appends a record that round-trips", func() {
		w, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		at := testDay.Add(3 * time.Hour)
		Expect(w.Append(dataFor("abc"), at)).To(Succeed())
		Expect(w.Close()).To(Succeed())

		lines := readAllLines(dir, testDay)
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]).To(ContainSubstring(`"id":"abc"`))
		Expect(lines[0]).To(ContainSubstring(`"time":"2026-08-03T03:00:00Z"`))
	})

	It("rolls over to a new file when the UTC day changes", func() {
		w, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(w.Append(dataFor("a"), testDay.Add(23*time.Hour))).To(Succeed())
		Expect(w.Append(dataFor("b"), testDay.Add(25*time.Hour))).To(Succeed())
		Expect(w.Close()).To(Succeed())

		Expect(readAllLines(dir, testDay)).To(HaveLen(1))
		Expect(readAllLines(dir, testDay.AddDate(0, 0, 1))).To(HaveLen(1))
	})

	It("writes a separate segment per writer session", func() {
		w1, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(w1.Append(dataFor("a"), testDay)).To(Succeed())
		Expect(w1.Close()).To(Succeed())

		w2, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(w2.Append(dataFor("b"), testDay)).To(Succeed())
		Expect(w2.Close()).To(Succeed())

		Expect(DaySegmentPaths(dir, testDay)).To(HaveLen(2))
		lines := readAllLines(dir, testDay)
		Expect(lines).To(HaveLen(2))
		Expect(lines[0]).To(ContainSubstring(`"id":"a"`))
		Expect(lines[1]).To(ContainSubstring(`"id":"b"`))
	})

	// The reason segments exist. A killed process leaves its member unterminated; appending
	// to that file would put a gzip header inside an open flate stream, and gzip.Reader then
	// fails with "flate: corrupt input" for everything written after the restart.
	It("keeps a crashed session's damage inside its own segment", func() {
		w1, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(w1.Append(dataFor("before"), testDay)).To(Succeed())
		crash(w1)

		w2, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(w2.Append(dataFor("after"), testDay)).To(Succeed())
		Expect(w2.Close()).To(Succeed())

		Expect(DaySegmentPaths(dir, testDay)).To(HaveLen(2))
		lines := readAllLinesLive(dir, testDay)
		Expect(lines).To(HaveLen(2))
		Expect(lines[0]).To(ContainSubstring(`"id":"before"`))
		Expect(lines[1]).To(ContainSubstring(`"id":"after"`))
	})

	It("makes records readable after Flush, before Close", func() {
		w, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		defer func() { _ = w.Close() }()
		Expect(w.Append(dataFor("a"), testDay)).To(Succeed())
		Expect(w.Flush()).To(Succeed())
		Expect(readAllLinesLive(dir, testDay)).To(HaveLen(1))
	})

	It("serializes concurrent appends", func() {
		w, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())

		const n = 50
		var wg sync.WaitGroup
		wg.Add(n)
		for i := range n {
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				Expect(w.Append(dataFor(strconv.Itoa(i)), testDay)).To(Succeed())
			}()
		}
		wg.Wait()
		Expect(w.Close()).To(Succeed())

		Expect(readAllLines(dir, testDay)).To(HaveLen(n))
	})

	It("refuses a second writer while the first holds the lock", func() {
		w1, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		defer func() { _ = w1.Close() }()

		_, err = NewWriter(dir)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("another ingest instance"))
	})

	It("releases the lock on Close", func() {
		w1, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(w1.Close()).To(Succeed())

		w2, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(w2.Close()).To(Succeed())
	})

	It("tolerates Close being called twice", func() {
		w, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(w.Close()).To(Succeed())
		Expect(w.Close()).To(Succeed())
	})

	// A handler racing SIGTERM must not resurrect a closed writer: that would hold a segment
	// open with no lock and no flush loop, and creating an empty segment would make HasDay
	// report a day that has no data.
	It("refuses Append after Close and creates no file", func() {
		w, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(w.Close()).To(Succeed())

		Expect(w.Append(dataFor("a"), testDay)).To(MatchError(os.ErrClosed))
		Expect(DaySegmentPaths(dir, testDay)).To(BeEmpty())
		Expect(HasDay(dir, testDay)).To(BeFalse())
	})

	// gzip.Writer latches its first write error forever, and openFor keeps the broken stream
	// while the day is unchanged. Without a fatal signal, one transient ENOSPC or EIO turns
	// every later report into a 500 for the rest of the process's life, with nothing but a
	// log line to show for it.
	Describe("an unrecoverable write error", func() {
		// breakFile closes the segment file under the gzip stream, which is how a write
		// failure reaches gzip.Writer: the next write to the file fails and gzip keeps
		// returning that error from every call afterwards.
		breakFile := func(w *Writer) {
			GinkgoHelper()
			w.mu.Lock()
			defer w.mu.Unlock()
			Expect(w.file).ToNot(BeNil())
			Expect(w.file.Close()).To(Succeed())
		}

		It("is reported through Fatal and Err when Flush hits it", func() {
			w, err := NewWriter(dir)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = w.Close() }()
			Expect(w.Append(dataFor("a"), testDay)).To(Succeed())
			Expect(w.Fatal()).ToNot(BeClosed())

			breakFile(w)

			Expect(w.Flush()).ToNot(Succeed())
			Expect(w.Fatal()).To(BeClosed())
			Expect(w.Err()).To(HaveOccurred())

			// The latch is permanent: this is the state that would otherwise 500 forever.
			Expect(w.Append(dataFor("b"), testDay)).ToNot(Succeed())
			Expect(w.Fatal()).To(BeClosed())
		})

		It("is reported through Fatal and Err when Append hits it", func() {
			w, err := NewWriter(dir)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = w.Close() }()
			Expect(w.Append(dataFor("a"), testDay)).To(Succeed())

			breakFile(w)

			// Enough incompressible payload that the deflate writer has to spill to the
			// file, so the failure surfaces from Append itself and not only from Flush.
			var appendErr error
			for i := 0; i < 32 && appendErr == nil; i++ {
				appendErr = w.Append(dataFor(randomID(64*1024)), testDay)
			}
			Expect(appendErr).To(HaveOccurred(), "a broken segment file must fail Append")
			Expect(w.Fatal()).To(BeClosed())
			Expect(w.Err()).To(MatchError(appendErr))
		})

		It("keeps the first error and stays fatal", func() {
			w, err := NewWriter(dir)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = w.Close() }()
			Expect(w.Append(dataFor("a"), testDay)).To(Succeed())
			breakFile(w)
			Expect(w.Flush()).ToNot(Succeed())

			first := w.Err()
			Expect(w.Flush()).ToNot(Succeed())
			Expect(w.Err()).To(MatchError(first))
		})

		// A sync failure is writeback reporting an error for bytes the process already handed
		// to the kernel: records believed durable may be gone, and the deflate stream they
		// belong to keeps growing. Returned plain, it left the Writer healthy — green
		// /healthz, 200s to reporters — while the segment tail silently rotted.
		It("is reported through Fatal and Err when the file sync fails", func() {
			prev := syncFile
			syncFile = func(*os.File) error { return errors.New("fsync boom") }
			DeferCleanup(func() { syncFile = prev })

			w, err := NewWriter(dir)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = w.Close() }()
			Expect(w.Append(dataFor("a"), testDay)).To(Succeed())
			Expect(w.Fatal()).ToNot(BeClosed())

			flushErr := w.Flush()
			Expect(flushErr).To(MatchError(ContainSubstring("fsync boom")))
			Expect(w.Fatal()).To(BeClosed())
			Expect(w.Err()).To(MatchError(flushErr))

			// Kept, so a later sync that happens to succeed does not talk the process out of
			// shutting down: nothing after the fact can tell a lost write from a durable one.
			syncFile = prev
			_ = w.Flush()
			Expect(w.Err()).To(MatchError(flushErr))
			Expect(w.Fatal()).To(BeClosed())
		})

		// Running out of segment indexes is unrecoverable in the same way: openFor asks for the
		// same day on every later Append and gets the same refusal. Without the latch, ingest
		// answers 500 to every report until UTC midnight with /healthz still green, so nothing
		// marks the container unhealthy or restarts it — the failure mode this Writer exists to
		// avoid. A restart does not free indexes, so this becomes a visible crash-loop instead.
		It("is reported through Fatal and Err when the day runs out of segment indexes", func() {
			dayPath := dayDir(dir, testDay)
			Expect(os.MkdirAll(dayPath, 0750)).To(Succeed())
			for i := 1; i <= maxSegmentIndex; i++ {
				name := fmt.Sprintf("%s%03d%s", segmentPrefix(testDay), i, consts.ReportFileExt)
				Expect(os.WriteFile(filepath.Join(dayPath, name), []byte{}, 0600)).To(Succeed())
			}

			w, err := NewWriter(dir)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = w.Close() }()

			appendErr := w.Append(dataFor("a"), testDay)
			Expect(appendErr).To(MatchError(ContainSubstring("highest index")))
			Expect(w.Fatal()).To(BeClosed())
			Expect(w.Err()).To(MatchError(appendErr))
		})

		It("reports nothing while the writer is healthy", func() {
			w, err := NewWriter(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(w.Append(dataFor("a"), testDay)).To(Succeed())
			Expect(w.Flush()).To(Succeed())
			Expect(w.Fatal()).ToNot(BeClosed())
			Expect(w.Err()).ToNot(HaveOccurred())
			Expect(w.Close()).To(Succeed())
			Expect(w.Fatal()).ToNot(BeClosed())
			Expect(w.Err()).ToNot(HaveOccurred())
		})
	})

	It("refuses Flush after Close", func() {
		w, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(w.Append(dataFor("a"), testDay)).To(Succeed())
		Expect(w.Close()).To(Succeed())

		Expect(w.Flush()).To(MatchError(os.ErrClosed))
	})
})

// crash simulates an unclean shutdown: the flush loop stops and the lock is released, but the
// gzip member is never terminated, exactly as if the process had been killed after a flush.
func crash(w *Writer) {
	GinkgoHelper()
	Expect(w.Flush()).To(Succeed())
	close(w.stop)
	w.wg.Wait()

	w.mu.Lock()
	defer w.mu.Unlock()
	Expect(w.lock).ToNot(BeNil())
	Expect(syscall.Flock(int(w.lock.Fd()), syscall.LOCK_UN)).To(Succeed())
	Expect(w.lock.Close()).To(Succeed())
	w.lock = nil
	// w.gz and w.file are deliberately left open and unterminated.
}
