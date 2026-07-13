package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Akshats-git/PayCore/internal/ledger"
)

// LedgerRepo writes and reads financial transactions in the ledger.
type LedgerRepo struct {
	pool *pgxpool.Pool
}

// NewLedgerRepo returns a LedgerRepo backed by the given connection pool.
func NewLedgerRepo(pool *pgxpool.Pool) *LedgerRepo {
	return &LedgerRepo{pool: pool}
}

// StoredTransaction is a transaction as it exists in the database: the
// transactions row plus all of its ledger entries.
type StoredTransaction struct {
	ID        int64
	Kind      ledger.Kind
	Status    string
	CreatedAt time.Time
	Entries   []ledger.Entry
}

// PostTx validates a transaction and writes it — the transactions row and all of
// its entries — using q, which may be the pool or, for an idempotent charge, a
// caller's already-open transaction. It does NOT begin or commit; that is the
// caller's responsibility. It returns the new transaction's ID and creation time.
//
// Taking a Querier is what lets the idempotency claim, the charge, and the
// key-completion all happen inside one transaction and commit together.
func (r *LedgerRepo) PostTx(ctx context.Context, q Querier, t ledger.Transaction) (int64, time.Time, error) {
	if err := t.Validate(); err != nil {
		return 0, time.Time{}, fmt.Errorf("invalid transaction: %w", err)
	}

	var (
		id        int64
		createdAt time.Time
	)
	if err := q.QueryRow(ctx,
		`INSERT INTO transactions (kind, status) VALUES ($1, 'succeeded') RETURNING id, created_at`,
		string(t.Kind),
	).Scan(&id, &createdAt); err != nil {
		return 0, time.Time{}, fmt.Errorf("insert transaction: %w", err)
	}

	for _, e := range t.Entries {
		if _, err := q.Exec(ctx,
			`INSERT INTO ledger_entries (transaction_id, account_id, direction, amount, currency)
			 VALUES ($1, $2, $3, $4, $5)`,
			id, e.AccountID, string(e.Direction), int64(e.Amount), e.Currency,
		); err != nil {
			return 0, time.Time{}, fmt.Errorf("insert entry for account %d: %w", e.AccountID, err)
		}
	}
	return id, createdAt, nil
}

// Post writes a transaction atomically in its own database transaction: begin,
// insert everything, commit. Either all of it lands or none does. Use this for
// the non-idempotent path; the idempotent charge path calls PostTx inside a
// transaction it manages itself.
func (r *LedgerRepo) Post(ctx context.Context, t ledger.Transaction) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	// Rollback is a no-op once Commit has succeeded.
	defer func() { _ = tx.Rollback(ctx) }()

	id, _, err := r.PostTx(ctx, tx, t)
	if err != nil {
		return 0, err
	}

	// The deferred balance trigger (migration 0002) runs here, at COMMIT.
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return id, nil
}

// GetTransaction fetches one transaction and its entries by ID. If no such
// transaction exists it returns an error wrapping pgx.ErrNoRows.
func (r *LedgerRepo) GetTransaction(ctx context.Context, id int64) (StoredTransaction, error) {
	var (
		st           StoredTransaction
		kind, status string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, kind, status, created_at FROM transactions WHERE id = $1`, id,
	).Scan(&st.ID, &kind, &status, &st.CreatedAt)
	if err != nil {
		return StoredTransaction{}, fmt.Errorf("get transaction %d: %w", id, err)
	}
	st.Kind = ledger.Kind(kind)
	st.Status = status

	rows, err := r.pool.Query(ctx,
		`SELECT account_id, direction, amount, currency
		 FROM ledger_entries WHERE transaction_id = $1 ORDER BY id`, id)
	if err != nil {
		return StoredTransaction{}, fmt.Errorf("get entries for %d: %w", id, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			e   ledger.Entry
			dir string
			amt int64
		)
		if err := rows.Scan(&e.AccountID, &dir, &amt, &e.Currency); err != nil {
			return StoredTransaction{}, fmt.Errorf("scan entry: %w", err)
		}
		e.Direction = ledger.Direction(dir)
		e.Amount = ledger.Money(amt)
		st.Entries = append(st.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return StoredTransaction{}, fmt.Errorf("iterate entries: %w", err)
	}
	return st, nil
}
