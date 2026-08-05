// QUM-1062: the policy table, asserted at the PURE layer.
//
// buildInjection is the I/O-free core of both drains. Every divergence between
// the root and child paths is a named field on drainPolicy, and every row below
// is a test that fails if that field is flipped — which is the acceptance
// criterion, not decoration.
//
// WHY THESE TESTS EXIST AT THIS LAYER AT ALL. A refactor's tests are written from
// the code being refactored, so nothing about them can disagree with the author
// (/testing-practices § "the same breath"). Two mitigations are baked in here:
//
//  1. They assert OUTCOMES — how many frames, what priority, which body carries
//     which text, which IDs ride which frame — never which branch executed.
//  2. They run ABOVE boundSystemFrame. That matters concretely: boundSystemFrame
//     (internal/runtime/unified.go) dedups identical lines WITHIN one frame, so a
//     handle-level test that counts an entry ID inside a single frame body cannot
//     distinguish "the drain emitted it once" from "the drain emitted it twice and
//     the frame layer collapsed them". That is a measured precedent, not a
//     hypothetical: a handle-level liveness test stayed green under mutation for
//     exactly this reason. buildInjection's output is pre-dedup and pre-truncation,
//     so a duplicate here is visible. Handle-level assertions in the sibling files
//     count across FRAMES (via attemptedSnapshot), which dedup cannot mask.
//
// The corollary, stated so nobody over-reads these: this layer says nothing about
// what reached the wire. Frame → wire is the writer's job and is pinned in
// qum1072_child_drain_bounded_write_test.go and weave_handle_test.go.
package supervisor

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/inboxprompt"
)

// qum1062Entry builds a maildir entry of the given class.
func qum1062Entry(id string, class inboxprompt.Class) agentloop.Entry {
	return agentloop.Entry{
		ID: id, ShortID: id, Class: class,
		From: "weave", Subject: "s", Body: "b",
	}
}

func qum1062Snapshot(statusLines []string, entries ...agentloop.Entry) inboxSnapshot {
	return inboxSnapshot{pending: entries, statusLines: statusLines}
}

// frameIndexCarrying returns the index of the frame whose body contains sub, or
// -1. Used instead of "does any frame contain it" so a test can assert WHICH
// frame, which is the whole point of the coalescing and status-attachment rows.
func frameIndexCarrying(frames []systemFrame, sub string) int {
	for i, f := range frames {
		if strings.Contains(f.body, sub) {
			return i
		}
	}
	return -1
}

// --- ROW 2 + ROW 3: interrupt priority, and the frame split it implies --------

// TestQUM1062_Policy_InterruptPriority_And_Coalescing is the paired test for the
// two rows that cannot be separated on the child side: the child writes
// interrupts at `now` in their OWN frame; weave writes everything at `next` in
// ONE frame.
//
// The priority asymmetry is LOCKED by QUM-925's design — see drainPolicy's
// interruptPriority field — so this test pins a deliberate difference, not an
// accident awaiting cleanup.
func TestQUM1062_Policy_InterruptPriority_And_Coalescing(t *testing.T) {
	snap := qum1062Snapshot(nil,
		qum1062Entry("id-int", inboxprompt.ClassInterrupt),
		qum1062Entry("id-async", inboxprompt.ClassAsync),
	)

	t.Run("child splits and uses now", func(t *testing.T) {
		frames := buildInjection(snap, childDrainPolicy())
		if len(frames) != 2 {
			t.Fatalf("frames = %d, want 2 — the child must emit the interrupt batch as its OWN frame so `now` can preempt; a single coalesced frame would carry interrupts at the async priority", len(frames))
		}
		if frames[0].priority != "now" {
			t.Errorf("interrupt frame priority = %q, want \"now\"", frames[0].priority)
		}
		if frames[1].priority != "next" {
			t.Errorf("async frame priority = %q, want \"next\"", frames[1].priority)
		}
		if i := frameIndexCarrying(frames, "id-int"); i != 0 {
			t.Errorf("interrupt entry rode frame %d, want frame 0", i)
		}
		if i := frameIndexCarrying(frames, "id-async"); i != 1 {
			t.Errorf("async entry rode frame %d, want frame 1", i)
		}
	})

	t.Run("weave coalesces at next", func(t *testing.T) {
		frames := buildInjection(snap, weaveDrainPolicy(nil))
		if len(frames) != 1 {
			t.Fatalf("frames = %d, want 1 — weave coalesces both classes into ONE frame (QUM-925); splitting would put a `now` write on weave's path and preempt its in-flight turn", len(frames))
		}
		if frames[0].priority != "next" {
			t.Errorf("weave frame priority = %q, want \"next\" — a `now` write arms armInterruptLocked and preempts weave's turn (LOCKED, QUM-925)", frames[0].priority)
		}
		// Class precedence survives as ORDERING WITHIN the frame, which is what
		// coalescing trades the scheduling distinction for.
		//
		// PRESENCE IS ASSERTED FIRST, and that is not defensive noise:
		// strings.Index returns -1 for a missing substring, and -1 > n is false, so
		// a bare ordering comparison stays GREEN when a body is dropped entirely.
		// Coalescing is exactly the code path that could drop one.
		body := frames[0].body
		iInt, iAsync := strings.Index(body, "id-int"), strings.Index(body, "id-async")
		if iInt < 0 || iAsync < 0 {
			t.Fatalf("coalesced frame must carry BOTH bodies (interrupt idx=%d, async idx=%d):\n%s", iInt, iAsync, body)
		}
		if iInt > iAsync {
			t.Errorf("interrupt body must precede async body inside the coalesced frame:\n%s", body)
		}
	})
}

// --- ROW 4: ack-on-write ------------------------------------------------------

// TestQUM1062_Policy_AckInterruptOnWrite pins that only the child's interrupt
// frame is flagged for ConfirmDeliveredWithoutReplay.
//
// Weave has NO ack-on-write, and nothing before QUM-1062 tested that: turning it
// on for weave would have gone unnoticed. That is the gap this row closes.
func TestQUM1062_Policy_AckInterruptOnWrite(t *testing.T) {
	snap := qum1062Snapshot(nil, qum1062Entry("id-int", inboxprompt.ClassInterrupt))

	child := buildInjection(snap, childDrainPolicy())
	if len(child) != 1 || !child[0].ackOnWrite {
		t.Fatalf("child interrupt frame ackOnWrite = %v (frames=%d), want true — without it the entry stays in pending/ and PostTurnSweep re-injects it every turn (the QUM-821 ~30 writes/s storm)", len(child) == 1 && child[0].ackOnWrite, len(child))
	}

	weave := buildInjection(snap, weaveDrainPolicy(nil))
	if len(weave) != 1 {
		t.Fatalf("weave frames = %d, want 1", len(weave))
	}
	if weave[0].ackOnWrite {
		t.Error("weave frame ackOnWrite = true, want false — weave writes at `next` and confirms on the isReplay echo; acking on write would mark inbox mail delivered before the CLI consumed it, which is the durability trade QUM-1066 rejected for the async tier")
	}
}

// TestQUM1062_Policy_CoalescedFrameIsNeverAckedOnWrite guards a hazard the policy
// struct itself introduces, found by mutating the ack row and getting NO failure.
//
// buildInjection ignores ackInterruptOnWrite on the coalesced path — correctly,
// because a coalesced frame carries async entries and acking it would mark
// ordinary inbox mail delivered before the CLI consumed it. But "correctly
// ignored" and "silently ignored" look identical from a test that only reads the
// emitted frame: flipping weave's ackInterruptOnWrite to true changed nothing
// observable, so the field could be set on a coalescing policy and mean nothing.
//
// This asserts the INVARIANT instead of the emitted value: no policy may combine
// coalescing with ack-on-write. That gives the ack row a mutation that can
// actually fail, and it fails at the policy rather than downstream where the
// symptom (async mail marked delivered early) would be far from the cause.
func TestQUM1062_Policy_CoalescingPreconditions(t *testing.T) {
	for _, tc := range allDrainPolicies() {
		if reason := coalescingPreconditionViolation(tc.p); reason != "" {
			t.Errorf("%s policy violates a coalescing precondition: %s", tc.name, reason)
		}
	}

	// ANTI-VACUITY, done properly: feed the predicate the shapes it exists to
	// reject and require it to reject them, then feed it a production policy and
	// require it to accept. A previous version of this block constructed a bad
	// policy and asserted its own fields were set, which is compile-time true and
	// proved nothing about the predicate.
	for _, bad := range []struct {
		name string
		p    drainPolicy
	}{
		{"coalesce+ack", drainPolicy{coalesceInterrupts: true, ackInterruptOnWrite: true}},
		{"coalesce+now", drainPolicy{coalesceInterrupts: true, interruptPriority: "now"}},
	} {
		if coalescingPreconditionViolation(bad.p) == "" {
			t.Errorf("predicate accepted the invalid %s combination it exists to reject", bad.name)
		}
	}
	for _, good := range allDrainPolicies() {
		if r := coalescingPreconditionViolation(good.p); r != "" {
			t.Fatalf("predicate rejected the production %s policy (%s) — it is too strict to be a useful invariant", good.name, r)
		}
	}
}

// --- ROW 6: where the destructively-drained status lines ride ------------------

// TestQUM1062_Policy_StatusLinesRideTheAsyncFrame pins BOTH halves of the
// status-line placement, which are separate observables:
//
//   - WHICH frame carries them (the child's async frame, never its interrupt
//     frame — a live WARN test depends on the interrupt frame having none), and
//   - WHERE they sit relative to the other bodies, which differs by path: weave
//     puts status ahead of interrupt AND async in one frame; the child necessarily
//     delivers its interrupt frame first and status lines only in frame 2.
//
// The second half is easy to miss because both paths "prepend status lines" — but
// prepending to different frames produces a different wire order.
func TestQUM1062_Policy_StatusLinesRideTheAsyncFrame(t *testing.T) {
	const line = "QUM1062-STATUS-LINE\n"
	snap := qum1062Snapshot([]string{line},
		qum1062Entry("id-int", inboxprompt.ClassInterrupt),
		qum1062Entry("id-async", inboxprompt.ClassAsync),
	)

	child := buildInjection(snap, childDrainPolicy())
	if len(child) != 2 {
		t.Fatalf("child frames = %d, want 2", len(child))
	}
	if strings.Contains(child[0].body, "QUM1062-STATUS-LINE") {
		t.Error("status line rode the child's INTERRUPT frame; it must ride the async frame only — the interrupt frame's timeout WARN asserts it carries no lost lines")
	}
	if !strings.HasPrefix(child[1].body, line) {
		t.Errorf("status line must be PREPENDED to the child's async frame so it surfaces before queued mail:\n%s", child[1].body)
	}
	// The destructive lines must be attached to the frame that carries them, so a
	// failed write can name them in the WARN. This is the only surviving copy —
	// DrainStatusChangeLines already removed the envelope from disk.
	if len(child[0].destructiveLines) != 0 {
		t.Errorf("interrupt frame destructiveLines = %v, want empty", child[0].destructiveLines)
	}
	if len(child[1].destructiveLines) != 1 {
		t.Errorf("async frame destructiveLines = %v, want the one drained line — a failed write must be able to log the bodies", child[1].destructiveLines)
	}

	weave := buildInjection(snap, weaveDrainPolicy(nil))
	if len(weave) != 1 {
		t.Fatalf("weave frames = %d, want 1", len(weave))
	}
	body := weave[0].body
	if !strings.HasPrefix(body, line) {
		t.Errorf("weave must prepend status lines ahead of BOTH classes:\n%s", body)
	}
	if len(weave[0].destructiveLines) != 1 {
		t.Errorf("weave frame destructiveLines = %v, want the drained line", weave[0].destructiveLines)
	}
}

// --- entry-ID routing ---------------------------------------------------------

// TestQUM1062_Policy_EntryIDsRideTheirOwnFrame pins which IDs are attached to
// which frame, and the coalesced ORDER.
//
// This is not cosmetic: entryIDs drive OnDelivered → MarkDelivered, so attaching
// an ID to the wrong frame marks an entry delivered on a write that did not carry
// it. The coalesced order (interrupts then asyncs) is preserved byte-for-byte
// from the pre-refactor weave path.
func TestQUM1062_Policy_EntryIDsRideTheirOwnFrame(t *testing.T) {
	snap := qum1062Snapshot(nil,
		qum1062Entry("id-int", inboxprompt.ClassInterrupt),
		qum1062Entry("id-async", inboxprompt.ClassAsync),
	)

	child := buildInjection(snap, childDrainPolicy())
	if len(child) != 2 {
		t.Fatalf("child frames = %d, want 2", len(child))
	}
	if got := child[0].entryIDs; len(got) != 1 || got[0] != "id-int" {
		t.Errorf("child interrupt frame entryIDs = %v, want [id-int] — an async ID here would be marked delivered by a write that never carried it", got)
	}
	if got := child[1].entryIDs; len(got) != 1 || got[0] != "id-async" {
		t.Errorf("child async frame entryIDs = %v, want [id-async]", got)
	}

	weave := buildInjection(snap, weaveDrainPolicy(nil))
	if len(weave) != 1 {
		t.Fatalf("weave frames = %d, want 1", len(weave))
	}
	if got := weave[0].entryIDs; len(got) != 2 || got[0] != "id-int" || got[1] != "id-async" {
		t.Errorf("weave frame entryIDs = %v, want [id-int id-async] in that order (interrupts first), preserved from the pre-unification path", got)
	}
}

// --- empty / status-only shapes ----------------------------------------------

// TestQUM1062_Policy_EmptyAndStatusOnly covers the two edge shapes both paths
// shared before unification, either of which is easy to break while restructuring.
//
// The status-only case is the one with teeth: the lines have already been
// destructively drained by the time buildInjection runs, so a policy that emitted
// no frame because there were no maildir entries would LOSE them outright.
func TestQUM1062_Policy_EmptyAndStatusOnly(t *testing.T) {
	for _, pol := range []struct {
		name string
		p    drainPolicy
	}{{"child", childDrainPolicy()}, {"weave", weaveDrainPolicy(nil)}} {
		t.Run(pol.name+"/empty", func(t *testing.T) {
			if frames := buildInjection(qum1062Snapshot(nil), pol.p); len(frames) != 0 {
				t.Fatalf("frames = %d, want 0 for an empty snapshot — an empty write would be a spurious turn", len(frames))
			}
		})
		t.Run(pol.name+"/status only", func(t *testing.T) {
			frames := buildInjection(qum1062Snapshot([]string{"only-a-status-line\n"}), pol.p)
			if len(frames) != 1 {
				t.Fatalf("frames = %d, want 1 — the lines are already off disk, so emitting nothing loses them permanently", len(frames))
			}
			if !strings.Contains(frames[0].body, "only-a-status-line") {
				t.Errorf("status-only frame does not carry the line:\n%s", frames[0].body)
			}
			if len(frames[0].entryIDs) != 0 {
				t.Errorf("status-only frame entryIDs = %v, want none — there is nothing to MarkDelivered", frames[0].entryIDs)
			}
		})
	}
}

// --- ROW 1 + ROW 5: the fields that are not observable at this layer ----------

// TestQUM1062_Policy_ProductionPoliciesMatchTheirPaths pins the two policy fields
// whose EFFECT is not visible in buildInjection's output: the serialising mutex
// and the write-timeout seam.
//
// Stated plainly because it is the weakest test in this file: this asserts the
// policy VALUES, i.e. mechanism, not outcome. Their outcomes are pinned elsewhere
// and this test exists to catch a silent swap during the refactor:
//
//   - mu — weave serialises, the child does not. Outcome pinned by
//     TestWeaveRuntimeHandle_ConcurrentWakeForDelivery_NoDuplicateWrite, whose
//     recorded mutation (delete the lock) goes red on the first iteration. Note
//     that test keys on STATUS LINES as well as maildir entries, which is what
//     makes it a test of drainMu rather than of the in-flight filter: status lines
//     carry no entry ID, so the filter cannot suppress a concurrent double-read of
//     them. The child's nil is the declared QUM-1066 TOCTOU residual — see the
//     comment on childDrainPolicy.
//   - writeTimeout — a FUNC seam, not a value, so the child keeps following
//     childDrainWriteTimeout when a test overrides it. Outcome pinned by the
//     bounded-write tests in qum1072_child_drain_bounded_write_test.go.
func TestQUM1062_Policy_ProductionPoliciesMatchTheirPaths(t *testing.T) {
	var mu sync.Mutex

	weave := weaveDrainPolicy(&mu)
	if weave.mu == nil {
		t.Error("weave policy mu = nil, want the handle's drainMu — weave takes concurrent pokes from independent MCP handler goroutines, and the destructive status_change read is unsafe to run twice at once")
	}

	child := childDrainPolicy()
	if child.mu != nil {
		t.Error("child policy mu != nil — the child drain is deliberately unserialised (QUM-1066 residual). Serialising it here would be a behaviour change this issue does not authorise; file it instead")
	}

	// --- the writeTimeout row: assert WIRING, not resting value -----------------
	//
	// This row is the one that cannot be checked by comparing numbers, because
	// childDrainWriteTimeout is SEEDED FROM weaveDrainWriteTimeout — so both
	// policies return the same 5s at rest and `weave.writeTimeout() ==
	// weaveDrainWriteTimeout` is satisfied by EITHER wiring. Cross-wiring weave to
	// childDrainWriteTimeout.get passed the entire package.
	//
	// So the override below does double duty, and the ORDER of these four
	// statements is the whole test:
	//
	//   - the child policy is built BEFORE the override, so a policy that captured
	//     its duration by value returns the stale number and fails (the "dead
	//     seam" mutation);
	//   - the assertions run AFTER it with a distinctive value, so the two rows are
	//     separable at all: weave must NOT move while the child does (the
	//     "cross-wired seam" mutation).
	//
	// Both mutations look correct in review and are identical at rest. Only the
	// divergence under an override tells them apart.
	prev := childDrainWriteTimeout.get()
	t.Cleanup(func() { childDrainWriteTimeout.set(prev) })
	childDrainWriteTimeout.set(7 * time.Millisecond)

	if got := child.writeTimeout(); got != 7*time.Millisecond {
		t.Errorf("child writeTimeout = %v, want 7ms — the policy must read childDrainWriteTimeout through a LIVE func seam; capturing it by value at construction time silently unbinds every bounded-write test", got)
	}
	if got := weave.writeTimeout(); got != weaveDrainWriteTimeout {
		t.Errorf("weave writeTimeout = %v, want %v — weave FOLLOWED a child-side override, so its bound is cross-wired to childDrainWriteTimeout. Weave's bound is a const on purpose: nothing overrides it, and wiring it to the child's atomicDuration would make it test-overridable and drag it to 150ms inside withShortChildDrainTimeout — unpinned in exactly the tests that exist to pin bounds", got, weaveDrainWriteTimeout)
	}
}

// TestQUM1062_Policy_RowsAreDistinguishableAtRest is the generalisation of the
// bug above, so the next row to become identical at rest is caught by a test
// rather than by QA.
//
// A policy row whose two values are EQUAL when nothing is overridden cannot be
// verified by comparing values — any assertion passes under a cross-wiring that
// swaps one side for the other. writeTimeout was exactly that and went unnoticed
// through implementation and review; it is asserted by identity above instead.
//
// This audits every other row and requires it to differ at rest. It is deliberately
// a whitelist of ONE: if a future row becomes identical, this fails and the author
// has to either make it differ or assert it by wiring.
func TestQUM1062_Policy_RowsAreDistinguishableAtRest(t *testing.T) {
	var mu sync.Mutex
	w, c := weaveDrainPolicy(&mu), childDrainPolicy()

	for _, row := range []struct {
		name       string
		same       bool
		assertedBy string // non-empty = knowingly identical at rest
	}{
		{"mu", (w.mu == nil) == (c.mu == nil), ""},
		{"interruptPriority", w.interruptPriority == c.interruptPriority, ""},
		{"coalesceInterrupts", w.coalesceInterrupts == c.coalesceInterrupts, ""},
		{"ackInterruptOnWrite", w.ackInterruptOnWrite == c.ackInterruptOnWrite, ""},
		{"logPrefix", w.logPrefix == c.logPrefix, ""},
		{
			"writeTimeout",
			w.writeTimeout() == c.writeTimeout(),
			"TestQUM1062_Policy_ProductionPoliciesMatchTheirPaths, by overriding the child's atomicDuration and requiring weave NOT to follow",
		},
	} {
		switch {
		case row.same && row.assertedBy == "":
			t.Errorf("policy row %q is IDENTICAL on both paths at rest, and nothing declares that. A value comparison cannot distinguish the two wirings, so any assertion on it passes under a cross-wiring mutation. Either make the values differ, or assert the row by wiring (override one side and require the other not to follow) and record that here.", row.name)
		case !row.same && row.assertedBy != "":
			t.Errorf("policy row %q now DIFFERS at rest but is still listed as knowingly identical (asserted by %s). Drop the exemption — a plain value comparison is sufficient again.", row.name, row.assertedBy)
		}
	}
}

// TestQUM1062_Policy_LogPrefixIsPerPath pins that the two paths keep distinct
// WARN message prefixes. Live tests match on the literal
// "unified-runtime: drainPendingToStdin write failed", so collapsing the two
// prefixes to one string would break them — and, more importantly, would make a
// fleet log unable to say which agent class lost a notification.
func TestQUM1062_Policy_LogPrefixIsPerPath(t *testing.T) {
	if got := weaveDrainPolicy(nil).logPrefix; got != "weave-runtime" {
		t.Errorf("weave logPrefix = %q, want \"weave-runtime\"", got)
	}
	if got := childDrainPolicy().logPrefix; got != "unified-runtime" {
		t.Errorf("child logPrefix = %q, want \"unified-runtime\"", got)
	}
}

// allDrainPolicies enumerates every production policy, so an invariant test
// cannot silently stop covering a policy someone adds later. Hardcoding the two
// at each call site was the earlier shape and it let a third escape unchecked.
func allDrainPolicies() []struct {
	name string
	p    drainPolicy
} {
	return []struct {
		name string
		p    drainPolicy
	}{
		{"weave", weaveDrainPolicy(nil)},
		{"child", childDrainPolicy()},
	}
}

// coalescingPreconditionViolation reports why p's coalescing configuration is
// invalid, or "" if it is sound. Both preconditions are documented on
// drainPolicy's fields; this is what makes them checkable rather than aspirational.
//
// Extracted as a named predicate rather than inlined so a test can assert BOTH
// polarities — that it rejects the invalid shapes and accepts the production ones.
// An inline condition can only ever demonstrate the second.
func coalescingPreconditionViolation(p drainPolicy) string {
	if !p.coalesceInterrupts {
		return ""
	}
	if p.ackInterruptOnWrite {
		return "coalesceInterrupts with ackInterruptOnWrite: the coalesced frame carries async entries, so acking it on write marks ordinary inbox mail delivered before the CLI consumed it — the durability trade rejected for the async tier. buildInjection ignores the flag when coalescing, so this is a silent no-op today and a real weakening the moment someone wires it through"
	}
	if p.interruptPriority != drainAsyncPriority {
		return "coalesceInterrupts with interruptPriority " + p.interruptPriority + ": coalescing emits one frame at that priority, so interrupt entries would ride the async frame and be silently DEMOTED — coalescing is only sound when both classes already share a priority"
	}
	return ""
}
