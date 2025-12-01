package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"net/url"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

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

	// Create schema if not exists
	createTableQuery := `
CREATE TABLE IF NOT EXISTS insights (
	id VARCHAR NOT NULL,
	time DATETIME default CURRENT_TIMESTAMP,
	data JSONB
);
CREATE INDEX IF NOT EXISTS insights_time ON insights(time);
CREATE INDEX IF NOT EXISTS insights_id_time ON insights(id, time);
`
	_, err = db.Exec(createTableQuery)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	return db, nil
}

func SaveReport(db *sql.DB, data insights.Data, t time.Time) error {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return err
	}

	query := `INSERT INTO insights (id, data, time) VALUES (?, ?, ?)`
	_, err = db.Exec(query, data.InsightsID, dataJSON, t.Format("2006-01-02 15:04:05"))
	return err
}

func PurgeOldEntries(db *sql.DB) error {
	// Delete entries older than 60 days
	query := `DELETE FROM insights WHERE time < ?`
	cnt, err := db.Exec(query, time.Now().Add(-60*24*time.Hour))
	if err != nil {
		return err
	}
	deleted, _ := cnt.RowsAffected()
	log.Printf("Deleted %d old entries\n", deleted)
	return nil
}

func SelectData(db *sql.DB, date time.Time) (iter.Seq[insights.Data], error) {
	query := `
SELECT i1.id, i1.time, i1.data
FROM insights i1
INNER JOIN (
    SELECT id, MAX(time) as max_time
    FROM insights
    WHERE time >= date(?) AND time < date(?, '+1 day')
    GROUP BY id
) i2 ON i1.id = i2.id AND i1.time = i2.max_time
WHERE i1.time >= date(?) AND time < date(?, '+1 day')
ORDER BY i1.id, i1.time DESC;`
	d := date.Format("2006-01-02")
	rows, err := db.Query(query, d, d, d, d)
	if err != nil {
		return nil, fmt.Errorf("querying data: %w", err)
	}
	return func(yield func(insights.Data) bool) {
		defer rows.Close()
		for rows.Next() {
			var j string
			var id string
			var t time.Time
			err := rows.Scan(&id, &t, &j)
			if err != nil {
				log.Printf("Error scanning row: %s", err)
				return
			}
			var data insights.Data
			err = json.Unmarshal([]byte(j), &data)
			if err != nil {
				log.Printf("Error unmarshalling data: %s", err)
				return
			}
			if !yield(data) {
				return
			}
		}
	}, nil
}
