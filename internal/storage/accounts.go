package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Akshats-git/PayCore/internal/ledger"
)

// AccountRepo reads and writes accounts. It is the storage-layer adapter for the
// pure ledger.Account domain type.
type AccountRepo struct {
	pool *pgxpool.Pool
}

// NewAccountRepo returns an AccountRepo backed by the given connection pool.
func NewAccountRepo(pool *pgxpool.Pool) *AccountRepo {
	return &AccountRepo{pool: pool}
}

// Create inserts a new account and returns it with its database-assigned ID.
func (r *AccountRepo) Create(ctx context.Context, name string, accountType ledger.AccountType, currency string) (ledger.Account, error) {
	var (
		a   ledger.Account
		typ string
	)
	err := r.pool.QueryRow(ctx,
		`INSERT INTO accounts (name, type, currency) VALUES ($1, $2, $3)
		 RETURNING id, name, type, currency`,
		name, string(accountType), currency,
	).Scan(&a.ID, &a.Name, &typ, &a.Currency)
	if err != nil {
		return ledger.Account{}, fmt.Errorf("create account: %w", err)
	}
	a.Type = ledger.AccountType(typ)
	return a, nil
}

// Get fetches one account by ID. If it does not exist, the returned error wraps
// pgx.ErrNoRows.
func (r *AccountRepo) Get(ctx context.Context, id int64) (ledger.Account, error) {
	var (
		a   ledger.Account
		typ string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, name, type, currency FROM accounts WHERE id = $1`, id,
	).Scan(&a.ID, &a.Name, &typ, &a.Currency)
	if err != nil {
		return ledger.Account{}, fmt.Errorf("get account %d: %w", id, err)
	}
	a.Type = ledger.AccountType(typ)
	return a, nil
}

// Balance computes an account's balance by summing its ledger entries. The
// balance is always DERIVED here — never read from a stored column — so it can
// never silently drift out of agreement with the entries that produced it.
//
// The convention: balance = credits − debits. Crediting an account increases its
// balance (value arriving); debiting decreases it (value leaving). PayCore models
// user balances as liabilities, so a customer who is charged goes down and a
// merchant who is paid goes up.
func (r *AccountRepo) Balance(ctx context.Context, id int64) (ledger.Money, error) {
	var balance int64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount) FILTER (WHERE direction = 'credit'), 0)
		     - COALESCE(SUM(amount) FILTER (WHERE direction = 'debit'), 0)
		FROM ledger_entries WHERE account_id = $1`, id).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("compute balance for account %d: %w", id, err)
	}
	return ledger.Money(balance), nil
}
