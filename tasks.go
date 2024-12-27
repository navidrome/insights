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
		log.Print("Summarizing data for the last week")
		now := time.Now().Truncate(24 * time.Hour).UTC()
		for d := 0; d < 10; d++ {
			_ = summarizeData(db, now.Add(-time.Duration(d)*24*time.Hour))
		}
	}
}
