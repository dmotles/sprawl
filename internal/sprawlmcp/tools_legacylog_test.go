package sprawlmcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/store"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// legacySup is a supervisor whose Spawn and Retire succeed, so these tests are
// about the RECORDING and nothing else.
type legacySup struct {
	supervisor.Supervisor
	info    *supervisor.AgentInfo
	retired []string
	err     error
}

func (s *legacySup) Spawn(context.Context, supervisor.SpawnRequest) (*supervisor.AgentInfo, error) {
	return s.info, s.err
}

func (s *legacySup) Retire(context.Context, string, string, bool, bool, bool, bool) ([]string, error) {
	return s.retired, s.err
}

func spawnArgs() json.RawMessage {
	return json.RawMessage(`{"type":"engineer","family":"engineering","prompt":"do it","branch":"dmotles/thing"}`)
}

// TestSpawn_RecordsTheAgentAsOutstandingWork.
//
// The assertion that matters is that the recorded identity comes from the
// supervisor's ANSWER, not from the request: the name is allocated during the
// spawn, so a version reading `req` would record an empty name and produce a
// contract no retire could ever close.
func TestSpawn_RecordsTheAgentAsOutstandingWork(t *testing.T) {
	src := &fakeGoalSource{enabled: true}
	sup := &legacySup{info: &supervisor.AgentInfo{
		Name: "ratz", Type: "engineer", Family: "engineering",
		Parent: "weave", Branch: "dmotles/thing",
	}}

	if _, err := New(sup).WithGoals(src).toolSpawn(callerCtx("weave"), spawnArgs()); err != nil {
		t.Fatalf("toolSpawn: %v", err)
	}
	if len(src.legacySpawns) != 1 {
		t.Fatalf("the store was asked to record %d spawn(s), want 1 — `sprawl goals` will not list this agent", len(src.legacySpawns))
	}
	got := src.legacySpawns[0]
	if got.AgentName != "ratz" {
		t.Errorf("recorded agent_name %q, want ratz — the allocated name, not the requested one", got.AgentName)
	}
	if got.AgentType != "engineer" || got.Family != "engineering" || got.Parent != "weave" || got.Branch != "dmotles/thing" {
		t.Errorf("recorded %+v; type/family/parent/branch must survive the hop or the listing is unreadable", got)
	}
}

// TestSpawn_RecordsNothingWhenTheEventLogIsOff. The disabled store is the
// default configuration, so this is the common path, and it must be silent
// rather than merely non-fatal.
func TestSpawn_RecordsNothingWhenTheEventLogIsOff(t *testing.T) {
	src := &fakeGoalSource{enabled: false}
	sup := &legacySup{info: &supervisor.AgentInfo{Name: "ratz", Type: "engineer"}}

	if _, err := New(sup).WithGoals(src).toolSpawn(callerCtx("weave"), spawnArgs()); err != nil {
		t.Fatalf("toolSpawn over a disabled store: %v", err)
	}
	if len(src.legacySpawns) != 0 {
		t.Errorf("a disabled store was written to %d time(s)", len(src.legacySpawns))
	}
}

// TestSpawn_SucceedsWhenTheRecordingFails.
//
// The agent exists and is running by the time the recording is attempted, so a
// failure here must not be reported as a failed spawn — the caller's remedy for
// "spawn failed" is to spawn again, which would leave two agents.
func TestSpawn_SucceedsWhenTheRecordingFails(t *testing.T) {
	src := &fakeGoalSource{enabled: true, legacySpawnErr: errors.New("dial tcp: connection refused")}
	sup := &legacySup{info: &supervisor.AgentInfo{Name: "ratz", Type: "engineer"}}

	out, err := New(sup).WithGoals(src).toolSpawn(callerCtx("weave"), spawnArgs())
	if err != nil {
		t.Fatalf("a failed recording failed the spawn: %v", err)
	}
	if !strings.Contains(out, "ratz") {
		t.Errorf("the spawn result no longer names the agent: %q", out)
	}
	if len(src.legacySpawns) != 1 {
		t.Errorf("the recording was not even attempted (%d call(s))", len(src.legacySpawns))
	}
}

// TestRetire_ClosesEveryAgentTheCascadeActuallyRemoved.
//
// The descendants are the control. Closing only the requested target leaves each
// descendant outstanding forever — a contract nobody can close, because the
// agent it names no longer exists — and a fixture with one agent in it cannot
// tell that implementation from this one.
func TestRetire_ClosesEveryAgentTheCascadeActuallyRemoved(t *testing.T) {
	src := &fakeGoalSource{enabled: true}
	sup := &legacySup{retired: []string{"kid", "grandkid", "ratz"}}

	args := json.RawMessage(`{"agent":"ratz","cascade":true,"merge":true}`)
	if _, err := New(sup).WithGoals(src).toolRetire(callerCtx("weave"), args); err != nil {
		t.Fatalf("toolRetire: %v", err)
	}
	if len(src.legacyRetires) != 3 {
		t.Fatalf("closed %d contract(s), want 3 — the list the supervisor returned, not just the target: %+v", len(src.legacyRetires), src.legacyRetires)
	}
	seen := map[string]bool{}
	for _, c := range src.legacyRetires {
		seen[c.agent] = true
		if c.outcome == "" {
			t.Errorf("%q was closed with a blank outcome", c.agent)
		}
		if !c.merged {
			t.Errorf("%q was closed with merged=false despite merge:true", c.agent)
		}
	}
	for _, want := range []string{"kid", "grandkid", "ratz"} {
		if !seen[want] {
			t.Errorf("%q was retired but its contract was left open", want)
		}
	}
}

// TestRetire_SucceedsWhenTheClosingFails. Retire's counterpart to
// TestSpawn_SucceedsWhenTheRecordingFails: the agent is already gone, so
// reporting a failure would invite a retry against something that no longer
// exists.
func TestRetire_SucceedsWhenTheClosingFails(t *testing.T) {
	src := &fakeGoalSource{enabled: true, legacyRetireErr: errors.New("dial tcp: connection refused")}
	sup := &legacySup{retired: []string{"ratz"}}

	out, err := New(sup).WithGoals(src).toolRetire(callerCtx("weave"), json.RawMessage(`{"agent":"ratz"}`))
	if err != nil {
		t.Fatalf("a failed close failed the retire: %v", err)
	}
	if !strings.Contains(out, "ratz") {
		t.Errorf("the retire result no longer names the agent: %q", out)
	}
}

// TestLegacyLog_NoGoalSourceIsNotACrash. WithGoals is never called on a host
// with no event log configured, so s.goals is a genuine nil interface here —
// distinct from the typed-nil case Enabled() covers.
func TestLegacyLog_NoGoalSourceIsNotACrash(t *testing.T) {
	sup := &legacySup{info: &supervisor.AgentInfo{Name: "ratz", Type: "engineer"}, retired: []string{"ratz"}}
	s := New(sup)

	if _, err := s.toolSpawn(callerCtx("weave"), spawnArgs()); err != nil {
		t.Fatalf("toolSpawn with no goal source: %v", err)
	}
	if _, err := s.toolRetire(callerCtx("weave"), json.RawMessage(`{"agent":"ratz"}`)); err != nil {
		t.Fatalf("toolRetire with no goal source: %v", err)
	}
}

// TestGoalSource_IsSatisfiedByTheLedger pins the wiring this slice depends on:
// the two new methods are on the interface the server already holds, so there is
// no second surface to forget to attach.
func TestGoalSource_IsSatisfiedByTheLedger(t *testing.T) {
	var _ GoalSource = (*store.Ledger)(nil)
}
