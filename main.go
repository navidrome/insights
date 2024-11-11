package main

import (
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
)

func main() {
	r := chi.NewRouter()
	r.Use(middleware.Logger)

	db, err := OpenDB("insights.db")
	if err != nil {
		log.Fatal(err)
	}
	r.Use(middleware.Logger)
	limiter := httprate.NewRateLimiter(1, 30*time.Minute, httprate.WithKeyByIP())
	r.Use(limiter.Handler)
	r.Post("/collect", handler(db))

	log.Print("Starting Insights server on :8080")
	http.ListenAndServe(":8080", r)
}
