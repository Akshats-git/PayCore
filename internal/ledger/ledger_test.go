package ledger

import (
	"errors"
	"math/rand"
	"testing"
)

func TestNewTransferIsBalanced(t *testing.T) {
	tx := NewTransfer(Charge, 1, 2, 50000, "INR")
	if err := tx.Validate(); err != nil {
		t.Fatalf("NewTransfer should always be valid, got: %v", err)
	}
	if len(tx.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(tx.Entries))
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		tx      Transaction
		wantErr error // nil means "should be valid"
	}{
		{
			name: "balanced two-entry transfer",
			tx:   NewTransfer(Charge, 1, 2, 100, "INR"),
		},
		{
			name: "balanced split: one debit, two credits",
			tx: Transaction{Kind: Charge, Entries: []Entry{
				{AccountID: 1, Direction: Debit, Amount: 100, Currency: "INR"},
				{AccountID: 2, Direction: Credit, Amount: 70, Currency: "INR"},
				{AccountID: 3, Direction: Credit, Amount: 30, Currency: "INR"},
			}},
		},
		{
			name: "unbalanced by 10",
			tx: Transaction{Kind: Charge, Entries: []Entry{
				{AccountID: 1, Direction: Debit, Amount: 100, Currency: "INR"},
				{AccountID: 2, Direction: Credit, Amount: 90, Currency: "INR"},
			}},
			wantErr: ErrUnbalanced,
		},
		{
			name: "too few entries",
			tx: Transaction{Kind: Charge, Entries: []Entry{
				{AccountID: 1, Direction: Debit, Amount: 100, Currency: "INR"},
			}},
			wantErr: ErrTooFewEntries,
		},
		{
			name: "non-positive amount",
			tx: Transaction{Kind: Charge, Entries: []Entry{
				{AccountID: 1, Direction: Debit, Amount: 0, Currency: "INR"},
				{AccountID: 2, Direction: Credit, Amount: 0, Currency: "INR"},
			}},
			wantErr: ErrNonPositiveAmount,
		},
		{
			name: "mixed currency",
			tx: Transaction{Kind: Charge, Entries: []Entry{
				{AccountID: 1, Direction: Debit, Amount: 100, Currency: "INR"},
				{AccountID: 2, Direction: Credit, Amount: 100, Currency: "USD"},
			}},
			wantErr: ErrMixedCurrency,
		},
		{
			name: "invalid direction",
			tx: Transaction{Kind: Charge, Entries: []Entry{
				{AccountID: 1, Direction: "sideways", Amount: 100, Currency: "INR"},
				{AccountID: 2, Direction: Credit, Amount: 100, Currency: "INR"},
			}},
			wantErr: ErrInvalidDirection,
		},
		{
			name: "invalid kind",
			tx: Transaction{Kind: "gift", Entries: []Entry{
				{AccountID: 1, Direction: Debit, Amount: 100, Currency: "INR"},
				{AccountID: 2, Direction: Credit, Amount: 100, Currency: "INR"},
			}},
			wantErr: ErrInvalidKind,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tx.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected valid, got error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected error %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestValidateBalanceProperty is a property test: it generates a thousand
// randomly-shaped but genuinely balanced transactions (one debit, split across a
// random number of credits that sum to the same total) and asserts every one of
// them validates. It's a small preview of the "prove it, don't claim it" testing
// style that peaks with the concurrency test in Phase 2.
func TestValidateBalanceProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 1000; i++ {
		total := Money(rng.Int63n(1_000_000) + 1)

		entries := []Entry{{AccountID: 1, Direction: Debit, Amount: total, Currency: "INR"}}
		remaining := total
		accountID := int64(2)
		for remaining > 0 {
			part := Money(rng.Int63n(int64(remaining)) + 1)
			entries = append(entries, Entry{AccountID: accountID, Direction: Credit, Amount: part, Currency: "INR"})
			remaining -= part
			accountID++
		}

		tx := Transaction{Kind: Charge, Entries: entries}
		if err := tx.Validate(); err != nil {
			t.Fatalf("balanced transaction #%d (total=%d) should validate, got: %v", i, total, err)
		}
	}
}
