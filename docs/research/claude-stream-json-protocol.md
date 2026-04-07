# Research: Claude Code stream-json Protocol and Agent SDK Internals

## Status: Complete

## Executive Summary

Claude Code supports a bidirectional NDJSON protocol (`--input-format stream-json` / `--output-format stream-json`) that enables programmatic control of a long-running Claude Code process via stdin/stdout. The official TypeScript Agent SDK (`@anthropic-ai/claude-agent-sdk`) uses this protocol internally. This document analyzes the protocol, SDK architecture, and provides recommendations for implementing a Go-based agent loop in Sprawl.

---

## 1. Stream-JSON Protocol Overview

### How It Works

When launched with `-p --input-format stream-json --output-format stream-json`, Claude Code:

1. Reads NDJSON (newline-delimited JSON) messages from **stdin**
2. Writes NDJSON messages to **stdout**
3. Runs as a **long-lived process** — it does NOT exit after processing one prompt
4. Supports **multi-turn conversations** — new user messages can be sent after each turn completes

This is fundamentally different from the `-p` (print mode) default, which processes one prompt and exits. The stream-json mode keeps the process alive and bidirectional.

### CLI Invocation

```bash
claude -p \
  --input-format stream-json \
  --output-format stream-json \
  --replay-user-messages \
  --verbose \
  --session-id "$SESSION_ID" \
  --system-prompt "$SYSTEM_PROMPT" \
  --permission-mode bypassPermissions
```

**Important flags:**

| Flag | Purpose |
|------|---------|
| `-p` / `--print` | Non-interactive mode (required) |
| `--input-format stream-json` | Accept NDJSON on stdin |
| `--output-format stream-json` | Emit NDJSON on stdout |
| `--replay-user-messages` | Re-emit user messages in output (SDK always sets this) |
| `--verbose` | Required when using stream-json output |
| `--include-partial-messages` | Include token-level streaming events |
| `--session-id <uuid>` | Deterministic session ID |
| `--resume <id-or-name>` | Resume an existing session |
| `--bare` | Skip autodiscovery (hooks, plugins, CLAUDE.md) — faster startup |

**Note:** `--output-format stream-json` **requires** `--verbose` — Claude Code will error without it.

---

## 2. Message Types (Output — stdout)

Every line on stdout is a JSON object. The `type` field determines the message kind.

### 2.1 StdoutMessage Union (from SDK types)

```
StdoutMessage =
  | SDKMessage            // Core messages (see below)
  | SDKControlResponse    // Responses to control requests
  | SDKControlRequest     // Permission requests from Claude
  | SDKControlCancelRequest
  | SDKKeepAliveMessage   // Connection keep-alive
```

### 2.2 SDKMessage Union (Core Messages)

```
SDKMessage =
  | SDKAssistantMessage        // Complete assistant turn
  | SDKUserMessage             // User message echo
  | SDKUserMessageReplay       // Replayed user message (resume)
  | SDKResultMessage           // Turn result (success or error)
  | SDKSystemMessage           // System init
  | SDKPartialAssistantMessage // Streaming tokens (stream_event)
  | SDKCompactBoundaryMessage  // Context compaction boundary
  | SDKStatusMessage           // Status changes (compacting, etc.)
  | SDKAPIRetryMessage         // API retry notification
  | SDKLocalCommandOutputMessage
  | SDKHookStartedMessage
  | SDKHookProgressMessage
  | SDKHookResponseMessage
  | SDKToolProgressMessage
  | SDKAuthStatusMessage
  | SDKTaskNotificationMessage // Background task completed
  | SDKTaskStartedMessage      // Background task started
  | SDKTaskProgressMessage     // Background task progress
  | SDKSessionStateChangedMessage
  | SDKFilesPersistedEvent
  | SDKToolUseSummaryMessage
  | SDKRateLimitEvent
  | SDKElicitationCompleteMessage
  | SDKPromptSuggestionMessage
```

### 2.3 Key Message Schemas

#### System Init (`type: "system", subtype: "init"`)

First message emitted after launch. Contains session metadata.

```json
{
  "type": "system",
  "subtype": "init",
  "session_id": "550e8400-e29b-41d4-a716-446655440000",
  "claude_code_version": "2.1.87",
  "cwd": "/home/user/project",
  "model": "claude-sonnet-4-6",
  "tools": ["Bash", "Read", "Edit", "Write", "Glob", "Grep"],
  "mcp_servers": [{"name": "linear", "status": "connected"}],
  "permissionMode": "bypassPermissions",
  "apiKeySource": "user",
  "agents": ["code-reviewer"],
  "slash_commands": ["/commit", "/review"],
  "output_style": "normal",
  "skills": [],
  "plugins": [],
  "uuid": "abc123...",
  "betas": []
}
```

#### Assistant Message (`type: "assistant"`)

A complete assistant response turn (after all tool calls resolve).

```json
{
  "type": "assistant",
  "uuid": "msg-uuid-123",
  "session_id": "session-uuid",
  "message": {
    "id": "msg_01...",
    "type": "message",
    "role": "assistant",
    "content": [
      {"type": "text", "text": "I'll fix that bug..."},
      {"type": "tool_use", "id": "toolu_01...", "name": "Read", "input": {"file_path": "/src/auth.py"}}
    ],
    "model": "claude-sonnet-4-6",
    "stop_reason": "end_turn",
    "usage": {"input_tokens": 1500, "output_tokens": 200}
  },
  "parent_tool_use_id": null
}
```

The `parent_tool_use_id` is non-null when the message originates from a **subagent** — it references the Agent tool_use that spawned it.

#### Stream Event (`type: "stream_event"`)

Token-level streaming (requires `--include-partial-messages`).

```json
{
  "type": "stream_event",
  "event": {
    "type": "content_block_delta",
    "index": 0,
    "delta": {"type": "text_delta", "text": "Hello"}
  },
  "parent_tool_use_id": null,
  "uuid": "evt-uuid",
  "session_id": "session-uuid"
}
```

#### Result Message (`type: "result"`)

Emitted when a turn completes (after all tool calls finish).

**Success:**

```json
{
  "type": "result",
  "subtype": "success",
  "result": "I've fixed the bug in auth.py by...",
  "duration_ms": 15000,
  "duration_api_ms": 8000,
  "is_error": false,
  "num_turns": 3,
  "total_cost_usd": 0.045,
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 5000, "output_tokens": 1200, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
  "modelUsage": {"claude-sonnet-4-6": {"inputTokens": 5000, "outputTokens": 1200}},
  "permission_denials": [],
  "uuid": "result-uuid",
  "session_id": "session-uuid"
}
```

**Error:**

```json
{
  "type": "result",
  "subtype": "error_max_turns",
  "duration_ms": 30000,
  "is_error": true,
  "num_turns": 10,
  "errors": ["Maximum turns (10) reached"],
  "stop_reason": null,
  "total_cost_usd": 0.12,
  "usage": {"input_tokens": 15000, "output_tokens": 3000},
  "uuid": "result-uuid",
  "session_id": "session-uuid"
}
```

Error subtypes: `error_max_turns`, `error_during_execution`, `error_max_budget_usd`, `error_max_structured_output_retries`.

#### API Retry (`type: "system", subtype: "api_retry"`)

```json
{
  "type": "system",
  "subtype": "api_retry",
  "attempt": 1,
  "max_retries": 5,
  "retry_delay_ms": 2000,
  "error_status": 529,
  "error": "rate_limit",
  "uuid": "retry-uuid",
  "session_id": "session-uuid"
}
```

#### Session State Changed (`type: "system", subtype: "session_state_changed"`)

Opt-in via `CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1`.

```json
{
  "type": "system",
  "subtype": "session_state_changed",
  "state": "idle",
  "uuid": "state-uuid",
  "session_id": "session-uuid"
}
```

States: `idle` (turn complete, ready for input), `running` (processing), `requires_action` (waiting for permission).

**This is the authoritative "turn over" signal** — when state becomes `idle`, the process is ready for a new user message.

#### Task Notifications (Background Agents)

```json
{
  "type": "system",
  "subtype": "task_started",
  "task_id": "task-123",
  "tool_use_id": "toolu_01...",
  "description": "Running code review",
  "task_type": "agent",
  "uuid": "...",
  "session_id": "..."
}
```

```json
{
  "type": "system",
  "subtype": "task_notification",
  "task_id": "task-123",
  "status": "completed",
  "output_file": "/tmp/task-output.json",
  "summary": "Found 3 issues in auth.py",
  "uuid": "...",
  "session_id": "..."
}
```

---

## 3. Message Types (Input — stdin)

### 3.1 User Messages

To submit a new prompt, write an `SDKUserMessage` as a JSON line:

```json
{"type":"user","message":{"role":"user","content":"Fix the bug in auth.py"},"parent_tool_use_id":null}
```

Full schema:

```typescript
type SDKUserMessage = {
  type: "user";
  message: MessageParam;        // Anthropic API MessageParam
  parent_tool_use_id: string | null;
  isSynthetic?: boolean;
  tool_use_result?: unknown;
  priority?: "now" | "next" | "later";
  timestamp?: string;           // ISO 8601
  uuid?: string;                // Optional UUID
  session_id?: string;          // Auto-assigned if omitted
};
```

The `message` field uses the Anthropic API `MessageParam` format:

```json
{
  "role": "user",
  "content": "Your prompt text here"
}
```

Or with structured content:

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "Analyze this image:"},
    {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "..."}}
  ]
}
```

### 3.2 Control Requests (SDK → Claude Code)

The SDK sends control messages on stdin to manage the session:

```json
{"type":"control_request","request_id":"req-123","request":{"subtype":"initialize","hooks":{},"agents":{}}}
```

Key control request subtypes:

| Subtype | Purpose |
|---------|---------|
| `initialize` | Configure hooks, agents, system prompt after launch |
| `interrupt` | Cancel current turn |
| `set_permission_mode` | Change permission mode mid-session |
| `set_model` | Change model mid-session |
| `mcp_status` | Query MCP server status |
| `mcp_set_servers` | Add/remove MCP servers dynamically |
| `stop_task` | Stop a background task |
| `rewind_files` | Restore files to a previous state |
| `seed_read_state` | Seed file read cache |
| `end_session` | Gracefully end the session |

### 3.3 Control Responses (for permission requests)

When Claude Code emits a `control_request` with `subtype: "can_use_tool"` on stdout, the SDK must respond on stdin:

**Allow:**

```json
{"type":"control_response","response":{"subtype":"success","request_id":"req-456"}}
```

**Deny:**

```json
{"type":"control_response","response":{"subtype":"error","request_id":"req-456","error":"User denied Bash access"}}
```

---

## 4. TypeScript Agent SDK Architecture

### 4.1 Subprocess Management

The SDK (`@anthropic-ai/claude-agent-sdk`) works by:

1. **Spawning Claude Code as a child process** using Node.js `child_process.spawn()`
2. **Communicating via stdin/stdout** using the stream-json protocol
3. **Managing the lifecycle** (abort, close, cleanup)

The SDK constructs the command:

```javascript
// Simplified from minified SDK source
const args = [
  process.argv[1],  // Path to Claude Code CLI script
  "--print",
  "--input-format", "stream-json",
  "--output-format", "stream-json",
  "--replay-user-messages",
  ...options.verbose ? ["--verbose"] : [],
  ...options.permissionMode ? ["--permission-mode", options.permissionMode] : [],
  "--session-id", sessionId,
];

const env = {
  ...options.env,
  // OAuth token, sandbox flags, etc.
};

const child = spawn(execPath, args, { env, cwd, stdio: ['pipe', 'pipe', 'pipe'] });
```

The SDK passes its own executable (`process.execPath` / node) with `process.argv[1]` (the CLI entry point) as the script. It does NOT shell out to `claude` directly — it runs the bundled CLI script under the same Node.js runtime.

### 4.2 Initialization Flow

After spawning the subprocess:

1. SDK reads stdout for the `system/init` message
2. SDK sends an `initialize` control request on stdin with:
   - Hook configurations (translated from callback functions to callback IDs)
   - SDK MCP server names
   - JSON schema (for structured output)
   - System prompt / append system prompt
   - Agent definitions
3. Claude Code responds with a `control_response` containing:
   - Available slash commands
   - Available agents
   - Available models
   - Account info
   - Output style

### 4.3 Message Flow

```
SDK (stdin)                          Claude Code (stdout)
──────────                           ───────────────────
                                     ← system/init
initialize control_request →
                                     ← control_response (success)
user message →
                                     ← stream_event (tokens...)
                                     ← stream_event (tokens...)
                                     ← assistant (complete turn)
                                     ← control_request (can_use_tool)
control_response (allow) →
                                     ← stream_event (more tokens...)
                                     ← assistant (final turn)
                                     ← result (success)
                                     ← system/session_state_changed (idle)
user message →                       (next turn...)
```

### 4.4 Hook Mechanism

**File-based hooks** (`.claude/settings.json`):

```json
{
  "hooks": {
    "PostToolUse": [{
      "matcher": "Edit|Write",
      "hooks": [{"type": "command", "command": "echo 'file changed'"}]
    }]
  }
}
```

**Programmatic hooks** (SDK):

The SDK translates callback functions into a callback ID system:

1. At initialization, SDK registers hook callbacks with unique IDs
2. Sends `{ subtype: "initialize", hooks: { "PostToolUse": [{ matcher: "Edit|Write", hookCallbackIds: ["cb-123"] }] } }`
3. When a hook fires, Claude Code sends a `control_request` with `subtype: "hook_callback"`, `callback_id: "cb-123"`, and the hook input data
4. SDK executes the callback function locally and sends back a `control_response`

**Key insight:** Programmatic hooks are translated to a request/response protocol over stdin/stdout. The hook functions run in the SDK process, not in Claude Code.

### 4.5 Session Management

- **Session ID**: Either auto-generated UUID or passed via `--session-id`
- **Resume**: Use `--resume <session-id>` — Claude Code replays the conversation history
- **Fork**: Use `--resume <id>` + `--fork-session` — creates a new session branching from the original
- **Session persistence**: Sessions saved as JSONL in `~/.claude/projects/<hash>/`
- **Disable persistence**: `--no-session-persistence` for ephemeral sessions

**Resume works with stream-json mode.** When resuming:

1. Claude Code loads the session from disk
2. User messages are replayed (emitted as `SDKUserMessageReplay` with `isReplay: true`)
3. The session continues from where it left off
4. New user messages on stdin are appended to the existing conversation

---

## 5. Subagent Message Flow

When Claude invokes a subagent via the Agent tool:

1. Claude emits an `assistant` message with a `tool_use` block of type `Agent`
2. The subagent runs internally within the same Claude Code process
3. All messages from the subagent carry `parent_tool_use_id` set to the Agent tool_use ID
4. Subagent assistant messages, stream events, and results all flow through stdout
5. When the subagent completes, its result is returned as a tool_result to the parent

For **background tasks** (launched via `run_in_background`):

```
← task_started  { task_id, tool_use_id, description }
← task_progress { task_id, usage, summary }  (periodic)
← task_notification { task_id, status: "completed", summary }
```

The parent conversation continues while background tasks run. The SDK's `stream()` method holds back intermediate results until background tasks complete.

---

## 6. Input Injection Behavior

**Critical finding:** The stream-json protocol supports a `priority` field on user messages:

```typescript
priority?: "now" | "next" | "later";
```

This suggests input can be queued while Claude is processing. However, based on SDK behavior:

1. The V1 `query()` API is **single-turn** — you provide one prompt, iterate over messages, then the generator completes
2. The V2 `SDKSession` API (`send()` / `stream()`) supports **multi-turn** — you can call `send()` after iterating
3. The `interrupt()` method sends a `control_request` with `subtype: "interrupt"` to cancel the current turn

**For Dendra's use case:**

- After a `result` message (turn complete), send the next user message on stdin
- The `session_state_changed` → `idle` event is the authoritative signal that Claude is ready
- You CAN send messages while Claude is processing, but they queue — the `priority` field controls ordering
- `interrupt()` can cancel a running turn

---

## 7. SDK Options → CLI Flags Mapping

| SDK Option | CLI Flag | Notes |
|------------|----------|-------|
| `systemPrompt` (string) | `--system-prompt` | Full replacement |
| `systemPrompt` (preset) | (uses default) + `--append-system-prompt` | With `append` field |
| `resume` | `--resume` | Session ID |
| `sessionId` | `--session-id` | For new sessions |
| `forkSession` | `--fork-session` | With resume |
| `continue` | `--continue` | Most recent session |
| `allowedTools` | `--allowedTools` | Space-separated |
| `disallowedTools` | `--disallowedTools` | Space-separated |
| `tools` | `--tools` | Restrict available tools |
| `permissionMode` | `--permission-mode` | default/acceptEdits/bypassPermissions/plan/dontAsk |
| `model` | `--model` | Model name or alias |
| `fallbackModel` | `--fallback-model` | Overload fallback |
| `maxTurns` | `--max-turns` | Turn limit |
| `maxBudgetUsd` | `--max-budget-usd` | Budget cap |
| `cwd` | (set via cwd of spawn) | Working directory |
| `env` | (set via env of spawn) | Environment variables |
| `agents` | `--agents` (JSON) | Subagent definitions |
| `betas` | `--betas` | Beta features |
| `includePartialMessages` | `--include-partial-messages` | Streaming tokens |
| `settingSources` | `--setting-sources` | user,project,local |
| `mcpServers` | `--mcp-config` | MCP server config |
| `effort` | `--effort` | low/medium/high/max |
| `persistSession` | `--no-session-persistence` | (negated) |
| `debug` | `--debug` | Debug mode |
| `additionalDirectories` | `--add-dir` | Extra directories |
| `hooks` | (via initialize control_request) | Not a CLI flag — programmatic only |
| `canUseTool` | (via control_request/response protocol) | Permission callback |

---

## 8. Known Issues and Considerations

### 8.1 stdout Buffering

**Issue [#25670](https://github.com/anthropics/claude-code/issues/25670):** When `claude -p --output-format stream-json` output is piped, stdout is block-buffered (4-8KB) instead of line-buffered. JSON lines accumulate in the pipe buffer and don't appear until the buffer fills or the process exits.

**Impact on Go implementation:** When reading stdout from a subprocess in Go, the OS pipe buffer may delay delivery of messages. This is mitigated by:

- The SDK communicates via the child process's stdio pipes, not shell pipes
- Go's `os/exec` connects to the child's stdout via a pipe — same behavior
- Messages will be delivered when the buffer fills OR when Claude Code flushes
- In practice, assistant messages and results flush reliably; partial streaming tokens may batch

**Mitigation:** Set `stdbuf -oL` or use pseudo-TTY, or accept slight latency for streaming tokens. For Dendra's wrapper loop, we primarily care about `result` and `session_state_changed` messages, which flush reliably.

### 8.2 `--input-format stream-json` is Underdocumented

**Issue [#24594](https://github.com/anthropics/claude-code/issues/24594):** The input format is not officially documented beyond the CLI flags table. The protocol was reverse-engineered from the SDK source code.

**Impact:** The protocol may change between versions. Pin to a specific Claude Code version in production and test upgrades.

### 8.3 Bare Mode Recommendation

Anthropic recommends `--bare` for SDK/scripted usage and plans to make it the default for `-p`. Benefits:

- Faster startup (skips hook/plugin/CLAUDE.md discovery)
- Deterministic behavior across machines
- No side effects from user or project settings

For Dendra agents, use `--bare` and pass all configuration explicitly via flags and the `initialize` control request.

---

## 9. Recommendations for Go Implementation

### 9.1 Architecture: Long-Running Process with stdin/stdout

**Replace the bash wrapper loop** with a Go-managed Claude Code subprocess:

```
┌─────────────────────────────────────────────┐
│  Go Agent Manager (per agent)               │
│                                             │
│  ┌────────────────────────────────────────┐ │
│  │ claude -p --input-format stream-json   │ │
│  │   --output-format stream-json          │ │
│  │   --verbose --bare                     │ │
│  │   --session-id dendra-<name>           │ │
│  │   --system-prompt "..."                │ │
│  │   --permission-mode bypassPermissions  │ │
│  └─────────┬──────────────┬───────────────┘ │
│     stdin ↓               ↑ stdout          │
│  ┌─────────┴──────────────┴───────────────┐ │
│  │ Go NDJSON reader/writer                │ │
│  │ - Parse stdout messages                │ │
│  │ - Send user messages on stdin          │ │
│  │ - Handle control_request (permissions) │ │
│  │ - Track session state                  │ │
│  └────────────────────────────────────────┘ │
│                                             │
│  Poll loop (check .dendra/messages/)        │
│  On new message → send user message stdin   │
└─────────────────────────────────────────────┘
```

### 9.2 Key Design Decisions

1. **Single long-lived process per agent.** Don't restart Claude Code between turns. Use stdin to inject new prompts. This preserves context and avoids cold-start overhead.

2. **Use `session_state_changed` → `idle` as the turn-complete signal.** Set `CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1` in the environment. When state is `idle`, the agent is ready for the next message.

3. **Bypass permissions** with `--permission-mode bypassPermissions`. Non-root agents run with full permissions (the system prompt constrains behavior, not permission prompts). Auto-respond to any `can_use_tool` requests with `success`.

4. **Use `--bare` mode** for faster startup and deterministic behavior. Pass all configuration via CLI flags.

5. **Session persistence** is valuable. Use `--session-id dendra-<agent-name>` and let Claude Code persist sessions. On respawn, `--resume dendra-<agent-name>` recovers full conversation history.

6. **NDJSON reader in Go.** Use `bufio.Scanner` with line splitting. Parse each line as JSON. Dispatch on `type` field. This is trivial in Go.

7. **Don't use `--include-partial-messages`** unless needed for UI. Reduces output volume significantly. For Dendra agents (no interactive UI), we only need complete turns and results.

### 9.3 Go Types (Proposed)

```go
// Core message wrapper — discriminate on Type field
type StreamMessage struct {
    Type      string          `json:"type"`
    Subtype   string          `json:"subtype,omitempty"`
    UUID      string          `json:"uuid,omitempty"`
    SessionID string          `json:"session_id,omitempty"`
    Raw       json.RawMessage `json:"-"` // Full message for type-specific parsing
}

// User message (sent on stdin)
type UserMessage struct {
    Type              string      `json:"type"` // "user"
    Message           MessageParam `json:"message"`
    ParentToolUseID   *string     `json:"parent_tool_use_id"`
}

type MessageParam struct {
    Role    string `json:"role"` // "user"
    Content string `json:"content"`
}

// Result message (received on stdout)
type ResultMessage struct {
    Type             string  `json:"type"`    // "result"
    Subtype          string  `json:"subtype"` // "success" or "error_*"
    Result           string  `json:"result,omitempty"`
    DurationMs       int     `json:"duration_ms"`
    IsError          bool    `json:"is_error"`
    NumTurns         int     `json:"num_turns"`
    TotalCostUsd     float64 `json:"total_cost_usd"`
    Errors           []string `json:"errors,omitempty"`
}

// Control request (permission prompt, received on stdout)
type ControlRequest struct {
    Type      string              `json:"type"`       // "control_request"
    RequestID string              `json:"request_id"`
    Request   ControlRequestInner `json:"request"`
}

type ControlRequestInner struct {
    Subtype  string `json:"subtype"` // "can_use_tool", "hook_callback", etc.
    ToolName string `json:"tool_name,omitempty"`
    Input    any    `json:"input,omitempty"`
}

// Control response (sent on stdin)
type ControlResponse struct {
    Type     string                `json:"type"` // "control_response"
    Response ControlResponseInner  `json:"response"`
}

type ControlResponseInner struct {
    Subtype   string `json:"subtype"`    // "success" or "error"
    RequestID string `json:"request_id"`
    Error     string `json:"error,omitempty"`
}

// Session state (received on stdout)
type SessionStateChanged struct {
    Type      string `json:"type"`    // "system"
    Subtype   string `json:"subtype"` // "session_state_changed"
    State     string `json:"state"`   // "idle", "running", "requires_action"
    SessionID string `json:"session_id"`
}
```

### 9.4 Agent Loop Pseudocode (Go)

```go
func RunAgent(agentName, systemPrompt, initialPrompt string) error {
    sessionID := "dendra-" + agentName

    // Spawn Claude Code
    cmd := exec.Command("claude",
        "-p",
        "--input-format", "stream-json",
        "--output-format", "stream-json",
        "--verbose",
        "--bare",
        "--session-id", sessionID,
        "--system-prompt", systemPrompt,
        "--permission-mode", "bypassPermissions",
    )
    cmd.Env = append(os.Environ(),
        "DENDRA_AGENT_IDENTITY="+agentName,
        "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1",
    )

    stdin, _ := cmd.StdinPipe()
    stdout, _ := cmd.StdoutPipe()
    cmd.Start()

    // Read init message
    scanner := bufio.NewScanner(stdout)
    scanner.Scan()
    // Parse system/init...

    // Send initial prompt
    sendUserMessage(stdin, initialPrompt)

    // Main loop
    for scanner.Scan() {
        msg := parseMessage(scanner.Bytes())

        switch {
        case msg.Type == "control_request":
            // Auto-approve all tool use
            sendControlResponse(stdin, msg.RequestID, "success")

        case msg.Type == "result":
            // Turn complete. Check for new messages.
            handleResult(msg)

        case msg.Type == "system" && msg.Subtype == "session_state_changed":
            if msg.State == "idle" {
                // Ready for next turn. Check inbox.
                if hasNewMessages(agentName) {
                    sendUserMessage(stdin,
                        "You have new messages. Check your inbox with: dendra messages inbox")
                }
                // Otherwise, poll periodically
            }
        }
    }

    return cmd.Wait()
}
```

### 9.5 Advantages Over Bash Wrapper Loop

| Aspect | Bash Wrapper (original design) | Go + stream-json (proposed) |
|--------|-------------------------------|----------------------------|
| Process lifecycle | Start/stop Claude per turn | Single long-lived process |
| Context | Relies on --resume (cold restart) | Live process, no restart needed |
| Latency | ~2-5s startup per wake cycle | ~0ms (already running) |
| Message delivery | Poll files, then restart Claude | Send on stdin immediately |
| Permission handling | --dangerously-skip-permissions | Respond to control_request protocol |
| Error handling | Exit code only | Rich error messages in stream |
| Observability | tmux scrollback only | Structured JSON events |
| Cost tracking | Not available | Per-turn cost in result messages |
| Complexity | Simple bash script | Moderate Go code |

### 9.6 Migration from Current Design

1. **Phase 1:** Implement Go NDJSON reader/writer (`internal/agent/stream.go`)
2. **Phase 2:** Implement Go agent process manager (`internal/agent/process.go`)
3. **Phase 3:** Update `cmd/spawn.go` to use new process manager instead of direct claude invocation
4. **Phase 4:** Implement message-triggered wake (stdin injection) instead of file polling
5. **Phase 5:** Remove bash wrapper script plan entirely

### 9.7 Open Questions for Implementation

1. **Should we use `--resume` on process crash/restart?** If the Claude Code process dies (OOM, crash), we can restart it with `--resume dendra-<name>` to recover conversation history. The stream-json mode supports this.

2. **How to handle `--bare` vs loading CLAUDE.md?** With `--bare`, project CLAUDE.md files are not loaded. We can pass them via `--append-system-prompt-file` or include their content in the system prompt. Alternatively, use `--setting-sources project` instead of `--bare`.

3. **Should we track token usage?** The `result` message includes `total_cost_usd` and `usage`. We could accumulate this per agent in the state file for budget management.

4. **tmux integration.** The Go process manager should still run inside tmux for observability. Claude's stdout flows through our Go process — we should tee it to the terminal (via os.Stdout) so `tmux attach` still shows live agent output.

5. **Graceful shutdown.** On `dendra kill`, send SIGTERM to the Go process. The Go process should send an `end_session` control request, wait briefly, then SIGTERM the Claude Code subprocess.

---

## Appendix A: Full SDKMessage Type Reference

| Type | Subtype | Direction | Description |
|------|---------|-----------|-------------|
| `system` | `init` | stdout | Session initialization metadata |
| `system` | `status` | stdout | Status changes (e.g., compacting) |
| `system` | `api_retry` | stdout | API retry notification |
| `system` | `session_state_changed` | stdout | Turn lifecycle (idle/running/requires_action) |
| `system` | `compact_boundary` | stdout | Context window compaction |
| `system` | `hook_started` | stdout | Hook execution started |
| `system` | `hook_progress` | stdout | Hook execution progress |
| `system` | `hook_response` | stdout | Hook execution completed |
| `system` | `local_command_output` | stdout | Slash command output |
| `system` | `task_started` | stdout | Background task started |
| `system` | `task_progress` | stdout | Background task progress |
| `system` | `task_notification` | stdout | Background task completed/failed |
| `system` | `files_persisted` | stdout | Files saved event |
| `system` | `elicitation_complete` | stdout | MCP elicitation done |
| `system` | `auth_status` | stdout | Auth status update |
| `assistant` | — | stdout | Complete assistant message |
| `user` | — | both | User message / echo |
| `result` | `success` | stdout | Turn completed successfully |
| `result` | `error_*` | stdout | Turn ended with error |
| `stream_event` | — | stdout | Token-level streaming |
| `tool_progress` | — | stdout | Long-running tool progress |
| `tool_use_summary` | — | stdout | Tool use summary |
| `rate_limit_event` | — | stdout | Rate limit info |
| `prompt_suggestion` | — | stdout | Predicted next prompt |
| `control_request` | `can_use_tool` | stdout | Permission request |
| `control_request` | `hook_callback` | stdout | Hook callback invocation |
| `control_request` | `initialize` | stdout | (response to init) |
| `control_response` | `success`/`error` | both | Control response |
| `keep_alive` | — | stdout | Connection keepalive |

## Appendix B: Control Request Subtypes (stdin → Claude Code)

| Subtype | Purpose |
|---------|---------|
| `initialize` | Configure hooks, agents, schema, prompts |
| `interrupt` | Cancel current turn |
| `set_permission_mode` | Change permission mode |
| `set_model` | Change model |
| `set_max_thinking_tokens` | Change thinking budget |
| `mcp_status` | Query MCP server status |
| `mcp_message` | Send JSON-RPC to MCP server |
| `mcp_set_servers` | Replace dynamic MCP servers |
| `mcp_reconnect` | Reconnect MCP server |
| `mcp_toggle` | Enable/disable MCP server |
| `stop_task` | Stop background task |
| `rewind_files` | Restore files to previous state |
| `seed_read_state` | Seed file read cache |
| `reload_plugins` | Reload plugins |
| `end_session` | Gracefully end session |
| `hook_callback` | (from Claude Code → SDK, response required) |
