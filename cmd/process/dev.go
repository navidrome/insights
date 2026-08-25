//go:build dev

// Dev-only routes: static web assets and the server-rendered charts page. Production builds
// get the no-op in dev_stub.go. Not a package doc: dev.go sorts before main.go.

package main

import (
	"net/http"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/insights/internal/charts"
	"github.com/navidrome/insights/internal/consts"
)

func registerDevRoutes(r chi.Router, dataFolder string) {
	// Chart data is output, so it lives under the data folder. index.html is a source asset
	// that ships in the repo, so it stays relative to the working directory.
	r.Handle("/chartdata/*", http.StripPrefix("/chartdata/",
		http.FileServer(http.Dir(filepath.Join(dataFolder, consts.ChartDataDir)))))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, consts.WebIndexPath)
	})

	// Legacy charts endpoint, no rate limiting, rendered server-side
	r.Get("/charts", charts.ChartsHandler(dataFolder))
}
