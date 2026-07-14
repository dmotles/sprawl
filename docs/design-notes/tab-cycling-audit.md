# Tab Panel-Cycling Audit (QUM-694)

> **SUPERSEDED by QUM-695 — 2026-06-05.**
> The audit landed on option (b) "drop the hint, keep cycling"; dmotles
> immediately escalated to option (c) "delete cycling outright" since
> viewport yank-mode (the sole behavior cycling unlocked) was never used.
> QUM-695 removed `activePanel`, the Panel enum, the Tab/Shift+Tab cycler,
> and the entire `v` → `j`/`k`/`y` yank workflow. This document is retained
> as historical research; it does not describe current behaviour.

**Status:** superseded (research only)
**Branch:** `dmotles/qum-694-tab-audit-research`
**Date:** 2026-06-05

## Scope

Determine what behavior is uniquely gated by `activePanel` (the field cycled
by Tab / Shift+Tab in `internal/tui/app.go`), and decide whether to:

  - **(a)** keep cycling + the `tab: cycle panel` placeholder hint as-is,
  - **(b)** remove the hint but keep cycling,
  - **(c)** remove cycling entirely (delete `activePanel`, simplify dispatch).

The hint string lives at `internal/tui/input.go:18`:

```go
const inputPlaceholderHelp = "/: commands • ?: help • tab: cycle panel • ctrl+c: clear/quit"
```

## Method

1. Read every site that branches on `m.activePanel` or `Panel{Tree,Viewport,Input}`
   in `internal/tui/` (Grep-confirmed; results are exhaustive, not sampled).
2. Read `tree.Update`, `viewport.Update`, `input.Update` and the `delegateKey`
   switch to confirm the dispatch surface each panel exposes.
3. Empirical: launched `./sprawl enter` in a 200×50 detached tmux pane via
   the sandbox harness, drove keystrokes, and captured the pane after each.
   See "Empirical evidence" below for raw transcript.

## Per-panel behavior matrix

The table lists keys / behaviors that are **only** active when the named
panel is the active panel. Behaviors available regardless of `activePanel`
(global) are listed separately at the bottom.

| Panel | Unique gated behavior | Code site |
|---|---|---|
| **PanelInput** | Character keystrokes inserted into the textarea. | `delegateKey` → `m.input.Update(msg)` (`app.go:2327`) |
| PanelInput | `Up` / `Down` walks shell history (when cursor at first/last line). | `app.go:461` (`activePanel == PanelInput` gate) |
| PanelInput | `Ctrl+R` enters reverse-search mode (stashes current input). | `app.go:441` |
| PanelInput | `/` on empty input opens the command palette (`OpenPaletteMsg`). | `input.go:134` (only fires via input.Update, which only runs on PanelInput) |
| PanelInput | `?` is typed as a literal char rather than toggling Help. (`?`-as-help is gated `activePanel != PanelInput`.) | `app.go:494` |
| PanelInput | Bracketed-paste (`PasteMsg`) is forwarded to the textarea. | `app.go:404` (`activePanel != PanelInput` → drop) |
| **PanelTree** | `Up` / `Down` / `j` / `k` move the tree cursor. | `tree.go:172` via `delegateKey` |
| PanelTree | `Enter` emits `AgentSelectedMsg{Name: <selected>}` — switches observed agent. | `tree.go:183` |
| **PanelViewport** | `PgUp` / `PgDn` / arrow scroll keys move the viewport. (Note: mouse wheel works regardless — see globals.) | `delegateKey` → `vp.Update(msg)` (`app.go:2322`); the `KeyPressMsg` switch in `viewport.go::Update` is reached only via that delegation. |
| PanelViewport | `v` enters viewport select-mode (yank workflow); `j`/`k`/`g`/`G`/`y`/`Esc` then drive selection + clipboard yank. | `app.go:618` (`activePanel == PanelViewport` gate around `handleViewportSelectKey`) |

### Behaviors that do NOT depend on `activePanel` (globals)

These remain available no matter which panel is "focused":

  - **Mouse wheel scroll** on the viewport. `MouseMsg` is handled by the
    top-level `Update` and forwarded directly to `m.observedVP()` *before*
    the `activePanel` switch ever runs (`app.go:385`). Confirmed in the
    code; not empirically retested here.
  - **`Ctrl+N` / `Ctrl+P`** — cycle the observed agent linearly. Works from
    any panel (`app.go:598`).
  - **`Ctrl+O`** — toggle expand-all-tool-inputs.
  - **`Ctrl+L`** — manual viewport resync (QUM-669).
  - **`Ctrl+V`** — toggle the validate popup.
  - **`Ctrl+Q`** — reopen the question modal.
  - **`Ctrl+_`** / **`Ctrl+/`** — toggle mouse-capture selection mode.
  - **`Ctrl+C`** — clear input / open quit-confirm.
  - **`?`** / **`F1`** — open help (but `?` is gated off when on PanelInput so
    you can type literal `?`).
  - **`Tab`** / **`Shift+Tab`** — cycle `activePanel` itself.
  - **All async/modal messages** — palette, question, error, confirm, help
    modals all run via their own routes; `activePanel` is irrelevant once a
    modal is up.

### Selecting an agent — three available paths

This came up while auditing PanelTree's `Enter`. Selecting a specific agent
has three independent paths today:

  1. **Tree (`Enter`)** — visual; needs PanelTree focus + arrow-navigation.
  2. **`Ctrl+N` / `Ctrl+P`** — global; linear, no focus required.
  3. **Command palette `/agent <name>`** — global; arbitrary jump by name.

So PanelTree's `Enter` is *not* the unique path to agent selection; it is
one of three. It is, however, the only way to pick an agent by visual
cursor without typing the name or stepping linearly through every agent.

## Empirical evidence

Driven against `./sprawl enter` in a 200×50 detached tmux pane (sandbox
harness via `scripts/sprawl-test-env.sh`). Single agent (weave) only —
attempts to spawn a child were skipped because the in-tool persistent shell
makes a clean child-spawn flow expensive to drive; the gaps left by this
are noted below.

| Step | Key sent | Observed result | Inferred panel |
|---|---|---|---|
| Initial | — | Input bar shows placeholder `/: commands • ?: help • tab: cycle panel • ctrl+c: clear/quit`. | PanelInput (startup default when a bridge is present) |
| A | `hello` | Input bar now reads `▌ hello`. | PanelInput (chars inserted) |
| B | `Tab` | Input bar still `▌ hello`. **No visible focus change anywhere on screen.** | PanelTree (per code) |
| C | `XYZ` | Input bar unchanged. **Chars swallowed silently.** | PanelTree confirmed (textarea inert) |
| D | `Tab` | Still no visible change. | PanelViewport (per code) |
| E | `QQQ` | Input bar still `▌ hello`. Chars again swallowed. | PanelViewport confirmed |
| F | `PgUp` | No visible change (empty viewport — no content to scroll). | Can't disprove; consistent with PanelViewport |
| G | `Tab` then `RESTORED` | Input bar now `▌ helloRESTORED`. | PanelInput restored |

**Critical finding:** there is **no visible indicator** of which panel is
active. No border-color change, no caret, no status-bar segment. A user
that hits Tab gets exactly zero feedback that anything happened until they
try a key that behaves differently on the new panel. This matches the
"No color rendering" caveat in the `tui-testing` skill, but it has real UX
consequences for the `tab: cycle panel` advertisement.

### Gaps in empirical coverage (would not change the recommendation)

  - Did not exercise the tree's arrow-nav + Enter agent-select path with
    children present. Code reading is unambiguous: `tree.Update` only fires
    via `delegateKey` when `activePanel == PanelTree`, and `Ctrl+N/P` +
    palette `/agent` cover the same outcome via global paths.
  - Did not exercise viewport select-mode (`v` → `j`/`k`/`y`). Requires
    visible content. Code reading is again unambiguous: `handleViewportSelectKey`
    is hard-gated on `activePanel == PanelViewport` (`app.go:618`).
  - Did not test mouse wheel directly — the wheel handler is in the
    `MouseMsg` branch which runs *before* any `activePanel` check, so it
    cannot depend on focus by construction.

## What Tab actually unlocks today

Distilled from the table above, the *unique* user-facing behaviors that
Tab gates are:

  1. **PanelTree arrow-nav + Enter** to pick an agent by visual cursor —
     redundant with `Ctrl+N/P` (linear) and the command palette
     (`/agent <name>`).
  2. **PanelViewport `v` → select-mode → `y`-yank** for copying viewport
     content into the clipboard. There is no other in-TUI path to this
     workflow today. (`Ctrl+_` selection mode is a different feature — it
     drops mouse capture so the terminal does native click-drag selection,
     not the in-TUI ChatList selection.)
  3. **PanelViewport `PgUp` / `PgDn` / arrow scrolling.** Redundant with
     mouse wheel, which works from any panel.

Items 1 and 3 are fully covered by other always-on paths. Item 2 is the
only behavior with no global equivalent.

## Recommendation: **(b) — remove the hint, keep cycling**

**Rationale.** Tab cycling has exactly one behavior with no alternative:
entering viewport select-mode (`v` → `y` yank), which is hard-gated on
`activePanel == PanelViewport`. Pure removal (option c) would either
require relocating that gate (e.g. making `v` a global key, which collides
with the input panel typing it as a literal) or deleting the workflow,
which discards a real feature. Keeping cycling as-is (option a) is
defensible *only* if users actually discover and use that workflow — and
the hint text "tab: cycle panel" gives them no hint that the payoff is
copy-to-clipboard. The hint also advertises a no-op (PanelTree and
PanelViewport are visually indistinguishable, see Empirical step B), which
is a poor user-experience contract. Recommend **dropping the hint and
keeping cycling**, with an optional follow-up to expose select-mode
through a more discoverable surface (Ctrl-shortcut, command palette
entry, status-bar affordance) so cycling can be removed entirely in a
future cleanup.

### Suggested follow-up (not in scope here)

  - File a follow-up issue: surface viewport select-mode via a global
    Ctrl-shortcut (e.g. `Ctrl+Y` to enter yank-mode, scoped to the
    observed viewport). Once that lands, `activePanel` can be deleted
    outright — option (c) becomes viable.
  - The updated placeholder hint should fit in the same width budget;
    something like `/: commands • ?: help • ctrl+c: clear/quit` works.

## Files to touch if option (b) is accepted

  - `internal/tui/input.go:18` — drop ` • tab: cycle panel` from
    `inputPlaceholderHelp`. (Cycling code stays; the
    `tab: cycle panel` advertisement does not.)
  - Search for tests that assert on the placeholder text and adjust.
    (`grep -r "tab: cycle panel"` from repo root — at audit time the only
    matches were the constant itself and the linked Linear issue.)

No `activePanel` removal required for option (b).
