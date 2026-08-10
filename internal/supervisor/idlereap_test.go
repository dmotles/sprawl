// QUM-1186 lane 3: the idle-reaper predicate.
//
// The predicate is six observations, and the rule that shapes every test here
// is D1a: an UNAVAILABLE observation is not a negative one. Reaping is
// destructive — it tears down a subprocess — so any term that cannot be
// resolved must block the reap, not permit it. A bool that is false because
// nobody could measure it is indistinguishable from one that is false because
// the agent is genuinely idle, which is why idleObs is a tri-state and why
// there is one named test per unavailable arm rather than a single table.
package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/protocol"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
)

// fakeIdleProbe is a fully controllable idleRuntimeProbe. Every field is
// explicit so a test never relies on a zero value to mean "observed false".
type fakeIdleProbe struct {
	inTurn         bool
	inTurnObserved bool
	lastAct        time.Time
	inFlight       int
	inFlightSeen   bool
}

func (f *fakeIdleProbe) InTurnObserved() (bool, bool)        { return f.inTurn, f.inTurnObserved }
func (f *fakeIdleProbe) LastActivityAt() time.Time           { return f.lastAct }
func (f *fakeIdleProbe) InFlightSystemObserved() (int, bool) { return f.inFlight, f.inFlightSeen }

// fakeQuestions is a questionPendingProbe that answers from a fixed set.
type fakeQuestions struct{ pending map[string]bool }

func (f *fakeQuestions) hasPendingFrom(name string) bool { return f.pending[name] }

const idleTestThreshold = 15 * time.Minute

// idleTestInputs returns inputs for an agent that IS reapable on every term,
// so each test below can spoil exactly one of them. Its own assertion is
// TestAssessIdle_AllPreconditionsMet_Reaps — the positive control that proves
// the other tests are spoiling something that otherwise passes.
func idleTestInputs(t *testing.T) idleInputs {
	t.Helper()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	return idleInputs{
		Name:       "alice",
		RootName:   "weave",
		SprawlRoot: t.TempDir(),
		Probe: &fakeIdleProbe{
			inTurn:         false,
			inTurnObserved: true,
			lastAct:        now.Add(-30 * time.Minute),
			inFlight:       0,
			inFlightSeen:   true,
		},
		Questions: &fakeQuestions{pending: map[string]bool{}},
		Now:       now,
		Threshold: idleTestThreshold,
	}
}

func TestAssessIdle_AllPreconditionsMet_Reaps(t *testing.T) {
	got := assessIdle(idleTestInputs(t))
	if !got.Reap {
		t.Fatalf("assessIdle().Reap = false for a fully idle agent, want true (blocker=%q, %+v)", got.Blocker, got)
	}
	if got.Blocker != "" {
		t.Errorf("Blocker = %q on a reap, want empty", got.Blocker)
	}
}

func TestAssessIdle_InTurn_DoesNotReap(t *testing.T) {
	in := idleTestInputs(t)
	in.Probe.(*fakeIdleProbe).inTurn = true
	got := assessIdle(in)
	if got.Reap {
		t.Fatal("assessIdle().Reap = true for an in-turn agent, want false")
	}
	if got.InTurn != obsBusy {
		t.Errorf("InTurn = %v, want obsBusy", got.InTurn)
	}
	if got.Blocker != "in_turn" {
		t.Errorf("Blocker = %q, want %q", got.Blocker, "in_turn")
	}
}

// TestAssessIdle_InTurnUnobservable_DoesNotReap is the D1a test, and the whole
// reason idleObs exists. AgentRuntime.InTurn() returns false both for "not in a
// turn" and for a handle it cannot probe (runtime.go: nil handle, or a handle
// that is not a turnProbe). A predicate built on InTurn() would reap an agent
// whose turn state it never managed to observe.
//
// Mutation that proves this fires: swap InTurnObserved() for InTurn() in
// assessIdle's InTurn arm — this test goes red while every other test here
// stays green.
func TestAssessIdle_InTurnUnobservable_DoesNotReap(t *testing.T) {
	in := idleTestInputs(t)
	p := in.Probe.(*fakeIdleProbe)
	p.inTurn = false // the trap: "false" from a probe that could not run
	p.inTurnObserved = false
	got := assessIdle(in)
	if got.Reap {
		t.Fatal("assessIdle().Reap = true when InTurn could not be observed; an unavailable observation is not a negative one")
	}
	if got.InTurn != obsUnavailable {
		t.Errorf("InTurn = %v, want obsUnavailable (NOT obsBusy — the distinction is what makes the log actionable)", got.InTurn)
	}
	if got.Blocker != "in_turn_unobservable" {
		t.Errorf("Blocker = %q, want %q", got.Blocker, "in_turn_unobservable")
	}
}

func TestAssessIdle_PendingQueueNonEmpty_DoesNotReap(t *testing.T) {
	in := idleTestInputs(t)
	if _, err := agentloop.Enqueue(in.SprawlRoot, in.Name, agentloop.Entry{
		ShortID: "m1", Class: agentloop.ClassAsync, From: "weave", Body: "hi",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	got := assessIdle(in)
	if got.Reap {
		t.Fatal("assessIdle().Reap = true with a message durably queued; reaping here drops the poke and strands the entry")
	}
	if got.Pending != obsBusy {
		t.Errorf("Pending = %v, want obsBusy", got.Pending)
	}
}

// TestAssessIdle_ListPendingError_DoesNotReap: an unreadable queue is not an
// empty queue. listDir returns (nil, nil) for a MISSING dir — that is a real
// "no mail" answer — but a genuine I/O error must block.
func TestAssessIdle_ListPendingError_DoesNotReap(t *testing.T) {
	in := idleTestInputs(t)
	// Make the pending dir path a regular file so os.ReadDir fails ENOTDIR.
	pending := agentloop.PendingDir(in.SprawlRoot, in.Name)
	if err := os.MkdirAll(filepath.Dir(pending), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(pending, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := assessIdle(in)
	if got.Reap {
		t.Fatal("assessIdle().Reap = true when ListPending errored; unknown is not empty")
	}
	if got.Pending != obsUnavailable {
		t.Errorf("Pending = %v, want obsUnavailable", got.Pending)
	}
}

func TestAssessIdle_InFlightSystemEntries_DoesNotReap(t *testing.T) {
	in := idleTestInputs(t)
	in.Probe.(*fakeIdleProbe).inFlight = 1
	got := assessIdle(in)
	if got.Reap {
		t.Fatal("assessIdle().Reap = true with an in-flight system entry, want false")
	}
	if got.InFlight != obsBusy {
		t.Errorf("InFlight = %v, want obsBusy", got.InFlight)
	}
}

func TestAssessIdle_NoUnifiedRuntime_DoesNotReap(t *testing.T) {
	in := idleTestInputs(t)
	p := in.Probe.(*fakeIdleProbe)
	p.inFlight = 0 // the trap again: zero from a probe that could not run
	p.inFlightSeen = false
	got := assessIdle(in)
	if got.Reap {
		t.Fatal("assessIdle().Reap = true with no UnifiedRuntime to observe; absent is not clean")
	}
	if got.InFlight != obsUnavailable {
		t.Errorf("InFlight = %v, want obsUnavailable", got.InFlight)
	}
}

func TestAssessIdle_NilProbe_DoesNotReap(t *testing.T) {
	in := idleTestInputs(t)
	in.Probe = nil
	got := assessIdle(in)
	if got.Reap {
		t.Fatal("assessIdle().Reap = true with no runtime probe at all, want false")
	}
}

func TestAssessIdle_OutstandingQuestion_DoesNotReap(t *testing.T) {
	in := idleTestInputs(t)
	in.Questions = &fakeQuestions{pending: map[string]bool{"alice": true}}
	got := assessIdle(in)
	if got.Reap {
		t.Fatal("assessIdle().Reap = true with an outstanding ask_user_question; the answer would be delivered to a dead process")
	}
	if got.Question != obsBusy {
		t.Errorf("Question = %v, want obsBusy", got.Question)
	}
}

func TestAssessIdle_NilQuestionQueue_DoesNotReap(t *testing.T) {
	in := idleTestInputs(t)
	in.Questions = nil
	got := assessIdle(in)
	if got.Reap {
		t.Fatal("assessIdle().Reap = true with no question queue to consult; unavailable is not 'no questions'")
	}
	if got.Question != obsUnavailable {
		t.Errorf("Question = %v, want obsUnavailable", got.Question)
	}
}

func TestAssessIdle_ZeroLastActivity_DoesNotReap(t *testing.T) {
	in := idleTestInputs(t)
	in.Probe.(*fakeIdleProbe).lastAct = time.Time{}
	got := assessIdle(in)
	if got.Reap {
		t.Fatal("assessIdle().Reap = true with a zero LastActivityAt; the zero time is 'never observed', and it is also infinitely old — the arm that must NOT be taken")
	}
	if got.Quiescent != obsUnavailable {
		t.Errorf("Quiescent = %v, want obsUnavailable", got.Quiescent)
	}
}

// TestAssessIdle_ThresholdBoundary pins > rather than >=, in both directions,
// so a future refactor cannot loosen the comparison unnoticed.
func TestAssessIdle_ThresholdBoundary(t *testing.T) {
	cases := []struct {
		name     string
		age      time.Duration
		wantReap bool
	}{
		{"one_below", idleTestThreshold - time.Nanosecond, false},
		{"exactly_at", idleTestThreshold, false},
		{"one_above", idleTestThreshold + time.Nanosecond, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := idleTestInputs(t)
			in.Probe.(*fakeIdleProbe).lastAct = in.Now.Add(-tc.age)
			got := assessIdle(in)
			if got.Reap != tc.wantReap {
				t.Errorf("age=%v: Reap = %v, want %v (blocker=%q)", tc.age, got.Reap, tc.wantReap, got.Blocker)
			}
		})
	}
}

// TestAssessIdle_RootIsNeverReaped: weave owns the TUI and the operator's
// session. Reaping it kills the fleet's console.
func TestAssessIdle_RootIsNeverReaped(t *testing.T) {
	in := idleTestInputs(t)
	in.Name = "weave"
	in.RootName = "weave"
	got := assessIdle(in)
	if got.Reap {
		t.Fatal("assessIdle().Reap = true for the root agent, want false")
	}
	if got.NotRoot != obsBusy {
		t.Errorf("NotRoot = %v, want obsBusy", got.NotRoot)
	}
}

func TestAssessIdle_EmptyNameIsNeverReaped(t *testing.T) {
	in := idleTestInputs(t)
	in.Name = ""
	if got := assessIdle(in); got.Reap {
		t.Fatal("assessIdle().Reap = true for an empty agent name, want false")
	}
}

// TestAssessIdle_ZeroThresholdIsDisabled: the config knob's 0 means OFF. The
// ticker is not even started when the threshold is zero, but the predicate must
// refuse independently — otherwise a future caller that skips the start-time
// check would reap everything, since every age exceeds a zero threshold.
func TestAssessIdle_ZeroThresholdIsDisabled(t *testing.T) {
	in := idleTestInputs(t)
	in.Threshold = 0
	got := assessIdle(in)
	if got.Reap {
		t.Fatal("assessIdle().Reap = true with threshold 0, want false (0 disables the reaper)")
	}
	if got.Blocker != "disabled" {
		t.Errorf("Blocker = %q, want %q", got.Blocker, "disabled")
	}
}

// --- the two AgentRuntime observation methods the predicate is built on ------
//
// The fake probe above cannot prove *AgentRuntime* answers "unavailable" where
// it should; these do. They are the bridge between the predicate's tri-state
// and the real handle probes.

// unobservableTurnHandle is a RuntimeHandle that implements NEITHER turnProbe
// nor a UnifiedRuntime — i.e. exactly the shape whose InTurn() returns a
// meaningless false.
type unobservableTurnHandle struct{ *runtimeTestSession }

// observableTurnHandle implements turnProbe and exposes a UnifiedRuntime.
type observableTurnHandle struct {
	*runtimeTestSession
	urt    *runtimepkg.UnifiedRuntime
	inTurn bool
}

func (h *observableTurnHandle) InTurn() bool                               { return h.inTurn }
func (h *observableTurnHandle) UnifiedRuntime() *runtimepkg.UnifiedRuntime { return h.urt }

func newIdleTestRuntime(t *testing.T, handle RuntimeHandle) *AgentRuntime {
	t.Helper()
	tmp := t.TempDir()
	saveTestAgentForRuntime(t, tmp, "alice")
	rt := NewAgentRuntime(AgentRuntimeConfig{SprawlRoot: tmp, Agent: testAgentState("alice")})
	if handle != nil {
		rt.AttachHandle(handle)
	}
	return rt
}

func TestAgentRuntime_InTurnObserved_ReportsUnavailableSeparatelyFromFalse(t *testing.T) {
	t.Run("no handle", func(t *testing.T) {
		rt := newIdleTestRuntime(t, nil)
		inTurn, observed := rt.InTurnObserved()
		if observed {
			t.Errorf("observed = true with no handle, want false")
		}
		if inTurn {
			t.Errorf("inTurn = true with no handle, want false")
		}
	})
	t.Run("handle is not a turnProbe", func(t *testing.T) {
		rt := newIdleTestRuntime(t, &unobservableTurnHandle{&runtimeTestSession{sessionID: "s"}})
		if _, observed := rt.InTurnObserved(); observed {
			t.Error("observed = true for a handle that does not implement turnProbe, want false")
		}
		// The contrast that makes the point: the legacy accessor cannot tell
		// this case from a genuinely idle agent.
		if rt.InTurn() {
			t.Error("precondition: InTurn() should return false here — that is exactly the ambiguity InTurnObserved exists to remove")
		}
	})
	t.Run("handle is a turnProbe", func(t *testing.T) {
		for _, want := range []bool{true, false} {
			rt := newIdleTestRuntime(t, &observableTurnHandle{
				runtimeTestSession: &runtimeTestSession{sessionID: "s"},
				urt:                runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice"}),
				inTurn:             want,
			})
			inTurn, observed := rt.InTurnObserved()
			if !observed {
				t.Fatalf("observed = false for a turnProbe handle, want true")
			}
			if inTurn != want {
				t.Errorf("inTurn = %v, want %v", inTurn, want)
			}
		}
	})
}

func TestAgentRuntime_InFlightSystemObserved_ReportsUnavailableSeparatelyFromZero(t *testing.T) {
	t.Run("no unified runtime", func(t *testing.T) {
		rt := newIdleTestRuntime(t, &unobservableTurnHandle{&runtimeTestSession{sessionID: "s"}})
		n, observed := rt.InFlightSystemObserved()
		if observed {
			t.Errorf("observed = true with no UnifiedRuntime, want false")
		}
		if n != 0 {
			t.Errorf("n = %d, want 0", n)
		}
	})
	t.Run("unified runtime with nothing in flight", func(t *testing.T) {
		rt := newIdleTestRuntime(t, &observableTurnHandle{
			runtimeTestSession: &runtimeTestSession{sessionID: "s"},
			urt:                runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice"}),
		})
		n, observed := rt.InFlightSystemObserved()
		if !observed {
			t.Fatal("observed = false with a live UnifiedRuntime, want true")
		}
		if n != 0 {
			t.Errorf("n = %d, want 0", n)
		}
	})
}

// TestQuestionQueue_HasPendingFrom is the predicate's question term against the
// real queue, including the negative control: a question from a DIFFERENT agent
// must not block alice's reap.
func TestQuestionQueue_HasPendingFrom(t *testing.T) {
	q := newQuestionQueue()
	// ask() short-circuits with OutcomeTUIUnavailable when no consumer is
	// registered, so without this the question would never reach the queue and
	// the test would pass vacuously.
	if err := q.register(newFakeConsumer("tui")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if q.hasPendingFrom("alice") {
		t.Fatal("hasPendingFrom(alice) = true on an empty queue, want false")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = q.ask(context.Background(), QuestionRequest{
			RequestID: "q1",
			From:      "bob",
			Questions: []Question{{ID: "q1a", Prompt: "?"}},
		})
	}()
	waitFor(t, func() bool { return q.hasPendingFrom("bob") }, "question from bob to be queued")

	if q.hasPendingFrom("alice") {
		t.Error("hasPendingFrom(alice) = true while only bob has a question outstanding; the term must be per-agent")
	}

	q.closeAll(OutcomeSessionEnded, "test over")
	<-done
	if q.hasPendingFrom("bob") {
		t.Error("hasPendingFrom(bob) = true after closeAll, want false")
	}
}

// waitFor polls cond until it holds or the deadline expires.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after 2s waiting for %s", what)
}

// --- the defect the e2e busy-agent control found ----------------------------

// phaseSessionHandle is a minimal runtime.SessionHandle so a test can drive a
// real UnifiedRuntime's turn-phase machine.
type phaseSessionHandle struct{}

func (phaseSessionHandle) WriteUserMessage(context.Context, protocol.UserMessage) error { return nil }
func (phaseSessionHandle) Interrupt(context.Context) error                              { return nil }
func (phaseSessionHandle) CancelAsyncMessage(context.Context, string) (bool, error) {
	return false, nil
}

// TestAgentRuntime_InTurnObserved_CountsARuntimePhaseTurn is the regression for
// the defect the e2e row's busy-agent control caught on its first live run: a
// child running a 90-second Bash tool call was reclaimed mid-turn.
//
// Cause: unifiedHandle.InTurn() forwards to backend Session.InTurn(), which
// tracks SPRAWL-INITIATED turns only. A child executing its spawn prompt is
// driven by the CLI's own argv, so that probe reads false for the entire turn,
// and with no frames arriving during a long tool call LastActivityAt goes stale
// too — every predicate term said "idle" while the agent was working.
//
// UnifiedRuntime's phase machine is the authority (unified.go: "the sole
// in_turn authority: State().InTurn == (phase != phaseIdle)"), so the
// observation must be the UNION of the two signals. Neither alone is complete:
// the phase machine is absent on non-unified handles, and the session probe is
// what the rest of the tree already reads.
func TestAgentRuntime_InTurnObserved_CountsARuntimePhaseTurn(t *testing.T) {
	urt := runtimepkg.New(runtimepkg.RuntimeConfig{
		Name:    "alice",
		Session: phaseSessionHandle{},
	})
	if err := urt.Start(context.Background()); err != nil {
		t.Fatalf("urt.Start: %v", err)
	}
	t.Cleanup(func() { _ = urt.Stop(context.Background()) })

	// The handle's own turn probe stays FALSE for the whole test — that is the
	// trap. Only the runtime phase moves.
	rt := newIdleTestRuntime(t, &observableTurnHandle{
		runtimeTestSession: &runtimeTestSession{sessionID: "s"},
		urt:                urt,
		inTurn:             false,
	})

	if inTurn, observed := rt.InTurnObserved(); !observed || inTurn {
		t.Fatalf("precondition: InTurnObserved() = (%v, %v) before any turn, want (false, true)", inTurn, observed)
	}

	if _, err := urt.WriteUserPrompt(context.Background(), "do a long thing", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	if !urt.State().InTurn {
		t.Fatal("precondition: the runtime's phase machine did not enter a turn, so this test cannot measure what it claims")
	}

	inTurn, observed := rt.InTurnObserved()
	if !observed {
		t.Fatal("observed = false with a live UnifiedRuntime, want true")
	}
	if !inTurn {
		t.Error("InTurnObserved() = false while the runtime's phase machine reports a turn in progress. " +
			"The reaper would tear this agent down mid-work — this is exactly what the e2e busy-agent control caught.")
	}
}

// turnProbeOnlyHandle implements turnProbe but exposes NO UnifiedRuntime — so
// the only turn signal available is the weaker, sprawl-initiated-turns-only
// one.
type turnProbeOnlyHandle struct {
	*runtimeTestSession
	inTurn bool
}

func (h *turnProbeOnlyHandle) InTurn() bool { return h.inTurn }

// TestAgentRuntime_InTurnObserved_RequiresThePhaseAuthority is the true-D1a
// pin. A union that accepted the session probe's "not in turn" when the phase
// machine is absent would still be deriving a NEGATIVE answer from an
// UNAVAILABLE observation — the same defect, wearing a union's clothes. Where
// the authority cannot be read, the term is unavailable and the agent is never
// reaped; the weaker probe may only ADD busy-ness.
func TestAgentRuntime_InTurnObserved_RequiresThePhaseAuthority(t *testing.T) {
	t.Run("no phase authority, probe says idle -> unavailable", func(t *testing.T) {
		rt := newIdleTestRuntime(t, &turnProbeOnlyHandle{
			runtimeTestSession: &runtimeTestSession{sessionID: "s"},
			inTurn:             false,
		})
		inTurn, observed := rt.InTurnObserved()
		if observed {
			t.Error("observed = true with no UnifiedRuntime to read the phase machine from; " +
				"accepting the session probe's 'idle' here is a negative answer built on an unavailable observation")
		}
		if inTurn {
			t.Error("inTurn = true, want false alongside observed=false")
		}
	})
	t.Run("no phase authority, probe says busy -> still blocks", func(t *testing.T) {
		rt := newIdleTestRuntime(t, &turnProbeOnlyHandle{
			runtimeTestSession: &runtimeTestSession{sessionID: "s"},
			inTurn:             true,
		})
		// Either answer blocks the reap; what must NOT happen is obsIdle.
		in := idleTestInputs(t)
		in.Probe = rt
		if got := assessIdle(in); got.Reap {
			t.Error("assessIdle().Reap = true for a handle with no phase authority")
		}
	})
}
