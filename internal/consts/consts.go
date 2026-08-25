package consts

import "time"

// Server configuration
const (
	DefaultPort       = "8080"
	ReadHeaderTimeout = 3 * time.Second
	// Bounds the whole request. Without it a slow-loris upload stalls ingest's shutdown drain
	// past its deadline. Reports average ~1.6KB, so 5s is far more than any client needs.
	ReadTimeout = 5 * time.Second
	// Must be set whenever ReadTimeout is: net/http falls back to it when this is zero, and Go
	// will not replay a POST body when a pooled connection closes mid-dispatch. Above Caddy's
	// 2-minute keep-alive, so the proxy closes idle connections first.
	IdleTimeout = 150 * time.Second
	// Per client IP, not per instance: instances behind one NAT report within minutes of each
	// other. An instance reports about once a day, so this is far above legitimate use.
	RateLimitRequests = 10
	RateLimitWindow   = 30 * time.Minute
)

// Cron schedules
const (
	CronSummarize     = "0 */2 * * *" // Every 2 hours
	CronGenerateChart = "5 0 * * *"   // Daily at 00:05 UTC
	// Hourly, not daily: a daily check cannot stop a volume that fills between two runs, and a
	// full disk takes ingest down. The :30 offset keeps it off summarization's hour.
	CronCleanup = "30 * * * *" // Hourly at :30
)

// Data retention and summarization
const (
	SummarizeLookbackDays = 5
	// MinFreeBytes is a floor on the whole volume, not a cap on what reports may use.
	MinFreeBytes = 500 << 20 // 500 MiB
	// MinRetentionDays is the age below which a day is never deleted, whatever the disk looks
	// like. A safety floor, not a target.
	MinRetentionDays = 7
)

// MinRetentionDays must stay strictly greater than SummarizeLookbackDays, or the purge can
// delete a day the summarizer is still re-reading. Negative constants do not convert to uint.
const _ = uint(MinRetentionDays - SummarizeLookbackDays - 1)

// Report file storage
const (
	// FlushInterval bounds what an unclean shutdown can lose. Costs 0.7% against one-shot.
	FlushInterval = 30 * time.Second
	// MaxLineBytes caps a single report line. Payloads average ~1.6KB.
	MaxLineBytes = 4 * 1024 * 1024
)

// File paths and directories
const (
	ChartDataDir   = "web/chartdata"
	WebIndexPath   = "web/index.html"
	ChartsJSONFile = "charts.json"
	SummariesDir   = "summaries"
	ReportsDir     = "reports"
	ReportFileExt  = ".ndjson.gz"
)

// File permissions
const (
	DirPermissions  = 0750
	FilePermissions = 0600
)

// Date formats
const (
	DateFormat      = "2006-01-02"
	ChartDateFormat = "Jan 02, 2006"
)

// Chart configuration
const (
	ChartWidth           = "1400px"
	ChartHeight          = "500px"
	TopVersionsCount     = 15
	VersionSelectionDays = 60    // Rolling window (in days) for top-N version selection
	IncompleteThreshold  = 0.8   // 20% drop indicates incomplete data
	PlayerGroupThreshold = 0.002 // 0.2% threshold for grouping players

	// PlayerTypesGroupThreshold is applied when the summary is written, not when the chart is
	// drawn. Kept below PlayerGroupThreshold so the pie still decides its own slices from data
	// the summary preserved.
	PlayerTypesGroupThreshold = 0.001 // 0.1%
)

// OthersLabel is the bucket that low-count entries collapse into. Shared so the summary writer
// and the pie chart agree on one name instead of each appending its own.
const OthersLabel = "Others"

// Chart colors and styling
const (
	ChartBackgroundColor = "#ffffff"
	ChartTextColor       = "#000000"
	GapHighlightColor    = "rgba(200, 200, 200, 0.3)"
	GapLabelColor        = "#888888"
)

// API configuration
const (
	AuthHeaderPrefix = "Bearer "
	APIKeyQueryParam = "api_key"
)
