package supervisor

import (
	"context"
	"sync"

	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/store"
)

// runLedgerSubscriber subscribes to bus and forwards every RuntimeEvent to the
// event-log lifecycle emitter (QUM-1249).
//
// Deliberately shaped exactly like runUsageSubscriber in runtime_launcher.go,
// down to the buffer semantics and the once-guarded stop function, because it
// sits at the same point in the same lifecycle and any divergence between the
// two would be a difference nobody chose.
//
// Buffer is 32 — if it fills, the EventBus drops events for this subscriber
// only, and the existing QUM-681 drop telemetry surfaces it. Note what a drop
// costs here specifically: turn boundaries are the liveness signal (Appendix B
// item 4), so a dropped turn_finished makes an agent look quieter than it is. It
// does not affect the agent itself.
//
// A nil emitter is tolerated and still drains the channel: not draining would
// back the bus up for every OTHER subscriber, so the disabled path has to keep
// reading. This is the common case — the feature flag is off by default.
func runLedgerSubscriber(bus *runtimepkg.EventBus, em *store.LifecycleEmitter, name string) func() {
	ch, unsub := bus.SubscribeNamed(name, 32)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for ev := range ch {
			if em == nil {
				continue
			}
			em.Handle(ev)
		}
		if em != nil {
			// Close AFTER the channel drains, so run_finished is the last event
			// and reflects every turn that actually happened. It runs on this
			// goroutine, which is why LifecycleEmitter needs no mutex.
			em.Close(context.Background())
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			unsub()
			<-doneCh
		})
	}
}

// newLifecycleEmitter builds the emitter for one agent's run, or nil when the
// store is off.
//
// Every failure here yields nil rather than an error. The store is an
// observability component: it must never be the reason an agent fails to
// launch, and a launch path that could fail on it would violate the
// "agents never brick on the store" requirement at the worst possible moment.
// A misconfiguration is surfaced by `sprawl store doctor` and by the warning
// Process/Open already log, not by refusing to start an agent.
func newLifecycleEmitter(ctx context.Context, spec RuntimeStartSpec, sessionID string) *store.LifecycleEmitter {
	ledger, err := store.Process(ctx, spec.SprawlRoot)
	if err != nil || ledger == nil {
		return nil
	}

	// Agent metadata comes from the on-disk state, which the launcher has
	// already loaded by this point; re-reading it here keeps this seam
	// independent of the launcher's internals. A missing state file yields empty
	// fields rather than no emitter — the run still happened.
	var agentType, agentFamily, branch, parent, worktree string
	if a, err := state.LoadAgent(spec.SprawlRoot, spec.Name); err == nil && a != nil {
		agentType, agentFamily, branch, parent, worktree = a.Type, a.Family, a.Branch, a.Parent, a.Worktree
	}
	if worktree == "" {
		worktree = spec.SprawlRoot
	}

	// Provenance is best-effort: a dirty-digest failure must not cost the run
	// its whole lifecycle record, so an unreadable tree yields an absent field
	// rather than no emitter.
	gitSHA, _ := store.HeadSHA(ctx, store.RealGit, worktree)
	dirty, _ := store.DirtyDigest(ctx, store.RealGit, worktree)

	return store.NewLifecycleEmitter(store.LifecycleDeps{
		Ledger:      ledger,
		AgentName:   spec.Name,
		AgentType:   agentType,
		AgentFamily: agentFamily,
		Parent:      parent,
		Branch:      branch,
		SessionID:   sessionID,
		Resumed:     spec.Resume,
		GitSHA:      gitSHA,
		DirtyDigest: dirty,
	})
}
