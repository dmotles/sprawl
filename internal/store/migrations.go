package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
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
	return syncSeedSchemas(ctx, db)
}

// syncSeedSchemas publishes the embedded seed event-type schemas into
// event_type_schemas.
//
// The embedded registry is the SINGLE source of truth. The alternative — a
// static SQL migration repeating every derived uuid as a literal — duplicates
// each id across two files that nothing forces to agree, and a drifted literal
// there fails in the worst possible way: the pinned schema_id resolves fine
// against the embedded registry and FK-fails in Postgres, so it breaks only at
// append time and only against a real database.
//
// INSERT ... ON CONFLICT DO NOTHING, then READ BACK AND COMPARE. The read-back
// is what enforces "immutable versioned definitions": a seed edited in place
// rather than version-bumped would otherwise leave the old row in the database
// while the binary carried the new text, so one schema_id would mean two
// different things and in-flight instances would validate against whichever
// side they reached. Refusing the migration is the only outcome that surfaces
// that; DO UPDATE would paper over it by mutating a published definition, which
// is precisely what immutability forbids.
//
// Rows this build does not ship are left untouched — M2 publishes event types
// through its own path, and a sync that deleted what it did not recognise would
// wipe them.
func syncSeedSchemas(ctx context.Context, db *sql.DB) error {
	reg, err := SeedRegistry()
	if err != nil {
		return err
	}
	for _, s := range reg.All() {
		var closes any
		if s.Closes != "" {
			closes = s.Closes
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO event_type_schemas (id, name, version, json_schema, closes, opens)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (id) DO NOTHING`,
			s.ID, s.Name, s.Version, []byte(s.JSONSchema), closes, s.Opens); err != nil {
			return fmt.Errorf("store: seeding %s@%d: %w", s.Name, s.Version, err)
		}

		var (
			gotName    string
			gotVersion int
			gotOpens   bool
			gotCloses  sql.NullString
			gotSchema  []byte
		)
		if err := db.QueryRowContext(ctx,
			`SELECT name, version, opens, closes, json_schema FROM event_type_schemas WHERE id = $1`, s.ID).
			Scan(&gotName, &gotVersion, &gotOpens, &gotCloses, &gotSchema); err != nil {
			return fmt.Errorf("store: reading back seed %s@%d: %w", s.Name, s.Version, err)
		}
		if gotName != s.Name || gotVersion != s.Version || gotOpens != s.Opens || gotCloses.String != s.Closes {
			return fmt.Errorf(
				"store: published event-type schema %s@%d has diverged from this build's definition (database says %s@%d opens=%v closes=%q): definitions are immutable, so bump the version instead of editing in place",
				s.Name, s.Version, gotName, gotVersion, gotOpens, gotCloses.String)
		}
		same, err := sameJSONDoc(gotSchema, s.JSONSchema)
		if err != nil {
			return fmt.Errorf("store: comparing published schema %s@%d: %w", s.Name, s.Version, err)
		}
		if !same {
			return fmt.Errorf(
				"store: published event-type schema %s@%d has a different json_schema in the database than in this build: definitions are immutable, so bump the version instead of editing in place",
				s.Name, s.Version)
		}
	}
	return nil
}

// sameJSONDoc compares two JSON documents structurally.
//
// jsonb reorders keys and drops whitespace, so a byte comparison would report a
// difference on every round trip and turn the divergence check above into a
// permanent false alarm that someone would then delete.
func sameJSONDoc(a, b []byte) (bool, error) {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false, fmt.Errorf("left side is not JSON: %w", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false, fmt.Errorf("right side is not JSON: %w", err)
	}
	an, err := json.Marshal(av)
	if err != nil {
		return false, err
	}
	bn, err := json.Marshal(bv)
	if err != nil {
		return false, err
	}
	return string(an) == string(bn), nil
}
