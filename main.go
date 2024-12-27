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
	c := cron.New()
	_, err := c.AddFunc("1 * * * *", summarize(ctx, db))
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

	summarize(ctx, db)()

	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	limiter := httprate.NewRateLimiter(1, 30*time.Minute, httprate.WithKeyByIP())
	r.Use(limiter.Handler)
	r.Post("/collect", handler(db))

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
