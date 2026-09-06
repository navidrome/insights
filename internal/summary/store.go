package summary

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/navidrome/insights/internal/consts"
	"github.com/navidrome/insights/internal/fsutil"
)

type SummaryRecord struct {
	Time time.Time
	Data Summary
}

// gzipExt is the suffix that marks a compressed summary. Files without it are the plain-JSON
// form written before compression, still on disk until the migration converts them.
const gzipExt = ".gz"

func SummaryFilePath(dataFolder string, t time.Time) string {
	return filepath.Join(
		dataFolder,
		consts.SummariesDir,
		t.Format("2006"),
		t.Format("01"),
		"summary-"+t.Format(consts.DateFormat)+".json"+gzipExt,
	)
}

func SaveSummary(dataFolder string, summary Summary, t time.Time) error {
	filePath := SummaryFilePath(dataFolder, t)

	// Create directory structure if needed
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, consts.DirPermissions); err != nil {
		return err
	}

	// Marshal summary to JSON. Still indented: gzip absorbs the whitespace almost entirely, and
	// it keeps the file readable through zcat.
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}

	// Compress in memory, so what reaches the disk is one finished gzip stream. A day is about
	// 160 KB of JSON, roughly a quarter of that once compressed.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return err
	}
	// Close, not Flush: it writes the gzip footer, without which the file cannot be read back.
	if err := gz.Close(); err != nil {
		return err
	}

	// Atomic: GetSummaries runs concurrently and would log a half-written file as malformed,
	// dropping that day from the charts.
	return fsutil.WriteFileAtomic(filePath, buf.Bytes(), consts.FilePermissions)
}

// summaryPathRegex matches the layout SummaryFilePath writes, relative to the summaries
// directory. Matching the whole relative path rather than just the file name keeps a copy nested
// deeper out of the charts: production grew a summaries/2026/04/bkp/ directory of hand-made
// backups, and a name-only match loaded each of those days twice.
var summaryPathRegex = regexp.MustCompile(`^\d{4}/\d{2}/summary-(\d{4}-\d{2}-\d{2})\.json(\.gz)?$`)

// GetSummaries yields one day at a time, oldest first. Ranging over the returned sequence again
// re-reads the files, which is what lets a caller make several passes without ever holding more
// than one day. Only the day being yielded is alive; what to keep is the caller's decision.
//
// Per-file damage is logged and skipped, so one unreadable day does not cost the whole export.
func GetSummaries(dataFolder string) (iter.Seq[SummaryRecord], error) {
	baseDir := filepath.Join(dataFolder, consts.SummariesDir)

	files, err := summaryPaths(baseDir)
	if err != nil {
		return nil, err
	}

	seq := func(yield func(SummaryRecord) bool) {
		for _, f := range files {
			s, err := readSummaryFile(f.path)
			if err != nil {
				log.Printf("Warning: skipping file %s: %v", f.path, err)
				continue
			}
			// A day nobody reported on carries no signal, and an empty series point would draw
			// as a collapse in the charts.
			if s.NumInstances == 0 {
				continue
			}
			if !yield(SummaryRecord{Time: f.date, Data: s}) {
				return
			}
		}
	}
	return seq, nil
}

// readSummaryFile decodes one summary file, un-gzipping the .json.gz form on the way through.
// The decode streams off the file rather than off a buffer holding all of it, so a day costs the
// decoder's window and not the file's size.
func readSummaryFile(path string) (Summary, error) {
	f, err := os.Open(path) //#nosec G304 -- the path comes from a walk of a controlled directory
	if err != nil {
		return Summary{}, fmt.Errorf("opening: %w", err)
	}
	defer func() { _ = f.Close() }()

	var r io.Reader = f
	if strings.HasSuffix(path, gzipExt) {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return Summary{}, fmt.Errorf("decompressing: %w", err)
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}

	var s Summary
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		return Summary{}, fmt.Errorf("decoding: %w", err)
	}
	return s, nil
}

// datedPath pairs a summary file with the date in its name. Holding the two together rather than
// in separate slices indexed in step means a later filter or reorder cannot silently pair a date
// with another day's file.
type datedPath struct {
	date time.Time
	path string
}

// compressed reports whether this is the .gz form. The suffix is the only thing that says so,
// and readSummaryFile keys off the same suffix.
func (d datedPath) compressed() bool { return strings.HasSuffix(d.path, gzipExt) }

// summaryPaths returns one entry per day under baseDir, sorted oldest first. A day present in
// both forms — a plain file prod/compress-summaries.sh has not converted yet, or one it was
// interrupted before unlinking — collapses to the compressed file. Collapsing here rather than in
// each caller is what stops a day being counted twice in every chart.
//
// The paths are collected up front, and only the paths: 555 of them is about 40 KB, against the
// 28 MB of file contents that streaming keeps out of memory. The explicit sort matters because
// summaryPathRegex reads the date from the file name only and never checks it against the
// surrounding YYYY/MM directory, so a file under a mismatched directory sorts wherever WalkDir's
// lexical order puts that directory, not where its own date belongs.
func summaryPaths(baseDir string) ([]datedPath, error) {
	var entries []datedPath

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
		entries = append(entries, datedPath{date: t, path: path})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	slices.SortStableFunc(entries, func(a, b datedPath) int { return a.date.Compare(b.date) })

	// Collapse each day to one entry, the compressed copy winning: it is the one the converter
	// wrote last, and the plain file beside it is what it had not unlinked yet.
	days := entries[:0]
	for _, e := range entries {
		last := len(days) - 1
		switch {
		case last < 0 || !days[last].date.Equal(e.date):
			days = append(days, e)
		case e.compressed():
			days[last] = e
		}
	}
	return days, nil
}
