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

// fakeWorkflows is a WorkflowReader that records what it was asked for.
type fakeWorkflows struct {
	workflows []Workflow
	err       error
	gotOpts   ListOptions
}

func (f *fakeWorkflows) ListWorkflows(_ context.Context, opts ListOptions) ([]Workflow, error) {
	f.gotOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.workflows, nil
}

func workflowRow(openContracts int) []any {
	return []any{
		uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		"git@github.com:dmotles/sprawl.git",
		int(9), openContracts,
		int64(100), int64(140),
		time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 9, 11, 30, 0, 0, time.UTC),
	}
}

func TestPgWorkflowReader_ListWorkflows(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{rows: [][]any{workflowRow(2)}}}

	got, err := PgWorkflowReader{Pool: pool}.ListWorkflows(context.Background(), ListOptions{Limit: 50})
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	want := Workflow{
		WorkflowInstanceID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		ProjectID:          uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		ProjectName:        "sprawl",
		EventCount:         9,
		OpenContracts:      2,
		FirstSeq:           100,
		LastSeq:            140,
		FirstAt:            time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC),
		LastAt:             time.Date(2026, 9, 9, 11, 30, 0, 0, time.UTC),
		State:              WorkflowInFlight,
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("workflow = %+v, want %+v", got, want)
	}
}

// State is derived, so the boundary between the two values is the whole of the
// rule and both sides of it need pinning. A reader that hard-coded either value
// would satisfy one case and fail the other.
func TestPgWorkflowReader_DerivesStateFromOpenContracts(t *testing.T) {
	for _, tc := range []struct {
		name          string
		openContracts int
		want          string
	}{
		{"no open contracts is settled", 0, WorkflowSettled},
		{"one open contract is in flight", 1, WorkflowInFlight},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := &queryPool{rows: &rowsStub{rows: [][]any{workflowRow(tc.openContracts)}}}
			got, err := PgWorkflowReader{Pool: pool}.ListWorkflows(context.Background(), ListOptions{Limit: 50})
			if err != nil {
				t.Fatalf("ListWorkflows: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d workflows, want 1", len(got))
			}
			if got[0].State != tc.want {
				t.Errorf("state = %q with %d open contracts, want %q", got[0].State, tc.openContracts, tc.want)
			}
		})
	}
}

// Settled instances must remain listable. store/operability.go's openWorkflowsSQL
// ends in `HAVING count(oc.event_id) > 0` because the CLI answers "what is
// outstanding?"; copying that clause into a browsable view would make an
// instance disappear the moment its last contract closed, which reads as data
// loss rather than as completion. Asserted on the SQL because the stub returns
// whatever rows it was handed and so cannot notice a filter.
func TestPgWorkflowReader_DoesNotHideSettledInstances(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	if _, err := (PgWorkflowReader{Pool: pool}).ListWorkflows(context.Background(), ListOptions{Limit: 10}); err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if strings.Contains(pool.gotSQL, "HAVING") {
		t.Errorf("the workflows query filters with HAVING, so settled instances would vanish from the view:\n%s", pool.gotSQL)
	}
	// The other half: the annotating outer join is what makes event_count and
	// open_contract_count different numbers. An inner join would silently make
	// both equal AND reintroduce the hiding this test just forbade, from a
	// query with no HAVING in it.
	if !strings.Contains(pool.gotSQL, "LEFT JOIN open_contracts") {
		t.Errorf("open_contracts is not OUTER joined, so events without a contract row are dropped:\n%s", pool.gotSQL)
	}
}

func TestPgWorkflowReader_PassesTheProjectFilterThrough(t *testing.T) {
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
			if _, err := (PgWorkflowReader{Pool: pool}).ListWorkflows(context.Background(), tc.opts); err != nil {
				t.Fatalf("ListWorkflows: %v", err)
			}
			if len(pool.gotArgs) != 2 {
				t.Fatalf("query got %d args, want 2", len(pool.gotArgs))
			}
			if got, ok := pool.gotArgs[0].(*uuid.UUID); !ok || got != tc.want {
				t.Errorf("project arg = %#v, want %#v — the SQL's ($1 IS NULL) branch needs a typed nil, not uuid.Nil", pool.gotArgs[0], tc.want)
			}
		})
	}
}

// The raw remote_url can carry an internal host, an org name, or embedded
// credentials. It is read by the query and must die in the scan loop.
func TestWorkflow_JSONCarriesNoRemoteURL(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{rows: [][]any{{
		uuid.New(), uuid.New(),
		"https://bot:tok3n@git.internal.example.com/some-org/widget.git",
		1, 0, int64(1), int64(1), time.Now(), time.Now(),
	}}}}
	got, err := PgWorkflowReader{Pool: pool}.ListWorkflows(context.Background(), ListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{"git.internal.example.com", "some-org", "tok3n", "https"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("the workflows JSON carries %q from remote_url: %s", secret, blob)
		}
	}
}

func TestHandleWorkflows_ServesTheNamedEnvelope(t *testing.T) {
	reader := &fakeWorkflows{workflows: []Workflow{}}
	cfg := fullConfig()
	cfg.Workflows = reader
	rec := get(t, cfg, "/api/workflows?limit=5")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"workflows":[]`) {
		t.Errorf("no workflows encoded as %s, want {\"workflows\":[]}", strings.TrimSpace(body))
	}
	if reader.gotOpts.Limit != 5 {
		t.Errorf("reader asked for limit %d, want 5", reader.gotOpts.Limit)
	}
}

func TestHandleWorkflows_DoesNotLeakTheDatabaseError(t *testing.T) {
	var logged strings.Builder
	cfg := fullConfig()
	cfg.Workflows = &fakeWorkflows{err: errPgDetail}
	cfg.Logger = textLogger(&logged)
	rec := get(t, cfg, "/api/workflows")
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
