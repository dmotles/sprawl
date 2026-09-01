package sprawlmcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/dmotles/sprawl/internal/store"
)

func reportArgs(goal, outcome, summary string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"goal_event_id":%q,"outcome":%q,"summary":%q}`, goal, outcome, summary))
}

// TestReportResult_ClosesTheCallersOwnGoal. The load-bearing assertion is
// closedAgent: the owner reaching the store is the SESSION's identity, never an
// argument. A tool that let the caller name the owner would let any agent close
// any goal, permanently.
func TestReportResult_ClosesTheCallersOwnGoal(t *testing.T) {
	goal, closeID := uuid.New(), uuid.New()
	src := &fakeGoalSource{enabled: true, closeID: closeID}

	out, err := goalServer(src).toolReportResult(callerCtx("finn"), reportArgs(goal.String(), "success", "shipped it"))
	if err != nil {
		t.Fatalf("toolReportResult: %v", err)
	}
	if src.closedAgent != "finn" {
		t.Errorf("the store was told the closer was %q, want finn — the owner comes from the session", src.closedAgent)
	}
	if src.closedGoal != goal {
		t.Errorf("closed %s, want %s", src.closedGoal, goal)
	}
	if src.closedOutcome != store.GoalSucceeded || src.closedSummary != "shipped it" {
		t.Errorf("outcome/summary reached the store as (%q, %q)", src.closedOutcome, src.closedSummary)
	}

	var got reportResultOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.CloseEvent != closeID.String() {
		t.Errorf("close_event_id came back %q, want the id the store minted (%s)", got.CloseEvent, closeID)
	}
	if !got.Irreversible || !strings.Contains(got.Note, "rework") {
		t.Errorf("the result must tell the agent the close is final and what to do instead; got irreversible=%v note=%q", got.Irreversible, got.Note)
	}
}

// TestReportResult_RefusalsNeverReachTheStore.
//
// Every case here is a WRITE that must not happen. The `closeCalls` assertion is
// the whole point: a tool that validated after calling the store would still
// return an error and still have closed the goal, and no test that only checked
// the error would notice.
func TestReportResult_RefusalsNeverReachTheStore(t *testing.T) {
	goal := uuid.New().String()
	for _, tc := range []struct {
		name    string
		caller  string
		args    json.RawMessage
		wantErr string
	}{
		{"unknown caller", "", reportArgs(goal, "success", "done"), "which agent"},
		{"goal id is not a uuid", "finn", reportArgs("nope", "success", "done"), "not a uuid"},
		{"empty summary", "finn", reportArgs(goal, "success", ""), "summary"},
		{"malformed arguments", "finn", json.RawMessage(`{`), "invalid arguments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeGoalSource{enabled: true, closeID: uuid.New()}
			ctx := callerCtx(tc.caller)
			if tc.caller == "" {
				ctx = callerCtx("")
			}
			_, err := goalServer(src).toolReportResult(ctx, tc.args)
			if err == nil {
				t.Fatal("report_result accepted the call")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should mention %q; got: %v", tc.wantErr, err)
			}
			if src.closeCalls != 0 {
				t.Errorf("the store was asked to close a goal %d time(s) despite the refusal — the write is permanent", src.closeCalls)
			}
		})
	}

	// Paired control: a well-formed call DOES reach the store exactly once.
	// Without it, every case above is satisfied by a tool that refuses
	// everything.
	src := &fakeGoalSource{enabled: true, closeID: uuid.New()}
	if _, err := goalServer(src).toolReportResult(callerCtx("finn"), reportArgs(goal, "success", "done")); err != nil {
		t.Fatalf("a well-formed report_result was refused: %v", err)
	}
	if src.closeCalls != 1 {
		t.Errorf("a valid call reached the store %d times, want exactly 1", src.closeCalls)
	}
}

// TestReportResult_SurfacesTheStoresRefusal — the ownership check lives in the
// store, so the tool's job is to not swallow it.
func TestReportResult_SurfacesTheStoresRefusal(t *testing.T) {
	src := &fakeGoalSource{enabled: true, closeErr: errors.New("store: ... belongs to another agent")}
	_, err := goalServer(src).toolReportResult(callerCtx("finn"), reportArgs(uuid.New().String(), "success", "done"))
	if err == nil {
		t.Fatal("report_result reported success over a store refusal")
	}
	if !strings.Contains(err.Error(), "another agent") {
		t.Errorf("the store's reason was lost; got: %v", err)
	}
}

func TestReportResult_RefusedWhenTheEventLogIsOff(t *testing.T) {
	src := &fakeGoalSource{enabled: false}
	_, err := New(nil).WithGoals(src).toolReportResult(callerCtx("finn"), reportArgs(uuid.New().String(), "success", "done"))
	if err == nil {
		t.Fatal("report_result closed a goal with the event log off")
	}
	if src.closeCalls != 0 {
		t.Error("the store was written to with the event log off")
	}
}

// TestReportResult_OutcomeEnumMatchesTheStore. The tool advertises an enum to
// claude and the store enforces one; if they drift, the model is told to send a
// value that is then rejected at the write — after it believes it has finished.
func TestReportResult_OutcomeEnumMatchesTheStore(t *testing.T) {
	var advertised []string
	for _, def := range baseToolDefinitions() {
		if def["name"] != "report_result" {
			continue
		}
		schema := def["inputSchema"].(map[string]any)["properties"].(map[string]any)
		// Comma-ok, not a bare assertion: the failure this test exists to
		// catch is the enum being ABSENT, and a bare assertion panics on
		// exactly that case — making the message below unreachable.
		advertised, _ = schema["outcome"].(map[string]any)["enum"].([]string)
	}
	if len(advertised) == 0 {
		t.Fatal("report_result advertises no outcome enum, so any string reaches the store")
	}
	if len(advertised) != len(store.ValidGoalOutcomes) {
		t.Fatalf("the tool advertises %v but the store accepts %v", advertised, store.ValidGoalOutcomes)
	}
	for i, a := range advertised {
		if store.GoalOutcome(a) != store.ValidGoalOutcomes[i] {
			t.Errorf("outcome %d: tool says %q, store says %q", i, a, store.ValidGoalOutcomes[i])
		}
	}
}
