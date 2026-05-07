# Manager Wake-Loss When Child `report_status` Races Turn-End

**Date:** 2026-05-07
**Investigator:** ghost (researcher under weave)
**Linear:** QUM-510
**Branch:** `dmotles/wake-loss-investigation`
**Time-boxed:** ~30 min static analysis (no live repro)

## Symptom Recap

During tower's QUM-504..509 cleanup wave on 2026-05-07T04:04:58Z, two events
landed in the same second on tower's runtime:

1. The `make validate` background bash task completed (Claude-emitted
   `task_notification`).
2. Child engineer `ratz` invoked `mcp__sprawl__report_status{state:"complete"}`
   for QUM-506.

Tower's Claude subprocess produced exactly **one** turn at 04:04:58Z. That turn
saw the validate output and replied "Wave 1 integration validate green.
Awaiting ratz on QUM-506" — i.e. tower's reply was generated as if ratz had
**not** completed.

Tower then sat idle for 12+ minutes until weave's 04:17:29Z `send_async` nudge
re-woke it, after which tower correctly merged QUM-506. Meanwhile ratz's
status report was visible in tower's inbox via `peek` / `messages_list` — the
on-disk message was fine, it just never reached a turn prompt.

## Threat Model: How Should This Path Work?

Under the unified runtime (post-QUM-399/QUM-400), child→parent `report_status`
notifications follow this path (all in the supervisor process):

```
ratz toolReportStatus
  → Real.ReportStatus(ctx, "", "complete", summary, detail)            // real.go:894
      → state.LoadAgent(ratz)                                          // parent="tower"
      → parentRuntime, _ = r.startedRuntime("tower")
      → agentops.Report(...)
          → messages.Send(.../tower/new/...)                           // maildir
          → agentloop.Enqueue(.../tower/queue/pending/<seq>-async-<uuid>.json)
      → parentRuntime.InterruptDelivery()                              // real.go:916
          → unifiedHandle.InterruptDelivery()                          // runtime_launcher.go:299
              → agentloop.ListPending("tower")                         // reads pending dir
              → SplitByClass → asyncs = [ratz_entry]
              → rt.Queue().Enqueue({Class:Inbox, Prompt:..., EntryIDs:[ratz_id]})
              → rt.InterruptDelivery(ctx)                              // unified.go:368
                  → if started && turnRunning: rt.Interrupt(ctx)       // unified.go:378
                  → rt.queue.Wake()                                    // unblock TurnLoop signal
```

The TurnLoop (`internal/runtime/turnloop.go:71-113`) is supposed to:
1. finish whatever turn was running (interrupted or naturally),
2. fall back to the top of its `for` loop,
3. `DrainAll()` the queue (which now contains the ratz inbox item),
4. `executeTurn()` against the prompt rendered from that item,
5. fire `OnQueueItemDelivered` → `agentloop.MarkDelivered(ratz_id)` after the
   turn lands successfully.

When weave's later `send_async` worked correctly, it followed the same path —
which strongly implies the wiring itself is fine. **The bug is specific to
the "`report_status` arrives while parent is mid-turn (long background task)"
case.**

## Root Cause: `pendingInterrupt` Arming Race in `rt.Interrupt`

The strongest candidate root cause I can identify by static analysis is a
**TOCTOU race between `rt.InterruptDelivery` and the `stateTrackingSession`
wrapper goroutine that flips `turnRunning` back to false when the inner
event channel closes**. The race arms `rt.pendingInterrupt = true`
spuriously, which causes the *very next* turn — the one that would deliver
ratz's report — to be immediately interrupted by `stateTrackingSession.StartTurn`
before Claude can act on the prompt.

### The race in detail

`internal/runtime/unified.go:368` reads `turnRunning` under `RLock`, releases
the lock, then conditionally calls `rt.Interrupt`:

```go
func (rt *UnifiedRuntime) InterruptDelivery(ctx context.Context) error {
    rt.mu.RLock()
    started := rt.started
    stopped := rt.stopped
    turnRunning := rt.turnRunning   // <-- snapshot
    rt.mu.RUnlock()                 // <-- lock released

    if stopped { return nil }
    if started && turnRunning {     // <-- decided from stale snapshot
        _ = rt.Interrupt(ctx)
    }
    rt.queue.Wake()
    return nil
}
```

`internal/runtime/unified.go:324` then re-acquires the lock and **decides
based on a fresh read of `turnRunning`**:

```go
func (rt *UnifiedRuntime) Interrupt(ctx context.Context) error {
    rt.mu.Lock()
    ...
    turnRunning := rt.turnRunning
    state := rt.state
    if state == StateTurnActive {
        rt.state = StateInterrupting
    } else if !turnRunning {
        // Queue items may be pending but the wrapper hasn't entered StartTurn yet.
        // Arm a pending-interrupt flag so the next StartTurn classifies its
        // terminal event as EventInterrupted (not EventTurnCompleted).
        rt.pendingInterrupt = true   // <<< QUM-510 trap
    }
    rt.mu.Unlock()
    ...
}
```

Meanwhile `stateTrackingSession.StartTurn`'s post-turn goroutine
(`internal/runtime/unified.go:181-188`) flips both fields atomically when
the backend's events channel closes:

```go
// Channel closed: turn ended (success, failure, or interrupt).
rt.mu.Lock()
rt.turnRunning = false
if rt.state != StateStopped {
    rt.state = StateIdle
}
rt.mu.Unlock()
```

The dangerous interleaving:

| Step | Goroutine | Action | State after |
|------|-----------|--------|-------------|
| 1 | TurnLoop / wrapper | tower's long turn is running | `state=TurnActive`, `turnRunning=true` |
| 2 | Supervisor (ratz report) | `InterruptDelivery` RLock → reads `turnRunning=true` → unlock | unchanged |
| 3 | Wrapper goroutine | Inner events channel closes; Lock; `turnRunning=false`, `state=Idle`; unlock | `state=Idle`, `turnRunning=false` |
| 4 | Supervisor (cont.) | Calls `rt.Interrupt(ctx)`; Lock; reads `state=Idle`, `turnRunning=false` | `pendingInterrupt=true` ← **the trap** |
| 5 | TurnLoop | `executeTurn` returns true; `OnQueueItemDelivered([prior items])`; `continue`; `DrainAll()` returns `[ratz_inbox]` | queue empty, ratz item in flight |
| 6 | TurnLoop | `executeTurn(ratz_prompt)` → `stateTrackingSession.StartTurn` Lock; reads `pendingInterrupt=true`; sets it false | `pending=true` captured |
| 7 | Wrapper | Calls inner.StartTurn (Claude receives prompt); spawns forwarder; THEN since `pending==true`, calls `loop.Interrupt(...)` (`unified.go:160-165`) | `thisTurn ← {}` |
| 8 | TurnLoop | `executeTurn` select: hits `<-thisTurn` → `Session.Interrupt(...)`, `interrupted=true`; drains until events closes | Claude sees prompt + interrupt back-to-back; emits short `result` |
| 9 | TurnLoop | `EventInterrupted` published; `OnQueueItemDelivered([ratz_id])` fires → `agentloop.MarkDelivered(ratz_id)` moves the file pending/→delivered/ | ratz's pending entry is **gone** |
|10 | Tower | Sits idle. Next `ListPending(tower)` returns nothing for ratz. Tower has no in-session memory of "ratz complete" because Claude was interrupted before processing the prompt content. |

**This is the same class of bug as QUM-462** ("`InterruptDelivery` arms
`pendingInterrupt` when idle → next turn immediately interrupted, no work
delivered"). QUM-462's fix added the `if started && turnRunning` guard at
the `rt.InterruptDelivery` layer — but the guard's read is **non-atomic
with respect to the `rt.Interrupt` it gates**, so the race can still slip
through when a turn ends in the same scheduler quantum as the inbound
report.

The QUM-462 commit message ("InterruptDelivery no longer arms
pendingInterrupt when idle") describes the *intent*; the *implementation*
gates by a stale snapshot, which is correct only when there is no mid-turn
boundary crossing. With long background-task turns ending in the same
second as a child completion (exactly the QUM-510 timing), the boundary
crossing is inevitable.

### Why the file ends up in `delivered/` despite being undelivered

`OnQueueItemDelivered` fires for any turn where `started == true`, regardless
of whether the turn was naturally completed or interrupted
(`internal/runtime/turnloop.go:90-94`). For the spuriously-interrupted
ratz turn, `started=true` (StartTurn returned a channel) and we enter the
`if started && OnQueueItemDelivered != nil` branch → `MarkDelivered` runs
on `ratz_id` even though Claude was interrupted before ingesting the
prompt.

The post-condition: pending/<ratz>.json has moved to delivered/ but no
turn ever surfaced its body to Claude. Tower's session has no recollection
of the message.

### Confirming the race is reachable

The `task_notification` arrival pattern makes this race likely-not-rare:

* `make validate` is a long background task. Tower's turn does not emit
  `result` until validate finishes (Claude SDK holds the turn open while
  background tasks run).
* So the long turn inevitably ends in the same scheduler tick that
  `task_notification` arrives — a fat target for any concurrent
  `InterruptDelivery` from a child.
* Children whose work duration is bounded by a peer's `make validate`
  tend to finish within seconds of each other, so the same-second race
  will reproduce naturally on every multi-wave manager orchestration
  whose validate gate is nontrivial.

## Alternative Hypotheses Considered (and why they're weaker)

I considered and discarded the following — listing them so the engineer
who fixes this knows they were not overlooked:

1. **Notification slot collision (`task_notification` clobbers `report_status`).**
   Inspected: tower's manager-side `task_notification` is a stream message
   handled inside Claude — it does *not* pass through Sprawl's
   notification path or share a queue slot with `mcp__sprawl__send_async`
   / `report_status`. They use disjoint datastructures. Eliminated.

2. **Two simultaneous wake events coalesced into one wake.**
   The `MessageQueue.signal` channel is intentionally coalescing
   (buffered(1), `select` with `default` on Wake). But coalescing only
   means **fewer wakeups**, not **lost items** — `DrainAll` returns
   *all* queued items regardless of how many wake pokes preceded it.
   Eliminated.

3. **`MessageQueue.Enqueue` dedup drop.**
   `Enqueue` drops when *all* `EntryIDs` are already in `q.pending`
   (QUM-460 fix). For ratz's first delivery this map is empty (DrainAll
   resets it) so the dedup branch cannot fire on the initial arrival.
   Possible if ratz reports twice with the same ID, but ratz only sent
   one report. Eliminated for this incident.

4. **`startedRuntime("tower")` returns nil.**
   Would skip `InterruptDelivery` entirely. Tower was clearly running, so
   the registry entry exists with `Lifecycle == Started`. Eliminated.

5. **`agentops.Report` returns an empty `MessageID`.**
   The supervisor gates `parentRuntime.InterruptDelivery()` on
   `res.MessageID != ""` (real.go:915). MessageID is set after a
   successful `agentloop.Enqueue` of the entry. If Enqueue fails the
   tool call returns an error — ratz's call succeeded, so MessageID was
   non-empty. Eliminated.

6. **`StartTurn` on the ratz item failed (Claude session bad).**
   Possible but not strongly supported. If StartTurn errored we'd see
   `EventTurnFailed` in tower's activity ndjson, and `OnQueueItemDelivered`
   would *not* run, so ratz's pending file would still be on disk.
   This contradicts the observation that weave's later `send_async`
   worked (a healthy session) and is also harder to trigger
   deterministically than the race in §"Root Cause". Possible secondary
   factor but unlikely to be the primary root cause.

## Reproduction Sketch (research-only — do **not** run live)

A reasonably tight unit test in `internal/runtime/unified_test.go` could
exercise the race deterministically without subprocesses:

1. Construct a `UnifiedRuntime` with a mock `SessionHandle` whose
   `StartTurn` returns an `events` channel the test controls.
2. Drive a turn: `Queue.Enqueue(initial)` → wait for
   `EventTurnStarted`. State now: `turnRunning=true`,
   `state=TurnActive`.
3. **Pause the wrapper goroutine** (e.g. via a test seam that
   gates the wrapper's post-loop lock acquisition behind a channel)
   so we can observe an inner-channel-closed/wrapper-not-yet-flipped
   intermediate state.
4. From the test goroutine: close the inner events channel
   (turn ends).
5. **Before** unblocking the wrapper goroutine, in another goroutine:
   call `rt.InterruptDelivery(ctx)`. It will read `turnRunning=true`
   from RLock and proceed to call `rt.Interrupt`.
6. **Now** unblock the wrapper. It runs first (or interleaves — both
   land via `rt.mu.Lock()`); when `rt.Interrupt` re-acquires the lock,
   it sees `turnRunning=false` and arms `pendingInterrupt=true`.
7. Enqueue a follow-up "ratz" item.
8. Assert: the next `executeTurn` is interrupted before Claude could
   process the prompt — i.e. `EventInterrupted` (not `EventTurnCompleted`)
   fires for the ratz turn. **This is the regression signature.**

The deterministic test seam is the same shape as QUM-462's regression
test (`weave_handle_test.go`'s
`TestWeaveRuntimeHandle_InterruptDelivery_TerminalEventIsCompleted_NotInterrupted`).

A full live repro would require a real manager + child + a long-ish
background `Bash run_in_background` task arranged so the child's
`mcp__sprawl__report_status` lands in the same scheduler tick as the
task_notification — annoying to set up but well within the existing
e2e harness once the unit-level repro confirms the mechanism.

## Proposed Fixes

Two viable designs, complementary rather than mutually exclusive.

### Option A — Plug the race at the source (strongly recommended)

In `internal/runtime/unified.go`, fold the `if started && turnRunning`
guard into `rt.Interrupt` itself **under the same lock**, and forbid
arming `pendingInterrupt` from inbound-delivery contexts:

```go
// Add a private variant that callers from the InterruptDelivery path use.
func (rt *UnifiedRuntime) interruptForDelivery(ctx context.Context) {
    rt.mu.Lock()
    if rt.state == StateStopped || !rt.turnRunning {
        rt.mu.Unlock()
        return
    }
    sess := rt.cfg.Session
    loop := rt.turnLoop
    if rt.state == StateTurnActive {
        rt.state = StateInterrupting
    }
    rt.mu.Unlock()

    _ = sess.Interrupt(ctx)
    if loop != nil {
        _ = loop.Interrupt(ctx)
    }
}

func (rt *UnifiedRuntime) InterruptDelivery(ctx context.Context) error {
    rt.interruptForDelivery(ctx)   // never arms pendingInterrupt
    rt.queue.Wake()
    return nil
}
```

`rt.Interrupt` (the user-Ctrl-B path) keeps its current behavior,
including the `!turnRunning → pendingInterrupt = true` branch.

**Trade-offs:**
* (+) Race-free: the entire decision is made under one lock, and the
  delivery path can no longer arm `pendingInterrupt`.
* (+) Minimal surface change. Public `Interrupt` semantics for
  user-initiated interrupts are unchanged.
* (−) Adds a private method, mild duplication with `rt.Interrupt`.
  Acceptable; the duplication is small and the single-lock invariant
  is the whole point.
* (−) Async messages no longer abort an in-flight turn that is *just
  about to end* — but `queue.Wake()` ensures the queue is drained on
  the very next iteration, which is exactly what we want.

### Option B — Skip mid-turn preemption for async-class entirely

A simpler, more conservative design change: `unifiedHandle.InterruptDelivery`
calls `rt.queue.Wake()` only, never `rt.Interrupt`. Async-class arrivals
(child reports, peer messages) wait for the current turn to end naturally
and are picked up on the next `DrainAll`. Interrupt-class arrivals
(`SendInterrupt`, ancestor→descendant) keep using `rt.Interrupt` via a
separate path (already partly the case — see `unifiedHandle.InterruptDelivery`
splitting interrupts from asyncs in runtime_launcher.go:299).

**Trade-offs:**
* (+) Eliminates the entire class of race for async deliveries. Not
  just QUM-510 but any future mid-turn `pendingInterrupt` foot-gun.
* (+) Conceptually cleaner: the interrupt class is the only one that
  semantically requires preempting the current turn. Async should not.
* (−) Average latency for async delivery to a busy parent goes up by
  the time-to-end-of-current-turn (typically seconds, occasionally
  minutes if the turn is doing a long bash). For child→manager status
  updates this is fine — managers don't need <1s reactivity.
* (−) Behavior-visible change beyond a bug fix; needs a test sweep
  for any code that relies on async preemption.

### Recommendation

Ship **Option A** as the QUM-510 fix (narrow, correct, low-risk). File
a follow-up to evaluate Option B as a separate hardening pass — the
async-preempts-turn semantics is questionable independently of this
bug, and removing it would shrink the cognitive surface of the runtime
significantly.

### Defense-in-depth (regardless of which option)

Add a "post-turn pending sweep" step in `TurnLoop.Run`'s for loop:
after every turn ends, before blocking on `Signal()`, call
`unifiedHandle.feedTasks()` *and* re-`ListPending` + re-`Enqueue` any
entries that are on disk but not in `q.pending`. This makes the
runtime self-healing against any future arming/dedup race we haven't
foreseen — abandoned pending entries always get a second chance.

The cost is one extra `os.ReadDir` per turn, which is negligible.
This step also forecloses the Hypothesis #6 ("StartTurn failed and
abandoned the queue item") class of bug if it ever does fire.

## Severity Assessment

**Severity: High** for any orchestration that depends on the
"manager delegates → background validate → child reports complete"
pattern, which is the canonical multi-wave manager workflow.

* **Affects which agents:** Any agent whose long-running turn ends in
  the same scheduler tick as a child's `report_status` /
  `send_async` arrival. Managers with `make validate`-style background
  tasks are the primary victims. Engineers and researchers can hit
  the same race for peer messages but rarely run multi-minute
  background tasks, so exposure is lower.
* **Failure mode:** Silent. The lost message is moved from `pending/`
  to `delivered/` by `OnQueueItemDelivered`, so even forensic
  recovery via `ListPending` returns nothing. The only on-disk trace
  is the ratz entry sitting in `delivered/` with no corresponding
  turn-prompt evidence in tower's activity ndjson — visible only
  with deliberate cross-reference.
* **Effect on history:** The post-mortem corpus
  `docs/forensics/tui-weave-wedge-2026-05-05.md` and several
  "manager seems to have stalled" anecdotes in
  `docs/research/weave-session-cycling-2026-05-05.md` are plausibly
  the same root cause manifesting through different observable
  surfaces. Worth a corpus pass post-fix to see whether they
  retro-classify as duplicates of QUM-510.
* **Mitigation pre-fix:** As QUM-510's "Mitigation" section already
  notes — managers should `mcp__sprawl__status({})` or
  `mcp__sprawl__peek` after every long background task to defensively
  poll for any queue items their parent runtime may have eaten.
  This is brittle and doesn't help if the manager itself has
  cycled out.

## Reflections (per researcher protocol)

* **Surprising:** The QUM-462 fix (which I read carefully before
  forming a hypothesis) addresses the *intentional* idle-Interrupt
  case, not the *accidental* "turn ended a microsecond ago" case.
  The guard moved one floor up but didn't make the decision
  atomic. This is a subtle leak that I think went unnoticed
  because QUM-462's specific repro (idle weave, no inflight turn at
  all) doesn't exercise the boundary-crossing case at all.
* **Open questions I'd chase if I had more time:**
  1. Whether `backend/claude/adapter.go` (didn't read) does anything
     interesting when `Interrupt` arrives during a background bash —
     specifically whether Claude acks the interrupt or silently
     discards it. This affects whether `EventInterrupted` even fires
     for the long turn (orthogonal to the race I described, but a
     useful confirmation channel).
  2. Whether a test like
     `TestUnifiedRuntime_InterruptDelivery_DoesNotArmPendingInterrupt_OnTurnEndBoundary`
     exists — I didn't see one. Adding it (per the repro sketch above)
     would make the regression deterministic.
  3. Whether `OnQueueItemDelivered` should fire on `EventInterrupted`
     at all — under Option A's lens it's correct that it does, but if
     we ship Option B (or a hybrid) we may want to reclassify
     "interrupt-aborted before any assistant message" as
     "not-delivered" so the pending file stays put for retry.
  4. Cross-reference QUM-488 (recently shipped) — the prompt mentions
     it added a task-queue bridge that may be related. I confirmed
     `feedTasks` only handles `state.Task` (delegated work), not
     queue messages, so QUM-488 does not directly cause the race.
     But the concept of "every iteration, re-feed from disk" from
     QUM-488 is exactly the defense-in-depth pattern I propose for
     pending entries — there's symmetry to exploit if Option A's
     author wants to.
* **What I would investigate next:** activity-ndjson grepping in the
  forensics corpus to retroactively count how many "stuck manager"
  incidents fit the QUM-510 fingerprint (`pending/<id>.json` →
  `delivered/<id>.json` move with no matching `[inbox]` line in
  the manager's turn prompts).
