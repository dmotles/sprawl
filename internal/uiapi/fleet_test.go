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

// fakeFleet is a FleetReader that records what it was asked for.
type fakeFleet struct {
	members []FleetMember
	err     error
	gotOpts ListOptions
}

func (f *fakeFleet) ListFleet(_ context.Context, opts ListOptions) ([]FleetMember, error) {
	f.gotOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.members, nil
}

func TestPgFleetReader_ListFleet(t *testing.T) {
	projID := uuid.New()
	last := time.Date(2026, 9, 9, 17, 45, 0, 0, time.UTC)
	pool := &queryPool{rows: &rowsStub{rows: [][]any{
		{"ratz", projID, "git@github.com:dmotles/sprawl.git", 12, int64(48000), int64(9100), int64(20), int64(310), last},
	}}}

	got, err := PgFleetReader{Pool: pool}.ListFleet(context.Background(), ListOptions{Limit: 50})
	if err != nil {
		t.Fatalf("ListFleet: %v", err)
	}
	want := FleetMember{
		AgentName: "ratz", ProjectID: projID, ProjectName: "sprawl",
		TurnCount: 12, InputTokens: 48000, OutputTokens: 9100,
		FirstSeq: 20, LastSeq: 310, LastTurnAt: last,
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("member = %+v, want %+v", got, want)
	}
}

// The view is advisory because it is reconstructed from turn_finished, and
// these three clauses are what make that claim true rather than decorative.
func TestPgFleetReader_ReadsTurnsAndAttributesThem(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	if _, err := (PgFleetReader{Pool: pool}).ListFleet(context.Background(), ListOptions{Limit: 10}); err != nil {
		t.Fatalf("ListFleet: %v", err)
	}
	// agent_sessions is a projection nothing in the tree writes: a query
	// against it returns nothing on a live system, which looks like an empty
	// fleet rather than like a missing table.
	if strings.Contains(pool.gotSQL, "agent_sessions") {
		t.Errorf("the fleet query reads agent_sessions, which nothing writes:\n%s", pool.gotSQL)
	}
	if !strings.Contains(pool.gotSQL, "'turn_finished'") {
		t.Errorf("the fleet query is not driven by turn_finished:\n%s", pool.gotSQL)
	}
	// Matched by NAME through event_type_schemas, so a future v2 of the type
	// keeps appearing instead of silently emptying the view.
	if !strings.Contains(pool.gotSQL, "event_type_schemas") {
		t.Errorf("the fleet query does not resolve the type by name:\n%s", pool.gotSQL)
	}
	// A turn with no agent_name cannot be attributed to an agent. Bucketing
	// those under "" invents a fleet member named after the failure to name one.
	if !strings.Contains(pool.gotSQL, `COALESCE(e.payload->>'agent_name', '') <> ''`) {
		t.Errorf("unattributable turns are not excluded, so an agent named \"\" would appear:\n%s", pool.gotSQL)
	}
}

// A bare ::bigint cast raises on the first payload carrying a non-number, and
// takes the whole view down rather than the one bad row with it.
func TestPgFleetReader_SumsTokensWithoutAnUnguardedCast(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	if _, err := (PgFleetReader{Pool: pool}).ListFleet(context.Background(), ListOptions{Limit: 10}); err != nil {
		t.Fatalf("ListFleet: %v", err)
	}
	for _, col := range []string{"input_tokens", "output_tokens"} {
		if !strings.Contains(pool.gotSQL, `jsonb_typeof(e.payload->'`+col+`') = 'number'`) {
			t.Errorf("%s is summed without a jsonb_typeof guard, so one malformed payload errors the whole query:\n%s", col, pool.gotSQL)
		}
	}
}

func TestPgFleetReader_PassesTheProjectFilterThrough(t *testing.T) {
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
			if _, err := (PgFleetReader{Pool: pool}).ListFleet(context.Background(), tc.opts); err != nil {
				t.Fatalf("ListFleet: %v", err)
			}
			if len(pool.gotArgs) != 2 {
				t.Fatalf("query got %d args, want 2", len(pool.gotArgs))
			}
			if got, ok := pool.gotArgs[0].(*uuid.UUID); !ok || got != tc.want {
				t.Errorf("project arg = %#v, want %#v", pool.gotArgs[0], tc.want)
			}
		})
	}
}

// There is no `alive` field, on purpose: the log cannot distinguish a retired
// agent from a wedged one, and a boolean would assert a distinction the data
// does not hold. Pinned as an assertion because it is the sort of field a later
// change adds "for convenience", and the honesty constraint would go with it.
func TestFleetMember_MakesNoLivenessClaim(t *testing.T) {
	blob, err := json.Marshal(FleetMember{AgentName: "ratz"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"alive", "online", "running", "status"} {
		if strings.Contains(string(blob), `"`+forbidden+`"`) {
			t.Errorf("the fleet JSON claims %q, which turn_finished cannot support: %s", forbidden, blob)
		}
	}
	if !strings.Contains(string(blob), `"last_turn_at"`) {
		t.Errorf("the fleet JSON has no last_turn_at, so a reader has nothing to judge activity by: %s", blob)
	}
}

func TestFleetMember_JSONCarriesNoRemoteURL(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{rows: [][]any{
		{
			"ratz", uuid.New(), "https://bot:tok3n@git.internal.example.com/some-org/widget.git",
			1, int64(1), int64(1), int64(1), int64(1), time.Now(),
		},
	}}}
	got, err := PgFleetReader{Pool: pool}.ListFleet(context.Background(), ListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("ListFleet: %v", err)
	}
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{"git.internal.example.com", "some-org", "tok3n", "https"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("the fleet JSON carries %q from remote_url: %s", secret, blob)
		}
	}
}

func TestHandleFleet_ServesTheNamedEnvelope(t *testing.T) {
	reader := &fakeFleet{members: []FleetMember{}}
	cfg := fullConfig()
	cfg.Fleet = reader
	rec := get(t, cfg, "/api/fleet?limit=5")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"fleet":[]`) {
		t.Errorf("an empty fleet encoded as %s, want {\"fleet\":[]}", strings.TrimSpace(body))
	}
	if reader.gotOpts.Limit != 5 {
		t.Errorf("reader asked for limit %d, want 5", reader.gotOpts.Limit)
	}
}

func TestHandleFleet_DoesNotLeakTheDatabaseError(t *testing.T) {
	var logged strings.Builder
	cfg := fullConfig()
	cfg.Fleet = &fakeFleet{err: errPgDetail}
	cfg.Logger = textLogger(&logged)
	rec := get(t, cfg, "/api/fleet")
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
