package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/go-echarts/go-echarts/v2/charts"
	"github.com/go-echarts/go-echarts/v2/components"
	"github.com/go-echarts/go-echarts/v2/opts"
)

const (
	chartWidth  = "1400px"
	chartHeight = "500px"
	topVersions = 15
)

func chartsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		summaries, err := getSummaries(db)
		if err != nil {
			log.Printf("Error loading summaries: %v", err)
			http.Error(w, "Failed to load data", http.StatusInternalServerError)
			return
		}
		if len(summaries) == 0 {
			http.Error(w, "No data available", http.StatusNotFound)
			return
		}

		page := components.NewPage()
		page.PageTitle = "Navidrome Insights"
		page.AddCharts(
			buildVersionsChart(summaries),
			buildOSChart(summaries),
			buildPlayersChart(summaries),
		)

		w.Header().Set("Content-Type", "text/html")
		_ = page.Render(w)
	}
}

func buildVersionsChart(summaries []SummaryRecord) *charts.Line {
	// Build X-axis dates
	dates := make([]string, len(summaries))
	for i, s := range summaries {
		dates[i] = s.Time.Format("Jan 02, 2006")
	}

	// Collect all versions and their total counts, plus "All" totals
	versionTotals := make(map[string]uint64)
	allTotals := make([]uint64, len(summaries))
	for i, s := range summaries {
		for version, count := range s.Data.Versions {
			versionTotals[version] += count
			allTotals[i] += count
		}
	}

	// Get top N versions by total count
	topVersionsList := getTopKeys(versionTotals, topVersions)

	// Sort versions for consistent ordering
	sort.Strings(topVersionsList)

	// Create line chart
	line := charts.NewLine()
	line.SetGlobalOptions(
		charts.WithInitializationOpts(opts.Initialization{
			Width:           chartWidth,
			Height:          chartHeight,
			BackgroundColor: "#ffffff",
		}),
		charts.WithTitleOpts(opts.Title{
			Title:      "Number of Navidrome Installations",
			TitleStyle: &opts.TextStyle{Color: "#000000"},
		}),
		charts.WithTooltipOpts(opts.Tooltip{
			Show:    opts.Bool(true),
			Trigger: "axis",
		}),
		charts.WithLegendOpts(opts.Legend{
			Show:      opts.Bool(true),
			Right:     "10",
			Orient:    "vertical",
			TextStyle: &opts.TextStyle{Color: "#000000"},
		}),
		charts.WithXAxisOpts(opts.XAxis{
			Name: "Date",
			AxisLabel: &opts.AxisLabel{
				Color: "#000000",
			},
		}),
		charts.WithYAxisOpts(opts.YAxis{
			Name: "Installations",
			AxisLabel: &opts.AxisLabel{
				Color: "#000000",
			},
		}),
		charts.WithGridOpts(opts.Grid{
			Right: "280",
		}),
	)

	line.SetXAxis(dates)

	// Add "All" series first (total installations)
	allData := make([]opts.LineData, len(summaries))
	for i, total := range allTotals {
		allData[i] = opts.LineData{Value: total}
	}
	line.AddSeries("All", allData)

	// Add series for each version
	for _, version := range topVersionsList {
		data := make([]opts.LineData, len(summaries))
		for i, s := range summaries {
			count := s.Data.Versions[version]
			data[i] = opts.LineData{Value: count}
		}
		line.AddSeries(version, data)
	}

	line.SetSeriesOptions(
		charts.WithLineChartOpts(opts.LineChart{Smooth: opts.Bool(true)}),
	)

	return line
}

func buildOSChart(summaries []SummaryRecord) *charts.Pie {
	if len(summaries) == 0 {
		return nil
	}
	latest := summaries[len(summaries)-1]

	// Prepare data
	var data []opts.PieData
	for os, count := range latest.Data.OS {
		data = append(data, opts.PieData{Name: os, Value: count})
	}

	// Sort data by value descending
	sort.Slice(data, func(i, j int) bool {
		return data[i].Value.(uint64) > data[j].Value.(uint64)
	})

	pie := charts.NewPie()
	pie.SetGlobalOptions(
		charts.WithInitializationOpts(opts.Initialization{
			Width:           chartWidth,
			Height:          chartHeight,
			BackgroundColor: "#ffffff",
		}),
		charts.WithTitleOpts(opts.Title{
			Title:      "Operating systems and architectures",
			TitleStyle: &opts.TextStyle{Color: "#000000"},
		}),
		charts.WithTooltipOpts(opts.Tooltip{
			Show:      opts.Bool(true),
			Trigger:   "item",
			Formatter: "{b}: {c} ({d}%)",
		}),
		charts.WithLegendOpts(opts.Legend{
			Show:      opts.Bool(true),
			Right:     "10",
			Orient:    "vertical",
			TextStyle: &opts.TextStyle{Color: "#000000"},
			Type:      "scroll",
		}),
	)

	pie.AddSeries("OS", data).
		SetSeriesOptions(
			charts.WithLabelOpts(opts.Label{
				Show: opts.Bool(false),
			}),
			charts.WithPieChartOpts(opts.PieChart{
				Radius: []string{"0%", "75%"},
				Center: []string{"40%", "50%"},
			}),
		)

	return pie
}

func buildPlayersChart(summaries []SummaryRecord) *charts.Line {
	dates := make([]string, len(summaries))
	for i, s := range summaries {
		dates[i] = s.Time.Format("Jan 02, 2006")
	}

	line := charts.NewLine()
	line.SetGlobalOptions(
		charts.WithInitializationOpts(opts.Initialization{
			Width:           chartWidth,
			Height:          chartHeight,
			BackgroundColor: "#ffffff",
		}),
		charts.WithTitleOpts(opts.Title{
			Title:      "Number of Connected Players",
			TitleStyle: &opts.TextStyle{Color: "#000000"},
		}),
		charts.WithTooltipOpts(opts.Tooltip{
			Show:    opts.Bool(true),
			Trigger: "axis",
		}),
		charts.WithLegendOpts(opts.Legend{
			Show:      opts.Bool(true),
			Right:     "10",
			Orient:    "vertical",
			TextStyle: &opts.TextStyle{Color: "#000000"},
		}),
		charts.WithXAxisOpts(opts.XAxis{
			Name: "Date",
			AxisLabel: &opts.AxisLabel{
				Color: "#000000",
			},
		}),
		charts.WithYAxisOpts(opts.YAxis{
			Name: "Players",
			AxisLabel: &opts.AxisLabel{
				Color: "#000000",
			},
		}),
		charts.WithGridOpts(opts.Grid{
			Right: "280",
		}),
	)

	line.SetXAxis(dates)

	// Calculate total players for each summary
	totalData := make([]opts.LineData, len(summaries))
	for i, s := range summaries {
		var total uint64
		for _, count := range s.Data.PlayerTypes {
			total += count
		}
		totalData[i] = opts.LineData{Value: total}
	}
	line.AddSeries("Total Players", totalData)

	line.SetSeriesOptions(
		charts.WithLineChartOpts(opts.LineChart{Smooth: opts.Bool(true)}),
	)

	return line
}

// getTopKeys returns the top N keys from a map sorted by value descending
func getTopKeys(m map[string]uint64, n int) []string {
	type kv struct {
		Key   string
		Value uint64
	}
	var pairs []kv
	for k, v := range m {
		pairs = append(pairs, kv{k, v})
	}
	slices.SortFunc(pairs, func(a, b kv) int {
		if a.Value > b.Value {
			return -1
		}
		if a.Value < b.Value {
			return 1
		}
		return 0
	})

	if n > len(pairs) {
		n = len(pairs)
	}
	result := make([]string, n)
	for i := 0; i < n; i++ {
		result[i] = pairs[i].Key
	}
	return result
}

// exportChartsJSON generates a JSON file with all chart configurations
func exportChartsJSON(db *sql.DB, outputDir string) error {
	summaries, err := getSummaries(db)
	if err != nil {
		return err
	}
	if len(summaries) == 0 {
		log.Print("No data to export")
		return nil
	}

	// Build all charts
	versionsChart := buildVersionsChart(summaries)
	versionsChart.Validate()

	osChart := buildOSChart(summaries)
	osChart.Validate()

	playersChart := buildPlayersChart(summaries)
	playersChart.Validate()

	// Combine all charts into a single JSON array to preserve order
	chartsData := []map[string]interface{}{
		{"id": "versions", "options": versionsChart.JSON()},
		{"id": "os", "options": osChart.JSON()},
		{"id": "players", "options": playersChart.JSON()},
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(chartsData, "", "  ")
	if err != nil {
		return err
	}

	// Ensure output directory exists
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	// Write to file
	outputPath := filepath.Join(outputDir, "charts.json")
	if err := os.WriteFile(outputPath, jsonData, 0644); err != nil {
		return err
	}

	log.Printf("Exported charts to %s", outputPath)
	return nil
}
