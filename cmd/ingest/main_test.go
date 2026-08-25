package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/insights/internal/consts"
	"github.com/navidrome/insights/internal/store"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

// TestRunDrainsInFlightRequestBeforeReturning pins the ordering main depends on: main closes
// the writer as soon as run returns, and Serve returns at the *start* of Shutdown's drain.
func TestRunDrainsInFlightRequestBeforeReturning(t *testing.T) {
	dir := t.TempDir()
	writer, err := store.NewWriter(dir)
	if err != nil {
		t.Fatalf("creating writer: %v", err)
	}
	defer func() { _ = writer.Close() }()

	// Blocks in front of the real router, so the request is provably in flight.
	entered := make(chan struct{})
	release := make(chan struct{})
	router := newRouter(writer)
	blocking := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		router.ServeHTTP(rw, r)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, ln, blocking) }()

	var data insights.Data
	data.InsightsID = "in-flight"
	data.Version = "0.61.2"
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshalling payload: %v", err)
	}

	status := make(chan int, 1)
	reqErr := make(chan error, 1)
	go func() {
		resp, err := http.Post("http://"+ln.Addr().String()+"/collect", "application/json", bytes.NewReader(body))
		if err != nil {
			reqErr <- err
			return
		}
		defer func() { _ = resp.Body.Close() }()
		status <- resp.StatusCode
	}()

	select {
	case <-entered:
	case err := <-reqErr:
		t.Fatalf("request failed before reaching the handler: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the request to reach the handler")
	}

	cancel() // the SIGTERM equivalent, with a report half-served

	select {
	case err := <-runErr:
		t.Fatalf("run returned (err=%v) while a request was still in flight: main would close the writer mid-request", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after the in-flight request finished")
	}

	select {
	case code := <-status:
		if code != http.StatusOK {
			t.Fatalf("got status %d, want 200", code)
		}
	case err := <-reqErr:
		t.Fatalf("request failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the response")
	}

	// main's next step.
	if err := writer.Close(); err != nil {
		t.Fatalf("closing writer: %v", err)
	}

	seq, _, err := store.ReadDay(dir, time.Now().UTC())
	if err != nil {
		t.Fatalf("reading day: %v", err)
	}
	var ids []string
	for r := range seq {
		ids = append(ids, r.Data.InsightsID)
	}
	if len(ids) != 1 || ids[0] != "in-flight" {
		t.Fatalf("got %v, want [in-flight]", ids)
	}
}

// TestRunWaitsForAHandlerThatOutlivesTheShutdownDeadline covers what Shutdown does not: it
// returns on its deadline and leaves that handler running. The last assertion is the one that
// matters, that the report reached disk.
func TestRunWaitsForAHandlerThatOutlivesTheShutdownDeadline(t *testing.T) {
	// Short enough to reach the deadline inside the spec.
	defer withTimeouts(100*time.Millisecond, 5*time.Second)()

	dir := t.TempDir()
	writer, err := store.NewWriter(dir)
	if err != nil {
		t.Fatalf("creating writer: %v", err)
	}
	defer func() { _ = writer.Close() }()

	entered := make(chan struct{})
	release := make(chan struct{})
	appendErr := make(chan error, 1)
	// Decode before the block, as the real handler does, so force-closing the connection
	// cannot rob the handler of the payload.
	handler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var data insights.Data
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			appendErr <- err
			return
		}
		close(entered)
		<-release
		appendErr <- writer.Append(data, time.Now())
		rw.WriteHeader(http.StatusOK)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, ln, handler) }()

	var data insights.Data
	data.InsightsID = "slow-handler"
	data.Version = "0.61.2"
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshalling payload: %v", err)
	}

	reqErr := make(chan error, 1)
	go func() {
		resp, err := http.Post("http://"+ln.Addr().String()+"/collect", "application/json", bytes.NewReader(body))
		if err != nil {
			// Expected: force-closed once Shutdown gives up on it.
			reqErr <- err
			return
		}
		_ = resp.Body.Close()
	}()

	select {
	case <-entered:
	case err := <-reqErr:
		t.Fatalf("request failed before reaching the handler: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the request to reach the handler")
	}

	cancel() // SIGTERM, with a report accepted and not yet written

	// Well past the shutdown deadline, handler still mid-request.
	select {
	case err := <-runErr:
		t.Fatalf("run returned (err=%v) while a handler could still Append: main would close the writer under it", err)
	case <-time.After(10 * shutdownTimeout):
	}

	close(release)

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after the handler finished")
	}

	select {
	case err := <-appendErr:
		if err != nil {
			t.Fatalf("appending the accepted report: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the handler to finish")
	}

	// main's next step.
	if err := writer.Close(); err != nil {
		t.Fatalf("closing writer: %v", err)
	}

	seq, _, err := store.ReadDay(dir, time.Now().UTC())
	if err != nil {
		t.Fatalf("reading day: %v", err)
	}
	var ids []string
	for r := range seq {
		ids = append(ids, r.Data.InsightsID)
	}
	if len(ids) != 1 || ids[0] != "slow-handler" {
		t.Fatalf("got %v, want [slow-handler]: the report was accepted and must not be lost", ids)
	}
}

// withTimeouts shrinks the shutdown deadlines for one spec and returns the restore.
func withTimeouts(shutdown, drain time.Duration) func() {
	prevShutdown, prevDrain := shutdownTimeout, handlerDrainTimeout
	shutdownTimeout, handlerDrainTimeout = shutdown, drain
	return func() { shutdownTimeout, handlerDrainTimeout = prevShutdown, prevDrain }
}

// TestRunForceClosesConnectionsOnTheShutdownDeadline pins the other half: the wait above is
// bounded only because the connections go away.
func TestRunForceClosesConnectionsOnTheShutdownDeadline(t *testing.T) {
	defer withTimeouts(100*time.Millisecond, 5*time.Second)()

	entered := make(chan struct{})
	readErr := make(chan error, 1)
	// Blocks reading a body the client never sends. Only closing the connection releases it.
	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		_, err := io.ReadAll(r.Body)
		readErr <- err
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, ln, handler) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	defer func() { _ = conn.Close() }()
	// Headers promising a body that never arrives.
	if _, err := conn.Write([]byte("POST /collect HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n")); err != nil {
		t.Fatalf("writing request: %v", err)
	}

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the request to reach the handler")
	}

	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(handlerDrainTimeout / 2):
		t.Fatal("run waited on a handler blocked on a connection nobody closed")
	}

	select {
	case <-readErr:
	case <-time.After(time.Second):
		t.Fatal("the stalled handler was never released")
	}
}

// TestServerSetsAnIdleTimeoutOfItsOwn pins the one timeout whose default is a trap: net/http
// falls back to ReadTimeout, which would retire Caddy's pooled connections after 5s, and Go
// will not replay a POST body.
func TestServerSetsAnIdleTimeoutOfItsOwn(t *testing.T) {
	srv := newServer(http.NotFoundHandler())

	if srv.IdleTimeout != consts.IdleTimeout {
		t.Fatalf("IdleTimeout is %s, want consts.IdleTimeout (%s)", srv.IdleTimeout, consts.IdleTimeout)
	}
	// The assertion above would also pass if the two were equal, which is what the fallback
	// produces.
	if srv.IdleTimeout <= srv.ReadTimeout {
		t.Fatalf("IdleTimeout %s must be longer than ReadTimeout %s: an idle pooled connection is not a stalled request",
			srv.IdleTimeout, srv.ReadTimeout)
	}
	// Above Caddy's 2-minute keep-alive, so the proxy closes idle connections first.
	if srv.IdleTimeout <= 2*time.Minute {
		t.Fatalf("IdleTimeout %s must exceed Caddy's 2m default keep-alive, or ingest is the side closing pooled connections", srv.IdleTimeout)
	}
	if srv.ReadTimeout != consts.ReadTimeout || srv.ReadHeaderTimeout != consts.ReadHeaderTimeout {
		t.Fatalf("got ReadTimeout %s / ReadHeaderTimeout %s, want %s / %s",
			srv.ReadTimeout, srv.ReadHeaderTimeout, consts.ReadTimeout, consts.ReadHeaderTimeout)
	}
}

// TestRunCutsOffARequestThatNeverFinishes pins the bound that makes the shutdown deadline mean
// something: ReadHeaderTimeout covers only the headers, so without ReadTimeout a stalled body
// holds a handler for as long as the client likes.
//
// It is slower than the rest by consts.ReadTimeout, which is the point.
func TestRunCutsOffARequestThatNeverFinishes(t *testing.T) {
	if consts.ReadTimeout >= shutdownTimeout {
		t.Fatalf("ReadTimeout %s must stay under the shutdown deadline %s", consts.ReadTimeout, shutdownTimeout)
	}

	entered := make(chan struct{})
	readErr := make(chan error, 1)
	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		_, err := io.ReadAll(r.Body)
		readErr <- err
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, ln, handler) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	defer func() { _ = conn.Close() }()
	// Headers promising a body this client never sends.
	if _, err := conn.Write([]byte("POST /collect HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n")); err != nil {
		t.Fatalf("writing request: %v", err)
	}

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the request to reach the handler")
	}

	// No shutdown: the read deadline alone has to end this request.
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("the stalled body read succeeded, want it cut off by the read deadline")
		}
	case <-time.After(consts.ReadTimeout + 3*time.Second):
		t.Fatal("a client that never finishes its body kept a handler running: it could stall the shutdown drain indefinitely")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}
}

// TestFatalWriterErrorStopsTheServer pins the recovery path for a permanently broken writer:
// without it, /collect 500s forever behind a green /healthz and nothing restarts the
// container.
func TestFatalWriterErrorStopsTheServer(t *testing.T) {
	dir := t.TempDir()
	writer, err := store.NewWriter(dir)
	if err != nil {
		t.Fatalf("creating writer: %v", err)
	}
	defer func() { _ = writer.Close() }()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	// Stands in for store.Writer.Fatal, which only closes on a real disk failure. store's own
	// suite covers the closing.
	fatal := make(chan struct{})
	ctx, cancel := watchWriter(context.Background(), fatal)
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, ln, newRouter(writer)) }()

	// Serving before the failure, so run returns below because of the signal and not because
	// it never started.
	resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("probing /healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d from /healthz, want 200", resp.StatusCode)
	}

	select {
	case err := <-runErr:
		t.Fatalf("run returned (err=%v) before the writer failed", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(fatal) // the writer latches an unrecoverable error

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run kept serving after the writer failed permanently: every report would be answered 500 until the process is killed by hand")
	}
}

// breakingListener serves one connection and then breaks, with the second Accept blocking
// until the test releases it, so the Serve error lands at a chosen point.
type breakingListener struct {
	net.Listener
	accepts int
	breakAt chan struct{}
}

func (l *breakingListener) Accept() (net.Conn, error) {
	l.accepts++
	if l.accepts == 1 {
		return l.Listener.Accept()
	}
	<-l.breakAt
	// Not a net.Error, so Serve gives up rather than retrying.
	return nil, errors.New("accept boom")
}

// TestRunDrainsInFlightRequestAfterServeError is the same guarantee for the path where Serve
// fails on its own. The last assertion is the one that matters, that the report reached disk.
func TestRunDrainsInFlightRequestAfterServeError(t *testing.T) {
	dir := t.TempDir()
	writer, err := store.NewWriter(dir)
	if err != nil {
		t.Fatalf("creating writer: %v", err)
	}
	defer func() { _ = writer.Close() }()

	entered := make(chan struct{})
	release := make(chan struct{})
	router := newRouter(writer)
	blocking := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		router.ServeHTTP(rw, r)
	})

	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	ln := &breakingListener{Listener: tcp, breakAt: make(chan struct{})}
	defer func() { _ = ln.Close() }()

	// Never cancelled, as on the bind-failure path in production.
	runErr := make(chan error, 1)
	go func() { runErr <- run(context.Background(), ln, blocking) }()

	var data insights.Data
	data.InsightsID = "in-flight"
	data.Version = "0.61.2"
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshalling payload: %v", err)
	}

	status := make(chan int, 1)
	reqErr := make(chan error, 1)
	go func() {
		resp, err := http.Post("http://"+tcp.Addr().String()+"/collect", "application/json", bytes.NewReader(body))
		if err != nil {
			reqErr <- err
			return
		}
		defer func() { _ = resp.Body.Close() }()
		status <- resp.StatusCode
	}()

	select {
	case <-entered:
	case err := <-reqErr:
		t.Fatalf("request failed before reaching the handler: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the request to reach the handler")
	}

	close(ln.breakAt) // the accept loop fails, with a report half-served

	select {
	case err := <-runErr:
		t.Fatalf("run returned (err=%v) while a request was still in flight: main would close the writer mid-request", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-runErr:
		if err == nil || !strings.Contains(err.Error(), "accept boom") {
			t.Fatalf("got %v, want the Serve failure reported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after the in-flight request finished")
	}

	select {
	case code := <-status:
		if code != http.StatusOK {
			t.Fatalf("got status %d, want 200", code)
		}
	case err := <-reqErr:
		t.Fatalf("request failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the response")
	}

	// main's next step, and the point of the drain.
	if err := writer.Close(); err != nil {
		t.Fatalf("closing writer: %v", err)
	}

	seq, _, err := store.ReadDay(dir, time.Now().UTC())
	if err != nil {
		t.Fatalf("reading day: %v", err)
	}
	var ids []string
	for r := range seq {
		ids = append(ids, r.Data.InsightsID)
	}
	if len(ids) != 1 || ids[0] != "in-flight" {
		t.Fatalf("got %v, want [in-flight]: the report was accepted and must not be lost", ids)
	}
}

// TestRunReturnsServeError checks that a serve failure comes straight back, so main reaches
// writer.Close and a process that can never bind does not hold the lock file.
func TestRunReturnsServeError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("closing listener: %v", err)
	}

	// ctx is never cancelled: only Serve's own error can release run.
	done := make(chan error, 1)
	go func() { done <- run(context.Background(), ln, http.NotFoundHandler()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("got nil error from a failed Serve, want the failure reported")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after Serve failed")
	}
}
