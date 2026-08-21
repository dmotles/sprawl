package store

import (
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Derived event ids, and why they replaced a claim (QUM-1250, code-review fix).
//
// ===========================================================================
// A CLAIM IS AN "ATTEMPTED" MARKER. IT WAS BEING USED AS A "DONE" MARKER.
// ===========================================================================
//
// The dispatcher's own use of event_claims is correct: claim, act, THEN advance
// the cursor, and release on failure so a retry is possible. But the notify
// handler and the sweeper each took a SECOND claim of their own, before their log
// append, and never released it. That inverts the write-ahead discipline the rest
// of this package is built on, and the two markers differ exactly when something
// fails:
//
//	claim -> append FAILS -> retry -> claim is LOST (our own row) -> "another host
//	has this" -> return nil -> the dispatcher records success and advances.
//
// End state: no event, no side effect, cursor advanced, one Debug line blaming a
// host that does not exist. For a notification that is a result that landed
// unobserved with nothing able to discover it; for a poke it is worse, because the
// epoch is DERIVED FROM THE EVENT THAT FAILED TO BE WRITTEN — so the same key is
// retried forever, the goal is never poked again AND never quarantined, and the
// sweep reports it under `Skipped` where it is indistinguishable from the five
// legitimate gates. Both were verified with probes in code review.
//
// THE FIX IS TO STOP NEEDING A CLAIM. Deriving the event's id from the facts that
// identify the work makes the APPEND ITSELF the exclusion mechanism: `events.id`
// is UNIQUE, so a second host inserting the same logical event gets SQLSTATE
// 23505 and knows, from the database rather than from a convention, that somebody
// else already did this. There is no window between the marker and the record
// because THEY ARE THE SAME WRITE.
//
// This is what the issue said would happen ("poke event ids are derived ... so
// events.id UNIQUE refuses a duplicate with 23505 even if a claim were somehow
// granted twice") and what the first implementation did not do — it minted a
// random id and leaned on the claim instead.
//
// CONSEQUENCE, and it is a real one rather than a free win: a crash between the
// append and the side effect now leaves a recorded event whose effect did not
// happen. That is deliberate and it is the recoverable direction:
//
//   - a notification: the owner_notify contract stays OPEN, and the handler
//     re-injects on any pass that finds it open (see notify.go).
//   - a poke: the epoch has advanced, so the next sweep pokes again after that
//     epoch's backoff. One lost poke costs one backoff interval, where the old
//     behaviour cost the goal forever.
//
// THE DERIVATION INPUTS ARE FROZEN, exactly as seeds.go's are: changing the
// namespace, the separator or a kind string changes every id derived from it, so
// a re-run after such a change re-does work that was already done. Add a new kind
// rather than editing one.
var dispatchNamespace = uuid.MustParse("3f6b2c81-9d4a-4e77-b0c5-1a8e2d5f7b34")

// Event-id kinds. Each names a logical action, and the parts that follow it are
// what make two attempts at the SAME action collide and two attempts at
// DIFFERENT actions not.
const (
	// kindOwnerNotify is (subject event, recipient): one notification per
	// landing result per recipient. Two recipients for one result are two
	// notifications, which is the point of keying on both.
	kindOwnerNotify = "owner_notify"
	// kindGoalPoke is (goal, epoch). The epoch is what lets the NEXT poke for
	// the same goal be a different event while making a concurrent second
	// sweeper's poke for the same epoch collide.
	kindGoalPoke = "goal_poke"
	// kindGoalStuck is (goal) alone: a goal is quarantined once, ever.
	kindGoalStuck = "goal_stuck"
	// kindOwnershipReassigned is (contract, new owner): reassigning the same
	// contract to the same fallback twice is one event.
	kindOwnershipReassigned = "ownership_reassigned"
)

// DerivedEventID builds the stable id for one logical action.
//
// uuid.NewSHA1 over "<kind>:<part>:<part>...", the same construction seeds.go
// uses for schema ids and for the same reason: two hosts computing it from the
// same facts get the same answer without talking to each other.
func DerivedEventID(kind string, parts ...string) uuid.UUID {
	return uuid.NewSHA1(dispatchNamespace, []byte(kind+":"+strings.Join(parts, ":")))
}

// IsUniqueViolation reports whether err is Postgres refusing a duplicate key.
//
// This is how a consumer learns "somebody else already recorded this action", and
// it keys on SQLSTATE rather than on message text for the reason this suite keys
// on SQLSTATE everywhere: text is lc_messages-dependent and is satisfiable by
// anything that chooses to print the same words.
//
// NOTE it is deliberately NOT folded into isTransportFailure's classification. A
// unique violation is a PgError, so isTransportFailure already reports it as a
// refusal rather than an outage and it will never be spilled. This asks the
// narrower question: is this refusal the specific, EXPECTED one that means the
// work is already done?
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505"
}
