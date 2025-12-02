package summary

import (
	"encoding/json"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/navidrome/insights/consts"
)

type SummaryRecord struct {
	Time time.Time
	Data Summary
}

func SummaryFilePath(t time.Time) string {
	dataFolder := os.Getenv("DATA_FOLDER")
	return filepath.Join(
		dataFolder,
		consts.SummariesDir,
		t.Format("2006"),
		t.Format("01"),
		"summary-"+t.Format(consts.DateFormat)+".json",
	)
}

func SaveSummary(summary Summary, t time.Time) error {
	filePath := SummaryFilePath(t)

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

	return os.WriteFile(filePath, data, consts.FilePermissions)
}

// summaryFileRegex matches files like "summary-2025-11-29.json"
var summaryFileRegex = regexp.MustCompile(`^summary-(\d{4}-\d{2}-\d{2})\.json$`)

func GetSummaries() ([]SummaryRecord, error) {
	dataFolder := os.Getenv("DATA_FOLDER")
	baseDir := filepath.Join(dataFolder, consts.SummariesDir)

	var summaries []SummaryRecord

	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip inaccessible directories/files
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}

		if d.IsDir() {
			return nil
		}

		// Check if filename matches expected pattern
		matches := summaryFileRegex.FindStringSubmatch(d.Name())
		if matches == nil {
			return nil
		}

		// Parse date from filename
		dateStr := matches[1]
		t, err := time.Parse(consts.DateFormat, dateStr)
		if err != nil {
			log.Printf("Warning: skipping file with invalid date %s: %v", path, err)
			return nil
		}

		// Read and parse file
		data, err := os.ReadFile(path) //#nosec G304 -- path is from controlled directory walk
		if err != nil {
			log.Printf("Warning: skipping unreadable file %s: %v", path, err)
			return nil
		}

		var summary Summary
		if err := json.Unmarshal(data, &summary); err != nil {
			log.Printf("Warning: skipping malformed file %s: %v", path, err)
			return nil
		}

		// Skip empty summaries
		if summary.NumInstances == 0 {
			return nil
		}

		summaries = append(summaries, SummaryRecord{Time: t, Data: summary})
		return nil
	})

	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	// Sort by date ascending
	slices.SortFunc(summaries, func(a, b SummaryRecord) int {
		return a.Time.Compare(b.Time)
	})

	return summaries, nil
}
