package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/dmotles/sprawl/internal/protocol"
)

// SessionConfig holds configuration for a host session.
type SessionConfig struct {
	MCPServerNames []string
	SystemPrompt   string
	ToolHandler    ControlHandler
}

// Session manages a high-level session with Claude Code.
type Session struct {
	transport Transport
	config    SessionConfig
	reqSeq    atomic.Int64
}

// NewSession creates a new Session with the given transport and config.
func NewSession(t Transport, cfg SessionConfig) *Session {
	return &Session{
		transport: t,
		config:    cfg,
	}
}

func (s *Session) nextRequestID() string {
	n := s.reqSeq.Add(1)
	return fmt.Sprintf("req-%d", n)
}

// Initialize sends the initialize control request and waits for the response.
func (s *Session) Initialize(ctx context.Context) error {
	requestID := s.nextRequestID()

	msg := map[string]any{
		"type":       "control_request",
		"request_id": requestID,
		"request": map[string]any{
			"subtype":       "initialize",
			"system_prompt": s.config.SystemPrompt,
		},
	}

	if err := s.transport.Send(ctx, msg); err != nil {
		return err
	}

	// Read messages until we get a control_response
	for {
		resp, err := s.transport.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if resp != nil && resp.Type == "control_response" {
			return nil
		}
	}
}

// SendUserMessage sends a user message and returns a channel of events.
func (s *Session) SendUserMessage(ctx context.Context, prompt string) (<-chan *protocol.Message, error) {
	msg := protocol.UserMessage{
		Type: "user",
		Message: protocol.MessageParam{
			Role:    "user",
			Content: prompt,
		},
	}

	if err := s.transport.Send(ctx, msg); err != nil {
		return nil, err
	}

	events := make(chan *protocol.Message, 100)

	go func() {
		defer close(events)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			m, err := s.transport.Recv(ctx)
			if err != nil {
				return
			}
			if m == nil {
				continue
			}

			// Handle inline control requests (auto-approve tool use)
			if m.Type == "control_request" {
				s.handleInlineControlRequest(ctx, m)
				continue
			}

			events <- m

			if m.Type == "result" {
				return
			}
		}
	}()

	return events, nil
}

func (s *Session) handleInlineControlRequest(ctx context.Context, msg *protocol.Message) {
	var cr struct {
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(msg.Raw, &cr); err != nil {
		return
	}

	// Auto-approve by sending a success control_response
	resp := protocol.ControlResponse{
		Type: "control_response",
		Response: protocol.ControlResponseInner{
			Subtype:   "success",
			RequestID: cr.RequestID,
		},
	}
	_ = s.transport.Send(ctx, resp)
}

// Interrupt sends an interrupt request.
func (s *Session) Interrupt(ctx context.Context) error {
	requestID := s.nextRequestID()

	msg := protocol.InterruptRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   protocol.InterruptRequestInner{Subtype: "interrupt"},
	}

	return s.transport.Send(ctx, msg)
}

// Close sends end_session and shuts down the transport.
func (s *Session) Close() error {
	return s.transport.Close()
}
