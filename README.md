# PayCore

A mini payment processing engine that moves money between accounts **correctly under concurrency, crashes, and load** — the hard problems real payment infrastructure is built to survive.

The goal is *provable* correctness of money movement, not feature count.

## Documents

- **[SPEC.md](SPEC.md)** — the full design: data model, API surface, the idempotency mechanism, and the phased build plan.
- **[BLOG.md](BLOG.md)** — an incremental build log. Every part is explained in detail as it is built, one committable slice at a time.

## Run locally

```bash
go run ./cmd/server
# in another terminal:
curl -s localhost:8080/healthz
# => {"status":"ok"}
```

## Test

```bash
go test ./...
```

## Stack

Go · PostgreSQL · Redis · Docker Compose · GitHub Actions
