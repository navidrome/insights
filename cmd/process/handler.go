package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/navidrome/insights/internal/consts"
)

// apiKeyMiddleware validates the API key if API_KEY env var is set.
// If API_KEY is empty, all requests are allowed (public access).
// Otherwise, requires Authorization: Bearer <key> header or api_key query param.
func apiKeyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := os.Getenv("API_KEY")
		if apiKey == "" {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, consts.AuthHeaderPrefix) {
			if strings.TrimPrefix(authHeader, consts.AuthHeaderPrefix) == apiKey {
				next.ServeHTTP(w, r)
				return
			}
		}

		if r.URL.Query().Get(consts.APIKeyQueryParam) == apiKey {
			next.ServeHTTP(w, r)
			return
		}

		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

// chartsJSONHandler serves the charts.json file this process generates.
func chartsJSONHandler(dataFolder string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Built from the data folder and two constants, with nothing from the request in it.
		chartsPath := filepath.Join(dataFolder, consts.ChartDataDir, consts.ChartsJSONFile)
		if _, err := os.Stat(chartsPath); os.IsNotExist(err) { //#nosec G703 -- path is from controlled env var and constants
			http.Error(w, "Charts data not available", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		http.ServeFile(w, r, chartsPath) //#nosec G703 -- path is from controlled env var and constants
	}
}

// healthzHandler reports that the cron worker is up.
func healthzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	}
}
