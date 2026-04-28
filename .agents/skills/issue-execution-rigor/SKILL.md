---
name: issue-execution-rigor
description: "Use when implementing a non-trivial Linear issue in this repository. Follow a phase-gated workflow: plan, red tests, green implementation, validation with end-to-end tests, reflection, and closeout, with sub-agent review at each phase."
---

# Issue Execution Rigor

Use this skill for non-trivial implementation work, especially Linear issues like `QUM-351`.

Do not use it for:
- tiny docs-only edits
- pure brainstorming
- one-off questions about the codebase

## Goal

Execute implementation work with explicit phase gates and independent review. The standard is:

1. plan first
2. tests first
3. minimum implementation to go green
4. real validation, including end-to-end coverage where applicable
5. reflection before closeout

## Companion Skills

Load the repo skills that match the work before changing code:

- `linear-issues` for issue state and comments
- `testing-practices` before writing tests
- `go-cli-best-practices` for Go CLI changes
- `cli-ux-best-practices` for CLI behavior/output changes
- `tui-testing` for `sprawl enter` / TUI changes
- `e2e-testing-sandboxing` for sandboxed end-to-end validation

## Required Phase Gates

### 1. Setup

- Read the issue and the relevant code paths.
- Set the issue to `In Progress` and leave a pickup comment if you are actively working it.
- State the first step before doing substantial work.

### 2. Plan

- Inspect the real code before proposing changes.
- Write a concrete plan:
  - implementation slices
  - risks
  - verification strategy
  - required end-to-end tests
- Have a sub-agent review the plan.
- If the review finds real gaps, revise the plan before proceeding.

Do not start coding before the plan exists.

### 3. Red

- Write tests first for the intended seam or behavior.
- Run the narrowest useful test target.
- Confirm the failure is real and matches the intended gap.
- Have a sub-agent review the red tests and failure shape.
- Fix weak tests before implementing.

Do not skip the red phase by writing tests after the fix.

### 4. Green

- Implement the minimum change that makes the red tests pass.
- Keep scope tight to the issue.
- Re-run focused tests until green.
- Have a sub-agent review the implementation.
- Address feedback worth fixing before broad validation.

### 5. Validation

- Run `make validate`.
- Run all mandatory repo-specific end-to-end tests for touched surfaces.
- Final QA must include end-to-end tests for user-visible runtime paths. Unit tests are not enough.
- Investigate every failure and classify it with evidence:
  - product regression
  - test/harness drift
  - unrelated pre-existing failure
- Have a sub-agent review the validation evidence.
- If the reviewer raises a real concern, address it and re-validate.

### 6. Reflection

- Ask what should improve that is out of scope for the current issue.
- Distinguish:
  - already tracked work
  - new follow-up candidates worth filing
- File Linear follow-ups when the gap is concrete and actionable.

### 7. Closeout

- Update the Linear issue with what changed and how it was validated.
- Commit with a good message.
- Mark the issue `Done` only after validation and issue comments are complete.

## Sub-Agent Review Standard

At each gated phase, ask a sub-agent for review of the artifact from that phase:

- plan
- red tests
- green implementation
- validation evidence

The sub-agent should review the artifact, not redo the whole task.

If the reviewer has actionable feedback, address it and confirm the reviewer is satisfied before moving on.

## Validation Floor

Minimum expected validation:

- focused tests for the changed area
- `make validate`
- mandatory e2e commands required by repo instructions and touched surfaces

For TUI or orchestration work, include the full relevant end-to-end path, not only unit or quick tests.

If an end-to-end test fails but the product appears correct, gather enough evidence to justify calling it harness drift before closeout.

## Example Invocation

Use this workflow for `QUM-351`:

```text
Use $issue-execution-rigor for QUM-351.
Follow plan -> red -> green -> validation -> reflection -> closeout.
Require sub-agent review at each phase.
Run mandatory end-to-end tests before calling it done.
```

## Final Check

Before declaring the work done, ask:

- Did I write and review a plan before coding?
- Did I prove the tests were red first?
- Did a sub-agent review each phase?
- Did final QA include end-to-end validation?
- Did I capture follow-up improvements instead of letting them disappear?
