package uiapi

import (
	"context"
	"encoding/json"
	"errors"
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

	got, err := PgEventReader{Pool: pool}.ListEvents(context.Background(), 3)
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
	if _, err := (PgEventReader{Pool: pool}).ListEvents(context.Background(), 42); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(pool.gotArgs) != 1 || pool.gotArgs[0] != 42 {
		t.Errorf("query args = %v, want the limit bound as a parameter", pool.gotArgs)
	}
	if !strings.Contains(strings.ToUpper(pool.gotSQL), "LIMIT $1") {
		t.Errorf("SQL does not bind the limit: %s", pool.gotSQL)
	}
}

// TestListEvents_EmptyLedgerIsANonNilSlice pins the `[]Event{}` initialisation,
// which is what makes an empty ledger encode as [] rather than null.
func TestListEvents_EmptyLedgerIsANonNilSlice(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	got, err := PgEventReader{Pool: pool}.ListEvents(context.Background(), 10)
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
			got, err := PgEventReader{Pool: tc.pool}.ListEvents(context.Background(), 10)
			if err == nil {
				t.Fatalf("ListEvents returned %v and no error — a partial or failed read reported as a complete ledger is silent data loss", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not carry the cause %q", err, tc.want)
			}
		})
	}
}
