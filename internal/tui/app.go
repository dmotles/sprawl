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

	bridge    *Bridge
	turnState TurnState

	supervisor    supervisor.Supervisor
	sprawlRoot    string
	observedAgent string
	rootAgent     string
	agentBuffers  map[string]*AgentBuffer

	activePanel Panel
	theme       Theme
	width       int
	height      int
	ready       bool
}

// NewAppModel constructs the root model with all sub-models.
// bridge may be nil for static placeholder mode.
// sup and sprawlRoot are optional; when provided, the tree polls agent status.
func NewAppModel(accentColor, repoName, version string, bridge *Bridge, sup supervisor.Supervisor, sprawlRoot string) AppModel {
	theme := NewTheme(accentColor)
	startPanel := PanelTree
	if bridge != nil {
		startPanel = PanelInput
	}
	rootAgent := "enter"
	app := AppModel{
		tree:          NewTreeModel(&theme),
		viewport:      NewViewportModel(&theme),
		input:         NewInputModel(&theme),
		statusBar:     NewStatusBarModel(&theme, repoName, version, 0),
		bridge:        bridge,
		turnState:     TurnIdle,
		supervisor:    sup,
		sprawlRoot:    sprawlRoot,
		observedAgent: rootAgent,
		rootAgent:     rootAgent,
		agentBuffers:  make(map[string]*AgentBuffer),
		activePanel:   startPanel,
		theme:         theme,
	}
	app.updateFocus()
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
		m.viewport.AppendError(msg.Err.Error())
		m.setTurnState(TurnIdle)
		m.input.SetDisabled(false)
		return m, nil

	case TurnStateMsg:
		m.setTurnState(msg.State)
		if msg.State == TurnIdle {
			m.input.SetDisabled(false)
		}
		return m, nil

	case AgentTreeMsg:
		m.tree.SetNodes(msg.Nodes)
		m.statusBar.SetAgentCount(len(msg.Nodes))
		// Update rootAgent to the tree root (first depth-0 node) if available.
		for _, n := range msg.Nodes {
			if n.Depth == 0 {
				// If the observedAgent was the old rootAgent, migrate it to the new root.
				if m.observedAgent == m.rootAgent && m.rootAgent != n.Name {
					// Transfer current viewport buffer to the new root agent name.
					m.agentBuffers[n.Name] = &AgentBuffer{
						Messages:   m.viewport.GetMessages(),
						AutoScroll: m.viewport.IsAutoScroll(),
					}
					m.observedAgent = n.Name
				}
				m.rootAgent = n.Name
				break
			}
		}
		if m.supervisor != nil {
			return m, scheduleAgentTick(m.supervisor, m.sprawlRoot)
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

func (m *AppModel) setTurnState(state TurnState) {
	m.turnState = state
	m.statusBar.SetTurnState(state)
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
