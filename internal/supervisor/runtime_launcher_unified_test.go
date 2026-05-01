package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	backend "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/inboxprompt"
	"github.com/dmotles/sprawl/internal/protocol"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
)

// fakeBackendSession is a minimal backend.Session double for the unified
// starter tests. It records Initialize/StartTurn/Close/Wait calls so tests can
// assert wiring without a real subprocess.
type fakeBackendSession struct {
	id   string
	caps backend.Capabilities

	mu          sync.Mutex
	initCalled  bool
	initSpec    backend.InitSpec
	closeCalls  int
	waitCalls   int
	startCalls  int
	interrupted int32
}

func newFakeBackendSession(id string, caps backend.Capabilities) *fakeBackendSession {
	return &fakeBackendSession{id: id, caps: caps}
}

func (f *fakeBackendSession) Initialize(_ context.Context, spec backend.InitSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initCalled = true
	f.initSpec = spec
	return nil
}

func (f *fakeBackendSession) StartTurn(_ context.Context, _ string, _ ...backend.TurnSpec) (<-chan *protocol.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	ch := make(chan *protocol.Message)
	close(ch)
	return ch, nil
}

func (f *fakeBackendSession) Interrupt(context.Context) error {
	atomic.AddInt32(&f.interrupted, 1)
	return nil
}

func (f *fakeBackendSession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	return nil
}

func (f *fakeBackendSession) Wait() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitCalls++
	return nil
}

func (f *fakeBackendSession) Kill() error                        { return nil }
func (f *fakeBackendSession) LastTurnError() error               { return nil }
func (f *fakeBackendSession) SessionID() string                  { return f.id }
func (f *fakeBackendSession) Capabilities() backend.Capabilities { return f.caps }

// recordingObserver collects OnMessage calls for the activity subscriber tests.
type recordingObserver struct {
	mu   sync.Mutex
	msgs []*protocol.Message
}

func (r *recordingObserver) OnMessage(msg *protocol.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, msg)
}

func (r *recordingObserver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs)
}

// writeAgentState seeds an AgentState file under sprawlRoot so LoadAgent
// inside the unified starter resolves it.
func writeAgentState(t *testing.T, sprawlRoot string, st *state.AgentState) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(sprawlRoot, ".sprawl", "agents", st.Name), 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	if err := state.SaveAgent(sprawlRoot, st); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Dispatcher selection (env flag)
// ---------------------------------------------------------------------------

func TestNewRuntimeStarter_EnvFlagDispatch(t *testing.T) {
	old := unifiedRuntimeEnabled
	defer func() { unifiedRuntimeEnabled = old }()

	unifiedRuntimeEnabled = func() bool { return true }
	starter := newRuntimeStarter(backend.InitSpec{}, nil)
	if _, ok := starter.(*inProcessUnifiedStarter); !ok {
		t.Fatalf("flag=true: starter type = %T, want *inProcessUnifiedStarter", starter)
	}

	unifiedRuntimeEnabled = func() bool { return false }
	starter = newRuntimeStarter(backend.InitSpec{}, nil)
	if _, ok := starter.(*inProcessRuntimeStarter); !ok {
		t.Fatalf("flag=false: starter type = %T, want *inProcessRuntimeStarter", starter)
	}
}

// ---------------------------------------------------------------------------
// QUM-408 regression guard: BuildAgentSessionSpec reuse
// ---------------------------------------------------------------------------

// TestInProcessUnifiedStarter_Start_ReuseBuildAgentSessionSpec ensures that
// the unified starter feeds the backend adapter a SessionSpec produced by
// agentloop.BuildAgentSessionSpec. The load-bearing assertion is that
// engineer agents carry agent.TDDSubAgentsJSON() via spec.Agents and
// researchers do not. See QUM-408.
func TestInProcessUnifiedStarter_Start_ReuseBuildAgentSessionSpec(t *testing.T) {
	cases := []struct {
		name       string
		agentType  string
		wantAgents string
	}{
		{"engineer carries TDD sub-agents", "engineer", agent.TDDSubAgentsJSON()},
		{"researcher omits sub-agents", "researcher", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldStart := unifiedAdapterStartFn
			oldNew := unifiedRuntimeNewFn
			defer func() {
				unifiedAdapterStartFn = oldStart
				unifiedRuntimeNewFn = oldNew
			}()

			sprawlRoot := t.TempDir()
			worktree := filepath.Join(sprawlRoot, "wt")
			if err := os.MkdirAll(worktree, 0o755); err != nil {
				t.Fatalf("mkdir worktree: %v", err)
			}
			writeAgentState(t, sprawlRoot, &state.AgentState{
				Name:      "alice",
				Type:      tc.agentType,
				Worktree:  worktree,
				Branch:    "feat/x",
				Prompt:    "do the thing",
				SessionID: "sess-alice",
			})

			var captured backend.SessionSpec
			fakeSession := newFakeBackendSession("sess-alice", backend.Capabilities{})
			unifiedAdapterStartFn = func(_ context.Context, spec backend.SessionSpec) (backend.Session, error) {
				captured = spec
				return fakeSession, nil
			}
			// Stub the runtime constructor so we don't actually launch a turn loop.
			unifiedRuntimeNewFn = runtimepkg.New

			starter := newInProcessUnifiedStarter(backend.InitSpec{}, nil)
			handle, err := starter.Start(context.Background(), RuntimeStartSpec{
				Name:       "alice",
				Worktree:   worktree,
				SprawlRoot: sprawlRoot,
				SessionID:  "sess-alice",
				TreePath:   "weave/alice",
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer func() { _ = handle.Stop(context.Background()) }()

			if got := captured.Agents; got != tc.wantAgents {
				t.Fatalf("captured SessionSpec.Agents = %q, want %q", got, tc.wantAgents)
			}
			if captured.Identity != "alice" {
				t.Errorf("Identity = %q, want \"alice\"", captured.Identity)
			}
			if captured.SessionID != "sess-alice" {
				t.Errorf("SessionID = %q, want \"sess-alice\"", captured.SessionID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Initialize-on-bridge / skip-on-no-bridge
// ---------------------------------------------------------------------------

func TestInProcessUnifiedStarter_Start_CallsSessionInitializeWhenBridgePresent(t *testing.T) {
	oldStart := unifiedAdapterStartFn
	oldNew := unifiedRuntimeNewFn
	defer func() {
		unifiedAdapterStartFn = oldStart
		unifiedRuntimeNewFn = oldNew
	}()

	sprawlRoot := t.TempDir()
	worktree := filepath.Join(sprawlRoot, "wt")
	_ = os.MkdirAll(worktree, 0o755)
	writeAgentState(t, sprawlRoot, &state.AgentState{
		Name: "alice", Type: "researcher", Worktree: worktree, SessionID: "sess-alice",
	})

	fakeSession := newFakeBackendSession("sess-alice", backend.Capabilities{})
	unifiedAdapterStartFn = func(_ context.Context, _ backend.SessionSpec) (backend.Session, error) {
		return fakeSession, nil
	}
	unifiedRuntimeNewFn = runtimepkg.New

	bridge := dummyBridge{}
	initSpec := backend.InitSpec{
		MCPServerNames: []string{"sprawl"},
		ToolBridge:     bridge,
	}
	starter := newInProcessUnifiedStarter(initSpec, []string{"mcp__sprawl__spawn"})
	handle, err := starter.Start(context.Background(), RuntimeStartSpec{
		Name: "alice", Worktree: worktree, SprawlRoot: sprawlRoot,
		SessionID: "sess-alice", TreePath: "weave/alice",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = handle.Stop(context.Background()) }()

	fakeSession.mu.Lock()
	called := fakeSession.initCalled
	gotSpec := fakeSession.initSpec
	fakeSession.mu.Unlock()

	if !called {
		t.Fatal("session.Initialize was not called when ToolBridge is present")
	}
	if len(gotSpec.MCPServerNames) == 0 || gotSpec.MCPServerNames[0] != "sprawl" {
		t.Errorf("Initialize spec.MCPServerNames = %v, want [\"sprawl\"]", gotSpec.MCPServerNames)
	}
	if gotSpec.ToolBridge == nil {
		t.Error("Initialize spec.ToolBridge is nil; want non-nil")
	}
}

func TestInProcessUnifiedStarter_Start_SkipsInitializeWhenNoBridge(t *testing.T) {
	oldStart := unifiedAdapterStartFn
	oldNew := unifiedRuntimeNewFn
	defer func() {
		unifiedAdapterStartFn = oldStart
		unifiedRuntimeNewFn = oldNew
	}()

	sprawlRoot := t.TempDir()
	worktree := filepath.Join(sprawlRoot, "wt")
	_ = os.MkdirAll(worktree, 0o755)
	writeAgentState(t, sprawlRoot, &state.AgentState{
		Name: "alice", Type: "researcher", Worktree: worktree, SessionID: "sess-alice",
	})

	fakeSession := newFakeBackendSession("sess-alice", backend.Capabilities{})
	unifiedAdapterStartFn = func(_ context.Context, _ backend.SessionSpec) (backend.Session, error) {
		return fakeSession, nil
	}
	unifiedRuntimeNewFn = runtimepkg.New

	starter := newInProcessUnifiedStarter(backend.InitSpec{}, nil)
	handle, err := starter.Start(context.Background(), RuntimeStartSpec{
		Name: "alice", Worktree: worktree, SprawlRoot: sprawlRoot,
		SessionID: "sess-alice", TreePath: "weave/alice",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = handle.Stop(context.Background()) }()

	fakeSession.mu.Lock()
	called := fakeSession.initCalled
	fakeSession.mu.Unlock()

	if called {
		t.Fatal("session.Initialize was called even though ToolBridge and MCPServerNames are absent")
	}
}

// dummyBridge satisfies backend.ToolBridge.
type dummyBridge struct{}

func (dummyBridge) HandleIncoming(_ context.Context, _ string, msg json.RawMessage) (json.RawMessage, error) {
	_ = msg
	return json.RawMessage(`{}`), nil
}

// Compile-time guard that dummyBridge satisfies backend.ToolBridge.
var _ backend.ToolBridge = dummyBridge{}

// ---------------------------------------------------------------------------
// unifiedHandle behavior
// ---------------------------------------------------------------------------

// buildStartedUnifiedHandleForTest spins up an inProcessUnifiedStarter with
// stubs and returns the resulting *unifiedHandle, the fake backend session,
// and the sprawlRoot (so tests can seed pending queue entries on disk via
// agentloop.Enqueue before calling InterruptDelivery — see QUM-437).
func buildStartedUnifiedHandleForTest(t *testing.T, caps backend.Capabilities) (*unifiedHandle, *fakeBackendSession, string) {
	t.Helper()
	oldStart := unifiedAdapterStartFn
	oldNew := unifiedRuntimeNewFn
	t.Cleanup(func() {
		unifiedAdapterStartFn = oldStart
		unifiedRuntimeNewFn = oldNew
	})

	sprawlRoot := t.TempDir()
	worktree := filepath.Join(sprawlRoot, "wt")
	_ = os.MkdirAll(worktree, 0o755)
	writeAgentState(t, sprawlRoot, &state.AgentState{
		Name: "alice", Type: "researcher", Worktree: worktree, SessionID: "sess-alice",
	})

	fakeSession := newFakeBackendSession("sess-alice", caps)
	unifiedAdapterStartFn = func(_ context.Context, _ backend.SessionSpec) (backend.Session, error) {
		return fakeSession, nil
	}
	unifiedRuntimeNewFn = runtimepkg.New

	starter := newInProcessUnifiedStarter(backend.InitSpec{}, nil)
	handle, err := starter.Start(context.Background(), RuntimeStartSpec{
		Name: "alice", Worktree: worktree, SprawlRoot: sprawlRoot,
		SessionID: "sess-alice", TreePath: "weave/alice",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	uh, ok := handle.(*unifiedHandle)
	if !ok {
		t.Fatalf("handle type = %T, want *unifiedHandle", handle)
	}
	return uh, fakeSession, sprawlRoot
}

func TestUnifiedHandle_DelegatesToRuntime(t *testing.T) {
	caps := backend.Capabilities{SupportsInterrupt: true}
	uh, fakeSession, _ := buildStartedUnifiedHandleForTest(t, caps)
	defer func() { _ = uh.Stop(context.Background()) }()

	if got := uh.Capabilities(); got != caps {
		t.Errorf("Capabilities() = %+v, want %+v", got, caps)
	}
	if got := uh.SessionID(); got != "sess-alice" {
		t.Errorf("SessionID() = %q, want \"sess-alice\"", got)
	}

	// Wake should not error and should poke the runtime queue signal.
	if err := uh.Wake(); err != nil {
		t.Errorf("Wake: %v", err)
	}

	// Interrupt should return nil and propagate to the underlying backend
	// session (mirrors the mockUnifiedSession.interruptCount pattern from
	// internal/runtime/unified_test.go).
	// QUM-435: under the new contract, unifiedHandle.Interrupt is a single delegated call;
	// the +1 is satisfied by rt.Interrupt's unconditional session forward.
	beforeInterrupts := atomic.LoadInt32(&fakeSession.interrupted)
	if err := uh.Interrupt(context.Background()); err != nil {
		t.Errorf("Interrupt: %v", err)
	}
	if got := atomic.LoadInt32(&fakeSession.interrupted); got != beforeInterrupts+1 {
		t.Errorf("session.Interrupt call count = %d, want %d (Interrupt did not propagate)", got, beforeInterrupts+1)
	}

	// InterruptDelivery enqueue/no-enqueue contract is exercised by the
	// dedicated TestUnifiedHandle_InterruptDelivery_* tests below (QUM-437);
	// this test no longer asserts that behavior.
}

func TestUnifiedHandle_StopClosesDone(t *testing.T) {
	uh, _, _ := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})

	if err := uh.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-uh.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done did not close within 2s of Stop")
	}

	// Idempotency.
	if err := uh.Stop(context.Background()); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestUnifiedHandle_StopUnsubscribesEventBus(t *testing.T) {
	before := runtime.NumGoroutine()

	uh, _, _ := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	if err := uh.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Allow scheduling slack for goroutines to finish.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("goroutine count = %d, want close to baseline %d (likely leaked subscriber)", runtime.NumGoroutine(), before)
}

// ---------------------------------------------------------------------------
// Activity subscriber goroutine
// ---------------------------------------------------------------------------

// TestActivitySubscriber_WritesToObserver verifies the helper that the new
// unified starter uses to forward EventProtocolMessage events to a
// backend.Observer (typically the ObserverWriter that writes activity.ndjson).
// It also verifies that non-message events are skipped and that the helper's
// stop function unblocks the goroutine.
func TestActivitySubscriber_WritesToObserver(t *testing.T) {
	bus := runtimepkg.NewEventBus()
	obs := &recordingObserver{}

	stop := runActivitySubscriber(bus, obs)

	// EventProtocolMessage with a message should be forwarded.
	msg := &protocol.Message{Type: "assistant"}
	bus.Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventProtocolMessage, Message: msg})

	// Non-message events should not be forwarded.
	bus.Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnStarted})
	bus.Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventQueueDrained})

	// Wait until the observer sees exactly one message.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && obs.count() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := obs.count(); got != 1 {
		t.Fatalf("observer.count = %d, want 1 (only EventProtocolMessage should forward)", got)
	}

	stop()
}

// ---------------------------------------------------------------------------
// QUM-437: InterruptDelivery enqueues a real inbox/interrupt prompt
// ---------------------------------------------------------------------------
//
// Under the new contract InterruptDelivery reads pending/ off disk and
// formats a real prompt via inboxprompt.Build{Queue,Interrupt}FlushPrompt
// instead of the old "You have new messages" stub. Async entries become a
// single ClassInbox QueueItem; interrupt entries become a single
// ClassInterrupt QueueItem; an empty pending dir enqueues nothing.

func TestUnifiedHandle_InterruptDelivery_EnqueuesRealAsyncPrompt(t *testing.T) {
	uh, _, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	e1, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-async-1", ShortID: "sa1", Class: agentloop.ClassAsync,
		From: "child-alpha", Subject: "status", Body: "all green",
		Tags: []string{"fyi"},
	})
	if err != nil {
		t.Fatalf("Enqueue async: %v", err)
	}

	if err := uh.InterruptDelivery(); err != nil {
		t.Fatalf("InterruptDelivery: %v", err)
	}

	// synchronous enqueue contract: InterruptDelivery must enqueue before it
	// returns. The poll loop is defensive in case an implementation defers it.
	items := drainQueueWithDeadline(uh, 1, 50*time.Millisecond)
	if len(items) != 1 {
		t.Fatalf("queue items = %d, want 1; items=%+v", len(items), items)
	}
	got := items[0]
	if got.Class != runtimepkg.ClassInbox {
		t.Errorf("Class = %q, want %q", got.Class, runtimepkg.ClassInbox)
	}
	want := inboxprompt.BuildQueueFlushPrompt([]inboxprompt.Entry{
		{
			Seq: e1.Seq, ID: e1.ID, ShortID: e1.ShortID, Class: inboxprompt.ClassAsync,
			From: e1.From, Subject: e1.Subject, Body: e1.Body, Tags: e1.Tags,
			EnqueuedAt: e1.EnqueuedAt,
		},
	})
	if got.Prompt != want {
		t.Errorf("Prompt mismatch.\n--- got ---\n%s\n--- want ---\n%s", got.Prompt, want)
	}
	if len(got.EntryIDs) != 1 || got.EntryIDs[0] != e1.ID {
		t.Errorf("EntryIDs = %v, want [%q]", got.EntryIDs, e1.ID)
	}
}

func TestUnifiedHandle_InterruptDelivery_EnqueuesRealInterruptPrompt(t *testing.T) {
	uh, _, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	e1, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-int-1", ShortID: "si1", Class: agentloop.ClassInterrupt,
		From: "weave", Subject: "stop", Body: "reprioritize",
		Tags: []string{"resume_hint:writing tests"},
	})
	if err != nil {
		t.Fatalf("Enqueue interrupt: %v", err)
	}

	if err := uh.InterruptDelivery(); err != nil {
		t.Fatalf("InterruptDelivery: %v", err)
	}

	// synchronous enqueue contract: InterruptDelivery must enqueue before it
	// returns. The poll loop is defensive in case an implementation defers it.
	items := drainQueueWithDeadline(uh, 1, 50*time.Millisecond)
	if len(items) != 1 {
		t.Fatalf("queue items = %d, want 1; items=%+v", len(items), items)
	}
	got := items[0]
	if got.Class != runtimepkg.ClassInterrupt {
		t.Errorf("Class = %q, want %q", got.Class, runtimepkg.ClassInterrupt)
	}
	want := inboxprompt.BuildInterruptFlushPrompt([]inboxprompt.Entry{
		{
			Seq: e1.Seq, ID: e1.ID, ShortID: e1.ShortID, Class: inboxprompt.ClassInterrupt,
			From: e1.From, Subject: e1.Subject, Body: e1.Body, Tags: e1.Tags,
			EnqueuedAt: e1.EnqueuedAt,
		},
	})
	if got.Prompt != want {
		t.Errorf("Prompt mismatch.\n--- got ---\n%s\n--- want ---\n%s", got.Prompt, want)
	}
	if len(got.EntryIDs) != 1 || got.EntryIDs[0] != e1.ID {
		t.Errorf("EntryIDs = %v, want [%q]", got.EntryIDs, e1.ID)
	}
}

func TestUnifiedHandle_InterruptDelivery_EmptyPendingNoEnqueue(t *testing.T) {
	uh, _, _ := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	if err := uh.InterruptDelivery(); err != nil {
		t.Fatalf("InterruptDelivery: %v", err)
	}

	items := uh.rt.Queue().DrainAll()
	if len(items) != 0 {
		t.Errorf("queue items = %d, want 0 (empty pending must not enqueue a stub); items=%+v", len(items), items)
	}
}

func TestUnifiedHandle_InterruptDelivery_SeparatesInterruptAndAsync(t *testing.T) {
	uh, _, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	asyncEntry, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-async-1", ShortID: "sa1", Class: agentloop.ClassAsync,
		From: "child-alpha", Subject: "status", Body: "all green",
	})
	if err != nil {
		t.Fatalf("Enqueue async: %v", err)
	}
	intEntry, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-int-1", ShortID: "si1", Class: agentloop.ClassInterrupt,
		From: "weave", Subject: "stop", Body: "reprioritize",
	})
	if err != nil {
		t.Fatalf("Enqueue interrupt: %v", err)
	}

	if err := uh.InterruptDelivery(); err != nil {
		t.Fatalf("InterruptDelivery: %v", err)
	}

	// synchronous enqueue contract: InterruptDelivery must enqueue before it
	// returns. The poll loop is defensive in case an implementation defers it.
	items := drainQueueWithDeadline(uh, 2, 50*time.Millisecond)
	if len(items) != 2 {
		t.Fatalf("queue items = %d, want 2 (one interrupt + one inbox); items=%+v", len(items), items)
	}
	// DrainAll sorts by class priority (interrupt before inbox).
	if items[0].Class != runtimepkg.ClassInterrupt {
		t.Errorf("items[0].Class = %q, want %q (interrupt must sort first)", items[0].Class, runtimepkg.ClassInterrupt)
	}
	if items[1].Class != runtimepkg.ClassInbox {
		t.Errorf("items[1].Class = %q, want %q", items[1].Class, runtimepkg.ClassInbox)
	}

	wantInterrupt := inboxprompt.BuildInterruptFlushPrompt([]inboxprompt.Entry{
		{
			Seq: intEntry.Seq, ID: intEntry.ID, ShortID: intEntry.ShortID,
			Class: inboxprompt.ClassInterrupt, From: intEntry.From,
			Subject: intEntry.Subject, Body: intEntry.Body, Tags: intEntry.Tags,
			EnqueuedAt: intEntry.EnqueuedAt,
		},
	})
	if items[0].Prompt != wantInterrupt {
		t.Errorf("interrupt Prompt mismatch.\n--- got ---\n%s\n--- want ---\n%s", items[0].Prompt, wantInterrupt)
	}
	if len(items[0].EntryIDs) != 1 || items[0].EntryIDs[0] != intEntry.ID {
		t.Errorf("interrupt EntryIDs = %v, want [%q]", items[0].EntryIDs, intEntry.ID)
	}

	wantAsync := inboxprompt.BuildQueueFlushPrompt([]inboxprompt.Entry{
		{
			Seq: asyncEntry.Seq, ID: asyncEntry.ID, ShortID: asyncEntry.ShortID,
			Class: inboxprompt.ClassAsync, From: asyncEntry.From,
			Subject: asyncEntry.Subject, Body: asyncEntry.Body, Tags: asyncEntry.Tags,
			EnqueuedAt: asyncEntry.EnqueuedAt,
		},
	})
	if items[1].Prompt != wantAsync {
		t.Errorf("async Prompt mismatch.\n--- got ---\n%s\n--- want ---\n%s", items[1].Prompt, wantAsync)
	}
	if len(items[1].EntryIDs) != 1 || items[1].EntryIDs[0] != asyncEntry.ID {
		t.Errorf("async EntryIDs = %v, want [%q]", items[1].EntryIDs, asyncEntry.ID)
	}
}

// drainQueueWithDeadline is a defensive poll-loop wrapper around
// uh.rt.Queue().DrainAll() that retries until at least `want` items are
// observed or the deadline elapses. The unified-runtime InterruptDelivery
// contract is synchronous (it must enqueue before returning), but this
// helper protects the test suite from races if a future implementation
// defers the enqueue. Returns whatever was drained at deadline expiry.
func drainQueueWithDeadline(uh *unifiedHandle, want int, timeout time.Duration) []runtimepkg.QueueItem {
	deadline := time.Now().Add(timeout)
	var items []runtimepkg.QueueItem
	for time.Now().Before(deadline) {
		items = append(items, uh.rt.Queue().DrainAll()...)
		if len(items) >= want {
			return items
		}
		time.Sleep(2 * time.Millisecond)
	}
	// final drain after deadline in case the loop missed a late publish
	items = append(items, uh.rt.Queue().DrainAll()...)
	return items
}

// TestUnifiedHandle_InterruptDelivery_TruncatesOversizedBody pins the
// supervisor-level truncation path: an async entry whose body exceeds the
// per-message cap must surface in the synthesized inbox prompt as a
// truncated payload citing the entry's ShortID for the read-hint. Guards
// against the supervisor path bypassing inboxprompt's size guards.
func TestUnifiedHandle_InterruptDelivery_TruncatesOversizedBody(t *testing.T) {
	uh, _, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	body := strings.Repeat("z", inboxprompt.MaxQueueFlushBodyBytes+200)
	if _, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-async-big", ShortID: "sa1", Class: agentloop.ClassAsync,
		From: "peer", Subject: "big", Body: body,
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := uh.InterruptDelivery(); err != nil {
		t.Fatalf("InterruptDelivery: %v", err)
	}

	items := drainQueueWithDeadline(uh, 1, 50*time.Millisecond)
	var inbox *runtimepkg.QueueItem
	for i := range items {
		if items[i].Class == runtimepkg.ClassInbox {
			inbox = &items[i]
			break
		}
	}
	if inbox == nil {
		t.Fatalf("no ClassInbox item found; items=%+v", items)
	}
	if !strings.Contains(inbox.Prompt, "truncated") {
		t.Errorf("prompt missing 'truncated' marker; prompt=%q", inbox.Prompt)
	}
	if !strings.Contains(inbox.Prompt, "sprawl messages read sa1") {
		t.Errorf("prompt missing read-hint citing ShortID 'sa1'; prompt=%q", inbox.Prompt)
	}
}
