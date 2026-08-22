//go:build dev

// Dev-only routes: static web assets and the server-rendered charts page. Production builds
// get the no-op in dev_stub.go. Not a package doc: dev.go sorts before main.go.

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

	// Legacy charts endpoint, no rate limiting, rendered server-side
	r.Get("/charts", charts.ChartsHandler())
}
