// Package charges is the application service for creating and reading charges.
// It sits between the HTTP layer and the storage/domain layers: a handler calls
// it with plain values and gets back a result, with no SQL leaking up and no
// HTTP routing leaking down.
package charges

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Akshats-git/PayCore/internal/ledger"
	"github.com/Akshats-git/PayCore/internal/storage"
)

// idempotencyTTL is how long a used key is remembered before it can be reclaimed.
const idempotencyTTL = 24 * time.Hour

// Charge is the application-level view of a completed charge: a single transfer
// of money from one account to another. Its JSON form is the API response.
type Charge struct {
	ID          int64        `json:"id"`
	FromAccount int64        `json:"from_account"`
	ToAccount   int64        `json:"to_account"`
	Amount      ledger.Money `json:"amount"`
	Currency    string       `json:"currency"`
	Status      string       `json:"status"`
	CreatedAt   time.Time    `json:"created_at"`
}

// CreateRequest carries the validated inputs for a new charge.
type CreateRequest struct {
	FromAccount int64
	ToAccount   int64
	Amount      ledger.Money
	Currency    string
}

// Sentinel errors let the HTTP layer map failures to the right status code
// without knowing anything about SQL.
var (
	ErrAccountNotFound = errors.New("charges: account not found")
	ErrChargeNotFound  = errors.New("charges: charge not found")
	ErrKeyConflict     = errors.New("charges: idempotency key reused with a different request")
	ErrKeyInProgress   = errors.New("charges: request with this idempotency key is still in progress")
)

// pgForeignKeyViolation is the SQLSTATE Postgres returns when an insert violates
// a foreign key — here, when a charge references an account that doesn't exist.
const pgForeignKeyViolation = "23503"

// EventChargeSucceeded is the outbox event type emitted when a charge is created.
const EventChargeSucceeded = "charge.succeeded"

// chargeEvent is the JSON envelope stored in the outbox and later delivered.
type chargeEvent struct {
	Type string `json:"type"`
	Data Charge `json:"data"`
}

// Service creates and reads charges.
type Service struct {
	pool        *pgxpool.Pool
	ledger      *storage.LedgerRepo
	idempotency *storage.IdempotencyRepo
	outbox      *storage.OutboxRepo
}

// NewService returns a charge Service.
func NewService(pool *pgxpool.Pool, ledgerRepo *storage.LedgerRepo, idempotencyRepo *storage.IdempotencyRepo, outboxRepo *storage.OutboxRepo) *Service {
	return &Service{pool: pool, ledger: ledgerRepo, idempotency: idempotencyRepo, outbox: outboxRepo}
}

// CreateIdempotent creates a charge under an idempotency key, or replays the
// stored response if the key was already used. It returns the HTTP status code
// and response body to send — identical on the first call and every retry.
//
// The claim, the charge, and the key-completion all run inside ONE database
// transaction, so a duplicate request that arrives while this one is mid-flight
// blocks on the claim's unique index and, once this commits, sees the completed
// key and replays its result. That is how 50 concurrent identical requests
// produce exactly one charge.
func (s *Service) CreateIdempotent(ctx context.Context, key string, rawBody []byte, req CreateRequest) (int, []byte, error) {
	hash := storage.HashRequest(rawBody)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	won, existing, err := s.idempotency.Claim(ctx, tx, key, hash, idempotencyTTL)
	if err != nil {
		return 0, nil, err
	}

	// We lost the claim: the key already exists.
	if !won {
		switch {
		case existing.RequestHash != hash:
			return 0, nil, ErrKeyConflict // same key, different request body
		case existing.Status == storage.IdempotencyInProgress:
			return 0, nil, ErrKeyInProgress
		default: // completed — replay the exact stored response
			return existing.ResponseCode, existing.ResponseBody, nil
		}
	}

	// We won the claim: create the charge in this same transaction.
	transfer := ledger.NewTransfer(ledger.Charge, req.FromAccount, req.ToAccount, req.Amount, req.Currency)
	txID, createdAt, err := s.ledger.PostTx(ctx, tx, transfer)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
			return 0, nil, ErrAccountNotFound
		}
		return 0, nil, err
	}

	charge := Charge{
		ID:          txID,
		FromAccount: req.FromAccount,
		ToAccount:   req.ToAccount,
		Amount:      req.Amount,
		Currency:    req.Currency,
		Status:      "succeeded",
		CreatedAt:   createdAt,
	}
	body, err := json.Marshal(charge)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal charge: %w", err)
	}

	// Emit a webhook event in this SAME transaction (transactional outbox): the
	// event and the charge commit together, so a charge can never exist without
	// its event, nor an event without its charge.
	event, err := json.Marshal(chargeEvent{Type: EventChargeSucceeded, Data: charge})
	if err != nil {
		return 0, nil, fmt.Errorf("marshal event: %w", err)
	}
	if _, err := s.outbox.Enqueue(ctx, tx, EventChargeSucceeded, event); err != nil {
		return 0, nil, err
	}

	// Store the response so retries replay these exact bytes, then commit the
	// claim, the charge, the event, and the completion together.
	if err := s.idempotency.Complete(ctx, tx, key, http.StatusCreated, body, &txID); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, nil, fmt.Errorf("commit: %w", err)
	}
	return http.StatusCreated, body, nil
}

// Create posts a charge without idempotency (used internally and in tests).
func (s *Service) Create(ctx context.Context, fromAccount, toAccount int64, amount ledger.Money, currency string) (Charge, error) {
	txID, err := s.ledger.Post(ctx, ledger.NewTransfer(ledger.Charge, fromAccount, toAccount, amount, currency))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
			return Charge{}, ErrAccountNotFound
		}
		return Charge{}, err
	}
	return s.Get(ctx, txID)
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
