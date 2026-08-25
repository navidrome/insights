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

	"github.com/navidrome/insights/internal/consts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// readAllLines returns every segment's lines in order. Each must decode cleanly, trailer
// included, so a writer that stops terminating its member fails here.
func readAllLines(dataFolder string, date time.Time) []string {
	GinkgoHelper()
	return readSegments(dataFolder, date, false)
}

// readAllLinesLive is readAllLines for a day still open, tolerating the io.ErrUnexpectedEOF a
// flushed-but-unterminated member ends with.
func readAllLinesLive(dataFolder string, date time.Time) []string {
	GinkgoHelper()
	return readSegments(dataFolder, date, true)
}

func readSegments(dataFolder string, date time.Time, tolerateOpenMember bool) []string {
	GinkgoHelper()

	// An unterminated member reports the error from Close too.
	check := func(err error, path string) {
		GinkgoHelper()
		if tolerateOpenMember && errors.Is(err, io.ErrUnexpectedEOF) {
			return
		}
		Expect(err).ToNot(HaveOccurred(), "decoding %s", path)
	}

	var lines []string
	for _, path := range mustPaths(dataFolder, date) {
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

// randomID is incompressible, so writing one forces deflate to spill to the file.
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

	// The reason segments exist: appending to a killed process's unterminated member costs
	// every record written after the restart.
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

	// A handler racing SIGTERM must not reopen a segment with no lock and no flush loop.
	It("refuses Append after Close and creates no file", func() {
		w, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(w.Close()).To(Succeed())

		Expect(w.Append(dataFor("a"), testDay)).To(MatchError(os.ErrClosed))
		Expect(DaySegmentPaths(dir, testDay)).To(BeEmpty())
		Expect(HasDay(dir, testDay)).To(BeFalse())
	})

	// gzip.Writer latches its first write error forever, so without a fatal signal one ENOSPC
	// 500s every later report for the life of the process.
	Describe("an unrecoverable write error", func() {
		// breakFile closes the file under the gzip stream, so the next write fails.
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

			// The latch is permanent.
			Expect(w.Append(dataFor("b"), testDay)).ToNot(Succeed())
			Expect(w.Fatal()).To(BeClosed())
		})

		// The rollover close writes the previous day's buffered tail and trailer. A failure
		// there loses records, and closeFile has already cleared the segment state, so without
		// the latch the next Append opens a fresh segment and answers 200.
		It("is reported through Fatal and Err when the UTC rollover close hits it", func() {
			w, err := NewWriter(dir)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = w.Close() }()
			Expect(w.Append(dataFor("a"), testDay)).To(Succeed())
			Expect(w.Fatal()).ToNot(BeClosed())

			breakFile(w)

			Expect(w.Append(dataFor("b"), testDay.AddDate(0, 0, 1))).ToNot(Succeed())
			Expect(w.Fatal()).To(BeClosed())
			Expect(w.Err()).To(HaveOccurred())
		})

		It("is reported through Fatal and Err when Append hits it", func() {
			w, err := NewWriter(dir)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = w.Close() }()
			Expect(w.Append(dataFor("a"), testDay)).To(Succeed())

			breakFile(w)

			// Enough incompressible payload that Append itself fails, not only Flush.
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

		// A sync failure means records believed durable may be gone. Returned plain, it leaves
		// a green /healthz while the segment tail rots.
		It("is reported through Fatal and Err when the file sync fails", func() {
			// Installed before the Writer exists and restored after Close joins the flush
			// loop, which calls syncFile on its own goroutine.
			prev := syncFile
			var syncs int
			syncFile = func(f *os.File) error {
				syncs++
				if syncs == 1 {
					return errors.New("fsync boom")
				}
				return prev(f)
			}
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

			// Kept: nothing afterwards can tell a lost write from a durable one.
			_ = w.Flush()
			Expect(w.Err()).To(MatchError(flushErr))
			Expect(w.Fatal()).To(BeClosed())
		})

		// Every later Append gets the same refusal until UTC midnight. The latch trades a
		// silent 500-forever outage for a visible crash-loop.
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

		// A persistent creation failure 500s every report behind a green /healthz. A single
		// blip must not, because the next Append retries from scratch.
		Describe("a segment that cannot be created", func() {
			// A regular file where a month directory goes makes MkdirAll fail with ENOTDIR.
			// Unlike a read-only directory, this also works when the test runs as root.
			blockDay := func(date time.Time) {
				GinkgoHelper()
				monthDir := dayDir(dir, date)
				Expect(os.MkdirAll(filepath.Dir(monthDir), 0750)).To(Succeed())
				Expect(os.WriteFile(monthDir, []byte{}, 0600)).To(Succeed())
			}
			unblockDay := func(date time.Time) {
				GinkgoHelper()
				Expect(os.Remove(dayDir(dir, date))).To(Succeed())
			}
			// age backdates the failure run, so a spec can cross segmentCreateGrace without
			// sleeping.
			age := func(w *Writer, d time.Duration) {
				GinkgoHelper()
				w.mu.Lock()
				defer w.mu.Unlock()
				Expect(w.createFailedAt).ToNot(BeZero(), "no failure run to age")
				w.createFailedAt = w.createFailedAt.Add(-d)
			}

			It("is reported through Fatal and Err once it has lasted past the grace window", func() {
				blockDay(testDay)

				w, err := NewWriter(dir)
				Expect(err).ToNot(HaveOccurred())
				defer func() { _ = w.Close() }()

				// A blocked month directory now fails at the listing, before MkdirAll gets a
				// turn. It is still a creation failure, and still subject to the grace window.
				Expect(w.Append(dataFor("a"), testDay)).To(MatchError(ContainSubstring("finding the next segment")))
				Expect(w.Fatal()).ToNot(BeClosed(), "one failure is a blip, not a verdict")
				Expect(w.Err()).ToNot(HaveOccurred())

				age(w, 2*segmentCreateGrace)

				appendErr := w.Append(dataFor("b"), testDay)
				Expect(appendErr).To(HaveOccurred())
				Expect(w.Fatal()).To(BeClosed())
				Expect(w.Err()).To(MatchError(appendErr))
			})

			// A failure that goes away leaves nothing behind for a later one to inherit.
			It("does not latch when a failure is followed by a success", func() {
				blockDay(testDay)

				w, err := NewWriter(dir)
				Expect(err).ToNot(HaveOccurred())
				defer func() { _ = w.Close() }()

				Expect(w.Append(dataFor("a"), testDay)).ToNot(Succeed())
				Expect(w.Fatal()).ToNot(BeClosed())
				// Old enough to latch, so only the success below can stop it.
				age(w, 2*segmentCreateGrace)

				unblockDay(testDay)
				Expect(w.Append(dataFor("b"), testDay)).To(Succeed())
				Expect(w.Fatal()).ToNot(BeClosed())

				// A fresh failure starts its own run, so it is a blip again.
				nextMonth := testDay.AddDate(0, 1, 0)
				blockDay(nextMonth)
				Expect(w.Append(dataFor("c"), nextMonth)).ToNot(Succeed())
				Expect(w.Fatal()).ToNot(BeClosed())
				Expect(w.Err()).ToNot(HaveOccurred())
			})
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

// crash stops the flush loop and releases the lock without terminating the gzip member, as a
// kill after a flush would.
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
	// w.gz and w.file are left open and unterminated on purpose.
}
