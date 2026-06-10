#!/usr/bin/env bash
# scripts/e2e-tests/wake-live.sh — QUM-606 / QUM-724 / QUM-744 wake-lifecycle
# regression guard.
#
# Build tag: needs_build_tags=sprawl_test so the build-tag-gated
# `mcp__sprawl___test_induce_wedge` MCP tool is compiled in.
#
# Scenarios (per QUM-724 §1-5 / QUM-744):
#   1. Wake from faulted   : spawn engineer, induce SubscriberWedge fault,
#                            drive `mcp__sprawl__wake`, assert NEW
#                            claude --resume subprocess (PID ≠ original) is
#                            alive 2s after wake returns, then drive a
#                            post-wake send_message; sentinel must land in
#                            the child's activity.ndjson within 60s.
#   2. Wake from paused    : pause an idle engineer, drive wake, assert
#                            success ack with mode=resumed and the agent
#                            comes back active.
#   3. Wake from died      : SIGKILL the claude PID, wait for liveness=died,
#                            wake. **SKIPPED** — gated on QUM-760 (the EOF
#                            on idle-agent claude exit does not currently
#                            promote to setTerminalErr, so liveness=died is
#                            unreachable for an idle SIGKILL via the live
#                            wiring; the supervisor classifier itself is unit
#                            tested at runtime_durable_fault_test.go). Mirrors
#                            the QUM-735 pause-lifecycle P4 SKIP gate.
#   4. Wake fallback       : pause an idle engineer, delete its claude
#                            session transcript (~/.claude/projects/.../*.jsonl)
#                            so `claude --resume <sid>` prints
#                            "No conversation found with session ID:" on
#                            stderr → OnResumeFailure → wake falls back to
#                            fresh. Assert mode=fresh and a new session_id
#                            (≠ original) appears on disk.
#   5. Wake no-op (healthy): drive wake on a healthy running agent and
#                            assert the success ack mentions "already
#                            running; no wake needed" (ErrWakeNotNeeded
#                            surfaced as success).

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1 needs_build_tags=sprawl_test"
}

# ---------------------------------------------------------------------------
# Helpers (mirror the conventions used by paused-persistence.sh /
# pause-lifecycle.sh; intentionally NOT factored into a shared lib so each
# e2e remains hermetic and editable in isolation).
# ---------------------------------------------------------------------------

wake_find_child_by_branch() {
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

wake_wait_active() {
    local state_path="$1" timeout="${2:-90}" elapsed=0 status=""
    while [ "$elapsed" -lt "$timeout" ]; do
        status=$(jq -r '.status // empty' "$state_path" 2>/dev/null || true)
        [ "$status" = "active" ] && return 0
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

wake_wait_status() {
    local state_path="$1" expected="$2" timeout="${3:-30}" elapsed=0 status=""
    while [ "$elapsed" -lt "$timeout" ]; do
        status=$(jq -r '.status // empty' "$state_path" 2>/dev/null || true)
        [ "$status" = "$expected" ] && return 0
        sleep 1
        elapsed=$((elapsed + 1))
    done
    return 1
}

wake_pgrep_claude() {
    local sid="$1"
    command -v pgrep >/dev/null 2>&1 || return 0
    pgrep -af 'claude' 2>/dev/null | awk -v sid="$sid" '$0 ~ sid {print $1; exit}'
}

wake_session_id() {
    local state_path="$1"
    jq -r '.session_id // empty' "$state_path" 2>/dev/null || true
}

# wake_send_one_line SESSION PROMPT
# Stuff a single-shot prompt into the TUI input and submit.
wake_send_one_line() {
    local session="$1" prompt="$2"
    _stmux send-keys -t "$session" "$prompt"
    sleep 0.5
    _stmux send-keys -t "$session" Enter
}

# wake_drive_wake SESSION CHILD TOKEN [EXPECTED_PATTERN] [TIMEOUT]
# Drives `mcp__sprawl__wake` for CHILD and waits for either the supplied
# pattern (default: any wake ack mentioning mode or no-wake-needed) within
# TIMEOUT seconds. TOKEN gates against stale scrollback false matches.
wake_drive_wake() {
    local session="$1" child="$2" token="$3"
    local pattern="${4:-${token}_DONE}"
    local timeout="${5:-90}"
    local prompt
    prompt="Call mcp__sprawl__wake with agent_name='$child'. Then reply with EXACTLY one line: '${token}_DONE wake_result=<<<' followed by the verbatim tool response text followed by '>>>' and stop."
    wake_send_one_line "$session" "$prompt"
    wait_for_pattern_fast "$session" "$pattern" "$timeout"
}

# ---------------------------------------------------------------------------
# test_run
# ---------------------------------------------------------------------------

test_run() {
    if ! command -v pgrep >/dev/null 2>&1; then
        echo "FATAL: pgrep not on PATH" >&2
        return 1
    fi

    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-wake-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum744"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi

    export SPRAWL_ENABLE_TEST_TOOLS=1
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SUFFIX
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    local SESSION="sprawl-wake-e2e-${SUFFIX}"
    local STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"
    local SPAWN_PROMPT_TEMPLATE
    SPAWN_PROMPT_TEMPLATE="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='__BRANCH__', and prompt set to exactly: 'You are an automated QUM-744 wake-lifecycle probe. Call mcp__sprawl__report_status with state=working, summary=\"idle, awaiting signal\". Then stop and wait. Do nothing else until you receive a message.'"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo "  SUFFIX=$SUFFIX"

    # Custom launch (not e2e_launch_tui) so we can inject SPRAWL_CLAUDE /
    # SPRAWL_ENABLE_TEST_TOOLS into the spawned shell — needed for S1's
    # build-tag-gated _test_induce_wedge MCP tool.
    echo ""
    echo "=== Launching sprawl enter ==="
    _stmux new-session -d -s "$SESSION" -x 200 -y 50 \
        "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_CLAUDE='$SPRAWL_CLAUDE' SPRAWL_ENABLE_TEST_TOOLS=1 '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
    _stmux set-option -t "$SESSION" window-size manual >/dev/null
    _stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

    if wait_for_pattern "$SESSION" "weave " 45; then
        pass "TUI rendered (weave root visible)"
    else
        fail "TUI did not render within 45s"
        capture_pane "$SESSION" | tail -30 >&2
        [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
        e2e_print_results
        return 1
    fi
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION"

    # =====================================================================
    # Scenario 1: Wake from faulted
    # =====================================================================
    echo ""
    echo "=== Scenario 1: Wake from faulted (induce SubscriberWedge → wake) ==="
    local S1_BRANCH="qum744-s1-faulted-${SUFFIX}"
    local S1_PROMPT="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$S1_BRANCH}"
    wake_send_one_line "$SESSION" "$S1_PROMPT"

    local S1_STATE S1_NAME
    if ! S1_STATE=$(wake_find_child_by_branch "$S1_BRANCH"); then
        fail "S1: child state did not appear within 180s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    S1_NAME=$(jq -r '.name' "$S1_STATE")
    pass "S1: child spawned (name=$S1_NAME)"

    if ! wake_wait_active "$S1_STATE" 90; then
        fail "S1: child never reached active"
        e2e_print_results
        return 1
    fi
    local S1_SID
    S1_SID=$(wake_session_id "$S1_STATE")
    if [ -z "$S1_SID" ]; then
        fail "S1: session_id never materialized"
        e2e_print_results
        return 1
    fi
    local S1_PID
    S1_PID=$(wake_pgrep_claude "$S1_SID")
    if [ -z "$S1_PID" ]; then
        fail "S1: could not locate original claude PID for sid=$S1_SID"
        pgrep -af claude >&2 || true
        e2e_print_results
        return 1
    fi
    pass "S1: child active (sid=$S1_SID, pid=$S1_PID)"

    local S1_INDUCE="Call mcp__sprawl___test_induce_wedge with agent_name='$S1_NAME', fault_class='subscriber_wedged'. Confirm in your reply that the call succeeded with token S1_INDUCE_${SUFFIX}."
    wake_send_one_line "$SESSION" "$S1_INDUCE"
    if wait_for_pattern_fast "$SESSION" "S1_INDUCE_${SUFFIX}|Induced subscriber_wedged|SubscriberWedge" 60; then
        pass "S1: fault induction tool returned"
    else
        fail "S1: fault induction tool did not surface within 60s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    if ! wake_drive_wake "$SESSION" "$S1_NAME" "S1_WAKE_${SUFFIX}" '"mode":"resumed"|"mode": "resumed"' 90; then
        fail "S1: mode=resumed ack did not appear within 90s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    pass "S1: wake returned mode=resumed"

    # PRIMARY: a new claude --resume subprocess (PID ≠ S1_PID) must exist.
    sleep 2
    local S1_NEW_PID="" probe_end=$((SECONDS + 12))
    while [ "$SECONDS" -lt "$probe_end" ]; do
        S1_NEW_PID=$(pgrep -af 'claude' | awk -v sid="$S1_SID" -v origpid="$S1_PID" '$0 ~ "--resume" && $0 ~ sid && $1 != origpid {print $1; exit}' || true)
        [ -n "$S1_NEW_PID" ] && break
        sleep 0.5
    done
    if [ -z "$S1_NEW_PID" ]; then
        fail "S1 PRIMARY: no live claude --resume subprocess for sid=$S1_SID 2s after wake"
        pgrep -af claude | head -20 >&2 || true
        e2e_print_results
        return 1
    fi
    if [ "$S1_NEW_PID" = "$S1_PID" ]; then
        fail "S1 PRIMARY: new claude PID ($S1_NEW_PID) equals original ($S1_PID) — wake did not swap subprocess"
        e2e_print_results
        return 1
    fi
    if ! kill -0 "$S1_NEW_PID" 2>/dev/null; then
        fail "S1 PRIMARY: new claude PID $S1_NEW_PID does not respond to signal 0"
        e2e_print_results
        return 1
    fi
    pass "S1: new claude --resume PID=$S1_NEW_PID alive (was $S1_PID)"

    # Post-wake turn — sentinel must round-trip through activity.ndjson.
    local S1_PROBE="WAKE-S1-PROBE-${SUFFIX}"
    local S1_TURN="Call mcp__sprawl__send_message with to='$S1_NAME', body='Echo ${S1_PROBE} verbatim in your next reply and then call report_status complete.', interrupt=false."
    wake_send_one_line "$SESSION" "$S1_TURN"
    local S1_ACT="$SPRAWL_ROOT/.sprawl/agents/$S1_NAME/activity.ndjson"
    local act_end=$((SECONDS + 60)) act_seen=0
    while [ "$SECONDS" -lt "$act_end" ]; do
        if [ -f "$S1_ACT" ] && grep -qF "$S1_PROBE" "$S1_ACT"; then
            act_seen=1
            break
        fi
        sleep 1
    done
    if [ "$act_seen" -eq 1 ]; then
        pass "S1: post-wake turn produced frame with sentinel '$S1_PROBE'"
    else
        fail "S1: post-wake turn did NOT surface sentinel '$S1_PROBE' in activity within 60s"
        [ -f "$S1_ACT" ] && tail -20 "$S1_ACT" >&2 || echo "    <activity file missing>" >&2
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # =====================================================================
    # Scenario 2: Wake from paused
    # =====================================================================
    echo ""
    echo "=== Scenario 2: Wake from paused ==="
    local S2_BRANCH="qum744-s2-paused-${SUFFIX}"
    local S2_PROMPT="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$S2_BRANCH}"
    wake_send_one_line "$SESSION" "$S2_PROMPT"

    local S2_STATE S2_NAME
    if ! S2_STATE=$(wake_find_child_by_branch "$S2_BRANCH"); then
        fail "S2: child state did not appear within 180s"
        e2e_print_results
        return 1
    fi
    S2_NAME=$(jq -r '.name' "$S2_STATE")
    pass "S2: child spawned (name=$S2_NAME)"

    if ! wake_wait_active "$S2_STATE" 90; then
        fail "S2: child never reached active"
        e2e_print_results
        return 1
    fi
    pass "S2: child active"

    local S2_PAUSE="Call mcp__sprawl__pause with agent='$S2_NAME', cascade=false, timeout_seconds=15. Then reply with EXACTLY one line: 'S2_PAUSE_${SUFFIX} ack' and stop."
    wake_send_one_line "$SESSION" "$S2_PAUSE"
    if ! wait_for_pattern_fast "$SESSION" "Agent $S2_NAME paused cleanly" 60; then
        fail "S2: pause ack ('Agent $S2_NAME paused cleanly') did not appear within 60s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    if ! wake_wait_status "$S2_STATE" "paused" 15; then
        fail "S2: disk Status did not reach 'paused' within 15s"
        cat "$S2_STATE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    pass "S2: child paused (disk Status=paused)"

    if ! wake_drive_wake "$SESSION" "$S2_NAME" "S2_WAKE_${SUFFIX}" '"mode":"resumed"|"mode": "resumed"' 90; then
        fail "S2: wake mode=resumed ack did not appear within 90s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    pass "S2: wake returned mode=resumed"

    if ! wake_wait_status "$S2_STATE" "active" 30; then
        local cur
        cur=$(jq -r '.status // empty' "$S2_STATE" 2>/dev/null || true)
        fail "S2: post-wake disk Status='$cur' (want 'active')"
        e2e_print_results
        return 1
    fi
    pass "S2: post-wake disk Status=active"

    # =====================================================================
    # Scenario 3: Wake from died — SKIPPED (gated on QUM-760).
    # =====================================================================
    echo ""
    echo "=== Scenario 3: Wake from died — SKIPPED ==="
    echo "  SKIP: S3 — external SIGKILL of an idle claude PID does NOT currently"
    echo "        flip the supervisor's liveness to died via the live wiring"
    echo "        (session reader EOF does not promote to setTerminalErr, so the"
    echo "        TurnLoop ctx never cancels and watchHandleExit is structurally"
    echo "        blind). The supervisor's three-way died classifier is unit"
    echo "        tested at internal/supervisor/runtime_durable_fault_test.go"
    echo "        :: TestWatchHandleExit_UnexpectedExitClassifiesAsDied. The live"
    echo "        wiring gap is tracked by QUM-760 (Done on a sibling branch;"
    echo "        flip this SKIP once QUM-760's fix lands on this branch)."
    echo "        Mirrors the QUM-735 pause-lifecycle P4 SKIP gate. (QUM-744)"

    # =====================================================================
    # Scenario 4: Wake fallback — corrupt session transcript → mode=fresh
    # =====================================================================
    echo ""
    echo "=== Scenario 4: Wake fallback (delete transcript → mode=fresh) ==="
    local S4_BRANCH="qum744-s4-fresh-${SUFFIX}"
    local S4_PROMPT="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$S4_BRANCH}"
    wake_send_one_line "$SESSION" "$S4_PROMPT"

    local S4_STATE S4_NAME
    if ! S4_STATE=$(wake_find_child_by_branch "$S4_BRANCH"); then
        fail "S4: child state did not appear within 180s"
        e2e_print_results
        return 1
    fi
    S4_NAME=$(jq -r '.name' "$S4_STATE")
    pass "S4: child spawned (name=$S4_NAME)"

    if ! wake_wait_active "$S4_STATE" 90; then
        fail "S4: child never reached active"
        e2e_print_results
        return 1
    fi
    local S4_ORIG_SID
    S4_ORIG_SID=$(wake_session_id "$S4_STATE")
    if [ -z "$S4_ORIG_SID" ]; then
        fail "S4: original session_id never materialized"
        e2e_print_results
        return 1
    fi
    pass "S4: child active (orig sid=$S4_ORIG_SID)"

    # Pause so the agent is in a wake-accept-set liveness with no live handle
    # consuming the transcript file.
    local S4_PAUSE="Call mcp__sprawl__pause with agent='$S4_NAME', cascade=false, timeout_seconds=15. Then reply 'S4_PAUSE_${SUFFIX} ack' and stop."
    wake_send_one_line "$SESSION" "$S4_PAUSE"
    if ! wait_for_pattern_fast "$SESSION" "Agent $S4_NAME paused cleanly" 60; then
        fail "S4: pause ack did not appear"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    if ! wake_wait_status "$S4_STATE" "paused" 15; then
        fail "S4: disk Status did not reach 'paused'"
        e2e_print_results
        return 1
    fi
    pass "S4: child paused"

    # Hunt the claude transcript for this session_id under $HOME/.claude
    # (the canonical projects/<encoded-cwd>/<sid>.jsonl path) and delete it.
    # Without the transcript, `claude --resume <sid>` immediately emits
    # "No conversation found with session ID:" on stderr → OnResumeFailure
    # → Wake falls back to fresh.
    local S4_TRANSCRIPTS=""
    S4_TRANSCRIPTS=$(find "${HOME:-/root}/.claude" -type f -name "${S4_ORIG_SID}.jsonl" 2>/dev/null || true)
    if [ -z "$S4_TRANSCRIPTS" ]; then
        # Fallback wider search (.config/claude, .local/share/claude, etc.)
        S4_TRANSCRIPTS=$(find "${HOME:-/root}" -maxdepth 6 -type f -name "${S4_ORIG_SID}.jsonl" 2>/dev/null || true)
    fi
    if [ -z "$S4_TRANSCRIPTS" ]; then
        fail "S4: could not locate claude transcript file for sid=$S4_ORIG_SID under \$HOME — cannot induce resume failure"
        echo "  Tried: ${HOME:-/root}/.claude/**/${S4_ORIG_SID}.jsonl" >&2
        e2e_print_results
        return 1
    fi
    local tpath
    while IFS= read -r tpath; do
        [ -z "$tpath" ] && continue
        rm -f "$tpath" || true
        echo "  S4: deleted transcript $tpath"
    done <<< "$S4_TRANSCRIPTS"
    pass "S4: claude transcript(s) for sid=$S4_ORIG_SID deleted"

    if ! wake_drive_wake "$SESSION" "$S4_NAME" "S4_WAKE_${SUFFIX}" '"mode":"fresh"|"mode": "fresh"' 120; then
        fail "S4: wake mode=fresh ack did not appear within 120s (fallback path)"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    pass "S4: wake returned mode=fresh"

    # Assert a NEW session_id is minted on disk.
    local S4_NEW_SID="" s4_end=$((SECONDS + 30))
    while [ "$SECONDS" -lt "$s4_end" ]; do
        S4_NEW_SID=$(wake_session_id "$S4_STATE")
        if [ -n "$S4_NEW_SID" ] && [ "$S4_NEW_SID" != "$S4_ORIG_SID" ]; then
            break
        fi
        sleep 2
    done
    if [ -z "$S4_NEW_SID" ] || [ "$S4_NEW_SID" = "$S4_ORIG_SID" ]; then
        fail "S4: session_id did not change after fresh fallback (orig=$S4_ORIG_SID, current=$S4_NEW_SID)"
        cat "$S4_STATE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    pass "S4: new session_id minted (was $S4_ORIG_SID, now $S4_NEW_SID)"

    # =====================================================================
    # Scenario 5: Wake no-op on healthy → ErrWakeNotNeeded as success ack
    # =====================================================================
    echo ""
    echo "=== Scenario 5: Wake no-op on healthy ==="
    local S5_BRANCH="qum744-s5-noop-${SUFFIX}"
    local S5_PROMPT="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$S5_BRANCH}"
    wake_send_one_line "$SESSION" "$S5_PROMPT"

    local S5_STATE S5_NAME
    if ! S5_STATE=$(wake_find_child_by_branch "$S5_BRANCH"); then
        fail "S5: child state did not appear within 180s"
        e2e_print_results
        return 1
    fi
    S5_NAME=$(jq -r '.name' "$S5_STATE")
    pass "S5: child spawned (name=$S5_NAME)"

    if ! wake_wait_active "$S5_STATE" 90; then
        fail "S5: child never reached active"
        e2e_print_results
        return 1
    fi
    pass "S5: child active (healthy running)"

    # Drive wake. ErrWakeNotNeeded must surface as a success ack containing
    # "already running" / "no wake needed" (toolWake in
    # internal/sprawlmcp/server.go line ~630 formats this literal).
    if ! wake_drive_wake "$SESSION" "$S5_NAME" "S5_WAKE_${SUFFIX}" "already running; no wake needed" 60; then
        fail "S5: 'already running; no wake needed' ack did not appear within 60s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    pass "S5: wake on healthy agent surfaced ErrWakeNotNeeded as success ack"

    e2e_print_results
}
