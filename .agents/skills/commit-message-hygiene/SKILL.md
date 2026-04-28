---
name: commit-message-hygiene
description: Use before creating or amending a git commit in this repository. Write the subject as a high-level summary of what changed; put why, constraints, and validation in the body when they matter.
---

# Commit Message Hygiene

Use this skill before any `git commit` or `git commit --amend`.

## Goal

Write commit subjects that say what changed at a high level. Do not spend the subject explaining why the change was made. If the why matters, put it in the body.

## Workflow

1. Inspect the staged diff, not your memory of the work.
2. Check recent `git log --oneline` output if you need to match local style.
3. Identify the highest-level change that a reviewer would care about.
4. Write a short subject in imperative mood that captures that change.
5. Add a body only when it adds real value: why, constraints, tradeoffs, rollout notes, or validation.

If the staged diff contains multiple unrelated changes, split the commit instead of writing a vague subject.

## Subject Line Rules

- Prefer `[scope: ]<high-level change>` when a scope helps.
- Summarize what changed, not why it changed.
- Use imperative mood: `add`, `share`, `remove`, `tighten`, `wire`.
- Stay at the behavior or architecture level, not the file-edit level.
- Avoid generic subjects like `refactor`, `fix stuff`, `updates`, or `wip`.
- Avoid rationale-heavy subjects like `do X because Y was broken`.
- Aim for about 50 characters; stay under about 72 when reasonable.
- Omit the trailing period.
- Match repo style for casing. If you use `scope:`, lower-case after the prefix is fine.

## Body Rules

Use the body for information that does not belong in the subject:

- Why the change was needed
- Important constraints or tradeoffs
- Alternatives considered or intentionally deferred follow-ups
- Follow-up work or known gaps
- Validation that is worth preserving with the commit
- Issue references when they improve traceability

Structure:

- Leave one blank line between the subject and the body.
- Wrap prose around 72 characters when practical.
- Explain context and rationale, not a line-by-line restatement of the diff.
- Put Linear references in a footer or final short section, e.g. `Refs: QUM-350`.
- Keep issue ids out of the subject by default unless the repo or reviewer
  explicitly wants them there.

Short body template:

```text
<subject>

Why:
- ...

Validation:
- ...

Refs: QUM-...
```

Skip the body if it adds nothing useful.

## Examples

Bad:

```text
Refactor Claude sessions behind shared backend adapter
```

Better:

```text
runtime: share Claude session startup across enter and child runtimes
```

Bad:

```text
Add backend abstraction so Codex support will be easier later
```

Better:

```text
backend: add shared session contract for Claude runtime setup
```

Better with body:

```text
backend: add shared session contract for Claude runtime setup

Why:
- Collapse root and child Claude startup onto one code path before same-process runtime work.

Validation:
- make validate
- bash scripts/test-tui-e2e.sh --quick
```

## Final Check

Before committing, ask:

- Does the subject say what changed in one line?
- Is the subject written as an instruction to the codebase rather than a diary entry?
- Would the why read more naturally in the body?
- Is the subject specific enough that someone scanning `git log --oneline` can understand the change?
