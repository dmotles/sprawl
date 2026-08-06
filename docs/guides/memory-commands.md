# Sprawl developer commands

This file documents internal/maintenance commands intended for Sprawl
contributors and operators (not end-user agents). End-user CLI documentation
lives in the package help text (`sprawl <cmd> --help`).

## `sprawl memory regenerate-timeline`

Re-build the canonical session timeline (`.sprawl/memory/timeline.md`) from
scratch by re-summarizing every session under `.sprawl/memory/sessions/`.

### Synopsis

```
sprawl memory regenerate-timeline [--out PATH] [--dry-run] [--force]
                                  [--model NAME] [--timeout DURATION]
```

### Default output path

`<sprawl-root>/.sprawl/memory/timeline.md.next`

The command is **non-destructive by default**: it never overwrites the live
`timeline.md`. Output goes to a sibling `.next` file so the result can be
diffed and reviewed before any cutover. To replace `timeline.md` you must
explicitly `mv` (or `cp`) the `.next` file over it after manual inspection.

### Flags

- `--out PATH` — write to a custom path instead of the default `.next` file.
- `--dry-run` — print the rendered timeline to stdout; do not touch the
  filesystem.
- `--force` — overwrite the output path if it already exists. Without this
  flag the command refuses to clobber an existing file and returns an error.
- `--model NAME` — override the Claude model used for per-session
  summarization. Default is `haiku` (fast and sufficient for one-line
  summaries).
- `--timeout DURATION` — bound each individual `claude -p` call. Defaults to
  `memory.DefaultInvokeTimeout` (120s).

### Non-destructive guarantee

1. The live `timeline.md` is never written to by this command.
2. With `--dry-run`, no file is created.
3. Without `--force`, an existing `--out` (or default `.next`) path causes an
   immediate error before any LLM calls.
4. Writes are atomic (`os.CreateTemp` + `os.Rename`); on error the temp file
   is removed.
5. If the LLM produces a malformed row twice in a row for any session, the
   pipeline emits a deterministic placeholder row for that session rather
   than aborting or dropping it. The session itself is never modified.

### When to use

- After changing the timeline schema or summary style and you want every
  historical session re-rendered consistently.
- After a `Consolidate` bug or LLM regression has left the live timeline in
  a degraded state.
- As part of a manual session-memory audit: regenerate, diff against the
  live timeline, and cherry-pick changes.

## `sprawl memory append-session` (hidden dev tool)

Append (or merge) a single session summary into `.sprawl/memory/timeline.md`.
This is the incremental counterpart to `regenerate-timeline`: rather than
re-summarizing every session, it summarizes one, inserts its row in the
correct date-sorted position, and rewrites the file atomically.

The command is registered as `Hidden: true` because it's intended for
internal use by the session-end hook and dev tooling, not direct human use.
It is reachable via `sprawl memory append-session <session-id>` regardless.

### Synopsis

```
sprawl memory append-session <session-id> [flags]
```

### Flags

- `--dry-run` — print the candidate row to stdout; do not modify
  `timeline.md`.
- `--model NAME` — override the Claude model used for the one-line summary.
  Default is `haiku`.
- `--timeout DURATION` — bound the `claude -p` call. Defaults to
  `memory.DefaultInvokeTimeout` (120s).
- `--lock-timeout DURATION` — maximum time to wait for the timeline flock
  before giving up with `ErrTimelineLockContended`. Defaults to
  `memory.DefaultAppendLockTimeout` (5s).

### Idempotency

If `timeline.md` already contains a row referencing `<session-id>`, the
command no-ops without making any LLM call and exits successfully. This
makes it safe to re-run on the same session or to invoke from a hook that
may fire more than once.

### Concurrency

A `gofrs/flock` advisory lock on `<root>/.sprawl/memory/timeline.md.lock`
serializes concurrent writers. If another process holds the lock for longer
than `--lock-timeout`, the command exits with a wrapped
`ErrTimelineLockContended` error so callers can retry or surface the
conflict.

## `sprawl memory show-context-blob`

Print the rendered system-prompt context blob to stdout. This is the same
blob `sprawl enter` injects into a fresh Claude session, so it is the
fastest way to inspect what an agent will actually see at boot.

The command is **hidden** (it does not appear in `sprawl memory --help`)
because it is an inspection seam intended for Sprawl contributors, not an
end-user workflow.

### Synopsis

```
sprawl memory show-context-blob
```

### Behavior

- Reads `SPRAWL_ROOT` from the environment; errors out if unset.
- Calls `memory.BuildContextBlob` with `rootName = filepath.Base(root)`.
- Writes the assembled blob verbatim to stdout. No trailing newline is
  added beyond what the builder produces.

### When to use

- Diffing the rendered context blob across changes to
  `internal/memory/context.go`.
- Confirming that a new session, agent, or persistent-knowledge entry
  surfaces in the blob as expected.
- Sanity-checking the budget enforcement path against a real project.

## `sprawl memory show-arc-summary`

Print the LLM-generated project arc summary to stdout. This is the
milestone-level narrative produced by `memory.SummarizeProjectArc` from
the regenerated timeline.

The command is **hidden** (inspection-only, not part of the documented
end-user surface).

### Synopsis

```
sprawl memory show-arc-summary [--timeline PATH] [--model NAME]
                               [--timeout DURATION]
```

### Flags

- `--timeline PATH` — override the timeline file
  (default: `<sprawl-root>/.sprawl/memory/timeline.md`).
- `--model NAME` — Claude model override (default: `haiku`).
- `--timeout DURATION` — LLM invocation timeout
  (default: `memory.DefaultInvokeTimeout`, currently 120s).

### Behavior

- Reads `SPRAWL_ROOT` from the environment; errors out if unset.
- Reads the timeline (default path or `--timeline`), sends a deterministic
  prompt asking for a 5–10 line milestone summary, and writes the model's
  response to stdout.
- On budget overflow (>10 lines or >1200 chars) the summarizer retries
  once with a `RETRY` marker. If the second attempt also overflows, it
  emits a deterministic fallback prefixed with `summarization failed`
  containing a truncated copy of the timeline.
- A non-validation invoker error (timeout, missing binary, etc.) is
  returned to the caller; the command exits non-zero.

### When to use

- Iterating on the arc-summarizer prompt or budget rules.
- Comparing arc summaries across model choices via `--model`.
- Diagnosing why a particular timeline yields the fallback path.
