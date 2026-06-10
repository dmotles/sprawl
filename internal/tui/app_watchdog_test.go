// QUM-775 item 2: TUI liveness watchdog.
//
// Scenario reproduced: the runtime's terminal EventTurnCompleted is dropped
// (or arrives during a quiescent period that prevents gap-detection from
// firing). The AppModel's turnState stays in TurnStreaming/TurnThinking
// forever, gating input and Esc.
//
// Fix: a self-perpetuating ticker periodically checks whether turnState has
// been "Streaming/Thinking with no bus activity for > watchdogTimeout". When
// so, the reducer queries the backend's optional LivenessProbe capability
// (RuntimeInTurn()); if the runtime is genuinely idle, finalizeTurn() fires
// and recovers the wedged TUI.

package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// fakeLivenessBackend embeds fakeSessionBackend and implements LivenessProbe.
// Tests toggle inTurn to dictate what the watchdog will observe.
type fakeLivenessBackend struct {
	*fakeSessionBackend
	inTurn bool
}

func (f *fakeLivenessBackend) RuntimeInTurn() bool { return f.inTurn }

func newAppForWatchdogTest(t *testing.T, inTurn bool) (AppModel, *fakeLivenessBackend) {
	t.Helper()
	probe := &fakeLivenessBackend{fakeSessionBackend: newFakeSessionBackend(), inTurn: inTurn}
	probe.SetContinuous(true)
	m := NewAppModel("colour212", "testrepo", "v0.1.0", probe, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := updated.(AppModel)
	return app, probe
}

// TestWatchdog_FinalizesWhenStuckStreamingAndRuntimeIdle is the headline
// regression test for QUM-775. After the watchdog timeout has elapsed with
// no bus activity and the runtime reports !InTurn, the watchdog must drive
// turnState back to TurnIdle.
func TestWatchdog_FinalizesWhenStuckStreamingAndRuntimeIdle(t *testing.T) {
	app, _ := newAppForWatchdogTest(t, false)

	// Freeze a clock the test can advance. lastBusActivityAt is set to "now"
	// initially; advance the clock past watchdogTimeout to trigger.
	now := time.Unix(1_700_000_000, 0)
	app.watchdogClock = func() time.Time { return now }
	app.lastBusActivityAt = now
	app.setTurnState(TurnStreaming)

	// Advance past the watchdog timeout.
	now = now.Add(app.watchdogTimeout + time.Second)

	updated, _ := app.Update(TurnWatchdogTickMsg{})
	next := updated.(AppModel)

	if next.turnState != TurnIdle {
		t.Errorf("watchdog did not finalize wedged turn: turnState = %v, want TurnIdle", next.turnState)
	}
}

// TestWatchdog_NoOpWhenRuntimeStillInTurn ensures the watchdog respects the
// LivenessProbe answer: if the runtime says "yes, I am still in a turn", the
// TUI must keep showing TurnStreaming. This guards against false positives
// from a slow but live backend.
func TestWatchdog_NoOpWhenRuntimeStillInTurn(t *testing.T) {
	app, _ := newAppForWatchdogTest(t, true)

	now := time.Unix(1_700_000_000, 0)
	app.watchdogClock = func() time.Time { return now }
	app.lastBusActivityAt = now
	app.setTurnState(TurnStreaming)

	now = now.Add(app.watchdogTimeout + time.Second)

	updated, _ := app.Update(TurnWatchdogTickMsg{})
	next := updated.(AppModel)

	if next.turnState != TurnStreaming {
		t.Errorf("watchdog spuriously finalized live turn: turnState = %v, want TurnStreaming", next.turnState)
	}
}

// TestWatchdog_NoOpWithinTimeout: with the clock not yet past the timeout,
// the watchdog must not act regardless of LivenessProbe state.
func TestWatchdog_NoOpWithinTimeout(t *testing.T) {
	app, _ := newAppForWatchdogTest(t, false)

	now := time.Unix(1_700_000_000, 0)
	app.watchdogClock = func() time.Time { return now }
	app.lastBusActivityAt = now
	app.setTurnState(TurnStreaming)

	// Advance only a little (well within timeout).
	now = now.Add(5 * time.Second)
	if app.watchdogTimeout <= 5*time.Second {
		t.Fatalf("test precondition violated: watchdogTimeout=%s must be > 5s", app.watchdogTimeout)
	}

	updated, _ := app.Update(TurnWatchdogTickMsg{})
	next := updated.(AppModel)

	if next.turnState != TurnStreaming {
		t.Errorf("watchdog fired prematurely: turnState = %v, want TurnStreaming", next.turnState)
	}
}

// TestWatchdog_NoOpWhenTurnIdle: when turnState is already Idle, the
// watchdog must not call finalizeTurn (and must not query the probe — but
// we observe via state being unchanged).
func TestWatchdog_NoOpWhenTurnIdle(t *testing.T) {
	app, _ := newAppForWatchdogTest(t, false)

	now := time.Unix(1_700_000_000, 0)
	app.watchdogClock = func() time.Time { return now }
	app.lastBusActivityAt = now

	now = now.Add(app.watchdogTimeout + time.Second)

	updated, _ := app.Update(TurnWatchdogTickMsg{})
	next := updated.(AppModel)

	if next.turnState != TurnIdle {
		t.Errorf("watchdog mutated idle state: turnState = %v, want TurnIdle", next.turnState)
	}
}

// TestWatchdog_BackendWithoutProbeCapability: a backend that does not
// implement LivenessProbe must not panic and must not spuriously finalize.
// (Belt-and-braces: the watchdog must fail safe.)
func TestWatchdog_BackendWithoutProbeCapability(t *testing.T) {
	fake := newFakeSessionBackend()
	fake.SetContinuous(true)
	m := NewAppModel("colour212", "testrepo", "v0.1.0", fake, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := updated.(AppModel)

	now := time.Unix(1_700_000_000, 0)
	app.watchdogClock = func() time.Time { return now }
	app.lastBusActivityAt = now
	app.setTurnState(TurnStreaming)

	now = now.Add(app.watchdogTimeout + time.Second)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("watchdog panicked on backend without LivenessProbe: %v", r)
		}
	}()
	updated2, _ := app.Update(TurnWatchdogTickMsg{})
	next := updated2.(AppModel)

	if next.turnState != TurnStreaming {
		t.Errorf("watchdog finalized despite no probe capability: turnState = %v, want TurnStreaming", next.turnState)
	}
}

// TestWatchdog_BusActivityResetsTimer: receiving a bus-derived msg
// (SessionResultMsg here) must update lastBusActivityAt so subsequent
// watchdog ticks see a fresh activity time.
func TestWatchdog_BusActivityResetsTimer(t *testing.T) {
	app, _ := newAppForWatchdogTest(t, false)

	now := time.Unix(1_700_000_000, 0)
	app.watchdogClock = func() time.Time { return now }
	// Initial activity timestamp is old.
	app.lastBusActivityAt = now.Add(-5 * time.Minute)
	app.setTurnState(TurnStreaming)

	// Advance the clock and inject a SessionResultMsg — a translated bus
	// event. (Even though SessionResultMsg also fires finalizeTurn, the
	// activity update must happen before the reducer runs so the timestamp
	// is captured regardless.)
	now = now.Add(1 * time.Second)
	updated, _ := app.Update(AssistantContentMsg{Msgs: []tea.Msg{}})
	next := updated.(AppModel)

	if !next.lastBusActivityAt.Equal(now) {
		t.Errorf("lastBusActivityAt = %v, want %v (bus activity should reset the watchdog timer)", next.lastBusActivityAt, now)
	}
}

// TestWatchdog_NonBusMsgsDoNotResetTimer guards against the noteBusActivityIfApplicable
// helper drifting to "everything resets the timer" — which would silently disable the
// wedge-recovery path. Window resizes, ticks, and key events must NOT update
// lastBusActivityAt. (Code-review nit from QUM-775 reviewer.)
func TestWatchdog_NonBusMsgsDoNotResetTimer(t *testing.T) {
	app, _ := newAppForWatchdogTest(t, false)

	t0 := time.Unix(1_700_000_000, 0)
	app.watchdogClock = func() time.Time { return t0.Add(5 * time.Minute) }
	// Set lastBusActivityAt to a fixed sentinel.
	app.lastBusActivityAt = t0

	nonBusMsgs := []tea.Msg{
		tea.WindowSizeMsg{Width: 80, Height: 24},
		tea.KeyPressMsg{Code: 'x'},
		mcpOpTickMsg{},
		TurnWatchdogTickMsg{},
	}
	for _, msg := range nonBusMsgs {
		updated, _ := app.Update(msg)
		next := updated.(AppModel)
		if !next.lastBusActivityAt.Equal(t0) {
			t.Errorf("non-bus msg %T reset lastBusActivityAt to %v; want it to stay at %v",
				msg, next.lastBusActivityAt, t0)
		}
		app = next
	}
}
