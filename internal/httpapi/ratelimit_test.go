package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Akshats-git/PayCore/internal/ratelimit"
)

// fakeLimiter is a RateLimiter with fixed behavior for middleware tests.
type fakeLimiter struct {
	allow bool
	err   error
}

func (f fakeLimiter) Allow(context.Context, string) (ratelimit.Result, error) {
	if f.err != nil {
		return ratelimit.Result{}, f.err
	}
	return ratelimit.Result{Allowed: f.allow, RetryAfter: time.Second}, nil
}

func routerWithLimiter(lim RateLimiter) http.Handler {
	return NewRouter(Deps{Logger: discardLogger(), Ready: alwaysReady, RateLimiter: lim})
}

// A POST /v1/charges with no Idempotency-Key returns 400 *if it reaches the
// handler* — a convenient signal that the rate limiter let it through.
func postV1(t *testing.T, router http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/charges", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestRateLimitAllowsReachHandler(t *testing.T) {
	rec := postV1(t, routerWithLimiter(fakeLimiter{allow: true}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (request should reach the handler)", rec.Code)
	}
}

func TestRateLimitDenies(t *testing.T) {
	rec := postV1(t, routerWithLimiter(fakeLimiter{allow: false}))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("expected a Retry-After header on a 429")
	}
}

func TestRateLimitFailsOpen(t *testing.T) {
	// If the limiter errors (e.g. Redis down), the request should still be served.
	rec := postV1(t, routerWithLimiter(fakeLimiter{err: errors.New("redis down")}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (fail open reaches the handler)", rec.Code)
	}
}

func TestRateLimitExemptsHealth(t *testing.T) {
	// Even with a limiter that denies everything, health probes must pass.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	routerWithLimiter(fakeLimiter{allow: false}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (health is exempt from rate limiting)", rec.Code)
	}
}
