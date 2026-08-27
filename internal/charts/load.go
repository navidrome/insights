package charts

import (
	"time"

	"github.com/navidrome/insights/internal/consts"
	"github.com/navidrome/insights/internal/summary"
)

// daySeries is what the time-series charts need from a day, and nothing else. A production
// summary is about 180 KB, most of it the PlayerTypes tail; the two charts that walk every day
// read a sum of that tail and a handful of version counts. Keeping the rest is what made chart
// export track the age of the service.
type daySeries struct {
	Time          time.Time
	NumInstances  int64
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
// The order is forced: the version window is measured back from the last day that survives the
// incomplete-tail trim, so pass 1 has to finish before pass 2 knows what to scan.
func loadChartInput(dataFolder string) (chartInput, error) {
	// Pass 1: which day is last, once the incomplete tail is gone.
	seq, err := summary.GetSummaries(dataFolder)
	if err != nil {
		return chartInput{}, err
	}
	var scan []daySeries
	for r := range seq {
		scan = append(scan, daySeries{Time: r.Time, NumInstances: r.Data.NumInstances})
	}
	scan = trimIncomplete(scan)
	if len(scan) == 0 {
		return chartInput{}, nil
	}
	lastTime := scan[len(scan)-1].Time

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

	// Pass 3: the slim record per day, plus the one full summary.
	in := chartInput{TopVersions: top, LatestTime: lastTime}
	in.Series = make([]daySeries, 0, len(scan))
	for r := range seq {
		if r.Time.After(lastTime) {
			break
		}
		d := daySeries{
			Time:         r.Time,
			NumInstances: r.Data.NumInstances,
			Versions:     make(map[string]uint64, len(top)),
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
		if r.Time.Equal(lastTime) {
			in.Latest = r.Data
		}
	}
	return in, nil
}

// trimIncomplete drops trailing days whose instance count falls off a cliff. A day still being
// collected reads as a collapse in installations, and plotting it makes the chart look like an
// outage.
func trimIncomplete(days []daySeries) []daySeries {
	if len(days) == 0 {
		return nil
	}
	for len(days) > 1 {
		last := days[len(days)-1]
		prev := days[len(days)-2]
		if prev.NumInstances > 0 {
			if float64(last.NumInstances)/float64(prev.NumInstances) < consts.IncompleteThreshold {
				days = days[:len(days)-1]
				continue
			}
		}
		break
	}
	return days
}
