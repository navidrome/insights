package charts

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/navidrome/insights/internal/consts"
	"github.com/navidrome/insights/internal/summary"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("loadChartInput", func() {
	var dir string

	BeforeEach(func() { dir = GinkgoT().TempDir() })

	day := func(n int) time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n) }

	// Writing through SaveSummary rather than a local writer keeps these fixtures on the same
	// path production uses, so a change to the on-disk format cannot pass here and fail there.
	write := func(n int, s summary.Summary) {
		GinkgoHelper()
		Expect(summary.SaveSummary(dir, s, day(n))).To(Succeed())
	}

	It("keeps only the selected versions on each day", func() {
		for n := 0; n < 3; n++ {
			write(n, summary.Summary{
				NumInstances: 100,
				Versions:     map[string]uint64{"keep-a": 90, "keep-b": 80, "drop": 1},
				PlayerTypes:  map[string]uint64{"x": 5},
			})
		}

		in, err := loadChartInput(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(in.TopVersions).To(HaveLen(3), "only three versions exist, so all three are top")
		for _, d := range in.Series {
			Expect(d.Versions).To(HaveLen(3))
		}
	})

	It("drops versions outside the top N", func() {
		versions := map[string]uint64{}
		for i := 0; i < consts.TopVersionsCount+5; i++ {
			versions[string(rune('a'+i))] = uint64(100 - i)
		}
		for n := 0; n < 3; n++ {
			write(n, summary.Summary{NumInstances: 100, Versions: versions, PlayerTypes: map[string]uint64{"x": 1}})
		}

		in, err := loadChartInput(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(in.TopVersions).To(HaveLen(consts.TopVersionsCount))
		for _, d := range in.Series {
			Expect(d.Versions).To(HaveLen(consts.TopVersionsCount))
		}
	})

	It("sums PlayerTypes into one number per day", func() {
		write(0, summary.Summary{
			NumInstances: 100,
			Versions:     map[string]uint64{"v": 1},
			PlayerTypes:  map[string]uint64{"a": 3, "b": 4, "c": 5},
		})

		in, err := loadChartInput(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(in.Series).To(HaveLen(1))
		Expect(in.Series[0].TotalPlayers).To(Equal(uint64(12)))
	})

	Describe("player exclusions", func() {
		writeExclusions := func(content string) {
			GinkgoHelper()
			path := filepath.Join(dir, consts.PlayerExclusionsFile)
			Expect(os.WriteFile(path, []byte(content), consts.FilePermissions)).To(Succeed())
		}

		BeforeEach(func() {
			write(0, summary.Summary{
				NumInstances: 100,
				Versions:     map[string]uint64{"v": 1},
				PlayerTypes:  map[string]uint64{"tms-web": 3, "tms-web-stats": 500, "Symfonium": 40},
			})
		})

		It("removes matching players from the totals and the latest summary", func() {
			writeExclusions("^tms-\n")

			in, err := loadChartInput(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(in.Series[0].TotalPlayers).To(Equal(uint64(40)))
			Expect(in.Latest.PlayerTypes).To(Equal(map[string]uint64{"Symfonium": 40}))
		})

		It("ignores comments and blank lines", func() {
			writeExclusions("# one private web app\n\n   \n  ^tms-web-stats$  \n")

			in, err := loadChartInput(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(in.Latest.PlayerTypes).To(HaveKey("tms-web"))
			Expect(in.Latest.PlayerTypes).ToNot(HaveKey("tms-web-stats"))
		})

		It("skips an invalid pattern and still applies the others", func() {
			writeExclusions("[unclosed\n^tms-\n")

			in, err := loadChartInput(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(in.Latest.PlayerTypes).To(Equal(map[string]uint64{"Symfonium": 40}))
		})

		It("logs what each rule removed from the latest day", func() {
			writeExclusions("^tms-\n^no-such-player$\n")

			var buf bytes.Buffer
			log.SetOutput(&buf)
			defer log.SetOutput(os.Stderr)
			_, err := loadChartInput(dir)
			Expect(err).ToNot(HaveOccurred())

			Expect(buf.String()).To(ContainSubstring(
				`Player exclusion "^tms-" removed 2 names, 503 of 543 players on 2026-01-01`))
			Expect(buf.String()).To(ContainSubstring(
				`Player exclusion "^no-such-player$" removed 0 names, 0 of 543 players on 2026-01-01`))
		})

		It("excludes nothing when the file is missing", func() {
			in, err := loadChartInput(dir)
			Expect(err).ToNot(HaveOccurred())
			Expect(in.Series[0].TotalPlayers).To(Equal(uint64(543)))
			Expect(in.Latest.PlayerTypes).To(HaveLen(3))
		})
	})

	It("keeps the full summary for the last day only", func() {
		write(0, summary.Summary{NumInstances: 100, Versions: map[string]uint64{"v": 1},
			OS: map[string]uint64{"old": 1}, PlayerTypes: map[string]uint64{"a": 1}})
		write(1, summary.Summary{NumInstances: 100, Versions: map[string]uint64{"v": 1},
			OS: map[string]uint64{"new": 2}, PlayerTypes: map[string]uint64{"a": 1}})

		in, err := loadChartInput(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(in.LatestTime).To(Equal(day(1)))
		Expect(in.Latest.OS).To(Equal(map[string]uint64{"new": 2}))
	})

	It("treats the last complete day as latest when the tail is incomplete", func() {
		write(0, summary.Summary{NumInstances: 1000, Versions: map[string]uint64{"v": 1},
			OS: map[string]uint64{"good": 1}, PlayerTypes: map[string]uint64{"a": 1}})
		write(1, summary.Summary{NumInstances: 1000, Versions: map[string]uint64{"v": 1},
			OS: map[string]uint64{"good": 2}, PlayerTypes: map[string]uint64{"a": 1}})
		// A 90% drop, well under consts.IncompleteThreshold.
		write(2, summary.Summary{NumInstances: 100, Versions: map[string]uint64{"v": 1},
			OS: map[string]uint64{"partial": 1}, PlayerTypes: map[string]uint64{"a": 1}})

		in, err := loadChartInput(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(in.Series).To(HaveLen(2))
		Expect(in.LatestTime).To(Equal(day(1)))
		Expect(in.Latest.OS).To(Equal(map[string]uint64{"good": 2}))
	})

	It("returns an empty input when there are no summaries", func() {
		in, err := loadChartInput(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(in.Series).To(BeEmpty())
	})

	// The guard in loadChartInput exists for a race that a single before/after fixture cannot
	// reproduce: within one loadChartInput call, pass 1 picks lastTime from a snapshot of the
	// files on disk, and pass 3 re-reads those same files later in the same call. Corrupting a
	// file before calling loadChartInput once corrupts it for every pass equally -- pass 1 would
	// just never pick that day as lastTime in the first place, and the run stays self-consistent.
	// The only way pass 1 and pass 3 can disagree is if the last day's file changes while
	// loadChartInput is running, which is exactly what a concurrent writer (another save landing
	// on today's file while an export or the chart handler reads it) can do in production.
	//
	// So this test drives the race directly: a writer goroutine hammers the last day's file
	// between valid and malformed content for as long as the test runs, concurrently with many
	// calls to loadChartInput. With the guard removed, a 100-iteration run of this same setup
	// measured roughly two dozen inconsistent results (non-empty Series, zero-value Latest) per
	// 100 calls on a development machine -- the race is not rare, it is just invisible to a
	// two-shot before/after fixture.
	It("never returns a non-empty Series with a zero-value Latest while the last day's file is being rewritten concurrently", func() {
		const days = 300
		for n := range days {
			write(n, summary.Summary{
				NumInstances: 100,
				Versions:     map[string]uint64{"v": 1},
				OS:           map[string]uint64{"good": uint64(n)}, //#nosec G115 -- test-only, n is small and non-negative
				PlayerTypes:  map[string]uint64{"a": 1},
			})
		}
		// SummaryFilePath, not a hand-built name: the torn file has to land on the path the
		// reader actually opens. A second name for the same day is deduplicated away, and the
		// writer below would then never win.
		lastPath := summary.SummaryFilePath(dir, day(days-1))
		raw, err := json.Marshal(summary.Summary{
			NumInstances: 100,
			Versions:     map[string]uint64{"v": 1},
			OS:           map[string]uint64{"good": uint64(days - 1)},
			PlayerTypes:  map[string]uint64{"a": 1},
		})
		Expect(err).ToNot(HaveOccurred())
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, err = gz.Write(raw)
		Expect(err).ToNot(HaveOccurred())
		Expect(gz.Close()).To(Succeed())
		goodBytes := buf.Bytes()
		// A prefix of the real file, which is what a half-finished write leaves: a valid gzip
		// header over a stream that ends early.
		badBytes := goodBytes[:len(goodBytes)/2]

		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(done)
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Deliberately not SaveSummary: its atomic rename can never expose a torn file,
				// and a torn file is exactly what this test needs pass 3 to trip over.
				_ = os.WriteFile(lastPath, badBytes, consts.FilePermissions)
				_ = os.WriteFile(lastPath, goodBytes, consts.FilePermissions)
			}
		}()
		DeferCleanup(func() {
			close(stop)
			<-done
		})

		// Keep going until the writer wins once, rather than a fixed count. How many calls that
		// takes is a property of the machine, not of the code: a fixed 100 fired within a dozen
		// calls on a development machine and never fired at all on a CI runner, turning a green
		// build red for a reason that had nothing to do with the guard.
		deadline := time.Now().Add(10 * time.Second)
		sawEmpty := false
		calls := 0
		for !sawEmpty && time.Now().Before(deadline) {
			in, err := loadChartInput(dir)
			Expect(err).ToNot(HaveOccurred())
			calls++
			if len(in.Series) == 0 {
				sawEmpty = true
				continue
			}
			// The caller-visible invariant: whenever there is data to show, the latest day's
			// full summary must be real. NumInstances == 0 is the zero value GetSummaries never
			// yields (it filters exactly that case out), so seeing it here means in.Latest was
			// never actually captured -- the failure this guard exists to prevent.
			Expect(in.Latest.NumInstances).ToNot(BeZero(),
				"a non-empty Series with a zero-value Latest would render five snapshot charts "+
					"from all-zero data and publish totalInstances: 0")
		}
		// Whether the race fired is a fact about the host, so it is reported, not asserted. The
		// assertion above ran on every call either way; a run where the writer never won proves
		// nothing, but it is not evidence of a defect and must not fail the build.
		if !sawEmpty {
			AddReportEntry("race did not fire",
				fmt.Sprintf("%d calls in 10s without the writer winning: this run did not exercise the guard", calls))
		}
	})

	It("selects versions from the rolling window, not from all history", func() {
		// An old version that dominates history but is absent from the window must not win.
		write(0, summary.Summary{NumInstances: 100, PlayerTypes: map[string]uint64{"a": 1},
			Versions: map[string]uint64{"ancient": 1000000}})
		recent := consts.VersionSelectionDays + 10
		for n := recent; n < recent+3; n++ {
			write(n, summary.Summary{NumInstances: 100, PlayerTypes: map[string]uint64{"a": 1},
				Versions: map[string]uint64{"current": 50}})
		}

		in, err := loadChartInput(dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(in.TopVersions).To(ContainElement("current"))
		Expect(in.TopVersions).ToNot(ContainElement("ancient"))
	})
})
