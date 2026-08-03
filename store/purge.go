package store

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"
	"time"

	"github.com/navidrome/insights/consts"
)

// reportFileRegex matches segment files like "reports-2026-08-03.001.ndjson.gz". The ".gz" is
// optional so a segment decompressed by hand expires with the rest of its day. Anything else
// sharing the tree — the ingest lock file above all — does not match and is left alone.
var reportFileRegex = regexp.MustCompile(`^reports-(\d{4}-\d{2}-\d{2})\.(\d{3})\.ndjson(\.gz)?$`)

// freeBytes reports the space available on the volume holding path. It is a package-level
// variable so tests can drive the purge with simulated disk pressure: filling a real volume to
// a threshold is neither deterministic nor safe on a developer machine.
var freeBytes = statfsFreeBytes

// statfsFreeBytes reads the free space of the volume containing path.
//
// Bavail, not Bfree: Bfree includes the blocks reserved for root, which this process cannot
// write to, so using it would report space that does not exist and let the disk fill anyway.
// The block size the count is expressed in is platform-specific; see blockSize.
func statfsFreeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	return st.Bavail * blockSize(&st), nil
}

// PurgeToFreeSpace deletes report days, oldest first, until the volume holding dataFolder has
// at least minFreeBytes available. Retention is a function of free space, not of age: while
// the disk has room, every day collected is kept.
//
// Deletion is a whole day at a time. Summaries are bucketed by UTC day, so half a day on disk
// is a day that summarizes to a wrong number rather than to no number at all.
//
// A day younger than minRetentionDays is never deleted, whatever the disk looks like. If the
// target is still unmet when the purge reaches that floor it stops and says so loudly: at that
// point something other than reports is filling the volume, and quietly working around it
// would hide the one fact an operator needs.
//
// Nothing is logged when free space already meets the target — this runs hourly, and a line an
// hour saying "nothing to do" is how the useful lines get lost. That path also skips the
// directory prune, deliberately: nothing was deleted, so nothing new can have been emptied, and
// two walks of the tree an hour buy nothing.
func PurgeToFreeSpace(dataFolder string, minFreeBytes uint64, minRetentionDays int) error {
	free, err := freeBytes(dataFolder)
	if err != nil {
		return err
	}
	if free >= minFreeBytes {
		return nil
	}

	baseDir := filepath.Join(dataFolder, consts.ReportsDir)
	days, segments, err := reportDays(baseDir)
	if err != nil {
		return err
	}

	// The oldest day kept regardless of pressure is minRetentionDays days back; the day before
	// it is the newest one this purge may delete.
	floor := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -minRetentionDays)

	var deletedDays, deletedFiles int
	var probeErr error
	// Whether the loop ran out of deletable days because of the floor, as opposed to running
	// out of days altogether. Only the first of those is retention holding data back, and the
	// warning below must not claim the one that did not happen.
	var stoppedAtFloor bool
	for _, day := range days {
		if !day.Before(floor) {
			stoppedAtFloor = true
			break
		}
		if n := removeDay(segments[day]); n > 0 {
			deletedDays++
			deletedFiles += n
		}
		// Re-probe after every day: the point is to stop at the target, not to delete
		// everything that is old enough.
		next, err := freeBytes(dataFolder)
		if err != nil {
			probeErr = err
			break
		}
		free = next
		if free >= minFreeBytes {
			break
		}
	}

	// A failed probe leaves free holding the reading from before these deletions, so the one
	// thing not reported here is a free-space figure: the honest answer is that it is unknown.
	if probeErr != nil {
		if deletedDays > 0 {
			log.Printf("Purged %d report day(s), %d segment(s) before the free-space probe failed",
				deletedDays, deletedFiles)
		}
		return probeErr
	}

	if deletedDays > 0 {
		log.Printf("Purged %d report day(s), %d segment(s); %d MiB now free", deletedDays, deletedFiles, free>>20)
	}
	if free < minFreeBytes {
		// Both cases mean something other than reports is filling the volume, but only one of
		// them has retention holding history back, and telling an operator to look at retention
		// when there is nothing left to purge sends them the wrong way.
		reason := "there are no report days left to delete"
		if stoppedAtFloor {
			reason = fmt.Sprintf("every report day left is within the %d-day minimum retention", minRetentionDays)
		}
		log.Printf("WARNING: %d MiB free, below the %d MiB target, and %s. Something other than "+
			"reports is filling the volume; free space by hand before ingest starts rejecting reports.",
			free>>20, minFreeBytes>>20, reason)
	}

	return pruneEmptyDirs(baseDir)
}

// reportDays groups every report segment under baseDir by its UTC day and returns the days in
// chronological order, oldest first. A missing baseDir is not an error: it is a store that has
// not collected anything yet.
func reportDays(baseDir string) ([]time.Time, map[time.Time][]string, error) {
	segments := make(map[time.Time][]string)
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
		segments[day] = append(segments[day], path)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}

	days := make([]time.Time, 0, len(segments))
	for day := range segments {
		days = append(days, day)
	}
	slices.SortFunc(days, func(a, b time.Time) int { return a.Compare(b) })
	return days, segments, nil
}

// removeDay deletes every segment of one day and reports how many went away. A file that
// cannot be removed is logged and skipped rather than aborting the purge: the run that gives
// up on the first bad file is the run that lets the disk fill.
func removeDay(paths []string) int {
	var removed int
	for _, path := range paths {
		if err := os.Remove(path); err != nil { //#nosec G122 -- path comes from a controlled directory walk under DATA_FOLDER
			log.Printf("Error deleting %s: %v", path, err) //#nosec G706 -- path comes from a controlled directory walk
			continue
		}
		removed++
	}
	return removed
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
