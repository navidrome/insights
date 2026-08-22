package store

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/navidrome/insights/consts"
)

// reportFileRegex matches segment files like "reports-2026-08-03.001.ndjson.gz". The ".gz" is
// optional so a hand-decompressed segment still expires with its day.
var reportFileRegex = regexp.MustCompile(`^reports-(\d{4}-\d{2}-\d{2})\.(\d{3})\.ndjson(\.gz)?$`)

// purgingPrefix hides a segment from every reader. Hiding a whole day before unlinking any of
// it keeps a failed deletion from leaving half a day on disk and still summarizable.
const purgingPrefix = ".purging-"

// DayPurgePending reports whether any segment of date's UTC day is hidden by a purge that has
// not finished.
//
// Such a day is part visible at best, and the part that is visible reads as a complete, smaller
// day: nothing about it looks wrong to a reader. Every way a day ends up in that state is
// self-healing, since resumeInterrupted finishes it on the next hourly run, but the window is
// an hour wide and a backfill inside it would publish the smaller number.
func DayPurgePending(dataFolder string, date time.Time) (bool, error) {
	dir := dayDir(dataFolder, date)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("listing %s: %w", dir, err)
	}

	prefix := segmentPrefix(date)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		rest, ok := strings.CutPrefix(e.Name(), purgingPrefix)
		if !ok {
			continue
		}
		// Matched against this day's prefix, so a neighbouring day being purged does not
		// suspend summarization of one that is whole.
		if _, isSegment := segmentIndex(rest, prefix); isSegment {
			return true, nil
		}
	}
	return false, nil
}

// freeBytes is a variable so tests can simulate disk pressure.
var freeBytes = statfsFreeBytes

// removeFile is a variable so tests can fail one unlink while its siblings succeed.
var removeFile = os.Remove

// statfsFreeBytes reads the free space of the volume containing path. Bavail, not Bfree: Bfree
// counts root-reserved blocks this process cannot write to.
func statfsFreeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	return st.Bavail * blockSize(&st), nil
}

// PurgeToFreeSpace deletes whole report days, oldest first, until the volume holding dataFolder
// has minFreeBytes free. It keeps every day younger than minRetentionDays, warns when it stops
// short, and is silent when there is room and nothing to sweep. dataFolder is defaulted because
// statfs, unlike filepath.Join, resolves "" to ENOENT.
func PurgeToFreeSpace(dataFolder string, minFreeBytes uint64, minRetentionDays int) error {
	if dataFolder == "" {
		dataFolder = "."
	}

	baseDir := filepath.Join(dataFolder, consts.ReportsDir)
	days, segments, abandoned, err := reportDays(baseDir)
	if err != nil {
		return err
	}

	// Repair before retention, and before the free-space check: a day an earlier purge left
	// half finished must not wait for the disk to fill up before it is made whole.
	resumed, resumedFiles, err := resumeInterrupted(abandoned, segments)
	if err != nil {
		return err
	}
	days = slices.DeleteFunc(days, func(d time.Time) bool { return resumed[d] })

	// Probed after the repair, so the day loop below never deletes a day to reach a target the
	// repair already met.
	free, err := freeBytes(dataFolder)
	if err != nil {
		return err
	}
	if free >= minFreeBytes {
		if resumedFiles > 0 {
			// Files went away, so a day directory may now be empty.
			return pruneEmptyDirs(baseDir)
		}
		return nil
	}

	// The oldest day kept regardless of pressure.
	floor := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -minRetentionDays)

	var deletedDays, deletedFiles int
	var probeErr, removeErr error
	// Set only when retention held days back, not when there were no days left to delete.
	var stoppedAtFloor bool
	for _, day := range days {
		if !day.Before(floor) {
			stoppedAtFloor = true
			break
		}
		n, err := removeDay(segments[day])
		deletedFiles += n
		// Only a day that went away entirely counts. A partly deleted day is still on disk.
		if err == nil {
			deletedDays++
		}
		if err != nil {
			// Stop here: the volume is refusing deletions, so every later day fails the same
			// way and ends up in a state an operator has to be told about.
			log.Printf("WARNING: purging report day %s: %v", day.Format(consts.DateFormat), err)
			removeErr = err
			break
		}
		// Stop at the target, not at everything old enough.
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

	if removeErr != nil {
		// Segments, not days: the failed day contributed segments without contributing a day.
		if deletedFiles > 0 {
			log.Printf("Purged %d report day(s), %d segment(s) before the deletion failed",
				deletedDays, deletedFiles)
		}
		return removeErr
	}

	// free is stale after a failed probe, so no free-space figure is reported.
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
		// Retention holding days back and nothing left to purge send an operator to different
		// places.
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

// reportDays groups report segments under baseDir by UTC day, oldest first, and also returns
// the segments an earlier purge hid but failed to unlink, keyed by the same day. A missing
// baseDir is not an error.
func reportDays(baseDir string) ([]time.Time, map[time.Time][]string, map[time.Time][]string, error) {
	segments := make(map[time.Time][]string)
	abandoned := make(map[time.Time][]string)
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
		// A hidden segment is still a report file name underneath, so it names its own day.
		if rest, ok := strings.CutPrefix(d.Name(), purgingPrefix); ok {
			if m := reportFileRegex.FindStringSubmatch(rest); m != nil {
				if day, parseErr := time.ParseInLocation(consts.DateFormat, m[1], time.UTC); parseErr == nil {
					abandoned[day] = append(abandoned[day], path)
				}
			}
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
		return nil, nil, nil, err
	}

	days := make([]time.Time, 0, len(segments))
	for day := range segments {
		days = append(days, day)
	}
	slices.SortFunc(days, func(a, b time.Time) int { return a.Compare(b) })
	return days, segments, abandoned, nil
}

// resumeInterrupted finishes the days an earlier purge started and did not complete.
//
// A hidden segment is that purge's record of intent, so the rest of its day goes with it,
// whatever free space looks like. Unlinking only the hidden half would leave the visible half
// on disk, where HasDay reports it and a backfill summarizes it as if it were the whole day.
//
// It returns the days it finished, so the caller can drop them from its own list, and how many
// files it deleted.
func resumeInterrupted(abandoned, segments map[time.Time][]string) (map[time.Time]bool, int, error) {
	interrupted := make([]time.Time, 0, len(abandoned))
	for day := range abandoned {
		interrupted = append(interrupted, day)
	}
	slices.SortFunc(interrupted, func(a, b time.Time) int { return a.Compare(b) })

	done := make(map[time.Time]bool, len(interrupted))
	var files int
	for _, day := range interrupted {
		// The visible siblings first. Until the day is gone the hidden segments are the only
		// record that it was being purged, so unlinking them ahead of the siblings would throw
		// away the one thing a later run needs to finish the job.
		if rest := segments[day]; len(rest) > 0 {
			n, err := removeDay(rest)
			files += n
			if err != nil {
				return done, files, fmt.Errorf("finishing the interrupted purge of %s: %w",
					day.Format(consts.DateFormat), err)
			}
		}
		for _, path := range abandoned[day] {
			if err := removeFile(path); err != nil { //#nosec G122 -- path comes from a controlled directory walk under DATA_FOLDER
				log.Printf("Error deleting segment %s abandoned by an earlier purge: %v", path, err) //#nosec G706 -- path comes from a controlled directory walk
				continue
			}
			files++
			log.Printf("Deleted segment %s abandoned by an earlier purge", path) //#nosec G706 -- path comes from a controlled directory walk
		}
		done[day] = true
		log.Printf("Finished purging report day %s, left incomplete by an earlier run",
			day.Format(consts.DateFormat))
	}
	return done, files, nil
}

// removeDay hides every segment of a day before unlinking any of them, so no failure leaves a
// day that is partly on disk and still reported by HasDay. A rename failure rolls back; an
// unlink failure leaves hidden bytes for a later sweep. A kill between renames still leaves a
// partial day.
func removeDay(paths []string) (int, error) {
	hidden := make([]string, 0, len(paths))
	for i, path := range paths {
		h := filepath.Join(filepath.Dir(path), purgingPrefix+filepath.Base(path))
		if err := os.Rename(path, h); err != nil {
			unhide(hidden, paths[:i])
			return 0, fmt.Errorf("hiding segment %s: %w (the day was left intact)", path, err)
		}
		hidden = append(hidden, h)
	}

	for i, path := range hidden {
		if err := removeFile(path); err != nil { //#nosec G122 -- path comes from a controlled directory walk under DATA_FOLDER
			return i, fmt.Errorf("deleting segment %s: %w (%d of %d segment(s) of the day are "+
				"still on disk, hidden from summarization until a later purge deletes them)",
				path, err, len(hidden)-i, len(hidden))
		}
	}
	return len(hidden), nil
}

// unhide restores the names removeDay renamed away. A failure here cannot be undone: it leaves
// the day partly visible, which is what the two passes exist to prevent.
func unhide(hidden, paths []string) {
	for i, path := range hidden {
		if err := os.Rename(path, paths[i]); err != nil {
			log.Printf("WARNING: could not restore %s after an aborted purge: %v. That day is "+
				"now partly hidden and will summarize from incomplete data if it is backfilled.",
				paths[i], err) //#nosec G706 -- path comes from a controlled directory walk
		}
	}
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

	// Deepest first, so a month directory is emptied before its year is considered.
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
