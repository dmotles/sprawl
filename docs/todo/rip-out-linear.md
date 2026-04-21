# Rip Linear out, migrate to GitHub Issues + Projects v2

**Status:** planned — execution starts after current session-boundary work lands (✅ landed)
**Priority:** high
**Filed:** 2026-04-20
**Updated:** 2026-04-21 — revised to use gh CLI instead of an MCP server

## Problem

Linear workspace hit the free-tier issue quota; cannot file new issues. Sprawl is public open-source — GitHub Issues is native, free, unlimited, and lets community contributors file issues without a Linear invite.

## Approach: gh CLI, not an MCP server

The `gh` CLI is already installed (`/usr/bin/gh`, v2.89.0) and authed as `dmotles` with `repo` + `workflow` scopes. Agents shell out to `gh` via the Bash tool — same pattern as `git`. **No GitHub MCP server needed.** Structured output via `gh <cmd> --json <fields>` keeps the data flow clean.

Benefits over an MCP server: zero setup, no PAT management, same tool for CLI humans and agents, `gh api` escape hatch for GraphQL.

## Data model

- **Issue state** (open/closed) lives on the issue itself — binary.
- **Status** (Backlog / In Progress / Done) lives on a **Projects v2 custom field** — richer than Linear.
- **Priority** (Urgent / High / Medium / Low) lives as a second Project v2 custom field OR as labels. Pick one, document in the skill.
- **Type** (bug / feature / refactor / research) lives as labels.

Agents update status via `gh project item-edit --field Status --value "In Progress"` instead of Linear's `save_issue state=In Progress`. Shape is the same; plumbing differs.

## Plan

6 phases. Phase 0 is user manual (one command + project/label setup); Phases 2/3/5 are agent work; Phase 4 is user migration of in-flight issues; Phase 6 is weave validation.

### Phase 0 — Prerequisites (user, manual)

1. Add `project` scope to gh token:
   ```bash
   gh auth refresh -s project
   ```
2. Create **GitHub Projects v2** workspace for sprawl:
   ```bash
   gh project create --owner dmotles --title "Sprawl"
   ```
   Or via the GitHub UI.
3. Add custom fields to the project:
   - **Status** (single-select: Backlog / In Progress / Done)
   - **Priority** (single-select: Urgent / High / Medium / Low)
4. Link the project to the `dmotles/sprawl` repo (Project settings → Manage access → add repo).

Optional: add `read:org` scope if we ever query org membership. Not blocking.

### Phase 1 — Seed labels (user, one-time)

```bash
gh label create "type/bug" --color "d73a4a" --description "Something broken"
gh label create "type/feature" --color "a2eeef" --description "New capability"
gh label create "type/refactor" --color "fbca04" --description "Code cleanup / restructure"
gh label create "type/research" --color "5319e7" --description "Investigation / design"
```

Priority can live as labels (e.g. `priority/high`) OR as a Project v2 field. Pick one to avoid duplication. **Recommended: Project v2 field** — keeps labels focused on classification (type/area), not ordering.

### Phase 2 — Update conventions & tooling (researcher + engineer)

Files to rewrite:

- `CLAUDE.local.md` — replace the Linear section with GitHub (repo slug, branch prefix, gh CLI notes, how to query/file issues via `gh`).
- `CLAUDE.md` — "Linear Issue Tracking" section → "GitHub Issue Tracking." Reference the new skill.
- `.claude/skills/linear-issues/SKILL.md` → replace with `.claude/skills/github-issues/SKILL.md`. Document:
  - Common `gh` commands for agents (create/view/edit/comment/close issues, manage project items, list with filters)
  - JSON output shape: `gh issue view NN --json title,body,state,labels,comments`
  - Project v2 item management: `gh project item-add`, `gh project item-edit`
  - Issue lifecycle: file → add to Project → set Status=In Progress → comment progress → set Status=Done → close
  - Branch naming convention: `dmotles/<short-kebab-desc>` or `dmotles/gh-<issueNum>-<desc>` (pick one, document)

### Phase 3 — Update sprawl's internal prompts (engineer)

- `internal/agent/prompt.go` + `internal/agent/prompt_child_sections.go`:
  - Weave root prompt: replace references to Linear MCP tools + QUM-NNN with gh CLI + `gh` issue refs.
  - Engineer/researcher/manager child prompts: reference the `github-issues` skill, not `linear-issues`.
  - Remove hardcoded `QUM-` references (check `strings.Contains` calls and doc strings).
- `rootinit.BuildContextBlob` reads `CLAUDE.md` / `CLAUDE.local.md` at runtime, so Phase 2 edits carry most of the "how to track work" context without further code changes. Verify this is still true.
- Update tests: look for `TestEngineerSystemPrompt_ContainsKeyPhrases` and similar; adjust expected-phrase lists.

### Phase 4 — Migrate in-flight work (user, manual)

Identify Linear issues with status `In Progress` or `Backlog` still worth doing. Expected small set:

- QUM-260 — async FinalizeHandoff to prevent 30s UI freeze
- QUM-261 — resume-failure heuristic gap
- Anything in `docs/todo/*.md` that has accumulated

Port each to GitHub Issues by hand (or via `gh issue create --title ... --body-file ... --label type/bug`). Add to the Sprawl project. Copy AC verbatim.

Leave the 260+ completed Linear issues in Linear archived. No bulk migration.

### Phase 5 — Sweep & remove (researcher + engineer)

- Inventory all Linear references:
  ```bash
  grep -rn 'linear.app\|Qumulo-dmotles\|QUM-[0-9]\|team.*ID.*06162534' \
    --include='*.md' --include='*.go' --include='*.sh' \
    --exclude-dir=.sprawl --exclude-dir=.claude/projects
  ```
- Classify each hit:
  - **Intentional historical**: commit messages, changelog entries, session summaries in `.sprawl/memory/` → leave alone.
  - **Stale active reference**: docs, prompts, skills still pointing at Linear workflow → update.
- Delete `.claude/skills/linear-issues/` once the replacement is in.
- Remove Linear MCP server from `settings.json` if present.
- If the `mcp__linear__*` tools still appear in Claude's allowed tool lists anywhere (e.g. embedded in agent prompts), remove them.

### Phase 6 — Validate (weave)

- File a test issue via `gh issue create`. Add to the Sprawl project. Set Status=Backlog.
- Spawn an engineer against it end-to-end. Verify lifecycle: engineer reads via `gh issue view`, sets Status=In Progress, comments via `gh issue comment`, closes via `gh issue close` (or sets Status=Done if we keep issues open post-done).
- Delete this file or move to `docs/todo/done/`.

## Open decisions to make during execution

- **Issue numbering in branch names**: `dmotles/gh-42-foo` (explicit prefix) or `dmotles/42-foo` (bare number) or `dmotles/foo` (no number). Linear had `qum-NNN`; GitHub issue numbers are auto-assigned by the repo. Pick one and lock it in.
- **Issue templates**: `.github/ISSUE_TEMPLATE/bug.md`, `.github/ISSUE_TEMPLATE/feature.md`, `.github/ISSUE_TEMPLATE/research.md`. Probably yes — helps community contributors and gives agents a consistent structure to fill in.
- **PR template**: `.github/pull_request_template.md` with "Closes #N" guidance. Probably yes.
- **Priority model**: Project v2 field vs labels? Recommend field. But decide.
- **Status model**: Project v2 field (Backlog/In Progress/Done) + issue open/closed, OR just issue open/closed + labels? Recommend Project v2 field.

## Acceptance Criteria

- [ ] Phase 0: `gh auth refresh -s project` run. Sprawl Project v2 created with Status + Priority custom fields. Project linked to repo.
- [ ] Phase 1: type/bug, type/feature, type/refactor, type/research labels created.
- [ ] Phase 2: CLAUDE.md, CLAUDE.local.md, skills updated. No Linear references in active workflow docs.
- [ ] Phase 3: weave + child prompts reference gh CLI + the github-issues skill. `grep -rn "QUM-" --include='*.go'` returns only test fixtures or historical comments.
- [ ] Phase 4: in-flight Linear issues ported to GitHub and added to the Sprawl project.
- [ ] Phase 5: `grep` sweep clean of stale active references.
- [ ] Phase 6: E2E validation — engineer handles a real GitHub issue start-to-finish.

## Non-goals

- Migrating the 260+ completed Linear issues. Archived in place.
- Archiving the Linear workspace. Free tier keeps it readable for history; downgrade whenever.
- Cross-linking every Linear QUM-N to a new GitHub issue#. Not worth the effort.

## Issue dependencies / blocking

GitHub has native issue dependencies (GA 2024) and sub-issues. Not gated behind Projects v2.

**Three overlapping mechanisms:**

1. **Issue dependencies** (`blockedByIssues` / `blockingIssues`) — formal two-way links, Linear-equivalent. Closing a blocked issue is gated on its blockers being closed (configurable per-repo). This is what replaces Linear's `blockedBy` / `blocks` relations.

2. **Sub-issues** — hierarchical parent/child. Parent shows progress bar. Good for epics with sub-tasks.

3. **Tasklists** — `- [ ] #42` checkbox lists with live state. No formal link but GitHub surfaces "tracked by" backlinks. Easiest to edit by hand.

**gh CLI limitation:** v2.89.0 has no dedicated subcommand for dependencies or sub-issues. Access via `gh api graphql` using GraphQL mutations:

- `addSubIssue(input: {issueId, subIssueId})`
- `removeSubIssue(input: {issueId, subIssueId})`
- (similar for blocking/blocked-by via the `IssueDependency` mutations)

**Recommendation:** ship a small wrapper in `scripts/` or the github-issues skill — e.g. `scripts/gh-block.sh <blocker-num> <blocked-num>` that runs the right GraphQL. Agents use the wrapper; humans can too.

**For sprawl phases:** use real dependencies (option 1) so agents can query "what's unblocked" via GraphQL and auto-pick next work. Falls back to tasklists if the API gets annoying.
