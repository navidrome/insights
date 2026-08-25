package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log"
	"slices"
	"time"

	"github.com/navidrome/insights/internal/consts"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

// recordHeader decodes only the two fields needed to pick a winner, which keeps pass 1 cheap.
type recordHeader struct {
	Time time.Time `json:"time"`
	Data struct {
		InsightsID string `json:"id"`
	} `json:"data"`
}

// winner is the position and timestamp of the newest record seen so far for an instance.
type winner struct {
	pos int32
	ts  int64
}

// LastPerID yields the newest record per instance, ties broken by later position. Two passes,
// because the winner may be the last line and the decoded records do not fit in memory: pass 1
// keeps a (position, timestamp) per instance, pass 2 decodes only the winners.
// LastPerID takes one snapshot of the day and reads it twice. Pass 2 finds its winners by
// position in the concatenated day, so a segment that leaves the listing between the passes
// would shift every later position onto a different record rather than merely removing its own.
func LastPerID(dataFolder string, date time.Time) (iter.Seq[insights.Data], func() error, error) {
	paths, err := DaySegmentPaths(dataFolder, date)
	if err != nil {
		return nil, nil, err
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("no report segment for %s", date.UTC().Format(consts.DateFormat))
	}
	return LastPerIDFrom(paths)
}

// LastPerIDFrom is LastPerID over a snapshot the caller already holds.
//
// Callers that also compare the day's segments before and after the read must use this: a
// snapshot of their own plus a second one taken in here is two listings again, and a segment
// hidden between them is invisible to both the read and the comparison.
func LastPerIDFrom(paths []string) (iter.Seq[insights.Data], func() error, error) {
	positions, pass1 := winningPositions(paths)

	var pass2 error
	seq := func(yield func(insights.Data) bool) {
		pass2 = nil
		if len(positions) == 0 {
			return
		}
		lines, incomplete := readLinesFrom(paths)
		var malformed int
		// Deferred, so a consumer that stops early still gets the verdict on what was read.
		defer func() {
			pass2 = errors.Join(pass2, incomplete(), decodeFailures(malformed))
		}()

		next := 0
		var pos int32
		for line := range lines {
			if next >= len(positions) {
				return
			}
			if pos != positions[next] {
				pos++
				continue
			}
			next++
			pos++

			var rec Record
			if err := json.Unmarshal(line, &rec); err != nil {
				log.Printf("Skipping malformed record: %s", err)
				malformed++
				continue
			}
			if !yield(rec.Data) {
				return
			}
		}
	}
	return seq, func() error { return errors.Join(pass1, pass2) }, nil
}

// winningPositions is pass 1: the sorted positions of every instance's newest record. The map
// lives and dies here, ~20 MB for a production day against ~220 MB for the decoded records.
//
// The returned error carries what this pass could not read: a segment missed here drops its
// instances from the winner set entirely, exactly as in pass 2.
func winningPositions(paths []string) ([]int32, error) {
	lines, readIncomplete := readLinesFrom(paths)

	best := make(map[string]winner)
	var pos int32
	var malformed int
	for line := range lines {
		var h recordHeader
		// Pass 2 counts lines, not records, so a line skipped here still occupies a position.
		if err := json.Unmarshal(line, &h); err != nil {
			malformed++
			pos++
			continue
		}
		// Not damage: ingest stores a report without an instance id as sent. Such a line can
		// never win anything, but it is still a line.
		if h.Data.InsightsID == "" {
			pos++
			continue
		}
		// A missing "time" gives the zero time.Time, whose UnixNano is large and negative, so
		// such records lose to any real timestamp.
		ts := h.Time.UnixNano()
		// Ties go to the later position. Always true when reached, but it states the rule.
		if cur, ok := best[h.Data.InsightsID]; !ok || ts > cur.ts || (ts == cur.ts && pos > cur.pos) {
			best[h.Data.InsightsID] = winner{pos: pos, ts: ts}
		}
		pos++
	}

	positions := make([]int32, 0, len(best))
	for _, w := range best {
		positions = append(positions, w.pos)
	}
	slices.Sort(positions)
	return positions, errors.Join(readIncomplete(), decodeFailures(malformed))
}
