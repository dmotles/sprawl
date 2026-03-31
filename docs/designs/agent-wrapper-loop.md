# Design: Non-Interactive Agent Wrapper Loop

## Status: Draft

## Context

Currently, non-root agents are launched as **interactive Claude Code sessions** inside tmux windows. When messages need to be delivered (e.g., a parent sending instructions, a child reporting back), the system would use `tmux send-keys` to type into the interactive session. This is fragile:

- Messages can land in the middle of Claude processing, corrupting input
- Messages can be dropped or partially delivered
- There's no clean boundary between "agent is working" and "agent is idle"
- The interactive session has no awareness of the dendra messaging system
- Timing-dependent behavior is inherently unreliable

Meanwhile, DESCRIPTION.md already describes the desired agent lifecycle as a **wake/work/sleep loop** (see "Agent Lifecycle" section). This design implements that architecture.

## Proposed Architecture

Replace the interactive Claude Code session with a **wrapper loop** that runs inside the tmux window. The wrapper manages the agent's lifecycle by alternating between running Claude Code in non-interactive mode (`-p` / print mode) and polling for new work.

```
┌─────────────────────────────────────────────────┐
│  tmux window (dendra-root-children:alice)        │
│                                                   │
│  ┌─────────────────────────────────────────────┐ │
│  │  dendra-agent-loop (bash script)            │ │
│  │                                             │ │
│  │  1. claude -p --resume $SID "$initial_msg"  │ │
│  │     ↳ claude runs, outputs visible in tmux  │ │
│  │     ↳ claude exits when done                │ │
│  │                                             │ │
│  │  2. Poll for new messages/signals           │ │
│  │     ↳ check .dendra/ state files            │ │
│  │     ↳ sleep between checks                  │ │
│  │                                             │ │
│  │  3. On new message:                         │ │
│  │     claude -p --resume $SID "You have new   │ │
│  │       messages. Check your inbox."           │ │
│  │     ↳ back to step 2                        │ │
│  │                                             │ │
│  │  4. Loop until killed/retired               │ │
│  └─────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────┘
```

### Key Properties

- **tmux window stays** -- it's the observability layer. You `tmux attach` and watch the agent work in real time.
- **All Claude output is visible** -- because claude runs in the foreground of the wrapper script, its stdout/stderr flows to the tmux pane.
- **Clean message delivery** -- messages are never injected into a running session. They're picked up during the poll phase, after Claude has exited.
- **Session continuity** -- every invocation uses `--resume <session-id>`, so the agent retains full conversation history across wake cycles.
- **Root stays interactive** -- this only applies to child agents (engineers, managers, researchers, testers, code-mergers).

## Design Decisions

### Decision 1: Bash script, not Go binary

The wrapper loop should be a **bash script** (`scripts/dendra-agent-loop.sh`), not a compiled Go binary.

**Rationale:**
- The loop is trivial: run a command, check a file, sleep, repeat. Bash excels at this.
- The script runs *inside* the tmux window -- it needs to be a transparent passthrough for Claude's output. Bash gives us `exec`-like transparency; a Go binary would add a layer.
- Debugging is easier: you can `cat` the script, edit it live, add `echo` statements.
- No compilation step means no circular dependency (the build system doesn't need to produce a binary for the wrapper before agents can run).
- The heavy logic (message checking, state management) already lives in the `dendra` CLI. The wrapper just calls `dendra` commands.

**The script is NOT a user-facing command.** It's an internal implementation detail invoked by `dendra spawn`. Users never run it directly.

### Decision 2: File-based polling with signal-assisted wake

The wrapper polls for new messages by checking the agent's inbox directory. But pure polling means latency (up to one full poll interval before the agent notices a message). To reduce latency, we combine polling with a **signal file** (or a FIFO/named pipe) that allows immediate wake.

**Polling mechanism:**

```bash
INBOX_DIR="$DENDRA_ROOT/.dendra/messages/$AGENT_NAME/inbox"
WAKE_FILE="$DENDRA_ROOT/.dendra/agents/$AGENT_NAME.wake"

while true; do
    # Check for new messages
    if has_new_messages "$INBOX_DIR"; then
        wake_claude "You have new messages. Check your inbox."
        continue
    fi

    # Check for wake signal (from dendra messages send, dendra report, etc.)
    if [ -f "$WAKE_FILE" ]; then
        reason=$(cat "$WAKE_FILE")
        rm -f "$WAKE_FILE"
        wake_claude "$reason"
        continue
    fi

    sleep "$POLL_INTERVAL"
done
```

**Wake file protocol:**
- When `dendra messages send <agent>` delivers a message, it also writes a `.wake` file for the target agent: `.dendra/agents/<name>.wake`
- The wake file contains a human-readable reason string (e.g., "You have new messages. Check your inbox." or "Your child bob reported done.")
- The wrapper checks for this file each poll cycle and deletes it after reading
- If multiple signals arrive between polls, only the last one is kept (which is fine -- the prompt just tells Claude to check its inbox)

**Why not inotifywait / fswatch?**
- Not universally available; adds a dependency
- The poll interval is short (2-3 seconds), so latency is acceptable
- The wake file provides near-instant response for the common case
- Simplicity wins for v1

**Why not a named pipe (FIFO)?**
- Named pipes block on write if no reader -- if the agent is mid-run (Claude is executing), the writer would hang
- File-based signaling is simpler and non-blocking
- Can upgrade to FIFO or Unix socket later if needed

**Poll interval:** 3 seconds default. Configurable via environment variable `DENDRA_POLL_INTERVAL`.

### Decision 3: Session ID management

Each agent gets a deterministic session ID derived from its name: `dendra-<agent-name>` (e.g., `dendra-alice`). This is set at spawn time and stored in the agent state file.

**On first run:**
```bash
claude -p --resume "$SESSION_ID" \
    --system-prompt "$SYSTEM_PROMPT" \
    --name "$SESSION_ID" \
    --dangerously-skip-permissions \
    "$INITIAL_PROMPT"
```

**On subsequent wakes:**
```bash
claude -p --resume "$SESSION_ID" \
    --dangerously-skip-permissions \
    "$WAKE_MESSAGE"
```

Note: `--system-prompt` is only passed on the first invocation. Claude Code's `--resume` restores the full conversation context including the system prompt from the first run.

The `-p` flag (print mode) is critical: it makes Claude run non-interactively, process the prompt, and exit. Without it, Claude would enter interactive mode and the wrapper loop would hang.

### Decision 4: Changes to LaunchOpts and BuildArgs

The current `LaunchOpts` struct and `BuildArgs` method build arguments for an interactive Claude session. For the wrapper loop, we need to support non-interactive (`-p`) mode and `--resume`.

**Add to `LaunchOpts`:**
```go
type LaunchOpts struct {
    // ... existing fields ...
    PrintMode  bool   // -p flag: non-interactive, print-and-exit
    ResumeID   string // --resume <session-id>
}
```

**Update `BuildArgs`:**
```go
if opts.PrintMode {
    args = append(args, "-p")
}
if opts.ResumeID != "" {
    args = append(args, "--resume", opts.ResumeID)
}
```

### Decision 5: Changes to spawn command

The `runSpawn` function currently builds a shell command that directly runs `claude` with arguments. Instead, it should invoke the wrapper script, passing configuration via environment variables.

**Current (spawn.go line ~155):**
```go
shellCmd := fmt.Sprintf("cd %s && %s",
    tmux.ShellQuote(worktreePath),
    tmux.BuildShellCmd(claudePath, claudeArgs))
```

**Proposed:**
```go
wrapperPath := filepath.Join(dendraRoot, "scripts", "dendra-agent-loop.sh")
shellCmd := fmt.Sprintf("cd %s && %s",
    tmux.ShellQuote(worktreePath),
    tmux.ShellQuote(wrapperPath))
```

**Configuration passed via environment variables** (added to the `env` map):

| Variable | Value | Purpose |
|---|---|---|
| `DENDRA_AGENT_IDENTITY` | agent name | Already exists |
| `DENDRA_ROOT` | repo root | Already exists |
| `DENDRA_SESSION_ID` | `dendra-<name>` | Claude --resume session ID |
| `DENDRA_SYSTEM_PROMPT` | system prompt text | Passed on first run only |
| `DENDRA_INITIAL_PROMPT` | initial task prompt | First wake message |
| `DENDRA_CLAUDE_PATH` | path to claude binary | So wrapper doesn't need to find it |
| `DENDRA_POLL_INTERVAL` | `3` (seconds) | Poll interval, configurable |
| `DENDRA_SKIP_PERMISSIONS` | `1` | Whether to pass --dangerously-skip-permissions |

Using environment variables (rather than command-line arguments to the script) avoids shell quoting issues with the system prompt, which can contain quotes, newlines, and special characters.

### Decision 6: Wrapper script behavior on Claude exit codes

Claude Code can exit with different codes. The wrapper should handle them:

| Exit Code | Meaning | Wrapper Behavior |
|---|---|---|
| 0 | Success | Normal -- enter poll loop |
| 1 | Error | Log error, enter poll loop (agent can be woken to retry) |
| Other | Unknown | Log, enter poll loop |

The wrapper **never exits on its own** (except on SIGTERM/SIGKILL from `dendra kill`). Even if Claude errors, the wrapper stays alive so the agent can be woken again. This is important: a transient error shouldn't permanently kill an agent.

### Decision 7: Impact on kill and retire

**kill:** No changes needed. `dendra kill` already:
1. Finds PIDs in the tmux window (`ListWindowPIDs`)
2. Sends SIGTERM/SIGKILL
3. Kills the tmux window

This works identically whether the window is running an interactive Claude session or the wrapper script. The wrapper is just a bash process -- SIGTERM will kill it and any child `claude` process.

**retire:** No changes needed for the same reason. Retire calls kill first, then cleans up state.

**respawn:** Needs a small change. Currently respawn would relaunch an interactive session. With the wrapper, respawn should relaunch the wrapper script (which will use `--resume` to pick up the existing session). The wrapper's first-run vs subsequent-run logic handles this naturally: if a session ID already has history, `--resume` picks it up.

### Decision 8: Observability and logging

Since the wrapper runs inside tmux, all output is visible to anyone watching:

```
[dendra-agent-loop] Starting agent alice (session: dendra-alice)
[dendra-agent-loop] Running: claude -p --resume dendra-alice "You have been assigned a task..."
... claude output appears here ...
[dendra-agent-loop] Claude exited (code 0). Entering poll loop.
[dendra-agent-loop] Polling for messages (interval: 3s)...
[dendra-agent-loop] Wake signal received: "You have new messages. Check your inbox."
[dendra-agent-loop] Running: claude -p --resume dendra-alice "You have new messages. Check your inbox."
... claude output appears here ...
```

The `[dendra-agent-loop]` prefix makes it easy to distinguish wrapper output from Claude output when scrolling through tmux history.

## Wrapper Script Pseudocode

```bash
#!/usr/bin/env bash
set -euo pipefail

# Configuration from environment
AGENT_NAME="${DENDRA_AGENT_IDENTITY:?}"
DENDRA_ROOT="${DENDRA_ROOT:?}"
SESSION_ID="${DENDRA_SESSION_ID:?}"
CLAUDE_PATH="${DENDRA_CLAUDE_PATH:?}"
SYSTEM_PROMPT="${DENDRA_SYSTEM_PROMPT:-}"
INITIAL_PROMPT="${DENDRA_INITIAL_PROMPT:?}"
POLL_INTERVAL="${DENDRA_POLL_INTERVAL:-3}"
SKIP_PERMISSIONS="${DENDRA_SKIP_PERMISSIONS:-0}"

INBOX_DIR="$DENDRA_ROOT/.dendra/messages/$AGENT_NAME/inbox"
WAKE_FILE="$DENDRA_ROOT/.dendra/agents/$AGENT_NAME.wake"

log() { echo "[dendra-agent-loop] $*"; }

# Build base claude args
build_claude_args() {
    local prompt="$1"
    local is_first="$2"
    local args=("-p" "--resume" "$SESSION_ID")

    if [ "$is_first" = "true" ] && [ -n "$SYSTEM_PROMPT" ]; then
        args+=("--system-prompt" "$SYSTEM_PROMPT")
        args+=("--name" "$SESSION_ID")
    fi

    if [ "$SKIP_PERMISSIONS" = "1" ]; then
        args+=("--dangerously-skip-permissions")
    fi

    args+=("$prompt")
    echo "${args[@]}"
}

run_claude() {
    local prompt="$1"
    local is_first="${2:-false}"
    log "Running claude (session: $SESSION_ID)"

    # Run claude in foreground so output is visible in tmux
    local exit_code=0
    "$CLAUDE_PATH" -p --resume "$SESSION_ID" \
        $([ "$is_first" = "true" ] && [ -n "$SYSTEM_PROMPT" ] && \
          echo "--system-prompt \"$SYSTEM_PROMPT\" --name $SESSION_ID") \
        $([ "$SKIP_PERMISSIONS" = "1" ] && echo "--dangerously-skip-permissions") \
        "$prompt" || exit_code=$?

    log "Claude exited (code $exit_code)"
    return 0  # wrapper never fails
}

has_new_messages() {
    # Check if inbox directory has unread messages
    [ -d "$INBOX_DIR" ] && [ "$(ls -A "$INBOX_DIR" 2>/dev/null)" ]
}

# --- Main Loop ---

log "Starting agent $AGENT_NAME (session: $SESSION_ID)"

# First run: execute initial task
run_claude "$INITIAL_PROMPT" "true"

# Clear any wake signals that arrived during first run
rm -f "$WAKE_FILE"

log "Entering poll loop (interval: ${POLL_INTERVAL}s)"

while true; do
    # Check for wake signal (highest priority -- explicit notification)
    if [ -f "$WAKE_FILE" ]; then
        reason=$(cat "$WAKE_FILE" 2>/dev/null || echo "You have new notifications.")
        rm -f "$WAKE_FILE"
        log "Wake signal: $reason"
        run_claude "$reason"
        continue
    fi

    # Check for new messages in inbox
    if has_new_messages; then
        log "New messages detected in inbox"
        run_claude "You have new messages. Check your inbox with: dendra messages inbox"
        continue
    fi

    sleep "$POLL_INTERVAL"
done
```

**Note:** The actual implementation will need more careful handling of `--system-prompt` quoting. The pseudocode above is illustrative. The real script will use arrays properly to avoid word-splitting issues.

## Changes Required

### New Files

| File | Description |
|---|---|
| `scripts/dendra-agent-loop.sh` | The wrapper loop script |

### Modified Files

| File | Change |
|---|---|
| `internal/agent/claude.go` | Add `PrintMode` and `ResumeID` fields to `LaunchOpts`; update `BuildArgs` |
| `cmd/spawn.go` | Generate session ID, invoke wrapper script instead of raw claude command, pass config via env vars |
| `internal/state/state.go` | Add `SessionID` field to `AgentState` |

### Messaging system integration

When `dendra messages send` delivers a message, it should also write a wake file:

```go
// After writing message to inbox:
wakeFile := filepath.Join(dendraRoot, ".dendra", "agents", targetAgent+".wake")
os.WriteFile(wakeFile, []byte("You have new messages. Check your inbox."), 0644)
```

Similarly, `dendra report` (child reporting to parent) should write a wake file for the parent:

```go
wakeFile := filepath.Join(dendraRoot, ".dendra", "agents", parentName+".wake")
reason := fmt.Sprintf("Your child %s reported: %s", agentName, reportType)
os.WriteFile(wakeFile, []byte(reason), 0644)
```

This ensures that message delivery triggers a near-immediate wake, rather than waiting up to `POLL_INTERVAL` seconds.

## Agent State Transitions (Updated)

```
                    ┌──────────┐
        spawn ───>  │ starting │  (wrapper launched, first claude run)
                    └────┬─────┘
                         │
                    claude exits
                         │
                         v
                    ┌──────────┐  ◄── wake signal/message ──┐
                    │ sleeping │                              │
                    └────┬─────┘  ── claude -p --resume ──> │
                         │                                   │
                         │           ┌──────────┐           │
                         │           │ working  │ ──────────┘
                         │           └──────────┘
                    kill │
                         v
                    ┌──────────┐
                    │  killed  │ ──── respawn ───> starting
                    └────┬─────┘
                         │
                  retire │
                         v
                    ┌──────────┐
                    │ retired  │ (state file deleted)
                    └──────────┘
```

Note: The `starting`, `sleeping`, and `working` states are conceptual -- the wrapper knows which state it's in, but the agent state file may simply remain `"active"` throughout. Tracking fine-grained states in the state file is a nice-to-have for v2 (e.g., for a dashboard UI).

## Migration Path

1. **Phase 1:** Implement the wrapper script and update `spawn` to use it. All new agents get the wrapper. Existing interactive sessions are unaffected (there are none in a fresh system).

2. **Phase 2:** Implement the wake file protocol in `dendra messages send` and `dendra report`. Until this is done, agents rely on polling (3s latency, which is fine).

3. **Phase 3:** (Optional) Add fine-grained state tracking (`sleeping`/`working`) to the state file for observability.

## Edge Cases

**Claude hangs and never exits:**
The wrapper can't proceed to the poll phase until Claude exits. The `dendra kill` command handles this -- it sends SIGTERM/SIGKILL to all processes in the tmux window, which includes both the wrapper and Claude. After respawn, the wrapper starts fresh.

Future improvement: add a configurable timeout to the wrapper (e.g., `DENDRA_RUN_TIMEOUT=600`) that kills the Claude process if it runs longer than N seconds. Not needed for v1.

**Multiple wake signals arrive while Claude is running:**
Only the last `.wake` file contents are preserved (each write overwrites). This is fine -- the wake message is just a nudge to check inbox/notifications, not the message itself. The actual messages are in the inbox.

**Wake file written between poll check and sleep:**
There's a small race window where a wake file could be written after the wrapper checks for it but before `sleep` starts. In the worst case, the agent sleeps for one poll interval before noticing. This is acceptable for v1. A FIFO or `inotifywait` would eliminate this race but adds complexity.

**System prompt too large for environment variable:**
Linux environment variable limits are typically 128KB per variable and ~2MB total. System prompts are unlikely to exceed this. If they do, we can write the system prompt to a temp file and pass the path instead.

**Agent spawned while system is shutting down:**
The wrapper should handle SIGTERM gracefully -- kill the child Claude process (if running) and exit cleanly. A simple `trap` handler covers this.

## Open Questions

1. **Should the wrapper write its PID to a file?** This would let `dendra kill` target the wrapper specifically, rather than relying on tmux's `list-panes` PID discovery. Not critical since the current approach works, but could be cleaner.

2. **Should there be a max wake cycles limit?** If an agent gets into a loop (keeps waking, doing nothing useful, sleeping), a circuit breaker could prevent runaway API usage. Probably a v2 concern -- managers should notice unproductive agents.

3. **Should the wrapper log to a file in addition to stdout?** tmux scrollback is finite. A log file (`.dendra/logs/<agent>.log`) would preserve full history. Nice-to-have for debugging but not critical for v1.

4. **Should `--resume` be optional on first run?** If the session doesn't exist yet, Claude Code may create it automatically. Need to verify Claude Code's behavior when `--resume` is given a non-existent session ID. If it errors, the wrapper should omit `--resume` on first run and start using it from the second invocation onward.
