# Sprawl Messaging & Delivery Architecture — Ground Truth (2026-05-12)

> **Resolved by QUM-550** (2026-05-12): the design questions opened in Q1–Q5
> below have been answered. `send_async` and `send_interrupt` are collapsed
> into a single `send_message(to, body, interrupt)` tool. `interrupt: false`
> is strictly cooperative — never calls `Session.Interrupt`. `interrupt:
> true` unconditionally calls `Session.Interrupt` (with the QUM-549
> best-effort caveat below). `report_status` is now strictly cooperative
> and ships state+summary only. See the slice-1/slice-2 commits and
> `/internal/sprawlmcp/tools.go` for the canonical surface.
>
> **Slice 5 update (2026-05-12)**: the deprecated `send_async` /
> `send_interrupt` / `message` MCP tools and their supervisor wrappers
> have been **deleted entirely**. `send_message` is the sole messaging
> tool — there is no back-compat alias.
>
> **QUM-549 caveat**: `Session.Interrupt`'s observable preemption is
> reliable for streaming/thinking states only. While the recipient is
> awaiting a sprawl-side MCP tool response, the interrupt JSON reaches
> claude's stdin but the effect is not observable until that MCP call
> returns — sprawl's `readTurn` is parked inside `ToolBridge.HandleIncoming`.
> See `docs/research/qum-549-send-interrupt-during-mcp-tool-wait.md` for
> the full trace. The architectural fix is tracked in QUM-552. For hard
> recovery from a wedged MCP call, use `kill`.

Captures findings from a live diagnostic session that surfaced gaps between
documented behavior and actual implementation.

Cross-references: QUM-339 (inbox/maildir rethink), QUM-473 (legacy
InterruptDelivery overload umbrella, Med — §1 closed by QUM-550 slice 4: the
legacy `InterruptDelivery` is removed; only `WakeForDelivery` /
`ForceInterruptForDelivery` remain. See "Severity" below), QUM-542/543/544
(retire-hang incident that started the dig).

## TL;DR for the next operator

1. **There are TWO independent channels** into a running Claude subprocess:
   - **Queue drain at turn boundary** — cooperative, low-cost. The TurnLoop's
     `Run()` outer select parks on `Queue.Signal()` between turns
     (`internal/runtime/turnloop.go:109-114`). When a turn ends with
     `stop_reason=end_turn` (or any other stop reason — sprawl doesn't
     discriminate), control returns to the outer loop which drains pending
     messages and starts the next turn with them prepended to the prompt.
   - **`control_request` over Claude's stdin** — preemptive, mid-turn.
     `Session.Interrupt(ctx)` at `internal/backend/session.go:319-327` writes
     a JSON control frame `{"type":"control_request",...,
     "request":{"subtype":"interrupt"}}` directly to Claude's stdin. Claude
     aborts whatever it's doing and emits a turn-end result. The TurnLoop
     publishes `EventInterrupted` rather than `EventTurnCompleted`.

2. **(Pre-QUM-550) The MCP tool descriptions used to lie** — slice 1 fixed this; `send_async` now routes cooperatively, `send_interrupt` routes through the force path, and both are deprecated in favor of `send_message`.

3. **`interruptForDelivery` is conditionally interrupting — pre-QUM-550 behavior.** Slice 1 split this into `WakeForDelivery` (cooperative-only) and `ForceInterruptForDelivery` (unconditional preempt). The legacy `interruptForDelivery` is retained for callers not yet migrated. The wrapper at
   `internal/runtime/unified.go:404-423` `interruptForDelivery()` calls
   `Session.Interrupt` only if `turnRunning == true`. Always calls
   `Queue.Wake()`. So:
   - Recipient idle → enqueue + signal. Recipient picks up on next StartTurn.
   - Recipient mid-turn → enqueue + signal + control_request to claude's
     stdin → mid-turn preemption, current turn ends as Interrupted, drain
     fires immediately into the next turn.

   The caller has **no visibility** into which path will execute. Same MCP
   call, two very different cost profiles and UX outcomes.

4. **No Claude Code hooks wired.** Zero references to `PreToolUse`,
   `PostToolUse`, `SessionStart`, `Notification` in the codebase. The hooks
   system exists in Claude Code but sprawl doesn't use it. Mid-turn-between-
   tool-calls injection is not currently possible — though the hooks system
   would support it if we chose to wire it.

## Lifecycle & state machine

`internal/runtime/turnloop.go` + `internal/runtime/unified.go`:

- **`StateIdle`** — goroutine literally parked on `select` reading from
  `Queue.Signal()` or `ctx.Done()` at `turnloop.go:109-114`. Zero CPU.
  `TurnLoop.interruptCh == nil`.
- **`StateTurnActive`** — `TurnLoop.interruptCh != nil`. Set by
  `stateTrackingSession.StartTurn` at `unified.go:130`. Claude is processing
  a prompt; tool_use/tool_result roundtrips happen entirely within this
  state.
- **`StateInterrupting`** — set by `UnifiedRuntime.Interrupt()` at
  `unified.go:335` when Esc/Ctrl+B or `interruptForDelivery` calls Interrupt.
- **`StateStopped`** — terminal.

`end_turn` is a *transition signal*, not a state. When Claude emits a `result`
message with `stop_reason=end_turn`, `Session.readTurn` returns (closes its
events channel), `executeTurn` returns, `Run()` outer loop either drains the
queue or parks on `Signal()`. The agent might park for milliseconds, minutes,
or hours.

Sprawl does NOT discriminate on `stop_reason`. `end_turn`, `tool_use`,
`max_tokens` — all treated identically. The only special-case branching is
the `interrupted` flag on the result, which determines `EventInterrupted`
vs `EventTurnCompleted`.

## Esc/Ctrl+B interrupt path

When the user is observing weave and presses Esc during streaming/thinking:

1. TUI captures `tea.KeyEscape` in `internal/tui/app.go:527`. Gated to
   `turnState == TurnStreaming || TurnThinking` — typing in the input panel
   does NOT trigger interrupt.
2. Calls `m.bridge.Interrupt()` → `TUIAdapter.Interrupt()` (at
   `internal/tuiruntime/tuiadapter.go:191-204`).
3. → `runtime.Interrupt()` → `Session.Interrupt(ctx)`.
4. `Session.Interrupt` writes the control_request JSON to Claude's stdin
   via `transport.Send` (`internal/backend/session.go:319-327`).
5. Claude receives the control message, aborts the in-flight tool call,
   emits a turn-end `result` with `interrupted=true`.
6. TurnLoop catches the closing events channel, publishes
   `EventInterrupted`, TUIAdapter translates to `InterruptCompletedMsg`.

The string "User wanted to stop using the tool" you see in your transcript
**does not exist in sprawl's codebase**. It's generated by Claude itself
as part of the interrupted-tool-result the SDK synthesizes for the API
context.

(Note: persistent knowledge previously recorded "Ctrl+B" for this binding.
The code uses Esc. Persistent knowledge should be updated.)

## Per-MCP-tool trace table

> **Update (QUM-550 slice 4):** the `InterruptDelivery` /
> `interruptForDelivery` path referenced in the rows below has been removed.
> `send_async` no longer exists as a separate tool (use `send_message` with
> `interrupt: false`); `send_message(interrupt: false)` routes through
> `WakeForDelivery` (no Session.Interrupt); `send_message(interrupt: true)`
> routes through `ForceInterruptForDelivery` (unconditional). `report_status`
> routes through `WakeForDelivery` only. The table below is preserved as a
> pre-QUM-550 trace; treat it as historical.

| Tool | Path | Mid-turn interrupt? | Queue.Wake? | Notes |
|---|---|---|---|---|
| `send_async` | server.go:285 → SendAsync (real.go:798) → InterruptDelivery → interruptForDelivery (unified.go:404) | YES if recipient turnRunning | Always | Doc says "Does NOT interrupt" — wrong. |
| `send_interrupt` | server.go:307 → SendInterrupt (real.go:866) → InterruptDelivery → same path | YES if recipient turnRunning | Always | Functionally identical to `send_async` except for class flag. |
| `report_status` | server.go:359 → ReportStatus (real.go:973) → InterruptDelivery on PARENT | YES if parent turnRunning | Always | Fires on parent's runtime. Same call mechanics. |
| `delegate` | server.go:254 → Delegate (real.go:329) → runtime.Wake() ONLY | NO — never calls Session.Interrupt | Yes (via Wake) | The only message-class write that's strictly cooperative. |
| `merge` | various → emits `.poke` file in merged agent's worktree | NO (file-based) | NO | Read at next turn-init. |
| `spawn` | various → creates new agent | N/A (no recipient) | N/A | First-turn prompt is the "notification". |
| `kill` | sprawlmcp → unifiedHandle.Stop (post-QUM-543) | N/A | N/A | Process-level SIGKILL. No protocol message. |
| `peek` / `messages_*` / `status` | various | NO | NO | Read-only or self-mailbox; no signal. |

## `control_request` callers (writers to Claude stdin)

Three callsites emit `control_request` frames to Claude's stdin via
`transport.Send`:

- `session.Initialize()` at `internal/backend/session.go:156` —
  `subtype: "initialize"` (handshake)
- `session.Interrupt()` at `internal/backend/session.go:326` —
  `subtype: "interrupt"` (turn abort)
- `host/router.go:183` `router.Respond()` — control_request for MCP tool
  responses (the path by which tool_results get back to claude)

Only Session.Interrupt is invoked from the messaging/notification stack
(via `interruptForDelivery` when `turnRunning`).

## "draining N async message(s) into next prompt"

This banner is purely TUI cosmetics, emitted in
`internal/tui/app.go:604` `InboxDrainMsg` handler. The actual content
compositing happens in `internal/inboxprompt/inboxprompt.go:90`
`BuildQueueFlushPrompt()`. The banner fires when the TUI receives a queue-
drain event from the runtime; the drain itself is the turn-boundary
mechanism described above.

## Why this was hard to figure out

A diagnostic session at 2026-05-11 retire-hang incident took three rounds
of pushback to arrive at the truth above. Contributing factors:

1. **MCP tool descriptions in the system prompt contradict the code.** First
   read "send_async … Does NOT interrupt" was taken at face value.
2. **`InterruptDelivery` name implies always-interrupt;** code shows
   conditional behavior. The same identifier appears in three layers
   (QUM-473 already flagged this).
3. **`turnRunning` gates a major behavioral change** without surfacing to
   callers. Two cost regimes hidden behind one MCP call.
4. **Initial code-read research summary missed the `interruptForDelivery →
   Session.Interrupt` path** because it focused on the queue drain. Easy
   miss — the path is one if-branch deep in a helper function whose name
   doesn't suggest it.
5. **Persistent knowledge had stale binding info** (Ctrl+B vs Esc) that
   reinforced wrong mental model of the interrupt UX.

## Design questions opened (NOT decisions)

These are for the next design session — capturing for memory.

### Q1: Should `send_async` actually be non-interrupting?

Today's behavior — mid-turn preemption when recipient is running — is
either a bug or a feature depending on the design intent. Arguments:
- **Pro mid-turn preempt (current behavior):** snappy UX for inter-agent
  comms; recipients get parent feedback in near-real-time without
  parent having to wait minutes for the recipient's current turn to end.
- **Pro cooperative (matching docs):** preserves recipient's prompt cache;
  avoids interrupting tool execution; matches "async = don't urgently
  bother me" mental model.

### Q2: Should `send_interrupt` and `send_async` collapse?

They're functionally near-identical. Either:
- Collapse into one tool with a `priority` or `urgency` flag, OR
- Make `send_interrupt` actually different — e.g., unconditionally call
  Session.Interrupt regardless of turnRunning; or write a different
  control_request subtype if/when Claude supports one for "inject user
  message mid-stream without aborting tool".

### Q3: Should `report_status` carry full body content?

Today's `report_status({summary, detail})` puts the full body into the
parent's inbox prompt, which means engineers tend to fire both
`report_status` AND `send_async` back-to-back with overlapping content.
Make `report_status` terse (state + summary only, detail stored on disk
for lookup) and have `send_async` be the content channel. Forces a
cleaner separation: "what state am I in" vs "here's information for you".

### Q4: When to use which tool?

Today's overlap:
- `delegate` = "do this work" (has lifecycle; queued→started→done)
- `send_async` = "FYI" (no lifecycle; just content)
- `send_interrupt` = ??? (claims urgency, doesn't actually differ)
- `report_status` = "I'm in state X" (auto-targets parent; carries content)

Clean cut would be:
- `delegate` keeps its role — work assignment with task tracking.
- `send_async` keeps its role — content delivery between peers/parent.
- `send_interrupt` → either remove, OR repurpose as "truly preempt"
  (call Session.Interrupt unconditionally, even if recipient is idle —
  i.e., wake them now from idle vs waiting for next external wake).
- `report_status` → terse status only. State + summary in 1-2 lines.
  Body removed from the inbox prompt; detail stored on disk only.

### Q5: Hooks?

Claude Code supports hooks. We don't use them. Wiring a `PostToolUse`
hook that pulls from the queue would give us **between-tool-calls-
within-a-turn** delivery — the missing third point in the design space.
Probably not the first thing to do, but the option exists.

## Open follow-ups to file

- New High Bug: documentation-vs-reality gap on MCP tool descriptions.
  send_async doc says "Does NOT interrupt"; it does.
- Bump QUM-473 priority to High given how much misdirection the naming
  caused.
- New investigation: why didn't the `send_interrupt` I sent to wedged
  tower today actually preempt? Either Claude can't honor a control_request
  while waiting on a tool_result, or there's a state-tracking bug where
  `turnRunning` was incorrectly false at the time of send. Worth filing
  as a follow-up to QUM-542.
- Persistent-knowledge update: replace "Ctrl+B" with "Esc" for interrupt
  binding; add "send_async interrupts when recipient is running" caveat.
