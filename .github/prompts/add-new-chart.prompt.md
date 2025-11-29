---
name: add-new-chart
description: Guide for adding a new chart to a go-echarts multi-chart page
argument-hint: Type of chart and data source to visualize
---

# Adding a New Chart to the Charts Page

Follow this pattern to add a new chart to the existing charts page:

## Step 1: Create a Chart Builder Function

Create a new function that builds and returns a chart. Follow the naming convention `build{ChartName}Chart`:

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
            BackgroundColor: "#1a1a1a",
        }),
        charts.WithTitleOpts(opts.Title{
            Title:      "Your Chart Title",
            TitleStyle: &opts.TextStyle{Color: "#ffffff"},
        }),
        // Add other options as needed...
    )

    // 4. Add data series
    chart.SetXAxis(dates).AddSeries("Series Name", data)

    return chart
}
```

## Step 2: Register the Chart in the Handler

Add the new chart to the page in `chartsHandler`:

```go
page.AddCharts(
    buildVersionsChart(summaries),
    build{ChartName}Chart(summaries),  // Add your new chart here
)
```

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
- `Data.Players` - map of player count → instances
- `Data.Tracks` - map of track bin → count
- `Data.NumInstances` - total instances
- `Data.NumActiveUsers` - total active users
