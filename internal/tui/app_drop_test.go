// QUM-669 step 3-4 tests: gap-detection state machine + debounce.
//
// These tests are RED-phase TDD: the AppModel does not yet reduce
// EventDropDetectedMsg or gapConfirmMsg into any state. The constants
// `gapDebounceWindow` and `gapBurstThreshold` are stubbed in app.go so the
// tests can reference them by name. The implementer's job is the reducer
// wiring described in docs/designs/qum-669-viewport-wedge-recovery.md §2.3 –
// §2.7. Do not relax these tests to pass without that wiring.

package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// newAppForDropTest builds an AppModel against a continuous fakeSessionBackend
// sized to a renderable terminal (80x24). Returns the AppModel and the fake
// backend so individual tests can stage messages / inspect call counters.
func newAppForDropTest(t *testing.T) (AppModel, *fakeSessionBackend) {
	t.Helper()
	fake := newFakeSessionBackend()
	fake.SetContinuous(true)
	m := NewAppModel("colour212", "testrepo", "v0.1.0", fake, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(AppModel), fake
}

// runCmd invokes a tea.Cmd once and returns the produced msg. Nil-safe.
//
//nolint:unused // helper retained for follow-up tests; pruning to be revisited
func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	return cmd()
}

// findGapConfirm walks a possibly-batched cmd output and returns the first
// gapConfirmMsg encountered (if any). Returns ok=false when no such msg is
// produced.
func findGapConfirm(t *testing.T, cmd tea.Cmd) (gapConfirmMsg, bool) {
	t.Helper()
	msgs := collectBatchMsgs(t, cmd)
	for _, m := range msgs {
		if gc, ok := m.(gapConfirmMsg); ok {
			return gc, true
		}
	}
	return gapConfirmMsg{}, false
}

// findViewportResync walks a possibly-batched cmd output and returns the first
// ViewportResyncMsg encountered.
func findViewportResync(t *testing.T, cmd tea.Cmd) (ViewportResyncMsg, bool) {
	t.Helper()
	msgs := collectBatchMsgs(t, cmd)
	for _, m := range msgs {
		if r, ok := m.(ViewportResyncMsg); ok {
			return r, true
		}
	}
	return ViewportResyncMsg{}, false
}

func TestAppModel_GapBelowBurst_EntersDebouncePending(t *testing.T) {
	app, _ := newAppForDropTest(t)
	app.setTurnState(TurnStreaming)

	// Missing=3 is well below gapBurstThreshold (10): expect the
	// gap-pending debounce path, NOT an immediate resync.
	if gapBurstThreshold <= 3 {
		t.Fatalf("test precondition violated: gapBurstThreshold=%d must be > 3", gapBurstThreshold)
	}
	updated, cmd := app.Update(EventDropDetectedMsg{From: 5, To: 9, Missing: 3})
	next := updated.(AppModel)

	// AC #4 invariant: gap detection clears the wedged TurnStreaming state
	// IMMEDIATELY so Ctrl+C / quit chords are unblocked, even before the
	// debounce window elapses.
	if next.turnState != TurnIdle {
		t.Errorf("turnState after gap = %v, want TurnIdle (AC #4)", next.turnState)
	}

	// QUM-693: banners never enter ChatList, so the legacy "no banner in vp"
	// negative assertion is structurally vacuous — deleted.

	// A gapConfirmMsg should be scheduled (debounce tick).
	if _, ok := findGapConfirm(t, cmd); !ok {
		t.Errorf("expected returned cmd to schedule a gapConfirmMsg debounce tick, got nothing")
	}

	// And no resync cmd may run yet — that's only allowed after the debounce
	// confirms or above-threshold short-circuit.
	if _, ok := findViewportResync(t, cmd); ok {
		t.Errorf("did NOT expect a ViewportResyncMsg during gap-pending (Missing=%d < threshold=%d)", 3, gapBurstThreshold)
	}
}

func TestAppModel_GapDebounce_NoFurtherDropsReturnsToNormal(t *testing.T) {
	app, _ := newAppForDropTest(t)
	app.setTurnState(TurnStreaming)

	updated, cmd := app.Update(EventDropDetectedMsg{From: 5, To: 9, Missing: 3})
	next := updated.(AppModel)
	gc, ok := findGapConfirm(t, cmd)
	if !ok {
		t.Fatalf("debounce tick not scheduled; cannot drive the confirm path")
	}

	// Fire the gap-confirm tick. With no further drops in the window, the
	// reducer should walk back to normal: no resync produced, no banner.
	confirmed, confirmCmd := next.Update(gc)
	final := confirmed.(AppModel)

	if _, ok := findViewportResync(t, confirmCmd); ok {
		t.Errorf("gapConfirmMsg with no further drops must NOT trigger resync")
	}
	// QUM-693: banners never enter ChatList — negative assertion deleted.
	view := final.statusBar.View()
	if strings.Contains(strings.ToLower(view), "resync") {
		t.Errorf("status bar should not show a resync pill after clean debounce, got:\n%s", view)
	}
}

func TestAppModel_GapAboveBurstThreshold_TriggersImmediateResync(t *testing.T) {
	fake := newFakeSessionBackend()
	fake.SetContinuous(true)
	fake.SetSessionID("sid")

	sprawlRoot, homeDir := writeRootSessionFixture(t, "sid", []string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
	})

	m := NewAppModel("colour212", "testrepo", "v0.1.0", fake, nil, sprawlRoot, nil)
	m.SetHomeDir(homeDir)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := updated.(AppModel)
	app.setTurnState(TurnStreaming)

	missing := gapBurstThreshold + 5
	updated2, cmd := app.Update(EventDropDetectedMsg{From: 1, To: 1 + missing + 1, Missing: missing})
	next := updated2.(AppModel)

	if next.turnState != TurnIdle {
		t.Errorf("turnState = %v, want TurnIdle on burst-threshold gap (AC #4)", next.turnState)
	}

	// QUM-675 S5: drop banner now lives on the statusbar transient label.
	bar := strings.ToLower(stripAnsi(next.statusBar.View()))
	if !strings.Contains(bar, "events lost") && !strings.Contains(bar, "dropped") && !strings.Contains(bar, "gap detected") {
		t.Errorf("expected a transient drop banner on the statusbar; got: %s", bar)
	}

	resync, ok := findViewportResync(t, cmd)
	if !ok {
		t.Fatalf("expected ViewportResyncMsg on burst-threshold gap (Missing=%d ≥ threshold=%d); got no resync cmd", missing, gapBurstThreshold)
	}
	if resync.Err != nil {
		t.Errorf("ViewportResyncMsg.Err = %v, want nil", resync.Err)
	}
	if len(resync.Entries) == 0 {
		t.Errorf("ViewportResyncMsg.Entries is empty; LoadTranscript should have hydrated from the fixture")
	}
	if resync.MissingCount != missing {
		t.Errorf("ViewportResyncMsg.MissingCount = %d, want %d", resync.MissingCount, missing)
	}
}

// TestAppModel_TwoSubThresholdDrops_CoalesceIntoSingleResync covers the
// code-review follow-up #2: two EventDropDetectedMsg{Missing=K} (K<threshold)
// arriving back-to-back must accumulate into a single resync once the sum
// crosses gapBurstThreshold — not two parallel resyncs.
func TestAppModel_TwoSubThresholdDrops_CoalesceIntoSingleResync(t *testing.T) {
	fake := newFakeSessionBackend()
	fake.SetContinuous(true)
	fake.SetSessionID("sid")

	sprawlRoot, homeDir := writeRootSessionFixture(t, "sid", []string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
	})
	m := NewAppModel("colour212", "testrepo", "v0.1.0", fake, nil, sprawlRoot, nil)
	m.SetHomeDir(homeDir)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := updated.(AppModel)

	// First sub-threshold drop → gap-pending, no resync yet.
	half := gapBurstThreshold/2 + 1
	u1, c1 := app.Update(EventDropDetectedMsg{From: 1, To: 1 + half + 1, Missing: half})
	app = u1.(AppModel)
	if _, ok := findViewportResync(t, c1); ok {
		t.Fatalf("first sub-threshold drop produced a ViewportResyncMsg unexpectedly")
	}

	// Second sub-threshold drop pushes the accumulator over the threshold —
	// must produce exactly ONE ViewportResyncMsg cmd.
	u2, c2 := app.Update(EventDropDetectedMsg{From: 2, To: 2 + half + 1, Missing: half})
	app = u2.(AppModel)
	resync, ok := findViewportResync(t, c2)
	if !ok {
		t.Fatalf("second sub-threshold drop should have crossed gapBurstThreshold and kicked a resync")
	}
	if resync.MissingCount != half*2 {
		t.Errorf("coalesced MissingCount = %d, want %d (sum of two sub-threshold drops)", resync.MissingCount, half*2)
	}

	// A THIRD drop arriving while the first resync is in flight must NOT
	// kick a second resync (design §5 hotspot #1).
	if !app.resyncInFlight {
		t.Fatalf("expected resyncInFlight=true after resync dispatch; got false")
	}
	_, c3 := app.Update(EventDropDetectedMsg{From: 3, To: 5, Missing: 3})
	if _, ok := findViewportResync(t, c3); ok {
		t.Errorf("in-flight resync must coalesce subsequent drops; got a second ViewportResyncMsg")
	}
	if _, ok := findGapConfirm(t, c3); ok {
		t.Errorf("in-flight resync must not arm a fresh debounce; got gapConfirmMsg")
	}
}

func TestAppModel_GapStaleConfirmIgnored(t *testing.T) {
	app, _ := newAppForDropTest(t)
	app.setTurnState(TurnStreaming)

	// Cold-fire a gapConfirmMsg with a bogus id. With no prior gap-pending
	// state, this is a stale tick (mirrors mcpOpThresholdMsg's "ignore stale
	// deliveries for a finished call" pattern). The reducer must be a no-op.
	prevState := app.turnState
	updated, cmd := app.Update(gapConfirmMsg{gapID: 99999})
	next := updated.(AppModel)

	if next.turnState != prevState {
		t.Errorf("stale gapConfirmMsg mutated turnState %v → %v", prevState, next.turnState)
	}
	if cmd != nil {
		// A spurious cmd here would mean a resync is being kicked off without
		// a real gap — exactly the alarm-fatigue regression to guard against.
		if _, ok := findViewportResync(t, cmd); ok {
			t.Errorf("stale gapConfirmMsg produced a ViewportResyncMsg")
		}
	}
	// QUM-693: banners never enter ChatList — negative assertion deleted.
	_ = next
}
