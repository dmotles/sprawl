---
name: git-recovery
description: Use for anything git, commit, or merge shaped in this repo — recovery when git state has gone wrong, and the standing rules that stop it going wrong. Recovery: a commit landed on the wrong branch, a rebase onto `main` conflicts on commits that already landed, a squash-merge stranded a downstream branch, a merge attempt looks like it un-committed an agent's work. Standing rules: stage explicit paths only (never `git add -A`), never overwrite the ref that tells you where you were. Mechanism: the `main` commit guard and reference-transaction backstop, the forward-only parent contract of `sprawl merge`, and the pre-merge recovery refs under `refs/sprawl/premerge/`. Read it BEFORE `reset`, `rebase`, `checkout -f`, `clean`, `branch -f`, `add -A`, `commit --amend`, or a merge. It exists because the obvious checks here lie in both directions.
---

# Git recovery, commit guards, and the merge engine

This file is the repository's canonical home for git procedure: the recovery
routines, the standing rules about staging and refs, and the mechanism of the
guards and the merge engine that the recoveries depend on. `CLAUDE.md` cites it
rather than restating it.

Recovery procedures for the git states this repo actually produces. Every one of
them cost a real incident, and none of them is derivable from reading the code.

Read this **before** the recovery command, not after. The common thread across
every entry is that the fastest-looking fix (`reset --hard`, `clean`, re-run the
rebase) is the one that destroys the thing you are recovering.

## The rules that apply to every entry

1. **Pin before you move.** The first action in any recovery is to make the
   content you are trying to save reachable from a ref. An unreferenced commit
   is held only by the reflog — which expires, which `gc` can prune, and which
   does not survive the worktree being removed. A ref survives all three.
2. **`--hard` is never right.** Between the other two, the discriminator is
   whether the ref you are moving is the checked-out `HEAD` of the worktree you
   are standing in: `--soft` when it is **not** (the agent-worktree rescue
   below), `--mixed` when it **is** (the main checkout — see the next section
   for why `--soft` is actively wrong there). `reset --hard` is how recoveries
   turn into second incidents.
3. **Never `git reset --hard` on `main`, ever, for any reason.** The root
   agent's uncommitted work lives in the main checkout, and a `--hard` there can
   clobber weave's uncommitted state or destroy work outright. A non-root agent
   that wants `main`'s ref moved should stop and ask the root agent rather than
   move it.
4. **Stage explicit paths only — never `git add -A`.** See
   § *Staging: never `git add -A`*. This one is a standing rule, not a recovery
   step: it applies to every commit you make, including the recovery commits
   here.
5. **Never overwrite the thing that tells you where you were.** See
   § *Never overwrite the thing that tells you where you were*. Also standing,
   and also load-bearing during a recovery, because a recovery is exactly when
   you are creating and moving pointers.

## Which merge engine will run?

Two sections below branch on this, so establish it first. The current engine
(QUM-1087 + QUM-1090) rebases-validates-fast-forwards and writes recovery refs;
the pre-QUM-1087 engine rewrote the agent's branch before it knew its squash
commit would succeed. **Which one applies depends on the binary that will run**,
not on branch state and not on a report that the fix merged. Probe the binary:

```bash
strings <the-sprawl-binary-that-will-run> | grep -c 'refs/sprawl/premerge'
```

| result | reading |
|---|---|
| **nonzero** | the QUM-1087/QUM-1090 engine. It creates no commit, so it cannot orphan a squash; recovery refs are being written for you. |
| **zero** | the pre-QUM-1087 engine: it rewrites the branch before it knows the squash succeeds, and § *A merge attempt reports uncommitted changes* applies in full. |

Assert **nonzero**, never a specific count — `grep -c` counts *lines*, Go packs
strings, and the matching strings include unrelated help text, so the number
moves with changes that have nothing to do with the engine. As an illustration
only: a binary built from `c7093cc` (`go build -o /tmp/probe-sprawl .`) returned
**7**. That build is also the probe's positive control — if you doubt the probe,
build the tree and watch it return nonzero before you trust a zero.

**Probe the binary the merge will actually run, not whatever `sprawl` resolves
to on your `PATH`.** The tree you are reading is fixed; an installed binary is
not.

## A commit landed on `main` by mistake

Recover by re-homing the commit to the branch that should own it, then moving
`main`'s ref without touching the working tree.

```bash
# 1. Identify the stray commit.
git -C <main-checkout> log --oneline -1 main

# 2. Re-home it. cherry-pick preserves the commit and does not mutate main.
git -C <agent-worktree> cherry-pick <stray-sha>
#    Verify it now exists on the agent branch before continuing.

# 3. Rewind main's ref — ROOT AGENT ONLY, from the main checkout.
git -C <main-checkout> reset --mixed <prior-good-sha>
```

**Do not expect `status` to be clean afterwards, and do not use `--soft` or a bare
`update-ref` here.** In the main checkout `HEAD` *is* `main`, so any command that
moves the ref without also moving the index leaves the stray commit's whole tree
sitting in the index — reported as **staged**, and silently re-landed by the next
`git commit` anyone runs in that checkout. Both `reset --soft` and
`update-ref refs/heads/main` produce that outcome; they are not different here.

`reset --mixed` moves the ref *and* resets the index, while leaving the working
tree alone. The stray content therefore reappears as ordinary **uncommitted** work
in the main checkout, which is the correct resting state — it is the root agent's
uncommitted work again, exactly as it was before someone committed it by mistake.
The root agent decides what happens to it from there. Nothing is lost, because
`--mixed` never writes the working tree. Its one cost: it also unstages anything
the root agent had legitimately staged, which is noisy but recoverable — re-stage
by explicit path.

There is no command that makes `status` clean here, and you should not look for
one: the only way to a clean status is discarding the content, which is
`reset --hard` and is forbidden. If you have seen this procedure written with
`--soft` and "confirm `status` is clean" — that was the earlier `CLAUDE.md`
wording, it is wrong for the reason above, and it should not be restored.

Confirm the *ref* moved rather than the status:

```bash
git -C <main-checkout> merge-base --is-ancestor <stray-sha> main   # must exit NON-zero
```

Read that argument order carefully — it asks *is the stray commit contained in
`main`*, which is the question you want answered **no**. See
§ *Check that the question the command answers is the question you are claiming*.

This should be a rare exception rather than a routine: the commit guards block
non-root agents from landing on `main` in the first place (§ *Guards: what stops
you landing on `main`*), and sprawl additionally refuses to resume or wake a
non-root agent whose worktree HEAD is on `main`.

## A merge attempt reports uncommitted changes, or the branch lost its commits

Symptom, usually on a **retry** after an earlier merge attempt failed:
`has uncommitted changes in worktree. Ask the agent to commit first`.

**Do not clean, checkout, stash, or `reset --hard` that worktree.** The message
is true and describes the wrong cause: the staged content may be the only live
copy of the work.

**Two causes, and the message you received is itself the discriminator.**

* **The agent genuinely has uncommitted edits.** Ordinary ` M` modifications.
  Nothing is at risk; ask it to commit.
* **A pre-QUM-1087 engine orphaned its squash.** That engine soft-reset the
  agent's branch to the merge base *before* it knew the squash commit would
  succeed. If that commit then failed — a pre-commit hook tripping on an
  unrelated flake is enough — nothing undid the reset. The commits are reachable
  from no ref, surviving only as staged index content and unreferenced objects.

The shipped error text distinguishes them: a staged-only worktree gets a message
naming a `PREVIOUS FAILED MERGE`, pointing at the premerge recovery refs, and
saying **do NOT discard**; the bare `Ask the agent to commit first` is the
ordinary-edits case. And per § *Which merge engine will run?*, the current engine
creates no commit and therefore cannot produce the second cause at all — so on an
up-to-date binary the first is the likely one. Either way, diagnose before you
discard.

**Diagnose first, writing nothing:**

```bash
git -C <agent-worktree> rev-parse --abbrev-ref HEAD   # which branch is it on
git -C <agent-worktree> log --oneline -5              # does the branch still hold its commits
git -C <agent-worktree> status --short                # what is staged
git -C <agent-worktree> reflog --all | head -30       # the branch's real tip from before the merge
```

If the merge that failed was run by a current binary, prefer the recovery refs it
already wrote over the reflog — see § *Pre-merge recovery refs (QUM-1090)*.

**Then pin, then move the ref — never the tree:**

```bash
# 1. PIN the recovered tip before anything else.
#    Name the ATTEMPT, not the branch, and embed a millisecond ISO timestamp:
#      refs/sprawl/rescue/<agent>/<ISO8601.mmm>/<slug>
git update-ref refs/sprawl/rescue/<agent>/<ISO8601.mmm>/<slug> <recovered-sha>

# 2. Move the branch back WITHOUT touching the working tree or index.
git -C <agent-worktree> reset --soft refs/sprawl/rescue/<agent>/<ISO8601.mmm>/<slug>

# 3. Verify, then commit anything still staged.
git -C <agent-worktree> log --oneline -5
git -C <agent-worktree> status --short
```

Two rules travel with that rescue ref, and both are argued in full under
§ *The `refs/sprawl/` namespace*: **millisecond** precision in the timestamp, and
**never hand-write a ref under `refs/sprawl/premerge/`** — hand-written pins go
under `refs/sprawl/rescue/` or `refs/sprawl/manual/`. Nothing prunes those two
namespaces, so an orphaned rescue ref is a trivial price against a lost commit.

## A rebase onto `main` conflicts on commits that already landed (QUM-1083)

Note before you start: the current engine (§ *The merge engine mutates the parent
once, forward-only*) creates no commit, which **deletes this hazard class
outright** for merges it performs. This section applies to squash-merges done by
hand, and to history produced before that engine landed.

### The precondition

Squash-merging a base branch to `main` replaces its commits with one new commit
carrying a **different SHA**. A branch still based on the *originals* now holds
that content twice — once in its own history, once in the squash — so `git
rebase main` replays base commits onto a tree that already contains them.

This hits **any fan-out with a common base**, which is the normal shape whenever
two managers stack work on one tree. It is not anyone's mistake; it is what
squash-then-rebase does.

### Both natural checks lie, in opposite directions

* **`git branch --contains <original-base-tip>` does not list `main`** after a
  squash-merge, even though the content is fully present. The reading "not
  merged" and the reading "merged, re-parented" have opposite correct responses,
  and the command cannot tell you which one you are looking at.
* **The rebase may succeed, which proves nothing.** Git drops a replayed commit
  on an exact patch-id match (`skipped previously applied commit`) or when the
  replay empties out (`patch contents already upstream`), so a single-commit
  base, or base commits touching disjoint files, sail through. As soon as one
  base commit's patch does not apply verbatim to the squashed tree — the normal
  case for a branch that touches a file more than once — it conflicts, and it
  conflicts on the *base* commits, not on yours.

### Prevent, don't recover

When two branches share a base, either **merge the dependent one first** or
**rebase it onto the squash before merging the base**. Best of all, do not base a
branch on another branch that has not landed yet. The procedure below is for when
that was missed.

### Step 1 — gate on the base being content-equivalent

```bash
git diff <squash-commit-on-main> <original-base-tip>   # must be EMPTY
```

If it is not empty, **stop**: the squash changed content, and the downstream
delta is not safe to replay blind.

### Step 2 — cherry-pick the delta; do not rebase

```bash
git switch -c <my-branch>-rebased main
git cherry-pick -n <original-base-tip>..<my-tip>
```

The range excludes the already-landed commits **by construction**, where a
rebase includes them by construction. That difference is the whole reason this
works. On the clean path `-n` leaves nothing in progress and `git status` shows
an ordinary staged set, which is where step 3 happens.

**A conflict here does not mean step 1 failed.** Step 1 only establishes that the
squash matches the *original base*; it says nothing about commits `main` acquired
afterwards. You branched off `main`'s tip, so if later work touched the same
regions as your delta you get an ordinary cherry-pick conflict with step 1
entirely correct — resolve it as such rather than re-auditing the gate. A
conflicted *range* cherry-pick leaves the sequencer populated
(`.git/sequencer`, no `CHERRY_PICK_HEAD`), so `git status` says `Cherry-pick
currently in progress` and branch switching is refused until you resolve it or
run `git cherry-pick --abort`.

### Step 3 — verify the delta, not the absence of conflicts

If you branched off the squash commit and `main` has not moved since, the tree
must match exactly:

```bash
git diff <my-tip>          # must be EMPTY
git status --short         # git diff cannot see untracked strays
```

If `main` has advanced past the squash, that diff reports `main`'s later commits
and is **not** a pass/fail predicate (QUM-1085). Compare the *deltas* instead:

```bash
diff <(git diff <original-base-tip> <my-tip>) \
     <(git diff main <my-branch>-rebased)     # must be exactly ZERO lines
```

Raw line counts here are dominated by blob `index` and `@@` header lines; to
count only content, filter with
`| grep -E '^[<>] [+-]' | grep -vE '^[<>] (\+\+\+|---)'`.

**A clean cherry-pick is not evidence of an identical tree.** The wrong range
exits **0** with content silently missing. Git reports textual success; it does
not report that you got the tree you meant. (And never sweep a stray in with
`git add -A` — see § *Staging: never `git add -A`*.)

### Step 4 — commit, then check the parent

```bash
git commit
git merge-base --is-ancestor main <my-branch>-rebased   # must exit 0
```

**Run the parent check after committing, not before** — until you commit, the
branch tip *is* the squash commit, so `--is-ancestor` inspects `main` against
itself and returns 0 even with nothing staged. Run it also on **any branch
someone hands you** claiming to be the rebased result.

**"Tree matches" is necessary and not sufficient.** A branch built on the
*original* base carries a byte-identical tree and passes steps 1–3 while
sitting off `main` entirely. Same shape as asserting a value where the
**wiring** is what matters (cf. QUM-1080) — hence step 4.

Finally, retire the original: once `<my-branch>-rebased` passes both checks,
point the merge at it, or `git branch -f <my-branch> <my-branch>-rebased` so
the name everyone else is using follows the recovered work.

### Check that the question the command answers is the question you are claiming

Both hazards above are one class — an asymmetric relation verified
in the convenient direction and reported in the desired one — and running the
command *more carefully* does not catch it, because the command is already
correct and its result already true. `git merge-base --is-ancestor main
<branch>` (step 4) asks *is `main` contained in my branch* — "I am rebased up to
date"; the reversed argument order asks *did my commits land on `main`*. Those
are different claims. Reread the argument order — or the subject of any check
you did not design — against the sentence you are about to write.

This is not a git rule and it is not confined to git. The general form, and the
other places it has bitten, live with the assertion rules — read
`/testing-practices` § *Check that the question the command answers is the
question you are claiming*.

## The merge engine mutates the parent once, forward-only (QUM-1087)

`sprawl merge` (and the `retire` MCP tool with `merge: true`, which routes
through the same engine — note there is no `sprawl retire` CLI command)
**rebases the agent's branch onto the parent's, validates that rebased tree in
the agent's own worktree, and only then fast-forwards the parent onto it.** The
parent is mutated exactly once, after the tree is already known good, so there
is no rollback of the parent — and `internal/merge` contains no primitive that
could perform one. `RealGitResetHard` was deleted, not merely left uncalled.

Two consequences worth knowing before reading the code:

* **`--ff-only` exiting 0 does not mean the parent moved.** It exits 0 without
  moving anything when already up to date, which is what let a validate-failure
  rollback rewind a *pre-existing parent commit* whenever the agent's content
  was already upstream. So the engine asserts the ref move directly:
  `merge-base --is-ancestor <parent-tip> <rebased-tip>` before, and exact SHA
  equality of the parent's tip against the rebased tip after. Mind the argument
  order — reversed, it asks a different question that is also true when the two
  are equal.
* **The engine creates no commit.** The agent's own commits are fast-forwarded
  as they are; squashing is the branch owner's decision before declaring done.
  This also deletes the QUM-1083 hazard class outright: a downstream branch
  stays a genuine ancestor, so there is no double-presence and no cherry-pick
  recovery to perform.

**Accepted cost, recorded so it is not re-litigated: the agent's intermediate
commits land on the parent individually, and whether each was hook-validated is
a property of the repository, not of sprawl.** In *this* repo each was validated
by `scripts/pre-commit`, installed by *this repo's own* `.sprawl/config.yaml`
`worktree.setup` (and by `make hooks`). **Sprawl installs no hooks in any repo it
drives** — so in any other repo the intermediate commits carry no validation at
all, and even here `git commit --no-verify` defeats the hook (the same
skippability that motivated the QUM-837 reference-transaction backstop). What
gates the landing is the engine's own validate run on the rebased tip; the
per-commit hook is a per-repo bonus. Do not restate this as "every commit is
hook-validated" — that sentence is true of one repository and false of the
mechanism. And `git bisect` on the parent can therefore land on a commit with no
*enforced* guarantee of being green. This is not an invitation to add a
per-commit validation loop.

## Pre-merge recovery refs (QUM-1090)

Every non-noop, non-dry-run `merge` writes two refs **before its first
mutation** (now the rebase), so a failed or crashed merge is recoverable from a
ref rather than from the reflog:

```
refs/sprawl/premerge/<agent>/<timestamp>/agent    ← agent branch tip
refs/sprawl/premerge/<agent>/<timestamp>/parent   ← parent branch tip
```

Unlike reflog entries these survive `git gc`, survive branch deletion at
retire, and are discoverable. Inspect and recover with:

```bash
git for-each-ref refs/sprawl/premerge                 # newest last: the name sorts chronologically
git update-ref refs/heads/<branch> <the recovery ref>  # recovery is one command
```

**Both siblings matter, and a check that only looks at `/agent` is wrong.**
The agent ref covers agent-branch damage; the **parent** ref is what makes a
wrongly-rewound `main` a one-liner, and a rewound `main` is the loss mode
that motivated this. A check that finds `/agent`, passes, and never looks
for `/parent` misses exactly the half that was added.

**The `/parent` ref survives QUM-1087, on a different argument.** After
QUM-1087 the engine never rewinds the parent — the ff-merge is the parent's
only mutation and it is forward-only — so the original "the rollback might
rewind the wrong commit" justification is gone. It stays for two reasons that
are true of the new flow: (1) *forward is not safe by itself* — `--ff-only`
guarantees the parent only advances, and guarantees nothing about **what** it
advances onto; advancing `main` onto a rebased branch nobody intended is
precisely the QUM-1088 stale-branch defect, which *reported success*, and
recovery from "the parent advanced to the wrong tree" is `git update-ref
refs/heads/main <parentRef>` only because the ref exists. And (2) it is the
durable witness for the headline claim "a validate failure leaves the parent's
SHA byte-identical" — something a reader may need to check after the fact,
possibly after a `git gc`, which the reflog cannot answer. Do not "simplify"
the pair to one ref on the grounds that the rollback is gone.

### The `refs/sprawl/` namespace

`refs/sprawl/premerge/` is owned **exclusively** by this mechanism, so
anything under it is tool output by construction. That is load-bearing, not
tidiness: a hand-made ref once lived under this prefix, which meant the
obvious verification (`git for-each-ref refs/sprawl/premerge`, see output,
conclude it works) returned non-empty on a tree where the feature had never
run once — a false positive in the verification of the very mechanism you would
be checking for. Put ad-hoc refs under `refs/sprawl/rescue/` or
`refs/sprawl/manual/`.

Only `premerge/` is swept. `sprawl gc` prunes those after
`--premerge-retention-days` (default 14), ageing them by the **timestamp in the
ref name** — never by the commit date, since a commit authored months ago and
merged today is not an old *ref*. A ref whose name does not parse is never
pruned. Nothing prunes `rescue/` or `manual/`, which is why a hand-written pin
there is safe to leave behind.

## Never overwrite the thing that tells you where you were

One rule, four surfaces. It is worth seeing them as one, because each looks
like local caution on its own:

1. **Operator procedure** — when relocating a ref, create the replacement
   **before** deleting the original, never the reverse.
2. **Engine primitive** — restore a branch with a compare-and-swap
   (`git update-ref <ref> <new> <old>`), so a ref that moved underneath you
   **refuses** rather than clobbers. A blind write cannot tell "I am fixing
   this" from "someone else already did".
3. **Ad-hoc safety refs** — add a new ref rather than repointing an existing
   one. Two refs cost nothing; a lost pointer costs everything.
4. **Naming** — name a ref for an **attempt**, not for a **branch**. A ref
   named for a branch has to be *maintained*; a ref named for an attempt
   *cannot go stale*, because it describes a moment rather than a moving
   target. This is why the refs above carry `<agent>/<timestamp>` rather
   than one ref per branch.

Point 4 was demonstrated live, by accident, on the very merge the mechanism
exists to protect. A hand-written `refs/sprawl/rescue/<agent>-<slug>` was
created at a branch tip as a stand-in while QUM-1090 was not yet in the
running binary. The next `git commit --amend` silently invalidated it: it
went on pointing at the old SHA, **nothing announced** that it had stopped
answering "where is this branch", and recovery would still have worked *only
because that amend happened to be message-only*. An amend that touched the
tree would have left the ref pointing at a tree nobody wanted — **still
looking authoritative**. That is luck, not design, and it is the failure
mode a per-attempt name does not have.

The scope of that hazard is wider than "has anyone branched from it".
**An amend is unsafe once someone has *cited* the SHA, not only once someone
has *branched from* it.** A citation is a reference too: a mutation-verification
result quoted by SHA, a `Source-Commit:` trailer, a review note, an issue
comment, a line of evidence in a commit message. An amend replaces the object
those name and — exactly as above — **nothing announces it**; the citation goes
on looking authoritative while describing a commit that no longer exists.
Declined in practice during this series: commit `f7b2779` had been
mutation-verified by SHA and the result quoted as evidence, so an amend that the
letter of the rule permitted was refused and a follow-up commit written instead.
Two commits cost a line of history; a citation that silently names the wrong
object costs the evidence itself.

The timestamp in that name is **millisecond** precision, and the first live
exercise of the feature is why that is not fussiness:

```
refs/sprawl/premerge/engX/20260806T080747.780Z/{agent,parent}
refs/sprawl/premerge/engX/20260806T080747.863Z/{agent,parent}
```

Two merges of the same agent **83 milliseconds apart, inside the same
second**. At second precision those two names collide, and the second merge
**silently overwrites the first's recovery pair** — destroying the artifact
the feature exists to preserve, in precisely the circumstance it exists for.
Millisecond precision was argued for from *reasoning about* collisions before
any of this ran; the design's failure case then occurred unprompted on first
contact. That is what makes it a demonstrated necessity rather than a
defensible preference, and it is the strongest evidence point 4 will get.
Use millisecond precision in hand-written rescue refs too, for the same reason.

The incident is the point. Compressed to a maxim the rule reads as obvious
and gets routed around; attached to a case where it caught out people who
were paying close attention, it is a thing that actually happens.

## Staging: never `git add -A` (QUM-989)

**Standing rule: stage explicit paths only.** Never `git add -A`, `git add .`,
or `git commit -a`.

The reason is specific to this repo, not general tidiness. Agent worktrees sit
on a **shared filesystem** next to other agents' scratch output, and agents run
tooling (`terraform apply`, `az acr build`, test harnesses) that drops files
nobody named in advance. `-A` stages *whatever is present*, so the contents of
your commit become a function of **other agents' filesystem hygiene and of
files you never created**. This has occurred here (QUM-989): a 57 KB terraform
plan and two Azure apply/build logs appeared inside an agent worktree, written
by an unidentified process, in a subtree that worktree's own work never
touched. An `-A` commit that comes out clean is clean by timing, not by
discipline.

The two `main` guards do not help here: this is a **correct-branch,
correct-identity commit containing foreign content**. Neither does
`scripts/guard-employer-leak` for the worst case — it is text-only and
structurally blind to binaries, and a terraform plan is a zip archive.

`.gitignore` is a backstop, not the control — except for the binary artifact
classes, where it is the *only* defence (see the QUM-989 comments in
`.gitignore`). It can only exclude patterns someone predicted; `-A` is
precisely the operation that finds the ones nobody did. (It also does not stop
`git add -f`.)

```bash
git add internal/tui/app.go internal/tui/app_test.go
git status && git diff --cached   # the staged set must be EXACTLY what you intend
git commit -m "..."
```

When the change is large, `git add -u` is the sanctioned shortcut: it stages
modifications to **already-tracked** files only, so it cannot pick up a foreign
artifact by construction. Still review `git diff --cached` before committing.

Explicit paths also fail *loudly* rather than silently: `git add` on an ignored
path errors out instead of skipping it, so an over-broad ignore rule can never
quietly drop a file you meant to commit.

If an untracked file surprises you, do not stage it — find out what wrote it.

## Guards: what stops you landing on `main` (QUM-808)

The pre-commit hook (`scripts/pre-commit`, installed via `make hooks`) runs
`scripts/guard-main-commit` **before** `make validate`. The guard refuses a
`git commit` on branch `main` when the committing process is a non-root agent,
identified via `$SPRAWL_AGENT_IDENTITY`:

- `weave` (the root agent) — allowed to commit to `main`.
- any other non-empty identity (a child agent) — **blocked** on `main`.
- unset/empty (a human running `git` directly) — allowed.
- any branch other than `main` — always allowed.

Because git worktrees share the common `.git/hooks` directory, the guard fires
from whichever worktree is committing — so the QUM-808 failure mode (an agent's
Bash cwd drifting to the main repo root + absolute-path `git commit` silently
landing on `main`) is caught regardless of cwd.

**Installation.** The hook is **auto-installed on every agent worktree
creation**: `.sprawl/config.yaml`'s `worktree.setup` idempotently symlinks
`$SPRAWL_ROOT/scripts/pre-commit` into the shared hooks dir
(`$(git rev-parse --git-common-dir)/hooks/pre-commit`). Since worktrees share
one common `.git/hooks` dir, installing from any worktree covers the main
checkout and all worktrees at once. (Note `make hooks` only works from the
main checkout — a worktree's `.git` is a file, not a directory, so the
`worktree.setup` snippet uses the `--git-common-dir` form instead.) **weave
should run `make hooks` once in the main checkout** to cover the case where no
agent worktree has been created yet — otherwise the guard stays dormant until
the first worktree is spawned.

### Reference-transaction backstop (QUM-837)

The pre-commit guard above is **skippable by `git commit --no-verify`**, and
any condition that pushes agents onto `--no-verify` therefore disables it
(QUM-836/QUM-830). `scripts/guard-main-ref` closes that hole: it is a git
`reference-transaction` hook — a class git does **not** skip under
`--no-verify`. It rejects any update to `refs/heads/main` by a non-root agent
**regardless of how the update was attempted** (commit, reset, merge, even
`--no-verify`). Verified on git 2.34.1: a non-zero exit in the `prepared` phase
aborts the whole transaction (`fatal: ref updates aborted by hook`).

- **Identity semantics are identical to `guard-main-commit`**: `weave` (root)
  allowed; unset/empty `$SPRAWL_AGENT_IDENTITY` (a human) allowed; only a
  non-empty, non-`weave` identity is blocked.
- **Keys strictly on `refs/heads/main`.** The hook fires for *all* ref updates
  (fetch → `refs/remotes/*`, resets, other branches, the `HEAD` symref line);
  only the literal `refs/heads/main` update is rejected, and only in the
  `prepared` phase (the `aborted` phase re-fires the same lines and must stay
  inert). Legitimate non-root work on other branches is unaffected.
- **Auto-installed** via the same `.sprawl/config.yaml` `worktree.setup` path as
  the pre-commit hook (idempotent `ln -sf` into
  `$(git rev-parse --git-common-dir)/hooks/reference-transaction`), so it fires
  from every worktree and the main checkout regardless of cwd. `make hooks`
  installs it alongside the pre-commit hook.

Sprawl also enforces a **hook-independent** defense in depth: a non-root agent
whose worktree HEAD is on `main` is refused at resume/wake
(`agentops.AssertNotOnMain`, wired into `Real.RecoverAgents` and `Real.Wake`),
and `worktree.Create` asserts a freshly created worktree's HEAD is its intended
branch. A stale advertised `AgentState.Branch` is self-healed (warn-only) from
the worktree's real HEAD on resume/wake — never a hard error, since
delegate-reuse legitimately diverges it and `merge.go` already untrusts it.

## See also

* `/false-red` — symptom-first triage when a failure may be environmental
  rather than yours. It carries an overlapping entry on the merge-un-commits
  symptom; this file owns the procedure and the namespace rules.
* `/testing-practices` — the general form of the argument-order rule, and the
  rest of the assertion-rigor material.
