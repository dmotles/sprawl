# CLAUDE.md

Read `DESCRIPTION.md` for project context. This file covers how to work in this codebase.

## Terminology

- **agent** — a sprawl-spawned process with its own worktree and its own Claude session.
- **sub-agent** — a sprawl-spawned process that shares its parent's worktree (Arc Item #3 model). Persisted as `AgentState.Subagent`.
- **sidechain** — a Claude in-process `Agent`-tool spawn (Explore, Plan, Oracle, TDD agents). On the wire: `isSidechain: true` / `parent_tool_use_id != null`.

These three are distinct. "Sub-agent" must never refer to a Claude Agent-tool spawn — use "sidechain".

## Lifecycle model (QUM-786)

Authoritative rules for agent Status / `IsTerminal` / wake plumbing. If you
touch `internal/state/state.go`, `internal/supervisor/{runtime,real}.go`,
or any MCP verb that targets an agent by name, this is the contract.

- `StatusComplete` ("complete") is the **resting state after `state:complete`**
  — runtime torn down; `session_id` / `branch` / `worktree` preserved;
  **revivable**. It is **not terminal**.
- `IsTerminal(status)` returns true **only for `{retired, retiring}`**.
  Permanent termination is a deliberate parent action (`retire`/`kill`),
  never a side effect of the agent reporting complete. Everything else
  (`complete`, `paused`, `faulted`, `died`, `killed`, `resume_failed`) is
  revivable in spirit and the code must treat it that way.
- `StatusStopped` is **retired as a write target**; it is parsed only for
  legacy state files and migrated on load (`complete` if `LastReportState=
  complete`, else `faulted`).
- `delegate(complete-agent, task)` **auto-wakes** with no flag —
  `wake_if_offline` is not required and not consulted.
- `delegate(paused|faulted|died|killed|resume_failed agent, task)` requires
  explicit `wake_if_offline=true` and surfaces the canonical
  `"is <state> ... wake_if_offline"` error otherwise.
- `delegate(retired|retiring agent, task)` errors. The specific class
  depends on whether `state.json` still exists: `TerminalAgentError`
  (`"… no longer running"`) during the brief `retiring` window or for
  legacy zombies; `"agent %q not found"` once `retire` has deleted the
  state file (`internal/agent/retire.go:82`). Both are valid terminal
  signals — the contract is "delegate fails clearly," not a specific
  error string.
- `send_message` mirrors `delegate` for the gate logic.
- `wake` accepts everything **except `{retired, retiring}`**.

Touched-file matrix-row mapping for these set-sites lives in the table
under **Validating Changes** (`complete-lifecycle` row).

## Build & Test

```bash
make              # runs full validation, in order: build + proto-check + fmt-check
                  #   + lint + test-race-gate + test-race + wirelog-helpers-unit
                  #   + e2e-matrix-unit + gitignore-classes + leak-scan
                  #   (race-gate runs BEFORE test-race on purpose: it takes ~2s and
                  #    fails fast on exactly the regression that would make the
                  #    ~2min race run stop measuring anything)
make validate     # same as above — the default target
make build        # builds ./sprawl binary
make fmt          # auto-fix formatting
make fmt-check    # check formatting without fixing (used in CI/hooks)
make lint         # run golangci-lint
make test         # run all unit tests WITHOUT -race — a convenience run, NOT what validate uses
make test-race    # go test -race ./... — THE enforced gate; validate depends on this, not `test`
make test-race-gate  # shell unit test proving validate's go-test invocation still carries -race,
                     # and that -race really detects a planted race in this toolchain
make test-e2e-matrix-unit  # shell unit tests for the e2e matrix driver (fast, no claude)
make hooks        # install pre-commit hook

make test-wirelog-helpers-unit   # bash+jq unit tests for the e2e rows' wire-log
                                 # counter helpers; part of `make validate`

scripts/smoke-test-memory.sh   # integration test for weave memory system
scripts/sprawl-test-env.sh     # set up isolated test environment
```

### What `make validate` guarantees about data races (QUM-972)

**It runs the whole unit suite under the race detector.** Until QUM-972 it did
not — `validate` ran a bare `go test ./...`, so race detection was pure
convention, and live data races sat behind a permanently green `validate` in
**two** packages: `internal/backend` and `internal/rootinit`. One of the rootinit
races was a *production* defect: four concurrent unsynchronised writers to one
caller-supplied `io.Writer`. `validate` now depends on `test-race` **instead of**
`test`; there is no uninstrumented run.

**A race count is run-dependent — do not quote a bare total.** The detector
reports what it witnesses, so repeated runs disagree: pre-fix,
`internal/rootinit` reported **8** (six writer races plus two from the
`callOrder` append), while `internal/backend` reported **3** in three of four
runs and **2** in the fourth. State it as "races in two packages, count varying
by run" — the variance is itself the point (see the `-shuffle=on` follow-up: the
gate currently gives order-dependent races exactly one ordering). A commit
message in history (`4db5057`) quotes "9 races (backend 3, rootinit 6)"; that
rootinit figure omits the two `callOrder` reports.

State the guarantee accurately, because it is narrower than "no races exist":

* **Covered** — every package under `./...`, on the code paths the unit tests
  actually drive.
* **Not covered** — the e2e harnesses (`make test-e2e-matrix*`,
  `scripts/e2e-tests/*`), anything behind the `hub_e2e` / `sprawl_test` build
  tags, and any concurrent path no unit test exercises.
* A green run means **no race was *observed***. The detector reports races it
  witnesses on executed interleavings; it does not prove absence.

Cost, measured on a 4-core host with warm build caches (`-count=1`): `go test
./...` 99.0s vs `go test -race ./...` 122.2s — **+23%**, not the 2-10× the flag
usually costs, because this suite is sleep/timeout-bound rather than CPU-bound
(`internal/supervisor` alone is 75s of the 122s and barely moves under
instrumentation). A targeted concurrency-heavy subset was measured at 76.0s: it
saves 46s while covering 4 of ~40 packages and needs a hand-maintained list that
silently stops covering newly-concurrent packages, so it was rejected.

`-race` needs cgo and a C toolchain. That fails **loudly** — the build is
refused — so it cannot degrade into a false green. `make test-race-gate`
(also in `validate`) closes the two silent-regression paths that string-matching
alone cannot: it reads the wiring from `make -n validate` and asserts *every*
`go test` line carries `-race` (an env-var prefix such as `CGO_ENABLED=0 go test`
was a proven false-green before it keyed on invocation rather than line start),
and it re-runs validate's own extracted flags against a planted race plus a clean
control on **every** run, so "the detector is inert here" is caught rather than
assumed.

**Repo-wide convention for duration test tunables:** a duration knob that
production reads **from a goroutine** and tests override must be a synchronised
seam — the `atomicDuration` type, currently duplicated (deliberately, it is eight
lines and stays unexported) in `internal/backend/session.go`,
`internal/rootinit/consolidating_lock.go`, and `internal/merge/runtests.go`.
Never a plain `time.Duration` package var. This is repo-wide, not a
per-package exception: if you add a new one, use the same shape and the same
name.

Two of those three were **fixes**; the third was **prevention**, and the
distinction matters when reading the list. `internal/backend/session.go` and
`internal/rootinit/consolidating_lock.go` had races the detector actually
reported. `internal/merge/runtests.go` reported **zero** pre-fix races — no unit
test overrode its knob concurrently — and was converted because it is the same
shape one test away from the same defect. So `internal/merge` appearing here is
not evidence of a third racing package.

Snapshotting the var at goroutine entry does **not** fix it — the snapshot read
*is* the racing access. Do not write (or trust) a comment claiming otherwise;
QUM-972 deleted the ones `session.go` carried. Note also that a knob whose
override *happens* to be safe today because every caller writes it before
starting the reader is safe only by an **unstated precondition**; convert it
rather than documenting the precondition.

## Commit guard (QUM-808)

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

### Safe recovery from a wrong-tree commit on `main`

If a commit ever lands on `main` by mistake, **do NOT have an agent run
`git reset --hard` on `main`** — that can clobber weave's uncommitted state or
destroy work. Recover by re-homing the commit to the correct branch, then
moving `main`'s ref back without touching the working tree:

1. **Identify** the stray commit: `git -C <main-checkout> log --oneline -1 main`.
2. **Re-home it** to the owning agent's branch (cherry-pick preserves the
   commit; it does not mutate `main`):
   ```bash
   git -C <agent-worktree> cherry-pick <stray-sha>
   ```
   Verify it now exists on the agent branch.
3. **Rewind `main`'s ref** to the prior good commit using a *soft* reset (run by
   weave/root only, from the main checkout — keeps the working tree and index
   intact, only moves the branch pointer):
   ```bash
   git -C <main-checkout> reset --soft <prior-good-sha>
   ```
   Use `--soft` (or `git update-ref refs/heads/main <prior-good-sha>`), never
   `--hard`. Confirm `git -C <main-checkout> status` is clean and the stray
   commit is no longer an ancestor of `main`.

The guard makes this recovery a rare exception, not a routine: agents are
blocked from landing on `main` in the first place.

### Recovering a downstream branch after a squash-merge (QUM-1083)

**The precondition.** Squash-merging a base branch to `main` replaces its
commits with one new commit carrying a **different SHA**. A branch still based
on the *originals* now holds that content twice — once in its own history, once
in the squash — so `git rebase main` replays base commits onto a tree that
already contains them. This hits **any fan-out with a common base**, which is
the normal shape whenever two managers stack work on one tree. It is not
anyone's mistake; it is what squash-then-rebase does.

**Both natural checks lie, in opposite directions.** `git branch --contains
<original-base-tip>` does **not** list `main` after a squash-merge, even though
the content is fully present — the reading "not merged" and the reading
"merged, re-parented" have opposite correct responses. And the rebase itself
**may succeed, which proves nothing**: git drops a replayed commit on an exact
patch-id match (`skipped previously applied commit`) or when the replay empties
out (`patch contents already upstream`), so a single-commit base, or base
commits touching disjoint files, sail through. As soon as one base commit's
patch does not apply verbatim to the squashed tree — the normal case for a
branch that touches a file more than once — it conflicts, and it conflicts on
the *base* commits, not on yours.

**Prevent, don't recover.** When two branches share a base, either **merge the
dependent one first** or **rebase it onto the squash before merging the base**.
Best of all, don't base a branch on another branch that hasn't landed yet. The
procedure below is for when that was missed.

**Step 1 — gate on the base being content-equivalent.**

```bash
git diff <squash-commit-on-main> <original-base-tip>   # must be EMPTY
```

If it is not empty, **stop**: the squash changed content, and the downstream
delta is not safe to replay blind.

**Step 2 — cherry-pick the delta; do not rebase.**

```bash
git switch -c <my-branch>-rebased main
git cherry-pick -n <original-base-tip>..<my-tip>
```

The range excludes the already-landed commits **by construction**, where a
rebase includes them by construction. That difference is the whole reason this
works. On the clean path `-n` leaves nothing in progress and `git status` shows
an ordinary staged set, which is where step 3 happens.

**A conflict here does not mean step 1 failed.** Step 1 only establishes that
the squash matches the *original base*; it says nothing about commits `main`
acquired afterwards. You branched off `main`'s tip, so if later work touched
the same regions as your delta you get an ordinary cherry-pick conflict with
step 1 entirely correct — resolve it as such rather than re-auditing the gate.
A conflicted *range* cherry-pick leaves the sequencer populated
(`.git/sequencer`, no `CHERRY_PICK_HEAD`), so `git status` says `Cherry-pick
currently in progress` and branch switching is refused until you resolve it or
run `git cherry-pick --abort`.

**Step 3 — verify the delta, not the absence of conflicts.**

If you branched off the squash commit and `main` has not moved since, the tree
must match exactly:

```bash
git diff <my-tip>          # must be EMPTY
git status --short         # git diff cannot see untracked strays
```

If `main` has advanced past the squash, that diff reports `main`'s later
commits and is **not** a pass/fail predicate — compare the *deltas* instead
(QUM-1085):

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
`git add -A` — see below.)

**Step 4 — commit, then check the parent.**

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

**Check that the question the command answers is the question you are
claiming.** Both hazards above are one class — an asymmetric relation verified
in the convenient direction and reported in the desired one — and running the
command *more carefully* does not catch it, because the command is already
correct and its result already true. `git merge-base --is-ancestor main
<branch>` (step 4) asks *is `main` contained in my branch* — "I am rebased up to
date"; the reversed argument order asks *did my commits land on `main`*. Those
are different claims. Reread the argument order — or the subject of any check
you did not design — against the sentence you are about to write.

Finally, retire the original: once `<my-branch>-rebased` passes both checks,
point the merge at it, or `git branch -f <my-branch> <my-branch>-rebased` so
the name everyone else is using follows the recovered work.

### The merge engine mutates the parent once, forward-only (QUM-1087)

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

### Pre-merge recovery refs (QUM-1090)

Every non-noop, non-dry-run `merge` writes two refs **before its first
mutation** (now the rebase), so a failed or crashed merge is recoverable from a ref rather
than from the reflog:

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

`refs/sprawl/premerge/` is owned **exclusively** by this mechanism, so
anything under it is tool output by construction. That is load-bearing, not
tidiness: a hand-made ref once lived under this prefix, which meant the
obvious verification (`git for-each-ref refs/sprawl/premerge`, see output,
conclude it works) returned non-empty on a tree where the feature had never
run once. Put ad-hoc refs under `refs/sprawl/rescue/` or
`refs/sprawl/manual/`.

`sprawl gc` prunes these after `--premerge-retention-days` (default 14),
ageing them by the **timestamp in the ref name** — never by the commit date,
since a commit authored months ago and merged today is not an old *ref*. A
ref whose name does not parse is never pruned.

### Never overwrite the thing that tells you where you were

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

The incident is the point. Compressed to a maxim the rule reads as obvious
and gets routed around; attached to a case where it caught out people who
were paying close attention, it is a thing that actually happens.

### Never `git add -A` (QUM-989)

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

The two `main` guards above do not help here: this is a **correct-branch,
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

## Install

> **Warning:** Do not run `make install` unless your agent identity is `weave` or the user explicitly asks you to. Other agents should only use `make build`, then test against the locally built `./sprawl` binary using temporary directories with overridden environment variables (e.g. `SPRAWL_ROOT`, `SPRAWL_AGENT_IDENTITY`) to exercise the tool.

## Running `claude` from agent bash subshells (QUM-518)

When an agent invokes `claude -p ...` from a Bash tool subshell, Claude Code
sanitizes the subprocess env and strips `CLAUDE_CODE_OAUTH_TOKEN`. The inner
`claude` then fails with `Not logged in`. The fix is a thin shell shim that
re-hydrates auth env vars before exec'ing the real binary.

**Setup (one-time, host side):**

1. Create `.env` at the repo root containing your auth token(s):

   ```
   CLAUDE_CODE_OAUTH_TOKEN=...
   ANTHROPIC_API_KEY=...     # optional
   ```

   Then `chmod 0600 .env`. **`.env` is gitignored — never commit it.**

2. Launch sprawl with the shim as `$SPRAWL_CLAUDE`:

   ```bash
   SPRAWL_CLAUDE=$(pwd)/scripts/run-claude sprawl enter
   ```

`scripts/run-claude` sources `$SPRAWL_ROOT/.env` (falling back to the script's
parent dir if `$SPRAWL_ROOT` is unset) and then `exec`s `claude`. The
`worktree.setup` hook in `.sprawl/config.yaml` copies `.env` into each new
agent worktree (preserving `0600` mode via `cp -p`) so the shim works from
inside worktrees too.

`internal/agent/claude.go` honors `$SPRAWL_CLAUDE`: if set, it is used
verbatim as the `claude` binary path; otherwise it falls back to a `PATH`
lookup.

## tmux safety (QUM-325)

> **Never run bare `tmux kill-server`.** Sandbox scripts now use a dedicated tmux socket via `SPRAWL_TMUX_SOCKET` (QUM-325), so sandbox operations are isolated from the user's default tmux server. Production sessions still share the default socket.
>
> To clear sandbox state, use the sanctioned `sprawl_sandbox_destroy` helper (from `scripts/sprawl-test-env.sh`) or the `_stmux kill-session -t $SPRAWL_NAMESPACE` wrapper — both target only the sandbox session on the sandbox socket. In scripts, always use `_stmux` (not bare `tmux`) for sandbox tmux operations.

## `/tmp` hygiene — hard rules

Sandbox roots live under `/tmp`, but `/tmp` is **shared** with other agents and
with host tooling. These rules are not advisory:

- **Never `rm -rf` a broad `/tmp` glob** (`/tmp/*`, `/tmp/sprawl-*`, `$TMPDIR/*`,
  …). It destroys other agents' in-flight sandboxes and host state.
- **Only remove a sandbox root you created**, and only after asserting the path
  is under `/tmp/` and matches the prefix you expect — assert, then delete. See
  `_e2e_cleanup` in `scripts/lib/e2e-common.sh` and `_unit_reset_markers` in
  `scripts/test-e2e-matrix-unit.sh` for the pattern (a `case` guard on the
  literal path, and `find -delete` rather than a `rm` glob).
- **Never touch `/tmp/coder-script-data`.** It is host tooling state. In this
  workspace `/tmp/coder-script-data/bin/claude` is a **symlink** into the
  developer's home dir, where the real binary lives on the persistent volume —
  so deleting it breaks `claude` PATH resolution rather than the installation,
  and recovery is a single `ln -s`. The hazard is not the blast radius, it is
  the silence: nothing in the harness would *tell* you, and every
  `needs_claude` e2e row would quietly start skipping.

## Text selection in `sprawl enter` (QUM-653 / QUM-731)

The TUI captures the mouse so the scroll wheel scrolls the chat viewport
(QUM-731). Mouse capture intercepts plain click-drag, so use one of the
terminal- or tmux-native paths below to select and copy — none require a
modal toggle (the QUM-617 selection-mode toggle stays retired):

* **Shift+drag** — most terminals (xterm.js / coder web terminal, gnome-
  terminal, kitty, wezterm, Alacritty, iTerm2) bypass mouse capture while
  Shift is held; copy with your usual keystroke (Cmd+C / Ctrl+Shift+C).
* **tmux copy-mode** (`prefix` + `[`) — scroll, search, and yank tmux-style.
  Works regardless of terminal.
* **Right-click → Copy** — in most terminals the right-click context menu
  copies the OS-level selection even with mouse capture on.

Scroll inside the TUI:

* **Mouse wheel** — scrolls the observed chat viewport up/down (suppressed
  while a modal — `/help`, palette, confirm, question, validate-popup — is
  open).
* `PgUp` / `PgDn` — page up/down
* `Home` / `End` — jump to top/bottom
* `Up` / `Down` — navigate prompt input history **when the input is empty**
  (or while a history walk is already in progress); no-op when freshly
  typing. `PgUp` / `PgDn` / mouse wheel scroll the chat viewport regardless
  of input state.

### Incident snapshot hotkey (QUM-728)

Press `Ctrl+\` to write a forensic bundle to
`<repoRoot>/.sprawl/incidents/<ISO8601>-tui-snapshot/`. Includes:
goroutine dump, fd list, sprawl status, `ps auxf`, `/proc/<pid>/status`
for weave, last 10k mcp-calls.jsonl lines, per-agent activity rates,
memory + loadavg. Non-blocking — TUI stays interactive. Status bar shows
`snapshot saved → <path>` on completion (or `snapshot failed` + an error
toast on failure).

### Runtime pprof toggle (QUM-678 / QUM-934)

`--pprof <addr>` (or `SPRAWL_PPROF_ADDR`) exposes `net/http/pprof` at launch.
**`SIGUSR2` toggles the listener on a running session** — no relaunch, which is
the point: restarting resolves some session-scoped perf bugs and so destroys
the evidence. (`SIGUSR1` is the separate sigdump goroutine/fd dump.)

Bind-failure policy differs by **provenance**, deliberately — don't merge the
two branches:

* **Explicitly configured** (`--pprof` / `SPRAWL_PPROF_ADDR` / an explicit arg):
  bound as-is or fails loudly. Never silently relocated — an operator who named
  a port will curl that port.
* **Unconfigured** (our own `127.0.0.1:6060` default, which nobody asked for):
  tries the default, then falls back to an ephemeral `127.0.0.1:0` on
  `EADDRINUSE`. Loopback only, and only `EADDRINUSE` relocates.

While the listener is up, its **bound address is written to
`<SPRAWL_ROOT>/.sprawl/runtime/pprof-addr`** and removed on stop, so
`curl http://$(cat .sprawl/runtime/pprof-addr)/debug/pprof/` works even when the
fallback picked an ephemeral port. The toggle's log line only reaches
`.sprawl/logs/tui-stderr-*.log` (the TUI redirects stderr), so this file is the
discoverable surface; an in-TUI surface is still deferred. The file is advisory
— written only after the weave flock is held, and cleared at launch, so a
SIGKILLed session's stale entry cannot mislead the next one.

## Project Configuration

Sprawl reads `.sprawl/config.yaml` for project-level settings:

```yaml
validate: "make validate"   # command run on the rebased tree to validate a merge
```

Since QUM-1087 this is **not** post-merge validation: the engine rebases the
agent's branch, runs this command on the rebased tree **in the agent's own
worktree**, and only fast-forwards the parent if it passes. A failure leaves the
parent's SHA byte-identical. If no config file exists or the `validate` key is
absent, validation is skipped with a warning. Use `--no-validate` on `sprawl
merge` to explicitly skip it.

## Repo Layout

- `cmd/` — CLI commands (cobra). Each command has its own file + test file.
- `internal/agent/` — Claude Code launcher, agent name allocation, prompt building
- Agent types: `engineer` (writes code), `researcher` (investigates, writes findings), `manager` (orchestrates), `qa` (verifies an engineer's work against ACs).
- `internal/config/` — Project configuration loading (`.sprawl/config.yaml`)
- `internal/supervisor/` — same-process child runtime registry and orchestration
- `internal/state/` — Agent state persistence (JSON files in `.sprawl/agents/`)
- `internal/worktree/` — Git worktree creation for agent isolation

## Meta: Developing Sprawl Inside Sprawl

This repo IS Sprawl. The `.sprawl/` directory at the repo root stores agent state and worktrees. If you're an agent working on this codebase, you are running inside the system you're building. Don't mess with `.sprawl/` contents unless that's your task.

## Code Patterns

**Dependency injection**: Commands use a `deps` struct to inject interfaces for external dependencies (backend processes, git, env vars, filesystem). See `cmd/gc.go` or `cmd/usage.go` for the command-local shape. Agent operations keep theirs in `internal/agentops` as exported `XxxDeps` with nil-defaulting accessors (`internal/agentops/report.go`), which the command aliases — `cmd/merge.go` is `type mergeDeps = agentops.MergeDeps`. This enables testing without real subprocesses.

**Tests required**: Every file in `cmd/` and `internal/` has a corresponding `_test.go`. Keep it that way. **Read `/testing-practices` before writing any tests for the first time** — it covers the dependency injection pattern, mock conventions, and common pitfalls.

**Every new assertion must demonstrate it CAN fail** — a negative control, a mutation, or a red-first run — and you must record which one and what it printed; an assertion nobody has watched fail is a claim, not a check. Any harness that aggregates its own results needs an **assertion-count floor**, so a run reporting `0 passed / 0 failed` exits non-zero instead of green (worked example: `scripts/test-wirelog-helpers-unit.sh`). A **parent-commit** control proves a failure is *pre-existing*, never that it is *acceptable*; read `/testing-practices` § **Assertion Rigor** before writing or reviewing any assertion.

**A watched failure proves the instrument works, not that it measures the right thing.** Red-first is necessary and **not sufficient**. An assertion can fail for a reason you chose, on behaviour the correct design does not have — and in the transcript that is indistinguishable from one that caught something real. Two instances from the QUM-1105/QUM-1087 series, both by the same author, days apart:

* An assertion written against the derived squash message's trailer block was watched failing, and what it pinned was **a blank line the correct design does not emit**. The failure was genuine, the instrument worked, and the measurement was of nothing.
* An argument-order assertion (`merge-base --is-ancestor <parent> <branch>`) was watched failing red-first, and its comment then claimed a swap "leaves every other assertion green" — inferring, from the one red it had seen, that it was the *only* guard. The negative control refuted that: swapping the arguments also failed four real-git scenario tests, because post-rebase the parent is a strict ancestor and the reversed question answers false. The claim in the comment was false while every individual observation behind it was true.

So after watching red, state separately **what the assertion would let through**, and prefer a control that mutates the **production** behaviour you care about over one that mutates the test. The sharpest form is a mutation that leaves every other assertion green: if exactly one test fails, that test is the one carrying the claim. **Write the prediction down before running the control** — the second instance above was caught only because the prediction was recorded and turned out not to match, and a prediction formed after seeing the output cannot fail to match. If nothing fails, the claim is unguarded no matter how much red you have already seen.

**No fallback branch may silently succeed (QUM-997).** Any validation or test script must exit non-zero when something it checks actually fails. The shape to know by sight is `cond && ok "…" || <arm that neither counts a failure nor fails the run>` — and its if/else twin, `if cond; then pass; else <something that isn't fail>; fi`, including a missing `else`. A skip on an unmet precondition must exit **77**, never 0. A harness using `set +e` (to report all failures rather than the first) has given up the mechanism that makes an early death loud and therefore **must** carry an assertion-count floor. `/testing-practices` § **The non-asserting fallback** has both spellings, the corollaries, and the audit that found six live instances of this class — five of them structural rather than lexical, two inside `make validate`, one of which printed `40 PASS / 15 FAIL / exit 0`. It also records that a deterministic parser for this class was **built and rejected**: it acquired four separate blind spots of the same class it detected, one blinding 462 lines across 5 harnesses while every aggregate counter stayed byte-identical. Do not rebuild it; the defence is manual review against that checklist.

**Read `/go-cli-best-practices` before writing or modifying Go code** — it covers cobra patterns, error handling conventions, and dependency injection structure used throughout this codebase.

**Read `/cli-ux-best-practices` before adding or modifying any CLI command's behavior** — it covers output design for agent consumers, the "next action hint" pattern, error message design, and idempotency. Every command must tell the calling agent what to do next.


## Public vs Private Repo Hygiene

Before any commit, merge, or PR, determine whether the current repo is public or private:

- `git remote get-url origin` → if hosted on a public namespace (github.com/<user-or-org>/...) and the upstream is public (check `gh repo view --json visibility 2>/dev/null` if available), treat as PUBLIC. Default assumption: PUBLIC unless you can confirm otherwise.
- A repo named after, owned by, or branded with a company is not by itself proof of privacy. Check the actual visibility.

For PUBLIC repos:
- Do NOT commit content that names or describes the user's employer's internal systems, products, codenames, repo names, host aliases, customer names, internal URLs, deployment topology, or operational specifics.
- As you build context on the user across sessions, you may learn their employer or current company. Use that knowledge to filter: anything that references that employer's internal context goes in `.sprawl/agents/<name>/findings/` (gitignored), NOT in the tracked tree.
- Forensic/debug/incident artifacts captured from real production systems (logs, paths, session IDs, tool runs against real hosts) are especially likely to contain leakable context. Default to gitignored unless explicitly sanitized.
- When in doubt, ask the user before committing.

For PRIVATE repos:
- Less strict but still avoid mixing one employer's internal context into another's repo.

This applies to all agents (engineers, researchers, QA, managers). Reviewers must flag suspected leaks during code review and refuse to merge until resolved.
## Linear Issue Tracking

This project tracks work in Linear. See `CLAUDE.local.md` for workspace-specific configuration (team name, issue prefix).

When creating, managing, or querying issues, **invoke the `/linear-issues` skill via the Skill tool first** — do not rely on remembered conventions. The skill defines required fields (label, milestone, state) that are easy to miss otherwise.

**Issue lifecycle** — if you are working on a Linear issue:
1. **Start**: Set the issue state to "In Progress" via `save_issue`. Add a comment via `save_comment` noting you're picking it up (include your agent name/identity if you have one).
2. **Progress**: As you work, post comments on the issue with notable findings, decisions, or blockers. Keep the issue thread as a living log — especially for research or investigation tasks. Don't let useful context stay only in your head.
3. **Finish**: Set the issue state to "Done" via `save_issue`. Add a comment summarizing what was done, linking to any relevant commits or PRs.

## Spawning Agents

When spawning an agent to work on a Linear issue, keep the prompt short. Point the agent at the issue — don't repeat the issue contents in the prompt. See `CLAUDE.local.md` for the team prefix to use in branch names.

The issue is the source of truth. The agent can read it via Linear MCP tools (`get_issue`).

## Session Handoff

At the end of a session, use `/handoff` to persist context for the next session. It guides you through writing a structured summary and calling the `handoff` MCP tool.

## Sandbox Testing

Use the `/e2e-testing-sandboxing` skill for the full setup, inspection, and cleanup workflow. Quick start:

```bash
make build
eval "$(bash scripts/sprawl-test-env.sh)"
```

## Linting & Formatting

This project uses [golangci-lint v2](https://golangci-lint.run/) with `gofumpt` formatting. Configuration is in `.golangci.yml`.

* **All code must pass** `make validate` before committing. The pre-commit hook enforces this.
* Run `make fmt` to auto-fix formatting issues.
* Run `make hooks` after cloning to install the pre-commit hook.

## Validating Changes

1. `make validate` — full pipeline: build, fmt-check, lint, test
2. Manual smoke test: run the built `./sprawl` binary with relevant commands
3. For end-to-end validation, use the `/e2e-testing-sandboxing` skill to set up a sandbox environment
4. For TUI changes, read `/tui-testing` for the E2E validation harness and manual testing workflow. TUI validation is mandatory for all TUI-related changes.
5. **Mandatory-test e2e harness.** When you touch any file listed in the table below, run `make test-e2e-matrix-<row>` for the corresponding row (or `make test-e2e-matrix` to run all rows).

   **Derive the row set from the table; never from a list someone handed you (QUM-1081).** The obligation is *every* row whose named files or functions your diff touches — the **union** of both greps over the table as it stands at the commit you are making. Take `git diff --name-only`, grep the table for each path, then grep again for the functions you edited. A row's function list tells you *why* it covers you; it never narrows a path match. And a literal path grep will not match the eight files-column entries that are **globs** rather than literal paths (`internal/supervisor/*.go` and similar) — a grep for `runtime_launcher.go` misses the row that covers it via `internal/supervisor/*.go` — so check the glob rows by hand. **A green run against the wrong rows is indistinguishable from coverage.**

   Corollaries of the union rule, each of which is enough on its own to produce a wrong row set:

   * **The obligation is a property of the commit, not of one file in it.** Derive over every path in `git diff --name-only`, including a delta that is comment-only. A change of any size to a second file brings in that file's rows.
   * **A bare function name does not identify a row.** The same name can appear in more than one file under different rows — `drainPendingToStdin` exists in both `weave_handle.go` and `runtime_launcher.go`, under different rows. Match on path first, then on symbol.
   * **Symbol scopes are annotative, not exhaustive.** A row's function list says *why* the row covers a file, not every function it reaches. Treating it as a closed set lets an omission in the table silently shrink an obligation.
   * **Obligation and coverage are different questions — answer the obligation first.** You may afterwards reason about which of the owed rows could plausibly catch a given defect, but that analysis never removes a row from the run. A reader cannot tell a verified narrowing from a careless one. The asymmetry settles it: over-running costs a CI slot, under-running ships the defect **and comes back green either way**.
   * **A row named as the *only* live coverage of a path is never optional.** Where the table says in bold that one row is the sole live coverage of some behaviour (e.g. `notif-stacked-restart` for `weave_handle.go`'s `runInboxRedrainTicker` / `weaveInboxRedrainInterval` / `drainPendingToStdin` redrain path — the file's other rows exercise the poke path instead), every other row can come back green while that behaviour goes untested.

   Deriving mechanically also makes a *gap* in the table a checkable claim about the table rather than an unfalsifiable one about the author. One such gap is open today: per QUM-1073, no row delivers an **async** message to a **busy child**.

   **Writing an issue or a brief? State the rule, not the row list.** An implementer cannot tell an authoritative hard-coded list from a careless one, so a list in an AC silently substitutes your reading of the table for theirs. Cite the table and the rule; if you must name a row, name it as an example, not as the set.

   **Multi-row invocation is supported (QUM-947).** Several rows in the table below instruct you to re-run additional rows; run them in one shot by calling the driver directly with N row names:

   ```bash
   bash scripts/e2e-matrix.sh recall-sendnow tui-live-render drain-row-inject
   ```

   The summary reports `passed/requested`, where the denominator is **the number of rows you named on the command line** — it can never be smaller than what you asked for. The driver also echoes `=== Matrix: running N row(s): ... ===` before starting, so a wrong selection is visible immediately. Rules: an unknown or malformed row name is rejected with exit 2 **before any row runs** (fail fast, and every bad name is reported, so one re-run fixes them all); `all` and `--list` must each be the only argument; duplicate names run twice by design, because deduplicating would shrink the denominator below the request. Note `make test-e2e-matrix-<row>` takes exactly one row — `make` parses `make test-e2e-matrix-a b c` as three separate goals — so use the direct `bash scripts/e2e-matrix.sh` form for multi-row runs.

   > **Reading older transcripts:** before QUM-947 the driver silently discarded every argument after the first and printed `Matrix: 1/1 passed`. Any historical multi-row invocation that "passed" proved only that its *first* row ran. Do not trust such a claim.

   The driver's own arg parsing, fail-fast validation, and summary arithmetic are unit-tested by `make test-e2e-matrix-unit` (`scripts/test-e2e-matrix-unit.sh`) — pure shell, no `claude` or `tmux` needed. It runs as part of `make validate`, because a regression test guarding a false-green is worthless if it only runs when someone remembers. Runtime is ~4.5s standalone (more under `make validate`'s parallel load), most of it section `[16]`, which re-runs the whole suite once per driver debug seam with that seam exported and demands an identical verdict and identical coverage — the suite's own verdict must not depend on the environment that invoked it. Each seam registered in `UNIT_SCRUBBED_VARS` adds ~1.2s. An exported `SPRAWL_E2E_MATRIX_DEBUG_*` is scrubbed out of the suite's environment and cannot change the verdict (silently, by design); an exported `UNIT_NESTED_SEAM_CHECK` **fails** the run rather than quietly disabling `[16]`.

   All rows require a real, **authenticated** `claude` binary on PATH. `SPRAWL_E2E_SKIP_NO_CLAUDE=1` turns a missing `claude` from a hard `FATAL` into a **skip** — and a skip is accounted separately from a pass (QUM-952).

   **The gate keys on presence only — it never probes auth.** All 11 `needs_claude` gates read the flag *inside* the binary-absent branch, so there are three states, not two:

   | claude state | gate fires? | `SPRAWL_E2E_SKIP_NO_CLAUDE` | outcome |
   |---|---|---|---|
   | absent | yes | consulted | row is **skipped** — nothing asserted, exit 3 |
   | present, **unauthenticated** | no | **never read; inert** | row runs and fails with `Not logged in` |
   | present + authenticated | no | n/a | real run |

   The middle state is a **misdiagnosis hazard, not a false green**: the row fails with a Session Error whose body is `Not logged in`, which is trivially misread as a product regression. If you see `Not logged in`, fix auth — see the `scripts/run-claude` shim and `.env` above; the flag is **not** the remedy, because the gate it controls never fires in that state. And **never hide `claude` from PATH to force a skip.** That converts the middle state into the absent state, and all it buys you is a vacuous all-skip run that asserts nothing. QUM-974 tracks the related defect that `e2e_recover_oauth_token` reports success even when it recovers no token.

   **Skip accounting (QUM-952).** A skipped row is reported as `SKIP <row>`, never `PASS`, and forces a nonzero exit — **exit 3** when rows skipped but none outright failed. Two summary lines are printed:

   ```
   === Matrix: 2/3 passed ===
   === Matrix breakdown: 2 passed, 0 failed, 1 skipped / 3 requested ===
   ```

   The first line is the QUM-947 contract and is unchanged — `passed` means *actually executed and passed*, so a skip now shows up there as a shortfall. Note that **`=== Matrix: ` is not a unique prefix**: the selection banner (`=== Matrix: running N row(s): …`) and the failed-rows / skipped-rows lines share it. If you scrape, anchor on `^=== Matrix: [0-9]+/[0-9]+ passed ===$` (exactly one per run) or `^=== Matrix breakdown: `. The breakdown line is the only place the skip count appears, and `passed + failed + skipped == requested` always (a violation is an internal error, exit 4, printed *instead of* any summary). Skipped rows are additionally named on stderr in a `!!! … SKIPPED` banner with each row's reason.

   Driver exit codes: `0` every requested row executed and passed · `1` ≥1 row failed (dominates skips) · `2` usage/argument error, nothing ran · `3` ≥1 row skipped, none failed · `4` internal invariant violation. `77` is reserved as an individual *row's* skip signal (the autotools convention) and is never the driver's own exit status.

   **A skipped row does not discharge a mandatory-gate obligation.** If the touched-file table below sends you to a row and that row skips, the row is **not** validated — say so plainly rather than citing a green-looking run. That is why any skip exits nonzero: the exit status is the only signal `make` and non-reading callers see, and `SPRAWL_E2E_SKIP_NO_CLAUDE=1` acknowledges the *diagnostic*, not the *obligation*. Do not add `|| true` or a `-` prefix to the `test-e2e-matrix*` Makefile recipes to work around a nonzero skip — that would re-hide real failures too.

   Two known remaining gaps (tracked as QUM-970 and QUM-969), so a green run is not over-read: (a) some rows echo a **partial** `SKIP:` for individual phases and still report the row `PASS` — `wake-live` S3, `liveness-transitions`, `pause-lifecycle` — and those are not counted anywhere, so scan row output for `SKIP:` lines even on a pass; (b) the legacy `scripts/test-*-e2e.sh` fallback scripts mostly carry their own inline `exit 0` skip and still false-green. `scripts/test-subagent-model-e2e.sh` is the exception — it shares the fixed helper, so running it directly (`bash scripts/test-subagent-model-e2e.sh`; it has no `make` target) now exits **77**, which means **"skipped, asserted nothing"**, not "crashed".

   > **Reading older transcripts:** before QUM-952 a skipped row was reported as `PASS` and exited 0, with no skipped bucket at all — so `SPRAWL_E2E_SKIP_NO_CLAUDE=1` with no `claude` on PATH printed a fully green `Matrix: N/N passed` while asserting nothing. **A skip proves nothing — never cite a skipped run as evidence a row passed**, and treat any historical green matrix summary from an environment without `claude` as vacuous. The `wake-live` row requires the `sprawl_test` build tag — the driver (`scripts/e2e-matrix.sh`) handles this automatically via `needs_build_tags=sprawl_test`. The original per-test Makefile targets (`make test-notify-tui-e2e`, `make test-handoff-e2e`, `make test-merge-reuse-e2e`, `make test-ask-user-question-e2e`, `make test-drain-row-inject-e2e`, `make test-paste-coalesce-e2e`, `make test-wake-live-e2e`) and their underlying `scripts/test-*-e2e.sh` scripts remain available as a fallback during the soak period; they will be removed in a follow-up issue once the matrix rows have proven flake-free for a few days.

   **Relaunch waits for `weave.lock`, it does not sleep (QUM-948).** Every
   `e2e_launch_tui` first blocks until `<SPRAWL_ROOT>/.sprawl/memory/weave.lock`
   is acquirable, before `tmux new-session`. This is load-bearing for any row
   that kills and relaunches a session on the same root: `tmux kill-session`
   only signals the pane, while the flock is released by the kernel when the
   dying process's fd closes — so a fixed sleep races teardown and `sprawl
   enter` dies with `another weave session is already running`. Tune the
   deadline with **`SPRAWL_E2E_LOCK_WAIT_SECS`** (default **30**); a
   non-numeric value warns and falls back to 30 rather than aborting the row.
   A lock that outlives the deadline **fails the row** (`FAIL: weave.lock not
   released within the …s deadline`) plus a holder diagnostic — it never hangs
   and never passes, because a lock still held at that point is a leak, not
   slow teardown. Covered by `make test-e2e-lockwait-unit`
   (`scripts/test-e2e-lockwait-unit.sh` — pure shell, needs `flock(1)`, no
   `claude`/`tmux`).

   **This table is prose, so a refactor that moves code between files silently
   relocates it out from under its gates and nothing in the pipeline notices**
   (QUM-1084: QUM-1060 moved 443 lines of drain logic into
   `internal/supervisor/drain.go`, leaving three-line wrappers behind, and the
   rows that gate that behaviour still *looked* correct). When you move
   code, re-derive the rows for its **new** path, not just the old one.

   **And when you audit this table, audit the category, not the predicted
   instance.** A prediction handed to you as a target narrows the search to
   itself: a sweep briefed to confirm one named dead entry can establish that
   its named entry was never in the table, miss the dead entries that are, and
   report clean. Audit every entry against the tree.

   When the glob check above turns your file up, note **a glob hit means
   *incidentally listed*, not *substantively gated*** — `internal/supervisor/*.go`
   put `drain.go` under `handoff` without anyone deciding that handoff covers the
   drain. That is a statement about **coverage**, not about what you owe: it never
   shrinks the union, it only tells you which rows could plausibly catch you. Same
   for any script you write here — **it is a candidate generator for coverage
   analysis, never an oracle for an obligation.** An obligation needs no helper;
   you run the union.

   Two cautions when counting rows this way. **A document that cites a count over
   itself is self-falsifying by construction** — these paragraphs live inside the
   corpus they describe, so a whole-file grep matches the prose too (including
   this sentence). Cite the rule; treat any figure as an illustration at a stated
   commit, and anchor it to table rows rather than `grep -c` output:
   `grep -E '^   \| ' CLAUDE.md | grep -cE ...`.
   And globs are not the only entries a literal path grep misses:
   `internal/supervisor/liveness/` and `.claude/agents/` are directory prefixes
   in the files column, matched by neither a path grep nor a glob grep. And
   because `*` crosses `/` in both bash pattern-matching and git pathspec,
   **treat a glob row as matching unless you have read the row and it clearly
   does not apply — when in doubt, include it**; `internal/supervisor/*.go`
   sweeping in nested packages like `liveness/` is over-inclusion, which is the
   direction to fail in. The narrow reading is mechanically defensible — shell
   expansion really does not cross `/` — and it is still the wrong call here:
   when a mechanism and a fail-safe direction disagree, take the direction that
   fails safely.

   | files touched | matrix row | guards |
   |---|---|---|
   | `cmd/enter.go`, `cmd/enter_notify.go`, `internal/tui/app.go`, `internal/tui/messages.go`, or `internal/tui/tree.go` | `notify-tui` | QUM-311/QUM-312 |
   | `cmd/enter.go`, `internal/supervisor/*.go`, `internal/sprawlmcp/*.go`, `internal/rootinit/postrun.go`, or `internal/tui/app.go`'s `HandoffRequestedMsg`/`SessionRestartingMsg`/`RestartSessionMsg` handlers | `handoff` | QUM-329 |
   | `internal/agentops/merge.go`, `internal/sprawlmcp/server.go` (`toolMerge`), `cmd/merge.go`, `internal/supervisor/supervisor.go` (`Merge`), or `internal/supervisor/real.go` (`Real.Merge` / `mergeFn`) | `merge-reuse` | QUM-511/QUM-489 |
   | `internal/supervisor/question.go`, `internal/supervisor/question_real.go`, `internal/supervisor/real.go` (`RegisterRootRuntime` — QUM-535 root-type persistence; `Real.Wake` proactive `cancelByAgent` — QUM-611/QUM-724), `internal/sprawlmcp/server.go` (`toolAskUserQuestion` + eligibility gate), `internal/sprawlmcp/tools.go` (`ask_user_question` schema), `internal/tui/question.go`, `internal/tui/messages.go` (`DismissQuestionMsg.Hard` — QUM-611), `internal/tui/app.go` (question modal + `Ctrl-Q` binding + `View()` composition for `showQuestion` + `DismissQuestionMsg` cancel path — QUM-611), `internal/tui/statusbar.go` (`SetPendingQuestions` / `SetQuestionModalHidden` — QUM-611), or `cmd/enter.go` (TUI question consumer registration + `QuestionsChanged` forwarder goroutine) | `ask-user-question` | QUM-527/QUM-535/QUM-611 |
   | `internal/messages/messages.go`, `internal/runtime/unified.go` (QUM-817: `writeMessage`/`WriteSystemMessage`/`markConsumed`/`Outstanding` — the deleted `queue.go` is gone), `internal/agentloop/session_spec.go` (`ReplayUserMessages` — the isReplay echo is what renders the drain row), `internal/supervisor/weave_handle.go`, `internal/supervisor/runtime.go`, `internal/supervisor/runtime_launcher.go` (`drainPendingToStdin`/`feedTasks`), `internal/supervisor/sweep_coordinator.go` (`OnDelivered` — QUM-1084: the table filed this under `runtime_launcher.go`, but it is defined here), **`internal/supervisor/drain.go`** (QUM-1084 — QUM-1060 moved the whole inbox→stdin drain here; `runtime_launcher.go`/`weave_handle.go` keep only three-line wrappers. `runDrain` = read/build/write, `readInboxSnapshot` = the maildir peek + `InFlightSystemEntryIDs` filter + the DESTRUCTIVE `DrainStatusChangeLines`, `buildInjection` = the pure snapshot→frames step, `writeInjection` = the bounded per-frame write + ack. **Blind spot, per QUM-1073: no row in this table exercises the drain's async (`interrupt=false`) branch to a BUSY child** — this row delivers child→weave, and the weave drain has had the in-flight filter since QUM-925, so a green run here is not evidence for that branch. Do not infer async-child coverage from this row or from `idle-interrupt-inject`), `internal/supervisor/real.go`, `internal/inboxprompt/inboxprompt.go`, `internal/tui/messages.go`, `internal/tui/viewport.go`, or `cmd/enter.go` (`buildEnterSessionSpec` `ReplayUserMessages`) | `drain-row-inject` | QUM-555/QUM-323/QUM-817/QUM-1084 |
   | `internal/runtime/unified.go` (`UnifiedRuntime.Interrupt` — the bare Esc-abort frame, now the ONLY interrupt entry point; QUM-821 deleted `ForceInterruptForDelivery`), `internal/supervisor/runtime_launcher.go` (`drainPendingToStdin` — a wrapper since QUM-1060; QUM-821 deleted `unifiedHandle.ForceInterruptDelivery`), **`internal/supervisor/drain.go`** (QUM-1084 — the interrupt/async priority split now lives here as policy, not as a branch: `drainPolicy.interruptPriority` (`now` for children, `next` for weave — a LOCKED asymmetry), `drainAsyncPriority = "next"`, `coalesceInterrupts`, and `ackInterruptOnWrite` (QUM-821 ack-on-write, child-only, invalid when coalesced). QUM-1072's per-frame `writeTimeout` bound is here too. **Blind spot, per QUM-1073: this row delivers weave→child at `now`, a tier protected by ack-on-write, so it does not reach the async branch to a BUSY child — and no other row does either; the gap is total across this table.** The nearest is `wake-on-traffic`, which reaches async delivery to a newly-woken *idle* child), `internal/supervisor/runtime.go` / `internal/supervisor/weave_handle.go` (QUM-821 deleted the `RuntimeHandle.ForceInterruptDelivery` interface method + `AgentRuntime`/`WeaveRuntimeHandle` impls; `InterruptCount`/`RuntimeEventInterrupted` telemetry stays on `AgentRuntime.Interrupt`), or `internal/supervisor/real.go` (`Real.SendMessage` — interrupt=true now always routes via `WakeForDelivery`; urgency carried by the `now`-priority drain, not a bare interrupt) | `idle-interrupt-inject` | QUM-619/QUM-817/QUM-821/QUM-1084 |
   | `internal/backend/session.go` (`session.Interrupt` — QUM-827: sends ONLY the SDK interrupt control_request, must NOT cancel in-flight async MCP handlers; they cancel at teardown via `drainInflight`), or `internal/runtime/unified.go` (`UnifiedRuntime.Interrupt` `interruptPending` arm when in-turn + `openFrameTurn` clear-on-open + `routeFrame` EndOfTurn re-classifying a user-interrupted turn as `EventInterrupted` instead of `EventTurnCompleted`/`EventTurnFailed` — QUM-827; QUM-927 widened the arm to `inTurn || frameTurnOpen` for the turn-boundary Esc — the mu-guarded `frameTurnOpen` mirror of the reader-goroutine-only `autoTurn.open` set in `openFrameTurn` / cleared in `closeFrameTurn` at both `st.reset()` sites, the `!frameTurnOpen` gate on `setPhaseLocked`'s idle→non-idle clear, and the `system/init` arm-retire that closes the last stale-arm path; **QUM-927 rework also widened the FAULT-SURFACE gate in the `SetTerminalErrorHandler` closure to `inTurnLocked() || frameTurnOpen`** — a second, opposite-direction use of the same flag: the arm SUPPRESSES an error surface for a user Esc, while this gate PRESERVES one for a genuine transport EOF / subprocess death at the same boundary. Touching either needs this row **and** the reducer-level tests in `internal/tui/app_boundary_fault_test.go`, which are the real gate — per QUM-958 both of this row's assertions are structurally unable to detect the "fault surfaces nothing" class, so a pass here is inconclusive. The deliberate no-`EventBackendFaulted`-case decision in `internal/tui/event_translate.go` is pinned by `TestTranslateRuntimeEvent_BackendFaultedHasNoCase`; the root-idle residual gap is QUM-964) | `esc-interrupt-survives` | QUM-827/QUM-927 |
   | `internal/inputcoalesce/coalescer.go` or the `tea.NewProgram` call site in `cmd/enter.go` (`resolveEnterDeps.runProgram` closure) | `paste-coalesce` | QUM-608 |
   | `internal/supervisor/runtime.go` (`AgentRuntime.Wake` / startWithSpec / health probe), `internal/supervisor/real.go` (`Real.Wake` wrapper / `RecoverAgents` post-restart resume path), `internal/sprawlmcp/server.go` (`toolWake`), `internal/backend/claude/adapter.go` (subprocess lifetime / `realStarter.Start` / `Pid()` exposure), or `internal/runtime/unified.go` (Done() closure on terminal fault / `SetTerminalErrorHandler` wiring; QUM-817: `turnloop.go` is deleted — turn lifecycle + Done now flow through `routeFrame`) | `wake-live` | QUM-606/QUM-724/QUM-817 |
   | `internal/runtime/eventbus.go` (`Publish` Seq stamping, `CurrentSeq`, `PublishWithSeq`; terminal-event undroppable path `isTerminalEvent` / `terminalPublishDeadline` — QUM-775), `internal/runtime/unified.go` (`UnifiedRuntime.Interrupt` idle-branch synthetic `EventInterrupted` emit — QUM-775), `internal/tuiruntime/tuiadapter.go` (`lastSeq`, `pendingMsg` stash/drain, gap-detect branch, `SPRAWL_DEBUG_GAP_INJECT`), `internal/tui/replay.go`, or `internal/tui/app.go`'s `EventDropDetectedMsg` (including its QUM-978 per-leg `rearmPump` — see the `tui-live-render` row) / `ViewportResyncMsg` / `gapConfirmMsg` reducers / `gapStateNormal..gapStateRecovered` / `resyncCmd` / `kickResyncFromGap` / Ctrl+L key arm. **QUM-829 deleted the QUM-775 TUI liveness watchdog and its drop seam** — `TurnWatchdogTickMsg`, `runTurnWatchdog`, `noteBusActivityIfApplicable`, `watchdogTimeoutDefault`, `SPRAWL_TUI_WATCHDOG_TIMEOUT_MS`, `SPRAWL_DEBUG_DROP_NEXT_TERMINAL_MSG`, and the `RuntimeInTurn` `LivenessProbe` in `internal/tui/session_backend.go` — all verified absent from the tree, so do not grep for them to decide whether this row applies (the only `LivenessProbe` left is `internal/supervisor/dead_routing.go`'s unrelated QUM-725 type). Note the watchdog was the mechanism that would have recovered from a parked pump; with it gone, the `EventDropDetectedMsg` re-arm is the only thing standing between a gap and a frozen viewport. | `viewport-resync` | QUM-669/QUM-775/QUM-829/QUM-978 |
   | `internal/tui/app.go`'s pump-delivered reducers — `WaitForEvent()` is ONE-SHOT, so every reducer receiving a bus/pump-delivered msg MUST re-issue `m.bridge.WaitForEvent()` (directly, or via `finalizeTurn` for the terminal trio `SessionResultMsg` / `InterruptCompletedMsg` / `SessionErrorMsg`) or the bubbletea event pump parks and live render freezes (QUM-826). Reducers that re-arm today: `UserMessageSentMsg` / `UserMessageConsumedMsg` (the FIRST non-nil pump event of every typed turn — the original QUM-826 break) / `UserMessageCancelledMsg` / `TurnStartedMsg` / `AssistantContentMsg` / `ToolResultMsg` / `SessionModelMsg` / `CompactBoundaryMsg` / `CompactingStatusMsg` / `CompactFailedMsg` / `EventDropDetectedMsg` (QUM-978), plus the top-level `AssistantTextMsg` / `ThinkingMsg` / `ToolCallMsg` reducers (on the bridge path `MapProtocolMessage` only ever returns these NESTED in `AssistantContentMsg.Msgs`; the standalone reducers are the child-stream / test delivery shape, and they re-arm too). NOT exhaustive as a list of pump-delivered msgs. `EventDropDetectedMsg` is returned directly by `TUIAdapter.WaitForEvent` (both the gap-detect and `SPRAWL_DEBUG_GAP_INJECT` branches) and since QUM-978 re-arms on **every** exit leg — resync-in-flight, burst, already-dropped, below-burst (the last two share a return via the `||` disjunct, and the inner `if m.resyncInFlight` in the burst branch is dead code that is re-armed for symmetry only) — via the nil-guarded `AppModel.rearmPump` helper, batched with the existing `resyncCmd` / `gapDebounceCmd`. The re-arm lives in that reducer **only**: `ViewportResyncMsg` and `gapConfirmMsg` are produced by `resyncCmd` / `gapDebounceCmd` and by the Ctrl+L manual arm (which consumes no pump event at all), never by the pump, so re-arming there too would leave two `WaitForEvent` cmds racing for one event and manufacture the spurious `lastSeq` gaps QUM-669 exists to detect. Pinned by the per-leg tests plus `TestAppModel_GapReducers_RearmExactlyOncePerDrop` / `TestAppModel_CtrlL_DoesNotRearmPump` / `TestAppModel_EventDropDetectedMsg_NilBridge_NoPanic` in `internal/tui/app_drop_rearm_test.go`, and end-to-end by `TestTUIAdapter_GapReducerRearm_DeliversStashedPendingMsg` (the adapter stashes the gap-arriving event's translated msg in `pendingMsg` and only drains it on the NEXT `WaitForEvent`, so without the re-arm that msg is stranded forever). Also `internal/tui/event_translate.go` (`EventUserMessageConsumed`/`Cancelled`/`Sent` → msgs; a nil return means "skip this event, keep reading" — never "park"), or `internal/tui/protocol_mapping.go` — where the trigger is any change that makes `MapProtocolMessage` return a NEW non-nil msg, since that msg becomes pump-delivered and needs a re-arming reducer. QUM-928's sidechain suppression (non-empty `parent_tool_use_id`, inside `mapAssistantMessage`/`mapUserMessage` only) and the deliberately-unmapped `task_notification` are pump-SAFE by construction: both consumers (`internal/tuiruntime/tuiadapter.go`'s `WaitForEvent`, `internal/tui/child_stream.go`) loop on `msg != nil`, so a nil mapping never returns to bubbletea and never consumes the armed cmd. `AutoContinueMsg` and the `task_notification → AutoContinueMsg` mapping were DELETED by QUM-928 — do not grep for them to decide whether this row applies. | `tui-live-render` | QUM-826/QUM-928 |
   | `internal/tui/pendingzone.go` (new — `pendingZone` + `classifyInboundFrame` + `peelNotificationEntries`), `internal/tui/chatlist.go` (`zone` field + `ZoneAdd{User,System}`/`ZoneSettle`/`ZoneDrop`/`ZoneUserCount`/`ClearZone` + `buildRender` zone tail + `Reset` zone-clear — QUM-833), `internal/tui/app.go`'s `InboxDrainMsg` (eager `AppendSystemNotification` DELETED) / `UserMessageSentMsg` (eager-create+classify into zone) / `UserMessageConsumedMsg` (settle/relocate, untracked=no-op) / `UserMessageCancelledMsg` (`ZoneDrop`) reducers + `SessionRestartingMsg` zone-clear + retired `queuedUser`/`queuedText`/`syncQueuedIndicator` (`queuedUserCount` now `ZoneUserCount`), `internal/tui/input.go` (retired `SetQueuedCount`/`queuedCount`/`pendingQueuedIndicator` ⏳ indicator), or `internal/tui/replay.go` (both content branches route through the shared `peelNotificationEntries` — single-classifier convergence). **QUM-925 adds `internal/supervisor/weave_handle.go` to this row** (`runInboxRedrainTicker` / `weaveInboxRedrainInterval` / `drainPendingToStdin`) **and QUM-1084 adds `internal/supervisor/drain.go` alongside it — additively, not as a move**: the ticker and its interval stayed in `weave_handle.go`, but what the ticker *drives* is now `runDrain` under `weaveDrainPolicy` (the serialising `drainMu`, `coalesceInterrupts: true`, and the QUM-559 status-lines-first ordering inside `buildInjection`). Because this row injects straight into `queue/pending/` on disk there is no in-process producer to poke, so the redrain ticker is the delivery path and **this row is its only live coverage** — the file's other rows (`drain-row-inject`, `idle-interrupt-inject`) exercise the poke path instead. This row is also the primary live evidence for QUM-925 **AC 1** ("an idle weave receiving a system frame enters a turn"): its `L0` asserts the idle precondition and its `L2` asserts turn entry from the CLI's own `"kind":"result"` frames. Note `L1`'s pane citations are NOT AC-1 evidence — they render from sprawl's own `EventUserMessageSent` publish and stay green with the CLI dead (mutation-verified). | `notif-stacked-restart` (re-run `tui-live-render`, `drain-row-inject`, `recall-sendnow`) | QUM-833/QUM-925/QUM-1084 |
   | `internal/usage/*.go`, `internal/supervisor/runtime_launcher.go` (`runUsageSubscriber`), `internal/protocol/types.go` (`AssistantMessage.ParseUsage` + `Usage` parse path), `internal/state/state.go` (AgentState cost-field removal), `internal/tui/app.go` (`persistCostCmd` removal; `ShowUsageMsg`/`DismissUsageMsg` handlers, `showUsage`/`usageModal` modal gate), `internal/tui/usagemodal.go` (new — QUM-721), `internal/tui/commands/registry.go` (`/usage` entry + `ActionShowUsage`; the `ActionShowUsage` dispatch itself lives in `app.go`, already named above) | `usage` | QUM-368/QUM-721 |
   | `internal/supervisor/real.go` (`Real.Kill`, `Real.Pause`), `internal/supervisor/runtime.go` (`AgentRuntime.Pause`, `watchHandleExit`), `internal/supervisor/liveness/`, `internal/sprawlmcp/server.go` (`toolPause`), `internal/state/state.go`, `cmd/enter.go` shutdown-loop | `pause-lifecycle` | QUM-722 |
   | `internal/state/state.go` (`StatusComplete` constant, `IsTerminal` narrowed to `{retired, retiring}`, legacy-`stopped` migration in `LoadAgent`), `internal/supervisor/runtime.go` (set-sites that previously stamped `StatusStopped` now stamp `StatusComplete` on `state:complete` teardown / `StatusFaulted` on clean-exit-without-report), or `internal/supervisor/real.go` (`Real.Delegate` auto-wake on `StatusComplete` + `TerminalAgentError` gate narrowed to `{retired, retiring}`, `Real.SendMessage` mirror, `Real.Wake` accept-set, `RecoverAgents` settle-pass) | `complete-lifecycle` | QUM-786/QUM-787/QUM-789/QUM-790 |
   | `internal/supervisor/real.go` (`RecoverAgents`), `internal/supervisor/runtime.go` (`StartResume`, `RuntimeStartSpec`), `internal/supervisor/runtime_launcher.go` (`Start` initialPrompt override), `internal/agent/restart_prompt.go` (new) | `paused-persistence` | QUM-723 |
   | `internal/supervisor/runtime.go` (`AgentRuntime.StopAfterTurn` — the reusable defer-teardown-to-turn-end primitive: subscribe-before-check EventBus wait on `{EventTurnCompleted, EventInterrupted, EventTurnFailed, EventBackendFaulted}` / ctx / timeout runaway guard), or `internal/supervisor/real.go` (`Real.ReportStatus` teardown goroutine — calls `rt.StopAfterTurn(stopCtx, runtimeStopTimeout)` instead of `rt.Stop` so a follow-on send_message in the same turn survives; failure-path `syncRuntimeFromState` still runs after it returns) | `report-then-send` (re-run `complete-lifecycle`) | QUM-866 |
   | `internal/supervisor/real.go` (`Real.SendMessage` / `Real.ReportStatus` dead-recipient route-up), `internal/supervisor/dead_routing.go` (new), `internal/inboxprompt/dead_routing.go` (new), `internal/tui/death_toast.go` (new), `internal/tui/app.go` (`AgentDiedMsg` reducer), or `cmd/enter.go` (registry-subscriber death goroutine in `onStart`) | `death-observability` | QUM-725 |
   | `internal/supervisor/real.go` (`Real.SendMessage`, `Real.Delegate`, `Real.Wake` WakeReason), `internal/supervisor/runtime_launcher.go` (`feedTasks` writes delegated tasks to stdin priority `later` — QUM-817 Amendment 1: proven not to strand an idle agent), `internal/sprawlmcp/server.go` (`toolSendMessage`, `toolDelegate`), `internal/sprawlmcp/tools.go` (schemas), `internal/agent/wake_prompts.go` (new), **`internal/supervisor/drain.go`** (QUM-1084, scoped: `Real.SendMessage` → `WakeForDelivery` → `drainPendingToStdin` → `runDrain` is how the woken agent's first inbox item actually reaches stdin, so a drain break surfaces here. This is the **only** row that reaches the async (`interrupt=false`) branch at all — but to a **newly-woken, idle** child, NOT the busy child of QUM-1073's gap, so it does not close that gap) | `wake-on-traffic` | QUM-726/QUM-817/QUM-1084 |
   | `internal/agentops/spawn.go` (`PrepareSpawn` subagent validation: type allow-list, depth cap, branch rejection, root-cannot-host, parent worktree+branch reuse), `internal/supervisor/real.go` (`Real.Spawn` `AgentInfo.Subagent` / `SharedWorktreeWith` population + StatusReport mirror), `internal/supervisor/supervisor.go` (`SpawnRequest.Subagent`, `AgentInfo.Subagent`/`SharedWorktreeWith`), `internal/sprawlmcp/server.go` (`toolSpawn` subagent+branch interaction validation), `internal/sprawlmcp/tools.go` (spawn schema `subagent` property), or `internal/agent/prompt_child_sections.go` (engineer reviewer-spawn prose) | `subagent-model` | QUM-709/QUM-756 |
   | `.claude/agents/oracle.md`, `.claude/agents/test-critic.md`, or any other worktree-local sidechain definition under `.claude/agents/` | `sidechain-discovery-smoke` | QUM-757 |
   | `internal/claude/launch.go` (`LaunchOpts.ReplayUserMessages` / `BuildArgs`), `internal/backend/session.go` (`SessionSpec.ReplayUserMessages`, `runReader` `control_cancel_request` route + `handleControlCancelRequest`), `internal/backend/claude/adapter.go` (`LaunchOpts.ReplayUserMessages` set-site), `internal/agentloop/session_spec.go` + `cmd/enter.go` `buildEnterSessionSpec` (QUM-817: both set `ReplayUserMessages: true` — without it the CLI never echoes and the whole Slice-2 consumption-ack contract silently breaks), or `internal/protocol/types.go` (`UserMessage.{Priority,UUID,SessionID}`, `UserFrame` (`isReplay`), `CancelAsyncMessageRequest` (`message_uuid`), `CancelAsyncMessageAck` (`cancelled`), `SystemNotification`) | `replay-echo` | QUM-814/QUM-817 |
   | `internal/backend/session.go` (`TurnInfo` / `SetFrameRouter` / `frameRouter`, `runReader` single observe-and-route + orphan-teardown router notify; deleted `pendingTrigger` / `autonomousFrameHandler`), or `internal/runtime/unified.go` (`routeFrame` now drives ALL turns + the `isReplay`→`markConsumed` branch, `inTurn`, `State()` InTurn OR, `New` `SetFrameRouter` install — QUM-817 deleted `turnloop.go` and folded its KEEP-list lifecycle here; **QUM-929 deleted the QUM-640 `[auto-continue]` stdin injection, `continuationPrompt`, and the `servicedTaskSet` QUM-807 dedup** — the CLI self-resumes on background-task completion in every timing case, so the nudge was redundant and structurally one turn late. The row is now an **upstream-regression detector**: it asserts the CLI's NATIVE self-resume (a bg task completing while idle drives an autonomous turn) with ZERO sprawl stdin writes, and fails loudly if a future CLI stops self-resuming. RETAINED on purpose: the `task_notification` `EventProtocolMessage` **observation** (task telemetry), `runtime.AutoContinuePrefix`, and the `internal/tui/replay.go` prefix classifier — historical wire logs still carry `[auto-continue]` frames and must rehydrate as the ↻ marker. **QUM-928 corrected this row's earlier claim that the observation is "the sole source of the live ↻ marker": there is no live ↻ marker any more.** Since QUM-929 removed the injection, sprawl never auto-continues, so a live marker would assert an event that never happened — and it was unreachable anyway (QUM-914 routed every notification elsewhere; measured 4,014/4,014 carry `tool_use_id`, 79% from foreground `Bash`). The live path is deleted; the marker is **replay-only**, which is correct because old sessions contain real injections and new ones cannot) | `idle-continuation` (re-run `viewport-resync`, `notify-tui`, `wake-live`) | QUM-815/QUM-812/QUM-817/QUM-929 |
   | `internal/backend/session.go` (`Session.CancelAsyncMessage` + `pendingControl` request_id→waiter map + `matchPendingControl` + `runReader` control_response route after `matchInitHandshake` + `ErrReaderExited`), `internal/runtime/unified.go` (`SessionHandle.CancelAsyncMessage`, `Recall`, `SendAllNow`, `cancelPendingUser`/`snapshotPendingUser`/`flipPending`, `OutstandingEntry.seq` + `outSeq`), `internal/runtime/eventbus.go` (`EventUserMessageCancelled`), `internal/tuiruntime/tuiadapter.go` (`Recall`/`SendAllNow` bridge + `SendMessage` uuid on `UserMessageSentMsg`), `internal/tui/session_backend.go` (`SessionBackend.Recall`/`SendAllNow`), `internal/tui/event_translate.go` (`EventUserMessageConsumed`/`Cancelled` → msgs), `internal/tui/app.go` (`Ctrl+U` recall / `Ctrl+G` send-all-now weave-only handlers + `queuedUser` set + `PromptsRecalledMsg`/`SendAllNowResultMsg`/`UserMessageConsumedMsg`/`UserMessageCancelledMsg` reducers), `internal/tui/input.go` (`SetQueuedCount` + `⏳ N queued` indicator), or `internal/tui/messages.go` (`PromptsRecalledMsg`/`SendAllNowResultMsg`/`UserMessageConsumedMsg`/`UserMessageCancelledMsg`/`UserMessageSentMsg.UUID`) | `recall-sendnow` (re-run `replay-echo` — session.go control_response route) | QUM-824 |
   | `internal/runtime/unified.go` (`writeMessage` arming `interruptPending` on a `priority:"now"` write when `inTurn || frameTurnOpen` (QUM-927) — QUM-830: a now-write cancel-and-replace preempts the in-flight turn, and the preempted turn's is_error terminal must classify as `EventInterrupted` not `EventTurnCompleted{IsError}` → empty "Session Error"; shares the QUM-827 flag/`openFrameTurn`-clear/`consumeInterruptPending` invariants (and the QUM-927 `!frameTurnOpen` gate on the `setPhaseLocked` clear + the `system/init` arm-retire)), or `internal/tui/app.go` (`Ctrl+G` send-all-now `sendAllNowInFlight` debounce latch + its clear on `SendAllNowResultMsg` — both legs) | `sendnow-tui` (live Ctrl+G double-tap keystroke gate) + re-run `recall-sendnow`, `esc-interrupt-survives`, `idle-interrupt-inject` | QUM-830 |
   | `internal/tui/app.go` (`SubmitMsg` unified always-write-to-stdin path — the deleted `turnState != TurnIdle → pendingSubmit` branch; `UserMessageSentMsg`/`UserMessageConsumedMsg`/`UserMessageCancelledMsg` render-on-consume via `queuedText`; the deleted Esc-preempt + Ctrl+C-recall-slot handlers; `finalizeTurn` no auto-fire; `SessionRestartingMsg` queue-clear; `shortHelpState.HasQueued`), `internal/tui/messages.go` (`UserMessageSentMsg.Text`), `internal/tuiruntime/tuiadapter.go` (`SendMessage` carries `Text`; deleted `InterruptAndSend`), `internal/tui/session_backend.go` (deleted `SessionBackend.InterruptAndSend`), `internal/tui/input.go` (deleted `pendingPreview`/`SetPendingPreview` single-slot preview), or `internal/tui/shorthelp.go` (`HasQueued` bindings → esc=interrupt + ctrl+u recall / ctrl+g send now) | `busy-queue-typing` (live keystroke gate) + re-run `recall-sendnow`, `tui-live-render`, `idle-interrupt-inject`, `drain-row-inject`, `viewport-resync`, `notify-tui`, `wake-live` | QUM-828 |
   | `internal/tui/items.go` (`UserItem.pending` / `SystemNotificationItem.pending` + the shared `pendingStyler` interface and both `SetZonePending` impls — deliberately NOT named `SetPending`: `ToolCallItem.pending` means "unfinished" (`Finished() == !pending`), so a same-named method there would satisfy the interface structurally and let `ZoneSettle` silently mark a live tool call finished; keep the rationale comment at items.go:55-65), `internal/tui/chatlist.go` (`ZoneAddUser` / `ZoneAddSystem` build pending=true; `ZoneSettle` flips every `pendingStyler` envelope to false + nils relocated envelope caches), `internal/tui/pendingzone.go` (pending-zone envelope construction), `internal/tui/render_helpers.go` (`renderUserPromptBlock` `pending` arg), or `internal/tui/theme.go` (`UserPromptPendingText` faint variant — note the *system-notification* dim is applied as `Faint(true)` on `notificationGlyphAndStyle`'s returned style rather than as per-class `*PendingText` theme fields, deliberately: it is total over the notification-class branch by construction, so a newly added class cannot ship bright-when-pending). **QUM-925 F3:** `SystemNotificationItem.Render`'s gutter (`pendingGutter` `┊` vs `committedGutter` `│`) is a LOAD-BEARING second differentiator, not decoration — faint is SGR 2, which a terminal may ignore, and the pending/consumed distinction is LOCKED, so a faint-only delta can silently degrade to nothing. `assertDimIsFaintDelta` cannot detect that (it only proves faint was *added*); its complement `assertPendingSurvivesSGRStrip` strips all SGR and demands the plain text still differ. The two are in tension by design and both must hold — if you touch either, keep both. | `pending-dim-bright` (live: busy-submit → dim → brighten-on-settle → exactly-once → Ctrl+U removes dim; covers **both** zone-held classes — user bubbles and peeled system notifications. NB the row's 6 assertions are all user-bubble today: the system-notification path, gutter included, has unit coverage only until a live `/tui-testing` pass exercises it) | QUM-832/QUM-925 |
   | `internal/runtime/unified.go` (`settleNeverAcked` + `lastRunningMark` + `noteRunningTransition` — QUM-1000: a slash command the CLI REFUSES (unknown `/qum1000-nope`, or a builtin the sdk-cli entrypoint declines like `/status`) gets NO `isReplay` echo, so the pending-zone entry never settles; the sweep settles the OLDEST still-pending `kind:user` entry submitted at or before the last `running` transition, on the non-interrupt terminal leg (so an unarmed `is_error` terminal sweeps too) and ONLY when that turn acked nothing, published BEFORE the terminal event because `internal/tui/app.go`'s `UserMessageConsumedMsg` reducer (QUM-831 `TurnIdle`→`TurnThinking`) has nothing to clear it afterwards — a change to that reducer should route back here. All three guards exist because an early settle is not cosmetic: `snapshotPendingUser` only sees `statePending`, so it silently removes the prompt from `Ctrl+U` recall. `seq` is stamped at SUBMIT time, so two prompts typed before the wire `running` both land under the watermark while the CLI runs them in separate turns — the acked-nothing gate and oldest-only are what keep the second recallable) | `qum1000-refused-slash` (live: `/status` in a sandbox TUI, tmux `capture-pane -e` proves the row is no longer faint, plus a jq-parsed wire assertion that the CLI emitted zero `isReplay` echoes for it; re-run `pending-dim-bright`, `recall-sendnow`, `tui-live-render`, `drain-row-inject`, `notif-stacked-restart`) | QUM-1000 |
   | `internal/hub/*.go`, `internal/hubtail/*.go`, `cmd/hubd/*.go`, `cmd/enter_hub.go`, `internal/transcript/*.go`, or `web/src/wire/*` (the read-only wire-log live-tail stack) | `hub-e2e` (Go-only, behind the `hub_e2e` build tag: real local hubd process + real host tailer + Connect subscriber; proves live-tail, running/idle pill data source, and zero-gap/zero-dupe reconnect across a subscriber blip + a hubd restart — `make test-hub-e2e` or `make test-e2e-matrix-hub-e2e`) | QUM-911 |
   | `Makefile`'s `.PHONY` / `validate` / `test-race` / `test-race-gate` lines, or `scripts/test-race-gate.sh` | **not an e2e row** — run `make test-race-gate` (pure bash + go, no claude/tmux; already part of `make validate`). It is the only thing standing between the tree and a silent loss of race detection: dropping `-race` from `validate` fails nothing on its own. Also re-run `make test-race` if you touched the package pattern. | QUM-972 |
