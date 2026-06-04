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

## Running `claude` from agent bash subshells (QUM-518)

When an agent invokes `claude -p ...` from a Bash tool subshell, Claude Code
sanitizes the subprocess env and strips `CLAUDE_CODE_OAUTH_TOKEN`. The inner
`claude` then fails with `Not logged in`. The fix is a thin shell shim that
re-hydrates auth env vars before exec'ing the real binary.

**Setup (one-time, host side):**

1. Create `.env` at the repo root containing your auth token(s):

   ```
   CLAUDE_CODE_OAUTH_TOKEN=...
   ANTHROPIC_API_KEY=...     # optional
   ```

   Then `chmod 0600 .env`. **`.env` is gitignored — never commit it.**

2. Launch sprawl with the shim as `$SPRAWL_CLAUDE`:

   ```bash
   SPRAWL_CLAUDE=$(pwd)/scripts/run-claude sprawl enter
   ```

`scripts/run-claude` sources `$SPRAWL_ROOT/.env` (falling back to the script's
parent dir if `$SPRAWL_ROOT` is unset) and then `exec`s `claude`. The
`worktree.setup` hook in `.sprawl/config.yaml` copies `.env` into each new
agent worktree (preserving `0600` mode via `cp -p`) so the shim works from
inside worktrees too.

`internal/agent/claude.go` honors `$SPRAWL_CLAUDE`: if set, it is used
verbatim as the `claude` binary path; otherwise it falls back to a `PATH`
lookup.

## tmux safety (QUM-325)

> **Never run bare `tmux kill-server`.** Sandbox scripts now use a dedicated tmux socket via `SPRAWL_TMUX_SOCKET` (QUM-325), so sandbox operations are isolated from the user's default tmux server. Production sessions still share the default socket.
>
> To clear sandbox state, use the sanctioned `sprawl_sandbox_destroy` helper (from `scripts/sprawl-test-env.sh`) or the `_stmux kill-session -t $SPRAWL_NAMESPACE` wrapper — both target only the sandbox session on the sandbox socket. In scripts, always use `_stmux` (not bare `tmux`) for sandbox tmux operations.

## Text selection in `sprawl enter` (QUM-617)

`sprawl enter` enables Bubble Tea's mouse cell-motion capture so the viewport
receives scroll-wheel events. The same capture mode prevents the host
terminal from doing native click-drag text selection. Two ways to copy text
out of the viewport:

* **`Ctrl+_` toggles selection mode** (or `Ctrl+/` if your terminal allows it
  — note: Chrome/Chromium-based browsers including the coder web terminal will
  eat `Ctrl+/` for their own bindings, so use `Ctrl+_` as the reliable form).
  Both keys send the same wire byte (ASCII US, 0x1F) so Bubble Tea surfaces
  them identically. Press once to drop mouse capture (the status bar shows
  `-- SELECT (mouse capture off) — Ctrl+_ to resume --`), select / copy with
  your mouse the way you would anywhere else, then press the same key again to
  resume normal capture. Scroll wheel won't work while in selection mode — use
  PgUp/PgDn or the viewport keyboard scroll keys.
* **Shift+drag** (or Option+drag on macOS) bypasses mouse capture in most
  terminals (xterm.js / coder web terminal, gnome-terminal, kitty, wezterm,
  Alacritty, iTerm2). Behaviour varies; if your terminal swallows the
  modifier and selects nothing, fall back to the `Ctrl+_` toggle.

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
- `internal/supervisor/` — same-process child runtime registry and orchestration
- `internal/state/` — Agent state persistence (JSON files in `.sprawl/agents/`)
- `internal/worktree/` — Git worktree creation for agent isolation

## Meta: Developing Sprawl Inside Sprawl

This repo IS Sprawl. The `.sprawl/` directory at the repo root stores agent state and worktrees. If you're an agent working on this codebase, you are running inside the system you're building. Don't mess with `.sprawl/` contents unless that's your task.

## Code Patterns

**Dependency injection**: Commands use a `deps` struct to inject interfaces for external dependencies (backend processes, git, env vars, filesystem). See `cmd/merge.go` or `cmd/report.go` for representative examples. This enables testing without real subprocesses.

**Tests required**: Every file in `cmd/` and `internal/` has a corresponding `_test.go`. Keep it that way. **Read `/testing-practices` before writing any tests for the first time** — it covers the dependency injection pattern, mock conventions, and common pitfalls.

**Read `/go-cli-best-practices` before writing or modifying Go code** — it covers cobra patterns, error handling conventions, and dependency injection structure used throughout this codebase.

**Read `/cli-ux-best-practices` before adding or modifying any CLI command's behavior** — it covers output design for agent consumers, the "next action hint" pattern, error message design, and idempotency. Every command must tell the calling agent what to do next.

## Linear Issue Tracking

This project tracks work in Linear. See `CLAUDE.local.md` for workspace-specific configuration (team name, issue prefix).

When creating, managing, or querying issues, **invoke the `/linear-issues` skill via the Skill tool first** — do not rely on remembered conventions. The skill defines required fields (label, milestone, state) that are easy to miss otherwise.

**Issue lifecycle** — if you are working on a Linear issue:
1. **Start**: Set the issue state to "In Progress" via `save_issue`. Add a comment via `save_comment` noting you're picking it up (include your agent name/identity if you have one).
2. **Progress**: As you work, post comments on the issue with notable findings, decisions, or blockers. Keep the issue thread as a living log — especially for research or investigation tasks. Don't let useful context stay only in your head.
3. **Finish**: Set the issue state to "Done" via `save_issue`. Add a comment summarizing what was done, linking to any relevant commits or PRs.

## Spawning Agents

When spawning an agent to work on a Linear issue, keep the prompt short. Point the agent at the issue — don't repeat the issue contents in the prompt. See `CLAUDE.local.md` for the team prefix to use in branch names.

The issue is the source of truth. The agent can read it via Linear MCP tools (`get_issue`).

## Session Handoff

At the end of a session, use `/handoff` to persist context for the next session. It guides you through writing a structured summary and calling the `handoff` MCP tool.

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
5. **Mandatory-test e2e harness.** When you touch any file listed in the table below, run `make test-e2e-matrix-<row>` for the corresponding row (or `make test-e2e-matrix` to run all rows). All rows require a real `claude` binary on PATH; set `SPRAWL_E2E_SKIP_NO_CLAUDE=1` to skip. The `recover-live` row requires the `sprawl_test` build tag — the driver (`scripts/e2e-matrix.sh`) handles this automatically via `needs_build_tags=sprawl_test`. The original per-test Makefile targets (`make test-notify-tui-e2e`, `make test-handoff-e2e`, `make test-merge-reuse-e2e`, `make test-ask-user-question-e2e`, `make test-drain-row-inject-e2e`, `make test-paste-coalesce-e2e`, `make test-recover-live-e2e`) and their underlying `scripts/test-*-e2e.sh` scripts remain available as a fallback during the soak period; they will be removed in a follow-up issue once the matrix rows have proven flake-free for a few days.

   | files touched | matrix row | guards |
   |---|---|---|
   | `cmd/enter.go`, `cmd/enter_notify.go`, `internal/tui/app.go`, `internal/tui/messages.go`, or `internal/tui/tree.go` | `notify-tui` | QUM-311/QUM-312 |
   | `cmd/enter.go`, `internal/supervisor/*.go`, `internal/sprawlmcp/*.go`, `internal/rootinit/postrun.go`, or `internal/tui/app.go`'s `HandoffRequestedMsg`/`SessionRestartingMsg`/`RestartSessionMsg` handlers | `handoff` | QUM-329 |
   | `internal/agentops/merge.go`, `internal/sprawlmcp/server.go` (`toolMerge`), `cmd/merge.go`, `internal/supervisor/supervisor.go` (`Merge`), or `internal/supervisor/real.go` (`Real.Merge` / `mergeFn`) | `merge-reuse` | QUM-511/QUM-489 |
   | `internal/supervisor/question.go`, `internal/supervisor/question_real.go`, `internal/supervisor/real.go` (`RegisterRootRuntime` — QUM-535 root-type persistence; `Real.Recover` proactive `cancelByAgent` — QUM-611), `internal/sprawlmcp/server.go` (`toolAskUserQuestion` + eligibility gate), `internal/sprawlmcp/tools.go` (`ask_user_question` schema), `internal/tui/question.go`, `internal/tui/messages.go` (`DismissQuestionMsg.Hard` — QUM-611), `internal/tui/app.go` (question modal + `Ctrl-Q` binding + `View()` composition for `showQuestion` + `DismissQuestionMsg` cancel path — QUM-611), `internal/tui/statusbar.go` (`SetPendingQuestions` / `SetQuestionModalHidden` — QUM-611), or `cmd/enter.go` (TUI question consumer registration + `QuestionsChanged` forwarder goroutine) | `ask-user-question` | QUM-527/QUM-535/QUM-611 |
   | `internal/messages/messages.go`, `internal/runtime/unified.go`, `internal/runtime/queue.go`, `internal/supervisor/weave_handle.go`, `internal/supervisor/runtime.go`, `internal/supervisor/runtime_launcher.go`, `internal/supervisor/real.go`, `internal/inboxprompt/inboxprompt.go`, `internal/tui/messages.go`, `internal/tui/viewport.go`, or `cmd/enter.go` | `drain-row-inject` | QUM-555/QUM-323 |
   | `internal/runtime/unified.go` (`UnifiedRuntime.ForceInterruptForDelivery` — QUM-619 idle-recipient gate), `internal/supervisor/runtime_launcher.go` (`unifiedHandle.ForceInterruptDelivery` / `drainPendingToQueue`), or `internal/supervisor/real.go` (`Real.SendMessage` interrupt=true branch) | `idle-interrupt-inject` | QUM-619 |
   | `internal/inputcoalesce/coalescer.go`, the `tea.NewProgram` call site in `cmd/enter.go` (`resolveEnterDeps.runProgram` closure), or `cmd/input_debug.go`'s coalescer consumer (`runInputDebug`'s `--coalesce` branch) | `paste-coalesce` | QUM-608 |
   | `internal/supervisor/runtime.go` (Recover / startWithSpec / health probe), `internal/supervisor/real.go` (`Real.Recover` wrapper / `RecoverAgents` post-restart resume path), `internal/backend/claude/adapter.go` (subprocess lifetime / `realStarter.Start` / `Pid()` exposure), `internal/runtime/unified.go` (Done() closure on terminal fault / `SetTerminalErrorHandler` wiring), or `internal/runtime/turnloop.go` | `recover-live` | QUM-606 |
   | `internal/runtime/eventbus.go` (`Publish` Seq stamping, `CurrentSeq`, `PublishWithSeq`), `internal/tuiruntime/tuiadapter.go` (`lastSeq`, `pendingMsg`, gap-detect branch, `SPRAWL_DEBUG_GAP_INJECT`), `internal/tui/replay.go`, or `internal/tui/app.go`'s `EventDropDetectedMsg` / `ViewportResyncMsg` / `gapConfirmMsg` reducers / `gapStateNormal..gapStateRecovered` / `resyncCmd` / `kickResyncFromGap` / Ctrl+L key arm | `viewport-resync` | QUM-669 |
