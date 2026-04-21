package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/tui/commands"
)

func newTestPaletteModel(t *testing.T) PaletteModel {
	t.Helper()
	theme := NewTheme("colour212")
	p := NewPaletteModel(&theme)
	p.SetSize(120, 40)
	return p
}

func TestPaletteModel_InitiallyHidden(t *testing.T) {
	p := newTestPaletteModel(t)
	if p.Visible() {
		t.Error("new palette should be hidden")
	}
}

func TestPaletteModel_ShowMakesVisibleAndResetsState(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	if !p.Visible() {
		t.Error("Show() should set visible")
	}
	if p.filter != "" {
		t.Errorf("filter = %q, want empty after Show()", p.filter)
	}
	if p.cursor != 0 {
		t.Errorf("cursor = %d, want 0 after Show()", p.cursor)
	}
	if len(p.matches) != len(commands.All()) {
		t.Errorf("matches len = %d, want %d", len(p.matches), len(commands.All()))
	}
}

func TestPaletteModel_HideClearsVisible(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	p.Hide()
	if p.Visible() {
		t.Error("Hide() should clear visible")
	}
}

func TestPaletteModel_TypingFiltersMatches(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	p, _ = p.Update(tea.KeyPressMsg{Code: 'h'})
	if p.filter != "h" {
		t.Errorf("filter = %q, want %q", p.filter, "h")
	}
	if len(p.matches) != 2 {
		t.Errorf("matches len after 'h' = %d, want 2 (/help,/handoff)", len(p.matches))
	}
	p, _ = p.Update(tea.KeyPressMsg{Code: 'a'})
	if p.filter != "ha" {
		t.Errorf("filter = %q, want %q", p.filter, "ha")
	}
	if len(p.matches) != 1 {
		t.Errorf("matches len after 'ha' = %d, want 1 (/handoff)", len(p.matches))
	}
	if p.matches[0].Name != "/handoff" {
		t.Errorf("match = %q, want /handoff", p.matches[0].Name)
	}
}

func TestPaletteModel_BackspaceRemovesLastFilterChar(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	p, _ = p.Update(tea.KeyPressMsg{Code: 'h'})
	p, _ = p.Update(tea.KeyPressMsg{Code: 'a'})
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if p.filter != "h" {
		t.Errorf("filter after backspace = %q, want %q", p.filter, "h")
	}
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if p.filter != "" {
		t.Errorf("filter after 2nd backspace = %q, want empty", p.filter)
	}
	// Extra backspace at empty is a no-op.
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if p.filter != "" {
		t.Errorf("filter after 3rd backspace = %q, want empty", p.filter)
	}
	if !p.Visible() {
		t.Error("palette should remain visible at empty filter + backspace")
	}
}

func TestPaletteModel_DownArrowAdvancesCursor(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if p.cursor != 1 {
		t.Errorf("cursor after Down = %d, want 1", p.cursor)
	}
}

func TestPaletteModel_UpArrowWrapsBackwards(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if p.cursor != len(p.matches)-1 {
		t.Errorf("cursor after Up from 0 = %d, want %d (wrap)", p.cursor, len(p.matches)-1)
	}
}

func TestPaletteModel_DownArrowWrapsAtEnd(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	n := len(p.matches)
	for i := 0; i < n; i++ {
		p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if p.cursor != 0 {
		t.Errorf("cursor after %d Downs = %d, want 0 (wrap)", n, p.cursor)
	}
}

func TestPaletteModel_TabNavigatesForward(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if p.cursor != 1 {
		t.Errorf("cursor after Tab = %d, want 1", p.cursor)
	}
}

func TestPaletteModel_ShiftTabNavigatesBackward(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if p.cursor != len(p.matches)-1 {
		t.Errorf("cursor after Shift+Tab = %d, want %d", p.cursor, len(p.matches)-1)
	}
}

func TestPaletteModel_EscClosesPalette(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	_, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc should emit a cmd")
	}
	msg := cmd()
	if _, ok := msg.(ClosePaletteMsg); !ok {
		t.Errorf("Esc returned %T, want ClosePaletteMsg", msg)
	}
}

func TestPaletteModel_EnterOnExitEmitsQuitAndClose(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	// /exit is index 0 (first in registry).
	_, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should emit a cmd")
	}
	msg := cmd()
	gotClose, gotQuit := inspectBatch(msg)
	if !gotClose {
		t.Error("Enter on /exit should emit ClosePaletteMsg")
	}
	if !gotQuit {
		t.Error("Enter on /exit should emit PaletteQuitMsg")
	}
}

func TestPaletteModel_EnterOnHelpEmitsToggleHelp(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	// Move cursor to /help (index 1).
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should emit a cmd")
	}
	msg := cmd()
	gotClose := false
	gotToggle := false
	walkBatch(msg, func(m tea.Msg) {
		switch m.(type) {
		case ClosePaletteMsg:
			gotClose = true
		case ToggleHelpMsg:
			gotToggle = true
		}
	})
	if !gotClose {
		t.Error("Enter on /help should emit ClosePaletteMsg")
	}
	if !gotToggle {
		t.Error("Enter on /help should emit ToggleHelpMsg")
	}
}

func TestPaletteModel_EnterOnHandoffEmitsInjectPrompt(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	// Move cursor to /handoff (index 2).
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should emit a cmd")
	}
	msg := cmd()
	gotClose := false
	var inject *InjectPromptMsg
	walkBatch(msg, func(m tea.Msg) {
		switch v := m.(type) {
		case ClosePaletteMsg:
			gotClose = true
		case InjectPromptMsg:
			vv := v
			inject = &vv
		}
	})
	if !gotClose {
		t.Error("Enter on /handoff should emit ClosePaletteMsg")
	}
	if inject == nil {
		t.Fatal("Enter on /handoff should emit InjectPromptMsg")
	}
	if inject.Template != commands.HandoffPromptTemplate {
		t.Error("InjectPromptMsg.Template != HandoffPromptTemplate")
	}
}

func TestPaletteModel_EnterWithNoMatchesIsNoop(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	// Filter to nothing.
	p, _ = p.Update(tea.KeyPressMsg{Code: 'z'})
	p, _ = p.Update(tea.KeyPressMsg{Code: 'z'})
	if len(p.matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(p.matches))
	}
	_, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		// Must not produce quit/inject/toggle.
		msg := cmd()
		walkBatch(msg, func(m tea.Msg) {
			switch m.(type) {
			case PaletteQuitMsg, InjectPromptMsg, ToggleHelpMsg:
				t.Errorf("Enter with no matches should not emit %T", m)
			}
		})
	}
}

func TestPaletteModel_ViewHiddenIsEmpty(t *testing.T) {
	p := newTestPaletteModel(t)
	v := p.View()
	if strings.TrimSpace(v) != "" {
		t.Errorf("View() hidden should be empty, got %q", v)
	}
}

func TestPaletteModel_ViewVisibleListsCommands(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	v := p.View()
	for _, name := range []string{"/exit", "/help", "/handoff"} {
		if !strings.Contains(v, name) {
			t.Errorf("View() missing %q\n%s", name, v)
		}
	}
}

// --- Agent switching mode (QUM-279) ---

func TestPaletteModel_TypingSwitchTransitionsToAgentMode(t *testing.T) {
	p := newTestPaletteModel(t)
	p.SetAgents([]string{"weave", "finn", "ghost"})
	p.Show()
	for _, r := range "switch" {
		p, _ = p.Update(tea.KeyPressMsg{Code: r})
	}
	if !p.InAgentMode() {
		t.Fatal("typing 'switch' should transition palette into agent-mode")
	}
	if p.filter != "" {
		t.Errorf("filter after transition = %q, want empty", p.filter)
	}
	// Should now list all agents as matches.
	if len(p.agentMatches) != 3 {
		t.Errorf("agentMatches len = %d, want 3 (all agents)", len(p.agentMatches))
	}
}

func TestPaletteModel_EnterOnSwitchCommandTransitionsToAgentMode(t *testing.T) {
	p := newTestPaletteModel(t)
	p.SetAgents([]string{"weave", "finn", "ghost"})
	p.Show()
	// Filter down to /switch exclusively.
	for _, r := range "sw" {
		p, _ = p.Update(tea.KeyPressMsg{Code: r})
	}
	if len(p.matches) != 1 || p.matches[0].Name != "/switch" {
		t.Fatalf("setup: filter 'sw' matches = %v, want [/switch]", p.matches)
	}
	// Enter should transition to agent mode, not close.
	p2, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !p2.InAgentMode() {
		t.Error("Enter on /switch command should transition to agent mode")
	}
	if !p2.Visible() {
		t.Error("palette should remain visible after /switch transition")
	}
	// The cmd (if any) should NOT emit ClosePaletteMsg.
	if cmd != nil {
		msg := cmd()
		walkBatch(msg, func(m tea.Msg) {
			if _, ok := m.(ClosePaletteMsg); ok {
				t.Error("Enter on /switch should not emit ClosePaletteMsg — stays open for agent selection")
			}
		})
	}
}

func TestPaletteModel_AgentModeFuzzyFilters(t *testing.T) {
	p := newTestPaletteModel(t)
	p.SetAgents([]string{"weave", "finn", "ghost", "ratz"})
	p.Show()
	for _, r := range "switch" {
		p, _ = p.Update(tea.KeyPressMsg{Code: r})
	}
	// Type 'fi' — should match only finn via subsequence.
	p, _ = p.Update(tea.KeyPressMsg{Code: 'f'})
	p, _ = p.Update(tea.KeyPressMsg{Code: 'i'})
	if len(p.agentMatches) != 1 || p.agentMatches[0] != "finn" {
		t.Errorf("agentMatches after 'fi' = %v, want [finn]", p.agentMatches)
	}
}

func TestPaletteModel_AgentModeEnterEmitsSwitchAndClose(t *testing.T) {
	p := newTestPaletteModel(t)
	p.SetAgents([]string{"weave", "finn", "ghost"})
	p.Show()
	for _, r := range "switch" {
		p, _ = p.Update(tea.KeyPressMsg{Code: r})
	}
	p, _ = p.Update(tea.KeyPressMsg{Code: 'f'})
	p, _ = p.Update(tea.KeyPressMsg{Code: 'i'})
	_, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter in agent mode should emit a cmd")
	}
	msg := cmd()
	gotClose := false
	var sel *AgentSelectedMsg
	walkBatch(msg, func(m tea.Msg) {
		switch v := m.(type) {
		case ClosePaletteMsg:
			gotClose = true
		case AgentSelectedMsg:
			vv := v
			sel = &vv
		}
	})
	if !gotClose {
		t.Error("Enter in agent mode should emit ClosePaletteMsg")
	}
	if sel == nil {
		t.Fatal("Enter in agent mode should emit AgentSelectedMsg")
	}
	if sel.Name != "finn" {
		t.Errorf("AgentSelectedMsg.Name = %q, want %q", sel.Name, "finn")
	}
}

func TestPaletteModel_AgentModeBackspaceReturnsToCommandMode(t *testing.T) {
	p := newTestPaletteModel(t)
	p.SetAgents([]string{"weave", "finn"})
	p.Show()
	for _, r := range "switch" {
		p, _ = p.Update(tea.KeyPressMsg{Code: r})
	}
	if !p.InAgentMode() {
		t.Fatal("setup: should be in agent mode")
	}
	// Backspace on empty filter should drop back to command mode.
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if p.InAgentMode() {
		t.Error("Backspace on empty agent-mode filter should return to command mode")
	}
}

func TestPaletteModel_AgentModeEnterWithNoMatchesNoop(t *testing.T) {
	p := newTestPaletteModel(t)
	p.SetAgents([]string{"finn"})
	p.Show()
	for _, r := range "switch" {
		p, _ = p.Update(tea.KeyPressMsg{Code: r})
	}
	p, _ = p.Update(tea.KeyPressMsg{Code: 'z'})
	p, _ = p.Update(tea.KeyPressMsg{Code: 'z'})
	if len(p.agentMatches) != 0 {
		t.Fatalf("expected 0 agent matches, got %v", p.agentMatches)
	}
	_, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		msg := cmd()
		walkBatch(msg, func(m tea.Msg) {
			if _, ok := m.(AgentSelectedMsg); ok {
				t.Error("Enter with no agent matches should not emit AgentSelectedMsg")
			}
		})
	}
}

// inspectBatch returns (gotClose, gotQuit) for the convenience of /exit test.
func inspectBatch(msg tea.Msg) (bool, bool) {
	var gotClose, gotQuit bool
	walkBatch(msg, func(m tea.Msg) {
		switch m.(type) {
		case ClosePaletteMsg:
			gotClose = true
		case PaletteQuitMsg:
			gotQuit = true
		}
	})
	return gotClose, gotQuit
}

// walkBatch invokes fn on msg and, if msg is a tea.BatchMsg (slice of cmds),
// expands each into its produced msg. This is how we inspect the composite
// cmds emitted by Enter handlers.
func walkBatch(msg tea.Msg, fn func(tea.Msg)) {
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			walkBatch(c(), fn)
		}
		return
	}
	fn(msg)
}
