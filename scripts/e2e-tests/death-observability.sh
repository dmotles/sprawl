#!/usr/bin/env bash
# scripts/e2e-tests/death-observability.sh — QUM-725 death TUI toast +
# route-up-on-dead-recipient regression guard. QUM-745 flesh-out.
#
# Phases (per QUM-725 §Validation):
#   1. toast-on-death:        Spawn engineer; SIGKILL its claude PID; assert
#                             disk liveness=died and the TUI surfaces the
#                             death toast.
#   2. toast-persists-on-focus-cycle:
#                             Press Ctrl+N to cycle the observed agent;
#                             assert toast remains visible in the pane.
#   3. route-up-single-hop:   Engineer dies (parent=weave); weave's
#                             send_message to the dead engineer wraps and
#                             lands in weave's own pending queue with the
#                             "This message was sent to … is dead" prefix.
#   4. route-up-multi-hop:    Spawn manager+engineer chain. Kill BOTH the
#                             engineer and the manager. From a sibling, send
#                             to the dead engineer; assert the wrapper lands
#                             in weave's queue enumerating both dead names.
#   5. report-status-to-dead: Same multi-hop setup; sibling report_status
#                             to dead engineer routes wrapped to weave.
#
# Helper-level correctness for the wrapper + AgentDiedMsg dispatch is
# covered by unit tests in:
#   - internal/inboxprompt/dead_routing_test.go
#   - internal/supervisor/dead_routing_test.go
#   - internal/supervisor/real_dead_routing_test.go
#   - internal/tui/death_toast_test.go
# This row gates the live wiring: registry-subscriber → TUI toast and
# SendMessage / ReportStatus dead-recipient detection through real MCP.
#
# KNOWN GAP (QUM-745 implementation discovery, 2026-06-10):
# Phase 1's `SIGKILL claude PID → disk status=died` transition does not
# currently fire end-to-end for an idle agent. Tracing the impl:
#
#   1. SIGKILL of the claude subprocess triggers transport.Recv EOF in
#      session.runReader (backend/session.go:579).
#   2. runReader sets s.fatalErr (NOT s.terminalErr) and returns;
#      s.readerDone closes.
#   3. session.SetTerminalErrorHandler (the only path that cancels the
#      runtime ctx so loopWG completes and rt.done closes — see
#      runtime/unified.go:99-137) is wired to setTerminalErr, NOT
#      setFatalErr. setTerminalErr is invoked only from the
#      SubscriberWedged / HangTimeout / InduceTerminalFault paths, not
#      from the plain EOF path.
#   4. The turn loop's outer Run loop (runtime/turnloop.go:112-144) only
#      exits on ctx.Done() or queue signal — never on session EOF — so
#      an idle SIGKILL leaves rt.loopWG blocked, rt.done open, and
#      supervisor.watchHandleExit (the only thing that stamps disk
#      Status=died) structurally unable to fire.
#
# Phases 2-5 transitively depend on Phase 1 (they need a Died target).
# This row is therefore currently expected to fail Phase 1 until the
# underlying "EOF → terminal-fault" gap in backend/session.runReader is
# closed (filed as a follow-up — see ratz's report on QUM-745).
# The script remains structurally correct: once the gap is closed the
# 5 phases exercise exactly the validation §QUM-725 calls for.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# Poll disk state until <name> has status=<want> (state.Status field, the
# durable resting status — runtime.watchHandleExit stamps "died" on the
# unexpected-exit branch). Returns 0 on success; 1 on timeout. Echoes the
# state file path on success.
_dod_wait_for_status() {
    local name="$1" want="$2" timeout="${3:-60}"
    local state_file="$SPRAWL_ROOT/.sprawl/agents/${name}.json"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        if [ -f "$state_file" ]; then
            local st
            st=$(jq -r '.status // empty' "$state_file" 2>/dev/null || true)
            if [ "$st" = "$want" ]; then
                echo "$state_file"
                return 0
            fi
        fi
        sleep 0.5
    done
    return 1
}

_dod_wait_for_died() { _dod_wait_for_status "$1" "died" "${2:-60}"; }
# "active" is the resting status stamped by runtime.start once the backend
# session has initialized; we poll for it as a kill-readiness signal so
# SIGKILL doesn't race claude's --init handshake (would classify as
# Faulted rather than Died and AgentDiedMsg never fires).
_dod_wait_for_active() { _dod_wait_for_status "$1" "active" "${2:-180}"; }

# Resolve the PID of the claude subprocess for agent <name> by matching its
# session_id (read from the agent state file) against `pgrep -af claude`.
_dod_pid_for() {
    local name="$1"
    local state_file="$SPRAWL_ROOT/.sprawl/agents/${name}.json"
    local sid
    sid=$(jq -r '.session_id // empty' "$state_file" 2>/dev/null || true)
    [ -z "$sid" ] && return 1
    pgrep -af 'claude' | awk -v sid="$sid" '$0 ~ sid {print $1; exit}'
}

# Wait for a child agent (anything other than `weave`) to appear under
# .sprawl/agents/*.json. Optional filter by `type` field. Echoes its name.
_dod_wait_for_child() {
    local timeout="${1:-180}" want_type="${2:-}" exclude="${3:-}"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        while IFS= read -r f; do
            [ -z "$f" ] && continue
            local n t
            n=$(jq -r '.name // empty' "$f" 2>/dev/null || true)
            t=$(jq -r '.type // empty' "$f" 2>/dev/null || true)
            [ -z "$n" ] && continue
            [ "$n" = "weave" ] && continue
            [ -n "$exclude" ] && [[ ",$exclude," == *",$n,"* ]] && continue
            if [ -n "$want_type" ] && [ "$t" != "$want_type" ]; then
                continue
            fi
            echo "$n"
            return 0
        done < <(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null)
        sleep 1
    done
    return 1
}

# Wait until any pending/*.json under <recipient>'s queue contains <needle> in
# its body. Returns 0 on success; 1 on timeout. Echoes the matching file path.
# Used for send_message route-up wrappers (real.go path persists via
# agentloop.Enqueue → .sprawl/agents/<n>/queue/pending/).
_dod_wait_for_inbox_body() {
    local recipient="$1" needle="$2" timeout="${3:-90}"
    local qdir="$SPRAWL_ROOT/.sprawl/agents/$recipient/queue/pending"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        if [ -d "$qdir" ]; then
            while IFS= read -r f; do
                [ -z "$f" ] && continue
                if jq -r '.body // empty' "$f" 2>/dev/null | grep -qF "$needle"; then
                    echo "$f"
                    return 0
                fi
            done < <(find "$qdir" -maxdepth 1 -name '*.json' 2>/dev/null)
        fi
        sleep 0.5
    done
    return 1
}

# Wait until any maildir envelope under <recipient>'s messages dir contains
# <needle>. Status_change envelopes (QUM-614) are written by
# messages.SendStatusChange to .sprawl/messages/<recipient>/new/ — the
# route-up dead-parent path (real.go::ReportStatus) wraps the summary and
# delivers via this maildir, NOT via the agentloop queue. Used for Phase 5.
_dod_wait_for_maildir_body() {
    local recipient="$1" needle="$2" timeout="${3:-90}"
    local mdir="$SPRAWL_ROOT/.sprawl/messages/$recipient"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        local sub
        for sub in new cur archive; do
            local d="$mdir/$sub"
            [ -d "$d" ] || continue
            while IFS= read -r f; do
                [ -z "$f" ] && continue
                if grep -qF "$needle" "$f" 2>/dev/null; then
                    echo "$f"
                    return 0
                fi
            done < <(find "$d" -maxdepth 1 -type f 2>/dev/null)
        done
        sleep 0.5
    done
    return 1
}

test_run() {
    if ! command -v pgrep >/dev/null 2>&1; then
        echo "FATAL: pgrep not on PATH" >&2
        return 1
    fi

    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-death-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum725"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi

    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SESSION="sprawl-death-e2e-$(head -c4 /dev/urandom | xxd -p)"
    local STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"
    local BRANCH_SUFFIX
    BRANCH_SUFFIX="$(head -c4 /dev/urandom | xxd -p)"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"

    echo ""
    echo "=== Launching sprawl enter ==="
    _stmux new-session -d -s "$SESSION" -x 200 -y 50 \
        "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_CLAUDE='$SPRAWL_CLAUDE' '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
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

    echo ""
    echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
    e2e_attach_phantom_client "$SESSION"

    # -----------------------------------------------------------------
    # Phase 1: toast-on-death
    # -----------------------------------------------------------------
    echo ""
    echo "=== Phase 1: spawn engineer and SIGKILL its claude ==="
    local SPAWN_PROMPT="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum-745-death-${BRANCH_SUFFIX}', and prompt set to exactly: 'You are an automated QUM-745 death probe. Call mcp__sprawl__report_status with state=working, summary=\"idle, awaiting SIGKILL\". Then stop and wait. Do nothing else until you receive a message.'"
    e2e_send_user_prompt "$SESSION" "$SPAWN_PROMPT"

    local ENGINEER
    if ! ENGINEER=$(_dod_wait_for_child 180 engineer); then
        fail "engineer child did not appear within 180s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    pass "engineer spawned: name=$ENGINEER"

    # CRITICAL: wait for the spawn MCP tool to fully complete before we
    # SIGKILL. status=active stamps as soon as the child reports_status,
    # but the spawn handshake (initialize control_request → control_response
    # round-trip on the parent's side) may still be in flight at that point;
    # killing mid-handshake causes the supervisor to surface a spawn error
    # rather than the AgentDiedMsg path. We anchor on the engineer's
    # status-change frame "<name> changed status to working", which both
    # claude (parent) and the supervisor have already observed by the time
    # it's rendered into weave's pane.
    if wait_for_substring_fast "$SESSION" "${ENGINEER} changed status to working" 120 || \
       wait_for_substring_fast "$SESSION" "Spawned engineer" 5; then
        pass "engineer spawn completed (status-change / spawn ack observed)"
    else
        fail "engineer spawn ack did not appear within 120s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    # Drain a short grace so the parent claude's spawn-MCP response has
    # committed before we SIGKILL — empirically eliminates the
    # "session reader exited before initialize" misclassification.
    sleep 5

    # Wait for the engineer's claude subprocess to register a session_id +
    # be locatable via pgrep before we attempt to SIGKILL it.
    local ENG_PID=""
    local i
    for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
        ENG_PID=$(_dod_pid_for "$ENGINEER" || true)
        [ -n "$ENG_PID" ] && break
        sleep 2
    done
    if [ -z "$ENG_PID" ]; then
        fail "could not resolve claude PID for engineer $ENGINEER"
        cat "$SPRAWL_ROOT/.sprawl/agents/${ENGINEER}.json" >&2 2>/dev/null || true
        pgrep -af claude >&2 || true
        e2e_print_results
        return 1
    fi
    pass "engineer claude PID=$ENG_PID; sending SIGKILL"
    kill -9 "$ENG_PID" 2>/dev/null || true
    # Wait for the kernel to reap the process before polling for the
    # supervisor's watchHandleExit to fire (the subprocess's Wait() in
    # backend/claude unblocks once the process is reaped).
    local kill_end=$((SECONDS + 10))
    while [ "$SECONDS" -lt "$kill_end" ]; do
        kill -0 "$ENG_PID" 2>/dev/null || break
        sleep 0.5
    done
    if kill -0 "$ENG_PID" 2>/dev/null; then
        fail "SIGKILL did not reap PID $ENG_PID within 10s"
    fi

    if _dod_wait_for_died "$ENGINEER" 120 >/dev/null; then
        pass "engineer disk state transitioned to status=died"
    else
        fail "engineer did not transition to status=died within 120s"
        echo "  agent state file:" >&2
        cat "$SPRAWL_ROOT/.sprawl/agents/${ENGINEER}.json" >&2 2>/dev/null || true
        echo "  pgrep claude tail:" >&2
        pgrep -af claude >&2 || true
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    # Pane assertion: BuildDeathToast renders "<name> (<type>) died — last
    # seen <ago>. Parent <parent> notified." Pane width may wrap or
    # truncate; assert each distinguishing fragment independently and tag
    # the failures we observe.
    if wait_for_substring_fast "$SESSION" "died" 30 && \
       wait_for_substring_fast "$SESSION" "Parent" 5 && \
       wait_for_substring_fast "$SESSION" "notified" 5; then
        pass "TUI death toast surfaced (died / Parent / notified)"
    else
        fail "TUI death toast did not surface within 30s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    # -----------------------------------------------------------------
    # Phase 2: toast-persists-on-focus-cycle
    # -----------------------------------------------------------------
    echo ""
    echo "=== Phase 2: Ctrl+N focus-cycle keeps the toast visible ==="
    _stmux send-keys -t "$SESSION" C-n
    sleep 1
    _stmux send-keys -t "$SESSION" C-n
    sleep 1
    if capture_pane "$SESSION" | grep -q "died" && \
       capture_pane "$SESSION" | grep -q "notified"; then
        pass "toast persisted across focus cycle"
    else
        fail "toast disappeared after Ctrl+N focus cycle"
        capture_pane "$SESSION" | tail -40 >&2
    fi

    # -----------------------------------------------------------------
    # Phase 3: route-up single-hop
    # -----------------------------------------------------------------
    # Engineer is dead, parent=weave. weave (which is the only live
    # ancestor) sends to engineer; WalkDeadAncestors routes to weave;
    # WrapForDeadTarget wraps the body and Enqueue lands it under weave's
    # pending queue. Caller is the TUI prompt → caller identity is "weave".
    echo ""
    echo "=== Phase 3: route-up single-hop (weave → dead engineer → weave) ==="
    local PROBE_SINGLE="QUM-745-SINGLE-$$-$(date +%s)"
    local SEND_SINGLE="Call mcp__sprawl__send_message with to='${ENGINEER}', body='${PROBE_SINGLE}', interrupt=false."
    e2e_send_user_prompt "$SESSION" "$SEND_SINGLE"

    # WrapForDeadTarget prefix: "This message was sent to <T> but <T> is dead."
    local WRAP_SINGLE="This message was sent to ${ENGINEER} but ${ENGINEER} is dead."
    if _dod_wait_for_inbox_body "weave" "$WRAP_SINGLE" 90 >/dev/null; then
        pass "single-hop wrapper landed in weave's pending queue"
    else
        fail "single-hop wrapper did NOT land in weave queue within 90s"
        echo "  weave queue tail:" >&2
        find "$SPRAWL_ROOT/.sprawl/agents/weave/queue" -name '*.json' 2>/dev/null \
            | head -10 | while read -r f; do echo "--- $f ---" >&2; jq . "$f" >&2 2>/dev/null || cat "$f" >&2; done
        capture_pane "$SESSION" | tail -40 >&2
    fi
    # Additionally assert the original body survived verbatim through the
    # wrapper (defense-in-depth — the unit test guarantees the format, this
    # confirms the live wiring didn't strip the body somewhere upstream).
    if _dod_wait_for_inbox_body "weave" "$PROBE_SINGLE" 5 >/dev/null; then
        pass "single-hop wrapper preserved original body sentinel"
    else
        fail "single-hop wrapper missing original body sentinel"
    fi

    # -----------------------------------------------------------------
    # Phase 4 + 5: route-up multi-hop (send_message) + dead-parent
    # report_status. Real.ReportStatus delivers status_change to the
    # REPORTER'S parent (no explicit `to=`); the dead-parent route-up
    # lives in real.go:1858-1874 (WalkDeadAncestors from parent). So the
    # ordering is: build a weave→manager→engineer2 chain plus a sibling
    # at the weave level, kill manager first, exercise Phase 5
    # (engineer2.report_status → wraps to weave because parent dead),
    # then kill engineer2 and exercise Phase 4 (sibling.send_message to
    # dead engineer2 with two dead names in the chain).
    echo ""
    echo "=== Phase 4+5 setup: spawn manager + nested engineer + sibling ==="
    local MGR_SPAWN="Call mcp__sprawl__spawn with family='engineering', type='manager', branch='qum-745-mgr-${BRANCH_SUFFIX}', and prompt set to exactly: 'You are an automated QUM-745 manager probe. Call mcp__sprawl__spawn with family=\"engineering\", type=\"engineer\", branch=\"qum-745-eng2-${BRANCH_SUFFIX}\", and prompt set to exactly: \"You are an automated QUM-745 nested engineer. Call mcp__sprawl__report_status with state=working, summary=idle. Then stop.\". Then stop and wait for messages.'"
    e2e_send_user_prompt "$SESSION" "$MGR_SPAWN"

    local MANAGER ENG2 SIBLING
    if ! MANAGER=$(_dod_wait_for_child 180 manager "${ENGINEER}"); then
        fail "manager child did not appear within 180s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    pass "manager spawned: name=$MANAGER"

    if ! ENG2=$(_dod_wait_for_child 240 engineer "${ENGINEER}"); then
        fail "nested engineer (child of manager) did not appear within 240s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    pass "nested engineer spawned: name=$ENG2"

    local SIBLING_SPAWN="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum-745-sib-${BRANCH_SUFFIX}', and prompt set to exactly: 'You are an automated QUM-745 sibling probe. Call mcp__sprawl__report_status with state=working, summary=\"sibling idle\". Then stop and wait for messages.'"
    e2e_send_user_prompt "$SESSION" "$SIBLING_SPAWN"

    if ! SIBLING=$(_dod_wait_for_child 240 engineer "${ENGINEER},${ENG2}"); then
        fail "sibling engineer did not appear within 240s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    pass "sibling spawned: name=$SIBLING"

    # Wait for both target PIDs to materialize.
    local ENG2_PID="" MGR_PID=""
    for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
        [ -z "$ENG2_PID" ] && ENG2_PID=$(_dod_pid_for "$ENG2" || true)
        [ -z "$MGR_PID" ] && MGR_PID=$(_dod_pid_for "$MANAGER" || true)
        [ -n "$ENG2_PID" ] && [ -n "$MGR_PID" ] && break
        sleep 2
    done
    if [ -z "$ENG2_PID" ] || [ -z "$MGR_PID" ]; then
        fail "could not resolve PIDs (manager=$MGR_PID engineer2=$ENG2_PID)"
        pgrep -af claude >&2 || true
        e2e_print_results
        return 1
    fi
    pass "manager PID=$MGR_PID; nested engineer PID=$ENG2_PID"

    # Wait for both to be fully initialized before SIGKILL. We can't watch
    # weave's pane here — the nested engineer's status-change envelope
    # routes to its parent (manager), not to weave. Probe disk state
    # directly: both reach status=active once their runtime start has
    # completed and the first report_status has landed.
    if _dod_wait_for_active "$MANAGER" 180 >/dev/null && \
       _dod_wait_for_active "$ENG2" 180 >/dev/null; then
        pass "manager + nested engineer both reached status=active"
    else
        fail "manager/nested engineer never reached status=active within 180s"
        echo "  manager state:" >&2
        cat "$SPRAWL_ROOT/.sprawl/agents/${MANAGER}.json" >&2 2>/dev/null || true
        echo "  nested engineer state:" >&2
        cat "$SPRAWL_ROOT/.sprawl/agents/${ENG2}.json" >&2 2>/dev/null || true
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    sleep 5

    echo ""
    echo "=== Phase 5 prep: SIGKILL manager (engineer2 stays alive) ==="
    kill -9 "$MGR_PID" 2>/dev/null || true
    if _dod_wait_for_died "$MANAGER" 60 >/dev/null; then
        pass "manager transitioned to liveness=died"
    else
        fail "manager did not transition to liveness=died within 60s"
        cat "$SPRAWL_ROOT/.sprawl/agents/${MANAGER}.json" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    # -----------------------------------------------------------------
    # Phase 5: report_status with dead parent routes wrapped to weave.
    # -----------------------------------------------------------------
    # Drive engineer2 (alive, child of dead manager) to report_status.
    # Real.ReportStatus walks parent(=MANAGER, dead) → weave (live);
    # WrapForDeadTarget wraps the summary; messages.SendStatusChange
    # delivers a status_change envelope into weave's maildir. We assert
    # by grepping weave's pending queue for the wrapper text + sentinel.
    echo ""
    echo "=== Phase 5: engineer2.report_status with dead manager → wraps to weave ==="
    local PROBE_STATUS="QUM-745-STATUS-$$-$(date +%s)"
    local DRIVE_STATUS="Call mcp__sprawl__send_message with to='${ENG2}', body='Call mcp__sprawl__report_status with state=\"working\", summary=\"${PROBE_STATUS}\". Then stop.', interrupt=false."
    e2e_send_user_prompt "$SESSION" "$DRIVE_STATUS"

    # Real.ReportStatus dead-parent wrap clause for a single dead hop.
    # status_change envelopes land in .sprawl/messages/<recipient>/new/, NOT
    # in the agentloop queue — they're ephemeral state pings (QUM-559).
    local WRAP_STATUS="This message was sent to ${MANAGER} but ${MANAGER} is dead."
    if _dod_wait_for_maildir_body "weave" "$PROBE_STATUS" 180 >/dev/null; then
        pass "report_status sentinel landed in weave maildir (dead-parent route-up fired)"
    else
        fail "report_status sentinel did not appear in weave maildir within 180s"
        echo "  weave maildir tail:" >&2
        find "$SPRAWL_ROOT/.sprawl/messages/weave" -type f 2>/dev/null \
            | tail -10 | while read -r f; do echo "--- $f ---" >&2; cat "$f" >&2 2>/dev/null; done
        capture_pane "$SESSION" | tail -40 >&2
    fi
    if _dod_wait_for_maildir_body "weave" "$WRAP_STATUS" 5 >/dev/null; then
        pass "report_status wrapper enumerated dead parent"
    else
        fail "report_status wrapper missing dead-parent enumeration"
    fi

    # -----------------------------------------------------------------
    # Phase 4: SIGKILL engineer2; sibling sends → multi-hop wrap to weave.
    # -----------------------------------------------------------------
    echo ""
    echo "=== Phase 4 prep: SIGKILL engineer2 (manager already dead) ==="
    # Re-resolve engineer2's PID — the prior report_status turn may have
    # restarted/relaunched the runtime; pick the current claude subprocess.
    ENG2_PID=""
    for i in 1 2 3 4 5 6 7 8 9 10; do
        ENG2_PID=$(_dod_pid_for "$ENG2" || true)
        [ -n "$ENG2_PID" ] && break
        sleep 2
    done
    if [ -z "$ENG2_PID" ]; then
        fail "could not re-resolve engineer2 claude PID for SIGKILL"
        e2e_print_results
        return 1
    fi
    kill -9 "$ENG2_PID" 2>/dev/null || true
    if _dod_wait_for_died "$ENG2" 60 >/dev/null; then
        pass "engineer2 transitioned to liveness=died"
    else
        fail "engineer2 did not transition to liveness=died within 60s"
        cat "$SPRAWL_ROOT/.sprawl/agents/${ENG2}.json" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    echo ""
    echo "=== Phase 4: sibling → dead engineer2 (multi-hop wrap to weave) ==="
    # Sibling.send_message(to=ENG2, ...) → walks ENG2(dead)→MANAGER(dead)→weave.
    local PROBE_MULTI="QUM-745-MULTI-$$-$(date +%s)"
    local DRIVE_MULTI="Call mcp__sprawl__send_message with to='${SIBLING}', body='Call mcp__sprawl__send_message with to=\"${ENG2}\", body=\"${PROBE_MULTI}\", interrupt=false. Then stop.', interrupt=false."
    e2e_send_user_prompt "$SESSION" "$DRIVE_MULTI"

    local WRAP_MULTI="This message was sent to ${ENG2} but ${ENG2}, ${MANAGER} are dead."
    if _dod_wait_for_inbox_body "weave" "$WRAP_MULTI" 180 >/dev/null; then
        pass "multi-hop wrapper landed in weave queue (both dead names)"
    else
        fail "multi-hop wrapper did NOT land in weave queue within 180s"
        echo "  weave queue tail:" >&2
        find "$SPRAWL_ROOT/.sprawl/agents/weave/queue" -name '*.json' 2>/dev/null \
            | tail -10 | while read -r f; do echo "--- $f ---" >&2; jq . "$f" >&2 2>/dev/null || cat "$f" >&2; done
        capture_pane "$SESSION" | tail -40 >&2
    fi
    if _dod_wait_for_inbox_body "weave" "$PROBE_MULTI" 5 >/dev/null; then
        pass "multi-hop wrapper preserved original body sentinel"
    else
        fail "multi-hop wrapper missing original body sentinel"
    fi

    e2e_print_results
}
