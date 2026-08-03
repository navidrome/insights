// Command process runs the scheduled jobs (summarize, chart generation, purge) and serves the
// charts.json artifact it produces. It never writes report files, so it can be restarted
// without interrupting ingestion.
package main

import (
	"flag"
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

// startTasks schedules the background jobs and starts running them.
func startTasks(dataFolder string, days int) error {
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
