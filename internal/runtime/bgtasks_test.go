// QUM-1197 item 2: the outstanding-background-work set maintained off
// `system`/`background_tasks_changed`.
//
// The set is what the idle reaper's seventh term reads. The three properties
// that make it safe are each pinned separately here, because each one failing
// produces a term that still LOOKS like it works:
//
//   - never-observed is not observed-empty. A runtime that has never seen the
//     frame knows nothing; reporting "no work" there is a silent licence to reap.
//   - a frame we could not parse is not an empty set. tower's own census
//     returned a confident false ZERO twice from a broken jq before he fixed it;
//     the only reason he distrusted it was having already read one frame by eye.
//   - the set is LEVEL-TRIGGERED (replace, not merge), and FirstSeen must be
//     carried forward across frames, or every frame resets every age and the
//     refusal record can no longer show a task wedged for two hours.
//
// These drive rt.routeFrame directly, the established style in this package
// (see phase_test.go's stateFrame / feedInit / newPhaseRuntime).
package runtime

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// bgFrame builds a background_tasks_changed frame carrying exactly the given
// tasks, and its TurnInfo tag.
//
// It populates Raw, which is load-bearing: protocol.ParseAs reads ONLY Raw, so a
// helper that left it nil would make every test below silently exercise the
// parse-failure arm and prove nothing about the happy path.
func bgFrame(t *testing.T, tasks ...protocol.BackgroundTask) (*protocol.Message, backend.TurnInfo) {
	t.Helper()
	if tasks == nil {
		tasks = []protocol.BackgroundTask{}
	}
	payload := map[string]any{
		"type":    "system",
		"subtype": protocol.SubtypeBackgroundTasksChanged,
		"tasks":   tasks,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal bg frame: %v", err)
	}
	return &protocol.Message{
		Type:    "system",
		Subtype: protocol.SubtypeBackgroundTasksChanged,
		Raw:     raw,
	}, backend.TurnInfo{Autonomous: true, PreInit: true}
}

func bgTask(id, typ string) protocol.BackgroundTask {
	return protocol.BackgroundTask{TaskID: id, TaskType: typ, Description: "desc-" + id}
}

// newBGRuntime builds a runtime with a controllable clock so FirstSeen and the
// ages derived from it are deterministic.
func newBGRuntime(now func() time.Time) *UnifiedRuntime {
	return New(RuntimeConfig{Name: "bg-agent", Session: &mockUnifiedSession{}, Now: now})
}

// TestBackgroundTasks_NeverObserved_IsUnavailable is the arm the whole term
// rests on. Direction: MUST report observed=false. Positive control: initialise
// the observed flag to true in New and this fires.
func TestBackgroundTasks_NeverObserved_IsUnavailable(t *testing.T) {
	rt := newBGRuntime(nil)

	tasks, observed := rt.WorkOutstandingObserved()
	if observed {
		t.Errorf("observed = true for a runtime that has NEVER seen a background_tasks_changed frame (tasks=%v); never-observed and observed-empty are different facts, and only one of them may permit a reap", tasks)
	}
}

// TestBackgroundTasks_ObservedEmpty_IsObservedAndEmpty is its counterpart, and
// the positive control that the accessor can report observed at all: an EMITTED
// tasks:[] is a fact about the world.
func TestBackgroundTasks_ObservedEmpty_IsObservedAndEmpty(t *testing.T) {
	rt := newBGRuntime(nil)

	rt.routeFrame(bgFrame(t))

	tasks, observed := rt.WorkOutstandingObserved()
	if !observed {
		t.Fatalf("observed = false after an emitted tasks:[] frame; the drain frame is the only thing that ever says 'nothing outstanding'")
	}
	if len(tasks) != 0 {
		t.Errorf("len(tasks) = %d after a drain frame, want 0: %+v", len(tasks), tasks)
	}
}

// TestBackgroundTasks_OneTask_IsObservedAndBusy: the plain positive case.
func TestBackgroundTasks_OneTask_IsObservedAndBusy(t *testing.T) {
	rt := newBGRuntime(nil)

	rt.routeFrame(bgFrame(t, bgTask("b1", protocol.BackgroundTaskLocalBash)))

	tasks, observed := rt.WorkOutstandingObserved()
	if !observed || len(tasks) != 1 {
		t.Fatalf("got (%d tasks, observed=%v), want (1, true)", len(tasks), observed)
	}
	if tasks[0].TaskID != "b1" || tasks[0].TaskType != protocol.BackgroundTaskLocalBash {
		t.Errorf("task = %+v, want id b1 / type local_bash", tasks[0])
	}
}

// TestBackgroundTasks_LevelTriggered_ReplacesSetAndKeepsFirstSeen pins both
// halves of the level-triggered contract at once, because a merge-instead-of-
// replace bug and a FirstSeen-reset bug both leave a set that looks plausible:
//
//   - REPLACE: a task absent from the newest frame is gone, not remembered.
//   - CARRY FORWARD: a task present in both frames keeps its ORIGINAL FirstSeen,
//     or the refusal record's per-task age resets on every frame and can never
//     show the two-hour wedge it exists to make visible.
func TestBackgroundTasks_LevelTriggered_ReplacesSetAndKeepsFirstSeen(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := base
	rt := newBGRuntime(func() time.Time { return clock })

	rt.routeFrame(bgFrame(t, bgTask("a", protocol.BackgroundTaskLocalBash), bgTask("b", protocol.BackgroundTaskLocalAgent)))
	clock = base.Add(90 * time.Second)
	rt.routeFrame(bgFrame(t, bgTask("b", protocol.BackgroundTaskLocalAgent), bgTask("c", protocol.BackgroundTaskLocalBash)))

	tasks, observed := rt.WorkOutstandingObserved()
	if !observed {
		t.Fatal("observed = false after two well-formed frames")
	}
	byID := map[string]OutstandingTask{}
	for _, task := range tasks {
		byID[task.TaskID] = task
	}
	if _, stillThere := byID["a"]; stillThere {
		t.Errorf("task 'a' survived a frame that did not list it; the set is level-triggered — every frame is the whole truth, so this is a merge bug: %+v", tasks)
	}
	if len(tasks) != 2 {
		t.Errorf("len(tasks) = %d, want 2 (b and c): %+v", len(tasks), tasks)
	}
	if got := byID["b"].FirstSeen; !got.Equal(base) {
		t.Errorf("task 'b' FirstSeen = %v, want the FIRST frame's %v; resetting it on every frame destroys the per-task age the refusal record reports", got, base)
	}
	if got := byID["c"].FirstSeen; !got.Equal(base.Add(90 * time.Second)) {
		t.Errorf("task 'c' FirstSeen = %v, want the second frame's %v", got, base.Add(90*time.Second))
	}
}

// TestBackgroundTasks_MalformedFrame_DoesNotReadAsEmpty is the confident-false-
// zero arm. Direction: after an unparseable frame the accessor MUST report
// observed=false (we no longer know the set) and MUST NOT have discarded the
// tasks it did know about. The recovery half matters too: the next well-formed
// frame carries the whole truth, so it clears the doubt.
func TestBackgroundTasks_MalformedFrame_DoesNotReadAsEmpty(t *testing.T) {
	rt := newBGRuntime(nil)
	rt.routeFrame(bgFrame(t, bgTask("b1", protocol.BackgroundTaskLocalBash)))

	// Same subtype, unreadable body.
	rt.routeFrame(
		&protocol.Message{Type: "system", Subtype: protocol.SubtypeBackgroundTasksChanged, Raw: []byte(`{"tasks":`)},
		backend.TurnInfo{Autonomous: true, PreInit: true},
	)
	tasks, observed := rt.WorkOutstandingObserved()
	if observed {
		t.Errorf("observed = true after an UNPARSEABLE frame (tasks=%+v); a scan that failed is not a scan that came back clean", tasks)
	}
	// The PRESERVATION half. The doc comment claimed it and nothing checked it:
	// a mutation that cleared bgTasks on a parse failure left the whole suite
	// green. Asserted on the field directly (this test is in package runtime)
	// because the accessor deliberately hides the set while observed is false.
	rt.bgMu.Lock()
	kept := len(rt.bgTasks)
	rt.bgMu.Unlock()
	if kept != 1 {
		t.Errorf("known set size = %d after an unreadable frame, want 1; the frame told us nothing, so discarding what we already knew turns 'cannot tell' into 'lost it'", kept)
	}

	// A frame with the subtype but no tasks key at all: same reading.
	rt.routeFrame(
		&protocol.Message{Type: "system", Subtype: protocol.SubtypeBackgroundTasksChanged, Raw: []byte(`{"type":"system","subtype":"background_tasks_changed"}`)},
		backend.TurnInfo{Autonomous: true, PreInit: true},
	)
	if _, observed := rt.WorkOutstandingObserved(); observed {
		t.Error("observed = true after a frame with NO tasks key; an absent key is not an empty set")
	}

	// Recovery: the next good frame is the whole truth.
	rt.routeFrame(bgFrame(t))
	tasks, observed = rt.WorkOutstandingObserved()
	if !observed || len(tasks) != 0 {
		t.Errorf("after recovery got (%d tasks, observed=%v), want (0, true); a well-formed frame must clear the doubt", len(tasks), observed)
	}
}

// TestBackgroundTasks_UnknownTaskType_StillBlocks: the census closed the
// vocabulary as of a measurement, not forever. Direction: an unknown task_type
// MUST still appear in the set — filtering on the known two would make a future
// CLI's new task type silently reapable.
func TestBackgroundTasks_UnknownTaskType_StillBlocks(t *testing.T) {
	rt := newBGRuntime(nil)

	rt.routeFrame(bgFrame(t, bgTask("z1", "local_something_new")))

	tasks, observed := rt.WorkOutstandingObserved()
	if !observed || len(tasks) != 1 {
		t.Fatalf("got (%d tasks, observed=%v), want (1, true) for an unknown task_type", len(tasks), observed)
	}
}

// TestBackgroundTasks_Frame_DoesNotOpenATurnAndStillPublishes is the
// don't-perturb pin. The frame must feed the set without touching the phase
// machine, and must keep being published as EventProtocolMessage — that publish
// is the TUI/telemetry stream and LastActivityAt's source (QUM-1213's subject,
// not ours), so silently swallowing it here would be a cross-lane regression.
func TestBackgroundTasks_Frame_DoesNotOpenATurnAndStillPublishes(t *testing.T) {
	rt := newBGRuntime(nil)
	sub, unsub := rt.EventBus().Subscribe(8)
	defer unsub()

	rt.routeFrame(bgFrame(t, bgTask("b1", protocol.BackgroundTaskLocalBash)))

	if rt.State().InTurn {
		t.Error("InTurn = true after a background_tasks_changed frame; it must never open a turn")
	}
	var sawProtocol, sawTurnStarted bool
	deadline := time.After(time.Second)
collect:
	for {
		select {
		case ev := <-sub:
			switch ev.Type {
			case EventProtocolMessage:
				if ev.Message != nil && ev.Message.Subtype == protocol.SubtypeBackgroundTasksChanged {
					sawProtocol = true
				}
			case EventTurnStarted:
				sawTurnStarted = true
			}
		case <-deadline:
			break collect
		default:
			break collect
		}
	}
	if !sawProtocol {
		t.Error("no EventProtocolMessage published for the frame; the TUI/telemetry stream (and LastActivityAt) must keep seeing it")
	}
	if sawTurnStarted {
		t.Error("EventTurnStarted published for a background_tasks_changed frame; it is publish-only")
	}
}

// TestBackgroundTasks_ConcurrentIngestAndRead exists to make the set's mutex
// FALSIFIABLE. Production ingests on the reader goroutine and the reaper reads
// from the sweep goroutine, so the lock is required — but without a test that
// drives both at once, deleting it leaves -race green and the lock is merely
// intended. Direction: with the lock removed, -race MUST report here.
func TestBackgroundTasks_ConcurrentIngestAndRead(t *testing.T) {
	rt := newBGRuntime(nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			rt.routeFrame(bgFrame(t, bgTask(fmt.Sprintf("b%d", i), protocol.BackgroundTaskLocalBash)))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, _ = rt.WorkOutstandingObserved()
		}
	}()
	wg.Wait()
}

// TestBackgroundTasks_SortedOldestFirstThenByID pins the ordering the accessor's
// docstring promises. Without it the claim is decoration: reversing both
// comparators left the runtime AND supervisor suites green, because the record
// assertions only look for substrings. Order matters because the refusal record
// is truncated at a cap — with the wrong order, truncation drops the OLDEST
// tasks, which are exactly the wedged ones the record exists to surface.
func TestBackgroundTasks_SortedOldestFirstThenByID(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := base
	rt := newBGRuntime(func() time.Time { return clock })

	// "old" is seen first; then a tie pair ("t-b", "t-a") arrives together, so the
	// id comparator is the only thing that can order them.
	rt.routeFrame(bgFrame(t, bgTask("old", protocol.BackgroundTaskLocalBash)))
	clock = base.Add(time.Minute)
	rt.routeFrame(bgFrame(t,
		bgTask("old", protocol.BackgroundTaskLocalBash),
		bgTask("t-b", protocol.BackgroundTaskLocalAgent),
		bgTask("t-a", protocol.BackgroundTaskLocalAgent),
	))

	tasks, observed := rt.WorkOutstandingObserved()
	if !observed || len(tasks) != 3 {
		t.Fatalf("got (%d tasks, observed=%v), want (3, true)", len(tasks), observed)
	}
	var ids []string
	for _, task := range tasks {
		ids = append(ids, task.TaskID)
	}
	want := []string{"old", "t-a", "t-b"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order = %v, want %v (oldest FirstSeen first, then by task id)", ids, want)
		}
	}
}

// --- QUM-1197 (c) + PROVENANCE ---------------------------------------------
//
// Ruling of 2026-08-11: never-observed no longer blocks forever. 207 of 361
// recorded sessions never emit a task frame at all, because the CLI emits one
// only when the set CHANGES — so the strict rule left a majority of the fleet
// permanently unreclaimable and turned a passing gated row red. Observed-empty
// may now be concluded from the ABSENCE of any task frame, but only once the
// session has demonstrably been talking to us: a system/init AND at least one
// completed turn with no task frame in between.
//
// The relaxation is paid for with PROVENANCE: every conclusion records whether
// it rests on a real emitted tasks:[] or on absence, so the one shape this
// trades away — the CLI renaming or dropping the subtype — stays detectable by
// a single grep (fleet-wide by_absence) instead of being silent.

// closeTurn drives the frames that make a completed turn observable: an init
// and a terminal result.
func closeTurn(rt *UnifiedRuntime) {
	rt.routeFrame(&protocol.Message{Type: "system", Subtype: "init"}, backend.TurnInfo{Autonomous: true})
	rt.routeFrame(&protocol.Message{Type: "result", Subtype: "success"}, backend.TurnInfo{Autonomous: true, EndOfTurn: true})
}

// TestWorkBasis_NoFramesAndNoCompletedTurn_IsUnobservable: before the session
// has proved it is talking to us, absence is not evidence. Direction: MUST
// report unobservable — this is the arm that keeps a silent or broken session
// from being reaped on the strength of hearing nothing.
func TestWorkBasis_NoFramesAndNoCompletedTurn_IsUnobservable(t *testing.T) {
	rt := newBGRuntime(nil)

	if _, basis := rt.WorkOutstanding(); basis != WorkUnobservable {
		t.Errorf("basis = %v for a runtime that has seen nothing at all, want %v", basis, WorkUnobservable)
	}
	// An init alone is not enough: the turn must complete without a task frame.
	rt.routeFrame(&protocol.Message{Type: "system", Subtype: "init"}, backend.TurnInfo{Autonomous: true})
	if _, basis := rt.WorkOutstanding(); basis != WorkUnobservable {
		t.Errorf("basis = %v after an init with no completed turn, want %v", basis, WorkUnobservable)
	}
}

// TestWorkBasis_CompletedTurnWithNoFrames_IsByAbsence is the arm that unblocks
// the 57.3%. Direction: MUST become observable, and MUST be labelled by_absence
// rather than passed off as an observation.
func TestWorkBasis_CompletedTurnWithNoFrames_IsByAbsence(t *testing.T) {
	rt := newBGRuntime(nil)

	closeTurn(rt)

	tasks, basis := rt.WorkOutstanding()
	if basis != WorkByAbsence {
		t.Fatalf("basis = %v after a completed turn with no task frame, want %v; without this a majority of real sessions are permanently unreclaimable", basis, WorkByAbsence)
	}
	if len(tasks) != 0 {
		t.Errorf("tasks = %+v under by_absence, want none", tasks)
	}
}

// TestWorkBasis_RealFrameBeatsAbsence: once the CLI has spoken, the set is the
// authority and the conclusion is labelled as observed. Both orders are driven,
// because a frame can arrive before or after the first completed turn and the
// provenance must not depend on which.
func TestWorkBasis_RealFrameBeatsAbsence(t *testing.T) {
	t.Run("frame after a completed turn", func(t *testing.T) {
		rt := newBGRuntime(nil)
		closeTurn(rt)
		rt.routeFrame(bgFrame(t, bgTask("b1", protocol.BackgroundTaskLocalBash)))

		tasks, basis := rt.WorkOutstanding()
		if basis != WorkObserved || len(tasks) != 1 {
			t.Fatalf("got (%d tasks, basis=%v), want (1, %v)", len(tasks), basis, WorkObserved)
		}
	})
	t.Run("frame before any completed turn", func(t *testing.T) {
		rt := newBGRuntime(nil)
		rt.routeFrame(bgFrame(t))
		if _, basis := rt.WorkOutstanding(); basis != WorkObserved {
			t.Errorf("basis = %v after an emitted tasks:[] with no completed turn, want %v; a real frame needs no corroboration", basis, WorkObserved)
		}
	})
}

// TestWorkBasis_ParseFailureIsStillUnobservable_EvenAfterACompletedTurn is the
// arm the relaxation must NOT weaken. A frame we could not read is a failed
// scan, and a completed turn does not license reading it as absence — otherwise
// the (c) rule would launder every parse failure into a reap.
func TestWorkBasis_ParseFailureIsStillUnobservable_EvenAfterACompletedTurn(t *testing.T) {
	rt := newBGRuntime(nil)
	closeTurn(rt)
	rt.routeFrame(bgFrame(t, bgTask("b1", protocol.BackgroundTaskLocalBash)))

	rt.routeFrame(
		&protocol.Message{Type: "system", Subtype: protocol.SubtypeBackgroundTasksChanged, Raw: []byte(`{"tasks":`)},
		backend.TurnInfo{Autonomous: true, PreInit: true},
	)

	if _, basis := rt.WorkOutstanding(); basis != WorkUnobservable {
		t.Errorf("basis = %v after an unreadable frame on a session with a completed turn, want %v; absence-based reasoning must not launder a failed read", basis, WorkUnobservable)
	}
}
