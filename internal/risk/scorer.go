// Package risk scores a charge for fraud BEFORE it is posted. The point of this
// package is not the model — increment 18 supplies a real one — but the systems
// discipline around calling it: a scorer runs inline on the charge path, so it
// must answer within a strict latency budget, and if it is slow or down a
// circuit breaker trips and the path degrades gracefully to a fallback. Risk
// scoring must NEVER be the reason a legitimate charge is delayed or rejected.
package risk

import (
	"context"

	"github.com/Akshats-git/PayCore/internal/ledger"
)

// Action is the risk engine's verdict on a charge.
type Action string

const (
	// ActionAllow lets the charge proceed.
	ActionAllow Action = "allow"
	// ActionBlock rejects the charge as too risky.
	ActionBlock Action = "block"
)

// Request is the minimal set of features the scorer evaluates. It is
// deliberately small: increment 18 swaps the Scorer implementation for a trained
// model without changing this contract.
type Request struct {
	FromAccount int64
	ToAccount   int64
	Amount      ledger.Money
	Currency    string
}

// Decision is the scorer's verdict plus context for logging and the API.
type Decision struct {
	// Score is a 0..100 risk score, higher meaning riskier. A degraded
	// decision carries a Score of -1: no real score was computed.
	Score int
	// Action is what to do with the charge.
	Action Action
	// Reason is a short human-readable explanation, surfaced on a block.
	Reason string
	// Degraded is true when this is a fallback decision produced because the
	// scorer timed out, errored, or the breaker was open — not a real score.
	Degraded bool
}

// Scorer evaluates a charge's fraud risk. Implementations MUST honor ctx
// cancellation/deadline: the caller imposes a strict latency budget and expects
// a slow scorer to return ctx.Err() promptly rather than run to completion.
type Scorer interface {
	Score(ctx context.Context, req Request) (Decision, error)
}

// RuleScorer is a deterministic heuristic that stands in for a trained model. It
// scores on transaction amount alone — larger charges are treated as riskier —
// and blocks anything at or above BlockAtOrAbove. Increment 18 replaces it with
// a real model behind this same Scorer interface; nothing else in the system
// changes.
type RuleScorer struct {
	// BlockAtOrAbove is the amount (minor units) at or above which a charge is
	// blocked outright. Zero disables blocking.
	BlockAtOrAbove ledger.Money
	// RiskUnit maps amount to a 0..100 score: score = min(100, amount/RiskUnit).
	// Zero falls back to a sane default so the scorer never divides by zero.
	RiskUnit ledger.Money
}

// Score implements Scorer. It ignores ctx because it does no I/O and is
// effectively instant — but a real model implementation would respect it.
func (s RuleScorer) Score(_ context.Context, req Request) (Decision, error) {
	unit := s.RiskUnit
	if unit <= 0 {
		unit = 1000 // default: one risk point per 10.00 of the minor unit
	}
	score := int(req.Amount / unit)
	if score > 100 {
		score = 100
	}
	if s.BlockAtOrAbove > 0 && req.Amount >= s.BlockAtOrAbove {
		return Decision{Score: score, Action: ActionBlock, Reason: "amount exceeds risk threshold"}, nil
	}
	return Decision{Score: score, Action: ActionAllow, Reason: "within risk tolerance"}, nil
}
