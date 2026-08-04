package summary

import (
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/navidrome/insights/store"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

var _ = Describe("SummarizeData", func() {
	var dir string
	var day time.Time

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		day = time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
		Expect(os.Setenv("DATA_FOLDER", dir)).To(Succeed())
		DeferCleanup(func() { _ = os.Unsetenv("DATA_FOLDER") })
	})

	writeReports := func(ids ...string) {
		GinkgoHelper()
		w, err := store.NewWriter(dir)
		Expect(err).ToNot(HaveOccurred())
		for i, id := range ids {
			var d insights.Data
			d.InsightsID = id
			d.Version = "0.61.2"
			d.OS.Type = "linux"
			d.Library.Tracks = 500
			d.Library.ActiveUsers = 2
			Expect(w.Append(d, day.Add(time.Duration(i)*time.Second))).To(Succeed())
		}
		Expect(w.Close()).To(Succeed())
	}

	It("writes no summary file when the day has no report file", func() {
		Expect(SummarizeData(dir, day)).To(Succeed())

		_, err := os.Stat(SummaryFilePath(day))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("does not overwrite an existing summary when the report file is missing", func() {
		existing := Summary{NumInstances: 42}
		Expect(SaveSummary(existing, day)).To(Succeed())

		Expect(SummarizeData(dir, day)).To(Succeed())

		summaries, err := GetSummaries()
		Expect(err).ToNot(HaveOccurred())
		Expect(summaries).To(HaveLen(1))
		Expect(summaries[0].Data.NumInstances).To(Equal(int64(42)))
	})

	It("summarizes the instances in the day's report file", func() {
		writeReports("a", "b", "c")

		Expect(SummarizeData(dir, day)).To(Succeed())

		summaries, err := GetSummaries()
		Expect(err).ToNot(HaveOccurred())
		Expect(summaries).To(HaveLen(1))
		Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)))
		Expect(summaries[0].Data.NumActiveUsers).To(Equal(int64(6)))
	})

	It("counts a repeated instance once", func() {
		writeReports("a", "a", "b")

		Expect(SummarizeData(dir, day)).To(Succeed())

		summaries, err := GetSummaries()
		Expect(err).ToNot(HaveOccurred())
		Expect(summaries[0].Data.NumInstances).To(Equal(int64(2)))
	})

	// A backfill and the hourly purge are separate OS processes, so no mutex covers the gap
	// between listing a day's segments and reading them. What matters here is the *partial*
	// read: a day that loses all of its segments mid-read already summarizes to zero instances
	// and returns before the save, but one that loses some of them produces a real, wrong
	// number that would replace a correct summary.
	Describe("a day whose segments change while it is summarized", func() {
		// interpose replaces the first segment listing — the one SummarizeData captures as its
		// baseline — with the same listing plus a side effect, which is how the purge lands in
		// the middle of the read at a point the spec picks rather than the scheduler.
		interpose := func(during func(paths []string)) {
			GinkgoHelper()
			prev := segmentPathsFor
			first := true
			segmentPathsFor = func(dataFolder string, date time.Time) []string {
				paths := prev(dataFolder, date)
				if first {
					first = false
					during(paths)
				}
				return paths
			}
			DeferCleanup(func() { segmentPathsFor = prev })
		}

		It("keeps the good summary when a segment disappears", func() {
			writeReports("a", "b")
			writeReports("c") // a second writer session, so the day has two segments
			Expect(SummarizeData(dir, day)).To(Succeed())

			summaries, err := GetSummaries()
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)))

			// The purge deletes one of the two segments, so the re-read below sees only "c".
			interpose(func(paths []string) {
				Expect(paths).To(HaveLen(2))
				Expect(os.Remove(paths[0])).To(Succeed())
			})

			Expect(SummarizeData(dir, day)).To(Succeed())

			summaries, err = GetSummaries()
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries).To(HaveLen(1))
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)),
				"a partial read overwrote a correct summary")
		})

		// The other direction, which must stay allowed: ingest opens a new segment on every
		// restart and today's day gains segments while it is being summarized. A check that
		// treated any change as unsafe would stop the current day from ever being summarized.
		It("still saves when a segment appears", func() {
			writeReports("a")

			interpose(func(paths []string) {
				Expect(paths).To(HaveLen(1))
				writeReports("b") // ingest restarts and starts a second segment
			})

			Expect(SummarizeData(dir, day)).To(Succeed())

			summaries, err := GetSummaries()
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries).To(HaveLen(1))
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(2)))
		})
	})
})
