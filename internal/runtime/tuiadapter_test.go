// Tests for TUIAdapter (QUM-397). The adapter wraps a UnifiedRuntime and
// exposes its lifecycle/event stream as bubbletea-friendly tea.Cmd values.
//
// These tests construct a real EventBus + MessageQueue + TurnLoop +
// UnifiedRuntime; only the underlying SessionHandle is mocked. They will
// not compile until tuiadapter.go is added (red phase of TDD).

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	tui "github.com/dmotles/sprawl/internal/tui"
)

// adapterMockSession is a SessionHandle test double that lets each test
// hand-control the channel returned from StartTurn so we can drive specific
// runtime events on demand. Independent of the package's other mocks so it
// can evolve separately.
type adapterMockSession struct {
	mu             sync.Mutex
	starts         []string
	onStart        func(call int) (<-chan *protocol.Message, error)
	interruptCalls int32
	interruptErr   error
}

func (m *adapterMockSession) StartTurn(_ context.Context, prompt string, _ ...backend.TurnSpec) (<-chan *protocol.Message, error) {
	m.mu.Lock()
	m.starts = append(m.starts, prompt)
	call := len(m.starts) - 1
	cb := m.onStart
	m.mu.Unlock()
	if cb != nil {
		return cb(call)
	}
	ch := make(chan *protocol.Message)
	close(ch)
	return ch, nil
}

func (m *adapterMockSession) Interrupt(_ context.Context) error {
	atomic.AddInt32(&m.interruptCalls, 1)
	return m.interruptErr
}

func (m *adapterMockSession) interruptCount() int {
	return int(atomic.LoadInt32(&m.interruptCalls))
}

// adapterMockSessionWithID adds a SessionID() method to test the
// sessionIDProvider type-assertion path.
type adapterMockSessionWithID struct {
	adapterMockSession
	id string
}

func (m *adapterMockSessionWithID) SessionID() string { return m.id }

// runCmd executes a tea.Cmd with a bounded timeout so a hang doesn't wedge
// the suite.
func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("tea.Cmd is nil")
	}
	type res struct{ msg tea.Msg }
	out := make(chan res, 1)
	go func() { out <- res{msg: cmd()} }()
	select {
	case r := <-out:
		return r.msg
	case <-time.After(2 * time.Second):
		t.Fatal("tea.Cmd did not return within 2s")
		return nil
	}
}

// buildAdapter spins up a real UnifiedRuntime around the supplied mock
// session and returns the runtime + adapter. t.Cleanup is registered to
// stop the runtime.
func buildAdapter(t *testing.T, mock SessionHandle) (*UnifiedRuntime, *TUIAdapter) {
	t.Helper()
	rt := New(RuntimeConfig{
		Name:    "tui-adapter-test",
		Session: mock,
	})
	a := NewTUIAdapter(rt)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	return rt, a
}

func makeAssistantMsg(t *testing.T, raw string) *protocol.Message {
	t.Helper()
	var m protocol.Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	m.Raw = json.RawMessage(raw)
	return &m
}

func TestTUIAdapter_Initialize_Success(t *testing.T) {
	mock := &adapterMockSession{}
	_, a := buildAdapter(t, mock)

	msg := runCmd(t, a.Initialize())
	if _, ok := msg.(tui.SessionInitializedMsg); !ok {
		t.Fatalf("Initialize() returned %T, want tui.SessionInitializedMsg", msg)
	}
}

func TestTUIAdapter_Initialize_AlreadyStarted(t *testing.T) {
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	msg := runCmd(t, a.Initialize())
	errMsg, ok := msg.(tui.SessionErrorMsg)
	if !ok {
		t.Fatalf("Initialize() on already-started runtime returned %T, want tui.SessionErrorMsg", msg)
	}
	if errMsg.Err == nil {
		t.Errorf("SessionErrorMsg.Err is nil")
	}
}

func TestTUIAdapter_WaitForEvent_AssistantText(t *testing.T) {
	ch := make(chan *protocol.Message, 4)
	mock := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			return ch, nil
		},
	}
	rt, a := buildAdapter(t, mock)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Drive a turn so the adapter sees an EventProtocolMessage.
	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "hi"})
	ch <- makeAssistantMsg(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"Hello world"}]}}`)

	// First WaitForEvent should produce the AssistantContentMsg (lifecycle
	// events are skipped per spec).
	msg := runCmd(t, a.WaitForEvent())
	acm, ok := msg.(tui.AssistantContentMsg)
	if !ok {
		t.Fatalf("WaitForEvent() = %T, want tui.AssistantContentMsg", msg)
	}
	if len(acm.Msgs) == 0 {
		t.Fatalf("AssistantContentMsg has no inner msgs")
	}
	textMsg, ok := acm.Msgs[0].(tui.AssistantTextMsg)
	if !ok {
		t.Fatalf("Msgs[0] = %T, want tui.AssistantTextMsg", acm.Msgs[0])
	}
	if textMsg.Text != "Hello world" {
		t.Errorf("Text = %q, want %q", textMsg.Text, "Hello world")
	}
}

func TestTUIAdapter_WaitForEvent_ToolCall(t *testing.T) {
	ch := make(chan *protocol.Message, 4)
	mock := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			return ch, nil
		},
	}
	rt, a := buildAdapter(t, mock)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "go"})
	ch <- makeAssistantMsg(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"ls"}}]}}`)

	msg := runCmd(t, a.WaitForEvent())
	acm, ok := msg.(tui.AssistantContentMsg)
	if !ok {
		t.Fatalf("WaitForEvent() = %T, want tui.AssistantContentMsg", msg)
	}
	if len(acm.Msgs) == 0 {
		t.Fatalf("AssistantContentMsg has no inner msgs")
	}
	tc, ok := acm.Msgs[0].(tui.ToolCallMsg)
	if !ok {
		t.Fatalf("Msgs[0] = %T, want tui.ToolCallMsg", acm.Msgs[0])
	}
	if tc.ToolName != "Bash" || tc.ToolID != "tool-1" {
		t.Errorf("ToolCallMsg = {Name:%q, ID:%q}, want {Bash, tool-1}", tc.ToolName, tc.ToolID)
	}
}

func TestTUIAdapter_WaitForEvent_ToolResult(t *testing.T) {
	ch := make(chan *protocol.Message, 4)
	mock := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			return ch, nil
		},
	}
	rt, a := buildAdapter(t, mock)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "go"})
	// user protocol message carrying a tool_result block
	ch <- makeAssistantMsg(t, `{"type":"user","uuid":"u-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"output","is_error":false}]}}`)

	msg := runCmd(t, a.WaitForEvent())
	tr, ok := msg.(tui.ToolResultMsg)
	if !ok {
		t.Fatalf("WaitForEvent() = %T, want tui.ToolResultMsg", msg)
	}
	if tr.ToolID != "tool-1" {
		t.Errorf("ToolID = %q, want %q", tr.ToolID, "tool-1")
	}
	if tr.Content != "output" {
		t.Errorf("Content = %q, want %q", tr.Content, "output")
	}
}

func TestTUIAdapter_WaitForEvent_TurnCompleted_SessionResult(t *testing.T) {
	ch := make(chan *protocol.Message, 4)
	mock := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			return ch, nil
		},
	}
	rt, a := buildAdapter(t, mock)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "go"})
	// Result message - turnLoop will emit EventProtocolMessage(result) +
	// EventTurnCompleted. The adapter should surface the SessionResultMsg
	// from EventTurnCompleted.
	resultRaw := `{"type":"result","subtype":"success","is_error":false,"result":"done","duration_ms":42,"num_turns":1,"total_cost_usd":0.01}`
	ch <- makeAssistantMsg(t, resultRaw)
	close(ch)

	// Loop until we see SessionResultMsg (we may see the protocol result
	// message first, which also maps to SessionResultMsg). Either is fine.
	deadline := time.After(3 * time.Second)
	for {
		msg := runCmd(t, a.WaitForEvent())
		if sr, ok := msg.(tui.SessionResultMsg); ok {
			if sr.IsError {
				t.Errorf("IsError = true, want false")
			}
			if sr.DurationMs != 42 {
				t.Errorf("DurationMs = %d, want 42", sr.DurationMs)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("did not see SessionResultMsg; last msg=%T", msg)
		default:
		}
	}
}

func TestTUIAdapter_WaitForEvent_TurnFailed_SessionResultError(t *testing.T) {
	mock := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			return nil, errors.New("boom")
		},
	}
	rt, a := buildAdapter(t, mock)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "go"})

	deadline := time.After(3 * time.Second)
	for {
		msg := runCmd(t, a.WaitForEvent())
		if sr, ok := msg.(tui.SessionResultMsg); ok {
			if !sr.IsError {
				t.Errorf("IsError = false, want true")
			}
			if sr.Result != "boom" {
				t.Errorf("Result = %q, want %q", sr.Result, "boom")
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("did not see SessionResultMsg{IsError:true}; last=%T", msg)
		default:
		}
	}
}

func TestTUIAdapter_WaitForEvent_Interrupted_InterruptResultMsg(t *testing.T) {
	ch := make(chan *protocol.Message, 4)
	mock := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			return ch, nil
		},
	}
	rt, a := buildAdapter(t, mock)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "long"})

	// Wait for runtime to enter TurnActive before interrupting.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && rt.State() != StateTurnActive {
		time.Sleep(5 * time.Millisecond)
	}
	if rt.State() != StateTurnActive {
		t.Fatalf("did not enter StateTurnActive; got %v", rt.State())
	}

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// Drain the turn so EventInterrupted fires.
	resultRaw := `{"type":"result","subtype":"interrupted","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0}`
	ch <- makeAssistantMsg(t, resultRaw)
	close(ch)

	deadline2 := time.After(3 * time.Second)
	for {
		msg := runCmd(t, a.WaitForEvent())
		if ir, ok := msg.(tui.InterruptResultMsg); ok {
			if ir.Err != nil {
				t.Errorf("InterruptResultMsg.Err = %v, want nil", ir.Err)
			}
			return
		}
		select {
		case <-deadline2:
			t.Fatalf("did not see InterruptResultMsg; last=%T", msg)
		default:
		}
	}
}

func TestTUIAdapter_WaitForEvent_SkipsLifecycleEvents(t *testing.T) {
	// Ensure WaitForEvent does not surface EventTurnStarted / EventQueueDrained
	// / EventStopped as TUI messages — it must loop past them. We drive a
	// successful turn and verify the only msgs we observe are
	// AssistantContentMsg + SessionResultMsg.
	ch := make(chan *protocol.Message, 4)
	mock := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			return ch, nil
		},
	}
	rt, a := buildAdapter(t, mock)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "go"})
	ch <- makeAssistantMsg(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`)
	ch <- makeAssistantMsg(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":1,"num_turns":1,"total_cost_usd":0}`)
	close(ch)

	// Pull msgs and assert none of them are zero-valued / nil. None of
	// SessionInitializedMsg / UserMessageSentMsg should appear (those are
	// adapter-synthesized in Initialize/SendMessage).
	saw := make(map[string]int)
	deadline := time.After(3 * time.Second)
	done := false
	for !done {
		msg := runCmd(t, a.WaitForEvent())
		switch m := msg.(type) {
		case tui.AssistantContentMsg:
			saw["assistant"]++
		case tui.SessionResultMsg:
			saw["result"]++
			done = true
		case tui.SessionErrorMsg:
			// EOF after channel close — acceptable terminator.
			if !errors.Is(m.Err, io.EOF) {
				t.Fatalf("unexpected SessionErrorMsg: %v", m.Err)
			}
			done = true
		default:
			t.Fatalf("unexpected lifecycle leak: %T", msg)
		}
		select {
		case <-deadline:
			t.Fatalf("test did not finish in 3s; saw=%v", saw)
		default:
		}
	}
	if saw["assistant"] == 0 {
		t.Errorf("never saw AssistantContentMsg")
	}
}

func TestTUIAdapter_SendMessage_EnqueuesUserClass(t *testing.T) {
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)
	// Note: NOT starting the runtime so the queue isn't drained — we want
	// to inspect what got enqueued.

	msg := runCmd(t, a.SendMessage("hello"))
	if _, ok := msg.(tui.UserMessageSentMsg); !ok {
		t.Fatalf("SendMessage() = %T, want tui.UserMessageSentMsg", msg)
	}

	q := rt.Queue()
	items := q.DrainAll()
	if len(items) != 1 {
		t.Fatalf("queue depth = %d, want 1", len(items))
	}
	if items[0].Class != ClassUser {
		t.Errorf("queued item class = %q, want %q", items[0].Class, ClassUser)
	}
	if items[0].Prompt != "hello" {
		t.Errorf("queued item prompt = %q, want %q", items[0].Prompt, "hello")
	}
}

func TestTUIAdapter_Interrupt_ForwardsToRuntime(t *testing.T) {
	turnCh := make(chan *protocol.Message, 4)
	mock := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			return turnCh, nil
		},
	}
	rt, a := buildAdapter(t, mock)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "long"})

	// Wait for an in-flight turn before triggering Interrupt.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && rt.State() != StateTurnActive {
		time.Sleep(5 * time.Millisecond)
	}
	if rt.State() != StateTurnActive {
		t.Fatalf("not StateTurnActive; got %v", rt.State())
	}

	msg := runCmd(t, a.Interrupt())
	ir, ok := msg.(tui.InterruptResultMsg)
	if !ok {
		t.Fatalf("Interrupt() = %T, want tui.InterruptResultMsg", msg)
	}
	if ir.Err != nil {
		t.Errorf("InterruptResultMsg.Err = %v, want nil", ir.Err)
	}

	deadline2 := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline2) && mock.interruptCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if mock.interruptCount() == 0 {
		t.Errorf("Session.Interrupt was not invoked")
	}

	// Let the turn drain so cleanup can stop.
	turnCh <- makeAssistantMsg(t, `{"type":"result","subtype":"interrupted","is_error":false,"duration_ms":1,"num_turns":1,"total_cost_usd":0}`)
	close(turnCh)
}

func TestTUIAdapter_Observe_SwapsSubscription(t *testing.T) {
	chA := make(chan *protocol.Message, 4)
	mockA := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) { return chA, nil },
	}
	rtA := New(RuntimeConfig{Name: "a", Session: mockA})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rtA.Stop(ctx)
	})
	if err := rtA.Start(context.Background()); err != nil {
		t.Fatalf("Start A: %v", err)
	}

	a := NewTUIAdapter(rtA)

	chB := make(chan *protocol.Message, 4)
	mockB := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) { return chB, nil },
	}
	rtB := New(RuntimeConfig{Name: "b", Session: mockB})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rtB.Stop(ctx)
	})
	if err := rtB.Start(context.Background()); err != nil {
		t.Fatalf("Start B: %v", err)
	}

	// Swap the adapter's observed runtime to B.
	a.Observe(rtB)

	// Publish on A; the adapter should NOT see it.
	rtA.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "from-A"})
	chA <- makeAssistantMsg(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"alpha"}]}}`)

	// Publish on B; the adapter should see "beta".
	rtB.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "from-B"})
	chB <- makeAssistantMsg(t, `{"type":"assistant","uuid":"b-1","message":{"role":"assistant","content":[{"type":"text","text":"beta"}]}}`)

	// Drain msgs from the adapter; expect to see "beta" but never "alpha".
	deadline := time.After(3 * time.Second)
	sawBeta := false
	for !sawBeta {
		msg := runCmd(t, a.WaitForEvent())
		if acm, ok := msg.(tui.AssistantContentMsg); ok {
			for _, inner := range acm.Msgs {
				if tx, ok := inner.(tui.AssistantTextMsg); ok {
					if tx.Text == "alpha" {
						t.Fatalf("adapter received event from old runtime A after Observe(B)")
					}
					if tx.Text == "beta" {
						sawBeta = true
					}
				}
			}
		}
		select {
		case <-deadline:
			t.Fatal("did not see beta msg from rtB after Observe")
		default:
		}
	}
}

func TestTUIAdapter_Cancel_StopsReceiving(t *testing.T) {
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	a.Cancel()

	msg := runCmd(t, a.WaitForEvent())
	errMsg, ok := msg.(tui.SessionErrorMsg)
	if !ok {
		t.Fatalf("WaitForEvent() after Cancel = %T, want tui.SessionErrorMsg", msg)
	}
	if !errors.Is(errMsg.Err, io.EOF) {
		t.Errorf("err = %v, want io.EOF", errMsg.Err)
	}

	// Second Cancel must be a no-op.
	a.Cancel()
}

func TestTUIAdapter_SessionID(t *testing.T) {
	mock := &adapterMockSessionWithID{id: "session-xyz"}
	_, a := buildAdapter(t, mock)
	if got := a.SessionID(); got != "session-xyz" {
		t.Errorf("SessionID() = %q, want %q", got, "session-xyz")
	}
}
