# Testing Practices

## Running Tests

Run all tests:

```bash
go test ./...
```

Run tests for a specific package:

```bash
go test ./cmd/...
go test ./internal/state/...
go test ./internal/agentops/...
go test ./internal/supervisor/...
go test ./internal/agent/...
go test ./internal/worktree/...
```

Run a specific test by name:

```bash
go test ./cmd/... -run TestRetire_HappyPathDeletesState
go test ./cmd/... -run TestMessagesSend_HappyPath
```

Use `-v` for verbose output:

```bash
go test -v ./...
```

## Assertion Rigor

**Read this before the rest of the file.** It is the one section here that is a
requirement rather than a convention, and `CLAUDE.md` states its binding form.

### The rule

> **Every new assertion must demonstrate that it CAN fail**, by one of:
> a **negative control**, a **mutation**, or a **red-first** run.
>
> **Every harness that aggregates its own results must have an
> assertion-count floor.** A run reporting `0 passed` / `0 failed` must exit
> non-zero. An anti-vacuity mechanism that can itself go quiet is no protection.
>
> Record *which* demonstration you used and *what it printed*. An assertion
> nobody has watched fail is a claim, not a check.

### Why — this is a selection effect, not bad luck

Over one session (2026-07-24/25) **21 independent instances** of "a check that
reports green while measuring nothing" were found across four agents and two
manager subtrees. That is not an anomaly, and the reasoning matters more than
the rule, because **the rule without the "why" reads as ceremony and gets
skipped**:

**Every one of those instances failed toward green.** Nobody ships a check that
spuriously *fails* — it blocks someone, produces an immediate symptom, and is
fixed within minutes. A check that spuriously *passes* blocks nobody, produces
no symptom, and is **indistinguishable from a working check** until something
independent reveals what it missed. The two classes have radically different
survival rates, so the surviving population of checks is **enriched for
broken-toward-green**. It follows that the remaining stock is large, and that
finding them takes deliberate effort rather than waiting for symptoms.

Two corollaries fall out of that:

* **The highest-yield targets are the inverse of where attention goes:** the
  oldest, greenest, least-recently-failed checks. A check that has never failed
  is either protecting nothing or protecting something perfectly, and those two
  states look identical from outside. (Retrofitting existing checks is tracked
  separately as QUM-956 — not something to fold into unrelated work.)
* **The recursive hazard is the modal outcome, not an irony.** Four times in
  that one session a *measuring apparatus produced a zero it was structurally
  incapable of not producing* — an `O(1)` orphan probe whose predicate excluded
  the tail by definition; a "30 consecutive frames" threshold fed by a sampled
  walk that set the bit 1 frame in N; a cursor probe that never called
  `SetSize`, so the renderer returned `""` and an empty render satisfied a
  count-of-zero perfectly; and a suppression control whose sentinel lived in a
  collapsed body, so the control leg *also* read 0. All four were inside
  instruments built specifically to detect this class, by four different
  competent agents. **Assume your new detector has this defect, and design the
  control that would reveal it before you trust its first zero.** Stated as an
  expectation, not a warning — a warning invites *"I was careful,"* and being
  careful was not enough for any of the four.

### The worked example: `scripts/test-wirelog-helpers-unit.sh`

Do it like that file. It exists because the wire-log helpers it tests feed
non-vacuity guards in the e2e rows, and one of them returned the string `null`
— which makes bash's integer comparison error out **and** evaluate false, so the
guard was silently skipped while the row printed `pass`.

It has all three properties this section asks for, and every claim below is
reproducible from this worktree:

**1. An assertion-count floor.** `MIN_ASSERTIONS=54` at the top; the summary
compares it against a ledger file (each case runs in a subshell, so counters
cannot roll up in variables — a case that dies early contributes no ledger lines
and the floor catches it):

```bash
$ bash scripts/test-wirelog-helpers-unit.sh | tail -1
=== wire-log helper unit results: 54 passed / 0 failed ===
```

Demonstrate the floor itself can fire — bump it by one in a copy and run. The
copy must live in `scripts/` because the script resolves the row files it sources
relative to its own location; the increment is computed rather than hard-coded so
this stays correct when the floor is bumped; and cleanup is on a `trap`, because
the interesting run **exits non-zero** and a trailing `rm` would be skipped under
`set -e`, leaving a stray file in the tracked tree for the next agent's
`git status`:

```bash
DEMO=$(mktemp scripts/.floor-demo.XXXXXX)
trap 'rm -f "$DEMO"' EXIT
awk '/^MIN_ASSERTIONS=/{split($0,a,"="); print "MIN_ASSERTIONS=" a[2]+1; next} {print}' \
    scripts/test-wirelog-helpers-unit.sh > "$DEMO"
bash "$DEMO" >/dev/null; echo "exit=$?"
```

```
  FAIL: only 54 assertions ran, expected at least 55 — a case died early and this run measured less than it claims
exit=1
```

**2. A negative control against the pre-fix commit.** The two helper fixes landed
in `82e0535`. Run today's assertions against the *parent's* helpers:

```bash
CTRL=$(mktemp -d /tmp/wirelog-ctl.XXXXXX)
mkdir -p "$CTRL/scripts/e2e-tests"
cp scripts/test-wirelog-helpers-unit.sh "$CTRL/scripts/"
git show 82e0535^:scripts/e2e-tests/idle-continuation.sh     > "$CTRL/scripts/e2e-tests/idle-continuation.sh"
git show 82e0535^:scripts/e2e-tests/idle-interrupt-inject.sh > "$CTRL/scripts/e2e-tests/idle-interrupt-inject.sh"
bash "$CTRL/scripts/test-wirelog-helpers-unit.sh"   # => 46 passed / 8 failed
rm -rf "$CTRL"
```

**3. Failure sets attributed per fix, and disjoint.** Of those 8 failures, 7 name
the `last_seq_of` integer-sentinel fix and 1 names the `count_now_writes`
newest-by-mtime fix — so each fix is independently covered, rather than one
fixture answering for both. **Report the attribution, not the count:** a single
fixture that happens to exercise two properties produces the same total while
proving neither.

### How to demonstrate a red

**Red-first.** Write the assertion, run it against the unfixed tree, and keep the
failure text — stated in user terms, not in internal state:

```
--- FAIL: TestAppModel_GenuineFaultAtTurnBoundaryWithArm_SurfacesSessionErrorInRootPane (0.00s)
    app_boundary_fault_test.go:263: a genuine backend fault at a turn boundary with an interrupt
    arm set surfaced NO Session Error dialog (showError=false); the user is left with a dead
    subprocess and only the transient label "Interrupted (0ms)"
```

Reproduce it by narrowing the gate that fix widened: in
`internal/runtime/unified.go`, change the **`turnRunning :=` assignment inside
`routeFrame`** from `rt.inTurnLocked() || rt.frameTurnOpen` back to
`rt.inTurnLocked()`, then run
`go test ./internal/tui/ -run TestAppModel_GenuineFaultAtTurnBoundaryWithArm`
and revert.

**Name the site, not just the string** — `rt.inTurnLocked() || rt.frameTurnOpen`
occurs twice, and mutating the *other* one (the `priority == "now"` arm in
`writeMessage`) leaves this test **green**. A reader who picks that match and
concludes the assertion has no detection power has manufactured exactly the
false finding described under *"a mutation you didn't verify landed"* above. Note
also what a good failure message does here: it describes what **the user** is
left with, not which boolean was wrong.

**Mutation.** Break the thing under test and watch the specific assertion that
names that property fire. Live example in this tree — the deliberate *absence*
of an `EventBackendFaulted` case in `internal/tui/event_translate.go`, pinned by
`TestTranslateRuntimeEvent_BackendFaultedHasNoCase`. Add the case and the pin
fails:

```
--- FAIL: TestTranslateRuntimeEvent_BackendFaultedHasNoCase (0.00s)
    event_translate_test.go:240: [InterruptedAsResult] EventBackendFaulted must translate to
    nil (faults route via the agent-named BackendFaultMsg supervisor path, and the root fault
    surface is EventTurnFailed); got tui.SessionErrorMsg backend: claude subprocess exited
    unexpectedly
```

And **verify the mutation landed before trusting that**: `git diff` showed
exactly `+ case sprawlrt.EventBackendFaulted:` / `+ return SessionErrorMsg{...}`.

**Negative control.** Run the assertion against the pre-fix commit or a broken
input, as in the worked example above.

Two rules that make a mutation trustworthy:

* **A mutation you didn't verify landed is not a mutation.** Echo or `git diff`
  the mutated line *before* running. A no-op mutation and an undetectable
  mutation produce **identical** output — and reading that green as "these tests
  have no detection power" manufactures a false finding about someone else's
  work, which is this whole class inverted.
* **Run mutations with `go test -count=1`.** Go caches test results, so a
  mutation run can print `ok … (cached)` — and a cache hit is indistinguishable
  at a glance from a run that exercised your change. It happened while writing
  this section: the first mutation result below came back `(cached)` and had to
  be re-taken. The cache was keyed correctly that time, but "the build didn't
  change, so here's the old answer" is precisely what a mutation that failed to
  land also produces.
* **Mutate each *arm* of a compound guard separately, and name the fault rather
  than the clause.** `if ! reset || [ -s "$FILE" ]` reads as one clause and gets
  credited as one; a fixture satisfying both conditions leaves *either* arm
  independently deletable with the suite green. Asking *"is this clause
  covered?"* finds nothing, because the clause **is** covered — for the wrong
  reason. Asking *"which faults can dirty this sentinel?"* finds the uncovered
  one. Report the failure **sets** per arm and show they are disjoint; equal
  counts are exactly what a double-satisfying fixture also produces.
* **Report the surviving mutants, and classify each.** A bare score is not
  actionable. For each mutant the suite did *not* catch, say whether it is
  **equivalent** (no observable change to the property under test) or
  **uncovered** (a real behaviour the suite does not constrain). *"11 of 12
  caught, and here is the twelfth and why it is benign"* is a stronger position
  than *"mutation testing passed"* — "we know precisely what we don't cover"
  beats a high number.

This applies to ad-hoc tooling you write in the moment, not just to committed
tests — 5 of the 21 instances were in throwaway agent orchestration. The cheapest
example, and one that will be stepped on again:

> **A `pgrep -f` / `ps | grep` liveness guard must exclude itself and its own
> supervisor.** Any predicate keyed on a process's *command line* is matched by
> every process whose command line quotes that predicate — **including the
> guard**. An `until ! pgrep -f "<row-name>"; do sleep 20; done` wait-loop could
> therefore never clear, and the job reported "experiment in flight" twice while
> running nothing. It **self-amplified**: each extra monitor added another command
> line containing the pattern. Key liveness guards on a PID from `$!` or a
> pidfile; if a pattern is unavoidable, anchor it to the binary and exclude
> `$$`/`$PPID`.

The negative control there costs ten seconds — run the predicate with nothing
running and confirm it reports not-running. It was skipped *because the check
looked obviously correct*, which is the honest heart of this whole convention.

### Necessary but not sufficient: constrain the fix, not just the symptom

Red-first proves a test **isn't vacuous**. It does **not** prove the test
**constrains the fix to the right place**. When a defect has more than one
plausible repair site (a gate vs. a downstream consumer, a producer vs. a
translator), a test written from the *symptom* will ratify whichever change
suppresses the symptom.

Real case: a reducer test for the QUM-927 fault-surfacing regression was proven
red-first — a genuine, non-vacuous failure — and a *wrong* fix that left the
actual gate untouched still turned it green. The remedy is mutation testing
pointed at the **fix** rather than the code under test: build the plausible wrong
fix and show your assertion still fails against it. It is cheap, because the
wrong fixes are the obvious alternatives someone would actually reach for.

Pair it with the move that closed that case:

> **Pin a deliberate non-change with a test.** When you choose *not* to make a
> change, assert the non-change and document why. That converts an unstated
> design decision into something a wrong fix must actively break — see
> `TestTranslateRuntimeEvent_BackendFaultedHasNoCase` above.

And a related harness-level trap: **a test that bypasses the real routing path is
not testing the path it names**, and it will read as coverage.

### Negative assertions

> **For a negative assertion, prove the thing you are looking for was capable of
> appearing.**

Otherwise *"not on screen"* and *"never arrived"* are the same observation, and
every negative gate sits one plumbing failure away from vacuity. Three instances
of that one principle — derive the right one for your situation rather than
pattern-matching to the nearest:

| level | statement |
|---|---|
| **principle** | prove the sought thing *could* have appeared |
| instance | never report a zero without a **non-zero from the same capture** proving the measurement was live |
| instance | prove the sentinel landed in a frame that genuinely **met the suppression predicate** — not merely that it arrived |
| instance | **assert the instrument produced non-empty output** before counting absences |

An empty render, a `capture_pane` against a dead session, a `grep` over a
truncated log, and a profile with no samples all satisfy every absence assertion
perfectly. For a suppression gate, require all four legs in the same run: the
sentinel arrives; it arrives in a frame meeting the predicate; it is absent with
suppression on; and it is **present with suppression off**. That last leg must be
mandatory rather than best-effort — in the case that produced this rule, the
first discriminator was invalid and the suppression-*off* control also read 0,
which is the only reason anyone found out.

### A verification function with no reachable failure path is not a check

If a helper's job is to establish or confirm a precondition, there must be an
input for which it returns failure — and you must have run that input. Real case:
`e2e_recover_oauth_token` walks up to 8 ancestor PIDs looking for a token and
then ends with an unconditional `return 0`, so it **structurally cannot report
that the precondition it exists to establish is unmet**. Guards that only ever
succeed are indistinguishable from guards that work, and they are load-bearing
precisely because callers stop thinking about the precondition once a guard
exists.

Related: **prefer deleting a contingency over documenting it.** A comment saying
*"this is `O(1)` because invariant X holds"* is exactly the artifact that lets a
future reader restore a vacuous implementation believing it an optimisation. And
check a comment's **semantics**, not just its stated bounds — *"Range is 0..1"*
was numerically true while its meaning (`0` ⇒ nothing uncacheable) was false. **A
true statement adjacent to a false one makes the false one harder to see.**

### Provenance: emit what you actually ran

A SHA proves which tree you are *standing in*, not that what you *ran* came from
it. Three wrong-tree measurements happened in one evening — one validated a live
working tree while naming a commit; one tested `main`, two slices behind, and
reported it as fix-arm evidence (producing false **passes**); one ran a `/tmp`
copy that could never authenticate. All three would have been caught at zero cost
by the run's own header.

> **Every claim-producing run emits its own provenance, and any claim about that
> run quotes it.** `pwd`; `git rev-parse HEAD`; `git status --porcelain | wc -l`;
> the binary under test (path + mtime); and a **fix-marker** `grep -c` proving
> the artifact under test actually contains the change. Then bracket it: re-check
> HEAD and porcelain **after** the run and discard the result if they differ.

The first four establish *where you were*; the fix-marker establishes *what you
ran*; the bracket establishes *that it didn't move while you measured*. A marker
asserting an **absence** (a deletion → expect 0) needs companion positive markers
in the same block to prove the grep was live. This applies to any claim-producing
run — e2e rows, ad-hoc `/tmp` harnesses, benchmarks, A/B measurements — not just
to committed test scripts, because that is where the convention otherwise never
reaches.

### Reading a control, and reading a failure

* **A parent-commit control proves a failure is *pre-existing*, not that it is
  *acceptable*.** Reproducing on parent establishes not-a-regression; it says
  nothing about not-a-bug. This over-reading got a real lock flake dismissed as
  environmental twice, by two different agents.
* **Read the raw failure output before modelling the failure.** A correlation
  that fits is not a diagnosis. In the worst case of the session, a persuasive
  load→timeout model was fitted to a diligently-recorded covariate while the
  actual cause — `Not logged in · Please run /login` — was printed verbatim in
  every affected log. **A covariate you are proud of collecting is a hypothesis
  generator, not evidence.** Name what you would expect to see if your
  explanation were wrong, then look for it.
* **Verify the path that failed, not a path that resembles it.** A passing check
  on an adjacent code path is *more* dangerous than no check, because it converts
  uncertainty into false confidence and stops the investigation. Ask: *if the
  thing I am verifying were broken in the way I am worried about, would this
  particular check fail?*
* **When you change your tooling, the next failure is probably yours.** Before
  blaming the environment or the code under test, check what your most recent
  change to *how you run things* could have broken.
* **State which direction a "known limitation" fails.** A false-FAIL residual is
  an annoyance you will notice; a false-GREEN residual is invisible and
  unbounded. They are not comparable and must not be waved through with the same
  sentence. At least one accepted limitation in this repo had its direction
  recorded **backwards**, and nobody re-examined it because it was already
  labelled as understood.
* **When you discover an invariant does not hold, enumerate everything that
  depends on it before declaring the blast radius.** *"X is unaffected"* is a
  claim about a dependency graph and needs the same evidence as any other. This
  is structurally tempting rather than careless: the reward for finding a defect
  is fixing *that* defect, and nothing prompts the enumeration.

### Claims about code, and claims about claims

The same asymmetry applies to what agents *report*, and this is the part **no
mechanism catches** — provenance headers, assertion floors, and negative controls
are all blind to it.

* **A completion claim about code is verified by reading the code, not by reading
  the report.** An engineer once reported a rework complete while describing the
  original, QA-rejected commit; a manager's status echoed it upward. It stopped
  one hop later because that manager `grep`ed the diff before merging and found
  neither required change present. Nothing reached `main` — that is the mechanism
  working as designed, and the takeaway is *verify before merging*, not *feel
  bad*.
* **Concurrence is not evidence.** Independent agreement raises confidence
  without adding evidence, which **inverts** the usual "multiple reviewers
  approved this" signal for this class: each additional competent reader makes an
  unverified claim harder to question. A guard clause praised independently by
  two reviewers as "closing the defect class" was later deleted outright with the
  suite fully green. For any clause credited with closing a class, **name the
  artifact that would fail if it were deleted** — deleting it is the cheapest
  test available. An endorsement from *above* is the most costly, because a
  subordinate has the least standing to reopen a question the parent has blessed.
* **A reviewer's job on an inbound claim is to verify the observation, not to
  improve the argument.** Polishing a claim built on an unexamined premise makes
  it more dangerous: it travels further and resists challenge better.
* **Do not accept a soundness argument whose precondition has not been actively
  searched for.** *"X is safe because reaching the bad state requires Y"* is a
  **specification of how to break it**. Treat Y as a search target; if you looked
  and could not find a Y-satisfying path, say that you looked and where.
* **Name the granularity a verdict closes at, and what it explicitly does not.**
  Three fixes in one cluster each *relocated* a defect class rather than
  eliminating it, and each verdict was true at its own granularity with the
  granularity left implicit — which is what let the chain form. Standing
  question: *at what granularity is this closed, and does the same argument apply
  one level down?*
* **Retract as wrong; do not narrow to survive.** When a control disproves your
  claim, withdraw it rather than shaving it until a remnant survives — a salvaged
  claim carries the original's rhetorical weight on much weaker evidence and
  reads as *confirmed* rather than *reduced*. Same for a statistic computed over
  a confounded design: withdraw it, don't soften it.
* **A stale claim that nobody retracts is indistinguishable from a live one**,
  and a retraction must chase every place the claim was relayed, not just where
  it originated. Likewise, **an anchored verdict must be re-anchored before it is
  relayed**, not only before it is issued — anchoring protects the original
  claim, not the retelling.
* **De-staling a historical clause is not the same edit as de-staling a trigger
  list.** Naming a deleted symbol is correct in a clause describing what
  happened; in a list of triggers that tell someone when a rule applies, it is
  the hazard.

### Which tree is your claim about? Both directions of a stale base

Provenance above is about *what you ran*. This is about *which tree the claim is
about* — and a stale base breaks that in **both** directions, which is why one
half is routinely missed:

> **Before filing a defect, verify it reproduces on `main` (or the current
> integration target), not only on your base.** A measurement can be entirely
> accurate and still describe a tree nobody is on.

The asymmetry is the point. A stale base under a *test* produces a false **pass**
— you exercise old code and report the fix working. A stale base under a *bug
report* produces a false **positive** — you describe a real defect that has
already been fixed, and send someone to repair it twice. Same root cause,
opposite direction, and **neither is visible from the measurement itself.** Only
provenance distinguishes them.

The converse, from the same exchange, and the sharper of the two:

> **A grep that matches your vocabulary has not necessarily found your
> mechanism.** Closing a defect on the strength of matching keywords is the
> negative-assertion problem in reverse — you are asserting a *presence* that is
> the wrong presence.

Worked instance, live: a **manager** told an **engineer** to close a report that a
harness "has no assertion-count floor" as already-fixed, on the evidence that the
file had grown by 287 lines and gained 7 matches for `floor`. Every match was
real. None was the mechanism: they belonged to a `NESTED-FLOOR` check that
compares a *child* run's assertion count to the *parent's* for **equality** — a
parity check, satisfied perfectly by `0 == 0`. The top-level summary block was
**byte-identical** to the base, and still prints a green `0 passed / 0 failed`
with exit 0. Settled in one command by diffing the block rather than counting
keyword hits.

Record the direction, because it is the lesson: the engineer **refused the
manager's instruction** and checked the mechanism anyway. The instruction carried
authority *plus* plausible quantitative evidence — the two things that most
reliably substitute for a check — and the manager's own follow-up verification
first appeared to refute the engineer, an artifact of a sloppy `grep -B2 -A6`
extraction pulling in unrelated context. Had the engineer complied, a live defect
would have been closed on a manager's say-so. This is *concurrence is not
evidence* (instance 17) inverted: rank is not evidence either, and it is the
subordinate's job to check the mechanism regardless of who asserted the
conclusion.

> **A suite-size figure is branch-relative and rots within hours. Quote it with
> the SHA it was measured on, or don't quote it.** One suite in this repo was
> reported as 219, 237, and 245 assertions on the same night — all three correct,
> on three different trees. A bare number invites the reader to assume it
> describes theirs.

### Assert the intended outcome, not the current mechanism

> **A test written from the mechanism will pin whatever the code does — including
> the defect — and will then block the correct fix.**

Live example, still in the tree:
`TestInterrupt_AtTurnBoundary_ArmDoesNotSurviveInit` in
`internal/runtime/interrupt_classify_test.go`. For the wire order *boundary arm →
`Interrupt` → `system/init` → `is_error` result*, it asserts `interrupted == 0`
and `completed == 1`. A later analysis concluded that same order must yield
`interrupted == 1` — so the test **encodes the defect as desired behaviour.** Its
header comment reasons entirely from mechanism:

```
// arrives WITHOUT a terminal `result` for the armed turn. A `system/init` with
// the frame turn already open is exactly that case (routeFrame's clear-on-open
// is gated on !st.open, so it does not fire) — if the arm survived it, the next
// turn's genuine is_error result would be swallowed as a clean interrupt.
```

That is a true statement about the implementation and no statement at all about
what the user should see. It was written during a *previous* fix to the same
flag — so this is the **second** time a test at that site ratified the behaviour
it was meant to constrain.

The remedy that worked: **supersede it with a test keyed on the property, not the
detail.** Assert that the armed turn *closes* — the outcome — rather than that a
particular `init` frame does or does not clear a particular flag. A mechanism
assertion has to be rewritten every time the mechanism changes, which is exactly
when you least want the guard rewritten by whoever is changing it.

### A rationale you were given is a claim about intent, not about the code

> **When you transcribe a rationale you were handed, state what is true in the
> tree today. Put the forward-looking reasoning in the commit message, not in the
> comment.**

Instance, and the error belongs to the **manager**, not the engineer: an
`O(n)` accessor was approved with the justification *"an instrumentation consumer
samples one observation in N — the same basis `OrphanCount` runs on."* The
engineer wrote that into the doc comment as a statement of fact. **Both halves
were false in the tree:** there were no consumers at all, and the sibling's real
basis is *"debug and test use only — not on any render path."* The comment read
as accurate while resting on something that did not exist.

Caught before landing. The comment now says what is checkable:

```
// O(n), for debug and test use only — not on any render path. No non-test
// consumers today.
```

Note the shape: a rationale from **above** arrives with authority and is the
least likely to be checked against the tree — the same asymmetry as *concurrence
is not evidence*, one level up. If you are handed a premise, verify it before you
carve it into a comment, because the comment is where the next reader will find
it and stop looking.

### The honest limit

Distribution of those 21 instances: **6 in committed harness code, 5 in committed
product code, 5 in ad-hoc agent tooling, 5 at the coordination/claim layer.** The
last group has grown fastest and is the one **no mechanism catches**. Everything
above the "Claims about code" heading can be mechanised; that section cannot, and
the document says so rather than implying the mechanisms are sufficient.

## Dependency Injection Testing Pattern

This codebase uses a **struct-based dependency injection** pattern for testing CLI commands. Each command defines a `*Deps` struct that holds all external dependencies as fields — typically as **function values** (closures) for filesystem, environment, and git operations, with the occasional interface for richer collaborators (`backend.Adapter`, `worktree.Creator`, `merge.Deps`). The production code path wires in real implementations, while tests inject closures that record calls or return canned values.

The richest end-to-end example today is the offline `retire` command — `internal/agentops/retire.go` defines `RetireDeps`, `cmd/retire.go` wires the production deps, and `cmd/retire_test.go` builds them with closures. Use it as the reference.

### How it works

1. **Define a deps struct** for the command. The current convention is to put the struct (and the business logic) in `internal/agentops/` and re-export a type alias from `cmd/`. From `internal/agentops/retire.go`:

   ```go
   type RetireDeps struct {
       Getenv              func(string) string
       WorktreeRemove      func(repoRoot, worktreePath string, force bool) error
       GitStatus           func(worktreePath string) (string, error)
       RemoveAll           func(string) error
       GitBranchDelete     func(repoRoot, branchName string) error
       GitBranchIsMerged   func(repoRoot, branchName string) (bool, error)
       GitBranchSafeDelete func(repoRoot, branchName string) error
       DoMerge             func(ctx context.Context, cfg *merge.Config, deps *merge.Deps) (*merge.Result, error)
       NewMergeDeps        func() *merge.Deps
       LoadAgent           func(sprawlRoot, name string) (*state.AgentState, error)
       CurrentBranch       func(repoRoot string) (string, error)
       // ...
   }
   ```

   And in `cmd/retire.go`:

   ```go
   type retireDeps = agentops.RetireDeps
   ```

2. **The package-level run function** (`agentops.Retire`) accepts the deps struct instead of calling globals directly:

   ```go
   func Retire(deps *RetireDeps, agentName string, cascade, force, abandon, mergeFirst, yes, noValidate bool) error {
       // uses deps.Getenv, deps.WorktreeRemove, deps.LoadAgent, etc.
   }
   ```

3. **Tests build the deps with closures** in a per-test helper (e.g. `newTestRetireDeps` in `cmd/retire_test.go`):

   ```go
   func newTestRetireDeps(t *testing.T) (*retireDeps, string) {
       t.Helper()
       tmpDir := t.TempDir()
       deps := &retireDeps{
           Getenv: func(key string) string {
               if key == "SPRAWL_ROOT" {
                   return tmpDir
               }
               return ""
           },
           WorktreeRemove: func(repoRoot, worktreePath string, force bool) error {
               return os.RemoveAll(worktreePath)
           },
           GitStatus:           func(worktreePath string) (string, error) { return "", nil },
           RemoveAll:           os.RemoveAll,
           GitBranchDelete:     func(repoRoot, branchName string) error { return nil },
           GitBranchIsMerged:   func(repoRoot, branchName string) (bool, error) { return false, nil },
           GitBranchSafeDelete: func(repoRoot, branchName string) error { return nil },
           DoMerge:             func(_ context.Context, cfg *merge.Config, deps *merge.Deps) (*merge.Result, error) { return &merge.Result{}, nil },
           NewMergeDeps:        func() *merge.Deps { return &merge.Deps{} },
           LoadAgent:           state.LoadAgent,
           CurrentBranch:       func(repoRoot string) (string, error) { return "main", nil },
           // ...
       }
       return deps, tmpDir
   }
   ```

   Note that `state.LoadAgent` is wired through as a real function — tests use the real `state` package against `t.TempDir()` rather than mocking it.

4. **Individual tests override fields when they need to assert specific behavior** rather than maintaining mock structs:

   ```go
   func TestRetire_DirtyWorktree_Refuses(t *testing.T) {
       deps, tmpDir := newTestRetireDeps(t)
       deps.GitStatus = func(string) (string, error) { return "M file.go", nil }
       // ...
   }
   ```

### Function values vs interfaces

This codebase **strongly prefers function values** over single-method interfaces. Use a `func(...) (...)` field whenever the dependency is one operation (`os.Getenv`, `git status`, `state.LoadAgent`, a merge invocation). Reach for an interface only when:

- The collaborator has multiple related methods that callers compose together (e.g. `worktree.Creator`, `backend.Adapter`, `supervisor.Supervisor`).
- You need to fake a stateful object across several calls.

Counter-example to follow: `cmd/messages.go::messagesDeps` only needs `getenv` plus injectable `stdout`/`stderr` (`io.Writer`) — no interfaces at all. See `cmd/messages_test.go::newTestMessagesDeps`.

### Resolve / run separation

Each command file in `cmd/` has the same shape:

- `resolve<Command>Deps()` constructs the production deps (real `os.Getenv`, real git wrappers from `agentops`, real `state.LoadAgent`).
- `run<Command>(deps, ...)` is pure business logic and is the unit under test.
- The cobra `RunE` is a one-liner that calls `resolve...` and then `run...`.

`defaultRetireDeps` / `defaultMessagesDeps` package-level pointers exist so integration-style tests can swap in a pre-built deps struct without going through `resolve`.

### Test file conventions

- Each command file `cmd/foo.go` has a corresponding `cmd/foo_test.go`.
- Helper constructors follow the pattern `newTest<Command>Deps(t *testing.T)`.
- Tests use `t.TempDir()` for isolated filesystem state.
- The `state` and `messages` packages are used directly (not mocked) — tests create real state files and Maildir entries in temp dirs.
- Mock structs only appear when faking interfaces (`worktree.Creator`, `merge.Deps`); see `cmd/mocks_test.go` for the shared ones.

## Manual CLI Validation

Build the binary:

```bash
make build
```

This produces a `./sprawl` binary. The interactive entrypoint is `sprawl enter` — there is no `sprawl init` (it was removed in QUM-346; see `cmd/init_removed_test.go` for the regression guard). The CLI surface is intentionally small: the agent-facing operations (spawn, delegate, retire, kill, send_message, report_status, status, peek, merge, handoff, messages_*) are all MCP tools driven from inside a `sprawl enter` weave session. The standalone CLI exposes only:

```bash
# Open the TUI / weave session (loads the same-process supervisor)
./sprawl enter

# Tail an agent's session log
./sprawl logs alice

# Squash-merge an agent's branch (also available as the `merge` MCP tool)
./sprawl merge alice

# Branch hygiene — delete merged branches not owned by any active agent
./sprawl cleanup branches

# Config + memory utilities
./sprawl config show
./sprawl memory show
```

For anything else — inspecting agent state, sending messages, reporting status, spawning, killing, retiring — drive it from inside `sprawl enter` via the MCP tools.

## Validating Agent Behavior

When testing the full system (not unit tests), inspect these artifacts:

### Agent state files

```bash
# State files live in .sprawl/agents/
ls .sprawl/agents/
cat .sprawl/agents/alice.json

# Each JSON file contains: name, type, family, parent, prompt, branch,
# worktree path, status, session id, cost fields, and last_report_*.
# The full schema is internal/state/state.go::AgentState.
```

### Messages

```bash
# Maildir layout under .sprawl/messages/<agent>/{new,cur,archive}/
ls .sprawl/messages/
ls .sprawl/messages/weave/new/

# Inbox via MCP (from inside a weave session)
# messages_peek({})            — unread count + previews
# messages_list({filter: "unread"})
```

### Git worktrees

```bash
# Worktrees live under .sprawl/worktrees/<agent-name>/
ls .sprawl/worktrees/
git worktree list

# Check for uncommitted changes in an agent's worktree
git -C .sprawl/worktrees/alice status
```

### End-to-end harnesses

The `make validate` pipeline does NOT cover the live supervisor / TUI integration. Use these dedicated harnesses (each spins up an isolated `/tmp` sandbox via `scripts/sprawl-test-env.sh`):

```bash
make test-handoff-e2e          # supervisor + MCP handoff round-trip (QUM-329)
make test-notify-tui-e2e       # TUI inbox-notifier delivery (QUM-311/312)
make test-tui-e2e              # general TUI rendering smoke
```

Each target requires a real `claude` binary on `PATH`; set `SPRAWL_E2E_SKIP_NO_CLAUDE=1` to skip in environments without one. They are **mandatory** before merging changes that touch the file lists called out in `CLAUDE.md` ("TUI-notifier changes are mandatory-tested" / "Handoff-path changes are mandatory-tested").

For ad-hoc exploration, use the `/e2e-testing-sandboxing` skill to set up a sandbox manually.

## Testing Pyramid

### Unit tests (fast, isolated, closures)

The bulk of testing happens here. External dependencies (filesystem mutations, git commands, environment, signals, time) are injected as function-value fields on the deps struct. These tests verify:

- Happy-path logic for each command
- Error handling (missing env vars, exhausted name pool, git failures, worktree failures)
- State transitions (active → killed, active → retiring → deleted)
- Edge cases (already-killed agents, agents with children, dirty worktrees, deprecated CLI paths)

Run with: `go test ./...`.

### Integration-style tests (use real `state` / `messages` / `merge` packages)

Command tests use the real `state` and `messages` packages to read/write JSON files and Maildir entries in `t.TempDir()`. This validates serialization and file operations end-to-end without mocking the filesystem.

`internal/supervisor/*_test.go` exercises the same-process runtime registry against fake backends (see `internal/backend` and `internal/runtime` test helpers) — that's where the bulk of supervisor logic is covered without spinning up real Claude processes.

### Manual / scripted e2e (real claude, real git, sandbox /tmp)

Full-system behavior — TUI rendering, MCP tool routing, claude-process lifecycle, inter-agent message delivery, handoff/restart — is validated by the `make test-*-e2e` targets and ad-hoc sandbox sessions. These cannot be meaningfully unit-tested.

## Common Pitfalls

### Don't shell out to real `git` / `tmux` / `claude` in unit tests

The closure-injection pattern exists specifically so tests don't depend on `git`, `tmux`, or `claude` being installed. If a unit test calls real binaries it will be slow, flaky, and CI-hostile. Always inject closures via the deps struct.

### Function values vs interfaces — pick the smaller hammer

Use function fields when the dependency is one operation (`getenv`, `signalFunc`, `gitStatus`). Use interfaces when the collaborator is stateful or has multiple methods (`worktree.Creator`, `backend.Adapter`). Don't define a single-method interface just to "be testable" — a `func(...)` field is simpler.

### Use `t.Helper()` in test setup functions

All `newTest*Deps` helpers call `t.Helper()` so failure messages point to the actual test function, not the helper.

### Use `t.TempDir()` for state isolation

Never write state files to a shared directory. Each test gets its own temp dir via `t.TempDir()`, which is automatically cleaned up.

### The `state` and `messages` packages are intentionally NOT mocked

Tests use `state.SaveAgent`/`state.LoadAgent` and `messages.*` directly against temp directories. This gives confidence that JSON serialization and Maildir handling work without adding indirection.

### Override fields per-test rather than building parallel mock structs

Idiomatic style: call `newTest<Command>Deps(t)`, then mutate the field you care about (e.g. `deps.GitStatus = func(string) (string, error) { return "M foo", nil }`). See `TestRetire_DirtyWorktree_Refuses` in `cmd/retire_test.go`.

### `cmd/init_removed_test.go` guards a deletion

If you find yourself wanting to add a `sprawl init` or `_root-session` command back, read QUM-346 first — that test will fail and is intentional. The interactive entrypoint is `sprawl enter`.
