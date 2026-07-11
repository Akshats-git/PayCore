// Package config loads PayCore's runtime configuration from the environment,
// following the 12-factor principle that configuration lives in the environment
// rather than being hardcoded, so the same binary runs identically on a laptop
// and in a container.
package config

import "os"

// Config holds all runtime configuration for the service.
type Config struct {
	// Addr is the TCP address the HTTP server listens on, e.g. ":8080".
	Addr string
	// DatabaseURL is the Postgres connection string.
	DatabaseURL string
	// RedisURL is the Redis connection string.
	RedisURL string
}

// Load reads configuration from the environment, applying local-dev defaults so
// that `docker compose up` followed by `go run ./cmd/server` works with no extra
// setup. The defaults match the credentials in docker-compose.yml.
func Load() Config {
	return Config{
		Addr:        getenv("PAYCORE_ADDR", ":8080"),
		DatabaseURL: getenv("PAYCORE_DATABASE_URL", "postgres://paycore:paycore@localhost:5433/paycore?sslmode=disable"),
		RedisURL:    getenv("PAYCORE_REDIS_URL", "redis://localhost:6379/0"),
	}
}

// getenv returns the value of the environment variable named by key, or fallback
// if it is unset or empty.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
