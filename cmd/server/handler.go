package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/navidrome/insights/consts"
	"github.com/navidrome/insights/db"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

func handler(dbConn *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var data insights.Data

		err := decodeJSONBody(w, r, &data)
		if err != nil {
			var mr *malformedRequest
			if errors.As(err, &mr) {
				http.Error(w, mr.msg, mr.status)
			} else {
				log.Printf("error decoding payload: %s", err.Error())
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			}
			return
		}

		err = db.SaveReport(dbConn, data, time.Now())
		if err != nil {
			log.Printf("Error handling request: %s", err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}

// apiKeyMiddleware validates the API key if API_KEY env var is set.
// If API_KEY is empty, all requests are allowed (public access).
// Otherwise, requires Authorization: Bearer <key> header or api_key query param.
func apiKeyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := os.Getenv("API_KEY")
		if apiKey == "" {
			// No API key configured, allow public access
			next.ServeHTTP(w, r)
			return
		}

		// Check Authorization header
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, consts.AuthHeaderPrefix) {
			if strings.TrimPrefix(authHeader, consts.AuthHeaderPrefix) == apiKey {
				next.ServeHTTP(w, r)
				return
			}
		}

		// Check query parameter
		if r.URL.Query().Get(consts.APIKeyQueryParam) == apiKey {
			next.ServeHTTP(w, r)
			return
		}

		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

// chartsJSONHandler serves the charts.json file directly.
func chartsJSONHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chartsPath := filepath.Join(consts.ChartDataDir, consts.ChartsJSONFile)
		if _, err := os.Stat(chartsPath); os.IsNotExist(err) {
			http.Error(w, "Charts data not available", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		http.ServeFile(w, r, chartsPath)
	}
}
