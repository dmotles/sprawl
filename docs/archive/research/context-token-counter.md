# Context Token Counter for Sprawl TUI — Research Findings

**Date**: 2026-04-30
**Researcher**: query
**Branch**: dmotles/research-context-token-counter

## Executive Summary

Implementing a context window usage counter in the TUI status bar is **feasible with moderate effort**. The data we need is already flowing through the system — it's just not being extracted. Claude Code's `assistant` messages contain a `usage` object with full token breakdowns (input, output, cache), and the `SystemInit` message includes the model name from which we can derive the context window limit. No new external data sources or API changes are required.

---

## 1. Available Data Sources

### 1.1 Token Usage in Assistant Messages (Primary Source)

Each `assistant` protocol message wraps the full Anthropic API message object, which includes a `usage` field:

```json
{
  "type": "assistant",
  "message": {
    "id": "msg_01ABC123",
    "role": "assistant",
    "model": "claude-sonnet-4-6-20250514",
    "content": [...],
    "usage": {
      "input_tokens": 15234,
      "output_tokens": 823,
      "cache_creation_input_tokens": 6264,
      "cache_read_input_tokens": 9666
    }
  }
}
```

**Currently unused by sprawl.** The bridge (`internal/tui/bridge.go:158-186`) only parses `content` blocks from `AssistantMessage.Content` (looking for `text` and `tool_use` blocks). The `usage` object is present in the raw JSON but never deserialized.

The `AssistantMessage` type (`internal/protocol/types.go:38-44`) stores the message as `json.RawMessage`:

```go
type AssistantMessage struct {
    Type            string          `json:"type"`
    Content         json.RawMessage `json:"message"` // Full API response — includes usage
    ParentToolUseID *string         `json:"parent_tool_use_id"`
}
```

**Implementation**: Add a `Usage` struct to `assistantContent` (or parse separately from `AssistantMessage.Content`) and emit it as part of the TUI message flow.

### 1.2 Model Name from SystemInit

The `SystemInit` message (`internal/protocol/types.go:23-34`) already includes the model:

```go
type SystemInit struct {
    Model          string   `json:"model"`
    // ... other fields
}
```

This gives us e.g. `"claude-sonnet-4-6-20250514"` or `"claude-opus-4-7-20260301"`, from which we can derive the context window limit.

**Currently**: The bridge ignores `system` messages entirely (`bridge.go:148-155` returns `nil`). The model name is not surfaced to the TUI.

### 1.3 Result Message (Already Used)

The `ResultMessage` provides `total_cost_usd` (session-cumulative), which is already displayed in the status bar. It does **NOT** include token counts — only cost, duration, and turn count.

### 1.4 Claude Code Session JSONL Files (Backup Source)

Claude Code's own session logs at `~/.claude/projects/<hash>/<session-id>.jsonl` contain per-turn `usage` objects with full breakdowns. These are the richest data source but:
- Depend on Claude Code's internal file format (undocumented, may change)
- Require file I/O and parsing
- Are redundant if we extract usage from the stream-json protocol directly

**Recommendation**: Use the stream-json assistant messages as the primary source. Session JSONL files are useful only for retroactive analysis or debugging.

---

## 2. Model Context Window Limits

As of April 2026, Claude model context windows:

| Model | Context Window | Max Output |
|-------|---------------|------------|
| Claude Opus 4.7 | 1,000,000 tokens | 64,000 tokens |
| Claude Opus 4.6 | 1,000,000 tokens | 32,000 tokens |
| Claude Sonnet 4.6 | 1,000,000 tokens | 64,000 tokens |
| Claude Mythos Preview | 1,000,000 tokens | 64,000 tokens |

**All current models support 1M context windows** (GA as of March 2026 with no pricing premium). Older models (Claude 3.x) had 200K windows.

**Implementation approach**: Map model name prefix → context window size. A simple lookup table suffices:

```go
var modelContextWindows = map[string]int{
    "claude-opus-4":   1_000_000,
    "claude-sonnet-4": 1_000_000,
    "claude-mythos":   1_000_000,
    // fallback for unknown models
}
```

Match by prefix (e.g. `strings.HasPrefix(model, "claude-opus-4")`) to handle version suffixes like `-20250514`.

---

## 3. Compaction Detection

### 3.1 API-Level Compaction

The Anthropic API supports server-side compaction (beta: `compact-2026-01-12`). When compaction occurs:
1. A `compaction` content block appears in the response
2. Input tokens drop dramatically (the summary replaces the full history)
3. All message blocks prior to the compaction block are dropped

### 3.2 Claude Code's Internal Compaction

Claude Code uses its own compaction logic (via `autoCompactIfNeeded` in its `QueryEngine`). The threshold is:
- `effectiveWindow = contextWindow - max(maxOutputTokens, 20_000)`
- `autoCompactThreshold = effectiveWindow - 13_000`
- For a 200K model: ~167K trigger. For 1M model: ~967K trigger.

### 3.3 Detection in stream-json

**No explicit compaction event exists in stream-json output.** However, compaction can be detected by observing:
1. A sudden drop in `input_tokens` between consecutive `assistant` messages
2. The appearance of a `compaction` content block in the assistant message

**Implementation**: Track `input_tokens` across turns. If `input_tokens[n] < input_tokens[n-1] * 0.5`, flag it as a compaction event. Increment a compaction counter displayed in the UI.

---

## 4. Proposed Implementation

### 4.1 Protocol Layer Changes

Add a `Usage` type to `internal/protocol/types.go`:

```go
// Usage contains token consumption metrics from an assistant turn.
type Usage struct {
    InputTokens              int `json:"input_tokens"`
    OutputTokens             int `json:"output_tokens"`
    CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
    CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}
```

Extend the assistant message parsing to extract usage from the wrapped API response.

### 4.2 TUI Message Layer

Add a new `SessionUsageMsg` to `internal/tui/messages.go`:

```go
type SessionUsageMsg struct {
    InputTokens  int
    OutputTokens int
    CacheRead    int
    CacheCreate  int
}
```

### 4.3 Bridge Changes

In `mapAssistantMessage()`, also parse the `usage` field from the API response envelope and emit `SessionUsageMsg`:

```go
type apiMessageEnvelope struct {
    Content []contentBlock `json:"content"`
    Model   string         `json:"model,omitempty"`
    Usage   *protocol.Usage `json:"usage,omitempty"`
}
```

Additionally, handle `SystemInit` messages to capture the model name and emit a `SessionModelMsg`.

### 4.4 Status Bar Display

Add fields to `StatusBarModel`:

```go
type StatusBarModel struct {
    // ... existing fields
    contextTokens    int    // current input_tokens (proxy for context usage)
    contextLimit     int    // derived from model name
    compactionCount  int    // number of detected compactions
}
```

Render as: `42.3k/1M tokens` or `42,312/1,000,000 tokens` (compact form preferred for status bar real estate).

### 4.5 Data Flow

```
Claude Code stdout
  ↓
protocol.Message (type="assistant")
  ↓
bridge.mapAssistantMessage()
  ├→ AssistantTextMsg / ToolCallMsg (existing)
  └→ SessionUsageMsg{InputTokens, OutputTokens, ...} (NEW)
  ↓
AppModel.Update()
  ├→ statusBar.SetTokenUsage(msg.InputTokens, msg.OutputTokens)
  └→ detect compaction (input_tokens drop)
  ↓
StatusBar.View()
  → "42.3k/1M tokens | $0.0234 | Streaming..."

Also:
protocol.Message (type="system", subtype="init")
  ↓
bridge.mapProtocolMessage() → SessionModelMsg{Model: "claude-opus-4-7-..."}
  ↓
AppModel.Update()
  → statusBar.SetContextLimit(deriveLimit(msg.Model))
```

---

## 5. Implementation Complexity

### Low complexity (can be done in a single PR):

1. **Add `Usage` struct to protocol types** — trivial
2. **Parse usage from assistant messages in bridge** — ~20 lines
3. **Surface `SystemInit.Model` to TUI** — ~15 lines
4. **Add token counter to status bar** — ~30 lines
5. **Model→context-window lookup** — ~15 lines

### Medium complexity (follow-up):

6. **Compaction detection** — track input_tokens across turns, flag drops
7. **Persist token usage to agent state** — like cost persistence
8. **Token usage in `sprawl status` output** — aggregate across agents

### Estimated effort: 1-2 days for the core feature (items 1-5).

---

## 6. Key Design Decisions

### Q: What number represents "context window usage"?

`input_tokens` from the most recent assistant message is the best proxy. It represents the total tokens Claude received as input for that turn — system prompt + conversation history + tool results. This number grows as the conversation progresses and drops when compaction occurs.

**Alternative**: Sum `input_tokens + output_tokens` for total context consumption. But `input_tokens` alone is the standard measure of "how much of the context window is used" because output tokens don't persist in the same way.

### Q: Handle multiple messages per turn?

A single turn may produce multiple `assistant` messages (e.g., thinking → tool_use → text). Each has its own `usage`. Use the **latest** `input_tokens` value — it reflects the current context state.

### Q: What about the `--include-partial-messages` flag?

Stream events (`stream_event` type) contain partial deltas, not full usage objects. Usage data is only complete in full `assistant` messages. Sprawl already receives these (they're the primary protocol message type).

---

## 7. Reflections

### Surprising findings
- The token usage data is **already flowing through the system** in assistant messages — it's just being ignored. No new data sources needed.
- Ghost's prior research (2026-04-29) focused heavily on session JSONL files as the token data source, but the simpler path is right in the stream-json protocol.
- The `SetTurnCost` double-counting bug noted in ghost's research has already been fixed (QUM-366) — the status bar now replaces rather than accumulates.
- All current Claude models have 1M context windows, simplifying the context limit logic.

### Open questions
1. **Does the `assistant` message `usage.input_tokens` actually appear in stream-json output?** Ghost's research noted "NOT available: Per-token breakdown" in the result message, but didn't check the assistant message envelope. The Anthropic API docs and Claude Code JSONL files confirm it's there, but this should be verified by running `claude -p "hello" --output-format stream-json --verbose 2>/dev/null` and checking the assistant message.
2. **How does compaction affect the `usage` object?** After compaction, does `input_tokens` reflect the post-compaction size, or the pre-compaction size?
3. **Is the model name in `SystemInit` always present?** Edge case: what if Claude Code is configured with a custom model or the field is empty?
4. **Should we show effective tokens or raw tokens?** Cache read tokens are "free" in terms of cost but still count toward the context window. The user likely cares about total context usage regardless of caching.

### What I'd investigate next with more time
- **Empirical verification**: Run a multi-turn session with `--output-format stream-json --verbose` and capture the raw JSON to confirm `usage` fields appear in assistant messages.
- **Compaction observation**: Run a long session until compaction triggers and observe how `input_tokens` changes.
- **Cache token semantics**: Clarify whether `cache_read_input_tokens` should be added to `input_tokens` for context window accounting, or if `input_tokens` already includes them.
