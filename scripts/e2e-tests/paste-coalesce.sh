#!/usr/bin/env bash
# scripts/e2e-tests/paste-coalesce.sh — QUM-608 paste-coalescer regression
# guard. Migrated from scripts/test-paste-coalesce-e2e.sh (see QUM-616 Wave 2A),
# which was deleted once this row proved flake-free (QUM-1183).
#
# Phase 1: inject a 200-char literal burst via `tmux send-keys -l` and assert
#          the QUM608HEAD…QUM608TAIL payload lands in the pane within 5s
#          (well below the ~30s typewriter-animation budget the bug produces).
# Phase 2: SIGINT the sprawl process and assert clean exit within 10s
#          (catches deadlocks in the coalescer's Close path).

# QUM-1029: the number of assertions a COMPLETE, PASSING run of this row
# makes. Four assertions on the green path; every failure path returns without
# reaching the aggregator.
#
# QUM-1277: 3 -> 4. Resolving the sprawl pid now records a `pass` of its own.
# It used to assert nothing on success, which is why this row could die at that
# step having measured nothing while its declared floor stayed satisfiable.
MIN_ASSERTIONS=4

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

test_run() {
    # The TUI must run as root weave, not as whatever identity the harness
    # inherited from its caller.
    unset SPRAWL_AGENT_IDENTITY

    e2e_recover_oauth_token
    e2e_setup_tmux_socket "sprawl-paste-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum608"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    local SESSION="sprawl-paste-e2e-$(head -c4 /dev/urandom | xxd -p)"

    # Deterministic 200-char payload: leading sentinel + 180×'a' filler +
    # trailing sentinel. The filler is all printable lowercase so the
    # coalescer's shouldWrapBurst heuristic (no ESC bytes) matches; without
    # the coalescer, tmux 3.2a would deliver these as 200 separate
    # KeyPressMsg events and the InputModel's per-rune handler would repaint
    # after each one — visible as typewriter animation.
    local PASTE_HEAD="QUM608HEAD"
    local PASTE_TAIL="QUM608TAIL"
    local PASTE_FILL
    PASTE_FILL=$(printf 'a%.0s' $(seq 1 180))
    local PASTE_BODY="${PASTE_HEAD}${PASTE_FILL}${PASTE_TAIL}"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo "  paste body length=${#PASTE_BODY}"
    echo ""

    # --- Launch the TUI (QUM-1181: converged onto the shared e2e_launch_tui
    # helper, which now forwards SPRAWL_CLAUDE itself — this row no longer
    # needs its own hand-rolled launch to get the auth shim). ---
    echo "=== Launching sprawl enter ==="
    if ! e2e_launch_tui "$SESSION" 240 50; then
        return 1
    fi
    pass "TUI rendered ('weave' root visible)"
    # QUM-608: zero out tmux escape-time so the coalescer sees pastes as one
    # contiguous burst (a non-zero escape-time can cause tmux to split ESC
    # sequences across reads and defeat the burst-detection heuristic).
    #
    # Code review (zone, F8): this now runs AFTER the render wait instead of
    # before it, since the wait moved inside e2e_launch_tui. Still correct —
    # the option only has to be in place before Phase 1's paste, which is
    # much later — but noted since this row's whole point is burst timing.
    _stmux set-option -t "$SESSION" escape-time 0 >/dev/null

    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 2

    echo ""
    echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
    e2e_attach_phantom_client "$SESSION"

    # Wait for weave session to be live (claude subprocess attached) so the
    # input panel is actually able to receive keystrokes.
    sleep 5

    # Capture the sprawl PID at or under the tmux pane's foreground process.
    #
    # QUM-1277: this used to walk `pgrep -P "$PANE_PID"` for a CHILD named
    # sprawl, which made the row a test of the pane shell. zsh (tmux's
    # default-shell on this host) exec-optimizes e2e_launch_tui's single command
    # string, so the pane pid IS sprawl and has no such child, and the row died
    # here before asserting anything about paste coalescing; dash and bash fork,
    # so the same row passed elsewhere. e2e_resolve_pane_process is
    # self-inclusive and recursive, so it never asks what the shell did.
    #
    # The needle is the binary's own basename, not the literal "sprawl":
    # e2e-matrix.sh rebuilds needs_build_tags rows as sprawl-matrix-<row>.
    local PANE_PID SPRAWL_PID="" RESOLVE_RC=0
    PANE_PID=$(_stmux display -t "$SESSION" -p '#{pane_pid}' 2>/dev/null || true)
    SPRAWL_PID=$(e2e_resolve_pane_process "$PANE_PID" "$(basename "$SPRAWL_BIN")") || RESOLVE_RC=$?
    if [ "$RESOLVE_RC" -eq 2 ]; then
        fail "tmux pane has no usable PID (pane_pid='$PANE_PID') — the pane is gone, so the sprawl process could not be looked for"
        return 1
    fi
    if [ "$RESOLVE_RC" -ne 0 ] || [ -z "$SPRAWL_PID" ]; then
        fail "could not locate sprawl process PID under tmux pane (pane_pid=$PANE_PID)"
        echo "  pane comm: $(cat "/proc/$PANE_PID/comm" 2>/dev/null || echo '<unreadable>')" >&2
        # Children read the same way the resolver reads them (not via pgrep):
        # this is a diagnostic for a /proc walk, so it should show what that
        # walk saw.
        echo "  pane children: $(cat "/proc/$PANE_PID/task/"*/children 2>/dev/null | tr '\n' ' ')" >&2
        echo "  looking for comm: $(basename "$SPRAWL_BIN")" >&2
        return 1
    fi
    pass "resolved sprawl PID=$SPRAWL_PID at or under tmux pane PID=$PANE_PID"

    # --- Phase 1: paste burst ---
    echo ""
    echo "=== Phase 1: inject 200-char burst into input panel ==="
    local PASTE_START_SECS=$SECONDS

    # `tmux send-keys -l` writes the literal bytes to the inner pty in one
    # write(2). Without the coalescer this surfaces as one KeyPressMsg per
    # byte; with the coalescer, the readLoop sees a single ~200B chunk and
    # wraps it in ESC[200~/ESC[201~, producing one tea.PasteMsg.
    _stmux send-keys -t "$SESSION" -l "$PASTE_BODY"

    # Head and tail are checked independently (grep -F per sentinel) because
    # the input panel renders the 200-char paste across multiple visual lines
    # and the tmux pane capture inserts line breaks at the panel's rendered
    # column boundary. Use -S -200 to capture scrollback so a paste pushed
    # below the visible viewport is still seen.
    local PASTE_END=$((SECONDS + 10))
    local PASTE_OK=0 pane_snapshot
    while [ "$SECONDS" -lt "$PASTE_END" ]; do
        pane_snapshot=$(capture_pane_scrollback "$SESSION" 200)
        if echo "$pane_snapshot" | grep -qF "$PASTE_HEAD" \
            && echo "$pane_snapshot" | grep -qF "$PASTE_TAIL"; then
            PASTE_OK=1
            break
        fi
        sleep 0.2
    done
    local PASTE_ELAPSED=$((SECONDS - PASTE_START_SECS))
    if [ "$PASTE_OK" -eq 1 ]; then
        pass "200-char paste body visible in pane within ${PASTE_ELAPSED}s (head+tail both present)"
    else
        fail "paste body did not appear within 10s — coalescer regression (typewriter behavior returned?)"
        capture_pane "$SESSION" | tail -40 >&2
        return 1
    fi

    # --- Phase 2: clean SIGINT shutdown ---
    echo ""
    echo "=== Phase 2: SIGINT and assert clean shutdown ==="
    # Send SIGINT directly to the sprawl process. Delivering via tmux keypress
    # would risk the input panel intercepting it as "clear input" since the
    # box is non-empty after Phase 1.
    kill -INT "$SPRAWL_PID"

    local SHUTDOWN_DEADLINE=$((SECONDS + 10))
    local SHUTDOWN_OK=0
    while [ "$SECONDS" -lt "$SHUTDOWN_DEADLINE" ]; do
        if ! kill -0 "$SPRAWL_PID" 2>/dev/null; then
            SHUTDOWN_OK=1
            break
        fi
        sleep 0.5
    done
    if [ "$SHUTDOWN_OK" -eq 1 ]; then
        pass "sprawl PID $SPRAWL_PID exited cleanly after SIGINT"
    else
        fail "sprawl PID $SPRAWL_PID did not exit within 10s of SIGINT — coalescer deadlocked Close path?"
        kill -KILL "$SPRAWL_PID" 2>/dev/null || true
    fi

    echo ""
    e2e_print_results
}
