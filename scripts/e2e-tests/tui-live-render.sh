#!/usr/bin/env bash
# scripts/e2e-tests/tui-live-render.sh — QUM-826 live-render regression guard.
#
# This is the named gate that was SKIPPED on QUM-824 and let the live-render
# breakage through: a keystroke→submit→render loop driven against a REAL
# claude through the interactive weave TUI, asserting that BOTH the typed user
# prompt AND the streamed assistant response render LIVE — with NO restart.
#
# Why this catches the QUM-826 bug specifically: the pump-delivered reducers
# (UserMessageConsumedMsg / UserMessageCancelledMsg / AutoContinueMsg) returned
# `m, nil` without re-issuing m.bridge.WaitForEvent(). UserMessageConsumedMsg
# is the FIRST non-nil pump event of every typed turn, so the bubbletea event
# pump parked before any assistant content was read — assistant output never
# rendered live (only after a restart, via resume-replay). The matrix rows that
# passed (notify-tui, viewport-resync) drive state/maildir writes, not a live
# primary-stream keystroke turn, so they did not exercise this path.
#
# Anti-false-positive: the typed prompt is echoed into the user bubble (it
# renders locally on submit even under the bug). So the assistant assertion
# keys on a COMPUTED value that is NOT present in the prompt text — under the
# bug the computed answer never paints (pump parked), so the assertion fails;
# after the fix it appears.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-live-render-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum826"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    # Re-hydrate auth for the inner claude via the run-claude shim (QUM-518).
    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SUFFIX
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    local SESSION="sprawl-live-render-e2e-${SUFFIX}"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo "  SUFFIX=$SUFFIX"
    echo ""

    echo "=== Launching sprawl enter in tmux ==="
    if ! e2e_launch_tui "$SESSION" 200 50; then
        return 1
    fi
    pass "TUI rendered (weave root pill visible in header tree)"

    # Advance past any first-run trust prompt (QUM-310 gotcha).
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi

    # Let the first AgentTreeMsg tick land and the session initialize.
    sleep 3

    # QUM-327: detached tmux delivers input only with a client attached. Without
    # this the typed prompt below would silently never submit.
    e2e_attach_phantom_client "$SESSION"

    # The computed answer (42) is NOT present in the prompt text, so a pane
    # match on "ANS_<suffix>=42" can only come from the assistant's reply, never
    # from the echoed user bubble.
    local ANSWER="42"
    local SENTINEL="ANS_${SUFFIX}=${ANSWER}"
    local PROMPT="Reply with EXACTLY one line and nothing else: the literal text ANS_${SUFFIX}= immediately followed by the result of 40 plus 2. Do not restate the question."

    echo ""
    echo "=== Driving a live keystroke turn ==="
    e2e_send_user_prompt "$SESSION" "$PROMPT"

    # AC: the typed user prompt renders live (the prompt prefix surfaces in the
    # viewport after submit). This is local-append coverage, not the gate.
    if wait_for_substring_fast "$SESSION" "ANS_${SUFFIX}=" 20; then
        pass "user prompt rendered live (prompt prefix visible)"
    else
        fail "user prompt prefix 'ANS_${SUFFIX}=' did not render within 20s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    # LOAD-BEARING GATE: the streamed assistant response renders live, WITHOUT a
    # restart. Under the QUM-826 bug the pump parks on the first consumed event
    # and the computed answer never paints → this fails. After the fix it
    # appears. (Regex-anchored on the computed value absent from the prompt.)
    if wait_for_pattern_fast "$SESSION" "${SENTINEL}" 60; then
        pass "assistant response rendered LIVE (no restart) — '${SENTINEL}'"
    else
        fail "assistant sentinel '${SENTINEL}' did not render live within 60s (pump parked?)"
        capture_pane "$SESSION" | tail -60 >&2
        [ -f "${SPRAWL_ROOT}/.sprawl/tui-stderr.log" ] && tail -20 "${SPRAWL_ROOT}/.sprawl/tui-stderr.log" >&2
        e2e_print_results
        return 1
    fi

    e2e_print_results
}
