package uiapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// rowsStub is a pgx.Rows over a fixed set of already-typed columns.
type rowsStub struct {
	rows    [][]any
	i       int
	scanErr error
	err     error
	closed  bool
}

func (r *rowsStub) Next() bool {
	if r.i >= len(r.rows) {
		return false
	}
	r.i++
	return true
}

func (r *rowsStub) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	row := r.rows[r.i-1]
	for i := range dest {
		switch d := dest[i].(type) {
		case *int64:
			*d = row[i].(int64)
		case *uuid.UUID:
			*d = row[i].(uuid.UUID)
		case *string:
			*d = row[i].(string)
		case *time.Time:
			*d = row[i].(time.Time)
		case *json.RawMessage:
			*d = row[i].(json.RawMessage)
		case *bool:
			*d = row[i].(bool)
		case *int:
			*d = row[i].(int)
		case *float64:
			*d = row[i].(float64)
		default:
			return errors.New("rowsStub: unhandled destination type")
		}
	}
	return nil
}

func (r *rowsStub) Close()                                       { r.closed = true }
func (r *rowsStub) Err() error                                   { return r.err }
func (r *rowsStub) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *rowsStub) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *rowsStub) Values() ([]any, error)                       { return nil, nil }
func (r *rowsStub) RawValues() [][]byte                          { return nil }
func (r *rowsStub) Conn() *pgx.Conn                              { return nil }

// queryPool is a Pool that answers Query from a canned rowsStub and records the
// SQL and arguments it was given.
type queryPool struct {
	rows    *rowsStub
	err     error
	gotSQL  string
	gotArgs []any
}

func (p *queryPool) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	p.gotSQL, p.gotArgs = sql, args
	if p.err != nil {
		return nil, p.err
	}
	return p.rows, nil
}

func (p *queryPool) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("ListEvents must not use QueryRow")
}
func (p *queryPool) Ping(context.Context) error { return nil }

func eventRow(seq int64, typeName string, payload string) []any {
	return []any{seq, uuid.New(), typeName, time.Now().UTC(), uuid.New(), uuid.New(), json.RawMessage(payload)}
}

// TestListEvents_ScansAndPreservesOrder covers the happy path and, critically,
// that the reader does not re-sort: the SQL orders by seq DESC and Go must not
// have an opinion about it.
func TestListEvents_ScansAndPreservesOrder(t *testing.T) {
	rows := &rowsStub{rows: [][]any{
		eventRow(3, "run_finished", `{"outcome":"ok"}`),
		eventRow(2, "turn_finished", `{"cost_usd":0.5}`),
		eventRow(1, "run_started", `{}`),
	}}
	pool := &queryPool{rows: rows}

	got, err := PgEventReader{Pool: pool}.ListEvents(context.Background(), ListOptions{Limit: 3})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	for i, wantSeq := range []int64{3, 2, 1} {
		if got[i].Seq != wantSeq {
			t.Errorf("event %d has seq %d, want %d — the ledger browser opens newest-first and the handler must not reorder the query's result", i, got[i].Seq, wantSeq)
		}
	}
	if got[0].Type != "run_finished" {
		t.Errorf("type = %q, want the schema NAME resolved by the join, not an id", got[0].Type)
	}
	if string(got[1].Payload) != `{"cost_usd":0.5}` {
		t.Errorf("payload = %s, want the raw jsonb passed through byte-for-byte — round-tripping it through Go reorders keys and mangles numeric precision", got[1].Payload)
	}
	if !rows.closed {
		t.Error("rows were not closed, so the connection leaks back to the pool held")
	}
}

// TestListEvents_PassesTheLimitToPostgres. Without this the reader could fetch
// the whole ledger and slice in Go, which is the unbounded scan MaxEventLimit
// exists to prevent.
func TestListEvents_PassesTheLimitToPostgres(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	if _, err := (PgEventReader{Pool: pool}).ListEvents(context.Background(), ListOptions{Limit: 42}); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(pool.gotArgs) != 4 || pool.gotArgs[3] != 42 {
		t.Errorf("query args = %v, want the limit bound as the last parameter", pool.gotArgs)
	}
	if !strings.Contains(strings.ToUpper(pool.gotSQL), "LIMIT $4") {
		t.Errorf("SQL does not bind the limit: %s", pool.gotSQL)
	}
}

// TestListEvents_EmptyLedgerIsANonNilSlice pins the `[]Event{}` initialisation,
// which is what makes an empty ledger encode as [] rather than null.
func TestListEvents_EmptyLedgerIsANonNilSlice(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	got, err := PgEventReader{Pool: pool}.ListEvents(context.Background(), ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if got == nil {
		t.Fatal("an empty ledger returned a nil slice, which marshals to `null` and makes every consumer handle a second empty case")
	}
}

// TestListEvents_SurfacesFailures covers the three distinct ways this can fail.
// rows.Err() is the one worth the line: a scan loop that ignores it reports a
// TRUNCATED ledger as a complete one, which is silent data loss in the view
// whose whole job is showing everything.
func TestListEvents_SurfacesFailures(t *testing.T) {
	tests := []struct {
		name string
		pool *queryPool
		want string
	}{
		{
			name: "the query itself fails",
			pool: &queryPool{err: errors.New("permission denied for table events")},
			want: "permission denied",
		},
		{
			name: "a row fails to scan",
			pool: &queryPool{rows: &rowsStub{rows: [][]any{eventRow(1, "x", "{}")}, scanErr: errors.New("bad column type")}},
			want: "bad column type",
		},
		{
			name: "iteration ended early",
			pool: &queryPool{rows: &rowsStub{err: errors.New("connection closed mid-result")}},
			want: "connection closed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PgEventReader{Pool: tc.pool}.ListEvents(context.Background(), ListOptions{Limit: 10})
			if err == nil {
				t.Fatalf("ListEvents returned %v and no error — a partial or failed read reported as a complete ledger is silent data loss", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not carry the cause %q", err, tc.want)
			}
		})
	}
}

// The filters are the only ones the ledger's indexes serve: (project_id, seq),
// (workflow_instance_id, seq) and seq itself. Bound as parameters and asserted
// positionally, because the SQL's ($n IS NULL OR ...) branches are position
// dependent — swapping two of them would filter by the wrong column while every
// individual filter still "worked".
func TestListEvents_BindsEveryFilterInOrder(t *testing.T) {
	projID, wfID := uuid.New(), uuid.New()
	before := int64(500)
	pool := &queryPool{rows: &rowsStub{}}
	opts := ListOptions{Limit: 10, ProjectID: &projID, WorkflowInstanceID: &wfID, BeforeSeq: &before}
	if _, err := (PgEventReader{Pool: pool}).ListEvents(context.Background(), opts); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(pool.gotArgs) != 4 {
		t.Fatalf("query got %d args, want 4", len(pool.gotArgs))
	}
	for i, want := range []any{&projID, &wfID, &before, 10} {
		if pool.gotArgs[i] != want {
			t.Errorf("arg $%d = %#v, want %#v — the ($n IS NULL OR ...) branches are position dependent", i+1, pool.gotArgs[i], want)
		}
	}
	for _, clause := range []string{
		"($1::uuid IS NULL OR e.project_id = $1::uuid)",
		"($2::uuid IS NULL OR e.workflow_instance_id = $2::uuid)",
		"($3::bigint IS NULL OR e.seq < $3::bigint)",
	} {
		if !strings.Contains(pool.gotSQL, clause) {
			t.Errorf("SQL is missing the nullable filter %s:\n%s", clause, pool.gotSQL)
		}
	}
}

// An absent filter must be a typed nil, not a zero value: uuid.Nil is a real
// uuid as far as the ($1 IS NULL) branch is concerned, so it would filter the
// ledger down to events with a nil project instead of returning all of them.
func TestListEvents_AnAbsentFilterIsNullNotZero(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	if _, err := (PgEventReader{Pool: pool}).ListEvents(context.Background(), ListOptions{Limit: 10}); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for i, name := range []string{"project_id", "workflow_instance_id"} {
		got, ok := pool.gotArgs[i].(*uuid.UUID)
		if !ok || got != nil {
			t.Errorf("%s arg = %#v, want a nil *uuid.UUID", name, pool.gotArgs[i])
		}
	}
	if got, ok := pool.gotArgs[2].(*int64); !ok || got != nil {
		t.Errorf("before_seq arg = %#v, want a nil *int64", pool.gotArgs[2])
	}
}

// Pagination is keyset, not OFFSET. On an append-only log OFFSET also SHIFTS:
// rows arrive at the head between requests, so page 2 by offset re-shows rows
// the reader already saw and skips ones they did not.
func TestListEvents_PaginatesByKeysetNotOffset(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	if _, err := (PgEventReader{Pool: pool}).ListEvents(context.Background(), ListOptions{Limit: 10}); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if strings.Contains(strings.ToUpper(pool.gotSQL), "OFFSET") {
		t.Errorf("the ledger query uses OFFSET, which both costs N pages and shifts as rows are appended:\n%s", pool.gotSQL)
	}
}

// No ?type= and no time filter, deliberately: `schema_id` and `at` have no
// index, so either one turns a browser-reachable endpoint into a sequential
// scan of the whole ledger. Pinned because "add a type filter" is an obvious
// request and the reason it is absent is not visible from the endpoint.
func TestParseListOptions_OffersOnlyIndexedFilters(t *testing.T) {
	q := url.Values{}
	for _, unindexed := range []string{"type", "since", "until", "before", "after", "owner_agent_id"} {
		q.Set(unindexed, "anything")
	}
	opts, err := parseListOptions(q)
	if err != nil {
		t.Fatalf("parseListOptions: %v", err)
	}
	// Accepted-and-ignored is the failure this guards: a caller who filters by
	// ?type= and gets the unfiltered ledger has a wrong answer that looks right.
	// So the assertion is that none of them became a filter.
	if opts.ProjectID != nil || opts.WorkflowInstanceID != nil || opts.BeforeSeq != nil || opts.Bucket != "" {
		t.Errorf("an unindexed query parameter was interpreted as a filter: %+v", opts)
	}
}

func TestParseListOptions_ValidatesTheKeysetCursor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		want    int64
		wantErr string
	}{
		{name: "the first usable cursor", raw: "1", want: 1},
		{name: "a large cursor", raw: "999999", want: 999999},
		{name: "zero is refused", raw: "0", wantErr: "at least 1"},
		{name: "negative is refused", raw: "-5", wantErr: "at least 1"},
		{name: "non-numeric is refused", raw: "head", wantErr: "must be an integer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseListOptions(url.Values{"before_seq": {tc.raw}})
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("before_seq=%q was accepted as %v — seq starts at 1, so this cursor can never match and an empty page reads as \"no events\" rather than \"bad cursor\"", tc.raw, opts.BeforeSeq)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("before_seq=%q: %v", tc.raw, err)
			}
			if opts.BeforeSeq == nil || *opts.BeforeSeq != tc.want {
				t.Errorf("before_seq = %v, want %d", opts.BeforeSeq, tc.want)
			}
		})
	}
}

func TestParseListOptions_RejectsAMalformedWorkflowInstanceID(t *testing.T) {
	_, err := parseListOptions(url.Values{"workflow_instance_id": {"not-a-uuid"}})
	if err == nil {
		t.Fatal("a malformed workflow_instance_id was accepted — a dropped filter answers a question about one instance with every instance's events")
	}
	if !strings.Contains(err.Error(), "workflow_instance_id") {
		t.Errorf("error = %q, want it to name the parameter that was wrong", err)
	}
}
