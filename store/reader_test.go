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

// writeDay writes the given IDs as one writer session, one record each, one second apart.
// Every call is a new session, so every call creates a new segment.
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

// truncate lops the last n bytes off the day's last segment, simulating an unclean shutdown.
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

// truncationBytes is how much to lop off a 100-record segment (~960 bytes compressed) to
// simulate a kill mid-write. It is well past the 8-byte trailer and into the deflate stream,
// so it costs several complete records and leaves the last surviving line half-written —
// both of the conditions the tolerant reader exists for. Lopping only the trailer would make
// those tests pass without exercising either.
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

// manyIDs builds n sortable IDs, enough compressed data that a truncation costs records
// rather than only the gzip trailer.
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

	// Both of these tails are routine, not damage: the live segment has no trailer until
	// ingest closes it, and a killed process leaves one behind. Logging either would cry
	// corruption on every summarization run.
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

	// A process killed between creating a segment and its first flush leaves a zero-byte
	// file. It sits there for the whole retention window, and every summarization pass reads
	// every day twice, so logging it is a hundred identical lines a day about nothing.
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

	// The other half of that: a file with content that is not gzip is damage, and staying
	// quiet about it would hide data loss.
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

	// Task 4 numbers line positions continuously across a day's segments, so the sequence
	// must be one run over every segment: no restart and no gap at a segment boundary.
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

	// A truncated tail leaves a half-written line. Yielding it would hand consumers a record
	// that never existed, and make every read of a crash-damaged segment log a bogus
	// "malformed record". Only newline-terminated lines are yielded.
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

	// Breaking on the last line of a segment must not start the next one.
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
