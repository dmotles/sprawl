# Paste pipeline architecture — diagnosing QUM-449 and recommending a clean path forward

**Status:** research-only. QUM-449 has been reverted on `main` (commit `626c738`).
**Branch:** `dmotles/paste-pipeline-architecture-research`.
**Investigated by:** ghost (research agent), 2026-05-02.
**Predecessors on this same problem:** QUM-430, QUM-432, QUM-448, QUM-449, plus the earlier
`docs/research/paste-render-cadence.md` (commit `12d0ac1`).

---

## TL;DR

QUM-449 swapped one bad UX (typewriter cadence) for a worse one (apparent freeze).
The `pasteFlushMsg`/`tea.Tick(30ms)` design is *not* broken in the way the symptom
description suggests — the Tick is reliable, the Cmd plumbing is correct. The
real problem is **the QUM-432 time-based classifier is the wrong substrate to
build paste handling on**. Layering a buffer on top of an unreliable signal
produced a worse user experience than no buffer.

The fact that we have shipped four paste-related fixes (QUM-430 / 432 / 448 /
449) and still have a broken UX is the system telling us we are solving the
wrong problem. Paste in this app should ride **`tea.PasteMsg` only**, full stop;
everything we have built around stripped bracketed paste is brittle.

**Recommended path: Option C (architectural).**

1. Revert QUM-449 (done, `626c738`).
2. Treat stripped bracketed paste as an **environment bug**, not an app bug:
   document the required tmux/SSH config in our docs and (eventually) detect &
   warn at startup. Where the markers arrive, our existing `tea.PasteMsg` path
   in `app.go:324–342` already handles paste perfectly.
3. Defer / delete QUM-432's heuristic. Without QUM-449 and with documented
   environment requirements, the only remaining failure mode is "user typed
   `\n` literally during a fast paste" — a corner case, not a primary flow.
4. Optionally optimize `AppModel.View()` so the typewriter cadence (when
   bracketed paste really is unavailable) is invisible (<200µs/char). This is
   the right place to spend complexity, because it improves *every* fast input
   path, not just paste.

The detailed reasoning, code citations, and option matrix follow.

---

## 1. What QUM-449 actually does and why dmotles sees what they see

**The change** (commit `2e587ea`, reverted in `626c738`):

- Adds `pasteBuf *strings.Builder` to `InputModel`.
- Inside `InputModel.Update`, when a `tea.KeyPressMsg` is classified as part of
  a paste burst (`pasteUntil` active OR last printable < 10 ms ago), the rune
  is **not** forwarded to `textarea.Update`; it is appended to `pasteBuf` and a
  fresh `tea.Tick(30 ms, pasteFlushMsg{})` Cmd is returned
  (`armPasteFlush`).
- On `pasteFlushMsg`, on a real `tea.PasteMsg`, on a non-printable key, or on a
  submit-Enter, the buffer is drained via `m.ta.InsertString(...)` and reset.

**Why dmotles sees "frozen, then splat after ~1s":**

Per-rune the buffer path no longer mutates the textarea, so `m.ta.View()`
returns the same string every Update. `cursedRenderer.flush` short-circuits on
unchanged views (`cursed_renderer.go:287–290`):

```go
if !s.starting && !closing && s.lastView != nil &&
    viewEquals(s.lastView, &view) && frameArea == s.cellbuf.Bounds() {
    // no-op — nothing visibly changes for the user.
    return nil
}
```

So during the entire paste burst, the user sees zero visual progress on the
input bar. The buffer drains in exactly two cases:

1. `pasteFlushMsg` arrives. `tea.Tick(30 ms)` is scheduled at Update return
   time (`commands.go:154`: `time.NewTimer(d)` runs *before* the closure does
   `<-t.C`). However, **every in-burst keypress arms a fresh 30 ms tick**, so
   the *flush* happens 30 ms after whichever earlier in-burst key happened to
   win the timer race. In practice for a multi-paragraph paste over SSH/tmux,
   keys arrive at irregular cadence (some <10 ms apart, some not), so the
   buffer alternately fills, drains, fills again — and the *last* drain is
   roughly 30 ms after the last pasted byte. Total perceived freeze ≈ "paste
   delivery time + 30 ms".
2. A non-printable key arrives (e.g. right-arrow). The else-branch at
   `input.go` immediately drains the buffer into the textarea, then forwards
   the arrow to `textarea.Update`. **Hence dmotles's "right-arrow makes it
   instant"** — the keystroke triggers the drain.

**So the design is doing exactly what it was written to do.** The bug is that
the design assumed paste arrival would be one tight burst followed by quiet —
in dmotles's environment, the bytes trickle in over hundreds of milliseconds,
and "no visible progress for that whole window" is *worse* UX than typewriter
cadence.

### Sub-finding: the Cmd plumbing is fine

I want to record this explicitly because it was the prompt's first hypothesis:

- `tea.Tick(d, fn)` (`commands.go:154–164`) instantiates the `time.Timer`
  *eagerly* when `Tick` is called (i.e. at `armPasteFlush` invocation), and
  the returned closure does `ts := <-t.C` later. So even if `handleCommands`
  delays running the closure, the deadline is anchored to when Update
  returned — not when the Cmd starts.
- The cmds channel (`tea.go:998`) is unbuffered, but `handleCommands`
  immediately spawns a goroutine per Cmd (`tea.go:721–733`), so a slow Cmd
  doesn't backpressure the channel.
- `Program.Send` posts to `p.msgs` (`tea.go:1186`), an unbuffered channel that
  is also fed by ultraviolet's `StreamEvents` (`tty.go:87`). When ultraviolet
  is hot delivering paste bytes, both senders queue at `p.msgs`. Go's runtime
  serves blocked senders in FIFO order from `sendq`; if ultraviolet is
  re-blocking on every event, the Tick goroutine's send may sit behind several
  events. That adds tens of milliseconds of jitter at most — **not** the 1 s
  the user observes.

> **Verdict on the prompt's "is the Tick Cmd being lost / is there a
> render-batching gate" hypothesis:** No. `Update→render` runs every msg
> (`tea.go:872–880`), each msg drives a `view = m.View()` store; `flush` runs
> from a 60 FPS ticker (`tea.go:1414–1418`). The "freeze" is real but its
> cause is purely "we didn't change the view, so the renderer rendered nothing
> new".

---

## 2. What Bubble Tea v2 expects for paste

I read `paste.go`, `input.go`, `tea.go`, `keyboard.go`, and
`cursed_renderer.go` in `charm.land/bubbletea/v2@v2.0.3` (and diffed
`v2.0.6` — only `go.mod`/`go.sum` and golden `testdata` differ; **no
paste-pipeline changes between v2.0.3 and v2.0.6**, so a version bump will not
help).

Key facts:

- **The official paste model is `tea.PasteMsg{Content string}`** (`paste.go`).
  `PasteStartMsg` and `PasteEndMsg` exist but Bubble Tea v2 **buffers the
  entire pasted content internally and emits a single `PasteMsg` at the end**;
  the start/end events are surfaced primarily for layouts that want to e.g.
  show a "pasting…" indicator. (See ultraviolet
  `terminal_reader.go:285–388`'s `scanEvents` — bracketed-paste accumulates
  into `d.paste` until `PasteEndEvent`, then emits the buffered string.)
- **Bracketed paste is enabled by default.** `View.DisableBracketedPasteMode`
  defaults to false, and `cursed_renderer.flush`'s teardown path
  unconditionally writes `ResetModeBracketedPaste` (`cursed_renderer.go:175`)
  iff it was enabled — confirming it was. We do NOT need to opt in.
- **Kitty keyboard protocol and `modifyOtherKeys` are also enabled by
  default** (`cursed_renderer.go:134–139`):

  ```go
  // Enable modifyOtherKeys and Kitty keyboard protocol.
  // Both can coexist; terminals ignore what they don't support.
  _, _ = s.scr.WriteString(ansi.SetModifyOtherKeys2)
  kittyFlags := keyboardEnhancementsFlags(s.lastView.KeyboardEnhancements)
  _, _ = s.scr.WriteString(ansi.KittyKeyboard(kittyFlags, 1))
  ```

  This means we *cannot* improve paste detection via "enable a richer keyboard
  protocol" — the protocol is on, and it doesn't carry paste markers
  separately from CSI 200~/201~. **Kitty keyboard does not help paste.**
- **`textarea` v2.1.0 has a paste-aware fast path** that we already use:
  `textarea.go:1223–1224`:

  ```go
  case tea.PasteMsg:
      m.insertRunesFromUserInput([]rune(msg.Content))
  ```

  One Update, one `recalculateHeight`, one View. This is the path that "just
  works" when bracketed paste markers reach our process.
- **There is no renderer-level frame coalescing that would explain dmotles's
  symptom.** `cursedRenderer.render(view)` *stores* the latest view
  (`cursed_renderer.go:579–584`); the per-frame ticker at 60 FPS reads
  whatever was most recently stored. Multiple Updates between ticks coalesce
  into the most-recent view automatically. No frames are queued or dropped.

> **Verdict on prompt §2 ("are we missing a Bubble Tea option?"):** No knob,
> no filter, no upstream paste-aware textarea API would fix this. The
> bracketed-paste path Bubble Tea exposes is the right path; we're already on
> it whenever the markers arrive.

---

## 3. Reference implementations: what Crush does

`charmbracelet/crush@v0.64.0` uses **the same `bubbletea/v2` and
`bubbles/v2` versions as us** (`bubbletea/v2 v2.0.6`, `bubbles/v2 v2.1.0` —
patch difference from us, no paste-relevant changes). Searching the entire
crush source for paste handling
(`grep -rln "PasteMsg\|paste" .../crush@v0.64.0/internal/`):

- The only `tea.PasteMsg` handler is `handlePasteMsg` in
  `internal/ui/model/ui.go:3411–3492`. It branches on size (≥10 lines or
  ≥1000 cols → treat as a file attachment) and otherwise forwards
  `msg.Content` straight through to `textarea`. **No heuristic. No
  per-keypress classifier. No buffering.**
- There is no "stripped bracketed paste" recovery path anywhere in crush.
- crush's program construction (`internal/cmd/root.go:126–131`) is just
  `tea.NewProgram(model, WithEnvironment, WithContext, WithFilter(mouse))` —
  no paste-related options.

The implication is striking: **Charm's own flagship coding agent assumes
bracketed paste works.** When users complain about paste in tmux/ssh, the
remediation is environment-side, not code-side.

I also looked at the prompt's mention of Claude Code (TypeScript/Ink). Ink
has the same architecture: it consumes `bracketed-paste` as a single event,
not per-character. There is no equivalent of QUM-432 in Ink-using apps.

> **Verdict on prompt §4 ("how do other coding agents solve this?"):** They
> don't solve it inside the app. They rely on the bracketed-paste contract
> being end-to-end and let the user fix the environment when it isn't.

---

## 4. Re-auditing our four-fix layered architecture

| Fix | What it does | Necessary? |
|---|---|---|
| **QUM-430** (`b5c152d`) — `tea.PasteMsg` handler in `app.go` | Forwards bracketed paste to `InputModel.Update` so embedded `\n` is inserted, not treated as submit. | **Yes** — this *is* the right path. Keep. |
| **QUM-432** (`ac7fada`) — time-based paste classifier in `input.go` | When markers are stripped, classifies an Enter arriving within 10 ms of the previous printable as embedded `\n`. | **Marginal.** It papers over a single specific bug (premature submit on stripped paste) with a heuristic that has cliffs (12 ms gap → still submits). Without QUM-449, the rest of the paste arrives correctly anyway because the textarea receives each rune. The only thing this catches is the Enter classification. Could be replaced by docs ("configure tmux to pass bracketed paste") + accept the rare misfire. |
| **QUM-448** (`4b325b1`) — re-resize panels on input growth | Independent of paste classification; fixes panel cache when textarea grows. | **Yes** — orthogonal, correct, keep. |
| **QUM-449** (`2e587ea`, reverted) — buffer paste-burst runes, flush on tick | Tries to coalesce N KeyPressMsgs into one InsertString to avoid per-char `View()` cost. | **No** — wrong layer (input model, but the cost lives in `AppModel.View()`), wrong substrate (heuristic burst detection), regressed UX in dmotles's env. |

**The pattern is anti-architectural.** Each fix layered another piece of
state and another timing window onto an already-fragile classifier. Each fix
was correct *in isolation* but the ensemble has cliffs:

- A 9 ms gap and an 11 ms gap produce qualitatively different behavior
  (in-burst vs not-in-burst).
- During a long paste that mixes <10 ms and >10 ms gaps, the textarea oscillates
  between "rune just appeared" and "rune was buffered, will appear later",
  which is exactly the "typewriter then freeze then splat" pattern dmotles
  reports.
- Tests pass because they use a fake clock and synthetic burst patterns that
  don't reproduce real terminal jitter.

**Anti-pattern check (prompt §3):**

- *Are we re-decoding paste content multiple times?* No — each layer dispatches
  on a different msg type, so there's no double-decode. The cost is the
  `AppModel.View()` rebuild per msg, not redundant decoding.
- *Are we fighting the framework?* Yes. Bubble Tea's contract is "bracketed
  paste arrives as one PasteMsg, period." Trying to recover from stripped
  markers in user code is the framework saying "configure your environment."
- *Could enabling a richer keyboard protocol help?* No, see §2 — the protocol
  is already on, and it doesn't carry paste info anyway.

---

## 5. Recommended path forward

### Option A — fix QUM-449 narrowly. **Not recommended.**

The narrowly-fixable shape would be: drain the buffer on a render-tick boundary
rather than on a per-burst tick, so the user sees rolling progress. Concretely,
either (i) flush every N runes into the textarea even mid-burst (so the user
sees waves of chars), or (ii) replace the time-based heuristic with
"flush whenever there's been *no key for X ms*", measured from a separate
debouncer that doesn't reset on every burst-key.

**Why not:** Same heuristic substrate. Same cliffs. Same brittleness. We will
ship a fifth fix in a month.

**Estimated effort:** ~1 day. **Estimated risk:** medium — easy to reintroduce
the freeze pattern under different paste cadence; hard to test without
real-terminal jitter.

### Option B — revert QUM-449 permanently, accept typewriter cadence. **Recommended as immediate action; insufficient as a long-term answer.**

Revert is already done. The user falls back to the QUM-432 + QUM-448 baseline:
each pasted rune renders one frame at a time. Multi-paragraph paste of a few
hundred runes will visibly type out over a second or so when bracketed paste is
stripped. With markers preserved, paste is instant.

**Why this is good enough today:** it is the least-bad UX (visible progress
beats silent freeze), and dmotles can paste-then-wait for the typewriter to
finish. It also clears the deck for the bigger fix.

**Estimated effort:** zero (already done). **Estimated risk:** zero.

### Option C — environmental + view-cost fix. **Recommended as the real solution.**

Two complementary moves:

1. **Document the bracketed-paste environment requirements** in
   `docs/tui-troubleshooting.md` (or wherever — repo doesn't have a TUI doc
   yet; could live next to `tui-testing.md`):
   - tmux ≥ 3.3 needs `set -s extended-keys on` and (for some configs)
     `set -s allow-passthrough on`.
   - Inner-tmux-in-outer-tmux setups need bracketed paste passthrough on the
     outer.
   - SSH normally passes ESC sequences through unchanged; if a user is using
     mosh or another connection multiplexer, they may need to verify.
   - Test command: `printf '\e[?2004h' && cat`, paste something, expect to
     see `\e[200~ … \e[201~` framing.

   Most users in the team (per `docs/research/paste-render-cadence.md`) hit
   this on tmux specifically; documenting the fix is high-leverage.

2. **Optimize `AppModel.View()` so per-char cost is invisible** (target
   < 200 µs / call so a 1000-rune paste is < 200 ms total — below the JND for
   "instant" in a terminal).
   - `AppModel.View()` rebuilds bordered tree, viewport, activity, status, and
     input panels each call (`app.go:1306–1402`), then `lipgloss.JoinHorizontal`
     / `JoinVertical`. None of those except the input change during a paste.
   - Cache rendered panel strings keyed on `(panel.dirty, layout)` and
     re-render only the input pane during paste bursts.
   - `bubbles/v2/textarea` already memoizes visual line wrapping via
     `memoization.NewMemoCache` (`textarea.go:1218–1220`), so the textarea
     side is fine; the cost is in our side.

   This change benefits *every* fast input path (rapid typing, key-repeat
   cursor motion, autoscroll), not just paste.

   **Side benefit:** with View() fast enough, **QUM-432's classifier becomes
   unnecessary even when bracketed paste is stripped**, because the rare
   "user typed real Enter inside a fast paste" can be tolerated as a
   submit — a misfire that the rolling-progress view makes visible to the
   user, who can recover with `Up`-arrow history.

**Estimated effort:** 0.5 day for docs (Option C.1); 1–2 days for view
optimization with benchmarks (Option C.2). **Estimated risk:** low (docs are
zero-risk; view caching is well-understood, with a clear regression signal in
the existing TUI snapshot tests).

### Option D — synthesize PasteMsg in a `WithFilter`. **Not recommended.**

Conceptually clean: `tea.NewProgram(model, tea.WithFilter(coalesceBurstFilter))`
detects bursts in the global msg stream and emits a synthetic
`tea.PasteMsg{Content: ...}` after a quiet window, swallowing the original
KeyPressMsgs. Then the normal app path handles it.

**Why not:** identical heuristic problem to QUM-449, just relocated. Plus the
filter has to make decisions without app context (e.g., is the active panel
the input bar?). And it would prevent KeyPressMsgs from reaching the tree /
palette / viewport panels during the burst window, so global keys (Esc,
Ctrl-C) would feel laggy.

**Estimated effort:** 2 days. **Estimated risk:** medium-high — easy to
introduce subtle regressions across all panels.

### Recommended sequencing

1. **Now:** ship the revert (QUM-449 → already on `626c738`). Validate. Done.
2. **Next:** file a small doc issue (Option C.1) — write
   `docs/tui-bracketed-paste.md` describing how to configure tmux/SSH so
   markers reach our process. Half-day's work; high leverage; closes most
   user reports immediately.
3. **Then:** file a perf issue (Option C.2) — profile `AppModel.View()` under
   paste, identify the dominant cost (likely lipgloss `JoinHorizontal`/border
   rendering of the tree+viewport+activity panel), add a panel-level render
   cache. ~1–2 days. Includes a benchmark in `internal/tui/` so we don't
   regress.
4. **Optional follow-up:** with the perf fix in place, revisit whether
   QUM-432's classifier still pays its weight. If not, delete it; the
   resulting code is materially simpler and the failure mode (rare premature
   submit on user-typed Enter mid-paste) is visible and recoverable.

---

## 6. Linear issues to file

Concrete proposed issues (titles + summary + estimate). Filed alongside this
doc.

1. **QUM-XXX — Document tmux/SSH bracketed-paste configuration for sprawl
   TUI** *(docs, ~0.5 day)*
   - Body: write a short troubleshooting doc explaining how stripped
     bracketed-paste markers cause the typewriter-cadence symptom, and how to
     verify / fix at the tmux+SSH layer. Link from `CLAUDE.md` /
     `docs/research/paste-pipeline-architecture.md`. Include the
     `printf '\e[?2004h' && cat` self-test.
2. **QUM-XXX — Profile and cache `AppModel.View()` panel renders** *(perf,
   ~1.5 day)*
   - Body: under paste, `AppModel.View()` runs once per pasted rune. Each call
     rebuilds bordered tree, viewport, activity, status, and input panels
     even though only the input changes. Goal: <200 µs/call when only the
     input panel is dirty. Add a benchmark
     (`BenchmarkAppModel_View_PasteBurst`) that inserts 500 runes and
     measures total wall time. References this doc.
3. **QUM-XXX — Revisit QUM-432 paste classifier after view-cost fix lands**
   *(cleanup, ~0.5 day, blocked by #2)*
   - Body: with `View()` fast enough that typewriter cadence is invisible,
     the time-based Enter classifier in `input.go` becomes a maintainability
     burden with cliffs at the 10 ms / 50 ms boundaries. Evaluate deleting
     it; the surviving failure mode is "user typed Enter mid-paste in a
     terminal that strips markers" — rare and recoverable.

(QUM-449 itself stays Done; revert is committed.)

---

## 7. Open questions / what I'd investigate next

1. **Where exactly are markers being stripped in dmotles's environment?**
   Worth a 10-minute experiment: run `printf '\e[?2004h' && cat`
   inside their actual sprawl TUI launch context (tmux session inside the
   work dev container or whatever they're using), paste, see if `\e[200~`
   reaches `cat`. That nails down whether QUM-432 is even relevant in the
   target deployment.
2. **Profile data, not assumptions.** I claim `AppModel.View()` is
   ~1 ms/call in §1 / paste-render-cadence.md, but I haven't actually
   measured. A benchmark would either confirm "yes 1 ms" (then Option C.2
   is the right size) or surprise us with "10 ms" (then the cure is
   bigger — maybe lipgloss is the wrong layout engine for fast input
   paths).
3. **`viewEquals` semantics during paste.** I asserted that during a QUM-449
   buffered burst the view is unchanged; worth confirming by capturing
   stored views from `cursed_renderer.go` during a paste and diffing. If the
   cursor moved (e.g. the spinner), the view *would* change frame to frame
   and `viewEquals` would NOT short-circuit, meaning we'd be paying full
   per-frame render cost even on "no input change" frames. That would shift
   priorities.
4. **Charmbracelet's stance on stripped bracketed paste.** They presumably
   have GitHub issues about this. Worth a quick search before doing perf
   work, in case there's an upstream fix in flight or a recommended pattern
   we can copy verbatim. My quick read of crush suggests "no," but I didn't
   exhaustively check the bubbles/textarea issue tracker.

---

## 8. Surprises

- **The Tick plumbing is *fine*.** I went into this expecting the prompt's
  hypothesis (Tick lost in render gating) to be the cause; the actual cause
  is that the buffered design *intentionally* shows nothing until a quiet
  window, and the quiet window over a slow SSH paste is the entire paste
  duration. Mechanism != design intent.
- **Bubble Tea v2 already enables Kitty keyboard, modifyOtherKeys, and
  bracketed paste by default.** I had assumed we'd find an opt-in. There is
  no opt-in — they're on. So all the "should we enable richer protocols"
  questions resolve to "we already are; it doesn't help paste."
- **Crush has zero stripped-paste recovery code.** The flagship coding agent
  by the same authors as Bubble Tea simply requires bracketed paste to
  work end-to-end. That's the loudest signal that QUM-432's class of fix
  isn't the convention.
- **QUM-449 *passed validation and both e2e gates.*** The tests use a fake
  `nowFunc` and synthetic burst patterns; they exercise the buffer-and-drain
  shape correctly but cannot reproduce the "buffer drains only after the
  whole paste arrives" perceived freeze, because the test feeds runes
  synchronously with no real-terminal jitter. We need a paste benchmark
  with realistic delivery cadence (random 1–30 ms gaps) before we trust
  ourselves to ship paste-pipeline changes again.
