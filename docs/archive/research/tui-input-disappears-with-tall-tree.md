> Terminology note (2026-06): pre-rename "sub-agent" = current "sidechain".

# TUI input bar disappears when agent tree grows tall

**Date:** 2026-05-06
**Investigator:** ghost (researcher agent)
**Pane:** sprawl `enter` under `SPRAWL_UNIFIED_RUNTIME=1`, terminal 190×49
**Reported symptom:** when ≥4 rows show in the tree panel (1 weave + 1 manager + 1 engineer + 1 grandchild engineer), the bottom input box vanishes. When agents retire and the tree shrinks, the input reappears.

## TL;DR

The vertical layout math in `internal/tui/layout.go::ComputeLayout` does correctly reserve `StatusHeight (1)` + `InputHeight (3..12)` rows before assigning the rest of the terminal height to tree/viewport/activity. However:

1. **lipgloss `Style.Height(h)` is a *minimum*, not a *maximum*.** The bordered tree/viewport/activity panels can grow taller than the assigned `TreeHeight` if their inner content has more visual rows than declared (whether from too many lines or from soft-wrapping wide lines). The composed `JoinVertical(mainRow, input, status)` therefore overflows the terminal, pushing input + status off the bottom.
2. **No `MaxHeight` is set.** `cachedPanel` calls `style.Width(w).Height(h).Render(content)` (`internal/tui/view_cache.go:154`). `Height` clamps the *minimum* row count and pads with whitespace; lipgloss does not truncate over-tall content. The only thing keeping the tree column from overflowing is `TreeModel.View()` self-limiting `len(nodes)` to `m.height` (`internal/tui/tree.go:160-162`) — which only protects the row count, not row width.
3. **A wide tree row → soft-wrap → panel grows by N lines → input clipped.** The author of `resizePanels` already knew this failure mode and called it out by comment: see `internal/tui/app.go:1693-1697`, which warns that the wrong inner-content width "lets long tree rows bleed past the border and soft-wrap, which then pushes the tree panel taller than its declared Height and clips the input box off the bottom of the screen (QUM-324 residual)." The same hazard re-surfaces whenever any individual tree row's *visible* width exceeds the inner content budget (off-by-one in cell-width math, embedded wide unicode, ANSI styles that lipgloss rewraps differently than `ansi.Truncate`).

The class of bug is real and documented; the specific instance is hard to reliably reproduce without the user's exact tree state.

## What I verified

### Layout math is correct in principle

`ComputeLayout(190, 49, 3)` (`internal/tui/layout.go:50-113`) reserves the bottom 4 rows for input + status:

```
mainHeight   = 49 - StatusHeight(1) - InputHeight(3) = 45
TreeHeight   = ViewportHeight = ActivityHeight = 45
```

Each of those 45's is then passed into `cachedPanel(...)` as `layout.<Panel>Height-2` (`internal/tui/app.go:1486-1500`), which lipgloss renders as **outer** panel height 43 (border included). MainRow is therefore 43 rows tall on this terminal, leaving 49 − 43 − 3 − 1 = **2 spare rows**. When the layout matches its declared sizes, the input bar fits.

### Synthetic repro at 190×49 with 4 nodes — does NOT reproduce

I added a temporary test that builds an `AppModel`, sends `WindowSizeMsg{190,49}`, populates `childNodes` with ratz/tower/finn (matching the reporter's tree shape), and counts the lines in `app.View().Content`. Result: 47 lines, input bar present at lines 43-45, status bar at line 46 — fits within 49.

I also scanned `n` from 4 to 60 (test deleted before commit). The output stays at 47-48 lines because `TreeModel.View()` self-clips row count to `m.height`. So *bare row count* alone is not the trigger.

### The known-residual failure mode

The `resizePanels` comment block (`internal/tui/app.go:1693-1697`) explicitly describes the symptom as a known QUM-324 hazard:

> "Passing only -2 here is an off-by-two that lets long tree rows bleed past the border and soft-wrap, which then pushes the tree panel taller than its declared Height and clips the input box off the bottom of the screen (QUM-324 residual)."

`clipTreeRow` (`internal/tui/tree.go:25-31`) clips to `m.width - rowPrefixWidth` cells using `ansi.Truncate`. If any row's *terminal-visible* width exceeds `m.width` (whether via a runewidth/uniseg disagreement on a wide-ambiguous codepoint like `●`, an unaccounted-for ANSI control, or a future change that adds an extra glyph to the row format), lipgloss will wrap it inside the panel border, growing the panel beyond `TreeHeight` and pushing input off the bottom. The current code has no defense against this regression.

### Why agent count correlates with the symptom

- More tree rows = more chances for any single row to slightly exceed the inner-content width budget. One wrap is enough to push input + status one row below the visible region.
- More retired agents accumulate in the tree before AgentTreeMsg garbage-collects them; each extra row is one more shot at the soft-wrap edge case.
- When agents retire and the tree shrinks back to the rows that don't trip the wrap, the panel returns to declared height and input reappears — exactly the user's observation.

## Recent commits considered

Diffed `internal/tui/{app,tree,viewport,layout,view_cache,input}.go` since 2026-04-22. None of QUM-279/280/281 (tree-yank), QUM-205 (root unread badge), QUM-476 (viewport sub-agent flatten — `viewport.go` only), or QUM-463 (spinner kick on child panel — `viewport.go`) changed layout or row formatting. The QUM-448 fix (`f9e3cec`) added input-growth re-sizing but only protects against the *input* growing; it does not cap the *tree* panel.

The `cachedPanel` cache (QUM-451) is not the culprit either: cache keys include `w, h, active`, so any size mutation invalidates and re-renders.

## Proposed fix shape

Two complementary defensive layers, both small (≈10–25 lines each):

### 1. Hard-cap each panel's outer height with `MaxHeight`

In `internal/tui/view_cache.go::renderPanel` (line 147-155):

```go
return style.Width(w).Height(h).MaxHeight(h).MaxWidth(w).Render(content)
```

`MaxHeight(h)` truncates over-tall content to exactly `h` rows. `MaxWidth(w)` matches. With both Width/MaxWidth and Height/MaxHeight set, the bordered panel is guaranteed to be exactly `w × h` cells — soft-wraps clipped, no overflow into the row below.

This is the minimal fix and it's the canonical lipgloss idiom for "fixed-size panel" — see lipgloss godoc and the bubbletea `examples/composable-views` / `examples/split-editors` patterns where dashboards reserve a footer.

### 2. Add a regression test mirroring `app_input_overflow_test.go` for tree growth

`internal/tui/app_tree_overflow_test.go` (new, ≤80 lines): seeds `childNodes` with a tall stack of nodes whose `LastReportMessage` has worst-case width (mixed wide unicode + ANSI), sends `WindowSizeMsg{190, 49}`, asserts `len(strings.Split(View().Content, "\n")) <= 49` and that the `"Type a message..."` placeholder appears in the output.

This locks down the invariant the user reported and gates against future "QUM-324 residual" regressions.

### 3. Optional: scrollable tree with explicit row budget

If we anticipate trees getting genuinely deep (≫ TreeHeight rows), use bubbles/viewport for the tree panel as well, with the same `SetSize` discipline as the message viewport. This converts row count from "render-everything" to a windowed scroll; depth-50 trees stay fixed-height.

## Bubble Tea / lipgloss canonical pattern

The standard bubbletea layout idiom for "always-visible footer" is:

1. Receive `tea.WindowSizeMsg`, store `w, h`.
2. Compute footer height as a constant or from the footer's content lines: `footerH := lipgloss.Height(footer.View())`.
3. Subtract footer + any header from `h` to get the body height: `bodyH := h - footerH - headerH`.
4. Pass `bodyH` as the outer height of every body panel via `style.Width(w).Height(bodyH).MaxHeight(bodyH).Render(...)`. **Always pair `Height` with `MaxHeight`** when the panel must not overflow.
5. Compose with `lipgloss.JoinVertical(lipgloss.Left, header, body, footer)`.

Sprawl follows steps 1-3 and 5 already; the missing piece is step 4's `MaxHeight` clamp. References: `github.com/charmbracelet/bubbletea/tree/main/examples/composable-views`, `github.com/charmbracelet/bubbletea/tree/main/examples/split-editors`, lipgloss `Style.MaxHeight` godoc (`pkg.go.dev/github.com/charmbracelet/lipgloss#Style.MaxHeight`).

## Reflections / open questions

- **Surprising:** I could not synthetically reproduce at the reporter's exact tree shape. The bug is conditional on something more than row count — likely a row whose *displayed* cell width exceeds `m.width-rowPrefixWidth` despite `ansi.Truncate` clipping. Candidates: a wide-ambiguous codepoint (●) rendered as 2 cells in the user's terminal, or a costTag/LastReportMessage that includes a multi-cell glyph.
- **Open:** what is the exact tree-row content in the reporter's failing snapshot? A capture of `app.tree.View()` at the moment of failure (with ANSI stripped + `ansi.StringWidth` measurement per line) would pin it down. Worth adding a debug `Ctrl+D` dump in a follow-up if this recurs.
- **Open:** is the same hazard latent in the viewport panel (long tool-call output line that escapes its inner-width clip)? Likely yes — the proposed `MaxHeight` fix protects all three panels in one shot.
- **Next investigation:** if `MaxHeight` alone doesn't end the symptom, instrument `cachedPanel` to log when `lipgloss.Height(rendered) > h` and ship a diagnostic build to the reporter.
