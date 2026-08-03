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
})
