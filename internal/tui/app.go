package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// Panel identifies which panel is active.
type Panel int

const (
	PanelTree Panel = iota
	PanelViewport
	PanelInput
	panelCount // sentinel for wrapping
)

// AgentBuffer stores the viewport state for a specific agent.
type AgentBuffer struct {
	Messages   []MessageEntry
	AutoScroll bool
}

// AppModel is the root Bubble Tea model composing all panels.
type AppModel struct {
	tree      TreeModel
	viewport  ViewportModel
	input     InputModel
	statusBar StatusBarModel
	confirm   ConfirmModel

	help     HelpModel
	showHelp bool

	bridge    *Bridge
	turnState TurnState

	supervisor    supervisor.Supervisor
	sprawlRoot    string
	observedAgent string
	rootAgent     string
	childNodes    []TreeNode
	agentBuffers  map[string]*AgentBuffer

	activePanel Panel
	showConfirm bool
	theme       Theme
	width       int
	height      int
	ready       bool
	tooSmall    bool

	showError   bool
	errorDialog ErrorDialogModel
	restartFunc func() (*Bridge, error)
}

// NewAppModel constructs the root model with all sub-models.
// bridge may be nil for static placeholder mode.
// sup and sprawlRoot are optional; when provided, the tree polls agent status.
// restartFunc is called when the user requests a session restart after a crash.
func NewAppModel(accentColor, repoName, version string, bridge *Bridge, sup supervisor.Supervisor, sprawlRoot string, restartFunc func() (*Bridge, error)) AppModel {
	theme := NewTheme(accentColor)
	startPanel := PanelTree
	if bridge != nil {
		startPanel = PanelInput
	}
	rootAgent := "weave"
	app := AppModel{
		tree:          NewTreeModel(&theme),
		viewport:      NewViewportModel(&theme),
		input:         NewInputModel(&theme),
		statusBar:     NewStatusBarModel(&theme, repoName, version, 0),
		help:          NewHelpModel(&theme),
		confirm:       NewConfirmModel(&theme),
		bridge:        bridge,
		turnState:     TurnIdle,
		supervisor:    sup,
		sprawlRoot:    sprawlRoot,
		observedAgent: rootAgent,
		rootAgent:     rootAgent,
		agentBuffers:  make(map[string]*AgentBuffer),
		activePanel:   startPanel,
		theme:         theme,
		restartFunc:   restartFunc,
	}
	app.updateFocus()
	app.rebuildTree()
	return app
}

// Init returns the bridge initialize command if a bridge is present,
// otherwise nil (the app waits for WindowSizeMsg).
func (m AppModel) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.bridge != nil {
		cmds = append(cmds, m.bridge.Initialize())
	}
	if m.supervisor != nil {
		cmds = append(cmds, tickAgentsCmd(m.supervisor, m.sprawlRoot))
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// Update handles messages: window resize, global keybinds, bridge messages, and panel delegation.
func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.tooSmall = IsTooSmall(m.width, m.height)
		if !m.tooSmall {
			m.resizePanels()
		}
		return m, nil

	case tea.KeyPressMsg:
		// Ctrl+C: show confirmation dialog (or ignore if already showing).
		if msg.Mod&tea.ModCtrl != 0 && msg.Code == 'c' {
			if m.showConfirm {
				return m, nil
			}
			m.showConfirm = true
			m.confirm.Show()
			m.confirm.SetSize(m.width, m.height)
			return m, nil
		}

		// When confirm dialog is visible, route all keys to it.
		if m.showConfirm {
			var cmd tea.Cmd
			m.confirm, cmd = m.confirm.Update(msg)
			return m, cmd
		}

		// Toggle help on ? or F1.
		if msg.Code == '?' || msg.Code == tea.KeyF1 {
			m.showHelp = !m.showHelp
			return m, nil
		}

		// When help is shown, only Esc dismisses; swallow everything else.
		if m.showHelp {
			if msg.Code == tea.KeyEscape {
				m.showHelp = false
			}
			return m, nil
		}

		// When error dialog is shown, delegate all keys to it.
		if m.showError {
			cmd := m.errorDialog.Update(msg)
			return m, cmd
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

	case SessionInitializedMsg:
		m.viewport.AppendStatus("Session ready")
		return m, nil

	case SubmitMsg:
		if msg.Text == "" || m.bridge == nil || m.turnState != TurnIdle {
			return m, nil
		}
		m.viewport.AppendUserMessage(msg.Text)
		m.setTurnState(TurnThinking)
		m.input.SetDisabled(true)
		return m, m.bridge.SendMessage(msg.Text)

	case UserMessageSentMsg:
		m.setTurnState(TurnStreaming)
		if m.bridge != nil {
			return m, m.bridge.WaitForEvent()
		}
		return m, nil

	case AssistantTextMsg:
		m.setTurnState(TurnStreaming)
		m.viewport.AppendAssistantChunk(msg.Text)
		if m.bridge != nil {
			return m, m.bridge.WaitForEvent()
		}
		return m, nil

	case ToolCallMsg:
		m.viewport.AppendToolCall(msg.ToolName, msg.Approved, msg.Input)
		if m.bridge != nil {
			return m, m.bridge.WaitForEvent()
		}
		return m, nil

	case SessionResultMsg:
		// Display result text only if no assistant text was already streamed.
		// When Claude returns text in the assistant message, it also appears
		// in result.Result — avoid duplicating it.
		if !msg.IsError && strings.TrimSpace(msg.Result) != "" && !m.viewport.HasPendingAssistant() {
			m.viewport.AppendAssistantChunk(strings.TrimSpace(msg.Result))
		}
		m.viewport.FinalizeAssistantMessage()
		if msg.IsError {
			m.viewport.AppendError(fmt.Sprintf("Error: %s", msg.Result))
		} else {
			m.viewport.AppendStatus(fmt.Sprintf("Completed in %dms, cost $%.4f", msg.DurationMs, msg.TotalCostUsd))
			m.statusBar.SetTurnCost(msg.TotalCostUsd)
		}
		m.setTurnState(TurnIdle)
		m.input.SetDisabled(false)
		return m, nil

	case SessionErrorMsg:
		if m.turnState == TurnThinking || m.turnState == TurnStreaming {
			m.errorDialog = NewErrorDialog(&m.theme, msg.Err)
			m.errorDialog.SetSize(m.width, m.height)
			m.showError = true
			m.setTurnState(TurnIdle)
			m.input.SetDisabled(true)
			return m, nil
		}
		m.viewport.AppendError(msg.Err.Error())
		m.setTurnState(TurnIdle)
		m.input.SetDisabled(false)
		return m, nil

	case RestartSessionMsg:
		m.showError = false
		if m.bridge != nil {
			_ = m.bridge.Close()
		}
		if m.restartFunc != nil {
			newBridge, err := m.restartFunc()
			if err != nil {
				m.errorDialog = NewErrorDialog(&m.theme, err)
				m.errorDialog.SetSize(m.width, m.height)
				m.showError = true
				return m, nil
			}
			m.bridge = newBridge
			m.viewport.AppendStatus("Session restarting...")
			m.setTurnState(TurnIdle)
			m.input.SetDisabled(false)
			return m, m.bridge.Initialize()
		}
		return m, tea.Quit

	case TurnStateMsg:
		m.setTurnState(msg.State)
		if msg.State == TurnIdle {
			m.input.SetDisabled(false)
		}
		return m, nil

	case AgentTreeMsg:
		m.childNodes = msg.Nodes
		m.rebuildTree()
		m.statusBar.SetAgentCount(len(msg.Nodes) + 1) // +1 for weave root
		if m.supervisor != nil {
			return m, scheduleAgentTick(m.supervisor, m.sprawlRoot)
		}
		return m, nil

	case ConfirmResultMsg:
		m.showConfirm = false
		m.confirm.Hide()
		if msg.Confirmed {
			return m, tea.Quit
		}
		return m, nil

	case SignalMsg:
		if !m.showConfirm {
			m.showConfirm = true
			m.confirm.Show()
			m.confirm.SetSize(m.width, m.height)
		}
		return m, nil

	case AgentSelectedMsg:
		// Save current viewport to buffer.
		m.agentBuffers[m.observedAgent] = &AgentBuffer{
			Messages:   m.viewport.GetMessages(),
			AutoScroll: m.viewport.IsAutoScroll(),
		}

		m.observedAgent = msg.Name

		// Load from buffer if exists, else show empty with status.
		if buf, ok := m.agentBuffers[msg.Name]; ok {
			m.viewport.SetMessages(buf.Messages)
			m.viewport.SetAutoScroll(buf.AutoScroll)
		} else {
			m.viewport.SetMessages(nil)
			m.viewport.AppendStatus(fmt.Sprintf("Observing %s...", msg.Name))
		}

		// Disable input for non-root agents.
		if msg.Name != m.rootAgent {
			m.input.SetDisabled(true)
		} else {
			m.input.SetDisabled(false)
		}
		return m, nil
	}

	return m, nil
}

// View renders the full TUI layout.
func (m AppModel) View() tea.View {
	if !m.ready {
		return tea.NewView("  Initializing...")
	}

	if m.tooSmall {
		msg := fmt.Sprintf("Terminal too small (minimum %dx%d)", MinTermWidth, MinTermHeight)
		v := tea.NewView(msg)
		v.AltScreen = true
		return v
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

	if m.showHelp {
		content = m.help.View()
	}

	if m.showConfirm {
		content = m.confirm.View()
	}

	if m.showError {
		content = m.errorDialog.View()
	}

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m *AppModel) setTurnState(state TurnState) {
	m.turnState = state
	m.statusBar.SetTurnState(state)
	m.rebuildTree()
}

func (m *AppModel) rebuildTree() {
	nodes := PrependWeaveRoot(m.childNodes, m.turnState.String())
	m.tree.SetNodes(nodes)
}

func (m *AppModel) resizePanels() {
	layout := ComputeLayout(m.width, m.height)

	// Account for border (2 chars each side).
	m.tree.SetSize(layout.TreeWidth-2, layout.TreeHeight-2)
	m.viewport.SetSize(layout.ViewportWidth-2, layout.ViewportHeight-2)
	m.input.SetWidth(layout.InputWidth - 2)
	m.statusBar.SetWidth(layout.StatusWidth)
	m.help.SetSize(m.width, m.height)
	m.confirm.SetSize(m.width, m.height)
	m.errorDialog.SetSize(m.width, m.height)
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

func tickAgentsCmd(sup supervisor.Supervisor, sprawlRoot string) tea.Cmd {
	return func() tea.Msg {
		agents, err := sup.Status(context.Background())
		if err != nil {
			return AgentTreeMsg{}
		}
		unread := make(map[string]int)
		for _, a := range agents {
			msgs, _ := messages.List(sprawlRoot, a.Name, "unread")
			unread[a.Name] = len(msgs)
		}
		return AgentTreeMsg{Nodes: buildTreeNodes(agents, unread)}
	}
}

func scheduleAgentTick(sup supervisor.Supervisor, sprawlRoot string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(2 * time.Second)
		return tickAgentsCmd(sup, sprawlRoot)()
	}
}
