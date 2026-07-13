package httpapi

import (
	"net/http"
	"strings"
	"sync/atomic"
)

// priority classifies a request for load shedding.
type priority int

const (
	nonCritical priority = iota
	critical
)

// classifyPriority marks charge creation as critical and everything else
// (reads: charge/account lookups) as non-critical. Under overload we shed the
// non-critical first — like a restaurant turning away new walk-ins during a rush
// so the kitchen can finish the orders it already has.
func classifyPriority(r *http.Request) priority {
	if r.Method == http.MethodPost && r.URL.Path == "/v1/charges" {
		return critical
	}
	return nonCritical
}

// Shedder protects the whole service during a genuine overload. Where the rate
// limiter guards against a single noisy client, the shedder guards against total
// saturation: once more than maxInFlight requests are being handled at once, new
// non-critical requests are rejected with 503 so critical work still gets a turn.
type Shedder struct {
	maxInFlight int64
	inFlight    atomic.Int64
}

// NewShedder returns a Shedder that begins shedding non-critical requests once
// more than maxInFlight are in flight simultaneously.
func NewShedder(maxInFlight int) *Shedder {
	return &Shedder{maxInFlight: int64(maxInFlight)}
}

func (s *Shedder) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health/readiness probes are never shed.
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}

		n := s.inFlight.Add(1)
		defer s.inFlight.Add(-1)

		if n > s.maxInFlight && classifyPriority(r) == nonCritical {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusServiceUnavailable, "service overloaded, please retry")
			return
		}
		next.ServeHTTP(w, r)
	})
}
