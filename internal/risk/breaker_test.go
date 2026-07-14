package risk

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock so breaker timing is deterministic.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var errBoom = errors.New("boom")

func fail() error { return errBoom }
func ok() error   { return nil }

func TestBreakerTripsAfterThreshold(t *testing.T) {
	b := NewBreaker(3, time.Second)

	// Two failures: still closed, calls still reach fn.
	for i := 0; i < 2; i++ {
		if err := b.Do(fail); !errors.Is(err, errBoom) {
			t.Fatalf("call %d: err = %v, want boom (breaker still closed)", i, err)
		}
	}
	if b.State() != "closed" {
		t.Fatalf("state = %s, want closed", b.State())
	}

	// Third consecutive failure trips it.
	if err := b.Do(fail); !errors.Is(err, errBoom) {
		t.Fatalf("third call: err = %v, want boom", err)
	}
	if b.State() != "open" {
		t.Fatalf("state = %s, want open after threshold failures", b.State())
	}

	// While open, fn is not called and ErrOpen comes back immediately.
	called := false
	err := b.Do(func() error { called = true; return nil })
	if !errors.Is(err, ErrOpen) {
		t.Fatalf("open call: err = %v, want ErrOpen", err)
	}
	if called {
		t.Fatal("fn was called while breaker was open; it must fail fast")
	}
}

func TestBreakerSuccessResetsFailureCount(t *testing.T) {
	b := NewBreaker(3, time.Second)
	_ = b.Do(fail)
	_ = b.Do(fail)
	_ = b.Do(ok) // resets the streak
	_ = b.Do(fail)
	_ = b.Do(fail)
	if b.State() != "closed" {
		t.Fatalf("state = %s, want closed (success broke the failure streak)", b.State())
	}
}

func TestBreakerHalfOpenRecovers(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	b := NewBreaker(1, 5*time.Second)
	b.now = clock.Now

	_ = b.Do(fail) // trips open immediately (threshold 1)
	if b.State() != "open" {
		t.Fatalf("state = %s, want open", b.State())
	}

	// Before cooldown elapses, still fails fast.
	clock.Advance(4 * time.Second)
	if err := b.Do(ok); !errors.Is(err, ErrOpen) {
		t.Fatalf("within cooldown: err = %v, want ErrOpen", err)
	}

	// After cooldown, a trial call is admitted; success closes the breaker.
	clock.Advance(2 * time.Second)
	if err := b.Do(ok); err != nil {
		t.Fatalf("trial call: err = %v, want nil", err)
	}
	if b.State() != "closed" {
		t.Fatalf("state = %s, want closed after a successful trial", b.State())
	}
}

func TestBreakerHalfOpenReopensOnFailure(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	b := NewBreaker(1, 5*time.Second)
	b.now = clock.Now

	_ = b.Do(fail) // open
	clock.Advance(6 * time.Second)

	// The trial call fails: back to open, and the cooldown restarts.
	if err := b.Do(fail); !errors.Is(err, errBoom) {
		t.Fatalf("trial call: err = %v, want boom", err)
	}
	if b.State() != "open" {
		t.Fatalf("state = %s, want open after a failed trial", b.State())
	}
	if err := b.Do(ok); !errors.Is(err, ErrOpen) {
		t.Fatalf("after failed trial: err = %v, want ErrOpen (cooldown restarted)", err)
	}
}
