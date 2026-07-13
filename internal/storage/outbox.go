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
