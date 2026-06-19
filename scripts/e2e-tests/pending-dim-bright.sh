#!/usr/bin/env bash
# scripts/e2e-tests/pending-dim-bright.sh — QUM-832 live gate.
#
# Proves the optimistic-render UX against the REAL claude through the
# interactive weave TUI: a follow-up prompt submitted while weave is busy
# renders IMMEDIATELY as a DIM (pending-zone) user bubble, then BRIGHTENS to
# normal styling — exactly once — when its isReplay consume echo settles it into
# the committed transcript. A recall (Ctrl+U) of a still-pending follow-up
# removes its dim bubble.
#
# How the dim/bright delta is asserted: lipgloss renders the committed (bright)
# user bubble with a BOLD attribute (SGR 1) and the pending (dim) bubble with a
# FAINT attribute (SGR 2). tmux's escape-preserving capture (`capture-pane -e`)
# normalizes the span as `\x1b[<attr>m\x1b[38;5;<fg>m<text>` — so we key on the
# attribute escape (`\x1b[2m` faint vs `\x1b[1m` bold) on the line carrying the
# sentinel. That is the actual rendered styling, not a proxy.
#
# Sequencing: submit the busy prompt, WAIT until its turn is visibly in flight
# (thinking/streaming) before submitting the follow-up — otherwise the two
# submits coalesce into one multi-line bubble. The follow-up then sits queued
# (dim) until the busy turn ends and the follow-up is consumed (bright).

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

# capture_pane_ansi SESSION — capture preserving SGR escapes.
capture_pane_ansi() {
    _stmux capture-pane -t "$1" -e -p 2>/dev/null || true
}

# sentinel_has_attr SESSION SENTINEL ATTR — true if the line carrying SENTINEL
# carries the SGR attribute escape \x1b[ATTRm (1=bold/bright, 2=faint/dim).
sentinel_has_attr() {
    capture_pane_ansi "$1" | grep -aF "$2" | grep -qaP "\x1b\[${3}m"
}

# wait_sentinel_attr SESSION SENTINEL ATTR TIMEOUT — poll until the sentinel
# line carries the given attribute.
wait_sentinel_attr() {
    local end=$((SECONDS + $4))
    while [ "$SECONDS" -lt "$end" ]; do
        if sentinel_has_attr "$1" "$2" "$3"; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

# wait_turn_busy SESSION TIMEOUT — wait until weave is visibly mid-turn so the
# follow-up lands as a queued (pending) submit, not a coalesced newline.
wait_turn_busy() {
    local end=$((SECONDS + $2))
    while [ "$SECONDS" -lt "$end" ]; do
        if capture_pane "$1" | grep -qiE "thinking…|Streaming|tool|^\s*1\."; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-dim-bright-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum832"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SUFFIX SESSION
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    SESSION="sprawl-dim-bright-e2e-${SUFFIX}"

    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo ""

    echo "=== Launching sprawl enter in tmux ==="
    if ! e2e_launch_tui "$SESSION" 200 50; then
        return 1
    fi
    pass "TUI rendered (weave root pill visible)"

    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION"

    local PENDING="PENDINGMSG${SUFFIX}"
    local BUSY1="List sixty short facts about the ocean, one fact per line, numbered."

    echo ""
    echo "=== Submit a busy turn, then a follow-up while it streams ==="
    e2e_send_user_prompt "$SESSION" "$BUSY1"
    if ! wait_turn_busy "$SESSION" 30; then
        fail "busy turn never started streaming within 30s"
        capture_pane "$SESSION" | tail -30 >&2
        e2e_print_results
        return 1
    fi
    # Now weave is mid-turn — the follow-up lands queued behind it.
    e2e_send_user_prompt "$SESSION" "$PENDING"

    # AC1: the follow-up appears IMMEDIATELY and DIM (faint, SGR 2) while queued.
    if wait_sentinel_attr "$SESSION" "$PENDING" 2 25; then
        pass "follow-up rendered immediately as a DIM (faint) pending bubble"
    else
        fail "follow-up '$PENDING' did not render dim/faint within 25s"
        capture_pane_ansi "$SESSION" | grep -aF "$PENDING" | cat -v >&2 || true
        capture_pane "$SESSION" | tail -20 >&2
        e2e_print_results
        return 1
    fi

    # AC2: when consumed (isReplay settles it into the committed transcript), the
    # bubble BRIGHTENS (bold, SGR 1).
    if wait_sentinel_attr "$SESSION" "$PENDING" 1 120; then
        pass "pending bubble BRIGHTENED to normal (bold) styling on settle"
    else
        fail "follow-up '$PENDING' never brightened (bold) within 120s"
        capture_pane_ansi "$SESSION" | grep -aF "$PENDING" | cat -v >&2 || true
        e2e_print_results
        return 1
    fi

    # AC3: rendered EXACTLY ONCE — no duplicate bubble from a double-enqueue.
    local count
    count="$(capture_pane "$SESSION" | grep -oF "$PENDING" | wc -l | tr -d ' ')"
    if [ "$count" = "1" ]; then
        pass "follow-up bubble rendered exactly once (no double-enqueue)"
    else
        fail "follow-up '$PENDING' rendered $count times on screen, want exactly 1"
        capture_pane "$SESSION" | tail -30 >&2
        e2e_print_results
        return 1
    fi

    sleep 2

    echo ""
    echo "=== Recall (Ctrl+U) removes a still-pending follow-up ==="
    local RECALL="RECALLMSG${SUFFIX}"
    local BUSY2="List forty short facts about outer space, one fact per line, numbered."
    e2e_send_user_prompt "$SESSION" "$BUSY2"
    if ! wait_turn_busy "$SESSION" 30; then
        fail "second busy turn never started streaming within 30s"
        capture_pane "$SESSION" | tail -30 >&2
        e2e_print_results
        return 1
    fi
    e2e_send_user_prompt "$SESSION" "$RECALL"

    if wait_sentinel_attr "$SESSION" "$RECALL" 2 25; then
        pass "recall follow-up rendered as a dim pending bubble (pre-recall)"
    else
        fail "recall follow-up '$RECALL' did not render dim within 25s"
        capture_pane_ansi "$SESSION" | grep -aF "$RECALL" | cat -v >&2 || true
        e2e_print_results
        return 1
    fi

    # Ctrl+U recalls every still-pending human prompt; the DIM bubble must
    # disappear. (Recall rehydrates the text into the INPUT box — plain styling,
    # no faint span — so we assert the dim/faint span vanishes, not raw text.)
    _stmux send-keys -t "$SESSION" C-u
    local end=$((SECONDS + 15)) gone=0
    while [ "$SECONDS" -lt "$end" ]; do
        if ! sentinel_has_attr "$SESSION" "$RECALL" 2; then
            gone=1
            break
        fi
        sleep 0.2
    done
    if [ "$gone" = "1" ]; then
        pass "Ctrl+U removed the dim pending bubble"
    else
        fail "dim pending bubble for '$RECALL' survived Ctrl+U recall"
        capture_pane_ansi "$SESSION" | grep -aF "$RECALL" | cat -v >&2 || true
        e2e_print_results
        return 1
    fi

    e2e_print_results
}
