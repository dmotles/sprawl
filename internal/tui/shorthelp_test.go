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

func TestShortHelpBindings_AlwaysOn(t *testing.T) {
	// QUM-695: with activePanel and yank-mode gone, ShortHelpState only
	// distinguishes turn / input / palette state.
	// QUM-630: when HasQueued is set, ctrl+c is repurposed to "edit" (recall
	// queued to prompt). Skip that state from the ctrl+c=clear/quit
	// always-on check; the dedicated HasQueued tests assert the new copy.
	states := []ShortHelpState{
		{TurnState: TurnIdle, InputEmpty: true},
		{TurnState: TurnStreaming},
		{PopoverOpen: true},
	}
	wantSubs := map[string][]string{
		"F1":     {"help"},
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

func TestShortHelpBindings_NoTabHint(t *testing.T) {
	// QUM-695: the "tab: cycle panel" always-on hint was removed; verify
	// no state surfaces a "tab" key in the bindings.
	states := []ShortHelpState{
		{TurnState: TurnIdle, InputEmpty: true},
		{TurnState: TurnStreaming},
		{HasQueued: true},
	}
	for _, s := range states {
		b := shortHelpBindings(s)
		if containsKey(b, "tab") {
			t.Errorf("state %+v: tab hint should be gone post-QUM-695, got keys=%v", s, keysOf(b))
		}
	}
}

func TestShortHelpBindings_EditorEmpty(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{InputEmpty: true})
	if !containsKey(b, "/") {
		t.Fatalf("expected key %q for empty editor, got keys=%v", "/", keysOf(b))
	}
	if h := hintFor(b, "/"); !strings.Contains(h, "command") {
		t.Errorf("key %q hint=%q, want contains %q", "/", h, "command")
	}
}

func TestShortHelpBindings_EditorNonEmpty(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{InputEmpty: false})
	if containsKey(b, "/") {
		t.Errorf("expected key %q to be absent when editor non-empty, got keys=%v", "/", keysOf(b))
	}
}

// QUM-828: with prompts queued AND idle, the weave-only recall (Ctrl+U) and
// send-all-now (Ctrl+G) affordances are advertised; esc no longer carries a
// queued submit.
func TestShortHelpBindings_HasQueuedIdle_AdvertisesRecallAndSendNow(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{HasQueued: true})
	if !containsKey(b, "ctrl+u") {
		t.Fatalf("expected key ctrl+u when queued+idle, got keys=%v", keysOf(b))
	}
	if h := hintFor(b, "ctrl+u"); !strings.Contains(h, "recall") {
		t.Errorf("ctrl+u hint=%q while queued+idle, want contains %q", h, "recall")
	}
	if !containsKey(b, "ctrl+g") {
		t.Fatalf("expected key ctrl+g when queued+idle, got keys=%v", keysOf(b))
	}
	if h := hintFor(b, "ctrl+g"); !strings.Contains(h, "send now") {
		t.Errorf("ctrl+g hint=%q while queued+idle, want contains %q", h, "send now")
	}
}

// QUM-828: with prompts queued AND an active turn, esc is a bare interrupt
// (no content) and the weave-only recall (Ctrl+U) affordance is advertised.
func TestShortHelpBindings_HasQueuedStreaming_AdvertisesInterruptAndRecall(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{TurnState: TurnStreaming, HasQueued: true})
	if !containsKey(b, "esc") {
		t.Fatalf("expected key %q when queued+streaming, got keys=%v", "esc", keysOf(b))
	}
	if h := hintFor(b, "esc"); !strings.Contains(h, "interrupt") || strings.Contains(h, "send") {
		t.Errorf("esc hint=%q while queued+streaming, want a bare %q (no content)", h, "interrupt")
	}
	if !containsKey(b, "ctrl+u") {
		t.Fatalf("expected key ctrl+u when queued+streaming, got keys=%v", keysOf(b))
	}
	if h := hintFor(b, "ctrl+u"); !strings.Contains(h, "recall") {
		t.Errorf("ctrl+u hint=%q while queued+streaming, want contains %q", h, "recall")
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

func TestShortHelpBindings_PopoverOpen(t *testing.T) {
	b := shortHelpBindings(ShortHelpState{PopoverOpen: true})
	if !containsKey(b, "enter") {
		t.Errorf("popover-open: expected key %q, got keys=%v", "enter", keysOf(b))
	}
	if !containsKey(b, "esc") {
		t.Fatalf("popover-open: expected key esc, got keys=%v", keysOf(b))
	}
	if h := hintFor(b, "esc"); !strings.Contains(h, "close") {
		t.Errorf("popover-open esc hint=%q, want contains %q", h, "close")
	}
}

func TestShortHelpBindings_CountBounded(t *testing.T) {
	cases := []struct {
		name  string
		state ShortHelpState
	}{
		{"input empty", ShortHelpState{TurnState: TurnIdle, InputEmpty: true}},
		{"input queued", ShortHelpState{HasQueued: true}},
		{"streaming", ShortHelpState{TurnState: TurnStreaming}},
		{"thinking", ShortHelpState{TurnState: TurnThinking}},
		{"popover open", ShortHelpState{PopoverOpen: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := shortHelpBindings(tc.state)
			if len(b) < 2 || len(b) > 5 {
				t.Errorf("len=%d, want 2..5. bindings=%v", len(b), keysOf(b))
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
	m.SetState(ShortHelpState{TurnState: TurnIdle, InputEmpty: true})
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

	m.SetState(ShortHelpState{TurnState: TurnIdle, InputEmpty: true})
	idleOut := m.View()
	if strings.Contains(idleOut, "close") {
		t.Errorf("idle View() should NOT contain palette 'close' hint, got: %q", idleOut)
	}

	m.SetState(ShortHelpState{PopoverOpen: true})
	paletteOut := m.View()
	if !strings.Contains(paletteOut, "close") {
		t.Errorf("popover-open View() should contain 'close' hint, got: %q", paletteOut)
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
	m.SetState(ShortHelpState{TurnState: TurnIdle, InputEmpty: true})
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
	m.SetState(ShortHelpState{TurnState: TurnIdle, InputEmpty: true})
	_ = m.View()
}
