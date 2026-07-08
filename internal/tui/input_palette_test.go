package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// QUM-864: the `/`-opens-palette keystroke intercept was removed. `/` is now
// inserted literally into the textarea; the inline command popover (owned by
// AppModel) derives purely from the resulting text. InputModel therefore has no
// palette behaviour left — these tests cover the surviving input concerns.

func TestInputModel_SlashInsertsLiterally(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	updated, cmd := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated

	// '/' must not trigger a submit or any special command — it is a plain
	// textarea edit now.
	if cmd != nil {
		if _, ok := cmd().(SubmitMsg); ok {
			t.Error("'/' must not submit")
		}
	}
	if m.ta.Value() != "/" {
		t.Errorf("input value = %q, want %q ('/' inserted literally)", m.ta.Value(), "/")
	}
}

func TestInputModel_SlashWhileDisabledIsSwallowed(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.SetDisabled(true)

	updated, cmd := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated
	if cmd != nil {
		t.Error("disabled input must not emit a cmd on '/'")
	}
	if m.ta.Value() != "" {
		t.Errorf("disabled input value = %q, want empty (key swallowed)", m.ta.Value())
	}
}

func TestInputModel_LeadingSpaceSlashCommandSubmitsTrimmed(t *testing.T) {
	// Escape hatch per spec: `" /handoff"` → submit `/handoff` literal.
	// Under QUM-455 a plain Enter schedules a lookahead tick; resolve it by
	// dispatching pasteLookaheadMsg directly rather than waiting on tea.Tick.
	m := newTestInputModel(t)
	_ = m.Focus()

	m.ta.SetValue(" /handoff")

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if !m.pendingEnter {
		t.Fatal("Enter should set pendingEnter=true")
	}
	_, cmd := m.Update(pasteLookaheadMsg{seq: m.pendingEnterSeq})
	if cmd == nil {
		t.Fatal("Lookahead resolution with ' /handoff' should produce SubmitMsg")
	}
	sm, ok := cmd().(SubmitMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want SubmitMsg", cmd())
	}
	if sm.Text != "/handoff" {
		t.Errorf("SubmitMsg.Text = %q, want %q (leading space stripped)", sm.Text, "/handoff")
	}
}
