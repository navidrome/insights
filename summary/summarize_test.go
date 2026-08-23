package summary

import (
	"os"
	"path/filepath"
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

	writeReportsOn := func(date time.Time, ids ...string) {
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
			Expect(w.Append(d, date.Add(time.Duration(i)*time.Second))).To(Succeed())
		}
		Expect(w.Close()).To(Succeed())
	}

	writeReports := func(ids ...string) {
		GinkgoHelper()
		writeReportsOn(day, ids...)
	}

	It("writes no summary file when the day has no report file", func() {
		Expect(SummarizeData(dir, day)).To(Succeed())

		_, err := os.Stat(SummaryFilePath(dir, day))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("does not overwrite an existing summary when the report file is missing", func() {
		existing := Summary{NumInstances: 42}
		Expect(SaveSummary(dir, existing, day)).To(Succeed())

		Expect(SummarizeData(dir, day)).To(Succeed())

		summaries, err := GetSummaries(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(summaries).To(HaveLen(1))
		Expect(summaries[0].Data.NumInstances).To(Equal(int64(42)))
	})

	It("summarizes the instances in the day's report file", func() {
		writeReports("a", "b", "c")

		Expect(SummarizeData(dir, day)).To(Succeed())

		summaries, err := GetSummaries(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(summaries).To(HaveLen(1))
		Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)))
		Expect(summaries[0].Data.NumActiveUsers).To(Equal(int64(6)))
	})

	It("counts a repeated instance once", func() {
		writeReports("a", "a", "b")

		Expect(SummarizeData(dir, day)).To(Succeed())

		summaries, err := GetSummaries(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(summaries[0].Data.NumInstances).To(Equal(int64(2)))
	})

	// A backfill and the purge are separate processes, so nothing covers the gap between
	// listing a day's segments and reading them. A *partial* loss is what matters: losing all
	// of them already summarizes to zero and returns before the save.
	Describe("a day whose segments change while it is summarized", func() {
		// interpose adds a side effect to the baseline listing, so the purge lands mid-read at
		// a point the spec picks.
		interpose := func(during func(paths []string)) {
			GinkgoHelper()
			prev := segmentPathsFor
			first := true
			segmentPathsFor = func(dataFolder string, date time.Time) ([]string, error) {
				paths, err := prev(dataFolder, date)
				if first {
					first = false
					during(paths)
				}
				return paths, err
			}
			DeferCleanup(func() { segmentPathsFor = prev })
		}

		It("keeps the good summary when a segment disappears", func() {
			writeReports("a", "b")
			writeReports("c") // a second writer session, so the day has two segments
			Expect(SummarizeData(dir, day)).To(Succeed())

			summaries, err := GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)))

			// The purge deletes one of the two segments, so the re-read below sees only "c".
			interpose(func(paths []string) {
				Expect(paths).To(HaveLen(2))
				Expect(os.Remove(paths[0])).To(Succeed())
			})

			Expect(SummarizeData(dir, day)).To(Succeed())

			summaries, err = GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries).To(HaveLen(1))
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)),
				"a partial read overwrote a correct summary")
		})

		// A damaged segment is still listed, so the disappearance check cannot see it. The
		// reader skips it and the day summarizes to a smaller, entirely plausible number.
		It("keeps the good summary when a segment cannot be read", func() {
			writeReports("a", "b")
			writeReports("c") // a second writer session, so the day has two segments
			Expect(SummarizeData(dir, day)).To(Succeed())

			summaries, err := GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)))

			paths, _ := store.DaySegmentPaths(dir, day)
			Expect(paths).To(HaveLen(2))
			Expect(os.WriteFile(paths[0], []byte("not gzip at all\n"), 0o600)).To(Succeed())

			Expect(SummarizeData(dir, day)).ToNot(Succeed(), "-once has to see this fail")

			summaries, err = GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries).To(HaveLen(1))
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)),
				"a damaged segment overwrote a correct summary")
		})

		// The reviewer's rollback scenario, end to end. The baseline lists the whole day, the
		// purge hides a segment straight after, and its rename fails so the name is restored
		// before the final check. A run that lists the day again for the read sees only the
		// survivor, reads it cleanly, and finds a segment set identical to the baseline.
		It("keeps the good summary when a hidden segment is restored before the check", func() {
			writeReports("a", "b")
			writeReports("c") // a second writer session, so the day has two segments
			Expect(SummarizeData(dir, day)).To(Succeed())

			summaries, err := GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)))

			paths, err := store.DaySegmentPaths(dir, day)
			Expect(err).ToNot(HaveOccurred())
			Expect(paths).To(HaveLen(2))
			hidden := filepath.Join(filepath.Dir(paths[0]), ".purging-"+filepath.Base(paths[0]))

			prev := segmentPathsFor
			calls := 0
			segmentPathsFor = func(dataFolder string, date time.Time) ([]string, error) {
				calls++
				if calls == 2 {
					Expect(os.Rename(hidden, paths[0])).To(Succeed()) // the rename failed, roll back
				}
				out, listErr := prev(dataFolder, date)
				if calls == 1 {
					Expect(os.Rename(paths[0], hidden)).To(Succeed()) // hidden right after the baseline
				}
				return out, listErr
			}
			DeferCleanup(func() { segmentPathsFor = prev })

			Expect(SummarizeData(dir, day)).To(Succeed())

			summaries, err = GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)),
				"the read covered one segment while the check saw both")
		})

		// A day whose every segment is unreadable yields no instances, so a verdict checked
		// after the zero-instance return would never be reached and -once would report success.
		It("fails rather than reporting nothing to summarize when the day cannot be read", func() {
			writeReports("a")

			paths, err := store.DaySegmentPaths(dir, day)
			Expect(err).ToNot(HaveOccurred())
			Expect(os.WriteFile(paths[0], []byte("not gzip at all\n"), 0o600)).To(Succeed())

			Expect(SummarizeData(dir, day)).ToNot(Succeed())
		})

		// A purge deleting one segment must not excuse damage to another. errors.Is on a join
		// answers true as soon as one constituent matches, so the benign case has to be "every
		// failure was a missing file", not "one of them was".
		It("fails when a missing segment and a damaged one land in the same read", func() {
			writeReports("a", "b")
			writeReports("c") // a second writer session, so the day has two segments

			paths, err := store.DaySegmentPaths(dir, day)
			Expect(err).ToNot(HaveOccurred())
			Expect(paths).To(HaveLen(2))

			// The baseline lists both, then one is taken by a purge and the other turns out to
			// be unreadable.
			interpose(func(listed []string) {
				Expect(listed).To(HaveLen(2))
				Expect(os.Remove(paths[0])).To(Succeed())
				Expect(os.WriteFile(paths[1], []byte("not gzip at all\n"), 0o600)).To(Succeed())
			})

			Expect(SummarizeData(dir, day)).ToNot(Succeed(), "-once has to see the damage")
		})

		// A purge that fails a rename and then fails to restore what it already hid leaves the
		// day part visible, and so does a kill between two renames. Nothing about the visible
		// part looks wrong while it is being read, so the hidden segments have to be the signal.
		It("keeps the good summary when a purge left part of the day hidden", func() {
			writeReports("a", "b")
			writeReports("c") // a second writer session, so the day has two segments
			Expect(SummarizeData(dir, day)).To(Succeed())

			summaries, err := GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)))

			paths, err := store.DaySegmentPaths(dir, day)
			Expect(err).ToNot(HaveOccurred())
			Expect(paths).To(HaveLen(2))
			hidden := filepath.Join(filepath.Dir(paths[0]), ".purging-"+filepath.Base(paths[0]))
			Expect(os.Rename(paths[0], hidden)).To(Succeed())

			Expect(SummarizeData(dir, day)).To(Succeed(), "an unfinished purge is not a failure")

			summaries, err = GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries).To(HaveLen(1))
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)),
				"the visible half read cleanly and looked like a smaller day")
		})

		// beforeCall runs during the n-th listing, before it reads the directory, so a spec can
		// change the day between the read and the check rather than only before the read.
		beforeCall := func(n int, during func()) {
			GinkgoHelper()
			prev := segmentPathsFor
			calls := 0
			segmentPathsFor = func(dataFolder string, date time.Time) ([]string, error) {
				calls++
				if calls == n {
					during()
				}
				return prev(dataFolder, date)
			}
			DeferCleanup(func() { segmentPathsFor = prev })
		}

		// Must stay allowed on the live day: ingest opens a new segment on every restart, so
		// today gains segments as a matter of course and a run that refused to save on one
		// would never summarize the current day at all.
		//
		// The saved number comes from the snapshot the run took, not from the day as it stands
		// when the run ends. The segment that appeared is picked up by the next run two hours
		// later, which is the price of reading one snapshot instead of re-listing mid-read.
		It("still saves when a segment appears on the live day", func() {
			today := time.Now().UTC().Truncate(24 * time.Hour)
			writeReportsOn(today, "a")

			interpose(func(paths []string) {
				Expect(paths).To(HaveLen(1))
				writeReportsOn(today, "b") // ingest restarts and starts a second segment
			})

			Expect(SummarizeData(dir, today)).To(Succeed())

			summaries, err := GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries).To(HaveLen(1))
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(1)), "the snapshot held one segment")
		})

		// An old day never gains a segment on its own, so one appearing means a purge hid
		// segments and then rolled the rename back. The read in between saw only the survivors,
		// and comparing for disappearance alone would call that subset stable.
		It("keeps the good summary when a segment reappears on an old day", func() {
			writeReports("a", "b")
			writeReports("c") // a second writer session, so the day has two segments
			Expect(SummarizeData(dir, day)).To(Succeed())

			summaries, err := GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)))

			// The purge hides the first segment, so the read below sees only "c".
			paths, _ := store.DaySegmentPaths(dir, day)
			Expect(paths).To(HaveLen(2))
			hidden := filepath.Join(filepath.Dir(paths[0]), ".purging-"+filepath.Base(paths[0]))
			Expect(os.Rename(paths[0], hidden)).To(Succeed())

			// Its next rename fails, so it rolls back before the stability check runs.
			beforeCall(2, func() {
				Expect(os.Rename(hidden, paths[0])).To(Succeed())
			})

			Expect(SummarizeData(dir, day)).To(Succeed())

			summaries, err = GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(summaries).To(HaveLen(1))
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(3)),
				"a subset read overwrote a correct summary")
		})
	})
})
