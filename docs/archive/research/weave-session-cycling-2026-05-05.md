> Terminology note (2026-06): pre-rename "sub-agent" = current "sidechain".

# Weave session cycling under unified runtime — root-cause analysis

**Date:** 2026-05-05
**Author:** ghost
**Status:** Historical. Captured while the unified runtime was a `SPRAWL_UNIFIED_RUNTIME=1` opt-in. QUM-400 retired the env gate and the legacy paths; the failure mode described here was specific to that pre-cleanup configuration.
**Repro binary:** v0.1.10-174-gf91d87f (main @ 80100d7)
**Gate (historical):** `SPRAWL_UNIFIED_RUNTIME=1`, TUI mode (`sprawl enter`)

---

## TL;DR

A type collision between three independent EventBus adapters causes the AppModel
to mis-interpret child-runtime / activity-panel subscription closes as **the
weave bridge's EOF**, kicking off a full session-restart cycle (`Session
restarting (session ended)...` banner + `RestartSessionMsg`). The user
observes a stack of phantom restart banners; under handoff/consolidation
windows where `m.restarting=true` coalesces the actual restart, the only
visible symptom is the banner stack — but each EOF still pumps a
`SessionRestartingMsg{Reason: "session ended"}` through the AppModel.

Proximate emissions originate in `internal/tui/activity_stream.go` and
`internal/tui/child_stream.go`; both return `SessionErrorMsg{Err: io.EOF}`
on subscription close. AppModel's `case SessionErrorMsg` (`internal/tui/app.go:790`)
short-circuits any EOF — regardless of source — into the bridge-restart path.

The activity-stream marker `error stop=tool_use turns=3` reported by the user
is **not** the root cause; it's a coincident Claude-internal Task subagent
result, and the weave subprocess is in fact still alive across the entire
banner stack. The two are correlated only because the user's `/handoff` and
the Task error happened in the same window, and the handoff path itself
triggers the EOF cascade described below.

---

## Evidence

### Live pane capture

`/home/coder/.claude/projects/-home-coder-sprawl/930475a0-c284-4d61-b6a1-88db7713b373/tool-results/b7m7ezon6.txt`
shows the full TUI viewport during the incident. Key timeline from the activity
panel (right column) and viewport banners (middle column):

| Time     | Activity (right panel)                            | Viewport banner (left)                           |
|----------|---------------------------------------------------|--------------------------------------------------|
| 18:21:37 | `Agent {description: "Heavy survey for Esc..."}`  |                                                  |
| 18:21:37 | `task_started`                                    |                                                  |
| 18:21:40 | `task_progress` / Glob / Grep                     |                                                  |
| 18:21:42 |                                                   | `/handoff dispatched — see output below`         |
| 18:21:42 |                                                   | `Session restarting (handoff)...`                |
| 18:21:43 | `task_notification`                               | `Session restarting (session ended)...`          |
| 18:21:43 | `error stop=tool_use turns=3`                     |                                                  |
| 18:21:50 | `session_state_changed: idle`                     | `Session restarting (session ended)...` ×6 more  |
| 18:21:50 | `session_state_changed: running`                  |                                                  |
| 18:21:50 | `init`                                            |                                                  |
| 18:21:55 | `Yes — that's the fix working...` (assistant)     |                                                  |
| 18:21:55 | `success stop=end_turn turns=1`                   |                                                  |
| 18:23:10 | `session_state_changed: idle/running/init`        | (more `Session restarting` banners through 18:23:27) |
| 18:23:31 |                                                   | `consolidation started`                          |
| 18:25:??  |                                                   | `Consolidation complete (120s)`                  |

Critical observations:

1. After `error stop=tool_use turns=3` at 18:21:43, the activity panel records
   `session_state_changed: idle/running` and a successful turn (`success
   stop=end_turn turns=1`) at 18:21:55. **The weave Claude subprocess did not
   exit**; it kept producing protocol events on the same EventBus through the
   entire banner-stack window.
2. The 8 `Session restarting (session ended)...` banners stack between
   18:21:43 and 18:23:27 — two minutes of repeated phantom restarts while the
   genuine handoff-driven restart is in progress (consolidation kicks off at
   18:23:31 and finishes 120s later).
3. The single legitimate restart banner is `Session restarting (handoff)...`
   at 18:21:42. Everything after is spurious.

### Activity ndjson confirms the live subprocess

The previous wedge investigation (`docs/forensics/tui-weave-wedge-2026-05-05.md`)
notes that the same activity file appends `result error stop=tool_use turns=3`
at 18:54:42 in a separate incident. In today's capture the equivalent line at
18:21:43 is *followed* by further `init`, `assistant`, and `success` entries —
confirming the ring-buffer subscriber on weave's UnifiedRuntime EventBus is
still receiving events across the spurious banners.

---

## Where the EOFs come from

Three TUI adapters subscribe to EventBus channels and *all three* surface
subscription close as the same tea.Msg:

| Adapter                                                | File                                | EOF return site (line)            |
|--------------------------------------------------------|-------------------------------------|-----------------------------------|
| `tuiruntime.TUIAdapter` (the bridge for weave's runtime) | `internal/tuiruntime/tuiadapter.go` | 94, 110 — `tui.SessionErrorMsg{Err: io.EOF}` |
| `tui.ActivityStreamAdapter` (activity panel, any agent)  | `internal/tui/activity_stream.go`   | 114, 125 — `SessionErrorMsg{Err: io.EOF}`    |
| `tui.ChildStreamAdapter` (per-child viewport)            | `internal/tui/child_stream.go`      | 103, 114 — `SessionErrorMsg{Err: io.EOF}`    |

The AppModel handler at `internal/tui/app.go:785-795`:

```go
case SessionErrorMsg:
    // Transport EOF is the normal end-of-session signal (clean exit or
    // /handoff). Auto-restart via Phase D rather than showing the crash
    // dialog ...
    if errors.Is(msg.Err, io.EOF) {
        reason := "session ended"
        return m, tea.Batch(
            sendMsgCmd(SessionRestartingMsg{Reason: reason}),
            sendMsgCmd(RestartSessionMsg{}),
        )
    }
```

does **not** distinguish which adapter produced the message. Any `io.EOF`
fired by *any* of the three adapters is treated as the weave bridge ending —
producing the `Session restarting (session ended)` banner and a (coalesced
during handoff) `RestartSessionMsg`.

---

## How the cascade reaches 8 banners during /handoff

`/handoff` dispatch sequence (from `internal/tui/app.go`):

1. **HandoffRequestedMsg** (`app.go:806`): emits
   `tea.Batch(SessionRestartingMsg{handoff}, RestartSessionMsg{})`.
2. **RestartSessionMsg** (`app.go:836`): closes `m.bridge`, sets
   `m.restarting = true`, dispatches `restartFunc` off the main goroutine,
   returns `RestartCompleteMsg` once the new bridge is ready.
3. `restartFunc` (in `cmd/enter.go:124-133`, set by `makeRestartFunc`) **stops
   the registered weave UnifiedRuntime** (calls `existing.Stop(ctx)`,
   `reg.Remove("weave")`) before running consolidation. Stopping the runtime
   does *not* close its EventBus subscribers' channels (subscribers must
   `unsubscribe()` themselves), but `bridge.Close()` from step 2 *does* close
   the **bridge's** subscription via `TUIAdapter.Cancel()`.
4. `newSessionImplUnified` (`cmd/enter.go:411-419`) is also self-cleaning: it
   re-runs `existing.Stop(ctx)` + `reg.Remove("weave")` if anything is still
   registered, then constructs a brand-new UnifiedRuntime.

The single legitimate close happens at step 2: bridge.Close → adapter.Cancel
→ unsubscribe → channel closes → 1 EOF from the bridge's parked WaitForEvent.

The remaining EOFs come from the **activity adapter**:

* `ActivityStreamAdapter` was bound to weave's UnifiedRuntime via
  `AgentSelectedMsg` (`app.go:1177-1206`); user is observing weave at the
  moment of /handoff, so the adapter's subscription is the OLD runtime's
  EventBus.
* When the OLD runtime's EventBus is gc'd along with the runtime — or when
  later activity panels dispatch a stale `ActivityStreamMsg` and AppModel
  re-arms `activityStreamWaitCmd` against the (still-subscribed-but-stopped)
  adapter — the adapter's `WaitForEvent` returns `SessionErrorMsg{Err: io.EOF}`
  immediately if its `cancelled` flag was set, or as soon as the channel
  closes.
* `RestartCompleteMsg` does **not** re-bind the activity adapter to the new
  UnifiedRuntime (only `AgentSelectedMsg` does that — see `app.go:1177-1213`).
  The user has to re-select weave in the tree to re-attach. So between
  /handoff and the next manual selection, every queued `ActivityStreamMsg`
  in the AppModel's mailbox triggers another `WaitForEvent` on the dead
  adapter → another EOF → another banner.

The ChildStreamAdapter has the same shape and contributes additional EOFs if
a child agent retires (or weave's child-tree poll observes a runtime
disappearing) during the same window.

The cumulative count (≈8 in the captured incident) matches:

* 1 from the bridge's TUIAdapter (legitimate but mis-routed banner).
* N from the activity adapter as queued ActivityStreamMsgs drain into a
  cancelled adapter.
* Possibly some from a child-stream adapter created by an earlier child
  selection.

The exact count is timing-sensitive and not load-bearing for the diagnosis;
the type collision is the bug.

---

## Why this is a regression introduced by QUM-439 / QUM-440

| Commit  | Date    | Change                                                                |
|---------|---------|-----------------------------------------------------------------------|
| 9771dfc | Apr 25  | QUM-439: child viewport streams from UnifiedRuntime EventBus (introduces `ChildStreamAdapter`). |
| 719b029 | Apr 25  | QUM-440: activity panel streams from EventBus (introduces `ActivityStreamAdapter`). |

Both commits reused the existing `tui.SessionErrorMsg{Err: io.EOF}` sentinel
to signal subscription close, copying the shape from the bridge's
`tuiruntime.TUIAdapter`. The bridge's sentinel was previously the **only**
producer of that message, so the AppModel handler at `app.go:790` was safe.
After QUM-439/QUM-440, the same sentinel has three producers.

QUM-475 (today's commit 862134b) and QUM-471 (today's f91d87f) did not
introduce the cycling — they touched the InterruptCompletedMsg / finalizeTurn
chokepoint and InterruptDelivery wake-only path respectively, neither of
which interacts with the activity/child stream adapters' EOF emissions.

The reason this regressed-and-stayed-latent until today is that the failure
mode requires:

* A user-driven session restart (e.g. `/handoff` or a real EOF from claude),
  AND
* An active activity-stream or child-stream adapter at the moment of restart.

Headless E2E test harnesses (`make test-handoff-e2e`,
`make test-notify-tui-e2e`) do not panel-observe an agent across handoff,
so they never subscribe the activity adapter to the old runtime. The TUI
e2e harness in `scripts/test-notify-tui-e2e.sh` likewise focuses on the
synthetic weave-row banner, not the full pane capture, so phantom-banner
stacks weren't caught.

QUM-475's exhaustiveness test
(`internal/tuiruntime/event_mapping_exhaustive_test.go`) only pins the
`tuiruntime.TUIAdapter`'s mapping of RuntimeEventType → tea.Msg. It does
not cover the `tui.ActivityStreamAdapter` or `tui.ChildStreamAdapter`,
so the dual EOF emission slipped through.

---

## What `error stop=tool_use turns=3` actually means here

The activity entry `error stop=tool_use turns=3` is rendered by
`internal/agentloop/activity.go:163`:

```go
summary := fmt.Sprintf("%s stop=%s turns=%d", status, res.StopReason, res.NumTurns)
```

It corresponds to a Claude protocol `result` message with `is_error: true`
and `stop_reason: "tool_use"`. In the captured incident, the result
immediately follows a `task_started` / `task_progress` / `task_notification`
sequence — i.e., it's a Claude **internal Task subagent** (the built-in
`Agent` tool, *not* a Sprawl child), which the weave Claude binary spawned
to run "Heavy survey for Esc test". The Task subagent failed internally,
returned an error result *to the parent Claude session*, and the parent
session continued (see `success stop=end_turn turns=1` 12 seconds later).

This is **not** weave's own subprocess exiting. The activity ring forwards
all `result` messages from the Claude subprocess regardless of nesting,
so a sub-Task's terminal result lands in the same ndjson file as a
top-level turn result — easy to misread as "weave's session just ended".

Conclusion: the `error stop=tool_use turns=3` marker is a coincident
Claude-internal Task failure with no causal relationship to the banner
stack. The user's note that it "preceded the first session-ended banner"
reflects the activity-panel render order, not a causal sequence — the
real trigger of banner #1 is `bridge.Close()` from the /handoff dispatch
two timestamps earlier.

---

## Subprocess lifetime under unified runtime

Confirmed-correct invariants (no regression):

* `claude --print --input-format=stream-json --output-format=stream-json`
  stays alive across multiple turns; weave's subprocess lifecycle in unified
  mode is supposed to exit only on `bridge.Close()`, `WeaveRuntimeHandle.Stop`,
  or a process crash. The captured activity ndjson shows the subprocess is
  in fact alive across the banner stack.
* `internal/runtime/turnloop.go` runs one turn per queue drain and publishes
  `EventTurnStarted` / `EventTurnCompleted` / `EventTurnFailed` /
  `EventInterrupted` / `EventQueueDrained` per cycle. It does **not** exit
  the subprocess between turns.
* `internal/backend/session.go` owns the stream-json transport. `readTurn`
  closes the per-turn `events` channel on EOF, but the `transport.Recv` EOF
  only happens when the subprocess actually exits — which would require the
  Claude binary to terminate. None of that happened in the captured incident.
* No `--max-turns` flag is passed (`internal/claude/launch.go` does not emit
  one); `error stop=tool_use turns=3` is *not* a max-turns hit.

The "subprocess silently relaunching many times per minute" hypothesis from
the issue prompt is **not** what's happening. The subprocess is alive; only
the TUI is firing phantom restart banners. The genuine restart cycle is
happening exactly once (the /handoff path), but it's masked by 7 phantom
"session ended" banners that are not actually restarting anything (they
coalesce against `m.restarting=true`).

The token / API burn the user is worried about is real but secondary:
the genuine /handoff restart does relaunch the subprocess once, with
`--resume`, plus a 120s consolidation. The phantom banners do not relaunch
the subprocess — `RestartSessionMsg` is coalesced — so they cost a banner
render and an `m.bridge.Close()` no-op (bridge is already nil), not a
new claude invocation.

**That said**: in non-handoff EOFs (e.g., a real subprocess crash) where
`m.restarting=false`, each phantom EOF *would* trigger a real restart
attempt. The captured incident happened to be a /handoff window, which
masked the worst-case behavior.

---

## Proposed fix (1–3 bullets)

1. **Type-split the adapter EOFs.** Introduce dedicated tea.Msg types for
   activity-stream and child-stream subscription closes — e.g.,
   `ActivityStreamClosedMsg{Agent, Epoch}` and `ChildStreamClosedMsg{Agent, Epoch}`.
   Update `internal/tui/activity_stream.go:114,125` and
   `internal/tui/child_stream.go:103,114` to return those instead of
   `SessionErrorMsg{Err: io.EOF}`. AppModel handlers for the new types
   silently de-attach the adapter (clear `m.activityAdapter` /
   `m.childAdapter` on epoch match) without emitting `SessionRestartingMsg`.
   `tuiruntime.TUIAdapter` keeps emitting `SessionErrorMsg{Err: io.EOF}` —
   it remains the *sole* producer of the bridge-restart trigger.

2. **Re-bind activity adapter on RestartCompleteMsg.** When weave's
   UnifiedRuntime is replaced (e.g., post-handoff), `m.activityAdapter`
   still points at the dead old runtime. `RestartCompleteMsg`
   (`internal/tui/app.go:914-978`) should call
   `m.activityAdapter.Observe(newWeaveUnifiedRuntime)` if
   `m.activityAdapterAgent == m.rootAgent`, so the activity panel keeps
   streaming after restart without the user manually re-selecting weave.

3. **Add a regression test** in
   `internal/tuiruntime/event_mapping_exhaustive_test.go` (or a new
   `internal/tui/adapter_eof_isolation_test.go`) that asserts:
   (a) ActivityStreamAdapter close emits the new sentinel, not
   SessionErrorMsg; (b) ChildStreamAdapter close emits the new sentinel;
   (c) the AppModel handler for the new sentinels does NOT emit
   `SessionRestartingMsg`. This guards against the same type-collision
   pattern recurring as more EventBus consumers are added.

A minimal version of the fix is just (1); items (2) and (3) are
defense-in-depth that prevent the next regression in this area.

---

## Files most relevant to the bug

* `internal/tui/activity_stream.go:104-126` — ActivityStreamAdapter.WaitForEvent (EOF emission).
* `internal/tui/child_stream.go:93-115` — ChildStreamAdapter.WaitForEvent (EOF emission).
* `internal/tui/app.go:785-795` — case SessionErrorMsg, EOF→restart short-circuit.
* `internal/tui/app.go:1023-1053` — case ActivityStreamMsg (re-arms WaitForEvent on cancelled adapter).
* `internal/tui/app.go:1143-1279` — case AgentSelectedMsg (only place activity/child adapters are bound).
* `internal/tui/app.go:914-978` — case RestartCompleteMsg (does NOT re-bind activity adapter).
* `internal/tuiruntime/tuiadapter.go:84-110` — TUIAdapter.WaitForEvent (the *intended* SessionErrorMsg{io.EOF} producer).
* `cmd/enter.go:411-486` — newSessionImplUnified (creates fresh UnifiedRuntime per restart).
* `cmd/enter.go:95-154` — makeRestartFunc (Stops old weave runtime before consolidation).

---

## Reflections / open questions

**Surprising finding:** the `error stop=tool_use turns=3` marker the user
flagged as the trigger turned out to be a red herring — a Claude-internal
Task subagent failure unrelated to weave's own subprocess. The visible
correlation in the activity panel (the marker right next to the first
phantom banner) is rendering coincidence. This is exactly the kind of
shoulder-to-shoulder timing that misleads a triage diff against pre-today
main: the symptom changed (phantom banners) but the suspected cause
(`stop=tool_use`) had been benign all along.

**Open questions I would investigate with more time:**

1. **Exact EOF count derivation.** I traced 1 bridge EOF + N activity EOFs
   but did not write a deterministic test reproducing exactly 8. The mailbox
   queue depth at the moment of cancellation is timing-dependent. A
   bubbletea harness that injects synthetic ActivityStreamMsg backlog
   before /handoff should let us tune the upper bound.

2. **Non-handoff EOF behavior.** In a real subprocess crash (not /handoff),
   `m.restarting=false` initially, so each phantom EOF triggers a real
   `RestartSessionMsg` cycle until coalescing kicks in. That's potentially
   N actual claude relaunches per crash. Worth simulating with a sandbox
   that kills weave's claude subprocess and observing the banner/restart
   count.

3. **Did QUM-439/QUM-440's reviewers consider the type collision?** The
   commit messages don't mention the SessionErrorMsg sentinel — the original
   bridge contract was likely not captured well enough in code comments
   to flag the collision at review time. Worth adding a doc comment on
   `tui.SessionErrorMsg` reserving `Err: io.EOF` for the bridge.

4. **Does ChildStreamAdapter's EOF on child retire produce phantom restarts
   in normal operation (no /handoff)?** I believe yes (every retire would
   produce one banner + a coalesce-or-real-restart). Worth a sandboxed
   spawn-then-retire cycle to confirm before fix lands.
