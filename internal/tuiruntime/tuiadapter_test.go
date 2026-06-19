// Tests for TUIAdapter (QUM-397, repackaged in QUM-431). The adapter wraps
// a UnifiedRuntime and exposes its lifecycle/event stream as
// bubbletea-friendly tea.Cmd values.
//
// These tests construct a real EventBus + UnifiedRuntime; only the underlying
// SessionHandle is mocked. QUM-817: the Go MessageQueue/TurnLoop are deleted,
// so turns are driven by publishing RuntimeEvents directly onto the runtime's
// EventBus and inbound prompts are asserted against the session's
// WriteUserMessage recorder.

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

	"github.com/dmotles/sprawl/internal/protocol"
	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
	tui "github.com/dmotles/sprawl/internal/tui"
)

// adapterMockSession is a SessionHandle test double (QUM-817). It records the
// user messages the runtime writes to the CLI stdin (replacing the old
// StartTurn drive path) so tests can assert prompt content + priority.
// Independent of the package's other mocks so it can evolve separately.
type adapterMockSession struct {
	mu             sync.Mutex
	writes         []protocol.UserMessage
	interruptCalls int32
	interruptErr   error
	cancelResults  map[string]bool
	cancelCalls    []string
}

func (m *adapterMockSession) WriteUserMessage(_ context.Context, msg protocol.UserMessage) error {
	m.mu.Lock()
	m.writes = append(m.writes, msg)
	m.mu.Unlock()
	return nil
}

func (m *adapterMockSession) Interrupt(_ context.Context) error {
	atomic.AddInt32(&m.interruptCalls, 1)
	return m.interruptErr
}

func (m *adapterMockSession) CancelAsyncMessage(_ context.Context, uuid string) (bool, error) {
	m.mu.Lock()
	m.cancelCalls = append(m.cancelCalls, uuid)
	res := m.cancelResults[uuid]
	m.mu.Unlock()
	return res, nil
}

func (m *adapterMockSession) interruptCount() int {
	return int(atomic.LoadInt32(&m.interruptCalls))
}

func (m *adapterMockSession) lastWrite() (protocol.UserMessage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.writes) == 0 {
		return protocol.UserMessage{}, false
	}
	return m.writes[len(m.writes)-1], true
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

// TestTUIAdapter_SubscriberNamePropagates asserts the adapter registers its
// EventBus subscription under the descriptive "tui-viewport" label, so drop
// telemetry surfaces an actionable name. (QUM-482)
func TestTUIAdapter_SubscriberNamePropagates(t *testing.T) {
	mock := &adapterMockSession{}
	rt, _ := buildAdapter(t, mock)

	counts := rt.EventBus().DroppedCounts()
	if _, ok := counts["tui-viewport"]; !ok {
		t.Fatalf("DroppedCounts() = %v, want key %q", counts, "tui-viewport")
	}
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
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)

	// Drive a turn frame straight onto the bus (QUM-817: no queue/StartTurn).
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{
		Type:    sprawlrt.EventProtocolMessage,
		Message: makeAssistantMsg(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"Hello world"}]}}`),
	})

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
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)

	rt.EventBus().Publish(sprawlrt.RuntimeEvent{
		Type:    sprawlrt.EventProtocolMessage,
		Message: makeAssistantMsg(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"ls"}}]}}`),
	})

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
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)

	// user protocol message carrying a tool_result block
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{
		Type:    sprawlrt.EventProtocolMessage,
		Message: makeAssistantMsg(t, `{"type":"user","uuid":"u-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"output","is_error":false}]}}`),
	})

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
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)

	// QUM-817: emit EventProtocolMessage(result) + EventTurnCompleted directly,
	// mirroring what the frame router publishes for a real turn. The adapter
	// should surface the SessionResultMsg from EventTurnCompleted.
	resultRaw := `{"type":"result","subtype":"success","is_error":false,"result":"done","duration_ms":42,"num_turns":1,"total_cost_usd":0.01}`
	resultMsg := makeAssistantMsg(t, resultRaw)
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventProtocolMessage, Message: resultMsg})
	var pr protocol.ResultMessage
	if err := protocol.ParseAs(resultMsg, &pr); err != nil {
		t.Fatalf("ParseAs: %v", err)
	}
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventTurnCompleted, Result: &pr})

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
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)

	// QUM-817: a failed turn is published as EventTurnFailed; the adapter maps
	// it to SessionResultMsg{IsError:true, Result:<err>}.
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventTurnFailed, Error: errors.New("boom")})

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

// QUM-475: EventInterrupted is a TERMINAL lifecycle event (the turn drained
// after a user-initiated interrupt). The adapter must map it to a NEW
// InterruptCompletedMsg carrying the same result fields as SessionResultMsg
// (Result/DurationMs/NumTurns/TotalCostUsd) so the AppModel can drive
// turnState back to TurnIdle and re-arm continuous-bridge bookkeeping.
//
// This is distinct from InterruptResultMsg, which is the request-ack from
// Interrupt() (see TestTUIAdapter_Interrupt_ForwardsToRuntime). Conflating
// the two — as the pre-fix code does — means the request-ack path drives
// finalize logic (causing the wedge described in
// docs/forensics/tui-weave-wedge-2026-05-05.md) and the terminal path is
// invisible to the AppModel.
func TestTUIAdapter_WaitForEvent_Interrupted_InterruptCompletedMsg(t *testing.T) {
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)

	// QUM-817: EventInterrupted is published by the runtime when an in-flight
	// turn drains after a user interrupt. Emit it directly with a result
	// payload; the bridge adapter maps it to InterruptCompletedMsg.
	resultRaw := `{"type":"result","subtype":"interrupted","is_error":false,"result":"stopped","duration_ms":10,"num_turns":2,"total_cost_usd":0.005}`
	var pr protocol.ResultMessage
	if err := protocol.ParseAs(makeAssistantMsg(t, resultRaw), &pr); err != nil {
		t.Fatalf("ParseAs: %v", err)
	}
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventInterrupted, Result: &pr})

	deadline2 := time.After(3 * time.Second)
	for {
		msg := runCmd(t, a.WaitForEvent())
		if _, ok := msg.(tui.InterruptResultMsg); ok {
			t.Fatalf("WaitForEvent surfaced tui.InterruptResultMsg for EventInterrupted; want tui.InterruptCompletedMsg (request-ack must not collide with terminal lifecycle)")
		}
		if ic, ok := msg.(tui.InterruptCompletedMsg); ok {
			if ic.Result != "stopped" {
				t.Errorf("InterruptCompletedMsg.Result = %q, want %q", ic.Result, "stopped")
			}
			if ic.DurationMs != 10 {
				t.Errorf("InterruptCompletedMsg.DurationMs = %d, want 10", ic.DurationMs)
			}
			if ic.NumTurns != 2 {
				t.Errorf("InterruptCompletedMsg.NumTurns = %d, want 2", ic.NumTurns)
			}
			if ic.TotalCostUsd != 0.005 {
				t.Errorf("InterruptCompletedMsg.TotalCostUsd = %v, want 0.005", ic.TotalCostUsd)
			}
			return
		}
		select {
		case <-deadline2:
			t.Fatalf("did not see InterruptCompletedMsg; last=%T", msg)
		default:
		}
	}
}

func TestTUIAdapter_WaitForEvent_SkipsLifecycleEvents(t *testing.T) {
	// Ensure WaitForEvent does not surface EventTurnStarted / EventQueueDrained
	// / EventStopped as TUI messages — it must loop past them. We drive a
	// successful turn and verify the only msgs we observe are
	// AssistantContentMsg + SessionResultMsg.
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)

	// QUM-817: publish a representative turn directly — interleaving the
	// lifecycle-only events (TurnStarted/QueueDrained/Stopped) the adapter must
	// skip with the content + terminal events it must surface.
	resultRaw := `{"type":"result","subtype":"success","is_error":false,"duration_ms":1,"num_turns":1,"total_cost_usd":0}`
	resultMsg := makeAssistantMsg(t, resultRaw)
	var pr protocol.ResultMessage
	if err := protocol.ParseAs(resultMsg, &pr); err != nil {
		t.Fatalf("ParseAs: %v", err)
	}
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventTurnStarted})
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{
		Type:    sprawlrt.EventProtocolMessage,
		Message: makeAssistantMsg(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`),
	})
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventQueueDrained})
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventProtocolMessage, Message: resultMsg})
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventTurnCompleted, Result: &pr})

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

// QUM-817: SendMessage writes a kind:user prompt straight to the CLI stdin
// with priority "next" (the CLI owns queuing/coalescing). We assert the
// recorded stdin write rather than a Go-side queue item.
func TestTUIAdapter_SendMessage_WritesUserPriorityNext(t *testing.T) {
	mock := &adapterMockSession{}
	_, a := buildAdapter(t, mock)

	msg := runCmd(t, a.SendMessage("hello"))
	if _, ok := msg.(tui.UserMessageSentMsg); !ok {
		t.Fatalf("SendMessage() = %T, want tui.UserMessageSentMsg", msg)
	}

	um, ok := mock.lastWrite()
	if !ok {
		t.Fatal("SendMessage did not write a user message to stdin")
	}
	if um.Message.Content != "hello" {
		t.Errorf("write content = %q, want %q", um.Message.Content, "hello")
	}
	if um.Priority != "next" {
		t.Errorf("write priority = %q, want %q", um.Priority, "next")
	}
}

func TestTUIAdapter_Interrupt_ForwardsToRuntime(t *testing.T) {
	mock := &adapterMockSession{}
	_, a := buildAdapter(t, mock)

	// QUM-817: Interrupt() forwards unconditionally to the runtime, which
	// forwards to the backend session (no turn-loop state required).
	msg := runCmd(t, a.Interrupt())
	ir, ok := msg.(tui.InterruptResultMsg)
	if !ok {
		t.Fatalf("Interrupt() = %T, want tui.InterruptResultMsg", msg)
	}
	if ir.Err != nil {
		t.Errorf("InterruptResultMsg.Err = %v, want nil", ir.Err)
	}

	if mock.interruptCount() == 0 {
		t.Errorf("Session.Interrupt was not invoked")
	}
}

func TestTUIAdapter_Observe_SwapsSubscription(t *testing.T) {
	mockA := &adapterMockSession{}
	rtA := sprawlrt.New(sprawlrt.RuntimeConfig{Name: "a", Session: mockA})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rtA.Stop(ctx)
	})

	a := NewTUIAdapter(rtA)

	mockB := &adapterMockSession{}
	rtB := sprawlrt.New(sprawlrt.RuntimeConfig{Name: "b", Session: mockB})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rtB.Stop(ctx)
	})

	// Swap the adapter's observed runtime to B.
	a.Observe(rtB)

	// Publish on A's bus; the adapter should NOT see it.
	rtA.EventBus().Publish(sprawlrt.RuntimeEvent{
		Type:    sprawlrt.EventProtocolMessage,
		Message: makeAssistantMsg(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"alpha"}]}}`),
	})

	// Publish on B's bus; the adapter should see "beta".
	rtB.EventBus().Publish(sprawlrt.RuntimeEvent{
		Type:    sprawlrt.EventProtocolMessage,
		Message: makeAssistantMsg(t, `{"type":"assistant","uuid":"b-1","message":{"role":"assistant","content":[{"type":"text","text":"beta"}]}}`),
	})

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
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)

	// Publish BOTH the protocol-result frame AND EventTurnCompleted so the
	// dedupe (QUM-436 Item 1) is exercised: the protocol-result mapping is
	// dropped and only the lifecycle SessionResultMsg survives.
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{
		Type:    sprawlrt.EventProtocolMessage,
		Message: makeAssistantMsg(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`),
	})
	resultMsg := makeAssistantMsg(t, `{"type":"result","subtype":"success","is_error":false,"result":"done","duration_ms":42,"num_turns":3,"total_cost_usd":0.01}`)
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventProtocolMessage, Message: resultMsg})
	var pr protocol.ResultMessage
	if err := protocol.ParseAs(resultMsg, &pr); err != nil {
		t.Fatalf("ParseAs: %v", err)
	}
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventTurnCompleted, Result: &pr})

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
	mockA := &adapterMockSession{}
	rtA := sprawlrt.New(sprawlrt.RuntimeConfig{Name: "a", Session: mockA})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rtA.Stop(ctx)
	})
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

	mockB := &adapterMockSession{}
	rtB := sprawlrt.New(sprawlrt.RuntimeConfig{Name: "b", Session: mockB})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rtB.Stop(ctx)
	})

	a.Observe(rtB)

	// Drive a real event on rtB's bus.
	rtB.EventBus().Publish(sprawlrt.RuntimeEvent{
		Type:    sprawlrt.EventProtocolMessage,
		Message: makeAssistantMsg(t, `{"type":"assistant","uuid":"b-1","message":{"role":"assistant","content":[{"type":"text","text":"beta"}]}}`),
	})

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
	mockA := &adapterMockSession{}
	rtA := sprawlrt.New(sprawlrt.RuntimeConfig{Name: "a", Session: mockA})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rtA.Stop(ctx)
	})
	a := NewTUIAdapter(rtA)

	a.Cancel()

	mockB := &adapterMockSession{}
	rtB := sprawlrt.New(sprawlrt.RuntimeConfig{Name: "b", Session: mockB})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rtB.Stop(ctx)
	})

	a.Observe(rtB)

	// Drive a real event on rtB's bus; WaitForEvent must surface it (not EOF).
	rtB.EventBus().Publish(sprawlrt.RuntimeEvent{
		Type:    sprawlrt.EventProtocolMessage,
		Message: makeAssistantMsg(t, `{"type":"assistant","uuid":"b-1","message":{"role":"assistant","content":[{"type":"text","text":"beta"}]}}`),
	})

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

// QUM-830: SendAllNow() with a nil runtime must return a SendAllNowResultMsg
// (carrying ErrNoRuntime), NOT a SessionErrorMsg. The TUI's Ctrl+G debounce
// latch clears ONLY on SendAllNowResultMsg; if the nil-runtime window (during a
// session restart, before Observe(rt)) returned a SessionErrorMsg instead, the
// latch would never clear and Ctrl+G would wedge dead for the rest of the
// session. Mirrors the Interrupt() nil-runtime precedent (returns its own
// result type, not SessionErrorMsg).
func TestTUIAdapter_SendAllNow_NilRuntime_ReturnsSendAllNowResultErr(t *testing.T) {
	mock := &adapterMockSession{}
	_, a := buildAdapter(t, mock)
	a.Observe(nil)

	msg := runCmd(t, a.SendAllNow())
	res, ok := msg.(tui.SendAllNowResultMsg)
	if !ok {
		t.Fatalf("SendAllNow() with nil runtime = %T, want tui.SendAllNowResultMsg (so the Ctrl+G latch clears)", msg)
	}
	if !errors.Is(res.Err, ErrNoRuntime) {
		t.Errorf("Err = %v, want errors.Is(_, ErrNoRuntime)=true", res.Err)
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

// QUM-399: Close() must satisfy the tui.BridgeDelegate signature, returning
// nil and idempotently cancelling the EventBus subscription.
func TestTUIAdapter_Close_CancelsSubscriptionAndReturnsNil(t *testing.T) {
	mock := &adapterMockSession{}
	_, a := buildAdapter(t, mock)

	if err := a.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
	// Idempotent.
	if err := a.Close(); err != nil {
		t.Errorf("second Close() = %v, want nil", err)
	}
	// After Close, WaitForEvent must surface EOF.
	msg := runCmd(t, a.WaitForEvent())
	if _, ok := msg.(tui.SessionErrorMsg); !ok {
		t.Errorf("WaitForEvent after Close = %T, want tui.SessionErrorMsg (EOF)", msg)
	}
}

// QUM-399: IsContinuous must always return true so the AppModel keeps
// WaitForEvent running across turn boundaries when wrapping a TUIAdapter.
func TestTUIAdapter_IsContinuous_ReturnsTrue(t *testing.T) {
	mock := &adapterMockSession{}
	_, a := buildAdapter(t, mock)
	if !a.IsContinuous() {
		t.Errorf("IsContinuous() = false, want true")
	}
}

// --- QUM-669: gap detection at the adapter seam --------------------------
//
// Per docs/designs/qum-669-viewport-wedge-recovery.md §2.2, the TUIAdapter
// tracks `lastSeq` on its EventBus subscription. When a received event's
// Seq is non-contiguous with the previous one (and lastSeq != 0), the
// adapter emits a tui.EventDropDetectedMsg as the *next* tea.Msg result
// BEFORE returning the translated tea.Msg for the gap-arriving event. The
// first event after subscription (lastSeq == 0 sentinel) never emits a
// gap msg. After an Observe() swap, lastSeq resets to 0 so the first event
// on the new subscription is likewise not flagged.

// TestTUIAdapter_EmitsEventDropDetectedMsg drives the real WaitForEvent()
// codepath, using EventBus.PublishWithSeq (test-only) to deterministically
// stamp Seq=1, 2, 10 so the gap is observable without a slow-consumer race.
func TestTUIAdapter_EmitsEventDropDetectedMsg(t *testing.T) {
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)

	mkProtoEvent := func(text string) sprawlrt.RuntimeEvent {
		raw := `{"type":"assistant","uuid":"a-` + text +
			`","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}`
		return sprawlrt.RuntimeEvent{
			Type:    sprawlrt.EventProtocolMessage,
			Message: makeAssistantMsg(t, raw),
		}
	}

	// Pre-publish all three events; the adapter subscription buffer is 64
	// so they all enqueue without drops. The gap between Seq=2 and Seq=10
	// must be inferred from the seq stamps alone.
	rt.EventBus().PublishWithSeq(mkProtoEvent("one"), 1)
	rt.EventBus().PublishWithSeq(mkProtoEvent("two"), 2)
	rt.EventBus().PublishWithSeq(mkProtoEvent("ten"), 10)

	// Read 1: Seq=1 (first event on fresh subscription, no gap msg).
	msg1 := runCmd(t, a.WaitForEvent())
	if _, ok := msg1.(tui.EventDropDetectedMsg); ok {
		t.Fatalf("Seq=1 first event surfaced EventDropDetectedMsg; sentinel lastSeq=0 must suppress")
	}
	if _, ok := msg1.(tui.AssistantContentMsg); !ok {
		t.Fatalf("read 1: got %T, want tui.AssistantContentMsg", msg1)
	}

	// Read 2: Seq=2 (contiguous, no gap msg).
	msg2 := runCmd(t, a.WaitForEvent())
	if _, ok := msg2.(tui.EventDropDetectedMsg); ok {
		t.Fatalf("Seq=2 contiguous surfaced EventDropDetectedMsg; want none")
	}
	if _, ok := msg2.(tui.AssistantContentMsg); !ok {
		t.Fatalf("read 2: got %T, want tui.AssistantContentMsg", msg2)
	}

	// Read 3: gap detected (Seq jumps 2 -> 10). Expect EventDropDetectedMsg.
	msg3 := runCmd(t, a.WaitForEvent())
	drop, ok := msg3.(tui.EventDropDetectedMsg)
	if !ok {
		t.Fatalf("read 3 (gap): got %T, want tui.EventDropDetectedMsg", msg3)
	}
	if drop.From != 2 {
		t.Errorf("EventDropDetectedMsg.From = %d, want 2", drop.From)
	}
	if drop.To != 10 {
		t.Errorf("EventDropDetectedMsg.To = %d, want 10", drop.To)
	}
	if drop.Missing != 7 {
		t.Errorf("EventDropDetectedMsg.Missing = %d, want 7", drop.Missing)
	}

	// Read 4: the translated msg for the Seq=10 event must still flow in-band
	// after the drop notice.
	msg4 := runCmd(t, a.WaitForEvent())
	if _, ok := msg4.(tui.AssistantContentMsg); !ok {
		t.Fatalf("read 4 (post-gap event): got %T, want tui.AssistantContentMsg (in-band event must still flow)", msg4)
	}
}

// TestTUIAdapter_OutOfOrderSeq_DoesNotUnderflow guards against a regression
// observed in the QUM-669 drain-row-inject e2e: a backwards or duplicate Seq
// arriving on the subscription must NOT produce an EventDropDetectedMsg with
// a wrapped uint64 missing count (e.g. 18446744073709551615). Even though
// EventBus.publishMu now serializes stamp+fanout production-side, the adapter
// keeps a defensive guard so any future regression surfaces as a missing
// gap-msg, not as a screaming-uint64 banner.
func TestTUIAdapter_OutOfOrderSeq_DoesNotUnderflow(t *testing.T) {
	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)

	mkEv := func(text string) sprawlrt.RuntimeEvent {
		raw := `{"type":"assistant","uuid":"a-` + text +
			`","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}`
		return sprawlrt.RuntimeEvent{
			Type:    sprawlrt.EventProtocolMessage,
			Message: makeAssistantMsg(t, raw),
		}
	}

	// Forward to Seq=5, then deliver Seq=3 (out-of-order / duplicate) and
	// Seq=4 (also out-of-order). Neither must produce a drop msg.
	rt.EventBus().PublishWithSeq(mkEv("five"), 5)
	rt.EventBus().PublishWithSeq(mkEv("three"), 3)
	rt.EventBus().PublishWithSeq(mkEv("four"), 4)

	// Read 1: Seq=5 (first event, sentinel suppresses gap msg).
	if _, ok := runCmd(t, a.WaitForEvent()).(tui.AssistantContentMsg); !ok {
		t.Fatalf("read 1: want AssistantContentMsg")
	}
	// Read 2: Seq=3, backwards. Must NOT emit EventDropDetectedMsg.
	msg2 := runCmd(t, a.WaitForEvent())
	if drop, ok := msg2.(tui.EventDropDetectedMsg); ok {
		t.Fatalf("backward Seq=3 surfaced EventDropDetectedMsg{Missing=%d}; want translated msg only", drop.Missing)
	}
	// Read 3: Seq=4, also backwards. Must NOT emit a drop msg.
	msg3 := runCmd(t, a.WaitForEvent())
	if drop, ok := msg3.(tui.EventDropDetectedMsg); ok {
		t.Fatalf("backward Seq=4 surfaced EventDropDetectedMsg{Missing=%d}; want translated msg only", drop.Missing)
	}
}

// TestTUIAdapter_ObserveResetsGapBaseline verifies that after Observe() swaps
// the adapter onto a fresh runtime+bus, the first event on the new
// subscription does NOT emit a spurious EventDropDetectedMsg even though its
// Seq value (1) is less than the previous bus's lastSeq.
func TestTUIAdapter_ObserveResetsGapBaseline(t *testing.T) {
	mockA := &adapterMockSession{}
	rtA, a := buildAdapter(t, mockA)

	mkProtoEvent := func(text string) sprawlrt.RuntimeEvent {
		raw := `{"type":"assistant","uuid":"a-` + text +
			`","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}`
		return sprawlrt.RuntimeEvent{
			Type:    sprawlrt.EventProtocolMessage,
			Message: makeAssistantMsg(t, raw),
		}
	}

	// Pump bus A up to lastSeq=10 from the adapter's POV. Publish two
	// events (Seq=1 and Seq=10); reading both establishes lastSeq=10 inside
	// the adapter (after first event AND after the implied drop+translated
	// pair when production logic lands). For the RED test we just need at
	// least one read to occur so the swap path matters.
	rtA.EventBus().PublishWithSeq(mkProtoEvent("a1"), 1)
	rtA.EventBus().PublishWithSeq(mkProtoEvent("a10"), 10)

	// Read until we've consumed both translated AssistantContentMsg events
	// from bus A. Once production logic emits the gap msg there will be an
	// additional EventDropDetectedMsg interleaved; tolerate that.
	gotContent := 0
	for gotContent < 2 {
		msg := runCmd(t, a.WaitForEvent())
		if _, ok := msg.(tui.AssistantContentMsg); ok {
			gotContent++
		}
	}

	// Swap onto a fresh runtime (and therefore a fresh bus whose CurrentSeq
	// starts at 0).
	mockB := &adapterMockSession{}
	rtB := sprawlrt.New(sprawlrt.RuntimeConfig{
		Name:    "tui-adapter-test-b",
		Session: mockB,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rtB.Stop(ctx)
	})
	a.Observe(rtB)

	// First event on the new bus has Seq=1; must NOT emit a gap msg even
	// though prior bus's lastSeq was 10.
	rtB.EventBus().PublishWithSeq(mkProtoEvent("b1"), 1)
	msg := runCmd(t, a.WaitForEvent())
	if _, ok := msg.(tui.EventDropDetectedMsg); ok {
		t.Fatalf("post-Observe first event surfaced spurious EventDropDetectedMsg; lastSeq must reset on Observe")
	}
	if _, ok := msg.(tui.AssistantContentMsg); !ok {
		t.Fatalf("post-Observe first event: got %T, want tui.AssistantContentMsg", msg)
	}

	// Silence unused warnings if rtA is the only thing keeping the import live.
	_ = rtA
}

// TestTUIAdapter_DebugGapInject_TriggersSyntheticDrop verifies the
// SPRAWL_DEBUG_GAP_INJECT one-shot test seam (QUM-669 viewport-resync e2e
// row). When the env var is set to a positive uint64 at subscribe time, the
// adapter synthesizes an EventDropDetectedMsg with Missing=N at the SECOND
// event of the subscription, then resumes normal lastSeq tracking from the
// real Seq of the arriving event. The translated msg for that arriving
// event must still flow in-band on the next WaitForEvent call.
func TestTUIAdapter_DebugGapInject_TriggersSyntheticDrop(t *testing.T) {
	t.Setenv("SPRAWL_DEBUG_GAP_INJECT", "15")

	mock := &adapterMockSession{}
	rt, a := buildAdapter(t, mock)

	mkProtoEvent := func(text string) sprawlrt.RuntimeEvent {
		raw := `{"type":"assistant","uuid":"a-` + text +
			`","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}`
		return sprawlrt.RuntimeEvent{
			Type:    sprawlrt.EventProtocolMessage,
			Message: makeAssistantMsg(t, raw),
		}
	}

	rt.EventBus().PublishWithSeq(mkProtoEvent("one"), 1)
	rt.EventBus().PublishWithSeq(mkProtoEvent("two"), 2)

	// Read 1: first event on the fresh subscription — sentinel lastSeq=0
	// suppresses any drop msg even with injectGap set.
	msg1 := runCmd(t, a.WaitForEvent())
	if _, ok := msg1.(tui.EventDropDetectedMsg); ok {
		t.Fatalf("read 1: surfaced EventDropDetectedMsg; sentinel lastSeq=0 must suppress")
	}
	if _, ok := msg1.(tui.AssistantContentMsg); !ok {
		t.Fatalf("read 1: got %T, want tui.AssistantContentMsg", msg1)
	}

	// Read 2: arrival of the second event (Seq=2). The one-shot inject
	// seam must synthesize an EventDropDetectedMsg{Missing: 15} here BEFORE
	// the translated msg for Seq=2 is delivered.
	msg2 := runCmd(t, a.WaitForEvent())
	drop, ok := msg2.(tui.EventDropDetectedMsg)
	if !ok {
		t.Fatalf("read 2: got %T, want tui.EventDropDetectedMsg (synthetic gap)", msg2)
	}
	if drop.Missing != 15 {
		t.Errorf("EventDropDetectedMsg.Missing = %d, want 15", drop.Missing)
	}
	if drop.From != 1 {
		t.Errorf("EventDropDetectedMsg.From = %d, want 1", drop.From)
	}
	if drop.To != 1+15+1 {
		t.Errorf("EventDropDetectedMsg.To = %d, want %d", drop.To, 1+15+1)
	}

	// Read 3: the translated msg for the in-band Seq=2 event must still flow.
	msg3 := runCmd(t, a.WaitForEvent())
	if _, ok := msg3.(tui.AssistantContentMsg); !ok {
		t.Fatalf("read 3 (post-synthetic-gap event): got %T, want tui.AssistantContentMsg", msg3)
	}

	// Read 4: a third real event must NOT trigger a second synthetic gap —
	// injectGap is one-shot per subscription. Publish a contiguous Seq=3.
	rt.EventBus().PublishWithSeq(mkProtoEvent("three"), 3)
	msg4 := runCmd(t, a.WaitForEvent())
	if _, ok := msg4.(tui.EventDropDetectedMsg); ok {
		t.Fatalf("read 4: second synthetic gap fired; injectGap must be one-shot")
	}
}

// --- QUM-824: Recall + SendAllNow bridge ---

func TestTUIAdapter_Recall_NilRuntime_ReturnsSessionError(t *testing.T) {
	mock := &adapterMockSession{}
	_, a := buildAdapter(t, mock)
	a.Observe(nil)
	msg := runCmd(t, a.Recall())
	se, ok := msg.(tui.SessionErrorMsg)
	if !ok {
		t.Fatalf("Recall() = %T, want tui.SessionErrorMsg", msg)
	}
	if !errors.Is(se.Err, ErrNoRuntime) {
		t.Errorf("Err = %v, want errors.Is(_, ErrNoRuntime)", se.Err)
	}
}

// QUM-830: the former TestTUIAdapter_SendAllNow_NilRuntime_ReturnsSessionError
// asserted the old contract (SessionErrorMsg on nil runtime), which wedged the
// Ctrl+G debounce latch. Superseded by
// TestTUIAdapter_SendAllNow_NilRuntime_ReturnsSendAllNowResultErr above.

func TestTUIAdapter_Recall_RehydratesPendingText(t *testing.T) {
	mock := &adapterMockSession{cancelResults: map[string]bool{}}
	rt, a := buildAdapter(t, mock)

	uuid, err := rt.WriteUserPrompt(context.Background(), "draft text", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	mock.cancelResults[uuid] = true

	msg := runCmd(t, a.Recall())
	pr, ok := msg.(tui.PromptsRecalledMsg)
	if !ok {
		t.Fatalf("Recall() = %T, want tui.PromptsRecalledMsg", msg)
	}
	if pr.Err != nil {
		t.Fatalf("PromptsRecalledMsg.Err = %v", pr.Err)
	}
	if pr.Text != "draft text" {
		t.Errorf("Text = %q, want %q", pr.Text, "draft text")
	}
}

func TestTUIAdapter_SendAllNow_DelegatesToRuntime(t *testing.T) {
	mock := &adapterMockSession{cancelResults: map[string]bool{}}
	rt, a := buildAdapter(t, mock)

	uuid, err := rt.WriteUserPrompt(context.Background(), "queued one", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	mock.cancelResults[uuid] = true

	msg := runCmd(t, a.SendAllNow())
	sr, ok := msg.(tui.SendAllNowResultMsg)
	if !ok {
		t.Fatalf("SendAllNow() = %T, want tui.SendAllNowResultMsg", msg)
	}
	if sr.Err != nil {
		t.Fatalf("SendAllNowResultMsg.Err = %v", sr.Err)
	}
	// The last write must be the now-priority resubmit superseding the queued
	// prompt (the original was a separate `next` write with the same content).
	w, ok := mock.lastWrite()
	if !ok {
		t.Fatal("SendAllNow did not write the concatenated now message")
	}
	if w.Priority != "now" {
		t.Errorf("last write priority = %q, want now", w.Priority)
	}
	if w.Message.Content != "queued one" {
		t.Errorf("last write content = %q, want %q", w.Message.Content, "queued one")
	}
}
