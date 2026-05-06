# QUM-488 — `mcp__sprawl__delegate` (and `sprawl delegate`) silently drop tasks under unified runtime

**Status:** Root-caused.
**Date:** 2026-05-06
**Author:** ghost (research agent)
**Branch:** `dmotles/qum-488-mcp-delegate-silent-drop-research`

## Verdict

The unified runtime (`SPRAWL_UNIFIED_RUNTIME=1`, default in current sessions)
**never consumes the file-backed task queue** that `Real.Delegate` and the
`sprawl delegate` CLI both write to. Tasks land on disk and are silently
ignored. Wake signals reach the runtime but its turn loop only drains the
in-memory `MessageQueue`; it has no equivalent of the legacy runner's
`r.deps.NextTask(...)` poll.

This affects **both MCP and CLI delegate paths** equally, exactly matching
the observed symptom (tower's MCP delegate at 16:31:19Z and CLI delegate at
16:59:03Z both failed; `send_async` worked).

## Evidence

### 1. Both delegate attempts wrote task files, neither was consumed

```
$ ls -la .sprawl/agents/finn/tasks/
-rw-r--r-- 1 coder coder 2620 May  6 16:31 20260506T163119.377374872Z-b7431a91-...json   # MCP delegate
-rw-r--r-- 1 coder coder 2292 May  6 16:59 20260506T165903.682972024Z-41392db2-...json   # CLI delegate
```

Both files have `"status": "queued"` and a populated `prompt_file` path. The
write path works perfectly.

### 2. Only the legacy runner reads the task queue

Grep for `state.NextTask` / `state.EnqueueTask` consumers across `internal/`:

| File | Role |
|------|------|
| `internal/state/tasks.go:82` | Defines `NextTask` |
| `internal/agentloop/runner.go:42-43`, `:204`, `:764` | **Legacy runner consumes via `r.deps.NextTask(...)`** |
| `internal/supervisor/runtime_launcher.go:204` | Wires `state.NextTask` into legacy `inProcessRuntimeStarter`'s `RunnerDeps` |
| `internal/supervisor/real.go:296` | `Delegate` writes via `state.EnqueueTask` |
| `internal/runtime/*` (UnifiedRuntime / TurnLoop) | **No reference at all.** Confirmed via `grep -r 'NextTask\|TasksDir\|state\.Task' internal/runtime` → 0 matches |

`internal/runtime/turnloop.go`'s `Run` loop (lines 71-113) only does:

```go
items := l.cfg.Queue.DrainAll()      // in-memory MessageQueue
…
case <-l.cfg.Queue.Signal():         // wake → re-check MessageQueue
```

There is no disk poll for tasks.

### 3. Wake() under the unified runtime is a queue-signal poke, not a task-pickup trigger

`internal/supervisor/runtime_launcher_unified.go:205-208`:

```go
func (h *unifiedHandle) Wake() error {
    h.rt.Queue().Wake()           // signals MessageQueue.Signal()
    return nil
}
```

The signal merely unblocks the `select` in `TurnLoop.Run`. When the loop
re-checks, it calls `Queue.DrainAll()` — and the in-memory `MessageQueue` is
still empty, because `Real.Delegate` never enqueued anything to it. The loop
goes back to sleep. The task file remains unread.

### 4. Why send_async works (the contrast)

`Real.SendAsync` (`real.go:634-675`) writes to **two** places:
1. Maildir, via `messages.Send`.
2. The agentloop on-disk queue (`agentloop.Enqueue`) — files under
   `.sprawl/agents/<name>/queue/{pending,delivered}/`.

It then calls `runtime.InterruptDelivery()`, which under the unified runtime
goes through `unifiedHandle.InterruptDelivery`
(`runtime_launcher_unified.go:210-238`):

```go
func (h *unifiedHandle) InterruptDelivery() error {
    pending, err := agentloop.ListPending(h.sprawlRoot, h.name)
    if err == nil && len(pending) > 0 {
        interrupts, asyncs := inboxprompt.SplitByClass(pending)
        // → builds inbox prompt, h.rt.Queue().Enqueue(QueueItem{...})
    }
    return h.rt.InterruptDelivery(context.Background())
}
```

This handle **acts as a bridge** between the on-disk queue and the in-memory
runtime queue. A bridge of the same shape was simply never written for
tasks.

### 5. `runtime.RecordQueuedTask` is metrics-only

`internal/supervisor/runtime.go:369-374`:

```go
func (r *AgentRuntime) RecordQueuedTask(_ *state.Task) {
    r.mu.Lock()
    r.snapshot.QueueDepth++
    r.mu.Unlock()
    r.emit(RuntimeEventTaskQueued)
}
```

It increments a counter and emits an event. It does **not** enqueue into
the runtime's MessageQueue. The argument is a `_` — the task content is
discarded.

## Root cause (one paragraph)

The QUM-398/399/400 unified-runtime port bridged the on-disk message
inbox into the runtime's in-memory queue (via
`unifiedHandle.InterruptDelivery`) but did not bridge the on-disk task queue
(`.sprawl/agents/<name>/tasks/*.json`). The legacy runner consumed tasks
via a periodic `state.NextTask` poll inside its main loop; the unified
`TurnLoop` has no such poll. Consequently, every successful
`Real.Delegate` (MCP **and** CLI) under `SPRAWL_UNIFIED_RUNTIME=1` writes a
queued task to disk that nothing ever picks up. `runtime.Wake()` does fire
and does signal the loop — but the loop wakes, drains an empty in-memory
queue, and goes back to sleep.

## Why the original "MCP-only / caller-identity" hypothesis was wrong

The session prompt suspected a parent/identity check or env-Getenv resolution
bug analogous to QUM-487. `Real.Delegate` (`real.go:281-307`) performs no
caller check, no env lookup, and no identity-gated branching. The CLI path
shares the same supervisor entry point. So the bug cannot be identity-gated
— and indeed, both paths fail the same way for the same reason.

## Proposed fix

### Shape (recommended): bridge tasks the same way messages are bridged

Two coupled changes in `internal/supervisor/`:

1. **In `unifiedHandle.Wake()`** (or a new `feedTasks` helper called from
   `Wake()` and from runtime startup): read pending tasks from
   `state.ListTasks(...)` filtered to `Status=="queued"`, and for each, mark
   it `in-progress` (`state.UpdateTask`) and enqueue a `runtimepkg.QueueItem`
   into `h.rt.Queue()` with the task prompt (`"You have a new task. Read
   it from @<promptFile> and begin working."` — same shape as
   `runner.go:773-777`).
2. **At unified runtime start** (`runtime_launcher_unified.go:Start`): call
   the same task-feed helper after `rt.Start(...)` so any tasks queued while
   the runtime was stopped are picked up on launch (matches the legacy
   runner's poll-on-each-iteration behavior).
3. **Mark task done** when the runtime reports `EventTurnCompleted` — wire
   a callback similar to `OnQueueItemDelivered` (see `runtime_launcher_unified.go:99-105`,
   already used to mark agentloop entries delivered) that updates
   `task.Status="done"` via `state.UpdateTask`.

`Real.Delegate` itself stays unchanged — it continues to write via
`state.EnqueueTask` (preserving CLI cross-process delivery) and call
`runtime.Wake()`. The bridge in `unifiedHandle.Wake` then sweeps the
on-disk queue and feeds it into the in-memory queue.

### Estimated footprint

* Production code: ~30-50 LOC in `runtime_launcher_unified.go` plus a
  small helper.
* Tests: ~80-150 LOC across `runtime_launcher_unified_test.go`
  (in-process delegate happy path, in-process delegate while turn busy,
  CLI-side delegate-then-wake) and a new e2e gate similar to
  `test-handoff-e2e` if not subsumed.

### Alternatives considered

* **Move task consumption into `TurnLoop.Run`**: invasive — turnloop is
  meant to be backend-agnostic and shouldn't know about
  `internal/state/tasks.go`. The handle layer is the right seam.
* **Have `Real.Delegate` enqueue directly to `runtime.Queue()` and skip
  `state.EnqueueTask` when in-process**: fragile — the CLI path is a
  separate process and must continue to use the disk queue, so we'd need
  the bridge anyway. Better to have one path: always write to disk, always
  drain on wake.
* **Add a wake-file fallback** (the original Suspicions §2 idea): doesn't
  help, because the unified runtime does not poll the wake file either.
  The wake file is read inside `agentloop.SendPromptWithInterrupt`'s
  goroutine in the legacy runner — there is no equivalent in the unified
  runtime's turn loop.

## Recommendation

**Don't fix in this same session.** The fix requires:
* Touching `internal/supervisor/runtime_launcher_unified.go` and adding
  a task-delivery callback wired through `runtimepkg.RuntimeConfig`.
* Coordinating with finn (currently on QUM-400 step 3 in the same area).
* New e2e coverage (otherwise the same regression class will recur the
  next time someone refactors the runtime).

File a follow-up engineer task with a pointer to this doc; leave QUM-488
**Backlog → "needs engineer"** with the proposed fix shape recorded.

## Open questions / next investigations

* When did this regress? Likely never worked under unified runtime — the
  port (QUM-398) appears to have stubbed task handling and no one
  noticed because the existing TUI delegate flow exercised the legacy
  runner. Worth a `git log -S "NextTask"` sweep to confirm whether unified
  runtime ever consumed tasks.
* Does any e2e test currently exercise `Real.Delegate` under
  `SPRAWL_UNIFIED_RUNTIME=1`? Suspect not — file a test-coverage gap.
* `runtime.RecordQueuedTask`'s `_ *state.Task` parameter is dead. The fix
  may want to repurpose this method (it already is on the public
  `AgentRuntime` and emits a metrics event) so the supervisor's
  `Real.Delegate` flow is the one that records + enqueues, instead of
  doing it inside the handle. That's a small design call to make during
  implementation.

## References

* `internal/supervisor/real.go:281-307` — `Real.Delegate`
* `internal/supervisor/runtime_launcher_unified.go:205-238` — `unifiedHandle.Wake` / `InterruptDelivery`
* `internal/runtime/turnloop.go:71-113` — `TurnLoop.Run`
* `internal/agentloop/runner.go:764-802` — legacy task consumption (the path that works)
* `internal/state/tasks.go` — task queue persistence
* Companion bug: QUM-487 (`merge` parent-mismatch) — independent root cause; do NOT bundle fixes.
