package dispatchadapt

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentpkg "github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/store"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// The supervisor-backed seams (QUM-1252, AC5).
//
// The disk-backed seams above are a set of honest "I do not know" answers. These
// are the narrow set of answers a process WITH a supervisor can honestly give,
// and every test here exists to keep the boundary between the two exactly where
// it is: an absent runtime is an observation, an unreadable phase machine is
// not.

// fakeSup records what was asked of it and answers from a fixture.
type fakeSup struct {
	infos     []supervisor.AgentInfo
	statusErr error

	wokeName   string
	wokeReason agentpkg.WakeReason
	wokeBody   string
	wokeCalls  int
	wakeErr    error
}

func (f *fakeSup) Status(context.Context) ([]supervisor.AgentInfo, error) {
	return f.infos, f.statusErr
}

func (f *fakeSup) Wake(_ context.Context, name string, reason agentpkg.WakeReason, body string) (*supervisor.WakeResult, error) {
	f.wokeCalls++
	f.wokeName, f.wokeReason, f.wokeBody = name, reason, body
	if f.wakeErr != nil {
		return nil, f.wakeErr
	}
	return &supervisor.WakeResult{}, nil
}

func supervisedSnapshot(t *testing.T, root string, sup *fakeSup) []store.LocalAgent {
	t.Helper()
	v := &SupervisorAgents{Disk: &DiskAgents{SprawlRoot: root}, Sup: sup}
	got, err := v.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return got
}

func turnOf(t *testing.T, got []store.LocalAgent, name string) store.TurnState {
	t.Helper()
	for _, a := range got {
		if a.Name == name {
			return a.Turn
		}
	}
	t.Fatalf("%q is not in the snapshot (%d entries) — the disk view is what enumerates agents, and dropping one silently removes it from every sweep", name, len(got))
	return store.TurnUnknown
}

// THE ASSERTION AC5 TURNS ON. An agent with no live subprocess cannot be
// mid-turn — there is no process to be mid-anything — so TurnIdle here is a real
// observation and not the negative-answer-from-an-absent-observation that
// DiskAgents refuses to give. Without it the sweeper is inert against exactly
// the case AC5 describes: a researcher whose session died holding an open goal.
func TestSupervisorAgents_NoSubprocessIsObservedIdle(t *testing.T) {
	root := t.TempDir()
	seedAgent(t, root, &state.AgentState{Name: "ghost", Status: state.StatusDied, Branch: "b", Worktree: "/wt/ghost"})
	sup := &fakeSup{infos: []supervisor.AgentInfo{{Name: "ghost", SubprocessAlive: false}}}

	if got := turnOf(t, supervisedSnapshot(t, root, sup), "ghost"); got != store.TurnIdle {
		t.Errorf("turn state for an agent with no live subprocess = %v, want TurnIdle; a dead session cannot be mid-turn, and reporting TurnUnknown leaves the stall sweeper inert against the one case it exists for", got)
	}
}

func TestSupervisorAgents_InTurnIsReportedAsInTurn(t *testing.T) {
	root := t.TempDir()
	seedAgent(t, root, &state.AgentState{Name: "busy", Status: state.StatusActive, Branch: "b", Worktree: "/wt/busy"})
	sup := &fakeSup{infos: []supervisor.AgentInfo{{Name: "busy", SubprocessAlive: true, InTurn: true}}}

	if got := turnOf(t, supervisedSnapshot(t, root, sup), "busy"); got != store.TurnInTurn {
		t.Errorf("turn state for a live in-turn agent = %v, want TurnInTurn", got)
	}
}

// A LIVE AGENT REPORTING in_turn=false IS STILL UNKNOWN, and this is the test
// that keeps this type from quietly becoming QUM-1328.
//
// AgentInfo.InTurn is the ambiguous signal internal/supervisor/runtime.go's
// InTurnObserved was introduced to replace: its false means BOTH "between turns"
// and "nobody could tell". Status() does not carry the observed bit, so a false
// from a live agent is unusable and must stay unusable. Reading it as idle is
// precisely the "poke every working agent" defect, arriving through a field that
// looks authoritative.
func TestSupervisorAgents_LiveButNotInTurnStaysUnknown(t *testing.T) {
	root := t.TempDir()
	seedAgent(t, root, &state.AgentState{Name: "quiet", Status: state.StatusActive, Branch: "b", Worktree: "/wt/quiet"})
	sup := &fakeSup{infos: []supervisor.AgentInfo{{Name: "quiet", SubprocessAlive: true, InTurn: false}}}

	if got := turnOf(t, supervisedSnapshot(t, root, sup), "quiet"); got != store.TurnUnknown {
		t.Errorf("turn state for a LIVE agent reporting in_turn=false = %v, want TurnUnknown; AgentInfo.InTurn carries no observed bit, so its false means 'between turns' and 'nobody could tell' alike, and treating it as idle pokes working agents", got)
	}
}

// An agent the supervisor does not mention keeps the disk view's answer.
func TestSupervisorAgents_AgentTheSupervisorDoesNotKnowStaysUnknown(t *testing.T) {
	root := t.TempDir()
	seedAgent(t, root, &state.AgentState{Name: "stranger", Status: state.StatusActive, Branch: "b", Worktree: "/wt/stranger"})
	sup := &fakeSup{infos: nil}

	if got := turnOf(t, supervisedSnapshot(t, root, sup), "stranger"); got != store.TurnUnknown {
		t.Errorf("turn state for an agent absent from Status() = %v, want TurnUnknown", got)
	}
}

// THE DISK VIEW REMAINS THE ENUMERATOR AND THE STATUS AUTHORITY.
//
// The on-disk AgentState is the sole wake arbiter, and the overlay must not
// substitute the supervisor's view of Status for it: the supervisor's projection
// and the state file can disagree, and the gates that decide whether to WAKE
// something must read the arbiter.
func TestSupervisorAgents_DiskOwnsStatusAndEnumeration(t *testing.T) {
	root := t.TempDir()
	seedAgent(t, root, &state.AgentState{Name: "ghost", Status: state.StatusDied, Branch: "br", Worktree: "/wt/ghost", SessionID: "sess-1"})
	sup := &fakeSup{infos: []supervisor.AgentInfo{
		{Name: "ghost", Status: state.StatusActive, Branch: "wrong", SubprocessAlive: false},
		{Name: "not-on-disk", SubprocessAlive: true},
	}}

	got := supervisedSnapshot(t, root, sup)
	if len(got) != 1 {
		t.Fatalf("%d entries, want 1 — the disk view enumerates, so a supervisor-only name must not appear", len(got))
	}
	if got[0].Status != state.StatusDied {
		t.Errorf("Status = %q, want %q from the state file; the on-disk AgentState is the sole wake arbiter and the supervisor's projection must not override it", got[0].Status, state.StatusDied)
	}
	if got[0].Branch != "br" || got[0].SessionID != "sess-1" {
		t.Errorf("disk fields lost in the overlay: branch %q, session %q", got[0].Branch, got[0].SessionID)
	}
}

// A Status() FAILURE FAILS THE SNAPSHOT rather than degrading to the disk view.
//
// Degrading looks harmless — the disk view is the old behaviour — but it is not
// the old behaviour in effect: it silently returns the sweeper to inert, on a
// timer, with nothing in the log saying the observation stopped working. Sweep
// already stops its pass when it cannot read local state, so an error here lands
// in a caller that handles it.
func TestSupervisorAgents_StatusFailureFailsTheSnapshot(t *testing.T) {
	root := t.TempDir()
	seedAgent(t, root, &state.AgentState{Name: "alice", Status: state.StatusActive, Branch: "b", Worktree: "/wt/a"})
	v := &SupervisorAgents{
		Disk: &DiskAgents{SprawlRoot: root},
		Sup:  &fakeSup{statusErr: errors.New("boom")},
	}
	if _, err := v.Snapshot(context.Background()); err == nil {
		t.Fatal("Snapshot returned nil error when the supervisor could not be read; degrading to the disk view would silently return the sweeper to inert")
	}
}

func TestSupervisorAgents_NilSupIsRefused(t *testing.T) {
	v := &SupervisorAgents{Disk: &DiskAgents{SprawlRoot: t.TempDir()}}
	if _, err := v.Snapshot(context.Background()); err == nil {
		t.Fatal("Snapshot with no supervisor returned nil error; a silently disk-only view is the inert sweeper wearing the name of the effective one")
	}
}

// ---------------------------------------------------------------------------
// WakeInjector
// ---------------------------------------------------------------------------

func newWakeInjector(t *testing.T, root string, sup *fakeSup) (*WakeInjector, *[]string) {
	t.Helper()
	var enqueued []string
	q := &QueueInjector{
		SprawlRoot: root,
		Send:       func(_, _, to, _, _ string) (string, error) { return "id-" + to, nil },
		Enqueue: func(_, agentName string, e agentloop.Entry) (agentloop.Entry, error) {
			enqueued = append(enqueued, agentName+":"+e.Body)
			return e, nil
		},
	}
	return &WakeInjector{Queue: q, Sup: sup, Disk: &DiskAgents{SprawlRoot: root}}, &enqueued
}

// A LIVE AGENT IS ENQUEUED, NOT WOKEN. Waking one that is already running is at
// best a no-op and at worst a restart of a working session.
func TestWakeInjector_LiveAgentIsEnqueued(t *testing.T) {
	root := t.TempDir()
	seedAgent(t, root, &state.AgentState{Name: "alice", Status: state.StatusActive, Branch: "b", Worktree: "/wt/a"})
	inj, enqueued := newWakeInjector(t, root, &fakeSup{})

	if err := inj.Inject(context.Background(), "alice", "poke"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if len(*enqueued) != 1 {
		t.Errorf("enqueued %v, want one entry for a live agent", *enqueued)
	}
}

// THE AC5 PATH: a crashed agent is WOKEN, with the poke as its first prompt.
func TestWakeInjector_CrashedAgentIsWoken(t *testing.T) {
	for _, status := range []string{state.StatusDied, state.StatusFaulted, state.StatusResumeFailed, state.StatusComplete} {
		root := t.TempDir()
		seedAgent(t, root, &state.AgentState{Name: "ghost", Status: status, Branch: "b", Worktree: "/wt/g"})
		sup := &fakeSup{}
		inj, enqueued := newWakeInjector(t, root, sup)

		if err := inj.Inject(context.Background(), "ghost", "your goal is still open"); err != nil {
			t.Fatalf("status %q: Inject: %v", status, err)
		}
		if sup.wokeCalls != 1 || sup.wokeName != "ghost" {
			t.Errorf("status %q: woke %d time(s) for %q, want once for \"ghost\" — without a wake the poke sits in pending/ until someone starts a session by hand, so the goal never completes", status, sup.wokeCalls, sup.wokeName)
		}
		if !strings.Contains(sup.wokeBody, "your goal is still open") {
			t.Errorf("status %q: wake body %q does not carry the poke; the agent would come back online with no idea why", status, sup.wokeBody)
		}
		if len(*enqueued) != 0 {
			t.Errorf("status %q: also enqueued %v; the wake already delivers the body, so this is the same poke twice", status, *enqueued)
		}
	}
}

// AN OPERATOR-KILLED AGENT IS NEVER WOKEN, and this is a decision rather than a
// limitation. `sprawl kill` is a human decision exactly as `sprawl pause` is,
// and the sweeper's own reasoning for excluding paused agents from auto-resume
// applies unchanged: overriding it is not the sweeper's call. So the poke is
// enqueued durably — it is there when a human brings the agent back — and
// nothing is revived on a timer.
func TestWakeInjector_KilledAgentIsNotWoken(t *testing.T) {
	root := t.TempDir()
	seedAgent(t, root, &state.AgentState{Name: "shot", Status: state.StatusKilled, Branch: "b", Worktree: "/wt/s"})
	sup := &fakeSup{}
	inj, enqueued := newWakeInjector(t, root, sup)

	if err := inj.Inject(context.Background(), "shot", "poke"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if sup.wokeCalls != 0 {
		t.Errorf("woke an operator-killed agent %d time(s); a kill is a human decision and reviving it on a sweep timer overrides an operator on a machine they may not be watching", sup.wokeCalls)
	}
	if len(*enqueued) != 1 {
		t.Errorf("enqueued %v, want the poke queued durably so it is delivered if a human wakes the agent later", *enqueued)
	}
}

func TestWakeInjector_PausedAgentIsNotWoken(t *testing.T) {
	root := t.TempDir()
	seedAgent(t, root, &state.AgentState{Name: "held", Status: state.StatusPaused, Branch: "b", Worktree: "/wt/h"})
	sup := &fakeSup{}
	inj, _ := newWakeInjector(t, root, sup)

	if err := inj.Inject(context.Background(), "held", "poke"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if sup.wokeCalls != 0 {
		t.Errorf("woke a paused agent %d time(s); StatusPaused is excluded from auto-resume on purpose", sup.wokeCalls)
	}
}

// A WAKE FAILURE IS AN ERROR, not a silent fall-back to the queue. Sweep treats
// a failed injection as a failed pass while KEEPING the goal_poke event, so the
// backoff still advances — a persistently unwakable owner backs off and is
// eventually quarantined instead of being retried at the base interval forever.
func TestWakeInjector_WakeFailureIsReported(t *testing.T) {
	root := t.TempDir()
	seedAgent(t, root, &state.AgentState{Name: "ghost", Status: state.StatusDied, Branch: "b", Worktree: "/wt/g"})
	sup := &fakeSup{wakeErr: errors.New("no session to resume")}
	inj, _ := newWakeInjector(t, root, sup)

	err := inj.Inject(context.Background(), "ghost", "poke")
	if err == nil {
		t.Fatal("Inject returned nil after the wake failed; the caller would record a delivered poke that never arrived")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the recipient, got: %v", err)
	}
}

// ErrWakeNotNeeded IS NOT A FAILURE, AND IT IS NOT A DELIVERY EITHER — it is
// the one wake outcome that needs the queue.
//
// AgentRuntime.Wake returns it from a short-circuit that fires BEFORE the
// injection is forwarded, so the body is genuinely undelivered: the agent is
// alive and healthy, it just did not get the poke. Treating that as an error
// loses the body twice over — no wake, and no enqueue either, because the
// fall-through to the queue is skipped — while Sweep has already emitted
// goal_poke and consumed the epoch. The goal then marches to its quarantine cap
// on pokes that reached nobody.
//
// The state is reachable rather than theoretical: the disk status is read from a
// file and the runtime handle is in memory, so any stale-status window (a `died`
// or `resume_failed` sticker that a recovery has already superseded, or a
// `complete` agent parked while its handle is still live per QUM-818) lands
// exactly here. Every other caller in the tree already special-cases it —
// internal/sprawlmcp/server.go treats it as success, internal/supervisor/idlereap.go
// logs it as a WARN — and this one is the only place where getting it wrong
// silently drops a message.
func TestWakeInjector_WakeNotNeededFallsBackToTheQueue(t *testing.T) {
	root := t.TempDir()
	seedAgent(t, root, &state.AgentState{Name: "ghost", Status: state.StatusDied, Branch: "b", Worktree: "/wt/g"})
	sup := &fakeSup{wakeErr: supervisor.ErrWakeNotNeeded}
	inj, enqueued := newWakeInjector(t, root, sup)

	if err := inj.Inject(context.Background(), "ghost", "your goal is still open"); err != nil {
		t.Fatalf("Inject: %v — ErrWakeNotNeeded means the session is healthy, which is not a delivery failure", err)
	}
	if len(*enqueued) != 1 {
		t.Fatalf("enqueued %v, want one durable entry: the wake short-circuited BEFORE forwarding the injection, so this poke has not been delivered by any path", *enqueued)
	}
	if !strings.Contains((*enqueued)[0], "your goal is still open") {
		t.Errorf("queued entry %q does not carry the poke body", (*enqueued)[0])
	}
}

// An agent that is not on disk at all cannot be classified, so it falls back to
// the durable queue rather than being woken on a guess.
func TestWakeInjector_UnknownAgentFallsBackToTheQueue(t *testing.T) {
	root := t.TempDir()
	sup := &fakeSup{}
	inj, enqueued := newWakeInjector(t, root, sup)

	if err := inj.Inject(context.Background(), "nobody", "poke"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if sup.wokeCalls != 0 {
		t.Errorf("woke an agent this host has no state file for (%d call(s))", sup.wokeCalls)
	}
	if len(*enqueued) != 1 {
		t.Errorf("enqueued %v, want one durable entry", *enqueued)
	}
}
