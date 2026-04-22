# TUI Render Corruption — Diagnosis (2026-04-22)

**Reporter:** trace (researcher)
**Evidence:** `docs/research/tui-render-corruption-2026-04-22.txt` (pane capture of `sprawl enter`)
**Scope:** Investigation only. No production code modified.

## TL;DR

The TUI tree panel renders each row's `LastReportSummary` verbatim, with **no truncation, no wrapping, and no newline stripping**. When a child agent reports a long (hundreds-of-chars) or multi-line summary via `sprawl report …`, that summary bleeds past the tree panel's right border and corrupts the rest of the frame — the tree's right `│`, the viewport's left `│`, and in some cases the viewport's content column all shift or disappear.

The small 2-char fragments floating to the right of the viewport (`ep`, `AD`, `te`, `tr`, `da`, `s`, `es`, `ng`, `fe`, `ph`) are the tail ends of similarly-unbounded content rendered inside the viewport's tool-call block (`renderToolCall`), which writes the raw `ToolInput` JSON without any wrap/clip. The `bubbles/v2` viewport's default clip-and-split does not catch long ANSI-styled single lines reliably when the outer `lipgloss` Style only sets a minimum width.

This is a regression introduced when the tree began displaying `LastReportSummary` (QUM-295, commit `d5e2a59`). The `InboxArrivalMsg` path from QUM-311/312 is **not** the trigger — those banners are short status lines and render cleanly.

## Evidence — what each fragment is

In `docs/research/tui-render-corruption-2026-04-22.txt`, the corrupting text is traceable character-for-character to a single source: the commit message of the current `HEAD` (`f378a6b`), which is the `tower` agent's last `sprawl report done "…"` summary.

| Fragment in pane capture | Position in `f378a6b` commit message                                     |
| ------------------------ | ------------------------------------------------------------------------ |
| `is now at f378a6b tower`| (fragment of longer checkout-style string rendered into summary)         |
| `ted for EC2 (weave→sprawl_` | `…commit``ted for EC2 (weave→sprawl_``spawn→ghost…`                  |
| `ee), and EC6 (Session Er`   | `…sprawl_retr``ee), and EC6 (Session Er``ror dialog…`                |
| single-char bleeds `m`, `a v`, `o`, lone letters | interior characters of the same summary      |

That summary is >1200 characters long, single-line as received by `agentops.Report` but effectively wrapping across the tree panel when `lipgloss` composes the frame.

The 2-char trailing fragments (`ep`, `AD`, `te`, `tr`, `da`, `s`, `es`, `ng`, `fe`, `ph`) are the last 2 chars of long JSON lines emitted by `ViewportModel.renderToolCall` for the `ToolSearch` and `mcp__sprawl-ops__sprawl_spawn` tool blocks — each JSON payload line is slightly wider than the inner viewport cell count, and the overflow escapes past the right `│`.

## Root cause (primary): Tree `LastReportSummary` rendered with no size guard

**File:** `internal/tui/tree.go`

```go
func (m TreeModel) View() string {
    ...
    for i, node := range m.nodes {
        if i >= m.height && m.height > 0 { break }         // counts logical nodes, not rows
        indent := strings.Repeat("  ", node.Depth)
        icon := typeIcon(node.Type)
        dot := m.theme.ReportDot(node.LastReportState)
        var line string
        if node.LastReportSummary != "" {
            line = fmt.Sprintf("%s%s %s %s — %s",
                indent, dot, icon, node.Name, node.LastReportSummary)   // <-- unbounded
        } else {
            line = fmt.Sprintf("%s%s %s %s (%s)", indent, dot, icon, node.Name, node.Status)
        }
        if node.Unread > 0 { line += fmt.Sprintf(" (%d)", node.Unread) }

        if i == m.selected {
            b.WriteString(m.theme.SelectedItem.Render(fmt.Sprintf("> %s", line)))    // <-- no Width()
        } else {
            b.WriteString(m.theme.NormalText.Render(fmt.Sprintf("  %s", line)))      // <-- no Width()
        }
        ...
    }
    return b.String()
}
```

Problems, in order of severity:

1. **No truncation of `LastReportSummary`** — `tree.go:136`. The `summary` parameter handed to `agentops.Report` is stored verbatim as `state.LastReportMessage` (see `internal/agentops/report.go:140`) and surfaces all the way to the tree row. There is no length cap and no newline stripping anywhere between `sprawl report <summary>` and `fmt.Sprintf`.

2. **`NormalText` / `SelectedItem` styles have no `.Width()`** — `internal/tui/theme.go:82-91`. `lipgloss.Style.Render` on a string longer than the panel's inner width does not wrap by default; the rendered line is exactly as wide as the content.

3. **Outer `treeBorder` width is a minimum, not a maximum** — `internal/tui/app.go:607-610`:
   ```go
   treeBorder := m.borderStyle(PanelTree).
       Width(layout.TreeWidth - 2).
       Height(layout.TreeHeight - 2)
   treeView := treeBorder.Render(m.tree.View())
   ```
   `lipgloss/v2` `Style.Width(n)` pads shorter content up to `n` but does not clip or wrap content wider than `n`. When any line of the inner content exceeds `TreeWidth-2`, the rounded right border is pushed right along with the overflow — or, more commonly, interacts with `lipgloss.JoinHorizontal(Top, treeView, vpView)` (`app.go:626-628`) in ways that produce the exact artefact seen: some rows keep their tree `│`/`│` pair, others have it silently eaten by overflowing glyphs, and the viewport's left border/content shift on those rows.

4. **Secondary: the `i >= m.height` break counts logical nodes, not rendered rows.** If `LastReportSummary` happens to contain literal `\n`, a single node produces multiple display rows and exceeds `TreeHeight`. That further confuses `JoinHorizontal` line-pairing with the viewport column, which *does* honor its `Height`. (Not required to reproduce the primary symptom; makes it strictly worse.)

## Root cause (secondary): tool-call input rendered with no size guard

**File:** `internal/tui/viewport.go:326-342`

```go
func (m *ViewportModel) renderToolCall(sb *strings.Builder, msg MessageEntry) {
    indicator := "⏳"
    if msg.Approved { indicator = "✓" }
    toolHeader := fmt.Sprintf("┌ %s %s", indicator, msg.Content)
    sb.WriteString(m.theme.AccentText.Render(toolHeader))
    if msg.ToolInput != "" {
        sb.WriteString("\n")
        inputLine := fmt.Sprintf("│ %s", msg.ToolInput)       // <-- unbounded
        sb.WriteString(m.theme.NormalText.Render(inputLine))  // <-- no Width()
    }
    sb.WriteString("\n")
    sb.WriteString(m.theme.AccentText.Render("└"))
}
```

`msg.ToolInput` is the already-summarized tool-input string produced by the bridge, but for tools like `mcp__sprawl-ops__sprawl_spawn` the JSON (`{"branch":"dmotles/smoketest-tui-notify","family":"engineering","prompt":"…"}`) is still well over 100 characters. The bubbles/v2 `viewport.Model.SetContent` only soft-wraps when its `SoftWrap` option is enabled, and the current `NewViewportModel` does not enable it (see `viewport.go:64-74`). The result is a slice of very long ANSI-styled single lines that extend past the viewport's right border.

The 2-char right-side fragments are the trailing portion of each such long line, reaching the column after the viewport's right `│` because the border is drawn at position `Width` regardless of content length.

## Contributing factor: East-Asian-ambiguous glyphs

Tree and viewport content both contain characters whose display width is ambiguous (`●` U+25CF from `ReportDot`, `→` U+2192, `▍` U+258D streaming cursor, box-drawing `┌│└├`). `lipgloss/v2` uses `mattn/go-runewidth` or `uniseg` to measure; depending on the terminal's East-Asian-Width setting, these may be one cell in practice but measured as two (or vice versa), which silently shifts alignment by a fixed offset per line. This is **not** the primary cause — the symptom shape is dominated by the unbounded-string bug — but it explains why the overflow amount differs per row and why some fragments are 1 char wide and others 2.

## Reproduction hypothesis

Two deterministic ways to reproduce from a clean sandbox:

1. **Long-summary repro (tree bleed):**
   ```bash
   sprawl enter                                 # in one pane
   # in another pane, as a spawned child agent:
   sprawl report done "$(git log -1 --format=%B HEAD~0)"   # or any >200 char summary
   ```
   Within 2 s (the `tickAgentsCmd` poll interval, `app.go:825`) the child's row in the tree panel will render the whole summary, overflowing the tree column and garbling the frame exactly as in the evidence.

2. **Long tool-input repro (viewport bleed):**
   Invoke any MCP tool whose `ToolInput` summary exceeds the viewport width (e.g. `mcp__sprawl-ops__sprawl_spawn` with a long prompt). The `│ …` line under the tool header will overflow the viewport's right `│`, producing the 2-char trailing fragments.

Combined — which is the situation in the evidence — the two bleeds visually overlap because `JoinHorizontal` places the viewport column at the right of whatever the tree column rendered as.

## Recommended fix shape

Do not fix in this investigation; shape only.

1. **Truncate the tree row line at the known inner width.** `TreeModel.SetSize` already receives `w`; use it to clip each rendered `line` to `w - len(indent) - len(prefix "> ")` runes *after* ANSI-stripping, with an ellipsis when cut. Stripping embedded `\n` in `LastReportSummary` before the `Sprintf` is also cheap insurance. `lipgloss.Width` + `runewidth` (or the `charmbracelet/x/ansi` helpers already vendored) give the correct visual width.

2. **Set a hard width on `NormalText` / `SelectedItem` rows** via `lipgloss.NewStyle().MaxWidth(w)` (v2) or a post-render `ansi.Truncate` so overflow is actually clipped, not just unpadded.

3. **Cap `LastReportMessage` at the source.** `agentops.Report` (`internal/agentops/report.go:140`) should trim `summary` to a sane length (e.g. 200 chars, single line) and stash the full text in `LastReportDetail` (which already exists and is already not displayed in the tree). This is a belt-and-suspenders guard that also benefits any other consumer of `LastReportMessage`.

4. **Clip `msg.ToolInput` in `renderToolCall`** (`internal/tui/viewport.go:337`) to the viewport's inner width — either by truncation with an ellipsis, or by enabling `bubbles/v2` `viewport.Model.SoftWrap = true` so the viewport wraps long lines instead of letting them escape.

5. **Fix the `i >= m.height` loop guard** in `TreeModel.View` to count *rendered rows*, not nodes, so multi-line summaries can't push the tree past `TreeHeight`. Easier fix: do (1) and (3) so summaries are always single-line-bounded, making this a non-issue.

Tests to add alongside a fix:
- `TestTreeView_LongSummaryTruncated` — render a tree with a 2 000-char `LastReportSummary` into a 30-wide panel, assert `lipgloss.Width(line) ≤ 30` for every output line.
- `TestTreeView_MultilineSummaryStripped` — pass `"line1\nline2\nline3"`, assert exactly one tree row per node.
- `TestRenderToolCall_LongInputClipped` — same for the viewport's tool-input block.
- E2E addition in `scripts/test-tui-e2e.sh`: spawn a child, have it `sprawl report done <huge>`, `tmux capture-pane`, assert no column past viewport's right `│` contains non-space characters.

## Files / call-sites to reference in the fix

| Area                         | File                          | Line(s)   |
| ---------------------------- | ----------------------------- | --------- |
| Tree row formatting (primary)| `internal/tui/tree.go`        | 131-148   |
| Tree height loop guard       | `internal/tui/tree.go`        | 127-128   |
| Summary source-of-truth      | `internal/agentops/report.go` | 138-142   |
| AgentInfo persistence        | `internal/supervisor/real.go` | 191-192   |
| Viewport tool-call block     | `internal/tui/viewport.go`    | 326-342   |
| Viewport construction (no SoftWrap) | `internal/tui/viewport.go` | 64-74 |
| Outer border width composition | `internal/tui/app.go`       | 606-629   |
| Layout sizing contract       | `internal/tui/layout.go`      | 45-100    |
| Styles without `.Width()`    | `internal/tui/theme.go`       | 82-96     |

## What I would investigate next (if more time)

- **Measure `lipgloss.Width` vs actual terminal cell width** for `●`, `→`, `▍`, and the box-drawing glyphs under the three terminals we care about (tmux-in-alacritty, tmux-in-iTerm2, coder's web terminal). If any of them disagrees with the library's width, a single `runewidth.EastAsianWidth = false` at TUI start may be worth pinning.
- **Audit every `lipgloss.Style.Render(long_string)` call site in `internal/tui/`** for the same class of bug. `renderToolCall` is almost certainly not the only one — any status/error message with embedded markup would exhibit it. `grep -n "Render(fmt.Sprintf" internal/tui/` is a decent seed.
- **Confirm bubbles/v2 `viewport.Model` wrap semantics** on the currently-pinned version. If `SoftWrap` defaults to `false`, that is effectively a foot-gun for the whole project and should be flipped in `NewViewportModel`.
- **Check if any other `AgentInfo.LastReport*` field leaks unbounded text into the UI** (e.g. the status-bar, palette, or the new activity panel).

## Reflections

- **Surprising:** the regression pathway is unrelated to the `InboxArrivalMsg` / QUM-311-312 work that was suspected. The new short-banner path is well-behaved; the pre-existing but newly-displayed `LastReportSummary` field (QUM-295) is the actual culprit. Worth recording in the QUM-295 retro / Linear thread that "rendering a data field the tree was already carrying" can regress frame rendering without any new data-flow code.
- **Open question:** whether `lipgloss/v2` has a Width-as-max mode we're supposed to be using. The v1 `Style.MaxWidth` is a thing; the v2 migration may have silently dropped it in favour of explicit `ansi.Truncate`. Docs dive + one-line fix if it exists.
- **What I'd do next with more time:** add an `ansi.Truncate`-based clipping helper to `internal/tui/` and sweep every panel through it, with a golden-output E2E that intentionally overfeeds every panel with 5000-char strings. This bug class will re-appear otherwise.

## Linear

Filed as issue QUM-…  — see status message for key.
