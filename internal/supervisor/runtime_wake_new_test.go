package supervisor

// QUM-724 — new wake-verb behavior tests.
//
// These tests cover NEW behaviors introduced by the rename+expand of `recover`
// into `wake`. They reference symbols that do not exist yet:
//
//   - AgentRuntime.Wake(ctx) (*WakeResult, error)
//   - ErrWakeNotNeeded sentinel
//   - RuntimeEventWoken event kind
//   - WakeResult{Mode string, SessionRestored bool}
//
// They will fail to compile until the implementer lands the rename. That is
// the intended red-phase signal for TDD.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// wakeCapturingStarter is a RuntimeStarter that records every Start call's
// spec, optionally fires the spec's OnResumeFailure callback on the first
// call (to drive the wake fallback path), and can produce per-call sessions
// (e.g. first one pre-faulted to fail the health probe).
type wakeCapturingStarter struct {
	mu               sync.Mutex
	specs            []RuntimeStartSpec
	startErr         error
	failOnCall       int // 0=never; N=Nth call returns startErr
	fireResumeFailOn int // 0=never; N=invoke spec.OnResumeFailure on Nth call (after returning the handle)
	sessionMaker     func(call int) *runtimeTestSession
	lastSessions     []*runtimeTestSession
	startCalls       int
}

func (s *wakeCapturingStarter) Start(spec RuntimeStartSpec) (RuntimeHandle, error) {
	s.mu.Lock()
	s.startCalls++
	call := s.startCalls
	s.specs = append(s.specs, spec)
	failOnCall := s.failOnCall
	startErr := s.startErr
	fireOn := s.fireResumeFailOn
	maker := s.sessionMaker
	s.mu.Unlock()

	if failOnCall != 0 && call == failOnCall && startErr != nil {
		return nil, startErr
	}

	var sess *runtimeTestSession
	if maker != nil {
		sess = maker(call)
	} else {
		sess = &runtimeTestSession{
			sessionID: "sess-" + spec.Name,
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		}
	}

	s.mu.Lock()
	s.lastSessions = append(s.lastSessions, sess)
	s.mu.Unlock()

	if fireOn != 0 && call == fireOn && spec.OnResumeFailure != nil {
		// Fire the resume-failure callback synchronously so Wake observes the
		// signal on the same goroutine that drove the Start.
		go spec.OnResumeFailure()
	}
	return sess, nil
}

func (s *wakeCapturingStarter) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startCalls
}

func (s *wakeCapturingStarter) snapshotSpecs() []RuntimeStartSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RuntimeStartSpec(nil), s.specs...)
}

func (s *wakeCapturingStarter) snapshotSessions() []*runtimeTestSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*runtimeTestSession(nil), s.lastSessions...)
}

// shortenWakeTimeouts is a t.Helper that points the package-level wake
// timeout vars at small values so tests don't block. The implementer is
// expected to rename recoverHealthProbeTimeout/recoverStopAbandonTimeout
// to wakeHealthProbeTimeout/wakeStopAbandonTimeout; until then this still
// targets the old names (the rename mechanically updates this body).
func shortenWakeTimeouts(t *testing.T) {
	t.Helper()
	prevProbe := wakeHealthProbeTimeout
	prevStop := wakeStopAbandonTimeout
	wakeHealthProbeTimeout = 200 * time.Millisecond
	wakeStopAbandonTimeout = 1 * time.Second
	t.Cleanup(func() {
		wakeHealthProbeTimeout = prevProbe
		wakeStopAbandonTimeout = prevStop
	})
}

// drainForWoken waits up to 2s for a RuntimeEventWoken event on ch. Returns
// the count of woken events observed plus the count of stopped events for
// negative assertions.
func drainForWoken(t *testing.T, ch <-chan RuntimeEvent, window time.Duration) (woken, stopped int) {
	t.Helper()
	deadline := time.After(window)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Kind == RuntimeEventWoken {
				woken++
			}
			if ev.Kind == RuntimeEventStopped {
				stopped++
			}
		case <-deadline:
			return
		}
	}
}

// runWakeAcceptanceTest exercises the common shape for the three offline-
// state acceptance tests (Paused / Killed / Died). It seeds an AgentState
// with the given disk Status (the snapshot precondition the Wake precondition
// must accept), invokes Wake, and asserts the expected success signature:
//   - starter.Start invoked exactly once with Resume=true and the SessionID
//     carried from the snapshot,
//   - returned WakeResult{Mode:"resumed", SessionRestored:true},
//   - disk Status transitions to "active",
//   - exactly one RuntimeEventWoken emitted within 2s.
func runWakeAcceptanceTest(t *testing.T, diskStatus string) {
	t.Helper()
	shortenWakeTimeouts(t)

	starter := &wakeCapturingStarter{}
	agent := testAgentState("alice")
	agent.Status = diskStatus
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: t.TempDir(),
		Agent:      agent,
		Starter:    starter,
	})

	events, cancel := rt.Subscribe(16)
	defer cancel()

	res, err := rt.Wake(context.Background(), "")
	if err != nil {
		t.Fatalf("Wake on disk Status=%q: err=%v, want nil", diskStatus, err)
	}
	if res == nil {
		t.Fatal("Wake returned nil WakeResult on success")
	}
	if res.Mode != "resumed" {
		t.Errorf("WakeResult.Mode = %q, want %q", res.Mode, "resumed")
	}
	if !res.SessionRestored {
		t.Error("WakeResult.SessionRestored = false, want true (resume path succeeded)")
	}
	if got, want := starter.callCount(), 1; got != want {
		t.Errorf("starter.Start calls = %d, want %d (single attempt, no fallback)", got, want)
	}
	specs := starter.snapshotSpecs()
	if len(specs) == 0 {
		t.Fatal("starter captured zero specs")
	}
	if !specs[0].Resume {
		t.Error("first Start spec.Resume = false, want true (Wake must request --resume)")
	}
	if specs[0].SessionID != agent.SessionID {
		t.Errorf("first Start spec.SessionID = %q, want %q (snapshot session-id)", specs[0].SessionID, agent.SessionID)
	}
	if got := rt.Snapshot().Status; got != state.StatusActive {
		t.Errorf("disk Status after Wake = %q, want %q", got, state.StatusActive)
	}
	if got := rt.Snapshot().Liveness; got != liveness.Running {
		t.Errorf("Liveness after Wake = %q, want %q", got, liveness.Running)
	}
	if woken, _ := drainForWoken(t, events, 2*time.Second); woken != 1 {
		t.Errorf("RuntimeEventWoken count = %d, want 1", woken)
	}
}

func TestWake_AcceptsPaused(t *testing.T) {
	runWakeAcceptanceTest(t, state.StatusPaused)
}

func TestWake_AcceptsKilled(t *testing.T) {
	runWakeAcceptanceTest(t, state.StatusKilled)
}

func TestWake_AcceptsDied(t *testing.T) {
	runWakeAcceptanceTest(t, state.StatusDied)
}

// TestWake_FallbackOnResumeRejected pins QUM-724 wake-fallback semantics:
// when the first starter.Start succeeds but the spec's OnResumeFailure fires
// (claude rejected the --resume cookie), Wake MUST:
//   - abandon the first handle (StopAbandon, not polite Stop),
//   - retry starter.Start once with Resume=false and empty SessionID (new
//     fresh session, no resume),
//   - return WakeResult{Mode:"fresh", SessionRestored:false},
//   - end with disk Status == "active" (no lingering "resume_failed"),
//   - emit exactly one RuntimeEventWoken (the fallback success).
func TestWake_FallbackOnResumeRejected(t *testing.T) {
	shortenWakeTimeouts(t)

	starter := &wakeCapturingStarter{
		fireResumeFailOn: 1,
	}
	agent := testAgentState("alice")
	agent.Status = state.StatusPaused
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: t.TempDir(),
		Agent:      agent,
		Starter:    starter,
	})

	events, cancel := rt.Subscribe(32)
	defer cancel()

	res, err := rt.Wake(context.Background(), "")
	if err != nil {
		t.Fatalf("Wake with resume-failure fallback: err=%v, want nil", err)
	}
	if res == nil {
		t.Fatal("Wake returned nil WakeResult on fallback success")
	}
	if res.Mode != "fresh" {
		t.Errorf("WakeResult.Mode = %q, want %q (fallback path)", res.Mode, "fresh")
	}
	if res.SessionRestored {
		t.Error("WakeResult.SessionRestored = true, want false on fallback")
	}
	if got, want := starter.callCount(), 2; got != want {
		t.Errorf("starter.Start calls = %d, want %d (initial resume + fallback fresh)", got, want)
	}
	specs := starter.snapshotSpecs()
	if len(specs) < 2 {
		t.Fatalf("captured %d specs, want >= 2", len(specs))
	}
	if !specs[0].Resume {
		t.Error("first Start spec.Resume = false, want true (initial wake attempts resume)")
	}
	if specs[1].Resume {
		t.Error("fallback Start spec.Resume = true, want false (fresh session)")
	}
	// QUM-744: the fresh-fallback spec must carry a freshly-minted host-side
	// session_id that DIFFERS from the original (so the backend session's
	// config.SessionID propagates the new id back to handle.SessionID() and
	// thus through to disk). The exact value is opaque — only the non-empty
	// + not-equal-to-original invariant is pinned.
	if specs[1].SessionID == "" {
		t.Errorf("fallback Start spec.SessionID = empty; want a freshly-minted non-empty id")
	}
	if specs[1].SessionID == specs[0].SessionID {
		t.Errorf("fallback Start spec.SessionID = %q (same as original resume spec); want freshly minted", specs[1].SessionID)
	}
	// First handle must have been abandoned, not politely stopped.
	sessions := starter.snapshotSessions()
	if len(sessions) < 1 {
		t.Fatalf("captured %d sessions, want >= 1", len(sessions))
	}
	if sessions[0].stopAbandonCalls.Load() < 1 {
		t.Errorf("first handle StopAbandon calls = %d, want >= 1 (fallback must abandon)", sessions[0].stopAbandonCalls.Load())
	}
	if sessions[0].stopCalls.Load() != 0 {
		t.Errorf("first handle Stop calls = %d, want 0 (fallback path skips polite Stop)", sessions[0].stopCalls.Load())
	}
	if got := rt.Snapshot().Status; got != state.StatusActive {
		t.Errorf("disk Status after fallback Wake = %q, want %q (no transient resume_failed persistence)", got, state.StatusActive)
	}
	if woken, _ := drainForWoken(t, events, 2*time.Second); woken != 1 {
		t.Errorf("RuntimeEventWoken count = %d, want 1 (fallback success emits one event)", woken)
	}
}

// TestWake_FallbackOnHealthProbeFail pins the second fallback trigger: the
// first --resume handle survives Start() but the post-Start health probe
// fails (no frames within the probe window). Wake must abandon the first
// handle and retry once with Resume=false.
func TestWake_FallbackOnHealthProbeFail(t *testing.T) {
	shortenWakeTimeouts(t)

	starter := &wakeCapturingStarter{
		sessionMaker: func(call int) *runtimeTestSession {
			sess := &runtimeTestSession{
				sessionID: "sess-alice",
				caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
			}
			// First (resume) handle is born terminally faulted so the
			// post-Start health probe fails. Second (fresh) handle is healthy.
			if call == 1 {
				sess.terminallyFaulted = true
			}
			return sess
		},
	}
	agent := testAgentState("alice")
	agent.Status = state.StatusKilled
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: t.TempDir(),
		Agent:      agent,
		Starter:    starter,
	})

	events, cancel := rt.Subscribe(32)
	defer cancel()

	res, err := rt.Wake(context.Background(), "")
	if err != nil {
		t.Fatalf("Wake with health-probe fallback: err=%v, want nil", err)
	}
	if res == nil {
		t.Fatal("Wake returned nil WakeResult on health-probe fallback")
	}
	if res.Mode != "fresh" {
		t.Errorf("WakeResult.Mode = %q, want %q (health-probe fallback)", res.Mode, "fresh")
	}
	if res.SessionRestored {
		t.Error("WakeResult.SessionRestored = true, want false on fallback")
	}
	if got := starter.callCount(); got != 2 {
		t.Errorf("starter.Start calls = %d, want 2 (resume + fresh retry)", got)
	}
	specs := starter.snapshotSpecs()
	if len(specs) >= 2 && specs[1].Resume {
		t.Error("fallback (second) Start spec.Resume = true, want false")
	}
	sessions := starter.snapshotSessions()
	if len(sessions) >= 1 && sessions[0].stopAbandonCalls.Load() < 1 {
		t.Errorf("first handle StopAbandon calls = %d, want >= 1 (fallback must abandon faulted handle)", sessions[0].stopAbandonCalls.Load())
	}
	if got := rt.Snapshot().Status; got != state.StatusActive {
		t.Errorf("disk Status after fallback Wake = %q, want %q", got, state.StatusActive)
	}
	if woken, _ := drainForWoken(t, events, 2*time.Second); woken != 1 {
		t.Errorf("RuntimeEventWoken count = %d, want 1", woken)
	}
}

// TestWake_FallbackFailureSurfacesError pins the both-attempts-fail path:
// the first resume Start succeeds but its OnResumeFailure fires, the second
// (fresh) Start returns an error. Wake must surface the error, end the
// runtime in liveness Stopped, and emit NO RuntimeEventWoken.
func TestWake_FallbackFailureSurfacesError(t *testing.T) {
	shortenWakeTimeouts(t)

	wantErr := errors.New("fresh start failed: out of fd")
	starter := &wakeCapturingStarter{
		fireResumeFailOn: 1,
		failOnCall:       2,
		startErr:         wantErr,
	}
	agent := testAgentState("alice")
	agent.Status = state.StatusPaused
	sprawlRoot := t.TempDir()
	// Pre-save the agent on disk so the doubly-failed-wake terminal-stamp
	// code path (LoadAgent → mutate Status → SaveAgent) has a file to read
	// and overwrite — exercises the on-disk assertion below. (QUM-744)
	if err := state.SaveAgent(sprawlRoot, agent); err != nil {
		t.Fatalf("pre-save agent: %v", err)
	}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: sprawlRoot,
		Agent:      agent,
		Starter:    starter,
	})

	events, cancel := rt.Subscribe(32)
	defer cancel()

	res, err := rt.Wake(context.Background(), "")
	if err == nil {
		t.Fatal("Wake on doubly-failed path: err = nil, want non-nil (fallback Start failure must surface)")
	}
	if res != nil {
		t.Errorf("WakeResult = %+v, want nil on failure", res)
	}
	if !errors.Is(err, wantErr) && !containsAny(err.Error(), wantErr.Error()) {
		t.Errorf("Wake error = %v, want one wrapping %v", err, wantErr)
	}
	if got := rt.Snapshot().Liveness; got != liveness.Stopped {
		t.Errorf("Liveness after failed Wake = %q, want %q", got, liveness.Stopped)
	}
	// QUM-744: the doubly-failed wake path MUST explicitly stamp a terminal
	// disk Status before returning the error — relying on watchHandleExit
	// normalization is a watcher-race risk. Assert both in-memory and on-disk.
	if got := rt.Snapshot().Status; got != state.StatusResumeFailed {
		t.Errorf("in-memory Status after doubly-failed Wake = %q, want %q", got, state.StatusResumeFailed)
	}
	if loaded, lErr := state.LoadAgent(sprawlRoot, agent.Name); lErr != nil {
		t.Fatalf("post-Wake LoadAgent: %v", lErr)
	} else if loaded == nil {
		t.Fatal("post-Wake LoadAgent returned nil agent")
	} else if loaded.Status != state.StatusResumeFailed {
		t.Errorf("on-disk Status after doubly-failed Wake = %q, want %q", loaded.Status, state.StatusResumeFailed)
	}
	woken, _ := drainForWoken(t, events, 1*time.Second)
	if woken != 0 {
		t.Errorf("RuntimeEventWoken emitted %d times on failed Wake; want 0", woken)
	}
}

// quietPrintf keeps fmt imported even if assertions are trimmed. (Defensive
// since we use fmt.Errorf-style wrapping above and the file should not
// silently lose the dependency under a future edit.)
var _ = fmt.Sprintf
