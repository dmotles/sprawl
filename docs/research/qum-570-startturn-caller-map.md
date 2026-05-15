# QUM-570 — `Session.StartTurn` caller map & refactor risk audit

**Author:** ghost (researcher) — pre-refactor audit for QUM-570 (persistent stream reader).
**Companion to:** `docs/research/notification-injection-race-2026-05-14.md` (the "what's broken" forensic doc).
**Scope:** map every production caller of `backend.Session.StartTurn`, document the events-channel-close contract each depends on, identify invariants the refactor must preserve, and recommend a fix approach.

## TL;DR

* Production has **exactly one** caller of `Session.StartTurn`: `internal/runtime/turnloop.go:165` (`TurnLoop.executeTurn`). Everything else (`stateTrackingSession`, `host.Session.SendUserMessage`) is a forwarder or test/legacy.
* Every production path relies on three coupled invariants: (i) the events channel closes **after** a `result` message is delivered; (ii) `turnInProgress` releases on channel close; (iii) `drainInflight` runs before `readTurn` returns. The persistent-reader refactor moves (i) onto a per-turn subscription, must keep (ii) gating only sprawl-side concurrent `StartTurn`s, and must move (iii) to session-shutdown rather than per-turn end.
* Inline `control_request` dispatch (`handleInlineControlRequest`, including the QUM-552 async `mcp_message` path) is wired through the per-turn `initSpec` snapshot. The snapshot itself is already cached in `s.initSpec` at `Initialize` time — making it reachable from a session-scoped reader is mechanical.
* **Recommendation: option (a), persistent reader + per-turn subscription.** Option (c) (post-turn pending sweep) is worth shipping in the same PR as defense-in-depth; (b) and (d) are not.
* Biggest risk: TurnSpec-per-turn `ToolBridge` override is exercised only by tests today, but if anything relies on per-turn swapping it can't trivially live under a session-scoped reader. Verify nothing does, then delete the seam.

## 1. Caller map

### 1.1 Production callers (transitively reach `backend.Session.StartTurn`)

| # | Site | Forwarder? | Channel-close contract relied on |
|---|---|---|---|
| 1 | `internal/runtime/turnloop.go:165` `TurnLoop.executeTurn` | No — root caller | Closes after `result` → unblocks the `<-events` select → `executeTurn` returns, runs deferred state cleanup, TurnLoop loops back to `DrainAll()`. Also: channel close is the **sole** "turn is over" signal observed by `stateTrackingSession`'s forwarder goroutine. |
| 2 | `internal/runtime/unified.go:137` `stateTrackingSession.StartTurn` | Yes — wraps SessionHandle | Forwards inner channel through a per-call goroutine. **Inner channel close is the trigger to flip `state=Idle` and `turnRunning=false`** (lines 181-188). |
| 3 | `internal/host/session.go:43` `host.Session.SendUserMessage` | Yes — backend.Session passthrough | Only consumer is `cmd/hosttest/main.go` (smoke binary, not on the sprawl/weave hot path) and `internal/host/*_test.go`. Production weave + child do not flow through this. |

Backend session construction sites (where a `backend.Session` is actually wired up against `TurnLoop`):

* `cmd/enter.go:413` — weave root: `backendclaude.NewAdapter(...).Start(...)` → `runtimepkg.New(...)`.
* `internal/supervisor/runtime_launcher.go:91` — children: `unifiedAdapterStartFn(...)` → `runtimepkg.New(...)`.

Both paths build a `UnifiedRuntime`; `UnifiedRuntime.Start` wraps the session in `stateTrackingSession` and hands it to a `TurnLoop`. So every production `StartTurn` ultimately resolves to caller #1 above.

### 1.2 Test callers (informational — refactor blast radius)

Direct `Session.StartTurn` callers in test code (channel-close semantics are asserted on by some):

* `internal/backend/session_test.go:212, 241, 317, 376, 419` — backend unit tests.
* `internal/backend/session_async_dispatch_test.go:95, 143, 206, 268` — QUM-552 async-MCP-dispatch invariant tests.
* `internal/runtime/turnloop_test.go:*` — `fakeSessionHandle`-style doubles; assert turn lifecycle.
* `internal/runtime/unified_test.go:*` — runtime state-tracker tests.
* `internal/supervisor/runtime_launcher_test.go:61,987` — `fakeBackendSession{,WithStartErr}.StartTurn`.
* `internal/supervisor/weave_handle_test.go:27` — `resultEmittingSession.StartTurn`.
* `internal/supervisor/runtime_test.go:32` — `runtimeTestSession.StartTurn`.
* `internal/tui/app_child_unified_test.go`, `internal/tuiruntime/tuiadapter_test.go` — adapter tests.

Every fake constructs a channel that it closes (often after a single `result`) to terminate `executeTurn`. The refactor's biggest test-touch surface is the fake-session population — each fake will need a "no, the *stream* doesn't end, only the per-turn subscription does" upgrade. That's mechanical but ≥10 files.

## 2. Inline control-request consumers

* `internal/backend/session.go:258-264` — `readTurn` intercepts every `control_request` and routes through `handleInlineControlRequest` **before** the events channel sees it. The control_request frame is never delivered to subscribers.
* `internal/backend/session.go:305-346` — `handleInlineControlRequest`:
  * `mcp_message` subtype → `dispatchMCPAsync` (QUM-552). Tracked in `s.inflight`, drained on `readTurn` exit.
  * `can_use_tool` subtype → synchronous "allow" response on the transport.
  * Other subtypes → synchronous empty `success`.
* `internal/backend/session.go:352-417` — `dispatchMCPAsync`: pulls `initSpec.ToolBridge`, calls `HandleIncoming` in a goroutine, sends a `control_response`. Bridge ctx parents off the per-turn `parentCtx`.
* `initSpec` flow: stored in `s.initSpec` at `Initialize` time (`session.go:154`), captured per-turn at `StartTurn` time (`session.go:199-204`, with optional per-turn override from `TurnSpec`). Only the captured copy is threaded through `readTurn` → `handleInlineControlRequest`.

**Persistent-reader implication:** the inline control-request path is already initSpec-stateful and can be served from a session-scoped reader by reading `s.initSpec` directly (no need for per-turn capture). The per-turn `TurnSpec.Init` override at `session.go:199-204` is unused by production callers — confirmed by grep: all `TurnSpec{Init: ...}` usage is in `_test.go` plus `internal/host/session.go:43` (compat shim with no prod consumer). The persistent reader can drop the override seam.

`ToolBridge` implementations live in `internal/host/mcp_bridge.go` (`*MCPBridge` is the production wiring; both weave and children pass an `*MCPBridge`). It is goroutine-safe and bound to the supervisor for the host's lifetime, so reaching it from a session-lifetime reader is trivial — it's the same pointer either way.

## 3. Lifecycle / shutdown audit

Session lifecycle today:

1. `backend.NewSession` (no goroutines).
2. `session.Initialize(ctx, spec)` — sends an `initialize` control_request, runs its own private `Recv` loop on `s.transport` until it sees a `control_response` (`session.go:174-188`). Stores `s.initSpec = spec`. **After Initialize returns, no goroutine is reading `transport.Recv`.**
3. Per-turn: `StartTurn` → spawns `readTurn` goroutine → reads until `result` → `defer close(events); defer turnInProgress=false; defer drainInflight()` (in that lexical order, executed in reverse: drainInflight, then turnInProgress flip, then events close).
4. Session shutdown: `handle.Stop` → `rt.Stop` → calls `Session.Interrupt` (cancels in-flight async MCP handler ctxs as a side effect, `session.go:462-467`) → ctx-cancels the loop → `teardownSession` (`Close → Kill → bounded Wait`, QUM-545).

Load-bearing invariants each caller relies on:

* **`turnInProgress`** (`session.go:113, 193-197, 230`): protects against concurrent sprawl-side `StartTurn`s on the same session. Refactor must keep gating concurrent **logical** turns (sprawl can't have two prompts mid-flight) but must **not** gate the persistent reader itself.
* **`drainInflight`** (`session.go:236, 421-438`): the QUM-552 invariant. Async MCP handlers must have their ctxs cancelled and the wait group drained (bounded 5s) before the goroutine that owns the transport exits. Today this is per-turn (per `readTurn` exit). **Under a persistent reader, this must move to session shutdown** — i.e. the reader goroutine's defer. If the reader runs for the session lifetime, drainInflight is fired exactly once, at session close, which is structurally correct.
* **`lastTurnErr`** (`session.go:198, 247, 491-495`): cleared on `StartTurn`, set on read errors / send errors. Currently scoped to "this turn". Under persistent-reader semantics, errors on the reader goroutine outside an active sprawl turn need a new home — likely an Observer / event-bus channel, since `LastTurnError()` is consumed by per-turn callers.
* **Observer fan-out** (`session.go:182, 254`): the Observer (single instance, attached at Initialize and per-Recv-call) sees every inbound message. Production attaches an activity ObserverWriter via the EventBus subscriber, not via `SessionConfig.Observer` directly (see `runtime_launcher.go:85-89`, which explicitly does NOT set `sessionSpec.Observer`). Good — the event-bus pathway is the one we want; the unused Observer slot can become the fan-out point for autonomous turns.
* **`Interrupt`** (`session.go:440-469`): cancels every in-flight MCP handler ctx synchronously with the wire-level interrupt write. Independent of `turnInProgress`; not affected by the refactor.

## 4. Vote on the forensic doc's options

The forensic doc's ranked options were (a) drain SDK queue inside `executeTurn`, (b) block `StartTurn` until SDK idle, (c) post-turn pending sweep, (d) watchdog. QUM-570 reframes (a) as the architectural shift to a persistent reader.

**Pick (a) as primary. Ship (c) alongside as defense-in-depth. Defer (b) and (d).**

* **(a) persistent reader / session-scoped event bus.** Direct root-cause fix. The only architectural sharp edge is moving `drainInflight` to session shutdown, which is structurally clean (one fewer per-turn coupling). All production `StartTurn` callers funnel through `TurnLoop.executeTurn`, so the contract change has one principal consumer to update. The per-turn `TurnSpec.Init` override is dead code in production and can be retired. Autonomous-turn detection slots in naturally as a reader-side classifier ("no sprawl subscriber active → autonomous frame; still publish to observers; still serve `control_request`s").
* **(c) post-turn pending sweep.** Independent of the architectural change, cheap, and catches a class of regressions that aren't covered even by (a): e.g. if a future SDK semantics change once again hides a turn from us, (c) detects the "delivered envelope without `messages_read` response" anomaly and re-wakes. Lines up with QUM-512's existing proposal. Should land in the same PR if scope allows; otherwise a fast follow-up.
* **(b) block-StartTurn-until-SDK-idle.** Requires SDK telemetry we lack (the forensic doc's open question on `queue-operation` events) and adds latency on every turn for no reason once (a) is in place. Skip.
* **(d) watchdog.** Operator-facing rescue. Useful if (a) lands and somehow misses an edge case, but (a)+(c) makes it redundant for the deadlock class. Operator wedge-detection has independent value (escalation, observability) and belongs in a separate ticket if pursued.

## 5. Risk callouts

1. **`stateTrackingSession`'s channel-close = "turn ended" coupling** (`internal/runtime/unified.go:167-188`). After the refactor, the per-turn subscription channel still closes on logical turn end, so this code path should be OK if the persistent reader publishes "subscription closed" precisely on `result`. But it's the load-bearing state-update for `RuntimeState`; mis-timing here will silently break `peek`/`status`. Worth a focused test that asserts state flips back to Idle on every logical-turn-end (sprawl-initiated and autonomous).
2. **Reader/Initialize race.** `Initialize` runs its own `Recv` loop today. If the persistent reader starts in `NewSession` (per the issue spec's option) it must not race with `Initialize`'s loop, or both will pull from the transport. Cleanest: keep `Initialize`'s synchronous Recv but start the persistent reader after `Initialize` returns — slightly different from the spec's "from session-start" wording but probably what's meant.
3. **`TurnSpec.Init` per-turn override.** Allowed by the API; only tests + the host compat shim use it. If the persistent reader is keyed on `s.initSpec`, the per-turn override silently stops working. **Verify** no future caller plans to swap ToolBridge mid-session before deleting the seam. Otherwise the refactor breaks the seam silently.
4. **`turnInProgress` semantics.** Currently a single mutex flag guarding the per-turn-reader's lifetime. Repurposed for "is there a sprawl-side logical turn open?" the flag has the same shape, but the set/clear edges move (set on `StartTurn`, cleared on `result` observed by the reader, not on reader exit). A `logicalTurnActive` rename + per-turn signal struct will read more cleanly.
5. **`drainInflight` timing.** Critical: if the reader observes a `result` and the per-turn subscriber returns, there can still be in-flight async MCP handlers in `s.inflight`. Under today's code, the per-turn `readTurn` defers `drainInflight`. Under the refactor, `drainInflight` only runs on session shutdown — which is correct, but means a sprawl-side `Session.Interrupt` (called from `rt.Stop`, line 284 of unified.go) is the sole synchronous cancellation surface for handlers from a prior turn. Already does the right thing (`session.go:462-467`); just must not regress.
6. **Test blast radius.** Every fake `Session.StartTurn` returns a channel and closes it. The new contract is "channel closes on next `result` after prompt acked; stream persists". Fakes will need to expose a "feed a result" method instead of pre-closing. ≥10 test files touched (see §1.2). Mechanical but tedious.
7. **`LastTurnError`** retains per-turn semantics in the API but the reader can now produce errors *between* turns. Need to decide whether those bubble up as session-level errors (probably yes — they typically mean transport is dead, so a `result` will never arrive and `executeTurn` would hang forever otherwise).
8. **`host.Session`'s `SendUserMessage` compat shim.** No prod consumer. Delete it in the same PR or leave alone; either is fine. Just don't promote it to a supported surface.
9. **Reproducer test fidelity.** The acceptance criterion #2 in QUM-570 mentions both a real-claude and a mock-transport variant. The mock variant is the easier one (the existing `mockManagedTransport` in `session_test.go` already supports scripted message sequences). Real-claude reproduction is genuinely hard because the SDK's autonomous-turn trigger (background-bash completion) is not directly drivable in unit tests; tracking real-claude coverage in QUM-569 is probably the right move.

## 6. Open questions (for dmotles)

1. **Persistent reader start point.** Spec says "in `Session.Start()` (or first `StartTurn`)". Current code has no `Start` — closest is `Initialize`. Acceptable to start the reader at `Initialize` *after* the synchronous control_response is observed? Or do we want a new explicit `Session.Start(ctx) error` API call?
2. **Autonomous-turn event signature.** Forensic timeline cites `init` event with `origin.kind=task-notification` and a fresh `system` frame. Is the protocol contract reliable enough to detect "autonomous turn started" off `system`/`init` alone, or should we treat *any* event arriving while no sprawl subscriber is open as an autonomous frame? Tilts the classifier toward "stream-state" vs "per-message-type".
3. **Subscribe() shape.** The issue suggests an optional `Subscribe()` for observers. Concrete contract: do observers see every event in order, including those during a sprawl-active turn? (Probably yes — they're observers, not per-turn subscribers.) Backpressure behavior on slow observer?
4. **`turnInProgress` repurposing vs replacement.** Keep the field as-is (semantics shift, mutex stays) or introduce a separate `logicalTurnActive` and let `turnInProgress` track reader liveness? Naming-only but affects diff size.
5. **`TurnSpec.Init` override.** Delete? Or preserve as no-op + slog.Warn for one release?
6. **Activity persistence for autonomous turns.** Spec criterion #4 says "Persist activity (so peek/status are honest)". The activity subscriber already listens to `EventProtocolMessage` on the EventBus, so as long as autonomous-turn messages publish onto the bus, this is free. Confirm: the persistent reader should fan out to the bus regardless of whether a sprawl subscriber is attached?
7. **Reproducer scope.** Are we accepting the mock-transport reproducer as sufficient for landing, with a real-claude reproducer tracked under QUM-569? Or is real-claude blocking?

---

### Reflections (per researcher protocol)

* **Surprising:** the production caller map is *much* simpler than I expected — exactly one root `StartTurn` site (`TurnLoop.executeTurn`) once you peel back the wrappers. The breadth lives in test fakes, not production seams. That makes the architectural shift cheaper than the forensic doc's "few hundred LOC, callers' contract everywhere" framing suggests.
* **Also surprising:** the per-turn `TurnSpec.Init` override is effectively dead in production — every weave/child path relies on `s.initSpec` captured at `Initialize`. The API surface implies per-turn bridge swapping is supported; in practice nothing does it.
* **Open in my head:** I did not validate the exact protocol shape of a Claude-Code-initiated autonomous-turn frame end-to-end. The forensic doc cites `J:240` `origin.kind=task-notification` as the signal, but I didn't trace it through `internal/protocol` to confirm sprawl parses that field. If sprawl can't distinguish autonomous from sprawl-initiated turns from the stream alone, the simpler heuristic ("no sprawl subscriber attached → autonomous") still works. Worth a 30-min protocol-parser check during implementation.
* **If I had more time:** I'd write the mock-transport reproducer test now (RED), to make the engineer's TDD setup zero-cost. The existing `mockManagedTransport` in `session_test.go` is already wired for scripted multi-message sequences, so the reproducer is mostly stitching: enqueue a `system`+`tool_use`+`result` autonomous-turn sequence after the sprawl-initiated turn's `result`, attach an observer, assert the observer (and an MCP bridge mock) saw the autonomous-turn events.
