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

// Post validates a transaction and writes it — the transactions row together
// with all of its ledger_entries — inside a SINGLE database transaction. Either
// everything commits or nothing does. This is the guarantee that a charge can
// never be left half-written: if the process dies, the network drops, or any
// single insert fails, Postgres rolls the whole thing back and the ledger is
// exactly as it was before.
//
// It returns the new transaction's ID.
func (r *LedgerRepo) Post(ctx context.Context, t ledger.Transaction) (int64, error) {
	// Guard at the door: never even open a database transaction for something
	// the domain rules already reject.
	if err := t.Validate(); err != nil {
		return 0, fmt.Errorf("invalid transaction: %w", err)
	}

	dbtx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	// Rollback is a no-op once Commit has succeeded, so this safely undoes
	// everything on any early return below (all of which are error paths).
	defer func() { _ = dbtx.Rollback(ctx) }()

	var txID int64
	err = dbtx.QueryRow(ctx,
		`INSERT INTO transactions (kind, status) VALUES ($1, 'succeeded') RETURNING id`,
		string(t.Kind),
	).Scan(&txID)
	if err != nil {
		return 0, fmt.Errorf("insert transaction: %w", err)
	}

	for _, e := range t.Entries {
		if _, err := dbtx.Exec(ctx,
			`INSERT INTO ledger_entries (transaction_id, account_id, direction, amount, currency)
			 VALUES ($1, $2, $3, $4, $5)`,
			txID, e.AccountID, string(e.Direction), int64(e.Amount), e.Currency,
		); err != nil {
			return 0, fmt.Errorf("insert entry for account %d: %w", e.AccountID, err)
		}
	}

	// The deferred balance trigger (migration 0002) runs here, at COMMIT, and
	// will reject the transaction if the entries somehow don't balance.
	if err := dbtx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return txID, nil
}

// GetTransaction fetches one transaction and its entries by ID. If no such
// transaction exists it returns an error wrapping pgx.ErrNoRows, so callers can
// distinguish "not found" from a real failure with errors.Is.
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
