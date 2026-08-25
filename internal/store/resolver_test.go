package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// QUM-1262 decision (b): schema validation resolves against the EMBEDDED
// registry first and falls back to a DB lookup for ids this build does not
// carry. What is asserted here is the DECISION the resolver makes given an
// answer — the live SQL is covered by the store_pg integration suite.
//
// Every assertion in this file is paired with a control in the opposite
// direction, because the two failure modes are opposites and a resolver with
// either defect passes the other's test: a resolver that ALWAYS queried the DB
// would satisfy the fallback test while silently letting a hand-edited row
// override the binary, and a resolver that NEVER queried would satisfy the
// embedded-wins test while leaving `sprawl def publish` unable to validate any
// DB-only type.

// countingPool answers the schema lookup with a canned row and counts how many
// times it was asked. The COUNT is load-bearing: a resolver that queries first
// and discards the answer returns the same *value* as one that never queries,
// so a value-only assertion cannot tell them apart.
type countingPool struct {
	PgPool // embedded nil: anything this double does not implement panics

	mu    sync.Mutex
	calls int
	// askedFor records the id bound to $1 on each lookup. The COUNT alone
	// cannot catch a resolver that queries for the wrong id — a stale loop
	// variable or a zero UUID produces the same call count and, against a
	// double that answers every id identically, the same value.
	askedFor []uuid.UUID

	// row is returned for every lookup; nil means "no such row".
	row *schemaRowFixture
	err error
}

type schemaRowFixture struct {
	name       string
	version    int
	jsonSchema string
	closes     string
	opens      bool
}

func (p *countingPool) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	p.mu.Lock()
	p.calls++
	if len(args) > 0 {
		if id, ok := args[0].(uuid.UUID); ok {
			p.askedFor = append(p.askedFor, id)
		}
	}
	p.mu.Unlock()
	switch {
	case p.err != nil:
		return errRow{err: p.err}
	case p.row == nil:
		return errRow{err: pgx.ErrNoRows}
	default:
		return schemaRow{*p.row}
	}
}

func (p *countingPool) queryCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *countingPool) ids() []uuid.UUID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uuid.UUID(nil), p.askedFor...)
}

type schemaRow struct{ f schemaRowFixture }

func (r schemaRow) Scan(dest ...any) error {
	if len(dest) != 5 {
		return errors.New("schemaRow: want five destinations (name, version, json_schema, closes, opens)")
	}
	name, ok := dest[0].(*string)
	if !ok {
		return errors.New("schemaRow: dest[0] is not *string")
	}
	version, ok := dest[1].(*int)
	if !ok {
		return errors.New("schemaRow: dest[1] is not *int")
	}
	schema, ok := dest[2].(*json.RawMessage)
	if !ok {
		return errors.New("schemaRow: dest[2] is not *json.RawMessage")
	}
	closes, ok := dest[3].(**string)
	if !ok {
		return errors.New("schemaRow: dest[3] is not **string")
	}
	opens, ok := dest[4].(*bool)
	if !ok {
		return errors.New("schemaRow: dest[4] is not *bool")
	}
	*name = r.f.name
	*version = r.f.version
	*schema = json.RawMessage(r.f.jsonSchema)
	if r.f.closes == "" {
		*closes = nil
	} else {
		c := r.f.closes
		*closes = &c
	}
	*opens = r.f.opens
	return nil
}

// embeddedFixture builds a one-entry registry so the tests do not depend on
// which types the real seed set happens to contain today.
func embeddedFixture(t *testing.T, name string, version int, schema string) (*Registry, uuid.UUID) {
	t.Helper()
	id := SeedID(name, version)
	r, err := NewRegistry([]*EventTypeSchema{{
		ID:         id,
		Name:       name,
		Version:    version,
		JSONSchema: json.RawMessage(schema),
	}})
	if err != nil {
		t.Fatalf("building fixture registry: %v", err)
	}
	return r, id
}

// TestSchemaResolver_FallsBackToDBForUnknownID is QUM-1262 (b) proper: an id the
// binary does not carry must still be resolvable so `sprawl def publish` can
// validate against it, rather than refusing every type published after this
// build was cut.
func TestSchemaResolver_FallsBackToDBForUnknownID(t *testing.T) {
	embedded, _ := embeddedFixture(t, "in-binary", 1, `{"type":"object"}`)
	// The fixture is deliberately discriminating on every column the resolver
	// has to carry across: a distinctive schema body, a non-zero Closes, and
	// Opens true. A row whose columns are all zero values cannot tell a
	// resolver that copies them from one that drops them on the floor.
	pool := &countingPool{row: &schemaRowFixture{
		name:       "db-only-type",
		version:    3,
		jsonSchema: `{"type":"object","required":["k"]}`,
		closes:     "some-opener",
		opens:      false,
	}}
	r := NewSchemaResolver(embedded, pool)

	unknown := SeedID("db-only-type", 3)
	got, err := r.Resolve(context.Background(), unknown)
	if err != nil {
		t.Fatalf("an id absent from the binary must resolve from the DB; got: %v", err)
	}
	if got.Name != "db-only-type" || got.Version != 3 {
		t.Errorf("resolved the wrong schema: got %s@%d, want db-only-type@3", got.Name, got.Version)
	}
	if got.ID != unknown {
		t.Errorf("resolved schema carries id %s, want the id that was asked for (%s)", got.ID, unknown)
	}
	// The JSON Schema is the whole point of resolving: a resolver that returned
	// the right name with a nil body would make Validate accept EVERY payload,
	// which is worse than the refusal it replaced.
	if want := `{"type":"object","required":["k"]}`; string(got.JSONSchema) != want {
		t.Errorf("resolved JSONSchema = %q, want %q — an empty schema validates every payload", got.JSONSchema, want)
	}
	// Closes and Opens drive the appender's contract handling (appendTx keys
	// open_contracts maintenance off exactly these). A DB-only type that lost
	// them would leave contracts open forever with no local symptom.
	if got.Closes != "some-opener" {
		t.Errorf("resolved Closes = %q, want %q", got.Closes, "some-opener")
	}
	if got.Opens {
		t.Error("resolved Opens = true from a row where opens is false")
	}
	if ids := pool.ids(); len(ids) != 1 || ids[0] != unknown {
		t.Errorf("the resolver looked up %v, want exactly [%s] — a lookup for the wrong id returns this double's canned row regardless", ids, unknown)
	}
	// A DB row cannot say whether a type is spillable — event_type_schemas has
	// no such column — and the safe default is "not spillable", because
	// spilling a type whose contract role is unknown is the failure
	// validateSeedDoc exists to prevent.
	if got.Spillable {
		t.Error("a DB-resolved schema must default to NOT spillable; event_type_schemas cannot express spillability")
	}

	// Positive control (direction: MUST fire). Same lookup, no pool: the
	// resolver has nothing to fall back TO and must say so rather than
	// accepting the payload unvalidated.
	degraded := NewSchemaResolver(embedded, nil)
	if _, err := degraded.Resolve(context.Background(), unknown); err == nil {
		t.Fatal("control failed to fire: with no pool an unknown id must NOT resolve — accepting it would validate the payload against nothing")
	} else if !errors.Is(err, ErrUnknownSchema) {
		t.Errorf("control fired with the wrong error: got %v, want ErrUnknownSchema", err)
	}
}

// TestSchemaResolver_EmbeddedWinsAndIsNotQueried is the negative control for the
// test above (direction: MUST stay quiet).
//
// It asserts the query COUNT, not just the returned value. syncSeedSchemas
// read-back-compares every seeded type and errors on divergence, so a DB row
// that disagrees with the binary can only exist by hand-editing — and in that
// case the binary is authoritative. A resolver that queried first and preferred
// the embedded copy would return the right value here while putting a DB round
// trip on the append hot path for every event.
func TestSchemaResolver_EmbeddedWinsAndIsNotQueried(t *testing.T) {
	embedded, id := embeddedFixture(t, "in-binary", 1, `{"type":"object"}`)
	pool := &countingPool{row: &schemaRowFixture{
		name:       "in-binary",
		version:    1,
		jsonSchema: `{"type":"object","required":["tampered"]}`,
	}}
	r := NewSchemaResolver(embedded, pool)

	got, err := r.Resolve(context.Background(), id)
	if err != nil {
		t.Fatalf("an embedded id must resolve: %v", err)
	}
	// Both directions: the tampered body must be absent AND the compiled-in one
	// present. The absence check alone is satisfied by a nil JSONSchema.
	if strings.Contains(string(got.JSONSchema), "tampered") {
		t.Error("the DB copy overrode the compiled-in one; the binary is authoritative for types it carries")
	}
	if want := `{"type":"object"}`; string(got.JSONSchema) != want {
		t.Errorf("resolved JSONSchema = %q, want the compiled-in %q", got.JSONSchema, want)
	}
	if n := pool.queryCount(); n != 0 {
		t.Errorf("the resolver issued %d DB queries for an id it already had compiled in; want 0", n)
	}
}

// TestSchemaResolver_DBFailureIsNotAnUnknownSchema keeps the two outcomes
// distinguishable at the point a caller reads them.
//
// "this build does not carry that type" is a permanent, actionable fact; "the
// database is unreachable right now" is transient. Collapsing them means a
// publish blocked by an outage reads as a publish blocked by a bad schema id,
// and the operator goes looking for the wrong thing.
func TestSchemaResolver_DBFailureIsNotAnUnknownSchema(t *testing.T) {
	embedded, _ := embeddedFixture(t, "in-binary", 1, `{"type":"object"}`)
	boom := errors.New("dial tcp: connection refused")
	pool := &countingPool{err: boom}
	r := NewSchemaResolver(embedded, pool)

	_, err := r.Resolve(context.Background(), SeedID("db-only-type", 3))
	if err == nil {
		t.Fatal("a failed lookup must not report a schema as resolved")
	}
	if !errors.Is(err, ErrSchemaUnresolvableDegraded) {
		t.Errorf("got %v, want ErrSchemaUnresolvableDegraded — an outage is not the same as an unknown type", err)
	}
	if errors.Is(err, ErrUnknownSchema) {
		t.Error("an outage was reported as 'this build does not carry that type', which sends the operator to fix the wrong thing")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the underlying transport failure should stay in the chain for diagnosis; got: %v", err)
	}
	var hint *HintError
	if !errors.As(err, &hint) || hint.Hint == "" {
		t.Errorf("the error carries no next action: %v", err)
	}
}

// TestSchemaResolver_CachesDBLookups pins that a DB-only type costs one query
// for its first sighting and none afterwards. Definitions are immutable, so the
// cache cannot go stale; without it every append of a DB-only type pays a round
// trip inside the emit path.
func TestSchemaResolver_CachesDBLookups(t *testing.T) {
	embedded, _ := embeddedFixture(t, "in-binary", 1, `{"type":"object"}`)
	pool := &countingPool{row: &schemaRowFixture{name: "db-only-type", version: 3, jsonSchema: `{"type":"object"}`}}
	r := NewSchemaResolver(embedded, pool)

	id := SeedID("db-only-type", 3)
	for i := range 3 {
		if _, err := r.Resolve(context.Background(), id); err != nil {
			t.Fatalf("lookup %d: %v", i, err)
		}
	}
	if n := pool.queryCount(); n != 1 {
		t.Errorf("resolved the same DB-only id 3 times with %d queries; want exactly 1", n)
	}

	// Control (direction: MUST fire): a DIFFERENT unknown id must still be
	// looked up, so the count above is a cache hit and not a resolver that
	// stopped querying after its first answer.
	if _, err := r.Resolve(context.Background(), SeedID("another-db-only", 1)); err != nil {
		t.Fatalf("a second distinct unknown id must be looked up: %v", err)
	}
	if n := pool.queryCount(); n != 2 {
		t.Errorf("after a second distinct id the query count is %d; want 2 — the cache is swallowing lookups it should not", n)
	}
}

// TestSchemaResolver_PropagatesOpensFromDB is the opposite-valued companion to
// the Closes/Opens assertions in the fallback test.
//
// Split out rather than folded in because Opens and Closes are mutually
// exclusive by validateSeedDoc's rule, so one fixture cannot exercise both at
// their non-zero value — and a resolver that hardcoded either field would pass
// whichever single test happened to agree with the hardcoded value.
func TestSchemaResolver_PropagatesOpensFromDB(t *testing.T) {
	embedded, _ := embeddedFixture(t, "in-binary", 1, `{"type":"object"}`)
	pool := &countingPool{row: &schemaRowFixture{
		name:       "db-only-opener",
		version:    1,
		jsonSchema: `{"type":"object"}`,
		opens:      true,
	}}
	r := NewSchemaResolver(embedded, pool)

	got, err := r.Resolve(context.Background(), SeedID("db-only-opener", 1))
	if err != nil {
		t.Fatalf("resolving a DB-only opener: %v", err)
	}
	if !got.Opens {
		t.Error("resolved Opens = false from a row where opens is true; the appender would never record the open contract")
	}
	if got.Closes != "" {
		t.Errorf("resolved Closes = %q from a NULL closes column", got.Closes)
	}
}

// TestSchemaResolver_ConcurrentResolveIsRaceFree exercises the cache the way the
// append path does — every agent turn emits events, and emitters run on their
// own EventBus subscriber goroutines, so the resolver is shared across
// goroutines by construction. An unguarded cache map passes every other test in
// this file and races only in production.
func TestSchemaResolver_ConcurrentResolveIsRaceFree(t *testing.T) {
	embedded, embeddedID := embeddedFixture(t, "in-binary", 1, `{"type":"object"}`)
	pool := &countingPool{row: &schemaRowFixture{name: "db-only-type", version: 3, jsonSchema: `{"type":"object"}`}}
	r := NewSchemaResolver(embedded, pool)
	dbID := SeedID("db-only-type", 3)

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := dbID
			if i%2 == 0 {
				id = embeddedID
			}
			if _, err := r.Resolve(context.Background(), id); err != nil {
				t.Errorf("concurrent resolve: %v", err)
			}
		}(i)
	}
	wg.Wait()

	// Bounded rather than exact: concurrent first-sightings may legitimately
	// race into more than one lookup before the cache is populated, but a
	// resolver that never caches would issue 8.
	if n := pool.queryCount(); n < 1 || n > 4 {
		t.Errorf("8 concurrent resolves of one DB-only id issued %d queries; want 1..4 (0 means it never looked up, 8 means it never cached)", n)
	}
}

// TestSchemaResolver_MissingRowIsUnknownNotDegraded covers the third outcome:
// the DB answered, and the answer is "no such id".
func TestSchemaResolver_MissingRowIsUnknownNotDegraded(t *testing.T) {
	embedded, _ := embeddedFixture(t, "in-binary", 1, `{"type":"object"}`)
	r := NewSchemaResolver(embedded, &countingPool{row: nil})

	_, err := r.Resolve(context.Background(), SeedID("nowhere", 1))
	if !errors.Is(err, ErrUnknownSchema) {
		t.Errorf("got %v, want ErrUnknownSchema — the DB answered definitively that the id does not exist", err)
	}
	if errors.Is(err, ErrSchemaUnresolvableDegraded) {
		t.Error("a definitive 'no such row' was reported as an outage, which invites a pointless retry")
	}
}
