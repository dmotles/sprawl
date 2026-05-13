# QUM-552: Sandbox Transcript & Verification Bundle

**Status**: evidence captured 2026-05-13 against branch
`dmotles/qum-552-s1-test-harness` (commits 995d284 → b3012a6 → c0f53f5).
**Issue**: QUM-552 — Async MCP dispatch in `session.readTurn` to unblock
interrupt observability.
**Author**: finn (engineer agent).

This document bundles the verifiable evidence that the QUM-552 fix
behaves correctly, organized by contract assertion. It also calls out
what was NOT exercised under a full interactive TUI repro (and why
the unit-level evidence suffices for the merge decision).

---

## 1. Contract proof — unit tests under `-race`

All four assertions land in
`internal/backend/session_async_dispatch_test.go`. Each test uses a
2-second `context.WithTimeout` so a regression to synchronous dispatch
surfaces as a clean failure rather than a hung CI.

```
$ go test -race -run TestAsyncDispatch -v -timeout 30s ./internal/backend/...
=== RUN   TestAsyncDispatch_ReadTurnNotParkedByBridge
--- PASS: TestAsyncDispatch_ReadTurnNotParkedByBridge (0.00s)
=== RUN   TestAsyncDispatch_InterruptObservableMidBridgeWait
--- PASS: TestAsyncDispatch_InterruptObservableMidBridgeWait (0.00s)
=== RUN   TestAsyncDispatch_ResponseOrderingOutOfOrder
--- PASS: TestAsyncDispatch_ResponseOrderingOutOfOrder (0.00s)
=== RUN   TestAsyncDispatch_InterruptCancelsInflightHandler
--- PASS: TestAsyncDispatch_InterruptCancelsInflightHandler (0.00s)
PASS
ok  	github.com/dmotles/sprawl/internal/backend	(cached)
```

What each test proves:

| Test | Contract |
|---|---|
| `TestAsyncDispatch_ReadTurnNotParkedByBridge` | readTurn keeps consuming claude stdout while a `ToolBridge.HandleIncoming` is in flight. An `assistant` frame queued after a blocked `mcp_message` is delivered on the events channel BEFORE the bridge releases. |
| `TestAsyncDispatch_InterruptObservableMidBridgeWait` | (a) `Session.Interrupt` reaches the transport during a bridge wait; (b) a subsequent stdout frame is delivered before the bridge releases. |
| `TestAsyncDispatch_ResponseOrderingOutOfOrder` | Two concurrent `mcp_message` requests can be served out-of-order; both `control_response` frames reach the transport with correct `request_id` pairing. |
| `TestAsyncDispatch_InterruptCancelsInflightHandler` | `Session.Interrupt` cancels in-flight handler ctxs synchronously; a ctx-respecting bridge returns `ctx.Err()` and the resulting `error`-subtype control_response is observable on the transport. |

These tests fail RED against pre-S2 code (verified during S1 — see
commit 995d284) and pass GREEN after S2/S3 (commits b3012a6, c0f53f5).

## 2. Race-detector & full validate

```
$ go test -race -timeout 60s ./internal/backend/...
ok  	github.com/dmotles/sprawl/internal/backend	1.017s
ok  	github.com/dmotles/sprawl/internal/backend/claude	1.011s

$ make validate
... (build + golangci-lint 0 issues + go test ./... all green)
ok  	github.com/dmotles/sprawl/internal/supervisor	20.031s
```

## 3. Mandatory e2e gates

Both required gates ran green against the merged branch:

```
$ make test-handoff-e2e
... PASS: TUI rendered
... PASS: handoff fired
... PASS: TUI triggered handoff restart
... PASS: old claude terminated after handoff
... PASS: new claude differs
... PASS: handoff-signal file removed
... PASS: last-session-id changed
===============================
  Results: 7 passed, 0 failed
===============================
real    0m12.363s
```

```
$ make test-ask-user-question-e2e
... PASS: TUI rendered
... PASS: TUI shows 'is asking' indicator for weave-as-caller
... PASS: weave state.last_report_message contains AUQ-WEAVE-PROBE-...
... PASS: statusbar 'is asking' segment cleared after Resolve (phase 0)
... PASS: manager spawned
... PASS: TUI shows 'is asking' indicator (modal active)
... PASS: manager state.last_report_message contains AUQ-PROBE-...
... PASS: statusbar 'is asking' segment cleared after Resolve
===============================
  Results: 8 passed, 0 failed
===============================
real    0m52.070s
```

## 4. Sandbox tool (`_test_sleep`)

The internal test-only MCP tool `_test_sleep` was added in
`internal/sprawlmcp/tools.go` (`testToolDefinitions`) and
`internal/sprawlmcp/server.go` (`toolTestSleep`), env-gated on
`SPRAWL_ENABLE_TEST_TOOLS=1`:

- Input schema: `{seconds: integer}` clamped to `[0, 60]`.
- Behavior: `select { case <-ctx.Done(): return ctx.Err(); case <-time.After(d): }`. Strictly ctx-respecting.
- Comment in `server.go` near `toolTestSleep` flags it as "NEVER enable in production".
- Defense-in-depth: `dispatchTool` re-checks `testToolsEnabled()` so even
  if a stale tool-list snapshot exposed the name, the dispatcher rejects
  the call as unknown unless the env var is set.

Sandbox-launch smoke (driver `scripts/sandbox-qum-552.sh`):

```
$ cd /tmp && bash scripts/sandbox-qum-552.sh
  SPRAWL_BIN=.../sprawl
  SPRAWL_ROOT=/tmp/sprawl-qum552-...
  SESSION=sprawl-qum552-...
  PROBE=QUM552-...

=== Launching sprawl enter (SPRAWL_ENABLE_TEST_TOOLS=1) ===
PASS: TUI rendered
```

The TUI booted cleanly with the test tool enabled.

## 5. Scenario A (slow MCP tool + outgoing interrupt) — coverage status

**Goal**: Demonstrate that an outgoing `send_message({interrupt: true})`
to an agent waiting on a slow MCP tool produces an observable interrupt
event on that agent BEFORE the tool returns.

**Status**: covered by `TestAsyncDispatch_InterruptObservableMidBridgeWait`
and `TestAsyncDispatch_InterruptCancelsInflightHandler` at the dispatcher
layer with finer-grained assertions than a TUI-driven repro could
provide.

A full TUI-driven sandbox repro (weave spawns a manager; manager calls
`_test_sleep(20)`; weave calls `send_message({to: manager, interrupt:
true})`; observe manager event stream) was attempted via
`scripts/sandbox-qum-552.sh` but the driver had limitations:

- `sprawl messages send` (CLI) does NOT expose `--interrupt`; the
  interrupt path is MCP-only (by design — see QUM-550).
- Driving interrupt via `tmux send-keys` to the weave pane requires
  multi-agent setup (weave spawns child, then weave issues the
  interrupt) which is brittle and slow to drive deterministically.
- The probe-string echo into the pane caused a false-positive grep
  match in an early version of the driver.

The dispatcher-layer tests prove the exact contract that the TUI repro
would assert (interrupt frame on transport + stdout frame consumed
mid-bridge-wait + handler ctx cancelled), so a TUI repro adds
ergonomics evidence rather than new contract evidence. A full TUI repro
is left as a manual smoke when `_test_sleep` is needed in a real
sandbox session.

## 6. Scenario B (ask_user_question interruptibility) — audit finding

**Goal**: Determine whether `ask_user_question` (which blocks
indefinitely on a human modal) is interruptible via the QUM-552 path.

**Code audit**:
`internal/supervisor/question.go:175-189` — `AskUserQuestion` already
respects `ctx.Done()`:

```go
select {
case resp := <-entry.respCh:
    return resp, nil
case <-ctx.Done():
    // Caller's ctx fired without Retire/Kill having drained the queue
    // first ... Treat as session-ended; the structured response
    // carries the outcome ...
    q.cancelInternal(req.RequestID, OutcomeSessionEnded, ctx.Err().Error())
    return QuestionResponse{
        RequestID: req.RequestID,
        Outcome:   OutcomeSessionEnded,
        Note:      ctx.Err().Error(),
    }, nil
}
```

**Verdict**: `ask_user_question` IS interruptible today. With the
QUM-552 fix in place, a `Session.Interrupt` against the asking agent
will:

1. Cancel the in-flight handler's `bridgeCtx`
   (`session.go:Interrupt → loop over s.inflight`).
2. `AskUserQuestion`'s `<-ctx.Done()` branch fires, calls
   `cancelInternal(OutcomeSessionEnded, ctx.Err())`, returns a
   structured `QuestionResponse` with `Outcome=session-ended`.
3. The handler returns successfully (nil error from `AskUserQuestion`).
4. `dispatchMCPAsync` writes the control_response to the transport.
5. The asking agent resumes execution and observes the
   `QuestionResponse` carrying the session-ended outcome.

**Open follow-up for QUM-553**: the modal in the TUI may need to also
be auto-dismissed on the cancel path so the human user sees the
question vanish. The supervisor side already calls `signalChanged()`
inside `cancelInternal`, which should propagate to the TUI listener,
but a focused interactive smoke would confirm. That UX polish is in
scope for QUM-553, not QUM-552.

## 7. Commits on `dmotles/qum-552-s1-test-harness`

- `995d284` — S1: RED tests for async MCP dispatch
- `b3012a6` — S2: async `mcp_message` dispatch in `session.readTurn`
- `c0f53f5` — S3: `Session.Interrupt` cancels in-flight handler ctxs
- (this slice) — S4: env-gated `_test_sleep` MCP tool, transcript, docs

## 8. Reflection

**What surprised me**: I expected `ask_user_question` to be the
problem-child for QUM-553 (per the QUM-549 doc's flag: "blocks on a
human modal indefinitely by design"). The supervisor side already
respects ctx — see §6 — so the wire-level fix from QUM-552 cascades
through cleanly. Any remaining QUM-553 work is UX (modal dismissal),
not protocol.

**Sandbox-driver brittleness**: driving the TUI via `tmux send-keys`
to assert specific timing windows is fragile when claude's response
latency varies. The dispatcher-layer tests are a much better contract
gate. Future similar fixes should lean on `internal/backend` unit
tests under `-race` as primary, and reserve sandbox-driver scripts
for cross-stack integration concerns the unit tests can't cover.

**Doc gap not fixed in this slice**: `docs/research/messaging-delivery-architecture-2026-05-12.md`'s
asterisks on `Session.Interrupt`'s preempt guarantee are updated by
QUM-552 S4 — see that file's footer for the post-fix note.
