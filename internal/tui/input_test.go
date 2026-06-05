package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

// --- QUM-456: trailing-backslash line continuation ---
//
// Typing `foo\` and pressing plain Enter should NOT submit. Instead, the
// trailing backslash is dropped and a newline is inserted so the user can
// continue typing. Modeled after Claude Code / crush.

// TestInputModel_TrailingBackslash_InsertsNewlineNoSubmit: foo\ + Enter
// resolves through the lookahead tick and produces no SubmitMsg; the
// textarea ends up with "foo\n".
func TestInputModel_TrailingBackslash_InsertsNewlineNoSubmit(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.SetValue(`foo\`)

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
	if cmd != nil {
		if sub, ok := cmd().(SubmitMsg); ok {
			t.Fatalf("trailing-backslash Enter must not submit; got SubmitMsg{Text: %q}", sub.Text)
		}
	}
	if got, want := m.Value(), "foo\n"; got != want {
		t.Errorf("textarea value = %q, want %q", got, want)
	}
	if m.pendingEnter {
		t.Error("pendingEnter must be cleared after backslash continuation resolves")
	}
}

// TestInputModel_NoTrailingBackslash_StillSubmits: regression guard for the
// no-backslash case.
func TestInputModel_NoTrailingBackslash_StillSubmits(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.SetValue("foo")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
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
	if sub.Text != "foo" {
		t.Errorf("SubmitMsg.Text = %q, want %q", sub.Text, "foo")
	}
}

// TestInputModel_DoubleBackslash_StripsOnlyOne: `foo\\` + Enter strips ONE
// trailing backslash and inserts a newline → "foo\\n" (literal backslash
// then newline). Match crush behavior — only strip one.
func TestInputModel_DoubleBackslash_StripsOnlyOne(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.SetValue(`foo\\`)

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	lk, ok := cmd().(pasteLookaheadMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want pasteLookaheadMsg", cmd())
	}
	updated, cmd = m.Update(lk)
	m = updated
	if cmd != nil {
		if _, ok := cmd().(SubmitMsg); ok {
			t.Fatal("double-backslash Enter must not submit")
		}
	}
	if got, want := m.Value(), "foo\\\n"; got != want {
		t.Errorf("textarea value = %q, want %q", got, want)
	}
}

// --- QUM-571/QUM-576: Alt+Enter and Ctrl+J insert newlines without submit ---

// TestInputModel_AltEnter_InsertsNewline_NoSubmit: Alt+Enter (modifier =
// tea.ModAlt) inserts a literal newline into the textarea and must NOT
// schedule a lookahead tick, set pendingEnter, or produce a SubmitMsg.
func TestInputModel_AltEnter_InsertsNewline_NoSubmit(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.ta.SetValue("hello")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	m = updated

	if got, want := m.ta.Value(), "hello\n"; got != want {
		t.Errorf("textarea value after Alt+Enter = %q, want %q", got, want)
	}
	if m.pendingEnter {
		t.Error("Alt+Enter must not set pendingEnter")
	}
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(SubmitMsg); ok {
			t.Error("Alt+Enter must not produce SubmitMsg")
		}
		if _, ok := msg.(pasteLookaheadMsg); ok {
			t.Error("Alt+Enter must not schedule pasteLookaheadMsg")
		}
	}
}

// TestInputModel_CtrlJ_InsertsNewline_NoSubmit: Ctrl+J inserts a literal
// newline into the textarea, without lookahead tick or SubmitMsg.
func TestInputModel_CtrlJ_InsertsNewline_NoSubmit(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.ta.SetValue("hello")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	m = updated

	if got, want := m.ta.Value(), "hello\n"; got != want {
		t.Errorf("textarea value after Ctrl+J = %q, want %q", got, want)
	}
	if m.pendingEnter {
		t.Error("Ctrl+J must not set pendingEnter")
	}
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(SubmitMsg); ok {
			t.Error("Ctrl+J must not produce SubmitMsg")
		}
		if _, ok := msg.(pasteLookaheadMsg); ok {
			t.Error("Ctrl+J must not schedule pasteLookaheadMsg")
		}
	}
}

// TestInputModel_AltEnter_AfterPendingEnter_ResolvesEnterAsEmbedded: plain
// Enter sets pendingEnter; following Alt+Enter resolves the pending Enter as
// an embedded newline and itself inserts another newline. No SubmitMsg.
func TestInputModel_AltEnter_AfterPendingEnter_ResolvesEnterAsEmbedded(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.ta.SetValue("hello")

	// Plain Enter: pending.
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if !m.pendingEnter {
		t.Fatal("plain Enter should set pendingEnter")
	}
	if cmd != nil {
		if _, ok := cmd().(SubmitMsg); ok {
			t.Fatal("plain Enter must not produce SubmitMsg directly")
		}
	}

	// Alt+Enter: resolves the pending Enter as embedded, then inserts its own
	// newline.
	updated, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	m = updated

	if got, want := m.ta.Value(), "hello\n\n"; got != want {
		t.Errorf("textarea value after Enter then Alt+Enter = %q, want %q", got, want)
	}
	if m.pendingEnter {
		t.Error("pendingEnter must be cleared after Alt+Enter resolution")
	}
	if cmd != nil {
		if _, ok := cmd().(SubmitMsg); ok {
			t.Error("Alt+Enter following pending Enter must not produce SubmitMsg")
		}
	}
}

// --- QUM-664: input chrome (vertical bar gutter + inline placeholder help) ---

// TestInputModel_PlaceholderShowsHelpWhenEmpty: an empty input must render
// the static placeholder containing the key-binding hints (commands / help /
// ctrl+c) so the user always knows what to do. The `tab: cycle panel` hint
// was dropped in QUM-694 (decision b) while panel cycling itself was retained.
func TestInputModel_PlaceholderShowsHelpWhenEmpty(t *testing.T) {
	m := newTestInputModel(t)
	m.SetWidth(80)
	view := stripANSI(m.View())
	if !strings.Contains(view, "commands") {
		t.Errorf("empty View() should contain placeholder help substring 'commands', got:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+c") {
		t.Errorf("empty View() should contain 'ctrl+c' placeholder hint, got:\n%s", view)
	}
	if strings.Contains(view, "tab: cycle panel") {
		t.Errorf("empty View() must not contain 'tab: cycle panel' (dropped in QUM-694), got:\n%s", view)
	}
}

// TestInputModel_PlaceholderHiddenWhenPopulated: once the user types into
// the input, the placeholder must not be visible.
func TestInputModel_PlaceholderHiddenWhenPopulated(t *testing.T) {
	m := newTestInputModel(t)
	m.SetWidth(80)
	m.SetValue("x")
	view := stripANSI(m.View())
	if strings.Contains(view, "commands") {
		t.Errorf("View() with content should NOT contain placeholder text 'commands', got:\n%s", view)
	}
}

// TestInputModel_VerticalBarOnFirstRow: the input renders a "▌ " vertical
// bar gutter on its first row. ANSI-stripped output must start with "▌ ".
func TestInputModel_VerticalBarOnFirstRow(t *testing.T) {
	m := newTestInputModel(t)
	m.SetWidth(80)
	view := stripANSI(m.View())
	if !strings.Contains(view, "▌") {
		t.Errorf("View() should contain vertical bar '▌', got:\n%s", view)
	}
	// First non-empty line should start with "▌ ".
	lines := strings.Split(view, "\n")
	if len(lines) == 0 {
		t.Fatal("View() returned no lines")
	}
	if !strings.HasPrefix(lines[0], "▌ ") {
		t.Errorf("first row should start with '▌ ', got: %q", lines[0])
	}
}

// TestInputModel_InputBgFillsEveryRow: when the input grows multi-line, the
// chrome background tint must cover every textarea row, not only the cursor
// row (QUM-664 v3 eyeball regression: bubbles textarea's default
// Focused.CursorLine Background was rendering only on row 1, leaving rows 2+
// against terminal-native bg).
func TestInputModel_InputBgFillsEveryRow(t *testing.T) {
	m := newTestInputModel(t)
	m.SetWidth(80)
	m.SetValue("a\nb\nc")
	// Raw View() (ANSI preserved) — assert each non-empty row contains an
	// ANSI sequence that sets the same background color as the gutter row.
	raw := m.View()
	lines := strings.Split(raw, "\n")
	// Compute the expected bg-setter sequence from the same source the
	// production code reads (bubbles textarea defaults). That keeps the test
	// pinned to the contract rather than a hardcoded escape.
	wantBgFrag := lipgloss.NewStyle().Background(m.inputBg).Render(" ")
	// Extract the bg SGR open from the rendered fragment ("\x1b[...m " ...).
	// The leading escape + parameters up to the terminator is the prefix.
	idx := strings.Index(wantBgFrag, "m")
	if idx < 0 {
		t.Fatalf("could not derive bg SGR prefix from %q", wantBgFrag)
	}
	bgOpen := wantBgFrag[:idx+1]
	nonEmpty := 0
	for _, l := range lines {
		if strings.TrimSpace(stripANSI(l)) == "" {
			continue
		}
		nonEmpty++
		if !strings.Contains(l, bgOpen) {
			t.Errorf("row %q does not contain bg SGR open %q — input bg must fill every row, not just row 1",
				stripANSI(l), bgOpen)
		}
	}
	if nonEmpty < 3 {
		t.Fatalf("expected at least 3 non-empty rows (a, b, c), got %d:\n%s", nonEmpty, stripANSI(raw))
	}
}

// TestInputModel_VerticalBarOnEveryRow: a multi-row input value must render
// the "▌ " gutter on every non-empty row, not just the first.
func TestInputModel_VerticalBarOnEveryRow(t *testing.T) {
	m := newTestInputModel(t)
	m.SetWidth(80)
	m.SetValue("a\nb\nc")
	view := stripANSI(m.View())
	lines := strings.Split(view, "\n")
	rows := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		rows++
		if !strings.HasPrefix(l, "▌ ") {
			t.Errorf("non-empty row should start with '▌ ', got: %q", l)
		}
	}
	if rows < 3 {
		t.Errorf("expected at least 3 non-empty input rows (a, b, c), got %d:\n%s", rows, view)
	}
}

// TestInputModel_TextWidthShrinksByBarGutter: SetWidth(80) must leave the
// textarea with width 78 — two cells reserved for the "▌ " gutter so wrap
// happens inside the bar, not past it.
func TestInputModel_TextWidthShrinksByBarGutter(t *testing.T) {
	m := newTestInputModel(t)
	m.SetWidth(80)
	got := m.textInputWidth()
	if got != 78 {
		t.Errorf("textInputWidth() = %d, want 78 (m.width=80 minus 2 for '▌ ' gutter)", got)
	}
}

// TestInputModel_VerticalBarUsesInputBarStyle: the "▌" gutter must be
// rendered under theme.InputBarStyle (grey ANSI 8), mirroring how
// TestViewportModel_UserMessageUsesUserPromptStyle pins the chevron color.
func TestInputModel_VerticalBarUsesInputBarStyle(t *testing.T) {
	theme := NewTheme("colour212")
	m := NewInputModel(&theme)
	m.SetWidth(80)
	raw := m.View()

	// The gutter inherits the input's chrome background so the bar reads as
	// part of the input box (QUM-664 v3). Compose the same way production
	// does so the assertion tracks the contract.
	want := theme.InputBarStyle.Background(m.inputBg).Render("▌")
	if !strings.Contains(raw, want) {
		t.Errorf("raw View() should contain InputBarStyle-rendered '▌' (%q), got:\n%s",
			want, raw)
	}
}

// TestInputModel_QueuedIndicatorCoexistsWithBar: when a queued submit preview
// is set alongside a value, both the "▌" gutter and the "queued: <preview>"
// indicator must appear in the rendered view.
func TestInputModel_QueuedIndicatorCoexistsWithBar(t *testing.T) {
	m := newTestInputModel(t)
	m.SetWidth(80)
	m.SetValue("hi")
	m.SetPendingPreview("draft text")
	view := stripANSI(m.View())
	if !strings.Contains(view, "▌") {
		t.Errorf("View() should contain '▌' gutter, got:\n%s", view)
	}
	if !strings.Contains(view, "queued: draft text") {
		t.Errorf("View() should contain 'queued: draft text', got:\n%s", view)
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
