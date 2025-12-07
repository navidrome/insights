# Project Context

## Purpose
Navidrome Insights Server is a Go service that collects anonymous usage metrics from Navidrome instances worldwide. It generates aggregated summaries and visualizations to help understand how Navidrome is being used without tracking individual users.

Key goals:
- Collect anonymized telemetry from Navidrome instances (version, OS, library size, player usage)
- Aggregate data into daily summaries with statistical analysis
- Generate interactive charts for visualization
- Maintain user privacy through aggregation and data retention limits

## Tech Stack
- **Language**: Go 1.25+
- **HTTP Router**: chi/v5
- **Database**: SQLite with WAL mode (embedded, single-file)
- **Testing**: Ginkgo/Gomega BDD framework
- **Charts**: go-echarts for server-rendered visualizations, exported to JSON
- **Scheduling**: robfig/cron for periodic tasks
- **Rate Limiting**: go-chi/httprate
- **External Dependency**: `github.com/navidrome/navidrome/core/metrics/insights` (Data struct definition)

## Project Conventions

### Code Style
- Standard Go formatting (`gofmt`)
- Package names: lowercase, single word when possible
- Constants grouped in `consts/consts.go` by category
- Prefer simple, flat package structure
- No exported package-level variables; use functions that return handlers or values
- Error handling: log and return, don't panic

### Architecture Patterns
- **Flat Structure**: Few packages, each with a focused responsibility
  - `cmd/server/` - HTTP server and entry point
  - `db/` - Database operations
  - `summary/` - Data aggregation logic
  - `charts/` - Visualization generation
  - `consts/` - Shared constants
- **Iterator Pattern**: `db.SelectData()` returns `iter.Seq[insights.Data]` for memory-efficient processing
- **Regex Normalization**: Player names normalized via regex map in `summary/summary.go`
- **Binning**: Numeric values grouped into predefined bins (TrackBins, AlbumBins, etc.)
- **Build Tags**: `dev` tag enables additional routes for local development

### Testing Strategy
- **Framework**: Ginkgo/Gomega (BDD style)
- **Patterns**:
  - Use `DescribeTable` for parameterized tests
  - Define local type aliases to construct `insights.Data` in tests
  - Use temp directories with `os.MkdirTemp()` for file-based tests
- **Run tests**: `go test ./...`
- **Linting**: `make lint` (runs golangci-lint in Docker)

### Git Workflow
- Single `main` branch
- Direct commits for small changes
- No specific commit message convention enforced

## Domain Context
**Data Flow**:
1. Navidrome instances POST to `/collect` (rate-limited: 1 req/30min per IP)
2. Data stored in SQLite with instance ID and timestamp
3. Cron job every 2h: Summarize last N days → JSON files in `summaries/YYYY/MM/`
4. Cron daily 00:05 UTC: Generate `charts.json` from summaries
5. Cron daily 00:30 UTC: Purge entries older than 33 days

**Key Data Types**:
- `insights.Data`: Imported from Navidrome, contains Version, OS, Library stats, Player usage
- `summary.Summary`: Aggregated metrics with counts, bins, and statistical measures
- Charts exported as JSON for frontend consumption

**Summarization Window**: Last 5 days of data are re-summarized to account for late-arriving data

## Important Constraints
- **Privacy**: Only aggregated data is exposed; raw reports purged after ~30 days
- **Rate Limiting**: 1 request per 30 minutes per IP to prevent abuse
- **Single-threaded DB**: SQLite with `SetMaxOpenConns(1)` for consistency
- **Build Modes**:
  - Production: Only `/collect` and `/api/charts` endpoints
  - Development (`-tags dev`): Adds static file serving and legacy chart endpoints

## External Dependencies
- **Navidrome**: Source of the `insights.Data` struct definition
- **No external databases**: Self-contained SQLite
- **No external APIs**: All data processing is local
