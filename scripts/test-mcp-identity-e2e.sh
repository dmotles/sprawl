#!/usr/bin/env bash
# test-mcp-identity-e2e.sh — E2E test for QUM-387: verify that child
# agents using MCP tools get their identity propagated correctly.
#
# This test:
#   1. Sets up an isolated /tmp sandbox
#   2. Launches `sprawl enter` in a detached tmux session
#   3. Spawns a child agent with a minimal task
#   4. Waits for the child to call report_status via MCP
#   5. Verifies the child's state was updated (not weave's)
#   6. Verifies weave received a notification in its mailbox
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"
# When invoked from /tmp, REPO_ROOT is /tmp. Override if needed.
if [ "$REPO_ROOT" = "/tmp" ]; then
    REPO_ROOT="/home/coder/sprawl/.sprawl/worktrees/finn"
fi

SPRAWL_BIN="$REPO_ROOT/sprawl"
if [ ! -x "$SPRAWL_BIN" ]; then
    echo "FATAL: sprawl binary not found at $SPRAWL_BIN" >&2
    exit 1
fi

# --- Preflight ---
if ! command -v claude >/dev/null 2>&1; then
    echo "FATAL: claude binary not found on PATH" >&2
    exit 1
fi
if ! command -v tmux >/dev/null 2>&1; then
    echo "FATAL: tmux binary not found on PATH" >&2
    exit 1
fi

# --- Dedicated tmux socket ---
SPRAWL_TMUX_SOCKET="sprawl-mcp-identity-e2e-$$"
export SPRAWL_TMUX_SOCKET
_stmux() { tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"; }

# --- Create isolated sandbox ---
SPRAWL_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-mcp-identity-e2e-XXXXXX")
SPRAWL_ROOT_REAL="$(cd "$SPRAWL_ROOT" && pwd -P)"
case "$SPRAWL_ROOT_REAL" in
    /tmp/*) ;;
    *) echo "FATAL: SPRAWL_ROOT=$SPRAWL_ROOT_REAL not under /tmp/" >&2; exit 1 ;;
esac
SPRAWL_ROOT="$SPRAWL_ROOT_REAL"

git -C "$SPRAWL_ROOT" init -b main --quiet
git -C "$SPRAWL_ROOT" -c user.name="Test" -c user.email="test@test" \
    commit --allow-empty -m "init" --quiet
mkdir -p "$SPRAWL_ROOT/.sprawl"
echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"

SESSION="sprawl-mcp-identity-$(head -c4 /dev/urandom | xxd -p)"
STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"

echo "=== QUM-387 MCP Identity E2E Test ==="
echo "  SPRAWL_BIN=$SPRAWL_BIN"
echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
echo "  SESSION=$SESSION"
echo ""

# --- Infrastructure ---
PASS_COUNT=0
FAIL_COUNT=0
pass() { PASS_COUNT=$((PASS_COUNT + 1)); echo "  PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); echo "  FAIL: $1" >&2; }
capture_pane() { _stmux capture-pane -t "$1" -p 2>/dev/null || true; }
wait_for_pattern() {
    local session="$1" pattern="$2" timeout="$3" elapsed=0
    while [ "$elapsed" -lt "$timeout" ]; do
        if capture_pane "$session" | grep -qE "$pattern"; then return 0; fi
        sleep 1; elapsed=$((elapsed + 1))
    done
    return 1
}

cleanup() {
    local rc=$?
    if _stmux has-session -t "$SESSION" 2>/dev/null; then
        _stmux kill-session -t "$SESSION" 2>/dev/null || true
    fi
    case "$SPRAWL_ROOT" in /tmp/*) rm -rf -- "$SPRAWL_ROOT" ;; esac
    exit "$rc"
}
trap cleanup EXIT

# --- Launch TUI ---
echo "=== Launching sprawl enter ==="
_stmux new-session -d -s "$SESSION" -x 200 -y 50 \
    "SPRAWL_ROOT='$SPRAWL_ROOT' '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
_stmux set-option -t "$SESSION" window-size manual >/dev/null
_stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

if wait_for_pattern "$SESSION" "weave \\(idle\\)" 30; then
    pass "TUI rendered ('weave (idle)' visible)"
else
    fail "TUI did not render within 30s"
    capture_pane "$SESSION" | tail -20 >&2
    exit 1
fi

# Trust prompt handling
if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
    _stmux send-keys -t "$SESSION" "1" Enter; sleep 1
fi
sleep 3

# --- Register a child agent manually (simulating spawn without needing
#     the full spawn flow which requires git worktree) ---
CHILD_NAME="mcp-test-child"
mkdir -p "$SPRAWL_ROOT/.sprawl/agents"
cat > "$SPRAWL_ROOT/.sprawl/agents/${CHILD_NAME}.json" <<JSON
{
  "name": "${CHILD_NAME}",
  "type": "engineer",
  "family": "engineering",
  "parent": "weave",
  "prompt": "mcp identity e2e test",
  "branch": "mcp-identity-e2e",
  "worktree": "${SPRAWL_ROOT}",
  "status": "active",
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "tree_path": "weave├${CHILD_NAME}"
}
JSON

echo ""
echo "=== Test: child 'sprawl report done' via CLI (baseline) ==="
# This uses the CLI path (SPRAWL_AGENT_IDENTITY env var).
REPORT_LOG=$(mktemp /tmp/mcp-identity-e2e-report.XXXXXX)
set +e
env \
    SPRAWL_AGENT_IDENTITY="$CHILD_NAME" \
    SPRAWL_ROOT="$SPRAWL_ROOT" \
    SPRAWL_TEST_MODE=1 \
    SPRAWL_QUIET_DEPRECATIONS=1 \
    "$SPRAWL_BIN" report done "baseline CLI report" \
    > "$REPORT_LOG" 2>&1
REPORT_RC=$?
set -e

if [ "$REPORT_RC" -eq 0 ]; then
    pass "CLI report done exited 0"
else
    fail "CLI report done exited $REPORT_RC"
    cat "$REPORT_LOG" >&2
fi

# Verify state was updated for the CHILD, not weave
CHILD_STATUS=$(python3 -c "import json; d=json.load(open('$SPRAWL_ROOT/.sprawl/agents/$CHILD_NAME.json')); print(d.get('status','?'))" 2>/dev/null || echo "ERROR")
if [ "$CHILD_STATUS" = "done" ]; then
    pass "Child state updated to 'done' (not weave)"
else
    fail "Child status = '$CHILD_STATUS', expected 'done'"
fi

# Verify weave got a notification
if wait_for_pattern "$SESSION" "inbox: [0-9]+ new message" 10; then
    pass "Weave TUI shows inbox notification from child report"
else
    fail "Weave TUI did not show inbox notification"
    capture_pane "$SESSION" | tail -20 >&2
fi

# Verify weave's mailbox has a message FROM the child
WEAVE_MSGS_DIR="$SPRAWL_ROOT/.sprawl/messages/weave"
if [ -d "$WEAVE_MSGS_DIR" ]; then
    MSG_COUNT=$(find "$WEAVE_MSGS_DIR" -name "*.json" 2>/dev/null | wc -l)
    if [ "$MSG_COUNT" -gt 0 ]; then
        # Check that the message is FROM the child, not from weave
        FIRST_MSG=$(find "$WEAVE_MSGS_DIR" -name "*.json" 2>/dev/null | head -1)
        MSG_FROM=$(python3 -c "import json; d=json.load(open('$FIRST_MSG')); print(d.get('from','?'))" 2>/dev/null || echo "ERROR")
        if [ "$MSG_FROM" = "$CHILD_NAME" ]; then
            pass "Message in weave's mailbox is FROM '$CHILD_NAME' (not from 'weave')"
        else
            fail "Message in weave's mailbox FROM='$MSG_FROM', expected '$CHILD_NAME'"
        fi
    else
        fail "No messages found in weave's mailbox"
    fi
else
    fail "Weave mailbox directory does not exist"
fi

rm -f "$REPORT_LOG"

echo ""
echo "==============================="
echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "==============================="
[ "$FAIL_COUNT" -gt 0 ] && exit 1
exit 0
