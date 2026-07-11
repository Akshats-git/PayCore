// Command server starts the PayCore HTTP service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Akshats-git/PayCore/internal/config"
	"github.com/Akshats-git/PayCore/internal/httpapi"
	"github.com/Akshats-git/PayCore/internal/storage"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// All real work happens in run(). main() stays tiny so that run() can use
	// defer for cleanup — deferred calls do NOT execute if you call os.Exit,
	// so os.Exit lives only here, after run() has returned and its defers ran.
	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg := config.Load()

	// Open backing stores. If either is unreachable at startup, fail fast: a
	// payment service with no database is not something to limp along with.
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer startupCancel()

	pool, err := storage.NewPostgresPool(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	logger.Info("connected to postgres")

	rdb, err := storage.NewRedisClient(startupCtx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()
	logger.Info("connected to redis")

	// Readiness probe: both stores must answer a ping for the service to be
	// considered ready to serve traffic.
	ready := func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("postgres: %w", err)
		}
		if err := rdb.Ping(ctx).Err(); err != nil {
			return fmt.Errorf("redis: %w", err)
		}
		return nil
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.NewRouter(logger, ready),
		ReadHeaderTimeout: 5 * time.Second, // basic protection against slow-header attacks
	}

	// Start the listener in its own goroutine so we can block on OS signals
	// below. A fatal listen error is delivered back on serverErr.
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server starting", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// Wait for either a fatal server error or a shutdown signal (Ctrl-C, or the
	// SIGTERM that `docker stop` / Kubernetes send).
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		return fmt.Errorf("server: %w", err)
	case <-stop:
		logger.Info("shutdown signal received, draining connections")
	}

	// Give in-flight requests up to 10s to finish. For a payment service this
	// matters: a routine deploy must never be the reason a charge is left
	// half-written.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("server stopped cleanly")
	return nil
}
