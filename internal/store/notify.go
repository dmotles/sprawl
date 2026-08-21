package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/dmotles/sprawl/internal/state"

	"github.com/google/uuid"
)

// Notification delivery (QUM-1250, M1b): THE EVENT LOG IS THE NOTIFICATION QUEUE.
//
// When a result lands for a contract somebody owns, that owner has to find out.
// The plan of record makes the notification itself an open/close pair —
// OWNER_NOTIFY opens on the landing, NOTIFY_ACKED closes it at the recipient's
// next turn boundary — for one reason: SO A LOST DELIVERY IS SWEPT RATHER THAN
// ASSUMED. Injecting into a live process is best-effort by nature; a contract
// makes the best-effort part observable.
//
// ORDER, which mirrors the spawn write-ahead and is load-bearing for the same
// reason with the opposite consequence:
//
//	claim  ->  append owner_notify  ->  inject
//
// The claim first because injection is a side effect and two hosts must not both
// do it. The append BEFORE the injection because a failed injection then leaves
// an open contract the sweeper can find, where the reverse order delivers with no
// trace and a half-happened delivery is indistinguishable from one that never
// started.
//
// WHAT IS REUSED AND WHAT IS NOT, because the issue is specific about this:
// delivery reuses the existing hardened INJECTION leg — Real.SendMessage, which
// carries the QUM-1066 duplicate filter, the QUM-1072 bounded per-frame write
// that stopped one wedged recipient hanging an unrelated agent's MCP call, and
// the idempotent ack — and NOT the maildir as the authoritative queue. The
// residual, stated plainly rather than glossed: because Real.SendMessage writes
// a maildir envelope and a queue entry on the way through, the maildir is still
// the local durable buffer for an in-flight delivery. It is a BUFFER and no
// longer the record: the owner_notify contract is, so losing the buffer costs a
// re-delivery rather than a lost notification. Retiring the on-disk storage is a
// later issue, once parity is proven.
//
// THE ACK ARRIVES FROM THE LOG, NOT FROM A RUNTIME HOOK. The plan says
// NOTIFY_ACKED is closed "by the runtime at the recipient's turn boundary".
// Reaching into the runtime would mean editing internal/supervisor — eight e2e
// matrix rows, and a second authority on when a turn ended. `turn_finished` is
// already emitted for every turn by M1a's lifecycle emitter, so a cursor consumer
// over it reaches the same conclusion from evidence that already exists. The
// local AgentState taxonomy stays the sole wake arbiter, untouched.

// notifyBodyMaxRunes bounds the injected summary.
//
// Real.SendMessage rejects a body over 500 runes outright — it is the first
// statement in the function, so a rejected send has no side effects — and
// documents 300. Staying under the documented figure rather than the hard one
// leaves room for the wrapping the injection path adds, and means a notification
// cannot start failing to deliver at exactly the moment a result gets
// interesting. The event id in the body is what makes the short form sufficient:
// the recipient reads the detail out of the log.
const notifyBodyMaxRunes = 300

// Injector pushes a notification into a recipient's live stream. Implemented
// outside this package over Real.SendMessage.
type Injector interface {
	Inject(ctx context.Context, recipient, body string) error
}

// EventLookup resolves one event by id — the opener that a close refers to.
type EventLookup interface {
	ByID(ctx context.Context, id uuid.UUID) (DispatchedEvent, error)
}

// OpenNotify is an owner_notify with no close.
type OpenNotify struct {
	EventID    uuid.UUID
	Recipient  string
	WorkflowID uuid.UUID
}

// NotifyReader reads outstanding notifications.
type NotifyReader interface {
	OpenNotifies(ctx context.Context, projectID uuid.UUID, recipient string) ([]OpenNotify, error)
}

// NotifyHandlerDeps follows the repo's deps-struct convention.
type NotifyHandlerDeps struct {
	Emitter  EventEmitter
	Injector Injector
	Claims   ClaimStore
	Lookup   EventLookup
	Host     string
	Consumer string
	// Local and FallbackOwner enable owner-dead handling, and both are OPTIONAL:
	// with either absent the handler notifies the owner as addressed, exactly as
	// it did before this existed. The degradation has to be in that direction —
	// a fallback of "" would produce an owner_notify with an empty recipient,
	// which is a contract nothing can ever ack.
	Local         LocalAgents
	FallbackOwner string
	Lease         func() time.Duration
	Logger        *slog.Logger
}

// NotifyHandler notifies a contract's owner when its result lands.
type NotifyHandler struct {
	emitter  EventEmitter
	injector Injector
	claims   ClaimStore
	lookup   EventLookup
	host     string
	consumer string
	local    LocalAgents
	fallback string
	lease    func() time.Duration
	log      *slog.Logger
}

var _ Handler = (*NotifyHandler)(nil)

func NewNotifyHandler(d NotifyHandlerDeps) (*NotifyHandler, error) {
	switch {
	case d.Emitter == nil:
		return nil, fmt.Errorf("store: notify handler needs an event emitter")
	case d.Injector == nil:
		return nil, fmt.Errorf("store: notify handler needs an injector")
	case d.Claims == nil:
		return nil, fmt.Errorf("store: notify handler needs a claim store; without one two hosts would both notify the same owner")
	case d.Lookup == nil:
		return nil, fmt.Errorf("store: notify handler needs an event lookup to resolve the contract being closed")
	case d.Host == "":
		return nil, fmt.Errorf("store: notify handler needs a host identity")
	case d.Consumer == "":
		return nil, fmt.Errorf("store: notify handler needs a consumer name")
	}
	h := &NotifyHandler{
		emitter: d.Emitter, injector: d.Injector, claims: d.Claims, lookup: d.Lookup,
		host: d.Host, consumer: d.Consumer,
		local: d.Local, fallback: d.FallbackOwner,
		lease: d.Lease, log: d.Logger,
	}
	if h.lease == nil {
		h.lease = func() time.Duration { return DefaultClaimLease }
	}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	return h, nil
}

// ownerOf reads the owner from a contract-opening event's payload.
//
// From the PAYLOAD rather than from events.owner_agent_id, and the reason is a
// gap rather than a preference: that column is a uuid, and M1b has no registry
// mapping agents to uuids — agents are identified by NAME everywhere in sprawl
// today. M2 populates the column once cards give agents identities; until then
// the payload's `owner` string is the only thing that can address a real
// recipient. Reading the uuid column would produce a well-typed value that
// nothing could deliver to.
func ownerOf(ev DispatchedEvent) (string, error) {
	var p struct {
		Owner string `json:"owner"`
	}
	if len(ev.Payload) == 0 {
		return "", nil
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return "", fmt.Errorf("store: contract %s has an unreadable payload: %w", ev.ID, err)
	}
	return p.Owner, nil
}

// notifyConsumer is the per-recipient claim key.
//
// Per RECIPIENT, not per event: one landing result can owe notifications to more
// than one party, and a claim keyed on the event alone would deliver to whoever
// was resolved first and silently drop the rest.
func notifyConsumer(recipient string) string { return "notify:" + recipient }

// Handle notifies the owner of the contract this event closes.
func (h *NotifyHandler) Handle(ctx context.Context, ev DispatchedEvent) error {
	if ev.ClosesEventID == nil {
		// Not a landing result. Nothing to notify anyone about.
		return nil
	}

	opener, err := h.lookup.ByID(ctx, *ev.ClosesEventID)
	if err != nil {
		// NOT skipped. A transport failure here would otherwise silently drop a
		// notification and let the dispatcher advance past the result.
		return fmt.Errorf("store: resolving the contract %s closed by %s: %w", *ev.ClosesEventID, ev.ID, err)
	}
	recipient, err := ownerOf(opener)
	if err != nil {
		return err
	}
	if recipient == "" {
		// An unowned contract is legitimate — a system-opened goal, say. Opening
		// an owner_notify with no recipient would create a contract nothing can
		// ever ack, because there is nobody to take a turn.
		h.log.Debug("contract closed with no owner to notify",
			"event", ev.ID, "contract", opener.ID, "type", opener.SchemaName)
		return nil
	}

	// OWNER-DEAD HANDLING: "never let a RESULT land unobserved."
	//
	// A notification addressed to an agent that is permanently gone cannot be
	// delivered, so the injection fails on every pass and the owner_notify
	// contract stays open forever — a result nobody ever sees, whose only symptom
	// is a growing pile of failed deliveries. Reassigning ownership to the
	// fallback (root now, the workflow engine later) is what stops that.
	//
	// Recorded as an EXPLICIT SYSTEM EVENT rather than a quiet redirect, because
	// the plan says so and because the alternative is unattributable: a reader
	// would see a result delivered to an agent that never opened the contract
	// with nothing saying why.
	recipient, err = h.reassignIfOwnerIsGone(ctx, ev, opener, recipient)
	if err != nil {
		return err
	}

	won, err := h.claims.Claim(ctx, ev.ID, notifyConsumer(recipient), h.host, h.lease())
	if err != nil {
		return fmt.Errorf("store: claiming the notification of %q for event %s: %w", recipient, ev.ID, err)
	}
	if !won {
		h.log.Debug("another host is notifying this recipient", "recipient", recipient, "event", ev.ID)
		return nil
	}

	// Recorded BEFORE the injection, so a failed delivery leaves a sweepable
	// contract rather than nothing at all.
	if _, err := h.emitter.Emit(ctx, EmitRequest{
		TypeName:           "owner_notify",
		TypeVersion:        1,
		WorkflowInstanceID: ev.WorkflowInstanceID,
		Payload: map[string]any{
			"recipient":        recipient,
			"subject_event_id": ev.ID.String(),
			"goal_event_id":    opener.ID.String(),
			"reason":           opener.SchemaName + " closed by " + ev.SchemaName,
			"host":             h.host,
		},
	}); err != nil {
		return fmt.Errorf("store: recording the notification of %q: %w (nothing was injected)", recipient, err)
	}

	if err := h.injector.Inject(ctx, recipient, notifyBody(ev, opener)); err != nil {
		// The contract stays OPEN on purpose. That is what makes this
		// recoverable: the sweeper finds the outstanding owner_notify and
		// re-delivers, instead of the result sitting unobserved forever.
		return fmt.Errorf("store: injecting the notification for %q (the owner_notify contract stays open and will be re-delivered): %w", recipient, err)
	}
	return nil
}

// reassignIfOwnerIsGone returns the owner to notify, reassigning first if the
// original is permanently gone.
//
// WHAT COUNTS AS GONE is narrow on purpose, because every false positive
// permanently severs a live agent from its own result:
//
//   - state.IsTerminal (retired, retiring) — parent-decided and permanent.
//   - died WITH NO SESSION. The session is the discriminator the plan names
//     ("retired/died, no session"): an agent recorded as died but still holding a
//     session id may come back, and reassigning it takes its work away while it
//     is recoverable.
//
// WHAT DOES NOT COUNT, and each of these is the tempting mistake:
//
//   - Every other resting status. suspended, idle, complete, faulted and paused
//     are all revivable — a notification auto-wakes or waits — so reassigning
//     them would hand the fallback every result belonging to a quiet agent.
//   - ABSENCE FROM THIS HOST. A goal owned by an agent on another host is normal
//     in a multi-host fleet, and not being in this host's state is not evidence
//     of death. Reassigning on that basis would steal work from a healthy agent
//     every time a result landed on the wrong host, and the reassignment event
//     would look entirely legitimate. The delivery is attempted as addressed; if
//     it genuinely fails, the contract stays open and the sweeper re-delivers,
//     which is the mechanism already built for this.
//   - The FALLBACK itself. One hop only: reassigning the fallback to itself would
//     either loop or emit a reassignment that changes nothing on every pass.
func (h *NotifyHandler) reassignIfOwnerIsGone(ctx context.Context, ev, opener DispatchedEvent, recipient string) (string, error) {
	if h.local == nil || h.fallback == "" || recipient == h.fallback {
		return recipient, nil
	}
	locals, err := h.local.Snapshot(ctx)
	if err != nil {
		// NOT degraded to "assume alive" silently — but also not fatal to the
		// notification, which is the more important of the two jobs. Notifying
		// the original owner is what the system did before owner-dead handling
		// existed, and a failed delivery still leaves a sweepable contract.
		h.log.Warn("cannot read local agent state, so notifying the owner as addressed without checking whether it is gone",
			"recipient", recipient, "error", err)
		return recipient, nil
	}
	var owner LocalAgent
	var known bool
	for _, l := range locals {
		if l.Name == recipient {
			owner, known = l, true
			break
		}
	}
	if !known {
		return recipient, nil
	}
	gone := state.IsTerminal(owner.Status) || (owner.Status == state.StatusDied && owner.SessionID == "")
	if !gone {
		return recipient, nil
	}

	if _, err := h.emitter.Emit(ctx, EmitRequest{
		TypeName:           "ownership_reassigned",
		TypeVersion:        1,
		WorkflowInstanceID: ev.WorkflowInstanceID,
		Payload: map[string]any{
			"contract_event_id": opener.ID.String(),
			"from_owner":        recipient,
			"to_owner":          h.fallback,
			"reason":            "the owner is " + owner.Status + " with no recoverable session, so this result would otherwise land unobserved",
			"host":              h.host,
		},
	}); err != nil {
		return "", fmt.Errorf("store: reassigning ownership of %s from %q to %q: %w", opener.ID, recipient, h.fallback, err)
	}
	h.log.Warn("reassigned a contract whose owner is permanently gone",
		"contract", opener.ID, "from", recipient, "to", h.fallback, "status", owner.Status)
	return h.fallback, nil
}

// notifyBody renders the short summary.
//
// It names the closing event so the recipient can read the detail, and it is
// bounded — see notifyBodyMaxRunes. Truncation is by RUNE and appends an ellipsis
// so a cut is visible; a byte-truncated body could split a multi-byte rune and
// produce a body the recipient renders as a replacement character.
func notifyBody(closing, opener DispatchedEvent) string {
	body := fmt.Sprintf("%s landed for your %s. Read it with get_workflow_log or search_log: event %s",
		closing.SchemaName, opener.SchemaName, closing.ID)
	return truncateRunes(body, notifyBodyMaxRunes)
}

func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	if limit <= 1 {
		return string(r[:limit])
	}
	return string(r[:limit-1]) + "…"
}

// ---------------------------------------------------------------------------
// The ack
// ---------------------------------------------------------------------------

// NotifyAckHandlerDeps follows the repo's deps-struct convention.
type NotifyAckHandlerDeps struct {
	Emitter  EventEmitter
	Notifies NotifyReader
	Host     string
	Logger   *slog.Logger
}

// NotifyAckHandler closes a recipient's outstanding notifications when it takes
// a turn.
//
// Registered for `turn_finished`, which M1a already emits for every turn — so
// this needs no new signal, no runtime hook, and no second opinion about when a
// turn ended.
type NotifyAckHandler struct {
	emitter  EventEmitter
	notifies NotifyReader
	host     string
	log      *slog.Logger
}

var _ Handler = (*NotifyAckHandler)(nil)

func NewNotifyAckHandler(d NotifyAckHandlerDeps) (*NotifyAckHandler, error) {
	switch {
	case d.Emitter == nil:
		return nil, fmt.Errorf("store: notify-ack handler needs an event emitter")
	case d.Notifies == nil:
		return nil, fmt.Errorf("store: notify-ack handler needs a notify reader")
	case d.Host == "":
		return nil, fmt.Errorf("store: notify-ack handler needs a host identity")
	}
	log := d.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &NotifyAckHandler{emitter: d.Emitter, notifies: d.Notifies, host: d.Host, log: log}, nil
}

func (h *NotifyAckHandler) Handle(ctx context.Context, ev DispatchedEvent) error {
	var p struct {
		AgentName string `json:"agent_name"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("store: turn boundary %s has an unreadable payload: %w", ev.ID, err)
	}
	if p.AgentName == "" {
		// agent_name is optional on turn_finished@1, so this is a real state
		// rather than corruption. Without it there is no recipient to ack, and
		// guessing would ack somebody else's notification.
		h.log.Debug("turn boundary names no agent, so no notification can be acked", "event", ev.ID)
		return nil
	}

	open, err := h.notifies.OpenNotifies(ctx, ev.ProjectID, p.AgentName)
	if err != nil {
		return fmt.Errorf("store: reading outstanding notifications for %q: %w", p.AgentName, err)
	}
	// The overwhelmingly common case: every turn of every agent reaches here and
	// has nothing outstanding. Emitting anything would hit ErrNoOpenContract and
	// dead-letter once per turn across the whole system.
	for _, n := range open {
		if _, err := h.emitter.Emit(ctx, EmitRequest{
			TypeName:           "notify_acked",
			TypeVersion:        1,
			WorkflowInstanceID: n.WorkflowID,
			ClosesEventID:      &n.EventID,
			Payload: map[string]any{
				"recipient":     p.AgentName,
				"turn_event_id": ev.ID.String(),
				"host":          h.host,
			},
		}); err != nil {
			return fmt.Errorf("store: acking notification %s for %q: %w", n.EventID, p.AgentName, err)
		}
		h.log.Debug("acked a notification at a turn boundary",
			"recipient", p.AgentName, "notify", n.EventID, "turn", ev.ID)
	}
	return nil
}
