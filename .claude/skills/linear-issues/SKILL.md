---
name: linear-issues
description: Create, update, query, and manage Linear issues for this project. Use when creating tasks, filing bugs, planning work, setting up dependencies, or checking what's available to work on.
user-invocable: true
argument-hint: "[action] e.g. 'create', 'list blocked', 'plan milestone', or an issue ID"
---

# Linear Issue Management for Sprawl

## Workspace Context


Team name, issue prefix, and project name are defined in `CLAUDE.local.md`. Read it for the current workspace configuration.
- **Statuses**: Backlog → Todo → In Progress → Done (also: Canceled, Duplicate)
- **Labels**: Bug, Feature, Improvement
- **Priority scale**: 1=Urgent, 2=High, 3=Normal, 4=Low

## Core Principle: Write for the Implementer, Not Yourself

Issues must be written as if **someone else entirely** — a different person or agent with no prior context — will implement them. The author of the issue will not be the one doing the work. This means every issue must be self-contained and leave as little ambiguity as possible.

### Be Thorough With What You Know

The standard is rigor and thoughtfulness, not a mandatory research phase. Use judgment:

- **If you already have context** (from a conversation, prior investigation, or the user's explanation), put all of it into the issue. Don't leave useful knowledge in your head — the implementer won't have it.
- **If the issue IS the research** (e.g., "Investigate whether X approach is viable"), that's fine — write a clear issue describing what to investigate and what the deliverable is.
- **If you're writing higher-level planning issues** that a downstream agent will break down, you don't need to fill in every implementation detail — that's the agent's job. But still be clear about the goal, constraints, and what "done" looks like at that level.
- **If you have enough information to be specific, be specific.** Don't write vague issues when you could write precise ones. File paths, function names, API details — if you know them, include them.

The key question is: **could someone pick this up and either do the work or know exactly what to investigate, without needing to ask clarifying questions?**

### Required Fields

Every issue must have:

1. **Title** — a concise, imperative action phrase. Good: "Add timeout to agent spawn." Bad: "Agent spawning issue" or "we need to fix the thing with agents."
2. **Description** — structured markdown (see Description Structure below).
3. **Labels** — at least one of: `Bug`, `Feature`, `Improvement`.
4. **Priority** — default to 3 (Normal) unless there's a reason not to.

### Description Structure

Every issue description must include these sections:

#### Context & Motivation
Why does this issue exist? What problem does it solve or what need does it address? Explain the "why" thoroughly — this is the most important part. A reader should understand the purpose without needing to ask questions.

#### Implementation Details
Concrete, specific information the implementer needs:
- **File locations and names** — exact paths to files that need to change (e.g., `cmd/merge.go`, `internal/agentops/merge.go`)
- **Functions and types** — specific functions, interfaces, or types involved (e.g., "the `mergeDeps` alias in `cmd/merge.go`")
- **API contracts and integration points** — how this interacts with other components, what interfaces it needs to satisfy
- **URLs and external resources** — if there are docs, APIs, or references the implementer should consult, link them directly
- **Scope boundaries** — what is explicitly NOT included in this issue

Not every issue will have all of these, but include everything that's relevant. Err on the side of too much context rather than too little.

#### Acceptance Criteria
A clear checklist that defines "done." Each criterion should be:
- **Verifiable** — someone unfamiliar with the issue could check whether it's met
- **Specific** — not "it works" but "calling `spawn({timeout_seconds: 30, ...})` kills the agent process after 30 seconds"

**Critically, include how to validate the work.** If there's a way the implementer can exercise the feature to prove correctness — a CLI command to run, a specific scenario to test, a UI flow to verify via browser automation — describe it. The implementer should know not just what to build but how to confirm it works.

#### Testing Expectations
Implementation is expected to follow TDD practices — testing is part of the work, not an afterthought. You don't need to write an exhaustive test plan in every issue, but:
- If there are specific edge cases worth calling out, mention them
- If the issue is specifically about fixing or improving tests, be detailed about what test coverage is expected
- The implementer should understand that tests are expected as part of delivery

#### Non-Code Issues
For issues that aren't about code changes (process, research, documentation, configuration):
- Be equally thorough in explaining what needs to happen
- Define clear deliverables — what artifact or outcome marks this as done
- Minimize ambiguity — if there are decisions to be made, either make them in the issue or explicitly flag them as decisions the implementer needs to make

### Issue Type Guidelines

**Bug** — Something is broken or behaving incorrectly.
- Title: "Fix [what's wrong] in [where]"
- Description must include: steps to reproduce, expected behavior, actual behavior
- Include the specific files/functions where the bug manifests if known

**Feature** — New capability that doesn't exist yet.
- Title: "Add [capability] to [component]"
- Description must include: user-facing behavior, scope boundaries (what's explicitly NOT included)
- Include integration points with existing code

**Improvement** — Enhancement to existing functionality.
- Title: "Improve [what] in [where]" or "Refactor [what] for [why]"
- Description must include: what exists today, what will be better after, and why the change matters

## Dependency Tracking

Use `blocks` and `blockedBy` fields when creating/updating issues to express ordering constraints.

- If issue A must be done before issue B can start: A `blocks` B (or equivalently, B is `blockedBy` A).
- Relations are **append-only** via MCP — you can add but not remove them.
- When planning work, always check: does this issue depend on something that isn't done yet?

When filing a batch of related issues, wire up dependencies at creation time. Create issues in dependency order (blockers first) so you have their IDs to reference in `blockedBy` on dependent issues. Do not file a set of sequenced issues without connecting them — disconnected issues lose the ordering context that motivated the breakdown.

### Finding Available Work

To find issues that are ready to work on (unblocked):

1. List issues: `list_issues` with `project: "<project from CLAUDE.local.md>"`, `state: "Todo"` or `state: "Backlog"`
2. For each issue, `get_issue` to check if it has `blockedBy` relations
3. Issues with no `blockedBy` (or all blockers in Done state) are available

## Milestones

Use milestones to group issues into meaningful project phases. Milestones live inside the Sprawl project.

- Create milestones with `save_milestone` (requires `project: "<project from CLAUDE.local.md>"`)
- Assign issues to milestones via the `milestone` field on `save_issue`
- Milestones can have target dates

When creating issues as part of a milestone plan, always assign them to the milestone via the `milestone` field at creation time. Issues filed for a milestone but not linked to it create confusion about what work is actually tracked.

## Sub-Issues

For large issues, break them into sub-issues using `parentId` on `save_issue`.

- The parent should describe the overall goal
- Each sub-issue should be independently completable
- Sub-issues inherit the parent's project automatically

## MCP Tool Reference

### Creating an issue
```
save_issue:
  title: "Add timeout to agent spawn"
  description: "## Context\n..."
  team: "<team from CLAUDE.local.md>"
  project: "<project from CLAUDE.local.md>"
  labels: ["Feature"]
  priority: 3
  state: "Todo"
```

### Setting dependencies
```
save_issue:
  id: "<PREFIX>-10"
  blockedBy: ["<PREFIX>-8", "<PREFIX>-9"]
```

### Creating a milestone
```
save_milestone:
  project: "<project from CLAUDE.local.md>"
  name: "v0.2 - Multi-agent coordination"
  description: "Core features for running multiple agents"
  targetDate: "2026-05-01"
```

### Assigning issue to milestone
```
save_issue:
  id: "<PREFIX>-10"
  milestone: "v0.2 - Multi-agent coordination"
```

### Querying
- `list_issues` — filter by project, state, label, assignee, priority
- `get_issue` — full details including relations and description
- `list_milestones` — all milestones in a project
- `list_comments` — discussion on an issue

## Conventions

- Always assign to the project configured in `CLAUDE.local.md` — never create orphan issues
- Default to `state: "Backlog"` for new issues unless they're immediately actionable (use "Todo")
- When closing an issue via code change, update its state to "Done" and leave a comment linking the commit/PR
- When an issue is blocked, add a comment explaining what it's waiting on

## Recording Progress While Working an Issue

**The issue is the work record.** There is no status-reporting tool: QUM-1186
deleted `report_status`, and its habit was deliberately not moved onto
`send_message`. Sprawl observes whether an agent is alive and in a turn; nobody
needs to be told an agent is still working, and a parent's inbox is not a
progress log.

So: comment on the Linear issue at each milestone — picked it up, tests red,
tests green, `make validate` green, decisions taken, blockers hit — and close it
out with a summary. That record outlives the session and the agent, and your
parent (or the user) can read it without asking you.

Message your parent only when they have something to act on: your work is ready
to merge, you are blocked on something only they can unblock, or you need a
decision. Keep it short — `send_message` caps `body` at 300 characters by hard
error — and put the detail on the issue, sending the issue key.

## Messaging Tools (when you need to talk to another agent)

The agent-facing messaging surface is MCP-only:

- `send_message` — the channel for substantive agent-to-agent communication:
  questions, context-sharing, findings, hand-offs. Durable — it lands in the
  recipient's inbox and can be retrieved later. It is the ONLY way to make
  another agent receive text — there is no separate work-assignment tool, so an
  assignment is a message. `now: false` (the default) is cooperative;
  `now: true` requests preemption, which is **rare** and reserved for genuinely
  urgent parent->descendant corrections ("I forgot to tell you: use the other
  API"). `body` is capped at 300 characters, enforced by a hard error rather
  than truncation — put the detail on the issue and send the key.
  Consult the tool's own MCP schema for its current arguments and semantics.
- `peek` — inspect the activity sprawl has OBSERVED from a child/peer. It no
  longer returns a last-report block; agents do not report on themselves.
  **Use this before** sending a child "are you done?" — only `send_message`
  if peek is inconclusive.
