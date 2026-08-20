//go:build store_pg

package store

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres semantics of the event claim (QUM-1250, M1b).
//
// These assert what only a real database can: that ON CONFLICT DO NOTHING on the
// composite primary key really does admit exactly one winner, that the lease
// predicate really is evaluated against the server clock, and that the four
// verbs are actually GRANTED to the least-privilege role rather than merely
// mentioned in a migration comment.
//
// The hermetic half (claims_test.go) covers the statement shapes and the two
// properties a single-database fixture cannot see — clock source and transport
// failure — and is where the reasoning for each lives.

// claimFixture is a migrated schema plus a registered project and one appended
// event to claim. Claims reference events(id), so a claim test needs a real
// event: asserting on a fabricated uuid would pass while proving nothing about
// the foreign key.
type claimFixture struct {
	pool    *pgxpool.Pool
	store   *PgClaimStore
	eventID uuid.UUID
}

func newClaimFixture(t *testing.T) *claimFixture {
	t.Helper()
	_, pool := newTestSchema(t)
	ctx := context.Background()

	projectID, _, err := ensureProject(ctx, pool, "https://example.invalid/claims.git")
	if err != nil {
		t.Fatalf("ensureProject: %v", err)
	}
	reg := testRegistry(t)
	app := NewAppender(AppenderDeps{Pool: pool, Registry: reg})
	schema, ok := reg.ByName("run_started", 1)
	if !ok {
		t.Fatal("run_started@1 missing from the seed registry")
	}
	id := uuid.New()
	if _, err := app.Append(ctx, Event{
		ID:                 id,
		ProjectID:          projectID,
		WorkflowInstanceID: uuid.New(),
		SchemaID:           schema.ID,
		Payload:            []byte(`{"agent_name":"alice","agent_type":"engineer","session_id":"s1"}`),
	}); err != nil {
		t.Fatalf("seeding an event to claim: %v", err)
	}
	return &claimFixture{pool: pool, store: &PgClaimStore{Pool: pool}, eventID: id}
}

// expire forces the claim's lease into the past, deterministically.
//
// A short lease plus a sleep would be the obvious alternative and is worse in
// both directions: it makes the test slow, and it makes it flaky under load in
// the direction of a FALSE GREEN for the takeover-refused case (a lease meant to
// still be live has quietly expired while the test was descheduled). Moving the
// deadline explicitly means the precondition is a fact rather than a race.
func (f *claimFixture) expire(t *testing.T, consumer string) {
	t.Helper()
	tag, err := f.pool.Exec(context.Background(),
		`UPDATE event_claims SET lease_expires = now() - interval '1 second'
		 WHERE event_id = $1 AND consumer = $2`, f.eventID, consumer)
	if err != nil {
		t.Fatalf("expiring the lease: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("expiring the lease changed %d rows, want 1 — the claim this test depends on does not exist, so whatever it asserts next is vacuous", tag.RowsAffected())
	}
}

func (f *claimFixture) claimRow(t *testing.T, consumer string) (host string, leaseExpires time.Time) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT host, lease_expires FROM event_claims WHERE event_id = $1 AND consumer = $2`,
		f.eventID, consumer).Scan(&host, &leaseExpires); err != nil {
		t.Fatalf("reading the claim row: %v", err)
	}
	return host, leaseExpires
}

// ---------------------------------------------------------------------------
// The core guarantee
// ---------------------------------------------------------------------------

// Exactly one claimant per (event_id, consumer). This is Appendix B item 1.
func TestClaims_SecondClaimForTheSameConsumerLoses(t *testing.T) {
	f := newClaimFixture(t)
	ctx := context.Background()

	first, err := f.store.Claim(ctx, f.eventID, "dispatcher", "host-a", 30*time.Second)
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	if !first {
		t.Fatal("the first Claim on a fresh event lost; nothing would ever be dispatched")
	}

	second, err := f.store.Claim(ctx, f.eventID, "dispatcher", "host-b", 30*time.Second)
	if err != nil {
		t.Fatalf("second Claim returned an error rather than a polite refusal: %v", err)
	}
	if second {
		t.Error("a second host also won the claim, so both would act on this event")
	}

	host, _ := f.claimRow(t, "dispatcher")
	if host != "host-a" {
		t.Errorf("claim is held by %q, want host-a — the loser overwrote the winner", host)
	}
}

// POSITIVE CONTROL for the assertion above, and it is the one that proves the
// mechanism rather than the outcome.
//
// Direction: this runs the claim insert WITHOUT its ON CONFLICT guard against a
// subject where the defect IS present, so the probe MUST fire — the second
// insert must be refused by Postgres with a unique violation. If this ever comes
// back with no error, the composite primary key is gone and the test above is
// passing for some other reason (a Go-side cache, a WHERE NOT EXISTS, a
// coincidence), not because the database is enforcing exactly-once.
//
// Keyed on SQLSTATE 23505 rather than on message text, per this suite's
// convention: text is lc_messages-dependent and is satisfiable by anything that
// chooses to print the same words.
func TestClaims_TheCompositePrimaryKeyIsWhatRefusesTheDuplicate(t *testing.T) {
	f := newClaimFixture(t)
	ctx := context.Background()

	unguarded := `INSERT INTO event_claims (event_id, consumer, host, claimed_at, lease_expires)
	              VALUES ($1, $2, $3, now(), now() + interval '30 seconds')`

	if _, err := f.pool.Exec(ctx, unguarded, f.eventID, "dispatcher", "host-a"); err != nil {
		t.Fatalf("first unguarded insert: %v", err)
	}
	_, err := f.pool.Exec(ctx, unguarded, f.eventID, "dispatcher", "host-b")
	if err == nil {
		t.Fatal("a second unguarded insert for the same (event_id, consumer) SUCCEEDED; the primary key that makes claims exactly-once is not enforcing anything")
	}
	if got := pgCode(err); got != sqlStateUniqueViolation {
		t.Errorf("second unguarded insert failed with SQLSTATE %q, want %q (unique violation): %v", got, sqlStateUniqueViolation, err)
	}
}

// Different CONSUMERS claim the same event independently — the key is composite.
//
// This is the negative control for the exactly-once assertion: a store that
// keyed on event_id alone would satisfy "a second claim loses" perfectly while
// making the dispatcher and the notifier mutually exclusive, so whichever
// consumer reached an event first would silently suppress the other.
func TestClaims_DifferentConsumersClaimTheSameEventIndependently(t *testing.T) {
	f := newClaimFixture(t)
	ctx := context.Background()

	for _, consumer := range []string{"dispatcher", "notify:alice", "sweeper.poke:3"} {
		won, err := f.store.Claim(ctx, f.eventID, consumer, "host-a", 30*time.Second)
		if err != nil {
			t.Fatalf("Claim for consumer %q: %v", consumer, err)
		}
		if !won {
			t.Errorf("consumer %q could not claim an event another consumer holds; the claim key is not composite", consumer)
		}
	}
}

// The lease deadline is the SERVER's now() plus the lease, within a tolerance
// wide enough to absorb a round trip and narrow enough to catch a wrong clock.
//
// The hermetic half asserts the STATEMENT derives it from now(); this asserts
// the resulting ROW agrees with the server's clock. Neither alone is enough: the
// statement check passes an implementation that mentions now() and then
// overwrites the column, and this check alone passes a Go-computed deadline on
// any host whose clock happens to be correct — which is every developer laptop,
// and none of the fleet under a skew incident.
func TestClaims_LeaseExpiresTracksTheServerClock(t *testing.T) {
	f := newClaimFixture(t)
	ctx := context.Background()

	const lease = 47 * time.Second
	if _, err := f.store.Claim(ctx, f.eventID, "dispatcher", "host-a", lease); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	var serverNow, leaseExpires time.Time
	if err := f.pool.QueryRow(ctx,
		`SELECT now(), lease_expires FROM event_claims WHERE event_id = $1 AND consumer = $2`,
		f.eventID, "dispatcher").Scan(&serverNow, &leaseExpires); err != nil {
		t.Fatalf("reading now() and lease_expires together: %v", err)
	}

	remaining := leaseExpires.Sub(serverNow)
	const tolerance = 5 * time.Second
	if remaining < lease-tolerance || remaining > lease+tolerance {
		t.Errorf("lease_expires is %v ahead of the server clock, want ~%v — the deadline was not computed by the database", remaining, lease)
	}
}

// ---------------------------------------------------------------------------
// Lease takeover: the deliberate at-least-once escape hatch
// ---------------------------------------------------------------------------

// A LIVE lease is never taken over. Negative control for the takeover pair.
func TestClaims_TakeoverRefusesALiveLease(t *testing.T) {
	f := newClaimFixture(t)
	ctx := context.Background()

	if _, err := f.store.Claim(ctx, f.eventID, "dispatcher", "host-a", time.Hour); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	took, err := f.store.TakeoverExpired(ctx, f.eventID, "dispatcher", "host-b", 30*time.Second)
	if err != nil {
		t.Fatalf("TakeoverExpired: %v", err)
	}
	if took {
		t.Error("host-b took over a lease with an hour left on it; the holder is still working and two handlers now run on one event")
	}
	if host, _ := f.claimRow(t, "dispatcher"); host != "host-a" {
		t.Errorf("the claim's host is now %q, want host-a — a refused takeover still mutated the row", host)
	}
}

// An EXPIRED lease IS taken over, and the row records the new owner.
//
// POSITIVE CONTROL for the test above: same fixture, same call, only the lease
// deadline differs. Without this leg, an implementation whose takeover never
// succeeds would satisfy "a live lease is never taken over" perfectly — and a
// crashed host's events would then wedge forever, which is the failure the lease
// exists to prevent.
//
// Recording the new host matters beyond bookkeeping: it is the only trace that
// the original claimer died, and the dispatcher's own diagnostics read it.
func TestClaims_TakeoverSucceedsOnAnExpiredLeaseAndRecordsTheNewHost(t *testing.T) {
	f := newClaimFixture(t)
	ctx := context.Background()

	if _, err := f.store.Claim(ctx, f.eventID, "dispatcher", "host-a", 30*time.Second); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	f.expire(t, "dispatcher")

	took, err := f.store.TakeoverExpired(ctx, f.eventID, "dispatcher", "host-b", time.Hour)
	if err != nil {
		t.Fatalf("TakeoverExpired: %v", err)
	}
	if !took {
		t.Fatal("host-b could not take over an EXPIRED lease; a crashed claimer's events would wedge forever")
	}

	host, expires := f.claimRow(t, "dispatcher")
	if host != "host-b" {
		t.Errorf("claim host is %q after takeover, want host-b", host)
	}
	if !expires.After(time.Now()) {
		t.Error("the taken-over claim's lease is still in the past, so the next host will immediately steal it too")
	}
}

// After a takeover the ORIGINAL host can no longer renew.
//
// This is what makes takeover safe against a host that was slow rather than
// dead: it comes back, tries to extend its lease, and is told it no longer owns
// the claim — which is a signal it can act on, instead of two hosts both
// believing they hold the event.
func TestClaims_TakeoverStopsTheOriginalHostFromRenewing(t *testing.T) {
	f := newClaimFixture(t)
	ctx := context.Background()

	if _, err := f.store.Claim(ctx, f.eventID, "dispatcher", "host-a", 30*time.Second); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	f.expire(t, "dispatcher")
	if took, err := f.store.TakeoverExpired(ctx, f.eventID, "dispatcher", "host-b", time.Hour); err != nil || !took {
		t.Fatalf("TakeoverExpired: took=%v err=%v", took, err)
	}

	renewed, err := f.store.Renew(ctx, f.eventID, "dispatcher", "host-a", time.Hour)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if renewed {
		t.Error("host-a renewed a claim it no longer owns; it has no way to learn it was taken over")
	}
}

// ---------------------------------------------------------------------------
// Renew / Release
// ---------------------------------------------------------------------------

// The owning host renews; a non-owner does not. Both legs, one fixture.
//
// Renew exists so a handler doing slow work (creating a worktree, launching a
// session) is not taken over mid-flight. Without the owner leg it is useless;
// without the non-owner leg it is an unconditional lease-extension primitive any
// host can use to freeze an event forever.
func TestClaims_RenewIsScopedToTheOwner(t *testing.T) {
	f := newClaimFixture(t)
	ctx := context.Background()

	if _, err := f.store.Claim(ctx, f.eventID, "dispatcher", "host-a", 30*time.Second); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	_, before := f.claimRow(t, "dispatcher")

	stranger, err := f.store.Renew(ctx, f.eventID, "dispatcher", "host-b", time.Hour)
	if err != nil {
		t.Fatalf("Renew as a non-owner: %v", err)
	}
	if stranger {
		t.Error("host-b renewed host-a's claim")
	}
	if _, after := f.claimRow(t, "dispatcher"); !after.Equal(before) {
		t.Errorf("a refused Renew moved the deadline from %v to %v", before, after)
	}

	owner, err := f.store.Renew(ctx, f.eventID, "dispatcher", "host-a", time.Hour)
	if err != nil {
		t.Fatalf("Renew as the owner: %v", err)
	}
	if !owner {
		t.Fatal("the owning host could not renew its own claim, so slow work is always taken over mid-flight")
	}
	if _, after := f.claimRow(t, "dispatcher"); !after.After(before) {
		t.Errorf("the owner's Renew did not extend the deadline: %v -> %v", before, after)
	}
}

// Release drops the owner's claim and makes the event claimable again; a
// non-owner's Release changes nothing.
//
// Release is how a handler that failed cleanly hands the event back IMMEDIATELY
// rather than making the whole fleet wait out the lease. The non-owner leg is
// the safety half: without it, any host can drop a claim another host is
// actively working under, and a second handler starts on the same event.
func TestClaims_ReleaseIsScopedToTheOwner(t *testing.T) {
	f := newClaimFixture(t)
	ctx := context.Background()

	if _, err := f.store.Claim(ctx, f.eventID, "dispatcher", "host-a", time.Hour); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if err := f.store.Release(ctx, f.eventID, "dispatcher", "host-b"); err != nil {
		t.Fatalf("Release as a non-owner returned an error rather than doing nothing: %v", err)
	}
	if host, _ := f.claimRow(t, "dispatcher"); host != "host-a" {
		t.Fatalf("a non-owner's Release disturbed the claim (host is now %q)", host)
	}
	// A non-owner's Release must not have made the event claimable.
	if won, err := f.store.Claim(ctx, f.eventID, "dispatcher", "host-c", time.Hour); err != nil {
		t.Fatalf("Claim after a non-owner Release: %v", err)
	} else if won {
		t.Fatal("host-c claimed an event still held by host-a; a non-owner's Release freed it")
	}

	if err := f.store.Release(ctx, f.eventID, "dispatcher", "host-a"); err != nil {
		t.Fatalf("Release as the owner: %v", err)
	}
	won, err := f.store.Claim(ctx, f.eventID, "dispatcher", "host-c", time.Hour)
	if err != nil {
		t.Fatalf("Claim after the owner released: %v", err)
	}
	if !won {
		t.Error("the event is still unclaimable after its owner released it, so a cleanly-failed handler wedges the event for the whole lease")
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

// Under real contention, exactly one claimant wins. The seed of AC2.
//
// This is a genuinely concurrent probe against one database, not a sequential
// one dressed up: every goroutine waits on the same barrier before issuing its
// insert, so they contend inside Postgres rather than queueing politely in Go.
func TestClaims_ConcurrentClaimantsProduceExactlyOneWinner(t *testing.T) {
	f := newClaimFixture(t)

	const claimants = 16
	var (
		wins    atomic.Int64
		wg      sync.WaitGroup
		barrier = make(chan struct{})
		errs    = make(chan error, claimants)
	)
	for i := 0; i < claimants; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-barrier
			won, err := f.store.Claim(context.Background(), f.eventID, "dispatcher",
				fmt.Sprintf("host-%02d", i), 30*time.Second)
			if err != nil {
				errs <- err
				return
			}
			if won {
				wins.Add(1)
			}
		}(i)
	}
	close(barrier)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("a claimant failed rather than losing politely: %v", err)
	}
	if got := wins.Load(); got != 1 {
		t.Errorf("%d of %d concurrent claimants won, want exactly 1", got, claimants)
	}

	var rows int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM event_claims WHERE event_id = $1 AND consumer = $2`,
		f.eventID, "dispatcher").Scan(&rows); err != nil {
		t.Fatalf("counting claim rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("event_claims holds %d rows for one (event_id, consumer), want 1", rows)
	}
}

// ---------------------------------------------------------------------------
// Grants
// ---------------------------------------------------------------------------

// All four verbs work as the least-privilege application role.
//
// 00002_m1a_app_role.sql grants "event_claims (M1b): claim, renew a lease,
// release. All four verbs." — a COMMENT, and this is the assertion that turns it
// into a checked fact. Every other test in this file connects as the schema
// owner, which bypasses every grant, so without this one the whole file could be
// green against a deployment where the dispatcher cannot claim anything.
//
// A missing grant surfaces as 42501 at the FIRST claim in production, which is
// the same class of defect M1a's review found in Open(): a privilege the code
// needs and the role does not hold, invisible to every owner-connected test.
func TestClaims_AllFourVerbsWorkAsTheApplicationRole(t *testing.T) {
	f := newClaimFixture(t)

	asAppRole(t, f.pool, func(ctx context.Context, conn *pgx.Conn) {
		// connPool (appender_integration_test.go) issues the statements on the
		// SET ROLE connection. Going through f.pool would silently defeat this
		// test: pgxpool hands out whichever connection it likes, and the role
		// was only assumed on the one asAppRole prepared.
		s := &PgClaimStore{Pool: connPool{conn}}

		won, err := s.Claim(ctx, f.eventID, "dispatcher", "host-a", 30*time.Second)
		if err != nil {
			t.Fatalf("Claim as %s: %v (SQLSTATE %q)", appRole, err, pgCode(err))
		}
		if !won {
			t.Fatal("Claim as the application role reported a loss on a fresh event")
		}
		if _, err := s.Renew(ctx, f.eventID, "dispatcher", "host-a", time.Hour); err != nil {
			t.Fatalf("Renew as %s: %v (SQLSTATE %q)", appRole, err, pgCode(err))
		}
		if _, err := s.TakeoverExpired(ctx, f.eventID, "dispatcher", "host-b", time.Hour); err != nil {
			t.Fatalf("TakeoverExpired as %s: %v (SQLSTATE %q)", appRole, err, pgCode(err))
		}
		if err := s.Release(ctx, f.eventID, "dispatcher", "host-a"); err != nil {
			t.Fatalf("Release as %s: %v (SQLSTATE %q)", appRole, err, pgCode(err))
		}
	})
}
