package storage

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Akshats-git/PayCore/internal/ledger"
	"github.com/Akshats-git/PayCore/migrations"
)

// testPool connects to the database named by PAYCORE_TEST_DATABASE_URL, ensures
// the schema is migrated, and truncates the ledger tables so each test starts
// from a clean slate. If the env var is unset, the test is skipped — so a plain
// `go test ./...` with no database still passes, while CI (and a local run with
// the var set) exercises the real thing.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("PAYCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PAYCORE_TEST_DATABASE_URL not set; skipping integration test")
	}
	if err := RunMigrations(url, migrations.FS); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	pool, err := NewPostgresPool(context.Background(), url)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(context.Background(),
		`TRUNCATE accounts, transactions, ledger_entries, idempotency_keys, outbox RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

func TestAccountRepoCreateAndGet(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewAccountRepo(pool)

	created, err := repo.Create(ctx, "alice", ledger.Liability, "INR")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("expected a non-zero account id")
	}

	got, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != created {
		t.Fatalf("get returned %+v, want %+v", got, created)
	}
}

func TestAccountBalance(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	accounts := NewAccountRepo(pool)
	ledgerRepo := NewLedgerRepo(pool)

	from, _ := accounts.Create(ctx, "alice", ledger.Liability, "INR")
	to, _ := accounts.Create(ctx, "bob", ledger.Liability, "INR")

	// A brand-new account, with no entries, has a zero balance.
	if b, err := accounts.Balance(ctx, from.ID); err != nil || b != 0 {
		t.Fatalf("new account balance = %d (err %v), want 0", b, err)
	}

	// Move 500.00 from alice to bob.
	if _, err := ledgerRepo.Post(ctx, ledger.NewTransfer(ledger.Charge, from.ID, to.ID, 50000, "INR")); err != nil {
		t.Fatalf("post: %v", err)
	}

	// balance = credits − debits: the debited payer goes negative, the credited
	// payee goes positive, and together they net to zero.
	if b, _ := accounts.Balance(ctx, from.ID); b != -50000 {
		t.Fatalf("alice (payer) balance = %d, want -50000", b)
	}
	if b, _ := accounts.Balance(ctx, to.ID); b != 50000 {
		t.Fatalf("bob (payee) balance = %d, want 50000", b)
	}
}

func TestLedgerRepoPostWritesBalancedTransaction(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	accounts := NewAccountRepo(pool)
	ledgerRepo := NewLedgerRepo(pool)

	from, _ := accounts.Create(ctx, "alice", ledger.Liability, "INR")
	to, _ := accounts.Create(ctx, "bob", ledger.Liability, "INR")

	txID, err := ledgerRepo.Post(ctx, ledger.NewTransfer(ledger.Charge, from.ID, to.ID, 50000, "INR"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if txID == 0 {
		t.Fatal("expected a non-zero transaction id")
	}

	// The transaction's entries exist and they balance.
	var debits, credits int64
	err = pool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(amount) FILTER (WHERE direction = 'debit'), 0),
			COALESCE(SUM(amount) FILTER (WHERE direction = 'credit'), 0)
		FROM ledger_entries WHERE transaction_id = $1`, txID).Scan(&debits, &credits)
	if err != nil {
		t.Fatalf("sum entries: %v", err)
	}
	if debits != 50000 || credits != 50000 {
		t.Fatalf("debits=%d credits=%d, want 50000/50000", debits, credits)
	}
}

func TestLedgerRepoRejectsUnbalanced(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	accounts := NewAccountRepo(pool)
	ledgerRepo := NewLedgerRepo(pool)

	from, _ := accounts.Create(ctx, "alice", ledger.Liability, "INR")
	to, _ := accounts.Create(ctx, "bob", ledger.Liability, "INR")

	// Deliberately unbalanced: 100 out, 90 in. Validate() should reject it
	// before any database transaction is even opened.
	bad := ledger.Transaction{Kind: ledger.Charge, Entries: []ledger.Entry{
		{AccountID: from.ID, Direction: ledger.Debit, Amount: 100, Currency: "INR"},
		{AccountID: to.ID, Direction: ledger.Credit, Amount: 90, Currency: "INR"},
	}}
	_, err := ledgerRepo.Post(ctx, bad)
	if !errors.Is(err, ledger.ErrUnbalanced) {
		t.Fatalf("expected ErrUnbalanced, got %v", err)
	}
	assertTransactionCount(t, pool, 0)
}

func TestLedgerRepoAtomicRollback(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	accounts := NewAccountRepo(pool)
	ledgerRepo := NewLedgerRepo(pool)

	to, _ := accounts.Create(ctx, "bob", ledger.Liability, "INR")

	// Balanced and valid at the domain level, but the debit leg references an
	// account that doesn't exist. The transactions row inserts fine; the entry
	// insert then fails with a foreign-key violation. The whole transaction must
	// roll back, leaving NO transactions row behind — that's atomicity.
	const missingAccount = 999999
	_, err := ledgerRepo.Post(ctx, ledger.NewTransfer(ledger.Charge, missingAccount, to.ID, 100, "INR"))
	if err == nil {
		t.Fatal("expected a foreign-key error")
	}
	assertTransactionCount(t, pool, 0)
}

func TestDatabaseRejectsUnbalancedAtCommit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	accounts := NewAccountRepo(pool)
	from, _ := accounts.Create(ctx, "alice", ledger.Liability, "INR")
	to, _ := accounts.Create(ctx, "bob", ledger.Liability, "INR")

	// Bypass the app-level Validate entirely and write an unbalanced transaction
	// with raw SQL. The deferred trigger must reject it at COMMIT — proving the
	// invariant holds even against a buggy caller.
	dbtx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = dbtx.Rollback(ctx) }()

	var txID int64
	if err := dbtx.QueryRow(ctx,
		`INSERT INTO transactions (kind, status) VALUES ('charge', 'succeeded') RETURNING id`).Scan(&txID); err != nil {
		t.Fatalf("insert transaction: %v", err)
	}
	mustExec(t, ctx, dbtx,
		`INSERT INTO ledger_entries (transaction_id, account_id, direction, amount, currency) VALUES ($1, $2, 'debit', 100, 'INR')`, txID, from.ID)
	mustExec(t, ctx, dbtx,
		`INSERT INTO ledger_entries (transaction_id, account_id, direction, amount, currency) VALUES ($1, $2, 'credit', 90, 'INR')`, txID, to.ID)

	// Individual inserts succeeded; the imbalance is only caught at COMMIT.
	if err := dbtx.Commit(ctx); err == nil {
		t.Fatal("expected COMMIT to fail: the balance trigger should reject the unbalanced transaction")
	}
}

// --- helpers ---

func assertTransactionCount(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM transactions`).Scan(&n); err != nil {
		t.Fatalf("count transactions: %v", err)
	}
	if n != want {
		t.Fatalf("transactions count = %d, want %d", n, want)
	}
}

func mustExec(t *testing.T, ctx context.Context, tx pgx.Tx, sql string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}
