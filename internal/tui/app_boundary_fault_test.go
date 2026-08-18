// QUM-927 rework: REDUCER-LAYER coverage for a genuine backend fault that
// lands at a turn boundary while an interrupt arm is set.
//
// Why this file exists at all. The original QUM-927 tests
// (internal/runtime/interrupt_classify_test.go) assert at the EVENT-BUS layer,
// which structurally cannot see an event the TUI never consumes — so a
// bus-level "EventBackendFaulted was published" assertion passes while the user
// sees nothing. That codified the gap instead of catching it. These tests
// assert what the TUI actually SURFACES, by pushing real bus events through the
// real shared translator (TranslateRuntimeEvent — the exact call the root
// bridge makes at tuiruntime/tuiadapter.go and the per-child ChildStreamAdapter
// makes at child_stream.go) into the real AppModel reducer.
//
// Both panes are covered on purpose. A root-only test cannot catch
// double-surfacing on children, and a child-only test cannot catch root
// silence. Those are the two failure modes in tension in this fix, so an
// assertion that can only see one side of the tradeoff would ratify whichever
// choice the implementer made rather than decide the question.
//
// Red-first is necessary but NOT sufficient, and this file is shaped by that
// lesson: an earlier draft of these tests was red-first AND still passed under a
// wrong fix that left internal/runtime/unified.go's gate untouched and instead
// added an EventBackendFaulted case to TranslateRuntimeEvent. Red-first proves a
// test is not vacuous; it does not prove the test constrains the fix to the
// right place. TestTranslateRuntimeEvent_BackendFaultedHasNoCase (in
// event_translate_test.go) and the mid-turn single-surface test below are what
// close that hole.
package tui

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// -- test doubles ------------------------------------------------------------

// faultableFrameSession is a SessionHandle double that captures BOTH optional
// hooks runtime.New probes for by type assertion: the terminal-error handler
// (so a test can fire a genuine transport fault on demand) and the frame router
// (which IS the runtime's unexported routeFrame, giving an out-of-package test
// the only legitimate way to drive wire frames). Mirrors
// internal/runtime/backend_fault_test.go's mockFaultableSession.
type faultableFrameSession struct {
	noopSession

	mu      sync.Mutex
	onFault func(error)
	router  func(*protocol.Message, backend.TurnInfo)
}

func (s *faultableFrameSession) SetTerminalErrorHandler(h func(error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onFault = h
}

func (s *faultableFrameSession) SetFrameRouter(r func(*protocol.Message, backend.TurnInfo)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.router = r
}

// fireTerminalErr simulates the backend reader promoting a sticky terminal
// error (transport EOF / subprocess death): internal/backend/session.go:686's
// setTerminalErr, which invokes the handler synchronously and OUTSIDE the
// session mutex (session.go:1417-1421).
//
// Note the handler also cancels the runtime's runCtx, so EventStopped and
// close(rt.done) race any subsequent routeFrame call. Benign here — routeFrame
// consults neither — but it is why the drain below must tolerate an extra
// lifecycle event (it translates to nil).
func (s *faultableFrameSession) fireTerminalErr(t *testing.T, err error) {
	t.Helper()
	s.mu.Lock()
	h := s.onFault
	s.mu.Unlock()
	if h == nil {
		t.Fatal("terminal-error handler was never installed; the runtime's type assertion did not match this double")
	}
	h(err)
}

func (s *faultableFrameSession) routeFrame(t *testing.T, msg *protocol.Message, info backend.TurnInfo) {
	t.Helper()
	s.mu.Lock()
	r := s.router
	s.mu.Unlock()
	if r == nil {
		t.Fatal("frame router was never installed; the runtime's type assertion did not match this double")
	}
	r(msg, info)
}

// -- helpers -----------------------------------------------------------------

func newBoundaryRuntime(t *testing.T, name string) (*sprawlrt.UnifiedRuntime, *faultableFrameSession) {
	t.Helper()
	sess := &faultableFrameSession{}
	rt := sprawlrt.New(sprawlrt.RuntimeConfig{Name: name, Session: sess})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	})
	return rt, sess
}

// routeStateChange drives an authoritative session_state_changed wire frame.
func routeStateChange(t *testing.T, sess *faultableFrameSession, state string) {
	t.Helper()
	sess.routeFrame(t, &protocol.Message{Type: "system", Subtype: "session_state_changed"},
		backend.TurnInfo{Autonomous: true, StateChange: state})
}

// openMidTurnVia drives a turn that is genuinely in flight: init + wire
// `running`, so phase == phaseRunning and State().InTurn is true.
func openMidTurnVia(t *testing.T, rt *sprawlrt.UnifiedRuntime, sess *faultableFrameSession) {
	t.Helper()
	sess.routeFrame(t, &protocol.Message{Type: "system", Subtype: "init"}, backend.TurnInfo{Autonomous: true})
	routeStateChange(t, sess, protocol.SessionStateRunning)
	// routeFrame → setPhase is synchronous, so this is already true; asserted
	// rather than polled so a regression here fails loudly instead of hanging.
	if !rt.State().InTurn {
		t.Fatal("mid-turn setup: State().InTurn is false after wire `running`")
	}
}

// openTurnBoundaryVia drives the QUM-927 wire shape from outside the runtime
// package: a frame-level turn is open (init routed) but the CLI has already
// reported session_state_changed:idle after the model's end_turn while async
// sidechains resolve. So State().InTurn is FALSE even though a terminal
// `result` for that turn is still inbound. Mirrors openTurnBoundary in
// internal/runtime/interrupt_classify_test.go.
func openTurnBoundaryVia(t *testing.T, rt *sprawlrt.UnifiedRuntime, sess *faultableFrameSession) {
	t.Helper()
	openMidTurnVia(t, rt, sess)
	routeStateChange(t, sess, protocol.SessionStateIdle)
	if rt.State().InTurn {
		t.Fatal("turn-boundary setup: State().InTurn is still true after wire idle; the test would exercise the mid-turn path instead")
	}
	// NOTE: the open frame turn's id is unobservable from outside internal/runtime (QUM-931), so this
	// helper can only prove phase==idle. The tests below therefore assert on the
	// INTERRUPT path having run (transient label) in addition to the fault
	// surface — without that, a regression of the QUM-927 arm would make the
	// orphan branch publish EventTurnFailed{errStreamClosedNoResult} and the
	// fault assertion would pass vacuously, for the wrong reason.
}

// resultFrameForTest builds a terminal `result` frame. Mirrors resultFrame in
// internal/runtime/interrupt_classify_test.go.
func resultFrameForTest(t *testing.T, isError bool) *protocol.Message {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type":           "result",
		"subtype":        "success",
		"is_error":       isError,
		"duration_ms":    42,
		"num_turns":      1,
		"total_cost_usd": 0.0,
		"result":         "",
	})
	if err != nil {
		t.Fatalf("marshal result frame: %v", err)
	}
	return &protocol.Message{Type: "result", Subtype: "success", Raw: raw}
}

// drainTranslated drains the bus and returns every non-nil tea.Msg the REAL
// shared translator produces. An event the translator maps to nil is an event
// the user never sees, and this is what makes that invisibility observable.
//
// Fully deterministic: every event these tests assert on is published
// synchronously on the test goroutine before this is called, so a non-blocking
// default is correct — no wall-clock waiting.
func drainTranslated(
	t *testing.T,
	ch <-chan sprawlrt.RuntimeEvent,
	interruptedFn func(sprawlrt.RuntimeEvent) tea.Msg,
) []tea.Msg {
	t.Helper()
	var out []tea.Msg
	for {
		select {
		case ev := <-ch:
			if msg := TranslateRuntimeEvent(ev, interruptedFn); msg != nil {
				out = append(out, msg)
			}
		default:
			return out
		}
	}
}

// countErrorSurfaces counts messages that raise a user-visible session error.
func countErrorSurfaces(msgs []tea.Msg) int {
	n := 0
	for _, msg := range msgs {
		if r, ok := msg.(SessionResultMsg); ok && r.IsError {
			n++
		}
	}
	return n
}

func applyAll(t *testing.T, app AppModel, msgs []tea.Msg) AppModel {
	t.Helper()
	for _, msg := range msgs {
		app = sendMsg(t, app, msg)
	}
	return app
}

// -- root pane ---------------------------------------------------------------

// A genuine backend fault (transport EOF / subprocess death) that lands at a
// turn boundary with an interrupt arm set MUST surface the "Session Error"
// dialog in the root pane, exactly as it did pre-QUM-927.
//
// RED before the fix: at the boundary phase reads idle, so the
// SetTerminalErrorHandler closure's narrow `turnRunning := rt.inTurnLocked()`
// gate suppresses EventTurnFailed; the only event published is
// EventBackendFaulted, which TranslateRuntimeEvent drops via its default arm
// (the root session has no other consumer — runFaultSubscriber is
// children-only). The orphan-teardown branch then consumes the arm and
// publishes EventInterrupted, so the user sees "Interrupted" for a DEAD
// SUBPROCESS: no Session Error, no restart modal, no fault surface at all.
func TestAppModel_GenuineFaultAtTurnBoundaryWithArm_SurfacesSessionErrorInRootPane(t *testing.T) {
	rt, sess := newBoundaryRuntime(t, "weave")
	ch, unsub := rt.EventBus().SubscribeNamed("root-boundary-fault", 64)
	defer unsub()

	openTurnBoundaryVia(t, rt, sess)
	// Esc at the boundary arms the still-open frame turn (QUM-927/QUM-931).
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	// THEN the subprocess genuinely dies. Production order: session.go fires the
	// terminal-error handler from the reader loop (session.go:686) and only then
	// runs its deferred orphan-teardown router notify (session.go:643-647).
	sess.fireTerminalErr(t, backend.ErrSubprocessExited)
	sess.routeFrame(t, nil, backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	// InterruptedAsCompleted is the root bridge's handler (tuiadapter.go).
	msgs := drainTranslated(t, ch, InterruptedAsCompleted)
	app := applyAll(t, applyResize(t, newTestAppModel(t)), msgs)

	if !app.showError {
		t.Fatalf("a genuine backend fault at a turn boundary with an interrupt arm set surfaced NO Session Error dialog "+
			"(showError=false); the user is left with a dead subprocess and only the transient label %q",
			app.statusBar.TransientLabel())
	}
	// Fatal, not Error: if the dialog is raised but does not name the fault, the
	// surface came from the orphan branch's generic errStreamClosedNoResult
	// (i.e. the QUM-927 arm regressed) rather than from the real fault, and the
	// assertion above would have passed for the wrong reason.
	content := ansi.Strip(app.View().Content)
	if !strings.Contains(content, "subprocess exited") {
		t.Fatalf("Session Error dialog does not name the real fault; want text containing %q, got:\n%s",
			"subprocess exited", content)
	}
	// Proves the boundary+arm shape was genuinely exercised: the interrupt path
	// must ALSO have run. Documents the accepted mixed surface — the modal
	// "Session Error" (with [r] restart) is the dominant, correct surface; the
	// transient label beneath it still reads "Interrupted" because the
	// re-classified teardown event is what closes the turn.
	if label := app.statusBar.TransientLabel(); !strings.Contains(label, "Interrupted") {
		t.Errorf("transient label = %q, want it to contain %q — without the interrupt leg this test is not exercising the boundary+arm shape",
			label, "Interrupted")
	}
	// A fault must never be recorded under a blank agent key. RuntimeEvent
	// carries FaultClass/FaultNextAction but NO identity, so any attempt to
	// surface EventBackendFaulted through the name-blind translator lands in
	// faults[""] and renders a " faulted: ..." toast with no agent.
	if _, blank := app.faults[""]; blank {
		t.Errorf("a fault was recorded under the empty agent key; the name-blind translator path surfaced it: faults=%v", app.faults)
	}
}

// Negative control: the SAME boundary shape with an Esc but NO fault must stay
// a clean interrupt — no Session Error. This is the QUM-927 behavior the rework
// must not regress, and it is what keeps the test above honest (it proves the
// assertion is fault-sensitive rather than merely arm-sensitive). It fails if
// the QUM-927 widening is reverted.
func TestAppModel_BoundaryInterruptWithoutFault_StaysCleanInRootPane(t *testing.T) {
	rt, sess := newBoundaryRuntime(t, "weave")
	ch, unsub := rt.EventBus().SubscribeNamed("root-boundary-clean", 64)
	defer unsub()

	openTurnBoundaryVia(t, rt, sess)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	// The interrupted turn's terminal arrives normally — no fault fired.
	sess.routeFrame(t, resultFrameForTest(t, true), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	msgs := drainTranslated(t, ch, InterruptedAsCompleted)
	app := applyAll(t, applyResize(t, newTestAppModel(t)), msgs)

	if app.showError {
		t.Errorf("a boundary Esc with NO backend fault raised the Session Error dialog; QUM-927's whole point is that it must not")
	}
	if label := app.statusBar.TransientLabel(); !strings.Contains(label, "Interrupted") {
		t.Errorf("transient label = %q, want it to contain %q", label, "Interrupted")
	}
}

// A genuine fault MID-TURN must surface EXACTLY ONE error. This is the test
// that forces the fix to live at the runtime gate: the wrong fix (leave the gate
// alone, add an EventBackendFaulted case to TranslateRuntimeEvent) yields TWO
// error surfaces here, because a mid-turn fault publishes BOTH
// EventBackendFaulted and EventTurnFailed (unified.go:213 and :237) — the user
// gets the error dialog raised twice and finalizeTurn() runs twice.
//
// Deliberately routes NO orphan teardown, so what this test constrains is the
// TRANSLATOR, not the publish count. "Exactly one error surface mid-turn" is NOT
// a production invariant: the deferred notify at session.go:643-647 is gated on
// `cur.autonomous`, and in production EVERY turnFrame is autonomous — the only
// non-autonomous path is session.StartTurn, whose sole caller is
// nothing in the tree (host.Session, its only caller, was deleted as dead code;
// cmd/enter.go builds only host.NewMCPBridge). So a real mid-turn fault also
// gets the teardown and yields two surfaces. Omitting the teardown here is what
// keeps this test from enshrining that double-publish shape as desired while
// still failing loudly for the translator wrong fix. The double publish itself
// is tracked as QUM-967.
func TestAppModel_GenuineFaultMidTurn_SurfacesExactlyOneError(t *testing.T) {
	rt, sess := newBoundaryRuntime(t, "weave")
	ch, unsub := rt.EventBus().SubscribeNamed("root-midturn-fault", 64)
	defer unsub()

	openMidTurnVia(t, rt, sess)
	sess.fireTerminalErr(t, backend.ErrSubprocessExited)

	msgs := drainTranslated(t, ch, InterruptedAsCompleted)
	if got := countErrorSurfaces(msgs); got != 1 {
		t.Errorf("a mid-turn backend fault produced %d error surfaces, want exactly 1; msgs=%#v", got, msgs)
	}
	app := applyAll(t, applyResize(t, newTestAppModel(t)), msgs)
	if !app.showError {
		t.Error("a mid-turn backend fault surfaced no Session Error dialog")
	}
}

// QUM-931/QUM-935 (T5) — the Esc-burst-from-submit shape at the REDUCER layer:
// what the user actually sees. Wire order:
//
//	user (optimistic submit) → interrupt → system/init → result{is_error}
//
// The arm precedes its OWN turn's init, so a mechanism that can only name "the
// currently open turn" (or that retires arms on init) surfaces the empty,
// fatal-looking "Session Error" modal on a session whose backend never died.
// Asserted here rather than only on the bus because the bus cannot see what the
// TUI renders — the mistake this file exists to correct.
func TestAppModel_OptimisticSubmitEscThenInitThenIsError_StaysCleanInRootPane(t *testing.T) {
	rt, sess := newBoundaryRuntime(t, "weave")
	ch, unsub := rt.EventBus().SubscribeNamed("submit-esc-reducer", 64)
	defer unsub()

	// Submit from idle: InTurn is true optimistically, with NO frame turn open yet.
	if _, err := rt.WriteUserPrompt(context.Background(), "hi", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	if !rt.State().InTurn {
		t.Fatal("setup: State().InTurn is false after an optimistic submit")
	}
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	// The arm's own turn opens here, then terminates with the interrupt's is_error.
	sess.routeFrame(t, &protocol.Message{Type: "system", Subtype: "init"}, backend.TurnInfo{Autonomous: true})
	sess.routeFrame(t, resultFrameForTest(t, true), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	msgs := drainTranslated(t, ch, InterruptedAsCompleted)
	app := applyAll(t, applyResize(t, newTestAppModel(t)), msgs)

	if got := countErrorSurfaces(msgs); got != 0 {
		t.Errorf("an Esc burst from submit produced %d error surfaces, want 0; msgs=%#v", got, msgs)
	}
	if app.showError {
		t.Errorf("an Esc burst from submit raised the fatal \"Session Error\" dialog on a live session (QUM-935); dialog=%q",
			ansi.Strip(app.View().Content))
	}
	if label := app.statusBar.TransientLabel(); !strings.Contains(label, "Interrupted") {
		t.Errorf("transient label = %q, want it to contain %q", label, "Interrupted")
	}
}

// The fault-sensitive twin of T5, and the reason T5 alone is not enough: the
// crude fix "classify every is_error terminal as an interrupt" satisfies T5 while
// silently swallowing genuine crashes. A REAL backend fault in the same wire
// shape must still surface exactly one Session Error.
func TestAppModel_OptimisticSubmitEscWithGenuineFault_StillSurfacesSessionError(t *testing.T) {
	rt, sess := newBoundaryRuntime(t, "weave")
	ch, unsub := rt.EventBus().SubscribeNamed("submit-esc-fault-reducer", 64)
	defer unsub()

	if _, err := rt.WriteUserPrompt(context.Background(), "hi", "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	sess.routeFrame(t, &protocol.Message{Type: "system", Subtype: "init"}, backend.TurnInfo{Autonomous: true})
	// The subprocess genuinely dies while the interrupt is armed.
	sess.fireTerminalErr(t, backend.ErrSubprocessExited)
	sess.routeFrame(t, nil, backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	msgs := drainTranslated(t, ch, InterruptedAsCompleted)
	app := applyAll(t, applyResize(t, newTestAppModel(t)), msgs)

	if !app.showError {
		t.Fatalf("a genuine backend fault was swallowed by the interrupt arm; the user is left with a dead subprocess and only %q",
			app.statusBar.TransientLabel())
	}
	if content := ansi.Strip(app.View().Content); !strings.Contains(content, "subprocess exited") {
		t.Errorf("Session Error does not name the real fault; want %q, got:\n%s", "subprocess exited", content)
	}
}

// -- child pane --------------------------------------------------------------

// A child's fault must surface EXACTLY ONCE, via the agent-named supervisor
// path (runFaultSubscriber → Real.dispatchFault → cmd/enter.go
// SetBackendFaultEmitter → BackendFaultMsg).
//
// The child leg is routed FAITHFULLY here: production wraps every child event
// in ChildStreamMsg{Agent,Epoch} (childStreamWaitCmd, app.go:3677) and applies
// it via applyChildStreamInner (app.go:3699), which writes to the child's
// buffer. Feeding translated child events straight into the root reducer — as
// an earlier draft did — is not the production route and makes the whole test
// meaningless: it would let a child's fault raise the ROOT Session Error dialog
// and the test would not notice.
//
// So the assertion is: the child's own bus traffic contributes NO second fault
// surface and does not hijack the root pane's error dialog. The named
// supervisor-path fault is the single surface.
func TestAppModel_ChildFaultAtTurnBoundary_SurfacesExactlyOnceViaSupervisorPath(t *testing.T) {
	rt, sess := newBoundaryRuntime(t, "kid")

	reg := supervisor.NewRuntimeRegistry()
	registerUnified(t, reg, "kid", rt)
	app := newAppWithRegistry(t, &supervisorWithRegistry{reg: reg})

	// Attach the real per-child adapter, then clear the backfill gate so live
	// events are applied rather than queued (app.go's childBackfillPending).
	app = sendMsg(t, app, AgentSelectedMsg{Name: "kid"})
	app = sendMsg(t, app, ChildTranscriptMsg{Agent: "kid", SessionID: "sid"})
	if got := app.ChildAdapterAgent(); got != "kid" {
		t.Fatalf("ChildAdapterAgent() = %q, want %q; the child leg would be dropped by the epoch/agent guard", got, "kid")
	}
	epoch := app.ChildAdapterEpoch()

	// Our own subscription, so draining cannot steal the adapter's events.
	ch, unsub := rt.EventBus().SubscribeNamed("child-boundary-fault", 64)
	defer unsub()

	openTurnBoundaryVia(t, rt, sess)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	sess.fireTerminalErr(t, backend.ErrSubprocessExited)
	sess.routeFrame(t, nil, backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	// The supervisor path a child ALREADY has — the authoritative, agent-named
	// fault surface.
	app = sendMsg(t, app, BackendFaultMsg{
		Agent:      "kid",
		Class:      "SubprocessExited",
		Reason:     backend.ErrSubprocessExited.Error(),
		NextAction: "wake",
	})
	// The child viewport path over the same bus, wrapped exactly as production
	// does. InterruptedAsResult is the ChildStreamAdapter's handler.
	inners := drainTranslated(t, ch, InterruptedAsResult)
	// Anti-vacuity: without this the whole child-bus leg can be DELETED and every
	// assertion below still passes, because applyChildStreamInner's SessionResultMsg
	// arm only finalizes the child buffer and cannot move app.faults or showError.
	// The leg's job is to prove a fault-bearing child terminal really did traverse
	// the child route and still produced no second surface — so its arrival has to
	// be asserted, not assumed.
	var sawChildFaultTerminal bool
	for _, inner := range inners {
		if r, ok := inner.(SessionResultMsg); ok && r.IsError {
			sawChildFaultTerminal = true
		}
		app = sendMsg(t, app, ChildStreamMsg{Agent: "kid", Epoch: epoch, Inner: inner})
	}
	if !sawChildFaultTerminal {
		t.Fatalf("the child bus produced no error-bearing terminal, so this test is not exercising the child fault route at all; msgs=%#v", inners)
	}

	if got := len(app.faults); got != 1 {
		t.Errorf("child fault surfaced %d times, want exactly 1; faults=%v", got, app.faults)
	}
	if _, named := app.faults["kid"]; !named {
		t.Errorf("no fault recorded for agent %q; the authoritative supervisor-path surface was lost; faults=%v", "kid", app.faults)
	}
	if _, blank := app.faults[""]; blank {
		t.Errorf("a fault was recorded under the empty agent key; faults=%v", app.faults)
	}
	// A CHILD's fault must not raise the ROOT pane's modal session-error dialog.
	if app.showError {
		t.Errorf("a child's backend fault raised the root Session Error dialog; child terminals belong in the child buffer (applyChildStreamInner)")
	}
}
