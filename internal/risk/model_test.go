package risk

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Akshats-git/PayCore/internal/ledger"
)

// The embedded model must satisfy the Scorer interface at compile time.
var _ Scorer = (*ModelScorer)(nil)

func mustModel(t *testing.T) *ModelScorer {
	t.Helper()
	m, err := NewModelScorer()
	if err != nil {
		t.Fatalf("load embedded model: %v", err)
	}
	return m
}

// TestModelMatchesTrainingSamples is the parity test: the Go scorer must produce
// the same probabilities the Python training script did for the same inputs. If
// the feature engineering or math drifts between the two languages, this breaks.
// Expected values are those the script prints for these charges.
func TestModelMatchesTrainingSamples(t *testing.T) {
	m := mustModel(t)
	cases := []struct {
		name     string
		amount   int64
		currency string
		wantProb float64
	}{
		{"500 domestic", 50_000, "INR", 0.269},
		{"2000 domestic", 200_000, "INR", 0.443},
		{"500 foreign", 50_000, "USD", 0.795},
		{"3000 foreign", 300_000, "USD", 0.897},
	}
	for _, c := range cases {
		got := m.probability(Request{Amount: ledger.Money(c.amount), Currency: c.currency})
		if math.Abs(got-c.wantProb) > 0.005 {
			t.Errorf("%s: probability = %.4f, want ~%.3f (Go/Python parity)", c.name, got, c.wantProb)
		}
	}
}

// TestModelForeignBeatsAmount proves the model learned something the old
// amount-only heuristic could not express: a small FOREIGN charge scores riskier
// than a large DOMESTIC one.
func TestModelForeignBeatsAmount(t *testing.T) {
	m := mustModel(t)
	smallForeign := m.probability(Request{Amount: 50_000, Currency: "USD"})
	largeDomestic := m.probability(Request{Amount: 200_000, Currency: "INR"})
	if smallForeign <= largeDomestic {
		t.Fatalf("small foreign (%.3f) should be riskier than large domestic (%.3f)", smallForeign, largeDomestic)
	}
}

func TestModelBlocksAndAllows(t *testing.T) {
	m := mustModel(t)

	if d, _ := m.Score(context.Background(), Request{Amount: 50_000, Currency: "INR"}); d.Action != ActionAllow {
		t.Errorf("500 domestic: action = %s, want allow", d.Action)
	}
	if d, _ := m.Score(context.Background(), Request{Amount: 300_000, Currency: "USD"}); d.Action != ActionBlock {
		t.Errorf("3000 foreign: action = %s, want block", d.Action)
	}
}

// TestModelThroughGuard confirms the trained model drops into the exact same
// Guard that protected the placeholder scorer, with no other change.
func TestModelThroughGuard(t *testing.T) {
	g := NewGuard(mustModel(t), discardLogger(), GuardConfig{Budget: time.Second})
	d := g.Assess(context.Background(), Request{Amount: 300_000, Currency: "USD"})
	if d.Degraded || d.Action != ActionBlock {
		t.Fatalf("decision = %+v, want a real block through the guard", d)
	}
}

func TestNewModelScorerRejectsInconsistentArtifact(t *testing.T) {
	// features says 2, but mean/scale/coef are length 1 → must be rejected.
	bad := []byte(`{"features":["a","b"],"mean":[0],"scale":[1],"coef":[0.5],"intercept":0}`)
	if _, err := newModelScorerFrom(bad); err == nil {
		t.Fatal("expected an error for an inconsistent artifact, got nil")
	}
}
