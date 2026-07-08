package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/attach"
	"github.com/dmotles/sprawl/internal/tui/commands"
)

// paletteMode enumerates the palette's two input modes. It starts in
// modeCommand filtering the command registry. When the user selects /switch
// (or types "switch"), it transitions to modeAgent where the filter fuzzy-
// matches the caller-supplied agent list.
type paletteMode int

const (
	modeCommand paletteMode = iota
	modeAgent
	// modeAttach captures a free-form `/attach` argument line (file paths +
	// optional quoted prompt). Unlike modeCommand/modeAgent it accepts a broad
	// charset (spaces, slashes, dots, quotes) into attachArgs. QUM-860.
	modeAttach
)

// PaletteModel renders a floating centered command palette overlay. It is
// shown/hidden by the app: OpenPaletteMsg flips it on; the palette hides
// itself synchronously on Esc and on command dispatch.
// While visible, all key events route here (see app.go) — the palette owns
// the filter input and navigation.
type PaletteModel struct {
	theme   *Theme
	width   int
	height  int
	visible bool

	mode    paletteMode
	filter  string
	cursor  int
	matches []commands.Command

	// Agent-mode state. agents is set by the app before Show() via
	// SetAgents(); agentMatches is derived from filter.
	agents       []string
	agentMatches []string

	// attachArgs is the free-form argument buffer in modeAttach (QUM-860).
	attachArgs string
}

// NewPaletteModel constructs a hidden palette model.
func NewPaletteModel(theme *Theme) PaletteModel {
	return PaletteModel{theme: theme}
}

// Show makes the palette visible, resets filter/cursor/mode, and populates
// matches from the full registry.
func (m *PaletteModel) Show() {
	m.visible = true
	m.mode = modeCommand
	m.filter = ""
	m.cursor = 0
	m.refreshMatches()
}

// SetAgents supplies the agent name list that agent-mode filters against.
// The caller (app.go) should call this immediately before Show() so the list
// reflects the current tree.
func (m *PaletteModel) SetAgents(names []string) {
	m.agents = append(m.agents[:0], names...)
}

// InAgentMode reports whether the palette is currently in the agent-selection
// sub-mode triggered by /switch.
func (m PaletteModel) InAgentMode() bool { return m.mode == modeAgent }

// InAttachMode reports whether the palette is currently in the attach-argument
// sub-mode triggered by /attach (QUM-860).
func (m PaletteModel) InAttachMode() bool { return m.mode == modeAttach }

// Hide hides the palette.
func (m *PaletteModel) Hide() { m.visible = false }

// Visible reports whether the overlay is showing.
func (m PaletteModel) Visible() bool { return m.visible }

// SetSize updates the available area for centering the overlay.
func (m *PaletteModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

func (m *PaletteModel) refreshMatches() {
	if m.mode == modeAgent {
		m.agentMatches = commands.FuzzyMatchAgents(m.filter, m.agents)
		if m.cursor >= len(m.agentMatches) {
			m.cursor = 0
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		return
	}
	m.matches = commands.Filter(m.filter)
	if m.cursor >= len(m.matches) {
		m.cursor = 0
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// enterAgentMode switches the palette into agent-selection mode, clearing
// the filter and populating matches from the full agent list.
func (m *PaletteModel) enterAgentMode() {
	m.mode = modeAgent
	m.filter = ""
	m.cursor = 0
	m.refreshMatches()
}

// exitAgentMode returns to command mode, resetting the filter and matches.
func (m *PaletteModel) exitAgentMode() {
	m.mode = modeCommand
	m.filter = ""
	m.cursor = 0
	m.refreshMatches()
}

// enterAttachMode switches the palette into attach-argument mode, clearing the
// argument buffer (QUM-860).
func (m *PaletteModel) enterAttachMode() {
	m.mode = modeAttach
	m.attachArgs = ""
	m.cursor = 0
}

// exitAttachMode returns to command mode, clearing the argument buffer.
func (m *PaletteModel) exitAttachMode() {
	m.mode = modeCommand
	m.attachArgs = ""
	m.filter = ""
	m.cursor = 0
	m.refreshMatches()
}

// Update handles key events while the palette is visible. It emits:
//   - hides synchronously on Esc (no cmd) — caller observes via Visible()
//   - navigation on Up/Down/Tab/Shift+Tab (no cmd)
//   - hides synchronously + (PaletteQuitMsg | ToggleHelpMsg | ShowUsageMsg |
//     ToggleTreeMsg | InjectPromptMsg | AgentSelectedMsg) on Enter (QUM-793:
//     synchronous close avoids the tea.Batch(closeCmd, action) race where
//     the action's modal-gate would observe stale showPalette=true)
//   - filter edits on printable chars and Backspace (no cmd)
func (m PaletteModel) Update(msg tea.KeyPressMsg) (PaletteModel, tea.Cmd) {
	if !m.visible {
		return m, nil
	}

	switch msg.Code {
	case tea.KeyEscape:
		m.visible = false
		return m, nil
	case tea.KeyEnter:
		return m.dispatchSelected()
	case tea.KeyUp:
		m.moveCursor(-1)
		return m, nil
	case tea.KeyDown:
		m.moveCursor(1)
		return m, nil
	case tea.KeyTab:
		if msg.Mod&tea.ModShift != 0 {
			m.moveCursor(-1)
		} else {
			m.moveCursor(1)
		}
		return m, nil
	case tea.KeyBackspace:
		if m.mode == modeAttach {
			if len(m.attachArgs) > 0 {
				r := []rune(m.attachArgs)
				m.attachArgs = string(r[:len(r)-1])
			} else {
				// Backspace at empty arg buffer returns to command mode.
				m.exitAttachMode()
			}
			return m, nil
		}
		if len(m.filter) > 0 {
			r := []rune(m.filter)
			m.filter = string(r[:len(r)-1])
			m.refreshMatches()
		} else if m.mode == modeAgent {
			// Backspace at empty filter in agent mode returns to command mode.
			m.exitAgentMode()
		}
		return m, nil
	}

	// modeAttach accepts a broad charset (paths, spaces, dots, slashes, quotes)
	// into the free-form argument buffer — distinct from the restricted command/
	// agent filter charset below. QUM-860.
	if m.mode == modeAttach {
		if r := msg.Code; r > 0 && r < 0x10FFFF && unicode.IsGraphic(r) {
			m.attachArgs += string(r)
		}
		return m, nil
	}

	// Printable ASCII letter/digit/dash/underscore appends to filter.
	if msg.Code > 0 && msg.Code < 0x10FFFF {
		r := msg.Code
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			m.filter += string(r)
			// In command mode, typing "switch" auto-transitions to agent
			// mode — lets `/switch fi` flow work without a space separator.
			if m.mode == modeCommand && strings.EqualFold(m.filter, "switch") {
				m.enterAgentMode()
			} else {
				m.refreshMatches()
			}
		}
	}
	return m, nil
}

func (m *PaletteModel) moveCursor(delta int) {
	n := len(m.matches)
	if m.mode == modeAgent {
		n = len(m.agentMatches)
	}
	if n == 0 {
		m.cursor = 0
		return
	}
	m.cursor = (m.cursor + delta + n) % n
}

func (m PaletteModel) dispatchSelected() (PaletteModel, tea.Cmd) {
	if m.mode == modeAttach {
		paths, prompt := attach.ParseArgs(m.attachArgs)
		m.visible = false
		if len(paths) == 0 {
			// Nothing actionable typed; just close.
			return m, nil
		}
		return m, sendMsgCmd(AttachMsg{Paths: paths, Prompt: prompt})
	}

	if m.mode == modeAgent {
		if len(m.agentMatches) == 0 || m.cursor >= len(m.agentMatches) {
			return m, nil
		}
		name := m.agentMatches[m.cursor]
		m.visible = false
		return m, sendMsgCmd(AgentSelectedMsg{Name: name})
	}

	if len(m.matches) == 0 || m.cursor >= len(m.matches) {
		return m, nil
	}
	cmd := m.matches[m.cursor]

	var action tea.Cmd
	switch cmd.Kind {
	case commands.KindUI:
		switch cmd.Action {
		case commands.ActionQuit:
			action = sendMsgCmd(PaletteQuitMsg{})
		case commands.ActionToggleHelp:
			action = sendMsgCmd(ToggleHelpMsg{})
		case commands.ActionShowUsage:
			action = sendMsgCmd(ShowUsageMsg{})
		case commands.ActionToggleTree:
			action = sendMsgCmd(ToggleTreeMsg{})
		}
	case commands.KindPromptInjection:
		tmpl := cmd.PromptTemplate
		action = sendMsgCmd(InjectPromptMsg{Template: tmpl})
	case commands.KindAgentSwitch:
		// Transition to agent-selection mode; do NOT close the palette.
		m.enterAgentMode()
		return m, nil
	case commands.KindAttach:
		// Transition to attach-argument mode; do NOT close the palette (QUM-860).
		m.enterAttachMode()
		return m, nil
	}

	// QUM-793: hide synchronously so the AppModel can observe palette
	// closure in the same Update tick that dispatches the action cmd.
	// This avoids the tea.Batch(closeCmd, action) race where the action's
	// modal-gate check would still see m.showPalette=true.
	m.visible = false
	return m, action
}

// View renders the centered palette box. Returns empty string when hidden.
func (m PaletteModel) View() string {
	if !m.visible {
		return ""
	}

	boxWidth := 60
	if m.width > 0 && m.width-8 < boxWidth {
		boxWidth = m.width - 8
	}
	if boxWidth < 40 {
		boxWidth = 40
	}

	// Attach-mode: render a dedicated prompt-line box for the free-form arg line.
	if m.mode == modeAttach {
		return m.viewAttach(boxWidth)
	}

	var sb strings.Builder
	// Filter row.
	if m.mode == modeAgent {
		sb.WriteString(m.theme.AccentText.Render("> /switch "))
	} else {
		sb.WriteString(m.theme.AccentText.Render("> /"))
	}
	sb.WriteString(m.filter)
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", boxWidth-4))
	sb.WriteString("\n")

	// Agent-mode rows.
	if m.mode == modeAgent {
		if len(m.agentMatches) == 0 {
			if len(m.agents) == 0 {
				sb.WriteString(m.theme.NormalText.Render("  no agents available"))
			} else {
				sb.WriteString(m.theme.NormalText.Render("  no matching agents"))
			}
		} else {
			for i, name := range m.agentMatches {
				prefix := "  "
				if i == m.cursor {
					prefix = m.theme.AccentText.Render("› ")
				}
				sb.WriteString(prefix)
				sb.WriteString(m.theme.AccentText.Render(name))
				if i < len(m.agentMatches)-1 {
					sb.WriteString("\n")
				}
			}
		}
		box := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(m.theme.Palette.Primary).
			Background(m.theme.Palette.BgBase).
			Padding(0, 1).
			Width(boxWidth).
			Render(sb.String())
		hint := m.theme.NormalText.Render("↑↓/Tab navigate • Enter switch • ⌫ back • Esc close")
		full := box + "\n" + hint
		if m.width > 0 && m.height > 0 {
			return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, full)
		}
		return full
	}

	// Match rows.
	if len(m.matches) == 0 {
		sb.WriteString(m.theme.NormalText.Render("  no matching commands"))
	} else {
		// Find longest name for column alignment.
		maxNameLen := 0
		for _, c := range m.matches {
			if len(c.Name) > maxNameLen {
				maxNameLen = len(c.Name)
			}
		}
		for i, c := range m.matches {
			prefix := "  "
			if i == m.cursor {
				prefix = m.theme.AccentText.Render("› ")
			}
			name := fmt.Sprintf("%-*s", maxNameLen, c.Name)
			nameStyled := m.theme.AccentText.Render(name)
			desc := m.theme.NormalText.Render("  " + c.Description)
			sb.WriteString(prefix)
			sb.WriteString(nameStyled)
			sb.WriteString(desc)
			if i < len(m.matches)-1 {
				sb.WriteString("\n")
			}
		}
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Palette.Primary).
		Background(m.theme.Palette.BgBase).
		Padding(0, 1).
		Width(boxWidth).
		Render(sb.String())

	hint := m.theme.NormalText.Render("↑↓/Tab navigate • Enter run • Esc close")
	full := box + "\n" + hint

	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, full)
	}
	return full
}

// viewAttach renders the attach-argument prompt box (QUM-860). The user types a
// free-form `<path...> "prompt"` line; Enter dispatches AttachMsg.
func (m PaletteModel) viewAttach(boxWidth int) string {
	var sb strings.Builder
	sb.WriteString(m.theme.AccentText.Render("> /attach "))
	sb.WriteString(m.attachArgs)
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", boxWidth-4))
	sb.WriteString("\n")
	sb.WriteString(m.theme.NormalText.Render(`  <path...> "prompt" — local image(s): jpeg/png/gif/webp, ≤10 MB each`))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Palette.Primary).
		Background(m.theme.Palette.BgBase).
		Padding(0, 1).
		Width(boxWidth).
		Render(sb.String())
	hint := m.theme.NormalText.Render("Enter attach • ⌫ back • Esc close")
	full := box + "\n" + hint
	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, full)
	}
	return full
}
