// Package store persists raw insights reports as gzipped NDJSON, one file per UTC day.
//
// Files are append-only and never rewritten. A writer keeps one gzip stream open and
// flushes periodically; a restart appends a new gzip member to the same file, which is
// valid multi-member gzip and read natively by gzip.Reader.
package store

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/navidrome/insights/consts"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

// Record is one line of a report file: the payload plus the time the server received it.
// (InsightsID, Time) is the deduplication key.
type Record struct {
	Time time.Time     `json:"time"`
	Data insights.Data `json:"data"`
}

// DayFilePath returns the compressed report file path for the UTC day of date.
func DayFilePath(dataFolder string, date time.Time) string {
	d := date.UTC()
	return filepath.Join(
		dataFolder,
		consts.ReportsDir,
		d.Format("2006"),
		d.Format("01"),
		"reports-"+d.Format(consts.DateFormat)+consts.ReportFileExt,
	)
}

// plainDayFilePath is the uncompressed variant of DayFilePath. Nothing writes it; it is
// accepted on read so a manually decompressed file can be inspected in place.
func plainDayFilePath(dataFolder string, date time.Time) string {
	return strings.TrimSuffix(DayFilePath(dataFolder, date), ".gz")
}

// HasDay reports whether a report file exists for the UTC day of date. Callers use this to
// distinguish "no data collected" from "day never recorded" — a missing file must never be
// summarized as an empty day.
func HasDay(dataFolder string, date time.Time) bool {
	if _, err := os.Stat(DayFilePath(dataFolder, date)); err == nil {
		return true
	}
	_, err := os.Stat(plainDayFilePath(dataFolder, date))
	return err == nil
}
