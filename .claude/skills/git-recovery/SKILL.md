---
name: git-recovery
description: Use when git state has gone wrong and you are about to run a recovery command — a commit landed on the wrong branch, a rebase onto `main` conflicts on commits that already landed, a squash-merge stranded a downstream branch, or a merge attempt looks like it un-committed an agent's work. Read it BEFORE `reset`, `rebase`, `checkout -f`, `clean`, or `branch -f`. It exists because the obvious checks here lie in both directions.
---

# Git recovery

Recovery procedures for the git states this repo actually produces. Every one of
them cost a real incident, and none of them is derivable from reading the code.

Read this **before** the recovery command, not after. The common thread across
every entry is that the fastest-looking fix (`reset --hard`, `clean`, re-run the
rebase) is the one that destroys the thing you are recovering.

## The three rules that apply to every entry

1. **Pin before you move.** The first action in any recovery is to make the
   content you are trying to save reachable from a ref. An unreferenced commit
   is one `gc` away from gone, and one mistake away from unfindable.
2. **`--soft`, never `--hard`.** `reset --soft` moves a branch pointer and
   leaves the working tree and index alone. `reset --hard` is how recoveries
   turn into second incidents. If a procedure here says `--soft`, there is no
   variant of it that uses `--hard`.
3. **Never `git reset --hard` on `main`, ever, for any reason.** The root
   agent's uncommitted work lives in the main checkout. A non-root agent that
   wants `main`'s ref moved should stop and ask the root agent rather than move
   it.

## A merge attempt reports uncommitted changes, or the branch lost its commits

Symptom, usually on a **retry** after an earlier merge attempt failed:
`has uncommitted changes in worktree. Ask the agent to commit first`.

**Do not clean, checkout, stash, or `reset --hard` that worktree.** The message
is true and describes the wrong cause: the staged content may be the only live
copy of the work.

Mechanism: the merge engine soft-resets the agent's branch to the merge base
*before* it knows the squash commit will succeed. If that commit then fails — a
pre-commit hook tripping on an unrelated flake is enough — nothing undoes the
reset. The commits are reachable from no ref, surviving only as staged index
content and unreferenced objects.

**Diagnose first, writing nothing:**

```bash
git -C <agent-worktree> rev-parse --abbrev-ref HEAD   # which branch is it on
git -C <agent-worktree> log --oneline -5              # does the branch still hold its commits
git -C <agent-worktree> status --short                # what is staged
git -C <agent-worktree> reflog --all | head -30       # the branch's real tip from before the merge
```

**Then pin, then move the ref — never the tree:**

```bash
# 1. PIN the recovered tip before anything else.
#    Name the ATTEMPT, not the branch, and embed an ISO timestamp:
#      refs/sprawl/rescue/<agent>/<ISO8601>/<slug>
git update-ref refs/sprawl/rescue/<agent>/<ISO8601>/<slug> <recovered-sha>

# 2. Move the branch back WITHOUT touching the working tree or index.
git -C <agent-worktree> reset --soft refs/sprawl/rescue/<agent>/<ISO8601>/<slug>

# 3. Verify, then commit anything still staged.
git -C <agent-worktree> log --oneline -5
git -C <agent-worktree> status --short
```

**The `refs/sprawl/rescue/` namespace is not documented in any landed tree.** A
merge-safety series in flight (QUM-1090 / QUM-1100) proposes it, together with a
decision to keep such refs permanently rather than sweep them. Use it anyway; an
orphaned ref is a trivial price against a lost commit. Two rules travel with it:

* **Embed an ISO timestamp in the ref name.** `sprawl gc` ages refs by the
  timestamp *in the name*, never by commit date, so a name it cannot parse is
  never pruned. Nothing sweeps `rescue/` today; the naming keeps the option open
  and costs nothing.
* **Never hand-write a ref under `refs/sprawl/premerge/`.** That prefix is
  reserved for the merge engine's own output, so that anything under it is tool
  output *by construction*. A hand-made ref there once made a check for the
  engine's presence return non-empty on a tree where the engine had never run —
  a false positive in the verification of the very mechanism you would be
  checking for.

**Whether the hazard is live depends on the binary that will run**, not on
branch state and not on a report that the fix merged. Check the binary:

```bash
strings $(command -v sprawl) | grep -c 'refs/sprawl/premerge'
# 0 = the old engine: it rewrites the branch before it knows the squash succeeds,
#     and this section applies.
```

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
git -C <main-checkout> reset --soft <prior-good-sha>
#    or: git update-ref refs/heads/main <prior-good-sha>
```

`--soft` keeps the working tree and index intact and only moves the pointer.
Confirm afterwards that `git -C <main-checkout> status` is clean and that the
stray commit is no longer an ancestor of `main`.

This should be a rare exception rather than a routine: commit guards block
non-root agents from landing on `main` in the first place, and sprawl
additionally refuses to resume or wake a non-root agent whose worktree HEAD is
on `main`.

## A rebase onto `main` conflicts on commits that already landed (QUM-1083)

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
conflicted *range* cherry-pick leaves the sequencer populated, so `git status`
says `Cherry-pick currently in progress` and branch switching is refused until
you resolve it or run `git cherry-pick --abort`.

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
compare only content, filter with
`| grep -E '^[<>] [+-]' | grep -vE '^[<>] (\+\+\+|---)'`.

**A clean cherry-pick is not evidence of an identical tree.** The wrong range
exits **0** with content silently missing. Git reports textual success; it does
not report that you got the tree you meant. And never sweep a stray in with
`git add -A` — staging is explicit paths only, always.

### Step 4 — commit, then check the parent

```bash
git commit
git merge-base --is-ancestor main <my-branch>-rebased   # must exit 0
```

**Run the parent check after committing, not before.** Until you commit, the
branch tip *is* the squash commit, so `--is-ancestor` inspects `main` against
itself and returns 0 with nothing staged. Run it also on **any branch someone
hands you** claiming to be the rebased result.

**"Tree matches" is necessary and not sufficient.** A branch built on the
*original* base carries a byte-identical tree and passes steps 1–3 while sitting
off `main` entirely — which is why step 4 exists.

Finally, retire the original: once `<my-branch>-rebased` passes both checks,
point the merge at it, or `git branch -f <my-branch> <my-branch>-rebased` so the
name everyone else is already using follows the recovered work.

## The epistemic rule this section taught

The hazard behind step 4 and behind `git branch --contains` is the same one: an
**asymmetric relation verified in the convenient direction and reported in the
desired one**. It is not a git rule and it is not confined to git, so it lives
with the other assertion rules — read `/testing-practices` § *Check that the
question the command answers is the question you are claiming*.
