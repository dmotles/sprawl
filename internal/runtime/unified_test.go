package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	livenesspkg "github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// Liveness-state literals used throughout these tests, mapping the legacy
// RuntimeState enum onto its liveness.State equivalent (QUM-626 M5 fold):
//
//	liveIdle         -> Running (non-autonomous)
//	liveTurnActive   -> Running·autonomous-turn
//	liveInterrupting -> Stopping
//	liveStopped      -> Stopped
var (
	liveIdle    = livenesspkg.State{Liveness: livenesspkg.Running}
	liveStopped = livenesspkg.State{Liveness: livenesspkg.Stopped}
)

// mockUnifiedSession is a SessionHandle test double scoped to the unified
// runtime tests. It is independent of mockSession in turnloop_test.go so
// these tests can evolve separately.
type mockUnifiedSession struct {
	mu             sync.Mutex
	writes         []protocol.UserMessage
	interruptCalls int32
	// cancelResults maps a uuid to the {cancelled} value CancelAsyncMessage
	// should return for it. Absent uuids return false. (QUM-824)
	cancelResults map[string]bool
	// cancelCalls records, in order, the uuids passed to CancelAsyncMessage.
	cancelCalls []string
	// cancelHook, if set, runs inside CancelAsyncMessage before recording.
	cancelHook func(uuid string)
}

// WriteUserMessage records the written stdin user message (QUM-817 — replaces
// the old StartTurn drive path).
func (m *mockUnifiedSession) WriteUserMessage(_ context.Context, msg protocol.UserMessage) error {
	m.mu.Lock()
	m.writes = append(m.writes, msg)
	m.mu.Unlock()
	return nil
}

func (m *mockUnifiedSession) Interrupt(_ context.Context) error {
	atomic.AddInt32(&m.interruptCalls, 1)
	return nil
}

// CancelAsyncMessage simulates the CLI cancel_async_message ack (QUM-824). It
// returns cancelResults[uuid] (default false), records the call, and invokes the
// optional cancelHook inside the call (used to prove Recall does not hold outMu
// across the session call).
func (m *mockUnifiedSession) CancelAsyncMessage(_ context.Context, uuid string) (bool, error) {
	if m.cancelHook != nil {
		m.cancelHook(uuid)
	}
	m.mu.Lock()
	m.cancelCalls = append(m.cancelCalls, uuid)
	res := m.cancelResults[uuid]
	m.mu.Unlock()
	return res, nil
}

func (m *mockUnifiedSession) cancelledUUIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.cancelCalls...)
}

func (m *mockUnifiedSession) interruptCount() int {
	return int(atomic.LoadInt32(&m.interruptCalls))
}

func (m *mockUnifiedSession) writeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.writes)
}

func (m *mockUnifiedSession) lastWrite() (protocol.UserMessage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.writes) == 0 {
		return protocol.UserMessage{}, false
	}
	return m.writes[len(m.writes)-1], true
}

// mockUnifiedSessionWithID adds a SessionID() method so we can test the
// unexported sessionIDProvider type-assertion path.
type mockUnifiedSessionWithID struct {
	mockUnifiedSession
	id string
}

func (m *mockUnifiedSessionWithID) SessionID() string { return m.id }

// waitForState polls rt.State() up to d, returning true on match.
func waitForState(t *testing.T, rt *UnifiedRuntime, want livenesspkg.State, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if rt.State() == want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestNew_Defaults(t *testing.T) {
	rt := New(RuntimeConfig{Name: "agent-1", Session: &mockUnifiedSession{}})
	if rt.EventBus() == nil {
		t.Errorf("EventBus() = nil, want non-nil")
	}
	if got := rt.State(); got != liveIdle {
		t.Errorf("State() = %v, want liveIdle", got)
	}
	if rt.Outstanding() == nil {
		t.Errorf("Outstanding() = nil, want non-nil empty map")
	}
}

func TestStart_Stop_Lifecycle(t *testing.T) {
	rt := New(RuntimeConfig{Name: "x", Session: &mockUnifiedSession{}})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := rt.State(); got != liveStopped {
		t.Errorf("State() after Stop = %v, want liveStopped", got)
	}
}

func TestStart_Twice_Errors(t *testing.T) {
	rt := New(RuntimeConfig{Name: "x", Session: &mockUnifiedSession{}})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()
	if err := rt.Start(context.Background()); err == nil {
		t.Error("second Start did not error")
	}
}

func TestStop_WithoutStart_NoOp(t *testing.T) {
	rt := New(RuntimeConfig{Name: "x", Session: &mockUnifiedSession{}})
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop without Start: %v", err)
	}
	if got := rt.State(); got != liveStopped {
		t.Errorf("State() = %v, want liveStopped", got)
	}
}

// TestInitialPrompt_WrittenToStdin: the spawn prompt is written to the CLI
// stdin as a `next` user message on Start (QUM-817), not enqueued.
func TestInitialPrompt_WrittenToStdin(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock, InitialPrompt: "do the thing"})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && mock.writeCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	um, ok := mock.lastWrite()
	if !ok {
		t.Fatal("InitialPrompt was not written to stdin")
	}
	if um.Message.Content != "do the thing" || um.Priority != "next" {
		t.Errorf("initial write = %+v, want content='do the thing' priority=next", um)
	}
}

// TestWriteUserPrompt_RecordsPendingUser: a human prompt write records a
// pending kind:user outstanding entry and writes to stdin (QUM-817).
func TestWriteUserPrompt_RecordsPendingUser(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})
	uuid, err := rt.WriteUserPrompt(context.Background(), "hello", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	if uuid == "" {
		t.Fatal("WriteUserPrompt returned empty uuid")
	}
	out := rt.Outstanding()
	e, ok := out[uuid]
	if !ok {
		t.Fatalf("uuid %q not in outstanding map", uuid)
	}
	if e.kind != kindUser || e.state != statePending {
		t.Errorf("entry = {kind:%v state:%v}, want {user pending}", e.kind, e.state)
	}
	um, ok := mock.lastWrite()
	if !ok || um.UUID != uuid || um.Priority != "next" || um.Message.Content != "hello" {
		t.Errorf("stdin write = %+v, want uuid=%s priority=next content=hello", um, uuid)
	}
}

// TestWriteSystemMessage_RecordsPendingSystem mirrors the above for kind:system.
func TestWriteSystemMessage_RecordsPendingSystem(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})
	uuid, err := rt.WriteSystemMessage(context.Background(), "<system-notification type=\"system\">hi</system-notification>", "next", []string{"e1"})
	if err != nil {
		t.Fatalf("WriteSystemMessage: %v", err)
	}
	e, ok := rt.Outstanding()[uuid]
	if !ok || e.kind != kindSystem || e.state != statePending {
		t.Errorf("entry = %+v ok=%v, want {system pending}", e, ok)
	}
}

func TestSessionID_FromProvider(t *testing.T) {
	rt := New(RuntimeConfig{Name: "x", Session: &mockUnifiedSessionWithID{id: "abc-123"}})
	if got := rt.SessionID(); got != "abc-123" {
		t.Errorf("SessionID() = %q, want %q", got, "abc-123")
	}
}

func TestSessionID_WithoutProvider(t *testing.T) {
	rt := New(RuntimeConfig{Name: "x", Session: &mockUnifiedSession{}})
	if got := rt.SessionID(); got != "" {
		t.Errorf("SessionID() = %q, want \"\"", got)
	}
}

func TestRuntimeConfig_CapabilitiesPlumbed(t *testing.T) {
	caps := backend.Capabilities{SupportsInterrupt: true, SupportsResume: true, SupportsToolBridge: true}
	rt := New(RuntimeConfig{Name: "x", Session: &mockUnifiedSession{}, Capabilities: caps})
	if got := rt.Capabilities(); got != caps {
		t.Errorf("Capabilities() = %+v, want %+v", got, caps)
	}
}

func TestDone_ClosedAfterStop(t *testing.T) {
	rt := New(RuntimeConfig{Name: "x", Session: &mockUnifiedSession{}})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = rt.Stop(context.Background())
	select {
	case <-rt.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done not closed after Stop")
	}
}

func TestDone_ClosedWithoutStart(t *testing.T) {
	rt := New(RuntimeConfig{Name: "x", Session: &mockUnifiedSession{}})
	_ = rt.Stop(context.Background())
	select {
	case <-rt.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed after Stop-without-Start")
	}
}

func TestStop_Idle_CallsSessionInterruptOnce(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = rt.Stop(context.Background())
	if got := mock.interruptCount(); got != 1 {
		t.Errorf("Session.Interrupt count = %d, want 1 (polite stop)", got)
	}
}

func TestStop_WithoutStart_NoSessionInterrupt(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})
	_ = rt.Stop(context.Background())
	if got := mock.interruptCount(); got != 0 {
		t.Errorf("Session.Interrupt count = %d, want 0", got)
	}
}

func TestInterrupt_WhenIdle_ForwardsToSessionAndEmitsSynthetic(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})
	ch, unsub := rt.EventBus().SubscribeNamed("int", 8)
	defer unsub()
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if got := mock.interruptCount(); got < 1 {
		t.Errorf("Session.Interrupt count = %d, want >= 1", got)
	}
	// QUM-775: idle interrupt emits a synthetic EventInterrupted.
	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == EventInterrupted {
				return
			}
		case <-deadline:
			t.Fatal("idle Interrupt did not emit synthetic EventInterrupted")
		}
	}
}

// TestInterrupt_CarriesNoContent pins the QUM-821 invariant: the bare interrupt
// frame (Esc-abort) must NEVER carry content. rt.Interrupt forwards a contentless
// control frame to the session and must not write any stdin user message. Urgent
// content delivery rides priority:"now" user messages, not the interrupt frame.
func TestInterrupt_CarriesNoContent(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if got := mock.interruptCount(); got < 1 {
		t.Errorf("Session.Interrupt count = %d, want >= 1 (interrupt frame must be forwarded)", got)
	}
	if got := mock.writeCount(); got != 0 {
		last, _ := mock.lastWrite()
		t.Errorf("stdin writes during Interrupt = %d, want 0 (bare interrupt must carry no content); last=%+v", got, last)
	}
}

func TestInterrupt_WhenStopped_NoOp(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})
	_ = rt.Start(context.Background())
	_ = rt.Stop(context.Background())
	before := mock.interruptCount()
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if got := mock.interruptCount(); got != before {
		t.Errorf("Session.Interrupt count = %d, want %d (no-op when stopped)", got, before)
	}
}
