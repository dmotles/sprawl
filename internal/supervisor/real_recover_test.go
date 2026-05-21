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

// TestRealRecover_CancelsPendingQuestionsFromRecoveringAgent is the QUM-611
// defensive guard. AskUserQuestion from the recovering agent must unblock
// proactively (via cancelByAgent) BEFORE the runtime tears down the abandoned
// session — not incidentally via drainInflight after the reader exits. The
// test verifies this by calling AskUserQuestion with a plain
// context.Background() (no bridgeCtx → no drainInflight), then calling
// Real.Recover. Without the proactive cancel the ask blocks forever; with it
// the ask returns OutcomeAgentRetired with a "recover"-flavored reason.
func TestRealRecover_CancelsPendingQuestionsFromRecoveringAgent(t *testing.T) {
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

	// A no-op consumer keeps the queue from rejecting with
	// OutcomeTUIUnavailable; we don't actually care about OnEnqueue/OnCancel
	// dispatch here.
	if err := r.RegisterQuestionConsumer(&noopQuestionConsumer{}); err != nil {
		t.Fatalf("RegisterQuestionConsumer: %v", err)
	}

	askDone := make(chan QuestionResponse, 1)
	go func() {
		resp, _ := r.AskUserQuestion(context.Background(), QuestionRequest{
			RequestID: "req-1",
			From:      "alice",
			Questions: []Question{{ID: "q1", Prompt: "?"}},
		})
		askDone <- resp
	}()
	// Wait for the question to land in the queue before invoking Recover so
	// the ordering under test (proactive cancel BEFORE StopAbandon) is the
	// one actually exercised.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if d, _ := r.PeekQuestions(); d > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if d, _ := r.PeekQuestions(); d == 0 {
		t.Fatal("setup: question never enqueued")
	}

	if err := r.Recover(context.Background(), "alice"); err != nil {
		t.Fatalf("Real.Recover: %v", err)
	}

	select {
	case resp := <-askDone:
		if resp.Outcome != OutcomeAgentRetired {
			t.Errorf("Outcome = %q, want %q", resp.Outcome, OutcomeAgentRetired)
		}
		if !strings.Contains(resp.Note, "recover") {
			t.Errorf("Note = %q, want a 'recover'-flavored reason", resp.Note)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AskUserQuestion did not return within 2s after Recover — proactive cancelByAgent missing")
	}
}

// noopQuestionConsumer satisfies QuestionConsumer for tests that need a
// registered consumer (so the queue accepts enqueues) but don't care about
// observing dispatches.
type noopQuestionConsumer struct{}

func (noopQuestionConsumer) Name() string                      { return "noop-test" }
func (noopQuestionConsumer) OnEnqueue(*PendingQuestion)        {}
func (noopQuestionConsumer) OnCancel(requestID, reason string) {}

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
