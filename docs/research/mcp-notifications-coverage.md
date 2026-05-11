# MCP server→client notifications coverage in Claude Code 2.1.x

**Status:** Research spike (QUM-526)
**Author:** ghost
**Date:** 2026-05-11
**Client tested:** Claude Code `2.1.126` (stdio MCP transport)
**Related:** `docs/research/mcp-hang-observability-design.md` §3 (QUM-498 — `notifications/progress` baseline)

## TL;DR

**Every server-initiated MCP notification method I tested is silently dropped
by Claude Code 2.1.126.** The `notifications/progress` verdict from QUM-498
generalizes: the dispatcher does not deliver *any* of the spec's
server→client notification methods to the model, to the debug log, or to
client-side state (no follow-up `tools/list` / `resources/list` /
`prompts/list` / `resources/read` requests are issued in response).

This means sprawl cannot route MCP-call observability through any of the
protocol-defined notification channels. The only viable path remains
**Angle C** from `mcp-hang-observability-design.md` — in-process TUI
surfacing via the `tea.Msg` bridge.

## Per-method verdict

The MCP spec (2025-06-18 base + 2025-11-25 negotiated by the client) defines
six server→client notification methods. I tested all six in a single tool
call.

| Method | Surfaced to model? | Visible in `--debug-to-stderr`? | Client side-effect on server? | Verdict |
|---|---|---|---|---|
| `notifications/progress` | ❌ | ❌ | ❌ (no UI, no log) | **Silently dropped** |
| `notifications/message` (logging) | ❌ | ❌ | ❌ (no log line) | **Silently dropped** |
| `notifications/resources/updated` | ❌ | ❌ | ❌ (no `resources/read` follow-up) | **Silently dropped** |
| `notifications/resources/list_changed` | ❌ | ❌ | ❌ (no `resources/list` re-fetch) | **Silently dropped** |
| `notifications/tools/list_changed` | ❌ | ❌ | ❌ (no `tools/list` re-fetch) | **Silently dropped** |
| `notifications/prompts/list_changed` | ❌ | ❌ | ❌ (no `prompts/list` re-fetch) | **Silently dropped** |

"Side-effect on server" means: did the client issue any follow-up JSON-RPC
request that we would expect a conforming dispatcher to send in reaction to
the notification? For the `*_list_changed` family the spec implies a refresh
fetch; for `resources/updated` it implies a `resources/read` (when
subscribed). None of these follow-ups appear in the server log during the
13 s the client kept the stdio connection open after `_fire_all` returned.

## Method

A standalone Python stdio MCP server
(`.sprawl/agents/ghost/findings/qum-526/probe_server.py`) advertises every
notification-producing capability:

```json
{
  "tools": {"listChanged": true},
  "resources": {"subscribe": true, "listChanged": true},
  "prompts": {"listChanged": true},
  "logging": {}
}
```

It exposes one tool, `_fire_all`, which emits one well-formed JSON-RPC
notification of each method (echoing the caller's `_meta.progressToken` for
`notifications/progress`), spaced ~50 ms apart, then returns a plain text
result. The server logs every inbound and outbound frame with a UTC
timestamp to `/tmp/qum526-server.log`.

The probe was attached to a headless `claude -p` session via:

```bash
claude -p \
  --mcp-config qum-526/mcp-config.json \
  --strict-mcp-config \
  --permission-mode bypassPermissions \
  --debug-to-stderr \
  --output-format stream-json --include-partial-messages --verbose \
  '<instruction to call _fire_all once and report observed side-effects>'
```

This is the same general harness shape as QUM-498's `progress` spike, just
expanded to cover the full notification surface.

### Evidence — server log (trimmed to the relevant window)

```
16:28:49.345  IN   tools/call _fire_all  (_meta.progressToken=4)
16:28:49.345  OUT  notifications/progress              {progressToken:4, progress:1, total:1}
16:28:49.396  OUT  notifications/message               {level:"info", logger:"qum526-probe"}
16:28:49.446  OUT  notifications/resources/updated     {uri:"probe://state"}
16:28:49.497  OUT  notifications/resources/list_changed
16:28:49.547  OUT  notifications/tools/list_changed
16:28:49.597  OUT  notifications/prompts/list_changed
16:28:49.647  OUT  tools/call result
# 13 seconds of idle stdio. Zero follow-up requests from the client.
16:29:02      ―    SIGINT from client; clean shutdown.
```

Full log: `.sprawl/agents/ghost/findings/qum-526/qum526-server.log`.

### Evidence — Claude Code debug log

Filtering `claude --debug-to-stderr` output for the probe server:

```
16:28:44.028  MCP server "qum526_probe": Starting connection with timeout 30000ms
16:28:44.097  MCP server "qum526_probe": Successfully connected (transport: stdio) in 73ms
16:28:44.097  MCP server "qum526_probe": Connection established with capabilities:
              {"hasTools":true,"hasPrompts":true,"hasResources":true,
               "hasResourceSubscribe":true,"serverVersion":{...}}
16:28:44.097  [MCP] Server "qum526_probe" connected with subscribe=true
16:28:49.343  [Stall] tool_dispatch_start tool=mcp__qum526_probe___fire_all
16:28:49.344  MCP server "qum526_probe": Calling MCP tool: _fire_all
16:28:49.649  MCP server "qum526_probe": Tool '_fire_all' completed successfully in 305ms
16:28:49.650  [Stall] tool_dispatch_end ... outcome=ok durationMs=307
```

Between `Calling MCP tool` and `completed successfully` there are **no**
log entries mentioning `notifications/`, `progress`, `message`,
`list_changed`, `resources/updated`, or any logger name supplied by the
probe. A full-file grep across `--debug-to-stderr` confirms no notification
method name from the spec is logged at any verbosity.

### Evidence — the model's own report

The model received only the final tool-result text. Verbatim (truncated):

> "I literally saw only one thing in this client surface: the tool's final
> result text … Beyond that single string, I did not observe any of the
> server-initiated notifications surfaced to me as separate, distinguishable
> events. … None of the out-of-band MCP notifications were exposed to me as
> separate observable [events]."

### Nuance: the client *does* request progress

Claude Code attaches `_meta.progressToken` to outbound `tools/call`
requests (`progressToken: 4` this run). The wire-level intent is present;
the surfacing is not. This matches QUM-498's observation and rules out
"workaround by not asking for progress."

### Nuance: capability advertisement is partial

The connection-established log records `hasTools / hasPrompts / hasResources
/ hasResourceSubscribe` booleans but does **not** mention
`tools.listChanged`, `resources.listChanged`, or `prompts.listChanged`.
Whether the client even tracks `listChanged` capability internally is
unclear from the public surface, but the empirical result is the same:
list-changed notifications produce no reaction.

### Nuance: stdio only

This test only covers the stdio transport (which is what sprawl uses and
what `claude mcp add` configures by default). I did not test SSE or HTTP
transports. If those exist and behave differently, that's out of scope for
this spike — sprawl's MCP server is stdio.

## What "supported" in Claude Code's docs means (or doesn't)

Claude Code's user-facing MCP docs reference `tools/list_changed` as if it
were honored. The empirical evidence here says no: in 2.1.126, sending
`tools/list_changed` does not trigger a `tools/list` refresh, does not
update the model's tool roster mid-turn, and is not logged at any
verbosity. Either the docs are aspirational, the feature was removed in
a recent client, or it only works through a non-stdio code path I can't
exercise from a sprawl MCP server.

For sprawl's purposes the practical answer is: **assume nothing
server-initiated reaches the agent.**

## Implications for sprawl observability

Three things this nails down:

1. **QUM-497 Angle B remains dead.** There is no protocol-level path —
   not `progress`, not `message`, not any `list_changed` — that gets
   server-initiated state into the calling Claude agent's context. The
   recommendation in `mcp-hang-observability-design.md` to skip Angle B
   stands and is now backed by full notification-surface coverage rather
   than just the `progress` data point.

2. **Angle C (TUI surfacing) is the only viable path** for in-flight MCP
   visibility, which is exactly what finn shipped in QUM-497
   (status-bar + 60 s viewport banner). No further protocol-routing
   experiments are warranted until Anthropic ships a `notifications/*`
   dispatcher.

3. **Repurposing notification channels is off the table.** I considered
   whether sprawl could (ab)use, say, `notifications/message` (logging) to
   smuggle in-flight ops info to the host. It cannot — the host's Claude
   Code dispatcher will silently drop it. We'd be writing a message into
   the void. The only existing "push to agent" mechanism that demonstrably
   works is `send_async` into the maildir, which we already have.

## What I'd test next (if forced to keep digging)

- **Other clients.** Does VS Code's Copilot agent / Continue / Cursor /
  the official `mcp-cli` actually deliver these notifications? If even one
  consumer surfaces `tools/list_changed`, our verdict here is
  Claude-Code-specific rather than ecosystem-wide. That's relevant if
  sprawl ever exposes its MCP server to non-Claude consumers.
- **Client-initiated `roots/list_changed`.** I did not test the inverse
  (client→server) notifications. The client advertised
  `capabilities.roots: {}` but I never asked it to enumerate roots and
  then signal `roots/list_changed`. That's outside the QUM-526 scope
  (server→client only) but worth knowing.
- **Watch claude-code#4157.** When/if that issue reopens, re-run this
  exact harness. Five minutes of work to re-verify whenever a new Claude
  Code minor is cut.

## Reflection — open questions and surprises

What surprised me: I genuinely expected at least `tools/list_changed` to
trigger a re-fetch, because the Claude Code docs reference it and because
some adjacent MCP clients (per spec hearsay) do honor it. The dispatcher
appears to be uniformly silent — not "drops progress specifically" but
"drops everything." That's actually cleaner intel than a partial-coverage
result would have been: the right design assumption is "no
server-initiated notification reaches Claude Code, ever, in 2.1.x."

Open questions I'm flagging but not chasing in this spike:

- Whether this is a bug or a deliberate design decision in Claude Code's
  MCP client. The fact that the client still *requests* progress tokens
  while dropping the responses suggests partial implementation rather than
  a "we won't support this" stance, but that's inference, not evidence.
- Whether `notifications/message` is dropped before or after Claude Code's
  internal logger pipeline. The `--debug-to-stderr` output gives us zero
  signal, but the client could in principle be writing them to a separate
  file. (I didn't enumerate every log path; `--debug-to-stderr` is the
  documented one.)
- Whether the `2024-11-05` → `2025-11-25` protocol-version negotiation
  affects notification dispatch differently from request dispatch. I
  re-tested at `2025-06-18` server-side this time (vs QUM-498's
  `2024-11-05`) and got identical behavior, so probably not — but I
  haven't varied it systematically.

## Artifacts

`.sprawl/agents/ghost/findings/qum-526/`:

- `probe_server.py` — the stdio MCP probe
- `mcp-config.json` — claude `--mcp-config` payload
- `qum526-server.log` — every JSON-RPC frame in/out, timestamped UTC
- `qum526-claude-debug.log` — `--debug-to-stderr` capture
- `qum526-claude-stream.jsonl` — full `--output-format stream-json` trace
