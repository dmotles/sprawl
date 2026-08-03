package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-1000: a slash command the CLI REFUSES gets an ordinary `assistant` refusal
// text and NO isReplay echo, inside an otherwise normal running…result…idle
// envelope. The echo is the only consumption ack (QUM-817), so the TUI pending
// zone (QUM-833) never receives its ZoneSettle and a ghost `› /status` row sits
// in the prompt area forever. Two measured refusal classes: an unknown command
// (`/qum1000-nope`) and a real builtin the sdk-cli entrypoint declines
// (`/status`). Accepted commands (`/model`, `/context`) and leading-slash prose
// DO echo and already settle today — asserting against those is vacuously green,
// which is why every test below drives a refusal or an explicit echo control.
//
// The fix settles never-acked kind:user entries at a clean turn terminal,
// guarded by the submit-order watermark taken at the last running TRANSITION.
//
// Which mutation each test kills. Every claim here was verified by applying the
// mutation to a working implementation and observing the named test fail; the
// three "shared" notes record where a mutation is caught by more than one test
// so no test is credited with a kill it does not uniquely make.
//
//	RefusedCommand_SettlesOnTurnTerminal     — the whole bug: no sweep at all
//	                                           (red-first; shared with every other
//	                                           does-sweep test: the order test,
//	                                           SweepPublishesConsumedBeforeTerminal,
//	                                           RunningAfterSubmittedTimeout and
//	                                           ErrorTerminalWithNoArm)
//	SweepPublishesConsumedBeforeTerminal     — moving settleNeverAcked below the
//	                                           terminal Publish (spurious-spinner
//	                                           regression); also pins Result payload
//	MultiplePending_OnlyOldestSettles        — settling every entry ≤ the watermark
//	                                           instead of just the oldest, or picking
//	                                           by map-iteration order instead of by
//	                                           minimum seq. Uses 8 entries because Go
//	                                           randomizes a map's iteration START
//	                                           OFFSET, so an order-picking bug is
//	                                           detected with probability (k-1)/k —
//	                                           1-in-8 at k=2, barely a check at all.
//	SecondPreRunningSubmit_StaysRecallable   — removing the "this turn acked nothing"
//	                                           gate. seq is stamped at SUBMIT time, so
//	                                           two prompts typed before the wire
//	                                           `running` both land ≤ the watermark while
//	                                           the CLI executes them across two turns;
//	                                           settling the second makes it invisible to
//	                                           Ctrl+U recall.
//	MidTurnIdleThenRunning_DoesNotReMark     — dropping `&& openTurnID == 0` from the
//	                                           running-transition gate (a mid-turn wire
//	                                           `idle` makes the phase check alone read
//	                                           "fresh")
//	EchoedPrompt_SettlesExactlyOnce          — dropping the state == statePending filter
//	PromptSubmittedAfterRunningMark_NotSwept — deleting `e.seq <= lastRunningMark`
//	                                           (the QUM-927/QUM-935 bare-trigger
//	                                           class; shared with MidTurnQueued +
//	                                           NoRunningTransition)
//	MidTurnQueuedPrompt_Survives…            — re-marking on every `running` frame
//	                                           instead of only on a transition
//	RunningAfterSubmittedTimeout_Sweeps      — marking only on submitted→running and
//	                                           not on idle→running (a >2s CLI ack
//	                                           lands `running` on an idle phase)
//	SystemMessageNeverSwept_NoOnDelivered    — dropping the kind == kindUser filter.
//	                                           NOTE it does NOT catch implementing the
//	                                           sweep via markConsumed: kind:user entries
//	                                           never carry entryIDs, so OnDelivered is
//	                                           structurally unreachable on that path.
//	                                           The filter does the work; the OnDelivered
//	                                           assertion is belt to that braces.
//	InterruptedTerminal_DoesNotSweep         — sweeping above the consumeInterrupt
//	                                           branch (would make an Esc'd prompt
//	                                           unrecallable)
//	OrphanTeardown_DoesNotSweep              — sweeping above the orphan early-return
//	ErrorTerminalWithNoArm_Sweeps            — pins the deliberate choice for an
//	                                           unarmed is_error terminal (see its doc)
//	NoRunningTransition_NothingSwept         — seeding the watermark from outSeq at write
//	                                           time (shared with PromptSubmittedAfter…
//	                                           and MidTurnQueued)
//	SystemInjectionDoesNotStrand…/subtest A  — advancing lastRunningMark from a
//	                                           kind:system write. UNIQUE kill: verified
//	                                           by running all TestQUM1000 under the
//	                                           mutation — no other test fails.
//	SystemInjectionDoesNotStrand…/subtest B  — narrowing routeFrame's kind-blind
//	                                           noteTurnAcked to kind:user only, which
//	                                           strands a prompt queued before an inbox
//	                                           write. UNIQUE kill, same verification.
//	                                           The two subtests' failure sets are
//	                                           DISJOINT: mutation A fails only A,
//	                                           mutation B only B — so neither is one
//	                                           fixture answering for the other.
//	WrongfulSweep_LosesRecallNotDelivery     — a sweep that RETRACTS (issues
//	                                           cancel_async_message) rather than only
//	                                           re-labelling, and an ack that stops
//	                                           counting once the entry is already
//	                                           swept (cascading onto the next queued
//	                                           prompt). Both UNIQUE against the whole
//	                                           internal/runtime package. See that
//	                                           test's own table for the third
//	                                           (non-unique) mutation and the printed
//	                                           failures.
//
// These tests direct-drive rt.routeFrame, mirroring phase_test.go /
// interrupt_classify_test.go. They mutate the package global
// submittedPhaseTimeout, so they must NOT be t.Parallel().

// --- readable failure output ------------------------------------------------
// outstandingState / outstandingKind / RuntimeEventType are unexported ints with
// no String(); a raw `state=0` reads as "zero value / missing entry", which is
// actively misleading on the one line that defines this bug.

func stateName(s outstandingState) string {
	switch s {
	case statePending:
		return "statePending"
	case stateConsumed:
		return "stateConsumed"
	case stateCancelled:
		return "stateCancelled"
	}
	return fmt.Sprintf("outstandingState(%d)", int(s))
}

func kindName(k outstandingKind) string {
	switch k {
	case kindUser:
		return "kindUser"
	case kindSystem:
		return "kindSystem"
	}
	return fmt.Sprintf("outstandingKind(%d)", int(k))
}

func eventName(t RuntimeEventType) string {
	switch t {
	case EventUserMessageConsumed:
		return "UserMessageConsumed"
	case EventUserMessageCancelled:
		return "UserMessageCancelled"
	case EventUserMessageSent:
		return "UserMessageSent"
	case EventTurnStarted:
		return "TurnStarted"
	case EventTurnCompleted:
		return "TurnCompleted"
	case EventTurnFailed:
		return "TurnFailed"
	case EventInterrupted:
		return "Interrupted"
	case EventProtocolMessage:
		return "ProtocolMessage"
	case EventStopped:
		return "Stopped"
	case EventBackendFaulted:
		return "BackendFaulted"
	}
	return fmt.Sprintf("RuntimeEventType(%d)", int(t))
}

func eventNames(events []RuntimeEvent) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, eventName(ev.Type))
	}
	return out
}

// --- harness ----------------------------------------------------------------

// qum1000Fixture is a runtime whose OnDelivered calls are recorded, plus a
// subscriber channel. Every publish in these tests originates synchronously on
// the test goroutine, so drainNow (non-blocking) sees them all.
type qum1000Fixture struct {
	rt   *UnifiedRuntime
	mock *mockUnifiedSession
	ch   <-chan RuntimeEvent

	mu        sync.Mutex
	delivered [][]string
}

func newQUM1000Fixture(t *testing.T) *qum1000Fixture {
	t.Helper()
	withShortSubmittedTimeout(t, 500*time.Millisecond)
	f := &qum1000Fixture{mock: &mockUnifiedSession{cancelResults: map[string]bool{}}}
	f.rt = New(RuntimeConfig{
		Name:    "weave",
		Session: f.mock,
		OnDelivered: func(ids []string) {
			f.mu.Lock()
			f.delivered = append(f.delivered, ids)
			f.mu.Unlock()
		},
	})
	ch, unsub := f.rt.EventBus().SubscribeNamed("qum1000-"+t.Name(), 64)
	f.ch = ch
	t.Cleanup(unsub)
	return f
}

func (f *qum1000Fixture) deliveries() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.delivered...)
}

// drainNow collects every already-buffered event without waiting: EventBus
// fanout completes synchronously inside Publish, and every publish in these
// tests happens on this goroutine.
func drainNow(ch <-chan RuntimeEvent) []RuntimeEvent {
	var events []RuntimeEvent
	for {
		select {
		case ev := <-ch:
			events = append(events, ev)
		default:
			return events
		}
	}
}

// replayEcho drives the CLI's isReplay consumption ack for uuid — the accepted
// path's settle signal, used here as the positive control.
func replayEcho(t *testing.T, rt *UnifiedRuntime, uuid string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type":     "user",
		"uuid":     uuid,
		"isReplay": true,
		"message":  map[string]any{"role": "user", "content": "echo"},
	})
	if err != nil {
		t.Fatalf("marshal replay echo: %v", err)
	}
	rt.routeFrame(&protocol.Message{Type: "user", Raw: raw}, backend.TurnInfo{Autonomous: true, Replay: true})
}

// assistantText routes an ordinary assistant content frame — the shape the CLI's
// refusal text ("/status isn't available in this environment.") actually arrives
// in. Routed mid-turn so the tests prove intervening content frames neither
// trigger nor block the sweep.
func assistantText(t *testing.T, rt *UnifiedRuntime, text string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type":    "assistant",
		"message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": text}}},
	})
	if err != nil {
		t.Fatalf("marshal assistant frame: %v", err)
	}
	rt.routeFrame(&protocol.Message{Type: "assistant", Raw: raw}, backend.TurnInfo{Autonomous: true})
}

// cleanTerminal routes a non-error terminal `result` frame closing the open turn.
func cleanTerminal(t *testing.T, rt *UnifiedRuntime) {
	t.Helper()
	rt.routeFrame(resultFrame(t, false, 10), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
}

// runningTransition + init: the CLI confirming a live turn, as observed on the
// wire for a refused slash command (running → command_lifecycle → init → …).
func runningTransition(rt *UnifiedRuntime) {
	rt.routeFrame(stateFrame(protocol.SessionStateRunning))
	feedInit(rt)
}

func entryState(t *testing.T, rt *UnifiedRuntime, uuid string) OutstandingEntry {
	t.Helper()
	e, ok := rt.Outstanding()[uuid]
	if !ok {
		t.Fatalf("outstanding entry %s is absent; the test cannot assert on a zone entry that was never created", uuid)
	}
	return e
}

// consumedFor counts EventUserMessageConsumed publishes carrying uuid.
func consumedFor(events []RuntimeEvent, uuid string) int {
	n := 0
	for _, ev := range events {
		if ev.Type == EventUserMessageConsumed && ev.UUID == uuid {
			n++
		}
	}
	return n
}

func indexOfType(events []RuntimeEvent, typ RuntimeEventType) int {
	for i, ev := range events {
		if ev.Type == typ {
			return i
		}
	}
	return -1
}

func indexOfConsumed(events []RuntimeEvent, uuid string) int {
	for i, ev := range events {
		if ev.Type == EventUserMessageConsumed && ev.UUID == uuid {
			return i
		}
	}
	return -1
}

// --- tests ------------------------------------------------------------------

// TestQUM1000_RefusedCommand_SettlesOnTurnTerminal is the red-first repro. It
// asserts the entry APPEARS pending first (both before and after the turn goes
// running), so "the zone is empty" cannot pass vacuously, and only then that the
// clean terminal settles it. The two cases differ in wire shape, not just text:
// the refused builtin also routes its refusal assistant frame, proving content
// frames neither trigger nor block the sweep.
func TestQUM1000_RefusedCommand_SettlesOnTurnTerminal(t *testing.T) {
	cases := []struct {
		name    string
		cmd     string
		refusal string // routed as an assistant frame when non-empty
	}{
		{name: "refused-builtin", cmd: "/status", refusal: "/status isn't available in this environment."},
		{name: "unknown-command", cmd: "/qum1000-nope arg1 arg2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newQUM1000Fixture(t)

			uuid, err := f.rt.WriteUserPrompt(context.Background(), tc.cmd, "next")
			if err != nil {
				t.Fatalf("WriteUserPrompt(%q): %v", tc.cmd, err)
			}
			// (a) the pending-zone entry exists — anti-vacuity.
			if got := entryState(t, f.rt, uuid); got.state != statePending || got.kind != kindUser {
				t.Fatalf("after write: state=%s kind=%s, want statePending/kindUser", stateName(got.state), kindName(got.kind))
			}

			// (b) still pending once the CLI confirms the turn and answers with its
			// refusal text; no isReplay echo ever comes.
			runningTransition(f.rt)
			if tc.refusal != "" {
				assistantText(t, f.rt, tc.refusal)
			}
			if got := entryState(t, f.rt, uuid); got.state != statePending {
				t.Fatalf("after running+init(+refusal text): state=%s, want statePending", stateName(got.state))
			}

			// (c) the clean terminal must settle it — the CLI finished this turn
			// without ever acking the submission, so no ack is coming.
			cleanTerminal(t, f.rt)
			if got := entryState(t, f.rt, uuid); got.state != stateConsumed {
				t.Errorf("after clean terminal: state=%s, want stateConsumed (ghost pending-zone entry)", stateName(got.state))
			}

			// (d) exactly one consume publish — that is what drives ZoneSettle.
			f.rt.routeFrame(stateFrame(protocol.SessionStateIdle))
			events := drainNow(f.ch)
			if n := consumedFor(events, uuid); n != 1 {
				t.Errorf("EventUserMessageConsumed for %s = %d, want exactly 1; events=%v", uuid, n, eventNames(events))
			}
		})
	}
}

// TestQUM1000_SweepPublishesConsumedBeforeTerminal pins the publish ORDER, and
// the constraint is the TUI's: internal/tui/app.go's UserMessageConsumedMsg
// reducer flips TurnIdle→TurnThinking (QUM-831), and nothing in the TUI clears a
// spurious TurnThinking — there is no turn watchdog; the only routes back to
// TurnIdle are finalizeTurn (SessionResultMsg / InterruptCompletedMsg /
// SessionErrorMsg), a restart, or the QUM-669 resync. Publishing consumed BEFORE
// the terminal guarantees finalizeTurn's TurnIdle is the last word.
//
// It also pins that the terminal still carries its Result payload — the sweep is
// inserted on that exact line, and a stripped Result silently breaks the
// "Completed (Nms)" render.
func TestQUM1000_SweepPublishesConsumedBeforeTerminal(t *testing.T) {
	f := newQUM1000Fixture(t)

	uuid, err := f.rt.WriteUserPrompt(context.Background(), "/status", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	runningTransition(f.rt)
	cleanTerminal(t, f.rt)

	events := drainNow(f.ch)
	ci := indexOfConsumed(events, uuid)
	ti := indexOfType(events, EventTurnCompleted)
	if ci < 0 {
		t.Fatalf("no EventUserMessageConsumed for %s; events=%v", uuid, eventNames(events))
	}
	if ti < 0 {
		t.Fatalf("no EventTurnCompleted; events=%v", eventNames(events))
	}
	if ci > ti {
		t.Errorf("consumed published AFTER terminal (consumed idx %d, terminal idx %d): the TUI would be left with a spurious lit spinner", ci, ti)
	}
	if events[ci].Seq >= events[ti].Seq {
		t.Errorf("consumed Seq %d >= terminal Seq %d; bus ordering does not match publish order", events[ci].Seq, events[ti].Seq)
	}
	res := events[ti].Result
	if res == nil {
		t.Fatalf("EventTurnCompleted.Result is nil; the terminal payload was stripped")
	}
	if res.DurationMs != 10 || res.IsError {
		t.Errorf("EventTurnCompleted.Result = {DurationMs:%d IsError:%v}, want {10 false}", res.DurationMs, res.IsError)
	}
}

// TestQUM1000_MultiplePending_OnlyOldestSettles: a turn consumes ONE queued
// submission, so at most one entry can have been silently dropped by it. The
// others are queued for later turns and must stay recallable.
//
// The entry count is load-bearing, not arbitrary. Go randomizes the START OFFSET
// of a map bucket walk, so a sweep that picks by iteration order instead of by
// minimum seq is detected with probability (k-1)/k — measured 1-in-8 at k=2,
// effectively an unwatched assertion. k=8 makes it near-certain.
func TestQUM1000_MultiplePending_OnlyOldestSettles(t *testing.T) {
	f := newQUM1000Fixture(t)

	// All 8 are in flight before the CLI confirms the turn, so all are at or
	// below the watermark. The first enters phaseSubmitted; the rest queue.
	const n = 8
	uuids := make([]string, 0, n)
	for i := range n {
		uuid, err := f.rt.WriteUserPrompt(context.Background(), fmt.Sprintf("/qum1000-nope-%d", i), "next")
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		uuids = append(uuids, uuid)
	}
	runningTransition(f.rt)
	cleanTerminal(t, f.rt)

	events := drainNow(f.ch)
	if got := entryState(t, f.rt, uuids[0]); got.state != stateConsumed {
		t.Errorf("oldest entry after clean terminal: state=%s, want stateConsumed", stateName(got.state))
	}
	if idx := indexOfConsumed(events, uuids[0]); idx < 0 {
		t.Errorf("no consume publish for the oldest entry; events=%v", eventNames(events))
	}
	for i, uuid := range uuids[1:] {
		if got := entryState(t, f.rt, uuid); got.state != statePending {
			t.Errorf("entry %d (queued behind the oldest) was also settled: state=%s, want statePending", i+1, stateName(got.state))
		}
		if idx := indexOfConsumed(events, uuid); idx >= 0 {
			t.Errorf("entry %d got a consume publish at idx %d; only the oldest may settle", i+1, idx)
		}
	}
}

// TestQUM1000_SecondPreRunningSubmit_StaysRecallable is the regression byte's
// review caught: writeMessage stamps seq at SUBMIT time, so two prompts typed
// back-to-back before the wire `running` arrives BOTH land at or below the
// watermark — yet the CLI executes them across two turns. The first turn's
// terminal must not settle the second prompt, because a settled entry is invisible
// to snapshotPendingUser and therefore to Ctrl+U recall / Ctrl+G send-all-now.
//
// Kills: removing the turnAcked gate at the sweep call site.
func TestQUM1000_SecondPreRunningSubmit_StaysRecallable(t *testing.T) {
	f := newQUM1000Fixture(t)

	first := writePendingUser(t, f.rt, f.mock, "first prompt", "next")
	second := writePendingUser(t, f.rt, f.mock, "second prompt", "next")
	runningTransition(f.rt)
	// T1 executes and acks only the first prompt.
	replayEcho(t, f.rt, first)
	cleanTerminal(t, f.rt)

	if got := entryState(t, f.rt, second); got.state != statePending {
		t.Fatalf("the second pre-running submit was settled by T1: state=%s, want statePending", stateName(got.state))
	}
	if n := consumedFor(drainNow(f.ch), second); n != 0 {
		t.Errorf("EventUserMessageConsumed for the second submit = %d, want 0", n)
	}
	// The property the no-sweep protects: it is still recallable.
	text, err := f.rt.Recall(context.Background())
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if text != "second prompt" {
		t.Errorf("Recall = %q, want %q — the early settle made a queued prompt unrecallable", text, "second prompt")
	}
}

// TestQUM1000_MidTurnIdleThenRunning_DoesNotReMark: a mid-turn wire `idle`
// (QUM-927's turn-boundary shape: the phase reads idle while the frame turn stays
// open) followed by another `running` must not re-take the watermark. The phase
// check alone reads "fresh" there; the openTurnID check is what suppresses it.
//
// Kills: dropping `&& rt.openTurnID == 0` from the running-transition gate.
func TestQUM1000_MidTurnIdleThenRunning_DoesNotReMark(t *testing.T) {
	f := newQUM1000Fixture(t)

	runningTransition(f.rt) // running (mark=0) → init (frame turn open)
	uuidB, err := f.rt.WriteUserPrompt(context.Background(), "queued mid-turn", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	// The QUM-927 shape: phase goes idle while the frame turn is still open, then
	// the CLI reports running again for the same turn.
	f.rt.routeFrame(stateFrame(protocol.SessionStateIdle))
	f.rt.routeFrame(stateFrame(protocol.SessionStateRunning))
	cleanTerminal(t, f.rt)

	if got := entryState(t, f.rt, uuidB); got.state != statePending {
		t.Errorf("the mid-turn idle→running doublet re-took the watermark and settled a queued prompt: state=%s, want statePending", stateName(got.state))
	}
	if n := consumedFor(drainNow(f.ch), uuidB); n != 0 {
		t.Errorf("EventUserMessageConsumed for the queued prompt = %d, want 0", n)
	}
}

// TestQUM1000_EchoedPrompt_SettlesExactlyOnce is the no-regression control for
// accepted input (`/model`, ordinary prose): the isReplay echo settles it, and
// the later sweep must NOT publish a second consume.
func TestQUM1000_EchoedPrompt_SettlesExactlyOnce(t *testing.T) {
	f := newQUM1000Fixture(t)

	uuid, err := f.rt.WriteUserPrompt(context.Background(), "/model", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	runningTransition(f.rt)
	replayEcho(t, f.rt, uuid)
	if got := entryState(t, f.rt, uuid); got.state != stateConsumed {
		t.Fatalf("after isReplay echo: state=%s, want stateConsumed", stateName(got.state))
	}
	cleanTerminal(t, f.rt)
	f.rt.routeFrame(stateFrame(protocol.SessionStateIdle))

	events := drainNow(f.ch)
	if n := consumedFor(events, uuid); n != 1 {
		t.Errorf("EventUserMessageConsumed for %s = %d, want exactly 1 (double settle); events=%v", uuid, n, eventNames(events))
	}
}

// TestQUM1000_PromptSubmittedAfterRunningMark_NotSwept is the submit-order guard.
// A prompt submitted while a turn is already running (e.g. at a turn boundary,
// with the previous turn's trailing terminal still inbound) was NOT in flight
// when that turn began, so that turn's terminal says nothing about it. Deleting
// the `seq <= lastRunningMark` comparison — a bare turn-end trigger — is exactly
// the QUM-927/QUM-935 premise class (see retireUnclaimedNextArmLocked's scar).
func TestQUM1000_PromptSubmittedAfterRunningMark_NotSwept(t *testing.T) {
	f := newQUM1000Fixture(t)

	// T1 is already running when B is submitted.
	runningTransition(f.rt)
	uuidB, err := f.rt.WriteUserPrompt(context.Background(), "prompt B", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	cleanTerminal(t, f.rt)
	f.rt.routeFrame(stateFrame(protocol.SessionStateIdle))

	if got := entryState(t, f.rt, uuidB); got.state != statePending {
		t.Fatalf("B swept by T1's terminal: state=%s, want statePending", stateName(got.state))
	}
	if n := consumedFor(drainNow(f.ch), uuidB); n != 0 {
		t.Errorf("EventUserMessageConsumed for B = %d, want 0", n)
	}

	// B settles normally via its own echo in T2.
	runningTransition(f.rt)
	replayEcho(t, f.rt, uuidB)
	if got := entryState(t, f.rt, uuidB); got.state != stateConsumed {
		t.Errorf("B after its own echo: state=%s, want stateConsumed", stateName(got.state))
	}
}

// TestQUM1000_MidTurnQueuedPrompt_SurvivesTurnAndSettlesViaOwnEcho covers the
// priority:"next" queue-while-busy case, and additionally drives a duplicate
// `running` frame mid-turn (the QUM-903 resume-boundary doublet) to prove the
// watermark advances only on a genuine TRANSITION — re-marking on every running
// frame would drag it past the queued prompt and settle it a turn early.
func TestQUM1000_MidTurnQueuedPrompt_SurvivesTurnAndSettlesViaOwnEcho(t *testing.T) {
	f := newQUM1000Fixture(t)

	runningTransition(f.rt)
	uuidB, err := f.rt.WriteUserPrompt(context.Background(), "queued mid-turn", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	// Duplicate running while already running — must not re-mark.
	f.rt.routeFrame(stateFrame(protocol.SessionStateRunning))
	cleanTerminal(t, f.rt)
	f.rt.routeFrame(stateFrame(protocol.SessionStateIdle))

	if got := entryState(t, f.rt, uuidB); got.state != statePending {
		t.Fatalf("mid-turn queued prompt swept by T1: state=%s, want statePending", stateName(got.state))
	}
	if n := consumedFor(drainNow(f.ch), uuidB); n != 0 {
		t.Errorf("EventUserMessageConsumed for the queued prompt = %d, want 0", n)
	}

	runningTransition(f.rt)
	replayEcho(t, f.rt, uuidB)
	if got := entryState(t, f.rt, uuidB); got.state != stateConsumed {
		t.Errorf("queued prompt after its own echo in T2: state=%s, want stateConsumed", stateName(got.state))
	}
}

// TestQUM1000_RunningAfterSubmittedTimeout_Sweeps pins the idle→running half of
// the watermark hook, which is a LIVE production path, not a theoretical one: if
// the CLI takes longer than submittedPhaseTimeout to ack, guardSubmitted clears
// the synthetic phase to idle while the entry is still pending, so the wire
// `running` lands on an IDLE phase. Marking only on submitted→running would
// leave the watermark behind the entry and the ghost row would survive exactly as
// it does today.
func TestQUM1000_RunningAfterSubmittedTimeout_Sweeps(t *testing.T) {
	f := newQUM1000Fixture(t)
	withShortSubmittedTimeout(t, 20*time.Millisecond)

	uuid, err := f.rt.WriteUserPrompt(context.Background(), "/status", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.rt.State().InTurn {
		if time.Now().After(deadline) {
			t.Fatal("submitted-phase guard never cleared to idle; the test would exercise the submitted→running path instead")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := entryState(t, f.rt, uuid); got.state != statePending {
		t.Fatalf("entry left pending state before the turn even ran: state=%s", stateName(got.state))
	}

	runningTransition(f.rt)
	cleanTerminal(t, f.rt)

	if got := entryState(t, f.rt, uuid); got.state != stateConsumed {
		t.Errorf("idle→running watermark never advanced: state=%s, want stateConsumed", stateName(got.state))
	}
	if n := consumedFor(drainNow(f.ch), uuid); n != 1 {
		t.Errorf("EventUserMessageConsumed = %d, want exactly 1", n)
	}
}

// TestQUM1000_SystemMessageNeverSwept_NoOnDelivered: kind:system entries (inbox
// drains) carry maildir entryIDs, so sweeping one would durably record an
// undelivered inbox message as delivered. The sweep must never touch them. The
// positive control proves the spy can fire, so the zero-calls assertion is not
// vacuous.
func TestQUM1000_SystemMessageNeverSwept_NoOnDelivered(t *testing.T) {
	f := newQUM1000Fixture(t)

	uuid, err := f.rt.WriteSystemMessage(context.Background(), "<system-notification>inbox", "next", []string{"e1"})
	if err != nil {
		t.Fatalf("WriteSystemMessage: %v", err)
	}
	runningTransition(f.rt)
	cleanTerminal(t, f.rt)
	f.rt.routeFrame(stateFrame(protocol.SessionStateIdle))

	if got := entryState(t, f.rt, uuid); got.state != statePending {
		t.Errorf("kind:system entry was swept: state=%s, want statePending", stateName(got.state))
	}
	if got := f.deliveries(); len(got) != 0 {
		t.Errorf("OnDelivered fired from the sweep: %v, want no calls", got)
	}
	if n := consumedFor(drainNow(f.ch), uuid); n != 0 {
		t.Errorf("EventUserMessageConsumed for the system entry = %d, want 0", n)
	}

	// Positive control: the real ack path DOES deliver it, so the spy is wired.
	replayEcho(t, f.rt, uuid)
	got := f.deliveries()
	if len(got) != 1 || len(got[0]) != 1 || got[0][0] != "e1" {
		t.Errorf("OnDelivered after the isReplay echo = %v, want one call with [e1]", got)
	}
}

// TestQUM1000_InterruptedTerminal_DoesNotSweep: an Esc / now-write preempt
// leaves genuinely UNEXECUTED prompts pending, and Ctrl+U recall (QUM-824) can
// only rehydrate a statePending entry — settling them would silently make them
// unrecallable. Only the unarmed terminal legs sweep.
func TestQUM1000_InterruptedTerminal_DoesNotSweep(t *testing.T) {
	f := newQUM1000Fixture(t)

	uuid := writePendingUser(t, f.rt, f.mock, "unexecuted prompt", "next")
	runningTransition(f.rt)
	if err := f.rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	f.rt.routeFrame(resultFrame(t, true, 10), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	if got := entryState(t, f.rt, uuid); got.state != statePending {
		t.Fatalf("interrupted turn swept a pending prompt: state=%s, want statePending", stateName(got.state))
	}
	events := drainNow(f.ch)
	if n := consumedFor(events, uuid); n != 0 {
		t.Errorf("EventUserMessageConsumed on the interrupted leg = %d, want 0", n)
	}
	if indexOfType(events, EventInterrupted) < 0 {
		t.Errorf("no EventInterrupted; events=%v", eventNames(events))
	}

	// Recall still rehydrates it — the property the no-sweep protects.
	text, err := f.rt.Recall(context.Background())
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if text != "unexecuted prompt" {
		t.Errorf("Recall = %q, want %q", text, "unexecuted prompt")
	}
}

// TestQUM1000_OrphanTeardown_DoesNotSweep: a turn torn down with no terminal
// `result` (session close / fault) says nothing about whether the CLI executed
// the pending prompt, and the same recall argument as the interrupted leg
// applies. The sweep must sit below the orphan early-return in routeFrame.
func TestQUM1000_OrphanTeardown_DoesNotSweep(t *testing.T) {
	f := newQUM1000Fixture(t)

	uuid := writePendingUser(t, f.rt, f.mock, "orphaned prompt", "next")
	runningTransition(f.rt)
	f.rt.routeFrame(nil, backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	if got := entryState(t, f.rt, uuid); got.state != statePending {
		t.Fatalf("orphan teardown swept a pending prompt: state=%s, want statePending", stateName(got.state))
	}
	events := drainNow(f.ch)
	if n := consumedFor(events, uuid); n != 0 {
		t.Errorf("EventUserMessageConsumed on the orphan leg = %d, want 0", n)
	}
	if indexOfType(events, EventTurnFailed) < 0 {
		t.Errorf("no EventTurnFailed; events=%v", eventNames(events))
	}
}

// TestQUM1000_ErrorTerminalWithNoArm_Sweeps pins a deliberate choice rather than
// an accident. An is_error `result` with NO interrupt arm (a genuine model / API
// error, not a user abort) publishes EventTurnCompleted, so the sweep fires. That
// is intended: the CLI opened and finished a turn while the entry was in flight
// and never acked it, so no ack is coming and leaving the row would strand it
// exactly as a refusal does. The distinction the sweep respects is
// "did a user abort claim this turn" (Esc / now-write preempt ⇒ the prompt may be
// genuinely unexecuted and must stay recallable), not "was the result an error".
func TestQUM1000_ErrorTerminalWithNoArm_Sweeps(t *testing.T) {
	f := newQUM1000Fixture(t)

	uuid, err := f.rt.WriteUserPrompt(context.Background(), "/status", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	runningTransition(f.rt)
	f.rt.routeFrame(resultFrame(t, true, 10), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	if got := entryState(t, f.rt, uuid); got.state != stateConsumed {
		t.Errorf("unarmed is_error terminal: state=%s, want stateConsumed", stateName(got.state))
	}
	events := drainNow(f.ch)
	if n := consumedFor(events, uuid); n != 1 {
		t.Errorf("EventUserMessageConsumed = %d, want exactly 1; events=%v", n, eventNames(events))
	}
	if indexOfType(events, EventInterrupted) >= 0 {
		t.Errorf("unarmed is_error terminal published EventInterrupted; events=%v", eventNames(events))
	}
}

// TestQUM1000_NoRunningTransition_NothingSwept: with no wire `running` at all
// the watermark stays behind every write, so the sweep is inert by construction.
// Kills seeding lastRunningMark from outSeq (or an unguarded compare).
func TestQUM1000_NoRunningTransition_NothingSwept(t *testing.T) {
	f := newQUM1000Fixture(t)

	uuid, err := f.rt.WriteUserPrompt(context.Background(), "/status", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	feedInit(f.rt)
	cleanTerminal(t, f.rt)

	if got := entryState(t, f.rt, uuid); got.state != statePending {
		t.Errorf("swept with no running transition: state=%s, want statePending", stateName(got.state))
	}
	if n := consumedFor(drainNow(f.ch), uuid); n != 0 {
		t.Errorf("EventUserMessageConsumed = %d, want 0", n)
	}
}

// TestQUM1000_SystemInjectionDoesNotStrandUserPrompt covers the sweep's behaviour
// when a kind:system entry and a pending kind:user prompt are alive together — a
// combination no other test in this package constructs. Nothing here fails today.
//
// WHAT IT GUARDS, stated as an invariant rather than as an inventory of the tree:
// a kind:system write must not, by itself, move lastRunningMark; and the ack that
// suppresses the sweep must stay KIND-BLIND. Narrowing that ack to kind:user is
// the change this test exists to fail on. Both hold today and both are checkable
// from this file alone, so the guard does not depend on what else is merged.
//
// The configuration is LIVE, not prospective. QUM-925 slice A — which routes inbox
// drains through kind:system stdin writes — is in this tree, as this work's own
// base (664ff74 when written; a SHA is itself rebase-perishable, so the durable
// form of the claim is the predicate named next, which you can read in the file).
// Slice A deliberately did NOT widen the synthetic-phase
// predicate: writeMessage still reads `if kind == kindUser && rt.phase ==
// phaseIdle`, and the comment there records that widening it was CONSIDERED AND
// REJECTED, because the CLI takes a turn on any queued stdin message when idle, so
// the write alone drives the turn. Read the paragraphs below in the present tense.
//
// Recorded rather than silently corrected, because it is this file's own subject:
// an earlier version of this comment said slice A "is NOT in this tree", that
// `grep -rn QUM-925 internal/` "matches only this comment", and that slice A "was
// never merged". All three were true when written; all three were falsified by a
// rebase onto slice A, without one character of this file changing (that grep
// returns 64 hits here, across internal/runtime, internal/tui, internal/supervisor).
// The claims did not rot — their BASE moved. Any claim here about what the rest of
// the tree contains is a claim about a moving target; claims about the invariants
// above are not, which is why the guard is stated that way.
//
// Both subtests assert the same observable property — a queued prompt stays
// PENDING and RECALLABLE — rather than reading lastRunningMark. That is
// deliberate: /testing-practices records three tests on this area that pinned
// mechanism instead of outcome and thereby ratified the defect they were meant
// to constrain. Recall is the real surface, because snapshotPendingUser filters
// on statePending, so an early settle silently removes a prompt from Ctrl+U.
//
// Naming the two hazards separately matters because they have very different
// likelihoods, and crediting them equally would overstate the second:
//
//   - "a kind:system write must not move the watermark" is structurally true
//     today and was NOT broken by slice A's system path. lastRunningMark has
//     one writer (noteRunningTransition) with one caller, reachable only from
//     routeFrame's StateChange==running branch; widening that site would set
//     phaseSubmitted instead of phaseIdle, and the `fresh` gate tests
//     `phase != phaseRunning`, which both satisfy. Subtest A's unique kill is only
//     a kind:system-SPECIFIC watermark write — a kind-blind one is already caught
//     by NoRunningTransition_NothingSwept — so as a bug guard it is close to
//     ceremony, and it is rated low-plausibility rather than sold as one.
//
//     The present-tense reason to keep it: A and B are a matched pair that makes
//     the watermark's role checkable from the tests alone — A (no transition →
//     safe), B (transition + ack → safe), and the QUM-1033 residual below
//     (transition, no ack → settles) bracket it. That is checkable today; "guards a
//     change that has not landed" would not be. A is NOT a control for B — they
//     differ in two variables (the running transition AND the echo), so it cannot
//     serve as one.
//
//   - the KIND-BLIND ACK is the live exposure. outSeq is kind-blind, so a prompt
//     queued before an inbox write lands UNDER the watermark once the injected
//     turn goes running. What keeps it recallable is discriminator (2): the
//     system entry's own isReplay echo calls noteTurnAcked, so the turn "acked
//     something" and the sweep is skipped. That ack is kind-blind today, and
//     narrowing it to kind:user — a plausible tidy-up for someone maintaining the
//     kind:system path slice A landed — strands the prompt. Subtest B earns its keep.
//
// Residual, stated rather than implied: if a system-injected turn ends WITHOUT
// consuming the notification (no echo, hence no ack) while a user prompt sits under
// the watermark, the sweep DOES settle that prompt and costs Ctrl+U recall. That is
// QUM-1033, listed by that key under settleNeverAcked's EARLY-settle residuals
// (referenced by key, not by position in that list — an earlier version of this
// comment pointed at "documented residual #2", which was a DELAYED-settle item, so
// the pointer named the wrong hazard class). It is reachable without slice A, which
// raises the exposure by making kind:system turns common rather than an edge, and
// needs per-turn uuid attribution to close — a design change, not a test. Subtest B
// pins the common case; it does not remove that.
func TestQUM1000_SystemInjectionDoesNotStrandUserPrompt(t *testing.T) {
	t.Run("system write alone does not advance the watermark", func(t *testing.T) {
		f := newQUM1000Fixture(t)

		prompt := writePendingUser(t, f.rt, f.mock, "queued prompt", "next") // seq=1
		if _, err := f.rt.WriteSystemMessage(context.Background(), "<system-notification>inbox", "next", []string{"e1"}); err != nil {
			t.Fatalf("WriteSystemMessage: %v", err)
		} // seq=2

		// NO runningTransition, deliberately: the watermark must stay 0 even though
		// two entries have been written. feedInit opens a frame turn WITHOUT a wire
		// `running`, so the terminal below still reaches settleNeverAcked (the turn
		// acked nothing) and only the seq > lastRunningMark compare stops it.
		//
		// Swapping feedInit for runningTransition here makes all three legs fail,
		// but do NOT read that as this test guarding the transition: that
		// configuration (running, no echo) is the QUM-1033 residual in the doc
		// above, NOT subtest B — B is running PLUS the echo. In that configuration this
		// subtest's own failure message would also be wrong, since the running
		// transition moved the watermark, not the kind:system write.
		feedInit(f.rt)
		cleanTerminal(t, f.rt)

		if got := entryState(t, f.rt, prompt); got.state != statePending {
			t.Errorf("a kind:system write advanced the watermark and the sweep settled a queued prompt: state=%s, want statePending", stateName(got.state))
		}
		if n := consumedFor(drainNow(f.ch), prompt); n != 0 {
			t.Errorf("EventUserMessageConsumed for the queued prompt = %d, want 0", n)
		}
		if got := f.deliveries(); len(got) != 0 {
			t.Errorf("OnDelivered fired without any consumption ack: %v, want no calls", got)
		}
		// Recall mutates state, so it is asserted last.
		text, err := f.rt.Recall(context.Background())
		if err != nil {
			t.Fatalf("Recall: %v", err)
		}
		if text != "queued prompt" {
			t.Errorf("Recall = %q, want %q — the prompt was settled early and is no longer recallable", text, "queued prompt")
		}
	})

	t.Run("a system entry's echo counts as this turn's ack", func(t *testing.T) {
		f := newQUM1000Fixture(t)

		prompt := writePendingUser(t, f.rt, f.mock, "queued prompt", "next") // seq=1
		system, err := f.rt.WriteSystemMessage(context.Background(), "<system-notification>inbox", "next", []string{"e1"})
		if err != nil {
			t.Fatalf("WriteSystemMessage: %v", err)
		} // seq=2

		// The injected turn goes genuinely running, so the watermark advances to 2
		// and the prompt (seq=1) is UNDER it — this subtest depends on that being
		// true, which is why it drives the transition subtest A withholds.
		runningTransition(f.rt)
		// The CLI consumed the notification, not the prompt.
		replayEcho(t, f.rt, system)
		cleanTerminal(t, f.rt)

		// Anti-vacuity: prove the echo actually landed. Without this, a replayEcho
		// that silently missed would leave the prompt pending for the WRONG reason
		// (no ack, but also no sweep trigger observed) and the subtest would stay
		// green under the very mutation it exists to catch.
		if got := f.deliveries(); len(got) != 1 || len(got[0]) != 1 || got[0][0] != "e1" {
			t.Fatalf("OnDelivered after the system echo = %v, want one call with [e1] — the echo did not land, so this subtest would prove nothing", got)
		}

		if got := entryState(t, f.rt, prompt); got.state != statePending {
			t.Errorf("the system entry's ack was not counted, so the sweep stranded a queued prompt under the watermark: state=%s, want statePending", stateName(got.state))
		}
		if n := consumedFor(drainNow(f.ch), prompt); n != 0 {
			t.Errorf("EventUserMessageConsumed for the queued prompt = %d, want 0", n)
		}
		text, err := f.rt.Recall(context.Background())
		if err != nil {
			t.Fatalf("Recall: %v", err)
		}
		if text != "queued prompt" {
			t.Errorf("Recall = %q, want %q — a kind-blind ack narrowed to kind:user made a queued prompt unrecallable", text, "queued prompt")
		}
	})
}

// assertOnlyQCancelled splits what was one bundled assertion into its two
// opposite legs, because a single message could not name both without lying in
// one direction: under a mutation where Q is NOT cancelled it printed
// "cancelled uuids = []" while blaming an extra cancel that never happened.
//
//	(a) the swept prompt P produced NO cancel request — the property this test
//	    owns, and the whole answer to "un-cancellable, not dropped";
//	(b) Q WAS cancelled — the fixture-liveness leg, proving the spy can record a
//	    cancel at all, so (a) is silence-with-meaning rather than an inert mock.
func assertOnlyQCancelled(t *testing.T, cancelled []string, promptP, promptQ string) {
	t.Helper()
	for _, uuid := range cancelled {
		if uuid == promptP {
			t.Errorf("cancel_async_message issued for the SWEPT prompt %s (cancelled=%v) — sprawl must never ask the CLI to drop it; the sweep only loses sprawl's own handle on it", promptP, cancelled)
		}
	}
	found := false
	for _, uuid := range cancelled {
		if uuid == promptQ {
			found = true
		}
	}
	if !found {
		t.Errorf("no cancel_async_message issued for the still-pending prompt %s (cancelled=%v) — the spy records nothing at all here, so the absence of a cancel for the swept prompt proves nothing", promptQ, cancelled)
	}
	if len(cancelled) != 1 {
		t.Errorf("cancelled uuids = %v, want exactly one (the still-pending prompt)", cancelled)
	}
}

// TestQUM1000_WrongfulSweep_LosesRecallNotDelivery answers the question the
// land was gated on: when the sweep WRONGLY settles a queued kind:user prompt
// — the QUM-1033 residual, a system-injected turn that acks nothing while a
// user prompt sits under the watermark — is that prompt still delivered to the
// model, or is it LOST? Those are opposite harms: a false render plus a silent
// loss of Ctrl+U/Ctrl+G is degraded UX; a dropped prompt is data loss in a path
// this change introduces.
//
// ANSWER, as observed here: UNRECALLABLE, NOT DROPPED — where "not dropped" is
// bounded, not proven; see NOT OBSERVED below. The prompt's bytes are on stdin
// before the sweep can run, the sweep performs zero session calls, and sprawl's
// ONLY retraction mechanism (cancel_async_message, sole production caller
// cancelPendingUser) is never invoked for it. What the user loses is the ability
// to STOP it: Recall returns nothing for that prompt because it never asks the
// CLI to cancel it, not because the message is gone.
//
// WHY THE ARMS ARE SHAPED THIS WAY (the vacuity trap). "The prompt was
// delivered" is unobservable against a mock and would be fixture-supplied, so
// this test does not assert it. It asserts a DIFFERENCE: the swept and control
// arms drive the same two stdin writes and differ by exactly one frame (the
// system entry's echo, which supplies this turn's ack and suppresses the
// sweep). Each arm pins its own wire trace to its own pre-turn snapshot — zero
// cancels, zero interrupts, byte-identical writes — and the two snapshots are
// identical IN SHAPE by construction (same two writes, differing only in
// generated uuids); no assertion compares one arm's trace to the other's, and
// none could. Meanwhile their LOCAL state diverges completely: swept ⇒
// stateConsumed and Recall skips it; not-swept ⇒ statePending and Recall
// cancels it and hands the text back. A sweep that retracted anything would
// break the swept arm's trace equality while both gates still pass, so the
// assertion distinguishes "unchanged despite the flip" from "unchanged because
// nothing happened".
//
// The control controls the FIRST HALF only. Its one extra frame has three
// effects — noteTurnAcked, the system entry's own flip, and OnDelivered — of
// which only noteTurnAcked can reach the discriminator (the sweep is
// kind:user-only, so the system entry's state is irrelevant to it). After the
// gate the control arm returns; the swept arms run T2 / Recall / SendAllNow
// with no counterpart. Its first half is deliberately frame-for-frame identical
// to subtest B of SystemInjectionDoesNotStrandUserPrompt: this is that fixture
// plus the wire spy and the post-Recall cancel assertions. If one moves, move
// both.
//
// OBSERVED IN-PROCESS (sprawl's side of the channel):
//   - the prompt reached Session.WriteUserMessage with the right uuid, text and
//     priority BEFORE any turn frame — asserted, not assumed;
//   - the sweep makes no session call of any kind: SessionHandle has exactly
//     three methods (WriteUserMessage, Interrupt, CancelAsyncMessage) and all
//     three are spied, so "zero session calls" is complete rather than partial;
//   - the entry survives with its text, and a LATER ack for it is still accepted
//     and still counts as that turn's ack, so the flip does not cascade onto the
//     next queued prompt;
//   - Recall and SendAllNow exclude it silently, issuing no cancel request for
//     it and never resending it.
//
// NOT OBSERVED — bounded, and say so rather than overclaiming: that the real
// CLI still holds and later executes the queued bytes. replayEcho is
// fixture-supplied; the T2 block proves only that sprawl HANDLES such an echo,
// never that it arrives. The bound has two legs:
//
//   - structural: the sweep puts nothing on the wire, so the CLI cannot observe
//     it, and the queued bytes are indistinguishable to the CLI from any other
//     queued write;
//   - corpus: measured 2026-08-03 over the 738 recorded session wire logs in
//     this workspace's .sprawl/logs/sessions (46 agents), 116 priority:"next"
//     stdin writes across 50 sessions had their isReplay echo arrive only AFTER
//     an intervening `result` terminal — the CLI demonstrably holds a queued
//     message across a turn boundary and runs it in a later turn. Reproduce by
//     parsing each .ndjson line's .raw as JSON and keying on
//     {dir,type,uuid,isReplay,priority}; do NOT commit excerpts, the logs carry
//     real prompt text. NOT reproducible from the repo — those logs are
//     workspace-local, gitignored and rotating, so treat the figure as a dated
//     observation rather than a re-runnable check. That statistic evidences the
//     CLI's queue behaviour; it
//     says nothing about a prompt sprawl has locally re-labelled, which is the
//     structural leg's job.
//
// Live end-to-end closure is the qum1000-refused-slash row's job, not this
// test's.
//
// Mutation kills. Each was applied to the working implementation and the whole
// internal/runtime package re-run, so "unique" means nothing else in the package
// caught it — not merely nothing else named QUM-1000:
//
//	M1 sweep also cancels the swept uuid     — WIRE-2. UNIQUE; no other test in
//	   (adds a CancelAsyncMessage call         the package snapshots cancelCalls
//	    to settleNeverAcked)                   across a sweep. Printed WIRE-2's
//	                                           "cancel_async_message issued for
//	                                           [<uuid>], want none" plus
//	                                           assertOnlyQCancelled's leg (a).
//	M3 noteTurnAcked only when markConsumed  — NO CASCADE. UNIQUE; subtest B of
//	   actually flipped a pending entry        SystemInjectionDoesNotStrandUser-
//	                                           Prompt survives it because its
//	                                           system entry IS pending at echo
//	                                           time. Printed: "Q after T2:
//	                                           state=stateConsumed, want
//	                                           statePending", and separately leg
//	                                           (b): "no cancel_async_message issued
//	                                           for the still-pending prompt … the
//	                                           spy records nothing at all here".
//	                                           Those two legs point in OPPOSITE
//	                                           directions — which is exactly why
//	                                           they are separate assertions. One
//	                                           message naming both would have to
//	                                           lie in one of them, and did: before
//	                                           the split it blamed an extra cancel
//	                                           while printing an EMPTY cancel list.
//	M5 snapshotPendingUser also returns      — RECALL and SENDALLNOW. NOT unique:
//	   stateConsumed entries (the tempting     it also fails TestRecall_OnlyPending-
//	   "fix" for QUM-1033)                     UserRehydrates_TwoAckModels, the
//	                                           older and more direct guard. Listed
//	                                           anyway because it is the highest-
//	                                           consequence mutation of the three:
//	                                           it is what would turn this render
//	                                           bug into REAL data loss, by making
//	                                           Ctrl+U cancel — and Ctrl+G resend —
//	                                           a message the CLI is about to
//	                                           execute. Printed, in both swept
//	                                           arms: "cancel_async_message issued
//	                                           for the SWEPT prompt <uuid>". Note
//	                                           what M5 does NOT
//	                                           reach: flipPending's statePending
//	                                           guard withholds the swept text, so
//	                                           SendAllNow still resends only "prompt
//	                                           Q" and the resent-payload assertion
//	                                           stays green. Under M5 the observable
//	                                           harm is the CANCEL REQUEST for a
//	                                           message the CLI is about to run, not
//	                                           a duplicate resend — the payload
//	                                           assertion pins the resend shape (it
//	                                           fires under M3, which leaves nothing
//	                                           pending to resend), not M5.
//
// M4 (markConsumed's Publish moved inside the statePending guard) flips the
// post-sweep consume count 2→1. That is an intentional-change tripwire, not a
// defect kill: the property under test is that a post-sweep ack is not
// REJECTED, not the count.
//
// Assertions kept as BASELINE rather than as evidence, labelled so they are not
// counted as kills: interruptCount()==0 (nothing in routeFrame can reach
// Session.Interrupt), the EventUserMessageCancelled scan (only cancelPendingUser
// publishes it, and it has not run at that point), and the control arm's trace
// equality (no sweep runs there at all). The SWEPT arms' trace equality is live
// — a re-added QUM-929-style stdin nudge at turn-end would break it.
func TestQUM1000_WrongfulSweep_LosesRecallNotDelivery(t *testing.T) {
	cases := []struct {
		name string
		ack  bool   // route the system entry's echo, supplying this turn's ack
		tail string // "recall" | "sendnow" — how the user tries to reach the prompt
	}{
		{name: "swept-then-recall", ack: false, tail: "recall"},
		{name: "swept-then-sendallnow", ack: false, tail: "sendnow"},
		// The control arm returns inside the tc.ack branch, before the tail switch,
		// so it has no tail — an empty string here rather than dead data.
		{name: "control-turn-acked", ack: true, tail: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newQUM1000Fixture(t)

			// drainNow is destructive and this test drains three times; accumulate
			// so every count runs over the union rather than over what is left.
			var all []RuntimeEvent
			drain := func() { all = append(all, drainNow(f.ch)...) }

			promptP := writePendingUser(t, f.rt, f.mock, "prompt P", "next") // seq=1
			system, err := f.rt.WriteSystemMessage(ctx, "<system-notification>inbox", "next", []string{"e1"})
			if err != nil {
				t.Fatalf("WriteSystemMessage: %v", err)
			} // seq=2

			// WIRE-1: the stdin write ACTUALLY happened, asserted before any turn
			// frame. Everything below is meaningless if the bytes never left.
			before := f.mock.writeTrace()
			if len(before) != 2 {
				t.Fatalf("stdin writes = %d, want 2 — the test cannot ask whether a written prompt survives the sweep if it was never written", len(before))
			}
			if w := before[0]; w.UUID != promptP || w.Message.Content != "prompt P" || w.Priority != "next" {
				t.Fatalf("first stdin write = {uuid:%s content:%q priority:%q}, want {uuid:%s content:%q priority:next}", w.UUID, w.Message.Content, w.Priority, promptP, "prompt P")
			}
			if before[1].UUID != system {
				t.Fatalf("second stdin write uuid = %s, want the system entry %s", before[1].UUID, system)
			}

			// The injected turn goes genuinely running, so the watermark advances to
			// 2 and P (seq=1) is UNDER it — the residual's precondition.
			runningTransition(f.rt)
			if tc.ack {
				// The CLI consumed the notification: this turn acked something.
				replayEcho(t, f.rt, system)
			}
			cleanTerminal(t, f.rt)
			f.rt.routeFrame(stateFrame(protocol.SessionStateIdle))
			drain()

			// GATE: each arm must actually reach its configuration, or every wire
			// assertion below is trivially true in both.
			if tc.ack {
				if got := entryState(t, f.rt, promptP); got.state != statePending {
					t.Fatalf("P after an ACKED turn: state=%s, want statePending — the acked-nothing gate is broken and the sweep ran when it must not", stateName(got.state))
				}
				if n := consumedFor(all, promptP); n != 0 {
					t.Fatalf("EventUserMessageConsumed for P = %d, want 0 on an acked turn; events=%v", n, eventNames(all))
				}
				// Anti-vacuity: prove the suppressing echo landed. Without this, an
				// echo that silently missed would leave P pending for the WRONG
				// reason and this arm would be a control over nothing.
				if got := f.deliveries(); len(got) != 1 || len(got[0]) != 1 || got[0][0] != "e1" {
					t.Fatalf("OnDelivered after the system echo = %v, want one call with [e1] — the echo did not land", got)
				}
			} else {
				if got := entryState(t, f.rt, promptP); got.state != stateConsumed {
					t.Fatalf("P after an UNACKED turn: state=%s, want stateConsumed — the wrongful sweep this test exists to characterise did not happen", stateName(got.state))
				}
				if n := consumedFor(all, promptP); n != 1 {
					t.Fatalf("EventUserMessageConsumed for P = %d, want 1 from the sweep; events=%v", n, eventNames(all))
				}
				// Baseline (kind filter, owned by SystemMessageNeverSwept_NoOnDelivered).
				if got := f.deliveries(); len(got) != 0 {
					t.Fatalf("OnDelivered fired without any consumption ack: %v, want no calls", got)
				}
			}

			// WIRE-2: the sweep emitted NOTHING on the wire.
			if got := f.mock.writeTrace(); !reflect.DeepEqual(got, before) {
				t.Errorf("stdin write history changed across the terminal:\n got %+v\nwant %+v — the turn terminal must not touch the wire", got, before)
			}
			if got := f.mock.cancelledUUIDs(); len(got) != 0 {
				t.Errorf("cancel_async_message issued for %v, want none — that is sprawl's ONLY retraction channel, and the turn terminal must not use it", got)
			}
			if n := f.mock.interruptCount(); n != 0 { // baseline
				t.Errorf("Interrupt calls = %d, want 0", n)
			}

			// Baseline: the entry survives with its text. OutstandingEntry.text and
			// .kind are written once, in writeMessage's struct literal, and never
			// reassigned — so no mutation of the sweep can make this fire (a
			// delete-the-entry sweep is caught by entryState's own Fatalf, above).
			if got := entryState(t, f.rt, promptP); got.text != "prompt P" || got.kind != kindUser {
				t.Errorf("P after the terminal: text=%q kind=%s, want %q/kindUser", got.text, kindName(got.kind), "prompt P")
			}

			if tc.ack {
				// Control: with the turn acked, P was never swept and Recall retracts
				// it for real — the same spy, on the same mock, DOES record a cancel.
				// That is what makes the swept arms' silence attributable to the flip
				// rather than to an inert fixture.
				text, err := f.rt.Recall(ctx)
				if err != nil {
					t.Fatalf("Recall: %v", err)
				}
				if text != "prompt P" {
					t.Errorf("Recall = %q, want %q", text, "prompt P")
				}
				if got := f.mock.cancelledUUIDs(); len(got) != 1 || got[0] != promptP {
					t.Errorf("cancelled uuids = %v, want exactly [%s] — an unswept prompt must be genuinely retractable", got, promptP)
				}
				if got := entryState(t, f.rt, promptP); got.state != stateCancelled {
					t.Errorf("P after Recall: state=%s, want stateCancelled", stateName(got.state))
				}
				return
			}

			// --- swept arms: T2, a later turn that DOES ack the swept prompt ------
			promptQ := writePendingUser(t, f.rt, f.mock, "prompt Q", "next") // seq=3
			runningTransition(f.rt)                                          // watermark → 3, so Q is under it too
			replayEcho(t, f.rt, promptP)
			cleanTerminal(t, f.rt)
			f.rt.routeFrame(stateFrame(protocol.SessionStateIdle))
			drain()

			// The late ack for an already-swept entry is ACCEPTED, not rejected: the
			// entry is still there to ack. Two causes for a count of 1: the echo did
			// not land, or markConsumed's Publish was moved inside its statePending
			// guard (M4 — an intentional change, not a defect). Either way the
			// no-cascade assertion below would no longer prove what it claims.
			if n := consumedFor(all, promptP); n != 2 {
				t.Fatalf("EventUserMessageConsumed for P = %d, want 2 (the sweep's, plus the CLI's later ack); events=%v", n, eventNames(all))
			}
			// NO CASCADE: that late ack counts as T2's ack, so T2's terminal does not
			// sweep Q — which sits under the watermark and would otherwise be the
			// next prompt made unrecallable.
			if got := entryState(t, f.rt, promptQ); got.state != statePending {
				t.Errorf("Q after T2: state=%s, want statePending — an ack for an already-swept entry stopped counting and the sweep cascaded onto the next queued prompt", stateName(got.state))
			}
			if n := consumedFor(all, promptQ); n != 0 {
				t.Errorf("EventUserMessageConsumed for Q = %d, want 0", n)
			}
			for _, ev := range all { // baseline: only cancelPendingUser publishes this, and it has not run
				if ev.Type == EventUserMessageCancelled {
					t.Errorf("EventUserMessageCancelled published for %s; nothing up to this point cancels", ev.UUID)
				}
			}

			// The discriminator, and the plain-English answer: the user tries to
			// reach the swept prompt, by either route, and cannot. Last, because both
			// routes mutate state.
			if !tc.ack && tc.tail != "recall" && tc.tail != "sendnow" {
				t.Fatalf("unknown tail %q — a typo must not silently take the recall path", tc.tail)
			}
			if tc.tail == "sendnow" {
				// Ctrl+G: cancel everything still pending and resend it as one
				// now-write. P must not be in it — the CLI still holds the original,
				// so resending it would DUPLICATE the prompt rather than recover it.
				n0 := len(f.mock.writeTrace())
				if err := f.rt.SendAllNow(ctx); err != nil {
					t.Fatalf("SendAllNow: %v", err)
				}
				trace := f.mock.writeTrace()
				if len(trace) != n0+1 {
					t.Fatalf("SendAllNow made %d new stdin write(s), want exactly 1 (Q, which is still pending)", len(trace)-n0)
				}
				if w := trace[len(trace)-1]; w.Message.Content != "prompt Q" || w.Priority != "now" {
					t.Errorf("resent now-write = {content:%q priority:%q}, want {content:%q priority:now} — the swept prompt must not be resent", w.Message.Content, w.Priority, "prompt Q")
				}
				assertOnlyQCancelled(t, f.mock.cancelledUUIDs(), promptP, promptQ)
				return
			}

			// Ctrl+U: recall everything still pending. P is absent and, crucially,
			// no cancel is issued for it.
			text, err := f.rt.Recall(ctx)
			if err != nil {
				t.Fatalf("Recall: %v", err)
			}
			if text != "prompt Q" {
				t.Errorf("Recall = %q, want %q — P must not come back (it is genuinely consumed by now) and Q must", text, "prompt Q")
			}
			assertOnlyQCancelled(t, f.mock.cancelledUUIDs(), promptP, promptQ)
			if got := entryState(t, f.rt, promptP); got.state != stateConsumed {
				t.Errorf("P after Recall: state=%s, want stateConsumed (untouched)", stateName(got.state))
			}
			if got := entryState(t, f.rt, promptQ); got.state != stateCancelled {
				t.Errorf("Q after Recall: state=%s, want stateCancelled", stateName(got.state))
			}
		})
	}
}
