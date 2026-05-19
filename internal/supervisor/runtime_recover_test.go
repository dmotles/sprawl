package supervisor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
)

// QUM-601: AgentRuntime.Recover tears down the existing handle via
// StopAbandon (no polite Interrupt) and re-invokes the RuntimeStarter to
// build a fresh handle. The new handle is swapped in atomically. Recovery
// is gated on the live handle being terminally faulted; healthy runtimes
// reject with ErrRecoverNotNeeded, and stopped/killed/retired runtimes
// reject with a "cannot recover" error.

// recoverCountingStarter is a RuntimeStarter that hands out a unique handle
// per Start() call so the recover path can be told apart from the initial
// start. It also tolerates a configurable error injected on the Nth Start.
type recoverCountingStarter struct {
	mu           sync.Mutex
	startCalls   int
	failOnCall   int // 0 = never; 1 = first; 2 = second; etc.
	failErr      error
	lastSessions []*runtimeTestSession
	// specs records the RuntimeStartSpec received on each Start call so
	// tests can assert that Recover propagates Resume=true (QUM-601).
	specs []RuntimeStartSpec
}

func (s *recoverCountingStarter) Start(_ context.Context, spec RuntimeStartSpec) (RuntimeHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startCalls++
	s.specs = append(s.specs, spec)
	if s.failOnCall != 0 && s.startCalls == s.failOnCall && s.failErr != nil {
		return nil, s.failErr
	}
	sess := &runtimeTestSession{
		sessionID: "sess-" + spec.Name,
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	s.lastSessions = append(s.lastSessions, sess)
	return sess, nil
}

func (s *recoverCountingStarter) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startCalls
}

func TestAgentRuntime_Recover_HealthySession_ReturnsErrRecoverNotNeeded(t *testing.T) {
	starter := &recoverCountingStarter{}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if starter.callCount() != 1 {
		t.Fatalf("after initial Start, startCalls = %d, want 1", starter.callCount())
	}

	err := rt.Recover(context.Background())
	if !errors.Is(err, ErrRecoverNotNeeded) {
		t.Fatalf("Recover on healthy session: err = %v, want ErrRecoverNotNeeded", err)
	}
	if starter.callCount() != 1 {
		t.Errorf("starter must not be re-invoked on a healthy session; startCalls = %d, want 1", starter.callCount())
	}
}

func TestAgentRuntime_Recover_StoppedRuntime_Errors(t *testing.T) {
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	starter := &runtimeTestStarter{session: session}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := rt.Snapshot().Lifecycle; got != RuntimeLifecycleStopped {
		t.Fatalf("after Stop, Lifecycle = %q, want %q", got, RuntimeLifecycleStopped)
	}

	err := rt.Recover(context.Background())
	if err == nil {
		t.Fatal("Recover on a Stopped runtime: err = nil, want a 'cannot recover' error")
	}
	if errors.Is(err, ErrRecoverNotNeeded) {
		t.Errorf("Recover on Stopped runtime returned ErrRecoverNotNeeded; want a distinct hard error")
	}
}

func TestAgentRuntime_Recover_RegisteredButNotStarted_Errors(t *testing.T) {
	starter := &runtimeTestStarter{session: &runtimeTestSession{}}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})
	// Lifecycle is Registered; no handle has ever been attached.

	err := rt.Recover(context.Background())
	if err == nil {
		t.Fatal("Recover on Registered runtime: err = nil, want a 'cannot recover' error")
	}
	if errors.Is(err, ErrRecoverNotNeeded) {
		t.Errorf("Recover on Registered runtime returned ErrRecoverNotNeeded; want a distinct hard error")
	}
}

func TestAgentRuntime_Recover_FaultedSession_StopsAbandonAndRestarts(t *testing.T) {
	// Initial-start handle is poisoned. Recover must call StopAbandon on it
	// (not Stop), then invoke starter.Start a SECOND time to build a fresh
	// handle, and the runtime must remain Started afterwards.
	starter := &recoverCountingStarter{}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})

	events, cancel := rt.Subscribe(16)
	defer cancel()

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("initial Start: %v", err)
	}
	if starter.callCount() != 1 {
		t.Fatalf("after initial Start, startCalls = %d, want 1", starter.callCount())
	}
	starter.mu.Lock()
	first := starter.lastSessions[0]
	starter.mu.Unlock()
	// Poison the first handle so IsTerminallyFaulted() returns true.
	first.terminallyFaulted = true

	if err := rt.Recover(context.Background()); err != nil {
		t.Fatalf("Recover on faulted session: %v", err)
	}

	if first.stopAbandonCalls != 1 {
		t.Errorf("first handle stopAbandonCalls = %d, want 1 (Recover must use abandon, not polite Stop)", first.stopAbandonCalls)
	}
	if first.stopCalls != 0 {
		t.Errorf("first handle stopCalls = %d, want 0 (recover path skips polite Interrupt+Stop)", first.stopCalls)
	}
	if starter.callCount() != 2 {
		t.Errorf("starter.startCalls = %d, want 2 (initial + recover)", starter.callCount())
	}
	// QUM-601: the initial Start must NOT request resume (fresh session),
	// but the recover-path Start MUST set Resume=true so claude resumes
	// the prior conversation transcript instead of starting a blank one.
	starter.mu.Lock()
	specs := append([]RuntimeStartSpec(nil), starter.specs...)
	starter.mu.Unlock()
	if len(specs) < 2 {
		t.Fatalf("starter captured %d specs, want at least 2", len(specs))
	}
	if specs[0].Resume {
		t.Errorf("initial Start spec.Resume = true, want false (fresh session)")
	}
	if !specs[1].Resume {
		t.Fatal("recover did not set spec.Resume=true on the restart")
	}
	if got := rt.Snapshot().Lifecycle; got != RuntimeLifecycleStarted {
		t.Errorf("Lifecycle after Recover = %q, want %q", got, RuntimeLifecycleStarted)
	}

	// Recover must emit RuntimeEventRecovered to subscribers within 1s.
	deadline := time.After(1 * time.Second)
	sawRecovered := false
loop:
	for {
		select {
		case ev := <-events:
			if ev.Kind == RuntimeEventRecovered {
				sawRecovered = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	if !sawRecovered {
		t.Errorf("subscriber never saw RuntimeEventRecovered after Recover")
	}
}

func TestAgentRuntime_Recover_RetryWhileInFlight_Errors(t *testing.T) {
	// Two concurrent Recover callers race against the same faulted runtime.
	// Exactly one must succeed; the other must report a "recovery in
	// progress" error from the recoverMu guard.
	// Pre-closed release channel for the initial Start so it does not
	// block; we re-arm the blocker before kicking off the Recover races.
	initialRelease := make(chan struct{})
	close(initialRelease)
	initialReleased := make(chan struct{})
	starter := &blockingRecoverStarter{
		release:  initialRelease,
		released: initialReleased,
	}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("initial Start: %v", err)
	}
	<-initialReleased

	// Poison the live handle.
	starter.mu.Lock()
	live := starter.lastSession
	starter.mu.Unlock()
	live.terminallyFaulted = true

	// Re-arm the blocker for the recover-path Start invocation.
	starter.mu.Lock()
	starter.release = make(chan struct{})
	starter.released = make(chan struct{})
	releaseRecover := starter.release
	recoverReleased := starter.released
	starter.mu.Unlock()

	var (
		wg          sync.WaitGroup
		successCnt  atomic.Int32
		failureCnt  atomic.Int32
		failureErrs = make(chan error, 2)
	)
	// Kick off two concurrent Recover calls.
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := rt.Recover(context.Background())
			if err == nil {
				successCnt.Add(1)
			} else {
				failureCnt.Add(1)
				failureErrs <- err
			}
		}()
	}

	// Give the second caller a moment to enter and fail-fast on the lock.
	// We don't sleep-poll on success; instead we drain one failure here
	// (it should arrive immediately because recoverMu rejects synchronously).
	var earlyFail error
	select {
	case earlyFail = <-failureErrs:
	case <-time.After(2 * time.Second):
		t.Fatal("second Recover never returned a 'recovery in progress' error while first is still in flight")
	}
	if earlyFail == nil || !containsAny(earlyFail.Error(), "in progress", "already") {
		t.Errorf("concurrent Recover error = %v, want one mentioning 'in progress' or 'already'", earlyFail)
	}

	// Release the first Recover's Start.
	close(releaseRecover)
	<-recoverReleased

	wg.Wait()
	if successCnt.Load() != 1 {
		t.Errorf("successful Recover count = %d, want exactly 1", successCnt.Load())
	}
	if failureCnt.Load() != 1 {
		t.Errorf("failed Recover count = %d, want exactly 1", failureCnt.Load())
	}
}

// blockingRecoverStarter blocks each Start call on a release channel so the
// concurrent-recover test can deterministically hold a recovery in flight.
type blockingRecoverStarter struct {
	mu          sync.Mutex
	startCalls  int
	lastSession *runtimeTestSession
	release     chan struct{}
	released    chan struct{}
}

func (s *blockingRecoverStarter) Start(_ context.Context, spec RuntimeStartSpec) (RuntimeHandle, error) {
	s.mu.Lock()
	s.startCalls++
	release := s.release
	released := s.released
	s.mu.Unlock()

	<-release
	sess := &runtimeTestSession{
		sessionID: "sess-" + spec.Name,
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	s.mu.Lock()
	s.lastSession = sess
	s.mu.Unlock()
	close(released)
	return sess, nil
}

// containsAny reports whether s contains any of the listed substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
