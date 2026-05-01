package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// setFakeClock installs a controllable clock for tests that exercise the
// time-based paste classifier (QUM-432). Returns an advance(d) closure that
// callers use to move the fake clock forward; the original nowFunc is restored
// via t.Cleanup.
func setFakeClock(t *testing.T) func(time.Duration) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	orig := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = orig })
	return func(d time.Duration) { now = now.Add(d) }
}

func newTestInputModel(t *testing.T) InputModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewInputModel(&theme)
}

func TestInputModel_InitialState(t *testing.T) {
	m := newTestInputModel(t)
	view := m.View()
	if len(strings.TrimSpace(view)) == 0 {
		t.Error("View() should not be empty initially")
	}
}

func TestInputModel_SetWidth(t *testing.T) {
	m := newTestInputModel(t)
	// Should not panic.
	m.SetWidth(60)
}

func TestInputModel_FocusBlur(t *testing.T) {
	m := newTestInputModel(t)
	// Should not panic.
	_ = m.Focus()
	m.Blur()
}

// --- New tests for Enter key submission ---

func TestInputModel_EnterWithText_EmitsSubmitMsg(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	// Set value directly (textinput may not process individual key chars in tests)
	m.ta.SetValue("hello")

	// Press Enter
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated

	// Should produce a cmd that yields SubmitMsg
	if cmd == nil {
		t.Fatal("Enter with text should return a cmd")
	}
	msg := cmd()
	submitMsg, ok := msg.(SubmitMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want SubmitMsg", msg)
	}
	if submitMsg.Text != "hello" {
		t.Errorf("SubmitMsg.Text = %q, want %q", submitMsg.Text, "hello")
	}

	// Input should be cleared after submit
	if m.ta.Value() != "" {
		t.Error("input should be cleared after Enter submission")
	}
}

func TestInputModel_EnterEmpty_NoSubmitMsg(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	// Press Enter with no text
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Should not produce a SubmitMsg (either nil cmd, or cmd that returns nil)
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(SubmitMsg); ok {
			t.Error("Enter with empty input should not produce SubmitMsg")
		}
	}
}

func TestInputModel_DisabledIgnoresInput(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.SetDisabled(true)

	// Try typing while disabled
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'a'})
	m = updated

	// Try Enter while disabled
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated

	// Should not produce a submit command while disabled
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(SubmitMsg); ok {
			t.Error("disabled input should not produce SubmitMsg on Enter")
		}
	}
}

func TestInputModel_SetDisabledTrue(t *testing.T) {
	m := newTestInputModel(t)
	m.SetDisabled(true)
	if !m.disabled {
		t.Error("disabled should be true after SetDisabled(true)")
	}
}

func TestInputModel_SetDisabledFalse(t *testing.T) {
	m := newTestInputModel(t)
	m.SetDisabled(true)
	m.SetDisabled(false)
	if m.disabled {
		t.Error("disabled should be false after SetDisabled(false)")
	}
}

// --- Tests for QUM-381: multi-line textarea migration ---

// TestInputModel_ShiftEnterInsertsNewline verifies that shift+enter inserts a
// newline into the input rather than submitting. With the current textinput
// implementation this will FAIL because textinput does not handle shift+enter
// as newline insertion. After the textarea migration it should pass.
func TestInputModel_ShiftEnterInsertsNewline(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	// Seed the input with some text.
	m.ta.SetValue("hello")

	// Send shift+enter — should insert a newline, not submit.
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m = updated

	// Shift+enter must NOT produce a SubmitMsg.
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(SubmitMsg); ok {
			t.Fatal("shift+enter should not produce SubmitMsg")
		}
	}

	// The value should now contain a newline.
	val := m.ta.Value()
	if !strings.Contains(val, "\n") {
		t.Errorf("after shift+enter, value should contain a newline, got %q", val)
	}
}

// --- QUM-432: time-based paste classifier for the stripped-bracketed-paste path ---
//
// In environments where outer tmux/SSH/Terminal.app strips the ESC[200~ /
// ESC[201~ markers before they reach the TUI, multi-line pastes arrive as a
// stream of tea.KeyPressMsg events with embedded line breaks decoded as
// KeyPressMsg{Code: tea.KeyEnter}. A time-based classifier reclassifies an
// Enter that arrives within pasteBurstWindow of the previous printable key as
// an embedded newline (literal "\n" inserted into the textarea) rather than a
// submit. Plain Enter outside that window submits as today.

// TestInputModel_RapidPrintableThenEnter_InsertsNewlineNoSubmit verifies the
// core paste-detection path: a printable key followed immediately by Enter
// (sub-burst-window) is reclassified as embedded — textarea receives a literal
// newline and no SubmitMsg is emitted.
func TestInputModel_RapidPrintableThenEnter_InsertsNewlineNoSubmit(t *testing.T) {
	advance := setFakeClock(t)
	m := newTestInputModel(t)
	_ = m.Focus()

	// Type "a" then Enter at 0ms — well within pasteBurstWindow.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = updated
	advance(1 * time.Millisecond)
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated

	if cmd != nil {
		if _, ok := cmd().(SubmitMsg); ok {
			t.Fatal("rapid Enter after printable should NOT submit (embedded newline)")
		}
	}
	got := m.ta.Value()
	if !strings.Contains(got, "\n") {
		t.Errorf("textarea after embedded Enter should contain newline, got %q", got)
	}
}

// TestInputModel_LoneEnterAfterPause_Submits verifies that a real Enter typed
// after a quiet period (longer than the burst+quiet windows) still submits as
// today. This is the regression guard for normal typing-then-Enter UX.
func TestInputModel_LoneEnterAfterPause_Submits(t *testing.T) {
	advance := setFakeClock(t)
	m := newTestInputModel(t)
	_ = m.Focus()
	m.ta.SetValue("hello")

	// Simulate the user typing a key then pausing well past the quiet window.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	m = updated
	advance(500 * time.Millisecond)

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated

	if cmd == nil {
		t.Fatal("Enter after quiet pause should produce a cmd")
	}
	msg := cmd()
	sub, ok := msg.(SubmitMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want SubmitMsg", msg)
	}
	if !strings.Contains(sub.Text, "hello") {
		t.Errorf("SubmitMsg.Text = %q, want to contain %q", sub.Text, "hello")
	}
}

// TestInputModel_PasteBurst_AllLinesPreserved drives a rapid sequence of
// printable + Enter messages mimicking a stripped multi-line paste of
// "line1\rline2\rline3". The result must be a multi-line textarea value with
// no SubmitMsg emitted during the burst.
func TestInputModel_PasteBurst_AllLinesPreserved(t *testing.T) {
	advance := setFakeClock(t)
	m := newTestInputModel(t)
	_ = m.Focus()

	// Printable keys forward to textarea which may return cursor-blink Tick
	// cmds that sleep when invoked — never SubmitMsg — so we don't drain them.
	feed := func(r rune) {
		t.Helper()
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated
		advance(100 * time.Microsecond)
	}
	feedEnter := func() {
		t.Helper()
		updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated
		if cmd != nil {
			if _, ok := cmd().(SubmitMsg); ok {
				t.Fatal("Enter during paste burst produced SubmitMsg")
			}
		}
		advance(100 * time.Microsecond)
	}

	for _, r := range "line1" {
		feed(r)
	}
	feedEnter()
	for _, r := range "line2" {
		feed(r)
	}
	feedEnter()
	for _, r := range "line3" {
		feed(r)
	}
	feedEnter()

	got := m.ta.Value()
	want := "line1\nline2\nline3\n"
	if got != want {
		t.Errorf("textarea value after paste burst = %q, want %q", got, want)
	}
}

// TestInputModel_PasteThenSubmit verifies the end-to-end flow: a paste burst
// produces multi-line textarea content (no submit), then after a quiet pause
// the user presses Enter to submit the whole multi-line message.
func TestInputModel_PasteThenSubmit(t *testing.T) {
	advance := setFakeClock(t)
	m := newTestInputModel(t)
	_ = m.Focus()

	feed := func(r rune) {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated
		advance(100 * time.Microsecond)
	}
	feedEnter := func() {
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated
		advance(100 * time.Microsecond)
	}

	for _, r := range "ab" {
		feed(r)
	}
	feedEnter()
	for _, r := range "cd" {
		feed(r)
	}
	// Trailing Enter at end of paste — embedded.
	feedEnter()

	// Quiet pause, well past the quiet window.
	advance(500 * time.Millisecond)

	// User presses Enter to actually submit.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter after quiet pause should submit")
	}
	msg := cmd()
	sub, ok := msg.(SubmitMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want SubmitMsg", msg)
	}
	if !strings.Contains(sub.Text, "ab") || !strings.Contains(sub.Text, "cd") {
		t.Errorf("SubmitMsg.Text = %q, want to contain both %q and %q", sub.Text, "ab", "cd")
	}
	if !strings.Contains(sub.Text, "\n") {
		t.Errorf("SubmitMsg.Text = %q, want to contain a newline", sub.Text)
	}
}

// TestInputModel_QuietWindowExtendsOnContinuingBurst verifies that during a
// paste burst the quiet window is extended each time a new key arrives, so an
// Enter that lands slightly past pasteBurstWindow but within pasteQuietWindow
// of the running burst is still classified as embedded.
func TestInputModel_QuietWindowExtendsOnContinuingBurst(t *testing.T) {
	advance := setFakeClock(t)
	m := newTestInputModel(t)
	_ = m.Focus()

	// Start a paste burst: 'a' + Enter at 0ms (Enter classified embedded).
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = updated
	advance(1 * time.Millisecond)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated

	// Subsequent printable + Enter pair separated by 20ms each — past
	// pasteBurstWindow (10ms), but within pasteQuietWindow (50ms) of prior
	// burst activity. Must still be embedded.
	advance(20 * time.Millisecond)
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	m = updated
	advance(20 * time.Millisecond)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		if _, ok := cmd().(SubmitMsg); ok {
			t.Fatal("Enter inside extended quiet window should be embedded, not submit")
		}
	}
}

// TestInputModel_EnterStillSubmitsMultiLine verifies that pressing Enter
// submits even when the input contains multi-line text. The SubmitMsg.Text
// should preserve the full multi-line content.
func TestInputModel_EnterStillSubmitsMultiLine(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	// Seed multi-line content directly.
	m.ta.SetValue("line1\nline2")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated

	if cmd == nil {
		t.Fatal("Enter with multi-line text should return a cmd")
	}
	msg := cmd()
	submitMsg, ok := msg.(SubmitMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want SubmitMsg", msg)
	}
	expected := "line1\nline2"
	if submitMsg.Text != expected {
		t.Errorf("SubmitMsg.Text = %q, want %q", submitMsg.Text, expected)
	}
}
