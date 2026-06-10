#!/usr/bin/env bash
# scripts/e2e-tests/paused-persistence.sh — QUM-741 paused-persistence +
# restart-injection e2e regression guard.
#
# Phases:
#   1. pause survives sprawl restart   : pause an idle engineer, tear down
#                                        sprawl, re-launch on same SPRAWL_ROOT,
#                                        confirm disk Status=paused untouched
#                                        and status MCP projects paused.
#   2. active agent resumes with the   : spawn an active engineer, tear down
#      restart-injection prompt          sprawl, re-launch, confirm it reaches
#                                        active again AND the canonical
#                                        RestartInjectionPrompt was injected
#                                        (search activity*.ndjson under the
#                                        agent dir).
#   3. mixed tree: paused + active     : two siblings — pause one, leave the
#      siblings → only active resumes    other active, restart sprawl, confirm
#                                        only the active sibling resumes and
#                                        the paused one stays paused.
#
# CRITICAL: all 3 phases share ONE $SPRAWL_ROOT (one e2e_make_sandbox_root
# call) so the .sprawl/agents/ state survives sprawl process restarts.
# Only the tmux session is torn down + relaunched between phases.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# pause_find_child_by_branch BRANCH — wait until a non-weave state.json
# appears matching the supplied branch. Echoes the path. (Copied inline from
# pause-lifecycle.sh per the QUM-741 spec — no shared lib introduced.)
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

# pause_wait_active STATE_PATH [TIMEOUT_SECONDS]
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

# pause_wait_status STATE_PATH EXPECTED [TIMEOUT]
pause_wait_status() {
    local state_path="$1" expected="$2" timeout="${3:-30}" elapsed=0 status=""
    while [ "$elapsed" -lt "$timeout" ]; do
        status=$(jq -r '.status // empty' "$state_path" 2>/dev/null || true)
        [ "$status" = "$expected" ] && return 0
        sleep 1
        elapsed=$((elapsed + 1))
    done
    return 1
}

# pause_pgrep_claude SID — print PID whose argv contains the session_id.
pause_pgrep_claude() {
    local sid="$1"
    command -v pgrep >/dev/null 2>&1 || return 0
    pgrep -af 'claude' 2>/dev/null | awk -v sid="$sid" '$0 ~ sid {print $1; exit}'
}

# pause_assert_status SESSION CHILD TOKEN EXPECTED [TIMEOUT]
# Drives weave to mcp__sprawl__status and echo a sentinel; waits for it.
pause_assert_status() {
    local session="$1" child="$2" token="$3" expected="$4" timeout="${5:-60}"
    local prompt="Call mcp__sprawl__status. In the JSON result, find the agent whose name is '$child' and read its status field. Then reply with EXACTLY one line and nothing else: '${token} status=<value>' where <value> is exactly the string in the status field."
    _stmux send-keys -t "$session" "$prompt"
    sleep 0.5
    _stmux send-keys -t "$session" Enter
    wait_for_pattern_fast "$session" "${token} status=${expected}" "$timeout"
}

# shutdown_sprawl SESSION — SIGKILL the sprawl process running in the tmux
# pane, simulating an UNCLEAN crash (rather than a clean Ctrl-C+y exit).
# This is critical for the QUM-741 e2e: clean shutdown runs the shutdown
# loop which pauses idle agents → they end up persisted as Status=paused
# and become indistinguishable from explicitly-paused agents on restart.
# An unclean crash (SIGKILL) preserves the disk Status as-is, so:
#   - explicitly-paused agents stay Status=paused (RecoverAgents skips)
#   - active agents stay Status=active as crash survivors
#     (RecoverAgents resumes them with RestartInjectionPrompt)
# This matches the conceptual semantics of QUM-723's restart-injection.
shutdown_sprawl() {
    local session="$1"
    # Locate the bash shell pid running inside the tmux pane.
    local pane_pid sprawl_pid claude_pids=""
    pane_pid=$(_stmux list-panes -t "$session" -F '#{pane_pid}' 2>/dev/null | head -1)
    if [ -n "$pane_pid" ] && command -v pgrep >/dev/null 2>&1; then
        # Collect the FULL descendant tree (sprawl + claude subprocesses)
        # BEFORE killing anything. Order matters: once sprawl dies, its
        # claude children are reparented to init, so we lose the linkage.
        local all_descendants=""
        local frontier="$pane_pid" next_frontier
        while [ -n "$frontier" ]; do
            next_frontier=""
            for pid in $frontier; do
                local kids
                kids=$(pgrep -P "$pid" 2>/dev/null || true)
                if [ -n "$kids" ]; then
                    all_descendants="$all_descendants $kids"
                    next_frontier="$next_frontier $kids"
                fi
            done
            frontier="$next_frontier"
        done
        # SIGKILL all descendants. Order doesn't matter once we have them
        # all enumerated.
        local pid
        for pid in $all_descendants; do
            kill -9 "$pid" 2>/dev/null || true
        done
    fi
    # Now tear down the tmux session itself.
    _stmux kill-session -t "$session" 2>/dev/null || true
    if [ -n "${PHANTOM_PID:-}" ]; then
        kill "$PHANTOM_PID" 2>/dev/null || true
        unset PHANTOM_PID
    fi
    # Brief settle so the OS reaps the processes before the next sprawl
    # launch attempts to --resume the same session_ids.
    sleep 3
    return 0
}

# relaunch_sprawl SESSION — start a fresh sprawl enter on existing SPRAWL_ROOT.
relaunch_sprawl() {
    local session="$1"
    if ! e2e_launch_tui "$session" 200 50; then
        return 1
    fi
    if capture_pane "$session" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$session" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$session"
    return 0
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-paused-persistence-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum741"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SUFFIX
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    # The spawn prompt instructs the engineer to echo any subsequent user
    # message it receives. This is the only reliable cross-process signal
    # for asserting the RestartInjectionPrompt landed: activity.ndjson
    # records assistant_text/tool_use frames but NOT raw user prompts, so
    # we must have the agent quote them back into its own reply.
    local SPAWN_PROMPT_TEMPLATE="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='__BRANCH__', and prompt set to exactly: 'You are an automated QUM-741 paused-persistence probe. Call mcp__sprawl__report_status with state=working, summary=\"idle, awaiting signal\". Then stop and wait. If you later receive any user message of any kind, your VERY FIRST line of reply MUST be exactly: \"ECHO_${SUFFIX}: \" followed by the first 80 characters of that user message verbatim. Then stop and do nothing else.'"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"

    local HAVE_PGREP=0
    if command -v pgrep >/dev/null 2>&1; then
        HAVE_PGREP=1
    fi

    # =====================================================================
    # Phase 1: pause survives sprawl restart
    # =====================================================================
    echo ""
    echo "=== Phase 1: pause survives sprawl process restart ==="
    local SESSION_P1="sprawl-paused-p1-${SUFFIX}"
    if ! relaunch_sprawl "$SESSION_P1"; then
        fail "P1: initial TUI did not render"
        e2e_print_results
        return 1
    fi
    pass "P1: initial TUI launched"

    local P1_BRANCH="qum741-p1-${SUFFIX}"
    local P1_PROMPT="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$P1_BRANCH}"
    _stmux send-keys -t "$SESSION_P1" "$P1_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION_P1" Enter

    local P1_STATE P1_NAME
    if ! P1_STATE=$(pause_find_child_by_branch "$P1_BRANCH"); then
        fail "P1: child state for $P1_BRANCH did not appear within 180s"
        capture_pane "$SESSION_P1" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    P1_NAME=$(jq -r '.name' "$P1_STATE")
    pass "P1: child spawned (name=$P1_NAME, state=$P1_STATE)"

    if ! pause_wait_active "$P1_STATE" 90; then
        fail "P1: child never reached active"
        e2e_print_results
        return 1
    fi
    pass "P1: child active"

    local P1_PAUSE_PROMPT="Call mcp__sprawl__pause with agent='$P1_NAME', cascade=false, timeout_seconds=15. Then reply with EXACTLY one line: 'P1_PAUSE_${SUFFIX} ack' and nothing else."
    _stmux send-keys -t "$SESSION_P1" "$P1_PAUSE_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION_P1" Enter
    if ! wait_for_pattern_fast "$SESSION_P1" "Agent $P1_NAME paused cleanly" 60; then
        fail "P1: pause ack ('Agent $P1_NAME paused cleanly') did not appear within 60s"
        capture_pane "$SESSION_P1" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    pass "P1: pause tool returned 'paused cleanly'"

    if ! pause_wait_status "$P1_STATE" "paused" 15; then
        fail "P1: disk Status did not reach 'paused' within 15s"
        cat "$P1_STATE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    pass "P1: disk Status=paused"

    # Capture session_id and claude PID before teardown for the optional
    # subprocess-died assertion.
    local P1_SID="" P1_PID=""
    P1_SID=$(jq -r '.session_id // empty' "$P1_STATE" 2>/dev/null || true)
    if [ "$HAVE_PGREP" -eq 1 ] && [ -n "$P1_SID" ]; then
        P1_PID=$(pause_pgrep_claude "$P1_SID")
    fi

    echo "  P1: tearing down sprawl process (session=$SESSION_P1)"
    if shutdown_sprawl "$SESSION_P1"; then
        pass "P1: sprawl process SIGKILLed (simulated crash)"
    else
        echo "  NOTE: P1 shutdown did not complete within 15s; forced kill"
    fi

    # Optional: assert paused claude subprocess died with sprawl.
    if [ -n "$P1_PID" ]; then
        local p1_exited=0 i
        for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
            if ! kill -0 "$P1_PID" 2>/dev/null; then
                p1_exited=1
                break
            fi
            sleep 1
        done
        if [ "$p1_exited" -eq 1 ]; then
            pass "P1: claude subprocess (PID=$P1_PID) exited with sprawl"
        else
            echo "  NOTE: P1 claude PID=$P1_PID still alive after 15s (non-fatal)"
        fi
    fi

    # Re-launch sprawl on the SAME SPRAWL_ROOT.
    local SESSION_P1B="sprawl-paused-p1b-${SUFFIX}"
    echo "  P1: re-launching sprawl on same SPRAWL_ROOT (session=$SESSION_P1B)"
    if ! relaunch_sprawl "$SESSION_P1B"; then
        fail "P1: post-restart TUI did not render"
        e2e_print_results
        return 1
    fi
    pass "P1: post-restart TUI launched"

    # Settle: give RecoverAgents a moment to finish its scan (paused must be
    # SKIPPED — if the skip logic regressed, we'd see Status flip to active).
    sleep 5

    local P1_POST_DISK
    P1_POST_DISK=$(jq -r '.status // empty' "$P1_STATE" 2>/dev/null || true)
    if [ "$P1_POST_DISK" = "paused" ]; then
        pass "P1: disk Status=paused survives sprawl restart"
    else
        fail "P1: post-restart disk Status='$P1_POST_DISK' (want 'paused' — paused must NOT auto-resume)"
        cat "$P1_STATE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    if pause_assert_status "$SESSION_P1B" "$P1_NAME" "P1_POST_${SUFFIX}" "paused" 60; then
        pass "P1: mcp__sprawl__status projects status=paused post-restart"
    else
        fail "P1: mcp__sprawl__status did not project status=paused"
        capture_pane "$SESSION_P1B" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # =====================================================================
    # Phase 2: active agent gets restart-injection prompt on resume
    # =====================================================================
    echo ""
    echo "=== Phase 2: active agent gets restart-injection prompt on resume ==="
    # Spawn second engineer in the P1B session.
    local P2_BRANCH="qum741-p2-${SUFFIX}"
    local P2_PROMPT="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$P2_BRANCH}"
    _stmux send-keys -t "$SESSION_P1B" "$P2_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION_P1B" Enter

    local P2_STATE P2_NAME
    if ! P2_STATE=$(pause_find_child_by_branch "$P2_BRANCH"); then
        fail "P2: child state for $P2_BRANCH did not appear within 180s"
        capture_pane "$SESSION_P1B" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    P2_NAME=$(jq -r '.name' "$P2_STATE")
    pass "P2: child spawned (name=$P2_NAME)"

    if ! pause_wait_active "$P2_STATE" 90; then
        fail "P2: child never reached active pre-restart"
        e2e_print_results
        return 1
    fi
    pass "P2: child active pre-restart"

    # Tear down sprawl.
    echo "  P2: tearing down sprawl (session=$SESSION_P1B)"
    if shutdown_sprawl "$SESSION_P1B"; then
        pass "P2: sprawl process SIGKILLed (simulated crash)"
    else
        echo "  NOTE: P2 shutdown forced"
    fi

    # Re-launch.
    local SESSION_P2B="sprawl-paused-p2b-${SUFFIX}"
    echo "  P2: re-launching sprawl on same SPRAWL_ROOT (session=$SESSION_P2B)"
    if ! relaunch_sprawl "$SESSION_P2B"; then
        fail "P2: post-restart TUI did not render"
        e2e_print_results
        return 1
    fi
    pass "P2: post-restart TUI launched"

    if ! pause_wait_active "$P2_STATE" 90; then
        fail "P2: child did not reach active post-restart (RecoverAgents resume failed?)"
        cat "$P2_STATE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    pass "P2: child reached active post-restart (auto-resumed)"

    # Assert that the canonical RestartInjectionPrompt landed in the
    # resumed claude session. spec.RestartInjection flows into the
    # initialPrompt override in runtime_launcher.go and becomes the
    # resumed agent's first post-restart user message. activity.ndjson
    # records assistant frames but NOT user prompts, so we rely on the
    # spawn prompt's echo-back contract: the agent quotes the start of
    # the next user message back as "ECHO_<SUFFIX>: <first 80 chars>".
    # We grep for ECHO + the first ~30 chars of the canonical literal.
    local CANONICAL_PREFIX="Sprawl was just restarted"
    local ECHO_TAG="ECHO_${SUFFIX}: ${CANONICAL_PREFIX}"
    local AGENT_DIR="$SPRAWL_ROOT/.sprawl/agents/$P2_NAME"
    local found=0 elapsed=0 hit=""
    while [ "$elapsed" -lt 120 ]; do
        if [ -d "$AGENT_DIR" ]; then
            hit=$(find "$AGENT_DIR" -maxdepth 2 -name 'activity*.ndjson' -print0 2>/dev/null \
                | xargs -0 grep -lF "$ECHO_TAG" 2>/dev/null | head -1 || true)
            if [ -n "$hit" ]; then
                found=1
                break
            fi
        fi
        sleep 3
        elapsed=$((elapsed + 3))
    done
    if [ "$found" -eq 1 ]; then
        pass "P2: agent echoed restart-injection prompt back (found '$ECHO_TAG' in $hit)"
    else
        fail "P2: agent did not echo the RestartInjection literal back within 120s (looking for '$ECHO_TAG')"
        echo "  Agent dir contents:" >&2
        ls -la "$AGENT_DIR" >&2 2>/dev/null || true
        echo "  activity tail:" >&2
        find "$AGENT_DIR" -maxdepth 2 -name 'activity*.ndjson' -print 2>/dev/null \
            | head -3 | while read -r f; do tail -20 "$f" >&2; done
        e2e_print_results
        return 1
    fi

    # =====================================================================
    # Phase 3: mixed tree — paused + active siblings → only active resumes
    # =====================================================================
    echo ""
    echo "=== Phase 3: mixed tree — paused + active siblings ==="
    # Tear down and re-launch for a clean phase.
    echo "  P3: tearing down sprawl (session=$SESSION_P2B)"
    if shutdown_sprawl "$SESSION_P2B"; then
        pass "P3: sprawl process SIGKILLed before P3 setup"
    else
        echo "  NOTE: P3 shutdown forced"
    fi
    local SESSION_P3="sprawl-paused-p3-${SUFFIX}"
    if ! relaunch_sprawl "$SESSION_P3"; then
        fail "P3: TUI did not render for P3 setup"
        e2e_print_results
        return 1
    fi
    pass "P3: TUI launched for P3 setup"

    # Spawn paused-sibling.
    local P3_PAUSED_BRANCH="qum741-p3-paused-${SUFFIX}"
    local P3_PAUSED_PROMPT="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$P3_PAUSED_BRANCH}"
    _stmux send-keys -t "$SESSION_P3" "$P3_PAUSED_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION_P3" Enter

    local P3_PAUSED_STATE P3_PAUSED_NAME
    if ! P3_PAUSED_STATE=$(pause_find_child_by_branch "$P3_PAUSED_BRANCH"); then
        fail "P3: paused-sibling state did not appear"
        e2e_print_results
        return 1
    fi
    P3_PAUSED_NAME=$(jq -r '.name' "$P3_PAUSED_STATE")
    pass "P3: paused-sibling spawned (name=$P3_PAUSED_NAME)"
    if ! pause_wait_active "$P3_PAUSED_STATE" 90; then
        fail "P3: paused-sibling never reached active"
        e2e_print_results
        return 1
    fi

    # Spawn active-sibling.
    local P3_ACTIVE_BRANCH="qum741-p3-active-${SUFFIX}"
    local P3_ACTIVE_PROMPT="${SPAWN_PROMPT_TEMPLATE/__BRANCH__/$P3_ACTIVE_BRANCH}"
    _stmux send-keys -t "$SESSION_P3" "$P3_ACTIVE_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION_P3" Enter

    local P3_ACTIVE_STATE P3_ACTIVE_NAME
    if ! P3_ACTIVE_STATE=$(pause_find_child_by_branch "$P3_ACTIVE_BRANCH"); then
        fail "P3: active-sibling state did not appear"
        e2e_print_results
        return 1
    fi
    P3_ACTIVE_NAME=$(jq -r '.name' "$P3_ACTIVE_STATE")
    pass "P3: active-sibling spawned (name=$P3_ACTIVE_NAME)"
    if ! pause_wait_active "$P3_ACTIVE_STATE" 90; then
        fail "P3: active-sibling never reached active"
        e2e_print_results
        return 1
    fi

    # Pause the first one.
    local P3_PAUSE="Call mcp__sprawl__pause with agent='$P3_PAUSED_NAME', cascade=false, timeout_seconds=15. Then reply 'P3_PAUSE_${SUFFIX} ack' and nothing else."
    _stmux send-keys -t "$SESSION_P3" "$P3_PAUSE"
    sleep 0.5
    _stmux send-keys -t "$SESSION_P3" Enter
    if ! wait_for_pattern_fast "$SESSION_P3" "Agent $P3_PAUSED_NAME paused cleanly" 60; then
        fail "P3: pause ack for $P3_PAUSED_NAME did not appear"
        capture_pane "$SESSION_P3" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    if ! pause_wait_status "$P3_PAUSED_STATE" "paused" 15; then
        fail "P3: paused-sibling disk Status did not reach 'paused'"
        e2e_print_results
        return 1
    fi
    pass "P3: paused-sibling reached disk Status=paused"

    # Tear down sprawl.
    echo "  P3: tearing down sprawl"
    if shutdown_sprawl "$SESSION_P3"; then
        pass "P3: sprawl process SIGKILLed before restart assertion"
    else
        echo "  NOTE: P3 shutdown forced"
    fi

    # Re-launch on the same SPRAWL_ROOT.
    local SESSION_P3B="sprawl-paused-p3b-${SUFFIX}"
    echo "  P3: re-launching sprawl (session=$SESSION_P3B)"
    if ! relaunch_sprawl "$SESSION_P3B"; then
        fail "P3: post-restart TUI did not render"
        e2e_print_results
        return 1
    fi
    pass "P3: post-restart TUI launched"

    # First wait for the active-sibling to resume. This proves RecoverAgents
    # has actually run its scan (and that the active sibling was eligible).
    # Without this gate, the paused-stays-paused check below could pass
    # vacuously by reading the status before RecoverAgents has even
    # iterated.
    if ! pause_wait_active "$P3_ACTIVE_STATE" 90; then
        fail "P3: active-sibling did not reach active post-restart (RecoverAgents failed to resume)"
        cat "$P3_ACTIVE_STATE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    pass "P3: active-sibling resumed to Status=active post-restart"

    # Cross-check: the resumed active-sibling must also see the
    # RestartInjection (same echo-back contract as P2). This verifies
    # the injection isn't lost when a paused sibling is present in the
    # BFS pass.
    local P3_AGENT_DIR="$SPRAWL_ROOT/.sprawl/agents/$P3_ACTIVE_NAME"
    local p3_found=0 p3_elapsed=0 p3_hit=""
    while [ "$p3_elapsed" -lt 120 ]; do
        if [ -d "$P3_AGENT_DIR" ]; then
            p3_hit=$(find "$P3_AGENT_DIR" -maxdepth 2 -name 'activity*.ndjson' -print0 2>/dev/null \
                | xargs -0 grep -lF "$ECHO_TAG" 2>/dev/null | head -1 || true)
            if [ -n "$p3_hit" ]; then
                p3_found=1
                break
            fi
        fi
        sleep 3
        p3_elapsed=$((p3_elapsed + 3))
    done
    if [ "$p3_found" -eq 1 ]; then
        pass "P3: active-sibling echoed RestartInjection literal ($p3_hit)"
    else
        fail "P3: active-sibling did not echo RestartInjection literal within 120s (looking for '$ECHO_TAG')"
        ls -la "$P3_AGENT_DIR" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    # Now poll the paused-sibling for the FULL 30s window. We expect it
    # to remain Status=paused for the entire window (any flip is a
    # regression). Fail-fast on the first non-paused observation.
    local p3_paused_disk="" p3_paused_elapsed=0
    while [ "$p3_paused_elapsed" -lt 30 ]; do
        p3_paused_disk=$(jq -r '.status // empty' "$P3_PAUSED_STATE" 2>/dev/null || true)
        if [ -n "$p3_paused_disk" ] && [ "$p3_paused_disk" != "paused" ]; then
            fail "P3: paused-sibling flipped to '$p3_paused_disk' post-restart (want still 'paused')"
            cat "$P3_PAUSED_STATE" >&2 2>/dev/null || true
            e2e_print_results
            return 1
        fi
        sleep 2
        p3_paused_elapsed=$((p3_paused_elapsed + 2))
    done
    if [ "$p3_paused_disk" = "paused" ]; then
        pass "P3: paused-sibling Status=paused survives restart (30s settle, RecoverAgents proven to have run)"
    else
        fail "P3: paused-sibling final Status='$p3_paused_disk' (want 'paused')"
        cat "$P3_PAUSED_STATE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    # Teardown of last session — let cleanup traps handle it, but be polite.
    shutdown_sprawl "$SESSION_P3B" || true

    e2e_print_results
}
