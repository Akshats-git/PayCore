package storage

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/Akshats-git/PayCore/internal/ledger"
)

// TestSystemWideInvariant is the headline correctness test for Phase 1. It posts
// a long, random sequence of charges and refunds between random accounts, then
// asserts that across the ENTIRE ledger the debits still equal the credits — and
// equivalently, that every account balance sums to exactly zero. In a
// double-entry system money is only ever moved, never created or destroyed, and
// this proves it end-to-end against the real database.
func TestSystemWideInvariant(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	accountRepo := NewAccountRepo(pool)
	ledgerRepo := NewLedgerRepo(pool)

	const numAccounts = 6
	ids := make([]int64, numAccounts)
	for i := range ids {
		a, err := accountRepo.Create(ctx, fmt.Sprintf("acct-%d", i), ledger.Liability, "INR")
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		ids[i] = a.ID
	}

	rng := rand.New(rand.NewSource(7))
	const attempts = 300
	posted := 0
	for i := 0; i < attempts; i++ {
		from := ids[rng.Intn(numAccounts)]
		to := ids[rng.Intn(numAccounts)]
		if from == to {
			continue
		}
		amount := ledger.Money(rng.Int63n(100_000) + 1)
		kind := ledger.Charge
		if rng.Intn(2) == 0 {
			kind = ledger.Refund
		}
		if _, err := ledgerRepo.Post(ctx, ledger.NewTransfer(kind, from, to, amount, "INR")); err != nil {
			t.Fatalf("post transaction %d: %v", i, err)
		}
		posted++
	}

	// Across the whole ledger, debits must equal credits.
	var debits, credits int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount) FILTER (WHERE direction = 'debit'), 0),
		       COALESCE(SUM(amount) FILTER (WHERE direction = 'credit'), 0)
		FROM ledger_entries`).Scan(&debits, &credits); err != nil {
		t.Fatalf("sum entries: %v", err)
	}
	if debits != credits {
		t.Fatalf("LEDGER OUT OF BALANCE after %d transactions: debits=%d credits=%d", posted, debits, credits)
	}

	// Equivalently: the sum of every account's balance is exactly zero.
	var totalBalance int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN direction = 'credit' THEN amount ELSE -amount END), 0)
		FROM ledger_entries`).Scan(&totalBalance); err != nil {
		t.Fatalf("sum balances: %v", err)
	}
	if totalBalance != 0 {
		t.Fatalf("sum of all account balances = %d, want 0", totalBalance)
	}

	t.Logf("%d transactions posted; ledger balanced (debits = credits = %d); all balances sum to 0", posted, debits)
}
