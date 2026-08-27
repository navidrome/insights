package summary

import (
	"encoding/json"
	"io/fs"
	"iter"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/navidrome/insights/internal/consts"
	"github.com/navidrome/insights/internal/fsutil"
)

type SummaryRecord struct {
	Time time.Time
	Data Summary
}

func SummaryFilePath(dataFolder string, t time.Time) string {
	return filepath.Join(
		dataFolder,
		consts.SummariesDir,
		t.Format("2006"),
		t.Format("01"),
		"summary-"+t.Format(consts.DateFormat)+".json",
	)
}

func SaveSummary(dataFolder string, summary Summary, t time.Time) error {
	filePath := SummaryFilePath(dataFolder, t)

	// Create directory structure if needed
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, consts.DirPermissions); err != nil {
		return err
	}

	// Marshal summary to JSON
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}

	// Atomic: GetSummaries runs concurrently and would log a half-written file as malformed,
	// dropping that day from the charts.
	return fsutil.WriteFileAtomic(filePath, data, consts.FilePermissions)
}

// summaryPathRegex matches the layout SummaryFilePath writes, relative to the summaries
// directory. Matching the whole relative path rather than just the file name keeps a copy nested
// deeper out of the charts: production grew a summaries/2026/04/bkp/ directory of hand-made
// backups, and a name-only match loaded each of those days twice.
var summaryPathRegex = regexp.MustCompile(`^\d{4}/\d{2}/summary-(\d{4}-\d{2}-\d{2})\.json$`)

// GetSummaries yields one day at a time, oldest first. Ranging over the returned sequence again
// re-reads the files, which is what lets a caller make several passes without ever holding more
// than one day. Only the day being yielded is alive; what to keep is the caller's decision.
//
// Per-file damage is logged and skipped, so one unreadable day does not cost the whole export.
func GetSummaries(dataFolder string) (iter.Seq[SummaryRecord], error) {
	baseDir := filepath.Join(dataFolder, consts.SummariesDir)

	dates, paths, err := summaryPaths(baseDir)
	if err != nil {
		return nil, err
	}

	seq := func(yield func(SummaryRecord) bool) {
		for i, p := range paths {
			data, err := os.ReadFile(p) //#nosec G304 -- p comes from a walk of a controlled directory
			if err != nil {
				log.Printf("Warning: skipping unreadable file %s: %v", p, err)
				continue
			}
			var s Summary
			if err := json.Unmarshal(data, &s); err != nil {
				log.Printf("Warning: skipping malformed file %s: %v", p, err)
				continue
			}
			// Skip empty summaries
			if s.NumInstances == 0 {
				continue
			}
			if !yield(SummaryRecord{Time: dates[i], Data: s}) {
				return
			}
		}
	}
	return seq, nil
}

// summaryPaths returns every summary file under baseDir with its date, sorted oldest first.
//
// The paths are collected up front, and only the paths: 555 of them is about 40 KB, against the
// 28 MB of file contents that streaming keeps out of memory. Doing it this way makes the ordering
// a promise the function keeps rather than one inherited from how WalkDir happens to sort
// directories, which the bkp/ directory above already breaks.
func summaryPaths(baseDir string) ([]time.Time, []string, error) {
	type entry struct {
		date time.Time
		path string
	}
	var entries []entry

	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error { //#nosec G703 -- baseDir is from a controlled env var and constant
		if err != nil {
			// A missing summaries directory is a service that has not summarized yet, not damage.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(baseDir, path)
		if err != nil {
			// WalkDir only ever hands back paths under baseDir, so this is unreachable in
			// practice; skip rather than abort the walk over one file.
			log.Printf("Warning: skipping file outside base directory %s: %v", path, err)
			return nil
		}
		matches := summaryPathRegex.FindStringSubmatch(filepath.ToSlash(rel))
		if matches == nil {
			return nil
		}
		t, err := time.Parse(consts.DateFormat, matches[1])
		if err != nil {
			log.Printf("Warning: skipping file with invalid date %s: %v", path, err)
			return nil
		}
		entries = append(entries, entry{date: t, path: path})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}

	slices.SortFunc(entries, func(a, b entry) int { return a.date.Compare(b.date) })

	dates := make([]time.Time, len(entries))
	paths := make([]string, len(entries))
	for i, e := range entries {
		dates[i] = e.date
		paths[i] = e.path
	}
	return dates, paths, nil
}
