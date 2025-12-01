package charts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/navidrome/insights/summary"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCharts(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Charts Suite")
}

var _ = Describe("Charts", func() {
	var tempDir string
	var originalDataFolder string

	BeforeEach(func() {
		var err error
		tempDir, err = os.MkdirTemp("", "charts-test")
		Expect(err).NotTo(HaveOccurred())

		// Set DATA_FOLDER to temp directory for tests
		originalDataFolder = os.Getenv("DATA_FOLDER")
		os.Setenv("DATA_FOLDER", tempDir)
	})

	AfterEach(func() {
		os.RemoveAll(tempDir)
		os.Setenv("DATA_FOLDER", originalDataFolder)
	})

	Describe("ExcludeIncompleteDays", func() {
		It("returns nil when summaries are empty", func() {
			Expect(ExcludeIncompleteDays(nil)).To(BeNil())
			Expect(ExcludeIncompleteDays([]summary.SummaryRecord{})).To(BeNil())
		})

		It("returns all summaries when no significant drops", func() {
			summaries := []summary.SummaryRecord{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 100}},
				{Time: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 105}},
				{Time: time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 110}},
				{Time: time.Date(2025, 1, 4, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 115}},
			}
			result := ExcludeIncompleteDays(summaries)
			Expect(result).To(HaveLen(4))
		})

		It("removes trailing days with significant drops (incomplete data)", func() {
			summaries := []summary.SummaryRecord{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 1000}},
				{Time: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 1050}},
				{Time: time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 1100}},
				{Time: time.Date(2025, 1, 4, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 700}}, // 36% drop - incomplete
				{Time: time.Date(2025, 1, 5, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 100}}, // even more incomplete
				{Time: time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 50}},  // even more incomplete
			}
			result := ExcludeIncompleteDays(summaries)
			// Jan 6 has 50 vs Jan 5's 100 (50% drop) -> removed
			// Jan 5 has 100 vs Jan 4's 700 (86% drop) -> removed
			// Jan 4 has 700 vs Jan 3's 1100 (36% drop) -> removed
			// Result: Jan 1, 2, 3
			Expect(result).To(HaveLen(3))
			Expect(result[2].Data.NumInstances).To(Equal(int64(1100)))
		})
	})

	Describe("buildTimeSeriesData", func() {
		It("returns empty data for empty summaries", func() {
			ts := buildTimeSeriesData([]summary.SummaryRecord{})
			Expect(ts.Dates).To(BeEmpty())
			Expect(ts.Lookup).To(BeEmpty())
		})

		It("creates continuous date range without gaps", func() {
			summaries := []summary.SummaryRecord{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 100}},
				{Time: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 110}},
				{Time: time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 120}},
			}
			ts := buildTimeSeriesData(summaries)
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
			summaries := []summary.SummaryRecord{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 100}},
				{Time: time.Date(2025, 1, 5, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 150}},
			}
			ts := buildTimeSeriesData(summaries)
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
			ts := buildTimeSeriesData([]summary.SummaryRecord{})
			gaps := ts.findGaps()
			Expect(gaps).To(BeEmpty())
		})

		It("returns empty when no gaps exist", func() {
			summaries := []summary.SummaryRecord{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 100}},
				{Time: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 110}},
				{Time: time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 120}},
			}
			ts := buildTimeSeriesData(summaries)
			gaps := ts.findGaps()
			Expect(gaps).To(BeEmpty())
		})

		It("finds a single gap", func() {
			summaries := []summary.SummaryRecord{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 100}},
				{Time: time.Date(2025, 1, 5, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 150}},
			}
			ts := buildTimeSeriesData(summaries)
			gaps := ts.findGaps()
			Expect(gaps).To(HaveLen(1))
			Expect(gaps[0].StartDate).To(Equal("Jan 02, 2025"))
			Expect(gaps[0].EndDate).To(Equal("Jan 04, 2025"))
		})

		It("finds multiple gaps", func() {
			summaries := []summary.SummaryRecord{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 100}},
				{Time: time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 110}},
				{Time: time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC), Data: summary.Summary{NumInstances: 120}},
			}
			ts := buildTimeSeriesData(summaries)
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

	Describe("GetSummaries", func() {
		It("returns empty slice when no summaries exist", func() {
			summaries, err := summary.GetSummaries()
			Expect(err).NotTo(HaveOccurred())
			Expect(summaries).To(BeEmpty())
		})

		It("returns summaries ordered by time ascending", func() {
			// Insert test summaries
			summary1 := summary.Summary{NumInstances: 100, Versions: map[string]uint64{"0.54.0": 50, "0.54.1": 50}}
			summary2 := summary.Summary{NumInstances: 150, Versions: map[string]uint64{"0.54.0": 60, "0.54.1": 90}}

			err := summary.SaveSummary(summary1, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = summary.SaveSummary(summary2, time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())

			summaries, err := summary.GetSummaries()
			Expect(err).NotTo(HaveOccurred())
			Expect(summaries).To(HaveLen(2))
			Expect(summaries[0].Time.Day()).To(Equal(1))
			Expect(summaries[1].Time.Day()).To(Equal(2))
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(100)))
			Expect(summaries[1].Data.NumInstances).To(Equal(int64(150)))
		})

		It("skips empty summaries where NumInstances is 0", func() {
			summary1 := summary.Summary{NumInstances: 100, Versions: map[string]uint64{"0.54.0": 100}}
			summary2 := summary.Summary{NumInstances: 0} // Empty summary
			summary3 := summary.Summary{NumInstances: 200, Versions: map[string]uint64{"0.54.0": 200}}

			err := summary.SaveSummary(summary1, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = summary.SaveSummary(summary2, time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = summary.SaveSummary(summary3, time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())

			summaries, err := summary.GetSummaries()
			Expect(err).NotTo(HaveOccurred())
			Expect(summaries).To(HaveLen(2))
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(100)))
			Expect(summaries[1].Data.NumInstances).To(Equal(int64(200)))
		})
	})

	Describe("ChartsHandler", func() {
		It("returns 404 when no data available", func() {
			handler := ChartsHandler()
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
			err := summary.SaveSummary(s, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = summary.SaveSummary(s, time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = summary.SaveSummary(s, time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())

			handler := ChartsHandler()
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
		It("returns nil when no summaries exist", func() {
			chart := buildOSChart([]summary.SummaryRecord{})
			Expect(chart).To(BeNil())
		})

		It("returns pie chart with data from latest summary", func() {
			summaries := []summary.SummaryRecord{
				{
					Time: time.Now().Add(-24 * time.Hour),
					Data: summary.Summary{OS: map[string]uint64{"Linux - amd64": 10}},
				},
				{
					Time: time.Now(),
					Data: summary.Summary{OS: map[string]uint64{"Linux - amd64": 20, "macOS - arm64": 5}},
				},
			}

			chart := buildOSChart(summaries)
			Expect(chart).NotTo(BeNil())
		})
	})

	Describe("buildPlayerTypesChart", func() {
		It("returns nil when no summaries exist", func() {
			chart := buildPlayerTypesChart([]summary.SummaryRecord{})
			Expect(chart).To(BeNil())
		})

		It("returns pie chart with data from latest summary", func() {
			summaries := []summary.SummaryRecord{
				{
					Time: time.Now().Add(-24 * time.Hour),
					Data: summary.Summary{PlayerTypes: map[string]uint64{"NavidromeUI": 10}},
				},
				{
					Time: time.Now(),
					Data: summary.Summary{PlayerTypes: map[string]uint64{"NavidromeUI": 20, "Supersonic": 15, "Audioling": 5}},
				},
			}

			chart := buildPlayerTypesChart(summaries)
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
			summaries := []summary.SummaryRecord{
				{
					Time: time.Now(),
					Data: summary.Summary{PlayerTypes: map[string]uint64{
						"PlayerA": 500,
						"PlayerB": 300,
						"PlayerC": 100,
						"PlayerD": 50,
						"PlayerE": 40,
						"PlayerF": 5,
						"PlayerG": 3,
						"PlayerH": 1,
						"PlayerI": 1,
					}},
				},
			}

			chart := buildPlayerTypesChart(summaries)
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
			summaries := []summary.SummaryRecord{
				{
					Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
					Data: summary.Summary{PlayerTypes: map[string]uint64{"NavidromeUI": 10, "Supersonic": 5}},
				},
				{
					Time: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC),
					Data: summary.Summary{PlayerTypes: map[string]uint64{"NavidromeUI": 20, "Supersonic": 10, "Audioling": 5}},
				},
			}

			chart := buildPlayersChart(summaries)
			Expect(chart).NotTo(BeNil())
		})

		It("handles empty player types", func() {
			summaries := []summary.SummaryRecord{
				{
					Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
					Data: summary.Summary{PlayerTypes: map[string]uint64{}},
				},
			}

			chart := buildPlayersChart(summaries)
			Expect(chart).NotTo(BeNil())
		})
	})

	Describe("buildPlayersPerInstallationChart", func() {
		It("returns nil when no summaries exist", func() {
			chart := buildPlayersPerInstallationChart([]summary.SummaryRecord{})
			Expect(chart).To(BeNil())
		})

		It("returns bar chart with player distribution from latest summary", func() {
			summaries := []summary.SummaryRecord{
				{
					Time: time.Now(),
					Data: summary.Summary{Players: map[string]uint64{"0": 100, "1": 500, "2": 200, "3": 50}},
				},
			}

			chart := buildPlayersPerInstallationChart(summaries)
			Expect(chart).NotTo(BeNil())
		})

		It("handles empty players data", func() {
			summaries := []summary.SummaryRecord{
				{
					Time: time.Now(),
					Data: summary.Summary{Players: map[string]uint64{}},
				},
			}

			chart := buildPlayersPerInstallationChart(summaries)
			Expect(chart).NotTo(BeNil())
		})
	})

	Describe("buildTracksChart", func() {
		It("returns nil when no summaries exist", func() {
			chart := buildTracksChart([]summary.SummaryRecord{})
			Expect(chart).To(BeNil())
		})

		It("returns horizontal bar chart with track distribution from latest summary", func() {
			summaries := []summary.SummaryRecord{
				{
					Time: time.Now(),
					Data: summary.Summary{Tracks: map[string]uint64{"0": 50, "1000": 200, "10000": 150, "50000": 80}},
				},
			}

			chart := buildTracksChart(summaries)
			Expect(chart).NotTo(BeNil())
		})

		It("handles empty tracks data", func() {
			summaries := []summary.SummaryRecord{
				{
					Time: time.Now(),
					Data: summary.Summary{Tracks: map[string]uint64{}},
				},
			}

			chart := buildTracksChart(summaries)
			Expect(chart).NotTo(BeNil())
		})
	})

	Describe("buildAlbumsArtistsChart", func() {
		It("returns nil when no summaries exist", func() {
			chart := buildAlbumsArtistsChart([]summary.SummaryRecord{})
			Expect(chart).To(BeNil())
		})

		It("returns horizontal bar chart with albums and artists distribution from latest summary", func() {
			summaries := []summary.SummaryRecord{
				{
					Time: time.Now(),
					Data: summary.Summary{
						Albums:  map[string]uint64{"0": 50, "100": 200, "1000": 150, "5000": 80},
						Artists: map[string]uint64{"0": 40, "100": 180, "1000": 120, "5000": 60},
					},
				},
			}

			chart := buildAlbumsArtistsChart(summaries)
			Expect(chart).NotTo(BeNil())
		})

		It("handles empty albums and artists data", func() {
			summaries := []summary.SummaryRecord{
				{
					Time: time.Now(),
					Data: summary.Summary{Albums: map[string]uint64{}, Artists: map[string]uint64{}},
				},
			}

			chart := buildAlbumsArtistsChart(summaries)
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

	Describe("ExportChartsJSON", func() {
		var outputDir string

		BeforeEach(func() {
			var err error
			outputDir, err = os.MkdirTemp("", "charts-output")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			os.RemoveAll(outputDir)
		})

		It("does nothing when no summaries exist", func() {
			err := ExportChartsJSON(outputDir)
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
			err := summary.SaveSummary(s, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = summary.SaveSummary(s, time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = summary.SaveSummary(s, time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())

			err = ExportChartsJSON(outputDir)
			Expect(err).NotTo(HaveOccurred())

			// Verify file exists
			jsonPath := filepath.Join(outputDir, "charts.json")
			data, err := os.ReadFile(jsonPath)
			Expect(err).NotTo(HaveOccurred())

			// Verify JSON structure (array of charts)
			var chartsData []map[string]interface{}
			err = json.Unmarshal(data, &chartsData)
			Expect(err).NotTo(HaveOccurred())
			Expect(chartsData).To(HaveLen(6))
			Expect(chartsData[0]["id"]).To(Equal("versions"))
			Expect(chartsData[1]["id"]).To(Equal("os"))
			Expect(chartsData[2]["id"]).To(Equal("players"))
			Expect(chartsData[3]["id"]).To(Equal("playerTypes"))
			// Expect(chartsData[4]["id"]).To(Equal("playersPerInstallation"))
			Expect(chartsData[4]["id"]).To(Equal("tracks"))
			Expect(chartsData[5]["id"]).To(Equal("albumsArtists"))
		})
	})
})
