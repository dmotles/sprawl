# QUM-615 — AgentLiveness unification: design spec

Author: ghost (Researcher)
Date: 2026-05-26
Status: **DESIGN SPEC ONLY** — no production code changes in this deliverable.
Tracking: [QUM-615](https://linear.app/qumulo-dmotles/issue/QUM-615) (stays *In Progress* for the implementation phase that follows).
Source of truth: QUM-615 issue body + `docs/research/architecture-simplification-audit-2026-05-20.md` candidate #2 (§3b seam map).

> All file:line citations were re-verified against the worktree at spec time
> (branch `dmotles/qum-615-...`). The audit's line numbers had drifted slightly;
> the numbers here are current. Anything I am <100% sure of is flagged in §5.

---

## LOCKED design decisions (weave/user review, 2026-05-26)

These are settled. The body below (§2–§5) is the supporting rationale; where it
differs from the list here, **this list wins.**

1. **Axis split CONFIRMED.** `AgentLiveness` (liveness axis) is split from a
   separate `LastReportState` (task-outcome axis: `working` / `blocked` /
   `complete` / `failure`). `done`/`problem` are *outcomes*, **not** liveness
   states — exactly as §2.1's note proposed. `AgentLiveness` never carries a
   task outcome.

2. **On-disk schema versioning — folds into M4 (new requirement).** Add a
   `schema_version int` field to `AgentState`.
   - `version 0` / absent = legacy combined `Status`.
   - `version 1` = split (`AgentLiveness` projection + `LastReportState`).
   - **Migrate-on-load** in `LoadAgent`: if `version < 1`, (a) derive
     `LastReportState` from the legacy `done`/`problem` value, (b) derive
     liveness from *actual evidence at load time* — transcript present + clean
     exit ⇒ `Suspended`; otherwise ⇒ `Stopped` — then (c) bump to `version 1`.
     Write-back happens on the next `SaveAgent`.
   - Migration MUST be **idempotent** and unit-testable (round-trip a v0 fixture
     → v1, re-load is a no-op).
   - This is part of the **R1 must-verify gate**: before M4, confirm *every*
     `Status == "done"` / `"problem"` read site, and confirm the resume-eligibility
     filter keys off `Liveness == Suspended`, **not** the outcome string.

3. **R3 is a HARD GATE.** Write the fault→`Done()` chain unit test — "after
   `setTerminalErr`, `Done()` closes within N ms and `Liveness` reaches
   `Faulted`" — **before any production edit in M2.** It is the safety net for
   the entire refactor. No M2 code lands without it green first.

4. **R6 RESOLVED = option (a).** weave/root shares the **single** `AgentLiveness`
   machine. The cross-process states (`Suspended` / `Resuming` / `ResumeFailed`)
   are simply *unreachable* for the root — no second enum, no reduced state set.

5. **Future consumer (NOTE ONLY — out of scope; do NOT add a slice).** Once M5
   lands, the QUM-619 idle-interrupt gate (`Session.Interrupt` gated on a
   `turnRunning` snapshot) *could* read `Liveness` (`Running` vs
   `Running·AutonomousTurn`) instead of its ad-hoc snapshot. Recorded as a
   future opportunity, **not** work in this refactor.

6. **Remaining engineer-verify gates stay baked into their slices:** R2
   (`StatusRunning` dead-weight check) → M4; R4 (transient-state rendering) →
   M1/M3; R5 (UnifiedRuntime↔AgentRuntime lock graph) → M5; R7 (seed all disk
   statuses into `Liveness` at construction) → M4. See §5.

---

## 0. Scope and premise

Today "is this agent alive / usable / faulted?" has **five concurrent
representations**, each owned by a different layer, each with its own writer
set and its own readers. No single value answers "is this agent currently
usable?" (audit §3b). The cross-product is the structural cause of QUM-602
(banner doesn't re-fire), QUM-606 (zombie undetectable), and QUM-611 (modal
desync).

The goal: **one `AgentLiveness` state machine owned by `AgentRuntime`. Every
other layer OBSERVES it; none keeps its own private copy.** The on-disk
`Status` string becomes a *projection* (serialization) of the machine, not an
independent authority.

This spec delivers the five things QUM-615 asks for:

1. Current-state map + observer table (§1).
2. The unified `AgentLiveness` machine — states, transitions, invariants (§2).
3. Migration plan, one observer per slice, with file-overlap serialization
   flags (§3).
4. Validation strategy: the new matrix row name + every transition it drives
   (§4).
5. Open questions / risks ranked by confidence (§5).

### A material discovery up front

**QUM-614 (fold `report_status` into the maildir) has already partly landed.**
`Real.ReportStatus` (`internal/supervisor/real.go:1368-1398`) no longer pushes
to an in-memory `statusNotifier` ring — it calls `messages.SendStatusChange`
and a single `WakeForDelivery`. The audit's S3 ("delete the in-memory ring")
is effectively done. This *helps* QUM-615: the notification surface is already
narrower than the audit assumed. It does **not** remove any of the five
aliveness representations, though — those are still all present.

---

## 1. Current-state map — the five representations

### 1.1 Per-representation: where it lives, who writes, who reads

#### (1) `RuntimeLifecycle` — `internal/supervisor/runtime.go:44-52`

5 values: `registered`, `started`, `stopped`, `killed`, `retired`.
Field: `RuntimeSnapshot.Lifecycle` (`runtime.go:142`). Owned by `AgentRuntime`.

| Writers | Site | Transition written |
|---|---|---|
| `snapshotFromAgentState` (construction from disk) | `runtime.go:201-202, 228-234` | `"" → registered`; disk `killed→killed`, `retired→retired`, else `registered` |
| `startWithSpec` (Start/StartResume) | `runtime.go:322` | `→ started` |
| `AttachHandle` (weave root register) | `runtime.go:383` | `→ started` |
| `Recover` (success swap) | `runtime.go:640` | `→ started` |
| `Recover` (Start fail / health-probe fail) | `runtime.go:613, 632` | `→ stopped` |
| `Stop`/`stopWithFunc` | `runtime.go:798-799` | `started → stopped` |
| `watchHandleExit` (Done() fired) | `runtime.go:854-855` | `started → stopped` |
| `kill` path | `runtime.go:230` | `→ killed` |
| `retire` path | `runtime.go:232` | `→ retired` |
| `SyncAgentState` (disk→snapshot reconcile) | `runtime.go:830-839` | preserves started/stopped, else killed/retired/registered |

Readers: `Real.Status` → `ProcessAlive` (`real.go:410-417`); `startedRuntime`
gate (`real.go:1431`); `Delegate` wake gate (`real.go:461`); `Recover`
precondition (`runtime.go:556, 566`); `RecoverAgents` (`real.go:887`); every
consumer of `RuntimeSnapshot` and the `RuntimeEvent` stream (TUI tree rows).

#### (2) `RuntimeState` — `internal/runtime/unified.go:18-31`

4 values: `idle`, `turn-active`, `interrupting`, `stopped`. Field:
`UnifiedRuntime.state` (`unified.go:91`). Owned by `UnifiedRuntime`.

| Writers | Site | Transition |
|---|---|---|
| `stateTrackingSession.StartTurn` | `unified.go:184-185` | `idle → turn-active` |
| `Interrupt` | `unified.go:404-409+` | `turn-active → interrupting` |
| `Stop`/`StopWithOptions` | `unified.go:346, 386` | `→ stopped` |
| turn-loop channel-close path | (turnloop) | `turn-active → idle` |

Readers: `State()` (`unified.go:395`); internal turn-loop bookkeeping;
`Interrupt`/`Stop` no-op guards (`unified.go:239, 408`). **Not surfaced to the
supervisor or disk** — it is effectively private telemetry.

#### (3) `terminalErr` (sticky) — `internal/backend/session.go:224`

Plus one-shot sibling `fatalErr` (`session.go:219`). Owned by `*session`.

| Writers | Site | Trigger |
|---|---|---|
| `setTerminalErr` (sticky, first-fire arms handler) | `session.go:1009-1023` | `ErrSubscriberWedged` (`session.go:599`), `ErrHangTimeout` (`session.go:674`), `InduceTerminalFault` test path (`session.go:1061`) |
| `setFatalErr` (consumable) | `session.go:992-997` | per-turn transport errors, control-response send failures |

Readers: `IsTerminallyFaulted()` (`session.go:1044`); `LastTurnError`
(`session.go:981-988`); the one-shot `terminalErrHandler`
(`session.go:1019-1023`) installed by `UnifiedRuntime.New` via type assertion
(`unified.go:126-150`) — **this is the only edge that turns a backend fault
into a `Done()` close** (QUM-602/606 R2).

#### (4) `Status` (disk string) — `internal/state/state.go:17-24`

8 declared constants: `active`, `running`, `suspended`, `killed`, `retired`,
`retiring`, `done`, `resume_failed`. Field: `AgentState.Status`. The durable
source every cross-process reader trusts.

| Writers | Site | Value |
|---|---|---|
| spawn | (spawn path) | `active` |
| `RecoverAgents` success | `real.go:828` | `active` |
| `RecoverAgents` resume-eligible filter | `real.go:783` | reads `{suspended,active,running}` |
| `RecoverAgents`/`Recover` OnResumeFailure | `real.go:809`, `runtime.go:542` | `resume_failed` |
| `Shutdown` (suspend, QUM-372) | `real.go:904` | `suspended` |
| retire | (retire path) | `retiring` → `retired` |
| kill | (kill path) | `killed` |
| `agentops.Report` complete | `agentops/report.go:118` | `done` |
| `agentops.Report` failure | `agentops/report.go:120` | **`problem`** |

Readers: `Real.Status` → `AgentInfo.Status` (`real.go:427`); `Delegate` gate
(`real.go:447-450`); `RecoverAgents` eligibility (`real.go:783, 901`);
`snapshotFromAgentState` → seeds Lifecycle (`runtime.go:228`); every `state.LoadAgent` caller; `sprawl status`/`peek` MCP output.

#### (5) `process_alive` (derived) — `internal/supervisor/real.go:407-417`

`*bool` on `AgentInfo`. Computed purely from `Lifecycle`: `started→true`,
`{stopped,killed,retired}→false`, anything else → absent (nil).

Readers: `sprawl status` MCP tool output; operators. **No internal logic gates
on it** (audit OQ#4 — grep confirms; see §5).

### 1.2 The full observer table

Rows = representation. Columns = which layer can *observe* it directly.

| Representation | backend `session` | `UnifiedRuntime` | `AgentRuntime` | `Real` supervisor | disk `state.json` | TUI / MCP `status` |
|---|---|---|---|---|---|---|
| `RuntimeLifecycle` | — | — | **OWNS** | reads (`Status`, gates) | seeded from `Status` | reads via snapshot/events |
| `RuntimeState` | — | **OWNS** | — (not surfaced) | — | — | — |
| `terminalErr` | **OWNS** | reads via handler + `IsTerminallyFaulted` probe | reads via `terminalFaultProbe` (`runtime.go:574-576`) | indirectly (Recover) | — | only via fault banner (EventBus) |
| `currentTurn` (`InAutonomousTurn`) | **OWNS** | — | reads via `autonomousTurnProbe` (`runtime.go:255`) | — | — | — |
| disk `Status` | — | — | reads at construction + Sync | **OWNS** (writes) | **is** the value | reads |
| `process_alive` | — | — | — | **OWNS** (derives from Lifecycle) | — | reads |

**The diagnosis the table makes visible:**

- **No column can observe every row.** `session` owns the fault truth but
  can't see Lifecycle; `AgentRuntime` owns Lifecycle but learns about faults
  only through a single type-asserted handler edge; disk `Status` is authoritative
  for cross-process readers but is updated by a *different* writer set than
  Lifecycle and can disagree with it.
- **`Faulted` has no home.** A terminally-faulted session is `terminalErr != nil`
  (session), but Lifecycle is still `started` until the 5-hop chain
  (`setTerminalErr → handler → cancel runCtx → loopWG → closeDoneOnce → Done() →
  watchHandleExit`) flips it to `stopped`. During that window `process_alive`
  lies (`true`), and `Status` on disk is still `active`. This *is* QUM-606.
- **`stopped` is overloaded.** Lifecycle `stopped` means BOTH "deliberately
  stopped" AND "faulted and torn down" — which forces the `Recover`
  precondition to accept `stopped` as a legal recover source
  (`runtime.go:553-557`), a documented hack.
- **Two writer sets for the "same" fact diverge.** disk `Status="done"` (set by
  `report_status complete`) has no corresponding Lifecycle value — a "done"
  agent is Lifecycle `started` until its handle exits. And `report_status
  failure` writes `Status="problem"`, a value **not even in the declared
  constant set** (§5 R3).

---

## 2. The unified `AgentLiveness` state machine

### 2.1 States

The issue's starting sketch is close. Refinements: split the fault path so
`Faulted` is a first-class resting state (fixes the `stopped` overload); add
`AutonomousTurn` as a *sub-state* of `Running` rather than a peer (it nests);
add `ResumeFailed` as a terminal-ish resting state distinct from `Faulted`;
keep `Suspended`/`Resuming` for the cross-process restart path.

```
                ┌─────────────┐
                │  Unstarted  │   (registered; no handle, no subprocess)
                └──────┬──────┘
                       │ start / resume requested
                       ▼
                ┌─────────────┐
                │  Starting   │   (subprocess spawning; handle being built)
                └──────┬──────┘
            start ok   │           start fail / health-probe fail
        ┌──────────────┼───────────────────────────┐
        ▼              │                            ▼
  ┌───────────┐        │                      ┌───────────┐
  │  Running  │◀───────┘                      │  Faulted  │
  │           │                               └─────┬─────┘
  │  ┌──────────────────┐ │                        │ recover
  │  │ ·AutonomousTurn· │ │  (nested sub-state;     │ requested
  │  └──────────────────┘ │   Running re-entrant)   ▼
  └──┬───────────┬────────┘                   ┌────────────┐
     │           │ backend terminalErr        │ Recovering │
     │ stop       └──────────────────────────▶│            │──┐ ok
     │ requested                              └─────┬──────┘  │
     ▼                                              │ fail    │
  ┌───────────┐                                     ▼         └──▶ Running
  │ Stopping  │                               ┌───────────┐
  └─────┬─────┘                               │  Faulted  │ (re-rest)
        ▼                                     └───────────┘
  ┌───────────┐
  │  Stopped  │
  └───────────┘

  cross-process restart path (sprawl enter):
  Unstarted ──suspend(Shutdown)──▶ Suspended ──RecoverAgents──▶ Resuming
       Resuming ──ok──▶ Running        Resuming ──resume cookie rejected──▶ ResumeFailed

  terminal operator actions (from most live states):
  {Running, Faulted, Stopped, Suspended, ResumeFailed} ──kill──▶ Killed
  {Running, Faulted, Stopped, Suspended, ResumeFailed} ──retire──▶ Retiring ──▶ Retired
```

**State definitions:**

| State | Meaning | Maps from today |
|---|---|---|
| `Unstarted` | Registered in registry; no live handle, no subprocess. | Lifecycle `registered` |
| `Starting` | Handle being built / subprocess spawning. *(new — today there is no observable starting window)* | (transient, currently invisible) |
| `Running` | Live handle, backend healthy, ready for input. | Lifecycle `started` + `RuntimeState idle` + `terminalErr == nil` |
| `Running·AutonomousTurn` | Sub-state: backend is mid SDK-initiated turn. Not a separate top-level state — it's `Running` with `inAutonomousTurn == true`. | `currentTurn.autonomous` / `InAutonomousTurn()` |
| `Faulted` | Backend session terminally faulted; handle may or may not be torn down yet, but the agent is NOT usable. **First-class resting state.** | `terminalErr != nil` (today smeared across `started`→`stopped`) |
| `Recovering` | `mcp__sprawl__recover` in flight: old handle abandoned, new one starting + health-probing. | (today transient inside `Recover`, invisible) |
| `Stopping` | Deliberate stop in flight (polite interrupt + loop drain). | `RuntimeState interrupting`/stop-in-flight |
| `Stopped` | Deliberately stopped; **NOT** faulted. | Lifecycle `stopped` (the *non-fault* half) |
| `Suspended` | Cross-process: process exited cleanly, transcript preserved, eligible for resume. | disk `suspended` |
| `Resuming` | `RecoverAgents` restarting a suspended agent with `--resume`. | (today transient inside `StartResume`) |
| `ResumeFailed` | `--resume` cookie rejected; cannot auto-resume. | disk `resume_failed` |
| `Killed` | Operator-killed; terminal. | Lifecycle/disk `killed` |
| `Retiring` | Retire in flight. | disk `retiring` |
| `Retired` | Retired; terminal. | Lifecycle/disk `retired` |

> **`done`/`problem` are NOT liveness states.** Today disk `Status` conflates
> *liveness* with *task-outcome*: `done` (task complete) and `problem`
> (task failed) are outcomes reported by the agent, orthogonal to whether the
> subprocess is alive. The spec keeps these OUT of `AgentLiveness`. They move
> to a separate `LastReportState` projection (they already partly live in
> `LastReportState`). **This is a deliberate semantic split — see §5 R2.**

### 2.2 Legal transitions (trigger table)

| # | From | To | Trigger | Today's mechanism |
|---|---|---|---|---|
| T1 | Unstarted | Starting | `Start()` / `startWithSpec` | `runtime.go:315` |
| T2 | Starting | Running | `starter.Start` returns + (recover path) health probe passes | `runtime.go:322` |
| T3 | Starting | Faulted | `starter.Start` error OR health-probe fail | `runtime.go:613,632` (today → `stopped`) |
| T4 | Running | Running·AutonomousTurn | backend `system/init` frame, `currentTurn.autonomous=true` | `session.go:573-580` |
| T5 | Running·AutonomousTurn | Running | autonomous turn `result` frame | `session.go:607-608` |
| T6 | Running (or sub) | Faulted | `setTerminalErr` (wedge / hang / induced) | `session.go:1009` → handler → `Done()` |
| T7 | Running | Stopping | `Stop()` requested | `runtime.go:798`, `unified.go:333` |
| T8 | Stopping | Stopped | loop drained | `unified.go:386`, `runtime.go:799` |
| T9 | Faulted | Recovering | `mcp__sprawl__recover` | `runtime.go:608` |
| T10 | Recovering | Running | new handle + health probe pass | `runtime.go:640` |
| T11 | Recovering | Faulted | recover Start fail / health-probe fail | `runtime.go:613,632` |
| T12 | Running/Stopped/Faulted | Suspended | `Shutdown` (sprawl exit, QUM-372) | `real.go:904` |
| T13 | Suspended | Resuming | `RecoverAgents` at startup | `real.go:783`, `runtime.go:349` |
| T14 | Resuming | Running | resume start ok | `real.go:828` |
| T15 | Resuming | ResumeFailed | `--resume` cookie rejected (`OnResumeFailure`) | `real.go:809`, `runtime.go:542` |
| T16 | {Running,Faulted,Stopped,Suspended,ResumeFailed} | Killed | `kill` | `runtime.go:230` |
| T17 | {Running,Faulted,Stopped,Suspended,ResumeFailed} | Retiring | `retire` | retire path |
| T18 | Retiring | Retired | retire complete | `runtime.go:232` |
| T19 | ResumeFailed | Recovering | explicit `recover` (manual retry) | `runtime.go:556` allows it post-change |

### 2.3 Invariants

1. **No `Running` without `Starting`.** Every entry to `Running` passes through
   `Starting` (fresh) or `Recovering`/`Resuming` (restart). There is no edge
   `Unstarted → Running`.
2. **`Faulted` is reachable only from `Running`/`Starting`/`Recovering`** — a
   never-started agent cannot be Faulted (it's `Unstarted`).
3. **`Faulted → Stopped` requires an intermediate.** A faulted agent does NOT
   silently become `Stopped`; it either goes `Recovering` (recover) or is
   explicitly killed/retired. *(This kills the current `stopped`-overload: today
   `watchHandleExit` flips a faulted runtime to `stopped`, erasing the fault.)*
4. **`AutonomousTurn` is a sub-state, never a sink.** It always returns to
   `Running` (T5) or escalates to `Faulted` (T6). It cannot be a direct source
   of `Stopping` without first resolving the turn (Stop forwards a polite
   interrupt first — `unified.go:365`).
5. **Terminal states are absorbing.** `Killed` and `Retired` have no outgoing
   edges. `SyncAgentState` already enforces this for killed/retired
   (`runtime.go:831-832`); the machine makes it total.
6. **`process_alive == (Liveness == Running || Running·AutonomousTurn)`.** No
   other state is "alive." This eliminates the QUM-606 lie window (Faulted is
   no longer reported alive).
7. **disk `Status` is a pure projection of `AgentLiveness`** (plus the
   orthogonal `LastReportState` for done/problem). The serialization must be
   bijective for the resume-eligibility filter to keep working
   (`real.go:783,901`).
8. **Single writer.** Only `AgentRuntime` mutates `AgentLiveness`. `session`
   and `UnifiedRuntime` *emit signals* (terminalErr, turn frames) that
   `AgentRuntime` consumes to drive transitions; they do not hold authoritative
   copies.
9. **Every transition emits exactly one `RuntimeEvent`** on the existing
   per-runtime event stream, so TUI/peek/status observe transitions without
   polling. (Today some transitions emit, some are silent — e.g. `RuntimeState`
   never emits.)

### 2.4 What the sketch in the issue was missing

- **`Starting` and `Recovering` as observable states** (the issue jumps
  `Starting → Running`; but recover has a distinct health-probe window that
  can fail — T11 — and operators need to see "recovering" vs "running").
- **`AutonomousTurn` nesting** (the issue drew it as a peer `Running ↔
  AutonomousTurn`; it is a re-entrant sub-state, and Stop semantics differ when
  a turn is active — invariant 4).
- **The `stopped`-overload fix** — making `Faulted` first-class so
  `Faulted → Stopped` is illegal without an intermediate (invariant 3). This is
  the single most important refinement; it's what closes QUM-606.
- **`done`/`problem` are explicitly NOT liveness** (§2.1 note, §5 R2).
- **`ResumeFailed → Recovering`** retry edge (T19), which the current `Recover`
  precondition already half-allows.

---

## 3. Migration plan — one observer per shippable slice

Principle: introduce `AgentLiveness` as a **derived, read-only projection
first** (computed from today's five fields), prove each observer reads the
projection identically to its private field, *then* flip ownership so the
projection becomes authoritative and the private field is deleted. This lets
every slice ship and be validated independently.

Likely home: `internal/supervisor/liveness/` (new package) for the enum +
transition table + projection helpers, consumed by `AgentRuntime`. Keeping it
in its own package avoids an import cycle with `internal/runtime`.

| Slice | Observer migrated | Files touched | Independently shippable? | Validatable by |
|---|---|---|---|---|
| **M0** | none — introduce `liveness.AgentLiveness` enum + `From(snapshot)` projection + transition validator. Pure additive. | `internal/supervisor/liveness/*.go` (new) | yes | unit tests (table-driven transition legality) |
| **M1** | `process_alive` → `Liveness == Running` | `internal/supervisor/real.go` (`Status`, `:407-417`) | yes | unit test on `Real.Status`; matrix `liveness-transitions` Phase "alive-flips-false-on-fault" |
| **M2** | `Recover` precondition → reads `Liveness` (`Faulted`) instead of `Lifecycle ∈ {started,stopped}` hack | `internal/supervisor/runtime.go` (`:556-580`) | yes (behavior-preserving) | `recover-live` row (existing) + new row Phase "recover-from-faulted" |
| **M3** | TUI tree-row + fault banner → subscribe to `AgentLiveness` transitions | `internal/tui/tree.go`, `internal/tui/app.go` fault handler | yes | `notify-tui` row + new row Phase "banner-refires" (QUM-602) |
| **M4** | disk `Status` becomes projection: write `Status` from `Liveness` serialization; `snapshotFromAgentState` seeds `Liveness` not `Lifecycle`. **Adds `schema_version int` to `AgentState` + migrate-on-load (LOCKED decision #2): v0→v1 derives `LastReportState` from legacy done/problem and liveness from load-time evidence (transcript+clean-exit ⇒ Suspended, else Stopped), idempotent, write-back on next SaveAgent.** | `internal/state/state.go` (`schema_version`, migrate-on-load in `LoadAgent`), `internal/supervisor/runtime.go` (`:201-236`, `SyncAgentState :830-839`), `internal/agentops/report.go` (split done/problem into `LastReportState`) | **NO — must serialize after M1–M3** (it changes the authority direction) | full matrix run; on-disk round-trip test (`state_test.go:394`); **v0→v1 migration idempotence test** |
| **M5** | Fold `RuntimeState` (idle/turn-active/interrupting) into `Liveness` sub-states; delete the private `state` field | `internal/runtime/unified.go` (`:18-47, 91, 184-185, 346-409`) | yes after M4 | `drain-row-inject`, `idle-interrupt-inject` rows + new row turn transitions |
| **M6** | Delete `RuntimeLifecycle` enum once nothing reads it | `internal/supervisor/runtime.go` (all `Lifecycle` sites) | last — depends on M1–M5 | full matrix + `make validate` |

#### File-overlap serialization map (what MUST NOT run in parallel)

- **`internal/supervisor/runtime.go`** is touched by M2, M4, M6 → these three
  must **serialize** (M2 → M4 → M6). M2 is behavior-preserving so it can land
  early; M4 flips authority; M6 deletes.
- **`internal/supervisor/real.go`** is touched by M1 and M4 (`Status`/Sync). M1
  (read projection) before M4 (write projection). Serialize M1 → M4.
- **`internal/runtime/unified.go`** is touched only by M5 → isolated, but M5
  depends on M4 having made `Liveness` authoritative.
- **`internal/state/state.go` + `agentops/report.go`** are M4-only → the
  riskiest slice (on-disk format); gets its own PR and its own soak.
- M1, M3 touch disjoint files (`real.go:Status` vs `tui/*`) → **can proceed in
  parallel** after M0.

**Ordering summary:** `M0` → (`M1` ∥ `M3`) → `M2` → `M4` → `M5` → `M6`.

#### Dependency note (blocked-by)

QUM-615 is blocked-by QUM-613 (composable sub-interfaces), QUM-614
(report_status→maildir — *already largely landed*, see §0), QUM-616 (matrix
harness). **M0–M3 do not strictly need QUM-613**, but the fault/turn probes
(`terminalFaultProbe`, `autonomousTurnProbe`, `runtimeHandleDone`) that
`AgentRuntime` uses to drive transitions are exactly the duck-typed probes
QUM-613 promotes to declared interface methods. Landing QUM-613 first means M2/M5
read declared methods instead of type assertions — cleaner and safer. **Recommend
QUM-613 lands before M2.** QUM-616 (matrix) must land before any slice can be
validated by the row in §4.

---

## 4. Validation strategy — matrix harness row

QUM-616's harness (`scripts/e2e-matrix.sh`) discovers `scripts/e2e-tests/*.sh`
rows; each row file exports `test_metadata()` and `test_run()` and is sourced
in a subshell with `scripts/lib/e2e-common.sh` helpers. A new row is just a new
file — the driver picks it up automatically (`discover_rows`,
`e2e-matrix.sh:20-33`).

### Row name: **`liveness-transitions`**

File: `scripts/e2e-tests/liveness-transitions.sh`.
Metadata: `needs_claude=1 needs_tmux=1 needs_jq=1 needs_build_tags=sprawl_test`
(needs the `mcp__sprawl___test_induce_wedge` tool, same as `recover-live`).
Makefile target (per CLAUDE.md convention): `make test-e2e-matrix-liveness-transitions`.

The row spawns one engineer child and drives every transition class, asserting
on the observable projections (disk `Status`, `process_alive` via `sprawl
status`/peek output, and TUI banner text). Phases map 1:1 onto §2.2:

| Phase | Transitions exercised | Assertion |
|---|---|---|
| P1 spawn → idle | T1, T2 | `Status` becomes `active`; `process_alive=true`; `Liveness=Running` |
| P2 drive a turn | T4, T5 | `Running·AutonomousTurn` observed then back to `Running` (no fault) |
| P3 induce wedge | T6 | `Liveness=Faulted`; **`process_alive` flips to false** (QUM-606 guard); TUI banner fires |
| P4 re-fault while faulted | T6 idempotent | banner **re-fires** (QUM-602 guard); no spurious `Stopped` |
| P5 recover | T9, T10 | `Recovering` observed; new claude `--resume` PID ≠ original alive 2s later (QUM-606 PID guard, lifted from `recover-live`); `Liveness=Running` |
| P6 recover-fail variant | T11 | with a poisoned resume cookie: `Liveness=Faulted` (NOT `Stopped`); banner fires |
| P7 stop | T7, T8 | `Liveness=Stopped`; `process_alive=false`; **distinct from Faulted** |
| P8 suspend/resume (cross-process) | T12, T13, T14 | drive `sprawl` exit + re-enter; `suspended` → `active`; PID changes |
| P9 resume-failed | T15 | poison the resume cookie at restart; `Status=resume_failed`; `Liveness=ResumeFailed` |
| P10 kill + retire | T16, T17, T18 | terminal absorbing; no further transitions emitted |

**Why these assertions catch the target bugs:** P3's `process_alive=false`
assertion is the direct QUM-606 regression guard (the lie window closes). P4's
banner-re-fire is QUM-602. P6/P7 together prove the `Faulted`-vs-`Stopped`
distinction (invariant 3) — the structural fix. P9 proves `ResumeFailed` is its
own resting state.

The existing `recover-live` row's PID-liveness assertion (Phase 4 there) is
**subsumed** by P5 here; during soak, keep both, then retire `recover-live`
once `liveness-transitions` proves flake-free (mirrors the QUM-616 soak
protocol in CLAUDE.md).

---

## 5. Open questions / risks — ranked by confidence

Ranked most-confident-first. Anything below ~95% an engineer must verify
against live wire **before** coding.

**R1 — `done`/`problem` semantic split (DECISION LOCKED #1+#2; remains a must-verify gate before M4).**
disk `Status` today mixes liveness (`active`/`suspended`/...) with task-outcome
(`done`/`problem`). The spec splits them: `AgentLiveness` for liveness,
`LastReportState` for outcome. **Risk:** something may key off `Status=="done"`
as a liveness/eligibility signal. `RecoverAgents` already excludes `done`
from resume (`real.go:901`), so resume treats `done` as terminal-ish. If we move
`done` out of `Status`, the resume filter must instead check
`Liveness ∈ {Suspended}` AND not-done-via-LastReportState. **Engineer must
verify** every `Status == "done"` / `"problem"` read site before M4. `problem`
is not even a declared constant (`state.go` lacks it; `agentops/report.go:120`
writes the raw string) — that's a latent bug the split would surface.

**R2 — on-disk `Status` serialization must stay bijective (confidence ~85%).**
`state_test.go:394-427` pins the 8 status constants and round-trips them.
M4 changes who writes `Status` but must keep deserializing every historical
value, including agents persisted before this change (`suspended`, `active`,
`running`). Note `active` vs `running` are *both* present today and both map to
"resume-eligible" (`real.go:783`) — do they mean different things, or is
`running` legacy dead weight? **Engineer must grep for `StatusRunning` writers**;
if none, it's a free deletion; if some, the projection needs both.

**R3 — the 5-hop fault→Done() chain is the load-bearing edge (confidence ~90%
it works today; ~50% it survives refactor untested). DECISION LOCKED #3 — HARD GATE: the chain test below MUST be green before any M2 production edit.** The chain
`setTerminalErr → terminalErrHandler → cancel runCtx → loopWG.Wait →
closeDoneOnce → Done() → watchHandleExit → Lifecycle` (`session.go:1009`,
`unified.go:129-150,297-300`, `runtime.go:846-863`) has **no integration test
asserting the shape is preserved**. Audit OQ flagged this. The migration MUST
add a unit test ("after `setTerminalErr`, `Done()` closes within N ms and
Liveness reaches `Faulted`") **before** M2 touches the path. **This is the
highest-execution-risk item** — get the fault edge under test first.

**R4 — `Starting`/`Recovering`/`Stopping` are today invisible transients
(confidence ~80%).** Making them observable means the TUI/status may briefly
show states it never showed before. Need to confirm no consumer treats
"not exactly `started`/`stopped`" as an error. `Real.Status` switch
(`real.go:410-417`) leaves `process_alive` *nil* for `registered` and would do
the same for new transients — confirm nil `process_alive` renders sanely in the
TUI and `sprawl status` (it's a `*bool`, so "unknown" is representable).

**R5 — `RuntimeState` (idle/turn-active/interrupting) deletion in M5
(confidence ~75%).** It's currently private to `UnifiedRuntime` and drives the
`Interrupt`/`Stop` no-op guards (`unified.go:239,408`). Folding it into
`Liveness` sub-states means those guards must read the new machine across a
package boundary (supervisor→runtime). Verify there's no lock-ordering hazard:
`UnifiedRuntime.mu` vs `AgentRuntime.mu` are taken in different goroutines
(turn loop vs supervisor). **Engineer must map the lock graph** before M5; a
naive "ask AgentRuntime for liveness from inside the turn loop" could deadlock.

**R6 — `AttachHandle` / weave root runtime (DECISION LOCKED #4 — RESOLVED, option (a)).**
weave (root) goes through `AttachHandle` (`runtime.go:377`), not `startWithSpec`,
and `WeaveRuntimeHandle` has a divergent `WakeForDelivery` contract (audit §3c).
**Resolution: root shares the single `AgentLiveness` machine.** The cross-process
states (`Suspended`/`Resuming`/`ResumeFailed`) are simply *unreachable* for root
— no second enum, no reduced state set. The implementer should assert (in test)
that root never reaches those states rather than modelling a separate machine.

**R7 — `Killed`/`Retired` seeded from disk at construction (confidence ~90%).**
`snapshotFromAgentState` (`runtime.go:228-234`) seeds Lifecycle from disk
`Status` for killed/retired only. The new machine must seed `Liveness` for ALL
disk statuses (including `suspended`→`Suspended`, `resume_failed`→`ResumeFailed`)
at construction, else a restart loses the resting state. Low risk but easy to
miss — call it out in M4.

---

## 6. Reflection

**What surprised me.**

- **QUM-614 already landed the hard part of the notification surface.**
  `Real.ReportStatus` no longer uses the in-memory `statusNotifier` ring — it's
  `messages.SendStatusChange` + one `WakeForDelivery` (`real.go:1368-1398`).
  The audit (5 days old) described the ring as still present. The blocked-by on
  QUM-614 is effectively satisfied for our purposes.
- **`problem` is an undeclared status.** `agentops/report.go:120` writes
  `Status="problem"` but `state.go` has no `StatusProblem` constant and
  `state_test.go`'s exhaustive constant pin doesn't include it. It's a string
  that leaks through. The liveness split would force this into the open — likely
  a small latent-bug fix riding along.
- **The `stopped` overload is even worse than the audit framed it.** It's not
  just "faulted or deliberately stopped" — `Recover` *depends* on the overload
  (`runtime.go:553-557` explicitly accepts `stopped` as a recover source
  precisely because a fault masquerades as stopped). Fixing the overload
  (first-class `Faulted`) is therefore a *behavior change to recover
  preconditions*, not a pure rename. M2 must be careful.

**Open questions I'd investigate next with more time.**

- The lock graph between `UnifiedRuntime.mu` and `AgentRuntime.mu` (R5) — I'd
  trace it before committing to M5's ownership flip. It's the deadlock risk.
- Whether `StatusRunning` (`running`) has any live writer at all, or is dead
  weight alongside `active` (R2). A 20-minute grep would de-risk M4.
- A live-wire test of the 5-hop fault→Done() chain (R3) — I'd write that unit
  test *first*, before any production edit, as the safety net for the whole
  refactor.

**Caveats.** I read code, not runtime behavior — the transition timings
(health-probe window, loopWG drain) are inferred from structure, not measured.
The state machine is a *spec*; the implementer should treat §5 R1/R3/R5 as
must-verify gates, not settled facts.

— ghost
