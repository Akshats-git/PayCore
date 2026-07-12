// Package charges is the application service for creating and reading charges.
// It sits between the HTTP layer and the storage/domain layers: a handler calls
// it with plain values and gets back a Charge, with no SQL leaking up and no
// HTTP concerns leaking down.
package charges

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Akshats-git/PayCore/internal/ledger"
	"github.com/Akshats-git/PayCore/internal/storage"
)

// Charge is the application-level view of a completed charge: a single transfer
// of money from one account to another. It is reconstructed from the underlying
// double-entry transaction (a debit leg + a credit leg).
type Charge struct {
	ID          int64
	FromAccount int64
	ToAccount   int64
	Amount      ledger.Money
	Currency    string
	Status      string
	CreatedAt   time.Time
}

// Sentinel errors let the HTTP layer map failures to the right status code
// without knowing anything about SQL.
var (
	ErrAccountNotFound = errors.New("charges: account not found")
	ErrChargeNotFound  = errors.New("charges: charge not found")
)

// pgForeignKeyViolation is the SQLSTATE Postgres returns when an insert violates
// a foreign key — here, when a charge references an account that doesn't exist.
const pgForeignKeyViolation = "23503"

// Service creates and reads charges.
type Service struct {
	ledger *storage.LedgerRepo
}

// NewService returns a charge Service backed by the given ledger repository.
func NewService(ledgerRepo *storage.LedgerRepo) *Service {
	return &Service{ledger: ledgerRepo}
}

// Create posts a charge that moves amount from fromAccount to toAccount, then
// returns the resulting Charge.
func (s *Service) Create(ctx context.Context, fromAccount, toAccount int64, amount ledger.Money, currency string) (Charge, error) {
	tx := ledger.NewTransfer(ledger.Charge, fromAccount, toAccount, amount, currency)

	id, err := s.ledger.Post(ctx, tx)
	if err != nil {
		// A foreign-key violation means one of the accounts doesn't exist —
		// that's a client error, not a server fault.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
			return Charge{}, ErrAccountNotFound
		}
		return Charge{}, err
	}
	return s.Get(ctx, id)
}

// Get reconstructs a charge from its stored transaction and entries.
func (s *Service) Get(ctx context.Context, id int64) (Charge, error) {
	stored, err := s.ledger.GetTransaction(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Charge{}, ErrChargeNotFound
		}
		return Charge{}, err
	}

	// A charge is a two-leg transfer: the debit leg is the source of the money,
	// the credit leg is the destination.
	c := Charge{ID: stored.ID, Status: stored.Status, CreatedAt: stored.CreatedAt}
	for _, e := range stored.Entries {
		c.Amount = e.Amount
		c.Currency = e.Currency
		switch e.Direction {
		case ledger.Debit:
			c.FromAccount = e.AccountID
		case ledger.Credit:
			c.ToAccount = e.AccountID
		}
	}
	return c, nil
}
