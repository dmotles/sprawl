# Root Cause: Child Agent Viewport Missing Tool Call Output

## Summary

Child agent viewports show tool call headers (name, input summary) but **not** their results because `scanTranscript()` in `internal/tui/replay.go` discards `tool_result` content blocks when parsing the Claude session JSONL log. The fix is ~20 lines in `scanTranscript`.

## Architecture Context

### Root agent (weave) — live bridge streaming

The root agent's viewport is populated via real-time protocol events from the Bridge:

1. `assistant` message with `tool_use` block → `ToolCallMsg` → `ViewportModel.AppendToolCall()` creates a `MessageEntry{Type: MessageToolCall, Pending: true, Result: ""}`
2. `user` message with `tool_result` block → `ToolResultMsg` → `ViewportModel.MarkToolResult()` finds the entry by `ToolID`, sets `Pending=false`, populates `Result` with the content

The viewport renderer (`renderToolCall` in viewport.go:532) then shows a 3-line preview of `msg.Result` below the tool call header.

### Child agents — transcript replay

Child agent viewports are populated by polling the child's Claude session JSONL log file on a 2-second tick:

1. `app.go:818` — `AgentSelectedMsg` triggers `loadChildTranscriptCmd()`
2. `app.go:1372` — reads `.sprawl/agents/<name>.json` for session_id + worktree
3. `app.go:1397` — calls `LoadChildTranscript()` → `scanTranscript()` (replay.go:80)
4. `app.go:838` — `ChildTranscriptMsg` handler calls `vp.SetMessages(msg.Entries)`

## The Bug

In `scanTranscript()` (`internal/tui/replay.go:80-227`), the `user` record handler processes array content blocks at lines 142-173:

```go
case []any:
    var parts []string
    for _, b := range c {
        bm, ok := b.(map[string]any)
        if !ok {
            continue
        }
        bt, _ := bm["type"].(string)
        if bt == "text" {
            if txt, ok := bm["text"].(string); ok && txt != "" {
                parts = append(parts, txt)
            }
        }
        // QUM-379: tool_result blocks pop Agent IDs from the nesting stack.
        if bt == "tool_result" {
            tid, _ := bm["tool_use_id"].(string)
            for j := len(agentStack) - 1; j >= 0; j-- {
                if agentStack[j] == tid {
                    agentStack = append(agentStack[:j], agentStack[j+1:]...)
                    break
                }
            }
        }
    }
```

**The `tool_result` branch only pops the Agent nesting stack. It never extracts the `content` field or patches it onto the corresponding `MessageToolCall` entry.**

As a result, every `MessageToolCall` entry produced by `scanTranscript` has:
- `Pending: false` (Go zero value — correct, the call is not in flight)
- `Result: ""` (empty — **this is the bug**)

The renderer at viewport.go:601 checks `!msg.Pending && msg.Result != ""` before rendering the result preview, so empty results are simply not shown.

## Proposed Fix

In the `tool_result` handling block of `scanTranscript` (replay.go ~line 155), after the agent stack pop:

1. Extract the `content` field from the `tool_result` block (it can be a string or array of `{type:"text", text:"..."}` — same polymorphism handled by `flattenToolResultContent` in bridge.go)
2. Extract the `is_error` boolean
3. Walk backward through `entries` to find the matching `MessageToolCall` by `tool_use_id` == `ToolID`
4. Set `entry.Result = content` and `entry.Failed = isError`

This mirrors the logic of `ViewportModel.MarkToolResult()` (viewport.go:269-296) but operates on the flat `entries` slice during scan rather than the live message buffer.

The content extraction can reuse `flattenToolResultContent` from bridge.go (it accepts `json.RawMessage`), or a parallel implementation since the transcript content is `map[string]any` not `json.RawMessage`. A local helper that handles both string and `[]any` forms would be cleanest.

### Sketch

```go
// Inside the tool_result branch of scanTranscript:
if bt == "tool_result" {
    tid, _ := bm["tool_use_id"].(string)
    isErr, _ := bm["is_error"].(bool)
    resultContent := extractToolResultContent(bm["content"])

    // Pop agent stack (existing code)
    for j := len(agentStack) - 1; j >= 0; j-- {
        if agentStack[j] == tid {
            agentStack = append(agentStack[:j], agentStack[j+1:]...)
            break
        }
    }

    // NEW: patch result onto matching tool call entry
    if tid != "" {
        for j := len(entries) - 1; j >= 0; j-- {
            if entries[j].Type == MessageToolCall && entries[j].ToolID == tid {
                entries[j].Result = resultContent
                entries[j].Failed = isErr
                break
            }
        }
    }
}
```

Where `extractToolResultContent` handles the polymorphic content field:

```go
func extractToolResultContent(v any) string {
    switch c := v.(type) {
    case string:
        return c
    case []any:
        var parts []string
        for _, b := range c {
            bm, ok := b.(map[string]any)
            if !ok { continue }
            if bt, _ := bm["type"].(string); bt == "text" {
                if txt, ok := bm["text"].(string); ok && txt != "" {
                    parts = append(parts, txt)
                }
            }
        }
        return strings.Join(parts, "\n")
    }
    return ""
}
```

## Files Involved

| File | Lines | Role |
|------|-------|------|
| `internal/tui/replay.go` | 80-227 | `scanTranscript` — the bug location; needs the fix |
| `internal/tui/replay.go` | 49-75 | `LoadChildTranscript` — calls scanTranscript; no change needed |
| `internal/tui/bridge.go` | 286-315 | `mapUserMessage` — live equivalent that correctly emits `ToolResultMsg` |
| `internal/tui/bridge.go` | 322-344 | `flattenToolResultContent` — reference implementation for content extraction |
| `internal/tui/viewport.go` | 269-296 | `MarkToolResult` — live equivalent that patches Result onto entries |
| `internal/tui/viewport.go` | 598-626 | `renderToolCall` result preview block — correctly gated on `!msg.Pending && msg.Result != ""` |
| `internal/tui/app.go` | 830-861 | `ChildTranscriptMsg` handler — routes parsed entries to viewport; no change needed |

## Why Root Agent Works

The root agent (weave) never goes through `scanTranscript` for live content. Its viewport is populated by the Bridge's real-time protocol event stream (`bridge.go:mapUserMessage` → `ToolResultMsg` → `app.go:486` → `viewport.MarkToolResult`), which correctly extracts and patches tool results. The root agent only uses `LoadTranscript` (which also calls `scanTranscript`) for **resume replay** of a prior session — meaning the same bug exists for root-agent resumed sessions too, though it's less noticeable since live streaming quickly takes over.

## Open Questions

1. **Resume replay for root agent**: `LoadTranscript` calls the same `scanTranscript`, so resumed root sessions also lack tool results in the replayed portion. This is the same bug, just less visible. The fix to `scanTranscript` addresses both paths.
2. **Performance**: The backward walk to find the matching tool call is O(n) per result, but n is bounded by `ReplayMaxMessages` (500) and tool calls are typically recent, so the walk is short in practice.
