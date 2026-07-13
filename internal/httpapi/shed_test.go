package httpapi

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestShedderAllowsWhenNotSaturated(t *testing.T) {
	h := NewShedder(5).middleware(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/charges/1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (well under the limit)", rec.Code)
	}
}

func TestShedderExemptsNonV1(t *testing.T) {
	// Even with a zero limit (shed every non-critical /v1), non-/v1 paths pass.
	h := NewShedder(0).middleware(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (non-/v1 is exempt)", rec.Code)
	}
}

// TestShedderShedsNonCriticalButKeepsCritical holds the in-flight slots full
// with slow non-critical requests, then proves a further non-critical request is
// shed (503) while a critical one still gets through. Coordination is via
// channels, so the test is deterministic — no sleeps.
func TestShedderShedsNonCriticalButKeepsCritical(t *testing.T) {
	const limit = 2
	s := NewShedder(limit)

	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	h := s.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))

	// Occupy both in-flight slots with slow non-critical requests.
	var wg sync.WaitGroup
	for i := 0; i < limit; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/charges/1", nil))
		}()
	}
	for i := 0; i < limit; i++ {
		<-entered // both are now inside the handler; in-flight == limit
	}

	// A further non-critical request is shed with 503.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/charges/1", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("non-critical request under load: status %d, want 503", rec.Code)
	}

	// But a critical request (creating a charge) still gets through.
	critRec := httptest.NewRecorder()
	critDone := make(chan struct{})
	go func() {
		h.ServeHTTP(critRec, httptest.NewRequest(http.MethodPost, "/v1/charges", nil))
		close(critDone)
	}()
	select {
	case <-entered: // it reached the handler — it was not shed
	case <-time.After(2 * time.Second):
		t.Fatal("critical request did not get through under load")
	}

	close(release)
	wg.Wait()
	<-critDone
	if critRec.Code != http.StatusOK {
		t.Fatalf("critical request: status %d, want 200", critRec.Code)
	}
}
