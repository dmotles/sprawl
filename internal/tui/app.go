package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Panel identifies which panel is active.
type Panel int

const (
	PanelTree Panel = iota
	PanelViewport
	PanelInput
	panelCount // sentinel for wrapping
)

// AppModel is the root Bubble Tea model composing all panels.
type AppModel struct {
	tree      TreeModel
	viewport  ViewportModel
	input     InputModel
	statusBar StatusBarModel

	activePanel Panel
	theme       Theme
	width       int
	height      int
	ready       bool
}

// NewAppModel constructs the root model with all sub-models.
func NewAppModel(accentColor, repoName, version string) AppModel {
	theme := NewTheme(accentColor)
	return AppModel{
		tree:        NewTreeModel(&theme),
		viewport:    NewViewportModel(&theme),
		input:       NewInputModel(&theme),
		statusBar:   NewStatusBarModel(&theme, repoName, version, 0),
		activePanel: PanelTree,
		theme:       theme,
	}
}

// Init returns nil; the app waits for WindowSizeMsg.
func (m AppModel) Init() tea.Cmd {
	return nil
}

// Update handles messages: window resize, global keybinds, and panel delegation.
func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.resizePanels()
		return m, nil

	case tea.KeyPressMsg:
		// Global keybinds.
		if msg.Mod&tea.ModCtrl != 0 && msg.Code == 'c' {
			return m, tea.Quit
		}

		if msg.Code == tea.KeyTab {
			if msg.Mod&tea.ModShift != 0 {
				m.activePanel = (m.activePanel - 1 + panelCount) % panelCount
			} else {
				m.activePanel = (m.activePanel + 1) % panelCount
			}
			m.updateFocus()
			return m, nil
		}

		// Delegate to active panel.
		return m.delegateKey(msg)
	}

	return m, nil
}

// View renders the full TUI layout.
func (m AppModel) View() tea.View {
	if !m.ready {
		return tea.NewView("  Initializing...")
	}

	layout := ComputeLayout(m.width, m.height)

	// Render tree panel with border.
	treeBorder := m.borderStyle(PanelTree).
		Width(layout.TreeWidth - 2).
		Height(layout.TreeHeight - 2)
	treeView := treeBorder.Render(m.tree.View())

	// Render viewport panel with border.
	vpBorder := m.borderStyle(PanelViewport).
		Width(layout.ViewportWidth - 2).
		Height(layout.ViewportHeight - 2)
	vpView := vpBorder.Render(m.viewport.View())

	// Combine tree and viewport horizontally.
	mainRow := lipgloss.JoinHorizontal(lipgloss.Top, treeView, vpView)

	// Render input with border.
	inputBorder := m.borderStyle(PanelInput).
		Width(layout.InputWidth - 2).
		Height(layout.InputHeight - 2)
	inputView := inputBorder.Render(m.input.View())

	// Status bar.
	statusView := m.statusBar.View()

	// Stack vertically.
	content := lipgloss.JoinVertical(lipgloss.Left, mainRow, inputView, statusView)

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m *AppModel) resizePanels() {
	layout := ComputeLayout(m.width, m.height)

	// Account for border (2 chars each side).
	m.tree.SetSize(layout.TreeWidth-2, layout.TreeHeight-2)
	m.viewport.SetSize(layout.ViewportWidth-2, layout.ViewportHeight-2)
	m.input.SetWidth(layout.InputWidth - 2)
	m.statusBar.SetWidth(layout.StatusWidth)
}

func (m *AppModel) updateFocus() {
	if m.activePanel == PanelInput {
		m.input.Focus()
	} else {
		m.input.Blur()
	}
}

func (m AppModel) borderStyle(panel Panel) lipgloss.Style {
	if panel == m.activePanel {
		return m.theme.ActiveBorder
	}
	return m.theme.InactiveBorder
}

func (m AppModel) delegateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.activePanel {
	case PanelTree:
		var cmd tea.Cmd
		m.tree, cmd = m.tree.Update(msg)
		return m, cmd
	case PanelViewport:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	case PanelInput:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}
