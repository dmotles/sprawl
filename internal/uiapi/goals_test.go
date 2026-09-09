package uiapi

import (
	"context"
	"encoding/json"
	"errors"
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
