# Architecture simplification audit — 2026-05-20

Author: ghost (Researcher)
Tracking: not a Linear issue — opinion piece for weave + dmotles.
Scope: read-only audit. No implementation, no work-breakdown, no PRs.
Premise: bugs keep recurring in the recover / lifecycle / messaging seams.
Hypothesis from weave: we have overcomplicated some seams. Find where.

This document stops at "what to simplify and why." Decision and sequencing
are up to weave and dmotles.

---

## 1. TL;DR — the five things I would change

1. **Make `RuntimeHandle` a fat interface, delete the type-assertion probes.**
   Five capabilities (`Done`, `IsTerminallyFaulted`, `InduceTerminalFault`,
   `InAutonomousTurn`, `StopWaitTimedOut`, `UnifiedRuntime`) are currently
   bolted onto a small core via duck typing. Every probe site has a
   "defensive default if not implemented" branch that papers over a missing
   abstraction. Make these required, satisfy them with `nopHandle` /
   `nopSession` defaults in tests, and the cluster-3 surface vanishes. Blast
   radius small; fan-out across two production handles, five test handles.

2. **Promote "alive" to a single state machine with a single observer.**
   Today five layers each model aliveness differently
   (`Lifecycle` / `RuntimeState` / `terminalErr` / `Status` / `process_alive`)
   and only one fault path actually wakes Done(). Collapse to one
   `AgentLiveness` enum owned by `AgentRuntime`, computed from the backend
   session's terminal-error + transport-alive signals, and emit a single
   stream of transitions on a single channel. Today's
   `RuntimeLifecycle×RuntimeState×terminalErr` cross-product is the source
   of QUM-602 (banner doesn't re-fire) and the QUM-606 secondary issues.
   Blast radius medium.

3. **One child→parent notification path, not three.** Maildir-`Send`,
   `report_status` ring, and `RuntimeEvent` bus are three parallel paths
   that each carry "something happened in a child." The drain-row e2e
   exists because they silently diverge. Fold `report_status` into the
   maildir as a `type=status_change` synthetic message; let `defaultNotifier`
   be the only fan-out. Removes the in-memory `statusNotifier` ring,
   removes the `statusDrainer` field on `unifiedHandle`, removes the
   "two callers must agree on which path" coordination problem. Blast
   radius medium; touches inboxprompt, drain pipeline, TUI tickers.

4. **Subprocess lifetime should never share a ctx with an MCP request.**
   The QUM-606 fix replaced one `starter.Start(ctx, …)` with
   `starter.Start(context.Background(), …)`. The bug class (every callsite
   along the chain has to remember to detach) is still present. Make
   `RuntimeStarter.Start` take **no** ctx, derive its own internally,
   accept a `CancelHint <-chan struct{}` if cooperative cancellation is
   ever wanted. Forces the right pattern by signature. Blast radius small.

5. **Collapse six e2e harnesses to one matrix-driven harness.** The six
   `make test-*-e2e` shells each guard the same failure class: "MCP
   server / supervisor / TUI consumer / cmd/enter forwarder wired to
   mismatched instances." That is one design problem (cluster 4) being
   caught six times. The harnesses cost ~3000 LOC of shell, ~30s
   each on CI, and every change to messaging/runtime requires reading
   four `CLAUDE.md` bullets to know which to run. Build one harness that
   spawns a child, drives a `(notifier_path, recipient_type, send_class)`
   matrix in-process, asserts on the same disk + TUI signals. Blast
   radius medium-to-large but mostly tests + Makefile.

The deeper recurring story: this codebase has been growing **probes** instead of
**contracts**. Every new failure mode adds a new `interface{...}` assertion or a new
sticky boolean on `session`. The simplifications above all share the shape
"replace probes with a single contract."

---

## 2. Failure-mode taxonomy

### Cluster 1 — ctx-lifetime confusion

**Symptoms.** Subprocesses that die at MCP-request-end (QUM-606). Sends that
discard the caller's deadline (QUM-600). Interrupts that race the wedge
detector (QUM-603). Hangs because the wrong ctx flows below a syscall
boundary.

**Code citations.**

- `internal/backend/claude/adapter.go:146` — `exec.CommandContext(ctx, …)`
  binds subprocess life to the caller's ctx.
- `internal/backend/session.go:313` — reader ctx is **explicitly** detached:
  `context.WithCancel(context.Background())`.
- `internal/runtime/unified.go:274` — turnloop runCtx is **explicitly**
  detached: `context.WithCancel(context.Background())`.
- `internal/runtime/turnloop.go:194-199` — per-turn ctx is **explicitly**
  wrapped with `TurnTimeout`.
- `internal/supervisor/runtime.go:598` — recover-path subprocess Start is
  **now explicitly** `context.Background()` (post-QUM-606).
- `internal/supervisor/real.go:492` — Spawn-path is also explicitly
  `context.Background()`.
- `internal/supervisor/runtime.go:306` — `startWithSpec` still forwards the
  caller's ctx into `starter.Start`. It is reachable from `Start` (line
  284, `Start(ctx)`) and `StartResume` (line 359). Resume-from-restart
  runs under the long-lived enter ctx so it's fine — but the precedent
  is exactly the QUM-606 trap.
- `internal/backend/claude/adapter.go:200-209` — `Send` is its own ad-hoc
  ctx-honoring wrapper because the underlying `WriteJSON` doesn't honor
  ctx (QUM-603).
- `internal/backend/session.go:915-926` — `Interrupt`'s bounded
  `interruptSendTimeout` wrapper exists for the same reason (QUM-600).

**Specific issues exemplifying.** QUM-600 (transport.Send discarded ctx).
QUM-603 (ctx-aware Send retrofit). QUM-606 (MCP-request ctx → SIGKILL on
tool return). QUM-549 (send_interrupt blind spot during MCP-tool-wait —
same shape: which ctx is the in-flight handler bound to).

**Pattern.** Every layer makes an ad-hoc decision about whether to honor or
detach from the caller's ctx. The decisions are written in prose comments
at each callsite. There are at least four ctxs in flight along a single
recover call: MCP request, supervisor recover, runtime Start (Background),
turnloop runCtx (Background), session reader ctx (Background), per-turn
turnCtx (WithTimeout). Tracing which one a write/read/syscall honors
requires reading five files.

### Cluster 2 — Lifecycle / Done() blindness to backend faults

**Symptoms.** Faulted session sits in `Lifecycle == Started` because
`Done()` doesn't close (QUM-606 secondary). Fault banner doesn't re-fire
on silent re-fault (QUM-602). Recover succeeds, agent is dead, supervisor
shows green (QUM-606 user-visible).

**Code citations.**

- `internal/supervisor/runtime.go:47-53` — RuntimeLifecycle: five values.
- `internal/runtime/unified.go:19-31` — RuntimeState: four values.
- `internal/backend/session.go:222-225` — `terminalErr` (sticky) +
  `fatalErr` (one-shot). Two error stores.
- `internal/backend/session.go:218` — `currentTurn *turnFrame` is the
  fifth "alive" signal.
- `internal/supervisor/real.go:420-431` — `Status` builds `ProcessAlive`
  from `Lifecycle` alone, with no consultation of `IsTerminallyFaulted`.
  So a faulted-but-not-yet-stopped runtime reports `ProcessAlive=true`.
- `internal/runtime/unified.go:121-152` — the **new** post-QUM-606 path
  to close Done(): SetTerminalErrorHandler → cancel runCtx → loopWG →
  closeDoneOnce. Five-hop chain. The handler is installed via a type
  assertion (line 126, `if setter, ok := cfg.Session.(interface { … })`).
- `internal/supervisor/runtime.go:836-854` — `watchHandleExit` is the only
  thing that flips `Lifecycle` from Started → Stopped on a backend fault.
  Goroutine per handle, lives forever.

**Specific issues.** QUM-602 (silent re-fault, banner missing). QUM-606
(zombie not observed). QUM-471 (viewport invisible inbox — same shape:
the event channel for "something happened" not wired through). QUM-465
(double-fire from in-process notifier and 2s tick).

**Pattern.** Five overlapping representations of "alive," each with its
own update path. The QUM-606 secondary fix wired one of the missing
edges; there is no architectural guarantee the other 19 cross-product
combinations are correctly wired. The post-fix `if lifecycle !=
Started && lifecycle != Stopped` check at `runtime.go:547` is itself a
tell — the lifecycle is now ambiguous (Stopped means "was faulted, no
live handle" OR "was deliberately stopped"), and the Recover path has
to special-case both.

### Cluster 3 — Type-assertion-probe interface proliferation

**Symptoms.** Adding a capability means adding a probe site, not extending
an interface. Tests that don't implement the optional method silently
take the defensive default — sometimes wrong. Refactoring is unsafe
because the probe sites aren't found by "Find Usages."

**Code citations.**

- `internal/supervisor/runtime.go:27-29` — `unifiedRuntimeProvider`
  (UnifiedRuntime).
- `internal/supervisor/runtime.go:116-118` — `runtimeHandleDone` (Done).
- `internal/supervisor/runtime.go:246` — `h.(interface{ InAutonomousTurn() bool })`.
- `internal/supervisor/runtime.go:566` — `handle.(interface{ IsTerminallyFaulted() bool })`.
- `internal/supervisor/runtime.go:674` — same probe again, this time on
  newHandle in the health-probe path.
- `internal/supervisor/runtime.go:725` — third copy of the same probe in
  `waitForTerminalFaultOrTimeout`.
- `internal/supervisor/runtime.go:754` — `handle.(interface{ InduceTerminalFault(error) })`.
- `internal/supervisor/runtime.go:778` — `handle.(interface{ StopWaitTimedOut() bool })`.
- `internal/runtime/unified.go:126` — `cfg.Session.(interface { SetTerminalErrorHandler(func(error)) })`.

**RuntimeHandle surface.**

```
declared (runtime.go:91-109)         duck-typed
─────────────────────────────       ─────────────────────────────
Interrupt                            Done() <-chan struct{}
Wake                                 IsTerminallyFaulted() bool
WakeForDelivery                      InduceTerminalFault(error)
ForceInterruptDelivery               InAutonomousTurn() bool
Stop                                 StopWaitTimedOut() bool
StopAbandon                          UnifiedRuntime() *runtimepkg.UnifiedRuntime
SessionID
Capabilities
```

Six "optional" methods. Three production handles
(`unifiedHandle`, `WeaveRuntimeHandle`, `testExportUnifiedHandle`)
implement all six. Test doubles implement a subset and rely on the
defensive defaults. `probeNewHandleHealth` at
`runtime.go:661` has special-case fallback paths for "no UnifiedRuntime,"
"no IsTerminallyFaulted," "handle has UnifiedRuntime but nil," etc. —
all of which exist because the interface lies about what's required.

**Pattern.** Bolt-on capabilities accumulate when adding to the named
interface feels like a big commitment, so each new feature gets a
local probe. finn called this out in the QUM-606 reflection. The cost
is paid at every probe site, forever, in defensive branches and
shadow tests.

### Cluster 4 — Messaging / notifier wiring fragility

**Symptoms.** Same regression class keeps recurring: "the MCP tool fires
on instance A, the TUI listens on instance B, the supervisor was built
in cmd/enter and passed to sprawlmcp.New but the notifier closure
captured a different supervisor." Six e2e shells exist.

**Code citations.**

- `cmd/enter.go:1-16` — the file's docstring is a 16-line warning about
  this exact problem.
- `cmd/enter.go:632-642` — `messages.SetDefaultNotifier(buildTUIRootNotifier("weave", send))`
  registers the notifier process-wide. Singleton via `defaultNotifierMu`
  (`internal/messages/messages.go:73-86`).
- `internal/messages/messages.go:172-188` — defaultNotifier fires from
  inside `Send`, swallows panics. Per-call `WithNotify` can override.
- `internal/supervisor/real.go:1382-1388` — `ReportStatus` pushes to the
  in-memory `statusNotifier` ring and **separately** calls
  `parentRuntime.WakeForDelivery()`. Two unrelated mechanisms, both
  needed.
- `internal/supervisor/runtime_launcher.go:454-502` —
  `drainPendingToQueue` is the only consumer of `statusNotifier.Drain`
  plus `agentloop.ListPending`. Both drains must run together.
- `internal/supervisor/weave_handle.go:96-108` — weave-side
  `WakeForDelivery` intentionally **does not** drain
  `agentloop.ListPending` because the TUI's `peekAndDrainCmd` does that
  job. Comment explicitly notes the divergence. So child-runtime and
  root-runtime have different drain contracts.
- `internal/sprawlmcp/server.go:42-53` — `Server.msgSender` is a third
  back-channel: the MCP server pushes `tui.MCPCallStartedMsg` / `Ended`
  into the TUI program when calls are in flight (QUM-497).

**Specific issues.** QUM-311 / QUM-312 (TUI-mode inbox notifier dropped
child→weave). QUM-329 (handoff MCP tool fires on one supervisor while
TUI listens on another). QUM-489 / QUM-511 (merge silently no-ops because
state.Branch stale). QUM-527 / QUM-535 (ask-user-question modal wired to
wrong instance, root-as-caller rejected). QUM-555 / QUM-565 (drain
pipeline broken). QUM-549 (send_interrupt blind spot).

**Pattern.** A "child told the parent something" event has at least three
delivery paths (maildir, statusNotifier ring, EventBus). Each path has
its own consumer in the TUI. The cmd/enter wiring must agree on
exactly one shared supervisor instance, and the test harnesses catch
the regression when that agreement breaks. The six-harness wall is
itself the failure mode being measured.

### Cluster 5 — In-memory vs on-disk state divergence

**Symptoms.** Disk-backed checks see stale data (QUM-535: eligibility gate
rejected weave because in-memory Type=root mutation never persisted).
Merge silently no-ops because state.Branch is stale (QUM-511). retire
fails because state file doesn't exist but registry does (QUM-404 →
reconcile dance).

**Code citations.**

- `internal/supervisor/real.go:337-368` — `RegisterRootRuntime` now
  explicitly persists agentState to disk on type assignment (QUM-535 fix
  at line 358). Comment is half the function body.
- `internal/supervisor/real.go:1440-1475` — `reconcileStateFromRegistry`
  synthesizes an AgentState from the in-memory snapshot when the on-disk
  file is missing.
- `internal/supervisor/real.go:1506-1547` — `reconcileRuntimeTreeFromState`
  goes the other direction.
- `internal/supervisor/runtime.go:808-834` — `SyncAgentState` mirrors a
  loaded AgentState into the snapshot. Caller must remember to call it.
- `internal/supervisor/real.go:1398-1408` — `syncRuntimeFromState` is the
  glue: load from disk, push to snapshot. Called from ReportStatus.
- `internal/supervisor/real.go:1422-1431` — `startedRuntime` consults the
  in-memory registry, not disk, for the `WakeForDelivery` / 
  `ForceInterruptDelivery` decision in `SendMessage`.

**Specific issues.** QUM-404 (state file missing, runtime present). QUM-511
(merge no-op via stale state.Branch). QUM-489 (toolMerge hides no-op).
QUM-535 (RegisterRootRuntime forgetting SaveAgent). QUM-476 (TUI
in-flight agent registry desync).

**Pattern.** Two-source state ownership. Each mutation point has to
remember whether it's a write-through to disk or in-memory-only. Bugs
look like "I wrote here but somebody else read there."

### Cluster 6 — Multiple parallel paths for child→parent notification

**Symptoms.** Children's messages don't reach the parent. Or reach it
twice. Or reach the TUI but not the prompt. The drain-row e2e exists
exactly because these paths silently diverge.

**Code citations.**

- `internal/messages/messages.go:101-190` — maildir `Send` writes
  new/, fires defaultNotifier, copies to sent/. Three side effects.
- `internal/messages/messages.go:73-86` — process-level
  `defaultNotifier` singleton.
- `internal/supervisor/real.go:1215-1274` — `SendMessage` writes
  maildir (`messages.Send`), persists envelope (`agentloop.Enqueue`),
  **and** wakes the runtime (`WakeForDelivery` or
  `ForceInterruptDelivery`). Three side effects again, partially
  overlapping with `messages.Send`'s.
- `internal/supervisor/real.go:1351-1390` — `ReportStatus` uses a
  **different** path entirely: state.SaveAgent, `statusNotifier.Enqueue`,
  `parentRuntime.WakeForDelivery`. No maildir, no agentloop entry.
- `internal/supervisor/runtime_launcher.go:454-502` —
  `drainPendingToQueue` is the consumer of both async maildir
  entries and statusNotifier lines, with bespoke order-of-operations
  prepending status lines to the async batch.
- `internal/supervisor/weave_handle.go:106-108` — weave's
  `WakeForDelivery` **does not** drain `agentloop.ListPending`. The
  TUI's `peekAndDrainCmd` handles that job
  (`internal/tui/app.go:2261`).
- `internal/runtime/eventbus.go` — per-runtime EventBus carries
  RuntimeEvent: protocol frames, turn lifecycle, fault. Used by
  activity subscriber, delivery-confirmation subscriber, fault
  subscriber. A fourth path.
- `internal/inboxprompt/inboxprompt.go:111-160` — three different
  `<system-notification>` shapes: type=message, type=message
  interrupt=true, type=status_change.

**Specific issues.** QUM-323 (drain broken). QUM-555 (slim notification
prompts). QUM-565 (test-notify-tui-e2e regression). QUM-462 / QUM-510
(InterruptDelivery TOCTOU). QUM-549 / QUM-550 (cooperative-wake vs
force-interrupt split).

**Pattern.** Three full data paths and four signal paths for what users
think of as "child sent something to parent." Each path is independently
correct; the system requires they agree. They don't always.

---

## 3. Seam maps

### 3a. Ctx flow through `mcp__sprawl__recover`

```
MCP request ctx (cancelled at tool return)
   │
   └─> sprawlmcp.handleToolsCall ctx                     server.go:111
         │
         └─> toolRecover(ctx)                            server.go:461
               │
               └─> Real.Recover(ctx)                     real.go:714
                     │
                     └─> AgentRuntime.Recover(ctx)       runtime.go:500
                           │  uses ctx for:
                           │   - probeNewHandleHealth  (ok — bounded internally)
                           │   - handle.StopAbandon    (ok — bounded internally)
                           │
                           └─> starter.Start(context.Background(), spec)   ← QUM-606 fix
                                 │                                         runtime.go:598
                                 └─> inProcessUnifiedStarter.Start(Background)
                                       │                                   runtime_launcher.go:96
                                       └─> startBackendSession(Background)
                                             │                             runtime_launcher.go:237
                                             └─> unifiedAdapterStartFn(Background, spec)
                                                   │                       runtime_launcher.go:36
                                                   └─> backendclaude.NewAdapter().Start(Background, spec)
                                                         │                  adapter.go:61
                                                         └─> realStarter.Start(Background, execSpec)
                                                               │            adapter.go:145
                                                               └─> exec.CommandContext(Background, …)
                                                                            ← subprocess dies on Background.Done
                                                                              (never), not on MCP-ctx.Done
                                                                              ← this is the bug class

then session.Start launches:
    runReader(context.Background())   session.go:313 — detached from caller
    runObserverDrain                  session.go:330
    runHangWatchdog(readerCtx)        session.go:332

then UnifiedRuntime.Start launches:
    turnLoop.Run(runCtx)              unified.go:294, runCtx = WithCancel(Background)
    closeDoneOnce watcher             unified.go:298

per-turn ctx wrapping happens INSIDE turnloop:
    turnCtx = WithTimeout(ctx, TurnTimeout)   turnloop.go:194-199
```

**Switch points** — each is an architectural decision written in prose:

1. `runtime.go:598` — Recover must detach (QUM-606).
2. `runtime_launcher.go:171` — `rt.Start(context.Background())` for Start.
3. `runtime_launcher.go:242` — `session.Start(context.Background())` for Start.
4. `session.go:313` — reader ctx is Background-derived.
5. `unified.go:274` — turnloop runCtx is Background-derived.
6. `turnloop.go:194-199` — per-turn timeout wrap.

The right ctx flows below the right boundary only if all six humans got
all six decisions right. Any new pathway has to make all six again.

### 3b. "Alive" state representation per layer

| Layer | State | Update sites | Observable to |
|---|---|---|---|
| `transport` (adapter) | unix process via `cmd.Process` | `realStarter.Start`, `Kill`, OS reap | `Pid()`, `Wait()`, `Send` failure |
| `backend.session` | `started bool` | `Start` (atomic-once) | not exposed |
| `backend.session` | `fatalErr error` | `setFatalErr` (many sites), consumed by `LastTurnError` | `LastTurnError` |
| `backend.session` | `terminalErr error` (sticky) | `setTerminalErr` only — F1 wedge, D1 hang, `InduceTerminalFault` | `IsTerminallyFaulted`, fires `terminalErrHandler` |
| `backend.session` | `currentTurn *turnFrame` | `StartTurn`, `runReader` on `result`, defer | `InAutonomousTurn`, gates hang-watchdog |
| `UnifiedRuntime` | `started`, `stopped`, `turnRunning`, `state` | `Start`, `Stop`, wrapper `StartTurn`, `Interrupt`, terminalErrHandler | `State`, `Done` |
| `UnifiedRuntime` | `done chan struct{}` | `closeDoneOnce` after `loopWG.Wait` | external watchers |
| `AgentRuntime` | `snapshot.Lifecycle` (5 vals) | `Start`, `Stop`, `Recover`, `watchHandleExit`, `SyncAgentState`, attach paths | `Snapshot`, RuntimeEvent stream, AgentInfo |
| `AgentRuntime` | `stopWaitTimedOut atomic.Bool` | `stopWithFunc` | retire / kill checkpoints |
| `AgentInfo` | `ProcessAlive *bool` | derived from Lifecycle | MCP `status` tool output |
| `state.AgentState` (disk) | `Status` ("active"/"running"/"suspended"/"killed"/"retired") | every report_status, every retire/kill, RegisterRootRuntime | every disk reader |

**Who observes what.**

- `UnifiedRuntime.Done()` is the only Started→Stopped trigger for
  `AgentRuntime.Lifecycle` other than explicit `Stop`/`StopAbandon`.
  Before QUM-606, a session terminal-fault did NOT close Done(); the
  loop kept spinning on `Queue.Signal()` between turns. Now (QUM-606
  R2 fix in `unified.go:139-150`) `setTerminalErr` cancels runCtx →
  loopWG unblocks → Done closes → watchHandleExit flips Lifecycle.
  The chain works but it is 5 hops with no integration test that the
  shape is preserved on future edits.
- `terminalErr` is observable to: `LastTurnError`, `IsTerminallyFaulted`,
  `terminalErrHandler` (one-shot, installed via type assertion).
- `currentTurn` is observable to: `InAutonomousTurn`, the hang
  watchdog's `turnActive` gate.
- `Lifecycle` is observable to: every consumer of RuntimeSnapshot and
  AgentInfo.
- `Status` (disk) is observable to: every consumer of state.AgentState.
- **No single value answers "is this agent currently usable?"**

This is cluster 2 made visible.

### 3c. Child → parent notification paths

```
                         child does ...                            parent observes via ...

(A) maildir async send   sup.SendMessage(to=parent, interrupt=false)
                            ├─> messages.Send  (writes new/, fires defaultNotifier) ─┐
                            ├─> agentloop.Enqueue (persists envelope)                 │
                            └─> runtime.WakeForDelivery                               │
                                  └─> handle.drainPendingToQueue                      │
                                        ├─> ListPending → BuildQueueFlushPrompt       ▼
                                        └─> Queue.Enqueue(ClassInbox) ──────► next-turn prompt
                                                                                      │
                            and in the TUI:                                           │
                            defaultNotifier ─> tui.InboxArrivalMsg ─► banner + badge ─┘

(B) maildir interrupt    sup.SendMessage(to=descendant, interrupt=true)
                            ├─> messages.Send (same as A)
                            ├─> agentloop.Enqueue (ClassInterrupt)
                            └─> runtime.ForceInterruptDelivery
                                  └─> handle.drainPendingToQueue → ClassInterrupt
                                  └─> rt.ForceInterruptForDelivery
                                        ├─> session.Interrupt (always, even when idle)
                                        ├─> loop.Interrupt (if turnRunning)
                                        └─> queue.Wake

(C) report_status        sup.ReportStatus(reporter, state, summary)
                            ├─> state.SaveAgent (LastReport* fields)
                            ├─> statusNotifier.Enqueue(parent, line)   ← in-memory ring
                            └─> parentRuntime.WakeForDelivery
                                  └─> handle.drainPendingToQueue
                                        └─> statusDrainer(parentName) → prepended

(D) backend event        EventBus.Publish (per-runtime)
                            consumed by:
                              activitySubscriber (writes activity.ndjson)
                              deliveryConfirmationSubscriber (sweep coord)
                              faultSubscriber (calls faultEmitter → TUI banner)
                              tui viewport stream subscriber (QUM-439)
```

**Observations.** Paths A and B share their data store (maildir +
agentloop pending) but differ in supervisor side effect (Wake vs
ForceInterrupt). Path C does not touch maildir or agentloop pending —
it uses an in-memory `statusNotifier` ring instead. Path D is for
"what's happening inside one runtime," but the **fault subscriber**
in (D) is also "child telling parent something." Four paths converge
on "the parent's claude needs to know."

For weave the divergence is even sharper: `WeaveRuntimeHandle`'s
`WakeForDelivery` intentionally **does not** drain pending — it relies
on the TUI's polling tick. Children use the inline drain. Two contracts
for the same method (`weave_handle.go:96-108` vs
`runtime_launcher.go:441-452`).

### 3d. Type-assertion-probe surface on RuntimeHandle

```
Declared interface  (runtime.go:91-109)
─────────────────────────────────────────
RuntimeHandle:
  Interrupt(ctx) error
  Wake() error
  WakeForDelivery() error
  ForceInterruptDelivery() error
  Stop(ctx) error
  StopAbandon(ctx) error
  SessionID() string
  Capabilities() backend.Capabilities

Duck-typed probes (each ad-hoc at one or more call sites)
─────────────────────────────────────────────────────────
  runtimeHandleDone:           Done() <-chan struct{}                   runtime.go:116-118
                               probed at:                               runtime.go:320, 381, 639

  unifiedRuntimeProvider:      UnifiedRuntime() *UnifiedRuntime         runtime.go:27-29
                               probed at:                               runtime.go:38, 662

  inline: InAutonomousTurn() bool                                       runtime.go:246
  inline: IsTerminallyFaulted() bool                                    runtime.go:566, 674, 725
  inline: InduceTerminalFault(error)                                    runtime.go:754
  inline: StopWaitTimedOut() bool                                       runtime.go:778

Implemented by:
  unifiedHandle (child)        runtime_launcher.go:339-369  — all six
  WeaveRuntimeHandle (root)    weave_handle.go:31-44        — all six
  testExportUnifiedHandle      runtime_test_export.go       — all six
  test doubles in *_test.go    various                      — subset; rely on defensive defaults
```

Three of the inline probes are the **same** check (`IsTerminallyFaulted`)
in three different call sites. Two of them have a comment saying "handles
that don't expose the probe are treated as faulted" or "treated as
healthy" — opposite defaults at different probe sites, which is itself a
correctness hazard.

---

## 4. Simplification candidates (prioritized)

Each candidate cites the clusters it closes and a blast-radius estimate.
Estimates assume the implementer is willing to update tests but not
willing to break public state-file or MCP-tool contracts.

### S1. Fat-interface `RuntimeHandle` + nopHandle defaults

**What.** Promote the six duck-typed methods to declared interface
methods. Provide a `nopRuntimeHandle` embed for tests that don't care
about a given capability (returns false / nil / no-op).

**Why.** Closes cluster 3 entirely. Eliminates the three copies of
`handle.(interface{IsTerminallyFaulted() bool})`. Removes the
"defensive default" branches at 7 call sites. The two opposite defaults
(`waitForTerminalFaultOrTimeout` line 728 returns nil meaning "healthy,"
`Recover` line 564 treats missing-probe as "faulted") become impossible
to write because the interface says they must be implemented.

**Blast radius.** Small. Two production handles + ~5 test doubles need
to grow methods or embed `nopHandle`. No behavior change in production.

**Risk.** Test doubles in other packages that satisfy
`supervisor.RuntimeHandle` will fail to compile until updated; that's
the whole point. Verify there are no embedded-handle leaks across
package boundaries first.

### S2. Single `AgentLiveness` state machine

**What.** Collapse `RuntimeLifecycle` (5 vals) × `RuntimeState`
(4 vals) × `terminalErr` (sticky bool) × `Status` (disk string) ×
`process_alive` (derived) into ONE `AgentLiveness` enum
{`Registered`, `Starting`, `Running`, `Faulted`, `Stopping`, `Stopped`,
`Killed`, `Retired`} owned by `AgentRuntime`. Computed from a single
source — backend session state — and broadcast on a single channel.
Disk `Status` becomes a serialization of this enum.

**Why.** Closes cluster 2. Closes most of cluster 5: the disk Status
field is no longer separately authoritative, it's a projection of the
in-memory state machine. `ProcessAlive` becomes `Liveness == Running`.
`IsTerminallyFaulted()` becomes `Liveness == Faulted`. The QUM-606
"Lifecycle=Stopped means either faulted or deliberately stopped"
ambiguity goes away (Faulted is its own state).

**Blast radius.** Medium. Touches every reader of Lifecycle / Status,
including the TUI tree-row renderer, the recover preconditions, the
retire cascade. The serialization format is the riskiest part — every
existing on-disk state.AgentState needs to keep deserializing.

**Risk.** Recover's "accept Stopped because it's actually post-fault"
hack at runtime.go:547 becomes "accept Faulted." That's a behavior
change visible to operators. Worth getting right.

### S3. Unified child→parent event stream

**What.** Make `report_status` write to the maildir as a `subject="status_change"`
synthetic message with the body lines that `BuildStatusNotification`
currently produces. Drop the `statusNotifier` in-memory ring entirely.
Drop `Real.DrainStatusNotifications`. Drop `statusDrainer` on
`unifiedHandle`. Drop the `statusLines` prepend in
`drainPendingToQueue`. The maildir + `agentloop.Enqueue` + 
`WakeForDelivery` becomes the **only** path.

**Why.** Closes most of cluster 4 and cluster 6. Removes one of the
three notification paths. The drain-row e2e (`test-drain-row-inject-e2e.sh`)
becomes a single path test. The QUM-559 in-memory ring exists because
"status updates shouldn't traverse maildir" was an aesthetic decision
— but it costs us a second drain pipeline, a second consumer in
`drainPendingToQueue`, and a divergent contract between child
(`unifiedHandle`) and root (`WeaveRuntimeHandle`).

**Blast radius.** Medium. `report_status` flow changes; agents will see
status changes as inbox-class entries. Operators will see a new mail
category. Worth being explicit that this is a UX shift.

**Risk.** Maildir spam if `report_status` is high-rate. Mitigation: a
synthetic-message coalescing pass in `drainPendingToQueue`, or a
`type=status_change` filter on TUI rendering.

### S4. Ctx-detached `RuntimeStarter.Start`

**What.** Change the signature:

```go
// before
Start(ctx context.Context, spec RuntimeStartSpec) (RuntimeHandle, error)
// after
Start(spec RuntimeStartSpec) (RuntimeHandle, error)
```

Internal subprocess ctx is always Background-derived. If a caller
wants to cancel a long-running setup (currently nobody does — all
prep work is synchronous), add `spec.CancelHint <-chan struct{}` and
let the starter `select` on it before the point of no return.

**Why.** Closes most of cluster 1. The QUM-606 trap (and its precursor
in QUM-600/603) is fundamentally "the wrong ctx was forwarded to
exec.CommandContext." By signature, that becomes impossible. The
mental load of "which ctx do I forward at each layer" drops to one
decision per layer (the per-turn timeout, which is legitimate
business logic).

**Blast radius.** Small. Two `Start` implementations
(`inProcessUnifiedStarter.Start`, test starters), three call sites
(`AgentRuntime.startWithSpec`, `AgentRuntime.Recover`, post-restart
resume).

**Risk.** Any future use case that legitimately wants to cancel a
`Start` mid-flight needs the `CancelHint` channel. Add it if needed;
don't add it speculatively.

### S5. One e2e harness, six matrix cases

**What.** Replace the six `scripts/test-*-e2e.sh` files with one
`scripts/test-supervisor-e2e.sh` that drives a matrix of (sender_type,
recipient_type, message_class, expected_disk_signal,
expected_tui_signal). Each row of the matrix is one row in the
six-test grid today.

**Why.** Closes the "we have six harnesses to catch one failure class"
smell in cluster 4. The harnesses share ~70% of their setup
(sandbox env, tmux pane, sprawl enter launch). Today every messaging
change has to read four `CLAUDE.md` bullets to know which harnesses
are mandatory. The bullets ARE the design — and the design is "we
distrust our own wiring."

**Blast radius.** Medium-to-large but mostly test code.
`CLAUDE.md` mandatory-tests list shrinks from 6 to 1. The shared
matrix table becomes the artifact, ~200 lines instead of ~3000.

**Risk.** Pre-existing assertions in each harness are subtle (the
ask-user-question Phase 0/Phase 1 split, the recover Phase 4 PID
liveness assertion). Don't lose them in the merge; codify them as
named matrix rows.

### S6. Collapse `WakeForDelivery` / `ForceInterruptDelivery` /
`Interrupt` / `Wake` into one method

**What.** Today RuntimeHandle has four methods that all "tell the
runtime there's new work" with different urgency. Replace with:

```go
Notify(ctx, opts NotifyOptions) error
// opts.Class:    ClassInbox | ClassInterrupt | ClassTask | ClassUser
// opts.Preempt:  bool — preempt the in-flight turn?
// opts.Reason:   string — observability hint
```

**Why.** Closes the rest of cluster 6 and a slice of cluster 4. The
`WakeForDelivery` / `ForceInterruptDelivery` split (QUM-549/550)
exists to encode interrupt vs cooperative in the API. The split
exists because the legacy `InterruptDelivery` had a TOCTOU race
(QUM-462, QUM-510); the fix was "two methods that don't make a
conditional decision." But the conditional decision is still
required at the call site — it just moves up to `Real.SendMessage`
(`real.go:1262-1266`). Folding to one method with an explicit `Preempt`
bool makes the decision explicit by parameter.

**Blast radius.** Medium. Touches all callers of WakeForDelivery /
ForceInterruptDelivery / Wake / Interrupt. About 12 production call
sites + many tests.

**Risk.** The intent of the QUM-549/550 split was to make it
impossible to write the conditional-gate-on-turnRunning bug. The
fat `Notify(opts)` API can reintroduce it if implementers gate on
`opts.Preempt` plus a turn-running check. Test for it.

### S7. Single ctx field on RuntimeStartSpec for cancellation hints

**Variant of S4, smaller scope.** If the appetite for S4 is too big,
at minimum: remove the ctx parameter from
`AgentRuntime.startWithSpec`'s call chain and pass it via
`RuntimeStartSpec.CancelHint <-chan struct{}` so the trap is in one
place per layer instead of one per signature.

---

## 5. What I would NOT simplify

Equally valuable to flag. Things that look complex but are load-bearing.

- **`backend.session`'s `runReader` defer LIFO ordering.** The three
  defers in `session.go:479-517` (drop current turn, drain inflight
  MCP handlers, close observer channel) look like they could be
  collapsed. They can't: the order is precisely calibrated to QUM-552
  (async-MCP-handler shutdown), QUM-595 (observer drain after reader
  exit), and the `events`-channel-close-implies-observer-flushed
  invariant relied on by tests. Comments at lines 504-517 spell out
  why. Keep as-is.

- **Two-phase MCP control-request dispatch (sync `can_use_tool` vs
  async `mcp_message`).** `session.go:735-779` looks like it could
  be uniformly async. It can't: sync `can_use_tool` is fast and
  reordering with subsequent stream frames would break claude. QUM-552
  documents the design call. Keep as-is.

- **`Stop` vs `StopAbandon` split.** Two stop variants seems
  redundant. They aren't: the polite-Interrupt path is needed for
  clean retire, and skipping it is needed for the wedged-pipe
  abandon case (QUM-600). The `StopOptions.SkipPoliteInterrupt`
  field is small and self-documenting. Keep as-is.

- **Per-turn `TurnTimeout` ctx wrap in TurnLoop.** Looks like it
  duplicates the outer-loop ctx. It doesn't: it distinguishes
  parent-shutdown (silent) from per-turn deadline (TurnFailed). QUM-581.
  Keep as-is.

- **Sticky `terminalErr` vs one-shot `fatalErr` on `session`.** Two
  error stores feels wrong. They're modeling two different things:
  recoverable per-turn errors (consumable) and permanent
  session-death (sticky). Collapsing to one would break either the
  next-StartTurn fast-reject (terminalErr) or the
  LastTurnError-clears-after-consume contract.

- **The `defaultNotifier` singleton.** Cluster 4 complains about
  notifier wiring, but the SINGLETON itself isn't the problem — it's
  exactly the right shape for "one process-wide TUI sender." The
  problem is that there are multiple data paths into the TUI
  (notifier, EventBus, statusNotifier, msgSender). S3 attacks that,
  not the singleton.

- **The pendingInterrupt mechanic in UnifiedRuntime.** Looks like a
  hack (a bool that conditionally classifies the next StartTurn's
  terminal event). It's load-bearing: closes the race where
  Interrupt is called between turns. QUM-462 / QUM-550. Without it
  EventInterrupted classifications would be wrong.

---

## 6. Open questions

These are places where reading the code alone didn't tell me whether
the complexity is essential or accidental. Each would be resolved by
a follow-up empirical test or design conversation with dmotles.

1. **Is the in-memory `statusNotifier` ring a deliberate "don't put
   status in the inbox" UX call, or an artifact of the QUM-559
   implementation that happened to land first?** S3 collapses it.
   But if dmotles wants `messages_list` to NOT show status events,
   S3 needs a `type` field filter. Quick conversation, not a code
   read.

2. **Can the QUM-606 "no health probe means treat as healthy"
   default at `runtime.go:728` ever be exercised in production?**
   Both production handles expose `IsTerminallyFaulted`. The
   defensive default is for tests. If true, it's dead-code-in-prod
   and should be deleted (which is the same shape as S1).

3. **Does the post-restart `RecoverAgents` path
   (`real.go:772-852`) need its own `IsTerminallyFaulted` probe?**
   It currently doesn't. A child whose persisted state says "active"
   but whose backend would immediately re-fault on resume gets no
   detection. Empirical question: does this happen in practice? The
   QUM-606 doc flagged "could --resume immediately re-wedge?" as
   open and recommended a health probe at recover-time but not at
   resume-time. The R4 probe at `runtime.go:617` is for Recover;
   StartResume skips it.

4. **Is `process_alive` in `AgentInfo` ever consulted by anything
   that would notice if it lied?** The status MCP tool returns it.
   Operators read it. Does any agent prompt or any internal logic
   gate on it? If no, we can de-derive it (currently from Lifecycle
   only, which lies during the Faulted→Stopped transition window).
   Grep says no internal logic gates on it. Confirm.

5. **The activity ring (`agentloop.ActivityRing` written via
   `runActivitySubscriber` on EventBus) — could it replace the
   EventBus entirely?** Activity is the durable side-effect; the
   bus is the in-process fan-out. If the only fan-out consumers are
   the activity writer + the fault subscriber + the
   delivery-confirmation subscriber + the TUI viewport stream, and
   each of those could read from disk-tailing the activity file
   instead, the bus could go away. Big change; merits its own
   audit.

6. **Why does `Real.SendMessage` write both maildir AND
   `agentloop.Enqueue` (`real.go:1242-1257`)?** The maildir is the
   user-visible inbox; agentloop pending is the queue-flush source.
   Could the queue-flush read from the maildir's `new/` directly?
   That would unify the two stores. I suspect there's a sequencing
   reason (the agentloop entry carries a queue-class and a `Body`
   that `BuildQueueFlushPrompt` uses) but didn't confirm.

7. **What's the right action on `IsTerminallyFaulted` during the
   `recoverHealthProbeTimeout` window if a session faults
   immediately after start but the EventBus has no `init` frame
   yet?** Today the probe at `runtime.go:684` re-checks
   IsTerminallyFaulted after subscribing — careful race close. But
   the tick path at line 711 only fires every 50ms. A fault that
   never publishes its `EventBackendFaulted` to the bus (is there
   one?) would only be detected on tick. Worth a deliberate
   construction or a test.

---

## 7. Reflection

**What surprised me.**

- The QUM-606 fix was minimal in code (1 LOC for R1, ~10 LOC for R2)
  but the surrounding design that made the bug possible is sprawling
  — five separate aliveness representations, three notification
  paths, six type-assertion probes. The bug was a symptom of
  cluster 3 (no contract for Done-firing) crossed with cluster 1
  (no contract for which ctx flows down).
- The six mandatory-e2e wall in `CLAUDE.md` is itself an
  architectural artifact. We're using shell harnesses as type
  checkers for our internal instance graph. If the Supervisor
  interface and the cmd/enter wiring made the instance graph
  obvious, four of the six harnesses would be unit tests.
- `WeaveRuntimeHandle` and `unifiedHandle` having **opposite**
  drain contracts in `WakeForDelivery` (one drains pending, one
  defers to a TUI poller) is not documented at the interface — only
  in the comment on `weave_handle.go:96-108`. That's an
  unenforceable invariant.

**Open questions I'd investigate next if I had more time.**

- The EventBus consumer audit (open question 5). Plausible
  -2000 LOC opportunity.
- Whether the `agentloop` "pending" / "delivered" envelope state
  machine duplicates information that's already in the maildir's
  new/ vs cur/ split (open question 6).
- A targeted unit test for "after `setTerminalErr`, can a turn
  loop be observed publishing TurnStarted?" Right now the answer
  is "no because the cancel-runCtx-from-handler chain runs," but
  it's a test of a 5-hop invariant that nothing currently
  guarantees.

**Caveats.**

- I read code, not history. The decisions that produced today's
  shape were made under information I don't have. Several of my
  "this should be simpler" calls may have been considered and
  rejected with context I'm missing. The intent of this doc is
  to surface candidates, not to imply prior contributors got it
  wrong.
- The dollar value of the suggested simplifications is dominated
  by future bug-prevention. None of them is a hot-path performance
  fix. Decide on appetite first; if "land features, don't refactor"
  is the right call this quarter, all six can wait.
- The deliverable specifically excludes implementation phasing,
  per the prompt.

— ghost
