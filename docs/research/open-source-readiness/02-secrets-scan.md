# Secrets & Proprietary Information Scan

**Date:** 2026-04-04
**Original researcher:** creek
**Validated by:** brook (2026-04-05) — all claims verified against codebase, additional findings added
**Status:** Validated ✓

## Summary

Full scan of codebase and git history (181 commits at time of validation). **No hardcoded credentials, API keys, tokens, or secrets found.** However, several proprietary/personal references need attention before open-source release.

## Findings

### Clean Areas

- No `.env` files in repo or history
- No API keys or tokens in source code
- No credentials in config files
- No personal email addresses in source code files (only in git metadata)
- Git history clean across all 181 commits — no secret content in diffs

### Items Requiring Attention

#### 1. "Qumulo-dmotles" Linear team name

References internal/personal Linear workspace. Found in:

| File | Line | Content |
|------|------|---------|
| `CLAUDE.md` | 49 | `team **Qumulo-dmotles** (prefix: QUM)` |
| `CLAUDE.md` | 64 | `--branch "dmotles/qum-42-broadcast-partial-failure"` |
| `.claude/skills/linear-issues/SKILL.md` | 12 | `Team: Qumulo-dmotles (prefix: QUM)` |
| `.claude/skills/linear-issues/SKILL.md` | 137 | `team: "Qumulo-dmotles"` |
| `.claude/skills/go-cli-best-practices/SKILL.md` | 350 | `github.com/dmotles/dendra` |
| `.claude/skills/cli-ux-best-practices/SKILL.md` | 111 | `branch dmotles/qum-42 preserved` |

**Decision:** Move to `CLAUDE.local.md` and `.gitignore` it. The Linear integration config (`.claude/skills/linear-issues/`) should also be excluded or parameterized.

#### 2. Personal email in git metadata

- **Email:** `seltom.dan@gmail.com`
- **Author name:** `dmotles`
- **Scope:** All 181 commits — this is the only committer email in the entire history
- **Not in source code** — only in `git log` metadata

Standard for OSS but user wants to use a different public identity. Can be scrubbed via `git filter-repo` before first public push.

#### 3. Hardcoded `/home/coder/dendra` path

Found in one test file:

| File | Line | Content |
|------|------|---------|
| `internal/memory/sessionlog_test.go` | 30 | `got := EncodeCWDForClaude("/home/coder/dendra/.dendra/worktrees/oak")` |
| `internal/memory/sessionlog_test.go` | 33 | `t.Errorf("EncodeCWDForClaude(%q) = ...", "/home/coder/dendra/.dendra/worktrees/oak", ...)` |

Cosmetic, not a security issue. Should be replaced with a generic path like `/home/testuser/project/.dendra/worktrees/testagent`.

#### 4. Go module path `github.com/dmotles/dendra` (NEW)

The module path in `go.mod` line 1 references the personal GitHub account `dmotles`. This propagates to **every `.go` file** via import statements (~80+ instances across `cmd/` and `internal/`). This will need updating if the repo moves to a different GitHub org/account.

**Files affected:** `go.mod` + all `.go` files with import statements referencing `github.com/dmotles/dendra`.

#### 5. QUM- Linear issue references in documentation (NEW)

Internal Linear issue identifiers (QUM-29, QUM-30, QUM-42, QUM-45, etc.) appear throughout documentation and some test data:

| File | Issue IDs |
|------|-----------|
| `CLAUDE.md` | QUM-42, QUM-126–QUM-130, QUM-131, QUM-133 |
| `.claude/skills/linear-issues/SKILL.md` | Multiple QUM examples |
| `.claude/skills/handoff/SKILL.md` | QUM-45 |
| `docs/research/go-agent-loop-integration.md` | QUM-29, QUM-30 |
| `docs/research/claude-stream-json-protocol.md` | QUM-29 |

These are not security-sensitive but reveal internal project management details.

#### 6. Linear MCP configuration (NEW)

`.mcp.json` contains the Linear MCP server URL (`https://mcp.linear.app/mcp`). This is a public service URL but the configuration implies the repo expects a Linear integration. `.claude/settings.json` contains whitelisted Linear MCP tool permissions (lines 7–38).

#### 7. `.beads/config.yaml` Linear references (NEW)

Lines 51–52 contain commented-out references to `linear.url` and `linear.api-key`. No actual keys present — just config skeleton.

## Action Items

- [ ] Move Qumulo/Linear-specific config to `CLAUDE.local.md`
- [ ] Add `CLAUDE.local.md` to `.gitignore`
- [ ] Decide on public email/identity before first public push
- [ ] Scrub git history with `git filter-repo` to replace personal email
- [ ] Fix hardcoded path in `internal/memory/sessionlog_test.go`
- [ ] Decide on Go module path for public release (update `go.mod` + all imports)
- [ ] Remove or generalize QUM- issue references in documentation
- [ ] Review `.mcp.json` and `.claude/settings.json` for Linear-specific config
- [ ] Review `.beads/config.yaml` for any proprietary references
