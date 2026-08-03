package store

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

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
