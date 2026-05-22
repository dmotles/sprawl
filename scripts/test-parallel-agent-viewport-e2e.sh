#!/usr/bin/env bash
# test-parallel-agent-viewport-e2e.sh - End-to-end test for parallel Agent
# tool call rendering in the TUI viewport (QUM-386).
#
# What it does:
#   1. Builds sprawl and creates a fake claude binary that speaks the
#      stream-json protocol and emits parallel Agent tool_use blocks.
#   2. Spins up an isolated /tmp sandbox with a git repo.
#   3. Launches `sprawl enter` in a detached tmux session using the
#      fake claude.
#   4. Waits for the TUI to render, then verifies via tmux capture-pane
#      that two independent Agent containers appear (two ┌ markers).
#   5. After the fake claude completes, verifies that both containers
#      collapse to show result text.
#
# Requires tmux on PATH. Does NOT require a real claude binary.
#
# Usage:
#   bash scripts/test-parallel-agent-viewport-e2e.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# --- Dedicated tmux socket for sandbox isolation (QUM-325) ---
SPRAWL_TMUX_SOCKET="${SPRAWL_TMUX_SOCKET:-sprawl-parallel-e2e-$$}"
export SPRAWL_TMUX_SOCKET

_stmux() { tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"; }

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
SPRAWL_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-parallel-e2e-XXXXXX")
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

SESSION="sprawl-parallel-e2e-$(head -c4 /dev/urandom | xxd -p)"
STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"

echo "  SPRAWL_BIN=$SPRAWL_BIN"
echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
echo "  SESSION=$SESSION"
echo ""

# --- Create fake claude binary ---
# This script speaks the stream-json protocol over stdin/stdout (NDJSON).
# sprawl enter launches claude with -p --input-format stream-json --output-format stream-json.
# Protocol:
#   1. Host sends control_request (initialize) on stdin
#   2. We respond with system init + control_response on stdout
#   3. Host sends user message on stdin (after user types in TUI)
#   4. We emit assistant message with 2 parallel Agent tool_use blocks
#   5. We emit tool_result user messages for each agent
#   6. We emit result message
FAKE_CLAUDE="$SPRAWL_ROOT/fake-claude"
cat > "$FAKE_CLAUDE" <<'FAKESCRIPT'
#!/usr/bin/env bash
# Fake claude binary for parallel agent e2e test.
# Speaks stream-json protocol (NDJSON on stdin/stdout).
# Ignores all CLI args (sprawl passes -p, --input-format, etc.)

# Step 1: Read the control_request (initialize) from host
read -r _init_req 2>/dev/null || true

# Step 2: Emit system init message
echo '{"type":"system","subtype":"init","session_id":"fake-session-001","cwd":"/tmp","tools":["Agent","Bash","Read"],"model":"claude-fake","permissionMode":"auto"}'

# Step 2b: Emit control_response to complete initialization
echo '{"type":"control_response","response":{"subtype":"success","request_id":"req-1"}}'

# Step 3: Read the user message (sent after user types in TUI)
read -r _user_msg 2>/dev/null || true

# Step 4: Emit assistant message with TWO parallel Agent tool_use blocks
echo '{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"I will run two tasks in parallel."},{"type":"tool_use","id":"agent-tool-1","name":"Agent","input":{"prompt":"Research task alpha"}},{"type":"tool_use","id":"agent-tool-2","name":"Agent","input":{"prompt":"Research task beta"}}]}}'

# Small delay so TUI can render the parallel containers
sleep 2

# Step 4b (QUM-386 live-path): emit sidechain assistant messages with
# parent_tool_use_id pointing at each outer Agent. Each sidechain message
# carries an inner tool_use (Read) whose attribution must follow the wire
# parent_tool_use_id, NOT the "last active agent" heuristic. With the heuristic
# both alpha-file.txt and beta-file.txt would land under agent-tool-2 (the
# most recently pushed); post-fix alpha lives under agent-tool-1 and beta
# under agent-tool-2.
echo '{"type":"assistant","uuid":"s-1","parent_tool_use_id":"agent-tool-1","message":{"role":"assistant","content":[{"type":"tool_use","id":"r-alpha","name":"Read","input":{"file_path":"/tmp/alpha-file.txt"}}]}}'
echo '{"type":"assistant","uuid":"s-2","parent_tool_use_id":"agent-tool-2","message":{"role":"assistant","content":[{"type":"tool_use","id":"r-beta","name":"Read","input":{"file_path":"/tmp/beta-file.txt"}}]}}'
sleep 1

# Step 5: Emit tool_result user messages for the agents
echo '{"type":"user","uuid":"u-3","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"agent-tool-1","content":"Alpha research complete with findings"}]}}'
echo '{"type":"user","uuid":"u-4","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"agent-tool-2","content":"Beta research complete with results"}]}}'

# Small delay so TUI can render the collapsed containers
sleep 1

# Step 6: Emit final result
echo '{"type":"result","subtype":"success","is_error":false,"result":"Both tasks completed.","duration_ms":500,"num_turns":1,"total_cost_usd":0.05}'

# Keep alive briefly so the TUI doesn't EOF too fast
sleep 2
FAKESCRIPT
chmod +x "$FAKE_CLAUDE"

# --- Test infrastructure ---
PASS_COUNT=0
FAIL_COUNT=0
pass() { PASS_COUNT=$((PASS_COUNT + 1)); echo "  PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); echo "  FAIL: $1" >&2; }

capture_pane() { _stmux capture-pane -t "$1" -p 2>/dev/null || true; }

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
    if [ -n "${SPRAWL_TMUX_SOCKET:-}" ]; then
        tmux -L "$SPRAWL_TMUX_SOCKET" kill-server 2>/dev/null || true
        rm -f -- "/tmp/tmux-$(id -u)/$SPRAWL_TMUX_SOCKET" 2>/dev/null || true
    fi
    case "$SPRAWL_ROOT" in
        /tmp/*) rm -rf -- "$SPRAWL_ROOT" ;;
    esac
    exit "$rc"
}
trap cleanup EXIT INT TERM HUP

# QUM-458 layer 1: setsid'd watchdog reaps the sandbox if the driver dies via
# SIGKILL (which bypasses bash's EXIT trap).
# shellcheck source=lib/sandbox-traps.sh
. "$(dirname "$0")/lib/sandbox-traps.sh"
sandbox_install_watchdog "$$" "$SPRAWL_TMUX_SOCKET" "$SPRAWL_ROOT"

# --- Launch the TUI with fake claude ---
echo "=== Launching sprawl enter with fake claude in tmux ==="

# Put fake claude first in PATH so spawl enter finds it
export PATH="$SPRAWL_ROOT:$PATH"
# Rename fake-claude to claude
cp "$FAKE_CLAUDE" "$SPRAWL_ROOT/claude"
chmod +x "$SPRAWL_ROOT/claude"

_stmux new-session -d -s "$SESSION" -x 120 -y 40 \
    "SPRAWL_ROOT='$SPRAWL_ROOT' PATH='$SPRAWL_ROOT:$PATH' '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
_stmux set-option -t "$SESSION" window-size manual >/dev/null
_stmux resize-window -t "$SESSION" -x 120 -y 40 >/dev/null

# --- Test A: TUI starts up ---
echo ""
echo "=== Test A: TUI renders ==="
if wait_for_pattern "$SESSION" "weave" 15; then
    pass "TUI rendered (weave visible)"
else
    fail "TUI did not render within 15s"
    echo "  pane:" >&2
    capture_pane "$SESSION" | tail -30 >&2
    echo "  stderr:" >&2
    [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    echo "==============================="
    exit 1
fi

# --- Send a user message to trigger the fake claude's response ---
echo ""
echo "=== Sending user message via tmux ==="
# Wait a moment for initialization to complete
sleep 2
_stmux send-keys -t "$SESSION" "run parallel tasks" Enter

# --- Test B: Two parallel Agent containers appear ---
echo ""
echo "=== Test B: Two parallel Agent containers render ==="

# Wait for both Agent tool calls to appear in the viewport.
# The fake claude emits them after reading the user message.
if wait_for_pattern "$SESSION" "Agent" 15; then
    pass "Agent tool call appeared in viewport"
else
    fail "Agent tool call never appeared"
    echo "  pane:" >&2
    capture_pane "$SESSION" | tail -30 >&2
    echo "  stderr:" >&2
    [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
fi

# Check for two ┌ markers (one per Agent container).
# Give it a moment for the second one to render.
sleep 2
PANE_CONTENT=$(capture_pane "$SESSION")
OPEN_MARKERS=$(echo "$PANE_CONTENT" | grep -c "┌" || true)

if [ "$OPEN_MARKERS" -ge 2 ]; then
    pass "Two independent ┌ markers found ($OPEN_MARKERS total) — parallel containers"
else
    fail "Expected at least 2 ┌ markers for two Agent containers, found $OPEN_MARKERS"
    echo "  pane:" >&2
    echo "$PANE_CONTENT" >&2
fi

# Check both task descriptions appear
if echo "$PANE_CONTENT" | grep -q "Research task alpha"; then
    pass "Agent container for 'Research task alpha' visible"
else
    fail "Agent container for 'Research task alpha' not found"
    echo "  pane:" >&2
    echo "$PANE_CONTENT" >&2
fi

if echo "$PANE_CONTENT" | grep -q "Research task beta"; then
    pass "Agent container for 'Research task beta' visible"
else
    fail "Agent container for 'Research task beta' not found"
    echo "  pane:" >&2
    echo "$PANE_CONTENT" >&2
fi

# --- Test B2: Sidechain inner tool_uses attribute to correct parent (QUM-386 live-path) ---
echo ""
echo "=== Test B2: Sidechain Reads attribute to correct parent Agent ==="

# Allow extra time for sidechain assistant messages + inner Reads to render
# inside their respective containers before tool_results collapse them.
sleep 2
PANE_B2=$(capture_pane "$SESSION")

# Locate the line numbers of each outer Agent container by their unique task
# descriptions. Container 1 = alpha, Container 2 = beta.
ALPHA_HEADER_LINE=$(echo "$PANE_B2" | grep -n "Research task alpha" | head -n1 | cut -d: -f1 || true)
BETA_HEADER_LINE=$(echo "$PANE_B2"  | grep -n "Research task beta"  | head -n1 | cut -d: -f1 || true)

# The inner Reads' filenames are unique anchors for the inner tool_use lines.
ALPHA_FILE_LINE=$(echo "$PANE_B2" | grep -n "alpha-file.txt" | head -n1 | cut -d: -f1 || true)
BETA_FILE_LINE=$(echo "$PANE_B2"  | grep -n "beta-file.txt"  | head -n1 | cut -d: -f1 || true)

if [ -z "$ALPHA_HEADER_LINE" ] || [ -z "$BETA_HEADER_LINE" ]; then
    fail "could not locate both Agent container header lines (alpha=$ALPHA_HEADER_LINE beta=$BETA_HEADER_LINE)"
    echo "  pane:" >&2
    echo "$PANE_B2" >&2
elif [ -z "$ALPHA_FILE_LINE" ] || [ -z "$BETA_FILE_LINE" ]; then
    fail "inner Read filenames missing from pane (alpha-file.txt=$ALPHA_FILE_LINE beta-file.txt=$BETA_FILE_LINE)"
    echo "  pane:" >&2
    echo "$PANE_B2" >&2
else
    # alpha-file.txt must appear AFTER the alpha container header AND BEFORE
    # the beta container header — i.e. inside container 1.
    if [ "$ALPHA_FILE_LINE" -gt "$ALPHA_HEADER_LINE" ] && [ "$ALPHA_FILE_LINE" -lt "$BETA_HEADER_LINE" ]; then
        pass "alpha-file.txt nested inside container 1 (line $ALPHA_FILE_LINE between $ALPHA_HEADER_LINE and $BETA_HEADER_LINE)"
    else
        fail "alpha-file.txt at line $ALPHA_FILE_LINE NOT between container 1 header ($ALPHA_HEADER_LINE) and container 2 header ($BETA_HEADER_LINE) — mis-attributed to wrong Agent"
        echo "  pane:" >&2
        echo "$PANE_B2" >&2
    fi

    # beta-file.txt must appear AFTER the beta container header (inside
    # container 2 — pane end is implicit upper bound).
    if [ "$BETA_FILE_LINE" -gt "$BETA_HEADER_LINE" ]; then
        pass "beta-file.txt nested inside container 2 (line $BETA_FILE_LINE after $BETA_HEADER_LINE)"
    else
        fail "beta-file.txt at line $BETA_FILE_LINE NOT after container 2 header ($BETA_HEADER_LINE) — mis-attributed"
        echo "  pane:" >&2
        echo "$PANE_B2" >&2
    fi
fi

# --- Test C: Containers collapse after completion ---
echo ""
echo "=== Test C: Containers collapse after Agent results arrive ==="

# Wait for the result to arrive (fake claude sleeps briefly then sends results)
if wait_for_pattern "$SESSION" "Alpha research complete" 20; then
    pass "Agent result 'Alpha research complete' visible (container collapsed)"
else
    fail "Agent result 'Alpha research complete' not found"
    echo "  pane:" >&2
    capture_pane "$SESSION" | tail -30 >&2
fi

if wait_for_pattern "$SESSION" "Beta research complete" 20; then
    pass "Agent result 'Beta research complete' visible (container collapsed)"
else
    fail "Agent result 'Beta research complete' not found"
    echo "  pane:" >&2
    capture_pane "$SESSION" | tail -30 >&2
fi

# --- Summary ---
echo ""
echo "==============================="
echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "==============================="

if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
fi
exit 0
