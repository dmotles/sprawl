# E2E matrix obligation — waiver record

**Date:** 2026-08-07
**Branch:** `dmotles/docs-restructure-audit`
**Base:** `main` @ `3fc837d`
**Authorized by:** dmotles, this session, via weave.
**Rows discharged: 0.**

## The rule being departed from

CLAUDE.md, § *Validating Changes*, mandatory-test e2e harness:

> **The obligation is a property of the commit, not of one file in it.** Derive
> over every path in `git diff --name-only`, **including a delta that is
> comment-only.**

and:

> **Obligation and coverage are different questions — answer the obligation
> first.** [...] over-running costs a CI slot, under-running ships the defect
> **and comes back green either way**.

This branch owes rows under that rule and does not run them.

## Mechanism — why a docs branch owes 25+ rows

`flux`'s D4 de-link pass rewrote **doc-path references inside code comments** in
20 Go files, many of which sit under gated paths. Example, `internal/supervisor/real.go`:

```
-// docs/designs/messaging-overhaul.md §4.2.3 / §4.7.
+// docs/archive/designs/messaging-overhaul.md §4.2.3 / §4.7.
```

Every such edit is a comment line. The rule above is explicit that this still
owes the row, and that is the correct rule: a reader cannot distinguish a
verified narrowing from a careless one.

**Exactly one Go file in this branch has a non-comment change:**
`cmd/skills_dead_api_test.go` (`byte`'s dead-API guard). It is a `cmd/` test
file and appears in no row of the table.

## Owed row set, derived at this commit

Derived by the union rule against the table **as it stands in `CLAUDE.md` at the
commit this record is part of** — not from an earlier estimate and not from a
list handed over by anyone. Literal path matches, then glob and directory-prefix
rows checked by hand, then rows pulled in by other rows' `re-run` clauses.

**23 rows by literal path match:**

`ask-user-question`, `busy-queue-typing`, `complete-lifecycle`,
`death-observability`, `drain-row-inject`, `esc-interrupt-survives`,
`idle-continuation`, `idle-interrupt-inject`, `merge-reuse`,
`notif-stacked-restart`, `pause-lifecycle`, `paused-persistence`,
`pending-dim-bright`, `qum1000-refused-slash`, `recall-sendnow`, `replay-echo`,
`report-then-send`, `sendnow-tui`, `subagent-model`, `usage`, `viewport-resync`,
`wake-live`, `wake-on-traffic`

**+2 by glob / directory-prefix, checked by hand** (a literal path grep does not
match these, per the table's own warning):

| row | glob | matching changed files |
|---|---|---|
| `handoff` | `internal/supervisor/*.go` | 5 |
| `hub-e2e` | `internal/hub/*.go`, `cmd/hubd/*.go` | 4 |

**+2 pulled in transitively by `re-run` clauses** in rows already owed —
`notif-stacked-restart` re-runs `tui-live-render`; `idle-continuation` re-runs
`notify-tui`:

`notify-tui`, `tui-live-render`

**Total owed: 27 distinct rows. Discharged: 0.**

This corrects an earlier estimate of "~25" made against the previous base
(`fb179e3`). The figure moved because the base moved and because the glob and
re-run legs were derived by hand rather than estimated.

### Not part of this waiver

The `Makefile` row is **not an e2e row** — it directs to `make test-race-gate`,
which is part of `make validate` and **did run**. It is discharged, not waived.

## What was run instead

`GOFLAGS=-count=1 make validate` on this tip, exit code captured.
**0 packages cached, 43 packages ok, exit 0.** The `-count=1` matters: a plain
`make validate` reported `(cached)` for all 43 and discharged nothing.

## What this does and does not establish

It establishes that the unit suite passes under `-race` with no cached results.

**It does not discharge a single owed row.** The judgment that defect risk here
is approximately zero — every Go edit but one is a comment, and the exception is
a test file outside the table — is a *coverage* judgment. It is recorded here as
a judgment and **it retires nothing**. If a defect in this branch is later traced
to any of the 27 rows above, this file is the record that the row was owed, was
identified, and was deliberately not run.

## Decision

Landing all 35 commits as one branch was chosen over two alternatives that were
both on the table: **splitting the branch** so the comment-only Go edits could
land separately, and **running the rows**. dmotles chose to land as-is and record
the waiver. In his words when confirming:

> comments are not worth re-running e2e.

That is the actual reasoning, recorded here rather than only its outcome — a
later reader can disagree with it on its merits, which is not possible if only
the decision survives.
