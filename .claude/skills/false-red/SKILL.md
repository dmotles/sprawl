---
name: false-red
description: Read this when a build, `make validate`, a test, or a merge just failed and you are about to blame your diff. Matches a failure against known environment-caused failures on this host — disk exhaustion, lint lock contention, stripped auth, a test-harness cleanup race, and a merge that un-commits work. Look here BEFORE reverting, retrying with a bypass, or filing a regression.
user-invocable: true
---

# Your run went red. Is it you?

Some failures here are properties of the machine, not of your change. Each entry
starts with text you can see on your own screen right now. Match the text, apply
the remedy, move on. If nothing matches, it is your diff.

Entries are written symptom-first on purpose. The symptom is something you
observe; the mechanism is a claim about the system and can be wrong. Where the
mechanism here is reported rather than confirmed, it says so — act on the
remedy regardless, and do not repeat an unconfirmed cause as fact.

Two standing rules, whatever you match:

- **Never paper over a failure to get green.** No `|| true`, no `-` prefix on a
  Makefile recipe, no `--no-verify`, no removing a tool from `PATH` to force a
  skip. Each converts a diagnosable failure into a silent one.
- **A retry that passes is evidence, not proof.** Say which entry you matched
  and why, rather than reporting a clean re-run.

---

## `no space left on device`

Seen inside `go build` or `go test`, often killing many unrelated things at once.

A filesystem filled. Not your change.

**Check both filesystems — they are usually different devices**, so checking
either alone can show plenty of room while the other is full:

```bash
df -h "$(go env GOCACHE)" /tmp /
```

Free whichever is actually full. `go clean -cache` is the least destructive move
when the cache is the culprit; it costs a rebuild and nothing else. Expect
recurrence — the race detector runs on every validate and race builds are large.

**Do not attribute this to the build cache before you have looked.** That exact
attribution has already been wrong here: an incident was diagnosed as build-cache
pressure while that filesystem had ample free space and a different one was at
capacity. The property is *a filesystem filled*; *the build cache* is a guess
that is convenient to check and can be false.

A full disk also breaks things that do not announce themselves as disk failures
— agent-to-agent message delivery silently losing a message is a reported
instance. If the disk was full, distrust everything else you observed in that
window, not just the build.

> **A signal would beat this document.** A disk-space precondition on the e2e
> harness, failing with its own distinct exit status so an unfit machine is never
> reported as a test result, is specified in QUM-1118. That is the real fix. The
> issue is open, nothing implements it yet, and **the exit status is unassigned
> — do not code against a number.**

---

## `parallel golangci-lint is running`

Another lint run holds the lock. It is **machine-wide** — shared across every
worktree and every agent on this host, not scoped to your checkout. Both
`make fmt-check` and `make lint` take it, so any two concurrent `make validate`
runs contend and the second to arrive loses.

Nothing is wrong with your change, or with the other agent's. Wait and re-run.
If you are coordinating several agents, serialise the validate step rather than
starting them together.

**Do not reach for `--allow-parallel-runners` to make it go away.** The flag
suppresses the guard rather than the contention, and what a parallel run then
does to shared state is not something this file has established — which is
itself the reason not to use it to get green.

> **This could become a signal too**: a distinct exit status for lock
> contention, or a bounded retry-with-backoff in the Makefile, would remove the
> judgement call. Not filed.

---

## `Not logged in`

Also `Not logged in · Please run /login`, and — inside a test — a Session Error
whose entire body is that phrase.

Authentication, not a product regression. This is the most misread failure here,
because a harness reports it in exactly the shape a real defect would take.

A `claude` invoked from an agent's shell gets a sanitised environment with its
auth token stripped. The remedy is the shim that re-hydrates the token before
exec'ing the real binary — see CLAUDE.md's section on running `claude` from
agent bash subshells for the one-time `.env` setup and the `SPRAWL_CLAUDE`
launch form.

Two things that look like fixes and are not:

- **The e2e skip flag does not help.** It keys on the binary being *absent*. An
  installed-but-unauthenticated binary never trips that gate, so the flag is
  inert in precisely this case.
- **Never hide `claude` from `PATH` to force a skip.** That buys a run that
  asserts nothing and reports it as an absence of failure.

---

## `TempDir RemoveAll cleanup: unlinkat ... directory not empty`

Attributed to `testing.go`, on a test whose subject is duplicate-write
prevention in the delivery path.

**Read the failure for an actual assertion. There is none.** If the only failure
text is the cleanup line, no expectation was violated and the product behaved.
The run fails anyway.

The trap is the subject matter: a FAIL on a duplicate-write test reads as a real
regression in message delivery, and has been misdiagnosed as one.

Remedy: re-run. It is load-dependent, so a passing standalone re-run does **not**
contradict this diagnosis — and equally does not confirm it, so check for the
missing assertion rather than trusting the retry.

Tracked as QUM-1070, unfixed. A neighbouring test shares the shape and can
present identically.

> *Mechanism reported, not confirmed here:* the issue attributes it to test
> cleanup racing a released-but-unwaited goroutine writing into the directory
> being deleted. The symptom and the absent assertion are what you should key
> on; the cause above is secondhand.

**This entry is most likely to hurt you indirectly** — it can fail a pre-commit
hook, which produces the next entry.

---

## `has uncommitted changes in worktree. Ask the agent to commit first`

Seen from `merge`, usually on a **retry** after an earlier merge attempt failed.

**Do not clean, reset, checkout, or `git reset --hard` that worktree.** The
message is true and describes the wrong cause. The staged content may be the only
live copy of the agent's work.

The merge engine rewrites the agent's branch *before* it knows the merge will
succeed: it soft-resets the branch to the merge base, then commits the squash. If
that commit fails — a pre-commit hook tripping on an unrelated flake is enough —
the reset is never undone. The commits are then reachable from no ref, surviving
only as staged index content and unreferenced objects. This has cost thousands of
lines in a real incident.

**Diagnose before touching anything:**

```bash
git -C <agent-worktree> rev-parse --abbrev-ref HEAD   # which branch is it on
git -C <agent-worktree> log --oneline -5              # does the branch still have its commits
git -C <agent-worktree> status --short                # what is staged
```

**Recover by pinning first, then moving the ref — never the tree.** This
procedure has been run successfully more than once:

```bash
# 1. Find the branch's real tip from before the merge.
git -C <agent-worktree> reflog --all | head -30
git -C <agent-worktree> for-each-ref refs/sprawl/premerge   # only if the fixed engine is installed
                                                            # (see the predicate below — do NOT write here yourself)

# 2. PIN IT before anything else. A ref makes it survive gc and mistakes.
#    Name the ATTEMPT, not the branch, and embed an ISO timestamp:
#    refs/sprawl/rescue/<agent>/<ISO8601>/<slug>
git update-ref refs/sprawl/rescue/<agent>/2026-08-06T23:00:00Z/<slug> <recovered-sha>

# 3. Move the branch back WITHOUT touching the working tree or index.
git -C <agent-worktree> reset --soft refs/sprawl/rescue/<agent>/2026-08-06T23:00:00Z/<slug>

# 4. Verify, then commit anything still staged.
git -C <agent-worktree> log --oneline -5
git -C <agent-worktree> status --short
```

`--soft` moves the branch pointer only. **`--hard` at any point in this
procedure destroys the thing you are recovering.**

**About the rescue namespace.** `refs/sprawl/rescue/` is **not documented in any
landed tree** — a merge-safety series in flight proposes it, together with a
decision to keep such refs **permanently** rather than sweep them. Use it anyway:
an orphaned ref is a trivial cost against a lost commit. Two rules go with it:

* **Embed an ISO timestamp in the ref name.** `sprawl gc` ages refs by the
  timestamp *in the name*, never by commit date, and a name it cannot parse is
  never pruned. Nothing sweeps `rescue/` today and that is by design; the naming
  keeps the option open, and it matches the repo's "name a ref for an attempt,
  not for a branch" rule. Costs nothing.
* **Never pin a rescue under `refs/sprawl/premerge/`.** That prefix is owned
  exclusively by the QUM-1090 engine so that anything under it is tool output *by
  construction*. A hand-made ref there once made `git for-each-ref
  refs/sprawl/premerge` return non-empty on a tree where the feature had never
  run — a false positive in the verification of the very mechanism this entry
  tells you to check for. Same failure class as everything else in this file.

> **A signal is landing for this one.** A merge engine that writes recovery refs
> before it rewrites anything, and undoes its own reset when the squash commit
> fails, is in flight (QUM-1090 / QUM-1100). Once installed, step 1 becomes a
> lookup instead of an excavation. **Check which engine you have before you need
> it** — and check the *binary that will run*, not branch state or a claim that
> the series landed:
>
> ```bash
> strings $(command -v sprawl) | grep -c 'refs/sprawl/premerge'
> # 0 = OLD engine: the un-commit hazard is live and this entry applies.
> ```
>
> **That predicate is this entry's cut criterion.** Retire the entry when it
> returns nonzero — not when someone reports the fix merged. Measured `0` on this
> host on 2026-08-06.

---

## Nothing matched

Then treat it as yours. Before you do, confirm the machine was fit at the time:
disk on both filesystems, no concurrent validate, auth working. A failure that
matched nothing here is worth reporting *with* that confirmation — every entry
above started as an unexplained red run that somebody bothered to characterise.

---

## Provenance

Each entry's evidence, so a reader can re-check rather than trust. Verified
2026-08-06 against the tree at `68d2ddc`; **the symptom strings are the durable
part — the mechanisms and issue states are not.**

| entry | symptom string | mechanism |
|---|---|---|
| `no space left on device` | **observed** — quoted in an incident report and in a delivered agent message describing most rows of a matrix run dying in `go build` | **verified** — the two filesystems measured as distinct devices; the build-cache misattribution is recorded in QUM-1118 itself |
| `parallel golangci-lint is running` | **verified** — present in the installed `golangci-lint` binary; contention **observed** by two agents on this host | machine-wide scope **observed** (cross-worktree); both Makefile call sites read directly. What a parallel run does to shared state: **not established** |
| `Not logged in` | **verified** — verbatim in a captured payload under `docs/research/` and in an e2e script | **verified** — stated in `scripts/run-claude`'s own header comment and CLAUDE.md; skip-flag inertness quoted verbatim from the harness |
| `TempDir RemoveAll cleanup:` | **reported only** — from QUM-1070; not reproduced here | **reported only.** The named test **is** present and unfixed in the tree (verified). Cause is secondhand |
| `has uncommitted changes in worktree` | **verified** — literal string in `internal/agentops/merge.go` | **verified** — the un-undone `reset --soft` is readable in `internal/merge/merge.go`; the incident is described in the fix commit's own message; recovery **observed** to work twice |

**Known gap:** the `TempDir` entry is the only one resting entirely on a
secondhand report. If you hit it, capture the real output and correct this file.
