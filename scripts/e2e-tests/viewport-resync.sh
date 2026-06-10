#!/usr/bin/env bash
# scripts/e2e-tests/viewport-resync.sh — QUM-669 viewport-wedge-recovery
# regression guard. Exercises the EventBus Seq stamping + TUIAdapter gap
# detection + tui.AppModel resync state machine + JSONL replay + banner path
# end-to-end against a real claude child.
#
# Forge-mandated comment (design Q3): SPRAWL_DEBUG_GAP_INJECT=N is a
# TEST-ONLY debug seam read by tuiruntime.subscribe(). It causes the adapter
# to synthesize one EventDropDetectedMsg with Missing=N at the SECOND event
# of the session, exercising the wedge-recovery path end-to-end without
# needing to race a real slow subscriber. Not a public surface.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

test_run() {
    e2e_recover_oauth_token
    e2e_setup_tmux_socket "sprawl-viewport-resync-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum669-viewport-resync"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    # One-shot synthetic gap at the second event of the session.
    export SPRAWL_DEBUG_GAP_INJECT=15
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SESSION="sprawl-viewport-resync-$(head -c4 /dev/urandom | xxd -p)"
    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo "  SPRAWL_DEBUG_GAP_INJECT=$SPRAWL_DEBUG_GAP_INJECT"
    echo ""

    echo "=== Launching sprawl enter ==="
    if ! e2e_launch_tui "$SESSION" 200 50; then
        return 1
    fi
    pass "TUI rendered"

    # Trust-prompt dance (QUM-310).
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3

    echo ""
    echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
    e2e_attach_phantom_client "$SESSION"

    # Send a simple user prompt so the adapter receives a second event after
    # the session-init event. The synthetic gap fires on that second event.
    echo ""
    echo "=== Sending user prompt to flush events through the adapter ==="
    e2e_send_user_prompt "$SESSION" "Reply with exactly the word HELLO and nothing else."

    echo ""
    echo "=== Asserting resync banner appears in viewport ==="
    if wait_for_pattern_fast "$SESSION" "resynced.*recovered.*events" 120; then
        pass "QUM-669: resync banner observed after synthetic gap"
    else
        fail "QUM-669: resync banner never appeared"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -60 >&2
        echo "  stderr log tail:" >&2
        [ -f "$SPRAWL_ROOT/.sprawl/tui-stderr.log" ] && tail -40 "$SPRAWL_ROOT/.sprawl/tui-stderr.log" >&2
        e2e_print_results
        return 1
    fi

    # NOTE: The ⚠ status-bar chip (QUM-681 / DropTelemetry) fires on REAL
    # EventBus drops; our synthetic gap doesn't increment the bus drop
    # counter, so the chip may not surface in this scenario. We assert only
    # the banner + post-resync transcript preservation paths here.

    # Design §7 documents that the user message may NOT be in the resync if
    # the wirelog hasn't flushed yet ("the wire log flusher is best-effort.
    # If a drop coincides with an unflushed write, resync may rebuild a
    # transcript shorter than the in-memory state. Acceptable"). So we do
    # NOT assert the in-flight user prompt survived — only that the viewport
    # is no longer wedged. AC #4: the TUI must remain responsive.

    echo ""
    echo "=== Asserting viewport is no longer wedged (no stuck Streaming chip) ==="
    # After the resync, setTurnState(TurnIdle) is invoked unconditionally,
    # so the status bar must NOT continue to display "Streaming..." nor
    # "Thinking..." after the banner has been observed and any in-flight turn
    # has had a moment to settle.
    sleep 3
    if capture_pane "$SESSION" | grep -qE "Streaming\.\.\.|Thinking\.\.\."; then
        fail "QUM-669: status bar shows a wedged in-flight turn after resync"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    pass "QUM-669: no wedged turn-state chip after resync (AC #4)"

    # ---------------------------------------------------------------------
    # QUM-775 scenario: dropped-terminal-event recovery.
    #
    # Drop the first terminal SessionResultMsg of a fresh subscription via
    # SPRAWL_DEBUG_DROP_NEXT_TERMINAL_MSG=1 and shorten the TUI watchdog to
    # ~3s via SPRAWL_TUI_WATCHDOG_TIMEOUT_MS=3000. Without the watchdog, the
    # TUI would stay in TurnStreaming forever after the dropped terminal.
    # With the watchdog, finalizeTurn fires and the status bar clears within
    # the timeout window.
    # ---------------------------------------------------------------------
    echo ""
    echo "=== QUM-775: dropped-terminal-event watchdog recovery ==="
    # Tear down the QUM-669 session and give the parent sprawl process a
    # moment to release its weave.lock before we re-launch into a fresh
    # sandbox root for the next scenario.
    _stmux kill-session -t "$SESSION" 2>/dev/null || true
    sleep 2
    unset SPRAWL_DEBUG_GAP_INJECT
    export SPRAWL_DEBUG_DROP_NEXT_TERMINAL_MSG=1
    export SPRAWL_TUI_WATCHDOG_TIMEOUT_MS=3000

    # Fresh sandbox so we don't trip the previous run's weave.lock.
    local PRIOR_ROOT="$SPRAWL_ROOT"
    e2e_make_sandbox_root "sprawl-qum775-drop-terminal"
    e2e_init_sandbox_repo
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT (prior=$PRIOR_ROOT)"

    local SESSION775="sprawl-viewport-resync-775-$(head -c4 /dev/urandom | xxd -p)"
    echo "  SESSION=$SESSION775"
    echo "  SPRAWL_DEBUG_DROP_NEXT_TERMINAL_MSG=$SPRAWL_DEBUG_DROP_NEXT_TERMINAL_MSG"
    echo "  SPRAWL_TUI_WATCHDOG_TIMEOUT_MS=$SPRAWL_TUI_WATCHDOG_TIMEOUT_MS"

    if ! e2e_launch_tui "$SESSION775" 200 50; then
        return 1
    fi
    pass "QUM-775: TUI re-launched with dropped-terminal seam"

    if capture_pane "$SESSION775" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION775" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION775"

    echo ""
    echo "=== QUM-775: sending prompt to trigger a terminal event ==="
    e2e_send_user_prompt "$SESSION775" "Reply with exactly the word DONE and nothing else."

    # Give the turn time to complete + watchdog (3s timeout + 5s tick + slop).
    echo ""
    echo "=== QUM-775: waiting for watchdog to recover wedged turn ==="
    sleep 12

    if capture_pane "$SESSION775" | grep -qE "Streaming\.\.\.|Thinking\.\.\."; then
        fail "QUM-775: TUI still wedged after dropped terminal + watchdog window"
        echo "  pane tail:" >&2
        capture_pane "$SESSION775" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    pass "QUM-775: TUI recovered from dropped terminal event (no Streaming/Thinking)"

    _stmux kill-session -t "$SESSION775" 2>/dev/null || true

    e2e_print_results
}
