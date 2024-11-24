package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
)

func dataCleaner(db *sql.DB) {
	for {
		log.Print("Cleaning old data")
		_ = purgeOldEntries(db)
		time.Sleep(24 * time.Hour)
	}
}

func main() {
	db, err := openDB("insights.db")
	if err != nil {
		log.Fatal(err)
	}

	go dataCleaner(db)

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
