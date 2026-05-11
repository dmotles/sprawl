package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// questionInputMode selects between the option-cursor mode and the free-form
// custom-text input mode.
type questionInputMode int

const (
	qModeSelect questionInputMode = iota
	qModeText
)

// QuestionModel renders the "ask the user a question" modal (QUM-527 slice 2c).
// It is a Bubble Tea sub-model owned by AppModel; AppModel routes key events
// to it while showQuestion is true and listens for QuestionAnsweredMsg /
// DismissQuestionMsg to drive the supervisor question queue.
//
// Per-question editing state (cursor, multi-pick set, mode, custom-text
// buffer) is held in parallel slices indexed by qIdx so that drafts survive
// inter-question navigation (QUM-538). The scalar `cursor` field mirrors
// `cursors[qIdx]` for the benefit of legacy tests that read it directly.
type QuestionModel struct {
	theme         *Theme
	width, height int
	visible       bool
	req           *supervisor.PendingQuestion
	qIdx          int
	answers       []supervisor.QuestionAnswer
	cursors       []int
	multiPicked   []map[int]struct{}
	modes         []questionInputMode
	customInputs  []textinput.Model
	// cursor mirrors cursors[qIdx]; kept for legacy test access.
	cursor int
}

// NewQuestionModel constructs an empty, non-visible QuestionModel bound to the
// supplied theme.
func NewQuestionModel(theme *Theme) QuestionModel {
	return QuestionModel{theme: theme}
}

// SetSize updates the centering dimensions.
func (m *QuestionModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

// HasPending reports whether a request is currently installed (regardless of
// whether the modal is visible).
func (m QuestionModel) HasPending() bool {
	return m.req != nil
}

// Visible reports whether the modal should be drawn.
func (m QuestionModel) Visible() bool {
	return m.visible
}

// activeRequestID returns the RequestID of the installed request, or "" if
// none. Used by AppModel to gate CancelQuestionMsg.
func (m QuestionModel) activeRequestID() string {
	if m.req == nil {
		return ""
	}
	return m.req.Req.RequestID
}

// newCustomInput constructs a fresh textinput with the standard placeholder.
func newCustomInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "type a custom answer..."
	return ti
}

// Install replaces the modal's active request with pq, seeding a fresh answer
// slice (one entry per question, with QuestionID pre-populated) and resetting
// all per-question editing state. Visibility is unchanged — call Show() to
// flip it on. Drafts from any prior request are discarded.
func (m QuestionModel) Install(pq *supervisor.PendingQuestion) QuestionModel {
	m.req = pq
	if pq != nil {
		n := len(pq.Req.Questions)
		m.answers = make([]supervisor.QuestionAnswer, n)
		m.cursors = make([]int, n)
		m.multiPicked = make([]map[int]struct{}, n)
		m.modes = make([]questionInputMode, n)
		m.customInputs = make([]textinput.Model, n)
		for i := range pq.Req.Questions {
			m.answers[i].QuestionID = pq.Req.Questions[i].ID
			m.multiPicked[i] = make(map[int]struct{})
			m.modes[i] = qModeSelect
			m.customInputs[i] = newCustomInput()
		}
	} else {
		m.answers = nil
		m.cursors = nil
		m.multiPicked = nil
		m.modes = nil
		m.customInputs = nil
	}
	m.qIdx = 0
	m.cursor = 0
	return m
}

// Show flips the modal to visible.
func (m QuestionModel) Show() QuestionModel {
	m.visible = true
	return m
}

// Hide flips the modal to non-visible. Drafts (selected options, multi-pick
// set, custom-text buffer, qIdx cursor) are preserved so Show() resumes.
func (m QuestionModel) Hide() QuestionModel {
	m.visible = false
	return m
}

// Reset clears all modal state: pending request, drafts, visibility, cursor.
// AppModel calls this after a successful submit so the next QuestionsAvailable
// can install a fresh request.
func (m QuestionModel) Reset() QuestionModel {
	m.req = nil
	m.answers = nil
	m.cursors = nil
	m.multiPicked = nil
	m.modes = nil
	m.customInputs = nil
	m.visible = false
	m.qIdx = 0
	m.cursor = 0
	return m
}

// syncCursor copies cursors[qIdx] into the mirror field.
func (m *QuestionModel) syncCursor() {
	if m.qIdx >= 0 && m.qIdx < len(m.cursors) {
		m.cursor = m.cursors[m.qIdx]
	} else {
		m.cursor = 0
	}
}

// isAnswered reports whether question i has been answered, declined, or has
// custom text set.
func (m QuestionModel) isAnswered(i int) bool {
	if i < 0 || i >= len(m.answers) {
		return false
	}
	a := m.answers[i]
	return a.Declined || len(a.Selected) > 0 || a.CustomText != ""
}

// Update handles key events while the modal is active. Returns the updated
// model and an optional cmd (QuestionAnsweredMsg / DismissQuestionMsg).
func (m QuestionModel) Update(msg tea.Msg) (QuestionModel, tea.Cmd) {
	if m.req == nil {
		return m, nil
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	cur := m.req.Req.Questions[m.qIdx]
	mode := m.modes[m.qIdx]

	// Text mode: handle Enter (commit text), Esc (cancel back to select), and
	// route everything else (including Left/Right) into the textinput so the
	// caret moves rather than the qIdx.
	if mode == qModeText {
		switch key.Code {
		case tea.KeyEnter:
			m.answers[m.qIdx].CustomText = m.customInputs[m.qIdx].Value()
			m.answers[m.qIdx].Selected = nil
			m.answers[m.qIdx].Declined = false
			return m.advance()
		case tea.KeyEscape:
			m.modes[m.qIdx] = qModeSelect
			m.customInputs[m.qIdx] = newCustomInput()
			return m, nil
		}
		var cmd tea.Cmd
		m.customInputs[m.qIdx], cmd = m.customInputs[m.qIdx].Update(msg)
		return m, cmd
	}

	// Select mode.
	switch key.Code {
	case tea.KeyLeft:
		if m.qIdx > 0 {
			m.qIdx--
			m.syncCursor()
		}
		return m, nil
	case tea.KeyRight:
		if m.qIdx < len(m.req.Req.Questions)-1 {
			m.qIdx++
			m.syncCursor()
		}
		return m, nil
	case tea.KeyUp:
		if m.cursors[m.qIdx] > 0 {
			m.cursors[m.qIdx]--
		}
		m.syncCursor()
		return m, nil
	case tea.KeyDown:
		if m.cursors[m.qIdx] < len(cur.Options)-1 {
			m.cursors[m.qIdx]++
		}
		m.syncCursor()
		return m, nil
	case 'j':
		if m.cursors[m.qIdx] < len(cur.Options)-1 {
			m.cursors[m.qIdx]++
		}
		m.syncCursor()
		return m, nil
	case 'k':
		if m.cursors[m.qIdx] > 0 {
			m.cursors[m.qIdx]--
		}
		m.syncCursor()
		return m, nil
	case tea.KeySpace:
		if cur.MultiSelect {
			if m.multiPicked[m.qIdx] == nil {
				m.multiPicked[m.qIdx] = make(map[int]struct{})
			}
			c := m.cursors[m.qIdx]
			if _, ok := m.multiPicked[m.qIdx][c]; ok {
				delete(m.multiPicked[m.qIdx], c)
			} else {
				m.multiPicked[m.qIdx][c] = struct{}{}
			}
		}
		return m, nil
	case tea.KeyEnter:
		if cur.MultiSelect {
			var sel []string
			for i := range cur.Options {
				if _, ok := m.multiPicked[m.qIdx][i]; ok {
					sel = append(sel, cur.Options[i].Label)
				}
			}
			m.answers[m.qIdx].Selected = sel
		} else if len(cur.Options) > 0 {
			m.answers[m.qIdx].Selected = []string{cur.Options[m.cursors[m.qIdx]].Label}
		}
		m.answers[m.qIdx].CustomText = ""
		m.answers[m.qIdx].Declined = false
		return m.advance()
	case 'o':
		m.modes[m.qIdx] = qModeText
		ti := newCustomInput()
		_ = ti.Focus()
		m.customInputs[m.qIdx] = ti
		return m, nil
	case 'd':
		m.answers[m.qIdx].Declined = true
		m.answers[m.qIdx].Selected = nil
		m.answers[m.qIdx].CustomText = ""
		return m.advance()
	case 'D':
		for i := m.qIdx; i < len(m.answers); i++ {
			m.answers[i].Declined = true
			m.answers[i].Selected = nil
			m.answers[i].CustomText = ""
		}
		return m.submit()
	case tea.KeyEscape:
		return m, func() tea.Msg { return DismissQuestionMsg{} }
	}

	// Ctrl-Q from inside the modal also dismisses.
	if key.Mod&tea.ModCtrl != 0 && (key.Code == 'q' || key.Code == 'Q') {
		return m, func() tea.Msg { return DismissQuestionMsg{} }
	}
	return m, nil
}

// advance commits the current question (already updated by the caller) and
// hunts for the next unanswered question. Search starts at qIdx+1 and wraps
// to 0, skipping the current qIdx. If no unanswered question is found, it
// submits. Per-question editing state is persisted in the slices so any
// existing draft on the target question is preserved (QUM-538).
func (m QuestionModel) advance() (QuestionModel, tea.Cmd) {
	n := len(m.req.Req.Questions)
	if n == 0 {
		return m.submit()
	}
	for step := 1; step < n; step++ {
		next := (m.qIdx + step) % n
		if !m.isAnswered(next) {
			m.qIdx = next
			m.syncCursor()
			return m, nil
		}
	}
	return m.submit()
}

// submit emits a QuestionAnsweredMsg cmd. Outcome is OutcomeDeclined iff every
// answer is marked Declined; otherwise OutcomeAnswered.
func (m QuestionModel) submit() (QuestionModel, tea.Cmd) {
	outcome := supervisor.OutcomeAnswered
	allDeclined := true
	for _, a := range m.answers {
		if !a.Declined {
			allDeclined = false
			break
		}
	}
	if allDeclined && len(m.answers) > 0 {
		outcome = supervisor.OutcomeDeclined
	}
	resp := supervisor.QuestionResponse{
		RequestID: m.req.Req.RequestID,
		Outcome:   outcome,
		Answers:   append([]supervisor.QuestionAnswer(nil), m.answers...),
	}
	id := m.req.Req.RequestID
	return m, func() tea.Msg {
		return QuestionAnsweredMsg{RequestID: id, Response: resp}
	}
}

// renderTabStrip returns the per-question tab strip rendered above the modal
// body. Each tab shows the 1-based question number and an answered marker
// (`[*]` for answered, `[ ]` for unanswered). The current tab is bolded with
// the theme accent foreground; other tabs use the muted style.
func (m QuestionModel) renderTabStrip() string {
	if m.req == nil || len(m.req.Req.Questions) == 0 {
		return ""
	}
	accent := "212"
	if m.theme != nil {
		accent = m.theme.AccentColor
	}
	cur := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(accent))
	other := lipgloss.NewStyle().Faint(true)
	segs := make([]string, 0, len(m.req.Req.Questions))
	for i := range m.req.Req.Questions {
		marker := "[ ]"
		if m.isAnswered(i) {
			marker = "[*]"
		}
		seg := fmt.Sprintf(" %d%s ", i+1, marker)
		if i == m.qIdx {
			seg = cur.Render(seg)
		} else {
			seg = other.Render(seg)
		}
		segs = append(segs, seg)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, segs...)
}

// View renders the modal centered in the available area.
func (m QuestionModel) View() string {
	if m.req == nil {
		return ""
	}
	q := m.req.Req.Questions[m.qIdx]
	mode := m.modes[m.qIdx]
	curIdx := m.cursors[m.qIdx]

	dialogWidth := 64
	if m.width > 0 && m.width < dialogWidth+4 {
		dialogWidth = m.width - 4
	}
	if dialogWidth < 24 {
		dialogWidth = 24
	}

	var b strings.Builder
	from := m.req.Req.From
	if from == "" {
		from = "an agent"
	}
	fmt.Fprintf(&b, "%s is asking\n", from)
	b.WriteString(m.renderTabStrip())
	b.WriteString("\n\n")
	if q.Header != "" {
		fmt.Fprintf(&b, "%s\n", q.Header)
	}
	if q.Prompt != "" {
		fmt.Fprintf(&b, "%s\n\n", q.Prompt)
	} else {
		b.WriteString("\n")
	}

	for i, opt := range q.Options {
		marker := "  "
		if i == curIdx && mode == qModeSelect {
			marker = "> "
		}
		check := "  "
		if q.MultiSelect {
			if _, ok := m.multiPicked[m.qIdx][i]; ok {
				check = "[x] "
			} else {
				check = "[ ] "
			}
		}
		fmt.Fprintf(&b, "%s%s%s\n", marker, check, opt.Label)
	}

	if mode == qModeText {
		b.WriteString("\nCustom answer: ")
		b.WriteString(m.customInputs[m.qIdx].View())
		b.WriteString("\n[Enter] submit  [Esc] back to options\n")
	} else {
		b.WriteString("\n[←/→] navigate  [Enter] confirm  [Space] toggle  [o] custom  [d] decline  [D] decline all  [Esc] dismiss\n")
	}

	accent := "212"
	if m.theme != nil {
		accent = m.theme.AccentColor
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(accent)).
		Padding(1, 2).
		Width(dialogWidth).
		Render(b.String())

	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}
	return box
}

// Compile-time guard: keep QuestionConsumer satisfying the supervisor
// interface even when no tests reference it.
var _ supervisor.QuestionConsumer = (*QuestionConsumer)(nil)

// QuestionConsumer is the supervisor.QuestionConsumer implementation that
// bridges the in-process question queue into the bubbletea program. send is
// the program's Send-equivalent function (captured at onStart time).
type QuestionConsumer struct {
	send func(tea.Msg)
}

// NewQuestionConsumer wraps send into a supervisor.QuestionConsumer.
func NewQuestionConsumer(send func(tea.Msg)) *QuestionConsumer {
	return &QuestionConsumer{send: send}
}

// Name implements supervisor.QuestionConsumer. Always "tui".
func (c *QuestionConsumer) Name() string { return "tui" }

// OnEnqueue implements supervisor.QuestionConsumer. The forwarder goroutine in
// cmd/enter.go (subscribed to QuestionsChanged) is responsible for filling
// Depth via PeekQuestions; the OnEnqueue dispatch carries only Head so the
// modal can install promptly without an extra hop.
func (c *QuestionConsumer) OnEnqueue(pq *supervisor.PendingQuestion) {
	if c == nil || c.send == nil {
		return
	}
	c.send(QuestionsAvailableMsg{Head: pq})
}

// OnCancel implements supervisor.QuestionConsumer.
func (c *QuestionConsumer) OnCancel(requestID, reason string) {
	if c == nil || c.send == nil {
		return
	}
	c.send(CancelQuestionMsg{RequestID: requestID, Reason: reason})
}
