# Changelog

All notable changes to Sprawl are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) loosely; releases are
not strictly semver while we are pre-1.0.

## [Unreleased]

### Deprecated

- **Legacy CLI commands now emit deprecation warnings** ([QUM-337]). Phase 2.1
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

[QUM-337]: https://linear.app/qumulo-dmotles/issue/QUM-337
