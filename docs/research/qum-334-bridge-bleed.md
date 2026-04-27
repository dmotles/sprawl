# QUM-334 — Bridge-event bleed into child agent viewport

**Status:** Design / research — not yet committed to an implementation.
**Author:** ghost (research agent)
**Date:** 2026-04-27
**Linear:** [QUM-334](https://linear.app/qumulo-dmotles/issue/QUM-334)
**Related:** QUM-279 (multi-agent observation), QUM-323 (drain-commit), QUM-331 (respawn pollution), QUM-332 (child transcript tail)

---

## 1. Problem recap

`tui.AppModel` owns one `ViewportModel` (`internal/tui/app.go:46`, field `viewport`). The bridge (`internal/tui/bridge.go`) is wired to weave's Claude subprocess and emits `AssistantTextMsg`, `ToolCallMsg`, `SessionResultMsg`, `SessionErrorMsg`, `UserMessageSentMsg`, `SessionInitializedMsg` — all consumed in `AppModel.Update` and all currently calling `m.viewport.AppendXxx(...)` unconditionally.

When the user cycles to a non-root agent (`Ctrl+N`), `AgentSelectedMsg` (app.go:626) snapshots the current `viewport.GetMessages()` into `agentBuffers[weave]` and replaces the viewport content with the child's transcript (now populated since QUM-332). But the bridge handlers keep arriving and append onto whatever is currently in `viewport.messages` — which is finn's transcript. Result: weave's `peek/diff/validate/merge` chatter shows up grafted onto finn's session.

The bridge never had per-agent gating. Pre-QUM-332 the child viewport was a static "Observing X..." status entry, so the corruption was invisible (the appends still happened — they just landed on top of a one-line banner the user couldn't tell apart from drift).

### 1.1 Scope of the bleed

The triage notes only `AssistantTextMsg`/`ToolCallMsg`/`SessionResultMsg` — but the same shared-viewport pattern means **any** `viewport.AppendXxx` fired while `observedAgent != rootAgent` bleeds. Searching `internal/tui/app.go`:

| Line | Call | Origin |
|------|------|--------|
| 326 | `AppendStatus("/handoff dispatched ...")` | `InjectPromptMsg` (weave-only path, but still mutates the live viewport) |
| 342 | `AppendStatus("inbox: draining ...")` | `InboxDrainMsg` (weave queue) |
| 348 | `AppendUserMessage(msg.Prompt)` | `InboxDrainMsg` |
| 358 | `AppendUserMessage(msg.Text)` | `SubmitMsg` (gated by input disabled — safe; input is disabled while observing non-root, see L656) |
| 381 | `AppendAssistantChunk(msg.Text)` | `AssistantTextMsg` ★ |
| 388 | `AppendToolCall(...)` | `ToolCallMsg` ★ |
| 399, 401, 403, 405 | `AppendAssistantChunk` / `FinalizeAssistantMessage` / `AppendError` / `AppendStatus` | `SessionResultMsg` ★ |
| 432 | `AppendError(msg.Err.Error())` | `SessionErrorMsg` (idle path) |
| 455 | `AppendStatus("Session restarting ...")` | `SessionRestartingMsg` |
| 537/539 | `AppendStatus("— New session started ...")` | `RestartCompleteMsg` |
| 574 | `AppendStatus("inbox: N new message(s) for weave")` | `AgentTreeMsg` (per-tick) |
| 604 | `AppendStatus("inbox: new message from <X>")` | `InboxArrivalMsg` |

Items marked ★ are the active streaming path. The rest are infrequent but exhibit the same bleed. **Any chosen fix must cover all of these, not just the bridge handlers** — otherwise an inbox-arrival or restart banner mid-observation still corrupts the child view.

`UserMessageSentMsg` (L363) does not directly touch the viewport, but the QUM-323 `pendingDrainIDs` commit logic is fine because user input is gated to root only (L654–658). Verified.

`SessionInitializedMsg` (L289) only updates the status bar — already root-scoped state, no viewport mutation. Safe.

### 1.2 Streaming chunk-merge state — exactly where it lives

`ViewportModel.AppendAssistantChunk` (`internal/tui/viewport.go:156-166`):

```go
if n := len(m.messages); n > 0 && m.messages[n-1].Type == MessageAssistant && !m.messages[n-1].Complete {
    m.messages[n-1].Content += text
} else {
    m.messages = append(m.messages, MessageEntry{Type: MessageAssistant, Content: text})
}
```

Critically, the "pending assistant message" is **just** the last entry of `m.messages` with `Complete == false`. There is no separate pending-message field — it's a flag on the slice. `FinalizeAssistantMessage` (viewport.go:169) flips that flag. `HasPendingAssistant` (viewport.go:210) reads it.

This matters for option-C analysis below: replicating the chunk-merge logic against a `[]MessageEntry` stored anywhere else (e.g. `AgentBuffer.Messages`) is a five-line copy of the same predicate, not a non-trivial state-machine port.

### 1.3 AgentBuffer today

```go
type AgentBuffer struct {
    Messages   []MessageEntry  // copy of viewport.messages on cycle-away
    AutoScroll bool
    SessionID  string          // for child transcript invalidation, QUM-332
}
```

Population: `agentBuffers[name]` is written in two places:

1. `AgentSelectedMsg` (app.go:633): swap-out save of the just-deselected agent (copies `viewport.GetMessages()`).
2. `ChildTranscriptMsg` (app.go:693–699): non-root only — caches the polled transcript so the next 2s tick doesn't re-clobber.

`agentBuffers[weave]` is therefore only written when weave is being deselected. While observing a child, weave's buffer is frozen at the moment of cycle-away. **Bridge events arriving during observation are not durably stored anywhere accessible from the AppModel state** — they currently land in the live viewport (which holds finn's transcript) and get implicitly discarded when `AgentSelectedMsg` cycles back to weave (because the cycle-away saves finn's viewport, then `SetMessages(agentBuffers[weave].Messages)` reverts to the stale snapshot).

Wait — re-reading L633: on cycle-back to weave, the sequence is:

1. Save current viewport (finn's transcript + weave's bleed) into `agentBuffers[finn]` ← **persists the pollution into finn's cache!**
2. Replace viewport with `agentBuffers[weave].Messages` (the snapshot from the moment of cycle-away to finn).
3. From this point on, bridge events append to weave's transcript (correctly) again.

So the bug actually has **two** failure modes:

* **Mode A (visible):** while observing finn, weave's events are rendered as if they were finn's.
* **Mode B (latent):** on cycle-back, finn's cached `AgentBuffer.Messages` now contains weave's bleed. The next time the user cycles to finn, the polluted cache is restored. The next `ChildTranscriptMsg` tick (2s later) does `SetMessages(msg.Entries)` (app.go:692) which replaces with a fresh disk read — **pollution self-heals on the first successful poll**. So mode B is real but transient.

Self-healing of mode B is fortunate and reduces the urgency of certain fixes, but should not be relied on (an empty child transcript triggers the "Waiting for X..." banner branch at L702–711, which preserves the polluted cache further? — no, L708 also calls `SetMessages` to a synthetic banner, so even the empty branch self-heals).

### 1.4 Bridge wiring

`Bridge` (`internal/tui/bridge.go`) holds a single `events <-chan *protocol.Message` for one `host.Session` (weave's). There is no per-agent demultiplexing. Children do not have bridges (per Out-of-Scope #1). `WaitForEvent` is a one-shot pull from the channel, re-armed by each handler that returns `m.bridge.WaitForEvent()`. **If a handler does not re-arm, the stream stalls.** This is important for option D (drop-on-floor) below — the re-arm must still happen.

---

## 2. Acceptance criteria revisited

Restated for design clarity:

1. **Render isolation:** while `observedAgent != rootAgent`, no bridge-origin `viewport.AppendXxx` may execute against the rendered viewport.
2. **Global UI continuity:** `setTurnState`, `statusBar.SetTurnCost`, `input.SetDisabled` continue to fire on bridge events regardless of observation. Input bar and status reflect weave's reality.
3. **No bridge stall:** every handler must still call `m.bridge.WaitForEvent()` at the same cadence. Skipping the re-arm freezes the bridge.
4. **No data loss:** every event during the observation window must be visible on cycle-back to weave, with correct ordering and intact mid-stream assistant chunks.
5. **Mid-stream resilience:** cycling away and back during a streaming assistant turn must leave the assembled message correct (no torn chunks, no duplicate finals).
6. **Memory bound:** events accumulated during an arbitrarily long observation window must not grow without bound. (Implicit; today's code has no bound either, but options that buffer in memory inherit this concern.)

---

## 3. Candidate approaches

### (A) Two `Viewport` instances — one per agent, swap on focus

**Sketch.** `AgentBuffer` gains a `vp ViewportModel` field. Bridge handlers always operate on `agentBuffers[rootAgent].vp`. `AppModel.View` renders `agentBuffers[observedAgent].vp.View()`. `AgentSelectedMsg` becomes a pointer-swap: no copy of `[]MessageEntry`.

**Concrete code paths.**

* `internal/tui/app.go:32-42` — extend `AgentBuffer` with `vp ViewportModel`.
* `internal/tui/app.go:46` — `AppModel.viewport` either disappears (renderer reads from `agentBuffers[observedAgent].vp`) or becomes a thin pointer/alias.
* All bridge handlers (`app.go:379-410`, plus 432/455/537/574/604) → `m.agentBuffers[m.rootAgent].vp.AppendXxx(...)`.
* `AgentSelectedMsg` (app.go:626-680) — drops `GetMessages`/`SetMessages` swap; instead lazily creates `AgentBuffer{vp: NewViewportModel(&m.theme)}` if missing.
* `View()` (app.go:725) — `vpView := vpBorder.Render(m.agentBuffers[m.observedAgent].vp.View())`.
* `resizePanels` (app.go:858) — must call `SetSize` on **every** viewport in `agentBuffers`, not just one. Memory bound: O(N agents × viewport state).
* `ChildTranscriptMsg` (app.go:682-718) — `m.agentBuffers[msg.Agent].vp.SetMessages(msg.Entries)` instead of mutating the shared viewport.
* `handleViewportSelectKey` (app.go:905) — must dispatch to the active agent's `vp`.

**State / streaming concerns.**

* Streaming chunk state is preserved naturally — each agent's `vp.messages` has its own pending-assistant tail.
* Polling cadence (2s `defaultChildTranscriptTick`, app.go:125) interacts cleanly: child polling writes to child `vp`, bridge writes to weave `vp`. No cross-talk.
* Memory: each `ViewportModel` carries a `bubbles/v2/viewport.Model` (scroll state, content cache, soft-wrap buffers). For N agents, this is N viewports. With <=10 agents typical, fine. Memory cap is enforced by `ReplayMaxMessages = 500` (replay.go:15) on the message side.

**Edge cases.**

* **Rapid focus switches:** trivially correct — pointer swap is atomic in the single-goroutine Bubble Tea model.
* **Agent retirement mid-stream:** if a child agent disappears from the tree, its `agentBuffers[name]` entry leaks unless cleaned up. Cleanup must happen in `AgentTreeMsg` (app.go:565). Today there's no cleanup at all (`agentBuffers` only grows). **Pre-existing leak; not made worse by A but worth fixing alongside.**
* **Root vs child semantics:** The root viewport always exists; child viewports are created lazily on first selection. No special-casing needed once the lazy-init lands.
* **Resize storm:** `resizePanels` must iterate all viewports; a 100ms resize drag fires many WindowSizeMsgs. Each one re-layouts every viewport. With 10 agents this is fine; with 100 it could hitch. Bound is the same as today's tree-poll cadence — not a real concern.
* **Resume / restart paths:** `RestartCompleteMsg` (app.go:533) calls `m.viewport.SetMessages(nil)`. Under A this becomes `m.agentBuffers[m.rootAgent].vp.SetMessages(nil)` — fine.
* **Selection state (QUM-281):** Per-agent select mode now has per-agent state, which is arguably *better* UX (selection survives observation cycle).

**Cost.** ~20 callsites touched. Most are mechanical (`m.viewport.X` → `m.agentBuffers[m.rootAgent].vp.X` for bridge-origin; → `m.agentBuffers[m.observedAgent].vp.X` for user-origin). The conceptual model is clean and maps to the user's mental model ("each agent has its own view"). Existing tests that use `m.viewport.GetMessages()` need updating.

### (B) Buffer-as-source-of-truth — viewport becomes a stateless renderer

**Sketch.** `AgentBuffer` holds the canonical `[]MessageEntry`, plus `AutoScroll`, plus the bubbles-viewport scroll/cell state migrated up. `ViewportModel` keeps no `messages` field; `View()` takes a `[]MessageEntry` argument (or a buffer pointer) and renders. `AppendAssistantChunk` etc. become pure functions over `[]MessageEntry` (`func MergeAssistantChunk(msgs []MessageEntry, text string) []MessageEntry`). Bridge handlers call the merge functions on `agentBuffers[rootAgent].Messages`. The renderer is invoked via `m.viewport.Render(agentBuffers[observedAgent])` on each View tick.

**Concrete code paths.**

* `internal/tui/viewport.go:55-68` — strip `messages`, `selection`, `autoScroll`, `hasNewContent` from `ViewportModel`. They move into `AgentBuffer` (or a new `AgentViewState` struct).
* All `Append*` methods (viewport.go:146-206) — convert to package-level pure functions taking `*AgentBuffer`.
* `renderMessages` (viewport.go:296-333) — takes the buffer as a parameter.
* `ChildTranscriptMsg` (app.go:682-718) — writes directly to `agentBuffers[msg.Agent]`, no viewport interaction.
* All bridge handlers — write to `agentBuffers[m.rootAgent]`, never to a viewport.
* `View()` (app.go:725) — picks the observed buffer and renders.
* The `bubbles/v2 viewport.Model` (`vp viewport.Model`, viewport.go:57) — its scroll/cursor state must also become per-agent. Simplest: keep one bubbles viewport instance, but reset its content + restore its yOffset on each agent switch. Cleanest: per-agent bubbles viewport (which is essentially what option A already does — and now we've recovered A by another path).

**State / streaming concerns.**

* Same chunk-merge logic, lifted to a pure function. No semantic change.
* Polling: child polling writes to its buffer; bridge writes to weave's buffer. No cross-talk.
* Memory: same as A.
* `bubbles/v2 viewport.Model` per-agent state (yOffset, contentLines cache) — must be preserved across switches or scroll position resets. If we keep one bubbles viewport and only swap content, **scroll position is lost on every switch**, which is a regression vs A.

**Edge cases.** Mostly identical to A. The big difference is invasiveness: every test that touches `viewport.AppendStatus` or `viewport.GetMessages` needs rewriting against the new pure-function API. `viewport_test.go` (and `messages_test.go`, `app_test.go`, `app_child_transcript_test.go`, `replay_test.go`, etc.) all anchor on the current API.

**Cost.** Highest of the three. The API shift is large and ripples across the test suite. Conceptually it's the cleanest end state — but it doesn't deliver value beyond what A delivers, and A has lower migration risk. **B is essentially A + a structural rewrite the codebase doesn't need yet.**

### (C) Hybrid — gate the rendered viewport, mirror to weave's buffer with chunk handling

**Sketch.** Introduce a helper:

```go
func (m *AppModel) routeBridgeAppend(fn func(target appendTarget)) {
    if m.observedAgent == m.rootAgent {
        fn(viewportTarget{vp: &m.viewport})
        return
    }
    fn(bufferTarget{buf: m.agentBuffers[m.rootAgent]})
}
```

`appendTarget` is an interface with `AppendAssistantChunk`, `AppendToolCall`, `FinalizeAssistantMessage`, etc. The buffer implementation re-implements chunk-merge against `buf.Messages`. On `AgentSelectedMsg` cycle-back to weave, `m.viewport.SetMessages(agentBuffers[weave].Messages)` already restores correctly because the buffer was kept up to date.

**Concrete code paths.**

* New file `internal/tui/append_target.go` (~80 lines) — interface + viewport adapter + buffer adapter, with the chunk-merge predicate duplicated for the buffer side.
* Bridge handlers (app.go:379-410, 432, 455, 537, 574, 604) — wrapped with `routeBridgeAppend`. `setTurnState`/`statusBar`/`input.SetDisabled` calls stay outside the gate (per AC #2).
* `AgentSelectedMsg` (app.go:626-680) — no change to the cycle-away save (it already snapshots viewport, but for weave's buffer we want the *buffer side* not the viewport side; need to add a guard so cycling weave→child uses the live viewport snapshot, while cycling child→weave uses the up-to-date buffer that's been receiving mirrored writes). This is the trickiest part.

**State / streaming concerns.**

* **The hybrid has a subtle ordering problem.** Consider: weave is mid-stream, viewport has a pending `Complete=false` assistant entry. User cycles to finn. Cycle-away saves that pending entry into `agentBuffers[weave].Messages` (L633). Bridge keeps streaming → mirror writes new chunks into `agentBuffers[weave].Messages` (chunk-merge: extends the last incomplete entry). User cycles back. Cycle-away saves finn's viewport into `agentBuffers[finn]`. Then `m.viewport.SetMessages(agentBuffers[weave].Messages)`. The pending-assistant flag (`Complete=false` on last entry) is preserved in the slice, so the next bridge chunk arriving at the live viewport correctly extends it. **OK — this works because pending state IS in the slice, not separate.**
* But: while observing finn, the bridge mirror writes go to the buffer, not the viewport. When cycling back, we restore `viewport.messages = buffer.Messages`. The bubbles `viewport.Model` content is recomputed by `renderAndUpdate` (viewport.go:286) which `SetMessages` already calls. **Cycle-back is correct.**
* Polling cadence: child polling at 2s (defaultChildTranscriptTick) writes via `SetMessages` to the live viewport. Bridge mirror writes to `agentBuffers[weave]`. They don't collide.
* Memory: `agentBuffers[weave].Messages` grows during observation. Bound by the cycle-back (which truncates to viewport's `ReplayMaxMessages` — actually nothing truncates today; `Append*` is unbounded). Same memory profile as observing weave directly.

**Edge cases.**

* **Rapid cycle while streaming:** weave→finn→weave→finn→weave during a single assistant message. Each cycle-away to finn snapshots an in-flight pending entry; each cycle-back restores from the buffer that's been getting mirror-merged. Since both the snapshot and the mirror operate on the same `Complete=false` last-entry convention, they converge — but only if the cycle-away `GetMessages()` returns a *deep enough* copy that subsequent buffer-side writes don't mutate the saved snapshot through aliasing.

  Reading `GetMessages` (viewport.go:228) — it does a slice copy, but `MessageEntry` is a value type with no slice/map fields, so the copy is fully independent. **Safe.** But: when we then set `agentBuffers[weave] = &AgentBuffer{Messages: copy}`, the buffer-target append helper extends the slice in place. The viewport is no longer aliased to it. Good.

* **Agent retirement mid-stream:** orthogonal, same as A.
* **Mid-result-stream (`SessionResultMsg`):** the `if !msg.IsError && ... && !m.viewport.HasPendingAssistant()` check at L398 reads viewport state. Under C while observing a child, that branch needs to check buffer state instead. A small bug surface; a `target.HasPendingAssistant()` interface method covers it.
* **Tool-call ordering between observation and bridge:** none — bridge events and child-transcript polls write to disjoint stores.

**Cost.** Medium. ~80 lines new code, ~15 callsites wrapped. Duplicates chunk-merge predicate (viewport.go:157 logic) in the buffer adapter. Lower invasiveness than A or B but introduces a small abstraction (the target interface) that future `Append*` additions must remember to use — easy to forget and reintroduce the bleed with a new event type.

### (D) Disk-replay-on-cycle-back — drop bridge appends during observation, replay weave's session log

**Sketch.** When observing a child, bridge handlers skip the viewport write entirely (no in-memory mirror). On cycle-back to weave, instead of restoring from `agentBuffers[weave]`, run the same `LoadChildTranscript`-style replay against weave's own Claude session JSONL log to reconstruct the transcript fresh.

**Concrete code paths.**

* Bridge handlers — gate-and-skip behind `if m.observedAgent == m.rootAgent`, but always re-arm `WaitForEvent` (re-arm is non-negotiable per §2 #3).
* `AgentSelectedMsg` cycling back to weave — instead of using `agentBuffers[weave].Messages`, dispatch a `loadChildTranscriptCmd(weave)`-equivalent that re-reads weave's session log.
* Need to know weave's session log path: `cmd/enter.go` already wires `homeDir` (app.go:1117). Weave's worktree + session ID are available from the bridge (`m.bridge.SessionID()`).

**State / streaming concerns.**

* **Streaming fidelity is the killer.** Claude session JSONL records assistant messages **at completion**, not as chunks. While bridge streams chunks of "I'll peek the diff..." in real time, the session log contains nothing until the message finalizes. If the user cycles back mid-stream, the disk replay shows the transcript up to the last completed turn — the current in-flight stream is invisible until it lands.
* Worse: after cycle-back, the next bridge chunk arrives expecting a pending assistant message in `viewport.messages`, but disk replay re-built the transcript without one. `AppendAssistantChunk` would create a *new* assistant entry from the chunk forward, splitting the message into "before-cycle (from disk)" + "from-chunk-onward (live)" — **torn streaming state** (violates AC #5).
* Fixable by accumulating bridge events into a side-buffer during observation and replaying them after the disk read. But now we have option C with extra steps and a disk read.
* Polling cadence: irrelevant — the disk read happens once on cycle-back.
* Memory: zero in-memory event accumulation; bounded by `ReplayMaxMessages`.

**Edge cases.**

* **EOF / restart during observation:** weave's bridge can EOF mid-observation (auto-restart at app.go:417). If we relied on disk replay, the restart-banner sequence (`SessionRestartingMsg`, `RestartCompleteMsg`) wouldn't appear in the transcript at all (those banners are TUI-side `AppendStatus`, not Claude session events). Cycle-back would show a transcript that ends at the pre-EOF turn with no indication a session boundary occurred. UX regression.
* **Cost banner:** `Completed in Xms, cost $Y` (L405) is also TUI-side, not in the session log. Lost on cycle-back.
* **Inbox banners:** all `AppendStatus` calls for inbox arrivals are pure TUI annotations. None survive.

**Verdict.** D delivers the simplest in-memory story (no mirror, no per-agent viewports) at the cost of mid-stream fidelity and TUI-only banner loss. The TUI annotations are the hard part: they're not events, they're decorations. **Reject** unless we're willing to lose them — and the user explicitly wants the cost banner ("Completed in Xms, cost $Y") visible on cycle-back as part of the observation use case.

### (E — speculative, mentioned for completeness) Bridge-side rerouting

What if the bridge itself wrote events into a per-agent in-memory ring (keyed by `m.rootAgent`), and the AppModel stopped caring about viewport state for bridge events at all? The viewport renders by subscribing to the active ring.

Effectively this is option B with the buffer ownership pushed one layer further from the model. It saves nothing over B and complicates the Bubble Tea single-goroutine model (rings shared between bridge goroutine and update goroutine need synchronization). **Not pursued.**

---

## 4. Comparison matrix

| Criterion | A: Per-agent Viewport | B: Buffer-as-truth | C: Gate + mirror | D: Disk replay |
|---|---|---|---|---|
| Render isolation (AC #1) | ✅ trivial | ✅ trivial | ✅ via gate | ✅ via gate |
| Global UI continuity (AC #2) | ✅ unchanged | ✅ unchanged | ✅ unchanged | ✅ unchanged |
| Bridge re-arm (AC #3) | ✅ unchanged | ✅ unchanged | ✅ unchanged | ⚠ must remember |
| No data loss (AC #4) | ✅ separate vp | ✅ separate buf | ✅ mirrored buf | ❌ TUI banners lost |
| Mid-stream fidelity (AC #5) | ✅ per-agent pending tail | ✅ per-agent pending tail | ✅ pending tail in slice | ❌ torn streams |
| Memory bound | O(agents × vp state) | O(agents × buf state) | O(observation window) | O(1) |
| Code surface | ~20 callsites + view/resize | full vp rewrite | ~15 callsites + helper | ~10 callsites + replay |
| Test churn | medium | high | low | low |
| Adds future-proofing for streaming children | ✅ each child can stream | ✅ ditto | ⚠ refactor needed | ❌ |
| Risk of reintroducing bleed via new event type | low (new viewports get added with new agents) | low | **high** (must remember to call helper) | medium |
| Existing tests broken | yes (model shape change) | yes (API change) | minimal | minimal |

---

## 5. Recommendation: **(A) Per-agent Viewport**, with caveats

**Why A over C** (the "small change"):

1. **Conceptual integrity.** "Each agent has its own viewport" matches the user's mental model exactly. Cycling agents = swapping which one is rendered. There is no "where does this event go?" question for any current or future event type — bridge events go to weave's viewport, child polling goes to that child's viewport, full stop.
2. **Future-proofs streaming children.** QUM-279/QUM-332 added child *observation*. The next logical step (Out-of-Scope #1: streaming child agents' live events) requires per-agent event sinks. A is half the work for that future feature; C and D would need a second refactor to support it.
3. **No "remember the helper" tax.** The §1.1 audit found 11 `AppendXxx` callsites; the next added event handler will be the 12th. C requires the author to remember to use `routeBridgeAppend` — easy to miss. A has no such gotcha because the wrong target literally won't compile (the model field that *was* `viewport` is gone; you have to pick an agent).
4. **Selection mode (QUM-281) per-agent is a UX win.** Today, exiting select mode by cycling agents loses your selection. With A, selection survives as part of the per-agent viewport state.

**Why not B.** B's end state is similar to A but the migration touches every test file. The marginal value over A (turning `ViewportModel` into a pure renderer) is real but unrelated to fixing this bug. Defer until there's an independent reason.

**Why not C.** C is the smallest diff and is honestly defensible if the team is risk-averse and wants the minimum patch. But the "remember to gate" tax compounds with every future event type — and QUM-329 just landed `HandoffRequestedMsg`, QUM-323 added `InboxDrainMsg`, QUM-311 added `InboxArrivalMsg`. The TUI message surface is still expanding. Building a structural fix now that prevents the next bleed-by-default beats reactive gating.

**Why not D.** Loses TUI-side banners and breaks streaming fidelity (AC #5 hard-fails).

### Caveats / required follow-ups for A

1. **AgentBuffer cleanup on retirement.** Today `agentBuffers` only grows. With per-agent viewports inside it, the leak gets more expensive. Add a sweep in `AgentTreeMsg` (app.go:565) that drops buffers for agents no longer in `msg.Nodes` (and not equal to `m.rootAgent`).
2. **Resize must iterate.** `resizePanels` needs to call `SetSize` on every cached viewport. Trivial loop; mention in tests.
3. **Lazy init.** First `AgentSelectedMsg` for a new agent must construct the viewport at the current size. Encapsulate in a helper `m.viewportFor(name) *ViewportModel`.
4. **Test refactor.** `app_test.go` and `viewport_test.go` test against `m.viewport` directly. Migrate via a `m.viewportFor(rootAgent)` accessor.
5. **Audit non-bridge `AppendXxx` callers.** §1.1 lists them. Inbox banners (L574, L604) and restart banners (L455, L537/539) are *weave-only* events; they should target `agentBuffers[weave].vp`. The "/handoff dispatched" banner (L326) is also weave-only. None should target the *observed* agent's viewport.
6. **The `SubmitMsg` user echo (L358) is rendered to the live viewport.** Under A this should target weave's viewport (input is gated to root, so this is functionally identical, but should be explicit so a future "child-input" feature doesn't surprise).
7. **`AppModel.PreloadTranscript` (app.go:813)** writes to `m.viewport`. Becomes `agentBuffers[rootAgent].vp.SetMessages(...)`.

### Test plan for A

* Unit: `AssistantTextMsg` arrives while `observedAgent == finn` → rendered viewport bytes equal finn's transcript; `agentBuffers[weave].vp.GetMessages()` shows the weave chunk appended.
* Unit: cycle weave-mid-stream → finn → weave; assert weave's viewport has a single coherent assistant message with the chunks concatenated.
* Unit: inbox arrival while observing finn → finn's viewport unchanged; weave's viewport has the banner.
* Existing E2E: `make test-notify-tui-e2e` should still pass (weave-only path).
* New E2E (optional): spawn child, send weave a message that triggers tool use, cycle to child, cycle back, assert weave's transcript is intact.

---

## 6. Open questions for the implementer

1. **Should weave's viewport scroll position survive cycle-away?** A naturally preserves it. C does only if `agentBuffers[weave]` also stores `autoScroll` + bubbles-vp `yOffset`. Worth confirming the desired UX.
2. **Activity panel (`activity.SetAgent`) is already per-agent observed.** Does anything similar need to happen for the *selection* state (QUM-281)? My read: yes, but it's a free bonus under A.
3. **`HasPendingAssistant` (used at app.go:398) currently reads the live viewport.** Under A, this should read `agentBuffers[rootAgent].vp.HasPendingAssistant()`. Easy to miss.
4. **Should banner messages for weave (e.g. "inbox: new message from X") *also* echo into the currently-observed agent's viewport** so the user never misses one? My recommendation: no — they're weave's events, they belong to weave's view. The status-bar / tree-row badge already conveys urgency without leaking content. But this is a UX call.

---

## 7. Reflections (research-honest notes)

* **Surprise:** the "pending assistant" state isn't a separate field — it's just `Complete=false` on the last slice entry. That makes options C and B much cheaper than the issue text implied (the chunk-merge logic is a five-line predicate, not a state machine). It also means the latent mode-B pollution self-heals on the first child-transcript poll, which de-risks all four options.
* **Surprise:** the bleed isn't only the three handlers in the issue — 11 `Append*` callsites are affected. Any fix that addresses only the three named handlers leaves a smaller version of the same bug present.
* **Open question I'd investigate next:** how does `bubbles/v2 viewport.Model` behave when its content is replaced via `SetContent` mid-render? In particular, does `yOffset` snap back to 0 or persist? This affects how cleanly A preserves scroll position across cycles. Reading the bubbles source would close this; I didn't follow that thread.
* **Open question:** is `agentBuffers` ever read by another goroutine? It's only touched in `Update`, which is single-threaded by Bubble Tea, so no. But if QUM-279's future "stream live child events" ever lands a goroutine writing into a child's buffer, this becomes a concurrency concern. Worth flagging in the implementation PR.
* **What I'd do with more time:** prototype option A end-to-end in a branch and measure: (1) test-suite churn, (2) memory delta with 10 agents observed, (3) whether scroll-position preservation falls out for free or needs explicit per-agent yOffset save. The doc above is a paper analysis; one prototype would settle the residual unknowns.
