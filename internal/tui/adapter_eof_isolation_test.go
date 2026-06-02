// Tests for QUM-479: dedicated tea.Msg sentinels for stream-adapter EOF.
//
// AppModel's SessionErrorMsg{io.EOF} handler is the bridge-restart path.
// ChildStreamAdapter previously emitted SessionErrorMsg{io.EOF} on cancel/
// runtime stop, mis-routing harmless adapter-teardown signals into a phantom
// "Session restarting..." banner.
//
// The fix introduces a dedicated sentinel type:
//   - ChildStreamClosedMsg{Agent, Epoch}
//
// QUM-648 removed the activity panel and its adapter; the activity-half of
// these tests was deleted along with internal/tui/activity_stream.go.

package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// -- Cancel emits the dedicated closed sentinel (not SessionErrorMsg) -

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

// -- Runtime stop (EventBus shutdown) emits the closed sentinel ------
//
// Stopping the UnifiedRuntime closes its EventBus subscriber chans. The
// adapter must surface that as the dedicated closed sentinel — not as
// SessionErrorMsg{io.EOF}, which would mis-route into the bridge-restart
// path.

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

// -- AppModel must NOT trigger a bridge restart on closed sentinels --

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

// -- AppModel tears down the CHILD adapter on a matching closed sentinel
//
// Mirror of QUM-479 for the child-stream adapter. After installing the child
// adapter via AgentSelectedMsg{Name: "kid"}, delivering ChildStreamClosedMsg
// with the matching epoch must clear m.childAdapter and m.childAdapterAgent.

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

// -- Stale-epoch ChildStreamClosedMsg must NOT tear down a current adapter

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
