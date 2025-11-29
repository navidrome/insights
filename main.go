package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/robfig/cron/v3"
)

func startTasks(ctx context.Context, db *sql.DB) error {
	c := cron.New(cron.WithLocation(time.UTC))
	// Run summarize every day at midnight UTC
	_, err := c.AddFunc("0 0 * * *", summarize(ctx, db))
	if err != nil {
		return err
	}
	_, err = c.AddFunc("5 0 * * *", cleanup(ctx, db))
	if err != nil {
		return err
	}
	c.Start()
	return nil
}

func main() {
	ctx := context.Background()
	db, err := openDB("insights.db")
	if err != nil {
		log.Fatal(err)
	}

	if err := startTasks(ctx, db); err != nil {
		log.Fatal(err)
	}

	go summarize(ctx, db)()

	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)

	// Static files for charts
	r.Handle("/chartdata/*", http.StripPrefix("/chartdata/", http.FileServer(http.Dir("web/chartdata"))))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/index.html")
	})

	// Charts endpoint (no rate limiting) - legacy, renders server-side
	r.Get("/charts", chartsHandler(db))

	// Rate-limited collect endpoint
	limiter := httprate.NewRateLimiter(1, 30*time.Minute, httprate.WithKeyByIP())
	r.With(limiter.Handler).Post("/collect", handler(db))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Print("Starting Insights server on :" + port)
	server := &http.Server{
		Addr:              ":" + port,
		ReadHeaderTimeout: 3 * time.Second,
		Handler:           r,
	}
	err = server.ListenAndServe()
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
