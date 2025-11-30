# Navidrome Insights Server

Go service collecting anonymous usage metrics from Navidrome instances, generating aggregated summaries and visualizations.

## Architecture

```
cmd/server/       → HTTP server (main.go), /collect endpoint (handler.go), cron tasks (tasks.go)
db/               → SQLite operations (openDB, saveReport, selectData, purgeOldEntries)
summary/          → Aggregation logic (summary.go) and file storage (store.go)
charts/           → Chart generation using go-echarts, exports to JSON
cmd/consolidate/  → CLI tool to merge historical backup DBs into one
web/              → Static frontend (index.html consumes chartdata/charts.json)
```

### Data Flow

1. Navidrome POSTs to `/collect` (rate-limited: 1 req/30min per IP) → stored in SQLite
2. Cron every 2h: `summary.SummarizeData()` aggregates last 10 days → `summaries/YYYY/MM/summary-YYYY-MM-DD.json`
3. Cron daily 00:05 UTC: `charts.ExportChartsJSON()` → `web/chartdata/charts.json`
4. Cron daily 00:30 UTC: `db.PurgeOldEntries()` removes entries >30 days old

### External Dependency

`insights.Data` struct imported from `github.com/navidrome/navidrome/core/metrics/insights`. Key fields: `Version`, `OS`, `Library.ActivePlayers`, `Library.Tracks`.

## Development

```bash
make dev                    # Docker Compose + hot reload (reflex)
make lint                   # golangci-lint in container
go test ./...               # Run Ginkgo tests locally
DATA_FOLDER=tmp go run ./cmd/server/*.go  # Run server with custom data folder
```

**Environment**: `PORT` (default `8080`), `DATA_FOLDER` (default current dir)

## Key Patterns

### Regex-Based Normalization (`summary/summary.go`)

Player names normalized via regex map. Empty string = discard:

```go
var playersTypes = map[*regexp.Regexp]string{
    regexp.MustCompile("NavidromeUI.*"): "NavidromeUI",  // Normalize variants
    regexp.MustCompile("feishin"):       "",             // Discard (buggy old versions)
}
```

### Binning (`mapToBins`)

Numeric values grouped into predefined bins: `var TrackBins = []int64{0, 1, 100, 500, ...}`

### Iterator Pattern

`db.SelectData()` returns `iter.Seq[insights.Data]` for memory-efficient processing.

### Incomplete Data Detection (`charts.ExcludeIncompleteDays`)

Removes trailing days where instance count drops >20% (indicates incomplete collection).

## Testing

**Ginkgo/Gomega BDD framework**. Key patterns:

- Use `DescribeTable` for parameterized tests (see `summary/summary_test.go`)
- Define local type aliases to construct `insights.Data`:
  ```go
  type insightsOS struct { Type string; Arch string; Containerized bool }
  type insightsLibrary struct { ActivePlayers map[string]int64 }
  ```
- Use temp directories: `os.MkdirTemp()` + set `DATA_FOLDER` env var

## Database

SQLite with WAL mode. Schema auto-created in `db.OpenDB()`:

```sql
insights(id VARCHAR, time DATETIME, data JSONB, PRIMARY KEY (id, time))
```

Summaries stored as JSON files in `summaries/`, not in SQLite.

## Consolidation Tool

Merge historical backup zip files into a single DB:

```bash
make consolidate BACKUPS=/path/to/zips DEST=/path/to/output
```
