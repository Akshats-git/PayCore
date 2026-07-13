package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the subset of pgx methods shared by *pgxpool.Pool and pgx.Tx.
// Taking a Querier lets a repository method run either directly on the pool or —
// crucially for idempotency — inside a caller's database transaction, so the
// claim, the charge, and the completion all commit atomically together.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Idempotency key states.
const (
	IdempotencyInProgress = "in_progress"
	IdempotencyCompleted  = "completed"
)

// IdempotencyRecord is a stored idempotency key: the request that claimed it and,
// once the request finishes, the response to replay for any retry.
type IdempotencyRecord struct {
	Key           string
	RequestHash   string
	Status        string
	ResponseCode  int
	ResponseBody  []byte
	TransactionID int64
}

// IdempotencyRepo stores and retrieves idempotency keys.
type IdempotencyRepo struct {
	pool *pgxpool.Pool
}

// NewIdempotencyRepo returns an IdempotencyRepo backed by the given pool.
func NewIdempotencyRepo(pool *pgxpool.Pool) *IdempotencyRepo {
	return &IdempotencyRepo{pool: pool}
}

// HashRequest returns the SHA-256 (hex) of a request body. Reusing an
// idempotency key with a *different* body is a client mistake; comparing this
// hash on a retry is how we detect it.
func HashRequest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Claim attempts to take ownership of an idempotency key. It is the atomic heart
// of the whole feature: a single `INSERT ... ON CONFLICT DO NOTHING` picks one
// winner even when many identical requests arrive at the same instant.
//
//   - If this call inserted the row, it won: returns won=true.
//   - If the key already existed, it lost: returns won=false and the existing
//     record (which may still be in_progress, or already completed).
func (r *IdempotencyRepo) Claim(ctx context.Context, q Querier, key, requestHash string, ttl time.Duration) (won bool, existing IdempotencyRecord, err error) {
	var claimedKey string
	err = q.QueryRow(ctx,
		`INSERT INTO idempotency_keys (key, request_hash, status, expires_at)
		 VALUES ($1, $2, 'in_progress', $3)
		 ON CONFLICT (key) DO NOTHING
		 RETURNING key`,
		key, requestHash, time.Now().Add(ttl),
	).Scan(&claimedKey)

	switch {
	case err == nil:
		// Our INSERT succeeded — we own the key.
		return true, IdempotencyRecord{}, nil
	case errors.Is(err, pgx.ErrNoRows):
		// ON CONFLICT DO NOTHING inserted nothing: the key already exists.
		existing, err = r.read(ctx, q, key)
		return false, existing, err
	default:
		return false, IdempotencyRecord{}, fmt.Errorf("claim idempotency key: %w", err)
	}
}

// Complete records the final response for a key so later retries can replay it.
// Pass transactionID for the charge that was created, or nil if there was none.
func (r *IdempotencyRepo) Complete(ctx context.Context, q Querier, key string, code int, body []byte, transactionID *int64) error {
	_, err := q.Exec(ctx,
		`UPDATE idempotency_keys
		 SET status = 'completed', response_code = $2, response_body = $3, transaction_id = $4
		 WHERE key = $1`,
		key, code, body, transactionID,
	)
	if err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	return nil
}

// read fetches a stored key. Callers reach it through Claim.
func (r *IdempotencyRepo) read(ctx context.Context, q Querier, key string) (IdempotencyRecord, error) {
	var (
		rec  IdempotencyRecord
		code *int32 // nullable until the request completes
		body []byte
		txID *int64
	)
	err := q.QueryRow(ctx,
		`SELECT key, request_hash, status, response_code, response_body, transaction_id
		 FROM idempotency_keys WHERE key = $1`, key,
	).Scan(&rec.Key, &rec.RequestHash, &rec.Status, &code, &body, &txID)
	if err != nil {
		return IdempotencyRecord{}, fmt.Errorf("read idempotency key: %w", err)
	}
	if code != nil {
		rec.ResponseCode = int(*code)
	}
	rec.ResponseBody = body
	if txID != nil {
		rec.TransactionID = *txID
	}
	return rec, nil
}
