package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/insights/store"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

// TestRunDrainsInFlightRequestBeforeReturning pins the shutdown ordering main depends on.
//
// main closes the report writer as soon as run returns, and Append fails with os.ErrClosed
// after that. So run returning while a request is still being served means that report is
// answered 500 and lost. Serve returns the moment the listener closes, which is the *start*
// of Shutdown's drain, so run has to wait for the drain to finish on its own.
func TestRunDrainsInFlightRequestBeforeReturning(t *testing.T) {
	dir := t.TempDir()
	writer, err := store.NewWriter(dir)
	if err != nil {
		t.Fatalf("creating writer: %v", err)
	}
	defer func() { _ = writer.Close() }()

	// Block in front of the real router, so the request is provably in flight when the
	// shutdown signal arrives instead of depending on timing to get it there.
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

	// main's next step. With the drain finished this cannot lose the report.
	if err := writer.Close(); err != nil {
		t.Fatalf("closing writer: %v", err)
	}

	seq, err := store.ReadDay(dir, time.Now().UTC())
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

// TestFatalWriterErrorStopsTheServer pins the recovery path for a permanently broken report
// writer.
//
// gzip.Writer latches its first write error forever, so after one ENOSPC or EIO every later
// Append fails, /collect answers 500 for the rest of the process's life, and /healthz stays
// green — nothing restarts the container. Shutting the server down instead turns that into a
// supervisor restart, which opens a fresh segment.
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

	// Stands in for store.Writer.Fatal: the writer only closes that channel on a real disk
	// failure, which a test cannot provoke portably. store's own suite covers the closing.
	fatal := make(chan struct{})
	ctx, cancel := watchWriter(context.Background(), fatal)
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, ln, newRouter(writer)) }()

	// The server is serving before the failure, so a run that returns below returns because
	// of the fatal signal and not because it never started.
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

// breakingListener serves one connection and then breaks, which is how an unexpected Serve
// error arrives while a request accepted a moment earlier is still being handled. The second
// Accept blocks until the test says so, so the failure lands at a point it chooses rather than
// at one the scheduler picks.
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
	// Not a net.Error, so Serve does not treat it as temporary and retry: it gives up and
	// returns it, exactly as it would for a broken accept loop.
	return nil, errors.New("accept boom")
}

// TestRunDrainsInFlightRequestAfterServeError is the same guarantee as the test above, for the
// path where Serve fails on its own rather than being shut down.
//
// The reports already accepted are still being served when that happens, and main closes the
// report writer as soon as run returns — so returning straight away answers a report that was
// accepted with a 500 and loses it. The assertion that matters is the last one: that the report
// is on disk. A test that only checked that run returned the Serve error would pass against a
// run that drained nothing.
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

	// Never cancelled: only Serve's own failure can release run here, as on the bind-failure
	// path in production.
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

	// main's next step, and the point of the drain: the accepted report is already written.
	if err := writer.Close(); err != nil {
		t.Fatalf("closing writer: %v", err)
	}

	seq, err := store.ReadDay(dir, time.Now().UTC())
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

// TestRunReturnsServeError checks that a serve failure comes straight back instead of waiting
// on a shutdown that will never be signalled. main has to reach writer.Close on this path too,
// or a process that can never bind leaves the lock file held.
func TestRunReturnsServeError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("closing listener: %v", err)
	}

	// ctx is never cancelled: nothing but Serve's own error can release run here.
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
