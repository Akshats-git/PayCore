package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// RunMigrations applies all pending "up" migrations from fsys (an embedded
// filesystem of *.sql files) to the database at databaseURL.
//
// It is safe to call on every startup: golang-migrate records which versions
// have already run in a schema_migrations table and applies only what's new. It
// also takes a database lock while migrating, so if several instances of the
// service start at the same time, only one runs the migration and the others
// wait — no racing, duplicate DDL.
func RunMigrations(databaseURL string, fsys fs.FS) error {
	// A short-lived *sql.DB just for migrations, separate from the pgxpool the
	// app serves requests with. The "pgx" driver is registered by the blank
	// import above and accepts the same postgres:// URL.
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open db for migrations: %w", err)
	}
	defer db.Close()

	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		return fmt.Errorf("migration driver: %w", err)
	}

	src, err := iofs.New(fsys, ".")
	if err != nil {
		return fmt.Errorf("migration source: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx", driver)
	if err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}

	// ErrNoChange just means the database is already at the latest version,
	// which is the normal case on most startups — not an error for us.
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}
