package tui

import (
	"strings"
	"testing"
)

// A UserItem carrying attachments renders a chip line (📎 name · type · size)
// ABOVE the prompt text, and the chip content survives width truncation. QUM-860.
func TestUserItem_RendersAttachmentChip(t *testing.T) {
	ctx := newTestCtx()
	chips := []AttachmentChip{{Name: "mock.png", MediaType: "image/png", Size: "320 KB"}}
	item := NewUserItemWithAttachments(ctx, "what is wrong here", chips)

	out := stripANSI(item.Render(80))
	if !strings.Contains(out, "📎") {
		t.Errorf("want paperclip glyph, got %q", out)
	}
	for _, want := range []string{"mock.png", "image/png", "320 KB"} {
		if !strings.Contains(out, want) {
			t.Errorf("chip missing %q, got %q", want, out)
		}
	}
	if !strings.Contains(out, "what is wrong here") {
		t.Errorf("want prompt body, got %q", out)
	}
	// Chip line must be above the prompt line.
	chipIdx := strings.Index(out, "mock.png")
	promptIdx := strings.Index(out, "what is wrong here")
	if chipIdx < 0 || promptIdx < 0 || chipIdx > promptIdx {
		t.Errorf("chip must render above prompt; chipIdx=%d promptIdx=%d in %q", chipIdx, promptIdx, out)
	}
}

// Multiple attachments → one chip line per file.
func TestUserItem_RendersChipPerAttachment(t *testing.T) {
	ctx := newTestCtx()
	chips := []AttachmentChip{
		{Name: "a.png", MediaType: "image/png", Size: "1 KB"},
		{Name: "b.jpg", MediaType: "image/jpeg", Size: "2 KB"},
	}
	item := NewUserItemWithAttachments(ctx, "compare", chips)
	out := stripANSI(item.Render(80))
	if !strings.Contains(out, "a.png") || !strings.Contains(out, "b.jpg") {
		t.Errorf("want a chip per file, got %q", out)
	}
}

// An attachment turn with an empty prompt still renders its chip(s) and does not
// emit a dangling empty prompt line.
func TestUserItem_EmptyPromptStillShowsChip(t *testing.T) {
	ctx := newTestCtx()
	chips := []AttachmentChip{{Name: "only.png", MediaType: "image/png", Size: "1 KB"}}
	item := NewUserItemWithAttachments(ctx, "", chips)
	out := item.Render(80)
	if !strings.Contains(stripANSI(out), "only.png") {
		t.Errorf("want chip, got %q", out)
	}
	if strings.Contains(out, "›") {
		t.Errorf("empty prompt should not render a chevron prompt line, got %q", out)
	}
}

// The chip composes with the QUM-832 pending dim/bright flip: a pending chip
// renders differently (dim) from a committed (bright) chip, and SetPending(false)
// brightens it to the committed rendering.
func TestUserItem_ChipComposesWithPendingDimBright(t *testing.T) {
	ctx := newTestCtx()
	chips := []AttachmentChip{{Name: "mock.png", MediaType: "image/png", Size: "320 KB"}}

	pending := NewUserItemWithAttachments(ctx, "hi", chips)
	pending.SetPending(true)
	bright := NewUserItemWithAttachments(ctx, "hi", chips)

	pOut := pending.Render(80)
	bOut := bright.Render(80)
	if pOut == bOut {
		t.Errorf("pending (dim) and committed (bright) chip renders must differ")
	}
	// Flipping pending→committed yields the bright rendering.
	pending.SetPending(false)
	if pending.Render(80) != bOut {
		t.Errorf("SetPending(false) must brighten chip to committed rendering")
	}
}
