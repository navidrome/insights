// Command process runs the scheduled jobs (summarize, chart generation, purge) and serves the
// charts.json it produces. It never writes report files, so restarting it is safe.
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

// shutdownTimeout bounds in-flight requests once a signal arrives. jobDrainTimeout then waits
// out a running job: a purge killed between hiding a day's segments and unlinking them leaves
// the half-hidden day the two-pass purge exists to prevent. Nothing waits on the startup pass,
// which ends in atomic writes. Variables, so the specs can reach the deadline path.
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

	// See jobDrainTimeout for what a job cut in half costs.
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

	// Stop unschedules future runs; its context is done once the running jobs return.
	if !waitFor(scheduler.Stop().Done(), jobDrainTimeout) {
		log.Printf("Background jobs still running %s after the stop signal; exiting anyway, so "+
			"a purge may be left half-applied", jobDrainTimeout)
	}

	if serveErr != nil {
		// Non-zero so a port already in use on redeploy does not look like a clean stop.
		os.Exit(1)
	}
	log.Print("Process worker stopped")
}

// serve runs server until ctx is cancelled, then drains in-flight requests, returning nil on
// the clean path. A bind failure returns at once: main still has a scheduler to drain.
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

// checkDays rejects a lookback that summarizes nothing, which would otherwise run as a silent
// no-op forever. The upper bound is checkScheduledDays' job.
func checkDays(days int) error {
	if days < 1 {
		return fmt.Errorf("-days must be at least 1, got %d", days)
	}
	return nil
}

// checkScheduledDays keeps the scheduled lookback below the purge floor, the runtime half of
// the invariant consts asserts between SummarizeLookbackDays and MinRetentionDays. -once is
// exempt: a months-deep backfill is the point of the flag, and SummarizeData already refuses
// to save a day whose segments vanished mid-read.
func checkScheduledDays(days int) error {
	if days >= consts.MinRetentionDays {
		return fmt.Errorf("scheduled -days must be below the %d-day purge floor (consts.MinRetentionDays), got %d; "+
			"-once has no such limit, so use it for a wider backfill", consts.MinRetentionDays, days)
	}
	return nil
}

// startTasks schedules the background jobs and starts them, returning the scheduler so main
// can stop it and drain whatever job was in flight.
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
