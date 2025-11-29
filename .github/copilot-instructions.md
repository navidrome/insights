# Navidrome Insights Server

A lightweight Go service that collects anonymous usage metrics from Navidrome instances and generates aggregated summaries with visualizations.

## Architecture

```
main.go          → Entry point: HTTP server setup, cron tasks registration
handler.go       → POST /collect endpoint, receives insights.Data from Navidrome
db.go            → SQLite operations (openDB, saveReport, purgeOldEntries)
summary.go       → Data aggregation logic (summarizeData, mapping functions)
summary_store.go → File-based summary persistence (JSON files in summaries/)
charts.go        → Chart generation using go-echarts (line, pie, bar charts)
tasks.go         → Cron job wrappers (summarize, generateCharts, cleanup)
```

### Data Flow

1. Navidrome instances POST JSON to `/collect` (rate-limited: 1 req/30min per IP)
2. Raw data stored in SQLite `insights` table with instance ID and timestamp
3. Every 2 hours: `summarizeData()` aggregates last 10 days → JSON files in `summaries/YYYY/MM/`
4. Daily at 00:05 UTC: `exportChartsJSON()` generates `web/chartdata/charts.json` for frontend
5. Daily at 00:30 UTC: `purgeOldEntries()` removes entries older than 30 days

### External Data Model

The `insights.Data` struct is imported from `github.com/navidrome/navidrome/core/metrics/insights`. Key fields: `Version`, `OS`, `Library.ActivePlayers`, `Library.Tracks`. See the [Navidrome source](https://github.com/navidrome/navidrome/blob/main/core/metrics/insights/data.go).

## Development

```bash
make dev      # Docker Compose + hot reload (reflex), creates .env if missing
make lint     # golangci-lint in container
make linux    # Build production binary to binary/
go test ./... # Run Ginkgo tests locally
```

**Environment variables**:

- `PORT` - HTTP server port (default: `8080`)
- `DATA_FOLDER` - Where DB and summaries are stored (default: current dir)

## Key Patterns

### Regex-Based Mapping (`summary.go`)

Player names and versions are normalized using regex maps. Empty string values mean "discard":

```go
var playersTypes = map[*regexp.Regexp]string{
    regexp.MustCompile("NavidromeUI.*"): "NavidromeUI",  // Normalize variants
    regexp.MustCompile("feishin"):       "",             // Discard (old version bug)
    regexp.MustCompile("DSubCC"):        "",             // Discard (chromecast noise)
}
```

### Binning for Distributions (`mapToBins`)

Numeric values are grouped into predefined bins for histograms:

```go
var trackBins = []int64{0, 1, 100, 500, 1000, 5000, 10000, 20000, 50000, 100000, 500000, 1000000}
```

### Iterator Pattern

`selectData()` returns `iter.Seq[insights.Data]` for memory-efficient processing of large datasets.

### Incomplete Data Handling (`charts.go`)

`excludeIncompleteDays()` removes trailing days where instance count drops >20%, indicating incomplete data collection.

## Testing

Uses **Ginkgo/Gomega** BDD framework. Patterns to follow:

- Use `DescribeTable` for parameterized tests (see `summary_test.go`)
- Define local type aliases to construct `insights.Data` for tests:
  ```go
  type insightsOS struct { Type string; Arch string; Containerized bool }
  type insightsLibrary struct { ActivePlayers map[string]int64 }
  ```
- Test files use temp directories via `os.MkdirTemp` and set `DATA_FOLDER` env var

## Database

SQLite with WAL mode. Schema auto-created in `openDB()`:

```sql
insights(id VARCHAR, time DATETIME, data JSONB, PRIMARY KEY (id, time))
```

Summaries are stored as JSON files in `summaries/YYYY/MM/summary-YYYY-MM-DD.json`, not in SQLite.

## Charts

Built with go-echarts. Charts are exported to JSON and consumed by `web/index.html`:

- `buildVersionsChart()` - Line chart of version adoption over time (top 15 versions)
- `buildOSChart()` - Pie chart of OS/arch distribution
- `buildPlayerTypesChart()` - Pie chart, groups <0.2% into "Others"
- `buildTracksChart()` - Horizontal bar chart of library sizes
