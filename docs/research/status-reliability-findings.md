# Status Reliability & Retire Safety: Investigation Findings

**Date:** 2026-04-06
**Investigator:** brook (researcher agent)

## Incident Summary

A manager agent (summit) completed its primary task and ran `sprawl report done`. When `sprawl status` was checked, it showed:

| AGENT  | STATUS | PROCESS |
|--------|--------|---------|
| summit | done   | -       |

This was interpreted as "the process is dead." `sprawl retire --abandon` was used, destroying 6 unmerged commits. In reality, the agent was still alive — it had reported done on its first task, and a new message with follow-up work had already been sent to it.

## Root Cause Analysis

### Finding 1: Terminal statuses suppress liveness checks entirely

This is the core bug. In `internal/observe/observe.go` lines 60-64:

```go
for _, info := range result {
    if IsTerminal(info.Status) {
        continue // ProcessAlive stays nil
    }
    // ... tmux liveness check only runs for non-terminal statuses
}
```

`IsTerminal()` returns true for `"done"`, `"problem"`, and `"retiring"`. When status is terminal, `ProcessAlive` is never set — it stays `nil`.

Then in `cmd/status.go` `processDisplay()` (lines 188-199):

```go
func processDisplay(info *observe.AgentInfo) string {
    if info.ProcessAlive == nil {
        if isTerminalStatus(info.Status) {
            return "-"     // <-- THIS: shows dash instead of checking liveness
        }
        return "?"
    }
    // ...
}
```

**Result:** When an agent reports "done", the PROCESS column shows "-" without ever checking tmux. The dash is indistinguishable from "process actually exited."

### Finding 2: "done" status does NOT mean the process exited

The agent loop (`cmd/agentloop.go`) is a perpetual loop that:
1. Checks for kill sentinel → exits if found
2. Checks for poke files → delivers them
3. Checks for queued tasks → executes them
4. Checks inbox for unread messages → delivers them
5. Checks for wake files → delivers them
6. Sleeps 3 seconds → loops back to step 1

**The loop never exits on its own.** When an agent calls `sprawl report done`, it only updates the state file — the agent loop keeps running, waiting for new tasks or messages. "done" is a task-level concept being displayed as if it were a process-level concept.

### Finding 3: `retire --abandon` has no safety checks for live processes

In `cmd/retire.go`, the `runRetire` function:
- Checks for `--merge` and `--abandon` mutual exclusivity
- Loads agent state
- Optionally merges first
- Checks for children (unless `--force`)
- Marks agent as "retiring"
- Calls `agent.RetireAgent()`

With `--abandon`, the retire flow:
1. Sends a kill sentinel to the agent loop
2. Polls for 5 seconds to see if the tmux window disappears
3. Force-kills the tmux window if it doesn't
4. Removes the worktree (force-remove since abandon)
5. Deletes the state file
6. Force-deletes the branch with `git branch -D`

**There is no warning about:**
- Whether the process is actually alive (counterintuitive given "done/-" display)
- How many unmerged commits exist on the branch
- Whether there are pending messages in the agent's inbox

### Finding 4: The status model conflates task state and process state

The STATUS column has these possible values:
- `""` (empty/blank) — initial state after spawn
- `"done"` — agent called `sprawl report done`
- `"problem"` — agent called `sprawl report problem`
- `"retiring"` — retire command checkpoint (crash recovery)

None of these directly indicate process liveness. "done" means "the agent finished a task," which is often the moment a manager sends it MORE work. This is the fundamental semantic confusion that caused the incident.

## Recommendations

### R1: Always check liveness, even for terminal statuses (HIGH PRIORITY)

**Change:** Remove the `IsTerminal()` skip in `observe.go`. Always check tmux for process liveness.

**Display change:** Show compound status in the PROCESS column:
- `alive` — tmux window exists
- `DEAD` — tmux window does not exist
- `?` — tmux not available to check

This means `sprawl status` would show `done / alive` for an agent that reported done but is still running — clearly different from `done / DEAD`.

**Effort:** ~10 lines changed in `observe.go` and `status.go`.

### R2: `retire --abandon` should show unmerged commit count and require confirmation (HIGH PRIORITY)

**Change:** Before proceeding with `--abandon`, check for unmerged commits on the agent's branch and display them:

```
WARNING: Agent "summit" has 6 unmerged commits on branch dmotles/summit-feature:
  abc1234 Add initial implementation
  def5678 Fix edge case in parser
  ...

This will permanently delete these commits. Use --yes to confirm, or use --merge instead.
```

Without `--yes`, refuse to proceed. This is the direct guard that would have prevented the incident.

**Effort:** ~30-40 lines. Add a `gitLogUnmerged` dependency, call it before proceeding, gate on `--yes` flag.

### R3: `retire --abandon` should warn if process is still alive (MEDIUM PRIORITY)

**Change:** Before retiring, check tmux liveness. If the process is alive, show:

```
WARNING: Agent "summit" process is still alive in tmux.
Use --yes to confirm forced retirement, or stop the agent first.
```

This catches the case where someone misreads the status table and tries to retire a live agent.

**Effort:** ~15 lines. Add a tmux liveness check before the "retiring" checkpoint.

### R4: Add LAST REPORT timestamp to default status output (LOW PRIORITY)

Currently the LAST REPORT column shows the report type and truncated message, but not when it was reported. Adding a relative timestamp ("2m ago", "1h ago") would help operators distinguish between "just reported done" and "reported done a long time ago."

**Effort:** ~20 lines. The `LastReportAt` field already exists in state.

### R5: Consider a "waiting" status distinct from "done" (MEDIUM PRIORITY)

When an agent reports "done" on a task but the agent loop is still running and ready for more work, the status is misleading. Consider:
- After an agent reports "done", if the agent loop picks up a new task or message, automatically reset status to "working" or "active"
- Or introduce a "waiting" status for agents that reported done but whose process is still alive

This would require changes in the agent loop to update state after delivering new work.

**Effort:** ~20-30 lines in `agentloop.go`.

## Open Questions

1. **Should `retire` (without `--abandon`) also warn about unmerged commits more prominently?** Currently it refuses if unmerged, which is good, but the error message could be clearer about what to do next.

2. **Should there be a `sprawl stop` command** that gracefully stops an agent's process without retiring it? This would let operators stop a process without destroying state, which is a safer intermediate step.

3. **Race condition:** Between checking status and running retire, the agent could pick up new work. The "retiring" checkpoint and kill sentinel handle this, but there's a window where work could be lost. Is this acceptable?

4. **Should `sprawl retire --abandon` be renamed** to something more alarming like `--destroy` or `--discard`? The word "abandon" may not convey sufficient danger.

## Summary of Changes by Priority

| Priority | Rec | Description | Effort |
|----------|-----|-------------|--------|
| HIGH | R1 | Always check liveness in status | ~10 lines |
| HIGH | R2 | Unmerged commit warning + --yes gate for --abandon | ~30-40 lines |
| MEDIUM | R3 | Warn if process alive during retire --abandon | ~15 lines |
| MEDIUM | R5 | Reset status when agent gets new work | ~20-30 lines |
| LOW | R4 | Add relative timestamp to LAST REPORT | ~20 lines |

The highest-impact fix is R1 — it directly addresses the misleading display that caused the incident. R2 is the safety net that prevents data loss even when the display is misread.
