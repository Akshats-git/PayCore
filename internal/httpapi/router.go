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

// Deps bundles everything the router needs to build its handlers. Grouping them
// in a struct keeps NewRouter's signature stable as the API surface grows.
type Deps struct {
	Logger   *slog.Logger
	Ready    ReadinessFunc
	Accounts AccountService
	Charges  ChargeService
}

// NewRouter returns the top-level HTTP handler for the service. Every route the
// service exposes is registered here, so this function is the one place to look
// to see the whole API surface.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	// Health.
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /readyz", handleReadyz(d.Logger, d.Ready))

	// Accounts and charges.
	mux.HandleFunc("POST /v1/accounts", handleCreateAccount(d.Logger, d.Accounts))
	mux.HandleFunc("GET /v1/accounts/{id}", handleGetAccount(d.Logger, d.Accounts))
	mux.HandleFunc("POST /v1/charges", handleCreateCharge(d.Logger, d.Charges))
	mux.HandleFunc("GET /v1/charges/{id}", handleGetCharge(d.Logger, d.Charges))

	// Middleware wraps the entire mux: a request flows through logRequests
	// first, then into whichever handler the mux matches.
	return logRequests(d.Logger, mux)
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
// traffic (pull it from the load balancer) — but NOT to restart it.
func handleReadyz(logger *slog.Logger, ready ReadinessFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError writes a JSON error body ({"error": msg}) with the given status.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// writeRaw writes pre-serialized JSON bytes with the given status. It's used to
// send an idempotent response byte-for-byte, whether freshly produced or replayed
// from storage.
func writeRaw(w http.ResponseWriter, code int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(body)
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
