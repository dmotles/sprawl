# CLAUDE.md audit — Part B (lines 354–623) + `CLAUDE.local.md` + `DESCRIPTION.md`

**Auditor:** recon (researcher) · **Date:** 2026-08-06 · **Branch:** `dmotles/docs-audit-claudemd-b`
**Surface:** `CLAUDE.md` from `### Never git add -A (QUM-989)` (line 354) through `## Validating Changes` items 1–4 (line 623). The e2e matrix table and its row-derivation preamble (lines 624–769) belong to a third researcher and are **not** covered here. Plus `CLAUDE.local.md` (21 lines) and `DESCRIPTION.md` (195 lines).

**Measured size of my surface:** lines 354–617 = **264 lines / ~2,470 words**, plus `## Validating Changes` items 1–4 (6 lines / 91 words). 10 distinct QUM references, 14 occurrences.

---

## 1. Verdict

The thesis holds, and in my surface it holds *harder* than expected: this is not merely long, it is **a third-generation copy of things the tree already states better**. Of ~270 lines, roughly 150 are a lossy duplicate of a skill, a Go doc comment, a Makefile target, an in-product help modal, or a test that already pins the behaviour — and in **every single case where a duplicate exists, the CLAUDE.md copy is the stale one**. `## Repo Layout` names 6 of 33 `internal/` packages and is strictly worse than the same list in `/go-cli-best-practices`. `## Code Patterns` asserts "Every file in `cmd/` and `internal/` has a corresponding `_test.go`" — **36 of 216 do not**, including `internal/supervisor/drain.go`, the 443-line file the e2e table spends four paragraphs on. `## Project Configuration` documents 1 of 9 config keys, all 9 of which carry `sprawl:"purpose=…"` struct tags that already exist to be reflected into generated docs. `## Validating Changes` item 1 describes a `make validate` that has not been accurate since QUM-972, and disagrees with the *other* description of `make validate` 570 lines above it in the same file.

The most valuable thing in this surface is small: the `/tmp` and `git add -A` hard rules, the `SPRAWL_CLAUDE` shim (agents genuinely cannot discover this from the code when they hit `Not logged in`), and the "you are running inside the system you are building" orientation. That is about **30 lines**. Everything else is a pointer, a cut, or a mechanism.

Two sections **earn their keep and I argue for them below**: `### Never git add -A` (compressed) and `## Meta: Developing Sprawl Inside Sprawl` (verbatim — 4 lines, universal, unstatable anywhere else).

**Recommendation: 264 lines → 34 lines in CLAUDE.md.** ~88% cut. `DESCRIPTION.md` needs 6 corrections and should move out of the root-read path. `CLAUDE.local.md` is correct and should be left alone.

---

## 2. Rotted claims found

Ranked by danger. Every "Evidence" column is a command I ran in this worktree at `HEAD`.

| # | Claim | Where | Reality | Evidence |
|---|---|---|---|---|
| **R1** | "Every file in `cmd/` and `internal/` has a corresponding `_test.go`. **Keep it that way.**" | CLAUDE.md:552 | **36 of 216 non-test `.go` files have no `_test.go`.** Includes `internal/supervisor/drain.go` (the QUM-1084 drain), `internal/supervisor/sweep_coordinator.go`, `internal/tui/session_backend.go`, `internal/merge/runtests.go`, `internal/agentops/kill.go`, all of `internal/hub/store/`. | `for f in $(find cmd internal -name '*.go' ! -name '*_test.go'); do [ -e "${f%.go}_test.go" ] \|\| echo "$f"; done \| wc -l` → `36`; denominator `216`. |
| **R2** | `make validate` — "full pipeline: build, fmt-check, lint, **test**" | CLAUDE.md:620 | Actual: `build proto-check fmt-check lint test-race-gate test-race test-wirelog-helpers-unit test-e2e-lockwait-unit test-e2e-matrix-unit test-gitignore-classes leak-scan`. There is **no `test` dependency** — QUM-972 deliberately replaced it with `test-race`, and this line still advertises the uninstrumented run that QUM-972 exists to have removed. | `grep -n '^validate:' Makefile` → line 4. |
| **R3** | Same pipeline, second copy, also wrong, **differently** | CLAUDE.md:49–55 (`## Build & Test`, other researcher's surface — reported for the union) | That copy has `test-race` but omits `test-e2e-lockwait-unit`. So the repo holds **three** descriptions of `make validate` (Makefile, :49, :620), and **two of the three are wrong in non-overlapping ways**. | `sed -n '4p' Makefile` vs CLAUDE.md:49–55 vs :620. |
| **R4** | "Agent types: `engineer`, `researcher`, `manager`, `qa`." | CLAUDE.md:538 | `ValidTypes = {manager, researcher, engineer, qa, tester, code-merger}` — **six**. `tester` and `code-merger` are missing. | `internal/agentops/spawn.go:17`. |
| **R5** | "child agents run in **tmux sessions** managed by sprawl" | DESCRIPTION.md:184 | False since the tmux-mode removal. Children are same-process stream-json subprocesses under `internal/supervisor`. tmux survives **only** in the sandbox/e2e harness. | `internal/claude/launch.go:5` — *"in stream-json subprocess mode (the only launch mode left after the tmux [removal])"*; no tmux in any production runtime path. |
| **R6** | "**Web UI** — planned but **not yet implemented**" | DESCRIPTION.md:194 | A real Vite/React app exists: `web/` with `package.json`, `vite.config.ts`, `src/wire/{store,stream,transcript,useLiveTail,LiveTailView}`, plus `internal/hub/`, `internal/hubtail/`, `cmd/hubd/`, and a dedicated `hub-e2e` matrix row. | `ls web/ web/src/wire`; CLAUDE.md's own table has a `hub-e2e` row. |
| **R7** | "**Tester agent type** / **Code Merger agent type** — planned but not yet implemented" | DESCRIPTION.md:191–192 | Both are in `ValidTypes` and both have name pools. | `internal/agentops/spawn.go:17`; `internal/agent/names_test.go:294`. |
| **R8** | "**Automatic `.env` copying** — planned but not yet implemented" | DESCRIPTION.md:193 | Implemented. `.sprawl/config.yaml`'s `worktree.setup` does `cp -p "$SPRAWL_ROOT/.env" "$PWD/"`. **CLAUDE.md:427 says so 230 lines away from DESCRIPTION.md saying the opposite.** | `cat .sprawl/config.yaml`; CLAUDE.md:427. |
| **R9** | Incident bundle "Includes: goroutine dump, fd list, sprawl status, `ps auxf`, `/proc/<pid>/status`, 10k mcp-calls lines, activity rates, memory + loadavg" | CLAUDE.md:491–494 | Stale subset — **omits `cpu-*.pprof`, `heap-*.pprof` (QUM-934, the same issue this file cites in the section heading two lines below) and `binary.txt`**. The bundle writes its own authoritative legend as `README.md` at capture time. | `internal/observe/incident/snapshot.go:535–555` (`buildReadme`), package doc :1–14. |
| **R10** | "Agents come in **four** types" (Root/Manager/Engineer/Researcher table) | DESCRIPTION.md:48–55 | See R4/R7. Six spawnable types. | as R4. |
| **R11** | `## Repo Layout` lists 6 `internal/` packages | CLAUDE.md:534–543 | **33 exist.** Absent: `tui`, `backend`, `runtime`, `sprawlmcp`, `protocol`, `messages`, `memory`, `hub`, `agentops`, `agentloop`, `rootinit`, `transcript`, `usage`, `observe`, … — i.e. essentially every package the e2e matrix table sends agents into. | `ls internal/ \| wc -l` → 33. |
| **R12** | `## Project Configuration` documents the `validate` key | CLAUDE.md:524–533 | 9 keys exist: `validate`, `validate_timeout`, `validate_popup_after_seconds`, `pause_timeout_seconds`, `hub_url`, `hub_token_file`, `memory_model`, `worktree.setup`, `worktree.teardown`. `worktree.setup` — the one that installs both `main` guards and copies `.env` — is described *elsewhere* in CLAUDE.md and never here. | `grep 'yaml:"' internal/config/config.go`. |
| **R13** | `sprawl` User CLI is 4 commands | DESCRIPTION.md:127–132 | 20 command files in `cmd/`: `config`, `gc`, `memory`, `usage`, `version`, `hub`, `sandbox-gc`, `hooks`, `color`, `debug`, … | `ls cmd/*.go \| grep -v _test`. |
| **R14** | `--json` output mode is the resume mechanism | DESCRIPTION.md:44 | Flag is `--output-format stream-json`. `--resume` is correct. | `internal/claude/launch.go:17,44,79`. |

### Adjacent rot, outside my surface, reported because it is one hop from it

| # | Claim | Where | Reality |
|---|---|---|---|
| **A1** | `/linear-issues` skill's "Messaging Tools" section documents `send_async({to,subject,body})`, `send_interrupt(...)`, and `message(...)` as the agent messaging surface | `.claude/skills/linear-issues/SKILL.md:198–212` | **None of these tools exist.** The real surface is `send_message({to, body, interrupt?})`. `grep '"send_async"' internal/sprawlmcp/tools.go` → nothing; `tools.go:140` → `send_message`. An agent following this skill literally cannot message anyone. |
| **A2** | Same section: `report_status({state, summary, detail})` | ibid. | `report_status` has **no `detail` parameter** (`internal/sprawlmcp/tools.go:178`). |
| **A3** | F1 help modal: "Up / Down — Navigate input history (**or scroll output when input empty**)" | `internal/tui/help.go:45` | Backwards. `app.go:646` and CLAUDE.md:483 both correctly say *empty input → history, non-empty → no-op*; the viewport is never scrolled by arrows. The in-product help — the copy users actually read — is the wrong one. |
| **A4** | F1 help modal omits `Ctrl+\` (incident snapshot) | `internal/tui/help.go:30–49` | Bound at `app.go:882`. CLAUDE.md documents it; the product does not. |
| **A5** | `cmd/enter.go:199` cites `cmd/rootloop.go` | — | File does not exist. |

**The pattern across R1–R14 and A1–A5 is uniform: the copy furthest from the code is the wrong one, and nothing anywhere fails when it drifts.** R1 is the single most dangerous, because it is an instruction ("Keep it that way") that an agent will believe and act on, and it is false about the exact file — `drain.go` — that the rest of CLAUDE.md tells agents is load-bearing.

---

## 3. CLAUDE.md ↔ skill / code duplication map

`.claude/skills/` totals 3,262 lines and is loaded on demand. CLAUDE.md is 768 lines and is loaded **always**. Every row below is content paying the always-on price for something already available on demand — or already in the tree.

| CLAUDE.md content | Lines | Duplicate of | Which is better | Action |
|---|---|---|---|---|
| `## Code Patterns` — "Dependency injection" para (:550) | 1 (dense) | `/go-cli-best-practices` § *Dependency Injection for Testability* (:132–180) **and** `/testing-practices` § *Dependency Injection Testing Pattern* (:1688) | **Skill.** Skill gives the 3-part deps/resolve/run structure and a full worked `RetireDeps` example; CLAUDE.md gives one sentence and two file citations. | **Cut.** Keep only the existing `/go-cli-best-practices` pointer at :558. |
| `## Code Patterns` — "Every new assertion must demonstrate it CAN fail" (:554) | 1 (~600 chars) | `/testing-practices` § **Assertion Rigor** (:35–111) — which CLAUDE.md *itself cites by name* in its last clause | **Skill**, by ~75 lines to 1. | **Cut to the pointer.** A paragraph that ends "read `/testing-practices` § X" is a pointer wearing a copy as a coat. |
| `## Code Patterns` — "No fallback branch may silently succeed (QUM-997)" (:556) | 1 (~1,900 chars — the single heaviest line in my surface) | `/testing-practices` § **The non-asserting fallback** (:112–309), incl. the worked example and the rejected-parser history | **Skill.** CLAUDE.md compresses 200 lines into one unreadable paragraph *and* cites the skill for the rest. | **Cut to the pointer.** |
| `## Sandbox Testing` (:601–609) | 9 | `/e2e-testing-sandboxing` § Setup (:18–39) | Skill. | Cut to 1-line pointer (it nearly is one already). |
| `## tmux safety` (:435–440) | 6 | `/e2e-testing-sandboxing` § **DO NOT** (:12–16) + § *Hygiene contract* (:143–177) | **Skill**, decisively — it has the 5-layer lifecycle and the 2026-04-21 incident. | **Cut.** Fold the `SPRAWL_TMUX_SOCKET` / `_stmux` specifics into the skill (they are currently *absent* from it — `grep SPRAWL_TMUX_SOCKET .claude/skills/` returns nothing). This is the one place CLAUDE.md holds unique content, and it belongs in the skill. |
| `## /tmp hygiene` (:441–460) | 20 | Partly `/e2e-testing-sandboxing` § DO NOT / Hygiene contract | Split. The `rm -rf` prohibition duplicates the skill; the `/tmp/coder-script-data` warning is **unique and non-obvious**. | **Cut 17, keep 3.** See §5. |
| `## Session Handoff` (:597–600) | 4 | `/handoff` skill (69 lines) | Skill. | **Cut to 0 lines in CLAUDE.md** — the skill is `user-invocable` and weave-only; it is discoverable from the skill list. At most one breadcrumb. |
| `## Linear Issue Tracking` (:580–590) — the 3-step lifecycle | 11 | `/linear-issues` § Conventions (:174–178) + § *Reporting Progress While Working an Issue* (:180–196) | Skill (though see A1/A2 — the skill's *messaging* section is rotted). | **Cut 8, keep 3** (the "invoke the skill first, don't trust memory" instruction is genuinely load-bearing and belongs in CLAUDE.md). |
| `### Runtime pprof toggle` (:498–523) | 26 | `cmd/enter_pprof.go` doc comments :1–120 — near-verbatim, including the "nobody asked for 6060" reasoning at :97 and the provenance split at :93–97 | **Code.** The comment sits on the branch it explains and cannot drift from it. | **Cut all 26.** Move the *operator-facing* two facts (`SIGUSR2` toggles; address is in `.sprawl/runtime/pprof-addr`) to `docs/dev/`. |
| `### Incident snapshot hotkey` (:488–497) | 10 | `internal/observe/incident/snapshot.go` package doc + `buildReadme()` — which emits the legend **into every bundle** | **Code.** Self-documenting at capture time; CLAUDE.md's copy is already stale (R9). | **Cut 9, keep 1** (the hotkey itself). |
| `## Text selection` + scroll keys (:461–487) | 27 | `internal/tui/help.go` (F1) for the keys; `/tui-testing` for validation | Neither is complete (A3/A4), but the fix is to make **one** authoritative and test it — not to keep a third. | **Cut 22, keep ~5.** See §6-M2. |
| `## Repo Layout` (:534–543) | 10 | `/go-cli-best-practices` § Project Structure (:9–33) — 9 packages incl. `agentops`, `agentloop`, `backend`, `runtime`, `messages` | **Skill**, and it is *also* incomplete (9 of 33) — but it is nearer the audience that needs it. | **Cut all 10** from CLAUDE.md; generate the real list (§6-M1). |
| `## Public vs Private Repo Hygiene` (:563–579) | 17 | `scripts/guard-employer-leak` (per-commit staged scan via `scripts/pre-commit`, **plus** whole-tree `--all` via `make leak-scan`, which `validate` depends on) + `.gitignore:51` (`findings/`, unanchored, QUM-989) | **Tooling.** The mechanical rule is enforced on every commit *and* every validate. | **Cut 14, keep 3** — the residual judgement rule (prose *describing* internal systems without using a listed term) is not term-matchable and must survive. |
| `## Validating Changes` items 1–4 (:620–623) | 4 | Makefile (item 1, rotted — R2) + skill names (items 3, 4) | Makefile / skills. | **Keep 3, rewritten** — drop the pipeline enumeration entirely, name the target. |
| `## Linting & Formatting` (:610–617) | 8 | `.golangci.yml` + `Makefile` + the pre-commit hook that *runs* `make validate` | Tooling. Nothing here needs a human-readable rule: the hook refuses the commit. | **Cut 6, keep 2.** |
| "the QUM-617 selection-mode toggle stays retired" (:466) | — | `TestAppModel_NoSelectionModeToggle` (`internal/tui/app_test.go:1927`) | **Test.** | **Cut.** A test that already pins a deletion does not also need a sentence. |

**Nothing in my surface is duplicated by `/cli-ux-best-practices` or `/tui-testing` beyond their pointers, which are correct and should stay.**

---

## 4. Ranked cut list

Ordered by lines-saved × risk-of-harm. "Saved" = lines removed from the always-loaded file.

| Rank | Cut | Saves | What is lost | Why acceptable |
|---|---|---|---|---|
| 1 | **`## Code Patterns` :554 + :556** — the assertion-rigor and non-asserting-fallback paragraphs | 2 lines / **~2,500 chars — the densest cut available anywhere in my surface** | Nothing. Both paragraphs terminate in a citation of the very `/testing-practices` section they compress. | The pointer already exists in the same sentence. This is the purest instance of the "pointer survives, copy dies" rule in the file. |
| 2 | **`### Runtime pprof toggle`** :498–523 | 26 | Nothing agent-universal. This is operator debugging trivia most agents will never need. | Verbatim-equivalent doc comments live on the code at `cmd/enter_pprof.go:1–120` and cannot drift. Two operator facts move to `docs/dev/debugging.md`. |
| 3 | **`## Text selection` + scroll keys** :461–487 | 22 | A partial keybinding list. | The keys are the F1 modal's job. Fix A3/A4 and pin `help.go` with a test instead of maintaining a third copy. Keep only "the TUI captures the mouse; Shift+drag or tmux copy-mode to select". |
| 4 | **`## Public vs Private Repo Hygiene`** :563–579 → 3 lines | 14 | The long enumeration of leak categories. | `guard-employer-leak` blocks the mechanical case on every commit **and** every `make validate`; `.gitignore:51` handles findings dirs. Retain only the non-mechanisable judgement rule. |
| 5 | **`/tmp hygiene`** :441–460 → 3 lines | 17 | The `_e2e_cleanup` / `_unit_reset_markers` pattern citations. | Those are for people *writing* harnesses — `/e2e-testing-sandboxing`'s job. Keep the two rules an agent can violate at any moment without meaning to: never `rm -rf` a `/tmp` glob; never touch `/tmp/coder-script-data`. |
| 6 | **`## Repo Layout`** :534–543 | 10 | An inventory that is 6/33 correct (R11) and 4/6 correct on agent types (R4). | Actively misleading today. Replace with a generated file (§6-M1). |
| 7 | **`### Never git add -A`** :354–396 → 6 lines | 37 | The QUM-989 incident narrative (57 KB terraform plan, Azure logs, the `.gitignore`-is-a-backstop argument). | The **rule** survives; the **story** goes to `docs/` or stays in Linear where it already is. This is the file's single largest narrative block and it argues at length for a one-line prohibition. |
| 8 | **`## Running claude from agent bash subshells`** :401–434 → 4 lines | 30 | The one-time host-side `.env` setup procedure. | Procedural, done once, by a human, not by an agent mid-task. Belongs in `docs/dev/setup.md`. Agents keep the diagnostic breadcrumb — which is the only part they need and the only part they cannot derive. |
| 9 | **`## Linear Issue Tracking` lifecycle steps** :586–589 | 8 | The In Progress → comment → Done ritual. | `/linear-issues` §Conventions + §Reporting Progress already own it. Keep the "invoke the skill first" instruction. |
| 10 | **`### Incident snapshot hotkey`** :488–497 → 1 line | 9 | The bundle-contents list — already stale (R9). | Every bundle ships `README.md` with the authoritative legend. |
| 11 | **`## Linting & Formatting`** :610–617 → 2 | 6 | Restating that formatting is checked. | The pre-commit hook runs `make validate`, which runs `fmt-check` + `lint`. A rule enforced by a hook that refuses the commit does not need a paragraph. |
| 12 | **`## tmux safety`** :435–440 | 6 | `_stmux` / `SPRAWL_TMUX_SOCKET` guidance. | Move (do not delete) into `/e2e-testing-sandboxing`, which currently lacks it. Only sandbox-running agents need it. |
| 13 | **`## Sandbox Testing`** :601–609 → 1 | 8 | A 2-line quickstart. | The skill's §Setup is the same thing with the safety rails attached. |
| 14 | **`## Session Handoff`** :597–600 → 0–1 | 3 | A pointer to a weave-only skill. | The skill list is already in every agent's context. Every non-weave agent pays for this today and can never use it. |
| 15 | **`## Project Configuration`** :524–533 → 1 | 9 | A 1-of-9-keys config sample (R12). | Replace with `sprawl config --schema` output or a generated `docs/reference/config.md` (§6-M3). |
| 16 | **`## Validating Changes` item 1** :620 | 0 (rewrite) | The (wrong) pipeline enumeration. | R2. Name the target, not its contents. |
| **Total** | | **≈ 207 lines** | | **264 → ~34 lines (-78%)**, and the removed text carries **every** rotted claim in R1, R2, R4, R9, R11, R12. |

---

## 5. Retained items

Everything I keep, with destination and a one-line reason. Total CLAUDE.md retention: **~34 lines**.

### → `CLAUDE.md` (universal, every agent, every turn)

| Item | Lines | Reason |
|---|---|---|
| `## Meta: Developing Sprawl Inside Sprawl` — **verbatim, all 4 lines** | 4 | The single highest value-per-line block in my surface. It is orientation an agent cannot derive from any file, it applies to literally every agent on every turn, and there is nowhere else it could live. **This section earns its keep and I would resist cutting it.** |
| **Never `git add -A`** — rule only: *"Stage explicit paths only. Never `git add -A` / `git add .` / `git commit -a`; `git add -u` is the sanctioned shortcut for large changes. Worktrees share a filesystem with other agents' scratch output — see `docs/`. If an untracked file surprises you, do not stage it; find out what wrote it."* | 5 | Universal, violable on any turn, **not enforced by any hook** (the two `main` guards do not fire — QUM-989 was a correct-branch, correct-identity commit), and the failure is silent. The strongest earn-its-keep case after Meta. |
| `/tmp` hard rules — 2 bullets: never `rm -rf` a `/tmp` glob; never touch `/tmp/coder-script-data` | 3 | Cross-agent blast radius; the second is genuinely undiscoverable (a symlink whose deletion silently converts every `needs_claude` e2e row into a skip). |
| `SPRAWL_CLAUDE` breadcrumb — *"`claude` from a Bash subshell fails `Not logged in` — Claude Code strips the OAuth token. Use `scripts/run-claude` as `$SPRAWL_CLAUDE`. Setup: `docs/dev/setup.md`."* | 3 | The **diagnostic** is the payload. An agent hitting `Not logged in` will otherwise burn a long detour, and the CLAUDE.md e2e-table prose explicitly warns against the wrong remedy. Breadcrumb, not procedure. |
| Public/private residual judgement — *"This repo is PUBLIC. Never commit employer-internal context; put such artifacts in `.sprawl/agents/<name>/findings/`. `guard-employer-leak` catches listed terms, not descriptions — the judgement is yours."* | 3 | The guard is term-list based; prose describing an internal system without naming it passes cleanly. That residue is not mechanisable and must stay. |
| `make install` warning (`## Install`) | 2 | Short, universal, and a real footgun (an agent clobbering the shared binary). |
| `/linear-issues` invocation instruction | 2 | "Invoke the skill; do not trust remembered conventions" is itself a behavioural rule that no skill can state about itself. |
| Skill pointers — one compact block: `/testing-practices`, `/go-cli-best-practices`, `/cli-ux-best-practices`, `/e2e-testing-sandboxing`, `/tui-testing`, `/linear-issues`, `/handoff` | 7 | This is what CLAUDE.md is *for*. |
| Validating Changes — *"`make validate` before committing (the pre-commit hook enforces it). TUI changes: `/tui-testing`. E2E: `/e2e-testing-sandboxing`. Mandatory e2e rows: see `docs/…`."* | 3 | Names targets and skills; enumerates nothing that can drift. |
| Spawning Agents — *"Point the agent at the Linear issue; don't paste its contents."* | 2 | Genuine agent-behaviour rule, no skill owns it, cheap. |

### → `docs/`

| Item | Target | Reason |
|---|---|---|
| QUM-989 narrative (terraform plan, Azure logs, `.gitignore`-is-a-backstop) | `docs/dev/git-hygiene.md` | Real institutional memory; explains *why* the rule is absolute. Not needed on every turn. |
| `.env` / `scripts/run-claude` one-time host setup | `docs/dev/setup.md` | Human, once, at install time. |
| pprof operator facts (`SIGUSR2` toggle, `.sprawl/runtime/pprof-addr`) + incident-snapshot hotkey | `docs/dev/debugging.md` | Operator-facing, occasional; the rationale stays in `cmd/enter_pprof.go`. |
| `DESCRIPTION.md` | `docs/architecture/vision.md` (see §7) | Vision doc, not per-turn context. |

### → `.claude/skills/`

| Item | Target skill | Reason |
|---|---|---|
| `SPRAWL_TMUX_SOCKET`, `_stmux`, `sprawl_sandbox_destroy` specifics | `/e2e-testing-sandboxing` — **currently missing `SPRAWL_TMUX_SOCKET` entirely** | Only sandbox-running agents need it; the skill already owns the surrounding hygiene contract. |
| `_e2e_cleanup` / `_unit_reset_markers` cleanup pattern | `/e2e-testing-sandboxing` | For harness authors. |
| Linear issue lifecycle (In Progress → comments → Done) | `/linear-issues` §Conventions (mostly already there) | Consolidate; delete the CLAUDE.md copy. |
| DI pattern, assertion rigor, non-asserting fallback | `/go-cli-best-practices`, `/testing-practices` (all three **already there and better**) | Nothing to move — just delete the CLAUDE.md copies. |
| **Proposed new skill: `git-hygiene`** | new | `add -A` rationale + the two `main` guards + the squash-merge recovery procedure (lines 210–353, the other researcher's surface) are one coherent topic, needed occasionally, ~140 lines. A natural on-demand unit. I flag it here because my §7 (`add -A`) is its natural anchor; the caller owns the merge. |

---

## 6. Replacement mechanisms

Where prose asserts something about the tree, the fix is a mechanism, not better prose. Ranked by value.

**M1 — Generate `## Repo Layout` (fixes R4, R11).**
`internal/agentops/spawn.go:17` already holds `ValidTypes` as a Go slice. `ls internal/` already holds the package list. A `make docs-layout` target emitting `docs/reference/layout.md` (package list from the tree; agent types from `ValidTypes` via a tiny generator or `go doc`) makes both un-rottable. **Additionally: a Go test asserting `ValidTypes` matches the types named in `docs/reference/layout.md`** — cheap, and R4/R7/R10 (three separate rotted claims across two files) all die at once.

**M2 — Make `internal/tui/help.go` the single source of keybindings, and test it (fixes A3, A4, and cut #3).**
Today there are three copies (CLAUDE.md, `help.go`, `app.go`'s key handlers) and the *product-facing* one is the wrong one. A table-driven test that walks the `bindings` slice and asserts each entry corresponds to a live handler in `app.go` — and that every `msg.Mod&tea.ModCtrl != 0 && msg.Code == X` in `app.go` appears in `bindings` — kills A3, A4 and lets CLAUDE.md drop 22 lines with no loss. The bidirectional assertion is the point: A4 (`Ctrl+\` bound but undocumented) is only catchable in the code→docs direction.

**M3 — Generate the config reference (fixes R12).**
`internal/config/config.go` already carries `sprawl:"default=…,purpose=…"` struct tags **and already reflects over them** (`reflect.TypeOf(Config{})` at :136). The generated artifact is ~10 lines of code away. Emit `docs/reference/config.md` from the tags; CLAUDE.md keeps a one-line pointer. Adding a config key then updates its own docs, which is the property the current section lacks.

**M4 — Assert the `make validate` description, or delete it (fixes R2, R3).**
Two prose copies of one Makefile line, both wrong, in the same file. Either (a) delete both and name only the target — my recommendation, it costs nothing — or (b) a `scripts/test-validate-doc.sh` that extracts `validate:`'s prerequisites and greps them out of CLAUDE.md. **(a) is strictly better**: the enumeration has no reader-value that "run `make validate`" lacks.

**M5 — Decide R1: make it true, or delete the claim.**
"Every file has a `_test.go`" is 83% true and stated as an invariant. Two honest options: (i) a `make test-coverage-floor` check listing files without a sibling test against an explicit allow-list of the current 36 — which makes the claim true *and* makes each exemption a reviewed decision; or (ii) delete the sentence. **Do not leave it as prose.** Note the standout: `internal/supervisor/drain.go` — 443 lines of drain logic that CLAUDE.md's own e2e table describes as load-bearing in four separate rows — has **no unit test file at all**. That is worth its own issue independent of this audit.

**M6 — Delete the QUM-617 sentence; `TestAppModel_NoSelectionModeToggle` already is the mechanism.**
Cited here as the template the other five should follow: a deletion pinned by a test needs no prose at all.

---

## 7. `CLAUDE.local.md` and `DESCRIPTION.md`

### `CLAUDE.local.md` (21 lines) — **correct. Leave it alone.**

**Purpose:** gitignored, per-workspace, non-checked-in configuration — the Linear team/project IDs, the `dmotles/` branch prefix, and two dated notes. It is the correct shape for what it holds: workspace-specific values that must not enter a public repo, referenced by name from both CLAUDE.md (:582, :593) and `/linear-issues` (:12).

**Verified:** `docs/todo/punchlist.md` exists (3,024 bytes). `CLAUDE.local.md` is in `.gitignore` (:55), so its own self-referential warning ("add to `.gitignore` if it isn't already") is satisfied. `.sprawl/config.yaml`'s `worktree.setup` copies it into every new worktree, so the reference from CLAUDE.md resolves inside worktrees — that mechanism is real and I confirmed it in the live config.

**One nit:** the "GitHub-issues migration abandoned on 2026-04-21" note is dated narrative in an always-loaded file. It is 1 line and it prevents a re-litigation, so it pays for itself. **No changes recommended.**

### `DESCRIPTION.md` (195 lines) — **right in kind, wrong in six facts. Correct it, then move it out of the always-read path.**

**Purpose:** the vision/design document — why Sprawl exists, the Conway's-Game-of-Life framing, the root/manager/IC model, the rules, the forcing function. CLAUDE.md's first line makes it mandatory reading ("Read `DESCRIPTION.md` for project context"), so **every agent pays 195 lines for it, forever**, on top of CLAUDE.md's 768.

**Is it right?** The *conceptual* half (§Why, §The Root, §Agent Lifecycle, §The Rules, §The Forcing Function, §Name) is accurate, well-argued, and genuinely load-bearing — it is the only place the *intent* of the system is written down, and an agent that has not read it will make wrong structural decisions. **That half earns its keep.**

The *inventory* half has rotted in exactly the way the thesis predicts — R5, R6, R7, R8, R10, R13, R14. Worst: it tells agents children run in **tmux** (they have not since the tmux-mode removal), and its "Future / Potential Enhancements" section lists **three things that already exist** — including `.env` copying, which CLAUDE.md documents as working 230 lines earlier in the same required-reading set. A doc whose "not yet implemented" list is 3-for-4 wrong is worse than absent: an agent will trust it and avoid a shipped feature.

**Recommendation:**
1. **Fix R5, R6, R7, R8, R10, R13, R14** — seven one-line corrections, or delete §Future entirely (it is the densest concentration of rot in the file: 4 bullets, 3 wrong).
2. **Delete the CLI/MCP-tool inventories** (§Interface, §User CLI, §Agent MCP Tools, §Messaging, §Reporting, lines 121–176, ~56 lines). Every tool listed there ships its own MCP `description` field into the agent's context automatically — this is a hand-maintained copy of something the runtime already injects, and it is already incomplete (omits `wake`, `pause`, `toast`, `status`).
3. **Move to `docs/architecture/vision.md`** and demote CLAUDE.md's first line from "Read `DESCRIPTION.md`" to a breadcrumb. The concept half is worth reading **once**, not on every turn; keeping it mandatory is the same always-on-cost mistake CLAUDE.md makes, just in a second file.

Net: 195 → ~110 lines, and out of the mandatory path.

---

## 8. Reflections

**Surprising.** I expected rot in `## Repo Layout` and found it, but the sharper finding is *directional*: in **every** case where CLAUDE.md duplicates something else, the CLAUDE.md copy is the stale one — no exceptions across 14 duplications. Distance from the code predicts wrongness perfectly here. Second surprise: the rot is **bidirectional and self-contradicting inside one required-reading set** — CLAUDE.md:427 documents `.env` copying as working while DESCRIPTION.md:193 lists it as unimplemented, and both are mandatory. Third: `internal/config/config.go` *already reflects over* `sprawl:"purpose=…"` tags — the generated config reference is ~10 lines away and nobody has written it. Fourth: I went looking for prose-vs-code rot and found the *product itself* is wrong (A3: the F1 help modal describes Up/Down backwards, while both CLAUDE.md and the code comment have it right) — the copy users read is the least accurate of three.

**Open questions.** (1) Who reads `DESCRIPTION.md`'s vision half, and how often — if the answer is "once, at onboarding", the always-read mandate is indefensible and my §7 recommendation is a slam dunk; if managers genuinely re-derive decomposition rules from it mid-run, it is load-bearing per-turn and I would keep more of it. I could not settle this from the tree. (2) Is the 36-file test gap (R1) intentional (generated code, build-tag files, trivial types) or accumulated drift? `internal/hub/gen/**` is clearly exempt; `internal/supervisor/drain.go` clearly is not. (3) I did not measure actual token cost, only lines — the `## Code Patterns` paragraphs are ~2,500 characters on 2 lines, so a line-count-ranked cut list understates them; a token-ranked one would put them at #1 by a wider margin than I show.

**Next, with more time.** (a) File the `drain.go`-has-no-test finding as its own issue — it is a code defect surfaced by a docs audit and does not belong buried here. (b) Fix A1/A2 immediately: the `/linear-issues` skill documents three MCP tools that do not exist, which is strictly worse than the CLAUDE.md rot I was sent to find, because a skill is read *at the moment of acting*. (c) Write the M2 bidirectional keybinding test — it is small, it kills two live defects, and it is the cleanest demonstration in this repo that a test beats a paragraph. (d) Grep the other 500 lines of CLAUDE.md for the same three mechanisms (generated inventory, tested source-of-truth, deleted-because-tooling-enforced) — I would expect the yield to be similar, and the other two researchers can confirm.

**Caveat on my own evidence.** Every claim in §2 is a command run at this worktree's `HEAD` and reproduced in the Evidence column, so each is falsifiable by re-running it. The line counts in §4 are arithmetic over the section boundaries in §1; the "≈34 lines retained" figure is my proposed rewrite, not a measured artifact, and should be treated as a target rather than a result.
