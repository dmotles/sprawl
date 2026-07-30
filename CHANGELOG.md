# Changelog

All notable changes to Sprawl are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) loosely; releases are
not strictly semver while we are pre-1.0.

## [Unreleased]

## [v0.5.4] - 2026-07-30

### Added

- **Hub MVP** (QUM-870 … QUM-911) — a new remote hub for observing sprawl sessions from a browser. Lands as one arc: a Connect/protobuf wire + `hubd` process spine, a storage seam (Postgres + object store, goose migrations), host registration and browser session auth, a read-only SPA with a wire-log live-tail and running/idle status pill, and Terraform IaC to deploy it on Azure (private database, remote state backend). Read-only in this release — no control surface from the browser.
- **Durable wire log** (QUM-902, QUM-903, QUM-904) — every session's frames are now written to a seq'd, frame-oriented wire log, which becomes the authoritative transcript source. The TUI rehydrates resume/replay from it instead of the Claude JSONL, so content a live render drop missed reappears on reload. In-turn state is now driven by the CLI's own `session_state_changed` signal, fixing spurious "thinking" indicators.
- **`toast` MCP tool** (QUM-898) — agents can raise a TUI toast directly. `Ctrl+T` now opens the agent tree and `Esc` clears toasts (QUM-895).
- **Agent blurbs in `status` / `peek`** (QUM-899) — each agent carries a short self-describing blurb, surfaced in the reshaped `status` and `peek` output so a parent can tell at a glance what a child is doing.
- **User-level config file** (QUM-886) — `sprawl` reads a `0600` user config for hub URL and token, with documented precedence over environment and project config.
- **Race detector enforced in `make validate`** (QUM-972) — `validate` now runs the whole unit suite under `-race` instead of an uninstrumented `go test`, plus a gate test that fails if the flag is ever dropped or rendered inert. Nine live data races (one of them a production defect) had been sitting behind a green `validate`; new ones are caught automatically going forward.
- **Richer incident bundles** (QUM-934) — `Ctrl+\` now captures CPU and heap pprof profiles and the binary identity alongside the existing dumps, and the pprof listener can be toggled on a running session with `SIGUSR2` (an unconfigured default relocates off a busy port and advertises its bound address).
- **Employer-name leak guard** (QUM-872, QUM-873) — a pre-commit guard plus a whole-tree scan to keep private context out of this public repo.

### Changed

- **Sidechain internals no longer clutter the chat stream** (QUM-928) — frames from in-process `Agent`-tool spawns (Explore, Plan, oracle, …) are suppressed live and on reload; an `Agent()` call now renders as a plain tool row that closes on its launch ack. Measured against 660 wire logs, this removed ~20.5k interleaved frames from the transcript view.
- **Interrupt handling is turn-scoped** (QUM-927, QUM-929, QUM-931) — the interrupt arm now tracks *which* turn it belongs to instead of a single boolean, collapsing a family of Esc-at-a-turn-boundary bugs into one fix. A genuine transport failure or subprocess death at a turn boundary now surfaces as a Session Error rather than being misreported as "Interrupted". The redundant `[auto-continue]` stdin injection was deleted — the CLI self-resumes on background-task completion on its own; old sessions still rehydrate the ↻ marker.
- **`git add -A` is banned repo-wide** (QUM-989) — agent worktrees sit on a shared filesystem, so staging must name explicit paths. Backed by additional gitignore rules for infrastructure artifact classes.

### Fixed

- **Slash-command popover clipped and overran on wide terminals** (QUM-930) — the popover had no width call at all, so it rendered as a fixed 52-column stub whose already-bordered output was then guillotined, eating the closing border glyphs and cutting descriptions mid-word. It is now width-adaptive (clamped to a readable 20–100 columns), elides with an explicit `…`, and measures display width correctly for wide runes.
- **TUI event pump could freeze permanently after a gap-detect** (QUM-978) — `EventDropDetectedMsg` consumed the one-shot armed command without re-arming on any exit path, so a detected event gap parked the pump and froze live render until something unrelated happened to re-arm it. It now re-arms on every leg.
- **Unbounded render-cost growth in long sessions** (QUM-933) — intermediate assistant text blocks were never finalized, so any text block followed by a tool call stayed permanently uncacheable and re-ran the full markdown pipeline every frame. Measured ~88× improvement (51 ms → 0.6 ms per frame) and stray streaming cursors dropped to zero.
- **Assistant output stranded on session restart** (QUM-986) — an in-flight assistant item is now finalized at the restart chokepoint.
- **Child processes could outlive their parent** (QUM-896) — `Pdeathsig`/`Setpgid` and process-group kill were restored in the Claude launcher after a regression.
- **`[auto-continue]` markers on reload** (QUM-924) — replayed transcripts render them as the ↻ marker again via a shared prefix constant.

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

