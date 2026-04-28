---
name: linear-issues
description: Use when creating, updating, querying, or planning Linear issues for this repository. Reads workspace team and project settings from CLAUDE.local.md and uses the Linear MCP tools.
---

The canonical workflow for this repository lives in `../../../.claude/skills/linear-issues/SKILL.md`.

Read that file before doing Linear issue work and follow it as the source of truth.

Codex-specific notes:
- Read `../../../CLAUDE.local.md` for the current team key, project ID, branch prefix, and workspace conventions.
- Use the `mcp__linear__*` tools directly when available.
- If Linear MCP is unavailable in the current session, stop and say so instead of guessing at issue metadata.
