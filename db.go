package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

type DBModel struct {
	ID   string
	Time time.Time
	insights.Data
}

func OpenDB(fileName string) (*sql.DB, error) {
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
		id TEXT PRIMARY KEY,
		time DATETIME,
		data JSONB
	);`
	_, err = db.Exec(createTableQuery)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	return db, nil
}

func SaveToDB(db *sql.DB, data insights.Data) error {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return err
	}

	query := `INSERT INTO models (id, time, data) VALUES (?, ?, ?)`
	_, err = db.Exec(query, data.InsightsID, time.Now(), dataJSON)
	return err
}
