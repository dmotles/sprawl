# QUM-727 — Stopped agents keep claude subprocess + eventbus subscribers alive

**Author:** scout (researcher)
**Date:** 2026-06-09
**Status:** design — implementation not started
**Linear:** [QUM-727](https://linear.app/qumulo-dmotles/issue/QUM-727)
**Related:** QUM-606 (recover-live), QUM-625 (liveness/Status axis split), QUM-668 (terminal-outcome Status), QUM-638 (SIGTERM grace), QUM-669 (viewport resync)

---

## TL;DR

`report_status({state:"complete"|"failure"})` flips the agent's persisted
`Status` to a terminal liveness (`stopped` / `faulted`) but never calls
`runtime.Stop`. The in-memory `UnifiedRuntime`, its turn-loop goroutine,
its four eventbus subscribers, and the underlying `claude` subprocess all
remain alive — ~280 MB RSS plus ~5–10 goroutines per "done" agent. Fix:
hook a single `runtime.Stop(ctx)` call into the terminal-outcome arm of
`Real.ReportStatus`. Expose the leak directly via two booleans on the
`status` MCP payload (`subprocess_alive`, `eventbus_subscribed`) so the
next regression is visible without a sigdump.

---

## 1. Current lifecycle map

There are **six** code paths that today can land an agent with
`Status="stopped"` (or `"faulted"`) on disk. Only three actually tear
down the live runtime; the other three leak.

| # | Trigger | Disk `Status` written | Subprocess killed? | Subscribers cancelled? | Leak? |
|---|---|---|---|---|---|
| 1 | `report_status(complete)` → `agentops.Report` | `stopped` | **no** | **no** | **YES** ← primary bug |
| 2 | `report_status(failure)` → `agentops.Report` | `faulted` | **no** | **no** | **YES** ← primary bug |
| 3 | `mcp__sprawl__retire` → `Real.Retire` → `runtime.Stop` / `StopAbandon` | `retired` | yes | yes | no |
| 4 | `mcp__sprawl__kill` → `Real.Kill` → `runtime.Stop` | `killed` | yes | yes | no |
| 5 | Backend terminal fault → `unified.go`'s terminal-error handler cancels `runCtx` → loop exits → `watchHandleExit` | `faulted` | yes (loop exit closes session) | yes | no |
| 6 | `RecoverAgents` settle-pass (post-restart): legacy `active` with terminal `LastReportState` → `Status=stopped/faulted` | `stopped`/`faulted` | n/a (no live handle at boot) | n/a | no |

### The healthy paths (3, 4, 5) all converge on the same teardown

`AgentRuntime.stopWithFunc` (`internal/supervisor/runtime.go:867`) /
`unifiedHandle.stopOnceWith`
(`internal/supervisor/runtime_launcher.go:592`) is the canonical
teardown. In order it:

1. Calls `UnifiedRuntime.Stop` (or `StopWithOptions{SkipPoliteInterrupt:true}`)
   which cancels the turn-loop `runCtx`, waits `loopWG`, optionally issues
   a polite `Session.Interrupt`.
2. Joins each of the four per-runtime EventBus subscribers
   (`stopFault`, `stopUsage`, `stopDelivery`, `stopActivity`). Each `stop*`
   closure calls `unsub()` (which removes from `EventBus.subscribers` and
   closes its channel) then waits for its forwarder goroutine.
3. Calls `teardownSession` →
   `backend/claude/adapter.go:transport.Close()` → SIGTERM →
   `defaultTermGrace` (200 ms) → SIGKILL → bounded `Wait` (5 s,
   `unifiedHandleStopWaitTimeout`).
4. Closes the activity NDJSON file.
5. Stamps the durable disk `Status` (preserves
   `killed/retired/retiring/faulted`; otherwise `stopped`).
6. Emits `RuntimeEventStopped`.

This path is well-engineered and battle-tested. The fix is to **route
paths #1 and #2 through it**.

### The leaking paths (1, 2)

`internal/agentops/report.go:99` `Report()` only mutates persisted state:

```go
switch stateVal {
case ReportStateComplete:
    agentState.Status = state.StatusStopped
case ReportStateFailure:
    agentState.Status = state.StatusFaulted
}
deps.saveAgent(...)
```

Then `Real.ReportStatus`
(`internal/supervisor/real.go:1472`) calls `r.syncRuntimeFromState(name)`
which only mirrors the persisted Status into the snapshot — it does NOT
touch the live handle. `SyncAgentState`
(`internal/supervisor/runtime.go:937`) explicitly *preserves* the
in-memory `liveness.Running` when a handle is attached:

```go
case r.handle == nil && (updated.Status == StatusFaulted || ... StatusStopped ...):
    updated.Liveness = liveness.Unstarted
...
case r.snapshot.Liveness == liveness.Running:
    updated.Liveness = liveness.Running     // ← handle still attached, leak preserved
```

So a `report_status(complete)` agent ends up with:
- `disk.Status = "stopped"`
- `runtime.snapshot.Liveness = Running`
- `runtime.handle != nil`
- `claude` subprocess running (~280 MB)
- 4 EventBus subscribers spinning
- TurnLoop goroutine alive on `queue.Pop`
- Activity NDJSON file open

…which is exactly the leak observed in the 2026-06-09 incident
(`forge`, `ghost`, `probe`, `query`, `recon`, `scout`, `tower`, `trace`).

---

## 2. Root cause

**`agentops.Report` writes a terminal `Status` without instructing the
supervisor to tear down the live runtime.**

Specific file:lines:

- **Bug site (source of truth flip):**
  `internal/agentops/report.go:132-137` — sets
  `Status = StatusStopped` / `StatusFaulted` and persists, but
  `agentops.Report` has no handle to the supervisor and no seam to drive
  teardown.
- **Bug site (missed teardown hook):**
  `internal/supervisor/real.go:1497` — `r.syncRuntimeFromState(agentName)`
  is the only post-Report runtime touchpoint. It mirrors snapshot fields
  but never calls `runtime.Stop`.

This was a deliberate-but-incomplete design choice in QUM-668: the
"terminal report outcomes drive Status to terminal liveness" change
recognised that an `active` zombie next to a `complete` `LastReportState`
was incoherent, but only fixed the persistence half. The runtime-teardown
half was never written.

**Question 3 (is this intentional warm-restart?):** No. The warm-restart
guard for QUM-606 lives in `AgentRuntime.Recover` and is explicit:
`Status="stopped"` (deliberate stop) is rejected as a recover source;
`Status="faulted"` is recoverable but only because `watchHandleExit`
stamps it durably after the handle is gone (M4 invariant 3, runtime.go
:617-631). The same logic applies after our fix: a
`report_status(failure)`-induced stop will produce `handle == nil` +
`Status="faulted"` on disk, projecting `liveness.Faulted` — exactly the
Recover precondition. We strictly *improve* recover correctness by
removing the "lie window" where a live but disk-faulted handle exists.

---

## 3. Proposed fix

### 3.1 Core change: tear the runtime down when a report is terminal

**File:** `internal/supervisor/real.go`
**Method:** `Real.ReportStatus`
**Insertion point:** immediately after `agentops.Report` returns success
and *before* `r.syncRuntimeFromState`.

```go
// QUM-727: terminal-outcome reports (complete/failure) must release the
// live runtime — subprocess + EventBus subscribers — to prevent stopped
// agents from pinning ~280 MB RSS each and inflating goroutine fan-out.
// agentops.Report has already flipped the durable Status to stopped/faulted;
// runtime.Stop's terminal-status guard (stopWithFunc) preserves faulted,
// so the disk Status survives this teardown intact. The subsequent
// syncRuntimeFromState mirrors the now-Stopped snapshot Liveness back
// into the runtime registry.
if reportState == agentops.ReportStateComplete || reportState == agentops.ReportStateFailure {
    if runtime, ok := r.startedRuntime(agentName); ok {
        stopCtx, cancel := withRuntimeStopTimeout(ctx)
        if stopErr := runtime.Stop(stopCtx); stopErr != nil {
            slog.Warn("supervisor: ReportStatus runtime.Stop failed",
                slog.String("agent", agentName),
                slog.String("state", reportState),
                slog.Any("err", stopErr))
        }
        cancel()
    }
}
```

Behaviour:

- `startedRuntime` returns nil for an already-stopped agent → idempotent
  if a parent / multi-report storm races. (`stopOnce` inside
  `unifiedHandle` is the second line of defence.)
- A failure of `runtime.Stop` is **non-fatal**: the durable
  `LastReportState` and `Status` writes have already happened. We log
  and continue. Returning the error to the caller would falsely block a
  legitimate "complete" report.
- `withRuntimeStopTimeout` already bounds the call at 10 s. The
  underlying `unifiedHandle.Stop` re-bounds the post-Kill Wait at 5 s.

### 3.2 Grace window decision

**Reuse what's already there.** No new constants.

- **SIGTERM → SIGKILL grace:** `defaultTermGrace = 200 ms`
  (`adapter.go:23`). Already established by QUM-638; chosen to let claude
  flush its transcript / wirelog on a clean exit without blowing the
  500 ms happy-path budget. Configurable via `SPRAWL_TERM_GRACE` for
  forensic / test use.
- **Post-Kill `Wait` cap:** `unifiedHandleStopWaitTimeout = 5 s`
  (`runtime_launcher.go:573`). Bounds the QUM-542 stuck-pipe drain class.
- **Total `ReportStatus.runtime.Stop` budget:** ≤ ~5.2 s in the worst
  case (200 ms grace + 5 s wait), well inside the 10 s
  `runtimeStopTimeout` used by retire / kill.

Rationale for *not* introducing a new "soft drain" window before
`runtime.Stop`: the agent's last turn has already returned by the time it
calls `report_status` (you cannot call an MCP tool mid-turn — the tool
call *is* part of a turn whose result frame closes the turn). There is no
in-flight tool-output stream to preserve.

### 3.3 Per-file change list (in implementation order)

1. **`internal/supervisor/real.go`** — add the teardown block to
   `ReportStatus` (section 3.1). Also widen
   `Real.ReportStatus`'s `ctx` plumbing: today the parameter is
   `_ context.Context`; rename to `ctx context.Context` so the bounded
   `withRuntimeStopTimeout(ctx)` call gets cancel-on-shutdown semantics
   from upstream.

2. **`internal/runtime/eventbus.go`** — expose `SubscriberCount() int`:

   ```go
   // SubscriberCount returns the number of currently-registered subscribers.
   // QUM-727: surfaced through mcp__sprawl__status as the eventbus_subscribed
   // boolean so stopped-but-leaking runtimes are visible.
   func (b *EventBus) SubscriberCount() int {
       b.mu.RLock()
       defer b.mu.RUnlock()
       return len(b.subscribers)
   }
   ```

3. **`internal/supervisor/runtime.go`** — add accessor on `AgentRuntime`
   returning whether `r.handle != nil` *and* the EventBus subscriber
   count, both probed under `r.mu`:

   ```go
   // SubprocessAlive reports whether a live RuntimeHandle is currently
   // attached. Distinct from the projected liveness — a fault that has
   // detached but not yet been disk-stamped reads false here. (QUM-727)
   func (r *AgentRuntime) SubprocessAlive() bool { ... }

   // EventBusSubscriberCount returns the live subscriber count on the
   // underlying UnifiedRuntime's EventBus, or 0 if no handle is attached.
   // (QUM-727)
   func (r *AgentRuntime) EventBusSubscriberCount() int { ... }
   ```

4. **`internal/supervisor/supervisor.go`** — extend `AgentInfo`:

   ```go
   SubprocessAlive    bool `json:"subprocess_alive"`
   EventbusSubscribed bool `json:"eventbus_subscribed"`
   EventbusSubCount   int  `json:"eventbus_sub_count,omitempty"`
   ```

   Keep `ProcessAlive *bool` unchanged for back-compat (it's already part
   of the public MCP payload). `SubprocessAlive` is the new ground-truth
   field; `ProcessAlive` remains the liveness-projection field. After
   the fix the two should agree in steady state.

5. **`internal/supervisor/real.go` — `Real.Status`** — populate the new
   fields from the new accessors. Document that `SubprocessAlive ==
   false && status == "stopped"` is the post-QUM-727 invariant.

6. **`internal/sprawlmcp/server.go`** — no change to `toolStatus` itself
   (it already JSON-marshals `AgentInfo`); but update the tool's
   description text in `internal/sprawlmcp/tools.go` so the new fields
   are documented in the schema the agent sees.

### 3.4 Out of scope for this fix (callable as follow-up issues)

- The per-agent EventBus is *per-runtime*, not shared with weave's.
  The forensics H1 hypothesis ("17 fan-out subscribers on weave's bus")
  was structurally wrong: weave's bus has only TUI + activity. So the
  "publish backpressure" symptom is largely driven by RSS + goroutine
  count, not weave's bus fanout. A future "publish-latency histogram by
  subscriber count" instrumentation (forensics rec #3) is still worth
  doing but does NOT block this fix.
- Reducing per-claude RSS (~280 MB) — upstream.
- The two `mcp-calls.jsonl` orphan-`start` entries (forensics §c).

---

## 4. Test plan

### 4.1 Unit — `internal/agentops/report_test.go`

`TestReportTerminalOutcomeStampsTerminalStatus` already exists (QUM-668
covered it). Extend the file to also assert the *non-terminal* arm:
`TestReportNonTerminalOutcomeLeavesStatusAlone`. No runtime involvement.

### 4.2 Unit — `internal/supervisor/real_runtime_test.go` (new test, or new file `real_report_status_teardown_test.go`)

`TestReportStatusCompleteTearsDownRuntime`

Setup: use the existing fake `RuntimeStarter` + fake `Session` pattern
from `runtime_test.go` / `real_runtime_test.go`. Spawn an agent, drive a
turn (or short-circuit the start), call
`real.ReportStatus(ctx, "agent", "complete", "done")`.

Assert:
- `state.LoadAgent(...).Status == "stopped"`
- `runtime.Snapshot().Liveness == liveness.Stopped`
- `runtime.SubprocessAlive() == false`
- `runtime.EventBusSubscriberCount() == 0`
- Recorded session ops include `Close` and `Wait`
- Returns no error

Mirror `TestReportStatusFailureTearsDownRuntime` (asserts
`Status == "faulted"` and that the runtime is still **recoverable** —
call `real.Recover` and observe a new handle attached, confirming the
QUM-606 invariant survives).

### 4.3 Unit — `internal/supervisor/real_runtime_test.go`

`TestReportStatusWorkingPreservesRuntime` — regression guard that
non-terminal reports do not tear down. Subprocess + subscribers stay.

### 4.4 e2e matrix — extend `recover-live` row

`recover-live` (`scripts/e2e-matrix.sh`, mandatory-test table in
`CLAUDE.md`) is the right home: any change to `ReportStatus` that
interacts with subprocess lifetime must keep recover semantics intact.
Add a scenario:

1. Spawn a child via real claude.
2. From the child, `report_status({state:"failure", summary:"x"})`.
3. From parent, assert via `ps --ppid` that the child's claude PID is
   gone within ~6 s.
4. From parent, call `mcp__sprawl__recover` on the child; assert a
   fresh PID appears, the child responds to a `send_message`, and
   `Status` flips back to `active`.

A second scenario: same but `state:"complete"`. Recover MUST be rejected
(the QUM-625 M4 invariant 3 — `stopped` is not a legal recover source).

### 4.5 Live sandbox — acceptance criterion 5

The Linear AC already prescribes: spawn 6 short-lived agents that
complete immediately, then `ps --ppid <weave_pid>` and confirm only
still-active agents remain; `kill -USR1 <weave_pid>` and confirm no
orphaned `eventbus.Subscribe` frames keyed to retired agents.

Wrap this as a one-off script under `scripts/manual/qum-727-leak-check.sh`
(NOT in CI — too long-running and depends on real claude).

---

## 5. Instrumentation plan

### 5.1 New `status` JSON fields

Surface in `mcp__sprawl__status` (assembled in `Real.Status` →
`AgentInfo`):

| field | type | semantics |
|---|---|---|
| `subprocess_alive` | `bool` | `runtime.handle != nil` — ground truth |
| `eventbus_subscribed` | `bool` | `EventBus.SubscriberCount() > 0` |
| `eventbus_sub_count` | `int` (omitempty) | exact count, for debugging fan-out load |

Post-fix invariant (acceptance-test it):

> `status == "stopped" || status == "faulted" || status == "killed" || status == "retired" ⇒ !subprocess_alive && !eventbus_subscribed`

### 5.2 Assembly site

`internal/supervisor/real.go:403` `Real.Status` — extend the existing
per-runtime loop:

```go
for _, runtime := range r.runtimeRegistry.List() {
    snap := runtime.Snapshot()
    ...
    subAlive := runtime.SubprocessAlive()
    subCount := runtime.EventBusSubscriberCount()
    subprocessAliveByName[snap.Name] = subAlive
    eventbusSubCountByName[snap.Name] = subCount
}
```

Then populate `AgentInfo.SubprocessAlive`,
`AgentInfo.EventbusSubscribed`, `AgentInfo.EventbusSubCount` from
those maps inside the `for _, a := range agents` loop.

### 5.3 Optional: SIGUSR1 sigdump enrichment

`internal/observe/sigdump` already writes goroutines + fds. Add a
per-runtime block to `in-flight.json`:

```json
{ "agents": { "<name>": { "subprocess_alive": true, "ebus_subs": 4, "pid": 12345 } } }
```

— easy add and pays the next forensic incident back in one minute. Defer
to a follow-up if it bloats this PR; the MCP-tool fields above are the
must-have.

---

## 6. Risk to QUM-606 recover-live

Explicit checklist — verify each before merging.

| invariant | before fix | after fix | notes |
|---|---|---|---|
| `Status="stopped"` is NOT a recover source | ✓ (runtime.go :637) | ✓ (unchanged) | The Liveness projection still rejects Stopped. |
| `Status="faulted"` IS a recover source | ✓ | ✓ | Now reached via two paths: (a) backend terminal fault → `watchHandleExit` stamps faulted (existing); (b) `report_status(failure)` → `agentops.Report` stamps faulted → `Real.ReportStatus` calls `runtime.Stop` → `stopWithFunc` preserves `Status="faulted"` (new). Both end with `handle==nil + disk=faulted`, projecting `Faulted`. |
| Healthy live handle short-circuit (`ErrRecoverNotNeeded`) | ✓ | ✓ | Only triggered when `handle != nil && !IsTerminallyFaulted`. Unchanged. |
| Resume cookie preservation | ✓ | ✓ | `report_status(failure)`-induced Stop does NOT clear `SessionID` (stopWithFunc only writes `Status`). Recover's `RuntimeStartSpec.Resume=true` works. |
| Post-recover `Status="active"` write | ✓ | ✓ | runtime.go :721-735 unchanged. |
| Recover-live e2e matrix row passes | ✓ (assumed; baseline) | must be re-run after fix | Mandatory per `CLAUDE.md`. |
| `RecoverAgents` (sprawl-enter resume) skips terminal `LastReportState` | ✓ (real.go :863) | ✓ (unchanged) | Children that completed/failed before a restart are still excluded from auto-resume. |
| `Real.Recover` proactively cancels `ask_user_question` (QUM-611) | ✓ | ✓ | Independent of the report path. |
| Stop while a turn is running | n/a (terminal reports happen mid-tool, which means turn is "active" in the sense that the report tool-call result frame hasn't closed it) | ⚠ verify | `UnifiedRuntime.Stop` issues a polite `Session.Interrupt` before `cancel()`. The report-MCP-tool result has already been written by the time `runtime.Stop` runs (the MCP dispatch finishes after `ReportStatus` returns). Need an integration test to confirm the result frame still flushes to claude's stdin before the `Stop`-induced `Close` shuts the pipe. Worst case: add a small `runtime.Stop` deferral (e.g. dispatch via `go func()` after the MCP reply is sent). **Open question — see §7.** |

### 6.1 Cross-issue overlap audit (QUM-739, QUM-606)

- **QUM-739** (tower's territory:
  `internal/agentops/{retire,merge,terminal_error}.go`) — no overlap.
  `agentops.Report` is in `internal/agentops/report.go`; QUM-739
  touches neighbouring files but not Report. The supervisor-side hook
  proposed here lives in `internal/supervisor/real.go`, which QUM-739
  does not modify.
- **QUM-606** — high overlap. The fix touches the same `runtime.Stop` /
  `watchHandleExit` / `Recover` triangle. Mitigation: (a) re-run the
  recover-live e2e matrix row after the change; (b) the new tests
  explicitly assert both arms (`complete` → Stopped → not recoverable;
  `failure` → Faulted → recoverable).

---

## 7. Reflections + open questions

### Surprises

- **Per-runtime, not weave-shared, EventBus.** The original forensics
  hypothesis H1 ("17 fan-out subscribers on weave's bus") was
  structurally wrong: each child agent has its own `UnifiedRuntime`
  with its own `EventBus`. Weave's bus has only the TUI viewport +
  activity subscriber. The lag mechanism, post-fix, will most likely
  be explained by RSS / scheduler / pagecache contention from 17 live
  claude subprocesses rather than publish-fanout. The fix still
  addresses the user-visible symptom (the 280 MB × 8 pinned-stopped
  RSS), but the publish-latency-by-subscriber-count instrumentation
  (forensics rec #3) is a separate, lower-priority follow-up.

- **`agentops.Report` writes terminal `Status` but cannot reach the
  supervisor.** This is an architectural-seam mismatch: the
  state-persistence half is in `agentops`, the runtime-teardown half
  needs a Supervisor reference, and `agentops.Report` is intentionally
  Supervisor-free. The fix lives one layer up at the Supervisor
  call site, which is correct but worth flagging as a recurring shape.

- **`syncRuntimeFromState` deliberately preserves `Running` when a
  handle is attached** (runtime.go :963-966). That comment makes the
  leak look intentional — but it's intentional for a *different* case
  (in-flight recover writing transient disk status while the new
  handle is healthy). The leak was always an oversight, not a feature.

### Open questions

- **MCP tool result flush vs. `runtime.Stop` race (see §6 table).**
  The `report_status` tool returns a JSON result that must reach
  claude's stdout pipe *before* the SIGTERM/SIGKILL closes the
  transport. The MCP dispatch in `internal/sprawlmcp/server.go`
  serialises the result before `ReportStatus` returns, but the result
  frame is then queued onto claude's stdout subscriber path — does it
  fully drain before `runtime.Stop`? **Mitigation candidate:** run the
  `runtime.Stop` in a `go func()` and let `ReportStatus` return
  immediately. The teardown grace then absorbs any in-flight flush.
  *Engineer: validate this with the new unit test (`Close` must come
  after the tool result write).*

- **Should `complete` and `failure` differ in teardown urgency?**
  Today the fix treats them identically. Argument for differentiating:
  a `failure` agent's parent may want to call `recover` immediately —
  any milliseconds spent in SIGTERM grace pure overhead. Argument
  against: 200 ms is trivial against a fresh claude startup (~2-3 s).
  Recommend keep identical; one less knob.

- **Eventbus `SubscriberCount()` lock cost on a hot
  `mcp__sprawl__status` poll.** Negligible at 17 agents × ~5
  subscribers and RLock, but the TUI calls status every ~1 s. Confirm
  no regression in the existing status-bar refresh microbench (if one
  exists; otherwise this is paper-only worry).

### What I would investigate next

1. Re-run the 2026-06-09 forensics methodology *after* the fix lands,
   with the new `subprocess_alive` / `eventbus_subscribed` fields
   surfaced. Goal: confirm RSS sum drops from ~4.77 GB to ~2.5 GB
   (only the ~9 active agents × 280 MB).
2. Publish-latency-by-subscriber-count histogram (forensics rec #3) —
   confirms (or rules out) any residual eventbus contention on weave's
   own bus during TUI bursts.
3. Audit `LastReportType=="done"` / `"problem"` legacy callers to see
   if any pre-QUM-668 state files still carry `Status="done"` and
   are leaking; the schema migration in `state.go:81-108` handles them
   on Load, but a runtime that started before migration ran could be
   in a hybrid state.

---

## Appendix A — file:line index for the implementing engineer

| concern | location |
|---|---|
| Bug site (persistence-only terminal flip) | `internal/agentops/report.go:132-137` |
| Bug site (no teardown hook) | `internal/supervisor/real.go:1493-1497` |
| Fix insertion point | `internal/supervisor/real.go:1497` (before `syncRuntimeFromState`) |
| Canonical teardown | `internal/supervisor/runtime.go:867` `stopWithFunc`; `internal/supervisor/runtime_launcher.go:592` `stopOnceWith` |
| Terminal-status preservation in teardown | `internal/supervisor/runtime.go:910-919` |
| Subprocess SIGTERM→SIGKILL grace | `internal/backend/claude/adapter.go:23` `defaultTermGrace = 200 * time.Millisecond` |
| Post-Kill Wait cap | `internal/supervisor/runtime_launcher.go:573` `unifiedHandleStopWaitTimeout = 5 * time.Second` |
| Stop budget | `internal/supervisor/real.go:1538` `runtimeStopTimeout = 10 * time.Second` |
| Recover precondition (Stopped rejected) | `internal/supervisor/runtime.go:617-640` |
| Disk Status M4 invariant 3 | comments at `internal/supervisor/runtime.go:625-631` |
| `AgentInfo` (where new fields land) | `internal/supervisor/supervisor.go:18-35` |
| `Real.Status` (assembly site for new fields) | `internal/supervisor/real.go:403-465` |
| `EventBus.subscribers` (where SubscriberCount peeks) | `internal/runtime/eventbus.go:128-147` |
| Existing recover-live e2e harness | `scripts/e2e-matrix.sh`, mandatory-test table in `CLAUDE.md` |
