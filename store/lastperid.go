package store

import (
	"encoding/json"
	"errors"
	"iter"
	"log"
	"slices"
	"time"

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
func LastPerID(dataFolder string, date time.Time) (iter.Seq[insights.Data], func() error, error) {
	positions, pass1, err := winningPositions(dataFolder, date)
	if err != nil {
		return nil, nil, err
	}

	var pass2 error
	seq := func(yield func(insights.Data) bool) {
		pass2 = nil
		if len(positions) == 0 {
			return
		}
		lines, incomplete, err := readLines(dataFolder, date)
		if err != nil {
			pass2 = err
			log.Printf("Error reopening report file for second pass: %s", err)
			return
		}
		// Deferred, so a consumer that stops early still gets the verdict on what was read.
		defer func() { pass2 = errors.Join(pass2, incomplete()) }()

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
	}
	return seq, func() error { return errors.Join(pass1, pass2) }, nil
}

// winningPositions is pass 1: the sorted positions of every instance's newest record. The map
// lives and dies here, ~20 MB for a production day against ~220 MB for the decoded records.
//
// incomplete carries a segment this pass could not read in full, which matters as much as it
// does in pass 2: a segment missed here drops its instances from the winner set entirely.
func winningPositions(dataFolder string, date time.Time) (positions []int32, incomplete error, err error) {
	lines, readIncomplete, err := readLines(dataFolder, date)
	if err != nil {
		return nil, nil, err
	}

	best := make(map[string]winner)
	var pos int32
	for line := range lines {
		var h recordHeader
		// Pass 2 counts lines, not records, so an undecodable line still occupies a position.
		if err := json.Unmarshal(line, &h); err != nil || h.Data.InsightsID == "" {
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

	positions = make([]int32, 0, len(best))
	for _, w := range best {
		positions = append(positions, w.pos)
	}
	slices.Sort(positions)
	return positions, readIncomplete(), nil
}
