---
name: tui-testing
description: Automated and manual TUI testing workflows for the sprawl enter command.
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

## Known Issues

- **No color rendering**: Panel borders may render without color distinction between active/inactive panels. Tab navigation test may not detect focus changes visually.
- **Assistant text not rendering**: The viewport may not display assistant response text. The harness tests for turn state changes as a proxy.
