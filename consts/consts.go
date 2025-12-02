package consts

import "time"

// Server configuration
const (
	DefaultPort       = "8080"
	ReadHeaderTimeout = 3 * time.Second
	RateLimitRequests = 1
	RateLimitWindow   = 30 * time.Minute
)

// Cron schedules
const (
	CronSummarize     = "0 */2 * * *" // Every 2 hours
	CronGenerateChart = "5 0 * * *"   // Daily at 00:05 UTC
	CronCleanup       = "30 0 * * *"  // Daily at 00:30 UTC
)

// Data retention and summarization
const (
	SummarizeLookbackDays = 10
	PurgeRetentionDays    = 60
)

// File paths and directories
const (
	ChartDataDir   = "web/chartdata"
	WebIndexPath   = "web/index.html"
	ChartsJSONFile = "charts.json"
	SummariesDir   = "summaries"
)

// File permissions
const (
	DirPermissions  = 0750
	FilePermissions = 0600
)

// Date formats
const (
	DateFormat      = "2006-01-02"
	DateTimeFormat  = "2006-01-02 15:04:05"
	ChartDateFormat = "Jan 02, 2006"
)

// Chart configuration
const (
	ChartWidth           = "1400px"
	ChartHeight          = "500px"
	TopVersionsCount     = 15
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
