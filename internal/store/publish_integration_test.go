//go:build store_pg

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/card"
	"github.com/google/uuid"
)

// The publish path against a real Postgres (QUM-1251, AC1 and AC5).
//
// Everything worth asserting here is a property of the SCHEMA rather than of the
// Go code, so none of it is reachable from the hermetic tests in publish_test.go:
// that a republish is refused is agent_cards' UNIQUE (name, version) doing it,
// and that publish and the spawn path agree on the column list can only be shown
// by writing a row through one and reading it through the other.

// publishTestCard builds a card that is NOT one of the embedded seeds, so nothing
// asserted about it can be satisfied by syncSeedCards having already written a
// byte-identical row. Version 9 is chosen for the same reason.
func publishTestCard(t *testing.T, name string) *card.Card {
	t.Helper()
	return &card.Card{
		Name:          name,
		Version:       9,
		AgentType:     "engineer",
		Description:   "published by publish_integration_test",
		Model:         "haiku",
		Effort:        "high",
		Body:          "You are {{AGENT_NAME}}, published to a real database.",
		ContentSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		// append_env_context FALSE against the three all-true seeds: the one
		// discriminating render flag available, so an implementation that
		// hardcoded all-true could not pass.
		Opts: card.RenderOpts{AppendEnvContext: false, SubagentBanner: true, SandboxWarning: true},
	}
}

// TestPublishCard_RepublishingTheSameVersionIsRefused is AC1's "rows are
// immutable", leg (a).
//
// The refusal is the DATABASE's, which is the whole point: PublishCard carries no
// ON CONFLICT clause, so UNIQUE (name, version) raises 23505 and nothing in Go
// has to remember to check. The SQLSTATE is asserted rather than the message text
// — text is lc_messages-dependent and satisfiable by anything that prints the
// same words.
func TestPublishCard_RepublishingTheSameVersionIsRefused(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()
	c := publishTestCard(t, "slim-engineer-republish")

	if err := PublishCard(ctx, pool, c); err != nil {
		t.Fatalf("first publish failed, so the refusal below would not be a refusal of a REpublish: %v", err)
	}

	err := PublishCard(ctx, pool, c)
	if err == nil {
		t.Fatalf("republishing %s@%d succeeded — the row is not immutable", c.Name, c.Version)
	}
	if !errors.Is(err, ErrCardAlreadyPublished) {
		t.Errorf("republish failed with %v, want it wrapped in ErrCardAlreadyPublished so `def publish` can tell an operator to bump the version", err)
	}
	if got := pgCode(err); got != sqlStateUniqueViolation {
		t.Errorf("republish SQLSTATE = %q, want %q — the refusal did not come from the unique constraint", got, sqlStateUniqueViolation)
	}

	// And the ORIGINAL row is untouched. A refusal that had partially applied
	// would be worse than one that succeeded, and the wording of the error alone
	// cannot tell the two apart.
	var body string
	if err := pool.QueryRow(ctx, `SELECT prompt FROM agent_cards WHERE id = $1`, c.ID()).Scan(&body); err != nil {
		t.Fatalf("reading back the original row: %v", err)
	}
	if body != c.Body {
		t.Errorf("the original row's prompt is now %q, want %q", body, c.Body)
	}

	// Aim control: a DIFFERENT version of the same name publishes fine. Without
	// it, a PublishCard that refused every insert — a broken statement, a wrong
	// table — would satisfy the refusal above perfectly.
	next := publishTestCard(t, c.Name)
	next.Version = c.Version + 1
	if err := PublishCard(ctx, pool, next); err != nil {
		t.Errorf("publishing %s@%d was also refused, so the refusal above is not specific to a REpublish: %v", next.Name, next.Version, err)
	}
}

// TestPublishCard_IsReadableByCardForType closes the loop the two column lists
// exist for: a published card must be what the spawn path reads.
//
// Sharing cardColumns makes drift impossible by construction, and this is the
// end-to-end evidence that the construction works — including the render
// options, which live in a jsonb column and are the one field a mismatch turns
// into a silently different prompt rather than an error.
func TestPublishCard_IsReadableByCardForType(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()

	c := publishTestCard(t, "slim-engineer-readback")
	// Version above every seed's, because CardForType takes the HIGHEST version
	// for the agent type: at v1 this test would pass while reading the seed.
	if err := PublishCard(ctx, pool, c); err != nil {
		t.Fatalf("PublishCard: %v", err)
	}

	got, err := ledgerOn(pool).CardForType(ctx, "engineer")
	if err != nil {
		t.Fatalf("CardForType could not read the card publish just wrote: %v", err)
	}
	if got.Name != c.Name || got.Version != c.Version {
		t.Fatalf("CardForType returned %s@%d, want the freshly published %s@%d", got.Name, got.Version, c.Name, c.Version)
	}
	for _, f := range []struct{ field, got, want string }{
		{"description", got.Description, c.Description},
		{"prompt", got.Body, c.Body},
		{"model", got.Model, c.Model},
		{"effort", got.Effort, c.Effort},
		{"content_sha256", got.ContentSHA256, c.ContentSHA256},
		{"agent_type", got.AgentType, c.AgentType},
	} {
		if f.got != f.want {
			t.Errorf("%s round-tripped as %q, want %q", f.field, f.got, f.want)
		}
	}
	if got.Opts != c.Opts {
		t.Errorf("render options round-tripped as %+v, want %+v", got.Opts, c.Opts)
	}
}

// TestListCards_ReadsEverySeedPlusWhatWasPublished. `def list` is AC1's
// evidence, so its query has to work against the real table — the hermetic test
// can only show that a fake row set decodes.
func TestListCards_ReadsEverySeedPlusWhatWasPublished(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()

	seeds, err := card.Seeds()
	if err != nil {
		t.Fatalf("card.Seeds: %v", err)
	}
	c := publishTestCard(t, "slim-engineer-listed")
	if err := PublishCard(ctx, pool, c); err != nil {
		t.Fatalf("PublishCard: %v", err)
	}

	got, err := ListCards(ctx, pool)
	if err != nil {
		t.Fatalf("ListCards: %v", err)
	}
	if want := len(seeds) + 1; len(got) != want {
		t.Fatalf("ListCards returned %d cards, want %d (every migrated seed plus the one published here)", len(got), want)
	}
	// Every seed is present, so a listing that silently dropped rows — the
	// failure a bare "the published card is in there" assertion cannot see —
	// fails here.
	for _, s := range seeds {
		found := false
		for _, p := range got {
			if p.Name == s.Name && p.Version == s.Version {
				found = true
			}
		}
		if !found {
			t.Errorf("ListCards omitted the seeded card %s@%d", s.Name, s.Version)
		}
	}
	// Ordered by (agent_type, version), which is what makes "which version wins"
	// legible in the output.
	for i := 1; i < len(got); i++ {
		prev, cur := got[i-1], got[i]
		if prev.AgentType > cur.AgentType || (prev.AgentType == cur.AgentType && prev.Version > cur.Version) {
			t.Errorf("ListCards is not ordered by (agent_type, version): %s@%d came before %s@%d",
				prev.AgentType, prev.Version, cur.AgentType, cur.Version)
		}
	}
	if p := findListed(got, c.Name, c.Version); p == nil {
		t.Errorf("ListCards omitted the card published in this test")
	} else if p.RenderErr != "" {
		t.Errorf("the published card lists RenderErr = %q", p.RenderErr)
	}
}

// seedRegistryForTest is the embedded registry, with the load error handled once.
func seedRegistryForTest(t *testing.T) *Registry {
	t.Helper()
	reg, err := SeedRegistry()
	if err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}
	return reg
}

func findListed(cards []PublishedCard, name string, version int) *PublishedCard {
	for i := range cards {
		if cards[i].Name == name && cards[i].Version == version {
			return &cards[i]
		}
	}
	return nil
}

// TestSchemaResolver_DBFallbackAgainstRealPostgres closes QUM-1262's actual gap.
//
// The resolver's DB-fallback SQL had zero non-test callers and had never run
// against Postgres: every existing test drives it through a hand-written PgPool
// fake, which agrees with whatever the statement says. A wrong column name, a
// wrong table, or a scan destination that cannot take a jsonb are all invisible
// to a fake and all fatal here.
func TestSchemaResolver_DBFallbackAgainstRealPostgres(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()

	// A type this build does not carry: that is the only branch the DB fallback
	// is on. Its id is derived the same way the seeds' are so nothing depends on
	// a literal.
	id := uuid.NewSHA1(uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8"), []byte("qum-1262-db-only-type@1"))
	if _, ok := seedRegistryForTest(t).ByID(id); ok {
		t.Fatalf("this build carries %s, so the DB fallback would never be reached", id)
	}
	schemaDoc := json.RawMessage(`{"type":"object","properties":{"why":{"type":"string"}}}`)
	if _, err := pool.Exec(ctx,
		`INSERT INTO event_type_schemas (id, name, version, json_schema, closes, opens)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, "qum-1262-db-only-type", 1, schemaDoc, nil, true); err != nil {
		t.Fatalf("inserting the DB-only type: %v", err)
	}

	r := NewSchemaResolver(seedRegistryForTest(t), pool)
	got, err := r.Resolve(ctx, id)
	if err != nil {
		t.Fatalf("the DB fallback could not resolve a type that IS in the database: %v", err)
	}
	if got.Name != "qum-1262-db-only-type" || got.Version != 1 {
		t.Errorf("resolved %s@%d, want qum-1262-db-only-type@1", got.Name, got.Version)
	}
	if !got.Opens {
		t.Errorf("Opens round-tripped as false; the appender keys open_contracts maintenance off exactly this field")
	}
	if !strings.Contains(string(got.JSONSchema), `"why"`) {
		t.Errorf("json_schema round-tripped as %q", got.JSONSchema)
	}
	if got.Spillable {
		t.Errorf("Spillable = true for a DB-resolved type; the DB cannot say, and only false is safe")
	}

	// Control: an id in NEITHER the build nor the database is a permanent,
	// distinguishable refusal. Without this leg a resolver that returned a
	// zero-valued schema for everything would pass the assertions above.
	absent := uuid.NewSHA1(uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8"), []byte("qum-1262-nowhere-type@1"))
	if _, err := r.Resolve(ctx, absent); !errors.Is(err, ErrUnknownSchema) {
		t.Errorf("resolving an id present nowhere returned %v, want ErrUnknownSchema", err)
	}
}
