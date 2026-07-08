package tui

import (
	"strings"
	"testing"
)

// ZoneAddUserWithAttachments seeds a pending (dim) user bubble carrying chip
// lines; ZoneSettle relocates it into the committed transcript, brightens it,
// and the chip is present exactly once (no double render). QUM-860 / QUM-832.
func TestChatList_ZoneAddUserWithAttachments_SettleRendersChipOnce(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	chips := []AttachmentChip{{Name: "mock.png", MediaType: "image/png", Size: "320 KB"}}
	cl.ZoneAddUserWithAttachments("u1", "what is this", chips)

	// While pending, the chip renders as the inline zone tail.
	pending := stripAnsi(cl.Render(80))
	if !strings.Contains(pending, "mock.png") {
		t.Fatalf("pending render missing chip; got:\n%s", pending)
	}

	if ok := cl.ZoneSettle("u1"); !ok {
		t.Fatal("ZoneSettle(u1) = false, want true")
	}
	if cl.Len() != 1 {
		t.Fatalf("committed Len = %d after settle, want 1", cl.Len())
	}
	out := stripAnsi(cl.Render(80))
	if got := strings.Count(out, "mock.png"); got != 1 {
		t.Errorf("chip rendered %d times after settle, want exactly 1; render:\n%s", got, out)
	}
	if !strings.Contains(out, "what is this") {
		t.Errorf("settled render missing prompt body; got:\n%s", out)
	}
}
