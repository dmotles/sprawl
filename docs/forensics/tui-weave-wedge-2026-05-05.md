# Weave TUI wedge investigation — 2026-05-05

**Scope:** live-session forensics only. No code changes.

> **Status: historical.** Captured while the unified runtime was a
> `SPRAWL_UNIFIED_RUNTIME=1` opt-in alongside legacy `tui.Bridge` /
> `agentloop.Runner`. QUM-400 retired the legacy paths, the env gate, and
> renamed `runtime_launcher_unified.go` → `runtime_launcher.go`. References
> to deleted symbols / files reflect code as it stood at the investigation
> date.

---

## Snapshot

Observed on the currently-running `sprawl enter` TUI session in tmux window `0:1.1`.

### Runtime state observed

- `weave` process is still alive (`sprawl enter`, PID 678) and the Claude subprocess is also alive (`claude`, PID 1990), so this is **not** a dead-process failure.
- The TUI pane is still rendering `weave (streaming)` and the status bar shows `Streaming...`, but the runtime activity log stopped advancing after a terminal error.
- `ghost` completed successfully, but its completion message is still stuck in weave's pending queue and has not been drained into a new turn.

### Concrete evidence

1. **Last weave activity ends in a terminal tool-use error, then stops.**
   - `.sprawl/agents/weave/activity.ndjson:949-954`
   - Last entry:
     - `2026-05-05T15:54:42Z result error stop=tool_use turns=3`
   - No later `session_state_changed: idle`, `init`, assistant text, or tool events were appended.

2. **The TUI kept thinking weave was still streaming.**
   - tmux capture showed:
     - viewport banner `Interrupt sent — waiting for turn to end`
     - status bar `Streaming...`
     - input bar showing a queued-message indicator rather than accepting a fresh turn
   - Five seconds later the capture was materially unchanged, and `.sprawl/agents/weave/activity.ndjson` still had the same line count.

3. **A completed child message is stranded in weave's queue.**
   - `.sprawl/agents/weave/queue/pending/0000000244-async-0123fb70-b9de-48eb-9b49-22a94bdac59b.json:1-14`
   - This is ghost's `[COMPLETE]` report from `2026-05-05T15:54:42Z`.
   - Because it remains in `pending/`, the normal drain path never re-ran.

4. **tmux itself was not wedged.**
   - `pane_in_mode=0`, `pane_current_command=sprawl`; no copy-mode or pane-mode interference.

---

## Most likely culprit

### 1. Interrupt terminal events are mapped to the wrong TUI message type

This is the strongest match for the live symptoms.

#### Current code path

- User presses `Esc` during an active turn:
  - `internal/tui/app.go:498-505`
  - AppModel appends `Interrupting...` and calls `m.bridge.Interrupt()`.
- The unified runtime later emits a terminal interrupt event when the turn actually ends:
  - `internal/runtime/turnloop.go:180-186`
  - If the turn was interrupted, the terminal event is `EventInterrupted`, not `EventTurnCompleted`.
- The unified TUI adapter maps that terminal runtime event to `tui.InterruptResultMsg`:
  - `internal/tuiruntime/tuiadapter.go:151-152`
- But AppModel treats `InterruptResultMsg` as only an **interrupt-dispatch acknowledgement**:
  - `internal/tui/app.go:964-973`
  - It only appends `Interrupt sent — waiting for turn to end`
  - It does **not** set `TurnIdle`
  - It does **not** finalize the assistant message
  - It does **not** re-arm `WaitForEvent()`

#### Why this matches the live wedge

If the terminal interrupt lands as `InterruptResultMsg`, the TUI stays in `TurnThinking`/`TurnStreaming`, so:

- the status bar keeps saying `Streaming...`
- fresh user input is queued instead of sent immediately (`internal/tui/app.go:608-619`)
- `peekAndDrainCmd` never runs again because it is gated on `m.turnState == TurnIdle` (`internal/tui/app.go:1077-1080`)
- pending async queue items, like ghost's completion report, remain stranded in `pending/`

That is almost exactly the state observed in the live session.

#### Supporting test evidence

The current behavior is intentionally encoded in tests:

- `internal/tuiruntime/tuiadapter_test.go:318-361` expects `EventInterrupted` to surface as `InterruptResultMsg`
- `internal/tui/app_test.go:3023-3061` only checks that `InterruptResultMsg` appends a status line; it does not assert any terminal-turn cleanup

That test shape suggests a model/adapter contract bug rather than a random runtime failure.

---

## Secondary plausible culprit

### 2. EventBus silent drops can also strand the TUI in streaming state

The unified runtime EventBus is a known silent-loss risk.

- `internal/runtime/eventbus.go:92-104`
- Publish is non-blocking and drops events when a subscriber buffer is full.
- The TUI adapter subscribes with a 64-event buffer (`internal/tuiruntime/tuiadapter.go:23-25`, `internal/tuiruntime/tuiadapter.go:55-60`).
- The activity subscriber also uses a 64-event buffer (`internal/supervisor/runtime_launcher_unified.go:157-181`).

This was already called out in the audit note:

- `docs/research/unified-runtime-messaging-audit.md:217-227`
- Filed as QUM-472 there.

Why it matters here:

- if the TUI subscriber drops the terminal `EventTurnCompleted` / `EventInterrupted`, AppModel can miss the only message that would return it to idle
- the activity subscriber may still show enough evidence to make the backend look healthy while the TUI state machine is stale

I do **not** think silent-drop is the primary explanation for this specific incident, because the interrupt-path contract mismatch above already explains the exact observed state. But it remains a credible amplifier and should stay on the shortlist.

---

## Less likely explanations

### 3. tmux/input-mode issue

Unlikely. The pane was not in copy-mode, and the problem is mirrored on disk by a stalled activity log plus stranded pending queue item.

### 4. Root runtime actually deadlocked

Also less likely. The `sprawl enter` process and weave Claude subprocess were both alive and blocked in normal wait states, which looks more like a missed terminal-state transition than a process-level hang.

---

## Working hypothesis

The live session most likely wedged because an interrupt completed on the unified-runtime path, `EventInterrupted` was translated into `InterruptResultMsg`, and AppModel handled that as a non-terminal status update instead of as turn completion. That left weave permanently non-idle, which in turn blocked both manual input dispatch and queue-drain of ghost's completion message.

A secondary/systemic risk is EventBus silent drop (QUM-472), which could produce a superficially similar stuck-streaming symptom if the TUI misses terminal events.

---

## Suggested fix direction

1. Separate **interrupt request acknowledgement** from **terminal interrupted-turn completion** in the TUI message contract.
   - Either:
     - map `EventInterrupted` to a terminal message type handled like `SessionResultMsg`, or
     - teach `AppModel.Update(InterruptResultMsg)` to distinguish request-ack vs terminal-event cases.
2. Add a regression test that exercises:
   - active turn
   - `Esc`
   - runtime publishes `EventInterrupted`
   - AppModel returns to `TurnIdle`
   - queued input and pending inbox drain resume normally
3. Keep QUM-472 in scope as follow-on hardening if the primary fix alone does not eliminate all wedges.

---

## Files most relevant to the bug

- `internal/tui/app.go:498-505`
- `internal/tui/app.go:964-973`
- `internal/tui/app.go:1077-1080`
- `internal/tuiruntime/tuiadapter.go:80-160`
- `internal/runtime/turnloop.go:160-190`
- `internal/runtime/eventbus.go:92-104`
- `docs/research/unified-runtime-messaging-audit.md:217-227`
