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

**Method, before you work any individual entry: key on the absent assertion
first.** Ask what the run actually failed *on* — an assertion in a `_test.go`
frame, or something else — before you ask whether a re-run passes. A passing
re-run is **corroboration only, never the primary discriminator**, because it is
equally consistent with a flake *and* with a real intermittent defect; it cannot
tell those apart, so it cannot be what your diagnosis rests on.

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

Attributed to `testing.go`, seen in `internal/supervisor`.

**Run the discriminator before you look at WHICH test failed.** The failing
frame is `testing.go:<n>`, never a `_test.go` line:

```bash
grep -cE '_test\.go:[0-9]+:' <logfile>    # must be 0
grep -nE 'testing\.go:[0-9]+:' <logfile>  # the cleanup line, and nothing else
```

Zero `_test.go:` frames means **no assertion was violated** — no expectation
failed and the product behaved. The run fails anyway.

**Do not diagnose this by test identity.** The instances observed so far are
listed below as illustrations, *not* as a list to match against: **22 test files
in `internal/supervisor` alone call `t.TempDir()`** (re-measured 2026-08-10; the
figure was 18 when this entry was written, and it moved without anyone touching
this skill), so this can surface on a test nobody has written down yet, and a
name you do not recognise proves nothing either way. The absent `_test.go:`
frame is the diagnostic; the names are not.

**The list being COMPLETE makes it less of a diagnostic, not more.** Instance 1
sat here unnamed until 2026-08-10, when an agent hit it, matched the
discriminator, and independently rediscovered the very test this entry had been
describing by subject the whole time. That is the argument against name-matching
in miniature: the entry could describe the failure precisely and still not name
it, and the name added nothing to the identification — the grep did all the
work. Every additional sighting has landed on a test already listed **or** on
one the list would have had to grow to accommodate; neither outcome is
information. If you are here because you recognise a name below, you have not
yet run the discriminator. Run it.

Observed instances, all in `internal/supervisor`:

1. **`TestWeaveRuntimeHandle_WakeForDelivery_ConsumedButNotYetDelivered_NoDuplicateWrite`**
   (`weave_handle_test.go:601`; reaches `t.TempDir()` via `newWeaveHandleForTest`
   at `:53`) — subject is **duplicate-write prevention in the delivery path**.
   This entry described that subject from the day it was written but did not
   name the test, because the original report was secondhand with no output
   captured. **Named 2026-08-10**, when an agent hit it directly, applied the
   discriminator (zero `_test.go:` frames, exactly one `testing.go:1464`), and
   arrived at the same test the description had been pointing at all along.
2. **`TestQUM1072_SenderMCPCallReturns_WhileRecipientWedged`** — subject is a
   sender's MCP call returning while the recipient is wedged. Observed
   2026-08-07 during `make validate` under 4-way host contention:

   ```
   --- FAIL: TestQUM1072_SenderMCPCallReturns_WhileRecipientWedged (0.18s)
       testing.go:1464: TempDir RemoveAll cleanup: unlinkat /tmp/TestQUM1072_SenderMCPCallReturns_WhileRecipientWedged1704787535/001/.sprawl/agents: directory not empty
   FAIL	github.com/dmotles/sprawl/internal/supervisor	83.602s
   ```

3. **`TestAgentRuntime_FaultChain_DoneClosesAndLivenessReachesFaulted`**
   (`runtime_fault_chain_test.go`) — subject is fault-chain liveness. Observed
   **twice on 2026-08-10, hours apart, by two different agents**, discriminator
   applied both times: zero `_test.go:` frames, exactly one `testing.go:` frame.

   ```
   --- FAIL: TestAgentRuntime_FaultChain_DoneClosesAndLivenessReachesFaulted (0.00s)
       testing.go:1464: TempDir RemoveAll cleanup: unlinkat /tmp/TestAgentRuntime_FaultChain_DoneClosesAndLivenessReachesFaulted1095510233/001/.sprawl/agents: directory not empty
   FAIL	github.com/dmotles/sprawl/internal/supervisor	70.976s
   ```

   **FIXED 2026-08-10 (QUM-1197 item 2 lane): see the MECHANISM section below.**
   The cause was this test's own teardown poll, which waited on the in-memory
   status while `watchHandleExit`'s disk write was still pending. Sightings on
   OTHER tests are not covered by that fix.

   The second sighting was in a **pre-commit hook's** `make validate`, on the
   commit that added this very paragraph — which is the "hurts you indirectly"
   warning at the bottom of this entry, arriving on schedule. Same test, same
   `testing.go:1464`, different tmp suffix (`1042376124`), 71.390s.

   > *Circumstance, recorded and explicitly NOT a claimed cause:* the first
   > sighting occurred with `/home/coder` **99% full, 233M free**. Nobody has
   > connected the two, and a full disk is worth writing down because it breaks
   > things that do not announce themselves as disk failures.
   >
   > **The second sighting constrains this, and is the reason to trust the
   > constraint over the correlation:** it reproduced on the *same test* with
   > **9.1G free (52% used)**. So a full disk is **not necessary** for this
   > symptom. Do not diagnose by disk, in either direction — a full disk does not
   > explain it and a healthy one does not rule it out. Instance 2 was likewise
   > seen under CPU contention rather than disk exhaustion.

**Frequency, and the symptom that actually costs you.** On 2026-08-10 a single
agent hit this **four times in one day** while working one slice: once on
instance 2 (`TestQUM1072_…`), twice on instance 3
(`TestAgentRuntime_FaultChain_…`), and once on instance 1, which is how instance
1 finally got a name. Discriminator applied every time — zero `_test.go:`
frames, exactly one `testing.go:1464` — so all four are this, not four
regressions. (Whether any of those four are the *same events* already tallied
under instances 2 and 3 above was never established, so do not add the numbers
up. The point is the rate one agent saw in one day, not a total.)

**It fails inside the pre-commit hook.** That is the expensive symptom, and it
is worth more than the tally: the hook runs `make validate`, so this does not
merely lose you a test run, it **refuses your commit**. On that same day it cost
one agent two commit attempts and cost a merge cycle. The correct response is to
re-run the commit, *not* to reach for `--no-verify` — which this repo forbids
outright, and which would here be bypassing a guard over a failure that has
nothing to do with your diff. Confirm with the grep, then commit again.

**Load correlates with every sighting and explains none of them.** Contention —
CPU, parallel agents, a busy host — is present in all of them, and it is why a
standalone re-run passes. That is a *condition*, not a mechanism: "it was under
load" does not tell you what is still holding `.sprawl/agents` open. Do not let a
fresh sighting under load reopen the disk hypothesis, which is falsified above
and stays falsified.

### MECHANISM, traced 2026-08-10 (instance 3 only) — and instance 3 is FIXED

The mechanism is a **test that waits on an in-memory read while the production
code's disk write is still pending.** `watchHandleExit` (`runtime.go`) sets the
in-memory snapshot under `r.mu` and only then persists to
`<root>/.sprawl/agents/<name>.json` — deliberately **outside** the lock, as its
own comment says. Instance 3's teardown poll waited for
`rt.Snapshot().Status == faulted`, i.e. the in-memory half, so the test could
return with the watcher goroutine still about to write. `t.TempDir()`'s
`RemoveAll` then raced that write: the directory is removed, `SaveAgent`
recreates a file inside it, and `unlinkat … .sprawl/agents: directory not empty`
is what the loser of that race prints. Load widens the window; it is not the
cause. Nothing is "holding the directory open" — something is *re-creating* it.

**Fix (test-only, no production change):** make the poll also require the
DURABLE status on disk (`state.LoadAgent(root, name).Status == faulted`). The
window closes, and it is the stronger assertion anyway, since durable-Faulted is
what that test's M4 subject actually is.

Controls, both directions, measured on one host and one commit:

* **Defect present** — with the in-memory-only wait, `go test ./... -race
  -count=1` and `make validate` failed **3 of 3** runs (the failure had become
  reliable rather than rare because a diff on the same package added ~8 tests,
  shifting the timing).
* **Defect absent** — with the disk wait, **2 of 2** whole-tree `-race` runs
  green, zero `(cached)` lines. Discriminator applied throughout: zero
  `_test.go:` frames in every red.

**Scope of this claim, stated narrowly on purpose.** Only instance 3 was traced
and only instance 3 is fixed. Instances 1 and 2 are *consistent with* the same
shape — both assert on state a supervisor goroutine persists asynchronously —
but neither has been traced, so treat this as a hypothesis for them and check it
rather than assuming. **The discriminator above stays the entry point:** a fresh
sighting on any other test is not covered by this fix, and the general rule the
mechanism suggests is worth more than the one-test repair — *if a test waits on an
in-memory projection of state that production also writes to disk, wait for the
disk.*

**The trap is the subject matter, and it moves with the test.** Whichever test
surfaces it, the FAIL reads as a real regression in *that test's* subject — a
**duplicate-write** regression for instance 1, a delivery regression for
instance 2, a liveness regression for instance 3, which is exactly the area a
messaging or lifecycle slice is touching when it hits this. Instance 1 is the
sharpest version: a messaging slice that hits
`…_ConsumedButNotYetDelivered_NoDuplicateWrite` while changing the delivery path
is being handed a red that names its own riskiest invariant, and none of it is
real. All three have been or could be misdiagnosed that way. Matching on test
identity is what produces the misdiagnosis; the grep above is what prevents it.

Remedy: re-run. It is load-dependent, so a passing standalone re-run does **not**
contradict this diagnosis — and equally does not confirm it, so check for the
missing assertion rather than trusting the retry.

Tracked as QUM-1070. **Partially fixed:** instance 3's mechanism is traced and
repaired (see MECHANISM above); instances 1 and 2 are untraced and QUM-1070 stays
open for them.

> *Mechanism reported, not confirmed here:* the issue attributes it to test
> cleanup racing a released-but-unwaited goroutine writing into the directory
> being deleted. The symptom and the absent assertion are what you should key
> on; the cause above is secondhand.

> *Lead, explicitly unconfirmed, and specific to instance 2 only:* immediately
> before that FAIL, instance 2's run emitted two of these —
> `WARN unified-runtime: drainPendingToStdin write failed — maildir entries stay in pending/ ... err="context deadline exceeded"`.
> That is *consistent with* the released-but-unwaited-goroutine theory above and
> is **not** confirmation of it: nobody has traced the write to the deletion.
> Do not repeat it as a cause. Nothing equivalent was captured for instance 3,
> so its absence there is not evidence either way — one run's log was kept and
> the other's was not.

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
| `TempDir RemoveAll cleanup:` | **observed repeatedly and independently, by four agents across two days** — 2026-08-07 on `TestQUM1072_SenderMCPCallReturns_WhileRecipientWedged` under 4-way host contention; 2026-08-10 on `TestAgentRuntime_FaultChain_DoneClosesAndLivenessReachesFaulted` at 99% disk; again 2026-08-10 on that same test at 52% disk, in the pre-commit hook of the commit adding this row; and 2026-08-10 **four sightings by one agent in a single day** across all three instances, including the first direct sighting of instance 1 (overlap with the three above was never established — the rate is the datum, not a total). All during `make validate`, discriminator applied each time | **still reported only.** All three named tests **are** present in the tree and do reach `t.TempDir()` (verified: `qum1072_child_drain_bounded_write_test.go`; `runtime_fault_chain_test.go:118`; `weave_handle_test.go:601` via `newWeaveHandleForTest` at `:53`), and the absent-assertion discriminator is **verified** on every captured run (one `testing.go:` frame, zero `_test.go:` frames each). Instance 1 is now **named from a direct sighting** rather than from the secondhand description that opened this entry — but that is the SYMPTOM being observed, not the cause. The *cause* remains untraced by anyone: the `drainPendingToStdin` correlation is still an unconfirmed lead scoped to instance 2, and no later sighting confirms or extends it. **Two things ARE established rather than reported:** the 99%-full disk is **not necessary** — the same test reproduced with 9.1G free, a falsified hypothesis rather than a mechanism; and the failure surfaces **inside the pre-commit hook**, blocking commits, which is observed rather than inferred. **Load correlates with every sighting and explains none** — a condition, not a mechanism |
| `has uncommitted changes in worktree` | **verified** — literal string in `internal/agentops/merge.go` | **verified** — the un-undone `reset --soft` is readable in `internal/merge/merge.go`; the incident is described in the fix commit's own message; recovery **observed** to work twice |

**Closed gap (QUM-1161):** the `TempDir` entry used to be the only one resting
entirely on a secondhand report. Its **symptom** is now firsthand — reproduced
three times (2026-08-07 and twice on 2026-08-10), all outputs quoted in the
entry, on two tests rather than the one originally described. Its **cause** is
still secondhand, and the standing invitation still applies to that half: if you
ever trace the cleanup race to an actual writer, correct this file. Three
independent sightings raise confidence that the symptom is real and
load-dependent, and the third **falsified one candidate condition** (a full disk
is not necessary). Neither says anything about the reported mechanism.

The distinction is the point. "Reproduced" upgraded the symptom and the
discriminator; it did **not** upgrade the mechanism, and this table keeps the two
columns separate so that a firsthand symptom cannot be read as a confirmed
cause.
