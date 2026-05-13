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

// TurnSpec carries optional per-turn overrides. Today only the tool bridge
// state is relevant so the host wrapper can thread it through sends.
type TurnSpec struct {
	Init InitSpec
}

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
	Initialize(ctx context.Context, spec InitSpec) error
	StartTurn(ctx context.Context, prompt string, spec ...TurnSpec) (<-chan *protocol.Message, error)
	Interrupt(ctx context.Context) error
	Close() error
	Wait() error
	Kill() error
	LastTurnError() error
	SessionID() string
	Capabilities() Capabilities
}

// ErrTurnInProgress is returned when callers try to start a second concurrent
// turn on the same stream-json session.
var ErrTurnInProgress = errors.New("backend: turn already in progress")

type session struct {
	transport ManagedTransport
	config    SessionConfig
	reqSeq    atomic.Int64

	mu             sync.Mutex
	turnInProgress bool
	initSpec       InitSpec
	lastTurnErr    error

	// inflight tracks the cancellation funcs of in-flight async MCP
	// handlers, keyed by control_request request_id. inflightWG waits
	// for those goroutines on session shutdown. See
	// handleInlineControlRequest for the rationale.
	inflight   map[string]context.CancelFunc
	inflightWG sync.WaitGroup
}

// inflightDrainTimeout bounds how long readTurn waits for in-flight async
// MCP handlers to finish on shutdown. A wedged handler must not be able to
// permanently leak the session goroutine.
const inflightDrainTimeout = 5 * time.Second

// NewSession creates a backend session on top of the provided transport.
func NewSession(t ManagedTransport, cfg SessionConfig) Session {
	return &session{
		transport: t,
		config:    cfg,
		inflight:  make(map[string]context.CancelFunc),
	}
}

func (s *session) SessionID() string {
	return s.config.SessionID
}

func (s *session) Capabilities() Capabilities {
	return s.config.Capabilities
}

func (s *session) nextRequestID() string {
	n := s.reqSeq.Add(1)
	return fmt.Sprintf("req-%d", n)
}

func (s *session) Initialize(ctx context.Context, spec InitSpec) error {
	s.mu.Lock()
	s.initSpec = spec
	s.mu.Unlock()

	requestID := s.nextRequestID()
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

	for {
		resp, err := s.transport.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if resp != nil && s.config.Observer != nil {
			s.config.Observer.OnMessage(resp)
		}
		if resp != nil && resp.Type == "control_response" {
			return nil
		}
	}
}

func (s *session) StartTurn(ctx context.Context, prompt string, spec ...TurnSpec) (<-chan *protocol.Message, error) {
	s.mu.Lock()
	if s.turnInProgress {
		s.mu.Unlock()
		return nil, ErrTurnInProgress
	}
	s.turnInProgress = true
	s.lastTurnErr = nil
	initSpec := s.initSpec
	if len(spec) > 0 {
		if len(spec[0].Init.MCPServerNames) > 0 || spec[0].Init.ToolBridge != nil {
			initSpec = spec[0].Init
		}
	}
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
		s.turnInProgress = false
		s.mu.Unlock()
		return nil, err
	}

	events := make(chan *protocol.Message, 100)
	go s.readTurn(ctx, events, initSpec)
	return events, nil
}

func (s *session) readTurn(ctx context.Context, events chan<- *protocol.Message, initSpec InitSpec) {
	defer close(events)
	defer func() {
		s.mu.Lock()
		s.turnInProgress = false
		s.mu.Unlock()
	}()
	// Bounded-drain in-flight async MCP handlers before returning so a
	// wedged handler can't permanently leak this session goroutine. See
	// handleInlineControlRequest for the async-dispatch rationale.
	defer s.drainInflight()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := s.transport.Recv(ctx)
		if err != nil {
			s.setTurnError(fmt.Errorf("reading message: %w", err))
			return
		}
		if msg == nil {
			continue
		}

		if s.config.Observer != nil {
			s.config.Observer.OnMessage(msg)
		}

		if msg.Type == "control_request" {
			if err := s.handleInlineControlRequest(ctx, msg, initSpec); err != nil {
				s.setTurnError(err)
				return
			}
			continue
		}

		select {
		case events <- msg:
		case <-ctx.Done():
			return
		}

		if msg.Type == "result" {
			return
		}
	}
}

// handleInlineControlRequest routes an incoming control_request frame.
//
// Async dispatch (QUM-552, follow-up to QUM-549):
//
// `mcp_message` subtypes are dispatched in a goroutine so that
// readTurn's outer Recv loop keeps consuming claude's stdout while a
// long-running MCP tool handler runs. Without this, `Session.Interrupt`
// is unobservable mid-tool-wait and the EventBus goes silent for the
// entire wedge window (see
// docs/research/qum-549-send-interrupt-during-mcp-tool-wait.md).
//
// Only `mcp_message` is asynchronous. `can_use_tool` (and any future
// subtype that resolves synchronously off a static decision) stays in
// the readTurn loop — those handlers are fast, do not call out through
// ToolBridge, and dispatching them async would only add scheduling
// overhead and reorder responses on the wire.
//
// Lifecycle invariant: each async handler registers its cancelFunc in
// s.inflight under cr.RequestID and increments s.inflightWG. On
// completion the entry is removed. On session shutdown (readTurn
// return), drainInflight cancels every remaining ctx and bounded-waits
// inflightDrainTimeout for the goroutines to finish so a wedged
// handler cannot permanently leak the session.
//
// Wire-level safety: protocol.Writer.mu (see internal/protocol/writer.go)
// serializes stdin writes, so concurrent transport.Send calls from
// multiple in-flight handlers are safe.
func (s *session) handleInlineControlRequest(ctx context.Context, msg *protocol.Message, initSpec InitSpec) error {
	var cr struct {
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(msg.Raw, &cr); err != nil {
		return err
	}

	var req struct {
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(cr.Request, &req); err != nil {
		return err
	}

	if req.Subtype == "mcp_message" {
		s.dispatchMCPAsync(ctx, cr.RequestID, cr.Request, initSpec)
		return nil
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
		return fmt.Errorf("sending control response: %w", err)
	}
	return nil
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
			s.setTurnError(fmt.Errorf("sending control response: %w", err))
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
			s.setTurnError(fmt.Errorf("sending control response: %w", sendErr))
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
			s.setTurnError(fmt.Errorf("sending control response: %w", err))
		}
	}()
}

// drainInflight cancels every in-flight async handler and waits up to
// inflightDrainTimeout for them to finish. Called from readTurn's defer.
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
	return s.transport.Close()
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
	err := s.lastTurnErr
	s.lastTurnErr = nil
	return err
}

func (s *session) setTurnError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastTurnErr = err
}
