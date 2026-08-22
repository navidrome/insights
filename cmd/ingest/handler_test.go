package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/navidrome/insights/store"
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
