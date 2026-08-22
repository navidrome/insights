// Command ingest accepts insights reports and appends them to the daily report file. It runs
// no background jobs, so restarting the process worker never interrupts collection.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/navidrome/insights/consts"
	"github.com/navidrome/insights/store"
)

// shutdownTimeout bounds how long in-flight reports have to finish once a signal arrives.
// handlerDrainTimeout bounds the wait after that for handlers Shutdown gave up on, whose
// connections run force-closes first. Variables so the specs can reach the deadline path.
var (
	shutdownTimeout     = 10 * time.Second
	handlerDrainTimeout = 5 * time.Second
)

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

	// Terminate the gzip member cleanly so buffered reports are not lost.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A latched write error would 500 every later report behind a green /healthz. Treat it as a
	// shutdown signal so the supervisor restarts us into a fresh segment.
	ctx, cancel := watchWriter(ctx, writer.Fatal())
	defer cancel()

	// Fall through rather than exiting, so writer.Close below still releases the lock file.
	var serveErr error
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Printf("Listen: %s", err)
		serveErr = err
	} else {
		log.Print("Starting Insights ingest on :" + port) //#nosec G706 -- port is from controlled env var or constant
		if err := run(ctx, ln, newRouter(writer)); err != nil {
			log.Printf("Serve: %s", err)
			serveErr = err
		}
	}

	// run returns only once the drain is complete.
	if err := writer.Close(); err != nil {
		log.Printf("Error closing report writer: %s", err)
	}
	if err := writer.Err(); err != nil {
		// Non-zero so this does not look like a clean stop. The skipped deferred cancels are
		// fine: the drain and writer.Close have already run.
		log.Printf("Ingest stopped after an unrecoverable write error: %s", err)
		os.Exit(1)
	}
	if serveErr != nil {
		// Same reason: a port already in use on redeploy must not exit 0.
		log.Printf("Ingest stopped after a server error: %s", serveErr)
		os.Exit(1)
	}
	log.Print("Ingest stopped")
}

// watchWriter cancels with its parent or on an unrecoverable writer error, whichever is
// first.
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

// newRouter wires the two public endpoints. /healthz is not rate limited: liveness probes.
func newRouter(writer *store.Writer) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)

	r.Get("/healthz", healthzHandler())

	limiter := httprate.NewRateLimiter(consts.RateLimitRequests, consts.RateLimitWindow, httprate.WithKeyByIP())
	r.With(limiter.Handler).Post("/collect", handler(writer))

	return r
}

// inflight counts the handlers running right now, so run can hold one invariant: the writer is
// not closed while a handler can still call Append. Shutdown alone does not give that, because
// it returns on its deadline and leaves those handlers running.
type inflight struct {
	mu   sync.Mutex
	n    int
	zero chan struct{} // non-nil only while wait is watching, closed when n reaches 0
}

// track counts every request for as long as its handler runs, /healthz included: a counter
// that has to know its route stops covering the next route somebody adds.
func (c *inflight) track(h http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		c.enter()
		defer c.leave()
		h.ServeHTTP(rw, r)
	})
}

func (c *inflight) enter() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *inflight) leave() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n--
	if c.n == 0 && c.zero != nil {
		close(c.zero)
		c.zero = nil
	}
}

// wait blocks until no handler is running or timeout expires, and reports which. A WaitGroup
// cannot be waited on with a deadline, and Add racing Wait is exactly this situation.
func (c *inflight) wait(timeout time.Duration) bool {
	c.mu.Lock()
	if c.n == 0 {
		c.mu.Unlock()
		return true
	}
	if c.zero == nil {
		c.zero = make(chan struct{})
	}
	zero := c.zero
	c.mu.Unlock()

	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-zero:
		return true
	case <-t.C:
		return false
	}
}

// newServer builds the ingest server. Separate so the specs can assert the timeouts directly
// instead of inferring them from behaviour that takes minutes to show.
func newServer(h http.Handler) *http.Server {
	return &http.Server{
		ReadHeaderTimeout: consts.ReadHeaderTimeout,
		// Without a whole-request deadline a slow-loris upload holds a handler past run's
		// shutdown deadline.
		ReadTimeout: consts.ReadTimeout,
		// Never left to net/http's fallback, which is ReadTimeout. See consts.IdleTimeout.
		IdleTimeout: consts.IdleTimeout,
		Handler:     h,
	}
}

// run serves ln until ctx is cancelled, then returns only once the in-flight requests have
// drained. That drain is what makes the report writer safe for main to close afterwards.
func run(ctx context.Context, ln net.Listener, h http.Handler) error {
	var live inflight
	server := newServer(live.track(h))

	// serveFailed lets a Serve error start the drain. Nothing else will: main cancels ctx only
	// on a signal or a fatal writer error, so waiting for one here would deadlock.
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
			// Shutdown left a handler running on an open connection. Closing it makes the
			// handler unwind, which is what this path needs, not the response.
			log.Printf("Error shutting down server, closing connections: %s", err)
			_ = server.Close()
		}
		// Only now is the writer safe to close: a handler reaching Append after Close loses a
		// report the client was never told to resend.
		if !live.wait(handlerDrainTimeout) {
			log.Printf("Report handlers still running %s after the shutdown deadline; closing the "+
				"report writer anyway, so an in-flight report may be lost", handlerDrainTimeout)
		}
	}()

	err := server.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		// The listener closing starts the drain; Shutdown is still running.
		<-done
		return nil
	}
	// The accept loop broke. Requests accepted before that are still being served, so run the
	// same bounded drain rather than losing them.
	close(serveFailed)
	<-done
	// The Serve failure explains why the process is stopping; the shutdown's does not.
	return err
}
