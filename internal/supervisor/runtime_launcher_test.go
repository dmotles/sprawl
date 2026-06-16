package supervisor

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	killCalls   int
	startCalls  int
	teardown    []string // ordered record of "close"/"kill"/"wait" calls (QUM-543)
	interrupted int32
	writes      []protocol.UserMessage                    // QUM-817: stdin user messages written
	router      func(*protocol.Message, backend.TurnInfo) // QUM-817: captured frame router

	// waitBlock, when non-nil, makes Wait() block until the channel is closed
	// (or until the test cleanup closes it). Used by the QUM-542 bounded-wait
	// regression test to simulate a child whose stdout pipe never drains after
	// SIGKILL (stuck Task subshell holding the FD open).
	waitBlock chan struct{}
}

func newFakeBackendSession(id string, caps backend.Capabilities) *fakeBackendSession {
	return &fakeBackendSession{id: id, caps: caps}
}

func (f *fakeBackendSession) Start(context.Context) error { return nil }

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
	// QUM-579: emit a minimal result frame before closing so the TurnLoop's
	// OnQueueItemDelivered callback (which now fires on the first non-
	// system:init frame, not on StartTurn return) actually runs in tests.
	res := protocol.ResultMessage{Type: "result", Subtype: "success"}
	raw, _ := json.Marshal(res)
	ch := make(chan *protocol.Message, 1)
	ch <- &protocol.Message{Type: "result", Subtype: "success", Raw: raw}
	close(ch)
	return ch, nil
}

func (f *fakeBackendSession) WriteUserMessage(_ context.Context, msg protocol.UserMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, msg)
	return nil
}

// SetFrameRouter captures the runtime's frame router (QUM-815/817) so tests can
// synthesize the CLI's isReplay consumption ack via echoReplay.
func (f *fakeBackendSession) SetFrameRouter(h func(*protocol.Message, backend.TurnInfo)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.router = h
}

// writesSnapshot returns a copy of the stdin user messages written so far.
func (f *fakeBackendSession) writesSnapshot() []protocol.UserMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]protocol.UserMessage, len(f.writes))
	copy(out, f.writes)
	return out
}

// waitForWrites blocks until at least n stdin writes are recorded or timeout
// elapses, returning a snapshot of the writes at return time.
func (f *fakeBackendSession) waitForWrites(n int, timeout time.Duration) []protocol.UserMessage {
	deadline := time.Now().Add(timeout)
	for {
		w := f.writesSnapshot()
		if len(w) >= n || time.Now().After(deadline) {
			return w
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// echoReplay simulates the CLI echoing a previously-written user message back as
// an isReplay frame — the consumption ack that drives the runtime's
// markConsumed → OnDelivered(entryIDs) path (QUM-817).
func (f *fakeBackendSession) echoReplay(uuid string) {
	f.mu.Lock()
	h := f.router
	f.mu.Unlock()
	if h == nil {
		return
	}
	raw := []byte(`{"type":"user","uuid":"` + uuid + `","isReplay":true}`)
	h(&protocol.Message{Type: "user", UUID: uuid, Raw: raw}, backend.TurnInfo{Replay: true})
}

// echoAllReplays echoes the isReplay ack for every recorded stdin write,
// confirming consumption of all written messages.
func (f *fakeBackendSession) echoAllReplays() {
	for _, w := range f.writesSnapshot() {
		f.echoReplay(w.UUID)
	}
}

func (f *fakeBackendSession) Interrupt(context.Context) error {
	atomic.AddInt32(&f.interrupted, 1)
	return nil
}

func (f *fakeBackendSession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	f.teardown = append(f.teardown, "close")
	return nil
}

func (f *fakeBackendSession) Wait() error {
	f.mu.Lock()
	f.waitCalls++
	f.teardown = append(f.teardown, "wait")
	block := f.waitBlock
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return nil
}

func (f *fakeBackendSession) Kill() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killCalls++
	f.teardown = append(f.teardown, "kill")
	return nil
}

func (f *fakeBackendSession) LastTurnError() error               { return nil }
func (f *fakeBackendSession) SessionID() string                  { return f.id }
func (f *fakeBackendSession) Capabilities() backend.Capabilities { return f.caps }
func (f *fakeBackendSession) InTurn() bool                       { return false }
func (f *fakeBackendSession) BackendStats() backend.Stats        { return backend.Stats{} }
func (f *fakeBackendSession) IsTerminallyFaulted() bool          { return false }
func (f *fakeBackendSession) InduceTerminalFault(_ error)        {}

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
// QUM-408 regression guard: BuildAgentSessionSpec reuse
// ---------------------------------------------------------------------------

// TestInProcessUnifiedStarter_Start_ReuseBuildAgentSessionSpec ensures that
// the unified starter feeds the backend adapter a SessionSpec produced by
// agentloop.BuildAgentSessionSpec. The load-bearing assertion is that the
// captured spec carries the agent identity/session, regardless of type.
// The `--agents` plumbing was removed end-to-end in QUM-716 (#4.5); the
// historical engineer-only injection is no longer relevant.
func TestInProcessUnifiedStarter_Start_ReuseBuildAgentSessionSpec(t *testing.T) {
	cases := []struct {
		name      string
		agentType string
	}{
		{"engineer", "engineer"},
		{"researcher", "researcher"},
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
			handle, err := starter.Start(RuntimeStartSpec{
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
	handle, err := starter.Start(RuntimeStartSpec{
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
	handle, err := starter.Start(RuntimeStartSpec{
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
// stubs and returns the resulting *unifiedHandle, the fake backend session
// (which records the stdin user messages the handle writes — QUM-817), and the
// sprawlRoot (so tests can seed pending queue entries on disk via
// agentloop.Enqueue before calling WakeForDelivery — see QUM-437). Tests assert
// delivery by inspecting fakeSession.writes and drive the consumption ack via
// fakeSession.echoReplay.
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
	handle, err := starter.Start(RuntimeStartSpec{
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
	// dedicated TestUnifiedHandle_WakeForDelivery_* tests below (QUM-437);
	// this test no longer asserts that behavior.
}

// TestUnifiedHandle_StopKillsSession is a QUM-543 regression guard: the
// emergency-stop path (Supervisor.Kill → handle.Stop) must SIGKILL the
// backend subprocess, not just Close its stdin pipe and Wait. Without
// session.Kill(), claude mid-turn ignores stdin EOF and the process
// survives — yet handle.Stop returns success, so mcp__sprawl__kill lies
// to the caller. See WeaveRuntimeHandle.Stop (weave_handle.go) for the
// already-correct pattern this mirrors.
func TestUnifiedHandle_StopKillsSession(t *testing.T) {
	uh, fakeSession, _ := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})

	if err := uh.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	fakeSession.mu.Lock()
	killCalls := fakeSession.killCalls
	closeCalls := fakeSession.closeCalls
	waitCalls := fakeSession.waitCalls
	order := append([]string(nil), fakeSession.teardown...)
	fakeSession.mu.Unlock()

	if killCalls != 1 {
		t.Errorf("session.Kill call count = %d, want 1 (Stop did not SIGKILL the backend subprocess)", killCalls)
	}
	if closeCalls != 1 {
		t.Errorf("session.Close call count = %d, want 1", closeCalls)
	}
	if waitCalls != 1 {
		t.Errorf("session.Wait call count = %d, want 1", waitCalls)
	}
	// Order must be Close → Kill → Wait so that Wait reaps the SIGKILLed
	// process and never blocks on a claude that ignores stdin EOF.
	want := []string{"close", "kill", "wait"}
	if len(order) != len(want) {
		t.Fatalf("teardown order = %v, want %v", order, want)
	}
	for i, op := range want {
		if order[i] != op {
			t.Errorf("teardown[%d] = %q, want %q (full order: %v)", i, order[i], op, order)
		}
	}
}

// TestUnifiedHandle_Stop_BoundsWedgedStopActivity is a QUM-547 regression
// guard: if the activity subscriber goroutine wedges (e.g. parked inside
// obs.OnMessage writing to a stuck NFS-backed activityFile), unifiedHandle.Stop
// must NOT hang on `<-doneCh` inside stopActivity. The bounded join logs and
// abandons after stopActivityTimeout.
func TestUnifiedHandle_Stop_BoundsWedgedStopActivity(t *testing.T) {
	uh, _, _ := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})

	block := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	uh.stopActivity = func() {
		<-block
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- uh.Stop(context.Background()) }()

	bound := 3 * stopActivityTimeout
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Stop returned err = %v, want nil", err)
		}
		if elapsed := time.Since(start); elapsed > bound {
			t.Errorf("Stop returned in %v, want <= %v (stopActivity wedge must be bounded)", elapsed, bound)
		}
	case <-time.After(bound):
		t.Fatalf("Stop wedged > %v on wedged stopActivity (QUM-547: join is unbounded)", bound)
	}
}

// TestUnifiedHandle_Stop_BoundsWedgedActivityClose is the activityFile.Close()
// counterpart: if the underlying close() syscall hangs (stuck FD / NFS),
// unifiedHandle.Stop must NOT hang. The activityClose seam lets the test
// inject a wedged closer.
func TestUnifiedHandle_Stop_BoundsWedgedActivityClose(t *testing.T) {
	uh, _, _ := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})

	block := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	uh.activityClose = func() error {
		<-block
		return nil
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- uh.Stop(context.Background()) }()

	bound := 3 * activityCloseTimeout
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Stop returned err = %v, want nil", err)
		}
		if elapsed := time.Since(start); elapsed > bound {
			t.Errorf("Stop returned in %v, want <= %v (activityClose wedge must be bounded)", elapsed, bound)
		}
	case <-time.After(bound):
		t.Fatalf("Stop wedged > %v on wedged activityClose (QUM-547: close is unbounded)", bound)
	}
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

// TestUnifiedHandle_Stop_BoundedWhenSessionWaitWedges is the QUM-542 regression
// guard. Before the fix, unifiedHandle.Stop called session.Wait() synchronously
// after SIGKILL, which can block indefinitely when a child Claude Code Task
// subshell holds the stdout pipe FD open. That made retire (Real.Retire →
// runtime.Stop → handle.Stop → session.Wait) hang for 30+ minutes, never
// emitting `retire.preflight` for the JSONL call log.
//
// After the fix, Stop bounds the post-Kill Wait via select+timeout so a stuck
// pipe-drain cannot wedge retire. SIGKILL still fires (QUM-543), the OS reaps
// the zombie eventually, and Stop returns within seconds.
func TestUnifiedHandle_Stop_BoundedWhenSessionWaitWedges(t *testing.T) {
	uh, fakeSession, _ := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})

	// Arm Wait() to block forever. Ensure cleanup unblocks it so the goroutine
	// the handle leaves behind isn't a permanent test leak.
	block := make(chan struct{})
	fakeSession.mu.Lock()
	fakeSession.waitBlock = block
	fakeSession.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- uh.Stop(context.Background())
	}()

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop returned error: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("unifiedHandle.Stop wedged on session.Wait — bounded timeout did not fire (QUM-542 regression)")
	}

	// QUM-543: SIGKILL must have fired even though Wait is wedged.
	fakeSession.mu.Lock()
	gotKills := fakeSession.killCalls
	fakeSession.mu.Unlock()
	if gotKills < 1 {
		t.Errorf("session.Kill calls = %d, want >= 1 (SIGKILL escalation must precede the bounded Wait)", gotKills)
	}

	// Done() must close so callers observing runtime lifecycle don't wedge.
	select {
	case <-uh.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("uh.Done() did not close after Stop returned")
	}
}

// TestUnifiedHandle_StopWaitTimedOut_FalseOnCleanStop is the QUM-546 happy-path
// guard: when session.Wait returns promptly, the bounded-wait timeout did NOT
// fire and unifiedHandle.StopWaitTimedOut must report false. This flag flows
// up through AgentRuntime.StopWaitTimedOut into Real.Retire/Real.Kill's
// `runtime-stop-done` checkpoint emission as the `wait_timeout` kv field, so
// false-on-clean is the load-bearing default that lets call-log readers
// distinguish "stop completed" from "stop abandoned".
func TestUnifiedHandle_StopWaitTimedOut_FalseOnCleanStop(t *testing.T) {
	uh, _, _ := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})

	if err := uh.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := uh.StopWaitTimedOut(); got {
		t.Errorf("StopWaitTimedOut() = true, want false after clean Stop")
	}
}

// TestUnifiedHandle_StopWaitTimedOut_TrueOnTimeout is the QUM-546 sad-path
// guard: when the post-SIGKILL session.Wait wedges beyond
// unifiedHandleStopWaitTimeout, the bounded-wait timer fires and the handle
// must record that fact so the `wait_timeout` kv field on the
// `retire.runtime-stop-done` / `kill.runtime-stop-done` checkpoint is true.
// Mirrors TestUnifiedHandle_Stop_BoundedWhenSessionWaitWedges (QUM-542) for
// the timeout-detection seam.
func TestUnifiedHandle_StopWaitTimedOut_TrueOnTimeout(t *testing.T) {
	uh, fakeSession, _ := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})

	block := make(chan struct{})
	fakeSession.mu.Lock()
	fakeSession.waitBlock = block
	fakeSession.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- uh.Stop(context.Background())
	}()

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop returned error: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("unifiedHandle.Stop wedged on session.Wait (QUM-542 bounded-wait failure)")
	}

	if got := uh.StopWaitTimedOut(); !got {
		t.Errorf("StopWaitTimedOut() = false, want true after wedged session.Wait")
	}
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

	stop := runActivitySubscriber(bus, obs, "activity")

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

// TestActivitySubscriber_NamePropagatesToDroppedCounts asserts that the name
// argument passed to runActivitySubscriber is propagated to the EventBus, so
// drop telemetry surfaces an actionable subscriber label rather than a
// synthetic "#N" key. (QUM-482)
func TestActivitySubscriber_NamePropagatesToDroppedCounts(t *testing.T) {
	bus := runtimepkg.NewEventBus()
	obs := &recordingObserver{}

	stop := runActivitySubscriber(bus, obs, "activity")
	defer stop()

	counts := bus.DroppedCounts()
	if _, ok := counts["activity"]; !ok {
		t.Fatalf("DroppedCounts() = %v, want key %q", counts, "activity")
	}
}

// ---------------------------------------------------------------------------
// QUM-437: InterruptDelivery enqueues a real inbox/interrupt prompt
// ---------------------------------------------------------------------------
//
// QUM-817: WakeForDelivery reads pending/ off disk and writes a real prompt
// (via inboxprompt.Build{Queue,Interrupt}FlushPrompt) straight to the CLI stdin
// with priority `next` instead of enqueueing a Go-side QueueItem. Async and
// interrupt entries are written as separate stdin messages; an empty pending
// dir writes nothing. Delivery is confirmed when the CLI echoes each write back
// as an isReplay frame (driven here via fakeSession.echoReplay), which flips
// the on-disk entry to delivered.

func TestUnifiedHandle_WakeForDelivery_WritesRealAsyncPromptNext(t *testing.T) {
	uh, fakeSession, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	e1, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-async-1", ShortID: "sa1", Class: agentloop.ClassAsync,
		From: "child-alpha", Subject: "status", Body: "all green",
		Tags: []string{"fyi"},
	})
	if err != nil {
		t.Fatalf("Enqueue async: %v", err)
	}

	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	writes := fakeSession.waitForWrites(1, 2*time.Second)
	if len(writes) != 1 {
		t.Fatalf("stdin writes = %d, want 1; writes=%+v", len(writes), writes)
	}
	got := writes[0]
	if got.Priority != "next" {
		t.Errorf("Priority = %q, want %q", got.Priority, "next")
	}
	want := inboxprompt.BuildQueueFlushPrompt([]inboxprompt.Entry{
		{
			Seq: e1.Seq, ID: e1.ID, ShortID: e1.ShortID, Class: inboxprompt.ClassAsync,
			From: e1.From, Subject: e1.Subject, Body: e1.Body, Tags: e1.Tags,
			EnqueuedAt: e1.EnqueuedAt,
		},
	})
	if got.Message.Content != want {
		t.Errorf("Content mismatch.\n--- got ---\n%s\n--- want ---\n%s", got.Message.Content, want)
	}

	// The isReplay echo confirms consumption and flips the entry to delivered.
	fakeSession.echoReplay(got.UUID)
	if pending, _ := agentloop.ListPending(sprawlRoot, "alice"); len(pending) != 0 {
		t.Errorf("pending after replay echo = %d, want 0 (entry must be marked delivered)", len(pending))
	}
}

// TestUnifiedHandle_WakeForDelivery_WritesRealInterruptPromptNow pins the
// QUM-821 urgency tier: an interrupt-class inbox entry (send_message(
// interrupt=true)) drains to stdin with priority "now" (cancel-and-replace),
// not "next". The content still rides the message; no bare interrupt frame is
// issued for delivery.
func TestUnifiedHandle_WakeForDelivery_WritesRealInterruptPromptNow(t *testing.T) {
	uh, fakeSession, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	e1, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-int-1", ShortID: "si1", Class: agentloop.ClassInterrupt,
		From: "weave", Subject: "stop", Body: "reprioritize",
		Tags: []string{"resume_hint:writing tests"},
	})
	if err != nil {
		t.Fatalf("Enqueue interrupt: %v", err)
	}

	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	writes := fakeSession.waitForWrites(1, 2*time.Second)
	if len(writes) != 1 {
		t.Fatalf("stdin writes = %d, want 1; writes=%+v", len(writes), writes)
	}
	got := writes[0]
	if got.Priority != "now" {
		t.Errorf("Priority = %q, want %q (QUM-821 interrupt-class urgency)", got.Priority, "now")
	}
	want := inboxprompt.BuildInterruptFlushPrompt([]inboxprompt.Entry{
		{
			Seq: e1.Seq, ID: e1.ID, ShortID: e1.ShortID, Class: inboxprompt.ClassInterrupt,
			From: e1.From, Subject: e1.Subject, Body: e1.Body, Tags: e1.Tags,
			EnqueuedAt: e1.EnqueuedAt,
		},
	})
	if got.Message.Content != want {
		t.Errorf("Content mismatch.\n--- got ---\n%s\n--- want ---\n%s", got.Message.Content, want)
	}

	// QUM-821: urgency is carried entirely by priority "now" — delivery must NOT
	// issue a bare contentless interrupt frame (that is reserved for Esc-abort).
	if got := atomic.LoadInt32(&fakeSession.interrupted); got != 0 {
		t.Errorf("session.Interrupt called %d times during now-priority delivery; want 0 (bare interrupt is Esc-only)", got)
	}

	// QUM-821 storm-prevention: now-priority (cancel-and-replace) writes are NOT
	// echoed via --replay-user-messages, so the entry must be marked delivered
	// synchronously on the write — WITHOUT an isReplay echo. Otherwise the entry
	// stays pending and PostTurnSweep re-injects it every turn (stdin storm).
	if pending, _ := agentloop.ListPending(sprawlRoot, "alice"); len(pending) != 0 {
		t.Errorf("pending after now-priority write (no echo) = %d, want 0 (must be marked delivered on write — storm prevention)", len(pending))
	}
	if delivered, _ := agentloop.ListDelivered(sprawlRoot, "alice"); len(delivered) != 1 {
		t.Errorf("delivered after now-priority write = %d, want 1", len(delivered))
	}
}

func TestUnifiedHandle_WakeForDelivery_EmptyPendingNoWrite(t *testing.T) {
	uh, fakeSession, _ := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	if writes := fakeSession.writesSnapshot(); len(writes) != 0 {
		t.Errorf("stdin writes = %d, want 0 (empty pending must not write a stub); writes=%+v", len(writes), writes)
	}
}

func TestUnifiedHandle_WakeForDelivery_SeparatesInterruptAndAsync(t *testing.T) {
	uh, fakeSession, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
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

	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	writes := fakeSession.waitForWrites(2, 2*time.Second)
	if len(writes) != 2 {
		t.Fatalf("stdin writes = %d, want 2 (one interrupt + one async); writes=%+v", len(writes), writes)
	}
	// drainPendingToStdin writes interrupts before asyncs (QUM-817). QUM-821:
	// the interrupt batch drains at priority "now"; the async batch stays "next".
	if writes[0].Priority != "now" {
		t.Errorf("interrupt batch Priority = %q, want \"now\" (QUM-821)", writes[0].Priority)
	}
	if writes[1].Priority != "next" {
		t.Errorf("async batch Priority = %q, want \"next\"", writes[1].Priority)
	}

	wantInterrupt := inboxprompt.BuildInterruptFlushPrompt([]inboxprompt.Entry{
		{
			Seq: intEntry.Seq, ID: intEntry.ID, ShortID: intEntry.ShortID,
			Class: inboxprompt.ClassInterrupt, From: intEntry.From,
			Subject: intEntry.Subject, Body: intEntry.Body, Tags: intEntry.Tags,
			EnqueuedAt: intEntry.EnqueuedAt,
		},
	})
	if writes[0].Message.Content != wantInterrupt {
		t.Errorf("interrupt Content mismatch.\n--- got ---\n%s\n--- want ---\n%s", writes[0].Message.Content, wantInterrupt)
	}

	wantAsync := inboxprompt.BuildQueueFlushPrompt([]inboxprompt.Entry{
		{
			Seq: asyncEntry.Seq, ID: asyncEntry.ID, ShortID: asyncEntry.ShortID,
			Class: inboxprompt.ClassAsync, From: asyncEntry.From,
			Subject: asyncEntry.Subject, Body: asyncEntry.Body, Tags: asyncEntry.Tags,
			EnqueuedAt: asyncEntry.EnqueuedAt,
		},
	})
	if writes[1].Message.Content != wantAsync {
		t.Errorf("async Content mismatch.\n--- got ---\n%s\n--- want ---\n%s", writes[1].Message.Content, wantAsync)
	}

	// QUM-821: the interrupt (now) entry was delivered on write; only the async
	// entry awaits its isReplay echo. Echo just the async write (writes[1]).
	fakeSession.echoReplay(writes[1].UUID)
	if pending, _ := agentloop.ListPending(sprawlRoot, "alice"); len(pending) != 0 {
		t.Errorf("pending after async replay echo = %d, want 0", len(pending))
	}
}

// ---------------------------------------------------------------------------
// QUM-441/817/821: WakeForDelivery wires MarkDelivered. Async-class entries are
// marked delivered on their isReplay consumption ack (the write alone must NOT
// mark them). Interrupt-class (now-priority) entries are marked delivered on the
// write itself, because cancel-and-replace injection yields no isReplay echo —
// deferring to an echo that never comes would leave the entry pending forever
// and PostTurnSweep would re-inject it every turn (QUM-821 storm).
// ---------------------------------------------------------------------------

// TestUnifiedHandle_WakeForDelivery_MarksPendingDelivered verifies the split:
// the async entry waits for its isReplay ack, while the interrupt (now) entry is
// delivered synchronously on the write (QUM-441/817/821).
func TestUnifiedHandle_WakeForDelivery_MarksPendingDelivered(t *testing.T) {
	uh, fakeSession, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	// Seed two pending entries — one async, one interrupt — so WakeForDelivery
	// writes two stdin messages (one per class).
	if _, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-async-1", ShortID: "sa1", Class: agentloop.ClassAsync,
		From: "child-alpha", Subject: "status", Body: "all green",
	}); err != nil {
		t.Fatalf("Enqueue async: %v", err)
	}
	if _, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-int-1", ShortID: "si1", Class: agentloop.ClassInterrupt,
		From: "weave", Subject: "stop", Body: "reprioritize",
	}); err != nil {
		t.Fatalf("Enqueue interrupt: %v", err)
	}

	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	// Two stdin writes (one per class).
	if writes := fakeSession.waitForWrites(2, 2*time.Second); len(writes) != 2 {
		t.Fatalf("stdin writes = %d, want 2; writes=%+v", len(writes), writes)
	}

	// QUM-821: before any echo, the interrupt (now) entry is ALREADY delivered
	// (marked on write); the async entry is still pending awaiting its echo.
	pendingBefore, _ := agentloop.ListPending(sprawlRoot, "alice")
	deliveredBefore, _ := agentloop.ListDelivered(sprawlRoot, "alice")
	if len(pendingBefore) != 1 || pendingBefore[0].ID != "id-async-1" {
		t.Fatalf("pending before echo = %+v, want exactly [id-async-1] (interrupt delivered on write)", pendingBefore)
	}
	if len(deliveredBefore) != 1 || deliveredBefore[0].ID != "id-int-1" {
		t.Fatalf("delivered before echo = %+v, want exactly [id-int-1] (now-priority delivered on write)", deliveredBefore)
	}

	// The async entry's isReplay echo flips it to delivered. (echoAllReplays also
	// re-echoes the now/interrupt uuid; that re-fires markConsumed → OnDelivered,
	// which tolerates the already-delivered entry — agentloop.MarkDelivered logs a
	// warning and returns an error, which OnDelivered swallows.)
	fakeSession.echoAllReplays()

	pending, _ := agentloop.ListPending(sprawlRoot, "alice")
	delivered, _ := agentloop.ListDelivered(sprawlRoot, "alice")
	if len(pending) != 0 || len(delivered) != 2 {
		t.Errorf("after echo: pending=%d (want 0), delivered=%d (want 2)", len(pending), len(delivered))
	}
}

// TestE2E_QUM441_TwoMessagesOverTimeNoReinjection is the QUM-441/817
// no-reinjection gate: write a first message, confirm it via the isReplay echo
// (pending → delivered). Then write a SECOND message and assert the new stdin
// write cites only the second message — the first must NOT be re-injected.
func TestE2E_QUM441_TwoMessagesOverTimeNoReinjection(t *testing.T) {
	uh, fakeSession, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	// --- Round 1: send first message, write to stdin, confirm via echo. ---
	// Post-QUM-555/QUM-556 the flush prompt no longer inlines the body — assert
	// on the entry's ShortID (cited as the `id=` arg of
	// `mcp__sprawl__messages_read(...)`) as the per-message identity token.
	if _, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "msg-1", ShortID: "m1", Class: agentloop.ClassAsync,
		From: "weave", Subject: "first", Body: "unique-token-AAA",
	}); err != nil {
		t.Fatalf("Enqueue 1: %v", err)
	}
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery 1: %v", err)
	}
	w1 := fakeSession.waitForWrites(1, 2*time.Second)
	if len(w1) != 1 {
		t.Fatalf("round 1 stdin writes = %d, want 1", len(w1))
	}
	if !strings.Contains(w1[0].Message.Content, "mcp__sprawl__messages_read(id=m1)") {
		t.Fatalf("round 1 write missing 'mcp__sprawl__messages_read(id=m1)' citation: %q", w1[0].Message.Content)
	}
	// Confirm consumption — flips m1 to delivered.
	fakeSession.echoReplay(w1[0].UUID)
	if p, _ := agentloop.ListPending(sprawlRoot, "alice"); len(p) != 0 {
		t.Fatalf("after round 1 echo: pending=%d, want 0", len(p))
	}
	if d, _ := agentloop.ListDelivered(sprawlRoot, "alice"); len(d) != 1 {
		t.Fatalf("after round 1 echo: delivered=%d, want 1", len(d))
	}

	// --- Round 2: send second message; the new write must NOT re-inject m1. ---
	if _, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "msg-2", ShortID: "m2", Class: agentloop.ClassAsync,
		From: "weave", Subject: "second", Body: "unique-token-BBB",
	}); err != nil {
		t.Fatalf("Enqueue 2: %v", err)
	}
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery 2: %v", err)
	}
	writes := fakeSession.waitForWrites(2, 2*time.Second)
	if len(writes) != 2 {
		t.Fatalf("round 2 total stdin writes = %d, want 2", len(writes))
	}
	round2 := writes[1]
	if !strings.Contains(round2.Message.Content, "mcp__sprawl__messages_read(id=m2)") {
		t.Errorf("round 2 write missing 'mcp__sprawl__messages_read(id=m2)' citation: %q", round2.Message.Content)
	}
	if strings.Contains(round2.Message.Content, "mcp__sprawl__messages_read(id=m1)") {
		t.Errorf("round 2 write RE-INJECTED first message (id=m1 found): %q", round2.Message.Content)
	}

	fakeSession.echoReplay(round2.UUID)
	pollDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(pollDeadline) {
		p, _ := agentloop.ListPending(sprawlRoot, "alice")
		d, _ := agentloop.ListDelivered(sprawlRoot, "alice")
		if len(p) == 0 && len(d) == 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	p, _ := agentloop.ListPending(sprawlRoot, "alice")
	d, _ := agentloop.ListDelivered(sprawlRoot, "alice")
	t.Errorf("after round 2: pending=%d (want 0), delivered=%d (want 2)", len(p), len(d))
}

// ---------------------------------------------------------------------------
// QUM-488: Delegate task-queue bridge
// ---------------------------------------------------------------------------
//
// Real.Delegate writes a queued task to disk via state.EnqueueTask and calls
// runtime.Wake(). Under the unified runtime, unifiedHandle.Wake() must scan
// the on-disk task queue, mark queued tasks in-progress, and enqueue a
// ClassTask QueueItem so the turn loop actually picks them up. Delivery must
// then mark the task done. The bridge must also fire on Start() so any tasks
// queued while the runtime was stopped are picked up at launch.

// buildStartedUnifiedHandleForTestWithSeed mirrors
// buildStartedUnifiedHandleForTest but invokes seed(sprawlRoot) AFTER the
// agent state is written but BEFORE Start runs, so callers can pre-seed the
// on-disk task queue and exercise the Start-time bridge sweep.
func buildStartedUnifiedHandleForTestWithSeed(t *testing.T, caps backend.Capabilities, seed func(sprawlRoot string)) (*unifiedHandle, *fakeBackendSession, string) {
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

	if seed != nil {
		seed(sprawlRoot)
	}

	fakeSession := newFakeBackendSession("sess-alice", caps)
	unifiedAdapterStartFn = func(_ context.Context, _ backend.SessionSpec) (backend.Session, error) {
		return fakeSession, nil
	}
	unifiedRuntimeNewFn = runtimepkg.New

	starter := newInProcessUnifiedStarter(backend.InitSpec{}, nil)
	handle, err := starter.Start(RuntimeStartSpec{
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

// assertTaskPromptShape verifies a delivered task prompt references the
// prompt file via "@<promptFile>" and mentions "task" (case-insensitive).
// The exact wording of the surrounding sentence is the implementer's choice;
// these substring checks lock in the load-bearing contract without pinning
// to a specific phrase.
func assertTaskPromptShape(t *testing.T, prompt, promptFile string) {
	t.Helper()
	wantRef := "@" + promptFile
	if !strings.Contains(prompt, wantRef) {
		t.Errorf("prompt does not reference prompt file %q\n--- prompt ---\n%s", wantRef, prompt)
	}
	if !strings.Contains(strings.ToLower(prompt), "task") {
		t.Errorf("prompt does not mention \"task\" (case-insensitive)\n--- prompt ---\n%s", prompt)
	}
}

// pollTaskStatus returns the last-known task with id, polling for up to
// timeout for it to reach wantStatus. Returns the task seen at the end of
// the poll (matching or not).
func pollTaskStatus(t *testing.T, sprawlRoot, agent, id, wantStatus string, timeout time.Duration) *state.Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last *state.Task
	for time.Now().Before(deadline) {
		tasks, err := state.ListTasks(sprawlRoot, agent)
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		for _, tk := range tasks {
			if tk.ID == id {
				last = tk
				if tk.Status == wantStatus {
					return tk
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return last
}

// TestUnifiedHandle_FeedTasks_PreExistingQueuedTaskBridgedAtStart asserts that
// a queued task on disk before Start is bridged into the runtime queue at
// launch and ends up marked done after delivery.
func TestUnifiedHandle_FeedTasks_PreExistingQueuedTaskBridgedAtStart(t *testing.T) {
	var seeded *state.Task
	uh, fakeSession, sprawlRoot := buildStartedUnifiedHandleForTestWithSeed(t, backend.Capabilities{}, func(sprawlRoot string) {
		tk, err := state.EnqueueTask(sprawlRoot, "alice", "do the pre-start task")
		if err != nil {
			t.Fatalf("EnqueueTask: %v", err)
		}
		seeded = tk
	})
	defer func() { _ = uh.Stop(context.Background()) }()

	if seeded == nil {
		t.Fatal("seeded task is nil")
	}

	writes := fakeSession.waitForWrites(1, 2*time.Second)
	if len(writes) != 1 {
		t.Fatalf("stdin writes = %d, want 1; writes=%+v", len(writes), writes)
	}
	got := writes[0]
	// QUM-817 + Amendment 1: delegated tasks are written with priority `later`
	// so they defer to a clean task boundary rather than splicing mid-turn.
	if got.Priority != "later" {
		t.Errorf("Priority = %q, want %q", got.Priority, "later")
	}
	assertTaskPromptShape(t, got.Message.Content, seeded.PromptFile)

	// The isReplay echo confirms consumption and drives the task → done.
	fakeSession.echoReplay(got.UUID)
	final := pollTaskStatus(t, sprawlRoot, "alice", seeded.ID, "done", 2*time.Second)
	if final == nil {
		t.Fatalf("task %q not found after delivery", seeded.ID)
	}
	if final.Status != "done" {
		t.Errorf("task status = %q, want %q", final.Status, "done")
	}
	if final.DoneAt == "" {
		t.Errorf("task DoneAt is empty; want a timestamp")
	}
}

// TestUnifiedHandle_Wake_BridgesQueuedTasks asserts that a task queued post-
// Start, followed by a Wake, gets bridged into the runtime queue and
// delivered, with the task marked done.
func TestUnifiedHandle_Wake_BridgesQueuedTasks(t *testing.T) {
	uh, fakeSession, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	tk, err := state.EnqueueTask(sprawlRoot, "alice", "do the post-start task")
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	if err := uh.Wake(); err != nil {
		t.Fatalf("Wake: %v", err)
	}

	writes := fakeSession.waitForWrites(1, 2*time.Second)
	if len(writes) != 1 {
		t.Fatalf("stdin writes = %d, want 1; writes=%+v", len(writes), writes)
	}
	got := writes[0]
	if got.Priority != "later" {
		t.Errorf("Priority = %q, want %q", got.Priority, "later")
	}
	assertTaskPromptShape(t, got.Message.Content, tk.PromptFile)

	fakeSession.echoReplay(got.UUID)
	final := pollTaskStatus(t, sprawlRoot, "alice", tk.ID, "done", 2*time.Second)
	if final == nil || final.Status != "done" {
		t.Errorf("task status final = %+v, want status=done", final)
	}
}

// TestUnifiedHandle_Wake_RepeatedCallsDoNotDoubleDeliver asserts that
// repeated Wake() calls (fanned out across goroutines as a stressor) do not
// each re-enqueue the same queued task. The bridge must be idempotent so a
// single queued task surfaces exactly once regardless of how many wakes
// arrive.
func TestUnifiedHandle_Wake_RepeatedCallsDoNotDoubleDeliver(t *testing.T) {
	uh, fakeSession, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	tk, err := state.EnqueueTask(sprawlRoot, "alice", "do the concurrent task")
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	const N = 10
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_ = uh.Wake()
		}()
	}
	wg.Wait()

	// Wait for the one write, then sleep a beat to give any racing extra
	// writes a chance to land. feedTasks flips the task queued→in-progress
	// under tasksMu, so only the first Wake writes it.
	if writes := fakeSession.waitForWrites(1, 2*time.Second); len(writes) < 1 {
		t.Fatalf("no stdin write within timeout")
	}
	time.Sleep(50 * time.Millisecond)

	writes := fakeSession.writesSnapshot()
	if len(writes) != 1 {
		t.Errorf("stdin writes = %d, want exactly 1 (repeated Wakes must not double-deliver)", len(writes))
	}

	fakeSession.echoReplay(writes[0].UUID)
	final := pollTaskStatus(t, sprawlRoot, "alice", tk.ID, "done", 2*time.Second)
	if final == nil || final.Status != "done" {
		t.Errorf("task status final = %+v, want status=done", final)
	}
}

// TestUnifiedHandle_FeedTasks_StoppedRuntimeNoEnqueue asserts that after
// Stop, a subsequent EnqueueTask + Wake does not surface any item, and the
// task remains queued on disk.
func TestUnifiedHandle_FeedTasks_StoppedRuntimeNoEnqueue(t *testing.T) {
	uh, fakeSession, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	// Idempotent Stop: the test stops the runtime explicitly below; this
	// defer guards against a leaked runtime if the test fails mid-flight.
	defer func() { _ = uh.Stop(context.Background()) }()

	if err := uh.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	tk, err := state.EnqueueTask(sprawlRoot, "alice", "do the post-stop task")
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	// Wake on a stopped handle must not panic and must not write.
	_ = uh.Wake()

	// Poll for ~200ms; nothing should be written.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n := len(fakeSession.writesSnapshot()); n > 0 {
			t.Fatalf("stdin writes = %d, want 0 (runtime is stopped)", n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	tasks, err := state.ListTasks(sprawlRoot, "alice")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	var found *state.Task
	for _, tt := range tasks {
		if tt.ID == tk.ID {
			found = tt
			break
		}
	}
	if found == nil {
		t.Fatalf("task %q not found", tk.ID)
	}
	if found.Status != "queued" {
		t.Errorf("task status = %q, want %q (must remain queued)", found.Status, "queued")
	}
}

// TestUnifiedHandle_FeedTasks_MultipleQueuedTasksFIFO asserts that two
// queued tasks pre-Start are both bridged in FIFO order.
func TestUnifiedHandle_FeedTasks_MultipleQueuedTasksFIFO(t *testing.T) {
	var t1, t2 *state.Task
	uh, fakeSession, sprawlRoot := buildStartedUnifiedHandleForTestWithSeed(t, backend.Capabilities{}, func(sprawlRoot string) {
		var err error
		t1, err = state.EnqueueTask(sprawlRoot, "alice", "task one")
		if err != nil {
			t.Fatalf("EnqueueTask 1: %v", err)
		}
		// Brief sleep ensures distinct timestamp prefix in filename.
		time.Sleep(5 * time.Millisecond)
		t2, err = state.EnqueueTask(sprawlRoot, "alice", "task two")
		if err != nil {
			t.Fatalf("EnqueueTask 2: %v", err)
		}
	})
	defer func() { _ = uh.Stop(context.Background()) }()

	writes := fakeSession.waitForWrites(2, 3*time.Second)
	if len(writes) != 2 {
		t.Fatalf("stdin writes = %d, want 2; writes=%+v", len(writes), writes)
	}
	for i, w := range writes {
		if w.Priority != "later" {
			t.Errorf("writes[%d].Priority = %q, want %q", i, w.Priority, "later")
		}
	}
	// FIFO: the first write references t1, the second t2.
	assertTaskPromptShape(t, writes[0].Message.Content, t1.PromptFile)
	assertTaskPromptShape(t, writes[1].Message.Content, t2.PromptFile)

	fakeSession.echoAllReplays()
	for _, id := range []string{t1.ID, t2.ID} {
		final := pollTaskStatus(t, sprawlRoot, "alice", id, "done", 2*time.Second)
		if final == nil || final.Status != "done" {
			t.Errorf("task %q final = %+v, want status=done", id, final)
		}
	}
}

// TestUnifiedHandle_FeedTasks_OnlyQueuedTasksAreDelivered asserts that the
// bridge filters by Status=="queued" and does not re-deliver tasks already
// marked done.
func TestUnifiedHandle_FeedTasks_OnlyQueuedTasksAreDelivered(t *testing.T) {
	var doneTask, queuedTask *state.Task
	uh, fakeSession, _ := buildStartedUnifiedHandleForTestWithSeed(t, backend.Capabilities{}, func(sprawlRoot string) {
		var err error
		doneTask, err = state.EnqueueTask(sprawlRoot, "alice", "already done task")
		if err != nil {
			t.Fatalf("EnqueueTask done: %v", err)
		}
		doneTask.Status = "done"
		doneTask.DoneAt = time.Now().UTC().Format(time.RFC3339)
		if err := state.UpdateTask(sprawlRoot, "alice", doneTask); err != nil {
			t.Fatalf("UpdateTask done: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
		queuedTask, err = state.EnqueueTask(sprawlRoot, "alice", "queued task")
		if err != nil {
			t.Fatalf("EnqueueTask queued: %v", err)
		}
	})
	defer func() { _ = uh.Stop(context.Background()) }()

	// Wait for one write, then a small grace period to catch any spurious extra.
	if writes := fakeSession.waitForWrites(1, 2*time.Second); len(writes) < 1 {
		t.Fatalf("expected at least one write for queued task")
	}
	time.Sleep(50 * time.Millisecond)

	writes := fakeSession.writesSnapshot()
	if len(writes) != 1 {
		t.Fatalf("stdin writes = %d, want exactly 1 (done task must not re-deliver); writes=%+v", len(writes), writes)
	}
	// The single write must reference the still-queued task, not the done one.
	assertTaskPromptShape(t, writes[0].Message.Content, queuedTask.PromptFile)
}

// captureSlogHandler is a minimal in-memory slog.Handler that records every
// record. Mirrors the helper in internal/runtime/eventbus_test.go so tests in
// this package can assert structured-log output without a real handler.
type captureSlogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureSlogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureSlogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *captureSlogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureSlogHandler) WithGroup(_ string) slog.Handler      { return h }

// installCaptureSlog swaps slog.Default() for a capturing handler for the
// duration of the test and returns the capture sink.
func installCaptureSlog(t *testing.T) *captureSlogHandler {
	t.Helper()
	h := &captureSlogHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// TestFeedTasks_ListErrorLogsViaSlog is the QUM-500 regression gate. When
// feedTasks encounters a state.ListTasks error, the diagnostic must be
// emitted via slog (not written directly to os.Stderr).
func TestFeedTasks_ListErrorLogsViaSlog(t *testing.T) {
	uh, _, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	// Corrupt the tasks dir so state.ListTasks fails on JSON parse.
	tasksDir := state.TasksDir(sprawlRoot, "alice")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "0001-bad.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	logs := installCaptureSlog(t)
	uh.feedTasks()

	logs.mu.Lock()
	defer logs.mu.Unlock()
	found := false
	for _, r := range logs.records {
		if r.Level >= slog.LevelWarn && strings.Contains(r.Message, "feedTasks") {
			found = true
			break
		}
	}
	if !found {
		msgs := make([]string, 0, len(logs.records))
		for _, r := range logs.records {
			msgs = append(msgs, r.Message)
		}
		t.Errorf("expected a Warn-or-higher slog record mentioning feedTasks; got %d records: %v", len(logs.records), msgs)
	}
}

// TestE2E_QUM488_DelegateThroughRealEnqueuesIntoRuntime is the QUM-488
// regression gate: a task queued via the public state API and a Wake()
// (mirroring what Real.Delegate does) must reach the agent's turn loop.
func TestE2E_QUM488_DelegateThroughRealEnqueuesIntoRuntime(t *testing.T) {
	uh, fakeSession, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	tk, err := state.EnqueueTask(sprawlRoot, "alice", "delegated work")
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if err := uh.Wake(); err != nil {
		t.Fatalf("Wake: %v", err)
	}

	writes := fakeSession.waitForWrites(1, 2*time.Second)
	if len(writes) != 1 {
		t.Fatalf("stdin writes = %d, want 1 (task queued via state API must reach runtime); writes=%+v", len(writes), writes)
	}
	if writes[0].Priority != "later" {
		t.Errorf("Priority = %q, want %q", writes[0].Priority, "later")
	}
	assertTaskPromptShape(t, writes[0].Message.Content, tk.PromptFile)

	fakeSession.echoReplay(writes[0].UUID)
	final := pollTaskStatus(t, sprawlRoot, "alice", tk.ID, "done", 2*time.Second)
	if final == nil || final.Status != "done" {
		t.Errorf("task status final = %+v, want status=done", final)
	}
}

// ---------------------------------------------------------------------------
// QUM-580: Defense-in-depth post-turn pending-envelope sweep.
//
// The sweepCoordinator (QUM-584 extraction from unifiedHandle) exposes a
// PostTurnSweep() method that the runtime's TurnLoop calls after every turn.
// The sweep decides whether to invoke the bound wake function based on:
//   - deliveredItems: number of QueueItems-with-EntryIDs delivered during the turn
//   - sawMessagesRead: whether the agent invoked mcp__sprawl__messages_read
//   - on-disk pending/ contents under sprawlRoot/.sprawl/agents/<name>/queue/pending/
//
// Rule: if pending/ is non-empty OR (delivered > 0 && !sawRead), wake.
// Otherwise no-op. Counters reset to zero after each sweep.
//
// runDeliveryConfirmationSubscriber tracks the messages-read tool-use over
// the EventBus, resetting on every EventTurnStarted.
// ---------------------------------------------------------------------------

// makeToolUseAssistant builds a well-formed assistant protocol.Message whose
// content is a single tool_use block with the given tool name. Used by the
// QUM-580 delivery-confirmation subscriber tests.
func makeToolUseAssistant(name string) *protocol.Message {
	raw, _ := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role": "assistant",
			"content": []map[string]any{
				{
					"type":  "tool_use",
					"name":  name,
					"input": map[string]any{},
				},
			},
		},
	})
	return &protocol.Message{Type: "assistant", Raw: raw}
}

// newSweepCoordinatorForTest constructs a *sweepCoordinator wired to the
// given sprawlRoot/name with a no-op wake function that records call counts
// via the returned counter. QUM-584: sweep state was extracted out of
// unifiedHandle into sweepCoordinator, so unit tests now operate on the
// coordinator directly.
func newSweepCoordinatorForTest(t *testing.T, sprawlRoot, name string) (*sweepCoordinator, *atomic.Int64) {
	t.Helper()
	var wakeCalls atomic.Int64
	c := newSweepCoordinator(sprawlRoot, name)
	c.Bind(func() error {
		wakeCalls.Add(1)
		return nil
	})
	return c, &wakeCalls
}

// writePendingEnvelope drops one canonical pending queue file under the
// agent's pending/ directory. Matches the format produced by
// agentloop.Enqueue (see internal/agentloop/queue.go:158-166).
func writePendingEnvelope(t *testing.T, sprawlRoot, name string) {
	t.Helper()
	pending := filepath.Join(sprawlRoot, ".sprawl", "agents", name, "queue", "pending")
	if err := os.MkdirAll(pending, 0o755); err != nil {
		t.Fatalf("mkdir pending: %v", err)
	}
	entry := agentloop.Entry{
		Seq: 1, ID: "id-pending-1", ShortID: "pp1",
		Class: agentloop.ClassAsync, From: "peer", Subject: "hi", Body: "hello",
		EnqueuedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	fname := "0000000001-async-id-pending-1.json"
	if err := os.WriteFile(filepath.Join(pending, fname), data, 0o644); err != nil {
		t.Fatalf("write entry: %v", err)
	}
}

func TestSweepCoordinator_PostTurnSweep_NoOpWhenIdle(t *testing.T) {
	sprawlRoot := t.TempDir()
	writeAgentState(t, sprawlRoot, &state.AgentState{Name: "alice", Type: "researcher"})
	c, wakeCalls := newSweepCoordinatorForTest(t, sprawlRoot, "alice")

	// Default counters: delivered=0, sawRead=false; pending/ empty.
	c.PostTurnSweep()

	if got := wakeCalls.Load(); got != 0 {
		t.Errorf("wake calls = %d, want 0 (idle sweep must no-op)", got)
	}
}

func TestSweepCoordinator_PostTurnSweep_NoOpWhenDeliveredAndRead(t *testing.T) {
	sprawlRoot := t.TempDir()
	writeAgentState(t, sprawlRoot, &state.AgentState{Name: "alice", Type: "researcher"})
	c, wakeCalls := newSweepCoordinatorForTest(t, sprawlRoot, "alice")

	c.mu.Lock()
	c.deliveredItems = 2
	c.sawMessagesRead = true
	c.mu.Unlock()

	c.PostTurnSweep()

	if got := wakeCalls.Load(); got != 0 {
		t.Errorf("wake calls = %d, want 0 (delivered+read confirms no missed envelopes)", got)
	}
}

func TestSweepCoordinator_PostTurnSweep_WakesWhenDeliveredButNoRead(t *testing.T) {
	sprawlRoot := t.TempDir()
	writeAgentState(t, sprawlRoot, &state.AgentState{Name: "alice", Type: "researcher"})
	c, wakeCalls := newSweepCoordinatorForTest(t, sprawlRoot, "alice")

	c.mu.Lock()
	c.deliveredItems = 1
	c.sawMessagesRead = false
	c.mu.Unlock()

	c.PostTurnSweep()

	if got := wakeCalls.Load(); got != 1 {
		t.Errorf("wake calls = %d, want 1 (delivered without messages_read implies missed envelope)", got)
	}
}

func TestSweepCoordinator_PostTurnSweep_WakesWhenPendingNonEmpty(t *testing.T) {
	sprawlRoot := t.TempDir()
	writeAgentState(t, sprawlRoot, &state.AgentState{Name: "alice", Type: "researcher"})
	writePendingEnvelope(t, sprawlRoot, "alice")
	c, wakeCalls := newSweepCoordinatorForTest(t, sprawlRoot, "alice")

	// delivered=0, sawRead=false — only the pending file triggers a wake.
	c.PostTurnSweep()

	if got := wakeCalls.Load(); got != 1 {
		t.Errorf("wake calls = %d, want 1 (non-empty pending/ must trigger wake)", got)
	}
}

func TestSweepCoordinator_PostTurnSweep_ResetsCountersAfterCall(t *testing.T) {
	sprawlRoot := t.TempDir()
	writeAgentState(t, sprawlRoot, &state.AgentState{Name: "alice", Type: "researcher"})
	c, _ := newSweepCoordinatorForTest(t, sprawlRoot, "alice")

	c.mu.Lock()
	c.deliveredItems = 5
	c.sawMessagesRead = true
	c.mu.Unlock()

	c.PostTurnSweep()

	c.mu.Lock()
	gotCount := c.deliveredItems
	gotSaw := c.sawMessagesRead
	c.mu.Unlock()

	if gotCount != 0 {
		t.Errorf("deliveredItems = %d after sweep, want 0", gotCount)
	}
	if gotSaw {
		t.Errorf("sawMessagesRead = true after sweep, want false")
	}
}

// TestSweepCoordinator_NoWakeBeforeBind asserts that PostTurnSweep is a safe
// no-op when invoked before Bind(...) has installed the wake function. This
// is the QUM-584 by-construction safety property: a future refactor that
// accidentally reordered Bind after rt.Start would degrade gracefully (sweep
// no-ops) rather than panicking on a nil pointer.
func TestSweepCoordinator_NoWakeBeforeBind(t *testing.T) {
	sprawlRoot := t.TempDir()
	writeAgentState(t, sprawlRoot, &state.AgentState{Name: "alice", Type: "researcher"})
	writePendingEnvelope(t, sprawlRoot, "alice")
	c := newSweepCoordinator(sprawlRoot, "alice")

	// pending/ is non-empty so the sweep decides a wake is needed — but wake
	// is unbound. Must not panic.
	c.PostTurnSweep()
}

// TestRunDeliveryConfirmationSubscriber_TracksToolUseAndResetsOnTurnStart
// verifies the EventBus subscriber that observes the agent's tool-use stream:
// when it sees a tool_use block named "mcp__sprawl__messages_read", it must
// set sawMessagesRead=true on the coordinator; on every EventTurnStarted it
// must reset the flag back to false so the next turn starts clean.
func TestRunDeliveryConfirmationSubscriber_TracksToolUseAndResetsOnTurnStart(t *testing.T) {
	sprawlRoot := t.TempDir()
	writeAgentState(t, sprawlRoot, &state.AgentState{Name: "alice", Type: "researcher"})
	c, _ := newSweepCoordinatorForTest(t, sprawlRoot, "alice")

	bus := runtimepkg.NewEventBus()
	stop := runDeliveryConfirmationSubscriber(bus, c, "delivery-confirmation")
	defer stop()

	// EventTurnStarted: precondition, ensures any pre-existing flag is cleared.
	bus.Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnStarted})

	// Assistant tool_use block invoking messages_read.
	msg := makeToolUseAssistant("mcp__sprawl__messages_read")
	bus.Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventProtocolMessage, Message: msg})

	// Wait until the subscriber observes the flag flipping.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		saw := c.sawMessagesRead
		c.mu.Unlock()
		if saw {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.mu.Lock()
	saw := c.sawMessagesRead
	c.mu.Unlock()
	if !saw {
		t.Fatalf("sawMessagesRead = false after tool_use(mcp__sprawl__messages_read); want true")
	}

	// Now publish another EventTurnStarted: must reset the flag.
	bus.Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnStarted})

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		saw = c.sawMessagesRead
		c.mu.Unlock()
		if !saw {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.mu.Lock()
	saw = c.sawMessagesRead
	c.mu.Unlock()
	if saw {
		t.Errorf("sawMessagesRead = true after EventTurnStarted reset; want false")
	}
}

// TestInProcessUnifiedStarter_CallbacksSafeBeforeFirstEvent is the QUM-584
// acceptance test: the runtime config callbacks (PostTurnSweep, OnDelivered)
// must be safely invocable even before rt.Start has returned. We exercise this
// by overriding unifiedRuntimeNewFn with a fake that fires both callbacks
// synchronously from inside New(cfg) — i.e. at the instant the supervisor hands
// them to the runtime, before any subsequent wiring (subscriber attach,
// handle.Bind, rt.Start) has completed.
//
// Before the QUM-584 refactor this would panic on a nil-pointer deref through
// the partially-built unifiedHandle (handle.rt unset → WakeForDelivery →
// h.rt.WakeForDelivery on nil rt). After the refactor the callbacks capture
// only the sweepCoordinator, which is fully constructed before New(cfg) is
// called, so both callbacks are no-ops at this instant: OnDelivered increments
// the delivered counter cleanly; PostTurnSweep finds wake==nil (Bind has not
// happened yet) and degrades gracefully.
func TestInProcessUnifiedStarter_CallbacksSafeBeforeFirstEvent(t *testing.T) {
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

	fakeSession := newFakeBackendSession("sess-alice", backend.Capabilities{})
	unifiedAdapterStartFn = func(_ context.Context, _ backend.SessionSpec) (backend.Session, error) {
		return fakeSession, nil
	}

	// The fake runtime fires both callbacks from inside New(cfg) — before
	// any later wiring step has executed. Any panic here would fail the test
	// because the supervisor's Start returns the panic via recovery in the
	// turn-loop... actually goroutine panics don't propagate; we fire
	// synchronously on the caller's goroutine so a panic crashes the test
	// process.
	unifiedRuntimeNewFn = func(cfg runtimepkg.RuntimeConfig) *runtimepkg.UnifiedRuntime {
		// Synchronous, in-line invocation of both callbacks. If they
		// reach into a partially-built handle, this panics.
		if cfg.OnDelivered != nil {
			cfg.OnDelivered([]string{"probe-id"})
		}
		if cfg.PostTurnSweep != nil {
			cfg.PostTurnSweep()
		}
		return runtimepkg.New(cfg)
	}

	starter := newInProcessUnifiedStarter(backend.InitSpec{}, nil)
	handle, err := starter.Start(RuntimeStartSpec{
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
	t.Cleanup(func() { _ = uh.Stop(context.Background()) })

	// Sanity: the OnQueueItemDelivered probe should have incremented the
	// coordinator's deliveredItems counter even though it fired before any
	// subsequent wiring. Counter may have since been reset by an
	// EventTurnStarted from the now-running turn loop, so we don't assert a
	// specific value — only that the coordinator is reachable and the
	// supervisor returned a valid handle.
	if uh.coord == nil {
		t.Fatal("unifiedHandle.coord is nil — QUM-584 wiring regressed")
	}
}

// TestInProcessUnifiedStarter_BindInstallsWakeFn asserts the QUM-584 Bind
// step actually wires the sweepCoordinator's wake function before Start
// returns. The coordinator's wake is initially nil; without Bind, any
// PostTurnSweep firing degrades to a no-op. After Start, wake must be non-nil
// so the defense-in-depth pending sweep can actually nudge the runtime.
func TestInProcessUnifiedStarter_BindInstallsWakeFn(t *testing.T) {
	oldStart := unifiedAdapterStartFn
	t.Cleanup(func() { unifiedAdapterStartFn = oldStart })

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

	starter := newInProcessUnifiedStarter(backend.InitSpec{}, nil)
	handle, err := starter.Start(RuntimeStartSpec{
		Name: "alice", Worktree: worktree, SprawlRoot: sprawlRoot,
		SessionID: "sess-alice", TreePath: "weave/alice",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	uh := handle.(*unifiedHandle)
	t.Cleanup(func() { _ = uh.Stop(context.Background()) })

	uh.coord.mu.Lock()
	wakeIsNil := uh.coord.wake == nil
	uh.coord.mu.Unlock()
	if wakeIsNil {
		t.Fatal("sweepCoordinator.wake is nil after Start — Bind step did not run before rt.Start")
	}
}

// TestStartUsesRestartInjectionWhenSet (QUM-723) — when
// RuntimeStartSpec.RestartInjection is non-empty, the in-process unified
// starter must feed that string into runtimepkg.RuntimeConfig.InitialPrompt
// (overriding the persisted AgentState.Prompt). When RestartInjection is
// empty, InitialPrompt falls back to AgentState.Prompt as before.
//
// This is the seam that delivers the neutral "Sprawl was just restarted..."
// nudge into the resumed session's first turn (see agent.RestartInjectionPrompt)
// instead of replaying whatever was in agent_state.Prompt at suspend.
func TestStartUsesRestartInjectionWhenSet(t *testing.T) {
	cases := []struct {
		name             string
		restartInjection string
		agentPrompt      string
		wantInitial      string
	}{
		{
			name:             "empty RestartInjection falls back to AgentState.Prompt",
			restartInjection: "",
			agentPrompt:      "do the original thing",
			wantInitial:      "do the original thing",
		},
		{
			name:             "non-empty RestartInjection overrides AgentState.Prompt",
			restartInjection: "Sprawl was just restarted. If you were in the middle of something, continue where you left off. Otherwise, hang tight.",
			agentPrompt:      "do the original thing",
			wantInitial:      "Sprawl was just restarted. If you were in the middle of something, continue where you left off. Otherwise, hang tight.",
		},
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
			_ = os.MkdirAll(worktree, 0o755)
			writeAgentState(t, sprawlRoot, &state.AgentState{
				Name: "alice", Type: "researcher", Worktree: worktree,
				SessionID: "sess-alice", Prompt: tc.agentPrompt,
			})

			fakeSession := newFakeBackendSession("sess-alice", backend.Capabilities{})
			unifiedAdapterStartFn = func(_ context.Context, _ backend.SessionSpec) (backend.Session, error) {
				return fakeSession, nil
			}
			var capturedInitial string
			unifiedRuntimeNewFn = func(cfg runtimepkg.RuntimeConfig) *runtimepkg.UnifiedRuntime {
				capturedInitial = cfg.InitialPrompt
				return runtimepkg.New(cfg)
			}

			starter := newInProcessUnifiedStarter(backend.InitSpec{}, nil)
			handle, err := starter.Start(RuntimeStartSpec{
				Name: "alice", Worktree: worktree, SprawlRoot: sprawlRoot,
				SessionID: "sess-alice", TreePath: "weave/alice",
				Resume:           tc.restartInjection != "",
				RestartInjection: tc.restartInjection,
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer func() { _ = handle.Stop(context.Background()) }()

			if capturedInitial != tc.wantInitial {
				t.Errorf("RuntimeConfig.InitialPrompt = %q, want %q", capturedInitial, tc.wantInitial)
			}
		})
	}
}
