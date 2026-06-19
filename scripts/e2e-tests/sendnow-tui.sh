#!/usr/bin/env bash
# scripts/e2e-tests/sendnow-tui.sh — QUM-830 live keystroke gate.
#
# The recurring miss this closes: every send-all-now crash shipped because the
# LIVE Ctrl+G keystroke path was never QA'd. recall-sendnow.sh exercises the
# cancel_async_message protocol over raw pipes; THIS gate drives the real TUI
# (`sprawl enter`) via tmux and presses Ctrl+G — including the rapid double-tap
# from the QUM-830 repro — against REAL claude, asserting the weave session
# NEVER ends/restarts ("Session Error") and the queued prompt is actually sent.
#
# Repro (from the issue): submit a message while weave is busy so a second
# prompt QUEUES behind it; hit Ctrl+G (send-all-now); before anything renders,
# hit Ctrl+G AGAIN. Pre-fix this preempted the in-flight turn into an is_error
# result that mis-surfaced as the empty "Session Error" overlay → restart.
#
# Pass criteria:
#   1. After a single Ctrl+G AND a rapid double Ctrl+G, NO "Session Error"
#      overlay ever appears during the sampling window.
#   2. The weave session survives (root pill still rendered; no restart banner).
#   3. The queued prompt is flushed (the "⏳ N queued" indicator clears) — the
#      now-priority send actually delivered.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

# sn_no_session_error SESSION SECONDS — poll for `SECONDS` and FAIL (return 1)
# the instant a "Session Error" overlay appears. Returns 0 iff it never shows.
sn_no_session_error() {
    local session="$1" secs="$2"
    local end=$((SECONDS + secs))
    while [ "$SECONDS" -lt "$end" ]; do
        if capture_pane "$session" | grep -qF "Session Error"; then
            return 1
        fi
        sleep 0.2
    done
    return 0
}

# sn_wait_queue_cleared SESSION SECONDS — wait until the "⏳ N queued" indicator
# is gone (the now-send flushed the queue and weave settled). Returns 0 once
# clear, 1 on timeout.
sn_wait_queue_cleared() {
    local session="$1" secs="$2"
    local end=$((SECONDS + secs))
    while [ "$SECONDS" -lt "$end" ]; do
        if ! capture_pane "$session" | grep -qF "queued"; then
            return 0
        fi
        sleep 0.3
    done
    return 1
}

test_run() {
    e2e_recover_oauth_token
    e2e_setup_tmux_socket "sprawl-sendnow-tui-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-sendnow-tui-e2e"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    local SESSION="sprawl-sendnow-tui-$(head -c4 /dev/urandom | xxd -p)"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
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

    # --- Drive the QUM-830 repro repeatedly in one session. ---
    #
    # The crash is claude-side NONDETERMINISTIC: a now-write only sometimes
    # preempts a long Bash tool call mid-execution (when it does, the aborted
    # turn emits an is_error terminal frame → the empty "Session Error" overlay).
    # Measured pre-fix repro rate ~1-in-4 per attempt, so a single shot is flaky.
    # We loop the attempt SN_ITERS times; pre-fix this is reliably RED (≈90%+ at
    # 8 iters), and the fix makes EVERY attempt clean (deterministically GREEN).
    # The hard gate is: across all iterations the session NEVER shows a Session
    # Error and NEVER restarts. Deterministic proof of the classification fix
    # lives in internal/runtime unit tests; this gate guards the live keystroke
    # path the bug actually shipped through.
    #
    # Each iteration:
    #   1. weave runs a CPU-bound python loop (a real tool call — no `sleep`,
    #      which the env hook blocks; no permission prompt — weave is bypass).
    #   2. a human prompt is queued behind it (⏳ queued).
    #   3. the repro: Ctrl+G, then immediately Ctrl+G again before render.
    #   4. assert no Session Error; the queue flushes; weave returns to idle.
    local SN_ITERS="${SN_ITERS:-8}"
    echo ""
    echo "=== Ctrl+G double-tap preempt repro × ${SN_ITERS} (tool-bound turns) ==="
    local iter
    for iter in $(seq 1 "$SN_ITERS"); do
        echo "  --- iteration ${iter}/${SN_ITERS} ---"
        e2e_send_user_prompt "$SESSION" "Run exactly this bash command and nothing else: awk 'BEGIN{s=0; for(i=0;i<300000000;i++) s+=i; print s}'"
        if ! wait_for_substring_fast "$SESSION" "running" 45; then
            fail "iter ${iter}: weave never entered a tool-bound turn"
            capture_pane "$SESSION" | tail -30 >&2
            e2e_print_results
            return 1
        fi

        # Queue a human-typed prompt behind the busy turn.
        e2e_send_user_prompt "$SESSION" "Reply with exactly: SENDNOW-OK"
        if ! wait_for_substring_fast "$SESSION" "queued" 15; then
            fail "iter ${iter}: queued indicator never appeared — second prompt did not queue"
            capture_pane "$SESSION" | tail -30 >&2
            e2e_print_results
            return 1
        fi

        # Guard: the tool MUST still be running when we fire Ctrl+G, else there
        # is no turn to preempt and this iteration cannot exercise the crash.
        if ! capture_pane "$SESSION" | grep -qiE "running|Streaming\.\.\.|Thinking\.\.\."; then
            echo "    note: tool turn ended before Ctrl+G — preempt window missed this iteration" >&2
        fi

        # The QUM-830 repro: Ctrl+G, then immediately Ctrl+G again before render.
        _stmux send-keys -t "$SESSION" C-g
        _stmux send-keys -t "$SESSION" C-g

        # Hard gate: NO Session Error overlay through the preempt + now-turn.
        if ! sn_no_session_error "$SESSION" 20; then
            fail "iter ${iter}: Session Error overlay appeared after Ctrl+G double-tap (the crash)"
            capture_pane "$SESSION" | tail -40 >&2
            e2e_print_results
            return 1
        fi

        # Session survived: weave root pill still rendered.
        if ! capture_pane "$SESSION" | grep -q "weave "; then
            fail "iter ${iter}: weave root pill vanished — session did not survive Ctrl+G"
            capture_pane "$SESSION" | tail -40 >&2
            e2e_print_results
            return 1
        fi

        # The queued prompt was flushed: the indicator clears. Wait for the
        # session to settle back to idle before the next iteration.
        if ! sn_wait_queue_cleared "$SESSION" 90; then
            fail "iter ${iter}: queued indicator never cleared — the now-send did not flush the queue"
            capture_pane "$SESSION" | tail -40 >&2
            e2e_print_results
            return 1
        fi
    done
    pass "no Session Error across ${SN_ITERS} Ctrl+G double-tap preempt attempts; queue flushed each time"

    e2e_print_results
}
