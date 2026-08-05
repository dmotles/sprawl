// Package supervisor / weave_handle.go — RuntimeHandle for the root weave
// agent backed by a UnifiedRuntime. Mirrors *unifiedHandle (used for
// children) but skips the starter step since the runtime is built externally
// by cmd/enter.go's TUI-mode launcher (see QUM-399 plan §5).
//
// The cmd/enter.go path constructs the UnifiedRuntime + backend session and
// calls NewWeaveRuntimeHandle to wire activity-ndjson capture, then
// registers the resulting handle with Supervisor.RegisterRootRuntime so
// child-agent ReportStatus / SendMessage calls trigger weave's
// WakeForDelivery via the same registry path used by child runtimes.

package supervisor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/inboxprompt"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/usage"
)

// WeaveRuntimeHandle is the RuntimeHandle for the root weave agent's
// UnifiedRuntime. Mirrors *unifiedHandle (children) but is constructed from
// an externally-owned runtime + session.
type WeaveRuntimeHandle struct {
	rt            *runtimepkg.UnifiedRuntime
	session       backendpkg.Session
	capabilities  backendpkg.Capabilities
	sessionID     string
	activityFile  *os.File
	activityClose func() error
	stopActivity  func()
	stopUsage     func()
	sprawlRoot    string
	name          string

	ring *agentloop.ActivityRing

	// drainMu serialises drainPendingToStdin. Pokes arrive on independent MCP
	// handler goroutines (one per child report_status / send_message), and both
	// inbox reads are unsafe to run concurrently: messages.DrainStatusChange is an
	// unlocked read-dir/read-file/remove sequence, and agentloop.ListPending is a
	// non-destructive peek whose ack lands much later. Overlapping drains would
	// write the same notification twice. (QUM-925)
	drainMu sync.Mutex

	// stopInboxRedrain tears down the inbox-redrain ticker goroutine (QUM-925).
	stopInboxRedrain func()

	stopOnce sync.Once
	stopErr  error
}

// weaveDrainWriteTimeout bounds the stdin write in drainPendingToStdin. It is a
// const, not a test-overridable var, so the CLAUDE.md atomicDuration convention
// does not apply. See the write site for why an unbounded write is fleet-fatal.
const weaveDrainWriteTimeout = 5 * time.Second

// weaveInboxRedrainInterval is how often the redrain ticker re-drains weave's
// inbox (QUM-925). Deliberately slower than the deleted 2s TUI poll: the poke path
// is instant, so this only has to catch entries no producer poked for.
//
// atomicDuration per the repo-wide CLAUDE.md convention — production reads it from
// the ticker goroutine and tests override it, so a plain time.Duration package var
// would be a data race under -race.
var weaveInboxRedrainInterval = newAtomicDuration(5 * time.Second)

// atomicDuration is the repo-wide shape for a duration knob that production reads
// from a goroutine and tests override. Deliberately duplicated (eight lines,
// unexported) rather than shared — see CLAUDE.md, and the copies in
// internal/backend/session.go, internal/rootinit/consolidating_lock.go, and
// internal/merge/runtests.go.
type atomicDuration struct{ ns atomic.Int64 }

func newAtomicDuration(d time.Duration) *atomicDuration {
	v := &atomicDuration{}
	v.set(d)
	return v
}

func (v *atomicDuration) get() time.Duration  { return time.Duration(v.ns.Load()) }
func (v *atomicDuration) set(d time.Duration) { v.ns.Store(int64(d)) }

// runInboxRedrainTicker re-drains weave's inbox on a slow ticker until the
// returned stop func is called (QUM-925).
//
// WHY THIS IS SAFE WHERE THE OLD 2s TUI POLL WAS NOT — read this before
// concluding "we replaced a poll with a ticker", which would be a lateral move:
//
//  1. SAME DRAINER. It calls drainPendingToStdin, so drainMu serialises it against
//     concurrent pokes. The old poll was a SECOND, INDEPENDENT drainer over a
//     DESTRUCTIVE read (DrainStatusChangeLines removes the envelope), and that is
//     the actual root of its lossiness — not its interval.
//  2. IN-FLIGHT FILTER. InFlightSystemEntryIDs stops it re-injecting anything
//     already written and awaiting its consumption ack. Without it the ticker would
//     BE the write storm it exists to avoid (mutation-measured: 7 writes of one
//     entry in 400ms — at the 60ms test override, not at the 5s production
//     interval; the point is the unbounded growth, not that rate).
//  3. NOT GATED ON TURN STATE. The old poll only fired while turnState == TurnIdle
//     and its reducer discarded the drained frame if a turn had started — that
//     gating was the original QUM-925 defect.
//
// The event-driven poke path (Real.SendMessage / Real.ReportStatus →
// WakeForDelivery) remains the primary and instant delivery mechanism. This exists
// only for entries that reach pending/ with no in-process producer to poke: an
// out-of-process writer dropping an envelope into the maildir directly, or a poke
// swallowed by Real's startedRuntime liveness gate because weave was mid-transition
// when the entry landed. Without a backstop those sit in pending/ indefinitely on
// an idle fleet.
//
// NAMING (deliberate, do not reintroduce "sweep"): "sweep" is already taken twice
// in this subsystem, so a third meaning cost a real false alarm — a reviewer
// flagged a semantic conflict from the name alone, and clearing it took a code
// read plus two messages.
//
//   - TODAY, in this very package: QUM-580's sweepCoordinator / PostTurnSweep
//     (sweep_coordinator.go). Grep "sweep" here and that is what you find.
//   - SOON: QUM-1000's settleNeverAcked, which walks the IN-MEMORY rt.outstanding
//     map and MUTATES entry state without writing anything. (Design doc only at
//     time of writing — docs/research/qum-1000-local-command-strand-design.md —
//     so do not expect a grep to find it.)
//
// This function shares nothing with either that a reader would assume from the
// word: it reads the ON-DISK maildir (pending/) and writes to the CLI's stdin on a
// TIMER. Different data structure, different trigger, different effect.
func (h *WeaveRuntimeHandle) runInboxRedrainTicker() func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			interval := weaveInboxRedrainInterval.get()
			select {
			case <-done:
				return
			case <-time.After(interval):
				h.drainPendingToStdin()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-stopped
	}
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

	// QUM-368: per-turn usage NDJSON recorder for the root weave agent.
	// Construction failure is non-fatal; we skip the subscriber wiring.
	usageRec, _ := usage.NewRecorder(sprawlRoot, name)
	stopUsage := runUsageSubscriber(rt.EventBus(), usageRec, "weave-usage")

	h := &WeaveRuntimeHandle{
		rt:           rt,
		session:      session,
		capabilities: session.Capabilities(),
		sessionID:    session.SessionID(),
		activityFile: activityFile,
		stopActivity: stopActivity,
		stopUsage:    stopUsage,
		sprawlRoot:   sprawlRoot,
		name:         name,
		ring:         ring,
	}
	h.stopInboxRedrain = h.runInboxRedrainTicker()
	// QUM-925: weave may start (or restart, via the QUM-329 handoff cycle) with a
	// non-empty pending/. No producer poke will ever fire for entries that arrived
	// before this process existed, and the 2s TUI poll that used to catch them is
	// gone — so drain once on Start. Start, not here: it runs after the TUI adapter
	// has subscribed to the EventBus, so the frame actually renders.
	rt.SetPostStartHook(h.drainPendingToStdin)
	return h, nil
}

// Interrupt delegates to UnifiedRuntime.Interrupt.
func (h *WeaveRuntimeHandle) Interrupt(ctx context.Context) error {
	return h.rt.Interrupt(ctx)
}

// Wake drains weave's inbox to stdin, mirroring unifiedHandle.Wake (QUM-925).
func (h *WeaveRuntimeHandle) Wake() error {
	h.drainPendingToStdin()
	return nil
}

// WakeForDelivery is the cooperative-wake path, fired unconditionally by the
// producer side on every child report_status / send_message (see Real.SendMessage
// and Real.ReportStatus). It drains weave's inbox straight to the CLI stdin the
// instant the notification arrives, regardless of weave's turn state (QUM-925).
//
// Before QUM-925 this was a no-op: pending entries were left on disk for the TUI's
// peekAndDrainCmd, a 2-second AgentTreeMsg poll that only fired while
// turnState == TurnIdle. That produced the reported bug — a child's report_status
// while weave sat idle accumulated silently and flushed stacked at weave's next
// turn — and, worse, could LOSE a status ping outright: DrainStatusChangeLines is
// destructive, and the TUI's InboxDrainMsg reducer discarded the drained frame if
// a turn had started in the meantime. The poll is gone; this is the sole drainer.
func (h *WeaveRuntimeHandle) WakeForDelivery() error {
	h.drainPendingToStdin()
	return nil
}

// drainPendingToStdin reads the durable maildir + status_change envelopes and
// writes them, tag-wrapped, as ONE kind:system priority-`next` stdin frame
// carrying the maildir entry IDs (QUM-925). The isReplay echo of the write later
// confirms delivery (markConsumed → OnDelivered → MarkDelivered).
//
// Sibling: unifiedHandle.drainPendingToStdin (runtime_launcher.go) for children.
// It deliberately differs in two ways.
//
// 1. PRIORITY. The child path writes interrupt-class entries at priority `now`;
// weave writes every class at `next`. Two load-bearing reasons: the LOCKED
// QUM-925 design states system frames are `next` and STAY `next` through Ctrl+G;
// and a `now` write arms armInterruptLocked (unified.go writeMessage), preempting
// weave's in-flight turn, which contradicts "Esc interrupts the turn but system
// frames remain queued" and the dumb-forwarder rule against timing games.
// (The no-isReplay-echo problem a `now` write also has is NOT part of this
// rationale — ConfirmDeliveredWithoutReplay solves that, as the child path shows.)
// Deliberate, documented consequence: an inter-agent send_message(interrupt=true)
// targeting weave is non-preemptive, an asymmetry vs a child recipient. Restoring
// preemption would be a follow-up issue, not a defect here.
//
// 2. COALESCING. Because both classes share one priority there is nothing for
// interrupts to preempt, so both are emitted in a single frame — interrupt bodies
// first, preserving the old class precedence as ordering within the frame rather
// than as delivery scheduling. Status lines are prepended ahead of both (QUM-559).
func (h *WeaveRuntimeHandle) drainPendingToStdin() {
	h.drainMu.Lock()
	defer h.drainMu.Unlock()

	pending, err := agentloop.ListPending(h.sprawlRoot, h.name)
	if err != nil {
		slog.Default().Debug(
			"weave-runtime: drainPendingToStdin ListPending failed",
			slog.String("agent", h.name),
			slog.Any("err", err),
		)
	}
	// Skip entries already written and awaiting their consumption ack. They stay in
	// pending/ until MarkDelivered, so without this filter every subsequent poke
	// re-injects them — the unbounded stdin write storm measured on the child path.
	if inFlight := h.rt.InFlightSystemEntryIDs(); len(inFlight) > 0 {
		kept := pending[:0]
		for _, e := range pending {
			if _, dup := inFlight[e.ID]; !dup {
				kept = append(kept, e)
			}
		}
		pending = kept
	}

	// WARNING, and the reason the failure path below logs the bodies: this is a
	// DESTRUCTIVE read — messages.DrainStatusChange removes the envelope from
	// the maildir. Unlike the agentloop entries above (a non-destructive peek, safe
	// to re-drain), a status_change line that is drained here and then fails to
	// reach stdin is GONE. That is the same permanent-loss class QUM-925 exists to
	// fix, one layer down, and it cannot be closed by reordering: whether any lines
	// exist is only knowable by draining them. The child path
	// (runtime_launcher.go) has the identical shape. Tracked as a follow-up; the
	// mitigation here is that the bodies are recoverable from the log.
	statusLines := inboxprompt.DrainStatusChangeLines(h.sprawlRoot, h.name)
	if len(pending) == 0 && len(statusLines) == 0 {
		return
	}

	interrupts, asyncs := inboxprompt.SplitByClass(pending)
	ids := make([]string, 0, len(pending))
	var prompt strings.Builder
	for _, line := range statusLines {
		prompt.WriteString(line)
	}
	if len(interrupts) > 0 {
		prompt.WriteString(inboxprompt.BuildInterruptFlushPrompt(interrupts))
		for _, e := range interrupts {
			ids = append(ids, e.ID)
		}
	}
	if len(asyncs) > 0 {
		prompt.WriteString(inboxprompt.BuildQueueFlushPrompt(asyncs))
		for _, e := range asyncs {
			ids = append(ids, e.ID)
		}
	}
	// BOUNDED write. Session.WriteUserMessage selects on ctx.Done(), so a
	// context.Background() here would block forever if claude's stdin pipe is full
	// (64KB kernel buffer, unread) — and because Real.ReportStatus / Real.SendMessage
	// call WakeForDelivery SYNCHRONOUSLY on the MCP handler goroutine, drainMu would
	// then be held forever and every child's report_status / send_message tool call
	// would wedge fleet-wide behind it. A bound degrades to "this notification is
	// late" instead.
	ctx, cancel := context.WithTimeout(context.Background(), weaveDrainWriteTimeout)
	defer cancel()
	if _, err := h.rt.WriteSystemMessage(ctx, prompt.String(), "next", ids); err != nil {
		slog.Default().Warn(
			"weave-runtime: drainPendingToStdin write failed — maildir entries stay in pending/ for the next poke, but any status_change lines in this batch are LOST (destructive drain); their bodies follow so they are recoverable from this log",
			slog.String("agent", h.name),
			slog.Int("lost_status_lines", len(statusLines)),
			slog.String("lost_status_bodies", strings.Join(statusLines, "")),
			slog.Any("err", err),
		)
	}
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
	return h.stopOnceWith(ctx, func(ctx context.Context) error { return h.rt.Stop(ctx) })
}

// StopAbandon is the QUM-600 teardown-only variant of Stop. It tells the
// UnifiedRuntime to skip its polite Session.Interrupt and otherwise
// mirrors Stop's activity-teardown / session-teardown sequence.
func (h *WeaveRuntimeHandle) StopAbandon(ctx context.Context) error {
	return h.stopOnceWith(ctx, func(ctx context.Context) error {
		return h.rt.StopWithOptions(ctx, runtimepkg.StopOptions{SkipPoliteInterrupt: true})
	})
}

// stopOnceWith is the shared body for Stop / StopAbandon. The caller picks
// how the UnifiedRuntime is stopped; everything else is identical.
func (h *WeaveRuntimeHandle) stopOnceWith(ctx context.Context, stopRuntime func(context.Context) error) error {
	h.stopOnce.Do(func() {
		// QUM-925: stop the inbox-redrain ticker FIRST — it holds drainMu and writes
		// to the session, both of which are torn down below.
		if h.stopInboxRedrain != nil {
			joinWithTimeout(h.stopInboxRedrain, stopActivityTimeout,
				"stopInboxRedrain abandoned — likely wedged inbox-redrain goroutine (QUM-925)",
				"handle", "WeaveRuntimeHandle", "agent", h.name)
		}
		err := stopRuntime(ctx)
		if h.stopActivity != nil {
			joinWithTimeout(h.stopActivity, stopActivityTimeout,
				"stopActivity abandoned — likely wedged activity subscriber goroutine (QUM-547)",
				"handle", "WeaveRuntimeHandle", "agent", h.name)
		}
		if h.stopUsage != nil {
			joinWithTimeout(h.stopUsage, stopActivityTimeout,
				"stopUsage abandoned — likely wedged usage subscriber goroutine (QUM-368)",
				"handle", "WeaveRuntimeHandle", "agent", h.name)
		}
		// QUM-545: shared Close → Kill teardown helper. waitTimeout=0 means
		// "skip Wait" — see teardown_session.go: legacy bridge.Close (which
		// this path replaces) only invokes Close+Kill, relying on the OS to
		// reap later. Calling Wait synchronously here makes /proc/<old-pid>/stat
		// disappear immediately, breaking scripts/test-handoff-e2e.sh's
		// parent-PID fallback path (assertion #4).
		teardownSession(h.session, 0)
		if h.activityFile != nil || h.activityClose != nil {
			closer := h.activityClose
			if closer == nil {
				closer = h.activityFile.Close
			}
			joinWithTimeout(func() { _ = closer() }, activityCloseTimeout,
				"activityFile.Close abandoned — likely stuck FD on activity.ndjson (QUM-547)",
				"handle", "WeaveRuntimeHandle", "agent", h.name)
		}
		if err != nil && !isExitError(err) {
			h.stopErr = err
		}
	})
	return h.stopErr
}

// SessionID returns the underlying session ID captured at construction.
func (h *WeaveRuntimeHandle) SessionID() string { return h.sessionID }

// InTurn reports whether the underlying backend session is
// currently servicing an autonomous (SDK-initiated) turn frame. See
// QUM-585 — surfaced through the peek MCP tool's JSON payload.
func (h *WeaveRuntimeHandle) InTurn() bool { return h.session.InTurn() }

// LastActivityAt returns the timestamp of the most recently recorded
// activity-ring entry on this runtime. Zero time when the ring is empty.
// (QUM-665)
func (h *WeaveRuntimeHandle) LastActivityAt() time.Time {
	if h.ring == nil {
		return time.Time{}
	}
	return h.ring.LastAt()
}

// IsTerminallyFaulted reports whether the underlying backend session has been
// poisoned with a sticky terminal error (QUM-601). Mirrors unifiedHandle.
func (h *WeaveRuntimeHandle) IsTerminallyFaulted() bool { return h.session.IsTerminallyFaulted() }

// Capabilities returns the backend capabilities reported at construction.
func (h *WeaveRuntimeHandle) Capabilities() backendpkg.Capabilities { return h.capabilities }

// Done returns a channel that closes when the underlying runtime exits.
func (h *WeaveRuntimeHandle) Done() <-chan struct{} { return h.rt.Done() }

// UnifiedRuntime returns the underlying UnifiedRuntime so consumers (e.g.
// the TUI viewport stream wiring — QUM-439) can subscribe to its EventBus.
func (h *WeaveRuntimeHandle) UnifiedRuntime() *runtimepkg.UnifiedRuntime { return h.rt }
