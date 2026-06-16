#!/usr/bin/env bash
# scripts/e2e-tests/idle-interrupt-inject.sh — QUM-619 + QUM-821 guard.
#
# QUM-821 rewrote send_message(interrupt=true) content delivery: it no longer
# issues a bare Session.Interrupt for delivery. Instead the inbound message is
# written to the CLI stdin at priority "now" (cancel-and-replace urgency) via
# the cooperative WakeForDelivery path. The bare interrupt frame is reserved
# for Esc-abort only and never carries content.
#
# Two scenarios against a real claude binary:
#
#   PHASE 1 — idle recipient (QUM-619 regression):
#     weave sends send_message(interrupt=true) to an idle child; the now-priority
#     stdin write must wake it so it reads the message and ACKs. Pre-QUM-619 the
#     bare interrupt cancelled the just-injected notification turn and dropped it.
#
#   PHASE 2 — mid-turn recipient (QUM-821 urgency + storm regression gate):
#     The child is driven into a long single turn (foreground `sleep`). While
#     mid-turn, weave sends an urgent send_message(interrupt=true). Assertions:
#       (a) the child's urgent ACK reaches weave (now-priority preempts the
#           in-flight turn — empirically it ACKs well before the sleep ends).
#       (b) STORM REGRESSION GATE: the child's raw NDJSON shows a BOUNDED number
#           of now-priority stdin writes for that single urgent send. QUM-821
#           found that a now message yields no isReplay ack, so without the
#           synchronous mark-on-write fix PostTurnSweep re-injects it every turn
#           (~1990 writes / 1989 turns in 68s). One urgent send must produce a
#           handful of writes, not thousands.
#
# Esc-abort-carries-no-content is verified at the unit layer (QUM-821:
# TestInterrupt_CarriesNoContent + supervisor now-priority drain tests); the
# bare interrupt frame structurally cannot carry content.

# A single urgent now-send to a busy child should produce ~1 now-priority stdin
# write (empirically exactly 1 post-fix). Keep a tight bound to catch a partial
# re-inject regression; a storm is thousands, so even a few is suspicious.
NOW_WRITE_STORM_BOUND=5

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# wait_for_new_child polls .sprawl/agents for a child whose name is not already
# seen. Echoes "name|statefile" on success; returns 1 on timeout.
wait_for_new_child() {
    local timeout="$1"; shift
    local seen=" $* "
    local elapsed=0 candidate local_name
    while [ "$elapsed" -lt "$timeout" ]; do
        while IFS= read -r candidate; do
            [ -z "$candidate" ] && continue
            local_name=$(jq -r '.name // empty' "$candidate" 2>/dev/null || true)
            [ -z "$local_name" ] && continue
            [ "$local_name" = "weave" ] && continue
            case "$seen" in *" $local_name "*) continue ;; esac
            echo "${local_name}|${candidate}"
            return 0
        done < <(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null)
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

wait_for_child_idle() {
    local statefile="$1" marker="$2" timeout="$3"
    local end=$((SECONDS + timeout)) summary
    while [ "$SECONDS" -lt "$end" ]; do
        summary=$(jq -r '.last_report_message // empty' "$statefile" 2>/dev/null || true)
        if [ -n "$summary" ] && printf '%s' "$summary" | grep -qF "$marker"; then
            return 0
        fi
        sleep 1
    done
    return 1
}

# count_now_writes counts now-priority stdin user-message frames written to a
# child's raw NDJSON session log (the storm regression signal).
count_now_writes() {
    local agent="$1"
    local f
    f=$(ls "$SPRAWL_ROOT"/.sprawl/logs/sessions/"$agent"/*.ndjson 2>/dev/null | head -1)
    [ -z "$f" ] && { echo "-1"; return; }
    jq -rc 'select(.dir=="in") | .raw | fromjson | select(.type=="user" and .priority=="now") | 1' "$f" 2>/dev/null | wc -l | tr -d ' '
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-idleint-e2e"

    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum821"
    e2e_install_cleanup_traps

    git -C "$SPRAWL_ROOT" init -b main --quiet
    git -C "$SPRAWL_ROOT" -c user.name="Test" -c user.email="test@test" \
        commit --allow-empty -m "init" --quiet
    mkdir -p "$SPRAWL_ROOT/.sprawl"
    echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"
    [ -f "$REPO_ROOT/.env" ] && cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"

    local SESSION="sprawl-idleint-e2e-$(head -c4 /dev/urandom | xxd -p)"
    local PROBE="IDLE-INTERRUPT-PROBE-$$-$(date +%s)"
    local NOW_PROBE="URGENT-NOW-PROBE-$$-$(date +%s)"
    local SUFFIX1 SUFFIX2
    SUFFIX1="$(head -c4 /dev/urandom | xxd -p)"
    SUFFIX2="$(head -c4 /dev/urandom | xxd -p)"

    echo "  SPRAWL_ROOT=$SPRAWL_ROOT  SESSION=$SESSION"
    echo "  PROBE=$PROBE  NOW_PROBE=$NOW_PROBE"
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
    e2e_attach_phantom_client "$SESSION"

    # ---------------------------------------------------------------------
    # PHASE 1 — idle recipient (QUM-619 regression).
    # ---------------------------------------------------------------------
    echo ""
    echo "=== PHASE 1: spawn idle probe child ==="
    local P1_CHILD P1_SPAWN
    P1_CHILD="You are an automated QUM-821 idle probe. STEP 1: IMMEDIATELY call mcp__sprawl__report_status with state=\"working\" and summary=\"phase1 probe ready\". STEP 2: Stop your turn and wait. STEP 3 (next turn, on an inbound system-notification): call mcp__sprawl__messages_read. If the body contains \"${PROBE}\", call mcp__sprawl__messages_send with to=\"weave\" and body=\"IDLE-PROBE-ACK: <copy the body you just read here>\", then call mcp__sprawl__report_status state=\"complete\" summary=\"probe acked\". Then stop. Do nothing else; do not read files; do not run commands."
    P1_SPAWN="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum821-idle-${SUFFIX1}', and prompt set to exactly the following text (do not modify it): '${P1_CHILD}'"
    e2e_send_user_prompt "$SESSION" "$P1_SPAWN"

    local CHILD1 CHILD1_NAME CHILD1_STATE
    if ! CHILD1=$(wait_for_new_child 180 weave); then
        fail "phase-1 child did not spawn within 180s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results; return 1
    fi
    CHILD1_NAME="${CHILD1%%|*}"; CHILD1_STATE="${CHILD1#*|}"
    pass "phase-1 child spawned (name=$CHILD1_NAME)"

    if wait_for_child_idle "$CHILD1_STATE" "phase1 probe ready" 120; then
        pass "phase-1 child reached idle"
    else
        fail "phase-1 child never reported ready within 120s"
        sed 's/^/    /' "$CHILD1_STATE" >&2 2>/dev/null || true
        e2e_print_results; return 1
    fi
    sleep 3  # let the runtime fully park before the interrupt.

    echo ""
    echo "=== Driving weave to send interrupt=true (now-priority) to idle $CHILD1_NAME ==="
    e2e_send_user_prompt "$SESSION" \
        "Call mcp__sprawl__send_message with to='${CHILD1_NAME}', body='${PROBE}', and interrupt=true. Do nothing else. Do not read files, do not run commands."

    local ACK1_NEEDLE="From ${CHILD1_NAME} — mcp__sprawl__messages_read(id="
    if wait_for_substring_fast "$SESSION" "$ACK1_NEEDLE" 120; then
        pass "idle recipient woken via now-priority delivery (ACK rendered)"
    else
        fail "idle child ACK '$ACK1_NEEDLE...' did NOT appear within 120s"
        capture_pane "$SESSION" | tail -80 >&2
        sed 's/^/    /' "$CHILD1_STATE" >&2 2>/dev/null || true
        e2e_print_results; return 1
    fi

    # ---------------------------------------------------------------------
    # PHASE 2 — mid-turn recipient (QUM-821 urgency + storm regression gate).
    # ---------------------------------------------------------------------
    echo ""
    echo "=== PHASE 2: spawn mid-turn probe child ==="
    local BUSY_SECS=40
    local P2_CHILD P2_SPAWN
    P2_CHILD="You are an automated QUM-821 mid-turn probe. STEP 1: IMMEDIATELY call mcp__sprawl__report_status with state=\"working\" and summary=\"phase2 probe ready\". STEP 2: stop your turn. Whenever a system-notification about a new message arrives, call mcp__sprawl__messages_read. If the body contains \"GO-BUSY\", call the Bash tool to run exactly this foreground command: sleep ${BUSY_SECS}. If the body contains \"${NOW_PROBE}\", call mcp__sprawl__messages_send with to=\"weave\" and body=\"URGENT-NOW-ACK\". Do nothing else; do not read files."
    P2_SPAWN="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum821-midturn-${SUFFIX2}', and prompt set to exactly the following text (do not modify it): '${P2_CHILD}'"
    e2e_send_user_prompt "$SESSION" "$P2_SPAWN"

    local CHILD2 CHILD2_NAME CHILD2_STATE
    if ! CHILD2=$(wait_for_new_child 180 weave "$CHILD1_NAME"); then
        fail "phase-2 child did not spawn within 180s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results; return 1
    fi
    CHILD2_NAME="${CHILD2%%|*}"; CHILD2_STATE="${CHILD2#*|}"
    pass "phase-2 child spawned (name=$CHILD2_NAME)"

    if wait_for_child_idle "$CHILD2_STATE" "phase2 probe ready" 120; then
        pass "phase-2 child reached idle"
    else
        fail "phase-2 child never reported ready within 120s"
        sed 's/^/    /' "$CHILD2_STATE" >&2 2>/dev/null || true
        e2e_print_results; return 1
    fi
    sleep 3

    echo ""
    echo "=== Driving $CHILD2_NAME into a long mid-turn (GO-BUSY → sleep ${BUSY_SECS}) ==="
    e2e_send_user_prompt "$SESSION" \
        "Call mcp__sprawl__send_message with to='${CHILD2_NAME}', body='GO-BUSY', and interrupt=false. Do nothing else."
    echo "  waiting 14s for the child to enter its sleep turn"
    sleep 14
    local BUSY_START=$SECONDS

    echo ""
    echo "=== Sending urgent interrupt=true (now-priority) to mid-turn $CHILD2_NAME ==="
    e2e_send_user_prompt "$SESSION" \
        "Call mcp__sprawl__send_message with to='${CHILD2_NAME}', body='${NOW_PROBE}', and interrupt=true. Do nothing else. Do not read files, do not run commands."
    local URGENT_SENT=$SECONDS

    echo ""
    echo "=== PHASE 2a: mid-turn child's urgent ACK reaches weave ==="
    local ACK2_NEEDLE="From ${CHILD2_NAME} — mcp__sprawl__messages_read(id="
    if wait_for_substring_fast "$SESSION" "$ACK2_NEEDLE" 120; then
        local ACK_AT=$SECONDS
        pass "mid-turn recipient delivered the now-priority urgent message (ACK rendered)"
        echo "  EMPIRICAL: time-to-ACK from urgent-send=$((ACK_AT - URGENT_SENT))s, from busy-start≈$((ACK_AT - BUSY_START))s (sleep=${BUSY_SECS}s)"
        if [ "$((ACK_AT - BUSY_START))" -lt "$((BUSY_SECS - 8))" ]; then
            echo "  EMPIRICAL: ACK landed before the ${BUSY_SECS}s sleep would finish ⇒ 'now' PREEMPTED the in-flight turn."
        else
            echo "  EMPIRICAL: ACK landed at/after the ${BUSY_SECS}s sleep ⇒ 'now' reordered at the iteration boundary."
        fi
    else
        fail "mid-turn child urgent ACK '$ACK2_NEEDLE...' did NOT appear within 120s"
        capture_pane "$SESSION" | tail -80 >&2
        sed 's/^/    /' "$CHILD2_STATE" >&2 2>/dev/null || true
        e2e_print_results; return 1
    fi

    echo ""
    echo "=== PHASE 2b: STORM regression gate (bounded now-priority writes) ==="
    local NW
    NW=$(count_now_writes "$CHILD2_NAME")
    echo "  now-priority stdin writes to $CHILD2_NAME = $NW (bound ${NOW_WRITE_STORM_BOUND})"
    if [ "$NW" -lt 0 ]; then
        fail "could not read $CHILD2_NAME NDJSON to count now-writes"
        e2e_print_results; return 1
    elif [ "$NW" -eq 0 ]; then
        # The urgent send was confirmed delivered above (Phase 2a ACK), so the
        # child's NDJSON must contain at least one now-priority write; zero means
        # a jq/parse problem reading the log, not a storm.
        fail "0 now-priority writes counted despite a confirmed urgent delivery — NDJSON parse/read issue"
        e2e_print_results; return 1
    elif [ "$NW" -le "$NOW_WRITE_STORM_BOUND" ]; then
        pass "now-priority delivery is bounded ($NW writes) — no re-inject storm (QUM-821)"
    else
        fail "now-priority write count $NW exceeds bound ${NOW_WRITE_STORM_BOUND} — re-inject storm regression (QUM-821)"
        e2e_print_results; return 1
    fi

    e2e_print_results
}
