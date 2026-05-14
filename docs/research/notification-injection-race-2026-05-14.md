# Notification injection race — tower↔finn soft-deadlock 2026-05-14 00:24 UTC

**Author:** ghost (researcher) — forensic investigation for weave / dmotles.
**Scope:** root-cause analysis only. No code changes.

## TL;DR

Tower's reply to finn was injected into finn's Claude Code SDK stdin **while
finn's SDK was mid-turn processing a `task-notification`** (Claude Code's own
background-bash completion event). The SDK queued tower's
`<system-notification>` prompt behind the in-flight turn. Sprawl's TurnLoop
believed it had delivered the prompt (it had — to the SDK's *internal* queue),
fired `OnQueueItemDelivered` → moved the envelope to `delivered/`, observed
the `result` event for the in-flight turn, and looped back to
`Queue().DrainAll()` → blocked on `Signal()`. Meanwhile the SDK dequeued
tower's prompt **on its own** ~46 ms later and started a follow-up turn that
generated a `mcp__sprawl__messages_read` tool_use — but that tool_use was
emitted on a session whose corresponding sprawl-side
`Session.StartTurn(...)` events channel had already closed. The MCP request
never reached the supervisor (zero entries in `mcp-calls.jsonl`) and finn
sat wedged until weave's 04:29 interrupt.

This is a **Claude Code internal-queue / sprawl-StartTurn-boundary aliasing
race**, not (as initially hypothesised) a queue-drain race on the sprawl side.
The on-disk pending→delivered move is correct; the
`<system-notification>` line *is* in the prompt. The break is downstream:
the prompt is processed in an SDK turn that sprawl is no longer streaming.

## Ms-precision timeline

Sources: `S` = `.sprawl/logs/mcp-calls.jsonl`,
`A` = `.sprawl/agents/finn/activity.ndjson`,
`J` = `/home/coder/.claude/projects/-home-coder-sprawl--sprawl-worktrees-finn/d4e7620d-7549-41f5-8bdc-9992fcb48fd8.jsonl`,
`Q` = `.sprawl/agents/finn/queue/delivered/*.json`.

| Time (UTC) | Src | Event |
|---|---|---|
| 00:24:06.797 | S | finn → tower `send_message` (blocker re: notify-tui-e2e) |
| 00:24:07.438 | S | finn `report_status(state=blocked)` |
| 00:24:10.016 | J:237 | finn assistant text "Noted — ask-user-question e2e passed (exit 0). Still waiting on tower's response re: notify-tui-e2e." |
| 00:24:10.069 | A | finn turn result `stop=end_turn turns=58` — sprawl TurnLoop returns to `DrainAll()` → blocks on `Signal()` |
| 00:24:14.063 | J:238 | **Claude Code SDK** internally enqueues `<task-notification>` (background bash `b11rcqgz6` = `make test-notify-tui-e2e` completed, exit 0). Sprawl is idle — no `StartTurn` is active. |
| 00:24:14.094 | J:239 | SDK dequeues the task-notification |
| 00:24:14.109 | J:240 | SDK starts a turn with `origin.kind=task-notification`, no sprawl `StartTurn` involved |
| 00:24:17–31 | J:241–248 | SDK autonomously runs 3 `Bash` tool_use calls (`tail`/`grep` task output). **Not surfaced in `activity.ndjson`** because sprawl is not in a turn. |
| 00:24:35.233 | S | tower → finn `send_message` (the "stop — verify your read" reply, short_id `r8n`) |
| 00:24:35.241 | Q | envelope `0000000002-async-3e8b98f8-…json` lands in finn's `queue/delivered/` (mtime). Already moved — see ordering below. |
| 00:24:35.251 | A | sprawl turn starts: `init` event in finn activity stream (sprawl-side StartTurn called for tower's notification) |
| 00:24:35.252 | J:249 | SDK records `queue-operation enqueue` for `<system-notification type="message">From tower — mcp__sprawl__messages_read(id=r8n)</system-notification>` — i.e. the SDK receives sprawl's prompt **but its task-notification turn (line 240→) is still in flight**, so it queues. |
| 00:24:36.185 | J:250 | SDK emits `assistant.thinking` (still on the task-notification turn) |
| 00:24:36.339 | J:251 | SDK emits `assistant.text "Waiting on tower's decision (Option 1 vs read QUM-569 first)."` — **stop_reason=end_turn**. Note the model wrote this text **without** having seen tower's `r8n` notification yet; it's the task-notification turn's final natural reply. |
| 00:24:36.378 | A | sprawl observes `result stop=end_turn turns=4` and `executeTurn` returns. TurnLoop loops → `DrainAll()` → empty → blocks on `Signal()`. |
| 00:24:36.379 | J:252 | SDK records `queue-operation dequeue` — pulling tower's queued notification |
| 00:24:36.385 | J:253 | SDK starts a follow-up turn with the dequeued `<system-notification>` as the user message (new `promptId d9173362-…`). **No sprawl `StartTurn` was called for this turn.** |
| 00:24:38.472 | J:254 | SDK emits `assistant.tool_use mcp__sprawl__messages_read(id=r8n)` — but the MCP request is never logged in `mcp-calls.jsonl` (zero entries between 00:24:07.438 and 04:29:27.462). |
| 00:24:38 → 04:29:27 | — | **Soft-deadlock.** SDK awaits a `tool_result` that never arrives. Sprawl TurnLoop blocks on `Signal()`. No further events. |
| 04:29:27.452 | Q | weave's interrupt envelope (`0000000003-interrupt-…json`) lands |
| 04:29:27.462 | S | finn `messages_read(id=r8n)` finally fires (force-interrupt rescue) |
| 04:29:27.479 | J | finn turn errors out (`result error stop=tool_use turns=3`) — see `internal/supervisor/runtime_launcher.go:330` `ForceInterruptDelivery` rescue path |

## Code map — what each layer thinks it's doing

- **`internal/agentloop/flush.go`** → re-exports `inboxprompt`.
- **`internal/inboxprompt/inboxprompt.go:111`** `BuildQueueFlushPrompt` produces the single-line `<system-notification type="message">From $AGENT — mcp__sprawl__messages_read(id=$ID)</system-notification>` (post-QUM-555 slim form). This is correct; the line we saw at `J:249` matches verbatim.
- **`internal/supervisor/runtime_launcher.go:327` `unifiedHandle.WakeForDelivery`** → `drainPendingToQueue()` lists pending envelopes, splits by class, and enqueues a `ClassInbox` / `ClassInterrupt` `QueueItem` on the runtime queue, then calls `rt.WakeForDelivery(ctx)`.
- **`internal/runtime/queue.go:81` `MessageQueue.Enqueue`** dedup-by-EntryID (QUM-460), pokes the signal channel.
- **`internal/runtime/turnloop.go:96-105` `TurnLoop.Run`** calls `DrainAll()`, then `executeTurn(prompt, items)` for each batch.
- **`internal/runtime/turnloop.go:165-181` `TurnLoop.executeTurn`** calls `Session.StartTurn(ctx, prompt)`, then — **immediately after `StartTurn` returns nil, BEFORE the events drain loop** (QUM-544 timing) — fires `OnQueueItemDelivered`, which `internal/supervisor/runtime_launcher.go:144` `agentloop.MarkDelivered` calls to rename `queue/pending/<file>` → `queue/delivered/<file>`. **This is why envelope `0000000002` was already in `delivered/` at 00:24:35.241 — the rename happened the instant sprawl handed the prompt to the SDK stdin.**
- **`internal/runtime/turnloop.go:198-211`** the events-drain loop terminates on `msg.Type == "result"` and `return`s — `executeTurn` is done.

### Where the assumption breaks

The `OnQueueItemDelivered` rename and the published `EventTurnCompleted` are
both treated as **"this prompt was delivered to and consumed by the agent."**
But "delivered to the SDK stdin" ≠ "processed in a turn that sprawl is
streaming." The SDK maintains its own internal user-prompt queue (visible as
`queue-operation enqueue/dequeue` lines in the SDK's JSONL). If the SDK is
already in a turn — for *any* reason, including its own
background-bash `task-notification` mechanism that runs **without sprawl
calling StartTurn** — the new prompt is buffered. The SDK will eventually
dequeue and run it autonomously, but the `Session.StartTurn(...)` events
channel that sprawl is reading has already terminated (with the *prior*
turn's `result`), so any tool_use emitted in the buffered-and-belatedly-run
turn is unobservable to sprawl and (apparently) unable to reach the MCP
transport. This is the deadlock.

## Root cause (ranked)

1. **(Strongest evidence) SDK-internal-queue ≠ sprawl-runtime-queue mismatch.** The SDK auto-runs turns triggered by its own `task-notification` mechanism (background-bash completion, see `J:238` / `J:240`) without sprawl initiating a `StartTurn`. When sprawl then calls `StartTurn` with a new prompt while one of those auto-turns is in flight, the SDK queues the prompt internally and processes it after the autonomous turn ends — by which time sprawl's `executeTurn` has already returned on the **previous** turn's `result` message. The follow-up turn's `tool_use` blocks of an MCP call that has no live channel to a willing server.
2. **(Plausible aggravator) `OnQueueItemDelivered` fires on `StartTurn` return, not on observed turn completion.** QUM-544 intentionally tightened this for stuck-MCP recovery (so `pending/` doesn't strand on a hung turn). The side effect is that the envelope is marked `delivered` **before** sprawl has any signal that the SDK has *actually rendered* the prompt to the model. Combined with (1), this hides the bug from operators: `ls delivered/` shows everything looks fine.
3. **(Discardable) Per-class queue separation gap.** Sprawl already separates `ClassInterrupt`/`ClassTask`/`ClassUser`/`ClassAsync`/`ClassInbox` in `internal/runtime/queue.go:27-42`. The race isn't in sprawl's queue — it's in the SDK's. Per-class separation on the sprawl side does not help.

QUM-510 is a prior occurrence of the same class: "Manager wake-loss when
child-completion + background-task notification land simultaneously"
(2026-05-07). The mitigation in QUM-510 addressed wake-coalescing on the
sprawl side; the deeper issue — the SDK autonomously running turns that
sprawl is not streaming — was not resolved.

## Repro recipe (deterministic-ish)

1. Spawn an engineer child `A`. Have it call `Bash(run_in_background:true)` on a sleep-and-emit task (e.g. `sleep 20 && echo done`).
2. While the background task is running, have a peer `B` send `A` a cooperative `send_message` (interrupt=false). Time it so the background bash completes **after** the message envelope lands in `A`'s `queue/pending/` but **before** `A`'s sprawl-side prompt for the notification has been consumed by an SDK turn (i.e., the SDK is mid-task-notification turn when sprawl's `StartTurn` writes the system-notification to stdin).
3. Expected: `A`'s SDK queues the system-notification, finishes the task-notification turn (which never sees the message), then auto-starts a follow-up turn on the dequeued notification. The follow-up turn's `mcp__sprawl__messages_read` tool_use hangs; `A` is soft-wedged until an interrupt arrives.

Programmatic harness sketch: bypass MCP — drive the SDK's stdin directly to enqueue a `task-notification` synchronous with a sprawl `StartTurn` and assert that the tool_use response surfaces in `mcp-calls.jsonl` within N seconds. Today it doesn't.

## Recommended fix-ticket scope

File a new bug ("SDK-internal queue runs follow-up turns whose tool_use events bypass sprawl's TurnLoop streaming"). Pick **one** of:

- **(a) Drain the SDK's internal queue inside one `executeTurn`.** Continue streaming events past the first `result` until the SDK reports its internal queue is empty (or until a configurable boundary). Requires Claude Code SDK telemetry we may not have — `queue-operation` events are visible in the JSONL but not necessarily on the events channel.
- **(b) Block sprawl `StartTurn` until SDK is idle.** Detect the SDK's `queue-operation enqueue/dequeue` events; defer `Session.StartTurn(prompt)` if the SDK has a non-empty internal queue. Requires the same telemetry.
- **(c) Defense-in-depth: post-turn pending sweep + force-restart-turn-if-still-pending.** After `executeTurn` returns, if `ListPending()` shows envelopes that were renamed to `delivered/` during this turn but the SDK emitted no `messages_read` tool_use for them, force a follow-up `WakeForDelivery`. This is symptomatic but cheap and lines up with QUM-512's defense-in-depth proposal.
- **(d) Interrupt-as-rescue mechanism (operator-facing).** Add a watchdog that detects "envelope marked delivered, no `messages_read` call within T seconds, agent reports `state=blocked` or `running` with no MCP activity" and auto-injects an interrupt-class envelope. This is what weave manually did at 04:29 today.

Recommend (c) + (d) as the practical near-term combo while (a)/(b) needs SDK-side investigation. None of them changes the queue-format or the on-disk drain semantics — those are correct.

## Related prior issues

- **QUM-510** — same class: simultaneous task-notification + peer message → wake-loss. Manager-side mitigation only.
- **QUM-512** — defense-in-depth: per-iteration `ListPending` reconciliation in `TurnLoop.Run` (proposed, backlog).
- **QUM-465** — inbox double-fire under unified runtime (different bug, but same notifier surface).
- **QUM-549/QUM-550** — interrupt-during-MCP-wait semantics: documents that an interrupt sent while the recipient is awaiting an MCP-tool response only becomes observable after that tool returns. Adjacent failure mode.
- **QUM-555/QUM-556/QUM-562** — flush-prompt slim-frame format. Rendered line at `J:249` matches QUM-555 form exactly; no defect in the formatter.
- **QUM-559** — `report_status` now ephemeral-only, no maildir. Not implicated here.
- **QUM-569** — backlog tracker for restoring real-claude drain-row e2e coverage (mentioned by both finn and tower; tangentially adjacent — a real-claude harness would catch this class of race).

## Reflections / open questions

- **Surprise:** I expected the bug to be on the sprawl-side queue plumbing. It's not — the `<system-notification>` line *is* in the SDK's stdin queue (J:249), exactly as designed. The break is one layer down: the SDK runs an autonomous turn (driven by its own `task-notification` mechanism) that sprawl never opened a stream for.
- **Open:** Why did the `mcp__sprawl__messages_read` tool_use at 00:24:38.472 produce zero MCP traffic? Two sub-hypotheses I did not fully resolve: (i) the MCP stdio channel is bound to a specific `Session.StartTurn` invocation and tears down on `result`; (ii) the MCP request *was* sent but the response routing is keyed on the StartTurn-scoped events channel and dropped silently. The Claude Code SDK source — not in this repo — would settle this. The `mcp-calls.jsonl` is updated only on supervisor-side tool dispatch (`internal/sprawlmcp/server.go`), so absence there means the request never reached us; (ii) is the more likely sub-hypothesis.
- **Open:** Is there a deterministic way to detect "SDK has an internal queue entry"? `queue-operation` events appear in the SDK's JSONL but I did not check whether they're surfaced on the streaming `events` channel that `Session.StartTurn` returns. That's the cheapest fix-enabler.
- **Next investigation if I had more time:** instrument `internal/backend/claude/adapter.go` to log the raw protocol envelope just before each `StartTurn` write and just after each `result` read, plus any out-of-band `queue-operation` events. Replay finn's session against that instrumentation to confirm hypothesis (1).
