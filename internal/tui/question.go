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
type QuestionModel struct {
	theme         *Theme
	width, height int
	visible       bool
	req           *supervisor.PendingQuestion
	qIdx          int
	answers       []supervisor.QuestionAnswer
	cursor        int
	multiPicked   map[int]struct{}
	mode          questionInputMode
	customInput   textinput.Model
}

// NewQuestionModel constructs an empty, non-visible QuestionModel bound to the
// supplied theme.
func NewQuestionModel(theme *Theme) QuestionModel {
	ti := textinput.New()
	ti.Placeholder = "type a custom answer..."
	return QuestionModel{
		theme:       theme,
		customInput: ti,
	}
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

// Install replaces the modal's active request with pq, seeding a fresh answer
// slice (one entry per question, with QuestionID pre-populated) and resetting
// all per-question editing state. Visibility is unchanged — call Show() to
// flip it on. Drafts from any prior request are discarded.
func (m QuestionModel) Install(pq *supervisor.PendingQuestion) QuestionModel {
	m.req = pq
	if pq != nil {
		m.answers = make([]supervisor.QuestionAnswer, len(pq.Req.Questions))
		for i := range pq.Req.Questions {
			m.answers[i].QuestionID = pq.Req.Questions[i].ID
		}
	} else {
		m.answers = nil
	}
	m.qIdx = 0
	m.cursor = 0
	m.multiPicked = make(map[int]struct{})
	m.mode = qModeSelect
	ti := textinput.New()
	ti.Placeholder = "type a custom answer..."
	m.customInput = ti
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
	m.visible = false
	m.qIdx = 0
	m.cursor = 0
	m.multiPicked = make(map[int]struct{})
	m.mode = qModeSelect
	ti := textinput.New()
	ti.Placeholder = "type a custom answer..."
	m.customInput = ti
	return m
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

	// Text mode: handle Enter (commit text), Esc (cancel back to select), and
	// route everything else into the textinput.
	if m.mode == qModeText {
		switch key.Code {
		case tea.KeyEnter:
			m.answers[m.qIdx].CustomText = m.customInput.Value()
			m.answers[m.qIdx].Selected = nil
			m.answers[m.qIdx].Declined = false
			return m.advance()
		case tea.KeyEscape:
			m.mode = qModeSelect
			ti := textinput.New()
			ti.Placeholder = "type a custom answer..."
			m.customInput = ti
			return m, nil
		}
		var cmd tea.Cmd
		m.customInput, cmd = m.customInput.Update(msg)
		return m, cmd
	}

	// Select mode.
	switch key.Code {
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.cursor < len(cur.Options)-1 {
			m.cursor++
		}
		return m, nil
	case 'j':
		if m.cursor < len(cur.Options)-1 {
			m.cursor++
		}
		return m, nil
	case 'k':
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case tea.KeySpace:
		if cur.MultiSelect {
			if m.multiPicked == nil {
				m.multiPicked = make(map[int]struct{})
			}
			if _, ok := m.multiPicked[m.cursor]; ok {
				delete(m.multiPicked, m.cursor)
			} else {
				m.multiPicked[m.cursor] = struct{}{}
			}
		}
		return m, nil
	case tea.KeyEnter:
		if cur.MultiSelect {
			var sel []string
			for i := range cur.Options {
				if _, ok := m.multiPicked[i]; ok {
					sel = append(sel, cur.Options[i].Label)
				}
			}
			m.answers[m.qIdx].Selected = sel
		} else if len(cur.Options) > 0 {
			m.answers[m.qIdx].Selected = []string{cur.Options[m.cursor].Label}
		}
		m.answers[m.qIdx].CustomText = ""
		m.answers[m.qIdx].Declined = false
		return m.advance()
	case 'o':
		m.mode = qModeText
		ti := textinput.New()
		ti.Placeholder = "type a custom answer..."
		_ = ti.Focus()
		m.customInput = ti
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

// advance moves the cursor to the next question, or submits if at the last.
// Per-question editing state is reset on the way.
func (m QuestionModel) advance() (QuestionModel, tea.Cmd) {
	if m.qIdx >= len(m.req.Req.Questions)-1 {
		return m.submit()
	}
	m.qIdx++
	m.cursor = 0
	m.multiPicked = make(map[int]struct{})
	m.mode = qModeSelect
	ti := textinput.New()
	ti.Placeholder = "type a custom answer..."
	m.customInput = ti
	return m, nil
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

// View renders the modal centered in the available area.
func (m QuestionModel) View() string {
	if m.req == nil {
		return ""
	}
	q := m.req.Req.Questions[m.qIdx]

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
	fmt.Fprintf(&b, "Question %d of %d\n\n", m.qIdx+1, len(m.req.Req.Questions))
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
		if i == m.cursor && m.mode == qModeSelect {
			marker = "> "
		}
		check := "  "
		if q.MultiSelect {
			if _, ok := m.multiPicked[i]; ok {
				check = "[x] "
			} else {
				check = "[ ] "
			}
		}
		fmt.Fprintf(&b, "%s%s%s\n", marker, check, opt.Label)
	}

	if m.mode == qModeText {
		b.WriteString("\nCustom answer: ")
		b.WriteString(m.customInput.View())
		b.WriteString("\n[Enter] submit  [Esc] back to options\n")
	} else {
		b.WriteString("\n[Enter] confirm  [Space] toggle  [o] custom  [d] decline  [D] decline all  [Esc] dismiss\n")
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
