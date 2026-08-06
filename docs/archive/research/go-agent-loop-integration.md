# Go Agent Loop: Integration Testing Strategy and Architecture

**Date:** 2026-03-31
**Author:** elm (sprawl agent)
**Milestone:** M2: Agent Wrapper Loop

---

## 1. Authentication and Environment

### How Claude Code Discovers Auth

Claude Code supports multiple authentication mechanisms, checked in this order:

1. **OAuth token** (`CLAUDE_CODE_OAUTH_TOKEN` env var) - Used in our Coder workspace
2. **API key** (`ANTHROPIC_API_KEY` env var) - Direct Anthropic API key
3. **Keychain** - OS keychain integration (skipped with `--bare`)
4. **apiKeyHelper** - Custom key helper via settings

### Our Dev Environment

In our Coder workspace, authentication uses **OAuth**:

```
CLAUDE_CODE_OAUTH_TOKEN=<set>
ANTHROPIC_MODEL=claude-opus-4-6
```

Key finding: **`--bare` mode skips OAuth and keychain reads.** It only checks `ANTHROPIC_API_KEY` or `apiKeyHelper`. Since our environment uses OAuth, `--bare` causes auth failures:

```
{"type":"assistant","error":"authentication_failed",
 "content":[{"type":"text","text":"Not logged in · Please run /login"}]}
```

### Minimal Environment for Tests

To run `claude -p` successfully in a test subprocess:

| Requirement | Details |
|---|---|
| `claude` binary | Must be on `$PATH` |
| Auth token | `CLAUDE_CODE_OAUTH_TOKEN` (inherited from parent env) |
| Working directory | Any valid directory |
| `--verbose` flag | **Required** when using `--output-format stream-json` |
| Do NOT use `--bare` | It blocks OAuth auth in our environment |

Subprocess inherits all env vars from the parent process, so auth works automatically when tests run inside a Claude Code session (which is always the case for our agents).

### Recommended Test Launch Command

```bash
claude -p \
  --output-format stream-json \
  --verbose \
  --dangerously-skip-permissions \
  --model sonnet \
  "your prompt here"
```

For bidirectional (multi-turn):

```bash
claude -p \
  --output-format stream-json \
  --input-format stream-json \
  --verbose \
  --dangerously-skip-permissions \
  --model sonnet
```

Use `--model sonnet` for integration tests to reduce cost and latency.

---

## 2. Stream-JSON Protocol: Empirical Findings

### Output Message Types

Every line on stdout is a complete JSON object (NDJSON). Observed message types:

#### `system` (init)

First message emitted. Contains session metadata.

```json
{
  "type": "system",
  "subtype": "init",
  "session_id": "uuid",
  "cwd": "/path/to/workdir",
  "tools": ["Bash", "Edit", "Read", ...],
  "mcp_servers": [{"name": "linear", "status": "connected"}],
  "model": "claude-sonnet-4-6",
  "permissionMode": "bypassPermissions",
  "apiKeySource": "none",
  "claude_code_version": "2.1.87"
}
```

#### `assistant` (model response)

Contains the model's response. May include text and/or tool use blocks.

```json
{
  "type": "assistant",
  "message": {
    "model": "claude-sonnet-4-6",
    "id": "msg_xxx",
    "role": "assistant",
    "content": [{"type": "text", "text": "hello world"}],
    "stop_reason": null,
    "usage": {"input_tokens": 3, "output_tokens": 5, ...}
  },
  "session_id": "uuid"
}
```

When a tool is invoked:

```json
{
  "type": "assistant",
  "message": {
    "content": [{
      "type": "tool_use",
      "id": "toolu_xxx",
      "name": "ToolSearch",
      "input": {"query": "...", "max_results": 1}
    }]
  }
}
```

The `error` field appears on auth failures: `"error": "authentication_failed"`.

#### `user` (tool result echo)

After Claude uses a tool, the result is echoed as a `user` message:

```json
{
  "type": "user",
  "message": {
    "role": "user",
    "content": [{"type": "tool_result", "tool_use_id": "toolu_xxx", "content": "..."}]
  }
}
```

#### `rate_limit_event`

Emitted after each API call with rate limit status:

```json
{
  "type": "rate_limit_event",
  "rate_limit_info": {
    "status": "allowed",
    "resetsAt": 1774976400,
    "rateLimitType": "five_hour",
    "overageStatus": "allowed"
  }
}
```

#### `result` (turn complete)

Final message for a turn. Contains aggregated usage and cost.

```json
{
  "type": "result",
  "subtype": "success",
  "is_error": false,
  "result": "the final text output",
  "stop_reason": "end_turn",
  "duration_ms": 2423,
  "num_turns": 1,
  "total_cost_usd": 0.064702,
  "session_id": "uuid"
}
```

#### `stream_event` (with `--include-partial-messages`)

Token-by-token streaming events. Mirrors the Anthropic API streaming format:

```json
{"type": "stream_event", "event": {"type": "message_start", ...}}
{"type": "stream_event", "event": {"type": "content_block_start", "index": 0, ...}}
{"type": "stream_event", "event": {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "hello"}}}
{"type": "stream_event", "event": {"type": "content_block_stop", "index": 0}}
```

These appear **before** the aggregated `assistant` message. Useful for real-time display in tmux panes.

### Input Message Format

When using `--input-format stream-json`, send NDJSON on stdin:

```json
{
  "type": "user",
  "message": {
    "role": "user",
    "content": "Your prompt text"
  },
  "parent_tool_use_id": null,
  "session_id": null
}
```

**Critical:** The `message` field with `role` and `content` is required. Sending just `{"type":"user","content":"..."}` fails with: `TypeError: undefined is not an object (evaluating '_.message.role')`.

### Multi-Turn Behavior

Key findings from empirical testing:

1. **Multi-turn works.** After receiving a `result` message, you can send another `user` message on stdin. Claude processes it with full conversation history.
2. **Session ID is preserved.** The same `session_id` appears in all messages across turns.
3. **A new `system/init` message is emitted** at the start of each turn (not just the first).
4. **Context is maintained.** In our test, Claude correctly recalled "42" from turn 1 when asked in turn 2.
5. **Closing stdin terminates the process.** When stdin EOF is reached, claude exits cleanly.

### Cost Observations

| Operation | Model | Cost |
|---|---|---|
| Simple single-turn (5 tokens out) | opus | $0.065 |
| Simple single-turn (5 tokens out) | sonnet | $0.039 |
| Multi-turn (2 turns, minimal) | sonnet | $0.019 |

Integration tests should use `sonnet` and minimal prompts to control costs.

---

## 3. Integration Test Design

### Build Tag Strategy

```go
//go:build integration

package agentsdk_test
```

Tests tagged with `integration` are excluded from `go test ./...` and only run explicitly:

```bash
go test -tags integration ./internal/agentsdk/...
```

### Test Harness Design

```go
package agentsdk_test

import (
    "bufio"
    "context"
    "encoding/json"
    "os/exec"
    "testing"
    "time"
)

// claudeProcess wraps a claude subprocess for testing.
type claudeProcess struct {
    cmd     *exec.Cmd
    stdin   io.WriteCloser
    scanner *bufio.Scanner
    cancel  context.CancelFunc
}

// startClaude launches claude with stream-json flags.
func startClaude(t *testing.T, args ...string) *claudeProcess {
    t.Helper()
    ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

    baseArgs := []string{"-p",
        "--output-format", "stream-json",
        "--verbose",
        "--dangerously-skip-permissions",
        "--model", "sonnet",
    }
    baseArgs = append(baseArgs, args...)

    cmd := exec.CommandContext(ctx, "claude", baseArgs...)
    stdin, _ := cmd.StdinPipe()
    stdout, _ := cmd.StdoutPipe()
    scanner := bufio.NewScanner(stdout)
    scanner.Buffer(make([]byte, 1<<20), 1<<20)

    if err := cmd.Start(); err != nil {
        cancel()
        t.Fatalf("Failed to start claude: %v", err)
    }

    t.Cleanup(func() {
        stdin.Close()
        cancel()
        cmd.Wait() // Don't check error; context cancellation is expected
    })

    return &claudeProcess{cmd: cmd, stdin: stdin, scanner: scanner, cancel: cancel}
}

// readUntilResult reads messages until a "result" type is received.
func (cp *claudeProcess) readUntilResult(t *testing.T) (msgs []StreamMessage) {
    t.Helper()
    for cp.scanner.Scan() {
        line := cp.scanner.Text()
        if line == "" {
            continue
        }
        var msg StreamMessage
        if err := json.Unmarshal([]byte(line), &msg); err != nil {
            t.Logf("Unparseable line: %s", line[:min(len(line), 200)])
            continue
        }
        msgs = append(msgs, msg)
        if msg.Type == "result" {
            return msgs
        }
    }
    t.Fatal("Stream ended without result message")
    return nil
}

// sendPrompt sends a user message via stdin (for stream-json input mode).
func (cp *claudeProcess) sendPrompt(t *testing.T, prompt string) {
    t.Helper()
    msg := InputMessage{
        Type:    "user",
        Message: InputContent{Role: "user", Content: prompt},
    }
    data, _ := json.Marshal(msg)
    if _, err := fmt.Fprintf(cp.stdin, "%s\n", data); err != nil {
        t.Fatalf("Failed to send prompt: %v", err)
    }
}
```

### Example Integration Tests

```go
//go:build integration

func TestSingleTurnStreamJSON(t *testing.T) {
    cp := startClaude(t, "Say exactly 'pong' and nothing else.")

    msgs := cp.readUntilResult(t)

    // Verify we got the expected message sequence
    var gotInit, gotAssistant, gotResult bool
    for _, m := range msgs {
        switch m.Type {
        case "system":
            gotInit = true
            if m.Subtype != "init" {
                t.Errorf("Expected init subtype, got %s", m.Subtype)
            }
        case "assistant":
            gotAssistant = true
        case "result":
            gotResult = true
            if m.IsError {
                t.Errorf("Result is error: %s", m.Result)
            }
            if !strings.Contains(m.Result, "pong") {
                t.Errorf("Expected 'pong' in result, got: %s", m.Result)
            }
        }
    }

    if !gotInit || !gotAssistant || !gotResult {
        t.Errorf("Missing message types: init=%v assistant=%v result=%v",
            gotInit, gotAssistant, gotResult)
    }
}

func TestMultiTurnStreamJSON(t *testing.T) {
    cp := startClaude(t, "--input-format", "stream-json")

    // Turn 1: Store a value
    cp.sendPrompt(t, "Remember: the secret word is 'banana'. Say only 'ok'.")
    msgs1 := cp.readUntilResult(t)
    result1 := msgs1[len(msgs1)-1]
    if result1.IsError {
        t.Fatalf("Turn 1 failed: %s", result1.Result)
    }

    // Turn 2: Recall the value
    cp.sendPrompt(t, "What was the secret word? Reply with just the word.")
    msgs2 := cp.readUntilResult(t)
    result2 := msgs2[len(msgs2)-1]
    if !strings.Contains(strings.ToLower(result2.Result), "banana") {
        t.Errorf("Turn 2: expected 'banana', got: %s", result2.Result)
    }
}
```

### Test Isolation and Cost Management

1. **Each test starts a fresh claude process.** No shared state between tests.
2. **Use `--model sonnet`** to minimize cost (~$0.01-0.04 per test).
3. **Use `--dangerously-skip-permissions`** to avoid interactive prompts.
4. **60-second timeout per test** via `context.WithTimeout`.
5. **Cleanup kills the subprocess** on test failure via `t.Cleanup`.
6. **Minimal prompts** ("say X and nothing else") to reduce token usage.
7. **No `--bare`** in our environment (breaks OAuth auth).
8. **Tag with `//go:build integration`** so they never run in CI by default.
9. **Consider `--max-budget-usd 0.10`** as a safety net per test.

---

## 4. Agent Loop Architecture Sketch

### Overview

The agent loop manages a Claude Code subprocess's lifecycle. Non-root agents follow a wake/work/sleep pattern, with the loop controlling when Claude is active.

### Two Viable Approaches

#### Approach A: Persistent Process with stream-json I/O

Keep a single `claude -p --input-format stream-json --output-format stream-json` process running. Send new prompts via stdin when the agent needs to wake.

```
┌─────────────────────────────────────────────┐
│  sprawl agent-loop <name>                   │
│                                             │
│  ┌──────────┐    stdin     ┌─────────────┐  │
│  │  Go loop  │──────────►│  claude -p    │  │
│  │  manager  │◄──────────│  stream-json  │  │
│  └──────────┘   stdout    └─────────────┘  │
│       │                                     │
│       ├── Watch for wake signals            │
│       ├── Send prompts on wake              │
│       ├── Parse response stream             │
│       └── Relay output to tmux pane         │
└─────────────────────────────────────────────┘
```

**Pros:**
- Single process, no restart overhead
- Context preserved naturally (same conversation)
- Multi-turn proven to work (tested above)
- Lower latency between turns

**Cons:**
- Long-running process may accumulate memory
- Context window eventually fills up (Claude handles this with context management, but we don't control it)
- If claude crashes, we need restart logic anyway
- MCP server connections may time out during long sleep periods

#### Approach B: Restart with `--resume`

Use `claude -p --resume <session-id>` for each wake cycle. The session is persisted to disk and resumed.

```
Wake cycle:
  1. claude -p --resume <session-id> --output-format stream-json "Check inbox"
  2. Claude processes, uses tools, reports done
  3. Claude exits (result message received)
  4. Go loop waits for next wake signal
  5. Repeat from step 1
```

**Pros:**
- Clean process for each cycle (no memory accumulation)
- Natural error boundary (crash = just don't resume)
- MCP servers reconnect fresh each time
- Simpler subprocess management

**Cons:**
- Restart overhead (~2-3 seconds per wake, observed in tests)
- `--resume` with stream-json needs testing (untested in this research)
- Session persistence requires disk I/O
- Context window still fills up over many resumes

### Recommendation: Hybrid Approach

Use **Approach A (persistent process)** as the primary mode, with **Approach B (restart)** as the fallback/recovery mechanism.

```go
type AgentLoop struct {
    name      string
    sessionID string
    proc      *claudeProcess  // nil when sleeping
    inbox     <-chan Message   // wake signals
}

func (al *AgentLoop) Run(ctx context.Context) error {
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case msg := <-al.inbox:
            if err := al.wake(ctx, msg); err != nil {
                // Process died or errored: restart
                al.proc = nil
                if err := al.restart(ctx, msg); err != nil {
                    return fmt.Errorf("restart failed: %w", err)
                }
            }
        }
    }
}

func (al *AgentLoop) wake(ctx context.Context, msg Message) error {
    if al.proc == nil {
        // First wake or after restart: launch new process
        return al.start(ctx, msg)
    }
    // Persistent process: inject prompt via stdin
    return al.proc.sendPrompt(ctx, formatWakePrompt(msg))
}
```

### Key Design Decisions

#### Display in tmux pane

Use `--include-partial-messages` to get `stream_event` deltas. The Go loop can write these token-by-token to the tmux pane's stdout, giving a live "watching the agent work" experience.

```go
for msg := range stream {
    switch msg.Type {
    case "stream_event":
        // Write partial text to tmux pane stdout for live display
        delta := extractTextDelta(msg.Event)
        if delta != "" {
            fmt.Print(delta) // Goes to tmux pane
        }
    case "result":
        // Turn complete, enter sleep
        return nil
    }
}
```

#### Handling incoming messages

The Go loop watches for incoming messages (from `sprawl messages send`) via filesystem polling or inotify on the agent's inbox directory. When a message arrives during sleep, it wakes the agent by sending a prompt via stdin.

During active processing (Claude is working), new messages are queued and delivered at the next natural pause (after the current `result` message).

#### Handling Claude exit states

| Exit State | Detection | Recovery |
|---|---|---|
| Normal completion | `result` with `stop_reason: "end_turn"` | Enter sleep, wait for next wake |
| Error | `result` with `is_error: true` | Log error, report to parent, restart |
| Auth failure | `assistant` with `error: "authentication_failed"` | Fatal: report to parent |
| Process crash | `cmd.Wait()` returns non-nil error | Restart with `--resume` |
| Context overflow | Claude auto-compacts (context_management events) | Continue; or restart if too degraded |
| Rate limit | `rate_limit_event` with `status: "blocked"` | Wait until `resetsAt`, then continue |

#### Session management

- **Session ID**: Generated at agent spawn time (UUID). Passed via `--session-id` on first launch.
- **Resume**: If the process crashes, restart with `--resume <session-id>` to preserve conversation history.
- **Fork**: Use `--fork-session` when context gets too large and we want a fresh start but with a summary.

---

## 5. Prototype Results Summary

### What Worked

1. **Single-turn stream-json output**: Launching `claude -p --output-format stream-json --verbose` with a prompt on the command line. Produces clean NDJSON on stdout.

2. **Bidirectional stream-json**: Using both `--input-format stream-json` and `--output-format stream-json` with a persistent process. Sending JSON messages on stdin and reading responses on stdout.

3. **Multi-turn conversations**: After receiving a `result` message, sending another `user` message continues the conversation with full context. Claude correctly recalled information from earlier turns.

4. **Go subprocess management**: The Go prototype successfully launches claude as a subprocess, manages stdin/stdout pipes, parses all message types, and handles cleanup.

5. **Partial message streaming**: `--include-partial-messages` produces `stream_event` messages with token-by-token deltas, suitable for real-time display.

### What Didn't Work

1. **`--bare` mode with OAuth**: In our Coder environment, `--bare` skips OAuth token discovery and fails with "Not logged in". Don't use `--bare` in integration tests.

2. **Wrong input format**: Sending `{"type":"user","content":"..."}` (without the `message` wrapper) causes a parse error. The correct format requires `message.role` and `message.content`.

3. **`--output-format stream-json` without `--verbose`**: Claude Code requires `--verbose` when using `--output-format stream-json` in print mode. Without it, you get: `Error: When using --print, --output-format=stream-json requires --verbose`.

### Not Yet Tested (Deferred to Implementation)

- `--resume` with stream-json mode
- Subagent messages (`parent_tool_use_id` flow)
- Error conditions (rate limits, context overflow) in stream-json mode
- Input injection while Claude is still processing a previous prompt
- Long-running sessions (memory behavior over many turns)

---

## 6. Working Prototype

The prototype is at `docs/research/stream-json-prototype/main.go`. It demonstrates:

- **Single-turn mode**: `go run ./docs/research/stream-json-prototype/ -mode single "your prompt"`
- **Multi-turn mode**: `go run ./docs/research/stream-json-prototype/ -mode multi`

Both modes parse all message types, print human-readable summaries, and handle cleanup. The prototype's type definitions (`StreamMessage`, `InputMessage`, etc.) can serve as a starting point for the `internal/agentsdk/` package.
