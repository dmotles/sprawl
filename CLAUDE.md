# CLAUDE.md

Read `DESCRIPTION.md` for project context. This file covers how to work in this codebase.

## Build & Test

```bash
make              # runs full validation (build + fmt-check + lint + test)
make validate     # same as above — the default target
make build        # builds ./sprawl binary
make fmt          # auto-fix formatting
make fmt-check    # check formatting without fixing (used in CI/hooks)
make lint         # run golangci-lint
make test         # run all unit tests
make hooks        # install pre-commit hook

scripts/smoke-test-memory.sh   # integration test for weave memory system
scripts/sprawl-test-env.sh     # set up isolated test environment
```

## Install

> **Warning:** Do not run `make install` unless your agent identity is `weave` or the user explicitly asks you to. Other agents should only use `make build`, then test against the locally built `./sprawl` binary using temporary directories with overridden environment variables (e.g. `SPRAWL_ROOT`, `SPRAWL_AGENT_IDENTITY`) to exercise the tool.

## Project Configuration

Sprawl reads `.sprawl/config.yaml` for project-level settings:

```yaml
validate: "make validate"   # command to run for post-merge validation
```

If no config file exists or the `validate` key is absent, post-merge validation is skipped with a warning. Use `--no-validate` on `sprawl merge` to explicitly skip validation.

## Repo Layout

- `cmd/` — CLI commands (cobra). Each command has its own file + test file.
- `internal/agent/` — Claude Code launcher, agent name allocation, prompt building
- `internal/config/` — Project configuration loading (`.sprawl/config.yaml`)
- `internal/state/` — Agent state persistence (JSON files in `.sprawl/agents/`)
- `internal/tmux/` — tmux session/window management for running agents
- `internal/worktree/` — Git worktree creation for agent isolation

## Meta: Developing Sprawl Inside Sprawl

This repo IS Sprawl. The `.sprawl/` directory at the repo root stores agent state and worktrees. If you're an agent working on this codebase, you are running inside the system you're building. Don't mess with `.sprawl/` contents unless that's your task.

## Code Patterns

**Dependency injection**: Commands use a `deps` struct to inject interfaces for external dependencies (tmux, claude, git, env vars). See `cmd/spawn.go` (`spawnDeps`) for the canonical example. This enables testing without real subprocesses.

**Tests required**: Every file in `cmd/` and `internal/` has a corresponding `_test.go`. Keep it that way. **Read `/testing-practices` before writing any tests for the first time** — it covers the dependency injection pattern, mock conventions, and common pitfalls.

**Read `/go-cli-best-practices` before writing or modifying Go code** — it covers cobra patterns, error handling conventions, and dependency injection structure used throughout this codebase.

**Read `/cli-ux-best-practices` before adding or modifying any CLI command's behavior** — it covers output design for agent consumers, the "next action hint" pattern, error message design, and idempotency. Every command must tell the calling agent what to do next.

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

## Session Handoff

At the end of a session, use `/handoff` to persist context for the next session. It guides you through writing a structured summary and piping it into `sprawl handoff`.

## Sandbox Testing

Use the `/e2e-testing-sandboxing` skill for the full setup, inspection, and cleanup workflow. Quick start:

```bash
make build
eval "$(bash scripts/sprawl-test-env.sh)"
```

## Linting & Formatting

This project uses [golangci-lint v2](https://golangci-lint.run/) with `gofumpt` formatting. Configuration is in `.golangci.yml`.

* **All code must pass** `make validate` before committing. The pre-commit hook enforces this.
* Run `make fmt` to auto-fix formatting issues.
* Run `make hooks` after cloning to install the pre-commit hook.

## Validating Changes

1. `make validate` — full pipeline: build, fmt-check, lint, test
2. Manual smoke test: run the built `./sprawl` binary with relevant commands
3. For end-to-end validation, use the `/e2e-testing-sandboxing` skill to set up a sandbox environment
4. For TUI changes, read `/tui-testing` for the E2E validation harness and manual testing workflow. TUI validation is mandatory for all TUI-related changes.
5. **Tmux-mode `sprawl init` changes are mandatory-tested.** Any change touching `cmd/rootloop.go` or `internal/claude/` must run `make test-init-e2e` and pass before merge. The script (`scripts/test-init-e2e.sh`) spins up an isolated sandbox under `/tmp/`, runs `sprawl init --detached`, and asserts the resulting tmux pane does not exhibit the QUM-261 class of regression (claude flipping to `--print` mode and the bash restart loop thrashing). Requires a real `claude` binary on PATH; set `SPRAWL_E2E_SKIP_NO_CLAUDE=1` to skip in environments without one.
6. **Messaging-path changes are mandatory-tested.** Any change touching `cmd/messages.go`, `cmd/messages_notify.go`, `cmd/report.go`, `cmd/root.go`'s notifier registration, `internal/messages/`, `internal/agentops/report.go`, or `internal/supervisor/real.go` must run `make test-notify-e2e` and pass before merge. The script (`scripts/test-notify-e2e.sh`) spins up an isolated sandbox, has a simulated child agent run `sprawl report done`, and asserts the `[inbox] New message from <child>` line appears in the root weave tmux pane — guarding against the QUM-310 class of regression where only some `messages.Send` callsites notify. Requires a real `claude` binary on PATH; set `SPRAWL_E2E_SKIP_NO_CLAUDE=1` to skip.
