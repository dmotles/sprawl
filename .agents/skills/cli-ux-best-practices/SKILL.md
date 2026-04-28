---
name: cli-ux-best-practices
description: Use before adding or modifying any Sprawl CLI command behavior, output, or errors. Focus on agent-facing UX, next-action hints, stdout vs stderr discipline, and recoverable error messaging.
---

The canonical workflow for this repository lives in `../../../.claude/skills/cli-ux-best-practices/SKILL.md`.

Read that file before changing CLI behavior and follow it as the source of truth.

Codex-specific notes:
- This wrapper exists only so Codex discovers the repo skill from `.agents/skills`.
- If you change command semantics, flags, or user-visible output, load the canonical skill first.
- If the change also touches Go command implementation or tests, load `go-cli-best-practices` and `testing-practices` too.
