# Agent-facing documentation restructure — decision document

**Author:** forge (manager) · **Date:** 2026-08-06 · **Phase:** 1 (audit) + 2 (bucketing). No file outside `docs/audits/` was modified.
**Inputs:** five independent read-only audits, one per surface. Each researcher was told the thesis *as a thesis to test*, told to default to cutting, and required to grep every factual claim against the tree.
**Verification:** every headline number below was re-derived by me from the tree at `3d92e2c`, not taken on report.

| # | surface | researcher | detail document |
|---|---|---|---|
| 1 | `CLAUDE.md` L1–353 | query | `claudemd-part-a.md` |
| 2 | `CLAUDE.md` L354–768 (ex-table), `DESCRIPTION.md`, `CLAUDE.local.md` | recon | `claudemd-part-b.md` |
| 3 | the e2e matrix table | scout | `e2e-matrix-table.md` |
| 4 | all 144 files under `docs/` | cipher | `docs-directory.md` |
| 5 | `.claude/skills`, `.claude/agents`, the agent-behavior gap | prism | `skills-and-agent-behavior.md` |

---

## The decision, in one table

| | now | proposed | change |
|---|---:|---:|---|
| `CLAUDE.md` | 768 lines / 10,577 words | **~75 lines** | **−90%** |
| `docs/` | 144 files / 39,778 lines | **24 files / 7,712 lines** kept, 55 archived, 65 deleted | **−69% of lines** |
| `.claude/skills/` | 7 skills / 3,262 lines | 7–9 skills, 3 rewritten against the current CLI | net roughly flat, **but 3 are actively wrong today** |

**Answer requested: yes or no to the six decisions in §5.** Everything before that is the argument.

---

## 1. Verdict: the thesis is confirmed, but its diagnosis needs one correction

Confirmed, quantitatively, on every surface:

- **The e2e table is 49.6% of `CLAUDE.md`** — 10.8k tokens, 5,251 words. The brief's "3,013 words / 28%" counted the rows only and omitted the mandatory derivation preamble. **Half the file is one table**, paid by every agent on every turn, forever.
- **Its precision is fake, as claimed.** Median production commit obligates **14 of 30 rows**; the mode is **18** (exactly `internal/tui/app.go`'s fan-out); **62% of commits obligate ≥10 rows.** For the files people actually change, the table's answer is already "run almost everything."
- **The rot is real, widespread, and has caused a live bug.** QUM-1111's false claim is still live at HEAD.
- **`docs/` has the same disease at 100-file scale.** 22% of code-path references dangle (152 of 688 distinct refs; 86% of those are genuine deletions, not typos). Only **6% of `docs/research/` survives contact with the tree**, and on close reading only **3 of 100 files** are prose describing how the system works today.

**The correction — and it changes what we should build.** The thesis says the file is bloated and its content is bad. The content is *good*. `scout` verified **315 of 399 symbol claims (79%) as accurate**, and found that **every single "verified absent — do not grep for this" list in the table was 100% correct**. `recon` found the prose uniformly well-argued. The table even **predicted its own failure mode in writing** and the failure happened anyway.

So this is not an authorship failure and we should not fix it by writing more carefully. It is a **mechanism failure**: unexecuted prose asserting properties of live code. The corollary matters for Phase 3 — **the tacit knowledge in these files is the valuable part and most of it must survive the cut**, relocated rather than deleted.

## 2. The mechanism, stated precisely: rot enters through *enumeration*

`query` isolated it; three researchers then confirmed it independently on their own surfaces. The failure unit is a **count followed by a list of code entities** — "three files", "both", "all 11", "the only".

The canonical instance is not a mistake:

> `CLAUDE.md` states `atomicDuration` is "duplicated in three files" and then reasons arithmetically over that set. **There are four.** The fourth landed by an author who followed the convention *correctly*.
> **The paragraph was made wrong by someone obeying it.**

The rule was worth writing. The roster of who obeys it was not, and the roster is what decayed. Confirmed at scale: the table's "all 11 `needs_claude` gates" is **32** — I counted — a 3× stale census produced entirely by compliance.

Four consequences, in descending order of how much they should change our plans:

1. **An existence-checking CI is structurally blind to this class.** Nothing is dead; sites were *added*. This materially weakens "just enforce the table with CI" and is why `scout`'s recommendation declares gates as **path patterns, never symbol rosters**.
2. **Claims of absence are stable; claims of presence expire silently.** Every "this was deleted, do not grep for it" list was perfect, while presence-claims rotted. **Documentation may safely say what is gone. It may not say what is there.** This is the single most useful editorial rule to come out of the audit.
3. **Distance from code predicts wrongness, perfectly.** In all **14** CLAUDE.md↔source duplications `recon` found, the CLAUDE.md copy is the stale one. No exceptions. `make validate` is described three times; two are wrong, in non-overlapping ways.
4. **The heuristic outperforms careful reading.** `recon` ran the enumeration grep across 270 lines *they had just finished hand-auditing* and it found **three defects they had missed — a ~25% miss rate.** Structural greps beat diligence.

**The strongest single datum in the audit** came from `prism`, unprompted: while writing the section documenting counted-census errors, they introduced one ("~18 citations"; it is 13), caught it only by checking their own claim, and left the correction visible. An expert actively warning about this class produced an instance of it inside the warning. **No amount of care fixes this. Only mechanism does.**

## 3. What the audit found that is *not* documentation work

These are the findings I most want not to be lost in a docs cleanup. Four are defects in the product; one is a safety gap in how we run agents. All are verified.

| # | finding | evidence | owner |
|---|---|---|---|
| **P1** | **Researcher and QA agents receive no safety guidance at all.** The `Executing actions with care` block, the prompt-injection/hooks section, and the destructive-var guardrail are present for engineer and manager, **absent for both researcher and QA**. The `rm -rf "$VAR"` rule exists **nowhere else in the repo** — not CLAUDE.md, not any skill, only two Go constants. QA is ordered to run `make validate` while told nothing about concurrency, making it the role least able to recognise a contention false-RED. | pinned prompt goldens in `internal/agent/` | **weave** — I am spawning researchers under this gap right now |
| **P2** | **`DESCRIPTION.md` asserts a safety property the code lacks.** "If the name pool is exhausted, the system errors… a natural ceiling on system complexity." That string is **absent from the tree** (I verified); `AllocateName` falls into an unbounded `for i := 1; ; i++`. Exact shape of QUM-1111. | `grep 'no more agents can be spawned'` → 0 hits | product question |
| **P3** | **Security finding sitting unfixed in an unread archive for four months.** `open-source-readiness/03-security-audit.md` filed a CRITICAL agent-name path traversal, "no validation anywhere." I verified: `func [Vv]alidateAgentName` → **zero hits**. This is a public repo. | verified at HEAD | **should be a Linear issue before anything is archived** |
| **P4** | **`internal/supervisor/drain.go`: 443 lines, no test file** — while CLAUDE.md instructs agents that every file has one (36 of 216 do not). The e2e table calls this file load-bearing across four rows. | verified | engineering |
| **P5** | **5 orphan e2e rows the table never obligates anyone to run**, incl. `qum903-false-thinking` guarding `unified.go`/`session.go` — files changed 43 and 26 times in 400 commits. | `grep -c` in CLAUDE.md → 0 | **tower has taken this** |

Two more, already routed: the **merge engine un-commits an agent's work when the squash commit's pre-commit hook fails, with no rescue ref** (I hit it twice today on a markdown-only merge; `vault` confirms it is the second production firing and that fixes are landed-but-not-installed), and **`merge(no_validate:true)` does not reach the pre-commit hook** (QUM-1101; being deleted by QUM-1087's no-squash restructure).

## 4. The honest counter-arguments

Three things survive the audit, and one common-sense remedy is wrong.

**(a) Some prose genuinely cannot rot, and must be kept.** `## Terminology` — 8 lines, no QUM reference, describes *our vocabulary* rather than the code. It is structurally unrottable and every agent needs it. `## Meta: Developing Sprawl Inside Sprawl` — 4 lines, orientation derivable from nothing. Keep verbatim. The selection criterion falls out: **content about our rules, vocabulary, and principles is durable; content about the current state of the code is not.**

**(b) Expensive tacit knowledge must move, not die.** The 117-line squash-merge recovery block ("both natural checks lie, in opposite directions") is unrecoverable from reading code and cost real incidents to learn. `cipher`'s runners-up are the same: `claude-code-host-protocol.md` is 1,151 accurate lines whose defect is *omission*, not error. **The failure was never writing these down — it was treating "written down" and "in CLAUDE.md" as the same thing.**

**(c) Summarizing instead of cutting makes things worse.** `prism`'s finding, and it inverts the obvious remedy. CLAUDE.md absorbed `testing-practices`' audit numbers, including a rejected parser named `internal/shlint` — **absent from the working tree and from all git history**. The CLAUDE.md copy survives review only because it is too vague to falsify. **That is unfalsifiability, not durability.** A compressed copy is not a safer copy; it is a copy whose rot cannot be detected. Cut, or point. Do not summarize.

**(d) Prose *does* work — for one specific rule shape.** `prism` set out to conclude "prose isn't a mechanism" and a counter-example stopped them. The `git add -A` ban and the terminology rule sit in the same file with identical (zero) enforcement. The ban has **zero violations**; the terminology rule is violated on `DESCRIPTION.md`'s fourth paragraph. The difference is not the document — it is that **prose holds for rules gating a deliberate action, and fails for rules constraining incidental output.** Use it to decide what needs teeth: keep prose for "don't run this command," build a check for "don't use this word."

## 5. The six decisions

**D1 — Cut `CLAUDE.md` to ~75 lines.** 353→~14 (part A), 264→~34 (part B), the e2e section→~12 (a rule plus a skill pointer), plus Terminology and Meta verbatim. Comfortably inside the 150-line target; I am not asking for the extra 75.
*Lost:* nothing that isn't relocated. *Kept in place:* Terminology, Meta, the `git add -A` prohibition (5 lines), the union rule for e2e derivation.

**D2 — Replace the e2e table with a skill plus generated manifests.** ~150 words stay in CLAUDE.md (the union rule + "invoke `/e2e-matrix`"). Mechanism and tacit row notes move to `.claude/skills/e2e-matrix/`. Each row script declares `# gates: <paths/globs>` — **patterns only, never symbol rosters**. `scripts/e2e-rows-for-diff.sh` derives the union mechanically. ~1 day. *Stopgap this week:* a coarse 6-line mapping plus fixing the live defects.

**D3 — Build exactly one referential-integrity checker.** Four researchers independently proposed variants; they must converge or we will have three half-checks. Two rules: **(i)** every path referenced in tracked markdown exists; **(ii)** no `file.go:NNN` line citations in tracked markdown. This alone would have caught `turnloop.go`×13, `cmd/retire.go`, `internal/shlint`, and `query`'s bad `retire.go:82` cite. **Precondition:** D4, because at a 22% dangling rate the check cannot be turned on.

**D4 — `docs/`: keep 24, archive 55 to a one-way `docs/archive/`, delete 65.** Drops the dangling rate 22% → 4%, which is what makes D3 enforceable. Merge `docs/design/` into `docs/designs/` (verified an accident, not a taxonomy — `design/` has only ever contained `hub/`). Add a `docs/README.md` index; today only **3 of 144 docs** are reachable from any always-read file.
*Do not archive P3 first* — triage the security finding before it moves.

**D5 — Fix the skills layer before consolidating it.** Three of seven are built on a CLI that no longer exists; `linear-issues` instructs agents to call `send_async`/`send_interrupt`/`message`, which **the test suite asserts are absent**, and never mentions `send_message`. That is silently broken inter-agent comms — the highest blast radius in the layer, and it should be fixed independent of this restructure. `handoff` is the template: short, procedural, zero dead claims.
**One open question gates the rest:** we cannot currently tell whether skills are *invoked* at all. A skill nobody loads should be cut, not consolidated. Answer before touching the layer.

**D6 — Move agent-behavior rules into the durable layer.** Extend the safety section to researcher and QA (P1). Publish the four false-RED classes — ENOSPC, the machine-wide `golangci-lint` lock, `Not logged in`, the `TempDir` cleanup race — which today exist **only in weave's private memory** where no child can see them. Every agent that runs `validate` needs them; I lost two merges and `prism` lost a commit to classes we already knew about. Fix the two one-line researcher-prompt bugs (relative findings path, missing `# Environment` block).

## 6. Sequencing

`CLAUDE.md` is contended — `vault` is folding rules in and `tower` has an addition parked. **Nothing here touches it yet.** Order: D6's two one-liners and D5's `linear-issues` fix are independent and safe now → D4 (largest, no conflicts, unblocks D3) → D3 → D1/D2 last, once the contended edits have landed, as a single rewrite rather than incremental edits.

## 7. What I would do differently

Run the enumeration grep **first**, before any per-claim reading. It found 25% more defects than careful hand-verification and cost minutes. Three of five researchers reached for it only after I relayed it mid-flight; had it been in the original brief the audit would have been faster and more complete.

**Counts in this document are measured at `3d92e2c` and are not invariants** — which is, self-referentially, the finding.
