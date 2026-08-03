package store

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// readAllLines decompresses a day file and returns its lines.
//
// io.ErrUnexpectedEOF is tolerated: a flushed-but-not-closed gzip member has no trailer yet,
// so reading a file a live writer still holds open ends that way. Everything decoded before
// the error is intact, which is what the reader in Task 3 has to rely on too.
func readAllLines(dataFolder string, date time.Time) []string {
	GinkgoHelper()
	f, err := os.Open(DayFilePath(dataFolder, date))
	Expect(err).ToNot(HaveOccurred())
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	Expect(err).ToNot(HaveOccurred())
	defer func() { _ = gz.Close() }()
	b, err := io.ReadAll(gz)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		Expect(err).ToNot(HaveOccurred())
	}
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
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

	It("appends a new gzip member after reopening the same day", func() {
		w1, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(w1.Append(dataFor("a"), testDay)).To(Succeed())
		Expect(w1.Close()).To(Succeed())

		w2, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(w2.Append(dataFor("b"), testDay)).To(Succeed())
		Expect(w2.Close()).To(Succeed())

		lines := readAllLines(dir, testDay)
		Expect(lines).To(HaveLen(2))
		Expect(lines[0]).To(ContainSubstring(`"id":"a"`))
		Expect(lines[1]).To(ContainSubstring(`"id":"b"`))
	})

	It("makes records readable after Flush, before Close", func() {
		w, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		defer func() { _ = w.Close() }()
		Expect(w.Append(dataFor("a"), testDay)).To(Succeed())
		Expect(w.Flush()).To(Succeed())
		Expect(readAllLines(dir, testDay)).To(HaveLen(1))
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
})
