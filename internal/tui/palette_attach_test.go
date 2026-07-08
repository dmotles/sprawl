package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/tui/commands"
)

// typeRunes feeds each rune of s to the palette as a KeyPressMsg.
func typeRunes(p PaletteModel, s string) PaletteModel {
	for _, r := range s {
		p, _ = p.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return p
}

// Selecting /attach transitions the palette into attach-argument mode without
// closing (mirrors /switch → agent mode). QUM-860.
func TestPalette_AttachEntersArgMode(t *testing.T) {
	theme := NewTheme(defaultAccentColor)
	p := NewPaletteModel(&theme)
	p.Show()
	// Filter down to /attach then Enter.
	p = typeRunes(p, "attach")
	p, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("selecting /attach should not dispatch immediately; got cmd")
	}
	if !p.InAttachMode() {
		t.Fatal("expected palette to be in attach mode after selecting /attach")
	}
	if !p.Visible() {
		t.Error("palette should stay visible in attach mode")
	}
}

// In attach mode the palette accepts a free-form argument line (paths, spaces,
// slashes, dots, quotes) and Enter emits AttachMsg with parsed paths + prompt.
func TestPalette_AttachArgLineParsedOnEnter(t *testing.T) {
	theme := NewTheme(defaultAccentColor)
	p := NewPaletteModel(&theme)
	p.Show()
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // no-op guard; refine below
	// Reset and drive deterministically: reopen, select /attach.
	p.Show()
	p = typeRunes(p, "attach")
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !p.InAttachMode() {
		t.Fatal("setup: expected attach mode")
	}

	p = typeRunes(p, `/tmp/mock.png "what is this"`)
	p, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter in attach mode should dispatch AttachMsg")
	}
	msg := cmd()
	am, ok := msg.(AttachMsg)
	if !ok {
		t.Fatalf("dispatched msg = %T, want AttachMsg", msg)
	}
	if len(am.Paths) != 1 || am.Paths[0] != "/tmp/mock.png" {
		t.Errorf("Paths = %v, want [/tmp/mock.png]", am.Paths)
	}
	if am.Prompt != "what is this" {
		t.Errorf("Prompt = %q, want %q", am.Prompt, "what is this")
	}
	if p.Visible() {
		t.Error("palette should close after dispatching AttachMsg")
	}
}

// Backspace at an empty attach-arg buffer returns to command mode.
func TestPalette_AttachBackspaceAtEmptyExits(t *testing.T) {
	theme := NewTheme(defaultAccentColor)
	p := NewPaletteModel(&theme)
	p.Show()
	p = typeRunes(p, "attach")
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !p.InAttachMode() {
		t.Fatal("setup: expected attach mode")
	}
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if p.InAttachMode() {
		t.Error("backspace at empty arg buffer should exit attach mode")
	}
}

// KindAttach is registered for /attach.
func TestRegistry_AttachCommandRegistered(t *testing.T) {
	var found bool
	for _, c := range commands.All() {
		if c.Name == "/attach" {
			found = true
			if c.Kind != commands.KindAttach {
				t.Errorf("/attach Kind = %v, want KindAttach", c.Kind)
			}
		}
	}
	if !found {
		t.Error("/attach not registered")
	}
}
