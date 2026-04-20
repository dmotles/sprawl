---
name: handoff
description: Write a session summary and hand off to the next weave session. ONLY TO BE USED BY WEAVE.
user-invocable: true
---

# Session Handoff

Use this skill at the end of a weave session to write a structured summary and hand off context to the next session. The summary you write is the **primary context** the next weave will have — make it count.

## Step 1: Write the Session Summary

Write a structured summary covering these categories:

### What was accomplished
- Commits made (reference SHAs or branch names)
- Issues closed or progressed (reference Linear issue IDs)
- Features delivered or milestones reached

### Key design decisions
- Architectural or implementation decisions made during this session
- Why each decision was made (trade-offs considered)
- Alternatives that were rejected and why

### Process observations
- What worked well (tooling, workflows, agent patterns)
- What didn't work or caused friction
- Any changes to how agents were managed or spawned

### Outstanding issues and backlog
- Work that was started but not finished
- Known bugs or issues discovered
- Backlog items that should be prioritized next

### User context
- What the user cares about (preferences, priorities observed)
- Any explicit requests or directions from the user
- Tone, style, or workflow preferences noticed

## Step 2: Trigger the Handoff

You have two ways to execute the handoff — prefer the MCP tool when available.

### Preferred: `mcp__sprawl-ops__sprawl_handoff` (TUI mode)

If `mcp__sprawl-ops__sprawl_handoff` appears in your tool list (you are running inside `sprawl enter`), call it directly:

```
mcp__sprawl-ops__sprawl_handoff({ summary: "<your full summary here>" })
```

The host will persist the summary to `.sprawl/memory/sessions/<session-id>.md`, write `.sprawl/memory/handoff-signal`, tear down this subprocess, and start a fresh weave session with consolidated memory. The new weave starts automatically — the user does **not** need to exit and re-enter `sprawl enter`.

### Fallback: `sprawl handoff` CLI (tmux mode)

If the MCP tool is unavailable (you are under the classic tmux root loop), pipe the summary via heredoc:

```bash
sprawl handoff <<'EOF'
<paste your full summary here>
EOF
```

The summary is saved and a signal file is written; the root loop restarts a fresh weave when the user exits the current session (ctrl+c / ctrl+d / `/exit`).

## Reminders

- **Be specific, not vague.** "Made progress on auth" is useless. "Implemented JWT validation in `internal/auth/`, added tests, blocked on refresh token rotation (see QUM-45)" is useful.
- **Include agent state.** If agents are still running, say which ones and what they're working on. The next weave needs to know what's in flight.
- **Reference artifacts.** Link to Linear issues, name branches, cite file paths. The next session can't search your memory — only what you write down.
- **Don't skip "what didn't work."** Process failures are some of the most valuable context for the next session.
