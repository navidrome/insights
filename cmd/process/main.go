// Command process runs the scheduled jobs (summarize, chart generation, purge) and serves the
// charts.json artifact it produces. It never writes report files, so it can be restarted
// without interrupting ingestion.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/navidrome/insights/consts"
	"github.com/robfig/cron/v3"
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

	if err := startTasks(dataFolder, *days); err != nil {
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
	if err := server.ListenAndServe(); err != nil {
		log.Fatal("ListenAndServe: ", err)
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
// from under the window that is still being re-read. A -once run has no such interaction: it
// reads what is on disk, skips any day the purge already took (summary guards on store.HasDay,
// and whole days are deleted, so a taken day is skipped rather than rewritten from part of
// itself) and then exits. Bounding -once as well would cost the backfill outright — retention
// is driven by free space now, so the store routinely holds months of days that reaching back
// into is the entire point of the flag.
func checkScheduledDays(days int) error {
	if days >= consts.MinRetentionDays {
		return fmt.Errorf("scheduled -days must be below the %d-day purge floor (consts.MinRetentionDays), got %d; "+
			"-once has no such limit, so use it for a wider backfill", consts.MinRetentionDays, days)
	}
	return nil
}

// startTasks schedules the background jobs and starts running them.
func startTasks(dataFolder string, days int) error {
	if err := checkScheduledDays(days); err != nil {
		return err
	}
	c, err := newScheduler(dataFolder, days)
	if err != nil {
		return err
	}
	c.Start()
	return nil
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
