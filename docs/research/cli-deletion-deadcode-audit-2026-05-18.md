# Dead-Code Audit After CLI Deletion (QUM-568, Phase 2.3d)

**Author:** ghost (researcher under weave)
**Date:** 2026-05-18
**Base tip:** `371e9f1` (main)
**Scope:** Identify code that became dead after Phase 2.3a/b/c deleted the
deprecated standalone CLI surface (`delegate`, `handoff`, `kill`, `messages`,
`report`, `retire`, `spawn`, `status`, `tree`) in commit `b3f506d` (QUM-566) and
the prior tmux-mode + same-process runtime work (QUM-346/347/350-354/573).

This document is **research-only**. Followup engineer issues will perform the
deletions.

## TL;DR

The Phase 2.3 deletion wave was tight; surprisingly little cruft remains.
The audit found roughly **~80 LOC of production dead code** spread across 6
exports in 3 files, plus **~250 LOC of associated tests**, plus 2 stale
comments. The deletions are small, clustered, and only lightly overlap.
**Recommendation: fold into a single engineer-level cleanup issue.**

| # | Surface | Dead Symbol(s) | Prod LOC | Test LOC | Confidence |
|---|---------|----------------|---------:|---------:|------------|
| 1 | `cmd/deprecation.go` | — | 0 | 0 | already inlined into `cmd/color.go` |
| 2 | `internal/state/state.go` | `WriteNamespace`, `WriteRootName`, `UsedNames` | 29 | ~40 | high |
| 2 | `internal/state/tasks.go` | `NextTask` | 13 | ~120 | high |
| 2 | `internal/state/state.go` | `SprawlDir` (transitive: only used by the two dead writers above + Read* readers) | 3 | 0 | medium |
| 3 | `internal/supervisor/*` | — | 0 | 0 | clean, no dual-paths |
| 4 | `internal/agentops/helpers.go` | `FindSprawlBin` | ~10 | 44 (file) | high |
| 4 | `internal/agentops/spawn.go` | `Spawn` (trivial passthrough to `PrepareSpawn`) | 4 | small | high |
| 5 | messaging/notifier plumbing | — (stale comments only) | 0 | 0 | clean functionally |
| 6 | `.sprawl/config.yaml` worktree hooks | — | 0 | 0 | clean |
| 7 | `cmd/root.go` | — | 0 | 0 | clean (22-line stub) |
| 8 | test helpers | — | 0 | 0 | clean |
| bonus | `cmd/weavelock.go` | `acquireOfflineLifecycle` + unused `namespace` param of `printWeaveLockError` | ~17 | ~50 (file) | high |
| bonus | "zombie reader" smell | `state.ReadNamespace`, `state.ReadRootName` are now no-op-equivalent in fresh installs | — | — | followup investigation |
| **Total** |  |  | **~76 LOC prod** | **~254 LOC tests** | |

---

## Method

For each candidate symbol I ran:

1. `grep -n "symbol" --include='*.go'` repo-wide, then
2. partitioned hits into (a) the defining file/test, (b) other test files, and
   (c) live non-test production callers.

A symbol is flagged **dead** only when it has **zero live production
callers** (cat c is empty). Five parallel `Explore` sub-agents combed the 8
candidate surfaces; spot-checks then verified or refuted each claim against
the live tree (multiple agent claims were corrected — see §"Surprises").

---

## Surface 1 — `cmd/deprecation.go`

**Status: already resolved.** The deprecation-banner helper was inlined into
`cmd/color.go:15-36` as part of QUM-566. The file no longer exists. The
banner's only remaining caller is `cmd/color.go` itself. No action.

---

## Surface 2 — `internal/state/*`

### Dead exports

**`internal/state/state.go`**

* **`WriteNamespace`** (`state.go:153-160`, 8 LOC). Zero non-test callers.
  Persisted `.sprawl/namespace` for the old `sprawl init` parent (QUM-346,
  deleted). The reader (`ReadNamespace`) survives but now always returns the
  empty string in fresh installs — see "zombie reader smell" below.
* **`WriteRootName`** (`state.go:174-181`, 8 LOC). Zero non-test callers.
  Only `internal/observe/observe_test.go:48` uses it as test setup. Same
  zombie-reader pattern as namespace.
* **`UsedNames`** (`state.go:209-220`, 13 LOC). Zero callers. The historical
  consumer was `AllocateName`, but `internal/agent/names.go:62` no longer
  uses it (the file-listing happens inline). The lone surviving reference is
  the symbol's own test (`state_test.go:190`).
* **`SprawlDir`** (`state.go:122-124`, 3 LOC). Used internally only by
  `WriteNamespace`/`ReadNamespace`/`WriteRootName`/`ReadRootName`. With the
  two `Write*` functions gone, `SprawlDir` becomes a 3-line helper used only
  by the two readers. Cleanest: inline at the two reader sites and drop
  `SprawlDir`. (Optional; the helper is harmless.)

**`internal/state/tasks.go`**

* **`NextTask`** (`tasks.go:82-93`, 13 LOC). Zero production callers. Per
  `docs/research/qum-488-delegate-wake-investigation.md:44`, the unified
  runtime never touched it; the legacy `agentloop.Runner` poll that did was
  retired in QUM-488/QUM-562 work. Only its own table-test
  (`tasks_test.go`) references it.

### File-lock / cross-process coordination

The audit specifically looked for `flock`, `syscall.Flock`, `.lock` files,
or shared mutexes in `internal/state/`. **None found.** The CLI-vs-supervisor
race that those would have guarded was eliminated by deleting the CLI.
There is no file-lock cruft to remove here.

### Zombie reader smell (followup, NOT in scope to delete)

`ReadNamespace` (1 prod caller — `agentops/spawn.go:171`) and `ReadRootName`
(1 prod caller — `agentops/spawn.go:181`) are still live readers, but
**nothing in production writes the underlying files anymore.** They always
return `""` on a fresh install, which means:

* `agentops/spawn.go:168-174` collapses from "env var > file > default" to
  "env var > default".
* `agentops/spawn.go:179-184` (whatever uses `ReadRootName`) is in the same
  shape.

This is not strictly dead code (a long-running sandbox could still have a
stale on-disk file), but the fallback branch is effectively unreachable in
practice. **Recommend a separate, narrower followup issue** to either:

1. Resurrect the writers (if persisted namespace/root-name is wanted), or
2. Delete the reader fallback paths and rely on env/default only.

This is out of scope for QUM-568 because it requires a product decision, not
a mechanical sweep.

---

## Surface 3 — `internal/supervisor/*` dual-paths

**Clean.** The `Explore` sweep found no `if supervisor == nil` /
"standalone" / "offline" branches in `internal/supervisor/`. The MCP tool
handlers in `internal/sprawlmcp/server.go` likewise have no
supervisor-presence checks — the design invariant is that the supervisor is
always running when MCP tools fire.

No code to remove.

---

## Surface 4 — `internal/agentops/*`

### Dead exports

* **`agentops.FindSprawlBin`** (`helpers.go`, ~10 LOC). Locate the `sprawl`
  binary on disk — used by the old CLI commands that needed to re-exec for
  spawn/agent-loop subprocesses. Only `cmd/sprawl_bin_test.go` (44 LOC, the
  entire test file) references it now. After QUM-352/354, no production
  code shells out to `sprawl`; the in-process supervisor calls Go functions
  directly.

* **`agentops.Spawn`** (`spawn.go:255-260`, 4 LOC). A trivial 3-line wrapper
  that just calls `PrepareSpawn`. No callers — `supervisor/real.go:236`
  uses `PrepareSpawn` directly. Safe to delete and remove from any
  remaining test that imports it.

### Internal-but-exported helpers (not dead, candidate for downcase)

These are exported but only used inside `package agentops`. Downcasing them
is a tidiness improvement, not dead-code removal:

* `IsValidType`, `IsValidFamily` (`spawn.go:53,63`) — used at `spawn.go:74,82`
* `ValidReportState` (`report.go:73`) — used at `report.go:98`

**Out of scope** for QUM-568 but worth noting.

### What stays live

`Merge`, `MergeDeps`, `MergeOutcome` (real users: `cmd/merge.go` +
`supervisor/real.go`); `Retire`, `RetireDeps`, `Kill`, `KillDeps`,
`Report`, `ReportDeps`, `ReportResult`, `PrepareSpawn`, `SpawnDeps`,
`ValidTypes`, `ValidFamilies`, `SupportedTypes`, and the `Real*` git/script
helpers (`GitCurrentBranch`, `RunBashScript`, `RealGitBranchDelete`,
`RealWorktreeRemove`, `RealGitBranchIsMerged`, `RealGitBranchSafeDelete`,
`RealGitStatus`, `RealGitUnmergedCommits`, `RealBranchExists`) all have
real callers in `supervisor/real.go` and/or `cmd/merge.go`.

The earlier `Explore` claim that `Report`/`ReportDeps`/`ReportResult`/
`ValidReportState` had zero callers was **wrong**:
`supervisor/real.go:1029` calls `agentops.Report(&agentops.ReportDeps{}, …)`
through `ReportStatus`. They are live.

---

## Surface 5 — Notification / messaging plumbing

**Functionally clean.** A whole-repo grep for tmux/legacy/cli-fallback
breadcrumbs found:

* `cli-fallback` / `cli_fallback` / `cliFallback`: **0 matches** ✓
* `SPRAWL_MESSAGING`: **0 matches** (already removed in QUM-347) ✓
* `.wake` / `wakeFile` / `.kill` / `killFile` sentinel-file refs: **0** ✓
* `poke` CLI artifacts: **0** ✓
* `tmux` Go source refs: only 4, all are accurate historical comments
  pointing at deleted-but-relevant prior behavior
  (`cmd/enter.go:139`, `cmd/enter.go:167`, `internal/supervisor/runtime.go:250`,
  `internal/rootinit/tools.go:3`).
* `agent-loop` / `agentloop`: 37 matches, all inside the live
  `internal/agentloop/` package, supervisor, or its tests. The CLI command
  was deleted; the package (queue/activity/session_spec helpers used by the
  in-process runtime) remains live.

`internal/messages/messages.go` has a single unified delivery path
(no tmux-mode-vs-TUI branching). Notifier registration flows:
`cmd/enter.go:598` → `messages.SetDefaultNotifier` → `cmd/enter_notify.go:23`
→ `tui.InboxArrivalMsg`. No legacy fork.

### Stale comments (cosmetic — flag, do not block)

* `internal/messages/messages.go:69` — comment still references "tmux
  wiring"; should say "TUI wiring".
* `cmd/enter.go:125` — comment "No-op in legacy mode (registry never holds
  weave there)" references a mode that no longer exists.

These do not affect behavior. Roll into the cleanup PR if convenient.

---

## Surface 6 — `.sprawl/config.yaml` worktree hooks

Inspected. Contains:

* `worktree.setup` — copies `.env` and `CLAUDE.local.md` into new worktrees.
  Live, required (QUM-518 dependency).
* `worktree.teardown` — no-op.
* `validate` — `make validate` for post-merge validation. Live.

**Clean.** No CLI-mode-only invariants.

`scripts/` directory: no shell scripts reference deleted CLI commands.

---

## Surface 7 — `cmd/root.go`

The file is 22 lines, minimal cobra setup. No flag handlers,
PersistentPreRun, or error formatters that were CLI-command specific.
**Clean.**

---

## Surface 8 — Test helpers

* `cmd/mocks_test.go`, `cmd/scripts_test.go`, `cmd/sprawl_bin_test.go`,
  `cmd/init_removed_test.go`, `cmd/hosttest/main.go`: each was inspected.
  None contain helpers used only by deleted commands' tests.
* `internal/testing/`: directory does not exist.
* `internal/tui/testutil_test.go`: tiny ANSI-strip helper, used by live
  TUI tests.
* `cmd/skills_sync_test.go:11-13` — the `codexSkillWhitelist` entry for
  `handoff` is **NOT** dead. The `handoff` *CLI command* was deleted, but
  `.claude/skills/handoff/SKILL.md` is a live weave-only skill backed by the
  `mcp__sprawl__handoff` MCP tool. The whitelist exempts it from the
  Codex-mirror requirement because Codex sessions cannot invoke sprawl MCP
  tools. (The earlier exploratory agent flagged this incorrectly — verified
  by reading the skill file.)

Single exception is the `cmd/sprawl_bin_test.go` file noted in Surface 4 —
the test file itself is the only consumer of a dead symbol, so it goes
together with `FindSprawlBin`.

---

## Bonus surface — `cmd/weavelock.go`

Not in the candidate list but the test helpers sweep surfaced it.

* **`acquireOfflineLifecycle`** (`weavelock.go:41-55`, 15 LOC). The function
  body itself proves it's dead: it returns
  `"standalone \`sprawl %s\` is unavailable while \`sprawl enter\` is running"`
  — but `%s` was always one of the now-deleted CLI commands. The only
  remaining references are in `cmd/weavelock_test.go:14,16,38` (which is
  itself the only test of the function). Recommend deleting the function +
  its dedicated test, while keeping `printWeaveLockError` (which is live at
  `cmd/enter.go:480`).

* **`printWeaveLockError` `namespace` parameter** (`weavelock.go:20`).
  Already discarded at line 24 (`_ = namespace`) with a comment admitting
  it's "retained for compatibility with older callers" — those callers are
  gone. The lone caller (`cmd/enter.go:480`) currently passes
  `deps.getenv("SPRAWL_NAMESPACE")` for it. Drop the parameter (and the
  comment) when removing `acquireOfflineLifecycle`.

---

## Surprises (worth flagging during follow-up planning)

1. **`state.ReadNamespace` / `state.ReadRootName` are zombie readers.** No
   production code writes the underlying files anymore (since QUM-346
   killed `sprawl init`), yet the read-with-fallback pattern still exists
   at `agentops/spawn.go:171,181`. This is a separate product question —
   not mechanical dead-code — and deserves its own followup.

2. **`agentops.Spawn` is a 3-line passthrough to `PrepareSpawn`.** Tiny
   thing, but it suggests the public surface was never fully reconciled
   after the QUM-352/QUM-354 cutover that introduced `PrepareSpawn`.

3. **`cmd/weavelock.go`'s `acquireOfflineLifecycle` error message still
   references the deleted CLI commands by name.** Mechanical fingerprint of
   incomplete deletion in QUM-566 — easy to miss because the file was not
   explicitly named in QUM-568's candidate list.

4. **Several internal `agentops` validators are exported but only used
   in-package** (`IsValidType`, `IsValidFamily`, `ValidReportState`). Not
   dead, but a tidiness opportunity.

---

## Open questions

* Should `state.WriteNamespace` / `state.WriteRootName` be resurrected
  (i.e., make `sprawl enter` persist these for future readers) or should
  the reader-side fallback be removed too? Out-of-scope for this audit;
  warrants a separate decision.
* Could the `Read*` helpers (`ReadNamespace`, `ReadRootName`) be wired to
  the supervisor's in-memory state instead of disk? Same answer — separate
  followup.

---

## Recommendation

**One engineer issue, single PR.** The deletions are concentrated in three
files (`internal/state/state.go`, `internal/state/tasks.go`,
`internal/agentops/helpers.go` + `spawn.go`, `cmd/weavelock.go`) and four
test files. Total touch surface ~330 LOC. No tricky cross-file refactors
required.

Suggested followup engineer-issue shape:

> **Title:** Phase 2.3e: Delete dead exports surfaced by QUM-568 audit
>
> **Scope:**
> 1. Delete `state.WriteNamespace`, `WriteRootName`, `UsedNames`, `NextTask`,
>    and `SprawlDir`; update `internal/observe/observe_test.go:48` to
>    write the file directly (or remove the setup step if observe no longer
>    needs it).
> 2. Delete `agentops.FindSprawlBin` and `cmd/sprawl_bin_test.go`.
> 3. Delete the `agentops.Spawn` passthrough; verify no external callers.
> 4. Delete `cmd/weavelock.go`'s `acquireOfflineLifecycle` and the matching
>    tests in `cmd/weavelock_test.go`; drop the `namespace` parameter from
>    `printWeaveLockError` and update `cmd/enter.go:480`.
> 5. (Optional) Fix the two stale comments in `messages.go:69` and
>    `enter.go:125`.
>
> **Validation:** `make validate` + all four mandatory e2e harnesses
> (notify, handoff, merge, ask-user-question).

If dmotles/weave prefers smaller blast-radius PRs, the deletions split
cleanly along package boundaries (state / agentops / cmd-weavelock) into
2–3 issues, but the surfaces don't share files and there is no overlap
risk, so the single-issue path is cheaper.

### File-overlap matrix (if split)

| Engineer issue | `state.go` | `tasks.go` | `agentops/helpers.go` | `agentops/spawn.go` | `weavelock.go` | `enter.go` |
|----------------|:----------:|:----------:|:---------------------:|:-------------------:|:--------------:|:----------:|
| A (state)      | ✓          | ✓          |                       |                     |                |            |
| B (agentops)   |            |            | ✓                     | ✓                   |                |            |
| C (weavelock)  |            |            |                       |                     | ✓              | ✓ (drop arg) |

The only file touched by more than one slice would be `cmd/enter.go`, and
only in C (single-line signature change).

---

## QUM-314 closure outlook (bonus)

Open sub-issues under QUM-314 after QUM-568 + QUM-569 land:

* **QUM-362** — automate same-process child runtime sandbox smoke (Backlog).
* **QUM-364** — automate no-tmux enter shutdown + standalone CLI smoke
  (Backlog).

Both are *test-coverage* gaps for work already shipped (QUM-352/QUM-354 are
Done). The Phase 2 acceptance criteria in QUM-314 do not enumerate
automated smoke coverage as a hard gate — they require the architecture
itself to be in place, which it is.

**Recommendation:** QUM-314 can close after QUM-568's cleanup + QUM-569's
e2e restore. QUM-362 and QUM-364 should be retargeted or left open as
standalone coverage backlog items; do not block parent closure on them.

That is a judgment call for dmotles. The mechanical situation: Phase 2's
code goal is met; only optional automated-smoke hardening remains.

---

## Appendix — Citations used

* `internal/state/state.go:122-160` — `SprawlDir`/`WriteNamespace`/`ReadNamespace`
* `internal/state/state.go:173-192` — `WriteRootName`/`ReadRootName`
* `internal/state/state.go:208-220` — `UsedNames`
* `internal/state/tasks.go:81-93` — `NextTask`
* `internal/agentops/helpers.go` — `FindSprawlBin` (only ref:
  `cmd/sprawl_bin_test.go:12,24,36`)
* `internal/agentops/spawn.go:255-260` — `Spawn` (3-line passthrough)
* `cmd/weavelock.go:20-39` — `printWeaveLockError`
* `cmd/weavelock.go:41-55` — `acquireOfflineLifecycle`
* `cmd/enter.go:480` — sole live caller of `printWeaveLockError`
* `cmd/color.go:15-36` — inlined deprecation banner (replaces deleted
  `cmd/deprecation.go`)
* QUM-566 commit `b3f506d` — Phase 2.3b CLI deletion
* QUM-565/566/567 status: Done (verified via Linear)
* `docs/research/qum-488-delegate-wake-investigation.md:44` — confirms
  `NextTask` is unreferenced by unified runtime
