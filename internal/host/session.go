package host

import (
	"context"

	backend "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// SessionConfig holds configuration for a host session.
type SessionConfig struct {
	MCPServerNames []string
	ToolHandler    ControlHandler
	MCPBridge      *MCPBridge
}

// Session is a thin compatibility wrapper over the shared backend session
// driver. The host package keeps ownership of MCPBridge but no longer owns a
// second stream-json control loop implementation.
type Session struct {
	session  backend.Session
	initSpec backend.InitSpec
}

// NewSession creates a new Session with the given transport and config.
func NewSession(t Transport, cfg SessionConfig) *Session {
	return &Session{
		session: backend.NewSession(&transportCompat{Transport: t}, backend.SessionConfig{}),
		initSpec: backend.InitSpec{
			MCPServerNames: cfg.MCPServerNames,
			ToolBridge:     cfg.MCPBridge,
		},
	}
}

// Initialize sends the initialize control request and waits for the response.
func (s *Session) Initialize(ctx context.Context) error {
	return s.session.Initialize(ctx, s.initSpec)
}

// SendUserMessage sends a user message and returns a channel of events.
func (s *Session) SendUserMessage(ctx context.Context, prompt string) (<-chan *protocol.Message, error) {
	return s.session.StartTurn(ctx, prompt, backend.TurnSpec{Init: s.initSpec})
}

// Interrupt sends an interrupt request.
func (s *Session) Interrupt(ctx context.Context) error {
	return s.session.Interrupt(ctx)
}

// Close shuts down the transport.
func (s *Session) Close() error {
	return s.session.Close()
}

type transportCompat struct {
	Transport
}

func (t *transportCompat) Wait() error { return nil }

func (t *transportCompat) Kill() error { return nil }
