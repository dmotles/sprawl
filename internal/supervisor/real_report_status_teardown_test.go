// QUM-727: Tests pinning that terminal-outcome report_status calls
// (complete / failure) tear down the live AgentRuntime — claude subprocess
// + EventBus subscribers — instead of leaking them. These tests are written
// before the implementation (TDD red phase) and exercise accessor methods
// (SubprocessAlive, EventBusSubscriberCount) that the implementer will add
// in the same change.

package supervisor

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// waitFor polls cond until it returns true or the timeout elapses. Fatal on
// timeout. Used because the QUM-727 fix may run runtime.Stop in a goroutine
// (so ReportStatus can return immediately and let the MCP reply path flush
// before the subprocess transport closes — design §7).
func waitFor(t *testing.T, cond func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

// TestReportStatusCompleteTearsDownRuntime pins the QUM-727 invariant: a
// terminal-outcome report (state="complete") must (a) flip persisted Status
// to "stopped" via agentops.Report (existing QUM-668 behavior), AND (b) tear
// down the live AgentRuntime so the claude subprocess + EventBus subscribers
// are released. Today path (b) is missing — this is the primary leak.
func TestReportStatusCompleteTearsDownRuntime(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	// Parent agent ("weave"-like): register so SendStatusChange targeting it
	// is not load-bearing for this test. We only assert the alice teardown.
	parent := testAgentState("weave")
	saveTestAgent(t, tmpDir, parent)

	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)

	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	res, err := r.ReportStatus(context.Background(), "alice", "complete", "done")
	if err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}
	if res == nil || res.ReportedAt == "" {
		t.Fatalf("ReportStatus result = %+v, want non-empty ReportedAt", res)
	}

	// Persisted Status flipped — QUM-668 contract.
	st, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if st.Status != state.StatusComplete {
		t.Errorf("alice.Status = %q, want %q (QUM-787)", st.Status, state.StatusComplete)
	}

	// Teardown invariants — the QUM-727 fix targets. Stop may run in a
	// goroutine; poll with a bounded deadline.
	waitFor(t, func() bool {
		return !rt.SubprocessAlive()
	}, 2*time.Second, "runtime.SubprocessAlive() to become false after complete-report")

	if got := rt.EventBusSubscriberCount(); got != 0 {
		t.Errorf("EventBusSubscriberCount() = %d, want 0 after teardown", got)
	}
	if got := rt.Snapshot().Liveness; got != liveness.Stopped {
		t.Errorf("Snapshot().Liveness = %q, want %q after teardown", got, liveness.Stopped)
	}
	if got := session.stopCalls.Load(); got < 1 {
		t.Errorf("session.stopCalls = %d, want >= 1 (QUM-727: terminal report must invoke runtime.Stop)", got)
	}
}

// TestReportStatusFailureTearsDownRuntimeAndStaysRecoverable pins the
// matching invariant for state="failure": Status → "faulted", runtime torn
// down, AND the agent remains a legal Recover source (QUM-606 / QUM-625 M4
// invariant 3 — durable Status="faulted" + handle nil projects to Faulted).
func TestReportStatusFailureTearsDownRuntimeAndStaysRecoverable(t *testing.T) {
	// Trim the recover health-probe wait so the test doesn't spend the full
	// 5s default polling for a fault that never comes from the fake session.
	prev := wakeHealthProbeTimeout
	wakeHealthProbeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { wakeHealthProbeTimeout = prev })

	r, tmpDir := newFakeReal(t)
	parent := testAgentState("weave")
	saveTestAgent(t, tmpDir, parent)

	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)

	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	if _, err := r.ReportStatus(context.Background(), "alice", "failure", "boom"); err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}

	// Persisted Status flipped to faulted.
	st, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if st.Status != state.StatusFaulted {
		t.Errorf("alice.Status = %q, want %q", st.Status, state.StatusFaulted)
	}

	waitFor(t, func() bool { return !rt.SubprocessAlive() }, 2*time.Second,
		"runtime.SubprocessAlive() to become false after failure-report")

	if got := rt.EventBusSubscriberCount(); got != 0 {
		t.Errorf("EventBusSubscriberCount() = %d, want 0 after teardown", got)
	}
	if got := session.stopCalls.Load(); got < 1 {
		t.Errorf("session.stopCalls = %d, want >= 1", got)
	}

	// The QUM-606 M4 invariant: a torn-down failure leaves the agent in a
	// state where Recover is legal (Status="faulted" + handle nil →
	// Liveness projection = Faulted). Verify Recover succeeds and the
	// runtime flips back to Running.
	if _, err := r.Wake(context.Background(), "alice", agent.WakeReasonBare, ""); err != nil {
		t.Fatalf("Wake after failure-report: %v (the QUM-606 invariant must survive QUM-727)", err)
	}
	waitFor(t, func() bool {
		return rt.Snapshot().Liveness == liveness.Running
	}, 2*time.Second, "Snapshot().Liveness to flip back to Running after Recover")
	if !rt.SubprocessAlive() {
		t.Errorf("SubprocessAlive() = false after Recover, want true")
	}
}

// TestReportStatusWorkingPreservesRuntime is the regression guard: a
// non-terminal report (state="working") must NOT tear down the live runtime.
// Today this passes; QUM-727 must not break it.
func TestReportStatusWorkingPreservesRuntime(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	parent := testAgentState("weave")
	saveTestAgent(t, tmpDir, parent)

	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)

	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	if _, err := r.ReportStatus(context.Background(), "alice", "working", "writing tests"); err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}

	// Give any erroneous async-Stop a chance to fire; assert it did NOT.
	time.Sleep(50 * time.Millisecond)

	st, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if st.Status != state.StatusActive {
		t.Errorf("alice.Status = %q, want %q (working must not change Status)", st.Status, state.StatusActive)
	}
	if got := rt.Snapshot().Liveness; got != liveness.Running {
		t.Errorf("Snapshot().Liveness = %q, want %q (working must not tear runtime down)", got, liveness.Running)
	}
	if got := session.stopCalls.Load(); got != 0 {
		t.Errorf("session.stopCalls = %d, want 0 (non-terminal report must not invoke Stop)", got)
	}
	if !rt.SubprocessAlive() {
		t.Errorf("SubprocessAlive() = false, want true (working preserves the live handle)")
	}
}

// TestReportStatusCompleteTearsDownMultipleRuntimesConcurrently is the
// multi-agent extension of TestReportStatusCompleteTearsDownRuntime: it
// exercises AC #5 of QUM-727 — concurrent terminal-outcome reports from
// many children must each tear down their own runtime cleanly without
// interference. A 7th "control" runtime that receives no terminal report
// must remain alive, proving the teardown assertions actually distinguish
// torn-down from live runtimes.
func TestReportStatusCompleteTearsDownMultipleRuntimesConcurrently(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	parent := testAgentState("weave")
	saveTestAgent(t, tmpDir, parent)

	const n = 6
	type child struct {
		name    string
		session *runtimeTestSession
		runtime *AgentRuntime
	}
	children := make([]child, 0, n)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("child%d", i)
		agentState := testAgentState(name)
		saveTestAgent(t, tmpDir, agentState)
		session := &runtimeTestSession{
			sessionID: "sess-" + name,
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		}
		rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
		if err := rt.Start(); err != nil {
			t.Fatalf("runtime start %s: %v", name, err)
		}
		children = append(children, child{name: name, session: session, runtime: rt})
	}

	// Control runtime: registered, but receives no terminal report. We back
	// it with a real UnifiedRuntime (via AttachUnifiedRuntimeForTest) and
	// subscribe to its EventBus so EventbusSubscribed reads true — proving
	// the assertions below actually detect a live subscriber rather than
	// vacuously passing on an empty bus.
	controlName := "control"
	controlAgent := testAgentState(controlName)
	saveTestAgent(t, tmpDir, controlAgent)
	controlRT := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      controlAgent,
	})
	controlURT := runtimepkg.New(runtimepkg.RuntimeConfig{
		Name:    controlName,
		Session: &runtimeTestSession{sessionID: "sess-" + controlName},
	})
	AttachUnifiedRuntimeForTest(t, controlRT, controlURT)
	_, unsub := controlURT.EventBus().Subscribe(4)
	t.Cleanup(unsub)

	// Concurrently fire terminal reports for all n children.
	var wg sync.WaitGroup
	errs := make(chan error, n)
	start := make(chan struct{})
	for _, c := range children {
		wg.Add(1)
		go func(c child) {
			defer wg.Done()
			<-start
			if _, err := r.ReportStatus(context.Background(), c.name, "complete", "done"); err != nil {
				errs <- fmt.Errorf("ReportStatus(%s): %w", c.name, err)
			}
		}(c)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	// Each child: persisted Status=stopped, runtime torn down, subscribers gone.
	for _, c := range children {
		c := c
		waitFor(t, func() bool {
			return !c.runtime.SubprocessAlive() && c.runtime.EventBusSubscriberCount() == 0
		}, 3*time.Second, fmt.Sprintf("%s teardown (SubprocessAlive=false, subscribers=0)", c.name))

		st, err := state.LoadAgent(tmpDir, c.name)
		if err != nil {
			t.Fatalf("LoadAgent(%s): %v", c.name, err)
		}
		if st.Status != state.StatusComplete {
			t.Errorf("%s.Status = %q, want %q", c.name, st.Status, state.StatusComplete)
		}
		if got := c.runtime.Snapshot().Liveness; got != liveness.Stopped {
			t.Errorf("%s.Snapshot().Liveness = %q, want %q", c.name, got, liveness.Stopped)
		}
		if got := c.session.stopCalls.Load(); got < 1 {
			t.Errorf("%s session.stopCalls = %d, want >= 1", c.name, got)
		}
	}

	// Control runtime untouched.
	if !controlRT.SubprocessAlive() {
		t.Errorf("control SubprocessAlive() = false, want true (no terminal report sent)")
	}
	if got := controlRT.EventBusSubscriberCount(); got != 1 {
		t.Errorf("control EventBusSubscriberCount() = %d, want 1", got)
	}

	// Status() must reflect both sides of the split.
	infos, err := r.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	byName := make(map[string]AgentInfo, len(infos))
	for _, info := range infos {
		byName[info.Name] = info
	}
	for _, c := range children {
		info, ok := byName[c.name]
		if !ok {
			t.Errorf("Status() missing entry for %s", c.name)
			continue
		}
		if info.SubprocessAlive {
			t.Errorf("Status()[%s].SubprocessAlive = true, want false", c.name)
		}
		if info.EventbusSubscribed {
			t.Errorf("Status()[%s].EventbusSubscribed = true, want false", c.name)
		}
		if info.EventbusSubCount != 0 {
			t.Errorf("Status()[%s].EventbusSubCount = %d, want 0", c.name, info.EventbusSubCount)
		}
	}
	ctl, ok := byName[controlName]
	if !ok {
		t.Fatalf("Status() missing entry for control")
	}
	if !ctl.SubprocessAlive {
		t.Errorf("Status()[control].SubprocessAlive = false, want true")
	}
	if !ctl.EventbusSubscribed {
		t.Errorf("Status()[control].EventbusSubscribed = false, want true (proves teardown assertions can distinguish)")
	}
}

// blockingStopHandle wraps a runtimeTestSession but makes Stop block until
// signalled. Used by TestReportStatusCompleteDoesNotBlockOnSlowStop to prove
// the QUM-727 fix dispatches runtime.Stop asynchronously so the MCP reply
// path (which serializes the JSON result before ReportStatus returns) is not
// gated on subprocess teardown latency. See design §7.
type blockingStopHandle struct {
	*runtimeTestSession
	stopBlock chan struct{} // close to unblock Stop
}

func (h *blockingStopHandle) Stop(ctx context.Context) error {
	select {
	case <-h.stopBlock:
	case <-ctx.Done():
		return ctx.Err()
	}
	return h.runtimeTestSession.Stop(ctx)
}

func (h *blockingStopHandle) StopAbandon(ctx context.Context) error {
	select {
	case <-h.stopBlock:
	case <-ctx.Done():
		return ctx.Err()
	}
	return h.runtimeTestSession.StopAbandon(ctx)
}

// Ensure blockingStopHandle still satisfies the RuntimeHandle interface and
// any optional probe interfaces by embedding runtimeTestSession. Compile-time
// assertion via interface guard.
var _ RuntimeHandle = (*blockingStopHandle)(nil)

// startTurn forwarder so the embedded session's StartTurn is reachable on
// the wrapper for any internal reflection-based probing. Not strictly
// required (Go embeds promoted methods automatically) but explicit here for
// readability. No-op delegate.
func (h *blockingStopHandle) StartTurn(ctx context.Context, prompt string, spec ...backendpkg.TurnSpec) (<-chan *protocol.Message, error) {
	return h.runtimeTestSession.StartTurn(ctx, prompt, spec...)
}

// TestReportStatusCompleteDoesNotBlockOnSlowStop pins that ReportStatus
// returns quickly even if runtime.Stop is slow — i.e. the QUM-727 fix
// dispatches the teardown asynchronously so the MCP tool result frame
// flushes to the child's stdin before the subprocess transport closes.
// (Design §7 open question; the answer is "yes, async".)
func TestReportStatusCompleteDoesNotBlockOnSlowStop(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	parent := testAgentState("weave")
	saveTestAgent(t, tmpDir, parent)

	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)

	inner := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	handle := &blockingStopHandle{
		runtimeTestSession: inner,
		stopBlock:          make(chan struct{}),
	}
	starter := &runtimeTestStarter{session: handle}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, starter)
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	// Call ReportStatus and time how long it takes to return. With async
	// teardown the call returns immediately; with synchronous teardown it
	// would block on stopBlock forever (we'd hit the test deadline).
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := r.ReportStatus(context.Background(), "alice", "complete", "done")
		done <- err
	}()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("ReportStatus: %v", err)
		}
		if elapsed > 500*time.Millisecond {
			t.Errorf("ReportStatus elapsed = %s, want < 500ms (Stop must dispatch async — QUM-727 §7)", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReportStatus blocked > 2s; Stop must run in a goroutine so MCP reply isn't gated on subprocess teardown")
	}

	// Sanity: while Stop is still blocked, the subprocess is still alive.
	if !rt.SubprocessAlive() {
		t.Errorf("SubprocessAlive() = false while Stop is blocked; teardown finished prematurely")
	}

	// Unblock Stop; assert the eventual teardown completes.
	close(handle.stopBlock)
	waitFor(t, func() bool {
		return !rt.SubprocessAlive()
	}, 2*time.Second, "SubprocessAlive() to become false after unblocking Stop")
}
