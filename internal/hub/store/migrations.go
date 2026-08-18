package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationsSub is the embedded migrations dir rooted so goose sees the .sql
// files directly.
func migrationsSub() (fs.FS, error) {
	return fs.Sub(migrationsFS, "migrations")
}

// newProvider builds a goose provider over db using the embedded migrations.
// goose runs on database/sql; pgStore opens a throwaway *sql.DB (pgx stdlib
// driver) for migration and status, separate from its pgxpool for queries.
func newProvider(db *sql.DB) (*goose.Provider, error) {
	sub, err := migrationsSub()
	if err != nil {
		return nil, fmt.Errorf("store: embed migrations: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		return nil, fmt.Errorf("store: build goose provider: %w", err)
	}
	return p, nil
}

// Migrate applies all pending migrations against dsn and returns nil on
// success. Used by both pgStore.Migrate and the `sprawl hub migrate` command.
func Migrate(ctx context.Context, dsn string) error {
	db, err := openSQL(dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	p, err := newProvider(db)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("store: apply migrations: %w", err)
	}
	return nil
}
