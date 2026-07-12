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

// Get fetches one account by ID.
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
