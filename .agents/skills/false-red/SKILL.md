---
name: false-red
description: Read when a build, validate run, test, or merge just failed and you are about to blame your diff. Matches the failure against known environment-caused failures on this host — disk exhaustion, lint lock contention, stripped auth, a test-harness cleanup race, and a merge that un-commits work.
---

The canonical catalogue for this repository lives in `../../../.claude/skills/false-red/SKILL.md`.

Read that file before reverting a change, retrying with a bypass, or reporting a regression, and follow it as the source of truth.

Codex-specific notes:
- This wrapper exists only so Codex discovers the repo skill from `.agents/skills`.
- Match on the literal error text you saw. If no entry matches, the failure is your diff.
- Two rules hold regardless of which entry matches: never suppress a failure to reach green (`|| true`, `--no-verify`, a `-` prefix on a recipe, hiding a binary from `PATH`), and treat a passing retry as evidence rather than proof — say which entry you matched.
- The merge-recovery entry is the one to read *before* acting: the natural response to its symptom destroys the work it is describing.
