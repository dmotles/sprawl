package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestStatusBar_ViewFitsDeclaredWidth guards against the bug where
// StatusBar style's Padding(0,1) plus View()'s Width(m.width).Render(line)
// rendered m.width+2 cells and wrapped the trailing right-side segments onto
// a second line at most terminal widths. Fix: drop Padding; View() manages
// its own left/right spacing inside line.
func TestStatusBar_ViewFitsDeclaredWidth(t *testing.T) {
	m := newTestStatusBarModel(t)
	for _, w := range []int{40, 80, 120, 190, 300} {
		m.SetWidth(w)
		m.SetRestartLabel("consolidating timeline") // keeps the line longish
		view := m.View()
		for _, ln := range strings.Split(view, "\n") {
			if ln == "" {
				continue
			}
			if got := ansi.StringWidth(ln); got > w {
				t.Errorf("width=%d: rendered line width=%d (want ≤ %d). line=%q", w, got, w, ln)
			}
		}
	}
}

func newTestStatusBarModel(t *testing.T) StatusBarModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewStatusBarModel(&theme, "myrepo", "v1.0.0", 3)
}

func TestStatusBar_ContainsRepoName(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	view := m.View()
	if !strings.Contains(view, "myrepo") {
		t.Errorf("View() should contain repo name 'myrepo', got:\n%s", view)
	}
}

func TestStatusBar_ContainsVersion(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	view := m.View()
	if !strings.Contains(view, "v1.0.0") {
		t.Errorf("View() should contain version 'v1.0.0', got:\n%s", view)
	}
}

func TestStatusBar_ContainsAgentCount(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	view := m.View()
	if !strings.Contains(view, "3") {
		t.Errorf("View() should contain agent count '3', got:\n%s", view)
	}
}

func TestStatusBar_SetWidth(t *testing.T) {
	m := newTestStatusBarModel(t)
	// Should not panic.
	m.SetWidth(120)
}

// TestStatusBar_NoStaticHelpHint guards QUM-596: the static "? Help" hint was
// removed from the status bar because pressing `?` in the (default-focused)
// input textarea just types `?`, making the hint misleading. The QUM-420
// dynamic short-help row carries the help discoverability now.
func TestStatusBar_NoStaticHelpHint(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	view := m.View()
	if strings.Contains(view, "? Help") {
		t.Errorf("View() should not contain static '? Help' hint (QUM-596), got:\n%s", view)
	}
}

// --- New tests for turn state display ---

func TestStatusBar_SetTurnState_Thinking(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	m.SetTurnState(TurnThinking)
	view := m.View()
	if !strings.Contains(view, "Thinking") {
		t.Errorf("View() with TurnThinking should contain 'Thinking', got:\n%s", view)
	}
}

func TestStatusBar_SetTurnState_Streaming(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	m.SetTurnState(TurnStreaming)
	view := m.View()
	if !strings.Contains(view, "Streaming") {
		t.Errorf("View() with TurnStreaming should contain 'Streaming', got:\n%s", view)
	}
}

func TestStatusBar_SetTurnState_Idle(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	// Set to thinking first, then back to idle
	m.SetTurnState(TurnThinking)
	m.SetTurnState(TurnIdle)
	view := m.View()
	// Should NOT contain thinking/streaming when idle
	if strings.Contains(view, "Thinking") {
		t.Errorf("View() with TurnIdle should not contain 'Thinking', got:\n%s", view)
	}
	if strings.Contains(view, "Streaming") {
		t.Errorf("View() with TurnIdle should not contain 'Streaming', got:\n%s", view)
	}
}

func TestStatusBar_SetTurnState_Complete(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	m.SetTurnState(TurnComplete)
	view := m.View()
	// Complete state should not show Thinking or Streaming
	if strings.Contains(view, "Thinking") {
		t.Errorf("View() with TurnComplete should not contain 'Thinking', got:\n%s", view)
	}
}

func TestStatusBar_SetTurnCost(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	m.SetTurnCost(0.0123)
	view := m.View()
	if !strings.Contains(view, "$0.0123") {
		t.Errorf("View() should contain cost '$0.0123', got:\n%s", view)
	}
}

func TestStatusBar_SetSessionID_Displayed(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	m.SetSessionID("a1b2c3d4")
	view := m.View()
	if !strings.Contains(view, "sess:a1b2c3d4") {
		t.Errorf("View() with SetSessionID should contain 'sess:a1b2c3d4', got:\n%s", view)
	}
}

func TestStatusBar_SessionID_OmittedWhenEmpty(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	view := m.View()
	if strings.Contains(view, "sess:") {
		t.Errorf("View() without SetSessionID should not contain 'sess:', got:\n%s", view)
	}
}

// TestStatusBar_CumulativeCost verifies that SetTurnCost replaces (not
// accumulates) because total_cost_usd from Claude is session-cumulative.
// Two calls with 0.01 then 0.02 should show 0.02, not 0.03. (QUM-366)
func TestStatusBar_CumulativeCost(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	m.SetTurnCost(0.01)
	m.SetTurnCost(0.02)
	view := m.View()
	if !strings.Contains(view, "$0.0200") {
		t.Errorf("View() should contain replaced cost '$0.0200', got:\n%s", view)
	}
	if strings.Contains(view, "$0.0300") {
		t.Errorf("View() should NOT contain accumulated cost '$0.0300' (double-counting bug), got:\n%s", view)
	}
}

// --- QUM-385: Token counter tests ---

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{500, "500"},
		{999, "999"},
		{1000, "1k"},
		{1500, "1.5k"},
		{42300, "42.3k"},
		{100000, "100k"},
		{999999, "1000.0k"},
		{1000000, "1M"},
		{1500000, "1.5M"},
	}
	for _, tc := range tests {
		got := formatTokenCount(tc.input)
		if got != tc.want {
			t.Errorf("formatTokenCount(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestModelContextLimit_KnownModels(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"claude-opus-4-7-20260301", 1_000_000},
		{"claude-sonnet-4-6-20250514", 1_000_000},
		{"claude-haiku-4-0-20260301", 200_000},
	}
	for _, tc := range tests {
		got := modelContextLimit(tc.model)
		if got != tc.want {
			t.Errorf("modelContextLimit(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

func TestModelContextLimit_Unknown(t *testing.T) {
	got := modelContextLimit("gpt-4o")
	if got != defaultContextLimit {
		t.Errorf("modelContextLimit(unknown) = %d, want %d", got, defaultContextLimit)
	}
}

func TestModelContextLimit_Empty(t *testing.T) {
	got := modelContextLimit("")
	if got != defaultContextLimit {
		t.Errorf("modelContextLimit(\"\") = %d, want %d", got, defaultContextLimit)
	}
}

func TestStatusBar_TokenCounter_Displayed(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	m.SetContextLimit(1_000_000)
	m.SetTokenUsage(42300)
	view := m.View()
	if !strings.Contains(view, "42.3k/1M tokens") {
		t.Errorf("View() should contain '42.3k/1M tokens', got:\n%s", view)
	}
}

func TestStatusBar_TokenCounter_OmittedWhenZero(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	view := m.View()
	if strings.Contains(view, "tokens") {
		t.Errorf("View() should not contain 'tokens' when no usage set, got:\n%s", view)
	}
}

func TestStatusBar_TokenCounter_OmittedWhenNoLimit(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	m.SetTokenUsage(5000)
	view := m.View()
	if strings.Contains(view, "tokens") {
		t.Errorf("View() should not contain 'tokens' when no limit set, got:\n%s", view)
	}
}

func TestStatusBar_TokenCounter_UpdatesOnNewValue(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	m.SetContextLimit(1_000_000)
	m.SetTokenUsage(10000)
	m.SetTokenUsage(20000)
	view := m.View()
	if !strings.Contains(view, "20k/1M tokens") {
		t.Errorf("View() should contain '20k/1M tokens' after update, got:\n%s", view)
	}
	if strings.Contains(view, "10k") {
		t.Errorf("View() should not contain old '10k' value, got:\n%s", view)
	}
}

// --- QUM-391 / QUM-321: SetRestartLabel tests ---

// TestStatusBar_SetRestartLabel_UsesLabelInView verifies that when a
// consolidation phase label is set, the status bar renders it.
func TestStatusBar_SetRestartLabel_UsesLabelInView(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	m.SetRestartLabel("consolidating timeline")
	view := m.View()
	if !strings.Contains(view, "consolidating timeline") {
		t.Errorf("View() should contain label 'consolidating timeline', got:\n%s", view)
	}
}

// TestStatusBar_SetRestartLabel_EmptyOmitted verifies that when the label is
// empty (no consolidation in flight), no label entry is rendered. QUM-321
// removed the vestigial "restart Ns" elapsed counter, so an empty label means
// no indicator at all.
func TestStatusBar_SetRestartLabel_EmptyOmitted(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(120)
	m.SetRestartLabel("")
	view := m.View()
	if strings.Contains(view, "restart") {
		t.Errorf("View() should not contain any 'restart' indicator when label is empty, got:\n%s", view)
	}
	if strings.Contains(view, "consolidating") {
		t.Errorf("View() should not contain a consolidation label when empty, got:\n%s", view)
	}
}

// TestStatusBar_PendingQuestions_DepthZero_NoSegment verifies that depth==0
// hides the pending-questions indicator entirely. (QUM-527)
func TestStatusBar_PendingQuestions_DepthZero_NoSegment(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(160)
	m.SetPendingQuestions(0, "")
	view := m.View()
	if strings.Contains(view, "asking") {
		t.Errorf("View() should not contain 'asking' at depth 0, got:\n%s", view)
	}
}

// TestStatusBar_PendingQuestions_DepthOne_RendersAsking checks the single-
// pending-question segment carries the agent name, the 'asking' verb, and
// the Ctrl-Q reopen hint. (QUM-527)
func TestStatusBar_PendingQuestions_DepthOne_RendersAsking(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	m.SetPendingQuestions(1, "weave")
	view := m.View()
	if !strings.Contains(view, "weave") {
		t.Errorf("View() should contain agent name 'weave', got:\n%s", view)
	}
	if !strings.Contains(view, "asking") {
		t.Errorf("View() should contain 'asking', got:\n%s", view)
	}
	if !strings.Contains(view, "Ctrl-Q") {
		t.Errorf("View() should contain 'Ctrl-Q' hint, got:\n%s", view)
	}
}

// TestStatusBar_PendingQuestions_ModalHidden_AdvertisesEscHint is the
// QUM-611 discoverability guard. When the modal is hidden but a question
// is still pending, the segment must advertise the Esc-cancel affordance
// alongside the Ctrl-Q reopen hint.
func TestStatusBar_PendingQuestions_ModalHidden_AdvertisesEscHint(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	m.SetPendingQuestions(1, "weave")
	m.SetQuestionModalHidden(true)
	view := m.View()
	if !strings.Contains(view, "Ctrl-Q") {
		t.Errorf("View() must keep Ctrl-Q hint when hidden, got:\n%s", view)
	}
	if !strings.Contains(view, "Esc") {
		t.Errorf("View() must advertise Esc-cancel hint when modal hidden but pending, got:\n%s", view)
	}
}

// --- QUM-681: EventBus drop telemetry status-bar segment ---

// TestStatusBarModel_EventDropsSegment_Renders checks that a single
// dropped-subscriber entry surfaces in the status bar with the canonical
// "⚠ events dropped: N (name)" format.
func TestStatusBarModel_EventDropsSegment_Renders(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	m.SetEventDrops([]EventDropSegment{{Name: "tui-viewport", Count: 42}})
	view := m.View()
	if !strings.Contains(view, "⚠ events dropped: 42 (tui-viewport)") {
		t.Errorf("View() should contain drop segment, got:\n%s", view)
	}
}

// TestStatusBarModel_EventDropsSegment_HiddenWhenEmpty checks that with no
// drops, no warning segment is rendered.
func TestStatusBarModel_EventDropsSegment_HiddenWhenEmpty(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	view := m.View()
	if strings.Contains(view, "⚠ events dropped") {
		t.Errorf("View() should not contain drop segment when empty, got:\n%s", view)
	}
}

// TestStatusBarModel_EventDropsSegment_MultipleSubscribers checks that the
// worst offender is named and a "+K more" tail tells the operator others
// also dropped.
func TestStatusBarModel_EventDropsSegment_MultipleSubscribers(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	m.SetEventDrops([]EventDropSegment{
		{Name: "worst", Count: 100},
		{Name: "mid", Count: 50},
		{Name: "tail", Count: 5},
	})
	view := m.View()
	if !strings.Contains(view, "⚠ events dropped: 100 (worst)") {
		t.Errorf("View() should contain worst-offender segment, got:\n%s", view)
	}
	if !strings.Contains(view, "+2 more") {
		t.Errorf("View() should contain '+2 more' for the tail, got:\n%s", view)
	}
}

// TestStatusBar_PendingQuestions_DepthThree_RendersPlusMore checks depth>=2
// renders the agent name and "+N more" where N = depth-1. (QUM-527)
func TestStatusBar_PendingQuestions_DepthThree_RendersPlusMore(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	m.SetPendingQuestions(3, "tower")
	view := m.View()
	if !strings.Contains(view, "tower") {
		t.Errorf("View() should contain agent name 'tower', got:\n%s", view)
	}
	if !strings.Contains(view, "+2 more") {
		t.Errorf("View() should contain '+2 more' at depth 3, got:\n%s", view)
	}
	if !strings.Contains(view, "Ctrl-Q") {
		t.Errorf("View() should contain 'Ctrl-Q' hint, got:\n%s", view)
	}
}

// --- QUM-669 step 7: resync-pill segment ---
//
// SetResyncPill installs a status-bar segment shown while a viewport resync
// is in flight. Empty hides the segment. Mirrors the SetValidatePill pattern
// (statusbar.go:85). Tests are RED until View() is wired to render the pill.

func TestStatusBarModel_SetResyncPill_RendersDuringResync(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	const pill = "resyncing…"
	m.SetResyncPill(pill)
	view := ansi.Strip(m.View())
	if !strings.Contains(view, pill) {
		t.Errorf("View() should contain resync pill %q after SetResyncPill, got:\n%s", pill, view)
	}
}

func TestStatusBarModel_SetResyncPill_ClearedByEmpty(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	const pill = "resyncing…"
	m.SetResyncPill(pill)
	m.SetResyncPill("")
	view := ansi.Strip(m.View())
	if strings.Contains(view, pill) {
		t.Errorf("View() should NOT contain resync pill %q after SetResyncPill(\"\"), got:\n%s", pill, view)
	}
}

// --- QUM-675 S5: transient label (the new single sink for status/banner text
// that used to land in the viewport via vp.AppendStatus / vp.AppendBanner).
// Spec: docs/designs/tui-structural-rewrite-plan.md §3 S5 + tower's
// display-policy comment on QUM-675. Single field, last-write-wins, no queue,
// no timer. Cleared by explicit state transitions (tested at the AppModel
// reducer level in app_test.go), not by an auto-decay.
//
// These tests are RED until SetTransientLabel + the transientLabel field land
// in statusbar.go and the View() renders the field as one |-joined right-side
// segment alongside the existing segments.

func TestStatusBar_SetTransientLabel_RendersInRightSegments(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	const label = "Interrupt sent"
	m.SetTransientLabel(label)
	view := ansi.Strip(m.View())
	if !strings.Contains(view, label) {
		t.Fatalf("View() should contain transient label %q after SetTransientLabel, got:\n%s", label, view)
	}
	// The label must be joined with the existing pipe-delimited right-side
	// parts list — not rendered as a standalone left-side chip. Asserting
	// against a neighbour segment (the version string) is the cheapest way to
	// verify it sits inside the same join.
	if !strings.Contains(view, "| "+label) && !strings.Contains(view, label+" |") {
		t.Errorf("transient label %q should be joined into the |-delimited right segments; got:\n%s", label, view)
	}
}

func TestStatusBar_SetTransientLabel_EmptyHides(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	const label = "Interrupt sent"
	m.SetTransientLabel(label)
	view := ansi.Strip(m.View())
	if !strings.Contains(view, label) {
		t.Fatalf("precondition: View() should contain %q before clear, got:\n%s", label, view)
	}
	m.SetTransientLabel("")
	view = ansi.Strip(m.View())
	if strings.Contains(view, label) {
		t.Errorf("View() should NOT contain transient label %q after SetTransientLabel(\"\"), got:\n%s", label, view)
	}
}

func TestStatusBar_SetTransientLabel_LastWriteWins(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(200)
	const labelA = "Session restarting (handoff)..."
	const labelB = "Interrupt sent"
	m.SetTransientLabel(labelA)
	m.SetTransientLabel(labelB)
	view := ansi.Strip(m.View())
	if strings.Contains(view, labelA) {
		t.Errorf("View() should NOT contain superseded label %q (last-write-wins), got:\n%s", labelA, view)
	}
	if !strings.Contains(view, labelB) {
		t.Errorf("View() should contain latest label %q, got:\n%s", labelB, view)
	}
}
