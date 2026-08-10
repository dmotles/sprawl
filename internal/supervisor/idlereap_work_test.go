// QUM-1197 item 2: the seventh reap term, `work_outstanding`.
//
// The defect it closes is the one this issue exists for: all six previous terms
// read `idle` HONESTLY while the agent had work it intended to return to. An
// agent that backgrounds a tool call or spawns a sidechain and ends its turn was
// reaped, the work died with it, and the wake notification it was waiting for
// never arrived — silent partial work loss with no error anywhere.
//
// The arm that carries the design is TRI-STATE, and it is the one an
// implementation is most likely to get wrong in the safe-looking direction:
// never having seen a background_tasks_changed frame is NOT the same fact as
// having seen `tasks:[]`. If the CLI renames the subtype or stops emitting, a
// set-valued term silently reads "no work" and the protection evaporates with no
// error. Hence TestAssessIdle_WorkOutstanding_NeverObservedIsNotObservedEmpty,
// which differs from its own positive control by exactly one bool.
package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
)

// feedBackgroundTasks delivers a background_tasks_changed frame through the
// runtime's installed frame router — the same seam a real backend session uses,
// so this drives the production ingest rather than reaching into runtime state.
//
// Called with no tasks it delivers the emitted `tasks:[]` drain frame, which is
// the positive statement "nothing outstanding" and is what makes an otherwise
// idle fixture reapable. Raw is populated because protocol.ParseAs reads only
// Raw: a helper that left it nil would feed every caller the parse-failure arm.
func feedBackgroundTasks(t *testing.T, sess *runtimeTestSession, tasks ...protocol.BackgroundTask) {
	t.Helper()
	if tasks == nil {
		tasks = []protocol.BackgroundTask{}
	}
	raw, err := json.Marshal(map[string]any{
		"type":    "system",
		"subtype": protocol.SubtypeBackgroundTasksChanged,
		"tasks":   tasks,
	})
	if err != nil {
		t.Fatalf("marshal background_tasks_changed: %v", err)
	}
	sess.routeFrameTo(t)(
		&protocol.Message{Type: "system", Subtype: protocol.SubtypeBackgroundTasksChanged, Raw: raw},
		backendpkg.TurnInfo{Autonomous: true, PreInit: true},
	)
}

// workTask builds one outstanding task, first seen `age` ago relative to
// idleTestInputs' fixed clock.
func workTask(id, typ string, seenAt time.Time) runtimepkg.OutstandingTask {
	return runtimepkg.OutstandingTask{
		BackgroundTask: protocol.BackgroundTask{TaskID: id, TaskType: typ, Description: "desc-" + id},
		FirstSeen:      seenAt,
	}
}

// TestAssessIdle_WorkOutstanding_DoesNotReap is the core POSITIVE control: the
// fixture is idle on every other term, so if the reap is refused it is this term
// doing it — and the blocker name says so.
func TestAssessIdle_WorkOutstanding_DoesNotReap(t *testing.T) {
	in := idleTestInputs(t)
	probe := in.Probe.(*fakeIdleProbe)
	probe.workObserved = true
	probe.workTasks = []runtimepkg.OutstandingTask{
		workTask("b1", protocol.BackgroundTaskLocalBash, in.Now.Add(-3*time.Minute)),
	}

	got := assessIdle(in)
	if got.Reap {
		t.Fatalf("Reap = true for an agent with a live backgrounded task; this is the reap that destroyed 866 seconds of work in the run that reopened QUM-1197 (%+v)", got)
	}
	if got.Blocker != "work_outstanding" {
		t.Errorf("Blocker = %q, want %q; a refusal blamed on another term would send the next reader to the wrong place", got.Blocker, "work_outstanding")
	}
}

// TestAssessIdle_WorkOutstanding_SidechainAloneBlocks: a live sidechain
// (local_agent) is in-process and adds NO pid, which is why every process-table
// design was ruled out — it is permanently blind to the 43 recorded closures
// that had one. Direction: MUST block, on the same footing as a backgrounded
// Bash task.
func TestAssessIdle_WorkOutstanding_SidechainAloneBlocks(t *testing.T) {
	in := idleTestInputs(t)
	probe := in.Probe.(*fakeIdleProbe)
	probe.workObserved = true
	probe.workTasks = []runtimepkg.OutstandingTask{
		workTask("a1", protocol.BackgroundTaskLocalAgent, in.Now.Add(-10*time.Second)),
	}

	if got := assessIdle(in); got.Reap {
		t.Fatalf("Reap = true with one live SIDECHAIN outstanding, want false (%+v)", got)
	}
}

// TestAssessIdle_WorkOutstanding_NeverObservedIsNotObservedEmpty is the arm the
// whole design turns on. Both subtests carry an EMPTY task list; the only delta
// is whether the set was ever observed.
//
//   - observed-empty MUST reap. This subtest is also the positive control that
//     proves the never-observed subtest is not passing vacuously — without it, an
//     implementation that blocked on this term unconditionally would look correct.
//   - never-observed MUST NOT reap, and must be named as UNOBSERVABLE rather than
//     busy, so the refusal record distinguishes "the agent has work" from "we
//     could not tell".
func TestAssessIdle_WorkOutstanding_NeverObservedIsNotObservedEmpty(t *testing.T) {
	t.Run("observed empty reaps", func(t *testing.T) {
		in := idleTestInputs(t)
		probe := in.Probe.(*fakeIdleProbe)
		probe.workObserved = true
		probe.workTasks = nil

		got := assessIdle(in)
		if !got.Reap {
			t.Fatalf("Reap = false for an agent whose emitted task set is EMPTY (blocker=%q); observed-empty is a positive statement that nothing is outstanding, and a term that refuses here is a mechanism that never acts", got.Blocker)
		}
	})
	t.Run("never observed blocks", func(t *testing.T) {
		in := idleTestInputs(t)
		probe := in.Probe.(*fakeIdleProbe)
		probe.workObserved = false
		probe.workTasks = nil

		got := assessIdle(in)
		if got.Reap {
			t.Fatalf("Reap = true for an agent whose task set was NEVER observed; if the CLI renames the subtype or stops emitting, this is where the protection silently evaporates (%+v)", got)
		}
		if got.Blocker != "work_outstanding_unobservable" {
			t.Errorf("Blocker = %q, want %q — asserted on the exact token so a refusal from some OTHER term cannot green this arm", got.Blocker, "work_outstanding_unobservable")
		}
	})
}

// TestAgentRuntime_WorkOutstandingObserved_ReportsUnavailableSeparatelyFromEmpty
// is the bridge from the predicate's tri-state to the real handle, modelled on
// its InFlightSystemObserved sibling. Four arms, and the second is the one a
// naive implementation reports as "empty".
func TestAgentRuntime_WorkOutstandingObserved_ReportsUnavailableSeparatelyFromEmpty(t *testing.T) {
	t.Run("no unified runtime", func(t *testing.T) {
		rt := newIdleTestRuntime(t, &unobservableTurnHandle{&runtimeTestSession{sessionID: "s"}})
		tasks, observed := rt.WorkOutstandingObserved()
		if observed {
			t.Errorf("observed = true with no UnifiedRuntime (tasks=%+v), want false; no runtime to ask is not 'asked, and nothing is outstanding'", tasks)
		}
	})
	t.Run("unified runtime that never saw a frame", func(t *testing.T) {
		rt := newIdleTestRuntime(t, &observableTurnHandle{
			runtimeTestSession: &runtimeTestSession{sessionID: "s"},
			urt:                runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice"}),
		})
		if _, observed := rt.WorkOutstandingObserved(); observed {
			t.Error("observed = true for a runtime that has never seen a background_tasks_changed frame, want false")
		}
	})
	t.Run("fed an emitted empty set", func(t *testing.T) {
		sess := &runtimeTestSession{sessionID: "s"}
		urt := runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice", Session: sess})
		rt := newIdleTestRuntime(t, &observableTurnHandle{runtimeTestSession: sess, urt: urt})
		feedBackgroundTasks(t, sess)
		tasks, observed := rt.WorkOutstandingObserved()
		if !observed || len(tasks) != 0 {
			t.Errorf("got (%d tasks, observed=%v), want (0, true) after an emitted tasks:[]", len(tasks), observed)
		}
	})
	t.Run("fed one task", func(t *testing.T) {
		sess := &runtimeTestSession{sessionID: "s"}
		urt := runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice", Session: sess})
		rt := newIdleTestRuntime(t, &observableTurnHandle{runtimeTestSession: sess, urt: urt})
		feedBackgroundTasks(t, sess, protocol.BackgroundTask{TaskID: "b1", TaskType: protocol.BackgroundTaskLocalBash})
		tasks, observed := rt.WorkOutstandingObserved()
		if !observed || len(tasks) != 1 || tasks[0].TaskID != "b1" {
			t.Errorf("got (%+v, observed=%v), want one task b1 observed", tasks, observed)
		}
	})
}

// TestReclaim_AgentWithOutstandingBackgroundWork_IsNotTornDown is the term at
// the RECLAIM call site, not in the predicate: the wiring half.
// TestReclaim_IdleAgent_IsTornDownAndRestsIdle stays the positive control that
// this fixture otherwise reaps.
func TestReclaim_AgentWithOutstandingBackgroundWork_IsNotTornDown(t *testing.T) {
	r, _, rt, handle := newReclaimFixture(t)
	feedBackgroundTasks(t, handle.runtimeTestSession, protocol.BackgroundTask{
		TaskID: "b-live", TaskType: protocol.BackgroundTaskLocalBash, Description: "sleep 900",
	})

	r.maybeReclaimIdle(context.Background(), rt)

	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("stopCalls = %d for an agent with a live backgrounded task, want 0 — this is the 866-second work loss the issue was reopened for", got)
	}
	if !rt.SubprocessAlive() {
		t.Error("SubprocessAlive() = false; the agent was torn down with work outstanding")
	}
}

// TestReclaim_NeverObservedBackgroundTasks_IsNotTornDown is its
// could-not-tell twin, distinguished from the test above by the RECORD: the
// blocker must name the unobservable arm, not the busy one.
func TestReclaim_NeverObservedBackgroundTasks_IsNotTornDown(t *testing.T) {
	r, _, rt, handle := newReclaimFixtureNoBackgroundFrame(t)

	h := installCaptureSlog(t)
	r.maybeReclaimIdle(context.Background(), rt)

	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("stopCalls = %d for an agent whose task set was never observed, want 0", got)
	}
	got := refusalRecordsAt(t, h, slog.LevelInfo, refusalMsg)
	if len(got) != 1 {
		t.Fatalf("INFO refusal records = %d, want 1. all records:\n%s", len(got), h.String())
	}
	if !strings.Contains(got[0], "blocker=work_outstanding_unobservable") {
		t.Errorf("record does not distinguish 'could not tell' from 'has work': %s", got[0])
	}
}

// TestRefusalRecord_NamesWorkOutstandingWithPerTaskAge is the operator-visible
// half of the settled design: there is deliberately NO auto-expiry of stale
// tasks (any cap short enough to clear a two-hour wedge also clears a real
// `make validate` on a loaded box), so a wedged task must instead be a NAMED
// condition in the record — with its age, or nobody can tell a wedge from work.
func TestRefusalRecord_NamesWorkOutstandingWithPerTaskAge(t *testing.T) {
	r, _, rt, handle := newReclaimFixture(t)
	feedBackgroundTasks(t, handle.runtimeTestSession,
		protocol.BackgroundTask{TaskID: "b-wedged", TaskType: protocol.BackgroundTaskLocalBash},
		protocol.BackgroundTask{TaskID: "a-side", TaskType: protocol.BackgroundTaskLocalAgent},
	)

	h := installCaptureSlog(t)
	r.maybeReclaimIdle(context.Background(), rt)

	got := refusalRecordsAt(t, h, slog.LevelInfo, refusalMsg)
	if len(got) != 1 {
		t.Fatalf("INFO refusal records = %d, want 1. all records:\n%s", len(got), h.String())
	}
	for _, want := range []string{
		"blocker=work_outstanding ",
		"work_outstanding=busy",
		"work_outstanding_n=2",
		"b-wedged",
		"a-side",
		protocol.BackgroundTaskLocalAgent,
	} {
		if !strings.Contains(got[0], want) {
			t.Errorf("refusal record missing %q; without it a wedged task is invisible and the no-auto-expiry decision has no operator-facing half. record: %s", want, got[0])
		}
	}
	// The age must be RENDERED, not just the ids. Asserted as a unit-bearing
	// token rather than an exact value so the test does not pin wall-clock.
	if !strings.Contains(got[0], "age=") {
		t.Errorf("refusal record carries no per-task age; a task outstanding for 2h1m and one started a second ago read identically. record: %s", got[0])
	}
}

// TestRefusalRecord_EmptyObservedSet_CarriesNoTaskDetail is the NEGATIVE
// control for the record: an agent refusing for a different reason, with an
// observed-empty set, must not emit task detail. Without this the assertions
// above would also pass against an implementation that printed the same task
// blob on every refusal.
func TestRefusalRecord_EmptyObservedSet_CarriesNoTaskDetail(t *testing.T) {
	r, _, rt, handle := newReclaimFixture(t)
	handle.inTurn.Store(true)

	h := installCaptureSlog(t)
	r.maybeReclaimIdle(context.Background(), rt)

	got := refusalRecordsAt(t, h, slog.LevelInfo, refusalMsg)
	if len(got) != 1 {
		t.Fatalf("INFO refusal records = %d, want 1. all records:\n%s", len(got), h.String())
	}
	if !strings.Contains(got[0], "work_outstanding=idle") {
		t.Errorf("record does not report the observed-empty set as idle: %s", got[0])
	}
	if !strings.Contains(got[0], "work_outstanding_n=0") {
		t.Errorf("record does not report a zero task count: %s", got[0])
	}
	if strings.Contains(got[0], "age=") {
		t.Errorf("record carries per-task age detail with NO tasks outstanding; the detail must describe the set the decision saw: %s", got[0])
	}
}

// TestRenderOutstanding_TruncationIsVisible pins the "+N more" marker. The
// comment at renderOutstanding claims truncation is visible rather than silent,
// and deleting the marker left the suite green. A 7-task set was measured on a
// real agent and the cap is 8, so this path is reachable in production.
//
// Directions: the marker MUST appear above the cap (positive) and MUST NOT
// appear at or below it (negative) — a marker that always fires would satisfy the
// first assertion alone.
func TestRenderOutstanding_TruncationIsVisible(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	build := func(n int) []runtimepkg.OutstandingTask {
		var out []runtimepkg.OutstandingTask
		for i := 0; i < n; i++ {
			out = append(out, workTask(fmt.Sprintf("b%02d", i), protocol.BackgroundTaskLocalBash, now.Add(-time.Minute)))
		}
		return out
	}

	over := renderOutstanding(build(renderMaxOutstanding+3), now)
	if !strings.Contains(over, fmt.Sprintf("+%d more", 3)) {
		t.Errorf("rendering %d tasks did not report the %d it dropped; silent truncation reads as a complete set: %s",
			renderMaxOutstanding+3, 3, over)
	}
	if got := strings.Count(over, "age="); got != renderMaxOutstanding {
		t.Errorf("rendered %d task details, want the cap %d: %s", got, renderMaxOutstanding, over)
	}

	atCap := renderOutstanding(build(renderMaxOutstanding), now)
	if strings.Contains(atCap, "more") {
		t.Errorf("rendering exactly %d tasks claimed truncation: %s", renderMaxOutstanding, atCap)
	}
	if got := renderOutstanding(nil, now); got != "" {
		t.Errorf("rendering an empty set = %q, want empty", got)
	}
}
