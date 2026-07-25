package tui

// QUM-986 — the third door onto the QUM-933/QUM-975 strand class: a turn-ending
// path that marks the turn idle WITHOUT finalising the in-flight assistant item.
//
// QUM-933 built the settle chokepoint (any content append settles the trailing
// assistant). QUM-975 taught FinalizeAssistantMessage to drop a trailing
// ThinkingItem first. Both are reachable only from finalizeTurn / the terminal
// SessionResultMsg + InterruptCompletedMsg handlers. The restart path never
// calls finalizeTurn at all:
//
//	SessionErrorMsg{io.EOF} (app.go:1551)   ─┐
//	HandoffRequestedMsg     (app.go:1566)   ─┼─> SessionRestartingMsg + RestartSessionMsg
//	cmd/enter.go:935 resume-failure         ─┘
//
// ...and SessionRestartingMsg only did ClearZone() + setTurnState(TurnIdle). So
// after `chunk -> AppendThinking -> EOF` the items are
// [assistant(unfinished), thinking] with turnState == TurnIdle: a non-tail
// unfinished assistant (permanently uncacheable, re-runs the whole
// goldmark+glamour pipeline every rebuild) rendering a stray ▍ streaming cursor
// in a transcript whose session is not running.
//
// The fix finalises in the SessionRestartingMsg reducer, so all three entry
// doors are covered at one chokepoint. The first two doors are driven
// end-to-end below (reducer batch executed, resulting msgs fed back); the
// cmd/enter.go door is out of this package and is covered by the chokepoint
// argument plus the empty-buffer no-op test.
//
// The fix deliberately does NOT use m.finalizeTurn() — see the comment at the
// call site and TestSessionRestarting_DoesNotRearmThePump below.
//
// Not discriminated by these tests: pushing the finalise down into
// ChatList.ClearZone() or setTurnState() would also pass. Both have a single
// non-test caller today (app.go:1593 / the turn-state machine), so the reducer
// is the honest place; noted so a future reader doesn't mistake silence for
// approval.

import (
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// strandedApp builds a sized, continuous-bridge AppModel in exactly the state
// the bug needs: streamed assistant text followed by a thinking block, so the
// assistant item is unfinished and no longer the tail.
//
// It asserts the bad state was actually CONSTRUCTED. Without these
// preconditions the post-assertions are vacuous — in particular a cursor count
// of 0 is trivially true of an empty render (ChatList.Render returns "" when
// width <= 0), so the render is probed for the assistant's own text here.
func strandedApp(t *testing.T) (AppModel, *fakeSessionBackend) {
	t.Helper()
	app, fake := idleTrackingApp(t)
	// finalizeTurn only re-arms the pump when the bridge is continuous, so the
	// waitCalls assertion in TestSessionRestarting_DoesNotRearmThePump can only
	// constrain anything with this set. TestSessionRestarting_PumpProbeIsLive
	// proves the probe moves.
	fake.SetContinuous(true)

	app = deliver(t, app, AssistantTextMsg{Text: "partial answer"})
	app = deliver(t, app, ThinkingMsg{})

	// Pinned so the turnState leg of the primary test stays non-vacuous: the
	// standalone AssistantTextMsg reducer sets TurnStreaming, so TurnIdle
	// afterwards is a real transition, not the initial value.
	if app.turnState != TurnStreaming {
		t.Fatalf("precondition: turnState = %v, want TurnStreaming", app.turnState)
	}
	cl := rootChat(app)
	if got := cl.Len(); got != 2 {
		t.Fatalf("precondition: Len() = %d, want 2 ([assistant, thinking])", got)
	}
	if got := cl.OrphanCount(); got != 1 {
		t.Fatalf("precondition: OrphanCount() = %d, want 1 (the strand must exist before we test that it is cleared)", got)
	}
	if !cl.HasPendingAssistant() {
		t.Fatal("precondition: HasPendingAssistant() = false, want true")
	}
	rendered := stripANSI(cl.Render(100))
	if !strings.Contains(rendered, "partial answer") {
		t.Fatalf("precondition: render does not contain the assistant text; a cursor count over this render would be vacuous.\nrender:\n%s", rendered)
	}
	if got := cursorCount(cl, 100); got != 1 {
		t.Fatalf("precondition: cursorCount() = %d, want 1 (the stray cursor must be present before we test that it is gone)", got)
	}
	return app, fake
}

// assertSettled pins the post-restart invariant. Only cursorCount is a
// genuinely independent route (it goes through Render); OrphanCount,
// UncacheableCount, Finished() and Idle()/HasPendingAssistant() are four
// flag/positional views of the same Finished()/streamingAssistant bits. They
// are all kept because each fails with a different diagnostic, but the
// redundancy is NOT corroboration.
//
// wantLen is asserted separately by callers, since the thinking-tail shape
// loses the marker (QUM-975 drop) and the tail-only shape does not.
func assertSettled(t *testing.T, app AppModel, wantText string) {
	t.Helper()
	cl := rootChat(app)
	if got := cl.OrphanCount(); got != 0 {
		t.Errorf("OrphanCount() = %d, want 0 — assistant item stranded unfinished behind a later item", got)
	}
	if got := cl.UncacheableCount(); got != 0 {
		t.Errorf("UncacheableCount() = %d, want 0 — stranded item re-renders through glamour on every frame", got)
	}
	if got := cursorCount(cl, 100); got != 0 {
		t.Errorf("cursorCount() = %d, want 0 — stray ▍ streaming cursor visible in a transcript with no running session", got)
	}
	if cl.HasPendingAssistant() {
		t.Error("HasPendingAssistant() = true, want false after the turn-ending restart")
	}
	if !cl.Idle() {
		t.Error("Idle() = false, want true after the turn-ending restart")
	}
	// Content preservation: finalising must settle the item, not drop or mutate it.
	items := assistantItems(app)
	if len(items) != 1 {
		t.Fatalf("assistant items = %d, want 1", len(items))
	}
	if !items[0].Finished() {
		t.Error("assistant Finished() = false, want true")
	}
	if got := items[0].Text(); got != wantText {
		t.Errorf("finalising mutated content: Text() = %q, want %q", got, wantText)
	}
}

// TestSessionRestarting_FinalizesStreamingAssistant is the primary QUM-986
// defect test: the reducer that already asserts "the turn is over" via
// setTurnState(TurnIdle) must make the item state agree with that flag.
func TestSessionRestarting_FinalizesStreamingAssistant(t *testing.T) {
	app, _ := strandedApp(t)

	app = deliver(t, app, SessionRestartingMsg{Reason: "session ended"})

	assertSettled(t, app, "partial answer")
	if got := rootChat(app).Len(); got != 1 {
		t.Errorf("Len() = %d, want 1 (settled assistant only; the thinking marker is dropped by FinalizeAssistantMessage)", got)
	}
	// No-regression leg: TurnStreaming -> TurnIdle already worked pre-fix.
	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v, want TurnIdle", app.turnState)
	}
}

// TestSessionRestarting_FinalizesTailAssistant covers the commonest real shape:
// streamed text with no trailing thinking block, so the unfinished assistant is
// the TAIL. OrphanCount is structurally blind here (it only sweeps items[:n-1])
// and reads 0 both before and after — it is asserted only as a
// nothing-got-worse leg. cursorCount and Finished() are what carry this test.
func TestSessionRestarting_FinalizesTailAssistant(t *testing.T) {
	app, fake := idleTrackingApp(t)
	fake.SetContinuous(true)
	app = deliver(t, app, AssistantTextMsg{Text: "tail answer"})

	cl := rootChat(app)
	if got := cursorCount(cl, 100); got != 1 {
		t.Fatalf("precondition: cursorCount() = %d, want 1", got)
	}
	if !cl.HasPendingAssistant() {
		t.Fatal("precondition: HasPendingAssistant() = false, want true")
	}

	app = deliver(t, app, SessionRestartingMsg{Reason: "session ended"})

	assertSettled(t, app, "tail answer")
	if got := rootChat(app).Len(); got != 1 {
		t.Errorf("Len() = %d, want 1", got)
	}
}

// TestSessionErrorEOF_FinalizesStreamingAssistant drives the real io.EOF entry
// door end-to-end: the reducer's returned batch is executed and each resulting
// msg fed back through Update, so this fails if the fix is ever placed
// somewhere the EOF producer does not reach.
func TestSessionErrorEOF_FinalizesStreamingAssistant(t *testing.T) {
	app, _ := strandedApp(t)

	updated, cmd := app.Update(SessionErrorMsg{Err: io.EOF})
	app = updated.(AppModel)
	msgs := collectBatchMsgs(t, cmd)
	if !hasMsgOfType[SessionRestartingMsg](msgs) {
		t.Fatalf("EOF did not schedule SessionRestartingMsg; got %v", msgs)
	}
	app = deliverExceptRestart(t, app, msgs)

	assertSettled(t, app, "partial answer")
}

// TestHandoffRequested_FinalizesStreamingAssistant covers the /handoff door.
// It shares the reducer pair with EOF today, but /handoff is the
// higher-traffic entry and a future change could split them, so it is asserted
// rather than inferred.
func TestHandoffRequested_FinalizesStreamingAssistant(t *testing.T) {
	app, _ := strandedApp(t)

	updated, cmd := app.Update(HandoffRequestedMsg{})
	app = updated.(AppModel)
	msgs := collectBatchMsgs(t, cmd)
	if !hasMsgOfType[SessionRestartingMsg](msgs) {
		t.Fatalf("handoff did not schedule SessionRestartingMsg; got %v", msgs)
	}
	app = deliverExceptRestart(t, app, msgs)

	assertSettled(t, app, "partial answer")
}

// deliverExceptRestart feeds msgs back through Update, skipping
// RestartSessionMsg — that reducer closes the bridge and calls restartFunc,
// while these tests are about what SessionRestartingMsg leaves behind.
func deliverExceptRestart(t *testing.T, app AppModel, msgs []tea.Msg) AppModel {
	t.Helper()
	for _, m := range msgs {
		if _, ok := m.(RestartSessionMsg); ok {
			continue
		}
		app = deliver(t, app, m)
	}
	return app
}

// TestSessionRestarting_ThinkingOnlyDropsMarker pins a behaviour change the fix
// introduces on a path that previously deleted nothing: FinalizeAssistantMessage
// runs dropTrailingThinkingMarker() unconditionally, so a restart with a
// thinking marker and no assistant text removes the marker. That is intended
// ("the turn is over, the marker was transient") but it is a user-visible
// deletion and must be a watched decision, not a side effect nobody noticed.
func TestSessionRestarting_ThinkingOnlyDropsMarker(t *testing.T) {
	app, fake := idleTrackingApp(t)
	fake.SetContinuous(true)
	app = deliver(t, app, ThinkingMsg{})
	if got := rootChat(app).Len(); got != 1 {
		t.Fatalf("precondition: Len() = %d, want 1 (the thinking marker)", got)
	}

	app = deliver(t, app, SessionRestartingMsg{Reason: "session ended"})

	if got := rootChat(app).Len(); got != 0 {
		t.Errorf("Len() = %d, want 0 — the transient thinking marker must not outlive the session", got)
	}
	if got := cursorCount(rootChat(app), 100); got != 0 {
		t.Errorf("cursorCount() = %d, want 0", got)
	}
}

// TestSessionRestarting_LeavesChildBufferAlone is the scope negative control:
// the fix is rootBuf()-scoped on purpose. A child agent's stream is driven by
// its own runtime and outlives weave's bridge restart, so finalising it here
// would kill a live cursor on a block still being written. This is what would
// catch a "fix" written as a loop over every viewport.
func TestSessionRestarting_LeavesChildBufferAlone(t *testing.T) {
	app, fake := idleTrackingApp(t)
	fake.SetContinuous(true)
	child := app.agentBufferFor("kern")
	child.AppendAssistantChunk("child mid-stream")
	childCL := child.vp.ChatList()
	childCL.SetSize(100)
	if !childCL.HasPendingAssistant() {
		t.Fatal("precondition: child HasPendingAssistant() = false, want true")
	}

	app = deliver(t, app, SessionRestartingMsg{Reason: "session ended"})

	if !childCL.HasPendingAssistant() {
		t.Error("child HasPendingAssistant() = false, want true — weave's restart must not finalise a child agent's in-flight stream")
	}
	if got := cursorCount(childCL, 100); got != 1 {
		t.Errorf("child cursorCount() = %d, want 1 (its live cursor must survive)", got)
	}
}

// TestSessionRestarting_EmptyBufferIsNoOp pins the empty-buffer shape that the
// cmd/enter.go:935 resume-failure producer delivers at startup: finalising with
// nothing in the buffer must be a safe no-op rather than an assumption. It
// exercises the REDUCER, not that producer — the producer itself is covered by
// the chokepoint enumeration in the header comment, not by this test. It also
// passes pre-fix (0 -> 0), so it is a safety control, not defect evidence.
func TestSessionRestarting_EmptyBufferIsNoOp(t *testing.T) {
	app, _ := idleTrackingApp(t)
	if got := rootChat(app).Len(); got != 0 {
		t.Fatalf("precondition: Len() = %d, want 0", got)
	}

	app = deliver(t, app, SessionRestartingMsg{Reason: "resume failed — no conversation found"})

	if got := rootChat(app).Len(); got != 0 {
		t.Errorf("Len() = %d, want 0 — finalising an empty buffer must not invent an item", got)
	}
	if got := rootChat(app).OrphanCount(); got != 0 {
		t.Errorf("OrphanCount() = %d, want 0", got)
	}
}

// TestSessionRestarting_RepeatedRestartIsInert pins idempotency from a
// genuinely settled starting state (asserted as a precondition, so the first
// restart is not silently doing the work), which is the double-restart shape
// RestartSessionMsg's own coalescing makes reachable.
func TestSessionRestarting_RepeatedRestartIsInert(t *testing.T) {
	app, fake := idleTrackingApp(t)
	fake.SetContinuous(true)
	app = deliver(t, app, AssistantTextMsg{Text: "done"})
	// SessionResultMsg routes through finalizeTurn, giving a settled buffer
	// without depending on the fix under test.
	app = deliver(t, app, SessionResultMsg{})

	items := assistantItems(app)
	if len(items) != 1 || !items[0].Finished() {
		t.Fatalf("precondition: want exactly one settled assistant item, got %d items", len(items))
	}
	if rootChat(app).HasPendingAssistant() {
		t.Fatal("precondition: HasPendingAssistant() = true, want false")
	}
	beforeLen := rootChat(app).Len()

	app = deliver(t, app, SessionRestartingMsg{Reason: "session ended"})
	app = deliver(t, app, SessionRestartingMsg{Reason: "session ended"})

	if got := rootChat(app).Len(); got != beforeLen {
		t.Errorf("Len() = %d, want %d — a redundant finalise must not duplicate or drop items", got, beforeLen)
	}
	if got := cursorCount(rootChat(app), 100); got != 0 {
		t.Errorf("cursorCount() = %d, want 0", got)
	}
	if got := rootChat(app).OrphanCount(); got != 0 {
		t.Errorf("OrphanCount() = %d, want 0", got)
	}
	items = assistantItems(app)
	if len(items) != 1 {
		t.Fatalf("assistant items = %d, want 1", len(items))
	}
	if got := items[0].Text(); got != "done" {
		t.Errorf("Text() = %q, want %q", got, "done")
	}
}

// TestSessionRestarting_DoesNotRearmThePump is a guardrail on the fix SHAPE,
// not red-first evidence — it passes before the fix too (the reducer returns
// nil today). It exists because finalizeTurn() appends bridge.WaitForEvent(),
// and this path is deliberately tearing the bridge down for RestartSessionMsg
// to rebuild, so re-arming here would arm a pump on a doomed bridge (QUM-826
// class freeze). A finalizeTurn()-shaped "simplification" flips it red.
//
// Scope: fakeSessionBackend.waitCalls increments when WaitForEvent is CALLED,
// which for finalizeTurn happens synchronously inside Update. Feeding the
// returned msgs back also catches an indirect re-arm via a follow-up reducer.
func TestSessionRestarting_DoesNotRearmThePump(t *testing.T) {
	app, fake := strandedApp(t)
	before := fake.waitCalls

	updated, cmd := app.Update(SessionRestartingMsg{Reason: "session ended"})
	app = updated.(AppModel)
	deliverExceptRestart(t, app, collectBatchMsgs(t, cmd))

	if fake.waitCalls != before {
		t.Errorf("waitCalls = %d, want %d — SessionRestartingMsg must not re-arm the event pump on a bridge being torn down; use rootBuf().FinalizeAssistantMessage(), not finalizeTurn()", fake.waitCalls, before)
	}
}

// TestSessionRestarting_PumpProbeIsLive is the anti-vacuity control for the
// assertion above: it proves fake.waitCalls actually moves on a path that DOES
// re-arm, so waitCalls-is-unchanged is a real constraint and not an instrument
// wired to a dead terminal.
func TestSessionRestarting_PumpProbeIsLive(t *testing.T) {
	app, fake := strandedApp(t)
	before := fake.waitCalls

	// SessionResultMsg routes through finalizeTurn, which re-arms.
	_, cmd := app.Update(SessionResultMsg{})
	collectBatchMsgs(t, cmd)

	if fake.waitCalls <= before {
		t.Fatalf("waitCalls = %d, want > %d — the pump probe never moves, so TestSessionRestarting_DoesNotRearmThePump constrains nothing", fake.waitCalls, before)
	}
}
