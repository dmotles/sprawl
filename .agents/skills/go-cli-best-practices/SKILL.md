---
name: go-cli-best-practices
description: Use before writing or modifying Go CLI code in this repository, especially Cobra commands, dependency injection, error handling, command structure, and test placement.
---

The canonical workflow for this repository lives in `../../../.claude/skills/go-cli-best-practices/SKILL.md`.

Read that file before making Go CLI changes and follow it as the source of truth.

Codex-specific notes:
- This wrapper exists only so Codex discovers the repo skill from `.agents/skills`.
- Treat the referenced file as the full repo-specific guidance for command layout, dependency injection, and Cobra patterns.
- If the task also changes tests or command output, load `testing-practices` and `cli-ux-best-practices`.
