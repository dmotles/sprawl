# MCP Hang Observability — Design Proposal

**Status:** Research / proposal
**Author:** ghost (research agent, weave-led session)
**Date:** 2026-05-06
**Trigger:** weave invoked `mcp__sprawl__retire {agent:"finn", merge:true}` after finn finished QUM-488. The MCP call hung for ~30 minutes with zero progress feedback. The user had to Ctrl+C the entire `sprawl enter` host. Re-running with `validate:false` after restart succeeded immediately. `make validate` runs in <90s in isolation, on main and in finn's worktree.

This document covers three independent angles:

- **A. Telemetry / post-mortem** — make future hangs reconstructable.
- **B. MCP streaming** — push progress notifications to the calling agent.
- **C. TUI surfacing** — show progress in the host TUI directly.

It also records what we know — and don't know — about the specific 30-min hang.

---

## 1. The hang: what the code path looks like

`mcp__sprawl__retire {merge:true, validate:true}` enters this code path:

1. `internal/sprawlmcp/server.go:toolRetire` — JSON-RPC handler. Decodes args, resolves caller identity from request context, calls `s.sup.Retire(ctx, …)`.
2. `internal/supervisor/real.go:Retire` — picks `mergeFn = agentops.Retire` and `retireDeps`, then synchronously calls it on the goroutine handling the JSON-RPC request.
3. `internal/agentops/retire.go:Retire` — when `mergeFirst==true`:
   - Validates pre-conditions
   - Builds `merge.Config` with `ValidateCmd` from `.sprawl/config.yaml` (currently `make validate`)
   - Calls `deps.DoMerge` = `merge.Merge`
4. `internal/merge/merge.go:Merge` —
   1. Acquires an exclusive flock on `.sprawl/locks/<agent>.lock`
   2. Computes merge base; squash-resets agent worktree; commits
   3. Rebases agent branch onto parent
   4. Fast-forward merges into parent worktree
   5. **Runs validate**: `deps.RunTests(parentWorktree, validateCmd)` → `merge.RealRunTests` → `exec.Command("bash","-c", command).CombinedOutput()`
   6. Writes the recipient poke; releases lock

The blocking call inside the supervisor goroutine is **`exec.Cmd.CombinedOutput()`** in `RealRunTests`. There is no context, no timeout, no progress sink, no cancellation, no streaming.

### Observed log evidence

Stripped tower stderr from the hang session (`tui-stderr-20260506-153820.log`) shows lots of successful merges that day, but the only line concerning finn's QUM-488 retire is *missing entirely* — i.e., the hang fired before any of merge.go's success messages were written. Restart logs (`184811.log`, `190039.log`) confirm the post-restart `validate:false` retire succeeded:

```
Merged "finn" into main (3831c7e)
Merged and retired "finn", deleted branch dmotles/qum-488-delegate-task-queue-bridge
```

The merge itself completed instantly *after* the restart, which is consistent with the squash/rebase/ff-merge already being safe — i.e., the hang is in step 4.5 (validate) or in something that holds the flock from step 4.1.

### Root-cause hypotheses (ranked)

| # | Hypothesis | Plausibility | Why |
|---|---|---|---|
| 1 | `cmd.CombinedOutput()` hung waiting for the stdout/stderr pipe to close because some grandchild of `make validate` inherited fd 1/2 and never exited | **High** | Classic Go gotcha. The repo already has `*_stdio_leak_test.go` files in `internal/agentops/` and `internal/merge/`, indicating prior incidents. `RealRunTests` uses `cmd.CombinedOutput()` with default `Stdin=nil`, default `ExtraFiles=nil` — if a `go test` binary in the repo backgrounds a tmux/sandbox subprocess without redirecting fd 2, CombinedOutput never returns. |
| 2 | `make validate` deadlocks on a shared resource the running host holds (e.g., a tmux socket, the `.sprawl/locks/<agent>.lock` flock, or a build cache) | **Medium** | The host *is* using the same repo's `.sprawl/`. The merge holds `<finn>.lock` already. If a test in `make test` tries to flock the same path it would deadlock with the supervisor goroutine that's awaiting validate. Worth grepping. |
| 3 | `make validate` hits a `go test` subtest that itself tries to spawn an agent or talk to the live tower's tmux socket / MCP socket | **Medium** | The TUI E2E gates (test-notify-tui-e2e, test-handoff-e2e) require a real `claude` binary and a real tmux socket. Inside a running host, those would either find an in-use socket or block on namespace conflicts. Mitigated only by `SPRAWL_E2E_SKIP_NO_CLAUDE`. |
| 4 | Validate succeeded but the merge goroutine wedged trying to write the poke file or release flock | **Low** | Both are local file ops. Flock release is non-blocking. |
| 5 | The MCP request context was cancelled (e.g., by Claude Code's tool-call timeout) but the supervisor goroutine kept running because `RealRunTests` doesn't honor ctx | **Low for hang, High for "no error returned"** | This wouldn't cause the *user-visible* hang in itself (the goroutine just keeps running) but it explains the "no error, no result" experience. |

**Verdict:** Without runtime repro and a stack dump, we cannot pin the specific cause. Hypothesis 1 (stdio leak) is the highest-EV bet; hypothesis 2 (lock contention with the live host) is the next. Both are *recurrence-likely* and both are addressed by Angle A's diagnostics. **We should not block telemetry work on root-cause.** First time it happens with telemetry in place, we will know.

A separate bug ticket should track the runtime repro: try `make validate` invoked from inside a running host via the MCP retire path, with goroutine and FD snapshots taken at T+60s.

---

## 2. Angle A — Telemetry / post-mortem

**Goal:** Next time an MCP tool hangs, an agent (or a human) can answer "what was it doing, and where was it stuck?" from on-disk evidence alone, even after the host is killed.

### Current state — observability gap survey

Grep of `internal/sprawlmcp/`:

| Tool | per-call ID | start log | end log | progress | survives kill? |
|---|---|---|---|---|---|
| spawn | ❌ | ❌ | ❌ | ❌ | partial — agent state file is updated |
| status | ❌ | ❌ | ❌ | ❌ | n/a (read-only) |
| delegate | ❌ | ❌ | ❌ | ❌ | task queue file persists |
| send_async | ❌ | ❌ | ❌ | ❌ | maildir write persists |
| send_interrupt | ❌ | ❌ | ❌ | ❌ | maildir + injection |
| peek | ❌ | ❌ | ❌ | ❌ | n/a |
| report_status | ❌ | ❌ | ❌ | ❌ | state file updated |
| message (deprecated) | ❌ | ❌ | ❌ | ❌ | maildir |
| **merge** | ❌ | ❌ | ❌ | ❌ | **NO** — only post-success stderr line |
| **retire** | ❌ | ❌ | ❌ | ❌ | "retiring" checkpoint in state file (good!) |
| kill | ❌ | ❌ | ❌ | ❌ | state file updated |
| handoff | ❌ | ❌ | ❌ | ❌ | session-summary file |
| messages_list / read / archive / peek | ❌ | ❌ | ❌ | ❌ | n/a |

Only `retire` has any crash-safe checkpoint (the `Status="retiring"` on the agent state file), and even that doesn't tell us *which step* of retire it's stuck in.

There are no per-call IDs anywhere. There is no "currently in flight" registry. The supervisor's stderr is captured to `tui-stderr-<ts>.log`, but those lines are buffered behind the TUI's alt-screen renderer and are dominated by spinner reprints — they're nearly unreadable without `sed`/`grep`. (See `tui-stderr-20260506-153820.log`: 50 file-lines that expand to 88KB of escape sequences.)

### Proposed instrumentation

**A1. Per-call ID + structured call log.** Generate a UUID per `tools/call`. Write JSONL lines to `.sprawl/logs/mcp-calls.jsonl`:

```jsonl
{"ts":"2026-05-06T18:11:42Z","call_id":"01H...","phase":"start","tool":"retire","caller":"weave","args":{"agent_name":"finn","merge":true,"validate":true}}
{"ts":"2026-05-06T18:11:42Z","call_id":"01H...","phase":"checkpoint","tool":"retire","step":"merge.lock-acquired"}
{"ts":"2026-05-06T18:11:43Z","call_id":"01H...","phase":"checkpoint","tool":"retire","step":"merge.squash-committed","commit":"abc1234"}
{"ts":"2026-05-06T18:11:43Z","call_id":"01H...","phase":"checkpoint","tool":"retire","step":"merge.validate-started","cmd":"make validate"}
# (call hangs here)
{"ts":"2026-05-06T18:51:43Z","call_id":"01H...","phase":"end","status":"ctrl-c","duration_s":2401}  # never written if process is killed
```

`fsync` after every line. After a host kill, the *last line in the file* is the last thing the call did before wedging. **This is the single highest-leverage change.**

Implementation cost: ~150 LOC + tests. One new package `internal/sprawlmcp/calllog` with a `Logger` struct injected via deps. Each tool handler emits `start`, optional `checkpoint`s, and `end`. For the merge/retire hot path, instrument every step in `merge.Merge` and `retire.Retire`.

**A2. In-flight registry / heartbeat file.** Maintain `.sprawl/runtime/in-flight.json` listing currently-running MCP calls. Each entry: `{call_id, tool, caller, started_at, last_heartbeat, current_step}`. Updated atomically on every checkpoint. After a host kill, this file shows what was wedged at the moment of death.

A periodic goroutine (1s tick) updates `last_heartbeat` for the top of every active call's stack so we can distinguish "stuck waiting for user input" from "stuck inside a subprocess".

Implementation cost: ~200 LOC + tests.

**A3. SIGUSR1 → goroutine + FD dump.** Wire a signal handler in `cmd/enter.go` (or wherever the supervisor lives) that, on SIGUSR1, writes:
- `runtime.Stack(buf, true)` → `.sprawl/runtime/goroutines-<ts>.txt`
- contents of `/proc/self/fd` (symlink targets) → `.sprawl/runtime/fds-<ts>.txt`

This lets a user, *before* Ctrl-C-ing the host, send `pkill -USR1 sprawl` and capture the live state.

Implementation cost: ~80 LOC + a manual test. Trivially testable in a sandbox.

**A4. pprof on a unix socket (off-by-default).** Bind `pprof.Index` to `.sprawl/runtime/pprof.sock` when `SPRAWL_PPROF=1`. Same evidence as A3 plus heap, mutex, block profiles. More expensive to wire and test; use only if A3 proves insufficient.

**A5. Subprocess instrumentation.** Replace `RealRunTests`'s `cmd.CombinedOutput()` with:
- Streaming stdout/stderr capture (`cmd.Stdout = io.MultiWriter(buf, lineLogger)`)
- A wrapper that periodically logs "validate still running, T+60s, last line: …" to the call log
- Optional context-driven cancellation: derive the subprocess ctx from the MCP request ctx with a configurable timeout (e.g., 10 min default, overridable via `.sprawl/config.yaml:validate_timeout`)

When validate finishes (or times out), we have not just the exit status but a per-line tail in the call log.

Implementation cost: ~150 LOC + tests, mostly already-paved by `helpers_stdio_leak_test.go`.

**Tradeoffs.**

| Item | Cost | Information gain | Survives host kill? |
|---|---|---|---|
| A1 per-call JSONL | low | huge | ✅ |
| A2 in-flight registry | low–med | high | ✅ |
| A3 SIGUSR1 dump | low | huge | only if user sends SIGUSR1 first |
| A4 pprof socket | med | medium | needs live host |
| A5 subprocess wrapper | low–med | high | partial (line tail in call log) |

**Recommended A-bundle to ship together:** A1 + A2 + A3 + A5. Ship A4 only if a future hang stumps us.

---

## 3. Angle B — MCP progress notifications

**Goal:** Push progress messages from the sprawl MCP server to the calling agent (the weave Claude session) during long ops, so the agent sees `notifications/progress` events rather than a dead air gap.

### Spec recap

The MCP spec (basic/utilities/progress) lets a client opt into progress for a tool call by including `_meta.progressToken` in the `tools/call` params. The server then sends `notifications/progress` JSON-RPC messages with `{progressToken, progress, total?, message?}`. The client surfaces them however it likes.

### Claude Code client support — current evidence

[claude-code#4157](https://github.com/anthropics/claude-code/issues/4157) (closed, "not planned"):

> "I tried to emulate this be sending progress notifications as defined in [the MCP spec], but it does not show anything in the client. What do I need to do to make this work?"
>
> "It looks like it is sending notifications properly, but nothing is displayed in the client."

[claude-code#41733](https://github.com/anthropics/claude-code/issues/41733) is a related Telegram-plugin notification regression around March-April 2026 — distinct mechanism (`notifications/claude/channel`) but same general "the client drops server-initiated notifications" pattern.

**Verdict: Angle B is currently dead in Claude Code.** Server-sent `notifications/progress` are not surfaced to the agent's context, even when correctly formed. The issue was closed *not planned*, suggesting it's not on Anthropic's near roadmap.

### What would make B viable

1. Anthropic ships progress-notification surfacing in Claude Code. Not in our control. Watch the issue, but don't plan around it.
2. We run our own MCP client. Out of scope; we use Claude Code.
3. We emulate progress by *writing into the agent's mailbox* via `send_async` from inside the long-running tool. This is hack-y (it interleaves with real messages) but it does work today. **This is the closest "shippable B."**

### Proposed minimal experiment

If we want to *verify* the verdict, one engineer-hour: add a single test MCP tool `_test_progress` that emits 5 `notifications/progress` over 5s and observe whether the calling agent sees anything. If yes, viability changes; if no, file the issue link in our roadmap and move on. This is low-cost insurance.

### Recommendation

- **Do not invest** in protocol-level progress notifications.
- **Do** ship the "send_async heartbeat" pattern as a reusable helper — see C below — but route it through the TUI primarily, with the `send_async` route as an opt-in fallback for when the calling agent is not weave (e.g., a child agent calling `merge` on its descendant).

---

## 4. Angle C — TUI surfacing

**Goal:** When an MCP tool starts a long op, the host TUI renders a visible "operation in flight" indicator. Independent of B, fully under our control, deterministic.

### Where the surfacing lives

Sprawl's TUI (`internal/tui`) already has these surfaces:

- **StatusBar** (`statusbar.go`) — one-line bar at the bottom of the screen. Shows turn state, cost, agent count, session id, token usage, restart label. Updated via `Set*` methods on the model. Already supports a per-frame text mutation pattern (`SetRestartLabel(label string)`).
- **Banner** (`banner.go`) — multi-line block, mostly static for session header.
- **Activity stream** (`activity_stream.go`) — chronological feed of agent/protocol events.
- **Inbox banner / viewport banner** — already used for unread-message announcements (cf. QUM-311/312).

### Proposal: an "operations" line in the status bar

Add a `SetActiveOps([]OpDescriptor)` method to `StatusBarModel`. An `OpDescriptor` is `{tool, agent, started, message}`. When non-empty, the status bar renders e.g.:

```
[main]  3 agents · in: 1.2k · …  ⏳ retire(finn): make validate, T+47s
```

When two or more ops are in-flight, show a count and the oldest:

```
… ⏳ 2 ops · retire(finn): make validate T+47s
```

### How MCP tools push to the TUI

The MCP server runs in the same process as the TUI. The TUI's app model already receives messages via Bubble Tea's `tea.Msg` channel. The bridge already exists for `HandoffRequestedMsg`, `SessionRestartingMsg`, etc.

Add two new messages:

```go
type MCPCallStartedMsg struct {
    CallID  string
    Tool    string
    Caller  string
    Started time.Time
    Step    string // e.g., "merge.validate-started"
}
type MCPCallEndedMsg struct {
    CallID   string
    Status   string // "ok" | "error" | "timeout"
    Duration time.Duration
}
type MCPCallProgressMsg struct {
    CallID   string
    Step     string
    Tail     string // optional last-line preview of subprocess output
}
```

The MCP server gets a `func(tea.Msg)` injected on construction (already a pattern — see `internal/tui/bridge.go` and the `ToolBridge` interface). Tool handlers fire `MCPCallStartedMsg` at start, `MCPCallProgressMsg` at each checkpoint, `MCPCallEndedMsg` at end. Goroutine-safe because Bubble Tea's program already handles cross-goroutine `Send`.

The status bar maintains `map[string]OpDescriptor` keyed by `CallID`, updated by the `Update` reducer.

### Why this works during a hang

If the supervisor goroutine wedges in `RealRunTests`, the TUI goroutine is unaffected — it last received `MCPCallProgressMsg{Step:"merge.validate-started"}`, and the status line keeps ticking the elapsed time. The user sees "retire(finn): make validate, T+10m" *long before* they Ctrl-C. They can then `pkill -USR1 sprawl` (A3) before killing the host.

### Optional: viewport banner on long ops

When an op exceeds 60s, escalate to a viewport banner: "⏳ retire(finn) is taking longer than usual (T+1m12s). Send SIGUSR1 to capture state." Reusing the existing inbox-banner pattern.

### Implementation cost

- New messages + status-bar plumbing: ~250 LOC, mostly tests
- Bridge wiring (server.go injection): ~80 LOC
- Tool-handler call-log/heartbeat helper (shared with A1): ~100 LOC
- Mandatory TUI E2E test guard (per CLAUDE.md): ~100 LOC

Total: ~500 LOC, ~1 day for one engineer.

---

## 5. Recommended sequencing

```
                ┌──────────────────────────────┐
                │  P0  hang root-cause repro    │  ← Bug ticket; hand-in-hand with A
                └──────────────┬───────────────┘
                               │
       ┌───────────────────────┴────────────────────────┐
       │                                                 │
┌──────▼─────────┐                              ┌────────▼───────────┐
│ A1+A2 call log │                              │ A3 SIGUSR1 dump    │
│ + in-flight    │  (single PR, foundational)   │ (independent PR)   │
└──────┬─────────┘                              └────────────────────┘
       │
┌──────▼──────────┐
│ A5 subprocess   │  (depends on A1 for the call-log sink)
│ wrapper         │
└──────┬──────────┘
       │
┌──────▼──────────┐
│ C TUI surfacing │  (depends on A1 message vocabulary; reuses bridge)
└──────┬──────────┘
       │
┌──────▼──────────┐
│ B experiment    │  (small, can run anytime; report verdict, do not build)
└─────────────────┘
```

**Order of merging:**

1. **Bug ticket (P0):** runtime repro of the hang. Block on nothing; this becomes the test fixture for A's diagnostics.
2. **A1 + A2 in one PR:** call-log JSONL + in-flight registry. Foundational; nothing else lands without these. Medium priority.
3. **A3 SIGUSR1 handler:** independent, can ship in parallel with A1+A2. Medium.
4. **A5 subprocess wrapper:** lands after A1 (uses the call-log sink). Medium.
5. **C TUI surfacing:** lands after A1's message vocabulary stabilizes. Medium.
6. **B experiment:** one-day spike, report verdict in a Linear comment. Low.
7. **A4 pprof socket:** only if A3 proves insufficient on a real recurrence. Low / deferred.

**What to ship first if forced to pick one:** A1. Without per-call IDs and crash-safe checkpoints, every future hang is a guessing game.

---

## 6. Open questions

1. **Repro:** does `make validate` actually hang when invoked from inside the live host? Need a sandbox repro before we trust hypothesis 1.
2. **Locks:** does anything in `make test` flock `.sprawl/locks/*.lock`? A grep would settle hypothesis 2 in 5 minutes.
3. **Claude Code MCP timeout:** does the Claude Code MCP client actually time out tool calls? If so at what duration? If yes, the supervisor's goroutine outlives the cancelled request and that explains the "no error surfaced" UX. (See `cmd.CombinedOutput()` not honoring ctx.)
4. **Cascading retire's UX:** if retire of a parent triggers retire of children, we'd want the call log to show the *whole tree* under one root call_id with sub-call_ids. Worth considering during A1's design.

---

## References

- `internal/sprawlmcp/server.go` — JSON-RPC dispatch
- `internal/sprawlmcp/tools.go` — tool definitions
- `internal/supervisor/real.go:Merge,Retire` — caller-identity plumbing (QUM-487)
- `internal/agentops/{merge,retire}.go` — pre-flight + orchestration
- `internal/merge/merge.go,git.go` — flock + git steps + RealRunTests
- `internal/tui/{statusbar,banner,messages,bridge}.go` — TUI surfaces
- claude-code#4157 — progress-notification client support, "not planned"
- claude-code#41733 — adjacent notification-drop regression
- `tui-stderr-20260506-153820.log` — hang session stderr (pre-restart)
- `tui-stderr-20260506-184811.log`, `190039.log` — post-restart success
