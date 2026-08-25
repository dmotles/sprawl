package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Schema resolution with a DB fallback — QUM-1262 decision (b).
//
// The embedded Registry (seeds.go) is what makes degraded-mode validation
// possible: the appender is handed a PINNED schema_id and must know what it
// means with the database unreachable. That is a floor, not a ceiling. A type
// published after this binary was cut has an id the binary does not carry, and
// with embedded-only resolution every append of it — and every `sprawl def
// publish` that references it — is refused by a build that is merely old.
//
// So: embedded first, DB second, and the ORDER is the decision.
//
//   - Embedded wins on collision. For seeded types this is definitionally
//     vacuous, because syncSeedSchemas read-back-compares every seeded row and
//     errors on divergence — a disagreeing row can only exist by hand-editing,
//     and in that case the binary is authoritative.
//   - A DB lookup happens ONLY for ids absent from the binary, so the append hot
//     path pays nothing for the types it already knows. It is also why the query
//     count, not just the returned value, is what the tests assert: a resolver
//     that queried first and preferred the embedded copy returns identical
//     values while putting a round trip inside every emit.
//   - Results are cached forever. Definitions are immutable by construction
//     (agent_cards and event_type_schemas are both UNIQUE (name, version) and
//     never updated in place), so there is no staleness for the cache to have.
//
// This is deliberately layered ABOVE Registry rather than folded into it.
// Registry.ByID is `(id) (*EventTypeSchema, bool)` — no ctx, no pool — and a
// DB fallback is not expressible at that signature. Widening it would put a
// context and an I/O error on every purely-local lookup in the codebase.

var (
	// ErrUnknownSchema means no definition exists for that id — not in this
	// build, and (when a pool was available) not in the database either. It is
	// a PERMANENT, actionable fact: retrying will not help.
	ErrUnknownSchema = errors.New("store: unknown event-type schema id")

	// ErrSchemaUnresolvableDegraded means the lookup could not be COMPLETED —
	// the id is absent from this build and the database could not be asked.
	//
	// Kept distinct from ErrUnknownSchema because the two have opposite
	// remedies and opposite lifetimes. Collapsing them makes a publish blocked
	// by an outage read as a publish blocked by a bad schema id, which sends
	// the operator to fix a schema that is fine. It is also why this is not
	// silently treated as "accept the payload": a type nobody can validate
	// against is not a validated type.
	ErrSchemaUnresolvableDegraded = errors.New("store: event-type schema could not be resolved because the event log is unreachable")
)

// SchemaResolver resolves an event-type schema by PINNED id.
//
// An interface rather than a concrete type so the appender and `sprawl def
// publish` share one seam, and so an embedded-only resolver is a legitimate
// implementation rather than a nil pool special case at every call site.
type SchemaResolver interface {
	Resolve(ctx context.Context, id uuid.UUID) (*EventTypeSchema, error)
}

type dbBackedResolver struct {
	embedded *Registry
	pool     PgPool

	mu    sync.RWMutex
	cache map[uuid.UUID]*EventTypeSchema
}

// NewSchemaResolver returns a resolver over embedded, falling back to pool for
// ids embedded does not carry.
//
// A nil pool is legitimate and means embedded-only: that is the degraded-mode
// configuration, and it resolves every id this build carries while refusing —
// rather than silently accepting — the ones it does not.
func NewSchemaResolver(embedded *Registry, pool PgPool) SchemaResolver {
	return &dbBackedResolver{
		embedded: embedded,
		pool:     pool,
		cache:    make(map[uuid.UUID]*EventTypeSchema),
	}
}

func (r *dbBackedResolver) Resolve(ctx context.Context, id uuid.UUID) (*EventTypeSchema, error) {
	if s, ok := r.embedded.ByID(id); ok {
		return s, nil
	}
	r.mu.RLock()
	s, ok := r.cache[id]
	r.mu.RUnlock()
	if ok {
		return s, nil
	}
	if r.pool == nil {
		return nil, fmt.Errorf("%w %s: this build does not carry that type and no database is configured", ErrUnknownSchema, id)
	}

	s, err := r.loadFromDB(ctx, id)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.cache[id] = s
	r.mu.Unlock()
	return s, nil
}

func (r *dbBackedResolver) loadFromDB(ctx context.Context, id uuid.UUID) (*EventTypeSchema, error) {
	var (
		name       string
		version    int
		jsonSchema json.RawMessage
		closes     *string
		opens      bool
	)
	err := r.pool.QueryRow(ctx,
		`SELECT name, version, json_schema, closes, opens
		 FROM event_type_schemas WHERE id = $1`, id).
		Scan(&name, &version, &jsonSchema, &closes, &opens)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// A definitive answer: the row does not exist. Not an outage, so not
		// worth a retry.
		return nil, fmt.Errorf("%w %s: neither this build nor the event log carries that type", ErrUnknownSchema, id)
	case err != nil:
		return nil, &HintError{
			Err:  fmt.Errorf("%w: looking up %s: %w", ErrSchemaUnresolvableDegraded, id, err),
			Hint: "run `sprawl store doctor` to check the event log connection, then retry — the schema id is not necessarily wrong",
		}
	}

	out := &EventTypeSchema{
		ID:         id,
		Name:       name,
		Version:    version,
		JSONSchema: jsonSchema,
		Opens:      opens,
		// Spillable is deliberately left false. event_type_schemas has no
		// spillable column, so the DB cannot say — and "not spillable" is the
		// only safe default: a type whose contract role this build does not
		// know must never be written to a local spill file, which is exactly
		// the invariant validateSeedDoc enforces for seeded types.
	}
	if closes != nil {
		out.Closes = *closes
	}
	return out, nil
}
