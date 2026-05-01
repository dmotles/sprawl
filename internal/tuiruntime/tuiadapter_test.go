// Tests for TUIAdapter (QUM-397, repackaged in QUM-431). The adapter wraps
// a UnifiedRuntime and exposes its lifecycle/event stream as
// bubbletea-friendly tea.Cmd values.
//
// These tests construct a real EventBus + MessageQueue + TurnLoop +
// UnifiedRuntime; only the underlying SessionHandle is mocked.

package tuiruntime

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
	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
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
func buildAdapter(t *testing.T, mock sprawlrt.SessionHandle) (*sprawlrt.UnifiedRuntime, *TUIAdapter) {
	t.Helper()
	rt := sprawlrt.New(sprawlrt.RuntimeConfig{
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
	rt.Queue().Enqueue(sprawlrt.QueueItem{Class: sprawlrt.ClassUser, Prompt: "hi"})
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

	rt.Queue().Enqueue(sprawlrt.QueueItem{Class: sprawlrt.ClassUser, Prompt: "go"})
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

	rt.Queue().Enqueue(sprawlrt.QueueItem{Class: sprawlrt.ClassUser, Prompt: "go"})
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

	rt.Queue().Enqueue(sprawlrt.QueueItem{Class: sprawlrt.ClassUser, Prompt: "go"})
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

	rt.Queue().Enqueue(sprawlrt.QueueItem{Class: sprawlrt.ClassUser, Prompt: "go"})

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

	rt.Queue().Enqueue(sprawlrt.QueueItem{Class: sprawlrt.ClassUser, Prompt: "long"})

	// Wait for runtime to enter TurnActive before interrupting.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && rt.State() != sprawlrt.StateTurnActive {
		time.Sleep(5 * time.Millisecond)
	}
	if rt.State() != sprawlrt.StateTurnActive {
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

	rt.Queue().Enqueue(sprawlrt.QueueItem{Class: sprawlrt.ClassUser, Prompt: "go"})
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
	if items[0].Class != sprawlrt.ClassUser {
		t.Errorf("queued item class = %q, want %q", items[0].Class, sprawlrt.ClassUser)
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

	rt.Queue().Enqueue(sprawlrt.QueueItem{Class: sprawlrt.ClassUser, Prompt: "long"})

	// Wait for an in-flight turn before triggering Interrupt.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && rt.State() != sprawlrt.StateTurnActive {
		time.Sleep(5 * time.Millisecond)
	}
	if rt.State() != sprawlrt.StateTurnActive {
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
	rtA := sprawlrt.New(sprawlrt.RuntimeConfig{Name: "a", Session: mockA})
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
	rtB := sprawlrt.New(sprawlrt.RuntimeConfig{Name: "b", Session: mockB})
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
	rtA.Queue().Enqueue(sprawlrt.QueueItem{Class: sprawlrt.ClassUser, Prompt: "from-A"})
	chA <- makeAssistantMsg(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"alpha"}]}}`)

	// Publish on B; the adapter should see "beta".
	rtB.Queue().Enqueue(sprawlrt.QueueItem{Class: sprawlrt.ClassUser, Prompt: "from-B"})
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

// drainAdapter pulls msgs from a.WaitForEvent until it sees a SessionErrorMsg
// (typically io.EOF after the bus closes) or hits the deadline. Returns all
// observed msgs in order.
func drainAdapter(t *testing.T, a *TUIAdapter, deadline time.Duration) []tea.Msg {
	t.Helper()
	var out []tea.Msg
	stop := time.After(deadline)
	for {
		select {
		case <-stop:
			return out
		default:
		}
		type res struct{ msg tea.Msg }
		ch := make(chan res, 1)
		cmd := a.WaitForEvent()
		go func() { ch <- res{msg: cmd()} }()
		select {
		case r := <-ch:
			out = append(out, r.msg)
			if errMsg, ok := r.msg.(tui.SessionErrorMsg); ok {
				if errors.Is(errMsg.Err, io.EOF) {
					return out
				}
			}
		case <-stop:
			return out
		}
	}
}

// QUM-436 Item 1: exactly one SessionResultMsg per turn (the lifecycle one
// from EventTurnCompleted, not the protocol-result message). Today the adapter
// emits TWO: one from the protocol "result" mapping and another from
// EventTurnCompleted. The DurationMs assertion guards against a regression
// where only the protocol-mapped (zero-valued) SessionResultMsg is emitted.

func TestTUIAdapter_WaitForEvent_TurnCompleted_EmitsExactlyOneSessionResultMsg(t *testing.T) {
	ch := make(chan *protocol.Message, 4)
	mock := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) { return ch, nil },
	}
	rt, a := buildAdapter(t, mock)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rt.Queue().Enqueue(sprawlrt.QueueItem{Class: sprawlrt.ClassUser, Prompt: "go"})
	ch <- makeAssistantMsg(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`)
	ch <- makeAssistantMsg(t, `{"type":"result","subtype":"success","is_error":false,"result":"done","duration_ms":42,"num_turns":3,"total_cost_usd":0.01}`)
	close(ch)

	msgs := drainAdapter(t, a, 1*time.Second)

	var results []tui.SessionResultMsg
	for _, m := range msgs {
		if sr, ok := m.(tui.SessionResultMsg); ok {
			results = append(results, sr)
		}
	}
	if len(results) != 1 {
		t.Fatalf("got %d SessionResultMsg, want exactly 1; msgs=%+v", len(results), msgs)
	}
	sr := results[0]
	if sr.IsError {
		t.Errorf("IsError=true, want false")
	}
	if sr.DurationMs != 42 {
		t.Errorf("DurationMs=%d, want 42 (proves it's the lifecycle-driven SessionResultMsg, not zero-valued protocol path)", sr.DurationMs)
	}
	if sr.NumTurns != 3 {
		t.Errorf("NumTurns=%d, want 3", sr.NumTurns)
	}
}

func TestTUIAdapter_WaitForEvent_TurnFailed_EmitsExactlyOneSessionResultMsg(t *testing.T) {
	// Drive both events through the EventBus directly so we can sequence a
	// protocol "result" (is_error:true) ahead of EventTurnFailed. This
	// actually exercises Item 1's dedupe on the failure branch — without the
	// preceding protocol-result message, the adapter wouldn't have a duplicate
	// to suppress. The naive (pre-dedupe) implementation would emit two
	// SessionResultMsg here; the fix collapses them to one.
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	resultRaw := `{"type":"result","subtype":"error","is_error":true,"result":"boom","duration_ms":7,"num_turns":1}`
	resultMsg := makeAssistantMsg(t, resultRaw)
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventProtocolMessage, Message: resultMsg})

	var pr protocol.ResultMessage
	if err := protocol.ParseAs(resultMsg, &pr); err != nil {
		t.Fatalf("ParseAs: %v", err)
	}
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventTurnFailed, Error: errors.New("boom"), Result: &pr})

	msgs := drainAdapter(t, a, 1*time.Second)

	var results []tui.SessionResultMsg
	for _, m := range msgs {
		if sr, ok := m.(tui.SessionResultMsg); ok {
			results = append(results, sr)
		}
	}
	if len(results) != 1 {
		t.Fatalf("got %d SessionResultMsg, want exactly 1 (dedupe must apply on the failure branch too); msgs=%+v", len(results), msgs)
	}
	if !results[0].IsError {
		t.Errorf("IsError=false, want true")
	}
}

// QUM-436 Item 2: Observe(rtB) must NOT cause WaitForEvent (parked on the old
// channel) to return a spurious io.EOF. The parked goroutine should resubscribe
// to rtB transparently and surface the next real event.

func TestTUIAdapter_Observe_DoesNotEmitSpuriousEOF(t *testing.T) {
	chA := make(chan *protocol.Message, 4)
	mockA := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) { return chA, nil },
	}
	rtA := sprawlrt.New(sprawlrt.RuntimeConfig{Name: "a", Session: mockA})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rtA.Stop(ctx)
	})
	if err := rtA.Start(context.Background()); err != nil {
		t.Fatalf("Start A: %v", err)
	}
	a := NewTUIAdapter(rtA)

	// Park a goroutine on a.WaitForEvent before swapping to rtB.
	type res struct{ msg tea.Msg }
	out := make(chan res, 1)
	cmd := a.WaitForEvent()
	go func() { out <- res{msg: cmd()} }()

	// Brief sleep so the goroutine is parked on <-ch inside WaitForEvent.
	// (Event-based sync isn't available since the adapter doesn't expose its
	// internal subscribe state.) 50ms gives slow CI runners headroom without
	// meaningfully slowing the local suite.
	time.Sleep(50 * time.Millisecond)

	chB := make(chan *protocol.Message, 4)
	mockB := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) { return chB, nil },
	}
	rtB := sprawlrt.New(sprawlrt.RuntimeConfig{Name: "b", Session: mockB})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rtB.Stop(ctx)
	})
	if err := rtB.Start(context.Background()); err != nil {
		t.Fatalf("Start B: %v", err)
	}

	a.Observe(rtB)

	// Drive a real event on rtB.
	rtB.Queue().Enqueue(sprawlrt.QueueItem{Class: sprawlrt.ClassUser, Prompt: "from-B"})
	chB <- makeAssistantMsg(t, `{"type":"assistant","uuid":"b-1","message":{"role":"assistant","content":[{"type":"text","text":"beta"}]}}`)

	// The parked goroutine OR a follow-up call must surface the rtB event,
	// never a spurious SessionErrorMsg{io.EOF}.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case r := <-out:
			if errMsg, ok := r.msg.(tui.SessionErrorMsg); ok && errors.Is(errMsg.Err, io.EOF) {
				t.Fatalf("Observe() caused a spurious SessionErrorMsg{io.EOF}; want resubscribe + real event")
			}
			// Anything non-EOF: either the assistant content msg directly, or
			// some other valid event. If we already saw the right event, we're
			// done. Otherwise pull again.
			if acm, ok := r.msg.(tui.AssistantContentMsg); ok {
				for _, inner := range acm.Msgs {
					if tx, ok := inner.(tui.AssistantTextMsg); ok && tx.Text == "beta" {
						return
					}
				}
			}
			// Re-arm.
			cmd2 := a.WaitForEvent()
			go func() { out <- res{msg: cmd2()} }()
		case <-deadline:
			t.Fatal("did not see rtB event after Observe; deadline exceeded")
		}
	}
}

// Invariant/regression guard: Observe(rtB) after an explicit Cancel() must
// reset the cancelled flag and produce a fresh, working subscription. This
// passes against current code; it's pinned here so a future refactor that
// "remembers" cancellation across Observe() can't silently regress.
func TestTUIAdapter_Observe_AfterCancel_ResubscribesCleanly(t *testing.T) {
	chA := make(chan *protocol.Message, 4)
	mockA := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) { return chA, nil },
	}
	rtA := sprawlrt.New(sprawlrt.RuntimeConfig{Name: "a", Session: mockA})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rtA.Stop(ctx)
	})
	if err := rtA.Start(context.Background()); err != nil {
		t.Fatalf("Start A: %v", err)
	}
	a := NewTUIAdapter(rtA)

	a.Cancel()

	chB := make(chan *protocol.Message, 4)
	mockB := &adapterMockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) { return chB, nil },
	}
	rtB := sprawlrt.New(sprawlrt.RuntimeConfig{Name: "b", Session: mockB})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rtB.Stop(ctx)
	})
	if err := rtB.Start(context.Background()); err != nil {
		t.Fatalf("Start B: %v", err)
	}

	a.Observe(rtB)

	// Drive a real event on rtB; WaitForEvent must surface it (not EOF).
	rtB.Queue().Enqueue(sprawlrt.QueueItem{Class: sprawlrt.ClassUser, Prompt: "from-B"})
	chB <- makeAssistantMsg(t, `{"type":"assistant","uuid":"b-1","message":{"role":"assistant","content":[{"type":"text","text":"beta"}]}}`)

	deadline := time.After(2 * time.Second)
	for {
		type res struct{ msg tea.Msg }
		ch := make(chan res, 1)
		cmd := a.WaitForEvent()
		go func() { ch <- res{msg: cmd()} }()
		select {
		case r := <-ch:
			if errMsg, ok := r.msg.(tui.SessionErrorMsg); ok && errors.Is(errMsg.Err, io.EOF) {
				t.Fatalf("Observe(rtB) after Cancel still returns io.EOF; expected fresh subscription")
			}
			if acm, ok := r.msg.(tui.AssistantContentMsg); ok {
				for _, inner := range acm.Msgs {
					if tx, ok := inner.(tui.AssistantTextMsg); ok && tx.Text == "beta" {
						return
					}
				}
			}
		case <-deadline:
			t.Fatal("did not see rtB event after Cancel+Observe within 2s")
		}
	}
}

// QUM-436 Item 4: nil-runtime guards. Initialize/SendMessage/Interrupt called
// after Observe(nil) must return a SessionErrorMsg / InterruptResultMsg with
// the ErrNoRuntime sentinel — not panic on a nil receiver.

func TestTUIAdapter_Initialize_NilRuntime_ReturnsSessionError(t *testing.T) {
	mock := &adapterMockSession{}
	_, a := buildAdapter(t, mock)
	a.Observe(nil)

	msg := runCmd(t, a.Initialize())
	errMsg, ok := msg.(tui.SessionErrorMsg)
	if !ok {
		t.Fatalf("Initialize() with nil runtime = %T, want tui.SessionErrorMsg", msg)
	}
	if !errors.Is(errMsg.Err, ErrNoRuntime) {
		t.Errorf("Err = %v, want errors.Is(_, ErrNoRuntime)=true", errMsg.Err)
	}
}

func TestTUIAdapter_SendMessage_NilRuntime_ReturnsSessionError(t *testing.T) {
	mock := &adapterMockSession{}
	_, a := buildAdapter(t, mock)
	a.Observe(nil)

	msg := runCmd(t, a.SendMessage("hi"))
	errMsg, ok := msg.(tui.SessionErrorMsg)
	if !ok {
		t.Fatalf("SendMessage() with nil runtime = %T, want tui.SessionErrorMsg", msg)
	}
	if !errors.Is(errMsg.Err, ErrNoRuntime) {
		t.Errorf("Err = %v, want errors.Is(_, ErrNoRuntime)=true", errMsg.Err)
	}
}

func TestTUIAdapter_Interrupt_NilRuntime_ReturnsInterruptResultErr(t *testing.T) {
	mock := &adapterMockSession{}
	_, a := buildAdapter(t, mock)
	a.Observe(nil)

	msg := runCmd(t, a.Interrupt())
	ir, ok := msg.(tui.InterruptResultMsg)
	if !ok {
		t.Fatalf("Interrupt() with nil runtime = %T, want tui.InterruptResultMsg", msg)
	}
	if !errors.Is(ir.Err, ErrNoRuntime) {
		t.Errorf("Err = %v, want errors.Is(_, ErrNoRuntime)=true", ir.Err)
	}
}

// Invariant/regression guard: SessionID() must tolerate a nil runtime and
// return "" rather than panic. Passes against current code (the nil check is
// already in place); pinned here so a future refactor that drops the guard
// is caught by tests.
func TestTUIAdapter_SessionID_NilRuntime(t *testing.T) {
	mock := &adapterMockSession{}
	_, a := buildAdapter(t, mock)
	a.Observe(nil)

	if got := a.SessionID(); got != "" {
		t.Errorf("SessionID() with nil runtime = %q, want empty string", got)
	}
}
