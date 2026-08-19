//go:build store_pg

package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The embedded seed schemas are the SINGLE source of truth for event-type
// definitions, and Migrate syncs them into event_type_schemas.
//
// The alternative — a static SQL migration repeating each id as a literal —
// duplicates every derived uuid across two files that nothing forces to agree.
// A drifted literal there is the worst possible failure: a pinned schema_id
// resolves fine in Go (embedded registry) and FK-fails in Postgres, so it
// breaks only at append time, only against a real database.
//
// The sync is INSERT ... ON CONFLICT DO NOTHING plus a read-back equality
// check, because "immutable versioned definitions" has to be enforced somewhere
// and this is the only place that sees both sides.

func TestMigrate_SeedsEveryEmbeddedSchema(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()

	reg, err := SeedRegistry()
	if err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}
	if len(reg.All()) == 0 {
		t.Fatal("the embedded registry is empty, so the loop below would assert nothing")
	}

	var rowCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_type_schemas`).Scan(&rowCount); err != nil {
		t.Fatalf("count event_type_schemas: %v", err)
	}
	if rowCount != len(reg.All()) {
		t.Errorf("event_type_schemas holds %d rows, want %d (one per embedded seed)", rowCount, len(reg.All()))
	}

	for _, s := range reg.All() {
		var (
			name    string
			version int
			opens   bool
			closes  *string
			schema  []byte
		)
		err := pool.QueryRow(ctx,
			`SELECT name, version, opens, closes, json_schema FROM event_type_schemas WHERE id = $1`, s.ID).
			Scan(&name, &version, &opens, &closes, &schema)
		if err != nil {
			t.Errorf("%s@%d (id %s) was not seeded — a pinned schema_id would resolve in Go and FK-fail in Postgres: %v",
				s.Name, s.Version, s.ID, err)
			continue
		}
		if name != s.Name || version != s.Version {
			t.Errorf("id %s is seeded as %s@%d but the registry calls it %s@%d", s.ID, name, version, s.Name, s.Version)
		}
		if opens != s.Opens {
			t.Errorf("%s@%d seeded with opens=%v, registry says %v", s.Name, s.Version, opens, s.Opens)
		}
		gotCloses := ""
		if closes != nil {
			gotCloses = *closes
		}
		if gotCloses != s.Closes {
			t.Errorf("%s@%d seeded with closes=%q, registry says %q", s.Name, s.Version, gotCloses, s.Closes)
		}
		if !sameJSON(t, schema, s.JSONSchema) {
			t.Errorf("%s@%d json_schema in the database differs from the embedded one:\n db: %s\n go: %s",
				s.Name, s.Version, schema, s.JSONSchema)
		}
	}
}

// sameJSON compares two JSON documents structurally. jsonb reorders keys and
// drops whitespace, so a byte comparison would report a difference on every
// round trip and this assertion would be a permanent false alarm.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("left side is not JSON: %v", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("right side is not JSON: %v", err)
	}
	ab, err := json.Marshal(av)
	if err != nil {
		t.Fatalf("re-marshal left: %v", err)
	}
	bb, err := json.Marshal(bv)
	if err != nil {
		t.Fatalf("re-marshal right: %v", err)
	}
	return string(ab) == string(bb)
}

// TestMigrate_SeedSyncIsIdempotent pins that a second Migrate neither
// duplicates rows nor errors. Every process that opens a Ledger may migrate.
func TestMigrate_SeedSyncIsIdempotent(t *testing.T) {
	dsn, pool := newTestSchema(t)
	ctx := context.Background()

	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_type_schemas`).Scan(&before); err != nil {
		t.Fatalf("count: %v", err)
	}
	if before == 0 {
		t.Fatal("no seeds present after the first migrate, so idempotency is untestable here")
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(ctx, dsn); err != nil {
			t.Fatalf("Migrate re-run %d: %v", i+1, err)
		}
	}
	var after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_type_schemas`).Scan(&after); err != nil {
		t.Fatalf("count: %v", err)
	}
	if after != before {
		t.Errorf("re-running Migrate changed the seed row count from %d to %d", before, after)
	}
}

// TestMigrate_RefusesAnInPlaceEditOfAPublishedSchema is the immutability
// assertion, and the reason the sync reads back rather than blindly
// ON CONFLICT DO NOTHING.
//
// Definitions are immutable: a changed constraint gets a new VERSION. If a seed
// file were edited in place instead, ON CONFLICT DO NOTHING would silently keep
// the OLD row, and from then on the same schema_id would mean one thing to the
// binary (embedded) and another to the database — with in-flight workflow
// instances validating against whichever side they happened to reach. Failing
// the migration is the only outcome that surfaces it.
func TestMigrate_RefusesAnInPlaceEditOfAPublishedSchema(t *testing.T) {
	dsn, pool := newTestSchema(t)
	ctx := context.Background()

	reg, err := SeedRegistry()
	if err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}
	target := reg.All()[0]

	// Control: before the tampering, a re-migrate is clean. Without this leg, a
	// Migrate that ALWAYS failed would satisfy the assertion below.
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatalf("re-migrate before tampering must succeed: %v", err)
	}

	// Simulate an in-place edit of a published definition. The owner can do
	// this; the app role cannot (event_type_schemas is SELECT-only for it),
	// which is itself part of the defence.
	if _, err := pool.Exec(ctx,
		`UPDATE event_type_schemas SET json_schema = '{"type":"object","required":[]}'::jsonb WHERE id = $1`,
		target.ID); err != nil {
		t.Fatalf("tamper with %s: %v", target.Name, err)
	}

	err = Migrate(ctx, dsn)
	if err == nil {
		t.Fatalf("Migrate accepted a database row that no longer matches the embedded definition of %s@%d — the same schema_id now means two different things",
			target.Name, target.Version)
	}
	if !strings.Contains(err.Error(), target.Name) {
		t.Errorf("the error must name the diverged schema so an operator knows which one to fix; got: %v", err)
	}
}

// TestMigrate_SeedSyncLeavesForeignDefinitionsAlone pins that the sync only
// owns the schemas it ships. M2 publishes new event types through a different
// path, and a sync that deleted anything it did not recognise would wipe them.
func TestMigrate_SeedSyncLeavesForeignDefinitionsAlone(t *testing.T) {
	dsn, pool := newTestSchema(t)
	ctx := context.Background()

	foreign := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO event_type_schemas (id, name, version, json_schema, opens)
		 VALUES ($1, 'published_by_m2', 1, '{"type":"object"}'::jsonb, false)`, foreign); err != nil {
		t.Fatalf("insert a non-seed definition: %v", err)
	}
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_type_schemas WHERE id = $1`, foreign).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("the seed sync removed a definition it did not ship (found %d rows, want 1) — M2 publishes event types through another path and they must survive", n)
	}
}
