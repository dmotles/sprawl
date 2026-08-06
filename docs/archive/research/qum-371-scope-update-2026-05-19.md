# QUM-371 / QUM-372 Scope Update — 2026-05-19

**Author:** ghost (researcher, under weave)
**Base:** main @ `b9eac90` (QUM-600/603/602/601 merge)
**Predecessor:** `docs/research/agent-resume-after-restart.md` (2026-04-29, trace)
**Scope:** Re-slice QUM-372 (MVP happy path) against today's code. Research only — no code changes.

---

## 1. What QUM-372's original plan called for (April 29)

From the ticket + design doc, the MVP vertical slice was:

1. **State model** — introduce `StatusSuspended = "suspended"` in `internal/state/`.
2. **Graceful shutdown** — in `supervisor.Real.Shutdown()`, set actively-running agents to `"suspended"` instead of `"killed"`. Explicit `sprawl kill` continues to mark `"killed"`.
3. **`ResumeRunner()`** in the runtime launcher — a launcher variant that sets `Resume=true` on the initial `SessionSpec`, skips initial-prompt delivery, and wraps stderr with `claude/resumewatch.MarkerWriter`.
4. **`RecoverAgents()`** on the supervisor — scans `.sprawl/agents/`, filters `status=="suspended"` + `parent==weave`, ensures runtime registry entries, and launches with resume=true. Failures are logged but don't block remaining recovery.
5. **Wire into `cmd/enter.go`** — call `RecoverAgents` after supervisor init, before the TUI loop.

Acceptance: ctrl+c → run again → suspended children resume with prior context; `done`/`killed` agents are skipped; TUI shows the resumed agents in the tree; `make validate` green.

QUM-373 (resume-failure fallback w/ context reconstruction), QUM-374 (hard-kill orphan recovery), QUM-375 (UX: badge, `--no-resume`, suspension TTL) sit on top.

---

## 2. What QUM-372 can now reuse (already on disk)

Massive shift since April. Many primitives QUM-372 expected to build are present.

### 2.1 Resume-aware runtime start spec — **done**

`internal/supervisor/runtime.go:70-81`

```go
type RuntimeStartSpec struct {
    Name, Worktree, SprawlRoot, SessionID, TreePath string
    Resume bool  // QUM-601
}
```

Propagated into the backend `SessionSpec` inside `prepareLaunch`:

`internal/supervisor/runtime_launcher.go:206-208`

```go
// QUM-601: propagate the Resume flag from the RuntimeStartSpec ...
sessionSpec.Resume = spec.Resume
```

Net: any starter caller can flip `Resume=true` and the backend will be told to `--resume <SessionID>`. QUM-372 step 3's "ResumeRunner" already exists in shape — it's a flag on the existing starter.

### 2.2 Handle-swap recovery pattern — **done** (QUM-601)

`internal/supervisor/runtime.go:450-533` — `AgentRuntime.Recover(ctx)`:

- TryLock-fail-fast against concurrent Recover (`recoverMu`, line 451).
- Reads snapshot, builds `RuntimeStartSpec{Resume: true}` (line 468).
- Detaches old handle from registry **before** `StopAbandon` (lines 495-497) — suppresses the spurious `RuntimeEventStopped` the watcher would otherwise race against `RuntimeEventRecovered`.
- Calls `handle.StopAbandon(ctx)` — bounded teardown (QUM-600), no polite `Interrupt`.
- Calls `starter.Start(ctx, spec)` — same path a fresh spawn takes.
- Atomically swaps in the new handle, updates `Lifecycle = Started`, refreshes `SessionID` + `Capabilities` (lines 518-525).
- Emits `RuntimeEventRecovered` and re-arms `watchHandleExit`.

`Real.Recover()` at `internal/supervisor/real.go:713-744` wraps it with call-log checkpoints and the `recoveredEmitter` fan-out for the TUI fault-sticker clear.

**This is QUM-372 step 4 (`RecoverAgents`) for a single agent already implemented end-to-end.** The startup scan just needs to call it (or the equivalent path) for every suspended/orphaned child.

### 2.3 Bounded teardown — **done** (QUM-600)

- `RuntimeHandle.StopAbandon(ctx)` — `runtime.go:96-99`, `runtime_launcher.go:521-525`, `weave_handle.go:135-139`. Skips `Session.Interrupt`, just `Close → Kill → bounded Wait`.
- `teardownSession()` helper bounds Wait to `unifiedHandleStopWaitTimeout = 5s` (`runtime_launcher.go:511`).
- `StopWaitTimedOut` probe surfaces the bounded-Wait outcome to MCP-call checkpoints (`runtime_launcher.go:374-376`).

Means a wedged child during `Shutdown()` can't stall ctrl+c-to-suspend. QUM-372's worry about hangs during the suspend transition is mooted.

### 2.4 Resume-failure detection & fallback (weave) — **done** (QUM-598)

`cmd/enter.go`:

- `resumeFailureWindow = 5s` (line 56) + `restartState.resumeMarkerTripped atomic.Bool` (lines 81-89).
- `SessionSpec.OnResumeFailure` plumbed into `buildEnterSessionSpec` (line 308).
- Per-launch `onResumeFailure` closure stored, then drained in `makeRestartFunc` (`enter.go:107-155`) — `markerTripped` swap + time-since-last-start fallback selector.
- Initial-launch fallback at `enter.go:552-557`: if first session creation fails AND marker tripped, retry with `forceFresh=true`.
- Marker-driven TUI restart via `resumeFailureCh` + `RestartSessionMsg` (lines 686-698).

**For weave.** Children currently don't see any of this — see §3.4.

### 2.5 Recover MCP tool (interactive recovery) — **done** (QUM-601)

- Dispatch: `internal/sprawlmcp/server.go:207-208` → `toolRecover` at lines 452-470.
- `ErrRecoverNotNeeded` surfaces as `"Session healthy; no recovery needed for <name>"`.
- Errors fall through; success returns `"Recovered backend session for <name>"`.

Demonstrates the in-place handle-swap is a viable user-facing path. The startup auto-resume is a tighter loop of the same flow.

### 2.6 Fault surface chain — **done** (QUM-602)

- Per-runtime `runFaultSubscriber` on `EventBackendFaulted` (`runtime_launcher.go:144,310-336`).
- `Real.SetBackendFaultEmitter` / `SetBackendRecoveredEmitter` (`real.go:193,211`).
- TUI installs both in `cmd/enter.go:640-653`.

A resumed child whose `--resume` cookie is dead but whose subprocess crashes later still flows through the existing fault → TUI banner path. We don't have to rebuild it for QUM-372.

### 2.7 Disk-survival primitives — unchanged but worth restating

- Agent state JSON at `.sprawl/agents/<name>.json` — `state.SaveAgent`, `state.LoadAgent`, `state.ListAgents` in `internal/state/state.go:45-102`.
- Per-agent dir: SYSTEM.md, prompts, tasks, activity.ndjson — `state.AgentsDir(root)/<name>/`.
- Maildir + queue: `internal/messages/`, `internal/agentloop/queue.go` — file-locked, survive restart.
- Worktrees: plain dirs on disk.

### 2.8 Subscriber/MCP rewiring on resume — **free**

The starter's `Start()` (`runtime_launcher.go:96-184`) is single-path: every `starter.Start(spec)` call (whether `Spawn` or `Recover`) re-runs phases 1-9 including:

- `startBackendSession` calls `session.Initialize(initSpec)` if `ToolBridge != nil || len(MCPServerNames) > 0` (`runtime_launcher.go:246-252`) — children get their MCP bridge wired on each launch.
- EventBus subscribers (activity, delivery, fault) attached fresh in phase 5.
- `feedTasks()` drains queued tasks from disk into the runtime queue (phase 9).
- `coord` + sweep coordinator are fresh per-start.

So when `RecoverAgents` calls into the existing starter (with `Resume=true`), the child agent reconnects to MCP, EventBus subscribers, fault chain, and queued tasks **without any new wiring**. This was the biggest unknown in the original plan (`docs/research/agent-resume-after-restart.md` §4 Risk 4) and is no longer a risk.

---

## 3. What's still missing

### 3.1 `"suspended"` status concept

`internal/state/state.go:12-37` — `AgentState.Status` is a free-form string; constants aren't enumerated anywhere. Existing values observed in code:

- `"running"` (set by `RegisterRootRuntime` default — `real.go:347`)
- `"active"` (default in `reconcileStateFromRegistry` — `real.go:1326`)
- `"killed"` (`real.go:765`, `real.go:694`)
- `"retired"`, `"retiring"` (`real.go:460`)
- `"done"` (peppered, set by retire flow)

No `"suspended"`. QUM-372 step 1 still needed.

### 3.2 `Shutdown()` kills, doesn't suspend

`internal/supervisor/real.go:746-772` — `Shutdown` calls `runtime.Stop` then unconditionally sets `agentState.Status = "killed"`. Ctrl+C in `runEnter` invokes this path (`cmd/enter.go:822-825`).

Need: branch on the agent's prior status — only flip `Started` runtimes to `"suspended"`; preserve `"killed"`, `"retired"`, `"done"`. Probably also preserve `"running"`/`"active"` agents that ctrl+c caught mid-launch (treat as suspended).

### 3.3 No startup scan in `runEnter`

`cmd/enter.go:470-844` — `runEnter` builds supervisor, builds weave session, registers weave's root runtime, runs the TUI. Nothing iterates `state.ListAgents` to repopulate the child runtime registry. The registry starts empty post-restart; ctrl+c handed weave the unique privilege of in-process child runtimes, and there's no recovery handshake.

Need: a `Real.RecoverAgents(ctx)` (or extension method) called between `newSupervisor` and `runProgram` that:

1. `state.ListAgents(r.sprawlRoot)`.
2. For each agent where `parent == r.callerName` AND `status in {"suspended", "active", "running"}` AND name != root weave:
   - `r.runtimeRegistry.Ensure(AgentRuntimeConfig{SprawlRoot, Agent, Starter: r.runtimeStarter})` — note that `Spawn` sets `Starter` (`real.go:489`) but `Ensure` accepts it from config; need to pass `r.runtimeStarter`.
   - Call into a `StartResume(ctx)` variant of `AgentRuntime.Start` (see §3.5) or extend `Start` with a flag.
   - On error, log + continue; mark status `"failed"` or leave as is for next-launch retry.
3. Aggregate count → optional banner via the TUI message channel.

### 3.4 Children have no `OnResumeFailure` plumbing

Children's `SessionSpec` is built by `agentloop.BuildAgentSessionSpec(agentState, promptPath, sprawlRoot, io.Discard)` in `runtime_launcher.go:201`. No `OnResumeFailure` callback is set. The `claude/resumewatch.MarkerWriter` mechanism exists but is wired only on weave's spec (`cmd/enter.go:308`).

For QUM-372 happy path this is **tolerable** — the happy path assumes the session resumes successfully. But the failure mode (resume → fast exit → registry observes `RuntimeEventStopped` via `Done()` watcher) currently leaves the child dead with no retry. QUM-373 wants context-reconstruction here; QUM-372 should at minimum:

- Plumb `OnResumeFailure` from `RuntimeStartSpec` into `prepareLaunch` so we can detect the marker.
- On marker trip, fail the start with a sentinel (`ErrResumeFailed`-equivalent) so `RecoverAgents` can fall back to a fresh launch (`Resume=false`) and inject the initial prompt.

If we omit this for the MVP, the acceptance criterion "Resumed agents continue working with their prior Claude Code conversation context" still passes when the session is alive, and a dead session manifests as a stopped runtime visible in the TUI — degraded but not crashed. Recommend bundling minimal failure detection into 372.

### 3.5 `AgentRuntime.Start(ctx)` doesn't accept `Resume`

`runtime.go:277-310` — Start builds `RuntimeStartSpec` from snapshot inline, never sets `Resume`. `Recover` builds its own spec (line 468) with `Resume:true` and calls `starter.Start` directly, bypassing `AgentRuntime.Start`.

For startup auto-resume we want `AgentRuntime.Start` to optionally set `Resume=true`. Two reasonable shapes:

- **Add `StartResume(ctx)`** — mirrors `StopAbandon` precedent. Cleanest.
- **`Start(ctx, opts)` with a `StartOptions{Resume bool}`** — slightly bigger blast radius but only one caller today (`Real.Spawn` at `real.go:491`).

Recommended: add `StartResume(ctx)` to keep the diff small and the call sites obvious.

### 3.6 Atomic state writes

`state.SaveAgent` at `state.go:45-61` uses `os.WriteFile` directly. A `kill -9` mid-write could truncate. QUM-374 explicitly calls for write-to-temp-then-rename. **Not blocking for QUM-372** (graceful ctrl+c path has time to fsync), but cheap to add as part of 372 since the changes are touching `state.go` anyway.

### 3.7 Hard-kill orphan handling

QUM-374 territory. Out of scope for 372 but the §3.3 status filter should include `"active"`/`"running"` so that the same recovery path picks up orphans the next time a 374-driven scan extends behavior. If 372 uses `status in {"suspended"}` strictly, 374 has to refactor; if 372 uses `status in {"suspended","active","running"}` from day one, 374 just adds the dead-process probe.

Recommend: filter inclusive from day one; 372 simply marks via `"suspended"` on graceful shutdown.

### 3.8 No suspension policy / TTL

QUM-375. Out of scope for 372.

---

## 4. Suggested re-sliced implementation plan for QUM-372

Goal: end-to-end happy path (ctrl+c → relaunch → child resumes), with a minimal failure-mode guard so a dead session doesn't crash the supervisor. One engineer, 1-2 sessions.

### Step 1 — State enum + atomic write (small, foundational)

- `internal/state/state.go`:
  - Add constants `StatusActive = "active"`, `StatusRunning = "running"`, `StatusSuspended = "suspended"`, `StatusKilled = "killed"`, `StatusRetired = "retired"`, `StatusDone = "done"`. Don't enforce — just document the universe and provide referents.
  - Convert `SaveAgent` to write-to-temp-then-rename (`os.WriteFile(tmp); os.Rename(tmp, final)`). Pull out into a helper. Cheap, also satisfies QUM-374 partially.
- Tests: existing state tests cover round-trip; add one for the rename idempotency.

### Step 2 — Graceful shutdown → suspended

- `internal/supervisor/real.go:746-772` `Shutdown`:
  - Before flipping to `"killed"`, inspect prior status. Map:
    - `"killed"`, `"retired"`, `"retiring"`, `"done"` → leave alone.
    - everything else for a `RuntimeLifecycleStarted` runtime → `StatusSuspended`.
  - Also `runtime.SyncAgentState(agentState)` so the in-memory snapshot agrees (matters for the few seconds before runProgram fully exits).
- Add a unit test that builds a mock registry with three runtimes (one `"killed"`, one `"running"`, one `"done"`) and verifies the status transitions.

### Step 3 — `AgentRuntime.StartResume(ctx)`

- `internal/supervisor/runtime.go`: add `StartResume(ctx) error` mirroring `Start` but setting `RuntimeStartSpec.Resume = true`. Factor the shared body so `Start` and `StartResume` differ in one line. Emits `RuntimeEventStarted` (not `Recovered` — this is a fresh start with the resume flag, not a handle-swap).
- Existing `Recover` path is untouched; it remains the in-place-during-running flow.

### Step 4 — Wire `OnResumeFailure` through the starter (minimal failure guard)

- `internal/supervisor/runtime.go`: add `OnResumeFailure func()` to `RuntimeStartSpec`.
- `internal/supervisor/runtime_launcher.go:201` `prepareLaunch`: assign `sessionSpec.OnResumeFailure = spec.OnResumeFailure` next to the existing `sessionSpec.Resume = spec.Resume`.
- `AgentRuntime.StartResume` accepts/sets the callback. For QUM-372 the callback can be minimal: log + flip status to `"resume_failed"` (new sentinel) so the next launch starts fresh. Full context-reconstruction is QUM-373.

### Step 5 — `Real.RecoverAgents(ctx)`

- New method on `Real`. Iterates `state.ListAgents`. For each agent matching:
  - `agent.Parent == r.callerName` (i.e., direct children of this weave; deeper-tree resume is out of scope until QUM-371 generalizes).
  - `agent.Status in {StatusSuspended, StatusActive, StatusRunning}`.
  - `agent.Worktree` exists on disk (skip if the worktree was deleted between sessions).
- For each match:
  - `rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{SprawlRoot: r.sprawlRoot, Agent: agent, Starter: r.runtimeStarter})`.
  - `err := rt.StartResume(ctx)` — wrap with a per-agent log line.
  - On success, flip status back to `"active"` and `SaveAgent`.
  - On failure, log and continue (don't abort the loop). Status stays `"suspended"` for the next launch's retry.
- Returns `(resumedCount, []error)` — caller picks how loud to be.

### Step 6 — Wire into `runEnter`

- `cmd/enter.go:546-563`: after `deps.newSupervisor` returns and before `deps.newSession`, call `sup.RecoverAgents(ctx)`. Log line `[enter] recovered N suspended agents` to the same stderr-log path the rest of enter uses.
- Order considerations:
  - Must happen after `newSupervisor` (we need the registry).
  - Must happen before `runProgram` so the TUI's first `tickAgentsCmd` sees the resumed agents in `r.runtimeRegistry`.
  - Can happen before or after `newSession` for weave; before is cleaner because resumed children's MCP wiring uses `r.mcpBridge` which is set during `newSupervisor` (`real.go:386-394`).

### Step 7 — Tests

Mandatory:

- Unit: `Shutdown` transitions per §3.2 (already covered above).
- Unit: `RecoverAgents` with a mocked starter — verify it filters correctly, calls Start with `Resume=true`, handles per-agent failure without aborting the loop.
- Unit: `StartResume` propagates `Resume=true` into the `RuntimeStartSpec` passed to the starter.
- Integration-ish: spawn a fake child (test starter), shutdown → assert disk state is `"suspended"`, RecoverAgents → assert starter was called with `Resume=true` and the snapshot is `Started`.

Recommended but not blocking:

- E2E harness extension under `scripts/` — out of scope for the MVP. Add a follow-up issue.

### Step 8 — Validate + commit

`make validate` + `make build` smoke. No new mandatory-E2E surface touched outside of `cmd/enter.go` runEnter — but **the handoff-E2E (item #6 in CLAUDE.md) covers `cmd/enter.go` and `internal/supervisor/*.go`, both of which this change touches**. `make test-handoff-e2e` is required before merge.

---

## 5. Gotchas / risks (esp. things April's design didn't account for)

### 5.1 `RegisterRootRuntime` ≠ `Ensure` for children

`RegisterRootRuntime` (`real.go:336-367`) is the path weave uses; it bypasses the runtime starter (handle is built externally by `cmd/enter.go`). Children use `Ensure` + `Start` and need `Starter` set on the config. `RecoverAgents` must use the `Ensure`+`Start` shape with `r.runtimeStarter`, not `RegisterRootRuntime`.

### 5.2 Self-cleaning on collision

`defaultNewSession` (`cmd/enter.go:387-394`) already self-cleans a stale weave runtime entry. `RecoverAgents` should be idempotent: if a runtime entry already exists for an agent (shouldn't happen at startup, but defensive), skip rather than re-`Ensure`.

### 5.3 EventBus subscribers + MCP bridge survive — but only because the starter rewires every launch

§2.8 is the foundation. Don't optimize by trying to "reattach" subscribers — re-run the full starter. The reason QUM-601's in-place Recover works is that it tears down and re-runs the starter pipeline. QUM-372's resume is the same path.

### 5.4 Status drain race

`statusNotifier` (`real.go:133`, in-memory) is rebuilt empty on each `NewReal`. Any status notifications queued from descendants that were never delivered before ctrl+c are lost. Since descendants are also shutdown by the same `Shutdown` call, in practice there's no in-flight status during a clean ctrl+c. Worth a sentence in the commit message; not a code change.

### 5.5 Maildir + queue ordering across restart

Disk-backed; survive. `inboxprompt.SplitByClass` + `BuildQueueFlushPrompt` in the drain path (`runtime_launcher.go:453-501`) preserve ordering. New messages arriving while the child is "suspended" will be sitting in `pending/` and drained on first `WakeForDelivery` post-resume. Good.

### 5.6 Resumed child's TUI status row

The TUI's tree poller queries `sup.Status(ctx)` (`real.go:413`); resumed children appear because `state.ListAgents` returns them and the in-memory `runtimeRegistry` reports `processAlive=true` after `StartResume`. No new wiring needed for visibility. Whether to add a `[resumed]` badge is QUM-375.

### 5.7 SessionID staleness

`AgentState.SessionID` is the source of truth for resume. If a graceful shutdown lands while a child is mid-turn and `Session.SessionID()` has rotated (Claude Code rotates per turn? confirm in §6), the on-disk SessionID may be stale and resume would fail. **Open question** — see §6.

### 5.8 TreePath / Family — no work needed

`prepareLaunch` re-reads agent state (`runtime_launcher.go:190`) and feeds `Family`, `Parent`, `TreePath` back into the SYSTEM.md prompt regenerator. Resumed child keeps its place in the tree.

### 5.9 Fault chain re-arming

When a resumed runtime starts, the `runFaultSubscriber` (`runtime_launcher.go:144`) is recreated by the starter. The supervisor's `dispatchFault` indirection (`real.go:200-204`) ensures the live `faultEmitter` (installed by the TUI) is reached. So a fault on a resumed runtime surfaces to the TUI just like a fault on a freshly-spawned runtime. Good — but worth a one-line test.

### 5.10 Watchdog interaction

`installOrphanWatchdog` (`cmd/enter.go:207-224`) terminates the TUI if `SPRAWL_ROOT` disappears or weave's parent changes — independent of resume. Doesn't interfere.

---

## 6. Open questions to confirm with dmotles

1. **Resume scope: direct children only, or full tree?** The original ticket says `parent == "weave"`. With UnifiedRuntime, children-of-children are also in-process under this weave. If a manager has its own spawn tree, `RecoverAgents` could either (a) resume direct children only (manager re-spawns its own children) or (b) walk the whole tree. Recommended for MVP: direct children only, since (b) needs ordering (parents-first) and lifecycle policy that isn't in the original ticket. Confirm.

2. **Banner vs silent?** "N agents resumed" surfaced as a viewport banner, or just stderr log? The QUM-375 ticket flags this as UX — but a one-line viewport banner is cheap to add now and avoids the user wondering why agents exist in their tree they didn't spawn this session.

3. **Suspension policy / TTL?** QUM-375 leaves this as a design decision. For MVP: no TTL, always attempt resume, dead session manifests as resume-failed (per §3.4). Confirm OK to ship without a TTL.

4. **`"resume_failed"` status — accept the new sentinel?** §3.4 introduces it for the minimal failure guard. Alternative: keep `"suspended"` and let the next launch retry indefinitely. The sentinel is friendlier to the QUM-373 follow-up because it gives the fallback-launcher something to filter on.

5. **Do we want `--no-resume`?** QUM-375 has it. Trivial to add in step 6 (skip the `RecoverAgents` call). If we add it now, QUM-375 is just the badge work. Confirm.

6. **SessionID rotation under Claude Code resume**: when a child resumes via `--resume <id>`, does the post-resume session keep `<id>` or rotate to a new one? If it rotates, we need to capture the new ID on first post-resume turn and `SaveAgent` it, else the *next* restart resumes to a dead cookie. Currently `unifiedHandle.SessionID()` caches the ID at construction (`runtime_launcher.go:152`), and the snapshot is updated on `AttachHandle`/`Start` (`runtime.go:301`) — but only at launch time, not per-turn. Worth a quick empirical test before shipping.

---

## 7. Reflections

### What was surprising

- **QUM-601 already implements the hard part.** `AgentRuntime.Recover` is essentially the body of what `RecoverAgents`-per-agent would call. Re-reading the April design doc, "Phase 2: Resume Launcher" was projected as 3-4 days of risky work; today it's a `Resume=true` flag on an existing spec and a 2-line method.
- **The starter's single-path nature mooted the "subscriber re-attach" risk.** April's design (§4 Risk 4: "Resume Changes Conversation Flow") fretted about agentloop state. UnifiedRuntime's starter rebuilds everything per launch — EventBus, MCP bridge, fault chain, sweep coordinator, task feed — so the state-machine concern dissolves. The "agentloop" April referenced has since been deleted.
- **Disk-survival was already richer than the April doc claimed.** Read the §1 inventory and note we now have status notifier (in-memory, not persisted), validate emitter, ask-question queue (in-memory) — all of which would be lost on restart but none of which were broken by ctrl+c in the original design either.

### Open questions remaining

- §6 questions 1, 4, 5, 6 — design decisions, not research gaps.
- Will Claude Code reject `--resume` if the SessionSpec also includes `SystemPromptFile`? `internal/claude/launch.go:BuildArgs` (referenced by predecessor doc §1.2) is supposed to omit `--system-prompt-file` when `Resume==true`. Worth grepping in step 1 to confirm the contract still holds with today's adapter.
- Empirical: how long is the Claude session resumable after process exit? (April doc §6 Q1 — still unanswered.)

### What I'd investigate next if I had more time

- The QUM-601 Recover test surface (`internal/supervisor/runtime_test.go` for `Recover`) — copy/adapt that scaffolding for `StartResume` + `RecoverAgents` tests rather than rolling from scratch.
- Trace the `claude --resume` failure mode end-to-end in a sandbox — confirm `MarkerWriter` still wires correctly when called from the child starter path (since it was tested for weave, not for children).
- Sketch a 5-minute prototype: hardcode a `RecoverAgents` call in runEnter that ignores status (always resume any child), shutdown-ctrl+c-relaunch, and observe what happens. Could be done in <30 min and would either validate the plan or surface a missed wiring concern fast.

---

## 8. File:line cheat sheet

| Concern | File | Lines |
|---|---|---|
| Status string in state schema | `internal/state/state.go` | 12-37 |
| `SaveAgent` (needs atomic write) | `internal/state/state.go` | 45-61 |
| `ListAgents` | `internal/state/state.go` | 79-102 |
| `Shutdown` (sets killed) | `internal/supervisor/real.go` | 746-772 |
| `RegisterRootRuntime` | `internal/supervisor/real.go` | 336-367 |
| `Real.Recover` | `internal/supervisor/real.go` | 713-744 |
| `Real.Spawn` (Ensure+Start with starter) | `internal/supervisor/real.go` | 480-503 |
| `startedRuntime` helper | `internal/supervisor/real.go` | 1279-1288 |
| `RuntimeStartSpec` + `Resume` | `internal/supervisor/runtime.go` | 70-81 |
| `AgentRuntime.Start` (no resume flag) | `internal/supervisor/runtime.go` | 277-310 |
| `AgentRuntime.AttachHandle` (root path) | `internal/supervisor/runtime.go` | 318-334 |
| `AgentRuntime.Recover` (handle-swap) | `internal/supervisor/runtime.go` | 450-533 |
| `inProcessUnifiedStarter.Start` (9-phase) | `internal/supervisor/runtime_launcher.go` | 96-184 |
| `prepareLaunch` (Resume propagation) | `internal/supervisor/runtime_launcher.go` | 189-230 |
| Session Initialize on every start | `internal/supervisor/runtime_launcher.go` | 246-252 |
| `unifiedHandle.IsTerminallyFaulted` | `internal/supervisor/runtime_launcher.go` | 590-592 |
| `WeaveRuntimeHandle` (root) | `internal/supervisor/weave_handle.go` | 28-194 |
| MCP `recover` dispatch | `internal/sprawlmcp/server.go` | 207-208, 452-470 |
| Weave resume-failure plumbing | `cmd/enter.go` | 81-89, 107-155, 308, 519-525, 552-557, 686-698 |
| `runEnter` startup ordering | `cmd/enter.go` | 470-844 |
| Mandatory-test surface (handoff-E2E) | `CLAUDE.md` | §6 |
