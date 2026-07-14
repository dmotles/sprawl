package tui

// QUM-675 S5 — TUI structural rewrite: route ChatList "contract violators"
// (status/banner/error/system surfaces) out of the viewport stream and into
// the dedicated status-bar transient label / γ overlay / tree badge.
//
// These tests are RED until the S5 implementation reroutes the call sites
// listed in tower's display-policy comment on QUM-675. Each test asserts on
// TWO things:
//
//  1. The statusbar's transient label (rendered into m.statusBar.View())
//     OR the γ overlay (m.showError == true) carries the user-visible text.
//  2. The viewport (m.rootVP().ChatList().Items()) does NOT carry the same
//     text as a contract-violator surface (status/banner/error/system). Post
//     QUM-693 those entries can no longer enter the ChatList at all, so the
//     negative half of (2) is structurally vacuous — most call sites were
//     deleted; a few are retained as documentation via the helper stubs.
//
// Reference: docs/designs/tui-structural-rewrite-plan.md §3 S5 + the
// display-policy comment on QUM-675.

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// rootBannerOrStatusContains is retained as a structurally-vacuous helper
// after QUM-693. MessageStatus / MessageBanner / MessageError entries can
// never enter the ChatList (the only viewport-rendered surface), so the
// search is guaranteed to be false. The function survives so existing call
// sites continue to document the absence guarantee.
func rootBannerOrStatusContains(_ AppModel, _ string) bool {
	return false
}

// statusBarContains reports whether the rendered status bar (ANSI-stripped)
// contains substr.
func statusBarContains(app AppModel, substr string) bool {
	view := stripAnsi(app.statusBar.View())
	return strings.Contains(view, substr)
}

// --- 1. SessionRestartingMsg ------------------------------------------------

func TestSessionRestartingMsg_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(SessionRestartingMsg{Reason: "handoff"})
	app = updated.(AppModel)

	const want = "Session restarting (handoff)"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after SessionRestartingMsg; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry a status/banner entry containing %q (S5 reroute)", want)
	}
}

// 2. The "queued message dropped" sibling status emitted from inside the
// SessionRestartingMsg reducer must also land in the transient label, not the
// viewport.
func TestSessionRestartingMsg_DroppedQueuedMessage_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)
	// QUM-833: a pending user prompt now lives in the root ChatList's zone.
	app.rootBuf().ZoneAddUser("u1", "abc")

	updated, _ := app.Update(SessionRestartingMsg{Reason: "x"})
	app = updated.(AppModel)

	const want = "queued message"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after dropping queued submit on restart; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

// --- 3 & 4. InterruptResultMsg (the Esc-acknowledgement path) --------------

func TestInterruptResultMsg_Success_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(InterruptResultMsg{})
	app = updated.(AppModel)

	const want = "Interrupt sent"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after successful interrupt-ack; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

func TestInterruptResultMsg_Failure_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(InterruptResultMsg{Err: errors.New("nope")})
	app = updated.(AppModel)

	const want = "Interrupt failed"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after failed interrupt-ack; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

// --- 5. Esc-during-streaming "Interrupting..." feedback -------------------

func TestInterruptingFeedback_OnEscKey_RoutesToTransientLabel(t *testing.T) {
	mock := newFakeSessionBackend()
	app := readyAppWithBridge(t, mock)
	app.turnState = TurnStreaming
	app.statusBar.SetTurnState(TurnStreaming)

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	const want = "Interrupting..."
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after Esc-during-streaming; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

// --- 6 & 7. SessionResultMsg success / interrupted timing banners ----------

func TestSessionResultMsg_Completed_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(SessionResultMsg{DurationMs: 1500, TotalCostUsd: 0.001})
	app = updated.(AppModel)

	const want = "Completed in 1500ms"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after SessionResultMsg success; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

func TestInterruptCompletedMsg_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(InterruptCompletedMsg{DurationMs: 800})
	app = updated.(AppModel)

	const want = "Interrupted (800ms)"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after InterruptCompletedMsg; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

// --- 8. SessionResultMsg error path → γ overlay ----------------------------

// Per tower's display-policy spec: "Transport / session faults: route to the
// existing error-dialog overlay (γ overlay). No transient text. Removing the
// viewport copy is the win."
//
// SessionResultMsg.IsError is the "session-level error" path that
// EventTurnFailed translates to (see event_translate.go); S5 routes the user-
// visible surface from a vp.AppendError to the existing error dialog so the
// operator sees an unmistakable modal rather than a banner buried in scroll.
//
// NOTE: this test sits alongside TestAppModel_SessionResultMsg_FaultPath_*
// which asserts on TurnIdle ungating — that behavior is unchanged. We only
// add the surfacing-channel assertion here.
func TestSessionResultMsg_Error_EscalatesToErrorDialog(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(SessionResultMsg{IsError: true, Result: "boom"})
	app = updated.(AppModel)

	if !app.showError {
		t.Errorf("SessionResultMsg{IsError:true} should escalate to the γ overlay (showError=true); got showError=%v", app.showError)
	}
	// QUM-693: MessageError never enters ChatList — vacuous assertion deleted.
}

// --- 9. ConsolidationPhaseMsg ---------------------------------------------
//
// Decision per the oracle's spec: restartLabel is the existing dedicated
// surface for consolidation-phase text (QUM-391). S5 drops the duplicate
// vp.AppendStatus(msg.Phase) call — restartLabel keeps its dedicated channel,
// and the viewport no longer carries a parallel banner.

func TestConsolidationPhaseMsg_NoViewportBanner_RestartLabelOnly(t *testing.T) {
	app := readyApp(t)

	const phase = "Consolidating timeline..."
	updated, _ := app.Update(ConsolidationPhaseMsg{Phase: phase})
	app = updated.(AppModel)

	if !statusBarContains(app, phase) {
		t.Errorf("status bar (restartLabel) should contain phase %q after ConsolidationPhaseMsg; got:\n%s", phase, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, phase) {
		t.Errorf("root viewport must NOT carry the consolidation phase as a status/banner entry (S5 reroute — restartLabel is the dedicated surface)")
	}
}

// --- 10 & 11. ConsolidationCompleteMsg ------------------------------------

func TestConsolidationCompleteMsg_Success_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(ConsolidationCompleteMsg{Duration: 15 * time.Second})
	app = updated.(AppModel)

	const want = "Consolidation complete"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after ConsolidationCompleteMsg{Err:nil}; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

func TestConsolidationCompleteMsg_Failure_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(ConsolidationCompleteMsg{Err: errors.New("timeout"), Duration: time.Second})
	app = updated.(AppModel)

	const want = "Consolidation failed"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after ConsolidationCompleteMsg{Err:!=nil}; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

// --- 12. RestartCompleteMsg session banner --------------------------------
//
// Decision per the oracle's spec: the SessionBanner ASCII-art block is
// redundant with the existing SetSessionID() statusbar segment ("sess:..."),
// so S5 PURE-DELETES the AppendBanner(SessionBanner(...)) call. The viewport
// must not carry a MessageBanner with the banner text after a successful
// RestartCompleteMsg.
//
// We use a fake bridge so RestartCompleteMsg can install it as the new bridge.
func TestRestartCompleteMsg_NoSessionBannerInViewport(t *testing.T) {
	mock := newFakeSessionBackend()
	app := readyAppWithBridge(t, mock)
	// Force restarting flag so the reducer's quit-guard branches as expected.
	app.restarting = true

	preBanners := countBanners(app)

	updated, _ := app.Update(RestartCompleteMsg{Bridge: mock})
	app = updated.(AppModel)

	postBanners := countBanners(app)
	if postBanners > preBanners {
		t.Errorf("RestartCompleteMsg must NOT append a new MessageBanner to the viewport (S5 pure-deletion of SessionBanner); pre=%d post=%d", preBanners, postBanners)
	}
}

// 13. The transient label must NOT survive a successful restart — the user
// has just been told (via the dedicated session-id segment) that a new session
// is up, and any stale "Session restarting..." text from before should be
// cleared.
func TestRestartCompleteMsg_ClearsTransientLabel(t *testing.T) {
	mock := newFakeSessionBackend()
	app := readyAppWithBridge(t, mock)
	app.restarting = true
	// Pre-set a transient label via the session-restarting reducer so the
	// clear logic actually has something to clear.
	updated, _ := app.Update(SessionRestartingMsg{Reason: "handoff"})
	app = updated.(AppModel)
	if !statusBarContains(app, "Session restarting") {
		t.Fatalf("precondition: status bar should carry restart label before completion; got:\n%s", stripAnsi(app.statusBar.View()))
	}

	app.restarting = true
	updated, _ = app.Update(RestartCompleteMsg{Bridge: mock})
	app = updated.(AppModel)

	if statusBarContains(app, "Session restarting") {
		t.Errorf("RestartCompleteMsg should clear the transient 'Session restarting' label; got:\n%s", stripAnsi(app.statusBar.View()))
	}
}

// --- 14. BackendFaultMsg — pure deletion (tree badge already owns it) -----

// Per tower's display-policy spec row 6: "backend fault on …" → DO NOT set
// transient — already on tree badge. No viewport copy, no transient copy.
// Pure deletion.
func TestBackendFaultMsg_NoViewportCopy(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(BackendFaultMsg{
		Agent:      "alice",
		Class:      "HangTimeout",
		Reason:     "stalled",
		NextAction: "retire+respawn",
	})
	app = updated.(AppModel)

	// QUM-693: status/banner/error never enter ChatList — vacuous assertion deleted.
	if statusBarContains(app, "backend fault on alice") {
		t.Errorf("status bar transient label must NOT carry the backend-fault text — tree badge owns it (S5 spec row 6: pure deletion)")
	}
	// The faults map is the load-bearing surface that drives the tree badge;
	// it must still be populated on the fault edge.
	if _, ok := app.faults["alice"]; !ok {
		t.Errorf("faults[\"alice\"] should be populated after BackendFaultMsg; got faults=%v", app.faults)
	}
}

// --- 15. BackendFaultClearedMsg → transient label -------------------------

func TestBackendFaultClearedMsg_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)

	// QUM-776: the transient label is now gated on a prior tracked fault —
	// in-place recovery (the original caller of BackendFaultClearedMsg)
	// always observes one. Stage the fault before clearing to match that
	// real-world ordering.
	staged, _ := app.Update(BackendFaultMsg{Agent: "alice", Class: "HangTimeout", Reason: "stalled"})
	app = staged.(AppModel)

	updated, _ := app.Update(BackendFaultClearedMsg{Agent: "alice"})
	app = updated.(AppModel)

	const want = "backend recovered on alice"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after BackendFaultClearedMsg; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

// --- 16 & 17. AgentsResumedMsg → transient label --------------------------

func TestAgentsResumedMsg_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(AgentsResumedMsg{Resumed: 3, Failed: 0})
	app = updated.(AppModel)

	const want = "[startup] resumed 3 agents"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after AgentsResumedMsg; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

func TestAgentsResumedMsg_WithFailures_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(AgentsResumedMsg{Resumed: 2, Failed: 1})
	app = updated.(AppModel)

	const want = "(1 failed)"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after AgentsResumedMsg with failures; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, "[startup] resumed 2 agents") {
		t.Errorf("root viewport must NOT carry the resumed-agents banner (S5 reroute)")
	}
}

// --- 18 & 19. SessionErrorMsg (non-EOF) → γ overlay -----------------------

// Per spec: transport/session faults route to the existing error-dialog
// overlay, not the viewport. Today the Idle branch calls vp.AppendError;
// post-S5 it must escalate to the γ overlay like the Streaming/Thinking
// branch already does.
func TestSessionErrorMsg_NonEOF_Idle_EscalatesToErrorDialog(t *testing.T) {
	app := readyApp(t)
	app.turnState = TurnIdle

	updated, _ := app.Update(SessionErrorMsg{Err: errors.New("xport down")})
	app = updated.(AppModel)

	if !app.showError {
		t.Errorf("SessionErrorMsg (non-EOF, Idle) should escalate to the γ overlay (showError=true); got showError=%v", app.showError)
	}
	// QUM-693: MessageError never enters ChatList — vacuous assertion deleted.
}

// Streaming/Thinking branch already escalates pre-S5 — this guards it stays.
func TestSessionErrorMsg_NonEOF_Streaming_EscalatesToErrorDialog(t *testing.T) {
	app := readyApp(t)
	app.turnState = TurnStreaming

	updated, _ := app.Update(SessionErrorMsg{Err: errors.New("xport down")})
	app = updated.(AppModel)

	if !app.showError {
		t.Errorf("SessionErrorMsg (non-EOF, Streaming) should escalate to the γ overlay (showError=true)")
	}
	// QUM-693: MessageError never enters ChatList — vacuous assertion deleted.
}

// EOF SessionErrorMsg has a separate auto-restart path — guard that it does
// NOT escalate the γ overlay so we don't accidentally widen the error path.
func TestSessionErrorMsg_EOF_DoesNotEscalateOverlay(t *testing.T) {
	app := readyApp(t)
	app.turnState = TurnIdle

	updated, _ := app.Update(SessionErrorMsg{Err: io.EOF})
	app = updated.(AppModel)

	if app.showError {
		t.Errorf("EOF SessionErrorMsg should auto-restart (no γ overlay); got showError=%v", app.showError)
	}
}

// --- 20. InjectPromptMsg ("/handoff dispatched") → transient label --------

func TestInjectPromptMsg_DispatchStatus_RoutesToTransientLabel(t *testing.T) {
	mock := newFakeSessionBackend()
	app := readyAppWithBridge(t, mock)
	app.turnState = TurnIdle

	updated, _ := app.Update(InjectPromptMsg{Template: "/handoff"})
	app = updated.(AppModel)

	const want = "/handoff dispatched"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after InjectPromptMsg; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

// --- 21. Inbox banner status → transient label -----------------------------
//
// Two call sites today (app.go:1145, :1201) emit formatInboxBanner(...) into
// vp.AppendStatus. Both must reroute to the transient label. We exercise the
// InboxArrivalMsg path because it's the more directly testable of the two.

func TestInboxArrivalMsg_BannerRoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(InboxArrivalMsg{From: "alice", Subject: "ping"})
	app = updated.(AppModel)

	// formatInboxBanner emits text starting with "inbox:" — substring match
	// on that prefix tolerates the count/sender formatting churn.
	const wantPrefix = "inbox:"
	if !statusBarContains(app, wantPrefix) {
		t.Errorf("status bar should contain %q after InboxArrivalMsg; got:\n%s", wantPrefix, stripAnsi(app.statusBar.View()))
	}
	// QUM-693: MessageStatus never enters ChatList — vacuous assertion deleted.
}

// --- 22. mcpOpThresholdMsg → transient label ------------------------------

func TestMCPOpThresholdMsg_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)
	// Seed an in-flight op so the threshold reducer doesn't no-op on the
	// "no such op" branch.
	updated, _ := app.Update(MCPCallStartedMsg{
		CallID: "c1", Tool: "Bash", Caller: "weave", Started: time.Now().Add(-90 * time.Second),
	})
	app = updated.(AppModel)

	updated, _ = app.Update(mcpOpThresholdMsg{CallID: "c1"})
	app = updated.(AppModel)

	const want = "taking longer than usual"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after mcpOpThresholdMsg; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

// --- 23 & 24. ViewportResyncMsg paths --------------------------------------

// Failure path: per spec, errors go to the γ overlay, not a viewport
// MessageError entry. (resyncPill still covers the in-flight indicator.)
func TestResyncResultMsg_Failure_EscalatesToErrorDialog(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(ViewportResyncMsg{Err: errors.New("read failed")})
	app = updated.(AppModel)

	if !app.showError {
		t.Errorf("ViewportResyncMsg{Err:!=nil} should escalate to the γ overlay (showError=true); got showError=%v", app.showError)
	}
	// QUM-693: MessageError never enters ChatList — vacuous assertion deleted.
}

// Success path: the "✓ resynced — recovered N events" message is a different
// kind of confirmation from the resyncPill (which is the in-flight
// indicator, cleared in the same reducer). Route the success banner to the
// transient label so the user gets a one-shot confirmation that doesn't
// pollute the chat stream.
func TestResyncResultMsg_Success_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(ViewportResyncMsg{MissingCount: 7})
	app = updated.(AppModel)

	const want = "recovered 7 events"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after successful resync; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

// --- 25. ChildTranscriptMsg error → γ overlay ----------------------------

func TestChildTranscriptMsg_LoadError_EscalatesToErrorDialog(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(ChildTranscriptMsg{Agent: "alice", Err: errors.New("disk read failed")})
	app = updated.(AppModel)

	if !app.showError {
		t.Errorf("ChildTranscriptMsg{Err:!=nil} should escalate to the γ overlay (showError=true); got showError=%v", app.showError)
	}
	// QUM-693: MessageError never enters ChatList — vacuous assertion deleted.
}

// --- 26. (Removed) Clipboard-copy status from viewport yank-mode --------
//
// QUM-695: viewport yank-mode (`v` enter / `y` yank) was removed wholesale.
// The associated "Copied selection to clipboard" transient-label assertion
// is gone with it.

// --- 27. EventDropDetectedMsg "events lost" banner -----------------------
//
// app.go:2867 emits "⚠ N events lost — resync in flight" via AppendStatus
// when kickResyncFromGap fires. Per the display-policy table's last-write-
// wins rule, route to the transient label. resyncPill covers the in-flight
// indicator separately.
func TestEventDropDetectedMsg_BurstResyncBanner_RoutesToTransientLabel(t *testing.T) {
	app := readyApp(t)

	// Cross the burst threshold so kickResyncFromGap fires immediately.
	updated, _ := app.Update(EventDropDetectedMsg{From: 1, To: 1000, Missing: 1000})
	app = updated.(AppModel)

	const want = "events lost"
	if !statusBarContains(app, want) {
		t.Errorf("status bar should contain %q after burst gap detected; got:\n%s", want, stripAnsi(app.statusBar.View()))
	}
	if rootBannerOrStatusContains(app, want) {
		t.Errorf("root viewport must NOT carry %q (S5 reroute)", want)
	}
}

// --- 28. Clear-trigger: TurnIdle → TurnThinking ---------------------------

// Per the display-policy clear-rule table, "Interrupt sent" / "Completed in
// Xms" / startup banners clear on "next turn-start". TurnIdle → TurnThinking
// is the canonical turn-start transition.
func TestSetTurnState_OnIdleToThinking_ClearsTransientLabel(t *testing.T) {
	app := readyApp(t)

	updated, _ := app.Update(InterruptResultMsg{})
	app = updated.(AppModel)
	if !statusBarContains(app, "Interrupt sent") {
		t.Fatalf("precondition: status bar should carry 'Interrupt sent' before clear; got:\n%s", stripAnsi(app.statusBar.View()))
	}

	updated, _ = app.Update(TurnStateMsg{State: TurnThinking})
	app = updated.(AppModel)

	if statusBarContains(app, "Interrupt sent") {
		t.Errorf("TurnIdle → TurnThinking must clear the transient label; got:\n%s", stripAnsi(app.statusBar.View()))
	}
}

// --- 29. Clear-trigger: next user prompt sent ------------------------------
//
// "Completed in Xms" / "Interrupted (Xms)" / "[startup] resumed N" / "backend
// recovered on …" all clear on "next user prompt sent" per the spec. Drive
// that via the SubmitMsg path which is the in-process "user prompt sent"
// signal.
func TestUserPromptSent_ClearsTransientLabel(t *testing.T) {
	mock := newFakeSessionBackend()
	app := readyAppWithBridge(t, mock)
	app.turnState = TurnIdle

	updated, _ := app.Update(AgentsResumedMsg{Resumed: 2, Failed: 0})
	app = updated.(AppModel)
	if !statusBarContains(app, "[startup] resumed 2 agents") {
		t.Fatalf("precondition: status bar should carry startup banner; got:\n%s", stripAnsi(app.statusBar.View()))
	}

	updated, _ = app.Update(SubmitMsg{Text: "next prompt"})
	app = updated.(AppModel)

	if statusBarContains(app, "[startup] resumed 2 agents") {
		t.Errorf("SubmitMsg (next user prompt) should clear the transient label; got:\n%s", stripAnsi(app.statusBar.View()))
	}
}

// countBanners is structurally vacuous after QUM-693: MessageBanner can
// never enter the ChatList. Always returns 0. Retained so the RestartComplete
// pre/post call sites still document the absence guarantee.
func countBanners(_ AppModel) int { return 0 }
