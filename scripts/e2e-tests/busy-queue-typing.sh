#!/usr/bin/env bash
# scripts/e2e-tests/busy-queue-typing.sh — QUM-828 live busy-typing gate.
#
# The THIRD interactive regression class (after QUM-340 single-slot and QUM-826
# dead-render) that slipped because the *live busy-primary-stream keystroke*
# path was never driven. This row drives `sprawl enter` via tmux send-keys
# against a REAL claude and exercises the unified submit path WHILE A TURN IS
# IN FLIGHT:
#
#   Scenario A — busy queue + live render (the headline):
#     Start a long turn (the model runs `sleep` via Bash, bypassPermissions),
#     then type TWO more prompts + Enter while it streams. Assert:
#       1. the input shows "⏳ 2 queued" (NOT "1 queued" — proves no single-slot
#          replace; the QUM-340 legacy path would have kept only the last typed).
#       2. after the busy turn ends, BOTH queued prompts are consumed and their
#          assistant answers render LIVE (no restart). Each answer keys on a
#          COMPUTED sentinel absent from the prompt text, so a pane match can
#          only come from the assistant reply — and the regression (single-slot)
#          would drop the first-typed prompt, so only one sentinel would paint.
#
#   Scenario B — Esc aborts the turn, queue survives:
#     Start a long turn, queue TWO prompts, press Esc. Assert the turn aborts
#     (Interrupting/Interrupted feedback) AND both queued prompts still execute
#     afterward — Esc is a bare contentless halt that leaves the queue intact
#     (QUM-828 §5 / QUM-827).
#
#   Scenario C — Ctrl+G send-all-now (soft):
#     Queue two prompts behind a busy turn, press Ctrl+G, observe a superseding
#     turn. Preempt timing on a tool-bound turn is nondeterministic (see
#     recall-sendnow.sh / QUM-821), so this is a soft note, not a hard gate —
#     the cancel+supersede invariants are pinned by recall-sendnow.sh.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

# bqt_queue_two_while_busy SESSION SUFFIX TAG — start a long busy turn then type
# two queued prompts with computed sentinels.
#
# The busy window is a long MODEL-GENERATION turn (a ~600-word essay), NOT a
# `sleep` tool: the sandbox's Bash safety guard blocks standalone `sleep`, so a
# tool-induced wait collapses instantly. A long prose generation keeps the turn
# in TurnStreaming for 10-20s — a wide, reliable window to type behind.
bqt_queue_two_while_busy() {
    local session="$1" suffix="$2" tag="$3"
    e2e_send_user_prompt "$session" "Write approximately 600 words of continuous prose explaining in detail how the TCP three-way handshake establishes a connection. Output only the essay, no preamble and no lists."
    # Give the turn time to enter streaming, then type two prompts behind it
    # while it is still generating. Each computes a value NOT present in its
    # own text, so a pane match can only come from the assistant reply.
    sleep 3
    e2e_send_user_prompt "$session" "Reply with EXACTLY one line and nothing else: the literal text ${tag}A_${suffix}= immediately followed by the result of 40 plus 2. Do not restate the question."
    sleep 1
    e2e_send_user_prompt "$session" "Reply with EXACTLY one line and nothing else: the literal text ${tag}B_${suffix}= immediately followed by the result of 60 plus 3. Do not restate the question."
    sleep 1
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-busy-queue-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum828"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    # Re-hydrate auth for the inner claude via the run-claude shim (QUM-518).
    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SUFFIX
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    local SESSION="sprawl-busy-queue-e2e-${SUFFIX}"

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
    sleep 3
    # QUM-327: detached tmux delivers input only with a client attached.
    e2e_attach_phantom_client "$SESSION"

    # =====================================================================
    # Scenario A: busy queue + live render
    # =====================================================================
    echo ""
    echo "=== Scenario A: type two prompts while busy → ⏳ 2 queued → both render live ==="
    bqt_queue_two_while_busy "$SESSION" "$SUFFIX" "QA"

    # AC1: the queued indicator shows TWO (not one — single-slot would show one).
    if wait_for_pattern_fast "$SESSION" "2 queued" 20; then
        pass "A1: input shows '⏳ 2 queued' while busy (no single-slot replace)"
    else
        fail "A1: '2 queued' indicator did not appear within 20s (busy submit not tracked?)"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    if capture_pane "$SESSION" | grep -qE "1 queued"; then
        # A transient "1 queued" between the two Enters is fine; only a STUCK
        # single slot is a failure, which A1 (2 queued) already disproves.
        echo "  note: observed a transient '1 queued' (expected between the two submits)" >&2
    fi

    # AC2: after the busy turn drains, BOTH queued answers render LIVE. The
    # single-slot regression would have dropped QAA (first typed), so requiring
    # BOTH sentinels is the load-bearing anti-regression assertion.
    if wait_for_pattern_fast "$SESSION" "QAA_${SUFFIX}=42" 120; then
        pass "A2: first queued prompt consumed + rendered LIVE ('QAA_${SUFFIX}=42')"
    else
        fail "A2: first queued sentinel 'QAA_${SUFFIX}=42' never rendered (single-slot drop?)"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    if wait_for_pattern_fast "$SESSION" "QAB_${SUFFIX}=63" 120; then
        pass "A2: second queued prompt consumed + rendered LIVE ('QAB_${SUFFIX}=63')"
    else
        fail "A2: second queued sentinel 'QAB_${SUFFIX}=63' never rendered"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    # No restart: a session restart would surface this transient banner.
    if capture_pane "$SESSION" | grep -qiE "session restart"; then
        fail "A2: a session-restart banner is visible — queued turns must render WITHOUT a restart"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    pass "A2: both queued turns rendered live with no session restart"

    # Let the queued indicator settle back to empty before the next scenario.
    sleep 2

    # =====================================================================
    # Scenario B: Esc aborts the turn, queue survives
    # =====================================================================
    echo ""
    echo "=== Scenario B: queue two while busy, press Esc → turn aborts, queue survives ==="
    bqt_queue_two_while_busy "$SESSION" "$SUFFIX" "QB"

    if ! wait_for_pattern_fast "$SESSION" "2 queued" 20; then
        fail "B: '2 queued' indicator did not appear before Esc"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    pass "B: two prompts queued behind the busy turn"

    # Bare Esc: abort the in-flight (sleep) turn. The queue must NOT be dropped.
    _stmux send-keys -t "$SESSION" Escape
    if wait_for_pattern_fast "$SESSION" "[Ii]nterrupt" 15; then
        pass "B: Esc surfaced interrupt feedback (turn aborting)"
    else
        echo "  note: did not observe an explicit interrupt banner (timing); continuing to the queue-survives gate" >&2
    fi

    # The load-bearing gate: both queued prompts still execute AFTER the abort.
    if wait_for_pattern_fast "$SESSION" "QBA_${SUFFIX}=42" 120 \
        && wait_for_pattern_fast "$SESSION" "QBB_${SUFFIX}=63" 120; then
        pass "B: both queued prompts executed after the Esc abort (queue survived)"
    else
        fail "B: a queued prompt was lost across the Esc abort (queue must survive a bare interrupt)"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    sleep 2

    # =====================================================================
    # Scenario C: Ctrl+G send-all-now (soft)
    # =====================================================================
    echo ""
    echo "=== Scenario C: queue two while busy, press Ctrl+G (send-all-now) ==="
    bqt_queue_two_while_busy "$SESSION" "$SUFFIX" "QC"
    if ! wait_for_pattern_fast "$SESSION" "2 queued" 20; then
        echo "  note: '2 queued' not observed before Ctrl+G; skipping soft supersede check" >&2
    else
        _stmux send-keys -t "$SESSION" C-g
        if wait_for_pattern_fast "$SESSION" "QC[AB]_${SUFFIX}=(42|63)" 120; then
            pass "C: a superseding turn ran after Ctrl+G (queued content resolved)"
        else
            echo "  note: Ctrl+G supersede not observed live (now-preempt timing is nondeterministic on a tool-bound turn — QUM-821); cancel+supersede invariants are pinned by recall-sendnow.sh" >&2
        fi
    fi

    e2e_print_results
}
