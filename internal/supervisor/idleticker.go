// Package supervisor — QUM-1186 lane 3: the idle-reaper ticker.
//
// Structurally a twin of blurbticker.go, and for the same reasons. Two designs
// were rejected explicitly:
//
//   - PostTurnSweep. It only fires inside `if turn.EndOfTurn`, so an agent that
//     goes idle and never turns again never fires it again — i.e. exactly the
//     agent the reaper exists to reclaim would be the one it never looks at.
//   - A per-handle ticker. A goroutine owned by the handle whose job is to
//     destroy that handle deadlocks against Stop.
//
// So: one supervisor-level goroutine, iterating the registry, calling a
// Reclaim seam per runtime with no lock held.
package supervisor

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// idleRuntimeLister is the seam used to enumerate runtimes. *RuntimeRegistry
// satisfies it directly — unlike blurbticker, no adapter is needed, because
// the reclaim seam wants the concrete *AgentRuntime.
type idleRuntimeLister interface {
	List() []*AgentRuntime
}

// idleReaperDeps wires the ticker's collaborators. NewTicker defaults in
// newIdleReaper.
type idleReaperDeps struct {
	Registry idleRuntimeLister
	// Reclaim is invoked once per tick per runtime, outside any mutex. The
	// predicate and the teardown both live behind it ((*Real).maybeReclaimIdle),
	// so this file holds only the loop. The ctx is cancelled by Stop, so a
	// sweep in progress when the supervisor shuts down declines to start new
	// teardowns rather than making Shutdown wait out a full stop budget.
	Reclaim func(ctx context.Context, rt *AgentRuntime)
	// Interval supplies the sweep cadence, read once when the loop installs
	// its ticker. A mid-run config change is NOT picked up until restart.
	Interval  func() time.Duration
	NewTicker func(d time.Duration) (<-chan time.Time, func())
}

// idleReaper is the long-lived per-supervisor idle-reclaim goroutine.
type idleReaper struct {
	deps   idleReaperDeps
	stopCh chan struct{}
	doneCh chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	startOnce sync.Once
	stopOnce  sync.Once

	// mu linearises Start against Stop so neither can leave a goroutine running
	// past Stop() nor block Stop() on a goroutine that was never launched.
	mu            sync.Mutex
	launched      bool
	stopRequested bool

	// started is set INSIDE the loop goroutine, after its ticker is installed —
	// never in Start() — so it is evidence the goroutine actually ran rather
	// than that a struct was constructed. That distinction is the whole point
	// here: a reaper that is built and never runs has no symptom except RSS
	// nobody is watching.
	started atomic.Bool
	stopped atomic.Bool
}

func newIdleReaper(deps idleReaperDeps) *idleReaper {
	if deps.NewTicker == nil {
		deps.NewTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &idleReaper{
		deps:   deps,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start launches the loop goroutine. Guarded by sync.Once so repeat calls are
// no-ops, and a no-op after Stop.
func (i *idleReaper) Start() {
	i.startOnce.Do(func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		if i.stopRequested {
			return
		}
		i.launched = true
		go i.loop()
	})
}

// Stop signals the goroutine to exit and blocks until it has. Safe without
// Start, and safe concurrently.
func (i *idleReaper) Stop() {
	i.stopOnce.Do(func() {
		close(i.stopCh)
		i.cancel()
	})

	i.mu.Lock()
	i.stopRequested = true
	launched := i.launched
	i.mu.Unlock()

	if !launched {
		i.stopped.Store(true)
		return
	}
	<-i.doneCh
}

// Started reports whether the loop goroutine ever got as far as installing its
// ticker. Never cleared — pair it with Stopped to distinguish "running now".
func (i *idleReaper) Started() bool { return i.started.Load() }

// Stopped reports whether the ticker has been torn down.
func (i *idleReaper) Stopped() bool { return i.stopped.Load() }

func (i *idleReaper) loop() {
	defer func() {
		i.stopped.Store(true)
		close(i.doneCh)
	}()
	interval := idleReclaimSweepFallback
	if i.deps.Interval != nil {
		if d := i.deps.Interval(); d > 0 {
			interval = d
		}
	}
	ch, stop := i.deps.NewTicker(interval)
	defer stop()
	i.started.Store(true)
	for {
		select {
		case <-i.stopCh:
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			i.runOnce()
		}
	}
}

// runOnce is the synchronous tick-handler test seam: one registry sweep, one
// Reclaim call per runtime, no lock held.
func (i *idleReaper) runOnce() {
	if i.deps.Registry == nil || i.deps.Reclaim == nil {
		return
	}
	for _, rt := range i.deps.Registry.List() {
		if rt == nil {
			continue
		}
		i.deps.Reclaim(i.ctx, rt)
	}
}

// idleReclaimSweepFallback is the cadence used when no Interval seam is wired.
// It exists so a misconfigured ticker sweeps too rarely rather than spinning.
const idleReclaimSweepFallback = time.Minute
