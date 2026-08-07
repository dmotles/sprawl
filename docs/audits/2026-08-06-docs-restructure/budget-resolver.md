# The always-loaded budget resolver

`scripts/always-loaded-budget.sh` — the fifth derivation of this repo's
always-loaded instruction count, and the first one that can fail.

The four before it were `768 → 963 → 1040 → (211 → 170)`, each revision found
by someone checking what the previous figure had assumed, each moving the same
direction. The last happened inside a document arguing for line-count
discipline, written by the agent enforcing it, from a sense of how long the file
felt. That is not four mistakes; it is one artifact type failing four times — a
figure asserted in prose, in front of the thing it describes, with nothing that
fails when it is wrong.

## First: the definition. The number is a reading taken through it.

Five careful derivations have produced five values — `768 → 963 → 1040 → 170 →
810`. When five competent measurements of one quantity fail to converge, the
likeliest explanation is not five measurement errors: **the quantity was
underspecified.** "Always-loaded budget" did different work in each derivation.
So the durable artifact here is the definition; 810 is what you get by reading
through it, and a sixth derivation obeying it is *forced* to reproduce 810.

**Definition — the in-tree always-loaded budget is the sum, in lines, over every
INJECTION in the resolved set.**

1. **What counts as loaded: injection by the harness, and nothing else.** A file
   is in the set iff the harness places its contents in the agent's context
   without the agent choosing to open it. The ground truth is a live agent's
   injected `# claudeMd` context header, recorded in
   `scripts/testdata/always-loaded-manifest.observed`.
   **A prose instruction to read a file does NOT make it loaded.** This is
   observed, not argued: `DESCRIPTION.md` is absent from all three collected
   headers even though `CLAUDE.md:3` says "Read `DESCRIPTION.md`". A manifest
   reports in both directions — it lists what loads *and* omits what does not —
   so that absence is evidence, not silence. Such a file is reported as a
   **violation** with its conditional cost (`[+195 lines if obeyed]`), never
   added to the total, because it loads for compliant agents and not for others
   and a single number cannot describe both states.
2. **What counts as a copy: injections, not distinct files.** If the same bytes
   are placed in context twice from two paths, that is **two** injections and
   costs twice. `CLAUDE.local.md` is injected from the repo root *and* from the
   copy `worktree.setup` drops into every agent worktree; both appear in every
   worktree agent's header. Counting distinct files reports 789 and hid this
   doubling from the entire audit. The same rule governs `@`-imports: a file
   reached by two import paths is counted for each.
   Corollary, also measured, also asymmetric: **`CLAUDE.md` resolves to the
   nearest ancestor only; `CLAUDE.local.md` accumulates across ancestors.**
3. **What the unit is: lines** — `awk 'END{print NR}'`, which unlike `wc -l`
   does not drop a final line lacking a trailing newline. Lines is chosen
   because it is the unit the audit's target was stated in, and because it is
   the only unit every prior derivation shares, so the five are comparable at
   all. It is a **poor** unit on its merits: it moves under reflow without
   anything that matters changing, and it is not what the model pays for. The
   report therefore prints **lines, words and chars per file**, and if the
   ceiling ever gets gamed by reformatting the enforced metric should move to
   chars — not the ceiling upward. Tokens would be the honest unit and are not
   available without a tokenizer dependency; that is a known gap, not an
   oversight.
4. **What the transitive set is: `@`-imports, followed to any depth**, each
   occurrence its own injection, cycles terminated without double-counting,
   and an unresolvable import is a **hard failure**, never a skip. Today the
   repo has **zero** `@`-imports, which the report states explicitly as
   `@-imports resolved: 0` rather than omitting an empty section — a blank
   section and a broken walker look identical.
5. **Out-of-tree files are reported and excluded** from the enforced total.
   They load and they count against the agent, but they are not ours to edit,
   so a gate over them would be unactionable. Excluding them silently would be
   measuring the wrong set again, so they are printed.

### The four priors, explained as definitions rather than as errors

| figure | what it actually measured | which clause it read differently |
|---:|---|---|
| **768** | `CLAUDE.md` alone | (2) — omitted both `CLAUDE.local.md` injections entirely |
| **963** | `768 + 195` | (1) — counted the prose-mandated `DESCRIPTION.md` as loaded |
| **1040** | `768 + 195 + 21 + 21 + 28` (arithmetic: 1033) | (1) **and** (5) — prose read counted, out-of-tree folded in |
| **170** | the *proposed* draft `CLAUDE.md` body | a different **tree** — a cut that has not landed; not a measurement of this one |
| **810** | this definition | — |

Every one of those is a defensible number attached to an unstated set. None is
an arithmetic error. That is the finding: the disagreement was never about
counting, it was about scope, and nothing in the artifact type made scope
visible. **Do not tune the tree to hit any of them, including the ≤250 target**
— that target was set on a sound criterion (preload only what an agent can
violate before it would think to look it up) but stated in a unit nobody had
pinned, so it is a ruler five people read differently.

## The measured total, today

```
ALWAYS-LOADED: FAIL in_tree=810 ceiling=250 violations=1 injections=3
```

| lines | words | chars | injection |
|---:|---:|---:|---|
| 768 | 10,577 | 79,984 | `<worktree>/CLAUDE.md` |
| 21 | 118 | 1,009 | `<worktree>/CLAUDE.local.md` |
| 21 | 118 | 1,009 | `CLAUDE.local.md` (repo root) |
| **810** | | | **in-tree total, 3 injections, 0 `@`-imports** |

Out-of-tree, reported and **not** enforced:

| lines | file |
|---:|---|
| 28 | `~/.claude/CLAUDE.md` |
| 11 | `~/.claude/projects/-home-coder-sprawl/memory/MEMORY.md` |

### Does 810 agree with 212 or 1040? No, and here is the arithmetic

- **212** was never a measurement of *this* tree. It is the *proposed* figure —
  the 170-line draft `CLAUDE.md` plus the doubly-injected workspace config —
  for a cut that has not landed. Against the tree as it stands it is off by
  ~4×.
- **1040** measured a **different set**: `768 + 195 (DESCRIPTION.md) + 21 + 21 +
  28 (user-global) = 1033`, i.e. it folded in the out-of-tree file *and* the
  conditionally-read `DESCRIPTION.md`. Those are both real costs, but neither
  belongs in an *enforceable in-tree* budget: one we cannot edit, and the other
  only loads for agents that obey a prose instruction.
- **810** is in-tree, injections-not-files, `DESCRIPTION.md` excluded from the
  total and reported instead as a violation with its conditional cost
  (`[+195 lines if obeyed]`) attached.

Per the brief: where the script disagrees with the humans, the script is more
likely right — four for four so far. But the disagreements above are mostly
*scope* disagreements, not arithmetic ones, and that is the more interesting
finding. Every earlier figure was a defensible number attached to an unstated
set. The script's contribution is less the digits than the fact that the set is
now written down in code and the manifest fixture.

### The doubling, and why it was the point

`CLAUDE.local.md` is injected **twice** — once from the repo root, once from the
copy `.sprawl/config.yaml`'s `worktree.setup` hook drops into every agent
worktree. Both are byte-identical; both load. A resolver that counted distinct
*files* would report 789 and be wrong in the direction that looks conservative.
That is the single requirement most of the test suite is aimed at (see M12b
below).

## The injection model, and how it is falsifiable

Two filenames, **two different resolution rules**:

> `CLAUDE.md` resolves to the **nearest ancestor only**.
> `CLAUDE.local.md` **accumulates across ancestors**.

This is measured, not modelled. It comes from the injected `# claudeMd` context
headers of three live agents, read out of their own contexts: `weave` (cwd
`/home/coder/sprawl`, no worktree), `forge` (manager, in a worktree), and `hex`
(this agent, in a worktree).

| file | root copy | worktree copy |
|---|---|---|
| `CLAUDE.md` | absent (present for weave, which has no worktree) | present |
| `CLAUDE.local.md` | present | present |

weave's header is what discriminates: it lists the root `CLAUDE.md`, which kills
the reading that root `CLAUDE.md` is skipped for some intrinsic reason. Same
directory, same ancestry walk, two files, opposite treatment.

Getting this wrong **over-counts `CLAUDE.md` for worktree agents**, and it fails
in the direction that looks conservative — the worst direction, because a
too-high budget reads as caution rather than as error.

The model is a claim about a harness version we do not control, so the durable
artifact is not the model but the tripwire:
`scripts/testdata/always-loaded-manifest.observed`. It fires when the injected
set changes **shape**, whatever the reason. If it fires, confirm against a live
agent's context header *first*, then re-record.

It records **two perspectives**, `[worktree]` and `[root]`, selected by where
`--root` points. Recording only one — as the first version did — makes the other
**false-fail**, and the printed "re-record" remedy would then break the first: a
ping-pong between two correct readings, after which the next operator
reasonably concludes the tripwire is noise. A perspective with no recorded
section is a **failure**, not an unchecked pass. And the check never disengages
silently: measuring a foreign tree, or passing `--no-manifest`, prints
`manifest check: SKIPPED` with the reason, because a tripwire that prints
nothing is indistinguishable from one that passed.

Two anomalies recorded rather than chased:

- **`~/.claude/CLAUDE.md` loads despite `.claude/settings.json` listing it in
  `claudeMdExcludes`** — three independent observations. The script prints a
  note. The starting sentence for whoever picks it up: *the exclusion is
  configured and the file loads regardless.*
- **`MEMORY.md` is injected for every agent, weave included** — but only the
  index; its linked satellites (`persistent_knowledge.md`, the
  `feedback_*`/`project_*` files) are **not** injected, and store A's
  `persistent.md` is absent from all three manifests. It measured **11 lines**
  at the run above. It was reported to me as 7, and this document said 7 in
  three places until the tool's own output contradicted it — a file that grows
  between the sentence and the reading, which is the entire argument for taking
  the number from the tool. **Do not quote a line count from this paragraph;
  run the tool.**
  It is negligible as budget and still worth annotating, with the derivation
  rule attached because a headline without its rule is the same defect one level
  up: a QA pass measured **29%** of *this index's* standing present-tense claims
  false — 23% across both stores counting wrong-scope claims as failures, ~8%
  counting only outright-false ones. It earns the annotation by being an
  **index**: its errors select what gets read next, and one falsified line
  labels a plan "active, unimplemented" whose work has shipped. That figure is
  **deliberately not printed by the tool** — a measured-elsewhere statistic on
  every run is unfalsifiable by the thing printing it.

## What it deliberately does not detect

**Prose read-instructions are not detected by understanding them.** Detecting
"this sentence tells an agent to go read that file" is natural-language
classification, and this repo already built and rejected a deterministic prose
parser for a structurally identical problem — it acquired four blind spots of
the same class it was built to detect, one blinding 462 lines across five
harnesses while every aggregate counter stayed byte-identical
(`/testing-practices` § "The non-asserting fallback"). It was not rebuilt.

Instead the **construction is banned** and the ban is enforced lexically:

> An always-loaded file may `@`-import another file, or point at on-demand
> material (a skill, a `docs/` page). It may not mandate a read of a file it
> does not import.

Two token-matching legs, both narrow, neither parsing meaning:

- **(a) mention** — a git-tracked `.md` named in backticks and not `@`-imported.
  Deliberately *broader* than the rule: it fires on pointers too, and
  `scripts/always-loaded-budget.allow` is where a human writes down "this is a
  pointer, not a mandate". Over-firing costs one allowlist line; under-firing
  ships an instruction surface that some agents obey and others do not. Of the
  distinct backticked tokens in today's `CLAUDE.md` — 753 at commit `5410c39`,
  recompute with `grep -oE '\`[^\`]+\`' CLAUDE.md | sort -u | wc -l` — exactly
  three name a tracked `.md`, so the allowlist is three entries.

  > This sentence originally said **561**, a figure I carried over from a
  > subagent's report without recomputing it. That is the fifth instance today
  > of a plausible number asserted in prose, in a document arguing against
  > exactly that — and it happened inside the deliverable. Left visible, with
  > the recomputation command attached, because the correction is the point:
  > the three-entry allowlist is checked by the tool on every run; the token
  > count is not, and so it is the sentence that was free to be wrong.
- **(b) mandate** — an imperative read verb (`read`/`re-read`/`consult`)
  **immediately** followed by a backticked git-tracked path of any extension.
  The adjacency is load-bearing: `CLAUDE.md:554` contains the word "read" and an
  unrelated backticked tracked path far apart on the same line, and a
  line-scoped rule fires on it. Skill pointers (`` `/testing-practices` ``) need
  no special case — they are not tracked paths.

Explicitly still undetected, and this is the honest limit: a read mandated
without backticks; a file named indirectly; anything the recorded manifest does
not capture about the harness. The comment above the regex says not to
"improve" this into the rejected parser; if you are tempted, add an allowlist
entry instead.

**Also not invariant:** `wc -l`-style counts move under reflow without anything
that matters changing (appendix §E made this point). The report prints lines,
words and chars per file; **lines** is enforced because it is the audit's
vocabulary. If the ceiling starts getting gamed by reformatting, switch the
enforced metric to chars rather than raising the ceiling.

## The live violation

```
<worktree>/CLAUDE.md:3 names a tracked .md it does not @-import:
  `DESCRIPTION.md` [+195 lines if obeyed]
```

(Both legs match this line; they are deduplicated per `(file:line, target)`
because it is one problem to fix, not two.)

`CLAUDE.md` line 3 says "Read `DESCRIPTION.md` for project context" in prose,
with no `@`-import. So 195 lines load for compliant agents and not for others,
and nothing downstream distinguishes those states. Confirmed from the other
direction too: `DESCRIPTION.md` is **absent** from all three agents' context
headers, so the instruction genuinely does not cause a load. A manifest reports
in both directions, which is why that absence is evidence rather than silence.

Fixing it is a one-line change in `CLAUDE.md` — `@`-import it (and pay 195 lines
of budget) or downgrade it to a pointer. **This change is not permitted to make
it**: `CLAUDE.md` is contended by another writer.

## Why bash, not Go

The repo's meta-checks of exactly this class are shell —
`scripts/test-race-gate.sh`, `scripts/test-wirelog-helpers-unit.sh`,
`scripts/test-e2e-matrix-unit.sh`. A Go tool would need a package, a `cmd`
entry, and a build to run at all, for a job that is file-walking and line
counting. The one thing Go would buy — real unit tests — the bash suite gets
from fixtures for less.

## Wiring, and the one thing that is deliberately not in `validate`

| target | in `validate`? | why |
|---|---|---|
| `make test-always-loaded-budget-unit` | **yes** | fixture-only, ~6s, reads nothing from the real tree, so an unrelated `CLAUDE.md` edit can never fail it |
| `make always-loaded-budget` | **no** | it fails on the tree **today** (810 vs 250, plus the violation) |

Wiring a known-failing gate into `validate` teaches people to bypass `validate`.
The split is the same one `test-race-gate` uses: the mechanism's guard runs
every time, the gate itself waits.

**Precondition for promoting it into `validate`:** once the `CLAUDE.md` cut
lands and the `DESCRIPTION.md` read is resolved, add `always-loaded-budget` to
the `validate` dependency list. Naming that here so it does not become a
permanently-manual gate nobody runs.

## The expected-files precondition (absence is trivially true)

A check that a thing is *absent* passes trivially against a target that has been
renamed, moved or deleted — non-asserting-fallback class, and the same defect
weave closed in `scripts/test-e2e-matrix-unit.sh`'s `[15p]`. A budget resolver
has the same leg: rename `CLAUDE.local.md` away and a naive resolver reports a
**smaller total and exits green**, which reads identically to a real pass.

So the recorded manifest is not opt-in. `scripts/testdata/always-loaded-manifest.observed`
is **defaulted in whenever the script is measuring the repo it was recorded
against** (the sprawl root of `--root` equals the sprawl root of the script
itself); a foreign tree never gets it, because the manifest says nothing about
another tree's injection set. `--no-manifest` opts out explicitly. Every
recorded entry must be derived or the run fails — a missing expected file is a
**failure, not a smaller budget**.

**Negative control, watched.** A replica of the real layout (repo root + nested
worktree, real content), with the worktree's `CLAUDE.local.md` renamed to
`.bak`:

```
--- baseline (all expected files present) ---
  in-tree total: 810 lines across 3 injections
  derived injection set matches the recorded manifest
ALWAYS-LOADED: FAIL in_tree=810 ...  (exit 1 — over ceiling, as expected)

--- RENAMED AWAY: worktree CLAUDE.local.md -> CLAUDE.local.md.bak ---
  in-tree total: 789 lines across 2 injections
  recorded but NOT derived (renamed, moved or deleted — absence is trivially
  true, so this is a FAILURE, not a smaller budget): <worktree>/CLAUDE.local.md
ALWAYS-LOADED: FAIL in_tree=789 ...  actual exit=1
```

The total did drop to 789 — that is the reading a resolver without the
precondition would have published. The manifest is what turns it red.

## Failure modes the script refuses to have

- **Zero resolutions is a failure, not a total.** If it resolves no in-tree
  injections it prints `resolved 0 in-tree injections … refusing to report a
  budget the instrument could not have measured` and exits 1. A confident
  number produced by an instrument blind to its target is the entire family of
  errors behind this task.
- **`@-imports resolved: N` is always printed**, including `N=0`. The real repo
  has **zero** `@`-imports today, so the whole transitive walker could be
  inert and nothing in production output would show it. A blank section and a
  broken walker look identical; a printed `0` and a broken walker do not.
- **An unresolvable import fails**, it does not skip.
- **A skip exits 77, never 0** (git absent from PATH). A skip asserts nothing.
- **Usage/config errors exit 2** — missing conf, no `CEILING=`, non-integer
  ceiling, missing allowlist, unknown flag, `--root` that isn't a git repo.
  Running against a non-repo is *your* error, not the environment's, so it is 2
  rather than 77.
- **Line counting uses `awk 'END{print NR}'`, not `wc -l`**, which drops a final
  line with no trailing newline.

## Red-first evidence

94 assertions, in 10 blocks with **per-block** minima (a single grand-total
floor lets one block die at its first line while the others carry the run over
it). Every assertion has been watched failing under a deliberate mutation of the
resolver; the table records the mutation and what it printed. `scripts/` was
copied to a tempdir per mutation, so the real tree was never mutated.

| # | mutation | what failed (verbatim) |
|---|---|---|
| **M12b** | dedup injections by basename — *the audit's actual error: distinct files, not injections* | `CLAUDE.local.md accumulates at EVERY level: want '45', got '35'` · `byte-identical CLAUDE.local.md copies are NOT deduplicated: want '20', got '15'` · `four in-tree injection sites resolved: want '4', got '2'` (8 failures) |
| M1 | dedup injections by realpath | `a diamond counts the shared import once per path: want '24', got '14'` — *note this does **not** break the `CLAUDE.local.md` doubling, since the two copies have different realpaths; M12b is the control that does* |
| M2 | `CLAUDE.md` counted at every ancestor (nearest-only `break` dropped) | `ancestor CLAUDE.md files are NOT counted when a nearer one exists: want '45', got '1544'` (4 failures) |
| M30 | `CLAUDE.md` not resolved at all | `want '45', got '15'` · `four in-tree injection sites resolved: want '4', got '3'` (6 failures) |
| M13 | `find`-based sweep including descendants | `a descendant CLAUDE.md is not loaded and not counted: want '45', got '545'` · `the descendant CLAUDE.md is absent from the breakdown: output unexpectedly contained 'docs/CLAUDE.md'` |
| M32 | unreferenced `.md` files in the tree swept in | `an unreferenced .md in the tree is not swept in: output unexpectedly contained 'other.md'` |
| M3 | discovery floor removed | `an empty repo must NOT exit 0 (blind instrument): want '1', got '0'` · `empty repo names the zero-resolution: output did not contain 'resolved 0 in-tree injections'` |
| M7 | `wc -l` instead of `awk` | `a final line without a trailing newline still counts: want '40', got '39'` |
| M14 | imports one level deep (no recursion) | `transitive A->B->C counts all three: want '30', got '20'` · `the diamond resolves four imports, not three: output did not contain '@-imports resolved: 4'` (4 failures) |
| M4 | unresolvable `@`-import no longer fails | `a dangling @-import fails rather than skipping: want '1', got '0'` |
| M5 | leg (b) verb regex made unmatchable | `an imperative read of a tracked NON-.md path fires: want '1', got '0'` |
| M6 | leg (a) mention scan skipped | `a bare mention of a tracked .md fires (fail-toward-red): want '1', got '0'` |
| M15 | `is_tracked` filter dropped | `an untracked target does not fire: want '0', got '1'` · `a skill pointer does not fire: want '0', got '1'` |
| M16 | leg (b) loses adjacency (line-scoped) | `a distant backticked tracked path on a line containing 'read' does not fire: want '0', got '1'` |
| M23 | `@`-imported files no longer exempt | `the same read instruction is silent once the file IS @-imported: want '0', got '1'` |
| M24 | allowlist ignored | `an allowlisted mention is silent: want '0', got '1'` |
| M21 | per-(file,line,target) dedup removed | `one target reported once, even though both legs match it: want '1', got '2'` |
| M22 | violation omits `file:line` | `no single line contained both 'CLAUDE.md:3' and 'docs/x.md'` |
| M10 | ceiling boundary `-ge` instead of `-gt` | `total == ceiling is OK (<=, not <): want '0', got '1'` |
| M26 | largest contributor = first injection, not biggest | `no single line contained both 'largest contributor' and 'big.md'` |
| M20 | per-file breakdown removed, totals only | `a passing run emits a per-file line for CLAUDE.md with its count: no single line contained both 'CLAUDE.md' and '28'` |
| M8 | out-of-tree folded into the enforced total | `a 10000-line out-of-tree file does not change the enforced verdict: want '0', got '1'` |
| M27 / M27b | out-of-tree file / memory index not reported | `output did not contain '…/CLAUDE.md'` · `output did not contain 'MEMORY.md'` |
| M29 | `CLAUDE_CONFIG_DIR` ignored, always real `$HOME/.claude` | `no out-of-tree fallback to the developer's real $HOME: output unexpectedly contained '/home/coder/.claude'` |
| M17 | verdict line emitted on stderr | every `verdict_field` assertion returned empty |
| M18 | usage errors exit 1 instead of 2 | all 8 usage assertions: `want '2', got '1'` |
| M19 | missing git exits 0 instead of 77 | `git absent from PATH: want exit 77, got '0'` |
| M9 | manifest mismatch never fails | `a manifest entry the resolver does not derive fails: want '1', got '0'` (2 failures) |
| M28 | manifest mismatch reports no detail | `no single line contained both 'derived but NOT recorded' and '<worktree>/CLAUDE.local.md'` |
| M31 | re-record instruction removed | `the mismatch tells you to re-record the manifest: output did not contain 're-record'` |

### Defects found after it was green

Eleven more came from a code-review sub-agent (`link`) reading the committed
diff, and three from turning the same questions on my own work. The ones that
mattered are all **one class** — a check that stops checking without saying so —
which is the class the tool exists to catch, found inside the tool:

- **A `git ls-files` failure silently voided the entire read-instruction
  check.** `… || : >"$TRACKED"` swallowed the error; an empty tracked list makes
  `is_tracked` always false, so **both** legs became structurally unable to fire
  and the run reported green. Reproduced against a corrupted index: healthy
  `violations=1`, corrupted `ALWAYS-LOADED: OK … violations=0 RC=0`. Now a hard
  refusal. Watched by reverting it (M33): `a git ls-files failure refuses the
  run instead of voiding the read check: want '1', got '0'`.
- **The manifest false-failed from the main checkout** — weave's own
  perspective, and observation #1 in the manifest's own header. Both shapes are
  correct and observed; only one was recorded. Fixed with `[worktree]`/`[root]`
  sections; an unrecorded perspective now fails rather than passing unchecked.
  Watched (M36).
- **A deleted manifest silently disabled the tripwire** — `[ -f … ]` guarded the
  default, so removing the file turned the precondition off and the run looked
  clean. Now `exit 2` with a re-record instruction.
- **The manifest disengaged with no output** when `--root` was a foreign tree.
  Silence read as a pass. Now prints `manifest check: SKIPPED` with the reason
  (M35).
- **The unit suite was reading the real allowlist.** `run_budget` passed no
  `--allow`, so adding a path to `scripts/always-loaded-budget.allow` could
  quietly stop a fixture violation firing while the suite stayed green, making
  the header's "reads NOTHING from the real tree" claim false. Fixed with an
  empty fixture allowlist, pinned by an assertion that a path allowlisted *in
  the real tree* still fires against a fixture — and that control itself needed
  a setup assertion, since it decays the moment someone edits the real entry
  out. Watched: `a path allowlisted in the REAL tree still fires against a
  fixture: want '1', got '0'`.
- **The allowlist match was a regex built from a path** (`${tok//./\.}`), so a
  metacharacter in a filename would silently widen it. Now a literal field
  comparison.
- **`@~/…` and `@/abs/…` imports were counted in the enforced total**,
  contradicting the out-of-tree exclusion; a 50-line `$HOME/global-rules.md`
  inflated the in-tree figure by 50. Now classified out-of-tree (M34).
- **The tool printed the 29% memory-accuracy figure on every run** — a
  measured-elsewhere statistic, unfalsifiable by the thing printing it, i.e.
  the artifact type the tool replaces, inside the tool. Moved to this document.
- Smaller: `--help` emitted four lines of function source; `--root ""` fell back
  instead of erroring; a redundant `sort -u`; a comment recording that the
  import token grammar is whitespace-delimited by design.

Three of the above were self-found by asking the reviewer's own questions first:

- **A deleted manifest silently disabled the tripwire.** The default-manifest
  logic engaged only `[ -f "$DEFAULT_MANIFEST" ]`, so removing the recorded file
  turned the precondition off and the run looked clean. Now a missing manifest
  is `exit 2` with a re-record instruction; opting out is `--no-manifest` or not
  at all. Watched: `error: the recorded manifest '…' is missing; re-record it
  from a live agent's context header, or pass --no-manifest … / actual exit=2`.
- **The allowlist match was a regex built from a path** (`${tok//./\.}`), so a
  metacharacter in a filename would have silently widened it. Replaced with a
  literal field comparison — a too-wide allowlist entry is a check that stops
  checking.
- **The unit suite was reading the real allowlist.** `run_budget` passed no
  `--allow`, so every fixture inherited `scripts/always-loaded-budget.allow`:
  adding a path to it in the tree could quietly stop a fixture violation firing
  while the suite stayed green, and the file header's "reads NOTHING from the
  real tree" claim was false. Fixed by injecting an empty fixture allowlist, and
  pinned by a new assertion that a path allowlisted *in the real tree* still
  fires against a fixture. Watched, by reverting the fix: `a path allowlisted in
  the REAL tree still fires against a fixture: want '1', got '0'`.

Two of the mutation results are worth reading as findings rather than as
bookkeeping:

- **M1 vs M12b.** Deduplicating by realpath breaks only the diamond; the two
  `CLAUDE.local.md` copies survive it, because they *are* different paths. The
  assertion that actually pins the doubling needed a basename/content dedup to
  fail against. Had the suite shipped with only M1 as its control, the headline
  requirement would have looked tested and not been.
- **M28 caught a vacuous assertion in the suite itself.** The original
  `the missing-entry failure names the un-recorded injection` was a whole-output
  substring match, and the per-file breakdown above it already names every
  injection — so it passed with the mismatch detail entirely absent. It is now
  an `assert_line_both`. This is the *tests* falling to the same class the tool
  exists to catch: a check whose subject is not the thing it appears to
  constrain.

**The floor was watched firing twice.** During development, on a real
miscount: `FAIL: block 'imports' recorded 14 assertions, expected at least 15 —
it died early and this run measured less than it claims`. And deliberately, with
every assertion block deleted from a copy — the `0 passed / 0 failed` case this
repo has shipped green before:

```
  FAIL: block 'ceiling' recorded 0 assertions, expected at least 10 — it died early and this run measured less than it claims
  FAIL: block 'usage' recorded 0 assertions, expected at least 7 — ...
  FAIL: block 'report' recorded 0 assertions, expected at least 9 — ...
  FAIL: block 'manifest' recorded 0 assertions, expected at least 8 — ...
0-assertion run actual exit=1
```

Per-block minima, not one grand total: a single sum lets one block die at its
first line while the others carry the run over the floor.

**The `validate` wiring was confirmed by expansion, not by reading the
Makefile** — the lesson `scripts/test-race-gate.sh` exists to teach. `make -n
validate` line 33 is exactly `bash scripts/test-always-loaded-budget.sh`, with
no env-var prefix, and the live `always-loaded-budget` target is correctly
absent.

## Running it

```bash
make always-loaded-budget            # the live gate (exits 1 today, correctly)
make test-always-loaded-budget-unit  # the fixture suite (in `make validate`)

bash scripts/always-loaded-budget.sh --root . --ceiling 900   # ad-hoc
```

Files: `scripts/always-loaded-budget.sh`, `.conf` (the ceiling), `.allow` (the
mention allowlist), `scripts/testdata/always-loaded-manifest.observed` (the
tripwire), `scripts/test-always-loaded-budget.sh` (94 assertions).
