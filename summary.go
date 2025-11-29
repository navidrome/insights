package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/navidrome/navidrome/core/metrics/insights"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

type Summary struct {
	NumInstances   int64             `json:"numInstances,omitempty"`
	NumActiveUsers int64             `json:"numActiveUsers,omitempty"`
	Versions       map[string]uint64 `json:"versions,omitempty"`
	OS             map[string]uint64 `json:"os,omitempty"`
	Distros        map[string]uint64 `json:"distros,omitempty"`
	PlayerTypes    map[string]uint64 `json:"playerTypes,omitempty"`
	Players        map[string]uint64 `json:"players,omitempty"`
	Users          map[string]uint64 `json:"users,omitempty"`
	Tracks         map[string]uint64 `json:"tracks,omitempty"`
	MusicFS        map[string]uint64 `json:"musicFS,omitempty"`
	DataFS         map[string]uint64 `json:"dataFS,omitempty"`
	LibSizeAverage int64             `json:"libSizeAverage,omitempty"`
	LibSizeStdDev  float64           `json:"libSizeStdDev,omitempty"`
}

func summarizeData(db *sql.DB, date time.Time) error {
	rows, err := selectData(db, date)
	if err != nil {
		log.Printf("Error selecting data: %s", err)
		return err
	}
	summary := Summary{
		Versions:    make(map[string]uint64),
		OS:          make(map[string]uint64),
		Distros:     make(map[string]uint64),
		PlayerTypes: make(map[string]uint64),
		Players:     make(map[string]uint64),
		Users:       make(map[string]uint64),
		Tracks:      make(map[string]uint64),
		MusicFS:     make(map[string]uint64),
		DataFS:      make(map[string]uint64),
	}
	var numInstances int64
	var sumTracks int64
	var sumTracksSquared int64
	for data := range rows {
		// Summarize data here
		summary.NumInstances++
		summary.NumActiveUsers += data.Library.ActiveUsers
		summary.Versions[mapVersion(data)]++
		summary.OS[mapOS(data)]++
		if data.OS.Type == "linux" && !data.OS.Containerized {
			summary.Distros[data.OS.Distro]++
		}
		summary.Users[fmt.Sprintf("%d", data.Library.ActiveUsers)]++
		summary.MusicFS[mapFS(data.FS.Music)]++
		summary.DataFS[mapFS(data.FS.Data)]++
		totalPlayers := mapPlayerTypes(data, summary.PlayerTypes)
		summary.Players[fmt.Sprintf("%d", totalPlayers)]++
		mapToBins(data.Library.Tracks, trackBins, summary.Tracks)
		if data.Library.Tracks > 0 {
			sumTracks += data.Library.Tracks
			sumTracksSquared += data.Library.Tracks * data.Library.Tracks
			numInstances++
		}
	}
	if numInstances == 0 {
		log.Printf("No data to summarize for %s", date.Format("2006-01-02"))
		return nil
	}
	summary.LibSizeAverage = sumTracks / numInstances
	mean := float64(sumTracks) / float64(numInstances)
	variance := float64(sumTracksSquared)/float64(numInstances) - mean*mean
	summary.LibSizeStdDev = math.Sqrt(variance)

	// Save summary to database
	err = saveSummary(db, summary, date)
	if err != nil {
		log.Printf("Error saving summary: %s", err)
	}
	return err
}

// Match the first 8 characters of a git sha
var versionRegex = regexp.MustCompile(`\(([0-9a-fA-F]{8})[0-9a-fA-F]*\)`)

func mapVersion(data insights.Data) string {
	return versionRegex.ReplaceAllString(data.Version, "($1)")
}

var trackBins = []int64{0, 1, 100, 500, 1000, 5000, 10000, 20000, 50000, 100000, 500000, 1000000}

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
			s := caser.String(data.OS.Type)
			return strings.Replace(s, "bsd", "BSD", -1)
		}
	}()
	return os + " - " + data.OS.Arch
}

var playersTypes = map[*regexp.Regexp]string{
	regexp.MustCompile("NavidromeUI.*"):       "NavidromeUI",
	regexp.MustCompile("supersonic"):          "Supersonic",
	regexp.MustCompile("feishin"):             "", // Discard (old version reporting multiple times)
	regexp.MustCompile("audioling"):           "Audioling",
	regexp.MustCompile("^AginMusic.*"):        "AginMusic",
	regexp.MustCompile("playSub.*"):           "play:Sub",
	regexp.MustCompile("eu.callcc.audrey"):    "audrey",
	regexp.MustCompile("DSubCC"):              "", // Discard (chromecast)
	regexp.MustCompile(`bonob\+.*`):           "", // Discard (transcodings)
	regexp.MustCompile("https?://airsonic.*"): "Airsonic Refix",
	regexp.MustCompile("multi-scrobbler.*"):   "Multi-Scrobbler",
	regexp.MustCompile("SubMusic.*"):          "SubMusic",
	regexp.MustCompile("(?i)(hiby|_hiby_)"):   "HiBy",
	regexp.MustCompile("microSub"):            "AVSub",
}

func mapPlayerTypes(data insights.Data, players map[string]uint64) int64 {
	seen := map[string]uint64{}
	for p, count := range data.Library.ActivePlayers {
		for r, t := range playersTypes {
			if r.MatchString(p) {
				p = t
				break
			}
		}
		if p != "" {
			v := seen[p]
			seen[p] = max(v, uint64(count))
		}
	}
	var total int64
	for k, v := range seen {
		total += int64(v)
		players[k] += v
	}
	return total
}

var fsMappings = map[string]string{
	"unknown(0x2011bab0)": "exfat",
	"unknown(0x7366746e)": "ntfs",
	"unknown(0xc36400)":   "ceph",
	"unknown(0xf15f)":     "ecryptfs",
	"unknown(0xff534d42)": "cifs",
	"unknown(0x786f4256)": "vboxsf",
	"unknown(0xf2f52010)": "f2fs",
}

func mapFS(fs *insights.FSInfo) string {
	if fs == nil {
		return "unknown"
	}
	if t, ok := fsMappings[fs.Type]; ok {
		return t
	}
	return strings.ToLower(fs.Type)
}

func selectData(db *sql.DB, date time.Time) (iter.Seq[insights.Data], error) {
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
