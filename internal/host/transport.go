// Package host implements the Claude Code Agent SDK host protocol engine.
// It provides a transport layer for subprocess communication, a control
// message router with request/response correlation, an MCP bridge for
// tunneling JSON-RPC to SDK-managed servers, and a session manager for
// high-level interaction with Claude Code.
package host

import (
	"context"

	"github.com/dmotles/sprawl/internal/protocol"
)

// Transport is the interface for sending and receiving protocol messages.
type Transport interface {
	Send(ctx context.Context, msg any) error
	Recv(ctx context.Context) (*protocol.Message, error)
	Close() error
}
