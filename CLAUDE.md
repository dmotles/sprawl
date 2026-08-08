# CLAUDE.md

This repo IS Sprawl, developed inside itself. The `.sprawl/` directory at the repo
root holds agent state and worktrees — leave its contents alone unless that is your
task. Project overview: `DESCRIPTION.md`, on demand. This file covers how to work here.

## Terminology

- **agent** — a sprawl-spawned process with its own worktree and its own Claude session.
- **sub-agent** — a sprawl-spawned process that shares its parent's worktree (Arc Item #3 model). Persisted as `AgentState.Subagent`.
- **sidechain** — a Claude in-process `Agent`-tool spawn (Explore, Plan, Oracle, TDD agents). On the wire: `isSidechain: true` / `parent_tool_use_id != null`.

These three are distinct. "Sub-agent" must never refer to a Claude Agent-tool spawn — use "sidechain".

## Prohibitions

Each of these was written from an incident. None is advisory.

- **Never `git add -A`, `git add .`, or `git commit -a`.** Stage explicit paths, then review `git diff --cached`. Agent worktrees share a filesystem with other agents' scratch output, so `-A` makes your commit a function of files you never created. `git add -u` is the sanctioned shortcut for large changes.
- **A non-root agent never commits to `main`,** and never `git commit --no-verify`. Two hooks enforce this; do not work around them.
- **Never pass `-c core.hooksPath` to git.** Plain `git commit` is already correct. A `core.hooksPath` that is not a populated directory makes git run **no hooks at all and exit 0**, with no warning — voiding both prohibitions above at once, including the `reference-transaction` backstop that `--no-verify` cannot skip. In a worktree `.git` is a **file**, so `ls .git/hooks` returns `Not a directory`; that is normal, not a missing hook. Hooks live in the shared `$(git rev-parse --git-common-dir)/hooks`, and `make hooks` installs them from the main checkout only. `make validate` now fails when the guard is not armed; `sprawl hooks verify` prints the resolved chain. Mechanism: /git-recovery.
- **Never run `make install`** unless your identity is `weave` or the user asked. Use `make build` and exercise the local `./sprawl` binary.
- **Never `rm -rf` a broad `/tmp` glob** — assert a path is yours and under `/tmp/` before deleting it — and never touch `/tmp/coder-script-data`, which is host tooling state.
- **Never run bare `tmux kill-server`.** Sandbox tmux lives on its own socket; see /e2e-testing-sandboxing.
- **Do not merge, push, or force-push** unless you were told to.

## Build & validate

`make validate` is the gate, and the pre-commit hook runs it. It runs the whole unit suite under `-race`, so a green run means no race was *observed* on the paths the unit tests drive — not that none exists. The `Makefile` is authoritative for what validate depends on; do not trust a prose list of its targets, including any you find in this repo's own docs. `make fmt` auto-fixes formatting; `make hooks` installs the hooks. Target reference and lint/format policy: /sprawl-internals.

**Validating a change is more than running validate.** Smoke-test the built `./sprawl` binary against the commands you touched, and use /e2e-testing-sandboxing for end-to-end runs. **TUI validation is mandatory for all TUI-related changes** — the harness and the manual workflow are in /tui-testing.

## The e2e matrix gate

If your diff touches a gated file, you owe the corresponding e2e matrix rows. The obligation is the **union** of every row matching any path in `git diff --name-only` and every row matching a function you edited — derived by you, from the table, at the commit you are making, never from a list someone handed you. Glob and directory-prefix entries are missed by a literal path grep, so when in doubt include the row: over-running costs a CI slot, under-running ships the defect and comes back green either way. A skipped row discharges nothing. The table, the derivation rules, skip accounting and exit codes: /e2e-matrix.

## Tests and assertions

- **Every new assertion must demonstrate it CAN fail** — a positive control (run it against a subject where the defect IS present; it MUST fire), a mutation, or a red-first run — and you must record which one you used and what it printed. A negative control is the other half: a subject known clean, where the probe MUST stay quiet. Whenever you name either, state its direction — knowing a control's *name* never tells you it is aimed right. An assertion nobody has watched fail is a claim, not a check. A parent-commit control proves a failure is *pre-existing*; it never proves the failure is acceptable.
- **No fallback branch may silently succeed.** A validation or test script must exit non-zero when something it checks fails, and a skip on an unmet precondition must exit **77**, never 0. Any harness that aggregates its own results needs an **assertion-count floor**, so a run reporting `0 passed / 0 failed` exits non-zero instead of green. **Every row under `scripts/e2e-tests/` declares a top-level `MIN_ASSERTIONS=<n>`** — the shared aggregator enforces it and fails the row if the declaration is missing, zero, or unmet. The floor is per-row and caller-supplied, never derived from an aggregate: an aggregate floor is satisfied by `0 == 0`.
- Every **new** file under `cmd/` and `internal/` must ship with a sibling `_test.go`. Stated as a requirement on new work, not as a census of the tree: the census form of this claim was false, and a requirement about future behaviour cannot rot the way a count of current files does.

Read /testing-practices § Assertion Rigor before writing or reviewing any assertion.

## `Not logged in`

A `claude` you launched from a Bash subshell failing with `Not logged in` means its auth env was stripped, not that the product regressed. The fix is the `scripts/run-claude` shim plus a repo-root `.env`; setup is in /e2e-testing-sandboxing. Do not work around it by hiding `claude` from PATH.

## Public vs private repo hygiene

Determine whether this repo is public before any commit, merge, or PR (`git remote get-url origin`, `gh repo view --json visibility`). **Default to PUBLIC unless you can confirm otherwise** — a repo named after or branded with a company is not proof of privacy.

In a public repo, never commit content naming or describing the user's employer's internal systems, products, codenames, repo names, host aliases, customers, internal URLs, deployment topology, or operational specifics. That material goes in `.sprawl/agents/<name>/findings/` (gitignored), not the tracked tree. Forensic, debug, and incident artifacts captured from real production systems are especially likely to carry it — default them to gitignored unless explicitly sanitized. In a private repo this is looser, but still do not mix one employer's context into another's repo.

This applies to every agent. Reviewers must flag suspected leaks and refuse to merge until resolved. When in doubt, ask before committing.

**`guard-employer-leak` exits 3 when its own self-checks did not fire. A 3 is a failed scan, never a clean one** — do not read it as a 0, and do not work around it. The script header carries the full exit-code table and the list of what the scan does *not* examine.

## Linear

Work is tracked in Linear; see `CLAUDE.local.md` for the team and project IDs. Invoke /linear-issues before creating or updating an issue — it defines required fields that are easy to miss. When working an issue: set it In Progress and comment that you picked it up, post findings, decisions and blockers as comments while you work, then set it Done with a summary. When spawning an agent for an issue, keep the prompt short and point it at the issue — the issue is the source of truth, not your paraphrase of it.

## Skills

Not loaded for you. Read one when its condition applies. A skill holds the *procedure*; the *requirement* stays here — a rule you can only find after loading the skill it lives in is not reliably discoverable. That is why the TUI-validation mandate and the `scripts/run-claude` pointer above are stated in this file rather than delegated. **Do not tidy them away.**

- /sprawl-internals — agent lifecycle, status/`IsTerminal`/wake contracts, repo layout, DI pattern, build targets, project config, install policy.
- /testing-practices — before writing any test or any new assertion.
- /e2e-matrix — before running e2e tests or calling a change validated.
- /e2e-testing-sandboxing — before any sandbox run, any `tmux` command, or any harness work.
- /tui-testing — before changing the TUI; TUI validation is mandatory there.
- /git-recovery — before `reset`, `rebase`, `branch -f`, `commit --amend`, or a merge, and when git state has already gone wrong.
- /false-red — when a build, validate, test, or merge just failed and you are about to blame your diff.
- /go-cli-best-practices — before writing or modifying Go code.
- /cli-ux-best-practices — before adding or changing a CLI command's behaviour.
- /linear-issues — before creating or managing issues.
- /handoff — at the end of a weave session; weave only.
