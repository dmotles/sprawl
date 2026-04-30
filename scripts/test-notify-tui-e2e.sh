#!/usr/bin/env bash
# test-notify-tui-e2e.sh - End-to-end smoke test for the TUI-mode
# child→weave notification path (QUM-312).
#
# The tmux-mode analogue lives at scripts/test-notify-e2e.sh (QUM-310).
# This script is the TUI-mode counterpart: it exercises the
# sprawl-enter + buildTUIRootNotifier + AppModel + tickAgentsCmd stack
# that surfaces inbox arrivals as a viewport banner and as an unread
# badge on the synthesized weave row.
#
# What it does:
#   1. Spins up an isolated /tmp sandbox (plain git repo + .sprawl/
#      root-name), mirroring the safety guards in sprawl-test-env.sh.
#      No tmux-mode weave is launched — the TUI acquires its own flock.
#   2. Launches `sprawl enter` in a detached tmux session so the
#      bubbletea TUI has a pseudo-terminal to render into.
#   3. Waits for the TUI to render (tree panel shows 'weave (idle)').
#   4. Simulates an out-of-process child agent using
#      SPRAWL_AGENT_IDENTITY=sandbox-child (tower convention — the
#      pretend-child identity leaks to outer sessions, see QUM-311
#      reflection).
#   5. Runs `sprawl report done` as the child; asserts the TUI pane
#      picks up the maildir rise on its 2s tick and renders both
#      (a) the banner 'inbox: N new message(s) for weave', and
#      (b) the '(1)' unread badge on the weave row.
#   6. Runs `sprawl messages send weave` as the child; asserts the
#      unread badge rises to '(2)'.
#
# Requires a real `claude` binary on PATH (sprawl enter spawns claude
# with stream-json I/O; without it the TUI cannot initialize). If
# absent:
#   SPRAWL_E2E_SKIP_NO_CLAUDE=1 → skip (exit 0)
#   otherwise                   → exit 1
#
# Usage:
#   bash scripts/test-notify-tui-e2e.sh
#
# NOTE: This test creates a real tmux session and a claude subprocess.
# Do not run it in parallel with other TUI-mode e2e scripts.
set -euo pipefail

# QUM-337: this script drives `sprawl report done` and `sprawl messages
# send` on purpose to verify the QUM-312 TUI notifier path. Suppress the
# per-process deprecation warning so the captured tmux pane output stays
# clean during the M13 cutover soak. Slated for deletion in 2.5.
export SPRAWL_QUIET_DEPRECATIONS=1

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# --- Dedicated tmux socket for sandbox isolation (QUM-325) ---
SPRAWL_TMUX_SOCKET="${SPRAWL_TMUX_SOCKET:-sprawl-notify-e2e-$$}"
export SPRAWL_TMUX_SOCKET

# _stmux wraps tmux with the dedicated sandbox socket.
_stmux() { tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"; }

# --- Preflight: claude binary ---

if ! command -v claude >/dev/null 2>&1; then
    if [ "${SPRAWL_E2E_SKIP_NO_CLAUDE:-}" = "1" ]; then
        echo "SKIP: claude binary not found on PATH (SPRAWL_E2E_SKIP_NO_CLAUDE=1 set)"
        exit 0
    fi
    echo "FATAL: claude binary not found on PATH — TUI notify e2e requires a real claude" >&2
    echo "       Set SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip this test instead." >&2
    exit 1
fi

# --- Preflight: tmux binary ---

if ! command -v tmux >/dev/null 2>&1; then
    echo "FATAL: tmux binary not found on PATH" >&2
    exit 1
fi

# --- Build sprawl ---

echo "=== Building sprawl ==="
make -C "$REPO_ROOT" build >/dev/null
SPRAWL_BIN="$REPO_ROOT/sprawl"
if [ ! -x "$SPRAWL_BIN" ]; then
    echo "FATAL: sprawl binary not found at $SPRAWL_BIN" >&2
    exit 1
fi

# --- Create isolated /tmp sandbox ---

SPRAWL_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-tui-notify-e2e-XXXXXX")
# Hard guard: refuse to continue unless SPRAWL_ROOT is under /tmp/.
SPRAWL_ROOT_REAL="$(cd "$SPRAWL_ROOT" 2>/dev/null && pwd -P || echo "$SPRAWL_ROOT")"
case "$SPRAWL_ROOT_REAL" in
    /tmp/*) ;;
    *)
        echo "FATAL: sandbox SPRAWL_ROOT=$SPRAWL_ROOT_REAL not under /tmp/; aborting" >&2
        exit 1
        ;;
esac
SPRAWL_ROOT="$SPRAWL_ROOT_REAL"

git -C "$SPRAWL_ROOT" init -b main --quiet
git -C "$SPRAWL_ROOT" -c user.name="Test" -c user.email="test@test" \
    commit --allow-empty -m "init" --quiet
mkdir -p "$SPRAWL_ROOT/.sprawl"
echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"

SESSION="sprawl-notify-tui-$(head -c4 /dev/urandom | xxd -p)"
STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"

echo "  SPRAWL_BIN=$SPRAWL_BIN"
echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
echo "  SESSION=$SESSION"
echo ""

# --- Test infrastructure ---

PASS_COUNT=0
FAIL_COUNT=0
pass() { PASS_COUNT=$((PASS_COUNT + 1)); echo "  PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); echo "  FAIL: $1" >&2; }

capture_pane() { _stmux capture-pane -t "$1" -p 2>/dev/null || true; }

# wait_for_pattern <session> <pattern> <timeout_secs>
wait_for_pattern() {
    local session="$1" pattern="$2" timeout="$3"
    local elapsed=0
    while [ "$elapsed" -lt "$timeout" ]; do
        if capture_pane "$session" | grep -qE "$pattern"; then
            return 0
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    return 1
}

cleanup() {
    local rc=$?
    if _stmux has-session -t "$SESSION" 2>/dev/null; then
        _stmux kill-session -t "$SESSION" 2>/dev/null || true
    fi
    case "$SPRAWL_ROOT" in
        /tmp/*) rm -rf -- "$SPRAWL_ROOT" ;;
    esac
    exit "$rc"
}
trap cleanup EXIT

# --- Launch the TUI in a detached tmux session ---

echo "=== Launching sprawl enter in tmux ==="
_stmux new-session -d -s "$SESSION" -x 200 -y 50 \
    "SPRAWL_ROOT='$SPRAWL_ROOT' '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
# Pin the tmux session to 200x50 regardless of attached clients so the
# TUI's tree panel renders wide enough (TreeWidth = termWidth/4, capped
# at 50) to fit the 'weave (idle) (N)' unread badge. Without this the
# detached session shrinks to the default ~80-col width and the badge
# gets truncated.
_stmux set-option -t "$SESSION" window-size manual >/dev/null
_stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

if wait_for_pattern "$SESSION" "weave \\(idle\\)" 30; then
    pass "TUI rendered ('weave (idle)' visible in tree panel)"
else
    fail "TUI did not render 'weave (idle)' within 30s"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -30 >&2
    echo "  stderr log tail:" >&2
    [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    echo "==============================="
    exit 1
fi

# --- Advance claude past any first-run trust prompt (QUM-310 gotcha).
#     In TUI mode claude runs under stream-json, so a TTY-style trust
#     prompt usually doesn't render in the pane — but the prompt may
#     still block the stream-json handshake on a fresh /tmp folder.
#     Best-effort: if 'trust this folder' ever shows up (e.g. if claude
#     escalates to a TTY prompt in a future release) send '1<enter>'
#     and continue. No assertion — the main checks below catch any
#     downstream failure. ---

if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
    _stmux send-keys -t "$SESSION" "1" Enter
    sleep 1
fi

# Give the first AgentTreeMsg tick a moment to land so rootUnread starts
# at 0 before we trigger the first message.
sleep 3

# --- Register a fake child agent in state, with weave as parent ---
#     SPRAWL_AGENT_IDENTITY=sandbox-child (tower convention — avoids
#     pretend-child-identity leaks into outer sessions; see QUM-311 /
#     /e2e-testing-sandboxing).

CHILD_NAME="sandbox-child"
CHILD_STATE_DIR="$SPRAWL_ROOT/.sprawl/agents"
mkdir -p "$CHILD_STATE_DIR"
cat > "$CHILD_STATE_DIR/${CHILD_NAME}.json" <<JSON
{
  "name": "${CHILD_NAME}",
  "type": "engineer",
  "family": "engineering",
  "parent": "weave",
  "prompt": "tui notify e2e test",
  "branch": "tui-notify-e2e",
  "worktree": "${SPRAWL_ROOT}",
  "status": "active",
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "tree_path": "weave├${CHILD_NAME}"
}
JSON

# --- Test A: `sprawl report done` from a simulated child ---

echo ""
# QUM-323: distinctive body token that must appear in weave's prompt after
# the 2s drain tick runs peekAndDrainCmd and the bridge forwards to claude.
DRAIN_TOKEN_A="DRAIN-BODY-TOKEN-A-$$-$(date +%s)"
echo "=== Test A: child 'sprawl report done' → TUI banner + (1) badge ==="
REPORT_LOG="$(mktemp /tmp/notify-tui-e2e-report.XXXXXX)"
set +e
env \
    SPRAWL_AGENT_IDENTITY="$CHILD_NAME" \
    SPRAWL_ROOT="$SPRAWL_ROOT" \
    SPRAWL_TEST_MODE=1 \
    "$SPRAWL_BIN" report done "e2e tui notify test A $DRAIN_TOKEN_A" \
    > "$REPORT_LOG" 2>&1
REPORT_RC=$?
set -e
if [ "$REPORT_RC" -eq 0 ]; then
    pass "sprawl report done exited 0"
else
    fail "sprawl report done exited non-zero ($REPORT_RC)"
    echo "  report stdout/stderr:" >&2
    sed 's/^/    /' "$REPORT_LOG" >&2
fi

# Banner and badge should appear within 1-2 AgentTree ticks (~2-4s).
# Allow generous headroom for slow sandbox boxes.
if wait_for_pattern "$SESSION" "inbox: [0-9]+ new message\\(s\\) for weave" 10; then
    pass "banner 'inbox: N new message(s) for weave' appeared in viewport"
else
    fail "banner never appeared in TUI viewport after sprawl report done"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -30 >&2
fi

if wait_for_pattern "$SESSION" "weave.*\\(idle\\).*\\(1\\)" 10; then
    pass "weave row shows '(1)' unread badge"
else
    fail "weave row does NOT show '(1)' unread badge"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -30 >&2
fi

# QUM-323: assert drained body reaches weave's prompt, not just the banner.
# The 2s AgentTreeMsg tick fires peekAndDrainCmd which renders the flush
# prompt (containing DRAIN_TOKEN_A) and sends it via the bridge into claude.
# Claude echoes the user-turn in the viewport, so the token appears in the
# pane capture. Generous timeout for slow sandbox + claude stream startup.
if wait_for_pattern "$SESSION" "$DRAIN_TOKEN_A" 20; then
    pass "DRAIN_TOKEN_A '$DRAIN_TOKEN_A' reached weave's prompt (QUM-323 drain fired in TUI)"
else
    fail "DRAIN_TOKEN_A '$DRAIN_TOKEN_A' did NOT reach weave's prompt within 20s — QUM-323 TUI regression"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
fi

rm -f "$REPORT_LOG"

# --- Test B: `sprawl messages send weave` from the same child ---

echo ""
# QUM-323: second body token for the messages.Send path.
DRAIN_TOKEN_B="DRAIN-BODY-TOKEN-B-$$-$(date +%s)"
echo "=== Test B: child 'sprawl messages send weave' → badge rises to (2) ==="
SEND_LOG="$(mktemp /tmp/notify-tui-e2e-send.XXXXXX)"
set +e
env \
    SPRAWL_AGENT_IDENTITY="$CHILD_NAME" \
    SPRAWL_ROOT="$SPRAWL_ROOT" \
    SPRAWL_TEST_MODE=1 \
    "$SPRAWL_BIN" messages send weave "tui e2e subject" "tui e2e body $DRAIN_TOKEN_B" \
    > "$SEND_LOG" 2>&1
SEND_RC=$?
set -e
if [ "$SEND_RC" -eq 0 ]; then
    pass "sprawl messages send weave exited 0"
else
    fail "sprawl messages send weave exited non-zero ($SEND_RC)"
    echo "  send stdout/stderr:" >&2
    sed 's/^/    /' "$SEND_LOG" >&2
fi

if wait_for_pattern "$SESSION" "weave.*\\(idle\\).*\\(2\\)" 10; then
    pass "weave row shows '(2)' unread badge after second message"
else
    fail "weave row did NOT rise to '(2)' after sprawl messages send"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -30 >&2
fi

# QUM-323: assert drained body B reaches weave's prompt.
# Note: if Test A's drain is still mid-turn when B arrives, B stays pending
# until claude finishes A; the tick backstop then drains it. Timeout tuned
# accordingly (claude turn latency + 2s tick + send).
if wait_for_pattern "$SESSION" "$DRAIN_TOKEN_B" 45; then
    pass "DRAIN_TOKEN_B '$DRAIN_TOKEN_B' reached weave's prompt (QUM-323 drain fired in TUI)"
else
    fail "DRAIN_TOKEN_B '$DRAIN_TOKEN_B' did NOT reach weave's prompt within 45s — QUM-323 TUI regression"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
fi

rm -f "$SEND_LOG"

# --- Summary ---

echo ""
echo "==============================="
echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "==============================="

if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
fi
exit 0
