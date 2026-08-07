---
name: tui-testing
description: Use when validating changes to `sprawl enter` or the TUI. Covers the automated TUI harness, manual tmux-based validation, the repo's mandatory TUI test checklist, and the TUI's operator surfaces — selecting and copying text out of a mouse-capturing TUI, the scroll and prompt-history key map, the Ctrl+\ incident-snapshot bundle, and the SIGUSR2 / --pprof runtime profiling toggle.
---

The canonical workflow for this repository lives in `../../../.claude/skills/tui-testing/SKILL.md`.

Read that file before validating TUI changes and follow it as the source of truth.

Codex-specific notes:
- This wrapper exists only so Codex discovers the repo skill from `.agents/skills`.
- Use the referenced skill for the automated harness, manual tmux workflow, required validation checklist, text selection and scroll/input-history keys, the incident-snapshot hotkey, and the runtime pprof toggle.
- If the change touches the inbox notifier or handoff paths, follow the extra mandatory test requirements documented in the repo instructions as well.
