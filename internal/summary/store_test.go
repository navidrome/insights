package summary

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/navidrome/insights/internal/consts"
)

var _ = Describe("SaveSummary", func() {
	var dir string
	var day time.Time

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		day = time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
		Expect(os.Setenv("DATA_FOLDER", dir)).To(Succeed())
		DeferCleanup(func() { _ = os.Unsetenv("DATA_FOLDER") })
	})

	// Big enough that writing it is not instantaneous, like a real one.
	bigSummary := func(n int) Summary {
		s := Summary{NumInstances: 1, Versions: make(map[string]uint64, n)}
		for i := range n {
			s.Versions[fmt.Sprintf("0.61.%06d", i)] = uint64(i) //#nosec G115 -- test-only, i is small and non-negative
		}
		return s
	}

	It("writes a summary that reads back", func() {
		Expect(SaveSummary(dir, Summary{NumInstances: 7}, day)).To(Succeed())

		seq, err := GetSummaries(dir)
		Expect(err).ToNot(HaveOccurred())
		var summaries []SummaryRecord
		for r := range seq {
			summaries = append(summaries, r)
		}
		Expect(summaries).To(HaveLen(1))
		Expect(summaries[0].Data.NumInstances).To(Equal(int64(7)))
	})

	It("leaves no temporary file behind", func() {
		Expect(SaveSummary(dir, Summary{NumInstances: 7}, day)).To(Succeed())

		entries, err := os.ReadDir(filepath.Dir(SummaryFilePath(dir, day)))
		Expect(err).ToNot(HaveOccurred())
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		Expect(names).To(ConsistOf("summary-2026-08-03.json"))
	})

	// The reason the write has to be atomic: a reader landing in an O_TRUNC window logs
	// "skipping malformed file" and drops the day from the charts.
	It("never exposes a half-written summary to a concurrent reader", func() {
		const keys = 20000
		summary := bigSummary(keys)
		Expect(SaveSummary(dir, summary, day)).To(Succeed())
		path := SummaryFilePath(dir, day)

		// SaveSummary resolves DATA_FOLDER per call, so a leaked writer would save into the
		// next spec's temp dir.
		stop := make(chan struct{})
		done := make(chan struct{})
		DeferCleanup(func() {
			close(stop)
			<-done
		})
		go func() {
			defer GinkgoRecover()
			defer close(done)
			for range 200 {
				select {
				case <-stop:
					return
				default:
				}
				Expect(SaveSummary(dir, summary, day)).To(Succeed())
			}
		}()

		// Like GetSummaries but without the directory walk, so the loop lands inside a save.
		reads := 0
		for {
			select {
			case <-done:
				Expect(reads).To(BeNumerically(">", 0))
				return
			default:
			}
			data, err := os.ReadFile(path) //#nosec G304 -- test-only path from the suite's TempDir
			Expect(err).ToNot(HaveOccurred())
			var got Summary
			Expect(json.Unmarshal(data, &got)).To(Succeed(),
				"read %d of %d bytes was not a complete summary: a save in progress must not be visible", reads, len(data))
			Expect(got.Versions).To(HaveLen(keys))
			reads++
		}
	})

	Describe("GetSummaries streaming", func() {
		var dir string

		write := func(rel string, s Summary) {
			GinkgoHelper()
			p := filepath.Join(dir, consts.SummariesDir, rel)
			Expect(os.MkdirAll(filepath.Dir(p), 0o750)).To(Succeed())
			b, err := json.Marshal(s)
			Expect(err).ToNot(HaveOccurred())
			Expect(os.WriteFile(p, b, 0o600)).To(Succeed())
		}

		collect := func() []SummaryRecord {
			GinkgoHelper()
			seq, err := GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())
			var out []SummaryRecord
			for r := range seq {
				out = append(out, r)
			}
			return out
		}

		BeforeEach(func() { dir = GinkgoT().TempDir() })

		It("yields days oldest first", func() {
			// summaryPathRegex reads the date from the file name only and never checks it
			// against the surrounding YYYY/MM directory, so a mismatched pair like these two
			// makes path order (lexical, by directory) and date order genuinely disagree:
			// "2024/01/..." sorts before "2024/02/..." as paths, but the date inside the first
			// file is the latest of the three and the date inside the second is the earliest.
			write("2024/01/summary-2026-05-01.json", Summary{NumInstances: 1}) // late date, early dir
			write("2024/02/summary-2021-01-01.json", Summary{NumInstances: 2}) // early date, later dir
			write("2025/01/summary-2025-06-01.json", Summary{NumInstances: 3}) // dir matches its date

			var dates []string
			for _, r := range collect() {
				dates = append(dates, r.Time.Format(consts.DateFormat))
			}
			Expect(dates).To(Equal([]string{"2021-01-01", "2025-06-01", "2026-05-01"}))
		})

		It("ignores a summary nested below the YYYY/MM layout", func() {
			write("2026/04/summary-2026-04-12.json", Summary{NumInstances: 10})
			write("2026/04/bkp/summary-2026-04-12.json", Summary{NumInstances: 10})

			Expect(collect()).To(HaveLen(1), "the copy under bkp/ is not a summary")
		})

		It("can be ranged over more than once", func() {
			write("2026/01/summary-2026-01-01.json", Summary{NumInstances: 1})

			seq, err := GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())

			read := func() int64 {
				var n int64
				for r := range seq {
					n = r.Data.NumInstances
				}
				return n
			}
			Expect(read()).To(Equal(int64(1)))

			// Mutate the file on disk between passes: an implementation that decoded once and
			// replayed a cached slice would still return 1 here, so only a genuine re-read of
			// the file can make the second pass see 99.
			write("2026/01/summary-2026-01-01.json", Summary{NumInstances: 99})

			Expect(read()).To(Equal(int64(99)), "a second pass must re-read the files")
		})

		It("stops early without error when the consumer breaks", func() {
			write("2026/01/summary-2026-01-01.json", Summary{NumInstances: 1})
			write("2026/01/summary-2026-01-02.json", Summary{NumInstances: 2})

			seq, err := GetSummaries(dir)
			Expect(err).ToNot(HaveOccurred())
			n := 0
			for range seq {
				n++
				break
			}
			Expect(n).To(Equal(1))
		})

		It("skips days with no instances", func() {
			write("2026/01/summary-2026-01-01.json", Summary{NumInstances: 0})
			write("2026/01/summary-2026-01-02.json", Summary{NumInstances: 5})

			Expect(collect()).To(HaveLen(1))
		})

		It("returns no error when the summaries directory does not exist", func() {
			seq, err := GetSummaries(GinkgoT().TempDir())
			Expect(err).ToNot(HaveOccurred())
			n := 0
			for range seq {
				n++
			}
			Expect(n).To(Equal(0))
		})

		It("skips a malformed file and keeps going", func() {
			write("2026/01/summary-2026-01-01.json", Summary{NumInstances: 1})
			bad := filepath.Join(dir, consts.SummariesDir, "2026/01/summary-2026-01-02.json")
			Expect(os.WriteFile(bad, []byte("{not json"), 0o600)).To(Succeed())
			write("2026/01/summary-2026-01-03.json", Summary{NumInstances: 3})

			Expect(collect()).To(HaveLen(2))
		})
	})
})
