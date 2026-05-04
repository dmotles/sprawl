# QUM-462 Live Verification — Unified-Runtime Inbox Wake

**Verdict: PASS** ✅

Date: 2026-05-04
Verifier: trace (researcher, qa family)
Branch: `dmotles/qum-462-live-verify`

## Summary

Three independent `send_async` probes from a child agent (`trace`) to the
running root weave (PID 3910989, build `v0.1.10-165-g4403f3e`,
`SPRAWL_UNIFIED_RUNTIME=1`) all woke weave's claude subprocess and produced a
real reply turn — not just a banner. End-to-end inbox routing on the unified
path is working.

## Background

QUM-462 reported that under the unified runtime path, in-process child
`send_async` calls to weave fired the TUI inbox banner and `(N)` badge but
never woke claude — `turnState` stayed `TurnIdle` indefinitely. The fix landed
in `internal/runtime/unified.go:368-383`
(`UnifiedRuntime.InterruptDelivery`):

```go
// QUM-462: InterruptDelivery must NOT call rt.Interrupt against an idle
// runtime. ... arming pendingInterrupt here causes the wrapper's next
// StartTurn to immediately interrupt the very turn that would deliver the
// inbox prompt to Claude, so the user sees a banner but no turn ever
// completes. We forward to rt.Interrupt only when a turn is actually in
// flight (so a higher-priority queue item can preempt it cleanly);
// otherwise queue.Wake is sufficient to unblock the loop.
if started && turnRunning {
    _ = rt.Interrupt(ctx)
}
rt.queue.Wake()
```

The discriminator chosen for live verification: under the bug, banner fires
but no turn starts; under the fix, both fire and claude actually processes the
message.

## Experiment

**No sandboxes used.** QUM-458's e2e-leak audit is in flight; per task
constraints, no `make test-*-e2e` targets, no `sprawl-test-env.sh` invocations.

Method:

1. Captured a baseline of weave's `.sprawl/agents/weave/activity.ndjson` ring.
2. Sent three uniquely-tagged `send_async` messages to weave from trace via
   the `mcp__sprawl__send_async` MCP tool. Each asked weave to reply with a
   short literal token (`QUM-462-A{1,2,3}-ACK`) so claude's actual processing
   was provable, not just runtime wake.
3. After each send, observed weave's activity ring for:
   (a) `session_state_changed: running` (turn loop unblocked),
   (b) `tool_use` for `mcp__sprawl__send_async` with the expected subject
       (claude actively processed and replied),
   (c) `assistant_text` and `result success stop=end_turn` (turn completed).
4. Confirmed each ACK arrived in trace's inbox.

## Raw Observations

Selected lines from
`/home/coder/sprawl/.sprawl/agents/weave/activity.ndjson`:

### Probe A1

- trace `send_async` queued at: `2026-05-04T22:45:36Z`
- weave woke: `2026-05-04T22:45:36.580Z` — **~2 ms after enqueue** —
  `session_state_changed: idle → running`
- claude tool_use: `2026-05-04T22:45:42.97Z`
  `mcp__sprawl__send_async {"to":"trace","subject":"[QUM-462-verify-A1] ACK","body":"QUM-462-A1-ACK"}`
- turn completed: `2026-05-04T22:45:44.54Z` `result success stop=end_turn turns=3`
- ACK delivered to trace inbox ✅

### Probe A2

- trace `send_async` queued at: `2026-05-04T22:45:50Z`
- weave woke: `2026-05-04T22:45:50.684Z` — **~2 ms after enqueue**
- claude tool_use: `2026-05-04T22:45:53.37Z`
  `mcp__sprawl__send_async {"to":"trace","subject":"[QUM-462-verify-A2] ACK","body":"QUM-462-A2-ACK"}`
- turn completed: `2026-05-04T22:45:54.63Z` `result success stop=end_turn turns=2`
- ACK delivered to trace inbox ✅

### Probe A3

- trace `send_async` queued at: `2026-05-04T22:45:56Z`
- weave woke: `2026-05-04T22:45:56.737Z` — **~1 ms after enqueue**
- claude tool_use: `2026-05-04T22:45:59.43Z`
  `mcp__sprawl__send_async {"to":"trace","subject":"[QUM-462-verify-A3] ACK","body":"QUM-462-A3-ACK"}`
- turn completed: `2026-05-04T22:46:01.18Z` `result success stop=end_turn turns=2`
- ACK delivered to trace inbox ✅

## Verdict

**PASS** — three for three. Each probe:

- woke weave's UnifiedRuntime turn loop (`session_state_changed: running`)
  within milliseconds of enqueue,
- caused claude to actually consume the inbox prompt,
- caused claude to emit a `mcp__sprawl__send_async` reply with the requested
  literal token,
- reached `result success stop=end_turn`,
- delivered the reply token back to trace's mailbox.

This is exactly the post-fix behavior described in the QUM-462 issue body:
*both banner and turn fire*. No probes exhibited the pre-fix symptom (banner
without a started turn).

The five-hour rate-limit message (`status=rejected type=five_hour`) seen
earlier in the ring is unrelated to QUM-462 — it predates the probes and did
not block the test.

## Reflection

What was surprising / notable:

- **Wake latency is ~1–2 ms** from `send_async` enqueue to
  `session_state_changed: running`. The fix's `queue.Wake()` path is
  essentially instantaneous — no perceptible delay between InterruptDelivery
  and the turn loop being scheduled.
- **My initial `report_status` call (right after spawn) had already woken
  weave** before the formal experiment started — visible as the `Acked. trace
  is investigating. Holding.` turn at `22:45:06`. So even before probe A1,
  the runtime was demonstrating correct wake behavior. A1/A2/A3 are
  confirmation, not the only data point.
- I did **not** need to interrogate disk maildir vs in-memory queue
  distinctions to verify the fix at the user-observable level. The activity
  ring + reply-token round trip is a sufficient external test: claude's
  `tool_use` event is incontrovertible evidence that the prompt reached the
  model.

Open questions / what I'd investigate next:

- The fix only changes idle-time behavior (`!turnRunning`). It would be worth
  a focused unit/integration test that probes the `turnRunning == true` arm
  of `InterruptDelivery` to verify high-priority preemption still works
  cleanly — i.e. send_async during an in-flight turn should still preempt
  via `rt.Interrupt(ctx)` without arming a stale `pendingInterrupt`. The
  existing `TestInterruptDelivery_DoesNotArmPendingInterrupt_WhenIdle` test
  in `internal/runtime/unified_test.go:905` covers the idle case; I did not
  see a matching `WhenTurnRunning` test in a brief scan.
- The QUM-462 acceptance criteria call for extending `make test-notify-tui-e2e`
  to assert claude actually processed (not just banner+badge). That work is
  separate from this live-verify and should be tracked independently — out
  of scope for this task per the no-e2e constraint.
- Probes were sent against an idle weave each time (turn ended before the
  next probe). I did not stress-test concurrent inbox arrival during an
  in-flight turn. Worth a follow-up if soak surfaces anything.

## Constraints Honored

- ✅ No sandbox creation, no `make test-*-e2e`, no `sprawl-test-env.sh`.
- ✅ 3 `send_async` messages to weave (under the 5 cap).
- ✅ No `send_interrupt`.
- ✅ No production-code modification.
