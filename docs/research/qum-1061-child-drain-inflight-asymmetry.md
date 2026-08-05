# QUM-1061 — Why only the weave drain filters `InFlightSystemEntryIDs`

**Investigator:** ghost (researcher) · **Date:** 2026-08-04 · **Base:** `main` @ `7639e0f`
**Status:** answered. Reproduction lives in `internal/supervisor/qum1061_child_drain_duplicate_write_test.go`.
**Blocks:** QUM-1062 (the `FilterInFlight` policy row).

---

## Verdict

> **YES — reachable. Confidence: high.**
>
> An async-class (`priority: next`) child entry **is** written to stdin more than
> once when a re-drain lands in the write→ack window. It is not a narrow race:
> on the ordinary ordering it is **deterministic**, and while the ack never
> arrives it grows **linearly and without bound** — exactly one extra injection
> per turn boundary, forever.

The filter is **not** weave-only by design and the child was **not** fixed by a
different mechanism. The child's only protection is the QUM-821 ack-on-write for
the `now`/interrupt tier; the `next`/async tier is unprotected.

### Measured

From `TestQUM1061_ChildDrain_*` (real `unifiedHandle`, real `UnifiedRuntime`, real
`sweepCoordinator`, fake backend session):

| observation | value |
|---|---|
| injections after the first drain | 1 |
| entry still in `pending/` (no `isReplay` echo) | yes |
| injections after **one** turn boundary | **2** |
| injections after **10** turn boundaries | **11** (1.0 per boundary, exactly linear) |
| interrupt-class (`now`) entry, same scenario | 1 (ack-on-write protects it) |
| **weave** path, same scenario + an extra explicit re-drain | **1** (the filter works) |

## Why it is deterministic, not a race

`runtime.routeFrame` calls `cfg.PostTurnSweep` on its `EndOfTurn` leg
(`internal/runtime/unified.go:536`), **synchronously on the backend reader
goroutine**, and `sweepCoordinator.wake` is bound to
`unifiedHandle.WakeForDelivery` (`runtime_launcher.go:182`, `coord.Bind`). So:

```
… turn N running …
  Real.SendMessage → Enqueue → WakeForDelivery → drainPendingToStdin
                                                   → write #1 (priority next, entry stays in pending/)
  turn N terminal `result` frame
    → routeFrame EndOfTurn → PostTurnSweep → ListPending non-empty → wake
                                                   → write #2  ← DUPLICATE
  … CLI dequeues, emits isReplay for one of the two uuids → OnDelivered → MarkDelivered
```

The re-drain runs **before any later frame can be read**, and the CLI cannot emit
the `isReplay` echo for a `next`-priority write until it dequeues the message
*after* the current turn ends. So the echo is *structurally* unable to beat the
sweep. No interleaving, no load, no timing luck required.

The consequence is that **every** async inbox notification delivered to a *busy*
child is injected at least twice. That is the common case, not the edge case:
`Real.SendMessage` / `Real.ReportStatus` poke unconditionally, and a busy
recipient is precisely when a notification arrives mid-turn.

### Two distinct harms, different shapes

1. **Bounded double-injection (common).** The ack eventually arrives, so growth
   stops at 2 — but the recipient sees the same notification body twice in
   consecutive turns. `boundSystemFrame`'s dedup is **per-frame** and cannot
   collapse it (the two copies are in different frames). Second `MarkDelivered`
   for the same id fails and is swallowed, so no state corruption — just
   duplicated content and one wasted turn's context.
2. **Unbounded storm (the QUM-1028 strand shape).** If the echo never arrives —
   CLI refuses/drops the frame, session restarts mid-flight, `--replay-user-messages`
   off, the QUM-1000 refused-command class — the entry never leaves `pending/`
   and **every subsequent turn boundary re-injects it**. Measured at exactly
   1 write per boundary with no decay. Weave is immune to this by the filter;
   children are not. This is the same class QUM-821 measured at **~30 writes/s**
   against real claude 2.1.173 for the `now` tier, whose fix
   (`ConfirmDeliveredWithoutReplay`) was applied only to the interrupt branch.

The weave comment's phrase "the unbounded stdin write storm **measured on the
child path**" is therefore accurate and still describes live behaviour — the
measurement was taken on the child path and the child path was never fixed.

## Blast-radius statement for the follow-up

* **Per turn boundary:** exactly 1 duplicate injection of every un-acked async
  entry (linear in `boundaries × un-acked entries`, verified to 10 boundaries).
* **Per second:** the recipient's turn-boundary rate. Bounded above by the
  QUM-821 figure of ~30 writes/s for an agent taking rapid autonomous turns;
  ~0.1–1/s for a normally-working agent.
* **Per notification, common case:** ×2 content duplication for any
  notification arriving mid-turn.
* Each injection is a full frame; `boundSystemFrame` caps a single frame but
  imposes no cross-frame budget, so token cost scales with the storm.

## Does the QUM-1028 `stateConsumed` hazard apply to the obvious fix?

**Yes, and it transfers wholesale.** Porting weave's filter to the child path
(mutation **M1** — verified to kill both duplicate assertions, so it *works*)
also imports weave's wedge: `InFlightSystemEntryIDs` treats `stateConsumed` as
in-flight, and an entry that reached `stateConsumed` *without* `OnDelivered`
(QUM-1000 `settleNeverAcked`, and any future synthetic settle) is then suppressed
for the life of the process. Today that is weave-only exposure; the filter would
extend it to every child.

Mitigating facts, in the fix author's favour:

* `settleNeverAcked` is `kindUser`-only and inbox drains are `kindSystem`, so
  the *current* sweep cannot manufacture the wedge for a drained entry.
* `outstanding` is in-memory, so a restart clears the marker and the entry is
  still in `pending/` (never `MarkDelivered`) — "suppressed for this session",
  not "permanently undeliverable".
* Children have **no post-start re-drain hook**, but they *do* have an equivalent
  escape hatch — checked, not assumed. `SetPostStartHook` has exactly one non-test
  caller (`weave_handle.go:207`); `unifiedHandle` never registers one. What covers
  children instead is `Real.RecoverAgents`' explicit `rt.WakeForDelivery()`
  (`real.go:1347`, QUM-605) plus `unifiedHandle.Wake` → `drainPendingToStdin` on
  any `Real.Wake`. Both run against a **fresh runtime with an empty `outstanding`
  map**, so a suppressed entry is re-emitted exactly as it is for weave.
  Conclusion: the wedge would be a *delay* for children too, not permanent loss —
  but via a different, less-obvious mechanism, which is worth a comment at the fix
  site.

Recommended framing for QUM-1062's policy table: the row is not
`FilterInFlight: weave=true, child=false → unify to true`. It is *"suppress
re-injection of an entry whose write is outstanding"*, and the two candidate
mechanisms differ in their failure direction:

| mechanism | duplicate storm | strand risk |
|---|---|---|
| `InFlightSystemEntryIDs` filter (weave's) | fixed | imports the QUM-1028 wedge |
| ack-on-write (`ConfirmDeliveredWithoutReplay`, the `now` tier's) | fixed | marks delivered before the CLI consumed it — loses the message on a crash in the window |
| a `statePending`-only filter | **not** fixed (QUM-925 found the consumed-but-not-yet-`MarkDelivered` hole) | none |

The third is the trap; the first two are the real choice, and it is a choice
between duplication-free-but-suppressible and duplication-free-but-weaker-durability.

## AC 4 — the `lost_status_lines` WARN asymmetry

**Confirmed, and it is worse than the issue states. Route to QUM-1034.**

Both drains call the destructive `inboxprompt.DrainStatusChangeLines`
(`runtime_launcher.go:562`, `weave_handle.go:301`). On write failure:

* **weave** logs `WARN … lost_status_lines / lost_status_bodies` — the bodies are
  recoverable from the log.
* **child** does `_, _ = h.rt.WriteSystemMessage(...)`. The error is **not even
  inspected**. No WARN, no bodies, no counter. The lines are gone from the
  maildir with no record that they ever existed.

Demonstrated by `TestQUM1061_ChildDrain_DiscardsWriteErrorAndLosesStatusLines`:
envelope present → forced write failure → `WakeForDelivery` returns `nil` →
0 stdin writes → envelope **gone** → a retry drain has nothing to send. A
positive arm (working write) proves the drain does deliver status lines, so the
zero is attributable to the failure.

## Two further asymmetries found (not in the ACs)

Both are child-vs-weave gaps at the same two functions, so they belong in
QUM-1062's policy table rather than as separate one-offs.

1. **The child write is UNBOUNDED.** Weave wraps its write in
   `context.WithTimeout(…, weaveDrainWriteTimeout /* 5s */)` with a comment
   explaining that an unbounded write is *fleet-fatal*: `Real.SendMessage` /
   `Real.ReportStatus` call `WakeForDelivery` **synchronously on the MCP handler
   goroutine**. The child drain uses bare `context.Background()`
   (`runtime_launcher.go:583, 622`). `Real.SendMessage:1952` and
   `Real.ReportStatus:2242` poke a **child** runtime on that same goroutine — so a
   child whose stdin pipe is full (64 KB, unread) wedges the *sender's* MCP call
   indefinitely. Weave's rationale applies verbatim to the child; the bound does not.
2. **The child drain is UNSERIALISED.** Weave holds `drainMu` for the whole
   drain. The child has no equivalent, so two concurrent pokes (two producers
   reporting at once — correlated, per the weave comment: "a fleet finishing a
   phase") can each `ListPending` and each write the same entry, independently of
   the sweep path proven above. Structural observation only — not measured here,
   because a deterministic harness for it needs a write-side barrier.

## Files read

* `internal/supervisor/runtime_launcher.go` — `unifiedHandle.drainPendingToStdin` (549–624), `coord.Bind` (182)
* `internal/supervisor/weave_handle.go` — `WeaveRuntimeHandle.drainPendingToStdin` (243–350), the filter (282)
* `internal/supervisor/sweep_coordinator.go` — `PostTurnSweep`, `OnDelivered`
* `internal/runtime/unified.go` — `routeFrame` (358–540), `InFlightSystemEntryIDs` (1036–1091), `markConsumed`, `settleNeverAcked`, `ConfirmDeliveredWithoutReplay`, `boundSystemFrame`
* `internal/supervisor/real.go` — `SendMessage` (1952), `ReportStatus` (2242), `RecoverAgents` (1347)
* `internal/inboxprompt/inboxprompt.go` — `DrainStatusChangeLines`

## Open questions / what I would do next

1. ~~Do children have a post-start redrain at all?~~ **Answered during the
   investigation** — no `SetPostStartHook`, but `RecoverAgents`/`Wake` cover it
   against a fresh `outstanding` map. See the QUM-1028 section.
2. **Is the ×2 duplication observable in a live wire log?** A `notif-stacked-restart`
   / `drain-row-inject` run with a busy child should show the same
   `<system-notification>` body in two consecutive frames. Everything above is
   unit-level; a wire-log confirmation would close the last gap between "the
   mechanism reproduces" and "users see it".
3. **Is the unserialised-drain duplicate (asymmetry 1 above) reachable in
   practice?** Needs a write-side barrier in the fake session to make
   deterministic.
