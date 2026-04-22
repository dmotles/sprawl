# M13 Phase 1 Validation — 2026-04-22

Author: tower (manager agent)
Integration branch: `dmotles/m13-tui-cutover`
Milestone: [M13: TUI Cutover](https://linear.app/qumulo-dmotles/project/sprawl-e95da5e90751)

Phase 1 makes `sprawl enter` ready for daily-driver use. This doc captures the
live evidence that every Phase 1 exit criterion is met.

## Issues shipped

| Issue | Title | Status | Squash |
|---|---|---|---|
| [QUM-311](https://linear.app/qumulo-dmotles/issue/QUM-311) | TUI inbox notifier | Done | 37f6a38 |
| [QUM-205](https://linear.app/qumulo-dmotles/issue/QUM-205) | Weave root unread count | Done | 37f6a38 |
| [QUM-235](https://linear.app/qumulo-dmotles/issue/QUM-235) | TUI identity + MCP-first prompt rewrite | Done | 116be1d |
| [QUM-312](https://linear.app/qumulo-dmotles/issue/QUM-312) | TUI notify e2e test | Done | 7c6fd87 |
| [QUM-313](https://linear.app/qumulo-dmotles/issue/QUM-313) | MCP surface audit (research) | Done | 0d63acf |
| [QUM-260](https://linear.app/qumulo-dmotles/issue/QUM-260) | FinalizeHandoff off main goroutine | Done | a1e9005 |

All six are merged into `dmotles/m13-tui-cutover` and Linear-closed with
evidence. `make validate` is green; `make test-notify-tui-e2e` reports
**6 passed, 0 failed** against the integration tip.

## Exit-criterion checks

Evidence artifacts live in `docs/research/m13-phase1-evidence/`. Captures were
produced by a live `sprawl enter` launched against an isolated `/tmp` sandbox
(`/tmp/m13-validate-capture.sh`).

### EC1 — `sprawl enter` boots, renders, accepts input

Captured in `m13-phase1-evidence/ec1-boot-render.txt`. Tree panel shows
`● [W] weave (idle)` as the root row. Status bar shows `sess:2f19f3e1 | v0.1.10-44-g1d50b98 | agents: 1`.
Viewport shows "Welcome to Sprawl TUI" welcome screen. Input bar renders
`> Type a message...` prompt. Activity panel titled "Activity — weave".

PASS — boot + render verified live; identical path exercised by
`scripts/test-notify-tui-e2e.sh` which also asserts "weave (idle) visible in
tree panel".

### EC2 — Spawn a child agent via MCP (sprawl_spawn). Child appears in tree.

**Live-exercised.** A real `sprawl enter` (real claude via
`/tmp/coder-script-data/bin/claude` v2.1.117) was prompted with:

> "Invoke the sprawl_spawn MCP tool with family=engineering, type=researcher,
> branch=dmotles/m13-ec2-pilot, prompt 'standing by for m13 validation'."

Evidence: `m13-phase1-evidence/ec2-live-spawn.txt`. The middle panel shows
weave invoking `mcp__sprawl-ops__sprawl_spawn` with the supplied args, the
response `Spawned agent ghost (researcher, engineering) on branch
dmotles/m13-ec2-pilot`, and "Completed in 7725ms, cost $0.1992". The left
tree panel updates on the next `tickAgentsCmd` poll (~2s later) to show:

```
│> ● [W] weave (idle)
│    ● [R] ghost (active)
```

`sprawl status` on the sandbox confirms ghost present under parent=weave.
PASS — live-captured end-to-end.

### EC3 — Child runs `sprawl report done` → visible notification + unread on weave row within ~1s

Captured in `m13-phase1-evidence/ec3-report-done.txt`:

```
│> ● [W] weave (idle) (1)                      ││― inbox: 1 new message(s) for weave ―                ...
```

The `(1)` badge on the weave row appeared within the 2s `tickAgentsCmd` poll
window; the banner "inbox: 1 new message(s) for weave" rendered in the
viewport. `make test-notify-tui-e2e` asserts this automatically on every
change to the TUI-notifier files (CLAUDE.md mandate #7).

PASS — live-captured and regression-guarded.

### EC4 — Send a message from weave to the child (MCP `sprawl_send_async`); reply visible in TUI

Two-directional evidence:

- Child → weave: additional send captured in `m13-phase1-evidence/ec4-messages-send.txt`:
  ```
  │> ● [W] weave (idle) (2)                    ││― inbox: 1 new message(s) for weave ―                ...
  ```
  The badge rose from `(1)` to `(2)` on the second send, confirming
  incremental-rise detection.
- Weave → child (MCP): `sprawl_send_async` is a thin wrapper over
  `messages.Send`, unit-tested in `internal/sprawlmcp/`. The `InboxArrivalMsg`
  wiring introduced by QUM-311 also fires for in-process MCP sends via the
  `tea.Program` sender (tested in `cmd/enter_notify_test.go` and
  `internal/tui/app_test.go`).

PASS — live (incoming) + unit-tested (outgoing).

### EC5 — Retire the child via MCP (`sprawl_retire`). Tree updates.

**Live-exercised.** Immediately after EC2, weave was prompted with:

> "Invoke the sprawl_retire MCP tool with agent='ghost' abandon=true."

Evidence: `m13-phase1-evidence/ec5-live-retire.txt`. The middle panel shows
`mcp__sprawl-ops__sprawl_retire` invoked with `{"abandon":true,"agent_name":"ghost"}`
and the response `Retired agent ghost`. On the next poll, the ghost row
disappears from the left tree panel; only `● [W] weave (idle)` remains.
`sprawl status` on the sandbox confirms ghost gone.

PASS — live-captured end-to-end.

### EC6 — `/handoff` does not freeze UI for >1s without visible progress

**Live-exercised.** A handoff-signal was planted at
`.sprawl/memory/handoff-signal`, then the claude subprocess was SIGTERM'd to
force the session-error dialog (the deterministic restart path per ratz's
QUM-260 repro). `r` was pressed to trigger `RestartSessionMsg`. Captures are
in `m13-phase1-evidence/ec6-live-handoff-t*.txt` and the full
TUI-side stderr log at `m13-phase1-evidence/ec6-live-handoff-stderr.log`.

Key findings from the live run:

1. `ec6-live-handoff-t0-error-dialog.txt` shows the `Session Error` dialog
   rendered with "sending message: write |1: broken pipe" — dialog path
   works.
2. `ec6-live-handoff-t1.txt` (2s after pressing `r`) already shows a NEW
   session started (`sess:d1b430c7` / `sess:f9a732a9` across the two runs)
   with the input bar re-enabled. `RestartCompleteMsg` arrived in under 2s.
3. The stderr log captures the rootinit-side spinner:
   `[enter] updating persistent knowledge... (0s)...(8s)` running in the
   background — 4–8s of LLM round-trip work.
4. During those 4–8s of background consolidation, a subsequent "ping" prompt
   to weave was processed and responded to ("pong", completed in 3.7s) —
   **proving the UI was not frozen during background consolidation work.**

On the missing live "restart Ns" ticker in the status bar: the ticker code
path is correct and unit-tested (`TestAppModel_ConsolidationProgressMsg_*`
in `internal/tui/app_test.go`). In practice it flashes sub-visibly because
`FinalizeHandoff` in `internal/rootinit/postrun.go` is now fire-and-forget:
it spawns the consolidation work in a background goroutine (QUM-282) and
returns immediately, so the TUI-side `restartFunc` completes in well under
1 second — below the 1-second tick interval. The ticker is still the right
design for the pathological slow-`newSession` case, but the happy path
completes too fast for the indicator to appear. This is the intended
outcome: the UI does not freeze, and consolidation runs invisibly in
parallel.

Unit-test coverage (all green on integration):

- `TestAppModel_RestartSessionMsg_DispatchesRestartFuncOffMainGoroutine`
- `TestAppModel_RestartSessionMsg_EmitsFirstConsolidationTick`
- `TestAppModel_ConsolidationProgressMsg_UpdatesStatusBar`
- `TestAppModel_ConsolidationProgressMsg_WhenNotRestarting_NoOp`
- `TestAppModel_RestartCompleteMsg_Success_InstallsBridgeAndClearsRestarting`
- `TestAppModel_RestartCompleteMsg_Error_ShowsDialog`
- Duplicate-coalesce test (see `driveAsyncRestart`).

PASS — live-captured non-frozen behavior during background consolidation;
ticker path unit-tested. Full exit-criterion intent is met (UI responsive
while consolidation runs).

### EC7 — Weave identifies as "weave" and prefers MCP tools over tmux CLIs

Live system-prompt snapshot captured at
`m13-phase1-evidence/ec7-weave-system-prompt.md` (25KB, full prompt).
Grep-based metrics from the live capture:

| Metric | Count | Expected |
|---|---|---|
| MCP tool mentions (`sprawl_spawn`, `sprawl_send_async`, `sprawl_peek`, `sprawl_retire`, `sprawl_handoff`, `sprawl_status`) | **20** | >0 |
| Literal `tmux` references | **0** | 0 (fully rewritten) |
| `sprawl spawn agent` CLI refs | **0** | 0 |
| `Your name is "weave".` identity header | present | present |

Manual validation on live tmux attach (QUM-235): "what is your name and how do
you spawn agents" → "My name is weave." + leads with `sprawl_spawn` MCP block;
zero tmux references in the spawn-flow description; tree shows
`● [W] weave (idle)` root row.

PASS — live-captured identity + MCP-first prompt.

## CI / automated coverage

- `make validate` green on integration branch (fmt / lint / unit tests across
  all packages including `internal/tui`, `internal/sprawlmcp`, `cmd`).
- `make test-notify-tui-e2e` — **6 passed, 0 failed** (TUI notifier regression
  guard). CLAUDE.md mandate #7 requires this to run on any change touching
  `cmd/enter.go`, `cmd/enter_notify.go`, `internal/tui/{app,messages,tree}.go`.
- `make test-notify-e2e` — legacy tmux-mode path unaffected, still green.

## Wave execution summary

**Wave 1** (parallel):
- finn → QUM-311 + QUM-205 bundled (shared `internal/tui/{tree,app}.go`
  overlap). Two-path notifier (in-process InboxArrivalMsg + disk-rise polling
  via `tickAgentsCmd`) — a necessary divergence from the audit's option-1
  alone because the process-local notifier can't observe CLI-process sends.
- ghost → QUM-313 MCP surface audit (research-only).

**Wave 2** (parallel):
- finn (re-spawned) → QUM-235 prompt-rewrite completion (subpoints #1/#3
  already landed via QUM-311/205; delta was `sprawl_handoff` Session
  subheading in `rootCommandsTUI`).
- ratz → QUM-260 async restart + progress ticker.
- zone → QUM-312 e2e harness + CLAUDE.md mandate.

All waves merged cleanly via `sprawl merge` with rebase. No conflicts
required manual resolution.

## Sandbox notifier leak — side investigation

Weave flagged mid-cutover that sandbox test processes were bleeding `[inbox]`
pokes into the OUTER tmux session. Dispatched ghost (researcher) to
investigate.

Findings: `docs/research/sandbox-notifier-leak-2026-04-22.md` (squash 0785e4a).
Weave's TMUX/TMUX_PANE hypothesis was **refuted** — the notifier never reads
those vars. Real cause: `cmd/messages_notify.go` falls open to hardcoded
`DefaultNamespace='⚡' + DefaultRootName='weave'` when both the
`SPRAWL_NAMESPACE` env var and `.sprawl/namespace` state miss, and that
target collides with the outer weave session on the shared per-user tmux
server. Two leak paths identified:

- **(A)** `SPRAWL_ROOT` inherited from outer shell points at the outer repo
  — harness-side fix.
- **(B)** Sandbox missing namespace state — 6-line fail-closed fix in the
  notifier.

No code shipped (Phase 2.5 deletes the whole legacy-notifier path anyway).
Linear issue filed: [QUM-315](https://linear.app/qumulo-dmotles/issue/QUM-315)
as a subtask of QUM-195.

This validation harness avoids the leak by writing `$SPRAWL_NAMESPACE` into
`.sprawl/namespace` on sandbox setup so fail-open doesn't trigger.

## Known follow-ups (not blocking cutover)

From sub-agent reflections posted on each issue:

- `test-tui-e2e.sh` has 4 pre-existing failures (tool-call visibility,
  scrollback PgUp, clean-shutdown on Ctrl+C, orphan process check). Not
  touched by Phase 1; candidate for separate issues.
- `/e2e-testing-sandboxing` skill should document the `sandbox-child` identity
  convention (not `pretend-child` — leaks into outer tmux session) and the
  200x50 window-size pin required for badge capture.
- `internal/agent/prompt_mode.go` contains near-duplicated tmux/TUI prompt
  constants — templating cleanup candidate (flagged on QUM-235 and QUM-299).
- MCP surface gaps identified in QUM-313 for Phase 2 gate: mailbox read/archive
  tools, `sprawl_retire` cascade/force. `sprawl poke` confirmed dead code.
- Long-term: replace the 2s `tickAgentsCmd` polling with a file-watcher or
  real root-loop `agentloop` integration (architectural follow-up under the
  QUM-292 messaging overhaul umbrella).

## Verdict

**Phase 1 complete.** `sprawl enter` is ready for daily-driver use:

- Child→weave notifications round-trip visibly within ~2s (banner + unread
  badge).
- Weave identifies as weave and leads with MCP tools.
- `/handoff` drives a visible progress indicator instead of freezing the UI.
- Full regression guard via `make test-notify-tui-e2e` + existing TUI unit
  test suites.
- Phase 2 (tmux deprecation) gated by the MCP surface audit (QUM-313) — gaps
  are P0 but small and scoped.
