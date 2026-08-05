// QUM-1071: tests for the standalone blurb-refresh ticker extracted out of the
// QUM-730 heartbeat.
//
// The failure mode this file exists to catch is silent: a ticker that is
// constructed but never started, or started but never reaching
// maybeRefreshBlurb, produces stale blurbs and no error anywhere.
//
// The fakes here are deliberately this file's own rather than the heartbeat's
// fakeProbe/fakeLister, which were typed to the heartbeat's probe interfaces and
// were deleted along with heartbeat_test.go (QUM-1071; neither file exists).

package supervisor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/blurb"
	"github.com/dmotles/sprawl/internal/state"
)

// --- fakes -------------------------------------------------------------------

// fakeBlurbProbe needs no lock: its fields are set at construction and never
// mutated, so the ticker goroutine's reads are ordered by the goroutine start.
type fakeBlurbProbe struct {
	name    string
	lastAct time.Time
}

func (p *fakeBlurbProbe) Name() string { return p.name }

func (p *fakeBlurbProbe) LastActivityAt() time.Time { return p.lastAct }

type fakeBlurbLister struct {
	probes []blurbProbe
}

func (l *fakeBlurbLister) List() []blurbProbe { return l.probes }

type refreshCall struct {
	name    string
	lastAct time.Time
}

// refreshRecorder records RefreshBlurb invocations. Mutex-guarded because the
// loop-driven tests call it from the ticker goroutine.
type refreshRecorder struct {
	mu    sync.Mutex
	calls []refreshCall
}

func (rec *refreshRecorder) fn() func(string, time.Time) {
	return func(name string, lastAct time.Time) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.calls = append(rec.calls, refreshCall{name: name, lastAct: lastAct})
	}
}

func (rec *refreshRecorder) Calls() []refreshCall {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]refreshCall(nil), rec.calls...)
}

// waitForCalls polls until the recorder has at least n calls, then returns them.
// Fails the test on timeout — never falls through (QUM-997).
func waitForCalls(t *testing.T, rec *refreshRecorder, n int) []refreshCall {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		calls := rec.Calls()
		if len(calls) >= n {
			return calls
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("RefreshBlurb calls = %d after 2s, want >= %d (calls: %+v)", len(calls), n, calls)
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- T1: the seam is reached with the exact per-agent values -----------------

// TestBlurbTicker_RunOnce_RefreshesEveryAgent is the re-homed
// TestHeartbeat_RefreshBlurb_FiresEveryTick (QUM-899 / QUM-1071 AC 4): one
// RefreshBlurb call per registry agent per tick, carrying that agent's own name
// and its own LastActivityAt.
//
// Two agents with DISTINCT, non-zero last-activity times is load-bearing: with a
// single agent, "pass time.Time{}" and "pass the wrong probe's value" are both
// undetectable.
//
// Note the heartbeat version set InTurn(true) to pin "an in-turn agent still gets
// its blurb refreshed". blurbProbe has no InTurn() at all, so that property now
// holds by construction — the intent is preserved, not dropped.
func TestBlurbTicker_RunOnce_RefreshesEveryAgent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	aliceAct := now.Add(-2 * time.Minute)
	bobAct := now.Add(-47 * time.Minute)

	lister := &fakeBlurbLister{probes: []blurbProbe{
		&fakeBlurbProbe{name: "alice", lastAct: aliceAct},
		&fakeBlurbProbe{name: "bob", lastAct: bobAct},
	}}
	rec := &refreshRecorder{}
	bt := newBlurbTicker(blurbTickerDeps{Registry: lister, RefreshBlurb: rec.fn()})

	bt.runOnce()

	calls := rec.Calls()
	if len(calls) != 2 {
		t.Fatalf("RefreshBlurb calls = %d, want 2 (one per registry agent)", len(calls))
	}
	got := make(map[string]time.Time, len(calls))
	for _, c := range calls {
		if _, dup := got[c.name]; dup {
			t.Fatalf("RefreshBlurb called twice for %q in one tick: %+v", c.name, calls)
		}
		got[c.name] = c.lastAct
	}
	for name, want := range map[string]time.Time{"alice": aliceAct, "bob": bobAct} {
		gotAct, ok := got[name]
		if !ok {
			t.Fatalf("no RefreshBlurb call for %q; got calls %+v", name, calls)
		}
		if !gotAct.Equal(want) {
			t.Errorf("RefreshBlurb(%q) lastActivityAt = %v, want %v", name, gotAct, want)
		}
	}
}

// TestBlurbTicker_RunOnce_SkipsEmptyNameAndNilSeams pins the defensive branches:
// an unnamed probe is skipped, and a nil Registry / nil RefreshBlurb is a no-op
// rather than a panic.
func TestBlurbTicker_RunOnce_SkipsEmptyNameAndNilSeams(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	rec := &refreshRecorder{}
	lister := &fakeBlurbLister{probes: []blurbProbe{
		&fakeBlurbProbe{name: "", lastAct: now},
		&fakeBlurbProbe{name: "alice", lastAct: now},
	}}
	bt := newBlurbTicker(blurbTickerDeps{Registry: lister, RefreshBlurb: rec.fn()})
	bt.runOnce()
	calls := rec.Calls()
	if len(calls) != 1 || calls[0].name != "alice" {
		t.Fatalf("calls = %+v, want exactly one call for \"alice\" (empty-name probe skipped)", calls)
	}

	// nil Registry: no panic, no calls.
	rec2 := &refreshRecorder{}
	newBlurbTicker(blurbTickerDeps{RefreshBlurb: rec2.fn()}).runOnce()
	if n := len(rec2.Calls()); n != 0 {
		t.Fatalf("nil-Registry ticker made %d RefreshBlurb calls, want 0", n)
	}

	// nil RefreshBlurb: the assertion is that this does not panic (a panic fails
	// the test), so there is nothing further to check.
	newBlurbTicker(blurbTickerDeps{Registry: lister}).runOnce()
}

// --- T1b: the loop goroutine actually reaches runOnce ------------------------

// TestBlurbTicker_Loop_RefreshesOnEveryTick closes the composition gap between
// T1 (runOnce works when called directly) and
// TestNewReal_StartsAndStopsBlurbTicker (a goroutine exists): neither proves the
// goroutine reaches the seam. This does, driving a manual ticker channel.
//
// It delivers TWO ticks on purpose: a loop body that ran runOnce and then
// returned would satisfy a single-tick assertion, and "it keeps ticking" is the
// load-bearing property of a ticker (this is the re-homed
// TestHeartbeat_RefreshBlurb_FiresEveryTick's other half).
func TestBlurbTicker_Loop_RefreshesOnEveryTick(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	lastAct := now.Add(-3 * time.Minute)

	lister := &fakeBlurbLister{probes: []blurbProbe{
		&fakeBlurbProbe{name: "alice", lastAct: lastAct},
	}}
	rec := &refreshRecorder{}
	ticks := make(chan time.Time, 2)
	var gotInterval atomic.Int64
	bt := newBlurbTicker(blurbTickerDeps{
		Registry:     lister,
		RefreshBlurb: rec.fn(),
		NewTicker: func(d time.Duration) (<-chan time.Time, func()) {
			gotInterval.Store(int64(d))
			return ticks, func() {}
		},
	})
	bt.Start()
	defer bt.Stop()

	ticks <- now
	calls := waitForCalls(t, rec, 1)
	if calls[0].name != "alice" || !calls[0].lastAct.Equal(lastAct) {
		t.Fatalf("first call = %+v, want (alice, %v)", calls[0], lastAct)
	}

	ticks <- now.Add(blurbRefreshInterval)
	calls = waitForCalls(t, rec, 2)
	if calls[1].name != "alice" || !calls[1].lastAct.Equal(lastAct) {
		t.Errorf("second call = %+v, want (alice, %v)", calls[1], lastAct)
	}

	// Exact total: one refresh per tick and not one more. Pins out an eager
	// refresh at Start(), which both waits above would otherwise absorb.
	if n := len(rec.Calls()); n != 2 {
		t.Errorf("RefreshBlurb calls = %d after exactly 2 ticks, want 2 (no refresh outside a tick)", n)
	}

	if got := time.Duration(gotInterval.Load()); got != blurbRefreshInterval {
		t.Errorf("ticker interval = %v, want %v", got, blurbRefreshInterval)
	}
}

// TestBlurbRefreshInterval_Is30Minutes is a standalone constant pin: QUM-1071
// requires the extracted ticker keep the heartbeat's 30-minute cadence, and that
// regression must be reported independently of whether the loop works.
func TestBlurbRefreshInterval_Is30Minutes(t *testing.T) {
	t.Parallel()
	if blurbRefreshInterval != 30*time.Minute {
		t.Errorf("blurbRefreshInterval = %v, want 30m (cadence unchanged from the heartbeat)", blurbRefreshInterval)
	}
}

// --- T2: the AC-1 pin — NewReal starts it, Shutdown stops it -----------------

// TestNewReal_StartsAndStopsBlurbTicker is QUM-1071's AC-1 pin: it goes red if
// the ticker is constructed but never Start()ed in NewReal.
//
// Started() is set INSIDE the loop goroutine (after the ticker is installed),
// not in Start(), so this cannot be satisfied by a Start() that launches
// nothing. Its honest limit: it proves the goroutine runs, not that its deps are
// wired — TestNewReal_BlurbTickerReachesMaybeRefreshBlurb covers the wiring, and
// TestBlurbTicker_Loop_RefreshesOnEveryTick covers the loop reaching the seam.
//
// NewReal installs the production 30-minute ticker, so this must NEVER wait for a
// tick — the running flag is the only observable inside a test's lifetime.
func TestNewReal_StartsAndStopsBlurbTicker(t *testing.T) {
	t.Parallel()
	r, _ := newFakeReal(t)
	if r.blurbTicker == nil {
		t.Fatal("Real.blurbTicker = nil after NewReal; want a started blurb ticker")
	}

	deadline := time.Now().Add(2 * time.Second)
	running := false
	for time.Now().Before(deadline) {
		if r.blurbTicker.Started() {
			running = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !running {
		t.Fatal("blurb ticker goroutine did not start within 2s of NewReal; NewReal must call blurbTicker.Start()")
	}

	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !r.blurbTicker.Stopped() {
		t.Fatal("blurb ticker goroutine still running after Shutdown; Shutdown must call blurbTicker.Stop()")
	}
}

// TestNewReal_BlurbTickerReachesMaybeRefreshBlurb is the other half of the AC-1
// pin: TestNewReal_StartsAndStopsBlurbTicker proves a goroutine runs, this proves
// NewReal wired that ticker's Registry and RefreshBlurb seams to the real
// registry and to maybeRefreshBlurb. Without it, NewReal handing the ticker a nil
// registry or a nil seam leaves the whole suite green while blurbs go
// permanently stale with no error anywhere — QUM-1071's stated failure mode.
//
// It drives the production ticker's runOnce directly rather than waiting for a
// tick: NewReal installs the real 30-minute ticker.
func TestNewReal_BlurbTickerReachesMaybeRefreshBlurb(t *testing.T) {
	r, tmp := newFakeReal(t)
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	// An agent with no blurb yet: DecideTrigger returns TriggerInitial regardless
	// of activity time, so the dispatch is not hostage to a live runtime handle.
	saveTestAgent(t, tmp, &state.AgentState{
		Name: "kit", Type: "engineer", Family: "engineering", Parent: "weave",
		Status: state.StatusRunning,
	})
	r.runtimeRegistry.Ensure(AgentRuntimeConfig{Agent: &state.AgentState{Name: "kit"}})

	var mu sync.Mutex
	var dispatched []string
	r.dispatchBlurb = func(name string, _ blurb.TriggerKind) {
		mu.Lock()
		defer mu.Unlock()
		dispatched = append(dispatched, name)
	}

	r.blurbTicker.runOnce()

	mu.Lock()
	defer mu.Unlock()
	if len(dispatched) != 1 || dispatched[0] != "kit" {
		t.Fatalf("dispatched = %v, want [kit]; NewReal must wire the blurb ticker's Registry to the runtime registry and RefreshBlurb to maybeRefreshBlurb", dispatched)
	}
}

// --- T3: lifecycle -----------------------------------------------------------

// TestBlurbTicker_StopAfterStart asserts Stop() blocks until the goroutine's
// teardown ran — so Stopped() is true the instant Stop() returns, with no poll.
func TestBlurbTicker_StopAfterStart(t *testing.T) {
	t.Parallel()
	var tickerReleased atomic.Bool
	bt := newBlurbTicker(blurbTickerDeps{
		Registry: &fakeBlurbLister{},
		NewTicker: func(time.Duration) (<-chan time.Time, func()) {
			return make(chan time.Time), func() { tickerReleased.Store(true) }
		},
	})
	bt.Start()
	waitForBlurbTickerStarted(t, bt)

	done := make(chan struct{})
	go func() { bt.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() after Start() did not return within 2s")
	}
	if !bt.Stopped() {
		t.Fatal("Stopped() = false immediately after Stop() returned; Stop must block until the goroutine exits")
	}
	if !tickerReleased.Load() {
		t.Error("ticker stop func was not called; the loop must release its ticker on exit")
	}
}

// TestBlurbTicker_DoubleStart pins the startOnce guard: a second Start() must not
// launch a second goroutine. Without the guard the two teardown defers race to
// close(doneCh) and the process panics on the second close.
func TestBlurbTicker_DoubleStart(t *testing.T) {
	t.Parallel()
	var tickers atomic.Int32
	bt := newBlurbTicker(blurbTickerDeps{
		Registry: &fakeBlurbLister{},
		NewTicker: func(time.Duration) (<-chan time.Time, func()) {
			tickers.Add(1)
			return make(chan time.Time), func() {}
		},
	})
	bt.Start()
	waitForBlurbTickerStarted(t, bt)
	bt.Start()

	done := make(chan struct{})
	go func() { bt.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() after a double Start() did not return within 2s")
	}
	if got := tickers.Load(); got != 1 {
		t.Errorf("NewTicker called %d times across two Start() calls, want 1 (second Start must be a no-op)", got)
	}
}

// TestBlurbTicker_StartAfterStop pins the Start-vs-Stop ordering guard. A Stop()
// that ran before any Start() must leave the ticker permanently stopped: if
// Start() then launched a loop, that loop's teardown would close an already-torn
// -down ticker's channels, and nothing would ever stop it again.
func TestBlurbTicker_StartAfterStop(t *testing.T) {
	t.Parallel()
	var tickers atomic.Int32
	bt := newBlurbTicker(blurbTickerDeps{
		Registry: &fakeBlurbLister{},
		NewTicker: func(time.Duration) (<-chan time.Time, func()) {
			tickers.Add(1)
			return make(chan time.Time), func() {}
		},
	})

	done := make(chan struct{})
	go func() { bt.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() without Start() blocked for 2s")
	}

	bt.Start()
	// Give a wrongly-launched goroutine a chance to install its ticker.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if tickers.Load() != 0 || bt.Started() {
			t.Fatalf("Start() after Stop() launched a loop (NewTicker calls=%d, started=%v); want no-op",
				tickers.Load(), bt.Started())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Total over the window, not merely sampled: re-check after the loop so a
	// launch in the final sleep is still caught.
	if n := tickers.Load(); n != 0 || bt.Started() {
		t.Fatalf("Start() after Stop() launched a loop (NewTicker calls=%d, started=%v); want no-op", n, bt.Started())
	}

	// And a second Stop() must still return rather than block on a goroutine
	// that does not exist.
	done2 := make(chan struct{})
	go func() { bt.Stop(); close(done2) }()
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() after Start()-that-was-a-no-op blocked for 2s")
	}
}

// TestBlurbTicker_StopWithoutStart pins QUM-1071 req 4a: the heartbeat's Stop()
// blocks unconditionally on <-doneCh and therefore deadlocks when it was never
// started. Stop() is called on a helper goroutine deliberately — a deadlock on
// the test goroutine is a 10-minute hang and an unreadable panic dump instead of
// a clean failure.
func TestBlurbTicker_StopWithoutStart(t *testing.T) {
	t.Parallel()
	bt := newBlurbTicker(blurbTickerDeps{Registry: &fakeBlurbLister{}})

	done := make(chan struct{})
	go func() { bt.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() without Start() blocked for 2s (QUM-1071 req 4a: must return, not deadlock)")
	}
	// Semantics pin: Stopped() means "torn down", so it must be true after any
	// completed Stop() — including one that had no goroutine to wait for.
	// TestNewReal_StartsAndStopsBlurbTicker relies on this reading.
	if !bt.Stopped() {
		t.Error("Stopped() = false after Stop() on a never-started ticker; want true")
	}
}

// TestBlurbTicker_ConcurrentStop pins QUM-1071 req 4b: concurrent Stop() must
// not double-close stopCh (which panics, with or without -race) or deadlock.
func TestBlurbTicker_ConcurrentStop(t *testing.T) {
	t.Parallel()
	bt := newBlurbTicker(blurbTickerDeps{
		Registry: &fakeBlurbLister{},
		NewTicker: func(time.Duration) (<-chan time.Time, func()) {
			return make(chan time.Time), func() {}
		},
	})
	bt.Start()
	waitForBlurbTickerStarted(t, bt)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); bt.Stop() }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent Stop() calls did not all return within 2s")
	}
	if !bt.Stopped() {
		t.Fatal("Stopped() = false after concurrent Stop()")
	}
}

func waitForBlurbTickerStarted(t *testing.T, bt *blurbTicker) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if bt.Started() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("blurb ticker goroutine did not start within 2s")
}

// --- T4: the registry adapter ------------------------------------------------

// TestBlurbRegistryAdapter_ListsRegistryAgents pins the production seam between
// the RuntimeRegistry and the ticker's narrow lister interface.
func TestBlurbRegistryAdapter_ListsRegistryAgents(t *testing.T) {
	t.Parallel()
	if got := (*blurbRegistryAdapter)(nil).List(); got != nil {
		t.Errorf("nil adapter List() = %v, want nil", got)
	}
	if got := (&blurbRegistryAdapter{}).List(); got != nil {
		t.Errorf("nil-registry adapter List() = %v, want nil", got)
	}

	reg := NewRuntimeRegistry()
	for _, n := range []string{"alice", "bob"} {
		reg.Ensure(AgentRuntimeConfig{Agent: &state.AgentState{Name: n}})
	}
	names := map[string]bool{}
	for _, p := range (&blurbRegistryAdapter{reg: reg}).List() {
		names[p.Name()] = true
	}
	if len(names) != 2 || !names["alice"] || !names["bob"] {
		t.Errorf("adapter List() names = %v, want alice+bob", names)
	}
}
