#!/usr/bin/env bash
# test-paste-coalesce-e2e.sh — End-to-end gate for the QUM-608 stdin
# paste coalescer wired into `sprawl enter`.
#
# Background: tmux <3.4 in this stack (coder web terminal → tmux 3.2a)
# fails to deliver bracketed-paste markers to a Bubble Tea program
# running in alt-screen mode, so a pasted chunk of text arrives as one
# KeyPressMsg per byte. The QUM-455 Enter-lookahead then blocks submit
# for ~30-150s on a 500-char paste. QUM-608 wraps os.Stdin in
# internal/inputcoalesce.Coalescer, which detects bursts of printable
# bytes and synthesizes ESC[200~ ... ESC[201~ markers so Bubble Tea
# emits a single tea.PasteMsg — visual is instant.
#
# What this script does:
#   1. Spins up an isolated /tmp sandbox.
#   2. Launches `sprawl enter` in a detached tmux session (coalescer ON
#      by default).
#   3. Sends a 200-character literal burst into the pane via
#      `tmux send-keys -l` — which writes the bytes to the inner pty
#      in one batch, matching real paste behavior.
#   4. Asserts the full 200-char payload appears in the pane within 5s
#      (well below the ~30s typewriter-animation budget the bug
#      produces). The payload is a deterministic prefix+marker pattern
#      that we can grep for in the pane capture.
#   5. Sends SIGINT and asserts the sprawl process exits cleanly
#      within 10s (catches deadlocks in the coalescer's Close path).
#
# Requires a real `claude` binary on PATH; set SPRAWL_E2E_SKIP_NO_CLAUDE=1
# to skip.
#
# Usage: bash scripts/test-paste-coalesce-e2e.sh

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

unset SPRAWL_AGENT_IDENTITY

SPRAWL_TMUX_SOCKET="${SPRAWL_TMUX_SOCKET:-sprawl-paste-e2e-$$}"
export SPRAWL_TMUX_SOCKET
_stmux() { tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"; }

if ! command -v claude >/dev/null 2>&1; then
    if [ "${SPRAWL_E2E_SKIP_NO_CLAUDE:-}" = "1" ]; then
        echo "SKIP: claude binary not found on PATH (SPRAWL_E2E_SKIP_NO_CLAUDE=1 set)"
        exit 0
    fi
    echo "FATAL: claude binary not found on PATH — QUM-608 e2e requires a real claude" >&2
    echo "       Set SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip this test." >&2
    exit 1
fi
if ! command -v tmux >/dev/null 2>&1; then echo "FATAL: tmux not on PATH" >&2; exit 1; fi

echo "=== Building sprawl ==="
make -C "$REPO_ROOT" build >/dev/null
SPRAWL_BIN="$REPO_ROOT/sprawl"
if [ ! -x "$SPRAWL_BIN" ]; then
    echo "FATAL: sprawl binary not found at $SPRAWL_BIN" >&2
    exit 1
fi

# --- Sandbox under /tmp/ ---
SPRAWL_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-qum608-XXXXXX")
SPRAWL_ROOT_REAL="$(cd "$SPRAWL_ROOT" 2>/dev/null && pwd -P || echo "$SPRAWL_ROOT")"
case "$SPRAWL_ROOT_REAL" in
    /tmp/*) ;;
    *) echo "FATAL: sandbox SPRAWL_ROOT=$SPRAWL_ROOT_REAL not under /tmp/; aborting" >&2; exit 1 ;;
esac
SPRAWL_ROOT="$SPRAWL_ROOT_REAL"

git -C "$SPRAWL_ROOT" init -b main --quiet
git -C "$SPRAWL_ROOT" -c user.name="Test" -c user.email="test@test" \
    commit --allow-empty -m "init" --quiet
mkdir -p "$SPRAWL_ROOT/.sprawl"
echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"

if [ -f "$REPO_ROOT/.env" ]; then
    cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
fi

SESSION="sprawl-paste-e2e-$(head -c4 /dev/urandom | xxd -p)"
STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"

# Deterministic 200-char payload: leading sentinel + middle filler +
# trailing sentinel. The middle is all printable lowercase 'a' so the
# coalescer's shouldWrapBurst heuristic (no ESC bytes) matches; without
# the coalescer, tmux 3.2a would deliver these as 200 separate
# KeyPressMsg events and the InputModel's per-rune handler would
# repaint after each one — visible as typewriter animation.
PASTE_HEAD="QUM608HEAD"
PASTE_TAIL="QUM608TAIL"
PASTE_FILL=$(printf 'a%.0s' $(seq 1 180))
PASTE_BODY="${PASTE_HEAD}${PASTE_FILL}${PASTE_TAIL}"

echo "  SPRAWL_BIN=$SPRAWL_BIN"
echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
echo "  SESSION=$SESSION"
echo "  paste body length=${#PASTE_BODY}"
echo ""

PASS_COUNT=0
FAIL_COUNT=0
pass() { PASS_COUNT=$((PASS_COUNT + 1)); echo "  PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); echo "  FAIL: $1" >&2; }

capture_pane() { _stmux capture-pane -t "$1" -p 2>/dev/null || true; }

wait_for_pattern() {
    local session="$1" pattern="$2" timeout="$3" elapsed=0
    while [ "$elapsed" -lt "$timeout" ]; do
        if capture_pane "$session" | grep -qE "$pattern"; then return 0; fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    return 1
}
wait_for_pattern_fast() {
    local session="$1" pattern="$2" timeout="$3"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        if capture_pane "$session" | grep -qE "$pattern"; then return 0; fi
        sleep 0.2
    done
    return 1
}

PHANTOM_PID=""
cleanup() {
    local rc=$?
    if [ -n "${PHANTOM_PID:-}" ]; then kill "$PHANTOM_PID" 2>/dev/null || true; fi
    if [ -n "${SPRAWL_TMUX_SOCKET:-}" ]; then
        tmux -L "$SPRAWL_TMUX_SOCKET" kill-server 2>/dev/null || true
        rm -f -- "/tmp/tmux-$(id -u)/$SPRAWL_TMUX_SOCKET" 2>/dev/null || true
    fi
    case "$SPRAWL_ROOT" in
        /tmp/*)
            local attempt
            for attempt in 1 2 3 4 5; do
                if rm -rf -- "$SPRAWL_ROOT" 2>/dev/null; then break; fi
                sleep 1
            done
            ;;
    esac
    exit "$rc"
}
trap cleanup EXIT INT TERM HUP

. "$(dirname "$0")/lib/sandbox-traps.sh"
sandbox_install_watchdog "$$" "$SPRAWL_TMUX_SOCKET" "$SPRAWL_ROOT"

# --- Launch the TUI ---

echo "=== Launching sprawl enter ==="
_stmux new-session -d -s "$SESSION" -x 240 -y 50 \
    "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_CLAUDE='$REPO_ROOT/scripts/run-claude' '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
_stmux set-option -t "$SESSION" window-size manual >/dev/null
_stmux resize-window -t "$SESSION" -x 240 -y 50 >/dev/null

if wait_for_pattern "$SESSION" "weave \\(idle\\)" 45; then
    pass "TUI rendered ('weave (idle)' visible)"
else
    fail "TUI did not render within 45s"
    capture_pane "$SESSION" | tail -30 >&2
    [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
    exit 1
fi
if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
    _stmux send-keys -t "$SESSION" "1" Enter
    sleep 1
fi
sleep 2

echo ""
echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
script -q -c "tmux ${SPRAWL_TMUX_SOCKET:+-L $SPRAWL_TMUX_SOCKET} attach -t '$SESSION' -d" /dev/null >/dev/null 2>&1 &
PHANTOM_PID=$!
sleep 1

# Wait for weave session to be live (claude subprocess attached) so the
# input panel is actually able to receive keystrokes. The "weave (idle)"
# row only confirms the supervisor sees the agent — there's a separate
# startup window before the TUI fully accepts input.
sleep 5

# Capture the sprawl PID via the tmux pane's foreground process. The
# tmux pane's shell forks the sprawl binary; we want the sprawl PID,
# not the shell's. Walk children of the pane PID until we find one
# whose argv matches "sprawl enter".
PANE_PID=$(_stmux display -t "$SESSION" -p '#{pane_pid}' 2>/dev/null || true)
SPRAWL_PID=""
if [ -n "$PANE_PID" ]; then
    # The pane's command is the shell (because tmux new-session was
    # given a shell pipeline). Find a descendant whose comm is "sprawl"
    # — robust against stale sprawl processes from prior test runs.
    for cand in $(pgrep -P "$PANE_PID" 2>/dev/null || true); do
        if [ "$(cat "/proc/$cand/comm" 2>/dev/null || true)" = "sprawl" ]; then
            SPRAWL_PID="$cand"
            break
        fi
    done
fi
if [ -z "$SPRAWL_PID" ]; then
    fail "could not locate sprawl process PID under tmux pane (pane_pid=$PANE_PID)"
    exit 1
fi
echo "  sprawl PID=$SPRAWL_PID (under tmux pane PID=$PANE_PID)"

# --- Phase 1: paste burst ---

echo ""
echo "=== Phase 1: inject 200-char burst into input panel ==="
PASTE_START_SECS=$SECONDS

# `tmux send-keys -l` writes the literal bytes to the inner pty in one
# write(2). Without the coalescer this surfaces as one KeyPressMsg per
# byte; with the coalescer, the readLoop sees a single ~200B chunk and
# wraps it in ESC[200~/ESC[201~, producing one tea.PasteMsg.
_stmux send-keys -t "$SESSION" -l "$PASTE_BODY"

# Assert the head AND tail of the payload appear in the pane within 5s.
# 5s is well below the bug's ~30s typewriter budget for 200 chars.
# Head and tail are checked independently (grep -F per sentinel) because
# the input panel renders the 200-char paste across multiple visual
# lines and the tmux pane capture inserts line breaks at the panel's
# rendered column boundary.
PASTE_END=$((SECONDS + 10))
PASTE_OK=0
ITER=0
while [ "$SECONDS" -lt "$PASTE_END" ]; do
    ITER=$((ITER + 1))
    # Use -S - -E - to capture full scrollback (alt-screen content +
    # any wrapped overflow). Some renders push the paste below the
    # visible viewport (long single-line content), so the default
    # capture-pane -p which only sees the visible area can miss it.
    pane_snapshot=$(_stmux capture-pane -t "$SESSION" -p -S -200 2>/dev/null || true)
    if echo "$pane_snapshot" | grep -qF "$PASTE_HEAD" \
        && echo "$pane_snapshot" | grep -qF "$PASTE_TAIL"; then
        PASTE_OK=1
        break
    fi
    sleep 0.2
done
PASTE_ELAPSED=$((SECONDS - PASTE_START_SECS))
if [ "$PASTE_OK" -eq 1 ]; then
    pass "200-char paste body visible in pane within ${PASTE_ELAPSED}s (head+tail both present)"
else
    fail "paste body did not appear within 10s — coalescer regression (typewriter behavior returned?)"
    capture_pane "$SESSION" | tail -40 >&2
    exit 1
fi

# --- Phase 2: clean SIGINT shutdown ---

echo ""
echo "=== Phase 2: SIGINT and assert clean shutdown ==="
# Send SIGINT directly to the sprawl process. The task spec is "exits
# cleanly when sent SIGINT" — we deliver the signal at the OS level,
# not via tmux keypress (which the input panel might intercept as
# "clear input" since the box is non-empty after Phase 1).
kill -INT "$SPRAWL_PID"

# Poll for sprawl PID to disappear (kill -0 fails).
SHUTDOWN_END=$((SECONDS + 10))
SHUTDOWN_OK=0
while [ "$SECONDS" -lt "$SHUTDOWN_END" ]; do
    if ! kill -0 "$SPRAWL_PID" 2>/dev/null; then
        SHUTDOWN_OK=1
        break
    fi
    sleep 0.5
done
if [ "$SHUTDOWN_OK" -eq 1 ]; then
    pass "sprawl PID $SPRAWL_PID exited within $((SECONDS - SHUTDOWN_END + 10))s of SIGINT"
else
    fail "sprawl PID $SPRAWL_PID did not exit within 10s of SIGINT — coalescer deadlocked Close path?"
    # Force-kill so cleanup doesn't hang the watchdog.
    kill -KILL "$SPRAWL_PID" 2>/dev/null || true
fi

# --- Summary ---

echo ""
echo "==============================="
echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "==============================="
if [ "$FAIL_COUNT" -gt 0 ]; then exit 1; fi
exit 0
