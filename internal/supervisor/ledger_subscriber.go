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
func newLifecycleEmitter(ctx context.Context, spec RuntimeStartSpec, prep *preparedLaunch, sessionID string) *store.LifecycleEmitter {
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

	// prep is nil on the weave path, which has its own handle and no
	// preparedLaunch: weave's rendered prompt is not available at this seam, so
	// its run is recorded without a spawn context rather than with an empty one.
	var sc *store.SpawnContext
	if prep != nil {
		v := spawnContextFor(spec, prep, sessionID)
		sc = &v
	}
	return store.NewLifecycleEmitter(store.LifecycleDeps{
		Ledger:       ledger,
		SpawnContext: sc,
		AgentName:    spec.Name,
		AgentType:    agentType,
		AgentFamily:  agentFamily,
		Parent:       parent,
		Branch:       branch,
		SessionID:    sessionID,
		Resumed:      spec.Resume,
		GitSHA:       gitSHA,
		DirtyDigest:  dirty,
	})
}

// spawnContextFor maps one launch onto the spawn_context artifact's fields
// (QUM-1251 AC6).
//
// Which source is authoritative matters and is not obvious, because several
// fields have more than one candidate. The agent NAME comes from the spec — it
// is what the launcher was asked to start, and what every other lifecycle field
// is keyed on. The agent TYPE comes from the on-disk state, never from the
// card's own agent_type: the card is chosen FROM the type, so recording the
// card's would make a fallback-seed launch claim it ran the type it fell back
// to. Every field here is a string, so a transposition produces a perfectly
// well-formed artifact describing a different launch.
func spawnContextFor(spec RuntimeStartSpec, prep *preparedLaunch, sessionID string) store.SpawnContext {
	sc := store.SpawnContext{
		AgentName:      spec.Name,
		SessionID:      sessionID,
		Model:          prep.sessionSpec.Model,
		Effort:         prep.sessionSpec.Effort,
		RenderedPrompt: prep.systemPrompt,
	}
	if prep.agentState != nil {
		sc.AgentType = prep.agentState.Type
		sc.Branch = prep.agentState.Branch
	}
	// A nil card leaves every card field absent rather than recording a
	// definition named "" at version 0, which a reader could not tell from a
	// real one. The prompt is still recorded: the run happened, and the prompt is
	// the one input a replay cannot reconstruct.
	if prep.card != nil {
		sc.CardName = prep.card.Name
		sc.CardVersion = prep.card.Version
		sc.CardContentSHA256 = prep.card.ContentSHA256
		sc.CardSource = string(prep.cardSource)
	}
	return sc
}
