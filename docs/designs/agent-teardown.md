# Design: Agent Teardown & Cleanup

## Status: Implemented (superseded by MCP tools)

> **Implementation note (2026-05, M13 Phase 2.3):** The CLI synopses below are
> the original CLI-first proposal. The teardown surface ultimately shipped as
> MCP tools (`kill({agent})`, `retire({agent, merge?, abandon?, cascade?})`)
> driven from inside `sprawl enter`. The standalone CLI versions of these
> commands (`kill` / `retire` as cobra subcommands) were deleted in QUM-566;
> this document is kept
> as the canonical design record for the teardown semantics.

## Context

DESCRIPTION.md historically defined two teardown-adjacent commands:

- `kill` — kill an unresponsive agent (now the `kill` MCP tool)
- `respawn` — kill + restart with same session ID (never shipped; subsumed by reuse semantics)

But the system also needs full teardown: stop the process, close the tmux window, remove the worktree, delete state, and return the name to the pool. This doc designs the complete teardown surface.

## Design Decisions

### Decision 1: Two-tier teardown — `kill` (light) and `retire` (full)

`kill` and `retire` serve fundamentally different purposes:

- **`kill`** is operational. Something went wrong, stop the agent, but keep everything around so we can respawn it or inspect what happened. It's a circuit breaker.
- **`retire`** is lifecycle completion. The agent's work is done (or abandoned). Clean up everything, free the name, reclaim resources.

`respawn` depends on `kill` preserving state — it needs the session ID, agent config, and parent relationship intact. If `kill` did full teardown, `respawn` would be impossible.

**Why not `stop`/`kill`/`destroy`?** Three tiers is over-engineered for the current system. There's no meaningful distinction between "stop" and "kill" when agents are Claude Code processes — you either send a signal or you don't. And `destroy` sounds like it nukes the git branch too, which we explicitly don't want.

**Why `retire`?** It fits the agent metaphor — the agent is retired from service, its name goes back to the pool for someone new. It's unambiguous about what happens: the agent is gone, but its work (the git branch) survives.

### Decision 2: `kill` behavior

`kill` stops the agent process and closes its tmux window, but preserves all state.

**What it does:**
1. Send SIGTERM to the Claude Code process inside the tmux window
2. Wait briefly (2s) for graceful shutdown
3. If still running, SIGKILL
4. Close the tmux window
5. Update agent state file: set `status` to `"killed"`

**What it preserves:**
- Agent state file (`.sprawl/agents/<name>.json`) — intact
- Git worktree (`.sprawl/worktrees/<name>/`) — intact
- Git branch — intact
- Session ID — intact (in state file)
- Parent/child relationships — intact

**After kill, the agent can be:**
- `respawn`ed — restarts with same session ID, full conversation history
- `retire`d — fully cleaned up
- Inspected — worktree and state are still there for debugging

### Decision 3: `retire` behavior

`retire` does full teardown. The agent is removed from the system and its name returns to the pool.

**Cleanup sequence:**

```
1. Kill the process (if running)
   └─ SIGTERM → wait 2s → SIGKILL (same as `kill`)

2. Close the tmux window (if exists)
   └─ `tmux kill-window -t <session>:<window>`

3. Remove the git worktree
   └─ `git worktree remove .sprawl/worktrees/<name> --force`
   └─ Does NOT delete the branch — just detaches it

4. Delete the agent state file
   └─ Remove `.sprawl/agents/<name>.json`
   └─ Name is now available in the pool again

5. Update parent's state
   └─ Remove agent from parent's `children` list
```

**What survives retire:**
- The git branch (e.g., `sprawl/<name>`) — still exists in the repo, can be merged, inspected, or deleted manually
- Any messages already delivered to other agents' inboxes

**What does not survive:**
- The process, tmux window, worktree, state file, name allocation

### Decision 4: Children — block by default, `--cascade` to override

An agent with active children is a subtree. Retiring just the parent creates orphans with no one to report to. This is almost always a mistake.

**Default behavior:** If the agent has children that are not already retired, `retire` **refuses** and prints:

```
Error: alice has 3 active children: bob, charlie, dave
Use --cascade to retire alice and all descendants.
Use --force to retire alice only (children become orphans).
```

**`--cascade` flag:** Retires the agent and all descendants, bottom-up. Children are retired first (leaf nodes first, then their parents, then the target agent). This is the "clean shutdown of a subtree" operation.

**`--force` flag:** Overrides two safety checks: (1) ignores active children (children become orphans), and (2) discards uncommitted changes in the worktree. This is an escape hatch for broken state, not normal operation.

For `kill`, children are **not** affected. Killing a manager doesn't kill its engineers. The engineers will just get no response when they try to report — which is the correct behavior, since the manager might be respawned momentarily.

### Decision 5: `respawn` interaction

`respawn` = `kill` + relaunch with same session ID. It requires agent state to exist.

**Preconditions:**
- Agent state file must exist
- Agent must be in `"killed"` or `"running"` state (respawn on a running agent does kill-then-restart)
- Cannot respawn a `"retired"` agent (state file is gone)

**Behavior:**
1. `kill` the agent (if running)
2. Re-read the state file (still intact after kill)
3. Launch Claude Code with `--resume <session-id>` to restore conversation history
4. Open a new tmux window in the parent's children session
5. Update state: set `status` to `"running"`, update tmux window reference

### Decision 6: `--force` flag on `kill`

`kill` gets a `--force` flag for edge cases where the graceful shutdown path doesn't work:

- `kill` (default): SIGTERM → 2s wait → SIGKILL. Updates state.
- `kill --force`: SIGKILL immediately. Updates state. Also handles cases where the tmux window is wedged (uses `tmux kill-window` directly).

`retire` always gets `--force` semantics on the process-killing step (no reason to be gentle when you're tearing everything down anyway). The `--force` flag on `retire` means "ignore children" as described above.

## Command Reference

### `kill` MCP tool

Stop an agent's process but preserve all state for respawn or inspection.

```
kill({agent: "<name>"})                  # Graceful kill (SIGTERM → SIGKILL)
kill({agent: "<name>", force: true})     # Immediate SIGKILL, handle wedged state
```

**Exit codes:**
- 0: Agent killed successfully
- 1: Agent not found (no state file)
- 1: Agent already killed (not an error? — see open question)

### `retire` MCP tool

Full teardown. Stop process, close tmux, remove worktree, delete state, free name.

```
retire({agent: "<name>"})                              # Retire agent (fails if has children or dirty worktree)
retire({agent: "<name>", cascade: true})               # Retire agent + all descendants
retire({agent: "<name>", abandon: true, cascade: true}) # Override safety checks (orphan children, discard uncommitted work)
```

**Exit codes:**
- 0: Agent retired successfully
- 1: Agent not found
- 1: Agent has active children (suggest --cascade or --force)
- 1: Agent has uncommitted changes in worktree (suggest committing or --force)

### `sprawl respawn <agent-name>`

Kill and restart an agent, preserving session history.

```
sprawl respawn <agent-name>         # Kill + restart with same session ID
```

**Exit codes:**
- 0: Agent respawned successfully
- 1: Agent not found or already retired

## Agent State Transitions

```
                    ┌─────────┐
        spawn ───>  │ running │
                    └────┬────┘
                         │
                    kill │              respawn
                         │         ┌──────────────┐
                         v         │              │
                    ┌─────────┐    │              │
                    │ killed  │ ───┘   ───> running
                    └────┬────┘
                         │
                  retire │
                         v
                    ┌─────────┐
                    │ retired │ (state file deleted;
                    └─────────┘  shown here conceptually)
```

Valid transitions:
- `running` → `killed` (via `kill` or first step of `retire`)
- `killed` → `running` (via `respawn`)
- `running` → retired (via `retire`, which kills first)
- `killed` → retired (via `retire`)

## Cleanup Sequence Detail

For `retire`, the exact sequence matters for crash safety. If the process crashes mid-retire, we want to leave the system in a state where re-running `retire` finishes the job.

```
retire(agent):
    1. Read state file → get tmux session/window, worktree path, children
    2. Check children (unless --cascade or --force)
    3. If --cascade: retire(child) for each child, depth-first
    4. Kill process (SIGTERM → wait → SIGKILL)
    5. Close tmux window
    6. Mark state as "retiring" (write to state file)  ← crash-safe checkpoint
    7. Check worktree for uncommitted changes
       └─ Run `git -C <worktree> status --porcelain`
       └─ If output is non-empty AND --force is not set:
          abort with: "<name> has uncommitted changes in worktree.
                       Commit first or use --force to discard."
          (state remains "retiring" — safe to re-run after committing)
       └─ If clean or --force: proceed
    8. Remove git worktree
       └─ If clean: `git worktree remove <path>`
       └─ If --force: `git worktree remove --force <path>`
    9. Remove agent from parent's children list
   10. Delete state file  ← name is now free
```

Step 6 is the crash-safety checkpoint. If we crash after step 6 but before step 10, the agent is in `"retiring"` state. On next `retire` attempt, we skip steps 1-6 and resume from step 7. The state file is the last thing deleted because it's what tells us the agent exists.

Note that the dirty worktree check (step 7) happens *after* the process is killed (step 4). This is intentional — we don't want to check while the agent is still running and potentially making changes. The check catches work the agent did but didn't commit before being stopped.

## Edge Cases

**Agent process already dead but state says "running":**
Steps 4-5 of retire are no-ops (nothing to kill, window may already be closed). Continue with cleanup. Same for kill — update state to "killed" even if there's nothing to kill.

**Dirty worktree (uncommitted changes):**
The agent did work but didn't commit. Without the check, `git worktree remove --force` would silently destroy that work. The dirty check catches this: retire aborts, the manager (or user) can respawn the agent to commit, or use `--force` to discard. In `--cascade` mode, a dirty child worktree aborts the entire cascade (unless `--force` is also set).

**Worktree already removed:**
Step 8 of retire is a no-op. `git worktree remove` on a non-existent path is handled gracefully.

**tmux session itself is gone:**
If the entire `sprawl-<parent>-children` session is gone (e.g., tmux crashed), window cleanup is a no-op. Continue.

**Name pool is exhausted:**
Retiring agents frees names. This is the only way to reclaim names from the pool (short of manually deleting state files). Managers should retire completed engineers to free names for new work.

**Retiring the root:**
Retiring the root agent should require `cascade: true` if any agents exist, and should be treated as "shut down the entire system." This is a special case worth calling out in help text.

**Code Merger agents:**
Code Mergers are ephemeral by design — they should self-retire when their merge is complete. If a Code Merger gets stuck, it can be killed/retired like any other agent. Since they operate in the parent's worktree, retire skips the worktree removal step (they don't have their own).

## Impact on DESCRIPTION.md

The current DESCRIPTION.md mentions `kill` and `respawn`. This design adds `retire`. The CLI section should be updated:

```
kill({agent})                                  Kill an agent (preserves state for respawn)
kill({agent, force: true})                     Force-kill a wedged agent
retire({agent})                                Full teardown (fails if children or dirty worktree)
retire({agent, cascade: true})                 Retire agent and all descendants
retire({agent, abandon: true, cascade: true})  Override safety checks (orphans children, discards uncommitted work)
```

(Note: the originally-proposed `respawn` CLI was never built — agent reuse is now handled by `delegate` against an existing agent.)

## Impact on README.md

Add `retire` to the CLI reference section under "Spawn and Manage Agents."

## Open Questions

1. **Should `kill` on an already-killed agent be an error or a no-op?** Leaning toward no-op with a warning message — idempotency is friendly.

2. **Should `retire` auto-kill, or require the agent to be killed first?** This design says auto-kill (retire handles everything). Requiring a separate kill first adds friction for no safety benefit.

3. **Should retired agent names be logged somewhere?** When a state file is deleted, we lose the record that the agent ever existed. A lightweight audit log (`.sprawl/history.log`) with one line per lifecycle event could be useful for debugging but is not critical for v1.

4. **Timeout on graceful shutdown?** This design says 2 seconds. That might be too short if Claude Code is mid-operation, or too long if you're retiring 20 agents in a cascade. Could be configurable via `--timeout`, but defaulting to 2s seems reasonable for v1.
