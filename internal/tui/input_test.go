package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

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
	m.SetWidth(60)
}

func TestInputModel_FocusBlur(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.Blur()
}

// --- QUM-455: post-Enter lookahead debounce ---
//
// Plain Enter no longer submits synchronously. Instead InputModel marks
// pendingEnter=true, bumps pendingEnterSeq, and schedules a
// pasteLookaheadMsg via tea.Tick(pasteLookaheadWindow). If another
// KeyPressMsg arrives before the tick, the pending Enter is reclassified as
// an embedded newline. If the tick fires with a still-current seq, the
// pending Enter resolves as a real Submit.

// TestInputModel_EnterWithText_EmitsSubmitMsg verifies the new shape: Enter
// returns a non-nil cmd that yields pasteLookaheadMsg (NOT SubmitMsg). The
// follow-up dispatch of that msg produces the SubmitMsg.
func TestInputModel_EnterWithText_EmitsSubmitMsg(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.ta.SetValue("hello")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if cmd == nil {
		t.Fatal("Enter with text should return a cmd (the lookahead tick)")
	}
	tickMsg := cmd()
	lk, ok := tickMsg.(pasteLookaheadMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want pasteLookaheadMsg", tickMsg)
	}
	if lk.seq != m.pendingEnterSeq {
		t.Errorf("tick seq = %d, want %d (current pendingEnterSeq)", lk.seq, m.pendingEnterSeq)
	}
	if !m.pendingEnter {
		t.Error("pendingEnter should be true after Enter")
	}

	// Dispatch the lookahead msg → SubmitMsg with original text.
	updated, cmd = m.Update(lk)
	m = updated
	if cmd == nil {
		t.Fatal("pasteLookaheadMsg with current seq should return a SubmitMsg cmd")
	}
	sub, ok := cmd().(SubmitMsg)
	if !ok {
		t.Fatalf("lookahead cmd returned %T, want SubmitMsg", cmd())
	}
	if sub.Text != "hello" {
		t.Errorf("SubmitMsg.Text = %q, want %q", sub.Text, "hello")
	}
	if m.ta.Value() != "" {
		t.Error("textarea should be cleared after submit resolves")
	}
	if m.pendingEnter {
		t.Error("pendingEnter should be cleared after submit resolves")
	}
}

func TestInputModel_EnterEmpty_NoSubmitMsg(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	// The Enter still schedules a tick (pendingEnter=true) — but resolving
	// the tick on empty input must NOT produce a SubmitMsg.
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(SubmitMsg); ok {
			t.Error("Enter with empty input should not produce SubmitMsg directly")
		}
		if lk, ok := msg.(pasteLookaheadMsg); ok {
			_, cmd2 := m.Update(lk)
			if cmd2 != nil {
				if _, ok := cmd2().(SubmitMsg); ok {
					t.Error("lookahead resolution of empty Enter should not produce SubmitMsg")
				}
			}
		}
	}
}

func TestInputModel_DisabledIgnoresInput(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.SetDisabled(true)

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'a'})
	m = updated
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated

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

// TestInputModel_ShiftEnterInsertsNewline: shift+enter inserts newline,
// does not submit, and does not engage the lookahead path.
func TestInputModel_ShiftEnterInsertsNewline(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.ta.SetValue("hello")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m = updated

	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(SubmitMsg); ok {
			t.Fatal("shift+enter should not produce SubmitMsg")
		}
	}
	val := m.ta.Value()
	if !strings.Contains(val, "\n") {
		t.Errorf("after shift+enter, value should contain a newline, got %q", val)
	}
}

// TestInputModel_ShiftEnter_NoTickScheduled asserts shift+enter does NOT
// emit a pasteLookaheadMsg cmd and leaves pendingEnter false.
func TestInputModel_ShiftEnter_NoTickScheduled(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m = updated

	if m.pendingEnter {
		t.Error("shift+enter must not set pendingEnter")
	}
	if cmd != nil {
		if _, ok := cmd().(pasteLookaheadMsg); ok {
			t.Error("shift+enter must not schedule pasteLookaheadMsg")
		}
	}
}

// TestInputModel_EnterThenPrintable_EmbedsNewline: a printable, then Enter,
// then another printable. The Enter is reclassified as an embedded newline
// by the trailing printable (no submit, textarea contains "a\nb").
func TestInputModel_EnterThenPrintable_EmbedsNewline(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = updated
	// Enter — sets pendingEnter, schedules tick.
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if cmd != nil {
		if _, ok := cmd().(SubmitMsg); ok {
			t.Fatal("Enter alone must not directly produce SubmitMsg")
		}
	}
	// Follow-up printable arrives before the tick fires.
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	m = updated

	got := m.ta.Value()
	if !strings.Contains(got, "\n") {
		t.Errorf("textarea after embedded Enter should contain newline, got %q", got)
	}
	if m.pendingEnter {
		t.Error("pendingEnter should be cleared once embedded Enter is resolved")
	}
}

// TestInputModel_LoneEnterAfterPause_Submits: Enter resolves to Submit
// when the lookahead tick fires with a still-current seq.
func TestInputModel_LoneEnterAfterPause_Submits(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.ta.SetValue("hello")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if cmd == nil {
		t.Fatal("Enter should schedule a lookahead tick")
	}
	lk, ok := cmd().(pasteLookaheadMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want pasteLookaheadMsg", cmd())
	}

	updated, cmd = m.Update(lk)
	m = updated
	if cmd == nil {
		t.Fatal("lookahead resolution should return a SubmitMsg cmd")
	}
	sub, ok := cmd().(SubmitMsg)
	if !ok {
		t.Fatalf("lookahead cmd returned %T, want SubmitMsg", cmd())
	}
	if !strings.Contains(sub.Text, "hello") {
		t.Errorf("SubmitMsg.Text = %q, want to contain %q", sub.Text, "hello")
	}
}

// TestInputModel_PasteBurst_AllLinesPreserved: rapid sequence of printable +
// Enter messages mimicking a stripped multi-line paste. No SubmitMsg during
// the burst; embedded Enters are resolved by following printable keys.
func TestInputModel_PasteBurst_AllLinesPreserved(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	feed := func(r rune) {
		t.Helper()
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated
	}
	feedEnter := func() {
		t.Helper()
		updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated
		if cmd != nil {
			if _, ok := cmd().(SubmitMsg); ok {
				t.Fatal("Enter during paste burst produced SubmitMsg directly")
			}
		}
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
	// Third Enter is still pending at end of burst — leave unresolved.

	got := m.ta.Value()
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("textarea value after paste burst = %q, want %q", got, want)
	}
	if !m.pendingEnter {
		t.Error("trailing Enter should still be pending at end of burst")
	}
}

// TestInputModel_PasteThenSubmit: paste burst yields multi-line content;
// trailing pasteLookaheadMsg with current seq submits the whole thing.
func TestInputModel_PasteThenSubmit(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	feed := func(r rune) {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated
	}
	feedEnter := func() {
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated
	}

	for _, r := range "ab" {
		feed(r)
	}
	feedEnter()
	for _, r := range "cd" {
		feed(r)
	}
	// Trailing Enter — pending at end of burst.
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if cmd == nil {
		t.Fatal("trailing Enter should schedule a lookahead tick")
	}
	lk, ok := cmd().(pasteLookaheadMsg)
	if !ok {
		t.Fatalf("trailing Enter cmd returned %T, want pasteLookaheadMsg", cmd())
	}

	// Tick fires with current seq → submit.
	updated, cmd = m.Update(lk)
	m = updated
	if cmd == nil {
		t.Fatal("lookahead with current seq should return SubmitMsg cmd")
	}
	sub, ok := cmd().(SubmitMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want SubmitMsg", cmd())
	}
	if !strings.Contains(sub.Text, "ab") || !strings.Contains(sub.Text, "cd") {
		t.Errorf("SubmitMsg.Text = %q, want to contain both %q and %q", sub.Text, "ab", "cd")
	}
	if !strings.Contains(sub.Text, "\n") {
		t.Errorf("SubmitMsg.Text = %q, want to contain a newline", sub.Text)
	}
}

// TestInputModel_EmbeddedEnterReclassify: Enter then printable resolves the
// Enter as embedded; the now-stale lookahead tick is a no-op.
func TestInputModel_EmbeddedEnterReclassify(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if cmd == nil {
		t.Fatal("Enter should schedule a lookahead tick")
	}
	tickMsg := cmd()
	lk, ok := tickMsg.(pasteLookaheadMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want pasteLookaheadMsg", tickMsg)
	}
	staleSeq := lk.seq

	if _, isSubmit := tickMsg.(SubmitMsg); isSubmit {
		t.Fatal("Enter must not produce SubmitMsg directly")
	}

	// Follow-up printable resolves the pending Enter as embedded.
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'A', Text: "A"})
	m = updated

	got := m.ta.Value()
	if !strings.Contains(got, "\nA") {
		t.Errorf("textarea after embedded Enter + 'A' should contain %q, got %q", "\nA", got)
	}
	if m.pendingEnter {
		t.Error("pendingEnter must be cleared after embedded resolution")
	}

	// Stale tick fires — must be a no-op.
	prevVal := m.ta.Value()
	prevSeq := m.pendingEnterSeq
	updated, cmd2 := m.Update(pasteLookaheadMsg{seq: staleSeq})
	m = updated
	if cmd2 != nil {
		if _, ok := cmd2().(SubmitMsg); ok {
			t.Error("stale lookahead must not produce SubmitMsg")
		}
	}
	if m.ta.Value() != prevVal {
		t.Error("stale lookahead must not mutate textarea")
	}
	if m.pendingEnterSeq != prevSeq {
		t.Error("stale lookahead must not bump seq")
	}
}

// TestInputModel_TwoConsecutiveEmbeddedEnters_ProducesDoubleNewline: two
// Enters in a row with no key between → first becomes embedded "\n", second
// is the new pending. A follow-up printable resolves the second as embedded.
func TestInputModel_TwoConsecutiveEmbeddedEnters_ProducesDoubleNewline(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	// First Enter — capture its lookahead seq for the stale-tick assertion.
	updated, cmd1 := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if cmd1 == nil {
		t.Fatal("first Enter should schedule a lookahead tick")
	}
	lk1, ok := cmd1().(pasteLookaheadMsg)
	if !ok {
		t.Fatalf("first Enter cmd returned %T, want pasteLookaheadMsg", cmd1())
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	// Follow-up printable resolves the second pending Enter as embedded.
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'A', Text: "A"})
	m = updated

	got := m.ta.Value()
	if !strings.HasSuffix(got, "\n\nA") {
		t.Errorf("textarea = %q, want suffix %q", got, "\n\nA")
	}
	if m.pendingEnter {
		t.Error("pendingEnter must be cleared at end of sequence")
	}

	// First Enter's lookahead tick fires late with a now-stale seq — must be
	// a no-op (no SubmitMsg, textarea unchanged).
	prevVal := m.ta.Value()
	updated, cmd2 := m.Update(pasteLookaheadMsg{seq: lk1.seq})
	m = updated
	if cmd2 != nil {
		if _, ok := cmd2().(SubmitMsg); ok {
			t.Error("stale first-Enter lookahead must not produce SubmitMsg")
		}
	}
	if m.ta.Value() != prevVal {
		t.Errorf("stale first-Enter lookahead must not mutate textarea; got %q want %q", m.ta.Value(), prevVal)
	}
}

// TestInputModel_RealEnter_SubmitsViaLookaheadTick: hello + Enter; the tick
// resolves to a SubmitMsg and clears the textarea.
func TestInputModel_RealEnter_SubmitsViaLookaheadTick(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.ta.SetValue("hello")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if cmd == nil {
		t.Fatal("Enter should schedule a lookahead tick")
	}
	lk, ok := cmd().(pasteLookaheadMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want pasteLookaheadMsg", cmd())
	}

	updated, cmd = m.Update(lk)
	m = updated
	if cmd == nil {
		t.Fatal("lookahead resolution should return SubmitMsg cmd")
	}
	sub, ok := cmd().(SubmitMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want SubmitMsg", cmd())
	}
	if sub.Text != "hello" {
		t.Errorf("SubmitMsg.Text = %q, want %q", sub.Text, "hello")
	}
	if m.ta.Value() != "" {
		t.Error("textarea should be cleared after submit")
	}
}

// TestInputModel_StaleLookaheadMsg_NoOp: a sequence that bumps seq twice
// followed by a stale-seq tick must be a no-op.
func TestInputModel_StaleLookaheadMsg_NoOp(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	// Enter #1 — captures seq1.
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	lk1, ok := cmd().(pasteLookaheadMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want pasteLookaheadMsg", cmd())
	}
	staleSeq := lk1.seq

	// Printable resolves #1 as embedded → seq bumped.
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = updated

	// Enter #2 — bumps seq again.
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated

	// Now fire the stale tick — must be no-op.
	prevVal := m.ta.Value()
	prevPending := m.pendingEnter
	updated, cmd2 := m.Update(pasteLookaheadMsg{seq: staleSeq})
	m = updated
	if cmd2 != nil {
		if _, ok := cmd2().(SubmitMsg); ok {
			t.Error("stale lookahead must not produce SubmitMsg")
		}
	}
	if m.ta.Value() != prevVal {
		t.Error("stale lookahead must not mutate textarea")
	}
	if m.pendingEnter != prevPending {
		t.Error("stale lookahead must not change pendingEnter")
	}
}

// TestInputModel_MultiParagraphPaste_dmotlesRepro: the dmotles repro pattern.
// "Test" + Enter + "Also ." + lookahead-tick → SubmitMsg containing both
// paragraphs separated by "\n", textarea cleared.
func TestInputModel_MultiParagraphPaste_dmotlesRepro(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	feed := func(r rune) {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated
	}

	for _, r := range "Test" {
		feed(r)
	}
	// Enter — schedules tick #1 for embedded paragraph break.
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	for _, r := range "Also ." {
		feed(r)
	}

	// At this point, the trailing printables resolved the embedded Enter.
	// pendingEnter should be false; the user has not yet hit Enter again.
	if m.pendingEnter {
		t.Fatal("pendingEnter should be false after trailing printables resolved embedded Enter")
	}

	// User now hits Enter to submit.
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if cmd == nil {
		t.Fatal("submit Enter should schedule a tick")
	}
	lk, ok := cmd().(pasteLookaheadMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want pasteLookaheadMsg", cmd())
	}
	if lk.seq != m.pendingEnterSeq {
		t.Errorf("tick seq = %d, want current seq %d", lk.seq, m.pendingEnterSeq)
	}

	updated, cmd = m.Update(lk)
	m = updated
	if cmd == nil {
		t.Fatal("lookahead with current seq should return SubmitMsg cmd")
	}
	sub, ok := cmd().(SubmitMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want SubmitMsg", cmd())
	}
	if !strings.Contains(sub.Text, "Test") {
		t.Errorf("SubmitMsg.Text = %q, want to contain %q", sub.Text, "Test")
	}
	if !strings.Contains(sub.Text, "Also") {
		t.Errorf("SubmitMsg.Text = %q, want to contain %q", sub.Text, "Also")
	}
	if !strings.Contains(sub.Text, "\n") {
		t.Errorf("SubmitMsg.Text = %q, want to contain a newline between paragraphs", sub.Text)
	}
	if m.ta.Value() != "" {
		t.Error("textarea should be cleared after submit")
	}
}

// TestInputModel_EnterStillSubmitsMultiLine: pre-seeded multi-line value +
// Enter still submits via the lookahead tick path with full content.
func TestInputModel_EnterStillSubmitsMultiLine(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.ta.SetValue("line1\nline2")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if cmd == nil {
		t.Fatal("Enter should schedule a lookahead tick")
	}
	lk, ok := cmd().(pasteLookaheadMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want pasteLookaheadMsg", cmd())
	}

	updated, cmd = m.Update(lk)
	m = updated
	if cmd == nil {
		t.Fatal("lookahead resolution should return SubmitMsg cmd")
	}
	sub, ok := cmd().(SubmitMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want SubmitMsg", cmd())
	}
	if sub.Text != "line1\nline2" {
		t.Errorf("SubmitMsg.Text = %q, want %q", sub.Text, "line1\nline2")
	}
}
