package main

import (
	"context"
	"errors"
	"net"
	"net/http"
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
		// checkDays gates both modes, so it must NOT apply the purge floor: -once is how a
		// backfill reaches back over months of history.
		{days: consts.MinRetentionDays},
		{days: consts.MinRetentionDays + 20},
	} {
		err := checkDays(tc.days)
		if (err != nil) != tc.wantErr {
			t.Errorf("checkDays(%d) = %v, want error: %t", tc.days, err, tc.wantErr)
		}
	}
}

// The scheduled lookback must stay inside the purge floor. Pinned from both sides: one day
// under is allowed, the floor itself is not.
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

// The bound is worth nothing unless startTasks applies it. It returns before starting
// anything, so no cron outlives the test.
func TestStartTasksRejectsLookbackPastThePurgeFloor(t *testing.T) {
	_, err := startTasks(t.TempDir(), consts.MinRetentionDays)
	if err == nil {
		t.Fatal("startTasks accepted a lookback at the purge floor, want an error")
	}
	if !strings.Contains(err.Error(), "purge floor") {
		t.Errorf("startTasks error = %q, want it to name the purge floor", err)
	}
}

// TestSummarizeSerializesOverlappingRuns pins the mutual exclusion main relies on: a restart at
// the top of an even hour has the startup run and the cron run writing the same summary file.
// SkipIfStillRunning would not cover it, since the startup run is not a cron job.
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

// A missing job fails silently: nothing errors, the work just never runs.
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

// TestServeReturnsAfterShutdownOnSignal pins the clean path: serve returns nil, so main drains
// the scheduler instead of exiting non-zero on a stop that was asked for.
func TestServeReturnsAfterShutdownOnSignal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	server := &http.Server{Addr: addr, ReadHeaderTimeout: time.Second, Handler: http.NewServeMux()}

	done := make(chan error, 1)
	go func() { done <- serve(ctx, server) }()

	// Wait until it is accepting, so the cancel exercises Shutdown rather than racing
	// ListenAndServe.
	waitForListener(t, addr)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned %v on the signal path, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return after the context was cancelled")
	}
}

// TestServeReturnsListenError pins the failure path: a worker that cannot bind must not wait
// for a signal that is not coming.
func TestServeReturnsListenError(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()

	server := &http.Server{Addr: taken.Addr().String(), ReadHeaderTimeout: time.Second, Handler: http.NewServeMux()}

	done := make(chan error, 1)
	go func() { done <- serve(context.Background(), server) }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("serve returned nil for a port already in use, want the bind error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve blocked on a port already in use instead of returning the bind error")
	}
}

// TestWaitForReportsTimeout covers the branch that is otherwise only reachable by stalling a
// real job for jobDrainTimeout.
func TestWaitForReportsTimeout(t *testing.T) {
	closed := make(chan struct{})
	close(closed)
	if !waitFor(closed, time.Second) {
		t.Error("waitFor reported a timeout on an already-closed channel")
	}
	if waitFor(make(chan struct{}), 10*time.Millisecond) {
		t.Error("waitFor reported success on a channel that never closes")
	}
}

func waitForListener(t *testing.T, addr string) {
	t.Helper()
	for range 100 {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never started listening on %s", addr)
}

// -once is what a backfill runs, and it turns this error into a non-zero exit. Without it
// `make summarize` reports a clean run over days that never got a summary.
func TestSummarizeReportsFailuresAfterTryingEveryDay(t *testing.T) {
	prev := summarizeDay
	t.Cleanup(func() { summarizeDay = prev })

	boom := errors.New("boom")
	var attempted int
	summarizeDay = func(string, time.Time) error {
		attempted++
		if attempted == 2 {
			return boom
		}
		return nil
	}

	err := summarize(t.TempDir(), 3)()

	if !errors.Is(err, boom) {
		t.Errorf("summarize error = %v, want it to wrap %v", err, boom)
	}
	if attempted != 3 {
		t.Errorf("attempted %d days, want all 3 despite the failure on the second", attempted)
	}
}
