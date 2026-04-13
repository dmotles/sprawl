package host

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// mockMCPServer implements MCPServer for tests.
type mockMCPServer struct {
	handler func(ctx context.Context, msg json.RawMessage) (json.RawMessage, error)
}

func (m *mockMCPServer) HandleMessage(ctx context.Context, msg json.RawMessage) (json.RawMessage, error) {
	return m.handler(ctx, msg)
}

func TestMCPBridge_RegisterAddsServer(t *testing.T) {
	bridge := NewMCPBridge()

	server := &mockMCPServer{
		handler: func(ctx context.Context, msg json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"result":"ok"}`), nil
		},
	}

	bridge.Register("test-server", server)

	// Verify by sending a message to it
	ctx := context.Background()
	resp, err := bridge.HandleIncoming(ctx, "test-server", json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"test"}`))
	if err != nil {
		t.Fatalf("HandleIncoming() error: %v", err)
	}
	if resp == nil {
		t.Fatal("HandleIncoming() returned nil response")
	}
}

func TestMCPBridge_HandleIncomingRoutesToCorrectServer(t *testing.T) {
	bridge := NewMCPBridge()

	var serverACalled, serverBCalled bool

	bridge.Register("server-a", &mockMCPServer{
		handler: func(ctx context.Context, msg json.RawMessage) (json.RawMessage, error) {
			serverACalled = true
			return json.RawMessage(`{"result":"a"}`), nil
		},
	})
	bridge.Register("server-b", &mockMCPServer{
		handler: func(ctx context.Context, msg json.RawMessage) (json.RawMessage, error) {
			serverBCalled = true
			return json.RawMessage(`{"result":"b"}`), nil
		},
	})

	ctx := context.Background()
	_, err := bridge.HandleIncoming(ctx, "server-a", json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"test"}`))
	if err != nil {
		t.Fatalf("HandleIncoming(server-a) error: %v", err)
	}

	if !serverACalled {
		t.Error("server-a handler was not called")
	}
	if serverBCalled {
		t.Error("server-b handler was unexpectedly called")
	}
}

func TestMCPBridge_HandleIncomingNotification(t *testing.T) {
	bridge := NewMCPBridge()

	var receivedMsg json.RawMessage
	bridge.Register("notif-server", &mockMCPServer{
		handler: func(ctx context.Context, msg json.RawMessage) (json.RawMessage, error) {
			receivedMsg = msg
			// Notifications have no id, server returns nil
			return nil, nil
		},
	})

	ctx := context.Background()
	// JSON-RPC notification: no "id" field
	notification := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	resp, err := bridge.HandleIncoming(ctx, "notif-server", json.RawMessage(notification))
	if err != nil {
		t.Fatalf("HandleIncoming() error: %v", err)
	}

	if receivedMsg == nil {
		t.Fatal("server did not receive the notification")
	}

	// For notifications, bridge should return a dummy success response
	if resp == nil {
		t.Fatal("expected dummy success response for notification, got nil")
	}
}

func TestMCPBridge_HandleIncomingUnknownServer(t *testing.T) {
	bridge := NewMCPBridge()

	ctx := context.Background()
	_, err := bridge.HandleIncoming(ctx, "nonexistent", json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"test"}`))
	if err == nil {
		t.Fatal("HandleIncoming() expected error for unknown server, got nil")
	}
}

func TestMCPBridge_OnServerMessageResolvesPending(t *testing.T) {
	bridge := NewMCPBridge()

	// Set up a pending waiter for a server message
	waitCh := make(chan json.RawMessage, 1)
	bridge.AddPendingMCP("test-server", "42", waitCh)

	response := json.RawMessage(`{"jsonrpc":"2.0","id":42,"result":{"tools":[]}}`)
	bridge.OnServerMessage("test-server", "42", response)

	select {
	case got := <-waitCh:
		if got == nil {
			t.Fatal("received nil response")
		}
		var parsed map[string]any
		if err := json.Unmarshal(got, &parsed); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if parsed["id"] != float64(42) {
			t.Errorf("response id = %v, want 42", parsed["id"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pending response")
	}
}

func TestMCPBridge_OnServerMessageNoPendingWaiter(t *testing.T) {
	bridge := NewMCPBridge()

	// No pending waiter - should not panic
	response := json.RawMessage(`{"jsonrpc":"2.0","id":99,"result":"orphan"}`)
	bridge.OnServerMessage("test-server", "99", response)
	// Test passes if no panic
}

func TestMCPBridge_JSONRPCIDPreservation(t *testing.T) {
	bridge := NewMCPBridge()

	var receivedIDs []string
	bridge.Register("id-server", &mockMCPServer{
		handler: func(ctx context.Context, msg json.RawMessage) (json.RawMessage, error) {
			var parsed map[string]any
			if err := json.Unmarshal(msg, &parsed); err != nil {
				return nil, err
			}
			// Capture the id from the incoming request
			if id, ok := parsed["id"]; ok {
				idJSON, _ := json.Marshal(id)
				receivedIDs = append(receivedIDs, string(idJSON))
			}
			// Echo back with the same id
			return msg, nil
		},
	})

	ctx := context.Background()

	// Send messages with different ID types (number, string)
	msgs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":"abc-123","method":"tools/call"}`,
		`{"jsonrpc":"2.0","id":999,"method":"resources/list"}`,
	}

	for _, m := range msgs {
		resp, err := bridge.HandleIncoming(ctx, "id-server", json.RawMessage(m))
		if err != nil {
			t.Fatalf("HandleIncoming() error: %v", err)
		}

		// Verify the response preserves the original id
		var original, returned map[string]any
		json.Unmarshal([]byte(m), &original)
		json.Unmarshal(resp, &returned)

		origID, _ := json.Marshal(original["id"])
		retID, _ := json.Marshal(returned["id"])
		if string(origID) != string(retID) {
			t.Errorf("id mismatch: sent %s, got %s", origID, retID)
		}
	}
}

func TestMCPBridge_ConcurrentAccess(t *testing.T) {
	bridge := NewMCPBridge()

	bridge.Register("concurrent-server", &mockMCPServer{
		handler: func(ctx context.Context, msg json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"result":"ok"}`), nil
		},
	})

	var wg sync.WaitGroup
	ctx := context.Background()

	// Concurrent HandleIncoming calls
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			msg := json.RawMessage(`{"jsonrpc":"2.0","id":` + string(rune('0'+n)) + `,"method":"test"}`)
			_, _ = bridge.HandleIncoming(ctx, "concurrent-server", msg)
		}(i)
	}

	// Concurrent AddPendingMCP and OnServerMessage calls
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ch := make(chan json.RawMessage, 1)
			id := string(rune('a' + n))
			bridge.AddPendingMCP("concurrent-server", id, ch)
			bridge.OnServerMessage("concurrent-server", id, json.RawMessage(`{"ok":true}`))
		}(i)
	}

	wg.Wait()
	// Test passes if no panics or data races
}
