# DRAFT — CLAUDE.md insert: agent safety (Task 2 of D6)

**Not for merge as a standalone doc.** This is a paste-ready block for CLAUDE.md,
staged separately because CLAUDE.md is three-way contended. Delete this file once
the block lands.

**Suggested placement:** immediately before `## Build & Test`. It is a standing
constraint on how you work, not a procedure, so it should precede the procedures.

**Cost:** 9 lines including the heading and the blank line after it.

---

## PASTE THIS

```markdown
## Working alongside other agents

You are probably not the only agent on this host. Others hold worktrees on the
same repo and share one filesystem, one lint lock, and one `claude` auth.

- Don't kill processes you didn't start, modify shared branches, or touch files
  outside your worktree.
- **Destructive-var guardrail:** `rm -rf "$VAR"` — or any destructive command
  driven by a shell or env variable — is forbidden unless the immediately
  preceding line asserts the path, e.g. `[[ "$VAR" == /tmp/* ]] || exit 1`.
  Variables get unset, inherited from the wrong shell, or point somewhere you
  did not expect. Assert, then delete.
- Never bypass a failing check to reach green — no `--no-verify`, no `|| true`,
  no `-` prefix on a recipe. Fix the cause.
- **A red run is not always your diff.** Shared-host failures are common enough
  here to have their own catalogue: match the literal error text against the
  `false-red` skill before you revert, retry with a bypass, or report a
  regression.
```

---

## Why this text, and why this little of it

**What it replaces.** The first two bullets exist today only as Go string
constants — `engineerExecutingActionsSection` and
`managerExecutingActionsSection` in `internal/agent/prompt_child_sections.go`.
Researcher and QA prompts contain neither. That has two costs: changing the
guidance needs a rebuild, and no role can see the guidance any other role is
operating under. Moving it to CLAUDE.md fixes both, and makes it universal
rather than per-role, which is what it always should have been — nothing in the
destructive-var guardrail is about writing code.

**What was deliberately dropped.** The Go constants also carry a longer
"examples of actions requiring extra caution" list (force-push, `reset --hard`,
amending published commits, posting to external services) and a "measure twice,
cut once" close. Those are general good practice that a competent agent already
has; the guardrail is specific, mechanical, and non-obvious, and it is the part
that has actually prevented damage. In a file heading for ~75 lines, generic
caution is the first thing that should lose its seat. Leave the fuller text in
the prompts for the roles that have it — this block does not need to be its
superset to be worth landing.

**The last bullet is load-bearing, not a cross-reference.** QA is ordered to run
`make validate` and, of the four roles, is the one operating with the least
context about what else is running on the host. It is therefore the role most
likely to read a contention failure as a defect in the work it is reviewing —
which is the single worst place for that error, because a QA false-negative
verdict costs an engineer a rework cycle on a change that was correct.

One correction to the brief that motivated this draft: QA is **not** entirely
uninformed about concurrency. `qaVerificationProtocolSection` step 4 already
says "If validate reports lock contention, retry with backoff — do NOT bypass."
So the lint lock specifically is covered for QA. What is missing is everything
else — disk exhaustion, stripped auth, the harness cleanup race — and the fact
that the one hint QA does get is invisible to every other role and to anyone
reading the repo. The bullet above generalises it rather than introducing it.

**On the breadcrumb's form.** It names the literal skill and tells the reader
what to match on (*the error text*), because "see the docs" is the instruction
people skip. If the `false-red` content lands somewhere other than a skill, this
bullet needs the path corrected — it is the only line here with an external
dependency.
