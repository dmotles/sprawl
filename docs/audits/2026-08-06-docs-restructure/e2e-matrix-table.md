# Decision: the CLAUDE.md e2e mandatory-test table

**Audit date:** 2026-08-06 · **Auditor:** scout (researcher) · **Surface:** `CLAUDE.md` § Validating Changes item 5 (lines 624–768 at commit `0f40f67`+) — the mandatory-test e2e harness preamble + 30-row touched-file → matrix-row table.

---

## Verdict

**The table does not earn its keep in CLAUDE.md. Replace it.**

The three theses tested all hold, quantitatively:

1. **The rot caused a live bug (confirmed).** The `tui-live-render` row states that `UserMessageSentMsg` is "the FIRST non-nil pump event of every typed turn" and must re-arm `WaitForEvent`. On the ordinary submit path it is not a pump event at all — it is returned directly from the `SendMessage` `tea.Cmd` (`internal/tuiruntime/tuiadapter.go:246`). The reducer re-arms anyway (`internal/tui/app.go:1266-1268`, with a comment restating the table), spawning an extra concurrent one-shot `WaitForEvent` reader per typed prompt. Concurrent readers are exactly the mechanism the QUM-1111 findings doc (`docs/research/qum-1111-repro-and-mechanism.md`, commit `0f40f67`) identifies as breaking bus-Seq ordering and producing the permanent-pending bubble. The documentation and the code corroborate each other and are wrong together — which is why source audits that checked code against doc passed.
2. **The rot is measurable, ongoing, and has a mechanism (confirmed).** A mechanical sweep of all 604 backticked tokens found 4 symbols named as live that are deleted, 1 direct inter-row contradiction, 1 behavior contradiction (QUM-1111), 1 relocation errata the table carries *inline* instead of fixed (QUM-1084), **5 row scripts that exist in the driver but that no table entry obligates**, and — the sharpest class — **hand-maintained censuses that went stale because people complied with the rule they describe**: the preamble's "All 11 `needs_claude` gates" is now 32 gates. Details and cause classification in §B.
3. **The precision is fake for real traffic (confirmed).** Over the last 400 commits, the **median production commit obligates 14 of 30 rows**; the modal commit obligates 18 (that is `internal/tui/app.go`'s obligation — the single most-changed file in the repo). 62% of production commits obligate ≥10 rows. For the files people actually touch, the table's answer is already "run half to two-thirds of everything."

Meanwhile the section costs **5,251 words / 43,385 chars ≈ 10.8k tokens = 49.6% of CLAUDE.md**, resident in every agent's context on every turn.

**Recommendation (do this):** move the mechanism into a `.claude/skills/e2e-matrix/` skill loaded on demand; keep ~150 words in CLAUDE.md (the union rule + a trigger line); replace the prose files-column with per-row machine-checkable `# gates:` manifests in the row scripts themselves, enforced by a cheap existence-check unit test. Ranked alternatives with costs in §Alternatives.

To be fair to the table before condemning it: **315 of 399 checkable symbol claims (79%) verified live and correctly placed**, its "verified absent — do not grep for them" deleted-symbol lists checked out 100% accurate, and its tacit per-row warnings ("this row is the ONLY live coverage of X") encode real, hard-won incident knowledge. The content is largely good. The *mechanism* — a hand-maintained prose table that nothing executes or checks — is what failed, and the table itself predicted this: *"this table is prose, so a refactor that moves code between files silently relocates it out from under its gates and nothing in the pipeline notices."* It documented its own failure mode and the failure mode happened anyway, twice (QUM-1084, QUM-1111). That is evidence about the mechanism, not about the authors.

---

## A. Fan-out — does the table narrow anything?

Method: parsed all 30 e2e rows (+1 non-e2e race-gate row); matched every git-tracked file against each row's literal paths, globs (`*` crossing `/`, the table's own mandated fail-safe reading), and directory prefixes; obligation = matched row ∪ its mandated re-runs. Cross-referenced with `git log -400 --name-only`.

**Static distribution** — 214 of 982 tracked files are matched by ≥1 row:

| direct rows | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | 12 | 13 |
|---|---|---|---|---|---|---|---|---|---|---|---|
| files | 177 | 17 | 7 | 4 | 2 | 1 | 1 | 2 | 1 | 1 | 1 |

Looks narrow — 83% of matched files hit 1 row. But the tail *is* the traffic:

**Top most-changed production files (last 400 commits) vs obligation** (direct rows / total incl. mandated re-runs, of 30):

| changes | file | direct | total obligated |
|---|---|---|---|
| 94 | `internal/tui/app.go` | 12 | **18** |
| 52 | `internal/supervisor/real.go` | 13 | **13** |
| 43 | `internal/runtime/unified.go` | 9 | **14** |
| 43 | `internal/tui/messages.go` | 5 | 10 |
| 40 | `cmd/enter.go` | 8 | 8 |
| 33 | `internal/supervisor/runtime_launcher.go` | 6 | 6 |
| 26 | `internal/backend/session.go` | 4 | 7 |
| 22 | `internal/supervisor/runtime.go` | 8 | 8 |
| 20 | `internal/sprawlmcp/server.go` | 7 | 7 |

**Per-commit obligation** (the decisive stat — union over each commit's changed files, 277 of the last 400 commits touch production `.go`):

- **median 14 rows, mean 12.0, mode 18** (59 commits — every commit touching `app.go`)
- ≥10 rows: **62%** of commits · ≥5 rows: **74%** · 0 rows: 13%
- Full distribution: `{0:37, 1:26, 2:6, 3:1, 4:1, 5:7, 6:6, 7:2, 8:15, 9:4, 10:8, 11:3, 13:16, 14:25, 15:5, 16:4, 17:4, 18:59, 19:14, 20:2, 21:5, 22:3, 23:1, 25:11, 26:10, 27:1, 28:1}`

The distribution is bimodal: ~25% of commits (periphery) get 0–2 rows; the rest jump straight to 8–26. A two-tier rule — "core files → run everything; periphery → the 1–2 named rows or none" — reproduces essentially all of the table's discrimination. **35 rows of precision buy a binary decision.**

Note these counts follow the table's *own* mandated reading (globs cross `/`, symbol scopes never narrow a path match, when in doubt include). An agent that follows the instructions gets these numbers; any smaller number is the "verified narrowing vs careless narrowing" the table itself forbids.

## B. Rot — verified against the tree

Method: extracted all 604 backticked tokens (165 path/glob/dir + 439 symbol); classified; word-bounded `git grep` for each symbol's distinctive component across code files; manual triage of every automated hit (comments-only hits, deleted-context markers, and parser mis-associations were individually checked and excluded — raw pass had 60 "relocated" candidates, of which all but the ones below were tooling noise or correct-as-written).

### Confirmed defects (present at HEAD)

| # | class | row | claim | reality |
|---|---|---|---|---|
| 1 | behavior contradicted by code (QUM-1111 class) | `tui-live-render` | `UserMessageSentMsg` is "the FIRST non-nil pump event of every typed turn" and its reducer must re-arm `WaitForEvent` | On the typed-submit path it is returned directly from the `tea.Cmd` (`tuiadapter.go:246`), never travels the bus; the mandated re-arm spawns 1 extra concurrent reader per prompt → the QUM-1111 ordering bug (`docs/research/qum-1111-repro-and-mechanism.md`) |
| 2 | dead symbol named as live + **inter-row contradiction** | `recall-sendnow` | `internal/tui/input.go` (`SetQueuedCount` + `⏳ N queued` indicator) | Retired by QUM-833 (`ea693d5`); zero code hits. The `notif-stacked-restart` row in the *same table* says "retired `SetQueuedCount`/`queuedCount`/`pendingQueuedIndicator` ⏳ indicator." One row instructs you to look for what another row says is gone. |
| 3 | dead symbol named as live | `sendnow-tui` | "shares the QUM-827 flag/`openFrameTurn`-clear/`consumeInterruptPending` invariants" | `consumeInterruptPending` removed by QUM-931 (`e5b0c72`); zero code hits |
| 4 | relocation, carried as inline errata (QUM-1084 class) | `drain-row-inject` | `sweep_coordinator.go` (`OnDelivered` — "QUM-1084: the table filed this under `runtime_launcher.go`, but it is defined here") | The table's fix for a past relocation was to append a correction *into the cell* rather than have a mechanism that prevents the class. The errata is now part of the 43k chars everyone reads. |
| 5 | rows that exist but the table never obligates | — | table claims to be the derivation source for the obligation | **5 driver rows have no table entry**: `ask-user-question-idle` (QUM-635), `attach-blocks` (QUM-860), `blurb-live-gate` (QUM-899), `liveness-transitions` (QUM-615), `qum903-false-thinking` (QUM-903). `scripts/e2e-matrix.sh` discovers rows from `scripts/e2e-tests/*.sh`; the table is updated by hand and lags. |
| 6 | dead symbol in a *fallback script* the table still endorses | `idle-interrupt-inject` prose | legacy `scripts/test-*-e2e.sh` remain "available as a fallback" | `scripts/test-notify-tui-e2e.sh` still references `ForceInterruptDelivery`, deleted by QUM-821 |
| 7 | dead symbol named as live | `esc-interrupt-survives` | "cleared in `closeFrameTurn` at both `st.reset()` sites" | `st.reset()` has **zero** occurrences in `unified.go` — restructured away by QUM-931 (`e5b0c72`), the same commit that killed defect #3. One commit invalidated two rows and neither was updated. |
| 8 | **stale census (omission-by-compliance)** | preamble skip-gate prose | "All **11** `needs_claude` gates read the flag inside the binary-absent branch" | **32** row scripts now declare `needs_claude=1`. The 21 new gates were added by authors *correctly following the convention*; the census was made wrong by compliance. Nobody has verified the "reads the flag inside the binary-absent branch" claim for the 21 uncounted ones. |
| 9 | stale census (omission-by-compliance, self-hedged) | `tui-live-render` | "Reducers that re-arm today: …" (names 14 + the finalizeTurn trio) | 4 additional reducers re-arm at HEAD and are unlisted: `ChildTranscriptMsg`, `RestartCompleteMsg`, `SessionInitializedMsg`, `SessionRestartingMsg`. The row does hedge ("NOT exhaustive"), but the roster is what an engineer pattern-matches against — 18% of it is missing. |

Score: **399 symbol claims checked → 315 verified OK (79%), 4 stale-as-live, 1 behavior contradiction, 1 inline errata, 1 inter-row contradiction, 5 orphan rows, 2 stale censuses.**

### Rot by cause — and why it matters for the alternatives

Classifying every confirmed defect by mechanism (per the cross-cutting heuristic from the L1–353 audit: rot enters via *enumeration*, not age — the detector is "a count followed by a list of code entities"):

| cause | instances | catchable by a "named path/symbol still exists" CI check? |
|---|---|---|
| (a) code deleted, row still names it | #2, #3, #7 (+#6 in a legacy script) | **yes** |
| (b) code moved out from under its gate | #4 (QUM-1084) | **yes** (path-existence at file granularity) |
| (c) **new site added that the census does not list** | #5 (5 orphan rows), #8 (11→32 gates), #9 (14→18 reducers) | **no — structurally blind.** Every name in the table still exists; the table is wrong about the *set*. |
| (d) behavior claim contradicted by code | #1 (QUM-1111) | **no** |

Class (c) is the largest bucket by instance count and the `notif-stacked-restart`-style "only live coverage" claims are one refactor away from joining it. This is the decisive argument against enforce-only (§Alternatives #2): an existence checker makes the table *look* verified while its censuses silently under-count. The only census that cannot drift is one derived from the tree at read time (`ls scripts/e2e-tests/*.sh`, `grep -l needs_claude`), i.e. generation, not enforcement.

**`file:line` citations:** the table proper carries exactly one (`items.go:55-65`, the `pendingStyler` rationale) — currently accurate, but the class is unmaintainable by construction (the broader CLAUDE.md already has a confirmed-dead one: `internal/agent/retire.go:82` in the lifecycle section, per the L1–353 audit). Same referential-integrity failure class; same remedy (see reconciliation note in §Alternatives). Every "deleted — verified absent, do not grep" list in the table (viewport-resync's watchdog cluster, idle-continuation's auto-continue cluster, busy-queue-typing's deleted handlers) checked out **accurate** — the deleted-context hits my sweep flagged were all comments or test-file prose. Rot concentrates precisely where the table asserts *liveness*, because liveness claims silently expire and nothing re-checks them.

Rot rate in time terms: QUM-833 landed well over 100 commits ago; defect #2 sat in the table across every audit since, including the two source audits that missed QUM-1111. A prose claim in this table has no expiry mechanism other than a human re-reading 3,013 words of cells.

## C. Coverage gaps

- **The table's own admitted gap** (QUM-1073, async frame to a busy child) is real and stated three times — the honesty is a point in the authors' favor and irrelevant to the mechanism question.
- **The 5 orphan rows are gaps of the worst kind**: `qum903-false-thinking` guards the in_turn 3-state machine in `internal/runtime/unified.go` / `internal/backend/session.go` — two of the most-changed files in the repo (43 and 26 changes/400) — yet a commit to those files derives 14 and 7 rows *not including the row that actually guards that behavior*. Same shape for `attach-blocks` (QUM-860 multimodal ack contract: `app.go`/`tuiadapter.go` changes obligate 18 rows, never this one).
- **Top-changed production files with zero rows** (mostly defensible — non-e2e surfaces — but the TUI-render trio is questionable): `internal/tui/help.go` (11), `internal/agent/prompt_mode.go` (9), `internal/agentops/retire.go` (8), `internal/config/config.go` (8), `internal/tui/tree_orbital.go` (8), `internal/tui/view_cache.go` (8), `internal/agentops/report.go` (8), `internal/tui/layout.go` (7).

## D. Cost

Measured at HEAD (`wc` over lines 624–768):

| unit | table proper | preamble | section total | CLAUDE.md | section share |
|---|---|---|---|---|---|
| words | 3,013 | 2,238 | **5,251** | 10,577 | **49.6%** |
| chars | 29,006 | 14,379 | **43,385** | 79,511 | **54.6%** |
| est. tokens (chars/4) | ~7.3k | ~3.6k | **~10.8k** | ~19.9k | — |

(The audit brief's "3,013 words, 28%" counted the table rows only; the mandatory preamble — which is part of the same obligation and unreadable without the table — brings the true figure to half the file.)

Ongoing cost: this rides in **every agent's system prompt on every turn**. At a modest 8 active agents × 40 turns/day = 320 turns, that is ~3.5M tokens/day of context traffic for this section alone (cache-read priced, but paid forever), plus the harder-to-price costs: **5.4% of every 200k context window permanently occupied**, and attention dilution — the QUM-1111 wrong claim was *read* by every agent that touched the TUI and reinforced by its echo in a code comment.

## Six lenses

The section is ~90% PROCEDURAL (how to derive rows, how to invoke the driver, exit codes) and CONTEXTUAL (when each row applies), with load-bearing TACIT fragments embedded in row cells ("only live coverage of X", "blind spot: no row reaches Y"). It contains essentially no DECLARATIVE or AGENT-BEHAVIOR content that must be resident every turn. Procedural + contextual + tacit content that applies only at validation time is the textbook case for **on-demand loading (a skill) and machine-checkable manifests** — the only piece that must stay resident is the ~3-sentence trigger + rule.

---

## Alternatives, ranked

### 1. RECOMMENDED — skill + manifests + enforcement (hybrid of "generate it" and "delete the precision")

- **CLAUDE.md keeps ~150 words:** the rule ("the obligation is the union over every touched path; over-running costs a CI slot, under-running ships the defect green; a skip discharges nothing") + trigger ("touched production Go, scripts, or web/src/wire → invoke `/e2e-matrix` before claiming validation").
- **Each row script declares its own gate** in a header the driver and a checker can parse, e.g. `# gates: internal/tui/app.go internal/supervisor/*.go`. The mapping lives *next to the assertions it belongs to*, so the person adding a row cannot forget the table — there is no separate table. The 5 orphan rows become structurally impossible. **Design rule, per the class-(c) finding in §B: gates must be patterns (files, globs, dir prefixes), never symbol lists or counted censuses** — a pattern stays correct when a new site is added under it; a roster does not. Any prose that needs a census ("all N gates", "the reducers that re-arm") must derive it at read time (`grep -lc`) or not state a number.
- **`scripts/e2e-rows-for-diff.sh`** computes the union from `git diff --name-only` mechanically — replacing the double-grep-plus-hand-check-the-globs procedure that currently takes ~800 words to describe and that QUM-1081 documents agents getting wrong.
- **A unit test in `make validate`** (same shape as `test-e2e-matrix-unit`, pure shell/go, seconds) asserts every literal path in every `# gates:` line exists in the tree → the QUM-1084 relocation class fails CI within one commit instead of surviving indefinitely. Symbol names are dropped from the *obligation* surface entirely — by the table's own rule they "never narrow a path match," so they were rationale, not mechanism; rationale moves into the row scripts' comment headers (where much of it already lives).
- **The tacit fragments** (only-live-coverage warnings, blind-spot notes, QUM-1073) move to the skill and the row-script headers.
- **Lost:** always-resident visibility (mitigated by the trigger line); behavior claims like "reducer X must re-arm" are no longer near the code — but QUM-1111 shows that having them near the code *entrenched* the error; a claim that belongs anywhere belongs in a test (`app_drop_rearm_test.go` already pins the re-arm contract, for better or worse).
- **Build cost:** ~1 day (header convention + 40-line diff script + existence-check test + skill doc). **Per-turn cost:** ~150 tokens resident vs 10,800 today — a 98.6% reduction on this surface, −54% of CLAUDE.md.

### 2. Enforce-only (keep the table, add a CI checker)

A validate-stage script parses the prose table, extracts backticked paths, fails on dead ones. Kills classes (a) and (b) for ~2 hours of work — but is **structurally blind to class (c)**, the largest bucket (§B): every name in a stale census still exists, so the check passes while the table under-counts the tree. It also keeps all 10.8k tokens resident, and cannot check symbols reliably (dotted/method-qualified names, deleted-context prose — my own sweep needed manual triage of 60 false positives, and this repo has already **built and rejected** a deterministic prose-parser for a structurally identical class, documented in Code Patterns: it "acquired four separate blind spots of the same class it detected"). Cheap, real, and weaker than it looks. Acceptable only as a stopgap.

**Reconciliation with the proposed markdown line-cite lint:** the L1–353 audit proposes a ~15-line CI lint banning `path.go:NNN` citations in tracked markdown. That lint, the `# gates:` path-existence check (alternative 1), and the dead-path table check (this alternative) are one mechanism — referential integrity of doc→tree references — and should land as **one checker with two rules**: (i) every literal path in a machine-readable gate manifest exists; (ii) no `file.go:NNN` cites in tracked markdown (link to a symbol or an anchored comment instead). Do not build two separate parsers.

### 3. Delete the precision (coarse mapping, no tooling)

Replace 30 rows with ~6 lines: `internal/tui/`+`internal/tuiruntime/` → TUI rows; `internal/supervisor/`+`internal/runtime/`+`internal/backend/`+`internal/messages/`+`internal/inboxprompt/` → runtime/lifecycle rows; `internal/hub*`+`web/src/wire` → `hub-e2e`; `Makefile`/race-gate files → `make test-race-gate`; unsure → `all`. Given the median commit already obligates 14 rows, the coarse rule over-runs by ~2× on wall-clock for core commits and matches exactly on the periphery. Zero build cost, saves ~10.5k tokens immediately, cannot rot (directory prefixes outlive refactors). **Lost:** the periphery discrimination (177 files that map to exactly 1 row now run a family) and all tacit fragments unless separately rehomed. Acceptable as an immediate measure; strictly dominated by alternative 1 long-term.

### 4. Regenerate the prose table from annotations (rejected)

Same source-of-truth move as alternative 1 but keeps emitting the giant table into CLAUDE.md. Fixes rot, keeps the 10.8k-token residency — pays the build cost without collecting the token savings.

**Sequencing suggestion:** do 3 (or the 150-word rule) + the alternative-2 stopgap immediately; build 1 when someone has a day; fix the two live table defects (#1–#3 in §B) *now* regardless, since agents are reading them today — #1 is an active bug-reproduction recipe.

---

## Appendix: methodology & controls

- Parser: split each `   | files | row | guards |` line right-to-left (one cell legitimately contains `` `||` ``); row identity = first backticked token in the row cell, validated against `scripts/e2e-tests/*.sh` (30/30 matched; the race-gate row is prose, matching its own "not an e2e row" label).
- Negative control for the rot sweep: the table's explicit deleted-symbol lists were run through the same pipeline and correctly flagged as absent-from-code (their `deleted`-context markers suppressed false positives), and symbols the sweep flagged were individually opened — 100% of "deleted-but-present" hits were comments/test-prose, i.e. the sweep *can* distinguish and the 3 confirmed dead-as-live findings survived that triage.
- Fan-out matcher deliberately implements the table's fail-safe glob reading (`fnmatch`, `*` crosses `/`) and directory-prefix matching; obligations include mandated re-run rows; `validate`/race-gate excluded from row counts.
- All numbers computed at the worktree HEAD of `dmotles/docs-audit-e2e-table`, 2026-08-06. No builds or tests were run (disk constraint); every claim is grep/`git log`-derived plus manual source reading of `internal/tui/app.go`, `internal/tuiruntime/tuiadapter.go`, `internal/tui/event_translate.go`, and `git log -S` for symbol death dates.
