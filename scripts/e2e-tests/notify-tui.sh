#!/usr/bin/env bash
# scripts/e2e-tests/notify-tui.sh — QUM-312/QUM-559/QUM-565/QUM-471 regression
# guards. Migrated from scripts/test-notify-tui-e2e.sh (which remains in place
# until soak completes; do not edit the original — see QUM-616 Wave 2A).
#
# Test A: simulated MCP report_status → state-only write. Asserts the TUI
#         badge does NOT rise, no inbox banner surfaces, and no drain
#         notification appears (QUM-559 contract).
# Test B: simulated MCP messages_send → direct maildir envelope write. Asserts
#         the TUI picks up the maildir rise on its 2s tick and renders both
#         (a) the 'inbox: N new message[s]' banner (QUM-473 §3) and
#         (b) the '(1)' unread badge on the weave row.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

# Count occurrences of an inbox-banner pattern in the current pane capture.
# QUM-473 §3 unified the banner format across both emit sites:
#   - "inbox: N new message[s]"             (from AgentTreeMsg rise-detector)
#   - "inbox: N new message[s] from <sender>" (from InboxArrivalMsg notifier)
# QUM-465: a single send_async to weave must produce exactly one of these.
count_inbox_banners() {
    local session="$1"
    capture_pane "$session" \
        | grep -cE "inbox: [0-9]+ new message" \
        || true
}

# QUM-555/QUM-556/QUM-557/QUM-562: count message-class drain rows surfaced in
# weave's viewport, anchored on `mcp__sprawl__messages_read(id=<id>)` which is
# present only on async / interrupt message lines (status_change lines do not
# cite the read tool).
count_drain_notifications() {
    local session="$1"
    local sender="$2"
    capture_pane "$session" \
        | grep -cE "(✉|⚡) (\\[interrupt\\] )?From $sender — mcp__sprawl__messages_read\\(id=[^)]+\\)" \
        || true
}

# QUM-559: poll for `timeout` seconds and fail (return 1) if a weave
# unread-badge ever appears. Returns 0 iff no `weave[^│]*\([1-9]` badge ever
# shows during the sample window.
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

test_run() {
    e2e_recover_oauth_token
    e2e_setup_tmux_socket "sprawl-notify-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-tui-notify-e2e"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    local SESSION="sprawl-notify-tui-$(head -c4 /dev/urandom | xxd -p)"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo ""

    # QUM-471: unified runtime is the default; if the handle re-enqueues into
    # the runtime queue, EventTurnStarted is skipped by TUIAdapter and the
    # prompt body never reaches the viewport. count_inbox_banners (QUM-465)
    # must continue to show exactly 1 banner per delivery.
    echo "=== Launching sprawl enter in tmux ==="
    if ! e2e_launch_tui "$SESSION" 200 50; then
        return 1
    fi
    pass "TUI rendered ('weave (idle)' visible in tree panel)"

    # Advance past any first-run trust prompt (QUM-310 gotcha).
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi

    # Give the first AgentTreeMsg tick a moment to land so rootUnread starts
    # at 0 before we trigger the first message.
    sleep 3

    # --- Register a fake child agent in state (CHILD_NAME=sandbox-child, tower
    #     convention to avoid pretend-child-identity leaks into outer sessions
    #     — see QUM-311 / /e2e-testing-sandboxing).
    local CHILD_NAME="sandbox-child"
    local CHILD_STATE_DIR="$SPRAWL_ROOT/.sprawl/agents"
    local CHILD_STATE_FILE="$CHILD_STATE_DIR/${CHILD_NAME}.json"
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
    # side effect is updating the caller's state.json. The TUI's AgentTreeMsg
    # poll reads state.json for display only — it does NOT use state-file
    # changes as a notification trigger. So this state-only write must NOT
    # raise the badge, must NOT surface an `inbox: N new message` banner, and
    # must NOT cause a drain notification citing
    # `mcp__sprawl__messages_read` to appear.
    echo ""
    echo "=== Test A: simulated MCP report_status → state.last_report_message only (no maildir) ==="
    local BANNERS_BEFORE_A DRAINS_BEFORE_A
    BANNERS_BEFORE_A=$(count_inbox_banners "$SESSION")
    DRAINS_BEFORE_A=$(count_drain_notifications "$SESSION" "$CHILD_NAME")

    local REPORT_MSG_A="e2e tui notify test A"
    local REPORT_AT_A
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

    # QUM-559: child's state.last_report_message must be set.
    local LAST_MSG
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

    # QUM-559: banner delta must be 0.
    sleep 5
    local BANNERS_AFTER_A DELTA_A
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
    local DRAINS_AFTER_A
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
    # QUM-565: schema mirrors internal/messages/messages.go Send(). Atomic
    # write: tmp/ then rename into new/. Also drop a sent/-copy under the
    # sender's mailbox and pre-create cur/+archive/ so downstream
    # MarkRead/Archive don't ENOENT during this run.
    echo ""
    echo "=== Test B: simulated MCP messages_send weave → badge rises to (1) ==="
    local BANNERS_BEFORE_B
    BANNERS_BEFORE_B=$(count_inbox_banners "$SESSION")

    local WEAVE_MBOX="$SPRAWL_ROOT/.sprawl/messages/weave"
    local SENDER_MBOX="$SPRAWL_ROOT/.sprawl/messages/$CHILD_NAME"
    mkdir -p "$WEAVE_MBOX/tmp" "$WEAVE_MBOX/new" "$WEAVE_MBOX/cur" "$WEAVE_MBOX/archive"
    mkdir -p "$SENDER_MBOX/sent"

    local NS_NOW HEX_SUFFIX SHORT_ID MSG_ID MSG_TS MSG_FILE
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
        pass "QUM-559: weave row shows '(1)' unread badge after first real maildir delivery"
    else
        fail "weave row did NOT rise to '(1)' after simulated messages_send"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -30 >&2
    fi

    # QUM-565: drain-row inject assertion intentionally NOT made here. The
    # direct maildir-envelope write above exercises the TUI's maildir watcher
    # (banner + badge) but bypasses the internal/messages.Send()/WakeForDelivery
    # arm — so the drain row never lands. The drain pipeline is exercised live
    # by scripts/test-drain-row-inject-e2e.sh and unit-tested in
    # internal/runtime/unified_delivery_send_message_test.go.

    # QUM-465 / QUM-555: assert exactly ONE inbox banner was added by Test B.
    # Sample max-over-window — weave's response can scroll Test A's banner out
    # of the viewport before a single post-settle sample would capture it.
    local BANNERS_MAX_B=$BANNERS_BEFORE_B
    local BANNER_SAMPLE_END=$((SECONDS + 10))
    local BANNERS_NOW
    while [ "$SECONDS" -lt "$BANNER_SAMPLE_END" ]; do
        BANNERS_NOW=$(count_inbox_banners "$SESSION")
        if [ "$BANNERS_NOW" -gt "$BANNERS_MAX_B" ]; then
            BANNERS_MAX_B=$BANNERS_NOW
        fi
        sleep 0.2
    done
    local DELTA_B=$((BANNERS_MAX_B - BANNERS_BEFORE_B))
    if [ "$DELTA_B" -eq 1 ]; then
        pass "QUM-465: exactly 1 banner added by Test B delivery (delta=$DELTA_B)"
    else
        fail "QUM-465: Test B produced $DELTA_B banners (before=$BANNERS_BEFORE_B, max=$BANNERS_MAX_B); expected exactly 1"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
    fi

    # --- Test C: QUM-665 liveness-driven icon flip from paused to working ---
    #
    # Reuses sandbox-child but flips its self-reported state to "blocked".
    # Expectation:
    #   1) Initial render shows the blocked dot color on the sandbox-child row.
    #   2) After writing a single activity.ndjson entry with TS=now, the row's
    #      dot flips to the working color within ~3s (one 2s tree-rebuild tick
    #      plus margin).
    #   3) After ~3s with no further activity (>2s past last activity, the
    #      RecentActivityWindow), the dot reverts to the blocked color.
    #
    # We grep for the ReportDotWorking / ReportDotBlocked ANSI escape sequences
    # around the "●" glyph on the sandbox-child row. NewTheme builds these from
    # the dark palette's Success (working/green) and Busy (blocked/amber)
    # colors. If the ANSI grep approach proves too fragile in CI, the fallback
    # documented in the spec is to invoke `sprawl status` and assert via JSON —
    # but that path requires Status to expose in_autonomous_turn /
    # last_activity_at (QUM-665 surface) so isn't strictly cheaper.
    echo ""
    echo "=== Test C: QUM-665 liveness-driven icon flip (paused → working → paused) ==="

    # Rewrite child state to status=active, last_report_state=blocked so the
    # baseline icon is blocked (not complete).
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
  "tree_path": "weave├${CHILD_NAME}",
  "last_report_type": "status",
  "last_report_message": "waiting on user",
  "last_report_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "last_report_state": "blocked"
}
JSON
    pass "Test C setup: child state.last_report_state=blocked"

    # Wait for the tree to pick up the state change (one 2s tick + margin).
    sleep 3

    # Extract the first orbital-state glyph appearing on the sandbox-child row.
    # capture_pane (lib/e2e-common.sh) strips color via tmux `-p`. The orbital
    # pill renderer (internal/tui/tree_orbital.go) emits one of the glyphs
    # below per state: ⚙=working, ⏳=idle, ⏸=blocked, ✓=done, ✗=failure.
    extract_child_glyph() {
        local session="$1" child="$2"
        capture_pane "$session" \
            | grep -aoE "${child} [⚙⏳⏸✓✗]" \
            | head -1 \
            | awk '{print $2}'
    }

    # Baseline: assert blocked glyph (⏸) renders on the sandbox-child row.
    local BLOCKED_GLYPH
    BLOCKED_GLYPH=$(extract_child_glyph "$SESSION" "$CHILD_NAME")
    if [ "$BLOCKED_GLYPH" = "⏸" ]; then
        pass "QUM-665 baseline: sandbox-child renders blocked glyph (⏸)"
    else
        fail "expected ⏸ glyph for blocked sandbox-child, got: $BLOCKED_GLYPH"
        capture_pane "$SESSION" | tail -10 >&2
    fi

    # Write a single activity entry with TS=now.
    local ACTIVITY_DIR="${SPRAWL_ROOT}/.sprawl/agents/${CHILD_NAME}"
    mkdir -p "$ACTIVITY_DIR"
    local ACTIVITY_NOW
    ACTIVITY_NOW="$(date -u +%Y-%m-%dT%H:%M:%S.%6NZ)"
    printf '{"ts":"%s","kind":"tool_use","tool":"Bash","summary":"test"}\n' \
        "$ACTIVITY_NOW" \
        > "${ACTIVITY_DIR}/activity.ndjson"
    pass "Test C: wrote activity.ndjson entry (ts=${ACTIVITY_NOW})"

    # Poll up to 3s for the glyph to become ⚙ (working).
    local end=$((SECONDS + 3))
    local FLIPPED=0
    while [ "$SECONDS" -lt "$end" ]; do
        g=$(extract_child_glyph "$SESSION" "$CHILD_NAME")
        if [ "$g" = "⚙" ]; then
            FLIPPED=1
            break
        fi
        sleep 0.2
    done
    if [ "$FLIPPED" -eq 1 ]; then
        pass "QUM-665 flip: blocked → working glyph (⚙) within 3s of activity write"
    else
        fail "QUM-665 flip: glyph did not become ⚙ within 3s; last seen='$(extract_child_glyph "$SESSION" "$CHILD_NAME")'"
        capture_pane "$SESSION" | tail -10 >&2
    fi

    # Wait past the 30s RecentActivityWindow (QUM-692 widened from 2s).
    sleep 31

    # Poll up to 5s for the glyph to revert to ⏸ (blocked).
    end=$((SECONDS + 5))
    local REVERTED=0
    while [ "$SECONDS" -lt "$end" ]; do
        g=$(extract_child_glyph "$SESSION" "$CHILD_NAME")
        if [ "$g" = "⏸" ]; then
            REVERTED=1
            break
        fi
        sleep 0.2
    done
    if [ "$REVERTED" -eq 1 ]; then
        pass "QUM-665 reverted: glyph back to ⏸ (blocked) after window expired"
    else
        fail "QUM-665 reverted: glyph did not revert to ⏸; last seen='$(extract_child_glyph "$SESSION" "$CHILD_NAME")'"
        capture_pane "$SESSION" | tail -10 >&2
    fi

    echo ""
    e2e_print_results
}
