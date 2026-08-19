//go:build store_pg

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The assertions in this file pin Appendix A's CONSTRAINTS, not its table
// names. Ten correctly-named tables with the wrong shape satisfies the
// set-equality test in integration_test.go completely, and every property below
// is load-bearing for a later milestone that will simply assume it holds:
// `events.seq` IS the global total order the whole design rests on, and
// `event_claims`' composite primary key IS Appendix B item 1's dispatch
// idempotency (M1b builds on it without re-checking).

const sqlStateCheckViolation = "23514"

// fixture is the minimal FK closure needed to insert an `events` row.
type fixture struct {
	projectID uuid.UUID
	schemaID  uuid.UUID
}

// seedFixture inserts one project and one event_type_schema as the schema owner.
func seedFixture(t *testing.T, pool *pgxpool.Pool) fixture {
	t.Helper()
	ctx := context.Background()
	f := fixture{projectID: uuid.New(), schemaID: uuid.New()}
	if _, err := pool.Exec(ctx,
		`INSERT INTO projects (id, remote_url, created_at) VALUES ($1, $2, now())`,
		f.projectID, "https://example.invalid/"+f.projectID.String()); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO event_type_schemas (id, name, version, json_schema, opens)
		 VALUES ($1, $2, 1, '{"type":"object"}'::jsonb, false)`,
		f.schemaID, "seed_"+f.schemaID.String()[:8]); err != nil {
		t.Fatalf("seed event_type_schema: %v", err)
	}
	return f
}

// insertEvent inserts a minimal valid event and returns its id and seq.
func insertEvent(t *testing.T, pool *pgxpool.Pool, f fixture) (uuid.UUID, int64) {
	t.Helper()
	id := uuid.New()
	var seq int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload)
		 VALUES ($1, $2, $3, $4, '{}'::jsonb) RETURNING seq`,
		id, f.projectID, uuid.New(), f.schemaID).Scan(&seq); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	return id, seq
}

// TestEvents_SeqIsGeneratedAlwaysIdentityPrimaryKey pins the global total
// order: `seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY`.
//
// Three legs, because each admits a different wrong implementation:
// catalog identity (a plain bigint column with an application-supplied value
// would pass a monotonicity check on inserts made in order), GENERATED ALWAYS
// rather than BY DEFAULT (BY DEFAULT lets a caller override seq and forge
// ordering), and observed strict monotonicity (the catalog says nothing about
// what values actually land).
func TestEvents_SeqIsGeneratedAlwaysIdentityPrimaryKey(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()

	var isIdentity, generation string
	if err := pool.QueryRow(ctx,
		`SELECT is_identity, coalesce(identity_generation, '')
		 FROM information_schema.columns
		 WHERE table_schema = current_schema() AND table_name = 'events' AND column_name = 'seq'`).
		Scan(&isIdentity, &generation); err != nil {
		t.Fatalf("read events.seq column metadata: %v", err)
	}
	if isIdentity != "YES" {
		t.Errorf("events.seq is_identity=%q, want YES — without an identity column the global total order is caller-supplied", isIdentity)
	}
	if generation != "ALWAYS" {
		t.Errorf("events.seq identity_generation=%q, want ALWAYS — BY DEFAULT lets a caller override seq and forge the log order", generation)
	}

	var pkCols string
	if err := pool.QueryRow(ctx,
		`SELECT string_agg(a.attname, ',' ORDER BY a.attname)
		 FROM pg_index i
		 JOIN pg_class c ON c.oid = i.indrelid
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey)
		 WHERE n.nspname = current_schema() AND c.relname = 'events' AND i.indisprimary`).
		Scan(&pkCols); err != nil {
		t.Fatalf("read events primary key: %v", err)
	}
	if pkCols != "seq" {
		t.Errorf("events primary key is (%s), want (seq)", pkCols)
	}

	f := seedFixture(t, pool)
	_, first := insertEvent(t, pool, f)
	_, second := insertEvent(t, pool, f)
	if second <= first {
		t.Errorf("consecutive appends produced seq %d then %d — seq must be strictly increasing", first, second)
	}
}

// TestEvents_IDIsUnique pins `id uuid UNIQUE NOT NULL`. The event id is what
// closes_event_id references and what M1b's event_claims key on, so a duplicate
// would silently fork the contract graph.
func TestEvents_IDIsUnique(t *testing.T) {
	_, pool := newTestSchema(t)
	f := seedFixture(t, pool)
	id, _ := insertEvent(t, pool, f)

	_, err := pool.Exec(context.Background(),
		`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload)
		 VALUES ($1, $2, $3, $4, '{}'::jsonb)`,
		id, f.projectID, uuid.New(), f.schemaID)
	if got := pgCode(err); got != sqlStateUniqueViolation {
		t.Errorf("re-inserting an existing event id gave SQLSTATE %q (err=%v), want %s (unique_violation)",
			got, err, sqlStateUniqueViolation)
	}
}

// TestEvents_ForeignKeysEnforced pins all four Appendix A references. A missing
// FK is invisible in normal operation and only shows up as an unresolvable id
// months later, in a log that is supposed to be authoritative.
func TestEvents_ForeignKeysEnforced(t *testing.T) {
	_, pool := newTestSchema(t)
	f := seedFixture(t, pool)
	ctx := context.Background()
	bogus := uuid.New()

	cases := []struct {
		col string
		sql string
		arg []any
	}{
		{
			"project_id",
			`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload) VALUES ($1,$2,$3,$4,'{}'::jsonb)`,
			[]any{uuid.New(), bogus, uuid.New(), f.schemaID},
		},
		{
			"schema_id",
			`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload) VALUES ($1,$2,$3,$4,'{}'::jsonb)`,
			[]any{uuid.New(), f.projectID, uuid.New(), bogus},
		},
		{
			"closes_event_id",
			`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload, closes_event_id) VALUES ($1,$2,$3,$4,'{}'::jsonb,$5)`,
			[]any{uuid.New(), f.projectID, uuid.New(), f.schemaID, bogus},
		},
		{
			"artifact_id",
			`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload, artifact_id) VALUES ($1,$2,$3,$4,'{}'::jsonb,$5)`,
			[]any{uuid.New(), f.projectID, uuid.New(), f.schemaID, bogus},
		},
	}
	for _, c := range cases {
		_, err := pool.Exec(ctx, c.sql, c.arg...)
		if got := pgCode(err); got != sqlStateForeignKeyViolation {
			t.Errorf("events.%s accepted a non-existent reference: SQLSTATE %q (err=%v), want %s (foreign_key_violation)",
				c.col, got, err, sqlStateForeignKeyViolation)
		}
	}

	// Positive control, same run: the identical insert with REAL references
	// succeeds. Without it, four refusals are equally explained by the insert
	// being malformed in some way unrelated to the foreign keys.
	if _, _ = insertEvent(t, pool, f); t.Failed() {
		t.Log("note: the control insert above is what proves the four refusals were about the FKs")
	}
}

// TestEvents_NotNullColumnsEnforced pins the NOT NULLs Appendix A specifies.
func TestEvents_NotNullColumnsEnforced(t *testing.T) {
	_, pool := newTestSchema(t)
	f := seedFixture(t, pool)
	ctx := context.Background()

	cases := []struct {
		col string
		sql string
		arg []any
	}{
		{
			"project_id",
			`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload) VALUES ($1,NULL,$2,$3,'{}'::jsonb)`,
			[]any{uuid.New(), uuid.New(), f.schemaID},
		},
		{
			"workflow_instance_id",
			`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload) VALUES ($1,$2,NULL,$3,'{}'::jsonb)`,
			[]any{uuid.New(), f.projectID, f.schemaID},
		},
		{
			"schema_id",
			`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload) VALUES ($1,$2,$3,NULL,'{}'::jsonb)`,
			[]any{uuid.New(), f.projectID, uuid.New()},
		},
		{
			"payload",
			`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload) VALUES ($1,$2,$3,$4,NULL)`,
			[]any{uuid.New(), f.projectID, uuid.New(), f.schemaID},
		},
	}
	for _, c := range cases {
		_, err := pool.Exec(ctx, c.sql, c.arg...)
		if got := pgCode(err); got != sqlStateNotNullViolation {
			t.Errorf("events.%s accepted NULL: SQLSTATE %q (err=%v), want %s (not_null_violation)",
				c.col, got, err, sqlStateNotNullViolation)
		}
	}
}

// TestEvents_PayloadSizeCapEnforcedInSQL pins the "thin events (≤ ~8KB)" policy
// as a database CHECK.
//
// In SQL rather than in Go deliberately: a Go-only bound is bypassed by any
// direct psql insert, and the plan doc's own words are that events are thin and
// artifacts are fat — an invariant that lives only in application code rots the
// first time anyone writes a backfill script.
func TestEvents_PayloadSizeCapEnforcedInSQL(t *testing.T) {
	_, pool := newTestSchema(t)
	f := seedFixture(t, pool)
	ctx := context.Background()

	// Under the cap: must be accepted. This is the positive control, and it
	// runs first so a CHECK that rejects everything cannot read as a pass.
	small := `{"k":"` + strings.Repeat("a", 1024) + `"}`
	if _, err := pool.Exec(ctx,
		`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload) VALUES ($1,$2,$3,$4,$5::jsonb)`,
		uuid.New(), f.projectID, uuid.New(), f.schemaID, small); err != nil {
		t.Fatalf("a 1KB payload must be accepted; the size cap rejects legitimate events: %v", err)
	}

	big := `{"k":"` + strings.Repeat("a", 32*1024) + `"}`
	_, err := pool.Exec(ctx,
		`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload) VALUES ($1,$2,$3,$4,$5::jsonb)`,
		uuid.New(), f.projectID, uuid.New(), f.schemaID, big)
	if got := pgCode(err); got != sqlStateCheckViolation {
		t.Errorf("a 32KB payload was not refused by the database: SQLSTATE %q (err=%v), want %s (check_violation) — fat content belongs in artifacts",
			got, err, sqlStateCheckViolation)
	}
}

// TestDefinitionTables_NameVersionUnique pins `UNIQUE (name, version)` on all
// three definition tables. Immutable versioned rows referenced BY ID is the
// whole versioning story; two rows sharing (name, version) means "pinned"
// stops meaning anything.
func TestDefinitionTables_NameVersionUnique(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()

	inserts := map[string]string{
		"event_type_schemas": `INSERT INTO event_type_schemas (id, name, version, json_schema) VALUES ($1,'dup',1,'{}'::jsonb)`,
		"agent_cards":        `INSERT INTO agent_cards (id, name, version) VALUES ($1,'dup',1)`,
		"workflow_defs":      `INSERT INTO workflow_defs (id, name, version, steps) VALUES ($1,'dup',1,'[]'::jsonb)`,
	}
	for table, sql := range inserts {
		if _, err := pool.Exec(ctx, sql, uuid.New()); err != nil {
			t.Fatalf("%s: first insert must succeed: %v", table, err)
		}
		_, err := pool.Exec(ctx, sql, uuid.New())
		if got := pgCode(err); got != sqlStateUniqueViolation {
			t.Errorf("%s accepted a duplicate (name, version): SQLSTATE %q (err=%v), want %s",
				table, got, err, sqlStateUniqueViolation)
		}
	}
}

// TestArtifacts_Sha256Unique pins content-addressing: the same content must not
// be storable twice under two ids, or dedup is a lie and the artifact chain
// forks.
func TestArtifacts_Sha256Unique(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()
	digest := []byte(strings.Repeat("\x01", 32))

	if _, err := pool.Exec(ctx,
		`INSERT INTO artifacts (id, kind, content, sha256) VALUES ($1,'report','hello',$2)`,
		uuid.New(), digest); err != nil {
		t.Fatalf("first artifact insert must succeed: %v", err)
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO artifacts (id, kind, content, sha256) VALUES ($1,'report','hello',$2)`,
		uuid.New(), digest)
	if got := pgCode(err); got != sqlStateUniqueViolation {
		t.Errorf("artifacts accepted a duplicate sha256: SQLSTATE %q (err=%v), want %s — content addressing is not enforced",
			got, err, sqlStateUniqueViolation)
	}
}

// primaryKeyColumns returns a table's PK columns, comma-joined in index order.
func primaryKeyColumns(t *testing.T, pool *pgxpool.Pool, table string) string {
	t.Helper()
	var cols *string
	if err := pool.QueryRow(context.Background(),
		`SELECT string_agg(a.attname, ',' ORDER BY k.ord)
		 FROM pg_index i
		 JOIN pg_class c ON c.oid = i.indrelid
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON TRUE
		 JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
		 WHERE n.nspname = current_schema() AND c.relname = $1 AND i.indisprimary`,
		table).Scan(&cols); err != nil {
		t.Fatalf("read %s primary key: %v", table, err)
	}
	if cols == nil {
		return ""
	}
	return *cols
}

// TestProjectionAndClaimPrimaryKeys pins the two keys later milestones depend
// on without re-checking them.
//
// event_claims' composite PK is Appendix B item 1: "INSERT ... ON CONFLICT DO
// NOTHING on (event_id, consumer)" is what makes dispatch exactly-once under
// crash and redelivery. If the PK were (event_id) alone, two consumers could
// never both claim an event; if it were absent, ON CONFLICT has nothing to
// conflict on and every redelivery double-spawns. M1b will assume this.
func TestProjectionAndClaimPrimaryKeys(t *testing.T) {
	_, pool := newTestSchema(t)
	if got := primaryKeyColumns(t, pool, "event_claims"); got != "event_id,consumer" {
		t.Errorf("event_claims primary key is (%s), want (event_id,consumer) — Appendix B item 1's ON CONFLICT DO NOTHING dispatch claim depends on exactly this key", got)
	}
	if got := primaryKeyColumns(t, pool, "open_contracts"); got != "event_id" {
		t.Errorf("open_contracts primary key is (%s), want (event_id)", got)
	}
}

// TestEvents_AppendixAIndexesPresent pins the two read paths Appendix A names:
// per-workflow-instance replay and per-project cursor scans. Both are ordered
// by seq, so an index on the leading column alone does not serve them.
func TestEvents_AppendixAIndexesPresent(t *testing.T) {
	_, pool := newTestSchema(t)
	rows, err := pool.Query(context.Background(),
		`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND tablename = 'events'`)
	if err != nil {
		t.Fatalf("query pg_indexes: %v", err)
	}
	defer rows.Close()
	var defs []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatalf("scan: %v", err)
		}
		defs = append(defs, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(defs) == 0 {
		t.Fatal("events has no indexes at all — including the primary key, so this instrument is not reading the right table")
	}

	for _, want := range []string{"(workflow_instance_id, seq)", "(project_id, seq)"} {
		found := false
		for _, d := range defs {
			if strings.Contains(d, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no index on events %s; indexes present: %v", want, defs)
		}
	}
}
