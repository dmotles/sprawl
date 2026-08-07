# D1 — the three blocking preconditions for the `CLAUDE.md` cut

Built on `dmotles/d1-preconditions`. `CLAUDE.md` is **untouched** — this branch
only creates the destinations its breadcrumbs will need.

Scope note: the draft's appendix §C lists three blocking preconditions. All three
are built. **A fourth exists and §C does not list it** — see *Routing and
correctness objections* §1. It is a build break, not a lossy cut, so it should be
treated as harder-blocking than the three.

---

## 1. What moved where

### `/git-recovery` — new skill

`.claude/skills/git-recovery/SKILL.md`, with the required pointer stub at
`.agents/skills/git-recovery/SKILL.md`.

Carries, in this order:

- **The three rules every procedure shares** — pin before you move; `--soft`
  never `--hard`; never `reset --hard` on `main`.
- **The merge-engine un-commit recovery** (rescue refs). The mechanism (the
  engine soft-resets the agent's branch to the merge base before it knows the
  squash will succeed, and nothing undoes the reset if the squash commit fails),
  the do-not-clean warning, the read-only diagnose sequence, the
  `refs/sprawl/rescue/<agent>/<ISO8601>/<slug>` pin, the `--soft` move, and the
  two namespace rules (ISO timestamp in the name; never hand-write under
  `refs/sprawl/premerge/`, whose entire value is the by-construction inference
  that a non-empty listing means the tool ran). Includes the binary-level
  predicate for whether the hazard is live.

  **A claim corrected during review, worth recording as an instance of the
  class.** My first draft carried over, in the present tense, that *"`sprawl gc`
  ages refs by the timestamp in the name, never by commit date, so a name it
  cannot parse is never pruned."* Nothing in the tree does that: no landed Go
  code references `refs/sprawl` at all, and `sprawl gc` reaps orphan agent
  directories and stale session logs by mtime. The sentence described a
  *proposed* design as current behaviour — a claim of presence, which is the
  class the draft's own "Documentation you write" rule bans. The skill now states
  the namespace is unimplemented, and marks the timestamp convention as
  forward-looking rather than mechanical.
- **The wrong-tree-commit-on-`main` procedure** — cherry-pick to re-home, then
  `reset --mixed` on `main`, root agent only. See the correction below: neither
  `--soft` nor `update-ref` is right here.
- **The squash-merge downstream recovery (QUM-1083)** — the precondition, *both
  natural checks lie in opposite directions* (`git branch --contains`
  under-reports; a rebase that succeeds proves nothing, with git's two
  drop-a-replayed-commit messages named), prevent-don't-recover, and all four
  steps including the QUM-1085 delta comparison and the after-committing parent
  check.
- **A pointer** to `/testing-practices` for the generalised epistemic rule.

**One correction, not a move — and it took two attempts, which is the more useful
record.** `CLAUDE.md`'s wrong-tree-commit procedure offers
`git reset --soft <prior-good-sha>`, with `git update-ref` as an equivalent
alternative, then tells you to confirm `git status` is clean. **Those instructions
contradict each other**, and the skill now says so.

My first fix was also wrong, in a way worth writing down. I claimed `update-ref`
"moves the ref alone and leaves `status` clean" — reasoning that it does not touch
the index, which is true, and concluding status would be clean, which does not
follow. `status` compares the index to `HEAD`, and in the main checkout `HEAD` *is*
`main`, so moving the ref moves one side of that comparison. Both `--soft` and
`update-ref` therefore leave the stray commit's whole tree reported as **staged**,
byte-identically, and the next `commit` in that checkout silently re-lands it.
Verified in a throwaway repo, in both directions.

The corrected procedure uses **`reset --mixed`**: it moves the ref *and* resets the
index, leaving the working tree untouched, so the stray content reappears as
ordinary uncommitted work — which is the right resting state, since that is what it
was before someone committed it by mistake. The skill states plainly that no
command makes `status` clean here (the only route to clean is discarding content,
i.e. `--hard`, which is forbidden), notes that `--mixed` also unstages any
legitimate staged work, and confirms the outcome with a containment check rather
than a status check. Anyone diffing the skill against `CLAUDE.md` will see the
divergence; it is deliberate.

The class: I had a true premise about the command and drew a conclusion about a
*different* relation than the one the command participates in — the same shape as
the rule this branch moves into `/testing-practices`, committed while writing the
document that carries it. Caught by a reviewer who ran the command instead of
reasoning about it.

Two further edits made while moving, deliberately:

- `CLAUDE.md`'s step-3 aside *"never sweep a stray in with `git add -A` — see
  below"* resolved to a `CLAUDE.md` section that is **not** moving. Rewritten as
  an inline rule so the skill is self-contained.
- The reference to `prism`'s `/false-red` skill was **removed**. See objections §3.

### `/testing-practices` — addition

Two new `###` subsections inside `## Assertion Rigor`, plus one sentence appended
to the rejected-parser subsection.

- **`### Name the property before you name the probe`** — write the sentence you
  intend to publish, in behavioural terms, before choosing the search; if the
  sentence's subject and the search's subject are different nouns the search
  cannot settle the sentence. Includes *prefer the property over the countable
  proxy* (with the companion-test convention as the worked instance) and *a
  negative control that shares the probe's defect is not a control — only a
  positive one discriminates.*
- **`### Check that the question the command answers is the question you are
  claiming`** — the asymmetric-relation hazard, generalised past git, with
  `git merge-base --is-ancestor`'s argument order as the worked instance, and the
  point that *running the command more carefully does not catch it, because the
  command is already correct and its result already true.* Plus the sibling
  failure: a check that is necessary and not sufficient, reported as if it settled
  the claim — a true result about content where the wiring is what matters.
- **The standing prohibition on rebuilding the fallback-detector parser.** The
  skill documented the rejected parser and its blind spots thoroughly but never
  stated the resulting rule.

Placement: immediately after `### A null result is a statement about your search`,
because the three sections then read as one arc — an instrument that could not
see, then an instrument aimed at the wrong thing. This differs from the oracle's
suggested siting next to `### Necessary but not sufficient`; the new text names
that section explicitly and distinguishes itself from it, so the adjacency is not
needed. Both sites satisfy every constraint in
`cmd/docs_assertion_convention_test.go`.

What was **not** added, because the skill already has it in richer form: the
demonstrate-it-can-fail rule and the record-what-it-printed requirement; the
assertion-count floor and the empty-run rule; the parent-commit control and its
bound; both spellings of the non-asserting fallback and all its corollaries; the
rejected parser's blind spots; the selection effect. `CLAUDE.md`'s copy of all of
this is a compression of the skill, and a compressed copy is not a safer copy.

### `/e2e-testing-sandboxing` — tmux socket guidance

- **Two `DO NOT` bullets.** Never bare `tmux kill-server`; never bare `tmux` at
  all in sandbox or harness context. The `kill-server` bullet states the bound
  precisely: the ban is on the **unscoped** form, because socket-scoped
  `tmux -L "$SPRAWL_TMUX_SOCKET" kill-server` is what `sprawl_sandbox_destroy`
  itself runs and is correct. Followed by the asymmetry that makes it matter —
  production sessions still share the default socket.
- **`SPRAWL_TMUX_SOCKET` added to the Setup env-var table**, which omitted it even
  though the setup script exports it and prints it in its own banner.
- **`_stmux` documented in the "It also installs" list**, quoting its definition,
  and naming the fallback that matters: with the variable unset it degrades to
  bare `tmux` **silently**, so an unset socket is not safe, it is undetected.
- **Four bare-`tmux` examples corrected to `_stmux`** — the session query under
  *Inspecting State*, the pane-size pin, and the respawn-window trick. The
  Inspecting-State one was a live defect, not a style fix: `tmux list-sessions |
  grep "$SPRAWL_NAMESPACE"` queries the default server, so it could never have
  found a sandbox session.
- **The narrower sanctioned teardown** `_stmux kill-session -t "$SPRAWL_NAMESPACE"`
  added under Cleanup, for clearing the session while keeping `$SPRAWL_ROOT`.
- **The `kill-session` example corrected too.** `CLAUDE.md` offers
  `_stmux kill-session -t $SPRAWL_NAMESPACE` as the sanctioned narrow teardown.
  The namespace names the **socket**, not the session — each script mints its own
  session name — so that command errors with `session not found`. The skill now
  gives socket-scoped `_stmux kill-server` as the narrow form, and tells you to
  look the session name up rather than guess it. This is also the *second* defect
  in the Inspecting-State query: the grep on `$SPRAWL_NAMESPACE` could not have
  matched even on the right socket.
- **Both skill descriptions extended** — the `.claude` copy and the `.agents`
  pointer stub — with the tmux trigger condition, so the content is reachable by
  someone whose question is "is this tmux command safe" rather than "how do I set
  up a sandbox". Only the `name` field is test-enforced, so the stub's description
  is easy to leave stale.

---

## 2. Duplication window

Content that now exists in **both** `CLAUDE.md` and a skill. The cut must remove
every entry below; a move that leaves both copies is how the current state arose.

| `CLAUDE.md` section | now also in | cut |
|---|---|---|
| `### Recovering a downstream branch after a squash-merge (QUM-1083)` | `/git-recovery` | whole section |
| `### Safe recovery from a wrong-tree commit on \`main\`` | `/git-recovery` | whole section |
| `## tmux safety (QUM-325)` | `/e2e-testing-sandboxing` | whole section |
| The closing paragraph of the QUM-1083 section, *"Check that the question the command answers is the question you are claiming"* | `/testing-practices` (generalised) **and** `/git-recovery` (as the recovery's own step-4 rationale) | no separate action — it is inside the QUM-1083 section already listed above |
| Within `## Code Patterns`: the clause *"Do not rebuild it; the defence is manual review against that checklist"* | `/testing-practices` | this clause only. **The rest of that paragraph must stay** — see objections §1 |

Two clauses restated in a skill whose `CLAUDE.md` home is **not** being cut. No
action for the cut; listed so a later reader does not "finish the move" by deleting
the surviving original:

- **`git add -A`.** `/git-recovery`'s squash-merge step 3 carries *"never sweep a
  stray in with `git add -A` — staging is explicit paths only, always."* That
  restates `CLAUDE.md` `### Never git add -A (QUM-989)`, which stays. It is one
  clause replacing a "see below" cross-reference that would have dangled once the
  surrounding section moved, so the restatement is the point.
- **Commit-guard prose.** `/git-recovery`'s closing note that guards block non-root
  agents from landing on `main`, and that sprawl refuses to resume or wake a
  non-root agent whose worktree HEAD is on `main`, paraphrases `## Commit guard`
  and the reference-transaction backstop. Fine as a pointer while those sections
  stand. **If the cut trims them, this skill silently becomes a second home for
  that claim** — decide then whether it should be the only one.

Two things that look like duplication and are not:

- **`/tmp` hygiene and the `rm -rf $SPRAWL_ROOT` incident** were already in both
  the e2e skill and `CLAUDE.md` before this branch. Untouched here; not part of
  this duplication window.
- **"Name the property before you name the probe" and "prefer the property over
  the countable proxy" are not in today's `CLAUDE.md`.** They are new in the
  draft. Putting the long form in `/testing-practices` gives the draft's
  compressed version a home; there is nothing to cut.
- **The bare-`kill-server` prohibition already has a machine check** —
  `scripts/smoke-test-memory.sh` asserts that no non-comment bare
  `tmux kill-server` invocation exists under `scripts/`. So the prohibition
  survives the cut regardless. What was `CLAUDE.md`-only was the *reasoning* and
  the *`_stmux` convention*, which no test enforces. This refines the brief's
  "only place in the repo" framing rather than confirming it — see the control in
  §4.

---

## 3. Routing and correctness objections

### 1. Blocking, and missing from §C: the cut breaks `cmd/docs_assertion_convention_test.go`

`TestClaudeMDStatesAssertionConvention` extracts `CLAUDE.md`'s **`## Code
Patterns`** section by exact heading match and fatals if it is absent, then
requires within it: `can fail`, `negative control`, `mutation`, `red-first`,
`assertion-count floor`, `parent-commit`, `pre-existing`, `0 passed`, `0 failed`,
and a `/testing-practices` ↔ *assertion rigor* pointer proximity-matched inside
one sentence.

The draft body has **no `## Code Patterns` heading**, and is missing
`assertion-count floor`, `parent-commit`, `pre-existing`, `0 passed`, `0 failed`,
and the literal `assertion rigor`. It keeps `can fail`, `negative control`,
`mutation`, `red-first`.

So the draft as written does not merely lose material — **it fails `make validate`
on the missing section alone.** This is by design: the test's comments say the two
documentation homes are deliberately tied so a rename cannot split them, and the
draft's §D reasoning (*"do not summarise — cut or point"*) collides with a test
that requires the summary. The draft's `## Tests and assertions` section is the
natural place to satisfy it, but the heading name is held in a Go constant, so
either the section is renamed `Code Patterns` (ugly, and the draft is right that
the old name is bad) or the constant and the requirement list change with the cut.
**Either way it is a code change in `cmd/`, in the same commit as the cut, and §C
does not mention it.**

I did not fix this: `CLAUDE.md` and `cmd/` assertion-test edits are both outside
my scope, and the test is contended by the same writers.

### 2. The archived-citation instruction is unsafe in the current merge order

I was asked to repoint the e2e skill's `docs/research/qum-458-e2e-leak-analysis.md`
citation at `docs/archive/` with an `(archived)` label. **I did not write the
archive path**, because the target exists only on the docs-restructure branch. On
this branch and on `main` the archive path does not resolve, so writing it ships
exactly the dangling breadcrumb this project exists to remove — and my branch may
land first.

Instead: the root cause the citation was buying is now stated **inline** (the
socket split was correct but increased the leak surface, because each sandbox got
its own daemon nobody sweeps; combined with a missing parent-death contract, every
`kill -9` mid-e2e leaked one deterministically). The live path is kept as further
reading, with a forward note that a restructure is moving it under
`docs/archive/` and to expect an `(archived)` label there.

Net effect: the knowledge no longer depends on a document surviving, the link is
valid today, and repairing the path becomes the restructure's own link-repair pass
rather than a guess made in advance. **Whoever executes the archive sweep should
treat this skill as a known inbound reference.**

Recorded as a disagreement with the instruction, not a silent divergence.

### 3. `/git-recovery` vs `prism`'s `/false-red` — recommendation

They are organised on orthogonal axes and both should exist. `false-red` is
symptom-first, keyed on the literal error text you see on screen;
`git-recovery` is procedure-first. Prism's entry currently carries the full
pin-and-soft-reset recipe, which duplicates the primitive `git-recovery` owns.

**Recommendation: `git-recovery` owns the procedure and the namespace rules.
`false-red` keeps the symptom string, the do-not-clean warning, the mechanism
paragraph, the diagnose triple, and the binary predicate that is its own cut
criterion — then hands off.** I did not edit prism's file.

I also **removed** my initial cross-reference to `/false-red` from `git-recovery`:
that skill exists only in prism's worktree, so naming it from this branch is a
dangling pointer. `git-recovery` is self-contained instead. Once `false-red`
lands, adding the pointer back is a one-line follow-up in both directions.

### 4. Agreed with the draft's routing, with one refinement

The draft routes the QUM-1083 section's *closing epistemic rule* to
`/testing-practices` as a move. Agreed, but it is **dual-homed, not moved**: the
rule is also step 4's justification inside the recovery procedure, where
`--is-ancestor`'s argument order is the worked instance. `/git-recovery` keeps the
short form and points at the skill for the general rule. Deleting it from either
would leave the other unexplained.

---

## 4. Positive controls

Stated because a null result is a statement about the search.

**Claim: the tmux socket guidance existed only in `CLAUDE.md`.**
Probe: `git grep -ln` over the tracked tree for `SPRAWL_TMUX_SOCKET`, `_stmux`,
`kill-server`, `sprawl_sandbox_destroy`.
*Positive control:* the probe returns hits across the sandbox scripts and the
e2e drivers, so it was live, not blind.
Result, at the property level rather than the token level: the scripts *use* the
socket and the wrapper but do not teach them; the research documents narrate one
incident; the e2e skill named `sprawl_sandbox_destroy` but never the socket, never
`_stmux`, and its own session-query example used bare `tmux`, which **cannot
work** — that unworkable example is the strongest evidence the guidance was
absent, stronger than any count. **Refinement:** the *prohibition* is separately
enforced by an assertion in `scripts/smoke-test-memory.sh`, so the claim holds for
the reasoning and the wrapper convention, not for the prohibition. Stated in §2.

**Claim: the draft body fails the assertion-convention test.**
Probe: whitespace- and case-normalised search of the text between the draft's
`BEGIN BODY` / `END BODY` markers for each phrase the test requires.
*Positive control:* a phrase known to be in the body (`Hard rules`) returns 1.
*A recorded miss:* my first pass was a line-based `grep`, which reported
`negative control` absent — it is present, wrapped across a line break. The
normalised re-run corrected it. Same probe-blindness class as everything else in
this report, caught only because the control forced a second look. The remaining
absences survive normalisation.

**Claim: the e2e skill's citation target moved to `docs/archive/`.**
Probe: `find docs -name '*qum-458*'` in the docs-restructure worktree, and the
`(archived)` label rule in that branch's `docs/README.md`.
*Positive control:* the file resolves under `docs/archive/research/` there and
does **not** resolve there on this branch or on `main`.
The divergence *is* the finding. Note that the oracle sidechain, reading only the
un-restructured trees, concluded the citation was "not stale" — a correct
observation about the wrong tree, and the reason objection §2 is phrased as a
merge-order problem rather than a staleness one.

---

## 5. Verification

I was instructed not to invoke builds myself, so the commit's pre-commit hook was
the single execution of the suite against these files. **It ran the full
`make validate` and passed**, including the `cmd` package, which is where every
skill and documentation test lives — so `TestClaudeSkillsHaveCodexCounterparts`
and all of `docs_assertion_convention_test.go` are green against this commit.
The race gate, the gitignore-class harness, and the leak scan also passed.

What I additionally checked statically against the tests' own source, before
committing, so the green run was expected rather than lucky:

- `.agents/skills/git-recovery/SKILL.md` exists with frontmatter whose `name`
  equals the directory name — the condition `TestClaudeSkillsHaveCodexCounterparts`
  fails on for a new `.claude/skills/` entry.
- Fence parity is even in all three edited or created skill files —
  `extractMDSection` fatals whole-file on an odd count.
- None of the four banned bare-tally regexes in
  `TestSkillDocStatesNoBareInstanceTally` matches the edited
  `testing-practices` file.
- The `MIN_ASSERTIONS` figure the skill quotes still equals the script's single
  assignment, and my insertion did not drag a second `MIN_ASSERTIONS=<n>` into
  the citation window.
- No existing heading was renamed, and nothing was inserted above the
  same-breath rule, whose placement before the red-demonstration heading is
  asserted.

**The honest limit on that green.** Every test above is a **presence** check on
prose — the assertion-convention tests say so themselves. A pass means the
required phrases are in the required sections and no banned pattern appears; it
says nothing about whether the moved content is *correct*, or whether the
duplication window in §2 is complete. Those two are the load-bearing claims in
this branch and **neither has a mechanical check.** §2 was derived by hand and
should be reviewed by hand.

**Two traps left for the next editor of `/testing-practices`**, neither of which
any test reports usefully:

- The bare-tally ban is **whole-file and fence-blind**. Anyone who later writes a
  digit-plus-`instances` phrase *anywhere* in that file — including inside a code
  fence — fails the build with a message that points at the document's own rule
  rather than at their edit.
- The new sections open by naming two sibling sections. Cross-references by *name*
  survive insertion; the positional form ("the previous two sections") does not,
  and nothing catches it if it silently becomes false. It was written positionally
  first and changed on review.

**Not demonstrated red.** The control that was free here — create
`.claude/skills/git-recovery/` before its `.agents/` counterpart and watch the
sync test name the missing file — was not run, because running it means building.
So the sync test's green is consistent with the stub mattering and also with it
being unreachable; I have read the test and it is the former, but I did not watch
it fail.

**Reviewed.** A code-review sub-agent sharing this worktree checked every git
command in `/git-recovery` empirically in throwaway repositories, and every script
claim against the tree. It confirmed the merge-engine ordering claim exactly (the
engine really does soft-reset before committing, and an ordering test pins it),
both cherry-pick paths, the `--is-ancestor` direction, and all of the `_stmux` /
socket / `sprawl_sandbox_destroy` mechanics. It also found the `update-ref` error
above, the two unsupported commands now corrected, and the probe with no
constructible positive control. Every finding it raised is addressed in this
branch; none was declined.
