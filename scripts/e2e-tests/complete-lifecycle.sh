#!/usr/bin/env bash
# scripts/e2e-tests/complete-lifecycle.sh — QUM-786 lifecycle arc
# complete→delegate→revive→retire regression guard.
#
# Per QUM-786 (parent-only-terminal lifecycle arc), an agent that reports
# state:complete lands in StatusComplete (revivable). delegate against a
# complete agent auto-wakes (no wake_if_offline flag required). The agent
# revives with the SAME session_id, branch, and worktree. After retire,
# the agent is permanently terminal and delegate must surface
# TerminalAgentError.
#
# Phases:
#   1. spawn researcher with a small task → wait for Status=complete on disk
#      (precondition: agent reported state:complete and was torn down)
#   2. capture session_id / branch / worktree from state.json
#   3. delegate(agent, task) with NO wake_if_offline flag → succeeds; agent
#      flips Status away from complete
#   4. session_id / branch / worktree are preserved across the revival
#   5. WakePromptDelegate preamble ("abandoned") + the new task body
#      surface in the child's activity.ndjson — proves the prompt was
#      delivered as a wake-time injection
#   6. retire(agent) → Status=retired on disk
#   7. delegate against the retired agent surfaces the canonical
#      TerminalAgentError ("no longer running")

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# Wait for the child's state.json status field to equal $2 (timeout $3 sec).
complete_wait_status() {
    local state_file="$1" expected="$2" timeout="${3:-120}" elapsed=0 status=""
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

# Wait until the child's state.json status differs from $2 (timeout $3 sec).
complete_wait_status_not() {
    local state_file="$1" excluded="$2" timeout="${3:-60}" elapsed=0 status=""
    while [ "$elapsed" -lt "$timeout" ]; do
        status=$(jq -r '.status // empty' "$state_file" 2>/dev/null || true)
        if [ -n "$status" ] && [ "$status" != "$excluded" ]; then
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

# Wait until all supplied substrings appear (collectively) anywhere under
# the agent's on-disk footprint (activity.ndjson, queue/*, state.json).
complete_wait_agent_substrings() {
    local name="$1" timeout="$2"
    shift 2
    local agent_dir="$SPRAWL_ROOT/.sprawl/agents/$name"
    local elapsed=0
    while [ "$elapsed" -lt "$timeout" ]; do
        if [ -d "$agent_dir" ]; then
            local all_found=1 needle
            for needle in "$@"; do
                if ! grep -rqF "$needle" "$agent_dir" 2>/dev/null; then
                    all_found=0
                    break
                fi
            done
            if [ "$all_found" -eq 1 ]; then
                return 0
            fi
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

# Find a non-weave child state.json whose .branch matches $1. Echoes path.
complete_find_child_by_branch() {
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

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-complete-lifecycle-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum786"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SESSION="sprawl-complete-lifecycle-e2e-$(head -c4 /dev/urandom | xxd -p)"
    local STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"
    local SUFFIX
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"

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

    # ----- Phase 1: spawn researcher → report complete -----------------------
    echo ""
    echo "=== Phase 1: spawn researcher; reach Status=complete ==="
    local BRANCH="qum786-complete-${SUFFIX}"
    local PROMPT_BODY="You are a QUM-786 complete-lifecycle probe. First call mcp__sprawl__report_status with state=working and summary=\"phase1 starting\". Then immediately call mcp__sprawl__report_status with state=complete and summary=\"phase1 done\". Do not call any other tools and do not write any files."
    local SPAWN_PROMPT="Call mcp__sprawl__spawn with family='product', type='researcher', branch='$BRANCH', and prompt set to exactly: '$PROMPT_BODY'. Then reply 'SPAWN_${SUFFIX} ok' and nothing else."
    _stmux send-keys -t "$SESSION" "$SPAWN_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    local STATE NAME
    if ! STATE=$(complete_find_child_by_branch "$BRANCH"); then
        fail "P1: no child state appeared within 180s for branch $BRANCH"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    NAME=$(jq -r '.name' "$STATE")
    pass "P1: child spawned (name=$NAME)"

    if complete_wait_status "$STATE" "complete" 180; then
        pass "P1: disk Status=complete after researcher reported state=complete"
    else
        local current
        current=$(jq -r '.status // empty' "$STATE" 2>/dev/null || true)
        fail "P1: disk Status did not reach 'complete' within 180s (got '$current')"
        cat "$STATE" >&2 2>/dev/null || true
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # ----- Phase 2: capture identity (session_id / branch / worktree) -------
    echo ""
    echo "=== Phase 2: capture session_id / branch / worktree pre-revive ==="
    local SID_BEFORE BRANCH_BEFORE WORKTREE_BEFORE
    SID_BEFORE=$(jq -r '.session_id // empty' "$STATE" 2>/dev/null || true)
    BRANCH_BEFORE=$(jq -r '.branch // empty' "$STATE" 2>/dev/null || true)
    WORKTREE_BEFORE=$(jq -r '.worktree // empty' "$STATE" 2>/dev/null || true)
    if [ -z "$SID_BEFORE" ]; then
        fail "P2: no session_id captured before revive (cannot prove preservation)"
        cat "$STATE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    pass "P2: captured pre-revive identity (sid=${SID_BEFORE:0:8}…, branch=$BRANCH_BEFORE, worktree=$WORKTREE_BEFORE)"

    # ----- Phase 3: delegate WITHOUT wake_if_offline → must succeed ---------
    echo ""
    echo "=== Phase 3: delegate(complete-agent) with NO wake_if_offline ==="
    local TASK_TOKEN="QUM786_TASK2_${SUFFIX}"
    local DEL_PROMPT="Call mcp__sprawl__delegate with agent='$NAME' and task='do another thing — $TASK_TOKEN'. Do NOT pass wake_if_offline. Then quote the exact tool response back to me and reply 'DEL_${SUFFIX} done' on its own line."
    _stmux send-keys -t "$SESSION" "$DEL_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    if ! wait_for_pattern_fast "$SESSION" "DEL_${SUFFIX} done" 60; then
        fail "P3: delegate call did not complete within 60s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    # An auto-wake error path would say "is complete" or "is paused" — assert
    # the canonical offline-error string did NOT appear.
    if capture_pane "$SESSION" | grep -qE 'is (complete|paused|killed|died|faulted|resume_failed).*wake_if_offline'; then
        fail "P3: delegate against complete-agent surfaced an offline-error (should auto-wake instead)"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    pass "P3: delegate(no wake_if_offline) returned without an offline-error"

    if complete_wait_status_not "$STATE" "complete" 90; then
        local revived
        revived=$(jq -r '.status // empty' "$STATE" 2>/dev/null || true)
        pass "P3: disk Status flipped away from 'complete' to '$revived' (agent revived)"
    else
        fail "P3: disk Status did not change from 'complete' within 90s (auto-wake plumbing broken?)"
        cat "$STATE" >&2 2>/dev/null || true
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # ----- Phase 4: identity preserved (session_id / branch / worktree) ----
    echo ""
    echo "=== Phase 4: confirm session_id / branch / worktree unchanged ==="
    local SID_AFTER BRANCH_AFTER WORKTREE_AFTER
    SID_AFTER=$(jq -r '.session_id // empty' "$STATE" 2>/dev/null || true)
    BRANCH_AFTER=$(jq -r '.branch // empty' "$STATE" 2>/dev/null || true)
    WORKTREE_AFTER=$(jq -r '.worktree // empty' "$STATE" 2>/dev/null || true)
    if [ "$SID_BEFORE" = "$SID_AFTER" ]; then
        pass "P4: session_id preserved across revive (${SID_AFTER:0:8}…)"
    else
        fail "P4: session_id changed across revive (before=${SID_BEFORE:0:8}…, after=${SID_AFTER:0:8}…)"
        e2e_print_results
        return 1
    fi
    if [ "$BRANCH_BEFORE" = "$BRANCH_AFTER" ]; then
        pass "P4: branch preserved across revive ($BRANCH_AFTER)"
    else
        fail "P4: branch changed across revive (before=$BRANCH_BEFORE, after=$BRANCH_AFTER)"
        e2e_print_results
        return 1
    fi
    if [ "$WORKTREE_BEFORE" = "$WORKTREE_AFTER" ]; then
        pass "P4: worktree preserved across revive ($WORKTREE_AFTER)"
    else
        fail "P4: worktree changed across revive (before=$WORKTREE_BEFORE, after=$WORKTREE_AFTER)"
        e2e_print_results
        return 1
    fi

    # ----- Phase 5: new task body delivered as a prompt --------------------
    echo ""
    echo "=== Phase 5: new task body delivered as a prompt + agent acts on it ==="
    # The auto-wake-on-complete path threads the new task through both
    # RuntimeStartSpec.RestartInjection (claude's first post-wake user
    # prompt, prefixed with the WakePromptDelegate "abandoned" preamble)
    # AND state.EnqueueTask (durable maildir entry under
    # .sprawl/agents/<name>/tasks/*.json + .sprawl/agents/<name>/prompts/*.md).
    # The WakePromptDelegate byte-pin is unit-covered
    # (internal/supervisor/real_autowake_complete_test.go) and the e2e here
    # asserts the disk plumbing: task body lands in the agent's prompts/
    # AND the agent acted on it (report_status carrying the token).
    if complete_wait_agent_substrings "$NAME" 180 "$TASK_TOKEN"; then
        pass "P5: new task body delivered to agent (token=$TASK_TOKEN present in prompts/ or queue)"
    else
        fail "P5: missing '$TASK_TOKEN' in agent footprint within 180s (task body never delivered)"
        local agent_dir="$SPRAWL_ROOT/.sprawl/agents/$NAME"
        if [ -d "$agent_dir" ]; then
            find "$agent_dir" -type f -printf '%p %s\n' >&2
        fi
        e2e_print_results
        return 1
    fi
    # Soft signal: the activity log should accumulate further records
    # after the task is delivered (proves the wake didn't just
    # queue-and-stall). This is a loose co-occurrence check — both
    # tokens anywhere under agents/$NAME — not a same-record assertion;
    # a stricter check (single activity.ndjson record carrying the
    # token) is unit-covered by real_autowake_complete_test.go.
    if complete_wait_agent_substrings "$NAME" 120 "$TASK_TOKEN" "tool_use"; then
        pass "P5: activity log contains task token + tool_use records (agent processed the wake)"
    else
        echo "  NOTE: P5 soft signal not observed within 120s; primary AC (task body landed in prompts/) already PASSed" >&2
    fi

    # ----- Phase 6: retire(agent) → Status=retired --------------------------
    echo ""
    echo "=== Phase 6: retire(agent) → permanent termination ==="
    local RETIRE_PROMPT="Call mcp__sprawl__retire with agent='$NAME', abandon=true. Then quote the exact tool response back to me and reply 'RET_${SUFFIX} done' on its own line."
    _stmux send-keys -t "$SESSION" "$RETIRE_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    if ! wait_for_pattern_fast "$SESSION" "RET_${SUFFIX} done" 120; then
        fail "P6: retire call did not complete within 120s"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # Retire deletes the agent's state.json (internal/agent/retire.go:82).
    # Permanent termination is observable as the state file disappearing
    # (the agent name is now free to be reallocated).
    local rt_deadline=$((SECONDS + 60))
    while [ "$SECONDS" -lt "$rt_deadline" ]; do
        [ -e "$STATE" ] || break
        sleep 1
    done
    if [ ! -e "$STATE" ]; then
        pass "P6: state.json removed after retire (agent permanently terminated)"
    else
        local rt
        rt=$(jq -r '.status // empty' "$STATE" 2>/dev/null || true)
        fail "P6: state.json still present 60s after retire (status='$rt')"
        e2e_print_results
        return 1
    fi

    # ----- Phase 7: delegate against retired agent → not found / terminal --
    echo ""
    echo "=== Phase 7: delegate(retired-agent) → not found / terminal error ==="
    # Termination is observable either as TerminalAgentError ("no longer
    # running" — fires when state.json exists with terminal Status) or as
    # "agent X not found" (fires when retire has already deleted
    # state.json; see internal/agent/retire.go:82). Both are valid
    # termination signals; the contract is "delegate fails clearly," not
    # the exact string. The LLM-side echo-marker pattern would race the
    # prompt-text-still-in-input window, so we instead poll the pane for
    # either the canonical termination tokens (PASS) or a silent-success
    # banner (FAIL).
    local DEL2_PROMPT="Call mcp__sprawl__delegate with agent='$NAME' and task='should-fail-${SUFFIX}'. Do NOT pass wake_if_offline. Then summarize the outcome in one short sentence."
    _stmux send-keys -t "$SESSION" "$DEL2_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    local p7_deadline=$((SECONDS + 90))
    local p7_status=""
    while [ "$SECONDS" -lt "$p7_deadline" ]; do
        local p7_pane
        p7_pane=$(capture_pane "$SESSION")
        # State file must remain absent — a silent success would re-enqueue
        # a task and could only happen if a state file was somehow
        # re-created.
        if [ -e "$STATE" ]; then
            p7_status="state_reappeared"
            break
        fi
        if printf '%s' "$p7_pane" | grep -qiE 'no longer running|not found|cannot delegate|does not exist|not a known agent|no such agent'; then
            p7_status="error"
            break
        fi
        sleep 2
    done

    case "$p7_status" in
        error)
            pass "P7: delegate(retired-agent) surfaced a termination/not-found error"
            ;;
        state_reappeared)
            fail "P7: state.json reappeared after retire — delegate may have silently revived a deleted agent"
            ls -la "$(dirname "$STATE")" >&2 || true
            e2e_print_results
            return 1
            ;;
        *)
            fail "P7: delegate(retired-agent) did not surface a termination error within 90s"
            echo "  ---- P7 debug: pane tail ----" >&2
            capture_pane "$SESSION" | sed -E 's/\x1b\[[0-9;]*[mGKH]//g' | tail -80 >&2
            e2e_print_results
            return 1
            ;;
    esac

    e2e_print_results
}
