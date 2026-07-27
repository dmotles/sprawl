// QUM-978: EventDropDetectedMsg is pump-delivered — TUIAdapter.WaitForEvent
// returns it directly from its read loop, consuming the single armed cmd. Its
// reducer must therefore re-issue m.bridge.WaitForEvent() on every exit leg,
// or a detected gap parks the bubbletea event pump and live render freezes
// until an unrelated cmd re-arms it (QUM-826 invariant).
//
// The issue names four legs, but only THREE return statements are reachable:
// the inner `if m.resyncInFlight` inside the burst branch is dead code (the
// top-of-arm guard already returned). The fourth precondition — "already
// dropped" — is real and reachable (a FAILED resync leaves gapStateDropped
// with resyncInFlight cleared), it just lands on the same return as the burst
// leg via the `||` disjunct. These tests cover every reachable leg and every
// distinct precondition; the dead return is re-armed for symmetry only.
//
// The re-arm belongs in the EventDropDetectedMsg reducer ONLY. ViewportResyncMsg
// and gapConfirmMsg are produced by resyncCmd / gapDebounceCmd (and by the Ctrl+L
// manual arm, which consumes no pump event at all), never by the pump — re-arming
// there too would leave two WaitForEvent cmds racing for one event and manufacture
// the spurious lastSeq gaps QUM-669 exists to detect.
//
// Assertion rigor. The four per-leg tests plus RearmExactlyOncePerDrop were run
// red before the fix ("waitCalls = 0, want 1"). The two tests that are green
// both before and after — TestAppModel_CtrlL_DoesNotRearmPump and
// TestAppModel_EventDropDetectedMsg_NilBridge_NoPanic — were demonstrated by
// mutation instead:
//   - Ctrl+L: inserting `_ = m.bridge.WaitForEvent()` at the top of the
//     `case ViewportResyncMsg:` arm yields
//     "after Ctrl+L ViewportResyncMsg waitCalls = 1, want 0".
//   - NilBridge: an unguarded `m.bridge.WaitForEvent()` in the drop reducer
//     panics with a nil-pointer dereference at app.go's burst return.

package tui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// gapSubThreshold is a drop size guaranteed to be below gapBurstThreshold, so
// the "below burst" tests cannot silently become burst tests if the threshold
// is ever retuned.
func gapSubThreshold(t *testing.T) uint64 {
	t.Helper()
	if gapBurstThreshold < 3 {
		t.Fatalf("test precondition violated: gapBurstThreshold=%d must be >= 3 for a meaningful sub-threshold drop", gapBurstThreshold)
	}
	return gapBurstThreshold - 1
}

// gapBurstDrop returns a drop msg at the burst threshold.
func gapBurstDrop() EventDropDetectedMsg {
	return EventDropDetectedMsg{From: 1, To: 1 + gapBurstThreshold + 1, Missing: gapBurstThreshold}
}

// gapSmallDrop returns a sub-threshold drop msg.
func gapSmallDrop(t *testing.T) EventDropDetectedMsg {
	t.Helper()
	n := gapSubThreshold(t)
	return EventDropDetectedMsg{From: 5, To: 5 + n + 1, Missing: n}
}

// TestAppModel_EventDropDetectedMsg_BelowBurst_RearmsPump — leg (d).
func TestAppModel_EventDropDetectedMsg_BelowBurst_RearmsPump(t *testing.T) {
	app, fake := newAppForDropTest(t)
	fake.waitCalls = 0

	updated, cmd := app.Update(gapSmallDrop(t))
	next := updated.(AppModel)

	if fake.waitCalls != 1 {
		t.Errorf("below-burst drop waitCalls = %d, want 1 (pump re-arm)", fake.waitCalls)
	}
	if cmd == nil {
		t.Fatal("below-burst drop returned nil cmd; expected debounce + WaitForEvent re-arm")
	}
	// The debounce must still be armed — the re-arm is additive, not a swap.
	if _, ok := findGapConfirm(t, cmd); !ok {
		t.Error("below-burst drop no longer schedules a gapConfirmMsg debounce tick")
	}
	if next.gapState != gapStatePending {
		t.Errorf("gapState = %v, want gapStatePending", next.gapState)
	}
}

// TestAppModel_EventDropDetectedMsg_Burst_RearmsPump — leg (b).
func TestAppModel_EventDropDetectedMsg_Burst_RearmsPump(t *testing.T) {
	app, fake := newAppForDropTest(t)
	fake.waitCalls = 0

	updated, cmd := app.Update(gapBurstDrop())
	next := updated.(AppModel)

	if fake.waitCalls != 1 {
		t.Errorf("burst drop waitCalls = %d, want 1 (pump re-arm)", fake.waitCalls)
	}
	if cmd == nil {
		t.Fatal("burst drop returned nil cmd; expected resync + WaitForEvent re-arm")
	}
	// The resync must still be kicked — the re-arm is additive, not a swap.
	if _, ok := findViewportResync(t, cmd); !ok {
		t.Error("burst drop no longer kicks a resync (ViewportResyncMsg absent from returned cmd)")
	}
	if !next.resyncInFlight {
		t.Error("resyncInFlight = false, want true after burst drop")
	}
	if next.gapState != gapStateResyncing {
		t.Errorf("gapState = %v, want gapStateResyncing", next.gapState)
	}
}

// TestAppModel_EventDropDetectedMsg_ResyncInFlight_RearmsPump — leg (a). The
// precondition is reached the way production reaches it: a burst drop kicks a
// resync, then a second drop lands before the resync result does.
func TestAppModel_EventDropDetectedMsg_ResyncInFlight_RearmsPump(t *testing.T) {
	app, fake := newAppForDropTest(t)

	updated, _ := app.Update(gapBurstDrop())
	app = updated.(AppModel)
	if !app.resyncInFlight {
		t.Fatalf("precondition not reached: resyncInFlight = false after burst drop")
	}

	fake.waitCalls = 0
	small := gapSmallDrop(t)
	updated2, cmd := app.Update(small)
	next := updated2.(AppModel)

	if fake.waitCalls != 1 {
		t.Errorf("resync-in-flight drop waitCalls = %d, want 1 (pump re-arm)", fake.waitCalls)
	}
	// The re-arm must not have turned this leg into a second resync.
	if _, ok := findViewportResync(t, cmd); ok {
		t.Error("a drop arriving during an in-flight resync kicked a second resync")
	}
	// Existing coalescing behaviour must be untouched.
	if next.pendingMissing != small.Missing {
		t.Errorf("pendingMissing = %d, want %d (absorbed for post-resync banner accuracy)", next.pendingMissing, small.Missing)
	}
	if !next.resyncInFlight {
		t.Error("resyncInFlight = false; an in-flight resync must not be cancelled by a follow-up drop")
	}
}

// TestAppModel_EventDropDetectedMsg_AlreadyDropped_RearmsPump — leg (c). The
// reachable precondition is gapState == gapStateDropped with resyncInFlight
// already cleared, which is what a FAILED resync leaves behind.
func TestAppModel_EventDropDetectedMsg_AlreadyDropped_RearmsPump(t *testing.T) {
	app, fake := newAppForDropTest(t)

	updated, _ := app.Update(gapBurstDrop())
	app = updated.(AppModel)
	updated2, _ := app.Update(ViewportResyncMsg{Err: errors.New("boom")})
	app = updated2.(AppModel)
	if app.gapState != gapStateDropped || app.resyncInFlight {
		t.Fatalf("precondition not reached: gapState=%v resyncInFlight=%v, want gapStateDropped/false",
			app.gapState, app.resyncInFlight)
	}

	fake.waitCalls = 0
	// Sub-threshold: only the `gapState == gapStateDropped` disjunct can fire.
	updated3, cmd := app.Update(gapSmallDrop(t))
	next := updated3.(AppModel)

	if fake.waitCalls != 1 {
		t.Errorf("already-dropped drop waitCalls = %d, want 1 (pump re-arm)", fake.waitCalls)
	}
	if cmd == nil {
		t.Fatal("already-dropped drop returned nil cmd; expected resync + WaitForEvent re-arm")
	}
	if !next.resyncInFlight {
		t.Error("resyncInFlight = false; an already-dropped gap must re-kick the resync")
	}
}

// TestAppModel_EventDropDetectedMsg_NilBridge_NoPanic pins the guard. Several
// existing tests (e.g. app_transient_label_test.go) build an AppModel with a
// nil bridge, and resyncCmd already nil-guards; an unguarded
// m.bridge.WaitForEvent() in the drop reducer segfaults the whole package.
func TestAppModel_EventDropDetectedMsg_NilBridge_NoPanic(t *testing.T) {
	newNilBridgeApp := func() AppModel {
		m := NewAppModel("colour212", "testrepo", "v0.1.0", nil, nil, "", nil)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		return updated.(AppModel)
	}

	// leg (d): below burst.
	app := newNilBridgeApp()
	_, _ = app.Update(gapSmallDrop(t))
	// leg (b): burst.
	app = newNilBridgeApp()
	burst, _ := app.Update(gapBurstDrop())
	// leg (a): drop during an in-flight resync.
	inflight := burst.(AppModel)
	if !inflight.resyncInFlight {
		t.Fatalf("precondition not reached: resyncInFlight = false after burst drop")
	}
	_, _ = inflight.Update(gapSmallDrop(t))
	// leg (c): already dropped.
	failed, _ := inflight.Update(ViewportResyncMsg{Err: errors.New("boom")})
	dropped := failed.(AppModel)
	if dropped.gapState != gapStateDropped {
		t.Fatalf("precondition not reached: gapState = %v, want gapStateDropped", dropped.gapState)
	}
	_, _ = dropped.Update(gapSmallDrop(t))
	// Reaching here without a panic is the assertion: all four legs survived
	// a nil bridge.
}

// TestAppModel_GapReducers_RearmExactlyOncePerDrop — AC #2. Exactly one
// WaitForEvent per pump-delivered msg: the downstream ViewportResyncMsg and
// gapConfirmMsg reducers must NOT add a second armed cmd.
func TestAppModel_GapReducers_RearmExactlyOncePerDrop(t *testing.T) {
	// Part 1 — burst drop, then both ViewportResyncMsg legs.
	app, fake := newAppForDropTest(t)
	fake.waitCalls = 0

	updated, cmd := app.Update(gapBurstDrop())
	app = updated.(AppModel)
	if fake.waitCalls != 1 {
		t.Fatalf("after burst drop waitCalls = %d, want 1", fake.waitCalls)
	}
	// newAppForDropTest has no sprawlRoot, so the produced resync carries the
	// "sprawlRoot unset" error — that is the error leg of the reducer.
	resync, ok := findViewportResync(t, cmd)
	if !ok {
		t.Fatalf("burst drop produced no ViewportResyncMsg; cannot drive the downstream reducer")
	}
	if resync.Err == nil {
		t.Fatalf("expected the sprawlRoot-unset error leg; got a success ViewportResyncMsg")
	}
	updated2, _ := app.Update(resync)
	app = updated2.(AppModel)
	if fake.waitCalls != 1 {
		t.Errorf("after ViewportResyncMsg (error leg) waitCalls = %d, want 1 — ViewportResyncMsg is not pump-delivered and must not re-arm (double-arm reintroduces QUM-669 false positives)", fake.waitCalls)
	}
	// Success leg of the same reducer.
	updated3, _ := app.Update(ViewportResyncMsg{MissingCount: gapBurstThreshold})
	app = updated3.(AppModel)
	if fake.waitCalls != 1 {
		t.Errorf("after ViewportResyncMsg (success leg) waitCalls = %d, want 1", fake.waitCalls)
	}
	if app.gapState != gapStateNormal {
		t.Fatalf("gapState = %v after a successful resync, want gapStateNormal", app.gapState)
	}

	// Part 2 — sub-threshold drop on a fresh model, then all gapConfirmMsg legs.
	app2, fake2 := newAppForDropTest(t)
	fake2.waitCalls = 0

	updated4, cmd4 := app2.Update(gapSmallDrop(t))
	app2 = updated4.(AppModel)
	if fake2.waitCalls != 1 {
		t.Fatalf("after sub-threshold drop waitCalls = %d, want 1", fake2.waitCalls)
	}
	gc, ok := findGapConfirm(t, cmd4)
	if !ok {
		t.Fatalf("sub-threshold drop produced no gapConfirmMsg; cannot drive the downstream reducer")
	}

	// Stale confirm (gapID mismatch) — no re-arm.
	updated5, _ := app2.Update(gapConfirmMsg{gapID: gc.gapID + 4242})
	app2 = updated5.(AppModel)
	if fake2.waitCalls != 1 {
		t.Errorf("after stale gapConfirmMsg waitCalls = %d, want 1", fake2.waitCalls)
	}

	// Burst-crossed confirm leg: accumulation crossed the threshold inside the
	// debounce window. Only reachable defensively, so stage pendingMissing
	// directly rather than inventing an unreachable msg sequence.
	app2.pendingMissing = gapBurstThreshold
	updated6, _ := app2.Update(gc)
	app2 = updated6.(AppModel)
	if !app2.resyncInFlight {
		t.Fatalf("burst-crossed gapConfirmMsg leg not reached: resyncInFlight = false")
	}
	if fake2.waitCalls != 1 {
		t.Errorf("after burst-crossed gapConfirmMsg waitCalls = %d, want 1 — gapConfirmMsg is not pump-delivered and must not re-arm", fake2.waitCalls)
	}

	// Single-blip confirm leg on a third model.
	app3, fake3 := newAppForDropTest(t)
	fake3.waitCalls = 0
	updated7, cmd7 := app3.Update(gapSmallDrop(t))
	app3 = updated7.(AppModel)
	gc3, ok := findGapConfirm(t, cmd7)
	if !ok {
		t.Fatalf("sub-threshold drop produced no gapConfirmMsg on the third model")
	}
	updated8, _ := app3.Update(gc3)
	app3 = updated8.(AppModel)
	if app3.gapState != gapStateNormal {
		t.Fatalf("single-blip confirm leg not reached: gapState = %v, want gapStateNormal", app3.gapState)
	}
	if fake3.waitCalls != 1 {
		t.Errorf("after single-blip gapConfirmMsg waitCalls = %d, want 1", fake3.waitCalls)
	}
}

// TestAppModel_CtrlL_DoesNotRearmPump pins the decisive case for putting the
// re-arm in the drop reducer rather than the resync reducer: Ctrl+L produces a
// ViewportResyncMsg without consuming any pump event at all, so a re-arm on
// that path would be permanently unmatched.
func TestAppModel_CtrlL_DoesNotRearmPump(t *testing.T) {
	app, fake := newAppForDropTest(t)
	fake.waitCalls = 0

	updated, cmd := app.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	app = updated.(AppModel)
	if !app.resyncInFlight {
		t.Fatalf("Ctrl+L did not kick a resync; test is not exercising the intended path")
	}
	if fake.waitCalls != 0 {
		t.Fatalf("Ctrl+L waitCalls = %d, want 0 — no pump event was consumed", fake.waitCalls)
	}

	resync, ok := findViewportResync(t, cmd)
	if !ok {
		t.Fatalf("Ctrl+L produced no ViewportResyncMsg")
	}
	updated2, _ := app.Update(resync)
	app = updated2.(AppModel)
	if fake.waitCalls != 0 {
		t.Errorf("after Ctrl+L ViewportResyncMsg waitCalls = %d, want 0 — re-arming in the ViewportResyncMsg reducer would double-arm on every manual resync", fake.waitCalls)
	}
}
