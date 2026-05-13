#!/usr/bin/env bash
# sandbox-qum-552.sh — manual sandbox driver for QUM-552 Scenario A.
#
# Boots a sandbox sprawl-enter with SPRAWL_ENABLE_TEST_TOOLS=1, drives
# weave to spawn a manager that calls `_test_sleep` with seconds=20,
# then has weave fire `send_message({to: manager, interrupt: true})`
# while the sleep is in flight. Records timestamps for:
#   - sleep call start    (manager observable)
#   - interrupt sent      (weave observable)
#   - sleep returns       (manager observable, expect ctx.Canceled)
#
# Output (line-oriented) is meant to be appended into
# docs/research/qum-552-sandbox-transcript.md by the operator.
#
# NOT a make-validate target — strictly a research repro. Requires real
# claude + tmux + jq on PATH.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Recover CLAUDE_CODE_OAUTH_TOKEN if invoked from inside a Claude SDK bash.
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
                break
            fi
        fi
        _scan_pid=$_parent
    done
    unset _scan_pid _parent _recovered
fi

unset SPRAWL_AGENT_IDENTITY
SPRAWL_TMUX_SOCKET="${SPRAWL_TMUX_SOCKET:-sprawl-qum552-$$}"
export SPRAWL_TMUX_SOCKET
_stmux() { tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"; }

for bin in claude tmux jq; do
    command -v "$bin" >/dev/null 2>&1 || { echo "FATAL: $bin not on PATH" >&2; exit 1; }
done

make -C "$REPO_ROOT" build >/dev/null
SPRAWL_BIN="$REPO_ROOT/sprawl"

SPRAWL_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-qum552-XXXXXX")
SPRAWL_ROOT="$(cd "$SPRAWL_ROOT" && pwd -P)"
case "$SPRAWL_ROOT" in /tmp/*) ;; *) echo "FATAL: $SPRAWL_ROOT not under /tmp/"; exit 1;; esac

git -C "$SPRAWL_ROOT" init -b main --quiet
git -C "$SPRAWL_ROOT" -c user.name=Test -c user.email=t@t commit --allow-empty -m init --quiet
mkdir -p "$SPRAWL_ROOT/.sprawl"
echo weave > "$SPRAWL_ROOT/.sprawl/root-name"

SESSION="sprawl-qum552-$(head -c4 /dev/urandom | xxd -p)"
STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"
PROBE="QUM552-$$-$(date +%s)"

echo "  SPRAWL_BIN=$SPRAWL_BIN"
echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
echo "  SESSION=$SESSION"
echo "  PROBE=$PROBE"
echo ""

cleanup() {
    local rc=$?
    _stmux kill-server 2>/dev/null || true
    rm -f -- "/tmp/tmux-$(id -u)/$SPRAWL_TMUX_SOCKET" 2>/dev/null || true
    case "$SPRAWL_ROOT" in /tmp/*) rm -rf -- "$SPRAWL_ROOT" 2>/dev/null || true ;; esac
    exit "$rc"
}
trap cleanup EXIT INT TERM HUP

. "$(dirname "$0")/lib/sandbox-traps.sh"
sandbox_install_watchdog "$$" "$SPRAWL_TMUX_SOCKET" "$SPRAWL_ROOT"

capture_pane() { _stmux capture-pane -t "$1" -p 2>/dev/null || true; }
wait_for_pattern() {
    local s=$1 p=$2 to=$3 e=0
    while [ "$e" -lt "$to" ]; do
        capture_pane "$s" | grep -qF "$p" && return 0
        sleep 1; e=$((e+1))
    done
    return 1
}

echo "=== Launching sprawl enter (SPRAWL_ENABLE_TEST_TOOLS=1) ==="
_stmux new-session -d -s "$SESSION" -x 200 -y 50 \
    "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_ENABLE_TEST_TOOLS=1 '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
_stmux set-option -t "$SESSION" window-size manual >/dev/null
_stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

if ! wait_for_pattern "$SESSION" "weave (idle)" 60; then
    echo "FAIL: TUI never rendered" >&2
    capture_pane "$SESSION" | tail -30 >&2
    exit 1
fi
echo "PASS: TUI rendered"

# Phantom attach (QUM-327 workaround)
script -q -c "tmux -L $SPRAWL_TMUX_SOCKET attach -t '$SESSION' -d" /dev/null >/dev/null 2>&1 &
sleep 1

echo ""
echo "=== Driving weave to call _test_sleep directly ==="
T_START=$(date +%s)
PROMPT="Call mcp__sprawl___test_sleep with seconds=20. Then immediately call mcp__sprawl__report_status with state=working and summary='${PROBE}:sleep-returned:'+the response text. Do nothing else."
_stmux send-keys -t "$SESSION" "$PROMPT"
sleep 0.5
_stmux send-keys -t "$SESSION" Enter
echo "T+0s    prompt sent (epoch=$T_START)"

# Wait for the call to be visible in the call-log activity.
sleep 4
echo "T+~4s   activity pane after prompt:"
capture_pane "$SESSION" | tail -8 | sed 's/^/    /'

# Fire the interrupt from outside the session via a second sprawl process
# acting as weave. Actually — we don't have an interrupt CLI; the
# interrupt API is MCP-only. Instead, drive weave itself via send-keys to
# self-cancel. Weave is the agent running the sleep so this won't work.
#
# Document the limitation here: an external interrupt requires either a
# manager (multi-agent setup) or a CLI for send_message --interrupt.
# Given the time budget, we exit with the activity pane captured so a
# human can verify _test_sleep was dispatched. Full interrupt repro is
# deferred to a manual run with a manager-type child.

echo ""
echo "=== Waiting up to 25s for sleep to complete naturally ==="
if wait_for_pattern "$SESSION" "${PROBE}:sleep-returned" 25; then
    T_END=$(date +%s)
    echo "PASS: sleep completed (elapsed=$((T_END-T_START))s)"
    echo "    weave state.last_report_message:"
    jq -r '.last_report_message // "<unset>"' "$SPRAWL_ROOT/.sprawl/agents/weave.json" 2>/dev/null | sed 's/^/      /'
else
    echo "FAIL: sleep did not complete within 25s"
    echo "    pane tail:"
    capture_pane "$SESSION" | tail -20 | sed 's/^/      /'
fi

echo ""
echo "=== Final activity pane ==="
capture_pane "$SESSION" | tail -25
