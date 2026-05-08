package commands

// HandoffPromptTemplate is injected as a user message when the user selects
// /handoff from the TUI command palette. It replaces the role of
// .claude/skills/handoff/SKILL.md for `sprawl enter` (TUI mode) and is
// portable to any repo — no filesystem skill file required.
const HandoffPromptTemplate = `The user invoked /handoff from the sprawl enter command palette. ` +
	`Consolidate this session now.

> **Safe with active children.** Handoff replaces ONLY weave's own Claude subprocess; the supervisor, runtime registry, running child agents, and inbox notifier all survive untouched. You do NOT need to wait for in-flight agents to finish — just call out what they are working on in the summary so the next weave knows what's running. This is an architectural invariant; if handoff ever kills or corrupts a child, that is a bug — file it.

## How this gets used

Your summary does **not** become the next weave's whole system prompt by itself. The runtime (` + "`internal/memory/context.go`" + `, ` + "`BuildContextBlob`" + `) assembles a structured blob and embeds your text inside it. The next weave will see, in this order:

1. ` + "`## Project Arc`" + ` — auto-generated multi-session arc summary.
2. A footer pointing at ` + "`.sprawl/memory/timeline.md`" + ` and ` + "`.sprawl/memory/sessions/<id>.md`" + `.
3. ` + "`## Last Session`" + ` — auto wrapper. The verbatim body of the summary you write goes here, under a ` + "`### Session: <id> (<timestamp>)`" + ` subheading.
4. ` + "`## Pending Inbox`" + ` — auto, only when there are unread messages; live count.
5. ` + "`## Persistent Knowledge`" + ` — auto; verbatim contents of ` + "`.sprawl/memory/persistent.md`" + `.

You write **only** the body that goes under ` + "`## Last Session`" + `. Do **not** write your own ` + "`## Project Arc`" + `, ` + "`## Pending Inbox`" + `, or ` + "`## Persistent Knowledge`" + ` sections — the runtime appends those, and any copy you author will appear twice in the next weave's prompt. Stick to the five canonical body sections in Step 1 (What was accomplished, Key design decisions, Process observations, Outstanding issues and backlog, User context).

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

Call mcp__sprawl__handoff with the full summary as the ` + "`summary`" + ` string argument. After the tool returns, end your turn — ` +
	`the host will tear down this subprocess and start a fresh weave session ` +
	`with consolidated memory.

## Reminders
- Include agent state. If agents are still running, say which ones and what ` +
	`they're working on.
- Reference artifacts. Link to tracking issues, name branches, cite file paths.
- Don't skip "what didn't work." Process failures are high-value context.
`
