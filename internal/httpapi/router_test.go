package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// discardLogger returns a logger that throws its output away, so tests don't
// spew log lines. We still pass a real *slog.Logger so the code path under test
// is identical to production.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestHealthz asserts the liveness endpoint returns 200 and the expected body.
// It is intentionally the very first test in the project: from increment one,
// "it works" means "a test proves it works."
func TestHealthz(t *testing.T) {
	router := NewRouter(discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got := body["status"]; got != "ok" {
		t.Fatalf("status field = %q, want %q", got, "ok")
	}
}

// TestHealthzMethodNotAllowed documents a nice property of the method-aware
// router: a POST to a GET-only route is rejected with 405 automatically, with
// no extra code from us.
func TestHealthzMethodNotAllowed(t *testing.T) {
	router := NewRouter(discardLogger())

	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
