package uiapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPgGoalReader_ListGoals(t *testing.T) {
	var (
		goalID = uuid.New()
		projID = uuid.New()
		wfID   = uuid.New()
		opened = time.Date(2026, 9, 9, 17, 2, 11, 0, time.UTC)
	)
	pool := &queryPool{rows: &rowsStub{rows: [][]any{
		{goalID, int64(41), opened, projID, "git@github.com:dmotles/sprawl.git", wfID, "research", "tower", false},
	}}}

	got, err := PgGoalReader{Pool: pool}.ListGoals(context.Background(), ListOptions{Limit: 100})
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d goals, want 1", len(got))
	}
	want := Goal{
		ID: goalID, Seq: 41, OpenedAt: opened, ProjectID: projID,
		ProjectName: "sprawl", WorkflowInstanceID: wfID,
		GoalType: "research", Owner: "tower", Legacy: false,
	}
	if got[0] != want {
		t.Errorf("goal = %+v, want %+v", got[0], want)
	}
}

// The three contract types must all be asked for. A query that named only
// `goal_opened` would return a list that looks authoritative and omits every
// prose-spawned agent and every rework.
func TestPgGoalReader_AsksForAllThreeContractTypes(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	if _, err := (PgGoalReader{Pool: pool}).ListGoals(context.Background(), ListOptions{Limit: 10}); err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	types, ok := pool.gotArgs[0].([]string)
	if !ok {
		t.Fatalf("first arg is %T, want []string", pool.gotArgs[0])
	}
	for _, want := range []string{"goal_opened", "agent_spawned", "rework_requested"} {
		found := false
		for _, got := range types {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("contract type %q is not queried for, so those goals are invisible", want)
		}
	}
}

// nil ProjectID must reach the query as a nil, because the SQL's
// `$2::uuid IS NULL` branch is what makes it mean "all projects". A zero uuid
// here would filter to a project that does not exist and return nothing.
func TestPgGoalReader_ProjectFilterPassthrough(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		pool := &queryPool{rows: &rowsStub{}}
		if _, err := (PgGoalReader{Pool: pool}).ListGoals(context.Background(), ListOptions{Limit: 10}); err != nil {
			t.Fatalf("ListGoals: %v", err)
		}
		if pool.gotArgs[1] != (*uuid.UUID)(nil) {
			t.Errorf("project arg = %#v, want a nil *uuid.UUID so the filter is skipped", pool.gotArgs[1])
		}
	})
	t.Run("present", func(t *testing.T) {
		id := uuid.New()
		pool := &queryPool{rows: &rowsStub{}}
		if _, err := (PgGoalReader{Pool: pool}).ListGoals(context.Background(), ListOptions{Limit: 10, ProjectID: &id}); err != nil {
			t.Fatalf("ListGoals: %v", err)
		}
		got, ok := pool.gotArgs[1].(*uuid.UUID)
		if !ok || got == nil || *got != id {
			t.Errorf("project arg = %#v, want %v", pool.gotArgs[1], id)
		}
	})
}

// An empty result must encode as [] rather than null, so a consumer does not
// have to handle a second empty case.
func TestPgGoalReader_EmptyEncodesAsArray(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	got, err := PgGoalReader{Pool: pool}.ListGoals(context.Background(), ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != "[]" {
		t.Errorf("empty goals encoded as %s, want []", b)
	}
}

func TestPgGoalReader_QueryErrorIsWrapped(t *testing.T) {
	pool := &queryPool{err: errors.New("boom")}
	_, err := PgGoalReader{Pool: pool}.ListGoals(context.Background(), ListOptions{Limit: 10})
	if err == nil {
		t.Fatal("ListGoals returned no error on a failing query")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q does not carry the cause", err)
	}
}

// The reader reads remote_url out of the database and must not let it reach the
// wire. This is the encoded-shape half of the ProjectName guarantee: that test
// proves the label is safe, this one proves the raw URL has no field to travel
// in.
func TestGoal_JSONCarriesNoRemoteURL(t *testing.T) {
	const remote = "https://svc:tok3n@git.internal.example.com/employer-org/widget.git"
	pool := &queryPool{rows: &rowsStub{rows: [][]any{
		{uuid.New(), int64(1), time.Now().UTC(), uuid.New(), remote, uuid.New(), "research", "tower", false},
	}}}
	got, err := PgGoalReader{Pool: pool}.ListGoals(context.Background(), ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	body := string(b)
	if !strings.Contains(body, `"project_name":"widget"`) {
		t.Fatalf("encoded goal %s lacks the derived label — the leak check below would pass vacuously", body)
	}
	for _, forbidden := range []string{remote, "git.internal.example.com", "employer-org", "tok3n", "remote_url"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("encoded goal leaks %q: %s", forbidden, body)
		}
	}
}

// fakeGoals records what the handler asked for and returns what it is told to.
type fakeGoals struct {
	goals   []Goal
	err     error
	gotOpts ListOptions
}

func (f *fakeGoals) ListGoals(_ context.Context, opts ListOptions) ([]Goal, error) {
	f.gotOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.goals, nil
}

// The default limit must reach the reader. Without this the parseLimit cases
// could all be right while the handler queried a constant.
func TestHandleGoals_PassesOptionsThrough(t *testing.T) {
	id := uuid.New()
	reader := &fakeGoals{goals: []Goal{}}
	rec := get(t, Config{Events: &fakeEvents{}, Goals: reader},
		"/api/goals?limit=7&project_id="+id.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if reader.gotOpts.Limit != 7 {
		t.Errorf("reader asked for limit %d, want 7", reader.gotOpts.Limit)
	}
	if reader.gotOpts.ProjectID == nil || *reader.gotOpts.ProjectID != id {
		t.Errorf("reader got project %v, want %v", reader.gotOpts.ProjectID, id)
	}
}

// Absent ?project_id= must mean all projects — a nil filter, not a zero uuid,
// which would match nothing and render an empty UI on a populated database.
func TestHandleGoals_NoProjectFilterMeansAllProjects(t *testing.T) {
	reader := &fakeGoals{goals: []Goal{}}
	if rec := get(t, Config{Events: &fakeEvents{}, Goals: reader}, "/api/goals"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if reader.gotOpts.ProjectID != nil {
		t.Errorf("project filter = %v, want nil so every project is listed", *reader.gotOpts.ProjectID)
	}
	if reader.gotOpts.Limit != DefaultEventLimit {
		t.Errorf("limit = %d, want the default %d", reader.gotOpts.Limit, DefaultEventLimit)
	}
}

// A malformed project_id is a 400. Ignoring it would answer a question about
// one project with every project's data.
func TestHandleGoals_RejectsAMalformedProjectID(t *testing.T) {
	reader := &fakeGoals{goals: []Goal{}}
	rec := get(t, Config{Events: &fakeEvents{}, Goals: reader}, "/api/goals?project_id=not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — an unparseable filter must not be silently dropped", rec.Code)
	}
	if reader.gotOpts.Limit != 0 {
		t.Error("the reader was called despite an invalid filter")
	}
	if !strings.Contains(rec.Body.String(), "project_id must be a uuid") {
		t.Errorf("body %s does not say which rule was broken", strings.TrimSpace(rec.Body.String()))
	}
}

// The 500 body must not carry the database's error text. This endpoint is
// browser-reachable with no auth in v1, and a pg error names tables, columns
// and roles.
func TestHandleGoals_DatabaseErrorIsNotReturnedToTheBrowser(t *testing.T) {
	var logged strings.Builder
	const secret = `relation "sprawl_internal_secrets" does not exist`
	reader := &fakeGoals{err: errors.New(secret)}
	rec := get(t, Config{Events: &fakeEvents{}, Goals: reader, Logger: textLogger(&logged)}, "/api/goals")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, secret) || strings.Contains(body, "sprawl_internal_secrets") {
		t.Errorf("the response body leaks the database error: %s", strings.TrimSpace(body))
	}
	if !strings.Contains(body, "reading goals failed") {
		t.Errorf("body %s does not name the collection that failed", strings.TrimSpace(body))
	}
	// The other half: suppressed from the response but NOT lost. An operator
	// still needs the real cause, so it has to be in the log.
	if !strings.Contains(logged.String(), "sprawl_internal_secrets") {
		t.Errorf("the real error was dropped entirely rather than logged: %s", logged.String())
	}
}

// An empty result encodes as [] under the named key, not as null and not as a
// bare array.
func TestHandleGoals_EmptyEncodesAsNamedArray(t *testing.T) {
	rec := get(t, Config{Events: &fakeEvents{}, Goals: &fakeGoals{goals: []Goal{}}}, "/api/goals")
	if body := rec.Body.String(); !strings.Contains(body, `"goals":[]`) {
		t.Errorf("empty goals encoded as %s, want {\"goals\":[]}", strings.TrimSpace(body))
	}
}
