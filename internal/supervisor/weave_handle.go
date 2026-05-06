// Package supervisor / weave_handle.go — RuntimeHandle for the root weave
// agent backed by a UnifiedRuntime. Mirrors *unifiedHandle (used for
// children) but skips the starter step since the runtime is built externally
// by cmd/enter.go's TUI-mode launcher (see QUM-399 plan §5).
//
// The cmd/enter.go path constructs the UnifiedRuntime + backend session and
// calls NewWeaveRuntimeHandle to wire activity-ndjson capture, then
// registers the resulting handle with Supervisor.RegisterRootRuntime so
// child-agent ReportStatus / SendAsync calls trigger weave's
// InterruptDelivery via the same registry path used by child runtimes.

package supervisor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
)

// WeaveRuntimeHandle is the RuntimeHandle for the root weave agent's
// UnifiedRuntime. Mirrors *unifiedHandle (children) but is constructed from
// an externally-owned runtime + session.
type WeaveRuntimeHandle struct {
	rt           *runtimepkg.UnifiedRuntime
	session      backendpkg.Session
	capabilities backendpkg.Capabilities
	sessionID    string
	activityFile *os.File
	stopActivity func()
	sprawlRoot   string
	name         string

	stopOnce sync.Once
	stopErr  error
}

// NewWeaveRuntimeHandle wires activity.ndjson capture for the supplied
// UnifiedRuntime + session and returns a handle suitable for
// Supervisor.RegisterRootRuntime. The caller retains ownership of the
// runtime + session lifecycle until Stop is called on the handle.
func NewWeaveRuntimeHandle(rt *runtimepkg.UnifiedRuntime, session backendpkg.Session, sprawlRoot, name string) (*WeaveRuntimeHandle, error) {
	if rt == nil {
		return nil, fmt.Errorf("NewWeaveRuntimeHandle: runtime must be non-nil")
	}
	if session == nil {
		return nil, fmt.Errorf("NewWeaveRuntimeHandle: session must be non-nil")
	}

	activityDir := filepath.Join(sprawlRoot, ".sprawl", "agents", name)
	if err := os.MkdirAll(activityDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating activity dir %s: %w", activityDir, err)
	}
	activityFile, err := os.OpenFile(agentloop.ActivityPath(sprawlRoot, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // path derived from trusted inputs
	if err != nil {
		return nil, fmt.Errorf("opening activity file: %w", err)
	}
	ring := agentloop.NewActivityRing(agentloop.DefaultActivityCapacity, activityFile)
	observer := &agentloop.ObserverWriter{W: io.Discard, Ring: ring}

	stopActivity := runActivitySubscriber(rt.EventBus(), observer, "weave-activity")

	return &WeaveRuntimeHandle{
		rt:           rt,
		session:      session,
		capabilities: session.Capabilities(),
		sessionID:    session.SessionID(),
		activityFile: activityFile,
		stopActivity: stopActivity,
		sprawlRoot:   sprawlRoot,
		name:         name,
	}, nil
}

// Interrupt delegates to UnifiedRuntime.Interrupt.
func (h *WeaveRuntimeHandle) Interrupt(ctx context.Context) error {
	return h.rt.Interrupt(ctx)
}

// Wake pokes the runtime's queue signal.
func (h *WeaveRuntimeHandle) Wake() error {
	h.rt.Queue().Wake()
	return nil
}

// InterruptDelivery is wake-only — pending entries are intentionally left on
// disk for the TUI's peekAndDrainCmd to drain so the inbox banner and body
// render in the viewport. See QUM-471.
//
// This diverges from unifiedHandle.InterruptDelivery (children), which still
// flushes pending entries into the runtime queue from the handle: child
// agents have no TUI poller, so the handle-side enqueue is their only drain
// path. For weave, the TUI's peekAndDrainCmd (every 2s on AgentTreeMsg while
// turnState == TurnIdle) handles AppendSystemMessage(prompt) → bridge.SendMessage
// which enqueues a ClassUser item via TUIAdapter.SendMessage.
func (h *WeaveRuntimeHandle) InterruptDelivery() error {
	return h.rt.InterruptDelivery(context.Background())
}

// Stop tears down the runtime, activity subscriber, session, and activity
// file. Idempotent.
//
// Session teardown calls Close (signal EOF to claude's stdin) AND Kill
// (SIGKILL the subprocess) before Wait. Without Kill, Wait can block
// indefinitely when claude is mid-turn — Close alone signals stdin EOF, but
// claude is not contracted to exit promptly on that signal during an active
// turn. Always Kill-ing here ensures the QUM-329 handoff cycle (Stop old
// runtime → consolidation → new runtime) doesn't deadlock on weave.lock
// when consolidation runs.
func (h *WeaveRuntimeHandle) Stop(ctx context.Context) error {
	h.stopOnce.Do(func() {
		err := h.rt.Stop(ctx)
		if h.stopActivity != nil {
			h.stopActivity()
		}
		if h.session != nil {
			_ = h.session.Close()
			_ = h.session.Kill()
			// Do NOT call session.Wait here. Legacy bridge.Close (which this
			// path replaces) only invokes Close+Kill — it relies on the OS
			// or a later Wait elsewhere to reap. Calling Wait synchronously
			// here makes /proc/<old-pid>/stat disappear immediately, which
			// breaks scripts/test-handoff-e2e.sh's parent-PID fallback path
			// for assertion #4. Matching the legacy semantics keeps the
			// subprocess as a zombie briefly (reaped when sprawl exits).
		}
		if h.activityFile != nil {
			_ = h.activityFile.Close()
		}
		if err != nil && !isExitError(err) {
			h.stopErr = err
		}
	})
	return h.stopErr
}

// SessionID returns the underlying session ID captured at construction.
func (h *WeaveRuntimeHandle) SessionID() string { return h.sessionID }

// Capabilities returns the backend capabilities reported at construction.
func (h *WeaveRuntimeHandle) Capabilities() backendpkg.Capabilities { return h.capabilities }

// Done returns a channel that closes when the underlying runtime exits.
func (h *WeaveRuntimeHandle) Done() <-chan struct{} { return h.rt.Done() }

// UnifiedRuntime returns the underlying UnifiedRuntime so consumers (e.g.
// the TUI viewport stream wiring — QUM-439) can subscribe to its EventBus.
func (h *WeaveRuntimeHandle) UnifiedRuntime() *runtimepkg.UnifiedRuntime { return h.rt }
