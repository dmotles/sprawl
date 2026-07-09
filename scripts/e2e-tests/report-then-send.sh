#!/usr/bin/env bash
# scripts/e2e-tests/report-then-send.sh — QUM-866 deferred-teardown gate.
#
# Reproduces the race the QUM-866 fix closes: a child that calls
# report_status(state=complete) and THEN, in the SAME turn, send_message(parent,
# SENTINEL). Before the fix, report_status's teardown goroutine fired
# runtime.Stop immediately (mid-turn); Stop → drainInflight cancels the
# in-flight send_message handler, so the SENTINEL never reaches weave's maildir.
# With the fix, teardown is deferred to the genuine turn-end, so the SENTINEL is
# delivered before the subprocess is torn down.
#
# Unit tests pin the deferred-stop state machine; this e2e proves the race is
# actually closed end-to-end (no message loss) against a real claude binary.
#
# Phases:
#   1. spawn a child whose single prompt reports complete then send_messages a
#      unique SENTINEL to its parent (weave), in one turn.
#   2. assert the child reached Status=complete on disk (report landed).
#   3. assert the SENTINEL appears in weave's maildir (send_message survived the
#      teardown race — the QUM-866 invariant).

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# Find a non-weave child state.json whose .branch matches $1. Echoes path.
rts_find_child_by_branch() {
    local branch="$1" deadline=$((SECONDS + 180)) candidate name br
    while [ "$SECONDS" -lt "$deadline" ]; do
        for candidate in "$SPRAWL_ROOT"/.sprawl/agents/*.json; do
            [ -e "$candidate" ] || continue
            name=$(jq -r '.name // empty' "$candidate" 2>/dev/null || true)
            br=$(jq -r '.branch // empty' "$candidate" 2>/dev/null || true)
            if [ -n "$name" ] && [ "$name" != "weave" ] && [ "$br" = "$branch" ]; then
                printf '%s\n' "$candidate"
                return 0
            fi
        done
        sleep 1
    done
    return 1
}

# Wait for the child's state.json status field to equal $2 (timeout $3 sec).
rts_wait_status() {
    local state_file="$1" expected="$2" timeout="${3:-180}" elapsed=0 status=""
    while [ "$elapsed" -lt "$timeout" ]; do
        status=$(jq -r '.status // empty' "$state_file" 2>/dev/null || true)
        if [ "$status" = "$expected" ]; then
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

# Poll weave's maildir for the SENTINEL (timeout $2 sec). send_message writes
# the recipient's envelope under .sprawl/messages/<to>/{new,cur,archive}.
rts_wait_maildir_substring() {
    local needle="$1" timeout="${2:-120}" elapsed=0
    local maildir="$SPRAWL_ROOT/.sprawl/messages/weave"
    while [ "$elapsed" -lt "$timeout" ]; do
        if [ -d "$maildir" ] && grep -rqF "$needle" "$maildir" 2>/dev/null; then
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-report-then-send-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum866"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SESSION="sprawl-report-then-send-e2e-$(head -c4 /dev/urandom | xxd -p)"
    local SUFFIX
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    local SENTINEL="SENTINEL_${SUFFIX}"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo "  SENTINEL=$SENTINEL"

    echo ""
    echo "=== Launching sprawl enter ==="
    if ! e2e_launch_tui "$SESSION" 200 50; then
        e2e_print_results
        return 1
    fi
    pass "TUI rendered (weave root visible in header tree)"
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION"

    # ----- Phase 1: spawn a report-then-send child --------------------------
    echo ""
    echo "=== Phase 1: spawn child that reports complete THEN send_messages a sentinel ==="
    local BRANCH="qum866-report-then-send-${SUFFIX}"
    # The child prompt forces the exact ordering the bug is about: report
    # complete FIRST, then send_message in the SAME turn. The child's parent is
    # weave (the root), so send_message targets 'weave'.
    local PROMPT_BODY="You are a QUM-866 report-then-send probe. In a SINGLE turn, do exactly two tool calls in this order and nothing else: (1) call mcp__sprawl__report_status with state=complete and summary=\"qum866 probe done\"; (2) IMMEDIATELY after, call mcp__sprawl__send_message with to=\"weave\" and body=\"${SENTINEL}\". Do not write any files, do not call any other tools, and do not pause between the two calls."
    local SPAWN_PROMPT="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='$BRANCH', and prompt set to exactly: '$PROMPT_BODY'. Then reply 'SPAWN_${SUFFIX} ok' and nothing else."
    _stmux send-keys -t "$SESSION" "$SPAWN_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    local STATE NAME
    if ! STATE=$(rts_find_child_by_branch "$BRANCH"); then
        fail "P1: no child state appeared within 180s for branch $BRANCH"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    NAME=$(jq -r '.name' "$STATE")
    pass "P1: child spawned (name=$NAME)"

    # ----- Phase 2: child reached Status=complete ---------------------------
    # NOTE: P2 is a PRECONDITION, not the QUM-866 invariant. agentops.Report
    # writes disk Status=complete synchronously, BEFORE the teardown goroutine
    # runs, so P2 passes both pre- and post-fix. Only Phase 3 proves the fix.
    echo ""
    echo "=== Phase 2: child reports complete (precondition) ==="
    if rts_wait_status "$STATE" "complete" 180; then
        pass "P2: disk Status=complete after child reported state=complete"
    else
        local current
        current=$(jq -r '.status // empty' "$STATE" 2>/dev/null || true)
        fail "P2: disk Status did not reach 'complete' within 180s (got '$current')"
        cat "$STATE" >&2 2>/dev/null || true
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # ----- Phase 3: the SENTINEL survived the teardown race -----------------
    echo ""
    echo "=== Phase 3: send_message SENTINEL delivered to weave's maildir ==="
    # THE QUM-866 INVARIANT. Pre-fix, the send_message handler is cancelled by
    # drainInflight when the report_status teardown fires mid-turn, so the
    # SENTINEL never reaches weave's maildir → this phase FAILS. Post-fix,
    # teardown defers to turn-end and the SENTINEL is delivered.
    if rts_wait_maildir_substring "$SENTINEL" 150; then
        pass "P3: SENTINEL delivered to weave (send_message survived report_status teardown)"
    else
        fail "P3: SENTINEL '$SENTINEL' never reached weave's maildir within 150s — the follow-on send_message was lost to the teardown race (QUM-866 regression)"
        echo "  ---- weave maildir listing ----" >&2
        find "$SPRAWL_ROOT/.sprawl/messages/weave" -type f 2>/dev/null | head -40 >&2 || true
        echo "  ---- child state ----" >&2
        cat "$STATE" >&2 2>/dev/null || true
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    e2e_print_results
}
