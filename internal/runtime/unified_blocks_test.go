package runtime

import (
	"context"
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
)

// TestWriteUserBlocks_SetsBlocksAndTracksUser: a multimodal write records a
// pending kind:user outstanding entry (so its isReplay echo settles the bubble)
// and writes a UserMessage whose MessageParam carries Blocks, not Content
// (QUM-860).
func TestWriteUserBlocks_SetsBlocksAndTracksUser(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	blocks := []protocol.ContentBlock{
		{Type: "image", Source: &protocol.ImageSource{Type: "base64", MediaType: "image/png", Data: "AAAA"}},
		{Type: "text", Text: "describe this"},
	}
	uuid, err := rt.WriteUserBlocks(context.Background(), "describe this", blocks, "next")
	if err != nil {
		t.Fatalf("WriteUserBlocks: %v", err)
	}
	if uuid == "" {
		t.Fatal("WriteUserBlocks returned empty uuid")
	}

	// Outstanding entry is pending + kind:user so recall/consume ack apply.
	out := rt.Outstanding()
	e, ok := out[uuid]
	if !ok {
		t.Fatalf("uuid %q not in outstanding map", uuid)
	}
	if e.kind != kindUser || e.state != statePending {
		t.Errorf("entry = {kind:%v state:%v}, want {user pending}", e.kind, e.state)
	}

	um, ok := mock.lastWrite()
	if !ok {
		t.Fatal("no stdin write recorded")
	}
	if um.UUID != uuid || um.Priority != "next" {
		t.Errorf("write meta = {uuid:%s priority:%s}, want {uuid:%s next}", um.UUID, um.Priority, uuid)
	}
	if um.Message.Content != "" {
		t.Errorf("Content = %q, want empty (blocks path must not set Content)", um.Message.Content)
	}
	if len(um.Message.Blocks) != 2 {
		t.Fatalf("Blocks len = %d, want 2", len(um.Message.Blocks))
	}
	if um.Message.Blocks[0].Type != "image" || um.Message.Blocks[1].Type != "text" {
		t.Errorf("block order = [%q,%q], want [image,text]", um.Message.Blocks[0].Type, um.Message.Blocks[1].Type)
	}
}

// TestWriteUserBlocks_ConsumedByReplay: the outstanding entry flips to consumed
// on its isReplay echo, exactly like a text prompt — so the pending bubble
// settles for a multimodal turn.
func TestWriteUserBlocks_ConsumedByReplay(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})
	blocks := []protocol.ContentBlock{{Type: "text", Text: "hi"}}
	uuid, err := rt.WriteUserBlocks(context.Background(), "hi", blocks, "next")
	if err != nil {
		t.Fatalf("WriteUserBlocks: %v", err)
	}
	rt.markConsumed(uuid)
	if e := rt.Outstanding()[uuid]; e.state != stateConsumed {
		t.Errorf("state = %v after replay echo, want consumed", e.state)
	}
}
