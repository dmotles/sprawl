# Unified Runtime Messaging Audit (2026-05-05)

**Scope.** Audit message delivery, notification, and TUI visibility under
`SPRAWL_UNIFIED_RUNTIME=1` (the QUM-399 path) for both weave and child
agents. Identify what is broken, what is already filed in Linear, and what
gaps need new issues.

**Method.** Pure source-reading + Linear inspection. No sandboxes, no e2e
tests (rate-limit conservation; finn engineer is using sandbox infra for
QUM-465 in parallel).

**Author.** ghost (researcher, 2026-05-05).

---

## A. Delivery surface map

### A.1 Common foundation (both weave and children)

```
sender (any process)
  └─ messages.Send(root, from, to, subject, body)            internal/messages/messages.go:149
       ├─ writes <root>/.sprawl/messages/<to>/new/<id>.json (atomic rename)
       ├─ writes <root>/.sprawl/messages/<from>/sent/<id>.json (best-effort)
       ├─ if RecipientResolver(to) != Unified: writes .sprawl/agents/<to>.wake
       └─ DefaultNotifier(to, from, subject, msgID)          (best-effort, recover()-wrapped)

supervisor.SendAsync(ctx, to, subject, body, ...)            internal/supervisor/real.go:634
  ├─ messages.Send (above) — with WithoutWakeFile when target is started runtime
  ├─ agentloop.Enqueue → writes .sprawl/agents/<to>/queue/pending/<seq>-<class>-<id>.json
  └─ if startedRuntime(to): runtime.InterruptDelivery()
```

`messages.SetDefaultNotifier` is registered exactly once at TUI startup
(`cmd/enter.go:693`) with a closure that:

1. Filters to `to == "weave"` (root) only — child→child traffic never fires
   `InboxArrivalMsg`. (This is intentional per QUM-311.)
2. Calls `tea.Program.Send(InboxArrivalMsg{...})` — fire-and-forget.

`SetRecipientResolver` is registered at the same point and reads the
supervisor's `RuntimeRegistry` to decide whether to skip the legacy `.wake`
sentinel for unified-runtime recipients (QUM-438).

### A.2 Weave (root) — unified path (`SPRAWL_UNIFIED_RUNTIME=1`)

Construction path: `cmd/enter.go:newSessionImplUnified` (line 403).

```
backendclaude.Adapter.Start  → backend.Session
  Session.Initialize(initSpec) — registers MCP tools BEFORE runtime starts

runtime.New(RuntimeConfig{
    Name:"weave", Session:session, IsRoot:true,
    OnQueueItemDelivered: func(it){ for _, id := range it.EntryIDs {
        agentloop.MarkDelivered(root, "weave", id)   // pending/ → delivered/
    }},
})

supervisor.NewWeaveRuntimeHandle(rt, session, root, "weave")
  ├─ opens .sprawl/agents/weave/activity.ndjson append fd
  ├─ ActivityRing + ObserverWriter
  └─ runActivitySubscriber(rt.EventBus(), observer)   // forwards EventProtocolMessage to ring

supervisor.RegisterRootRuntime("weave", handle, agentState)
  └─ runtimeRegistry["weave"].AttachHandle(handle); Lifecycle=Started
```

Inbox arrival (in-process, e.g. child→weave via supervisor.SendAsync):

```
SendAsync("weave", ...)
  ├─ messages.Send(...)
  │    ├─ writes maildir/new/
  │    ├─ skips .wake (RecipientResolver returns Unified)
  │    └─ DefaultNotifier → InboxArrivalMsg → tea.Program.Send
  │         → AppModel.Update(InboxArrivalMsg): rootVP.AppendStatus("inbox: new message from X")
  │           + m.rootUnread++ + rebuildTree
  ├─ agentloop.Enqueue (writes pending/)
  └─ runtime.InterruptDelivery (= AgentRuntime.InterruptDelivery → handle.InterruptDelivery())
       → WeaveRuntimeHandle.InterruptDelivery():
            • ListPending → SplitByClass(interrupts, asyncs)
            • rt.Queue().Enqueue(ClassInterrupt|ClassInbox, EntryIDs)
            • rt.InterruptDelivery(ctx) → rt.queue.Wake() (and rt.Interrupt if mid-turn)
       → TurnLoop drains queue → executeTurn(prompt) → Session.StartTurn →
          claude subprocess sees the prompt
       → Protocol stream events arrive on rt.EventBus
       → TUIAdapter (subscribed) translates to tea.Msg, AppModel renders assistant
         response in viewport
```

Backstop (out-of-process delivery, e.g. `sprawl messages send` from a
shell):

```
tickAgentsCmd (every 2s)
  ├─ sup.Status() → child agent infos
  ├─ for each: messages.List(root, name, "unread")  — unread maildir count
  ├─ messages.List(root, "weave", "unread")          — weave's unread count
  └─ AgentTreeMsg{Nodes, RootUnread}
        → AppModel.Update(AgentTreeMsg):
             • RootUnread > m.rootUnread ⇒ AppendStatus("inbox: N new message(s) for weave") (rise detection)
             • if turnState==Idle && bridge!=nil: peekAndDrainCmd
                 → ListPending(root, "weave")
                 → InboxDrainMsg{Prompt, EntryIDs, Class}
                       → AppModel.Update(InboxDrainMsg):
                           • AppendSystemMessage(Prompt)  ← user-visible inbox banner+body
                           • bridge.SendMessage(Prompt)   ← legacy: stdin; unified: TUIAdapter.SendMessage
                                                             enqueues ClassUser into rt.queue
                           • UserMessageSentMsg → commitDrainCmd → MarkDelivered
```

### A.3 Weave — legacy path (no unified gate)

`cmd/enter.go:newSessionImpl` (line 493):

```
tui.NewBridge(ctx, &enterBridgeSession{session, initSpec})
  └─ Bridge.Initialize → starts streaming session manually; SendMessage writes stdin directly
```

Notifier path (in-process): identical (InboxArrivalMsg via SetDefaultNotifier).

Inbox prompt path: there is **no** `WeaveRuntimeHandle` in legacy mode.
`runtimeRegistry["weave"]` is empty → `r.startedRuntime("weave") → nil` →
`runtime.InterruptDelivery()` not called from `SendAsync`. Wake comes via:
- the `.wake` sentinel file (because RecipientResolver returns Unknown when
  no started runtime exists) — but legacy weave has no in-process loop
  scanning that file; the sentinel is harmless dead matter for weave.
- the `peekAndDrainCmd` backstop (every 2s tick) is the actual drain path.

### A.4 Children — unified path

`internal/supervisor/runtime_launcher_unified.go`. Per-child runtime
construction is identical to weave's, but goes through `RuntimeStarter` (not
external attach).

Wake/interrupt: `unifiedHandle.InterruptDelivery` mirrors
`WeaveRuntimeHandle.InterruptDelivery` (ListPending → Enqueue runtime
items → rt.InterruptDelivery).

`AgentRuntime.InterruptDelivery` → `unifiedHandle.InterruptDelivery` is
invoked by `r.SendAsync` / `r.SendInterrupt` / `r.ReportStatus` (when the
recipient or parent is a started runtime).

### A.5 Children — legacy path

`internal/supervisor/runtime_launcher.go` `inProcessRuntimeStarter` →
`agentloop.StartRunner` → `agentloop.Runner.Run` (single big function
spanning ~600 lines). Wake mechanism = control-channel (`ControlSignalWake`
/ `ControlSignalInterrupt`) **and** disk wake-file (`<name>.wake`) **and**
maildir polling. The runner repeatedly polls between turns:

1. Pending queue (interrupt then async)
2. Unread maildir messages (separate code path that synthesizes a
   "Run `sprawl messages read X`" prompt, distinct from the
   `BuildQueueFlushPrompt` rendering used elsewhere)
3. `.wake` sentinel file content (used as a verbatim prompt!)
4. `waitForWork(deps, 3*time.Second)` — sleep, repeat

The legacy runner has FOUR different ways to take in a notification (queue,
maildir, control channel, wake file). Three of them coexist with the
unified runtime when `SPRAWL_UNIFIED_RUNTIME=0`. See §D / hygiene.

---

## B. Symptom → issue catalog

### B.1 "Some notifications duplicate"

**Existing**: QUM-465 (Bug, In Progress, finn engineer fixing).

**Coverage check**: QUM-465's framing — two notifier paths (in-process
`InboxArrivalMsg` + cross-process `tickAgentsCmd` rise detector) — covers
the duplication of the **viewport banner**. It does NOT explicitly mention
a second mechanism that may be at play: the **prompt-injection** can also
double-fire because:

- `WeaveRuntimeHandle.InterruptDelivery` enqueues `ClassInbox`/`ClassInterrupt`
  items derived from `agentloop.ListPending`, **and**
- `peekAndDrainCmd` (kicked from every `AgentTreeMsg` while idle) reads
  the same pending/ entries and turns them into `InboxDrainMsg` → bridge
  → `TUIAdapter.SendMessage` → `ClassUser` queue item.

If the runtime's own enqueue lands first, `OnQueueItemDelivered` runs
`MarkDelivered` after `Session.StartTurn` returns. There is a window
between `WeaveRuntimeHandle.InterruptDelivery` enqueueing pending entries
and `MarkDelivered` actually moving the file out of `pending/`. If
`tickAgentsCmd` fires inside that window AND `m.turnState == TurnIdle`
(the turn loop hasn't started yet), `peekAndDrainCmd` returns a
non-empty `InboxDrainMsg` and a second prompt gets queued.

In that case, both queue items have the same prompt content, and Claude
gets the inbox payload twice. **This is a deeper version of QUM-465 and
would not be fully resolved by deduping the banner alone.**

**Action**: Posted comment on QUM-465 with this corrected/expanded
analysis. Also pointed at the WeaveRuntimeHandle.InterruptDelivery enqueue
path, which is the second-channel root cause.

### B.2 "Some notifications hit-or-miss"

**Multiple suspect paths**:

1. **`tea.Program.Send` is fire-and-forget.** The `buildTUIRootNotifier`
   closure captures `send func(tea.Msg)` from
   `cmd/enter.go:runProgram` which is `func(msg tea.Msg) { p.Send(msg) }`.
   After `p.Quit()` (or a Ctrl-C / SIGTERM), `Send` becomes a no-op or
   blocks on a closed channel (bubbletea handles internally; it does not
   error back). Loss is silent. The notifier is unset by
   `messages.SetDefaultNotifier(nil)` only AFTER `runProgram` returns —
   between Quit and that line, in-process `messages.Send` calls (e.g. from
   shutdown-time MCP traffic) lose their `InboxArrivalMsg`.
   **Severity: Low.** Cosmetic at shutdown; filed as a follow-up note in
   QUM-465 since the same closure is implicated.

2. **EventBus non-blocking publish.**
   `internal/runtime/eventbus.go:Publish` uses a non-blocking `select` and
   silently drops events for any subscriber whose buffer is full. The
   TUIAdapter subscribes with buffer=64 (`adapterEventBufferSize`). If a
   bursty turn produces >64 protocol messages while the AppModel render
   goroutine is occupied (long re-render, paste, modal open), events are
   dropped on the floor — and there is no logging or telemetry pointing
   at the loss. Activity-ring subscriber subscribes with buffer=64 too
   (`runtime_launcher_unified.go:165`), so the same event can be dropped
   in both subscribers independently. **Severity: Medium — silent data
   loss path.** New issue filed.

3. **Restart window** (re QUM-467 fixed bridge-lifecycle, but the
   handoff path still has races).
   `cmd/enter.go:makeRestartFunc` stops weave's runtime and removes it
   from the registry **before** `finalize` runs. Any
   `messages.Send` to weave during this window:
   - skips `.wake` only if the **resolver still says Unified** for weave
     (it does — the registry entry is removed only in the explicit
     `reg.Remove` path; if the resolver is consulted right before that
     line, it still sees Started). Otherwise the `.wake` file is written.
   - calls `runtime.InterruptDelivery` only if `startedRuntime("weave")`
     returns non-nil; after `reg.Remove`, it doesn't.
   - The `agentloop.Enqueue` succeeds (it's pure disk).
   The new runtime, on registration, does NOT replay pending entries
   eagerly — it relies on the next 2s tick → `peekAndDrainCmd` to discover
   them. Between Remove and the next tick, weave appears unresponsive
   even though the user can see the unread badge update. **Severity:
   Medium.** Existing partial coverage in QUM-329 (handoff plumbing)
   but specifically the post-restart "prime the queue" gap is not
   filed. Will fold into the new issue list.

### B.3 "TUI viewport vs weave-claude divergence"

**This is the most surprising finding.** Under
`SPRAWL_UNIFIED_RUNTIME=1`, when an in-process `send_async` reaches weave:

- `WeaveRuntimeHandle.InterruptDelivery` enqueues a `ClassInbox`
  `runtime.QueueItem` with the formatted inbox prompt as `Prompt`.
- The TurnLoop pulls it, calls `Session.StartTurn(ctx, prompt)`, and
  publishes `EventTurnStarted{Prompt: prompt}` on the EventBus.
- **`TUIAdapter.WaitForEvent` explicitly skips
  `EventTurnStarted`** (`tuiruntime/tuiadapter.go:153`). Comment: "Skip
  lifecycle-only events — read the next one."
- Therefore, the AppModel never sees the user-side of that turn.
- The viewport renders only the assistant's response — but not the
  `[inbox] You received N message(s) since the last turn:` block that
  prompted it.

Compare with the `peekAndDrainCmd → InboxDrainMsg` path, which
`AppendSystemMessage(msg.Prompt)` first (so the user sees what was
delivered), then `bridge.SendMessage(prompt)`. Under unified runtime, the
runtime-driven path never flows through `InboxDrainMsg`.

So under unified runtime, **inbox prompts injected into weave's claude
are silently invisible in the TUI viewport unless the 2s `tickAgentsCmd`
backstop happens to win the race**. The user sees weave reacting (the
assistant message scrolls in) without any visible cause — exactly the
"weave reacting to notifications I can't see in the viewport" symptom.

The mismatch is amplified by the dual-path race in §B.1 — sometimes the
peekAndDrainCmd does win and the user sees the prompt; sometimes the
runtime-internal drain wins and the user does not. **Severity: High —
silent invisible prompt injection. New issue filed.**

### B.4 Wake-loss patterns (Bash run_in_background + ScheduleWakeup)

**Existing**: QUM-470 covers child agents (`claude --print` mode) where
`ScheduleWakeup` etc. silently no-op.

**Weave**: Weave runs as an interactive session (NOT `--print`), so
`ScheduleWakeup`/`Monitor`/`Cron*` work normally for weave. Not
affected by QUM-470.

**Action**: No new issue. Confirmed weave is unaffected.

### B.5 Stream-closed errors during weave-claude restarts

**Existing**: QUM-467 (FIXED, shipped — bridge-lifecycle hoisted to
supervisor scope, set-once semantics).

**Verification**: `cmd/enter.go:supervisorMCPBridge` (line 340) reuses the
supervisor's bridge if available, falls back to a fresh one. The fallback
path constructs a fresh `host.NewMCPBridge() + sprawlmcp.New(sup)` — but
this only fires when `sup.MCPBridge()` returns nil OR `sup` is a test
double. In production runtime (`*supervisor.Real`), the accessor returns
the registered bridge. **The fallback at line 349-355 is not the
QUM-467 regression site** — it's a defensive double for non-Real
supervisor implementations.

**One residual concern**: nothing prevents a future caller from
calling `supervisorMCPBridge` against a `*supervisor.Real` whose
internal bridge is nil (e.g. construction error path, future refactor).
Worth a small assertion or unit test. Folded into hygiene umbrella.

### B.6 Child→peer messaging visibility in weave's TUI viewport

When child A → child B via `send_async`:

- B's maildir + harness queue + runtime InterruptDelivery fire normally.
- The TUI's `DefaultNotifier` filter (`if to != rootName { return }`)
  drops the notification for B.
- `tickAgentsCmd` polls each agent's unread maildir count → tree shows the
  `(N)` badge on B's row.
- Weave's viewport shows nothing. The user only sees the badge.

**Question (from initial prompt)**: should weave's TUI surface
child→peer messages?

**My read**: Surfacing every child→peer send in weave's viewport would
be too noisy in larger trees. But there's an argument for a one-line
status banner at the top of the viewport ("trace → finn: hello") to
keep the user informed without being overwhelming. **Filed as an
Improvement issue with two design options to pick from.**

### B.7 Notification *drops* — design of a deterministic test

Per the initial prompt, "design a small deterministic test". I'll skip
the implementation (read-only constraint) and capture the design here:

```
Test setup: weave under SPRAWL_UNIFIED_RUNTIME=1 in TUI mode.
Steps:
  1. Pause AppModel update goroutine artificially (e.g. via a
     long-running modal Update call) for >> 64 events.
  2. Have a child rapidly emit protocol messages (or simulate via
     direct EventBus.Publish) until adapter buffer overflows.
  3. Observe: if EventBus is dropping silently, no ts arrival in
     the AppModel, no log entry, no error indicator.
Repro path:
  - Easier real-world repro: fast bash output (large stdout) during
    a tool_result message, while user has the help overlay (F1)
    open or is mid-paste.
```

Conclusion: yes, `tickAgentsCmd` rise-detection itself does NOT miss a
maildir entry — `messages.List(unread)` is monotonic between deliveries
(messages only move out of `new/` when explicitly `MarkRead`'d, which
happens only via `ReadMessage`). The "drops" the user sees are upstream:
EventBus drops + p.Send shutdown window (B.2 items 1 & 2) + race
windows in the dual drain pipeline (B.1).

---

## C. Differences between unified and legacy paths

**Weave**:

| Concern | Unified (QUM-399) | Legacy |
|---|---|---|
| Bridge construction | `tui.NewBridgeFromDelegate(TUIAdapter)` | `tui.NewBridge(ctx, enterBridgeSession)` |
| Wake mechanism | `WeaveRuntimeHandle.InterruptDelivery` → in-memory queue | None (no in-process loop) |
| Inbox drain path | Two paths: runtime-internal + `peekAndDrainCmd` (race) | Single path: `peekAndDrainCmd` |
| Inbox prompt visible in viewport? | Only when `peekAndDrainCmd` wins the race | Always (single path) |
| Activity ring | EventBus subscriber | None on weave |

**Children**:

| Concern | Unified | Legacy |
|---|---|---|
| Wake mechanism | `unifiedHandle.InterruptDelivery` (in-mem queue + Wake) | Control channel + `.wake` file + maildir poll |
| Inbox prompt builder | `inboxprompt.BuildQueueFlushPrompt` | Same (after QUM-437 unified them) |
| `.wake` file written? | No (RecipientResolver returns Unified) | Yes |
| Prompt rendering visible to TUI when child observed? | EventTurnStarted **dropped** by ChildStreamAdapter (mirrors TUIAdapter) | N/A — children can't be observed live in legacy mode |
| Coexistence of legacy fallback paths in source | Yes (unified runtime starter) | No — same as itself |

---

## D. Recommendations (issues to file / amend)

Filed (this audit, see §E for IDs):

1. **[Bug/High]** Unified-runtime weave: inbox prompts injected via
   `WeaveRuntimeHandle.InterruptDelivery` are invisible in the TUI
   viewport because `TUIAdapter.WaitForEvent` discards
   `EventTurnStarted`. Mirror the `InboxDrainMsg`'s
   `AppendSystemMessage(prompt)` semantics for the runtime-driven path.
   See §B.3.

2. **[Bug/Medium]** EventBus drops events silently when subscriber buffer
   fills. Add overflow counter / log warning / consider per-subscriber
   ring with documented loss semantics. Most user-visible impact: TUI
   viewport drops content during paste-burst + fast-tool sessions.
   See §B.2 item 2.

3. **[Improvement/Medium]** Code hygiene umbrella — partial migrations,
   jank, refactor leverage. See §F. Deliberately consolidated as one
   issue per dmotles' "5 highest-impact + summarize" guidance.

4. **[Improvement/Low]** Surface child→peer messages in weave's TUI
   viewport (status banner) so user has visibility into inter-child
   communication. See §B.6.

Comments on existing issues:

- **QUM-465**: Posted a comment expanding root-cause analysis to include
  the prompt-injection race (not just banner duplication). The framing
  in QUM-465 is correct as far as it goes but only covers half the
  duplication surface. See §B.1.

Not filed (out of scope or already covered):

- QUM-462 / -465 / -467 / -470 — already filed and either fixed or
  in-progress.
- Weave-affected ScheduleWakeup wake-loss — does not exist (weave runs
  interactively, not in `--print` mode).
- Resume-failure / handoff bridge-lifecycle — covered by QUM-261 /
  QUM-329 / QUM-467.
- QUM-400 (Phase 4 cleanup) — already filed; my §F findings are inputs
  to that issue, not separate ones.

---

## E. New issues filed

- **QUM-471** [Bug/High] — Unified-runtime weave: inbox prompts injected into Claude are invisible in TUI viewport.
- **QUM-472** [Bug/Medium] — EventBus silently drops events on full subscriber buffer (no telemetry, no warn).
- **QUM-473** [Improvement/Medium] — Code hygiene umbrella: unified-runtime messaging cleanup, naming, and docs.
- **QUM-474** [Improvement/Low] — TUI: surface child→peer messages in weave's viewport (status banner only).

Comments added:

- **QUM-465** — root-cause expansion: dual-drain race + cross-link to QUM-471. Recommended single fix that closes QUM-465 + QUM-471 + the dual-drain race (collapse `WeaveRuntimeHandle.InterruptDelivery` and `unifiedHandle.InterruptDelivery` to just `rt.Queue().Wake()`, let `peekAndDrainCmd` be the sole drain pipeline).

---

## F. Code hygiene findings

This section is the per-prompt scope expansion (jank / partial
migrations / refactor opportunities). Filed as a single umbrella issue
per dmotles' guidance.

### F.1 Partial migrations — UnifiedRuntime cleanup (covered by QUM-400)

**Live coexistence**:

- `agentloop.Runner` (legacy 940-line runner) and `runtime.UnifiedRuntime`
  both exist; gated by `SPRAWL_UNIFIED_RUNTIME=1` env var.
- `tui.Bridge` (legacy) and `tuiruntime.TUIAdapter` (unified) both
  exist; selected by `defaultUnifiedRootEnabled()` in
  `cmd/enter.go:369`.
- `internal/supervisor/runtime_launcher.go`'s `newRuntimeStarter` selects
  legacy vs unified per `unifiedRuntimeEnabled()` (separate var, same
  env check).
- `inProcessRuntimeStarter` and `inProcessUnifiedStarter` coexist in
  `internal/supervisor/`.

**Status**: tracked by QUM-400. Not duplicated.

**Note for QUM-400 implementer**: the legacy runner has wake-handling
spaghetti — `.wake` file detection (line ~912), control channel (line
~256), maildir polling (line ~873), pending queue drain (line ~808).
Four mechanisms for the same concern. When QUM-400 deletes the runner,
all four go.

### F.2 SPRAWL_MESSAGING=legacy gate — already gone

The `SPRAWL_MESSAGING=legacy` env-gated tmux send-keys path is
**gone** from the source tree (verified via grep in `cmd/` and
`internal/`). Only mentioned in `docs/research/*.md` as historical
context. **No action needed.**

### F.3 tmux mode death residuals

`sprawl init` was deleted in QUM-346. Residuals checked:

- `cmd/handoff.go` is still present but `CLAUDE.md` notes it as a
  deprecated tmux-mode fallback. Emits stderr warning. Tracked elsewhere
  (QUM-337).
- `_stmux` / `SPRAWL_TMUX_SOCKET` / `SPRAWL_NAMESPACE` machinery — these
  are still legitimately used by the **sandbox** test infra
  (per CLAUDE.md "tmux safety" note), not by production. Not jank.

**No action needed.**

### F.4 supervisorMCPBridge fallback — clean enough

`cmd/enter.go:supervisorMCPBridge` (340-356) reuses
`sup.MCPBridge()` if available, otherwise builds a fresh bridge.
The fallback path only fires for non-Real supervisor implementations
(test doubles), so QUM-467's "third-site" risk is not present in
production. **No action needed beyond perhaps a unit test asserting
production never hits the fallback.**

### F.5 Confusing code / refactor leverage

Items I had to read 2+ times to understand:

1. **`UnifiedRuntime.InterruptDelivery` vs `Interrupt` vs
   `InterruptDelivery` (handle vs runtime)**. Three distinct concepts:
   - `UnifiedRuntime.Interrupt` — preempt the in-flight turn
   - `UnifiedRuntime.InterruptDelivery` — wake the queue signal so
     an idle loop re-checks, plus optionally interrupt if mid-turn
   - `RuntimeHandle.InterruptDelivery` (`unifiedHandle` /
     `WeaveRuntimeHandle`) — read pending/, enqueue to runtime queue,
     then call `UnifiedRuntime.InterruptDelivery`
   The semantic distance between layers (handle vs runtime) is real,
   but the shared name "InterruptDelivery" obscures it. Consider
   renaming the handle method to `DeliverPending` or
   `DrainPendingAndWake` to make the flow obvious.

2. **`MessageQueue.Wake` vs `Enqueue`'s implicit wake**. `Enqueue`
   already pokes the signal channel (queue.go:102); `Wake` exists for
   the case where you want to wake the loop without enqueueing. The
   distinction is correct but easy to miss; called out only in a one-line
   comment. **Doc improvement, not behavior change.**

3. **Two distinct "drain" pipelines for the same data** (§B.1). The
   `WeaveRuntimeHandle.InterruptDelivery` enqueue path and
   `peekAndDrainCmd` both read `agentloop.ListPending`. The design
   comment in `app.go:1066` explains the unified-runtime intent
   ("InboxDrainMsg → bridge.SendMessage either streams the drained
   prompt directly to claude (legacy bridge) or enqueues a ClassUser
   item via the TUIAdapter (unified bridge)") but doesn't acknowledge
   that the runtime-handle path **also** enqueues independently. This
   is the QUM-465 root cause, not just framing — there is real
   duplication.

4. **`agentloop.Class` vs `runtime.Class*` constants**. Two parallel
   string namespaces:
   - `agentloop` (= `inboxprompt`): `ClassAsync`, `ClassInterrupt`
   - `runtime`: `ClassInterrupt`, `ClassTask`, `ClassUser`,
     `ClassAsync`, `ClassInbox`
   `agentloop.ClassAsync` becomes `runtime.ClassInbox` when ferried into
   the runtime queue (see `WeaveRuntimeHandle.InterruptDelivery`
   line 117). This rename is silent and easy to miss when reading.
   **Either unify under one type or document the mapping prominently.**

5. **`AgentTreeMsg.RootUnread` rise detection in
   `app.go:1047-1050`** is a separate, parallel banner-emitter from the
   `InboxArrivalMsg` handler at line 1086. Both append banners
   ("inbox: %d new message(s) for weave" vs "inbox: new message from
   X"). They cover different origin channels but the user-facing UX
   is two distinct banner formats for the same conceptual event.
   Consider unifying the banner string format.

6. **Fan-out subscribers all use buffer=64**, but the consumer cadence
   is wildly different between TUI rendering (~16ms) and activity ring
   (immediate write). Sizing should be consumer-specific or
   documented as a deliberate ceiling.

### F.6 Refactor opportunities

Top three high-leverage refactors:

1. **Unify the inbox-prompt rendering pipeline.** Decide once: should
   the `WeaveRuntimeHandle.InterruptDelivery` enqueue inbox prompts
   directly, OR should it just `Wake()` and rely entirely on
   `peekAndDrainCmd` to drive delivery? Either way, single-path is
   simpler and resolves §B.1, §B.3, and the mental burden of §F.5
   item 3. Recommendation: **keep peekAndDrainCmd as the only drain
   path**, change `WeaveRuntimeHandle.InterruptDelivery` to just
   `rt.Queue().Wake()`. The runtime then emits `EventTurnStarted` only
   when the TUI explicitly enqueues `ClassUser` via `bridge.SendMessage`
   — which the AppModel can render via `AppendSystemMessage` first as
   today. (Note this is the "OR collapse to a single notifier" option
   listed in QUM-465's body.)

2. **Replace 940-line `agentloop.Runner.Run` with the unified runtime
   for children** (= QUM-400). Already filed. Drag-along: 1100-line
   `runner_test.go`, four wake mechanisms collapse to one,
   `runtime_launcher.go` halves in size.

3. **Add EventBus overflow telemetry** (counter + warn-once log) so
   silent drops become visible without new instrumentation each time we
   debug a "missing event" symptom. See B.2 item 2 issue.

---

## Reflection

**What surprised me**:

- The `EventTurnStarted.Prompt` drop in `TUIAdapter.WaitForEvent`
  (§B.3) is a real "TUI viewport divergence" mechanism that I did not
  expect to find — but is exactly what dmotles described as "weave
  reacting to notifications I can't see in the viewport." The
  `// Skip lifecycle-only events` comment is technically correct
  (`EventTurnStarted` IS a lifecycle event) but elides the fact that
  the prompt being delivered has no other rendering path in unified
  mode.

- The dual-drain race in §B.1 is more invasive than QUM-465 implies.
  Two paths both reading from `pending/` and producing prompts is the
  underlying design issue; deduping banners alone won't fix it.

- The legacy `agentloop.Runner` runs maildir-polling, control-channel,
  pending-queue drain, AND `.wake` file detection in series, all four
  active simultaneously. Even before unified runtime, this was
  confusing.

- The `EventBus` non-blocking publish + per-subscriber buffer of 64 is
  a real silent-loss path. I expected backpressure; the design
  explicitly chose drop-on-overflow.

**Open questions**:

- Is there a real TUI test that asserts an inbox prompt RENDERS in the
  viewport under unified runtime? `test-notify-tui-e2e.sh` checks for
  the **banner** (`inbox: N new message(s) for weave`) but I did not
  verify whether it also asserts the prompt body. If not, the §B.3 bug
  has been latent for the entire QUM-399 lifetime without a regression
  guard.

- Does the activity panel (which subscribes to the same EventBus with
  its own buffer-64 channel) actually observe drop events that the TUI
  also drops? If so, the user sees the activity tail "skip" but no
  indication of why.

- Is the `peekAndDrainCmd` backstop actually winning the race in
  practice (i.e. is the user seeing the InboxDrainMsg banner often
  enough that they don't notice the bug in B.3)? If so, the fix is
  simpler ("delete the WeaveRuntimeHandle enqueue path"); if not,
  we need a fix that emits EventTurnStarted-equivalent visibility.

**What I'd investigate next**:

- Live-trace a `SPRAWL_UNIFIED_RUNTIME=1` session to count, for one
  child→weave send, how many `EventTurnStarted` events fire vs how
  many `InboxDrainMsg` are dispatched vs how many viewport
  AppendSystemMessage calls hit. The ratio will tell us which path
  actually delivers in practice.

- Audit whether `ChildStreamAdapter` (peer of `TUIAdapter` for child
  agents) has the same EventTurnStarted-drop hole — likely yes, but
  the impact is different because we don't normally inject prompts to
  child agents from outside their own session.

- Diff `internal/tui/bridge.go` (legacy) vs `tuiruntime/tuiadapter.go`
  (unified) for any other lifecycle events that diverge silently.
