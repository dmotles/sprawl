# Memory architecture

The sprawl memory system is a 3-tier append-only model that records what each
session of work produced and surfaces a short narrative of project history into
the next session's system prompt. It replaces the earlier
LLM-driven-distillation pipeline (see `docs/forensics/memory-consolidation-perf.md`)
that proved unstable as session count grew.

Tracked under umbrella issue [QUM-513]; cutover landed in [QUM-517].

## Tiers

| Tier | Storage | Mutation | Cost |
|------|---------|----------|------|
| **Sessions** | `.sprawl/memory/sessions/<id>.md` | Append-only on handoff. Immutable after write. | Free |
| **Timeline** | `.sprawl/memory/timeline.md` | Append-only during `Consolidate`. Regeneratable from sessions. | Cheap (haiku, 1 session in → 1 line out) |
| **Project arc** | Rendered into the system prompt at session start | Regenerated from `timeline.md` each `BuildContextBlob` call | Very cheap (haiku, ~5–10 KB timeline in → 5–10 line summary out) |

### Session files

Written by the handoff path (`internal/memory/sessionlog.go`) when an agent
calls `sprawl handoff`. They contain the full structured handoff body and are
treated as immutable historical artifacts.

### Timeline format

Each row is exactly:

```
YYYY-MM-DD <session-uuid> | one-sentence summary
```

Validated by `internal/memory/regenerate.go` `TimelineRowRE` /
`ValidateTimelineRow`. Rows are sorted ascending by date. ~50–100 sessions × ~1
line ≈ 5–10 KB — small enough that no truncation/compression logic is needed
ever.

### System prompt rendering

`internal/memory/context.go` `BuildContextBlob` produces, in order:

1. `## Project Arc` — output of `SummarizeProjectArc` (a haiku call against
   `timeline.md`).
2. Footer: ``Read `.sprawl/memory/timeline.md` for the full session index. Read `.sprawl/memory/sessions/<id>.md` for the full handoff of any session.``
3. `## Pending Inbox` — single sentence: `N messages in inbox. Recommend
   archiving stale messages when possible.` Omitted when N=0.
4. `## Persistent Knowledge` — verbatim `.sprawl/memory/persistent.md`.

The arc summarizer is injected via `WithArcSummarizer`; the production wiring
lives in `internal/rootinit/deps.go` and uses a real `memory.NewCLIInvoker`.

## Append-only consolidation

`internal/memory/consolidate.go` `Consolidate` is the production code path
called from `internal/rootinit/postrun.go` after every handoff.

```
Read timeline.md (allow missing).
Build seenIDs from rows matching TimelineRowRE.
List session files on disk.
For each session not in seenIDs:
    AppendSessionWithOptions (flock-protected, idempotent, sorted insertion).
```

Properties:

- **Idempotent.** Calling `Consolidate` repeatedly with no new sessions on disk
  is a true no-op (no LLM calls, byte-identical timeline.md).
- **Crash-safe.** `AppendSessionWithOptions` uses a flock under
  `.sprawl/memory/timeline.md.lock` and writes via `tmp + rename`.
- **Bounded.** A single session in → at most one line out → cannot blow past
  any prompt-size cap.
- **Best-effort.** Per-session LLM errors are logged and skipped; the loop
  proceeds with remaining sessions.

## Operations

| Command | Purpose | Hidden? |
|---------|---------|---------|
| `sprawl memory regenerate-timeline [--out --dry-run --force]` | Rebuild `timeline.md` from `.sprawl/memory/sessions/`. Writes to `<path>.next` by default. | No |
| `sprawl memory append-session <id>` | Append a single session to the live `timeline.md`. | Yes (dev) |
| `sprawl memory show-context-blob` | Print exactly what `BuildContextBlob` would emit. | Yes (dev) |
| `sprawl memory show-arc-summary` | Print just the project-arc summary. | Yes (dev) |

### One-time regen of `.sprawl/memory/timeline.md`

This is required after the QUM-517 cutover lands, because the previous
distillation pipeline silently stalled on Apr 24. The 38 session files written
between Apr 23 and May 5 are not in the live timeline. Run from an authed env:

```bash
scripts/ops/regenerate-timeline.sh
```

The script:

1. Builds `./sprawl`.
2. Runs `sprawl memory regenerate-timeline --out .sprawl/memory/timeline.md.next`.
3. Diffs the candidate against the live file.
4. Prints the manual `mv` + `git commit` step for the operator to run after
   reviewing the diff.

The script is intentionally non-destructive: it never overwrites the live
`timeline.md` automatically. The operator inspects, then promotes.

## See also

- [QUM-513] — umbrella issue (re-architecture rationale).
- [QUM-514] — slice 1: `regenerate-timeline` command.
- [QUM-515] — slice 2: `AppendSession` production path.
- [QUM-516] — slice 3: arc summarizer + hidden CLIs.
- [QUM-517] — slice 4: cutover (this document).
- `docs/forensics/memory-consolidation-perf.md` — historical analysis of why
  the old pipeline failed.

[QUM-513]: https://linear.app/qumulo-dmotles/issue/QUM-513
[QUM-514]: https://linear.app/qumulo-dmotles/issue/QUM-514
[QUM-515]: https://linear.app/qumulo-dmotles/issue/QUM-515
[QUM-516]: https://linear.app/qumulo-dmotles/issue/QUM-516
[QUM-517]: https://linear.app/qumulo-dmotles/issue/QUM-517
