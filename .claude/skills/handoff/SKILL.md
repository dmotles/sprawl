---
name: handoff
description: Write a session summary and hand off to the next sensei session.
user-invocable: true
---

# Session Handoff

Use this skill at the end of a sensei session to write a structured summary and hand off context to the next session. The summary you write is the **primary context** the next sensei will have — make it count.

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

## Step 2: Pipe the Summary into `dendra handoff`

Once your summary is written, execute the handoff by piping it via a heredoc:

```bash
dendra handoff <<'EOF'
<paste your full summary here>
EOF
```

The `dendra handoff` command will:
- Save the summary as the session file for the current session ID
- Create a handoff signal file that the sensei loop detects
- The sensei loop will then start a new session with your summary as context

## Reminders

- **Be specific, not vague.** "Made progress on auth" is useless. "Implemented JWT validation in `internal/auth/`, added tests, blocked on refresh token rotation (see QUM-45)" is useful.
- **Include agent state.** If agents are still running, say which ones and what they're working on. The next sensei needs to know what's in flight.
- **Reference artifacts.** Link to Linear issues, name branches, cite file paths. The next session can't search your memory — only what you write down.
- **Don't skip "what didn't work."** Process failures are some of the most valuable context for the next session.
