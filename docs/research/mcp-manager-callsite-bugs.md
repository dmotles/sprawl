# MCP manager-callsite bugs: `delegate` and `merge` from a child agent

**Date:** 2026-05-06
**Researcher:** ghost
**Branch:** `dmotles/mcp-manager-callsite-bugs-research`
**Related session evidence:** tower (manager) → finn (child) interactions on 2026-05-06.

## Symptom summary

Two manager-flow MCP tools misbehave when invoked from a non-root agent (i.e.
a child agent acting as a manager for its own children, e.g. tower managing
finn):

| Tool                        | Symptom                                                                                  | CLI-equivalent path |
|-----------------------------|------------------------------------------------------------------------------------------|---------------------|
| `mcp__sprawl__merge`        | Returns `cannot merge "finn": you are not its parent (parent is "tower")` to **tower** — though tower IS finn's parent. | `sprawl merge finn` works on first try. |
| `mcp__sprawl__delegate`     | Returns success; task appears not to be delivered.                                       | `sprawl delegate finn "<task>"` works on first try. |

Both bugs share a common architectural shape: the MCP handler does not flow
the calling agent's identity through to the supervisor / agentops layer, so
the operation runs as if the supervisor process's owner (always root —
typically `weave`) were the caller.

## Bug B — `mcp__sprawl__merge` parent-mismatch (DEFINITIVE root cause)

### Code path (MCP)

1. tower's claude session forwards the MCP message; the host bridge attaches
   tower's identity onto the request context via
   `backend.WithCallerIdentity(ctx, "tower")` —
   [`internal/backend/session.go:298-302`](../../internal/backend/session.go).
2. `Server.toolMerge` unmarshals args and calls `s.sup.Merge(ctx, ...)` —
   [`internal/sprawlmcp/server.go:278-291`](../../internal/sprawlmcp/server.go). It does not read `ctx`.
3. `Real.Merge` discards `ctx` (`func (r *Real) Merge(_ context.Context, …)`)
   and forwards directly to `agentops.Merge` with a long-lived `r.mergeDeps`
   struct —
   [`internal/supervisor/real.go:344-346`](../../internal/supervisor/real.go).
4. `agentops.Merge` reads the caller from process env:
   ```go
   callerName := deps.Getenv("SPRAWL_AGENT_IDENTITY")
   …
   if agentState.Parent != callerName {
       return fmt.Errorf("cannot merge %q: you are not its parent (parent is %q)", agentName, agentState.Parent)
   }
   ```
   — [`internal/agentops/merge.go:44-63`](../../internal/agentops/merge.go).

`r.mergeDeps.Getenv` is initialized once at supervisor construction
(`os.Getenv` reads the supervisor process's environment). The supervisor
process is `sprawl enter` running under weave, so
`SPRAWL_AGENT_IDENTITY=weave`. The check then becomes `agentState.Parent !=
"weave"` for every MCP-side merge attempt, producing the reported
`parent is "tower"` error — finn's parent IS tower, but tower's MCP request
is evaluated as though it came from weave.

### Why the CLI works

`cmd/merge.go` is invoked as a subprocess from tower's worktree.
`runMerge` constructs deps with `os.Getenv` against the subprocess
environment. tower's worktree environment carries
`SPRAWL_AGENT_IDENTITY=tower` (set by the runtime launcher when tower's claude
process is spawned). So `callerName == "tower" == agentState.Parent`, and the
parent check passes.

### Reference test

[`cmd/merge_test.go:172`](../../cmd/merge_test.go) explicitly asserts the
`"you are not its parent"` error string — confirming the contract is
"identity comes from `SPRAWL_AGENT_IDENTITY`". The MCP-layer divergence is
that the supervisor's env is wrong for the caller.

### Proposed fix shape

There's already a precedent in `toolReportStatus`
([`server.go:266`](../../internal/sprawlmcp/server.go)):

```go
agentName := backendpkg.CallerIdentity(ctx)
result, err := s.sup.ReportStatus(ctx, agentName, …)
```

That pattern (introduced in QUM-387) was applied to `report_status` but never
propagated to `merge`, `retire`, `delegate`, or `message`. Apply the same
shape:

1. In `Server.toolMerge`, extract `caller := backendpkg.CallerIdentity(ctx)`
   and pass it explicitly: `s.sup.Merge(ctx, caller, agentName, message, noValidate)`.
2. Extend `Real.Merge` (and `Supervisor.Merge` interface) to accept the caller
   and override `mergeDeps.Getenv` for that single call — e.g. a per-call
   wrapper that returns the override for `SPRAWL_AGENT_IDENTITY` and falls
   through to `os.Getenv` for everything else.
3. Apply the same to `Retire` (which has the same `agentops/retire.go:59-67`
   guard) and the corresponding `toolRetire`.

Approximate footprint: ~30-50 LOC of plumbing changes plus tests.

## Bug A — `mcp__sprawl__delegate` silent task drop (NOT root-caused)

### Hypothesis from the brief vs. observed code

The brief hypothesized identity-based filtering analogous to bug B. **That
hypothesis is not supported by the current code:** `Real.Delegate` performs
no caller / parent check at all.

[`internal/supervisor/real.go:281-307`](../../internal/supervisor/real.go):
```go
func (r *Real) Delegate(_ context.Context, agentName, task string) error {
    agentState, err := state.LoadAgent(r.sprawlRoot, agentName)
    …
    enqueuedTask, err := state.EnqueueTask(r.sprawlRoot, agentName, task)
    …
    if runtime, ok := r.runtimeRegistry.Get(agentName); ok {
        runtime.RecordQueuedTask(enqueuedTask)
        if runtime.Snapshot().Lifecycle == RuntimeLifecycleStarted {
            _ = runtime.Wake()
        }
    }
    return nil
}
```

So caller identity does not influence delegate behavior at all today.

### What is verifiable

- The MCP path writes the task file to `r.sprawlRoot/.sprawl/agents/<name>/tasks/`.
- The supervisor's `r.sprawlRoot` is initialized from `SPRAWL_ROOT` of the
  `sprawl enter` process (cmd/enter.go ~L201). For weave's enter session this
  is the main repo root, which is the same root every child agent's
  `agentloop.Runner` polls for `NextTask` (see
  [`internal/agentloop/runner.go:764`](../../internal/agentloop/runner.go)).
- After enqueue, `runtime.Wake()` is fired (in-memory channel signal,
  [`internal/supervisor/runtime_launcher.go:101-103`](../../internal/supervisor/runtime_launcher.go))
  IF the child is in the in-process runtime registry. Wake errors are
  swallowed (`_ = runtime.Wake()`).
- The CLI path writes the same task file but skips Wake; the runner will pick
  it up at its next idle poll iteration.

### Why the symptom is suspicious

- The user reported verification via `messages_peek`, but `delegate` writes
  to the **tasks** queue, not the message inbox. `messages_peek` would
  legitimately return empty even for a correctly delivered task. **First
  thing to verify: was the task actually written to disk and consumed?**
  Inspect `.sprawl/agents/finn/tasks/` for files timestamped near
  16:31:19Z, and check finn's runner log for `[agent-loop] starting task <id>`.
- If the task file IS present but unread, the issue is that finn's runtime
  was not in this supervisor's registry → no Wake, and finn was not actively
  polling (e.g. blocked mid-turn or its runner exited).
- If the task file is NOT present, the failure happened in `state.LoadAgent`,
  `state.EnqueueTask`, or before — and the MCP handler still returned
  success, which would itself be a bug (worth verifying via tower's MCP
  response capture: was a non-error response actually returned, or did the
  bridge-side timeout swallow an error?).

### Plausible root causes (in order of likelihood)

1. **Verification artifact.** The user used `messages_peek` (mailbox) instead
   of inspecting `.sprawl/agents/finn/tasks/`; the task may have been
   delivered correctly. If finn was paused or unresponsive at that moment, a
   delayed pickup would still appear as "lost" in the user's experience.
2. **Runtime not in in-process registry.** If finn was spawned out-of-band
   (e.g. tower's `sprawl spawn` CLI subprocess attached its runtime to a
   transient supervisor in the subprocess, not to the long-lived in-process
   supervisor), the registry lookup falls through and Wake is skipped.
   Tasks are still persisted, but finn would only see them whenever its
   runner re-polls — which depends on what's keeping the runner ticking.
   This would not explain a difference vs the CLI path, however, since the
   CLI also doesn't fire any wake.
3. **MCP request response loss.** Some bridge corner case where the response
   appears successful to claude but the call body actually no-op'd. Less
   likely; would warrant inspecting the bridge transport logs.

### Recommended next steps for full diagnosis

- Reproduce: from a manager child, call `mcp__sprawl__delegate` and
  immediately list `.sprawl/agents/<child>/tasks/`. Verify whether the task
  file appears.
- Verify the runner ticks: tail the child's agentloop log (`[agent-loop] …`
  output) before and after the call. A successful path should show
  `[agent-loop] starting task <id>` once the runner re-enters its main loop.
- If the task file is present but unread, instrument
  `runtime.Wake()` (or its equivalent) to confirm the wake signal was
  actually queued for the child's runner.

### Proposed fix shape (if bug confirmed)

Independent of the unknown root cause, two robustness changes would close
common failure modes:

1. **Identity propagation** parallel to the bug B fix. Even though Delegate
   does not currently use caller identity, propagating caller identity is
   the right hygiene for future audit/parent-check additions.
2. **Wake-file fallback.** If `runtime.Wake()` is a no-op because the
   runtime isn't in the in-process registry, fall back to writing
   `.sprawl/agents/<name>.wake` (which the existing CLI/messages path uses
   for cross-process wakes) so the runner picks up the new task on its next
   loop iteration regardless of which supervisor delivered it. The fallback
   is cheap and idempotent — `runner.go:816` already removes the wake file
   on tick.

## Verdict

- **Bug B (merge):** definitively root-caused. Same root cause class as
  QUM-387 (which fixed `report_status` only). Trivial fix (~30-50 LOC) by
  applying the same context-identity pattern.
- **Bug A (delegate):** root cause not in caller-identity (the brief's
  hypothesis is not supported by current code). Symptom is real per the
  reporter; needs runtime evidence (task file present? runner log?) to
  pin the actual failure. Once isolated, fix is likely small (registry-miss
  fallback to wake-file).

## Reflections

- **Surprise:** The two bugs do **not** share a root cause as the brief
  hypothesized. Bug B's root cause (env-derived caller identity) does not
  apply to Delegate because Delegate has no caller-identity check today.
- **Open question:** Whether the delegate task actually landed on disk in
  the reported incident. The reporter's verification (`messages_peek`)
  doesn't inspect the tasks queue. Without that observation it's hard to
  know whether the bug is a delivery failure, a wake-up failure, or simply
  a verification artifact.
- **Next investigation if more time:** Reproduce in a sandbox per
  `/e2e-testing-sandboxing`: spawn a manager child, have the manager call
  `mcp__sprawl__delegate` against its child via the in-process supervisor,
  and instrument `state.EnqueueTask` + `runtime.Wake` to capture exact
  failure mode. Also audit `mcp__sprawl__retire` for the same env-identity
  bug class as bug B — `agentops/retire.go:59-67` has the identical
  `Getenv("SPRAWL_AGENT_IDENTITY")` + parent check.
- **Adjacent hazard:** `agentops.Retire` and any other agentops helper that
  reads `SPRAWL_AGENT_IDENTITY` from a long-lived deps struct will exhibit
  bug-B class behavior when invoked from a child via MCP. The fix should
  scope the retrofit to all such call sites at once.
