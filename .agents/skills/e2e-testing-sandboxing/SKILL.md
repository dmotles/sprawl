---
name: e2e-testing-sandboxing
description: Use when validating Sprawl changes end to end in an isolated sandbox without touching real repo state, tmux state, or production-like `.sprawl` data. Also read it before running any tmux command in sandbox or harness context — sandbox tmux lives on a dedicated socket and bare `tmux` reaches the wrong server. Also carries the hard `/tmp` hygiene rules and the `claude` auth shim (`.env`, `scripts/run-claude`, `$SPRAWL_CLAUDE`) that fixes `Not logged in` from a Bash subshell.
---

The canonical workflow for this repository lives in `../../../.claude/skills/e2e-testing-sandboxing/SKILL.md`.

Read that file before doing sandbox end-to-end validation and follow it as the source of truth.

Codex-specific notes:
- This wrapper exists only so Codex discovers the repo skill from `.agents/skills`.
- Follow the canonical skill's safety rules exactly, especially around `$SPRAWL_ROOT`, `/tmp` hygiene (never a broad `rm -rf` glob, never touch `/tmp/coder-script-data`), and cleanup.
- If `claude` fails with `Not logged in` from a Bash subshell, the canonical skill's `.env` / `scripts/run-claude` / `$SPRAWL_CLAUDE` shim is the fix.
- Prefer the sandbox helpers from the repo over ad hoc teardown commands.
