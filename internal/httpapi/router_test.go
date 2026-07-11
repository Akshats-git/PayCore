package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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

// alwaysReady is a ReadinessFunc that always reports the service is healthy.
func alwaysReady(context.Context) error { return nil }

// get performs an in-memory request against the router and returns the recorder.
// Using httptest.NewRecorder means we never open a real network port, so tests
// are fast and deterministic.
func get(t *testing.T, router http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestHealthz asserts the liveness endpoint returns 200 and the expected body.
func TestHealthz(t *testing.T) {
	rec := get(t, NewRouter(discardLogger(), alwaysReady), http.MethodGet, "/healthz")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := decodeStatus(t, rec); got != "ok" {
		t.Fatalf("status field = %q, want %q", got, "ok")
	}
}

// TestHealthzMethodNotAllowed documents that the method-aware router rejects a
// POST to a GET-only route with 405, with no extra code from us.
func TestHealthzMethodNotAllowed(t *testing.T) {
	rec := get(t, NewRouter(discardLogger(), alwaysReady), http.MethodPost, "/healthz")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// TestReadyz asserts that when dependencies are reachable, readyz returns 200.
func TestReadyz(t *testing.T) {
	rec := get(t, NewRouter(discardLogger(), alwaysReady), http.MethodGet, "/readyz")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := decodeStatus(t, rec); got != "ready" {
		t.Fatalf("status field = %q, want %q", got, "ready")
	}
}

// TestReadyzUnavailable asserts that when a dependency is down, readyz returns
// 503 — the signal a load balancer uses to stop routing traffic to this
// instance without restarting it.
func TestReadyzUnavailable(t *testing.T) {
	notReady := func(context.Context) error { return errors.New("postgres: connection refused") }
	rec := get(t, NewRouter(discardLogger(), notReady), http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := decodeStatus(t, rec); got != "unavailable" {
		t.Fatalf("status field = %q, want %q", got, "unavailable")
	}
}

// decodeStatus pulls the "status" field out of a JSON response body.
func decodeStatus(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body["status"]
}
