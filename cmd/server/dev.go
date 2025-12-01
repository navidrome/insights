//go:build dev

package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/insights/charts"
)

func registerDevRoutes(r chi.Router) {
	// Static files for charts
	r.Handle("/chartdata/*", http.StripPrefix("/chartdata/", http.FileServer(http.Dir("web/chartdata"))))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/index.html")
	})

	// Charts endpoint (no rate limiting) - legacy, renders server-side
	r.Get("/charts", charts.ChartsHandler())
}
