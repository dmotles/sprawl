package cardresolve

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/card"
)

// fakeFetcher is the DB seam.
//
// It records the types it was asked for, because "the resolver never queried"
// and "the query came back with nothing usable" are indistinguishable from the
// returned card alone — in production a DB card and a seed card are usually
// IDENTICAL, so every test here that claims something about the database path
// has to assert the call happened. A test that omits it is satisfied by a
// resolver whose whole body is `return card.SeedForType(agentType)`.
//
// Mutex-guarded because the resolver bounds the fetch with a timeout, which
// means a fetcher that ignores ctx keeps running after ForType returns — so the
// test's read races the fake's write under -race.
type fakeFetcher struct {
	mu       sync.Mutex
	card     *card.Card
	err      error
	block    time.Duration
	calls    []string
	deadline time.Duration // ctx deadline observed on the last call, relative to entry
	hadDL    bool
}

func (f *fakeFetcher) CardForType(ctx context.Context, agentType string) (*card.Card, error) {
	entry := time.Now()
	f.mu.Lock()
	f.calls = append(f.calls, agentType)
	if dl, ok := ctx.Deadline(); ok {
		f.hadDL, f.deadline = true, dl.Sub(entry)
	}
	block, c, err := f.block, f.card, f.err
	f.mu.Unlock()

	if block > 0 {
		select {
		case <-time.After(block):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c, err
}

func (f *fakeFetcher) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeFetcher) observedDeadline() (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deadline, f.hadDL
}

// wantQueried is the anti-vacuity check every DB-path claim in this file needs.
func wantQueried(t *testing.T, f *fakeFetcher, agentType string) {
	t.Helper()
	if got := f.asked(); len(got) != 1 || got[0] != agentType {
		t.Errorf("fetcher was asked for %v, want exactly [%s] — without this the assertions above are satisfied by a resolver that never queries at all", got, agentType)
	}
}

// seedFor deliberately reads the real embedded seed rather than a fixture: the
// whole claim of the seed fallback is that a spawn with no database gets the
// SAME prompt it gets today, and a hand-written fixture would let that claim be
// true of the fixture only.
func seedFor(t *testing.T, agentType string) *card.Card {
	t.Helper()
	c, err := card.SeedForType(agentType)
	if err != nil {
		t.Fatalf("card.SeedForType(%q): %v", agentType, err)
	}
	return c
}

// wantSameCard compares the FIELDS the callers of ForType consume, not pointer
// identity.
//
// Identity would happen to work — card.Seeds is a sync.OnceValues cache, so
// SeedForType hands back the same pointer every time — but it would be pinning
// implementation shape: a resolver that defensively copied the card (a
// defensible choice for a *Card shared by every spawn in the process) would go
// red for a reason unrelated to anything these tests claim.
func wantSameCard(t *testing.T, got, want *card.Card, what string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: got a nil card, want %s@%d", what, want.Name, want.Version)
	}
	for _, f := range []struct{ field, got, want string }{
		{"Name", got.Name, want.Name},
		{"AgentType", got.AgentType, want.AgentType},
		{"Model", got.Model, want.Model},
		{"Effort", got.Effort, want.Effort},
		{"Body", got.Body, want.Body},
	} {
		if f.got != f.want {
			t.Errorf("%s: %s = %q, want %q", what, f.field, truncate(f.got), truncate(f.want))
		}
	}
	if got.Version != want.Version {
		t.Errorf("%s: Version = %d, want %d", what, got.Version, want.Version)
	}
	if got.Opts != want.Opts {
		t.Errorf("%s: Opts = %+v, want %+v", what, got.Opts, want.Opts)
	}
}

func truncate(s string) string {
	if len(s) <= 60 {
		return s
	}
	return s[:60] + "…"
}

// publishedCard is VALID and differs from every seed in every field a caller
// reads, so an assertion that it came back cannot be satisfied by the fallback.
func publishedCard(agentType string) *card.Card {
	return &card.Card{
		Name:      "slim-" + agentType,
		Version:   2,
		AgentType: agentType,
		Model:     "haiku",
		Effort:    "high",
		Body:      "You are {{AGENT_NAME}}, a published card that no seed matches.",
		Opts:      card.RenderOpts{AppendEnvContext: true, SubagentBanner: true, SandboxWarning: true},
	}
}

// TestForType_PrefersTheDatabaseCard is the AC2 half this package can carry: the
// published card's MODEL and EFFORT reach the caller intact.
//
// AC2 proper ("mutate a card's model and SessionSpec.Model changes") is NOT
// discharged here and cannot be — it needs an assertion in the package that
// builds the SessionSpec. This shows only that the card arrives carrying them,
// which is the precondition.
func TestForType_PrefersTheDatabaseCard(t *testing.T) {
	want := publishedCard("engineer")
	f := &fakeFetcher{card: want}

	got, src, err := (&Resolver{Fetcher: f}).ForType(context.Background(), "engineer")
	if err != nil {
		t.Fatalf("ForType: %v", err)
	}
	if src != SourceDB {
		t.Errorf("source = %q, want %q — the published card must win over the seed", src, SourceDB)
	}
	wantSameCard(t, got, want, "published card")
	// Named explicitly as well as via wantSameCard: these two fields are the
	// payload AC2 is about, and a resolver that dropped every field it did not
	// model would otherwise fail with a less legible message.
	if got.Model != "haiku" || got.Effort != "high" {
		t.Errorf("Model/Effort = %q/%q, want haiku/high — the card is what governs them", got.Model, got.Effort)
	}
	wantQueried(t, f, "engineer")
}

// TestForType_PublishedAllFalseRenderOptsAreHonoured is the negative control for
// the validation the resolver performs: zero-valued Opts are a LEGITIMATE
// published value and must not be read as "suspicious, fall back".
//
// store.CardForType already refuses absent, null, `{}` and partial render
// options and turns them into an error, so all-false arriving here means an
// operator published all-false. A resolver that downgraded it to the seed would
// make every deliberately-plain card inert, and Source is the only place that
// would show.
func TestForType_PublishedAllFalseRenderOptsAreHonoured(t *testing.T) {
	want := publishedCard("engineer")
	want.Opts = card.RenderOpts{}
	f := &fakeFetcher{card: want}

	got, src, err := (&Resolver{Fetcher: f}).ForType(context.Background(), "engineer")
	if err != nil {
		t.Fatalf("ForType: %v", err)
	}
	if src != SourceDB {
		t.Fatalf("source = %q, want %q — all-false render options are a published value, not a defect", src, SourceDB)
	}
	if got.Opts != (card.RenderOpts{}) {
		t.Errorf("Opts = %+v, want all-false as published", got.Opts)
	}
	wantQueried(t, f, "engineer")
}

// TestForType_FallsBackToTheSeedWhenTheDatabaseFails covers AC3.
//
// Note what it deliberately CONFLATES: store.CardForType reports "no usable
// pool", "query failed" and "no row for this type" all as errors, so this one
// leg covers an unreachable event log AND a reachable one with an empty
// agent_cards. That is intended — the resolver's response is the same and a
// discriminator nobody consumes would be YAGNI — but do not cite this test as
// evidence that the two are told apart, because they are not.
func TestForType_FallsBackToTheSeedWhenTheDatabaseFails(t *testing.T) {
	f := &fakeFetcher{err: errors.New("store: no usable event log")}

	got, src, err := (&Resolver{Fetcher: f}).ForType(context.Background(), "researcher")
	if err != nil {
		t.Fatalf("a failed lookup must fall back, not fail the spawn: %v", err)
	}
	if src != SourceSeed {
		t.Errorf("source = %q, want %q", src, SourceSeed)
	}
	wantSameCard(t, got, seedFor(t, "researcher"), "researcher seed")
	wantQueried(t, f, "researcher")
}

func TestForType_NilFetcherUsesTheSeed(t *testing.T) {
	// The store is off by default, so this is the MAJORITY configuration. A
	// resolver that panicked or errored here would break every spawn in a tree
	// with no event log.
	got, src, err := (&Resolver{}).ForType(context.Background(), "qa")
	if err != nil {
		t.Fatalf("ForType with no fetcher: %v", err)
	}
	if src != SourceSeed {
		t.Errorf("source = %q, want %q", src, SourceSeed)
	}
	wantSameCard(t, got, seedFor(t, "qa"), "qa seed")
}

// TestForType_UnknownTypeFallsBackToTheEngineerCard keeps buildRoleSystemPrompt's
// `default:` arm alive at the resolver level.
//
// That arm is load-bearing: "weave" is a real value of AgentState.Type with no
// card of its own (legacy-root@1 is deferred out of QUM-1251) and it reaches
// this path in the existing session-spec tests. Without the arm an unknown type
// resolves to nothing and renders an EMPTY system prompt.
//
// Both fetcher states are legs, because a resolver that keyed the discriminator
// on "was there a fetcher" rather than "did this type have a seed of its own"
// would pass the nil-fetcher leg alone.
func TestForType_UnknownTypeFallsBackToTheEngineerCard(t *testing.T) {
	for name, f := range map[string]*fakeFetcher{
		"no fetcher":      nil,
		"fetcher errored": {err: errors.New("store: down")},
	} {
		t.Run(name, func(t *testing.T) {
			r := &Resolver{}
			if f != nil {
				r.Fetcher = f
			}
			got, src, err := r.ForType(context.Background(), "weave")
			if err != nil {
				t.Fatalf("an unknown type must still get a prompt: %v", err)
			}
			if src != SourceFallbackSeed {
				t.Errorf("source = %q, want %q — the caller has to be able to tell a borrowed card from one that fits", src, SourceFallbackSeed)
			}
			wantSameCard(t, got, seedFor(t, "engineer"), "engineer seed")
			if f != nil {
				wantQueried(t, f, "weave")
			}
		})
	}
}

// TestForType_PublishedCardForATypeWithNoSeed is the inverse of the test above,
// and it is the whole point of publishing cards as data: a type this binary has
// no seed for must still resolve to its OWN card when one is published, rather
// than borrowing the engineer's.
func TestForType_PublishedCardForATypeWithNoSeed(t *testing.T) {
	want := publishedCard("weave")
	f := &fakeFetcher{card: want}

	got, src, err := (&Resolver{Fetcher: f}).ForType(context.Background(), "weave")
	if err != nil {
		t.Fatalf("ForType: %v", err)
	}
	if src != SourceDB {
		t.Fatalf("source = %q, want %q — a published card for an unseeded type must not be downgraded to the engineer fallback", src, SourceDB)
	}
	wantSameCard(t, got, want, "published weave card")
	wantQueried(t, f, "weave")
}

// TestForType_DiscardsAnUnusableDatabaseCard is the validation gate.
//
// store.CardForType scans raw columns; nothing between the database and here
// runs card.Validate(), so an invalid row would otherwise reach Render. Each leg
// names an outcome strictly worse than falling back:
//
//   - nil card with a nil error, or an empty body: an agent launches with NO
//     system prompt — no role, no guardrails — and the spawn reports success.
//   - an unknown template token: Render hard-fails at spawn, or (worse, in a
//     future renderer) ships a literal "{{PARNET_NAME}}" to the model.
//   - version 0: the row cannot be cited in an agent_sessions record.
//   - the WRONG agent_type: the spawn gets another role's prompt while
//     reporting success. That is worse than an empty prompt, because it looks
//     entirely plausible.
func TestForType_DiscardsAnUnusableDatabaseCard(t *testing.T) {
	base := func(mut func(*card.Card)) *card.Card {
		c := publishedCard("engineer")
		mut(c)
		return c
	}
	for name, bad := range map[string]*card.Card{
		"nil card":         nil,
		"empty body":       base(func(c *card.Card) { c.Body = "  \n " }),
		"unknown token":    base(func(c *card.Card) { c.Body = "hi {{PARNET_NAME}}" }),
		"version zero":     base(func(c *card.Card) { c.Version = 0 }),
		"no name":          base(func(c *card.Card) { c.Name = "" }),
		"wrong agent_type": base(func(c *card.Card) { c.AgentType = "qa" }),
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeFetcher{card: bad}
			got, src, err := (&Resolver{Fetcher: f}).ForType(context.Background(), "engineer")
			if err != nil {
				t.Fatalf("the seed fallback must absorb this, not fail the spawn: %v", err)
			}
			if got == nil {
				t.Fatalf("ForType returned a nil card with a nil error (source %q)", src)
			}
			if strings.TrimSpace(got.Body) == "" {
				t.Fatalf("ForType returned a card with an empty body (source %q) — that renders an empty system prompt", src)
			}
			if src != SourceSeed {
				t.Errorf("source = %q, want %q", src, SourceSeed)
			}
			wantSameCard(t, got, seedFor(t, "engineer"), "engineer seed")
			// Aim check: the fallback must be a REJECTION, not a resolver that
			// never queried. Without it every leg here is green against a
			// seeds-only implementation.
			wantQueried(t, f, "engineer")
		})
	}
}

// TestForType_AppliesTheFetchTimeout is a differential pair: the block is held
// FIXED and only the configured bound varies, so the sole difference between the
// two outcomes is the knob.
//
// Varying both (a 1-minute block against a 50ms bound, and a 20ms block against
// a 10s bound) would be passed by any resolver with a hardcoded timeout anywhere
// in between, which is to say by one that ignores the seam entirely.
//
// The hang matters on its own: a pool that accepts the query and never answers
// is worse than a refused connection, because prepareLaunch simply blocks and
// nothing reports that the agent never started.
func TestForType_AppliesTheFetchTimeout(t *testing.T) {
	const block = 500 * time.Millisecond
	for _, tt := range []struct {
		name    string
		bound   time.Duration
		wantSrc Source
	}{
		{"bound below the fetch time", 5 * time.Millisecond, SourceSeed},
		{"bound above the fetch time", 10 * time.Second, SourceDB},
	} {
		t.Run(tt.name, func(t *testing.T) {
			SetFetchTimeoutForTest(t, tt.bound)
			published := publishedCard("engineer")
			f := &fakeFetcher{block: block, card: published}

			start := time.Now()
			got, src, err := (&Resolver{Fetcher: f}).ForType(context.Background(), "engineer")
			elapsed := time.Since(start)

			if err != nil {
				t.Fatalf("a slow lookup must never fail the spawn: %v", err)
			}
			if src != tt.wantSrc {
				t.Errorf("source = %q, want %q with a %s bound on a %s fetch", src, tt.wantSrc, tt.bound, block)
			}
			if tt.wantSrc == SourceDB {
				wantSameCard(t, got, published, "published card")
			} else {
				wantSameCard(t, got, seedFor(t, "engineer"), "engineer seed")
			}
			wantQueried(t, f, "engineer")

			// Deterministic pin on the seam's VALUE: no wall-clock assertion can
			// distinguish "honoured the configured bound" from "used some other
			// bound that happens to fall on the same side of the block".
			gotDL, ok := f.observedDeadline()
			if !ok {
				t.Fatalf("the fetch ran with no deadline at all — an unresponsive pool would block the launch forever")
			}
			//
			// Only the UPPER half of this is checked at millisecond bounds.
			// observedDeadline is measured at fetcher ENTRY, so it is the bound
			// MINUS however long the scheduler took to get there -- and on a box
			// running a fleet of agents that delay is routinely milliseconds.
			// A `gotDL < bound/2` leg therefore reported "the configured bound is
			// not what is applied" for a resolver that applied it exactly: an
			// observed 1.15ms against a 5ms bound, which is 3.85ms of scheduling,
			// not a defect. (Watched: it failed check-test-race under load.)
			//
			// The lower bound is not lost, it is asserted where scheduling noise
			// cannot reach it -- the 10s row, where bound/2 is 5 SECONDS. That
			// row is also the one the lower bound exists for: it is what refuses
			// a resolver with a hardcoded SMALL timeout. The 5ms row keeps the
			// upper check, which is what refuses a hardcoded LARGE one. Between
			// them both directions are still pinned.
			if gotDL > tt.bound {
				t.Errorf("the fetch context's deadline was %s, MORE than the configured %s — a larger bound than the seam says is being applied", gotDL, tt.bound)
			}
			if tt.bound >= time.Second && gotDL < tt.bound/2 {
				t.Errorf("the fetch context's deadline was %s, far below the configured %s — a smaller bound than the seam says is being applied", gotDL, tt.bound)
			}
			// Liveness guard only, deliberately loose: it fires when there is no
			// bound at all, and nothing finer should be read into it.
			if elapsed > 30*time.Second {
				t.Errorf("ForType took %s — no bound is being applied", elapsed)
			}
		})
	}
}

// TestForType_CancelledCallerStillYieldsACard is the input most likely to break
// "never fail a spawn": the resolver derives its bounded context from the
// caller's, so an already-cancelled parent makes the fetch fail instantly.
func TestForType_CancelledCallerStillYieldsACard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeFetcher{card: publishedCard("engineer")}

	got, src, err := (&Resolver{Fetcher: f}).ForType(ctx, "engineer")
	if err != nil {
		t.Fatalf("a cancelled caller must still get a card: %v", err)
	}
	if src != SourceSeed {
		t.Errorf("source = %q, want %q", src, SourceSeed)
	}
	wantSameCard(t, got, seedFor(t, "engineer"), "engineer seed")
}

// TestForType_CallerCancelledMidFetchStillYieldsACard covers the cancellation
// that arrives while the query is in flight, which the pre-flight ctx.Err()
// check cannot see.
//
// It exists because a mutation exposed the gap: replacing the caller's context
// with context.Background() as the timeout's parent left every other test in
// this file green. Without this leg, "the bounded context is derived from the
// caller's" is unmeasured — and a spawn the supervisor has already abandoned
// would go on waiting out the full fetch bound.
func TestForType_CallerCancelledMidFetchStillYieldsACard(t *testing.T) {
	SetFetchTimeoutForTest(t, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeFetcher{block: 30 * time.Second, card: publishedCard("engineer")}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	start := time.Now()
	got, src, err := (&Resolver{Fetcher: f}).ForType(ctx, "engineer")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("an abandoned spawn must still get a card: %v", err)
	}
	if src != SourceSeed {
		t.Errorf("source = %q, want %q", src, SourceSeed)
	}
	wantSameCard(t, got, seedFor(t, "engineer"), "engineer seed")
	wantQueried(t, f, "engineer")
	// The bound is 30s and the fetch is 30s, so returning promptly is only
	// possible if the cancellation propagated — this is the assertion, not a
	// liveness guard.
	if elapsed > 10*time.Second {
		t.Errorf("ForType took %s after the caller cancelled at 20ms — the bounded context is not derived from the caller's", elapsed)
	}
}

// TestSources_AreDistinct guards the cheapest way for this package's central
// distinction to evaporate: if two Source constants shared a string literal,
// every test above would still pass.
func TestSources_AreDistinct(t *testing.T) {
	seen := map[Source]string{}
	for name, s := range map[string]Source{
		"SourceDB":           SourceDB,
		"SourceSeed":         SourceSeed,
		"SourceFallbackSeed": SourceFallbackSeed,
	} {
		if s == "" {
			t.Errorf("%s is the empty string — an unset Source would be indistinguishable from it", name)
		}
		if prev, dup := seen[s]; dup {
			t.Errorf("%s and %s are both %q; the source discriminator cannot tell them apart", name, prev, s)
		}
		seen[s] = name
	}
}

// SetFetchTimeoutForTest overrides the fetch bound for the duration of one test.
//
// It lives in the test file, not beside the knob, so the production build does
// not import `testing`.
func SetFetchTimeoutForTest(t *testing.T, d time.Duration) {
	t.Helper()
	prev := fetchTimeout.get()
	fetchTimeout.set(d)
	t.Cleanup(func() { fetchTimeout.set(prev) })
}
