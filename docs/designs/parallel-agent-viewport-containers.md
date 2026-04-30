# Design: Parallel Agent Tool Call Containers in TUI Viewport

**Status**: Proposal
**Related**: QUM-379 (agent nesting), viewport.go

## Problem

When weave fires two `Agent` tool calls in parallel, the viewport renders
nested tool calls at the wrong depth. The root cause is `agentCallStack`, a
linear stack in `ViewportModel`:

```go
agentCallStack []string   // push on Agent start, pop on Agent result
```

Depth for every appended tool call is `len(agentCallStack)`. With two parallel
agents, the stack holds both IDs, yielding `Depth = 2` — as if one Agent is
nested inside the other. In reality they are siblings.

**Sequence showing the bug:**

```
AppendToolCall("Agent", "a1")  → stack=["a1"],     depth=0 (Agent itself)
AppendToolCall("Agent", "a2")  → stack=["a1","a2"], depth=1 (Agent itself — wrong for a2, should be 0)
AppendToolCall("Bash", "b1")   → depth=2            (WRONG: should be 1)
MarkToolResult("a1")           → stack=["a2"]
AppendToolCall("Read", "r1")   → depth=1            (correct now, but only by accident)
```

The desired behaviour is two **independent containers**, each showing their
own nested activity, with no cross-contamination.

## Library Constraints

| Component | Notes |
|-----------|-------|
| `bubbles/v2 viewport` | Pure string renderer — `SetContent(string)`. No nested viewports, no sub-components. Content changes require full re-render via `renderMessages()`. |
| `lipgloss/v2` | Styling only. No layout containers. |
| `glamour` | Markdown rendering to ANSI. Used for assistant text blocks. |
| `ansi` (charmbracelet/x) | `Truncate`, `Wrap`, `Cut`. Width-aware string ops. |

**Key constraint**: The viewport is a single scrollable pane backed by one
rendered string. "Containers" are a rendering illusion — box-drawing characters
and indentation, not real widgets. Any "container with its own scrolling" would
have to be simulated by clipping/truncating lines in `renderMessages()`.

## Proposed Design

### 1. Data Model Changes

#### Replace stack with a set + attribution

```go
// ViewportModel fields (replacing agentCallStack)

// activeAgents is the set of in-flight Agent tool call IDs.
activeAgents map[string]bool

// lastActiveAgent is the toolID of the most recently started Agent.
// Used to attribute incoming nested tool calls to a parent container.
lastActiveAgent string
```

#### New field on MessageEntry

```go
// ParentToolID links a nested tool call to its parent Agent's toolID.
// Empty for top-level entries and for Agent entries themselves at depth 0.
ParentToolID string
```

### 2. AppendToolCall Changes

```go
func (m *ViewportModel) AppendToolCall(name, toolID string, approved bool, input, fullInput string) {
    depth := 0
    parentID := ""
    if len(m.activeAgents) > 0 && name != "Agent" {
        depth = 1
        parentID = m.lastActiveAgent
    }
    // An Agent call inside another Agent is also nested.
    if name == "Agent" && len(m.activeAgents) > 0 {
        depth = 1
        parentID = m.lastActiveAgent
    }

    m.messages = append(m.messages, MessageEntry{
        // ... existing fields ...
        Depth:        depth,
        ParentToolID: parentID,
    })

    if name == "Agent" && toolID != "" {
        if m.activeAgents == nil {
            m.activeAgents = make(map[string]bool)
        }
        m.activeAgents[toolID] = true
        m.lastActiveAgent = toolID
    }
    m.renderAndUpdate()
}
```

**Simplification**: Depth is capped at 1. True multi-level nesting (Agent
calling Agent calling Agent) is vanishingly rare in practice. YAGNI — if
needed later, `ParentToolID` chains already encode the full tree.

### 3. MarkToolResult Changes

```go
func (m *ViewportModel) MarkToolResult(toolID, content string, isError bool) bool {
    // ... existing match logic ...

    // If the completed tool is an Agent, remove from active set.
    if m.activeAgents[toolID] {
        delete(m.activeAgents, toolID)
        if m.lastActiveAgent == toolID {
            m.lastActiveAgent = ""
            for id := range m.activeAgents {
                m.lastActiveAgent = id
                break // pick any remaining
            }
        }
    }
    m.renderAndUpdate()
    return true
}
```

### 4. Rendering Changes

#### Container rendering for Agent entries

The current `renderMessages` renders entries linearly. The change introduces a
two-pass approach:

**Pass 1 — Build child index:**

```go
// childrenOf maps an Agent's toolID to the indices of its child entries.
childrenOf := map[string][]int{}
rendered  := map[int]bool{}  // entries rendered inside a container

for i, msg := range m.messages {
    if msg.ParentToolID != "" {
        childrenOf[msg.ParentToolID] = append(childrenOf[msg.ParentToolID], i)
        rendered[i] = true
    }
}
```

**Pass 2 — Render with containers:**

```go
for i, msg := range m.messages {
    if rendered[i] {
        continue // already rendered inside a parent Agent container
    }
    if msg.Type == MessageToolCall && msg.Content == "Agent" {
        m.renderAgentContainer(&sb, msg, childrenOf[msg.ToolID])
    } else {
        // existing render logic
    }
}
```

#### renderAgentContainer (new function)

```
┌ ⠋ Agent
│   sub-task description
│   ✓ Bash  ls -la
│   ⠋ Read  /tmp/output.txt
│   ✓ Grep  "TODO"
└
```

When the Agent **completes** (not Pending), collapse to:

```
┌ ✓ Agent
│   sub-task description
│ Result: The analysis found 3 issues...
│ + 4 more lines
└
```

The children are hidden and replaced by the Agent's own result preview.

**Implementation sketch:**

```go
func (m *ViewportModel) renderAgentContainer(sb *strings.Builder, agent MessageEntry, childIndices []int) {
    // Header: same as renderToolCall header (┌ indicator name)
    // ... render ┌ + indicator + Agent name ...

    // Input summary (agent description)
    // ... render body lines with │ gutter ...

    if agent.Pending {
        // Show live nested activity
        for _, idx := range childIndices {
            child := m.messages[idx]
            m.renderNestedToolCall(sb, child)
            sb.WriteString("\n")
        }
    } else {
        // Collapsed: show result preview only
        // ... same previewResultLines logic as existing renderToolCall ...
    }

    sb.WriteString(m.theme.AccentText.Render("└"))
}
```

### 5. Bridge Fix (separate concern, noted here)

`mapAssistantMessage` currently returns only the **first** `tool_use` block
from an assistant message. Parallel Agent calls produce multiple `tool_use`
blocks in one response. This needs a separate fix — either return a batch
message type or emit multiple `ToolCallMsg` values. This is a prerequisite
for parallel agents to even show up in the viewport.

**Suggested approach**: Return a new `AssistantContentMsg` that carries
multiple content blocks, and have the App's Update loop process each block:

```go
type AssistantContentMsg struct {
    Blocks []ContentBlock  // text blocks and tool_use blocks
}
```

This is a bridge-layer change, orthogonal to the viewport rendering fix.

## Edge Cases

| Scenario | Handling |
|----------|----------|
| Agent A finishes while B runs | A's container collapses (shows result). B continues showing live nested calls. `lastActiveAgent` moves to B. |
| 3+ parallel agents | Each gets its own container. `activeAgents` set handles N agents. `lastActiveAgent` attribution is best-effort (most recent). |
| Agent inside Agent (true nesting) | The inner Agent entry gets `depth=1, ParentToolID=outer`. Its own children get `depth=1, ParentToolID=inner`. Containers nest visually via indentation but depth stays capped at 1 for simplicity. |
| `SetMessages` called (transcript reload) | Clears `activeAgents` and `lastActiveAgent` (same as current stack clear). |
| Tool call arrives with no active agent | `depth=0, ParentToolID=""` — renders as normal top-level box. |
| Agent result arrives before any nested calls | Container shows just the result, no nested section. Same as any non-Agent tool call with a result. |
| Attribution error (tool attributed to wrong agent) | Worst case: a tool call shows under the wrong Agent's container. Visually imperfect but not broken. The "most recent agent" heuristic works well because Claude Code serializes sub-agent activity bursts. |

## Tradeoffs

| Decision | Rationale |
|----------|-----------|
| Depth capped at 1 | YAGNI. True multi-level nesting (Agent→Agent→Agent) is extremely rare. `ParentToolID` preserves the info if needed later. |
| "Most recent agent" heuristic for attribution | The protocol doesn't carry a parent-agent field. Ordering heuristic works because Claude Code serializes sub-agent activity. Perfect attribution would require protocol changes. |
| No real sub-viewports | Bubbles viewport is string-only. Sub-viewports would require a major refactor (multiple `viewport.Model` instances stitched together). The box-drawing illusion is much simpler and consistent with existing patterns. |
| Collapse on completion | Matches Claude Code's UX. Keeps the viewport uncluttered as agents finish. The full activity is still in `messages[]` for scroll-back / expand-mode if needed later. |

## Implementation Order

1. **Fix depth calculation** — Replace `agentCallStack` with `activeAgents` set.
   Minimal change, fixes the depth=2 bug immediately. All existing tests pass
   with adjusted expectations.

2. **Add ParentToolID** — Attribute nested calls to parent agents. Add field to
   `MessageEntry`, populate in `AppendToolCall`.

3. **Container rendering** — New `renderAgentContainer` function. Modify
   `renderMessages` to use two-pass approach. Existing `renderNestedToolCall`
   is reused for the live-activity lines.

4. **Collapse on completion** — In `renderAgentContainer`, switch between
   live-activity and result-preview modes based on `agent.Pending`.

5. **Bridge: multi-block assistant messages** — Separate PR/issue. Required for
   parallel agents to appear in the stream at all.

## Open Questions

- **Protocol fidelity**: Does Claude Code's streaming protocol actually
  interleave sub-agent events from parallel Agent calls? Or does it serialize
  them? If serialized, the attribution heuristic is reliable. If truly
  interleaved, we may need protocol-level parent tracking.

- **Expand mode interaction**: When `toolInputsExpanded` is true, should
  collapsed Agent containers expand to show their full nested activity history?
  Probably yes, but adds complexity to `renderAgentContainer`.

- **Viewport height changes**: When an Agent collapses (30 lines of activity →
  5 lines of result preview), the viewport content shrinks significantly.
  `renderAndUpdate` already calls `GotoBottom` when auto-scroll is on, which
  handles this. But if the user has scrolled up, the jump could be jarring.
  May need to preserve scroll offset relative to the visible content.
