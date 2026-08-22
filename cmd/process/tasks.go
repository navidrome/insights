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

// cleanup keeps free space above consts.MinFreeBytes by deleting the oldest report days. It is
// silent when there is room, so any line from it means the disk is under pressure.
func cleanup(dataFolder string) func() {
	return func() {
		if err := store.PurgeToFreeSpace(dataFolder, consts.MinFreeBytes, consts.MinRetentionDays); err != nil {
			log.Printf("Error purging old reports: %v", err)
		}
	}
}

// summarizeMu serializes summarization. The cron entry and main's startup goroutine can
// otherwise write the same summary file at once. SkipIfStillRunning would miss the latter.
var summarizeMu sync.Mutex

// summarizeDay is indirected so tests can observe the mutual exclusion above.
var summarizeDay = summary.SummarizeData

func summarize(dataFolder string, days int) func() {
	return func() {
		summarizeMu.Lock()
		defer summarizeMu.Unlock()

		log.Print("Summarizing data")
		// UTC: the store reads days in UTC, but file names format in the date's own location,
		// so a local date would read one day and write another.
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
