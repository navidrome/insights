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

	// Create table if not exists
	createTableQuery := `
CREATE TABLE IF NOT EXISTS insights (
	id VARCHAR NOT NULL,
	time DATETIME default CURRENT_TIMESTAMP,
	data JSONB,
	PRIMARY KEY (id, time)
);`
	_, err = db.Exec(createTableQuery)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	return db, nil
}

func saveToDB(db *sql.DB, data insights.Data) error {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return err
	}

	query := `INSERT INTO insights (id, data) VALUES (?, ?)`
	_, err = db.Exec(query, data.InsightsID, dataJSON)
	return err
}

func purgeOldEntries(db *sql.DB) error {
	// Delete entries older than 30 days
	query := `DELETE FROM insights WHERE time < ?`
	cnt, err := db.Exec(query, time.Now().Add(-30*24*time.Hour))
	if err != nil {
		return err
	}
	deleted, _ := cnt.RowsAffected()
	log.Printf("Deleted %d old entries\n", deleted)
	return nil
}
