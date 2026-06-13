// QUM-545: tests for the shared session-teardown helper plus a parameterized
// contract test that every *Handle.Stop implementation must satisfy.
//
// The contract test is the load-bearing guard against the QUM-543 class of
// drift: if a future Stop implementation forgets to SIGKILL the backend
// subprocess, the contract test fails for that handle type. Add new
// *Handle.Stop implementations to the table below to enroll them.

package supervisor

import (
	"context"
	"reflect"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
)

// ---------------------------------------------------------------------------
// Direct helper tests
// ---------------------------------------------------------------------------

func TestTeardownSession_NoWait_OnlyClosesAndKills(t *testing.T) {
	fs := newFakeBackendSession("sess-x", backendpkg.Capabilities{})

	if got := teardownSession(fs, 0); got {
		t.Errorf("waitTimedOut = true, want false (Wait skipped when waitTimeout=0)")
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.closeCalls != 1 {
		t.Errorf("closeCalls = %d, want 1", fs.closeCalls)
	}
	if fs.killCalls != 1 {
		t.Errorf("killCalls = %d, want 1", fs.killCalls)
	}
	if fs.waitCalls != 0 {
		t.Errorf("waitCalls = %d, want 0 (waitTimeout=0 must skip Wait)", fs.waitCalls)
	}
	if want := []string{"close", "kill"}; !reflect.DeepEqual(fs.teardown, want) {
		t.Errorf("teardown order = %v, want %v", fs.teardown, want)
	}
}

func TestTeardownSession_WithTimeout_ClosesKillsWaits(t *testing.T) {
	fs := newFakeBackendSession("sess-x", backendpkg.Capabilities{})

	if got := teardownSession(fs, 2*time.Second); got {
		t.Errorf("waitTimedOut = true, want false (Wait returned cleanly)")
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.waitCalls != 1 {
		t.Errorf("waitCalls = %d, want 1 (waitTimeout>0 must call Wait)", fs.waitCalls)
	}
	if want := []string{"close", "kill", "wait"}; !reflect.DeepEqual(fs.teardown, want) {
		t.Errorf("teardown order = %v, want %v", fs.teardown, want)
	}
}

func TestTeardownSession_BoundedWait_ReturnsWhenWaitWedges(t *testing.T) {
	fs := newFakeBackendSession("sess-x", backendpkg.Capabilities{})
	block := make(chan struct{})
	fs.mu.Lock()
	fs.waitBlock = block
	fs.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})

	done := make(chan struct{})
	var waitTimedOut bool
	start := time.Now()
	go func() {
		waitTimedOut = teardownSession(fs, 100*time.Millisecond, "session_id", "sess-x")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("teardownSession wedged on session.Wait — bounded timeout did not fire")
	}
	if !waitTimedOut {
		t.Errorf("waitTimedOut = false, want true (QUM-546: bounded Wait abandoned must report timeout)")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("teardownSession returned in %v, want >= 100ms (timeout should have elapsed)", elapsed)
	}

	// SIGKILL must have fired even though Wait is wedged.
	fs.mu.Lock()
	gotKills := fs.killCalls
	fs.mu.Unlock()
	if gotKills != 1 {
		t.Errorf("killCalls = %d, want 1 (SIGKILL must precede the bounded Wait)", gotKills)
	}
}

// ---------------------------------------------------------------------------
// joinWithTimeout helper tests (QUM-547)
// ---------------------------------------------------------------------------
//
// joinWithTimeout runs op in a goroutine and returns true if it completes
// within timeout; otherwise emits a slog.Warn and returns false. nil op is a
// no-op that returns true.

func TestJoinWithTimeout_OpCompletes_ReturnsTrue(t *testing.T) {
	start := time.Now()
	completed := joinWithTimeout(func() {
		// Fast op.
	}, 2*time.Second, "should-not-warn")
	elapsed := time.Since(start)
	if !completed {
		t.Errorf("completed = false, want true (op finished within timeout)")
	}
	if elapsed >= 2*time.Second {
		t.Errorf("elapsed = %v, want < 2s (op should have returned immediately)", elapsed)
	}
}

func TestJoinWithTimeout_OpWedges_ReturnsFalseWithinTimeout(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})

	const timeout = 100 * time.Millisecond
	start := time.Now()
	completed := joinWithTimeout(func() {
		<-block
	}, timeout, "wedge-test", "kind", "stopActivity")
	elapsed := time.Since(start)

	if completed {
		t.Errorf("completed = true, want false (op wedged past timeout)")
	}
	if elapsed < timeout {
		t.Errorf("elapsed = %v, want >= %v (timeout must elapse before return)", elapsed, timeout)
	}
	if elapsed > 2*timeout {
		t.Errorf("elapsed = %v, want <= %v (3x slack — timer firing should be prompt)", elapsed, 2*timeout)
	}
}

func TestJoinWithTimeout_NilOp_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("joinWithTimeout(nil) panicked: %v", r)
		}
	}()
	if !joinWithTimeout(nil, time.Second, "nil-op") {
		t.Errorf("completed = false, want true (nil op should return true immediately)")
	}
}

func TestTeardownSession_NilSession_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("teardownSession(nil) panicked: %v", r)
		}
	}()
	teardownSession(nil, 0)
	teardownSession(nil, time.Second)
}

// ---------------------------------------------------------------------------
// Shared *Handle.Stop contract test (QUM-545)
// ---------------------------------------------------------------------------

// handleStopContractCase enrolls one *Handle.Stop implementation in the
// shared contract: any handle's Stop must invoke session.Close() THEN
// session.Kill() against its backend session. (Whether it also calls
// session.Wait() is handle-specific — WeaveRuntimeHandle deliberately skips
// it; unifiedHandle uses a bounded wait. The contract pins only the
// non-negotiable Close→Kill ordering that QUM-543 established.)
type handleStopContractCase struct {
	name string
	// build constructs the handle plus returns the fakeBackendSession it was
	// wired to so the test can inspect Close/Kill/Wait calls. The handle's
	// Stop method MUST be safe to call from the test (no external services).
	build func(t *testing.T) (handleStopper, *fakeBackendSession)
}

// handleStopper is the minimal surface the contract test exercises.
type handleStopper interface {
	Stop(ctx context.Context) error
}

func TestHandleStop_Contract_CloseThenKill(t *testing.T) {
	cases := []handleStopContractCase{
		{
			name: "unifiedHandle",
			build: func(t *testing.T) (handleStopper, *fakeBackendSession) {
				uh, fs, _ := buildStartedUnifiedHandleForTest(t, backendpkg.Capabilities{})
				return uh, fs
			},
		},
		{
			name: "WeaveRuntimeHandle",
			build: func(t *testing.T) (handleStopper, *fakeBackendSession) {
				return buildStartedWeaveRuntimeHandleForTest(t)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, fs := tc.build(t)
			if err := h.Stop(context.Background()); err != nil {
				t.Fatalf("Stop: %v", err)
			}

			fs.mu.Lock()
			order := append([]string(nil), fs.teardown...)
			closeCalls := fs.closeCalls
			killCalls := fs.killCalls
			fs.mu.Unlock()

			if closeCalls < 1 {
				t.Errorf("closeCalls = %d, want >= 1 (Stop must Close the session)", closeCalls)
			}
			if killCalls < 1 {
				t.Errorf("killCalls = %d, want >= 1 (Stop must SIGKILL the session — QUM-543)", killCalls)
			}

			// Find the first occurrence of "close" and "kill"; assert close < kill.
			closeIdx, killIdx := -1, -1
			for i, op := range order {
				if op == "close" && closeIdx == -1 {
					closeIdx = i
				}
				if op == "kill" && killIdx == -1 {
					killIdx = i
				}
			}
			if closeIdx < 0 || killIdx < 0 {
				t.Fatalf("teardown order = %v, missing close (idx=%d) or kill (idx=%d)", order, closeIdx, killIdx)
			}
			if closeIdx > killIdx {
				t.Errorf("teardown order = %v: Close (idx=%d) must precede Kill (idx=%d) — QUM-543 canonical ordering", order, closeIdx, killIdx)
			}
		})
	}
}

// buildStartedWeaveRuntimeHandleForTest mirrors buildStartedUnifiedHandleForTest
// for *WeaveRuntimeHandle. The runtime is constructed externally (matching
// production cmd/enter.go) and torn down by t.Cleanup.
func buildStartedWeaveRuntimeHandleForTest(t *testing.T) (*WeaveRuntimeHandle, *fakeBackendSession) {
	t.Helper()
	sprawlRoot := t.TempDir()
	const name = "weave"

	fs := newFakeBackendSession("sess-weave", backendpkg.Capabilities{})
	rt := runtimepkg.New(runtimepkg.RuntimeConfig{
		Name:       name,
		SprawlRoot: sprawlRoot,
		Session:    fs,
		IsRoot:     true,
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}
	// Note: do not also call rt.Stop in cleanup — WeaveRuntimeHandle.Stop owns
	// runtime teardown. The handle's stopOnce guards against double-Stop, but a
	// separate rt.Stop() here would still race the test's Stop call.

	h, err := NewWeaveRuntimeHandle(rt, fs, sprawlRoot, name)
	if err != nil {
		t.Fatalf("NewWeaveRuntimeHandle: %v", err)
	}
	t.Cleanup(func() {
		// Idempotent: Stop is the canonical teardown.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Stop(ctx)
	})
	return h, fs
}
