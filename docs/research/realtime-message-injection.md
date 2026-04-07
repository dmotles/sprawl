# QUM-170: Real-Time Message Injection into Running Claude Agent Sessions

## Problem Statement

Messages sent to sprawl agents (via `sprawl messages send` or `sprawl messages broadcast`) are delivered to the agent's mailbox immediately, but the running Claude Code session is never notified mid-turn. Agents only see messages when they voluntarily poll their inbox or when the agent loop iterates between turns. This means message delivery latency is unpredictable.

The existing mechanisms (wake files, poke files) only trigger **between** agent loop iterations, not **during** an active Claude Code turn. A turn can last minutes while Claude executes a chain of tool calls.

## Current Architecture

### How Sprawl Launches Claude Code

Sprawl launches Claude Code as a subprocess in `--print` mode with bidirectional NDJSON streaming:

```
claude -p --input-format stream-json --output-format stream-json \
  --verbose --model opus[1m] --effort medium \
  --permission-mode bypassPermissions --session-id <uuid>
```

Key files:
- `cmd/agentloop.go` — the agent loop that dispatches prompts to Claude
- `internal/agentloop/process.go` — process lifecycle management (`Start`, `SendPrompt`, `InterruptTurn`, `Stop`)
- `internal/agentloop/real_starter.go` — subprocess spawning via `exec.CommandContext` with `StdinPipe()`/`StdoutPipe()`
- `internal/protocol/writer.go` — NDJSON writer for stdin messages
- `internal/protocol/reader.go` — NDJSON reader for stdout messages

### The NDJSON Stream-JSON Protocol

Communication uses newline-delimited JSON over stdin/stdout.

**Input messages (to Claude's stdin):**

```json
{"type":"user","message":{"role":"user","content":"your prompt here"},"parent_tool_use_id":null}
```

```json
{"type":"control_request","request_id":"interrupt-1712456789","request":{"subtype":"interrupt"}}
```

```json
{"type":"control_response","response":{"subtype":"success","request_id":"<id>"}}
```

**Output messages (from Claude's stdout):**
- `system/init` — session initialization
- `assistant` — Claude's response (text, tool_use blocks)
- `control_request` — permission/hook callbacks (auto-approved by sprawl)
- `result` — turn completion with stop_reason, num_turns, cost
- `system/session_state_changed` — idle/running state transitions
- `rate_limit_event` — rate limit status

### The Agent Loop Flow

The agent loop in `cmd/agentloop.go` runs a polling loop:

1. Check for kill sentinel
2. Check for poke file (immediate high-priority message)
3. Check for queued tasks
4. Check inbox for unread messages
5. Check for wake file
6. Sleep 3 seconds and repeat

Each step that finds work calls `SendPrompt()`, which blocks until Claude returns a `result` message. During that blocking call, the loop cannot process new messages.

### Existing Interrupt Mechanism: Poke Files

Sprawl already has a mid-turn interrupt mechanism via **poke files**:

- A `.poke` file written to `.sprawl/agents/<name>.poke` triggers `InterruptTurn()` on the running process
- `sendPromptWithInterrupt()` runs a background goroutine that polls for poke files every 500ms
- When found, it sends `InterruptTurn()` which writes a `control_request` with `subtype: "interrupt"` to Claude's stdin
- Claude finishes its current turn (emitting a `result`), then the poke content is delivered as a new `SendPrompt()` on the next loop iteration

This same goroutine also polls the inbox and logs received messages (but does not deliver them until the turn ends).

---

## Approaches Investigated

### 1. Direct Stdin Injection via Stream-JSON (VIABLE - Already Implemented)

**Mechanism:** Write a `UserMessage` JSON line to the Claude subprocess's stdin pipe while a turn is running.

**Finding:** This is essentially what `SendPrompt()` does — it writes a user message to stdin and waits for a result. However, the current architecture strictly serializes this: `SendPrompt()` blocks until the current turn's `result` arrives before a new message can be sent.

**Can we inject a user message mid-turn?** The stream-json protocol appears to support queuing: you can write a user message to stdin while Claude is still processing a previous turn. However, the behavior is **undocumented** (GitHub issue [anthropics/claude-code#24594](https://github.com/anthropics/claude-code/issues/24594) — closed as "not planned"). Observed behavior:

- Claude Code processes one user turn at a time
- A user message written mid-turn would be **queued** until the current turn completes
- It would NOT interrupt the current tool execution chain

**Verdict:** The stdin pipe is already plumbed and accessible. Writing to it mid-turn is technically possible but won't interrupt the current turn — the message queues. To actually interrupt, you must use the interrupt control_request first (which sprawl's poke mechanism already does).

**Risk:** Sending a user message while a turn is in progress without first interrupting could lead to unpredictable ordering if Claude's internal queue has race conditions. The protocol is undocumented for this use case.

### 2. InterruptTurn + Follow-Up Message (VIABLE - Partially Implemented)

**Mechanism:** Send an interrupt control_request to end the current turn, then immediately send a new user message.

**Finding:** This is exactly what the poke file mechanism does today, but with a polling-based trigger. The flow is:

1. Write poke file to `.sprawl/agents/<name>.poke`
2. Background goroutine detects it within 500ms
3. Calls `proc.InterruptTurn(ctx)` which sends `{"type":"control_request","request_id":"interrupt-<ts>","request":{"subtype":"interrupt"}}`
4. Claude finishes current turn and emits a `result`
5. `SendPrompt()` returns with the result
6. Poke content is delivered as a new `SendPrompt()` in the next loop iteration

**Latency:** Up to 500ms for the poll interval + time for Claude to finish current API call + time to complete the interrupt. In practice, 1-5 seconds.

**Improvement opportunity:** Instead of polling for poke files, the `sendPromptWithInterrupt()` goroutine could use:
- `inotify`/`fsnotify` for file-watch-based triggers (sub-millisecond detection)
- A channel/signal from the message send command
- A Unix socket or named pipe for direct notification

**Verdict:** This approach works today. The main improvement is reducing the polling interval or switching to event-driven notification.

### 3. Claude Code Channels (VIABLE - New Official Feature)

**Mechanism:** Claude Code Channels (research preview, v2.1.80+) allow MCP servers to push events directly into a running Claude Code session. Events arrive as `<channel source="...">` tags in Claude's context, even mid-turn.

**How it works:**
1. A channel is an MCP server declaring `capabilities.experimental['claude/channel']`
2. It communicates with Claude Code over stdio (spawned as subprocess)
3. It pushes events via `mcp.notification({ method: 'notifications/claude/channel', params: { content: '...', meta: {...} } })`
4. Events arrive in Claude's context wrapped in `<channel>` tags
5. Two-way channels can expose reply tools

**Key details:**
- Events arrive **during** a running session, not just between turns
- Channels can be two-way (Claude can reply back)
- Permission relay is supported (remote tool approval)
- Requires claude.ai authentication (not API key)
- Research preview — protocol may change

**How sprawl could use this:**
- Build a sprawl channel MCP server that listens for messages (via Unix socket, HTTP, or file watch)
- When `sprawl messages send` delivers a message, it notifies the channel server
- The channel server pushes the message into the running Claude session
- Claude sees it immediately as a `<channel source="sprawl-inbox">` event

**Verdict:** This is the most architecturally clean approach for real-time message delivery. It's the official Anthropic mechanism for pushing events into running sessions. However, it's in research preview, requires claude.ai auth (not API key), and adds complexity (MCP server subprocess).

**Limitations:**
- Research preview — API may change
- Requires claude.ai login (not Console/API key auth)
- Team/Enterprise orgs must explicitly enable channels
- Custom channels need `--dangerously-load-development-channels` during preview
- Adds another subprocess to manage

### 4. tmux send-keys (NOT VIABLE for subprocess agents)

**Mechanism:** Use `tmux send-keys` to type text into the agent's tmux pane.

**Finding:** This approach is fundamentally incompatible with how sprawl launches agents.

- Sprawl agents run Claude in `--print` mode as a subprocess with piped stdin/stdout
- The tmux pane's stdin is connected to the **agent loop process** (`sprawl agent-loop`), not directly to Claude
- `tmux send-keys` would type into the agent loop's shell, not into Claude's stdin pipe
- Even if it reached Claude's stdin, it would inject raw text, not properly formatted NDJSON
- Risk of corrupting a partially-written JSON line or interrupting a tool call mid-flight
- No way to ensure atomic message delivery

**For the root agent (interactive mode):** `tmux send-keys` could theoretically work since the root loop runs Claude interactively (not `--print` mode). But it would simulate keyboard input, which is fragile and can't guarantee proper formatting.

**Verdict:** Not viable. The subprocess stdin pipe model makes tmux send-keys irrelevant for agent processes. Even for interactive sessions, it's too fragile.

### 5. Unix Signals (LIMITED VIABILITY)

**Mechanism:** Send a Unix signal (e.g., SIGUSR1) to the agent loop process to trigger an inbox check.

**Finding:**
- The agent loop already handles SIGTERM/SIGINT for graceful shutdown
- A custom signal handler for SIGUSR1 could set a flag that triggers the interrupt flow
- This avoids filesystem polling — signals are delivered immediately
- However, signals carry no payload — you'd still need the message in the mailbox

**Implementation sketch:**
```go
sigusr1 := make(chan os.Signal, 1)
signal.Notify(sigusr1, syscall.SIGUSR1)
go func() {
    for range sigusr1 {
        // Write poke file or trigger interrupt directly
        writePoke(pokePath, "Check your inbox — new message arrived")
    }
}()
```

**Verdict:** Viable as a lightweight notification mechanism to trigger an interrupt, but the poke file is still needed for the actual content. Signals are immediate (no polling delay) but require knowing the PID of the agent loop process.

### 6. Named Pipes / Unix Domain Sockets (VIABLE)

**Mechanism:** Create a named pipe or Unix socket for each agent. The message sender writes to it; the agent loop reads from it.

**Named pipe approach:**
- Create `/tmp/sprawl-<agent>.pipe` as a FIFO
- The `sendPromptWithInterrupt` goroutine reads from the pipe instead of polling files
- `sprawl messages send` writes to the pipe after delivering to mailbox
- Immediate delivery, no polling

**Unix socket approach:**
- Agent loop listens on a Unix domain socket
- Message sender connects and sends notification
- Supports bidirectional communication and multiple concurrent senders

**Verdict:** More robust than file polling but adds complexity. Unix sockets are the better option (support multiple senders, don't block on missing readers). However, this is a significant architectural change compared to improving the existing poke mechanism.

### 7. File System Watchers (inotify/fsnotify) (VIABLE)

**Mechanism:** Replace the 500ms polling loop with an OS-level file watch on the poke file path.

**Finding:**
- Go's `fsnotify` package wraps `inotify` (Linux) / `kqueue` (macOS)
- Can watch for file creation at a specific path
- Near-instant notification when a file appears
- Minimal code change: replace `time.Ticker` in `sendPromptWithInterrupt` with `fsnotify.Watcher`

**Verdict:** Lowest-effort improvement to the existing mechanism. Reduces latency from up to 500ms to near-instant while keeping the same poke file interface.

---

## Comparison Matrix

| Approach | Latency | Interrupts Mid-Turn? | Complexity | Stability | Recommended? |
|---|---|---|---|---|---|
| **Poke file (current)** | ~500ms polling | Yes (via interrupt) | Already built | Stable | Baseline |
| **Poke + fsnotify** | ~instant | Yes (via interrupt) | Low (swap ticker for watcher) | Stable | **Yes - Quick win** |
| **Poke + SIGUSR1** | ~instant | Yes (via interrupt) | Low (add signal handler) | Stable | Yes - Alternative |
| **Channels (MCP)** | Real-time, in-context | Yes (native) | High (new MCP server) | Preview/Unstable | **Yes - Long term** |
| **Unix socket** | ~instant | Yes (via interrupt) | Medium (new IPC layer) | Stable | Maybe - Overkill? |
| **Direct stdin mid-turn** | Queued until turn end | No | Low | Undocumented | No |
| **tmux send-keys** | N/A | N/A | N/A | Broken by design | **No** |

---

## Recommendations

### Short Term: Improve Poke File Detection (fsnotify)

**Replace the 500ms polling interval in `sendPromptWithInterrupt` with `fsnotify`.**

This is the smallest change for the biggest latency improvement:
- Swap `time.Ticker` for `fsnotify.Watcher` watching the poke file directory
- Near-instant detection instead of up to 500ms
- Same poke file interface — no changes to `sprawl messages send` or other callers
- Well-tested Go library, works on Linux and macOS

The interrupt + re-deliver flow already works correctly. The only latency bottleneck is the polling interval.

### Medium Term: Combine SIGUSR1 + Poke

**Have `sprawl messages send` write the poke file AND signal the agent loop process.**

- Store agent loop PID in `.sprawl/agents/<name>.pid`
- `sprawl messages send` writes poke file, then sends SIGUSR1 to PID
- Agent loop handles SIGUSR1 by immediately checking for poke file
- Fallback: if signal fails (process died), poke file is still picked up by polling
- Eliminates filesystem latency entirely for the common case

### Long Term: Claude Code Channels

**Build a sprawl-inbox channel MCP server.**

When channels graduate from research preview and support API key auth:
- Build an MCP server that listens on a Unix socket for sprawl messages
- Register it as a channel so it can push events into running Claude sessions
- Messages arrive as `<channel source="sprawl-inbox" from="neo" subject="...">` tags
- Claude sees them immediately in context, even mid-turn
- Two-way: Claude can reply through the channel (replaces `sprawl messages send` for replies)

This is the most architecturally sound long-term solution but depends on channels stabilizing and supporting API key authentication.

---

## Key Findings Summary

1. **The existing poke + interrupt mechanism is fundamentally sound.** The architecture is correct — interrupt the current turn, then deliver the message. The only weakness is the 500ms polling interval.

2. **Claude Code's stream-json protocol supports multi-turn on a single subprocess.** You can send multiple `SendPrompt()` calls to the same process. This is already how sprawl works — the process persists across the agent loop's iterations.

3. **Mid-turn user message injection queues, it doesn't interrupt.** Writing a user message to stdin while a turn is running will be queued until the turn finishes. To interrupt, you must send the interrupt control_request first.

4. **Claude Code Channels are the official answer to this problem.** They were designed specifically for "push events into a running session." However, they're in research preview with auth limitations that make them unsuitable for immediate adoption in sprawl's architecture.

5. **tmux send-keys is not viable.** The subprocess model with piped stdin makes tmux irrelevant.

6. **The biggest bang-for-buck improvement is fsnotify on the poke file directory.** This reduces detection latency from 500ms to near-instant with minimal code changes.

---

## Open Questions

1. **What happens if you write a user message to stdin during an active turn?** The protocol is undocumented for this case. Testing would be needed to confirm whether messages queue cleanly or cause errors.

2. **Will Claude Code Channels support API key authentication in the future?** Currently requires claude.ai login, which sprawl agents don't use.

3. **What's the actual interrupt latency?** When `InterruptTurn()` is called, how long does Claude take to emit the result? This depends on whether it's mid-API-call or mid-tool-execution.

4. **Should the poke mechanism carry structured data?** Currently it's a plain text file. If we want to distinguish "check your inbox" from "here's an urgent task", the poke content could be JSON.

5. **Can channels work in `--print` mode?** The docs show channels with interactive sessions. It's unclear whether `--print` mode with `--input-format stream-json` supports `--channels`.
