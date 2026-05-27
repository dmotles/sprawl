package backend

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-631: the backend reader must surface frames belonging to an *autonomous*
// turn (one the harness re-prompts itself into — opened by a system:init while
// no sprawl StartTurn is active) to a synchronous, opt-in callback installed
// on the concrete *session via SetAutonomousFrameHandler. Without it those
// frames reach NOWHERE except the optional async Observer, so the runtime
// EventBus (and therefore the TUI) never sees harness-initiated turns.
//
// SetAutonomousFrameHandler is NOT on the Session interface — it is accessed
// via a type assertion, mirroring SetTerminalErrorHandler (see
// session_terminalerr_handler_test.go).

// autonomousFrameSetter is the type-assertion surface the runtime wiring uses
// to install the autonomous-frame handler on the concrete *session.
type autonomousFrameSetter interface {
	SetAutonomousFrameHandler(func(*protocol.Message))
}

// installAutonomousFrameHandler casts the Session to the (non-interface)
// autonomous-frame setter and registers h. It fatals if the assertion fails,
// which is the expected red-phase failure until the method exists.
func installAutonomousFrameHandler(t *testing.T, s Session, h func(*protocol.Message)) {
	t.Helper()
	setter, ok := s.(autonomousFrameSetter)
	if !ok {
		t.Fatalf("Session does not satisfy SetAutonomousFrameHandler(func(*protocol.Message)) — autonomous-frame handler not wired (QUM-631)")
	}
	setter.SetAutonomousFrameHandler(h)
}

// assistantText pulls the first text block out of an assistant protocol.Message.
func assistantText(t *testing.T, msg *protocol.Message) string {
	t.Helper()
	var am protocol.AssistantMessage
	if err := protocol.ParseAs(msg, &am); err != nil {
		t.Fatalf("ParseAs(AssistantMessage): %v", err)
	}
	var inner struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(am.Content, &inner); err != nil {
		t.Fatalf("unmarshal assistant content: %v", err)
	}
	for _, c := range inner.Content {
		if c.Type == "text" {
			return c.Text
		}
	}
	return ""
}

// TestSession_AutonomousFrameHandler_ForwardsContentBearingTurn (QUM-631) is
// the load-bearing test: a content-bearing autonomous turn (system:init →
// assistant → result, no StartTurn) must be delivered frame-by-frame to the
// registered autonomous-frame handler, with the assistant CONTENT intact.
func TestSession_AutonomousFrameHandler_ForwardsContentBearingTurn(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	var (
		mu  sync.Mutex
		got []*protocol.Message
	)
	installAutonomousFrameHandler(t, session, func(msg *protocol.Message) {
		mu.Lock()
		got = append(got, msg)
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Feed an autonomous turn (no StartTurn).
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"auto-reply"}]}}`)
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("autonomous-frame handler saw %d frames, want 3", n)
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	frames := append([]*protocol.Message(nil), got...)
	mu.Unlock()

	if len(frames) != 3 {
		t.Fatalf("handler received %d frames, want exactly 3", len(frames))
	}
	if frames[0].Type != "system" || frames[0].Subtype != "init" {
		t.Errorf("frame[0] = %s/%s, want system/init", frames[0].Type, frames[0].Subtype)
	}
	if frames[1].Type != "assistant" {
		t.Errorf("frame[1].Type = %q, want assistant", frames[1].Type)
	}
	if frames[2].Type != "result" {
		t.Errorf("frame[2].Type = %q, want result", frames[2].Type)
	}
	// The content assertion is the key proof that CONTENT (not just frame
	// types) reaches the new surface.
	if txt := assistantText(t, frames[1]); txt != "auto-reply" {
		t.Errorf("assistant frame text = %q, want %q", txt, "auto-reply")
	}
}

// TestSession_AutonomousFrameHandler_NotCalledForStrayFrame (QUM-631 + QUM-570
// guard): a stray assistant frame with NO preceding system:init does not open
// an autonomous turnFrame, so it must remain observer-only and NEVER reach the
// autonomous-frame handler.
func TestSession_AutonomousFrameHandler_NotCalledForStrayFrame(t *testing.T) {
	transport := newMockManagedTransport()
	observer := &recordingObserver{}
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
		Observer:  observer,
	})

	var calls atomic.Int32
	installAutonomousFrameHandler(t, session, func(*protocol.Message) {
		calls.Add(1)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Stray telemetry: assistant frame with no preceding system:init.
	transport.feedMessage(t, `{"type":"assistant","uuid":"stray-1","message":{"role":"assistant","content":[{"type":"text","text":"stray"}]}}`)

	// Wait until the Observer has seen the stray, proving the reader processed it.
	deadline := time.After(2 * time.Second)
	for len(observer.Types()) < 1 {
		select {
		case <-deadline:
			t.Fatal("observer never saw stray assistant frame")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if got := calls.Load(); got != 0 {
		t.Fatalf("autonomous-frame handler invoked %d times for a stray frame, want 0 (QUM-570: stray frames stay observer-only)", got)
	}
}

// TestSession_AutonomousFrameHandler_NotCalledForSprawlTurn (QUM-631 + QUM-578
// guard): a normal sprawl-initiated turn has autonomous==false and must
// continue to flow only through the subscriber channel — never the
// autonomous-frame handler.
func TestSession_AutonomousFrameHandler_NotCalledForSprawlTurn(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	var calls atomic.Int32
	installAutonomousFrameHandler(t, session, func(*protocol.Message) {
		calls.Add(1)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Drive a normal sprawl turn: drain the user prompt, then feed an
	// assistant + result/success.
	go func() {
		<-transport.sendCh
		transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`)
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	}()
	events, err := session.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainMessages(events)

	if got := calls.Load(); got != 0 {
		t.Fatalf("autonomous-frame handler invoked %d times for a sprawl-initiated turn, want 0 (QUM-578: sprawl turns flow only through the subscriber channel)", got)
	}
}

// TestSession_AutonomousFrameHandler_SlowHandlerDoesNotWedgeReader (QUM-595
// guard): a synchronous autonomous handler that takes non-trivial time per
// call must not stall the reader, trip ErrSubscriberWedged, or push the
// session into a terminal fault. All frames must still be delivered and a
// follow-on StartTurn must still succeed.
func TestSession_AutonomousFrameHandler_SlowHandlerDoesNotWedgeReader(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	var calls atomic.Int32
	installAutonomousFrameHandler(t, session, func(*protocol.Message) {
		// Simulate a slow consumer with a small per-call sleep.
		time.Sleep(5 * time.Millisecond)
		calls.Add(1)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Autonomous turn: init + several assistant frames + result.
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"one"}]}}`)
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-2","message":{"role":"assistant","content":[{"type":"text","text":"two"}]}}`)
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-3","message":{"role":"assistant","content":[{"type":"text","text":"three"}]}}`)
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)

	// All 5 frames must eventually reach the handler.
	deadline := time.After(3 * time.Second)
	for calls.Load() < 5 {
		select {
		case <-deadline:
			t.Fatalf("slow handler saw %d frames, want 5 (reader stalled?)", calls.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}

	if session.IsTerminallyFaulted() {
		t.Fatalf("session entered terminal fault with a slow autonomous handler: %v", session.LastTurnError())
	}

	// A follow-on sprawl turn must still succeed.
	go func() {
		<-transport.sendCh
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	}()
	events, err := session.StartTurn(ctx, "after-auto")
	if err != nil {
		t.Fatalf("StartTurn after slow autonomous turn: %v", err)
	}
	drainMessages(events)
}

// taskNotificationFrame is the wire JSON for a harness `system/task_notification`
// trigger frame (QUM-634). On the stdout wire it carries a ready-made
// human-readable summary and arrives ~6ms BEFORE the system/init that opens
// the autonomous turn.
const taskNotificationFrame = `{"type":"system","subtype":"task_notification","task_id":"bzgr4iuq0","status":"completed","summary":"Background command \"sleep 30\" completed (exit code 0)"}`

// taskNotificationSummary is the human-readable summary embedded in
// taskNotificationFrame.
const taskNotificationSummary = `Background command "sleep 30" completed (exit code 0)`

// TestSession_AutonomousFrameHandler_TaskNotificationStashedThenAttachedOnInit
// (QUM-634) is the load-bearing stash-then-attach test: a pre-init
// `system/task_notification` frame arrives while currentTurn == nil. Per
// QUM-570 it must NOT allocate a turnFrame on its own, but it must be stashed
// in a single-slot pendingTrigger. When the next autonomous system/init
// allocates the turnFrame, the stashed trigger must be emitted to the
// autonomous-frame handler FIRST (before the init), then the init, assistant,
// and result follow — proving the trigger is surfaced before the turn opens.
func TestSession_AutonomousFrameHandler_TaskNotificationStashedThenAttachedOnInit(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	var (
		mu  sync.Mutex
		got []*protocol.Message
	)
	installAutonomousFrameHandler(t, session, func(msg *protocol.Message) {
		mu.Lock()
		got = append(got, msg)
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Trigger arrives BEFORE init (the QUM-634 timing crux), then the
	// autonomous turn: init → assistant → result.
	transport.feedMessage(t, taskNotificationFrame)
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"auto-reply"}]}}`)
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)

	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 4 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("autonomous-frame handler saw %d frames, want 4 (trigger + init + assistant + result)", n)
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	frames := append([]*protocol.Message(nil), got...)
	mu.Unlock()

	if len(frames) != 4 {
		t.Fatalf("handler received %d frames, want exactly 4 (trigger emitted before init)", len(frames))
	}
	// frame[0] is the stashed trigger, emitted BEFORE the init.
	if frames[0].Type != "system" || frames[0].Subtype != "task_notification" {
		t.Fatalf("frame[0] = %s/%s, want system/task_notification (stashed trigger must be emitted FIRST)", frames[0].Type, frames[0].Subtype)
	}
	var tn protocol.TaskNotification
	if err := protocol.ParseAs(frames[0], &tn); err != nil {
		t.Fatalf("ParseAs(TaskNotification) on frame[0]: %v", err)
	}
	if tn.Summary != taskNotificationSummary {
		t.Errorf("trigger summary = %q, want %q", tn.Summary, taskNotificationSummary)
	}
	if frames[1].Type != "system" || frames[1].Subtype != "init" {
		t.Errorf("frame[1] = %s/%s, want system/init", frames[1].Type, frames[1].Subtype)
	}
	if frames[2].Type != "assistant" {
		t.Errorf("frame[2].Type = %q, want assistant", frames[2].Type)
	}
	if frames[3].Type != "result" {
		t.Errorf("frame[3].Type = %q, want result", frames[3].Type)
	}
}

// TestSession_TaskNotificationPreInit_DoesNotAllocateFrameOrGate (QUM-634 +
// QUM-570 guard): a lone pre-init task_notification with NO following init
// must NOT allocate a turnFrame (it's a passive stash), must NOT invoke the
// autonomous-frame handler (the trigger only fires once attached on init), and
// must NOT gate a subsequent sprawl StartTurn. Mirrors
// TestSession_AutonomousFrame_StrayFrameDoesNotOpenFrame.
func TestSession_TaskNotificationPreInit_DoesNotAllocateFrameOrGate(t *testing.T) {
	transport := newMockManagedTransport()
	observer := &recordingObserver{}
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
		Observer:  observer,
	})

	var calls atomic.Int32
	installAutonomousFrameHandler(t, session, func(*protocol.Message) {
		calls.Add(1)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Lone pre-init task_notification — no following system/init.
	transport.feedMessage(t, taskNotificationFrame)

	// Wait until the observer has seen it, proving the reader processed it.
	deadline := time.After(2 * time.Second)
	for len(observer.Types()) < 1 {
		select {
		case <-deadline:
			t.Fatal("observer never saw the pre-init task_notification frame")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// The trigger must NOT have reached the autonomous handler yet — it only
	// fires when attached on a following init.
	if got := calls.Load(); got != 0 {
		t.Fatalf("autonomous-frame handler invoked %d times for a lone task_notification, want 0 (only fires when attached on init)", got)
	}

	// StartTurn must not block — the stashed trigger must not have allocated a
	// phantom autonomous frame, and must not return ErrTurnInProgress.
	startDone := make(chan error, 1)
	go func() {
		<-transport.sendCh
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	}()
	go func() {
		events, err := session.StartTurn(ctx, "go")
		if err != nil {
			startDone <- err
			return
		}
		drainMessages(events)
		startDone <- nil
	}()

	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("StartTurn() error: %v (stashed trigger must not gate StartTurn)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartTurn blocked despite only a stashed task_notification (no turnFrame should have been allocated)")
	}
}

// TestSession_TaskNotificationClearedOnStartTurn (QUM-634): a pre-init
// task_notification is stashed, then a sprawl StartTurn runs to completion.
// The stash must be cleared by StartTurn so a SUBSEQUENT autonomous turn does
// NOT carry the stale trigger. The second autonomous init must deliver only
// init + assistant + result (3 frames), with no leading task_notification.
func TestSession_TaskNotificationClearedOnStartTurn(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	var (
		mu  sync.Mutex
		got []*protocol.Message
	)
	installAutonomousFrameHandler(t, session, func(msg *protocol.Message) {
		mu.Lock()
		got = append(got, msg)
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Stash a trigger pre-init.
	transport.feedMessage(t, taskNotificationFrame)

	// Drive a sprawl turn to completion — this must clear the stale stash.
	go func() {
		<-transport.sendCh
		transport.feedMessage(t, `{"type":"assistant","uuid":"u-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`)
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	}()
	events, err := session.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainMessages(events)

	// Now a FRESH autonomous turn with no new trigger frame.
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-2","message":{"role":"assistant","content":[{"type":"text","text":"auto"}]}}`)
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)

	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("autonomous-frame handler saw %d frames, want 3 (init + assistant + result)", n)
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	frames := append([]*protocol.Message(nil), got...)
	mu.Unlock()

	if len(frames) != 3 {
		t.Fatalf("handler received %d frames, want exactly 3 (stale trigger must have been cleared by StartTurn); frames=%v", len(frames), frameLabels(frames))
	}
	for i, f := range frames {
		if f.Type == "system" && f.Subtype == "task_notification" {
			t.Fatalf("frame[%d] is a stale system/task_notification — StartTurn must clear the pending trigger", i)
		}
	}
	if frames[0].Type != "system" || frames[0].Subtype != "init" {
		t.Errorf("frame[0] = %s/%s, want system/init", frames[0].Type, frames[0].Subtype)
	}
}

// TestSession_TaskNotificationMidTurn_NotStashed (QUM-634): a task_notification
// arriving WHILE a sprawl turn is active (currentTurn != nil) must not be
// stashed as a pending trigger. After that turn completes, a later autonomous
// init must NOT carry the mid-turn task_notification (only init + assistant +
// result, 3 frames). This pins that stashing is gated on currentTurn == nil.
func TestSession_TaskNotificationMidTurn_NotStashed(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	var (
		mu  sync.Mutex
		got []*protocol.Message
	)
	installAutonomousFrameHandler(t, session, func(msg *protocol.Message) {
		mu.Lock()
		got = append(got, msg)
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Drive a sprawl turn; feed a task_notification mid-turn (before the
	// result), so currentTurn != nil when it arrives.
	go func() {
		<-transport.sendCh
		transport.feedMessage(t, `{"type":"assistant","uuid":"u-1","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}`)
		transport.feedMessage(t, taskNotificationFrame)
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	}()
	events, err := session.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainMessages(events)

	// Later autonomous turn — must NOT carry the mid-turn task_notification.
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-2","message":{"role":"assistant","content":[{"type":"text","text":"auto"}]}}`)
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)

	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("autonomous-frame handler saw %d frames, want 3 (init + assistant + result)", n)
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	frames := append([]*protocol.Message(nil), got...)
	mu.Unlock()

	for i, f := range frames {
		if f.Type == "system" && f.Subtype == "task_notification" {
			t.Fatalf("frame[%d] is a mid-turn system/task_notification — a trigger arriving while currentTurn != nil must NOT be stashed; frames=%v", i, frameLabels(frames))
		}
	}
	if len(frames) != 3 {
		t.Fatalf("handler received %d frames, want exactly 3; frames=%v", len(frames), frameLabels(frames))
	}
}

// frameLabels renders a compact type[:subtype] label list for failure messages.
func frameLabels(frames []*protocol.Message) []string {
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		label := f.Type
		if f.Subtype != "" {
			label += ":" + f.Subtype
		}
		out = append(out, label)
	}
	return out
}
