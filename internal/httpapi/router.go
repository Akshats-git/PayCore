// Package httpapi wires the PayCore HTTP routes and handlers together.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// NewRouter returns the top-level HTTP handler for the service. Every route the
// service exposes is registered here, so this function is the one place to look
// to see the whole API surface.
func NewRouter(logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// Go 1.22+ lets us bind the HTTP method directly in the route pattern.
	// A request with the wrong method to a registered path gets a 405 for free.
	mux.HandleFunc("GET /healthz", handleHealthz)

	// Middleware wraps the entire mux: a request flows through logRequests
	// first, then into whichever handler the mux matches.
	return logRequests(logger, mux)
}

// handleHealthz is a liveness probe: it returns 200 as long as the process is
// running and able to serve HTTP. It deliberately touches no dependencies
// (no DB, no Redis) — its only job is to answer "is this process alive?".
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
