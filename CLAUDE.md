# CLAUDE.md

Read `DESCRIPTION.md` for project context. This file covers how to work in this codebase.

## Build & Test

```bash
make build      # builds ./dendra binary
go test ./...   # run all tests
```

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

## Validating Changes

1. `go test ./...` — all tests pass
2. `make build` — binary compiles
3. Manual smoke test: run the built `./dendra` binary with relevant commands
