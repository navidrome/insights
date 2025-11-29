package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Charts", func() {
	var db *sql.DB

	BeforeEach(func() {
		var err error
		db, err = openDB(":memory:")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		db.Close()
	})

	Describe("excludeRecentDays", func() {
		It("returns nil when summaries count is less than or equal to days", func() {
			summaries := []SummaryRecord{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
				{Time: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)},
			}
			Expect(excludeRecentDays(summaries, 2)).To(BeNil())
			Expect(excludeRecentDays(summaries, 3)).To(BeNil())
		})

		It("returns summaries excluding last N days", func() {
			summaries := []SummaryRecord{
				{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Data: Summary{NumInstances: 100}},
				{Time: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), Data: Summary{NumInstances: 200}},
				{Time: time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), Data: Summary{NumInstances: 300}},
				{Time: time.Date(2025, 1, 4, 0, 0, 0, 0, time.UTC), Data: Summary{NumInstances: 400}},
			}
			result := excludeRecentDays(summaries, 2)
			Expect(result).To(HaveLen(2))
			Expect(result[0].Data.NumInstances).To(Equal(int64(100)))
			Expect(result[1].Data.NumInstances).To(Equal(int64(200)))
		})
	})

	Describe("getSummaries", func() {
		It("returns empty slice when no summaries exist", func() {
			summaries, err := getSummaries(db)
			Expect(err).NotTo(HaveOccurred())
			Expect(summaries).To(BeEmpty())
		})

		It("returns summaries ordered by time ascending", func() {
			// Insert test summaries
			summary1 := Summary{NumInstances: 100, Versions: map[string]uint64{"0.54.0": 50, "0.54.1": 50}}
			summary2 := Summary{NumInstances: 150, Versions: map[string]uint64{"0.54.0": 60, "0.54.1": 90}}

			err := saveSummary(db, summary1, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = saveSummary(db, summary2, time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())

			summaries, err := getSummaries(db)
			Expect(err).NotTo(HaveOccurred())
			Expect(summaries).To(HaveLen(2))
			Expect(summaries[0].Time.Day()).To(Equal(1))
			Expect(summaries[1].Time.Day()).To(Equal(2))
			Expect(summaries[0].Data.NumInstances).To(Equal(int64(100)))
			Expect(summaries[1].Data.NumInstances).To(Equal(int64(150)))
		})
	})

	Describe("chartsHandler", func() {
		It("returns 404 when no data available", func() {
			handler := chartsHandler(db)
			req := httptest.NewRequest(http.MethodGet, "/charts", nil)
			w := httptest.NewRecorder()

			handler(w, req)

			Expect(w.Code).To(Equal(http.StatusNotFound))
			Expect(w.Body.String()).To(ContainSubstring("No data available"))
		})

		It("returns HTML with chart when data exists", func() {
			summary := Summary{
				NumInstances: 100,
				Versions:     map[string]uint64{"0.54.0": 50, "0.54.1": 50},
				Players:      map[string]uint64{"0": 10, "1": 50, "2": 30},
				Tracks:       map[string]uint64{"0": 5, "1000": 40, "10000": 30},
			}
			// Insert 3 days of data (last 2 are excluded)
			err := saveSummary(db, summary, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = saveSummary(db, summary, time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = saveSummary(db, summary, time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())

			handler := chartsHandler(db)
			req := httptest.NewRequest(http.MethodGet, "/charts", nil)
			w := httptest.NewRecorder()

			handler(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Header().Get("Content-Type")).To(Equal("text/html"))
			body := w.Body.String()
			Expect(body).To(ContainSubstring("Navidrome Insights"))
			Expect(body).To(ContainSubstring("Number of Navidrome Installations"))
			Expect(body).To(ContainSubstring("Operating systems and architectures"))
			Expect(body).To(ContainSubstring("Player types"))
			Expect(body).To(ContainSubstring("Number of Active Players"))
			Expect(body).To(ContainSubstring("Active Players per Installation"))
			Expect(body).To(ContainSubstring("Number of Tracks in Library"))
			Expect(body).To(ContainSubstring("echarts"))
		})
	})

	Describe("buildOSChart", func() {
		It("returns nil when no summaries exist", func() {
			chart := buildOSChart([]SummaryRecord{})
			Expect(chart).To(BeNil())
		})

		It("returns pie chart with data from latest summary", func() {
			summaries := []SummaryRecord{
				{
					Time: time.Now().Add(-24 * time.Hour),
					Data: Summary{OS: map[string]uint64{"Linux - amd64": 10}},
				},
				{
					Time: time.Now(),
					Data: Summary{OS: map[string]uint64{"Linux - amd64": 20, "macOS - arm64": 5}},
				},
			}

			chart := buildOSChart(summaries)
			Expect(chart).NotTo(BeNil())
		})
	})

	Describe("buildPlayerTypesChart", func() {
		It("returns nil when no summaries exist", func() {
			chart := buildPlayerTypesChart([]SummaryRecord{})
			Expect(chart).To(BeNil())
		})

		It("returns pie chart with data from latest summary", func() {
			summaries := []SummaryRecord{
				{
					Time: time.Now().Add(-24 * time.Hour),
					Data: Summary{PlayerTypes: map[string]uint64{"NavidromeUI": 10}},
				},
				{
					Time: time.Now(),
					Data: Summary{PlayerTypes: map[string]uint64{"NavidromeUI": 20, "Supersonic": 15, "Audioling": 5}},
				},
			}

			chart := buildPlayerTypesChart(summaries)
			Expect(chart).NotTo(BeNil())
		})

		It("groups players with less than 0.5% into Others", func() {
			// Total: 1000, threshold: 5 (0.5%)
			// PlayerA: 500 (50%) - kept
			// PlayerB: 300 (30%) - kept
			// PlayerC: 100 (10%) - kept
			// PlayerD: 50 (5%) - kept
			// PlayerE: 40 (4%) - kept
			// PlayerF: 4 (0.4%) - grouped into Others
			// PlayerG: 3 (0.3%) - grouped into Others
			// PlayerH: 3 (0.3%) - grouped into Others
			summaries := []SummaryRecord{
				{
					Time: time.Now(),
					Data: Summary{PlayerTypes: map[string]uint64{
						"PlayerA": 500,
						"PlayerB": 300,
						"PlayerC": 100,
						"PlayerD": 50,
						"PlayerE": 40,
						"PlayerF": 4,
						"PlayerG": 3,
						"PlayerH": 3,
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
			// Should have Others bucket
			Expect(jsonStr).To(ContainSubstring("Others"))
			// Should NOT include small players individually
			Expect(jsonStr).NotTo(ContainSubstring("PlayerF"))
			Expect(jsonStr).NotTo(ContainSubstring("PlayerG"))
			Expect(jsonStr).NotTo(ContainSubstring("PlayerH"))
		})
	})

	Describe("buildPlayersChart", func() {
		It("returns line chart with player totals over time", func() {
			summaries := []SummaryRecord{
				{
					Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
					Data: Summary{PlayerTypes: map[string]uint64{"NavidromeUI": 10, "Supersonic": 5}},
				},
				{
					Time: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC),
					Data: Summary{PlayerTypes: map[string]uint64{"NavidromeUI": 20, "Supersonic": 10, "Audioling": 5}},
				},
			}

			chart := buildPlayersChart(summaries)
			Expect(chart).NotTo(BeNil())
		})

		It("handles empty player types", func() {
			summaries := []SummaryRecord{
				{
					Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
					Data: Summary{PlayerTypes: map[string]uint64{}},
				},
			}

			chart := buildPlayersChart(summaries)
			Expect(chart).NotTo(BeNil())
		})
	})

	Describe("buildPlayersPerInstallationChart", func() {
		It("returns nil when no summaries exist", func() {
			chart := buildPlayersPerInstallationChart([]SummaryRecord{})
			Expect(chart).To(BeNil())
		})

		It("returns bar chart with player distribution from latest summary", func() {
			summaries := []SummaryRecord{
				{
					Time: time.Now(),
					Data: Summary{Players: map[string]uint64{"0": 100, "1": 500, "2": 200, "3": 50}},
				},
			}

			chart := buildPlayersPerInstallationChart(summaries)
			Expect(chart).NotTo(BeNil())
		})

		It("handles empty players data", func() {
			summaries := []SummaryRecord{
				{
					Time: time.Now(),
					Data: Summary{Players: map[string]uint64{}},
				},
			}

			chart := buildPlayersPerInstallationChart(summaries)
			Expect(chart).NotTo(BeNil())
		})
	})

	Describe("buildTracksChart", func() {
		It("returns nil when no summaries exist", func() {
			chart := buildTracksChart([]SummaryRecord{})
			Expect(chart).To(BeNil())
		})

		It("returns horizontal bar chart with track distribution from latest summary", func() {
			summaries := []SummaryRecord{
				{
					Time: time.Now(),
					Data: Summary{Tracks: map[string]uint64{"0": 50, "1000": 200, "10000": 150, "50000": 80}},
				},
			}

			chart := buildTracksChart(summaries)
			Expect(chart).NotTo(BeNil())
		})

		It("handles empty tracks data", func() {
			summaries := []SummaryRecord{
				{
					Time: time.Now(),
					Data: Summary{Tracks: map[string]uint64{}},
				},
			}

			chart := buildTracksChart(summaries)
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

	Describe("exportChartsJSON", func() {
		var tempDir string

		BeforeEach(func() {
			var err error
			tempDir, err = os.MkdirTemp("", "charts-test")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			os.RemoveAll(tempDir)
		})

		It("does nothing when no summaries exist", func() {
			err := exportChartsJSON(db, tempDir)
			Expect(err).NotTo(HaveOccurred())

			// File should not be created
			_, err = os.Stat(filepath.Join(tempDir, "charts.json"))
			Expect(os.IsNotExist(err)).To(BeTrue())
		})

		It("exports charts JSON when data exists", func() {
			summary := Summary{
				NumInstances: 100,
				Versions:     map[string]uint64{"0.54.0": 50, "0.54.1": 50},
				OS:           map[string]uint64{"Linux - amd64": 80, "macOS - arm64": 20},
				PlayerTypes:  map[string]uint64{"NavidromeUI": 50, "Supersonic": 30},
				Players:      map[string]uint64{"0": 10, "1": 50, "2": 30},
				Tracks:       map[string]uint64{"0": 5, "1000": 40, "10000": 30},
			}
			// Insert 3 days of data (last 2 are excluded)
			err := saveSummary(db, summary, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = saveSummary(db, summary, time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			err = saveSummary(db, summary, time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())

			err = exportChartsJSON(db, tempDir)
			Expect(err).NotTo(HaveOccurred())

			// Verify file exists
			jsonPath := filepath.Join(tempDir, "charts.json")
			data, err := os.ReadFile(jsonPath)
			Expect(err).NotTo(HaveOccurred())

			// Verify JSON structure (array of charts)
			var chartsData []map[string]interface{}
			err = json.Unmarshal(data, &chartsData)
			Expect(err).NotTo(HaveOccurred())
			Expect(chartsData).To(HaveLen(5))
			Expect(chartsData[0]["id"]).To(Equal("versions"))
			Expect(chartsData[1]["id"]).To(Equal("os"))
			Expect(chartsData[2]["id"]).To(Equal("players"))
			Expect(chartsData[3]["id"]).To(Equal("playerTypes"))
			// Expect(chartsData[4]["id"]).To(Equal("playersPerInstallation"))
			Expect(chartsData[4]["id"]).To(Equal("tracks"))
		})
	})
})
