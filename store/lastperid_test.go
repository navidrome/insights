package store

import (
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("LastPerID", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	It("returns an error when the day file does not exist", func() {
		_, _, err := LastPerID(dir, testDay)
		Expect(err).To(HaveOccurred())
	})

	It("yields every instance exactly once", func() {
		writeDay(dir, testDay, "a", "b", "c")

		seq, _, err := LastPerID(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		var ids []string
		for d := range seq {
			ids = append(ids, d.InsightsID)
		}
		Expect(ids).To(ConsistOf("a", "b", "c"))
	})

	It("keeps only the newest record for a repeated instance", func() {
		w, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())

		early := dataFor("a")
		early.Library.Tracks = 100
		Expect(w.Append(early, testDay)).To(Succeed())

		late := dataFor("a")
		late.Library.Tracks = 999
		Expect(w.Append(late, testDay.Add(time.Hour))).To(Succeed())
		Expect(w.Close()).To(Succeed())

		seq, _, err := LastPerID(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		var got []int64
		for d := range seq {
			got = append(got, d.Library.Tracks)
		}
		Expect(got).To(Equal([]int64{999}))
	})

	It("picks the greatest timestamp even when it is not the last line", func() {
		w, err := NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())

		newest := dataFor("a")
		newest.Library.Tracks = 999
		Expect(w.Append(newest, testDay.Add(2*time.Hour))).To(Succeed())

		older := dataFor("a")
		older.Library.Tracks = 100
		Expect(w.Append(older, testDay.Add(time.Hour))).To(Succeed()) // out-of-order append
		Expect(w.Close()).To(Succeed())

		seq, _, err := LastPerID(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		var got []int64
		for d := range seq {
			got = append(got, d.Library.Tracks)
		}
		Expect(got).To(Equal([]int64{999}))
	})

	// All three "a" lines share a second, and only position 0 has Tracks 100, so the payload
	// says which tied position won. Later must win.
	It("collapses byte-identical duplicate lines to one record", func() {
		writeDay(dir, testDay, "a")
		line := `{"time":"2026-08-03T00:00:00Z","data":{"id":"a"}}`
		appendPlainLine(dir, testDay, line)
		appendPlainLine(dir, testDay, line)

		seq, _, err := LastPerID(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		var ids []string
		var tracks []int64
		for d := range seq {
			ids = append(ids, d.InsightsID)
			tracks = append(tracks, d.Library.Tracks)
		}
		Expect(ids).To(Equal([]string{"a"}))
		Expect(tracks).To(Equal([]int64{0}), "the last of the tied positions must win, not the first")
	})

	It("ignores records appended after the first pass", func() {
		writeDay(dir, testDay, "a", "b")

		seq, _, err := LastPerID(dir, testDay) // pass 1 runs here
		Expect(err).ToNot(HaveOccurred())

		appendPlainLine(dir, testDay, `{"time":"2026-08-03T00:00:09Z","data":{"id":"late"}}`)

		var ids []string
		for d := range seq { // pass 2 sees a longer file
			ids = append(ids, d.InsightsID)
		}
		Expect(ids).To(ConsistOf("a", "b"))
	})

	It("stops reading when the consumer breaks", func() {
		writeDay(dir, testDay, "a", "b", "c")

		seq, _, err := LastPerID(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		var ids []string
		for d := range seq {
			ids = append(ids, d.InsightsID)
			break
		}
		Expect(ids).To(HaveLen(1))
	})

	It("skips lines with no instance ID", func() {
		writeDay(dir, testDay, "a")
		appendPlainLine(dir, testDay, `{"time":"2026-08-03T00:00:05Z","data":{}}`)

		seq, _, err := LastPerID(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		var ids []string
		for d := range seq {
			ids = append(ids, d.InsightsID)
		}
		Expect(ids).To(Equal([]string{"a"}))
	})

	// A skipped line still occupies a position, or every later winner is off by one.
	It("keeps positions aligned when skipped lines come before later records", func() {
		writeDay(dir, testDay, "a")
		appendPlainLine(dir, testDay, "{not json")
		appendPlainLine(dir, testDay, `{"time":"2026-08-03T00:00:05Z","data":{}}`)
		writeDay(dir, testDay, "b")

		seq, _, err := LastPerID(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		var ids []string
		for d := range seq {
			ids = append(ids, d.InsightsID)
		}
		Expect(ids).To(ConsistOf("a", "b"))
	})

	// Both passes must walk one snapshot. Pass 2 finds its winners by position in the
	// concatenated day, so a segment that leaves the listing between the passes shifts every
	// later position onto a different record instead of merely removing its own.
	It("reports a segment that goes away between the two passes", func() {
		writeDay(dir, testDay, "a", "b")
		writeDay(dir, testDay, "c")

		// Pass 1 runs here, over both segments.
		seq, incomplete, err := LastPerID(dir, testDay)
		Expect(err).ToNot(HaveOccurred())

		paths, _ := DaySegmentPaths(dir, testDay)
		Expect(paths).To(HaveLen(2))
		Expect(os.Remove(paths[0])).To(Succeed())

		for range seq { //nolint:revive // draining is the point; the verdict is what is asserted
		}

		Expect(incomplete()).To(HaveOccurred(),
			"pass 2 re-listed the day and read a shorter one without saying so")
	})

	Describe("reporting lines it could not decode", func() {
		// A complete, newline-terminated line that is not JSON is not something the writer
		// produces, so it means a record is gone rather than a payload this reader is too old
		// to understand.
		It("reports a malformed line", func() {
			writeDay(dir, testDay, "a")
			appendPlainLine(dir, testDay, "{not json")

			seq, incomplete, err := LastPerID(dir, testDay)
			Expect(err).ToNot(HaveOccurred())
			for range seq { //nolint:revive // the verdict is what is asserted
			}

			Expect(incomplete()).To(HaveOccurred())
		})

		// ingest stores a report without an instance id exactly as it was sent, so these are
		// routine. Counting them as damage would block a day's summary for good over a payload
		// that is merely useless.
		It("reports nothing for a record with no instance id", func() {
			writeDay(dir, testDay, "a")
			appendPlainLine(dir, testDay, `{"time":"2026-08-03T00:00:05Z","data":{}}`)

			seq, incomplete, err := LastPerID(dir, testDay)
			Expect(err).ToNot(HaveOccurred())
			for range seq { //nolint:revive // the verdict is what is asserted
			}

			Expect(incomplete()).ToNot(HaveOccurred())
		})
	})
})
