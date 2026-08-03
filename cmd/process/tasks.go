package main

import (
	"log"
	"sync"
	"time"

	"github.com/navidrome/insights/charts"
	"github.com/navidrome/insights/consts"
	"github.com/navidrome/insights/store"
	"github.com/navidrome/insights/summary"
)

// cleanup keeps free space on the data volume above consts.MinFreeBytes by deleting the oldest
// report days. It runs hourly and says nothing on the runs where there is room to spare, so a
// line from it always means the disk is under pressure.
func cleanup(dataFolder string) func() {
	return func() {
		if err := store.PurgeToFreeSpace(dataFolder, consts.MinFreeBytes, consts.MinRetentionDays); err != nil {
			log.Printf("Error purging old reports: %v", err)
		}
	}
}

// summarizeMu serializes summarization runs. Two of them can otherwise overlap on the same
// date — the cron entry and the startup goroutine in main — and each one ends in a write of
// the same summary file, which a concurrent GetSummaries can catch half-written and drop the
// day from the charts. A cron-level SkipIfStillRunning would not help: the startup run is not
// a cron job. A full pass is minutes of gzip scanning on the production box, so the overlap
// window is wide.
var summarizeMu sync.Mutex

// summarizeDay is the per-date unit of work, indirected so tests can observe the mutual
// exclusion above.
var summarizeDay = summary.SummarizeData

func summarize(dataFolder string, days int) func() {
	return func() {
		summarizeMu.Lock()
		defer summarizeMu.Unlock()

		log.Print("Summarizing data")
		// UTC: the store reads days in UTC, and summary file names are formatted in the
		// date's own location, so a local-time date would read one day and write another.
		now := time.Now().Truncate(24 * time.Hour).UTC()
		for d := 0; d < days; d++ {
			date := now.AddDate(0, 0, -d)
			log.Print("Summarizing data for ", date.Format(consts.DateFormat))
			_ = summarizeDay(dataFolder, date)
		}
	}
}

func generateCharts() func() {
	return func() {
		log.Print("Exporting charts JSON")
		if err := charts.ExportChartsJSON(consts.ChartDataDir); err != nil {
			log.Printf("Error exporting charts JSON: %v", err)
		}
	}
}
