#!/usr/bin/env bash
# scripts/e2e-tests/qum903-false-thinking.sh — QUM-903 live repro.
#
# QUM-903 adopts the CLI's authoritative session_state_changed wire signal as the
# in_turn ("is Claude working") authority via a 3-state machine (idle / submitted
# / running), replacing the frame-router heuristic that leaked a false "thinking"
# state when an autonomous turn (allocated on a system/init at resume) never
# produced a clean EndOfTurn.
#
# Two live assertions against a real claude binary:
#
#   SCENARIO A — normal turn shows running then clears on idle:
#     weave sends an idle child a benign PING. The child runs one turn. Its raw
#     NDJSON wire log must contain BOTH a session_state_changed:running and a
#     session_state_changed:idle frame around that turn — the exact signals the
#     reconcile machine keys on (running confirms in_turn; idle clears it).
#
#   SCENARIO B — resume into an idle agent does NOT stick in_turn=true (the
#   false-"thinking" repro):
#     weave pauses the child, then wakes it → the runtime resumes the claude
#     session with an autonomous system/init and NO real follow-on work. After a
#     settle window, weave peeks the child; the authoritative in_turn must be
#     false. Pre-QUM-903 the autonomous init flipped InTurn=true and leaked it.

# QUM-1029: the number of assertions a COMPLETE, PASSING run of this row
# makes. All eight sites are green-reachable; the final chain's elif and else arms both fail.
MIN_ASSERTIONS=8

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# wait_for_new_child polls .sprawl/agents for a child whose name is not already
# seen. Echoes "name|statefile"; returns 1 on timeout.
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

# QUM-1186: this row's readiness gate used to poll the child's state.json for
# the self-reported summary field, which is deleted along with the tool that
# wrote it. Readiness is now observed as a message the child actually
# delivered — see e2e_wait_maildir_substring in scripts/lib/e2e-common.sh.

# state_events emits, one per line, every session_state_changed .state value seen
# on the wire (dir=out, from claude) across all of the agent's NDJSON logs.
state_events() {
    local agent="$1" f
    for f in "$SPRAWL_ROOT"/.sprawl/logs/sessions/"$agent"/*.ndjson; do
        [ -f "$f" ] || continue
        jq -rc 'select(.dir=="out") | .raw | fromjson
                | select(.type=="system" and .subtype=="session_state_changed") | .state' \
            "$f" 2>/dev/null || true
    done
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-qum903-e2e"

    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum903"
    e2e_install_cleanup_traps

    git -C "$SPRAWL_ROOT" init -b main --quiet
    git -C "$SPRAWL_ROOT" -c user.name="Test" -c user.email="test@test" \
        commit --allow-empty -m "init" --quiet
    mkdir -p "$SPRAWL_ROOT/.sprawl"
    echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"
    [ -f "$REPO_ROOT/.env" ] && cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"

    local SESSION SUFFIX
    SESSION="sprawl-qum903-e2e-$(head -c4 /dev/urandom | xxd -p)"
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT  SESSION=$SESSION"

    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    echo "=== Launching sprawl enter ==="
    if ! e2e_launch_tui "$SESSION" 200 50; then
        return 1
    fi
    pass "TUI rendered (weave root visible)"
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION"

    echo ""
    echo "=== Spawning idle probe child ==="
    local PROBE CHILD_PROMPT SPAWN
    PROBE="You are an automated QUM-903 probe. STEP 1: IMMEDIATELY call mcp__sprawl__send_message with to=\"weave\" and body=\"PROBE-READY-${SUFFIX}\". STEP 2: Stop your turn and wait. Whenever a system-notification about an inbound message arrives, call mcp__sprawl__messages_read; if its body contains \"PING\", call mcp__sprawl__send_message with to=\"weave\" and body=\"PONG-${SUFFIX}\", then stop. On a resume/restart notification do NOT do any work — just call mcp__sprawl__send_message with to=\"weave\" and body=\"RESUMED-IDLE-${SUFFIX}\" and stop. Never read files, never run commands."
    SPAWN="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum903-probe-${SUFFIX}', and prompt set to exactly the following text (do not modify it): '${PROBE}'"
    e2e_send_user_prompt "$SESSION" "$SPAWN"

    local CHILD CHILD_NAME CHILD_STATE
    if ! CHILD=$(wait_for_new_child 180 weave); then
        fail "probe child did not spawn within 180s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results; return 1
    fi
    CHILD_NAME="${CHILD%%|*}"; CHILD_STATE="${CHILD#*|}"
    pass "probe child spawned (name=$CHILD_NAME)"

    if e2e_wait_maildir_substring weave "PROBE-READY-${SUFFIX}" 120; then
        pass "probe child reached idle (its readiness message was delivered to weave)"
    else
        fail "probe child's readiness message never reached weave within 120s"
        sed 's/^/    /' "$CHILD_STATE" >&2 2>/dev/null || true
        e2e_print_results; return 1
    fi
    sleep 3

    # ---------------------------------------------------------------------
    # SCENARIO A — normal turn: running then clears on idle (wire signals).
    # ---------------------------------------------------------------------
    echo ""
    echo "=== SCENARIO A: normal turn (PING → PONG), assert wire running+idle ==="
    e2e_send_user_prompt "$SESSION" \
        "Call mcp__sprawl__send_message with to='${CHILD_NAME}', body='PING', and now=false. Do nothing else."
    if wait_for_substring_fast "$SESSION" "PONG-${SUFFIX}" 120; then
        pass "child ran a normal turn (PONG ACK rendered in weave)"
    else
        fail "child PONG-${SUFFIX} did not appear within 120s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results; return 1
    fi
    sleep 3  # let the turn's trailing idle wire frame flush to the NDJSON log.

    local EVENTS RUN_N IDLE_N
    EVENTS="$(state_events "$CHILD_NAME")"
    RUN_N=$(printf '%s\n' "$EVENTS" | grep -cx "running" || true)
    IDLE_N=$(printf '%s\n' "$EVENTS" | grep -cx "idle" || true)
    echo "  wire session_state_changed for $CHILD_NAME: running=$RUN_N idle=$IDLE_N"
    if [ "$RUN_N" -ge 1 ] && [ "$IDLE_N" -ge 1 ]; then
        pass "SCENARIO A: turn showed session_state_changed:running then :idle on the wire"
    else
        fail "SCENARIO A: expected >=1 running AND >=1 idle wire frames (got running=$RUN_N idle=$IDLE_N)"
        e2e_print_results; return 1
    fi
    # QUM-1186: a `wait_for_child_report … || true; then :; fi` used to sit
    # here. Its result was discarded by the `|| true`, so it was a 30-second
    # sleep wearing the shape of a gate — it could not fail and asserted
    # nothing. Deleted rather than migrated; the `sleep 2` below is the settle
    # this step actually needed.
    sleep 2

    # ---------------------------------------------------------------------
    # SCENARIO B — resume into an idle agent must NOT stick in_turn=true.
    # ---------------------------------------------------------------------
    echo ""
    echo "=== SCENARIO B: pause then wake $CHILD_NAME (autonomous resume init, no work) ==="
    e2e_send_user_prompt "$SESSION" \
        "Call mcp__sprawl__pause with agent='${CHILD_NAME}', cascade=false, timeout_seconds=20. Do nothing else."
    local paused=0 i
    for i in $(seq 1 30); do
        if [ "$(jq -r '.status // empty' "$CHILD_STATE" 2>/dev/null || true)" = "paused" ]; then
            paused=1; break
        fi
        sleep 2
    done
    if [ "$paused" -eq 1 ]; then
        pass "SCENARIO B: child paused (disk status=paused)"
    else
        fail "SCENARIO B: child did not reach paused within 60s"
        sed 's/^/    /' "$CHILD_STATE" >&2 2>/dev/null || true
        e2e_print_results; return 1
    fi

    e2e_send_user_prompt "$SESSION" \
        "Call mcp__sprawl__wake with agent='${CHILD_NAME}'. Do nothing else."
    local woke=0
    for i in $(seq 1 45); do
        if [ "$(jq -r '.status // empty' "$CHILD_STATE" 2>/dev/null || true)" = "active" ]; then
            woke=1; break
        fi
        sleep 2
    done
    if [ "$woke" -eq 1 ]; then
        pass "SCENARIO B: child woke (disk status=active; autonomous resume init fired)"
    else
        fail "SCENARIO B: child did not return to active within 90s"
        sed 's/^/    /' "$CHILD_STATE" >&2 2>/dev/null || true
        e2e_print_results; return 1
    fi

    # Settle well past the 30s RecentActivityWindow so the ONLY thing that could
    # keep the child "working" is a genuinely stuck in_turn.
    echo "  settling 35s past the recent-activity window before reading in_turn..."
    sleep 35

    echo ""
    echo "=== SCENARIO B: peek the resumed child — in_turn must be false ==="
    e2e_send_user_prompt "$SESSION" \
        "Call mcp__sprawl__peek with agent='${CHILD_NAME}' and tail=1. Then reply with EXACTLY one line and nothing else: QUM903_INTURN=<the value of the in_turn field from the peek result, either true or false>."
    if wait_for_substring_fast "$SESSION" "QUM903_INTURN=false" 90; then
        pass "SCENARIO B: resumed idle child reports in_turn=false — false-\"thinking\" leak FIXED"
    elif capture_pane "$SESSION" | grep -qF "QUM903_INTURN=true"; then
        fail "SCENARIO B: resumed idle child stuck in_turn=true — false-\"thinking\" leak REPRODUCED"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results; return 1
    else
        fail "SCENARIO B: weave did not report QUM903_INTURN within 90s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results; return 1
    fi

    e2e_print_results
}
