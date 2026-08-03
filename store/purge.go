package store

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/navidrome/insights/consts"
)

// reportFileRegex matches segment files like "reports-2026-08-03.001.ndjson.gz". The ".gz" is
// optional so a segment decompressed by hand expires with the rest of its day. Anything else
// sharing the tree — the ingest lock file above all — does not match and is left alone.
var reportFileRegex = regexp.MustCompile(`^reports-(\d{4}-\d{2}-\d{2})\.(\d{3})\.ndjson(\.gz)?$`)

// PurgeOldFiles deletes the segments of every UTC day older than retentionDays and prunes the
// directories they leave empty. Unlike the SQLite purge it replaces, which needed a periodic
// VACUUM to shrink the database file, deleting whole files reclaims the disk immediately.
//
// The oldest day kept is retentionDays days back; the day before it is the newest one deleted.
func PurgeOldFiles(dataFolder string, retentionDays int) error {
	baseDir := filepath.Join(dataFolder, consts.ReportsDir)
	cutoff := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -retentionDays)

	var removed int
	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error { //#nosec G703 -- baseDir is from controlled env var and constant
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		matches := reportFileRegex.FindStringSubmatch(d.Name())
		if matches == nil {
			return nil
		}
		day, parseErr := time.ParseInLocation(consts.DateFormat, matches[1], time.UTC)
		if parseErr != nil {
			log.Printf("Skipping report file with invalid date %s: %v", path, parseErr) //#nosec G706 -- path comes from a controlled directory walk
			return nil
		}
		if !day.Before(cutoff) {
			return nil
		}
		if rmErr := os.Remove(path); rmErr != nil { //#nosec G122 -- path comes from a controlled directory walk under DATA_FOLDER
			log.Printf("Error deleting %s: %v", path, rmErr) //#nosec G706 -- path comes from a controlled directory walk
			return nil
		}
		removed++
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	log.Printf("Deleted %d expired report files", removed)
	return pruneEmptyDirs(baseDir)
}

// pruneEmptyDirs removes the empty directories under baseDir, deepest first. baseDir itself
// stays: ingest keeps its lock file there and expects the directory to exist.
func pruneEmptyDirs(baseDir string) error {
	var dirs []string
	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error { //#nosec G703 -- baseDir is from controlled env var and constant
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() && path != baseDir {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Deepest paths first, so a month directory is emptied before its year is considered.
	slices.Sort(dirs)
	slices.Reverse(dirs)
	for _, dir := range dirs {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil || len(entries) > 0 {
			continue
		}
		if rmErr := os.Remove(dir); rmErr != nil {
			log.Printf("Error removing empty dir %s: %v", dir, rmErr) //#nosec G706 -- dir comes from a controlled directory walk
		}
	}
	return nil
}
