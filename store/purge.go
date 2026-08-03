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
// optional so a segment decompressed by hand expires with the rest of its day. Anything else
// sharing the tree — the ingest lock file above all — does not match and is left alone.
var reportFileRegex = regexp.MustCompile(`^reports-(\d{4}-\d{2}-\d{2})\.(\d{3})\.ndjson(\.gz)?$`)

// purgingPrefix marks a segment a purge has committed to deleting. A file wearing it matches
// neither reportFileRegex nor the reader's segment pattern, so it is invisible to HasDay, to
// summarization and to this purge's own day enumeration: hiding a day's segments before
// unlinking any of them is what keeps a failed deletion from leaving a day that is half on
// disk and still summarizable.
const purgingPrefix = ".purging-"

// freeBytes reports the space available on the volume holding path. It is a package-level
// variable so tests can drive the purge with simulated disk pressure: filling a real volume to
// a threshold is neither deterministic nor safe on a developer machine.
var freeBytes = statfsFreeBytes

// removeFile unlinks one segment. It is a package-level variable for the same reason as
// freeBytes: a file that cannot be unlinked while its siblings can is not something a test can
// arrange portably, and the behaviour on exactly that failure is what removeDay exists for.
var removeFile = os.Remove

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
//
// An empty dataFolder is the documented "defaults to the current directory" case that ingest
// and summarization both handle by letting filepath.Join drop it. statfs does not: it resolves
// "" to ENOENT, which would make every hourly run fail on the probe and retention never run at
// all. "." names the same directory the joins below resolve against.
func PurgeToFreeSpace(dataFolder string, minFreeBytes uint64, minRetentionDays int) error {
	if dataFolder == "" {
		dataFolder = "."
	}

	free, err := freeBytes(dataFolder)
	if err != nil {
		return err
	}
	if free >= minFreeBytes {
		return nil
	}

	baseDir := filepath.Join(dataFolder, consts.ReportsDir)
	days, segments, abandoned, err := reportDays(baseDir)
	if err != nil {
		return err
	}

	// Segments an earlier purge hid but could not unlink. Nothing reads them, so retrying is
	// the only thing left to do with them, and skipping the retry would leak their space for
	// good on a store whose retention is driven by free space. A failure here is logged and
	// not fatal: it is space that is already lost either way.
	for _, path := range abandoned {
		if err := removeFile(path); err != nil { //#nosec G122 -- path comes from a controlled directory walk under DATA_FOLDER
			log.Printf("Error deleting segment %s abandoned by an earlier purge: %v", path, err) //#nosec G706 -- path comes from a controlled directory walk
			continue
		}
		log.Printf("Deleted segment %s abandoned by an earlier purge", path) //#nosec G706 -- path comes from a controlled directory walk
	}

	// The oldest day kept regardless of pressure is minRetentionDays days back; the day before
	// it is the newest one this purge may delete.
	floor := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -minRetentionDays)

	var deletedDays, deletedFiles int
	var probeErr, removeErr error
	// Whether the loop ran out of deletable days because of the floor, as opposed to running
	// out of days altogether. Only the first of those is retention holding data back, and the
	// warning below must not claim the one that did not happen.
	var stoppedAtFloor bool
	for _, day := range days {
		if !day.Before(floor) {
			stoppedAtFloor = true
			break
		}
		n, err := removeDay(segments[day])
		if n > 0 {
			deletedDays++
			deletedFiles += n
		}
		if err != nil {
			// Loud, and by name: whatever state that day ended in, an operator has to be able
			// to find it. The purge stops here rather than moving on to the next day — the
			// volume is refusing deletions, so the days after this one would fail the same way,
			// and every one of them attempted is another day put in a state to explain.
			log.Printf("WARNING: purging report day %s: %v", day.Format(consts.DateFormat), err)
			removeErr = err
			break
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

	// A deletion that failed is reported, never worked around. The warning at the bottom of
	// this function would otherwise reach an operator whose whole report tree is still on disk
	// and tell them there are no report days left to delete.
	if removeErr != nil {
		if deletedDays > 0 {
			log.Printf("Purged %d report day(s), %d segment(s) before the deletion failed",
				deletedDays, deletedFiles)
		}
		return removeErr
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
//
// It also returns the segments an earlier purge hid but failed to unlink. They are invisible to
// every reader by design, which also means nothing else will ever notice they are still taking
// up space.
func reportDays(baseDir string) ([]time.Time, map[time.Time][]string, []string, error) {
	segments := make(map[time.Time][]string)
	var abandoned []string
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
		// Only a name this package itself produced: a hidden segment is still a report file
		// name underneath, so nothing else that happens to start with a dot is touched.
		if rest, ok := strings.CutPrefix(d.Name(), purgingPrefix); ok {
			if reportFileRegex.MatchString(rest) {
				abandoned = append(abandoned, path)
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

// removeDay deletes every segment of one day and reports how many went away.
//
// It does it in two passes — rename every segment out of the readable name space, then unlink
// them — because deleting them one at a time makes a failure halfway through leave a day that
// is partly on disk and still reported by HasDay. A `process -once -days N` backfill pointed at
// such a day rewrites its summary from the surviving reports and publishes the result: silent
// corruption of the product output, not just of the raw store. Free-space retention keeps
// months of days a backfill can be aimed at, so that is not a theoretical window.
//
// The two passes give the day one of two honest states instead:
//   - a rename fails: nothing has been unlinked, the names already taken are put back, and the
//     day is left exactly as it was — whole, visible, correctly summarizable.
//   - an unlink fails: every segment is already hidden, so the day is gone as far as every
//     reader is concerned, and the bytes left behind are picked up by a later purge.
//
// Either way the error is returned, and the caller stops the purge on it.
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

// unhide puts back the names removeDay renamed away, so an aborted deletion leaves the day
// whole. A failure here is the one case that cannot be undone: it leaves the day partly
// visible, which is exactly the state the two passes exist to prevent, so it is said out loud.
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
