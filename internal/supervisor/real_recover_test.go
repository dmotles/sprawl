package supervisor

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
)

// QUM-601: Real.Recover dispatches to the AgentRuntime for the named agent
// and, on success, fires a BackendRecoveredEmitter so the TUI can clear
// its per-agent fault sticker. Mirrors SetBackendFaultEmitter from QUM-602.

// recoverRecorder is a thread-safe sink for BackendRecoveredEmitter calls.
type recoverRecorder struct {
	mu     sync.Mutex
	agents []string
}

func (r *recoverRecorder) push(agent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents = append(r.agents, agent)
}

func (r *recoverRecorder) snap() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.agents))
	copy(out, r.agents)
	return out
}

func (r *recoverRecorder) waitFor(n int, d time.Duration) []string {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		got := r.snap()
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	return r.snap()
}

func TestRealRecover_DispatchesToAgentRuntime(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)

	starter := &recoverCountingStarter{}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, starter)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}
	starter.mu.Lock()
	first := starter.lastSessions[0]
	starter.mu.Unlock()
	first.terminallyFaulted = true

	if err := r.Recover(context.Background(), "alice"); err != nil {
		t.Fatalf("Real.Recover: %v", err)
	}

	if got := first.stopAbandonCalls; got != 1 {
		t.Errorf("first handle stopAbandonCalls = %d, want 1", got)
	}
	if got := starter.callCount(); got != 2 {
		t.Errorf("starter.startCalls = %d, want 2 (initial + recover)", got)
	}
	if got := rt.Snapshot().Lifecycle; got != RuntimeLifecycleStarted {
		t.Errorf("Lifecycle = %q, want %q", got, RuntimeLifecycleStarted)
	}
}

func TestRealRecover_UnknownAgent_ReturnsError(t *testing.T) {
	r, _ := newFakeReal(t)
	err := r.Recover(context.Background(), "nobody")
	if err == nil {
		t.Fatal("Recover on unknown agent: err = nil, want a not-found error")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "unknown") {
		t.Errorf("Recover error = %v, want one mentioning 'not found' or 'unknown'", err)
	}
}

func TestRealRecover_FiresBackendRecoveredEmitter(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)

	rec := &recoverRecorder{}
	r.SetBackendRecoveredEmitter(rec.push)

	starter := &recoverCountingStarter{}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, starter)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}
	starter.mu.Lock()
	first := starter.lastSessions[0]
	starter.mu.Unlock()
	first.terminallyFaulted = true

	if err := r.Recover(context.Background(), "alice"); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got := rec.waitFor(1, time.Second)
	if len(got) != 1 {
		t.Fatalf("BackendRecoveredEmitter fired %d times, want 1; got=%v", len(got), got)
	}
	if got[0] != "alice" {
		t.Errorf("emitter received agent = %q, want %q", got[0], "alice")
	}
}

func TestSetBackendRecoveredEmitter_NilClears(t *testing.T) {
	r := &Real{}
	rec := &recoverRecorder{}
	// Install + clear + reinstall must be idempotent and panic-free,
	// mirroring SetProgressEmitter / SetBackendFaultEmitter contracts.
	r.SetBackendRecoveredEmitter(rec.push)
	r.SetBackendRecoveredEmitter(nil)
	r.SetBackendRecoveredEmitter(rec.push)

	// After clear-then-reinstall, the live emitter must be the most recent
	// one — invoking dispatchRecovered (or whatever the indirect call path
	// is) must reach rec.push. We can only smoke-test this by going through
	// a real recover; but at minimum the calls above must not panic.
	if _, ok := any(r).(interface{ SetBackendRecoveredEmitter(func(string)) }); !ok {
		t.Fatal("Real does not expose SetBackendRecoveredEmitter(func(string))")
	}

	// Cheap sanity: clear once more and ensure subsequent Recover on an
	// unknown agent still surfaces a clean error (no emitter panic on the
	// reject path).
	r.SetBackendRecoveredEmitter(nil)
	_ = atomic.LoadInt32(new(int32)) // keep sync/atomic import for parity with sibling tests
	if err := r.Recover(context.Background(), "ghost"); err == nil {
		t.Errorf("Recover on unknown agent: err = nil after emitter-clear, want error")
	}
	// We do NOT assert emitter was/wasn't called here — the failure path
	// is allowed to skip the emitter (recover never succeeded). This guard
	// just pins that clear+reinstall doesn't wedge the dispatch wiring.
	_ = backendpkg.Capabilities{}
}
