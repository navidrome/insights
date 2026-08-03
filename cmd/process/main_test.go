package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/navidrome/insights/consts"
	"github.com/robfig/cron/v3"
)

func TestCheckDays(t *testing.T) {
	for _, tc := range []struct {
		days    int
		wantErr bool
	}{
		{days: -1, wantErr: true},
		{days: 0, wantErr: true},
		{days: 1},
		{days: consts.SummarizeLookbackDays},
		// checkDays gates both modes, so it must NOT bound the lookback against the purge
		// floor: -once is how a backfill reaches back over the months of history that
		// free-space retention now keeps. Moving that bound in here would break the backfill
		// while every scheduled-path spec below still passed, so it is pinned explicitly.
		{days: consts.MinRetentionDays},
		{days: consts.MinRetentionDays + 20},
	} {
		err := checkDays(tc.days)
		if (err != nil) != tc.wantErr {
			t.Errorf("checkDays(%d) = %v, want error: %t", tc.days, err, tc.wantErr)
		}
	}
}

// The scheduled summarize job runs alongside the hourly purge, so its lookback must stay inside
// the purge floor or a day can be deleted mid-window. Pinned from both sides: one day under the
// floor is allowed, the floor itself is not.
func TestCheckScheduledDays(t *testing.T) {
	for _, tc := range []struct {
		days    int
		wantErr bool
	}{
		{days: 1},
		{days: consts.SummarizeLookbackDays},
		{days: consts.MinRetentionDays - 1},
		{days: consts.MinRetentionDays, wantErr: true},
		{days: consts.MinRetentionDays + 1, wantErr: true},
	} {
		err := checkScheduledDays(tc.days)
		if (err != nil) != tc.wantErr {
			t.Errorf("checkScheduledDays(%d) = %v, want error: %t", tc.days, err, tc.wantErr)
		}
	}
}

// The bound is worth nothing unless the scheduled entry point actually applies it. startTasks
// returns before starting anything here, so no cron outlives the test.
func TestStartTasksRejectsLookbackPastThePurgeFloor(t *testing.T) {
	err := startTasks(t.TempDir(), consts.MinRetentionDays)
	if err == nil {
		t.Fatal("startTasks accepted a lookback at the purge floor, want an error")
	}
	if !strings.Contains(err.Error(), "purge floor") {
		t.Errorf("startTasks error = %q, want it to name the purge floor", err)
	}
}

// TestSummarizeSerializesOverlappingRuns pins the mutual exclusion main relies on.
//
// main starts a summarization run in a goroutine while the cron for the same job is already
// running, so a restart at the top of an even hour has both going at once. Each one ends in a
// write of the same summary file per date, and a run takes minutes of gzip scanning on the
// production box, so overlapping runs are what tears a summary file rather than a theoretical
// race. cron.SkipIfStillRunning would not cover this: the startup run is not a cron job.
func TestSummarizeSerializesOverlappingRuns(t *testing.T) {
	original := summarizeDay
	t.Cleanup(func() { summarizeDay = original })

	var active, overlaps atomic.Int32
	summarizeDay = func(string, time.Time) error {
		if active.Add(1) > 1 {
			overlaps.Add(1)
		}
		time.Sleep(time.Millisecond)
		active.Add(-1)
		return nil
	}

	job := summarize(t.TempDir(), 3)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job()
		}()
	}
	wg.Wait()

	if n := overlaps.Load(); n != 0 {
		t.Fatalf("got %d overlapping summarization runs, want 0: concurrent runs write the same summary file", n)
	}
}

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
