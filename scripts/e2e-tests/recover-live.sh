#!/usr/bin/env bash
# scripts/e2e-tests/recover-live.sh — QUM-606 recover-survival regression
# guard, migrated onto the matrix harness (QUM-616 Wave 2C).
#
# Build tag: needs_build_tags=sprawl_test so the build-tag-gated
# `mcp__sprawl___test_induce_wedge` MCP tool is compiled in.
#
# Phases:
#   1. Spawn an engineer-type child.
#   2. Induce SubscriberWedge fault via the test tool.
#   3. Drive mcp__sprawl__recover.
#   4. (PRIMARY) NEW claude --resume subprocess (PID ≠ original) is alive
#      2s after recover returns.
#   5. Drive a post-recover send_message; sentinel must land in the
#      child's activity.ndjson within 60s.
#
# The full original lives at scripts/test-recover-live-e2e.sh and is
# untouched during soak.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1 needs_build_tags=sprawl_test"
}

test_run() {
    if ! command -v pgrep >/dev/null 2>&1; then
        echo "FATAL: pgrep not on PATH" >&2
        return 1
    fi

    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-recover-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum606"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    # Copy .env so scripts/run-claude can rehydrate auth in subshells.
    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi

    export SPRAWL_ENABLE_TEST_TOOLS=1
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SESSION="sprawl-recover-e2e-$(head -c4 /dev/urandom | xxd -p)"
    local STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"
    local PROBE="RECOVER-PROBE-$$-$(date +%s)"
    local BRANCH_SUFFIX
    BRANCH_SUFFIX="$(head -c4 /dev/urandom | xxd -p)"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo "  PROBE=$PROBE"

    echo ""
    echo "=== Launching sprawl enter ==="
    # Custom launch (not e2e_launch_tui) so we can inject
    # SPRAWL_CLAUDE / SPRAWL_ENABLE_TEST_TOOLS into the spawned shell.
    _stmux new-session -d -s "$SESSION" -x 200 -y 50 \
        "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_CLAUDE='$SPRAWL_CLAUDE' SPRAWL_ENABLE_TEST_TOOLS=1 '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
    _stmux set-option -t "$SESSION" window-size manual >/dev/null
    _stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

    if wait_for_pattern "$SESSION" "weave \\(idle\\)" 45; then
        pass "TUI rendered ('weave (idle)' visible)"
    else
        fail "TUI did not render within 45s"
        capture_pane "$SESSION" | tail -30 >&2
        [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
        e2e_print_results
        return 1
    fi
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3

    echo ""
    echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
    e2e_attach_phantom_client "$SESSION"

    # --- Phase 1: spawn the child ---
    echo ""
    echo "=== Phase 1: spawn an engineer child that idles ==="
    local SPAWN_PROMPT="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum-606-recover-probe-${BRANCH_SUFFIX}', and prompt set to exactly: 'You are an automated QUM-606 probe. Call mcp__sprawl__report_status with state=working, summary=\"idle, awaiting fault induction\". Then stop and wait. Do nothing else until you receive a message.'"
    _stmux send-keys -t "$SESSION" "$SPAWN_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    local CHILD_STATE=""
    local CHILD_NAME=""
    local ELAPSED=0
    local SPAWN_LANDED=0
    while [ "$ELAPSED" -lt 180 ]; do
        while IFS= read -r candidate; do
            [ -z "$candidate" ] && continue
            local local_name
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
        e2e_print_results
        return 1
    fi
    pass "child spawned (name=$CHILD_NAME)"

    local ORIG_SID=""
    local i
    for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
        ORIG_SID=$(jq -r '.session_id // empty' "$CHILD_STATE" 2>/dev/null || true)
        [ -n "$ORIG_SID" ] && break
        sleep 2
    done
    if [ -z "$ORIG_SID" ]; then
        fail "child session_id never materialized; cannot run recover probe"
        cat "$CHILD_STATE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    pass "child session_id=$ORIG_SID"

    local ORIG_PID=""
    for i in 1 2 3 4 5; do
        ORIG_PID=$(pgrep -af 'claude' | awk -v sid="$ORIG_SID" '$0 ~ sid {print $1; exit}' || true)
        [ -n "$ORIG_PID" ] && break
        sleep 2
    done
    if [ -z "$ORIG_PID" ]; then
        fail "could not locate original claude subprocess matching session_id=$ORIG_SID"
        pgrep -af claude >&2 || true
        e2e_print_results
        return 1
    fi
    pass "original child claude PID=$ORIG_PID"

    # --- Phase 2: induce wedge ---
    echo ""
    echo "=== Phase 2: induce SubscriberWedge fault on $CHILD_NAME ==="
    local INDUCE_PROMPT="Call mcp__sprawl___test_induce_wedge with agent_name='$CHILD_NAME', fault_class='subscriber_wedged'. Confirm in your reply that the call succeeded."
    _stmux send-keys -t "$SESSION" "$INDUCE_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    if wait_for_pattern_fast "$SESSION" "Induced subscriber_wedged|SubscriberWedge|fault" 60; then
        pass "fault induction tool returned"
    else
        fail "fault induction tool did not surface within 60s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    # --- Phase 3: drive recover ---
    echo ""
    echo "=== Phase 3: drive mcp__sprawl__recover on $CHILD_NAME ==="
    local RECOVER_PROMPT="Call mcp__sprawl__recover with agent_name='$CHILD_NAME'. Quote the exact tool response back to me."
    _stmux send-keys -t "$SESSION" "$RECOVER_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    if wait_for_pattern_fast "$SESSION" "Recovered backend session for $CHILD_NAME" 60; then
        pass "mcp__sprawl__recover returned success ack"
    else
        fail "recover success ack did not appear within 60s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # --- Phase 4 (PRIMARY): new live claude --resume subprocess exists ---
    echo ""
    echo "=== Phase 4 (PRIMARY): new claude --resume subprocess survives ==="
    sleep 2
    local NEW_PID=""
    local PROBE_END=$((SECONDS + 10))
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
        e2e_print_results
        return 1
    fi
    if [ "$NEW_PID" = "$ORIG_PID" ]; then
        fail "PRIMARY: new claude PID ($NEW_PID) equals original ($ORIG_PID) — recover did not actually swap the subprocess"
        e2e_print_results
        return 1
    fi
    if ! kill -0 "$NEW_PID" 2>/dev/null; then
        fail "PRIMARY: new claude PID $NEW_PID does not respond to signal 0 — subprocess died immediately"
        e2e_print_results
        return 1
    fi
    pass "new claude --resume PID=$NEW_PID alive (was $ORIG_PID)"

    # --- Phase 5: drive a post-recover turn ---
    echo ""
    echo "=== Phase 5: drive a post-recover turn and assert frames ==="
    local TURN_PROMPT="Call mcp__sprawl__send_message with to='$CHILD_NAME', body='Echo ${PROBE} verbatim in your next reply and then call report_status complete.', interrupt=false."
    _stmux send-keys -t "$SESSION" "$TURN_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    local ACTIVITY="$SPRAWL_ROOT/.sprawl/agents/$CHILD_NAME/activity.ndjson"
    local ACT_END=$((SECONDS + 60))
    local ACT_SEEN=0
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

    e2e_print_results
}
