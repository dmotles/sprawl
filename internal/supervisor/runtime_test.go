package supervisor

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/state"
)

type runtimeTestSession struct {
	sessionID                   string
	caps                        backendpkg.Capabilities
	interrupts                  int
	wakes                       int
	wakeForDeliveryCalls        int
	forceInterruptDeliveryCalls int
	stopCalls                   int
	doneCh                      chan struct{}
}

func (s *runtimeTestSession) Initialize(context.Context, backendpkg.InitSpec) error { return nil }
func (s *runtimeTestSession) StartTurn(context.Context, string, ...backendpkg.TurnSpec) (<-chan *protocol.Message, error) {
	ch := make(chan *protocol.Message)
	close(ch)
	return ch, nil
}

func (s *runtimeTestSession) Interrupt(context.Context) error {
	s.interrupts++
	return nil
}

func (s *runtimeTestSession) Wake() error {
	s.wakes++
	return nil
}

func (s *runtimeTestSession) WakeForDelivery() error {
	s.wakeForDeliveryCalls++
	return nil
}

func (s *runtimeTestSession) ForceInterruptDelivery() error {
	s.forceInterruptDeliveryCalls++
	return nil
}

func (s *runtimeTestSession) Stop(context.Context) error {
	s.stopCalls++
	return nil
}
func (s *runtimeTestSession) Close() error                          { return nil }
func (s *runtimeTestSession) Wait() error                           { return nil }
func (s *runtimeTestSession) Kill() error                           { return nil }
func (s *runtimeTestSession) LastTurnError() error                  { return io.EOF }
func (s *runtimeTestSession) SessionID() string                     { return s.sessionID }
func (s *runtimeTestSession) Capabilities() backendpkg.Capabilities { return s.caps }
func (s *runtimeTestSession) Done() <-chan struct{}                 { return s.doneCh }

type runtimeTestStarter struct {
	mu      sync.Mutex
	specs   []RuntimeStartSpec
	session RuntimeHandle
	err     error
}

func (s *runtimeTestStarter) Start(_ context.Context, spec RuntimeStartSpec) (RuntimeHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.specs = append(s.specs, spec)
	if s.err != nil {
		return nil, s.err
	}
	return s.session, nil
}

func testAgentState(name string) *state.AgentState {
	return &state.AgentState{
		Name:      name,
		Type:      "engineer",
		Family:    "engineering",
		Parent:    "weave",
		Branch:    "dmotles/" + name,
		Worktree:  "/repo/.sprawl/worktrees/" + name,
		Status:    "active",
		CreatedAt: "2026-04-28T00:00:00Z",
		SessionID: "sess-" + name,
		TreePath:  "weave/" + name,
	}
}

func nextRuntimeEventKinds(t *testing.T, ch <-chan RuntimeEvent, count int) []RuntimeEventKind {
	t.Helper()
	kinds := make([]RuntimeEventKind, 0, count)
	deadline := time.After(2 * time.Second)
	for len(kinds) < count {
		select {
		case ev := <-ch:
			kinds = append(kinds, ev.Kind)
		case <-deadline:
			t.Fatalf("timed out waiting for %d runtime events; got %v", count, kinds)
		}
	}
	return kinds
}

func TestAgentRuntime_SnapshotSeedsFromAgentState(t *testing.T) {
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("alice"),
	})

	snap := rt.Snapshot()
	if snap.Name != "alice" {
		t.Fatalf("Name = %q, want alice", snap.Name)
	}
	if snap.Worktree != "/repo/.sprawl/worktrees/alice" {
		t.Fatalf("Worktree = %q", snap.Worktree)
	}
	if snap.SessionID != "sess-alice" {
		t.Fatalf("SessionID = %q", snap.SessionID)
	}
	if snap.Lifecycle != RuntimeLifecycleRegistered {
		t.Fatalf("Lifecycle = %q, want %q", snap.Lifecycle, RuntimeLifecycleRegistered)
	}
	if snap.QueueDepth != 0 {
		t.Fatalf("QueueDepth = %d, want 0", snap.QueueDepth)
	}
}

func TestAgentRuntime_StartInterruptQueueAndSyncEmitSnapshotsWithoutTmux(t *testing.T) {
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps: backendpkg.Capabilities{
			SupportsInterrupt: true,
			SupportsResume:    true,
		},
	}
	starter := &runtimeTestStarter{session: session}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})

	events, cancel := rt.Subscribe(16)
	defer cancel()

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt() error: %v", err)
	}

	rt.RecordQueuedTask()

	updated := testAgentState("alice")
	updated.LastReportState = "working"
	updated.LastReportMessage = "writing tests"
	updated.LastReportDetail = "red phase"
	rt.SyncAgentState(updated)

	gotKinds := nextRuntimeEventKinds(t, events, 4)
	wantKinds := []RuntimeEventKind{
		RuntimeEventStarted,
		RuntimeEventInterrupted,
		RuntimeEventTaskQueued,
		RuntimeEventStateSynced,
	}
	for i, want := range wantKinds {
		if gotKinds[i] != want {
			t.Fatalf("event[%d] = %q, want %q (all=%v)", i, gotKinds[i], want, gotKinds)
		}
	}

	if len(starter.specs) != 1 {
		t.Fatalf("starter specs = %d, want 1", len(starter.specs))
	}
	if starter.specs[0].Name != "alice" {
		t.Fatalf("starter spec name = %q", starter.specs[0].Name)
	}
	if starter.specs[0].Worktree != "/repo/.sprawl/worktrees/alice" {
		t.Fatalf("starter spec worktree = %q", starter.specs[0].Worktree)
	}
	if session.interrupts != 1 {
		t.Fatalf("interrupts = %d, want 1", session.interrupts)
	}

	snap := rt.Snapshot()
	if snap.Lifecycle != RuntimeLifecycleStarted {
		t.Fatalf("Lifecycle = %q, want %q", snap.Lifecycle, RuntimeLifecycleStarted)
	}
	if snap.QueueDepth != 1 {
		t.Fatalf("QueueDepth = %d, want 1", snap.QueueDepth)
	}
	if snap.InterruptCount != 1 {
		t.Fatalf("InterruptCount = %d, want 1", snap.InterruptCount)
	}
	if snap.LastReport.State != "working" {
		t.Fatalf("LastReport.State = %q", snap.LastReport.State)
	}
	if snap.LastReport.Message != "writing tests" {
		t.Fatalf("LastReport.Message = %q", snap.LastReport.Message)
	}
}

func TestRuntimeRegistry_EnsureDeduplicatesAndHandlesConcurrentAccess(t *testing.T) {
	registry := NewRuntimeRegistry()
	agentState := testAgentState("alice")

	var wg sync.WaitGroup
	results := make([]*AgentRuntime, 32)
	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = registry.Ensure(AgentRuntimeConfig{
				SprawlRoot: "/repo",
				Agent:      agentState,
			})
		}(i)
	}
	wg.Wait()

	first := results[0]
	if first == nil {
		t.Fatal("first runtime is nil")
	}
	for i, rt := range results {
		if rt != first {
			t.Fatalf("results[%d] returned a different runtime pointer", i)
		}
	}

	got, ok := registry.Get("alice")
	if !ok {
		t.Fatal("Get(alice) = missing, want present")
	}
	if got != first {
		t.Fatal("Get(alice) returned a different runtime pointer")
	}

	runtimes := registry.List()
	if len(runtimes) != 1 {
		t.Fatalf("List() len = %d, want 1", len(runtimes))
	}
	if runtimes[0] != first {
		t.Fatal("List()[0] returned a different runtime pointer")
	}
}

func TestAgentRuntime_UnexpectedHandleExitMarksStopped(t *testing.T) {
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps: backendpkg.Capabilities{
			SupportsInterrupt: true,
			SupportsResume:    true,
		},
		doneCh: make(chan struct{}),
	}
	starter := &runtimeTestStarter{session: session}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	close(session.doneCh)

	deadline := time.After(2 * time.Second)
	for {
		if snap := rt.Snapshot(); snap.Lifecycle == RuntimeLifecycleStopped {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("Lifecycle = %q, want %q", rt.Snapshot().Lifecycle, RuntimeLifecycleStopped)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// fakeAttachHandle is a minimal RuntimeHandle for AttachHandle tests.
type fakeAttachHandle struct {
	caps      backendpkg.Capabilities
	sessionID string
	doneCh    chan struct{}
}

func (h *fakeAttachHandle) Interrupt(context.Context) error       { return nil }
func (h *fakeAttachHandle) Wake() error                           { return nil }
func (h *fakeAttachHandle) WakeForDelivery() error                { return nil }
func (h *fakeAttachHandle) ForceInterruptDelivery() error         { return nil }
func (h *fakeAttachHandle) Stop(context.Context) error            { return nil }
func (h *fakeAttachHandle) SessionID() string                     { return h.sessionID }
func (h *fakeAttachHandle) Capabilities() backendpkg.Capabilities { return h.caps }
func (h *fakeAttachHandle) Done() <-chan struct{}                 { return h.doneCh }

// QUM-399: AttachHandle is the no-starter analog of Start. It marks the
// runtime as Started and captures the handle's capabilities + sessionID.
func TestAgentRuntime_AttachHandle_SetsLifecycleStartedAndCapabilities(t *testing.T) {
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("alice"),
	})
	if rt.Snapshot().Lifecycle != RuntimeLifecycleRegistered {
		t.Fatalf("pre-attach Lifecycle = %q, want %q", rt.Snapshot().Lifecycle, RuntimeLifecycleRegistered)
	}

	h := &fakeAttachHandle{
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		sessionID: "sess-attached",
	}
	rt.AttachHandle(h)

	snap := rt.Snapshot()
	if snap.Lifecycle != RuntimeLifecycleStarted {
		t.Errorf("Lifecycle = %q, want %q", snap.Lifecycle, RuntimeLifecycleStarted)
	}
	if !snap.Capabilities.SupportsInterrupt {
		t.Errorf("Capabilities.SupportsInterrupt = false, want true")
	}
	if snap.SessionID != "sess-attached" {
		t.Errorf("SessionID = %q, want sess-attached", snap.SessionID)
	}
}

func TestAgentRuntime_AttachHandle_EmitsStartedEvent(t *testing.T) {
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("alice"),
	})
	events, cancel := rt.Subscribe(4)
	defer cancel()

	rt.AttachHandle(&fakeAttachHandle{})

	kinds := nextRuntimeEventKinds(t, events, 1)
	if kinds[0] != RuntimeEventStarted {
		t.Errorf("event kind = %q, want %q", kinds[0], RuntimeEventStarted)
	}
}

// QUM-399: AttachHandle must wire a Done() watcher when the handle exposes
// one, so that an unexpected exit transitions the runtime lifecycle to
// Stopped (mirrors the Start() path's handle.Done watcher).
func TestAgentRuntime_AttachHandle_WatchesHandleDone_TransitionsToStopped(t *testing.T) {
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("alice"),
	})

	doneCh := make(chan struct{})
	h := &fakeAttachHandle{doneCh: doneCh}
	rt.AttachHandle(h)
	if rt.Snapshot().Lifecycle != RuntimeLifecycleStarted {
		t.Fatalf("after AttachHandle Lifecycle = %q, want %q",
			rt.Snapshot().Lifecycle, RuntimeLifecycleStarted)
	}

	close(doneCh)

	deadline := time.After(2 * time.Second)
	for {
		if rt.Snapshot().Lifecycle == RuntimeLifecycleStopped {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("Lifecycle = %q, want %q after handle.Done close",
				rt.Snapshot().Lifecycle, RuntimeLifecycleStopped)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestAgentRuntime_CancelSubscriptionStopsDeliveryWithoutClosingChannel(t *testing.T) {
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("alice"),
	})

	events, cancel := rt.Subscribe(1)
	cancel()
	rt.RecordQueuedTask()

	select {
	case _, ok := <-events:
		if !ok {
			t.Fatal("canceled subscription channel should remain open")
		}
		t.Fatal("canceled subscription should not receive events")
	case <-time.After(100 * time.Millisecond):
	}
}
