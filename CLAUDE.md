# CLAUDE.md

Read `DESCRIPTION.md` for project context. This file covers how to work in this codebase.

## Build & Test

```bash
make build      # builds ./dendra binary
go test ./...   # run all tests
```

## Install

> **Warning:** Do not run `make install` unless your agent identity is `root` or the user explicitly asks you to. Other agents should only use `make build`, then test against the locally built `./dendra` binary using temporary directories with overridden environment variables (e.g. `DENDRA_ROOT`, `DENDRA_AGENT_IDENTITY`) to exercise the tool.

## Repo Layout

- `cmd/` — CLI commands (cobra). Each command has its own file + test file.
- `internal/agent/` — Claude Code launcher, agent name allocation, prompt building
- `internal/state/` — Agent state persistence (JSON files in `.dendra/agents/`)
- `internal/tmux/` — tmux session/window management for running agents
- `internal/worktree/` — Git worktree creation for agent isolation

## Meta: Developing Dendra Inside Dendra

This repo IS Dendrarchy. The `.dendra/` directory at the repo root stores agent state and worktrees. If you're an agent working on this codebase, you are running inside the system you're building. Don't mess with `.dendra/` contents unless that's your task.

## Code Patterns

**Dependency injection**: Commands use a `deps` struct to inject interfaces for external dependencies (tmux, claude, git, env vars). See `cmd/spawn.go` (`spawnDeps`) for the canonical example. This enables testing without real subprocesses.

**Tests required**: Every file in `cmd/` and `internal/` has a corresponding `_test.go`. Keep it that way.

## Linear Issue Tracking

This project tracks work in Linear. All issues belong to the **Dendra** project in team **Qumulo-dmotles** (prefix: `QUM`).

When creating, managing, or querying issues, use the `/linear-issues` skill for conventions, required fields, and MCP tool usage.

**Issue lifecycle** — if you are working on a Linear issue:
1. **Start**: Set the issue state to "In Progress" via `save_issue`. Add a comment via `save_comment` noting you're picking it up (include your agent name/identity if you have one).
2. **Progress**: As you work, post comments on the issue with notable findings, decisions, or blockers. Keep the issue thread as a living log — especially for research or investigation tasks. Don't let useful context stay only in your head.
3. **Finish**: Set the issue state to "Done" via `save_issue`. Add a comment summarizing what was done, linking to any relevant commits or PRs.

## Spawning Agents

When spawning an agent to work on a Linear issue, keep the prompt short. Point the agent at the issue — don't repeat the issue contents in the prompt. Example:

```
dendra spawn --family engineering --type engineer \
  --branch "dmotles/qum-42-broadcast-partial-failure" \
  --prompt "Work on QUM-42. Read the issue for details."
```

The issue is the source of truth. The agent can read it via Linear MCP tools (`get_issue`).

## Validating Changes

1. `go test ./...` — all tests pass
2. `make build` — binary compiles
3. Manual smoke test: run the built `./dendra` binary with relevant commands
