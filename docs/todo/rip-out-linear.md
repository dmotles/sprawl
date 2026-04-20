# Rip Linear out, migrate to GitHub Issues + Projects v2

**Status:** planned — execution starts after current session-boundary work lands
**Priority:** high
**Filed:** 2026-04-20

## Problem

Linear workspace hit the free-tier issue quota mid-session; cannot file new issues. Sprawl is a public open-source project — GitHub Issues is the native, free, unlimited option that also lets external contributors file issues without a Linear invite. Keeps code and tracking on one platform.

## Plan

6 phases. Phase 0 is manual user prerequisites; Phases 2-3-5 are agent work; Phases 1 & 4 are light manual bookkeeping.

### Phase 0 — Prerequisites (user, manual)
- Verify GitHub Issues is enabled on `dmotles/sprawl` (default for public repos).
- Create **GitHub Projects v2** workspace "Sprawl" with custom statuses `Backlog / In Progress / Done` mirroring Linear.
- Install **GitHub MCP server** in Claude Code config. Anthropic's official or `github.com/github/github-mcp-server`.
- Generate GitHub PAT with `repo` scope. Store for MCP config.

### Phase 1 — Seed labels & workflow (user, one-time)
- Create labels: `priority/urgent`, `priority/high`, `priority/medium`, `priority/low`, `type/bug`, `type/feature`, `type/refactor`, `type/research`.
- Any additional workflow labels (e.g. `area/tui`, `area/memory`) as desired.

### Phase 2 — Update conventions & tooling (agent: researcher + engineer)
- Rewrite `CLAUDE.local.md` Linear section as GitHub (repo, labels to use, PAT auth note).
- Rewrite `CLAUDE.md` "Linear Issue Tracking" → "GitHub Issue Tracking."
- Replace `.claude/skills/linear-issues/SKILL.md` with `.claude/skills/github-issues/SKILL.md`. Document: repo slug, label taxonomy, the GitHub MCP tool names, issue lifecycle (open → add "In Progress" via project → comment → close when done).
- Branch naming: `dmotles/qum-NNN-…` → `dmotles/NN-…` or `dmotles/gh-NN-…`. Document in CLAUDE.local.md and the skill.

### Phase 3 — Update sprawl's internal prompts (agent: engineer)
- `internal/agent/prompt.go` + `internal/agent/prompt_child_sections.go`:
  - Weave root prompt references Linear workflow — replace with GitHub equivalents.
  - Engineer/researcher/manager child prompts reference the linear-issues skill + `get_issue`/`save_issue` — replace with github-issues skill + GitHub MCP tool names.
- `rootinit.BuildContextBlob` reads `CLAUDE.md` / `CLAUDE.local.md` at runtime, so once Phase 2 is done, most of the "how to track work" context flows through without code changes. Verify this is still true.

### Phase 4 — Migrate in-flight work (user + weave, manual)
- Identify Linear issues with status `In Progress` / `Backlog` that still matter. Expected set: QUM-260 (async FinalizeHandoff), QUM-261 (resume-failure heuristic), QUM-265 follow-ups (system prompt still references handoff skill), anything in `docs/todo/` that has accumulated.
- Port each to GitHub Issues by hand. Copy title + description + AC verbatim where possible.
- Leave the 260+ completed Linear issues in Linear archived. No bulk migration.

### Phase 5 — Sweep & remove (agent: researcher + engineer)
- `grep -r 'linear.app\|Qumulo-dmotles\|QUM-\|team.*ID' .` — classify each hit: intentional historical ref (commit messages, handoff summaries in `.sprawl/memory/`, changelog) vs stale ref to remove.
- Delete `.claude/skills/linear-issues/` if present.
- Remove Linear MCP server from `settings.json`.
- Update any remaining docs (`docs/` tree) that reference Linear workflow.

### Phase 6 — Validate (weave)
- Spawn an engineer against a test GitHub issue end-to-end. Verify lifecycle: read → In Progress → comments → Done.
- Delete this file or move to `docs/todo/done/`.

## Sequencing

Execution starts after the TUI session-boundary UX fix (currently in flight with finn) lands. User does Phase 0 + 1 manually. Researcher drafts Phase 2/3/5 edits (inventory Linear references, concrete file edits). Engineer applies. Phase 4 is manual by user. Phase 6 is weave validation.

## Open questions
- Do we want GitHub Issue templates (`.github/ISSUE_TEMPLATE/*.md`) for bug / feature / research? Probably yes, low cost, helps community contributors.
- PR template too? Probably yes — include "Fixes #N" guidance.
- Archive or downgrade Linear workspace? Free tier keeps 260+ issues readable for history. Pick when migration is validated.

## Acceptance Criteria

- [ ] Phase 0-1: GitHub Projects v2 set up, labels created, GitHub MCP installed, PAT configured.
- [ ] Phase 2: CLAUDE.md, CLAUDE.local.md, skills updated. No Linear references in active workflow docs.
- [ ] Phase 3: weave + child prompts reference GitHub MCP tools. Smoke-test: spawn an engineer, verify it reads GitHub issues correctly.
- [ ] Phase 4: in-flight issues ported.
- [ ] Phase 5: `grep` sweep clean.
- [ ] Phase 6: E2E validation through a real issue-to-merge cycle on GitHub.
