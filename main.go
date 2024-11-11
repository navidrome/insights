package main

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	r := chi.NewRouter()
	r.Use(middleware.Logger)

	db, err := OpenDB("insights.db")
	if err != nil {
		log.Fatal(err)
	}
	r.Use(middleware.Logger)
	r.Post("/collect", handler(db))

	log.Print("Starting Insights server on :8080")
	http.ListenAndServe(":8080", r)
}
