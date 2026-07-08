package tuiruntime

import (
	"os"
	"path/filepath"
	"testing"

	tui "github.com/dmotles/sprawl/internal/tui"
)

var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")

// SendAttachment: a valid image is assembled into a blocks turn written to
// stdin, and the bridge returns a UserMessageSentMsg carrying the prompt text
// and chip metadata so the pending bubble renders + settles (QUM-860).
func TestTUIAdapter_SendAttachment_WritesBlocksTurn(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mock.png")
	if err := os.WriteFile(p, pngHeader, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mock := &adapterMockSession{}
	_, a := buildAdapter(t, mock)

	msg := runCmd(t, a.SendAttachment([]string{p}, "what is this"))
	sent, ok := msg.(tui.UserMessageSentMsg)
	if !ok {
		t.Fatalf("SendAttachment() = %T, want tui.UserMessageSentMsg", msg)
	}
	if sent.Text != "what is this" {
		t.Errorf("sent.Text = %q, want %q", sent.Text, "what is this")
	}
	if len(sent.Attachments) != 1 || sent.Attachments[0].Name != "mock.png" {
		t.Errorf("sent.Attachments = %+v, want one chip named mock.png", sent.Attachments)
	}
	if sent.Attachments[0].MediaType != "image/png" {
		t.Errorf("chip media_type = %q, want image/png", sent.Attachments[0].MediaType)
	}

	um, ok := mock.lastWrite()
	if !ok {
		t.Fatal("SendAttachment did not write to stdin")
	}
	if um.Message.Content != "" {
		t.Errorf("write Content = %q, want empty (blocks turn)", um.Message.Content)
	}
	if len(um.Message.Blocks) != 2 || um.Message.Blocks[0].Type != "image" {
		t.Errorf("write Blocks = %+v, want [image,text]", um.Message.Blocks)
	}
}

// A local validation failure surfaces as an error toast and writes NO turn.
func TestTUIAdapter_SendAttachment_ValidationError_ToastNoTurn(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(p, []byte("not an image at all, plain text"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mock := &adapterMockSession{}
	_, a := buildAdapter(t, mock)

	msg := runCmd(t, a.SendAttachment([]string{p}, "x"))
	toast, ok := msg.(tui.AttachRejectedMsg)
	if !ok {
		t.Fatalf("SendAttachment() = %T, want tui.AttachRejectedMsg", msg)
	}
	if toast.Toast.Style != tui.ToastError {
		t.Errorf("toast style = %v, want ToastError", toast.Toast.Style)
	}
	if _, wrote := mock.lastWrite(); wrote {
		t.Error("validation failure must NOT write a turn to stdin")
	}
}

func TestTUIAdapter_SendAttachment_NilRuntime_ReturnsSessionError(t *testing.T) {
	a := &TUIAdapter{}
	msg := runCmd(t, a.SendAttachment([]string{"x.png"}, "y"))
	if _, ok := msg.(tui.SessionErrorMsg); !ok {
		t.Fatalf("SendAttachment() with nil runtime = %T, want tui.SessionErrorMsg", msg)
	}
}
