package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for goose
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationsSub roots the embedded FS so goose sees the .sql files directly.
func migrationsSub() (fs.FS, error) {
	return fs.Sub(migrationsFS, "migrations")
}

// Migrate applies all pending event-log migrations against dsn.
//
// goose runs on database/sql, so this opens a throwaway *sql.DB via the pgx
// stdlib driver — separate from the pgxpool the appender queries through.
// Mirrors internal/hub/store/migrations.go.
func Migrate(ctx context.Context, dsn string) error {
	sub, err := migrationsSub()
	if err != nil {
		return fmt.Errorf("store: embed migrations: %w", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("store: open sql: %w", err)
	}
	defer func() { _ = db.Close() }()

	p, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		return fmt.Errorf("store: build goose provider: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("store: apply migrations: %w", err)
	}
	return nil
}
