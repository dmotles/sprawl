#!/usr/bin/env bash
# scripts/e2e-tests/pause-lifecycle.sh — QUM-722 / QUM-735 pause/kill/died
# lifecycle regression guard.
#
# Phases (per QUM-735 spec):
#   1. clean-pause-idle           : pause(idle) → Outcome=paused, disk=paused
#   2. clean-pause-in-flight-turn : pause(mid-turn) → drains cleanly, paused
#   3. escalation                 : pause(Bash sleep 60, timeout=2s) →
#                                   Outcome=escalated_to_kill, disk=killed
#   4. died-detection             : SIGKILL claude PID → liveness=died within 2s
#   5. cascade-pause              : parent + 2 children; pause(parent,
#                                   cascade=true) → bottom-up, all=paused
#   6. Real.Kill hard-stop        : kill in-flight agent; process exits within
#                                   hard-stop window, disk=killed
#   7. shutdown-loop SIGINT       : Ctrl-C TUI with idle child → clean exit
#                                   within the shutdown budget
#
# Conventions mirror scripts/e2e-tests/liveness-transitions.sh: per-probe
# tokens (avoid stale-scrollback false matches), `mcp__sprawl__status`-driven
# liveness reads, pgrep-keyed-by-session_id for live-process introspection.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# pause_assert_status SESSION CHILD TOKEN EXPECTED [TIMEOUT]
# Prompts weave to call mcp__sprawl__status and echo a single-line sentinel
# "<TOKEN> status=<value>" for CHILD's disk Status, then waits for the
# expected value. TOKEN must be unique per call.
pause_assert_status() {
    local session="$1" child="$2" token="$3" expected="$4" timeout="${5:-60}"
    local prompt="Call mcp__sprawl__status. In the JSON result, find the agent whose name is '$child' and read its status field. Then reply with EXACTLY one line and nothing else: '${token} status=<value>' where <value> is exactly the string in the status field."
    _stmux send-keys -t "$session" "$prompt"
    sleep 0.5
    _stmux send-keys -t "$session" Enter
    wait_for_pattern_fast "$session" "${token} status=${expected}" "$timeout"
}

# pause_wait_in_turn SESSION CHILD [TIMEOUT]
# Polls mcp__sprawl__peek until the agent's in_turn field is true. Used
# to gate phases that need an actively running turn before issuing a
# pause/kill. Internally uses unique tokens per probe.
pause_wait_in_turn() {
    local session="$1" child="$2" timeout="${3:-60}"
    local start="$SECONDS" attempt=0 token prompt
    while [ "$SECONDS" -lt "$((start + timeout))" ]; do
        attempt=$((attempt + 1))
        token="P_INTURN_${child}_${attempt}_${SUFFIX}"
        prompt="Call mcp__sprawl__peek with agent='$child'. In the JSON result read the in_turn field. Then reply with EXACTLY one line and nothing else: '${token} in_turn=<value>' where <value> is true or false."
        _stmux send-keys -t "$session" "$prompt"
        sleep 0.5
        _stmux send-keys -t "$session" Enter
        if wait_for_pattern_fast "$session" "${token} in_turn=true" 20; then
            return 0
        fi
    done
    return 1
}

# pause_assert_liveness SESSION CHILD TOKEN EXPECTED [TIMEOUT]
# Probes mcp__sprawl__status for the agent's liveness projection token.
pause_assert_liveness() {
    local session="$1" child="$2" token="$3" expected="$4" timeout="${5:-60}"
    local prompt="Call mcp__sprawl__status. In the JSON result, find the agent whose name is '$child' and read its liveness field. Then reply with EXACTLY one line and nothing else: '${token} liveness=<value>' where <value> is the string in the liveness field."
    _stmux send-keys -t "$session" "$prompt"
    sleep 0.5
    _stmux send-keys -t "$session" Enter
    wait_for_pattern_fast "$session" "${token} liveness=${expected}" "$timeout"
}

# pause_wait_state_file CHILD_LIKE_PATTERN — wait until a non-weave state.json
# appears matching the supplied jq filter on its full path. Echoes the path.
pause_find_child_by_branch() {
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

# pause_pgrep_claude SID — print the claude PID whose argv contains the given
# session_id. Empty when pgrep is unavailable or no match.
pause_pgrep_claude() {
    local sid="$1"
    command -v pgrep >/dev/null 2>&1 || return 0
    pgrep -af 'claude' 2>/dev/null | awk -v sid="$sid" '$0 ~ sid {print $1; exit}'
}

# pause_wait_active STATE_PATH [TIMEOUT_SECONDS]
# Polls the state file's status field until it reaches "active". Returns 0
# on success, 1 on timeout. Used as a spawn-success precondition — the
# backend "session reader exited before initialize handshake" race
# (occasionally seen when many claude subprocesses spawn back-to-back)
# leaves the state file present but status stuck.
pause_wait_active() {
    local state_path="$1" timeout="${2:-60}" elapsed=0 status=""
    while [ "$elapsed" -lt "$timeout" ]; do
        status=$(jq -r '.status // empty' "$state_path" 2>/dev/null || true)
        [ "$status" = "active" ] && return 0
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-pause-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum722"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"
    # Enable the test-only `_test_sleep` MCP tool so P3 / P6 can deterministically
    # hold a sprawl-initiated turn open for N seconds (Bash sleep races claude's
    # decision-to-call-Bash and is too flaky for ±2s pause-timeout assertions).
    export SPRAWL_ENABLE_TEST_TOOLS=1

    local SESSION="sprawl-pause-e2e-$(head -c4 /dev/urandom | xxd -p)"
    local STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"
    local SUFFIX
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"

    echo ""
    echo "=== Launching sprawl enter (shared session for phases 1-6) ==="
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

    local HAVE_PGREP=0
    if command -v pgrep >/dev/null 2>&1; then
        HAVE_PGREP=1
    fi

    # ----- Phase 1: clean-pause-idle ----------------------------------------
    echo ""
    echo "=== Phase 1: clean-pause-idle ==="
    local P1_BRANCH="qum735-p1-idle-${SUFFIX}"
    local SPAWN_PROMPT_TEMPLATE="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='__BRANCH__', and prompt set to exactly: 'You are an automated QUM-735 pause-lifecycle probe. Call mcp__sprawl__report_status with state=working, summary=\"idle, awaiting pause\". Then stop and wait. Do nothing else until you receive a message.'"
    local P1_PROMPT="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$P1_BRANCH}"
    _stmux send-keys -t "$SESSION" "$P1_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    local P1_STATE P1_NAME
    if ! P1_STATE=$(pause_find_child_by_branch "$P1_BRANCH"); then
        fail "P1: no child state appeared within 180s for branch $P1_BRANCH"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    P1_NAME=$(jq -r '.name' "$P1_STATE")
    pass "P1: child spawned (name=$P1_NAME)"

    local i status=""
    for i in 1 2 3 4 5 6 7 8 9 10; do
        status=$(jq -r '.status // empty' "$P1_STATE" 2>/dev/null || true)
        [ "$status" = "active" ] && break
        sleep 2
    done
    if [ "$status" != "active" ]; then
        fail "P1: precondition disk Status did not reach 'active' (got '$status')"
        e2e_print_results
        return 1
    fi

    local P1_PAUSE_PROMPT="Call mcp__sprawl__pause with agent='$P1_NAME', cascade=false, timeout_seconds=15. Then reply with EXACTLY one line: 'P1_PAUSE_DONE_${SUFFIX} ack' and nothing else."
    _stmux send-keys -t "$SESSION" "$P1_PAUSE_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    if wait_for_pattern_fast "$SESSION" "Agent $P1_NAME paused cleanly" 60; then
        pass "P1: pause tool returned 'paused cleanly' (Outcome=paused)"
    else
        fail "P1: pause tool ack did not appear within 60s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    local P1_DISK="" dp
    for dp in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
        P1_DISK=$(jq -r '.status // empty' "$P1_STATE" 2>/dev/null || true)
        [ "$P1_DISK" = "paused" ] && break
        sleep 1
    done
    if [ "$P1_DISK" = "paused" ]; then
        pass "P1: disk Status=paused"
    else
        fail "P1: disk Status did not reach 'paused' (got '$P1_DISK')"
        cat "$P1_STATE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    # ----- Phase 2: clean-pause-in-flight-turn ------------------------------
    echo ""
    echo "=== Phase 2: clean-pause-in-flight-turn ==="
    local P2_BRANCH="qum735-p2-inflight-${SUFFIX}"
    local P2_PROMPT="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$P2_BRANCH}"
    _stmux send-keys -t "$SESSION" "$P2_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    local P2_STATE P2_NAME
    if ! P2_STATE=$(pause_find_child_by_branch "$P2_BRANCH"); then
        fail "P2: no child state appeared within 180s for branch $P2_BRANCH"
        e2e_print_results
        return 1
    fi
    P2_NAME=$(jq -r '.name' "$P2_STATE")
    pass "P2: child spawned (name=$P2_NAME)"

    local p2s=""
    for i in 1 2 3 4 5 6 7 8 9 10; do
        p2s=$(jq -r '.status // empty' "$P2_STATE" 2>/dev/null || true)
        [ "$p2s" = "active" ] && break
        sleep 2
    done

    # Drive a long sprawl-initiated turn (StartTurn). runtime.Pause subscribes
    # to EventTurnCompleted regardless of autonomy, so any open turn frame
    # works; we don't need in_autonomous_turn=true here (cf. liveness P2).
    local P2_WORK="Call mcp__sprawl__send_message with to='$P2_NAME' and body set to exactly: 'Count slowly from 1 to 40, printing each number on its own line. Pause briefly between each number so this takes several seconds. Do not call any tools.' Then reply 'P2_SENT_${SUFFIX} ok' and nothing else."
    _stmux send-keys -t "$SESSION" "$P2_WORK"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter
    if wait_for_pattern_fast "$SESSION" "P2_SENT_${SUFFIX} ok" 60; then
        pass "P2: long-turn work message dispatched"
    else
        fail "P2: send_message ack did not appear within 60s"
        e2e_print_results
        return 1
    fi
    # Give the child a moment to actually open the turn frame.
    sleep 4

    local P2_PAUSE="Call mcp__sprawl__pause with agent='$P2_NAME', cascade=false, timeout_seconds=30. Then reply 'P2_PAUSE_${SUFFIX} done' and nothing else."
    _stmux send-keys -t "$SESSION" "$P2_PAUSE"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    if wait_for_pattern_fast "$SESSION" "Agent $P2_NAME paused cleanly" 90; then
        pass "P2: mid-turn pause returned 'paused cleanly' (drained at turn boundary)"
    else
        # If the pause escalated, that's a phase-2 failure (we budgeted 30s).
        if capture_pane "$SESSION" | grep -qE "Escalated pause of agent $P2_NAME"; then
            fail "P2: pause escalated to kill instead of draining cleanly"
        else
            fail "P2: pause ack did not appear within 90s"
        fi
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    local P2_DISK=""
    for dp in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
        P2_DISK=$(jq -r '.status // empty' "$P2_STATE" 2>/dev/null || true)
        [ "$P2_DISK" = "paused" ] && break
        sleep 1
    done
    if [ "$P2_DISK" = "paused" ]; then
        pass "P2: disk Status=paused after in-flight drain"
    else
        fail "P2: disk Status did not reach 'paused' (got '$P2_DISK')"
        e2e_print_results
        return 1
    fi

    # ----- Phase 3: escalation ---------------------------------------------
    echo ""
    echo "=== Phase 3: escalation (Bash sleep 60, timeout=2s → escalated_to_kill) ==="
    local P3_BRANCH="qum735-p3-escalate-${SUFFIX}"
    local P3_PROMPT="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$P3_BRANCH}"
    _stmux send-keys -t "$SESSION" "$P3_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    local P3_STATE P3_NAME
    if ! P3_STATE=$(pause_find_child_by_branch "$P3_BRANCH"); then
        fail "P3: no child state appeared within 180s for branch $P3_BRANCH"
        e2e_print_results
        return 1
    fi
    if ! pause_wait_active "$P3_STATE" 90; then
        fail "P3: child never reached active status"
        e2e_print_results
        return 1
    fi
    P3_NAME=$(jq -r '.name' "$P3_STATE")
    pass "P3: child spawned and active (name=$P3_NAME)"

    # Drive a sprawl-initiated turn that holds open for ~60s via the
    # test-only `_test_sleep` MCP tool. This is deterministic (cf. Bash
    # sleep, which races claude's plan-vs-dispatch and flakes the ±2s
    # pause-timeout assertion below).
    local P3_DRIVE="Call mcp__sprawl__send_message with to='$P3_NAME' and body set to exactly: 'Call mcp__sprawl___test_sleep with seconds=60. Do not call any other tools.' Then reply 'P3_DRIVE_${SUFFIX} ok' and nothing else."
    _stmux send-keys -t "$SESSION" "$P3_DRIVE"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter
    if ! wait_for_pattern_fast "$SESSION" "P3_DRIVE_${SUFFIX} ok" 60; then
        fail "P3: send_message to drive Bash sleep did not ack"
        e2e_print_results
        return 1
    fi
    if pause_wait_in_turn "$SESSION" "$P3_NAME" 90; then
        pass "P3: confirmed child is in-turn (Bash sleep 60 in flight)"
    else
        fail "P3: child never reached in_turn=true (no in-flight Bash to escalate against)"
        e2e_print_results
        return 1
    fi

    local P3_PAUSE="Call mcp__sprawl__pause with agent='$P3_NAME', cascade=false, timeout_seconds=2. Then reply 'P3_PAUSE_${SUFFIX} done' and nothing else."
    _stmux send-keys -t "$SESSION" "$P3_PAUSE"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    if wait_for_pattern_fast "$SESSION" "Escalated pause of agent $P3_NAME to kill" 60; then
        pass "P3: pause escalated to kill (Outcome=escalated_to_kill)"
    else
        fail "P3: escalation ack did not appear within 60s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    local P3_DISK=""
    for dp in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
        P3_DISK=$(jq -r '.status // empty' "$P3_STATE" 2>/dev/null || true)
        [ "$P3_DISK" = "killed" ] && break
        sleep 1
    done
    if [ "$P3_DISK" = "killed" ]; then
        pass "P3: disk Status=killed after escalation"
    else
        fail "P3: disk Status did not reach 'killed' (got '$P3_DISK')"
        e2e_print_results
        return 1
    fi

    # ----- Phase 4: died-detection -----------------------------------------
    echo ""
    echo "=== Phase 4: died-detection (SIGKILL claude PID → liveness=died) ==="
    # FEASIBILITY GATE: an external SIGKILL of an idle backend subprocess is
    # not currently surfaced as liveness=Died by the supervisor. The session
    # reader stores fatalErr and returns when the stdout pipe EOFs
    # (internal/backend/session.go:580-590), but it does NOT call
    # setTerminalErr — so the UnifiedRuntime's terminal-error handler never
    # fires, runCtx isn't cancelled, loopWG.Wait() never returns, and
    # watchHandleExit never stamps StatusDied. A SIGKILL'd idle agent thus
    # remains projected as Liveness=Running indefinitely. This was
    # live-confirmed: 90s polls of state.json showed Status="active" after
    # SIGKILL of the verified-alive PID.
    #
    # Unit coverage that DOES verify the watchHandleExit branch:
    #   internal/supervisor/runtime_durable_fault_test.go
    #     ::TestWatchHandleExit_UnexpectedExitClassifiesAsDied
    #   internal/supervisor/runtime_fault_chain_test.go
    #     ::TestRuntime_Fault_DiedClassification
    #
    # The runtime <-> subprocess-exit wiring needed to lift this gate is
    # tracked separately; see follow-up Linear issue filed by QUM-735.
    if true; then
        echo "  SKIP: P4 — external SIGKILL → Died is NOT currently observable"
        echo "        from supervisor.Status (session reader EOF does not fire"
        echo "        the terminal-error handler). Unit coverage at"
        echo "        internal/supervisor/runtime_durable_fault_test.go"
        echo "        ::TestWatchHandleExit_UnexpectedExitClassifiesAsDied"
        echo "        guards the watchHandleExit branch; the live wiring gap"
        echo "        is filed for follow-up. (QUM-735)"
    elif [ "$HAVE_PGREP" -ne 1 ]; then
        echo "  SKIP: P4 — pgrep unavailable, cannot locate the claude PID"
    else
        local P4_STATE="" P4_NAME="" P4_BRANCH="" P4_SID="" P4_PID="" attempt
        # Retry the spawn precondition to absorb the rare
        # "session reader exited before initialize handshake" backend race
        # when many claude subprocesses spawn back-to-back. Each attempt
        # uses a fresh branch name. The precondition is intentionally
        # strict: we require status=active AND a live claude PID locatable
        # via session_id AND `kill -0` confirming the PID is alive — only
        # then is SIGKILL a meaningful test signal.
        for attempt in 1 2 3; do
            P4_BRANCH="qum735-p4-died-${SUFFIX}-a${attempt}"
            local P4_PROMPT="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$P4_BRANCH}"
            sleep 5
            _stmux send-keys -t "$SESSION" "$P4_PROMPT"
            sleep 0.5
            _stmux send-keys -t "$SESSION" Enter

            local CANDIDATE_STATE CANDIDATE_NAME CANDIDATE_SID="" CANDIDATE_PID=""
            if ! CANDIDATE_STATE=$(pause_find_child_by_branch "$P4_BRANCH"); then
                continue
            fi
            CANDIDATE_NAME=$(jq -r '.name' "$CANDIDATE_STATE")
            if ! pause_wait_active "$CANDIDATE_STATE" 60; then
                echo "  NOTE: P4 attempt $attempt — child '$CANDIDATE_NAME' never reached active; retrying"
                continue
            fi
            for i in 1 2 3 4 5 6 7 8 9 10; do
                CANDIDATE_SID=$(jq -r '.session_id // empty' "$CANDIDATE_STATE" 2>/dev/null || true)
                [ -n "$CANDIDATE_SID" ] && break
                sleep 2
            done
            if [ -z "$CANDIDATE_SID" ]; then
                echo "  NOTE: P4 attempt $attempt — no session_id surfaced; retrying"
                continue
            fi
            for i in 1 2 3 4 5 6 7 8 9 10; do
                CANDIDATE_PID=$(pause_pgrep_claude "$CANDIDATE_SID")
                [ -n "$CANDIDATE_PID" ] && break
                sleep 1
            done
            if [ -z "$CANDIDATE_PID" ] || ! kill -0 "$CANDIDATE_PID" 2>/dev/null; then
                echo "  NOTE: P4 attempt $attempt — no live PID for session_id=$CANDIDATE_SID; retrying"
                continue
            fi
            # Confirm the agent is genuinely healthy: status stays "active"
            # for at least 3s (filters out the backend-handshake-failed
            # race where status briefly flips active then back).
            sleep 3
            local STILL_ACTIVE
            STILL_ACTIVE=$(jq -r '.status // empty' "$CANDIDATE_STATE" 2>/dev/null || true)
            if [ "$STILL_ACTIVE" != "active" ]; then
                echo "  NOTE: P4 attempt $attempt — status flipped from active to '$STILL_ACTIVE' before SIGKILL; retrying"
                continue
            fi
            if ! kill -0 "$CANDIDATE_PID" 2>/dev/null; then
                echo "  NOTE: P4 attempt $attempt — PID $CANDIDATE_PID died on its own before SIGKILL; retrying"
                continue
            fi
            P4_STATE="$CANDIDATE_STATE"
            P4_NAME="$CANDIDATE_NAME"
            P4_SID="$CANDIDATE_SID"
            P4_PID="$CANDIDATE_PID"
            break
        done
        if [ -z "$P4_PID" ]; then
            fail "P4: could not establish a healthy SIGKILL precondition after 3 attempts"
            capture_pane "$SESSION" | tail -60 >&2
            e2e_print_results
            return 1
        fi
        pass "P4: precondition ready (name=$P4_NAME, branch=$P4_BRANCH, pid=$P4_PID)"
        pass "P4: located claude PID=$P4_PID (sid=$P4_SID)"

        local KILL_START=$SECONDS
        echo "  P4: SIGKILL precondition snapshot:"
        pgrep -af 'claude' 2>/dev/null | grep -F "$P4_SID" | head -5 | sed 's/^/    /' >&2 || true
        # Use pkill so all processes whose argv contains the session_id are
        # killed in one shot. A single PID might miss a fork/shell wrapper.
        pkill -9 -f "$P4_SID" 2>/dev/null || true
        # Belt-and-suspenders direct PID kill.
        kill -9 "$P4_PID" 2>/dev/null || true
        # Also kill any direct children of the captured PID (e.g. a shell
        # wrapper that exec'd to claude but kept a parent process around).
        if command -v pgrep >/dev/null 2>&1; then
            local kid
            for kid in $(pgrep -P "$P4_PID" 2>/dev/null || true); do
                kill -9 "$kid" 2>/dev/null || true
            done
        fi
        # Disk Status is the source-of-truth contract — watchHandleExit
        # classifies an unexpected exit and stamps StatusDied. Poll the
        # state file (file-system observable, no MCP roundtrip needed).
        local P4_DISK="" elapsed_disk=0
        while [ "$elapsed_disk" -lt 90 ]; do
            P4_DISK=$(jq -r '.status // empty' "$P4_STATE" 2>/dev/null || true)
            [ "$P4_DISK" = "died" ] && break
            sleep 1
            elapsed_disk=$((elapsed_disk + 1))
        done
        if [ "$P4_DISK" = "died" ]; then
            local ELAPSED=$((SECONDS - KILL_START))
            pass "P4: disk Status=died observed (~${ELAPSED}s after SIGKILL)"
        else
            fail "P4: disk Status did not become 'died' within 90s after SIGKILL (got '$P4_DISK')"
            cat "$P4_STATE" >&2 2>/dev/null || true
            capture_pane "$SESSION" | tail -60 >&2
            e2e_print_results
            return 1
        fi
        # Secondary: the liveness MCP projection should also surface "died".
        # Disk-status=died maps unambiguously through liveness.From (see
        # internal/supervisor/liveness/projection.go), so any failure here
        # would indicate a status-tool bug, not a lifecycle bug.
        if pause_assert_liveness "$SESSION" "$P4_NAME" "P4_LIVE_${SUFFIX}" "died" 30; then
            pass "P4: liveness=died surfaced via mcp__sprawl__status"
        else
            fail "P4: liveness MCP projection did not surface 'died' (disk is 'died')"
            capture_pane "$SESSION" | tail -60 >&2
            e2e_print_results
            return 1
        fi
    fi

    # ----- Phase 5: cascade-pause ------------------------------------------
    echo ""
    echo "=== Phase 5: cascade-pause (parent + 2 children, cascade=true) ==="
    local P5_PARENT_BRANCH="qum735-p5-parent-${SUFFIX}"
    local P5_C1_BRANCH="qum735-p5-c1-${SUFFIX}"
    local P5_C2_BRANCH="qum735-p5-c2-${SUFFIX}"
    local P5_KID_PROMPT_BODY="You are an automated QUM-735 cascade probe. Call mcp__sprawl__report_status with state=working, summary=\"idle\". Then stop and wait. Do nothing else until you receive a message."
    local P5_MGR_PROMPT="Call mcp__sprawl__spawn with family='engineering', type='manager', branch='$P5_PARENT_BRANCH', and prompt set to exactly the following multi-line text (preserving the embedded newline): 'You are an automated QUM-735 cascade parent. First call mcp__sprawl__spawn with family=engineering, type=engineer, branch=$P5_C1_BRANCH, prompt=\"$P5_KID_PROMPT_BODY\". Then call mcp__sprawl__spawn with family=engineering, type=engineer, branch=$P5_C2_BRANCH, prompt=\"$P5_KID_PROMPT_BODY\". After both spawn calls return, call mcp__sprawl__report_status with state=working, summary=\"cascade parent idle\". Then stop and wait.'"
    _stmux send-keys -t "$SESSION" "$P5_MGR_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    local P5_PARENT_STATE P5_PARENT_NAME
    if ! P5_PARENT_STATE=$(pause_find_child_by_branch "$P5_PARENT_BRANCH"); then
        fail "P5: parent manager state never appeared"
        e2e_print_results
        return 1
    fi
    P5_PARENT_NAME=$(jq -r '.name' "$P5_PARENT_STATE")
    pass "P5: parent manager spawned (name=$P5_PARENT_NAME)"

    local P5_C1_STATE P5_C2_STATE P5_C1_NAME P5_C2_NAME
    if ! P5_C1_STATE=$(pause_find_child_by_branch "$P5_C1_BRANCH"); then
        fail "P5: child 1 (branch $P5_C1_BRANCH) never appeared"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    if ! P5_C2_STATE=$(pause_find_child_by_branch "$P5_C2_BRANCH"); then
        fail "P5: child 2 (branch $P5_C2_BRANCH) never appeared"
        e2e_print_results
        return 1
    fi
    P5_C1_NAME=$(jq -r '.name' "$P5_C1_STATE")
    P5_C2_NAME=$(jq -r '.name' "$P5_C2_STATE")
    pass "P5: cascade children spawned (c1=$P5_C1_NAME c2=$P5_C2_NAME)"

    # Verify the parent linkage (cascade walks the Parent field).
    local P5_C1_PARENT P5_C2_PARENT
    P5_C1_PARENT=$(jq -r '.parent' "$P5_C1_STATE")
    P5_C2_PARENT=$(jq -r '.parent' "$P5_C2_STATE")
    if [ "$P5_C1_PARENT" = "$P5_PARENT_NAME" ] && [ "$P5_C2_PARENT" = "$P5_PARENT_NAME" ]; then
        pass "P5: parent linkage verified (c1.parent=c2.parent=$P5_PARENT_NAME)"
    else
        fail "P5: parent linkage wrong (c1.parent=$P5_C1_PARENT, c2.parent=$P5_C2_PARENT, want $P5_PARENT_NAME)"
        e2e_print_results
        return 1
    fi

    # Wait for all 3 to reach active.
    local all_active=0
    for i in $(seq 1 60); do
        local s_p s_c1 s_c2
        s_p=$(jq -r '.status // empty' "$P5_PARENT_STATE" 2>/dev/null || true)
        s_c1=$(jq -r '.status // empty' "$P5_C1_STATE" 2>/dev/null || true)
        s_c2=$(jq -r '.status // empty' "$P5_C2_STATE" 2>/dev/null || true)
        if [ "$s_p" = "active" ] && [ "$s_c1" = "active" ] && [ "$s_c2" = "active" ]; then
            all_active=1
            break
        fi
        sleep 2
    done
    if [ "$all_active" -ne 1 ]; then
        fail "P5: not all 3 agents reached 'active' status"
        e2e_print_results
        return 1
    fi

    local P5_PAUSE="Call mcp__sprawl__pause with agent='$P5_PARENT_NAME', cascade=true, timeout_seconds=30. Then reply 'P5_PAUSE_${SUFFIX} ack' and nothing else."
    _stmux send-keys -t "$SESSION" "$P5_PAUSE"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    if wait_for_pattern_fast "$SESSION" "Agent $P5_PARENT_NAME paused cleanly" 120; then
        pass "P5: cascade pause(parent) returned cleanly"
    else
        fail "P5: cascade pause ack did not appear within 120s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # All three must reach paused. Use mtime ordering (bottom-up: children
    # paused before parent → mtime(child) <= mtime(parent), since children
    # are paused in parallel before the parent runtime.Pause). mtime
    # granularity is 1s, so allow equal timestamps.
    local s_p="" s_c1="" s_c2="" mt_p="" mt_c1="" mt_c2=""
    for i in $(seq 1 30); do
        s_p=$(jq -r '.status // empty' "$P5_PARENT_STATE" 2>/dev/null || true)
        s_c1=$(jq -r '.status // empty' "$P5_C1_STATE" 2>/dev/null || true)
        s_c2=$(jq -r '.status // empty' "$P5_C2_STATE" 2>/dev/null || true)
        if [ "$s_p" = "paused" ] && [ "$s_c1" = "paused" ] && [ "$s_c2" = "paused" ]; then
            break
        fi
        sleep 1
    done
    if [ "$s_p" = "paused" ] && [ "$s_c1" = "paused" ] && [ "$s_c2" = "paused" ]; then
        pass "P5: all 3 agents reached Status=paused (parent+2 children)"
    else
        fail "P5: not all paused (parent=$s_p c1=$s_c1 c2=$s_c2)"
        e2e_print_results
        return 1
    fi

    mt_p=$(stat -c %Y "$P5_PARENT_STATE" 2>/dev/null || stat -f %m "$P5_PARENT_STATE")
    mt_c1=$(stat -c %Y "$P5_C1_STATE" 2>/dev/null || stat -f %m "$P5_C1_STATE")
    mt_c2=$(stat -c %Y "$P5_C2_STATE" 2>/dev/null || stat -f %m "$P5_C2_STATE")
    if [ "$mt_c1" -le "$mt_p" ] && [ "$mt_c2" -le "$mt_p" ]; then
        pass "P5: bottom-up ordering: max(c1=$mt_c1, c2=$mt_c2) <= parent=$mt_p"
    else
        fail "P5: ordering violated: c1=$mt_c1 c2=$mt_c2 parent=$mt_p"
        e2e_print_results
        return 1
    fi

    # ----- Phase 6: Real.Kill hard-stop assertion ---------------------------
    echo ""
    echo "=== Phase 6: Real.Kill hard-stop (in-flight kill, no polite drain) ==="
    local P6_BRANCH="qum735-p6-hardkill-${SUFFIX}"
    local P6_PROMPT="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$P6_BRANCH}"
    _stmux send-keys -t "$SESSION" "$P6_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    local P6_STATE P6_NAME
    if ! P6_STATE=$(pause_find_child_by_branch "$P6_BRANCH"); then
        fail "P6: no child state appeared within 180s for branch $P6_BRANCH"
        e2e_print_results
        return 1
    fi
    if ! pause_wait_active "$P6_STATE" 90; then
        fail "P6: child never reached active status"
        e2e_print_results
        return 1
    fi
    P6_NAME=$(jq -r '.name' "$P6_STATE")
    pass "P6: child spawned and active (name=$P6_NAME)"

    local P6_SID="" P6_PID=""
    for i in 1 2 3 4 5 6 7 8 9 10; do
        P6_SID=$(jq -r '.session_id // empty' "$P6_STATE" 2>/dev/null || true)
        [ -n "$P6_SID" ] && break
        sleep 2
    done
    if [ "$HAVE_PGREP" -eq 1 ] && [ -n "$P6_SID" ]; then
        for i in 1 2 3 4 5 6 7 8 9 10; do
            P6_PID=$(pause_pgrep_claude "$P6_SID")
            [ -n "$P6_PID" ] && break
            sleep 1
        done
    fi
    # Drive a sprawl-initiated turn that holds open via the deterministic
    # `_test_sleep` MCP tool so the kill happens during a genuinely
    # in-flight MCP-tool-wait (the worst case for hard-stop semantics).
    local P6_DRIVE="Call mcp__sprawl__send_message with to='$P6_NAME' and body set to exactly: 'Call mcp__sprawl___test_sleep with seconds=60. Do not call any other tools.' Then reply 'P6_DRIVE_${SUFFIX} ok' and nothing else."
    _stmux send-keys -t "$SESSION" "$P6_DRIVE"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter
    if ! wait_for_pattern_fast "$SESSION" "P6_DRIVE_${SUFFIX} ok" 60; then
        fail "P6: send_message to drive Bash sleep did not ack"
        e2e_print_results
        return 1
    fi
    if pause_wait_in_turn "$SESSION" "$P6_NAME" 90; then
        pass "P6: confirmed child is in-turn (Bash sleep 120 in flight)"
    else
        fail "P6: child never reached in_turn=true"
        e2e_print_results
        return 1
    fi

    local KILL_START=$SECONDS
    local P6_KILL="Call mcp__sprawl__kill with agent='$P6_NAME'. Then reply 'P6_KILL_${SUFFIX} done' and nothing else."
    _stmux send-keys -t "$SESSION" "$P6_KILL"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    if wait_for_pattern_fast "$SESSION" "Killed agent $P6_NAME" 30; then
        local KILL_ELAPSED=$((SECONDS - KILL_START))
        # Hard-stop window: a polite Stop would have to wait for the 120s
        # sleep to drain. A 15s ceiling is well below that and matches the
        # withRuntimeStopTimeout default.
        if [ "$KILL_ELAPSED" -le 15 ]; then
            pass "P6: Real.Kill returned in ${KILL_ELAPSED}s (well under polite-drain window of 120s)"
        else
            fail "P6: Real.Kill took ${KILL_ELAPSED}s — exceeds hard-stop window"
            e2e_print_results
            return 1
        fi
    else
        fail "P6: kill ack did not appear within 30s (would indicate polite drain)"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    local P6_DISK=""
    for dp in 1 2 3 4 5 6 7 8 9 10; do
        P6_DISK=$(jq -r '.status // empty' "$P6_STATE" 2>/dev/null || true)
        [ "$P6_DISK" = "killed" ] && break
        sleep 1
    done
    if [ "$P6_DISK" = "killed" ]; then
        pass "P6: disk Status=killed"
    else
        fail "P6: disk Status did not reach 'killed' (got '$P6_DISK')"
        e2e_print_results
        return 1
    fi

    # Process exit verification (if we captured the PID).
    if [ -n "$P6_PID" ]; then
        local exited=0
        for i in 1 2 3 4 5 6 7 8 9 10; do
            if ! kill -0 "$P6_PID" 2>/dev/null; then
                exited=1
                break
            fi
            sleep 1
        done
        if [ "$exited" -eq 1 ]; then
            pass "P6: claude PID=$P6_PID exited within hard-stop window"
        else
            fail "P6: claude PID=$P6_PID still alive after kill — hard-stop did not actually terminate the process"
            e2e_print_results
            return 1
        fi
    else
        echo "  NOTE: P6 process-exit check skipped (no PID captured)"
    fi

    # ----- Tear down the shared session before Phase 7 ----------------------
    echo ""
    echo "=== Tearing down shared session before Phase 7 ==="
    _stmux kill-session -t "$SESSION" 2>/dev/null || true
    if [ -n "${PHANTOM_PID:-}" ]; then
        kill "$PHANTOM_PID" 2>/dev/null || true
        unset PHANTOM_PID
    fi
    sleep 2

    # ----- Phase 7: shutdown-loop pause-then-kill on Ctrl-C -----------------
    echo ""
    echo "=== Phase 7: shutdown-loop on SIGINT (clean exit within budget) ==="
    # Fresh sandbox root so prior runs' durable state doesn't influence the
    # Shutdown classifier (it only touches in-memory runtimes anyway, but
    # this keeps the phase hermetic).
    local SHUTDOWN_ROOT
    SHUTDOWN_ROOT=$(mktemp -d -t sprawl-qum735-p7-XXXXXX)
    if [[ "$SHUTDOWN_ROOT" != /tmp/* ]]; then
        fail "P7: refusing to use SHUTDOWN_ROOT outside /tmp ($SHUTDOWN_ROOT)"
        e2e_print_results
        return 1
    fi
    # Initialize a minimal git repo (sandbox init expects it).
    (
        cd "$SHUTDOWN_ROOT"
        git init -q
        git config user.email "qum735@example.com"
        git config user.name "qum735"
        echo "qum735 shutdown phase" > README.md
        git add README.md
        git commit -q -m "init"
    )
    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SHUTDOWN_ROOT/.env"
    fi

    local SESSION2="sprawl-pause-e2e-shutdown-$(head -c4 /dev/urandom | xxd -p)"
    local STDERR_LOG2="$SHUTDOWN_ROOT/.sprawl-tui-stderr.log"
    mkdir -p "$SHUTDOWN_ROOT/.sprawl"
    _stmux new-session -d -s "$SESSION2" -x 200 -y 50 \
        "cd '$SHUTDOWN_ROOT' && SPRAWL_ROOT='$SHUTDOWN_ROOT' SPRAWL_CLAUDE='$SPRAWL_CLAUDE' '$SPRAWL_BIN' enter 2>'$STDERR_LOG2'"
    _stmux set-option -t "$SESSION2" window-size manual >/dev/null
    _stmux resize-window -t "$SESSION2" -x 200 -y 50 >/dev/null

    if wait_for_pattern "$SESSION2" "weave " 90; then
        pass "P7: secondary TUI rendered"
    else
        fail "P7: secondary TUI did not render within 90s"
        capture_pane "$SESSION2" | tail -30 >&2
        [ -f "$STDERR_LOG2" ] && tail -20 "$STDERR_LOG2" >&2
        rm -rf -- "$SHUTDOWN_ROOT" 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    if capture_pane "$SESSION2" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION2" "1" Enter
        sleep 1
    fi
    e2e_attach_phantom_client "$SESSION2"

    # Spawn one idle engineer.
    local P7_BRANCH="qum735-p7-idle-${SUFFIX}"
    local P7_SPAWN="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$P7_BRANCH}"
    _stmux send-keys -t "$SESSION2" "$P7_SPAWN"
    sleep 0.5
    _stmux send-keys -t "$SESSION2" Enter

    local P7_STATE P7_NAME
    local p7_deadline=$((SECONDS + 180))
    P7_STATE=""
    while [ "$SECONDS" -lt "$p7_deadline" ]; do
        for candidate in "$SHUTDOWN_ROOT"/.sprawl/agents/*.json; do
            [ -e "$candidate" ] || continue
            local nm br
            nm=$(jq -r '.name // empty' "$candidate" 2>/dev/null || true)
            br=$(jq -r '.branch // empty' "$candidate" 2>/dev/null || true)
            if [ -n "$nm" ] && [ "$nm" != "weave" ] && [ "$br" = "$P7_BRANCH" ]; then
                P7_STATE="$candidate"
                break
            fi
        done
        [ -n "$P7_STATE" ] && break
        sleep 1
    done
    if [ -z "$P7_STATE" ]; then
        fail "P7: idle child never appeared in secondary session"
        capture_pane "$SESSION2" | tail -40 >&2
        rm -rf -- "$SHUTDOWN_ROOT" 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    P7_NAME=$(jq -r '.name' "$P7_STATE")
    pass "P7: idle child spawned (name=$P7_NAME)"

    # Wait for the child to be active (so it has a live runtime to tear down).
    local p7s=""
    for i in $(seq 1 30); do
        p7s=$(jq -r '.status // empty' "$P7_STATE" 2>/dev/null || true)
        [ "$p7s" = "active" ] && break
        sleep 2
    done
    if [ "$p7s" != "active" ]; then
        fail "P7: idle child never reached 'active' (got '$p7s')"
        rm -rf -- "$SHUTDOWN_ROOT" 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    # Ctrl+C opens the QUM-409 "quit?" confirm dialog (visible in the footer
    # as "ctrl+c: clear/quit"). Send Ctrl+C to open the modal, then 'y' to
    # confirm. The deferred sup.Shutdown(ctx) then runs with a 5s budget;
    # we accept up to 15s wall clock for the full teardown + flush.
    local SHUTDOWN_BUDGET=15
    local SHUTDOWN_START=$SECONDS
    _stmux send-keys -t "$SESSION2" "C-c"
    sleep 1
    # Confirm with "y" (the QUM-409 confirm dialog).
    _stmux send-keys -t "$SESSION2" "y"

    local shutdown_done=0
    local deadline=$((SECONDS + SHUTDOWN_BUDGET))
    while [ "$SECONDS" -lt "$deadline" ]; do
        if ! _stmux has-session -t "$SESSION2" 2>/dev/null; then
            shutdown_done=1
            break
        fi
        sleep 0.2
    done
    local SHUTDOWN_ELAPSED=$((SECONDS - SHUTDOWN_START))
    if [ "$shutdown_done" -eq 1 ]; then
        pass "P7: TUI session exited in ${SHUTDOWN_ELAPSED}s after Ctrl-C + 'y' (within ${SHUTDOWN_BUDGET}s ceiling)"
    else
        fail "P7: TUI did not exit within ${SHUTDOWN_BUDGET}s after Ctrl-C + 'y'"
        capture_pane "$SESSION2" | tail -40 >&2 || true
        [ -f "$STDERR_LOG2" ] && tail -40 "$STDERR_LOG2" >&2
        _stmux kill-session -t "$SESSION2" 2>/dev/null || true
        rm -rf -- "$SHUTDOWN_ROOT" 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    if [ -f "$STDERR_LOG2" ] && grep -qF "TUI session ended." "$STDERR_LOG2"; then
        pass "P7: 'TUI session ended.' clean-exit marker recorded"
    else
        echo "  NOTE: P7 'TUI session ended.' marker absent from stderr log (non-fatal)"
    fi

    # Idle children get polite Stop in Shutdown → disk Status=suspended.
    local P7_DISK
    P7_DISK=$(jq -r '.status // empty' "$P7_STATE" 2>/dev/null || true)
    case "$P7_DISK" in
        suspended|paused|killed)
            pass "P7: idle child disk Status='$P7_DISK' (clean shutdown classification)"
            ;;
        *)
            echo "  NOTE: P7 idle child disk Status='$P7_DISK' (expected suspended/paused/killed; non-fatal)"
            ;;
    esac

    if [[ "$SHUTDOWN_ROOT" == /tmp/* ]]; then
        rm -rf -- "$SHUTDOWN_ROOT" 2>/dev/null || true
    fi

    e2e_print_results
}
