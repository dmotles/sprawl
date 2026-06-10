#!/usr/bin/env bash
# test-wake-live-e2e.sh — End-to-end gate for `mcp__sprawl__wake`
# subprocess survival (QUM-606).
#
# Background: QUM-601 introduced `mcp__sprawl__wake` to rebuild a
# faulted backend session in-place. QUM-606 found that the original
# implementation forwarded the MCP request ctx all the way down to
# `exec.CommandContext`, so the freshly-spawned claude subprocess was
# SIGKILL'd the instant `toolRecover` returned. The tool said "success",
# but the agent was a zombie — no live subprocess, no working subscriber
# pipe, no way to drive turns.
#
# This harness drives the live end-to-end recovery path with a real
# claude binary in an isolated /tmp sandbox:
#
#   Phase 1: spawn an engineer-type child (named via auto-allocator),
#            capture its original claude --resume PID.
#   Phase 2: induce a terminal SubscriberWedge fault on the child via
#            the build-tag-gated `mcp__sprawl___test_induce_wedge` MCP
#            tool. Wait for the TUI fault banner.
#   Phase 3: drive weave to call `mcp__sprawl__wake` on the child,
#            assert the success ack lands in the pane.
#   Phase 4 (PRIMARY): a NEW `claude … --resume …` subprocess for the
#            child is alive 2s after recover returned, AND its PID
#            differs from the pre-recover PID. This is the assertion
#            QUM-606 introduced — pre-fix, no new subprocess survived.
#   Phase 5: drive a post-recover turn (send_message with sentinel) and
#            assert a frame containing the sentinel lands in the child's
#            activity.ndjson within 60s — proves the recovered session
#            is actually functional, not just alive-but-dead.
#
# Build requirement: this harness requires the `sprawl` binary to be
# built with `-tags sprawl_test` so `mcp__sprawl___test_induce_wedge` is
# present in the MCP tool surface. The `make test-wake-live-e2e`
# target handles this. Invoking the script directly without the tag
# build will fail in Phase 2.
#
# Auth recovery (QUM-411): when invoked from inside a Claude Code SDK
# Bash tool subprocess, the SDK strips CLAUDE_CODE_OAUTH_TOKEN. Walk
# ancestors until we find one whose environ still has it.
#
# Gate: if `claude` is missing and SPRAWL_E2E_SKIP_NO_CLAUDE=1, skip.
#
# Usage: bash scripts/test-wake-live-e2e.sh
#
# NOTE: creates a real tmux session and at least two real claude
# subprocesses (weave + child). Do not run in parallel with other
# TUI-mode e2e scripts.

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

SPRAWL_TMUX_SOCKET="${SPRAWL_TMUX_SOCKET:-sprawl-wake-e2e-$$}"
export SPRAWL_TMUX_SOCKET
_stmux() { tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"; }

if ! command -v claude >/dev/null 2>&1; then
    if [ "${SPRAWL_E2E_SKIP_NO_CLAUDE:-}" = "1" ]; then
        echo "SKIP: claude binary not found on PATH (SPRAWL_E2E_SKIP_NO_CLAUDE=1 set)"
        exit 0
    fi
    echo "FATAL: claude binary not found on PATH — QUM-606 e2e requires a real claude" >&2
    echo "       Set SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip this test." >&2
    exit 1
fi
if ! command -v tmux >/dev/null 2>&1; then echo "FATAL: tmux not on PATH" >&2; exit 1; fi
if ! command -v jq >/dev/null 2>&1; then echo "FATAL: jq not on PATH" >&2; exit 1; fi
if ! command -v pgrep >/dev/null 2>&1; then echo "FATAL: pgrep not on PATH" >&2; exit 1; fi

# Allow the Makefile target to set SPRAWL_BIN to a pre-built sprawl_test
# binary; otherwise build with the tag here.
if [ -z "${SPRAWL_BIN:-}" ]; then
    echo "=== Building sprawl with -tags sprawl_test ==="
    (cd "$REPO_ROOT" && go build -tags sprawl_test -o sprawl-wake-e2e ./)
    SPRAWL_BIN="$REPO_ROOT/sprawl-wake-e2e"
fi
if [ ! -x "$SPRAWL_BIN" ]; then
    echo "FATAL: sprawl binary not found at $SPRAWL_BIN" >&2
    exit 1
fi

# --- Sandbox under /tmp/ ---

SPRAWL_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-qum724-XXXXXX")
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

# Copy .env so scripts/run-claude can rehydrate auth in spawned subshells.
if [ -f "$REPO_ROOT/.env" ]; then
    cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
fi

# Enable the test-tools env var too so the inject tool's no-op SPRAWL
# version (if mis-built) would still surface a recognizable error rather
# than "unknown tool".
export SPRAWL_ENABLE_TEST_TOOLS=1

SESSION="sprawl-wake-e2e-$(head -c4 /dev/urandom | xxd -p)"
STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"
PROBE="WAKE-PROBE-$$-$(date +%s)"
BRANCH_SUFFIX="$(head -c4 /dev/urandom | xxd -p)"

CHILD_STATE=""
CHILD_NAME=""

echo "  SPRAWL_BIN=$SPRAWL_BIN"
echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
echo "  SESSION=$SESSION"
echo "  PROBE=$PROBE"
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

# Find the PID of a claude subprocess whose argv contains "--resume <sid>".
# Returns the PID on stdout or empty when none matches.
find_resume_pid() {
    local session_id="$1"
    pgrep -af 'claude' | awk -v sid="$session_id" '$0 ~ "--resume" && $0 ~ sid {print $1; exit}'
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
_stmux new-session -d -s "$SESSION" -x 200 -y 50 \
    "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_CLAUDE='$REPO_ROOT/scripts/run-claude' SPRAWL_ENABLE_TEST_TOOLS=1 '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
_stmux set-option -t "$SESSION" window-size manual >/dev/null
_stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

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
sleep 3

echo ""
echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
script -q -c "tmux ${SPRAWL_TMUX_SOCKET:+-L $SPRAWL_TMUX_SOCKET} attach -t '$SESSION' -d" /dev/null >/dev/null 2>&1 &
PHANTOM_PID=$!
sleep 1

# --- Phase 1: spawn the child ---

echo ""
echo "=== Phase 1: spawn an engineer child that idles ==="
SPAWN_PROMPT="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum-606-recover-probe-${BRANCH_SUFFIX}', and prompt set to exactly: 'You are an automated QUM-606 probe. Call mcp__sprawl__report_status with state=working, summary=\"idle, awaiting fault induction\". Then stop and wait. Do nothing else until you receive a message.'"
_stmux send-keys -t "$SESSION" "$SPAWN_PROMPT"
sleep 0.5
_stmux send-keys -t "$SESSION" Enter

ELAPSED=0
SPAWN_LANDED=0
while [ "$ELAPSED" -lt 180 ]; do
    while IFS= read -r candidate; do
        [ -z "$candidate" ] && continue
        local_name=$(jq -r '.name // empty' "$candidate" 2>/dev/null || true)
        if [ -n "$local_name" ] && [ "$local_name" != "weave" ]; then
            CHILD_STATE="$candidate"
            CHILD_NAME="$local_name"
            SPAWN_LANDED=1
            break
        fi
    done < <(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null)
    [ "$SPAWN_LANDED" -eq 1 ] && break
    sleep 2
    ELAPSED=$((ELAPSED + 2))
done
if [ "$SPAWN_LANDED" -ne 1 ]; then
    fail "no child state appeared within 180s"
    capture_pane "$SESSION" | tail -40 >&2
    exit 1
fi
pass "child spawned (name=$CHILD_NAME)"

# Wait for the child's session_id to materialize (the spawn writes the
# state, but the session_id field is filled after the first protocol
# init). Up to 60s of polling.
ORIG_SID=""
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
    ORIG_SID=$(jq -r '.session_id // empty' "$CHILD_STATE" 2>/dev/null || true)
    [ -n "$ORIG_SID" ] && break
    sleep 2
done
if [ -z "$ORIG_SID" ]; then
    fail "child session_id never materialized; cannot run recover probe"
    cat "$CHILD_STATE" >&2 2>/dev/null || true
    exit 1
fi
pass "child session_id=$ORIG_SID"

# Find the original claude --resume PID for the child. Note: on the
# initial spawn the args carry --session-id <sid>, not --resume; we
# fall back to a search over `claude.*<sid>` so either form matches.
ORIG_PID=""
for _ in 1 2 3 4 5; do
    ORIG_PID=$(pgrep -af 'claude' | awk -v sid="$ORIG_SID" '$0 ~ sid {print $1; exit}' || true)
    [ -n "$ORIG_PID" ] && break
    sleep 2
done
if [ -z "$ORIG_PID" ]; then
    fail "could not locate original claude subprocess matching session_id=$ORIG_SID"
    pgrep -af claude >&2 || true
    exit 1
fi
pass "original child claude PID=$ORIG_PID"

# --- Phase 2: induce wedge via the build-tag-gated test tool ---

echo ""
echo "=== Phase 2: induce SubscriberWedge fault on $CHILD_NAME ==="
INDUCE_PROMPT="Call mcp__sprawl___test_induce_wedge with agent_name='$CHILD_NAME', fault_class='subscriber_wedged'. Confirm in your reply that the call succeeded."
_stmux send-keys -t "$SESSION" "$INDUCE_PROMPT"
sleep 0.5
_stmux send-keys -t "$SESSION" Enter

if wait_for_pattern_fast "$SESSION" "Induced subscriber_wedged|SubscriberWedge|fault" 60; then
    pass "fault induction tool returned"
else
    fail "fault induction tool did not surface within 60s"
    capture_pane "$SESSION" | tail -40 >&2
    exit 1
fi

# --- Phase 3: drive recover ---

echo ""
echo "=== Phase 3: drive mcp__sprawl__wake on $CHILD_NAME ==="
RECOVER_PROMPT="Call mcp__sprawl__wake with agent_name='$CHILD_NAME'. Quote the exact tool response back to me."
_stmux send-keys -t "$SESSION" "$RECOVER_PROMPT"
sleep 0.5
_stmux send-keys -t "$SESSION" Enter

if wait_for_pattern_fast "$SESSION" "Woke agent $CHILD_NAME" 60; then
    pass "mcp__sprawl__wake returned success ack"
else
    fail "recover success ack did not appear within 60s"
    capture_pane "$SESSION" | tail -60 >&2
    exit 1
fi

# --- Phase 4 (PRIMARY): new live claude --resume subprocess exists ---

echo ""
echo "=== Phase 4 (PRIMARY): new claude --resume subprocess survives ==="
# Recover swaps to a new session with --resume <original-sid>. The new
# PID must differ from the original AND must still be alive 2s after
# the recover return. Pre-QUM-606-fix, no such PID existed.
sleep 2
NEW_PID=""
PROBE_END=$((SECONDS + 10))
while [ "$SECONDS" -lt "$PROBE_END" ]; do
    NEW_PID=$(pgrep -af 'claude' | awk -v sid="$ORIG_SID" -v origpid="$ORIG_PID" '$0 ~ "--resume" && $0 ~ sid && $1 != origpid {print $1; exit}' || true)
    [ -n "$NEW_PID" ] && break
    sleep 0.5
done

if [ -z "$NEW_PID" ]; then
    fail "PRIMARY: no live claude --resume subprocess found for sid=$ORIG_SID 2s after recover (QUM-606 zombie regression)"
    echo "  pgrep claude tail:" >&2
    pgrep -af claude | head -20 >&2 || true
    capture_pane "$SESSION" | tail -60 >&2
    exit 1
fi
if [ "$NEW_PID" = "$ORIG_PID" ]; then
    fail "PRIMARY: new claude PID ($NEW_PID) equals original ($ORIG_PID) — recover did not actually swap the subprocess"
    exit 1
fi
# Confirm alive via signal-0 probe.
if ! kill -0 "$NEW_PID" 2>/dev/null; then
    fail "PRIMARY: new claude PID $NEW_PID does not respond to signal 0 — subprocess died immediately"
    exit 1
fi
pass "new claude --resume PID=$NEW_PID alive (was $ORIG_PID)"

# --- Phase 5: drive a post-recover turn ---

echo ""
echo "=== Phase 5: drive a post-recover turn and assert frames ==="
TURN_PROMPT="Call mcp__sprawl__send_message with to='$CHILD_NAME', body='Echo ${PROBE} verbatim in your next reply and then call report_status complete.', interrupt=false."
_stmux send-keys -t "$SESSION" "$TURN_PROMPT"
sleep 0.5
_stmux send-keys -t "$SESSION" Enter

ACTIVITY="$SPRAWL_ROOT/.sprawl/agents/$CHILD_NAME/activity.ndjson"
ACT_END=$((SECONDS + 60))
ACT_SEEN=0
while [ "$SECONDS" -lt "$ACT_END" ]; do
    if [ -f "$ACTIVITY" ] && grep -qF "$PROBE" "$ACTIVITY"; then
        ACT_SEEN=1
        break
    fi
    sleep 1
done
if [ "$ACT_SEEN" -eq 1 ]; then
    pass "post-recover turn produced frame with sentinel '$PROBE' in $CHILD_NAME/activity.ndjson"
else
    fail "post-recover turn did NOT surface sentinel '$PROBE' in activity within 60s"
    echo "  activity tail:" >&2
    [ -f "$ACTIVITY" ] && tail -20 "$ACTIVITY" >&2 || echo "    <activity file missing>" >&2
    capture_pane "$SESSION" | tail -60 >&2
fi

# --- Summary ---

echo ""
echo "==============================="
echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "==============================="
if [ "$FAIL_COUNT" -gt 0 ]; then exit 1; fi
exit 0
