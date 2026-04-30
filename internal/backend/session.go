package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

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
}

// NewSession creates a backend session on top of the provided transport.
func NewSession(t ManagedTransport, cfg SessionConfig) Session {
	return &session{
		transport: t,
		config:    cfg,
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

	resp := protocol.ControlResponse{
		Type: "control_response",
		Response: protocol.ControlResponseInner{
			Subtype:   "success",
			RequestID: cr.RequestID,
		},
	}

	switch req.Subtype {
	case "can_use_tool":
		resp.Response.Response = map[string]any{
			"behavior":  "allow",
			"toolUseID": "",
			"message":   "Allowed by host",
		}
	case "mcp_message":
		if initSpec.ToolBridge != nil {
			var mcpReq struct {
				ServerName string          `json:"server_name"`
				Message    json.RawMessage `json:"message"`
			}
			if err := json.Unmarshal(cr.Request, &mcpReq); err == nil {
				bridgeCtx := ctx
				if s.config.Identity != "" {
					bridgeCtx = WithCallerIdentity(ctx, s.config.Identity)
				}
				mcpResp, mcpErr := initSpec.ToolBridge.HandleIncoming(bridgeCtx, mcpReq.ServerName, mcpReq.Message)
				if mcpErr != nil {
					resp.Response.Subtype = "error"
					resp.Response.Response = map[string]any{"error": mcpErr.Error()}
				} else {
					resp.Response.Response = map[string]any{"mcp_response": mcpResp}
				}
			}
		}
	}

	if err := s.transport.Send(ctx, resp); err != nil {
		return fmt.Errorf("sending control response: %w", err)
	}
	return nil
}

func (s *session) Interrupt(ctx context.Context) error {
	requestID := s.nextRequestID()
	msg := protocol.InterruptRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   protocol.InterruptRequestInner{Subtype: "interrupt"},
	}
	return s.transport.Send(ctx, msg)
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
