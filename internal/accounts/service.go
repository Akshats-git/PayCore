// Package accounts is the application service for creating and reading accounts,
// including their derived balances. Like the charges service, it sits between
// the HTTP layer and storage, translating storage errors into typed sentinels.
package accounts

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/Akshats-git/PayCore/internal/ledger"
	"github.com/Akshats-git/PayCore/internal/storage"
)

// Account is the application-level view of an account, including its balance
// (always derived from ledger entries, never stored).
type Account struct {
	ID       int64
	Name     string
	Type     ledger.AccountType
	Currency string
	Balance  ledger.Money
}

// ErrNotFound is returned when an account ID does not exist.
var ErrNotFound = errors.New("accounts: account not found")

// Service creates and reads accounts.
type Service struct {
	repo *storage.AccountRepo
}

// NewService returns an account Service backed by the given repository.
func NewService(repo *storage.AccountRepo) *Service {
	return &Service{repo: repo}
}

// Create makes a new account. A brand-new account has no entries, so its balance
// is zero.
func (s *Service) Create(ctx context.Context, name string, accountType ledger.AccountType, currency string) (Account, error) {
	a, err := s.repo.Create(ctx, name, accountType, currency)
	if err != nil {
		return Account{}, err
	}
	return Account{ID: a.ID, Name: a.Name, Type: a.Type, Currency: a.Currency, Balance: 0}, nil
}

// Get returns an account together with its current derived balance.
func (s *Service) Get(ctx context.Context, id int64) (Account, error) {
	a, err := s.repo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Account{}, ErrNotFound
		}
		return Account{}, err
	}
	balance, err := s.repo.Balance(ctx, id)
	if err != nil {
		return Account{}, err
	}
	return Account{ID: a.ID, Name: a.Name, Type: a.Type, Currency: a.Currency, Balance: balance}, nil
}
