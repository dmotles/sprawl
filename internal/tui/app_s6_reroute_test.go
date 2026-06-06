package tui

// QUM-676 S6 — RED-phase tests for the two S5-residue reroute sites tower
// flagged for absorption into S6.
//
//  1. replay.LoadTranscript emits MessageStatus markers for both
//     "earlier messages truncated" and "Resumed from prior session".
//     Per chatlist-invariants §10 these become status-bar transient text;
//     they MUST NOT come back in the returned MessageEntry slice as
//     MessageStatus entries.
//
//  2. The ChildTranscriptMsg "empty entries" arm in app.go (~L1781) appends a
//     "Waiting for X to start..." MessageStatus entry to the child's
//     AgentBuffer. That entry must move to the status-bar transient label.
//
// Both reroutes mirror the existing SetRestartLabel / SetTransientLabel
// pattern in statusbar.go. The test asserts:
//  - the user-visible text appears on statusBar.View()
//  - the underlying viewport buffer no longer carries a MessageStatus with
//    the same text (no double-rendering / no contract violator residue).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// --- A. replay.LoadTranscript reroutes ------------------------------------

// TestLoadTranscript_TruncatedMarker_NotInEntries asserts that when the
// transcript exceeds maxMessages, the returned entries do NOT include a
// leading MessageStatus "earlier messages truncated" marker. After S6 this
// signal is delivered via a side-channel (status-bar transient text installed
// by the caller of LoadTranscript), not woven into the MessageEntry slice.
func TestLoadTranscript_TruncatedMarker_NotInEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	// Write more entries than the cap so truncation kicks in.
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, `{"type":"user","message":{"content":"u"}}`)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	entries, err := LoadTranscript(path, 3)
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	for _, e := range entries {
		if e.Type == MessageStatus && strings.Contains(e.Content, "earlier messages truncated") {
			t.Errorf("LoadTranscript returned a MessageStatus 'earlier messages truncated' entry — S6 reroutes this to status-bar transient text and removes it from the entry slice. Got: %+v", e)
		}
	}
}

// TestLoadTranscript_ResumedMarker_NotInEntries asserts the same for the
// trailing "Resumed from prior session" status marker.
func TestLoadTranscript_ResumedMarker_NotInEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	body := `{"type":"user","message":{"content":"u"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	entries, err := LoadTranscript(path, 0)
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	for _, e := range entries {
		if e.Type == MessageStatus && strings.Contains(e.Content, "Resumed from prior session") {
			t.Errorf("LoadTranscript returned a MessageStatus 'Resumed from prior session' entry — S6 reroutes this to status-bar transient text and removes it from the entry slice. Got: %+v", e)
		}
	}
}

// TestLoadChildTranscript_TruncatedMarker_NotInEntries asserts the sibling
// reroute applies to LoadChildTranscript's truncation marker too.
func TestLoadChildTranscript_TruncatedMarker_NotInEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "child.jsonl")
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, `{"type":"user","message":{"content":"u"}}`)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	entries, err := LoadChildTranscript(path, time.Time{}, 3)
	if err != nil {
		t.Fatalf("LoadChildTranscript: %v", err)
	}
	for _, e := range entries {
		if e.Type == MessageStatus && strings.Contains(e.Content, "earlier messages truncated") {
			t.Errorf("LoadChildTranscript returned a MessageStatus truncation marker; S6 reroutes this to status-bar transient text. Got: %+v", e)
		}
	}
}

// --- B. ChildTranscriptMsg empty-arm "Waiting for X..." reroute -----------

// TestChildTranscriptMsg_EmptyEntries_RoutesWaitingBannerToStatusBar asserts
// that when ChildTranscriptMsg arrives with no entries (the child's session
// log is not on disk yet), the resulting "Waiting for X to start..." banner
// appears on the status bar — NOT as a MessageStatus entry inside the
// child's AgentBuffer viewport.
func TestChildTranscriptMsg_EmptyEntries_RoutesWaitingBannerToStatusBar(t *testing.T) {
	app := readyApp(t)

	// Simulate the empty-arm default case: ChildTranscriptMsg with no entries.
	const childName = "alice"
	updated, _ := app.Update(ChildTranscriptMsg{
		Agent:     childName,
		Entries:   nil,
		SessionID: "sid-x",
	})
	app = updated.(AppModel)

	const want = "Waiting for " + childName + " to start"
	// Positive assertion: the banner reaches the status bar.
	view := stripAnsi(app.statusBar.View())
	if !strings.Contains(view, want) {
		t.Errorf("status bar should contain %q after empty ChildTranscriptMsg; got:\n%s", want, view)
	}

	// QUM-693: MessageStatus can never enter ChatList — the negative
	// assertion is structurally vacuous and was deleted.
}

// TestChildTranscriptMsg_NonEmpty_ClearsWaitingBanner asserts the recovery
// edge: once the child's first real backfill lands, the waiting banner
// disappears from the status bar (idempotency / cleanup).
func TestChildTranscriptMsg_NonEmpty_ClearsWaitingBanner(t *testing.T) {
	app := readyApp(t)
	const childName = "alice"

	// First: empty arm to install the banner.
	updated, _ := app.Update(ChildTranscriptMsg{Agent: childName, Entries: nil, SessionID: "sid"})
	app = updated.(AppModel)
	if !strings.Contains(stripAnsi(app.statusBar.View()), "Waiting for "+childName) {
		t.Fatalf("precondition: status bar should carry waiting banner; got:\n%s", stripAnsi(app.statusBar.View()))
	}

	// Then: a real backfill landing.
	updated, _ = app.Update(ChildTranscriptMsg{
		Agent:     childName,
		SessionID: "sid",
		Entries: []MessageEntry{
			{Type: MessageUser, Content: "first turn", Complete: true},
		},
	})
	app = updated.(AppModel)

	// Precise accessor: the transient label slot must be empty (cleared).
	// Substring-on-View() would false-pass if the banner text moved to a
	// different segment but the transient slot still carried stale content.
	if got := app.statusBar.TransientLabel(); got != "" {
		t.Errorf("expected transient label cleared after non-empty ChildTranscriptMsg, got %q", got)
	}
}

// keep tea import referenced even if the other tests don't use it directly.
var _ = tea.WindowSizeMsg{}
