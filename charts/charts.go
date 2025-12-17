package charts

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/go-echarts/go-echarts/v2/charts"
	"github.com/go-echarts/go-echarts/v2/components"
	"github.com/go-echarts/go-echarts/v2/opts"
	"github.com/navidrome/insights/consts"
	"github.com/navidrome/insights/summary"
)

// ExcludeIncompleteDays removes any trailing days when the instance count drops significantly
// (more than 20% drop) compared to the previous day, as this indicates incomplete data.
func ExcludeIncompleteDays(summaries []summary.SummaryRecord) []summary.SummaryRecord {
	if len(summaries) == 0 {
		return nil
	}

	// Remove trailing incomplete data (significant drops from previous day)
	for len(summaries) > 1 {
		last := summaries[len(summaries)-1]
		prev := summaries[len(summaries)-2]
		if prev.Data.NumInstances > 0 {
			dropRatio := float64(last.Data.NumInstances) / float64(prev.Data.NumInstances)
			if dropRatio < consts.IncompleteThreshold { // Detect significant drop
				summaries = summaries[:len(summaries)-1]
				continue
			}
		}
		break
	}
	return summaries
}

// timeSeriesData holds a continuous date range with data for each date.
// Dates without data will have nil in the lookup map.
type timeSeriesData struct {
	Dates  []string                             // Continuous date range as formatted strings
	Lookup map[time.Time]*summary.SummaryRecord // Map from date to summary (nil if missing)
	Start  time.Time                            // First date in the range
}

// gapRange represents a range of missing data
type gapRange struct {
	StartDate string // Formatted start date of gap
	EndDate   string // Formatted end date of gap
}

// buildTimeSeriesData creates a continuous date range from the first to last summary,
// filling gaps with nil values to show breaks in time series charts.
func buildTimeSeriesData(summaries []summary.SummaryRecord) timeSeriesData {
	if len(summaries) == 0 {
		return timeSeriesData{}
	}

	// Build lookup map from date to summary
	lookup := make(map[time.Time]*summary.SummaryRecord, len(summaries))
	for i := range summaries {
		lookup[summaries[i].Time] = &summaries[i]
	}

	// Generate continuous date range
	start := summaries[0].Time
	end := summaries[len(summaries)-1].Time

	var dates []string
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d.Format(consts.ChartDateFormat))
	}

	return timeSeriesData{Dates: dates, Lookup: lookup, Start: start}
}

// findGaps returns the ranges of missing data in the time series
func (ts timeSeriesData) findGaps() []gapRange {
	if len(ts.Dates) == 0 {
		return nil
	}

	var gaps []gapRange
	var gapStart time.Time
	inGap := false

	for i := range ts.Dates {
		date := ts.Start.AddDate(0, 0, i)
		hasData := ts.Lookup[date] != nil

		if !hasData && !inGap {
			// Start of a new gap
			gapStart = date
			inGap = true
		} else if hasData && inGap {
			// End of gap (previous day was the last gap day)
			gapEnd := date.AddDate(0, 0, -1)
			gaps = append(gaps, gapRange{
				StartDate: gapStart.Format(consts.ChartDateFormat),
				EndDate:   gapEnd.Format(consts.ChartDateFormat),
			})
			inGap = false
		}
	}

	// Handle gap that extends to the end
	if inGap {
		lastDate := ts.Start.AddDate(0, 0, len(ts.Dates)-1)
		gaps = append(gaps, gapRange{
			StartDate: gapStart.Format(consts.ChartDateFormat),
			EndDate:   lastDate.Format(consts.ChartDateFormat),
		})
	}

	return gaps
}

// buildMarkAreaData creates MarkArea data pairs for highlighting gaps
func buildMarkAreaData(gaps []gapRange) [][]opts.MarkAreaData {
	if len(gaps) == 0 {
		return nil
	}

	var areas [][]opts.MarkAreaData
	for _, gap := range gaps {
		areas = append(areas, []opts.MarkAreaData{
			{
				Name:  "Missing Data",
				XAxis: gap.StartDate,
				MarkAreaStyle: opts.MarkAreaStyle{
					ItemStyle: &opts.ItemStyle{
						Color: consts.GapHighlightColor,
					},
					Label: &opts.Label{
						Show:     opts.Bool(true),
						Position: "inside",
						Color:    consts.GapLabelColor,
					},
				},
			},
			{
				XAxis: gap.EndDate,
			},
		})
	}
	return areas
}

func ChartsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		summaries, err := summary.GetSummaries()
		if err != nil {
			log.Printf("Error loading summaries: %v", err)
			http.Error(w, "Failed to load data", http.StatusInternalServerError)
			return
		}
		// Exclude incomplete days (significant drops indicate incomplete data)
		summaries = ExcludeIncompleteDays(summaries)
		if len(summaries) == 0 {
			http.Error(w, "No data available", http.StatusNotFound)
			return
		}

		page := components.NewPage()
		page.PageTitle = "Navidrome Insights"
		page.AddCharts(
			buildVersionsChart(summaries),
			buildOSChart(summaries),
			buildPlayerTypesChart(summaries),
			buildPlayersChart(summaries),
			buildPlayersPerInstallationChart(summaries),
			buildTracksChart(summaries),
			buildAlbumsArtistsChart(summaries),
		)

		w.Header().Set("Content-Type", "text/html")
		_ = page.Render(w)
	}
}

func buildVersionsChart(summaries []summary.SummaryRecord) *charts.Line {
	// Build continuous date range with gaps
	ts := buildTimeSeriesData(summaries)
	start := summaries[0].Time

	// Calculate the cutoff date for rolling window (last N calendar days)
	lastDate := summaries[len(summaries)-1].Time
	cutoffDate := lastDate.AddDate(0, 0, -consts.VersionSelectionDays)

	// Collect version totals only from the rolling window for top-N selection
	versionTotals := make(map[string]uint64)
	for _, s := range summaries {
		if !s.Time.Before(cutoffDate) {
			for version, count := range s.Data.Versions {
				versionTotals[version] += count
			}
		}
	}

	// Get top N versions by total count in the rolling window
	topVersionsList := getTopKeys(versionTotals, consts.TopVersionsCount)

	// Sort versions by last day's count (highest to lowest)
	lastSummary := summaries[len(summaries)-1]
	slices.SortFunc(topVersionsList, func(a, b string) int {
		countA := lastSummary.Data.Versions[a]
		countB := lastSummary.Data.Versions[b]
		return cmp.Compare(countB, countA)
	})

	// Create a set of top versions for quick lookup
	topVersionsSet := make(map[string]bool)
	for _, v := range topVersionsList {
		topVersionsSet[v] = true
	}

	// Create line chart
	line := charts.NewLine()
	line.SetGlobalOptions(
		charts.WithInitializationOpts(opts.Initialization{
			Width:           consts.ChartWidth,
			Height:          consts.ChartHeight,
			BackgroundColor: consts.ChartBackgroundColor,
		}),
		charts.WithTitleOpts(opts.Title{
			Title:      "Number of Navidrome Installations",
			TitleStyle: &opts.TextStyle{Color: consts.ChartTextColor},
		}),
		charts.WithTooltipOpts(opts.Tooltip{
			Show:    opts.Bool(true),
			Trigger: "axis",
		}),
		charts.WithLegendOpts(opts.Legend{
			Show:      opts.Bool(true),
			Right:     "10",
			Orient:    "vertical",
			TextStyle: &opts.TextStyle{Color: consts.ChartTextColor},
		}),
		charts.WithXAxisOpts(opts.XAxis{
			Name:         "Date",
			NameLocation: "center",
			NameGap:      30,
			AxisLabel: &opts.AxisLabel{
				Color: consts.ChartTextColor,
			},
		}),
		charts.WithYAxisOpts(opts.YAxis{
			Name:         "Installations",
			NameLocation: "center",
			NameGap:      50,
			AxisLabel: &opts.AxisLabel{
				Color: consts.ChartTextColor,
			},
		}),
		charts.WithGridOpts(opts.Grid{
			Left:   "80",
			Right:  "280",
			Bottom: "60",
		}),
	)

	line.SetXAxis(ts.Dates)

	// Build series data with nil for missing dates
	allData := make([]opts.LineData, len(ts.Dates))
	versionData := make(map[string][]opts.LineData)
	othersData := make([]opts.LineData, len(ts.Dates))

	for _, version := range topVersionsList {
		versionData[version] = make([]opts.LineData, len(ts.Dates))
	}

	for i := range ts.Dates {
		date := start.AddDate(0, 0, i)
		s := ts.Lookup[date]
		if s == nil {
			// No data for this date - use nil to create gap
			allData[i] = opts.LineData{Value: nil}
			for _, version := range topVersionsList {
				versionData[version][i] = opts.LineData{Value: nil}
			}
			othersData[i] = opts.LineData{Value: nil}
		} else {
			// Calculate totals for this day
			var allTotal uint64
			var othersCount uint64
			for version, count := range s.Data.Versions {
				allTotal += count
				if !topVersionsSet[version] {
					othersCount += count
				}
			}
			allData[i] = opts.LineData{Value: allTotal}
			for _, version := range topVersionsList {
				versionData[version][i] = opts.LineData{Value: s.Data.Versions[version]}
			}
			othersData[i] = opts.LineData{Value: othersCount}
		}
	}

	// Find gaps and create mark areas
	gaps := ts.findGaps()
	markAreas := buildMarkAreaData(gaps)

	// Add series - first series gets the mark areas
	line.AddSeries("All", allData, charts.WithMarkAreaData(markAreas...))
	for _, version := range topVersionsList {
		line.AddSeries(version, versionData[version])
	}
	line.AddSeries("Others", othersData)

	line.SetSeriesOptions(
		charts.WithLineChartOpts(opts.LineChart{Smooth: opts.Bool(true)}),
	)

	return line
}

func buildOSChart(summaries []summary.SummaryRecord) *charts.Pie {
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
			Width:           consts.ChartWidth,
			Height:          consts.ChartHeight,
			BackgroundColor: consts.ChartBackgroundColor,
		}),
		charts.WithTitleOpts(opts.Title{
			Title:      "Operating systems and architectures",
			TitleStyle: &opts.TextStyle{Color: consts.ChartTextColor},
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
			TextStyle: &opts.TextStyle{Color: consts.ChartTextColor},
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

func buildPlayerTypesChart(summaries []summary.SummaryRecord) *charts.Pie {
	if len(summaries) == 0 {
		return nil
	}
	latest := summaries[len(summaries)-1]

	// Calculate total count
	var total uint64
	for _, count := range latest.Data.PlayerTypes {
		total += count
	}

	// Group players with less than threshold into "Others"
	threshold := float64(total) * consts.PlayerGroupThreshold
	var data []opts.PieData
	var othersCount uint64
	for playerType, count := range latest.Data.PlayerTypes {
		if float64(count) < threshold {
			othersCount += count
		} else {
			data = append(data, opts.PieData{Name: playerType, Value: count})
		}
	}
	if othersCount > 0 {
		data = append(data, opts.PieData{Name: "Others (less than 0.2%)", Value: othersCount})
	}

	// Sort data by value descending
	sort.Slice(data, func(i, j int) bool {
		return data[i].Value.(uint64) > data[j].Value.(uint64)
	})

	pie := charts.NewPie()
	pie.SetGlobalOptions(
		charts.WithInitializationOpts(opts.Initialization{
			Width:           consts.ChartWidth,
			Height:          consts.ChartHeight,
			BackgroundColor: consts.ChartBackgroundColor,
		}),
		charts.WithTitleOpts(opts.Title{
			Title:      "Client types",
			TitleStyle: &opts.TextStyle{Color: consts.ChartTextColor},
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
			TextStyle: &opts.TextStyle{Color: consts.ChartTextColor},
			Type:      "scroll",
		}),
	)

	pie.AddSeries("Client type", data).
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

func buildPlayersChart(summaries []summary.SummaryRecord) *charts.Line {
	// Build continuous date range with gaps
	ts := buildTimeSeriesData(summaries)
	start := summaries[0].Time

	line := charts.NewLine()
	line.SetGlobalOptions(
		charts.WithInitializationOpts(opts.Initialization{
			Width:           consts.ChartWidth,
			Height:          consts.ChartHeight,
			BackgroundColor: consts.ChartBackgroundColor,
		}),
		charts.WithTitleOpts(opts.Title{
			Title:      "Number of Active Clients",
			TitleStyle: &opts.TextStyle{Color: consts.ChartTextColor},
		}),
		charts.WithTooltipOpts(opts.Tooltip{
			Show:    opts.Bool(true),
			Trigger: "axis",
		}),
		charts.WithLegendOpts(opts.Legend{
			Show: opts.Bool(false),
		}),
		charts.WithXAxisOpts(opts.XAxis{
			Name:         "Date",
			NameLocation: "center",
			NameGap:      30,
			AxisLabel: &opts.AxisLabel{
				Color: consts.ChartTextColor,
			},
		}),
		charts.WithYAxisOpts(opts.YAxis{
			Name:         "Clients",
			NameLocation: "center",
			NameGap:      50,
			AxisLabel: &opts.AxisLabel{
				Color: consts.ChartTextColor,
			},
		}),
		charts.WithGridOpts(opts.Grid{
			Left:   "80",
			Right:  "280",
			Bottom: "60",
		}),
	)

	line.SetXAxis(ts.Dates)

	// Calculate total players for each date, with nil for missing dates
	totalData := make([]opts.LineData, len(ts.Dates))
	for i := range ts.Dates {
		date := start.AddDate(0, 0, i)
		s := ts.Lookup[date]
		if s == nil {
			totalData[i] = opts.LineData{Value: nil}
		} else {
			var total uint64
			for _, count := range s.Data.PlayerTypes {
				total += count
			}
			totalData[i] = opts.LineData{Value: total}
		}
	}

	// Find gaps and create mark areas
	gaps := ts.findGaps()
	markAreas := buildMarkAreaData(gaps)

	line.AddSeries("Total Clients", totalData, charts.WithMarkAreaData(markAreas...))

	line.SetSeriesOptions(
		charts.WithLineChartOpts(opts.LineChart{Smooth: opts.Bool(true)}),
	)

	return line
}

func buildPlayersPerInstallationChart(summaries []summary.SummaryRecord) *charts.Bar {
	if len(summaries) == 0 {
		return nil
	}
	latest := summaries[len(summaries)-1]

	// Define bins for grouping player counts to handle the long tail
	bins := []struct {
		label string
		min   int
		max   int // inclusive, -1 means infinity
	}{
		{"0", 0, 0},
		{"1", 1, 1},
		{"2", 2, 2},
		{"3", 3, 3},
		{"4", 4, 4},
		{"5", 5, 5},
		{"6-10", 6, 10},
		{"11-20", 11, 20},
		{"21-50", 21, 50},
		{"50+", 51, -1},
	}

	// Aggregate data into bins
	binValues := make([]uint64, len(bins))
	for countStr, value := range latest.Data.Players {
		var count int
		_, _ = fmt.Sscanf(countStr, "%d", &count)

		for i, bin := range bins {
			if count >= bin.min && (bin.max == -1 || count <= bin.max) {
				binValues[i] += value
				break
			}
		}
	}

	// Build chart data
	xLabels := make([]string, len(bins))
	data := make([]opts.BarData, len(bins))
	for i, bin := range bins {
		xLabels[i] = bin.label
		data[i] = opts.BarData{Value: binValues[i]}
	}

	bar := charts.NewBar()
	bar.SetGlobalOptions(
		charts.WithInitializationOpts(opts.Initialization{
			Width:           consts.ChartWidth,
			Height:          consts.ChartHeight,
			BackgroundColor: consts.ChartBackgroundColor,
		}),
		charts.WithTitleOpts(opts.Title{
			Title:      "Active Clients per Installation",
			TitleStyle: &opts.TextStyle{Color: consts.ChartTextColor},
		}),
		charts.WithTooltipOpts(opts.Tooltip{
			Show:    opts.Bool(true),
			Trigger: "axis",
		}),
		charts.WithLegendOpts(opts.Legend{
			Show: opts.Bool(false),
		}),
		charts.WithXAxisOpts(opts.XAxis{
			Name:         "Active Clients per Installation",
			NameLocation: "center",
			NameGap:      30,
			AxisLabel: &opts.AxisLabel{
				Color: consts.ChartTextColor,
			},
		}),
		charts.WithYAxisOpts(opts.YAxis{
			Name:         "Count of Installations",
			NameLocation: "center",
			NameGap:      50,
			AxisLabel: &opts.AxisLabel{
				Color: consts.ChartTextColor,
			},
		}),
		charts.WithGridOpts(opts.Grid{
			Left:   "80",
			Bottom: "60",
		}),
	)

	bar.SetXAxis(xLabels).AddSeries("Installations", data)

	return bar
}

var trackBinLabels = []string{
	"0", "1-500", "501-1,000", "1,001-5,000", "5,001-10,000",
	"10,001-20,000", "20,001-50,000", "50,001-100,000",
	"100,001-500,000", "500,001-1,000,000", ">1,000,001",
}

var albumArtistBinLabels = []string{
	"0", "1-10", "11-50", "51-100", "101-500",
	"501-1,000", "1,001-2,000", "2,001-5,000",
	"5,001-10,000", "10,001-50,000", "50,001-100,000", ">100,000",
}

func buildTracksChart(summaries []summary.SummaryRecord) *charts.Bar {
	if len(summaries) == 0 {
		return nil
	}
	latest := summaries[len(summaries)-1]

	// Map bin values to labels, maintaining order from trackBins in summary.go
	binToLabel := map[string]string{
		"0":       "0",
		"1":       "1-500",
		"500":     "501-1,000",
		"1000":    "1,001-5,000",
		"5000":    "5,001-10,000",
		"10000":   "10,001-20,000",
		"20000":   "20,001-50,000",
		"50000":   "50,001-100,000",
		"100000":  "100,001-500,000",
		"500000":  "500,001-1,000,000",
		"1000000": ">1,000,001",
	}

	// Build data in the order of trackBinLabels
	data := make([]opts.BarData, len(trackBinLabels))
	for i, label := range trackBinLabels {
		var value uint64
		for binKey, binLabel := range binToLabel {
			if binLabel == label {
				value = latest.Data.Tracks[binKey]
				break
			}
		}
		data[i] = opts.BarData{Value: value}
	}

	bar := charts.NewBar()
	bar.SetGlobalOptions(
		charts.WithInitializationOpts(opts.Initialization{
			Width:           consts.ChartWidth,
			Height:          consts.ChartHeight,
			BackgroundColor: consts.ChartBackgroundColor,
		}),
		charts.WithTitleOpts(opts.Title{
			Title:      "Number of Tracks in Library",
			TitleStyle: &opts.TextStyle{Color: consts.ChartTextColor},
		}),
		charts.WithTooltipOpts(opts.Tooltip{
			Show:    opts.Bool(true),
			Trigger: "axis",
		}),
		charts.WithLegendOpts(opts.Legend{
			Show: opts.Bool(false),
		}),
		charts.WithXAxisOpts(opts.XAxis{
			Name:         "Count of Installations",
			NameLocation: "center",
			NameGap:      30,
			AxisLabel: &opts.AxisLabel{
				Color: consts.ChartTextColor,
			},
		}),
		charts.WithYAxisOpts(opts.YAxis{
			Name:         "Tracks in Library",
			NameLocation: "center",
			NameGap:      130,
			AxisLabel: &opts.AxisLabel{
				Color: consts.ChartTextColor,
			},
		}),
		charts.WithGridOpts(opts.Grid{
			Left:   "180",
			Bottom: "60",
		}),
	)

	bar.SetXAxis(trackBinLabels).
		AddSeries("Installations", data).
		XYReversal()

	return bar
}

func buildAlbumsArtistsChart(summaries []summary.SummaryRecord) *charts.Bar {
	if len(summaries) == 0 {
		return nil
	}
	latest := summaries[len(summaries)-1]

	// Map bin values to labels, maintaining order from AlbumBins/ArtistBins in summary.go
	binToLabel := map[string]string{
		"0":      "0",
		"1":      "1-10",
		"10":     "11-50",
		"50":     "51-100",
		"100":    "101-500",
		"500":    "501-1,000",
		"1000":   "1,001-2,000",
		"2000":   "2,001-5,000",
		"5000":   "5,001-10,000",
		"10000":  "10,001-50,000",
		"50000":  "50,001-100,000",
		"100000": ">100,000",
	}

	// Build albums data
	albumsData := make([]opts.BarData, len(albumArtistBinLabels))
	for i, label := range albumArtistBinLabels {
		var value uint64
		for binKey, binLabel := range binToLabel {
			if binLabel == label {
				value += latest.Data.Albums[binKey]
			}
		}
		albumsData[i] = opts.BarData{Value: value}
	}

	// Build artists data
	artistsData := make([]opts.BarData, len(albumArtistBinLabels))
	for i, label := range albumArtistBinLabels {
		var value uint64
		for binKey, binLabel := range binToLabel {
			if binLabel == label {
				value += latest.Data.Artists[binKey]
			}
		}
		artistsData[i] = opts.BarData{Value: value}
	}

	bar := charts.NewBar()
	bar.SetGlobalOptions(
		charts.WithInitializationOpts(opts.Initialization{
			Width:           consts.ChartWidth,
			Height:          consts.ChartHeight,
			BackgroundColor: consts.ChartBackgroundColor,
		}),
		charts.WithTitleOpts(opts.Title{
			Title:      "Albums and Artists in Library",
			TitleStyle: &opts.TextStyle{Color: consts.ChartTextColor},
		}),
		charts.WithTooltipOpts(opts.Tooltip{
			Show:    opts.Bool(true),
			Trigger: "axis",
		}),
		charts.WithLegendOpts(opts.Legend{
			Show:   opts.Bool(true),
			Top:    "30",
			Orient: "horizontal",
			TextStyle: &opts.TextStyle{
				Color: consts.ChartTextColor,
			},
		}),
		charts.WithXAxisOpts(opts.XAxis{
			Name:         "Count of Installations",
			NameLocation: "center",
			NameGap:      30,
			AxisLabel: &opts.AxisLabel{
				Color: consts.ChartTextColor,
			},
		}),
		charts.WithYAxisOpts(opts.YAxis{
			Name:         "Items in Library",
			NameLocation: "center",
			NameGap:      100,
			AxisLabel: &opts.AxisLabel{
				Color: consts.ChartTextColor,
			},
		}),
		charts.WithGridOpts(opts.Grid{
			Left:   "140",
			Top:    "80",
			Bottom: "60",
		}),
	)

	bar.SetXAxis(albumArtistBinLabels).
		AddSeries("Albums", albumsData).
		AddSeries("Artists", artistsData).
		XYReversal()

	return bar
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
		return cmp.Compare(b.Value, a.Value)
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

// ExportChartsJSON generates a JSON file with all chart configurations
func ExportChartsJSON(outputDir string) error {
	summaries, err := summary.GetSummaries()
	if err != nil {
		return err
	}
	// Exclude incomplete days (significant drops indicate incomplete data)
	summaries = ExcludeIncompleteDays(summaries)
	if len(summaries) == 0 {
		log.Print("No data to export")
		return nil
	}

	// Build all charts
	versionsChart := buildVersionsChart(summaries)
	versionsChart.Validate()

	osChart := buildOSChart(summaries)
	osChart.Validate()

	playerTypesChart := buildPlayerTypesChart(summaries)
	playerTypesChart.Validate()

	playersChart := buildPlayersChart(summaries)
	playersChart.Validate()

	playersPerInstallationChart := buildPlayersPerInstallationChart(summaries)
	playersPerInstallationChart.Validate()

	tracksChart := buildTracksChart(summaries)
	tracksChart.Validate()

	albumsArtistsChart := buildAlbumsArtistsChart(summaries)
	albumsArtistsChart.Validate()

	// Combine all charts into a single JSON array to preserve order
	chartsData := []map[string]interface{}{
		{"id": "versions", "options": versionsChart.JSON()},
		{"id": "os", "options": osChart.JSON()},
		{"id": "players", "options": playersChart.JSON()},
		{"id": "playerTypes", "options": playerTypesChart.JSON()},
		// {"id": "playersPerInstallation", "options": playersPerInstallationChart.JSON()},
		{"id": "tracks", "options": tracksChart.JSON()},
		{"id": "albumsArtists", "options": albumsArtistsChart.JSON()},
	}

	// Get the most recent total instances count
	totalInstances := int64(0)
	if len(summaries) > 0 {
		totalInstances = summaries[len(summaries)-1].Data.NumInstances
	}

	// Wrap charts in an object with metadata
	output := map[string]interface{}{
		"totalInstances": totalInstances,
		"lastUpdated":    time.Now().UTC().Format(time.RFC3339),
		"charts":         chartsData,
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}

	// Ensure output directory exists
	if err := os.MkdirAll(outputDir, consts.DirPermissions); err != nil {
		return err
	}

	// Write to file
	outputPath := filepath.Join(outputDir, consts.ChartsJSONFile)
	if err := os.WriteFile(outputPath, jsonData, consts.FilePermissions); err != nil {
		return err
	}

	log.Printf("Exported charts to %s", outputPath)
	return nil
}
