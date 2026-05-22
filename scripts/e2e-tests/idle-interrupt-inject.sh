#!/usr/bin/env bash
# scripts/e2e-tests/idle-interrupt-inject.sh — QUM-619 regression guard.
#
# Reproduces the idle-recipient interrupt race fixed in QUM-619:
#
#   1. weave spawns a child whose initial prompt instructs it to settle
#      idle (call report_status state="working"), and to respond to any
#      future message containing the sentinel "IDLE-INTERRUPT-PROBE" by
#      messages_send'ing an "IDLE-PROBE-ACK: <token>" reply to weave.
#   2. We wait for the child to reach idle (report_status emitted).
#   3. weave is driven to call `mcp__sprawl__send_message(..., interrupt=true)`
#      with the sentinel body — exactly the case where the pre-QUM-619
#      bug caused `Session.Interrupt` to cancel the just-injected
#      notification turn, silently dropping the message.
#   4. Primary assertion: weave's pane renders a drain-row citation for
#      the child's ACK reply within 90s. Pre-fix, the child never wakes,
#      so the citation never appears.
#
# This complements drain-row-inject.sh (interrupt=false / cooperative
# wake path) by exercising the interrupt=true / preempt path against an
# idle recipient.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-idleint-e2e"

    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum619"
    e2e_install_cleanup_traps

    git -C "$SPRAWL_ROOT" init -b main --quiet
    git -C "$SPRAWL_ROOT" -c user.name="Test" -c user.email="test@test" \
        commit --allow-empty -m "init" --quiet
    mkdir -p "$SPRAWL_ROOT/.sprawl"
    echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi

    local SESSION="sprawl-idleint-e2e-$(head -c4 /dev/urandom | xxd -p)"
    local PROBE="IDLE-INTERRUPT-PROBE-$$-$(date +%s)"
    local BRANCH_SUFFIX
    BRANCH_SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    local STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo "  PROBE=$PROBE"
    echo ""

    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    echo "=== Launching sprawl enter ==="
    if ! e2e_launch_tui "$SESSION" 200 50; then
        return 1
    fi
    pass "TUI rendered ('weave (idle)' visible in tree panel)"

    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3

    echo ""
    echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
    e2e_attach_phantom_client "$SESSION"

    echo ""
    echo "=== Driving weave to spawn the idle-interrupt probe child ==="
    # The child's initial prompt is a two-phase program: phase 1 emits a
    # report_status (so we can observe it reach idle); phase 2 reacts to
    # the incoming interrupt-flagged message by reading it and sending
    # back an ACK. Single-line shape (QUM-432 paste-classifier).
    local CHILD_PROMPT
    CHILD_PROMPT='You are an automated QUM-619 idle-interrupt probe. STEP 1: IMMEDIATELY call mcp__sprawl__report_status with state="working" and summary="idle-interrupt probe ready". STEP 2: Stop your turn and wait. STEP 3 (on the NEXT turn, triggered by an inbound system-notification): call mcp__sprawl__messages_read to retrieve the new message. If its body contains "IDLE-INTERRUPT-PROBE", extract the entire body verbatim, then call mcp__sprawl__messages_send with to="weave" and body="IDLE-PROBE-ACK: <copy the body you just read here>". Then call mcp__sprawl__report_status with state="complete" and summary="probe acked". Then stop. Do nothing else. Do not read any files, do not run any commands, do not call any other tools.'
    local SPAWN_PROMPT
    SPAWN_PROMPT="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum-619-idle-probe-${BRANCH_SUFFIX}', and prompt set to exactly the following text (do not modify it): '${CHILD_PROMPT}'"
    e2e_send_user_prompt "$SESSION" "$SPAWN_PROMPT"

    echo ""
    echo "=== Waiting for spawn to land (poll .sprawl/agents/ for new *.json) ==="
    local ELAPSED=0
    local SPAWN_LANDED=0
    local CHILD_STATE=""
    local CHILD_NAME=""
    while [ "$ELAPSED" -lt 180 ]; do
        local candidate local_name
        while IFS= read -r candidate; do
            [ -z "$candidate" ] && continue
            local_name=$(jq -r '.name // empty' "$candidate" 2>/dev/null || true)
            if [ -n "$local_name" ] && [ "$local_name" != "weave" ]; then
                CHILD_STATE="$candidate"
                CHILD_NAME="$local_name"
                SPAWN_LANDED=1
                break
            fi
        done < <(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null)
        [ "$SPAWN_LANDED" -eq 1 ] && break
        sleep 2
        ELAPSED=$((ELAPSED + 2))
    done
    if [ "$SPAWN_LANDED" -eq 1 ]; then
        pass "child spawned (name=$CHILD_NAME, state=$CHILD_STATE)"
    else
        fail "no non-weave state file appeared within 180s — weave's claude did not call spawn"
        echo "  agents dir:" >&2
        ls -la "$SPRAWL_ROOT/.sprawl/agents/" >&2 2>/dev/null || true
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    echo ""
    echo "=== Waiting for child to settle idle (report_status summary visible) ==="
    # Child reaches idle once it has emitted its phase-1 report_status and
    # claude has finished the initial turn. We poll state.json for a
    # non-empty summary that includes the "idle-interrupt probe ready"
    # marker we instructed it to set.
    local SETTLED=0
    local SETTLED_END=$((SECONDS + 120))
    while [ "$SECONDS" -lt "$SETTLED_END" ]; do
        local summary
        summary=$(jq -r '.last_report_message // empty' "$CHILD_STATE" 2>/dev/null || true)
        if [ -n "$summary" ] && printf '%s' "$summary" | grep -qF "idle-interrupt probe ready"; then
            SETTLED=1
            break
        fi
        sleep 1
    done
    if [ "$SETTLED" -eq 1 ]; then
        pass "child reached idle (report_status summary='idle-interrupt probe ready')"
    else
        fail "child never reported 'idle-interrupt probe ready' within 120s"
        echo "  child state:" >&2
        sed 's/^/    /' "$CHILD_STATE" >&2 2>/dev/null || echo "    <missing>" >&2
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    # Belt-and-suspenders: give the runtime a moment to flush its
    # post-turn bookkeeping and park on the queue signal. The QUM-619
    # bug fires when the recipient is FULLY parked, not just mid-flush.
    sleep 3

    echo ""
    echo "=== Driving weave to send interrupt-flagged message to $CHILD_NAME ==="
    local SEND_PROMPT
    SEND_PROMPT="Call mcp__sprawl__send_message with to='${CHILD_NAME}', body='${PROBE}', and interrupt=true. Do nothing else. Do not read files, do not run commands."
    e2e_send_user_prompt "$SESSION" "$SEND_PROMPT"

    echo ""
    echo "=== Primary assertion: child's ACK drain-row appears in weave's pane ==="
    # If QUM-619 fix is in place, the child wakes, reads the probe, and
    # sends back an "IDLE-PROBE-ACK: ..." message. weave will then render
    # a drain-row citation `From <child> — mcp__sprawl__messages_read(id=`
    # for the ACK message. Pre-fix, the child's notification turn is
    # cancelled by the interrupt and the ACK is never sent — citation
    # never appears.
    local ACK_NEEDLE="From ${CHILD_NAME} — mcp__sprawl__messages_read(id="
    if wait_for_substring_fast "$SESSION" "$ACK_NEEDLE" 120; then
        pass "child ACK drain-row '$ACK_NEEDLE...' appeared in weave's pane (QUM-619 idle+interrupt path live)"
    else
        fail "child ACK drain-row '$ACK_NEEDLE...' did NOT appear in weave's pane within 120s"
        echo "  QUM-619 race: interrupt likely cancelled the child's notification turn" >&2
        echo "  pane tail (80 lines):" >&2
        capture_pane "$SESSION" | tail -80 >&2
        echo "  child state:" >&2
        sed 's/^/    /' "$CHILD_STATE" >&2 2>/dev/null || echo "    <missing>" >&2
        echo "  child maildir new/:" >&2
        ls -la "$SPRAWL_ROOT/.sprawl/messages/${CHILD_NAME}/new/" >&2 2>/dev/null || echo "    <missing>" >&2
        echo "  child maildir cur/:" >&2
        ls -la "$SPRAWL_ROOT/.sprawl/messages/${CHILD_NAME}/cur/" >&2 2>/dev/null || echo "    <missing>" >&2
    fi

    e2e_print_results
}
