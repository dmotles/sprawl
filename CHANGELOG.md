# Changelog

All notable changes to Sprawl are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) loosely; releases are
not strictly semver while we are pre-1.0.

## [Unreleased]

## [v0.3.0] - 2026-06-08

### Added

- **Toast notification subsystem** (QUM-649, QUM-651, QUM-701) — bordered floating overlays for transient TUI events. Three wired consumers: recovery (post-resume "recovered N agents"), interrupt (Esc during streaming), and terminal-fault (agent-side error). Ctrl+T dismisses all toasts.
- **`sprawl debug colors`** (QUM-698) — palette × visual-treatment grid viewer. First child under a new `sprawl debug` parent command group reserved for future diagnostics.
- **Live pprof endpoint** (QUM-678) — `--pprof` flag and `SPRAWL_PPROF_ADDR` env on `sprawl enter` expose net/http/pprof at /debug/pprof for live perf inspection.
- **Keyboard scroll in chat** (QUM-653) — PgUp / PgDn / Home / End and Up / Down (when input is empty) scroll the chat region. Mouse capture removed so terminal-native text selection (Cmd+C, tmux copy-mode, Shift+drag) now works without a modal toggle.

### Changed

- **Toast positioning and styling** (QUM-701) — toasts now render as rounded-border boxes, horizontally centered below the SPRAWL header (previously top-right text strips). Stack vertically; remaining toasts shift up when one dismisses. Info toasts track the configured accent (`Palette.Primary`); warning/error toasts keep their respective palette colors.
- **Palette swap** (QUM-700) — `Palette.Accent` moved from ANSI 39 → 51 (cyan); `Palette.Info` moved 51 → 39 (cyan-blue). Keeps `Accent` visually distinct from a user-customized `Primary`.
- **Header strip** (QUM-656, QUM-657, QUM-689, QUM-694, QUM-695) — SPRAWL wordmark + orbital agent tree port. Activity pane removed; tree column gone from main row; `?`-as-help dropped (F1 canonical). Anchor `──●` hidden when the root has no children.
- **ChatList sole render path** (QUM-673, QUM-676, QUM-677, QUM-693) — `internal/tui/viewport.go` (3453 LOC) and the `ViewportModel` facade (340 LOC) deleted. Yank-mode, `activePanel`, and Tab cycling all removed. Single-responsibility chat rendering.
- **Error surfaces** (QUM-680) — `agentops.TerminalAgentError` produces clearer Peek / SendMessage / Retire error messages.
- **`Real.Status` disk fallback** (QUM-682) — uses streaming `ReadActivityTail` instead of slurping the whole activity log.

### Fixed

- **Interrupt toast race-with-self** (QUM-697) — `ConditionDismiss` cleared the interrupt toast in the same event-loop pass it was spawned, so it never rendered. Switched to `TimerDismiss(2s)`.
- **Tree pivot for empty roots** (QUM-686) — root with no children no longer renders a dangling anchor.
- **Transient-label clear rules documented** (QUM-690) — long-standing comment debt resolved.

### Removed

- **`sprawl input-debug`** (QUM-699) — QUM-608-era hidden paste-coalesce diagnostic deleted. `Coalescer.Done()` removed (no other consumers). Helper `isStdinTTY` inlined into `cmd/enter.go`.

### Deprecated

- **Legacy CLI commands now emit deprecation warnings** (QUM-337). Phase 2.1
  of the M13 TUI cutover begins steering humans and agents toward the
  `sprawl_*` MCP tool surface that has matured to cover the full
  agent-callable workflow. Every invocation of the following CLI forms now
  prints a one-line stderr warning naming the MCP replacement; the CLI
  continues to work otherwise (exit code, stdout, and behavior are
  unchanged):

  | CLI form | Replacement |
  | --- | --- |
  | `sprawl spawn` / `sprawl spawn agent` | `spawn` |
  | `sprawl retire` | `retire` |
  | `sprawl kill` | `kill` |
  | `sprawl delegate` | `delegate` |
  | `sprawl messages send` | `send_async` (or `send_interrupt` for the rare urgent case) |
  | `sprawl messages list` | `messages_list` |
  | `sprawl messages read` | `messages_read` |
  | `sprawl messages archive` | `messages_archive` |
  | `sprawl report …` | `report_status` |
  | `sprawl status` | `status` |
  | `sprawl tree` | `status` (or `peek` for one agent) |
  | `sprawl handoff` | `handoff` |
  | `sprawl init` | (no MCP equivalent; `sprawl enter` replaces tmux mode) |
  | `sprawl color` | (no MCP equivalent; slated for deletion) |

  Set `SPRAWL_QUIET_DEPRECATIONS=1` in any environment that intentionally
  exercises the legacy path (CI scripts, tests, etc.) to suppress the
  warning. The three remaining tmux-path e2e scripts
  (`scripts/test-init-e2e.sh`, `scripts/test-notify-e2e.sh`,
  `scripts/test-notify-tui-e2e.sh`) already set this and will be removed in
  Phase 2.5 alongside the tmux machinery.

  **Soak agreement (per QUM-314):** when zero agent runs hit a deprecation
  warning over a 7-day window in production use, the next phase (2.2) may
  begin removing the tmux-only machinery, followed by deletion of the CLI
  forms in 2.3.

  The `/handoff` skill has been migrated from the `sprawl handoff` CLI to
  the `handoff` MCP tool to stop self-inflicted warning noise from
  agent-prompted CLI calls.

