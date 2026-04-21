# Rip Linear out — concrete edits for Phases 2, 3, 5

**Status:** research output, ready for engineer execution
**Produced:** 2026-04-21 by ghost (researcher)
**Parent plan:** [`rip-out-linear.md`](./rip-out-linear.md)

This document turns the high-level plan into a mechanical edit list. An engineer should be able to land Phases 2 and 3 by copy-pasting from here. Phase 5 is a sweep list + one wrapper script.

---

## 1. Inventory (grep classification)

Raw command:

```bash
grep -rn 'linear.app\|Qumulo-dmotles\|QUM-[0-9]\|team.*ID.*06162534\|mcp__linear' \
  --include='*.md' --include='*.go' --include='*.sh' --include='*.yaml' --include='*.yml' . \
  2>/dev/null | grep -v '\.sprawl/\|\.claude/projects/'
```

### Classification key

- **HIST** — intentional historical reference (commit log, changelog, session memory, one-off research note about past work). Leave alone.
- **STALE** — active workflow reference still pointing at Linear. Must be updated in this migration.
- **DELETE** — file/config that exists only to support the Linear integration. Remove outright.

### Classified hits

| Path | Lines | Class | Phase | Notes |
|---|---|---|---|---|
| `CLAUDE.md` | 58, 60, 62, 64, 71, 73 | STALE | P2 | Whole "Linear Issue Tracking" + "Spawning Agents" blocks. |
| `CLAUDE.local.md` | 3, 5, 7, 11, 13 | STALE | P2 | Private workspace config — rewrite for GitHub. |
| `.mcp.json` | 3–5 | DELETE | P5 | Linear MCP server definition. Remove entire `linear` entry. |
| `.claude/settings.json` | 7–38 | DELETE | P5 | All `mcp__linear__*` allow-list entries. Strip. |
| `.claude/skills/linear-issues/SKILL.md` | whole file | DELETE | P2 (after replacement lands) | Replaced by `.claude/skills/github-issues/SKILL.md`. |
| `.claude/skills/handoff/SKILL.md` | 17, 49, 68, 70 | STALE | P5 | "Link to Linear issues" / "QUM-45" example → GitHub equivalents. |
| `internal/agent/prompt_child_sections.go` | 53, 175 | STALE | P3 | Researcher/engineer child prompts mention "Linear". |
| `internal/agent/prompt_test.go` | 189 | STALE | P3 | `"comment on the Linear issue"` key phrase assertion. |
| `internal/tui/commands/handoff.go` | 17, 49 | STALE | P3/P5 | Handoff prompt template references "Linear issue IDs". |
| `docs/todo/rip-out-linear.md` | everywhere | HIST | — | The plan doc itself. Leave as-is (it IS the migration). |
| `docs/todo/README.md` | 3, 33 | STALE (minor) | P5 | "while Linear is over quota … batch-imported as GitHub Issues". Once migration lands, rewrite as "stopgap" → "archive/staging for one-off todo notes". |
| `docs/designs/unify-tui-weave-init.md` | QUM-252/255/256/257/259 throughout | HIST | — | Historical design record. Leave alone. |
| `docs/designs/agent-wrapper-loop.md` | 11 | HIST | — | "M2: Agent Wrapper Loop milestone in Linear" — stale reference to a past milestone. Optional polish in P5 (strike the sentence or rewrite to "see project history"). **Recommend P5.** |
| `docs/research/open-source-readiness/*.md` | many | HIST | — | Research snapshot of readiness audit. Leave alone. |
| `docs/research/realtime-message-injection.md` | 1 (title: `QUM-170`) | HIST | — | Research doc title. Leave alone. |
| `docs/research/go-agent-loop-integration.md` | 93, 115 | HIST | — | Example stream-JSON payload shows `{"name": "linear", "status": "connected"}` — was a literal snapshot from live output when the Linear MCP was attached. Fine as historical; optionally update sample in P5. **Keep.** |
| `docs/research/claude-stream-json-protocol.md` | 115 | HIST | — | Same as above. **Keep.** |
| `cmd/**/*_test.go` (many QUM-NNN) | — | HIST | — | Test-case origin tags. Leave alone. |
| `internal/**/*_test.go` (many QUM-NNN) | — | HIST | — | Same. Leave alone. |
| `cmd/messages.go:434` | `TODO(QUM-112)` | HIST | — | In-code TODO pointing at a historical Linear issue. Leave alone (or optionally update to a GitHub issue number once QUM-112 is ported in P4 — not required for this migration). |
| `cmd/merge_test.go:551` | `"implement QUM-42 broadcast fix"` | HIST | — | Test fixture string. Leave alone. |
| `internal/state/prompts_test.go:19` | `"Work on QUM-43..."` | HIST | — | Test fixture string. Leave alone. |
| `internal/sprawlmcp/server_test.go:486,494` | `"merged QUM-263"` | HIST | — | Test fixture. Leave alone. |
| `scripts/smoke-test-merge.sh:126,129` | "Linear history" | HIST | — | Refers to **git linear history**, not Linear the product. Leave alone. |
| `internal/agent/prompt_mode.go` | multiple "linear history" | HIST | — | Same — git linear-history phrasing. Leave alone. |
| `DESCRIPTION.md:183` | "Linear, GitHub Issues, etc." | STALE (minor) | P5 | Flip the ordering or drop "Linear" — sprawl no longer prefers it. Trivial edit. |
| `.beads/config.yaml` | 51–52 (per OSR audit) | HIST/DELETE? | P5 | Commented-out `linear.url`/`linear.api-key` skeleton. No secrets. Drop the commented block for cleanliness. |

**Phase 4 in-flight port candidates** (user-driven, listed in the parent plan): `QUM-260`, `QUM-261`. Out of scope for this doc.

---

## 2. Concrete edits for Phase 2

### 2.1 `CLAUDE.md` — replace the "Linear Issue Tracking" + "Spawning Agents" sections

**Old (lines 58–73):**

```markdown
## Linear Issue Tracking

This project tracks work in Linear. See `CLAUDE.local.md` for workspace-specific configuration (team name, issue prefix).

When creating, managing, or querying issues, use the `/linear-issues` skill for conventions, required fields, and MCP tool usage.

**Issue lifecycle** — if you are working on a Linear issue:
1. **Start**: Set the issue state to "In Progress" via `save_issue`. Add a comment via `save_comment` noting you're picking it up (include your agent name/identity if you have one).
2. **Progress**: As you work, post comments on the issue with notable findings, decisions, or blockers. Keep the issue thread as a living log — especially for research or investigation tasks. Don't let useful context stay only in your head.
3. **Finish**: Set the issue state to "Done" via `save_issue`. Add a comment summarizing what was done, linking to any relevant commits or PRs.

## Spawning Agents

When spawning an agent to work on a Linear issue, keep the prompt short. Point the agent at the issue — don't repeat the issue contents in the prompt. See `CLAUDE.local.md` for the team prefix to use in branch names.

The issue is the source of truth. The agent can read it via Linear MCP tools (`get_issue`).
```

**Replacement:**

```markdown
## GitHub Issue Tracking

This project tracks work as GitHub Issues on `dmotles/sprawl`, with status and priority managed via a linked GitHub **Projects v2** board. See `CLAUDE.local.md` for workspace-specific configuration (repo slug, branch prefix, project URL).

When creating, managing, or querying issues, use the `/github-issues` skill for conventions, required fields, and `gh` CLI usage.

**Issue lifecycle** — if you are working on a GitHub issue:
1. **Start**: Set the project Status field to "In Progress" via `gh project item-edit --field Status --value "In Progress"`. Post a comment via `gh issue comment <N> --body "<msg>"` noting you're picking it up (include your agent name/identity if you have one).
2. **Progress**: As you work, post comments on the issue with notable findings, decisions, or blockers via `gh issue comment`. Keep the issue thread as a living log — especially for research or investigation tasks. Don't let useful context stay only in your head.
3. **Finish**: Set the project Status field to "Done" and close the issue with `gh issue close <N> --comment "<summary>"`. Link relevant commits/PRs (GitHub auto-links `#N` mentions).

## Spawning Agents

When spawning an agent to work on a GitHub issue, keep the prompt short. Point the agent at the issue number — don't repeat the issue contents in the prompt. See `CLAUDE.local.md` for the branch-name convention.

The issue is the source of truth. The agent reads it via `gh issue view <N> --json title,body,state,labels,comments`.
```

### 2.2 `CLAUDE.local.md` — replace the Linear section

**Old (whole file, lines 1–13):**

```markdown
# CLAUDE.local.md — workspace-specific configuration (not checked in)

## Linear Issue Tracking

- **Team name:** Qumulo-dmotles
- **Team ID:** 06162534-2014-4d1c-aa90-eb222b8c61ba
- **Issue prefix:** QUM-
- **Branch prefix:** dmotles/
- **Project:** Sprawl  (ID: `c0ab9ab4-ced7-4ec6-9630-9e530cc60a46`)

When creating Linear issues via MCP tools, use the team ID above for the `team` parameter (the name alone causes a validation error). Always set `project: "Sprawl"` (or the project ID) so issues show up under the right project — omitting it puts them in the team's "no project" bucket.

When creating branches for issues, use the format: `dmotles/qum-NNN-short-description`
```

**Replacement:**

```markdown
# CLAUDE.local.md — workspace-specific configuration (not checked in)

## GitHub Issue Tracking

- **Repo:** `dmotles/sprawl`
- **Project (v2):** `Sprawl` — owned by `dmotles`. Find the project number with `gh project list --owner dmotles`.
- **Branch prefix:** `dmotles/`
- **Issue branch format:** `dmotles/gh-<issue-number>-<short-kebab-description>` (e.g. `dmotles/gh-42-fix-merge-lock`).
- **Authoring user:** `dmotles` (gh auth).

### gh CLI

`gh` is installed at `/usr/bin/gh` and authed with `repo` + `workflow` + `project` scopes. All issue and project operations go through `gh` — no MCP server, no PAT management. Run `gh auth status` to verify.

### Issue conventions

- **Status** (Backlog / In Progress / Done) is a Project v2 custom field — set via `gh project item-edit --field Status --value "<value>"`.
- **Priority** (Urgent / High / Medium / Low) is a Project v2 custom field — same mechanism.
- **Type** is expressed as labels: `type/bug`, `type/feature`, `type/refactor`, `type/research`.
- Every issue should be added to the Sprawl project at creation time via `gh project item-add`.

See the `/github-issues` skill for command reference and full lifecycle example.
```

### 2.3 New skill: `.claude/skills/github-issues/SKILL.md`

Full file content (create parent dir first):

````markdown
---
name: github-issues
description: Create, update, query, and manage GitHub Issues + Projects v2 for this project via the gh CLI. Use when creating tasks, filing bugs, planning work, setting up dependencies, or checking what's available to work on.
user-invocable: true
argument-hint: "[action] e.g. 'create', 'list blocked', 'plan milestone', or an issue number"
---

# GitHub Issue Management for Sprawl

All issue operations use the `gh` CLI. No MCP server, no PAT management, no Linear.
Workspace config (repo slug, project, branch prefix) lives in `CLAUDE.local.md`.

## Data model

- **Issue state** — `open` / `closed`. Lives on the issue.
- **Status** — `Backlog` / `In Progress` / `Done`. Lives on a Project v2 single-select field.
- **Priority** — `Urgent` / `High` / `Medium` / `Low`. Project v2 single-select field.
- **Type** — labels: `type/bug`, `type/feature`, `type/refactor`, `type/research`.
- **Dependencies** — native GitHub issue dependencies (`blockedByIssues` / `blockingIssues`). Managed via `gh api graphql`; see `scripts/gh-block.sh`.

## Core principle: write for the implementer, not yourself

Issues must be self-contained — assume a different person/agent with no prior context will implement them. Put every useful detail into the issue. The standard is rigor and thoughtfulness.

### Required fields

Every issue must have:

1. **Title** — concise, imperative. Good: "Add timeout to agent spawn." Bad: "Agent spawning issue."
2. **Body** — structured markdown (see *Description Structure* below).
3. **Label** — at least one of `type/bug`, `type/feature`, `type/refactor`, `type/research`.
4. **Project** — add to the Sprawl project (`gh project item-add`).
5. **Status** — set to `Backlog` on creation unless immediately actionable (`In Progress`).
6. **Priority** — set to `Medium` unless there's reason not to.

### Description structure

Every issue body should include:

#### Context & Motivation
Why does this exist? What problem does it solve? Most important section.

#### Implementation Details
Concrete info the implementer needs:
- File paths and names
- Functions, types, interfaces
- API contracts and integration points
- Links to external docs
- Scope boundaries — what is explicitly NOT included

#### Acceptance Criteria
A verifiable checklist. Include how to validate the work (CLI command, scenario, etc.).

#### Testing Expectations
TDD is expected. Call out edge cases. For test-focused issues, be detailed about coverage.

## Common `gh` commands

### Create an issue

```bash
gh issue create \
  --repo dmotles/sprawl \
  --title "Add timeout to agent spawn" \
  --body-file /tmp/issue-body.md \
  --label type/feature
```

`gh issue create` prints the issue URL. Parse the number off the end:

```bash
url=$(gh issue create --title "..." --body-file body.md --label type/feature)
num=$(basename "$url")
```

### View an issue (structured)

```bash
gh issue view 42 --json number,title,body,state,labels,comments,author,url
```

Fields you can request:
`assignees, author, body, closed, closedAt, comments, createdAt, labels, milestone, number, projectCards, projectItems, reactionGroups, state, title, updatedAt, url`.

### Edit / comment / close

```bash
gh issue edit 42 --add-label type/bug --remove-label type/feature
gh issue comment 42 --body "Picking this up — ghost"
gh issue close 42 --comment "Landed in abc1234."
gh issue reopen 42
```

### List / filter

```bash
gh issue list --label type/bug --state open --json number,title,labels
gh issue list --search "is:open no:assignee label:type/research" --json number,title
```

## Project v2 management

Agents **need the project scope** on their gh token (`gh auth refresh -s project`). Sprawl's Phase 0 setup covers this.

### Find project metadata

```bash
gh project list --owner dmotles                          # find the project number
gh project view <project-number> --owner dmotles --format json
gh project field-list <project-number> --owner dmotles --format json   # field IDs + option IDs
```

Cache the project number, Status field ID, and option IDs locally (e.g. in `CLAUDE.local.md`) rather than looking them up every call.

### Add an issue to the project

```bash
gh project item-add <project-number> --owner dmotles --url https://github.com/dmotles/sprawl/issues/42
```

### Set Status / Priority on a project item

```bash
# item-id comes from `gh project item-list` or the output of item-add with --format json
gh project item-edit \
  --project-id <PVT_...> \
  --id <PVTI_...> \
  --field-id <PVTSSF_... for Status> \
  --single-select-option-id <option id for "In Progress">
```

`gh project item-edit` currently requires IDs (not names) for the single-select option. Fetch them once via `gh project field-list ... --format json` and cache in the skill or a helper script.

**Recommended helper:** wrap the status transition in a script, e.g. `scripts/gh-status.sh <issue-num> <Backlog|In Progress|Done>`, so agents don't have to juggle field IDs. (Not shipped in this migration — follow-on polish.)

## Issue dependencies

GitHub has native `blockedByIssues` / `blockingIssues`. `gh` v2.89.0 has no dedicated subcommand; use `gh api graphql`. Sprawl ships `scripts/gh-block.sh` as a wrapper.

### Set a blocked-by relation

```bash
scripts/gh-block.sh <blocker-issue-num> <blocked-issue-num>
# e.g. issue 42 blocks issue 50:
scripts/gh-block.sh 42 50
```

See the script for raw GraphQL if you need to remove or query relations.

## Full lifecycle example

File an issue, add to project, set In Progress, comment, set Done, close:

```bash
# 1. File
url=$(gh issue create --title "Add merge --dry-run output" \
  --body-file body.md --label type/feature)
num=$(basename "$url")

# 2. Add to project (replace <N> with project number from `gh project list`)
gh project item-add <N> --owner dmotles --url "$url"

# 3. Set Status=In Progress (once IDs are cached/known)
scripts/gh-status.sh "$num" "In Progress"     # or raw gh project item-edit

# 4. Progress comments
gh issue comment "$num" --body "Plan: land in two waves. Starting with tests."

# 5. Mark Done
scripts/gh-status.sh "$num" "Done"

# 6. Close
gh issue close "$num" --comment "Landed in $(git rev-parse --short HEAD)."
```

## Finding available work

```bash
# All open issues in the Sprawl project, with their Status field:
gh project item-list <N> --owner dmotles --format json --limit 200 \
  | jq '.items[] | select(.content.state=="OPEN") | {num: .content.number, title: .content.title, status: .status}'
```

An issue is **available** if its project `Status` is `Backlog` AND it has no open `blockedByIssues`. Query blockers via:

```bash
gh api graphql -f query='
  query($num: Int!) {
    repository(owner:"dmotles", name:"sprawl") {
      issue(number: $num) {
        blockedByIssues(first: 20) { nodes { number state } }
      }
    }
  }' -F num=42
```

## Conventions

- Always add new issues to the Sprawl project — no orphans.
- Default `Status: Backlog`; only use `In Progress` when you (or an agent) are actively picking it up.
- On close, set `Status: Done` **and** run `gh issue close` — the project board should mirror issue state.
- When blocked, add a comment explaining what it's waiting on, and wire the dependency via `scripts/gh-block.sh`.
- Branches: `dmotles/gh-<issue-num>-<short-desc>` (see `CLAUDE.local.md`).
- When a PR closes the issue, include `Closes #<N>` in the PR body so GitHub auto-closes on merge.
````

---

## 3. Phase 3 prompt edits

Two source files + one test file.

### 3.1 `internal/agent/prompt_child_sections.go`

**Hit 1 — line 53 (engineer TDD reflection step):**

Current:
```go
   If there is an issue tracking system (Linear, Jira, Notion, Beads or bd)
   that the repo uses - file issues regarding your findings if you think they
```

Replacement:
```go
   If there is an issue tracking system (GitHub Issues, Jira, Notion, Beads or bd)
   that the repo uses - file issues regarding your findings if you think they
```

Rationale: generic example list, just swap the lead entry.

**Hit 2 — line 175 (`researcherReflectionSection`):**

Current:
```go
const researcherReflectionSection = `REFLECTION (before reporting done):
Before reporting done, pause and reflect on your research:
- What you found that was surprising or unexpected
- What open questions remain unanswered
- What you would investigate next if you had more time
Post these reflections as a comment on the Linear issue (if applicable) AND include them in your done report.`
```

Replacement:
```go
const researcherReflectionSection = `REFLECTION (before reporting done):
Before reporting done, pause and reflect on your research:
- What you found that was surprising or unexpected
- What open questions remain unanswered
- What you would investigate next if you had more time
Post these reflections as a comment on the tracking issue (if applicable — e.g. ` + "`gh issue comment <N>`" + ` for GitHub issues) AND include them in your done report.`
```

### 3.2 `internal/agent/prompt.go`

`grep -n 'linear\|Linear\|QUM' internal/agent/prompt.go` returns only the "linear history" references in `rootMergeRetireTmux` / `rootMergeRetireTUI` (about git, not Linear-the-product) — those stay. **No edits required in `prompt.go`.**

The root prompt body is generic ("issue tracking system, if present") and does not mention Linear explicitly. Runtime context about the specific issue tracker comes from `CLAUDE.md` / `CLAUDE.local.md` via `rootinit.BuildContextBlob`, so Phase 2's CLAUDE.md rewrite flows through automatically — the plan's claim on line 79 holds.

### 3.3 `internal/agent/prompt_test.go`

**Single assertion break — `TestBuildResearcherPrompt_ReflectionStep` (line ~181):**

```go
keyPhrases := []string{
    "REFLECTION",
    "surprising",
    "open questions",
    "investigate next",
    "comment on the Linear issue",   // ← breaks
    "done report",
}
```

Replace `"comment on the Linear issue"` with `"comment on the tracking issue"` (matches the new prompt body).

**Other checks:**

- `TestEngineerSystemPrompt_ContainsKeyPhrases` — grep shows no such test in `prompt_test.go`; the closest is `TestBuildEngineerPrompt_ReflectionStep` (line 164), whose key phrases don't reference "Linear". No change needed.
- `TestBuildRootPrompt_GoldenSnapshot_TmuxMode` / `..._TuiMode` (line ~1471) — snapshot tests. The root prompt text is **not changing** in Phase 3 (the only Linear reference was in child prompts), so the goldens should continue to match. If anything in `prompt.go` shifts inadvertently, regenerate goldens with the standard snapshot-update workflow.
- `TestBuildRootPrompt_DoesNotMentionRespawn` (line 277) — the string `"QUM-46"` lives in the test comment, not asserted. No break.
- Comment `// for the root prompt refactor (QUM-243 Wave A)` (line 1470) — historical tag in a comment, leave alone (HIST).

### 3.4 `internal/tui/commands/handoff.go` (engineer touches this in P3)

Two stale lines:

- Line 17: `- Issues closed or progressed (reference Linear issue IDs)`
  → `- Issues closed or progressed (reference GitHub issue numbers, e.g. #42)`
- Line 49: `- Reference artifacts. Link to Linear issues, name branches, cite file paths.`
  → `- Reference artifacts. Link to GitHub issues (#N), name branches, cite file paths.`

No tests assert on these exact strings (grep `commands/handoff_test.go` confirms).

---

## 4. Phase 5 sweep (what's left after P2+P3 land)

Residual items not covered above, in rough priority order:

1. **`.mcp.json`** — remove the entire `linear` server entry (lines 3–5).
2. **`.claude/settings.json`** — strip all 32 `mcp__linear__*` entries from the permissions allowlist (lines 7–38 per the `02-secrets-scan.md` audit; verify before deleting).
3. **`.claude/skills/linear-issues/`** — delete the whole directory once `/github-issues` is in and validated.
4. **`.claude/skills/handoff/SKILL.md`** — update references:
   - Line 17 / 49 / 70: "Link to Linear issues" → "Link to GitHub issues (#N)".
   - Line 68: example `(see QUM-45)` → `(see #45)` or a generic placeholder.
5. **`DESCRIPTION.md:183`** — `Linear, GitHub Issues, etc.` → just `GitHub Issues, etc.` (or reorder; this project has picked GitHub).
6. **`docs/todo/README.md:3,33`** — drop the "while Linear is over quota" framing; `docs/todo/` is now "a lightweight local staging area for planning notes" (or retire it entirely once all items land as GitHub issues).
7. **`docs/designs/agent-wrapper-loop.md:11`** — optional polish: strike or rewrite the sentence referencing the "M2 milestone in Linear".
8. **`.beads/config.yaml` lines 51–52** — drop the commented-out `linear.url` / `linear.api-key` skeleton (optional cleanup).
9. **`cmd/messages.go:434`** — `TODO(QUM-112)` — optional: once QUM-112 is ported to GitHub in P4, swap the tag. Not required for migration completeness.
10. **Self-check grep** — after all edits, rerun:
    ```bash
    grep -rn 'linear.app\|Qumulo-dmotles\|QUM-[0-9]\|team.*ID.*06162534\|mcp__linear' \
      --include='*.md' --include='*.go' --include='*.sh' --include='*.yaml' --include='*.yml' \
      --include='*.json' . 2>/dev/null | grep -v '\.sprawl/\|\.claude/projects/'
    ```
    Expected remaining hits: commit-message-style QUM-NNN tags inside `_test.go` files, historical QUM references in `docs/research/` and `docs/designs/`, and this migration's own docs. All classified HIST.

**Not included** (intentional): the `QUM-NNN` tags scattered through `cmd/*_test.go` and `internal/**/*_test.go`. These are case-provenance tags — they mark which historical Linear ticket the test case originated from. Rewriting them loses traceability for no gain. Leave alone.

---

## 5. Dependency wrapper script — `scripts/gh-block.sh`

Drafted content (bash, POSIX-ish, needs `gh` + `jq`):

```bash
#!/usr/bin/env bash
# scripts/gh-block.sh — add a "blocked-by" relation between two GitHub issues
# in the current repo (or an explicit repo via GH_REPO).
#
# Usage:
#   scripts/gh-block.sh <blocker-issue> <blocked-issue>
#
# Example:
#   scripts/gh-block.sh 42 50    # issue #42 must be closed before #50 can close
#
# This wraps the `addIssueDependency` GraphQL mutation since `gh` v2.89
# has no dedicated subcommand for issue dependencies.

set -euo pipefail

die() { printf 'gh-block: %s\n' "$*" >&2; exit 1; }

usage() {
  cat >&2 <<'EOF'
Usage: gh-block.sh <blocker-issue> <blocked-issue>

Arguments:
  blocker-issue  Issue number that must be completed first (blocker).
  blocked-issue  Issue number that is blocked by the above.

Environment:
  GH_REPO  owner/repo to target. Defaults to the repo of the current working tree.

The script uses `gh api graphql` with the addIssueDependency mutation
(GitHub issue dependencies, GA 2024).
EOF
  exit 2
}

[[ "${1:-}" == "-h" || "${1:-}" == "--help" ]] && usage
[[ $# -eq 2 ]] || usage

blocker="$1"
blocked="$2"

[[ "$blocker" =~ ^[0-9]+$ ]] || die "blocker must be a number, got: $blocker"
[[ "$blocked" =~ ^[0-9]+$ ]] || die "blocked must be a number, got: $blocked"
[[ "$blocker" != "$blocked" ]] || die "blocker and blocked must differ"

command -v gh >/dev/null || die "gh CLI not found in PATH"
command -v jq >/dev/null || die "jq not found in PATH (required for id lookup)"

# Auth + scope check
gh auth status >/dev/null 2>&1 || die "gh not authenticated — run: gh auth login"

# Resolve repo
if [[ -n "${GH_REPO:-}" ]]; then
  repo="$GH_REPO"
else
  repo=$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null) \
    || die "not inside a gh-recognized repo; set GH_REPO=owner/repo"
fi
owner="${repo%%/*}"
name="${repo##*/}"

# Resolve issue node IDs (needed by the GraphQL mutation).
resolve_id() {
  local num="$1"
  gh api graphql \
    -f query='query($o:String!,$n:String!,$num:Int!){
      repository(owner:$o,name:$n){ issue(number:$num){ id state } }
    }' \
    -F o="$owner" -F n="$name" -F num="$num" \
    | jq -er '.data.repository.issue.id // empty' \
    || die "could not resolve issue #$num in $repo"
}

blocker_id=$(resolve_id "$blocker")
blocked_id=$(resolve_id "$blocked")

# addIssueDependency takes the blocked issue as `issueId` and the blocker as `blockingIssueId`.
# (Mutation shape mirrors the GraphQL schema for native issue dependencies.)
gh api graphql \
  -f query='mutation($blocked:ID!,$blocker:ID!){
    addIssueDependency(input:{issueId:$blocked, blockingIssueId:$blocker}){
      issue { number }
      blockingIssue: issue { number }
    }
  }' \
  -F blocked="$blocked_id" -F blocker="$blocker_id" >/dev/null \
  || die "GraphQL mutation failed — check scopes (repo) and that dependencies are enabled in repo settings"

printf 'linked: #%s blocks #%s (in %s)\n' "$blocker" "$blocked" "$repo"
```

**Spec notes for the engineer implementing this:**

- Place at `scripts/gh-block.sh`, `chmod +x`.
- The GraphQL mutation names for GitHub's native issue dependencies (GA late 2024) have been referenced as `addIssueDependency` / `removeIssueDependency` in public docs. **Verify the exact schema shape** by running `gh api graphql -f query='{ __type(name: "Mutation") { fields { name } } }' | jq '.data.__type.fields[].name' | grep -i depend` before finalizing. If the mutation is actually named differently (e.g. `addSubIssue` which is a separate thing, or `addIssueBlockedBy`), adjust the `-f query=` block. Don't land the script blind — smoke-test once against a pair of throwaway issues.
- Consider a sibling `scripts/gh-unblock.sh` using `removeIssueDependency` if the inverse is needed often. Not required for Phase 5.
- Repo-level setting: GitHub has an org/repo setting "require blocking issues to be closed before closing a blocked issue." Out of script scope — document in the `/github-issues` skill if turned on.
- Pre-commit/CI: add to no special linter list; it's a standalone utility. Shellcheck-clean is a reasonable bar.

---

## Reflection

**Surprising findings:**

- **Most QUM-NNN references are historical and should stay.** Nearly every hit in `cmd/**/*_test.go` and `internal/**/*_test.go` is a case-provenance tag marking which past Linear ticket motivated the test. Wiping them loses traceability with zero benefit. Phase 5's `grep -r 'QUM-'` acceptance criterion should explicitly carve these out (the plan already implies "test fixtures or historical comments" — worth making explicit).
- **The real prompt-side blast radius is tiny.** After reading `prompt.go` / `prompt_child_sections.go`, only two lines say "Linear" in the entire root+child prompt tree, and one of them is an example list item (`Linear, Jira, Notion, Beads or bd`). The root prompt already delegates tracker-specifics to runtime context via `CLAUDE.md`, so Phase 2 carries most of the weight. Phase 3 is mostly text polish plus one test-phrase update.
- **`gh project item-edit` takes IDs, not names.** You can't say `--field Status --value "In Progress"` directly — you need the field ID (`PVTSSF_...`) and the option ID. The parent plan's shorthand is accurate in spirit but wrong in syntax. This is why a small `scripts/gh-status.sh` wrapper would materially help agents; without it, every status transition needs a field-list lookup. Worth filing as a follow-on.
- **Golden-snapshot tests for the root prompt** (`testdata/golden_tmux_claude_code.txt` + TUI variant) will NOT be touched by Phase 3 if edits stay confined to child prompts — confirmed by reading `prompt.go`. Good news: no snapshot-update churn.

**Open questions:**

1. **Exact GraphQL mutation name for issue dependencies.** I drafted `addIssueDependency` based on GitHub's 2024 announcement but did not verify against the live schema. The engineer must introspect (`gh api graphql -f query='{ __schema { mutationType { fields { name } } } }'`) before merging `scripts/gh-block.sh`. Could also be `linkIssueDependency` or similar.
2. **Priority model — labels vs Project v2 field.** Parent plan recommends Project field; I've written the skill assuming field. If the user prefers labels (`priority/high` etc.), the skill needs a one-paragraph edit.
3. **Branch naming.** Parent plan listed three options (`dmotles/gh-42-foo`, `dmotles/42-foo`, `dmotles/foo`). I locked in `dmotles/gh-<N>-<desc>` for the skill and CLAUDE.local.md. If that's wrong, one global find-replace across the deliverable fixes it.
4. **`.beads/config.yaml`** — is the `.beads/` subsystem live in the project? If yes, its commented-out Linear stub is benign; if it's dead code, the whole dir might be for removal. Didn't dig — out of scope.
5. **Phase 4 migration list** — the parent plan names QUM-260 / QUM-261 explicitly, but also "anything in `docs/todo/*.md` that has accumulated". Only `rip-out-linear.md` and `README.md` exist there today. If we expect more work to land there, the user may want to port `rip-out-linear.md` itself to a GitHub issue as a forcing function before P4.

**What I'd investigate next given more time:**

- Live-test the GraphQL mutation names against `dmotles/sprawl` on a throwaway pair of issues so the wrapper script can ship with confidence.
- Spec `scripts/gh-status.sh` in the same shape as `gh-block.sh` — the field-ID lookup friction is real.
- Audit `internal/memory/` outputs for "Linear" references that get auto-generated into memory files; if any template produces them, fix the template alongside P3.
- Walk the PR template / issue template gap called out as an open decision in the parent plan — draft `.github/ISSUE_TEMPLATE/*.md` and `.github/pull_request_template.md` content, since those materially improve community contribution UX and pair naturally with this migration.
