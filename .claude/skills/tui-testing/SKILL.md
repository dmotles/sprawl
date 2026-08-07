---
name: tui-testing
description: Automated and manual TUI testing workflows for the sprawl enter command, plus the TUI's operator surfaces — selecting and copying text out of a mouse-capturing TUI, the scroll and prompt-history key map, the Ctrl+\ incident-snapshot bundle, and the SIGUSR2 / --pprof runtime profiling toggle.
user-invocable: true
argument-hint: "[automated|manual|checklist] or omit for full guide"
---

# TUI Testing Workflow

Use this workflow to validate changes to `sprawl enter` and the TUI dashboard. The TUI uses Bubble Tea with 4 panels: agent tree, viewport, text input, and status bar.

## Part A: Automated Harness

Run the E2E test harness script:

```bash
# Full suite — all 8 test scenarios
bash scripts/test-tui-e2e.sh

# Quick mode — launch + render check only
bash scripts/test-tui-e2e.sh --quick
```

### What It Tests

| Test | What it checks |
|------|---------------|
| 1. Launch & render | TUI starts, panels visible, status bar renders, borders present |
| 2. Session init | "Session ready" appears (Claude subprocess connects) |
| 3. User input | Keystrokes render as "You: <message>" in viewport |
| 4. Assistant response | Turn state changes (Thinking/Streaming/Complete) |
| 5. Tool call visibility | "Tool:" appears after triggering tool use |
| 6. Scrollback | PgUp changes viewport content |
| 7. Tab navigation | Tab key switches panel focus without crashing |
| 8. Clean shutdown | Ctrl+C terminates cleanly, no orphaned processes |

### Interpreting Failures

- Tests 2-7 require a running Claude session. If Claude fails to start, these are SKIPped.
- Tests 4-5 make real API calls and have 60-second timeouts. Failures may indicate API issues, not TUI bugs.
- Some tests may fail due to known TUI bugs (no color, assistant text not rendering). The harness detecting these failures is itself validation that the harness works correctly.

## Part B: Manual Validation via tmux

For ad-hoc testing when the harness doesn't cover your change:

### 1. Set up

```bash
make build
TEST_ROOT=\$(mktemp -d /tmp/sprawl-tui-manual-XXXXXX)
git -C "\$TEST_ROOT" init -b main --quiet
git -C "\$TEST_ROOT" -c user.name="Test" -c user.email="test@test" commit --allow-empty -m "init" --quiet
mkdir -p "\$TEST_ROOT/.sprawl"
echo "manual-test" > "\$TEST_ROOT/.sprawl/root-name"
```

### 2. Launch in detached tmux

```bash
SESSION="tui-manual-\$\$"
tmux new-session -d -s "\$SESSION" -x 120 -y 40 \
    "SPRAWL_ROOT='\$TEST_ROOT' ./sprawl enter"
```

### 3. Interact

```bash
# Capture the screen as text
tmux capture-pane -t "\$SESSION" -p

# Send keystrokes
tmux send-keys -t "\$SESSION" "your message here" Enter

# Send special keys
tmux send-keys -t "\$SESSION" Tab        # switch panel
tmux send-keys -t "\$SESSION" PgUp       # scroll up
tmux send-keys -t "\$SESSION" C-c        # quit
```

### 4. Assert on content

```bash
# Check for specific text
tmux capture-pane -t "\$SESSION" -p | grep -q "expected text"

# Save full capture for review
tmux capture-pane -t "\$SESSION" -p > /tmp/tui-capture.txt
```

### 5. Tear down

```bash
tmux kill-session -t "\$SESSION" 2>/dev/null
rm -rf "\$TEST_ROOT"
```

## Part C: Mandatory Validation Checklist

Every agent touching TUI code MUST complete these steps before reporting done:

- [ ] `make validate` passes
- [ ] `bash scripts/test-tui-e2e.sh --quick` passes (launch and render)
- [ ] Full `bash scripts/test-tui-e2e.sh` run completed (document any known failures)
- [ ] If the harness doesn't cover your change, perform manual tmux validation and document what you checked (include `tmux capture-pane` output in your done report)
- [ ] Ctrl+C cleanly shuts down with no orphaned processes

## Text selection in `sprawl enter` (QUM-653 / QUM-731)

The TUI captures the mouse so the scroll wheel scrolls the chat viewport
(QUM-731). Mouse capture intercepts plain click-drag, so use one of the
terminal- or tmux-native paths below to select and copy — none require a
modal toggle (the QUM-617 selection-mode toggle stays retired):

* **Shift+drag** — most terminals (xterm.js / coder web terminal, gnome-
  terminal, kitty, wezterm, Alacritty, iTerm2) bypass mouse capture while
  Shift is held; copy with your usual keystroke (Cmd+C / Ctrl+Shift+C).
* **tmux copy-mode** (`prefix` + `[`) — scroll, search, and yank tmux-style.
  Works regardless of terminal.
* **Right-click → Copy** — in most terminals the right-click context menu
  copies the OS-level selection even with mouse capture on.

Scroll inside the TUI:

* **Mouse wheel** — scrolls the observed chat viewport up/down (suppressed
  while a modal — `/help`, palette, confirm, question, validate-popup — is
  open).
* `PgUp` / `PgDn` — page up/down
* `Home` / `End` — jump to top/bottom
* `Up` / `Down` — navigate prompt input history **when the input is empty**
  (or while a history walk is already in progress); no-op when freshly
  typing. `PgUp` / `PgDn` / mouse wheel scroll the chat viewport regardless
  of input state.

### Incident snapshot hotkey (QUM-728)

Press `Ctrl+\` to write a forensic bundle to
`<repoRoot>/.sprawl/incidents/<ISO8601>-tui-snapshot/`. Includes:
goroutine dump, fd list, sprawl status, `ps auxf`, `/proc/<pid>/status`
for weave, last 10k mcp-calls.jsonl lines, per-agent activity rates,
memory + loadavg. Non-blocking — TUI stays interactive. Status bar shows
`snapshot saved → <path>` on completion (or `snapshot failed` + an error
toast on failure).

### Runtime pprof toggle (QUM-678 / QUM-934)

`--pprof <addr>` (or `SPRAWL_PPROF_ADDR`) exposes `net/http/pprof` at launch.
**`SIGUSR2` toggles the listener on a running session** — no relaunch, which is
the point: restarting resolves some session-scoped perf bugs and so destroys
the evidence. (`SIGUSR1` is the separate sigdump goroutine/fd dump.)

Bind-failure policy differs by **provenance**, deliberately — don't merge the
two branches:

* **Explicitly configured** (`--pprof` / `SPRAWL_PPROF_ADDR` / an explicit arg):
  bound as-is or fails loudly. Never silently relocated — an operator who named
  a port will curl that port.
* **Unconfigured** (our own `127.0.0.1:6060` default, which nobody asked for):
  tries the default, then falls back to an ephemeral `127.0.0.1:0` on
  `EADDRINUSE`. Loopback only, and only `EADDRINUSE` relocates.

While the listener is up, its **bound address is written to
`<SPRAWL_ROOT>/.sprawl/runtime/pprof-addr`** and removed on stop, so
`curl http://$(cat .sprawl/runtime/pprof-addr)/debug/pprof/` works even when the
fallback picked an ephemeral port. The toggle's log line only reaches
`.sprawl/logs/tui-stderr-*.log` (the TUI redirects stderr), so this file is the
discoverable surface; an in-TUI surface is still deferred. The file is advisory
— written only after the weave flock is held, and cleared at launch, so a
SIGKILLed session's stale entry cannot mislead the next one.

## Known Issues

- **No color rendering**: Panel borders may render without color distinction between active/inactive panels. Tab navigation test may not detect focus changes visually.
- **Assistant text not rendering**: The viewport may not display assistant response text. The harness tests for turn state changes as a proxy.
