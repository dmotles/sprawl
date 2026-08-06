---
name: git-recovery
description: Use when git state has gone wrong and you are about to run a recovery command — a commit landed on the wrong branch, a rebase onto `main` conflicts on commits that already landed, a squash-merge stranded a downstream branch, or a merge attempt looks like it un-committed an agent's work. Read it BEFORE `reset`, `rebase`, `checkout -f`, `clean`, or `branch -f`.
---

The canonical procedures for this repository live in `../../../.claude/skills/git-recovery/SKILL.md`.

Read that file before running any git recovery command and follow it as the source of truth.

Codex-specific notes:
- This wrapper exists only so Codex discovers the repo skill from `.agents/skills`.
- Use the referenced skill for the rescue-ref procedure, the wrong-branch commit
  recovery, and the squash-merge downstream recovery.
- The rules that apply to all of them: pin before you move, `--soft` never
  `--hard`, and never `git reset --hard` on `main`.
