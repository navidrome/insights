package main

import (
	"log"
	"time"

	"github.com/navidrome/insights/charts"
	"github.com/navidrome/insights/consts"
	"github.com/navidrome/insights/store"
	"github.com/navidrome/insights/summary"
)

func cleanup(dataFolder string) func() {
	return func() {
		log.Print("Cleaning old data")
		if err := store.PurgeOldFiles(dataFolder, consts.PurgeRetentionDays); err != nil {
			log.Printf("Error cleaning old data: %v", err)
		}
	}
}

func summarize(dataFolder string, days int) func() {
	return func() {
		log.Print("Summarizing data")
		// UTC: the store reads days in UTC, and summary file names are formatted in the
		// date's own location, so a local-time date would read one day and write another.
		now := time.Now().Truncate(24 * time.Hour).UTC()
		for d := 0; d < days; d++ {
			date := now.AddDate(0, 0, -d)
			log.Print("Summarizing data for ", date.Format(consts.DateFormat))
			_ = summary.SummarizeData(dataFolder, date)
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
