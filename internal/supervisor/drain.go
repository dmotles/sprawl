// Package supervisor / drain.go — the single inbox→stdin drain shared by the
// root (weave) and child agent paths (QUM-1062).
//
// There used to be two `drainPendingToStdin` implementations, and every fix was
// two fixes. That is not a hypothetical cost: QUM-925 fixed weave's missing
// liveness drain BY COPYING THE CHILD'S, and the copy uncorked a ~2-month backlog
// that landed as one 38,673-byte frame and destroyed a session. The three commits
// immediately before this one (QUM-1066 in-flight filter, QUM-1068 idempotent
// ack, QUM-1072 bounded write) each had to be applied to one path and then
// deliberately mirrored to the other.
//
// SHAPE. The drain is split into an I/O half and a pure half:
//
//	readInboxSnapshot  — I/O: ListPending, the in-flight filter, and the
//	                     DESTRUCTIVE status_change drain
//	buildInjection     — PURE: snapshot + policy → frames. No I/O, no clock,
//	                     no runtime. Unit-testable without a session or handle.
//	writeInjection     — I/O: one bounded write per frame, ack, WARN on failure
//
// The pure middle is the high-value half. Before it, asserting anything about
// frame assembly meant standing up a handle, a fake backend.Session and a frame
// router — and such a test sits BELOW boundSystemFrame, which dedups identical
// lines inside a frame and can therefore mask a double-emit. See
// drain_policy_test.go's header for the measured precedent.
//
// POLICY, NOT FORKS. Every way the two paths differ is a named field on
// drainPolicy with the reason attached. If you are about to write `if isWeave`
// in this file, add a policy field instead.

package supervisor

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/inboxprompt"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
)

// drainAsyncPriority is the priority of the async (queued-mail) frame on BOTH
// paths, and of weave's single coalesced frame. It is not a policy field because
// nothing has ever varied it: QUM-925 locks system frames to `next`.
const drainAsyncPriority = "next"

// drainPolicy names every difference between the root and child drains.
//
// Adding a field here is cheap; adding a branch to buildInjection or
// writeInjection is not. The point of the struct is that a reader auditing "how
// do these two paths differ" reads one type instead of diffing two functions.
type drainPolicy struct {
	// mu serialises the whole drain. NON-NIL for weave, NIL for children — and
	// the nil is a known residual, not an oversight. See weaveDrainPolicy and
	// childDrainPolicy for the two halves of the reasoning.
	mu *sync.Mutex

	// interruptPriority is the stdin priority for interrupt-class (
	// send_message(interrupt=true)) entries. LOCKED asymmetry: `now` for children,
	// `next` for weave — see childDrainPolicy / weaveDrainPolicy, and QUM-925.
	interruptPriority string

	// coalesceInterrupts emits interrupts and asyncs as ONE frame instead of two.
	// Only sound when interruptPriority == drainAsyncPriority: with both classes
	// at the same priority there is nothing for the interrupt batch to preempt, so
	// the class distinction degrades to ordering WITHIN the frame. Setting this
	// true alongside a `now` interruptPriority would silently demote urgent mail
	// to the async priority.
	coalesceInterrupts bool

	// ackInterruptOnWrite confirms the interrupt frame delivered on a successful
	// write rather than waiting for the isReplay echo (QUM-821's
	// ConfirmDeliveredWithoutReplay). Child-only; see childDrainPolicy.
	//
	// MEANINGFUL ONLY WHEN coalesceInterrupts IS FALSE, and the pairing is an
	// INVALID COMBINATION rather than an unused one. A coalesced frame carries
	// async entries too, so acking it on write would mark ordinary inbox mail
	// delivered before the CLI consumed it — precisely the durability trade
	// QUM-1066 rejected for the async tier. buildInjection therefore never sets
	// ackOnWrite on a coalesced frame, and
	// TestQUM1062_Policy_CoalescedFrameIsNeverAckedOnWrite fails if any production
	// policy sets both. Do not "fix" the asymmetry by wiring the field through the
	// coalesce branch; that would silently weaken async durability on the root.
	ackInterruptOnWrite bool

	// writeTimeout bounds EACH frame's write. A FUNC, not a Duration: the child
	// binds it to the childDrainWriteTimeout atomicDuration so test overrides are
	// honoured (capturing the value at policy-construction time would silently
	// unbind every bounded-write test). QUM-1072.
	writeTimeout func() time.Duration

	// logPrefix distinguishes the two paths in the failure WARN. Deliberately not
	// unified to one literal: a fleet log must be able to say which agent class
	// lost a notification, and live tests match on the child's exact prefix.
	logPrefix string
}

// weaveDrainPolicy is the root agent's policy. mu is the handle's drainMu.
func weaveDrainPolicy(mu *sync.Mutex) drainPolicy {
	return drainPolicy{
		// SERIALISED. Pokes arrive on independent MCP handler goroutines (one per
		// child report_status / send_message). Both inbox reads are unsafe
		// concurrently: messages.DrainStatusChange is an unlocked
		// read-dir/read-file/remove sequence, and ListPending is a peek whose ack
		// lands much later. Overlapping drains write the same notification twice.
		// Note the in-flight filter does NOT cover this — status lines carry no
		// entry ID, so nothing but this mutex stops a concurrent double-read of
		// them. (QUM-925; pinned by
		// TestWeaveRuntimeHandle_ConcurrentWakeForDelivery_NoDuplicateWrite.)
		mu: mu,

		// LOCKED at `next`, and this is the one row that must not be "tidied".
		// Two load-bearing reasons: the QUM-925 design states system frames are
		// `next` and STAY `next` through Ctrl+G; and a `now` write arms
		// armInterruptLocked (runtime.writeMessage), preempting weave's in-flight
		// turn — which contradicts "Esc interrupts the turn but system frames
		// remain queued" and the dumb-forwarder rule against timing games.
		// Documented consequence: an inter-agent send_message(interrupt=true)
		// targeting weave is non-preemptive, an asymmetry vs a child recipient.
		// Restoring preemption is a follow-up issue, not a defect here.
		interruptPriority: drainAsyncPriority,

		// Follows from the priority above: with nothing to preempt, both classes
		// ride one frame and class precedence survives as ordering within it.
		coalesceInterrupts: true,

		// NO ack-on-write. Weave writes at `next` and confirms on the isReplay
		// echo. Acking on write would mark inbox mail delivered before the CLI
		// consumed it — the durability trade QUM-1066 rejected for the async tier.
		ackInterruptOnWrite: false,

		// Weave's bound is a const: nothing overrides it, so it needs no seam.
		writeTimeout: func() time.Duration { return weaveDrainWriteTimeout },

		logPrefix: "weave-runtime",
	}
}

// childDrainPolicy is a child agent's policy. It takes no mutex: there is none to
// pass, which is the point — see the mu field below.
func childDrainPolicy() drainPolicy {
	return drainPolicy{
		// UNSERIALISED — a declared residual, carried forward from QUM-1066 rather
		// than inherited silently, because QUM-1062 is where it became a policy
		// question rather than an accident of having two functions.
		//
		// The read-then-write is TOCTOU: a poke on the MCP handler goroutine
		// (Real.SendMessage / Real.ReportStatus) can interleave with PostTurnSweep
		// on the backend reader goroutine, and both can read the in-flight set
		// before either writes. So the child's guarantee is "written once per
		// SEQUENTIAL drain", not "once under concurrent drains". The unbounded
		// storm QUM-1066 killed is gone; a rare concurrent duplicate is not.
		//
		// Left nil DELIBERATELY: adding a mutex here would be a behaviour change,
		// and QUM-1062's contract is that unification changes nothing. Closing it
		// is its own issue.
		mu: nil,

		// `now` = cancel-and-replace urgency. The priority itself preempts the
		// recipient; no separate bare interrupt frame is issued (that is reserved
		// for Esc-abort). This is the LOCKED asymmetry vs weave — QUM-925.
		interruptPriority: "now",

		// NOT coalesced: the interrupt batch needs its own frame so `now` can
		// preempt. Coalescing it into the async frame would demote it to `next`.
		coalesceInterrupts: false,

		// A `now` write is injected directly into the model turn, so delivery is
		// confirmed on the successful write instead of waiting for an echo. Without
		// it the entry stays in pending/ and PostTurnSweep — which re-wakes whenever
		// pending/ is non-empty — re-drains and re-injects it after every turn: the
		// ~30 writes/s storm QUM-821 measured against real claude 2.1.173.
		//
		// The echo usually DOES arrive (QUM-1068 measured 51 of 54), but not
		// always, so it cannot be relied on; when it does, markConsumed is
		// idempotent and the second ack is a silent no-op.
		//
		// Tradeoff, by design: the urgent content is considered delivered before
		// the CLI processed it, so a CLI death in that window loses it. Accepted
		// for the urgency tier only.
		ackInterruptOnWrite: true,

		// Func seam over the package var so test overrides bind. QUM-1072.
		writeTimeout: childDrainWriteTimeout.get,

		logPrefix: "unified-runtime",
	}
}

// inboxSnapshot is everything the drain read from disk, already filtered. Passing
// it as one value is what lets buildInjection be pure.
//
// QUM-1186: statusLines is gone with the status_change envelope class. Every
// read the drain now performs is a NON-destructive peek of agentloop entries,
// which is why systemFrame.destructiveLines and the "these strings are the
// only surviving copy" hazard went with it.
type inboxSnapshot struct {
	pending []agentloop.Entry
}

// systemFrame is one prepared stdin write. Nothing here has reached the wire.
type systemFrame struct {
	// label discriminates frames in the failure WARN ("" for the async/coalesced
	// frame). Without it, a drain whose frames both fail emits two WARNs a log
	// reader cannot tell apart.
	label string

	body     string
	priority string

	// entryIDs drive OnDelivered → MarkDelivered, so they must ride the frame that
	// actually carries their bodies. An ID on the wrong frame marks an entry
	// delivered by a write that never contained it.
	entryIDs []string

	// ackOnWrite confirms delivery on a successful write (QUM-821).
	ackOnWrite bool
}

// buildInjection turns a snapshot into the frames to write. PURE: no I/O, no
// clock, no locks, no runtime.
//
// Its output is PRE-dedup and PRE-truncation — boundSystemFrame runs inside
// WriteSystemMessage, below this layer — which is exactly why assertions about
// duplicate emission belong here rather than on a rendered frame body.
func buildInjection(snap inboxSnapshot, pol drainPolicy) []systemFrame {
	if len(snap.pending) == 0 {
		return nil
	}

	interrupts, asyncs := inboxprompt.SplitByClass(snap.pending)

	if pol.coalesceInterrupts {
		var body strings.Builder
		ids := make([]string, 0, len(snap.pending))
		// Interrupt bodies before async bodies: the class precedence that the
		// shared priority removed from scheduling survives as frame ordering.
		if len(interrupts) > 0 {
			body.WriteString(inboxprompt.BuildInterruptFlushPrompt(interrupts))
			for _, e := range interrupts {
				ids = append(ids, e.ID)
			}
		}
		if len(asyncs) > 0 {
			body.WriteString(inboxprompt.BuildQueueFlushPrompt(asyncs))
			for _, e := range asyncs {
				ids = append(ids, e.ID)
			}
		}
		return []systemFrame{{
			body:     body.String(),
			priority: pol.interruptPriority,
			entryIDs: ids,
			// ackOnWrite deliberately NOT set from pol.ackInterruptOnWrite: this
			// frame carries async entries too, and acking it would mark them
			// delivered before the CLI consumed them. See the field's doc.
		}}
	}

	var frames []systemFrame
	if len(interrupts) > 0 {
		ids := make([]string, 0, len(interrupts))
		for _, e := range interrupts {
			ids = append(ids, e.ID)
		}
		frames = append(frames, systemFrame{
			label:      "interrupt batch",
			body:       inboxprompt.BuildInterruptFlushPrompt(interrupts),
			priority:   pol.interruptPriority,
			entryIDs:   ids,
			ackOnWrite: pol.ackInterruptOnWrite,
		})
	}
	// QUM-1186: this used to also fire on `len(statusLines) > 0` so a
	// status-only frame still flowed when every async entry was filtered as
	// in-flight. With status lines gone there is nothing to carry but asyncs.
	if len(asyncs) > 0 {
		ids := make([]string, 0, len(asyncs))
		for _, e := range asyncs {
			ids = append(ids, e.ID)
		}
		frames = append(frames, systemFrame{
			body:     inboxprompt.BuildQueueFlushPrompt(asyncs),
			priority: drainAsyncPriority,
			entryIDs: ids,
		})
	}
	return frames
}

// readInboxSnapshot performs the drain's reads: the maildir peek and the
// in-flight filter.
//
// QUM-1186: the DESTRUCTIVE status_change drain that used to run here is gone.
//
// Order is load-bearing and must not be "optimised": the destructive read happens
// unconditionally, before any emptiness decision, because whether any lines exist
// is only knowable by draining them.
//
// CONCRETELY, the edit to never make: an early `if len(pending) == 0 { return
// inboxSnapshot{} }` above the drain below. It reads like a harmless fast path and
// it permanently loses every status_change line whose maildir happened to be
// empty. That is why the drain is its own statement under this comment rather than
// an expression inside the returned literal — a reader adding that fast path has
// to step over a named, commented destructive call to do it.
func readInboxSnapshot(rt *runtimepkg.UnifiedRuntime, sprawlRoot, name string, pol drainPolicy) inboxSnapshot {
	pending, err := agentloop.ListPending(sprawlRoot, name)
	if err != nil {
		slog.Default().Debug(
			pol.logPrefix+": drainPendingToStdin ListPending failed",
			slog.String("agent", name),
			slog.Any("err", err),
		)
	}
	// Skip entries already written and awaiting their consumption ack. They stay
	// in pending/ until MarkDelivered, so without this filter every subsequent
	// poke re-injects them — the unbounded stdin write storm QUM-1061 measured at
	// exactly one extra injection per turn boundary, forever (QUM-1066).
	//
	// Filtering BEFORE SplitByClass covers interrupts too: a no-op on both
	// interrupt paths (a successful `now` write acks immediately; a failed one
	// leaves no outstanding record) that closes the same
	// markConsumed→MarkDelivered window for the `now` tier for free.
	//
	// Do NOT narrow the predicate to statePending — QUM-925 found the
	// consumed-but-not-yet-MarkDelivered hole that leaves. The reasoning lives on
	// InFlightSystemEntryIDs itself (internal/runtime/unified.go), including the
	// accepted QUM-1028 cost: an entry that reached stateConsumed WITHOUT
	// OnDelivered is suppressed for the life of the process. That is a DELAY, not
	// loss — the entry never left pending/ and `outstanding` is in-memory, so a
	// fresh runtime re-emits it.
	if inFlight := rt.InFlightSystemEntryIDs(); len(inFlight) > 0 {
		kept := pending[:0]
		for _, e := range pending {
			if _, dup := inFlight[e.ID]; !dup {
				kept = append(kept, e)
			}
		}
		pending = kept
	}

	return inboxSnapshot{pending: pending}
}

// writeInjection writes each frame to stdin, bounded, acking and logging per
// policy.
//
// TWO INVARIANTS, both load-bearing and both easy to break while tidying:
//
//  1. ONE CONTEXT PER FRAME, created inside the loop. Hoisting it would let the
//     first frame consume the whole deadline and hand the second a near-expired
//     one — turning a slow path into a guaranteed-failing one. Accepted cost:
//     a fully wedged pipe carrying both classes blocks for 2× the deadline.
//  2. A FAILED FRAME MUST NOT ABORT THE REST. `continue`, never `return`: the
//     pre-unification child's two writes were independent branches, and an
//     interrupt-write failure still let the async batch through.
func writeInjection(rt *runtimepkg.UnifiedRuntime, name string, frames []systemFrame, pol drainPolicy) {
	for _, f := range frames {
		// BOUNDED write. Session.WriteUserMessage → transport.Send runs the
		// blocking WriteJSON in a goroutine and selects on ctx.Done(), so the
		// context is the ONLY escape when the recipient's stdin pipe is full
		// (64 KiB, unread). An unbounded write here blocked FOREVER on the SENDER's
		// goroutine — Real.SendMessage pokes synchronously on the MCP handler
		// goroutine — so one wedged recipient hung an unrelated agent's tool
		// call. A bound degrades that to "this notification is late".
		// (QUM-1072.)
		d := pol.writeTimeout()
		ctx, cancel := context.WithTimeout(context.Background(), d)
		uuid, err := rt.WriteSystemMessage(ctx, f.body, f.priority, f.entryIDs)
		cancel()

		if err != nil {
			msg := pol.logPrefix + ": drainPendingToStdin write failed"
			if f.label != "" {
				msg += " (" + f.label + ")"
			}
			msg += " — maildir entries stay in pending/ for the next poke"
			attrs := []any{
				slog.String("agent", name),
				slog.Duration("deadline", d),
				slog.Int("entry_count", len(f.entryIDs)),
				slog.String("entry_ids", strings.Join(f.entryIDs, ",")),
			}
			attrs = append(attrs, slog.Any("err", err))
			slog.Default().Warn(msg, attrs...)
			continue // invariant 2
		}

		if f.ackOnWrite {
			// Routing through ConfirmDeliveredWithoutReplay (not coord.OnDelivered
			// directly) also flips the in-memory outstanding entry to consumed —
			// otherwise a now-uuid leaks as perpetually statePending, wrong for the
			// recall / queued→sent UI that reads Outstanding().
			rt.ConfirmDeliveredWithoutReplay(uuid)
		}
	}
}

// runDrain is the whole drain: read, build, write, under the policy's lock.
func runDrain(rt *runtimepkg.UnifiedRuntime, sprawlRoot, name string, pol drainPolicy) {
	if pol.mu != nil {
		pol.mu.Lock()
		defer pol.mu.Unlock()
	}
	snap := readInboxSnapshot(rt, sprawlRoot, name, pol)
	writeInjection(rt, name, buildInjection(snap, pol), pol)
}
