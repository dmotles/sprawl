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
5. **TUI-notifier changes are mandatory-tested.** Any change touching `cmd/enter.go`, `cmd/enter_notify.go`, `internal/tui/app.go`, `internal/tui/messages.go`, or `internal/tui/tree.go` must run `make test-notify-tui-e2e` and pass before merge. The script (`scripts/test-notify-tui-e2e.sh`) spins up an isolated `/tmp` sandbox, launches `sprawl enter` in a detached tmux pane, then simulates a child agent (identity `sandbox-child`) by writing a `state.json` (state=complete, last_report_message set) and a maildir envelope addressed to weave directly into the sandbox state tree, and asserts the TUI surfaces both the `inbox: N new message(s) for weave` viewport banner and the `(N)` unread badge on the synthesized weave row — guarding against the QUM-311/QUM-312 class of regression where the TUI-mode inbox notifier silently drops child→weave deliveries. Requires a real `claude` binary on PATH; set `SPRAWL_E2E_SKIP_NO_CLAUDE=1` to skip.
6. **Handoff-path changes are mandatory-tested.** Any change touching `cmd/enter.go`, `internal/supervisor/*.go`, `internal/sprawlmcp/*.go`, `internal/rootinit/postrun.go`, or `internal/tui/app.go`'s `HandoffRequestedMsg`/`SessionRestartingMsg`/`RestartSessionMsg` handlers must run `make test-handoff-e2e` and pass before merge. Requires a real `claude` binary on PATH; set `SPRAWL_E2E_SKIP_NO_CLAUDE=1` to skip. Guards against the QUM-329 class of regression where the MCP tool and the TUI listener end up hitting different supervisor instances.
7. **Merge-path changes are mandatory-tested.** Any change touching `internal/agentops/merge.go`, `internal/sprawlmcp/server.go` (`toolMerge`), `cmd/merge.go`, `internal/supervisor/supervisor.go` (`Merge`), or `internal/supervisor/real.go` (`Real.Merge` / `mergeFn`) must run `make test-merge-reuse-e2e` and pass before merge. The script is pure shell (no `claude` binary required) — it hand-crafts an agent worktree, simulates a delegate-style branch swap (worktree HEAD moves to a new branch while state.json still records the spawn-time branch), and asserts that `sprawl merge` follows the worktree's current branch. Guards against the QUM-511 class of regression where merge silently no-ops on a stale `agentState.Branch`, plus the QUM-489 class where `toolMerge` flattens `WasNoOp` to a generic "Merged agent X" success text and hides the bug from MCP callers.
8. **Ask-user-question-path changes are mandatory-tested.** Any change touching `internal/supervisor/question.go`, `internal/supervisor/question_real.go`, `internal/supervisor/real.go` (`RegisterRootRuntime` — QUM-535 root-type persistence; `Real.Recover` proactive `cancelByAgent` — QUM-611), `internal/sprawlmcp/server.go` (`toolAskUserQuestion` + eligibility gate), `internal/sprawlmcp/tools.go` (`ask_user_question` schema), `internal/tui/question.go`, `internal/tui/messages.go` (`DismissQuestionMsg.Hard` — QUM-611), `internal/tui/app.go` (question modal + `Ctrl-Q` binding + `View()` composition for `showQuestion` + `DismissQuestionMsg` cancel path — QUM-611), `internal/tui/statusbar.go` (`SetPendingQuestions` / `SetQuestionModalHidden` — QUM-611), or `cmd/enter.go` (TUI question consumer registration + `QuestionsChanged` forwarder goroutine) must run `make test-ask-user-question-e2e` and pass before merge. The script (`scripts/test-ask-user-question-e2e.sh`) spins up an isolated `/tmp` sandbox, launches `sprawl enter` in a detached tmux pane, then runs three phases: **Phase 0 (QUM-535)** drives weave directly to call `mcp__sprawl__ask_user_question` with `AUQ-WEAVE-PROBE-<token>-{aye,bee,cee}` sentinels — proves weave's persisted state has `type=root` so the disk-backed eligibility gate accepts root-as-caller; asserts the modal appears, sends `Down`+`Enter` to pick option 2, and asserts weave's `state.last_report_message` contains the `-bee` sentinel. **Phase 1 (QUM-527)** drives root weave to spawn a manager-type child via `mcp__sprawl__spawn` whose prompt instructs the auto-named manager to call `mcp__sprawl__ask_user_question` with a single-select payload carrying a unique `AUQ-PROBE-<token>-{alpha,beta,gamma}` sentinel, asserts the `is asking` modal indicator appears, sends `Down`+`Enter` to select option 2 (defeating any "always default-cursor" buggy implementation), and asserts the manager's `state.last_report_message` contains the unique `AUQ-PROBE-<token>-beta` sentinel — proving the `QuestionResponse` JSON crossed back through the MCP tool to the manager. **Phase 2 (QUM-611 Esc-cancel wedge regression guard)** spawns a fresh manager that records a pre-question baseline via `report_status`, calls `ask_user_question`, then unconditionally records a post-question `report_status`; the script asserts the modal appears, sends a **single Esc keypress (NOT a selection)**, then asserts (a) the modal closes within 10s and (b) the manager's `last_report_message` advances from the pre-sentinel to the post-sentinel within 30s — proving the blocked MCP call returned (un-wedged) so the manager's next turn fired. Pre-QUM-611-fix this assertion fails because `DismissQuestionMsg` only hid the modal without calling `Supervisor.CancelQuestion`, leaving the MCP call parked indefinitely. The eligibility-gate engineer / researcher reject paths are covered by unit tests in `internal/sprawlmcp/server_askquestion_test.go`. Requires a real `claude` binary on PATH; set `SPRAWL_E2E_SKIP_NO_CLAUDE=1` to skip. Guards against the QUM-527/QUM-535/QUM-611 class of regression where the supervisor, MCP server, TUI consumer, or `cmd/enter.go` forwarder end up wired to mismatched instances, the modal never appears, the response never returns, the root-as-caller eligibility gate rejects weave, or plain Esc on a visible modal hides the UI without unblocking the caller (the QUM-611 wedge).
9. **Drain-row prompt-inject changes are mandatory-tested.** Any change touching `internal/messages/messages.go`, `internal/runtime/unified.go`, `internal/runtime/queue.go`, `internal/supervisor/weave_handle.go`, `internal/supervisor/runtime.go`, `internal/supervisor/runtime_launcher.go`, `internal/supervisor/real.go`, `internal/inboxprompt/inboxprompt.go`, `internal/tui/messages.go`, `internal/tui/viewport.go`, or `cmd/enter.go` must run `make test-drain-row-inject-e2e` and pass before merge. The script (`scripts/test-drain-row-inject-e2e.sh`) spins up an isolated `/tmp` sandbox, launches `sprawl enter` in a detached tmux pane, then drives root weave to spawn an engineer-type child via `mcp__sprawl__spawn` whose prompt instructs the child to immediately call `mcp__sprawl__messages_send` to weave with a unique `DRAIN-PROBE-<token>` sentinel body before calling `report_status` complete. Phase 1 sanity-asserts the maildir watcher still fires (`inbox: N new message` banner within 60s, mirroring `test-notify-tui-e2e.sh`'s post-QUM-565 surface). Phase 2 is the primary assertion: weave's TUI pane must render the drain-row citation `From <child> — mcp__sprawl__messages_read(id=...)` within 90s — proving the `internal/messages.Send()` defaultNotifier callback fired, `supervisor.WakeForDelivery` delivered the wake, the queue flushed via `inboxprompt.BuildQueueFlushPrompt`, claude received the `<system-notification>` prompt-inject, and the TUI viewport rendered the body. The harness asserts on the stable `mcp__sprawl__messages_read(id=` fixed substring (not the `✉`/`⚡` glyph, since async-vs-interrupt classification can vary) via `grep -F` because the em-dash is a unicode codepoint. Requires a real `claude` binary on PATH; set `SPRAWL_E2E_SKIP_NO_CLAUDE=1` to skip. Restores the shell-layer regression guard that QUM-565 stripped from `test-notify-tui-e2e.sh` Test B when it migrated onto direct on-disk maildir writes that bypass the defaultNotifier callback — guards against the QUM-555/QUM-323 class of regression where the Send → notifier → wake → claude prompt-inject → child messages_read drain pipeline silently breaks.
11. **Paste-coalescer changes are mandatory-tested.** Any change touching `internal/inputcoalesce/coalescer.go`, the `tea.NewProgram` call site in `cmd/enter.go` (`resolveEnterDeps.runProgram` closure), or `cmd/input_debug.go`'s coalescer consumer (`runInputDebug`'s `--coalesce` branch) must run `make test-paste-coalesce-e2e` and pass before merge. The script (`scripts/test-paste-coalesce-e2e.sh`) spins up an isolated `/tmp` sandbox, launches `sprawl enter` in a detached tmux pane (coalescer ON by default), injects a deterministic 200-character literal burst via `tmux send-keys -l` (which writes the bytes to the inner pty in one batch — matches real paste behavior on tmux <3.4 where bracketed-paste markers are gated), asserts the full payload (`QUM608HEAD…QUM608TAIL`) lands in the pane within 5s (well below the ~30s typewriter-animation budget the bug produces), then sends `C-c` and asserts the sprawl process exits within 10s — guards against deadlocks in the coalescer's `Close` path that would wedge `sprawl enter` and force `kill -9`. Requires a real `claude` binary on PATH; set `SPRAWL_E2E_SKIP_NO_CLAUDE=1` to skip. Guards against the QUM-608 class of regression where pastes regress to one `KeyPressMsg` per byte (typewriter animation) or where the coalescer's stdin lifetime hangs shutdown.
10. **Recover-path changes are mandatory-tested.** Any change touching `internal/supervisor/runtime.go` (Recover / startWithSpec / health probe), `internal/supervisor/real.go` (`Real.Recover` wrapper / `RecoverAgents` post-restart resume path), `internal/backend/claude/adapter.go` (subprocess lifetime / `realStarter.Start` / `Pid()` exposure), `internal/runtime/unified.go` (Done() closure on terminal fault / `SetTerminalErrorHandler` wiring), or `internal/runtime/turnloop.go` must run `make test-recover-live-e2e` and pass before merge. The script (`scripts/test-recover-live-e2e.sh`) rebuilds the sprawl binary with `-tags sprawl_test` so the build-tag-gated `mcp__sprawl___test_induce_wedge` MCP tool is compiled in, spins up an isolated `/tmp` sandbox, launches `sprawl enter` in a detached tmux pane, drives root weave to spawn an engineer child via `mcp__sprawl__spawn`, captures the original `claude` subprocess PID via `pgrep -af 'claude' | awk … $0 ~ <session_id>`, induces a `SubscriberWedged` terminal fault on the child via `mcp__sprawl___test_induce_wedge` (which calls `setTerminalErr(ErrSubscriberWedged)` directly on the named agent's `*backend.session` — surgical and deterministic, no real frame burst or stalled reader required), waits for the fault to surface, then drives weave to call `mcp__sprawl__recover` on the child and asserts the success ack (`Recovered backend session for <child>`) lands in weave's TUI pane. **Phase 4 is the PRIMARY assertion** introduced by QUM-606: 2s after the recover return, a NEW `claude … --resume <session-id>` subprocess (PID ≠ original) must be alive (`kill -0 <new_pid>` succeeds). Pre-QUM-606-fix this assertion fails 100% of the time because `AgentRuntime.Recover` forwarded the MCP request ctx all the way down to `exec.CommandContext`, so the freshly-spawned claude was SIGKILLed the instant `toolRecover` returned. Phase 5 then drives a `mcp__sprawl__send_message` to the child carrying a `RECOVER-PROBE-<token>` sentinel and asserts the sentinel lands in the child's `activity.ndjson` within 60s — proves the recovered session is actually functional, not just alive-but-dead. Requires a real `claude` binary on PATH; set `SPRAWL_E2E_SKIP_NO_CLAUDE=1` to skip. Guards against the QUM-606 class of regression where the supervisor reports `Recovered backend session for X` while leaving a zombie — the user-visible breakage is "I called recover, sprawl said it worked, but the agent never responds again," which silently destroys work in real sessions.
