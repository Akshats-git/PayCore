package charges

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
		`TRUNCATE accounts, transactions, ledger_entries, idempotency_keys, outbox RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	svc := NewService(pool, storage.NewLedgerRepo(pool), storage.NewIdempotencyRepo(pool), storage.NewOutboxRepo(pool))
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

// TestCreateIdempotentCrashRecovery proves the durability half of idempotency:
// if a charge's transaction dies before COMMIT (a crash, a dropped connection),
// the claim rolls back TOGETHER WITH the charge — because they share one
// transaction — so no zombie "in_progress" key is left to block future retries.
// A retry with the same key then creates exactly one charge.
func TestCreateIdempotentCrashRecovery(t *testing.T) {
	svc, accounts := testService(t)
	ctx := context.Background()

	from, _ := accounts.Create(ctx, "alice", ledger.Liability, "INR")
	to, _ := accounts.Create(ctx, "bob", ledger.Liability, "INR")

	const key = "crash-key"
	body := []byte(`{"from_account":1,"to_account":2,"amount":50000,"currency":"INR"}`)
	req := CreateRequest{FromAccount: from.ID, ToAccount: to.ID, Amount: 50000, Currency: "INR"}

	// Simulate a charge that dies before commit: claim the key and write the
	// charge inside a transaction, then roll back instead of committing — exactly
	// what Postgres does to an uncommitted transaction when a process crashes.
	tx, err := svc.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	won, _, err := svc.idempotency.Claim(ctx, tx, key, storage.HashRequest(body), time.Hour)
	if err != nil || !won {
		t.Fatalf("claim inside doomed tx: won=%v err=%v", won, err)
	}
	if _, _, err := svc.ledger.PostTx(ctx, tx, ledger.NewTransfer(ledger.Charge, from.ID, to.ID, 50000, "INR")); err != nil {
		t.Fatalf("post inside doomed tx: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil { // the "crash": no commit ever happens
		t.Fatalf("rollback: %v", err)
	}

	// The crash left no trace — the claim and the charge both rolled back.
	if n := transactionCount(t, ctx, svc.pool); n != 0 {
		t.Fatalf("after crash, transactions = %d, want 0 (everything rolled back)", n)
	}

	// Retry with the same key. Because no key survived, this claims fresh and
	// creates the charge — and only one.
	code, _, err := svc.CreateIdempotent(ctx, key, body, req)
	if err != nil || code != 201 {
		t.Fatalf("retry after crash: code=%d err=%v", code, err)
	}
	if n := transactionCount(t, ctx, svc.pool); n != 1 {
		t.Fatalf("after retry, transactions = %d, want exactly 1", n)
	}
	if bal, _ := accounts.Balance(ctx, to.ID); bal != 50000 {
		t.Fatalf("payee balance = %d, want 50000 (exactly one charge)", bal)
	}
}

func transactionCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM transactions`).Scan(&n); err != nil {
		t.Fatalf("count transactions: %v", err)
	}
	return n
}

func TestCreateIdempotentWritesOutboxEvent(t *testing.T) {
	svc, accounts := testService(t)
	ctx := context.Background()

	from, _ := accounts.Create(ctx, "alice", ledger.Liability, "INR")
	to, _ := accounts.Create(ctx, "bob", ledger.Liability, "INR")

	body := []byte(`{"from_account":1,"to_account":2,"amount":50000,"currency":"INR"}`)
	req := CreateRequest{FromAccount: from.ID, ToAccount: to.ID, Amount: 50000, Currency: "INR"}

	if _, _, err := svc.CreateIdempotent(ctx, "k-outbox", body, req); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Exactly one pending event of the right type was written — in the same
	// transaction as the charge.
	var (
		count      int
		eventType  string
		status     string
		payloadRaw []byte
	)
	if err := svc.pool.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("outbox events = %d, want 1", count)
	}
	if err := svc.pool.QueryRow(ctx,
		`SELECT event_type, status, payload FROM outbox LIMIT 1`).Scan(&eventType, &status, &payloadRaw); err != nil {
		t.Fatal(err)
	}
	if eventType != EventChargeSucceeded || status != storage.OutboxPending {
		t.Fatalf("event_type=%q status=%q, want %q/pending", eventType, status, EventChargeSucceeded)
	}

	var env chargeEvent
	if err := json.Unmarshal(payloadRaw, &env); err != nil {
		t.Fatalf("payload is not valid event JSON: %v", err)
	}
	if env.Type != EventChargeSucceeded || env.Data.Amount != 50000 {
		t.Fatalf("payload envelope = %+v, want type %q amount 50000", env, EventChargeSucceeded)
	}

	// A replay of the same key must NOT enqueue a second event.
	if _, _, err := svc.CreateIdempotent(ctx, "k-outbox", body, req); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if err := svc.pool.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("after replay, outbox events = %d, want still 1 (no duplicate)", count)
	}
}
