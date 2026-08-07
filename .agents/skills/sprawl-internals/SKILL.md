---
name: sprawl-internals
description: Codebase orientation and runtime contracts for the sprawl repo — the agent lifecycle / Status / IsTerminal / wake contract, repo layout and agent types, the dependency-injection pattern, the build and test target reference, `.sprawl/config.yaml` semantics, and the `make install` policy.
---

The canonical reference for this repository lives in `../../../.claude/skills/sprawl-internals/SKILL.md`.

Read that file and follow it as the source of truth.

Codex-specific notes:
- This wrapper exists only so Codex discovers the repo skill from `.agents/skills`.
- Use the referenced skill for the QUM-786 lifecycle contract before touching any
  MCP verb that targets an agent by name, and for the `make` target reference.
- Two rules that apply regardless: only `weave` runs `make install`, and every
  change must pass `make validate` before it is committed.
