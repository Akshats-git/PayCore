// Package httpapi wires the PayCore HTTP routes and handlers together.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// ReadinessFunc reports whether the service's dependencies are reachable. It
// returns nil when the service is ready to serve traffic, or an error describing
// the first dependency that is not.
type ReadinessFunc func(ctx context.Context) error

// NewRouter returns the top-level HTTP handler for the service. Every route the
// service exposes is registered here, so this function is the one place to look
// to see the whole API surface.
func NewRouter(logger *slog.Logger, ready ReadinessFunc) http.Handler {
	mux := http.NewServeMux()

	// Liveness vs readiness are different questions (see the handlers below).
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /readyz", handleReadyz(logger, ready))

	// Middleware wraps the entire mux: a request flows through logRequests
	// first, then into whichever handler the mux matches.
	return logRequests(logger, mux)
}

// handleHealthz is a *liveness* probe: it returns 200 as long as the process is
// running and able to serve HTTP. It deliberately touches no dependencies. If
// this fails, the right response is to restart the process.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz is a *readiness* probe: it returns 200 only if the service can
// actually do its job, which means its dependencies (Postgres, Redis) are
// reachable. If this fails, the right response is to stop sending the process
// traffic (pull it from the load balancer) — but NOT to restart it, since the
// process itself is fine and the dependency may simply be recovering.
func handleReadyz(logger *slog.Logger, ready ReadinessFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Bound the check so a hung dependency can't make readyz hang too.
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		if err := ready(ctx); err != nil {
			logger.Warn("readiness check failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}

// writeJSON serializes body as JSON and writes it with the given status code.
// Centralizing this keeps every response consistently Content-Type'd and gives
// us a single place to evolve response formatting later.
func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// logRequests is a minimal middleware that records one structured log line per
// request after it completes. Middleware in Go is just a function that takes an
// http.Handler and returns a wrapped http.Handler.
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
		)
	})
}
