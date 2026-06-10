> Terminology note (2026-06): pre-rename "sub-agent" = current "sidechain".

# QUM-618 — SubscriberWedged / SubscriberWedge root cause (live repro, 2026-05-26)

**Author:** ghost (researcher, parent: weave)
**Status:** Research-only. Fix is a separate effort.
**Evidence snapshot:** `.sprawl/incidents/qum-618-2026-05-26/` (read-only; the live `finn` agent has since been recovered)
**Verdict:** Turn-boundary hypothesis **CONFIRMED and refined**. The wedge is on *our* side of the boundary, but the trigger is upstream of the turn boundary: the **30-minute per-turn `TurnTimeout` (QUM-581) fired on a legitimately long, healthy turn**, and its teardown is incomplete.

---

## 1. TL;DR — the confirmed failure mechanism

`finn` was in a single long turn (turn 5) from **21:28:25** running M4 implementation work via sub-agents. The runtime's per-turn deadline `TurnTimeout = 30 * time.Minute` (`internal/supervisor/runtime_launcher.go:128`) expired at **21:58:25** — last recorded activity is **21:58:22**, three seconds before the deadline.

When the per-turn `turnCtx` deadline fires, `TurnLoop.executeTurn` simply **returns** (`internal/runtime/turnloop.go:211-222`). It does **not**:

1. call `Session.Interrupt` to wind down the backend's in-flight turn, nor
2. reset the backend session's `currentTurn` frame.

The backend session's reader runs on a **detached `readerCtx`** (`internal/backend/session.go:320`), independent of `turnCtx`. So after the per-turn deadline, the backend's `s.currentTurn` (a **non-autonomous** turn-5 frame) **stays pinned forever**. From that point:

- The turn loop iterates, `DrainAll()`s the queued messages (seq 3/4/5), and calls `executeTurn` → `Session.StartTurn` for the follow-up turn. But `session.StartTurn` sees `s.currentTurn != nil && !autonomous` and **immediately returns `ErrTurnInProgress`** (`internal/backend/session.go:408-412`). The follow-up turn never starts.
- Because no protocol frame is ever produced for that follow-up turn, `OnQueueItemDelivered` never fires → `MarkDelivered` never runs → seq 3/4/5 stay in `pending/` on disk. But they have already been **consumed from the in-memory queue** by `DrainAll`, so nothing re-enqueues them. The loop then parks on `Queue.Signal()`.
- The abandoned subscriber channel (buffered 100) is no longer drained by the runtime forwarder. If the subprocess keeps emitting frames, the reader's bounded send blocks for `subscriberSendDeadline = 5s` and faults **`ErrSubscriberWedged`** (`internal/backend/session.go:590-601`). If the subprocess goes quiet, the D1 hang watchdog faults `ErrHangTimeout` ~10 min later (`defaultHangTimeout = 10 * time.Minute`).

So **`SubscriberWedged` is the surface symptom; the 30-minute turn-timeout abandonment is the root cause.** The two reported faces of this bug (a) "`SubscriberWedged` banner" and (b) "agent alive but silently parked, queue not draining, ignores async pokes" are the **same** root cause, differing only by whether the subprocess kept emitting frames after the deadline.

---

## 2. Evidence (from the frozen snapshot)

### 2.1 Turn boundaries (`finn-activity.ndjson`)

`system`/`init` frames mark each `StartTurn`; `result` frames mark clean completion.

| Turn | StartTurn (`init`) | `result` (clean end) |
|---|---|---|
| 1 | 20:23:55 | 20:36:39 `stop=end_turn` |
| 2 | 20:39:47 | 20:57:19 `stop=end_turn` |
| 3 | 20:59:35 | 21:25:59 `stop=end_turn turns=48` |
| 4 | 21:25:59 | 21:26:04 `stop=end_turn turns=1` |
| **5** | **21:28:25** | **— none —** (cut off; last activity 21:58:22) |

Turns 1–4 all end with a `result` frame. **Turn 5 has no `result`** — it was guillotined, not completed.

### 2.2 The 30-minute coincidence is not a coincidence

`21:28:25` (turn-5 `StartTurn`) `+ 30:00` = `21:58:25`. Last activity frame is `21:58:22` (an `Edit` inside a running sub-agent task). `TurnTimeout` is hard-coded to `30 * time.Minute`. The deadline fired mid-work.

### 2.3 The queue is stuck exactly as the mechanism predicts (`finn-queue/`)

- `delivered/` stops at **seq 2** (last delivery before turn 5).
- `pending/` holds **seq 3,4,5,6,7**, enqueued at:
  - seq 3 — 21:33:30 (mid-turn-5)
  - seq 4 — 21:38:36 (mid-turn-5)
  - seq 5 — 21:42:18 (mid-turn-5)
  - seq 6 — 22:44:12 (from tower, long after the wedge)
  - seq 7 — 22:51:40 (from weave: "finn — are you live?")

seq 3/4/5 were correctly held for the turn boundary. The boundary fired as a **timeout**, so they were drained from the in-memory queue into a `StartTurn` that returned `ErrTurnInProgress`, and were never marked delivered. seq 6/7 piled on later (those sends called `WakeForDelivery`, but by then the runtime had faulted/stopped so `rt.WakeForDelivery` is a no-op — `internal/runtime/unified.go:442-451`).

### 2.4 Subprocess stayed alive (`finn-proc.txt`)

`pid 252327`, `/tmp/.../claude -p ... --session-id b2d639f6-...`, healthy (not defunct). The backend subprocess is never killed by the turn-timeout path — only the *sprawl-side* `turnCtx` expired.

### 2.5 State (`finn-state.json`)

`status: "done"`, `last_report_state: "working"`, `last_report_at: 2026-05-26T21:44:36Z`. finn's last own MCP call was `report_status` at 21:44:36; everything after that until 21:58:22 was local sub-agent tool use. The `working`→`done` skew is consistent with the runtime going idle/faulted while finn's last *self-reported* state was still `working`.

---

## 3. Code-path trace (file:line)

1. **Turn started, deadline armed.** `internal/runtime/turnloop.go:194-199` wraps the outer ctx with `context.WithTimeout(ctx, l.cfg.TurnTimeout)`. `TurnTimeout` is set to `30 * time.Minute` at `internal/supervisor/runtime_launcher.go:126-128`.

2. **Deadline fires — incomplete teardown.** `internal/runtime/turnloop.go:211-222`:
   ```go
   case <-turnCtx.Done():
       if ctx.Err() == nil && turnCtx.Err() == context.DeadlineExceeded {
           l.cfg.EventBus.Publish(RuntimeEvent{Type: EventTurnFailed, Error: ...})
       }
       return   // <-- no Session.Interrupt, no backend turn reset
   ```
   The comment claims "the backend's `readTurn` is also wired to ctx and will close `events`" — but the backend reader is **not** wired to `turnCtx`; see step 4.

3. **Runtime forwarder leaks `turnRunning=true`.** `internal/runtime/unified.go:224-243`: the `stateTrackingSession` forwarder only resets `turnRunning=false` / `state=Idle` when the inner channel `ch` closes (the `for range` completes). On `turnCtx.Done()` it takes the early `return` at line 229-231, **skipping** the reset. So `rt.state` is stuck at `StateTurnActive` and `turnRunning` stays `true`. (Secondary bug — see §5.)

4. **Backend `currentTurn` stays pinned.** The reader loop runs on the session-scoped `readerCtx` created from `context.Background()` at `internal/backend/session.go:320`, **not** the per-turn ctx. So when `turnCtx` expires, `runReader` keeps running and `s.currentTurn` (turn 5's non-autonomous frame) is never cleared. `currentTurn` is only reset on a `result` frame (`session.go:605-616`) or `readerCancel`/Close (`session.go:486-501`, `590-601`).

5. **Every follow-up `StartTurn` is rejected.** `internal/backend/session.go:408-412`:
   ```go
   for s.currentTurn != nil {
       if !s.currentTurn.autonomous {
           s.mu.Unlock()
           return nil, ErrTurnInProgress   // <-- turn 5 frame is non-autonomous
       }
       ...
   }
   ```
   The turn loop's drain-and-reprompt (`turnloop.go:115-127`) consumes seq 3/4/5 from the in-memory queue, hands them to `executeTurn`, `StartTurn` returns `ErrTurnInProgress` (`turnloop.go:201-205` → `EventTurnFailed`), and the items are gone from the queue but still `pending/` on disk.

6. **Eventual terminal fault.** With nobody draining `tf.subscriber`, the reader's bounded send hits `subscriberSendDeadline = 5s` (`session.go:27`, `590-601`) → `setTerminalErr(ErrSubscriberWedged)` → QUM-602 handler (`unified.go:129-150`) cancels the loop ctx, the runtime stops, the fault banner fires, and `WakeForDelivery` becomes a no-op for all subsequent sends.

---

## 4. Ranked root-cause hypotheses (by confidence)

### H1 — **30-min `TurnTimeout` fired on a healthy long turn; its teardown is incomplete** — CONFIRMED (very high)
The 30-minute deadline matches the wedge time to within 3 seconds; turn 5 alone among 5 turns has no `result`; the queue is stuck exactly as the `ErrTurnInProgress` mechanism predicts. The teardown at `turnloop.go:211-222` neither interrupts the backend turn nor resets `currentTurn`, and the detached `readerCtx` keeps `currentTurn` pinned, poisoning every future `StartTurn`. **This is the live root cause.**

### H2 — Forwarder `turnRunning` leak on `turnCtx.Done` — CONFIRMED contributing (high)
`unified.go:224-243` leaves `turnRunning=true`/`state=StateTurnActive` after a per-turn timeout. This corrupts liveness/`peek` reporting and would mis-route a subsequent `send_message(interrupt=true)` (it would think a turn is in flight). Secondary to H1 but real and on the same path.

### H3 — Original issue candidates (single huge frame / frame-burst / tight 5s deadline) — REFUTED as the *trigger* (low)
The issue's authored hypotheses (#1 huge frame, #2 burst storm, #4 deadline too tight) describe how `ErrSubscriberWedged` fires *once the consumer stops draining*. In this incident the consumer stopped draining **because of H1**, not because a single frame or burst legitimately overran a healthy consumer. The 5s `subscriberSendDeadline` is the *messenger*, not the cause. (Tightening or loosening it would not prevent this wedge — it would only change which sentinel fires.)

### H4 — D1 hang watchdog (`ErrHangTimeout`) was the trigger — REFUTED (low)
The hang watchdog faults only after `defaultHangTimeout = 10 min` of *no frames*. finn was emitting frames continuously until 21:58:22, so the watchdog baseline was fresh. It may have fired *after* the wedge (if the subprocess went quiet post-deadline), but it did not trigger it.

---

## 5. Recommended fix shape (do NOT implement — separate effort)

The fix must make the per-turn timeout teardown **complete and recoverable**, so a long-but-healthy turn does not poison the session. Ranked options:

1. **Primary — make `TurnTimeout` teardown reset the backend turn (and reconsider the 30-min cap).**
   On `turnCtx.Done()` with `DeadlineExceeded`, the loop should drive a real backend wind-down before returning: call `Session.Interrupt` (bounded, QUM-600 already bounds it) and/or have the backend treat a per-turn deadline as a `currentTurn` reset so the next `StartTurn` is accepted instead of returning `ErrTurnInProgress`. The backend reader should observe the per-turn cancellation (e.g. thread a per-turn cancel into the turn frame) so `currentTurn` is cleared deterministically. Without this, *any* turn legitimately exceeding 30 min wedges the agent.
   - Also revisit whether **30 min is the right cap**. Long autonomous M4-style turns with sub-agents can plausibly exceed it; the cap exists to catch *wedged-open SDK streams* (QUM-578/581), not healthy work. Consider distinguishing "no frames for N minutes" (already covered by the D1 hang watchdog) from "wall-clock turn length," and possibly removing/raising the wall-clock cap now that the hang watchdog exists.

2. **Secondary — fix the `turnRunning`/state leak in the forwarder.**
   `stateTrackingSession.StartTurn`'s forwarder (`unified.go:224-243`) must reset `turnRunning=false` / `state=Idle` on the `ctx.Done()` early-return path too (e.g. via a `defer`), not only on clean channel close. Otherwise liveness reporting and interrupt routing stay wrong after any per-turn timeout.

3. **Defense-in-depth — re-enqueue drained items when the follow-up turn fails to start.**
   When `executeTurn` consumes queue items but `StartTurn` errors before any frame (no forward progress), the items are silently dropped from the in-memory queue while remaining `pending/` on disk with nothing to retry them. Consider re-enqueuing on a failed-to-start turn, or having the post-turn sweep re-`drainPendingToQueue` so a transient `StartTurn` failure self-heals at the next wake instead of requiring a `recover`.

4. **Observability — surface per-turn-deadline faults distinctly.**
   `EventTurnFailed{DeadlineExceeded}` currently does not raise the same operator-visible banner as `EventBackendFaulted`. A turn hitting the 30-min cap should be loudly visible (it is a real "your work was guillotined" event), not silently followed by a `SubscriberWedged` banner that misattributes the cause.

### Regression test shape
A test that: starts a turn, advances past `TurnTimeout` (inject a small timeout via config seam), keeps the fake backend's `currentTurn` "busy," enqueues a message, and asserts the follow-up turn is **accepted** (not `ErrTurnInProgress`) and the pending message is delivered + marked delivered. This reproduces the wedge and pins the fix.

---

## 6. Reflections

**Surprising:** The issue is filed as "`SubscriberWedged` under e2e load," steering toward frame-size/deadline tuning. The actual trigger is a **wall-clock turn-length cap** (QUM-581) firing on healthy work — `SubscriberWedged` is just the downstream sentinel. The four authored hypotheses in the issue all describe the messenger, not the cause. The 3-second gap between last activity (21:58:22) and the computed deadline (21:58:25) is the cleanest "aha" in the evidence.

**Open questions:**
- Did the terminal fault here fire as `ErrSubscriberWedged` (subprocess kept emitting) or `ErrHangTimeout` (subprocess went quiet)? The snapshot's activity ring was cut at 21:58:22, so the post-deadline frames (if any) and the exact sentinel aren't in the preserved data. Confirming would need the supervisor/TUI fault log for finn around 21:58–22:08, which isn't in the snapshot.
- Why was turn 5 so long (30+ min)? Was finn's M4 sub-agent work genuinely that long, or was there an earlier SDK stall that the wall-clock cap (not the hang watchdog) happened to catch? The hang watchdog (10 min, frame-based) did *not* fire first, which argues the subprocess was producing frames throughout — i.e. genuinely-long healthy work, which is the worst case for a wall-clock cap.

**What I'd investigate next with more time:**
- Grep the supervisor logs / TUI fault emitter records for finn's exact terminal sentinel and timestamp to nail down scenario A vs B.
- Audit every `executeTurn` exit path (`StartTurn` error, interrupt, deadline, clean close) against the backend `currentTurn` lifecycle to enumerate *all* states that can leave `currentTurn` pinned (the deadline path is the one that bit us; are there others, e.g. outer-ctx cancel mid-turn during a non-Stop scenario?).
- Quantify how often real turns approach 30 min in production to size the cap correctly.
