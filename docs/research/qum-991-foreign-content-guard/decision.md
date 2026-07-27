# QUM-991 — Foreign-content guard: reproduction, validation, and decision

**Author:** `probe` (researcher, spawned by `bastion`)
**Date:** 2026-07-27
**Status:** decision ready for `weave` to approve or reject
**Scope:** the mechanism decision, the hook-phase decision, the escape-hatch policy.
Implementation is deliberately NOT done here — `scripts/pre-commit` and the shared
`.git/hooks` dir are fleet infrastructure owned by the root agent.

Harnesses that produced every number below are committed alongside this doc:

| harness | required env | what it establishes |
| -- | -- | -- |
| `repro-binary-blindness.sh` | `GUARD=` | AC-1: the four-row binary-blindness table |
| `repro-hook-coverage.sh` | `SCRIPTS=` | AC-4: the three-hook coverage table + reference-transaction feasibility |
| `repro-design-probes.sh` | `REPO_ROOT=` | `git add -f`, already-tracked paths, `.gitattributes`, allow-list breadth |

All three create their scratch root with `mktemp -d /tmp/qum991-*.XXXXXX` and delete it
only behind a literal-prefix `case` guard (the `_unit_reset_markers` pattern). No `rm`
globs. Host: git 2.34.1, `zip` **absent**, `python3` `zipfile` present.

> **Read `repro-binary-blindness.sh`'s exit-code header before wiring it into
> anything.** It exits 0 on a successful *run*, not a successful *verdict* — its
> `ASSERT FAIL` rows are the expected finding, so it must **not** go into
> `make validate` as-is (green would mean "the gap is still open"). Gating it
> requires inverting the assertions first, so that a future guard actually fixing
> binary blindness turns the rows red. The other two are conventional: the
> hook-coverage harness asserts documented behaviour (0 = all 18 held); the design
> probes only measure and always exit 0.

### Why this lives in `docs/research/` and not in `.sprawl/agents/probe/findings/`

Originally written to the agent findings path per the researcher default, and it had
to be force-added there (`.gitignore:28` `.sprawl/*`) — which was the signal that the
location was wrong rather than that the `-f` needed justifying. `weave` has since
established, while auditing 94 branches, that `findings/` is **sensitive-by-default**
and is being added to `.gitignore` for exactly that reason. **A tracked deliverable
that has to fight the ignore file to land belongs somewhere deliberate.**
`docs/research/m13-phase1-evidence/` is the precedent for tracked research artifacts,
so this sits beside it and is tracked normally, with **no `-f`**.

The content is safe to track on a **public** repo, verified rather than assumed:
synthetic terms only (`ACMEGLOBALCORP` and a fake all-digits GUID), no real
subscription / tenant / resource-group / storage-account / employer values, and the
only real paths cited (`deploy/hub/infra/terraform/azure/…`) are already publicly
tracked. `scripts/guard-employer-leak --all` exits 0 over this worktree against the
real terms list — confirmed independently by `bastion`.

---

## 1. AC-1 — Binary blindness: independently reproduced, table CONFIRMED in all four rows

Run: `GUARD=$PWD/scripts/guard-employer-leak bash repro-binary-blindness.sh`

Synthetic terms list via `$SPRAWL_FORBIDDEN_TERMS_FILE` (no real term appears in this
doc or in the harness):

```
synthetic-employer:ci:ACMEGLOBALCORP
synthetic-subscription:exact:11111111-2222-3333-4444-555555555555
```

| row | fixture (verified to exist + be non-empty before reading the verdict) | `git diff --cached` shape | guard exit | verdict |
| -- | -- | -- | -- | -- |
| (a) **positive control** | `apply.log`, 70 b, `file(1)`=ASCII text, term on line 2 | `3 0 apply.log`; **3** `+` lines | **1** | **BLOCKED** ✓ (`apply.log:2: synthetic-subscription`) |
| (b) | `tfplan.zip`, 217 b, `file(1)`=Zip archive, ZIP_DEFLATED | `- - tfplan.zip`; **0** `+` lines | **0** | **PASSED — not caught** |
| (c) | `tfplan.bin`, 2118 b, 2 NULs asserted present, both terms asserted present as **literal plaintext bytes** (`grep -a`) | `- - tfplan.bin`; **0** `+` lines | **0** | **PASSED — not caught** |
| (d) | both binaries committed, whole-tree `--all` | n/a (`git grep -I`) | **0** | **PASSED — not caught** |
| (d-control) | `notes.txt` with the term, committed, `--all` re-run | n/a | **1** | **BLOCKED** ✓ |

**The issue's table is correct in every row. No corrections.**

Two controls make the negative rows meaningful rather than vacuous:

* **Row (a)** is the positive control. It blocks, so the list is loaded, the mode is
  right, and the harness works.
* **Row (d-control)** is new — I added it because row (d) alone cannot distinguish
  "`--all` is binary-blind" from "`--all` is not wired up / the list wasn't read in
  whole-tree mode". It blocks on a committed *text* term, so `--all` demonstrably
  works and its failure on row (d) is specifically the `-I` binary skip.
* Every row asserts, before reading a verdict, that the fixture **exists, is
  non-empty, and is actually in the index** (`git diff --cached --name-only | grep -qxF`).
  This is the trap QUM-991 warns about: a fixture that failed to materialise makes
  `git add` fail and the guard trivially "pass", which reads exactly like the finding.

**Row (c) is the load-bearing row.** The terms are sitting in the file as literal
uncompressed bytes and the guard still passes. This is not a compression problem and
cannot be fixed by decompressing, by adding terms, or by extending the term-match
logic. The mechanism is git's own binary classification: for a binary blob git emits
`Binary files /dev/null and b/tfplan.bin differ` and **zero** `+` lines, so
`guard-employer-leak`'s `"+"*` parser (`:131-137`) has nothing to read.

---

## 2. AC-4 — Three-hook coverage table: independently validated

Run: `SCRIPTS=$PWD/scripts bash repro-hook-coverage.sh` → **18 ok, 0 FAIL, 18 assertions.**

Each row below is a measured commit in a scratch repo, not a reading of the source.

| guard | keys on | fires on the wrong-branch payload? | `--no-verify`? | fires on **this** (foreign/binary content)? |
| -- | -- | -- | -- | -- |
| `guard-main-commit` (pre-commit) | `git symbolic-ref` HEAD vs `$1` + `$SPRAWL_AGENT_IDENTITY` | **yes** — rc=1, message cites QUM-808 | **SKIPPED** — measured: `--no-verify` commit to `main` as `probe` landed, rc=0 | **no** — measured content-blind: a NUL binary on a feature branch passes rc=0 |
| `guard-main-ref` (reference-transaction) | stdin ref lines, literal `refs/heads/$2`, `prepared` phase only | **yes** — rc=**128**, `fatal: ref updates aborted by hook`, `main` did not advance | **NOT skipped** — measured: blocks *with* `--no-verify` | **no** — measured content-blind: binary passes rc=0 |
| `guard-employer-leak` (pre-commit) | staged **content**, by term | no (content-only) | **SKIPPED** — measured: `--no-verify` landed the text leak, rc=0 | text **yes** · **binary no** (§1) |

Identity semantics confirmed for both ref guards: `weave` allowed, unset/empty
(human) allowed, other non-empty blocked. `guard-employer-leak` with an absent list
is a no-op exit 0.

**The issue's coverage table is also correct.** Confirmed independently, not repeated.

### 2.1 Surprise: bypass-proofness is relative, not absolute (this changes the phase decision)

Measured in the same harness (§5):

```
git -c core.hooksPath=/tmp/.../emptyhooks commit -m z   →  rc=0, BOTH hooks disabled
```

`core.hooksPath` disables `pre-commit` **and** `reference-transaction` alike. So
`reference-transaction`'s advantage is resistance to exactly **one** bypass verb
(`--no-verify`) — not to `core.hooksPath`, not to editing the symlink, not to
`git update-ref`, and not to the hooks never having been installed. This is QUM-951's
territory and it materially weakens axis 3's steer. See §4.

### 2.2 New finding, out of scope, worth its own issue: a *rejected* `reset --hard` on `main` still clobbers the working tree

`guard-main-ref` protects the **ref**. It does not protect the **tree**. Measured in
`repro-design-probes.sh` §6d, plus a standalone re-verification.

> **Do not confuse this with `repro-hook-coverage.sh` §2b, which prints
> `staged=[] status=[]`.** That row resets to `HEAD` — a deliberate **no-op** reset,
> chosen so it isolates "is the ref update rejected?" from any tree movement. §6d
> resets to `HEAD~1` and is the row that measures the clobber. Both scripts now carry
> a comment saying so, because the apparent contradiction cost a reviewer real time.

```
before:  main=a711a6f (c2), f.txt=v2
SPRAWL_AGENT_IDENTITY=probe git reset --hard HEAD~1   →  rc=128, guard message printed
after:   main=a711a6f (c2)  ← ref correctly NOT moved
         f.txt=v1           ← working tree WAS rewound
         status: "M  f.txt"
```

git applies `reset --hard`'s index+worktree effects **before** the ref transaction, so
the guard's rejection leaves the ref intact and the tree rewound and staged. Untracked
files survived; **uncommitted modifications to tracked files would not.** This
*validates by measurement* CLAUDE.md's existing warning ("do NOT have an agent run
`git reset --hard` on `main` — that can clobber weave's uncommitted state") and shows
the guard is not a defence against it — the warning is load-bearing prose, not a
belt-and-braces note. Reported separately to `bastion`; not actioned here.

---

## 3. Mechanism decision

### 3.1 Path allow-list — **REJECTED**, and this resolves the central design risk with a measurement

QUM-991 flags too-narrow/too-broad as the central design risk and demands a judgement.
The judgement is that the allow-list is decoration **for this repo specifically**, and
the proof is where the incident artifacts actually lived:

* `git ls-files | awk -F/ '{print $1}' | sort -u` → **33** distinct tracked top-level
  entries over **945** tracked files (19 directories + 14 root files). An allow-list
  enumerating them *is* the whole repo.
* Decisively: **`deploy/hub/infra/terraform/azure/` contains 15 tracked files** (34
  under `deploy/hub/infra/terraform/`). Two of the three QUM-989 artifacts —
  `apply4.log` and `tfplan5`, **including the 57 KB zip, the most sensitive one** —
  lived in **exactly that directory**. Any honest allow-list must permit that prefix,
  so an allow-list would have caught **1 of 3** artifacts (only root-level
  `acrbuild2.log`, and only because root files are allow-listed by name).

A mechanism that permits the directory the incident happened in is not a control. This
is not a tuning problem: making it catch `tfplan5` requires denying a prefix that
contains 15 legitimately-tracked files, i.e. converting it into a deny-list.

### 3.2 Binary/size heuristic — **ADOPTED as the load-bearing rule**, and the false-refusal cost is measured at zero

This is the only option that generalises to §1's finding instead of enumerating
around it. I priced its false-refusal risk against the repo's real history rather
than guessing, and the result is unusually clean:

```
commits on main: 754
commits in all history that ADDED a git-binary-classified file: 2
  → assets/banner.jpg
  → web/src/wire/useLiveTail.ts
tracked binary files today: 1 (assets/banner.jpg; the other 'binary' hit is a .gitkeep)
```

**The second one is the interesting one.** `web/src/wire/useLiveTail.ts` is a 1474-byte
TypeScript source file that git classified as binary because it contained **2 NUL
bytes**, used as a cache-key separator inside a template literal:

```
const key = target ? `${target.hostId}\x00${target.runId}\x00${target.sessionId}` : "";
```

It was fixed one commit later by `495eaa8 fix(web): strip NUL bytes from
useLiveTail.ts (QUM-910 follow-up)`.

So the binary rule's **only two historical firings** would have been `banner.jpg` (a
one-line allow-file entry, permanently) and a **genuine true positive that needed a
follow-up commit to fix anyway**. Measured false-refusal rate on this repo's actual
history: **0 in 754 commits.** That converts the heuristic from "speculative, needs an
allow path" into "measured, essentially free."

Note also that `guard-employer-leak` was blind to `useLiveTail.ts` at the moment it was
added, for the same structural reason as row (c). The undefended class is not
hypothetical; it has already occurred once in this repo, benignly.

**Signal to key on:** `git diff --cached --numstat --diff-filter=A` reporting `-\t-`.
Measured to work in both the index (`--cached`) and the commit tree (`diff-tree`, §4).

**Caveat, measured (probe 6c):** a committed `.gitattributes` line `*.bin diff` forces
text treatment and the numstat signal flips from `- - blob.bin` to `5 0 blob.bin`, so
the binary rule can be defeated by a tracked `.gitattributes` entry. This is
**not a silent hole**: the very same attribute makes `guard-employer-leak` start seeing
the file's content (measured: `+` content lines went 0 → 4). The bypass trades one
guard for the other. Accept the numstat signal, record the caveat; a direct NUL probe
(`git cat-file blob <sha> | head -c 8000`) is the more robust alternative if weave
prefers, at the cost of one subprocess per added file.

**Size threshold: DECLINE.** The issue offers ">N KB" as part of this option. I
recommend against it. Legitimate large *text* files exist (generated code, docs,
`go.sum`), so any threshold that catches a 57 KB plan also catches those, and it buys
nothing over the binary rule for the demonstrated payload. Adding it converts a
zero-false-refusal rule into a noisy one.

### 3.3 Provenance deny-list — **ADOPTED, but explicitly as the secondary rule**, and it is the part weave may cut

`*.log`, `tfplan*`, `*.tfplan`, `plan.out`, `*.pem`, `*.pfx`, `*.p12`, `*.env`,
`*.azcreds`, `*.tfstate*`, `*.retry`. Content-agnostic, so it catches binaries by name.
~15 lines of the script.

Honest accounting of its marginal value, because it is smaller than it looks:

* Post-QUM-989 the gitignore patch already covers these classes **by name**, so for
  the *exact* incident the deny-list is largely redundant with item 1 of the parent.
* Its non-redundant coverage is narrow: `git add -f` (which defeats gitignore but not
  the index — measured, §3.5) and a name gitignore didn't anticipate.
* It fails open on unforeseen classes by construction. Note the actual artifact was
  named **`tfplan5`** — no extension. `*.tfplan` misses it; only `tfplan*` catches it.
  That is the deny-list's whole weakness in one filename.
* Measured collateral: exactly **one** tracked file matches these globs today —
  `docs/research/m13-phase1-evidence/ec6-live-handoff-stderr.log`. So committing a
  `.log` deliberately into `docs/research/` is established practice here and needs an
  allow-file entry.

**Recommended split — this is the yes/no weave actually has to make:**

* **v1 (recommended): the binary rule alone.** ~40 lines of core logic. It is the part
  that closes the measured gap and the part nothing else covers.
* **v1+ (cheap add-on): the deny-list in the same script.** ~15 more lines. Worth it
  for `add -f` and unforeseen names; not worth a separate change.

### 3.4 Already-tracked paths — key on `--diff-filter=A`, and pin the resulting hole as a decision

Measured (probe 6b), modifying an already-tracked `already.log`:

```
--diff-filter=A  (adds only)  → []            ← not seen
--diff-filter=AM (adds+mods)  → [already.log] ← seen
```

**Decision: `A` only.** The threat model is *a foreign file appearing*, which is always
an add. `AM` would re-flag every legitimate edit to an already-accepted file forever —
and would have blocked the original add of `ec6-live-handoff-stderr.log` and then every
subsequent edit to it.

**The resulting hole, stated rather than hidden:** appending leaked content to an
already-tracked forbidden-class file is not caught by this guard. For **text** that is
covered by `guard-employer-leak` (it is an added line). For **binary** it is covered by
nothing. This is the correct division of labour, but it must be written down, and the
test suite must assert the pass so the hole is a pinned decision and not an accident
someone later "fixes" into a noise generator.

### 3.5 `git add -f` — measured: no interaction, and this is the layering story

Probe 6a:

```
git add apply4.log      → refused by .gitignore, staged=[]
git add -f apply4.log   → staged=[apply4.log], --diff-filter=A=[apply4.log]
```

`-f` overrides `.gitignore` and **nothing else**. The index shape is byte-identical to
a plain add, so an index-side guard fires on `add -f` exactly as on a plain add. No
special handling needed — and this is precisely why the guard is additive rather than
redundant to QUM-989's gitignore patch: **gitignore stops the accident, `add -f`
defeats gitignore, the guard still fires.**

---

## 4. Hook-phase decision: `pre-commit` — and I am declining the issue's steer, with reasons

QUM-991 steers toward `reference-transaction` per axis 3. **I recommend `pre-commit`
anyway.** Flagging this as an explicit disagreement so weave can overrule it knowingly.

### Feasibility is not the problem — I proved it works

Harness §4 installs a probe `reference-transaction` hook and commits two files
(one text, one NUL binary). In the `prepared` phase:

```
PHASE=prepared ref=HEAD              old=5c5c79f new=30d9c87
PHASE=prepared ref=refs/heads/main   old=5c5c79f new=30d9c87
  new-object-exists=yes type=commit
  names=text.txt tfplan.bin
  numstat=1 0 text.txt; - - tfplan.bin;
  cached-names=text.txt tfplan.bin
```

So: the commit object **is** already written and readable, `git diff-tree` gives the
paths, and `--numstat` gives the same `-\t-` binary signal. A tree-side content guard
is entirely implementable.

**One correction to the issue's framing:** it says a reference-transaction hook "sees
the commit tree not the index". Measured, for a plain `git commit` **both** are visible
— `cached-names` was still fully populated. The issue's *caution* is right but its
*reason* is imprecise, and the imprecision matters: `--cached` appears to work, so an
implementer would reach for it, and it would then be silently wrong for `reset`,
`merge`, `rebase`, `cherry-pick` and `fetch`, where the index has nothing to do with
the ref being updated. **Use `diff-tree`; the trap is that `--cached` looks fine in
testing.**

### Why `pre-commit` wins anyway — five reasons, strongest last

1. **Fires on every ref update.** A single commit already produces 2 transaction lines
   (`HEAD` + `refs/heads/main`). A `git fetch` produces one per remote ref; a rebase or
   cherry-pick of 20 commits produces 20 transactions. `guard-main-ref` absorbs this
   because it does cheap string matching; a content guard would run `diff-tree` per ref
   update. It needs `guard-main-ref`'s full phase-and-ref discipline plus new
   ref-namespace filtering that `guard-main-ref` never needed.
2. **Edge cases the index-side version simply does not have:** root commits need
   `--root` (`old` is all-zeros — visible in my probe), and merges have two parents so
   "the added files" is ambiguous by construction.
3. **New hook type ⇒ new install wiring.** `.sprawl/config.yaml`'s `worktree.setup`
   plus `make hooks` both need a third `ln -sf`. A guard invoked *from*
   `scripts/pre-commit` needs zero install wiring — one line at `scripts/pre-commit:17`.
4. **Worse remediation UX.** A pre-commit refusal leaves a fixable index and our
   message on stderr. A reference-transaction refusal surfaces as
   `fatal: ref updates aborted by hook` (rc=128) with our message interleaved among
   ref lines, after the commit object is already written — recoverable, but the agent
   reading it is measurably more likely to reach for a bigger hammer.
5. **The decisive one: bypass-proofness is illusory here.** §2.1 measured that
   `core.hooksPath` disables *both* hook types. So all that expense buys resistance to
   one bypass verb. And the axis-3 concern is misattributed: QUM-836 made `--no-verify`
   routine because a **hook bug** made it necessary. That is a bug-class problem, and
   the phase is not its fix. The real fixes are (a) never ship a hook that makes
   `--no-verify` necessary, and (b) **detect that the hooks are installed and firing** —
   QUM-951's territory. A hook-liveness assertion is worth strictly more than a phase
   change, because it covers `core.hooksPath`, missing symlinks, *and* `--no-verify`
   habituation, none of which a phase change covers more than partially.

### STATED KNOWN LIMITATION (required by AC-3, and it is not boilerplate)

> **This guard is skippable.** It is a `pre-commit`-phase guard. Measured: it does not
> run under `git commit --no-verify`, and it does not run under
> `git -c core.hooksPath=<dir> commit` (the latter also disables `guard-main-commit`
> **and** `guard-main-ref`). It is a control against an **accidental add**, not against
> a **deliberate or habituated bypass**. If `--no-verify` ever becomes routine again
> (the QUM-836 class), this guard silently stops existing **and nothing will say so** —
> that silence, not the skippability, is the real residual. Closing it is a
> hook-liveness check (QUM-951), not a phase change.

### Option B, priced but not recommended for v1

Same script invoked from **both** phases — `pre-commit` for UX, plus a
`reference-transaction prepared` pass restricted to `refs/heads/*` using `diff-tree`.
Cost: +~50 lines of phase/ref/root/merge discipline, +2 install sites, + the per-ref
cost on fetches. Recommend deferring until a bypass is actually observed; the
observation is what the QUM-951-style liveness check would give you.

---

## 5. Escape-hatch policy: two hatches, both logged, and the reasoning is counter-intuitive

QUM-991 requires a hatch, warns that an agent under pressure will reach for it, and
prefers one that is logged. The key insight is stronger than "prefer logged":

> **The hatch's real job is to be more attractive than `--no-verify`.** A guard with no
> hatch does not get respected; it gets bypassed — and the bypass an agent reaches for
> is `--no-verify`, which *also* disables `guard-employer-leak` and
> `guard-main-commit`. A narrow, logged hatch is therefore a **safety feature for the
> other two guards**. Refusing to provide one makes the whole stack weaker.

**Hatch 1 (the normal path): a tracked, reviewed allow-file — `scripts/foreign-allow`.**

* Format: one exact path per line (**no globs** — a glob is how an allow-file becomes an
  allow-everything), `path  # reason`, `#` comments, blank lines skipped.
* **Tracked, not gitignored** — deliberately unlike `forbidden-terms`. It contains no
  secrets, and being tracked is the whole point: using the hatch is a visible change in
  the diff weave reviews, with an author, a reason, and a permanent git-blame record.
  **The hatch is itself a reviewable artifact.** That is a better audit trail than any
  log file.
* Because the rules key on `--diff-filter=A`, an entry is needed only for the single
  commit that adds the file.
* Measured seed content — both entries are required today, not speculative:
  ```
  assets/banner.jpg                                              # README banner (QUM-991)
  docs/research/m13-phase1-evidence/ec6-live-handoff-stderr.log  # captured evidence log
  ```
* **No env-var form.** This follows `guard-main-commit:12-17`'s established convention
  (the protected value is a positional arg "specifically so the environment cannot
  retarget it"). An env-var allow-list is exactly the reflexive reach we are trying to
  make awkward.

**Hatch 2 (the emergency path): `SPRAWL_FOREIGN_GUARD_OVERRIDE="<reason>"`.**

* **Mandatory non-empty reason.** An empty or unset value does **not** override — it
  must be typed, which is the friction. A bare boolean flag would not be.
* Appends one audit line to `.sprawl/hygiene/foreign-override.log` (already gitignored
  by `.gitignore:28` `.sprawl/*`, so **no `.gitignore` change is needed** — verified)
  with ISO timestamp, `$SPRAWL_AGENT_IDENTITY`, branch, the overridden paths, and the
  reason. Greppable, attributable, auditable by weave.
* Prints a loud multi-line warning on stderr naming the log path, so the agent knows it
  was recorded.
* Refuses to override if the log cannot be written — **the override is contingent on
  being logged.** An unloggable override is not an override. This is the one line that
  makes "prefer one that is logged" enforceable rather than aspirational.

**Refusal message — required wording.** Must say *flag them, do not tidy*, and must
contain **no** deletion command. Deleting destroys both possibly-in-flight work and the
forensic evidence: the nanosecond-identical mtimes that identified the QUM-989
mechanism existed only because the finder left the files alone (and the artifacts were
subsequently deleted on instruction, which is why AC-1 had to be re-established
synthetically). Shape:

```
ERROR: refusing to commit foreign/unreviewed content (QUM-991 guard).

These newly-added paths look like they did not come from this repo's work:
  deploy/hub/infra/terraform/azure/tfplan5   (binary blob, 56876 b)
  acrbuild2.log                              (matches forbidden class *.log)

DO NOT DELETE THEM. If you did not create these files, leave them exactly where
they are and report them to your parent agent — deleting destroys both
possibly-in-flight work and the forensic evidence needed to find the writer.

If they are not yours:      unstage only, keep the files:  git restore --staged <path>
If they are yours but should not be tracked:  unstage, then add a .gitignore rule.
If they legitimately belong in the tree:      add an entry to scripts/foreign-allow
                                              with a one-line reason (reviewed change).
Emergency:  SPRAWL_FOREIGN_GUARD_OVERRIDE="<why>" — logged to
            .sprawl/hygiene/foreign-override.log and reviewed.
```

Note `git restore --staged` / `git rm --cached` unstage **without** deleting; the
message must name one of them and must never name `rm`.

---

## 6. Layering: which failure each layer covers, and the residual after all of them

Per QUM-991: the behavioural mitigation is **not** recorded as a control.

| layer | kind | covers | defeated by |
| -- | -- | -- | -- |
| `git add -A` ban | **behavioural — NOT a control** | accidental bulk staging (the actual near-miss vector) | any agent forgetting, once; context pressure; an agent that never read the rule |
| QUM-989 gitignore patch | structural, name-keyed | accidental staging of *known-named* classes | `git add -f` (measured); unforeseen names (`tfplan5`) |
| `guard-employer-leak` | structural, content-keyed | **text** carrying a listed term, added or whole-tree | **binaries (measured §1)**; `--no-verify`; `core.hooksPath`; a foreign file with no listed term |
| **NEW foreign-content guard** | structural, provenance+shape-keyed | a **newly-added** binary, or a newly-added forbidden-class path — regardless of content, regardless of gitignore, regardless of `add -f` | `--no-verify`; `core.hooksPath`; a `.gitattributes diff` line; content appended to an already-tracked file |
| `guard-main-commit` / `guard-main-ref` | structural, ref-keyed | the wrong-branch failure, completely | content-blind by design (measured) — **irrelevant to this payload** |

Structural covers *the ban being violated or bypassed*; behavioural covers *the
accident*. Neither subsumes the other.

### Residual risk after ALL layers — stated plainly

1. **A newly-added *text* file that is not in a forbidden class, contains no listed
   term, and is not gitignored passes everything.** A foreign README, a foreign
   lockfile, a stack trace with an unlisted hostname. **Unaddressed by design** —
   "this file is not mine" is genuinely not expressible as either a term list or a
   path list, and §3.1 shows the only mechanism that could express it (an allow-list)
   collapses into decoration on a 33-top-level-entry repo.
2. **Every bypass.** `--no-verify`, `core.hooksPath` (measured to kill all three
   guards), an uninstalled/edited hook symlink, raw `git update-ref`. The dangerous
   part is the silence, not the bypass.
3. **Content appended to an already-tracked forbidden-class file** (`filter=A` by
   design, §3.4) — covered by `guard-employer-leak` for text, by nothing for binary.
4. **No retroactive detection, at all.** Row (d) measured that `--all` is binary-blind,
   and this guard is add-time only. So if a binary leak lands by any of routes 1–3,
   **nothing in the repo or in `make validate` will ever tell you.** This is the most
   under-appreciated residual and it is a separate follow-up: dropping `-I` from
   `guard-employer-leak --all` and NUL-handling the field split (the `:155-156` comment
   is candid that NULs are exactly why `-I` is there). Out of scope for QUM-991, but I
   have now measured the row, so it should be filed.
5. **A `.gitattributes` line** flipping a binary to text defeats the binary rule —
   while simultaneously exposing the file to `guard-employer-leak` (§3.2). Traded, not
   silent.

---

## 7. Implementation price — enough for a yes/no without further analysis

### Recommendation: **BUILD IT — v1 = binary rule + deny-list, `pre-commit` phase.**

Justification in one line: it closes the one measured undefended class on a **public**
repo at a measured false-refusal cost of **0 in 754 commits**, and the near-miss was
real, prospective, and involved a 57 KB zip that no current layer sees.

If weave wants the minimum: **the binary rule alone is the load-bearing 40 lines** —
the deny-list is the part QUM-989's gitignore patch mostly already covers.

"Don't build it" would be defensible only if the binary gap were theoretical. It is
not: it has already occurred benignly once (`useLiveTail.ts`, §3.2) and nearly occurred
sensitively once (`tfplan5`).

### Files changed

| file | change | ~lines |
| -- | -- | -- |
| `scripts/guard-foreign-content` | **new**. House-style header comment; allow-file resolution reusing `guard-employer-leak`'s `--git-common-dir` + shell absolutization (incl. the `--path-format=absolute` fail-open caveat at `:54-57`); `git -c core.quotePath=false diff --cached --name-only -z --diff-filter=A`; NUL-delimited read; per path → deny-glob match, else numstat `-\t-`; allow-file skip; override handling + audit write; refusal message | **~150** (of which ~40 core logic, ~15 deny-list, ~25 override/audit, ~45 header+message) |
| `scripts/foreign-allow` | **new**, tracked. Header + the 2 measured entries | ~10 |
| `scripts/pre-commit` | +1 invocation after `guard-employer-leak` (`:16`), before the `GIT_*` unset (`:24`) + comment | **+3** |
| `scripts/test-guard-foreign-content.sh` | **new**, pure shell, style of `scripts/test-wirelog-helpers-unit.sh` (318 lines is the right size reference, not the 1847-line matrix suite) | **~300** |
| `Makefile` | `.PHONY` entry, recipe, add to `validate` deps (`:4`) | **+3** |
| `CLAUDE.md` | new subsection under "Commit guard": what it keys on, the allow-file, the override + its log, the stated skippability limitation | ~18 |
| `.gitignore` | **none needed** — `.sprawl/*` at `:28` already covers the override log (verified) | 0 |

**Total ≈ 480–500 lines, ~60% of it test.** One engineer session including TDD.

### What the shell test asserts

Wired into `make validate` per QUM-991's testing expectations ("a regression test
guarding a false-green is worthless if it only runs when someone remembers"). Per
QUM-953 every assertion needs a demonstrated failure mode; the control is named for
each. Assertion-count floor **≥25** so a `0 passed / 0 failed` run exits non-zero.

| # | assertion | how it is demonstrated to CAN-fail |
| -- | -- | -- |
| 1 | root `acrbuild2.log` added → refused, exit **1**, message names the path | negative control: identical commit without the file → exit 0 |
| 2 | `deploy/hub/infra/terraform/azure/tfplan5` (**no extension**, NUL binary) → refused, **and the message attributes it to the binary rule** | mutation: disable the binary rule → assert it now passes. Pins the fix to §1's finding; stops the two rules covering for each other's absence |
| 3 | zip with a forbidden term named `data.dat` (**matches no deny glob**) → refused by the binary rule — **and `guard-employer-leak` PASSES the same fixture in the same test** | the leak-guard pass is the control; it pins the exact gap this guard exists to close, so if `guard-employer-leak` is later fixed the redundancy becomes visible instead of silent |
| 4 | legitimate multi-dir commit (`internal/foo.go` + `scripts/bar` + `CLAUDE.md`) → exit 0 (AC-5) | mutation: rename one path to `x.log` → assert it flips to refuse |
| 5 | **absent allow-file ⇒ still ENFORCES** (not a no-op) | deliberate asymmetry vs `guard-employer-leak`, whose absent *terms* list means "nothing to hide"; an absent *allow* list means "nothing is allowed" — the fail-closed direction. Asserted explicitly because it is the one deviation from the reference implementation and a future reader will "fix" it the wrong way |
| 6 | malformed allow-file line ⇒ fails **broader-refusal** (grants no allowance) | control: a garbage line must not allow-list everything; assert a still-forbidden path is still refused with the garbage line present |
| 7 | `git add -f` of a gitignored `*.log` → refused | measured identical index shape (§3.5); control is the plain-add row |
| 8 | already-tracked `*.log` **modified** → **passes** | pins the deliberate `filter=A` hole (§3.4) as a decision, not an accident |
| 9 | path with a space, and a non-ASCII path → refused correctly | negative control: re-run with `core.quotePath=true` set in config and assert the verdict is unchanged (the `-z`/quotePath trap) |
| 10 | override with a reason → passes **and** the log gained exactly one line containing identity + reason + path; override with an **empty** reason → still refuses; override with an **unwritable** log dir → still refuses | three separate rows; the empty-reason and unwritable rows are the controls that make "logged" enforceable |
| 11 | refusal message contains the do-not-delete wording and does **not** contain `rm ` | text assertion so the do-no-harm wording cannot silently regress |
| 12 | exit code is **1**, never 2 (2 is reserved for usage, per `guard-employer-leak:48`) | control: bad argv → assert 2 |

### Follow-ups this investigation produced (not actioned)

1. **`guard-main-ref` does not protect the working tree from a rejected `reset --hard`**
   (§2.2, measured). Candidate issue.
2. **`guard-employer-leak --all` is binary-blind, so nothing detects an already-landed
   binary leak** (row (d) + residual 4). Candidate issue: drop `-I`, NUL-handle the
   field split.
3. **A hook-liveness check** — assert the three hooks are installed and firing —
   is worth more than any phase change (§4 reason 5, §2.1). Fits QUM-951.

---

## 8. Reflections

**Surprising.** (a) The single "binary file" ever added to `web/` was a *TypeScript
source file* with two deliberate NUL bytes as a cache-key separator, fixed one commit
later — so the binary heuristic's only non-`banner.jpg` historical firing would have
been a **true positive**, and `guard-employer-leak` was blind to it at the time. That
one commit turned the cost side of the mechanism decision from speculation into
measurement, and it is the most load-bearing fact in this document. (b)
`core.hooksPath` disables `reference-transaction` just as thoroughly as `pre-commit`,
which inverted the phase decision I expected to reach and made me decline the issue's
own steer. (c) `git diff --cached` *is* still populated in the `prepared` phase, so the
issue's caution about the index is right for the wrong reason — and the wrong reason is
the more dangerous shape, because `--cached` will pass an implementer's tests and then
be silently wrong for `reset`/`merge`/`fetch`. (d) A rejected `reset --hard` on `main`
still rewinds the working tree.

**Open questions.** Whether the `--diff-filter=A` hole (§3.4) is acceptable long-term
for *binary* files — I decided it is, but I did not measure how often a tracked binary
gets modified here (n=1 tracked binary, so the sample cannot answer it). Whether
`assets/banner.jpg` should be allow-file'd or handled by a `assets/` exemption — I
chose the allow-file for auditability, but with n=1 either is defensible. Whether the
QUM-989 writer's identity (parent's item 4) would change the mechanism choice: if the
writer turns out to be an in-repo tool rather than an external copy, a provenance
signal better than "is it binary" might exist, and I could not investigate it in scope.

**What I would do next with more time.** (1) Measure the false-refusal rate of the
deny-list against all 754 commits by replaying `--diff-filter=A` name lists through the
proposed globs — I measured the *binary* rule against full history but only spot-checked
the deny-list against the current tree, which is the weaker of my two cost numbers.
(2) Prototype the `guard-employer-leak --all` binary fix (residual 4) to see whether
NUL-handling the `-z` field split is genuinely awkward or just untried, since that is
the only layer that could ever catch a leak *after* it lands. (3) Write the hook-liveness
check, because §2.1 convinced me it dominates the phase question I spent the most time on.
