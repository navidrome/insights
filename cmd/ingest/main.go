// Command ingest accepts insights reports and appends them to the daily report file.
// It runs no background jobs: restarting the process worker never interrupts collection.
package main

import (
	"context"
	"errors"
	"log"
	"net"
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

// shutdownTimeout bounds how long in-flight reports have to finish once a signal arrives.
const shutdownTimeout = 10 * time.Second

func main() {
	dataFolder := os.Getenv("DATA_FOLDER")

	writer, err := store.NewWriter(dataFolder)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Writing reports under %s", dataFolder) //#nosec G706 -- dataFolder is from controlled env var

	port := os.Getenv("PORT")
	if port == "" {
		port = consts.DefaultPort
	}

	// Shut down cleanly so the gzip member is terminated and buffered reports are not lost.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A latched gzip error is permanent: every later report would be answered 500 for the
	// rest of this process's life, while /healthz stayed green. Treat it as a shutdown
	// signal instead, so the in-flight drain and writer.Close still run and the supervisor
	// restarts us into a fresh segment.
	ctx, cancel := watchWriter(ctx, writer.Fatal())
	defer cancel()

	// A failure to bind falls through rather than exiting, so writer.Close below still
	// releases the lock file.
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Printf("Listen: %s", err)
	} else {
		log.Print("Starting Insights ingest on :" + port) //#nosec G706 -- port is from controlled env var or constant
		if err := run(ctx, ln, newRouter(writer)); err != nil {
			log.Printf("Serve: %s", err)
		}
	}

	// run only returns once the drain is complete, so no handler can still be appending here.
	if err := writer.Close(); err != nil {
		log.Printf("Error closing report writer: %s", err)
	}
	if err := writer.Err(); err != nil {
		// Exit non-zero so the failure is visible in `docker compose ps` and in the exit
		// status, rather than looking like a clean stop. The deferred cancels are skipped
		// on purpose: the drain and writer.Close have already run.
		log.Printf("Ingest stopped after an unrecoverable write error: %s", err)
		os.Exit(1)
	}
	log.Print("Ingest stopped")
}

// watchWriter derives a context that is cancelled either with its parent or as soon as the
// report writer reports an unrecoverable error, whichever comes first.
func watchWriter(parent context.Context, fatal <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-fatal:
			log.Print("Report writer failed permanently, shutting down")
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// newRouter wires the two public endpoints. Only /collect is rate limited: /healthz has to stay
// answerable for liveness probes.
func newRouter(writer *store.Writer) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)

	r.Get("/healthz", healthzHandler())

	limiter := httprate.NewRateLimiter(consts.RateLimitRequests, consts.RateLimitWindow, httprate.WithKeyByIP())
	r.With(limiter.Handler).Post("/collect", handler(writer))

	return r
}

// run serves ln until ctx is cancelled, then drains the requests already in flight, returning
// only once that drain has finished.
//
// Waiting for the drain is what makes the report writer safe to close afterwards: Append fails
// with os.ErrClosed once Close has run, so a handler still mid-request when the writer closed
// would answer 500 and drop a report that had already been accepted.
func run(ctx context.Context, ln net.Listener, h http.Handler) error {
	server := &http.Server{
		ReadHeaderTimeout: consts.ReadHeaderTimeout,
		Handler:           h,
	}

	// serveFailed lets a Serve error start the drain itself. Nothing else will: a listener that
	// cannot be served — a port already in use, above all — leaves ctx uncancelled forever,
	// because main only cancels it on a signal or on a fatal writer error. Waiting on a
	// shutdown nobody triggered is how this path deadlocks.
	serveFailed := make(chan struct{})
	done := make(chan struct{})
	go func() { //#nosec G118 -- the shutdown deadline must not derive from ctx: ctx is already cancelled here, so a derived context would expire immediately and abort the drain
		defer close(done)
		select {
		case <-ctx.Done():
		case <-serveFailed:
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("Error shutting down server: %s", err)
		}
	}()

	err := server.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		// Serve returns when the listener closes, which is the start of the drain and not
		// the end of it. Shutdown is still running: wait for it.
		<-done
		return nil
	}
	// Serve failed for its own reasons — the accept loop broke, not the listener closing. The
	// requests it accepted before that are still being served, and main closes the report
	// writer the moment this returns, so returning now answers an already-accepted report 500
	// and loses it. Same bounded drain as the clean path, started here because nothing else
	// will start it.
	close(serveFailed)
	<-done
	// The Serve failure, not the shutdown's: that is the one that explains why the process is
	// stopping.
	return err
}
