---
name: testing-practices
description: Use before writing or changing tests in this repository. Covers the repo's Go test commands, dependency-injection test pattern, mock conventions, and manual validation workflow.
---

The canonical workflow for this repository lives in `../../../.claude/skills/testing-practices/SKILL.md`.

Read that file before adding or modifying tests and follow it as the source of truth.

Codex-specific notes:
- This wrapper exists only so Codex discovers the repo skill from `.agents/skills`.
- Use the referenced skill for package-specific test commands, deps-struct patterns, and mock conventions.
- If the task touches TUI flows or sandboxed end-to-end behavior, also load `tui-testing` or `e2e-testing-sandboxing` as appropriate.
