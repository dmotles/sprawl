# QUM-549: Why `send_interrupt` didn't preempt tower's wedged `retire(ratz)` call

**Status**: investigation complete (code-read; no empirical repro yet)
**Author**: ghost (researcher) — 2026-05-12
**Related**: QUM-542 (the wedge), QUM-473 (`InterruptDelivery` overload),
QUM-548 (MCP tool doc fixes), QUM-550 (`send_message` design — gated on this)
**Cross-refs**: `docs/research/messaging-delivery-architecture-2026-05-12.md`

## TL;DR

`Session.Interrupt` (the `control_request{"subtype":"interrupt"}` JSON frame
written to claude's stdin at `internal/backend/session.go:319-327`) was almost
certainly called and the JSON almost certainly reached claude's stdin during
the tower wedge. But the wedge was invisible to sprawl regardless, because
**`session.readTurn` is a single-threaded loop that dispatches incoming
`control_request{mcp_message}` calls synchronously through
`ToolBridge.HandleIncoming`**. While the retire handler was blocked waiting
for ratz to exit, readTurn was parked inside `HandleIncoming` and could not
service ANY further stdout from claude — including a potential interrupted
`result` message. So even if claude's CLI honors `interrupt` mid-MCP-tool-wait
(which is itself unverified), sprawl would not observe the preemption.

This makes the visible symptom — "send_interrupt did not preempt tower" — a
**sprawl-side observability bug**, not a Claude-side honor-the-interrupt bug.
It also means the preempt-via-`Session.Interrupt` story we told ourselves in
`docs/research/messaging-delivery-architecture-2026-05-12.md` has a giant
asterisk: it only works while readTurn is *not* parked inside
`HandleIncoming`. Concretely: it works for streaming/thinking states, but
**not for any state where claude is blocked awaiting a host-side MCP
response**.

## Hypothesis verdicts

| # | Hypothesis | Verdict | Evidence |
|---|---|---|---|
| 1 | Claude CLI can't honor `interrupt` while waiting on `tool_result` | **Unknown** — and moot until #4 is fixed | Anthropic docs imply interrupt produces `error_during_execution` results but don't specify mid-MCP-wait behavior. Untestable from sprawl's side until readTurn is unblocked. |
| 2 | `turnRunning` stale (false) during MCP tool wait → `Session.Interrupt` never called | **Ruled out** | `turnRunning` is set true in `stateTrackingSession.StartTurn` *before* delegating, and only flipped false when the inner events channel closes. The channel only closes when `readTurn` returns. `readTurn` does not return during an MCP-tool-wait. So `turnRunning` stays true throughout. `interruptForDelivery` correctly calls `Session.Interrupt`. |
| 3 | Write to claude stdin was blocked | **Ruled out for the write itself; the *read* side is the real block** | `protocol.Writer.mu` is only held during the actual syscall, not during MCP wait. The interrupt JSON is small (≈80 bytes), pipe buffer trivially fits it. The interrupt JSON reaches claude's stdin. But sprawl can never observe claude's response because readTurn is blocked (see #4). |
| 4 | Other state-machine race | **The actual cause: synchronous in-loop MCP dispatch in `readTurn`** | See below. |

## The real cause: synchronous MCP dispatch inside `readTurn`

`internal/backend/session.go:212-258` `readTurn`:

```go
for {
    msg, err := s.transport.Recv(ctx)
    ...
    if msg.Type == "control_request" {
        if err := s.handleInlineControlRequest(ctx, msg, initSpec); err != nil {
            s.setTurnError(err)
            return
        }
        continue
    }
    ...
}
```

`handleInlineControlRequest` (session.go:260-317) calls
`initSpec.ToolBridge.HandleIncoming` **synchronously** (line 302). For an
MCP tool call like `retire`, this routes into
`internal/sprawlmcp/server.go:405 toolRetire → supervisor.Retire`, which —
during the QUM-542 incident — blocked for 34 minutes waiting for ratz to
exit.

While `HandleIncoming` is blocked:

- `readTurn` is parked one frame deeper, **not reading from claude's
  stdout**.
- The wrapper forwarder in `stateTrackingSession.StartTurn` (unified.go:169-188)
  is parked on `for msg := range ch` — the inner channel has no new messages
  to forward.
- `TurnLoop.executeTurn` (turnloop.go:184-213) is parked on its `select` —
  the events channel has no new messages.
- `turnRunning` remains true.
- The EventBus emits nothing further. The last activity-stream event is the
  `EventProtocolMessage` for the incoming `control_request{mcp_message}` at
  retire start. **Matches the observed symptom: "ZERO new events after `phase:
  start` of the retire call until SIGKILL on ratz".**

When weave's `send_interrupt(tower)` arrives:

1. `Server.toolSendInterrupt → supervisor.SendInterrupt →
   unifiedHandle.InterruptDelivery → UnifiedRuntime.interruptForDelivery`
   (unified.go:404).
2. Under `rt.mu.Lock`, observes `turnRunning == true` (correct), transitions
   `StateTurnActive → StateInterrupting`.
3. Calls `sess.Interrupt(ctx)` → `session.Interrupt` (session.go:319) →
   `transport.Send` → `protocol.Writer.WriteJSON` → ~80 bytes to claude's
   stdin pipe. The mutex is acquired only for the duration of the syscall;
   it is **not contended** with the in-flight MCP call (which is upstack,
   not holding the writer mutex).
4. Calls `loop.Interrupt(ctx)` which non-blockingly signals `thisTurn`
   (turnloop.go:122-135).
5. `executeTurn`'s select wakes on `<-thisTurn` and calls
   `Session.Interrupt` **again** (turnloop.go:194 — second control_request
   to claude's stdin), sets `interrupted=true`, then loops back to its
   `select` waiting on `events`/`ctx.Done`/`thisTurn`.

At this point, claude has received **two** `control_request{interrupt}`
frames on stdin. Whether or not it acts on them, sprawl's `readTurn` is
still synchronously parked inside the retire handler. No further claude
stdout is consumed; the wrapper forwarder remains idle; executeTurn's
events channel remains empty; the EventBus stays quiet.

When ratz is finally SIGKILLed and `supervisor.Retire` returns,
`handleInlineControlRequest` sends the (now-stale) `mcp_message`
control_response and returns; readTurn resumes `Recv`-ing claude's stdout.
At that point, whatever claude wrote in the intervening ~34 minutes
(possibly an interrupted `result`, possibly nothing, possibly something
else) starts flowing. The observed outcome here would distinguish
between hypothesis 1 (Claude CLI does not preempt MCP-tool-wait → just
continues with the stale tool_result) and hypothesis 1' (Claude CLI does
preempt → emits an interrupted `result` we'd see immediately after
HandleIncoming returns). Either way, by the time we'd see it, the
operational damage is already done.

## What about `turnRunning`?

Confirmed correct during MCP-tool-wait. Concrete path:

- `unified.go:128-135` (`stateTrackingSession.StartTurn`): `turnRunning =
  true` set before `inner.StartTurn`.
- `unified.go:182-187`: `turnRunning = false` only when the goroutine
  forwarding `inner → out` observes the channel close.
- The channel closes only in `session.readTurn`'s `defer close(events)`
  (session.go:213), which fires only when readTurn returns.
- readTurn returns on: ctx done, Recv error, or a `result` message
  received from claude (session.go:254-256).
- During MCP-tool-wait, none of these have occurred.

So `interruptForDelivery`'s guard `if rt.stopped || !rt.turnRunning {
return }` (unified.go:406) correctly **does not** short-circuit. The
interrupt path fires as designed.

## Empirical repro (proposed, not executed)

I did not run a repro in this investigation — the code-read is conclusive
enough on hypotheses 2 and 3, and hypothesis 1 (the residual unknown) is
masked by hypothesis 4 anyway. But here's the repro shape for follow-up:

1. **Slow MCP tool**: add a test-only MCP tool `sleep_forever` (or
   `sleep_n_seconds` with a long N) in `internal/sprawlmcp/server.go`,
   exposed only when an env var is set.
2. **Parent agent** invokes the slow tool on a child agent via delegate
   or by having the child call it directly.
3. **Mid-wait**, parent calls `mcp__sprawl__send_interrupt({to: child, ...})`.
4. **Observe**: child's EventBus stream (via `sprawl peek` or activity log)
   shows ZERO new events for the duration of the slow tool. After the slow
   tool returns, observe what claude emits — interrupted `result`? Normal
   continuation? Nothing at all?

Expected based on this analysis: zero events during the wait, regardless
of what claude does. The slow tool can be a simple `time.Sleep(60s)`.

A more diagnostic-instrumented repro could add log lines at:

- `internal/runtime/unified.go:417` (`interruptForDelivery` — "calling
  Session.Interrupt, turnRunning=%v")
- `internal/backend/session.go:326` (`Session.Interrupt` — "writing
  interrupt control_request, requestID=%s")
- `internal/backend/session.go:302` (around the synchronous
  `HandleIncoming` call — "MCP dispatch begin/end")

…to definitively confirm the `Session.Interrupt` write happens during the
MCP wait. I am 95% sure it does (the code path is straight-line under a
lock with no early returns), but logs would close the loop.

## Implications for QUM-550 (`send_message({interrupt: true})`)

The blast radius of "interrupt mid-MCP-tool-wait" is narrower than the
`messaging-delivery-architecture` doc claimed. Specifically:

- **Streaming / thinking**: `Session.Interrupt` preempts. Verified
  behavior (this is the Esc / Ctrl+B path observed in TUI).
- **Tool-wait for a host-side MCP call routed through `ToolBridge`**:
  `Session.Interrupt`'s write succeeds, but **sprawl cannot observe any
  preempt effect until the MCP call returns**, because `readTurn` is
  serialized through `HandleIncoming`. This applies to every sprawl MCP
  tool — `delegate`, `merge`, `retire`, `send_*`, `report_status`,
  `ask_user_question`, etc. — that is being awaited.
- **Idle** (between turns): `turnRunning == false`, so
  `interruptForDelivery` no-ops on the Session side; only the queue is
  woken. The next StartTurn pulls in the delivered items normally.
- **Non-MCP tool-wait** (e.g., a built-in Bash/Read/etc. tool): claude
  handles the tool internally without round-tripping through sprawl, so
  `readTurn` is *not* parked. Whether claude preempts the tool itself is
  the Hypothesis-1 question; I suspect yes for Bash/Read (they're claude
  CLI internal and interruptible), unknown for can-use-tool-permission
  callbacks.

For QUM-550's `send_message({interrupt: true})` semantics, this means:

1. **Documenting "interrupt is best-effort and gated on receiver state"
   is mandatory.** It cannot be sold as a guarantee.
2. **The receiver's wedge state matters**, not just turn-vs-idle. The
   3-state mental model (idle / streaming / tool-wait) needs a 4th
   distinction: tool-wait *through sprawl's ToolBridge* vs tool-wait via
   claude's built-in tools.
3. **The "interrupt-rescues-a-wedge" use case** that sent us down this
   rabbit hole (recovery from a stuck MCP call) **does not work today
   and cannot work without unblocking `readTurn`**. The right escalation
   from "wedged" is kill / SIGKILL, not interrupt. QUM-542's bounded-wait
   fix is exactly the right pattern.

## Recommended fixes (file as follow-ups)

These are NOT in scope for QUM-549 (research issue). Recommend filing:

### Fix A — Async MCP dispatch in `readTurn` (High value, medium risk)

Spawn `HandleIncoming` in a goroutine inside `handleInlineControlRequest`;
have the goroutine write the control_response when done. Continue
draining stdout in `readTurn` while MCP calls are in flight.

Implications to think through:

- **Response ordering**: claude's protocol expects a control_response
  with the same `request_id`. With concurrent dispatch, multiple
  responses may be in flight; `protocol.Writer.mu` already serializes
  the writes, so that's fine. But claude may have ordering expectations
  on its end — need to verify.
- **Interrupt observability**: with readTurn unblocked, a claude-emitted
  interrupted `result` would now be observed in roughly real-time, and
  the EventBus would publish `EventInterrupted` as designed.
- **Stale responses**: if claude interrupts a turn while an MCP call is
  in flight, the eventual MCP response may arrive after claude has moved
  on. Either claude drops it (no_match request_id) or it confuses
  state. Needs an explicit test.
- **Cancellation**: should the in-flight MCP call also be cancelled when
  we observe an `EventInterrupted`? The context plumbing exists; we'd
  need to cancel `bridgeCtx`. Doing so would unwedge tools that respect
  ctx (`retire` does via its inner waits; `delegate` mostly does too).

Risk: changes a serialization invariant the codebase has implicitly
relied on since the bridge was introduced. Needs careful concurrency
tests. Likely should be its own High-priority issue.

### Fix B — Document the actual interrupt-honor envelope (Low cost, do this regardless)

Update the MCP tool descriptions for `send_async` / `send_interrupt`
(QUM-548) to be honest about the failure mode: "if the recipient is
currently waiting on a sprawl-side MCP tool call (e.g. another agent's
`retire` / `merge` / `delegate` / `send_*`), the interrupt will not be
visible until that call returns. Use `kill` for hard recovery from a
wedged tool call."

Update `docs/research/messaging-delivery-architecture-2026-05-12.md`
to add the asterisk on `Session.Interrupt`'s observable preemption,
referencing this doc.

### Fix C — Diagnostic logging in the interrupt path (Tiny, optional)

Add a structured log line at `interruptForDelivery` and `Session.Interrupt`
so the next wedge is unambiguous in the logs. Today the only signal is
"interrupted: true" from the MCP tool call, which only confirms
enqueue, not the Session.Interrupt write.

## Reflection

**Surprising:** I went in expecting hypothesis 1 (Claude SDK doesn't honor
interrupt mid-tool-wait) to be the answer. The actual culprit is much
closer to home: sprawl's own readTurn architecture makes observability of
the interrupt impossible regardless of what claude does. The 2026-05-12
architecture doc oversold `Session.Interrupt`'s preemption guarantee
specifically because the doc focused on the streaming case and never
walked through the host-side MCP dispatch path.

**Open questions:**

1. **Does the Claude CLI actually honor `interrupt` mid-MCP-tool-wait?**
   Not answerable without an empirical test, and not answerable from
   sprawl until Fix A lands (because readTurn can't see the result).
   Could be tested with a minimal harness writing directly to a `claude`
   subprocess's stdin (no sprawl involvement) — feeding it a tool_use,
   parking on the tool_result wait, then sending `control_request{interrupt}`
   and observing the next stdout line.
2. **What does claude emit when readTurn finally resumes?** Did claude
   queue/ignore the interrupt during the 34-minute wedge? If we had
   activity logs from the post-SIGKILL recovery, we'd know — tower's
   first event after retire-returns would be either an interrupted
   `result` (claude honored), a normal continuation (claude ignored), or
   nothing (claude is dead). Worth grabbing those logs from the wedge
   incident if they exist.
3. **Are there OTHER `ToolBridge`-routed call sites that could similarly
   block readTurn?** Yes — every sprawl MCP tool. Long-running ones to
   audit: `delegate` (spawns child, may block on supervisor lock?),
   `merge` (runs validate command — `make validate` can take many
   seconds), `kill` (waits for SIGKILL'd process to exit), `ask_user_question`
   (blocks on user modal — UNBOUNDED). The ask_user_question case is
   particularly interesting: by design, it holds readTurn parked for
   however long the user takes to answer, making the asking agent
   un-interruptible during that window. Worth a follow-up audit.

**What I'd investigate next if I had more time:**

- Build the minimal `claude`-subprocess harness from question 1 to settle
  the Claude-CLI-honor-interrupt question definitively.
- Add the diagnostic logging from Fix C and re-run the QUM-542 repro
  with the new logs to close the loop empirically.
- Audit every `ToolBridge`-routed handler for "can this block readTurn
  longer than acceptable" — particularly `ask_user_question` which is
  designed to block on a human.
