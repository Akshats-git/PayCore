// Package config loads PayCore's runtime configuration from the environment,
// following the 12-factor principle that configuration lives in the environment
// rather than being hardcoded, so the same binary runs identically on a laptop
// and in a container.
package config

import (
	"os"
	"strconv"
)

// Config holds all runtime configuration for the service.
type Config struct {
	// Addr is the TCP address the HTTP server listens on, e.g. ":8080".
	Addr string
	// DatabaseURL is the Postgres connection string.
	DatabaseURL string
	// RedisURL is the Redis connection string.
	RedisURL string

	// RateLimitCapacity is the per-client token-bucket size (burst).
	RateLimitCapacity int
	// RateLimitRefillPerSec is how fast each bucket refills, in tokens/second.
	RateLimitRefillPerSec float64
	// LoadShedMaxInFlight is the number of concurrent in-flight requests above
	// which non-critical requests start being shed.
	LoadShedMaxInFlight int
}

// Load reads configuration from the environment, applying local-dev defaults so
// that `docker compose up` followed by `go run ./cmd/server` works with no extra
// setup. The defaults match the credentials in docker-compose.yml.
func Load() Config {
	return Config{
		Addr:        getenv("PAYCORE_ADDR", ":8080"),
		DatabaseURL: getenv("PAYCORE_DATABASE_URL", "postgres://paycore:paycore@localhost:5433/paycore?sslmode=disable"),
		RedisURL:    getenv("PAYCORE_REDIS_URL", "redis://localhost:6379/0"),

		RateLimitCapacity:     getenvInt("PAYCORE_RATE_LIMIT_CAPACITY", 20),
		RateLimitRefillPerSec: getenvFloat("PAYCORE_RATE_LIMIT_REFILL_PER_SEC", 10),
		LoadShedMaxInFlight:   getenvInt("PAYCORE_LOAD_SHED_MAX_INFLIGHT", 100),
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

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getenvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}
