package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Outbox event statuses.
const (
	OutboxPending   = "pending"
	OutboxDelivered = "delivered"
	OutboxDead      = "dead"
)

// OutboxEvent is a queued outbound event awaiting delivery.
type OutboxEvent struct {
	ID        int64
	EventType string
	Payload   []byte
	Attempts  int
	CreatedAt time.Time
}

// OutboxRepo stores and retrieves outbound events.
type OutboxRepo struct {
	pool *pgxpool.Pool
}

// NewOutboxRepo returns an OutboxRepo backed by the given pool.
func NewOutboxRepo(pool *pgxpool.Pool) *OutboxRepo {
	return &OutboxRepo{pool: pool}
}

// Enqueue writes an event to the outbox using q, so it can join the caller's
// transaction. This is the transactional outbox pattern: the event and the state
// change that produced it (the charge) commit together, or not at all — no lost
// events, no phantom events.
func (r *OutboxRepo) Enqueue(ctx context.Context, q Querier, eventType string, payload []byte) (int64, error) {
	var id int64
	err := q.QueryRow(ctx,
		`INSERT INTO outbox (event_type, payload) VALUES ($1, $2) RETURNING id`,
		eventType, payload,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("enqueue outbox event: %w", err)
	}
	return id, nil
}

// ClaimDue atomically claims up to `limit` due, pending events for delivery. It
// leases them by pushing next_attempt_at `leaseFor` into the future, so a second
// worker (or a retry after this worker crashes mid-delivery) won't pick up the
// same event until the lease expires. `FOR UPDATE SKIP LOCKED` lets multiple
// workers claim disjoint batches without blocking each other.
func (r *OutboxRepo) ClaimDue(ctx context.Context, limit int, leaseFor time.Duration) ([]OutboxEvent, error) {
	leaseUntil := time.Now().Add(leaseFor)
	rows, err := r.pool.Query(ctx, `
		UPDATE outbox SET next_attempt_at = $2
		WHERE id IN (
			SELECT id FROM outbox
			WHERE status = 'pending' AND next_attempt_at <= now()
			ORDER BY next_attempt_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, event_type, payload, attempts, created_at`,
		limit, leaseUntil,
	)
	if err != nil {
		return nil, fmt.Errorf("claim due outbox events: %w", err)
	}
	defer rows.Close()

	var events []OutboxEvent
	for rows.Next() {
		var e OutboxEvent
		if err := rows.Scan(&e.ID, &e.EventType, &e.Payload, &e.Attempts, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan outbox event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// MarkDelivered marks an event as successfully delivered.
func (r *OutboxRepo) MarkDelivered(ctx context.Context, id int64) error {
	if _, err := r.pool.Exec(ctx,
		`UPDATE outbox SET status = 'delivered' WHERE id = $1`, id); err != nil {
		return fmt.Errorf("mark delivered: %w", err)
	}
	return nil
}

// Reschedule records a failed attempt and sets when the event is next due.
func (r *OutboxRepo) Reschedule(ctx context.Context, id int64, attempts int, nextAttempt time.Time) error {
	if _, err := r.pool.Exec(ctx,
		`UPDATE outbox SET attempts = $2, next_attempt_at = $3 WHERE id = $1`,
		id, attempts, nextAttempt); err != nil {
		return fmt.Errorf("reschedule outbox event: %w", err)
	}
	return nil
}

// MarkDead moves an event to the dead-letter state after too many failures.
func (r *OutboxRepo) MarkDead(ctx context.Context, id int64, attempts int) error {
	if _, err := r.pool.Exec(ctx,
		`UPDATE outbox SET status = 'dead', attempts = $2 WHERE id = $1`,
		id, attempts); err != nil {
		return fmt.Errorf("mark dead: %w", err)
	}
	return nil
}
