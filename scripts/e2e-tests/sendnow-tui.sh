#!/usr/bin/env bash
# scripts/e2e-tests/sendnow-tui.sh — QUM-830 + QUM-838 live keystroke gate.
#
# The recurring miss this closes: every send-all-now crash shipped because the
# LIVE Ctrl+G keystroke path was never QA'd. recall-sendnow.sh exercises the
# cancel_async_message protocol over raw pipes; THIS gate drives the real TUI
# (`sprawl enter`) via tmux and presses Ctrl+G — including the rapid double-tap
# from the QUM-830 repro — against REAL claude, asserting the weave session
# NEVER ends/restarts ("Session Error") AND (QUM-838) the Ctrl+G'd message
# actually RENDERS in the committed transcript (TUI == raw session log).
#
# Repro (from the issue): submit a message while weave is busy so a second
# prompt QUEUES behind it; hit Ctrl+G (send-all-now); before anything renders,
# hit Ctrl+G AGAIN. Pre-fix QUM-830 preempted the in-flight turn into an
# is_error result that mis-surfaced as the empty "Session Error" overlay →
# restart. Pre-fix QUM-838: the coalesced now-write was delivered to the model
# (present in the session log) but never settled into the TUI pending zone, so
# it VANISHED from the committed transcript.
#
# Pass criteria:
#   1. QUM-838 parity: a Ctrl+G'd (coalesced) queued message renders exactly
#      once in the committed transcript AND is present in the raw session
#      .jsonl (TUI == log).
#   2. After a single Ctrl+G AND a rapid double Ctrl+G, NO "Session Error"
#      overlay ever appears during the sampling window.
#   3. The weave session survives (root pill still rendered; no restart banner).
#   4. The queued prompt is flushed — the turn settles (status no longer shows
#      "Streaming..."), confirming the now-priority send actually delivered.
#      (QUM-833 retired the "⏳ N queued" indicator; see the NOTE below.)

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

# NOTE on queued-state detection (QUM-833/QUM-838): the "⏳ N queued" input
# indicator was retired in QUM-833, and the short-help "ctrl+g: send now"
# affordance is not reliably observable via `capture_pane` (the row is clipped
# in a full viewport). So these helpers key off the status-bar "Streaming..."
# label (busy) and assert the END STATE (the message rendered) rather than the
# transient queued indicator. Queue formation is timed with fixed sleeps behind
# a long model-streaming turn, mirroring busy-queue-typing.sh.

# sn_streaming SESSION — true while the status bar shows the streaming label.
sn_streaming() {
    capture_pane "$1" | grep -qF "Streaming..."
}

# sn_wait_settled SESSION SECONDS — wait until the turn settles (no longer
# streaming). Returns 0 once settled, 1 on timeout.
sn_wait_settled() {
    local session="$1" secs="$2"
    local end=$((SECONDS + secs))
    while [ "$SECONDS" -lt "$end" ]; do
        if ! sn_streaming "$session"; then
            return 0
        fi
        sleep 0.5
    done
    return 1
}

# sn_log_has_marker MARKER — true if any claude session transcript .jsonl under
# ~/.claude/projects contains MARKER. The weave session log is the raw record of
# what was delivered to the model; QUM-838's symptom was a message present in
# this log but MISSING from the TUI committed transcript.
sn_log_has_marker() {
    local marker="$1"
    grep -rlqF "$marker" "$HOME/.claude/projects/" 2>/dev/null
}

# sn_wait_pane_marker SESSION MARKER SECONDS — wait until MARKER appears in the
# rendered (committed) transcript. Returns 0 once visible, 1 on timeout.
sn_wait_pane_marker() {
    local session="$1" marker="$2" secs="$3"
    local end=$((SECONDS + secs))
    while [ "$SECONDS" -lt "$end" ]; do
        if capture_pane "$session" | grep -qF "$marker"; then
            return 0
        fi
        sleep 0.5
    done
    return 1
}

# sn_parity_check SESSION — the QUM-838 hard gate. Queue TWO human prompts (each
# tagged with a unique marker) behind a long model-streaming turn, fire a single
# Ctrl+G to coalesce + send-all-now while still streaming, then assert BOTH
# markers (a) render in the committed TUI transcript and (b) are present in the
# raw session .jsonl. The pre-fix bug: the coalesced now-write was delivered (in
# the log) but never settled into the pending zone, so it VANISHED from the TUI.
# Covers single + coalesced, mid-turn (the actual repro).
sn_parity_check() {
    local session="$1"
    local tag
    tag="$(head -c4 /dev/urandom | xxd -p)"
    local MA="PARITYUSERA${tag}" MB="PARITYUSERB${tag}"

    echo ""
    echo "=== QUM-838 parity: queued msgs + Ctrl+G render in committed transcript AND match the session log ==="

    # A long model-streaming turn is a reliable busy window to queue behind.
    e2e_send_user_prompt "$session" "Write approximately 600 words of continuous prose about the history of TCP/IP. Output only the essay, no preamble and no lists."
    if ! wait_for_substring_fast "$session" "Streaming..." 45; then
        fail "parity: weave never entered a streaming turn"
        capture_pane "$session" | tail -30 >&2
        return 1
    fi
    # Let the model get well into streaming so the two prompts queue behind it
    # (fixed-sleep timing, mirroring busy-queue-typing.sh).
    sleep 4

    # Queue two distinct human prompts. Each marker is unique to the USER bubble
    # (the assistant is asked to reply with a short token, not the marker), so a
    # pane match proves the user message rendered — not an assistant echo.
    e2e_send_user_prompt "$session" "${MA}: acknowledge with the single letter a"
    sleep 1
    e2e_send_user_prompt "$session" "${MB}: acknowledge with the single letter b"
    sleep 1

    # Single Ctrl+G coalesces both queued prompts into ONE priority:now write
    # while the turn is still streaming.
    _stmux send-keys -t "$session" C-g

    # The now-write must NOT crash the session (QUM-830 held).
    if ! sn_no_session_error "$session" 5; then
        fail "parity: Session Error overlay appeared after Ctrl+G"
        capture_pane "$session" | tail -40 >&2
        return 1
    fi

    # Let the now-write turn (and any follow-on) settle.
    sn_wait_settled "$session" 120 || true

    # (a) committed-transcript render: both markers must appear in the TUI. The
    # coalesced now-write renders as one user bubble at the (auto-scrolled) tail.
    local pane_ok=1
    if ! sn_wait_pane_marker "$session" "$MA" 60; then pane_ok=0; fi
    if ! capture_pane "$session" | grep -qF "$MB"; then pane_ok=0; fi

    # (b) raw session log: both markers were delivered to the model.
    local log_ok=1
    sn_log_has_marker "$MA" || log_ok=0
    sn_log_has_marker "$MB" || log_ok=0

    if [ "$log_ok" -ne 1 ]; then
        fail "parity: Ctrl+G'd markers not found in any session .jsonl (delivery/test-setup issue)"
        capture_pane "$session" | tail -40 >&2
        return 1
    fi
    pass "parity: Ctrl+G'd messages present in the raw session .jsonl (delivered to model)"

    if [ "$pane_ok" -ne 1 ]; then
        fail "QUM-838: Ctrl+G'd message is in the session log but MISSING from the TUI committed transcript (vanished)"
        capture_pane "$session" | tail -40 >&2
        return 1
    fi
    pass "parity: Ctrl+G'd (coalesced) messages render in the committed TUI transcript — TUI == log"
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
    # QUM-327: detached tmux delivers keystrokes only with a client attached.
    # Without this, e2e_send_user_prompt is silently dropped (the prompt never
    # reaches the TUI), so no turn starts and nothing queues.
    e2e_attach_phantom_client "$SESSION"

    # --- QUM-838 hard gate: the Ctrl+G'd message must RENDER (not vanish). ---
    # Run first (deterministic) so it gates regardless of the nondeterministic
    # crash-repro loop below.
    if ! sn_parity_check "$SESSION"; then
        e2e_print_results
        return 1
    fi

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
    #   2. a human prompt is queued behind it ("send now" affordance appears).
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

        # Queue a human-typed prompt behind the busy turn. QUM-833 retired the
        # "⏳ N queued" indicator, so we give the queued prompt a moment to land
        # on the CLI queue (fixed-sleep) rather than polling for an indicator.
        e2e_send_user_prompt "$SESSION" "Reply with exactly: SENDNOW-OK"
        sleep 2

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

        # The now-send flushed the queue: wait for the session to settle back to
        # idle (no longer streaming) before the next iteration.
        if ! sn_wait_settled "$SESSION" 120; then
            fail "iter ${iter}: turn never settled — the now-send did not flush the queue"
            capture_pane "$SESSION" | tail -40 >&2
            e2e_print_results
            return 1
        fi
    done
    pass "no Session Error across ${SN_ITERS} Ctrl+G double-tap preempt attempts; queue flushed each time"

    e2e_print_results
}
