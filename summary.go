package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"strings"
	"time"

	"github.com/navidrome/navidrome/core/metrics/insights"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

type Summary struct {
	Versions map[string]uint64
	OS       map[string]uint64
	Players  map[string]uint64
	Users    map[string]uint64
	Tracks   map[string]uint64
}

func summarizeData(db *sql.DB, date time.Time) error {
	rows, err := selectData(db, date)
	if err != nil {
		log.Printf("Error selecting data: %s", err)
		return err
	}
	summary := Summary{
		Versions: make(map[string]uint64),
		OS:       make(map[string]uint64),
		Players:  make(map[string]uint64),
		Users:    make(map[string]uint64),
		Tracks:   make(map[string]uint64),
	}
	for data := range rows {
		// Summarize data here
		summary.Versions[mapVersion(data)]++
		summary.OS[mapOS(data)]++
		mapPlayers(data, summary.Players)
		mapToBins(data.Library.ActiveUsers, userBins, summary.Users)
		mapToBins(data.Library.Tracks, trackBins, summary.Tracks)
	}
	// Save summary to database
	err = saveSummary(db, summary, date)
	if err != nil {
		log.Printf("Error saving summary: %s", err)
		return err
	}
	return err
}

func mapVersion(data insights.Data) string { return data.Version }

var trackBins = []int64{0, 1, 100, 500, 1000, 5000, 10000, 20000, 50000, 100000, 500000, 1000000}
var userBins = []int64{0, 1, 5, 10, 20, 50, 100, 200, 500, 1000}

func mapToBins(count int64, bins []int64, counters map[string]uint64) {
	for i := range bins {
		bin := bins[len(bins)-1-i]
		if count >= bin {
			counters[fmt.Sprintf("%d", bin)]++
			return
		}
	}
}

var caser = cases.Title(language.Und)

func mapOS(data insights.Data) string {
	os := func() string {
		switch data.OS.Type {
		case "darwin":
			return "macOS"
		case "linux":
			if data.OS.Containerized {
				return "Linux (containerized)"
			}
			return "Linux"
		default:
			s := strings.Replace(data.OS.Type, "bsd", "BSD", -1)
			return caser.String(s)
		}
	}()
	return os + " - " + data.OS.Arch
}

func mapPlayers(data insights.Data, players map[string]uint64) {
	for p, count := range data.Library.ActivePlayers {
		players[p] = uint64(count)
	}
}

func selectData(db *sql.DB, date time.Time) (iter.Seq[insights.Data], error) {
	query := `
SELECT i1.id, i1.time, i1.data
FROM insights i1
INNER JOIN (
    SELECT id, MAX(time) as max_time
    FROM insights
    WHERE time >= date(?, '-1 day') AND time < date(?)
    GROUP BY id
) i2 ON i1.id = i2.id AND i1.time = i2.max_time
WHERE i1.time >= date(?, '-1 day') AND time < date(?)
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
