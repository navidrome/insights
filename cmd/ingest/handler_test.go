package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/insights/internal/store"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

func TestCollectWritesRecord(t *testing.T) {
	dir := t.TempDir()

	w, err := store.NewWriter(dir)
	if err != nil {
		t.Fatalf("creating writer: %v", err)
	}
	defer func() { _ = w.Close() }()

	var data insights.Data
	data.InsightsID = "test-instance"
	data.Version = "0.61.2"
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshalling payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/collect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler(w)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flushing: %v", err)
	}

	seq, _, err := store.ReadDay(dir, time.Now().UTC())
	if err != nil {
		t.Fatalf("reading day: %v", err)
	}
	var ids []string
	for r := range seq {
		ids = append(ids, r.Data.InsightsID)
	}
	if len(ids) != 1 || ids[0] != "test-instance" {
		t.Fatalf("got %v, want [test-instance]", ids)
	}
	_ = os.RemoveAll(dir)
}

// A malformed payload is the one failure that says a client is sending something wrong, and it
// was the only one the handler answered without recording. The access log carries the status but
// never the reason, so a Navidrome release that started sending a bad field would 400 every
// affected instance in silence.
func TestCollectLogsWhyAPayloadWasRejected(t *testing.T) {
	dir := t.TempDir()
	w, err := store.NewWriter(dir)
	if err != nil {
		t.Fatalf("creating writer: %v", err)
	}
	defer func() { _ = w.Close() }()

	var logged bytes.Buffer
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})

	req := httptest.NewRequest(http.MethodPost, "/collect", strings.NewReader(`{"version": 42}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(w)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400", rec.Code)
	}
	// The reason, not just that something failed: "version" is what tells you which field the
	// client got wrong, and it is the whole point of logging this at all.
	if got := logged.String(); !strings.Contains(got, "version") {
		t.Fatalf("log does not name the offending field.\ngot: %q", got)
	}
}

func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	healthzHandler()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
	// The recorder reports 200 for a handler that writes nothing, so assert the body too.
	if got := rec.Body.String(); got != "ok\n" {
		t.Fatalf("got body %q, want %q", got, "ok\n")
	}
}
