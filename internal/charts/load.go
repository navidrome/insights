package charts

import (
	"cmp"
	"log"
	"slices"
	"time"

	"github.com/navidrome/insights/internal/consts"
	"github.com/navidrome/insights/internal/summary"
)

// getTopKeys returns the n keys with the highest values, ordered by value descending and then by
// name. The name is what makes it a total order: the pairs come out of a map, so without it two
// keys on equal counts swap between runs and change which ones make the cut at the boundary.
func getTopKeys(m map[string]uint64, n int) []string {
	type kv struct {
		Key   string
		Value uint64
	}
	pairs := make([]kv, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, kv{k, v})
	}
	slices.SortFunc(pairs, func(a, b kv) int {
		if c := cmp.Compare(b.Value, a.Value); c != 0 {
			return c
		}
		return cmp.Compare(a.Key, b.Key)
	})

	n = min(n, len(pairs))
	result := make([]string, n)
	for i := range n {
		result[i] = pairs[i].Key
	}
	return result
}

// sortVersionsByLastDay orders the selected versions by their count on the last day, breaking
// ties on the name for the same reason getTopKeys does.
func sortVersionsByLastDay(versions []string, lastDay map[string]uint64) {
	slices.SortFunc(versions, func(a, b string) int {
		if c := cmp.Compare(lastDay[b], lastDay[a]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
}

// daySeries is what the time-series charts need from a day, and nothing else. A production
// summary is about 180 KB, most of it the PlayerTypes tail; the two charts that walk every day
// read a sum of that tail and a handful of version counts. Keeping the rest is what made chart
// export track the age of the service.
type daySeries struct {
	Time          time.Time
	TotalPlayers  uint64            // sum of Summary.PlayerTypes
	Versions      map[string]uint64 // only the keys in chartInput.TopVersions
	AllVersions   uint64            // sum of every version count that day, top-N and tail alike
	OtherVersions uint64            // AllVersions minus the top-N slice: what the tail contributed
}

// chartInput is everything the charts need: a slim record per day, the selected version names,
// and the one full summary the five snapshot charts read.
type chartInput struct {
	Series      []daySeries
	TopVersions []string
	Latest      summary.Summary
	LatestTime  time.Time
}

// loadChartInput makes three passes over the summaries. Each pass reads about 28 MB of JSON and
// takes on the order of a second, against an export that runs once a day, so three passes that
// each do one thing beat two that need a ring buffer to explain.
//
// The order is forced: the version window is measured back from the last closed day, so pass 1
// has to finish before pass 2 knows what to scan.
func loadChartInput(dataFolder string) (chartInput, error) {
	return loadChartInputAt(dataFolder, time.Now())
}

// loadChartInputAt charts every day that closed before now. A day closes at 00:00 UTC, the
// boundary the store and summary files use, and until then its count only reflects the part of
// the day collected so far. A closed day is kept however low its count is: it is what really
// happened, an outage included, and a later day cannot make it any more complete. Days nobody
// reported on never get here, GetSummaries filters them out.
func loadChartInputAt(dataFolder string, now time.Time) (chartInput, error) {
	// Pass 1: which day is the last one closed. GetSummaries yields oldest first.
	seq, err := summary.GetSummaries(dataFolder)
	if err != nil {
		return chartInput{}, err
	}
	today := now.UTC().Truncate(24 * time.Hour)
	var lastTime time.Time
	closedDays := 0
	for r := range seq {
		if !r.Time.Before(today) {
			break
		}
		lastTime = r.Time
		closedDays++
	}
	if closedDays == 0 {
		return chartInput{}, nil
	}

	// Pass 2: the top versions over the rolling window ending on that day.
	cutoff := lastTime.AddDate(0, 0, -consts.VersionSelectionDays)
	versionTotals := make(map[string]uint64)
	lastVersions := make(map[string]uint64)
	for r := range seq {
		if r.Time.After(lastTime) {
			break
		}
		if r.Time.Before(cutoff) {
			continue
		}
		for v, c := range r.Data.Versions {
			versionTotals[v] += c
		}
		if r.Time.Equal(lastTime) {
			for v, c := range r.Data.Versions {
				lastVersions[v] = c
			}
		}
	}
	top := getTopKeys(versionTotals, consts.TopVersionsCount)
	sortVersionsByLastDay(top, lastVersions)
	keep := make(map[string]bool, len(top))
	for _, v := range top {
		keep[v] = true
	}

	// Pass 3: the slim record per day, plus the one full summary. Excluded players come out
	// here, before anything reads PlayerTypes, so the daily totals and the latest snapshot
	// agree, and a new rule also cleans every day already summarized.
	excluded := loadPlayerExclusions(dataFolder)
	in := chartInput{TopVersions: top, LatestTime: lastTime}
	in.Series = make([]daySeries, 0, closedDays)
	sawLatest := false
	for r := range seq {
		if r.Time.After(lastTime) {
			break
		}
		isLatest := r.Time.Equal(lastTime)
		if isLatest {
			logExclusions(r.Data.PlayerTypes, excluded, lastTime)
		}
		dropExcludedPlayers(r.Data.PlayerTypes, excluded)
		d := daySeries{
			Time:     r.Time,
			Versions: make(map[string]uint64, len(top)),
		}
		for _, c := range r.Data.PlayerTypes {
			d.TotalPlayers += c
		}
		// r.Data.Versions is the whole map, unfiltered, for exactly as long as this loop
		// body runs: it is what the "All" and "Others" lines need, and the only place that
		// full picture exists. d.Versions below keeps only the top-N slice of it.
		for v, c := range r.Data.Versions {
			d.AllVersions += c
			if keep[v] {
				d.Versions[v] = c
			} else {
				d.OtherVersions += c
			}
		}
		in.Series = append(in.Series, d)
		if isLatest {
			in.Latest = r.Data
			sawLatest = true
		}
	}
	if !sawLatest {
		// lastTime came from pass 1's read of these same files. If the file for that one day
		// becomes unreadable, malformed, or reports zero instances by the time pass 3 re-reads
		// it, GetSummaries silently skips it (it logs and continues; see store.go), so the
		// r.Time.Equal(lastTime) branch above never runs and in.Latest stays the zero-value
		// Summary while in.Series is still non-empty (every earlier day loaded fine). The five
		// snapshot charts read in.Latest, and the len(in.Series) == 0 guard both callers use to
		// detect "no data" would not catch this, so the charts would render from an all-zero
		// summary and charts.json would publish "totalInstances": 0.
		//
		// Folding this into the same "no data" contract, rather than adding a second error path
		// callers would need to learn, is deliberate: both callers already handle an empty
		// chartInput correctly (ChartsHandler answers 404, ExportChartsJSON skips the write and
		// logs), and the condition is expected to self-heal on the next run once the file
		// finishes writing or tomorrow's day arrives, so it is not treated as fatal.
		log.Printf("Warning: latest day %s vanished between passes, treating as no data",
			lastTime.Format(consts.DateFormat))
		return chartInput{}, nil
	}
	return in, nil
}
