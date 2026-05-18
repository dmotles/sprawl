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

// ManagedTransport is the shared subprocess transport contract for backend
// sessions. It extends the stream-json send/recv path with lifecycle hooks so
// callers can choose graceful close+wait or forceful kill semantics.
type ManagedTransport interface {
	Send(ctx context.Context, msg any) error
	Recv(ctx context.Context) (*protocol.Message, error)
	Close() error
	Wait() error
	Kill() error
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
}

type session struct {
	transport ManagedTransport
	config    SessionConfig
	reqSeq    atomic.Int64

	mu          sync.Mutex
	initSpec    InitSpec
	currentTurn *turnFrame
	fatalErr    error
	started     bool

	// readerCtx/readerCancel control the persistent stream reader.
	readerCtx    context.Context
	readerCancel context.CancelFunc
	readerDone   chan struct{}

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
}

// inflightDrainTimeout bounds how long the reader waits for in-flight async
// MCP handlers to finish on shutdown. A wedged handler must not be able to
// permanently leak the session goroutine.
const inflightDrainTimeout = 5 * time.Second

// NewSession creates a backend session on top of the provided transport.
func NewSession(t ManagedTransport, cfg SessionConfig) Session {
	return &session{
		transport:         t,
		config:            cfg,
		inflight:          make(map[string]context.CancelFunc),
		readerDone:        make(chan struct{}),
		initHandshakeResp: make(chan *protocol.Message, 1),
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

	go s.runReader(readerCtx)
	return nil
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
	}
	s.currentTurn = tf
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
		close(tf.done)
		return nil, err
	}

	return tf.subscriber, nil
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
	defer close(s.readerDone)
	defer s.drainInflight()
	defer func() {
		// Tear down any orphaned currentTurn frame on reader exit so
		// StartTurn callers blocked on tf.done unwind.
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
			close(cur.done)
			s.currentTurn = nil
		}
		s.mu.Unlock()
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

		if s.config.Observer != nil {
			s.config.Observer.OnMessage(msg)
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
		if s.currentTurn == nil && msg.Type == "system" && msg.Subtype == "init" {
			s.currentTurn = &turnFrame{
				startedAt:  time.Now(),
				subscriber: nil,
				done:       make(chan struct{}),
				autonomous: true,
			}
		}
		tf := s.currentTurn
		s.mu.Unlock()

		if tf != nil && tf.subscriber != nil {
			select {
			case tf.subscriber <- msg:
			case <-ctx.Done():
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
				close(cur.done)
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
	requestID := s.nextRequestID()
	msg := protocol.InterruptRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   protocol.InterruptRequestInner{Subtype: "interrupt"},
	}
	err := s.transport.Send(ctx, msg)

	// Cancel every in-flight async MCP handler ctx (QUM-552 S3). We
	// cancel on the outgoing Interrupt — not on a later observed
	// EventInterrupted from claude's stdout — because:
	//   - we control this call site, so cancellation is synchronous
	//     and atomic with the wire-level interrupt write;
	//   - observing EventInterrupted would arrive later and race with
	//     normal handler completion;
	//   - ctx-respecting handlers (retire/delegate/merge) will unwind
	//     immediately; non-respecting handlers (ask_user_question
	//     today — see QUM-553) are unaffected, which is no worse than
	//     the pre-S3 behavior.
	// Entries are NOT removed here; the dispatch goroutines own
	// their inflight-map cleanup on completion.
	s.mu.Lock()
	for _, cancel := range s.inflight {
		cancel()
	}
	s.mu.Unlock()

	return err
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
		<-s.readerDone
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
	return err
}

func (s *session) setFatalErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fatalErr == nil {
		s.fatalErr = err
	}
}
