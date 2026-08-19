package store

import (
	"io/fs"
	"strings"
	"testing"
)

// These tests are hermetic — no Docker, no Postgres — so they run in
// `make validate`. What actually applies the migrations is asserted by the
// store_pg suite; what is asserted here is that the embedded FS carries the
// files at all, and the two textual properties of the SQL that no behavioural
// test can observe.

// sqlOnly strips whole-line `--` comments so the textual assertions below read
// SQL rather than prose.
//
// Not a nicety: 00001's header comment EXPLAINS that M1a does not create the
// vector extension, and the pgvector pin below matched that explanation. The
// assertion fired on a file that was correct, which is a false ALARM — it costs
// the next reader's time rather than hiding a defect, and it is the failure
// direction a positive control cannot see. Both directions are controlled below.
func sqlOnly(body string) string {
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func readMigrations(t *testing.T) map[string]string {
	t.Helper()
	sub, err := migrationsSub()
	if err != nil {
		t.Fatalf("migrationsSub: %v", err)
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := fs.ReadFile(sub, e.Name())
		if err != nil {
			t.Fatalf("ReadFile %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(b)
	}
	if len(out) == 0 {
		t.Fatal("the embedded migrations FS is EMPTY — every assertion below would pass vacuously over it")
	}
	return out
}

// TestMigrationsFS_CarriesBothM1AMigrations pins that the embed directive picks
// up the migrations at all.
//
// This is not ceremony: `//go:embed migrations/*.sql` is resolved at compile
// time against the source tree, so a migration that is written but lands in the
// wrong directory compiles, embeds nothing extra, and silently ships a schema
// missing whatever that file contained.
func TestMigrationsFS_CarriesBothM1AMigrations(t *testing.T) {
	got := readMigrations(t)
	want := []string{
		"00001_m1a_event_log.sql",
		"00002_m1a_app_role.sql",
	}
	for _, n := range want {
		if _, ok := got[n]; !ok {
			names := make([]string, 0, len(got))
			for k := range got {
				names = append(names, k)
			}
			t.Errorf("migration %q is not embedded; embedded files: %v", n, names)
		}
	}
	if len(got) != len(want) {
		t.Errorf("embedded %d migration(s), expected exactly %d — an unexpected file here means a migration was added without updating this list", len(got), len(want))
	}
}

// TestMigrations_EachHasUpAndDown pins that every migration is reversible.
// goose accepts an Up-only migration happily, so a missing Down is invisible
// until someone needs to roll back.
func TestMigrations_EachHasUpAndDown(t *testing.T) {
	for name, body := range readMigrations(t) {
		if !strings.Contains(body, "-- +goose Up") {
			t.Errorf("%s has no `-- +goose Up` marker", name)
		}
		if !strings.Contains(body, "-- +goose Down") {
			t.Errorf("%s has no `-- +goose Down` marker — goose accepts an Up-only migration silently", name)
		}
	}
}

// TestMigrations_DoNotDependOnPgvector pins a DELIBERATE NON-CHANGE.
//
// M1a creates no vector columns (event_embeddings is M4), so it must not
// require the extension. Without this pin, adding `CREATE EXTENSION vector` —
// the natural reflex, since the design doc says "Postgres >= 16 + pgvector" —
// would be invisible in review and would turn every deployment without the
// extension available, and every CI container without a pgvector image, from
// working into failing at migrate time. Nothing behavioural can catch it,
// because the test container COULD have the extension.
//
// When M4 adds embeddings it must delete this test in the same commit, which is
// exactly the conversation that should happen.
func TestMigrations_DoNotDependOnPgvector(t *testing.T) {
	for name, body := range readMigrations(t) {
		lower := strings.ToLower(sqlOnly(body))
		for _, banned := range []string{"create extension", "vector(", "using hnsw"} {
			if strings.Contains(lower, banned) {
				t.Errorf("%s contains %q: M1a must not depend on pgvector (event_embeddings is M4). If this is intentional, delete this test in the same commit and say why", name, banned)
			}
		}
	}
}

// TestMigrations_AppendOnlyMigrationGrantsNoMutatingPrivilegeOnEvents is a
// cheap textual backstop for the property the store_pg suite proves properly.
//
// It exists because the real assertion is Docker-gated and therefore absent from
// `make validate`: without this, a commit that grants UPDATE on events passes
// the gate every contributor actually runs. It asserts the narrow, checkable
// thing — that no GRANT statement mentioning `events` carries a mutating verb —
// and is NOT a substitute for the catalog assertion in the integration suite,
// which is the only instrument that sees column-level grants.
func TestMigrations_AppendOnlyMigrationGrantsNoMutatingPrivilegeOnEvents(t *testing.T) {
	body, ok := readMigrations(t)["00002_m1a_app_role.sql"]
	if !ok {
		t.Fatal("00002_m1a_app_role.sql is not embedded, so this assertion has nothing to read")
	}
	var checked int
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		upper := strings.ToUpper(trimmed)
		if !strings.Contains(upper, "GRANT ") || !strings.Contains(upper, ".EVENTS ") {
			continue
		}
		checked++
		for _, verb := range []string{"UPDATE", "DELETE", "TRUNCATE", "ALL"} {
			// Everything before "ON" is the privilege list; a verb after it is
			// part of the object name or a REVOKE's target and is not a grant.
			privs := upper
			if i := strings.Index(upper, " ON "); i >= 0 {
				privs = upper[:i]
			}
			if strings.Contains(privs, verb) {
				t.Errorf("00002 grants %s on events: %q — events is append-only, enforced by exactly SELECT+INSERT", verb, trimmed)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no GRANT statement naming events in 00002 — this assertion inspected nothing, so its silence is not evidence")
	}
}
