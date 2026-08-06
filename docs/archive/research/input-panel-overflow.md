# TUI input-panel vertical overflow

**Status:** diagnosed (research only, no code changes)
**Branch / commit:** `dmotles/input-panel-overflow-research` against `ffb04c7`
**Reporter:** dmotles
**Tracker:** Linear `Bug` (filed alongside this doc)
**Files implicated:** `internal/tui/app.go`, `internal/tui/layout.go`, `internal/tui/input.go`

## Symptom

When dmotles types or pastes enough content into the TUI input panel that the
textarea grows tall, the input panel grows DOWNWARD past the bottom of the
terminal window. The status bar disappears below the visible area first, then
the bottom rows of the input panel itself (where the cursor sits) also scroll
off-screen.

Expected behaviour (per dmotles): either (a) the viewport / tree / activity
panes shrink to make room (panel grows upward into their space), or (b) the
input panel overlays the other panes when it exceeds some height threshold.

## Layout architecture (recap)

The TUI uses Bubble Tea v2 + Lipgloss v2. The full screen is composed in
`AppModel.View()` (`internal/tui/app.go:1306`). Every render it:

1. Calls `ComputeLayout(width, height, inputBoxHeight())`
   (`internal/tui/layout.go:50`) which:
   - clamps `inputHeight` into `[defaultInputHeight=3, maxInputHeight=12]`,
   - assigns `StatusHeight = 1`,
   - sets `mainHeight = height - StatusHeight - InputHeight`,
   - and assigns that `mainHeight` to `TreeHeight`, `ViewportHeight`, and
     `ActivityHeight`. So the math itself is correct: the layout DOES shrink
     the upper panes when input grows.
2. Renders each panel inside a `lipgloss` border with `.Width(...)` and
   `.Height(layout.<X>Height - 2)`.
3. Stacks them with `lipgloss.JoinVertical(Left, mainRow, inputView, statusView)`
   (`app.go:1369-1374`).

The textarea is configured at `input.go:51-65` with `DynamicHeight = true`,
`MinHeight = 1`, `MaxHeight = maxInputLines = 10`. So the textarea itself
caps at 10 rows; total input box (with border) caps at 12. That cap matches
`maxInputHeight` in layout.go.

So far, so good — the *math* never asks for more rows than the terminal has.

## Root cause

The bug is **not** in `ComputeLayout` and **not** in the textarea's
self-clamping. It is in size propagation.

`AppModel.resizePanels()` (`app.go:1485`) is what actually pushes computed
panel sizes down to the cached `tree`, `viewport(s)`, `activity`, and `input`
sub-models via their respective `SetSize`/`SetWidth` calls. But
`resizePanels()` is invoked **only twice**:

- on `tea.WindowSizeMsg` (`app.go:305`)
- on `AgentSelectedMsg` (`app.go:1082`)

It is **not** called when a keystroke or paste grows the textarea. The input
keypress flow goes:

```
PanelInput keys → m.input.Update(msg)        // app.go:1626 (and 332 for paste)
                → textarea.recalculateHeight // bubbles textarea.go:1678
                → textarea.SetHeight(h)      // grows internal viewport
                → returns control            // resizePanels never runs
```

Now consider the next `View()` call:

- `inputBoxHeight()` returns the new (larger) textarea height + 2
  (`app.go:1540`).
- `ComputeLayout` with the new input height returns a smaller `ViewportHeight`,
  `TreeHeight`, `ActivityHeight`.
- `View()` happily renders each border with `.Height(layout.X - 2)`.
- BUT — and this is the kicker — `lipgloss.Style.Height(N)` is a **minimum**,
  not a maximum (verified in
  `lipgloss/v2@v2.0.3/align.go:61-82` `alignTextVertical`: when the rendered
  string already has more than `N` lines, it is returned **unchanged**).
- The cached `ViewportModel` / `TreeModel` / `ActivityPanelModel` were last
  sized by `resizePanels()` for the **old** (smaller) input height, so their
  `View()` outputs are still ~9 rows taller than the current layout asks
  for.
- `JoinVertical` happily concatenates: oversized `mainRow` + new larger
  `inputView` + `statusView` → total exceeds the terminal height by exactly
  the delta `inputBoxHeight() - prevInputBoxHeight()`.

In altscreen mode the terminal does not scroll; rows past the bottom just
get dropped. The status bar — which is the **last** thing concatenated — is
the first to vanish, followed by the bottom of the input box itself.
Exactly the symptom dmotles described.

### Why the textarea's `MaxHeight=10` doesn't save us

It bounds growth to ~12 outer rows. With `maxInputHeight=12` and
`statusBarHeight=1`, the steady-state overflow is at most ~9 rows (default
input is 3, max is 12, delta = 9). On a 24-row terminal that is enough to
push the status bar **and** the input cursor off-screen. Matches the
report.

### Why this only manifests for the input box

The viewport, tree, and activity sub-models are sized by `resizePanels()`,
which only runs on window resize / agent switch. Their `.Height()`-rendered
output is locked to whatever the layout was at that moment. Input is the
only sub-model whose visible height is **content-driven inside its own
Update path**, so it's the only one whose effective height drifts away from
the last-cached layout between `resizePanels()` calls.

## Reproducibility / evidence

- Static read confirms the gap: `Grep resizePanels\(` returns exactly two
  call-sites, both non-input (`app.go:305`, `app.go:1082`).
- Layout math itself is fine — see `TestComputeLayout_DynamicInputHeight` and
  `TestComputeLayout_InputHeightClampedToMax` in `layout_test.go`. Neither
  exercises the *integration* between an input-height bump and the cached
  sub-model `SetSize` state.
- `lipgloss.Style.Height(N)` semantics: pad-to-N, never truncate. Verified
  at `lipgloss/v2@v2.0.3/align.go:61-82`. (`MaxHeight(N)` is the truncating
  variant; we don't use it.)
- Bubbles textarea View() **does** clip to its internal height
  (`bubbles/v2@v2.1.0/textarea/textarea.go:1451-1460`, viewport at
  `viewport/viewport.go:728-750`), so the input box itself is well-behaved
  in isolation. The overflow comes from the un-resized sibling panels above
  it.

## Recommended fix (opinionated)

dmotles preferred (a) — shrink the other panes. The layout *already does
this* in math; we just need to propagate it. The single-line gist:
**call `resizePanels()` after any `m.input.Update(msg)` that may have
changed `inputBoxHeight()`.**

### Patch sketch (~10 LOC, two call-sites)

`internal/tui/app.go`:

```go
// PasteMsg branch (~line 332)
case tea.PasteMsg:
    if m.observedAgent != m.rootAgent || m.activePanel != PanelInput || m.showHelp || m.showConfirm || m.showError || m.showPalette {
        return m, nil
    }
    prevH := m.inputBoxHeight()
    var cmd tea.Cmd
    m.input, cmd = m.input.Update(msg)
    if m.ready && !m.tooSmall && m.inputBoxHeight() != prevH {
        m.resizePanels()
    }
    return m, cmd
```

```go
// PanelInput branch (~line 1624)
case PanelInput:
    prevH := m.inputBoxHeight()
    var cmd tea.Cmd
    m.input, cmd = m.input.Update(msg)
    if m.ready && !m.tooSmall && m.inputBoxHeight() != prevH {
        m.resizePanels()
    }
    return m, cmd
```

That's it. The layout already enforces `min(content, maxInputHeight=12)`,
the textarea already self-clamps at `MaxHeight=10`, the panes already get
`mainHeight = height - 1 - InputHeight`. The fix just makes
`resizePanels()` run when the input grows, so the cached
`tree/viewport/activity` `SetSize` matches the layout being rendered this
frame.

### Regression test (TDD red-phase candidate)

Add to a new `internal/tui/app_input_overflow_test.go`:

```go
func TestInputGrowth_DoesNotOverflowTerminal(t *testing.T) {
    app := newBleedApp(t)
    updated, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
    app = updated.(AppModel)

    // Simulate a paste that fills the textarea past defaultInputHeight.
    big := strings.Repeat("line\n", 20)
    updated, _ = app.Update(tea.PasteMsg{Content: big})
    app = updated.(AppModel)

    rendered := app.View().String() // assuming View().String accessible; if
                                    // not, render via tea.View ToString helper
    lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
    if len(lines) > 24 {
        t.Fatalf("rendered %d lines, terminal only has 24 — input overflow regression", len(lines))
    }
}
```

(Adjust to whatever the existing test harness exposes — see
`testutil_test.go` and `app_paste_test.go` for the in-tree pattern.)

### Estimated scope

- Production code: **~10 LOC** in `app.go` (two `if` blocks around existing
  `m.input.Update` calls).
- Test: **~20 LOC** for one regression test exercising overflow + one
  baseline test confirming non-input paths are unaffected.
- Risk: low. `resizePanels()` is already idempotent and called from two
  other paths. The only new behaviour is "also call it when input grows."

## Alternatives considered

- **Recompute viewport/tree/activity sizes inside `View()` itself** (i.e.
  call `SetSize` from the render path). Works but mixes write-state into
  what should be a pure render, and Bubble Tea models pass by value through
  `View()` so the writes wouldn't persist anyway. Reject.
- **Lipgloss compositor / `lipgloss.Place` overlay** (option b in the
  brief). Bubble Tea v2 has `tea.NewView` with an `AltScreen` flag — the
  TUI already uses it. Lipgloss `Place` is used today for the help /
  palette / error / confirm modals (`palette.go:288,333`,
  `error_dialog.go:76`, `help.go:81`). An overlay could be added if dmotles
  later prefers (b), but it's strictly more code: a separate render path
  for "tall input" mode plus z-order management. With option (a) the
  layout naturally degrades — when the input wants 12 rows on a
  too-small terminal, the layout will give it those 12 and starve the
  upper panes (down to 0 rows if needed); `IsTooSmall` already gates the
  pathological case at 10 rows / 40 cols. The overlay design is
  unnecessary unless dmotles wants a UX where the upper panes stay
  fully visible even while input is tall. Defer.
- **Truncate / `MaxHeight` the input border style** (i.e. force lipgloss
  to clip rather than pad). Treats the symptom, not the cause —
  the *cached* viewport/tree heights would still be wrong, just clipped
  invisibly, with viewport scroll state out of sync with what's drawn.
  Reject.

## Open questions / next steps

- Should we also clamp `maxInputHeight` to a fraction of terminal height
  (e.g. `min(12, height/2)`) so a 16-row terminal doesn't end up with
  3-row tree/viewport panes? Probably yes, but that's a follow-up — the
  current `IsTooSmall` floor of 10 rows already prevents the worst case,
  and the current bug is the size-propagation gap, not the cap value.
- Worth checking whether the `searchOverlay()` path (`app.go:1368`) suffers
  from the same drift — it is rendered between `mainRow` and `inputView`
  and isn't sized by `resizePanels()`. Out of scope for this fix; flag
  for follow-up.

## Reflection

- **Surprising:** the layout math is correct end-to-end. I expected the
  bug to be in `ComputeLayout` (no max, or status-bar derived from input
  height). It's actually a state-propagation gap — the math runs every
  render, but the sub-models' cached `SetSize` only runs on two specific
  events.
- **Surprising:** `lipgloss.Style.Height(N)` doesn't truncate. That's a
  generic footgun in this codebase — anything that renders a sub-View
  inside a fixed-height border is one un-resized child away from
  overflow.
- **Open:** does `searchOverlay` have the same hazard? (Suspect yes —
  it's rendered with no explicit height bound and stacked above the
  input.) Worth a follow-up bug if confirmed.
- **Would investigate next:** wire up a render-time invariant assertion
  in tests (`len(lines) <= termHeight`) so this class of bug is caught
  by any future refactor — not just the input-grow path.
