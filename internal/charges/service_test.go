package charges

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Akshats-git/PayCore/internal/ledger"
	"github.com/Akshats-git/PayCore/internal/storage"
	"github.com/Akshats-git/PayCore/migrations"
)

// testService wires a real Service and AccountRepo to the test database. It
// skips when PAYCORE_TEST_DATABASE_URL is unset.
func testService(t *testing.T) (*Service, *storage.AccountRepo) {
	t.Helper()
	url := os.Getenv("PAYCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PAYCORE_TEST_DATABASE_URL not set; skipping integration test")
	}
	if err := storage.RunMigrations(url, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := storage.NewPostgresPool(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(),
		`TRUNCATE accounts, transactions, ledger_entries, idempotency_keys RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	svc := NewService(pool, storage.NewLedgerRepo(pool), storage.NewIdempotencyRepo(pool))
	return svc, storage.NewAccountRepo(pool)
}

func TestServiceCreateAndGet(t *testing.T) {
	svc, accounts := testService(t)
	ctx := context.Background()

	from, _ := accounts.Create(ctx, "alice", ledger.Liability, "INR")
	to, _ := accounts.Create(ctx, "bob", ledger.Liability, "INR")

	created, err := svc.Create(ctx, from.ID, to.ID, 50000, "INR")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.FromAccount != from.ID || created.ToAccount != to.ID {
		t.Fatalf("from/to = %d/%d, want %d/%d", created.FromAccount, created.ToAccount, from.ID, to.ID)
	}

	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != created.ID || got.Amount != 50000 {
		t.Fatalf("get returned %+v", got)
	}
}

func TestServiceAccountNotFound(t *testing.T) {
	svc, _ := testService(t)
	_, err := svc.Create(context.Background(), 111, 222, 100, "INR")
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("expected ErrAccountNotFound, got %v", err)
	}
}

func TestServiceChargeNotFound(t *testing.T) {
	svc, _ := testService(t)
	_, err := svc.Get(context.Background(), 99999)
	if !errors.Is(err, ErrChargeNotFound) {
		t.Fatalf("expected ErrChargeNotFound, got %v", err)
	}
}

func TestCreateIdempotentCreateThenReplay(t *testing.T) {
	svc, accounts := testService(t)
	ctx := context.Background()

	from, _ := accounts.Create(ctx, "alice", ledger.Liability, "INR")
	to, _ := accounts.Create(ctx, "bob", ledger.Liability, "INR")

	body := []byte(`{"from_account":1,"to_account":2,"amount":50000,"currency":"INR"}`)
	req := CreateRequest{FromAccount: from.ID, ToAccount: to.ID, Amount: 50000, Currency: "INR"}

	code1, resp1, err := svc.CreateIdempotent(ctx, "key-1", body, req)
	if err != nil || code1 != 201 {
		t.Fatalf("first call: code=%d err=%v", code1, err)
	}

	// Same key + same body → replay the exact same response, no new charge.
	code2, resp2, err := svc.CreateIdempotent(ctx, "key-1", body, req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if code2 != code1 || string(resp2) != string(resp1) {
		t.Fatalf("replay mismatch: (%d,%s) vs (%d,%s)", code2, resp2, code1, resp1)
	}

	// Exactly one charge happened: bob received 500.00 once, not twice.
	if bal, _ := accounts.Balance(ctx, to.ID); bal != 50000 {
		t.Fatalf("payee balance = %d, want 50000 (exactly one charge)", bal)
	}
}

func TestCreateIdempotentSameKeyDifferentBody(t *testing.T) {
	svc, accounts := testService(t)
	ctx := context.Background()

	from, _ := accounts.Create(ctx, "alice", ledger.Liability, "INR")
	to, _ := accounts.Create(ctx, "bob", ledger.Liability, "INR")
	req := CreateRequest{FromAccount: from.ID, ToAccount: to.ID, Amount: 50000, Currency: "INR"}

	if _, _, err := svc.CreateIdempotent(ctx, "key-2", []byte(`{"v":1}`), req); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, _, err := svc.CreateIdempotent(ctx, "key-2", []byte(`{"v":2}`), req)
	if !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("expected ErrKeyConflict for a reused key with a different body, got %v", err)
	}
}
