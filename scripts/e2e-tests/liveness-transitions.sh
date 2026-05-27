#!/usr/bin/env bash
# scripts/e2e-tests/liveness-transitions.sh — QUM-615 AgentLiveness matrix row.
#
# Spec: docs/research/qum-615-agent-liveness-spec-2026-05-26.md §4. Drives the
# AgentLiveness transition classes against the observable projections (disk
# Status + process_alive via the `mcp__sprawl__status` tool). Phases map 1:1
# onto spec §2.2 / §4.
#
# Build tag: needs_build_tags=sprawl_test so the build-tag-gated
# `mcp__sprawl___test_induce_wedge` MCP tool is compiled in (same as
# recover-live).
#
# Live phases (driven against this single TUI session):
#   P1  spawn→idle      : disk Status=active; process_alive=true        (T1/T2)
#   P2  drive a turn     : peek in_autonomous_turn true→false  (T4/T5 —
#                          attempted live; in_autonomous_turn=true is NOT
#                          operator-drivable via send_message, so P2 SKIPs with
#                          named unit coverage + escalation. See the P2 block.)
#   P3  induce wedge     : process_alive flips to FALSE                 (T6 — the
#                          QUM-606 lie-window guard)
#   P5  recover          : new --resume PID, back to Running/alive      (T9/T10)
#   P6  durable Faulted  : disk Status=faulted (!= stopped)             (AC a/b)
#   P10 kill             : durable Status=killed, process_alive≠true,
#                          absorbing                                    (T16)
# Remaining transitions are NOT deterministically drivable from one live TUI
# session and are SKIPPED with NAMED unit/matrix coverage at the bottom:
#   P4  re-fault re-fires banner (QUM-602), P7 stop→Stopped (T7/T8),
#   P8  suspend/resume (T12/T13/T14), P9 resume-failed (T15).
#
# process_alive is not persisted to disk — it is computed in Real.Status and
# surfaced via the `mcp__sprawl__status` MCP tool. To read it deterministically
# we prompt weave to call status and echo a single-line sentinel keyed by a
# per-phase token (so stale scrollback from an earlier phase can't false-match).

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1 needs_build_tags=sprawl_test"
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-liveness-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum615"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    # Copy .env so scripts/run-claude can rehydrate auth in subshells.
    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi

    export SPRAWL_ENABLE_TEST_TOOLS=1
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SESSION="sprawl-liveness-e2e-$(head -c4 /dev/urandom | xxd -p)"
    local STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"
    local BRANCH_SUFFIX
    BRANCH_SUFFIX="$(head -c4 /dev/urandom | xxd -p)"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"

    echo ""
    echo "=== Launching sprawl enter ==="
    _stmux new-session -d -s "$SESSION" -x 200 -y 50 \
        "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_CLAUDE='$SPRAWL_CLAUDE' SPRAWL_ENABLE_TEST_TOOLS=1 '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
    _stmux set-option -t "$SESSION" window-size manual >/dev/null
    _stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

    if wait_for_pattern "$SESSION" "weave \\(idle\\)" 45; then
        pass "TUI rendered ('weave (idle)' visible)"
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

    echo ""
    echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
    e2e_attach_phantom_client "$SESSION"

    # --- Phase 1: spawn child, assert Status=active + process_alive=true (T1/T2) ---
    echo ""
    echo "=== Phase 1 (T1/T2): spawn an engineer child that idles ==="
    local SPAWN_PROMPT="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum615-liveness-probe-${BRANCH_SUFFIX}', and prompt set to exactly: 'You are an automated QUM-615 liveness probe. Call mcp__sprawl__report_status with state=working, summary=\"idle, awaiting fault induction\". Then stop and wait. Do nothing else until you receive a message.'"
    _stmux send-keys -t "$SESSION" "$SPAWN_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    local CHILD_STATE=""
    local CHILD_NAME=""
    local ELAPSED=0
    local SPAWN_LANDED=0
    while [ "$ELAPSED" -lt 180 ]; do
        while IFS= read -r candidate; do
            [ -z "$candidate" ] && continue
            local local_name
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
    if [ "$SPAWN_LANDED" -ne 1 ]; then
        fail "no child state appeared within 180s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    pass "child spawned (name=$CHILD_NAME)"

    # disk Status should reach "active" (spawn writes it).
    local DISK_STATUS=""
    local i
    for i in 1 2 3 4 5 6 7 8 9 10; do
        DISK_STATUS=$(jq -r '.status // empty' "$CHILD_STATE" 2>/dev/null || true)
        [ "$DISK_STATUS" = "active" ] && break
        sleep 2
    done
    if [ "$DISK_STATUS" = "active" ]; then
        pass "P1: disk Status=active"
    else
        fail "P1: disk Status did not reach 'active' (got '$DISK_STATUS')"
        e2e_print_results
        return 1
    fi

    # process_alive=true via the status tool (sentinel keyed by phase token).
    if liveness_assert_process_alive "$SESSION" "$CHILD_NAME" "ALIVECHK_P1" "true"; then
        pass "P1: process_alive=true (T1/T2 Running)"
    else
        fail "P1: process_alive was not 'true' for $CHILD_NAME"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # Capture the original session_id + claude PID so P5 (recover) can assert
    # the subprocess was actually swapped (new --resume PID != original).
    # pgrep is required only for P5; if absent we skip P5 cleanly below.
    local ORIG_SID="" ORIG_PID="" HAVE_PGREP=0
    if command -v pgrep >/dev/null 2>&1; then
        HAVE_PGREP=1
    fi
    for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
        ORIG_SID=$(jq -r '.session_id // empty' "$CHILD_STATE" 2>/dev/null || true)
        [ -n "$ORIG_SID" ] && break
        sleep 2
    done
    if [ "$HAVE_PGREP" -eq 1 ] && [ -n "$ORIG_SID" ]; then
        for i in 1 2 3 4 5; do
            ORIG_PID=$(pgrep -af 'claude' | awk -v sid="$ORIG_SID" '$0 ~ sid {print $1; exit}' || true)
            [ -n "$ORIG_PID" ] && break
            sleep 2
        done
    fi

    # --- Phase 3: induce wedge → process_alive flips FALSE (T6, QUM-606 guard) ---
    echo ""
    echo "=== Phase 3 (T6): induce SubscriberWedge → process_alive=false (QUM-606) ==="
    local INDUCE_PROMPT="Call mcp__sprawl___test_induce_wedge with agent_name='$CHILD_NAME', fault_class='subscriber_wedged'. Confirm in your reply that the call succeeded."
    _stmux send-keys -t "$SESSION" "$INDUCE_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    if wait_for_pattern_fast "$SESSION" "Induced subscriber_wedged|SubscriberWedge|fault" 60; then
        pass "fault induction tool returned"
    else
        fail "fault induction tool did not surface within 60s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    # The KEY assertion: a faulted-but-not-torn-down runtime must report
    # process_alive=false. Before M1 this lied 'true' (Lifecycle still started).
    if liveness_assert_process_alive "$SESSION" "$CHILD_NAME" "ALIVECHK_P3" "false"; then
        pass "P3: process_alive flipped to FALSE on fault (QUM-606 lie window closed)"
    else
        fail "P3: process_alive did not flip to 'false' after fault (QUM-606 regression)"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # --- Phase 6 (T6/AC a-b): fault is DURABLE Faulted on disk, distinct from Stopped ---
    # QUM-625 M4: watchHandleExit records a torn-down fault as durable disk
    # Status="faulted" (not erased to "stopped"). This is the end-to-end guard
    # for AC (a) "record fault at teardown" + (b)/invariant-3 "Faulted != Stopped".
    # The fault chain (cancel runCtx → loop drain → Done() → watchHandleExit)
    # settles a few seconds after induction, so poll the child state.json.
    echo ""
    echo "=== Phase 6 (AC a/b): fault persists as durable disk Status=faulted (!= stopped) ==="
    local FAULT_STATUS="" fp
    for fp in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
        FAULT_STATUS=$(jq -r '.status // empty' "$CHILD_STATE" 2>/dev/null || true)
        [ "$FAULT_STATUS" = "faulted" ] && break
        sleep 2
    done
    if [ "$FAULT_STATUS" = "faulted" ]; then
        pass "P6: torn-down fault persisted durable Status=faulted (AC a/b — Faulted != Stopped)"
    else
        fail "P6: disk Status did not become 'faulted' after teardown (got '$FAULT_STATUS')"
        cat "$CHILD_STATE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    # --- Phase 5 (T9/T10): recover-from-faulted → new --resume PID, back to alive ---
    # QUM-624 M2: the Recover precondition now keys off the liveness projection
    # (accepts Faulted/Stopped/ResumeFailed). A genuinely-faulted agent (P3) must
    # still recover in place: a NEW claude --resume subprocess replaces the dead
    # one and the agent returns to Running (process_alive=true). This subsumes the
    # recover-live row's PID-swap guard.
    echo ""
    echo "=== Phase 5 (T9/T10): recover from faulted → swap subprocess, back to Running ==="
    local RECOVER_PROMPT="Call mcp__sprawl__recover with agent_name='$CHILD_NAME'. Quote the exact tool response back to me."
    _stmux send-keys -t "$SESSION" "$RECOVER_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    if wait_for_pattern_fast "$SESSION" "Recovered backend session for $CHILD_NAME" 60; then
        pass "P5: mcp__sprawl__recover returned success ack (Faulted accepted as recover source)"
    else
        fail "P5: recover success ack did not appear within 60s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # New live claude --resume subprocess exists (PID != original). Skipped when
    # pgrep is unavailable or the original PID was never captured.
    if [ "$HAVE_PGREP" -eq 1 ] && [ -n "$ORIG_SID" ] && [ -n "$ORIG_PID" ]; then
        sleep 2
        local NEW_PID="" PROBE_END=$((SECONDS + 10))
        while [ "$SECONDS" -lt "$PROBE_END" ]; do
            NEW_PID=$(pgrep -af 'claude' | awk -v sid="$ORIG_SID" -v origpid="$ORIG_PID" '$0 ~ "--resume" && $0 ~ sid && $1 != origpid {print $1; exit}' || true)
            [ -n "$NEW_PID" ] && break
            sleep 0.5
        done
        if [ -n "$NEW_PID" ] && [ "$NEW_PID" != "$ORIG_PID" ] && kill -0 "$NEW_PID" 2>/dev/null; then
            pass "P5: new claude --resume PID=$NEW_PID alive (was $ORIG_PID)"
        else
            fail "P5: no live new claude --resume subprocess found (orig=$ORIG_PID new='$NEW_PID')"
            pgrep -af claude | head -20 >&2 || true
            e2e_print_results
            return 1
        fi
    else
        echo "  SKIP: P5 PID-swap check (pgrep unavailable or original PID not captured)"
    fi

    # process_alive back to true — the recovered agent is Running again.
    if liveness_assert_process_alive "$SESSION" "$CHILD_NAME" "ALIVECHK_P5" "true"; then
        pass "P5: process_alive=true after recover (back to Running)"
    else
        fail "P5: process_alive did not return to 'true' after recover"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # QUM-625 M4 (invariant 7): a successful recover must CLEAR the durable
    # "faulted" disk Status back to "active" — otherwise merge would reject the
    # healthy agent and a clean exit would leave it non-auto-resumable.
    local POST_RECOVER_STATUS="" rp
    for rp in 1 2 3 4 5 6 7 8 9 10; do
        POST_RECOVER_STATUS=$(jq -r '.status // empty' "$CHILD_STATE" 2>/dev/null || true)
        [ "$POST_RECOVER_STATUS" = "active" ] && break
        sleep 2
    done
    if [ "$POST_RECOVER_STATUS" = "active" ]; then
        pass "P5: disk Status cleared faulted→active after recover (invariant 7)"
    else
        fail "P5: disk Status did not clear to 'active' after recover (got '$POST_RECOVER_STATUS')"
        cat "$CHILD_STATE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    # --- Phase 2 (T4/T5): drive an autonomous turn → in_autonomous_turn flips ---
    # in_autonomous_turn is surfaced by mcp__sprawl__peek and reflects the live
    # backend Session.InAutonomousTurn() — true while an SDK-initiated turn frame
    # is open (system:init with no pending sprawl StartTurn). We send the
    # recovered child a long-running work prompt and poll peek during the window:
    #   T4: in_autonomous_turn=true  while the child is mid-turn
    #   T5: in_autonomous_turn=false after it settles
    echo ""
    echo "=== Phase 2 (T4/T5): drive a long turn → peek in_autonomous_turn true→false ==="
    local P2_WORK_PROMPT="Call mcp__sprawl__send_message with to='$CHILD_NAME' and message set to exactly: 'Count slowly from 1 to 30, printing each number on its own line, and pause briefly between each number so this takes several seconds. Do not call any tools while counting.' Confirm you sent it."
    _stmux send-keys -t "$SESSION" "$P2_WORK_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    # Poll peek repeatedly for in_autonomous_turn=true while the child works.
    # Each poll uses a fresh token so stale scrollback can't false-match.
    local P2_SAW_TRUE=0 p2i
    for p2i in 1 2 3 4 5 6 7 8 9 10 11 12; do
        if liveness_assert_peek_autonomous "$SESSION" "$CHILD_NAME" "P2CHK_BUSY_${p2i}" "true" 10; then
            P2_SAW_TRUE=1
            break
        fi
    done
    if [ "$P2_SAW_TRUE" -eq 1 ]; then
        pass "P2: in_autonomous_turn=true observed mid-turn (T4 Running·AutonomousTurn)"
        # T5: after the turn settles, it must drop back to false.
        local P2_SAW_FALSE=0 p2j
        for p2j in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
            if liveness_assert_peek_autonomous "$SESSION" "$CHILD_NAME" "P2CHK_IDLE_${p2j}" "false" 10; then
                P2_SAW_FALSE=1
                break
            fi
        done
        if [ "$P2_SAW_FALSE" -eq 1 ]; then
            pass "P2: in_autonomous_turn settled back to false (T5 → Running idle)"
        else
            fail "P2: in_autonomous_turn never returned to false after the turn (T5)"
            capture_pane "$SESSION" | tail -60 >&2
            e2e_print_results
            return 1
        fi
    else
        # FEASIBILITY GATE (QUM-627) — CONFIRMED INFEASIBLE BY LIVE RUN.
        #
        # in_autonomous_turn==true requires an SDK-initiated turn frame
        # (system:init with NO pending sprawl StartTurn). A child driven via
        # mcp__sprawl__send_message goes through StartTurn, which is explicitly
        # NOT autonomous — see the source-of-truth contract test
        # internal/backend/session_test.go::TestSession_InAutonomousTurn_DuringSprawlTurn
        # ("sprawl-initiated turns are not autonomous"). The unit-level T4/T5
        # transitions are covered by
        # internal/supervisor/liveness/liveness_test.go (T4_running_to_autonomous /
        # T5_autonomous_to_running) and the projection path by
        # internal/supervisor/real_status_faulted_test.go (autonomous-turn → Running).
        #
        # A genuine live attempt (12 peek polls during a multi-second child work
        # prompt) observed in_autonomous_turn=false on every poll — there is no
        # operator-drivable way to open an autonomous frame from one TUI session.
        # Per the QUM-627 weave directive: do NOT fake/stub P2. It is left as a
        # documented SKIP pending escalation to tower; the true-branch assertion
        # above stays wired so P2 becomes a real green the moment an
        # autonomous-turn driver exists. This branch asserts NOTHING false.
        echo "  SKIP: P2 (T4/T5) — in_autonomous_turn=true is NOT drivable via sprawl send_message"
        echo "        (live-confirmed: 12 peek polls all returned false). ESCALATE to tower."
        echo "        Unit coverage: internal/supervisor/liveness/liveness_test.go (T4/T5 cases)"
        echo "        + internal/backend/session_test.go::TestSession_InAutonomousTurn_DuringAutonomousTurn."
    fi

    # --- Phase 10 (T16): kill the (recovered, Running) child → absorbing Killed ---
    # QUM-627: a terminal operator kill is an absorbing sink. We kill the child
    # that P5 brought back to Running and assert (a) durable disk Status="killed"
    # and (b) process_alive flips to false / the runtime is torn down — and stays
    # there (no further liveness transitions). This is the last phase, so killing
    # the live child is safe.
    echo ""
    echo "=== Phase 10 (T16): kill recovered child → durable Status=killed, process_alive=false (absorbing) ==="
    local KILL_PROMPT="Call mcp__sprawl__kill with agent_name='$CHILD_NAME'. Quote the exact tool response back to me."
    _stmux send-keys -t "$SESSION" "$KILL_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    if wait_for_pattern_fast "$SESSION" "Killed agent $CHILD_NAME" 60; then
        pass "P10: mcp__sprawl__kill returned success ack"
    else
        fail "P10: kill success ack did not appear within 60s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # Durable disk Status must reach "killed" (kill writes it).
    local KILL_STATUS="" kp
    for kp in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
        KILL_STATUS=$(jq -r '.status // empty' "$CHILD_STATE" 2>/dev/null || true)
        [ "$KILL_STATUS" = "killed" ] && break
        sleep 2
    done
    if [ "$KILL_STATUS" = "killed" ]; then
        pass "P10: disk Status=killed (T16 terminal sink recorded durably)"
    else
        fail "P10: disk Status did not become 'killed' after kill (got '$KILL_STATUS')"
        cat "$CHILD_STATE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    # process_alive must be false for a killed agent (runtime torn down). null is
    # also acceptable (the runtime may be fully deregistered). The assertion is
    # that it is NOT 'true'.
    if liveness_assert_process_alive "$SESSION" "$CHILD_NAME" "ALIVECHK_P10" "(false|null)"; then
        pass "P10: process_alive is not 'true' after kill (Killed is absorbing — no live process)"
    else
        fail "P10: process_alive did not flip away from 'true' after kill"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # Absorbing check: disk Status must remain "killed" (no further transitions).
    sleep 3
    local KILL_STATUS_AGAIN
    KILL_STATUS_AGAIN=$(jq -r '.status // empty' "$CHILD_STATE" 2>/dev/null || true)
    if [ "$KILL_STATUS_AGAIN" = "killed" ]; then
        pass "P10: disk Status stayed 'killed' (T16 sink is absorbing)"
    else
        fail "P10: disk Status left 'killed' after settle (got '$KILL_STATUS_AGAIN') — sink not absorbing"
        e2e_print_results
        return 1
    fi

    # --- Remaining phases: documented SKIP with NAMED concrete alt coverage. ---
    # Each skip below points at a real file::TestName (verified to exist) or a
    # matrix row so a reader can follow it to its proof. These transitions are
    # not deterministically drivable from a single live TUI session.
    echo ""
    echo "=== Phase 4 (re-fault re-fires banner, QUM-602): SKIP — covered by 'notify-tui' matrix row + unit test internal/tui/backend_fault_msg_test.go::TestAppModel_BackendFaultMsg_RefiresBannerOnRepeat (and ::TestAppModel_BackendFaultMsg_RefiresAfterClear) ==="
    echo "=== Phase 7 (stop → Stopped != Faulted, T7/T8): SKIP — no MCP 'stop' tool to drive a deliberate stop from weave; covered by unit tests internal/supervisor/runtime_durable_fault_test.go::TestWatchHandleExit_CleanStopRecordsStopped + internal/supervisor/runtime_recover_test.go::TestAgentRuntime_Recover_DeliberateStopRejected ==="
    echo "=== Phase 8 (suspend/resume cross-process, T12/T13/T14): SKIP — needs a full sprawl exit + re-enter (Shutdown→RecoverAgents); covered by unit tests internal/supervisor/real_recover_agents_test.go::TestRealRecoverAgents_CrashSurvivorActiveResumes + ::TestRealRecoverAgents_OnSuccessSetsStatusActiveAndSavesSessionID + suspend/crash-survivor migration round-trip internal/state/migrate_test.go::TestLoadAgent_MigratePreservesActiveCrashSurvivor ==="
    echo "=== Phase 9 (resume-failed, T15): SKIP — needs resume-cookie poisoning + restart; covered by unit tests internal/supervisor/runtime_recover_test.go::TestAgentRuntime_Recover_ResumeFailedSource_Accepted + internal/supervisor/real_recover_agents_test.go::TestRealRecoverAgents_OnResumeFailureFlipsStatusToResumeFailed ==="

    e2e_print_results
}

# liveness_assert_process_alive SESSION CHILD_NAME TOKEN EXPECTED
# Prompts weave to call mcp__sprawl__status and echo a single-line sentinel
# "<TOKEN> process_alive=<value>" for CHILD_NAME, then waits for the expected
# value. TOKEN must be unique per call so stale scrollback can't false-match.
liveness_assert_process_alive() {
    local session="$1" child="$2" token="$3" expected="$4"
    local prompt="Call mcp__sprawl__status. In the JSON result, find the agent whose name is '$child' and read its process_alive field. Then reply with EXACTLY one line and nothing else: '${token} process_alive=<value>' where <value> is true, false, or null — exactly as it appears in the JSON for that agent."
    _stmux send-keys -t "$session" "$prompt"
    sleep 0.5
    _stmux send-keys -t "$session" Enter
    wait_for_pattern_fast "$session" "${token} process_alive=${expected}" 60
}

# liveness_assert_peek_autonomous SESSION CHILD_NAME TOKEN EXPECTED [TIMEOUT]
# Prompts weave to call mcp__sprawl__peek on CHILD_NAME and echo a single-line
# sentinel "<TOKEN> in_autonomous_turn=<value>", then waits for the expected
# value. Mirrors liveness_assert_process_alive but reads peek's
# in_autonomous_turn field (true while the child's backend session is mid an
# SDK-initiated autonomous turn). TOKEN must be unique per call.
liveness_assert_peek_autonomous() {
    local session="$1" child="$2" token="$3" expected="$4" timeout="${5:-60}"
    local prompt="Call mcp__sprawl__peek with agent='$child'. In the JSON result read the in_autonomous_turn field. Then reply with EXACTLY one line and nothing else: '${token} in_autonomous_turn=<value>' where <value> is true or false — exactly as it appears in the JSON."
    _stmux send-keys -t "$session" "$prompt"
    sleep 0.5
    _stmux send-keys -t "$session" Enter
    wait_for_pattern_fast "$session" "${token} in_autonomous_turn=${expected}" "$timeout"
}
