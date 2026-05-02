# Paste render cadence — multi-paragraph blob fills in at "rapid typing speed"

**Status:** research-only.
**Branch:** `dmotles/paste-render-cadence-research`.
**Investigated by:** ghost (research agent), 2026-05-02.
**Related:** QUM-430 (PasteMsg handler), QUM-432 (stripped-bracketed-paste classifier).

## TL;DR

When dmotles pastes a multi-paragraph blob in their TUI session, bracketed-paste markers are being stripped before they reach Bubble Tea (same root cause as QUM-432). Bubble Tea's input scanner therefore emits **one `tea.KeyPressMsg` per rune**, each going through the full `AppModel.Update → AppModel.View()` pipeline before the next rune is consumed. The renderer's 60 FPS ticker doesn't gate the rate — but the per-char Update/View work does. The QUM-432 classifier fixed *submission* (no premature submit on embedded `\n`), but did **not** address *render cadence* — every pasted character still does a full TUI re-layout.

The "instant splat" path exists and works (`tea.PasteMsg` → `textarea.insertRunesFromUserInput` in one Update). It just isn't the path being taken in dmotles's terminal chain.

**Recommendation:** add burst-coalescing in `InputModel.Update` so a sequence of fast-arriving printable `KeyPressMsg`s gets buffered and flushed into the textarea as a single `InsertString` (effectively synthesizing a `PasteMsg`). See [Recommended fix](#recommended-fix).

## Hypothesis evaluation

The prompt enumerated four candidate causes. Verdict for each, with code citations:

### (1) Bracketed-paste markers stripped → KeyPressMsg stream — **CONFIRMED as the cause**

When the outer terminal/multiplexer strips `ESC[200~ … ESC[201~`, ultraviolet's `eventScanner.scanEvents` sees no `PasteStartEvent`, so `d.paste` stays nil. Every printable rune then goes through the normal decode path and is appended to the events slice as an individual `KeyPressEvent`:

- `ultraviolet/terminal_reader.go:285-388` (`scanEvents`): the bracketed-paste accumulator path is gated on `d.paste != nil` (line 290). Without `PasteStartEvent`, each rune is decoded standalone and appended to `events` (line 379).
- `ultraviolet/terminal_reader.go:233-235` (`sendEvents`): events are pushed one by one onto the unbuffered `eventc` channel (which is `bubbletea.Program.msgs`, `tea.go:501` and `tty.go:87`).
- `bubbletea/v2@v2.0.3/tea.go:752-880` (`eventLoop`): receives each msg, calls `model.Update(msg)`, then `p.render(model)` which calls `model.View()` and stores the result.

**Per-rune cost in our app:**

- `internal/tui/input.go:68-115` (`InputModel.Update`): runs the QUM-432 paste classifier (cheap) then forwards to `textarea.Update`.
- `bubbles/v2@v2.1.0/textarea/textarea.go:1315-1316`: default branch calls `m.insertRunesFromUserInput([]rune(msg.Text))` for the single rune.
- `textarea.go:1326`: every `Update` then calls `m.recalculateHeight()` and rebuilds its internal `view()` cache.
- `internal/tui/app.go:1306-1402` (`AppModel.View`): rebuilds the entire layout — bordered tree, viewport, activity, status bar, input bar, lipgloss `JoinHorizontal`/`JoinVertical`. This is non-trivial work; even at ~1 ms per call you get a visible per-char cadence over a 1000-rune paste.

So the slowness is the cumulative cost of running the full TUI render pipeline N times for an N-rune paste, *not* a hardcoded delay anywhere.

### (2) Markers preserved → tea.PasteMsg — works correctly, but not the path being taken

The "instant splat" path exists end-to-end:

- `internal/tui/app.go:324-333` forwards `tea.PasteMsg` to `m.input.Update(msg)`.
- `bubbles/v2@v2.1.0/textarea/textarea.go:1223-1224` handles `tea.PasteMsg` by calling `insertRunesFromUserInput([]rune(msg.Content))` once — all runes, one Update, one View rebuild.
- `app_paste_test.go` exercises this and asserts the multi-line paste lands verbatim.

This path *is* fast. dmotles just isn't on it because their terminal chain (likely tmux or an SSH multiplexer) is stripping `ESC[200~`/`ESC[201~`.

### (3) Renderer 60 FPS batching — **ruled out as the bottleneck**

Bubble Tea v2's `cursedRenderer.render(v)` only stores the latest view (`cursed_renderer.go:579-584`); the actual write to the terminal happens in `flush(false)` driven by the program's ticker at `time.Second/fps` (`tea.go:1392-1418`, default `fps = 60` per `renderer.go:13`).

Because `render(view)` overwrites the stored view rather than queuing frames, multiple Updates between ticks coalesce into a single visible frame. So if Update+View were sub-millisecond, you'd see hundreds of chars per visible frame — not 1.

The 60 FPS ticker is therefore a *ceiling* on visible frames, not a floor on per-char latency. It does mean we can't show progress *faster* than every 16 ms, but it isn't what makes the paste feel slow.

### (4) Terminal output rate / SSH bandwidth — **ruled out**

The bottleneck is the input read+process pipeline, not the output write rate. `cursedRenderer.flush` already short-circuits on unchanged view (`cursed_renderer.go:287-290`), and the diff-based renderer only writes touched cells. Output volume per frame is small; SSH/tmux bandwidth is not the limit.

A confirmation: when paste markers *aren't* stripped, the same SSH pipe handles a `tea.PasteMsg` flush in a single frame instantly. So the pipe is fine — it's the per-char Update path that's slow.

## Why the unbuffered `msgs` channel matters here

`p.msgs` is unbuffered (`tea.go:598`: `make(chan Msg)`). That means:

1. Ultraviolet's `scanEvents` decodes a chunk of N runes and tries to send N events.
2. Each `eventc <- event` (`terminal_reader.go:234`) blocks until `eventLoop` consumes the previous one.
3. `eventLoop` runs `Update` and `View` synchronously per message before looping.

So back-pressure forces every rune through the full app render pipeline before the next rune is even sent. There is no opportunity for ultraviolet or Bubble Tea to coalesce.

## Recommended fix

Coalesce KeyPressMsg bursts inside `InputModel.Update` and flush them as a single `InsertString` call.

The QUM-432 classifier already tracks paste-burst state via `pasteUntil` and `lastKeyAt` with `pasteBurstWindow = 10 ms`, `pasteQuietWindow = 50 ms`. That same signal can drive coalescing:

```go
// New state on InputModel:
//   pasteBuf strings.Builder // accumulates printable runes during a burst
//
// In Update, for printable KeyPressMsg (msg.Text != "" && msg.Code != KeyEnter):
//   if !m.pasteUntil.IsZero() && now.Before(m.pasteUntil) {
//       // we're already inside a paste burst — buffer the rune and arm
//       // / extend a flush timer; do NOT forward to textarea yet.
//       m.pasteBuf.WriteString(msg.Text)
//       m.pasteUntil = now.Add(pasteQuietWindow)
//       m.lastKeyAt = now
//       return m, tea.Tick(pasteFlushDelay, func(time.Time) tea.Msg { return pasteFlushMsg{} })
//   }
//   if !m.lastKeyAt.IsZero() && now.Sub(m.lastKeyAt) < pasteBurstWindow {
//       // a burst just started — promote the previous rune AND this one
//       // into the buffer, arm the timer.
//       ...
//   }
//
// On pasteFlushMsg (or on any non-printable key, or on the embedded-Enter
// path that already exists at input.go:83-91):
//   if m.pasteBuf.Len() > 0 {
//       m.ta.InsertString(m.pasteBuf.String())
//       m.pasteBuf.Reset()
//   }
```

Result: one `InsertString` call per paste burst, one Update, one View rebuild — the same shape as the working `tea.PasteMsg` path. The 50 ms quiet-window debounce at the end of paste is below the just-noticeable-difference for "instant", and consistent with the existing classifier tuning.

**Why this beats alternatives:**

| Alternative | Drawback |
|---|---|
| `tea.WithFilter` / global Msg filter that merges adjacent KeyPressMsgs | Forces buffering at the program level, losing per-key ordering guarantees for non-input panels (tree, palette). The fix should be local to the input bar. |
| Lower `fps` in `tea.WithFPS` | Fewer visible frames during paste, but Update/View still runs N times — wastes CPU and may make paste *more* visible per-frame, not less. Doesn't fix the cause. |
| Disable the QUM-432 classifier and require markers | Regresses behavior for tmux/ssh users who originally motivated QUM-432. |
| Synthesize `tea.PasteStartMsg`/`tea.PasteEndMsg` via filter | Same coalescing logic, more plumbing — and Bubble Tea's `PasteStartMsg`/`PasteEndMsg` are only emitted when real markers arrive, so we'd be replicating the bracketed-paste contract by hand. The `InsertString`-on-burst version is the simplest local fix. |

**Estimated size:** ~30 LOC in `internal/tui/input.go` plus a handful of unit tests in `input_test.go` exercising:
- single keypress → forwarded to textarea normally (no buffering)
- burst of 100 chars within `pasteBurstWindow` → one `InsertString`
- burst followed by Enter → existing embedded-newline path still works
- burst followed by quiet window → flush via tick
- non-printable key during burst → flush buffer first, then handle the key

A complementary improvement (defense in depth, not strictly necessary): expose `tea.WithFPS(120)` in `cmd/enter.go:158`. With fewer characters arriving per frame after the fix, this only matters if users genuinely type at >60 keystrokes/sec, but it's free. Optional.

## Open questions / what I'd investigate next

1. **Empirical timing.** I haven't profiled `AppModel.View()`. The qualitative argument (N × per-char View ≈ hundreds of ms for N≈few hundred chars) is consistent with dmotles's observation, but a `pprof` capture during a paste burst would pin down the dominant cost — `lipgloss.JoinHorizontal` over bordered panels is a frequent culprit and might warrant memoization independent of the paste fix.
2. **Where are markers being stripped?** dmotles's environment chain. Worth confirming whether their tmux is at a version where the `allow-passthrough` / `bracketed-paste` tunable could pass markers through end-to-end. If so, fixing the environment removes the slow path entirely (the fast PasteMsg path takes over). The in-app coalescing fix is still worthwhile because we can't rely on every user's terminal chain.
3. **Render-side memoization.** `bubbles/textarea` v2.1.0 already uses `memoization.NewMemoCache` for visual line wrapping (`textarea.go:1218-1220`). Whether the cache hit-rate during paste is reasonable or pathological is unverified; if it thrashes, that compounds the per-char cost.
4. **Bubble Tea v2.0.6 is available** (`/home/coder/go/pkg/mod/charm.land/bubbletea/v2@v2.0.6` is on disk; we're pinned to 2.0.3). Worth a changelog skim — unrelated to this bug, but if 2.0.6 added paste-burst coalescing in eventLoop, an upgrade alone could fix this.

## Surprises

- The fix really is local. Once you trace the full path, neither Bubble Tea v2's renderer nor ultraviolet has a knob for "merge consecutive KeyPressMsgs into a synthetic paste." It has to live in our `InputModel.Update`.
- The "60 FPS bounds char throughput" hypothesis was a red herring once I read `cursedRenderer.render` — it stores, doesn't queue. The throughput cap is per-char Update+View work, not framerate.
- The QUM-432 classifier's `pasteBurstWindow = 10 ms` / `pasteQuietWindow = 50 ms` constants are *already perfect inputs* for the coalescing fix. No new tuning needed; the same signal that decides "this Enter is embedded" can decide "buffer these printables."
