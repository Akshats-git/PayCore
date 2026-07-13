package httpapi

import (
	"context"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/Akshats-git/PayCore/internal/ratelimit"
)

// RateLimiter is the slice of rate-limiter behavior the middleware needs.
// *ratelimit.Limiter satisfies it.
type RateLimiter interface {
	Allow(ctx context.Context, key string) (ratelimit.Result, error)
}

// rateLimitMiddleware rejects requests that exceed the per-client token bucket
// with 429. It only limits the API surface (/v1/...); health and readiness
// probes are exempt so a rate-limited client can't hide the service's health.
func rateLimitMiddleware(limiter RateLimiter, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}

		res, err := limiter.Allow(r.Context(), clientIP(r))
		if err != nil {
			// Fail open: a rate-limiter (Redis) outage must not take down the
			// API. Protection is a safety net, not a correctness gate.
			logger.Warn("rate limiter unavailable, allowing request", "err", err)
			next.ServeHTTP(w, r)
			return
		}
		if !res.Allowed {
			retry := int(math.Ceil(res.RetryAfter.Seconds()))
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP extracts the client's IP from the request. Behind a real proxy you'd
// consult a trusted X-Forwarded-For header; for this service the direct peer
// address is the identity we rate-limit on.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
