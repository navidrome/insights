package main

import (
	"testing"
	"time"

	"github.com/navidrome/insights/consts"
	"github.com/robfig/cron/v3"
)

// The scheduled jobs are the only reason this binary exists, and a missing one fails silently:
// nothing errors, the work just never runs. Pin every schedule that must be registered.
func TestNewSchedulerRegistersAllJobs(t *testing.T) {
	c, err := newScheduler(t.TempDir(), consts.SummarizeLookbackDays)
	if err != nil {
		t.Fatalf("building scheduler: %v", err)
	}

	ref := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	got := map[time.Time]int{}
	for _, entry := range c.Entries() {
		if entry.Job == nil {
			t.Fatal("registered entry has no job")
		}
		got[entry.Schedule.Next(ref)]++
	}

	want := map[time.Time]int{}
	for _, spec := range []string{consts.CronSummarize, consts.CronGenerateChart, consts.CronCleanup} {
		sched, err := cron.ParseStandard(spec)
		if err != nil {
			t.Fatalf("parsing %q: %v", spec, err)
		}
		want[sched.Next(ref)]++
	}

	if len(got) != len(want) {
		t.Fatalf("got %d distinct schedules %v, want %d %v", len(got), got, len(want), want)
	}
	for at, n := range want {
		if got[at] != n {
			t.Fatalf("got %d jobs firing at %s, want %d (all: %v)", got[at], at, n, got)
		}
	}
}
