#!/usr/bin/env bash
# scripts/e2e-tests/ask-user-question-idle.sh — QUM-635 live e2e regression
# guard: an ask_user_question left unanswered LONGER than the D1 frame-stall
# watchdog window must NOT be guillotined. Before the fix, the watchdog saw a
# turn blocked on the (human-bound) control_request emitting zero frames,
# assumed a hang, cancelled the request, and left the TUI permanently stuck
# "streaming" a never-ending tool call (input + Esc gated; kill+restart the
# only recovery).
#
# The 10-minute default D1 window is impractical for an automated test, so this
# row launches sprawl with SPRAWL_BACKEND_HANG_TIMEOUT set to a short duration
# (QUM-635 diagnostic seam in internal/backend/claude/adapter.go). The watchdog
# ticks once a minute, so we idle well past both the window AND a tick boundary
# to guarantee a pre-fix build would have faulted.
#
# Assertions:
#   1. weave's modal appears ("is asking").
#   2. After idling > window + a watchdog tick with the modal UNANSWERED:
#        - the modal is STILL live ("is asking" still shown),
#        - NO "backend fault" banner appeared (D1 did not fire),
#        - weave's state is not faulted.
#   3. Answering the modal round-trips (weave reports the selected label),
#      proving the watchdog re-armed cleanly on resume (re-seed path).

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-auq-idle-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-auq-idle-e2e"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    # D1 watchdog window for this run. Kept short so the idle wait is bounded;
    # the watchdog tick cadence is ~1 minute, so we idle past a tick below.
    local HANG="25s"
    local IDLE_WAIT=95 # seconds: > HANG and > one watchdog tick (60s)

    local SESSION="sprawl-auq-idle-$(head -c4 /dev/urandom | xxd -p)"
    local STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"
    local WEAVE_STATE="$SPRAWL_ROOT/.sprawl/agents/weave.json"

    local PROBE="AUQ-IDLE-PROBE-$$-$(date +%s)"
    local PROBE_A="${PROBE}-aye"
    local PROBE_B="${PROBE}-bee"
    local PROBE_C="${PROBE}-cee"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo "  SPRAWL_BACKEND_HANG_TIMEOUT=$HANG  IDLE_WAIT=${IDLE_WAIT}s"

    wait_for_state_field_path() {
        local state_path="$1" field="$2" needle="$3" timeout="$4"
        local elapsed=0 value
        while [ "$elapsed" -lt "$timeout" ]; do
            if [ -f "$state_path" ]; then
                value=$(jq -r ".${field} // empty" "$state_path" 2>/dev/null || true)
                if [ -n "$value" ] && [[ "$value" == *"$needle"* ]]; then
                    return 0
                fi
            fi
            sleep 1
            elapsed=$((elapsed + 1))
        done
        return 1
    }

    echo ""
    echo "=== Launching sprawl enter with short D1 window ==="
    # Mirror e2e_launch_tui but inject SPRAWL_BACKEND_HANG_TIMEOUT so the D1
    # watchdog window is short enough to exercise in seconds.
    _stmux new-session -d -s "$SESSION" -x 200 -y 50 \
        "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_BACKEND_HANG_TIMEOUT='$HANG' '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
    _stmux set-option -t "$SESSION" window-size manual >/dev/null
    _stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null
    if ! wait_for_pattern "$SESSION" "weave \\(idle\\)" 45; then
        fail "TUI did not render 'weave (idle)' within 45s"
        capture_pane "$SESSION" | tail -30 >&2
        [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
        e2e_print_results
        return 1
    fi
    pass "TUI rendered ('weave (idle)' visible)"

    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3

    echo ""
    echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
    e2e_attach_phantom_client "$SESSION"

    echo ""
    echo "=== Driving weave to call ask_user_question ==="
    local WEAVE_PROMPT="Call mcp__sprawl__ask_user_question with questions=[{question:\"Idle-past-watchdog probe (${PROBE})\",multi_select:false,options:[{label:\"${PROBE_A}\"},{label:\"${PROBE_B}\"},{label:\"${PROBE_C}\"}]}]. Parse the QuestionResponse JSON, extract answers[0].selected[0], then call mcp__sprawl__report_status with state=working and summary set to that exact extracted label. Do nothing else."
    _stmux send-keys -t "$SESSION" "$WEAVE_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    echo ""
    echo "=== Waiting for the modal to appear ==="
    if wait_for_pattern "$SESSION" "is asking" 240; then
        pass "modal appeared ('is asking')"
    else
        fail "modal never appeared within 240s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    echo ""
    echo "=== Idling ${IDLE_WAIT}s with the modal UNANSWERED (past D1 window + a tick) ==="
    sleep "$IDLE_WAIT"

    echo ""
    echo "=== Asserting the modal survived the watchdog window ==="
    if capture_pane "$SESSION" | grep -qE "is asking"; then
        pass "modal still live after ${IDLE_WAIT}s idle (D1 did not guillotine the human-blocked turn)"
    else
        fail "modal disappeared after ${IDLE_WAIT}s idle — D1 watchdog likely fired (QUM-635 regression)"
        capture_pane "$SESSION" | tail -40 >&2
    fi

    if capture_pane "$SESSION" | grep -qiE "backend fault"; then
        fail "a 'backend fault' banner appeared during idle — D1 cancelled the human-blocked turn (QUM-635 regression)"
        capture_pane "$SESSION" | grep -iE "backend fault" >&2
    else
        pass "no backend-fault banner during idle"
    fi

    if [ -f "$WEAVE_STATE" ] && jq -e '.backend_fault // empty' "$WEAVE_STATE" >/dev/null 2>&1; then
        fail "weave state recorded a backend_fault during idle (QUM-635 regression)"
    else
        pass "weave state shows no backend fault"
    fi

    echo ""
    echo "=== Answering the modal (Down, Enter -> option 2: $PROBE_B) ==="
    _stmux send-keys -t "$SESSION" Down
    sleep 0.3
    _stmux send-keys -t "$SESSION" Enter

    echo ""
    echo "=== Waiting for weave to report the selected label (resume after re-seed) ==="
    if wait_for_state_field_path "$WEAVE_STATE" "last_report_message" "$PROBE_B" 240; then
        pass "weave reported '$PROBE_B' — answer round-tripped and the turn resumed cleanly post-idle"
    else
        fail "weave never reported '$PROBE_B' within 240s after answering"
        jq -r '.last_report_message // "<unset>"' "$WEAVE_STATE" 2>/dev/null >&2 || true
        capture_pane "$SESSION" | tail -40 >&2
    fi

    echo ""
    echo "=== Verifying modal indicator cleared after answer ==="
    sleep 3
    if capture_pane "$SESSION" | grep -qE "is asking"; then
        fail "statusbar still shows 'is asking' after answer"
    else
        pass "statusbar 'is asking' cleared after answer"
    fi

    # Wire-log evidence path (QUM-632) for the session, for forensic capture.
    local WIRELOG_DIR="$SPRAWL_ROOT/.sprawl/logs/sessions/weave"
    if [ -d "$WIRELOG_DIR" ]; then
        echo "  wire-log(s): $(find "$WIRELOG_DIR" -name '*.ndjson' 2>/dev/null | tr '\n' ' ')"
    fi

    e2e_print_results
}
