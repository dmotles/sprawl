---
name: e2e-testing-sandboxing
description: Use when validating Sprawl changes end to end in an isolated sandbox without touching real repo state, tmux state, or production-like `.sprawl` data.
---

The canonical workflow for this repository lives in `../../../.claude/skills/e2e-testing-sandboxing/SKILL.md`.

Read that file before doing sandbox end-to-end validation and follow it as the source of truth.

Codex-specific notes:
- This wrapper exists only so Codex discovers the repo skill from `.agents/skills`.
- Follow the canonical skill's safety rules exactly, especially around `$SPRAWL_ROOT`, `/tmp`, and cleanup.
- Prefer the sandbox helpers from the repo over ad hoc teardown commands.
