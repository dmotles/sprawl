// Tests for QUM-479: dedicated tea.Msg sentinels for stream-adapter EOF.
//
// AppModel's SessionErrorMsg{io.EOF} handler is the bridge-restart path.
// Both ActivityStreamAdapter and ChildStreamAdapter currently emit the same
// SessionErrorMsg{io.EOF} on cancel/runtime stop, mis-routing harmless
// adapter-teardown signals into a phantom "Session restarting..." banner.
//
// The fix introduces two dedicated sentinel types:
//   - ActivityStreamClosedMsg{Agent, Epoch}
//   - ChildStreamClosedMsg{Agent, Epoch}
//
// These tests are written BEFORE implementation (TDD red phase) and are
// expected to FAIL TO COMPILE until ActivityStreamClosedMsg and
// ChildStreamClosedMsg exist as tea.Msg types and the adapter+AppModel wiring
// is updated.

package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// -- 1 & 3. Cancel emits the dedicated closed sentinel (not SessionErrorMsg) -

func TestActivityStreamAdapter_CancelEmitsClosedSentinel(t *testing.T) {
	rt := newUnifiedRT(t, "alice")
	a := NewActivityStreamAdapter(rt)

	done := make(chan tea.Msg, 1)
	go func() {
		done <- a.WaitForEvent()()
	}()

	// Let WaitForEvent block on the channel before cancelling.
	time.Sleep(20 * time.Millisecond)
	a.Cancel()

	select {
	case msg := <-done:
		switch msg.(type) {
		case ActivityStreamClosedMsg:
			// expected
		case SessionErrorMsg:
			t.Fatalf("WaitForEvent after Cancel returned SessionErrorMsg; want ActivityStreamClosedMsg (QUM-479)")
		default:
			t.Fatalf("WaitForEvent after Cancel returned %T; want ActivityStreamClosedMsg", msg)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("WaitForEvent did not return after Cancel within deadline")
	}
}

func TestChildStreamAdapter_CancelEmitsClosedSentinel(t *testing.T) {
	rt := newUnifiedRT(t, "alice")
	a := NewChildStreamAdapter(rt)

	done := make(chan tea.Msg, 1)
	go func() {
		done <- a.WaitForEvent()()
	}()

	time.Sleep(20 * time.Millisecond)
	a.Cancel()

	select {
	case msg := <-done:
		switch msg.(type) {
		case ChildStreamClosedMsg:
			// expected
		case SessionErrorMsg:
			t.Fatalf("WaitForEvent after Cancel returned SessionErrorMsg; want ChildStreamClosedMsg (QUM-479)")
		default:
			t.Fatalf("WaitForEvent after Cancel returned %T; want ChildStreamClosedMsg", msg)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("WaitForEvent did not return after Cancel within deadline")
	}
}

// -- 2 & 4. Runtime stop (EventBus shutdown) emits the closed sentinel ------
//
// Stopping the UnifiedRuntime closes its EventBus subscriber chans. The
// adapter must surface that as the dedicated closed sentinel — not as
// SessionErrorMsg{io.EOF}, which would mis-route into the bridge-restart
// path.

func TestActivityStreamAdapter_RuntimeStopEmitsClosedSentinel(t *testing.T) {
	rt := newUnifiedRT(t, "alice")
	a := NewActivityStreamAdapter(rt)

	done := make(chan tea.Msg, 1)
	go func() {
		done <- a.WaitForEvent()()
	}()

	time.Sleep(20 * time.Millisecond)
	// Closing the runtime tears down the EventBus and closes subscriber chans.
	rt.Stop(context.Background())

	select {
	case msg := <-done:
		switch msg.(type) {
		case ActivityStreamClosedMsg:
			// expected
		case SessionErrorMsg:
			t.Fatalf("WaitForEvent after runtime stop returned SessionErrorMsg; want ActivityStreamClosedMsg (QUM-479)")
		default:
			t.Fatalf("WaitForEvent after runtime stop returned %T; want ActivityStreamClosedMsg", msg)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("WaitForEvent did not return after runtime stop within deadline")
	}
}

func TestChildStreamAdapter_RuntimeStopEmitsClosedSentinel(t *testing.T) {
	rt := newUnifiedRT(t, "alice")
	a := NewChildStreamAdapter(rt)

	done := make(chan tea.Msg, 1)
	go func() {
		done <- a.WaitForEvent()()
	}()

	time.Sleep(20 * time.Millisecond)
	rt.Stop(context.Background())

	select {
	case msg := <-done:
		switch msg.(type) {
		case ChildStreamClosedMsg:
			// expected
		case SessionErrorMsg:
			t.Fatalf("WaitForEvent after runtime stop returned SessionErrorMsg; want ChildStreamClosedMsg (QUM-479)")
		default:
			t.Fatalf("WaitForEvent after runtime stop returned %T; want ChildStreamClosedMsg", msg)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("WaitForEvent did not return after runtime stop within deadline")
	}
}

// -- 5 & 6. AppModel must NOT trigger a bridge restart on closed sentinels --

func TestAppModel_ActivityStreamClosedMsg_NoRestart(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)

	sup := &activityRecordingSupervisor{reg: reg}
	app := newActivityApp(t, sup)

	// Install an activity adapter for "alice".
	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)
	epoch := app.ActivityAdapterEpoch()

	updated, cmd := app.Update(ActivityStreamClosedMsg{Agent: "alice", Epoch: epoch})
	_ = updated

	msgs := collectBatchMsgs(t, cmd)
	if hasMsgOfType[SessionRestartingMsg](msgs) {
		t.Errorf("ActivityStreamClosedMsg must NOT produce SessionRestartingMsg; got msgs=%v", msgs)
	}
	if hasMsgOfType[RestartSessionMsg](msgs) {
		t.Errorf("ActivityStreamClosedMsg must NOT produce RestartSessionMsg; got msgs=%v", msgs)
	}
}

func TestAppModel_ChildStreamClosedMsg_NoRestart(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "kid")
	registerUnified(t, reg, "kid", urt)

	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	// Install a child adapter for "kid".
	updated, _ := app.Update(AgentSelectedMsg{Name: "kid"})
	app = updated.(AppModel)
	epoch := app.ChildAdapterEpoch()

	updated, cmd := app.Update(ChildStreamClosedMsg{Agent: "kid", Epoch: epoch})
	_ = updated

	msgs := collectBatchMsgs(t, cmd)
	if hasMsgOfType[SessionRestartingMsg](msgs) {
		t.Errorf("ChildStreamClosedMsg must NOT produce SessionRestartingMsg; got msgs=%v", msgs)
	}
	if hasMsgOfType[RestartSessionMsg](msgs) {
		t.Errorf("ChildStreamClosedMsg must NOT produce RestartSessionMsg; got msgs=%v", msgs)
	}
}

// -- 7. AppModel tears down the adapter on a matching closed sentinel ------

func TestAppModel_ActivityStreamClosedMsg_TearsDownAdapter(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)

	sup := &activityRecordingSupervisor{reg: reg}
	app := newActivityApp(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)

	if app.ActivityAdapter() == nil {
		t.Fatal("precondition: ActivityAdapter should be installed after AgentSelectedMsg")
	}
	epoch := app.ActivityAdapterEpoch()

	updated, _ = app.Update(ActivityStreamClosedMsg{Agent: "alice", Epoch: epoch})
	app = updated.(AppModel)

	if app.ActivityAdapter() != nil {
		t.Errorf("ActivityStreamClosedMsg must clear ActivityAdapter; got %v", app.ActivityAdapter())
	}
	if got := app.ActivityAdapterAgent(); got != "" {
		t.Errorf("ActivityStreamClosedMsg must clear ActivityAdapterAgent; got %q", got)
	}
}

// -- 8. Stale-epoch closed sentinel must NOT tear down a current adapter ---

func TestAppModel_ActivityStreamClosedMsg_StaleEpochIgnored(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)

	sup := &activityRecordingSupervisor{reg: reg}
	app := newActivityApp(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)
	currentEpoch := app.ActivityAdapterEpoch()
	if currentEpoch == 0 {
		t.Fatal("precondition: epoch should be > 0 after AgentSelectedMsg")
	}

	// Deliver a stale-epoch closed sentinel (E-1).
	updated, _ = app.Update(ActivityStreamClosedMsg{Agent: "alice", Epoch: currentEpoch - 1})
	app = updated.(AppModel)

	if app.ActivityAdapter() == nil {
		t.Errorf("stale-epoch ActivityStreamClosedMsg must NOT tear down current adapter")
	}
	if got := app.ActivityAdapterAgent(); got != "alice" {
		t.Errorf("stale-epoch ActivityStreamClosedMsg must NOT clear ActivityAdapterAgent; got %q", got)
	}
	if got := app.ActivityAdapterEpoch(); got != currentEpoch {
		t.Errorf("stale-epoch ActivityStreamClosedMsg must not bump epoch; was %d, now %d", currentEpoch, got)
	}
}

// -- 9. AppModel tears down the CHILD adapter on a matching closed sentinel
//
// Mirror of TestAppModel_ActivityStreamClosedMsg_TearsDownAdapter for the
// child-stream adapter (QUM-479). After installing the child adapter via
// AgentSelectedMsg{Name: "kid"}, delivering ChildStreamClosedMsg with the
// matching epoch must clear m.childAdapter and m.childAdapterAgent.

func TestAppModel_ChildStreamClosedMsg_TearsDownAdapter(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "kid")
	registerUnified(t, reg, "kid", urt)

	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "kid"})
	app = updated.(AppModel)

	if app.ChildAdapter() == nil {
		t.Fatal("precondition: ChildAdapter should be installed after AgentSelectedMsg for non-root agent")
	}
	if got := app.ChildAdapterAgent(); got != "kid" {
		t.Fatalf("precondition: ChildAdapterAgent = %q, want kid", got)
	}
	epoch := app.ChildAdapterEpoch()

	updated, _ = app.Update(ChildStreamClosedMsg{Agent: "kid", Epoch: epoch})
	app = updated.(AppModel)

	if app.ChildAdapter() != nil {
		t.Errorf("ChildStreamClosedMsg must clear ChildAdapter; got %v", app.ChildAdapter())
	}
	if got := app.ChildAdapterAgent(); got != "" {
		t.Errorf("ChildStreamClosedMsg must clear ChildAdapterAgent; got %q", got)
	}
}

// -- 10. Stale-epoch ChildStreamClosedMsg must NOT tear down a current adapter

func TestAppModel_ChildStreamClosedMsg_StaleEpochIgnored(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "kid")
	registerUnified(t, reg, "kid", urt)

	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "kid"})
	app = updated.(AppModel)
	currentEpoch := app.ChildAdapterEpoch()
	if currentEpoch == 0 {
		t.Fatal("precondition: child epoch should be > 0 after AgentSelectedMsg")
	}
	if app.ChildAdapter() == nil {
		t.Fatal("precondition: ChildAdapter should be installed after AgentSelectedMsg")
	}

	updated, _ = app.Update(ChildStreamClosedMsg{Agent: "kid", Epoch: currentEpoch - 1})
	app = updated.(AppModel)

	if app.ChildAdapter() == nil {
		t.Errorf("stale-epoch ChildStreamClosedMsg must NOT tear down current adapter")
	}
	if got := app.ChildAdapterAgent(); got != "kid" {
		t.Errorf("stale-epoch ChildStreamClosedMsg must NOT clear ChildAdapterAgent; got %q", got)
	}
	if got := app.ChildAdapterEpoch(); got != currentEpoch {
		t.Errorf("stale-epoch ChildStreamClosedMsg must not bump epoch; was %d, now %d", currentEpoch, got)
	}
}

// -- 11. RestartCompleteMsg must rebind the activity adapter for root --------
//
// QUM-479: when the activity adapter was previously bound to the root agent
// and a session restart completes, the adapter must be re-pointed at the
// freshly-registered UnifiedRuntime for the root agent. Specifically:
//   - m.activityAdapter remains non-nil (re-Observed, not torn down).
//   - m.activityAdapterAgent stays == m.rootAgent.
//   - m.activityAdapterEpoch is strictly bumped (a new generation).
//   - A follow-up ActivityStreamMsg-arming wait cmd is scheduled in the batch.

func TestAppModel_RestartComplete_RebindsActivityAdapterForRoot(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app = resized.(AppModel)

	// Wire a supervisor with a runtime registry holding a unified runtime for
	// the root agent ("weave"). We bypass the normal AgentSelectedMsg path
	// (which expects supervisor.PeekActivity etc.) and seed the activity
	// adapter fields directly so this test focuses on RestartCompleteMsg.
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, app.rootAgent)
	registerUnified(t, reg, app.rootAgent, urt)
	app.supervisor = &activityRecordingSupervisor{reg: reg}

	// Pre-existing activity adapter pointed at root.
	app.activityAdapter = NewActivityStreamAdapter(urt)
	app.activityAdapterAgent = app.rootAgent
	app.activityAdapterEpoch = 7

	priorEpoch := app.activityAdapterEpoch
	priorAdapter := app.activityAdapter

	// Drive a successful RestartCompleteMsg with a fresh bridge.
	newBridge := newFakeSessionBackend()
	updated, cmd := app.Update(RestartCompleteMsg{Bridge: newBridge, Err: nil})
	app = updated.(AppModel)

	if app.activityAdapter == nil {
		t.Fatal("RestartCompleteMsg must NOT tear down the activity adapter for the root agent")
	}
	if app.activityAdapter != priorAdapter {
		// Implementation may either keep the same adapter (re-Observe) or
		// replace it; either is acceptable as long as the binding survives.
		// We don't fail here, but record the choice for clarity.
		t.Logf("note: RestartCompleteMsg replaced activityAdapter (was %p, now %p)", priorAdapter, app.activityAdapter)
	}
	if got := app.activityAdapterAgent; got != app.rootAgent {
		t.Errorf("activityAdapterAgent after RestartCompleteMsg = %q, want %q", got, app.rootAgent)
	}
	if app.activityAdapterEpoch <= priorEpoch {
		t.Errorf("activityAdapterEpoch must strictly bump on root-rebind; was %d, now %d", priorEpoch, app.activityAdapterEpoch)
	}

	// A follow-up wait cmd must be scheduled. We expect *some* non-nil cmd
	// in the batch (Initialize is also there). Confirm a wait-style cmd was
	// armed by checking that the batch produces at least one cmd whose
	// invocation does not immediately yield a SessionRestartingMsg /
	// RestartSessionMsg (i.e. it is the activity-stream wait).
	if cmd == nil {
		t.Fatal("RestartCompleteMsg returned nil cmd; expected bridge.Initialize + activity wait")
	}
	msgs := collectBatchMsgs(t, cmd)
	if hasMsgOfType[SessionRestartingMsg](msgs) {
		t.Errorf("RestartCompleteMsg rebind must not emit SessionRestartingMsg; got %v", msgs)
	}
	if hasMsgOfType[RestartSessionMsg](msgs) {
		t.Errorf("RestartCompleteMsg rebind must not emit RestartSessionMsg; got %v", msgs)
	}
}

// -- 12. RestartCompleteMsg must NOT touch the activity adapter when
//        observing a non-root child agent -----------------------------------

func TestAppModel_RestartComplete_DoesNotRebindWhenObservingChild(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app = resized.(AppModel)

	reg := supervisor.NewRuntimeRegistry()
	urtRoot := newUnifiedRT(t, app.rootAgent)
	registerUnified(t, reg, app.rootAgent, urtRoot)
	urtChild := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urtChild)
	app.supervisor = &activityRecordingSupervisor{reg: reg}

	// Activity adapter is bound to a non-root child.
	childAdapter := NewActivityStreamAdapter(urtChild)
	app.activityAdapter = childAdapter
	app.activityAdapterAgent = "alice"
	app.activityAdapterEpoch = 11

	priorEpoch := app.activityAdapterEpoch

	newBridge := newFakeSessionBackend()
	updated, _ := app.Update(RestartCompleteMsg{Bridge: newBridge, Err: nil})
	app = updated.(AppModel)

	if app.activityAdapter != childAdapter {
		t.Errorf("RestartCompleteMsg must not replace activityAdapter when observing child; was %p, now %p", childAdapter, app.activityAdapter)
	}
	if got := app.activityAdapterAgent; got != "alice" {
		t.Errorf("activityAdapterAgent must remain %q on child observation; got %q", "alice", got)
	}
	if app.activityAdapterEpoch != priorEpoch {
		t.Errorf("activityAdapterEpoch must NOT bump when activity is bound to child; was %d, now %d", priorEpoch, app.activityAdapterEpoch)
	}
}
