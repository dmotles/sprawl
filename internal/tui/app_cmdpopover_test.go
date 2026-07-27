package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

// TestPopover_ResizeRerendersWithoutCorruption pins the resize AC (QUM-930):
// resizing the terminal while the popover is open re-renders it at the new
// width, in both directions, with the frame intact.
func TestPopover_ResizeRerendersWithoutCorruption(t *testing.T) {
	app := readyRoutingApp(t, newFakeSessionBackend())
	app = typeKey(t, app, '/')
	if !app.cmdPopover.visible(app.input.Value()) {
		t.Fatal("popover should be visible after typing /")
	}
	fullDesc := registryDesc(t, "/usage")

	// Narrow first: the widest description cannot fit, so it must be elided.
	app, _ = updateApp(app, tea.WindowSizeMsg{Width: 60, Height: 40})
	narrow := popoverFromView(t, app, "/handoff")
	assertPopoverBox(t, narrow, observedBoxWidth(t, narrow), len(app.cmdPopover.matches("/"))+2)
	if strings.Contains(narrow, fullDesc) {
		t.Fatalf("precondition: 60 cols cannot fit %q, but it rendered in full:\n%s", fullDesc, narrow)
	}
	if !strings.Contains(narrow, "…") {
		t.Errorf("at 60 cols the overflowing description must be …-elided; popover:\n%s", narrow)
	}
	narrowW := observedBoxWidth(t, narrow)
	if narrowW != 60-4 {
		t.Errorf("box width at 60 cols = %d, want exactly 56 (fill the available width when content overflows)", narrowW)
	}

	// Widen: the box must grow and show the description in full.
	app, _ = updateApp(app, tea.WindowSizeMsg{Width: 200, Height: 50})
	wide := popoverFromView(t, app, "/handoff")
	assertPopoverBox(t, wide, observedBoxWidth(t, wide), len(app.cmdPopover.matches("/"))+2)
	if w := observedBoxWidth(t, wide); w <= narrowW {
		t.Errorf("box width after widening = %d, want >%d", w, narrowW)
	}
	if !strings.Contains(wide, fullDesc) {
		t.Errorf("after widening to 200 cols the popover should show %q in full; popover:\n%s", fullDesc, wide)
	}

	// Shrink back: re-elides, frame still intact (shrinking is the direction
	// that used to clip the border away).
	app, _ = updateApp(app, tea.WindowSizeMsg{Width: 60, Height: 40})
	again := popoverFromView(t, app, "/handoff")
	assertPopoverBox(t, again, narrowW, len(app.cmdPopover.matches("/"))+2)
	if strings.Contains(again, fullDesc) {
		t.Errorf("after shrinking back to 60 cols the description must be elided again; popover:\n%s", again)
	}
}

// sliceCols returns the display columns [from, from+w) of an ANSI-free line,
// padding with spaces if the line is short.
func sliceCols(line string, from, w int) string {
	out := ansi.Cut(line, from, from+w)
	if pad := w - ansi.StringWidth(out); pad > 0 {
		out += strings.Repeat(" ", pad)
	}
	return out
}

// popoverFromView extracts the popover box out of the fully composited app view,
// so assertions run against what the terminal actually receives (including the
// overlayBottomLeft/compositeLeft composite). It anchors on the LAST rounded box
// containing mustContain — the view also carries the SPRAWL wordmark (whose art
// begins with ╭ and whose second line begins with ╰), toasts, and the tree HUD,
// all rounded boxes composited above the popover.
func popoverFromView(t *testing.T, app AppModel, mustContain string) string {
	t.Helper()
	view := ansi.Strip(app.View().Content)
	lines := strings.Split(view, "\n")
	for top := len(lines) - 1; top >= 0; top-- {
		head := strings.TrimLeft(lines[top], " ")
		indent := ansi.StringWidth(lines[top]) - ansi.StringWidth(head)
		if !strings.HasPrefix(head, "╭") {
			continue
		}
		// Width from the closing glyph when present; when the border has been
		// clipped away (the QUM-930 defect) fall back to the visible run so the
		// breakage is REPORTED rather than mistaken for "no popover found".
		w := ansi.StringWidth(strings.TrimRight(head, " "))
		if end := strings.Index(head, "╮"); end >= 0 {
			w = ansi.StringWidth(head[:end]) + 1
		}
		var out []string
		for i := top; i < len(lines); i++ {
			row := sliceCols(lines[i], indent, w)
			out = append(out, row)
			if strings.HasPrefix(row, "╰") {
				break
			}
		}
		block := strings.Join(out, "\n")
		if strings.Contains(block, mustContain) {
			return block
		}
	}
	t.Fatalf("no popover box containing %q found in composited view:\n%s", mustContain, view)
	return ""
}
