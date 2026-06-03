# TUI Chassis Port — Scoping (Slice 0)

Port the spike's visual chassis (`origin/dmotles/tui-chat-spike` @ `5245510`)
into `internal/tui/app.go` on main (`6605cb9`) without touching input, MCP,
modals, or supervisor wiring.

## 1. Spike files with the chassis

In `internal/tuichat/`:

- `chat.go` (487) — `View()`, `layout()`, `refreshContent`, glamour.
- `items.go` (212) — item renderers + `wrap`/`prefixLines`.
- `styles.go` (20) — ANSI-color styles **with no `Background()`**: terminal-
  native bg shows through.
- `header.go` (126), `tree.go` (462) — already mirrored in main as `header.go`
  + `tree_orbital.go` from Phase 2/2.5.

The chassis dmotles loved = `chat.go:View()` + `styles.go` + no panel borders.
Wordmark+orbital header already lives in main.

## 2. Source of the border/bg drift in main

All panels flow through `cachedPanel` → `renderPanel`
(`internal/tui/view_cache.go:142-154`) which applies:

- `theme.ActiveBorder` / `InactiveBorder` (`theme.go:95-102`):
  `Border(RoundedBorder).BorderForeground(...).Background(BgBase)`.
- `BgBase = lipgloss.Color("233")` (`colors.go:56`) — the off-black tint.
- Every text style in `theme.go:103-143` chains `.Background(bg)`, bleeding
  tint into content.

Call sites: `app.go:1715-1724` (panel renders + `cachedMainRow`).

## 3. Portable / adapt / delete

- **Portable as-is**: `styles.go`.
- **Adapt**: View() pattern `header + chat + gap + input + status`. Main's
  `renderView` (`app.go:1686-1782`) already has this shape — strip the border
  wrap.
- **Delete from main**: `Border(...)` + `Background(bg)` chains in `theme.go`;
  the border render in `view_cache.go:142-154` (becomes pass-through); the
  `cachedMainRow` JoinHorizontal. Keep `StatusBar.BgLessVisible` tint.
- **NOT slice 0**: `items.go`, glamour, app-owned message list, turn-driver —
  that's the reverted Phase 3.

## 4. LOC delta

Net **~ -40 to -60 LOC**, no new files. ~25 tests/goldens regolden
(`app_view_cache_test.go`, `app_panel_outer_size_test.go`, `app_bleed_test.go`,
`*_palette_test.go`, `internal/agent/testdata/*_tui.golden`).

## 5. Top 3 risks

1. **Width math assumes border**. `renderPanel` treats `w,h` as *outer* dims
   (QUM-501, `view_cache.go:136-141`). Removing the border without adjusting
   `ComputeLayout` leaks 2 rows × 2 cols.
2. **`defaultInputHeight = 3`** (`layout.go:6`) is "1 line + 2 border cells".
   Drop to 1 or the input bar looks padded.
3. **View cache + goldens**. Paste-burst perf (QUM-451) lives in
   `cachedPanel`; keep memoization even when the wrap is pass-through.
   `notify-tui` matrix row will need a golden refresh.

The spike does NOT rely on app-owned message ownership for the chassis — that
ownership question is Phase-3, not chassis.

## 6. Tractable as one slice?

**Yes, marginal.** ~50 LOC code + golden churn. Budget ≈ **1 engineer-day**,
under 500 LOC with goldens. Recommend single PR: "slice 0: remove panel
border + bg tint, terminal-native chassis."
