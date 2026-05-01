// Tests for QUM-439: stream child viewport from UnifiedRuntime EventBus
// instead of polling JSONL.
//
// These tests are written before implementation (TDD red phase). They are
// expected to FAIL TO COMPILE until the implementation lands. The symbols
// they reference but which do not yet exist:
//
//   - tui.ChildStreamMsg{Agent, Epoch, Inner}
//   - AppModel.childAdapter, AppModel.childAdapterAgent, AppModel.childAdapterEpoch
//     (or equivalent accessors below)
//   - supervisor.AgentRuntime.UnifiedRuntime() *runtime.UnifiedRuntime
//   - supervisor.AttachUnifiedRuntimeForTest(rt *AgentRuntime, urt *runtime.UnifiedRuntime)
//     test seam (place it in internal/supervisor/runtime_test_export.go).
//
// Read the QUM-439 issue body and the implementation plan in the worktree
// CLAUDE notes for context.
package tui

import (
	"context"
	"runtime"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// -- test doubles ------------------------------------------------------------

// noopSession is a minimal SessionHandle good enough to construct a real
// UnifiedRuntime. We never Start() the runtime in most tests; we only need
// its EventBus to publish synthetic events the adapter will translate.
type noopSession struct{}

func (noopSession) StartTurn(_ context.Context, _ string, _ ...backend.TurnSpec) (<-chan *protocol.Message, error) {
	ch := make(chan *protocol.Message)
	close(ch)
	return ch, nil
}

func (noopSession) Interrupt(_ context.Context) error { return nil }

// supervisorWithRegistry extends mockSupervisor with a real RuntimeRegistry
// the AppModel can consult.
type supervisorWithRegistry struct {
	mockSupervisor
	reg *supervisor.RuntimeRegistry
}

func (s *supervisorWithRegistry) RuntimeRegistry() *supervisor.RuntimeRegistry {
	return s.reg
}

// newUnifiedRT builds a fresh, un-started UnifiedRuntime suitable for
// publishing synthetic events on its EventBus.
func newUnifiedRT(t *testing.T, name string) *sprawlrt.UnifiedRuntime {
	t.Helper()
	return sprawlrt.New(sprawlrt.RuntimeConfig{
		Name:    name,
		Session: noopSession{},
	})
}

// registerUnified seeds the registry with an AgentRuntime backed by a unified
// runtime so AppModel.UnifiedRuntime(name) resolves.
func registerUnified(t *testing.T, reg *supervisor.RuntimeRegistry, name string, urt *sprawlrt.UnifiedRuntime) *supervisor.AgentRuntime {
	t.Helper()
	rt := reg.Ensure(supervisor.AgentRuntimeConfig{
		Agent: &state.AgentState{Name: name, Type: "engineer"},
	})
	// AttachUnifiedRuntimeForTest is a test-only seam to be added in
	// internal/supervisor (e.g. runtime_test_export.go) that installs a
	// unified-handle equivalent so AgentRuntime.UnifiedRuntime() returns urt.
	supervisor.AttachUnifiedRuntimeForTest(t, rt, urt)
	return rt
}

// registerLegacy seeds the registry with an AgentRuntime that has NO unified
// runtime (legacy / unstarted handle path).
func registerLegacy(t *testing.T, reg *supervisor.RuntimeRegistry, name string) *supervisor.AgentRuntime {
	t.Helper()
	return reg.Ensure(supervisor.AgentRuntimeConfig{
		Agent: &state.AgentState{Name: name, Type: "engineer"},
	})
}

func newAppWithRegistry(t *testing.T, sup supervisor.Supervisor) AppModel {
	t.Helper()
	sprawlRoot := t.TempDir()
	homeDir := t.TempDir()
	m := NewAppModel("colour212", "testrepo", "v0.1.0", nil, sup, sprawlRoot, nil)
	m.SetHomeDir(homeDir)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return resized.(AppModel)
}

// findChildStreamWaitCmd looks for a ChildStreamMsg-bearing tea.Cmd by
// invoking the cmd in a goroutine and inspecting the result type.
//
// We can't synchronously call WaitForEvent commands (they block). Instead we
// inspect for a marker: implementations are expected to wrap the adapter's
// WaitForEvent in a closure that, when run, eventually returns a
// ChildStreamMsg. For the AgentSelected tests we don't need to *run* it; we
// only need to detect that a streaming-cmd was scheduled. We approximate this
// by scanning the batch and asserting the presence of a non-nil cmd that is
// neither the legacy poll cmd nor the activity-tick cmd. Callers cross-check
// with stronger behavioural assertions where possible.

// hasChildTranscriptMsg returns true if the batch contains an immediate
// ChildTranscriptMsg (the backfill path).
func hasChildTranscriptMsg(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	msgs := collectBatchMsgsAllowAsync(t, cmd, 100*time.Millisecond)
	for _, m := range msgs {
		if _, ok := m.(ChildTranscriptMsg); ok {
			return true
		}
	}
	return false
}

// collectBatchMsgsAllowAsync invokes a cmd and recursively expands batches,
// allowing each leaf cmd up to deadline before giving up. Cmds that don't
// return within the deadline are skipped (treated as "still waiting").
func collectBatchMsgsAllowAsync(t *testing.T, cmd tea.Cmd, deadline time.Duration) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	out := make(chan tea.Msg, 1)
	go func() { out <- cmd() }()
	select {
	case raw := <-out:
		if batch, ok := raw.(tea.BatchMsg); ok {
			var all []tea.Msg
			for _, c := range batch {
				all = append(all, collectBatchMsgsAllowAsync(t, c, deadline)...)
			}
			return all
		}
		if raw == nil {
			return nil
		}
		return []tea.Msg{raw}
	case <-time.After(deadline):
		// Cmd is still blocked (likely WaitForEvent). That's fine — the test
		// just wanted to know what *immediate* msgs are produced.
		return nil
	}
}

// -- 1. AgentSelected unified path: starts adapter, seeds backfill, no poll --

func TestAgentSelected_UnifiedPath_StartsAdapterAndSeeds(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)

	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	updated, cmd := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)

	if cmd == nil {
		t.Fatal("AgentSelectedMsg{alice} returned nil cmd; expected backfill + WaitForEvent batch")
	}

	// Backfill ChildTranscriptMsg must be scheduled.
	if !hasChildTranscriptMsg(t, cmd) {
		t.Errorf("expected ChildTranscriptMsg backfill in batch for unified path")
	}

	// Adapter must be installed and pointed at alice.
	if app.ChildAdapter() == nil {
		t.Fatalf("AppModel.ChildAdapter() should be non-nil after unified AgentSelected")
	}
	if got := app.ChildAdapterAgent(); got != "alice" {
		t.Errorf("ChildAdapterAgent() = %q, want %q", got, "alice")
	}
	if app.ChildAdapterEpoch() == 0 {
		t.Errorf("ChildAdapterEpoch() should be > 0 after first unified select")
	}

	// scheduleChildTranscriptTick must NOT be in the batch — the unified
	// path replaces polling. We can't easily disambiguate "tick cmd" from
	// "WaitForEvent cmd" by type alone, so assert behaviorally: the only
	// immediately-resolving msg should be ChildTranscriptMsg (and possibly
	// activity-tick). A scheduleChildTranscriptTick would resolve to a
	// ChildTranscriptMsg only after the configured interval; with
	// SetChildTranscriptTick unset we'd be at defaultChildTranscriptTick.
	// If we set the tick interval extremely short, a poll cmd would emit a
	// second ChildTranscriptMsg.
	app.SetChildTranscriptTick(5 * time.Millisecond)
	updated2, cmd2 := app.Update(AgentSelectedMsg{Name: "alice"})
	_ = updated2
	msgs := collectBatchMsgsAllowAsync(t, cmd2, 60*time.Millisecond)
	count := 0
	for _, m := range msgs {
		if _, ok := m.(ChildTranscriptMsg); ok {
			count++
		}
	}
	// Exactly ONE ChildTranscriptMsg (the backfill); a polling tick would
	// add a second.
	if count > 1 {
		t.Errorf("got %d ChildTranscriptMsg; unified path should not also schedule a poll tick", count)
	}
}

// -- 2. Legacy handle keeps polling -----------------------------------------

func TestAgentSelected_LegacyHandle_KeepsPolling(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	registerLegacy(t, reg, "bob")
	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	// Persist a state file so the legacy backfill path produces something.
	if err := state.SaveAgent(app.sprawlRoot, &state.AgentState{
		Name: "bob", Type: "engineer", Status: "running",
	}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	updated, cmd := app.Update(AgentSelectedMsg{Name: "bob"})
	app = updated.(AppModel)
	if cmd == nil {
		t.Fatal("legacy AgentSelectedMsg returned nil cmd; expected backfill + tick")
	}
	if !hasChildTranscriptMsg(t, cmd) {
		t.Errorf("legacy path must dispatch ChildTranscriptMsg backfill")
	}
	if app.ChildAdapter() != nil {
		t.Errorf("legacy path must not install a child adapter; got %v", app.ChildAdapter())
	}
}

// -- 3. RegistryMiss keeps polling ------------------------------------------

func TestAgentSelected_RegistryMiss_KeepsPolling(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry() // empty
	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	updated, cmd := app.Update(AgentSelectedMsg{Name: "ghost"})
	app = updated.(AppModel)
	if cmd == nil {
		t.Fatal("registry-miss AgentSelectedMsg returned nil cmd")
	}
	if !hasChildTranscriptMsg(t, cmd) {
		t.Errorf("registry-miss path must still dispatch ChildTranscriptMsg backfill")
	}
	if app.ChildAdapter() != nil {
		t.Errorf("registry-miss path must not install a child adapter")
	}
}

// -- 4. Nil supervisor keeps polling (preserves existing invariant) ---------

func TestAgentSelected_NilSupervisor_KeepsPolling(t *testing.T) {
	app := newAppWithRegistry(t, nil)

	updated, cmd := app.Update(AgentSelectedMsg{Name: "nobody"})
	app = updated.(AppModel)
	if cmd == nil {
		t.Fatal("nil-supervisor AgentSelectedMsg returned nil cmd")
	}
	if !hasChildTranscriptMsg(t, cmd) {
		t.Errorf("nil-supervisor path must dispatch ChildTranscriptMsg backfill")
	}
	if app.ChildAdapter() != nil {
		t.Errorf("nil-supervisor path must not install a child adapter")
	}
}

// -- 5. Switching unified A -> unified B re-points the adapter ---------------

func TestViewportSwitch_TearsDownPriorAdapter(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urtA := newUnifiedRT(t, "alice")
	urtB := newUnifiedRT(t, "bart")
	registerUnified(t, reg, "alice", urtA)
	registerUnified(t, reg, "bart", urtB)

	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)
	epochA := app.ChildAdapterEpoch()

	updated, _ = app.Update(AgentSelectedMsg{Name: "bart"})
	app = updated.(AppModel)

	if got := app.ChildAdapterAgent(); got != "bart" {
		t.Errorf("after switch ChildAdapterAgent() = %q, want %q", got, "bart")
	}
	if app.ChildAdapterEpoch() == epochA {
		t.Errorf("ChildAdapterEpoch must bump on switch; both = %d", epochA)
	}

	// Behavioural: subscribe a sentinel directly to A's bus to count delivered
	// publishes; the previous adapter subscription must have been torn down,
	// so a publish on A's bus reaches only our sentinel (not the adapter's
	// closed channel). Indirectly: published events on A don't yield a
	// ChildStreamMsg{Agent: "alice", Epoch: current} into the app.
	ch, unsub := urtA.EventBus().Subscribe(4)
	defer unsub()
	urtA.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventTurnStarted})
	select {
	case <-ch:
		// sentinel received: bus is alive; can't go further from here
		// behaviorally without poking the adapter, so we trust the
		// ChildAdapterAgent assertion above.
	case <-time.After(50 * time.Millisecond):
		t.Errorf("sentinel never received its own publish on rtA bus")
	}
}

// -- 6. Switching unified -> root cancels adapter ----------------------------

func TestViewportSwitch_ToRoot_CancelsAdapter(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)
	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)
	if app.ChildAdapter() == nil {
		t.Fatal("adapter should be set after selecting alice")
	}

	updated, _ = app.Update(AgentSelectedMsg{Name: "weave"})
	app = updated.(AppModel)

	if app.ChildAdapter() != nil {
		t.Errorf("ChildAdapter must be cleared after switching to root; got %v", app.ChildAdapter())
	}
	if got := app.ChildAdapterAgent(); got != "" {
		t.Errorf("ChildAdapterAgent must be empty after switching to root; got %q", got)
	}
}

// -- 7. Stale-epoch ChildStreamMsg is dropped --------------------------------

func TestChildStreamMsg_StaleEpoch_Dropped(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)
	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)
	currentEpoch := app.ChildAdapterEpoch()

	// Snapshot alice's buffer.
	before := len(app.viewportFor("alice").GetMessages())

	stale := ChildStreamMsg{
		Agent: "alice",
		Epoch: currentEpoch - 1, // stale (definitely not current)
		Inner: AssistantTextMsg{Text: "leaked"},
	}
	updated, _ = app.Update(stale)
	app = updated.(AppModel)

	got := app.viewportFor("alice").GetMessages()
	if len(got) != before {
		t.Errorf("stale-epoch ChildStreamMsg mutated buffer (len %d -> %d): %+v", before, len(got), got)
	}
	for _, e := range got {
		if e.Content == "leaked" {
			t.Errorf("stale-epoch ChildStreamMsg leaked content into alice buffer: %+v", got)
		}
	}
}

// -- 8. ChildStreamMsg routes to correct agent buffer ------------------------

func TestChildStreamMsg_RoutesToCorrectAgent(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)
	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)
	epoch := app.ChildAdapterEpoch()

	// Deliver an empty backfill so the live-event gate clears (QUM-439 fix 2).
	updated, _ = app.Update(ChildTranscriptMsg{Agent: "alice", SessionID: "sid", Entries: nil})
	app = updated.(AppModel)

	rootBefore := len(app.viewportFor("weave").GetMessages())

	updated, _ = app.Update(ChildStreamMsg{
		Agent: "alice",
		Epoch: epoch,
		Inner: AssistantTextMsg{Text: "hi from alice"},
	})
	app = updated.(AppModel)

	// Alice buffer must contain the new text.
	aliceFound := false
	for _, e := range app.viewportFor("alice").GetMessages() {
		if e.Content != "" && contains(e.Content, "hi from alice") {
			aliceFound = true
			break
		}
	}
	if !aliceFound {
		t.Errorf("expected 'hi from alice' in alice buffer; got %+v", app.viewportFor("alice").GetMessages())
	}
	// Root must not be polluted.
	if got := len(app.viewportFor("weave").GetMessages()); got != rootBefore {
		t.Errorf("root viewport len changed (%d -> %d) on child stream", rootBefore, got)
	}
	for _, e := range app.viewportFor("weave").GetMessages() {
		if contains(e.Content, "hi from alice") {
			t.Errorf("alice's stream leaked into root buffer: %+v", e)
		}
	}
}

// -- 9. Live tool-call dedupes against backfill-seeded entries ---------------

func TestUnifiedStream_DedupesSeededToolCalls(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)
	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)
	epoch := app.ChildAdapterEpoch()

	// Seed via ChildTranscriptMsg with a tool_use entry.
	seeded := []MessageEntry{
		{Type: MessageToolCall, Content: "Bash: ls", ToolID: "tool-1", ToolInput: "ls", ToolInputFull: "ls", Complete: false},
	}
	updated, _ = app.Update(ChildTranscriptMsg{Agent: "alice", SessionID: "sid", Entries: seeded})
	app = updated.(AppModel)

	// Live stream re-delivers the same tool call; must be deduped.
	updated, _ = app.Update(ChildStreamMsg{
		Agent: "alice",
		Epoch: epoch,
		Inner: ToolCallMsg{ToolName: "Bash", ToolID: "tool-1", Input: "ls", FullInput: "ls"},
	})
	app = updated.(AppModel)

	count := 0
	for _, e := range app.viewportFor("alice").GetMessages() {
		if e.ToolID == "tool-1" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("tool-1 appears %d times in alice buffer; want exactly 1 (live call must dedupe vs. seeded)", count)
	}
}

// -- 10. Live AssistantTextMsg appears in the agent's buffer -----------------

func TestUnifiedStream_LiveAssistantTextAppears(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)
	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)
	epoch := app.ChildAdapterEpoch()

	// Seed a backfill (empty) to clear any "Waiting for..." banner state.
	updated, _ = app.Update(ChildTranscriptMsg{Agent: "alice", SessionID: "sid", Entries: nil})
	app = updated.(AppModel)

	updated, _ = app.Update(ChildStreamMsg{
		Agent: "alice",
		Epoch: epoch,
		Inner: AssistantTextMsg{Text: "live response"},
	})
	app = updated.(AppModel)

	found := false
	for _, e := range app.viewportFor("alice").GetMessages() {
		if contains(e.Content, "live response") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("live AssistantTextMsg did not appear in alice buffer; got %+v", app.viewportFor("alice").GetMessages())
	}
}

// -- 11. No goroutine leak after a few switches ------------------------------

func TestNoGoroutineLeak_AfterSwitches(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urtA := newUnifiedRT(t, "alice")
	urtB := newUnifiedRT(t, "bart")
	registerUnified(t, reg, "alice", urtA)
	registerUnified(t, reg, "bart", urtB)
	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	settle := func() {
		// Allow any in-flight goroutines (adapter background workers, etc.)
		// to drain. 100ms is generous on a loaded CI box.
		time.Sleep(100 * time.Millisecond)
		runtime.GC()
	}

	settle()
	before := runtime.NumGoroutine()

	for i := 0; i < 3; i++ {
		updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
		app = updated.(AppModel)
		updated, _ = app.Update(AgentSelectedMsg{Name: "bart"})
		app = updated.(AppModel)
		updated, _ = app.Update(AgentSelectedMsg{Name: "weave"})
		app = updated.(AppModel)
	}

	settle()
	after := runtime.NumGoroutine()

	delta := after - before
	// Slack: TUIAdapter and the underlying EventBus subscription may keep
	// 1-2 helper goroutines around; after switching back to root all
	// per-child adapters should be cancelled. Anything more indicates a
	// real leak.
	if delta > 2 {
		t.Errorf("goroutine count grew by %d after 3x switch cycles (before=%d, after=%d); suggests adapter teardown is leaking",
			delta, before, after)
	}
}

// -- helpers -----------------------------------------------------------------

// contains is a tiny strings.Contains alias to keep imports light.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
outer:
	for i := 0; i+len(sub) <= len(s); i++ {
		for j := 0; j < len(sub); j++ {
			if s[i+j] != sub[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

// -- 12. Live event arriving before backfill is preserved (no clobber) ------
//
// Regression test for QUM-439 fix 2. AgentSelectedMsg dispatches a
// loadChildTranscriptCmd alongside a childStreamWaitCmd. If a ChildStreamMsg
// (live ToolCallMsg) is processed BEFORE the corresponding ChildTranscriptMsg
// arrives, the naive implementation appends the live entry, then
// vp.SetMessages(seedEntries) clobbers it. The fix queues live events while
// childBackfillPending is true and drains them after seeding.
func TestUnifiedStream_LiveEventBeforeBackfill_NotClobbered(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)
	sup := &supervisorWithRegistry{reg: reg}
	app := newAppWithRegistry(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)
	epoch := app.ChildAdapterEpoch()

	// Deliver a live ChildStreamMsg BEFORE the ChildTranscriptMsg seed.
	live := ChildStreamMsg{
		Agent: "alice",
		Epoch: epoch,
		Inner: ToolCallMsg{ToolName: "Bash", ToolID: "t-live", Input: "echo hi", FullInput: "echo hi"},
	}
	updated, _ = app.Update(live)
	app = updated.(AppModel)

	// Now deliver the backfill seed (no t-live entry).
	seed := []MessageEntry{
		{Type: MessageToolCall, Content: "Bash: ls", ToolID: "t-seed-1", ToolInput: "ls", ToolInputFull: "ls", Complete: true},
		{Type: MessageAssistant, Content: "seeded text", Complete: true},
	}
	updated, _ = app.Update(ChildTranscriptMsg{Agent: "alice", SessionID: "sid", Entries: seed})
	app = updated.(AppModel)

	entries := app.viewportFor("alice").GetMessages()

	// Seed entries must be present.
	foundSeedTool := false
	foundSeedText := false
	for _, e := range entries {
		if e.ToolID == "t-seed-1" {
			foundSeedTool = true
		}
		if e.Type == MessageAssistant && contains(e.Content, "seeded text") {
			foundSeedText = true
		}
	}
	if !foundSeedTool {
		t.Errorf("seed tool t-seed-1 missing from viewport after backfill; got %+v", entries)
	}
	if !foundSeedText {
		t.Errorf("seed assistant text missing from viewport after backfill; got %+v", entries)
	}

	// Live tool call must also be present (not clobbered by SetMessages).
	foundLive := false
	for _, e := range entries {
		if e.ToolID == "t-live" {
			foundLive = true
			break
		}
	}
	if !foundLive {
		t.Errorf("live ToolCallMsg t-live was clobbered by ChildTranscriptMsg seed; entries=%+v", entries)
	}
}
