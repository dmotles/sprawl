package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/tui/commands"
)

// readyRoutingApp builds a ready AppModel wired to the given fake backend.
func readyRoutingApp(t *testing.T, bridge *fakeSessionBackend) AppModel {
	t.Helper()
	m := newTestAppModelWithBridge(t, bridge)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(AppModel)
}

// routedMsg expands a cmd and returns its single non-nil message, failing if
// the cmd produced zero or more than one. Slash routing dispatches exactly one
// message (a single sendMsgCmd), so this pins that contract.
func routedMsg(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	msgs := collectBatchMsgs(t, cmd)
	if len(msgs) != 1 {
		t.Fatalf("expected exactly 1 dispatched msg, got %d: %v", len(msgs), msgs)
	}
	return msgs[0]
}

func TestSubmitMsg_RoutesUICommandsLocally(t *testing.T) {
	cases := []struct {
		text string
		want tea.Msg
	}{
		{"/help", ToggleHelpMsg{}},
		{"/usage", ShowUsageMsg{}},
		{"/tree", ToggleTreeMsg{}},
		{"/exit", PaletteQuitMsg{}},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			bridge := newFakeSessionBackend()
			app := readyRoutingApp(t, bridge)
			_, cmd := app.Update(SubmitMsg{Text: tc.text})
			msg := routedMsg(t, cmd)
			if msg != tc.want {
				t.Errorf("SubmitMsg(%q) dispatched %T (%v), want %T", tc.text, msg, msg, tc.want)
			}
			if bridge.sendCalls != 0 {
				t.Errorf("SubmitMsg(%q) must NOT reach claude; sendCalls=%d", tc.text, bridge.sendCalls)
			}
		})
	}
}

// TestSubmitMsg_RoutesUICommandMidTurn locks in that local routing is
// turn-state-independent: a UI command submitted while a turn is streaming must
// still dispatch locally and never reach claude.
func TestSubmitMsg_RoutesUICommandMidTurn(t *testing.T) {
	bridge := newFakeSessionBackend()
	app := readyRoutingApp(t, bridge)
	app.turnState = TurnStreaming
	_, cmd := app.Update(SubmitMsg{Text: "/help"})
	msg := routedMsg(t, cmd)
	if _, ok := msg.(ToggleHelpMsg); !ok {
		t.Errorf("mid-turn SubmitMsg(/help) dispatched %T, want ToggleHelpMsg", msg)
	}
	if bridge.sendCalls != 0 {
		t.Errorf("mid-turn /help must not reach claude; sendCalls=%d", bridge.sendCalls)
	}
}

// TestSubmitMsg_TypedPastedEquivalence proves the core bug fix: a pasted
// command and a typed command converge on an identical SubmitMsg{Text}. The
// textarea holds the same value regardless of how it was populated (typed keys
// vs a bracketed paste inserted via InsertString), so Enter emits the same
// SubmitMsg — which the routing tests above then dispatch identically.
func TestSubmitMsg_TypedPastedEquivalence(t *testing.T) {
	const line = `/attach /tmp/x.png "hi"`
	submitTextFromInput := func(populate func(m *InputModel)) string {
		m := newTestInputModel(t)
		_ = m.Focus()
		populate(&m)
		updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated
		lk := cmd().(pasteLookaheadMsg)
		_, cmd = m.Update(lk)
		return cmd().(SubmitMsg).Text
	}
	typed := submitTextFromInput(func(m *InputModel) { m.ta.SetValue(line) })
	pasted := submitTextFromInput(func(m *InputModel) { m.ta.InsertString(line) })
	if typed != pasted {
		t.Fatalf("typed SubmitMsg.Text %q != pasted %q", typed, pasted)
	}
	if pasted != line {
		t.Errorf("SubmitMsg.Text = %q, want %q", pasted, line)
	}
}

func TestSubmitMsg_RoutesHandoffPromptInjection(t *testing.T) {
	bridge := newFakeSessionBackend()
	app := readyRoutingApp(t, bridge)
	_, cmd := app.Update(SubmitMsg{Text: "/handoff"})
	msg := routedMsg(t, cmd)
	inj, ok := msg.(InjectPromptMsg)
	if !ok {
		t.Fatalf("SubmitMsg(/handoff) dispatched %T, want InjectPromptMsg", msg)
	}
	if inj.Template != commands.HandoffPromptTemplate {
		t.Errorf("InjectPromptMsg.Template = %q, want HandoffPromptTemplate", inj.Template)
	}
	if bridge.sendCalls != 0 {
		t.Errorf("/handoff must not reach claude directly; sendCalls=%d", bridge.sendCalls)
	}
}

func TestSubmitMsg_RoutesAttachWithArgs(t *testing.T) {
	bridge := newFakeSessionBackend()
	app := readyRoutingApp(t, bridge)
	_, cmd := app.Update(SubmitMsg{Text: `/attach /tmp/x.png "hi"`})
	msg := routedMsg(t, cmd)
	att, ok := msg.(AttachMsg)
	if !ok {
		t.Fatalf("SubmitMsg(/attach ...) dispatched %T, want AttachMsg", msg)
	}
	if len(att.Paths) != 1 || att.Paths[0] != "/tmp/x.png" {
		t.Errorf("AttachMsg.Paths = %v, want [/tmp/x.png]", att.Paths)
	}
	if att.Prompt != "hi" {
		t.Errorf("AttachMsg.Prompt = %q, want %q", att.Prompt, "hi")
	}
	if bridge.sendCalls != 0 {
		t.Errorf("/attach must not reach claude as a raw prompt; sendCalls=%d", bridge.sendCalls)
	}
}

// assertUsageToast runs the cmd from a consumed slash command and asserts it
// surfaces a usage toast (feedback) rather than silently swallowing input.
func assertUsageToast(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	msg := routedMsg(t, cmd)
	if _, ok := msg.(ToastSpawnMsg); !ok {
		t.Fatalf("expected a ToastSpawnMsg (usage feedback), got %T", msg)
	}
}

func TestSubmitMsg_BareAttachConsumedNoLeak(t *testing.T) {
	// After palette deletion, a bare /attach (no paths) is consumed locally
	// (usage toast) and must NEVER leak to claude as a raw prompt.
	bridge := newFakeSessionBackend()
	app := readyRoutingApp(t, bridge)
	_, cmd := app.Update(SubmitMsg{Text: "/attach"})
	assertUsageToast(t, cmd)
	if bridge.sendCalls != 0 {
		t.Errorf("bare /attach must not reach claude; sendCalls=%d", bridge.sendCalls)
	}
}

func TestSubmitMsg_SwitchFuzzyResolvesToAgentSelected(t *testing.T) {
	bridge := newFakeSessionBackend()
	app := readyRoutingApp(t, bridge)
	// rootAgent "weave" is always in agentNames(); "weav" fuzzy-resolves to it.
	_, cmd := app.Update(SubmitMsg{Text: "/switch weav"})
	msg := routedMsg(t, cmd)
	sel, ok := msg.(AgentSelectedMsg)
	if !ok {
		t.Fatalf("/switch weav dispatched %T, want AgentSelectedMsg", msg)
	}
	if sel.Name != "weave" {
		t.Errorf("AgentSelectedMsg.Name = %q, want weave (fuzzy resolve)", sel.Name)
	}
	if bridge.sendCalls != 0 {
		t.Errorf("/switch must not reach claude; sendCalls=%d", bridge.sendCalls)
	}
}

func TestSubmitMsg_BareSwitchConsumedNoLeak(t *testing.T) {
	bridge := newFakeSessionBackend()
	app := readyRoutingApp(t, bridge)
	_, cmd := app.Update(SubmitMsg{Text: "/switch"})
	assertUsageToast(t, cmd)
	if bridge.sendCalls != 0 {
		t.Errorf("bare /switch must not reach claude; sendCalls=%d", bridge.sendCalls)
	}
}

func TestSubmitMsg_SwitchNoMatchConsumedNoLeak(t *testing.T) {
	bridge := newFakeSessionBackend()
	app := readyRoutingApp(t, bridge)
	_, cmd := app.Update(SubmitMsg{Text: "/switch nomatch"})
	assertUsageToast(t, cmd)
	if bridge.sendCalls != 0 {
		t.Errorf("/switch with no agent match must not reach claude; sendCalls=%d", bridge.sendCalls)
	}
}

func TestSubmitMsg_UnknownSlashPassesThrough(t *testing.T) {
	bridge := newFakeSessionBackend()
	app := readyRoutingApp(t, bridge)
	app.Update(SubmitMsg{Text: "/etc/hosts is broken"})
	if bridge.sendCalls != 1 {
		t.Fatalf("unknown-slash prompt must reach claude; sendCalls=%d", bridge.sendCalls)
	}
	if bridge.lastSent != "/etc/hosts is broken" {
		t.Errorf("lastSent = %q, want unchanged passthrough", bridge.lastSent)
	}
}

func TestSubmitMsg_NonSlashPassesThrough(t *testing.T) {
	bridge := newFakeSessionBackend()
	app := readyRoutingApp(t, bridge)
	app.Update(SubmitMsg{Text: "hello claude"})
	if bridge.sendCalls != 1 {
		t.Fatalf("non-slash prompt must reach claude; sendCalls=%d", bridge.sendCalls)
	}
	if bridge.lastSent != "hello claude" {
		t.Errorf("lastSent = %q, want %q", bridge.lastSent, "hello claude")
	}
}

func TestSubmitMsg_UICommandRoutesWithoutBridge(t *testing.T) {
	// UI commands (e.g. /help) must dispatch even when no bridge is wired,
	// matching the palette (which needs no bridge for UI actions).
	m := newTestAppModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := updated.(AppModel)
	_, cmd := app.Update(SubmitMsg{Text: "/help"})
	msg := routedMsg(t, cmd)
	if _, ok := msg.(ToggleHelpMsg); !ok {
		t.Errorf("SubmitMsg(/help) with no bridge dispatched %T, want ToggleHelpMsg", msg)
	}
}

// TestSubmitMsg_CompactRoutesAsPassthrough proves that on a compact-capable
// backend, submitting /compact (with guidance args) dispatches a PassthroughMsg
// carrying the verbatim line and does NOT reach claude via SendMessage
// (QUM-865).
func TestSubmitMsg_CompactRoutesAsPassthrough(t *testing.T) {
	bridge := newFakeSessionBackend()
	bridge.supportsCompact = true
	app := readyRoutingApp(t, bridge)

	_, cmd := app.Update(SubmitMsg{Text: "/compact focus on the code changes"})
	msg := routedMsg(t, cmd)
	pt, ok := msg.(PassthroughMsg)
	if !ok {
		t.Fatalf("SubmitMsg(/compact ...) dispatched %T, want PassthroughMsg", msg)
	}
	if pt.Text != "/compact focus on the code changes" {
		t.Errorf("PassthroughMsg.Text = %q, want verbatim line", pt.Text)
	}
	if bridge.sendCalls != 0 {
		t.Errorf("/compact must not route via SendMessage; sendCalls = %d", bridge.sendCalls)
	}
}

// TestSubmitMsg_CompactFallsThroughWhenUnsupported proves capability gating: on
// a backend that does not advertise /compact, the line is NOT specially routed —
// it falls through to claude as ordinary text (QUM-865 AC6).
func TestSubmitMsg_CompactFallsThroughWhenUnsupported(t *testing.T) {
	bridge := newFakeSessionBackend() // supportsCompact defaults false
	app := readyRoutingApp(t, bridge)

	app.Update(SubmitMsg{Text: "/compact"})
	if bridge.sendCalls != 1 {
		t.Fatalf("unsupported /compact must fall through to claude; sendCalls = %d", bridge.sendCalls)
	}
	if bridge.lastSent != "/compact" {
		t.Errorf("lastSent = %q, want %q (verbatim passthrough as text)", bridge.lastSent, "/compact")
	}
}
