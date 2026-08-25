// Package cardresolve resolves the agent card a spawn should launch with.
//
// It is the fallback chain, and it exists as its own package because the choice
// it makes is not observable from its result: in production a published card and
// the embedded seed it was extracted from are IDENTICAL, so "the database
// answered" and "the database was unreachable" produce the same prompt. Source
// is what lets a caller — and a test — tell those apart.
//
// The standing constraint is the store's: agents never brick on the store. Every
// failure here degrades to a compiled-in seed and none of them is reported to
// the caller as an error, because there is nothing useful a launch path can do
// with one. `sprawl store doctor` is where an operator learns the event log is
// unhappy; a refused spawn is not.
package cardresolve

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/dmotles/sprawl/internal/card"
)

// Source says which leg of the chain produced a card.
type Source string

const (
	// SourceDB — the published card from the event log.
	SourceDB Source = "db"
	// SourceSeed — the compiled-in card for the requested agent type.
	SourceSeed Source = "seed"
	// SourceFallbackSeed — the compiled-in ENGINEER card, standing in for an
	// agent type that has no card of its own. Distinct from SourceSeed because
	// the agent is being given a prompt written for a different role, which is a
	// materially different thing to report than "the store was down".
	SourceFallbackSeed Source = "fallback-seed"
)

// fallbackType is the agent type whose seed stands in for an unknown one. It
// carries over buildRoleSystemPrompt's `default:` arm, which is load-bearing:
// "weave" is a real AgentState.Type with no card of its own, and without this a
// spawn of an unrecognised type would render an EMPTY system prompt.
const fallbackType = "engineer"

// Fetcher reads a published card. Implemented by *store.Ledger.
//
// An interface rather than the concrete Ledger so this package does not depend
// on internal/store — the store depends on internal/card, and a resolver in the
// middle that imported both would put the launch path one edit away from an
// import cycle.
type Fetcher interface {
	CardForType(ctx context.Context, agentType string) (*card.Card, error)
}

// fetchTimeout bounds the published-card lookup.
//
// A refused connection fails fast on its own; the case this exists for is a pool
// that accepts the query and never answers, which would otherwise block
// prepareLaunch forever — an agent that never starts and nothing reporting why.
// Two seconds is long enough for a healthy remote database and short enough that
// a spawn against a sick one is merely late.
//
// atomicDuration rather than a plain package var, per the repo-wide convention
// for a duration knob tests override: ForType is called from the launch path,
// which runs concurrently with everything else in the supervisor, so an
// unsynchronised knob is a data race the race detector will find.
var fetchTimeout = newAtomicDuration(2 * time.Second)

// atomicDuration is the repo's convention for a duration knob production reads
// concurrently and tests override. Deliberately duplicated rather than shared
// — see internal/backend/session.go and internal/merge/runtests.go.
type atomicDuration struct{ ns atomic.Int64 }

func newAtomicDuration(d time.Duration) *atomicDuration {
	v := &atomicDuration{}
	v.set(d)
	return v
}

func (v *atomicDuration) get() time.Duration  { return time.Duration(v.ns.Load()) }
func (v *atomicDuration) set(d time.Duration) { v.ns.Store(int64(d)) }

// Resolver resolves cards. A zero Resolver is valid and serves seeds only, which
// is the majority configuration: the event log is off by default.
type Resolver struct {
	Fetcher Fetcher
}

// ForType returns the card to launch an agent of agentType with.
//
// It does not return an error for any reachable input. The signature keeps one
// so that a future leg can fail loudly, and so callers are not tempted to read
// "no error" as "the database answered" — that is what Source is for.
func (r *Resolver) ForType(ctx context.Context, agentType string) (*card.Card, Source, error) {
	if c := r.fetchPublished(ctx, agentType); c != nil {
		return c, SourceDB, nil
	}
	if c, err := card.SeedForType(agentType); err == nil {
		return c, SourceSeed, nil
	}
	c, err := card.SeedForType(fallbackType)
	if err != nil {
		// Unreachable from a well-formed binary — the seeds are embedded and
		// pinned by tests in internal/card — and fatal if it ever is reached:
		// there is no prompt to launch with. Reported rather than papered over
		// with a zero card, which would launch an agent with no instructions.
		return nil, "", err
	}
	return c, SourceFallbackSeed, nil
}

// fetchPublished returns the published card, or nil if there is not a usable one.
//
// nil covers every failure uniformly — no fetcher, an error, a hang, a nil card
// with a nil error, and a card that does not validate — because the caller's
// response to all of them is the same and a resolver that distinguished them
// would be inventing a decision nobody makes.
func (r *Resolver) fetchPublished(ctx context.Context, agentType string) *card.Card {
	if r.Fetcher == nil {
		return nil
	}
	// Checked HERE rather than left to the Fetcher. A Fetcher that answers
	// without consulting ctx would otherwise hand back a card obtained under a
	// dead context, so "a cancelled caller still gets a usable card, and gets it
	// from the seed" becomes a property of this function instead of a property
	// every Fetcher has to be trusted to have.
	if ctx.Err() != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout.get())
	defer cancel()

	// Called inline. An earlier draft ran the fetch on a goroutine and raced it
	// against ctx.Done() so the bound would hold even against a Fetcher that
	// ignores ctx — but the only implementation is *store.Ledger over pgxpool,
	// which honours ctx for both the acquire and the query, so that machinery
	// bought protection against a hypothetical at the cost of a leaked goroutine
	// on every timeout. The residual risk is stated plainly: the TIMEOUT half of
	// this bound, unlike the cancellation half above, does rely on the Fetcher
	// honouring ctx. A Fetcher that does not is a defect in the Fetcher.
	got, err := r.Fetcher.CardForType(ctx, agentType)
	if err != nil || got == nil {
		return nil
	}
	// Validated here because nothing else does. store.CardForType scans raw
	// columns straight into a Card, so an invalid row would otherwise reach
	// Render: an unknown template token hard-fails at spawn, and an empty body
	// launches an agent with no role and no guardrails while reporting success.
	if err := got.Validate(); err != nil {
		return nil
	}
	// A card for the wrong role is the worst outcome available here, because it
	// is the only one that looks entirely plausible: the agent gets a complete,
	// well-formed system prompt for somebody else's job.
	if got.AgentType != agentType {
		return nil
	}
	return got
}
