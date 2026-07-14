package risk

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Guard evaluates charges through a Scorer while protecting the charge path with
// three mechanisms:
//
//   - a strict latency budget: the scorer gets at most Budget to answer, so a
//     slow model can never stall a charge;
//   - a circuit breaker: repeated slow or failed scores trip it open, and while
//     open the scorer is not called at all (fail fast) — no point hammering a
//     sick dependency;
//   - graceful degradation: any timeout, error, or open breaker yields a
//     fail-open fallback decision, so risk scoring can never be the reason a
//     legitimate charge is delayed or rejected.
//
// Assess therefore never returns an error: the caller always gets a Decision.
type Guard struct {
	scorer  Scorer
	breaker *Breaker
	budget  time.Duration
	logger  *slog.Logger
}

// GuardConfig configures a Guard. Zero values fall back to sensible defaults.
type GuardConfig struct {
	// Budget is the latency budget for a single score call.
	Budget time.Duration
	// BreakerThreshold is how many consecutive failures trip the breaker.
	BreakerThreshold int
	// BreakerCooldown is how long the breaker stays open before a trial call.
	BreakerCooldown time.Duration
}

// NewGuard builds a Guard around scorer, applying defaults for any unset config.
func NewGuard(scorer Scorer, logger *slog.Logger, cfg GuardConfig) *Guard {
	if cfg.Budget <= 0 {
		cfg.Budget = 50 * time.Millisecond
	}
	if cfg.BreakerThreshold <= 0 {
		cfg.BreakerThreshold = 5
	}
	if cfg.BreakerCooldown <= 0 {
		cfg.BreakerCooldown = 5 * time.Second
	}
	return &Guard{
		scorer:  scorer,
		breaker: NewBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown),
		budget:  cfg.Budget,
		logger:  logger,
	}
}

// Assess scores req and always returns a Decision. On the happy path it returns
// the scorer's verdict. On any timeout, error, or open breaker it logs and
// returns a degraded fail-open decision — the charge proceeds.
func (g *Guard) Assess(ctx context.Context, req Request) Decision {
	var dec Decision
	err := g.breaker.Do(func() error {
		// Give the scorer only the latency budget, regardless of how much time
		// the parent request context still has. A slow scorer hits this deadline
		// and returns ctx.Err(), which the breaker counts as a failure.
		callCtx, cancel := context.WithTimeout(ctx, g.budget)
		defer cancel()

		d, scoreErr := g.scorer.Score(callCtx, req)
		if scoreErr != nil {
			return scoreErr
		}
		dec = d
		return nil
	})
	if err != nil {
		g.logger.Warn("risk scoring degraded; allowing charge",
			"err", err, "breaker", g.breaker.State())
		return fallbackDecision(err)
	}
	return dec
}

// fallbackDecision is the fail-open verdict returned when scoring is unavailable.
// Blocking a charge because the risk service is down would let a scorer outage
// take payments down with it; a payment processor accepts the residual risk of a
// few unscored charges instead. (A stricter policy could fail CLOSED above some
// amount; that is a business call, deliberately left as fail-open here.)
func fallbackDecision(err error) Decision {
	reason := "risk scoring unavailable"
	switch {
	case errors.Is(err, ErrOpen):
		reason = "risk scoring circuit open"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "risk scoring timed out"
	}
	return Decision{Score: -1, Action: ActionAllow, Reason: reason, Degraded: true}
}
