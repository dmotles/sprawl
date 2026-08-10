package backend

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-815 (Slice 1): the backend reader is a single observe-and-route loop.
// Every frame belonging to a turn — sprawl-initiated (StartTurn) OR autonomous
// (the harness re-prompts itself, opened by a system:init while no StartTurn is
// active) — plus a pre-init autonomous trigger (system/task_notification while
// idle) flows to ONE runtime-installed callback, SetFrameRouter. This replaces
// the QUM-631 SetAutonomousFrameHandler + the QUM-634 pendingTrigger stash.
//
// SetFrameRouter is NOT on the Session interface — it is accessed via a type
// assertion, mirroring SetTerminalErrorHandler.

// frameRouterSetter is the type-assertion surface the runtime wiring uses to
// install the single frame router on the concrete *session.
type frameRouterSetter interface {
	SetFrameRouter(func(*protocol.Message, TurnInfo))
}

// routedFrame records one router invocation.
type routedFrame struct {
	msg  *protocol.Message
	turn TurnInfo
}

// frameRouterRecorder is a concurrency-safe recorder of router invocations.
type frameRouterRecorder struct {
	mu     sync.Mutex
	frames []routedFrame
}

func (r *frameRouterRecorder) handler() func(*protocol.Message, TurnInfo) {
	return func(msg *protocol.Message, turn TurnInfo) {
		r.mu.Lock()
		r.frames = append(r.frames, routedFrame{msg: msg, turn: turn})
		r.mu.Unlock()
	}
}

func (r *frameRouterRecorder) snapshot() []routedFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]routedFrame(nil), r.frames...)
}

func (r *frameRouterRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}

// installFrameRouter casts the Session to the (non-interface) frame-router
// setter and registers h. It fatals if the assertion fails — the expected
// red-phase failure until the method exists.
func installFrameRouter(t *testing.T, s Session, h func(*protocol.Message, TurnInfo)) {
	t.Helper()
	setter, ok := s.(frameRouterSetter)
	if !ok {
		t.Fatalf("Session does not satisfy SetFrameRouter(func(*protocol.Message, TurnInfo)) — frame router not wired (QUM-815)")
	}
	setter.SetFrameRouter(h)
}

// waitForCount blocks until the recorder has at least n frames or the deadline
// elapses.
func (r *frameRouterRecorder) waitForCount(t *testing.T, n int, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		if r.count() >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("frame router saw %d frames, want >= %d", r.count(), n)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// taskNotificationFrame is the wire JSON for a harness system/task_notification.
const taskNotificationFrame = `{"type":"system","subtype":"task_notification","task_id":"bzgr4iuq0","status":"completed","summary":"Background command \"sleep 30\" completed (exit code 0)"}`

// TestSession_FrameRouter_AutonomousTurn_RoutesAllFramesWithTurnInfo: an
// autonomous turn (init → assistant → result, no StartTurn) routes every frame
// with Autonomous=true; EndOfTurn is set only on the result.
func TestSession_FrameRouter_AutonomousTurn_RoutesAllFramesWithTurnInfo(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})
	rec := &frameRouterRecorder{}
	installFrameRouter(t, session, rec.handler())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"auto-reply"}]}}`)
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)

	rec.waitForCount(t, 3, 3*time.Second)
	frames := rec.snapshot()
	if len(frames) != 3 {
		t.Fatalf("router saw %d frames, want 3", len(frames))
	}
	for i, f := range frames {
		if !f.turn.Autonomous {
			t.Errorf("frame[%d] Autonomous=false, want true", i)
		}
	}
	if frames[0].msg.Subtype != "init" || frames[1].msg.Type != "assistant" || frames[2].msg.Type != "result" {
		t.Fatalf("unexpected frame order: %s/%s, %s, %s", frames[0].msg.Type, frames[0].msg.Subtype, frames[1].msg.Type, frames[2].msg.Type)
	}
	if frames[0].turn.EndOfTurn || frames[1].turn.EndOfTurn {
		t.Error("EndOfTurn set on a non-result frame")
	}
	if !frames[2].turn.EndOfTurn {
		t.Error("EndOfTurn not set on the result frame")
	}
}

// TestSession_FrameRouter_PreInitTaskNotification_RoutedInOrder: a pre-init
// task_notification (arriving while idle, ~6ms before init) is routed
// immediately — no pendingTrigger stash — and is the first frame the router
// sees, followed by init/assistant/result, all Autonomous=true.
func TestSession_FrameRouter_PreInitTaskNotification_RoutedInOrder(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})
	rec := &frameRouterRecorder{}
	installFrameRouter(t, session, rec.handler())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	transport.feedMessage(t, taskNotificationFrame)
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"auto-reply"}]}}`)
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)

	rec.waitForCount(t, 4, 3*time.Second)
	frames := rec.snapshot()
	if len(frames) != 4 {
		t.Fatalf("router saw %d frames, want 4 (trigger + init + assistant + result)", len(frames))
	}
	if frames[0].msg.Type != "system" || frames[0].msg.Subtype != "task_notification" {
		t.Fatalf("frame[0] = %s/%s, want system/task_notification routed first", frames[0].msg.Type, frames[0].msg.Subtype)
	}
	for i, f := range frames {
		if !f.turn.Autonomous {
			t.Errorf("frame[%d] Autonomous=false, want true", i)
		}
	}
}

// TestSession_FrameRouter_SessionStateChangedRouted (QUM-903): a
// session_state_changed frame while idle IS routed to the frame router as the
// authoritative in_turn signal (carrying TurnInfo.StateChange), but must NOT
// allocate a turnFrame / open a turn (it never gates StartTurn — QUM-570).
func TestSession_FrameRouter_SessionStateChangedRouted(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})
	rec := &frameRouterRecorder{}
	installFrameRouter(t, session, rec.handler())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	transport.feedMessage(t, `{"type":"system","subtype":"session_state_changed","state":"idle","session_id":"sess-1"}`)
	rec.waitForCount(t, 1, 2*time.Second)
	frames := rec.snapshot()
	if len(frames) != 1 {
		t.Fatalf("router saw %d frames, want 1", len(frames))
	}
	if frames[0].turn.StateChange != "idle" {
		t.Errorf("routed StateChange = %q, want %q", frames[0].turn.StateChange, "idle")
	}
	if session.InTurn() {
		t.Error("session_state_changed frame opened a turn (InTurn=true), want false")
	}
}

// TestSession_FrameRouter_TrulyStrayFrameNotRouted: a stray system frame while
// idle that is neither init, task_notification, session_state_changed, nor a
// compaction status frame must NOT be routed and must NOT open a turn (QUM-570 —
// stray telemetry never gates).
func TestSession_FrameRouter_TrulyStrayFrameNotRouted(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})
	rec := &frameRouterRecorder{}
	installFrameRouter(t, session, rec.handler())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	transport.feedMessage(t, `{"type":"system","subtype":"some_unhandled_telemetry","session_id":"sess-1"}`)
	// Give the reader time to process; the router must not be invoked.
	time.Sleep(200 * time.Millisecond)
	if n := rec.count(); n != 0 {
		t.Fatalf("router was invoked %d times for a stray frame, want 0", n)
	}
	if session.InTurn() {
		t.Error("stray frame opened a turn (InTurn=true), want false")
	}
}

// TestSession_FrameRouter_SprawlTurn_TagsNonAutonomous: a sprawl-initiated turn
// routes frames with Autonomous=false, AND the subscriber channel still
// receives them (both paths fire; the runtime router early-returns for sprawl
// turns so it does not double-emit).
func TestSession_FrameRouter_SprawlTurn_TagsNonAutonomous(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})
	rec := &frameRouterRecorder{}
	installFrameRouter(t, session, rec.handler())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		sent := <-transport.sendCh // initialize control_request
		feedInitResponse(t, transport, sent)
		<-transport.sendCh // user-prompt send
		transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
		transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`)
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	}()

	if err := session.Initialize(ctx, InitSpec{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	events, err := session.StartTurn(ctx, "do work")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}

	var subCount int
	for range events {
		subCount++
	}
	if subCount == 0 {
		t.Fatal("sprawl subscriber received no frames")
	}

	// Both paths fire: the router must have seen the same frames as the
	// subscriber (the router is invoked per frame before the subscriber send),
	// all tagged non-autonomous.
	rec.waitForCount(t, subCount, 2*time.Second)
	frames := rec.snapshot()
	if len(frames) != subCount {
		t.Errorf("router saw %d frames, subscriber saw %d — both paths must see all frames", len(frames), subCount)
	}
	for i, f := range frames {
		if f.turn.Autonomous {
			t.Errorf("frame[%d] Autonomous=true for a sprawl turn, want false", i)
		}
	}
}

// TestSession_FrameRouter_SprawlTurn_ReplayFrameDoesNotChoke: a sprawl turn
// whose stream includes a user+isReplay frame (QUM-814) must route and complete
// without faulting (Slice-1 minimal — no outstanding-map yet).
func TestSession_FrameRouter_SprawlTurn_ReplayFrameDoesNotChoke(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})
	rec := &frameRouterRecorder{}
	installFrameRouter(t, session, rec.handler())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		sent := <-transport.sendCh
		feedInitResponse(t, transport, sent)
		<-transport.sendCh
		transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
		transport.feedMessage(t, `{"type":"user","uuid":"u-replay-1","session_id":"sess-1","isReplay":true,"message":{"role":"user","content":"queued prompt"}}`)
		transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`)
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	}()

	if err := session.Initialize(ctx, InitSpec{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	events, err := session.StartTurn(ctx, "do work")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	var sawReplay bool
	for msg := range events {
		if msg.Type == "user" {
			sawReplay = true
		}
	}
	if !sawReplay {
		t.Error("subscriber never saw the isReplay user frame")
	}
	if err := session.LastTurnError(); err != nil {
		t.Errorf("LastTurnError = %v, want nil (isReplay frame must not fault)", err)
	}
}

// TestSession_FrameRouter_AutonomousOrphan_NotifiesEndOnTeardown: an autonomous
// turn opened (init) but never closed (no result) must, on session Close,
// notify the router with EndOfTurn=true so the runtime can revert InTurn.
func TestSession_FrameRouter_AutonomousOrphan_NotifiesEndOnTeardown(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})
	rec := &frameRouterRecorder{}
	installFrameRouter(t, session, rec.handler())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	rec.waitForCount(t, 1, 2*time.Second)

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Expect a terminal EndOfTurn router invocation during teardown.
	deadline := time.After(2 * time.Second)
	for {
		sawEnd := false
		for _, f := range rec.snapshot() {
			if f.turn.EndOfTurn {
				sawEnd = true
			}
		}
		if sawEnd {
			// The consequence of the teardown notification: the turn is gone.
			if session.InTurn() {
				t.Error("session still InTurn after orphan teardown")
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("router never received an EndOfTurn invocation on orphan teardown")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// bgTasksWireFrame is a verbatim background_tasks_changed frame, one task
// outstanding, copied from a real session log.
const bgTasksWireFrame = `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"bsspuvme4","task_type":"local_bash","description":"Run make validate"}],"session_id":"sess-1"}`

// TestSession_FrameRouter_BackgroundTasksChangedRoutedBetweenTurns (QUM-1197
// item 2): background_tasks_changed carries the agent's outstanding background
// work, and the reap predicate consumes it from routeFrame. 104 of 1,615
// recorded frames — 66 of them DRAIN frames (tasks:[]) — arrive while
// s.currentTurn == nil, and were dropped here as stray telemetry. A consumer
// that never sees the drain frame takes its set non-empty and never clears it,
// making the agent permanently unreclaimable — a failure that looks like a
// working term.
//
// Routed like the QUM-867 compaction-status and QUM-634 task_notification
// pre-init frames: delivered immediately with PreInit=true, so it is
// publish-only and cannot allocate a turnFrame or gate StartTurn (QUM-570).
//
// Direction: MUST FIRE. The negative control is
// TestSession_FrameRouter_TrulyStrayFrameNotRouted above, which pins that this
// gate is not simply routing everything; it stays green across this change.
func TestSession_FrameRouter_BackgroundTasksChangedRoutedBetweenTurns(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})
	rec := &frameRouterRecorder{}
	installFrameRouter(t, session, rec.handler())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// No init first: this is the between-turns case, where currentTurn is nil.
	transport.feedMessage(t, bgTasksWireFrame)
	rec.waitForCount(t, 1, 2*time.Second)
	frames := rec.snapshot()
	if len(frames) != 1 {
		t.Fatalf("router saw %d frames for a between-turns background_tasks_changed, want 1", len(frames))
	}
	if frames[0].msg.Subtype != protocol.SubtypeBackgroundTasksChanged {
		t.Errorf("routed subtype = %q, want %q", frames[0].msg.Subtype, protocol.SubtypeBackgroundTasksChanged)
	}
	if !frames[0].turn.PreInit {
		t.Error("routed with PreInit=false; without it routeFrame would treat this as a turn frame and could open one")
	}
	if frames[0].msg.Raw == nil {
		t.Error("routed frame has nil Raw; protocol.ParseAs cannot read it, so the set would never be observed")
	}
	if session.InTurn() {
		t.Error("background_tasks_changed opened a turn (InTurn=true), want false; it must never gate StartTurn")
	}
}

// TestSession_FrameRouter_BackgroundTasksChangedMidTurn_IsNotPreInit is the
// other half: with a turn open the frame already routed today, and it must keep
// doing so as an ORDINARY turn frame. Direction: MUST FIRE with PreInit=false —
// an implementation that stamped PreInit unconditionally would make every
// mid-turn frame publish-only and change routeFrame's existing behaviour.
func TestSession_FrameRouter_BackgroundTasksChangedMidTurn_IsNotPreInit(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})
	rec := &frameRouterRecorder{}
	installFrameRouter(t, session, rec.handler())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	rec.waitForCount(t, 1, 2*time.Second)
	transport.feedMessage(t, bgTasksWireFrame)
	rec.waitForCount(t, 2, 2*time.Second)

	frames := rec.snapshot()
	last := frames[len(frames)-1]
	if last.msg.Subtype != protocol.SubtypeBackgroundTasksChanged {
		t.Fatalf("last routed frame = %s/%s, want the background_tasks_changed frame", last.msg.Type, last.msg.Subtype)
	}
	if last.turn.PreInit {
		t.Error("mid-turn background_tasks_changed routed with PreInit=true; it belongs to the open turn")
	}
}
