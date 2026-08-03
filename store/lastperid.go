package store

import (
	"encoding/json"
	"iter"
	"log"
	"slices"
	"time"

	"github.com/navidrome/navidrome/core/metrics/insights"
)

// recordHeader decodes only the two fields needed to pick a winner. Everything else in the
// line is discarded, which keeps pass 1 cheap.
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

// LastPerID yields the newest record for each instance in a day's report file, exactly once
// per instance. It replaces the SQL `MAX(time) GROUP BY id` query.
//
// The winner is the record with the greatest envelope time, ties broken by later position.
// Position alone would be wrong: a backwards clock step or a restart must not change which
// record wins.
//
// It runs in two passes because the newest record for an instance may be the last line of the
// day, so nothing can be emitted until the whole day has been read. Pass 1 keeps only a
// (position, timestamp) per instance and collapses that to a sorted position list, which is
// two orders of magnitude smaller than holding the decoded records; pass 2 re-reads and fully
// decodes only the winners, one at a time. Buffering the records instead would not fit in the
// memory this runs in.
//
// Pass 2 re-reads a file that ingest may still be appending to. Positions are stable because
// the file is append-only, so records added between the passes fall outside the winner set and
// are picked up by the next run.
func LastPerID(dataFolder string, date time.Time) (iter.Seq[insights.Data], error) {
	positions, err := winningPositions(dataFolder, date)
	if err != nil {
		return nil, err
	}

	return func(yield func(insights.Data) bool) {
		if len(positions) == 0 {
			return
		}
		lines, err := readLines(dataFolder, date)
		if err != nil {
			log.Printf("Error reopening report file for second pass: %s", err)
			return
		}

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
				continue
			}
			if !yield(rec.Data) {
				return
			}
		}
	}, nil
}

// winningPositions is pass 1: the sorted line positions of the newest record of every
// instance, one per instance.
//
// The map of instance IDs lives and dies inside this function so that it is unreachable, and
// so collectible, before pass 2 allocates anything. It holds 16 bytes plus an ID per instance
// rather than a decoded record — roughly 20 MB for a production day against the ~220 MB the
// records themselves would need, which is more than the machine has.
func winningPositions(dataFolder string, date time.Time) ([]int32, error) {
	lines, err := readLines(dataFolder, date)
	if err != nil {
		return nil, err
	}

	best := make(map[string]winner)
	var pos int32
	for line := range lines {
		var h recordHeader
		// A line that does not decode, or carries no instance ID, still occupies a position:
		// pass 2 counts lines, not records, so the count must not skip it.
		if err := json.Unmarshal(line, &h); err != nil || h.Data.InsightsID == "" {
			pos++
			continue
		}
		// A line with no "time" field leaves the zero time.Time, whose UnixNano is a large
		// negative number. Such records therefore tie with each other — the later position
		// wins — and lose to any record that carries a real timestamp.
		ts := h.Time.UnixNano()
		// Ties go to the later position. The comparison is written out rather than folded
		// away: pos only ever increases, so the clause is always true when reached, but it is
		// the only place the rule for two records sharing a second is stated.
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
	return positions, nil
}
