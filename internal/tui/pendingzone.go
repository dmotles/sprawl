package tui

// QUM-833 — uuid-keyed pending zone.
//
// Inbound user-role frames (typed prompts, drained system-notifications) have
// no committed-transcript identity until the CLI acknowledges them via its
// isReplay echo (EventUserMessageConsumed). The pendingZone holds each frame as
// a single uuid-keyed entry from the moment it is written to stdin
// (UserMessageSentMsg) until it settles (relocated into the committed
// transcript on consume) or is dropped (recall / supersede). One logical entry
// per uuid for its whole life — never rendered twice, never blind-appended.
//
// The zone is a separate slice owned by ChatList; it is NOT part of
// ChatList.items, so the assistant-chunk coalescing invariant (trailing item is
// the in-flight assistant) is never disturbed by an eager pending render. See
// ghost's design doc §4.1.1.

import "strings"

// pendingKind distinguishes a user-submitted prompt from a system-notification
// envelope. System entries are born final-styled and are never recall-droppable
// (LOCKED invariant 5); user entries are recallable.
type pendingKind int

const (
	pendingUser pendingKind = iota
	pendingSystem
)

// pendingEntry is one uuid-keyed inbound frame. A user frame holds exactly one
// item; a system frame holds the N items produced by the envelope peel-loop.
type pendingEntry struct {
	uuid  string
	kind  pendingKind
	items []*itemEnvelope
}

// pendingZone is an ordered, uuid-keyed set of pendingEntry. Order is
// arrival/submit order; settle relocates entries out in consume order.
type pendingZone struct {
	order  []*pendingEntry
	byUUID map[string]*pendingEntry
}

func newPendingZone() *pendingZone {
	return &pendingZone{byUUID: make(map[string]*pendingEntry)}
}

// add inserts an entry at the tail of the zone, keyed by uuid. A duplicate uuid
// replaces the existing entry's items in place (keeping its position).
func (z *pendingZone) add(e *pendingEntry) {
	if existing, ok := z.byUUID[e.uuid]; ok {
		existing.kind = e.kind
		existing.items = e.items
		return
	}
	z.byUUID[e.uuid] = e
	z.order = append(z.order, e)
}

// take removes and returns the entry for uuid, or nil if absent.
func (z *pendingZone) take(uuid string) *pendingEntry {
	e, ok := z.byUUID[uuid]
	if !ok {
		return nil
	}
	delete(z.byUUID, uuid)
	for i, cur := range z.order {
		if cur == e {
			z.order = append(z.order[:i], z.order[i+1:]...)
			break
		}
	}
	return e
}

// userCount returns the number of pending user-submitted entries (system
// notifications are not "queued by the user").
func (z *pendingZone) userCount() int {
	n := 0
	for _, e := range z.order {
		if e.kind == pendingUser {
			n++
		}
	}
	return n
}

// len returns the total number of pending entries (user + system).
func (z *pendingZone) len() int { return len(z.order) }

// itemCount returns the total number of rendered items across all pending
// entries. A user entry holds one item; a system entry holds the N items from
// its envelope peel. This is the content-item count (not the entry count) so it
// is unit-consistent with ChatList.Len(), which lets the scroll indicator's
// growth detector see a settle (zone entry → N committed items) as net-zero
// rather than a spurious jump. QUM-856.
func (z *pendingZone) itemCount() int {
	n := 0
	for _, e := range z.order {
		n += len(e.items)
	}
	return n
}

// clear removes every pending entry.
func (z *pendingZone) clear() {
	z.order = nil
	z.byUUID = make(map[string]*pendingEntry)
}

// classifyInboundFrame reports whether an inbound user-role frame is a
// system-notification envelope. THE single classify point — shared by the
// zone-create path, the legacy empty-uuid append path, and replay.go's peel so
// live and replay rendering can never drift again (QUM-833 three-way drift).
func classifyInboundFrame(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), systemNotificationOpenPrefix)
}

// peelNotificationEntries converts a system-notification frame body into
// transcript entries: N MessageSystemNotification entries (one per stacked
// envelope, peel order) plus any trailing residue as a MessageUser entry.
// Returns ok=false for a non-system frame so the caller emits a single user
// entry. This is the SAME classify+peel decision the live pending-zone path
// uses (classifyInboundFrame + stripSystemNotificationTag), so replay.go and the
// live path cannot drift (QUM-833 single-classifier convergence).
func peelNotificationEntries(text string) (entries []MessageEntry, ok bool) {
	if !classifyInboundFrame(text) {
		return nil, false
	}
	rest := text
	for {
		stripped, notifType, isInterrupt, remaining, more := stripSystemNotificationTag(rest)
		if !more {
			break
		}
		entries = append(entries, MessageEntry{
			Type:             MessageSystemNotification,
			Content:          stripped,
			Complete:         true,
			Interrupt:        isInterrupt,
			NotificationType: notifType,
		})
		rest = remaining
	}
	if strings.TrimSpace(rest) != "" {
		entries = append(entries, MessageEntry{
			Type:     MessageUser,
			Content:  rest,
			Complete: true,
		})
	}
	return entries, true
}
