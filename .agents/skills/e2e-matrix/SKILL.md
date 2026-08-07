---
name: e2e-matrix
description: Use BEFORE running e2e tests or calling a change validated, to answer "which e2e rows do I have to run" — the mandatory test gate. Derives the owed matrix rows from a diff via the touched-file table, and covers make test-e2e-matrix invocation, multi-row runs, skip accounting, and exit codes. Read it whenever your diff touches a gated file.
---

The canonical mandatory-test e2e matrix for this repository lives in `../../../.claude/skills/e2e-matrix/SKILL.md`.

Read that file before deciding which rows your diff owes, and follow it as the source of truth.

Codex-specific notes:
- This wrapper exists only so Codex discovers the repo skill from `.agents/skills`.
- Use the referenced skill for the touched-file table, the multi-row invocation
  contract, the skip accounting, and the driver exit codes.
- The rules that apply to all of them: derive the row set from the table as a
  union over every path in your diff, never from a list someone handed you; and
  a skipped row asserts nothing, so it never discharges the obligation.
