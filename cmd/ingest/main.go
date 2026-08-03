// Command ingest accepts insights reports and appends them to the daily report file.
// It runs no background jobs: restarting the process worker never interrupts collection.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/navidrome/insights/consts"
	"github.com/navidrome/insights/store"
)

func main() {
	dataFolder := os.Getenv("DATA_FOLDER")

	writer, err := store.NewWriter(dataFolder)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Writing reports under %s", dataFolder) //#nosec G706 -- dataFolder is from controlled env var

	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)

	r.Get("/healthz", healthzHandler())

	limiter := httprate.NewRateLimiter(consts.RateLimitRequests, consts.RateLimitWindow, httprate.WithKeyByIP())
	r.With(limiter.Handler).Post("/collect", handler(writer))

	port := os.Getenv("PORT")
	if port == "" {
		port = consts.DefaultPort
	}

	server := &http.Server{
		Addr:              ":" + port,
		ReadHeaderTimeout: consts.ReadHeaderTimeout,
		Handler:           r,
	}

	// Shut down cleanly so the gzip member is terminated and buffered reports are not lost.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("Error shutting down server: %s", err)
		}
	}()

	log.Print("Starting Insights ingest on :" + port) //#nosec G706 -- port is from controlled env var or constant
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("ListenAndServe: %s", err)
	}

	if err := writer.Close(); err != nil {
		log.Printf("Error closing report writer: %s", err)
	}
	log.Print("Ingest stopped")
}
