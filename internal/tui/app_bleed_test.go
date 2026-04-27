package tui

// QUM-334 — TDD red-phase tests for the per-agent ViewportModel fix
// (Option A from docs/research/qum-334-bridge-bleed.md).
//
// These tests reference APIs that do NOT yet exist on AppModel /
// ViewportModel:
//   - (*AppModel).viewportFor(name string) *ViewportModel
//   - (*AppModel).rootVP() *ViewportModel
//   - (*AppModel).observedVP() *ViewportModel
//   - (*ViewportModel).Len() int
//   - (*ViewportModel).Width() int
//
// As a result, this file is EXPECTED to fail to compile until the
// implementation lands. That compilation failure IS the red signal — see
// `go build ./internal/tui/...` output captured in the QUM-334 PR.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// newBleedApp builds a sized AppModel with empty sprawlRoot/homeDir suitable
// for in-memory bleed assertions (no on-disk transcript fixtures needed).
func newBleedApp(t *testing.T) AppModel {
	t.Helper()
	return newAppForChildTranscript(t, t.TempDir(), t.TempDir())
}

// cycleTo applies an AgentSelectedMsg and discards any returned cmd. The
// returned AppModel reflects the post-cycle state.
func cycleTo(t *testing.T, app AppModel, name string) AppModel {
	t.Helper()
	updated, _ := app.Update(AgentSelectedMsg{Name: name})
	return updated.(AppModel)
}

// TestBridge_BleedDuringObservation_DoesNotPolluteChildViewport asserts
// that an AssistantTextMsg arriving while the user is observing a child
// agent (finn) lands in weave's per-agent viewport — never finn's.
func TestBridge_BleedDuringObservation_DoesNotPolluteChildViewport(t *testing.T) {
	app := newBleedApp(t)

	// Cycle to finn — implementation lazy-creates an AgentBuffer with a vp.
	app = cycleTo(t, app, "finn")
	if _, ok := app.agentBuffers["finn"]; !ok {
		t.Fatalf("lazy-init failed: agentBuffers[finn] not present after cycle")
	}

	// Hydrate finn's viewport via a ChildTranscriptMsg (matches QUM-332 path).
	finnEntries := []MessageEntry{
		{Type: MessageAssistant, Content: "finn-content", Complete: true},
	}
	updated, _ := app.Update(ChildTranscriptMsg{Agent: "finn", Entries: finnEntries})
	app = updated.(AppModel)

	// Bridge event arrives while observing finn.
	updated, _ = app.Update(AssistantTextMsg{Text: "weave-chunk"})
	app = updated.(AppModel)

	finnVP := app.viewportFor("finn")
	for _, e := range finnVP.GetMessages() {
		if strings.Contains(e.Content, "weave-chunk") {
			t.Fatalf("finn vp polluted with weave-chunk: %+v", finnVP.GetMessages())
		}
	}
	gotFinn := finnVP.GetMessages()
	foundFinnContent := false
	for _, e := range gotFinn {
		if strings.Contains(e.Content, "finn-content") {
			foundFinnContent = true
		}
	}
	if !foundFinnContent {
		t.Errorf("expected finn-content in finn vp; got %+v", gotFinn)
	}

	weaveVP := app.viewportFor("weave")
	foundWeaveChunk := false
	for _, e := range weaveVP.GetMessages() {
		if e.Type == MessageAssistant && strings.Contains(e.Content, "weave-chunk") {
			foundWeaveChunk = true
		}
	}
	if !foundWeaveChunk {
		t.Errorf("expected weave-chunk in weave vp; got %+v", weaveVP.GetMessages())
	}
}

// TestBridge_MidStreamCycle_PreservesAssistantChunks asserts that
// streaming chunks delivered before, during, and after a cycle to a child
// all merge into a single coherent assistant entry on weave's viewport
// (chunk-merge predicate is per-agent, not torn).
func TestBridge_MidStreamCycle_PreservesAssistantChunks(t *testing.T) {
	app := newBleedApp(t)

	updated, _ := app.Update(AssistantTextMsg{Text: "hello "})
	app = updated.(AppModel)
	updated, _ = app.Update(AssistantTextMsg{Text: "world"})
	app = updated.(AppModel)

	app = cycleTo(t, app, "finn")

	updated, _ = app.Update(AssistantTextMsg{Text: "!"})
	app = updated.(AppModel)

	// Finalize the stream: SessionResultMsg with empty Result must not
	// re-append (HasPendingAssistant is true on weave) and must finalize.
	updated, _ = app.Update(SessionResultMsg{Result: "", IsError: false})
	app = updated.(AppModel)

	app = cycleTo(t, app, "weave")

	weaveVP := app.viewportFor("weave")
	msgs := weaveVP.GetMessages()
	assistantCount := 0
	var assistant MessageEntry
	for _, m := range msgs {
		if m.Type == MessageAssistant {
			assistantCount++
			assistant = m
		}
	}
	if assistantCount != 1 {
		t.Fatalf("expected exactly 1 assistant entry on weave vp; got %d (%+v)", assistantCount, msgs)
	}
	if assistant.Content != "hello world!" {
		t.Errorf("assistant.Content = %q, want %q", assistant.Content, "hello world!")
	}
	if !assistant.Complete {
		t.Errorf("assistant entry should be finalized after SessionResultMsg")
	}
	if weaveVP.HasPendingAssistant() {
		t.Errorf("weave vp should have no pending assistant after finalize")
	}

	// finn's vp must never have received any of the streamed chunks.
	finnVP := app.viewportFor("finn")
	if finnVP.Len() != 0 {
		t.Errorf("finn vp should be empty (no bleed); got %+v", finnVP.GetMessages())
	}
}

// TestInbox_ArrivalWhileObservingChild_TargetsWeaveViewport asserts that
// inbox banners (weave-only events) never bleed into the observed child's
// viewport.
func TestInbox_ArrivalWhileObservingChild_TargetsWeaveViewport(t *testing.T) {
	app := newBleedApp(t)
	app = cycleTo(t, app, "finn")

	updated, _ := app.Update(InboxArrivalMsg{From: "alice"})
	app = updated.(AppModel)

	finnVP := app.viewportFor("finn")
	if finnVP.Len() != 0 {
		t.Fatalf("finn vp must remain empty when inbox banner targets weave; got %+v", finnVP.GetMessages())
	}

	weaveVP := app.viewportFor("weave")
	found := false
	for _, e := range weaveVP.GetMessages() {
		if e.Type == MessageStatus && strings.Contains(e.Content, "alice") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected weave vp to contain inbox banner mentioning alice; got %+v", weaveVP.GetMessages())
	}
}

// TestResizePanels_SizesAllAgentBuffers asserts a WindowSizeMsg propagates
// to every cached viewport, not just the rendered one. We exercise this
// by triggering a resize after both weave and finn viewports exist, then
// rendering each — both must produce non-empty output (placeholder or
// content) at the new size.
func TestResizePanels_SizesAllAgentBuffers(t *testing.T) {
	app := newBleedApp(t)
	app = cycleTo(t, app, "finn") // lazy-create finn vp
	app = cycleTo(t, app, "weave")

	updated, _ := app.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	app = updated.(AppModel)

	weaveVP := app.viewportFor("weave")
	finnVP := app.viewportFor("finn")

	// resizePanels must call SetSize on every cached viewport, not just the
	// rendered one. ComputeLayout(200, 60) yields a viewport width; both
	// vps must reflect a non-zero post-resize width that matches.
	wantW := weaveVP.Width()
	if wantW == 0 {
		t.Fatalf("weave vp Width() == 0 after resize; SetSize never fired on root")
	}
	if finnVP.Width() != wantW {
		t.Errorf("finn vp Width() = %d, want %d (resize did not iterate cached vps)", finnVP.Width(), wantW)
	}
}

// TestAgentTreeMsg_RetiredAgent_BufferDropped asserts that when an agent
// disappears from the tree (and is not currently observed), its
// agentBuffers entry is cleaned up to bound memory.
func TestAgentTreeMsg_RetiredAgent_BufferDropped(t *testing.T) {
	app := newBleedApp(t)

	// Establish finn in the tree, observe it, then cycle back so finn is
	// no longer the observedAgent (cleanup must skip observedAgent).
	updated, _ := app.Update(AgentTreeMsg{
		Nodes: []TreeNode{{Name: "finn", Status: "running"}},
	})
	app = updated.(AppModel)
	app = cycleTo(t, app, "finn")
	app = cycleTo(t, app, "weave")

	if _, ok := app.agentBuffers["finn"]; !ok {
		t.Fatalf("precondition: finn buffer should exist after cycle; got %+v", app.agentBuffers)
	}

	// finn retires (no longer present in tree).
	updated, _ = app.Update(AgentTreeMsg{Nodes: nil})
	app = updated.(AppModel)

	if _, ok := app.agentBuffers["finn"]; ok {
		t.Errorf("finn buffer should be dropped on retirement; got %+v", app.agentBuffers)
	}
	if _, ok := app.agentBuffers["weave"]; !ok {
		t.Errorf("weave buffer must NOT be dropped (root agent); got %+v", app.agentBuffers)
	}
}

// TestSessionResultMsg_PendingCheck_UsesWeaveVP asserts that the
// HasPendingAssistant guard in SessionResultMsg consults weave's vp (not
// the rendered/observed one). When weave has a pending chunk "partial"
// and a SessionResultMsg with Result="partial extra" arrives while
// observing finn, the result text must NOT be re-appended (because weave
// already has the streamed text), the assistant entry must finalize, and
// finn's vp must be untouched.
func TestSessionResultMsg_PendingCheck_UsesWeaveVP(t *testing.T) {
	app := newBleedApp(t)
	app = cycleTo(t, app, "finn")

	app.viewportFor("weave").AppendAssistantChunk("partial")

	updated, _ := app.Update(SessionResultMsg{
		Result:       "partial extra",
		IsError:      false,
		DurationMs:   10,
		TotalCostUsd: 0.001,
	})
	app = updated.(AppModel)

	weaveVP := app.viewportFor("weave")
	msgs := weaveVP.GetMessages()
	assistantCount := 0
	var lastAssistant MessageEntry
	for _, m := range msgs {
		if m.Type == MessageAssistant {
			assistantCount++
			lastAssistant = m
		}
	}
	if assistantCount != 1 {
		t.Fatalf("expected 1 assistant entry on weave vp (no re-append); got %d (%+v)", assistantCount, msgs)
	}
	if lastAssistant.Content != "partial" {
		t.Errorf("weave assistant.Content = %q, want %q (Result must NOT have been appended)", lastAssistant.Content, "partial")
	}
	if !lastAssistant.Complete {
		t.Errorf("weave assistant entry should be finalized after SessionResultMsg")
	}
	if weaveVP.HasPendingAssistant() {
		t.Errorf("weave vp must not report pending after finalize")
	}

	finnVP := app.viewportFor("finn")
	if finnVP.Len() != 0 {
		t.Errorf("finn vp should be untouched; got %+v", finnVP.GetMessages())
	}
	if finnVP.HasPendingAssistant() {
		t.Errorf("finn vp should not have pending assistant; SessionResultMsg must target rootVP")
	}
}

// TestPreloadTranscript_TargetsRootViewport asserts that PreloadTranscript
// always writes to weave's viewport, even when the user has cycled to a
// child before the resume preload runs.
func TestPreloadTranscript_TargetsRootViewport(t *testing.T) {
	app := newBleedApp(t)
	app = cycleTo(t, app, "finn")

	entries := []MessageEntry{
		{Type: MessageAssistant, Content: "preloaded", Complete: true},
	}
	app.PreloadTranscript(entries)

	weaveVP := app.viewportFor("weave")
	wMsgs := weaveVP.GetMessages()
	found := false
	for _, e := range wMsgs {
		if e.Content == "preloaded" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected preloaded entry in weave vp; got %+v", wMsgs)
	}

	finnVP := app.viewportFor("finn")
	if finnVP.Len() != 0 {
		t.Errorf("finn vp should be empty after PreloadTranscript; got %+v", finnVP.GetMessages())
	}
}

// TestSessionRestartingMsg_TargetsWeaveViewport asserts the restart banner
// (a weave-only TUI annotation) lands on weave's vp regardless of which
// agent is observed.
func TestSessionRestartingMsg_TargetsWeaveViewport(t *testing.T) {
	app := newBleedApp(t)
	app = cycleTo(t, app, "finn")

	updated, _ := app.Update(SessionRestartingMsg{})
	app = updated.(AppModel)

	finnVP := app.viewportFor("finn")
	if finnVP.Len() != 0 {
		t.Fatalf("finn vp must not receive restart banner; got %+v", finnVP.GetMessages())
	}

	weaveVP := app.viewportFor("weave")
	found := false
	for _, e := range weaveVP.GetMessages() {
		if e.Type == MessageStatus && strings.Contains(strings.ToLower(e.Content), "restart") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected weave vp to contain restart banner; got %+v", weaveVP.GetMessages())
	}
}
