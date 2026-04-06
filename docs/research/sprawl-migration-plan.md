# Sprawl Migration Plan: Renaming Dendra/Dendrarchy

**Author:** brook (researcher agent)
**Date:** 2026-04-06
**Branch:** dmotles/sprawl-migration-plan
**Status:** Draft -- emoji pool finalized, name pools pending review

---

## 1. Overview

This document is a holistic, phased migration plan for renaming the project from **Dendra/Dendrarchy** to **Sprawl** and renaming the root agent from **sensei** to **neo**. The name "Sprawl" comes from William Gibson's Sprawl trilogy (Neuromancer, Count Zero, Mona Lisa Overdrive) -- a massive, interconnected, organic network. The root agent "neo" references The Matrix -- someone who sees the code, fights for humanity, mentors others, and grows.

### Scope of Changes

| Category | Current | New |
|---|---|---|
| CLI binary | `dendra` | `sprawl` |
| Project name | Dendrarchy | Sprawl |
| Go module path | `github.com/dmotles/dendra` | `github.com/dmotles/sprawl` |
| State directory | `.dendra/` | `.sprawl/` |
| Environment vars | `DENDRA_*` | `SPRAWL_*` |
| Root agent name | `sensei` | `neo` |
| Root agent personality | Measured/wise sensei | Matrix-inspired: sees potential, empowers, grows |
| Agent name pools | Trees (engineers), Water (researchers), Mountains (managers) | Cyberpunk-themed (see below) |
| Default namespace emoji | `🌳` (tree/nature pool) | `⚡` (electric/neon cyberpunk) |
| Namespace emoji pool | Tree/nature emojis | Electric/neon/cyberpunk emojis (see below) |
| Linear project | "Dendra" | "Sprawl" |

### What Does NOT Change

- Command names: `spawn`, `retire`, `merge`, `kill`, `delegate`, etc.
- Agent types: root, manager, engineer, researcher, tester, code-merger
- Agent families: product, engineering, qa
- Architecture and behavior
- Core CLI UX patterns

---

## 2. Complete File Inventory

### 2.1 Go Source Code (Production)

**Root command & binary:**
- `cmd/root.go` -- cobra root command `Use: "dendra"`, Long description mentions "Dendrarchy"
- `cmd/dendra_bin.go` -- `FindDendraBin()`, reads `DENDRA_BIN` env var
- `main.go` -- imports `github.com/dmotles/dendra/cmd`

**Init & sensei-loop:**
- `cmd/init.go` -- References `DENDRA_*` env vars, `findDendra`, `DefaultRootName`, `RootWindowName`, `sensei-loop` command
- `cmd/senseiloop.go` -- `senseiLoopDeps`, `rootSenseiTools`, reads `DENDRA_ROOT`, `DENDRA_NAMESPACE`, `DENDRA_TEST_MODE`

**Agent spawning:**
- `cmd/spawn.go` -- Reads `DENDRA_AGENT_IDENTITY`, `DENDRA_ROOT`, `DENDRA_NAMESPACE`, `DENDRA_TREE_PATH`, `DENDRA_BIN`, `DENDRA_TEST_MODE`
- `cmd/spawn_subagent.go` -- Similar env var references

**Other commands (env var consumers):**
- `cmd/retire.go` -- `DENDRA_ROOT`, `DENDRA_AGENT_IDENTITY`
- `cmd/messages.go` -- `DENDRA_ROOT`, `DENDRA_AGENT_IDENTITY`
- `cmd/report.go` -- `DENDRA_ROOT`, `DENDRA_AGENT_IDENTITY`
- `cmd/status.go` -- `DENDRA_ROOT`
- `cmd/tree.go` -- `DENDRA_ROOT`
- `cmd/logs.go` -- `DENDRA_ROOT`
- `cmd/merge.go` -- `DENDRA_ROOT`
- `cmd/delegate.go` -- `DENDRA_ROOT`, `DENDRA_AGENT_IDENTITY`
- `cmd/handoff.go` -- `DENDRA_ROOT`, `DENDRA_AGENT_IDENTITY`
- `cmd/poke.go` -- `DENDRA_ROOT`
- `cmd/cleanup_branches.go` -- `DENDRA_ROOT`
- `cmd/agentloop.go` -- `DENDRA_ROOT`, `DENDRA_AGENT_IDENTITY`

**Internal packages:**
- `internal/agent/names.go` -- Name pools (trees, water, mountains), fallback prefixes
- `internal/agent/prompt.go` -- All agent prompts reference "Dendrarchy", "dendra" CLI, "sensei", `.dendra/` paths, `DENDRA_AGENT_IDENTITY`
- `internal/agent/retire.go` -- May reference `.dendra/`
- `internal/tmux/tmux.go` -- `DefaultNamespace = "🌳"`, `DefaultRootName = "sensei"`, `RootWindowName = "sensei"`, `NamespacePool` (nature emojis)
- `internal/state/state.go` -- `.dendra/` paths for agents, namespace, root-name
- `internal/state/prompts.go` -- `.dendra/agents/` paths
- `internal/state/tasks.go` -- `.dendra/agents/` paths
- `internal/messages/messages.go` -- `.dendra/messages`, `.dendra/agents/` for wake files
- `internal/memory/session.go` -- `.dendra/memory/` paths
- `internal/memory/persistent.go` -- `.dendra/memory/persistent.md`
- `internal/memory/sessionlog.go` -- References `.dendra/worktrees/`
- `internal/merge/merge.go` -- `.dendra/locks/`
- `internal/merge/git.go` -- `.dendra/agents/` for poke files
- `internal/observe/observe.go` -- Various `.dendra/` paths
- `internal/agentloop/real_starter.go` -- Sets `DENDRA_AGENT_IDENTITY`, `DENDRA_ROOT`
- `internal/worktree/worktree.go` -- `.dendra/worktrees/` path

**Module definition:**
- `go.mod` -- `module github.com/dmotles/dendra`
- All import paths across every Go file reference `github.com/dmotles/dendra/...`

### 2.2 Go Test Files

Every production file above has a corresponding `_test.go` that:
- Hardcodes `DENDRA_*` env var names in mock `getenv` functions
- Hardcodes `.dendra/` paths in assertions
- References "sensei" as root agent name in test data
- References agent names from current pools in test fixtures

Key test files with heavy references:
- `cmd/agentloop_test.go` -- `DENDRA_ROOT`, `DENDRA_AGENT_IDENTITY` in mock getenv
- `cmd/senseiloop_test.go` -- sensei-loop specific tests
- `cmd/init_test.go` -- init flow tests
- `cmd/messages_test.go` -- Extensive `DENDRA_ROOT`/`DENDRA_AGENT_IDENTITY` checks
- `cmd/spawn_test.go` -- `DENDRA_*` env vars
- `internal/agent/prompt_test.go` -- Tests for "Dendrarchy", "dendra", `DENDRA_*` in prompts
- `internal/observe/observe_test.go` -- Heavy "sensei" usage as root name
- `internal/state/state_test.go` -- `.dendra/` path assertions
- `internal/memory/*_test.go` -- `.dendra/memory/` path assertions
- `internal/worktree/worktree_test.go` -- `.dendra/worktrees/` paths

### 2.3 Build & Configuration

- `Makefile` -- `go build -o dendra`, `rm -f dendra` in clean target
- `.gitignore` -- Likely has `/dendra` binary entry
- `go.mod` / `go.sum` -- Module path
- `.golangci.yml` -- No direct dendra references (just linter config)
- `.mcp.json` -- Check for any references
- `.beads/metadata.json` -- Project metadata

### 2.4 Scripts

- `scripts/dendra-test-env.sh` -- Filename contains "dendra", extensive `DENDRA_*` env vars, builds `dendra` binary, references `sensei` in session names
- `scripts/smoke-test-memory.sh` -- "sensei" in session names, `DENDRA_*` env vars, `.dendra/` paths, identity checks against "sensei"
- `scripts/smoke-test-merge.sh` -- Similar references
- `scripts/pre-commit` -- Runs `make validate` (no direct dendra reference, but binary name in Makefile matters)

### 2.5 Documentation

- `README.md` -- "Dendrarchy", "dendra", CLI examples
- `DESCRIPTION.md` -- Full project description with "Dendrarchy", "dendra"
- `CLAUDE.md` -- Build commands, repo layout, references to `.dendra/`, `dendra` binary
- `docs/research/naming-all-candidates.md` -- Historical context
- `docs/research/naming-candidates.md` -- Historical context
- `docs/research/open-source-readiness/README.md` -- "Dendra" project references
- `docs/research/open-source-readiness/*.md` -- Various "dendra" references
- `docs/designs/agent-wrapper-loop.md` -- "dendra" references
- `docs/designs/agent-teardown.md` -- "dendra" references
- `docs/testing/smoke-test-memory.md` -- "sensei" and "dendra" references
- `docs/testing/m4-manager-smoke-test.md` -- "dendra" references

### 2.6 Claude Code Configuration & Skills

- `.claude/settings.json` -- No direct dendra references (just MCP tool permissions)
- `.claude/skills/handoff/SKILL.md` -- References "dendra" CLI
- `.claude/skills/cli-ux-best-practices/SKILL.md` -- "Dendra" references
- `.claude/skills/e2e-testing-sandboxing/SKILL.md` -- `DENDRA_*` env vars
- `.claude/skills/linear-issues/SKILL.md` -- "Dendra" project references
- `.claude/skills/go-cli-best-practices/SKILL.md` -- "Dendra" references
- `.claude/skills/testing-practices/SKILL.md` -- "dendra" references

### 2.7 Linear Project

- Linear project name: "Dendra"
- Team prefix: `QUM` (stays the same)
- CLAUDE.md references "Dendra" project in Linear

---

## 3. New Name Pools & Emoji

### 3.1 Agent Name Pools (Cyberpunk-themed -- PENDING REVIEW)

The theme should fit the "sprawl" aesthetic -- Gibson's cyberpunk universe, positive sci-fi, The Matrix. **Explicitly avoid names associated with murderous or evil AIs** (HAL, Skynet, Ultron, etc.).

**Engineers (Hackers/Runners -- the ones who do the work):**
```
"finn", "ratz", "zone", "chip", "byte", "flux", "grid",
"hex", "link", "node", "ping", "riot", "sync", "volt", "wire",
"ajax", "blur", "dash", "edge"
```
*Inspiration: Gibson characters, hacker handles, tech terms that feel cyberpunk.*

> **Warning:** `case` (Neuromancer protagonist) is a bash/sh keyword and was removed from this list. Other potential conflicts: `link` (unix command) -- consider removing. `node` (Node.js) -- likely fine as it's not in PATH as bare `node` on systems without Node.js, but worth noting.

**Researchers (Deckers/Netrunners -- the ones who investigate):**
```
"ghost", "trace", "query", "probe", "recon", "scout", "cipher",
"prism", "pulse", "signal", "vector", "index", "logic", "orbit"
```
*Inspiration: Reconnaissance, information-gathering, signal analysis.*

**Managers (Fixers/Operators -- the ones who coordinate):**
```
"tower", "forge", "bastion", "citadel", "command",
"axis", "vault", "bridge", "cortex", "matrix", "prime", "zenith", "apex"
```
*Inspiration: Command structures, fortified positions, coordination points.*

> **Note:** `helm` was removed (Kubernetes Helm conflict). `nexus` was removed from both researchers and managers to avoid cross-pool duplicates.

**Fallback prefixes:**
```
"engineer":    "runner"
"researcher":  "decker"
"manager":     "fixer"
"tester":      "runner"
"code-merger": "runner"
```

> **Action needed:** These name pools are suggestions and need final review for:
> 1. No conflicts with existing CLI tools or common unix commands (`which <name>` check)
> 2. No overlap between pools (currently clean after dedup)
> 3. All names should be short (3-7 chars), memorable, and easy to type
> 4. Vibe check -- do they feel right for the sprawl aesthetic?

### 3.2 Namespace Emoji Pool (FINALIZED)

Electric/neon/cyberpunk vibe. First emoji is the default. This has been decided by the user.

```go
var NamespacePool = []string{
    "⚡", "🔮", "💠", "🌃", "💜", "🔷", "✴️", "💎", "🌆", "🛰️",
    "🌐", "🔌", "💡", "🧿", "☄️",
}
```

**Default namespace:** `"⚡"` (electric bolt -- energy, speed, cyberpunk neon)

**Session examples:** `⚡neo`, `⚡neo├finn├`, `🔮neo`

---

## 4. Root Agent Personality: neo

The root agent prompt personality should shift from "sensei" (wise teacher, measured) to "neo" (The One from The Matrix):

**Key traits:**
- Sees the underlying code/structure of the system (like Neo seeing the Matrix)
- Empowers others -- sees potential in agents and helps them grow
- Fights for the user's goals with determination
- Learns and adapts -- starts humble and grows more capable
- NOT a god complex -- more "reluctant hero who rises to the challenge"
- Collaborative rather than hierarchical -- a leader by example, not by authority

**Prompt tone changes:**
- "You are measured and wise" -> "You see the system clearly and act with precision"
- "As the sensei, you are the master control agent" -> "As neo, you see the full picture and orchestrate with clarity"
- Keep the practical/concise tone, but add a sense of seeing through complexity
- Reference "the sprawl" as the network of agents

---

## 5. The Cut-Over Plan

### The Critical Constraint

This project IS dendra. Agents (including the one writing this plan) are running inside the system being renamed. The cut-over is the moment we stop being dendra and start being sprawl.

### What Can Be Done BEFORE the Cut-Over (while still running as dendra)

1. **All Go code changes** -- rename strings, constants, env vars, paths in source
2. **All test changes** -- update test assertions and fixtures
3. **All documentation changes** -- update docs, README, CLAUDE.md
4. **Makefile changes** -- build target produces `sprawl` binary
5. **Script changes** -- rename scripts, update env vars
6. **Skills changes** -- update `.claude/skills/` content
7. **Name pool changes** -- swap in new cyberpunk-themed names
8. **Emoji pool changes** -- swap in new tech emojis
9. **Prompt changes** -- update all agent prompts including neo personality
10. **Module path change** -- update `go.mod` and all imports
11. **`go.sum` regeneration** -- after module path change
12. **Build and test validation** -- `make validate` passes with new names
13. **Linear project rename** -- can be done any time via Linear UI/API

### The Cut-Over Itself (Hard Switch)

This is the point of no return. It happens in a single, coordinated sequence:

1. **Tear down the entire dendra agent tree** -- `dendra retire --cascade` (or manual teardown of all agents)
2. **Rename the `.dendra/` state directory** -- `mv .dendra .sprawl` (on the main branch after all work is merged)
3. **Install the new binary** -- `make install` produces `sprawl` in PATH
4. **Re-initialize** -- `sprawl init` launches "neo" as the root agent
5. **Verify** -- The new system boots, neo responds, agents can be spawned

### What Must Be Done AFTER the Cut-Over

1. **Verify the running system** -- spawl init, spawn an agent, send messages, check status
2. **Clean up old state** -- Remove any leftover `.dendra/` artifacts
3. **Update any external references** -- GitHub repo description, Linear, etc.
4. **Git history note** -- The repo will have history under both names; this is fine

### Why This Ordering Works

The key insight is: **all code changes happen before the cut-over, but the state directory rename and binary installation happen AT the cut-over.** During development, the binary is still called `dendra` and reads `.dendra/` state. We develop the rename in a branch, validate it passes tests, then merge and cut over in one shot.

However, there's a subtlety: after the code changes are merged but before the cut-over, the `dendra` binary name in the Makefile will have changed to `sprawl`. This means **the last build before the cut-over must be done manually** or the Makefile needs a transitional target. We handle this by doing the binary name change as the very last code task (Phase 3).

---

## 6. Phases

### Phase 0: Preparation (Before Any Code Changes)

**Goal:** Set up the foundation and validate the plan.

| # | Task | Complexity | Dependencies | Acceptance Criteria |
|---|------|-----------|--------------|---------------------|
| 0.1 | Finalize name pools | S | None | Final cyberpunk name pools reviewed and approved. No conflicts with common unix commands. No overlap between pools. |
| 0.2 | ~~Finalize namespace emoji pool~~ | ~~S~~ | ~~None~~ | **DONE.** Pool finalized: ⚡ 🔮 💠 🌃 💜 🔷 ✴️ 💎 🌆 🛰️ 🌐 🔌 💡 🧿 ☄️ (default: ⚡) |
| 0.3 | Draft neo personality prompt | M | None | Root prompt text written and reviewed. Captures Matrix-inspired traits without being corny. |
| 0.4 | Rename Linear project | S | None | Linear project renamed from "Dendra" to "Sprawl" via Linear UI. |

**Validation:** All decisions documented and approved by the user.

---

### Phase 1: Internal Plumbing (Safe Refactors -- No Visible Change to Running System)

**Goal:** Rename all internal references in Go code, tests, and internal packages. The binary is still called `dendra` and reads `.dendra/` -- but all the string constants and code paths are updated.

**Strategy:** Use a constant/variable for the state directory name (`.dendra` -> `.sprawl`) and env var prefix (`DENDRA_` -> `SPRAWL_`), so the change is mechanical. The go module path change is the most invasive since it touches every file's imports.

| # | Task | Complexity | Dependencies | Acceptance Criteria |
|---|------|-----------|--------------|---------------------|
| 1.1 | Update Go module path | L | None | `go.mod` module path changed to `github.com/dmotles/sprawl`. All import paths in all `.go` files updated. `go build` succeeds. `go test ./...` passes. |
| 1.2 | Rename state directory constant `.dendra` -> `.sprawl` | M | 1.1 | All references to `.dendra/` in `internal/state/`, `internal/messages/`, `internal/memory/`, `internal/merge/`, `internal/observe/`, `internal/worktree/` changed to `.sprawl/`. All tests updated and passing. |
| 1.3 | Rename env vars `DENDRA_*` -> `SPRAWL_*` | L | 1.1 | All env var references (`DENDRA_ROOT`, `DENDRA_AGENT_IDENTITY`, `DENDRA_NAMESPACE`, `DENDRA_TREE_PATH`, `DENDRA_BIN`, `DENDRA_TEST_MODE`) renamed to `SPRAWL_*` equivalents in all Go files and tests. `make validate` passes. |
| 1.4 | Rename root agent: `sensei` -> `neo` | M | 1.1 | `DefaultRootName`, `RootWindowName` in `internal/tmux/tmux.go` changed. `sensei-loop` command renamed to `neo-loop` (or generic `root-loop`). All test fixtures updated. |
| 1.5 | Update cobra root command | S | 1.1 | `cmd/root.go`: `Use: "sprawl"`, Long description updated. `cmd/dendra_bin.go` -> `cmd/sprawl_bin.go` with `FindSprawlBin()`. |
| 1.6 | Update agent name pools | S | 1.1 | `internal/agent/names.go`: New cyberpunk-themed names. Fallback prefixes updated. All tests that reference specific agent names updated. |
| 1.7 | Update namespace emoji pool | S | 1.1 | `internal/tmux/tmux.go`: New emoji pool and default. Tests updated. |
| 1.8 | Update all agent prompts | M | 1.4, 1.5 | `internal/agent/prompt.go`: All prompt templates updated -- "Dendrarchy" -> "Sprawl", "dendra" -> "sprawl", "sensei" -> "neo". Root prompt personality rewritten for neo. Tests updated. |
| 1.9 | Update `FindDendraBin` -> `FindSprawlBin` and related | S | 1.3 | `cmd/dendra_bin.go` renamed, function renamed, `DENDRA_BIN` -> `SPRAWL_BIN` reference updated. Tests updated. |

**Validation:** `make validate` passes after each task. All tests pass. The binary name is still `dendra` at this point (that changes in Phase 3).

**Parallelism notes:**
- Task 1.1 (module path) MUST be done first -- it touches every file's imports, so all other tasks depend on it to avoid massive merge conflicts.
- After 1.1, tasks 1.2, 1.3, 1.6, 1.7 can be parallelized (they touch different files).
- Task 1.4 and 1.5 can be parallelized with each other.
- Task 1.8 depends on 1.4 and 1.5 (prompt content references root name and CLI name).
- Task 1.9 depends on 1.3 (env var rename).

---

### Phase 2: Scripts, Skills, and Documentation

**Goal:** Update everything outside of Go code.

| # | Task | Complexity | Dependencies | Acceptance Criteria |
|---|------|-----------|--------------|---------------------|
| 2.1 | Rename and update scripts | M | Phase 1 | `scripts/dendra-test-env.sh` -> `scripts/sprawl-test-env.sh`. All `DENDRA_*` -> `SPRAWL_*` in scripts. All `sensei` -> `neo` in session names. `scripts/smoke-test-memory.sh` and `scripts/smoke-test-merge.sh` updated. Scripts run successfully. |
| 2.2 | Update CLAUDE.md | S | Phase 1 | All references to `dendra`, `Dendrarchy`, `.dendra/`, `sensei` updated. Build commands updated. |
| 2.3 | Update README.md | S | Phase 1 | Full rewrite of README for "Sprawl" branding. CLI examples use `sprawl`. |
| 2.4 | Update DESCRIPTION.md | S | Phase 1 | Full rewrite for "Sprawl" identity and metaphor. |
| 2.5 | Update .claude/skills/ | M | Phase 1 | All 6 skill files updated: `dendra` -> `sprawl`, `DENDRA_*` -> `SPRAWL_*`, "Dendrarchy" -> "Sprawl". |
| 2.6 | Update docs/ directory | M | Phase 1 | Design docs, testing docs, research docs updated where they reference "dendra". Historical naming research docs can stay as-is (they're historical records). Open-source readiness docs updated. |
| 2.7 | Update .gitignore | S | Phase 1 | Binary entry changed from `/dendra` to `/sprawl`. |

**Validation:** All scripts run. `make validate` still passes. Documentation is coherent.

**Parallelism notes:** All Phase 2 tasks can be parallelized (they touch different files). But they all depend on Phase 1 being complete so the Go code references are consistent.

---

### Phase 3: Binary Name & Build System (The Last Code Change)

**Goal:** Change the binary output name. This is the final change before the cut-over.

| # | Task | Complexity | Dependencies | Acceptance Criteria |
|---|------|-----------|--------------|---------------------|
| 3.1 | Update Makefile binary name | S | Phase 2 | `go build -o sprawl .`, `rm -f sprawl` in clean, `make install` installs `sprawl`. `make validate` passes. |
| 3.2 | Final integration test | M | 3.1 | Full end-to-end validation: `make build` produces `sprawl` binary. Sandbox test with `SPRAWL_*` env vars. `sprawl init` works in test environment. Agent spawn works. Messages work. |

**Validation:** Complete E2E test in sandbox environment.

---

### Phase 4: The Cut-Over

**Goal:** Switch the live system from dendra to sprawl.

| # | Step | Description |
|---|------|-------------|
| 4.1 | Merge all work | Ensure all Phase 1-3 branches are merged to main. |
| 4.2 | Tear down running agents | Retire all active agents. Tear down dendra agent tree. |
| 4.3 | Rename state directory | `mv .dendra .sprawl` on main branch. |
| 4.4 | Build and install | `make build && make install` (produces `sprawl` binary). |
| 4.5 | Initialize sprawl | `sprawl init` -- neo comes online. |
| 4.6 | Verify | Spawn a test agent, send messages, check status, run tree. |
| 4.7 | Clean up | Remove old `dendra` binary from PATH if present. Update shell aliases. |

**Acceptance criteria:** `sprawl init` launches neo. `sprawl spawn` creates agents with cyberpunk names. `sprawl status` and `sprawl tree` work. Messages flow correctly.

---

### Phase 5: Post-Cut-Over Cleanup

**Goal:** Clean up any loose ends.

| # | Task | Complexity | Dependencies | Acceptance Criteria |
|---|------|-----------|--------------|---------------------|
| 5.1 | GitHub repo rename | S | Phase 4 | **Required.** Rename GitHub repo from `dendra` to `sprawl`. Set up redirect. Module path is already `github.com/dmotles/sprawl` from Phase 1. Verify `go install github.com/dmotles/sprawl@latest` works after rename. |
| 5.2 | Update external references | S | Phase 4 | Linear project description, any bookmarks, shell aliases, documentation links. |

---

## 7. Detailed Task Breakdown: Phase 1

Phase 1 is the most complex and has the most tasks. Here's a deeper breakdown:

### Task 1.1: Update Go Module Path

**Files touched:** `go.mod`, `go.sum`, and every `.go` file with imports (approximately 80+ files).

**Approach:**
1. Update `go.mod`: `module github.com/dmotles/sprawl`
2. Find-and-replace all imports: `github.com/dmotles/dendra/` -> `github.com/dmotles/sprawl/`
3. Run `go mod tidy` to regenerate `go.sum`
4. Run `make validate`

**Risk:** This is a massive find-and-replace across every Go file. It MUST be done first and alone to avoid merge conflicts with all other tasks.

**Complexity:** L (breadth, not depth -- mechanical but touches every file)

### Task 1.2: Rename State Directory Constant

**Files touched:** ~15 files in `internal/` packages + their tests.

**Approach:** Find every hardcoded `".dendra"` string in Go code and change to `".sprawl"`. The key locations are:
- `internal/state/state.go` -- `AgentsDir()`, `DendraDir()` -> `SprawlDir()`, `WriteNamespace()`, `ReadNamespace()`, etc.
- `internal/messages/messages.go` -- message dir path, wake file path
- `internal/memory/session.go` -- memory dir path
- `internal/memory/persistent.go` -- persistent memory path
- `internal/merge/merge.go` -- locks dir path
- `internal/merge/git.go` -- poke file path
- `internal/worktree/worktree.go` -- worktrees dir path
- All corresponding test files with path assertions

**Complexity:** M

### Task 1.3: Rename Environment Variables

**Files touched:** ~40+ files across `cmd/` and `internal/`.

**Approach:** Systematic find-and-replace:
- `DENDRA_ROOT` -> `SPRAWL_ROOT`
- `DENDRA_AGENT_IDENTITY` -> `SPRAWL_AGENT_IDENTITY`
- `DENDRA_NAMESPACE` -> `SPRAWL_NAMESPACE`
- `DENDRA_TREE_PATH` -> `SPRAWL_TREE_PATH`
- `DENDRA_BIN` -> `SPRAWL_BIN`
- `DENDRA_TEST_MODE` -> `SPRAWL_TEST_MODE`

This touches nearly every command file and test file.

**Complexity:** L (many files, mechanical but easy to miss one)

### Task 1.4: Rename Root Agent

**Files touched:**
- `internal/tmux/tmux.go` -- `DefaultRootName = "neo"`, `RootWindowName = "neo"`
- `cmd/senseiloop.go` -> `cmd/rootloop.go` (rename the file and the command)
- `cmd/senseiloop_test.go` -> `cmd/rootloop_test.go`
- `cmd/init.go` -- references to `sensei-loop` command
- Test files that reference "sensei" as root name (~20+ test files in `cmd/` and `internal/observe/`)

**Decision:** Rename `sensei-loop` to `root-loop` (not `neo-loop`) since the command should be generic and the root name could theoretically change again. The root name "neo" is a default, not embedded in the command name.

**Complexity:** M

### Task 1.8: Update All Agent Prompts

**Files touched:**
- `internal/agent/prompt.go` -- All 4 prompt templates (~700 lines of prompt text)
- `internal/agent/prompt_test.go` -- All prompt tests

**Changes needed:**
- Root prompt: Complete personality rewrite for "neo" + replace all "Dendrarchy"/"dendra" references
- Engineer prompt: Replace "Dendrarchy" -> "Sprawl", "dendra" -> "sprawl"
- Researcher prompt: Same replacements
- Manager prompt: Same replacements + "sensei" -> "neo" in system notification references
- `testSandboxWarning`: `$DENDRA_ROOT` -> `$SPRAWL_ROOT`, `$DENDRA_BIN` -> `$SPRAWL_BIN`
- `claudeCodeSubAgentGuidance`: "dendra" -> "sprawl" throughout

**Complexity:** M (many string changes, plus creative writing for neo personality)

---

## 8. Risk Assessment

### High Risk
- **Module path change (1.1)** -- Touches every file. Must be done first and merged before anything else to avoid conflicts.
- **The cut-over (Phase 4)** -- The system is live. If something goes wrong, we need to be able to roll back.

### Medium Risk
- **Env var rename (1.3)** -- Easy to miss one reference. Comprehensive test coverage mitigates this.
- **Prompt changes (1.8)** -- Prompts are long and contain complex string concatenation with Go format verbs. Easy to introduce syntax errors.

### Low Risk
- **Name pools (1.6)** -- Isolated to one file.
- **Emoji pool (1.7)** -- Isolated to one file.
- **Documentation (Phase 2)** -- No functional impact.

### Rollback Plan

If the cut-over fails:
1. `mv .sprawl .dendra` -- restore state directory
2. Rebuild old `dendra` binary from a tagged commit before the merge
3. `dendra init` -- restart the old system

To make this safe, **tag the commit before the merge** (e.g., `pre-sprawl-cutover`) so we can always rebuild the old binary.

---

## 9. Complexity Summary

| Phase | Tasks | Estimated Total Complexity |
|---|---|---|
| Phase 0 | 4 | S-M (mostly decisions) |
| Phase 1 | 9 | L (bulk of the work) |
| Phase 2 | 7 | M (mostly find-replace in docs) |
| Phase 3 | 2 | S-M |
| Phase 4 | 7 steps | M (coordination, not code) |
| Phase 5 | 2 | S (cleanup) |

**Total: ~24 tasks across 6 phases.** The critical path is: Phase 0 -> 1.1 -> {1.2, 1.3, 1.4, 1.5, 1.6, 1.7} -> {1.8, 1.9} -> Phase 2 -> Phase 3 -> Phase 4 -> Phase 5.

---

## 10. Open Questions

1. ~~**GitHub repo rename:**~~ **DECIDED.** Yes, rename repo from `dendra` to `sprawl`. Module path: `github.com/dmotles/sprawl`. This is required (Phase 5.1).

2. ~~**Backward compatibility shim:**~~ **DECIDED.** No shim. Clean break. No `DENDRA_*` -> `SPRAWL_*` mapping.

3. **`sensei-loop` command name:** Should this become `root-loop` (generic) or `neo-loop` (themed)? Recommendation: `root-loop` since the command is internal and the root name is configurable.

4. **Agent name pool finalization:** The cyberpunk name pools in this document are suggestions. They need review for:
   - Unix command conflicts (`case` is a bash keyword -- might be problematic)
   - Readability and memorability
   - Cross-pool uniqueness

5. ~~**Emoji terminal compatibility:**~~ **DECIDED.** Emoji pool finalized (see Section 3.2).

6. **Linear issue prefix:** Currently `QUM`. Does this stay or change?

---

## 11. Reflections

### Surprising Findings
- The scope is larger than expected: **110+ files** reference "dendra" in some form. The go module path change alone touches every Go file.
- The prompts in `internal/agent/prompt.go` are ~750 lines of carefully crafted text with complex Go format string concatenation. Changing them requires care to not break the `%s` / `%q` format verbs.
- The `senseiloop` command is a real cobra command (`sensei-loop`), not just a function -- it needs to be renamed as a command, which means updating the cobra registration in addition to the file.
- The smoke test scripts contain hardcoded "sensei" identity checks (e.g., testing that handoff fails with `not-sensei`). These are easy to miss.

### Open Questions / Areas for Further Investigation
- How to handle the `.dendra/` -> `.sprawl/` rename for the live state directory during cut-over. If there are active worktrees with `.dendra/` references in their paths, those paths might break.
- Whether the `.dendra/worktrees/` path is embedded in git worktree configuration (git stores absolute paths for worktrees). The cut-over may need to update git worktree metadata.
- Whether any Claude Code session state references `.dendra/` paths -- if so, sessions would need to be recreated.

### What I Would Investigate Next
- Read every skill file in detail to understand the full scope of "dendra" references
- Trace the worktree creation flow to understand if `.dendra/worktrees/` paths are stored in git metadata
- Check if `.beads/metadata.json` or any other config files reference "dendra"
- Validate the cyberpunk name pools against common shell builtins and PATH commands
- Research whether Go module path renames have any gotchas with `go mod tidy` and checksum databases
