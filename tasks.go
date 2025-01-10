package main

import (
	"context"
	"database/sql"
	"log"
	"time"
)

func cleanup(_ context.Context, db *sql.DB) func() {
	return func() {
		log.Print("Cleaning old data")
		_ = purgeOldEntries(db)
	}
}

func summarize(_ context.Context, db *sql.DB) func() {
	return func() {
		log.Print("Summarizing data")
		now := time.Now().Truncate(24 * time.Hour).UTC()
		for d := 0; d < 45; d++ {
			date := now.Add(-time.Duration(d) * 24 * time.Hour)
			log.Print("Summarizing data for ", date.Format("2006-01-02"))
			_ = summarizeData(db, date)
		}
	}
}
