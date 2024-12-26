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
		_ = summarizeData(db, time.Now())
	}
}
