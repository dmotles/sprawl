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
| 7 | `~/.claude/projects/-home-coder-sprawl/memory/MEMORY.md` |

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
`scripts/testdata/always-loaded-manifest.observed`, checked with
`--check-manifest` (wired into `make always-loaded-budget`). It fires when the
injected set changes **shape**, whatever the reason. If it fires, confirm
against a live agent's context header *first*, then re-record.

Two anomalies recorded rather than chased:

- **`~/.claude/CLAUDE.md` loads despite `.claude/settings.json` listing it in
  `claudeMdExcludes`** — three independent observations. The script prints a
  note. The starting sentence for whoever picks it up: *the exclusion is
  configured and the file loads regardless.*
- **`MEMORY.md` is injected for every agent, weave included** — but only the
  7-line index; its linked satellites (`persistent_knowledge.md`, the
  `feedback_*`/`project_*` files) are **not** injected, and store A's
  `persistent.md` is absent from all three manifests. Seven lines is negligible
  as budget and is annotated anyway, with the derivation rule attached: a QA
  pass measured **29%** of *this index's* standing present-tense claims false —
  23% across both stores counting wrong-scope claims as failures, ~8% counting
  only outright-false ones. It deserves the annotation because it is an
  **index**: its errors select what gets read next. One falsified line labels a
  plan "active, unimplemented" whose work has shipped.

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
  ships an instruction surface that some agents obey and others do not. Over the
  561 distinct backticked tokens in today's `CLAUDE.md`, exactly three tracked
  `.md` files are named, so the allowlist is three entries.
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
<worktree>/CLAUDE.md:3 mandates a read of a tracked path it does not @-import:
  `DESCRIPTION.md` [+195 lines if obeyed]
```

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

82 assertions, in 10 blocks with **per-block** minima (a single grand-total
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

Two of these are worth reading as findings rather than as bookkeeping:

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
tripwire), `scripts/test-always-loaded-budget.sh` (82 assertions).
