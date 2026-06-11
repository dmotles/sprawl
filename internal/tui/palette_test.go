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
	// QUM-793: Esc now hides the palette synchronously and returns no cmd.
	p, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if p.Visible() {
		t.Error("Esc should hide the palette synchronously")
	}
	if cmd != nil {
		t.Errorf("Esc should not emit a cmd (synchronous close), got %T", cmd())
	}
}

func TestPaletteModel_EnterOnExitEmitsQuitAndClose(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	// /exit is index 0 (first in registry).
	// QUM-793: palette hides synchronously; cmd carries only the action.
	p, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.Visible() {
		t.Error("Enter on /exit should hide the palette synchronously")
	}
	if cmd == nil {
		t.Fatal("Enter should emit an action cmd")
	}
	if _, ok := cmd().(PaletteQuitMsg); !ok {
		t.Errorf("Enter on /exit returned %T, want PaletteQuitMsg", cmd())
	}
}

func TestPaletteModel_EnterOnHelpEmitsToggleHelp(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	// Move cursor to /help (index 1).
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	// QUM-793: palette hides synchronously; cmd carries only the action.
	p, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.Visible() {
		t.Error("Enter on /help should hide the palette synchronously")
	}
	if cmd == nil {
		t.Fatal("Enter should emit an action cmd")
	}
	if _, ok := cmd().(ToggleHelpMsg); !ok {
		t.Errorf("Enter on /help returned %T, want ToggleHelpMsg", cmd())
	}
}

func TestPaletteModel_EnterOnHandoffEmitsInjectPrompt(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	// Move cursor to /handoff (index 3 after /tree was added at index 2).
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	// QUM-793: palette hides synchronously; cmd carries only the action.
	p, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.Visible() {
		t.Error("Enter on /handoff should hide the palette synchronously")
	}
	if cmd == nil {
		t.Fatal("Enter should emit an action cmd")
	}
	inject, ok := cmd().(InjectPromptMsg)
	if !ok {
		t.Fatalf("Enter on /handoff returned %T, want InjectPromptMsg", cmd())
	}
	if inject.Template != commands.HandoffPromptTemplate {
		t.Error("InjectPromptMsg.Template != HandoffPromptTemplate")
	}
}

// QUM-721 — /usage palette dispatch.
func TestPaletteModel_EnterOnUsageEmitsShowUsage(t *testing.T) {
	p := newTestPaletteModel(t)
	p.Show()
	// Filter to /usage exclusively.
	for _, r := range "usage" {
		p, _ = p.Update(tea.KeyPressMsg{Code: r})
	}
	if len(p.matches) != 1 || p.matches[0].Name != "/usage" {
		t.Fatalf("setup: filter 'usage' matches = %v, want [/usage]", p.matches)
	}
	// QUM-793: palette hides synchronously; cmd carries only the action.
	p, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.Visible() {
		t.Error("Enter on /usage should hide the palette synchronously")
	}
	if cmd == nil {
		t.Fatal("Enter should emit an action cmd")
	}
	if _, ok := cmd().(ShowUsageMsg); !ok {
		t.Errorf("Enter on /usage returned %T, want ShowUsageMsg", cmd())
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
	for _, name := range []string{"/exit", "/help", "/handoff", "/usage"} {
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
	// /switch is a mode transition: no cmd should be emitted at all.
	if cmd != nil {
		t.Errorf("Enter on /switch should not emit a cmd (stays open for agent selection), got %T", cmd())
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
	// QUM-793: palette hides synchronously; cmd carries only the action.
	p, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.Visible() {
		t.Error("Enter in agent mode should hide the palette synchronously")
	}
	if cmd == nil {
		t.Fatal("Enter in agent mode should emit a cmd")
	}
	sel, ok := cmd().(AgentSelectedMsg)
	if !ok {
		t.Fatalf("Enter in agent mode returned %T, want AgentSelectedMsg", cmd())
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

// QUM-793: palette dispatch must close the palette synchronously and emit
// the action cmd alone — no race between ClosePaletteMsg and the action's
// modal-gate check on m.showPalette.
//
// This test drives the full AppModel path: open the palette, simulate the
// user picking /tree / /usage / /help, run the palette's returned cmd
// through app.Update, and assert the target modal opens. With the legacy
// tea.Batch(closeCmd, action) approach, action-first delivery would hit
// the m.showPalette gate and silently no-op for /tree and /usage.
func TestAppModel_PaletteDispatchOpensTargetModalSynchronously(t *testing.T) {
	cases := []struct {
		name   string
		filter string // typed into palette to isolate the command
		check  func(t *testing.T, app AppModel)
	}{
		{
			name:   "/tree",
			filter: "tree",
			check: func(t *testing.T, app AppModel) {
				if !app.showTree {
					t.Error("after /tree palette dispatch, showTree must be true")
				}
			},
		},
		{
			name:   "/usage",
			filter: "usage",
			check: func(t *testing.T, app AppModel) {
				if !app.showUsage {
					t.Error("after /usage palette dispatch, showUsage must be true")
				}
			},
		},
		{
			name:   "/help",
			filter: "help",
			check: func(t *testing.T, app AppModel) {
				if !app.showHelp {
					t.Error("after /help palette dispatch, showHelp must be true")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := readyApp(t)
			u, _ := app.Update(OpenPaletteMsg{})
			app = u.(AppModel)
			if !app.showPalette {
				t.Fatal("setup: palette should be open")
			}
			// Type the filter via the app (routes to palette).
			for _, r := range tc.filter {
				u, _ = app.Update(tea.KeyPressMsg{Code: r})
				app = u.(AppModel)
			}
			// Enter dispatches.
			u, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			app = u.(AppModel)
			// Palette must be closed synchronously — before the action msg is
			// dispatched — so the action's modal-gate sees showPalette=false.
			if app.showPalette {
				t.Error("palette must be closed synchronously after Enter dispatch")
			}
			if app.palette.Visible() {
				t.Error("palette.Visible() must be false synchronously after Enter dispatch")
			}
			// Run the returned cmd's msg(s) through app.Update.
			if cmd != nil {
				walkBatch(cmd(), func(m tea.Msg) {
					u, _ = app.Update(m)
					app = u.(AppModel)
				})
			}
			tc.check(t, app)
		})
	}
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
