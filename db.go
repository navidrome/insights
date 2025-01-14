package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

func openDB(fileName string) (*sql.DB, error) {
	params := url.Values{
		"_journal_mode": []string{"WAL"},
		"_synchronous":  []string{"NORMAL"},
		"cache_size":    []string{"1000000000"},
		"cache":         []string{"shared"},
		"_busy_timeout": []string{"5000"},
		"_txlock":       []string{"immediate"},
	}
	dataSourceName := fmt.Sprintf("file:%s?%s", fileName, params.Encode())
	db, err := sql.Open("sqlite3", dataSourceName)
	if err != nil {
		return nil, err
	}

	// Create schema if not exists
	createTableQuery := `
CREATE TABLE IF NOT EXISTS insights (
	id VARCHAR NOT NULL,
	time DATETIME default CURRENT_TIMESTAMP,
	data JSONB,
	PRIMARY KEY (id, time)
);
CREATE TABLE IF NOT EXISTS summary (
    id   INTEGER   PRIMARY KEY AUTOINCREMENT,
    time DATETIME UNIQUE,
    data JSONB
);
CREATE INDEX IF NOT EXISTS idx_summary_time ON summary (time);
`
	_, err = db.Exec(createTableQuery)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	return db, nil
}

func saveReport(db *sql.DB, data insights.Data, t time.Time) error {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return err
	}

	query := `INSERT INTO insights (id, data, time) VALUES (?, ?, ?)`
	_, err = db.Exec(query, data.InsightsID, dataJSON, t.Format("2006-01-02 15:04:05"))
	return err
}

func saveSummary(db *sql.DB, summary Summary, t time.Time) error {
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return err
	}

	query := `INSERT INTO summary (time, data) VALUES (?, ?) ON CONFLICT(time) DO UPDATE SET data=?`
	_, err = db.Exec(query, t.Format("2006-01-02 15:04:05"), summaryJSON, summaryJSON)
	return err
}

func purgeOldEntries(db *sql.DB) error {
	// Delete entries older than 30 days
	query := `DELETE FROM insights WHERE time < ?`
	cnt, err := db.Exec(query, time.Now().Add(-90*24*time.Hour))
	if err != nil {
		return err
	}
	deleted, _ := cnt.RowsAffected()
	log.Printf("Deleted %d old entries\n", deleted)
	return nil
}
