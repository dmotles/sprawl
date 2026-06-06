// QUM-669 step 5-6 tests: resync-from-session-log + manual Ctrl+L
// short-circuit + quit-from-wedged-state regression. RED-phase TDD —
// AppModel does not yet reduce ViewportResyncMsg or honor Ctrl+L for resync.
// Stubs (gapDebounceWindow, gapBurstThreshold, ViewportResyncMsg,
// gapConfirmMsg, SetResyncPill) live in app.go / messages.go / statusbar.go
// so the tests compile; behavior must be implemented per
// docs/designs/qum-669-viewport-wedge-recovery.md §2.4 – §2.7.

package tui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/memory"
)

// writeRootSessionFixture stages a Claude session JSONL for the *root* agent
// (weave) at the path memory.SessionLogPath(homeDir, sprawlRoot, sessionID).
// Mirrors writeChildSessionFixture but skips the AgentState scaffolding —
// the root resync path keys off the bridge.SessionID() + sprawlRoot rather
// than a state file.
func writeRootSessionFixture(t *testing.T, sessionID string, jsonlLines []string) (sprawlRoot, homeDir string) {
	t.Helper()
	sprawlRoot = t.TempDir()
	homeDir = t.TempDir()
	path := memory.SessionLogPath(homeDir, sprawlRoot, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := strings.Join(jsonlLines, "\n")
	if len(jsonlLines) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return sprawlRoot, homeDir
}

// copyFixtureToSessionLog writes the canonical QUM-669 resync fixture
// (testdata/qum669-resync-fixture.jsonl) to the canonical session log path
// for the given session ID. Returns the staged sprawlRoot + homeDir.
func copyFixtureToSessionLog(t *testing.T, sessionID string) (sprawlRoot, homeDir string) {
	t.Helper()
	src := filepath.Join("testdata", "qum669-resync-fixture.jsonl")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("ReadFile fixture: %v", err)
	}
	sprawlRoot = t.TempDir()
	homeDir = t.TempDir()
	dst := memory.SessionLogPath(homeDir, sprawlRoot, sessionID)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return sprawlRoot, homeDir
}

// Post QUM-693: MessageStatus / MessageBanner entries never enter the
// ChatList, so the previous "strip path-specific status markers" helper has
// no items to filter — the comparison is over the raw item list.

func TestAppModel_ViewportResyncMsg_ReplacesMessagesAndAppendsBanner(t *testing.T) {
	fake := newFakeSessionBackend()
	fake.SetContinuous(true)
	m := NewAppModel("colour212", "testrepo", "v0.1.0", fake, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := updated.(AppModel)
	app.setTurnState(TurnStreaming) // simulate the wedged state

	rebuilt := []MessageEntry{
		{Type: MessageUser, Content: "rebuilt user", Complete: true},
		{Type: MessageAssistant, Content: "rebuilt assistant", Complete: true},
	}
	resynced, _ := app.Update(ViewportResyncMsg{Entries: rebuilt, MissingCount: 7, Err: nil})
	next := resynced.(AppModel)

	msgs := next.rootVP().ChatList().Items()
	// SetMessages should have installed the rebuilt entries — at least the
	// two we supplied should appear in order.
	foundUser, foundAssistant := false, false
	for _, it := range msgs {
		if u, ok := it.(*UserItem); ok && u.Text() == "rebuilt user" {
			foundUser = true
		}
		if a, ok := it.(*AssistantTextItem); ok && a.Text() == "rebuilt assistant" {
			foundAssistant = true
		}
	}
	if !foundUser || !foundAssistant {
		t.Errorf("rebuilt entries not installed: foundUser=%v foundAssistant=%v; msgs=%+v", foundUser, foundAssistant, msgs)
	}

	// QUM-675 S5: trailing banner now lives on the statusbar transient label.
	bannerRE := regexp.MustCompile(`(?i)resynced.*recovered\s+7\s+(events|messages).*session log`)
	if !bannerRE.MatchString(stripAnsi(next.statusBar.View())) {
		t.Errorf("expected trailing resync banner matching %q on statusbar; got: %s", bannerRE.String(), stripAnsi(next.statusBar.View()))
	}

	if next.turnState != TurnIdle {
		t.Errorf("turnState after successful resync = %v, want TurnIdle", next.turnState)
	}
}

func TestAppModel_ViewportResyncMsg_FailurePathKeepsDroppedState(t *testing.T) {
	fake := newFakeSessionBackend()
	fake.SetContinuous(true)
	m := NewAppModel("colour212", "testrepo", "v0.1.0", fake, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := updated.(AppModel)

	failed, _ := app.Update(ViewportResyncMsg{Err: errors.New("boom")})
	next := failed.(AppModel)

	// QUM-675 S5: resync failures now escalate to the γ overlay instead of
	// appending a banner to the viewport.
	if !next.showError {
		t.Errorf("expected γ overlay (showError=true) on resync failure; got showError=false")
	}
	// QUM-693: MessageError can never enter ChatList — the negative
	// assertion is structurally vacuous and was deleted.
}

// TestAppModel_ResyncFromSessionLog_MatchesNonDroppedRender is the gold
// equivalence test from design §4.4: the viewport produced by a resync must
// be byte-identical to the viewport produced by the normal resume-replay path
// reading the same JSONL — modulo the path-specific trailing status marker
// ("Resumed from prior session" on resume, "✓ resynced — recovered ..." on
// resync).
func TestAppModel_ResyncFromSessionLog_MatchesNonDroppedRender(t *testing.T) {
	const sessionID = "sid-gold"
	sprawlRoot, homeDir := copyFixtureToSessionLog(t, sessionID)

	// Path A — resume-replay gold. LoadTranscript over the fixture, then
	// PreloadTranscript into a fresh AppModel.
	logPath := memory.SessionLogPath(homeDir, sprawlRoot, sessionID)
	goldEntries, err := LoadTranscript(logPath, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	if len(goldEntries) == 0 {
		t.Fatalf("LoadTranscript returned no entries; fixture not staged correctly at %s", logPath)
	}
	mGold := NewAppModel("colour212", "testrepo", "v0.1.0", nil, nil, sprawlRoot, nil)
	mGold.SetHomeDir(homeDir)
	uGold, _ := mGold.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	mGold = uGold.(AppModel)
	mGold.PreloadTranscript(goldEntries)
	goldRender := mGold.rootVP().ChatList().Items()

	// Path B — resync path. Burst-threshold gap, drive the produced cmd,
	// feed the ViewportResyncMsg back through Update.
	fake := newFakeSessionBackend()
	fake.SetContinuous(true)
	fake.SetSessionID(sessionID)
	mResync := NewAppModel("colour212", "testrepo", "v0.1.0", fake, nil, sprawlRoot, nil)
	mResync.SetHomeDir(homeDir)
	uResync, _ := mResync.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	mResync = uResync.(AppModel)
	mResync.setTurnState(TurnStreaming)

	missing := gapBurstThreshold + 5
	stepped, cmd := mResync.Update(EventDropDetectedMsg{From: 1, To: 1 + missing + 1, Missing: missing})
	mResync = stepped.(AppModel)
	resync, ok := findViewportResync(t, cmd)
	if !ok {
		t.Fatalf("burst gap did not produce ViewportResyncMsg; cannot equivalence-check")
	}
	applied, _ := mResync.Update(resync)
	mResync = applied.(AppModel)
	// QUM-693: Status / Banner / Error entries never enter ChatList, so
	// there is no path-specific marker to strip. The comparison is over the
	// raw item list from both paths.
	resyncRender := mResync.rootVP().ChatList().Items()

	// Item-level equivalence (design §4.4 gold check). Compare the
	// fingerprint of each item (concrete-type tag + content-bearing fields)
	// rather than the rendered string so width-dependent layout drift does
	// not break the comparison.
	goldFP := itemFingerprints(goldRender)
	resyncFP := itemFingerprints(resyncRender)
	if !reflect.DeepEqual(resyncFP, goldFP) {
		t.Errorf("resync items do not match gold items\n\npath A (gold):\n%+v\n\npath B (resync):\n%+v", goldFP, resyncFP)
	}
}

// itemFingerprints returns a comparable per-item digest used by the gold
// equivalence check. Captures concrete-type and content-bearing fields only;
// theme/renderer context pointers are intentionally excluded so two
// independently-constructed AppModels produce equal fingerprints.
func itemFingerprints(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		switch v := it.(type) {
		case *UserItem:
			out = append(out, "user:"+v.Text())
		case *AssistantTextItem:
			out = append(out, "assistant:"+v.Text())
		case *ToolCallItem:
			out = append(out, "tool:"+v.ToolID()+":"+v.Name()+":"+v.Input()+":"+v.Result())
		case *SystemNotificationItem:
			out = append(out, "notif:"+v.NotificationType()+":"+v.Content())
		case *AutoTriggerItem:
			out = append(out, "auto:"+v.Summary())
		case *ThinkingItem:
			out = append(out, "thinking")
		default:
			out = append(out, "other")
		}
	}
	return out
}

func TestAppModel_CtrlL_TriggersImmediateResync(t *testing.T) {
	const sessionID = "sid-ctrl-l"
	sprawlRoot, homeDir := copyFixtureToSessionLog(t, sessionID)

	fake := newFakeSessionBackend()
	fake.SetContinuous(true)
	fake.SetSessionID(sessionID)
	m := NewAppModel("colour212", "testrepo", "v0.1.0", fake, nil, sprawlRoot, nil)
	m.SetHomeDir(homeDir)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := updated.(AppModel)

	// No prior gap detected — Ctrl+L is the manual short-circuit (design §2.5).
	_, cmd := app.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	resync, ok := findViewportResync(t, cmd)
	if !ok {
		t.Fatalf("Ctrl+L did not produce a ViewportResyncMsg; manual short-circuit not wired")
	}
	if resync.Err != nil {
		t.Errorf("ViewportResyncMsg.Err = %v, want nil", resync.Err)
	}
	if len(resync.Entries) == 0 {
		t.Errorf("ViewportResyncMsg.Entries is empty; fixture should have hydrated entries")
	}
}

func TestAppModel_CtrlL_DoesNotConflictWithReverseSearch(t *testing.T) {
	app, _ := newAppForDropTest(t)

	// Ctrl+R from PanelInput enters reverse-search (existing behavior; see
	// app.go:368). It must remain intact after Ctrl+L is wired.
	rUpdated, _ := app.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	app = rUpdated.(AppModel)
	if !app.searchActive {
		t.Fatalf("Ctrl+R from PanelInput did not enter searchActive state")
	}

	// While search is active, Ctrl+L MUST NOT punch through to the resync
	// path — search owns keys. The reverse-search overlay's reducer is free
	// to do whatever it wants with the keystroke, but no ViewportResyncMsg
	// may escape.
	updated, cmd := app.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	app = updated.(AppModel)
	if _, ok := findViewportResync(t, cmd); ok {
		t.Errorf("Ctrl+L during reverse-search produced a ViewportResyncMsg; precedence is wrong")
	}
	// Positive precedence assertion: the search reducer must still own this
	// keystroke. Concretely, searchActive must remain true (Ctrl+L did not
	// dismiss the overlay or otherwise bypass search). This pins the
	// "search-first" ordering so a future refactor that drops the guard but
	// happens to not emit a resync msg still fails.
	if !app.searchActive {
		t.Errorf("Ctrl+L during reverse-search dropped searchActive; key was not consumed by the search reducer")
	}
}

// TestAppModel_GapDetected_ForcesTurnIdleEvenIfStreaming is the AC #4
// invariant: a gap-detection event must always knock turnState back down to
// TurnIdle so the spinner can't get stuck ticking on a pending tool entry.
// Design §2.7.
func TestAppModel_GapDetected_ForcesTurnIdleEvenIfStreaming(t *testing.T) {
	app, _ := newAppForDropTest(t)
	app.setTurnState(TurnStreaming)

	dropped, _ := app.Update(EventDropDetectedMsg{From: 1, To: 5, Missing: 3})
	next := dropped.(AppModel)
	if next.turnState != TurnIdle {
		t.Errorf("turnState after EventDropDetectedMsg = %v, want TurnIdle (AC #4)", next.turnState)
	}
}

// TestAppModel_QuitFromStreaming_RaisesConfirmDialog proves Ctrl+C is not
// gated on turnState. From a TurnStreaming state with empty input, Ctrl+C
// must raise the quit-confirm dialog, and the dialog's accept path ('y')
// must result in a tea.Quit cmd. Design §2.7 / AC #4.
func TestAppModel_QuitFromStreaming_RaisesConfirmDialog(t *testing.T) {
	app, _ := newAppForDropTest(t)
	app.setTurnState(TurnStreaming)
	app.input.SetValue("")

	updated, _ := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = updated.(AppModel)
	if !app.showConfirm {
		t.Fatalf("Ctrl+C from TurnStreaming with empty input did not raise the quit-confirm dialog")
	}

	// Press 'y' to accept the dialog. The confirm reducer emits a
	// ConfirmResultMsg{Confirmed: true} via its cmd; feeding that back
	// through Update must yield tea.Quit.
	updated, cmd := app.Update(tea.KeyPressMsg{Code: 'y'})
	app = updated.(AppModel)
	if cmd == nil {
		t.Fatalf("'y' key on confirm dialog produced no cmd; expected ConfirmResultMsg emitter")
	}
	produced := cmd()
	result, ok := produced.(ConfirmResultMsg)
	if !ok {
		t.Fatalf("'y' on confirm dialog produced %T, want ConfirmResultMsg", produced)
	}
	if !result.Confirmed {
		t.Fatalf("ConfirmResultMsg.Confirmed = false; 'y' should accept")
	}
	_, quitCmd := app.Update(result)
	if quitCmd == nil {
		t.Fatalf("ConfirmResultMsg{Confirmed:true} produced no cmd; expected tea.Quit")
	}
	quitMsg := quitCmd()
	if _, ok := quitMsg.(tea.QuitMsg); !ok {
		t.Errorf("ConfirmResultMsg{Confirmed:true} cmd produced %T (%v), want tea.QuitMsg", quitMsg, quitMsg)
	}
}
