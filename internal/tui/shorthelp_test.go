package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// keysOf returns the Key field of every binding in the slice for clearer
// assertion error messages.
func keysOf(b []shortBinding) []string {
	out := make([]string, len(b))
	for i, kb := range b {
		out[i] = kb.Key
	}
	return out
}

// containsKey reports whether bindings contain a binding with exact key k.
func containsKey(b []shortBinding, k string) bool {
	for _, kb := range b {
		if kb.Key == k {
			return true
		}
	}
	return false
}

// hintFor returns the hint string for the binding with key k. Empty string if
// not present.
func hintFor(b []shortBinding, k string) string {
	for _, kb := range b {
		if kb.Key == k {
			return kb.Hint
		}
	}
	return ""
}

// hasHintContaining reports whether any binding's hint contains substring s.
func hasHintContaining(b []shortBinding, s string) bool {
	for _, kb := range b {
		if strings.Contains(kb.Hint, s) {
			return true
		}
	}
	return false
}

func TestShortHelpBindings_AlwaysOn(t *testing.T) {
	states := []ShortHelpState{
		{Focus: PanelTree, TurnState: TurnIdle},
		{Focus: PanelViewport, TurnState: TurnIdle},
		{Focus: PanelInput, TurnState: TurnIdle, InputEmpty: true},
		{Focus: PanelInput, TurnState: TurnStreaming},
		{PaletteOpen: true},
		{Focus: PanelViewport, SelectMode: true},
	}
	// For each always-on key, the hint must contain one of the substrings.
	wantSubs := map[string][]string{
		"?":      {"help"},
		"tab":    {"cycle"},
		"ctrl+c": {"quit", "clear"},
	}
	for _, s := range states {
		t.Run("", func(t *testing.T) {
			b := shortHelpBindings(s)
			for k, subs := range wantSubs {
				if !containsKey(b, k) {
					t.Errorf("state %+v: expected always-on key %q, got keys=%v", s, k, keysOf(b))
					continue
				}
				h := hintFor(b, k)
				ok := false
				for _, sub := range subs {
					if strings.Contains(h, sub) {
						ok = true
						break
					}
				}
				if !ok {
					t.Errorf("state %+v: key %q hint=%q, want contains one of %v", s, k, h, subs)
				}
			}
		})
	}
}

func TestShortHelpBindings_EditorEmpty(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{Focus: PanelInput, InputEmpty: true})
	if !containsKey(b, "/") {
		t.Fatalf("expected key %q for empty editor, got keys=%v", "/", keysOf(b))
	}
	if h := hintFor(b, "/"); !strings.Contains(h, "command") {
		t.Errorf("key %q hint=%q, want contains %q", "/", h, "command")
	}
}

func TestShortHelpBindings_EditorNonEmpty(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{Focus: PanelInput, InputEmpty: false})
	if containsKey(b, "/") {
		t.Errorf("expected key %q to be absent when editor non-empty, got keys=%v", "/", keysOf(b))
	}
}

func TestShortHelpBindings_EditorQueued(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{Focus: PanelInput, HasQueued: true})
	if !containsKey(b, "esc") {
		t.Fatalf("expected key %q when queued, got keys=%v", "esc", keysOf(b))
	}
	if h := hintFor(b, "esc"); !strings.Contains(h, "clear queue") {
		t.Errorf("esc hint=%q, want contains %q", h, "clear queue")
	}
}

func TestShortHelpBindings_Streaming(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{TurnState: TurnStreaming})
	if !containsKey(b, "esc") {
		t.Fatalf("expected key esc while streaming, got keys=%v", keysOf(b))
	}
	if h := hintFor(b, "esc"); !strings.Contains(h, "interrupt") {
		t.Errorf("esc hint=%q while streaming, want contains %q", h, "interrupt")
	}
}

func TestShortHelpBindings_Thinking(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{TurnState: TurnThinking})
	if !containsKey(b, "esc") {
		t.Fatalf("expected key esc while thinking, got keys=%v", keysOf(b))
	}
	if h := hintFor(b, "esc"); !strings.Contains(h, "interrupt") {
		t.Errorf("esc hint=%q while thinking, want contains %q", h, "interrupt")
	}
}

// Precedence: when streaming AND queued, esc must mean interrupt (not clear queue).
func TestShortHelpBindings_StreamingPlusQueued(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{TurnState: TurnStreaming, HasQueued: true})
	if !containsKey(b, "esc") {
		t.Fatalf("expected key esc while streaming+queued, got keys=%v", keysOf(b))
	}
	h := hintFor(b, "esc")
	if !strings.Contains(h, "interrupt") {
		t.Errorf("esc hint=%q while streaming+queued, want contains %q (streaming must take priority)", h, "interrupt")
	}
}

func TestShortHelpBindings_TreeFocused(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{Focus: PanelTree})
	// Navigation: assert via hint substring (key glyph may be "↑↓" or other).
	if !hasHintContaining(b, "navigate") {
		t.Errorf("tree-focused: expected a binding with hint containing %q, got bindings=%v", "navigate", keysOf(b))
	}
	// Enter is canonical/stable.
	if !containsKey(b, "enter") {
		t.Errorf("tree-focused: expected key %q, got keys=%v", "enter", keysOf(b))
	}
	// Cycle agent: assert via hint substring (key glyph may be "ctrl+n/p").
	if !hasHintContaining(b, "cycle agent") {
		t.Errorf("tree-focused: expected a binding with hint containing %q, got bindings=%v", "cycle agent", keysOf(b))
	}
}

func TestShortHelpBindings_ViewportFocused(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{Focus: PanelViewport})
	// Scroll: assert via hint substring (key glyph may be "pgup/pgdn").
	if !hasHintContaining(b, "scroll") {
		t.Errorf("viewport-focused: expected a binding with hint containing %q, got bindings=%v", "scroll", keysOf(b))
	}
	// v and ctrl+o are canonical/stable keys.
	for _, k := range []string{"v", "ctrl+o"} {
		if !containsKey(b, k) {
			t.Errorf("viewport-focused: expected key %q, got keys=%v", k, keysOf(b))
		}
	}
}

func TestShortHelpBindings_SelectMode(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{Focus: PanelViewport, SelectMode: true})
	// Move via j/k: assert via hint substring.
	if !hasHintContaining(b, "move") {
		t.Errorf("select-mode: expected a binding with hint containing %q, got bindings=%v", "move", keysOf(b))
	}
	// y is canonical for yank; also accept assertion via "yank" hint.
	if !containsKey(b, "y") && !hasHintContaining(b, "yank") {
		t.Errorf("select-mode: expected key %q or hint containing %q, got bindings=%v", "y", "yank", keysOf(b))
	}
	if !containsKey(b, "esc") {
		t.Fatalf("select-mode: expected key esc, got keys=%v", keysOf(b))
	}
	if h := hintFor(b, "esc"); !strings.Contains(h, "exit") {
		t.Errorf("select-mode esc hint=%q, want contains %q", h, "exit")
	}
}

func TestShortHelpBindings_PaletteOpen(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{PaletteOpen: true})
	// Palette navigation: assert via hint substring.
	if !hasHintContaining(b, "navigate") {
		t.Errorf("palette-open: expected a binding with hint containing %q, got bindings=%v", "navigate", keysOf(b))
	}
	if !containsKey(b, "enter") {
		t.Errorf("palette-open: expected key %q, got keys=%v", "enter", keysOf(b))
	}
	if !containsKey(b, "esc") {
		t.Fatalf("palette-open: expected key esc, got keys=%v", keysOf(b))
	}
	if h := hintFor(b, "esc"); !strings.Contains(h, "close") {
		t.Errorf("palette-open esc hint=%q, want contains %q", h, "close")
	}
}

func TestShortHelpBindings_CountBounded(t *testing.T) {
	cases := []struct {
		name  string
		state ShortHelpState
	}{
		{"idle tree", ShortHelpState{Focus: PanelTree, TurnState: TurnIdle}},
		{"idle viewport", ShortHelpState{Focus: PanelViewport, TurnState: TurnIdle}},
		{"input empty", ShortHelpState{Focus: PanelInput, TurnState: TurnIdle, InputEmpty: true}},
		{"input queued", ShortHelpState{Focus: PanelInput, HasQueued: true}},
		{"streaming", ShortHelpState{TurnState: TurnStreaming}},
		{"thinking", ShortHelpState{TurnState: TurnThinking}},
		{"select mode", ShortHelpState{Focus: PanelViewport, SelectMode: true}},
		{"palette open", ShortHelpState{PaletteOpen: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := shortHelpBindings(tc.state)
			if len(b) < 3 || len(b) > 7 {
				t.Errorf("len=%d, want 3..7. bindings=%v", len(b), keysOf(b))
			}
		})
	}
}

func newTestShortHelpModel(t *testing.T) ShortHelpModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewShortHelpModel(&theme)
}

func TestShortHelpModel_ViewSingleLine(t *testing.T) {
	m := newTestShortHelpModel(t)
	m.SetWidth(80)
	m.SetState(ShortHelpState{Focus: PanelInput, TurnState: TurnIdle, InputEmpty: true})
	out := m.View()
	if strings.Contains(out, "\n") {
		t.Errorf("View() must be single-line, got newline. out=%q", out)
	}
	if got := ansi.StringWidth(out); got != 80 {
		t.Errorf("View() width=%d, want 80. out=%q", got, out)
	}
}

func TestShortHelpModel_ViewContainsHints(t *testing.T) {
	m := newTestShortHelpModel(t)
	m.SetWidth(120)
	m.SetState(ShortHelpState{TurnState: TurnStreaming})
	out := m.View()
	if !strings.Contains(out, "interrupt") {
		t.Errorf("View() while streaming should contain 'interrupt', got: %q", out)
	}
}

func TestShortHelpModel_ViewTransitions(t *testing.T) {
	m := newTestShortHelpModel(t)
	m.SetWidth(120)

	m.SetState(ShortHelpState{Focus: PanelInput, TurnState: TurnIdle, InputEmpty: true})
	idleOut := m.View()
	if strings.Contains(idleOut, "close") {
		t.Errorf("idle View() should NOT contain palette 'close' hint, got: %q", idleOut)
	}

	m.SetState(ShortHelpState{PaletteOpen: true})
	paletteOut := m.View()
	if !strings.Contains(paletteOut, "close") {
		t.Errorf("palette-open View() should contain 'close' hint, got: %q", paletteOut)
	}
	if idleOut == paletteOut {
		t.Errorf("View() must change when state changes; idle and palette outputs identical")
	}
}

func TestShortHelpModel_NarrowWidth(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked at narrow width: %v", r)
		}
	}()
	m := newTestShortHelpModel(t)
	m.SetWidth(20)
	m.SetState(ShortHelpState{Focus: PanelInput, TurnState: TurnIdle, InputEmpty: true})
	out := m.View()
	if strings.Contains(out, "\n") {
		t.Errorf("View() at narrow width must be single-line, got newline. out=%q", out)
	}
	if out == "" {
		t.Errorf("View() at narrow width must be non-empty")
	}
}

func TestShortHelpModel_ZeroWidth(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked at zero width: %v", r)
		}
	}()
	m := newTestShortHelpModel(t)
	m.SetWidth(0)
	m.SetState(ShortHelpState{Focus: PanelInput, TurnState: TurnIdle, InputEmpty: true})
	_ = m.View()
}
