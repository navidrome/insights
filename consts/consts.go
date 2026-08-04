package consts

import "time"

// Server configuration
const (
	DefaultPort       = "8080"
	ReadHeaderTimeout = 3 * time.Second
	// ReadTimeout bounds the whole request, headers and body together. Without it a client
	// that dribbles its body out a byte at a time holds a handler open indefinitely, and
	// ingest's shutdown waits for its handlers before closing the report writer — so a single
	// slow-loris connection could stall the drain past its deadline. Reports average ~1.6KB,
	// so this is orders of magnitude more than any honest client needs, and it stays under
	// the shutdown deadline the drain is measured against.
	ReadTimeout = 5 * time.Second
	// RateLimitRequests is per client IP, not per instance. Several instances can share one
	// public IP behind NAT, and they report ~5 minutes after startup — so a host running two
	// of them loses one report on every correlated restart if the allowance is 1. An instance
	// reports about once a day, so this stays far above legitimate use while still capping a
	// flood. The undercount it prevents is invisible in the data, since no IP is stored.
	RateLimitRequests = 10
	RateLimitWindow   = 30 * time.Minute
)

// Cron schedules
const (
	CronSummarize     = "0 */2 * * *" // Every 2 hours
	CronGenerateChart = "5 0 * * *"   // Daily at 00:05 UTC
	// CronCleanup runs hourly, not daily: retention is now driven by free space, and a daily
	// check cannot stop a volume that fills between two runs. A full disk takes ingest down —
	// it fails fast by design — so the purge has to get a look in well before that. The :30
	// offset keeps it off the hour that summarization starts on, on a 1 vCPU box.
	CronCleanup = "30 * * * *" // Hourly at :30
)

// Data retention and summarization
const (
	SummarizeLookbackDays = 5
	// MinFreeBytes is the free space the purge tries to keep available on the volume holding
	// the data folder. It is a floor on the whole volume, not a cap on what reports may use:
	// what matters is that the box keeps working, whatever is consuming the disk.
	MinFreeBytes = 500 << 20 // 500 MiB
	// MinRetentionDays is the age below which a report day is never deleted, no matter how
	// tight the disk is. It is a safety floor, not a target: with free space to spare the
	// store keeps every day it has.
	MinRetentionDays = 7
)

// MinRetentionDays must stay strictly greater than SummarizeLookbackDays. SummarizeData
// re-reads the last SummarizeLookbackDays days every two hours; if disk pressure deleted a day
// still inside that window, the day would either drop out of the charts or be rewritten from
// partial data, with nothing in the logs to say why. This blank constant asserts the invariant
// at compile time: lowering MinRetentionDays to SummarizeLookbackDays or below makes the
// expression negative, and a negative constant does not convert to uint.
const _ = uint(MinRetentionDays - SummarizeLookbackDays - 1)

// Report file storage
const (
	// FlushInterval bounds how much buffered data an unclean shutdown can lose.
	// Measured cost against one-shot compression: 0.7%.
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
)

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
