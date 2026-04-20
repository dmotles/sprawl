package tui

import (
	"strings"
	"testing"
)

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

func TestStatusBar_CumulativeCost(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	m.SetTurnCost(0.01)
	m.SetTurnCost(0.02)
	view := m.View()
	if !strings.Contains(view, "$0.0300") {
		t.Errorf("View() should contain cumulative cost '$0.0300', got:\n%s", view)
	}
}
