package risk

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
)

// fraudModelJSON is the trained model artifact, compiled into the binary. It is
// produced offline by scripts/train_fraud_model.py; regenerate it by running
// that script. Because it's embedded, serving needs no Python and no file I/O at
// runtime — the model is just data the Go scorer interprets.
//
//go:embed model/fraud_model.json
var fraudModelJSON []byte

// modelArtifact is the on-disk shape written by the training script. Go and
// Python agree on this contract; nothing else crosses the language boundary.
type modelArtifact struct {
	Version        int       `json:"version"`
	HomeCurrency   string    `json:"home_currency"`
	Features       []string  `json:"features"`
	Mean           []float64 `json:"mean"`
	Scale          []float64 `json:"scale"`
	Coef           []float64 `json:"coef"`
	Intercept      float64   `json:"intercept"`
	BlockThreshold float64   `json:"block_threshold"`
	Metrics        struct {
		AUC float64 `json:"auc"`
	} `json:"metrics"`
}

// ModelScorer serves the trained logistic-regression fraud model. It implements
// Scorer, so it drops straight into the same Guard (latency budget + circuit
// breaker + fail-open) that already protected the placeholder RuleScorer —
// nothing else in the system changes.
type ModelScorer struct {
	art modelArtifact
}

// NewModelScorer loads the embedded model artifact. It returns an error only if
// the artifact is malformed, which a test catches at build time.
func NewModelScorer() (*ModelScorer, error) {
	return newModelScorerFrom(fraudModelJSON)
}

func newModelScorerFrom(raw []byte) (*ModelScorer, error) {
	var art modelArtifact
	if err := json.Unmarshal(raw, &art); err != nil {
		return nil, fmt.Errorf("risk: parse model artifact: %w", err)
	}
	n := len(art.Features)
	if n == 0 || len(art.Mean) != n || len(art.Scale) != n || len(art.Coef) != n {
		return nil, fmt.Errorf("risk: model artifact is inconsistent: %d features but mean/scale/coef are %d/%d/%d",
			n, len(art.Mean), len(art.Scale), len(art.Coef))
	}
	for i, s := range art.Scale {
		if s == 0 {
			return nil, fmt.Errorf("risk: model artifact has zero scale for feature %q", art.Features[i])
		}
	}
	return &ModelScorer{art: art}, nil
}

// features builds the raw feature vector for a charge. This MUST match
// make_features() in the training script exactly, or serving-time inputs won't
// match training-time ones.
func (m *ModelScorer) features(req Request) []float64 {
	logAmount := math.Log1p(float64(req.Amount))
	isForeign := 0.0
	if req.Currency != m.art.HomeCurrency {
		isForeign = 1.0
	}
	return []float64{logAmount, isForeign, logAmount * isForeign}
}

// probability standardizes the features, applies the logistic model, and returns
// P(fraud) in [0,1].
func (m *ModelScorer) probability(req Request) float64 {
	x := m.features(req)
	logit := m.art.Intercept
	for i, xi := range x {
		z := (xi - m.art.Mean[i]) / m.art.Scale[i]
		logit += m.art.Coef[i] * z
	}
	return 1.0 / (1.0 + math.Exp(-logit))
}

// Score implements Scorer. It is pure CPU with no I/O, so it comfortably fits the
// Guard's latency budget — but it's still called through the Guard, so if a
// future model version does I/O and turns slow, the budget and breaker already
// protect the charge path.
func (m *ModelScorer) Score(_ context.Context, req Request) (Decision, error) {
	p := m.probability(req)
	score := int(math.Round(p * 100))
	if p >= m.art.BlockThreshold {
		return Decision{Score: score, Action: ActionBlock, Reason: "model risk score above threshold"}, nil
	}
	return Decision{Score: score, Action: ActionAllow, Reason: "model risk score within tolerance"}, nil
}
