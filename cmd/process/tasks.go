package main

import (
	"errors"
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
func cleanup(dataFolder string) func() error {
	return func() error {
		err := store.PurgeToFreeSpace(dataFolder, consts.MinFreeBytes, consts.MinRetentionDays)
		if err != nil {
			log.Printf("Error purging old reports: %v", err)
		}
		return err
	}
}

// forCron adapts a job for the scheduler, which has nothing useful to do with an error the job
// has already logged. Only -once turns one into an exit status.
func forCron(job func() error) func() {
	return func() { _ = job() }
}

// summarizeMu serializes summarization. The cron entry and main's startup goroutine can
// otherwise write the same summary file at once. SkipIfStillRunning would miss the latter.
var summarizeMu sync.Mutex

// summarizeDay is indirected so tests can observe the mutual exclusion above.
var summarizeDay = summary.SummarizeData

func summarize(dataFolder string, days int) func() error {
	return func() error {
		summarizeMu.Lock()
		defer summarizeMu.Unlock()

		log.Print("Summarizing data")
		// UTC: the store reads days in UTC, but file names format in the date's own location,
		// so a local date would read one day and write another.
		now := time.Now().Truncate(24 * time.Hour).UTC()
		// Every day is attempted before reporting: one unreadable day must not hide the state
		// of the rest of a backfill.
		var errs []error
		for d := 0; d < days; d++ {
			date := now.AddDate(0, 0, -d)
			log.Print("Summarizing data for ", date.Format(consts.DateFormat))
			errs = append(errs, summarizeDay(dataFolder, date))
		}
		return errors.Join(errs...)
	}
}

func generateCharts() func() error {
	return func() error {
		log.Print("Exporting charts JSON")
		err := charts.ExportChartsJSON(consts.ChartDataDir)
		if err != nil {
			log.Printf("Error exporting charts JSON: %v", err)
		}
		return err
	}
}
