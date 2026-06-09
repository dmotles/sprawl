// QUM-730: supervisor heartbeat / liveness-check tests.
//
// These tests are written BEFORE the heartbeat implementation lands. They
// pin the contract described in the QUM-730 oracle plan:
//
//   - Tier-1 (error/rate-limit/stalled-system) nudges fire on the first tick
//     after IdleThreshold elapses with no further activity.
//   - Tier-2 (clean idle) nudges fire only after Tier2ConsecutiveTicks
//     consecutive eligible ticks (default 4).
//   - Negative gates: InTurn==true, Liveness != Running, or idle < threshold
//     suppress all nudges.
//   - LastActivityAt advancing past lastNudgeAt resets per-agent counters.
//   - EscalationThreshold consecutive nudges trigger exactly one
//     sendMessage(parent, ..., interrupt=false) (non-root), then go silent
//     until activity is observed.
//   - Root weave (Parent=="") escalates via toastFn / WARN log, never via
//     sendMessage.
//   - Config-disabled means the goroutine never fires nudges.
//   - ResolveLivenessConfig enforces a 5-minute minimum HeartbeatInterval
//     and applies defaults when the YAML block is absent.
//   - NewReal lifecycle: the heartbeat goroutine starts in NewReal and is
//     reliably shut down on Real.Shutdown.
//
// Implementer notes (the heartbeat type does not exist yet — the file
// expected by these tests is internal/supervisor/heartbeat.go):
//
//   - `LivenessConfig` is the typed config block (also expected by
//     `config.Config`); fields the tests touch are documented inline below.
//   - The heartbeat consumes a narrow `runtimeProbe` interface so we can
//     fake AgentRuntime in isolation. The expected interface:
//
//	   type runtimeProbe interface {
//	       Name() string
//	       InTurn() bool
//	       LastActivityAt() time.Time
//	       Snapshot() RuntimeSnapshot
//	   }
//
//     and a small `runtimeLister` seam:
//
//	   type runtimeLister interface { List() []runtimeProbe }
//
//   - Tests construct a `heartbeat` with all dependency seams injected
//     (registry, sendMessage, loadAgent, readActivityTail, sendLivenessCheck,
//     wakeForDelivery, nowFn, newTicker, toastFn, logger).
//   - Tests drive time with a manual ticker channel and a stepped clock —
//     no real wall-clock sleeps.

package supervisor

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// --- test fakes --------------------------------------------------------------

// fakeProbe satisfies the (yet-to-be-defined) runtimeProbe interface that
// the heartbeat consumes. Concrete *AgentRuntime is hard to fake in
// isolation (mutex, subscribers, atomic fields) — defining a narrow probe
// keeps tests focused on the heartbeat policy.
type fakeProbe struct {
	mu       sync.Mutex
	name     string
	inTurn   bool
	lastAct  time.Time
	snapshot RuntimeSnapshot
}

func (p *fakeProbe) Name() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.name
}

func (p *fakeProbe) InTurn() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inTurn
}

func (p *fakeProbe) LastActivityAt() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastAct
}

func (p *fakeProbe) Snapshot() RuntimeSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshot
}

func (p *fakeProbe) setLastActivity(t time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastAct = t
}

func (p *fakeProbe) setInTurn(b bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inTurn = b
}

func (p *fakeProbe) setLiveness(l liveness.AgentLiveness) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot.Liveness = l
}

type fakeLister struct {
	mu     sync.Mutex
	probes []*fakeProbe
}

func (l *fakeLister) List() []runtimeProbe {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]runtimeProbe, 0, len(l.probes))
	for _, p := range l.probes {
		out = append(out, p)
	}
	return out
}

// sendMessageRecorder records every sendMessage call.
type sendMessageRecorder struct {
	mu    sync.Mutex
	calls []sendMessageCall
}

type sendMessageCall struct {
	To        string
	Body      string
	Interrupt bool
}

func (r *sendMessageRecorder) fn() func(ctx context.Context, to, body string, interrupt bool) (*SendMessageResult, error) {
	return func(_ context.Context, to, body string, interrupt bool) (*SendMessageResult, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, sendMessageCall{To: to, Body: body, Interrupt: interrupt})
		return &SendMessageResult{}, nil
	}
}

func (r *sendMessageRecorder) Calls() []sendMessageCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sendMessageCall, len(r.calls))
	copy(out, r.calls)
	return out
}

// liveCheckRecorder records every sendLivenessCheck call.
type liveCheckRecorder struct {
	mu    sync.Mutex
	calls []string // recipient names, in order
}

func (r *liveCheckRecorder) fn() func(sprawlRoot, to string) (string, error) {
	return func(_ string, to string) (string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, to)
		return "id-" + to, nil
	}
}

func (r *liveCheckRecorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// toastRecorder captures toast emissions for root-weave escalation.
type toastRecorder struct {
	mu    sync.Mutex
	count int
}

func (t *toastRecorder) fn() func(format string, args ...any) {
	return func(_ string, _ ...any) {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.count++
	}
}

func (t *toastRecorder) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

// manualTicker drives heartbeat ticks deterministically.
type manualTicker struct {
	ch chan time.Time
}

func newManualTicker() *manualTicker {
	return &manualTicker{ch: make(chan time.Time, 16)}
}

//nolint:unused // Reserved for future ticker-driven heartbeat tests; runOneTick currently exercises the path directly. (QUM-730)
func (m *manualTicker) tick(now time.Time) {
	m.ch <- now
}

func (m *manualTicker) newTicker(_ time.Duration) (<-chan time.Time, func()) {
	stop := func() {}
	return m.ch, stop
}

// clock is a manually-advanced clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// readActivityTailFake holds a per-agent canned tail.
type readActivityTailFake struct {
	mu     sync.Mutex
	byName map[string][]agentloop.ActivityEntry
}

func (f *readActivityTailFake) set(name string, entries []agentloop.ActivityEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byName == nil {
		f.byName = make(map[string][]agentloop.ActivityEntry)
	}
	f.byName[name] = entries
}

func (f *readActivityTailFake) fn() func(string, int) ([]agentloop.ActivityEntry, error) {
	return func(path string, _ int) ([]agentloop.ActivityEntry, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		// Path is opaque here; we key on the trailing agent dir segment
		// the activityPath fn produces. The implementer must use
		// activityPath(sprawlRoot, name) to derive the path, and our
		// activityPathFake echoes name back via a sentinel.
		for name, entries := range f.byName {
			if strings.Contains(path, name) {
				return entries, nil
			}
		}
		return nil, nil
	}
}

// --- helpers -----------------------------------------------------------------

func defaultLivenessConfigForTests() LivenessConfig {
	return LivenessConfig{
		Enabled:               true,
		HeartbeatInterval:     5 * time.Minute,
		IdleThreshold:         10 * time.Minute,
		Tier2ConsecutiveTicks: 4,
		EscalationThreshold:   3,
	}
}

func newHeartbeatForTest(
	t *testing.T,
	cfg LivenessConfig,
	lister runtimeLister,
	clk *clock,
	ticker *manualTicker,
	send func(ctx context.Context, to, body string, interrupt bool) (*SendMessageResult, error),
	sendLive func(sprawlRoot, to string) (string, error),
	loadAgent func(sprawlRoot, name string) (*state.AgentState, error),
	readTail func(string, int) ([]agentloop.ActivityEntry, error),
	toast func(format string, args ...any),
) *heartbeat {
	t.Helper()
	hb := newHeartbeat(heartbeatDeps{
		Cfg:               cfg,
		SprawlRoot:        "/tmp/fake-root",
		Registry:          lister,
		SendMessage:       send,
		SendLivenessCheck: sendLive,
		LoadAgent:         loadAgent,
		ReadActivityTail:  readTail,
		ActivityPath: func(_, name string) string {
			// Sentinel path the readTail fake matches on.
			return "/activity/" + name + "/activity.ndjson"
		},
		WakeForDelivery: func(*AgentRuntime) error { return nil },
		NowFn:           clk.Now,
		NewTicker:       ticker.newTicker,
		ToastFn:         toast,
		Logger:          nil,
	})
	return hb
}

// runOneTick drives a single heartbeat tick synchronously by calling
// the implementation-provided test seam `(*heartbeat).runOnce(now)`.
// runOnce must process all registered agents and update their counters
// before returning. The implementer is required to expose this seam.
func runOneTick(t *testing.T, hb *heartbeat, now time.Time) {
	t.Helper()
	hb.runOnce(context.Background(), now)
}

// boolPtr is a tiny helper for constructing *bool YAML fields.
func boolPtr(b bool) *bool { return &b }

// --- Tier-1 detection --------------------------------------------------------

func TestHeartbeat_Tier1_ResultErrorTriggersNudgeImmediately(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: now}
	probe := &fakeProbe{name: "alice"}
	probe.setLiveness(liveness.Running)
	// Idle for 12 min — past 10 min threshold.
	probe.setLastActivity(now.Add(-12 * time.Minute))
	probe.snapshot.Name = "alice"
	probe.snapshot.Parent = "weave"

	lister := &fakeLister{probes: []*fakeProbe{probe}}
	tail := &readActivityTailFake{}
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: now.Add(-12 * time.Minute), Kind: "result", Summary: "error stop=foo turns=1"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

	runOneTick(t, hb, clk.Now())

	if got := live.Count(); got != 1 {
		t.Fatalf("liveness-check nudges = %d, want 1 (tier-1 error result)", got)
	}
}

func TestHeartbeat_Tier1_RateLimitTriggersNudgeImmediately(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: now}
	probe := &fakeProbe{name: "alice"}
	probe.setLiveness(liveness.Running)
	probe.setLastActivity(now.Add(-15 * time.Minute))
	probe.snapshot.Name = "alice"
	probe.snapshot.Parent = "weave"

	lister := &fakeLister{probes: []*fakeProbe{probe}}
	tail := &readActivityTailFake{}
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: now.Add(-15 * time.Minute), Kind: "rate_limit", Summary: "status=throttled type=requests"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

	runOneTick(t, hb, clk.Now())

	if got := live.Count(); got != 1 {
		t.Fatalf("nudges = %d, want 1 (rate_limit is tier-1)", got)
	}
}

func TestHeartbeat_Tier1_SystemInitWithNoAssistantFollowupTriggersImmediately(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: now}
	probe := &fakeProbe{name: "alice"}
	probe.setLiveness(liveness.Running)
	probe.setLastActivity(now.Add(-11 * time.Minute))
	probe.snapshot.Name = "alice"
	probe.snapshot.Parent = "weave"

	lister := &fakeLister{probes: []*fakeProbe{probe}}
	tail := &readActivityTailFake{}
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: now.Add(-11 * time.Minute), Kind: "system", Summary: "init"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

	runOneTick(t, hb, clk.Now())

	if got := live.Count(); got != 1 {
		t.Fatalf("nudges = %d, want 1 (stalled init system entry)", got)
	}
}

func TestHeartbeat_Tier1_SystemInitWithAssistantFollowupDoesNotNudge(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: now}
	probe := &fakeProbe{name: "alice"}
	probe.setLiveness(liveness.Running)
	probe.setLastActivity(now.Add(-11 * time.Minute))
	probe.snapshot.Name = "alice"
	probe.snapshot.Parent = "weave"

	lister := &fakeLister{probes: []*fakeProbe{probe}}
	tail := &readActivityTailFake{}
	// init followed by assistant_text → not stalled, not tier-1.
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: now.Add(-13 * time.Minute), Kind: "system", Summary: "init"},
		{TS: now.Add(-11 * time.Minute), Kind: "assistant_text", Summary: "hello"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

	runOneTick(t, hb, clk.Now())

	if got := live.Count(); got != 0 {
		t.Fatalf("nudges = %d, want 0 (init followed by assistant_text is not stalled)", got)
	}
}

// --- Tier-2 ramp -------------------------------------------------------------

func TestHeartbeat_Tier2_CleanIdleNudgesOnNthConsecutiveTick(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests() // Tier2ConsecutiveTicks=4
	start := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: start}
	probe := &fakeProbe{name: "alice"}
	probe.setLiveness(liveness.Running)
	// Idle since long before start.
	probe.setLastActivity(start.Add(-30 * time.Minute))
	probe.snapshot.Name = "alice"
	probe.snapshot.Parent = "weave"

	lister := &fakeLister{probes: []*fakeProbe{probe}}
	tail := &readActivityTailFake{}
	// Last entry is a clean result — tier-2 candidate.
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: start.Add(-30 * time.Minute), Kind: "result", Summary: "success stop=end_turn turns=2"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

	for i := 1; i <= 3; i++ {
		runOneTick(t, hb, clk.Now())
		clk.Set(clk.Now().Add(cfg.HeartbeatInterval))
		if got := live.Count(); got != 0 {
			t.Fatalf("after tick %d: nudges = %d, want 0 (tier-2 ramp not complete)", i, got)
		}
	}
	// 4th tick should fire.
	runOneTick(t, hb, clk.Now())
	if got := live.Count(); got != 1 {
		t.Fatalf("after 4th tick: nudges = %d, want 1 (tier-2 fires at Tier2ConsecutiveTicks)", got)
	}
}

// --- negative gates ----------------------------------------------------------

func TestHeartbeat_InTurn_SuppressesAllNudges(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: now}
	probe := &fakeProbe{name: "alice"}
	probe.setLiveness(liveness.Running)
	probe.setLastActivity(now.Add(-1 * time.Hour))
	probe.setInTurn(true)
	probe.snapshot.Name = "alice"
	probe.snapshot.Parent = "weave"

	lister := &fakeLister{probes: []*fakeProbe{probe}}
	tail := &readActivityTailFake{}
	// Even with a tier-1 trigger tail, InTurn must suppress.
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: now.Add(-1 * time.Hour), Kind: "rate_limit", Summary: "status=throttled"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

	for i := 0; i < 10; i++ {
		runOneTick(t, hb, clk.Now())
		clk.Set(clk.Now().Add(cfg.HeartbeatInterval))
	}
	if got := live.Count(); got != 0 {
		t.Fatalf("nudges = %d, want 0 (InTurn suppresses regardless of tail)", got)
	}
}

func TestHeartbeat_NonRunningLiveness_SuppressesAllNudges(t *testing.T) {
	t.Parallel()
	cases := []liveness.AgentLiveness{
		liveness.Unstarted, liveness.Starting, liveness.Faulted,
		liveness.Stopping, liveness.Stopped, liveness.Suspended,
		liveness.Killed, liveness.Retiring, liveness.Retired,
	}
	for _, l := range cases {
		l := l
		t.Run(l.String(), func(t *testing.T) {
			t.Parallel()
			cfg := defaultLivenessConfigForTests()
			now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
			clk := &clock{now: now}
			probe := &fakeProbe{name: "alice"}
			probe.setLiveness(l)
			probe.setLastActivity(now.Add(-1 * time.Hour))
			probe.snapshot.Name = "alice"
			probe.snapshot.Parent = "weave"

			lister := &fakeLister{probes: []*fakeProbe{probe}}
			tail := &readActivityTailFake{}
			tail.set("alice", []agentloop.ActivityEntry{
				{TS: now.Add(-1 * time.Hour), Kind: "rate_limit", Summary: "status=throttled"},
			})
			live := &liveCheckRecorder{}
			send := &sendMessageRecorder{}
			hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

			runOneTick(t, hb, clk.Now())
			if got := live.Count(); got != 0 {
				t.Fatalf("nudges = %d, want 0 for liveness=%s", got, l)
			}
		})
	}
}

func TestHeartbeat_IdleBelowThreshold_DoesNotNudge(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: now}
	probe := &fakeProbe{name: "alice"}
	probe.setLiveness(liveness.Running)
	// Idle 1m — well below 10m threshold.
	probe.setLastActivity(now.Add(-1 * time.Minute))
	probe.snapshot.Name = "alice"
	probe.snapshot.Parent = "weave"

	lister := &fakeLister{probes: []*fakeProbe{probe}}
	tail := &readActivityTailFake{}
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: now.Add(-1 * time.Minute), Kind: "rate_limit", Summary: "status=throttled"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

	runOneTick(t, hb, clk.Now())
	if got := live.Count(); got != 0 {
		t.Fatalf("nudges = %d, want 0 (idle below IdleThreshold)", got)
	}
}

// --- counter reset on observed activity --------------------------------------

func TestHeartbeat_ActivityAfterNudge_ResetsCounters(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: now}
	probe := &fakeProbe{name: "alice"}
	probe.setLiveness(liveness.Running)
	probe.setLastActivity(now.Add(-30 * time.Minute))
	probe.snapshot.Name = "alice"
	probe.snapshot.Parent = "weave"

	lister := &fakeLister{probes: []*fakeProbe{probe}}
	tail := &readActivityTailFake{}
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: now.Add(-30 * time.Minute), Kind: "rate_limit", Summary: "status=throttled"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

	// Tick 1 — tier-1 nudge fires.
	runOneTick(t, hb, clk.Now())
	if got := live.Count(); got != 1 {
		t.Fatalf("after tick 1: nudges = %d, want 1", got)
	}

	// Simulate activity AFTER the nudge.
	probe.setLastActivity(clk.Now().Add(1 * time.Second))
	// Replace tail with a fresh clean result — also resets stickiness.
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: clk.Now().Add(1 * time.Second), Kind: "assistant_text", Summary: "I'm back"},
	})
	// Advance idle threshold again, but with the new lastAct, idle < threshold
	// (only 5 minutes elapsed). The counter for tier-2 / escalation must reset.
	clk.Set(clk.Now().Add(5 * time.Minute))

	runOneTick(t, hb, clk.Now())
	if got := live.Count(); got != 1 {
		t.Fatalf("after tick 2: nudges = %d, want 1 (no new nudge — activity reset counters)", got)
	}
}

// --- first-observation regression (QUM-730 review) ---------------------------

// TestHeartbeat_Tier1_ThreeConsecutiveTicksAtSameLastAct_Escalates pins the
// regression flagged in code review: on a first-seen agent, the
// counter-reset branch must NOT wipe consecutiveNudges we just
// incremented. With a tier-1 trigger held constant across three ticks at
// the same lastAct, escalation must fire on the 3rd tick.
func TestHeartbeat_Tier1_ThreeConsecutiveTicksAtSameLastAct_Escalates(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests() // EscalationThreshold=3
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: now}
	probe := &fakeProbe{name: "alice"}
	probe.setLiveness(liveness.Running)
	probe.setLastActivity(now.Add(-30 * time.Minute))
	probe.snapshot.Name = "alice"
	probe.snapshot.Parent = "weave"

	lister := &fakeLister{probes: []*fakeProbe{probe}}
	tail := &readActivityTailFake{}
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: now.Add(-30 * time.Minute), Kind: "rate_limit", Summary: "status=throttled"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

	// Three ticks at the same lastAct (no activity progress between them).
	for i := 1; i <= 3; i++ {
		runOneTick(t, hb, clk.Now())
		clk.Set(clk.Now().Add(cfg.HeartbeatInterval))
	}

	if got := live.Count(); got != 3 {
		t.Fatalf("nudges after 3 ticks = %d, want 3 (tier-1 fires every tick until escalation)", got)
	}
	if got := len(send.Calls()); got != 1 {
		t.Fatalf("escalation sendMessage after 3 ticks = %d, want 1", got)
	}
}

// --- escalation --------------------------------------------------------------

func TestHeartbeat_Escalation_ThreeConsecutiveNudgesSendsToParentOnce(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests() // EscalationThreshold=3
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: now}
	probe := &fakeProbe{name: "alice"}
	probe.setLiveness(liveness.Running)
	probe.setLastActivity(now.Add(-30 * time.Minute))
	probe.snapshot.Name = "alice"
	probe.snapshot.Parent = "weave"

	lister := &fakeLister{probes: []*fakeProbe{probe}}
	tail := &readActivityTailFake{}
	// Tier-1 trigger on every tick (no activity observed).
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: now.Add(-30 * time.Minute), Kind: "rate_limit", Summary: "status=throttled type=requests"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

	for i := 1; i <= 5; i++ {
		runOneTick(t, hb, clk.Now())
		clk.Set(clk.Now().Add(cfg.HeartbeatInterval))
	}

	// Exactly one escalation sendMessage to the parent expected; nudges keep
	// going until escalation, then silent until activity.
	calls := send.Calls()
	if len(calls) != 1 {
		t.Fatalf("sendMessage call count = %d, want 1; calls=%+v", len(calls), calls)
	}
	c := calls[0]
	if c.To != "weave" {
		t.Errorf("escalation target = %q, want %q (parent)", c.To, "weave")
	}
	if c.Interrupt {
		t.Errorf("escalation message must be non-interrupt; got interrupt=true")
	}
	if !strings.Contains(strings.ToLower(c.Body), "appears stuck") {
		t.Errorf("escalation body missing 'appears stuck' substring: %q", c.Body)
	}
	if !strings.Contains(c.Body, "alice") {
		t.Errorf("escalation body missing agent name 'alice': %q", c.Body)
	}
}

func TestHeartbeat_Escalation_SilentAfterEscalationUntilActivity(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: now}
	probe := &fakeProbe{name: "alice"}
	probe.setLiveness(liveness.Running)
	probe.setLastActivity(now.Add(-30 * time.Minute))
	probe.snapshot.Name = "alice"
	probe.snapshot.Parent = "weave"

	lister := &fakeLister{probes: []*fakeProbe{probe}}
	tail := &readActivityTailFake{}
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: now.Add(-30 * time.Minute), Kind: "rate_limit", Summary: "status=throttled"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

	// Drive enough ticks to escalate.
	for i := 0; i < cfg.EscalationThreshold+5; i++ {
		runOneTick(t, hb, clk.Now())
		clk.Set(clk.Now().Add(cfg.HeartbeatInterval))
	}
	if got := len(send.Calls()); got != 1 {
		t.Fatalf("escalation sendMessage = %d, want exactly 1 (silent after first escalation)", got)
	}
}

// --- root weave escalation path ---------------------------------------------

func TestHeartbeat_Escalation_RootWeaveUsesToastNotSendMessage(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: now}
	probe := &fakeProbe{name: "weave"}
	probe.setLiveness(liveness.Running)
	probe.setLastActivity(now.Add(-30 * time.Minute))
	probe.snapshot.Name = "weave"
	probe.snapshot.Parent = "" // root weave

	lister := &fakeLister{probes: []*fakeProbe{probe}}
	tail := &readActivityTailFake{}
	tail.set("weave", []agentloop.ActivityEntry{
		{TS: now.Add(-30 * time.Minute), Kind: "rate_limit", Summary: "status=throttled"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	toast := &toastRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), toast.fn())

	for i := 0; i < cfg.EscalationThreshold+2; i++ {
		runOneTick(t, hb, clk.Now())
		clk.Set(clk.Now().Add(cfg.HeartbeatInterval))
	}

	if got := len(send.Calls()); got != 0 {
		t.Fatalf("root weave escalation must not call sendMessage; got %d calls", got)
	}
	if got := toast.Count(); got < 1 {
		t.Fatalf("root weave escalation must invoke toastFn at least once; got %d", got)
	}
}

// --- config disabled ---------------------------------------------------------

func TestHeartbeat_Disabled_NeverNudges(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests()
	cfg.Enabled = false
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: now}
	probe := &fakeProbe{name: "alice"}
	probe.setLiveness(liveness.Running)
	probe.setLastActivity(now.Add(-1 * time.Hour))
	probe.snapshot.Name = "alice"
	probe.snapshot.Parent = "weave"

	lister := &fakeLister{probes: []*fakeProbe{probe}}
	tail := &readActivityTailFake{}
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: now.Add(-1 * time.Hour), Kind: "rate_limit", Summary: "status=throttled"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

	for i := 0; i < 10; i++ {
		runOneTick(t, hb, clk.Now())
		clk.Set(clk.Now().Add(cfg.HeartbeatInterval))
	}
	if got := live.Count(); got != 0 {
		t.Fatalf("disabled heartbeat must not nudge; got %d", got)
	}
	if got := len(send.Calls()); got != 0 {
		t.Fatalf("disabled heartbeat must not escalate; got %d", got)
	}
}

// --- multiple agents ---------------------------------------------------------

func TestHeartbeat_MultipleAgents_HealthyUntouchedSickNudged(t *testing.T) {
	t.Parallel()
	cfg := defaultLivenessConfigForTests()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	clk := &clock{now: now}

	sick := &fakeProbe{name: "alice"}
	sick.setLiveness(liveness.Running)
	sick.setLastActivity(now.Add(-30 * time.Minute))
	sick.snapshot.Name = "alice"
	sick.snapshot.Parent = "weave"

	healthy := &fakeProbe{name: "bob"}
	healthy.setLiveness(liveness.Running)
	healthy.setLastActivity(now.Add(-30 * time.Second))
	healthy.snapshot.Name = "bob"
	healthy.snapshot.Parent = "weave"

	lister := &fakeLister{probes: []*fakeProbe{sick, healthy}}
	tail := &readActivityTailFake{}
	tail.set("alice", []agentloop.ActivityEntry{
		{TS: now.Add(-30 * time.Minute), Kind: "rate_limit", Summary: "status=throttled"},
	})
	tail.set("bob", []agentloop.ActivityEntry{
		{TS: now.Add(-30 * time.Second), Kind: "assistant_text", Summary: "working"},
	})
	live := &liveCheckRecorder{}
	send := &sendMessageRecorder{}
	hb := newHeartbeatForTest(t, cfg, lister, clk, newManualTicker(), send.fn(), live.fn(), nil, tail.fn(), nil)

	runOneTick(t, hb, clk.Now())

	live.mu.Lock()
	defer live.mu.Unlock()
	if len(live.calls) != 1 || live.calls[0] != "alice" {
		t.Fatalf("expected exactly 1 nudge to alice; got %#v", live.calls)
	}
}

// --- ResolveLivenessConfig ---------------------------------------------------

func TestResolveLivenessConfig_AppliesDefaultsWhenAbsent(t *testing.T) {
	t.Parallel()
	cfg := ResolveLivenessConfig(nil)
	if !cfg.Enabled {
		t.Errorf("default Enabled = false, want true")
	}
	if cfg.HeartbeatInterval < 5*time.Minute {
		t.Errorf("default HeartbeatInterval = %v, want >= 5m", cfg.HeartbeatInterval)
	}
	if cfg.IdleThreshold == 0 {
		t.Errorf("default IdleThreshold must be non-zero")
	}
	if cfg.Tier2ConsecutiveTicks != 4 {
		t.Errorf("default Tier2ConsecutiveTicks = %d, want 4", cfg.Tier2ConsecutiveTicks)
	}
	if cfg.EscalationThreshold != 3 {
		t.Errorf("default EscalationThreshold = %d, want 3", cfg.EscalationThreshold)
	}
}

func TestResolveLivenessConfig_EnforcesMinimumHeartbeatInterval(t *testing.T) {
	t.Parallel()
	raw := &LivenessConfigRaw{
		Enabled:           boolPtr(true),
		HeartbeatInterval: "30s", // way below the 5m floor
		IdleThreshold:     "10m",
	}
	cfg := ResolveLivenessConfig(raw)
	if cfg.HeartbeatInterval < 5*time.Minute {
		t.Errorf("HeartbeatInterval = %v, want clamped to >= 5m", cfg.HeartbeatInterval)
	}
}

// --- NewReal lifecycle integration ------------------------------------------

func TestNewReal_StartsAndShutsDownHeartbeatGoroutine(t *testing.T) {
	t.Parallel()
	r, _ := newFakeReal(t)
	// The heartbeat goroutine handle should be present on Real after NewReal.
	if r.heartbeat == nil {
		t.Fatal("Real.heartbeat = nil after NewReal; want a heartbeat instance")
	}

	// Track whether Shutdown closes the heartbeat. The implementer is
	// required to set heartbeat.stopped atomically when its goroutine exits.
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.heartbeat.Stopped() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("heartbeat goroutine did not exit within 2s after Shutdown")
}

// Compile-time sanity: ensure fakeProbe satisfies runtimeProbe.
var _ runtimeProbe = (*fakeProbe)(nil)
