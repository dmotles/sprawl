package uiapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeUsage is a UsageReader that records what it was asked for.
type fakeUsage struct {
	buckets []UsageBucket
	err     error
	gotOpts ListOptions
}

func (f *fakeUsage) ListUsage(_ context.Context, opts ListOptions) ([]UsageBucket, error) {
	f.gotOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.buckets, nil
}

func TestPgUsageReader_ListUsage(t *testing.T) {
	projID := uuid.New()
	start := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	pool := &queryPool{rows: &rowsStub{rows: [][]any{
		{start, projID, "git@github.com:dmotles/sprawl.git", 12.5, 3, 88, int64(410000), int64(52000)},
	}}}

	got, err := PgUsageReader{Pool: pool}.ListUsage(context.Background(), ListOptions{Limit: 30, Bucket: BucketDay})
	if err != nil {
		t.Fatalf("ListUsage: %v", err)
	}
	want := UsageBucket{
		BucketStart: start, Bucket: BucketDay, ProjectID: projID, ProjectName: "sprawl",
		CostUSD: 12.5, Sessions: 3, Turns: 88, InputTokens: 410000, OutputTokens: 52000,
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("bucket = %+v, want %+v", got, want)
	}
}

// THE defect this endpoint exists to avoid (QUM-1247). The CLI reports cost per
// turn cumulatively for the session, so summing turn_finished.cost_usd counts
// turn 1 N times and overstates spend by a measured 4-10x. A reviewer cannot
// see that from the query, which is why it is an assertion and not a comment.
func TestPgUsageReader_TakesCostFromRunsAndNeverFromTurns(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	if _, err := (PgUsageReader{Pool: pool}).ListUsage(context.Background(), ListOptions{Limit: 10}); err != nil {
		t.Fatalf("ListUsage: %v", err)
	}
	// The turn_finished branch must not read cost_usd at all. Asserted by
	// locating the clause that filters to turn_finished and checking the
	// SELECT list that feeds it, because a bare "no cost_usd anywhere" check
	// would forbid the run_finished cost this endpoint is built on.
	turns := section(t, pool.gotSQL, "), tokens AS (", "	)\n")
	if strings.Contains(turns, "cost_usd") {
		t.Errorf("the turn_finished CTE reads cost_usd — per-turn costs are session-cumulative, so summing them overstates spend 4-10x (QUM-1247):\n%s", turns)
	}
	runs := section(t, pool.gotSQL, "WITH sessions AS (", "	), spend AS (")
	if !strings.Contains(runs, "'run_finished'") || !strings.Contains(runs, "cost_usd") {
		t.Errorf("spend does not come from run_finished's cost_usd, so the view has no cost source at all:\n%s", runs)
	}
}

// run_finished is itself a session total, so two of them for one session are two
// statements of the same number rather than two costs to add. DISTINCT ON with
// `ORDER BY session_id, seq DESC` is what makes the picked row the LAST one; the
// ordering is semantics here, not presentation, so both halves are pinned.
func TestPgUsageReader_CountsEachSessionsCostOnce(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	if _, err := (PgUsageReader{Pool: pool}).ListUsage(context.Background(), ListOptions{Limit: 10}); err != nil {
		t.Fatalf("ListUsage: %v", err)
	}
	if !strings.Contains(pool.gotSQL, `DISTINCT ON (e.payload->>'session_id')`) {
		t.Errorf("run_finished rows are not deduplicated per session, so a session with two of them is billed twice:\n%s", pool.gotSQL)
	}
	if !strings.Contains(pool.gotSQL, `ORDER BY e.payload->>'session_id', e.seq DESC`) {
		t.Errorf("DISTINCT ON picks an arbitrary run_finished rather than the last one — without seq DESC the cost is whichever row the planner reached first:\n%s", pool.gotSQL)
	}
}

// A bucket can have turns but no finished run (work in progress) or a finished
// run but no retained turns (spilled). An inner or left join drops one of those,
// and the row it drops is real activity.
func TestPgUsageReader_KeepsBucketsWithOnlyOneSide(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	if _, err := (PgUsageReader{Pool: pool}).ListUsage(context.Background(), ListOptions{Limit: 10}); err != nil {
		t.Fatalf("ListUsage: %v", err)
	}
	if !strings.Contains(pool.gotSQL, "FULL OUTER JOIN tokens") {
		t.Errorf("spend and tokens are not FULL OUTER joined, so a bucket present on only one side vanishes:\n%s", pool.gotSQL)
	}
}

// ?bucket= reaches date_trunc as a bound parameter, and the value is checked
// against a closed set. Both halves matter: the reader must refuse an unknown
// bucket, and it must not concatenate the accepted one into the SQL.
func TestPgUsageReader_ValidatesTheBucket(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bucket  string
		wantArg string
		wantErr bool
	}{
		{name: "empty means the day", bucket: "", wantArg: BucketDay},
		{name: "hour is accepted", bucket: BucketHour, wantArg: BucketHour},
		{name: "day is accepted", bucket: BucketDay, wantArg: BucketDay},
		{name: "an unknown width is refused", bucket: "week", wantErr: true},
		{name: "a SQL fragment is refused", bucket: "day); DROP TABLE events; --", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := &queryPool{rows: &rowsStub{}}
			_, err := PgUsageReader{Pool: pool}.ListUsage(context.Background(), ListOptions{Limit: 10, Bucket: tc.bucket})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ListUsage accepted bucket %q — an unchecked value reaches date_trunc", tc.bucket)
				}
				if pool.gotSQL != "" {
					t.Errorf("the query ran despite an invalid bucket: %s", pool.gotSQL)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListUsage(%q): %v", tc.bucket, err)
			}
			if len(pool.gotArgs) != 3 {
				t.Fatalf("query got %d args, want 3", len(pool.gotArgs))
			}
			if got := pool.gotArgs[1]; got != tc.wantArg {
				t.Errorf("bucket arg = %#v, want %q", got, tc.wantArg)
			}
			// The negative control on the same subject: the value travels as a
			// parameter, so it must NOT appear in the statement text.
			if strings.Contains(pool.gotSQL, "date_trunc('") {
				t.Errorf("the bucket is interpolated into the SQL rather than bound:\n%s", pool.gotSQL)
			}
		})
	}
}

func TestPgUsageReader_PassesTheProjectFilterThrough(t *testing.T) {
	id := uuid.New()
	for _, tc := range []struct {
		name string
		opts ListOptions
		want any
	}{
		{"unfiltered sends a nil project", ListOptions{Limit: 10}, (*uuid.UUID)(nil)},
		{"filtered sends the id", ListOptions{Limit: 10, ProjectID: &id}, &id},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := &queryPool{rows: &rowsStub{}}
			if _, err := (PgUsageReader{Pool: pool}).ListUsage(context.Background(), tc.opts); err != nil {
				t.Fatalf("ListUsage: %v", err)
			}
			if len(pool.gotArgs) != 3 {
				t.Fatalf("query got %d args, want 3", len(pool.gotArgs))
			}
			if got, ok := pool.gotArgs[0].(*uuid.UUID); !ok || got != tc.want {
				t.Errorf("project arg = %#v, want %#v", pool.gotArgs[0], tc.want)
			}
		})
	}
}

func TestUsageBucket_JSONCarriesNoRemoteURL(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{rows: [][]any{
		{
			time.Now(), uuid.New(), "https://bot:tok3n@git.internal.example.com/some-org/widget.git",
			1.0, 1, 1, int64(1), int64(1),
		},
	}}}
	got, err := PgUsageReader{Pool: pool}.ListUsage(context.Background(), ListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("ListUsage: %v", err)
	}
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{"git.internal.example.com", "some-org", "tok3n", "https"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("the usage JSON carries %q from remote_url: %s", secret, blob)
		}
	}
}

func TestHandleUsage_ServesTheNamedEnvelope(t *testing.T) {
	reader := &fakeUsage{buckets: []UsageBucket{}}
	cfg := fullConfig()
	cfg.Usage = reader
	rec := get(t, cfg, "/api/usage?limit=5&bucket=hour")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"usage":[]`) {
		t.Errorf("no usage encoded as %s, want {\"usage\":[]}", strings.TrimSpace(body))
	}
	if reader.gotOpts.Limit != 5 || reader.gotOpts.Bucket != BucketHour {
		t.Errorf("reader asked for %+v, want limit 5 and bucket %q", reader.gotOpts, BucketHour)
	}
}

// An unknown ?bucket= is a 400, and the rejection happens before the query: a
// silent fallback to the day answers a question nobody asked and does not say
// it substituted.
func TestHandleUsage_BadBucketIs400AndNeverReachesTheDatabase(t *testing.T) {
	reader := &fakeUsage{gotOpts: ListOptions{Limit: -1}}
	cfg := fullConfig()
	cfg.Usage = reader
	rec := get(t, cfg, "/api/usage?bucket=week")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if reader.gotOpts.Limit != -1 {
		t.Errorf("the reader was called despite an invalid ?bucket=: %+v", reader.gotOpts)
	}
}

func TestHandleUsage_DoesNotLeakTheDatabaseError(t *testing.T) {
	var logged strings.Builder
	cfg := fullConfig()
	cfg.Usage = &fakeUsage{err: errPgDetail}
	cfg.Logger = textLogger(&logged)
	rec := get(t, cfg, "/api/usage")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "sprawl_internal_secrets") {
		t.Errorf("the response body leaks the database error: %s", strings.TrimSpace(body))
	}
	if !strings.Contains(logged.String(), "sprawl_internal_secrets") {
		t.Errorf("the real error was dropped rather than logged: %s", logged.String())
	}
}

// section returns the substring between two markers, failing the test when
// either is absent. Used to assert about ONE CTE of a multi-CTE query: a check
// over the whole statement cannot distinguish "turns read cost_usd" (the bug)
// from "runs read cost_usd" (the design). It fails loudly rather than returning
// "" so that a rewrite of the query turns these tests red instead of vacuous.
func section(t *testing.T, sql, from, to string) string {
	t.Helper()
	i := strings.Index(sql, from)
	if i < 0 {
		t.Fatalf("the query has no %q marker, so this assertion no longer looks at anything:\n%s", from, sql)
	}
	rest := sql[i+len(from):]
	j := strings.Index(rest, to)
	if j < 0 {
		t.Fatalf("the query has no %q marker after %q:\n%s", to, from, sql)
	}
	return rest[:j]
}
