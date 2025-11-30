package main

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/navidrome/insights/charts"
	"github.com/navidrome/insights/db"
	"github.com/navidrome/insights/summary"
)

const chartDataDir = "web/chartdata"

func cleanup(_ context.Context, dbConn *sql.DB) func() {
	return func() {
		log.Print("Cleaning old data")
		if err := db.PurgeOldEntries(dbConn); err != nil {
			log.Printf("Error cleaning old data: %v", err)
		}
	}
}

func summarize(_ context.Context, dbConn *sql.DB) func() {
	return func() {
		log.Print("Summarizing data")
		now := time.Now().Truncate(24 * time.Hour).UTC()
		for d := 0; d < 10; d++ {
			date := now.AddDate(0, 0, -d)
			log.Print("Summarizing data for ", date.Format("2006-01-02"))
			_ = summary.SummarizeData(dbConn, date)
		}
	}
}

func generateCharts(_ context.Context) func() {
	return func() {
		log.Print("Exporting charts JSON")
		if err := charts.ExportChartsJSON(chartDataDir); err != nil {
			log.Printf("Error exporting charts JSON: %v", err)
		}
	}
}
