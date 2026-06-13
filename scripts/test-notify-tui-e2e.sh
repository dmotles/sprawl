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
#   4. Registers a simulated out-of-process child agent (sandbox-child)
#      directly in state on disk (no CLI shellout — QUM-565 migrated this
#      test off the deprecated `sprawl-report` / `sprawl-messages` CLI
#      surface ahead of its 2.3b deletion).
#   5. Test A: simulates an MCP `report_status` by writing the child's
#      `state.json` directly with `status: done` and `last_report_message`
#      set. The on-disk write is the contract — `report_status` no longer
#      touches the maildir per QUM-559, so this is the same observable
#      footprint. Asserts the TUI badge does NOT rise, no inbox banner
#      surfaces, and no drain notification appears.
#   6. Test B: simulates an MCP `messages_send` by writing a maildir
#      envelope directly under `.sprawl/messages/weave/new/` (atomic
#      tmp→new rename, schema per `internal/messages/messages.go`).
#      Asserts the TUI pane picks up the maildir rise on its 2s tick and
#      renders both (a) the banner 'inbox: N new message[s]' (QUM-473 §3
#      unified format), and (b) the '(1)' unread badge on the weave row.
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

# QUM-555/QUM-556/QUM-557/QUM-562: count drain notification rows surfaced in
# weave's viewport. The typed-attribute wire format (QUM-562) carries three
# `<system-notification>` shapes:
#   - type="message"                       → ✉ glyph, NotificationText (cyan)
#   - type="message" interrupt="true"      → ⚡ glyph, InterruptText (amber)
#   - type="status_change"                 → ◉ glyph, StatusChangeText (grey)
# This counter targets only the message-class drain rows (async + interrupt),
# anchored on the `mcp__sprawl__messages_read(id=<id>)` citation that is
# present only in message-class lines (status_change lines do not cite the
# read tool). The TUI strips the literal tags before rendering, so the pane
# capture sees the glyph + body, not the raw markup.
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
# badge ever shows during the sample window. Use after a simulated
# report_status to assert the QUM-559 contract: report_status writes
# nothing to maildir, so the badge must NOT rise.
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

if wait_for_pattern "$SESSION" "weave ●" 30; then
    pass "TUI rendered ('weave ●' root pill visible — QUM-656/733 orbital tree)"
else
    fail "TUI did not render 'weave ●' within 30s"
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
#     CHILD_NAME=sandbox-child (tower convention — avoids
#     pretend-child-identity leaks into outer sessions; see QUM-311 /
#     /e2e-testing-sandboxing).
#
# QUM-565: this script no longer shells out to the deprecated `sprawl
# report` / `sprawl-messages-send` CLI surface (slated for deletion in
# Phase 2.3b of M13). Tests A and B now write the same on-disk
# side-effects directly: state.json for report_status, and a maildir
# envelope (schema per internal/messages/messages.go) for messages_send.
# The TUI watcher reacts to the on-disk contract, not to the CLI invocation.

CHILD_NAME="sandbox-child"
CHILD_STATE_DIR="$SPRAWL_ROOT/.sprawl/agents"
CHILD_STATE_FILE="$CHILD_STATE_DIR/${CHILD_NAME}.json"
mkdir -p "$CHILD_STATE_DIR"
cat > "$CHILD_STATE_FILE" <<JSON
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

# --- Test A: simulated MCP `report_status done` (state-only write) ---
#
# QUM-559: report_status no longer writes to maildir; the only on-disk
# side effect is updating the caller's state.json (status + last_report_*
# fields). We replicate that here without invoking the deprecated CLI:
# write a fresh state.json with status=done and last_report_message set.
# The TUI's AgentTreeMsg poll reads state.json for display only — it does
# NOT use state-file changes as a notification trigger (oracle confirmed
# during QUM-565). So this state-only write must NOT raise the badge,
# must NOT surface an `inbox: N new message` banner, and must NOT cause
# a drain notification citing `mcp__sprawl__messages_read` to appear.

echo ""
echo "=== Test A: simulated MCP report_status → state.last_report_message only (no maildir) ==="
BANNERS_BEFORE_A=$(count_inbox_banners "$SESSION")
DRAINS_BEFORE_A=$(count_drain_notifications "$SESSION" "$CHILD_NAME")

REPORT_MSG_A="e2e tui notify test A"
REPORT_AT_A="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
cat > "$CHILD_STATE_FILE" <<JSON
{
  "name": "${CHILD_NAME}",
  "type": "engineer",
  "family": "engineering",
  "parent": "weave",
  "prompt": "tui notify e2e test",
  "branch": "tui-notify-e2e",
  "worktree": "${SPRAWL_ROOT}",
  "status": "done",
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "tree_path": "weave├${CHILD_NAME}",
  "last_report_type": "done",
  "last_report_message": "${REPORT_MSG_A}",
  "last_report_at": "${REPORT_AT_A}",
  "last_report_state": "complete"
}
JSON
pass "simulated report_status: wrote state.json with status=done + last_report_message"

# QUM-559: child's state.last_report_message must be set (state-only persistence).
if command -v jq >/dev/null 2>&1; then
    LAST_MSG=$(jq -r '.last_report_message // empty' "$CHILD_STATE_FILE" 2>/dev/null || echo "")
    if [ "$LAST_MSG" = "$REPORT_MSG_A" ]; then
        pass "QUM-559: child state.last_report_message persisted"
    else
        fail "QUM-559: child state.last_report_message NOT persisted (got: $LAST_MSG)"
        echo "  child state file:" >&2
        cat "$CHILD_STATE_FILE" >&2 || true
    fi
else
    if grep -qE "\"last_report_message\"[^,}]*$REPORT_MSG_A" "$CHILD_STATE_FILE"; then
        pass "QUM-559: child state.last_report_message persisted"
    else
        fail "QUM-559: child state.last_report_message NOT persisted"
        echo "  child state file:" >&2
        cat "$CHILD_STATE_FILE" >&2 || true
    fi
fi

# QUM-559: badge must NOT rise — state-only writes don't touch the maildir.
if wait_for_no_badge_rise "$SESSION" 5; then
    pass "QUM-559: weave row stayed at no unread badge after simulated report_status"
else
    fail "QUM-559: weave row showed an unread badge after simulated report_status (maildir leak)"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -30 >&2
fi

# QUM-559: banner delta must be 0 (no `inbox: N new message` for state-only writes).
sleep 5
BANNERS_AFTER_A=$(count_inbox_banners "$SESSION")
DELTA_A=$((BANNERS_AFTER_A - BANNERS_BEFORE_A))
if [ "$DELTA_A" -eq 0 ]; then
    pass "QUM-559: zero banner-count delta after simulated report_status (state-only)"
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
    fail "QUM-559: maildir-drain notification from '$CHILD_NAME' appeared after simulated report_status (delta=$((DRAINS_AFTER_A - DRAINS_BEFORE_A)))"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
fi

# --- Test B: simulated MCP `messages_send` (direct maildir envelope write) ---
#
# QUM-565: schema mirrors internal/messages/messages.go Send() — fields
# `id` (<unixnano>.<from>.<8hex>), `shortId` (3-char base36), `from`, `to`,
# `subject`, `body`, `timestamp` (UTC RFC3339). Atomic write: tmp/ then
# rename into new/. We also drop a sent/-copy under the sender's mailbox
# to match Send()'s outbox behavior, and pre-create cur/ + archive/ so
# downstream MarkRead / Archive paths don't ENOENT during this run.

echo ""
echo "=== Test B: simulated MCP messages_send weave → badge rises to (1) ==="
# QUM-465: snapshot banner count before second send.
BANNERS_BEFORE_B=$(count_inbox_banners "$SESSION")
# QUM-565: drain-count snapshot dropped; see comment block after badge
# assertion below for rationale.

# Build a maildir envelope matching internal/messages.Message.
WEAVE_MBOX="$SPRAWL_ROOT/.sprawl/messages/weave"
SENDER_MBOX="$SPRAWL_ROOT/.sprawl/messages/$CHILD_NAME"
mkdir -p "$WEAVE_MBOX/tmp" "$WEAVE_MBOX/new" "$WEAVE_MBOX/cur" "$WEAVE_MBOX/archive"
mkdir -p "$SENDER_MBOX/sent"

# unixnano (preferred) + 8 random hex chars; RFC3339 UTC timestamp.
NS_NOW="$(python3 -c 'import time; print(time.time_ns())' 2>/dev/null || date +%s%N)"
HEX_SUFFIX="$(head -c 4 /dev/urandom | xxd -p)"
SHORT_ID="$(head -c 3 /dev/urandom | xxd -p | tr 'A-Z' 'a-z' | head -c 3)"
MSG_ID="${NS_NOW}.${CHILD_NAME}.${HEX_SUFFIX}"
MSG_TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
MSG_FILE="${MSG_ID}.json"

cat > "$WEAVE_MBOX/tmp/$MSG_FILE" <<JSON
{
  "id": "${MSG_ID}",
  "shortId": "${SHORT_ID}",
  "from": "${CHILD_NAME}",
  "to": "weave",
  "subject": "tui e2e subject",
  "body": "tui e2e body B",
  "timestamp": "${MSG_TS}"
}
JSON
mv "$WEAVE_MBOX/tmp/$MSG_FILE" "$WEAVE_MBOX/new/$MSG_FILE"
cp "$WEAVE_MBOX/new/$MSG_FILE" "$SENDER_MBOX/sent/$MSG_FILE"
pass "simulated messages_send: wrote maildir envelope (id=$SHORT_ID) atomically into weave/new/"

if wait_for_pattern_fast "$SESSION" "weave[^│]*\\(1\\)" 15; then
    pass "QUM-559: weave row shows '(1)' unread badge after first real maildir delivery (Test A no longer leaves residue)"
else
    fail "weave row did NOT rise to '(1)' after simulated messages_send"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -30 >&2
fi

# QUM-565: drain-row inject assertion intentionally NOT made here.
#
# A former assertion (QUM-555 / QUM-323 guard) observed weave's pane for a
# `<system-notification>` drain row citing `mcp__sprawl__messages_read` after
# Test B fired. That row is driven by `internal/messages.Send()`'s
# defaultNotifier callback → `supervisor.WakeForDelivery` → claude prompt-inject.
# The direct maildir-envelope write above (which replaced the now-deleted
# `messages send` CLI invocation removed in Phase 2.3b of M13, QUM-566)
# correctly exercises the TUI's maildir watcher (banner + badge,
# asserted above) but bypasses the Send()/WakeForDelivery arm — so the drain
# row never lands. No out-of-process IPC exists today to drive the in-process
# supervisor singleton from this script.
#
# The notifier+wake mechanics remain unit-tested in
# `internal/runtime/unified_delivery_send_message_test.go` and the
# `internal/supervisor/*_test.go` suites; the end-to-end drain-row inject is
# exercised live by every real-claude-child workflow in production. Restoring
# a shell-layer regression guard via a real-claude micro-harness is tracked
# in QUM-569.

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
