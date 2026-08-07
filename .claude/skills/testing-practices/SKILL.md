# Testing Practices

## Running Tests

Run all tests:

```bash
go test ./...
```

Note this bare form is a **convenience run, not the enforced gate**: `make
validate` runs the whole suite under the race detector via `make test-race`.
See § *What `make validate` guarantees about data races (QUM-972)* below before
concluding anything from a green `go test ./...`.

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

### The same breath

> **A green test written in the same breath as the mechanism is the highest-risk
> test in a diff, because nothing about it can disagree with the author.**

This is why red-first works, and it is worth stating before the mechanics: it is
the test's **independence**, not its **colour**, that carries the evidence. A
green run only tells you the code and the assertion agree, and they were written
by the same person from the same model of the problem an hour apart.

It also names the hole in TDD-as-practiced: **writing the test first does not
make it independent if you write it *from* the mechanism you are about to
build.** Test-first buys you a watched failure; it does not buy you a second
opinion. Those are different goods and only the first is procedural.

Practical default: **treat any new green test in a diff that also introduces the
thing it tests as suspect until something independent constrains it** — a
mutation, a control from a tree that predates the mechanism, or a reviewer who
derives the expected value without reading the implementation.

### Why — this is a selection effect, not bad luck

Over one session (2026-07-24/25) **more than twenty independent instances** of "a
check that reports green while measuring nothing" were found across four agents
and two manager subtrees — stated as **a floor, not a tally**: the corpus only
grows (this document has added to it since), and a bare count would rot inside
the very document that forbids bare counts. That is not an anomaly, and the
reasoning matters more than the rule, because **the rule without the "why" reads
as ceremony and gets skipped**:

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

### The non-asserting fallback: the one shape to know by sight (QUM-997)

This is the concrete shape the whole section is about, and it is worth memorising
because **it is invisible to review**: it reads exactly like a correct assertion.

> **Rule, unqualified: any validation or test script must exit non-zero when
> something it checks actually fails. No fallback branch may silently succeed.**

Both spellings. In each pair the two lines are visually near-identical and only one
is an assertion:

```bash
cond && ok "thing works" || no "thing is broken"        # CORRECT — records a failure
cond && ok "thing works" || printf '  info: %s\n' "$x"  # DEFECT — counts nothing, fails nothing
cond && ok "thing works"                                # DEFECT — no failure arm at all
cond && ok "thing works" || exit 1                      # acceptable: records nothing, but FAILS THE RUN
cond && ok "thing works" || exit 0                      # DEFECT — has the shape, exits successfully

if cond; then pass "thing works"; else fail "broken"; fi # CORRECT
if cond; then pass "thing works"; else echo "  note: not observed"; fi  # DEFECT
if cond; then pass "thing works"; fi                     # DEFECT — missing else
```

The `||` arm on a *continuation line* is the spelling that defeats readers, because
the eye has already moved on:

```bash
[ "$got" = "$want" ] && ok "counts match" \
    || printf '  info: got %s\n' "$got"
```

Three corollaries that are not obvious from the shape alone:

* **A skip must not exit 0.** `exit 0` on an unmet precondition (no `jq`, no
  `claude`) makes `make` see success over a harness that asserted nothing. Use
  **77** (the autotools SKIP convention this repo already uses for e2e rows). The
  flag or condition acknowledges the *diagnostic*, not the *obligation*.
* **`set +e` obliges you to add a floor.** A harness that deliberately tolerates
  failed assertions in order to report all of them has given up the one mechanism
  that makes an early death loud. Both `make validate` harnesses that use `set +e`
  were found holding a live false-green in the QUM-997 audit; both that use
  `set -euo pipefail` were clean. That correlation is the practical tell.
* **Setup failure is a third outcome, not a pass.** See § *A precondition that
  never holds makes the guard a no-op*.

#### The evidence: a detector for this class that kept falling to it

**A deterministic parser for this is overkill. It was built, and it was rejected**
— recorded here because the negative result is the useful part and would otherwise
be re-attempted.

An `internal/shlint` package was written to detect exactly the shapes above. Over
four review/QA rounds it accumulated **four distinct blind spots, each a silent
false-green inside the false-green detector**, each found by a different reader who
was specifically hunting the class, and each round fixed the spelling in front of it
while the class survived. All four were the same mechanism — a mis-parsed `<<`
swallowing the rest of the file, so it read clean by never being examined:

| # | spelling | how it read clean |
|---|---|---|
| 1 | `echo "use <<HOOKEOF"` | a `<<` inside a quoted string opened a phantom heredoc |
| 2 | `mask=$(( (1+2) << B ))` | an arithmetic left shift parsed as a redirection |
| 3 | `cat <<'EOF-1'` | delimiter capture stopped at the identifier, so the real terminator never matched |
| 4 | `grep -q x <<<"$LIVE"` | the herestring guard was off by one — the pattern matched at the *second* `<` |

Spelling 4 was **live in the tree**: it blinded the scanner to **462 code lines
across 5 tracked harnesses** (266 of `subagent-model.sh`'s 428 — 62% of one file)
— and while those lines were dark, **every aggregate counter was byte-identical**:
13 sites, 72 case blocks, 87 helper definitions, 0 findings. That is why four rounds
of green told nobody anything, and it is the transferable lesson:

> **An aggregate count cannot detect a coverage collapse.** Reverting fix #3 moved
> **17 of 17** per-file measurements (deltas +49 to −53) while the corpus total moved
> by **−1**, and the entire suite stayed green. If a floor is the only thing standing
> between you and a false green, make it **per-unit**, not a sum.

The tool's own live coverage was also far narrower than its cost implied. Measured
at `de22410`: a repo-wide sweep for `&& <pass helper>` with a non-asserting `||` arm
returns **zero** live instances, and the shape exists at all in only **1 of 75**
tracked shell files — a `docs/research` script `make validate` never executes.
Meanwhile the if/else spelling the parser could not read at all accounts for **99
function-level assertion sites across 64 files**. So the parser was expensive,
recursively defect-prone, and pointed away from where the risk actually lived.

One caution on that comparison, because it bit this very audit: a QA report credited
`test-e2e-matrix-unit.sh:assert_true` with "routing 115 assertions", and it was
relayed twice as a headline number. `assert_true` has **zero call sites** — it is a
defined-but-unused helper, and the suite's 115 assertions call `pass` directly. So
the mutation that "silently no-op'd" was a mutation of dead code, and it evidences
nothing about coverage. *Verify a count before you build an argument on it* —
§ *Claims about code, and claims about claims*.

**The defence is manual review against the checklist above, plus the assertion-rigor
convention in this section.** A reader with the shape in their head, reading the
script, outperformed the parser at every round.

#### What the manual audit found that the parser never would (QUM-997, at `de22410`)

Six live false-greens, all fixed, each with a watched failure recorded. **Not one is
the `&&`/`||` shape the parser was built for** — every one is structural:

| harness | defect | measured before the fix |
|---|---|---|
| `test-wirelog-helpers-unit.sh` (in `validate`) | unchecked `mktemp -d`: every fixture root became `/`, so assertions passed *vacuously* against the `-1` sentinel they assert, and the ledger-based floor had no lines to count | **40 spurious PASS, 15 real FAIL, blank counts, exit 0** |
| ″ | the summary's own `[ "$TOTAL" -lt … ]` and `[ "$FAIL" -gt 0 ]` both errored on a non-integer and evaluated false, skipping *both* gates — the exact trap this file's header describes | `=== results:  passed /  failed ===`, exit 0 |
| ″ | `jq`-absent skip exited 0 | green over a harness that never ran |
| `test-e2e-matrix-unit.sh` (in `validate`) | **no assertion-count floor at all** on its own totals, despite ~245 assertions across 16 sections | truncated after section [1]: **`2 passed / 0 failed`, exit 0** |
| `test-leak-resistance-e2e.sh` | negative assertions with no positive control: a driver dying instantly means no sandbox, hence nothing to leak, hence PASS | **`3 passed, 0 failed`, exit 0** with stub drivers printing `Not logged in` |
| ″ | no case-count floor, so a vanished `run_case` was invisible | — |

Two of those deserve singling out as reusable warnings:

* The matrix suite **did** contain the word "floor" seven times. Every match belonged
  to a `NESTED-FLOOR:` **parity** check comparing a child run's count to the
  parent's for equality — and `0 == 0` satisfies it perfectly. This defect had
  already been closed once as "already fixed" on the strength of those grep hits.
  See the rule *"a grep that matches your vocabulary has not necessarily found your
  mechanism"* in § *Which tree is your claim about?*; this is that lesson's live
  instance, and it was still live.
* The leak harness is the sharpest case of § *Negative assertions*: it printed
  `PASS` for "no orphan processes, no stale sockets, no residual dirs" in a run
  where the scenario never started. The fix is a positive control (`saw_sandbox`)
  plus reporting setup failure as a **third outcome** — `0 passed, 0 failed,
  3 never ran` — in the vocabulary of what actually happened.

**Standing prohibition, so this is not relitigated: do not rebuild the parser.**
It was built, measured against a real audit, and rejected on the evidence above.
The defence against this class is manual review against the shapes listed here,
not a detector — a detector for it acquires the defect it detects.

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

**2. A negative control against the pre-fix commit.** `82e0535` changed three
things in these helpers: the `last_seq_of` integer sentinel, `count_now_writes`'
newest-by-mtime file pick, and a `fromjson? // empty` torn-line tolerance. Run
today's assertions against the *parent's* helpers:

```bash
CTRL=$(mktemp -d /tmp/wirelog-ctl.XXXXXX)
mkdir -p "$CTRL/scripts/e2e-tests"
cp scripts/test-wirelog-helpers-unit.sh "$CTRL/scripts/"
git show 82e0535^:scripts/e2e-tests/idle-continuation.sh     > "$CTRL/scripts/e2e-tests/idle-continuation.sh"
git show 82e0535^:scripts/e2e-tests/idle-interrupt-inject.sh > "$CTRL/scripts/e2e-tests/idle-interrupt-inject.sh"
bash "$CTRL/scripts/test-wirelog-helpers-unit.sh"   # => 46 passed / 8 failed
rm -rf "$CTRL"
```

**3. Failure sets attributed per change, and disjoint — for two of the three
changes.** Of those 8 failures, 7 name the `last_seq_of` integer-sentinel fix and
1 names the `count_now_writes` newest-by-mtime fix. Two disjoint sets, so neither
is one fixture answering for the other — and that is the whole claim. **The third
change, `count_now_writes`' `fromjson? // empty` torn-line tolerance, has no
red-first evidence.** The suite does carry torn-inner-frame assertions, and they
pass against the *pre-fix* helper too: `jq` reports the bad input and keeps
reading, so the counts agree with and without the guard. Measured on jq-1.6 over a
3-frame log with a torn middle frame: both forms print 2 and **both exit 0** — the
only difference is a single `jq: error (at <file>:N): Unfinished string at EOF`
line on stderr, which the helper discards with `2>/dev/null`. By the
surviving-mutant rule below, that makes the tolerance **equivalent for the
property asserted**, not covered. Say which;
otherwise a 7+1 attribution reads as three-for-three. **Report the attribution,
not the count:** a single fixture that happens to exercise two properties produces
the same total while proving neither.

### Provenance of the observed string: who mints the artifact? (QUM-925)

> **An assertion that observes an artifact your own process produces cannot be
> evidence about another process.** Before citing a cross-process assertion, ask
> **who mints the artifact.** If your side can produce it, the assertion is about
> your side.
>
> **Provenance is a property of the individual assertion, not of the row.** A row can
> be sound overall while the sentence you quote from it is not evidence for the claim
> you are making.

This earns a named check rather than a line in the two sections above because it is
a **mechanical question with a grepable answer** — *who writes this text, us or the
thing we're testing?* — and it catches a class that careful reading provably does
not. Three confirmed instances on QUM-925, **none caught by reading the assertion
and reasoning about it:**

| # | assertion | claimed | actually proved | caught by |
|---|---|---|---|---|
| 1 | the pre-`assertDimIsFaintDelta` tests | pending renders dim | *some* SGR changed — `Underline(true)` passed | adversarial reviewer |
| 2 | the strip-SGR fallback as first specified | distinction survives a faint-blind terminal | plain text differs — a ZWSP gutter passed | adversarial reviewer |
| 3 | `notif-stacked-restart` L1 pane citation | an idle weave took a turn | sprawl wrote and rendered a frame | engineer attacking its own **new** assertion |

All three observed something weaker than the claim; in #3 that something is an
artifact **the asserting side minted**, which is the form a grep settles. It greps
weave's pane for `From <agent> — mcp__sprawl__messages_read(id=…)`, a string minted
by **sprawl** (`internal/inboxprompt/inboxprompt.go:121`) which, since QUM-925,
renders from sprawl's own `EventUserMessageSent` publish — and L1 has **no upstream
cross-process gate**, so it **passes with the CLI subprocess dead.** Proven by
mutation: suppress the `kind:system` stdin write, keep the publish → L1 PASS, L2
FAIL, same run.

**The contrast case, which is where the granularity rule earns its keep.**
`drain-row-inject:162` greps for the *same self-minted string* — but it sits behind
two unforgeable upstream assertions: a non-weave `state` file appearing (`:116`,
minted by weave's CLI calling `spawn`) and the child's `messages_send` envelope
landing in weave's maildir (`:144`, minted by the child's CLI). With a dead CLI the
row dies at `:116` and never reaches `:162`. So the row is **sound**; the citation
was still wrong — `:162` alone does not support *"weave acted on the notification"*,
and `:116`/`:144` prove the CLI acted on the **spawn prompt**, which is a different
claim. Remedy is a **citation fix — quote `:116`/`:144`** — not distrust of the row.

The positive form is what licenses trust. L2 observes a `"kind":"result"` entry,
which has exactly one producer chain, ending at `session.runReader` reading from
`transport.Recv` — and `grep -rn 'protocol.Message{' --include=*.go internal/` is
**empty outside tests**, so sprawl *cannot* mint one. That is an **impossibility
argument**, not an absence-of-evidence one, and only the former is evidence.

Four things that do not follow from the rule itself:

* **Careful reading is not the mechanism.** Three of us propagated the (false) claim
  that the contrast row was hollow too: one asserted it from a QA report without
  reading the row's assertion order, one relayed it onward, and the third caught it
  by reading the mint sites — then withdrew half its own sentence after checking.
  Reading failed three times consecutively on people writing this very guidance; the
  mechanical question (*who mints this, and what gates it?*) caught it.
* **Correcting one instance does not sweep the class.** The contrast case went
  un-examined through the first correction because only L1 was revisited. When you
  find one of these, grep for its siblings.
* **Plausibility is the trap.** These assertions convince *because* the text names a
  child agent and reads like the notification arriving — but the naming is sprawl's
  own prose template, so the most persuasive detail is the one your side supplied.
* **Not only a testing concern.** The general form is *reasoning carefully about a
  mechanism without checking whether the mechanism is exercised* — the same shape
  produced a filed issue describing a "user-visible semantics change" on a code path
  that is structurally unreachable (an ancestor gate that can never pass for the
  root).

**A provenance-correct artifact is necessary, not sufficient — prove it was being
produced.** The wire log has its own false-green mode: if
`SprawlRoot`/`Identity`/`SessionID` is empty the path is never built
(`internal/backend/claude/adapter.go:133-135`), and if `newWireLog` fails capture is
disabled with only a stderr line (`:308-311`) — so a harness pointed at the wrong
path counts **zero frames and passes every assertion vacuously.** Note also that the
switch from pane to wire log here was made for an unrelated reason (the property was
unrecoverable off-pane; *any* off-pane artifact with the right fields would do) and
turned out provenance-correct by luck. The check is what makes that reliable.

> **A liveness gate must announce itself on success, or it is unverifiable from the
> outside.** Presence is greppable; *firing* is not — a gate inside a branch that
> never ran, or after an early return, reads identically in source to one that
> executed, so a reviewer performs the greppable half and feels satisfied. Print on
> the success path (`WIRELOG_LIVENESS_OK in-user-frames=<n> out-frames=<n>
> log=<path>`) so execution, counts, and resolved artifact are all observable. And a
> liveness gate that **warns and continues is strictly worse than no gate** — it
> manufactures the appearance of coverage. This is the family one level up: the
> false-green modes above are all *the run says nothing and silence reads as fine*;
> this one is *the guard against that says nothing.* **A check that cannot itself be
> verified is not a check.**

Five neighbouring shapes now, and they are distinguished by **where** the defect
sits, not by how they read — all five read as coverage. This is the family's one
canonical **tabular** listing (§ *Mutate along the axis your assertion constrains*
names the shapes in prose as a pointer, but no other section restates the table):

| shape | what's wrong | remedy |
|---|---|---|
| § *The non-asserting fallback* | no failure arm — silently succeeds | add the else branch |
| § *Mutate along the axis your assertion constrains* | real failure arm, predicate too weak | tighten the predicate |
| **provenance (this section)** | predicate is fine — **it observes the wrong process** | assert on an artifact only the other side can mint |
| § *Indistinguishable from success* | assertion and predicate both fine — **the input is stale** | force a fresh run; don't trust the summary |
| § *A null result is a statement about your search* | nothing wrong with the check — **the instrument could not have observed it** | positive control: probe for something you know is there |

What the five have in common is stated in § *Indistinguishable from success*: **the
failure is indistinguishable from success at the point of observation.** Different
mechanisms, one property — which is why each row needs its *own* remedy, and why
mis-identifying the row means applying a fix that cannot work.

The fifth row points at `ratz`'s entry. **The row sits at this table's tail; the
*section* does not sit next to this one** — it was deliberately placed beside
§ *Mutate along the axis your assertion constrains*, the rule it inverts, because its
author grepped for this then-unmerged section and moved off the natural placement to
avoid colliding with it. (In document order that section is in fact last of the five;
the point is only that it is not adjacent to this entry, which is where a reader
would expect to find it.) Row placement and section placement are separate decisions
here, and only the second was deliberate. The row itself was added by whoever merged
second (here, this entry), and states the shape in full, so it does not depend on the
section for meaning.

### Indistinguishable from success at the point of observation (QUM-1047)

**If you arrived here holding a suspicious green, start with this table and leave.**
It is the whole section in one screen; everything below it is evidence for why these
rows exist, and you do not need it to act. This entry is long, and a long entry
nobody finishes is its own unexercised instrument — so the navigation is load-bearing,
not decoration.

| your symptom | you are probably on | do this |
|---|---|---|
| `(cached)` in the log, or a green you didn't wait for | a build cache | `go test -count=1` on the changed package; read the log, not the summary |
| a harness printed `0 passed / 0 failed`, or an aggregate you can't tie to a run | no assertion-count floor | make the harness exit non-zero on zero assertions |
| exit 0 out of a pipeline | verdict lost in a pipe | `set -o pipefail`; check the producer's status, not the consumer's |
| a `grep`/`ls` found nothing and you're about to report "no X exists" | a null result about your *search* | positive control: search for something you **know** is there, or check your base |
| someone told you a grep returned N | a relayed measurement | re-run it yourself; a relay strips the authority and keeps the appearance |
| a cited SHA, doc, or issue title that "reads as" a finding | a rotted reference or a promoted hypothesis | resolve it on **your** tree; check the body, not the title |
| a merge/tool reported success and the tree looks right | shared tooling on a stale base | check the *history*, not the tree — `git log`, not `git diff` |
| nothing is failing and you want to find something | audit a green on purpose | pick a test naming a coupling; ask what it *claims* to guard and whether its route reaches that — then run the mutation before you publish the catch |

Two cross-cutting rules that apply whichever row you landed on: **the remedy is never
"look harder"** — it is always to observe something else; and **a claim is only as
good as the tree it was measured on**, so re-derive rather than relay.

---

If this section carries one idea, it is this one — and it is the property the
whole family shares, not a fact about caching:

> **Different mechanisms, one property: the failure is indistinguishable from
> success at the point of observation.**

A cached test result, a swallowed exit code, and a stale SHA have nothing in common
mechanically. What they share is that **the observation you make is identical in
the good case and the bad case** — so no amount of care applied *at that
observation* can separate them. That is why the remedy is never "look harder"; it
is always to observe something else.

**Ignore the counts; they are the least durable part.** Two tallies appear near
this section and they are *different families*: the table above counts **assertion
shapes** (five as of writing), and § *Eight surfaces, one property* below counts the
**surfaces** on which the property has been observed — mechanisms, not shapes, and
they overlap only partly. Both numbers were smaller yesterday, both grew while this
section was being written, and the surfaces tally grew once more during the review
that preceded this commit. The property is the claim; the tallies are just how much of it
has been written down so far, so prefer the named list to the number.

It is also what the table above is *for*: read it as one property with several
entry points, not as a list of unrelated pitfalls — each row's remedy differs
because each mechanism differs.

**Three adjacent axes, one per section, easy to conflate** — this one asks **when**
the artifact was produced; § *Provenance of the observed string* asks **who**
produced it; `ratz`'s § *A null result is a statement about your search* asks
**whether the instrument could have observed it at all**. That section states the
same three-way split from its side; this clause exists so a reader arriving here
can navigate, not to restate it.

The rest of this section is the **worked example**: Go's build cache, which is the
most industrialised instance because it produces the property automatically,
continuously, and by design. The subject is the property; caching is the specimen.

#### The specimen: a genuine green over a cached result

The assertion is well-formed, the predicate is tight, the provenance is clean —
and the run you are reading **did not happen**:

```
ok  	github.com/dmotles/sprawl/internal/runtime	(cached)
```

This is the fourth shape in the table above, and the only one that needs **no
defect anywhere**: the code is correct, the assertion is correct, and the
*evidence* is corrupt.

**The seam against § *Provenance of the observed string*, which is the distinction
most easily lost:**

> **Provenance is about *who produced* the artifact; this is about *when* it was
> produced.** They **compose rather than overlap** — a provenance-clean assertion
> still reports a green from a cached run, and a fresh run proves nothing if the
> string it greps is self-minted.

There is a **third** axis, and all three have to hold independently, so it is worth
naming them together rather than leaving a reader to sort near-identical sections:

| axis | question | the failure |
|---|---|---|
| **who** (§ *Provenance of the observed string*) | who minted the artifact? | you assert on a string your own process produced |
| **whether** (§ *A null result is a statement about your search*; the general principle is § *Negative assertions*) | could your instrument have seen it at all? | a null result that is a statement about your *search*, not the code |
| **when** (this section) | when was the input produced? | a green from a run that already happened |

One phrasing unifies all three: **a predicate that returns the same answer
everywhere hasn't been tested, it's been unexercised.** Whether it is the same
answer because you minted it, because you could not have seen otherwise, or
because you are reading a cached verdict, the observation carries no information —
which is the property this whole section is about.

**This is not the cache in § *New render-affecting state is a stale-cache bug by
default*.** That one is a **render** cache *inside the product* — a real bug in
shipped code, fixable by an invalidation. This is Go's **build/test** cache,
*outside* the product. Nothing here is fixable by a code change, which is why it
needs a named check instead: conflating the two sends you looking for a defect
that does not exist.

**Mechanism.** Agent worktrees share a filesystem and a `GOCACHE`. `go test`
reuses a prior run's *result* for any package whose inputs are unchanged. So when
a manager re-runs `make validate` on a child's branch **to verify rather than
relay**, most packages print `(cached)` — and in the bad case that includes the
package the child changed, because the child already ran it. The re-run then
re-asserts the child's own result.

**Why the obvious check fails.** Exit 0 is genuine. Matching assertion counts are
genuine. Both are true properties of a run that did not happen for the package
under change, and nothing in a summary line distinguishes the two cases. That
makes this structurally worse than the harness false-greens audited under
QUM-997: those require a **defect in a harness**; this requires only a shared
filesystem and a warm cache — the fleet's normal operating condition.

**Remedy.** `go test -count=1` on the package under change (`-count=1` is the
documented cache bypass; **`-race` does not imply it**), and read the log for
`(cached)` rather than trusting the summary:

```bash
#!/usr/bin/env bash
# Did this green actually exercise the packages I changed?
# usage: check-cached <validate.log> [pkg-dir ...]   (default: dirs changed vs main)
# Deliberately NO `set -e`: a non-matching grep must be reported, not abort the run.
log=$1; shift
if [ $# -gt 0 ]; then
  changed=$(printf '%s\n' "$@")
else
  files=$(git diff --name-only main...HEAD) \
    || { echo "CANNOT DETERMINE CHANGED SET — git failed"; exit 2; }
  changed=$(printf '%s\n' "$files" | sed -n 's|/[^/]*\.go$||p' | sort -u)
fi
[ -n "$changed" ] || { echo "NO GO PACKAGES CHANGED vs main — nothing to check"; exit 0; }
rc=0
while IFS= read -r p; do
  [ -n "$p" ] || continue
  line=$(grep -E "^(ok|FAIL)[[:space:]]+[^[:space:]]*/$p[[:space:]]" "$log")
  case $line in
    "")           echo "MISSING  $p — no result line at all: this run never tested it"; rc=1 ;;
    FAIL*)        echo "FAILED   $p — this run DID test it, and it FAILED: $line";      rc=1 ;;
    *"(cached)"*) echo "CACHED   $p — NOT run by this log: $line";                      rc=1 ;;
    *)            echo "RAN      $p — $line" ;;
  esac
done <<<"$changed"
exit $rc
```

**Every arm here was added because the previous draft passed silently without it**,
and the sequence is worth recording, because the recipe kept committing the sin the
section names:

1. The **first** draft was a bare `for p in $changed; do grep …; done` — it printed
   nothing and exited 0 both when a package was never tested and when the changed
   set was empty. That is § *The non-asserting fallback*, inside the recipe for
   detecting false greens.
2. The **second** draft added `MISSING` and the empty-set guard — and a reviewer
   found it still **exited 0 on a log where the package had FAILED**, because
   `FAIL` matched the `*)` arm and was reported as `RAN`. A reader would have read
   exit 0 as "this log is trustworthy" over a genuine test failure. Hence the
   explicit `FAIL*` arm, ordered before `(cached)` so a cached *failure* still
   headlines as a failure.
3. The same review found the changed-set computation could not fail *visibly*:
   `$(git diff …)` inside a `${:-}` default discards git's exit status, so "not a
   git repository", a missing `main`, or a bad revspec all printed **`NO GO
   PACKAGES CHANGED`** and exited 0 — a message asserting a fact the script had not
   established. Hence the explicit `|| exit 2`.

**Three** instances of that shape in one recipe, in a section *about* that shape —
count the numbered list, not this sentence's earlier drafts, which said "two" by
silently scoping to the reviewer-caught ones. Item 1 was the author's own and the
author found it; items 2 and 3 were caught by a reviewer rather than by the author
re-reading it, which is the part worth keeping. Ordering matters too:
`""` must precede the globs, and `FAIL*` must precede `*"(cached)"*`.

**Portability, since this is meant to be copy-pasted:** `[^[:space:]]` rather than
`\S`, and no `xargs -r` — both of those are GNU-only, and on BSD/macOS `grep -E`
the `\S` form matches a literal `S`, which turns every package into a spurious
`MISSING` and makes the recipe a false-*alarm* generator. `$p` is interpolated into
an ERE, so a package path containing regex metacharacters is matched loosely; for
this repo's paths that is harmless.

**Exercised in every direction before being written down** — extending § *How to
demonstrate a red* from assertions to diagnostics, which is this section's claim
rather than that one's: a diagnostic nobody has watched fire is the same unchecked
claim as an assertion nobody has watched fail, and publishing an unexercised recipe
*in this particular section* would be self-refuting. The text above was extracted
from this file with `awk` and run; the transcript is verbatim, tabs and all, with
two elisions, both stated because a sentence claiming verbatim-ness is the last
place to leave one implicit: the module path is shortened to `…`, and the last leg
drops roughly a hundred lines of `git diff --no-index` usage text that git prints to
**stderr** before the diagnostic. (Only stdout is shown. Add `2>/dev/null` to the
recipe's `git diff` if the noise bothers you — behaviour and exit codes are
unaffected.)

```
$ check-cached log internal/runtime internal/backend    # one of each, SAME log
RAN      internal/runtime — ok<TAB>…/internal/runtime<TAB>23.261s
CACHED   internal/backend — NOT run by this log: ok<TAB>…/internal/backend<TAB>(cached)
exit=1
$ check-cached log internal/state                       # a FAIL line in the log
FAILED   internal/state — this run DID test it, and it FAILED: FAIL<TAB>…/internal/state<TAB>1.204s
exit=1
$ check-cached log internal/messages                    # absent from the log
MISSING  internal/messages — no result line at all: this run never tested it
exit=1
$ check-cached log            # on a branch whose diff vs main has no .go files
NO GO PACKAGES CHANGED vs main — nothing to check
exit=0
$ cd /tmp && check-cached log                           # not a git repository
CANNOT DETERMINE CHANGED SET — git failed
exit=2
```

Provenance of that transcript, since the section demands it: the `RAN` and `CACHED`
lines come from **one real log** (`internal/runtime` forced with `-count=1`,
`internal/backend` left warm), which is the load-bearing part — it rules out "the
run was configured differently" as the explanation for the difference. The `FAIL`
line is a **hand-written fixture** appended to that log rather than a broken test,
and the last two were run in a scratch repo and outside any repo. Say which are
synthetic; a fixture is fine, a fixture presented as a live run is not.

**And the demonstration re-demonstrated the finding while being re-run.** On the
first pass `internal/tui` reported `RAN … 13.525s`; on the second, from a
byte-identical invocation, it reported `(cached)` — because the first pass had
warmed it. Nothing about the command changed. That is instance 3 below happening
live in the act of documenting instance 3, and it is the entry's own evidence for
its central claim: **cache state is not an input you control, so a green is not a
property of your invocation.**

**Diagnostic: a recompile has a runtime signature; a cache hit is instant.**
Worked examples, all `internal/runtime`: a child reported **22.363s**; the
manager's verifying `make validate` printed `(cached)` behind a genuine exit 0;
forcing `-count=1 -race` gave **22.366s**. Later, on a rebased tree, **23.328s**
claimed against an independent **23.275s / 23.280s**, and **23.332s / 23.277s** on
the two runs made while writing this section. The child's "genuinely recompiled, not cached"
claim was **true every time** — but the green never established it; the
milliseconds-apart timings did. State that plainly, because it is the operational
rule: **a child's "not cached" claim is not verifiable from a green.** Only a
timing comparison or a `-count=1` re-run establishes it.

**Rule design: state the remedy as a check, not a prohibition.** The defect is never
`(cached)` itself — it is an **unexamined** `(cached)`. A cache hit on a package your
branch did not change is correct, and a rule phrased as *"never accept a cached
line"* is wrong often enough that the first reader to meet a legitimate exception
discards it wholesale. **An over-strict rule does not fail safe; it trains the reader
to discard the whole rule** — which costs you the true positives too. So phrase it as
a decision procedure:

```bash
git diff --name-only A..B -- '*.go' go.mod go.sum    # then decide
```

Empty output ⇒ every `(cached)` line in that run is legitimately cached and the green
stands. Non-empty ⇒ the packages under those files must show a real timing. Evidence
from this entry's own merge: the doc-only integration printed `(cached)` where the
cache was **legitimately valid** (0 Go files changed), the merging manager forced a
fresh run anyway, and `internal/runtime` came back **23.293s** — inside the
23.2–23.4s band recorded above. The check would have licensed the cached line; the
forced run bought 23s of nothing. Re-run first-hand while writing this paragraph
(`go test -count=1 -race ./internal/runtime/`): **23.298s**, same band, so the
"identical signature" claim is not being relayed here.

**Scope the *specimen* to `go test` package results.** Staleness-by-build-cache is
*not* a general property of "re-running the child's command yourself." A **shell**
suite has no build cache and is structurally immune to *this mechanism* — one
verification the same day was unaffected for exactly that reason. Say so, or the
first counterexample gets used to dismiss the entry.

But do not over-read that immunity: it is immunity to the **mechanism**, not to the
**property**. A shell suite reaches the same place by a different route — see the
`pipefail` row in § *Eight surfaces, one property* below, where a real failure exits
0 because the verdict was lost in a pipe. Scope claims to mechanisms; the property
travels.

**Three independent instances, three subtrees, one day:**

| # | package | detail |
|---|---|---|
| 1 | `internal/runtime` | caught by a manager **on itself**, re-reading its own log after claiming independent verification |
| 2 | `internal/tui` | **41 cached packages**, including the exact package the change under review touched, in a run already reported upward as independent |
| 3 | `internal/runtime` | clean **but only by luck** — the changed package genuinely recompiled while **34 others** in the same run were `(cached)`. Nothing about the invocation differed; the package simply happened not to be warm |

Instance 3 is the one that makes the check non-optional: **cache state is not a
property you control**, so a clean run is not evidence of a clean habit. And
**none of the three was found by review — all three by someone re-reading their
own evidence.**

#### Eight surfaces, one property

The build cache is not the claim. The claim is the property, and it has now been
observed on eight different surfaces in this repo — which is a stronger statement
than any of them individually, because it predicts the *next* one.

**The number counts table rows, and the prose below the table exceeds it.** Two
further instances are developed there — an empty durable path (a generalisation of
the null-grep row) and a correct practice paired with a wrong rule (which
generalises no row at all) — and they are deliberately not tabled, because the table
lists *surfaces* and those two are instances on a surface already named, or on none.
Said plainly because this section tells you to prefer the named list to the number:
**the named list is longer than eight, and it is the authority.**

| surface | mechanism | what looked like success |
|---|---|---|
| a test harness | aggregator with no assertion-count floor (QUM-1029, QUM-1044) | `0 passed / 0 failed`, exit 0 |
| a shell pipeline | verdict lost when piped, `pipefail` unset (QUM-1038) | exit 0 for a failed run |
| **a build cache** | `(cached)` result reused across agents (this entry) | genuine exit 0, matching counts |
| a documentation reference | a cited SHA that has rotted | a SHA that still resolves — to the wrong tree |
| **shared tooling** | `merge` rebasing onto the *caller's* stale base (QUM-1050) | `Merged agent zone`, exit success, no warning |
| **a null grep** | searched a worktree that predates the code | zero hits, exit 0, no error at all |
| **a tracker title** | hypothesis in the body, stated as fact in the title (QUM-752) | a title that reads as a finding — and stood as one, uncorrected, for eight weeks |
| **a relayed measurement** | a `grep` result relayed through three agents to a human, re-run by none of the relayers | a measurement, quoted verbatim, still carrying the authority of a run nobody repeated |

**The null grep is the purest instance, and it is worse than the build cache.** A
cached green at least leaves a `(cached)` token in the log for anyone who looks; a
null grep leaves **nothing** — no error, no warning, no token — and it *feels like
evidence*, because a search that ran to completion and found nothing is
indistinguishable from a search that ran to completion over the wrong tree. Live
instance: `grep -rn "NewTicker\|redrain"` returned zero hits on a branch that
**predates the code being searched for**, and was reported as *"no seam exists"*
when the honest reading was *"not on my branch."* **Three agents acted on that null
result before anyone ran the one-command positive control.**

Note it is simultaneously a staleness failure and a negative-assertion failure —
the search was clean, the *base* was old — which is why the axes above compose.
**The rule, its positive control, and the worked instances live in § *A null result
is a statement about your search*; go there.** This row records only that the
property shows up on this surface too, and that here it shows up in its purest
form.

**It generalises past `grep`, which is worth one instance because the surface does
not look like a search.** A manager checked a QA agent's durable evidence path,
found it empty, and read that as *the agent has no evidence* — when what was
established is only *nothing was written where I looked*. **An empty durable path is
not evidence that an agent has no evidence.** Same rule, no code involved: the
instrument was a directory listing rather than a regex, and it was equally
unexercised.

**The documentation-reference row** is the one that generalises the entry beyond
testing: **a stale SHA either resolves to something or fails silently, and either
way it does not announce that it is out of date.** Two instances in one day, both
*inside the artifact written to prevent them* — a `(cached)`-trap draft whose own
recovery instruction pointed at a commit predating a correction to that same
draft, and a manager citing a SHA the merge had already rewritten **in the same
exchange in which it merged the rule against citing rotted SHAs** (§ *Which tree
is your claim about?* — "quote it with the SHA it was measured on"). Both times the
**author of the rule** caught it, not the party citing it.

**The shared-tooling row is the sharpest, because the tool was reporting on
itself.** `merge` rebases the child's commits onto the **caller's** base. With the
caller seven commits behind `main` and the child based on a newer integration
commit, it replayed four of another agent's commits through the caller's squash and
produced **one commit, authored by the caller, containing two slices of someone
else's issue and two unrelated commits besides** — then printed `Merged agent
zone` and exited success. The resulting tree was byte-correct. **Only the history
lied**, and the success message was not evidence of a correct merge.

**The tracker-title row is the property one layer further out**: it acts on the
*claim* rather than on a run, and what is indistinguishable is not a green but a
hypothesis from a finding. QUM-752's body is honest — *"Hypothesis: under fleet load
…"* — its **title** states fleet load as fact, and **no load figure was ever
recorded**: no loadavg, no agent count, just "5–10+ agents were in flight,
therefore load." **Titles are what get cited; bodies get read once**, and the
recommended fix — filed with the issue and still standing unchallenged eight weeks
later — was a timeout bump *"with a comment documenting the fleet-load rationale"*,
which would write the unmeasured cause into the tree, where it would then be cited as
established. Note precisely what the eight weeks measure: not that anyone re-derived
the claim, but that nobody had to. **An unchallenged title accrues authority by
sitting still.** So: **a hypothesis stated in a title is a finding by
the time anyone cites it — keep the uncertainty in the part that travels.** The
mechanical tell, which is checkable without knowing the subject: **if a claim names
a cause, the artifact should contain the measurement; if it doesn't, the claim is a
title.** Worth recording that the same load reflex was proposed independently by two
managers in one day, and that on the one occasion anyone measured it, it inverted:
QA measured **QUM-1053**'s analogous hypothesis about a *different* row
(`idle-interrupt-inject`) and found the failure at loadavg **0.22** while the two
passes sat at **2.10** and **4.24**.

**Those figures do not refute QUM-752**, and the comment that recorded them says so
in as many words — *"that is not proof this row's cause is also not load — different
rows, different failures."* They are quoted here for the narrower thing they do
establish: that "concurrent agents were running, therefore contention" is a
conclusion this fleet has now been wrong about at least once when someone finally
measured. Reporting them as a refutation of QUM-752 would be this section's own
defect — a measurement from one artifact promoted into a finding about another — so
the row they belong to is named every time they are cited.

**The hardest instance to catch is a correct practice paired with a wrong rule**,
because nothing downstream of the practice ever misbehaves. Recorded on **QUM-1055**,
which carries the artifact: an agent corrected where retire-time evidence should be
preserved and, in the same message, wrote a rule that named the wrong *source* for it
— when it had actually preserved that evidence, it searched the worktree, which the
rule does not say. (The issue records the consequence directly: a second agent
following the stated rule checked the named durable path, found it empty, and would
have concluded there was nothing to preserve while 460 KB sat in the worktree.) The action was right; the
generalisation drawn from it was not. **The rule is the part that propagates**: the
author keeps doing the right thing (so no failure ever surfaces to them, and they are
structurally the least likely person to notice), while the next reader inherits the
defective abstraction and has nothing to check it against. The remedy is the same as
everywhere else in this section — do not derive the rule from memory of the action;
re-read what you actually did, and state the rule from that.

**A measurement decays into an assertion by being transmitted, and every link looks
identical to the one before.** This is its own shape — not an instrument that failed,
not a spec that under-required, not an expired claim, not a wrong rule generalised
from a right action. The measurement was **correct when it was made**; what degraded
was its evidentiary status, and nothing in the text records the degradation. The
chain, with the actual links, because a named chain argues better than a
hypothetical:

> `audit` ran the grep and reported *"neither QUM-1033 nor QUM-1028 is cited
> anywhere in the tree (grep = 0)"* → **`command`** forwarded it as fact → **`weave`**
> forwarded it to **the human**, in its own words, as a finding → and the only
> re-derivation happened when **`zone`** re-ran it, *after* it had reached the person
> who would act on it. It was **half wrong**: QUM-1028 had three pre-existing
> citations, so the finding applied to QUM-1033 alone.

**Four links, three forwards, zero re-derivations until the end.** The rule:

> **A claim of the form "grep returns N" must be re-run by whoever repeats it.** A
> grep's entire authority comes from having been run. A relay strips exactly that
> while preserving the appearance — the quoted string is identical, so the second
> speaker sounds precisely as authoritative as the first and has done none of the
> work. Forwarding it unverified converts a measurement back into a rumour.

**That scoping is too narrow, and § *The second detection method* below records the
instance that proved it: an *analytically*-derived claim is weaker evidence than a
grep, and this rule did not cover it.** Read the widened form stated there — *a relay
rule must scale with the claim's fragility, not with its apparent precision* — as the
governing one.

Note the cost profile is the reverse of the mechanical shapes: re-running a grep is
the cheapest verification in this entire document — seconds, no setup, no
judgement — which is exactly why it gets skipped. **The relay is cheap and the
re-run is cheap, so nothing about the economics warns you.** File it under
transmission, not under carelessness; three of the four links were being careful.

Note what these last three shapes have in common with the mechanical ones above:
**nobody failed a check.** A title promoted a hypothesis with no error anywhere, a
correct action produced an incorrect rule, and a true measurement stayed
word-for-word intact while quietly ceasing to be evidence. The property holds across the whole
list — **indistinguishable from success at the point of observation**, where the
"observation" is sometimes a reader citing a title rather than a run printing `ok`.
(Prefer the named list to a count; both tallies in this section grew after they were
written down.)

#### The detection generalises too: check the footprint, not the content

That one was caught by a **file-level** check, which is the transferable part:

```bash
git diff --stat main...HEAD -- internal/    # which files does my branch claim to touch?
```

It keys on **files rather than content**, and that is precisely why it works where
a diff review does not. Someone reading 5,684 insertions *for correctness* sees
correct code — because the code **was** correct. Asking instead *"which files has
this issue any business touching?"* surfaced three wrong ones (`items_dim_test.go`,
`pendingzone.go`, `tuiadapter_test.go`) immediately. Content review scales with
diff size; footprint review is O(number of files) and answers a different question.

The confirming tell was a **memorised expected value**: `unified.go` at `+302/−2`
where the reviewer knew the audited figure was `+176/−1`. Record both halves,
because they are two instruments — the cheap file-level instrument, and the fact
that *a number you have audited often enough to know by heart is itself an
instrument.* An expected value you have to look up cannot fire on sight; one you
have internalised fires without being invoked. That is the only member of this
family that catches the failure **at** the point of observation, and it works by
having brought a second observation with you.

**The manager-side framing, which is the part to internalise:** on a shared
filesystem, a manager verifying a child's work is **by default reading the child's
cache back to itself** — verification that is indistinguishable from relaying, and
that reports as independent. The practice that actually worked here was
child-side: the engineer flagged `(cached)` density as "the bit worth checking" on
its own run, which is the only reason its claim survived the manager's check.
**Flagging the weakness in your own evidence is what makes it checkable by someone
else** — and across every instance above, the mechanical question beat the careful
reading.

That last point is the section's reason for existing, and it is not a claim about
anyone's diligence. Every instance here was propagated by someone writing this
guidance, reasoning carefully, at the time they were most alert to the failure mode:
three agents acted on a null grep before anyone ran the control; three more
propagated an unverified claim *about verification* before anyone read the assertion
order (§ *Provenance of the observed string* records that one); two cited a rotted
SHA inside the rule against rotted SHAs. **Inattention is not available as the
explanation**, which is precisely the argument for a named mechanical check over
more careful review: the check works when you are wrong about being careful.

#### The second detection method: audit a green on purpose

Everything above was found while investigating something else — a failure, a
suspicious claim, a review already under way. That is a real limitation of the
footprint check and of every tell in this section: **they all need you to already be
looking.** This one **can be run cold** — and, in the interest of not doing here what
the rest of the section warns about, **the one instance below was not**: it surfaced
while its author was adding a cross-reference to the surrounding code, same as
everything above. So the cold-start property is a claim about the *procedure*, not
an observation about its provenance. It is written up as a procedure rather than
counted as another instance because that is the form in which it can be run at all:

> **Pick a test whose name asserts a coupling. Then ask whether it reaches its
> precondition *through* the code under test, or *around* it.**

A precondition built *around* the code — hand-assembled by the test to resemble what
the real code would have produced — is the mechanism. The test then pins the
behaviour of **a fixture** rather than of the code it appears to guard, so it
survives any divergence in that code. It is green, it is *correctly* green, and it
is green about the wrong subject. Nothing distinguishes it from a test that means
something, which is this section's property arriving where a reader is least likely
to look: a passing test.

**Post-merge correction: the construction is not the discriminator.** This paragraph
first shipped as an absolute — *reaches its precondition around the code under test ⇒
false green* — and that absolute is **false**. It is falsified by the counterexample
in the worked example below: leg 2 of
`internal/runtime/qum1056_sweep_inflight_disjoint_test.go` is structurally the same
construction (a real replay-echo → the production `markConsumed` route to a consumed
`kind:system` entry, then assert it is still in flight; the delivery callbacks differ
— leg 2's `OnDelivered` fires, the supervisor test's is nil) and is a sound,
mutation-killing check.
Reaching a precondition by a different real route is ordinary and usually fine. What
separates the two is **what the test claims about itself**: leg 2 claims to guard the
*filter*, which is exactly what its route reaches; the test below claimed to guard the
*sweep*, which its route does not reach. So the procedure's question is two-part —
*what do the name and doc comment say this guards, and does the construction reach
that?* — and the finding is the **mismatch**, not the substitution. Widened this way
it survives contact with the common case, which the absolute did not.

**Worked example — and read it as a retraction, because the method's first published
catch was wrong about its own catch.** The first version of this subsection called
`internal/supervisor/weave_handle_test.go`'s
`TestWeaveRuntimeHandle_ConsumedStateStaysSuppressed` a recorded false green. **It is
not one, and the error was this document's signature error: nobody ran the mutation.**
Run it — narrow `InFlightSystemEntryIDs`'s predicate from `e.state == stateCancelled`
to `e.state != statePending` — and **two** supervisor tests fail. Verbatim, as
printed:

```
--- FAIL: TestWeaveRuntimeHandle_WakeForDelivery_ConsumedButNotYetDelivered_NoDuplicateWrite (0.27s)
    weave_handle_test.go:718: entry written 2 times across 2 stdin writes, want exactly 1 — a poke inside the consumed-but-not-yet-delivered window duplicated the notification
--- FAIL: TestWeaveRuntimeHandle_ConsumedStateStaysSuppressed (1.12s)
    weave_handle_test.go:931: a consumed entry left the in-flight set — a poke would now re-write content already in the conversation
```

(Long lines left unwrapped on purpose: each `t.Errorf` is emitted as **one** line, and
a reflowed paste is not verbatim — the em-dash continuation would read as a separate
diagnostic. A quotation that cannot be located by its own wording is item 2 of § *Three
defects in this entry* one level down.)

Its assertions are live, and it kills the same mutation that leg 2 of
`internal/runtime/qum1056_sweep_inflight_disjoint_test.go` kills.

**The real defect is one sentence, and it is a mislabel, not a vacuous assertion.** A
provenance comment in that test (`weave_handle_test.go:918`) calls the state it
constructs *"exactly the state a `settleNeverAcked` sweep leaves behind"*. It cannot
be: the sweep is `kindUser`-only (`internal/runtime/unified.go`) and the entry here is
`kind:system`. The sentence claims the test speaks for the **sweep**; it speaks for
the **filter**, which it genuinely guards. What it substitutes is the *route*, not the
data — the supervisor mock's `echoReplay` → the production `markConsumed`, with
`OnDelivered` nil, which has the opposite delivery semantics from the sweep's. **A
mislabelled provenance sentence** is a class worth a detection method, because nothing
else in this file catches one.

**The failure chain has three named links, and each one skipped a one-command check.**
Naming them is deliberate: *"someone didn't verify"* is forgettable, a chain with
three named links is not.

1. **`zone`** (engineer) derived three surviving divergences — the sweep widening its
   kind predicate, adopting a new state value, or beginning to call `OnDelivered` —
   **analytically, from reading both predicates**, and labelled them unexercised. An
   accurate label on a claim that should not have been made.
2. **`command`** (manager) relayed it upward as **measured**.
3. **`weave`** (root) converted it into a **named detection method** with a written
   procedure, directed it into this file, and reported it to the human.

So the escalation was relay **with generalisation**: one unverified observation became
a method, which is a strictly larger claim than the one received. The engineer's own
sentence on retracting it, verbatim, because it is the shortest statement of the
failure:

> **"Flagging a claim as unexercised is not the same as not making it."**

The rule and its violation are two consecutive commits apart **in this file**: the
relayed-measurement row above landed at `81f028e`; the unexercised-flag sentence
landed at `83a154e`, the very next commit.

**And the relay rule needs widening, because it was scoped exactly backwards.**
§ *Eight surfaces, one property*'s rule covers *"grep returns N"* — a claim whose
authority comes from having been **run** — and explicitly not *"I read two predicates
and concluded X."* But a grep **was** run. An analytic derivation never was: it is
strictly weaker evidence, and it travelled with *more* confidence than its author had,
while the rule guarded the stronger class. Hence:

> **A relay rule must scale with the claim's fragility, not with its apparent
> precision.** Mechanically-derived claims *feel* checkable and so invite a re-run
> rule; analytically-derived claims feel like reasoning and so invite agreement. The
> second is where the rule is actually needed.

QUM-1056 carries the assertion that closes the underlying gap, and that claim survived
the retraction: under the mutation above, leg 2 is the **only** failure in
`./internal/runtime` — and before that file existed there were **none at all**, which
is the coverage gap itself. So the cross-package claim stands even though the
false-green claim does not. (Measured **with** the file present, obviously; a
pre-existence measurement of a test that lives in the file would be incoherent, and an
earlier draft of this sentence compressed the two clauses into exactly that.)

The procedure's value is directional: it tells a reader **where to look** when
nothing is failing, which is the state most of this file's failures were discovered
in and none of its other tells can be run from. The cost is that it is not
mechanical — `grep` cannot tell a proxy precondition from a real one — so it is a
reading habit, not a check, and it should not be cited as though it were one. **One
confirmed catch, and it turned out to be a mislabel rather than a false green: this
is a lead, not a track record.** Its one non-negotiable step is the one that was
skipped — **run the mutation before publishing the catch**, since the method's output
is a suspicion and the mutation is what converts it into a finding or kills it.

#### Three defects in this entry, found after it merged

Recorded rather than quietly fixed. All three are defects **in the artifact about
defects**, found by **three different readers**, *after* the entry merged — the
retraction in § *The second detection method* above plus the two below. **An entry
that carries its own post-merge corrections is more credible than one that arrived
clean**, and it is the honest record.

**1. A fix can relocate a defect outside its own detector's range.** A dangling
cross-reference in this file — it pointed at *"A grep that matches your vocabulary has
not necessarily found your mechanism"*, which is a blockquote, not a heading — was
repaired at `81f028e` by repointing it at § *Claims about code, and claims about
claims*. That heading is real; it is also the **wrong** one — the quoted rule lives in
§ *Which tree is your claim about?*, where `83a154e` finally pointed it. Watch what
happened to the measurement: dangling refs went **down**, and any checker that
resolves references against the heading list now reports the file **clean**. Dangling
is **loud**; resolves-to-wrong is **silent**. *The fix made the failure quieter while
the failure survived, and the metric improved on the way out.* Corrective, and it is
mechanical: when repairing a reference, **derive the target from the containing
heading of the quoted text** — locate the text, walk up to its nearest `###` — rather
than picking the heading that sounds right.

**2. The family's first false *alarm*, and it was in the checker.** A `§`-reference
checker written to audit this file reported **ten** dangling references. All ten were
fabricated, one of them naming a heading plainly present in the file; the mechanism was
a broken `while read` over process substitution plus title truncation, so every
reference to § *Provenance of the observed string* (heading: *"…: who mints the
artifact? (QUM-925)"*) read as dangling. Re-run with whitespace normalisation and
prefix matching: **0 dangling, and the negative control fired** — an injected reference
to a deliberately nonexistent heading was reported, exactly once. The class is not a
slip: a second, hastily written checker independently reproduced both mistakes (11
fabricated reports) before the same two fixes, so this is the default behaviour of a
naive title matcher.

Three notes, all self-applications of rules above. **The verdict is quoted without a
reference count on purpose**: the count moves with every edit to this file — 33 before
the commit that added this subsection, 40 at that commit, 42 one review round later —
and a draft of this sentence published the pre-edit **33** *after* the edit, i.e. a real
measurement of a tree that no longer existed. Quote the verdict; re-derive the count.
§ *Which tree is your claim about?* is the rule, and its *"a suite-size figure is
branch-relative and rots within hours"* blockquote is the same shape one artifact over.
**Second:** that clause first cited the blockquote *as a heading* — the exact defect of
item 1 above — and the checker described in this paragraph caught it before the commit
landed, which is the loud failure mode working as advertised. **Third:** the two bad
references named in this subsection are written in prose rather than in `§`-reference
form on purpose, so a future run of such a checker does not flag the paragraph
describing the defect. Two halves, and this section had only ever exercised one of them:

> **A negative control proves the instrument *can* fire; a positive control proves it
> can *stay quiet*.** A false-green instrument fails the first; a false-alarm
> instrument fails the second — and everything else in this section is about the
> first.

The cost profile is inverted from every other instance here, which is why it gets its
own paragraph: a false green costs *confidence*, silently and on the author's side; a
false alarm costs *someone else's time*, loudly — this alarm was handed to another
agent to chase. And the tell was not a control:

> **"I caught it only because the result was implausible — and implausibility is not a
> detector."** — `command`

**3. Unqualified-prohibition audit (bounded).** Prompted by defect 1, every entry added
to `/testing-practices` and `CLAUDE.md` on the same day was re-read for rules stated as
absolutes. Two confirmed and fixed: the *"reaches its precondition around the code ⇒
false green"* absolute in § *The second detection method* (falsified by its own worked
counterexample), and the *"never accept a cached line"* shape, restated as a check in
§ *The specimen* above. Two further absolutes were reviewed and **kept** — *"the remedy
is never 'look harder'"* and *"a liveness gate that **warns and continues is strictly
worse than no gate**"* (quoted as written, so the wording is greppable) — because each
is a claim about a mechanism for which no exception has been
found, not a prohibition on a common legitimate case; if you find the exception, weaken
them the same way. Scope stated because it bounds the claim: **that day's entries only**
— this is not an audit of the rest of this file.

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
tests — **throwaway agent orchestration is one of the four strata** the instances
fall into (§ *The honest limit*), not a footnote to them. The cheapest example,
and one that will be stepped on again:

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

### Necessary but not sufficient: a watched failure proves the instrument works, not that it measures the right thing

**A watched failure proves the instrument works, not that it measures the right
thing.** Red-first is necessary and **not sufficient**. An assertion can fail for
a reason you chose, on behaviour the correct design does not have — and in the
transcript that is indistinguishable from one that caught something real. Two
instances from the QUM-1105/QUM-1087 series, both by the same author, days apart:

* An assertion written against the derived squash message's trailer block was
  watched failing, and what it pinned was **a blank line the correct design does
  not emit**. The failure was genuine, the instrument worked, and the measurement
  was of nothing.
* An argument-order assertion (`merge-base --is-ancestor <parent> <branch>`) was
  watched failing red-first, and its comment then claimed a swap "leaves every
  other assertion green" — inferring, from the one red it had seen, that it was
  the *only* guard. The negative control refuted that: swapping the arguments
  also failed four real-git scenario tests, because post-rebase the parent is a
  strict ancestor and the reversed question answers false. The claim in the
  comment was false while every individual observation behind it was true.

So after watching red, state separately **what the assertion would let through**,
and prefer a control that mutates the **production** behaviour you care about
over one that mutates the test. The sharpest form is a mutation that leaves every
other assertion green: if exactly one test fails, that test is the one carrying
the claim. **Write the prediction down before running the control** — the second
instance above was caught only because the prediction was recorded and turned out
not to match, and a prediction formed after seeing the output cannot fail to
match. If nothing fails, the claim is unguarded no matter how much red you have
already seen.

Three near neighbours in this file make adjacent but distinct claims; keep all
four and do not collapse them. § *A mutation you didn't verify landed is not a
mutation* is about a mutation that never happened, where this section is about
one that happened and measured nothing. § *Name the site, not just the string*
already observes that mutating the other match leaves the test green, but as a
caution against a false finding; here that same observation is promoted to a
positive technique. § *Check that the question the command answers is the
question you are claiming* carries the `--is-ancestor` argument-order hazard as a
general rule, where the instance above is about the *comment's*
over-generalisation from a single red. And the section immediately below
constrains the **fix**, where this one constrains the **measurement**.

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

### A precondition that never holds makes the guard a no-op

The failure mode above is *silent*. This one is **loud and misfiled**, which is
worse, and it needs a different remedy: when a harness fails in **setup**, the
assertions it exists for never run, and the report looks like an intermittent
fault rather than a hole in coverage.

> **A row must distinguish "the scenario ran and passed" from "the scenario never
> started."** Report setup failures in a **separate class from assertion
> failures**, so a **setup precondition** that never holds cannot masquerade as
> flakiness. When a row fails in setup, the
> correct reading is *"this guard is currently a **no-op**,"* not *"this row is
> **flaky**."*

Live instance, verified with a control build in an isolated worktree (auth ruled
out — `Not logged in` and `claude binary not found` both zero): the `sendnow-tui`
row fails at `FAIL: iter 2: weave never entered a tool-bound turn` on both an
engineer's HEAD and the control. `scripts/e2e-tests/sendnow-tui.sh:241` shows that
assertion is a **setup precondition** guarding the busy turn the repro needs, and
it aborts the row — so the 8-iteration Ctrl+G double-tap repro the row exists for
**never ran to completion on either build**, and the row has never certified the
path. (Iteration 1 apparently did complete; a single unrepeated pass then a setup
abort is not the 8-iteration gate anyone reads the row as, and nothing in the
report distinguishes the two.)

State the consequence at the width you measured, which is narrower than the first
telling of it. The handed-down version was *"QUM-830 has no coverage on `main`"*;
checked against the tree, `main` does carry
`TestSendAllNow_NowWritePreemptMidTurn_SurfacesInterruptNotError`
(`internal/runtime`) and `TestCtrlG_DoubleTap_Debounced` (`internal/tui`), and
both pass. What is unguarded is the **live keystroke path the row alone
exercises** — reducer-level classification is covered. Same discipline as
*a rationale you were given is a claim about intent*: verify the blast radius
before you write it down, including when the claim arrives with the defect.

Note what did *not* go wrong. Nobody ignored a failure; the signal was present,
visible, and read carefully — and filed as "flaky row" rather than "the thing this
guards is unguarded." A vacuous assertion hides; this one announces itself in the
wrong vocabulary, and the vocabulary is the whole defect.

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

This is *the same breath* at mechanism level, and the instance count is what makes
it a pattern rather than a slip: **three self-pinning tests on one flag**, all
named for what `interruptPending` does rather than for what the user sees —
`TestInterrupt_AtTurnBoundary_WireRunningDoesNotClearArm`,
`…_ArmDoesNotSurviveInit`, and `…_ArmDoesNotLeakToNextTurn`. **All three pass**,
which is exactly what makes them dangerous: a failing self-pinned test gets argued
with, a passing one gets banked as coverage. (Counts scoped separately, because
they are two different measurements: *three* tests keyed to the flag, and the
`…_ArmDoesNotSurviveInit` site specifically ratified twice — the QUM-931 symbol
landed at `6d13e6a` and again at `0ab763c`.)

Sub-lesson, which surfaced only in-process: a retire keyed on **entry to a phase**
looked equivalent to one keyed on **a real new submit**, and was not — a guard
re-arm re-enters that phase for the arm's *own* turn. **"Looks equivalent" is not
equivalent** when the signal you key on is a phase rather than an identity.

The remedy that worked: **supersede it with a test keyed on the property, not the
detail.** Assert that the armed turn *closes* — the outcome — rather than that a
particular `init` frame does or does not clear a particular flag. A mechanism
assertion has to be rewritten every time the mechanism changes, which is exactly
when you least want the guard rewritten by whoever is changing it.

### Mutate along the axis your assertion constrains

Companion to the previous section, and the counterpart to § *The non-asserting
fallback*: that shape has **no failure arm**, this one has a real failure arm
whose **predicate is too weak** — so it fails loudly for the wrong inputs and
silently accepts the defect. (A third, § *Provenance of the observed string*, has a
fine predicate pointed at the wrong process; a fourth, § *Indistinguishable from
success*, has both right and reads a run that never happened; a fifth, § *A null
result is a statement about your search*, has nothing wrong with it at all and an
instrument that could not have observed the thing.) The canonical table of all five
is in § *Provenance of the observed string*; this list is a pointer to it, so if the
two ever disagree the table wins. All read as coverage; none is.

For a **styling** requirement, assert the specific SGR parameter set — not that
two renders differ. QUM-925 asked that a pending row render *dimmer*; six tests
asserted `dim != bright`, which `Underline(true)` also satisfies while making
pending **more** prominent than committed — the exact inverse of the requirement.

The transferable part is *why the controls missed it*: **three mutation controls
all mutated colour, while the hazard was attribute.** A control that varies a
different axis than the one your assertion constrains proves nothing about it. So
pick controls on the requirement's own axis, and assert the parameter, not the
difference. Reusable shape — `assertDimIsFaintDelta` in
`internal/tui/items_dim_test.go` — is a **bidirectional set diff** over SGR
params: exactly `{2}` added, nothing dropped. That rejects `Underline`, `Reverse`,
a foreground shift, and a no-op alike.

Corollary from the same issue (F3): an assertion of the form "X was added" cannot
detect that **X is the only differentiator**, which matters when X is advisory —
SGR 2 is ignored by some terminals, so a faint-only delta degrades to nothing and
a locked requirement is silently void. Pair it with its complement: strip all SGR
from both renders and assert the plain text still differs. The two are in tension
by design; both holding is the requirement.

### A null result is a statement about your search, not about the code

The previous section with the **sign flipped**: same rule applied to a *negative*
conclusion, and the negative case is the more dangerous one, because a null result
*feels* like evidence and produces no error for anyone to notice.

> **An absence in a worktree of unknown base is not evidence about the code.** A
> claim resting on a null grep needs a positive control before it leaves the
> worktree.

The null grep is the common case and the least suspected: printing nothing is a
fact about your *search*, not about the codebase, until you have shown the search
could return non-zero. **The positive control is the whole remedy and it is one
command** — grep for something you *know* is on that branch. It costs seconds;
skipping it is what let a null travel to two other agents as a finding.

Two instances, one day, both conclusions **inverting** once the control was added:

- a `capture-pane` probe that printed nothing *before* entering the alt screen, so
  `PREALT` had no way to be non-zero — "the pane has no scrollback" read as
  measured when it was unexercised;
- a `grep` for a seam on a base predating the code under discussion: "no seam
  exists" was really "not on my branch." The seam was there, `atomicDuration` and
  all.

Three neighbouring axes, easily conflated: § *Provenance of the observed string*
asks **who** produced the artifact, QUM-1047's companion entry asks **when** it
was produced, and this one asks **whether your instrument could have observed it
at all**.

Derived independently by two agents from two unrelated surfaces, which is why it is
written down — and in both cases **the claim had already been acted on by others
before the control was run.** That cost, not tidiness, is the argument.

### Name the property before you name the probe

§ *A null result is a statement about your search* and § *Mutate along the axis
your assertion constrains* are both about an instrument that could not see. This
one is about an instrument aimed at the wrong thing, which is the failure those
cannot catch: the probe fires, returns a true answer, and the answer is about a
different proposition than the one you publish.

> **Write the sentence you intend to publish, in behavioural terms, before you
> choose the search.** Then check that the sentence's subject and the search's
> subject are the same noun. If they are not, the search cannot settle the
> sentence, however carefully you run it.

**Prefer the property over the countable proxy.** "Is this behaviour tested" and
"does a file with this name exist" are different questions, and the second is the
one that is easy to run. Substituting the proxy for the property has produced
several of the findings this repo has had to retract — the recurring one being a
companion-test convention read as `foo.go → foo_test.go`, which reports gaps that
do not exist, because tests routinely live in a differently-named file in the same
package. The proxy is not merely weaker evidence than the property; it can be
false while the property holds.

And keep the control discipline pointed the right way: **before trusting a
negative result, prove the probe can produce a positive one.** A negative control
that shares the probe's defect is not a control — if the probe is blind, it is
blind on the control too, and both come back consistent. Only a positive one
discriminates.

### Check that the question the command answers is the question you are claiming

The dangerous case is not a wrong command. It is a **correct command, returning a
true result, answering an asymmetric question in the convenient direction while
you report it in the desired one.** Running it more carefully does not help,
because there is nothing wrong with the run.

The clearest specimen is an ancestry check after a rebase.
`git merge-base --is-ancestor main <branch>` asks *is `main` contained in my
branch* — "I am rebased up to date". Reverse the argument order and it asks *did
my commits land on `main`*. Both are one-line commands, both exit 0 on success,
and they are different claims. The same shape recurs wherever the relation has a
direction: containment, ancestry, subset, "A implies B", "the fix is in the
binary" versus "the binary has the fix's marker".

> **Reread the argument order — or the subject of any check you did not design —
> against the sentence you are about to write.** Not against the sentence you
> meant, and not against the intent you were handed: against the words that will
> ship.

The corollary for reviewers: a check someone else wrote and you are citing is a
check whose direction you have not verified. Cite it only after you have read what
it asks, not what its name suggests it asks.

**The sibling failure is a check that is necessary and not sufficient, reported as
if it settled the claim** — a true result about content where the *wiring* is what
matters. In the recovery procedure this rule came out of, a branch built on the
wrong base carries a byte-identical tree, passes every content comparison, and is
still not attached to `main`; the content checks were all true and none of them
was the claim. Distinct from § *Necessary but not sufficient: constrain the fix,
not just the symptom*, which is about a fix too narrow for the class — this one is
about a *verification* too narrow for the sentence. `/git-recovery` carries the
worked instance.

### New render-affecting state is a stale-cache bug by default

`renderEnvelope`'s cache key is `(width, expanded)` and every item reports
`Finished() == true`, so **any newly added state that affects rendering is served
stale from cache unless something explicitly invalidates it.** `ZoneSettle`'s
unconditional `env.cache = nil` is load-bearing and was **untested** — deleting
that line stayed green until QUM-925 added a test that renders before and after
the flip.

General form: a cache whose key omits a render input needs an explicit
invalidation *and* a test that fails when the invalidation is removed. Removing
the line is the control; if it stays green, the test is measuring the flag, not
the render.

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

### A capture taken at a geometry no user has cannot see the class (QUM-1086)

Terminal-rendered output is only correct **at a size**. An evidence capture of
it that does not state its geometry is not reproducible and, worse, may be
structurally blind to the defect it was taken to rule out.

QUM-1086's config error printed the offending keys first and the recognized-key
reference table second. The issue's own evidence capture was taken at
**200x50**, where the whole message fits, and was green. QA re-captured with
tmux `capture-pane` at **80x24** — the common floor — and the table alone wraps
to 26 rows, so cobra's usage block *and* the entire actionable half had scrolled
off. What survived was the list of *valid* keys with no indication which of the
user's was wrong: the deliverable half-defeated, in code that had just passed
review at the larger size.

The rule, and its two corollaries:

* **State the geometry in the capture.** `capture-pane` at 80x24, named as such.
  A capture that does not say is evidence about an unknown configuration.
* **Verify at the floor, not at your terminal.** Your terminal is not the
  environment under test; picking the size that fits is the same move as picking
  the fixture that passes.
* **Reason at the physical-row level, not the logical-line level.** 15 logical
  lines was 26 physical rows here. An assertion counting `\n` is measuring a
  different quantity than the one that determines what the user sees — see
  **Mutate along the axis your assertion constrains**.

This is the same shape as a green e2e-matrix run against the wrong rows
(CLAUDE.md's gate-derivation rule): the run is genuinely green, the command was
genuinely correct, and it answers a question adjacent to the one being claimed.
Note also that a first attempt at pinning this in `internal/config/errors_test.go`
measured the budget *relative to the table* rather than from the end of the
message, which made it green both pre- and post-fix — inert while looking like
the physical-row check. The assertion has to be anchored to the thing that
actually scrolls.

### The honest limit

Those instances are spread **near-evenly across four strata — committed harness
code, committed product code, ad-hoc agent tooling, and the coordination/claim
layer**; none of the four is a rounding error. No denominator is quoted on
purpose: a proportion rots the same way the count did, only slower, and the last
group has **grown fastest** — so any fraction stated today is drifting as you read
it. That last group is also the one **no mechanism catches**. Everything
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

- **Tests required**: every file in `cmd/` **and** `internal/` has a
  corresponding `_test.go` — e.g. the command file `cmd/foo.go` has
  `cmd/foo_test.go`. Keep it that way.
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
