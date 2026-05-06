# tmux→TUI Cruft Audit (2026-05-06)

**Author:** ghost
**Branch:** `dmotles/tmux-tui-cruft-audit`
**Goal:** Inventory dead/vestigial/duplicated code left behind by the tmux→TUI
migration so a cleanup wave can be planned. **Scope:** *what's left*, not
"delete tmux" (that's the QUM-314 umbrella). Tmux stays; this catalogues the
other cruft that will become deletable (or already is) once you accept the
post-QUM-400 reality.

## TL;DR

QUM-400 deleted the legacy runtime ledger (Bridge production code, JSONL poll
fallback, runtime_launcher legacy path, SPRAWL_UNIFIED_RUNTIME). Net -4313 LOC.
But ~3 kLOC of *test-only* dead-shape and several hundred LOC of unused
production fields/types ride on the tail. Skills (agent-facing docs) are also
out of sync.

Five gaps not currently tracked in Linear:

1. **`internal/tmux` is fully dead** — no Go importer in the repo. Pure delete.
2. **`internal/agentloop` legacy `Process`/`RealCommandStarter`** — pure
   subprocess wrapper from the old runner. No production caller; ~1.5 kLOC of
   tests still keep it alive.
3. **`claude.LaunchOpts` carries 5 dead "Interactive / tmux mode" fields** —
   `InitialPrompt`, `Tools`, `Bare`, `DangerouslySkipPermissions`, `Name`.
4. **`cmd/enter.go::buildEnterLaunchOpts`** is dead in production
   (only `cmd/enter_args_test.go` exercises it). Replaced by `buildEnterSessionSpec`.
5. **Skills docs reference deleted types** — `tmux.Runner`, `TmuxSession`,
   `TmuxWindow`, `tmux.FindTmux`, `sprawl init` examples. Agents reading these
   skills get a wrong mental model of the codebase.

Sixth pre-existing observation: `rootinit.ModeTmux` constant is dead in
production but kept alive by ~30 test sites. Bundled into recommendation #4
(refactor in lockstep with QUM-322).

## Methodology

1. Fetched the existing 5 issues (QUM-318, 321, 322, 473, 490) and confirmed
   QUM-318 already shipped (`sprawl poke` removal completed 2026-04-30).
2. `grep`-swept the tree for `tmux|Tmux|TMUX`, `Legacy|legacy`,
   `SPRAWL_MESSAGING`, `SPRAWL_UNIFIED_RUNTIME`, plus probed each `internal/`
   package for cross-package importers via `"github.com/dmotles/sprawl/..."`.
3. For each candidate hot spot, traced production reachability vs test-only
   reachability. The interesting cruft is "kept alive by tests."
4. Spot-checked existing issues (QUM-321/322/473/490) against current code.

Time: ~75 minutes.

## Categorised findings

Each finding lists effort estimate (XS/S/M/L), risk, and Linear status.

### A. Pure dead code (safe to delete, no behavior change)

#### A1. `internal/tmux` package — no Go importer in the repo

* `internal/tmux/tmux.go` (107 LOC) + `internal/tmux/tmux_test.go` (245 LOC).
* No `import "github.com/dmotles/sprawl/internal/tmux"` anywhere in the tree.
  `cmd/sandbox_gc.go` shells out to the `tmux` binary directly via
  `exec.CommandContext`, not via this package.
* The `Runner` interface (`NewSession`, `KillSession`, `SendKeys`,
  `CapturePane`, `ListSessions`, `SetOption`, `ResizeWindow`) and `RealRunner`
  describe a runtime that no longer exists.
* Test file uses `exec.LookPath("tmux")` as a skip-guard — also dead once the
  package is gone.
* **Effort: XS.** Delete the package.
* **Risk: low.** Verify no `_test.go` file imports it (none do). CI will catch
  any miss instantly.
* **Linear: NEW issue (file under cruft cleanup).**

#### A2. `cmd/enter.go::buildEnterLaunchOpts` is dead in production

* `enterMain` (cmd/enter.go) constructs `backend.SessionSpec` via
  `buildEnterSessionSpec` and starts the session through `adapter.Start` (line
  442). It never calls `buildEnterLaunchOpts`.
* `buildEnterLaunchOpts` and the `claude.LaunchOpts` it returns are referenced
  only by `cmd/enter_args_test.go` (7 callsites).
* The function exists as a relic of the pre-`backend.Adapter` flow.
* **Effort: XS.** Delete the function and its tests.
* **Risk: low.** The behaviour those tests assert (Resume omits SystemPromptFile,
  AllowedTools concatenation, etc.) is already covered structurally by
  `buildEnterSessionSpec` tests in `cmd/enter_backend_test.go`.
* **Linear: NEW issue.**

#### A3. `claude.LaunchOpts` "Interactive / tmux mode" fields

`internal/claude/launch.go:24-33` keeps these fields:

| Field | Production caller? |
|---|---|
| `InitialPrompt` | ❌ (separate `runtime.LoopCfg.InitialPrompt` is unrelated) |
| `Tools` (`--tools`) | ❌ (production passes `AllowedTools` only) |
| `Name` | ❌ |
| `Bare` (`--bare`) | ❌ |
| `DangerouslySkipPermissions` | ❌ |

The only production builder of `claude.LaunchOpts` is
`internal/backend/claude/adapter.go:71`, which sets only the subprocess-mode
subset (`Print`, `InputFormat`, `OutputFormat`, `Verbose`, `Model`, `Effort`,
`PermissionMode`, `SessionID`, `SystemPromptFile`, `AllowedTools`,
`DisallowedTools`, `Agents`, `Resume`).

The dead fields all have corresponding `BuildArgs` branches that emit tmux-mode
CLI flags. They round-trip in tests (`real_starter_test.go`) but no one sets
them.

* **Effort: S.** Delete fields, update `BuildArgs`, fix any test still relying
  on the round-trip. Trivially blocked on A4 (so the agentloop tests don't
  re-introduce a need for them).
* **Risk: low.**
* **Linear: NEW issue (bundled with A4).**

#### A4. `internal/agentloop/process.go` + `real_starter.go` — legacy subprocess Process

* `agentloop.Process` (process.go, 370 LOC), `RealCommandStarter` (real_starter.go,
  82 LOC), and the `MessageReader`/`MessageWriter`/`WaitFunc`/`CancelFunc`/
  `CommandStarter`/`Observer`/`ProcessConfig`/`Option`/`WithObserver`/
  `ProcessState` types all live here.
* External callers across the repo: **0 production, 0 non-test.** Only
  `process_test.go` (962 LOC) and `real_starter_test.go` (138 LOC) reference
  `NewProcess` / `RealCommandStarter`.
* The unified runtime path goes
  `runtime_launcher.go → agentloop.BuildAgentSessionSpec → unifiedAdapterStartFn
  → backendclaude.NewAdapter`. `agentloop.Process` is bypassed entirely.
* Only items still used outside the package: `ObserverWriter` (used in
  `runtime_launcher.go` and `weave_handle.go`), `BuildAgentSessionSpec`,
  `NewActivityRing`, `ActivityPath`, `Enqueue`, `MarkDelivered`, plus the
  `Class`/`Entry` aliases. Those stay.
* Total deletable LOC: ~1,550 (including the heavy `process_test.go`).
* **Effort: S.** Mostly mechanical; the test files are large but cleanly bounded.
* **Risk: low.** Runs through CI to confirm no surprise transitive dep.
* **Linear: NEW issue (this is the biggest single delete opportunity).**

### B. Dead-shape carried by tests (production no longer exercises it; tests do)

#### B1. Legacy Bridge / `BridgeSession` test fake — *already filed: QUM-490*

* `internal/tui/bridge_legacy_fake_test.go` (164 LOC) + ~80 dependent tests
  across `internal/tui/*_test.go`.
* QUM-490 covers the migration. Confirmed still applicable on 2026-05-06.
* No new issue needed.

#### B2. `rootinit.ModeTmux` — dead in production, ~30 test references

* `internal/rootinit/init.go:20` defines `ModeTmux Mode = "tmux"`.
* Production callers: only `cmd/enter.go:422,424` which call
  `rootinit.Prepare(..., rootinit.ModeTUI, ...)` and `PrepareFresh(... ModeTUI ...)`.
* Test callers: `internal/rootinit/init_test.go` calls `Prepare` with
  `ModeTmux` ~28 times. Those tests aren't testing anything tmux-specific —
  they were written before `ModeTUI` existed and never migrated.
* The `Mode` enum no longer needs two values once `ModeTmux` is purged.
* **Effort: XS.** Convert tests to `ModeTUI`, delete `ModeTmux` constant,
  collapse the `Mode` type to a no-op or leave for forward symmetry.
* **Risk: very low.** Mode is consumed downstream by `agent.PromptConfig.Mode`,
  which already defaults to `"tui"` (`prompt_mode.go:resolveMode`).
* **Linear: NEW issue, low priority. Could be batched with QUM-322.**

#### B3. `internal/agent/prompt_mode.go` mode-swapped helpers + `*_tmux.golden` testdata — *already filed: QUM-322*

* QUM-322 calls for templatising the duplicated constants.
* Concrete cruft inventory tied to that issue:
  * `prompt_mode.go` (454 LOC) — `engineerRulesTmux`, `researcherRulesTmuxRendered`,
    `managerRulesTmuxRendered`, `engineerReportDoneLine`, plus 7 more
    `if mode == "tui" else tmux` branches in `prompt_child_sections.go`.
  * `prompt.go` — 8 more `if mode == "tui"` branches.
  * Goldens in `internal/agent/testdata/`: `engineer_tmux.golden` (136),
    `manager_tmux.golden` (234), `researcher_tmux.golden` (39),
    `golden_tmux_claude_code.txt` (239), `golden_tmux_no_cli.txt` (205).
    **Total ~853 lines of prompt text that no production launch path
    produces.**
  * `prompt_test.go` references `Mode: "tmux"` in 5+ places.
* QUM-322 is the right home for this; just calling out the magnitude (a real
  template + parametrised golden generation could shrink this whole subtree by
  ~700 LOC).
* No new issue needed.

#### B4. `restart Ns` ticker in TUI handoff path — *already filed: QUM-321*

* Confirmed still present: `internal/tui/messages.go:258` defines
  `ConsolidationProgressMsg`; `app.go:866,1909,1918,1930` schedule and handle
  it; `statusbar.go:23` renders the "restart Ns" string; `app_test.go:1549-1700`
  has the related tests.
* Issue accurately describes what to delete.
* No new issue needed.

#### B5. "Legacy" branches in `internal/tui/app.go` for handles without unified runtime

* `app.go:1014` "Legacy poll path" + `app.go:1251` "Legacy poll path: kick off
  the periodic activity tick. Skipped on the unified path…" + `app.go:1263`
  "registry miss, or a legacy handle".
* These branches are reachable only when the supervisor's `RuntimeRegistry`
  returns nil/no `UnifiedRuntime` for the selected child. After QUM-400 every
  in-process child runtime is unified. The branches are defensive: they fire
  for unstarted or test-double handles.
* `internal/tui/app_child_unified_test.go::registerLegacy` and
  `activity_stream_test.go::TestApp_ActivityStream_LegacyAgent_KeepsPolling`
  are the only places that intentionally exercise these branches.
* Once QUM-314 phase 2.4 lands (every child uses an in-process unified runtime
  unconditionally), these branches become unreachable and the JSONL polling
  path collapses out. Pre-emptively removing them now would break the test
  doubles that QUM-490's migration may rely on.
* **Recommendation: track but defer.** Worth a one-line note in QUM-314
  phase 2.4 / QUM-490, not a separate issue.

### C. Duplicated paths

#### C1. Two handoff implementations (CLI + MCP) — partially tracked under QUM-337

* `cmd/handoff.go` (146 LOC) is the deprecated tmux-mode CLI wrapper. Emits a
  stderr deprecation warning (`deprecationWarning("handoff", "handoff")`).
* The MCP `handoff` tool (`internal/sprawlmcp/`) is the supported path.
* The CLI version still handles state/memory writes; it's not a thin shim.
  Everything it does is duplicated by the MCP tool.
* Removal is gated on the deprecation grace window.
* **Effort: S.** Delete `cmd/handoff.go` and its tests after the grace
  period. Update `CLAUDE.md` (it currently mentions the deprecation).
* **Linear:** QUM-337 mentions this; not filing a new one. **Suggestion: add
  a sub-issue under QUM-314 §2.5 explicitly titled "remove cmd/handoff.go
  after deprecation grace window."**

#### C2. Other deprecated CLI commands (`messages`, `report`, `kill`, `retire`, `spawn`, `status`, `tree`, `delegate`, `color`)

* All emit one-shot deprecation warnings. Each is duplicated by an MCP tool.
* These are deliberately retained per QUM-314 phase 2.5 ("residual CLI and
  docs cleanup"). Out of scope for *this* audit.

#### C3. Inbox-arrival banner format mismatch — *already filed under QUM-473 §3*

* No new issue.

#### C4. `peekAndDrainCmd` / `WeaveRuntimeHandle.InterruptDelivery` parallel drain — *already filed under QUM-473 §4*

* No new issue.

### D. Legacy env-var/config gates

#### D1. `SPRAWL_MESSAGING=legacy` — confirmed gone from source

* No occurrences in `cmd/` or `internal/`. Only mentioned in `docs/research/*.md`
  as historical context.
* Already noted in QUM-473 §8. No action.

#### D2. `SPRAWL_UNIFIED_RUNTIME` — confirmed gone

* No occurrences in `cmd/` or `internal/`. Same as D1.

#### D3. `SPRAWL_TMUX_SOCKET` / `SPRAWL_NAMESPACE`

* Still in active use by sandbox scripts and `cmd/sandbox_gc.go` (per
  `CLAUDE.md` "tmux safety" note). Not cruft. No action.

### E. Stale docs (do NOT delete per prompt; flag only)

These describe pre-cutover architecture and may mislead a fresh agent that
greps for "how does sprawl spawn agents":

* `docs/research/tmux-elimination-research.md` — still accurate as a *plan*,
  but reads as if not done. Several phases shipped.
* `docs/designs/agent-wrapper-loop.md` — describes the legacy `agentloop`
  Runner that's been deleted.
* `docs/designs/messaging-overhaul.md` — pre-unified-runtime architecture.
* `docs/designs/unify-tui-weave-init.md` — design doc for an *already shipped*
  refactor; mentions `tui.Bridge` lifecycle that no longer exists.
* `docs/research/qum-334-bridge-bleed.md`, `docs/research/tui-parity-audit-2026-04-22.md`,
  `docs/research/realtime-message-injection.md`, `docs/research/go-agent-loop-integration.md`
  — all reference removed types (`tui.Bridge`, `agentloop.Runner`) without an
  "obsolete" marker.

**Recommendation:** add a one-line note at the top of each ("**Status:** This
document describes architecture that has been superseded by
QUM-400/QUM-399/etc. Retained for forensic value."). Not blocking. Could be a
single doc-housekeeping issue.

* **Linear: NEW issue (low priority, batched).**

### F. Skills (agent-facing) — actively misleading

These are read by spawned agents to learn the codebase. They reference deleted
types as current:

| Skill | Issue |
|---|---|
| `.claude/skills/testing-practices/SKILL.md` | Examples use `tmux.Runner`, `tmux.NewSession`, `tmux list-sessions`. Lines 16, 44, 56, 68, 93, 149-159, 170, 190, 193, 204-214, 222, 234, 242. |
| `.claude/skills/go-cli-best-practices/SKILL.md` | Examples use `tmux.FindTmux`, `tmuxRunner: &tmux.RealRunner{...}`, `deps.tmuxRunner.NewWindow`, `deps.tmuxRunner.KillWindow(agentState.TmuxSession, agentState.TmuxWindow)` — every one of those references a deleted type/field. Lines 20, 137, 146-151, 192, 294, 295, 306, 335. |
| `.claude/skills/cli-ux-best-practices/SKILL.md` | Error message examples: `"tmux session %q not responding"`, `"runs in its own tmux window"`. Lines 151, 233. |
| `.claude/skills/handoff/SKILL.md` | One line correctly flagging the CLI deprecation; fine. |
| `.claude/skills/tui-testing/SKILL.md` | Uses tmux for test harness. **Legitimate** — tmux is still the e2e test driver. No change. |
| `.claude/skills/e2e-testing-sandboxing/SKILL.md` | Sandbox tmux mention is legitimate (`SPRAWL_TMUX_SOCKET`). Mostly fine; one stale `sprawl init` reference at line 110. |
| `.claude/skills/testing-practices/SKILL.md:127` | `./sprawl init` command example — **does not exist anymore.** |

* **Effort: S–M.** Replace examples wholesale with the post-QUM-400 dependency
  injection pattern (e.g. `backend.Adapter`, `supervisor.RuntimeRegistry`).
* **Risk: medium.** These shape what every spawned agent thinks about the
  codebase. Wrong examples = wasted agent time / bad PRs.
* **Linear: NEW issue, Medium priority.**

## Verification of pre-existing issues

| Issue | Status | Notes |
|---|---|---|
| QUM-318 (`sprawl poke`) | **DONE 2026-04-30.** | Already shipped. Skip. |
| QUM-321 (restart Ns ticker) | **Still applicable.** | Confirmed by code refs at `messages.go:258`, `app.go:866,1909`, `statusbar.go:23`, `app_test.go:1549-1700`. |
| QUM-322 (templatize prompts) | **Still applicable.** | Inventory expanded in §B3 above. |
| QUM-473 (hygiene umbrella) | **Still applicable.** | Findings 1-7 still match the code. Finding 8 (SPRAWL_MESSAGING gate) and finding 9 (tmux-mode residuals) are correctly marked "no action." |
| QUM-490 (TUI tests on Bridge fake) | **Still applicable.** | `bridge_legacy_fake_test.go` exists; ~80 tests use `NewBridge(ctx, mock)`. |

## Recommended cleanup wave order

The waves are ordered by *blast radius minimisation*: each later wave assumes
prior waves landed cleanly.

### Wave 0 — already filed; pick off whenever (parallelisable)

* QUM-321 (restart Ns ticker) — XS, isolated.
* QUM-322 (prompt templatize) — M, contained to `internal/agent`. Sub-tasks
  in §B3 above.
* QUM-473 §1-7 hygiene items — pick off in the order the umbrella suggests.

### Wave 1 — pure delete, parallelisable, ship first (NEW issues)

These have no behavioural risk and no test churn beyond the deletes themselves:

1. **Delete `internal/tmux` package** (A1). XS.
2. **Delete `cmd/enter.go::buildEnterLaunchOpts` and `cmd/enter_args_test.go`** (A2). XS.
3. **Delete `claude.LaunchOpts` tmux-mode fields** (A3). S.

Each can be a separate PR. They don't conflict with each other.

### Wave 2 — bigger but contained (NEW issue)

4. **Delete `internal/agentloop/process.go` + `real_starter.go` + their tests** (A4). S.
   Sequence after A3 so the LaunchOpts pruning doesn't get re-blocked by tests
   that round-trip dead fields. Touches only `internal/agentloop` and is
   bounded by Go's import graph.

### Wave 3 — touches tests with dependencies on Wave 0/1 (NEW issue, but defer)

5. **Migrate `rootinit_test.go` off `ModeTmux`, delete `ModeTmux`** (B2). XS.
   Don't ship until QUM-322 lands or you'll churn the test mode constants
   twice.

### Wave 4 — agent-facing docs (NEW issue, ship in parallel with Wave 1)

6. **Update skills docs** (F). S–M. Affects every agent's understanding of the
   codebase, so worth landing alongside the structural cleanups even though
   it's "just docs." Medium priority because the cost of *not* doing it is
   that every new agent spawned today learns a wrong codebase shape.

### Wave 5 — deferred (no new issue; flag in QUM-314 § 2.5)

7. **Delete `cmd/handoff.go`** (C1) once the deprecation grace window expires.
8. **Delete TUI "legacy poll path" branches** (B5) once QUM-314 phase 2.4
   guarantees every child runs through an in-process unified runtime.
9. **Doc housekeeping** (E) — annotate stale design docs.

### What serialises

* Wave 4 (skills) blocks future agents from being onboarded with wrong
  examples; consider it Medium priority and ship within the same week as
  Wave 1.
* Wave 2 (agentloop Process delete) wants Wave 1 #3 (LaunchOpts) first.
* Wave 5 #7 (cmd/handoff.go) is gated by deprecation grace, not by other
  cleanup.

## New Linear issues to file

The following are filed as part of this audit:

1. **QUM-504** — Delete dead `internal/tmux` package (no Go importer remains).
2. **QUM-505** — Delete dead `cmd/enter.go::buildEnterLaunchOpts` (replaced by SessionSpec path).
3. **QUM-506** — Remove dead "Interactive / tmux mode" fields from `claude.LaunchOpts`.
4. **QUM-507** — Delete dead `internal/agentloop/process.go` + `real_starter.go` (legacy subprocess Process). _Blocked by QUM-506._
5. **QUM-508** — Migrate `rootinit` tests off `ModeTmux`; delete the constant. _Blocked by QUM-322._
6. **QUM-509** — Refresh `.claude/skills/` docs that reference deleted tmux types.

A 7th candidate (doc housekeeping for `docs/designs/` and `docs/research/`
stale architecture docs) is **NOT** filed; it's listed in §E for the next
agent to pick up if they think the signal is worth it.

## Reflections

**Surprising:** `internal/tmux` is fully dead. I expected it to be the
penultimate domino, but it turns out everything moved on (the supervisor and
sandbox code shell out to `tmux` directly without the wrapper) and the package
itself was just left behind.

**Also surprising:** `agentloop.Process` and `claude.LaunchOpts`'s
interactive-mode fields are zombie-shaped — large surfaces (~1.5 kLOC and 5
fields with their own arg-builder branches respectively) kept alive by tests
that should have been deleted at the same time as the legacy runner.

**Open questions I didn't fully chase:**

* **Is `internal/host` partially dead?** Production uses `host.NewMCPBridge`
  only; `host.Session` / `host.Router` / `host.Transport` are referenced only
  by package-internal tests + `cmd/hosttest/main.go` (a developer tool).
  Worth a follow-up audit: is `cmd/hosttest` still useful, or is it now also
  cruft? I didn't dig because the tool *might* still be valuable for
  protocol-level smoke tests.
* **`internal/agentloop/process_test.go` (962 LOC)** has tests that look
  generic enough they could be migrated to test the new `backend/claude`
  adapter — would salvage some coverage rather than throw it away. Out of
  scope here; mention to whoever picks up A4.
* **The skills `tmux.Runner` mock examples** could be replaced with examples
  that mock `backend.Session` or `supervisor.RuntimeRegistry`. Filing F as a
  refresh, not a "delete tmux examples"; the skills need a forward-looking
  example.

**What I'd investigate next:**

1. Whether `cmd/hosttest` is still useful or is pure cruft.
2. Whether `internal/host` can collapse to just `mcp_bridge.go` after
   QUM-314 phase 2.4 (since the Session/Router/Transport surface looks
   adapter-shaped now).
3. The `tuiruntime` package didn't get a careful look; QUM-446 is the
   ChildStreamAdapter dedupe that touches it. Worth a separate cruft pass
   alongside QUM-446.
