---
name: add-new-chart
description: Guide for adding a new chart to a go-echarts multi-chart page
argument-hint: Type of chart and data source to visualize
---

# Adding a New Chart to the Charts Page

Follow this pattern to add a new chart to the existing charts page:

## Step 1: Create a Chart Builder Function

Create a new function in `charts.go` that builds and returns a chart. Follow the naming convention `build{ChartName}Chart`:

```go
func build{ChartName}Chart(summaries []SummaryRecord) *charts.{ChartType} {
    // 1. Extract X-axis data (usually dates)
    dates := make([]string, len(summaries))
    for i, s := range summaries {
        dates[i] = s.Time.Format("Jan 02, 2006")
    }

    // 2. Create the chart with the appropriate type (Line, Bar, Pie, etc.)
    chart := charts.New{ChartType}()

    // 3. Set global options (title, colors, legend, axes)
    chart.SetGlobalOptions(
        charts.WithInitializationOpts(opts.Initialization{
            Width:           chartWidth,
            Height:          chartHeight,
            BackgroundColor: "#ffffff",
        }),
        charts.WithTitleOpts(opts.Title{
            Title:      "Your Chart Title",
            TitleStyle: &opts.TextStyle{Color: "#000000"},
        }),
        // Add other options as needed...
    )

    // 4. Add data series
    chart.SetXAxis(dates).AddSeries("Series Name", data)

    return chart
}
```

## Step 2: Register the Chart in Two Places

### 2a. Add to `chartsHandler` (legacy server-rendered endpoint)

```go
page.AddCharts(
    buildVersionsChart(summaries),
    buildOSChart(summaries),
    build{ChartName}Chart(summaries),  // Add your new chart here
)
```

### 2b. Add to `exportChartsJSON` (static JSON export)

```go
// Build all charts
versionsChart := buildVersionsChart(summaries)
versionsChart.Validate()

osChart := buildOSChart(summaries)
osChart.Validate()

newChart := build{ChartName}Chart(summaries)
newChart.Validate()

// Combine all charts into a single JSON array to preserve order
chartsData := []map[string]interface{}{
    {"id": "versions", "options": versionsChart.JSON()},
    {"id": "os", "options": osChart.JSON()},
    {"id": "{chartName}", "options": newChart.JSON()},  // Add here
}
```

## Step 3: Validate and adjust your changes

Run the server in the background with `make dev` and verify that your new chart
appears on the main `/` page. Use Chrome DevTools to inspect and debug any rendering issues.

## Step 3: Add Tests

Add test cases for your new chart builder in `charts_test.go`.

## Available Chart Types

- `charts.NewLine()` - Line charts for trends over time
- `charts.NewBar()` - Bar charts for comparisons
- `charts.NewPie()` - Pie charts for distributions
- `charts.NewScatter()` - Scatter plots for correlations

## Data Sources

Chart data comes from `[]SummaryRecord` which contains:

- `Time` - timestamp for X-axis
- `Data.Versions` - map of version → count
- `Data.OS` - map of OS → count
- `Data.Distros` - map of distro → count
- `Data.PlayerTypes` - map of player type → count
- `Data.Players` - map of player count → instances
- `Data.Users` - map of user count → instances
- `Data.Tracks` - map of track bin → count
- `Data.MusicFS` - map of filesystem type → count
- `Data.DataFS` - map of filesystem type → count
- `Data.NumInstances` - total instances
- `Data.NumActiveUsers` - total active users
- `Data.LibSizeAverage` - average library size
- `Data.LibSizeStdDev` - library size standard deviation

## Architecture

The charts system uses two rendering approaches:

1. **Server-rendered** (`/charts`): Go renders complete HTML with embedded ECharts via `chartsHandler`
2. **Static JSON** (`/` + `/chartdata/charts.json`): Static HTML (`web/index.html`) loads JSON and renders client-side via `exportChartsJSON`

The static approach allows the JSON to be regenerated periodically (daily) while serving a lightweight HTML page. The `web/index.html` file dynamically renders all charts from the JSON array, so no HTML changes are needed when adding new charts—just update `exportChartsJSON` to include the new chart in the array.
