package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
)

func main() {

	db, err := OpenDB("insights.db")
	if err != nil {
		log.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	limiter := httprate.NewRateLimiter(1, 30*time.Minute, httprate.WithKeyByIP())
	r.Use(limiter.Handler)
	r.Use(middleware.RealIP)
	r.Post("/collect", handler(db))

	port := os.Getenv("PORT")
	if port == "" {
		port = "4578"
	}
	log.Print("Starting Insights server on :" + port)
	http.ListenAndServe(":"+port, r)
}
