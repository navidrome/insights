// Package store persists raw insights reports as gzipped NDJSON, split into segment files
// under one directory per UTC day.
//
// Each writer session (process start, or a UTC day rollover within a session) creates a NEW
// segment: reports-YYYY-MM-DD.NNN.ndjson.gz, NNN counting from 001. A session never appends
// to a segment written by an earlier one. That boundary is what makes an unclean shutdown
// survivable: a killed process leaves its member without a gzip trailer, and appending a new
// member after it would place a gzip header inside an open flate stream, which gzip.Reader
// reads as compressed data and rejects with "flate: corrupt input" — losing every record
// written for the rest of that day. With segments, the damage stops at the tail of the
// segment that was open when the process died, and readers recover the rest.
//
// Segments are append-only and never rewritten. A writer keeps one gzip stream open and
// flushes periodically rather than closing the member, which costs 0.7% compression against
// one-shot; closing and reopening per chunk costs 17%.
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

// Segment indexes are zero-padded to a fixed width so that lexicographic order over file
// names is also chronological order. maxSegmentIndex is the largest index that fits that
// width; beyond it the padding, and so the ordering guarantee, would break.
const (
	segmentIndexDigits = 3
	maxSegmentIndex    = 999
)

// plainReportFileExt is the uncompressed variant of consts.ReportFileExt. Nothing writes it;
// it is accepted on read so a manually decompressed segment can be inspected in place.
var plainReportFileExt = strings.TrimSuffix(consts.ReportFileExt, ".gz")

// Record is one line of a report file: the payload plus the time the server received it.
// (InsightsID, Time) is the deduplication key.
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

// segmentIndex extracts the segment index from a file name belonging to the given day. It
// reports false for anything that is not a segment of that day, so unrelated files sharing
// the directory are ignored rather than read as reports.
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
	// Sort by index so that ordering follows the number, not the string. When the same index
	// exists in both forms, the compressed one sorts first and the plain one is dropped below.
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
		// One entry per index: a caller that read both forms of a segment would count every
		// record in it twice.
		if len(indexes) > 0 && indexes[len(indexes)-1] == s.index {
			continue
		}
		indexes = append(indexes, s.index)
		paths = append(paths, filepath.Join(dir, s.name))
	}
	return indexes, paths
}

// DaySegmentPaths returns the existing report segments for the UTC day of date, oldest
// first, exactly one path per segment index: if a segment exists both compressed and
// uncompressed, only the compressed one is returned. The result is empty when the day has
// no segments.
func DaySegmentPaths(dataFolder string, date time.Time) []string {
	_, paths := daySegments(dataFolder, date)
	return paths
}

// NextSegmentPath returns the path a new writer session should create for the UTC day of
// date: one past the highest index on disk. It does not create the file or its directory.
//
// Indexes are never reused, not even one freed by deleting a segment by hand. Readers take
// segment order to be write order, and refilling a gap would put today's records ahead of
// records written days earlier.
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

// HasDay reports whether any report segment exists for the UTC day of date. Callers use this
// to distinguish "no data collected" from "day never recorded" — a missing day must never be
// summarized as an empty one.
func HasDay(dataFolder string, date time.Time) bool {
	return len(DaySegmentPaths(dataFolder, date)) > 0
}
