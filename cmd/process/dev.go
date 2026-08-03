//go:build dev

package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/insights/charts"
	"github.com/navidrome/insights/consts"
)

func registerDevRoutes(r chi.Router) {
	// Static files for charts
	r.Handle("/chartdata/*", http.StripPrefix("/chartdata/", http.FileServer(http.Dir(consts.ChartDataDir))))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, consts.WebIndexPath)
	})

	// Charts endpoint (no rate limiting) - legacy, renders server-side
	r.Get("/charts", charts.ChartsHandler())
}
