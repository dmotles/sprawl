// Package supervisor / teardown_session.go — shared session-teardown helper
// for *Handle.Stop implementations.
//
// QUM-545: extracted from two divergent inline implementations in
// runtime_launcher.go (unifiedHandle.Stop) and weave_handle.go
// (WeaveRuntimeHandle.Stop). The asymmetry between them was the root cause
// of QUM-543 (mcp__sprawl__kill lying because unifiedHandle omitted Kill)
// and QUM-542 (retire hanging 34+ minutes because unifiedHandle's Wait was
// unbounded). Centralizing the canonical Close → Kill (→ bounded Wait)
// pattern here gives future *Handle.Stop authors one obvious place to copy
// from and keeps the two existing implementations from drifting apart again.

package supervisor

import (
	"log/slog"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
)

// stopActivityTimeout bounds *Handle.Stop's join with the runActivitySubscriber
// goroutine (QUM-547). The subscriber is a self-contained for-range loop over
// the EventBus channel; the only way it wedges past `unsub()` (which closes
// the channel) is if it's parked inside obs.OnMessage writing to
// activityFile. Local-fs write — always fast unless NFS/wedged FD.
const stopActivityTimeout = 2 * time.Second

// activityCloseTimeout bounds *Handle.Stop's activityFile.Close() (QUM-547).
// close(2) on a regular file does not fsync; the only realistic stall is
// NFS metadata ops or a kernel device error.
const activityCloseTimeout = 2 * time.Second

// joinWithTimeout runs op in a goroutine and waits up to timeout for it to
// return. Returns true if op completed within the bound, false otherwise.
// On timeout, emits slog.Warn(msg, logAttrs..., "timeout", timeout). The
// goroutine is intentionally leaked on timeout (QUM-542 precedent: the OS or
// kernel resolves the underlying syscall eventually). nil op returns true
// immediately without panic.
func joinWithTimeout(op func(), timeout time.Duration, msg string, logAttrs ...any) (completed bool) {
	if op == nil {
		return true
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		op()
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		attrs := append([]any{"timeout", timeout}, logAttrs...)
		slog.Warn(msg, attrs...)
		return false
	}
}

// teardownSession performs the canonical Close → Kill (→ bounded Wait) tear-
// down sequence on a backend.Session.
//
// Ordering rationale:
//   - Close signals EOF on the subprocess stdin pipe. Sufficient for an idle
//     backend; insufficient if mid-turn (claude ignores stdin EOF while a
//     turn is active — QUM-543).
//   - Kill issues SIGKILL. Required for mid-turn shutdown to be honored;
//     without this, mcp__sprawl__kill returns success while the backend
//     keeps running (QUM-543).
//   - Wait reaps the zombie. Optional, gated by waitTimeout > 0.
//
// If waitTimeout > 0, after Kill the helper invokes session.Wait() inside a
// goroutine and abandons it if it does not complete within waitTimeout,
// emitting a slog.Warn citing QUM-542. SIGKILL has already landed by then,
// so the OS will reap the zombie eventually — the bounded wait keeps Stop
// callers (notably Real.Retire → runtime.Stop → handle.Stop) snappy when a
// stuck Claude Code Task subshell holds the parent's stdout pipe FD open.
//
// If waitTimeout == 0 the helper does NOT call Wait — the caller is opting
// out of synchronous reaping. This is the WeaveRuntimeHandle case: calling
// Wait there makes /proc/<old-pid>/stat disappear immediately, which breaks
// scripts/test-handoff-e2e.sh's parent-PID fallback path (assertion #4).
// Matching the legacy bridge.Close semantics keeps the subprocess as a
// zombie briefly (reaped when sprawl exits).
//
// logAttrs are appended to the timeout-warning slog call so the warning can
// identify which handle / session timed out.
//
// teardownSession does not return errors: Close / Kill / Wait failures during
// shutdown are expected (e.g. exit-status-1 from a SIGKILLed process) and
// are intentionally swallowed here. Callers that care about lifecycle errors
// must observe them through runtime.Stop / handle state, not this helper.
//
// Returns waitTimedOut = true iff the bounded Wait was attempted (waitTimeout
// > 0) and abandoned because session.Wait() failed to return within
// waitTimeout. Used by Real.Retire/Kill to surface the fact via the
// retire.runtime-stop-done / kill.runtime-stop-done MCP-call checkpoints
// (QUM-546). When waitTimeout == 0 (Wait skipped) the return is always false.
func teardownSession(session backendpkg.Session, waitTimeout time.Duration, logAttrs ...any) (waitTimedOut bool) {
	if session == nil {
		return false
	}
	_ = session.Close()
	_ = session.Kill()
	if waitTimeout <= 0 {
		return false
	}
	waitDone := make(chan struct{})
	go func() {
		_ = session.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		return false
	case <-time.After(waitTimeout):
		attrs := append([]any{"timeout", waitTimeout}, logAttrs...)
		slog.Warn("session.Wait abandoned after SIGKILL — likely stuck child pipe FD (QUM-542)", attrs...)
		return true
	}
}
