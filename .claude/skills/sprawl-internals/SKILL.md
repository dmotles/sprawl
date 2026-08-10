---
name: sprawl-internals
description: Codebase orientation and runtime contracts for the sprawl repo. Read when you need the agent lifecycle / Status / IsTerminal / wake contract, the repo layout and agent types, the dependency-injection code pattern, the full build and test target reference, `.sprawl/config.yaml` semantics, the linting and formatting rules, or the `make install` policy.
---

# Sprawl Internals

Orientation and runtime contracts for working in this repository. Relocated
here from `CLAUDE.md` (QUM-1155) so it is loaded when needed rather than on
every turn. Read `DESCRIPTION.md` for project context.

## Lifecycle model (QUM-786)

Authoritative rules for agent Status / `IsTerminal` / wake plumbing. If you
touch `internal/state/state.go`, `internal/supervisor/{runtime,real}.go`,
or any MCP verb that targets an agent by name, this is the contract.

- `StatusComplete` ("complete") is a **resting state** — runtime torn down;
  `session_id` / `branch` / `worktree` preserved; **revivable**. It is **not
  terminal**. Since QUM-1186 an agent cannot put itself here by reporting
  complete; there is no self-report tool. It is set by the supervisor.
- `StatusIdle` ("idle") is the resting state for an agent whose runtime was
  reclaimed for **inactivity**, not because its work finished (QUM-1186 D2).
  Session, worktree and branch are preserved and it revives **on demand**,
  when someone messages it. It is deliberately NOT `StatusComplete`: complete
  claims the agent finished, and an idle agent may be mid-task and merely
  quiet. It projects onto `liveness.Suspended`, is in the merge allow-set and
  the `send_message` auto-wake set, and is deliberately **excluded from the
  boot auto-resume loop** in `RecoverAgents` — resuming reclaimed agents at
  every `sprawl enter` would hand back the exact RSS the reaper freed.
  **Nothing writes this status by default today**: the idle reaper
  (`internal/supervisor/idlereap.go`) ships disabled — `idle_reclaim.after`
  defaults to `0` — because its predicate cannot see an agent that is
  mid-tool-call and reaps it (QUM-1197, which also blocks QUM-1187). The
  status, the machinery and its coverage are all live; only the switch is off.
- `IsTerminal(status)` returns true **only for `{retired, retiring}`**.
  Permanent termination is a deliberate parent action (`retire`/`kill`),
  never a side effect of anything the agent said about itself. Everything
  else (`complete`, `idle`, `paused`, `faulted`, `died`, `killed`,
  `resume_failed`) is revivable in spirit and the code must treat it that way.
- `StatusStopped` is **retired as a write target**; it is parsed only for
  legacy state files and migrated to `StatusSuspended` on load.
- **Liveness is observed, never asserted.** Every liveness answer sprawl gives
  traces to an observation of a process — `ProcessAlive`, `SubprocessAlive`,
  `EventbusSubscribed`, `InTurn` — and to nothing an agent claimed about
  itself. An observation that is *unavailable* is not a negative observation:
  an unknown `InTurn` must be treated as in-turn, never as idle.
- `send_message(complete|idle agent, body)` **auto-wakes** with no flag —
  `wake_if_offline` is not required and not consulted.
- `send_message(paused|faulted|died|killed|resume_failed agent, body)` requires
  explicit `wake_if_offline=true` and surfaces the canonical
  `"is <state> ... wake_if_offline"` error otherwise.
- `send_message(retired|retiring agent, body)` errors. The specific class
  depends on whether `state.json` still exists: `TerminalAgentError`
  (`"… no longer running"`) during the brief `retiring` window or for
  legacy zombies; `"agent %q not found"` once `retire` has deleted the
  state file (`internal/agent/retire.go:82`). Both are valid terminal
  signals — the contract is "it fails clearly," not a specific error string.
- `wake` accepts everything **except `{retired, retiring}`**.

Touched-file matrix-row mapping for these set-sites lives in the e2e matrix
table at `.claude/skills/e2e-matrix/SKILL.md` (`complete-lifecycle` row).

## Build & Test

```bash
make              # runs full validation, in order: build + proto-check + fmt-check
                  #   + lint + test-race-gate + test-race + wirelog-helpers-unit
                  #   + e2e-matrix-unit + gitignore-classes + leak-scan
                  #   (race-gate runs BEFORE test-race on purpose: it takes ~2s and
                  #    fails fast on exactly the regression that would make the
                  #    ~2min race run stop measuring anything)
make validate     # same as above — the default target
make build        # builds ./sprawl binary
make fmt          # auto-fix formatting
make fmt-check    # check formatting without fixing (used in CI/hooks)
make lint         # run golangci-lint
make test         # run all unit tests WITHOUT -race — a convenience run, NOT what validate uses
make test-race    # go test -race ./... — THE enforced gate; validate depends on this, not `test`
make test-race-gate  # shell unit test proving validate's go-test invocation still carries -race,
                     # and that -race really detects a planted race in this toolchain
make test-e2e-matrix-unit  # shell unit tests for the e2e matrix driver (fast, no claude)
make hooks        # install pre-commit hook

make test-wirelog-helpers-unit   # bash+jq unit tests for the e2e rows' wire-log
                                 # counter helpers; part of `make validate`

scripts/smoke-test-memory.sh   # integration test for weave memory system
scripts/sprawl-test-env.sh     # set up isolated test environment
```

Two notes on targets that are deliberately *not* in `validate`, accurate as of
this tree — do not read them as pending work of your own:

- `make always-loaded-budget` is the LIVE always-loaded instruction-budget
  gate. It is parked outside `validate` on purpose; `Makefile` carries the two
  reasons. Its fixture-only unit suite, `make test-always-loaded-budget-unit`,
  IS in `validate`, which keeps the mechanism from rotting while the gate
  waits — the same split as `test-race-gate` guarding `test-race`.
- `make test-e2e-matrix` and its per-row targets are never in `validate`; they
  need a real authenticated `claude`. See `.claude/skills/e2e-matrix/SKILL.md`
  for when you owe a row.

## Install

> **Warning:** Do not run `make install` unless your agent identity is `weave` or the user explicitly asks you to. Other agents should only use `make build`, then test against the locally built `./sprawl` binary using temporary directories with overridden environment variables (e.g. `SPRAWL_ROOT`, `SPRAWL_AGENT_IDENTITY`) to exercise the tool.

## Project Configuration

Sprawl reads `.sprawl/config.yaml` for project-level settings:

```yaml
validate: "make validate"   # command run on the rebased tree to validate a merge
```

Since QUM-1087 this is **not** post-merge validation: the engine rebases the
agent's branch, runs this command on the rebased tree **in the agent's own
worktree**, and only fast-forwards the parent if it passes. A failure leaves the
parent's SHA byte-identical. If no config file exists or the `validate` key is
absent, validation is skipped with a warning. Use `--no-validate` on `sprawl
merge` to explicitly skip it.

## Repo Layout

- `cmd/` — CLI commands (cobra). Each command has its own file + test file.
- `internal/agent/` — Claude Code launcher, agent name allocation, prompt building
- Agent types: `engineer` (writes code), `researcher` (investigates, writes findings), `manager` (orchestrates), `qa` (verifies an engineer's work against ACs).
- `internal/config/` — Project configuration loading (`.sprawl/config.yaml`)
- `internal/supervisor/` — same-process child runtime registry and orchestration
- `internal/state/` — Agent state persistence (JSON files in `.sprawl/agents/`)
- `internal/worktree/` — Git worktree creation for agent isolation

## Code Patterns

**Dependency injection**: Commands use a `deps` struct to inject interfaces for external dependencies (backend processes, git, env vars, filesystem). See `cmd/gc.go` or `cmd/usage.go` for the command-local shape. Agent operations keep theirs in `internal/agentops` as exported `XxxDeps` with nil-defaulting accessors (`internal/agentops/report.go`), which the command aliases — `cmd/merge.go` is `type mergeDeps = agentops.MergeDeps`. This enables testing without real subprocesses.

**Read `/go-cli-best-practices` before writing or modifying Go code** — it covers cobra patterns, error handling conventions, and dependency injection structure used throughout this codebase.

**Read `/cli-ux-best-practices` before adding or modifying any CLI command's behavior** — it covers output design for agent consumers, the "next action hint" pattern, error message design, and idempotency. Every command must tell the calling agent what to do next.

## Linting & Formatting

This project uses [golangci-lint v2](https://golangci-lint.run/) with `gofumpt` formatting. Configuration is in `.golangci.yml`.

* **All code must pass** `make validate` before committing. The pre-commit hook enforces this.
* Run `make fmt` to auto-fix formatting issues.
* Run `make hooks` after cloning to install the pre-commit hook.
