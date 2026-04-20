package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/tui/commands"
)

// PaletteModel renders a floating centered command palette overlay. It is
// shown/hidden by the app in response to OpenPaletteMsg/ClosePaletteMsg.
// While visible, all key events route here (see app.go) — the palette owns
// the filter input and navigation.
type PaletteModel struct {
	theme   *Theme
	width   int
	height  int
	visible bool

	filter  string
	cursor  int
	matches []commands.Command
}

// NewPaletteModel constructs a hidden palette model.
func NewPaletteModel(theme *Theme) PaletteModel {
	return PaletteModel{theme: theme}
}

// Show makes the palette visible, resets filter and cursor, and populates
// matches from the full registry.
func (m *PaletteModel) Show() {
	m.visible = true
	m.filter = ""
	m.cursor = 0
	m.refreshMatches()
}

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
	m.matches = commands.Filter(m.filter)
	if m.cursor >= len(m.matches) {
		m.cursor = 0
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// Update handles key events while the palette is visible. It emits:
//   - ClosePaletteMsg on Esc
//   - navigation on Up/Down/Tab/Shift+Tab (no cmd)
//   - ClosePaletteMsg + (PaletteQuitMsg | ToggleHelpMsg | InjectPromptMsg) on Enter
//   - filter edits on printable chars and Backspace (no cmd)
func (m PaletteModel) Update(msg tea.KeyPressMsg) (PaletteModel, tea.Cmd) {
	if !m.visible {
		return m, nil
	}

	switch msg.Code {
	case tea.KeyEscape:
		return m, sendMsgCmd(ClosePaletteMsg{})
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
		if len(m.filter) > 0 {
			r := []rune(m.filter)
			m.filter = string(r[:len(r)-1])
			m.refreshMatches()
		}
		return m, nil
	}

	// Printable ASCII letter/digit/dash/underscore appends to filter.
	if msg.Code > 0 && msg.Code < 0x10FFFF {
		r := msg.Code
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			m.filter += string(r)
			m.refreshMatches()
		}
	}
	return m, nil
}

func (m *PaletteModel) moveCursor(delta int) {
	n := len(m.matches)
	if n == 0 {
		m.cursor = 0
		return
	}
	m.cursor = (m.cursor + delta + n) % n
}

func (m PaletteModel) dispatchSelected() (PaletteModel, tea.Cmd) {
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
		}
	case commands.KindPromptInjection:
		tmpl := cmd.PromptTemplate
		action = sendMsgCmd(InjectPromptMsg{Template: tmpl})
	}

	closeCmd := sendMsgCmd(ClosePaletteMsg{})
	if action == nil {
		return m, closeCmd
	}
	return m, tea.Batch(closeCmd, action)
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

	var sb strings.Builder
	// Filter row.
	sb.WriteString(m.theme.AccentText.Render("> /"))
	sb.WriteString(m.filter)
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", boxWidth-4))
	sb.WriteString("\n")

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
		BorderForeground(lipgloss.Color(m.theme.AccentColor)).
		Background(lipgloss.Color(backgroundColor)).
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
