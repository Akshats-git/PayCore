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
// skips when PAYCORE_TEST_DATABASE_URL is unset, so `go test ./...` stays green
// without a database.
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
	return NewService(storage.NewLedgerRepo(pool)), storage.NewAccountRepo(pool)
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
	if created.Amount != 50000 || created.Currency != "INR" {
		t.Fatalf("amount/currency = %d/%s, want 50000/INR", created.Amount, created.Currency)
	}

	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != created.ID || got.FromAccount != from.ID || got.ToAccount != to.ID || got.Amount != 50000 {
		t.Fatalf("get returned %+v, want id/from/to/amount %d/%d/%d/50000", got, created.ID, from.ID, to.ID)
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
