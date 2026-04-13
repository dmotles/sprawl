package host

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// MCPServer handles JSON-RPC messages for a named MCP server.
type MCPServer interface {
	HandleMessage(ctx context.Context, msg json.RawMessage) (json.RawMessage, error)
}

// MCPBridge tunnels JSON-RPC between SDK MCP servers and Claude Code.
type MCPBridge struct {
	mu      sync.Mutex
	servers map[string]MCPServer
	pending map[string]chan json.RawMessage
}

// NewMCPBridge creates a new MCPBridge.
func NewMCPBridge() *MCPBridge {
	return &MCPBridge{
		servers: make(map[string]MCPServer),
		pending: make(map[string]chan json.RawMessage),
	}
}

// Register adds a named MCP server to the bridge.
func (b *MCPBridge) Register(name string, server MCPServer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.servers[name] = server
}

// HandleIncoming routes an incoming mcp_message to the correct server.
func (b *MCPBridge) HandleIncoming(ctx context.Context, serverName string, msg json.RawMessage) (json.RawMessage, error) {
	b.mu.Lock()
	server, ok := b.servers[serverName]
	b.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("server %q not found", serverName)
	}

	// Check if this is a notification (no "id" field) or a request
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(msg, &parsed); err != nil {
		return nil, err
	}

	_, hasID := parsed["id"]

	resp, err := server.HandleMessage(ctx, msg)
	if err != nil {
		return nil, err
	}

	if !hasID {
		// Notification: return dummy success response
		return json.RawMessage(`{"jsonrpc":"2.0","id":0,"result":{}}`), nil
	}

	return resp, nil
}

// AddPendingMCP registers a channel to receive a response for a given server and JSON-RPC ID.
func (b *MCPBridge) AddPendingMCP(serverName string, jsonrpcID string, ch chan json.RawMessage) {
	key := serverName + ":" + jsonrpcID
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending[key] = ch
}

// OnServerMessage delivers a server-initiated message to a pending waiter.
func (b *MCPBridge) OnServerMessage(serverName string, jsonrpcID string, msg json.RawMessage) {
	key := serverName + ":" + jsonrpcID
	b.mu.Lock()
	ch, ok := b.pending[key]
	if ok {
		delete(b.pending, key)
	}
	b.mu.Unlock()

	if ok {
		ch <- msg
	}
}
