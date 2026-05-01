// Tests for QUM-440: stream the TUI activity panel from the
// UnifiedRuntime EventBus instead of polling activity.ndjson every 2s.
//
// These tests are written BEFORE implementation (TDD red phase) and are
// expected to FAIL TO COMPILE until the implementation lands. The symbols
// they reference but which do not yet exist:
//
//   - tui.ActivityStreamAdapter (Observe / Cancel / WaitForEvent), modeled
//     on tui.ChildStreamAdapter in internal/tui/child_stream.go.
//   - tui.NewActivityStreamAdapter(rt *runtime.UnifiedRuntime).
//   - tui.ActivityStreamMsg{Agent string, Epoch uint64,
//     Entries []agentloop.ActivityEntry}.
//   - AppModel.ActivityAdapter() / .ActivityAdapterAgent() /
//     .ActivityAdapterEpoch() accessors (mirrors of the QUM-439
//     ChildAdapter accessors).
//   - AppModel.ActivityEntries(agent string) []agentloop.ActivityEntry —
//     a small read-only accessor so tests can assert what the panel will
//     render without parsing View() text. (Activity panel itself stores
//     entries today; expose the slice.)
//
// Goals (per QUM-440 plan):
//   1. Adapter unit: Observe(rt) swap, epoch bump, Cancel idempotent,
//      WaitForEvent returns SessionErrorMsg{io.EOF} on cancel.
//   2. Translator unit: EventProtocolMessage(assistant w/ N blocks) yields
//      one ActivityStreamMsg with N entries; non-protocol events skipped.
//   3. AppModel — backfill seed via PeekActivity once, no reschedule.
//   4. AppModel — live append in arrival order, seed+live coexist.
//   5. AppModel — dedupe live entry vs an identical seeded entry.
//   6. AppModel — viewport switch teardown bumps epoch; stale msgs ignored.
//   7. AppModel — fallback path: agent without UnifiedRuntime keeps polling.
//   8. AppModel — root-agent attach: weave (root) gets streaming path too.

package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/protocol"
	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// activityRecordingSupervisor wraps mockSupervisor and records PeekActivity
// calls so we can assert on the seed-once invariant.
type activityRecordingSupervisor struct {
	mockSupervisor
	reg          *supervisor.RuntimeRegistry
	peekCalls    int
	peekEntries  []agentloop.ActivityEntry
	peekLastName string
}

func (s *activityRecordingSupervisor) RuntimeRegistry() *supervisor.RuntimeRegistry {
	return s.reg
}

func (s *activityRecordingSupervisor) PeekActivity(_ context.Context, name string, _ int) ([]agentloop.ActivityEntry, error) {
	s.peekCalls++
	s.peekLastName = name
	return s.peekEntries, nil
}

// newActivityApp builds an AppModel wired to the given supervisor with a
// usable layout — copy-paste of newAppWithRegistry but kept local so the
// activity_stream_test file is self-contained.
func newActivityApp(t *testing.T, sup supervisor.Supervisor) AppModel {
	t.Helper()
	sprawlRoot := t.TempDir()
	homeDir := t.TempDir()
	m := NewAppModel("colour212", "testrepo", "v0.1.0", nil, sup, sprawlRoot, nil)
	m.SetHomeDir(homeDir)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	return resized.(AppModel)
}

// makeAssistantMsg builds a protocol.Message of type=assistant with N text
// blocks, suitable for triggering RecordMessage to emit N ActivityEntry
// values.
func makeAssistantMsg(t *testing.T, blocks ...string) *protocol.Message {
	t.Helper()
	// Hand-construct the JSON to keep the test fixture obvious.
	raw := `{"type":"assistant","message":{"role":"assistant","content":[`
	for i, b := range blocks {
		if i > 0 {
			raw += ","
		}
		raw += fmt.Sprintf(`{"type":"text","text":%q}`, b)
	}
	raw += `]}}`
	return &protocol.Message{Type: "assistant", Raw: []byte(raw)}
}

// -- 1. Adapter: Observe swap, epoch bump, Cancel idempotent, EOF on cancel --

func TestActivityStreamAdapter_Observe_SwapsAndBumpsEpoch(t *testing.T) {
	rt1 := newUnifiedRT(t, "alice")
	rt2 := newUnifiedRT(t, "bart")

	a := NewActivityStreamAdapter(rt1)
	if a == nil {
		t.Fatal("NewActivityStreamAdapter returned nil")
	}
	e1 := a.Epoch()
	if e1 == 0 {
		t.Errorf("epoch after construction should be > 0; got 0")
	}

	a.Observe(rt2)
	e2 := a.Epoch()
	if e2 == e1 {
		t.Errorf("Observe(rt2) should bump epoch (was %d, now %d)", e1, e2)
	}

	// Cancel is idempotent.
	a.Cancel()
	a.Cancel() // must not panic / not deadlock
}

func TestActivityStreamAdapter_WaitForEvent_ReturnsEOFOnCancel(t *testing.T) {
	rt := newUnifiedRT(t, "alice")
	a := NewActivityStreamAdapter(rt)

	done := make(chan tea.Msg, 1)
	go func() {
		done <- a.WaitForEvent()()
	}()

	// Give the goroutine a moment to actually block on the channel.
	time.Sleep(20 * time.Millisecond)
	a.Cancel()

	select {
	case msg := <-done:
		serr, ok := msg.(SessionErrorMsg)
		if !ok {
			t.Fatalf("WaitForEvent after Cancel returned %T, want SessionErrorMsg", msg)
		}
		if !errors.Is(serr.Err, io.EOF) {
			t.Errorf("SessionErrorMsg.Err = %v, want io.EOF", serr.Err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("WaitForEvent did not return after Cancel within deadline")
	}
}

// -- 2. Translator: assistant with N blocks → one msg with N entries --------

func TestActivityStreamAdapter_Translator_AssistantMultiBlock(t *testing.T) {
	rt := newUnifiedRT(t, "alice")
	a := NewActivityStreamAdapter(rt)
	defer a.Cancel()

	msg := makeAssistantMsg(t, "first chunk", "second chunk")

	out := make(chan tea.Msg, 1)
	go func() {
		out <- a.WaitForEvent()()
	}()

	rt.EventBus().Publish(sprawlrt.RuntimeEvent{
		Type:    sprawlrt.EventProtocolMessage,
		Message: msg,
	})

	select {
	case raw := <-out:
		streamMsg, ok := raw.(ActivityStreamMsg)
		if !ok {
			t.Fatalf("got %T, want ActivityStreamMsg", raw)
		}
		if len(streamMsg.Entries) != 2 {
			t.Errorf("got %d entries, want 2 (one per content block)", len(streamMsg.Entries))
		}
		// Order preservation: first block first.
		if len(streamMsg.Entries) >= 2 {
			if streamMsg.Entries[0].Summary == "" || streamMsg.Entries[1].Summary == "" {
				t.Errorf("entries should have non-empty summaries; got %+v", streamMsg.Entries)
			}
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("translator did not produce ActivityStreamMsg within deadline")
	}
}

func TestActivityStreamAdapter_Translator_SkipsNonProtocolEvents(t *testing.T) {
	rt := newUnifiedRT(t, "alice")
	a := NewActivityStreamAdapter(rt)
	defer a.Cancel()

	out := make(chan tea.Msg, 1)
	go func() {
		out <- a.WaitForEvent()()
	}()

	// TurnStarted alone should NOT yield an ActivityStreamMsg.
	rt.EventBus().Publish(sprawlrt.RuntimeEvent{Type: sprawlrt.EventTurnStarted})

	select {
	case msg := <-out:
		if _, ok := msg.(ActivityStreamMsg); ok {
			t.Errorf("TurnStarted should not yield ActivityStreamMsg; got %T", msg)
		}
	case <-time.After(50 * time.Millisecond):
		// Expected: translator consumed the event silently and is still blocking
		// on the next event. Good.
	}
}

// -- 3. AppModel — backfill seed via PeekActivity once, no reschedule -------

func TestApp_ActivityStream_UnifiedPath_SeedsOnceAndStreamsLive(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)

	seed := []agentloop.ActivityEntry{
		{TS: time.Unix(1700000000, 0).UTC(), Kind: "system", Summary: "init"},
		{TS: time.Unix(1700000001, 0).UTC(), Kind: "assistant_text", Summary: "hi"},
	}
	sup := &activityRecordingSupervisor{reg: reg, peekEntries: seed}
	app := newActivityApp(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)

	if sup.peekCalls != 1 {
		t.Errorf("expected exactly 1 PeekActivity call after select; got %d", sup.peekCalls)
	}
	if app.ActivityAdapter() == nil {
		t.Fatalf("ActivityAdapter() should be non-nil on unified path")
	}
	if got := app.ActivityAdapterAgent(); got != "alice" {
		t.Errorf("ActivityAdapterAgent() = %q, want alice", got)
	}
	if app.ActivityAdapterEpoch() == 0 {
		t.Errorf("ActivityAdapterEpoch() should be > 0 after attach")
	}

	// Apply the seed via the message loop (mirrors what tickActivityCmd
	// would deliver synchronously). The implementation may seed via a
	// one-shot ActivityTickMsg or directly; either way the panel should
	// hold the entries.
	updated, _ = app.Update(ActivityTickMsg{Agent: "alice", Entries: seed})
	app = updated.(AppModel)

	got := app.ActivityEntries("alice")
	if !reflect.DeepEqual(got, seed) {
		t.Errorf("ActivityEntries after seed mismatch:\n got=%+v\nwant=%+v", got, seed)
	}
}

func TestApp_ActivityStream_UnifiedPath_NoRescheduledTick(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)

	sup := &activityRecordingSupervisor{reg: reg}
	app := newActivityApp(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)

	// First seed call.
	first := sup.peekCalls

	// Deliver an ActivityTickMsg as the seed reply. On the unified path the
	// AppModel must NOT reschedule another tick (live data flows via the
	// stream adapter instead).
	_, cmd := app.Update(ActivityTickMsg{Agent: "alice", Entries: nil})

	// Run any returned cmd briefly to surface a follow-up tick.
	if cmd != nil {
		out := make(chan tea.Msg, 1)
		go func() { out <- cmd() }()
		select {
		case msg := <-out:
			if _, ok := msg.(ActivityTickMsg); ok {
				t.Errorf("unified path must not reschedule ActivityTickMsg; got one")
			}
		case <-time.After(60 * time.Millisecond):
			// fine — no immediate follow-up
		}
	}

	if sup.peekCalls > first {
		t.Errorf("PeekActivity should not be called again after seed on unified path (was %d, now %d)", first, sup.peekCalls)
	}
}

// -- 4. AppModel — live append: stream entries appended to seed in order ----

func TestApp_ActivityStream_LiveAppendsAfterSeed(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)

	seed := []agentloop.ActivityEntry{
		{TS: time.Unix(1700000000, 0).UTC(), Kind: "system", Summary: "init"},
	}
	sup := &activityRecordingSupervisor{reg: reg, peekEntries: seed}
	app := newActivityApp(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)
	updated, _ = app.Update(ActivityTickMsg{Agent: "alice", Entries: seed})
	app = updated.(AppModel)

	epoch := app.ActivityAdapterEpoch()

	live := []agentloop.ActivityEntry{
		{TS: time.Unix(1700000010, 0).UTC(), Kind: "tool_use", Tool: "Bash", Summary: `Bash {"command":"ls"}`},
	}
	updated, _ = app.Update(ActivityStreamMsg{
		Agent:   "alice",
		Epoch:   epoch,
		Entries: live,
	})
	app = updated.(AppModel)

	got := app.ActivityEntries("alice")
	if len(got) != 2 {
		t.Fatalf("expected seed(1)+live(1)=2 entries; got %d: %+v", len(got), got)
	}
	if got[0].Kind != "system" {
		t.Errorf("first entry should be the seed; got %+v", got[0])
	}
	if got[1].Kind != "tool_use" || got[1].Tool != "Bash" {
		t.Errorf("second entry should be the live tool_use Bash; got %+v", got[1])
	}
}

// -- 5. AppModel — dedupe: live entry matching seeded key is dropped --------

func TestApp_ActivityStream_DedupesLiveAgainstSeed(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "alice")
	registerUnified(t, reg, "alice", urt)

	ts := time.Unix(1700000005, 0).UTC()
	dup := agentloop.ActivityEntry{
		TS:      ts,
		Kind:    "tool_use",
		Tool:    "Read",
		Summary: `Read {"file":"/tmp/x"}`,
	}
	seed := []agentloop.ActivityEntry{dup}
	sup := &activityRecordingSupervisor{reg: reg, peekEntries: seed}
	app := newActivityApp(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)
	updated, _ = app.Update(ActivityTickMsg{Agent: "alice", Entries: seed})
	app = updated.(AppModel)
	epoch := app.ActivityAdapterEpoch()

	// Live re-delivery of the same entry — must be deduped.
	updated, _ = app.Update(ActivityStreamMsg{
		Agent:   "alice",
		Epoch:   epoch,
		Entries: []agentloop.ActivityEntry{dup},
	})
	app = updated.(AppModel)

	got := app.ActivityEntries("alice")
	if len(got) != 1 {
		t.Errorf("dup live entry should be deduped; entries=%+v", got)
	}
}

// -- 6. AppModel — viewport switch bumps epoch; stale msgs ignored ----------

func TestApp_ActivityStream_ViewportSwitch_DropsStaleEpoch(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urtA := newUnifiedRT(t, "alice")
	urtB := newUnifiedRT(t, "bart")
	registerUnified(t, reg, "alice", urtA)
	registerUnified(t, reg, "bart", urtB)

	sup := &activityRecordingSupervisor{reg: reg}
	app := newActivityApp(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "alice"})
	app = updated.(AppModel)
	staleEpoch := app.ActivityAdapterEpoch()

	updated, _ = app.Update(AgentSelectedMsg{Name: "bart"})
	app = updated.(AppModel)
	if app.ActivityAdapterAgent() != "bart" {
		t.Errorf("after switch ActivityAdapterAgent() = %q, want bart", app.ActivityAdapterAgent())
	}
	if app.ActivityAdapterEpoch() == staleEpoch {
		t.Errorf("epoch must bump on switch; both = %d", staleEpoch)
	}

	beforeAlice := len(app.ActivityEntries("alice"))
	beforeBart := len(app.ActivityEntries("bart"))

	// Late-delivered stream msg from alice's prior epoch must be dropped.
	stale := ActivityStreamMsg{
		Agent: "alice",
		Epoch: staleEpoch,
		Entries: []agentloop.ActivityEntry{
			{TS: time.Now(), Kind: "assistant_text", Summary: "leaked"},
		},
	}
	updated, _ = app.Update(stale)
	app = updated.(AppModel)

	if got := len(app.ActivityEntries("alice")); got != beforeAlice {
		t.Errorf("stale ActivityStreamMsg mutated alice entries (%d -> %d)", beforeAlice, got)
	}
	if got := len(app.ActivityEntries("bart")); got != beforeBart {
		t.Errorf("stale ActivityStreamMsg leaked into bart (%d -> %d)", beforeBart, got)
	}
}

// -- 7. AppModel — fallback path: agent without UnifiedRuntime keeps polling

func TestApp_ActivityStream_LegacyAgent_KeepsPolling(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	registerLegacy(t, reg, "bob")
	sup := &activityRecordingSupervisor{reg: reg}
	app := newActivityApp(t, sup)

	// Persist a state file so the legacy backfill path produces something.
	if err := state.SaveAgent(app.sprawlRoot, &state.AgentState{
		Name: "bob", Type: "engineer", Status: "running",
	}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	updated, _ := app.Update(AgentSelectedMsg{Name: "bob"})
	app = updated.(AppModel)

	if app.ActivityAdapter() != nil {
		t.Errorf("legacy agent must not install ActivityStreamAdapter; got %v", app.ActivityAdapter())
	}

	// Legacy path must reschedule on each ActivityTickMsg (preserves prior
	// 2s-poll behaviour).
	_, cmd := app.Update(ActivityTickMsg{Agent: "bob", Entries: nil})
	if cmd == nil {
		t.Fatal("legacy ActivityTickMsg returned nil cmd; expected reschedule")
	}
	out := make(chan tea.Msg, 1)
	go func() { out <- cmd() }()
	select {
	case msg := <-out:
		if _, ok := msg.(ActivityTickMsg); !ok {
			t.Errorf("legacy reschedule should yield ActivityTickMsg; got %T", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("legacy ActivityTickMsg reschedule did not fire within deadline (expected ~2s tick)")
	}
}

// -- 8. AppModel — root-agent attach: weave (root) gets streaming path too -

// QUM-440 goal: unlike the QUM-439 child-viewport carve-out, the activity
// panel should stream for the root agent too. Selecting weave with a unified
// runtime registered should install the ActivityStreamAdapter.
func TestApp_ActivityStream_RootAgent_AttachesAdapter(t *testing.T) {
	reg := supervisor.NewRuntimeRegistry()
	urt := newUnifiedRT(t, "weave")
	registerUnified(t, reg, "weave", urt)

	sup := &activityRecordingSupervisor{reg: reg}
	app := newActivityApp(t, sup)

	updated, _ := app.Update(AgentSelectedMsg{Name: "weave"})
	app = updated.(AppModel)

	if app.ActivityAdapter() == nil {
		t.Fatalf("root agent (weave) with UnifiedRuntime should attach ActivityStreamAdapter")
	}
	if got := app.ActivityAdapterAgent(); got != "weave" {
		t.Errorf("ActivityAdapterAgent() = %q, want weave", got)
	}
	if sup.peekCalls != 1 {
		t.Errorf("PeekActivity should still be called once for root seed; got %d", sup.peekCalls)
	}
}
