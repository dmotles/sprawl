# Headline-number verification — adversarial QA over the four presence-claims

**Author:** audit (QA) · **Date:** 2026-08-06 · **Tree:** measured at `main` = `3d92e2c` (the same commit DECISION.md cites) from worktree branch `dmotles/docs-audit-numbers-verify`.
**Remit:** independently re-derive the four numbers driving D1/D2/D4 — not re-run the original authors' methods (a re-run is a copy, not a control). Read-only; no builds, no tests. All probes are `git`/`grep`/python over the tree.
**Companion:** `sentry`'s `absence-claim-verification.md` covered absence-claims; this pass covers the presence-claims it named as the largest unaudited surface.

## Verdict, in one table

| # | claim | my derived value | survives? | decision impact |
|---|---|---|---|---|
| 1 | median commit obligates **14 of 30** rows; mode 18; 62% ≥10 | median **14**, mode **18**, **61%** ≥10 — but the denominator is **29** table rows, not 30 | **SURVIVES** (robust to method; one label error) | **D2 stands.** The number is *not* method-dependent — sentry's specific suspicion is answered with a measured null. |
| 2 | **22%** of `docs/` refs dangle (152/688; 86% genuine) | **21.8%** (153/701; 86% genuine; never-existed = 21 exactly) | **SURVIVES** | **D4 stands**, incl. the 22%→4% corollary that sequences D3 (I measure the KEEP set at **4.1%**). |
| 3 | **315/399 (79%)** of the table's symbol claims verify | denominator is a real extraction (my count: 427 symbol-shaped tokens vs scout's 439) — but "verified OK" means *exists somewhere, incl. comments*, **not** *the row's claim is true* | **SURVIVES AS AN EXISTENCE MEASURE; OVERSTATED AS A TRUTH MEASURE** — I found ≥3 more dead-named-live symbols scout's sweep missed, in one row | Cuts *for* D2, not against it. But DECISION.md §1's "The content is *good*" should be weakened. |
| 4 | in **all 14** CLAUDE.md↔source duplications, CLAUDE.md is the stale copy — no exceptions | the "14" is **not reconstructible** from recon's document; the set was assembled by hunting staleness; **one verified inverse case exists** (help.go) and agreeing duplications were excluded | **DOES NOT SURVIVE AS STATED** — it is a selection effect, as the brief suspected | **D1 still stands** (direction is real), but "distance from code predicts wrongness, *perfectly*" must be retracted. |

Net: **no decision flips.** Two numbers are confirmed with independent derivations — a real result, per the brief. Two need restating: #3's semantics and #4's universality.

---

## 1. "Median production commit obligates 14 of 30 e2e rows; mode 18; 62% ≥10" — SURVIVES (with one denominator error)

**Property published:** *an agent following the table's own derivation rules on a typical production commit is obligated to run about half the matrix.* The suspicion (sentry, forge): the denominator is method-dependent because glob rows and directory-prefix rows are matched inconsistently.

**Row count, established first (per forge's three parsing hazards), two independent ways:**

- Backtick-aware cell splitting (splits on `|` only outside backticks — hazard 3) over the table body: **30 body lines = 29 e2e rows + 1 explicitly "not an e2e row"** (race-gate). Zero cell-split failures; every row's first backticked row-cell token resolves to a real script; zero duplicates.
- Driver-directory reconciliation: `scripts/e2e-tests/` holds **34** row scripts. 29 table rows + scout's 5 orphan rows (`ask-user-question-idle`, `attach-blocks`, `blurb-live-gate`, `liveness-transitions`, `qum903-false-thinking` — independently reproduced) = **34 exactly**.

The two methods agree with each other and disagree with scout: **the table has 29 e2e rows, not 30**, and scout's appendix claim "30/30 matched against `scripts/e2e-tests/*.sh`" cannot be literally true (30 + 5 orphans = 35 ≠ 34). This is a (small) census error inside the anti-census audit. The *rate* claims are unaffected; the label "of 30" should read "of 29".

**My matching rule, stated precisely:** patterns = every backticked token in the files cell matching `^(internal|cmd|scripts|web|\.claude|docs)/` or `Makefile`. Literal paths match by equality; entries ending `/` match by prefix (hazard 2); glob entries match by regex, computed **both** ways (hazard 1): Rule A `*` crosses `/`, Rule B it does not. Obligation = matched rows ∪ their table-mandated re-runs (one level, non-transitive — scout's reading). Commits = last 400 on `main` at `3d92e2c`; "production commit" = touches ≥1 `.go` under `internal/`/`cmd/` (n=280; excluding `_test.go` from the filter: n=272, results unchanged).

**Result:**

| variant | n | median | mode | ≥10 rows |
|---|---|---|---|---|
| Rule A (`*` crosses `/` — the table's mandated reading) | 280 | **14** | **18** (×58) | **61%** |
| Rule B (`*` does not cross `/`) | 280 | **14** | **18** (×58) | **61%** |
| literal-only — globs and prefixes dropped entirely (the "naive grep") | 280 | 13 | 18 | 61% |
| anti-gate mentions pruned from viewport-resync (my extraction's over-inclusion) | 280 | 14 | 18 | 61% |
| **direct rows only — no mandated re-runs** | 280 | **10** | **0** | 54% |

Full Rule-A distribution: `{0:41, 1:26, 2:7, 3:1, 4:1, 5:7, 6:6, 7:2, 8:15, 9:4, 10:8, 11:3, 13:15, 14:22, 15:8, 16:4, 17:4, 18:58, 19:14, 20:2, 21:5, 22:3, 23:1, 25:11, 26:10, 27:1, 28:1}` — near-identical to scout's, including the bimodality that grounds the "two-tier rule" argument.

**Positive controls:** (a) Rules A and B genuinely classify real changed files differently — `internal/hub/store/*.go`, `internal/sprawlmcp/calllog/*.go`, `internal/supervisor/liveness/*.go` all flip — so the A≈B null is informative, not a broken matcher. (b) The pipeline *can* move the headline: dropping mandated re-runs shifts median 14→10 and mode 18→0. The number is sensitive to a real methodological choice; it is just not sensitive to the choice sentry suspected.

**Whether the decision holds if the number moves:** the only lever that moves it (excluding re-runs) is not a defensible reading — the table mandates the re-runs in the row cells. And even median-10-of-29 with 54% ≥10 still says "the table's precision buys a near-binary decision," so **D2's premise survives every variant computed**. One nuance the re-run sensitivity adds: about a third of the median obligation is *fan-out via mandated re-runs*, not direct file matches — the table amplifies its own obligation. That is an argument *for* D2's mechanical derivation, not against it.

**Also captured, per forge:** deriving this number required three special-case parsing rules (backtick-aware split, two glob semantics, directory prefixes) plus a driver-directory reconciliation to establish the row count at all. That the table needs this to be machine-read is itself D2 evidence, whichever way the numbers land.

## 2. "22% of docs/ code-path refs dangle (152/688; 86% genuine deletions)" — SURVIVES

**My method (chosen independently, then compared):** regex `\b((internal|cmd|scripts|web/src)/[A-Za-z0-9_.\-/*]+\.(go|sh|ts|tsx))\b` over all 144 `docs/` files at `main:3d92e2c` (cipher's exact surface — verified 144, no `docs/audits/`); distinct refs per file, summed; existence against `git ls-tree -r main`; glob refs (13 of them) resolved by pattern-match against the tree rather than auto-dangled; deletion-vs-never-existed against the set of every path in `git log --all --name-only` (one pass, not per-ref).

**Result:** **701 distinct refs across 91 docs; 153 dangle = 21.8%; 21 never existed in history → 132 (86%) genuine deletions/renames.** Against cipher's 688/91/152/22%/21/86%: agreement to within 2% on the denominator and *exactly* on the never-existed count and the 91-doc spread. My regex differences (I catch globs and a slightly wider charset) account for the +13 refs.

**The concentration question the brief asked:** *not* driven by a handful of pathological files. Top-5 files hold **27%** of dangling refs, top-10 hold 46%, and **56 files** carry ≥1 dangling ref (cipher independently said "56 files would fail" — reproduced). The 22% measures broad rot, so the cut buys what D4 says it buys.

**The decision-critical corollary re-derived:** on cipher's KEEP set (reconstructed from Appendix B buckets: 26 files vs their 24 — bucket-boundary ambiguity, noted), I measure **74 refs, 3 dangling = 4.1%**, and 2 of the 3 are the same illustrative paths cipher named (`internal/foo.go`, `scripts/test-guard-foreign-content.sh`; my third, `cmd/retire*.go` in `merge-engine.md`, is a glob my regex catches and theirs didn't). So "the gate is impossible today and trivial after the cut" **holds**: D3's sequencing on D4 is sound.

**Positive control:** the same probe resolves 548/701 refs as existing, and the never-existed check finds `turnloop.go` etc. present in history while `internal/foo.go` is absent — both branches of every classifier produce positives.

**Nits:** `turnloop.go` dangles from **12** docs by per-file-distinct counting, not 13 (cipher's worked instance is off by one, immaterial). And the number remains a floor — symbol-level refs and package-only mentions are excluded by construction (cipher says so themselves).

**Whether the decision holds if the number moves:** it doesn't move — two independent extractions land within 2%. **D4 stands.**

## 3. "315 of 399 (79%) symbol claims verify" — real denominator; overstated semantics

**Denominator:** real. My independent extraction over the section (fence-stripped, per-line backtick tokens, lines 624–768): **789 tokens, of which 427 are symbol-shaped identifiers** (scout: 604 relevant tokens, 439 symbol) and 186 path-like (scout: 165). The counts differ by inclusion rules for command strings/env vars, but 399 "checkable symbol claims" is consistent with a real extraction, not an estimate. Scout's 3 headline dead-as-live symbols independently confirm at zero non-comment hits (`SetQueuedCount`, `consumeInterruptPending`, `st.reset()` — positive control: `drainPolicy`, `runDrain`, `matchPendingControl` return live hits under the same probe).

**Semantics — the part the brief flagged, and it is real.** I spot-checked ~25 claims *for claim-truth*, not existence. The good news first: the deep behavioral claims mostly verify **including their values** — `drainAsyncPriority = "next"`, child `interruptPriority: "now"` / weave `"next"`, `coalesceInterrupts` true-for-weave/false-for-child, `ackInterruptOnWrite` child-only, the `InFlightSystemEntryIDs` filter and DESTRUCTIVE `DrainStatusChangeLines` read, `IsTerminal = {retired, retiring}`, `pendingStyler`/`SetZonePending` with its naming rationale still at items.go:55–65, `TestTranslateRuntimeEvent_BackendFaultedHasNoCase`, the QUM-1000 `settleNeverAcked` cluster. The table's content really is substantially good.

**But "verified OK" ≠ "the row's claim is true," and here is a concrete case.** QUM-931 (`e5b0c72` — the *same commit* scout cites for defects #3 and #7) also deleted the `interruptPending` bool, the `frameTurnOpen` **field** (now derived: `frameTurnOpenLocked()`, whose comment reads "Successor to the QUM-927 frameTurnOpen field"), `autoTurn.open`, and the clear-paths the esc-interrupt-survives row describes in detail — `openFrameTurn`'s clear-on-open, the `setPhaseLocked` conditional clear, the `system/init` arm-retire. The code comment at `unified.go:145` says these are **"DELETED, not ported."** All of them are still described as the live mechanism by the `esc-interrupt-survives` / `sendnow-tui` rows, and none is among scout's 4 counted dead-as-live. They "verify" under a grep because the replacement's own comment names them — a comment describing their deletion. So one commit's blast radius in the table is roughly **double** what the 79% figure absorbed, concentrated in one row.

**Whether the decision holds if the number moves:** the correction moves 79% *down* (on a claim-truth reading), which **strengthens** D2 — the number was the "content is good" counterweight, and it is the direction DECISION.md itself says deserves the most scrutiny. What must change is phrasing: §1's "The content is *good*. scout verified 315 of 399 symbol claims (79%) as accurate" should say **"79% of named symbols still exist where named; the behavioral claims spot-checked mostly hold, but symbol existence is an upper bound on claim truth, and at least one row's described mechanism is wholesale replaced while its symbols still 'verify' via comments."**

## 4. "In all 14 CLAUDE.md↔source duplications, the CLAUDE.md copy is the stale one — no exceptions" — a selection effect, as suspected

Three independent problems, in increasing order of severity:

**(a) The "14" is not reconstructible from recon's document.** recon's §3 duplication map has **16 rows**. recon's §8 says "no exceptions across 14 duplications." recon's first verification pass found "14 rotted claims" (§8, first sentence) — but those R-numbers include 9 claims about `DESCRIPTION.md`, which is not a CLAUDE.md↔source duplication. No enumerable set of exactly 14 CLAUDE.md↔source duplications exists in the document. DECISION.md §2.3 then restates it with a precision ("In all **14** … No exceptions") the source cannot support. A count that cannot be re-derived from its own source document, cited inside an audit whose central finding is that counted censuses rot — that is the finding.

**(b) The set was assembled by hunting staleness, so 14/14 is circular.** recon's own §2a lists duplications that **HOLD** — the bind-failure two-branch policy, the `_e2e_cleanup`/`_unit_reset_markers` citations, the four DI exemplars — i.e., duplications where the CLAUDE.md copy is *correct today*. These "both agree" cases are excluded from the 14 by construction. A perfect ratio over a staleness-selected set says nothing about direction; it says the collector kept what it was collecting.

**(c) A verified inverse case exists, in recon's own report.** recon's A3: the F1 help modal — *product code* — documents Up/Down as "Navigate input history (or scroll output when input empty)". Verified at `internal/tui/help.go:46` today: it is **backwards**, while CLAUDE.md:483 and the app.go handler are **right**. That is a duplication in which the copy *in the code* is the stale one and CLAUDE.md is correct. Also verified: `SPRAWL_TMUX_SOCKET` appears in CLAUDE.md and **nowhere** in `.claude/skills/` (grep exit 1) — a CLAUDE.md↔skill duplication where CLAUDE.md is the only complete copy. Both were in recon's own findings (A3, §3 tmux-safety row) and both sit outside the "14."

**Positive control:** the direction itself is real — I re-verified two of the claimed stale-CLAUDE.md instances raw: `Makefile:4` has no bare `test` prerequisite while CLAUDE.md:620 still advertises one (R2 ✓), and `anyModalUp` at `app.go:3153` gates on six modals while CLAUDE.md:479 lists five with two wrong members (R16 ✓). So my probe can find stale-CLAUDE.md cases *and* stale-elsewhere cases; it is not blind in either direction.

**Whether the decision holds:** **D1 stands** — CLAUDE.md really does carry many stale duplicates, and cutting to pointers is still right. What does not survive is the generalization built on the 14/14: **"Distance from code predicts wrongness, perfectly" (DECISION.md §2.3) is false as stated** — help.go is at distance zero and wrong. The honest restatement: *"among the divergent duplications the audit found, the CLAUDE.md copy was the stale side every time; the sample was selected for staleness, agreeing copies were excluded, and at least one inverse case (the F1 help modal) exists in the adjacent surface."* The practical rule that follows is also different and more useful: **any unexecuted copy can rot, including the one in the product** — which argues for recon's M2 (single tested source of keybindings) rather than for trusting proximity.

---

## Reflections (required by brief)

**Gaps in the acceptance criteria / brief:**
- #3 as briefed is not fully verifiable without redoing scout's entire 399-claim sweep with per-claim truth adjudication — days of work. I established the denominator's reality, confirmed the flagged defects, and sampled ~25 claims for truth; the esc-row counterexample is existence-proof that the 79% overstates claim-truth, but I cannot give a corrected percentage.
- #4's "14" turned out to be unverifiable *as a number* — the correct response was to attack its construction rather than recount it, which is what I did; but note a different QA could not "re-derive 14" either, because no such set is enumerated anywhere.
- The brief said "median 14 **of 30**" — the 30 itself was wrong (29). Numbers quoted in briefs propagate; this one came from scout's own mislabel.

**Risks remaining:**
- My #1 pattern extraction treats every path token in a files cell as a gate, including anti-gate mentions ("the only `LivenessProbe` left is `dead_routing.go`'s **unrelated** type"). Pruning the two clearest cases moved nothing, but a systematic gate-vs-mention adjudication of all 29 cells was not done — and cannot be done mechanically, which is itself a D2 argument.
- #2 remains a floor (file-path refs only); cipher's own suggestion to measure symbol-level dangling is still unmeasured and would likely be worse.
- I did not re-verify scout's *static* fan-out table (214-of-982 files matched) or the cost figures (49.6% of CLAUDE.md) — the per-commit distribution was the load-bearing number.
- Rows.json, scripts, and raw outputs live in `/tmp/audit-num1/`, `/tmp/audit-num2/` (session-scoped, not committed); the methods are stated precisely enough above to re-derive without them.

**What I would check next with more time:** (a) a corrected claim-truth rate for #3 over a random 50-claim sample with stated adjudication rules; (b) the symbol-level dangling rate for docs/; (c) whether the 5 orphan rows' gate comments (in the row scripts) would have caught the QUM-931 blast radius that the table missed — i.e., prototype evidence for D2's `# gates:` design.
