# Agent Resume After Weave Restart

**Date:** 2026-04-29
**Author:** trace (research agent)
**Question:** What would it take to make child agents survive a weave ctrl+c and resume when weave restarts?

## Executive Summary

The core building blocks for agent resume already exist: agent state files persist session IDs, branches, worktrees, and task queues to disk. Claude Code supports `--resume <session-id>` for session resumption. The main gap is **orchestration** — nothing in the supervisor reconnects to previously-running agents on startup. This is a **medium-sized effort** (1-2 weeks), not a weekend project, because the plumbing touches the supervisor, agentloop, rootinit, and shutdown paths, and the resume-failure edge cases need careful handling.

---

## 1. What Already Exists

### 1.1 Agent State Persistence (`.sprawl/agents/{name}.json`)

Each agent's state file contains everything needed to reconstruct it:

| Field | Purpose | Resumption relevance |
|-------|---------|---------------------|
| `name` | Agent identity | Required for re-registration |
| `type` | Role (engineer, researcher) | Required for prompt building |
| `parent` | Parent agent name | Required for tree reconstruction |
| `session_id` | Claude Code session UUID | **Key for resume** — passed to `--resume` |
| `branch` | Git branch name | Worktree already on this branch |
| `worktree` | Absolute worktree path | Survives restart (on disk) |
| `status` | Lifecycle state (active/done) | Determines which agents to resume |
| `prompt` | Initial task pointer | Needed if session resume fails |
| `tree_path` | Hierarchical position (e.g. `weave├trace`) | Required for supervisor tree |
| `last_report_*` | Last status update | Context for manager |

Example from a live agent (`trace.json`):
```json
{
  "name": "trace",
  "type": "researcher",
  "session_id": "d7c5fa30-b96a-47c1-97e1-7e95ac1ea567",
  "branch": "dmotles/research-agent-resume",
  "worktree": "/home/coder/sprawl/.sprawl/worktrees/trace",
  "status": "active",
  "parent": "weave"
}
```

### 1.2 Claude Code Session Resumption

The Claude CLI supports `--resume <session-id>`, and Sprawl already uses it for weave:

- **`internal/claude/launch.go`**: `BuildArgs()` emits `--resume <ID>` when `Resume == true`, omitting `--session-id` and `--system-prompt-file` (Claude rejects both together).
- **`internal/claude/resumewatch.go`**: Detects resume failure via stderr marker `"No conversation found with session ID:"`, kills the subprocess, and returns `ErrResumeFailed`.
- **`internal/rootinit/init.go`**: Weave's `Prepare()` checks `last-session-id` file and decides resume vs. fresh. Children don't use this path.

### 1.3 Child Crash Recovery (Within-Session)

Children already have in-session resume logic in `internal/agentloop/runner.go`:

```go
func (r *Runner) restartWithResume(ctx context.Context, observer Observer) error {
    resumeSpec := r.sessionSpec
    resumeSpec.Resume = true
    proc, err = StartBackendProcess(ctx, r.deps, resumeSpec, observer)
    // ...
}
```

This is called after process crashes during poke delivery, task execution, interrupt flush, inbox delivery, and wake cycles. It sets `Resume = true` on the same session spec and relaunches. **This proves the resume mechanism works for children** — it just isn't wired up for cross-restart recovery.

### 1.4 Git Worktrees

Worktrees are plain directories on disk. They survive any process death. Uncommitted changes, staged files, and branch state all persist. No recovery action needed.

### 1.5 Message Queues

Each agent's message queue (`queue/pending/`, `queue/delivered/`) is persisted to disk as individual JSON files with a file-based lock. Messages survive restart. No recovery action needed for the queue itself.

### 1.6 Task Queues

Agent tasks are persisted in `.sprawl/agents/{name}/tasks/` as JSON files with status tracking (queued/working/done). These survive restart.

---

## 2. The Gaps

### Gap 1: Supervisor Doesn't Discover Existing Agents on Startup

**Current behavior:** `supervisor.NewReal()` creates an empty `RuntimeRegistry`. Agents only enter the registry when explicitly spawned via `Spawn()`. The `Status()` method does scan disk for agent state files, but only for display — it doesn't create runtimes.

**What's needed:** A new `RecoverAgents()` method (or extension of `NewReal()`) that:
1. Scans `.sprawl/agents/` for agents with `status: active`
2. Filters to agents whose `parent` is the current weave identity
3. Creates `AgentRuntime` entries in the registry for each
4. Launches them with `Resume = true`

### Gap 2: No "Resume Mode" in Runtime Launcher

**Current behavior:** `runtime_launcher.go`'s `StartRunner()` always builds a fresh `SessionSpec` from agent state with `Resume = false`. The only resume path is the crash-recovery `restartWithResume()` within an already-running runner.

**What's needed:** `StartRunner()` (or a new `ResumeRunner()`) must accept a flag to start with `Resume = true` on the initial session spec, skipping the initial prompt delivery.

### Gap 3: No Graceful Shutdown State Transition

**Current behavior:** On shutdown (`supervisor.Shutdown()`), all runtimes are stopped and agent state is set to `"killed"`. There's no distinction between "weave is restarting, agent should resume" and "agent was intentionally terminated."

**What's needed:** A new state like `"suspended"` or `"paused"` that means "was running, should resume on next weave start." The shutdown path should use this state instead of `"killed"` for non-explicit shutdowns.

### Gap 4: Resume Failure Fallback for Children

**Current behavior:** Weave has sophisticated resume failure handling: if the Claude session doesn't exist anymore (e.g., session expired, conversation pruned), it detects the failure within 5 seconds and falls back to a fresh session. Children have no equivalent startup-level fallback.

**What's needed:** The child resume path must handle `ErrResumeFailed`:
1. Detect the "No conversation found" marker (already exists in `resumewatch.go`)
2. Fall back to a fresh session with the original prompt
3. The fresh session should include context about what the agent was doing (from `last_report_message`, task queue state, etc.)

### Gap 5: Rootinit Doesn't Apply to Children

**Current behavior:** `rootinit.Prepare()` is weave-only. It manages `last-session-id`, session summaries, and the resume/fresh decision tree. Children bypass this entirely — their session is managed by the agentloop.

**What's needed:** Either:
- (a) Extend rootinit to work for children (more complex, may not fit the architecture), OR
- (b) Add equivalent resume decision logic directly in the agentloop's `StartRunner()` path (preferred — keeps child session management local to the agentloop)

### Gap 6: Signal Handling Doesn't Distinguish Restart from Kill

**Current behavior:** `cmd/enter.go` handles SIGTERM/SIGHUP by quitting the TUI, then calling `supervisor.Shutdown()`. Ctrl+C triggers the same path via Bubble Tea's built-in handler. All paths lead to the same shutdown — no way to signal "I'm coming back."

**What's needed:** The signal handler should:
1. On SIGINT (ctrl+c): Set children to `"suspended"` state, not `"killed"`
2. Persist a "weave was interrupted" marker so the next `sprawl enter` knows to attempt recovery
3. Optionally: distinguish between first ctrl+c (graceful) and second ctrl+c (force)

---

## 3. Implementation Path

### Phase 1: State Model (Small, Low Risk)

1. Add `"suspended"` as a valid agent status in `internal/state/`
2. Modify `supervisor.Shutdown()` to set active agents to `"suspended"` instead of `"killed"`
3. Add a `"weave-interrupted"` marker file to `.sprawl/` (or a field in weave's own state)

### Phase 2: Resume Launcher (Medium, Core Change)

4. Add a `ResumeRunner()` function (or a `resume` flag to `StartRunner()`) in `internal/agentloop/` that:
   - Sets `Resume = true` on the initial `SessionSpec`
   - Skips initial prompt delivery
   - Wraps stderr with `MarkerWriter` for resume failure detection
5. Add resume failure fallback: on `ErrResumeFailed`, restart with a fresh session and a "you were previously working on X" context prompt
6. Wire this through `runtime_launcher.go` so the supervisor can call it

### Phase 3: Supervisor Recovery (Medium, Orchestration)

7. Add `RecoverAgents()` to the supervisor that:
   - Scans for agents with `status: "suspended"` and `parent: "weave"`
   - Creates `AgentRuntime` entries in the registry
   - Calls the resume launcher for each
8. Call `RecoverAgents()` during `sprawl enter` startup, after supervisor initialization
9. Add error handling: if an agent can't be resumed, log it and set status to `"failed"` or `"killed"`

### Phase 4: UX Polish (Small, Important)

10. Show resumed agents in the TUI with a visual indicator (e.g., a "resumed" badge)
11. Add a `sprawl enter --no-resume` flag to skip agent recovery
12. Log resume attempts and outcomes for debugging
13. Handle edge case: agent marked `"suspended"` but worktree was deleted

---

## 4. Key Risks and Hard Problems

### Risk 1: Claude Session Expiration (HIGH)

Claude Code sessions may not live forever. If the user ctrl+c's weave, goes to lunch, and comes back 2 hours later, the session might be gone. The `resumewatch.go` failure detection handles this, but the fallback experience matters: the agent restarts with no memory of what it was doing.

**Mitigation:** Build a "resumption context" from persisted state — last report message, task queue, activity log — and inject it as the initial prompt for the fresh session. This won't be perfect but gives the agent enough context to pick up approximately where it left off.

### Risk 2: Partial State on Hard Kill (MEDIUM)

If the user sends SIGKILL or the machine crashes, the graceful shutdown path doesn't run. Agent state stays `"active"` (not `"suspended"`), and the supervisor won't know these agents need recovery.

**Mitigation:** On startup, treat `"active"` agents with no running process as equivalent to `"suspended"`. The supervisor can check `ProcessAlive` (which it already does in `Status()`) and resume any `"active"` agent that isn't actually running.

### Risk 3: Concurrent State Writes (LOW-MEDIUM)

During shutdown, the supervisor writes state for each child. If the process is killed mid-write, a state file could be corrupted. The current implementation writes entire JSON files atomically (via `state.SaveAgentState()`), but this should be verified.

**Mitigation:** Ensure state writes use write-to-temp-then-rename pattern. Verify this exists or add it.

### Risk 4: Resume Changes Conversation Flow (MEDIUM)

When Claude resumes a session, it picks up mid-conversation. But the agent's internal state machine (agentloop) also has state: was it waiting for a task? Processing a poke? Flushing interrupts? The agentloop state is in-memory and lost on restart.

**Mitigation:** The agentloop should resume in a known state — the "idle/polling" state. On resume, it should check for pending tasks and messages, effectively re-entering its main loop. This is already close to what happens after crash recovery.

### Risk 5: Message Ordering After Resume (LOW)

Messages sent to an agent while it was suspended are queued on disk. When the agent resumes, it needs to process these in order. The current queue implementation uses sequence numbers, so ordering should be preserved. Verify this works when the queue grows during suspension.

---

## 5. Effort Estimate

| Phase | Effort | Risk |
|-------|--------|------|
| Phase 1: State Model | 1-2 days | Low |
| Phase 2: Resume Launcher | 3-4 days | Medium |
| Phase 3: Supervisor Recovery | 2-3 days | Medium |
| Phase 4: UX Polish | 1-2 days | Low |
| **Total** | **~1.5-2 weeks** | **Medium** |

This is **not a weekend project**. The core mechanism (resume launcher + supervisor recovery) is straightforward, but the edge cases (resume failure fallback, hard kill recovery, conversation context reconstruction) require careful design and testing. Each phase is independently shippable, so it can be delivered incrementally.

---

## 6. Reflections

### Surprising Findings

- **Children already have resume logic** — the `restartWithResume()` function in the agentloop handles crash recovery with `Resume = true`. This is 60% of the work already done. The missing piece is triggering it on startup rather than only after a crash within an active session.
- **The state model is richer than expected.** Session IDs, task queues, message queues, activity logs — all persisted. The disk state is sufficient to reconstruct an agent's context without any new persistence work.
- **Weave's own resume is sophisticated** — the `rootinit.Prepare()` → `resumewatch` → fallback-to-fresh pipeline is well-engineered and handles edge cases. Children can borrow this pattern.

### Open Questions

1. **How long do Claude Code sessions live?** If there's a TTL (e.g., 24 hours), that bounds the useful resume window. I didn't find documentation on this in the codebase.
2. **Should suspended agents auto-resume, or should the user confirm?** There's a UX question about whether `sprawl enter` should silently resume 5 agents or ask first.
3. **What about agents that were mid-tool-use?** If Claude was in the middle of a file edit or bash command when killed, does the resumed session know the tool call was interrupted? This is a Claude Code behavioral question.
4. **Should there be a maximum suspension duration?** After some threshold (e.g., 1 hour), it might be better to start fresh than resume a stale session.
5. **How does this interact with handoff?** If weave does a proper handoff (session summary), should children also get a mini-handoff, or is raw resume sufficient?

### What I'd Investigate Next

- **Claude Code session TTL**: Empirically test how long a session can be resumed after the process exits. Try 5 min, 30 min, 2 hours, 24 hours.
- **Mid-tool-use resume behavior**: Kill an agent during a `Bash` tool call and see what Claude does on resume. Does it retry? Does it know the call was interrupted?
- **Prototype Phase 2**: The resume launcher is the riskiest piece. A prototype that resumes a single child after weave restart would validate the approach quickly.
- **Memory/context compaction**: On a fresh fallback (resume failed), how much context from the activity log should be injected? Too little and the agent is lost; too much and it wastes tokens.
