package charts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-echarts/go-echarts/v2/charts"
	"github.com/go-echarts/go-echarts/v2/opts"
	"github.com/navidrome/insights/internal/consts"
	"github.com/navidrome/insights/internal/summary"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCharts(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Charts Suite")
}

var _ = Describe("Charts", func() {
	var tempDir string

	BeforeEach(func() {
		var err error
		tempDir, err = os.MkdirTemp("", "charts-test")
		Expect(err).NotTo(HaveOccurred())

	})

	AfterEach(func() {
		Expect(os.RemoveAll(tempDir)).To(Succeed())
	})

	Describe("buildTimeSeriesData", func() {
		It("returns empty data for empty series", func() {
			ts := buildTimeSeriesData([]daySeries{})
			Expect(ts.Dates).To(BeEmpty())
			Expect(ts.Lookup).To(BeEmpty())
		})

		It("creates continuous date range without gaps", func() {
			series := []daySeries{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), NumInstances: 100},
				{Time: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), NumInstances: 110},
				{Time: time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), NumInstances: 120},
			}
			ts := buildTimeSeriesData(series)
			Expect(ts.Dates).To(HaveLen(3))
			Expect(ts.Dates[0]).To(Equal("Jan 01, 2025"))
			Expect(ts.Dates[1]).To(Equal("Jan 02, 2025"))
			Expect(ts.Dates[2]).To(Equal("Jan 03, 2025"))
			// All dates should have data
			for i := 0; i < 3; i++ {
				date := time.Date(2025, 1, i+1, 0, 0, 0, 0, time.UTC)
				Expect(ts.Lookup[date]).NotTo(BeNil())
			}
		})

		It("fills gaps in date range with nil entries", func() {
			series := []daySeries{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), NumInstances: 100},
				{Time: time.Date(2025, 1, 5, 0, 0, 0, 0, time.UTC), NumInstances: 150},
			}
			ts := buildTimeSeriesData(series)
			// Should have 5 dates: Jan 1, 2, 3, 4, 5
			Expect(ts.Dates).To(HaveLen(5))
			Expect(ts.Dates[0]).To(Equal("Jan 01, 2025"))
			Expect(ts.Dates[4]).To(Equal("Jan 05, 2025"))
			Expect(ts.Start).To(Equal(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)))

			// Jan 1 and Jan 5 should have data
			Expect(ts.Lookup[time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)]).NotTo(BeNil())
			Expect(ts.Lookup[time.Date(2025, 1, 5, 0, 0, 0, 0, time.UTC)]).NotTo(BeNil())

			// Jan 2, 3, 4 should be nil (missing data)
			Expect(ts.Lookup[time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)]).To(BeNil())
			Expect(ts.Lookup[time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC)]).To(BeNil())
			Expect(ts.Lookup[time.Date(2025, 1, 4, 0, 0, 0, 0, time.UTC)]).To(BeNil())
		})
	})

	Describe("findGaps", func() {
		It("returns empty for empty time series", func() {
			ts := buildTimeSeriesData([]daySeries{})
			gaps := ts.findGaps()
			Expect(gaps).To(BeEmpty())
		})

		It("returns empty when no gaps exist", func() {
			series := []daySeries{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), NumInstances: 100},
				{Time: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), NumInstances: 110},
				{Time: time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), NumInstances: 120},
			}
			ts := buildTimeSeriesData(series)
			gaps := ts.findGaps()
			Expect(gaps).To(BeEmpty())
		})

		It("finds a single gap", func() {
			series := []daySeries{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), NumInstances: 100},
				{Time: time.Date(2025, 1, 5, 0, 0, 0, 0, time.UTC), NumInstances: 150},
			}
			ts := buildTimeSeriesData(series)
			gaps := ts.findGaps()
			Expect(gaps).To(HaveLen(1))
			Expect(gaps[0].StartDate).To(Equal("Jan 02, 2025"))
			Expect(gaps[0].EndDate).To(Equal("Jan 04, 2025"))
		})

		It("finds multiple gaps", func() {
			series := []daySeries{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), NumInstances: 100},
				{Time: time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), NumInstances: 110},
				{Time: time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC), NumInstances: 120},
			}
			ts := buildTimeSeriesData(series)
			gaps := ts.findGaps()
			Expect(gaps).To(HaveLen(2))
			// First gap: Jan 2
			Expect(gaps[0].StartDate).To(Equal("Jan 02, 2025"))
			Expect(gaps[0].EndDate).To(Equal("Jan 02, 2025"))
			// Second gap: Jan 4-5
			Expect(gaps[1].StartDate).To(Equal("Jan 04, 2025"))
			Expect(gaps[1].EndDate).To(Equal("Jan 05, 2025"))
		})
	})

	Describe("ChartsHandler", func() {
		It("returns 404 when no data available", func() {
			handler := ChartsHandler(tempDir)
			req := httptest.NewRequest(http.MethodGet, "/charts", nil)
			w := httptest.NewRecorder()

			handler(w, req)

			Expect(w.Code).To(Equal(http.StatusNotFound))
			Expect(w.Body.String()).To(ContainSubstring("No data available"))
		})

		It("returns HTML with chart when data exists", func() {
			s := summary.Summary{
				NumInstances: 100,
				Versions:     map[string]uint64{"0.54.0": 50, "0.54.1": 50},
				Players:      map[string]uint64{"0": 10, "1": 50, "2": 30},
				Tracks:       map[string]uint64{"0": 5, "1000": 40, "10000": 30},
			}
			// Insert 3 days of data (last 2 are excluded)
			err := summary.SaveSummary(tempDir, s, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = summary.SaveSummary(tempDir, s, time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = summary.SaveSummary(tempDir, s, time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())

			handler := ChartsHandler(tempDir)
			req := httptest.NewRequest(http.MethodGet, "/charts", nil)
			w := httptest.NewRecorder()

			handler(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Header().Get("Content-Type")).To(Equal("text/html"))
			body := w.Body.String()
			Expect(body).To(ContainSubstring("Navidrome Insights"))
			Expect(body).To(ContainSubstring("Number of Navidrome Installations"))
			Expect(body).To(ContainSubstring("Operating systems and architectures"))
			Expect(body).To(ContainSubstring("Client types"))
			Expect(body).To(ContainSubstring("Number of Active Clients"))
			Expect(body).To(ContainSubstring("Active Clients per Installation"))
			Expect(body).To(ContainSubstring("Number of Tracks in Library"))
			Expect(body).To(ContainSubstring("echarts"))
		})
	})

	Describe("buildOSChart", func() {
		It("orders slices the same way on every run", func() {
			latest := summary.Summary{OS: map[string]uint64{
				"linux - amd64": 7, "darwin - arm64": 7, "windows - amd64": 7,
				"freebsd - arm64": 7, "linux - 386": 7,
			}}

			names := func() []string {
				var out []string
				for _, d := range buildOSChart(latest).MultiSeries[0].Data.([]opts.PieData) {
					out = append(out, d.Name)
				}
				return out
			}

			first := names()
			for i := 0; i < 20; i++ {
				Expect(names()).To(Equal(first))
			}
		})

		It("returns pie chart with data from latest summary", func() {
			latest := summary.Summary{OS: map[string]uint64{"Linux - amd64": 20, "macOS - arm64": 5}}

			chart := buildOSChart(latest)
			Expect(chart).NotTo(BeNil())
		})
	})

	Describe("buildPlayerTypesChart", func() {
		// pieNames reads the slice labels back off the built chart.
		pieNames := func(pie *charts.Pie) []string {
			GinkgoHelper()
			var out []string
			for _, d := range pie.MultiSeries[0].Data.([]opts.PieData) {
				out = append(out, d.Name)
			}
			return out
		}

		It("orders slices the same way on every run", func() {
			// Equal counts must not depend on Go's randomised map iteration order.
			data := map[string]uint64{"alpha": 10, "bravo": 10, "charlie": 10, "delta": 10, "echo": 10}
			latest := summary.Summary{PlayerTypes: data}

			first := pieNames(buildPlayerTypesChart(latest))
			for i := 0; i < 20; i++ {
				Expect(pieNames(buildPlayerTypesChart(latest))).To(Equal(first))
			}
		})

		It("returns pie chart with data from latest summary", func() {
			latest := summary.Summary{PlayerTypes: map[string]uint64{"NavidromeUI": 20, "Supersonic": 15, "Audioling": 5}}

			chart := buildPlayerTypesChart(latest)
			Expect(chart).NotTo(BeNil())
		})

		It("groups players with less than 0.2% into Others", func() {
			// Total: 1000, threshold: 2 (0.2%)
			// PlayerA: 500 (50%) - kept
			// PlayerB: 300 (30%) - kept
			// PlayerC: 100 (10%) - kept
			// PlayerD: 50 (5%) - kept
			// PlayerE: 40 (4%) - kept
			// PlayerF: 5 (0.5%) - kept
			// PlayerG: 3 (0.3%) - kept
			// PlayerH: 1 (0.1%) - grouped into Others
			// PlayerI: 1 (0.1%) - grouped into Others
			latest := summary.Summary{PlayerTypes: map[string]uint64{
				"PlayerA": 500,
				"PlayerB": 300,
				"PlayerC": 100,
				"PlayerD": 50,
				"PlayerE": 40,
				"PlayerF": 5,
				"PlayerG": 3,
				"PlayerH": 1,
				"PlayerI": 1,
			}}

			chart := buildPlayerTypesChart(latest)
			Expect(chart).NotTo(BeNil())

			// Marshal chart to JSON and verify content
			jsonBytes, err := json.Marshal(chart.JSON())
			Expect(err).NotTo(HaveOccurred())
			jsonStr := string(jsonBytes)

			// Should include major players
			Expect(jsonStr).To(ContainSubstring("PlayerA"))
			Expect(jsonStr).To(ContainSubstring("PlayerB"))
			Expect(jsonStr).To(ContainSubstring("PlayerC"))
			Expect(jsonStr).To(ContainSubstring("PlayerD"))
			Expect(jsonStr).To(ContainSubstring("PlayerE"))
			Expect(jsonStr).To(ContainSubstring("PlayerF"))
			Expect(jsonStr).To(ContainSubstring("PlayerG"))
			// Should have Others bucket
			Expect(jsonStr).To(ContainSubstring("Others"))
			// Should NOT include small players individually
			Expect(jsonStr).NotTo(ContainSubstring("PlayerH"))
			Expect(jsonStr).NotTo(ContainSubstring("PlayerI"))
		})
	})

	Describe("buildPlayersChart", func() {
		It("returns line chart with player totals over time", func() {
			series := []daySeries{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), TotalPlayers: 15},
				{Time: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), TotalPlayers: 35},
			}

			chart := buildPlayersChart(series)
			Expect(chart).NotTo(BeNil())
		})

		It("handles a day with no players", func() {
			series := []daySeries{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), TotalPlayers: 0},
			}

			chart := buildPlayersChart(series)
			Expect(chart).NotTo(BeNil())
		})
	})

	Describe("buildPlayersPerInstallationChart", func() {
		It("returns bar chart with player distribution from latest summary", func() {
			latest := summary.Summary{Players: map[string]uint64{"0": 100, "1": 500, "2": 200, "3": 50}}

			chart := buildPlayersPerInstallationChart(latest)
			Expect(chart).NotTo(BeNil())
		})

		It("handles empty players data", func() {
			latest := summary.Summary{Players: map[string]uint64{}}

			chart := buildPlayersPerInstallationChart(latest)
			Expect(chart).NotTo(BeNil())
		})
	})

	Describe("buildTracksChart", func() {
		It("returns horizontal bar chart with track distribution from latest summary", func() {
			latest := summary.Summary{Tracks: map[string]uint64{"0": 50, "1000": 200, "10000": 150, "50000": 80}}

			chart := buildTracksChart(latest)
			Expect(chart).NotTo(BeNil())
		})

		It("handles empty tracks data", func() {
			latest := summary.Summary{Tracks: map[string]uint64{}}

			chart := buildTracksChart(latest)
			Expect(chart).NotTo(BeNil())
		})
	})

	Describe("buildAlbumsArtistsChart", func() {
		It("returns horizontal bar chart with albums and artists distribution from latest summary", func() {
			latest := summary.Summary{
				Albums:  map[string]uint64{"0": 50, "100": 200, "1000": 150, "5000": 80},
				Artists: map[string]uint64{"0": 40, "100": 180, "1000": 120, "5000": 60},
			}

			chart := buildAlbumsArtistsChart(latest)
			Expect(chart).NotTo(BeNil())
		})

		It("handles empty albums and artists data", func() {
			latest := summary.Summary{Albums: map[string]uint64{}, Artists: map[string]uint64{}}

			chart := buildAlbumsArtistsChart(latest)
			Expect(chart).NotTo(BeNil())
		})
	})

	Describe("getTopKeys", func() {
		It("returns top N keys sorted by value descending", func() {
			m := map[string]uint64{
				"a": 10,
				"b": 50,
				"c": 30,
				"d": 20,
			}
			result := getTopKeys(m, 2)
			Expect(result).To(HaveLen(2))
			Expect(result).To(ContainElements("b", "c"))
		})

		It("returns all keys if N exceeds map size", func() {
			m := map[string]uint64{
				"a": 10,
				"b": 20,
			}
			result := getTopKeys(m, 10)
			Expect(result).To(HaveLen(2))
		})

		It("handles empty map", func() {
			m := map[string]uint64{}
			result := getTopKeys(m, 5)
			Expect(result).To(BeEmpty())
		})
	})

	// Rolling-window version selection now happens in loadChartInput (see
	// "selects versions from the rolling window, not from all history" and
	// "drops versions outside the top N" in load_test.go). buildVersionsChart only receives
	// the already-selected list, so what is left to test here is that it buckets whatever
	// falls outside that list into "Others" rather than dropping it or giving it a series.
	Describe("buildVersionsChart others bucket", func() {
		It("sums versions outside top into Others, not as an individual series", func() {
			series := []daySeries{{
				Time:         time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
				NumInstances: 100,
				Versions:     map[string]uint64{"v1": 90, "v2": 10},
			}}
			top := []string{"v1"}

			chart := buildVersionsChart(series, top)
			Expect(chart).NotTo(BeNil())

			jsonBytes, err := json.Marshal(chart.JSON())
			Expect(err).NotTo(HaveOccurred())
			jsonStr := string(jsonBytes)

			Expect(jsonStr).To(ContainSubstring("v1"))
			Expect(jsonStr).NotTo(ContainSubstring("v2"))
		})
	})

	Describe("ExportChartsJSON", func() {
		// The output directory is derived from the data folder now, not passed in, so there is
		// one place charts can land and the spec asserts against that place.
		var outputDir string

		BeforeEach(func() {
			outputDir = filepath.Join(tempDir, "web", "chartdata")
		})

		It("does nothing when no summaries exist", func() {
			err := ExportChartsJSON(tempDir)
			Expect(err).NotTo(HaveOccurred())

			// File should not be created
			_, err = os.Stat(filepath.Join(outputDir, "charts.json"))
			Expect(os.IsNotExist(err)).To(BeTrue())
		})

		It("exports charts JSON when data exists", func() {
			s := summary.Summary{
				NumInstances: 100,
				Versions:     map[string]uint64{"0.54.0": 50, "0.54.1": 50},
				OS:           map[string]uint64{"Linux - amd64": 80, "macOS - arm64": 20},
				PlayerTypes:  map[string]uint64{"NavidromeUI": 50, "Supersonic": 30},
				Players:      map[string]uint64{"0": 10, "1": 50, "2": 30},
				Tracks:       map[string]uint64{"0": 5, "1000": 40, "10000": 30},
			}
			// Insert 3 days of data
			err := summary.SaveSummary(tempDir, s, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = summary.SaveSummary(tempDir, s, time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = summary.SaveSummary(tempDir, s, time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())

			err = ExportChartsJSON(tempDir)
			Expect(err).NotTo(HaveOccurred())

			// Verify file exists
			jsonPath := filepath.Join(outputDir, "charts.json")
			data, err := os.ReadFile(jsonPath) //#nosec G304 -- test file path
			Expect(err).NotTo(HaveOccurred())

			// Verify JSON structure (object with metadata + charts array)
			var output map[string]interface{}
			err = json.Unmarshal(data, &output)
			Expect(err).NotTo(HaveOccurred())

			// Verify metadata fields
			Expect(output["totalInstances"]).To(BeEquivalentTo(100))
			Expect(output["lastUpdated"]).NotTo(BeNil())

			// Verify charts array
			chartsData := output["charts"].([]interface{})
			Expect(chartsData).To(HaveLen(6))
			Expect(chartsData[0].(map[string]interface{})["id"]).To(Equal("versions"))
			Expect(chartsData[1].(map[string]interface{})["id"]).To(Equal("os"))
			Expect(chartsData[2].(map[string]interface{})["id"]).To(Equal("players"))
			Expect(chartsData[3].(map[string]interface{})["id"]).To(Equal("playerTypes"))
			// Expect(chartsData[4].(map[string]interface{})["id"]).To(Equal("playersPerInstallation"))
			Expect(chartsData[4].(map[string]interface{})["id"]).To(Equal("tracks"))
			Expect(chartsData[5].(map[string]interface{})["id"]).To(Equal("albumsArtists"))
		})

		// cmd/process serves this file at /api/charts, so an O_TRUNC rewrite would answer a
		// request landing in the window with a truncated body.
		It("republishes charts.json by rename rather than truncating it in place", func() {
			s := summary.Summary{
				NumInstances: 100,
				Versions:     map[string]uint64{"0.54.0": 100},
				OS:           map[string]uint64{"Linux - amd64": 100},
			}
			for day := 1; day <= 3; day++ {
				Expect(summary.SaveSummary(tempDir, s, time.Date(2025, 1, day, 0, 0, 0, 0, time.UTC))).To(Succeed())
			}
			jsonPath := filepath.Join(outputDir, "charts.json")
			Expect(ExportChartsJSON(tempDir)).To(Succeed())
			before, err := os.Stat(jsonPath)
			Expect(err).NotTo(HaveOccurred())

			Expect(ExportChartsJSON(tempDir)).To(Succeed())

			after, err := os.Stat(jsonPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.SameFile(before, after)).To(BeFalse(),
				"the served name must be pointed at a finished file, not at one being rewritten under it")

			// And no temp file is left in the published directory.
			found, err := os.ReadDir(outputDir)
			Expect(err).NotTo(HaveOccurred())
			var names []string
			for _, e := range found {
				names = append(names, e.Name())
			}
			Expect(names).To(ConsistOf("charts.json"))
		})
	})

	Describe("getTopKeys", func() {
		It("orders ties by name so the result does not depend on map order", func() {
			m := map[string]uint64{"delta": 5, "alpha": 5, "charlie": 5, "bravo": 5, "echo": 5}

			first := getTopKeys(m, 3)
			for i := 0; i < 50; i++ {
				Expect(getTopKeys(m, 3)).To(Equal(first))
			}
			Expect(first).To(Equal([]string{"alpha", "bravo", "charlie"}))
		})

		It("still orders by count descending before falling back to the name", func() {
			m := map[string]uint64{"zulu": 9, "alpha": 1, "mike": 5}

			Expect(getTopKeys(m, 3)).To(Equal([]string{"zulu", "mike", "alpha"}))
		})
	})

	Describe("buildVersionsChart", func() {
		It("orders series the same way on every run when counts tie", func() {
			tied := map[string]uint64{"1.0.0": 10, "1.0.1": 10, "1.0.2": 10, "1.0.3": 10}
			var series []daySeries
			for n := 1; n <= 3; n++ {
				series = append(series, daySeries{
					Time:         time.Date(2025, 1, n, 0, 0, 0, 0, time.UTC),
					NumInstances: 100,
					Versions:     tied,
				})
			}
			top := getTopKeys(tied, consts.TopVersionsCount)
			sortVersionsByLastDay(top, tied)

			names := func() []string {
				var out []string
				for _, s := range buildVersionsChart(series, top).MultiSeries {
					out = append(out, s.Name)
				}
				return out
			}

			first := names()
			for i := 0; i < 30; i++ {
				Expect(names()).To(Equal(first))
			}
		})
	})

})
