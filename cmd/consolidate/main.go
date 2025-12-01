package main

import (
	"archive/zip"
	"crypto/md5" //#nosec G501 -- used only for deduplication, not security
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
	"github.com/schollz/progressbar/v3"
)

func main() {
	backupsPath := flag.String("backups", "", "Path to the folder containing backup zip files (required for merge)")
	destPath := flag.String("dest", "", "Destination folder for consolidated DB and summaries (required)")
	summariesOnly := flag.Bool("summaries-only", false, "Skip DB merge and only regenerate summaries from existing DB")
	flag.Parse()

	if *destPath == "" {
		flag.Usage()
		os.Exit(1)
	}

	if !*summariesOnly && *backupsPath == "" {
		fmt.Fprintf(os.Stderr, "Error: -backups is required unless -summaries-only is set\n")
		flag.Usage()
		os.Exit(1)
	}

	if err := run(*backupsPath, *destPath, *summariesOnly); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func run(backupsPath, destPath string, summariesOnly bool) error {
	// Ensure destination folder exists
	if err := os.MkdirAll(destPath, 0750); err != nil {
		return fmt.Errorf("creating destination folder: %w", err)
	}

	// Set DATA_FOLDER for summary storage
	if err := os.Setenv("DATA_FOLDER", destPath); err != nil {
		return fmt.Errorf("setting DATA_FOLDER: %w", err)
	}

	consolidatedDBPath := filepath.Join(destPath, "insights.db")

	// If summaries-only mode, just regenerate summaries from existing DB
	if summariesOnly {
		log.Printf("Summaries-only mode: regenerating summaries from existing database")
		destDB, err := db.OpenDB(consolidatedDBPath)
		if err != nil {
			return fmt.Errorf("opening existing database: %w", err)
		}
		defer func() { _ = destDB.Close() }()

		if err := generateAllSummaries(destDB); err != nil {
			return fmt.Errorf("generating summaries: %w", err)
		}

		log.Printf("Summary regeneration complete!")
		return nil
	}

	// Check if output database already exists
	if _, err := os.Stat(consolidatedDBPath); err == nil {
		return fmt.Errorf("destination database already exists: %s", consolidatedDBPath)
	}

	// Create consolidated database (without indexes for faster inserts)
	log.Printf("Creating consolidated database: %s", consolidatedDBPath)
	destDB, err := openDestDB(consolidatedDBPath)
	if err != nil {
		return fmt.Errorf("creating consolidated database: %w", err)
	}
	defer func() { _ = destDB.Close() }()

	// Apply bulk import optimizations
	if err := applyBulkPragmas(destDB); err != nil {
		return fmt.Errorf("applying bulk pragmas: %w", err)
	}

	// Find all backup zip files
	zipFiles, err := findBackupZips(backupsPath)
	if err != nil {
		return fmt.Errorf("finding backup files: %w", err)
	}
	if len(zipFiles) == 0 {
		return fmt.Errorf("no backup zip files found in %s", backupsPath)
	}
	log.Printf("Found %d backup files", len(zipFiles))

	// Track seen (id, time) pairs to avoid duplicates across backups
	seenKeys := make(map[[16]byte]struct{})

	// Process each backup
	var totalImported int64
	for i, zipFile := range zipFiles {
		log.Printf("Processing backup %d/%d: %s", i+1, len(zipFiles), filepath.Base(zipFile))
		imported, err := processBackup(zipFile, destDB, seenKeys)
		if err != nil {
			log.Printf("Warning: error processing %s: %v", filepath.Base(zipFile), err)
		}
		totalImported += imported
	}
	log.Printf("Total rows imported: %d (dedup set size: %d)", totalImported, len(seenKeys))

	// Create indexes after all imports
	if err := createIndexes(destDB); err != nil {
		return fmt.Errorf("creating indexes: %w", err)
	}

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

func processBackup(zipPath string, destDB *sql.DB, seenKeys map[[16]byte]struct{}) (int64, error) {
	// Create temp directory for extraction
	tempDir, err := os.MkdirTemp("", "insights-backup-*")
	log.Printf("Extracting backup to temp dir: %s", tempDir)
	if err != nil {
		return 0, fmt.Errorf("creating temp directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

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
	defer func() { _ = srcDB.Close() }()

	// Import data
	return importData(zipPath, srcDB, destDB, seenKeys)
}

func extractDB(zipPath, destDir string) (string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()

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
			_ = extractFile(f, filepath.Join(destDir, base))
		}
	}

	return destPath, nil
}

func extractFile(f *zip.File, destPath string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()

	outFile, err := os.Create(destPath) //#nosec G304 -- destPath is controlled
	if err != nil {
		return err
	}
	defer func() { _ = outFile.Close() }()

	_, err = io.Copy(outFile, rc) //#nosec G110 -- src is controlled
	return err
}

const (
	batchSize       = 30000 // rows to collect before flushing to DB
	insertBatchSize = 5000  // rows per multi-value INSERT statement
)

type row struct{ id, t, data string }

func applyBulkPragmas(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA synchronous = OFF",
		"PRAGMA journal_mode = OFF",
		"PRAGMA locking_mode = EXCLUSIVE",
		"PRAGMA temp_store = MEMORY",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("executing %s: %w", p, err)
		}
	}
	return nil
}

// openDestDB opens a database for bulk imports (no primary key, no index)
func openDestDB(fileName string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", fileName)
	if err != nil {
		return nil, err
	}

	// Set page size before creating any tables
	if _, err := db.Exec("PRAGMA page_size = 16384"); err != nil {
		return nil, fmt.Errorf("setting page size: %w", err)
	}

	// Create table WITHOUT primary key for faster inserts
	createTableQuery := `
CREATE TABLE IF NOT EXISTS insights (
	id VARCHAR NOT NULL,
	time DATETIME default CURRENT_TIMESTAMP,
	data JSONB
)`
	if _, err := db.Exec(createTableQuery); err != nil {
		return nil, fmt.Errorf("creating table: %w", err)
	}

	db.SetMaxOpenConns(1)
	return db, nil
}

func createIndexes(db *sql.DB) error {
	log.Printf("Creating indexes...")
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS insights_time ON insights(time)"); err != nil {
		return err
	}
	_, err := db.Exec("CREATE INDEX IF NOT EXISTS insights_id_time ON insights(id, time)")
	return err
}

// hashKey creates an MD5 hash of the (id, time) pair for deduplication
func hashKey(id, t string) [16]byte {
	return md5.Sum([]byte(id + "\x00" + t)) //#nosec G401 -- used only for deduplication, not security
}

func importData(srcName string, srcDB, destDB *sql.DB, seenKeys map[[16]byte]struct{}) (int64, error) {
	// Get row count for progress bar
	var rowCount int64
	countSQL := "SELECT COUNT(*) FROM insights"
	if err := srcDB.QueryRow(countSQL).Scan(&rowCount); err != nil {
		return 0, fmt.Errorf("counting rows: %w", err)
	}

	// Query all data from source
	rows, err := srcDB.Query("SELECT id, time, data FROM insights")
	if err != nil {
		return 0, fmt.Errorf("querying source database: %w", err)
	}
	defer func() { _ = rows.Close() }()

	description := fmt.Sprintf("  %s", filepath.Base(srcName))
	bar := progressbar.NewOptions64(rowCount,
		progressbar.OptionSetDescription(description),
		progressbar.OptionShowCount(),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionFullWidth(),
		progressbar.OptionShowIts(),
	)

	var totalImported int64
	var totalScanned int64
	var batch []row

	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.t, &r.data); err != nil {
			log.Printf("\nWarning: error scanning row: %v", err)
			continue
		}
		totalScanned++

		// Skip duplicates using hash set
		key := hashKey(r.id, r.t)
		if _, seen := seenKeys[key]; seen {
			if totalScanned%int64(batchSize) == 0 {
				_ = bar.Add(batchSize)
			}
			continue
		}
		seenKeys[key] = struct{}{}

		batch = append(batch, r)

		if len(batch) >= batchSize {
			imported, err := insertBatch(destDB, batch)
			if err != nil {
				return totalImported, err
			}
			totalImported += imported
			_ = bar.Set64(totalScanned)
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
	_ = bar.Set64(totalScanned)

	fmt.Println() // newline after progress bar
	return totalImported, rows.Err()
}

// buildMultiInsertSQL builds a multi-value INSERT statement for n rows
func buildMultiInsertSQL(n int) string {
	var sb strings.Builder
	sb.WriteString("INSERT INTO insights (id, time, data) VALUES ")
	for i := range n {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("(?,?,?)")
	}
	return sb.String()
}

func insertBatch(db *sql.DB, batch []row) (int64, error) {
	if len(batch) == 0 {
		return 0, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Cache prepared statements within this transaction
	txStmtCache := make(map[int]*sql.Stmt)
	defer func() {
		for _, stmt := range txStmtCache {
			_ = stmt.Close()
		}
	}()

	getStmt := func(n int) (*sql.Stmt, error) {
		if stmt, ok := txStmtCache[n]; ok {
			return stmt, nil
		}
		stmt, err := tx.Prepare(buildMultiInsertSQL(n))
		if err != nil {
			return nil, err
		}
		txStmtCache[n] = stmt
		return stmt, nil
	}

	var totalImported int64

	// Process in chunks of insertBatchSize using multi-value INSERT
	for i := 0; i < len(batch); i += insertBatchSize {
		end := min(i+insertBatchSize, len(batch))
		chunk := batch[i:end]

		stmt, err := getStmt(len(chunk))
		if err != nil {
			return totalImported, fmt.Errorf("preparing statement: %w", err)
		}

		args := make([]any, 0, len(chunk)*3)
		for _, r := range chunk {
			args = append(args, r.id, r.t, r.data)
		}

		result, err := stmt.Exec(args...)
		if err != nil {
			return totalImported, fmt.Errorf("executing batch insert: %w", err)
		}
		affected, _ := result.RowsAffected()
		totalImported += affected
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing transaction: %w", err)
	}
	return totalImported, nil
}

func generateAllSummaries(db *sql.DB) error {
	// Get all distinct dates from the database
	rows, err := db.Query("SELECT DISTINCT DATE(time) as date FROM insights ORDER BY date")
	if err != nil {
		return fmt.Errorf("querying dates: %w", err)
	}
	defer func() { _ = rows.Close() }()

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

	bar := progressbar.NewOptions(len(dates),
		progressbar.OptionSetDescription("Generating summaries"),
		progressbar.OptionShowCount(),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionFullWidth(),
	)

	for _, dateStr := range dates {
		date, err := parseDate(dateStr)
		if err != nil {
			log.Printf("\nWarning: skipping invalid date %s: %v", dateStr, err)
			_ = bar.Add(1)
			continue
		}

		if err := summary.SummarizeData(db, date); err != nil {
			log.Printf("\nWarning: error summarizing %s: %v", dateStr, err)
		}
		_ = bar.Add(1)
	}
	fmt.Println() // newline after progress bar

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
