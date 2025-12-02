package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/navidrome/insights/consts"
	"github.com/navidrome/insights/db"
	"github.com/robfig/cron/v3"
)

func startTasks(ctx context.Context, dbConn *sql.DB) error {
	c := cron.New(cron.WithLocation(time.UTC))
	// Run summarize every 2 hours
	_, err := c.AddFunc(consts.CronSummarize, summarize(ctx, dbConn))
	if err != nil {
		return err
	}
	// Generate charts JSON once a day at 00:05 UTC
	_, err = c.AddFunc(consts.CronGenerateChart, generateCharts(ctx))
	if err != nil {
		return err
	}
	_, err = c.AddFunc(consts.CronCleanup, cleanup(ctx, dbConn))
	if err != nil {
		return err
	}
	c.Start()
	return nil
}

func main() {
	ctx := context.Background()
	dataFolder := os.Getenv("DATA_FOLDER")
	dbConn, err := db.OpenDB(filepath.Join(dataFolder, "insights.db"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Connected to database at %s", filepath.Join(dataFolder, "insights.db"))

	if err := startTasks(ctx, dbConn); err != nil {
		log.Fatal(err)
	}

	go summarize(ctx, dbConn)()
	go generateCharts(ctx)()

	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)

	// Dev-only routes (static files and charts endpoint)
	registerDevRoutes(r)

	// API endpoint to serve charts.json (protected by API_KEY if set)
	r.With(apiKeyMiddleware).Get("/api/charts", chartsJSONHandler())

	// Rate-limited collect endpoint
	limiter := httprate.NewRateLimiter(consts.RateLimitRequests, consts.RateLimitWindow, httprate.WithKeyByIP())
	r.With(limiter.Handler).Post("/collect", handler(dbConn))

	port := os.Getenv("PORT")
	if port == "" {
		port = consts.DefaultPort
	}

	log.Print("Starting Insights server on :" + port)
	server := &http.Server{
		Addr:              ":" + port,
		ReadHeaderTimeout: consts.ReadHeaderTimeout,
		Handler:           r,
	}
	err = server.ListenAndServe()
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
