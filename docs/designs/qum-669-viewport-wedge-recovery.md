# QUM-669 — TUI viewport wedge recovery after EventBus drops

**Status:** rev 1 — design pass, awaiting impl-site selection (forge) once
  citadel weighs in on B4's preferred landing.
**Author:** ghost
**Date:** 2026-06-03
**Branch:** `dmotles/qum-669-design`
**Tracks:** QUM-669

---

## 1. Chosen approach

**Sequence-numbered event stream (publisher-side) + gap-triggered resync
from the Claude session JSONL + status-bar segment + single in-viewport
banner on the transition into `dropped` state, with `Ctrl+R` as the
manual short-circuit.** This is the issue's option-4 (full resync)
combined with option-3 (toast/banner) as the *adjunct* signaling layer.
Options 1, 2, and 5 are rejected: a bigger buffer (#1) and a longer
publish deadline (#2) only push out the failure horizon and trade
correctness for delay; a ring that overwrites oldest (#5) silently loses
in-flight assistant streaming continuity, which is the worst failure
mode for the user.

Resync correctness is the only end-state that keeps the viewport's
contract — "what you see equals what the agent has actually done" —
robust against regressions in the render path (QUM-667 cache, future
ChatList work). The status-bar segment from QUM-681 already exists and
covers the *something is wrong* signal at low cost; this design adds
*and we recovered* on top, which is the part the user cannot do today.

**Important correction to the issue's framing:** the issue suggests
resyncing from `activity.ndjson`. That file is a **summarized** event
log (truncated to 200 B per entry, secret-redacted; see
`internal/agentloop/activity.go:19,49,260`) and cannot reconstruct the
viewport faithfully — tool inputs, full assistant text, and
system-notification envelopes are all lost. The right resync source is
the **Claude session JSONL** at
`memory.SessionLogPath(homeDir, sprawlRoot, sessionID)`, which is
already the input to the proven resume-replay path
(`internal/tui/replay.go` `LoadTranscript`). This design uses that path
verbatim — no new parser, no new on-disk format. Activity.ndjson stays
useful as a coarse cross-check during testing but is not the rebuild
source.

---

## 2. Mechanism

### 2.1 Sequence number — publisher-side, per-bus

Add a `Seq uint64` field to `RuntimeEvent`
(`internal/runtime/eventbus.go:85`). Add a `seq atomic.Uint64`
counter to `EventBus`. `Publish`
(`internal/runtime/eventbus.go:193`) stamps `event.Seq = b.seq.Add(1)`
**before** the fan-out loop. The stamp lives in publisher state, so
every subscriber sees an identical, monotonic, 1-indexed sequence and a
drop is detectable purely from a gap in received seq numbers — no
cross-subscriber bookkeeping required.

Why publisher-side, not per-subscriber: gap detection is then
unambiguous (`got - last != 1`) and survives the publisher fanning out
to a fresh subscriber mid-stream — the new subscriber simply takes the
current seq as its `lastSeq` baseline on the first event.

### 2.2 Gap detection — subscriber-side

In `TUIAdapter` (`internal/tuiruntime/tuiadapter.go`) add `lastSeq
uint64` to the adapter struct and update it in `WaitForEvent`'s receive
loop (line 101–115). When a received `ev.Seq != lastSeq + 1` and
`lastSeq != 0`, emit a `tui.EventDropDetectedMsg{From, To, Missing}` as
the *current* tea.Msg result — the very next message the AppModel
reduces. Update `lastSeq = ev.Seq` and continue normally so the
in-band event still flows.

Corroboration: `EventBus.DroppedCounts()` already exposes a per-
subscriber monotone counter, and `EventBus.DropTelemetry()` returns
`{Cumulative, LastDropAt}` (eventbus.go:233, :248). The status-bar
segment already consumes that snapshot (app.go:2546
`refreshDropTelemetry`, mcpOpTickInterval = 1 s). The seq-gap signal is
the **primary** event-trigger for resync; the drop-counter snapshot is
the **secondary** confirmation read in the resync command and surfaced
to the user. We do not rebuild the existing drop telemetry — we use it.

### 2.3 Single-blip filter (alarm-fatigue suppression)

Per AC: a single transient drop that drains immediately must NOT trigger
the resync flow or the banner. Reuse the existing rate-limit
constants in `eventbus.go:42`:

- `dropWarnBurstThreshold = 10` — fewer than 10 missing events that
  drain in `quietWindow = 500ms` get the status-bar ⚠ chip (already
  wired) but no resync and no banner.
- `dropWarnInterval = 5s` — banner is suppressed on subsequent gaps
  within 5 s of a resync completing (sticky suppression so a
  flapping consumer doesn't churn).

State machine (per-adapter, single goroutine in the AppModel reducer):

```
normal → gap-pending → dropped → resyncing → recovered → normal
        (debounce)    (banner)   (LoadTranscript)
              ↘ (drained within 500ms, missing<10) → normal
```

`gap-pending` is a 500-ms debounce window — if subsequent events
arrive with contiguous seq numbers and the cumulative miss count stays
below `dropWarnBurstThreshold`, return to `normal` without alarming.
Implemented as a `tea.Tick(500ms)` arming a `gapConfirmCmd` that the
reducer can cancel on a same-window recovery.

### 2.4 Resync command — read, parse, replace

On entry to `resyncing`:

```text
sessionID := backend.SessionID()                   // tui.SessionBackend
path := memory.SessionLogPath(home, sprawlRoot, sessionID)
entries, err := tui.LoadTranscript(path, ReplayMaxMessages)
return ViewportResyncMsg{Entries: entries, MissingCount: N, Err: err}
```

Reducer:
- `viewport.SetMessages(entries)` — `internal/tui/viewport.go:610`,
  already invalidates Agent-nesting state and selection. **No new
  rendering surface.**
- `setTurnState(TurnIdle)` if the rebuilt transcript shows no
  unterminated assistant block (it always shows finalized entries; the
  JSONL is post-result). This unwedges the "Streaming…" status bar and
  unwedges Esc-interrupt targeting (AC #4).
- Append an in-viewport `MessageStatus` line "✓ resynced — recovered
  N events from session log" (transient, scrollable, dismissable).
- Transition to `recovered`, then back to `normal` after a 500-ms
  cooldown.

Failure path (file missing, parse error, sessionID empty): toast
"resync failed: <err>; press Ctrl+L to retry, Ctrl+Q to quit." Stay in
`dropped` state — do **not** silently revert to `normal`. The status
bar ⚠ remains.

### 2.5 Manual short-circuit — Ctrl+R

Bind `Ctrl+R` in `app.go`'s key-handler (alongside the existing Ctrl+
chord set) to unconditionally start the resync command, independent of
the state machine. This gives the user a manual escape hatch even if
the gap detector misclassifies a drift (e.g. clock skew, future
publisher batching). Behavior identical to the automatic
`dropped → resyncing` transition.

> **Note on Ctrl+R.** The TUI structural rewrite plan §3 lists "Ctrl+R
> persistence" as out-of-scope for the rewrite, but **does not** claim
> the keybinding. There is no conflict today. If a future feature lays
> claim, a Ctrl+R⌃Y chord can be used; design accommodates either.

### 2.6 User-visible signal — placement

| Surface | Trigger | Notes |
|---|---|---|
| Status bar ⚠ segment | First drop tick (1 s) | Already shipped (QUM-681); no change. |
| In-viewport banner / `MessageStatus` line | `normal → dropped` transition only | One-shot, in-flow, scrollable. Avoids a modal that would interfere with the existing question/handoff modal stack. |
| Status bar resync state pill | `resyncing` | Reuses the `SetValidatePill` /  pending-questions segment pattern (statusbar.go:85, 225). |
| In-viewport `MessageStatus` line | `recovered` | Single line, same surface as the drop banner. |
| Toast (modal-light) | **NOT used.** | A modal would block clean-exit (AC #4) — the wedged-spinner failure mode is exactly the "blocking UI element you can't get past" case we're fixing. |

Both today's `viewport.go` and the future `ChatList` from B4's rewrite
expose Append-style status-message surfaces; both can render
`MessageStatus`. See §3 for the portable seam.

### 2.7 Exit-cleanliness (AC #4)

Three things conspire today to wedge exit:

1. `turnState == TurnStreaming` pins Esc to a phantom tool call.
2. Spinner ticker keeps animating a pending tool entry whose
   `tool_result` was dropped.
3. The status bar reads "Streaming…" forever.

The gap-detection path **immediately** (on receipt of
`EventDropDetectedMsg`, before the debounce or resync starts):

- Calls `setTurnState(TurnIdle)` if the prior state was `TurnStreaming`
  or `TurnThinking`. This is the central fix for AC #4 — independent
  of whether the resync succeeds, the user can quit cleanly via the
  existing `Ctrl+C` / quit paths (`app.go:622,912,926,982,1343`),
  which are NOT gated on `turnState` (audited; they always return
  `tea.Quit`).
- Spinner pending entries: `SetMessages(entries)` from the resync
  unconditionally replaces them with `Complete: true` rebuilt
  entries. Until resync completes, the spinner continues to animate
  but exit is no longer blocked — quit goes straight through.

Explicit invariant we add: **no code path in app.go's keybinding
reducer may consult `turnState` before deciding whether to honor a
quit chord.** The audit confirms today's code already meets this;
add a regression test (`TestQuitFromWedgedStreamingState`).

---

## 3. Portable seam: today (viewport.go) vs. B4 (ChatList)

Per `docs/designs/tui-structural-rewrite-plan.md`:
- S3-S4 introduce `ChatList` alongside `ViewportModel`.
- S6 deletes `viewport.go` wholesale.
- S5 routes "contract violators" away from `ChatList`.

The portable cut:

```
┌──────────────────────────────────────────────────────────────────┐
│ internal/runtime/eventbus.go                                     │
│   - Seq stamping (Publish)                                       │
│   - DropTelemetry (already shipped)                              │
│   These stay forever; the rewrite does not touch this layer.     │
├──────────────────────────────────────────────────────────────────┤
│ internal/tuiruntime/tuiadapter.go                                │
│   - lastSeq tracking, EventDropDetectedMsg emission              │
│   This is the "thin presenter shim" the design doc mentions.    │
│   Survives the rewrite — tuiadapter is the bus→tui adapter      │
│   regardless of whether the renderer is ViewportModel or         │
│   ChatList. Already mirrors DropTelemetry across the boundary.   │
├──────────────────────────────────────────────────────────────────┤
│ internal/tui/app.go                                              │
│   - State machine, debouncer, ViewportResyncMsg handler          │
│   - Ctrl+R binding                                               │
│   - turnState idle-on-gap fix                                    │
│   AppModel survives the rewrite — only the renderer it owns      │
│   changes (vp → cl).                                             │
├──────────────────────────────────────────────────────────────────┤
│ internal/tui/replay.go                                           │
│   - LoadTranscript (already shipped)                             │
│   - Used unchanged.                                              │
├──────────────────────────────────────────────────────────────────┤
│ Renderer (the one swappable surface):                            │
│   today:   ViewportModel.SetMessages([]MessageEntry)             │
│   future:  ChatList.Reset([]MessageEntry)  ← propose for S3-S4   │
│   Define a tiny interface in internal/tui that both implement:   │
│     type MessageSink interface {                                 │
│         Reset(msgs []MessageEntry)                               │
│         AppendStatus(text string)                                │
│     }                                                            │
│   AppModel holds the sink, not the concrete renderer.            │
└──────────────────────────────────────────────────────────────────┘
```

**Compatibility with the rewrite plan:**
- Resync uses `Reset(entries)`, which is the natural counterpart to
  `Append(...)` — *not* a contract violator under §3 S5. Add it to the
  `ChatList` API in S3 (the slice that introduces ChatList) as a
  first-class operation. This is small (≤30 LOC) and avoids the S5
  "contract violation" audit pulling resync out.
- `AppendStatus` already exists on `ViewportModel`
  (`viewport.go:498`); S5 routes status messages away from `ChatList`
  to a status-bar segment per the rewrite plan. **The resync banner
  fits the same reroute** — the design here writes a transient
  status segment, which is exactly the S5-compliant target. No
  conflict.

**Coordination note for forge:** if citadel prefers to land the
mechanism inside B4's branch (S3 or S4), the work splits cleanly:
EventBus seq stamping and the tuiadapter detection are independent
of any ChatList work and can land first on `main`. The renderer-
facing piece (`Reset(entries)`) lands wherever ChatList is being
introduced. **Final landing site is forge's call after citadel
responds.**

---

## 4. Test plan (TDD)

All four scenarios from the issue's Testing Expectations are covered.

### 4.1 Synthetic drop — `internal/runtime/eventbus_test.go`

`TestEventBus_GapDetection_OnSlowSubscriber`:
- Subscribe with `buffer=4`.
- Publish 100 events with a consumer that sleeps 5 ms between reads.
- Assert: received events have monotonic but non-contiguous Seq values;
  cumulative `DroppedCounts()` matches `100 - len(received)`.

### 4.2 TUIAdapter gap emission — `internal/tuiruntime/tuiadapter_test.go`

`TestTUIAdapter_EmitsEventDropDetectedMsg`:
- Stand up a runtime with a stub backend that publishes Seq=1, 2, then
  Seq=10 (simulating a gap).
- Assert: `WaitForEvent` returns `EventDropDetectedMsg{From: 2, To: 10,
  Missing: 7}` then resumes normal event delivery.

### 4.3 Single-blip filter — `internal/tui/app_drop_test.go`

`TestAppModel_GapDebounce_DoesNotBannerOnSingleBlip`:
- Inject `EventDropDetectedMsg{Missing: 3}`.
- Inject contiguous-Seq events within 500 ms.
- Assert: state machine returns to `normal`; no resync command
  scheduled; viewport has no `MessageStatus` banner; status bar ⚠
  chip is visible (drop telemetry tick) but no resync state pill.

### 4.4 Resync correctness — `internal/tui/app_resync_test.go`

`TestAppModel_ResyncFromSessionLog_MatchesNonDroppedRender`:
- Fixture: a small canned JSONL with 6 turns + 2 tool calls + 1
  system-notification.
- Path A: feed all 9 events through the EventBus normally; capture
  `viewport.GetMessages()` as the gold.
- Path B: feed events 1-2 then trigger a synthetic gap of 5 → 9;
  drive the AppModel through the state machine; assert
  `viewport.GetMessages()` equals the gold from path A.

### 4.5 Clean-exit during divergence — manual + e2e

`TestAppModel_QuitFromWedgedStreamingState` (unit):
- Force `turnState = TurnStreaming` and one `Pending: true` tool entry.
- Inject `EventDropDetectedMsg`.
- Send `KeyCtrlC`.
- Assert: returned `tea.Cmd` is `tea.Quit`; `turnState == TurnIdle`
  on the snapshot prior to quit.

Add a new e2e matrix row `viewport-resync` covering the full path:
- Trigger files: `internal/runtime/eventbus.go`,
  `internal/tuiruntime/tuiadapter.go`, `internal/tui/replay.go`,
  `internal/tui/app.go`'s `EventDropDetectedMsg` /
  `ViewportResyncMsg` handlers.
- Script: drive a synthetic high-rate publish through a real TUI;
  assert the status-bar ⚠ chip appears, the banner fires once, the
  resync completes, and the post-resync transcript matches a
  control run.

### 4.6 Existing e2e matrix

No expected regressions in `notify-tui`, `handoff`, `merge-reuse`,
`ask-user-question`, `drain-row-inject`, `idle-interrupt-inject`,
`paste-coalesce`, `recover-live`. The Seq field is additive on
`RuntimeEvent`; adapter changes are additive. Run the full matrix
(`make test-e2e-matrix`) as the S6-style validation gate.

---

## 5. LOC and complexity tier

**Tier: medium.**

| Area | File(s) | Approx LOC |
|---|---|---|
| `Seq` field + stamping | `internal/runtime/eventbus.go` | +25 |
| Adapter gap detection | `internal/tuiruntime/tuiadapter.go` | +40 |
| `MessageSink` interface + plumbing | `internal/tui/session_backend.go` | +20 |
| State machine + reducer + Ctrl+R | `internal/tui/app.go` | +120 |
| Resync command (reuse LoadTranscript) | `internal/tui/app.go` | +30 |
| Status-bar resync pill | `internal/tui/statusbar.go` | +15 |
| Test fixtures + tests | `*_test.go` | +400 |
| New e2e matrix row | `scripts/e2e-tests/*`, `CLAUDE.md` table | +50 |
| **Total** | | **~700 LOC, mostly tests** |

The production code is ~250 LOC across 5 files, almost all additive,
no deletions, no schema changes. The complexity sits in the state
machine + debouncer (item-2 below), not the wiring.

Risk hotspots for the implementer:
1. **Reentrancy.** The resync command must not race with new in-flight
   events arriving after `EventDropDetectedMsg`. Either (a) buffer
   events arriving during `resyncing` and re-apply post-`Reset`, or
   (b) start the new `lastSeq` baseline from the highest-seen Seq at
   resync-completion time. **Recommendation (b)** — simpler, and any
   "lost" events during resync are by definition already in the
   session JSONL the resync just read.
2. **Debounce + cancel.** `tea.Tick(500ms)` cannot be cancelled
   directly; instead, tag each `gapConfirmCmd` with a monotonic
   `gapID` and discard stale fires in the reducer. Idiomatic pattern
   already used in `app.go` for `mcpOpThresholdMsg` (line 2580 —
   `mcpOpThresholdCmd` with a `CallID`). Reuse the pattern.
3. **Resume-replay collision.** On a `sprawl enter` restart the
   `LoadTranscript` path is *already* called once (`cmd/enter.go:667`).
   The resync path uses the same loader; ensure neither double-loads
   nor races with the initial preload. Use a `resyncing` mutex on
   AppModel — single state machine, single in-flight resync at a
   time.

---

## 6. Acceptance-criteria coverage

| AC | Covered by | Notes |
|---|---|---|
| #1 design documented | This doc | ✓ |
| #2 ≤5 s divergence under 2× peak | §2.4 resync via `LoadTranscript` | A 500-line JSONL parses in <50 ms; status-bar tick is 1 s; 500-ms debounce + ~100 ms read = ≤2 s end-to-end on the qba-sized session. |
| #3 ≤5 s indicator after first drop | §2.6 status-bar segment | Already shipped (QUM-681 tick = 1 s). Banner fires post-debounce ≤ 500 ms after confirmed gap. |
| #4 clean exit at any point | §2.7 `setTurnState(TurnIdle)` on gap | Independent of resync success. Audit verifies quit paths don't gate on `turnState`. New unit test in §4.5. |
| #5 tests cover drop signal + resync equivalence | §4.1–§4.6 | All four issue scenarios covered. |

---

## 7. Open items / known limitations

- **SessionID identity post-handoff.** `SessionBackend.SessionID()`
  returns the *current* runtime's session. Across a session restart
  (handoff) the session JSONL switches files. Resync from the wrong
  file would mis-render. **Resolution:** Read the session ID *at the
  moment of resync*, not cached at gap-detection time; the existing
  `tuiadapter.SessionID()` already follows `Observe()` swaps.
- **Non-TUI subscribers.** The activity-ring subscriber and any future
  log writer subscribers don't need resync (they are append-only
  consumers and the on-disk activity.ndjson is independent). The
  Seq field they receive is harmless to ignore.
- **What if the session JSONL itself is incomplete?** The wire log
  flusher (`internal/backend/claude/wirelog.go`) is best-effort. If a
  drop coincides with an unflushed write, resync may rebuild a
  transcript shorter than the in-memory state. Acceptable — the
  resync banner explicitly tells the user "recovered N messages",
  and N may be less than the M originally lost. Document in the
  banner copy: "resynced from session log (N events recovered)".
- **Ctrl+R conflict with future persistence work.** Noted in §2.5.
  Open for forge / dmotles to confirm or pick an alternative chord.

---

## 8. References

- `internal/runtime/eventbus.go` — Publish, DropTelemetry,
  trySendWithYield
- `internal/tuiruntime/tuiadapter.go` — subscription, DropTelemetry
  mirror, Observe
- `internal/tui/replay.go` — `LoadTranscript`, `scanTranscript`
- `internal/tui/viewport.go:610` — `SetMessages`
- `internal/tui/statusbar.go:262` — `SetEventDrops`
- `internal/tui/app.go:2541` — `refreshDropTelemetry`
- `internal/agentloop/activity.go` — (NOT the resync source; clarified
  in §1)
- `internal/memory/sessionlog.go:36` — `SessionLogPath`
- `docs/designs/tui-structural-rewrite-plan.md` — S3/S4/S5/S6 plan
- QUM-472 — silent-drop telemetry (predecessor)
- QUM-667 — viewport render perf (parallel)
- QUM-681 — drop-telemetry status-bar segment (predecessor; reused)

---

## Addendum (impl notes — 2026-06-03)

- **§2.5 correction.** `Ctrl+R` is already bound to reverse-history-search in
  the input panel (QUM-410, `app.go:188,368,2261`). The shipped binding is
  **`Ctrl+L`** (terminal redraw mnemonic). Reverse-search precedence at the
  top of the KeyPressMsg switch (`app.go:364`) prevents conflict.
- **MessageSink interface deferred (YAGNI).** AppModel calls
  `viewport.SetMessages` / `AppendStatus` directly. The 2-method interface
  can be extracted in B4 S3 when ChatList lands (≤20 LOC mechanical).
- **lastSeq baseline NOT reset on resync completion** per §5 hotspot #1
  option b: any "lost" events during resync are by definition in the
  session JSONL the resync just read.
- **Debug-only gap injection seam**: `SPRAWL_DEBUG_GAP_INJECT=N` (env var
  read by `tuiruntime.subscribe`) synthesizes one
  `EventDropDetectedMsg{Missing:N}` at the second event of the session.
  Used by the `viewport-resync` e2e matrix row. Test-only — not a public
  surface.
