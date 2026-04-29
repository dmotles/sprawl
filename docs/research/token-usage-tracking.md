# Token Usage & Cost Tracking in Sprawl — Research Findings

**Date**: 2026-04-29
**Researcher**: ghost
**Branch**: dmotles/research-token-usage-tracking

## Executive Summary

Sprawl already has a **live cost display** in the TUI status bar (cumulative `$X.XXXX` per session), sourced from the `total_cost_usd` field in Claude Code's `result` protocol messages. However, **no token/cost data is persisted to disk** by sprawl itself — it's ephemeral, lost when the TUI exits. Separately, Claude Code's own session JSONL files contain **rich per-turn token breakdowns** (input, output, cache read, cache creation) that could serve as a retroactive data source.

---

## 1. What Exists Today

### 1.1 Protocol Layer (`internal/protocol/types.go`)

The `ResultMessage` struct captures cost from Claude Code's stream-json output:

```go
type ResultMessage struct {
    // ...
    TotalCostUsd float64  `json:"total_cost_usd"`
    DurationMs   int      `json:"duration_ms"`
    NumTurns     int      `json:"num_turns"`
}
```

This field is emitted by Claude Code at the end of every turn (type=`result`). It represents the **cumulative cost of that entire session up to that point** (not a per-turn delta).

### 1.2 TUI Display (`internal/tui/`)

The TUI has a working cost display pipeline:

1. **Bridge** (`bridge.go:336-348`): Parses `ResultMessage` → emits `SessionResultMsg` with `TotalCostUsd`
2. **App** (`app.go:521-522`): On `SessionResultMsg`, calls `m.statusBar.SetTurnCost(msg.TotalCostUsd)` and appends a status line: `"Completed in Xms, cost $X.XXXX"`
3. **StatusBar** (`statusbar.go:93-95`): `SetTurnCost` **accumulates** cost: `m.sessionCostUsd += cost`

**Important nuance**: `SetTurnCost` adds the incoming value to the running total. But `total_cost_usd` from Claude Code appears to be cumulative already. This means the status bar may be **double-counting** if a session has multiple turns — the value from turn 2 already includes turn 1's cost, but the status bar adds it again. (This should be verified, but the field name `total_cost_usd` and the test values suggest cumulative semantics.)

### 1.3 Agent State (`internal/state/state.go`)

The `AgentState` struct has **no cost or token fields**:

```go
type AgentState struct {
    Name, Type, Family, Parent, Prompt, Branch, Worktree, Status string
    CreatedAt, SessionID string
    Subagent bool
    TreePath string
    LastReportType, LastReportMessage, LastReportAt, LastReportState, LastReportDetail string
}
```

Nothing persisted to `.sprawl/agents/<name>.json` relates to cost or tokens.

### 1.4 Observe Package (`internal/observe/observe.go`)

No metrics, no cost fields. Purely for building the agent tree from state + supervisor data.

### 1.5 Supervisor (`internal/supervisor/`)

No token/cost tracking. The supervisor manages process lifecycle only.

### 1.6 Agent Loop (`internal/agentloop/process.go`)

The `Process.SendPrompt` method returns `*protocol.ResultMessage` which includes `TotalCostUsd`, but the caller (TUI bridge or headless runner) does not persist it anywhere.

---

## 2. External Data Sources Available

### 2.1 Claude Code Protocol Output (stream-json)

When sprawl runs Claude with `--output-format stream-json`, every turn completion emits:

```json
{"type":"result","subtype":"success","is_error":false,"duration_ms":5000,"num_turns":2,"total_cost_usd":0.03,"stop_reason":"end_turn"}
```

**Available fields**: `total_cost_usd`, `duration_ms`, `num_turns`
**NOT available**: Per-token breakdown (input/output/cache tokens)

### 2.2 Claude Code Session JSONL Files (`~/.claude/projects/.../<session-id>.jsonl`)

These are Claude Code's own persistence files. Each `assistant`-type message includes a detailed `usage` object:

```json
{
  "usage": {
    "input_tokens": 3,
    "cache_creation_input_tokens": 6264,
    "cache_read_input_tokens": 9666,
    "output_tokens": 155,
    "server_tool_use": {"web_search_requests": 0, "web_fetch_requests": 0},
    "service_tier": "standard",
    "cache_creation": {
      "ephemeral_1h_input_tokens": 0,
      "ephemeral_5m_input_tokens": 6264
    },
    "iterations": [...],
    "speed": "standard"
  }
}
```

**Available fields**:
- `input_tokens`, `output_tokens`
- `cache_creation_input_tokens`, `cache_read_input_tokens`
- Cache breakdown by TTL (`ephemeral_5m`, `ephemeral_1h`)
- `server_tool_use` counters (web search, web fetch)
- `service_tier`, `speed`

**Location**: `~/.claude/projects/<project-path-hash>/<session-id>.jsonl`
Each sprawl agent has a `session_id` in its state file, so the mapping agent→session→JSONL is possible.

### 2.3 Claude CLI Flags

- `--max-budget-usd <amount>`: Budget cap (print mode only) — suggests Claude tracks cost internally
- No `--report-usage` or similar flag exists to get a usage summary after exit

---

## 3. Gaps for a Token Usage Dashboard

### Gap 1: No Persistence of Cost Data

The TUI displays cost live but discards it on exit. To build historical tracking:

**Option A — Tap the protocol stream**: Intercept `ResultMessage` in the observer/bridge and write cost + timestamp + agent name to a file (e.g., `.sprawl/agents/<name>/cost-log.jsonl`). Easy to implement; gives per-turn `total_cost_usd` but no token breakdown.

**Option B — Parse Claude session JSONL**: Read `~/.claude/projects/.../<session-id>.jsonl` files post-hoc. Gives full token breakdown but depends on Claude Code's internal file format (undocumented, may change).

**Option C — Hybrid**: Use Option A for real-time tracking, Option B for detailed historical analysis.

### Gap 2: No Per-Token Breakdown in Protocol

The stream-json protocol only provides `total_cost_usd` — no `input_tokens`, `output_tokens`, etc. Getting token counts requires either:
- Reading Claude's session JSONL files (Option B above)
- Requesting that Claude Code add token counts to `result` messages (upstream change)

### Gap 3: No Cross-Agent Aggregation

No mechanism exists to sum costs across a parent and its children. Would need:
- A central cost ledger (e.g., `.sprawl/state/cost-ledger.jsonl`)
- Or a query that walks `.sprawl/agents/*/cost-log.jsonl` files

### Gap 4: No Live Running Meter for Children

The TUI only shows cost for the root agent's own Claude session. Child agents run in separate tmux sessions with no cost feedback to the parent TUI. Options:
- Children could write cost events to their state, and the TUI tree poll could surface them
- The supervisor could aggregate cost data from child protocol streams

### Gap 5: Status Bar Accumulation Bug (Potential)

`SetTurnCost` adds `msg.TotalCostUsd` to a running total, but `total_cost_usd` from Claude's `result` message appears to be **session-cumulative** (not a per-turn delta). If a session has multiple turns, the status bar would show inflated costs. This needs verification — if `total_cost_usd` is indeed cumulative, the fix is to track the last value and compute the delta.

---

## 4. Recommended Architecture for Token Usage Feature

### Minimal viable approach (taps existing data):

1. **Add cost fields to `AgentState`**: `TotalCostUsd float64`, `LastCostAt string`
2. **Persist on each turn result**: In the observer/bridge, update the agent's state file when a `ResultMessage` arrives
3. **Display in tree view**: Show per-agent cost in the TUI tree and `sprawl status` output
4. **Aggregate**: Sum `TotalCostUsd` across all agents for a session total

### Full approach (adds token granularity):

1. **Write a cost log**: `.sprawl/agents/<name>/cost-log.jsonl` with per-turn entries including timestamp, cost, and (if available) token counts
2. **Session JSONL reader**: A read-only parser for `~/.claude/projects/.../<session-id>.jsonl` that extracts `usage` objects — for retroactive analysis and richer dashboards
3. **TUI cost panel**: A dedicated view showing per-agent costs, token breakdown, and session totals
4. **`sprawl cost` command**: CLI command to report costs across agents

---

## 5. Reflections

### Surprising findings
- The TUI **already displays live cost** — this feature is further along than expected
- Claude Code's session JSONL files contain **extremely detailed** token breakdowns including cache hit/miss data — far richer than what the protocol exposes
- The `SetTurnCost` accumulation pattern may be double-counting if `total_cost_usd` is session-cumulative (needs verification)

### Open questions
- Is `total_cost_usd` in `ResultMessage` a per-turn delta or session-cumulative total? The field name suggests cumulative, but the TUI treats it as a delta (additive). Need to test with a multi-turn session.
- Do child agents launched via `sprawl spawn` (tmux mode) emit stream-json output that could be captured? Currently they run in interactive tmux sessions, not stream-json mode.
- What's the Claude Code team's roadmap for usage reporting? Could token counts be added to the `result` message type?
- How stable is the `~/.claude/projects/` JSONL format? Is it safe to depend on for a feature?

### Next steps if investigating further
- **Verify `total_cost_usd` semantics**: Run a multi-turn session and check whether the value is cumulative or incremental
- **Prototype cost persistence**: Add `TotalCostUsd` to `AgentState` and persist it from the bridge
- **Build a session JSONL parser**: Read-only Go package that extracts token usage from Claude's own logs
- **Design the `sprawl cost` command**: CLI interface for querying historical cost data
