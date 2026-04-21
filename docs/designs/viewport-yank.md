# Design: Viewport Selection & Yank to Clipboard

## Status: Draft (QUM-281)

## Motivation

QUM-280 enabled `tea.MouseModeCellMotion` so scroll wheel events reach the TUI
viewport. Consequence: the terminal no longer treats drags as native text
selection, so users lost the ability to highlight+Cmd/Ctrl+C text out of the
agent output. This hurts the most common "grab what the agent just said"
workflow.

The acceptance in QUM-281 requires:

- Selection → auto-copy to OS clipboard.
- Payload is **raw markdown**, not the styled/reflowed terminal rendering.
- Works over SSH.
- Works on macOS / Linux / Windows without shelling out to `pbcopy` /
  `xclip` / `wl-copy` / `clip.exe`.

## Non-Goals

- Mouse-drag selection inside the viewport. The raw-markdown requirement makes
  this hard (would need rendered-line → source-offset map because the
  `MarkdownRenderer` reflows). Keyboard-only for MVP.
- Line-level granularity. Same reason. Message-level is a clean unit of
  "something an agent said" and maps 1:1 to `ViewportModel.messages[]`.
- Multi-select with gaps. `v` → anchor, motion extends to cursor, contiguous
  range only.

## UX

Only active when the Viewport panel has focus.

| Key | Action |
|---|---|
| `v` | Enter select mode; anchor+cursor on the last message |
| `j` / `↓` | Move cursor down (extends selection) |
| `k` / `↑` | Move cursor up (extends selection) |
| `g` | Jump cursor to first message |
| `G` | Jump cursor to last message |
| `y` | Yank selected range as raw markdown → OSC 52; exit select mode |
| `Esc` / `v` | Exit select mode without yanking |

Visual cues: selected messages render with an accent-reversed gutter prefix
(`▌ `); the status bar shows `-- SELECT --` on the left while active. Mouse
wheel scrolling still works; `PgUp`/`PgDn` still scroll the viewport (they do
not move the selection cursor).

## Mechanism

- **Clipboard transport**: `tea.SetClipboard(s)` in bubbletea v2 emits an
  OSC 52 escape through the managed TTY. OSC 52 is:
  - Terminal-agnostic — no dependency on `pbcopy`/`xclip`/`wl-copy`.
  - SSH-transparent — the escape propagates through the session if the host
    terminal honors OSC 52 (modern iTerm2, kitty, wezterm, Alacritty with
    `enable_kitty_keyboard`, Windows Terminal, tmux with `set-clipboard on`).
  - Unbounded by local OS. Also avoids a race with the bubbletea renderer
    that direct `fmt.Print` would introduce.

- **Selection state**: kept in a small `SelectionState` value in
  `internal/tui/selection.go`. Pure; no bubbletea dependency; trivially
  testable. `ViewportModel` embeds it and exposes `EnterSelect`,
  `ExitSelect`, `MoveCursor`, `IsSelecting`, `SelectedRaw`.

- **Raw-markdown assembly**: `AssembleRawMarkdown(msgs, lo, hi)` produces a
  single string by concatenating `Content` for each message in the range,
  separated by blank lines. Rules:
  - `MessageAssistant` → verbatim `Content` (this is the raw markdown source
    the renderer consumed).
  - `MessageUser` → each line prefixed with `> ` (markdown blockquote).
  - `MessageToolCall` → `<!-- tool: <name> (<input>) -->`. Human-readable but
    clearly not agent prose. Skipped when unapproved? No — approval status is
    irrelevant to the text; always include.
  - `MessageStatus` / `MessageError` → skipped. These are TUI chrome, not
    conversation content.

- **Cross-panel safety**: all selection key handling is scoped to
  `PanelViewport`. When the Input panel is active, `v`/`j`/`k`/`y` type
  literal characters as before.

## Future Work

- `V` for "visual line" mode once a line/source map exists.
- Mouse drag selection once we have that map.
- `Y` for "yank entire session as markdown" — one-key export.
- Clipboard confirmation via `tea.ReadClipboard` round-trip in tests.

## Testing

Unit tests cover the pure selection functions and raw-markdown assembly.
Viewport tests assert highlight rendering on the cursor message. App-level
tests drive the full `v` / `j` / `y` flow and assert the returned `tea.Cmd`
emits bubbletea's `setClipboardMsg` with the expected payload.

Manual validation per `/tui-testing`: launch in tmux, generate assistant
output, enter select mode, yank, paste into an external buffer, and verify
the raw markdown matches.
