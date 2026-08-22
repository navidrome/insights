package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/navidrome/insights/consts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// writeDay writes the given IDs one second apart as one session, so each call adds a
// segment.
func writeDay(dir string, date time.Time, ids ...string) {
	GinkgoHelper()
	w, err := NewWriter(dir)
	Expect(err).ToNot(HaveOccurred())
	for i, id := range ids {
		Expect(w.Append(dataFor(id), date.Add(time.Duration(i)*time.Second))).To(Succeed())
	}
	Expect(w.Close()).To(Succeed())
}

// collectIDs drains a record iterator into the list of instance IDs it yielded.
func collectIDs(seq iter.Seq[Record]) []string {
	GinkgoHelper()
	var ids []string
	for rec := range seq {
		ids = append(ids, rec.Data.InsightsID)
	}
	return ids
}

// truncate lops n bytes off the day's last segment, as an unclean shutdown would.
func truncate(dir string, date time.Time, n int64) {
	GinkgoHelper()
	paths := DaySegmentPaths(dir, date)
	Expect(paths).ToNot(BeEmpty())
	path := paths[len(paths)-1]
	fi, err := os.Stat(path)
	Expect(err).ToNot(HaveOccurred())
	Expect(fi.Size()).To(BeNumerically(">", n))
	Expect(os.Truncate(path, fi.Size()-n)).To(Succeed())
}

// truncationBytes reaches past the 8-byte trailer into the deflate stream of a 100-record
// segment, so it costs whole records and leaves the last line half-written.
const truncationBytes = 40

// captureLog returns everything f logged through the standard logger.
func captureLog(f func()) string {
	GinkgoHelper()
	var buf bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(out)
		log.SetFlags(flags)
	}()
	f()
	return buf.String()
}

// manyIDs builds n sortable IDs, enough data that a truncation costs records.
func manyIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%03d", i)
	}
	return ids
}

var _ = Describe("ReadDay", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	It("returns an error when the day has no segments", func() {
		_, err := ReadDay(dir, testDay)
		Expect(err).To(HaveOccurred())
	})

	It("streams records in write order with time and payload intact", func() {
		writeDay(dir, testDay, "a", "b", "c")

		seq, err := ReadDay(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		var ids []string
		var times []time.Time
		for rec := range seq {
			ids = append(ids, rec.Data.InsightsID)
			times = append(times, rec.Time)
			Expect(rec.Data.Library.Tracks).To(Equal(int64(100)))
		}
		Expect(ids).To(Equal([]string{"a", "b", "c"}))
		Expect(times[0]).To(BeTemporally("==", testDay))
		Expect(times[2]).To(BeTemporally("==", testDay.Add(2*time.Second)))
	})

	It("stops early when the consumer breaks", func() {
		writeDay(dir, testDay, "a", "b", "c")

		seq, err := ReadDay(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		var ids []string
		for rec := range seq {
			ids = append(ids, rec.Data.InsightsID)
			break
		}
		Expect(ids).To(Equal([]string{"a"}))
	})

	It("reads every record across a day's segments, in segment order", func() {
		writeDay(dir, testDay, "a", "b")
		writeDay(dir, testDay, "c") // a second session, so a second segment
		Expect(DaySegmentPaths(dir, testDay)).To(HaveLen(2))

		seq, err := ReadDay(dir, testDay)
		Expect(err).ToNot(HaveOccurred())
		Expect(collectIDs(seq)).To(Equal([]string{"a", "b", "c"}))
	})

	It("reads records a writer has flushed but not closed", func() {
		w, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		defer func() { _ = w.Close() }()
		Expect(w.Append(dataFor("a"), testDay)).To(Succeed())
		Expect(w.Flush()).To(Succeed())

		seq, err := ReadDay(dir, testDay)
		Expect(err).ToNot(HaveOccurred())
		Expect(collectIDs(seq)).To(Equal([]string{"a"}))
	})

	It("yields the records that survived a truncated segment, in order", func() {
		ids := manyIDs(100)
		writeDay(dir, testDay, ids...)
		truncate(dir, testDay, truncationBytes)

		seq, err := ReadDay(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		got := collectIDs(seq)
		Expect(got).ToNot(BeEmpty())
		Expect(len(got)).To(BeNumerically("<", len(ids)), "the truncation should cost records")
		Expect(got).To(Equal(ids[:len(got)]), "survivors must be an intact prefix")
	})

	// Neither tail is damage, and logging them would cry corruption on every summarization
	// run.
	It("logs nothing when reading a truncated segment", func() {
		writeDay(dir, testDay, manyIDs(100)...)
		truncate(dir, testDay, truncationBytes)

		out := captureLog(func() {
			seq, err := ReadDay(dir, testDay)
			Expect(err).ToNot(HaveOccurred())
			Expect(collectIDs(seq)).ToNot(BeEmpty())
		})
		Expect(out).To(BeEmpty())
	})

	It("logs nothing when reading the segment a writer still has open", func() {
		w, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		defer func() { _ = w.Close() }()
		Expect(w.Append(dataFor("a"), testDay)).To(Succeed())
		Expect(w.Flush()).To(Succeed())

		out := captureLog(func() {
			seq, err := ReadDay(dir, testDay)
			Expect(err).ToNot(HaveOccurred())
			Expect(collectIDs(seq)).To(Equal([]string{"a"}))
		})
		Expect(out).To(BeEmpty())
	})

	// A zero-byte segment is a kill before the first flush, and it is re-read for the whole
	// retention window.
	It("logs nothing for a zero-byte segment", func() {
		writeDay(dir, testDay, "a")
		Expect(os.WriteFile(segmentPath(dir, testDay, 2), nil, consts.FilePermissions)).To(Succeed())
		writeDay(dir, testDay, "c")

		out := captureLog(func() {
			seq, err := ReadDay(dir, testDay)
			Expect(err).ToNot(HaveOccurred())
			Expect(collectIDs(seq)).To(Equal([]string{"a", "c"}))
		})
		Expect(out).To(BeEmpty())
	})

	// Content that is not gzip is damage, and staying quiet would hide data loss.
	It("logs a segment whose contents are not gzip at all", func() {
		writeDay(dir, testDay, "a")
		Expect(os.WriteFile(segmentPath(dir, testDay, 2), []byte("not gzip at all\n"), consts.FilePermissions)).To(Succeed())

		out := captureLog(func() {
			seq, err := ReadDay(dir, testDay)
			Expect(err).ToNot(HaveOccurred())
			Expect(collectIDs(seq)).To(Equal([]string{"a"}))
		})
		Expect(out).To(ContainSubstring("no readable gzip data"))
	})

	It("skips a malformed line and keeps the rest", func() {
		writeDay(dir, testDay, "a")
		appendPlainLine(dir, testDay, "{not json")
		appendPlainLine(dir, testDay, `{"time":"2026-08-03T00:00:05Z","data":{"id":"z"}}`)

		seq, err := ReadDay(dir, testDay)
		Expect(err).ToNot(HaveOccurred())
		Expect(collectIDs(seq)).To(Equal([]string{"a", "z"}))
	})

	It("keeps reading later segments when one is unreadable", func() {
		writeDay(dir, testDay, "a")
		Expect(os.WriteFile(segmentPath(dir, testDay, 2), []byte("not gzip at all\n"), consts.FilePermissions)).To(Succeed())
		writeDay(dir, testDay, "c")
		Expect(DaySegmentPaths(dir, testDay)).To(HaveLen(3))

		seq, err := ReadDay(dir, testDay)
		Expect(err).ToNot(HaveOccurred())
		Expect(collectIDs(seq)).To(Equal([]string{"a", "c"}))
	})

	It("reads a segment that was decompressed by hand", func() {
		writeDay(dir, testDay, "a")
		plain := filepath.Join(dayDir(dir, testDay), "reports-2026-08-03.002.ndjson")
		line := `{"time":"2026-08-03T00:00:05Z","data":{"id":"z"}}` + "\n"
		Expect(os.WriteFile(plain, []byte(line), consts.FilePermissions)).To(Succeed())

		seq, err := ReadDay(dir, testDay)
		Expect(err).ToNot(HaveOccurred())
		Expect(collectIDs(seq)).To(Equal([]string{"a", "z"}))
	})
})

var _ = Describe("readLines", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	It("returns an error when the day has no segments", func() {
		_, err := readLines(dir, testDay)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("2026-08-03"))
	})

	// LastPerID numbers positions across the whole day, so the sequence must not restart or
	// skip at a segment boundary.
	It("yields the lines of every segment as one continuous sequence", func() {
		writeDay(dir, testDay, "a", "b")
		writeDay(dir, testDay, "c")

		lines, err := readLines(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		var positions []int
		var ids []string
		pos := 0
		for line := range lines {
			var rec Record
			Expect(json.Unmarshal(line, &rec)).To(Succeed())
			positions = append(positions, pos)
			ids = append(ids, rec.Data.InsightsID)
			pos++
		}
		Expect(ids).To(Equal([]string{"a", "b", "c"}))
		Expect(positions).To(Equal([]int{0, 1, 2}))
	})

	// Yielding a half-written line would hand consumers a record that never existed.
	It("drops the half-written line at a truncated tail", func() {
		ids := manyIDs(100)
		writeDay(dir, testDay, ids...)
		truncate(dir, testDay, truncationBytes)

		lines, err := readLines(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		n := 0
		for line := range lines {
			var rec Record
			Expect(json.Unmarshal(line, &rec)).To(Succeed(), "line %d is not a complete record: %q", n, line)
			n++
		}
		Expect(n).To(BeNumerically(">", 0))
		Expect(n).To(BeNumerically("<", len(ids)))
	})

	It("stops reading when the consumer breaks mid-segment", func() {
		writeDay(dir, testDay, "a", "b", "c")

		lines, err := readLines(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		n := 0
		for range lines {
			n++
			break
		}
		Expect(n).To(Equal(1))
	})

	// Breaking on a segment's last line must not start the next segment.
	It("stops reading when the consumer breaks at a segment boundary", func() {
		writeDay(dir, testDay, "a")
		writeDay(dir, testDay, "b")

		lines, err := readLines(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		var ids []string
		for line := range lines {
			var rec Record
			Expect(json.Unmarshal(line, &rec)).To(Succeed())
			ids = append(ids, rec.Data.InsightsID)
			break
		}
		Expect(ids).To(Equal([]string{"a"}))
	})
})
