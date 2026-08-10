#!/usr/bin/env bash
# scripts/e2e-tests/esc-interrupt-survives.sh — QUM-827 regression guard.
#
# The live interactive Esc-during-turn gate that was NEVER exercised (the
# idle-interrupt-inject row tests send_message(now=true) now-priority
# delivery to a CHILD; it does not press Esc mid-turn against the root weave
# session). After QUM-821, a bare Esc-abort while an async MCP tool handler is
# in flight cancelled that handler, whose ctx-cancelled error control_response
# drove the claude CLI to exit → "Session restarting" + resume churn.
#
# This drives the real path: prompt weave to call the ctx-respecting
# `_test_sleep` MCP tool (so a handler is genuinely in flight in the backend
# session's inflight map), press Esc mid-tool, and assert:
#   1. NO "Session restarting" banner appears (the session must survive).
#   2. The backend session is still usable: a SECOND prompt sent after the Esc
#      is answered live (a torn-down+resumed session would churn first).
#
# Requires SPRAWL_ENABLE_TEST_TOOLS=1 so `_test_sleep` is exposed (gated, see
# internal/sprawlmcp/tools.go).
#
# QUM-927 scope note for whoever touches this next. This row guards TWO distinct
# things in internal/runtime/unified.go, not one:
#   a. the Interrupt arm (`armed := inTurn || rt.frameTurnOpen`), which classifies
#      a turn-boundary Esc as a clean interrupt; and
#   b. the fault-surface gate in the SetTerminalErrorHandler closure
#      (`rt.inTurnLocked() || rt.frameTurnOpen`), which keeps a GENUINE backend
#      fault at that same boundary surfacing as EventTurnFailed instead of being
#      reclassified as a clean interrupt.
# (a) and (b) push in opposite directions — (a) suppresses an error surface, (b)
# preserves one — so a change to either needs this row AND the reducer-level
# tests in internal/tui/app_boundary_fault_test.go.
#
# FALSIFIABILITY WARNING (QUM-958): both gates below are weak, so a PASS here is
# not strong evidence.
#   * The queue-intact gate greps the pane for SURVIVE_${SUFFIX} — a string this
#     script itself types at e2e_send_user_prompt. The echoed prompt renders in
#     the pane, so the grep can match its own input rather than the model's reply.
#   * assert_no_session_fault is a NEGATIVE assertion: it passes when nothing
#     renders, which is also what a silently-dead session looks like. It
#     therefore cannot detect the QUM-927 regression class at all — "fault
#     surfaces nothing" is indistinguishable from "session survived" here.
# Treat a failure as informative and a pass as inconclusive until QUM-958 lands.

# QUM-1029: the number of assertions a COMPLETE, PASSING run of this row
# makes. Five pass sites, less the busy-label gate whose else only echoes a note ("proceeding").
MIN_ASSERTIONS=4

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

# assert_no_session_fault SESSION TIMEOUT
# Polls for TIMEOUT seconds; returns 1 (failure) the moment a session-fault
# surface appears — either the non-EOF "Session Error" γ-overlay dialog or the
# EOF auto-restart "Session restarting" banner. Returns 0 if neither shows
# during the window. Both are how a torn-down session manifests in the TUI.
assert_no_session_fault() {
    local session="$1" timeout="$2"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        if capture_pane "$session" | grep -qE "Session Error|Session restarting"; then
            return 1
        fi
        sleep 0.5
    done
    return 0
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-esc-interrupt-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum827"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"
    export SPRAWL_ENABLE_TEST_TOOLS=1

    local SUFFIX
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    local SESSION="sprawl-esc-interrupt-e2e-${SUFFIX}"
    local STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo "  SUFFIX=$SUFFIX"
    echo ""

    echo "=== Launching sprawl enter (test tools enabled) ==="
    _stmux new-session -d -s "$SESSION" -x 200 -y 50 \
        "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_CLAUDE='$SPRAWL_CLAUDE' SPRAWL_ENABLE_TEST_TOOLS=1 '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
    _stmux set-option -t "$SESSION" window-size manual >/dev/null
    _stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

    if wait_for_pattern "$SESSION" "weave " 45; then
        pass "TUI rendered (weave root visible)"
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
    e2e_attach_phantom_client "$SESSION"

    # --- Drive weave into a turn with an in-flight MCP tool handler. ---
    echo ""
    echo "=== Driving a turn that calls _test_sleep (in-flight async handler) ==="
    local SLEEP_PROMPT="Call the mcp__sprawl___test_sleep tool with seconds=20. Do not narrate. After it returns, reply with EXACTLY one line: SLEPT_${SUFFIX} and nothing else."
    e2e_send_user_prompt "$SESSION" "$SLEEP_PROMPT"

    # Wait until the tool call is in flight (the tool-call header renders live).
    if wait_for_pattern_fast "$SESSION" "_test_sleep" 45; then
        pass "_test_sleep tool call is in flight (rendered live)"
    else
        fail "_test_sleep tool call never appeared within 45s"
        capture_pane "$SESSION" | tail -40 >&2
        [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
        e2e_print_results
        return 1
    fi
    # Small settle so the handler is firmly registered in the inflight map.
    sleep 1

    # QUEUE a second prompt WHILE the turn is in flight (the user's exact repro:
    # "type from idle → message queues → Esc"). A turn is in flight, so this human
    # prompt is written to stdin and held pending. The locked interrupt contract:
    # Esc must NOT drop this queued message; the CLI consumes it on its next
    # iteration.
    echo ""
    echo "=== Queuing a second prompt while the turn is in flight ==="
    local SURVIVE_PROMPT="Reply with EXACTLY one line: SURVIVE_${SUFFIX} and nothing else."
    # Confirm weave is still busy (the in-flight _test_sleep turn) so this second
    # prompt QUEUES behind it. QUM-833 retired the "⏳ N queued" indicator, so we
    # key off the status-bar turn label (Streaming.../Thinking..., see
    # internal/tui/statusbar.go) instead — mirroring busy-queue-typing.sh /
    # sendnow-tui.sh. Best-effort: the consumed-after-abort assertion below is the
    # real queue-intact gate.
    if wait_for_pattern_fast "$SESSION" "Streaming\.\.\.|Thinking\.\.\." 10; then
        pass "weave still busy (in-flight turn) — second prompt queues behind it"
    else
        echo "  note: busy turn label not observed; proceeding to queue + abort gate" >&2
    fi
    e2e_send_user_prompt "$SESSION" "$SURVIVE_PROMPT"

    echo ""
    echo "=== Pressing Esc to abort the in-flight turn ==="
    _stmux send-keys -t "$SESSION" Escape

    # PRIMARY GATE: the backend session must survive. Under the QUM-827 bug the
    # interrupted turn surfaces the non-EOF "Session Error" dialog (or, on a
    # clean EOF, the "Session restarting" banner). We watch ~20s (the full
    # _test_sleep window) so a fault that fires when the handler would have
    # completed is still caught.
    if assert_no_session_fault "$SESSION" 20; then
        pass "no 'Session Error'/'Session restarting' after Esc — backend session survived"
    else
        fail "session-fault surface appeared after Esc — session was torn down (QUM-827 repro)"
        capture_pane "$SESSION" | tail -40 >&2
        [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
        e2e_print_results
        return 1
    fi

    # QUEUE-INTACT GATE: the prompt queued BEFORE the Esc must survive the abort
    # and be consumed on the next CLI iteration — its unique token is answered
    # live, with no restart. This proves the abort is queue-non-destructive AND
    # the session stays alive.
    echo ""
    echo "=== Asserting the pre-Esc queued prompt was consumed (queue intact) ==="
    if wait_for_pattern_fast "$SESSION" "SURVIVE_${SUFFIX}" 60; then
        pass "queued prompt consumed after Esc — session alive + queue intact (no resume churn)"
    else
        fail "queued prompt 'SURVIVE_${SUFFIX}' not answered within 60s (dropped by abort?)"
        capture_pane "$SESSION" | tail -60 >&2
        [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
        e2e_print_results
        return 1
    fi

    e2e_print_results
}
