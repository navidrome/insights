package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
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
			}
			err := saveSummary(db, summary, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
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
})
