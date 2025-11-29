# Navidrome Insights Server

A lightweight Go service that collects anonymous usage metrics from Navidrome instances and generates aggregated summaries.

## Architecture

- **Single-binary HTTP server** using Chi router (`main.go`, `handler.go`)
- **SQLite database** with WAL mode for storage (`db.go`)
- **Cron-based background tasks** for summarization and cleanup (`tasks.go`)
- **Data model** imported from `github.com/navidrome/navidrome/core/metrics/insights`

### Data Flow
1. Navidrome instances POST JSON to `/collect` (rate-limited: 1 req/30min per IP)
2. Raw data stored in `insights` table with instance ID and timestamp
3. Every 2 hours, `summarizeData()` aggregates data into `summary` table
4. Daily cleanup purges entries older than 90 days

### insights.Data Structure (from Navidrome)
The `insights.Data` struct is defined in [`github.com/navidrome/navidrome/core/metrics/insights`](https://github.com/navidrome/navidrome/blob/main/core/metrics/insights/data.go) and includes:
- `id`: Unique instance identifier (UUID stored in Navidrome's DB)
- `version`: Navidrome version string (e.g., `0.54.2 (0b184893)`)
- `uptime`: Server uptime in seconds
- `build`: Go build settings and version
- `os`: Type, distro, version, arch, numCPU, containerized flag
- `mem`: Memory stats (alloc, totalAlloc, sys, numGC)
- `fs`: Filesystem info for music/data/cache/backup folders
- `library`: Tracks, albums, artists, playlists, shares, radios, activeUsers, activePlayers
- `config`: Feature flags and settings (40+ boolean/string fields)
- `plugins`: Map of installed plugin info

## Development

```bash
make dev      # Builds and runs with Docker Compose + hot reload (reflex)
make lint     # Run golangci-lint in container
make linux    # Build production Linux binary
```

**Environment**: Create `.env` with `PORT=8080` (auto-created by `make dev`)

## Testing

Uses **Ginkgo/Gomega** BDD framework:
```bash
go test ./...           # Run all tests
ginkgo -v               # Verbose Ginkgo output
```

Test patterns in `summary_test.go`:
- Use `DescribeTable` for parameterized tests
- Define local type aliases (e.g., `insightsOS`, `insightsLibrary`) to construct test `insights.Data`

## Key Patterns

### Mapping Functions (`summary.go`)
Data normalization uses regex-based mapping for versions, players, and OS types:
```go
var playersTypes = map[*regexp.Regexp]string{
    regexp.MustCompile("NavidromeUI.*"): "NavidromeUI",
    // Empty string = discard this entry
    regexp.MustCompile("feishin"):       "",
}
```

### Binning (`mapToBins`)
Numeric values are grouped into predefined bins for distribution analysis:
```go
var trackBins = []int64{0, 1, 100, 500, 1000, 5000, ...}
```

### Iterator Pattern
`selectData()` returns `iter.Seq[insights.Data]` for memory-efficient row processing.

## Database Schema

```sql
-- Raw metrics from instances
insights(id VARCHAR, time DATETIME, data JSONB)

-- Aggregated daily summaries  
summary(id INTEGER, time DATETIME UNIQUE, data JSONB)
```

## Docker Setup

- **Development**: `docker/app/` with reflex for hot reload
- **Production**: `docker/app-prod/` multi-stage build, outputs to `binary/`
- Production deployment uses Caddy (`prod/Caddyfile`)
