#!/usr/bin/env bash
# test-bridge-lifecycle-e2e.sh — End-to-end regression guard for QUM-467:
# Child agents must NOT lose MCP connectivity when weave's claude
# subprocess is restarted (handoff / resume / etc.).
#
# Pre-fix (red): cmd/enter.go constructs a fresh host.MCPBridge() at three
# weave-launch sites. The bridge a child registered against during its
# spawn is therefore tied to the original weave-claude lifetime; after a
# weave-claude restart, every `mcp__sprawl__*` tool call from the child
# fails with "stream closed".
#
# What this script does:
#   1. Spins up an isolated /tmp sandbox + .sprawl/root-name=weave.
#   2. Launches `sprawl enter` in a detached tmux session (200x50) so
#      bubbletea has a pseudo-terminal.
#   3. Drives weave's claude (via tmux send-keys) to call
#      `mcp__sprawl__spawn` with a deterministic child prompt. The child
#      is a REAL claude subprocess holding an open MCP session against
#      weave's bridge — the failure mode under test is precisely this
#      session being severed by the weave-claude restart.
#   4. The child's prompt instructs it to:
#        a. Call mcp__sprawl__send_async to weave with marker A token.
#        b. Wait for a signal file at $SPRAWL_ROOT/.sprawl/child-go on disk.
#        c. Call mcp__sprawl__send_async to weave with marker B token.
#        d. Call mcp__sprawl__report_status with status=complete.
#      Crucially the child holds a long-lived MCP session across (a)→(c).
#   5. Driver waits for marker A in weave's maildir, then drives weave to
#      call mcp__sprawl__handoff. Asserts handoff-signal file appears
#      and a NEW claude subprocess (different pid + --session-id) is up.
#   6. Driver writes the signal file. Child's tool call (c) fires through
#      the (potentially severed) MCP bridge.
#   7. Asserts:
#        * marker A landed pre-restart (proves baseline)
#        * marker B landed post-restart (proves bridge survived) — THIS
#          is the QUM-467 deciding assertion.
#        * child agent log/transcript contains no "stream closed" or
#          "broken pipe" error from the second send_async call.
#
# WHY A REAL CLAUDE CHILD: A synthetic CLI proxy (sprawl messages send)
# writes directly to the maildir and bypasses weave's MCP bridge entirely.
# That cannot detect bridge severance. Only a real long-lived MCP session
# from the child to weave exercises the failure mode.
#
# Gate: requires `claude` on PATH. If absent:
#   SPRAWL_E2E_SKIP_NO_CLAUDE=1 → skip (exit 0)
#   otherwise                   → exit 1
#
# Usage: bash scripts/test-bridge-lifecycle-e2e.sh
#
# NOTE: creates a real tmux session + multiple real claude subprocesses
# (weave + child + new weave post-handoff). Do not run in parallel.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# --- Recover CLAUDE_CODE_OAUTH_TOKEN from an ancestor env (QUM-411) ---
if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
    _scan_pid=$$
    for _ in 1 2 3 4 5 6 7 8; do
        _parent=$(awk '{print $4}' "/proc/$_scan_pid/stat" 2>/dev/null || true)
        [ -z "$_parent" ] || [ "$_parent" = "0" ] && break
        if [ -r "/proc/$_parent/environ" ]; then
            _recovered=$(tr '\0' '\n' < "/proc/$_parent/environ" \
                | grep '^CLAUDE_CODE_OAUTH_TOKEN=' | cut -d= -f2- || true)
            if [ -n "$_recovered" ]; then
                export CLAUDE_CODE_OAUTH_TOKEN="$_recovered"
                echo "  (recovered CLAUDE_CODE_OAUTH_TOKEN from ancestor pid=$_parent)"
                break
            fi
        fi
        _scan_pid=$_parent
    done
    unset _scan_pid _parent _recovered
fi

# --- Dedicated tmux socket for sandbox isolation (QUM-325) ---
SPRAWL_TMUX_SOCKET="${SPRAWL_TMUX_SOCKET:-sprawl-bridge-lifecycle-e2e-$$}"
export SPRAWL_TMUX_SOCKET
_stmux() { tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"; }

# --- Preflight ---

if ! command -v claude >/dev/null 2>&1; then
    if [ "${SPRAWL_E2E_SKIP_NO_CLAUDE:-}" = "1" ]; then
        echo "SKIP: claude binary not found on PATH (SPRAWL_E2E_SKIP_NO_CLAUDE=1 set)"
        exit 0
    fi
    echo "FATAL: claude binary not found on PATH — bridge-lifecycle e2e requires a real claude" >&2
    echo "       Set SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip this test." >&2
    exit 1
fi

if ! command -v tmux >/dev/null 2>&1; then
    echo "FATAL: tmux binary not found on PATH" >&2
    exit 1
fi

echo "=== Building sprawl ==="
make -C "$REPO_ROOT" build >/dev/null
SPRAWL_BIN="$REPO_ROOT/sprawl"
if [ ! -x "$SPRAWL_BIN" ]; then
    echo "FATAL: sprawl binary not found at $SPRAWL_BIN" >&2
    exit 1
fi

# --- Sandbox under /tmp/ ---

SPRAWL_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-bridge-lifecycle-e2e-XXXXXX")
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
mkdir -p "$SPRAWL_ROOT/.sprawl" "$SPRAWL_ROOT/.sprawl/memory" "$SPRAWL_ROOT/.sprawl/state"
echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"

SESSION="sprawl-bridge-lifecycle-$(head -c4 /dev/urandom | xxd -p)"
STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"
CHILD_GO_FILE="$SPRAWL_ROOT/.sprawl/child-go"
TOKEN_A="BRIDGE-LIFECYCLE-A-$$-$(date +%s)"
TOKEN_B="BRIDGE-LIFECYCLE-B-$$-$(date +%s)"

echo "  SPRAWL_BIN=$SPRAWL_BIN"
echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
echo "  SESSION=$SESSION"
echo "  TOKEN_A=$TOKEN_A"
echo "  TOKEN_B=$TOKEN_B"
echo "  CHILD_GO_FILE=$CHILD_GO_FILE"
echo ""

# --- Test infra ---

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

# Search weave's maildir for a token in any message body. Returns 0 if found.
weave_maildir_has_token() {
    local token="$1"
    local dir="$SPRAWL_ROOT/.sprawl/messages/weave"
    [ -d "$dir" ] || return 1
    grep -rl --include='*.json' "$token" "$dir" >/dev/null 2>&1
}

wait_for_token_in_weave_maildir() {
    local token="$1" timeout="$2"
    local elapsed=0
    while [ "$elapsed" -lt "$timeout" ]; do
        if weave_maildir_has_token "$token"; then
            return 0
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    return 1
}

# Find weave's claude subprocess pid (matches stream-json + sandbox path).
find_weave_claude_pid() {
    pgrep -af 'claude' 2>/dev/null | awk -v root="$SPRAWL_ROOT" '
        $0 ~ "stream-json" && index($0, root) > 0 && index($0, "weave") > 0 { print $1; exit }
    '
}

# Find ANY claude subprocess in this sandbox (used to detect new pid post-handoff).
find_any_claude_pid() {
    pgrep -af 'claude' 2>/dev/null | awk -v root="$SPRAWL_ROOT" '
        $0 ~ "stream-json" && index($0, root) > 0 { print $1 }
    '
}

# Search child agent transcripts/logs for a regex (bridge severance signals).
child_logs_match() {
    local re="$1"
    local logs_dir="$SPRAWL_ROOT/.sprawl/logs"
    [ -d "$logs_dir" ] || return 1
    grep -rE "$re" "$logs_dir" >/dev/null 2>&1
}

cleanup() {
    local rc=$?
    if [ -n "${PHANTOM_PID:-}" ]; then
        kill "$PHANTOM_PID" 2>/dev/null || true
    fi
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

# QUM-458 layer 1: setsid'd watchdog.
# shellcheck source=lib/sandbox-traps.sh
. "$(dirname "$0")/lib/sandbox-traps.sh"
sandbox_install_watchdog "$$" "$SPRAWL_TMUX_SOCKET" "$SPRAWL_ROOT"

# --- Launch the TUI ---

echo "=== Launching sprawl enter ==="
_stmux new-session -d -s "$SESSION" -x 200 -y 50 \
    "SPRAWL_ROOT='$SPRAWL_ROOT' '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
_stmux set-option -t "$SESSION" window-size manual >/dev/null
_stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

if wait_for_pattern "$SESSION" "weave \\(idle\\)" 45; then
    pass "TUI rendered"
else
    fail "TUI did not render within 45s"
    capture_pane "$SESSION" | tail -30 >&2
    [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
    _stmux send-keys -t "$SESSION" "1" Enter
    sleep 1
fi
sleep 3

# --- Phantom tmux client (QUM-327) so send-keys reaches bubbletea ---

echo ""
echo "=== Attaching phantom tmux client ==="
script -q -c "tmux ${SPRAWL_TMUX_SOCKET:+-L $SPRAWL_TMUX_SOCKET} attach -t '$SESSION' -d" /dev/null >/dev/null 2>&1 &
PHANTOM_PID=$!
sleep 1

OLD_WEAVE_PID="$(find_weave_claude_pid)"
if [ -z "$OLD_WEAVE_PID" ]; then
    # Fallback: any claude in the sandbox is weave's, as we haven't spawned
    # a child yet.
    OLD_WEAVE_PID="$(find_any_claude_pid | head -1)"
fi
echo "  old weave claude pid=$OLD_WEAVE_PID"

# --- Drive weave to spawn a child agent ---
#
# The child's prompt is deterministic and uses real MCP tools. We pass
# the absolute paths for the signal file + tokens via the prompt so the
# child knows what to watch.

echo ""
echo "=== Drive weave to spawn child via mcp__sprawl__spawn ==="
SPAWN_PROMPT="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum-467-child-probe', and prompt set to exactly: 'You are a QUM-467 bridge-lifecycle probe. STEP 1: call mcp__sprawl__send_async with to=weave subject=marker-a body=${TOKEN_A}. STEP 2: poll the path ${CHILD_GO_FILE} once per 5 seconds for up to 5 minutes using the Bash tool (test -f ${CHILD_GO_FILE}); when it exists, proceed. STEP 3: call mcp__sprawl__send_async with to=weave subject=marker-b body=${TOKEN_B}. STEP 4: call mcp__sprawl__report_status with status=complete summary=done. Do not do anything else. If any send_async call fails, return its full error message verbatim before proceeding.'"

_stmux send-keys -t "$SESSION" "$SPAWN_PROMPT"
sleep 0.5
_stmux send-keys -t "$SESSION" Enter

# --- Wait for marker A to land ---

echo ""
echo "=== Waiting for marker A (pre-restart child→weave MCP send) ==="
if wait_for_token_in_weave_maildir "$TOKEN_A" 240; then
    pass "marker A landed in weave maildir (child MCP send_async pre-restart works)"
else
    fail "marker A never landed within 240s — child failed to spawn or first send_async failed"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

# --- Drive weave to call handoff ---

echo ""
echo "=== Firing handoff via MCP ==="
HANDOFF_PROMPT="Call mcp__sprawl__handoff with summary='QUM-467 bridge-lifecycle e2e'."
_stmux send-keys -t "$SESSION" "$HANDOFF_PROMPT"
sleep 0.5
_stmux send-keys -t "$SESSION" Enter

# Detection strategy mirrors test-handoff-e2e.sh: the handoff-signal file
# is transient (removed by FinalizeHandoff once consolidation finishes),
# so polling for it as a level-triggered signal races against the
# consumer. Accept any of:
#   (a) handoff-signal file present (transient — caught only if poll
#       happens to land in the window),
#   (b) a new sessions/*.md file written (proves Handoff() ran),
#   (c) a new weave-claude pid distinct from OLD_WEAVE_PID (proves the
#       supervisor restart completed — strongest evidence and the most
#       race-resistant signal).
# The deciding bridge-lifecycle assertion (marker B) is what matters; we
# just need durable evidence that the restart actually happened before
# we release the child.
HANDOFF_SIGNAL="$SPRAWL_ROOT/.sprawl/memory/handoff-signal"
SESSIONS_DIR="$SPRAWL_ROOT/.sprawl/memory/sessions"
ELAPSED=0
HANDOFF_FIRED=0
NEW_WEAVE_PID=""
# Bumped to 300s — handoff + consolidation + new claude spawn can take
# 60-120s on its own, and the pre-consolidation send_async to weave that
# kicks the LLM off the handoff tool call adds latency.
while [ "$ELAPSED" -lt 300 ]; do
    if [ -f "$HANDOFF_SIGNAL" ]; then
        HANDOFF_FIRED=1
    fi
    if ls "$SESSIONS_DIR"/*.md >/dev/null 2>&1; then
        HANDOFF_FIRED=1
    fi
    cand="$(find_weave_claude_pid)"
    if [ -n "$cand" ] && [ "$cand" != "$OLD_WEAVE_PID" ]; then
        NEW_WEAVE_PID="$cand"
        HANDOFF_FIRED=1
        break
    fi
    sleep 2
    ELAPSED=$((ELAPSED + 2))
done
if [ "$HANDOFF_FIRED" -eq 1 ]; then
    pass "handoff fired (signal/sessions-md/new-pid evidence observed)"
else
    fail "handoff never fired within 300s — claude didn't call mcp__sprawl__handoff"
    capture_pane "$SESSION" | tail -40 >&2
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

# If we haven't yet seen a new pid, give it more time — the restart can
# complete after the signal/sessions evidence appeared.
if [ -z "$NEW_WEAVE_PID" ]; then
    ELAPSED=0
    while [ "$ELAPSED" -lt 180 ]; do
        cand="$(find_weave_claude_pid)"
        if [ -n "$cand" ] && [ "$cand" != "$OLD_WEAVE_PID" ]; then
            NEW_WEAVE_PID="$cand"
            break
        fi
        sleep 2
        ELAPSED=$((ELAPSED + 2))
    done
fi
if [ -n "$NEW_WEAVE_PID" ]; then
    pass "new weave claude pid=$NEW_WEAVE_PID (was $OLD_WEAVE_PID) — restart completed"
else
    fail "no new weave claude subprocess within 180s — restart did not complete"
fi

# Settle: let the new bridge bind, claude initialize MCP.
sleep 5

# --- Release the child to fire the post-restart MCP send ---

echo ""
echo "=== Releasing child (post-restart child→weave MCP send) ==="
touch "$CHILD_GO_FILE"
echo "  touched $CHILD_GO_FILE"

if wait_for_token_in_weave_maildir "$TOKEN_B" 300; then
    pass "marker B landed in weave maildir post-restart — bridge survived (QUM-467 fixed)"
else
    fail "marker B never landed within 300s post-restart — child MCP bridge severed (QUM-467 reproduced)"
    echo "  child logs (severance signals):" >&2
    if child_logs_match "stream closed|broken pipe|connection refused"; then
        echo "  child logs contain bridge-severance error(s):" >&2
        grep -rE "stream closed|broken pipe|connection refused" "$SPRAWL_ROOT/.sprawl/logs" 2>/dev/null | head -10 >&2 || true
    fi
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -30 >&2
fi

# Independent severance-signal check: even if marker B happens to land
# (e.g. through some retry path), the child's logs should not show
# "stream closed" between the first and second send. Pre-fix this regex
# is the smoking-gun signature.
if child_logs_match "stream closed|broken pipe|connection refused"; then
    fail "child logs contain bridge-severance error (stream closed / broken pipe / connection refused) — QUM-467 reproduced"
    grep -rE "stream closed|broken pipe|connection refused" "$SPRAWL_ROOT/.sprawl/logs" 2>/dev/null | head -10 >&2 || true
else
    pass "child logs free of bridge-severance error patterns"
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
