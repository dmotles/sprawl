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

// MigrationStatus is one migration's version + applied-state, for the
// `sprawl hub migrate status` output. It is decoupled from goose's own type so
// the CLI does not import goose.
type MigrationStatus struct {
	Version int64
	Source  string
	Applied bool
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

// MigrateStatus returns the version/applied state of every known migration.
func MigrateStatus(ctx context.Context, dsn string) ([]MigrationStatus, error) {
	db, err := openSQL(dsn)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	p, err := newProvider(db)
	if err != nil {
		return nil, err
	}
	st, err := p.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: migration status: %w", err)
	}
	out := make([]MigrationStatus, 0, len(st))
	for _, s := range st {
		ms := MigrationStatus{Applied: s.State == goose.StateApplied}
		if s.Source != nil {
			ms.Version = s.Source.Version
			ms.Source = s.Source.Path
		}
		out = append(out, ms)
	}
	return out, nil
}
