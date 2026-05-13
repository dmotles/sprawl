# Teardown Path Unbounded-Wait Audit (QUM-547)

**Status:** Reflection from QUM-542. Catalog every blocking call in the
supervisor child-shutdown path, classify each by whether it can hang on
external state, and bound any that can.

**Scope:** `*Handle.Stop` chain (unifiedHandle + WeaveRuntimeHandle), the
runtime it wraps (`internal/runtime/unified.go`), and the
`Real.Retire/Kill/Shutdown` orchestrator (`internal/supervisor/real.go`,
`internal/agentops/retire.go`).

## Background

QUM-542 root-caused a 34-minute supervisor wedge to a single unbounded
`session.Wait()` inside `unifiedHandle.Stop`: a stuck Claude Code Task
subshell held the child claude's stdout pipe FD open after SIGKILL of the
parent process, so `exec.Cmd.Wait()` blocked on `pipe.Drain()` indefinitely.
The fix bounded that one call with the goroutine + `select` +
`time.After` + `slog.Warn` pattern. QUM-545 extracted the canonical
`teardownSession` helper from both Stop implementations; QUM-546 wired the
bounded-Wait timeout signal up through the `retire.runtime-stop-done`
checkpoint.

The broader concern is: the Stop teardown chain has multiple blocking
calls. Any one of them, if it hangs on a stuck FD or wedged goroutine,
produces the same class of supervisor-visible hang. This audit catalogs
each.

## Outer bound

`Real.Retire`, `Real.Kill`, `Real.Shutdown` all wrap `runtime.Stop` in
`withRuntimeStopTimeout` (10s, `real.go:980`). This caps how long the
orchestrator waits for `runtime.Stop` to return its result — but it does
**not** interrupt the blocking calls inside `*Handle.Stop`. Those calls
ignore `ctx` (with one exception, `rt.Stop`). When ctx expires, the
orchestrator returns `ctx.Err()` and moves on, but the `*Handle.Stop`
goroutine remains blocked and the handle never actually finishes its
teardown until the underlying FD or syscall resolves.

Inner bounds give us:
1. **Snappier completion** — Stop returns true completion (not "timed out
   waiting on it"), enabling the runtime registry slot to be freed and
   subsequent retire/merge ops to proceed in worst-case scenarios.
2. **Visibility** — each abandoned call emits a `slog.Warn` citing the
   ticket that established the bound, so post-mortems pinpoint which leg
   wedged.

## Catalog

| # | Call (file:line — func) | Classification | Current bound | Action |
|---|---|---|---|---|
| 1 | `internal/runtime/unified.go:284` `sess.Interrupt(ctx)` (inside `UnifiedRuntime.Stop`) | External (backend MCP), ctx-aware | outer `stopCtx` 10s | Leave |
| 2 | `internal/runtime/unified.go:297–300` wait on `loopWG` via `loopDone`/`ctx.Done` | In-memory turn loop, ctx-aware | outer `stopCtx` 10s | Leave |
| 3 | `internal/supervisor/runtime_launcher.go:371` `h.stopActivity()` (`runActivitySubscriber` stopper: `unsub(); <-doneCh`) | In-memory goroutine join. Drains after `close(ch)`; worst case the goroutine is blocked inside `obs.OnMessage` writing to `activityFile`. Local fs write — always-fast on local disk, can hang on NFS / wedged FD | **Unbounded** | **BOUND** (Fix B) |
| 4 | `internal/supervisor/teardown_session.go:65` `session.Close()` | External; closes stdin pipe FD (`close(2)`). Kernel-bounded | unbounded but kernel-bounded | Leave |
| 5 | `internal/supervisor/teardown_session.go:66` `session.Kill()` | External; `syscall.Kill(SIGKILL)`. Kernel-bounded | unbounded but kernel-bounded | Leave |
| 6 | `internal/supervisor/teardown_session.go:72` `session.Wait()` (in goroutine, select) | External; child reap + stdout pipe drain. Can hang on stuck FD | **5s** for unifiedHandle (QUM-542); **skipped** (waitTimeout=0) for WeaveRuntimeHandle | Already bounded |
| 7 | `internal/supervisor/runtime_launcher.go:383` `h.activityFile.Close()` | External; `close(2)` on regular file. No fsync issued by close; kernel-bounded on local disk. NFS-mounted `.sprawl/` could hang | **Unbounded** | **BOUND** (Fix C) |
| 8 | `internal/supervisor/weave_handle.go:131` `h.stopActivity()` | Same as #3 | Unbounded | **BOUND** (Fix B) |
| 9 | `internal/supervisor/weave_handle.go:141` `h.activityFile.Close()` | Same as #7 | Unbounded | **BOUND** (Fix C) |
| 10 | `internal/agentops/retire.go` post-runtime.Stop fs/git ops (`WorktreeRemove`, `RemoveAll`, `ArchiveMessage`) | External (git, fs). Out of scope for *Handle.Stop chain | Not bounded inside `*Handle.Stop` | Out of scope; follow-up if a wedge ever surfaces |

## Decisions

- **Fix A — shared `joinWithTimeout` helper** in `teardown_session.go`.
  Centralizes the QUM-542 goroutine + select + time.After + slog.Warn
  pattern for non-session join points. Returns `(completed bool)`.
  Goroutine is intentionally leaked on timeout (matches QUM-542
  precedent — the OS or kernel resolves the underlying syscall).
- **Fix B — bound `stopActivity()`** in both `*Handle.Stop`
  implementations with `stopActivityTimeout = 2s`. The subscriber
  goroutine is a self-contained `for ev := range ch` loop; the only way
  it wedges past the unsub `close(ch)` is if it's parked inside
  `obs.OnMessage` writing to `activityFile`. 2s is generous for a local
  fs write.
- **Fix C — bound `activityFile.Close()`** in both `*Handle.Stop`
  implementations with `activityCloseTimeout = 2s`. `close(2)` on a
  regular file does not fsync; the only realistic stall is NFS/CIFS
  metadata ops or a kernel-side device error. 2s is generous.
- **Why both Fix B and Fix C:** they wedge on the same underlying FD,
  but in different syscalls. The subscriber's `OnMessage` is a `write(2)`
  in flight; `Close` is `close(2)` after the write returns. A wedged FD
  could trip either one.

## Why bound things the outer 10s timeout already protects?

The outer `withRuntimeStopTimeout` is a hard 10s cap on
`runtime.Stop`/`*Handle.Stop` returning at all. It is the last-line
backstop. But:

- The blocking calls run *inside* `*Handle.Stop` past `rt.Stop`. They
  don't observe ctx. When ctx fires, the orchestrator returns
  `ctx.Err()` but the wedged goroutine stays alive, holding handle
  references and (in the multi-process case) FDs.
- Inner bounds collapse three independent 10s waits to ~9s
  (`unifiedHandleStopWaitTimeout 5s + stopActivityTimeout 2s +
  activityCloseTimeout 2s`) in the worst case where every leg wedges,
  versus a single 10s outer cap that may not be enough if the legs
  run sequentially.
- `slog.Warn` per leg gives a per-call attribution post-mortem.

## Out of scope (follow-ups, if warranted)

- `internal/agentops/retire.go` post-runtime.Stop fs/git ops — those run
  *after* the handle teardown returns and are part of the retire
  orchestration, not the runtime shutdown contract. If they ever wedge,
  file a separate audit.
- `internal/runtime/unified.go`'s `rt.Stop` already observes ctx and is
  bounded by the outer 10s. The turn loop wait (`<-loopDone`) ties off
  on ctx as well.

## Tests added

See `internal/supervisor/teardown_session_test.go`,
`internal/supervisor/runtime_launcher_test.go`, and
`internal/supervisor/weave_handle_test.go` (new). For each new bound:
a wedge-the-leg fake that would otherwise hang past the bound; assert
`*Handle.Stop` still completes within `bound × N + slack`.
