//go:build store_pg

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/card"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The card read path against a real Postgres (QUM-1251, M2).
//
// This is the half of AC2 that only a database can establish: that a card row
// in Postgres — not the compiled-in seed that happens to be byte-identical to
// it — is what the spawn path reads. Because syncSeedCards enforces equality
// between the row and the embedded seed, a test that merely inspects the
// returned card cannot tell the two sources apart. Every assertion here
// therefore reads a row the seeds do NOT contain: a v2, or a tampered value.
//
// The one field that VARIES across the embedded seeds is
// render.append_env_context — false for legacy-researcher, true for the other
// three (see internal/card/seeds/*.md). It is the only discriminating constant
// available, so the render-opt assertions below are built on it rather than on
// the three all-true seeds, where an implementation that hardcoded
// RenderOpts{true,true,true} would pass everything.

// fullRenderJSON is a complete render object, used wherever a test plants a row.
//
// Complete rather than `'{}'`: CardForType requires all three keys, so `'{}'` is
// refused, and a test that planted one would be asserting the refusal path by
// accident while claiming to assert something else.
const fullRenderJSON = `{"append_env_context":true,"subagent_banner":true,"sandbox_warning":true}`

// ledgerOn builds a Ledger directly on a migrated pool, as newSpawnEnv does:
// Open() would re-resolve configuration, a DSN and git, none of which the card
// read path is about. CardForType does not scope by project, so projectID is
// deliberately left unset; if it ever does, this helper has to grow one.
func ledgerOn(pool *pgxpool.Pool) *Ledger {
	return &Ledger{enabled: true, pool: pool}
}

// TestCardForType_ReadsTheRowNotTheSeed is AC2's database half.
//
// The tamper is the whole point: after it, the row and the embedded seed
// disagree, so `haiku` can only have come from Postgres. The pre-tamper leg is
// the control that this query reads at all — without it, a CardForType that
// returned the embedded seed unconditionally would pass the first assertion and
// fail only the second, and it would be unclear which claim had broken.
//
// This proves it for SCALARS only. An implementation reading scalar columns from
// Postgres and Opts from the embedded seed passes this test — which is why
// TestCardForType_RenderOptsComeFromTheRow exists separately and tampers the
// render column on its own.
func TestCardForType_ReadsTheRowNotTheSeed(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()
	l := ledgerOn(pool)

	seed, err := card.SeedForType("engineer")
	if err != nil {
		t.Fatalf("card.SeedForType: %v", err)
	}

	before, err := l.CardForType(ctx, "engineer")
	if err != nil {
		t.Fatalf("CardForType before tampering: %v", err)
	}
	if before.Model != seed.Model {
		t.Fatalf("the freshly-migrated row disagrees with the seed (model %q vs %q) — the sync should have made them equal, so this test's baseline is wrong",
			before.Model, seed.Model)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE agent_cards SET model = 'haiku' WHERE id = $1`, seed.ID()); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	after, err := l.CardForType(ctx, "engineer")
	if err != nil {
		t.Fatalf("CardForType after tampering: %v", err)
	}
	if after.Model != "haiku" {
		t.Errorf("CardForType returned model %q after the row was changed to 'haiku' — it is reading the compiled-in seed, not the database", after.Model)
	}
	if seed.Model == "haiku" {
		t.Errorf("the seed's own model is now 'haiku', so the assertion above no longer distinguishes the database from the seed")
	}
}

// TestCardForType_RenderOptsComeFromTheRow closes the gap the model tamper
// leaves open, and it is the reason migration 00004 exists.
//
// Card.Opts governs three splices in Render: the sub-agent banner, the
// "# Environment" block and the TEST SANDBOX MODE warning. An implementation
// that read scalars from Postgres but Opts from the embedded seed — or that
// simply hardcoded all-true, which is what three of the four seeds say — would
// pass every other test in this file. So this one tampers render to a value NO
// seed holds and asserts both the struct and the rendered CONSEQUENCE.
//
// The rendered leg is not redundant with the struct leg: a renderer that stopped
// consulting Opts would satisfy the struct comparison and fail this.
func TestCardForType_RenderOptsComeFromTheRow(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()
	l := ledgerOn(pool)

	seed, err := card.SeedForType("engineer")
	if err != nil {
		t.Fatalf("card.SeedForType: %v", err)
	}
	// The premise: the engineer seed asks for the banner, so switching it off in
	// the row is a real disagreement rather than a no-op.
	if !seed.Opts.SubagentBanner {
		t.Fatalf("the engineer seed no longer sets subagent_banner, so tampering it off proves nothing")
	}

	in := card.Input{
		AgentName: "a", ParentName: "p", BranchName: "b",
		Env: card.Env{Subagent: true, ParentName: "p", TestMode: true},
	}

	// Control: before the tamper, the banner IS present. This is what
	// distinguishes "the tamper took effect" from "this render never emits a
	// banner for any input".
	beforeCard, err := l.CardForType(ctx, "engineer")
	if err != nil {
		t.Fatalf("CardForType before tampering: %v", err)
	}
	beforeOut, err := beforeCard.Render(in)
	if err != nil {
		t.Fatalf("rendering before tampering: %v", err)
	}
	if !strings.Contains(beforeOut, "SUB-AGENT BANNER") {
		t.Fatalf("the untampered engineer card renders no sub-agent banner, so the absence asserted below would not be caused by the tamper")
	}

	tag, err := pool.Exec(ctx,
		`UPDATE agent_cards
		    SET render = '{"append_env_context":false,"subagent_banner":false,"sandbox_warning":true}'::jsonb
		  WHERE id = $1`, seed.ID())
	if err != nil {
		t.Fatalf("tamper render: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("the tamper affected %d rows, want 1 — it matched nothing and the assertions below would be about the untampered row", tag.RowsAffected())
	}

	got, err := l.CardForType(ctx, "engineer")
	if err != nil {
		t.Fatalf("CardForType after tampering render: %v", err)
	}
	if got.Opts.SubagentBanner {
		t.Errorf("render.subagent_banner came back true after the row was set to false — Opts is not being read from the database")
	}
	if got.Opts.AppendEnvContext {
		t.Errorf("render.append_env_context came back true after the row was set to false — Opts is not being read from the database")
	}
	if !got.Opts.SandboxWarning {
		t.Errorf("render.sandbox_warning came back false but the row says true — the decode is not field-accurate (all-false is as wrong as all-true)")
	}

	out, err := got.Render(in)
	if err != nil {
		t.Fatalf("rendering the tampered card: %v", err)
	}
	if strings.Contains(out, "SUB-AGENT BANNER") {
		t.Errorf("the rendered prompt still carries the sub-agent banner though the card's render opts switched it off — Opts reached the struct but not the renderer")
	}
	if !strings.Contains(out, "TEST SANDBOX MODE") {
		t.Errorf("the rendered prompt dropped the sandbox warning though the row asks for it — the render opts are being read as all-false rather than field-by-field")
	}
}

// TestAgentCards_RenderColumnUsesSnakeCaseKeys pins the JSONB WIRE NAMES.
//
// Every other assertion in this slice reads the column back through the same
// card.RenderOpts that wrote it, so the encoding is symmetric: a misspelled json
// tag, a Go-default field name, or two tags SWAPPED all round-trip perfectly and
// leave every struct comparison green. Only raw SQL against the column can see
// the names, and the names are the contract `sprawl def publish` and any operator
// writing a card by hand have to match.
//
// legacy-researcher is what makes this a constraint rather than three trues that
// any permutation satisfies: it is the only seed with a false, and it is false
// for append_env_context specifically, so a swap of that key with either other
// key fires here.
func TestAgentCards_RenderColumnUsesSnakeCaseKeys(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()

	for _, tc := range []struct {
		agentType                        string
		wantEnv, wantBanner, wantSandbox string
	}{
		{"researcher", "false", "true", "true"},
		{"engineer", "true", "true", "true"},
	} {
		t.Run(tc.agentType, func(t *testing.T) {
			seed, err := card.SeedForType(tc.agentType)
			if err != nil {
				t.Fatalf("card.SeedForType: %v", err)
			}
			var env, banner, sandbox *string
			err = pool.QueryRow(ctx,
				`SELECT render->>'append_env_context', render->>'subagent_banner', render->>'sandbox_warning'
				   FROM agent_cards WHERE id = $1`, seed.ID()).
				Scan(&env, &banner, &sandbox)
			if err != nil {
				t.Fatalf("reading render keys for %s: %v", seed.Name, err)
			}
			// A NULL here means the key is absent under that name — which is
			// exactly the failure a swapped or misspelled tag produces, and it
			// would be indistinguishable from `false` if these were plain
			// strings.
			for name, got := range map[string]*string{
				"append_env_context": env, "subagent_banner": banner, "sandbox_warning": sandbox,
			} {
				if got == nil {
					t.Errorf("%s: render has no key %q — the json tag on card.RenderOpts does not match the documented wire name, so a hand-written or def-published card cannot set it",
						seed.Name, name)
				}
			}
			if env == nil || banner == nil || sandbox == nil {
				return
			}
			if *env != tc.wantEnv || *banner != tc.wantBanner || *sandbox != tc.wantSandbox {
				t.Errorf("%s: render is (append_env_context=%s, subagent_banner=%s, sandbox_warning=%s), want (%s, %s, %s) — two keys may be swapped",
					seed.Name, *env, *banner, *sandbox, tc.wantEnv, tc.wantBanner, tc.wantSandbox)
			}
		})
	}
}

// TestCardForType_ReadsAHandWrittenSnakeCaseRow is the other direction of the
// same contract: a row written by something that is NOT this Go struct — which
// is what `sprawl def publish` and the tamper legs both are — must decode.
func TestCardForType_ReadsAHandWrittenSnakeCaseRow(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()
	l := ledgerOn(pool)

	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_cards (id, name, version, agent_type, description, prompt, model, effort, content_sha256, render)
		 VALUES ($1, 'hand-written', 1, 'gardener', 'd', 'body', 'opus', 'low', repeat('c', 64),
		         '{"append_env_context":true,"subagent_banner":false,"sandbox_warning":false}'::jsonb)`,
		uuid.New()); err != nil {
		t.Fatalf("insert a hand-written card: %v", err)
	}

	got, err := l.CardForType(ctx, "gardener")
	if err != nil {
		t.Fatalf("CardForType: %v", err)
	}
	want := card.RenderOpts{AppendEnvContext: true, SubagentBanner: false, SandboxWarning: false}
	if got.Opts != want {
		t.Errorf("a hand-written snake_case render object decoded to %+v, want %+v", got.Opts, want)
	}
}

// TestCardForType_ReturnsTheHighestVersion pins the ORDER BY.
//
// First-match — whatever Postgres happens to return without an ORDER BY — would
// keep serving legacy@1 forever once the slim v2 cards land, and would look
// entirely correct while doing it, because today every type has exactly one
// version. That is why this plants a v2 rather than trusting the seeds.
func TestCardForType_ReturnsTheHighestVersion(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()
	l := ledgerOn(pool)

	// Control: with only v1 present, the probe reports v1. This is what
	// distinguishes "reads the newest row" from "hardcodes the highest number it
	// was ever shown".
	v1, err := l.CardForType(ctx, "engineer")
	if err != nil {
		t.Fatalf("CardForType with only v1 present: %v", err)
	}
	if v1.Version != 1 {
		t.Fatalf("expected the seeded engineer card to be v1, got v%d — this test's premise no longer holds", v1.Version)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_cards (id, name, version, agent_type, description, prompt, model, effort, content_sha256, render)
		 VALUES ($1, 'slim-engineer', 2, 'engineer', 'slim', 'v2 body', 'fable', 'high', repeat('b', 64), $2::jsonb)`,
		uuid.New(), fullRenderJSON); err != nil {
		t.Fatalf("insert a v2 card: %v", err)
	}

	got, err := l.CardForType(ctx, "engineer")
	if err != nil {
		t.Fatalf("CardForType with v2 present: %v", err)
	}
	if got.Version != 2 || got.Name != "slim-engineer" {
		t.Errorf("CardForType returned %s@%d, want slim-engineer@2 — a lower version winning means the query has no ORDER BY version DESC",
			got.Name, got.Version)
	}
}

// TestCardForType_RefusesIncompleteRenderOpts is the loud-failure half of the
// hazard 00004 exists to prevent, and it covers NULL, `{}` and a
// partially-specified object UNIFORMLY.
//
// That uniformity is the point, and it was a review finding: refusing NULL while
// happily reading `'{}'` would be incoherent, because both decode to
// "no splices" and both are indistinguishable from "nobody said". Under a struct
// decode, a missing key and an explicit `false` are the same value — so the only
// way to tell a card that MEANS all-false from one that never specified anything
// is to require all three keys to be present.
//
// Refusing is not fatal: the resolver above this falls back to the embedded seed
// on any error, so an unreadable row costs a spawn its published card and never
// its launch. Reading it as all-false instead would silently strip the sub-agent
// banner and the sandbox warning from a prompt that still looked plausible.
func TestCardForType_RefusesIncompleteRenderOpts(t *testing.T) {
	for name, set := range map[string]string{
		// A SQL NULL and a jsonb `null` DOCUMENT are different values and are
		// caught by different guards, so both are legs. The second is the one
		// that matters most, because encoding/json unmarshals a `null` document
		// into a struct as a NO-OP: it would arrive as all-false with no error
		// at all, which is the silent-splice-stripping outcome exactly.
		"sql null":           `NULL`,
		"json null document": `'null'::jsonb`,
		"empty object":       `'{}'::jsonb`,
		"one key missing":    `'{"append_env_context":true,"subagent_banner":true}'::jsonb`,
		"wrong key name":     `'{"append_env_context":true,"subagent_banner":true,"sandboxWarning":true}'::jsonb`,
	} {
		t.Run(name, func(t *testing.T) {
			_, pool := newTestSchema(t)
			ctx := context.Background()
			l := ledgerOn(pool)

			seed, err := card.SeedForType("engineer")
			if err != nil {
				t.Fatalf("card.SeedForType: %v", err)
			}
			// Control: the row is readable before its render column is spoiled,
			// so the refusal below is caused by this subtest's value and not by
			// anything else in the row.
			if _, err := l.CardForType(ctx, "engineer"); err != nil {
				t.Fatalf("the row must be readable before render is spoiled: %v", err)
			}

			tag, err := pool.Exec(ctx,
				`UPDATE agent_cards SET render = `+set+` WHERE id = $1`, seed.ID())
			if err != nil {
				t.Fatalf("spoiling render: %v", err)
			}
			if tag.RowsAffected() != 1 {
				t.Fatalf("the UPDATE affected %d rows, want 1 — it matched nothing and the refusal below would be about something else", tag.RowsAffected())
			}

			got, err := l.CardForType(ctx, "engineer")
			if err == nil {
				// Print the pointer, not a field: a (nil, nil) return is the
				// exact "silent zero" outcome this test is about, and
				// dereferencing here would panic instead of reporting it.
				t.Fatalf("CardForType accepted a row whose render opts are not fully specified (%s) and returned %+v — reading unspecified opts as no-splices strips the sub-agent banner and the sandbox warning silently", name, got)
			}
			if got != nil {
				t.Errorf("a refused row must yield no card; got %+v", got)
			}
			if !strings.Contains(err.Error(), "render") {
				t.Errorf("the error must name the column an operator has to fix; got: %v", err)
			}
		})
	}
}

// TestCardForType_UnknownTypeReportsNoRow pins that "no card for this type" is
// an error rather than a zero-valued card. The resolver above turns it into a
// seed fallback; a zero card would become an EMPTY system prompt, which is an
// agent with no safety instructions at all.
func TestCardForType_UnknownTypeReportsNoRow(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()
	l := ledgerOn(pool)

	// Control: a type that IS seeded resolves, so a failure below is about the
	// unknown type and not about the query being broken for every input.
	if _, err := l.CardForType(ctx, "engineer"); err != nil {
		t.Fatalf("a seeded type must resolve: %v", err)
	}

	got, err := l.CardForType(ctx, "gardener")
	if err == nil {
		t.Fatalf("an unseeded agent type must report no row; got %+v", got)
	}
	if got != nil {
		t.Errorf("a missing card must yield no card, not a zero-valued one; got %+v", got)
	}
	// This is the error an operator actually meets in a spawn log — the fallback
	// is silent by design, so the text is the only channel saying which agent
	// type ran on a compiled-in seed.
	if !strings.Contains(err.Error(), "gardener") {
		t.Errorf("the error must name the agent type whose lookup found nothing; got: %v", err)
	}
}

// TestCardForType_WorksAsTheAppRole is the production-fidelity leg.
//
// Every other test in this file tampers and reads as the schema OWNER, which
// bypasses both grants and ownership checks. Production reads as sprawl_app,
// which holds SELECT and nothing else on agent_cards. This is one happy-path
// read through that role, so a CardForType that depended on owner privileges or
// on an owner-resolved search_path could not pass unnoticed.
func TestCardForType_WorksAsTheAppRole(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	// SET ROLE rather than a separate login: sprawl_app is NOLOGIN by design,
	// and under SET ROLE both superuser and ownership bypass drop.
	if _, err := conn.Exec(ctx, "SET ROLE sprawl_app"); err != nil {
		t.Fatalf("SET ROLE sprawl_app: %v", err)
	}

	seed, err := card.SeedForType("researcher")
	if err != nil {
		t.Fatalf("card.SeedForType: %v", err)
	}
	// Read through the same SQL CardForType uses, on a connection that really is
	// the app role. A privilege or search_path problem shows up here.
	var name, model string
	var opts card.RenderOpts
	err = conn.QueryRow(ctx,
		`SELECT name, model, render FROM agent_cards
		  WHERE agent_type = $1 ORDER BY version DESC LIMIT 1`, "researcher").
		Scan(&name, &model, &opts)
	if err != nil {
		t.Fatalf("the app role cannot perform the card read the spawn path depends on: %v", err)
	}
	if name != seed.Name || opts != seed.Opts {
		t.Errorf("the app role read %s %+v, want %s %+v", name, opts, seed.Name, seed.Opts)
	}
	// Aim control: the same connection must NOT be able to write, or this test
	// is running as something more privileged than the app role and proves
	// nothing about production's privileges.
	if _, err := conn.Exec(ctx, `UPDATE agent_cards SET model = 'x' WHERE id = $1`, seed.ID()); err == nil {
		t.Error("this connection can UPDATE agent_cards, so SET ROLE did not take effect and the read above was not performed as sprawl_app")
	}
}

// TestMigrate_BackfillsRenderOnAPreM2Row covers the upgrade path, which no other
// test in this package reaches: newTestSchema always migrates a fresh, empty
// schema to head, so every seed row is written by a build that already has the
// render column.
//
// The hazard, found in review before it shipped: 00004 is additive and nullable
// against a table that may ALREADY hold rows, including rows whose id matches a
// seed. syncSeedCards uses ON CONFLICT (id) DO NOTHING, so on such a database
// render stays NULL — and a read-back that scans NULL into a card.RenderOpts
// fails, making Migrate return an error FOREVER, in every process that opens a
// Ledger. That is not a graceful fallback; it bricks the store on upgrade.
//
// Nulling the column reproduces exactly that state without needing
// partial-migration machinery: a seed-id row whose render was never set.
//
// Backfilling is not a breach of immutability. It writes only where NO value was
// ever published, so it completes a row rather than changing one, and the
// read-back still refuses every other kind of drift.
func TestMigrate_BackfillsRenderOnAPreM2Row(t *testing.T) {
	dsn, pool := newTestSchema(t)
	ctx := context.Background()
	seed, err := card.SeedForType("engineer")
	if err != nil {
		t.Fatalf("card.SeedForType: %v", err)
	}

	// Control: a re-migrate is clean before the column is nulled, so a failure
	// below is caused by the NULL rather than by Migrate being broken outright.
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatalf("re-migrate before nulling render must succeed: %v", err)
	}

	tag, err := pool.Exec(ctx, `UPDATE agent_cards SET render = NULL WHERE id = $1`, seed.ID())
	if err != nil {
		t.Fatalf("nulling render: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("the UPDATE affected %d rows, want 1 — it matched nothing and this test would be about a row that still has its render", tag.RowsAffected())
	}

	if err := Migrate(ctx, dsn); err != nil {
		t.Fatalf("Migrate failed against a row whose render column predates 00004 — an upgraded database would be unable to open a Ledger at all: %v", err)
	}

	var opts card.RenderOpts
	if err := pool.QueryRow(ctx, `SELECT render FROM agent_cards WHERE id = $1`, seed.ID()).Scan(&opts); err != nil {
		t.Fatalf("render is still unreadable after Migrate — it was not backfilled: %v", err)
	}
	if opts != seed.Opts {
		t.Errorf("render backfilled to %+v, want the embedded card's %+v", opts, seed.Opts)
	}
}

// TestMigrate_BackfillDoesNotOverwriteAPublishedRender is the other half of the
// backfill's contract, and the one that keeps it compatible with immutability.
//
// The backfill must touch only rows where render IS NULL. A backfill written as
// an unconditional UPDATE would silently repair every tampered render on the
// next Migrate — which would defeat the drift refusal in
// TestMigrate_RefusesAnInPlaceEditOfAPublishedCard by healing the very
// divergence that test requires Migrate to REPORT.
func TestMigrate_BackfillDoesNotOverwriteAPublishedRender(t *testing.T) {
	dsn, pool := newTestSchema(t)
	ctx := context.Background()
	seed, err := card.SeedForType("engineer")
	if err != nil {
		t.Fatalf("card.SeedForType: %v", err)
	}
	if !seed.Opts.SubagentBanner {
		t.Fatalf("the engineer seed no longer sets subagent_banner, so the divergence below is not a divergence")
	}

	tag, err := pool.Exec(ctx,
		`UPDATE agent_cards
		    SET render = '{"append_env_context":true,"subagent_banner":false,"sandbox_warning":true}'::jsonb
		  WHERE id = $1`, seed.ID())
	if err != nil {
		t.Fatalf("tamper render: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("the tamper affected %d rows, want 1", tag.RowsAffected())
	}

	if err := Migrate(ctx, dsn); err == nil {
		t.Fatal("Migrate accepted a row whose render diverges from the embedded card — an unconditional backfill would heal exactly the drift the immutability check must report")
	}

	// And the divergent value is still there: the backfill did not quietly
	// rewrite it on the way to failing.
	var banner *bool
	if err := pool.QueryRow(ctx,
		`SELECT (render->>'subagent_banner')::bool FROM agent_cards WHERE id = $1`, seed.ID()).Scan(&banner); err != nil {
		t.Fatalf("reading render back: %v", err)
	}
	if banner == nil || *banner {
		t.Errorf("subagent_banner is %v after a failed Migrate, want false — the backfill overwrote a published value instead of leaving it for the drift check", banner)
	}
}
