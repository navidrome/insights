package main

import (
	"archive/zip"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/navidrome/insights/db"
	"github.com/navidrome/insights/summary"
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
	destDB, err := db.OpenDB(consolidatedDBPath)
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
	srcDB, err := db.OpenDB(dbPath)
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

const batchSize = 30000

func importData(srcDB, destDB *sql.DB) (int64, error) {
	// Optimize for bulk import - disable sync for speed (data is recoverable from backups)
	if _, err := destDB.Exec("PRAGMA synchronous = OFF"); err != nil {
		return 0, fmt.Errorf("setting synchronous off: %w", err)
	}
	defer destDB.Exec("PRAGMA synchronous = NORMAL")

	// Query all data from source
	rows, err := srcDB.Query("SELECT id, time, data FROM insights")
	if err != nil {
		return 0, fmt.Errorf("querying source database: %w", err)
	}
	defer rows.Close()

	var totalImported int64
	var batch []struct{ id, t, data string }

	for rows.Next() {
		var id, data, t string
		if err := rows.Scan(&id, &t, &data); err != nil {
			log.Printf("Warning: error scanning row: %v", err)
			continue
		}
		batch = append(batch, struct{ id, t, data string }{id, t, data})

		if len(batch) >= batchSize {
			imported, err := insertBatch(destDB, batch)
			if err != nil {
				return totalImported, err
			}
			totalImported += imported
			batch = batch[:0]
		}
	}

	// Insert remaining rows
	if len(batch) > 0 {
		imported, err := insertBatch(destDB, batch)
		if err != nil {
			return totalImported, err
		}
		totalImported += imported
	}

	return totalImported, rows.Err()
}

func insertBatch(db *sql.DB, batch []struct{ id, t, data string }) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT OR IGNORE INTO insights (id, time, data) VALUES (?, ?, ?)")
	if err != nil {
		return 0, fmt.Errorf("preparing insert statement: %w", err)
	}
	defer stmt.Close()

	var imported int64
	for _, row := range batch {
		result, err := stmt.Exec(row.id, row.t, row.data)
		if err != nil {
			log.Printf("Warning: error inserting row: %v", err)
			continue
		}
		affected, _ := result.RowsAffected()
		imported += affected
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing transaction: %w", err)
	}
	return imported, nil
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

		if err := summary.SummarizeData(db, date); err != nil {
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
