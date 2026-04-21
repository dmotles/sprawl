package commands

// HandoffPromptTemplate is injected as a user message when the user selects
// /handoff from the TUI command palette. It replaces the role of
// .claude/skills/handoff/SKILL.md for `sprawl enter` (TUI mode) and is
// portable to any repo — no filesystem skill file required.
const HandoffPromptTemplate = `The user invoked /handoff from the sprawl enter command palette. ` +
	`Consolidate this session now.

## Step 1: Write the session summary

Cover these categories. Be specific, not vague — the summary is the primary ` +
	`context the next weave will have.

### What was accomplished
- Commits made (reference SHAs or branch names)
- Issues closed or progressed (reference issue IDs from your tracker, e.g. #42 or PROJ-123)
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

## Step 2: Persist via the MCP tool

Call mcp__sprawl-ops__sprawl_handoff with the full summary as the ` + "`summary`" + ` string argument. After the tool returns, end your turn — ` +
	`the host will tear down this subprocess and start a fresh weave session ` +
	`with consolidated memory.

## Reminders
- Include agent state. If agents are still running, say which ones and what ` +
	`they're working on.
- Reference artifacts. Link to tracking issues, name branches, cite file paths.
- Don't skip "what didn't work." Process failures are high-value context.
`
