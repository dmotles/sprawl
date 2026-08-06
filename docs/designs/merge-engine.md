# The Merge Engine: How `sprawl merge` Works and What It Must Uphold

This document describes the squash-merge engine (`internal/merge`), the
policy layer above it (`internal/agentops`), and the invariants and hazards
anyone changing this code — or adding a new caller — needs to know. Line
references are anchored at the commit that introduces this document; re-check
them against the tree you are editing.

## Purpose and callers

A merge takes one agent's branch and lands it on the caller's current branch
(normally `main`) as a **single squash commit**, then leaves the agent's
branch and worktree alive, clean, and up to date with the parent. Four paths
reach the engine:

| Caller | Route |
|---|---|
| MCP `merge` tool | `internal/sprawlmcp/server.go` (`toolMerge`) → `Real.Merge` (`internal/supervisor/real.go`) → `agentops.Merge` → `merge.Merge` |
| MCP `retire` tool with `merge_first` | `Real.Retire` → `agentops.Retire` → `merge.Merge` directly |
| CLI `sprawl merge` | `cmd/merge.go` → `agentops.Merge` → `merge.Merge` (separate process from any live weave session) |
| CLI `sprawl retire --merge` | `cmd/retire*.go` → `agentops.Retire` → `merge.Merge` (separate process) |

`agentops.Merge` (`internal/agentops/merge.go`) is the policy layer: it
validates the caller's identity and parentage, checks agent status, refuses
subagents (no branch of their own), requires both worktrees clean, and —
critically — **resolves the merge source from the agent worktree's actual
HEAD branch**, refusing detached HEAD (QUM-511). The spawn-time
`AgentState.Branch` field goes stale under delegate-reuse and must never be
trusted as the merge source. The retire path currently passes the stale
field and skips several of these preconditions; QUM-1088 tracks aligning it.

## The step sequence (`internal/merge/merge.go`, `Merge`)

This is the authoritative description. `Deps` is a seam struct; the
production implementations live in `internal/merge/git.go`.

1. **Dry-run short-circuit.** `DryRun` prints the plan and mutates nothing;
   it takes no lock.
2. **Lock.** flock on `.sprawl/locks/<agent>.lock` (per-agent — see
   *Locking model* below). Held for the whole sequence, released by defer.
3. **No-op check.** `git merge-base parentBranch agentBranch`; if it equals
   the agent worktree's HEAD, return `WasNoOp` — the agent has no new
   commits.
4. **Record recovery point.** The agent's pre-squash HEAD SHA is captured;
   failure messages reference it.
4b. **Derive the commit message** (QUM-1105; labelled `Step 3b` in the source
   comments, which number the recovery point as 3 where this list numbers it
   as 4) — see *The squash commit message* below. Still before the first
   mutation, deliberately: a branch the engine cannot describe is refused
   while it is intact, rather than after step 5 has rewound it to the merge
   base.
5. **Squash.** `git reset --soft <mergeBase>` in the **agent worktree**,
   then `git commit` — the agent branch now carries one commit containing
   its whole delta. This is the first mutation, and it rewrites the agent
   branch **before** the engine knows the merge will succeed. If the
   **commit** fails, the `reset --soft` is undone by a compare-and-swap
   `update-ref` back to the pre-squash tip (QUM-1100); the index and
   worktree are deliberately left alone, because the reset never touched
   them and in the crash variant the index is the only live copy.
6. **Rebase.** `git rebase parentBranch` in the agent worktree. On failure:
   `git rebase --abort` (best-effort), then restore the branch to its
   pre-squash tip by compare-and-swap and say so in the error (QUM-1090).
   Only if the swap is *refused* is the caller asked to act. See *Failure
   leaves the branch rewritten* below.
7. **Fast-forward merge.** `git merge --ff-only agentBranch` in the
   **parent worktree**. After a clean rebase the agent branch is exactly
   parent + one commit, so this only moves the parent ref forward.
8. **Validate.** Unless `--no-validate` or no `validate` command is
   configured (`.sprawl/config.yaml`), run the validate command in the
   parent worktree, streaming output to a log under `.sprawl/logs/`
   (path is printed and checkpointed), bounded by `ValidateTimeout`
   (default 10 min). On failure the merge is rolled back on the parent —
   see *Rollback contract* below.
9. **Poke.** Write a poke file telling the agent its history was squashed
   and its branch is now up to date with the parent.

Post-merge state contract (success): parent tip = old parent tip + one
squash commit; agent branch points at that same tip; agent worktree clean;
the agent's **original** commits are no longer reachable from any ref (the
squash replaced them — by design, reflog only).

## The safety invariant

> After any merge or retire-with-merge — success, failure, or crash —
> every commit reachable from the parent branch before the operation is
> still reachable from the parent branch afterwards; on success the agent's
> delta is applied on top. No committed work anywhere becomes unreachable
> from all refs except the agent-branch squash rewrite described above,
> which is the operation's documented purpose.

The parent ref must only ever move **forward** (the `--ff-only` in step 7
enforces this for the landing itself). The one backward mutation in the
sequence is the validate-failure rollback, and it is the part that carries
all the risk.

### Rollback contract (validate failure)

Intended contract: undo **exactly the commit this merge added, and nothing
else** — and if that cannot be established, refuse and say so rather than
guess. The current implementation is a blind `git reset --hard HEAD~1` on
the parent worktree (`internal/merge/git.go:122-130`), which *assumes* the
parent tip's first parent is the pre-merge tip. Two situations break that
assumption, and QUM-1087 tracks replacing the rollback with a
compare-and-swap `git update-ref` (or, better, validating on the rebased
agent branch *before* the ff-merge so no rollback of the parent exists at
all):

* the ff-merge didn't actually move the parent (see *`--ff-only` succeeds
  without moving* below), so `HEAD~1` is a pre-existing parent commit;
* something else advanced the parent between the ff-merge and the rollback
  (an unserialized concurrent merge — see *Locking model*).

### The scenario harness's loss detector is weaker than the invariant

The invariant above is stated over **parent-branch** reachability. The loss
detector most assertions in `internal/merge/scenario_test.go` use,
`reachableFromBranches`, is `git branch --contains` — reachable from **any**
`refs/heads/*` tip. So it tests a strictly weaker property than this document
claims, and a commit that has become unreachable *from the parent branch*
while still sitting on the agent branch passes it.

Its positive control (`TestMergeSafety_ReachabilityCheckerDetectsLoss`, the S0
"detectors can fire" pin) plants a **total** loss via `reset --hard` and
asserts `{false, false}` — valid, but only for that shape. QUM-1087's S5b is the other shape: the victim stays reachable from
the agent branch, so the detector returns `true` on a real loss *and the
control stays green throughout*. A control can prove a mechanism works in
general while the mechanism is blind to the particular case it will be asked
about. This is the same `--contains` asymmetry CLAUDE.md warns about in the
squash-rebase recovery procedure — "not merged" and "merged, re-parented"
having opposite correct responses — arriving in a checker rather than in a
procedure.

**No already-merged assertion misuses it** (audited at `889b37b`): the
loss-direction assertions in `_S5_ValidateFails`, `_S2_RebaseConflict`,
`_S6*` and `_HappyPath_NoLoss` are genuine "reachable from NO branch" claims,
where any-branch scope is exactly the right scope, and the recovery-ref
assertions use `reachableFromPremergeRefs`. The gap bites only S5b, which is
not ported yet — so QUM-1090 (`fa7a48e`) and QUM-1100 (`889b37b`) do not need
re-auditing on this account. Whoever ports S5b needs a parent-branch-scoped
checker, which is not a stricter variant of `reachableFromBranches` but the
only one that tests the invariant as written.

## Locking model

Serialization is currently a property of the **caller**, not of the engine.
Anyone adding a new caller must know this table:

| Layer | Lock | Scope | Who gets it |
|---|---|---|---|
| Engine (`merge.Merge` step 2) | flock `.sprawl/locks/<agent>.lock` | **per-agent** — two merges of *different* agents do not contend | every caller |
| Supervisor (`Real.Merge`, `internal/supervisor/real.go` — `mergeSem`) | in-process semaphore, capacity 1 | per-sprawl-root, within one weave process | MCP `merge` tool only |

Consequences, as design facts:

* Two merges of different agents into the same parent are **only**
  serialized if both go through `Real.Merge`. `Real.Retire(merge_first)`
  reaches the engine without `mergeSem` (`internal/supervisor/real.go` —
  the `retireFn` call sites), and the CLI commands run in a separate
  process where the semaphore does not exist.
* The rollback hazard above is why this matters: an interleaved merge plus
  a validate failure can rewind the wrong commit.
* QUM-1089 tracks moving serialization into the engine (a per-root flock
  taken alongside the per-agent one) so it holds by construction for every
  caller. QUM-1088 tracks routing retire's merge through the serialized
  path. Until both land: **do not add a caller that bypasses `Real.Merge`.**

## Destructive git primitives

Every backward or history-rewriting mutation reachable from the merge and
retire paths, with its trigger. This is the checklist to re-verify when
changing any of this code.

| Primitive | Location | Target | Runs when |
|---|---|---|---|
| `git reset --soft <mergeBase>` | `internal/merge/git.go:56-64` | agent branch | Every non-noop merge (step 5). Rewrites the agent branch before success is known. |
| `git rebase <parent>` | `internal/merge/git.go:84-92` | agent branch | Every non-noop merge (step 6). Rewrites the squash's SHA; **drops it if it becomes empty**. |
| `git rebase --abort` | `internal/merge/git.go:101-108` | agent worktree | Rebase failure. Best-effort, output discarded. Returns the branch to the *squash*, not the original tip. |
| `git merge --ff-only <agentBranch>` | `internal/merge/git.go:111-119` | parent branch | Step 7. Forward-only; cannot itself lose commits. Exits 0 without moving when already up to date. |
| `git reset --hard HEAD~1` | `internal/merge/git.go:122-130` | **parent branch** | Validate failure (step 8). The only backward mutation of the parent in the codebase; blind — see *Rollback contract* and QUM-1087. |
| `git branch -D` | `internal/agentops/helpers.go:107-116` | agent branch | `retire --abandon`, after an unmerged-commit warning/confirmation. Force-delete: makes the branch's commits ref-unreachable. Note the MCP retire path auto-confirms (`yes=true`). |
| `git branch -d` | `internal/agentops/helpers.go:152-161` | agent branch | Retire after merge / of an already-merged branch. Safe by construction (refuses unmerged). |
| `git worktree remove [--force]` | `internal/agentops/helpers.go:118-131` via `internal/agent/retire.go` (`forceRemove = force \|\| dirty`) | agent worktree | Every retire. `--force` discards **uncommitted** files only. |
| `os.RemoveAll(.sprawl/agents/<name>/)` | `internal/state/state.go` (`DeleteAgent`) | non-git agent data | Every retire, unconditionally (QUM-1055 tracks the findings-loss consequence). |

| `git update-ref <ref> <sha>` | `internal/merge/git.go` (`RealGitUpdateRef`) | `refs/sprawl/premerge/**` | Every non-noop, non-dry-run merge, before the first mutation. Creates refs only; never moves a branch (QUM-1090). |
| `git update-ref <ref> <new> <old>` (CAS) | `internal/merge/git.go` (`RealGitUpdateRefCAS`) via `restoreAgentBranch` | agent branch | Rebase failure (QUM-1090) and squash-commit failure (QUM-1100), to restore the pre-squash tip. Compare-and-swap: refuses rather than forcing if the ref moved. |
| `git update-ref -d <ref>` | `internal/agentops/gc.go` (`DefaultDeleteRef`) | `refs/sprawl/premerge/**` | `sprawl gc --apply` beyond the retention window; prefix-guarded in `ApplyPremergeGC`. |

Deliberately absent from these paths: `git push --force`, `git checkout -f`,
`git branch -f`. Raw `git update-ref` **is** used, in the three forms above —
its justification is *Recovery refs* below and the compare-and-swap rule in
CLAUDE.md ("never overwrite the thing that tells you where you were"). Note
raw `update-ref` does **not** honour git's "branch is checked out in another
worktree" protection, which is why the CAS restores run with cwd set to the
agent worktree while the premerge writes run from `SprawlRoot`. If a change
introduces a further destructive primitive, it needs a design-level
justification here.

## The squash commit message (QUM-1105)

The squash replaces the agent's commits with one commit, and the branch is
deleted at retire. **The agent's commit message is therefore the durable
record, and the squash is the only copy that survives.**

Precedence, in `buildMergeCommitMessage`:

1. `MessageOverride` (`--message` / `message:`) — highest, unchanged.
2. Otherwise the agent branch's own commits, via the `GitLogRange` seam over
   `mergeBase..agentHead` with `--first-parent --no-merges`. One commit: its
   message byte-for-byte. Several: a derived subject, then every commit's
   full message. Both then get a provenance **trailer** block appended
   (`Squash-Merge:`, `Sprawl-Agent:`, one `Source-Commit:` per commit,
   `Premerge-Ref:`), so "verbatim" describes the body, not the whole message.
3. There is no third source. An empty derivation is an **error** naming the
   override as the remedy.

Three things here are load-bearing and easy to undo by accident:

* **`AgentState.LastReportMessage` is not a source, in any position.** It
  used to be the default one. It is a ≤160-char status ping written for a
  TUI line and updated asynchronously, under no obligation to be current at
  merge time — the observed case replaced a 455-line verified message with a
  one-liner naming a SHA three amends old. The defect class is not
  "formatting": it is reading a field whose contract does not include being
  true later. Re-adding it as "additive context" reinstates a stale field in
  the one durable record, which is why the tests pin its total absence.
* **Both `git log` flags are required, and neither implies the other.** An
  agent that merges a sibling branch in would otherwise carry that branch's
  messages into its own squash: `--no-merges` drops the merge commit's
  boilerplate subject, `--first-parent` drops the side branch it brought in.
  Removing either is individually detectable (scenario `S12`).
* **Provenance is appended as trailers, not as a free-text footer, and it
  joins the agent's own trailer block when there is one.** git parses only
  the message's LAST paragraph as trailers, so a free-text footer demotes
  every trailer the agent wrote (`Refs:`, `Signed-off-by:`, its own
  `Co-Authored-By:`) out of the trailer block: still present as text, no
  longer readable by `git interpret-trailers` or by GitHub's co-author
  attribution. That is the QUM-1105 shape one level down — preserved to the
  eye, silently degraded to every machine — and the first cut of the fix did
  it. The `Co-Authored-By` guard is prefix-keyed and case-insensitive for the
  same family of reason: an exact-line match never fires against CLAUDE.md's
  own `Co-Authored-By: Claude Opus 5 <...>` and stamps a second, conflicting
  co-author on every convention-following commit.
* **`git commit` takes the message on stdin (`-F -`), never `-m`.** Linux
  caps a single argument at MAX_ARG_STRLEN (128 KiB) regardless of ARG_MAX,
  and a real agent message passes that; `-m` then dies at fork/exec with
  "argument list too long", inside the step-5 window. `--cleanup=verbatim`
  is explicit so a user's `commit.cleanup=strip` cannot silently delete every
  `#`-leading line — and `verbatim` rather than the `whitespace` default
  because `whitespace` strips trailing whitespace and collapses runs of blank
  lines, which is a markdown hard break and an embedded diff's blank context
  line respectively.

Why this was invisible for so long: weave passes `message:` explicitly on
every merge as a matter of habit, adopted for unrelated reasons. That
**masked** the defect rather than avoiding it — the failure surfaces the
moment anyone merges without it, and agents merging their own children are
the likeliest victims, since their branches are deleted at retire.

## Structural gotchas

Engineering facts about git that shape this design; each has bitten a
"reasonable" reading of the sequence.

* **`git rebase` silently drops a squash that becomes empty.** If the
  agent's delta is already on the parent under different SHAs (cherry-pick,
  duplicate work, content-equivalent squash), non-interactive rebase drops
  the commit ("patch contents already upstream") and the agent branch lands
  exactly on the parent tip. Nothing fails. Downstream code must not assume
  "rebase succeeded ⇒ the squash exists". This also means
  `Result.CommitHash` — captured at step 5, before the rebase rewrites the
  SHA — can name a commit that exists on no ref; QUM-1087 covers detecting
  the post-rebase no-op.
* **`git merge --ff-only` exits 0 without moving when already up to date.**
  So "ff-merge succeeded" does not imply "the parent advanced by one
  commit", which is exactly the premise the `HEAD~1` rollback rests on.
  Combined with the previous point, an already-upstream delta plus a
  validate failure rewinds a commit the merge never added (QUM-1087).
* **A failed merge leaves the agent branch rewritten to the squash on the
  validate-failure and ff-merge-failure paths.** The rebase-failure and
  squash-commit-failure paths now restore the pre-squash tip themselves
  (QUM-1090, QUM-1100); the other two do not. The ff-merge path is
  undeliberate rather than decided — nothing currently gates it. The
  validate-failure path deliberately does not: there the merge *did* happen
  and was rejected on quality, so the squashed+rebased branch is a
  legitimate state to iterate on and rewinding it would discard the rebase
  work. On that path the agent's original commits are reachable only from
  the recovery refs. Manual recovery after squash operations is historically
  where damage happens (CLAUDE.md's QUM-1083 procedure), which is why the
  tool now does the restore wherever it can and never prints a
  `git reset --hard` for a human to run.
* **The window between `reset --soft` and the squash commit is as wide as a
  validate run, on every merge — and its routine trigger is our own
  tooling.** `git commit` IS a subprocess: it runs the pre-commit hook,
  which runs `make validate`. A non-zero exit is ordinary, not exotic. An
  earlier version of this document called this a "crash window" and called
  it "narrow (no subprocess between the two steps)"; both were wrong, and
  the label is what people read. It fired in production on 2026-08-06 —
  an unrelated test flake failed the hook and left 3026 insertions across
  30 files reachable from no ref, after which the retry reported
  `has uncommitted changes in worktree`, which was true and misdescribed
  the cause (the content was the engine's own orphaned squash). QUM-1100
  restores the branch whenever the engine gets to run and splits that
  message. A true crash (SIGKILL) in the same window remains uncovered by
  construction — no code runs — and the recovery refs are the whole net
  there.
* **The rebase is merge-machinery (3-way), not patch application.** An
  agent based on a branch that was later squash-merged to the parent (the
  fan-out shape documented in CLAUDE.md under QUM-1083) merges cleanly:
  identical changes on both sides are absorbed, not duplicated. The
  QUM-1083 hazard is a property of running `git rebase` on a *multi-commit
  branch* manually, not of this engine's single-squash rebase.

## Recovery refs (QUM-1090, pending)

Once QUM-1090 lands, every non-noop merge writes, before its first
mutation:

```
refs/sprawl/premerge/<agent>/<timestamp>/agent   ← agent branch tip
refs/sprawl/premerge/<agent>/<timestamp>/parent  ← parent branch tip
```

Both tips matter: the agent ref covers branch-rewrite damage; the parent
ref makes recovery from any wrongly-moved parent a one-liner. Unlike reflog
entries, these survive `git gc`, survive branch deletion at retire, and are
discoverable (`git for-each-ref refs/sprawl/premerge`). Recovery is:

```bash
git update-ref refs/heads/<branch> refs/sprawl/premerge/<agent>/<ts>/<agent|parent>
```

(or a worktree `git reset --hard <ref>` where appropriate). The refs are
pruned by `sprawl gc` after a retention window. They are a recovery net,
not a correctness mechanism — they do not detect that recovery is needed,
and they cover neither uncommitted work nor non-git agent data.

## Checklist for changing this code

* Does the parent ref still only move forward outside the (CAS-guarded)
  rollback? Prove it, don't assume ff implies advancement.
* Is the merge source resolved from the worktree HEAD (QUM-511 rule), not
  from `AgentState.Branch`?
* Is every caller of `merge.Merge` serialized against every other caller
  (see *Locking model*)?
* Re-derive the mandatory e2e rows from the CLAUDE.md touched-file table
  (`merge-reuse` at minimum for `internal/agentops/merge.go` /
  `internal/merge` changes).
* If you add a destructive primitive, add it to the table above with its
  trigger.
* Can the squash commit message still only come from `MessageOverride` or
  the agent's own commits? Any third source, in any position, is the
  QUM-1105 defect — and it will report success while it destroys the record.
