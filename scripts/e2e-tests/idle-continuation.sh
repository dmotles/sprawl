#!/usr/bin/env bash
# scripts/e2e-tests/idle-continuation.sh — QUM-815 (Slice 1 of QUM-813) headline
# gate. Closes QUM-812.
#
# Proves the output-path fix end-to-end against the REAL claude CLI: when a
# run_in_background task completes while weave is IDLE (turn already ended), the
# CLI self-reprompts an AUTONOMOUS turn whose task_notification now flows through
# the single frame router → enqueues a QUM-640 continuation → a continuation turn
# fires WITH NO MANUAL PROMPT, and the turn shows a balanced start/complete (no
# wedge) in the viewport.
#
# Signal: weave's activity.ndjson "kind":"result" count must rise AFTER the
# background task completes, with zero intervening keystrokes. A balanced
# start/complete is proven by the status bar NOT being stuck in Streaming/Thinking
# afterward (an unbalanced lifecycle wedges the TUI turn-state reducer).

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# Count "kind":"result" entries in weave's activity.ndjson (one per completed
# turn).
count_results() {
    local f="$1" n
    [ -f "$f" ] || { echo 0; return; }
    # grep -c prints the count AND exits 1 when there are zero matches, so
    # capture the printed "0" and swallow the non-zero exit (avoid emitting a
    # second line).
    n=$(grep -c '"kind":"result"' "$f" 2>/dev/null) || n=0
    echo "$n"
}

# wait_results_ge FILE TARGET TIMEOUT — poll until the result count reaches
# TARGET or the deadline elapses. Returns 0 on success.
wait_results_ge() {
    local f="$1" target="$2" timeout="$3"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        if [ "$(count_results "$f")" -ge "$target" ]; then
            return 0
        fi
        sleep 1
    done
    return 1
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-idlecont-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum815"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    # Real claude needs auth; the run-claude shim re-hydrates the token from
    # $SPRAWL_ROOT/.env in the spawned shell (QUM-518).
    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SUFFIX SESSION STDERR_LOG
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    SESSION="sprawl-idlecont-${SUFFIX}"
    STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"
    local WEAVE_ACT="$SPRAWL_ROOT/.sprawl/agents/weave/activity.ndjson"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"

    echo ""
    echo "=== Launching sprawl enter (real claude via run-claude shim) ==="
    _stmux new-session -d -s "$SESSION" -x 200 -y 50 \
        "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_CLAUDE='$SPRAWL_CLAUDE' '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
    _stmux set-option -t "$SESSION" window-size manual >/dev/null
    _stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

    if ! wait_for_pattern "$SESSION" "weave " 45; then
        fail "TUI did not render within 45s"
        capture_pane "$SESSION" | tail -30 >&2
        [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
        e2e_print_results
        return 1
    fi
    pass "TUI rendered (weave root visible)"
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION"

    # =====================================================================
    # Drive: weave starts a short-lived background task, then ends its turn.
    # =====================================================================
    local PROBE="BGDONE_${SUFFIX}"
    local PROMPT="Use the Bash tool with run_in_background=true and command exactly: sleep 12; echo ${PROBE}. Start it in the background and then IMMEDIATELY end your turn — do not wait for it, do not call BashOutput, reply with just the word STARTED and stop."

    echo ""
    echo "=== Turn 1: start background task, then go idle ==="
    e2e_send_user_prompt "$SESSION" "$PROMPT"

    # Wait for the first (sprawl) turn to complete → 1 result.
    if ! wait_results_ge "$WEAVE_ACT" 1 90; then
        fail "first turn never produced a result (weave did not run the prompt)"
        capture_pane "$SESSION" | tail -40 >&2
        [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
        e2e_print_results
        return 1
    fi
    local RESULTS_AFTER_T1
    RESULTS_AFTER_T1=$(count_results "$WEAVE_ACT")
    pass "Turn 1 completed (results=${RESULTS_AFTER_T1}); weave now idle, bg task pending"

    # Confirm weave went idle (turn 1 ended) before the bg task completes.
    if wait_for_pattern_fast "$SESSION" "weave .*(idle|◌)|\\(idle\\)" 10; then
        pass "weave reached idle between turns"
    else
        echo "  (note: idle glyph not matched in pane; proceeding on result-count signal)"
    fi

    # =====================================================================
    # Crux: WITHOUT any further keystroke, the bg task (sleep 12) completes
    # while idle → autonomous turn → QUM-640 continuation → a NEW turn fires.
    # =====================================================================
    echo ""
    echo "=== Idle continuation: bg completion must drive a new turn with NO manual prompt ==="
    local TARGET=$((RESULTS_AFTER_T1 + 1))
    if wait_results_ge "$WEAVE_ACT" "$TARGET" 90; then
        pass "QUM-812 FIXED: result count rose to >=${TARGET} with NO manual input (continuation fired)"
    else
        fail "QUM-812: no continuation turn fired while idle (results stuck at $(count_results "$WEAVE_ACT"), want >=${TARGET})"
        echo "  --- weave activity.ndjson tail ---" >&2
        [ -f "$WEAVE_ACT" ] && tail -25 "$WEAVE_ACT" >&2
        echo "  --- pane tail ---" >&2
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    # The background task actually ran (sentinel in activity or wire log).
    local WIRE_DIR="$SPRAWL_ROOT/.sprawl/logs/sessions/weave"
    if grep -rqF "$PROBE" "$WEAVE_ACT" "$WIRE_DIR" 2>/dev/null; then
        pass "background task sentinel '${PROBE}' observed (bg task completed)"
    else
        echo "  (note: sentinel '${PROBE}' not located in logs; continuation still proven by result-count rise)"
    fi

    # =====================================================================
    # Balanced start/complete: the TUI must not be wedged in a streaming state
    # after the autonomous + continuation turns settle.
    # =====================================================================
    echo ""
    echo "=== Balanced lifecycle: no streaming wedge after continuation ==="
    sleep 4
    if capture_pane "$SESSION" | grep -qiE "Streaming\\.\\.\\.|Thinking\\.\\.\\.|esc to interrupt"; then
        fail "TUI appears wedged in a streaming/thinking state after continuation (unbalanced lifecycle)"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    pass "TUI settled (no streaming wedge) — autonomous + continuation turns emitted balanced lifecycle"

    e2e_print_results
}
