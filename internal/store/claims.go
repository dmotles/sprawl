package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Event claims — the mechanism the whole dispatch layer's exactly-once rests on
// (Appendix B item 1, QUM-1250).
//
// A consumer that is about to do something irreversible — spawn an agent, inject
// a notification, poke a goal — first inserts a row into `event_claims`, keyed
// `(event_id, consumer)`. The insert is conditional, so exactly one caller across
// the whole fleet gets a row and every other caller is told, politely, that it
// lost. The winner acts; the losers skip.
//
// WHY THE DATABASE AND NOT A LOCK. A lock has to be held for the duration of the
// work and released by someone. The work here is "create a git worktree and
// launch a Claude session", which can take tens of seconds and can die halfway.
// A row with a lease survives the process that wrote it, which a lock does not,
// and it leaves an audit trail of WHICH host acted, which a lock also does not.
//
// THE UNCOMFORTABLE PART, stated plainly because it is the design's one
// deliberate weakening and a reader will otherwise assume it away:
//
//	TakeoverExpired trades exactly-once for liveness.
//
// If a host claims an event and dies before finishing, the claim row outlives it
// and nothing else may act — the event wedges forever. The lease is the escape:
// once it expires, another host may take the claim over. But an expired lease
// does NOT prove the original claimer is dead; it may merely have been slow, or
// partitioned. So a takeover can genuinely run a second handler for one event.
//
// That is survivable ONLY because of Appendix B item 2, and the two mechanisms
// are a pair rather than two independent features: SPAWN_INTENT is appended
// under the claim BEFORE any local resource is created, so a second handler
// arriving after a takeover finds the intent and reconciles against it — adopt
// what exists, GC what does not match, emit SPAWN_FAILED for a traceless
// intent — instead of blindly spawning again. Renew exists so a handler doing
// slow-but-healthy work never reaches that path at all.
//
// If you are adding a new consumer whose side effect is NOT re-checkable that
// way, it is not safe under takeover, and the answer is to make the side effect
// re-checkable rather than to widen the lease.

// ClaimStore is the claim surface the dispatcher and the sweeper share.
type ClaimStore interface {
	// Claim attempts to take (eventID, consumer) for host. It returns true only
	// if this caller inserted the row. It NEVER steals a claim another host
	// holds, however old that claim is — that is TakeoverExpired's job, and
	// keeping them separate is what stops a caller getting steal semantics by
	// accident.
	Claim(ctx context.Context, eventID uuid.UUID, consumer, host string, lease time.Duration) (bool, error)
	// Renew extends the lease, but only for the host that already holds the
	// claim. A handler doing slow work calls this so it is not taken over
	// mid-flight; a host whose Renew returns false has been taken over and must
	// stop.
	Renew(ctx context.Context, eventID uuid.UUID, consumer, host string, lease time.Duration) (bool, error)
	// TakeoverExpired transfers an EXPIRED claim to host. Returns false if the
	// lease is still live. See the header: this is the deliberate at-least-once
	// escape hatch, and it is only safe for a re-checkable side effect.
	TakeoverExpired(ctx context.Context, eventID uuid.UUID, consumer, host string, lease time.Duration) (bool, error)
	// Release drops this host's own claim, so the event becomes claimable
	// immediately rather than after the lease runs out. For a handler that
	// failed cleanly and knows it did nothing.
	Release(ctx context.Context, eventID uuid.UUID, consumer, host string) error
}

// PgClaimStore is the Postgres implementation.
//
// It writes through the same narrow PgPool seam the appender uses, so claim
// behaviour is assertable without a database (claims_test.go) and the statement
// shapes stay pinned.
type PgClaimStore struct {
	Pool PgPool
}

var _ ClaimStore = (*PgClaimStore)(nil)

// DefaultClaimLease is how long a claim is held before another host may take it
// over.
//
// Sized against the SLOWEST side effect a claim covers, which is a spawn:
// creating a git worktree plus launching a session. Too short and a healthy host
// doing real work gets taken over mid-spawn (survivable, via reconcile, but it
// burns a worktree and a reconcile pass every time). Too long and a genuinely
// crashed host's events sit undispatched for that whole window. A handler that
// expects to exceed it calls Renew rather than having this raised.
const DefaultClaimLease = 5 * time.Minute

// ALL FOUR STATEMENTS DERIVE THEIR DEADLINE FROM now(), THE SERVER'S CLOCK, and
// none of them accepts a timestamp as a parameter.
//
// This is not a style preference, and it is the single easiest thing in this file
// to get wrong invisibly: computing `time.Now().Add(lease)` in Go and passing it
// as an argument writes a row that looks perfectly correct against one database,
// and every test fixture has one database. It breaks only in the configuration
// the design exists to serve — several hosts — and it breaks badly. A host whose
// clock runs ten minutes fast writes deadlines that are already past by every
// other reader's reckoning, so it steals every other host's live claims, and two
// handlers run on the same event with nothing anywhere reporting an error. The
// server clock is the only clock all hosts agree on.
//
// make_interval(secs => $n) rather than string-concatenating an interval literal:
// the lease is a parameter, and building `'... seconds'::interval` by
// concatenation would put a caller-supplied value into statement text.
const (
	claimInsertSQL = `
		INSERT INTO event_claims (event_id, consumer, host, claimed_at, lease_expires)
		VALUES ($1, $2, $3, now(), now() + make_interval(secs => $4::double precision))
		ON CONFLICT (event_id, consumer) DO NOTHING`

	claimRenewSQL = `
		UPDATE event_claims
		   SET lease_expires = now() + make_interval(secs => $4::double precision)
		 WHERE event_id = $1 AND consumer = $2 AND host = $3`

	// The `lease_expires < now()` predicate IS the safety of this statement.
	// Without it this is an unconditional steal and event_claims stops meaning
	// anything at all.
	//
	// A NULL lease_expires is deliberately NOT taken over: the column is
	// nullable in the schema, `NULL < now()` is NULL rather than true, so a row
	// written without a deadline is treated as held forever rather than as
	// instantly stealable. Failing toward "nobody may act" is the right
	// direction — it wedges one event visibly instead of letting two hosts act
	// on it silently.
	claimTakeoverSQL = `
		UPDATE event_claims
		   SET host = $3,
		       claimed_at = now(),
		       lease_expires = now() + make_interval(secs => $4::double precision)
		 WHERE event_id = $1 AND consumer = $2 AND lease_expires < now()`

	claimReleaseSQL = `
		DELETE FROM event_claims
		 WHERE event_id = $1 AND consumer = $2 AND host = $3`
)

// checkClaimArgs validates the identifying arguments shared by all four verbs.
//
// Each refusal here is a real failure shape rather than defensive padding:
//
//   - uuid.Nil is what an unset struct field yields. Written, it produces ONE
//     claim row shared by every event whose id nobody set, so the first such
//     event is handled and every later one is skipped as "already claimed" —
//     indistinguishable from correct operation.
//   - An empty consumer merges two consumers' claims, because consumer is half
//     the primary key. The dispatcher and the notifier would each silently
//     suppress events the other had taken.
//   - An empty host breaks every ownership predicate in this file: Renew and
//     Release are scoped by host, so a claim owned by "" is renewable and
//     releasable by any caller that also passes "".
func checkClaimArgs(eventID uuid.UUID, consumer, host string) error {
	if eventID == uuid.Nil {
		return fmt.Errorf("store: refusing to claim the nil event id: one shared claim row would make every event with an unset id look already-claimed")
	}
	if consumer == "" {
		return fmt.Errorf("store: refusing a claim with an empty consumer: consumer is half the claim's primary key, so an empty one merges different consumers' claims")
	}
	if host == "" {
		return fmt.Errorf("store: refusing a claim with an empty host: renew and release are scoped by host, so a claim owned by %q is renewable by any caller", "")
	}
	return nil
}

func checkLease(lease time.Duration) error {
	if lease <= 0 {
		return fmt.Errorf("store: refusing a claim lease of %v: the claim would be expired on arrival and immediately stealable by the next host to look at the event", lease)
	}
	return nil
}

// Claim takes the claim if nobody holds it. See ClaimStore.Claim.
//
// A TRANSPORT FAILURE IS RETURNED, never folded into a false. Both look like
// "false" to a careless implementation, and the difference is the difference
// between a transient outage and a permanently lost event: a dispatcher told
// "you lost the race" skips the event and ADVANCES ITS CURSOR PAST IT, and since
// no claim row exists, no lease expires and no takeover ever brings it back.
func (s *PgClaimStore) Claim(ctx context.Context, eventID uuid.UUID, consumer, host string, lease time.Duration) (bool, error) {
	if err := checkClaimArgs(eventID, consumer, host); err != nil {
		return false, err
	}
	if err := checkLease(lease); err != nil {
		return false, err
	}
	tag, err := s.Pool.Exec(ctx, claimInsertSQL, eventID, consumer, host, lease.Seconds())
	if err != nil {
		return false, fmt.Errorf("store: claiming event %s for consumer %q: %w", eventID, consumer, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Renew extends this host's own lease. See ClaimStore.Renew.
func (s *PgClaimStore) Renew(ctx context.Context, eventID uuid.UUID, consumer, host string, lease time.Duration) (bool, error) {
	if err := checkClaimArgs(eventID, consumer, host); err != nil {
		return false, err
	}
	if err := checkLease(lease); err != nil {
		return false, err
	}
	tag, err := s.Pool.Exec(ctx, claimRenewSQL, eventID, consumer, host, lease.Seconds())
	if err != nil {
		return false, fmt.Errorf("store: renewing the claim on event %s for consumer %q: %w", eventID, consumer, err)
	}
	return tag.RowsAffected() == 1, nil
}

// TakeoverExpired transfers an expired claim. See ClaimStore.TakeoverExpired and
// the file header on why this is safe only for a re-checkable side effect.
func (s *PgClaimStore) TakeoverExpired(ctx context.Context, eventID uuid.UUID, consumer, host string, lease time.Duration) (bool, error) {
	if err := checkClaimArgs(eventID, consumer, host); err != nil {
		return false, err
	}
	if err := checkLease(lease); err != nil {
		return false, err
	}
	tag, err := s.Pool.Exec(ctx, claimTakeoverSQL, eventID, consumer, host, lease.Seconds())
	if err != nil {
		return false, fmt.Errorf("store: taking over the expired claim on event %s for consumer %q: %w", eventID, consumer, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Release drops this host's own claim.
//
// A Release that matched nothing is NOT an error: the caller either never held
// the claim or was already taken over, and in both cases the desired end state
// (this host does not hold it) is the actual state. Returning an error here would
// make every cleanly-failed handler log a second, misleading failure.
func (s *PgClaimStore) Release(ctx context.Context, eventID uuid.UUID, consumer, host string) error {
	if err := checkClaimArgs(eventID, consumer, host); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, claimReleaseSQL, eventID, consumer, host); err != nil {
		return fmt.Errorf("store: releasing the claim on event %s for consumer %q: %w", eventID, consumer, err)
	}
	return nil
}
