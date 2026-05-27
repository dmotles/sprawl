package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-595: host stdout-reader wedge fix knobs and sentinels. Forensic doc:
// docs/research/permission-hang-forensic-2026-05-19.md.
//
// subscriberSendDeadline bounds how long runReader will block on a
// per-turn subscriber send before declaring the consumer wedged and
// faulting the session (F1). Exposed as a package var (not const) so
// tests can override; production code must not mutate.
//
// hangCheckInterval is the watchdog tick cadence (D1). Same override
// pattern.
var (
	subscriberSendDeadline = 5 * time.Second
	hangCheckInterval      = 1 * time.Minute
	// interruptSendTimeout bounds how long Session.Interrupt will wait on
	// transport.Send before declaring the wire send wedged and returning
	// ErrInterruptTimeout (QUM-600). The real-world wedge is a stuck
	// claude stdin pipe whose write blocks below the ctx-checking layer
	// of the adapter, so a plain ctx.Done() select on Send is not enough.
	// Exposed as a package var (not const) so tests can override;
	// production code must not mutate.
	interruptSendTimeout = 2 * time.Second
)

const (
	// observerQueueDepth bounds the per-session async Observer queue (F2).
	// Frames beyond this depth are dropped and counted in
	// Stats.ObserverDrops; the reader never blocks on a slow Observer.
	observerQueueDepth = 256
	// defaultHangTimeout applies when SessionConfig.HangTimeout is zero.
	defaultHangTimeout = 10 * time.Minute
)

// ErrSubscriberWedged is the sentinel fatal error set on the session when
// runReader's per-turn subscriber send exceeds subscriberSendDeadline (F1).
// Callers see it via LastTurnError; subsequent StartTurn returns it directly.
var ErrSubscriberWedged = errors.New("backend: subscriber send exceeded deadline (host reader wedged)")

// ErrHangTimeout is the sentinel fatal error set by the D1 watchdog when no
// frames arrive within SessionConfig.HangTimeout.
var ErrHangTimeout = errors.New("backend: reader hang timeout (no frames within HangTimeout)")

// ErrInterruptTimeout is returned by Session.Interrupt when the bounded
// transport.Send wrapper expires (QUM-600). The wedge mode it guards is a
// stuck claude stdin pipe whose OS-level write does not honor ctx; the
// in-flight async-MCP-handler cancellation still fires before the wrapper
// waits on the wire, so callers can safely fall through to teardown.
var ErrInterruptTimeout = errors.New("backend: interrupt send exceeded deadline (stdin writer wedged)")

// Stats reports per-session drop counters surfaced for observability /
// forensics. All fields are atomic-backed snapshots.
type Stats struct {
	// SubscriberDrops increments each time the per-turn subscriber send
	// path hits subscriberSendDeadline (F1).
	SubscriberDrops int64
	// ObserverDrops increments each time a frame is dropped because the
	// async Observer queue is full (F2).
	ObserverDrops int64
}

// ManagedTransport is the shared subprocess transport contract for backend
// sessions. It extends the stream-json send/recv path with lifecycle hooks so
// callers can choose graceful close+wait or forceful kill semantics.
type ManagedTransport interface {
	Send(ctx context.Context, msg any) error
	Recv(ctx context.Context) (*protocol.Message, error)
	Close() error
	Wait() error
	Kill() error
	// Pid returns the OS process ID of the backing subprocess, or 0 if
	// no subprocess is attached (in-memory test transports). Used by
	// the QUM-606 live-recover smoke harness and adapter unit tests to
	// assert subprocess lifetime independent of `ps` scraping.
	Pid() int
}

// Observer receives every inbound protocol message before backend control
// handling mutates flow.
type Observer interface {
	OnMessage(msg *protocol.Message)
}

// ToolBridge handles host-managed MCP messages.
type ToolBridge interface {
	HandleIncoming(ctx context.Context, serverName string, msg json.RawMessage) (json.RawMessage, error)
}

// Capabilities records the runtime features supported by a backend.
type Capabilities struct {
	SupportsInterrupt  bool
	SupportsResume     bool
	SupportsToolBridge bool
}

// SessionSpec is the backend-neutral launch/session contract the callers build
// and adapters interpret.
type SessionSpec struct {
	WorkDir         string
	Identity        string
	SprawlRoot      string
	SessionID       string
	PromptFile      string
	Model           string
	Effort          string
	PermissionMode  string
	AllowedTools    []string
	DisallowedTools []string
	// Agents carries the JSON payload for claude's `--agents` flag (Claude
	// Code sub-agent definitions). Empty means the flag is omitted. See
	// internal/agent.TDDSubAgentsJSON() for the engineer payload (QUM-408).
	Agents          string
	AdditionalEnv   map[string]string
	Resume          bool
	Stderr          io.Writer
	OnResumeFailure func()
	Observer        Observer
}

// InitSpec carries optional host-side session initialization data.
type InitSpec struct {
	MCPServerNames []string
	ToolBridge     ToolBridge
}

// TurnSpec carries optional per-turn overrides. Kept as an empty struct
// for variadic compatibility with the Session.StartTurn signature; all
// session-scoped state lives on the session and is established via
// Initialize. The field formerly named Init was removed in QUM-570.
type TurnSpec struct{}

// SessionConfig configures a Session instance.
type SessionConfig struct {
	SessionID    string
	Identity     string
	Capabilities Capabilities
	Observer     Observer
	// HangTimeout configures the D1 reader-loop hang watchdog. Zero means
	// defaultHangTimeout. Negative disables the watchdog entirely.
	HangTimeout time.Duration
}

// Session is the shared session contract root and child adapters compile
// against.
type Session interface {
	// Start launches the persistent stream reader goroutine. Idempotent.
	// Auto-called from Initialize / StartTurn if not yet invoked.
	Start(ctx context.Context) error
	Initialize(ctx context.Context, spec InitSpec) error
	StartTurn(ctx context.Context, prompt string, spec ...TurnSpec) (<-chan *protocol.Message, error)
	Interrupt(ctx context.Context) error
	Close() error
	Wait() error
	Kill() error
	LastTurnError() error
	SessionID() string
	Capabilities() Capabilities
	// InAutonomousTurn reports whether the session is currently servicing
	// an autonomous (SDK-initiated) turn frame — opened by a system:init
	// while no StartTurn was pending. Returns false when no turn is in
	// flight and false during sprawl-initiated turns.
	InAutonomousTurn() bool
	// BackendStats returns an atomic snapshot of per-session drop counters
	// (QUM-595). See Stats.
	BackendStats() Stats
	// IsTerminallyFaulted reports whether the session's sticky terminalErr
	// has been set (QUM-601). Used by the runtime-level Recover path to
	// decide whether in-place recovery is needed.
	IsTerminallyFaulted() bool
	// InduceTerminalFault forces the session into the terminally-faulted
	// state with the supplied sentinel error. Provided for the QUM-606
	// live-recover e2e harness: an external caller (the build-tag-gated
	// `_test_induce_wedge` MCP tool) needs a deterministic way to drive
	// a SubscriberWedge / HangTimeout fault without inducing a real
	// frame burst or stalled reader. Production callers MUST NOT use
	// this — it bypasses the real fault detectors.
	InduceTerminalFault(err error)
}

// ErrTurnInProgress is returned when callers try to start a second concurrent
// turn on the same stream-json session.
var ErrTurnInProgress = errors.New("backend: turn already in progress")

// turnFrame tracks per-turn state in the persistent stream reader.
// Sprawl-initiated turns (created by StartTurn) have a subscriber channel;
// autonomous turns (frames the SDK emits without a preceding StartTurn,
// detected by a `system`/`init` frame while currentTurn is nil) are tracked
// with autonomous=true and a nil subscriber so the next StartTurn can wait
// for the in-flight autonomous turn to end before issuing its own prompt.
type turnFrame struct {
	startedAt  time.Time
	subscriber chan *protocol.Message
	done       chan struct{} // closed by reader when the frame ends
	lastErr    error
	autonomous bool
	// ctx is the per-turn context threaded in by StartTurn (QUM-618). nil for
	// autonomous frames. When this ctx is cancelled (per-turn deadline or
	// parent cancel) the watchTurnCtx goroutine clears currentTurn so the next
	// StartTurn is accepted instead of returning ErrTurnInProgress.
	ctx context.Context
	// closeOnce makes close(done) idempotent across the watcher, the
	// result-frame block, and the orphan-teardown defer — preventing a
	// double-close panic.
	closeOnce sync.Once
}

// finish closes tf.done exactly once. It does NOT touch tf.subscriber — the
// reader exclusively owns the subscriber channel's lifetime.
func (tf *turnFrame) finish() {
	tf.closeOnce.Do(func() { close(tf.done) })
}

type session struct {
	transport ManagedTransport
	config    SessionConfig
	reqSeq    atomic.Int64

	mu          sync.Mutex
	initSpec    InitSpec
	currentTurn *turnFrame
	fatalErr    error
	// terminalErr is sticky once set. Unlike fatalErr (which LastTurnError
	// consumes), terminalErr remains observable so subsequent StartTurn
	// calls reject quickly after the session has been faulted by a
	// non-recoverable reader-side fault (F1 wedge / D1 hang).
	terminalErr error
	started     bool

	// readerCtx/readerCancel control the persistent stream reader.
	readerCtx    context.Context
	readerCancel context.CancelFunc
	readerDone   chan struct{}

	// QUM-595 wedge-fix surfaces.
	// observerCh carries Observer dispatch out of the reader hot path (F2).
	// Buffered observerQueueDepth; reader uses non-blocking select-default
	// send, increments observerDrops on overflow. The drain goroutine
	// reads serially to preserve arrival order.
	observerCh      chan *protocol.Message
	observerDone    chan struct{}
	observerDrops   atomic.Int64
	subscriberDrops atomic.Int64
	// lastFrameAt is the monotonic unix-nano timestamp of the most recent
	// frame observed by runReader. Read by the D1 watchdog. Initialized to
	// the session-start instant so the watchdog has a baseline before the
	// first frame.
	lastFrameAt  atomic.Int64
	watchdogDone chan struct{}

	// Init handshake bookkeeping. initRequestID is the in-flight initialize
	// control_request id (cleared once matched); initHandshakeResp is the
	// reader-side delivery channel.
	initRequestID     string
	initHandshakeResp chan *protocol.Message

	// inflight tracks the cancellation funcs of in-flight async MCP
	// handlers, keyed by control_request request_id. inflightWG waits
	// for those goroutines on session shutdown. See
	// handleInlineControlRequest for the rationale.
	inflight   map[string]context.CancelFunc
	inflightWG sync.WaitGroup

	// terminalErrHandler is a one-shot callback installed by the runtime
	// (QUM-602). It fires the FIRST time setTerminalErr is called on this
	// session, OUTSIDE s.mu so the handler can call back into session-safe
	// read methods (e.g. BackendStats) without deadlock. nil clears.
	terminalErrHandler atomic.Pointer[func(error)]

	// autonomousFrameHandler, if installed by the runtime (QUM-631), is invoked
	// for every frame routed to an autonomous (SDK/harness-initiated) turn —
	// the turnFrame opened by a system:init while no sprawl StartTurn was
	// pending. Lets the runtime surface harness-initiated turns to the EventBus
	// (and thus the TUI), which would otherwise be dropped (they have a nil
	// subscriber). Invoked synchronously from runReader; the handler MUST be
	// non-blocking (the runtime's handler only does a bounded EventBus.Publish).
	// Distinct from Observer, which sees EVERY frame regardless of turn kind.
	autonomousFrameHandler atomic.Pointer[func(*protocol.Message)]

	// pendingTrigger holds a pre-init system/task_notification frame (QUM-634).
	// It arrives ~6ms before the system/init that opens the autonomous turn, so
	// it's stashed single-slot here and emitted to autonomousFrameHandler just
	// before the init frame. Single-slot: a later task_notification overwrites
	// it; the next autonomous init or any StartTurn consumes/clears it. Passive
	// capture — never allocates a turnFrame or gates StartTurn (QUM-570).
	pendingTrigger *protocol.Message
}

// inflightDrainTimeout bounds how long the reader waits for in-flight async
// MCP handlers to finish on shutdown. A wedged handler must not be able to
// permanently leak the session goroutine.
var inflightDrainTimeout = 5 * time.Second

// NewSession creates a backend session on top of the provided transport.
func NewSession(t ManagedTransport, cfg SessionConfig) Session {
	return &session{
		transport:         t,
		config:            cfg,
		inflight:          make(map[string]context.CancelFunc),
		readerDone:        make(chan struct{}),
		initHandshakeResp: make(chan *protocol.Message, 1),
		observerCh:        make(chan *protocol.Message, observerQueueDepth),
		observerDone:      make(chan struct{}),
		watchdogDone:      make(chan struct{}),
	}
}

func (s *session) SessionID() string {
	return s.config.SessionID
}

func (s *session) Capabilities() Capabilities {
	return s.config.Capabilities
}

// InAutonomousTurn reports whether an autonomous (SDK-initiated) turn frame
// is currently in flight. Race-safe under the persistent reader goroutine —
// see TestSession_InAutonomousTurn_RaceSafeUnderConcurrentReader.
func (s *session) InAutonomousTurn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentTurn != nil && s.currentTurn.autonomous
}

func (s *session) nextRequestID() string {
	n := s.reqSeq.Add(1)
	return fmt.Sprintf("req-%d", n)
}

// Start launches the persistent stream reader. Idempotent.
//
// The reader ctx is detached from the caller's ctx so the reader survives
// any caller-ctx cancellation, matching the UnifiedRuntime.Start precedent.
func (s *session) Start(_ context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = true
	readerCtx, cancel := context.WithCancel(context.Background())
	s.readerCtx = readerCtx
	s.readerCancel = cancel
	s.mu.Unlock()

	// Seed the watchdog baseline so the first tick doesn't immediately
	// fault on an empty stream (D1).
	s.lastFrameAt.Store(time.Now().UnixNano())

	go s.runReader(readerCtx)
	go s.runObserverDrain()
	if hangTimeout := s.effectiveHangTimeout(); hangTimeout > 0 {
		go s.runHangWatchdog(readerCtx, hangTimeout)
	} else {
		// Negative HangTimeout disables the watchdog; close its done
		// channel so Close()'s join is a no-op.
		close(s.watchdogDone)
	}
	return nil
}

// effectiveHangTimeout resolves SessionConfig.HangTimeout into the actual
// duration the D1 watchdog uses. Zero → defaultHangTimeout. Negative → 0
// (disabled, caller checks > 0).
func (s *session) effectiveHangTimeout() time.Duration {
	switch {
	case s.config.HangTimeout < 0:
		return 0
	case s.config.HangTimeout == 0:
		return defaultHangTimeout
	default:
		return s.config.HangTimeout
	}
}

func (s *session) Initialize(ctx context.Context, spec InitSpec) error {
	if err := s.Start(ctx); err != nil {
		return err
	}

	requestID := s.nextRequestID()
	s.mu.Lock()
	s.initSpec = spec
	s.initRequestID = requestID
	s.mu.Unlock()

	request := map[string]any{
		"subtype": "initialize",
	}
	if len(spec.MCPServerNames) > 0 {
		request["sdkMcpServers"] = spec.MCPServerNames
	}
	msg := map[string]any{
		"type":       "control_request",
		"request_id": requestID,
		"request":    request,
	}

	if err := s.transport.Send(ctx, msg); err != nil {
		return err
	}

	select {
	case <-s.initHandshakeResp:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.readerDone:
		return errors.New("backend: session reader exited before initialize handshake")
	}
}

func (s *session) StartTurn(ctx context.Context, prompt string, _ ...TurnSpec) (<-chan *protocol.Message, error) {
	if err := s.Start(ctx); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.terminalErr != nil {
		err := s.terminalErr
		s.mu.Unlock()
		return nil, err
	}
	if s.fatalErr != nil {
		err := s.fatalErr
		s.mu.Unlock()
		return nil, err
	}
	for s.currentTurn != nil {
		if !s.currentTurn.autonomous {
			s.mu.Unlock()
			return nil, ErrTurnInProgress
		}
		done := s.currentTurn.done
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		s.mu.Lock()
		if s.terminalErr != nil {
			err := s.terminalErr
			s.mu.Unlock()
			return nil, err
		}
		if s.fatalErr != nil {
			err := s.fatalErr
			s.mu.Unlock()
			return nil, err
		}
	}
	tf := &turnFrame{
		startedAt:  time.Now(),
		subscriber: make(chan *protocol.Message, 100),
		done:       make(chan struct{}),
		ctx:        ctx,
	}
	s.currentTurn = tf
	// QUM-634: a sprawl turn means any stashed pre-init trigger is no longer the
	// immediately-preceding cause of an autonomous init; clear it so it can't
	// leak onto a later autonomous turn.
	s.pendingTrigger = nil
	// QUM-599: re-seed the watchdog baseline so the first tick of this
	// turn doesn't immediately fault on a stale lastFrameAt left over
	// from a long between-turn idle gap.
	s.lastFrameAt.Store(time.Now().UnixNano())
	s.mu.Unlock()

	msg := protocol.UserMessage{
		Type: "user",
		Message: protocol.MessageParam{
			Role:    "user",
			Content: prompt,
		},
	}
	if err := s.transport.Send(ctx, msg); err != nil {
		s.mu.Lock()
		tf.lastErr = err
		s.currentTurn = nil
		s.mu.Unlock()
		close(tf.subscriber)
		tf.finish()
		return nil, err
	}

	// Spawn the per-turn ctx watcher AFTER Send success so the send-error fast
	// path above (which already set currentTurn=nil + finish()) never leaves an
	// orphan watcher. Only sprawl-initiated turns carry a non-nil ctx.
	if ctx != nil {
		go s.watchTurnCtx(tf)
	}

	return tf.subscriber, nil
}

// watchTurnCtx clears currentTurn deterministically when the per-turn ctx is
// cancelled (deadline or parent cancel) so the NEXT StartTurn is accepted
// instead of returning ErrTurnInProgress (QUM-618). Exits without side effects
// on clean completion (tf.done closed by the result frame / reader teardown).
func (s *session) watchTurnCtx(tf *turnFrame) {
	select {
	case <-tf.done:
		return
	case <-tf.ctx.Done():
		s.mu.Lock()
		if s.currentTurn == tf {
			s.currentTurn = nil
		}
		s.mu.Unlock()
		tf.finish() // does NOT close tf.subscriber — reader owns it
	}
}

// runReader is the persistent stream-reader loop. It services transport.Recv
// for the lifetime of the session, multiplexing frames into the current
// turn's subscriber (sprawl-initiated turn) or into a synthesized autonomous
// turn frame (no subscriber). On a `result` frame the current turn ends.
//
// Control_request frames (mcp_message / can_use_tool / ...) are handled
// inline by handleInlineControlRequest; mcp_message specifically dispatches
// to ToolBridge asynchronously so the reader keeps draining stdout even
// while a long-running MCP tool is in flight (QUM-552).
func (s *session) runReader(ctx context.Context) {
	// Snapshot the F1 deadline at goroutine entry so tests overriding
	// the package var for a different session can't race with us. The
	// production knob is constant for the process lifetime.
	sendDeadline := subscriberSendDeadline

	defer close(s.readerDone)
	defer func() {
		// Orphan teardown: drop a faulted/cancelled current turn so
		// StartTurn callers blocked on tf.done unwind. This must run
		// AFTER the observer drain (defer below) has flushed so any
		// test reading observer state on events-chan close sees the
		// frames the reader produced.
		s.mu.Lock()
		if cur := s.currentTurn; cur != nil {
			if cur.lastErr == nil {
				if s.fatalErr != nil {
					cur.lastErr = s.fatalErr
				} else {
					cur.lastErr = context.Canceled
				}
			}
			if cur.subscriber != nil {
				close(cur.subscriber)
			}
			cur.finish()
			s.currentTurn = nil
		}
		s.mu.Unlock()
	}()
	defer s.drainInflight()
	// F2: close observerCh so the drain goroutine exits, then wait
	// (bounded) for it to flush. This runs FIRST (LIFO order — declared
	// last after the orphan teardown defer) so that callers consuming the
	// events chan see end-of-stream only after the observer has been
	// drained, preserving the pre-QUM-595 ordering guarantee that
	// `for range events` exit implies the observer has seen the same
	// frames.
	defer func() {
		close(s.observerCh)
		select {
		case <-s.observerDone:
		case <-time.After(inflightDrainTimeout):
		}
	}()

	for {
		msg, err := s.transport.Recv(ctx)
		if err != nil {
			s.mu.Lock()
			if s.fatalErr == nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					s.fatalErr = ctxErr
				} else {
					s.fatalErr = err
				}
			}
			s.mu.Unlock()
			return
		}
		if msg == nil {
			continue
		}

		// D1: update the watchdog baseline on every frame we observe.
		s.lastFrameAt.Store(time.Now().UnixNano())

		// F2: dispatch Observer asynchronously. The reader never blocks
		// on a slow Observer; overflow surfaces in ObserverDrops.
		if s.config.Observer != nil {
			select {
			case s.observerCh <- msg:
			default:
				s.observerDrops.Add(1)
			}
		}

		if msg.Type == "control_response" {
			if s.matchInitHandshake(msg) {
				continue
			}
			// Other control_responses are observed but not currently
			// routed; fall through to the turn frame for visibility.
		}

		if msg.Type == "control_request" {
			s.handleInlineControlRequest(ctx, msg)
			continue
		}

		// Route the frame. If a sprawl turn is active, deliver to its
		// subscriber. Otherwise, when a `system`/`init` frame arrives with
		// no in-flight turn, allocate an autonomous turnFrame so the next
		// StartTurn can ctx-cancellably wait for it to close on `result`
		// (QUM-578 explicit start/end markers). Stray non-init frames are
		// observer-only — never allocate a frame, never block StartTurn —
		// which avoids the QUM-570 deadlock class where the coarse
		// classifier would gate forever on between-turn telemetry that
		// never ended in a `result`.
		s.mu.Lock()
		if s.currentTurn == nil && msg.Type == "system" && msg.Subtype == "task_notification" {
			// QUM-634: stash the pre-init trigger. Observer already saw it (above);
			// do NOT allocate a turnFrame (QUM-570 — stray telemetry never gates).
			s.pendingTrigger = msg
			s.mu.Unlock()
			continue
		}
		var pendingTrigger *protocol.Message
		if s.currentTurn == nil && msg.Type == "system" && msg.Subtype == "init" {
			s.currentTurn = &turnFrame{
				startedAt:  time.Now(),
				subscriber: nil,
				done:       make(chan struct{}),
				autonomous: true,
			}
			pendingTrigger = s.pendingTrigger
			s.pendingTrigger = nil
		}
		tf := s.currentTurn
		s.mu.Unlock()

		// QUM-631: surface autonomous-turn frames (harness self-reprompt) to the
		// runtime so the TUI renders them. Autonomous turns have a nil subscriber,
		// so without this they reach only the async Observer and are dropped from
		// the live view. Only frames belonging to an autonomous turnFrame are
		// forwarded; stray non-init telemetry never allocates a frame (QUM-570) and
		// sprawl-initiated turns flow through the subscriber channel (QUM-578).
		if tf != nil && tf.autonomous {
			if hp := s.autonomousFrameHandler.Load(); hp != nil {
				// QUM-634: emit the stashed trigger BEFORE the init frame so the TUI
				// renders the auto-continue marker before the assistant response.
				if pendingTrigger != nil {
					(*hp)(pendingTrigger)
				}
				(*hp)(msg)
			}
		}

		if tf != nil && tf.subscriber != nil {
			// F1: bound the subscriber send. A wedged consumer must not
			// stall the reader — once subscriberSendDeadline elapses we
			// fault the session with ErrSubscriberWedged so StartTurn
			// waiters unwind cleanly. See
			// docs/research/permission-hang-forensic-2026-05-19.md.
			//
			// QUM-618: also race the per-turn ctx. When the turn was abandoned
			// (per-turn deadline / parent cancel), the watcher has already
			// cleared currentTurn and the consumer is gone by contract; drop
			// the frame WITHOUT faulting as SubscriberWedged. A nil turnDone
			// channel blocks forever in select — correct legacy behaviour for
			// autonomous frames.
			var turnDone <-chan struct{}
			if tf.ctx != nil {
				turnDone = tf.ctx.Done()
			}
			timer := time.NewTimer(sendDeadline)
			select {
			case tf.subscriber <- msg:
				timer.Stop()
			case <-turnDone:
				// QUM-618: per-turn ctx cancelled (deadline/abort). The consumer
				// is gone by contract; drop the frame, do NOT fault as
				// SubscriberWedged.
				timer.Stop()
			case <-ctx.Done(): // readerCtx — session shutdown
				timer.Stop()
				return
			case <-timer.C:
				s.subscriberDrops.Add(1)
				s.setTerminalErr(ErrSubscriberWedged)
				s.readerCancel()
				return
			}
		}

		if msg.Type == "result" && tf != nil {
			s.mu.Lock()
			cur := s.currentTurn
			s.currentTurn = nil
			s.mu.Unlock()
			if cur != nil {
				if cur.subscriber != nil {
					close(cur.subscriber)
				}
				cur.finish()
			}
		}
	}
}

// runObserverDrain consumes observerCh serially and invokes
// Observer.OnMessage in arrival order. The reader's send is non-blocking,
// so an Observer that wedges OnMessage only stalls this goroutine — never
// the reader. Exits when observerCh is closed (by runReader's defer).
func (s *session) runObserverDrain() {
	defer close(s.observerDone)
	for msg := range s.observerCh {
		if s.config.Observer != nil {
			s.config.Observer.OnMessage(msg)
		}
	}
}

// runHangWatchdog is the D1 reader-loop hang detector. Every
// hangCheckInterval it compares time.Since(lastFrameAt) against
// hangTimeout. On hang it sets ErrHangTimeout as terminal, cancels the
// reader (so transport.Recv unwinds), and exits. Exits cleanly on
// readerCtx cancellation.
//
// QUM-599: the watchdog is gated on turn-active state. Between turns
// (s.currentTurn == nil) claude legitimately goes silent — no keepalive,
// no telemetry — so measuring lastFrameAt against hangTimeout would
// terminally brick the session on idle. We only check for staleness while
// a turn (sprawl-initiated or autonomous) is in flight.
func (s *session) runHangWatchdog(ctx context.Context, hangTimeout time.Duration) {
	defer close(s.watchdogDone)
	// Snapshot the package var at goroutine entry so tests that override
	// the interval after this session was created can't race with us. The
	// production knob is constant for the process lifetime.
	interval := hangCheckInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.readerDone:
			// Reader exited (e.g., transport EOF) without our help —
			// piggyback the exit so we don't leak.
			return
		case <-ticker.C:
			// QUM-599: skip the staleness check when no turn is in
			// flight — between-turn idle is not a hang.
			s.mu.Lock()
			turnActive := s.currentTurn != nil
			s.mu.Unlock()
			if !turnActive {
				continue
			}
			last := s.lastFrameAt.Load()
			if last == 0 {
				continue
			}
			if time.Since(time.Unix(0, last)) >= hangTimeout {
				s.setTerminalErr(ErrHangTimeout)
				s.readerCancel()
				return
			}
		}
	}
}

// matchInitHandshake non-blocking-delivers the initialize control_response
// to Initialize's waiter if the request_id matches. Returns true if the
// frame was consumed.
func (s *session) matchInitHandshake(msg *protocol.Message) bool {
	var cr struct {
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if err := json.Unmarshal(msg.Raw, &cr); err != nil {
		return false
	}
	s.mu.Lock()
	want := s.initRequestID
	if want == "" || cr.Response.RequestID != want {
		s.mu.Unlock()
		return false
	}
	s.initRequestID = ""
	s.mu.Unlock()
	select {
	case s.initHandshakeResp <- msg:
	default:
	}
	return true
}

// handleInlineControlRequest routes an incoming control_request frame.
//
// Async dispatch (QUM-552, follow-up to QUM-549):
//
// `mcp_message` subtypes are dispatched in a goroutine so that the
// persistent reader keeps consuming claude's stdout while a long-running
// MCP tool handler runs. Without this, `Session.Interrupt` is unobservable
// mid-tool-wait and the EventBus goes silent for the entire wedge window
// (see docs/research/qum-549-send-interrupt-during-mcp-tool-wait.md).
//
// Only `mcp_message` is asynchronous. `can_use_tool` (and any future
// subtype that resolves synchronously off a static decision) stays in
// the reader loop — those handlers are fast, do not call out through
// ToolBridge, and dispatching them async would only add scheduling
// overhead and reorder responses on the wire.
//
// Lifecycle invariant: each async handler registers its cancelFunc in
// s.inflight under cr.RequestID and increments s.inflightWG. On
// completion the entry is removed. On session shutdown (reader return),
// drainInflight cancels every remaining ctx and bounded-waits
// inflightDrainTimeout for the goroutines to finish so a wedged
// handler cannot permanently leak the session.
//
// Wire-level safety: protocol.Writer.mu (see internal/protocol/writer.go)
// serializes stdin writes, so concurrent transport.Send calls from
// multiple in-flight handlers are safe.
func (s *session) handleInlineControlRequest(ctx context.Context, msg *protocol.Message) {
	var cr struct {
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(msg.Raw, &cr); err != nil {
		s.setFatalErr(err)
		return
	}

	var req struct {
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(cr.Request, &req); err != nil {
		s.setFatalErr(err)
		return
	}

	if req.Subtype == "mcp_message" {
		s.mu.Lock()
		initSpec := s.initSpec
		s.mu.Unlock()
		s.dispatchMCPAsync(ctx, cr.RequestID, cr.Request, initSpec)
		return
	}

	resp := protocol.ControlResponse{
		Type: "control_response",
		Response: protocol.ControlResponseInner{
			Subtype:   "success",
			RequestID: cr.RequestID,
		},
	}

	if req.Subtype == "can_use_tool" {
		resp.Response.Response = map[string]any{
			"behavior":  "allow",
			"toolUseID": "",
			"message":   "Allowed by host",
		}
	}

	if err := s.transport.Send(ctx, resp); err != nil {
		s.setFatalErr(fmt.Errorf("sending control response: %w", err))
	}
}

// dispatchMCPAsync launches ToolBridge.HandleIncoming in a goroutine,
// tracks its cancelFunc in s.inflight, and writes the eventual
// control_response when the handler returns. See
// handleInlineControlRequest for the architectural rationale.
func (s *session) dispatchMCPAsync(parentCtx context.Context, requestID string, rawRequest json.RawMessage, initSpec InitSpec) {
	resp := protocol.ControlResponse{
		Type: "control_response",
		Response: protocol.ControlResponseInner{
			Subtype:   "success",
			RequestID: requestID,
		},
	}

	if initSpec.ToolBridge == nil {
		// No bridge wired: send an empty success response synchronously
		// to preserve the prior best-effort behavior.
		if err := s.transport.Send(parentCtx, resp); err != nil {
			s.setFatalErr(fmt.Errorf("sending control response: %w", err))
		}
		return
	}

	var mcpReq struct {
		ServerName string          `json:"server_name"`
		Message    json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(rawRequest, &mcpReq); err != nil {
		// Malformed payload: respond with empty success, matching the
		// previous code path (which silently dropped the unmarshal error).
		if sendErr := s.transport.Send(parentCtx, resp); sendErr != nil {
			s.setFatalErr(fmt.Errorf("sending control response: %w", sendErr))
		}
		return
	}

	bridgeCtx, cancel := context.WithCancel(parentCtx)
	if s.config.Identity != "" {
		bridgeCtx = WithCallerIdentity(bridgeCtx, s.config.Identity)
	}

	s.mu.Lock()
	s.inflight[requestID] = cancel
	s.mu.Unlock()
	s.inflightWG.Add(1)

	go func() {
		defer s.inflightWG.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, requestID)
			s.mu.Unlock()
			cancel()
		}()

		mcpResp, mcpErr := initSpec.ToolBridge.HandleIncoming(bridgeCtx, mcpReq.ServerName, mcpReq.Message)
		if mcpErr != nil {
			resp.Response.Subtype = "error"
			resp.Response.Response = map[string]any{"error": mcpErr.Error()}
		} else {
			resp.Response.Response = map[string]any{"mcp_response": mcpResp}
		}

		// Use parentCtx for the send: bridgeCtx may have been cancelled
		// at shutdown, but we still want to flush the response if the
		// underlying transport is still alive.
		if err := s.transport.Send(parentCtx, resp); err != nil {
			s.setFatalErr(fmt.Errorf("sending control response: %w", err))
		}
	}()
}

// drainInflight cancels every in-flight async handler and waits up to
// inflightDrainTimeout for them to finish. Called from runReader's defer
// once per session shutdown.
func (s *session) drainInflight() {
	s.mu.Lock()
	for _, cancel := range s.inflight {
		cancel()
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.inflightWG.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(inflightDrainTimeout):
	}
}

func (s *session) Interrupt(ctx context.Context) error {
	// Cancel every in-flight async MCP handler ctx FIRST (QUM-552 S3 +
	// QUM-600). This MUST run before the bounded wire-send wrapper waits
	// on transport.Send so that, even when the stdin writer is wedged and
	// the Send goroutine leaks, ctx-respecting tool handlers unwind
	// immediately. The cancellation invariant is decoupled from the wire
	// send succeeding.
	//
	// Why cancel on the outgoing Interrupt (not on observed
	// EventInterrupted from claude's stdout):
	//   - we control this call site, so cancellation is synchronous
	//     and atomic with the wire-level interrupt request;
	//   - observing EventInterrupted would arrive later and race with
	//     normal handler completion;
	//   - ctx-respecting handlers (retire/delegate/merge/ask_user_question)
	//     will unwind immediately. ask_user_question became ctx-respecting
	//     after QUM-552 (see internal/supervisor/question.go ~q.ask) — the
	//     entry's <-ctx.Done() branch fires cancelInternal and returns
	//     OutcomeSessionEnded; the original QUM-553-era exception is gone.
	// Entries are NOT removed here; the dispatch goroutines own
	// their inflight-map cleanup on completion.
	s.mu.Lock()
	for _, cancel := range s.inflight {
		cancel()
	}
	s.mu.Unlock()

	requestID := s.nextRequestID()
	msg := protocol.InterruptRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   protocol.InterruptRequestInner{Subtype: "interrupt"},
	}

	// Bound transport.Send by interruptSendTimeout (QUM-600). The wedge
	// mode is a stuck claude stdin pipe whose OS-level write blocks below
	// the ctx-checking layer of the adapter, so a plain ctx-respecting
	// Send is not enough. The Send goroutine intentionally leaks on
	// timeout — acceptable per the QUM-542 teardown precedent; the parent
	// session is on its way to Close+Kill which will unblock the
	// underlying FD eventually.
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.transport.Send(ctx, msg)
	}()
	select {
	case err := <-errCh:
		return err
	case <-time.After(interruptSendTimeout):
		return ErrInterruptTimeout
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *session) Close() error {
	// Cancel the reader ctx first so the reader's loop unwinds and async
	// MCP handlers see their parentCtx cancellation before we tear down
	// the transport. Otherwise late transport.Send calls from
	// dispatchMCPAsync race against transport shutdown and stamp a
	// spurious fatalErr.
	s.mu.Lock()
	cancel := s.readerCancel
	started := s.started
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	err := s.transport.Close()
	if started {
		// Bounded wait for the reader to unwind. The reader can be wedged
		// inside transport.Recv on a blocking stdout read that does NOT honor
		// ctx cancellation and survives transport.Close while the subprocess
		// still holds the pipe (QUM-636). An unbounded join here hangs the
		// whole teardown forever and leaks the process + weave.lock, so we
		// cap it: inflight async handlers were already cancelled via
		// readerCancel above, so proceeding without the reader's drainInflight
		// only leaves the (harmless) wedged reader goroutine for the imminent
		// process exit to reap.
		select {
		case <-s.readerDone:
		case <-time.After(inflightDrainTimeout):
		}
		// Bounded wait for the F2 observer drain to flush queued frames.
		// On a wedged Observer.OnMessage we don't want Close to hang
		// forever — surface that as a drop and proceed.
		select {
		case <-s.observerDone:
		case <-time.After(inflightDrainTimeout):
		}
		// Watchdog exits promptly on readerCtx cancel; join with a small
		// safety bound.
		select {
		case <-s.watchdogDone:
		case <-time.After(inflightDrainTimeout):
		}
	}
	// Shutdown-induced ctx.Canceled in fatalErr is expected — don't
	// surface it via LastTurnError after Close.
	s.mu.Lock()
	if errors.Is(s.fatalErr, context.Canceled) {
		s.fatalErr = nil
	}
	s.mu.Unlock()
	return err
}

func (s *session) Wait() error {
	return s.transport.Wait()
}

func (s *session) Kill() error {
	return s.transport.Kill()
}

func (s *session) LastTurnError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.fatalErr
	s.fatalErr = nil
	// Sticky terminal errors (F1 wedge / D1 hang) remain observable on
	// subsequent reads. Recoverable per-turn fatalErr (transport hiccups,
	// send errors) stays one-shot.
	if err == nil && s.terminalErr != nil {
		return s.terminalErr
	}
	return err
}

func (s *session) setFatalErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fatalErr == nil {
		s.fatalErr = err
	}
}

// setTerminalErr marks the session as permanently faulted. Unlike fatalErr
// (consumable via LastTurnError), terminalErr is sticky and gates subsequent
// StartTurn calls. We also mirror into fatalErr so a one-shot
// LastTurnError() read after the fault still surfaces the sentinel.
//
// QUM-602: on the FIRST fire (terminalErr was nil before assignment), the
// registered terminalErrHandler (if any) is invoked OUTSIDE s.mu so the
// handler can call back into session read-side methods without deadlock.
// Subsequent terminal errors do not re-invoke the handler — sticky-once.
func (s *session) setTerminalErr(err error) {
	s.mu.Lock()
	firstFire := s.terminalErr == nil
	if firstFire {
		s.terminalErr = err
	}
	if s.fatalErr == nil {
		s.fatalErr = err
	}
	s.mu.Unlock()
	if firstFire {
		if hp := s.terminalErrHandler.Load(); hp != nil {
			(*hp)(err)
		}
	}
}

// SetTerminalErrorHandler installs a one-shot callback invoked the first
// time setTerminalErr fires on this session. Subsequent terminal errors do
// not re-invoke the handler — the sticky terminalErr semantics are
// preserved. The handler is invoked OUTSIDE s.mu so it can call session
// read-side methods (e.g. BackendStats) without deadlock. nil clears the
// handler.
func (s *session) SetTerminalErrorHandler(h func(err error)) {
	if h == nil {
		s.terminalErrHandler.Store(nil)
		return
	}
	s.terminalErrHandler.Store(&h)
}

// SetAutonomousFrameHandler installs a handler invoked for each frame of an
// autonomous (SDK/harness-initiated) turn (QUM-631). nil clears it. The
// handler runs synchronously on the reader goroutine and MUST NOT block.
func (s *session) SetAutonomousFrameHandler(h func(*protocol.Message)) {
	if h == nil {
		s.autonomousFrameHandler.Store(nil)
		return
	}
	s.autonomousFrameHandler.Store(&h)
}

// IsTerminallyFaulted reports whether the sticky terminalErr has been set
// on this session (QUM-601). Mirrors the LastTurnError / reject-next-StartTurn
// semantics: once true, the session will never service another sprawl-initiated
// turn and the runtime-layer Recover path must rebuild the handle.
func (s *session) IsTerminallyFaulted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalErr != nil
}

// InduceTerminalFault is the test/diagnostic seam used by the QUM-606
// live-recover e2e harness (and the build-tag-gated `_test_induce_wedge`
// MCP tool) to drive a deterministic terminal fault without inducing a
// real frame burst or stalled reader. Calls the same internal
// setTerminalErr path the real fault detectors use, so the runtime-side
// terminalErrHandler fires identically. Production callers MUST NOT use
// this method.
func (s *session) InduceTerminalFault(err error) {
	if err == nil {
		err = ErrSubscriberWedged
	}
	s.setTerminalErr(err)
}

// BackendStats returns an atomic snapshot of the session's drop counters.
func (s *session) BackendStats() Stats {
	return Stats{
		SubscriberDrops: s.subscriberDrops.Load(),
		ObserverDrops:   s.observerDrops.Load(),
	}
}
