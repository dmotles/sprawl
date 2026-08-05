// Package supervisor — QUM-1071 blurb-refresh ticker.
//
// The capability-blurb refresher (QUM-899) used to ride along on the QUM-730
// heartbeat, which was the only reason it looked like a liveness concern: the
// heartbeat merely happened to be a ticker over the runtime registry. This file
// is that ticker and nothing else — iterate the registry on a fixed interval and
// hand each agent its name and last-activity time to the RefreshBlurb seam.
//
// Unlike the heartbeat this ticker keeps NO per-agent state, so its mutex is
// only ever held inside Start/Stop and never around the seam: RefreshBlurb is
// called outside any lock, and the seam itself is non-blocking (it dispatches a
// background goroutine). The dirty-check and 15-minute floor live in
// (*Real).maybeRefreshBlurb and are unchanged.
package supervisor

import (
	"sync"
	"sync/atomic"
	"time"
)

// blurbRefreshInterval is how often the registry is swept. Hardcoded, with no
// config knob on purpose (QUM-1071): it matches the heartbeat's default cadence
// this was extracted from, and blurb.DecideTrigger applies its own 15-minute
// floor anyway.
const blurbRefreshInterval = 30 * time.Minute

// blurbProbe is the narrow per-runtime interface the blurb ticker consumes —
// deliberately only what it uses. Implemented by *AgentRuntime in production.
// Kept deliberately narrow rather than reusing the heartbeat's wider probe, so
// that heartbeat.go could be deleted (QUM-1071) without touching this file.
type blurbProbe interface {
	Name() string
	LastActivityAt() time.Time
}

// blurbRuntimeLister is the seam used to enumerate runtimes.
type blurbRuntimeLister interface {
	List() []blurbProbe
}

// blurbRegistryAdapter adapts *RuntimeRegistry (which returns concrete
// *AgentRuntime) to blurbRuntimeLister.
type blurbRegistryAdapter struct {
	reg *RuntimeRegistry
}

func (a *blurbRegistryAdapter) List() []blurbProbe {
	if a == nil || a.reg == nil {
		return nil
	}
	rts := a.reg.List()
	out := make([]blurbProbe, 0, len(rts))
	for _, rt := range rts {
		out = append(out, rt)
	}
	return out
}

var _ blurbProbe = (*AgentRuntime)(nil)

// Name returns the agent name from the runtime snapshot, satisfying blurbProbe.
// It was moved here out of the QUM-730 heartbeat ahead of that file's deletion.
//
// Note it materialises a whole RuntimeSnapshot, so callers should call it once
// per tick rather than per use.
func (r *AgentRuntime) Name() string {
	return r.Snapshot().Name
}

// blurbTickerDeps wires the ticker's collaborators. Defaults are applied in
// newBlurbTicker for NewTicker.
type blurbTickerDeps struct {
	Registry blurbRuntimeLister
	// RefreshBlurb is invoked once per tick per named agent with the
	// runtime-derived last-activity time, so the supervisor can decide
	// (dirty-check + 15-min floor) whether to regenerate the agent's capability
	// blurb (QUM-899). Called outside any mutex; the seam is non-blocking.
	RefreshBlurb func(name string, lastActivityAt time.Time)
	NewTicker    func(d time.Duration) (<-chan time.Time, func())
}

// blurbTicker is the long-lived per-supervisor blurb-refresh goroutine.
type blurbTicker struct {
	deps   blurbTickerDeps
	stopCh chan struct{}
	doneCh chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once

	// mu linearises Start against Stop so neither can leave a goroutine running
	// past Stop() nor block Stop() on a goroutine that was never launched.
	mu            sync.Mutex
	launched      bool
	stopRequested bool

	// started is set INSIDE the loop goroutine, after its ticker is installed —
	// never in Start() — so it is evidence the goroutine actually ran. Never
	// cleared: it answers "did the loop ever run", not "is it running now".
	// Stopped is the teardown signal.
	started atomic.Bool
	// stopped means "torn down": set by the loop's teardown, and by a Stop()
	// that had no goroutine to wait for.
	stopped atomic.Bool
}

// newBlurbTicker constructs a ticker with defaults applied for nil deps. It does
// no work until Start.
func newBlurbTicker(deps blurbTickerDeps) *blurbTicker {
	if deps.NewTicker == nil {
		deps.NewTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	return &blurbTicker{
		deps:   deps,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start launches the ticker goroutine. Guarded by sync.Once so repeat calls are
// no-ops, and a no-op after Stop.
func (b *blurbTicker) Start() {
	b.startOnce.Do(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.stopRequested {
			return
		}
		b.launched = true
		go b.loop()
	})
}

// Stop signals the goroutine to exit and blocks until it has. Safe to call
// without Start (returns instead of deadlocking) and safe to call concurrently
// (sync.Once guards the close).
func (b *blurbTicker) Stop() {
	// Closed before mu is taken, so a loop launched by a concurrent Start sees
	// an already-closed stopCh and tears down on its first select.
	b.stopOnce.Do(func() { close(b.stopCh) })

	b.mu.Lock()
	b.stopRequested = true
	launched := b.launched
	b.mu.Unlock()

	if !launched {
		b.stopped.Store(true)
		return
	}
	<-b.doneCh
}

// Started reports whether the loop goroutine ever got as far as installing its
// ticker. Never cleared — pair it with Stopped to distinguish "running now".
func (b *blurbTicker) Started() bool { return b.started.Load() }

// Stopped reports whether the ticker has been torn down.
func (b *blurbTicker) Stopped() bool { return b.stopped.Load() }

func (b *blurbTicker) loop() {
	defer func() {
		b.stopped.Store(true)
		close(b.doneCh)
	}()
	ch, stop := b.deps.NewTicker(blurbRefreshInterval)
	defer stop()
	b.started.Store(true)
	for {
		select {
		case <-b.stopCh:
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			b.runOnce()
		}
	}
}

// runOnce is the synchronous tick-handler test seam: one registry sweep, one
// RefreshBlurb call per named agent, no lock held.
func (b *blurbTicker) runOnce() {
	if b.deps.Registry == nil || b.deps.RefreshBlurb == nil {
		return
	}
	for _, p := range b.deps.Registry.List() {
		name := p.Name()
		if name == "" {
			continue
		}
		b.deps.RefreshBlurb(name, p.LastActivityAt())
	}
}
