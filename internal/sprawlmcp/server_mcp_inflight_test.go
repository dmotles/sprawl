package sprawlmcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/tui"
)

// QUM-497: handleToolsCall must emit an MCPCallStartedMsg before dispatching
// and an MCPCallEndedMsg afterward (status reflects ok/error/panic) — at the
// SAME boundaries the calllog already uses.

type recordingSender struct {
	mu   sync.Mutex
	msgs []any
}

func (r *recordingSender) push(msg any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, msg)
}

func (r *recordingSender) snap() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]any, len(r.msgs))
	copy(out, r.msgs)
	return out
}

func TestHandleToolsCall_EmitsStartedAndEnded_OnSuccess(t *testing.T) {
	sup := &mockSupervisor{}
	srv := New(sup)
	rec := &recordingSender{}
	srv.SetMsgSender(rec.push)

	// status is a read-only tool that needs no fake state to succeed.
	req := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"status"}}`)
	if _, err := srv.HandleMessage(backendpkg.WithCallerIdentity(context.Background(), "weave"), req); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	msgs := rec.snap()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (Started, Ended), got %d: %+v", len(msgs), msgs)
	}
	started, ok := msgs[0].(tui.MCPCallStartedMsg)
	if !ok {
		t.Fatalf("first msg not MCPCallStartedMsg: %T", msgs[0])
	}
	if started.Tool != "status" {
		t.Errorf("Started.Tool=%q want status", started.Tool)
	}
	if started.Caller != "weave" {
		t.Errorf("Started.Caller=%q want weave", started.Caller)
	}
	if started.CallID == "" {
		t.Errorf("Started.CallID empty (server should synthesize id when callLog is noop)")
	}
	if started.Started.IsZero() {
		t.Errorf("Started.Started is zero time")
	}

	ended, ok := msgs[1].(tui.MCPCallEndedMsg)
	if !ok {
		t.Fatalf("second msg not MCPCallEndedMsg: %T", msgs[1])
	}
	if ended.CallID != started.CallID {
		t.Errorf("Ended.CallID=%q != Started.CallID=%q", ended.CallID, started.CallID)
	}
	if ended.Status != "ok" {
		t.Errorf("Ended.Status=%q want ok", ended.Status)
	}
}

func TestHandleToolsCall_EmitsEnded_OnError(t *testing.T) {
	sup := &mockSupervisor{}
	sup.statusErr = errors.New("boom")
	srv := New(sup)
	rec := &recordingSender{}
	srv.SetMsgSender(rec.push)
	req := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"status"}}`)
	_, _ = srv.HandleMessage(backendpkg.WithCallerIdentity(context.Background(), "weave"), req)

	msgs := rec.snap()
	if len(msgs) < 2 {
		t.Fatalf("expected Started+Ended, got %d", len(msgs))
	}
	ended, ok := msgs[len(msgs)-1].(tui.MCPCallEndedMsg)
	if !ok {
		t.Fatalf("last msg not MCPCallEndedMsg: %T", msgs[len(msgs)-1])
	}
	if ended.Status != "error" {
		t.Errorf("Ended.Status=%q want error", ended.Status)
	}
}

func TestSetMsgSender_NilDisables(t *testing.T) {
	sup := &mockSupervisor{}
	srv := New(sup)
	rec := &recordingSender{}
	srv.SetMsgSender(rec.push)
	srv.SetMsgSender(nil) // clear

	req := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"status"}}`)
	_, _ = srv.HandleMessage(backendpkg.WithCallerIdentity(context.Background(), "weave"), req)

	if got := len(rec.snap()); got != 0 {
		t.Errorf("expected 0 msgs after SetMsgSender(nil), got %d", got)
	}
}
