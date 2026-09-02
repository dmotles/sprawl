package store

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Notification re-delivery (QUM-1252, M3a; the leg QUM-1250 shipped owing).
//
// M1b's file header claims a lost delivery "is swept rather than assumed". It
// was not: nothing swept notifications, and the only retry was the dispatcher
// re-running the same event after the handler returned an error — which meant a
// recipient this host could not reach head-of-line blocked every other event in
// the project behind it. This file is the sweep the header promised, and it lets
// the handler stop blocking.
//
// IT NEEDED A DELIVERY PREDICATE FIRST, and that is the part worth reading
// twice. An open owner_notify contract records UNACKED, not UNDELIVERED:
// NotifyAckHandler closed every open notification for an agent on any
// turn_finished, with no reference to whether an injection had ever succeeded.
// So an agent that was never told anything acked its way out of the contract at
// its next turn, and a re-delivery sweep built on top of that would have found
// nothing to re-deliver. notify_delivered is that predicate, and the ack now
// requires one. (Filed as QUM-1325; fixed here because the sweep is worthless
// without it.)
//
// THE SHAPE IS THE POKE LEG'S, deliberately, because the two problems are the
// same problem: an action that must be retried with backoff, capped, and
// deduplicated across hosts that cannot talk to each other.
//
//	notify_attempt      recorded BEFORE the injection, derived id on
//	                    (notification, epoch). The epoch IS the count of these,
//	                    so the backoff exponent and the dedup key are one number
//	                    and cannot drift.
//	notify_delivered    recorded AFTER a successful injection. The predicate.
//	notify_undelivered  existence-based quarantine at the cap.
//
// RECORD-BEFORE IS NOT A STYLE CHOICE. Because the epoch is a COUNT, a marker
// written only after a SUCCESSFUL action leaves a failed attempt invisible: the
// next pass recomputes an identical key, finds an identical backoff window, and
// either double-delivers or never advances. Losing record-before loses the RATE
// LIMITING, not merely the cap.
//
// WHAT THIS DELIBERATELY DOES NOT DO:
//
//   - No turn/paused/host gates. Injection is how an ordinary message reaches an
//     agent, and the notify handler has always injected unconditionally. The poke
//     leg gates on those because a poke is an unsolicited nudge that can preempt
//     work; a notification the recipient's own contract says it is owed is not.
//   - No owner-death reassignment. NotifyHandler does that at landing time only,
//     and its reassignment leaks a contract nothing can close (QUM-1326). A
//     notification addressed to a dead agent therefore reaches the cap and is
//     quarantined — noisy, bounded, honest, and not this slice's bug to fix.

// Notification re-delivery discipline. Separate constants from the poke leg's
// even where the numbers agree today: they answer different questions ("how
// long before nudging an agent again" versus "how long before retrying a
// delivery"), and sharing them would couple two tuning decisions that have no
// reason to move together.
const (
	// notifyBackoffBase is the wait before the second attempt. Doubling gives
	// 15m, 30m, 1h, 2h, 4h across the five attempts below.
	notifyBackoffBase = 15 * time.Minute
	// maxNotifyAttempts caps re-delivery. A recipient unreachable for the ~7.75
	// hours this spans is not going to be reached by a sixth try, and the
	// quarantine marker is a better signal to an operator than a retry loop.
	maxNotifyAttempts = 5
)

func notifyBackoff(epoch int) time.Duration {
	if epoch < 0 {
		epoch = 0
	}
	return time.Duration(1<<epoch) * notifyBackoffBase
}

// NotifyCandidate is one outstanding notification plus everything the gates
// need, computed by the reader in one query — see openGoalsSQL for why one
// query rather than N round trips per candidate.
type NotifyCandidate struct {
	NotifyEventID uuid.UUID
	WorkflowID    uuid.UUID
	Recipient     string
	// SubjectEventID is the landing result this notification is about, carried
	// so the re-delivered body says the same thing the first one did.
	SubjectEventID string
	Body           string
	OpenedAt       time.Time
	// Attempts is how many notify_attempt events exist. It IS the epoch.
	Attempts int
	// LastAttemptAt is when the most recent attempt happened. Zero if none.
	LastAttemptAt time.Time
	// Delivered is true when a notify_delivered event exists.
	Delivered bool
	// Quarantined is true when a notify_undelivered event exists.
	Quarantined bool
}

// NotifySweepReader produces the candidates.
type NotifySweepReader interface {
	OpenNotifications(ctx context.Context, projectID uuid.UUID) ([]NotifyCandidate, error)
}

// NotifySweeperDeps follows the repo's deps-struct convention.
type NotifySweeperDeps struct {
	Notifications NotifySweepReader
	Emitter       EventEmitter
	Injector      Injector

	ProjectID uuid.UUID
	Host      string
	Now       func() time.Time
	Logger    *slog.Logger
}

// NotifySweepResult is what one pass did. Mirrors SweepResult, including
// SkipReasons — see that type for why exposing the reason is load-bearing
// rather than a convenience.
type NotifySweepResult struct {
	Considered  int
	Delivered   int
	Quarantined int
	Skipped     int
	SkipReasons map[uuid.UUID]string
}

func (r *NotifySweepResult) note(notify uuid.UUID, why string) {
	if r.SkipReasons == nil {
		r.SkipReasons = map[uuid.UUID]string{}
	}
	r.SkipReasons[notify] = why
}

// SweepNotifications re-delivers this project's undelivered notifications.
func SweepNotifications(ctx context.Context, d NotifySweeperDeps) (NotifySweepResult, error) {
	var res NotifySweepResult
	switch {
	case d.Notifications == nil:
		return res, fmt.Errorf("store: notification sweeper needs a reader")
	case d.Emitter == nil:
		return res, fmt.Errorf("store: notification sweeper needs an event emitter")
	case d.Injector == nil:
		return res, fmt.Errorf("store: notification sweeper needs an injector to deliver a notification")
	case d.Host == "":
		return res, fmt.Errorf("store: notification sweeper needs a host identity")
	case d.ProjectID == uuid.Nil:
		return res, fmt.Errorf("store: notification sweeper needs a project id")
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	log := d.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	candidates, err := d.Notifications.OpenNotifications(ctx, d.ProjectID)
	if err != nil {
		return res, fmt.Errorf("store: reading outstanding notifications: %w", err)
	}

	for _, c := range candidates {
		res.Considered++
		if skip, why := notifyGateFor(c, now()); skip {
			res.Skipped++
			res.note(c.NotifyEventID, why)
			log.Debug("not re-delivering", "notify", c.NotifyEventID, "recipient", c.Recipient, "reason", why)
			continue
		}

		// The cap, checked BEFORE the attempt: at the cap nothing further is
		// delivered, ever. Derived id because a notification is quarantined once
		// even when N hosts reach the cap in the same pass.
		if c.Attempts >= maxNotifyAttempts {
			if _, err := d.Emitter.Emit(ctx, EmitRequest{
				TypeName:           "notify_undelivered",
				TypeVersion:        1,
				EventID:            DerivedEventID(kindNotifyUndelivered, c.NotifyEventID.String()),
				WorkflowInstanceID: c.WorkflowID,
				Payload: map[string]any{
					"notify_event_id": c.NotifyEventID.String(),
					"recipient":       c.Recipient,
					"attempts":        c.Attempts,
					"reason": fmt.Sprintf("undelivered after %d attempt(s); no longer retried, and the contract stays open because the result really is unobserved",
						c.Attempts),
					"host": d.Host,
				},
			}); err != nil && !IsUniqueViolation(err) {
				return res, fmt.Errorf("store: quarantining notification %s: %w", c.NotifyEventID, err)
			}
			res.Quarantined++
			log.Warn("notification reached the re-delivery cap; it will not be attempted again",
				"notify", c.NotifyEventID, "recipient", c.Recipient, "attempts", c.Attempts)
			continue
		}

		delivered, err := attemptNotifyDelivery(ctx, d.Emitter, d.Injector, d.Host, notifyDelivery{
			NotifyEventID: c.NotifyEventID,
			WorkflowID:    c.WorkflowID,
			Recipient:     c.Recipient,
			Epoch:         c.Attempts,
			Body:          c.Body,
		})
		switch {
		case err != nil:
			// A RECORDING failure, not a delivery one — the attempt event could
			// not be written, so nothing is known about this notification and
			// continuing would sweep the rest of the project on a store that is
			// not accepting writes.
			return res, fmt.Errorf("store: re-delivering notification %s to %q: %w", c.NotifyEventID, c.Recipient, err)
		case delivered:
			res.Delivered++
			log.Info("re-delivered a notification",
				"notify", c.NotifyEventID, "recipient", c.Recipient, "epoch", c.Attempts)
		default:
			// The injection failed, or another host owns this epoch. Both leave
			// the contract open and the attempt recorded, so the next pass finds
			// a higher epoch and a longer backoff. NOT an error: one unreachable
			// recipient must not stop the other candidates being swept.
			res.Skipped++
			res.note(c.NotifyEventID, "the injection did not succeed; the attempt is recorded and the backoff has advanced")
			log.Warn("re-delivery attempt did not land",
				"notify", c.NotifyEventID, "recipient", c.Recipient, "epoch", c.Attempts)
		}
	}
	return res, nil
}

// notifyGateFor decides whether this candidate is re-delivered, and says why
// not. See gateFor for why the reason is returned rather than logged in place.
func notifyGateFor(c NotifyCandidate, now time.Time) (skip bool, why string) {
	if c.Quarantined {
		return true, "quarantined: a notify_undelivered event already exists for this notification"
	}
	if c.Delivered {
		// Delivered but not yet acked — the ordinary state of a notification
		// waiting for its recipient's next turn boundary, and the majority of
		// what this sweep sees. Re-delivering would send the same notification
		// twice for the crime of the recipient being busy.
		return true, "already delivered; the contract is waiting on the recipient's next turn boundary, not on a delivery"
	}
	if c.Recipient == "" {
		// Should be unreachable — NotifyHandler refuses to open a notification
		// with no recipient — but injecting to "" would be a delivery to nobody
		// recorded as a success.
		return true, "the notification names no recipient, so there is nobody to deliver to"
	}
	if !c.LastAttemptAt.IsZero() {
		if wait := notifyBackoff(c.Attempts - 1); now.Sub(c.LastAttemptAt) < wait {
			return true, fmt.Sprintf("attempted %s ago, inside the %s backoff for epoch %d",
				now.Sub(c.LastAttemptAt).Round(time.Second), wait, c.Attempts)
		}
	}
	return false, ""
}

// notifyDelivery is one attempt's worth of input.
type notifyDelivery struct {
	NotifyEventID uuid.UUID
	WorkflowID    uuid.UUID
	Recipient     string
	Epoch         int
	Body          string
}

// attemptNotifyDelivery records an attempt, injects, and records the delivery.
//
// SHARED BY THE HANDLER AND THE SWEEPER on purpose. Two copies of "write the
// attempt, then inject, then write the delivery" is two chances to get the order
// wrong, and the order is the whole mechanism — see the record-before note in
// this file's header.
//
// Returns (false, nil) when the notification was NOT delivered but the log is
// consistent: either the injection failed, or another host had already claimed
// this epoch. Both are states the next sweep handles, and neither is a reason to
// abandon the pass. A non-nil error means the LOG could not be written, which is.
func attemptNotifyDelivery(ctx context.Context, emitter EventEmitter, injector Injector, host string, n notifyDelivery) (bool, error) {
	_, err := emitter.Emit(ctx, EmitRequest{
		TypeName:           "notify_attempt",
		TypeVersion:        1,
		EventID:            DerivedEventID(kindNotifyAttempt, n.NotifyEventID.String(), strconv.Itoa(n.Epoch)),
		WorkflowInstanceID: n.WorkflowID,
		Payload: map[string]any{
			"notify_event_id": n.NotifyEventID.String(),
			"recipient":       n.Recipient,
			"epoch":           n.Epoch,
			"host":            host,
		},
	})
	switch {
	case err == nil:
	case IsUniqueViolation(err):
		// Another host is delivering this epoch right now. Injecting anyway would
		// be the double-delivery the derived id exists to prevent.
		return false, nil
	default:
		return false, fmt.Errorf("recording delivery attempt %d: %w", n.Epoch, err)
	}

	if err := injector.Inject(ctx, n.Recipient, n.Body); err != nil {
		// The attempt event STAYS, for the poke leg's reason: the backoff must
		// advance whether or not the delivery landed, or an unreachable recipient
		// is retried at the base interval forever.
		return false, nil
	}

	// AFTER the injection, and only on success. This is the predicate the ack
	// reads, so writing it optimistically would restore the exact defect the
	// predicate exists to fix.
	if _, err := emitter.Emit(ctx, EmitRequest{
		TypeName:           "notify_delivered",
		TypeVersion:        1,
		EventID:            DerivedEventID(kindNotifyDelivered, n.NotifyEventID.String()),
		WorkflowInstanceID: n.WorkflowID,
		Payload: map[string]any{
			"notify_event_id": n.NotifyEventID.String(),
			"recipient":       n.Recipient,
			"epoch":           n.Epoch,
			"host":            host,
		},
	}); err != nil && !IsUniqueViolation(err) {
		// The injection HAPPENED. Reporting undelivered would be false, but so
		// would reporting delivered when the predicate the ack reads is missing:
		// without it the recipient can never close the contract, and the sweep
		// will re-deliver. The error is the honest answer — the caller stops the
		// pass on a store that is not accepting writes.
		return false, fmt.Errorf("recording the delivery of %s to %q (it WAS injected; the missing record means it will be delivered again): %w",
			n.NotifyEventID, n.Recipient, err)
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// The reader
// ---------------------------------------------------------------------------

// openNotificationsSQL assembles one NotifyCandidate per outstanding
// notification, project-wide. One query, correlated subqueries — see
// openGoalsSQL's header for both decisions.
const openNotificationsSQL = `
	SELECT
	    n.id,
	    n.workflow_instance_id,
	    COALESCE(n.payload->>'recipient', '')       AS recipient,
	    COALESCE(n.payload->>'subject_event_id', '') AS subject_event_id,
	    n.at,
	    (SELECT count(*) FROM events a
	      WHERE a.project_id = n.project_id
	        AND a.schema_id = ANY($2)
	        AND a.payload->>'notify_event_id' = n.id::text)      AS attempts,
	    (SELECT max(a.at) FROM events a
	      WHERE a.project_id = n.project_id
	        AND a.schema_id = ANY($2)
	        AND a.payload->>'notify_event_id' = n.id::text)      AS last_attempt_at,
	    EXISTS (SELECT 1 FROM events d
	             WHERE d.project_id = n.project_id
	               AND d.schema_id = ANY($3)
	               AND d.payload->>'notify_event_id' = n.id::text) AS delivered,
	    EXISTS (SELECT 1 FROM events q
	             WHERE q.project_id = n.project_id
	               AND q.schema_id = ANY($4)
	               AND q.payload->>'notify_event_id' = n.id::text) AS quarantined
	  FROM open_contracts oc
	  JOIN events n ON n.id = oc.event_id
	 WHERE n.project_id = $1
	   AND n.schema_id = ANY($5)
	 ORDER BY n.seq`

// PgNotifySweepReader produces re-delivery candidates through a pgx pool.
type PgNotifySweepReader struct {
	Pool interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	}
	Registry *Registry
}

var _ NotifySweepReader = (*PgNotifySweepReader)(nil)

func (r *PgNotifySweepReader) OpenNotifications(ctx context.Context, projectID uuid.UUID) ([]NotifyCandidate, error) {
	rows, err := r.Pool.Query(ctx, openNotificationsSQL,
		projectID,
		schemaIDsFor(r.Registry, "notify_attempt"),
		schemaIDsFor(r.Registry, "notify_delivered"),
		schemaIDsFor(r.Registry, "notify_undelivered"),
		schemaIDsFor(r.Registry, "owner_notify"),
	)
	if err != nil {
		return nil, fmt.Errorf("store: reading outstanding notifications for the sweeper: %w", err)
	}
	defer rows.Close()

	var out []NotifyCandidate
	for rows.Next() {
		var (
			c           NotifyCandidate
			lastAttempt *time.Time
		)
		if err := rows.Scan(
			&c.NotifyEventID, &c.WorkflowID, &c.Recipient, &c.SubjectEventID, &c.OpenedAt,
			&c.Attempts, &lastAttempt, &c.Delivered, &c.Quarantined,
		); err != nil {
			return nil, fmt.Errorf("store: scanning a re-delivery candidate: %w", err)
		}
		if lastAttempt != nil {
			c.LastAttemptAt = *lastAttempt
		}
		c.Body = resweepNotifyBody(c)
		out = append(out, c)
	}
	return out, rows.Err()
}

// resweepNotifyBody is the re-delivery body.
//
// Rebuilt from the contract rather than replayed from the original send, because
// the original body was never stored and reading the two source events back to
// reconstruct it byte-for-byte would be two more queries per candidate for a
// cosmetic match. It names the same subject event, which is the part the
// recipient acts on.
func resweepNotifyBody(c NotifyCandidate) string {
	subject := c.SubjectEventID
	if subject == "" {
		subject = c.NotifyEventID.String()
	}
	return truncateRunes(fmt.Sprintf(
		"A result landed for a contract you own and the first delivery did not reach you. Read it with get_workflow_log or search_log: event %s",
		subject), notifyBodyMaxRunes)
}
