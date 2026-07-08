package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/tui/commands"
)

// typeKey feeds a single printable rune to the app as a KeyPressMsg.
func typeKey(t *testing.T, app AppModel, r rune) AppModel {
	t.Helper()
	updated, _ := app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	return updated.(AppModel)
}

// updateApp applies a msg and returns the concrete AppModel + cmd.
func updateApp(app AppModel, msg tea.Msg) (AppModel, tea.Cmd) {
	updated, cmd := app.Update(msg)
	return updated.(AppModel), cmd
}

func TestPopover_SlashShowsInlineSuggestions(t *testing.T) {
	bridge := newFakeSessionBackend()
	app := readyRoutingApp(t, bridge)
	app = typeKey(t, app, '/')
	if app.input.Value() != "/" {
		t.Fatalf("input value = %q, want %q ('/' inserted literally, no palette)", app.input.Value(), "/")
	}
	if !app.cmdPopover.visible(app.input.Value()) {
		t.Fatal("popover should be visible after typing /")
	}
	view := app.View().Content
	if !strings.Contains(view, "attach") || !strings.Contains(view, "help") {
		t.Errorf("rendered view should list commands inline (attach, help); got:\n%s", view)
	}
	if bridge.sendCalls != 0 {
		t.Errorf("typing / must not reach claude; sendCalls=%d", bridge.sendCalls)
	}
}

func TestPopover_LiveFilterAndAutoHide(t *testing.T) {
	app := readyRoutingApp(t, newFakeSessionBackend())
	app = typeKey(t, app, '/')
	app = typeKey(t, app, 'h')
	if !app.cmdPopover.visible(app.input.Value()) {
		t.Fatalf("popover should stay visible on /h (matches help/handoff); value=%q", app.input.Value())
	}
	// Filtered contents: /h shows help+handoff but not the non-matching /attach.
	view := app.View().Content
	if !strings.Contains(view, "handoff") || !strings.Contains(view, "help") {
		t.Errorf("/h popover should list handoff+help; got:\n%s", view)
	}
	if strings.Contains(view, "/attach") {
		t.Errorf("/h popover must not list non-matching /attach; got:\n%s", view)
	}
	// Type chars that match nothing → auto-hide.
	app = typeKey(t, app, 'z')
	app = typeKey(t, app, 'z')
	if app.cmdPopover.visible(app.input.Value()) {
		t.Errorf("popover should auto-hide when no command matches (%q)", app.input.Value())
	}
	// Backspace back to a matching prefix → reappears (pure function of text).
	app, _ = updateApp(app, tea.KeyPressMsg{Code: tea.KeyBackspace})
	app, _ = updateApp(app, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if !app.cmdPopover.visible(app.input.Value()) {
		t.Errorf("popover should reappear after backspacing to /h; value=%q", app.input.Value())
	}
}

func TestPopover_ArrowsMoveHighlight(t *testing.T) {
	app := readyRoutingApp(t, newFakeSessionBackend())
	app = typeKey(t, app, '/')
	if app.cmdPopover.highlight != 0 {
		t.Fatalf("initial highlight = %d, want 0", app.cmdPopover.highlight)
	}
	app, _ = updateApp(app, tea.KeyPressMsg{Code: tea.KeyDown})
	if app.cmdPopover.highlight != 1 {
		t.Errorf("highlight after Down = %d, want 1", app.cmdPopover.highlight)
	}
	app, _ = updateApp(app, tea.KeyPressMsg{Code: tea.KeyUp})
	if app.cmdPopover.highlight != 0 {
		t.Errorf("highlight after Up = %d, want 0", app.cmdPopover.highlight)
	}
}

func TestPopover_EnterNoArgCommandFires(t *testing.T) {
	bridge := newFakeSessionBackend()
	app := readyRoutingApp(t, bridge)
	// Type "/help" so the sole match is the no-arg /help command.
	for _, r := range "/help" {
		app = typeKey(t, app, r)
	}
	updated, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = updated.(AppModel)
	msg := routedMsg(t, cmd)
	if _, ok := msg.(ToggleHelpMsg); !ok {
		t.Fatalf("Enter on /help dispatched %T, want ToggleHelpMsg", msg)
	}
	if app.input.Value() != "" {
		t.Errorf("input should be cleared after firing no-arg command; got %q", app.input.Value())
	}
	if bridge.sendCalls != 0 {
		t.Errorf("firing /help must not reach claude; sendCalls=%d", bridge.sendCalls)
	}
}

func TestPopover_EnterArgCommandInsertsWithSpace(t *testing.T) {
	bridge := newFakeSessionBackend()
	app := readyRoutingApp(t, bridge)
	for _, r := range "/attach" {
		app = typeKey(t, app, r)
	}
	updated, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = updated.(AppModel)
	if app.input.Value() != "/attach " {
		t.Errorf("input value = %q, want %q (command + trailing space)", app.input.Value(), "/attach ")
	}
	// Must NOT submit — no SubmitMsg anywhere in the (possibly batched) cmd.
	for _, msg := range collectBatchMsgs(t, cmd) {
		if _, ok := msg.(SubmitMsg); ok {
			t.Error("Enter on arg-taking command must NOT submit")
		}
	}
	if bridge.sendCalls != 0 {
		t.Errorf("inserting /attach must not reach claude; sendCalls=%d", bridge.sendCalls)
	}
	// Popover hidden now that the value has a trailing space.
	if app.cmdPopover.visible(app.input.Value()) {
		t.Error("popover should hide after inserting arg-command (whitespace)")
	}
}

func TestPopover_NotAModal_ScrollPassesThrough(t *testing.T) {
	// The popover must NOT gate scroll/mouse like the full-screen palette did.
	app := readyRoutingApp(t, newFakeSessionBackend())
	app = typeKey(t, app, '/')
	if !app.cmdPopover.visible(app.input.Value()) {
		t.Fatal("popover should be visible after /")
	}
	if app.anyModalUp() {
		t.Error("popover must NOT register as a modal (would gate scroll/mouse/paste)")
	}
	// PgUp must not be swallowed by the popover — it stays visible and the key
	// is not consumed as popover navigation.
	before := app.cmdPopover.highlight
	app, _ = updateApp(app, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if app.cmdPopover.highlight != before {
		t.Error("PgUp must not move popover highlight (popover only consumes ↑/↓/Enter/Esc)")
	}
}

func TestPopover_RootOnly_NotRenderedForChildPane(t *testing.T) {
	app := readyRoutingApp(t, newFakeSessionBackend())
	// Observe a non-root agent: the input bar (popover anchor) is hidden.
	app.observedAgent = "child"
	app.input.SetValue("/")
	view := app.View().Content
	// The popover box lists command descriptions; none should appear while a
	// child pane is observed.
	if strings.Contains(view, "Quit sprawl enter") {
		t.Error("popover must not render while observing a non-root agent")
	}
}

func TestPopover_EscThenFreshEntryReappears(t *testing.T) {
	app := readyRoutingApp(t, newFakeSessionBackend())
	app = typeKey(t, app, '/')
	app = typeKey(t, app, 'h')
	app, _ = updateApp(app, tea.KeyPressMsg{Code: tea.KeyEscape})
	if app.cmdPopover.visible(app.input.Value()) {
		t.Fatal("popover should be dismissed after Esc")
	}
	// Abandon the entry (backspace to empty), then a fresh / re-shows.
	app, _ = updateApp(app, tea.KeyPressMsg{Code: tea.KeyBackspace})
	app, _ = updateApp(app, tea.KeyPressMsg{Code: tea.KeyBackspace})
	app = typeKey(t, app, '/')
	if !app.cmdPopover.visible(app.input.Value()) {
		t.Errorf("a fresh / after clearing the entry should re-show the popover; value=%q dismissed=%v", app.input.Value(), app.cmdPopover.escDismissed)
	}
}

func TestPopover_EscDismissesKeepsText(t *testing.T) {
	app := readyRoutingApp(t, newFakeSessionBackend())
	app = typeKey(t, app, '/')
	app = typeKey(t, app, 'h')
	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)
	if app.input.Value() != "/h" {
		t.Errorf("Esc should preserve typed text; got %q, want /h", app.input.Value())
	}
	if app.cmdPopover.visible(app.input.Value()) {
		t.Error("popover should be hidden after Esc")
	}
	// Typing more of the same token stays dismissed (Esc is for this entry).
	app = typeKey(t, app, 'e')
	if app.cmdPopover.visible(app.input.Value()) {
		t.Error("popover should stay dismissed while extending the same /-token after Esc")
	}
}

func TestPickAgentMatch_PrefersExactThenFirst(t *testing.T) {
	// Exact (case-insensitive) name wins even when it isn't first.
	if got := pickAgentMatch("weave", []string{"weaver", "weave"}); got != "weave" {
		t.Errorf("pickAgentMatch exact = %q, want weave", got)
	}
	if got := pickAgentMatch("WEAVE", []string{"weaver", "weave"}); got != "weave" {
		t.Errorf("pickAgentMatch case-insensitive exact = %q, want weave", got)
	}
	// No exact → first (order-stable) fuzzy match.
	if got := pickAgentMatch("we", []string{"weaver", "welder"}); got != "weaver" {
		t.Errorf("pickAgentMatch no-exact = %q, want weaver (first)", got)
	}
}

func TestPopover_SessionRestartClearsEscDismissed(t *testing.T) {
	app := readyRoutingApp(t, newFakeSessionBackend())
	app = typeKey(t, app, '/')
	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)
	if !app.cmdPopover.escDismissed {
		t.Fatal("setup: Esc should latch escDismissed")
	}
	updated, _ = app.Update(SessionRestartingMsg{Reason: "handoff"})
	app = updated.(AppModel)
	if app.cmdPopover.escDismissed {
		t.Error("SessionRestartingMsg should clear the popover escDismissed latch")
	}
}

// TestRouteSlashCommand_CoversEveryRegisteredCommand is the QUM-863 footgun
// guard: every registered command MUST be intercepted by routeSlashCommand so
// none can silently leak to claude as a raw prompt (esp. a new KindUI Action).
// The backend is made compact-capable so capability-gated commands (/compact)
// are covered under the "capability available" assumption (QUM-865).
func TestRouteSlashCommand_CoversEveryRegisteredCommand(t *testing.T) {
	bridge := newFakeSessionBackend()
	bridge.supportsCompact = true
	app := readyRoutingApp(t, bridge)
	for _, c := range commands.All() {
		if _, ok := app.routeSlashCommand(c.Name); !ok {
			t.Errorf("routeSlashCommand(%q) ok=false; command would leak to claude", c.Name)
		}
	}
}

// TestPopover_GatesCompactByCapability proves /compact is offered in the popover
// only when the backend advertises it (QUM-865 AC6). CapNone commands are shown
// regardless.
func TestPopover_GatesCompactByCapability(t *testing.T) {
	hasCompact := func(app AppModel) bool {
		for _, c := range app.cmdPopover.matches("/comp") {
			if c.Name == "/compact" {
				return true
			}
		}
		return false
	}

	capable := newFakeSessionBackend()
	capable.supportsCompact = true
	if !hasCompact(readyRoutingApp(t, capable)) {
		t.Error("compact-capable backend must offer /compact in the popover")
	}

	incapable := newFakeSessionBackend() // supportsCompact defaults false
	if hasCompact(readyRoutingApp(t, incapable)) {
		t.Error("non-capable backend must NOT offer /compact in the popover")
	}
	// A CapNone command is always offered.
	app := readyRoutingApp(t, incapable)
	if len(app.cmdPopover.matches("/help")) == 0 {
		t.Error("CapNone command /help must be offered regardless of capability")
	}
}
