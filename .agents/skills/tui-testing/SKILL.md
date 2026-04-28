---
name: tui-testing
description: Use when validating changes to `sprawl enter` or the TUI. Covers the automated TUI harness, manual tmux-based validation, and the repo's mandatory TUI test checklist.
---

The canonical workflow for this repository lives in `../../../.claude/skills/tui-testing/SKILL.md`.

Read that file before validating TUI changes and follow it as the source of truth.

Codex-specific notes:
- This wrapper exists only so Codex discovers the repo skill from `.agents/skills`.
- Use the referenced skill for the automated harness, manual tmux workflow, and required validation checklist.
- If the change touches the inbox notifier or handoff paths, follow the extra mandatory test requirements documented in the repo instructions as well.
