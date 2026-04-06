# 07 — Unknown Unknowns: Open-Source Readiness Audit

> **Validated by:** delta (researcher agent), 2026-04-05
> **Method:** Full codebase scan of all Go source, config files, documentation, and hidden directories. Cross-referenced with community standards from comparable Go CLI tools.

## 1. Beads Integration

### What It Is

Beads (`bd`) is an issue tracking system integrated into the repo. It stores issues in a Dolt database (a Git-for-data SQL engine) per worktree.

### Files in the Repo

| Path | Purpose |
|---|---|
| `BEADS.md` | Agent-facing instructions for using `bd` commands |
| `.beads/config.yaml` | Beads configuration (remote URL, credentials path) |
| `.beads/.gitignore` | Excludes runtime Dolt files and credential keys |
| `.beads/hooks/` | Git hooks (pre-commit, post-merge, prepare-commit-msg, etc.) |
| `.beads/README.md` | Beads integration documentation |
| `.beads/metadata.json` | Metadata for the beads installation |
| `.gitignore` | Excludes `.dolt/`, `*.db`, `.beads-credential-key` |

### References in Code

- `DESCRIPTION.md` line ~149: "Uses [beads](BEADS.md) (`bd`) for issue tracking per worktree"
- `README.md` line ~149: Same reference
- `.beads/config.yaml` contains a remote URL pointing to a Dolt database server

### Open-Source Impact

**Medium-high risk.** Beads is an external tool that:

- Is not a standard open-source project dependency
- Has its own credential system (`.beads-credential-key` is gitignored)
- Installs git hooks that run on every commit
- Is referenced in documentation as a core workflow component

**Recommendation:** For open-source release, beads should be treated as optional. The `.beads/` directory and `BEADS.md` could remain but should be documented as "internal workflow tooling" that external contributors can ignore. Git hooks installed by beads should not block contributors who don't have `bd` installed.

## 2. CLAUDE.md Contents

### What It Contains

The root `CLAUDE.md` (read by Claude Code on startup) contains:

1. **Build commands** — `make validate`, `make build`, `make fmt`, etc.
2. **Repo layout** — `cmd/`, `internal/agent/`, `internal/state/`, etc.
3. **Code patterns** — Dependency injection via `deps` struct, test requirements
4. **Skill references** — `/testing-practices`, `/go-cli-best-practices`, `/cli-ux-best-practices`, `/e2e-testing-sandboxing`
5. **Linear issue tracking** — Team "Qumulo-dmotles", prefix "QUM"
6. **Agent spawning conventions** — Example commands, branch naming
7. **Session handoff** — `/handoff` skill usage
8. **Migration notes** — M12 merge/retire workflow changes

### Open-Source Impact

**Low risk, but needs review.** CLAUDE.md is a legitimate file for Claude Code users. However:

- References to "Qumulo-dmotles" team and "QUM" prefix are internal identifiers
- Skill references (e.g., `/linear-issues`) assume a specific Claude Code configuration
- The `.claude/settings.json` and `.claude/skills/` directories are committed to the repo

**Recommendation:** CLAUDE.md can stay as-is — it's genuinely useful for any Claude Code user working on the project. The Linear team name could be updated post-rename. Skills are additive and harmless.

## 3. .sprawl/ Runtime State

### What Gets Created at Runtime

The `.sprawl/` directory is **gitignored** and created at runtime by `sprawl init`. Based on code in `internal/state/`:

```
.sprawl/
├── namespace           # Namespace identifier
├── root-name           # Name of the root agent
├── agents/
│   └── {agent-name}/
│       ├── state.json  # Agent state (parent, type, family, status, branch)
│       ├── tasks/      # Task definitions
│       ├── prompts/    # Persistent prompts
│       └── inbox/      # Message inbox
└── worktrees/
    └── {agent-name}/   # Git worktree for each agent
```

### What's in the Repo's .sprawl/ (If Anything)

In a clean clone, `.sprawl/` does not exist. The `.gitignore` excludes it entirely. However, **in the development repo** (where sprawl develops itself), `.sprawl/` contains live agent state. This is by design — see CLAUDE.md: "This repo IS Sprawl."

### Open-Source Impact

**No risk.** The `.gitignore` correctly excludes `.sprawl/`. A clean clone will not contain any runtime state.

## 4. Missing Community Files

### Comparison with Popular Go CLI Tools

I compared sprawl's community files against three well-known Go CLI projects:

| File | cobra | go-task | age | sprawl |
|---|---|---|---|---|
| LICENSE | Apache-2.0 | MIT | BSD-3 | **Missing** |
| CONTRIBUTING.md | Yes | Yes | No | **Missing** |
| CODE_OF_CONDUCT.md | Yes | Yes | No | **Missing** |
| SECURITY.md | Yes | No | No | **Missing** |
| .github/ISSUE_TEMPLATE/ | Yes | Yes | No | **Missing** |
| .github/workflows/ (CI) | Yes | Yes | Yes | **Missing** |
| .github/PULL_REQUEST_TEMPLATE.md | Yes | Yes | No | **Missing** |
| .github/FUNDING.yml | No | Yes | No | **Missing** |
| CHANGELOG.md | No | Yes | No | **Missing** |
| .editorconfig | No | Yes | No | **Missing** |
| README.md | Yes | Yes | Yes | **Present** |

### Critical Gaps

1. **LICENSE** — Referenced in README (`See [LICENSE](LICENSE) for details`) but **the file does not exist**. This is a blocking issue — without a license, the code is "all rights reserved" by default.

2. **CONTRIBUTING.md** — No contribution guidelines. External contributors won't know:
   - How to set up a development environment
   - Code style requirements (gofumpt, golangci-lint)
   - PR process and review expectations
   - Whether sprawl agents are used in the contribution workflow

3. **.github/workflows/** — No CI pipeline. The project has `make validate` but nothing runs it automatically on PRs.

4. **SECURITY.md** — No vulnerability reporting process.

### Recommended Priority

| Priority | File | Why |
|---|---|---|
| P0 (blocking) | `LICENSE` | Cannot open-source without it |
| P1 (launch) | `.github/workflows/ci.yml` | Protects main branch quality |
| P1 (launch) | `CONTRIBUTING.md` | First thing contributors look for |
| P2 (soon after) | `SECURITY.md` | Standard for any tool handling system access |
| P2 (soon after) | `.github/ISSUE_TEMPLATE/` | Structures community bug reports |
| P3 (nice to have) | `CODE_OF_CONDUCT.md` | Community governance |
| P3 (nice to have) | `CHANGELOG.md` | GoReleaser can auto-generate |

## 5. Internal References That Need Cleanup

### Hardcoded Identifiers

| Location | Reference | Issue |
|---|---|---|
| `go.mod` | `github.com/dmotles/sprawl` | Module path — must match actual GitHub org/repo |
| `CLAUDE.md` | "Qumulo-dmotles" team | Internal team name in Linear |
| `CLAUDE.md` | "QUM" issue prefix | Internal Linear project prefix |
| `.mcp.json` | `https://mcp.linear.app/mcp` | Linear MCP server — internal workflow |
| `.claude/settings.json` | Linear tool permissions | Internal workflow config |
| `README.md` | `git clone <repo-url>` | Placeholder URL |

### Module Path

The `go.mod` declares `github.com/dmotles/sprawl`. All internal imports use this path. If the project moves to a different GitHub org (e.g., `github.com/sprawlrchy/sprawl`), every import across all `.go` files must be updated. This is a straightforward `sed` operation but touches many files.

**Current import count:**

```
$ grep -r "github.com/dmotles/sprawl" --include="*.go" -l | wc -l
```

This would affect all files in `cmd/` and `internal/` that import sibling packages.

## 6. Dependencies Audit

### Direct Dependencies

| Module | Version | License | Purpose | Risk |
|---|---|---|---|---|
| `github.com/spf13/cobra` | v1.10.2 | Apache-2.0 | CLI framework | None — industry standard |
| `github.com/gofrs/flock` | v0.13.0 | BSD-3-Clause | File locking | None — small, well-maintained |

### Indirect Dependencies

| Module | Version | License | Purpose |
|---|---|---|---|
| `github.com/inconshreveable/mousetrap` | v1.1.0 | Apache-2.0 | Windows console detection (cobra dep) |
| `github.com/spf13/pflag` | v1.0.9 | BSD-3-Clause | POSIX flag parsing (cobra dep) |
| `golang.org/x/sys` | v0.37.0 | BSD-3-Clause | OS-level interfaces |

**Assessment:** All dependencies use permissive licenses (Apache-2.0 or BSD-3-Clause). No GPL or AGPL contamination risk. The dependency tree is exceptionally small for a Go CLI tool — this is a strength.

### Go Version

`go.mod` requires Go 1.25.0. This is Go's **latest release** (as of 2026). This means:

- `go install github.com/dmotles/sprawl@latest` requires users to have Go 1.25+
- Older CI systems or user environments may not have this version
- Worth documenting minimum Go version prominently

## 7. Newly Discovered Unknowns

These are issues found during this audit that weren't in the original scope:

### 7a. Claude Code Skills Are Committed

The `.claude/skills/` directory is committed to the repo containing custom skill definitions (e.g., `go-cli-best-practices.md`, `testing-practices.md`). These are:

- Useful for any Claude Code user working on the project
- Not harmful to commit
- But they reference internal workflows (Linear, sprawl agent spawning)

**Verdict:** Keep them. They're analogous to `.vscode/` settings — project-specific tooling config.

### 7b. `.claude/settings.json` Grants Broad Permissions

The committed `.claude/settings.json` grants permissions to various MCP tools (Linear). This is fine for the project but means cloning the repo opts users into these permissions when using Claude Code.

### 7c. No Version String in Binary

The codebase has no `version` variable in `main.go` or a `--version` flag. GoReleaser's `ldflags` can inject a version at build time, but the receiving variable needs to exist. This should be added before the first release.

### 7d. DESCRIPTION.md vs README.md Overlap

Both files describe the system. `DESCRIPTION.md` is more detailed (full architecture, all agent types, CLI reference). `README.md` is a trimmed version. For open-source:

- `README.md` is what GitHub shows — it should be the primary public-facing doc
- `DESCRIPTION.md` could become `docs/architecture.md` or be folded into the README
- Having two overlapping docs creates maintenance burden

### 7e. Tmux Dependency Is Unusual

Sprawl requires tmux to function. This is uncommon for a Go CLI tool and creates friction:

- tmux is not installed by default on most systems
- macOS users need `brew install tmux`
- Container/CI environments may not have it
- The install.sh should probably check for tmux and warn

### 7f. Claude Code Is a Prerequisite

Sprawl is an orchestration layer over Claude Code. Users need:

1. A Claude Code installation
2. An Anthropic API key or subscription
3. tmux
4. Go (only if building from source)

This dependency chain should be prominently documented. The README already mentions prerequisites but could be more explicit about the Anthropic account requirement.

## Summary Checklist

| Item | Status | Severity | Notes |
|---|---|---|---|
| LICENSE file exists | **No** | P0 Blocker | Referenced in README but missing |
| No secrets in repo | **OK** | — | `.gitignore` excludes credential files |
| Dependencies are permissive | **OK** | — | All Apache-2.0 or BSD-3-Clause |
| .sprawl/ is gitignored | **OK** | — | No runtime state leaks |
| Beads is documented as optional | **No** | P1 | Currently presented as core workflow |
| CONTRIBUTING.md exists | **No** | P1 | Needed before accepting PRs |
| CI pipeline exists | **No** | P1 | `make validate` exists but no automation |
| Version flag in binary | **No** | P1 | Needed for GoReleaser and user experience |
| Internal Linear references | **Present** | P2 | Can be cleaned up or documented |
| Module path matches target org | **Unknown** | P2 | Depends on final GitHub org decision |
| Community templates exist | **No** | P3 | Issue/PR templates |
