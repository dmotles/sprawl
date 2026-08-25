package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/card"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Hermetic tests for the publish and list paths.
//
// The SEMANTICS — a republish of the same name@version actually being refused by
// Postgres, and the app role actually being denied UPDATE — are exercised against
// a real database in publish_integration_test.go, because those are properties of
// the schema and the grants rather than of this Go code. What is worth pinning
// HERE is the statement shape and the arg/scan order, because every defect in
// those is SILENT: an ON CONFLICT clause turns a refused republish into a
// successful no-op, and every column on this table is TEXT, so a transposed pair
// is not a type error — it is a card published with its description in the prompt
// column, which spawns an agent whose entire system prompt is one sentence.

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// stubPublishPool is a CardStore that records the one Exec it is asked to run,
// can be told to fail it, and can serve a canned row set to Query.
type stubPublishPool struct {
	sql     string
	args    []any
	execErr error

	rows     *stubRows
	queryErr error
}

func (p *stubPublishPool) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	p.sql, p.args = sql, args
	if p.execErr != nil {
		return pgconn.NewCommandTag(""), p.execErr
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (p *stubPublishPool) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	p.sql = sql
	if p.queryErr != nil {
		return nil, p.queryErr
	}
	return p.rows, nil
}

func (p *stubPublishPool) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("stubPublishPool: Begin is not stubbed")
}

func (p *stubPublishPool) QueryRow(context.Context, string, ...any) pgx.Row {
	return errRow{err: errors.New("stubPublishPool: QueryRow is not stubbed")}
}

func (p *stubPublishPool) Ping(context.Context) error { return nil }

// stubRows serves pre-built column value slices to a pgx-style scan loop. Only
// the methods ListCards uses are implemented; the embedded nil pgx.Rows makes any
// other call panic loudly rather than return a plausible zero.
type stubRows struct {
	pgx.Rows
	vals [][]any
	i    int
	err  error
}

func (r *stubRows) Next() bool { r.i++; return r.i <= len(r.vals) }
func (r *stubRows) Err() error { return r.err }
func (r *stubRows) Close()     {}

func (r *stubRows) Scan(dest ...any) error {
	row := r.vals[r.i-1]
	if len(dest) != len(row) {
		return fmt.Errorf("stubRows: row has %d values, Scan wants %d", len(row), len(dest))
	}
	for i, d := range dest {
		switch d := d.(type) {
		case *string:
			*d = row[i].(string)
		case *int:
			*d = row[i].(int)
		case *[]byte:
			*d = row[i].([]byte)
		default:
			return fmt.Errorf("stubRows: unsupported scan destination %T at %d", d, i)
		}
	}
	return nil
}

func testPublishCard() *card.Card {
	return &card.Card{
		Name:          "slim-engineer",
		Version:       2,
		AgentType:     "engineer",
		Description:   "a card under test",
		Model:         "haiku",
		Effort:        "high",
		Body:          "You are {{AGENT_NAME}}.",
		ContentSHA256: "deadbeef",
		Opts:          card.RenderOpts{SubagentBanner: true},
	}
}

// ---------------------------------------------------------------------------
// Statement shape
// ---------------------------------------------------------------------------

// TestPublishCardSQL_HasNoOnConflict is the immutability mechanism, stated as a
// test because it is a PROHIBITION and prohibitions rot silently.
//
// syncSeedCards deliberately uses ON CONFLICT (id) DO NOTHING: it re-runs on
// every process that opens a Ledger, so a conflict there is the normal case.
// Publish is the exact opposite — a conflict is the OPERATOR'S ERROR, the one
// thing AC1's "rows are immutable" is about, and DO NOTHING would report the
// republish as a success while the old card kept serving every spawn. Copying
// the sync statement is the obvious way to write this function, which is why the
// prohibition is pinned rather than trusted.
func TestPublishCardSQL_HasNoOnConflict(t *testing.T) {
	sql := normalize(publishCardSQL)
	if !strings.Contains(sql, "insert into agent_cards (id,") {
		t.Fatalf("publishCardSQL does not insert into agent_cards writing the derived id first, so this test is not looking at the publish statement: %s", sql)
	}
	if strings.Contains(sql, "on conflict") {
		t.Errorf("publishCardSQL carries an ON CONFLICT clause, so republishing an existing name@version would report success and silently keep serving the old card: %s", sql)
	}
	if strings.Contains(sql, "do update") {
		t.Errorf("publishCardSQL can DO UPDATE, which is publish editing an immutable row in place: %s", sql)
	}
}

// TestPublishCardSQL_PlaceholdersAreInColumnOrder pins the VALUES list itself.
//
// Not the placeholder COUNT: a `$5, $4` transposition inside VALUES keeps the
// count at ten and keeps PublishCard's Go arg order correct, and publishes the
// description into the prompt column. The count assertion is green for that; the
// literal list is not.
func TestPublishCardSQL_PlaceholdersAreInColumnOrder(t *testing.T) {
	sql := normalize(publishCardSQL)
	want := "values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)"
	if !strings.Contains(sql, want) {
		t.Errorf("publishCardSQL's VALUES list is not %q — a transposed placeholder writes one column's value into another, and every column here is text so nothing errors: %s", want, sql)
	}
}

// TestCardStatements_ShareOneColumnList. Publish, list and the SPAWN path must
// agree on the column list, and the only durable way to make them agree is for
// all three to be built from cardColumns. This asserts the sharing is real: the
// defect it catches is somebody re-inlining a literal list in one of them, which
// is how CardForType was written before this slice, and which drifts without
// ever failing — publish writes a row the spawn path then refuses to read.
func TestCardStatements_ShareOneColumnList(t *testing.T) {
	cols := normalize(cardColumns)
	if len(strings.Split(cols, ", ")) < 9 {
		t.Fatalf("cardColumns has fewer columns than agent_cards has meaningful ones (%q); this test would be trivially satisfiable", cols)
	}
	for name, sql := range map[string]string{
		"publishCardSQL": publishCardSQL,
		"listCardsSQL":   listCardsSQL,
		"cardForTypeSQL": cardForTypeSQL,
	} {
		if !strings.Contains(normalize(sql), cols) {
			t.Errorf("%s does not carry the shared column list %q verbatim, so it can drift from the other statements silently: %s", name, cols, normalize(sql))
		}
	}
}

// TestListCardsSQL_Shape. `def list` is AC1's evidence, and its two silent
// failure modes are both in this statement: no ORDER BY makes the listing
// arbitrary, so an operator cannot see which version wins; and a WHERE clause
// would hide exactly the rows an immutability check needs to see.
func TestListCardsSQL_Shape(t *testing.T) {
	sql := normalize(listCardsSQL)
	if !strings.Contains(sql, "from agent_cards") {
		t.Fatalf("listCardsSQL does not read agent_cards: %s", sql)
	}
	if strings.Contains(sql, "where") {
		t.Errorf("listCardsSQL filters rows; `def list` must show every published card, including ones this build has no seed for: %s", sql)
	}
	if !strings.Contains(sql, "order by agent_type, version") {
		t.Errorf("listCardsSQL has no (agent_type, version) ORDER BY, so the listing cannot show which version wins per type: %s", sql)
	}
}

// ---------------------------------------------------------------------------
// PublishCard
// ---------------------------------------------------------------------------

// TestPublishCard_PassesTheCardInColumnOrder — the Go half of the transposition
// class pinned in the SQL half above.
func TestPublishCard_PassesTheCardInColumnOrder(t *testing.T) {
	c := testPublishCard()
	pool := &stubPublishPool{}
	if err := PublishCard(context.Background(), pool, c); err != nil {
		t.Fatalf("PublishCard: %v", err)
	}
	if len(pool.args) != 10 {
		t.Fatalf("PublishCard passed %d args, want 10", len(pool.args))
	}
	for i, want := range []any{any(c.ID()), any(c.Name), any(c.Version), any(c.AgentType), any(c.Description), any(c.Body), any(c.Model), any(c.Effort), any(c.ContentSHA256)} {
		if pool.args[i] != want {
			t.Errorf("arg %d = %v, want %v", i+1, pool.args[i], want)
		}
	}
	renderJSON, ok := pool.args[9].([]byte)
	if !ok {
		t.Fatalf("the render arg is %T, want encoded json", pool.args[9])
	}
	if !strings.Contains(string(renderJSON), `"subagent_banner":true`) {
		t.Errorf("the render arg %q does not carry the card's render options", renderJSON)
	}
}

// TestPublishCard_UniqueViolationIsReportedAsAlreadyPublished, with the
// other-pg-error and not-a-pg-error legs as controls: a mapping that fired on
// every failure would make "already published" the message for a dead
// connection, and would tell the operator to bump a version over an outage.
func TestPublishCard_UniqueViolationIsReportedAsAlreadyPublished(t *testing.T) {
	for _, tt := range []struct {
		name     string
		execErr  error
		want     bool
		wantPgIs bool
	}{
		{"unique violation", &pgconn.PgError{Code: "23505", Message: `duplicate key value violates unique constraint "agent_cards_name_version_key"`}, true, true},
		{"some other pg error", &pgconn.PgError{Code: "42501", Message: "permission denied for table agent_cards"}, false, true},
		{"not a pg error at all", errors.New("connection refused"), false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := PublishCard(context.Background(), &stubPublishPool{execErr: tt.execErr}, testPublishCard())
			if err == nil {
				t.Fatalf("PublishCard returned nil for a failing Exec")
			}
			if got := errors.Is(err, ErrCardAlreadyPublished); got != tt.want {
				t.Errorf("errors.Is(err, ErrCardAlreadyPublished) = %v, want %v (err = %v)", got, tt.want, err)
			}
			// The underlying error must stay reachable, not be flattened into a
			// string: `def publish` decides its exit path on errors.Is, and an
			// operator debugging a 42501 needs the code.
			var pgErr *pgconn.PgError
			if got := errors.As(err, &pgErr); got != tt.wantPgIs {
				t.Errorf("errors.As(err, **pgconn.PgError) = %v, want %v — the cause was flattened (err = %v)", got, tt.wantPgIs, err)
			}
			// name AND version, asserted as the joined "name@version" rather than
			// as two separate substrings. ErrCardAlreadyPublished is (name,
			// version)-scoped, and a message naming only the card cannot tell an
			// operator which publish was refused — but a bare "2" is satisfied by
			// the wrapped pg text, which carries both the SQLSTATE 23505 and the
			// constraint name agent_cards_name_version_key. (Watched: dropping the
			// version from the message left the two-substring form GREEN.)
			if want := "slim-engineer@2"; !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q, so the operator cannot tell which publish failed", err, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ListCards
// ---------------------------------------------------------------------------

// renderJSON encodes a full render-options document. Full rather than partial
// because ParseRenderOpts requires every key to be PRESENT — an absent key and
// an explicit false are the same value once decoded into a struct, so it refuses
// rather than reading "unspecified" as "off".
func renderJSON(t *testing.T, o card.RenderOpts) []byte {
	t.Helper()
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("encoding render options: %v", err)
	}
	return b
}

func listRow(name string, version int, agentType, desc, prompt, model, effort, sha string, render []byte) []any {
	return []any{name, version, agentType, desc, prompt, model, effort, sha, render}
}

// TestListCards_DecodesInColumnOrder is the read-side half of the transposition
// class: rows.Scan's destination order has to match cardColumns, and every
// mismatched pair here is two text columns swapping values with no error.
func TestListCards_DecodesInColumnOrder(t *testing.T) {
	pool := &stubPublishPool{rows: &stubRows{vals: [][]any{
		listRow("slim-engineer", 2, "engineer", "the description", "the prompt", "haiku", "high", "abc123", renderJSON(t, card.RenderOpts{SubagentBanner: true})),
	}}}
	got, err := ListCards(context.Background(), pool)
	if err != nil {
		t.Fatalf("ListCards: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListCards returned %d cards, want 1", len(got))
	}
	c := got[0]
	if c.RenderErr != "" {
		t.Errorf("RenderErr = %q for a row with valid render options", c.RenderErr)
	}
	for _, f := range []struct{ field, got, want string }{
		{"Name", c.Name, "slim-engineer"},
		{"AgentType", c.AgentType, "engineer"},
		{"Description", c.Description, "the description"},
		{"Body", c.Body, "the prompt"},
		{"Model", c.Model, "haiku"},
		{"Effort", c.Effort, "high"},
		{"ContentSHA256", c.ContentSHA256, "abc123"},
	} {
		if f.got != f.want {
			t.Errorf("%s = %q, want %q — the scan order does not match cardColumns", f.field, f.got, f.want)
		}
	}
	if c.Version != 2 {
		t.Errorf("Version = %d, want 2", c.Version)
	}
	if !c.Opts.SubagentBanner {
		t.Errorf("render options were not decoded")
	}
}

// TestListCards_TolerAtesAnUnreadableRenderPerRow. ListCards has the OPPOSITE
// job to CardForType: CardForType refuses a row it cannot fully read, because it
// is on the spawn path and a half-read card launches an agent with a broken
// prompt. `def list` is the diagnostic an operator reaches for BECAUSE something
// is wrong, so a single bad row must not blank the whole listing — but it must
// also not be reported as fine, which is what RenderErr is for.
func TestListCards_TolerAtesAnUnreadableRenderPerRow(t *testing.T) {
	pool := &stubPublishPool{rows: &stubRows{vals: [][]any{
		listRow("legacy-engineer", 1, "engineer", "d", "p", "opus", "low", "sha1", []byte(`not json at all`)),
		listRow("slim-engineer", 2, "engineer", "d", "p", "haiku", "high", "sha2", renderJSON(t, card.RenderOpts{SubagentBanner: true})),
	}}}
	got, err := ListCards(context.Background(), pool)
	if err != nil {
		t.Fatalf("ListCards refused the whole listing over one unreadable row: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListCards returned %d cards, want both rows", len(got))
	}
	if got[0].RenderErr == "" {
		t.Errorf("the row with an unreadable render reports RenderErr = \"\", so `def list` would show it as fine")
	}
	if got[1].RenderErr != "" {
		t.Errorf("the VALID row reports RenderErr = %q; the tolerance is leaking across rows", got[1].RenderErr)
	}
}

// TestListCards_ReportsARowIterationError. rows.Err() is the one failure in a
// pgx scan loop that produces NO error at any other call site: a connection that
// dies mid-iteration simply stops yielding rows, so an unchecked Err() turns a
// truncated listing into a short one that looks complete.
func TestListCards_ReportsARowIterationError(t *testing.T) {
	pool := &stubPublishPool{rows: &stubRows{
		vals: [][]any{listRow("slim-engineer", 2, "engineer", "d", "p", "haiku", "high", "sha", renderJSON(t, card.RenderOpts{}))},
		err:  errors.New("connection reset mid-iteration"),
	}}
	if _, err := ListCards(context.Background(), pool); err == nil {
		t.Errorf("ListCards returned nil error over a failed row iteration, so a truncated listing reads as complete")
	}
}

// TestListCards_ReportsAQueryError — the plain-failure leg, so the tolerance
// above cannot be mistaken for tolerating everything.
func TestListCards_ReportsAQueryError(t *testing.T) {
	pool := &stubPublishPool{queryErr: errors.New("connection refused")}
	if _, err := ListCards(context.Background(), pool); err == nil {
		t.Errorf("ListCards returned nil error when the query itself failed")
	}
}
