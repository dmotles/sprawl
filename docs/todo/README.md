# docs/todo/

Stopgap issue tracking while Linear is over quota and GitHub Issues migration is pending.

## Convention

One markdown file per item. Filename: `short-kebab-descriptor.md` (e.g. `tui-session-boundary.md`, `async-finalize-handoff.md`).

Structure per file:

```markdown
# <Short Title>

**Status:** open | in-progress | done
**Priority:** high | medium | low
**Filed:** YYYY-MM-DD

## Problem
(what's broken or missing, how to reproduce)

## Fix
(proposed approach, alternatives considered)

## Acceptance Criteria
- [ ] bullet
- [ ] bullet
```

When a file's work ships, move it to `docs/todo/done/` OR delete it (git history preserves it either way).

## Migration

These files will be batch-imported as GitHub Issues when we cut over from Linear. Keep each file self-contained so it converts cleanly.

## Not for

- Large design documents — those go in `docs/designs/`.
- Agent-specific findings — those go in `.sprawl/agents/<name>/findings/` during a run.
