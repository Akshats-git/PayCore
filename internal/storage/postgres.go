// Package storage holds the adapters that connect PayCore to its backing
// stores: PostgreSQL (the source of truth for the ledger) and Redis (fast
// atomic operations for rate limiting later).
package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPostgresPool opens a connection pool to Postgres and verifies connectivity
// with a ping before returning.
//
// A pool — rather than a single connection — is what a concurrent HTTP server
// needs: each in-flight request borrows a connection from the pool and returns
// it when done, so N concurrent requests can talk to the database at once
// without stepping on each other.
func NewPostgresPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 10               // cap concurrent DB connections
	cfg.MaxConnLifetime = time.Hour // recycle connections periodically

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}
