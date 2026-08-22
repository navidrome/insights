// Command process runs the scheduled jobs (summarize, chart generation, purge) and serves the
// charts.json artifact it produces. It never writes report files, so it can be restarted
// without interrupting ingestion.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/navidrome/insights/consts"
	"github.com/robfig/cron/v3"
)

// shutdownTimeout bounds how long in-flight HTTP requests have to finish once a signal arrives.
//
// jobDrainTimeout bounds the wait that follows it, for a background job that was already
// running when the signal came. That wait is the reason this handler exists at all: the purge
// hides a day by renaming its segments aside and only then unlinks them, so a process killed
// between the two leaves exactly the half-hidden day the two-pass purge exists to prevent —
// HasDay still reports the day, a later backfill rewrites its summary from the segments that
// survived, and the next purge unlinks the hidden half for good. A purge pass is a bounded run
// of file renames and unlinks, so this budget is generous rather than tight.
//
// The startup pass in main is deliberately not waited on: summarize and chart export both end
// in an atomic write, so a kill there costs a temp file, not a torn one.
//
// Both are variables, not constants, so the specs can drive the deadline path without spending
// real time per run.
var (
	shutdownTimeout = 10 * time.Second
	jobDrainTimeout = 30 * time.Second
)

func main() {
	once := flag.Bool("once", false, "Run summarize and chart generation once, then exit")
	days := flag.Int("days", consts.SummarizeLookbackDays, "Number of past days to summarize")
	flag.Parse()

	if err := checkDays(*days); err != nil {
		log.Fatal(err)
	}

	dataFolder := os.Getenv("DATA_FOLDER")

	if *once {
		summarize(dataFolder, *days)()
		generateCharts()()
		return
	}

	// Shut down cleanly so a job midway through rewriting the store is not cut in half. See
	// jobDrainTimeout for what that costs when it happens.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	scheduler, err := startTasks(dataFolder, *days)
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		summarize(dataFolder, *days)()
		generateCharts()()
	}()

	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)

	r.Get("/healthz", healthzHandler())
	r.With(apiKeyMiddleware).Get("/api/charts", chartsJSONHandler())

	// Dev-only routes (static files and server-rendered charts)
	registerDevRoutes(r)

	port := os.Getenv("PORT")
	if port == "" {
		port = consts.DefaultPort
	}

	log.Print("Starting Insights process worker on :" + port) //#nosec G706 -- port is from controlled env var or constant
	server := &http.Server{
		Addr:              ":" + port,
		ReadHeaderTimeout: consts.ReadHeaderTimeout,
		Handler:           r,
	}

	serveErr := serve(ctx, server)
	if serveErr != nil {
		log.Printf("ListenAndServe: %s", serveErr)
	}

	// Only now stop the scheduler. Stop unschedules future runs and leaves the jobs already
	// running alone; the context it returns is done once the last of them has returned.
	if !waitFor(scheduler.Stop().Done(), jobDrainTimeout) {
		log.Printf("Background jobs still running %s after the stop signal; exiting anyway, so "+
			"a purge may be left half-applied", jobDrainTimeout)
	}

	if serveErr != nil {
		// Exit non-zero so a port already in use on redeploy is visible in `docker compose ps`
		// and in the exit status, rather than looking like a clean stop.
		os.Exit(1)
	}
	log.Print("Process worker stopped")
}

// serve runs server until ctx is cancelled, then gives in-flight requests shutdownTimeout to
// finish. It returns nil for the clean path, so only a real failure reaches the exit status.
//
// A ListenAndServe failure returns straight away rather than waiting on the signal: main still
// has a scheduler to drain, and a worker that cannot bind must not sit there until somebody
// stops it by hand.
func serve(ctx context.Context, server *http.Server) error {
	done := make(chan struct{})
	go func() { //#nosec G118 -- the shutdown deadline must not derive from ctx: ctx is already cancelled here, so a derived context would expire immediately and abort the drain
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("Error shutting down server, closing connections: %s", err)
			_ = server.Close()
		}
	}()

	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		// The listener closed because Shutdown started. Wait for it to finish.
		<-done
		return nil
	}
	return err
}

// waitFor reports whether c closed before timeout expired.
func waitFor(c <-chan struct{}, timeout time.Duration) bool {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-c:
		return true
	case <-t.C:
		return false
	}
}

// checkDays rejects a lookback that summarizes nothing. Without it, -days 0 or a negative
// value leaves the every-2h job running as a no-op forever: it still logs "Summarizing data",
// /healthz stays green, and the summaries silently stop being updated.
//
// It deliberately has no upper bound: see checkScheduledDays.
func checkDays(days int) error {
	if days < 1 {
		return fmt.Errorf("-days must be at least 1, got %d", days)
	}
	return nil
}

// checkScheduledDays bounds the lookback against the purge floor. This is the runtime half of
// the invariant consts asserts at compile time between SummarizeLookbackDays and
// MinRetentionDays: -days overrides the former, so without this the floor can be outrun without
// touching a constant.
//
// It applies to the scheduled mode only. There the summarize job runs continuously alongside
// the hourly purge, so a lookback reaching past the floor means the purge can delete a day out
// from under the window that is still being re-read. A -once run does meet the purge — a
// backfill reaching months back can land on the very day the hourly purge is deleting — but
// SummarizeData is what handles that, not this bound: it captures the day's segment paths
// before reading them and refuses to save if any of them disappeared underneath, so a day the
// purge took is skipped rather than rewritten from whatever part of it was still readable.
// Bounding -once as well would cost the backfill outright — retention
// is driven by free space now, so the store routinely holds months of days that reaching back
// into is the entire point of the flag.
func checkScheduledDays(days int) error {
	if days >= consts.MinRetentionDays {
		return fmt.Errorf("scheduled -days must be below the %d-day purge floor (consts.MinRetentionDays), got %d; "+
			"-once has no such limit, so use it for a wider backfill", consts.MinRetentionDays, days)
	}
	return nil
}

// startTasks schedules the background jobs and starts running them. It returns the running
// scheduler so main can stop it and wait out whatever job was in flight at the time.
func startTasks(dataFolder string, days int) (*cron.Cron, error) {
	if err := checkScheduledDays(days); err != nil {
		return nil, err
	}
	c, err := newScheduler(dataFolder, days)
	if err != nil {
		return nil, err
	}
	c.Start()
	return c, nil
}

// newScheduler registers every background job on a UTC cron, without starting it.
func newScheduler(dataFolder string, days int) (*cron.Cron, error) {
	c := cron.New(cron.WithLocation(time.UTC))
	if _, err := c.AddFunc(consts.CronSummarize, summarize(dataFolder, days)); err != nil {
		return nil, err
	}
	if _, err := c.AddFunc(consts.CronGenerateChart, generateCharts()); err != nil {
		return nil, err
	}
	if _, err := c.AddFunc(consts.CronCleanup, cleanup(dataFolder)); err != nil {
		return nil, err
	}
	return c, nil
}
