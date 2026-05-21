# QUM-619 — Idle-recipient + interrupt=true race cancels the just-injected notification

**Date:** 2026-05-21
**Author:** trace (researcher) — investigation only. No code changes.
**Scope:** Map the full code path, characterize the race, evaluate fix candidates.

## TL;DR

When an agent is idle and the sender calls
`send_message({interrupt: true})`, sprawl does **two things in parallel** on
the recipient:

1. Enqueues a `ClassInterrupt` `QueueItem` onto the recipient's runtime queue.
   This is the work that ultimately becomes a `<system-notification>` user
   prompt on claude's stdin. The `Enqueue` pokes the queue signal channel
   immediately, so the parked `TurnLoop` goroutine wakes up and races to call
   `Session.StartTurn`.
2. Calls `Session.Interrupt(ctx)` **unconditionally** on the recipient's
   backend session. This writes a `control_request{subtype=interrupt}` frame
   to the same claude stdin transport that `StartTurn` writes the
   `user`-prompt frame on.

The two writes go to the same underlying transport on **different goroutines
with no ordering guarantee**. When the user-message wins the transport-send
mutex first (which is the empirically-observed case on an idle recipient
because `TurnLoop` is already parked at the queue-signal select and only has
to do `DrainAll` + `buildCompositePrompt` before its `StartTurn` call),
claude:

* receives the `user` frame → emits a `system:init` / starts the turn → the
  `<system-notification>` is briefly visible in its stream;
* receives the `control_request{interrupt}` microseconds later → aborts the
  turn it just started → the prompt is effectively never processed.

The bug is **not** in the inboxprompt formatter, the maildir notifier, the
TUI surface, or the queue. The bug is one layer up: `ForceInterruptForDelivery`
unconditionally issues `Session.Interrupt` against an idle recipient where
there is no in-flight turn to interrupt — only the **just-started turn**
that exists solely to deliver the new prompt. The interrupt then cancels
the very turn it caused.

The cleanest fix is to make `ForceInterruptForDelivery` gate the
`Session.Interrupt` call on `turnRunning == true` **at lock-snapshot time**.
The `ClassInterrupt` queue item plus its naturally-poked signal is sufficient
to deliver the message at the next turn boundary (priority-sorted ahead of
any `async`/`inbox` items already queued).

This is closely related to but distinct from QUM-549/QUM-552
(interrupt-during-MCP-wait) and from the 2026-05-14 SDK-internal-queue
forensic (`notification-injection-race-2026-05-14.md`). Those bugs are about
*observable progress while a turn IS in flight*; QUM-619 is about
*interrupts colliding with their own delivery turn while the recipient is
idle*.

## Reproduction symptom

dmotles, 2026-05-21, observed live across:

* weave → engineer/researcher/manager child
* manager → its own child

Identical surface. Confirmed in code: same `Real.SendMessage` path, same
`unifiedHandle.ForceInterruptDelivery` plumbing, same `UnifiedRuntime`. The
agent type does not appear anywhere on the hot path — both the sender's
caller-resolution and the recipient's runtime handle are agent-type-agnostic.

## Full code path from `send_message(interrupt=true)` to claude stdin

References are at lines current to the worktree on
`dmotles/qum-619-idle-interrupt-race`.

### Sender side (interrupting agent)

* `internal/sprawlmcp/server.go` — `toolSendMessage` resolves caller identity
  and forwards to `Real.SendMessage(ctx, to, body, interrupt=true)`.
* `internal/supervisor/real.go:1225` — `Real.SendMessage`:
  1. Validates the recipient exists and (for `interrupt=true`) the caller is
     an ancestor.
  2. `messages.Send(sprawlRoot, caller, to, "", body)` — writes a maildir
     envelope under `.sprawl/messages/<to>/new/` and fires the process-level
     `defaultNotifier` (which in TUI mode emits an `InboxArrivalMsg` banner
     in weave's viewport but is otherwise **not** in the prompt-injection
     path).
  3. `agentloop.Enqueue(...)` — writes the envelope under
     `.sprawl/agents/<to>/queue/pending/<id>.json` with `Class =
     ClassInterrupt`.
  4. `runtime.ForceInterruptDelivery()` — see next section.

### Recipient side (idle agent's runtime handle)

* `internal/supervisor/runtime_launcher.go:449` —
  `unifiedHandle.ForceInterruptDelivery`:

  ```go
  func (h *unifiedHandle) ForceInterruptDelivery() error {
      h.drainPendingToQueue()
      return h.rt.ForceInterruptForDelivery(context.Background())
  }
  ```

* `internal/supervisor/runtime_launcher.go:454` —
  `unifiedHandle.drainPendingToQueue` lists envelopes from
  `queue/pending/`, splits by class, and **`Enqueue`**s a single
  `runtimepkg.QueueItem{Class: ClassInterrupt, Prompt:
  inboxprompt.BuildInterruptFlushPrompt(...)}` on the runtime queue.

* `internal/runtime/queue.go:81` — `MessageQueue.Enqueue`:

  ```go
  q.items = append(q.items, item)
  q.mu.Unlock()
  select {
  case q.signal <- struct{}{}:  // ← parked TurnLoop wakes here
  default:
  }
  ```

  This is the **first wake**. As soon as `Enqueue` returns, the parked
  `TurnLoop` goroutine is unblocked from `<-q.Signal()` and will race to
  `DrainAll` → `buildCompositePrompt` → `Session.StartTurn`.

* `internal/runtime/unified.go:458` — `UnifiedRuntime.ForceInterruptForDelivery`:

  ```go
  rt.mu.Lock()
  if rt.stopped { ... return nil }
  sess := rt.cfg.Session
  loop := rt.turnLoop
  if rt.state == StateTurnActive { rt.state = StateInterrupting }
  rt.mu.Unlock()

  if sess != nil { _ = sess.Interrupt(ctx) }     // ← writes interrupt control_request
  if loop != nil { _ = loop.Interrupt(ctx) }     // ← no-op when interruptCh is nil
  rt.queue.Wake()                                 // ← redundant; signal already poked
  return nil
  ```

  Note: no `turnRunning` check guards `sess.Interrupt`. The QUM-549 commit
  message in the trailing comment block (lines 481–488) is explicit about
  why: the predecessor `interruptForDelivery` had a conditional-on-snapshot
  gate that produced TOCTOU bugs (QUM-462 / QUM-510), so the successor
  `ForceInterruptForDelivery` was made unconditional. That fixed the
  "interrupt against a turn that just started" no-op race but introduced
  this one (interrupt against a turn that hasn't started yet but is about
  to be started **solely to deliver this very interrupt's message**).

### TurnLoop goroutine — concurrent with the above

* `internal/runtime/turnloop.go:95` — `TurnLoop.Run`:

  ```go
  for {
      ...
      items := l.cfg.Queue.DrainAll()        // ← pulls our ClassInterrupt item
      if len(items) > 0 {
          prompt := buildCompositePrompt(items)
          l.executeTurn(ctx, prompt, items)  // ← writes user-message to stdin
          ...
      }
      ...
      case <-l.cfg.Queue.Signal():           // ← was parked here
  }
  ```

* `internal/runtime/turnloop.go:201` — `executeTurn` → `Session.StartTurn(turnCtx, prompt)`.

* `internal/runtime/unified.go:179` — `stateTrackingSession.StartTurn`:

  ```go
  s.rt.mu.Lock()
  if s.rt.state == StateIdle { s.rt.state = StateTurnActive }
  s.rt.turnRunning = true                    // ← but this happens AFTER the
                                              //   sender already snapshotted turnRunning
  pending := s.rt.pendingInterrupt
  s.rt.pendingInterrupt = false
  s.rt.mu.Unlock()
  ch, err := s.inner.StartTurn(...)          // ← writes user frame
  ```

* `internal/backend/session.go:392` — `session.StartTurn` ultimately calls
  `s.transport.Send(ctx, protocol.UserMessage{...})` (line 451). This is
  the stdin write that lands the `<system-notification>` user prompt at
  claude.

* `internal/backend/session.go:875` — `session.Interrupt` calls
  `s.transport.Send(ctx, protocol.InterruptRequest{...})`. Both writes go
  through the same transport's `Send` mutex. They serialize, but in
  **whichever order the goroutines reach the mutex**.

## Timing diagram of the race

Two concurrent goroutines, both writing to the same claude-stdin transport:

```
sender goroutine                       │  TurnLoop goroutine (parked on signal)
                                       │
Real.SendMessage                       │
 ├─ messages.Send (maildir)            │
 ├─ agentloop.Enqueue (queue/pending/) │
 ├─ ForceInterruptDelivery             │
 │    └─ drainPendingToQueue           │
 │         └─ q.Enqueue ──────────────►│  wakes from <-Signal()
 │              (pokes q.signal)       │  → DrainAll
 │                                     │  → buildCompositePrompt
 │    └─ rt.ForceInterruptForDelivery  │  → executeTurn → StartTurn
 │         ├─ mu.Lock/Unlock           │       ├─ stateTrackingSession.StartTurn
 │         ├─ sess.Interrupt(ctx) ────►│       │    └─ rt.mu.Lock (sets turnRunning=true)
 │         │   └─ transport.Send       │       │       (sender already past its snapshot)
 │         │       (interrupt frame)   │       │    └─ rt.mu.Unlock
 │         ├─ loop.Interrupt (no-op)   │       └─ s.inner.StartTurn
 │         └─ q.Wake() (no-op)         │             └─ transport.Send (user frame)
                                       │
                ╲                  ╱   │
                 ╲   transport.Send.mutex
                  ╲    serializes      │
                                       │
   IF user-frame wins:                 │
     claude receives `user` first,     │
     starts a turn, emits system:init, │
     then receives `interrupt`,        │
     aborts the just-started turn.     │
     → prompt content discarded.       │  ← BUG: matches reported symptom
                                       │
   IF interrupt-frame wins:            │
     claude has no current turn,       │
     interrupt is a no-op,             │
     then receives `user` frame,       │
     turn proceeds, prompt processed.  │  ← happy path (probably rare on idle)
```

`stateTrackingSession.StartTurn` does eagerly set `turnRunning=true` under
`rt.mu` (line 187) before calling `s.inner.StartTurn`, but the sender's
snapshot of `turnRunning` was taken **before** this `Lock`. By the time the
sender's snapshot was taken (just before `Unlock` at line 469), the
TurnLoop goroutine may not have entered `stateTrackingSession.StartTurn`'s
critical section yet — depending on goroutine scheduling, `Enqueue`'s
signal poke may have taken longer to wake the parked TurnLoop than the
sender's own `Lock/Unlock` and `sess.Interrupt` setup. There is no lock
serialization across the two events. The QUM-549 lesson explicitly warns
against conditional-on-snapshot gating here, which is why the current code
is unconditional — see the trailing comment in `unified.go:481-488`.

### Why this isn't a `pendingInterrupt`-vs-StartTurn race in the QUM-462 sense

`UnifiedRuntime.Interrupt` (not `ForceInterruptForDelivery`) does set
`pendingInterrupt = true` when called against an idle runtime. The
`stateTrackingSession.StartTurn` wrapper consumes that flag and routes
through `loop.Interrupt` **after** `s.inner.StartTurn` returns nil. But the
ordering inside `StartTurn` is still `transport.Send(user-frame)` →
`loop.Interrupt`, and `loop.Interrupt` ultimately writes a control_request
to the same transport. So **even the `pendingInterrupt` path has the same
race** if it were used here — the user-frame still goes out first, then the
interrupt cancels it. The `pendingInterrupt` mechanism fixes "the interrupt
gets lost entirely because there was no turn to interrupt yet" — it does
not fix "the interrupt cancels its own delivery turn." For
`ForceInterruptForDelivery` the `pendingInterrupt` path is not even
involved; the call goes direct to `sess.Interrupt`.

## Why the symptom looks like "the message disappears"

* The maildir envelope is written by `messages.Send` first (Real.SendMessage
  step 2), and it stays on disk. From the sender's POV the message **was**
  delivered: `mcp__sprawl__messages_send` returns a short ID. The defaultNotifier
  fires once and the TUI banner appears in weave.
* The `queue/pending/<id>.json` envelope is written by `agentloop.Enqueue`
  before the runtime is poked.
* The `ClassInterrupt` `QueueItem` is consumed by `DrainAll` and its prompt
  is written to claude's stdin.
* But because claude aborts the turn the moment after the user-frame lands,
  the model never invokes the `mcp__sprawl__messages_read` tool that would
  read the maildir envelope or post a response.
* The next time **anything** wakes the recipient (a peer message, a status
  drain, an interrupt that actually preempts a running turn, etc.) the
  recipient processes the queue normally — but the `ClassInterrupt` item
  was already drained on the aborted turn, so the envelope is in
  `queue/delivered/` (or it wasn't, depending on the QUM-579 timing — see
  Open Questions). The maildir envelope is also still in `new/`. So the
  message *does* eventually surface, but only as a stale unread item on the
  next genuine wake event — minutes or hours later — and looks to the
  sender like a silently-dropped delivery.

## Manager-as-recipient

Identical. Managers and non-manager children both go through
`newInProcessUnifiedStarter` → `UnifiedRuntime` → `unifiedHandle`. There is
no agent-type branch on the recipient's hot path
(`Real.SendMessage` → `ForceInterruptDelivery` →
`ForceInterruptForDelivery` → `Session.StartTurn`/`Session.Interrupt`). The
only place agent type matters is the prompt template
(`buildAgentSystemPrompt`), which is consumed once at runtime startup and
unrelated. So the bug surface is the same for manager recipients; dmotles
should expect the same symptom there.

The **weave**-as-recipient case is theoretically distinct because weave uses
a different runtime handle (`WeaveRuntimeHandle`, not `unifiedHandle`).
However, weave is the root agent and cannot be a `send_message(interrupt=true)`
recipient under §8.5 ancestor-only gating (no agent is an ancestor of
weave). So that case is unreachable in production.

## Fix candidates — ranked

### (A) — Recommended: conditional-on-snapshot `Session.Interrupt` in `ForceInterruptForDelivery`

```go
func (rt *UnifiedRuntime) ForceInterruptForDelivery(ctx context.Context) error {
    rt.mu.Lock()
    if rt.stopped { rt.mu.Unlock(); return nil }
    sess := rt.cfg.Session
    loop := rt.turnLoop
    turnRunning := rt.turnRunning  // snapshot under lock
    if rt.state == StateTurnActive { rt.state = StateInterrupting }
    rt.mu.Unlock()

    if turnRunning {
        if sess != nil { _ = sess.Interrupt(ctx) }
        if loop != nil { _ = loop.Interrupt(ctx) }
    }
    rt.queue.Wake()
    return nil
}
```

**Reasoning.** The `ClassInterrupt` queue item is already enqueued (by
`drainPendingToQueue` in the calling layer) and the queue signal is
already poked (by `Enqueue`). The recipient's `TurnLoop` will wake, see
the `ClassInterrupt` item at the head of `DrainAll`'s priority-sorted
output, and start a turn whose only purpose is to deliver this message.
There is no turn to interrupt; calling `Session.Interrupt` would only
cancel the delivery turn we just caused. So we only need
`Session.Interrupt` when a turn is genuinely in flight (i.e. there's
existing work we want to preempt).

**Why doesn't this re-introduce the QUM-462/QUM-510 TOCTOU bugs?** Those
bugs were about the OPPOSITE failure mode: the sender saw `turnRunning=false`
under the lock, but the TurnLoop transitioned to `turnRunning=true`
immediately after, and the sender's "no-op because idle" decision caused
the interrupt to never reach claude — interrupt was silently lost. Under
this fix:

* Sender saw `turnRunning=false`, TurnLoop then transitions to
  `turnRunning=true` and writes the user-frame → no interrupt is sent →
  **the turn proceeds normally and delivers the interrupt-class prompt.**
  No bug; the message is processed. The QUM-462/QUM-510 failure mode
  ("interrupt is lost") does not apply because we don't actually want to
  interrupt this brand-new turn — we want it to run.
* Sender saw `turnRunning=true`, calls `Session.Interrupt`. The
  pre-existing turn aborts; TurnLoop loops back to `DrainAll`, picks up
  the `ClassInterrupt` item (still in the queue — it was enqueued *while*
  the prior turn was running, so the prior turn's `DrainAll` did not see
  it), and starts a new turn with the interrupt-class prompt. Works.
* `Session.Interrupt` while there is no current turn is documented as a
  no-op per the `SessionHandle` contract in `turnloop.go:33-35`, so even
  in a rare interleaving where `turnRunning` flips false between snapshot
  and call, the behaviour degrades gracefully.

**Drawbacks.**

* The `loop.Interrupt(ctx)` call also becomes conditional. That is
  consistent: `loop.Interrupt` is a no-op when `interruptCh` is nil
  (no in-flight turn), so it's load-bearing only when `turnRunning=true`.
* A correctness witness would be a test that sets up an idle recipient,
  fires `send_message(interrupt=true)`, and asserts that
  `Session.Interrupt` was NOT called (via a fake session that records
  calls).

**Touch points.**

* `internal/runtime/unified.go:458` — gate `sess.Interrupt` and
  `loop.Interrupt` behind the `turnRunning` snapshot.
* `internal/runtime/unified_test.go` — new test exercising the idle
  + force-interrupt path against a recording fake session.
* `scripts/test-drain-row-inject-e2e.sh` — extend with an
  `interrupt=true` variant against an idle child, asserting the drain-row
  citation appears within the existing 90s budget.

### (B) — Not recommended: insert a short delay between Interrupt and the prompt write

dmotles's candidate 2. The implementations differ in where the delay goes:

* Sleep between `q.Enqueue(ClassInterrupt)` and `sess.Interrupt` so the
  TurnLoop has time to finish StartTurn.
* Sleep between `sess.Interrupt` and the NEXT StartTurn so claude has time
  to process the interrupt before the new turn.

**Why this is worse than (A).**

1. It treats the symptom (write-ordering race) rather than the cause
   (we issued an interrupt against a turn that exists only to deliver our
   own message).
2. Any fixed delay is wrong: too short and the race still fires under
   load; too long and every idle-recipient interrupt-flagged send adds
   that latency to a hot user-facing path (managers waking children).
3. It does not eliminate the race in the (admittedly rare) interleaving
   where the TurnLoop has not yet entered `DrainAll` when the sleep ends.
4. It adds wall-clock latency to a code path that is supposed to be
   "preempt now."
5. The asymmetry breaks the post-QUM-549 unconditional invariant in a
   way that's hard to reason about in tests.

### (C) — Possible alternative: re-route through `pendingInterrupt` instead of `sess.Interrupt`

Make `ForceInterruptForDelivery` set `pendingInterrupt = true` and call
`rt.queue.Wake()` — but skip the direct `sess.Interrupt`. The wrapper
`stateTrackingSession.StartTurn` already consumes the flag and routes
through `loop.Interrupt` after `s.inner.StartTurn` returns nil. **But this
is the same race in a different costume** — the user-frame still goes out
first, then `loop.Interrupt` ultimately writes a control_request to the
same transport. It would only help if we ALSO refactor `StartTurn`'s
pendingInterrupt branch to NOT actually interrupt the turn that's about
to deliver the queue item. Functionally identical to (A) but with a
larger surface area. Reject.

### (D) — Defense in depth: post-turn pending sweep would mask, not fix

The existing post-turn sweep (`PostTurnSweep` → `sweepCoordinator`) does a
`ListPending` reconciliation. If we made it re-fire a `WakeForDelivery`
when the just-finished turn was interrupted **and** there are still
pending envelopes for *that very interrupt*, it would self-heal — the
next turn would re-deliver the prompt. This is symptomatic and lines up
with QUM-512's defense-in-depth proposal. Worth doing as a separate
hardening regardless, but should not be the primary fix because it
papers over an unnecessary preempt.

### Recommended combination

(A) is the primary fix. Strongly consider (D) as a separate defense-in-depth
ticket so any future race in this layer is caught by self-healing rather
than by silently-dropped notifications. (B) and (C) should not be pursued.

## Acceptance test sketch

A unit-level test against `UnifiedRuntime`:

1. Construct a `UnifiedRuntime` with a fake `SessionHandle` that records all
   `Interrupt` and `StartTurn` calls and exposes a controllable "turn in
   flight" state.
2. `rt.Start()`; queue is empty; `rt.State() == StateIdle`;
   `rt.turnRunning == false`.
3. Enqueue a `ClassInterrupt` item directly (simulating
   `drainPendingToQueue`).
4. Call `rt.ForceInterruptForDelivery(ctx)`.
5. Assert: `fakeSession.InterruptCallCount() == 0`. The `StartTurn` call
   eventually fires from the TurnLoop with the expected prompt.

An e2e regression: extend `scripts/test-drain-row-inject-e2e.sh` (or add
`scripts/test-idle-interrupt-inject-e2e.sh`) — spawn an engineer child,
wait for it to be idle (`state.json.last_report_message` set, no in-flight
turn), then have weave call `send_message(to=child, body=…, interrupt=true)`
with a unique sentinel, and assert that the child's `activity.ndjson` and
the drain-row citation in weave's pane both show the sentinel within the
existing 90s budget (likely far faster).

## Open questions / what I'd investigate next

* **What does claude internally do** when the `user` frame and `control_request{interrupt}` arrive back-to-back? The current understanding (turn starts then is aborted) is reverse-engineered from the symptom. Direct evidence would require either claude-side telemetry or a recorded JSONL session from the bug. If the actual behavior is that the interrupt aborts the **entire connection's user-prompt queue** (rather than just the current turn), the fix may need to be more aggressive (drain the prompt rather than rely on the queue item surviving to the next turn). Worth getting one captured session from dmotles.
* **QUM-579 OnQueueItemDelivered timing on aborted turns.** The post-2026-05-12 timing fires `OnQueueItemDelivered` on the first non-`system:init` frame. If the interrupt aborts the turn after `system:init` but before any other frame, the callback does NOT fire — so the envelope stays in `queue/pending/` and a future wake re-delivers it. That's actually a good property for fix (A) recovery scenarios, but it also means the post-turn sweep in (D) needs to consider both "still in pending/" and "moved to delivered/ but turn was interrupted" cases. Confirm by checking `sweepCoordinator` + the QUM-580 semantics — I did not exhaustively read `sweep_coordinator.go`.
* **Empirical write-order distribution.** I asserted that the user-frame wins the transport-mutex "often" on idle recipients because the parked TurnLoop has cheap work to do before its `StartTurn`. A microbenchmark / strace of the bug would settle this. Practically (A) doesn't depend on knowing the distribution — it eliminates the race entirely.
* **Manager-as-recipient confirmation.** dmotles flagged this as untested at issue-file time. The code analysis above says it's the same code path; live confirmation should match.
* **Does `loop.Interrupt` need the same gating?** In `ForceInterruptForDelivery` it's effectively a no-op when `turnRunning=false` (interruptCh is nil → returns nil), so the gate is a defensive consistency change rather than a behavior change. I would gate it anyway, for readability and to make the function's preconditions match what `Session.Interrupt` is being told.

## Reflections

* **Surprise.** I expected the bug to be in the maildir-watch / defaultNotifier path because the issue title says "cancels the inbound message notification." It's not — the defaultNotifier is purely a TUI banner emitter. The actual "notification" being cancelled is the `<system-notification type="message">` user-prompt frame the TurnLoop writes after dequeueing the `ClassInterrupt` item. Easy to conflate; worth being precise about in the recommendation comment.
* **Tension with QUM-549.** The QUM-549 fix made `ForceInterruptForDelivery` unconditional specifically to close the "interrupt against a turn that just-started racily" gap. Re-introducing a conditional gate would seem to undo that — but the gate proposed in (A) is conditional on `turnRunning` at *snapshot time*, not on `state == StateTurnActive`, and the failure mode it would re-introduce ("interrupt is silently lost") is **structurally impossible** because the cooperative wake path is preserved (`queue.Enqueue` + `queue.Wake` both run). The QUM-549 lesson was "don't drop the wake-and-deliver path"; (A) does not drop it. The QUM-619 lesson is "don't *also* preempt a turn that doesn't exist." The two are not in conflict; they live on different axes.
* **Open if I had more time.** I'd capture one real session JSONL with the bug active and verify exact timing of `user` vs `control_request` write order at the transport layer, plus what claude emits in between. That would settle the "what does claude actually do" open question and validate the (A) test sketch.
