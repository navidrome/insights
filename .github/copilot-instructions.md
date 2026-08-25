# Navidrome Insights Server

Go service collecting anonymous usage metrics from Navidrome instances, generating aggregated summaries and visualizations.

## Architecture

```
cmd/ingest/           → HTTP server accepting reports: /collect (handler.go), /healthz
cmd/process/          → Cron worker (tasks.go) + /api/charts, /healthz (handler.go)
cmd/monitor/          → CLI reporting on one UTC day of collected reports
cmd/regenerate-charts/→ CLI to rebuild charts.json from existing summaries
internal/store/       → Raw report storage: gzipped NDJSON segments (writer.go, reader.go,
                        lastperid.go, purge.go)
internal/summary/     → Aggregation logic (summary.go) and file storage (store.go)
internal/charts/      → Chart generation using go-echarts, exports to JSON
web/                  → Static frontend (index.html consumes chartdata/charts.json)
```

The two binaries are deliberately separate: `ingest` only appends, so restarting the cron
worker never interrupts collection. Both run from the same `DATA_FOLDER`.

### Data Flow

1. Navidrome POSTs to `/collect` (rate-limited: 10 req/30min per IP) → `ingest` appends a JSON
   line to `reports/YYYY/MM/reports-YYYY-MM-DD.NNN.ndjson.gz`
2. Cron every 2h: `summary.SummarizeData()` aggregates the last 5 days →
   `summaries/YYYY/MM/summary-YYYY-MM-DD.json`
3. Cron daily 00:05 UTC: `charts.ExportChartsJSON()` → `web/chartdata/charts.json`
4. Cron hourly at :30: `store.PurgeToFreeSpace()` deletes whole report days, oldest first,
   until the data volume has `consts.MinFreeBytes` free. Retention is driven by free space, not
   by age: with room to spare every day is kept, and a day younger than
   `consts.MinRetentionDays` is never deleted (that floor must stay above
   `consts.SummarizeLookbackDays`, which a compile-time assertion in `consts` enforces)
5. `/api/charts` (served by `process`) returns `charts.json` (protected by `API_KEY` if set,
   public otherwise)

### External Dependency

`insights.Data` struct imported from `github.com/navidrome/navidrome/core/metrics/insights`. Key fields: `Version`, `OS`, `Library.ActivePlayers`, `Library.Tracks`.

## Development

```bash
make dev                    # Docker Compose + hot reload (reflex), both binaries
make lint                   # golangci-lint in container
make test                   # go test ./...
make summarize DATA=tmp [DAYS=5]        # Run summarize + chart export once, then exit
make monitor DATA=tmp [DATE=YYYY-MM-DD] # Report on one UTC day of raw reports
DATA_FOLDER=tmp go run ./cmd/ingest     # Run the collector with a custom data folder
```

`make dev` runs `ingest` on `$PORT` (8080) and `process` on 8081.

**Environment**: `PORT` (default `8080`), `DATA_FOLDER` (default current dir), `API_KEY` (optional, protects `/api/charts`)

### Build Tags

- **Production** (`go build`): `ingest` serves `/collect`; `process` serves `/api/charts`
- **Development** (`go build -tags dev`): `process` also serves `/`, `/chartdata/*`, `/charts` for the static frontend and legacy server-rendered charts

The `make dev` command automatically uses `-tags dev` via reflex.

## Key Patterns

### Regex-Based Normalization (`internal/summary/summary.go`)

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

`store.ReadDay()` returns `iter.Seq[store.Record]` and `store.LastPerID()` returns
`iter.Seq[insights.Data]` for memory-efficient processing — nothing loads a day into memory.

### Incomplete Data Detection (`charts.ExcludeIncompleteDays`)

Removes trailing days where instance count drops >20% (indicates incomplete collection).

## Testing

**Ginkgo/Gomega BDD framework**. Key patterns:

- Use `DescribeTable` for parameterized tests (see `internal/summary/summary_test.go`)
- Define local type aliases to construct `insights.Data`:
  ```go
  type insightsOS struct { Type string; Arch string; Containerized bool }
  type insightsLibrary struct { ActivePlayers map[string]int64 }
  ```
- Use temp directories: `os.MkdirTemp()` + set `DATA_FOLDER` env var

## Storage

No database — there is no SQLite and no cgo (`CGO_ENABLED=0 go build ./...` must stay clean,
it is what lets the production image be `FROM scratch`).

Raw reports live under `$DATA_FOLDER/reports/YYYY/MM/` as gzipped NDJSON, one line per report
(`{"time":...,"data":{...}}`). Each writer session — process start, or a UTC day rollover
within a session — opens a **new** segment `reports-YYYY-MM-DD.NNN.ndjson.gz`; a session never
appends to a segment written by an earlier one, so an unclean shutdown only truncates the tail
of the segment that was open. Readers tolerate that truncation and skip to the next segment.

All day boundaries are UTC. Summaries stay as JSON files in `summaries/`.
