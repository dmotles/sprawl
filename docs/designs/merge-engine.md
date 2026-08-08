# The Merge Engine: How `sprawl merge` Works and What It Must Uphold

This document describes the merge engine (`internal/merge`), the
policy layer above it (`internal/agentops`), and the invariants and hazards
anyone changing this code — or adding a new caller — needs to know. Line
references are anchored at the commit that introduces this document; re-check
them against the tree you are editing.

## Purpose and callers

A merge takes one agent's branch, rebases it onto the caller's current branch
(normally `main`), validates the rebased tree **in the agent's worktree**, and
then **fast-forwards** the parent onto it — landing the agent's own commits as
they are. It then leaves the agent's branch and worktree alive, clean, and up to
date with the parent. Three paths reach the engine:

| Caller | Route |
|---|---|
| MCP `merge` tool | `internal/sprawlmcp/server.go` (`toolMerge`) → `Real.Merge` (`internal/supervisor/real.go`) → `agentops.Merge` → `merge.Merge` |
| MCP `retire` tool with `merge_first` | `Real.Retire` → `Real.merge` → `agentops.Merge` → `merge.Merge` (QUM-1088: it no longer reaches the engine from `agentops.Retire`) |
| CLI `sprawl merge` | `cmd/merge.go` → `agentops.Merge` → `merge.Merge` (separate process from any live weave session) |

There is **no `sprawl retire` CLI command** — an earlier revision of this table
listed one. `cmd/` has no `retire*.go`, and `cmd/init_removed_test.go` lists
`retire` among the removed commands, so `Real.Retire` is the only route to
retire-with-merge. That dead row survived because nobody was looking for it;
CLAUDE.md's "audit the category, not the predicted instance" is the rule it
violated.

**No squash.** The engine creates no commit at all. Squashing or rewriting
history is the branch owner's decision before declaring done — a manager may
squash before reporting ready, weave may before pushing to origin. Reasons:
smaller incremental diffs on the parent, finer bisect granularity, and it
deletes the QUM-1083 squash-then-rebase divergence class outright (a downstream
branch stays a genuine ancestor, so content is never present twice).

`agentops.Merge` (`internal/agentops/merge.go`) is the policy layer: it
validates the caller's identity and parentage, checks agent status, refuses
subagents (no branch of their own), requires both worktrees clean, and —
critically — **resolves the merge source from the agent worktree's actual
HEAD branch**, refusing detached HEAD (QUM-511). The spawn-time
`AgentState.Branch` field goes stale under delegate-reuse and must never be
trusted as the merge source. **QUM-1088 (folded into QUM-1087) resolved the
retire path**: it used to build its own merge config from the stale field and
skip several of these preconditions, so once delegate reuse moved a worktree the
merge reported success while the parent received none of the agent's current
work. `Real.Retire(mergeFirst)` now calls `Real.merge`, inheriting the
resolution, the detached-HEAD refusal, preconditions 7/8 and `mergeSem` — which
it previously bypassed. The fix was deleting the second call site, not copying
the resolution into it.

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
4. **Record recovery point.** The agent's pre-**rebase** HEAD SHA is
   captured; failure messages reference it.
5. **Write the premerge recovery refs** (QUM-1090) — `/agent` and `/parent`,
   both, before the first mutation. See *Pre-merge recovery refs* below and the
   CLAUDE.md section of the same name for why both siblings survive QUM-1087.
6. **Rebase.** `git rebase parentBranch` in the **agent worktree**. THE FIRST
   MUTATION, and it touches only the agent's own branch. On failure: `git rebase
   --abort` (best-effort), then restore the branch to its pre-rebase tip by
   compare-and-swap and say so in the error (QUM-1090). Only if the swap is
   *refused* is the caller asked to act.

   Note what the restore now rests on. `rebase --abort` returns branch, index and
   worktree together to `ORIG_HEAD`, which **is** the pre-rebase tip — so on the
   abort-succeeded path the CAS writes the value the ref already holds. That is
   not a pointless write: `RealGitRebaseAbort` swallows every error and always
   returns nil, so a partial abort is invisible to the caller and the CAS is its
   only detector. The pre-QUM-1087 argument here (about `reset --soft` preserving
   the index so the squash's tree matched) is **void** — there is no `reset
   --soft` and no squash — and must not be carried forward.
7. **Prove fast-forwardability**, before validation and therefore before
   anything expensive. Read the tip of the **ref** `git merge --ff-only` will
   resolve (not the worktree's HEAD — where those diverge, a HEAD-based check
   asserts a property of a different object than the merge acts on, which is
   QUM-1088's shape), then assert
   `merge-base --is-ancestor <parentTip> <rebasedTip>`. A false answer means the
   rebase did not produce a fast-forwardable branch: **surface it, do not
   reconcile it.** A git *failure* here is a different thing from a false answer
   and is reported as such.
8. **Validate.** Unless `--no-validate` or no `validate` command is configured
   (`.sprawl/config.yaml`), run the validate command **in the agent worktree, on
   the rebased tree**, streaming output to a log under `.sprawl/logs/` (path
   printed and checkpointed), bounded by `ValidateTimeout` (default 10 min).
   Post-rebase that tree is exactly what the parent would contain, so this is the
   same assertion the old design made, moved somewhere a red result costs
   nothing. **A failure has nothing to undo:** the parent has not been touched.
   The agent's branch is deliberately left rebased — a content-complete state to
   fix forward from.
9. **Fast-forward the parent.** `git merge --ff-only agentBranch` in the
   **parent worktree**. THE ONLY MUTATION OF THE PARENT, in the whole engine.
   A refusal here is *expected*, not anomalous: the normal cause is another merge
   landing during validation. The engine re-reads the parent tip to tell that
   case ("the parent moved from A to B during validation" — re-rebase and re-run)
   apart from "the rebase did not produce a fast-forwardable branch". Those have
   different causes and different remedies, so they carry distinct phrases and
   the tests assert each path emits its own and *not* the other's.
10. **Prove it was a pure ref move.** Re-read the parent tip and require exact
   equality with the rebased tip. `--ff-only` exiting 0 does **not** establish
   this: it exits 0 without moving the branch when already up to date, which is
   precisely what made the old S5b loss possible. Only the SHA equality
   discriminates that case, so it is asserted rather than implied. This also
   catches the agent branch moving during validation (a real window — the
   per-agent flock has no second taker), where the parent would otherwise
   silently receive more than what was validated.
11. **Poke.** Write a poke file telling the agent its branch was rebased and
   fast-forwarded, that nothing was squashed, and that its commits have new SHAs.

`Result.MergedTip` is the parent's tip after the fast-forward, read back from the
ref. It replaces `CommitHash`, which reported a squash hash captured *before* the
rebase and therefore named an object that existed on no ref.

Post-merge state contract (success): parent tip = the rebased agent branch tip,
exactly; the agent's individual commits are on the parent, with no squash commit
and no merge commit; agent branch points at that same tip; agent worktree clean.
The agent's **pre-rebase** commits are no longer reachable from any branch (the
rebase rewrote them), which is what the `/agent` recovery ref is for.

## The safety invariant

> After any merge or retire-with-merge — success, failure, or crash —
> every commit reachable from the parent branch before the operation is
> still reachable from the parent branch afterwards; on success the agent's
> delta is applied on top. No committed work anywhere becomes unreachable
> from all refs except the agent-branch REBASE rewrite described above, which is
> the operation's documented purpose — and which the `/agent` recovery ref covers.

The parent ref only ever moves **forward**, and after QUM-1087 there is **no
backward mutation of the parent anywhere in these paths** — not as a rollback,
not as a repair. That is enforced structurally rather than by policy:
`RealGitResetHard` and the `GitResetHard` seam were **deleted**, so the primitive
does not exist to acquire a caller. `internal/merge/parent_untouched_test.go`
asserts this two ways — a reflect check that the seam is absent, and a seam trace
that reports any *mutating* seam aimed at the parent worktree on every path.

### There is no rollback contract, by construction

The previous design mutated the parent **before** the tree was known good and
undid it with a blind `git reset --hard HEAD~1`. Both confirmed loss modes lived
in that window, and neither is patched here — both are structurally absent:

* **S5b — empty squash.** The agent's content was already upstream under a
  different SHA, so the rebase dropped it and `--ff-only` exited 0 **without
  moving the parent**. The rollback then rewound a **pre-existing parent
  commit**, making it unreachable from the parent. Measured, not hypothesised:
  against the pre-QUM-1087 engine this cost `main` a commit it had before the
  merge was invoked, while `git branch --contains` still reported the commit
  "reachable" via the agent branch — which is why the scenario harness needed a
  **parent-scoped** checker (`reachableFromParentBranch`), not just a
  branch-scoped one.
* **S5c — concurrent merge.** A second merge landed during the first's
  validation; the first's rollback removed the second's squash and left its own.
  Both results wrong, both messages lying. Now the parent moving during
  validation is a loud `--ff-only` refusal naming both SHAs, and the other
  merge's work is untouched.

Historical forensics (`.sprawl/logs/mcp-calls.jsonl`): 7 real validate-failure
rollbacks ran on this machine, and the concurrency S5c needs occurred 9 times.

A validate failure now performs **no repair at all** — it reports. The premerge
refs cover the attempt either way, and "the parent was never mutated" is a
stronger claim than "the parent was restored": a mutate-then-restore is
byte-identical at the end, so the scenario tests assert the parent's **reflog did
not grow**, which is the only witness that distinguishes the two.

## Locking model

Serialization is currently a property of the **caller**, not of the engine.
Anyone adding a new caller must know this table:

| Layer | Lock | Scope | Who gets it |
|---|---|---|---|
| Engine (`merge.Merge` step 2) | flock `.sprawl/locks/<agent>.lock` | **per-agent** — two merges of *different* agents do not contend | every caller |
| Supervisor (`Real.Merge`, `internal/supervisor/real.go` — `mergeSem`) | in-process semaphore, capacity 1 | per-sprawl-root, within one weave process | MCP `merge` tool |
| Supervisor (`Real.Retire(merge_first)`) | the same `mergeSem`, via `Real.merge` | per-sprawl-root | MCP `retire` tool — QUM-1088; it previously bypassed the semaphore entirely |

Consequences, as design facts:

* Two merges of different agents into the same parent are **only** serialized if
  both go through `Real.merge`. Both in-process paths now do: `Real.Retire`
  reaches the engine through it (QUM-1088 — it previously did not; 116 of ~570
  historical merges took the unserialised retire path). The **CLI** still runs in
  a separate process where the semaphore does not exist.
* An interleaved merge is now handled rather than dangerous: the second merge's
  `--ff-only` is refused loudly and its caller re-rebases. It is no longer able
  to rewind the first merge's work, because no path rewinds the parent at all.
* QUM-1089 tracks moving serialization into the engine (a per-root flock taken
  alongside the per-agent one) so it holds for the separate-process CLI too.
  Until it lands: **do not add an in-process caller that bypasses `Real.merge`.**
* **The per-agent flock has no second taker.** `grep -rn '"locks"'` finds only
  `merge.Merge`'s acquire and two `os.Remove` calls in retire; nothing on the
  agent side acquires it. So it serialises engine invocations against each other
  and does **not** stop a live agent committing mid-merge — a window that is as
  wide as a validate run now that validation happens in the agent's worktree.
  The post-ff SHA-equality check turns that from silent into loud (the merge
  fails rather than landing more than was validated), which is mitigation, not a
  fix. Tracked separately; `internal/agent/prompt_mode.go` used to tell every
  agent the lock made it "pause automatically during the rebase", which was
  false.

## Destructive git primitives

Every backward or history-rewriting mutation reachable from the merge and
retire paths, with its trigger. This is the checklist to re-verify when
changing any of this code.

| Primitive | Location | Target | Runs when |
|---|---|---|---|
| `git rebase <parent>` | `internal/merge/git.go` (`RealGitRebase`) | agent branch | Every non-noop merge (step 6). **The first mutation.** Rewrites the agent's commit SHAs; drops any commit whose patch is already upstream. |
| `git rebase --abort` | `internal/merge/git.go` (`RealGitRebaseAbort`) | agent worktree | Rebase failure. Best-effort, output discarded, **always returns nil** — so a partial abort is invisible and the CAS restore is its only detector. Returns branch/index/worktree to `ORIG_HEAD`. |
| `git merge --ff-only <rebasedTip>` | `internal/merge/git.go` (`RealGitFFMerge`) | parent branch | Step 9. Forward-only; cannot itself lose commits. Passed a **SHA, never a branch name** — a name re-resolves at ff time and could carry a tip nothing validated. **Exits 0 without moving when already up to date**, which is why step 10 asserts SHA equality rather than trusting this exit status. |

**`git reset --hard` on the parent is GONE (QUM-1087),** and so are `git reset
--soft` and `git commit`. `RealGitResetHard` and the `GitResetHard` seam were
deleted outright, so the rows that used to sit in this table have no code behind
them. Deleting the primitive is deliberately stronger than "no caller targets the
parent worktree": a seam that exists can silently acquire a caller, and an audit
of the call graph is a claim the next edit invalidates without saying so.
`internal/merge/parent_untouched_test.go` enforces both halves — `TestDeps_HasNoRollbackSeam`
(the seams do not exist) and a seam trace keyed on the target directory (nothing
mutating is aimed at the parent on any path).

| `git branch -D` | `internal/agentops/helpers.go:107-116` | agent branch | `retire --abandon`, after an unmerged-commit warning/confirmation. Force-delete: makes the branch's commits ref-unreachable. Note the MCP retire path auto-confirms (`yes=true`). |
| `git branch -d` | `internal/agentops/helpers.go:152-161` | agent branch | Retire after merge / of an already-merged branch. Safe by construction (refuses unmerged). |
| `git worktree remove [--force]` | `internal/agentops/helpers.go:118-131` via `internal/agent/retire.go` (`forceRemove = force \|\| dirty`) | agent worktree | Every retire. `--force` discards **uncommitted** files only. |
| `os.RemoveAll(.sprawl/agents/<name>/)` | `internal/state/state.go` (`DeleteAgent`) | non-git agent data | Every retire, unconditionally (QUM-1055 tracks the findings-loss consequence). |
| `git update-ref <ref> <sha>` | `internal/merge/git.go` (`RealGitUpdateRef`) | `refs/sprawl/premerge/**` | Every non-noop, non-dry-run merge, before the first mutation. Creates refs only; never moves a branch (QUM-1090). |
| `git update-ref <ref> <new> <old>` (CAS) | `internal/merge/git.go` (`RealGitUpdateRefCAS`) via `restoreAgentBranch` | agent branch | Rebase failure ONLY — `restoreAgentBranch` has exactly one caller (`rebaseFailureError`) since QUM-1087 removed the squash-commit-failure path. Restores the pre-**rebase** tip. Compare-and-swap: refuses rather than forcing if the ref moved. |
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

## Historical: the squash commit message (QUM-1105, retired at QUM-1087)

**The mechanism described below no longer exists.** QUM-1087 removed the squash,
so the engine composes no commit message and `buildMergeCommitMessage` and its
tests are deleted. The section is kept because every lesson in it still applies
to a **manual** squash — which is now where squashing lives — and to anyone who
proposes reintroducing one. Deleting the prose is how these get re-derived from
an incident.

The one that generalises furthest is not about formatting: `AgentState.LastReportMessage`
is a ≤160-char status ping written for a TUI line and updated asynchronously,
under no obligation to be current later. Reading it as a durable record was not a
formatting bug but a **contract** error, and it failed silently — the merge
reported success and the diff was correct. That finding survives the code.


(Historical, past tense throughout: none of this runs any more.) The squash
replaced the agent's commits with one commit, and the branch was
deleted at retire. **The agent's commit message is therefore the durable
record, and the squash is the only copy that survives.**

Precedence, in `buildMergeCommitMessage`:

1. `MessageOverride` (`--message` / `message:`) — was highest precedence. Both
   spellings are now REFUSED rather than ignored (`agentops.ErrMessageOverrideRetired`).
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

* **`git rebase` silently drops a commit that becomes empty.** If the agent's
  delta is already on the parent under different SHAs (cherry-pick, duplicate
  work, a content-equivalent squash landed elsewhere), non-interactive rebase
  drops the commit ("patch contents already upstream") and the branch can land
  exactly on the parent tip. Nothing fails. So "rebase succeeded" does not imply
  "the agent's commits still exist as commits".

  **The post-rebase no-op is NOT detected, deliberately** (QUM-1087 lists it as
  out of scope): if the rebase drops everything, `rebasedTip ==
  parentTipBeforeValidate`, the ff is a ref move to where the parent already is,
  step 10's equality holds, and the merge reports success having moved nothing.
  That is judged correct — the content IS on the parent, under other SHAs — but
  note it reads as an ordinary success, and `Result.MergedTip` is then the
  unchanged parent tip. Step 3's no-op check cannot see this case; it runs
  pre-rebase. An earlier version of this document claimed QUM-1087 "covers
  detecting the post-rebase no-op". It does not, and did not. What the engine
  does do is SAY so: it prints a NOTE that the parent did not move and why, and
  emits a `merge.post-rebase-noop` checkpoint, so a success with an unchanged
  parent tip is never left to be inferred. It deliberately does not set
  `Result.WasNoOp`, which would change callers' control flow.

* **`git merge --ff-only` exits 0 without moving when already up to date.** So
  "ff-merge succeeded" does not imply "the parent advanced". That was exactly the
  premise the old `HEAD~1` rollback rested on, and combined with the previous
  point it is how an already-upstream delta plus a validate failure rewound a
  commit the merge never added (S5b). Step 10's SHA equality exists because of
  this, and it is why the exit status is never trusted as evidence of a landing.

* **`git merge --ff-only <name>` re-resolves the name.** The engine passes the
  validated **SHA**, never `cfg.AgentBranch`. A name is resolved by git at ff
  time, and the agent's branch can move during validation — the per-agent flock
  has no second taker, and validation now runs in the agent's own worktree for as
  long as a validate takes. Merging the name would advance the parent onto a tip
  nothing validated, detectable only afterwards, with the parent already mutated.
  Pinned by `TestMergeSafety_AgentBranchMovesDuringValidation`.

* **A failed merge leaves the agent branch REBASED on the validate-failure and
  ff-failure paths, and that is deliberate on the first.** The rebase-failure path
  restores the pre-rebase tip itself by compare-and-swap (QUM-1090). The
  validate-failure path deliberately does not: the rebase is legitimate work and
  a content-complete state to fix forward from, and rewinding it would discard
  the rebase. On that path the agent's ORIGINAL commits are reachable only from
  the `/agent` recovery ref. Manual recovery after history rewriting is
  historically where damage happens (CLAUDE.md's QUM-1083 procedure), which is
  why the tool does the restore where it can and never prints a
  `git reset --hard` for a human to run.

* **The engine creates NO COMMIT, so it can never run the pre-commit hook — and
  that deletes a class rather than mitigating it.** Under the old squash, `git
  commit` was a subprocess that ran the hook, which ran `make validate`; a
  non-zero exit was ORDINARY, and the window between `reset --soft` and the
  commit was as wide as a validate run on every merge. It fired **twice in
  production on 2026-08-06**, on unrelated content (once a code branch, once a
  docs-only branch with a single markdown file) from two different triggers — a
  hook failure and an unrelated flaky test. Each time the agent's committed work
  ended up staged-but-uncommitted with the branch rewound, then reported as
  `has uncommitted changes in worktree`, which was true and misdescribed the
  cause. An earlier version of this document called it a "crash window" and
  "narrow (no subprocess between the two steps)"; both were wrong, and the label
  is what people read. With no commit there is no hook, no window, and no
  SIGKILL variant to be uncovered by. Asserted OUT-OF-SEAM by
  `TestMergeSafety_MergeInvokesNoCommitHook`, which installs a real hook that
  writes a sentinel — a Deps-level "we did not call GitCommit" would be satisfied
  by any commit path nobody thought of.

* **The rebase is merge-machinery (3-way), not patch application.** An agent
  based on a branch later squash-merged to the parent (the fan-out shape in
  CLAUDE.md under QUM-1083) rebases cleanly: identical changes on both sides are
  absorbed, not duplicated. Note the QUM-1083 hazard is a property of replaying
  commits onto a tree that already contains their content — which this engine now
  AVOIDS BY NOT SQUASHING, since the parent takes the agent's real commits and a
  downstream branch stays a genuine ancestor. An earlier version of this bullet
  said the hazard was "not of this engine's single-squash rebase"; the engine no
  longer squashes at all, so that framing is backwards.

## Recovery refs (QUM-1090)

Every non-noop, non-dry-run merge writes, before its first mutation (which is
now the rebase):

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

* Does the parent ref still only move forward, and only ONCE? There is no
  rollback to except any more. Prove it — do not assume ff implies advancement.
* Is the merge source resolved from the worktree HEAD (QUM-511 rule), not
  from `AgentState.Branch`?
* Is every caller of `merge.Merge` serialized against every other caller
  (see *Locking model*)?
* Re-derive the mandatory e2e rows from the CLAUDE.md touched-file table, as the
  UNION over every path in the diff. Do not take a row list from an issue or a
  commit message: `merge-reuse` names `internal/agentops/merge.go`,
  `internal/sprawlmcp/server.go` (`toolMerge`), `cmd/merge.go`,
  `supervisor.go` (`Merge`) and `real.go` (`Real.Merge`/`mergeFn`), and
  `internal/supervisor/*.go` / `internal/sprawlmcp/*.go` are GLOB rows a literal
  path grep will miss.
* If you add a destructive primitive, add it to the table above with its
  trigger.
* Does any new seam mutate the parent worktree? If so, stop: the parent has
  exactly one mutation (the ff-merge) and `parent_untouched_test.go` asserts it
  by seam trace, not by naming forbidden functions.
* Is the ff still proven by BOTH predicates — `--is-ancestor <parentTip>
  <rebasedTip>` before and exact SHA equality after? Neither alone is enough,
  and `--ff-only`'s exit status substitutes for neither.
* Which e2e matrix row does `internal/merge` sit under? `merge-reuse`, scoped
  (QUM-1154 resolved the gap this bullet used to report as open). It drives
  `merge.go` and `git.go` live, but runs `--no-validate`, so `runtests.go` and
  `validate_stream.go` are unreached and the premerge refs are written but not
  asserted. The real-git scenario tests in `scenario_test.go` are what stand in
  for the remainder; they are not optional.
* Is `git merge --ff-only` still passed the validated **SHA** rather than a
  branch name? A name re-resolves at ff time and can carry an unvalidated tip.
* Does the merge still create NO commit? If a commit reappears anywhere in the
  path, the pre-commit-hook failure class returns with it —
  `TestMergeSafety_MergeInvokesNoCommitHook` is the out-of-seam guard.
* (Retired with the squash, kept because it still applies to a MANUAL squash.)
  Can a squash commit message come only from an explicit override or the agent's
  own commits? Any third source, in any position, is the QUM-1105 defect — and it
  reports success while destroying the record.
