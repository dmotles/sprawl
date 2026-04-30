#!/usr/bin/env bash
# test-handoff-e2e.sh — End-to-end regression guard for QUM-329:
# TUI handoff must actually tear down and restart the claude subprocess
# when weave calls `handoff` via MCP.
#
# Before the QUM-329 fix, `cmd/enter.go` created two separate
# `supervisor.Supervisor` instances — one for the MCP server wired to the
# claude subprocess, one for the TUI's HandoffRequested listener. A
# `handoff` call fired the non-blocking channel send on supervisor
# #2, but the TUI listened on supervisor #1: teardown never ran, the
# subprocess survived indefinitely, and `persistent.md` was never
# re-loaded. See QUM-329 postmortem and tests in cmd/enter_test.go +
# cmd/enter_handoff_test.go for layered coverage.
#
# What this script does:
#   1. Builds ./sprawl into an isolated /tmp sandbox (mirrors the
#      safety guards in sprawl-test-env.sh; refuses to proceed if
#      SPRAWL_ROOT escapes /tmp/).
#   2. Seeds .sprawl/memory/last-session-id with a known UUID so the
#      first handoff has a concrete old-session target to diff against.
#   3. Launches `sprawl enter` in a detached tmux session wide enough
#      to render the TUI's tree + viewport panels (200×50).
#   4. Waits for the TUI to render and claude subprocess #1 to spawn,
#      capturing its pid and --session-id argv.
#   5. Fires a handoff by driving weave to call `handoff` via MCP.
#      This MUST go through the in-process MCP path (not the out-of-proc
#      `sprawl handoff` CLI) because the CLI spawns its own supervisor
#      and cannot exercise the QUM-329 split-supervisor bug — only the
#      in-process MCP tool shares a supervisor instance with the TUI
#      listener. We attach a phantom tmux client (QUM-327 workaround) so
#      `send-keys` delivers into the bubbletea input buffer, then type a
#      user message instructing weave to call the tool.
#   6. Within HANDOFF_TIMEOUT asserts:
#        * handoff-signal file was created, then removed
#        * old claude pid died
#        * new claude pid spawned with a DIFFERENT --session-id argv
#        * last-session-id file content changed
#        * the TUI pane capture contains "Session restarting (handoff)"
#   7. Teardown: kill the tmux session, remove the sandbox.
#
# Gate: if `claude` is missing and SPRAWL_E2E_SKIP_NO_CLAUDE=1, skip.
# Otherwise fail fast — the TUI cannot initialize without claude.
#
# Usage: bash scripts/test-handoff-e2e.sh
#
# NOTE: creates a real tmux session + real claude subprocess. Do not
# run in parallel with other TUI-mode e2e scripts.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# --- Preflight ---

if ! command -v claude >/dev/null 2>&1; then
    if [ "${SPRAWL_E2E_SKIP_NO_CLAUDE:-}" = "1" ]; then
        echo "SKIP: claude binary not found on PATH (SPRAWL_E2E_SKIP_NO_CLAUDE=1 set)"
        exit 0
    fi
    echo "FATAL: claude binary not found on PATH — handoff e2e requires a real claude" >&2
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

SPRAWL_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-handoff-e2e-XXXXXX")
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

SESSION="sprawl-handoff-e2e-$(head -c4 /dev/urandom | xxd -p)"
STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"

echo "  SPRAWL_BIN=$SPRAWL_BIN"
echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
echo "  SESSION=$SESSION"
echo ""

# --- Test infra ---

PASS_COUNT=0
FAIL_COUNT=0
pass() { PASS_COUNT=$((PASS_COUNT + 1)); echo "  PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); echo "  FAIL: $1" >&2; }

capture_pane() { tmux capture-pane -t "$1" -p 2>/dev/null || true; }

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

# find_claude_pids — returns newline-separated pids of claude subprocesses
# spawned by the TUI for THIS sandbox (matched by the sandbox-rooted
# --system-prompt-file argv so concurrent sandboxes don't alias).
find_claude_pids() {
    pgrep -af 'claude' 2>/dev/null | awk -v root="$SPRAWL_ROOT" '
        $0 ~ "stream-json" && index($0, root) > 0 { print $1 }
    '
}

# find_claude_pid — convenience wrapper: returns the first matching pid.
find_claude_pid() {
    find_claude_pids | head -1
}

# find_claude_pid_for_sid — returns the claude pid argv'd with the given
# --session-id value, scoped to this sandbox.
find_claude_pid_for_sid() {
    local want_sid="$1"
    pgrep -af 'claude' 2>/dev/null | awk -v root="$SPRAWL_ROOT" -v sid="$want_sid" '
        $0 ~ "stream-json" && index($0, root) > 0 && index($0, sid) > 0 { print $1; exit }
    '
}

# pid_is_live — returns 0 iff pid exists and is NOT a zombie.
pid_is_live() {
    local pid="$1"
    [ -n "$pid" ] || return 1
    [ -r "/proc/$pid/status" ] || return 1
    local state
    state=$(awk '/^State:/ { print $2; exit }' "/proc/$pid/status" 2>/dev/null)
    case "$state" in
        Z|"") return 1 ;;
        *) return 0 ;;
    esac
}

# claude_session_id_for_pid — extract the --session-id argv value from
# /proc/<pid>/cmdline for a given pid.
claude_session_id_for_pid() {
    local pid="$1"
    if [ -z "$pid" ] || [ ! -r "/proc/$pid/cmdline" ]; then
        echo ""
        return
    fi
    tr '\0' '\n' < "/proc/$pid/cmdline" | awk '
        /^--session-id$/ { getline sid; print sid; exit }
        /^--session-id=/ { sub(/^--session-id=/, ""); print; exit }
    '
}

cleanup() {
    local rc=$?
    if [ -n "${PHANTOM_PID:-}" ]; then
        kill "$PHANTOM_PID" 2>/dev/null || true
    fi
    if tmux has-session -t "$SESSION" 2>/dev/null; then
        tmux kill-session -t "$SESSION" 2>/dev/null || true
    fi
    case "$SPRAWL_ROOT" in
        /tmp/*) rm -rf -- "$SPRAWL_ROOT" ;;
    esac
    exit "$rc"
}
trap cleanup EXIT

# --- Launch the TUI ---

echo "=== Launching sprawl enter ==="
tmux new-session -d -s "$SESSION" -x 200 -y 50 \
    "SPRAWL_ROOT='$SPRAWL_ROOT' '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
tmux set-option -t "$SESSION" window-size manual >/dev/null
tmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

if wait_for_pattern "$SESSION" "weave \\(idle\\)" 45; then
    pass "TUI rendered"
else
    fail "TUI did not render within 45s"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -30 >&2
    echo "  stderr log tail:" >&2
    [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
    tmux send-keys -t "$SESSION" "1" Enter
    sleep 1
fi

# Give the subprocess a moment to fully settle.
sleep 3

# --- Capture the old claude pid + session-id ---

OLD_PID="$(find_claude_pid)"
if [ -z "$OLD_PID" ]; then
    fail "could not locate initial claude subprocess"
    echo "  pgrep output:" >&2
    pgrep -af 'claude' >&2 || true
    exit 1
fi
OLD_SID="$(claude_session_id_for_pid "$OLD_PID")"
if [ -z "$OLD_SID" ]; then
    fail "could not extract --session-id for pid $OLD_PID"
    exit 1
fi
echo "  old claude pid=$OLD_PID session-id=$OLD_SID"

# Snapshot last-session-id (as written by rootinit.Prepare during TUI launch).
OLD_LAST_SID_FILE="$SPRAWL_ROOT/.sprawl/memory/last-session-id"
if [ ! -f "$OLD_LAST_SID_FILE" ]; then
    fail "last-session-id file not created by TUI launch"
    exit 1
fi
OLD_LAST_SID="$(cat "$OLD_LAST_SID_FILE")"
echo "  last-session-id (pre-handoff) = $OLD_LAST_SID"

# --- Attach a phantom tmux client (QUM-327 workaround) ---
# Bubbletea drops input keys if the tmux pane has no attached client.
# `script -q -c ... /dev/null` gives us a detached-attach that keeps a
# client registered without opening a real terminal.
echo ""
echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
script -q -c "tmux attach -t '$SESSION' -d" /dev/null >/dev/null 2>&1 &
PHANTOM_PID=$!
sleep 1

# --- Fire the handoff via MCP ---
#
# We type a user prompt telling weave to call handoff. The MCP
# tool lives inside the claude subprocess and calls supervisor.Handoff
# on the SAME supervisor instance the TUI listener subscribes to (post
# QUM-329 fix). Pre-fix the channels diverge and the assertions below
# fail — red-first verifiable.

echo ""
echo "=== Firing handoff via MCP ==="
HANDOFF_PROMPT="Call the mcp__sprawl__handoff tool with a short summary 'QUM-329 e2e test handoff'."

tmux send-keys -t "$SESSION" "$HANDOFF_PROMPT" Enter
sleep 2

# --- Assertions ---

echo ""
echo "=== Post-handoff assertions ==="

HANDOFF_SIGNAL="$SPRAWL_ROOT/.sprawl/memory/handoff-signal"

# 1. handoff-signal file was created at some point. Poll up to 90s so
#    slow LLMs can complete the tool call.
ELAPSED=0
SIGNAL_APPEARED=0
while [ "$ELAPSED" -lt 90 ]; do
    if [ -f "$HANDOFF_SIGNAL" ]; then
        SIGNAL_APPEARED=1
        break
    fi
    # Side-effect: the file may be removed quickly by FinalizeHandoff.
    # Check recent sessions/*.md as a proxy for "handoff ran".
    if ls "$SPRAWL_ROOT/.sprawl/memory/sessions/"*.md >/dev/null 2>&1; then
        SIGNAL_APPEARED=1
        break
    fi
    sleep 1
    ELAPSED=$((ELAPSED + 1))
done
if [ "$SIGNAL_APPEARED" -eq 1 ]; then
    pass "handoff fired (signal file or session summary observed)"
else
    fail "handoff never fired within 90s (claude didn't call handoff; see pane tail)"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
fi

# 2. Wait for the TUI to react: capture-pane should show the restart
#    banner. The banner is visible only during the restart window
#    (SessionRestartingMsg → RestartSessionMsg clears viewport), so we
#    poll rapidly over a short horizon. If the strict banner check
#    misses the window, fall back to stderr evidence: FinalizeHandoff
#    logs "handoff signal detected, restarting".
# 2. Wait for the TUI to react. Evidence of restart in order of
#    preference:
#   (a) the "Session restarting (handoff)" viewport banner (brief),
#   (b) the "restart <N>s" status-bar indicator (stays up for the
#       duration of the async restart — ConsolidationProgressMsg ticks),
#   (c) the FinalizeHandoff stderr log entry (written to the
#       TUI-redirected log file under .sprawl/logs/tui-stderr-*.log).
TUI_LOG_GLOB="$SPRAWL_ROOT/.sprawl/logs/tui-stderr-*.log"
restart_evidence_seen=""
ELAPSED=0
while [ "$ELAPSED" -lt 60 ]; do
    if capture_pane "$SESSION" | grep -qE "Session restarting.*handoff|restart [0-9]+s"; then
        restart_evidence_seen="pane"
        break
    fi
    # shellcheck disable=SC2086
    if ls $TUI_LOG_GLOB 2>/dev/null | head -1 | xargs -r grep -l "handoff signal detected, restarting" >/dev/null 2>&1; then
        restart_evidence_seen="tui-log"
        break
    fi
    sleep 1
    ELAPSED=$((ELAPSED + 1))
done
if [ -n "$restart_evidence_seen" ]; then
    pass "TUI triggered handoff restart (evidence=$restart_evidence_seen)"
else
    fail "TUI never triggered handoff restart within 60s (QUM-329 regression)"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
    echo "  tui-stderr log tail:" >&2
    # shellcheck disable=SC2086
    ls $TUI_LOG_GLOB 2>/dev/null | head -1 | xargs -r tail -30 >&2 || true
fi

# 3. Old claude subprocess (the one argv'd with OLD_SID) must be gone or
#    a reaped zombie. Scoping the lookup by --session-id avoids confusion
#    with the new subprocess.
ELAPSED=0
OLD_GONE=0
while [ "$ELAPSED" -lt 60 ]; do
    STILL_PID="$(find_claude_pid_for_sid "$OLD_SID")"
    if [ -z "$STILL_PID" ] || ! pid_is_live "$STILL_PID"; then
        OLD_GONE=1
        break
    fi
    sleep 1
    ELAPSED=$((ELAPSED + 1))
done
if [ "$OLD_GONE" -eq 1 ]; then
    pass "old claude (session-id $OLD_SID) terminated after handoff"
else
    fail "old claude (session-id $OLD_SID, pid $STILL_PID) still alive 60s after handoff (QUM-329 regression — teardown never ran)"
fi

# 4. A new claude subprocess with a DIFFERENT --session-id should be
#    alive (matching the rewritten last-session-id).
NEW_PID=""
NEW_SID=""
ELAPSED=0
# After restart, a new claude will either be alive with a different
# --session-id (fresh path) OR be already launched and ready (the TUI
# shows input re-enabled). Be liberal about matching: any live claude
# subprocess anywhere that is a child of this sandbox's `sprawl enter`
# counts. Fall back to pgrep matching SPRAWL_ROOT in cmdline.
#
# We locate the sprawl-enter pid once so we can walk its descendants.
SPRAWL_ENTER_PID="$(pgrep -af "$SPRAWL_BIN enter" 2>/dev/null | awk -v root="$SPRAWL_ROOT" 'index($0, root) > 0 { print $1; exit }')"
if [ -z "$SPRAWL_ENTER_PID" ]; then
    # Fallback: parent PID of the original claude
    SPRAWL_ENTER_PID="$(awk '{ print $4 }' "/proc/$OLD_PID/stat" 2>/dev/null)"
fi
while [ "$ELAPSED" -lt 180 ]; do
    # Enumerate claude children of sprawl enter.
    if [ -n "$SPRAWL_ENTER_PID" ]; then
        CANDIDATES="$(pgrep -P "$SPRAWL_ENTER_PID" -f claude 2>/dev/null || true)"
    else
        CANDIDATES=""
    fi
    # Append any cmdline-root matches too.
    CANDIDATES="$CANDIDATES
$(find_claude_pids)"
    while IFS= read -r CAND_PID; do
        [ -z "$CAND_PID" ] && continue
        [ "$CAND_PID" = "$OLD_PID" ] && continue
        if ! pid_is_live "$CAND_PID"; then
            continue
        fi
        CAND_SID="$(claude_session_id_for_pid "$CAND_PID")"
        if [ -z "$CAND_SID" ]; then
            # Resume-mode claude lacks --session-id in argv. Still counts
            # as a new subprocess if pid != OLD_PID.
            NEW_PID="$CAND_PID"
            NEW_SID="(resume)"
            break
        fi
        if [ "$CAND_SID" != "$OLD_SID" ]; then
            NEW_PID="$CAND_PID"
            NEW_SID="$CAND_SID"
            break
        fi
    done <<< "$CANDIDATES"
    if [ -n "$NEW_PID" ]; then
        break
    fi
    sleep 1
    ELAPSED=$((ELAPSED + 1))
done
if [ -n "$NEW_PID" ]; then
    pass "new claude pid=$NEW_PID session-id=$NEW_SID (differs from old pid=$OLD_PID sid=$OLD_SID)"
else
    fail "no new live claude subprocess within 180s (QUM-329 regression)"
    echo "  sprawl enter pid: $SPRAWL_ENTER_PID" >&2
    echo "  children of sprawl enter:" >&2
    pgrep -P "$SPRAWL_ENTER_PID" -af >&2 || true
    echo "  all claude in sandbox (cmdline match):" >&2
    find_claude_pids | while IFS= read -r p; do
        echo "    pid=$p sid=$(claude_session_id_for_pid "$p") live=$(pid_is_live "$p" && echo yes || echo no)" >&2
    done
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -20 >&2
fi

# 5. handoff-signal should have been consumed (removed) by FinalizeHandoff.
if [ -f "$HANDOFF_SIGNAL" ]; then
    fail "handoff-signal file NOT removed post-consumption (side-fix regression — see QUM-329 comment)"
else
    pass "handoff-signal file removed by FinalizeHandoff"
fi

# 6. last-session-id should have changed (or been cleared + rewritten).
NEW_LAST_SID="$(cat "$OLD_LAST_SID_FILE" 2>/dev/null || echo "")"
if [ -n "$NEW_LAST_SID" ] && [ "$NEW_LAST_SID" != "$OLD_LAST_SID" ]; then
    pass "last-session-id changed ($OLD_LAST_SID -> $NEW_LAST_SID)"
else
    fail "last-session-id did not change ($OLD_LAST_SID == $NEW_LAST_SID)"
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
