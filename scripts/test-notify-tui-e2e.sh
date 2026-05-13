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
#      (a) the banner 'inbox: N new message[s]' (QUM-473 §3 unified format), and
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

# --- Recover CLAUDE_CODE_OAUTH_TOKEN from an ancestor env (QUM-411) ---
# When invoked from inside a Claude Code SDK Bash tool subprocess, the
# SDK strips CLAUDE_CODE_OAUTH_TOKEN from the child env (security +
# recursion-prevention). Without it the spawned `claude` subprocess hits
# "Not logged in" and the TUI never produces real assertions. Walk up
# ancestors until we find a process whose environ still has the token.
# HARNESS-ONLY shim — production sprawl Go code must NOT replicate this.
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

# Count occurrences of an inbox-banner pattern in the current pane capture.
# QUM-473 §3 unified the banner format across both emit sites:
#   - "inbox: N new message[s]"             (from AgentTreeMsg rise-detector, no source)
#   - "inbox: N new message[s] from <sender>" (from InboxArrivalMsg notifier, source known)
# QUM-465: a single send_async to weave must produce exactly one of these,
# not both. Either flavor counts as a banner; total must be 1 per send.
count_inbox_banners() {
    local session="$1"
    capture_pane "$session" \
        | grep -cE "inbox: [0-9]+ new message" \
        || true
}

# QUM-555/QUM-556/QUM-557: count drain notification rows surfaced in weave's
# viewport. Post-QUM-555 the queue-flush prompt is a one-line
# `<system-notification>` per entry. Post-QUM-556 the body cites the canonical
# MCP tool name `mcp__sprawl__messages_read(id=<id>)` rather than the ambiguous
# bare verb "Read <id>". Post-QUM-557 the TUI strips the literal
# `<system-notification>` tags before rendering and surfaces the line with a
# left-bar accent + glyph (`✉` async, `⚡` interrupt). The pane capture sees the
# stripped/rendered form, not the raw tag string. Match the rendered shape;
# we anchor on the glyph + sender + tool-call segment (no closing tag exists
# in the rendered output).
count_drain_notifications() {
    local session="$1"
    local sender="$2"
    capture_pane "$session" \
        | grep -cE "(✉|⚡) (\\[interrupt\\] )?From $sender — mcp__sprawl__messages_read\\(id=[^)]+\\)" \
        || true
}

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

# wait_for_pattern_fast <session> <pattern> <timeout_secs>
# QUM-555: same as wait_for_pattern but polls every 0.2s. The unread-badge
# rise→fall window is short under QUM-555 (weave reads the slim notification
# almost immediately, clearing `new/`) so 1s polling can miss the rising
# edge. 0.2s polling reliably catches the badge while it's visible.
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

# wait_for_no_badge_rise <session> <timeout_secs>
# QUM-559: poll the pane for the duration of timeout_secs and fail (return 1)
# if a weave unread-badge ever appears. Returns 0 iff no `weave[^│]*\([1-9]`
# badge ever shows during the sample window. Use after a `sprawl report
# done` to assert the QUM-559 contract: report_status writes nothing to
# maildir, so the badge must NOT rise.
wait_for_no_badge_rise() {
    local session="$1" timeout="$2"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        if capture_pane "$session" | grep -qE "weave[^│]*\([1-9]"; then
            return 1
        fi
        sleep 0.2
    done
    return 0
}

cleanup() {
    local rc=$?
    if [ -n "${SPRAWL_TMUX_SOCKET:-}" ]; then
        tmux -L "$SPRAWL_TMUX_SOCKET" kill-server 2>/dev/null || true
        rm -f -- "/tmp/tmux-$(id -u)/$SPRAWL_TMUX_SOCKET" 2>/dev/null || true
    fi
    # QUM-468: tmux kill-server sends SIGKILL but does not wait for the
    # claude/sprawl children to release their open handles under
    # .sprawl/agents/. A bare `rm -rf` then races and exits ENOTEMPTY
    # ("Directory not empty"), turning a green test into a red make
    # target. Settle briefly, retry, and finally tolerate failure
    # non-fatally — this is teardown, not an assertion. The setsid
    # watchdog (lib/sandbox-traps.sh) is the backstop for any leftover.
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
                echo "  WARN: cleanup could not fully remove $SPRAWL_ROOT (likely stragglers under .sprawl/agents/); watchdog will reap" >&2
            fi
            ;;
    esac
    exit "$rc"
}
trap cleanup EXIT INT TERM HUP

# QUM-458 layer 1: setsid'd watchdog reaps the sandbox if the driver dies via
# SIGKILL (which bypasses bash's EXIT trap).
# shellcheck source=lib/sandbox-traps.sh
. "$(dirname "$0")/lib/sandbox-traps.sh"
sandbox_install_watchdog "$$" "$SPRAWL_TMUX_SOCKET" "$SPRAWL_ROOT"

# --- Launch the TUI in a detached tmux session ---

echo "=== Launching sprawl enter in tmux ==="
# QUM-471: enable unified runtime so this script also guards the
# WeaveRuntimeHandle.WakeForDelivery / ForceInterruptDelivery path. Without this export, the TUI runs
# the legacy notifier — DRAIN_TOKEN_A / DRAIN_TOKEN_B (lines below) only catch
# the legacy regression. Under unified mode, peekAndDrainCmd is the sole drain
# pipeline (Option A): if the handle re-enqueues into the runtime queue,
# EventTurnStarted is skipped by TUIAdapter and the prompt body never reaches
# the viewport. The DRAIN_TOKEN assertions become the QUM-471 regression guard.
# count_inbox_banners (QUM-465) must continue to show exactly 1 banner per
# delivery; Option A makes unified-mode behavior match legacy.
#
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
#
# QUM-559: report_status no longer writes to maildir. The badge must NOT rise,
# no `inbox: N new message` banner must appear, and no drain notification with
# `mcp__sprawl__messages_read` from $CHILD_NAME must surface. The CLI must
# still exit 0 and persist `state.last_report_message` on the child agent.

echo ""
echo "=== Test A: child 'sprawl report done' → state.last_report_message only (no maildir) ==="
BANNERS_BEFORE_A=$(count_inbox_banners "$SESSION")
DRAINS_BEFORE_A=$(count_drain_notifications "$SESSION" "$CHILD_NAME")
REPORT_LOG="$(mktemp /tmp/notify-tui-e2e-report.XXXXXX)"
set +e
env \
    SPRAWL_AGENT_IDENTITY="$CHILD_NAME" \
    SPRAWL_ROOT="$SPRAWL_ROOT" \
    SPRAWL_TEST_MODE=1 \
    "$SPRAWL_BIN" report done "e2e tui notify test A" \
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

# QUM-559: child's state.last_report_message must be set (state-only persistence).
CHILD_STATE_FILE="$CHILD_STATE_DIR/${CHILD_NAME}.json"
if command -v jq >/dev/null 2>&1; then
    LAST_MSG=$(jq -r '.last_report_message // empty' "$CHILD_STATE_FILE" 2>/dev/null || echo "")
    if [ "$LAST_MSG" = "e2e tui notify test A" ]; then
        pass "QUM-559: child state.last_report_message persisted"
    else
        fail "QUM-559: child state.last_report_message NOT persisted after sprawl report done (got: $LAST_MSG)"
        echo "  child state file:" >&2
        cat "$CHILD_STATE_FILE" >&2 || true
    fi
else
    if grep -qE '"last_report_message"[^,}]*e2e tui notify test A' "$CHILD_STATE_FILE"; then
        pass "QUM-559: child state.last_report_message persisted"
    else
        fail "QUM-559: child state.last_report_message NOT persisted after sprawl report done"
        echo "  child state file:" >&2
        cat "$CHILD_STATE_FILE" >&2 || true
    fi
fi

# QUM-559: badge must NOT rise — report_status doesn't touch the maildir.
if wait_for_no_badge_rise "$SESSION" 5; then
    pass "QUM-559: weave row stayed at no unread badge after sprawl report done"
else
    fail "QUM-559: weave row showed an unread badge after sprawl report done (maildir leak)"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -30 >&2
fi

rm -f "$REPORT_LOG"

# QUM-559: banner delta must be 0 (no `inbox: N new message` for report_status).
sleep 5
BANNERS_AFTER_A=$(count_inbox_banners "$SESSION")
DELTA_A=$((BANNERS_AFTER_A - BANNERS_BEFORE_A))
if [ "$DELTA_A" -eq 0 ]; then
    pass "QUM-559: zero banner-count delta after report_status (was QUM-465 delta=1, flipped to delta=0)"
else
    fail "QUM-559: banner-count delta = $DELTA_A (before=$BANNERS_BEFORE_A, after=$BANNERS_AFTER_A); expected 0"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
fi

# QUM-559: no maildir-style drain notification from $CHILD_NAME must appear.
DRAINS_AFTER_A=$(count_drain_notifications "$SESSION" "$CHILD_NAME")
if [ "$DRAINS_AFTER_A" -eq "$DRAINS_BEFORE_A" ]; then
    pass "QUM-559: no maildir-drain notification from '$CHILD_NAME' (delta=0)"
else
    fail "QUM-559: maildir-drain notification from '$CHILD_NAME' appeared after report_status (delta=$((DRAINS_AFTER_A - DRAINS_BEFORE_A)))"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
fi

# --- Test B: `sprawl messages send weave` from the same child ---

echo ""
echo "=== Test B: child 'sprawl messages send weave' → badge rises to (1) ==="
# QUM-465: snapshot banner count before second send.
BANNERS_BEFORE_B=$(count_inbox_banners "$SESSION")
# QUM-555: snapshot drain count before second send to assert delta>=1.
DRAINS_BEFORE_B=$(count_drain_notifications "$SESSION" "$CHILD_NAME")
SEND_LOG="$(mktemp /tmp/notify-tui-e2e-send.XXXXXX)"
set +e
env \
    SPRAWL_AGENT_IDENTITY="$CHILD_NAME" \
    SPRAWL_ROOT="$SPRAWL_ROOT" \
    SPRAWL_TEST_MODE=1 \
    "$SPRAWL_BIN" messages send weave "tui e2e subject" "tui e2e body B" \
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

if wait_for_pattern_fast "$SESSION" "weave[^│]*\\(1\\)" 10; then
    pass "QUM-559: weave row shows '(1)' unread badge after first real maildir delivery (Test A no longer leaves residue)"
else
    fail "weave row did NOT rise to '(1)' after sprawl messages send"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -30 >&2
fi

# QUM-323 / QUM-555: assert the second drain reached weave's prompt by
# observing the `<system-notification>` count from $CHILD_NAME rise after
# Test B fires. Note: if Test A's drain is still mid-turn when B arrives, B
# stays pending until claude finishes A; the tick backstop then drains it.
# Timeout tuned accordingly (claude turn latency + 2s tick + send).
DRAIN_B_DEADLINE=$((SECONDS + 45))
DRAIN_B_OK=0
while [ "$SECONDS" -lt "$DRAIN_B_DEADLINE" ]; do
    DRAINS_NOW_B=$(count_drain_notifications "$SESSION" "$CHILD_NAME")
    if [ "$DRAINS_NOW_B" -gt "$DRAINS_BEFORE_B" ]; then
        DRAIN_B_OK=1
        break
    fi
    sleep 1
done
if [ "$DRAIN_B_OK" -eq 1 ]; then
    pass "QUM-555 second drain notification from '$CHILD_NAME' reached weave's prompt (QUM-323 drain fired in TUI)"
else
    fail "QUM-555 second drain notification from '$CHILD_NAME' did NOT reach weave's prompt within 45s — QUM-323 TUI regression"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
fi

rm -f "$SEND_LOG"

# QUM-465 / QUM-555: assert exactly ONE inbox banner was added by Test B's
# single delivery. Sample the banner count repeatedly and take the MAX
# observed value — under QUM-555 weave's response after Test B can scroll
# Test A's banner out of the viewport before a single post-settle sample
# would capture it, masking the delta. The max-over-window approach catches
# the rising edge regardless of subsequent scroll.
BANNERS_MAX_B=$BANNERS_BEFORE_B
BANNER_SAMPLE_END=$((SECONDS + 10))
while [ "$SECONDS" -lt "$BANNER_SAMPLE_END" ]; do
    BANNERS_NOW=$(count_inbox_banners "$SESSION")
    if [ "$BANNERS_NOW" -gt "$BANNERS_MAX_B" ]; then
        BANNERS_MAX_B=$BANNERS_NOW
    fi
    sleep 0.2
done
DELTA_B=$((BANNERS_MAX_B - BANNERS_BEFORE_B))
if [ "$DELTA_B" -eq 1 ]; then
    pass "QUM-465: exactly 1 banner added by Test B delivery (delta=$DELTA_B)"
else
    fail "QUM-465: Test B produced $DELTA_B banners (before=$BANNERS_BEFORE_B, max=$BANNERS_MAX_B); expected exactly 1"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
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
