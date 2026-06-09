# QUM-617 — TUI text selection blocked by mouse capture

> **2026-06-09 note (QUM-699):** the `cmd/input_debug.go` diagnostic command
> referenced below was deleted after QUM-608 shipped. Line/file refs are
> historical.

**Status:** research only (no code change). **Author:** ghost. **Date:** 2026-05-21.
**Linear:** [QUM-617](https://linear.app/qumulo-dmotles/issue/QUM-617/tui-cannot-select-copy-text-from-viewport-mouse-capture-blocks-native).
**HEAD verified:** `cb4dabb` (descendant of `6d108eb` on main).

---

## 1. TL;DR

The hypothesis in the ticket is **confirmed by source reading**. Sprawl's TUI
sets `tea.MouseModeCellMotion` on every rendered `tea.View` (`internal/tui/app.go:1798`,
`:1893`). Bubble Tea v2.0.3's `cursedRenderer.changes` translates that to
`ESC[?1002h` (XTerm button-event tracking) + `ESC[?1006h` (SGR extended)
(`charm.land/bubbletea/v2@v2.0.3/cursed_renderer.go:121–127`, ANSI byte values
in `github.com/charmbracelet/x/ansi@v0.11.7/mode.go:474, 521`). Mode `1002`
specifically *includes drag tracking* (motion-while-button-held), which is the
exact event the terminal would otherwise consume for native click-drag
selection. Claude Code's Ink-based TUI does not enable any mouse mode by
default, which is why selection works there on the same stack.

**Recommended fix shape: hybrid (c) + immediate workaround documentation.**
Ship the workaround today (Shift+click+drag bypasses mouse capture in
xterm.js / most terminals — confirmed by the xterm.js maintainers and tmux's
own documentation), then implement a selection-mode toggle (default keybind
`Ctrl-/` or similar — `Ctrl-S` is already a Bubble Tea search idiom and is
captured by some terminals as XOFF, so avoid it) that flips a single
`m.selectionMode bool` in `AppModel` and gates `v.MouseMode` in
`renderView`. Bubble Tea v2's renderer already diffs `MouseMode` across
frames and emits the proper enable/disable SGR sequences when the field
changes (`cursed_renderer.go:350–368`), so the toggle is one boolean plus a
keybind plus a short-help line. Fix (b) ("most-conservative mode, button-only
no motion" — i.e. mode `1000`) is **not directly available** in Bubble Tea v2
without forking: the library exposes only `MouseModeNone`, `MouseModeCellMotion`
(=1002), and `MouseModeAllMotion` (=1003). Fix (a) (disable mouse entirely,
keyboard-only scroll) is a real UX regression we don't need to accept.

---

## 2. Mouse-capture surface in Bubble Tea v2 + sprawl

### 2.1 Where sprawl sets the mode

* `internal/tui/app.go:1797–1798` (too-small fallback) and
  `internal/tui/app.go:1886–1893` (normal frame) both end `renderView` with:

  ```go
  v := tea.NewView(content)
  v.AltScreen = true
  // QUM-280: mouse cell motion enables scroll-wheel events on the viewport.
  // Tradeoff: this breaks native terminal text-select-and-copy. ...
  v.MouseMode = tea.MouseModeCellMotion
  return v
  ```

  The QUM-280 comment already calls out the exact tradeoff this ticket reports.
  QUM-281 was filed as the follow-up "proper selection-to-clipboard design"
  but is still open; QUM-617 supersedes the immediate-friction half of it.

* `cmd/enter.go:262–276` (`resolveEnterDeps.runProgram`) constructs the
  program with `tea.NewProgram(model, opts...)`. The only options conditionally
  added are `tea.WithInput(coal)` for the paste coalescer (QUM-608). No
  `tea.WithMouse…` option is passed — mouse mode is purely view-driven (v2's
  declarative pattern; see UPGRADE_GUIDE_V2.md §"Mouse").

* `cmd/input_debug.go:201–210` (`inputDebugModel.View`) does **not** set
  `v.MouseMode` at all, so the diagnostic command runs with mouse mode
  effectively disabled (Bubble Tea defaults to `MouseModeNone`). This means
  `sprawl input-debug` is **not** a faithful proxy for what `sprawl enter`
  emits w.r.t. mouse-mode bytes — see §8 Open Questions / empirical recipe.

### 2.2 What Bubble Tea v2 emits

Bubble Tea v2's renderer state machine handles mouse mode in two places:

* **Initial frame / restart-from-suspend** —
  `charm.land/bubbletea/v2@v2.0.3/cursed_renderer.go:121–127`:

  ```go
  switch s.lastView.MouseMode {
  case MouseModeNone:
  case MouseModeCellMotion:
      _, _ = s.scr.WriteString(ansi.SetModeMouseButtonEvent + ansi.SetModeMouseExtSgr)
  case MouseModeAllMotion:
      _, _ = s.scr.WriteString(ansi.SetModeMouseAnyEvent + ansi.SetModeMouseExtSgr)
  }
  ```

* **Frame-to-frame diff** —
  `cursed_renderer.go:350–368`: the renderer compares
  `view.MouseMode != s.lastView.MouseMode` and emits enable/disable pairs as
  needed. So a runtime toggle is fully supported — no special API needed.

* **Close / shutdown** — `cursed_renderer.go:181–187` emits the corresponding
  reset trio (`ESC[?1002l` + `ESC[?1003l` + `ESC[?1006l`) so terminals are
  left clean.

The ANSI byte values resolve via `github.com/charmbracelet/x/ansi@v0.11.7/mode.go`:

| Bubble Tea v2 mode    | Bytes emitted                  | XTerm name                 |
|-----------------------|--------------------------------|----------------------------|
| `MouseModeNone`       | *(nothing)*                    | —                          |
| `MouseModeCellMotion` | `ESC[?1002h` + `ESC[?1006h`    | "Button-event tracking" + SGR ext. |
| `MouseModeAllMotion`  | `ESC[?1003h` + `ESC[?1006h`    | "Any-event tracking" + SGR ext.    |

### 2.3 Defaults + the missing mode

* `tea.View.MouseMode` is a value field with zero value `MouseModeNone`
  (`tea.go:283–305`), so if a program never sets it, no mouse-capture bytes
  ever leave Bubble Tea. This is what Ink does too (see §5).

* **There is no `MouseModeButtons` (X11 `?1000h`, click-only without motion).**
  Mode `1000` is precisely the "button only, no motion" middle ground the
  ticket calls fix (b), and Bubble Tea v2 **does not expose it**. The closest
  is `MouseModeCellMotion` whose name is somewhat misleading — `1002` does
  send "cell-resolution" motion events, but only while a button is held,
  which is exactly the click-drag stream that defeats native selection.

### 2.4 Implication for fix (b)

To get a true "click + wheel + release, but no drag" mode without upstream
changes, sprawl would have to bypass Bubble Tea's mouse-mode machinery and
emit `ESC[?1000h` + `ESC[?1006h` directly (e.g. via a custom `tea.Cmd` that
writes through `tea.Println` or printing to the alt-screen before raw mode
is active). Bubble Tea's renderer would then *also* be allowed to overwrite
this on the next diff. This is the kind of state-divergence we've already
been burned by in QUM-608's paste pipeline, so I'd treat fix (b) as
"only-if-(c)-doesn't-work" and prefer upstreaming a new mode-value to
`charm.land/bubbletea/v2` if we go this route.

---

## 3. Where sprawl handles mouse events

`internal/tui/viewport.go:182–199` (`ViewportModel.Update`):

```go
func (m ViewportModel) Update(msg tea.Msg) (ViewportModel, tea.Cmd) {
    wasAtBottom := m.vp.AtBottom()
    var cmd tea.Cmd
    m.vp, cmd = m.vp.Update(msg)

    switch msg.(type) {
    case tea.KeyPressMsg, tea.MouseWheelMsg:
        if m.vp.AtBottom() { m.autoScroll = true; ... }
        else if wasAtBottom { m.autoScroll = false }
    }
    return m, cmd
}
```

Sprawl only ever inspects `tea.MouseWheelMsg` for autoscroll bookkeeping and
relies on the inner `charm.land/bubbles/v2/viewport.Model` to do the
actual scroll. That inner viewport's `Update` (`charm.land/bubbles/v2@v2.1.0/viewport/viewport.go:696–719`)
handles `tea.MouseWheelMsg` directly with sub-cases `MouseWheelUp/Down/Left/Right`.

The TUI app router (`internal/tui/app.go:330–343`) forwards *all* `tea.MouseMsg`
(of which `MouseWheelMsg`, `MouseClickMsg`, `MouseMotionMsg`, etc. are concrete
subtypes in v2) to `m.observedVP().Update(msg)`. Click and motion events
fall through harmlessly because the bubble viewport ignores them — but they
*are* being consumed by sprawl, which is precisely why the terminal can't
do native selection. The comment at `:331–336` already documents this.

**Could sprawl work with a more-conservative mode?** Yes — wheel-only is
sufficient for the only mouse interaction sprawl actually wants today. If
Bubble Tea exposed `MouseModeButtons` (1000), wheel events would still
arrive (terminals report wheel as button-press in mode 1000) and click+drag
would not. Until then, mode 1002 is the only practical option.

---

## 4. Bypass mechanism reality-check

### 4.1 Shift+click+drag in coder web terminal (xterm.js)

**Yes, this works.** xterm.js documents (and a maintainer confirmed in
discussion #2337) that holding **Shift on Linux/Windows, Option on macOS**
forces the browser-native selection regardless of whether the application
has enabled mouse mode. The mouse event is suppressed *at the xterm.js
layer* before being encoded into a `1002`-style report — so it never reaches
tmux or sprawl. ([xterm.js #2337 / #1536](https://github.com/xtermjs/xterm.js/issues/2337))

### 4.2 With tmux 3.2a in the path

tmux's mouse handling does not interfere with the Shift modifier because the
xterm.js layer absorbs the event before tmux is involved. The Arch wiki,
[tmuxai.dev](https://tmuxai.dev/tmux-enable-mouse/), and tmux #1804 all
document the same workaround. **Caveat (real and worth documenting):**
Shift+drag selects in *browser-native* coordinates, so it will select across
tmux pane boundaries — for sprawl's normal "TUI fills the pane" usage that
doesn't matter, but if a user has multiple tmux panes side-by-side and tries
to Shift+drag a region that spans pane borders, the result will include the
border characters and adjacent pane text. This is acceptable for a workaround.

### 4.3 Other terminals (for documentation completeness)

* **macOS Terminal.app:** `Fn`+drag or `Option`+drag.
* **iTerm2:** `Option`+drag (or "force selection on click" preference).
* **gnome-terminal / xterm / kitty / wezterm:** `Shift`+drag.
* **Alacritty:** `Shift`+drag.
* **VS Code integrated terminal:** `Alt`+drag (configurable).

### 4.4 Workaround precedence vs. tmux copy-mode

tmux's own copy-mode (`prefix [`, then space to start selection, enter to
yank, `prefix ]` to paste) is a *third* path that bypasses sprawl entirely.
It works but has the worst ergonomic story (multi-keystroke modal). The
Shift+drag workaround is strictly better for users on a real GUI terminal;
copy-mode is only superior if the user is already a tmux power-user.

---

## 5. Comparison to Ink (Claude Code's TUI library)

Ink does **not** enable any mouse mode by default. Its `useInput` hook reads
keystrokes only; the project ships no first-party mouse hook and explicitly
documents that mouse tracking would have to be enabled by writing the SGR
sequences manually (see Ink readme; cf. the third-party `ink-mouse` package
which describes itself as the workaround). The QUM-608 deep-dive citation
table independently confirmed that Ink emits only `ESC[?2004h` (bracketed
paste) at startup — no `1000/1002/1003/1006` bytes. ([Ink readme](https://github.com/vadimdemedes/ink/blob/master/readme.md), [ink-mouse](https://github.com/zenobi-us/ink-mouse))

That is the entire explanation for the asymmetry observed in dmotles's
side-by-side test: same xterm.js, same tmux 3.2a, same coder pane —
Claude Code never asks the terminal to forward mouse events, so the
browser/xterm.js handles native selection. Sprawl asks for `?1002h` and
gets exactly what it asked for.

---

## 6. The three fix shapes — evaluation

### (a) Disable mouse mode entirely; replace scroll with keyboard

* **Mechanic:** drop the `v.MouseMode = tea.MouseModeCellMotion` lines in
  `renderView`. Bind `PgUp`/`PgDn`/`Home`/`End` (and arguably `j`/`k`) to
  scroll the viewport. The bubbles viewport already exposes
  `ScrollUp(n)` / `ScrollDown(n)` / `GotoTop()` / `GotoBottom()`.
* **Cost:** loses scroll-wheel on the viewport — a real UX regression the
  ticket calls out we don't want to take. Trackpad scroll on macOS is
  particularly common during agent observation.
* **Benefit:** smallest diff. Genuinely zero risk of state divergence with
  the renderer because we're staying on the well-trodden `MouseModeNone`
  path that Ink + every read-only TUI uses.

### (b) Most-conservative mouse mode (button-only, no motion)

* **Mechanic:** would require `ESC[?1000h` + `ESC[?1006h`. Bubble Tea v2
  **does not expose this** (see §2.3). Two implementation paths:
  1. **Fork / upstream** a new `MouseModeButtons` value to
     `charm.land/bubbletea/v2`. Plausible — the patch is ~10 lines and the
     library's design already invites this granularity. ETA depends on
     upstream responsiveness; we'd have to vendor in the meantime.
  2. **Bypass** — emit the bytes directly via `tea.Printf` or by writing
     to stdout from `init()`. Fragile because cursedRenderer's
     enable/disable diff machinery would re-overwrite on the next frame.
     Strongly not recommended.
* **Cost:** out-of-band machinery, vendor dependency, or upstream-PR wait.
  Compatibility unknown across the full matrix (some terminals still
  consume click in mode 1000 even without drag — the only way to know is
  to test on the coder+tmux 3.2a stack).
* **Benefit:** *if* it works, click-drag falls through to native selection
  *without* needing the user to hold a modifier. Best ergonomics.

### (c) Selection-mode toggle

* **Mechanic:** add `selectionMode bool` to `AppModel`. Bind one keystroke
  (proposal: `Ctrl-/`; rationale below) that flips it; `renderView` chooses
  `v.MouseMode = MouseModeNone` when true, else `MouseModeCellMotion`. Any
  keypress while in selection mode (`tea.KeyPressMsg` other than the toggle
  itself, captured at the top of `Update`) exits selection mode. Surface
  the state in the existing `shortHelp` row (`internal/tui/app.go:1843–1845`)
  and the status bar so the mode is unambiguous.
* **Why `Ctrl-/`:** `Ctrl-S` is widely captured by terminals as XOFF
  (terminal flow-control), is taken by `bubbles/textinput` for "save" in
  some setups, and is overloaded in users' muscle memory by editor save.
  `Ctrl-/` is mapped to nothing in stock Bubble Tea / sprawl, is reachable
  on US/UK keyboards without contortion, and visually echoes the
  "fork into a different mode" semantics (like vi's `/` search but a
  detour). `Ctrl-\\` or `Ctrl-]` are acceptable alternatives.
* **Feasibility — confirmed:** Bubble Tea v2's renderer diffs `MouseMode`
  across frames (`cursed_renderer.go:350–368`) and emits the right
  enable/disable sequences automatically. We do not have to touch the
  renderer or stdout. The toggle is bool + keybind + short-help string.
* **Cost:** ~30 lines in `internal/tui/app.go` + a help/shortcut entry +
  tests asserting that `View().MouseMode` differs in the two states. Plus
  documentation (CLAUDE.md and the in-app help).
* **Benefit:** scroll-wheel preserved in normal mode; selection mode
  preserves wheel-less scrolling via keyboard if we feel like it
  (PgUp/PgDn still work). Works on every terminal — no compatibility
  matrix. Discoverable via short-help.

### (d) Hybrid (recommended)

Document Shift+click+drag in `CLAUDE.md` and the in-app help **now** —
ship the workaround as part of the QUM-617 fix commit. Then implement (c)
as the proper solution. The workaround handles the case where the user
forgets the keybind or is partway through reading agent output; the
selection-mode toggle handles the case where the user wants to select
multiple regions in a row, or where Shift+drag accidentally selects across
adjacent tmux panes.

---

## 7. Recommendation

**Pick (d): document Shift+drag immediately, implement (c) shortly after.**

Concretely, the implementation plan for (c) would be:

1. `internal/tui/app.go`: add `selectionMode bool` to `AppModel`. Add
   `(*AppModel).toggleSelectionMode()` and call it from `Update` on the
   chosen keybind.
2. In `Update`, when `selectionMode` is true and any non-toggle `tea.KeyPressMsg`
   arrives, set it false (after letting the keystroke route normally — or
   eat it; design decision worth a quick UX call).
3. In `renderView`, gate the existing `v.MouseMode = tea.MouseModeCellMotion`
   on `!m.selectionMode`. Else `v.MouseMode = tea.MouseModeNone`.
4. `internal/tui/statusbar.go` and `internal/tui/shorthelp.go`: surface the
   mode. Status bar gets a `[SELECT]` chip; short-help row swaps "wheel: scroll"
   for "(any key) exit select".
5. Tests: extend `internal/tui/app_test.go:1920–1946`
   (`TestAppModel_View_EnablesMouseCellMotion`) with a sibling
   `TestAppModel_View_SelectionMode_DisablesMouseCapture` and a transition test.
6. Docs: `CLAUDE.md` "Tips" section + an entry in the help-overlay (`help.go`).

Estimated diff: ~80 lines including tests. Not gated by any upstream library.

If (c) ships and dmotles still wants modifier-free selection — i.e. he
genuinely wants click-drag to select without first pressing the toggle —
then we revisit (b) with the upstream PR path. But (c) eliminates 99% of
the daily friction without that dependency.

---

## 8. Open questions / empirical follow-ups

1. **Empirical mouse-mode capture from `sprawl enter`.** `cmd/input_debug.go`
   does not set `v.MouseMode`, so it cannot serve as a stand-in (it'll show
   no `?100Xh` bytes regardless of what production does). To confirm the
   source-read claim end-to-end, run sprawl proper under `script(1)`:

   ```bash
   # in an isolated SPRAWL_ROOT
   make build
   eval "$(bash scripts/sprawl-test-env.sh)"
   script -q -c './sprawl enter' /tmp/qum617-stdout.log
   # press Ctrl-C immediately after the TUI paints once
   grep -aP '\x1b\[\?100[023][hl]' /tmp/qum617-stdout.log | head
   # expected: \x1b[?1002h and \x1b[?1006h on startup;
   #           \x1b[?1002l + \x1b[?1003l + \x1b[?1006l on quit.
   ```

   I did not run this in the ghost worktree because it would compete with
   the user's own `sprawl enter` session. The source-level confirmation is
   complete and the code path is exercised every render.

2. **Compatibility test for fix (b) (mode 1000).** If we ever pursue (b),
   the empirical question is "does click-only mode 1000 actually let
   xterm.js do native selection, given that the click event is still
   reported?" This needs to be tested on:
   * coder web terminal (xterm.js, primary target),
   * native macOS Terminal.app + iTerm2,
   * gnome-terminal + kitty + wezterm + Alacritty,
   * each of the above inside tmux 3.2a *and* tmux 3.4.

   Build a 30-line standalone Go program that emits `ESC[?1000h\x1b[?1006h`
   on startup and `ESC[?1000l\x1b[?1006l` on exit, with no other behavior.
   Try to drag-select. Record results. (Worth doing before any upstream
   PR — if mode 1000 doesn't help, fix (b) is dead anyway and the upstream
   work is wasted.)

3. **`Ctrl-/` keybind availability.** Confirm no terminal in the
   coder/tmux/iTerm2/Terminal.app/Alacritty matrix swallows `Ctrl-/`
   before it reaches sprawl. (kitty and modern xterm.js pass it through;
   tmux 3.2a does not consume it; macOS Terminal.app sends it as
   `0x1F` — the Bubble Tea key parser surfaces this as
   `KeyPressMsg{Code: 0x1F}` which we can match.)

4. **Interaction with the search overlay.** `internal/tui/app.go:1854`
   composes a `searchOverlay()`. Selection mode while a search overlay is
   active is probably nonsensical and should be ignored; worth a unit test.

5. **Modal interaction.** `anyModalUp()` already suppresses mouse events
   (`internal/tui/app.go:337–339`). Selection mode should be a no-op while a
   modal is up, or — more usefully — should still flip `MouseMode` to None
   so that mid-modal users can select text out of the modal body. Pick one
   in the implementation; the second option is more aligned with the
   ticket's user intent.

---

## 9. Reflections (per agent contract)

**Surprising / unexpected:**

* I expected Bubble Tea v2 to expose a `MouseModeButtons` (X11 1000) value
  given how prominent xterm's 1000/1002/1003 ladder is, but the library
  collapsed it to a binary "None / drag-tracking-on" choice plus the
  `AllMotion` extreme. The naming of `MouseModeCellMotion` actively
  obscures that 1002 captures drag — by name it sounds like a tame
  cell-aware reporting mode. This is exactly the kind of "library API
  hides the dangerous default" cluster QUM-608 also showed up in.
* The `QUM-280` comment at `app.go:1888–1892` already documents the exact
  tradeoff and even pre-suggests Shift+drag as the workaround. We've been
  one comment-in-source away from this fix for as long as that comment
  existed. Worth flagging as a "search known-tradeoff comments before
  spawning research" lesson.
* Bubble Tea v2's renderer-level diff of `MouseMode` is genuinely well-done
  — it converts toggle (c) from a "rewire the program initialization" job
  into a "flip a boolean in `View()`" job. Better than expected, makes (c)
  unambiguously the right answer.

**Open questions I'd investigate next:**

* The empirical `script(1)` capture (§8.1) — I deferred it to avoid
  stepping on the user's live session, but it's the only thing standing
  between "source-confirmed" and "byte-on-the-wire confirmed."
* Whether mode 1000 actually lets xterm.js native-select even when click
  is reported (§8.2). This is the only meaningful unknown that would
  change the recommendation between (c) and (b)+(c).
* Whether bubbles/v2 viewport responds to keyboard scroll keys without
  changes — quick code check, not done here.

**If I had more time:**

* Write the 30-line probe program for §8.2 and run it in the actual
  coder+tmux stack to settle the (b) compatibility question once and
  for all.
* Audit other Bubble Tea v2 view-level settings (`KeyboardEnhancements`,
  `ReportFocus`, `DisableBracketedPasteMode`) for similar "footgun by
  default" tradeoffs that we might be eating silently.

---

## 10. Citations

| Claim | Source | Lines |
|---|---|---|
| Sprawl sets `MouseModeCellMotion` on every frame | `internal/tui/app.go` | 1797–1798, 1886–1893 |
| Sprawl forwards all `tea.MouseMsg` to viewport | `internal/tui/app.go` | 330–343 |
| Viewport autoscroll inspects `MouseWheelMsg` only | `internal/tui/viewport.go` | 188–199 |
| Bubbles v2 viewport handles `tea.MouseWheelMsg` | `charm.land/bubbles/v2@v2.1.0/viewport/viewport.go` | 696–719 |
| Bubble Tea v2 `MouseModeCellMotion` = `?1002h` + `?1006h` | `charm.land/bubbletea/v2@v2.0.3/cursed_renderer.go` | 121–127 |
| Bubble Tea v2 diffs `MouseMode` across frames | same | 350–368 |
| Bubble Tea v2 resets mouse modes on close | same | 181–187 |
| `MouseMode` is declarative on `tea.View` | `charm.land/bubbletea/v2@v2.0.3/tea.go` | 175–177, 283–305 |
| ANSI bytes for the SGR/mouse modes | `github.com/charmbracelet/x/ansi@v0.11.7/mode.go` | 474, 486, 521 |
| Bubble Tea v2 upgrade table (no `MouseModeButtons`) | `charm.land/bubbletea/v2@v2.0.3/UPGRADE_GUIDE_V2.md` | 280–331 |
| `sprawl input-debug` does not set MouseMode | `cmd/input_debug.go` | 201–210 |
| `sprawl enter` constructs program with no mouse opts | `cmd/enter.go` | 262–276 |
| Ink does not enable mouse capture by default | Ink readme + QUM-608 citations table | — |
| xterm.js Shift forces native selection | xterm.js #1536, #2337, ITerminalOptions docs | — |
| tmux Shift+drag bypass (Linux/Windows) / Option (macOS) | tmux #1804, tmuxai.dev guide | — |
| QUM-280 comment already documents the tradeoff | `internal/tui/app.go` | 1888–1892 |
