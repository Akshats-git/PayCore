package risk

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// funcScorer adapts a function to the Scorer interface for tests.
type funcScorer func(context.Context, Request) (Decision, error)

func (f funcScorer) Score(ctx context.Context, r Request) (Decision, error) { return f(ctx, r) }

func TestRuleScorerBlocksAtThreshold(t *testing.T) {
	s := RuleScorer{BlockAtOrAbove: 100000, RiskUnit: 1000}

	if d, _ := s.Score(context.Background(), Request{Amount: 50000}); d.Action != ActionAllow {
		t.Fatalf("50000: action = %s, want allow", d.Action)
	}
	if d, _ := s.Score(context.Background(), Request{Amount: 100000}); d.Action != ActionBlock {
		t.Fatalf("100000: action = %s, want block", d.Action)
	}
}

func TestGuardAllowsWhenScorerHealthy(t *testing.T) {
	scorer := funcScorer(func(context.Context, Request) (Decision, error) {
		return Decision{Score: 12, Action: ActionAllow, Reason: "ok"}, nil
	})
	g := NewGuard(scorer, discardLogger(), GuardConfig{Budget: time.Second})

	d := g.Assess(context.Background(), Request{Amount: 500})
	if d.Action != ActionAllow || d.Degraded || d.Score != 12 {
		t.Fatalf("decision = %+v, want allow/score 12/not degraded", d)
	}
}

func TestGuardDegradesOnTimeout(t *testing.T) {
	// A scorer that never answers within the budget: it blocks until ctx is done.
	scorer := funcScorer(func(ctx context.Context, _ Request) (Decision, error) {
		<-ctx.Done()
		return Decision{}, ctx.Err()
	})
	g := NewGuard(scorer, discardLogger(), GuardConfig{Budget: 10 * time.Millisecond})

	start := time.Now()
	d := g.Assess(context.Background(), Request{Amount: 500})
	elapsed := time.Since(start)

	if !d.Degraded || d.Action != ActionAllow {
		t.Fatalf("decision = %+v, want degraded fail-open allow", d)
	}
	if d.Reason != "risk scoring timed out" {
		t.Fatalf("reason = %q, want timeout reason", d.Reason)
	}
	// The budget bounded the wait: nowhere near a full second.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("Assess took %v; the latency budget should have bounded it", elapsed)
	}
}

func TestGuardTripsBreakerAndFailsFast(t *testing.T) {
	var calls int32
	scorer := funcScorer(func(context.Context, Request) (Decision, error) {
		atomic.AddInt32(&calls, 1)
		return Decision{}, errors.New("scorer down")
	})
	// Threshold 3, generous budget so failures are real errors, not timeouts.
	g := NewGuard(scorer, discardLogger(), GuardConfig{Budget: time.Second, BreakerThreshold: 3})

	// Three failing calls trip the breaker; each degrades to fail-open.
	for i := 0; i < 3; i++ {
		if d := g.Assess(context.Background(), Request{Amount: 500}); !d.Degraded {
			t.Fatalf("call %d: decision = %+v, want degraded", i, d)
		}
	}
	// A fourth call must fail fast WITHOUT touching the scorer.
	d := g.Assess(context.Background(), Request{Amount: 500})
	if !d.Degraded || d.Reason != "risk scoring circuit open" {
		t.Fatalf("decision = %+v, want degraded/circuit open", d)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("scorer called %d times, want 3 (breaker should have short-circuited the 4th)", got)
	}
}

func TestGuardRecoversAfterCooldown(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	scorer := funcScorer(func(context.Context, Request) (Decision, error) {
		if down.Load() {
			return Decision{}, errors.New("down")
		}
		return Decision{Score: 7, Action: ActionAllow}, nil
	})

	clock := &fakeClock{t: time.Now()}
	g := NewGuard(scorer, discardLogger(), GuardConfig{Budget: time.Second, BreakerThreshold: 1, BreakerCooldown: 5 * time.Second})
	g.breaker.now = clock.Now

	// One failure trips the breaker open.
	if d := g.Assess(context.Background(), Request{Amount: 500}); !d.Degraded {
		t.Fatalf("first call: decision = %+v, want degraded", d)
	}
	if g.breaker.State() != "open" {
		t.Fatalf("breaker state = %s, want open", g.breaker.State())
	}

	// The scorer recovers; after the cooldown a trial call closes the breaker.
	down.Store(false)
	clock.Advance(6 * time.Second)
	d := g.Assess(context.Background(), Request{Amount: 500})
	if d.Degraded || d.Action != ActionAllow || d.Score != 7 {
		t.Fatalf("recovered decision = %+v, want real allow/score 7", d)
	}
	if g.breaker.State() != "closed" {
		t.Fatalf("breaker state = %s, want closed after recovery", g.breaker.State())
	}
}
