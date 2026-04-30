# Design: Messaging + Notification Overhaul (MCP-based, async + interrupt)

**Status:** Draft
**Author:** ghost (researcher agent)
**Tracking issue:** [QUM-277](https://linear.app/qumulo-dmotles/issue/QUM-277/design-messaging-notification-overhaul-mcp-based-async-interrupt)
**Date:** 2026-04-21
**Related punchlist items:** `docs/todo/punchlist.md` #1 (messaging overhaul) and #2 (sub-agent visibility)

---

## 1. Summary

Replace the `tmux send-keys`–based notification path with an MCP-and-harness–owned
delivery path that can cleanly inject messages into a Claude subprocess without
fighting the user's keyboard or modal overlays. Introduce two explicit message
classes — **async** (deliver on next yield) and **interrupt** (pause, inject,
resume) — and give the harness first-class visibility into sub-agent activity
so the TUI and `sprawl status` can surface what a child is actually doing.

This is a design-only doc. Implementation is decomposed into child issues at
the end.

---

## 2. Motivation

Today two independent systems race for the same input stream:

1. The user's keyboard (typing into `sprawl enter`, `AskUserQuestion` modals,
   confirmation dialogs, permission prompts).
2. `tmux send-keys` invocations issued by `cmd/messages.go` and `cmd/report.go`
   to deliver notifications to the root window.

The harness has no way to know whether Claude is mid-turn, waiting on a modal,
or idle at the prompt — it just pipes keystrokes in and hopes for the best.
When it loses that bet, the consequences range from harmless (a notification
line appears mid-sentence) to destructive (an `Enter` terminates a half-typed
command, or a notification is injected into an `AskUserQuestion` text field).

A second, orthogonal problem: when `weave` spawns a sub-agent, it cannot see
what the child is doing until the child reports back. `sprawl report status`
exists but is underused and opaque; the TUI has no panel for child activity.
Sub-agent silence makes the manager agent pattern hard to reason about.

### 2.1 Goals

- **G1.** Never send keystrokes into an agent's tty for the purpose of
  delivering a message. All message delivery goes through the harness, which
  owns the queue and the decision of when to flush.
- **G2.** Two clearly-named message classes with well-specified semantics:
  `async` and `interrupt`.
- **G3.** Sub-agent visibility: the harness exposes a structured "what is this
  child doing right now?" surface consumable by the TUI and `sprawl status`.
- **G4.** `coder_report_task`-style status reporting from child → parent, via
  MCP, persisted on disk.
- **G5.** Migration is phased. `tmux send-keys` stays as a fallback for
  headless/tmux-mode sessions until the MCP path is proven.

### 2.2 Non-goals

- Redesigning the Maildir persistence layout (`internal/messages`).
- Replacing the filesystem-based task queue (`state.EnqueueTask`).
- Changing the supervisor API surface beyond what the new tools require.
- Remote/cross-host agents. Everything described here is intra-host.

---

## 3. Current-state inventory

Full references in the research briefing; summary here.

### 3.1 Supervisor (`internal/supervisor/{supervisor,real}.go`)

Implements `Supervisor` interface: `Spawn`, `Status`, `Delegate`, `Message`,
`Merge`, `Retire`, `Kill`, `Handoff`, `HandoffRequested`. `Message` is a thin
wrapper over `internal/messages.Send`, which writes Maildir files and — as a
side effect — a sentinel `.wake` file under `.sprawl/agents/{name}.wake` and,
for root, a `tmux send-keys` notification line.

### 3.2 Agent loop (`cmd/agentloop.go`, `cmd/rootloop.go`)

Each agent runs inside a long-lived subprocess wrapper that cycles through six
phases: kill check → stale-state check → `.poke` file → task queue → inbox
(`.wake`) → idle sleep. Between turns, `sendPromptWithInterrupt` polls for
`.poke` and `.wake` files every 500 ms and, if one appears mid-turn, calls
`proc.InterruptTurn` (which maps to `host.Session.Interrupt`, a
`control_request` with subtype `interrupt` in the Claude Code SDK).

The `processManager` interface exposes `SendPrompt(ctx, prompt)` and
`InterruptTurn(ctx)` — both backed by the SDK protocol in `internal/host`.

### 3.3 Host session (`internal/host/session.go`)

Owns the SDK JSON-RPC-over-stdio link to Claude. Key operations already
available:

- `SendUserMessage(ctx, prompt)` — sends a `{"type":"user", ...}` frame and
  returns a channel of events terminated by a `result` message.
- `Interrupt(ctx)` — sends a `control_request` with subtype `interrupt`.
- `handleInlineControlRequest` — routes MCP tool calls and `can_use_tool`
  checks back to the registered `MCPBridge`.

This means the harness *already* has primitive support for mid-turn user-turn
injection and interrupt; we don't need to invent new transport mechanics.

### 3.4 MCP server (`internal/sprawlmcp/{server,tools}.go`)

Registered as `sprawl` via `host.MCPBridge`. Exposes `spawn`,
`status`, `delegate`, `message`, `merge`,
`retire`, `kill`, `handoff`. `message` today is
functionally identical to `sprawl messages send` — it writes to Maildir and
fires the same `.wake` notification as the CLI path.

### 3.5 CLI / tmux notification path

`cmd/messages.go` (Send, line 199) and `cmd/report.go` (notifyParent, line 157)
are the only two sites that call `tmux.SendKeys` for notification. Both
target the root window. `internal/tmux/tmux.go:287` is the single send-keys
implementation.

### 3.6 TUI (`internal/tui`)

Bubble Tea composition: tree, viewport, input, status bar, palette. Polls
`supervisor.Status()` on a tick to refresh the tree. There is no "activity
panel" for a focused agent — the viewport shows messages (today mostly in
tests), not a live protocol-event tail.

### 3.7 State on disk

```
.sprawl/agents/{name}.json          # AgentState (status, parent, report fields)
.sprawl/agents/{name}.poke          # sentinel: interrupt + content
.sprawl/agents/{name}.wake          # sentinel: "you have mail"
.sprawl/agents/{name}.kill          # sentinel: shutdown
.sprawl/agents/{name}/tasks/*.json  # task queue (FIFO)
.sprawl/messages/{name}/{new,cur,archive,sent}/*.json
```

### 3.8 Pain points

- **tmux send-keys races user keyboard.** Unavoidable at the protocol layer;
  tmux has no mutex for concurrent input sources.
- **`.wake` polling latency.** Worst case ~3.5 s between a `sprawl messages
  send` and inbox delivery, because the agent loop's idle sleep is 3 s and
  the poke/wake poll is 500 ms.
- **`.poke` content is delivery-at-most-once across restarts.** If the
  subprocess dies between reading and acking, the poke file is gone.
- **No shared child-activity surface.** `tmuxObserver.OnMessage` sees protocol
  events but discards them; nothing exposes them to parent or TUI.
- **`sprawl report` writes state + sends a Maildir message + (optionally)
  tmux-notifies the root.** Three paths, easy to miss one.

---

## 4. Proposed design

### 4.1 Message classes

| Class | Direction | Semantics | Primary use |
|-------|-----------|-----------|-------------|
| `async` | any → any | Queued. Target reads on next yield (between turns). No interruption. | Sub-agent status, peer coordination, low-priority handoffs. |
| `interrupt` | any → any (conventionally parent → child) | Queued. Target's current turn is interrupted; message is injected as a user turn; target resumes unless told to stop. | "I forgot to mention", mid-run corrections, urgent context. |

Both classes go through the same harness queue; they differ only in whether
the harness calls `Session.Interrupt` before flushing.

### 4.2 MCP tool surface

Four new tools on `sprawl`. `message` is kept as a deprecated
alias for `send_async` during the migration window (§7).

#### 4.2.1 `send_async`

```
name:        send_async
description: Queue an asynchronous message for a peer or child agent. The
             recipient will receive it on its next yield (between turns).
             Does not interrupt. Persisted; survives crashes.
params:
  to:      string  (required)  target agent name
  subject: string  (required)  ≤80 char human-readable label
  body:    string  (required)  markdown body, no length cap
  reply_to: string (optional)  message ID this replies to (threading)
  tags:    [string] (optional) e.g. ["status", "question", "fyi"]
returns: { message_id: string, queued_at: RFC3339 }
```

Semantics:
1. Persist the message to `.sprawl/messages/{to}/new/{id}.json` (existing Maildir).
2. Append a compact "notification envelope" to the target's **harness inbox
   queue** (§4.3) — an in-memory + on-disk append-only log the target's
   agent loop drains between turns.
3. Return immediately. No tmux. No sender-side blocking beyond the
   persist+enqueue.

#### 4.2.2 `send_interrupt`

```
name:        send_interrupt
description: Interrupt the target agent mid-turn and inject this message as
             a user turn. The target then resumes what it was doing unless
             the body explicitly directs it to stop. Use sparingly — this is
             the "I forgot to tell you something important" channel.
params:
  to:      string  (required)
  subject: string  (required)
  body:    string  (required)
  resume_hint: string (optional) free-form hint the target can quote back to
                itself after reading (e.g. "you were implementing X").
returns: { message_id: string, delivered_at: RFC3339, interrupted: bool }
```

Semantics:
1. Persist to Maildir (same as async).
2. Enqueue into the target's **harness interrupt queue** with
   `interrupt_on_flush = true`.
3. The target's harness, on receiving the enqueue signal, immediately calls
   `host.Session.Interrupt(ctx)` on the target's active session. The current
   turn terminates (SDK emits a `result` with stop reason
   `tool_use`/`interrupt`).
4. Harness then calls `host.Session.SendUserMessage(ctx, formatted)` with a
   rendered interrupt frame (§4.5).
5. `delivered_at` is set after the harness confirms the inject; `interrupted`
   is true iff the session was actively mid-turn at flush time.

#### 4.2.3 `report_status`

```
name:        report_status
description: Report status to this agent's parent. Structured, first-class.
             Replaces ad-hoc `sprawl report` for MCP-aware agents.
params:
  state:   enum { "working", "blocked", "complete", "failure" }  (required)
  summary: string  (required, ≤160 chars, coder_report_task-compatible)
  detail:  string  (optional, markdown, no cap)
returns: { reported_at: RFC3339 }
```

Semantics:
1. Write to the reporter's own `AgentState` (`LastReportType`,
   `LastReportMessage`, `LastReportAt`, plus a new `LastReportDetail`).
2. If `state` is `complete` or `failure`, set `AgentState.Status` accordingly
   (same rule as today's `cmd/report.go`).
3. If the reporter has a parent, enqueue an `async` message to the parent
   with subject `[STATUS/COMPLETE/FAILURE] {reporter} → {summary}`.
4. Emit a **status-tick event** on the harness event bus (§4.4) so the TUI
   and `sprawl status` can surface it live without polling.

#### 4.2.4 `peek`

```
name:        peek
description: Inspect a child or peer agent's recent activity. Returns the
             last N protocol events (tool calls, text, results) plus the
             latest status report. Use to answer "what is this agent doing?"
params:
  agent:   string (required)
  tail:    int    (optional, default 20, max 200)
returns: {
  status:       AgentStatus,
  last_report:  { state, summary, detail, at },
  activity:     [ { kind, ts, summary, tool?, text? } ]
}
```

Semantics: reads from the harness **activity ring** (§4.4). No subprocess
traffic; this is a pure query against on-disk + in-memory harness state.

### 4.3 Harness-owned queue

Replace the `.wake` / `.poke` sentinel scheme with a structured **per-agent
queue** managed by the target agent's `agentloop`:

```
.sprawl/agents/{name}/queue/
  pending/     # append-only: {seq}-{class}-{id}.json
  delivered/   # moved here atomically after injection
```

Each queue entry:

```json
{
  "seq": 42,
  "id": "msg_01HX...",
  "class": "async" | "interrupt",
  "from": "weave",
  "subject": "...",
  "body": "...",
  "reply_to": null,
  "tags": [],
  "enqueued_at": "2026-04-21T08:00:00Z"
}
```

- **Writers** (`send_async`, `send_interrupt`, and a shim for
  CLI `sprawl messages send`) append under `pending/` and fsync. An atomic
  rename via `pending/.tmp-{id}` → `pending/{seq}-{class}-{id}.json` gives
  crash-safe delivery.
- **Signal:** a lightweight `queue.notify` unix socket (or fallback: an
  `fsnotify` watch on `pending/`, or a `queue/.bump` mtime touch) wakes the
  target's agent loop without the 500 ms poll. Polling remains as backstop.
- **Reader:** the agent loop's new `flushQueue` step, described in §4.4.
- **Durability:** because entries persist until moved to `delivered/`, a
  subprocess crash mid-inject causes redelivery on next boot. Each entry
  carries an `id`; the loop deduplicates against `delivered/` on startup.

Rationale for a per-agent directory (vs. a single SQLite table):
- Matches the existing filesystem-primitive conventions (Maildir, tasks/).
- Trivially inspectable (`ls`, `cat`).
- No new runtime dependency.
- Atomic via rename on same filesystem.

### 4.4 Harness-owned activity ring

Each agent's harness records a rolling window of recent protocol events:

```
internal/agentloop/activity.go

type ActivityEntry struct {
    TS      time.Time
    Kind    string   // "tool_use" | "tool_result" | "assistant_text" | "user_text" | "result"
    Summary string   // ≤120 chars
    Tool    string   // present for tool_use/tool_result
    Raw     []byte   // optional, gated by verbose flag
}

type ActivityRing struct { ... }  // bounded, ~200 entries, thread-safe
```

Populated from the same place `tmuxObserver.OnMessage` sits today
(`agentloop.go:173`). Exposed to the supervisor via a new method:

```
Supervisor.PeekActivity(name string, tail int) ([]ActivityEntry, error)
```

which reads the harness-local ring if the agent is in-process, or
falls back to a per-agent append-only `activity.ndjson` file (tailed) when
the query crosses process boundaries (e.g. TUI in one process, agent
subprocess in another).

Emit an event on each update for subscribers (TUI panel, `sprawl status
--watch`).

### 4.5 Injection mechanics

#### 4.5.1 Async flush (between turns)

After `proc.SendPrompt` returns a `result`:

1. Acquire work lock (existing flock).
2. Atomically move all `queue/pending/*async*.json` into `queue/delivered/`.
3. If non-empty, format a single **notification frame** bundling all async
   messages:

   ```
   [inbox] You received N messages since the last turn:

   1. from weave  [status]  subject here
      <body, possibly truncated with "run `sprawl messages read <id>` for full body">

   2. ...

   You are currently executing: <last task/prompt summary>.
   Continue unless a message tells you otherwise.
   ```

4. Send as a user turn via `Session.SendUserMessage`. The agent responds,
   cycle repeats.

This replaces today's combination of `.wake` detection + inbox-listing step.

#### 4.5.2 Interrupt flush (mid-turn)

When an `interrupt` entry lands in `pending/`:

1. The harness wakes (socket signal or fsnotify).
2. It records the current in-flight prompt/task id so it can tell the agent
   what it was doing.
3. It calls `Session.Interrupt(ctx)`. The SDK emits a `result` with an
   interrupt stop reason; `sendPromptWithInterrupt` returns.
4. The harness moves the interrupt entry to `delivered/` and formats:

   ```
   [interrupt] {from} has injected an important message. You were in the
   middle of: <resume_hint or last-activity summary>.

   Subject: {subject}

   {body}

   After reading, decide whether to:
   - resume the interrupted work (default), OR
   - stop / change direction if the message says so.
   ```

5. Sends via `SendUserMessage`. When the resulting turn completes, the agent
   loop's next iteration picks up the next queued task / message as usual.

#### 4.5.3 "Does the child lose its place?"

Short answer: no more than it already does. The Claude Code session is
preserved — `Interrupt` ends the turn but not the session; conversation
history is intact. The interrupt frame explicitly names what was in flight
(via `resume_hint` or the harness's recorded last prompt summary), so the
model has textual context to resume against.

Open question: should the harness persist the *user-visible prompt* of the
interrupted turn so it can re-inject it verbatim after the interrupt turn
finishes? That would let us offer truly transparent interrupt-and-resume.
See §8 Open Questions.

### 4.6 Sub-agent visibility

Three surfaces built on §4.4 and §4.2.4:

1. **TUI "Activity" panel.** When an agent is selected in the tree, a new
   right-hand pane renders the last N `ActivityEntry`s (coloring tool calls
   vs. text, truncating tool args). Updates on `activity-tick` events.
2. **`sprawl status --watch`** and **`sprawl status <agent>`** — the CLI
   grows a `--watch` flag that tails the activity ring, and a positional
   argument that dumps the last 50 entries + `last_report` for one agent.
3. **`peek` MCP tool** — parent agents can query children
   programmatically ("what's qa doing right now?").

### 4.7 Status reporting

`report_status` becomes the canonical channel. The existing `sprawl
report` CLI remains (for bash-scripted agents and tests) but is reimplemented
on top of the same supervisor method, so there is exactly one persistence
path.

The TUI status bar grows a per-agent chip: a colored dot (green working,
yellow blocked, red failure, grey idle) + the ≤160-char `summary` from
`last_report`. This is the `coder_report_task`-equivalent surface.

### 4.8 System prompt updates

Agent system prompts need a short section teaching the model to prefer:

- `send_async` over `sprawl messages send` when MCP is available;
- `send_interrupt` — **rare** — only for genuinely urgent parent-side
  corrections;
- `report_status` at each meaningful step (sub-agents reporting to
  their parent), not just at task end;
- `peek` before asking a child "are you done?" — peek first, then
  send-async only if peek is inconclusive.

---

## 5. Architecture diagrams

### 5.1 Async path

```
  weave (parent)                      ghost (child)
  ┌───────────────┐                   ┌───────────────────────────┐
  │ Claude        │                   │ Claude                    │
  │  └─ sprawl_   │  MCP call         │                           │
  │     send_     │──────────────┐    │                           │
  │     async     │              │    │                           │
  └───────┬───────┘              ▼    │                           │
          │         ┌─────────────────────────────┐               │
          │         │ sprawl MCP (in weave    │               │
          │         │ harness process)             │               │
          │         │  1. persist Maildir          │               │
          │         │  2. append queue/pending/…   │               │
          │         │  3. notify ghost harness     │               │
          │         └──────────────┬──────────────┘               │
          │                        │ socket/fsnotify              │
          │                        ▼                              │
          │          ┌─────────────────────────────┐              │
          │          │ ghost agentloop (wakes)     │              │
          │          │  waits for current turn to  │              │
          │          │  complete (result)          │              │
          │          │  → SendUserMessage(frame)   │──────────────┘
          │          └─────────────────────────────┘
```

### 5.2 Interrupt path

```
  weave (parent)                      ghost (child, mid-turn)
  send_interrupt ─────▶  MCP server enqueues
                                     ├─ persist
                                     ├─ queue/pending/ {class:interrupt}
                                     └─ ghost harness signal

                                ghost agentloop:
                                     ├─ Session.Interrupt(ctx)        ← stops current turn
                                     ├─ move entry to delivered/
                                     └─ SendUserMessage(interrupt frame)
                                         (agent reads, then resumes)
```

---

## 6. Decomposition into sub-issues

Each is a reasonable standalone PR. Ordering reflects dependencies.

1. **Harness queue data structures.** `.sprawl/agents/{name}/queue/` layout;
   writer/reader helpers in `internal/agentloop/queue.go`; migration from
   `.wake`/`.poke`. No behavior change yet (loop still uses old sentinels).
2. **Activity ring.** `internal/agentloop/activity.go`; hook into
   `tmuxObserver.OnMessage`; persistence to `activity.ndjson`; supervisor
   method `PeekActivity`.
3. **MCP tools: `send_async` + `peek`.** Wire into existing
   `sprawl` server. `send_async` writes to the new queue;
   `peek` reads the activity ring. Keep `message` working as
   an alias.
4. **Agent loop: flush from new queue, retire `.wake`/inbox-step.** Replace
   the inbox-delivery phase with a `flushQueue` that drains `queue/pending/`
   between turns. Existing Maildir messages still work (queue carries
   pointers to them).
5. **MCP tool: `send_interrupt` + `Session.Interrupt` wiring.**
   Implement the interrupt-inject dance in §4.5.2. Add a test that exercises
   a full mid-turn interrupt against a stub transport.
6. **MCP tool: `report_status`; TUI status chip.** Move `cmd/report`
   onto the supervisor method; add the status chip rendering in
   `internal/tui`.
7. **TUI activity panel.** New pane on the right; subscribes to activity
   events; renders with color/truncation rules.
8. **CLI: `sprawl status --watch` and `sprawl status <agent>`.** Consume
   `PeekActivity` via supervisor.
9. **Deprecate tmux send-keys notification paths.** Remove the send-keys
   calls in `cmd/messages.go:199` and `cmd/report.go:157`. Leave the
   `tmux.SendKeys` primitive (still used for session wiring), just stop
   using it for notifications.
10. **System prompt updates + skill docs.** Teach agents the new surface.
11. **Migration shim removal.** After two release cycles, drop the
    `message` alias and the `.wake`/`.poke` sentinel reader.

---

## 7. Migration plan

Phased; no flag-day rewrite.

**Phase 0 (prep).** Land items 1–2 above. No agent-visible change; purely
infrastructure. All existing code paths still work.

**Phase 1 (dual-delivery).** Land items 3–4. `send_async` writes to
**both** the new queue and, for compatibility, the old `.wake` sentinel. The
agent loop's `flushQueue` supersedes the old inbox step, but the loop still
honors `.wake` as a wake source. `message` is kept as an alias. No
behavior regression: agents that still use `sprawl messages send` or
`message` continue to work.

**Phase 2 (interrupt + reporting).** Land items 5–6. The interrupt tool is
opt-in via system-prompt guidance — existing agents won't start using it
spontaneously.

**Phase 3 (visibility).** Land items 7–8. Pure additions; no removal.

**Phase 4 (deprecation).** Land item 9 — remove the tmux send-keys
notification calls. At this point the MCP path is primary. Keep the
`.wake`/`.poke` backstop for one release cycle to handle edge cases (agents
started via an older `sprawl` binary against a newer state dir).

**Phase 5 (cleanup).** Land items 10–11. Drop the alias, the sentinel
backstop, and the old inbox-delivery step.

**Rollback strategy.** At each phase, setting an env var
`SPRAWL_MESSAGING=legacy` falls back to the current tmux-send-keys path.
The harness always maintains the legacy code path until phase 5.

---

## 8. Open questions & trade-offs

### 8.1 Transparent interrupt-and-resume

Claude's session survives `Interrupt`, but the *turn* it was running is
discarded. We can:

- **(a) Rely on conversation context.** The interrupt frame names the
  in-flight task; the model resumes from context. Cheapest; depends on
  model behavior.
- **(b) Persist the originating user turn and re-send it.** After the
  interrupt-reply turn completes, the harness automatically re-injects
  the original user turn. Cleanest semantically but risks double-execution
  of side effects the model has already started.
- **(c) Offer a `resume_mode` parameter** on `send_interrupt`:
  `"soft"` (a) vs. `"hard"` (b). Punt the choice to the sender.

Recommendation: start with (a); add (c) if field experience shows model
drift.

**Status (QUM-294 shipped):** Implementation went with option (a) —
context-only resume. The §4.5.2 interrupt frame names the in-flight work via
the sender-supplied `resume_hint` (falling back to a generic "your previous
task" when omitted) and relies on Claude's preserved conversation history to
resume. Option (c) is deferred to a follow-up issue if empirical drift
shows up in practice. The `resume_hint` parameter is already plumbed through
`send_interrupt` → `SendInterrupt` → queue entry tag (`resume_hint:…`)
→ frame template, so upgrading to (c) later is purely additive.

### 8.2 Queue signalling

- **Unix domain socket per agent:** low-latency (~µs), requires socket
  lifecycle management, and doesn't survive agent-process restarts
  cleanly.
- **fsnotify on `queue/pending/`:** zero setup, cross-platform caveats
  (Linux fine, macOS via kqueue is fine too), slight startup cost.
- **Mtime-bump on `queue/.bump` + 100 ms poll:** simplest; matches
  existing 500 ms poll pattern but faster.

Recommendation: fsnotify with 1 s polling fallback. Unix sockets are a
premature optimization.

### 8.3 Message ordering across classes

If async #1, interrupt #2, async #3 all arrive in sequence, current
design has interrupt #2 preempt async #1 (which flushes on next yield
*after* the interrupt-reply turn). Arguably correct — interrupts are
time-sensitive — but worth documenting as the contract.

### 8.4 Cross-agent observability of protocol payloads

The activity ring could record full tool arguments and text, which is
great for debugging but may leak secrets (API keys in tool args, user
PII in prompts). Propose: summary-only by default, `SPRAWL_ACTIVITY_VERBOSE=1`
records `Raw` bytes too. Redact a small denylist (`Authorization`,
`ANTHROPIC_API_KEY`, etc.) in summary generation.

### 8.5 Should `send_interrupt` be available to child agents?

"Children interrupting their parent weave" could be useful ("I found
something urgent, look now") but is also a recipe for user-experience
disaster when three children all interrupt weave mid-conversation. Options:

- Restrict `send_interrupt` to parent → descendants by default;
  gate peer/child-to-parent on an explicit `--allow-upstream-interrupt`
  config.
- Or: allow but rate-limit (at most 1 upstream interrupt / 30 s per agent).

Recommendation: start restricted (parent→descendants only). Revisit after
usage.

### 8.6 Back-pressure

If a child generates 100 async messages before the parent's next yield,
the parent gets one giant notification frame. Mitigation: the frame
summarizes ("3 status updates, 1 question — run `sprawl messages read`
for details") and links to IDs. Avoid inlining every body above a size
threshold (say 2 KB each, 10 KB total).

### 8.7 Tests

Harness-level tests need a **stub transport** that lets us script Claude
event sequences (including `result` emission and mid-turn interrupt). The
existing `internal/host` tests already have scaffolding for this; extend
it to simulate `sendPromptWithInterrupt` scenarios.

---

## 9. Reflections

**Surprising / unexpected:**
- The Claude Code SDK already exposes first-class `Interrupt` and can
  accept `user` messages at any time — we don't need new transport
  plumbing, only harness-side queue management. The "interrupt" feature
  is mostly about *coordination* (when to fire it, what to inject), not
  new I/O.
- `cmd/messages.go` and `cmd/report.go` are the **only two** callers of
  `tmux.SendKeys` for notification purposes. The blast radius of ripping
  out send-keys notifications is tiny; the headline pain is all model /
  UX, not refactor.
- Sub-agent visibility is almost free once an activity ring exists — the
  `tmuxObserver.OnMessage` hook is already being called on every
  protocol event; today it just discards them.

**Open questions still unresolved:**
- Whether Claude-on-interrupt reliably resumes in-flight tasks from
  context alone (§8.1). This is an empirical question we can only answer
  by shipping Phase 2 behind a flag and measuring.
- Whether fsnotify on busy state directories is stable enough on macOS
  under heavy CI load. The 1 s poll fallback is cheap insurance.
- Exactly how the TUI should render an activity tail without overwhelming
  the operator when many agents are alive (§4.6 item 1). Needs a
  mini-mock before we build.

**What I would investigate next with more time:**
- Read the Claude Code SDK changelogs (>=2.x) for any new "permission"
  or "hooks" surface that might offer a cleaner interrupt primitive than
  `control_request:interrupt`.
- Prototype the queue + activity ring without touching messaging to
  de-risk items 1–2 before committing to the full design.
- Survey how other multi-agent harnesses (AutoGen, CrewAI, Let's-go)
  handle mid-turn interrupts — we may be reinventing a pattern.

---

## 10. References

- `docs/todo/punchlist.md` items #1 and #2
- `internal/supervisor/{supervisor,real}.go`
- `cmd/{agentloop,rootloop,messages,report,poke,delegate}.go`
- `internal/host/{session,router,transport,mcp_bridge}.go`
- `internal/sprawlmcp/{server,tools}.go`
- `internal/messages/messages.go` (Maildir layout)
- `internal/state/{state,tasks}.go`
- `internal/tmux/tmux.go:287` (send-keys primitive)
- Research briefing: `.sprawl/agents/ghost/findings/messaging-current-state.md` (to be committed alongside this doc)
