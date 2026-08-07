---
name: git-recovery
description: Use for anything git, commit, or merge shaped in this repo — recovery when git state has gone wrong, and the standing rules that stop it going wrong. Recovery: a commit landed on the wrong branch, a rebase onto `main` conflicts on commits that already landed, a squash-merge stranded a downstream branch, a merge attempt looks like it un-committed an agent's work. Standing rules: stage explicit paths only (never `git add -A`), never overwrite the ref that tells you where you were. Mechanism: the `main` commit guard and reference-transaction backstop, the forward-only parent contract of `sprawl merge`, and the pre-merge recovery refs under `refs/sprawl/premerge/`. Read it BEFORE `reset`, `rebase`, `checkout -f`, `clean`, `branch -f`, `add -A`, `commit --amend`, or a merge.
---

The canonical procedures for this repository live in `../../../.claude/skills/git-recovery/SKILL.md`.

Read that file before running any git recovery command and follow it as the source of truth.

Codex-specific notes:
- This wrapper exists only so Codex discovers the repo skill from `.agents/skills`.
- Topics covered there:
  - the pin-then-move-the-ref rescue, and how to tell **which merge engine your
    binary is** before you need to know
  - wrong-branch commit recovery on `main` — and why it is `--mixed`, not
    `--soft`, in the main checkout
  - squash-merge downstream recovery (QUM-1083), including the step-4
    argument-order check
  - the merge engine's forward-only parent contract (QUM-1087) and the pre-merge
    recovery refs (QUM-1090) — both `/agent` and `/parent` matter
  - `refs/sprawl/` namespace rules: `premerge/` is tool-owned; hand-written pins
    go under `rescue/`
  - "never overwrite the thing that tells you where you were" — four surfaces,
    and why a ref is named for an attempt rather than a branch
  - the standing staging rule: explicit paths only, never `git add -A`
    (`git add -u` is the sanctioned shortcut)
  - the `main` commit guard (QUM-808) and the reference-transaction backstop
    (QUM-837)
- The rules that apply to all of them: `--hard` is never right; between `--soft`
  and `--mixed` the discriminator is the index — `--soft` to preserve staged
  content, `--mixed` when rewinding away from a commit whose tree must not stay
  staged; never `git reset --hard` on `main`; pin before you move; stage explicit
  paths only.
