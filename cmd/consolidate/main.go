package main

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"iter"
	"log"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/navidrome/navidrome/core/metrics/insights"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

func main() {
	backupsPath := flag.String("backups", "", "Path to the folder containing backup zip files (required)")
	destPath := flag.String("dest", "", "Destination folder for consolidated DB and summaries (required)")
	flag.Parse()

	if *backupsPath == "" || *destPath == "" {
		flag.Usage()
		os.Exit(1)
	}

	if err := run(*backupsPath, *destPath); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func run(backupsPath, destPath string) error {
	// Ensure destination folder exists
	if err := os.MkdirAll(destPath, 0755); err != nil {
		return fmt.Errorf("creating destination folder: %w", err)
	}

	// Check if output database already exists
	consolidatedDBPath := filepath.Join(destPath, "insights.db")
	if _, err := os.Stat(consolidatedDBPath); err == nil {
		return fmt.Errorf("destination database already exists: %s", consolidatedDBPath)
	}

	// Set DATA_FOLDER for summary storage
	os.Setenv("DATA_FOLDER", destPath)

	// Create consolidated database
	log.Printf("Creating consolidated database: %s", consolidatedDBPath)
	destDB, err := openDB(consolidatedDBPath)
	if err != nil {
		return fmt.Errorf("creating consolidated database: %w", err)
	}
	defer destDB.Close()

	// Find all backup zip files
	zipFiles, err := findBackupZips(backupsPath)
	if err != nil {
		return fmt.Errorf("finding backup files: %w", err)
	}
	if len(zipFiles) == 0 {
		return fmt.Errorf("no backup zip files found in %s", backupsPath)
	}
	log.Printf("Found %d backup files", len(zipFiles))

	// Process each backup
	var totalImported int64
	for i, zipFile := range zipFiles {
		log.Printf("Processing backup %d of %d: %s", i+1, len(zipFiles), filepath.Base(zipFile))
		imported, err := processBackup(zipFile, destDB)
		if err != nil {
			log.Printf("Warning: error processing %s: %v", zipFile, err)
			continue
		}
		log.Printf("  Imported %d rows", imported)
		totalImported += imported
	}
	log.Printf("Total rows imported: %d", totalImported)

	// Generate summaries for all dates in the consolidated database
	if err := generateAllSummaries(destDB); err != nil {
		return fmt.Errorf("generating summaries: %w", err)
	}

	log.Printf("Consolidation complete!")
	return nil
}

func findBackupZips(backupsPath string) ([]string, error) {
	entries, err := os.ReadDir(backupsPath)
	if err != nil {
		return nil, err
	}

	var zipFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".zip") {
			zipFiles = append(zipFiles, filepath.Join(backupsPath, entry.Name()))
		}
	}

	// Sort by name to process in chronological order
	sort.Strings(zipFiles)
	return zipFiles, nil
}

func processBackup(zipPath string, destDB *sql.DB) (int64, error) {
	// Create temp directory for extraction
	tempDir, err := os.MkdirTemp("", "insights-backup-*")
	if err != nil {
		return 0, fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Extract insights.db from zip
	dbPath, err := extractDB(zipPath, tempDir)
	if err != nil {
		return 0, fmt.Errorf("extracting database: %w", err)
	}

	// Open source database
	srcDB, err := openDB(dbPath)
	if err != nil {
		return 0, fmt.Errorf("opening source database: %w", err)
	}
	defer srcDB.Close()

	// Import data
	return importData(srcDB, destDB)
}

func extractDB(zipPath, destDir string) (string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer r.Close()

	var dbFile *zip.File
	for _, f := range r.File {
		// Skip macOS metadata files and look for insights.db
		if strings.HasPrefix(f.Name, "__MACOSX") {
			continue
		}
		if filepath.Base(f.Name) == "insights.db" {
			dbFile = f
			break
		}
	}

	if dbFile == nil {
		return "", fmt.Errorf("insights.db not found in zip")
	}

	// Extract the database file
	destPath := filepath.Join(destDir, "insights.db")
	if err := extractFile(dbFile, destPath); err != nil {
		return "", err
	}

	// Also extract WAL and SHM files if present (for consistency)
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "__MACOSX") {
			continue
		}
		base := filepath.Base(f.Name)
		if base == "insights.db-wal" || base == "insights.db-shm" {
			extractFile(f, filepath.Join(destDir, base))
		}
	}

	return destPath, nil
}

func extractFile(f *zip.File, destPath string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	outFile, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, rc)
	return err
}

func importData(srcDB, destDB *sql.DB) (int64, error) {
	// Query all data from source
	rows, err := srcDB.Query("SELECT id, time, data FROM insights")
	if err != nil {
		return 0, fmt.Errorf("querying source database: %w", err)
	}
	defer rows.Close()

	// Prepare insert statement with OR IGNORE to skip conflicts
	stmt, err := destDB.Prepare("INSERT OR IGNORE INTO insights (id, time, data) VALUES (?, ?, ?)")
	if err != nil {
		return 0, fmt.Errorf("preparing insert statement: %w", err)
	}
	defer stmt.Close()

	var imported int64
	for rows.Next() {
		var id, data string
		var t string
		if err := rows.Scan(&id, &t, &data); err != nil {
			log.Printf("Warning: error scanning row: %v", err)
			continue
		}

		result, err := stmt.Exec(id, t, data)
		if err != nil {
			log.Printf("Warning: error inserting row: %v", err)
			continue
		}

		affected, _ := result.RowsAffected()
		imported += affected
	}

	return imported, rows.Err()
}

func generateAllSummaries(db *sql.DB) error {
	// Get all distinct dates from the database
	rows, err := db.Query("SELECT DISTINCT DATE(time) as date FROM insights ORDER BY date")
	if err != nil {
		return fmt.Errorf("querying dates: %w", err)
	}
	defer rows.Close()

	var dates []string
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			return fmt.Errorf("scanning date: %w", err)
		}
		dates = append(dates, date)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	log.Printf("Generating summaries for %d days...", len(dates))

	for i, dateStr := range dates {
		date, err := parseDate(dateStr)
		if err != nil {
			log.Printf("Warning: skipping invalid date %s: %v", dateStr, err)
			continue
		}

		if (i+1)%50 == 0 || i+1 == len(dates) {
			log.Printf("  Progress: %d/%d (current: %s)", i+1, len(dates), dateStr)
		}

		if err := summarizeData(db, date); err != nil {
			log.Printf("Warning: error summarizing %s: %v", dateStr, err)
		}
	}

	return nil
}

func parseDate(dateStr string) (t time.Time, err error) {
	// Try multiple formats since SQLite might return different formats
	formats := []string{
		"2006-01-02",
		"2006-01-02 15:04:05",
	}
	for _, format := range formats {
		t, err = time.Parse(format, dateStr)
		if err == nil {
			return t, nil
		}
	}
	return t, fmt.Errorf("could not parse date: %s", dateStr)
}

// --- Copied from main package (db.go, summary.go, summary_store.go) ---
// These are duplicated here to keep the consolidate tool self-contained

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
`
	_, err = db.Exec(createTableQuery)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	return db, nil
}

func summarizeData(db *sql.DB, date time.Time) error {
	rows, err := selectData(db, date)
	if err != nil {
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
		return nil
	}

	summary.LibSizeAverage = sumTracks / numInstances
	mean := float64(sumTracks) / float64(numInstances)
	variance := float64(sumTracksSquared)/float64(numInstances) - mean*mean
	summary.LibSizeStdDev = math.Sqrt(variance)

	return saveSummary(summary, date)
}

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
	osName := func() string {
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
	return osName + " - " + data.OS.Arch
}

var playersTypes = map[*regexp.Regexp]string{
	regexp.MustCompile("NavidromeUI.*"):       "NavidromeUI",
	regexp.MustCompile("supersonic"):          "Supersonic",
	regexp.MustCompile("feishin"):             "",
	regexp.MustCompile("audioling"):           "Audioling",
	regexp.MustCompile("^AginMusic.*"):        "AginMusic",
	regexp.MustCompile("playSub.*"):           "play:Sub",
	regexp.MustCompile("eu.callcc.audrey"):    "audrey",
	regexp.MustCompile("DSubCC"):              "",
	regexp.MustCompile(`bonob\+.*`):           "",
	regexp.MustCompile("https?://airsonic.*"): "Airsonic Refix",
	regexp.MustCompile("multi-scrobbler.*"):   "Multi-Scrobbler",
	regexp.MustCompile("SubMusic.*"):          "SubMusic",
	regexp.MustCompile("(?i)(hiby|_hiby_)"):   "HiBy",
	regexp.MustCompile("microSub"):            "AVSub",
	regexp.MustCompile("Stream Music"):        "Musiver",
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

const summariesDir = "summaries"

func summaryFilePath(t time.Time) string {
	dataFolder := os.Getenv("DATA_FOLDER")
	return filepath.Join(
		dataFolder,
		summariesDir,
		t.Format("2006"),
		t.Format("01"),
		"summary-"+t.Format("2006-01-02")+".json",
	)
}

func saveSummary(summary Summary, t time.Time) error {
	filePath := summaryFilePath(t)

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, data, 0644)
}
