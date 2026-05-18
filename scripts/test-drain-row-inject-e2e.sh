#!/usr/bin/env bash
# test-drain-row-inject-e2e.sh — End-to-end gate for the drain-row
# prompt-inject path (QUM-569).
#
# Background: QUM-565 migrated test-notify-tui-e2e.sh off the
# deprecated `sprawl messages send` / `sprawl report` CLI and onto
# direct on-disk maildir+state writes. The on-disk writes faithfully
# exercise the TUI maildir-watcher (banner + badge), but BYPASS
# `internal/messages.Send()`'s defaultNotifier callback, which is what
# drives `supervisor.WakeForDelivery` → claude prompt-inject of a
# `<system-notification>` drain row → child calls `mcp__sprawl__messages_read`.
# That end-to-end drain-row inject path lost its shell-layer regression
# guard during the QUM-565 migration (it's still unit-tested in
# internal/runtime/unified_delivery_send_message_test.go and the
# internal/supervisor/*_test.go suites). This harness restores that
# coverage by driving a real claude child to call messages_send via the
# MCP tool — the only way to fire defaultNotifier end-to-end from
# outside the supervisor's address space.
#
# What this script does:
#   1. Spins up an isolated /tmp sandbox (plain git repo + .sprawl/
#      root-name), mirroring the safety guards in sprawl-test-env.sh.
#   2. Launches `sprawl enter` in a detached tmux session so the
#      bubbletea TUI has a pseudo-terminal to render into.
#   3. Waits for the TUI to render (tree panel shows 'weave (idle)').
#   4. Attaches a phantom tmux client (QUM-327 workaround) so
#      `send-keys` delivers into the bubbletea input buffer.
#   5. Drives weave to spawn an engineer-type child via
#      `mcp__sprawl__spawn` whose prompt instructs the child to
#      immediately call `mcp__sprawl__messages_send` to weave with a
#      unique DRAIN-PROBE-<token> sentinel body, then report_status
#      complete, then stop.
#   6. Polls .sprawl/agents/ for the first non-weave child state file
#      to discover the auto-allocated child name.
#   7. Sanity asserts the maildir banner `inbox: N new message` rises
#      (proves the maildir-watcher path still works post-QUM-565).
#   8. Primary assertion: weave's TUI pane renders the drain-row
#      citation `From <child> — mcp__sprawl__messages_read(id=` within
#      90s — proves Send → defaultNotifier → WakeForDelivery → claude
#      prompt-inject → viewport render is end-to-end live. We assert on
#      the stable `mcp__sprawl__messages_read(id=` substring, not on
#      the unicode glyph (✉ / ⚡), because glyph rendering depends on
#      async-vs-interrupt classification while the citation is invariant.
#   9. On failure: print pane tail + child state.json + weave state.json
#      for diagnostics, exit 1. On success: PASS, cleanup, exit 0.
#
# Auth recovery (QUM-411): when invoked from inside a Claude Code SDK
# Bash tool subprocess, the SDK strips CLAUDE_CODE_OAUTH_TOKEN from the
# child env. Walk up ancestors until we find a process whose environ
# still has the token. HARNESS-ONLY shim — production sprawl Go code
# must NOT replicate this.
#
# Gate: if `claude` is missing and SPRAWL_E2E_SKIP_NO_CLAUDE=1, skip.
# Otherwise fail fast — the TUI cannot initialize without claude.
#
# Usage: bash scripts/test-drain-row-inject-e2e.sh
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

# --- Strip agent-identity leakage from the harness ---
unset SPRAWL_AGENT_IDENTITY

# --- Dedicated tmux socket for sandbox isolation (QUM-325) ---
SPRAWL_TMUX_SOCKET="${SPRAWL_TMUX_SOCKET:-sprawl-drain-e2e-$$}"
export SPRAWL_TMUX_SOCKET
_stmux() { tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"; }

# --- Preflight ---

if ! command -v claude >/dev/null 2>&1; then
    if [ "${SPRAWL_E2E_SKIP_NO_CLAUDE:-}" = "1" ]; then
        echo "SKIP: claude binary not found on PATH (SPRAWL_E2E_SKIP_NO_CLAUDE=1 set)"
        exit 0
    fi
    echo "FATAL: claude binary not found on PATH — drain-row e2e requires a real claude" >&2
    echo "       Set SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip this test." >&2
    exit 1
fi

if ! command -v tmux >/dev/null 2>&1; then
    echo "FATAL: tmux binary not found on PATH" >&2
    exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
    echo "FATAL: jq binary not found on PATH (used to read child state.json)" >&2
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

SPRAWL_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-qum569-XXXXXX")
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

# Copy .env into the sandbox so scripts/run-claude (used as SPRAWL_CLAUDE
# below) can rehydrate auth env vars inside spawned subshells. The shim
# falls back to $SPRAWL_ROOT/.env first, so this is the right place.
if [ -f "$REPO_ROOT/.env" ]; then
    cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
fi

SESSION="sprawl-drain-e2e-$(head -c4 /dev/urandom | xxd -p)"
STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"

# Unique probe sentinel per run.
PROBE="DRAIN-PROBE-$$-$(date +%s)"
BRANCH_SUFFIX="$(head -c4 /dev/urandom | xxd -p)"

CHILD_STATE=""
CHILD_NAME=""

echo "  SPRAWL_BIN=$SPRAWL_BIN"
echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
echo "  SESSION=$SESSION"
echo "  PROBE=$PROBE"
echo ""

# --- Test infra ---

PASS_COUNT=0
FAIL_COUNT=0
pass() { PASS_COUNT=$((PASS_COUNT + 1)); echo "  PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); echo "  FAIL: $1" >&2; }

capture_pane() { _stmux capture-pane -t "$1" -p 2>/dev/null || true; }

# wait_for_pattern <session> <pattern> <timeout_secs> — regex match, 1s poll.
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

# wait_for_pattern_fast <session> <pattern> <timeout_secs> — regex, 0.2s poll.
wait_for_pattern_fast() {
    local session="$1" pattern="$2" timeout="$3"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        if capture_pane "$session" | grep -qE "$pattern"; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

# wait_for_substring_fast <session> <fixed-substring> <timeout_secs> — grep -F.
# Use this for the drain-row citation (em-dash is unicode, regex-fragile).
wait_for_substring_fast() {
    local session="$1" needle="$2" timeout="$3"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        if capture_pane "$session" | grep -qF "$needle"; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

PHANTOM_PID=""
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
        /tmp/*)
            local attempt
            for attempt in 1 2 3 4 5; do
                if rm -rf -- "$SPRAWL_ROOT" 2>/dev/null; then
                    break
                fi
                sleep 1
            done
            if [ -d "$SPRAWL_ROOT" ]; then
                echo "  WARN: cleanup could not fully remove $SPRAWL_ROOT (stragglers under .sprawl/agents/); watchdog will reap" >&2
            fi
            ;;
    esac
    exit "$rc"
}
trap cleanup EXIT INT TERM HUP

# QUM-458 layer 1: setsid'd watchdog reaps the sandbox if the driver dies via SIGKILL.
# shellcheck source=lib/sandbox-traps.sh
. "$(dirname "$0")/lib/sandbox-traps.sh"
sandbox_install_watchdog "$$" "$SPRAWL_TMUX_SOCKET" "$SPRAWL_ROOT"

# --- Launch the TUI ---

echo "=== Launching sprawl enter ==="
# Use scripts/run-claude as SPRAWL_CLAUDE so spawned child subshells can
# rehydrate CLAUDE_CODE_OAUTH_TOKEN from the copied .env (see CLAUDE.md
# "Running claude from agent bash subshells").
_stmux new-session -d -s "$SESSION" -x 200 -y 50 \
    "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_CLAUDE='$REPO_ROOT/scripts/run-claude' '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
_stmux set-option -t "$SESSION" window-size manual >/dev/null
_stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

if wait_for_pattern "$SESSION" "weave \\(idle\\)" 45; then
    pass "TUI rendered ('weave (idle)' visible in tree panel)"
else
    fail "TUI did not render 'weave (idle)' within 45s"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -30 >&2
    echo "  stderr log tail:" >&2
    [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
    _stmux send-keys -t "$SESSION" "1" Enter
    sleep 1
fi

sleep 3

# --- Attach phantom tmux client (QUM-327 workaround) ---

echo ""
echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
script -q -c "tmux ${SPRAWL_TMUX_SOCKET:+-L $SPRAWL_TMUX_SOCKET} attach -t '$SESSION' -d" /dev/null >/dev/null 2>&1 &
PHANTOM_PID=$!
sleep 1

# --- Drive weave to spawn a child that immediately sends a message ---
#
# The child's prompt is enumerated step-by-step and embedded verbatim in
# weave's spawn-call prompt arg. Single-line shape so the TUI's
# paste-classifier (QUM-432) doesn't reclassify embedded \n as a submit.

echo ""
echo "=== Driving weave to spawn the drain-probe child ==="

SPAWN_PROMPT="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum-569-drain-probe-${BRANCH_SUFFIX}', and prompt set to exactly: 'You are an automated QUM-569 probe. STEP 1: IMMEDIATELY call mcp__sprawl__messages_send with to=\"weave\", body=\"DRAIN-PROBE-SENTINEL: ${PROBE}\". STEP 2: call mcp__sprawl__report_status with state=complete, summary=\"drain probe sent\". STEP 3: Stop. Do nothing else. Do not read any files, do not run any commands.'"

# QUM-432: send the prompt text, pause, then send Enter so the TUI's
# paste classifier doesn't reclassify Enter as an embedded newline.
_stmux send-keys -t "$SESSION" "$SPAWN_PROMPT"
sleep 0.5
_stmux send-keys -t "$SESSION" Enter

# --- Wait for the spawn to land (any non-weave state file appears) ---

echo ""
echo "=== Waiting for spawn to land (poll $SPRAWL_ROOT/.sprawl/agents/ for a new *.json) ==="
ELAPSED=0
SPAWN_LANDED=0
while [ "$ELAPSED" -lt 180 ]; do
    while IFS= read -r candidate; do
        [ -z "$candidate" ] && continue
        # Skip the synthesized weave state — match any non-weave agent.
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
if [ "$SPAWN_LANDED" -eq 1 ]; then
    pass "child spawned (name=$CHILD_NAME, state=$CHILD_STATE)"
else
    fail "no non-weave state file appeared within 180s — weave's claude did not call spawn"
    echo "  agents dir:" >&2
    ls -la "$SPRAWL_ROOT/.sprawl/agents/" >&2 2>/dev/null || true
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

# --- Sanity: maildir banner rises (post-QUM-565 watcher still works) ---

echo ""
echo "=== Waiting for inbox banner (sanity check, maildir watcher) ==="
if wait_for_pattern_fast "$SESSION" "inbox: [0-9]+ new message" 60; then
    pass "inbox banner appeared in weave's viewport (maildir watcher still alive)"
else
    fail "inbox banner never appeared within 60s — child may not have called messages_send"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
    echo "  child state:" >&2
    cat "$CHILD_STATE" 2>/dev/null | sed 's/^/    /' >&2 || echo "    <missing>" >&2
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

# --- Primary assertion: drain-row prompt-inject lands in weave's pane ---
#
# Format from internal/inboxprompt/inboxprompt.go BuildQueueFlushPrompt:
#   <system-notification type="message">From $FROM — mcp__sprawl__messages_read(id=$ID)</system-notification>
# The TUI strips the wrapper tag before rendering; the body
# `From <child> — mcp__sprawl__messages_read(id=...)` is what surfaces.
# We assert on the stable fixed substring (em-dash is U+2014, regex-fragile).
# Max-over-window sampling: track first sighting plus continue scanning
# in case scroll has pushed the row past the visible window.

echo ""
echo "=== Primary assertion: drain-row prompt-inject from $CHILD_NAME ==="
DRAIN_NEEDLE="From ${CHILD_NAME} — mcp__sprawl__messages_read(id="
DRAIN_SEEN=0
DRAIN_END=$((SECONDS + 90))
while [ "$SECONDS" -lt "$DRAIN_END" ]; do
    if capture_pane "$SESSION" | grep -qF "$DRAIN_NEEDLE"; then
        DRAIN_SEEN=1
        break
    fi
    sleep 0.2
done

if [ "$DRAIN_SEEN" -eq 1 ]; then
    pass "drain-row citation '$DRAIN_NEEDLE...' appeared in weave's pane (QUM-555/QUM-323 path live)"
else
    fail "drain-row citation '$DRAIN_NEEDLE...' did NOT appear in weave's pane within 90s"
    echo "  Send → defaultNotifier → WakeForDelivery → claude prompt-inject path is broken" >&2
    echo "  pane tail (80 lines):" >&2
    capture_pane "$SESSION" | tail -80 >&2
    echo "  child state:" >&2
    cat "$CHILD_STATE" 2>/dev/null | sed 's/^/    /' >&2 || echo "    <missing>" >&2
    echo "  weave state:" >&2
    cat "$SPRAWL_ROOT/.sprawl/agents/weave.json" 2>/dev/null | sed 's/^/    /' >&2 || echo "    <missing>" >&2
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
