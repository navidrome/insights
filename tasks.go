package main

import (
	"context"
	"database/sql"
	"log"
	"time"
)

const chartDataDir = "web/chartdata"

func cleanup(_ context.Context, db *sql.DB) func() {
	return func() {
		log.Print("Cleaning old data")
		if err := purgeOldEntries(db); err != nil {
			log.Printf("Error cleaning old data: %v", err)
		}
	}
}

func summarize(_ context.Context, db *sql.DB) func() {
	return func() {
		log.Print("Summarizing data")
		now := time.Now().Truncate(24 * time.Hour).UTC()
		for d := 0; d < 10; d++ {
			date := now.AddDate(0, 0, -d)
			log.Print("Summarizing data for ", date.Format("2006-01-02"))
			_ = summarizeData(db, date)
		}
	}
}

func generateCharts(_ context.Context) func() {
	return func() {
		log.Print("Exporting charts JSON")
		if err := exportChartsJSON(chartDataDir); err != nil {
			log.Printf("Error exporting charts JSON: %v", err)
		}
	}
}
