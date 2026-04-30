package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestStatusBar_ViewFitsDeclaredWidth guards against the bug where
// StatusBar style's Padding(0,1) plus View()'s Width(m.width).Render(line)
// rendered m.width+2 cells and wrapped the trailing "? Help" onto a second
// line at most terminal widths. Fix: drop Padding; View() manages its own
// left/right spacing inside line.
func TestStatusBar_ViewFitsDeclaredWidth(t *testing.T) {
	m := newTestStatusBarModel(t)
	for _, w := range []int{40, 80, 120, 190, 300} {
		m.SetWidth(w)
		m.SetRestartElapsed(42 * 1000000000) // 42s — keeps the line longish
		view := m.View()
		for _, ln := range strings.Split(view, "\n") {
			if ln == "" {
				continue
			}
			if got := ansi.StringWidth(ln); got > w {
				t.Errorf("width=%d: rendered line width=%d (want ≤ %d). line=%q", w, got, w, ln)
			}
		}
	}
}

func newTestStatusBarModel(t *testing.T) StatusBarModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewStatusBarModel(&theme, "myrepo", "v1.0.0", 3)
}

func TestStatusBar_ContainsRepoName(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	view := m.View()
	if !strings.Contains(view, "myrepo") {
		t.Errorf("View() should contain repo name 'myrepo', got:\n%s", view)
	}
}

func TestStatusBar_ContainsVersion(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	view := m.View()
	if !strings.Contains(view, "v1.0.0") {
		t.Errorf("View() should contain version 'v1.0.0', got:\n%s", view)
	}
}

func TestStatusBar_ContainsAgentCount(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	view := m.View()
	if !strings.Contains(view, "3") {
		t.Errorf("View() should contain agent count '3', got:\n%s", view)
	}
}

func TestStatusBar_SetWidth(t *testing.T) {
	m := newTestStatusBarModel(t)
	// Should not panic.
	m.SetWidth(120)
}

// --- New tests for turn state display ---

func TestStatusBar_SetTurnState_Thinking(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	m.SetTurnState(TurnThinking)
	view := m.View()
	if !strings.Contains(view, "Thinking") {
		t.Errorf("View() with TurnThinking should contain 'Thinking', got:\n%s", view)
	}
}

func TestStatusBar_SetTurnState_Streaming(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	m.SetTurnState(TurnStreaming)
	view := m.View()
	if !strings.Contains(view, "Streaming") {
		t.Errorf("View() with TurnStreaming should contain 'Streaming', got:\n%s", view)
	}
}

func TestStatusBar_SetTurnState_Idle(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	// Set to thinking first, then back to idle
	m.SetTurnState(TurnThinking)
	m.SetTurnState(TurnIdle)
	view := m.View()
	// Should NOT contain thinking/streaming when idle
	if strings.Contains(view, "Thinking") {
		t.Errorf("View() with TurnIdle should not contain 'Thinking', got:\n%s", view)
	}
	if strings.Contains(view, "Streaming") {
		t.Errorf("View() with TurnIdle should not contain 'Streaming', got:\n%s", view)
	}
}

func TestStatusBar_SetTurnState_Complete(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	m.SetTurnState(TurnComplete)
	view := m.View()
	// Complete state should not show Thinking or Streaming
	if strings.Contains(view, "Thinking") {
		t.Errorf("View() with TurnComplete should not contain 'Thinking', got:\n%s", view)
	}
}

func TestStatusBar_SetTurnCost(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	m.SetTurnCost(0.0123)
	view := m.View()
	if !strings.Contains(view, "$0.0123") {
		t.Errorf("View() should contain cost '$0.0123', got:\n%s", view)
	}
}

func TestStatusBar_SetSessionID_Displayed(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	m.SetSessionID("a1b2c3d4")
	view := m.View()
	if !strings.Contains(view, "sess:a1b2c3d4") {
		t.Errorf("View() with SetSessionID should contain 'sess:a1b2c3d4', got:\n%s", view)
	}
}

func TestStatusBar_SessionID_OmittedWhenEmpty(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	view := m.View()
	if strings.Contains(view, "sess:") {
		t.Errorf("View() without SetSessionID should not contain 'sess:', got:\n%s", view)
	}
}

// TestStatusBar_CumulativeCost verifies that SetTurnCost replaces (not
// accumulates) because total_cost_usd from Claude is session-cumulative.
// Two calls with 0.01 then 0.02 should show 0.02, not 0.03. (QUM-366)
func TestStatusBar_CumulativeCost(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	m.SetTurnCost(0.01)
	m.SetTurnCost(0.02)
	view := m.View()
	if !strings.Contains(view, "$0.0200") {
		t.Errorf("View() should contain replaced cost '$0.0200', got:\n%s", view)
	}
	if strings.Contains(view, "$0.0300") {
		t.Errorf("View() should NOT contain accumulated cost '$0.0300' (double-counting bug), got:\n%s", view)
	}
}

// --- QUM-385: Token counter tests ---

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{500, "500"},
		{999, "999"},
		{1000, "1k"},
		{1500, "1.5k"},
		{42300, "42.3k"},
		{100000, "100k"},
		{999999, "1000.0k"},
		{1000000, "1M"},
		{1500000, "1.5M"},
	}
	for _, tc := range tests {
		got := formatTokenCount(tc.input)
		if got != tc.want {
			t.Errorf("formatTokenCount(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestModelContextLimit_KnownModels(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"claude-opus-4-7-20260301", 1_000_000},
		{"claude-sonnet-4-6-20250514", 1_000_000},
		{"claude-haiku-4-0-20260301", 200_000},
	}
	for _, tc := range tests {
		got := modelContextLimit(tc.model)
		if got != tc.want {
			t.Errorf("modelContextLimit(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

func TestModelContextLimit_Unknown(t *testing.T) {
	got := modelContextLimit("gpt-4o")
	if got != defaultContextLimit {
		t.Errorf("modelContextLimit(unknown) = %d, want %d", got, defaultContextLimit)
	}
}

func TestModelContextLimit_Empty(t *testing.T) {
	got := modelContextLimit("")
	if got != defaultContextLimit {
		t.Errorf("modelContextLimit(\"\") = %d, want %d", got, defaultContextLimit)
	}
}

func TestStatusBar_TokenCounter_Displayed(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	m.SetContextLimit(1_000_000)
	m.SetTokenUsage(42300)
	view := m.View()
	if !strings.Contains(view, "42.3k/1M tokens") {
		t.Errorf("View() should contain '42.3k/1M tokens', got:\n%s", view)
	}
}

func TestStatusBar_TokenCounter_OmittedWhenZero(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	view := m.View()
	if strings.Contains(view, "tokens") {
		t.Errorf("View() should not contain 'tokens' when no usage set, got:\n%s", view)
	}
}

func TestStatusBar_TokenCounter_OmittedWhenNoLimit(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	m.SetTokenUsage(5000)
	view := m.View()
	if strings.Contains(view, "tokens") {
		t.Errorf("View() should not contain 'tokens' when no limit set, got:\n%s", view)
	}
}

func TestStatusBar_TokenCounter_UpdatesOnNewValue(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	m.SetContextLimit(1_000_000)
	m.SetTokenUsage(10000)
	m.SetTokenUsage(20000)
	view := m.View()
	if !strings.Contains(view, "20k/1M tokens") {
		t.Errorf("View() should contain '20k/1M tokens' after update, got:\n%s", view)
	}
	if strings.Contains(view, "10k") {
		t.Errorf("View() should not contain old '10k' value, got:\n%s", view)
	}
}
