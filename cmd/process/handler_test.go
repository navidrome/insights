package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/navidrome/insights/consts"
)

// nextMarker is written by the stand-in "next" handler so the middleware tests can tell
// "next was called" apart from "nothing happened": httptest.NewRecorder starts at 200, so a
// status-only assertion passes even when the middleware never calls next and writes nothing.
const nextMarker = "reached next handler"

func markerHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(nextMarker))
	})
}

func TestChartsJSONHandlerNotFound(t *testing.T) {
	t.Chdir(t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/charts", nil)
	rec := httptest.NewRecorder()
	chartsJSONHandler()(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "not available") {
		t.Fatalf("got body %q, want it to mention the data is not available", body)
	}
}

func TestAPIKeyMiddleware(t *testing.T) {
	t.Setenv("API_KEY", "secret")

	cases := []struct {
		name        string
		header      string
		query       string
		want        int
		wantReached bool
	}{
		{"no credentials", "", "", http.StatusUnauthorized, false},
		{"wrong key", "Bearer nope", "", http.StatusUnauthorized, false},
		{"wrong query key", "", "nope", http.StatusUnauthorized, false},
		{"bearer header", "Bearer secret", "", http.StatusOK, true},
		{"query param", "", "secret", http.StatusOK, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := "/api/charts"
			if tc.query != "" {
				url += "?" + consts.APIKeyQueryParam + "=" + tc.query
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			apiKeyMiddleware(markerHandler()).ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("got status %d, want %d", rec.Code, tc.want)
			}
			reached := strings.Contains(rec.Body.String(), nextMarker)
			if reached != tc.wantReached {
				t.Fatalf("next handler reached = %t, want %t (body %q)", reached, tc.wantReached, rec.Body.String())
			}
		})
	}
}

// An unset API_KEY makes /api/charts public, which is how the deployment without a key works.
func TestAPIKeyMiddlewareNoKeyConfigured(t *testing.T) {
	t.Setenv("API_KEY", "")

	req := httptest.NewRequest(http.MethodGet, "/api/charts", nil)
	rec := httptest.NewRecorder()
	apiKeyMiddleware(markerHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, nextMarker) {
		t.Fatalf("got body %q, want the next handler to have run", body)
	}
}

func TestChartsJSONHandlerServesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := os.MkdirAll(consts.ChartDataDir, consts.DirPermissions); err != nil {
		t.Fatalf("creating chart dir: %v", err)
	}
	const content = `{"totalInstances":1}`
	path := filepath.Join(consts.ChartDataDir, consts.ChartsJSONFile)
	if err := os.WriteFile(path, []byte(content), consts.FilePermissions); err != nil {
		t.Fatalf("writing charts file: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/charts", nil)
	rec := httptest.NewRecorder()
	chartsJSONHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("got content-type %q, want application/json", ct)
	}
	// The recorder reports 200 even for a handler that writes nothing, so assert the payload.
	if body := rec.Body.String(); body != content {
		t.Fatalf("got body %q, want %q", body, content)
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
