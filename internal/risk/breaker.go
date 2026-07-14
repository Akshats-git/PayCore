package risk

import (
	"errors"
	"sync"
	"time"
)

// ErrOpen is returned by the breaker when it is open and short-circuiting calls
// rather than letting them reach a scorer that is known to be unhealthy.
var ErrOpen = errors.New("risk: circuit breaker open")

// state is the circuit breaker's current mode.
type state int

const (
	stateClosed   state = iota // normal: calls pass through
	stateOpen                  // tripped: calls fail fast without touching the scorer
	stateHalfOpen              // cooling down elapsed: one trial call is allowed
)

func (s state) String() string {
	switch s {
	case stateOpen:
		return "open"
	case stateHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// Breaker is a three-state circuit breaker. After `threshold` consecutive
// failures it trips OPEN and fails fast for `cooldown`; once the cooldown
// elapses it goes HALF-OPEN and admits a single trial call — which closes the
// breaker on success or reopens it on failure. It is safe for concurrent use.
type Breaker struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time // injectable clock for deterministic tests

	mu       sync.Mutex
	state    state
	failures int
	openedAt time.Time
	probing  bool // a half-open trial call is currently in flight
}

// NewBreaker returns a closed breaker that trips after `threshold` consecutive
// failures and stays open for `cooldown`.
func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{
		threshold: threshold,
		cooldown:  cooldown,
		now:       time.Now,
		state:     stateClosed,
	}
}

// Do runs fn unless the breaker is open, in which case it returns ErrOpen
// without calling fn at all. It records fn's success (nil error) or failure to
// drive the state transitions.
func (b *Breaker) Do(fn func() error) error {
	if !b.beforeCall() {
		return ErrOpen
	}
	err := fn()
	b.afterCall(err == nil)
	return err
}

// beforeCall decides whether a call may proceed, transitioning OPEN → HALF-OPEN
// once the cooldown has elapsed and admitting exactly one trial while half-open.
func (b *Breaker) beforeCall() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case stateOpen:
		if b.now().Sub(b.openedAt) < b.cooldown {
			return false // still cooling down: fail fast
		}
		// Cooldown elapsed: admit a single trial call.
		b.state = stateHalfOpen
		b.probing = true
		return true
	case stateHalfOpen:
		if b.probing {
			return false // a trial is already in flight; don't pile on
		}
		b.probing = true
		return true
	default: // stateClosed
		return true
	}
}

// afterCall records the outcome and moves the breaker between states.
func (b *Breaker) afterCall(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == stateHalfOpen {
		b.probing = false
		if success {
			b.state = stateClosed
			b.failures = 0
		} else {
			b.state = stateOpen
			b.openedAt = b.now()
		}
		return
	}

	// stateClosed.
	if success {
		b.failures = 0
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.state = stateOpen
		b.openedAt = b.now()
	}
}

// State returns the breaker's current state as a string, for logging.
func (b *Breaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state.String()
}
