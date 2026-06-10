#!/usr/bin/env bash
# scripts/e2e-tests/sidechain-discovery-smoke.sh — QUM-757 sidechain-discovery
# smoke. Verifies Claude finds the worktree-local
# `.claude/agents/oracle.md` and `.claude/agents/test-critic.md` sidechain
# definitions (ported in QUM-713) at engineer-spawn time.
#
# Approach: stage the two `.claude/agents/*.md` files into the sandbox repo
# and commit them on main so engineer worktrees inherit them. Spawn an
# engineer; drive it to invoke each sidechain via the Task (Agent) tool with
# a trivial identity-eliciting prompt. Grep the engineer's activity.ndjson
# for persona phrases sourced from the file bodies — proof the local
# definitions reached Claude's discovery path. There is no user-level
# `~/.claude/agents/oracle.md` on this host (we check); if Claude can resolve
# `subagent_type=oracle` at all, it must be via the worktree-local file.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-sidechain-disc-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum757"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi

    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    # --- Stage sidechain definitions into the sandbox repo --------------
    local SRC_AGENTS="$REPO_ROOT/.claude/agents"
    if [ ! -f "$SRC_AGENTS/oracle.md" ] || [ ! -f "$SRC_AGENTS/test-critic.md" ]; then
        fail "expected $SRC_AGENTS/{oracle,test-critic}.md to exist in REPO_ROOT"
        e2e_print_results
        return 1
    fi
    mkdir -p "$SPRAWL_ROOT/.claude/agents"
    cp -p "$SRC_AGENTS/oracle.md" "$SPRAWL_ROOT/.claude/agents/oracle.md"
    cp -p "$SRC_AGENTS/test-critic.md" "$SPRAWL_ROOT/.claude/agents/test-critic.md"
    git -C "$SPRAWL_ROOT" add .claude/agents/oracle.md .claude/agents/test-critic.md
    git -C "$SPRAWL_ROOT" commit -m "qum-757: seed sidechain agents" --quiet
    pass "staged .claude/agents/{oracle,test-critic}.md into sandbox main"

    local SESSION="sprawl-sidechain-disc-e2e-$(head -c4 /dev/urandom | xxd -p)"
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

    e2e_attach_phantom_client "$SESSION"

    # --- Spawn the probe engineer --------------------------------------
    echo ""
    echo "=== Spawning probe engineer ==="
    local ENG_BRANCH="qum-757-probe-${BRANCH_SUFFIX}"
    # The engineer is told to invoke each sidechain via the Task tool with
    # an identity-eliciting prompt. The sidechain's response (which will
    # reflect the persona defined in the worktree-local .claude/agents/*.md
    # body) lands in the engineer's activity.ndjson as a tool_result frame.
    local ENG_SPAWN="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='${ENG_BRANCH}', and prompt set to exactly: 'You are the QUM-757 sidechain probe engineer. Perform these steps in order. (1) Use the Task tool with subagent_type=\"oracle\" and prompt=\"Identify yourself in one sentence beginning with the words I am. Then in two sentences plan how to add a hello.txt file.\". (2) Use the Task tool with subagent_type=\"test-critic\" and prompt=\"Identify yourself in one sentence beginning with the words I am. Then briefly critique this test: assertEquals(1, 1).\". (3) Call mcp__sprawl__report_status with state=complete, summary=\"sidechain probes done\". Do not do anything else.'"
    e2e_send_user_prompt "$SESSION" "$ENG_SPAWN"

    # Locate the engineer state file by branch (unique).
    local ENG_STATE="" ENG_NAME=""
    local ELAPSED=0
    while [ "$ELAPSED" -lt 240 ]; do
        local f n br sub
        while IFS= read -r f; do
            [ -z "$f" ] && continue
            n=$(jq -r '.name // empty' "$f" 2>/dev/null || true)
            [ -z "$n" ] && continue
            br=$(jq -r '.branch // empty' "$f" 2>/dev/null || true)
            sub=$(jq -r '.subagent // false' "$f" 2>/dev/null || true)
            if [ "$br" = "$ENG_BRANCH" ] && [ "$sub" != "true" ]; then
                ENG_STATE="$f"
                ENG_NAME="$n"
                break
            fi
        done < <(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null)
        [ -n "$ENG_NAME" ] && break
        sleep 2
        ELAPSED=$((ELAPSED + 2))
    done
    if [ -z "$ENG_NAME" ]; then
        fail "probe engineer (branch=$ENG_BRANCH) did not appear within 240s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    pass "probe engineer spawned (name=$ENG_NAME, branch=$ENG_BRANCH)"

    local ENG_WT
    ENG_WT=$(jq -r '.worktree // empty' "$ENG_STATE" 2>/dev/null || true)
    # Sanity check: the worktree-local sidechain files must be reachable
    # from the engineer's working directory (proves the commit + worktree
    # plumbing did the right thing). If this assertion fails, the rest of
    # the test is meaningless.
    if [ -n "$ENG_WT" ] && [ -f "$ENG_WT/.claude/agents/oracle.md" ] \
        && [ -f "$ENG_WT/.claude/agents/test-critic.md" ]; then
        pass "engineer worktree contains .claude/agents/{oracle,test-critic}.md"
    else
        fail "engineer worktree missing sidechain definitions ($ENG_WT/.claude/agents/)"
    fi

    local ENG_ACT="$SPRAWL_ROOT/.sprawl/agents/$ENG_NAME/activity.ndjson"
    local ENG_WIRELOG_DIR="$SPRAWL_ROOT/.sprawl/logs/sessions/$ENG_NAME"

    # --- Assert each sidechain executed via the worktree-local def -----
    # activity.ndjson is a 200-byte summary stream — it captures the Agent
    # tool_use call (input args) but truncates the tool_result. The full
    # tool_result lives in the raw stream-json wire-log under
    # .sprawl/logs/sessions/<name>/<session>.ndjson. We grep both:
    #   * activity for the tool_use (proves engineer attempted the sidechain).
    #   * wirelog for the persona phrase in tool_result text content
    #     (proves discovery resolved subagent_type to our local .md).
    echo ""
    echo "=== Waiting for sidechain results ==="
    local END=$((SECONDS + 360))
    local ORACLE_CALLED=0 CRITIC_CALLED=0
    local ORACLE_PERSONA=0 CRITIC_PERSONA=0
    # Match the JSON-escaped form as it appears in activity summary strings:
    # `Agent {\"description\":...,\"subagent_type\":\"oracle\",...}` becomes
    # `subagent_type\":\"oracle` on disk (one backslash, one quote).
    local ORACLE_PAT='subagent_type\":\"oracle\"'
    local CRITIC_PAT='subagent_type\":\"test-critic\"'
    while [ "$SECONDS" -lt "$END" ]; do
        if [ -f "$ENG_ACT" ]; then
            if [ "$ORACLE_CALLED" -eq 0 ] && grep -qF "$ORACLE_PAT" "$ENG_ACT"; then
                ORACLE_CALLED=1
            fi
            if [ "$CRITIC_CALLED" -eq 0 ] && grep -qF "$CRITIC_PAT" "$ENG_ACT"; then
                CRITIC_CALLED=1
            fi
        fi
        # Aggregate wire-log files for this engineer session (there may be
        # only one *.ndjson, but glob safely in case of session cycling).
        if [ -d "$ENG_WIRELOG_DIR" ]; then
            local WIRE_GLOB=("$ENG_WIRELOG_DIR"/*.ndjson)
            if [ -f "${WIRE_GLOB[0]:-}" ]; then
                # Phrases sourced from the file bodies. The sidechain's
                # `Identify yourself…` reply is highly likely to contain
                # both "Oracle" and "planning" (oracle.md L6) or "Test
                # Critic" (test-critic.md L6).
                if [ "$ORACLE_PERSONA" -eq 0 ] \
                    && grep -qi "oracle" "${WIRE_GLOB[@]}" \
                    && grep -qi "planning" "${WIRE_GLOB[@]}"; then
                    ORACLE_PERSONA=1
                fi
                if [ "$CRITIC_PERSONA" -eq 0 ] \
                    && grep -qi "test critic" "${WIRE_GLOB[@]}"; then
                    CRITIC_PERSONA=1
                fi
            fi
        fi
        if [ "$ORACLE_CALLED" -eq 1 ] && [ "$CRITIC_CALLED" -eq 1 ] \
            && [ "$ORACLE_PERSONA" -eq 1 ] && [ "$CRITIC_PERSONA" -eq 1 ]; then
            break
        fi
        sleep 3
    done

    if [ "$ORACLE_CALLED" -eq 1 ]; then
        pass "engineer invoked Task tool with subagent_type=oracle"
    else
        fail "engineer never invoked Task(subagent_type=oracle) within 360s"
        [ -f "$ENG_ACT" ] && tail -40 "$ENG_ACT" >&2 || echo "  (activity missing)" >&2
    fi
    if [ "$ORACLE_PERSONA" -eq 1 ]; then
        pass "oracle response cites worktree-local persona (\"Oracle\" + \"planning\")"
    else
        fail "oracle response did NOT cite expected persona phrases — discovery may have failed"
        if [ -d "$ENG_WIRELOG_DIR" ]; then
            echo "  --- wirelog tail ---" >&2
            for f in "$ENG_WIRELOG_DIR"/*.ndjson; do
                [ -f "$f" ] && tail -10 "$f" >&2
            done
        fi
    fi
    if [ "$CRITIC_CALLED" -eq 1 ]; then
        pass "engineer invoked Task tool with subagent_type=test-critic"
    else
        fail "engineer never invoked Task(subagent_type=test-critic) within 360s"
    fi
    if [ "$CRITIC_PERSONA" -eq 1 ]; then
        pass "test-critic response cites worktree-local persona (\"Test Critic\")"
    else
        fail "test-critic response did NOT cite expected persona phrase — discovery may have failed"
    fi

    # Negative assertion: if either sidechain failed to resolve, Claude
    # surfaces an "Unknown agent" / "agent type" error in the tool_result.
    if [ -d "$ENG_WIRELOG_DIR" ]; then
        local hit=0
        for f in "$ENG_WIRELOG_DIR"/*.ndjson; do
            [ -f "$f" ] || continue
            if grep -qiE "unknown agent|agent type .* not found|no such agent|Agent type .* not found" "$f"; then
                hit=1
                grep -iE "unknown agent|agent type .* not found|no such agent|Agent type .* not found" "$f" | head -3 >&2
            fi
        done
        if [ "$hit" -eq 1 ]; then
            fail "engineer wire-log contains an unresolved-sidechain error"
        else
            pass "no unresolved-sidechain error in engineer wire-log"
        fi
    else
        echo "  (wirelog dir missing — skipping negative assertion)"
    fi

    e2e_print_results
}
