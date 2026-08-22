// Package store persists raw insights reports as gzipped NDJSON in append-only segment files,
// one directory per UTC day. Each writer session creates a NEW segment,
// reports-YYYY-MM-DD.NNN.ndjson.gz, counting from 001.
//
// Appending to an earlier session's unterminated member would lose the rest of the day to
// "flate: corrupt input". A writer flushes rather than closing the member: 0.7% worse
// compression, against 17% for closing and reopening per chunk.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/insights/consts"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

// segmentIndex accepts exactly segmentIndexDigits digits, so a segment numbered past
// maxSegmentIndex would be invisible to every reader. Hence exhausting the range is fatal.
const (
	segmentIndexDigits = 3
	maxSegmentIndex    = 999
)

// plainReportFileExt is the uncompressed variant of consts.ReportFileExt. Nothing writes it.
// Readers accept it so a manually decompressed segment can be inspected in place.
var plainReportFileExt = strings.TrimSuffix(consts.ReportFileExt, ".gz")

// Record is one line of a report file. (InsightsID, Time) is the deduplication key.
type Record struct {
	Time time.Time     `json:"time"`
	Data insights.Data `json:"data"`
}

// dayDir returns the directory holding the segments for date's UTC day.
func dayDir(dataFolder string, date time.Time) string {
	d := date.UTC()
	return filepath.Join(dataFolder, consts.ReportsDir, d.Format("2006"), d.Format("01"))
}

// segmentPrefix returns the file name prefix shared by every segment of date's UTC day,
// up to and including the separator before the index.
func segmentPrefix(date time.Time) string {
	return "reports-" + date.UTC().Format(consts.DateFormat) + "."
}

// segmentPath returns the path of segment index for date's UTC day.
func segmentPath(dataFolder string, date time.Time, index int) string {
	name := fmt.Sprintf("%s%0*d%s", segmentPrefix(date), segmentIndexDigits, index, consts.ReportFileExt)
	return filepath.Join(dayDir(dataFolder, date), name)
}

// segmentIndex extracts the segment index from a file name belonging to the given day, and
// reports false for anything that is not a segment of that day.
func segmentIndex(name, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return 0, false
	}
	rest = strings.TrimSuffix(rest, ".gz")
	digits, ok := strings.CutSuffix(rest, plainReportFileExt)
	if !ok || len(digits) != segmentIndexDigits {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	index, err := strconv.Atoi(digits)
	if err != nil || index < 1 {
		return 0, false
	}
	return index, true
}

// daySegments returns the index and path of every segment of date's UTC day, ordered by
// index. A missing or unreadable day directory yields no segments.
func daySegments(dataFolder string, date time.Time) ([]int, []string) {
	dir := dayDir(dataFolder, date)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}

	prefix := segmentPrefix(date)
	type segment struct {
		index      int
		name       string
		compressed bool
	}
	var segments []segment
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if index, ok := segmentIndex(e.Name(), prefix); ok {
			segments = append(segments, segment{
				index:      index,
				name:       e.Name(),
				compressed: strings.HasSuffix(e.Name(), ".gz"),
			})
		}
	}
	// By number, not by name. Same index in both forms: compressed sorts first, plain is
	// dropped below.
	slices.SortFunc(segments, func(a, b segment) int {
		if a.index != b.index {
			return a.index - b.index
		}
		if a.compressed != b.compressed {
			if a.compressed {
				return -1
			}
			return 1
		}
		return 0
	})

	indexes := make([]int, 0, len(segments))
	paths := make([]string, 0, len(segments))
	for _, s := range segments {
		// One entry per index, or a caller reading both forms counts every record twice.
		if len(indexes) > 0 && indexes[len(indexes)-1] == s.index {
			continue
		}
		indexes = append(indexes, s.index)
		paths = append(paths, filepath.Join(dir, s.name))
	}
	return indexes, paths
}

// DaySegmentPaths returns the existing report segments for the UTC day of date, oldest first,
// one path per index. A segment that exists in both forms yields only the compressed one.
func DaySegmentPaths(dataFolder string, date time.Time) []string {
	_, paths := daySegments(dataFolder, date)
	return paths
}

// NextSegmentPath returns the path a new writer session should create, one past the highest
// index on disk. It creates nothing, and never reuses an index: order is write order.
func NextSegmentPath(dataFolder string, date time.Time) (string, error) {
	indexes, _ := daySegments(dataFolder, date)
	next := 1
	if len(indexes) > 0 {
		next = indexes[len(indexes)-1] + 1
	}
	if next > maxSegmentIndex {
		return "", fmt.Errorf("report segments for %s are past the highest index %d",
			date.UTC().Format(consts.DateFormat), maxSegmentIndex)
	}
	return segmentPath(dataFolder, date, next), nil
}

// HasDay reports whether any report segment exists for the UTC day of date. A day that was
// never recorded must never be summarized as an empty one.
func HasDay(dataFolder string, date time.Time) bool {
	return len(DaySegmentPaths(dataFolder, date)) > 0
}
