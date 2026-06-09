// Package supervisor — QUM-730 heartbeat/liveness-check loop.
//
// The heartbeat periodically scans the runtime registry for agents that
// appear stuck and either nudges them with an ephemeral system-notification
// (delivered through the maildir status-class drain channel) or escalates
// to the agent's parent. See docs/designs/qum-730-supervisor-heartbeat.md
// and the contract pinned by internal/supervisor/heartbeat_test.go.
package supervisor

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// LivenessConfig is the typed, resolved heartbeat configuration. Defaults
// applied by ResolveLivenessConfig. See heartbeat_test.go for the
// contract.
type LivenessConfig struct {
	Enabled               bool
	HeartbeatInterval     time.Duration
	IdleThreshold         time.Duration
	Tier2ConsecutiveTicks int
	EscalationThreshold   int
}

// LivenessConfigRaw is the YAML shape parsed from .sprawl/config.yaml
// under a top-level `liveness:` block. Durations are strings (parsed via
// time.ParseDuration); the Enabled field is a *bool so an unset value is
// distinguishable from explicit false — a partial YAML block (e.g. only
// `idle_threshold:` set) won't silently disable the heartbeat.
type LivenessConfigRaw struct {
	// Enabled toggles the heartbeat loop. Nil means "not set" — defaults
	// to enabled. A non-nil pointer is used at face value.
	Enabled               *bool  `yaml:"enabled"`
	HeartbeatInterval     string `yaml:"heartbeat_interval"`
	IdleThreshold         string `yaml:"idle_threshold"`
	Tier2ConsecutiveTicks int    `yaml:"tier2_consecutive_ticks"`
	EscalationThreshold   int    `yaml:"escalation_threshold"`
}

// Defaults — see QUM-730 oracle plan.
const (
	defaultHeartbeatInterval     = 30 * time.Minute
	defaultIdleThreshold         = 15 * time.Minute
	defaultTier2ConsecutiveTicks = 4
	defaultEscalationThreshold   = 3
	minHeartbeatInterval         = 5 * time.Minute
)

// ResolveLivenessConfig applies defaults to a (possibly nil) raw YAML
// block and enforces the 5-minute minimum HeartbeatInterval floor.
func ResolveLivenessConfig(raw *LivenessConfigRaw) LivenessConfig {
	cfg := LivenessConfig{
		Enabled:               true,
		HeartbeatInterval:     defaultHeartbeatInterval,
		IdleThreshold:         defaultIdleThreshold,
		Tier2ConsecutiveTicks: defaultTier2ConsecutiveTicks,
		EscalationThreshold:   defaultEscalationThreshold,
	}
	if raw == nil {
		return cfg
	}
	if raw.Enabled != nil {
		cfg.Enabled = *raw.Enabled
	}
	if v := strings.TrimSpace(raw.HeartbeatInterval); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.HeartbeatInterval = d
		} else {
			slog.Default().Warn(
				"heartbeat: ignoring malformed liveness.heartbeat_interval; using default",
				slog.String("value", v),
				slog.Any("err", err),
			)
		}
	}
	if v := strings.TrimSpace(raw.IdleThreshold); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.IdleThreshold = d
		} else {
			slog.Default().Warn(
				"heartbeat: ignoring malformed liveness.idle_threshold; using default",
				slog.String("value", v),
				slog.Any("err", err),
			)
		}
	}
	if raw.Tier2ConsecutiveTicks > 0 {
		cfg.Tier2ConsecutiveTicks = raw.Tier2ConsecutiveTicks
	}
	if raw.EscalationThreshold > 0 {
		cfg.EscalationThreshold = raw.EscalationThreshold
	}
	if cfg.HeartbeatInterval < minHeartbeatInterval {
		cfg.HeartbeatInterval = minHeartbeatInterval
	}
	return cfg
}

// runtimeProbe is the narrow interface the heartbeat consumes for each
// runtime. Implemented by *AgentRuntime in production and by fakeProbe in
// tests. Keeping it narrow keeps the heartbeat unit-testable.
type runtimeProbe interface {
	Name() string
	InTurn() bool
	LastActivityAt() time.Time
	Snapshot() RuntimeSnapshot
}

// runtimeLister is the narrow seam the heartbeat consumes to enumerate
// runtimes. Implemented by registryListerAdapter wrapping *RuntimeRegistry.
type runtimeLister interface {
	List() []runtimeProbe
}

// registryListerAdapter adapts *RuntimeRegistry (which returns concrete
// *AgentRuntime) to the runtimeLister interface.
type registryListerAdapter struct {
	reg *RuntimeRegistry
}

func (a *registryListerAdapter) List() []runtimeProbe {
	if a == nil || a.reg == nil {
		return nil
	}
	rts := a.reg.List()
	out := make([]runtimeProbe, 0, len(rts))
	for _, rt := range rts {
		out = append(out, rt)
	}
	return out
}

// Name returns the agent name from the runtime snapshot. Implements the
// runtimeProbe interface for *AgentRuntime so the registry adapter can
// surface it through the same channel as the test fake.
func (r *AgentRuntime) Name() string {
	return r.Snapshot().Name
}

// heartbeatDeps wires the heartbeat's collaborators. Defaults applied in
// newHeartbeat: NowFn → time.Now, NewTicker → real ticker, Logger →
// slog.Default.
type heartbeatDeps struct {
	Cfg               LivenessConfig
	SprawlRoot        string
	Registry          runtimeLister
	SendMessage       func(ctx context.Context, to, body string, interrupt bool) (*SendMessageResult, error)
	SendLivenessCheck func(sprawlRoot, to string) (string, error)
	LoadAgent         func(sprawlRoot, name string) (*state.AgentState, error)
	ReadActivityTail  func(string, int) ([]agentloop.ActivityEntry, error)
	ActivityPath      func(sprawlRoot, name string) string
	WakeForDelivery   func(*AgentRuntime) error
	NowFn             func() time.Time
	NewTicker         func(d time.Duration) (<-chan time.Time, func())
	ToastFn           func(format string, args ...any)
	Logger            *slog.Logger
}

// agentState is the heartbeat's per-agent bookkeeping. Owned exclusively
// by the loop goroutine; all reads/writes happen under h.mu in tickAgent.
type agentTickState struct {
	tier2Ramp         int
	consecutiveNudges int
	lastObservedActAt time.Time
	lastNudgeAt       time.Time
	escalated         bool
	// initialized tracks whether we've seen this agent before. The first
	// observation snapshots lastObservedActAt without treating it as
	// activity progress, so we don't spuriously reset counters on the
	// first qualifying tick.
	initialized bool
}

// heartbeat is the long-lived per-supervisor liveness-check goroutine.
type heartbeat struct {
	deps      heartbeatDeps
	stopCh    chan struct{}
	doneCh    chan struct{}
	stopped   atomic.Bool
	startOnce sync.Once

	mu     sync.Mutex
	agents map[string]*agentTickState
}

// newHeartbeat constructs a heartbeat with defaults applied for nil deps.
func newHeartbeat(deps heartbeatDeps) *heartbeat {
	if deps.NowFn == nil {
		deps.NowFn = time.Now
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.NewTicker == nil {
		deps.NewTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	if deps.ReadActivityTail == nil {
		deps.ReadActivityTail = agentloop.ReadActivityTail
	}
	if deps.ActivityPath == nil {
		deps.ActivityPath = agentloop.ActivityPath
	}
	if deps.LoadAgent == nil {
		deps.LoadAgent = state.LoadAgent
	}
	return &heartbeat{
		deps:   deps,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
		agents: make(map[string]*agentTickState),
	}
}

// Start begins the heartbeat goroutine. Guarded by sync.Once so repeat
// calls are no-ops.
func (h *heartbeat) Start() {
	h.startOnce.Do(func() {
		if !h.deps.Cfg.Enabled {
			// Mark Stopped so lifecycle tests don't block waiting on a
			// goroutine that was never started.
			h.stopped.Store(true)
			close(h.doneCh)
			return
		}
		go h.loop()
	})
}

// Stop signals the heartbeat goroutine to exit and blocks until it has.
func (h *heartbeat) Stop() {
	select {
	case <-h.stopCh:
		// Already closed.
	default:
		close(h.stopCh)
	}
	<-h.doneCh
}

// Stopped reports whether the goroutine has exited.
func (h *heartbeat) Stopped() bool { return h.stopped.Load() }

func (h *heartbeat) loop() {
	defer func() {
		h.stopped.Store(true)
		close(h.doneCh)
	}()
	// Bind a context to stopCh so escalation SendMessage calls can be
	// cancelled on Shutdown rather than blocking on the supervisor mutex.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-h.stopCh
		cancel()
	}()

	ch, stop := h.deps.NewTicker(h.deps.Cfg.HeartbeatInterval)
	defer stop()
	for {
		select {
		case <-h.stopCh:
			return
		case now, ok := <-ch:
			if !ok {
				return
			}
			h.runOnce(ctx, now)
		}
	}
}

// runOnce is the synchronous tick-handler test seam. Iterates the
// registry once, applies detection rules, and fires nudges/escalations.
func (h *heartbeat) runOnce(ctx context.Context, now time.Time) {
	if !h.deps.Cfg.Enabled {
		return
	}
	if h.deps.Registry == nil {
		return
	}
	for _, probe := range h.deps.Registry.List() {
		h.tickAgent(ctx, probe, now)
	}
}

func (h *heartbeat) tickAgent(ctx context.Context, probe runtimeProbe, now time.Time) {
	name := probe.Name()
	if name == "" {
		return
	}
	// Snapshot once per tick to avoid racing inconsistent reads.
	snap := probe.Snapshot()
	lastAct := probe.LastActivityAt()

	h.mu.Lock()
	defer h.mu.Unlock()

	st, ok := h.agents[name]
	if !ok {
		st = &agentTickState{}
		h.agents[name] = st
	}

	// First-observation snapshot: record lastAct but do NOT treat it as
	// progress. Otherwise the very first qualifying tick wipes counters
	// (including any we are about to increment) and escalation across
	// three consecutive ticks at the same lastAct never fires.
	if !st.initialized {
		st.lastObservedActAt = lastAct
		st.initialized = true
	} else if !lastAct.IsZero() && lastAct.After(st.lastObservedActAt) {
		// Counter-reset on observed activity progress.
		st.consecutiveNudges = 0
		st.tier2Ramp = 0
		st.escalated = false
		st.lastObservedActAt = lastAct
	}

	// Negative gates: liveness, in-turn, idle threshold.
	if snap.Liveness != liveness.Running {
		return
	}
	// QUM-549: a wedged-mid-tool agent (InTurn==true but stuck) is out
	// of scope for the v1 heartbeat — we only consider clearly-idle
	// agents. Revisit if/when QUM-549 lands.
	if probe.InTurn() {
		return
	}
	if lastAct.IsZero() {
		return
	}
	idle := now.Sub(lastAct)
	if idle < h.deps.Cfg.IdleThreshold {
		return
	}

	// Determine tier from the activity tail.
	tier, lastKind, lastSummary := h.classify(name)

	switch tier {
	case 1:
		st.tier2Ramp = 0
		h.maybeNudge(ctx, probe, snap, st, name, now, idle, lastKind, lastSummary)
	case 2:
		st.tier2Ramp++
		if st.tier2Ramp < h.deps.Cfg.Tier2ConsecutiveTicks {
			return
		}
		// Tier-2 ramp complete — reset ramp counter so it has to
		// re-accumulate after each nudge.
		st.tier2Ramp = 0
		h.maybeNudge(ctx, probe, snap, st, name, now, idle, lastKind, lastSummary)
	default:
		// Not a candidate.
		st.tier2Ramp = 0
	}
}

// classify inspects the last activity-ring entries for the agent and
// returns the tier (1 = immediate, 2 = ramp, 0 = no candidate) plus the
// last entry's kind/summary for escalation diagnostics.
func (h *heartbeat) classify(name string) (tier int, lastKind, lastSummary string) {
	if h.deps.ReadActivityTail == nil || h.deps.ActivityPath == nil {
		return 0, "", ""
	}
	path := h.deps.ActivityPath(h.deps.SprawlRoot, name)
	entries, err := h.deps.ReadActivityTail(path, 32)
	if err != nil || len(entries) == 0 {
		return 0, "", ""
	}
	last := entries[len(entries)-1]
	lastKind = last.Kind
	lastSummary = last.Summary

	switch last.Kind {
	case "result":
		if strings.HasPrefix(last.Summary, "error") {
			return 1, lastKind, lastSummary
		}
		return 2, lastKind, lastSummary
	case "rate_limit":
		return 1, lastKind, lastSummary
	case "system":
		// init/recover system entries are tier-1 only if no
		// assistant_text or tool_use followed them in the tail. Since
		// the tail entries are ordered oldest-first and `last` is the
		// final entry, "no follow-up" is exactly the condition that
		// `last` is itself the system entry.
		s := strings.ToLower(strings.TrimSpace(last.Summary))
		if strings.HasPrefix(s, "init") || strings.HasPrefix(s, "recover") {
			return 1, lastKind, lastSummary
		}
		return 2, lastKind, lastSummary
	default:
		return 2, lastKind, lastSummary
	}
}

func (h *heartbeat) maybeNudge(ctx context.Context, probe runtimeProbe, snap RuntimeSnapshot, st *agentTickState, name string, now time.Time, idle time.Duration, lastKind, lastSummary string) {
	if st.escalated {
		// Sticky silence — escalation already sent; wait for activity
		// to clear the flag (handled in tickAgent's counter-reset).
		return
	}

	// Fire one nudge.
	if h.deps.SendLivenessCheck != nil {
		if _, err := h.deps.SendLivenessCheck(h.deps.SprawlRoot, name); err != nil {
			h.deps.Logger.Debug(
				"heartbeat: SendLivenessCheck failed",
				slog.String("agent", name),
				slog.Any("err", err),
			)
		}
	}
	// Wake the runtime so it drains the liveness_check on its next
	// turn. The Registry's runtimeProbe is implemented by both
	// *AgentRuntime and the test fakeProbe; we only have a *AgentRuntime
	// to pass to WakeForDelivery when the probe is concrete.
	if h.deps.WakeForDelivery != nil {
		if rt, ok := probe.(*AgentRuntime); ok {
			_ = h.deps.WakeForDelivery(rt)
		}
	}
	st.consecutiveNudges++
	st.lastNudgeAt = now

	if st.consecutiveNudges < h.deps.Cfg.EscalationThreshold {
		return
	}

	// Escalation path.
	h.escalate(ctx, snap, name, now, idle, lastKind, lastSummary)
	st.escalated = true
}

func (h *heartbeat) escalate(ctx context.Context, snap RuntimeSnapshot, name string, _ time.Time, idle time.Duration, lastKind, lastSummary string) {
	parent := snap.Parent
	// Resolve from disk if missing on the snapshot (parent may be
	// populated lazily after NewReal). Best-effort — ignore lookup
	// failures and fall back to snap.Parent.
	if parent == "" && h.deps.LoadAgent != nil {
		if a, err := h.deps.LoadAgent(h.deps.SprawlRoot, name); err == nil && a != nil {
			parent = a.Parent
		}
	}

	body := buildEscalationBody(name, idle, lastKind, lastSummary, h.deps.Cfg.EscalationThreshold)

	if parent == "" {
		// Root weave — toast + WARN log only.
		h.deps.Logger.Warn(
			"heartbeat: root weave appears stuck",
			slog.String("agent", name),
			slog.Duration("idle", idle),
			slog.String("last_kind", lastKind),
			slog.String("last_summary", lastSummary),
		)
		if h.deps.ToastFn != nil {
			h.deps.ToastFn("agent %s appears stuck (idle %s)", name, idle.Round(time.Second))
		}
		return
	}

	if h.deps.SendMessage == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := h.deps.SendMessage(ctx, parent, body, false); err != nil {
		h.deps.Logger.Debug(
			"heartbeat: escalation SendMessage failed",
			slog.String("agent", name),
			slog.String("parent", parent),
			slog.Any("err", err),
		)
	}
}

func buildEscalationBody(name string, idle time.Duration, lastKind, lastSummary string, nudges int) string {
	var b strings.Builder
	b.WriteString("Agent ")
	b.WriteString(name)
	b.WriteString(" appears stuck: idle for ")
	b.WriteString(idle.Round(time.Second).String())
	b.WriteString(" after ")
	if nudges == 1 {
		b.WriteString("1 liveness nudge")
	} else {
		b.WriteString(strconv.Itoa(nudges))
		b.WriteString(" liveness nudges")
	}
	b.WriteString(" with no observed activity. Last activity kind=")
	b.WriteString(lastKind)
	b.WriteString(" summary=\"")
	b.WriteString(lastSummary)
	b.WriteString("\". Please check on this agent.")
	return b.String()
}
