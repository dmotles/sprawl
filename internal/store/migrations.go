package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"

	"github.com/dmotles/sprawl/internal/card"

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
	if err := syncSeedSchemas(ctx, db); err != nil {
		return err
	}
	return syncSeedCards(ctx, db)
}

// syncSeedCards publishes the embedded seed agent cards into agent_cards.
//
// Same contract, same reasoning and the same shape as syncSeedSchemas above: the
// embedded seeds are the single source of truth, a static SQL migration
// repeating each derived uuid as a literal would duplicate every id across two
// files that nothing forces to agree, and INSERT ... ON CONFLICT DO NOTHING
// followed by a READ-BACK-AND-COMPARE is what turns "immutable versioned
// definitions" into something the database enforces rather than something the
// commit log asserts.
//
// The stakes are higher here than for event types, which is why the comparison
// covers every column this sync writes rather than just the hash. A card IS a
// system prompt: a database row that has drifted from the embedded card means
// one card_id denotes two different sets of safety instructions, and every
// prompt-safety scanner in internal/agent would still pass, because those read
// the embedded seeds and never look at Postgres.
//
// Rows this build does not ship are left untouched — `sprawl def publish` writes
// cards through its own path, and a sync that deleted what it did not recognise
// would wipe them on the next migrate, which is every process that opens a
// Ledger.
func syncSeedCards(ctx context.Context, db *sql.DB) error {
	cards, err := card.Seeds()
	if err != nil {
		return fmt.Errorf("store: loading embedded seed cards: %w", err)
	}
	for _, c := range cards {
		renderJSON, err := json.Marshal(c.Opts)
		if err != nil {
			return fmt.Errorf("store: encoding render options for %s@%d: %w", c.Name, c.Version, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO agent_cards (id, name, version, agent_type, description, prompt, model, effort, content_sha256, render)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			 ON CONFLICT (id) DO NOTHING`,
			c.ID(), c.Name, c.Version, c.AgentType, c.Description, c.Body, c.Model, c.Effort, c.ContentSHA256, renderJSON); err != nil {
			return fmt.Errorf("store: seeding card %s@%d: %w", c.Name, c.Version, err)
		}

		// Backfill for a row that predates migration 00004.
		//
		// Without this, an UPGRADED database bricks the store rather than
		// degrading: the row already exists, so the INSERT above does nothing,
		// `render` stays NULL, the read-back below cannot decode it, and Migrate
		// returns an error in EVERY process that opens a Ledger, permanently.
		// No test against a fresh schema can see that, which is why it is called
		// out here and covered by TestMigrate_BackfillsRenderOnAPreM2Row.
		//
		// `render IS NULL` is load-bearing, not an optimisation. An
		// unconditional UPDATE would silently repair a TAMPERED render on every
		// migrate, healing precisely the divergence the read-back exists to
		// report. Writing only where nothing was ever published completes a row
		// instead of changing one, which is what keeps this compatible with
		// immutability.
		if _, err := db.ExecContext(ctx,
			`UPDATE agent_cards SET render = $2 WHERE id = $1 AND render IS NULL`,
			c.ID(), renderJSON); err != nil {
			return fmt.Errorf("store: backfilling render options for %s@%d: %w", c.Name, c.Version, err)
		}

		var got card.Card
		var gotRender []byte
		if err := db.QueryRowContext(ctx,
			`SELECT name, version, agent_type, description, prompt, model, effort, content_sha256, render
			   FROM agent_cards WHERE id = $1`, c.ID()).
			Scan(&got.Name, &got.Version, &got.AgentType, &got.Description, &got.Body, &got.Model, &got.Effort, &got.ContentSHA256, &gotRender); err != nil {
			return fmt.Errorf("store: reading back seed card %s@%d: %w", c.Name, c.Version, err)
		}
		// A render this build cannot parse is reported as DRIFT rather than as a
		// read failure: the row exists and disagrees with the embedded card,
		// which is exactly what the operator needs to be told.
		if got.Opts, err = card.ParseRenderOpts(gotRender); err != nil {
			return fmt.Errorf(
				"store: published agent card %s@%d has unreadable render options (%w): cards are immutable, so bump the version instead of editing in place",
				c.Name, c.Version, err)
		}
		if diff := describeCardDrift(&got, c); diff != "" {
			return fmt.Errorf(
				"store: published agent card %s@%d has diverged from this build's definition (%s): cards are immutable, so bump the version instead of editing in place",
				c.Name, c.Version, diff)
		}
	}
	return nil
}

// describeCardDrift returns a human-readable account of how a published card
// differs from the embedded one, or "" if they agree.
//
// It names the FIRST differing column rather than returning a bool, because the
// only useful thing an operator can do with this error is go and look at that
// column, and "a card diverged" sends them to diff a whole row by hand.
//
// content_sha256 is checked first and the body is checked separately. The hash
// alone is not enough: it covers the source bytes, so it catches an edited seed
// FILE, but a row whose `prompt` column was rewritten in the database has a
// perfectly valid hash for a body it no longer holds — and that is the single
// most dangerous edit possible on this table.
func describeCardDrift(got, want *card.Card) string {
	for _, f := range []struct{ name, got, want string }{
		{"content_sha256", got.ContentSHA256, want.ContentSHA256},
		{"name", got.Name, want.Name},
		{"agent_type", got.AgentType, want.AgentType},
		{"model", got.Model, want.Model},
		{"effort", got.Effort, want.Effort},
		{"description", got.Description, want.Description},
		{"prompt", got.Body, want.Body},
	} {
		if f.got != f.want {
			return fmt.Sprintf("database %s=%q, this build says %q", f.name, truncateForError(f.got), truncateForError(f.want))
		}
	}
	if got.Version != want.Version {
		return fmt.Sprintf("database version=%d, this build says %d", got.Version, want.Version)
	}
	// render last, and compared as a struct rather than as raw bytes: jsonb
	// reorders keys and drops whitespace, so a byte comparison would report a
	// difference on every round trip and turn this check into a permanent false
	// alarm that someone would then delete — the same reasoning sameJSONDoc
	// records for event-type schemas.
	//
	// A card published by a NEWER build may carry a render option this one does
	// not model; ParseRenderOpts ignores unknown keys, so such an option is not
	// compared here and does not read as drift. That is deliberate — refusing it
	// would make a forward version of the same card unspawnable — and the source
	// hash is what catches an actual edit.
	if got.Opts != want.Opts {
		return fmt.Sprintf("database render=%+v, this build says %+v", got.Opts, want.Opts)
	}
	return ""
}

// truncateForError bounds a value quoted into an error message. A card body is
// tens of kilobytes and an unbounded one would bury the rest of the message.
func truncateForError(s string) string {
	const limit = 80
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
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
