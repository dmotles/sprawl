#!/usr/bin/env bash
# scripts/e2e-tests/subagent-model.sh — QUM-756 sub-agent model e2e
# regression guard.
#
# Phases:
#   1. Spawn a tower manager, then spawn a sub-engineer under tower.
#      Verify no new worktree, shared worktree+branch, peek surfaces
#      subagent badge.
#   2. Sub commits to the shared worktree; commit appears in tower's
#      branch.
#   3. Retire the sub (cascade=false): sub.status=retired, no worktree
#      removed, commit still in branch, branch still exists.
#   4. Live MCP error paths: root cannot host sub-agents; branch must
#      not be set when subagent=true.
#   5. Cascade-retire the tower (with a fresh sub still under it): both
#      retired, worktree count decreases by 1.
#   6. Code-reviewer dogfood: engineer manager stages buggy diff, spawns
#      reviewer sub, reviewer runs `git diff --cached`, sends findings.
#   7. Observational depth-2 canary (WARN-only): reviewer must not
#      auto-spawn sub-agents.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# --- helpers ---------------------------------------------------------

# Find a non-weave agent state.json whose .parent equals the given
# parent name. Emits the file path on stdout, or empty.
_find_subagent_state() {
    local parent="$1"
    local f name p sub
    while IFS= read -r f; do
        [ -z "$f" ] && continue
        name=$(jq -r '.name // empty' "$f" 2>/dev/null || true)
        [ -z "$name" ] || [ "$name" = "weave" ] && continue
        p=$(jq -r '.parent // empty' "$f" 2>/dev/null || true)
        sub=$(jq -r '.subagent // false' "$f" 2>/dev/null || true)
        if [ "$p" = "$parent" ] && [ "$sub" = "true" ]; then
            printf '%s\n' "$f"
            return 0
        fi
    done < <(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null)
}

# Find a non-weave/non-subagent state.json (i.e. a top-level manager).
_find_manager_state() {
    local skip="${1:-}"
    local f name sub
    while IFS= read -r f; do
        [ -z "$f" ] && continue
        name=$(jq -r '.name // empty' "$f" 2>/dev/null || true)
        [ -z "$name" ] || [ "$name" = "weave" ] && continue
        if [ -n "$skip" ] && [ "$name" = "$skip" ]; then
            continue
        fi
        sub=$(jq -r '.subagent // false' "$f" 2>/dev/null || true)
        if [ "$sub" != "true" ]; then
            printf '%s\n' "$f"
            return 0
        fi
    done < <(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null)
}

_worktree_count() {
    git -C "$SPRAWL_ROOT" worktree list 2>/dev/null | wc -l | tr -d ' '
}

_wait_for_status() {
    # _wait_for_status <state_file> <expected_status> <timeout_s>
    local state="$1" want="$2" timeout="$3"
    local end=$((SECONDS + timeout))
    local got
    while [ "$SECONDS" -lt "$end" ]; do
        got=$(jq -r '.status // empty' "$state" 2>/dev/null || true)
        if [ "$got" = "$want" ]; then
            return 0
        fi
        sleep 1
    done
    return 1
}

# _wait_for_retired waits for retire to complete. Successful RetireAgent
# DELETES the agent state file (agent/retire.go), so file-absence is the
# right success signal. The transient "retiring" status may also appear
# during teardown — treat that as in-progress.
_wait_for_retired() {
    local state="$1" timeout="$2"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        if [ ! -f "$state" ]; then
            return 0
        fi
        local got
        got=$(jq -r '.status // empty' "$state" 2>/dev/null || true)
        if [ "$got" = "retired" ]; then
            return 0
        fi
        sleep 1
    done
    return 1
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-subagent-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum756"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi

    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SESSION="sprawl-subagent-e2e-$(head -c4 /dev/urandom | xxd -p)"
    local STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"
    local PROBE="QUM756-$$-$(date +%s)"
    local BRANCH_SUFFIX
    BRANCH_SUFFIX="$(head -c4 /dev/urandom | xxd -p)"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo "  PROBE=$PROBE"

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
    echo "=== Attaching phantom tmux client ==="
    e2e_attach_phantom_client "$SESSION"

    # --- Phase 1: Spawn tower manager + sub-engineer ------------------
    echo ""
    echo "=== Phase 1: spawn tower manager + sub-engineer ==="
    local TOWER_BRANCH="qum-756-tower-${BRANCH_SUFFIX}"
    local TOWER_SPAWN="Call mcp__sprawl__spawn with family='engineering', type='manager', branch='${TOWER_BRANCH}', and prompt set to exactly: 'You are the QUM-756 tower manager. Call mcp__sprawl__report_status with state=working, summary=\"ready to host subagents\". Then stop and wait. Do nothing else until you receive a message.'"
    e2e_send_user_prompt "$SESSION" "$TOWER_SPAWN"

    local TOWER_STATE="" TOWER_NAME=""
    local ELAPSED=0
    while [ "$ELAPSED" -lt 180 ]; do
        TOWER_STATE=$(_find_manager_state || true)
        if [ -n "$TOWER_STATE" ]; then
            TOWER_NAME=$(jq -r '.name // empty' "$TOWER_STATE" 2>/dev/null || true)
            [ -n "$TOWER_NAME" ] && break
        fi
        sleep 2
        ELAPSED=$((ELAPSED + 2))
    done
    if [ -z "$TOWER_NAME" ]; then
        fail "tower manager did not appear within 180s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    pass "tower manager spawned (name=$TOWER_NAME)"

    local TOWER_WT TOWER_BR
    TOWER_WT=$(jq -r '.worktree // empty' "$TOWER_STATE" 2>/dev/null || true)
    TOWER_BR=$(jq -r '.branch // empty' "$TOWER_STATE" 2>/dev/null || true)

    local BEFORE_WT_COUNT
    BEFORE_WT_COUNT=$(_worktree_count)
    echo "  BEFORE_WT_COUNT=$BEFORE_WT_COUNT (tower.worktree=$TOWER_WT, tower.branch=$TOWER_BR)"

    # Have tower spawn TWO sub-engineers up front while it is fresh: one
    # for the per-phase work (commit + retire non-cascade), one reserved
    # for the cascade-retire test in Phase 5. Spawning both back-to-back
    # while tower is responsive avoids a fragile second tower-spawn under
    # later rate-limit conditions.
    local SUB_SPAWN_PROMPT="Call mcp__sprawl__send_message with to='${TOWER_NAME}', body='IMPORTANT: as your next two actions, call mcp__sprawl__spawn TWICE to spawn two sub-engineers. (1) First call: subagent=true, type=\"engineer\", family=\"engineering\", prompt=\"You are QUM-756 sub-engineer ALPHA. Call mcp__sprawl__report_status with state=working, summary=ready. Then stop and wait.\". (2) Second call: subagent=true, type=\"engineer\", family=\"engineering\", prompt=\"You are QUM-756 sub-engineer BRAVO. Call mcp__sprawl__report_status with state=working, summary=ready. Then stop and wait.\". Do NOT set branch on either call. After both calls return, call mcp__sprawl__report_status with state=working, summary=\"two subs spawned\".', interrupt=false."
    e2e_send_user_prompt "$SESSION" "$SUB_SPAWN_PROMPT"

    # Poll for two subs under tower.
    local SUB_STATE="" SUB_NAME="" SUB2_STATE="" SUB2_NAME=""
    ELAPSED=0
    while [ "$ELAPSED" -lt 300 ]; do
        local _found=()
        local f n p sub
        while IFS= read -r f; do
            [ -z "$f" ] && continue
            n=$(jq -r '.name // empty' "$f" 2>/dev/null || true)
            [ -z "$n" ] && continue
            if [ "$n" = "weave" ] || [ "$n" = "$TOWER_NAME" ]; then
                continue
            fi
            p=$(jq -r '.parent // empty' "$f" 2>/dev/null || true)
            sub=$(jq -r '.subagent // false' "$f" 2>/dev/null || true)
            if [ "$p" = "$TOWER_NAME" ] && [ "$sub" = "true" ]; then
                _found+=("$f|$n")
            fi
        done < <(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null)
        if [ "${#_found[@]}" -ge 2 ]; then
            IFS='|' read -r SUB_STATE SUB_NAME <<< "${_found[0]}"
            IFS='|' read -r SUB2_STATE SUB2_NAME <<< "${_found[1]}"
            break
        fi
        sleep 2
        ELAPSED=$((ELAPSED + 2))
    done
    if [ -z "$SUB_NAME" ] || [ -z "$SUB2_NAME" ]; then
        fail "two sub-agents did not appear under tower within 300s (got SUB_NAME='$SUB_NAME' SUB2_NAME='$SUB2_NAME')"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    pass "sub-agent ALPHA spawned (name=$SUB_NAME, parent=$TOWER_NAME)"
    pass "sub-agent BRAVO spawned (name=$SUB2_NAME, parent=$TOWER_NAME, reserved for Phase 5 cascade)"

    local AFTER_WT_COUNT
    AFTER_WT_COUNT=$(_worktree_count)
    if [ "$AFTER_WT_COUNT" = "$BEFORE_WT_COUNT" ]; then
        pass "worktree count unchanged after sub spawn ($AFTER_WT_COUNT)"
    else
        fail "worktree count changed: before=$BEFORE_WT_COUNT after=$AFTER_WT_COUNT (sub should reuse parent's worktree)"
    fi

    local SUB_WT SUB_BR
    SUB_WT=$(jq -r '.worktree // empty' "$SUB_STATE" 2>/dev/null || true)
    SUB_BR=$(jq -r '.branch // empty' "$SUB_STATE" 2>/dev/null || true)
    if [ -n "$SUB_WT" ] && [ "$SUB_WT" = "$TOWER_WT" ]; then
        pass "sub.worktree == tower.worktree ($SUB_WT)"
    else
        fail "sub.worktree ($SUB_WT) != tower.worktree ($TOWER_WT)"
    fi
    if [ -n "$SUB_BR" ] && [ "$SUB_BR" = "$TOWER_BR" ]; then
        pass "sub.branch == tower.branch ($SUB_BR)"
    else
        fail "sub.branch ($SUB_BR) != tower.branch ($TOWER_BR)"
    fi

    # Drive weave to peek at the sub and assert the badge surfaces.
    local PEEK_PROMPT="Call mcp__sprawl__peek with agent_name='${SUB_NAME}'. Quote the response verbatim."
    e2e_send_user_prompt "$SESSION" "$PEEK_PROMPT"
    if wait_for_pattern_fast "$SESSION" "subagent" 60; then
        pass "peek surfaces 'subagent' token"
    else
        fail "peek did not surface 'subagent' token within 60s"
        capture_pane "$SESSION" | tail -60 >&2
    fi
    if wait_for_pattern_fast "$SESSION" "shared_worktree_with|shared worktree" 30; then
        pass "peek surfaces shared-worktree marker"
    else
        # Don't hard-fail: the exact wording may evolve. Warn instead.
        echo "  WARN: peek did not surface 'shared_worktree_with' marker (wording may have drifted)"
    fi

    # --- Phase 2: Sub commits to shared worktree ---------------------
    echo ""
    echo "=== Phase 2: sub commits to shared worktree ==="
    local SENTINEL_FILE="QUM756-${PROBE}.txt"
    local COMMIT_MSG="PHASE2-${PROBE}"
    local COMMIT_PROMPT="Call mcp__sprawl__send_message with to='${SUB_NAME}', body='In your worktree, create a file named ${SENTINEL_FILE} containing the text \"qum-756 phase 2\". Then run: git add ${SENTINEL_FILE} && git commit -m \"${COMMIT_MSG}\". Then call mcp__sprawl__report_status with state=working, summary=\"committed\".', interrupt=false."
    e2e_send_user_prompt "$SESSION" "$COMMIT_PROMPT"

    local COMMIT_SEEN=0
    local END=$((SECONDS + 180))
    while [ "$SECONDS" -lt "$END" ]; do
        if [ -n "$TOWER_WT" ] && git -C "$TOWER_WT" log "$TOWER_BR" --oneline 2>/dev/null | grep -qF "$COMMIT_MSG"; then
            COMMIT_SEEN=1
            break
        fi
        sleep 2
    done
    if [ "$COMMIT_SEEN" -eq 1 ]; then
        pass "sub commit '$COMMIT_MSG' visible in tower's branch ($TOWER_BR)"
    else
        fail "sub commit '$COMMIT_MSG' did NOT appear in $TOWER_BR within 180s"
        git -C "$TOWER_WT" log "$TOWER_BR" --oneline 2>/dev/null | head -10 >&2 || true
    fi

    # --- Phase 3: Retire sub (cascade=false) -------------------------
    echo ""
    echo "=== Phase 3: retire sub (cascade=false) ==="
    # Direct, terse prompt — weave is busy under rate-limit noise (faulted
    # sub-engineers spam observation notes), so make the ask unambiguous.
    local RETIRE_PROMPT="IMPORTANT: as your very next action, call mcp__sprawl__retire with agent_name='${SUB_NAME}' and cascade=false. Do not call any other tool first."
    e2e_send_user_prompt "$SESSION" "$RETIRE_PROMPT"

    # Confirm weave actually invoked retire (the TUI renders "sprawl/retire
    # <name>" inline) before checking on-disk state. 240s is generous —
    # weave can be backlogged behind tower's rate-limit telemetry.
    if wait_for_substring_fast "$SESSION" "sprawl/retire" 240; then
        pass "weave invoked sprawl/retire tool"
    else
        fail "weave never invoked sprawl/retire within 240s"
        capture_pane "$SESSION" | tail -40 >&2
    fi

    # RetireAgent deletes the state.json on success — file absence is the
    # right success signal, not a status="retired" string match.
    if _wait_for_retired "$SUB_STATE" 180; then
        pass "sub retired (state file removed or status=retired)"
    else
        fail "sub did not finish retire within 180s"
        ls -la "$(dirname "$SUB_STATE")" 2>/dev/null | head -10 >&2 || true
    fi

    local POST_RETIRE_WT
    POST_RETIRE_WT=$(_worktree_count)
    if [ "$POST_RETIRE_WT" = "$BEFORE_WT_COUNT" ]; then
        pass "worktree count still $POST_RETIRE_WT after sub retire (no removal)"
    else
        fail "worktree count changed after sub retire: before=$BEFORE_WT_COUNT after=$POST_RETIRE_WT"
    fi

    if git -C "$TOWER_WT" log "$TOWER_BR" --oneline 2>/dev/null | grep -qF "$COMMIT_MSG"; then
        pass "sub's commit still present in $TOWER_BR after sub retire"
    else
        fail "sub's commit vanished from $TOWER_BR after retire"
    fi

    if git -C "$SPRAWL_ROOT" branch --list "$TOWER_BR" 2>/dev/null | grep -qF "$TOWER_BR"; then
        pass "branch $TOWER_BR still exists after sub retire"
    else
        fail "branch $TOWER_BR was removed by sub retire"
    fi

    # --- Phase 4: Error paths (live MCP) -----------------------------
    echo ""
    echo "=== Phase 4: error-path validation ==="
    # 4a: root (weave) cannot host sub-agents. OBSERVATIONAL (WARN-only):
    # this contract is verbatim-asserted in
    # internal/supervisor/real_subagent_test.go::TestRealSpawn_Subagent_RootCannotHost.
    # The live attempt is flaky because under rate-limit conditions weave's
    # turn queue backs up and may not invoke the deliberate-failure tool
    # call within the test budget. Keep the probe (best-effort signal that
    # the MCP surface is wired) but don't fail the suite on it.
    local ERR_ROOT_PROMPT="IMPORTANT: as your next action, call mcp__sprawl__spawn with these exact parameters: subagent=true, type='engineer', family='engineering', prompt='should fail'. Do NOT set branch. The tool will return an error — that is expected and is what we want to observe. Do not call any other tool first."
    e2e_send_user_prompt "$SESSION" "$ERR_ROOT_PROMPT"
    if wait_for_substring_fast "$SESSION" "root cannot host sub-agents" 180; then
        pass "weave spawn(subagent=true) rejected with 'root cannot host sub-agents' (live probe)"
    else
        echo "  WARN: 'root cannot host sub-agents' not surfaced live in 180s; contract is regression-locked in TestRealSpawn_Subagent_RootCannotHost"
    fi

    # 4b: branch must not be set when subagent=true. OBSERVATIONAL
    # (WARN-only): contract is verbatim-asserted in the Go test
    # TestRealSpawn_Subagent_BranchRejected. The live probe asks tower
    # to perform a deliberate-failure spawn and quote the error back
    # via send_message; under rate-limit + parallel-test conditions
    # this two-hop dance is unreliable. The Go test gives byte-exact
    # coverage of the same gate. Keep the probe as a best-effort
    # signal that the MCP-tool surface is wired end-to-end.
    local ERR_BRANCH_PROMPT="Call mcp__sprawl__send_message with to='${TOWER_NAME}', body='Call mcp__sprawl__spawn with subagent=true, type=\"engineer\", family=\"engineering\", branch=\"qum-756-bad-${BRANCH_SUFFIX}\", prompt=\"should fail\". The tool will return an error — quote the verbatim error message back to me via send_message to weave.', interrupt=false."
    e2e_send_user_prompt "$SESSION" "$ERR_BRANCH_PROMPT"
    if wait_for_substring_fast "$SESSION" "branch must not be set when subagent" 240; then
        pass "spawn(subagent=true, branch=...) rejected with 'branch must not be set when subagent ...' error (live probe)"
    else
        echo "  WARN: 'branch must not be set when subagent' not surfaced live in 240s; contract is regression-locked in TestRealSpawn_Subagent_BranchRejected"
    fi

    # --- Phase 5: Cascade-retire tower -------------------------------
    echo ""
    echo "=== Phase 5: cascade-retire tower with reserved BRAVO sub ==="
    # SUB2 (BRAVO) was spawned in Phase 1 and reserved for this phase.
    # No fragile second tower-spawn under rate-limit conditions.
    pass "cascade target ready (BRAVO sub=$SUB2_NAME still alive under tower)"

    local CASCADE_PROMPT="IMPORTANT: as your very next action, call mcp__sprawl__retire with agent_name='${TOWER_NAME}' and cascade=true. Do not call any other tool first."
    e2e_send_user_prompt "$SESSION" "$CASCADE_PROMPT"

    if _wait_for_retired "$SUB2_STATE" 300; then
        pass "BRAVO sub retired (cascade)"
    else
        fail "BRAVO sub did not finish retire within 300s (cascade)"
    fi
    if _wait_for_retired "$TOWER_STATE" 300; then
        pass "tower retired (cascade)"
    else
        fail "tower did not finish retire within 300s (cascade)"
    fi

    local AFTER_CASCADE_WT
    AFTER_CASCADE_WT=$(_worktree_count)
    local EXPECTED=$((BEFORE_WT_COUNT - 1))
    if [ "$AFTER_CASCADE_WT" -le "$EXPECTED" ]; then
        pass "worktree count dropped to $AFTER_CASCADE_WT (<=$EXPECTED) after cascade retire"
    else
        fail "worktree count $AFTER_CASCADE_WT > expected $EXPECTED after cascade retire"
    fi

    # --- Phase 6: Code-reviewer dogfood ------------------------------
    echo ""
    echo "=== Phase 6: code-reviewer dogfood ==="
    local FORGE_BRANCH="qum-756-forge-${BRANCH_SUFFIX}"
    local FORGE_SPAWN="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='${FORGE_BRANCH}', and prompt set to exactly: 'You are the QUM-756 forge engineer. In your worktree, create three files: a.txt containing \"alpha\", b.txt containing \"beta with an unused var bug\", and c.txt containing \"gamma off-by-one\". Then run: git add a.txt b.txt c.txt (do NOT commit). Then call mcp__sprawl__report_status with state=working, summary=\"diff staged\". Then stop and wait for instructions.'"
    e2e_send_user_prompt "$SESSION" "$FORGE_SPAWN"

    # Match by branch (FORGE_BRANCH is unique) rather than by name
    # exclusion. Phase 5 cascade-retired tower/ALPHA/BRAVO and freed
    # their names back into the auto-allocator pool, so the new forge
    # engineer may legitimately reuse one of those names.
    local FORGE_STATE="" FORGE_NAME=""
    ELAPSED=0
    while [ "$ELAPSED" -lt 300 ]; do
        local f n br sub
        while IFS= read -r f; do
            [ -z "$f" ] && continue
            n=$(jq -r '.name // empty' "$f" 2>/dev/null || true)
            [ -z "$n" ] && continue
            br=$(jq -r '.branch // empty' "$f" 2>/dev/null || true)
            sub=$(jq -r '.subagent // false' "$f" 2>/dev/null || true)
            if [ "$br" = "$FORGE_BRANCH" ] && [ "$sub" != "true" ]; then
                FORGE_STATE="$f"
                FORGE_NAME="$n"
                break
            fi
        done < <(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null)
        [ -n "$FORGE_NAME" ] && break
        sleep 2
        ELAPSED=$((ELAPSED + 2))
    done

    if [ -z "$FORGE_NAME" ]; then
        fail "forge engineer (branch=$FORGE_BRANCH) did not appear within 300s"
        capture_pane "$SESSION" | tail -60 >&2
    else
        pass "forge engineer spawned (name=$FORGE_NAME)"
        local FORGE_WT
        FORGE_WT=$(jq -r '.worktree // empty' "$FORGE_STATE" 2>/dev/null || true)

        # Wait for the staged diff to materialize before asking for review.
        local STAGE_END=$((SECONDS + 120))
        local STAGED=0
        while [ "$SECONDS" -lt "$STAGE_END" ]; do
            if [ -n "$FORGE_WT" ] && git -C "$FORGE_WT" diff --cached --name-only 2>/dev/null | grep -q .; then
                STAGED=1
                break
            fi
            sleep 2
        done
        if [ "$STAGED" -eq 1 ]; then
            pass "forge engineer staged a diff in its worktree"
        else
            fail "forge engineer did not stage a diff within 120s"
        fi

        local REVIEW_PROMPT="Call mcp__sprawl__send_message with to='${FORGE_NAME}', body='Spawn a code-reviewer subagent: Call mcp__sprawl__spawn with subagent=true, type=\"engineer\", family=\"engineering\", and prompt=\"You are a code reviewer. Run the bash command: git diff --cached. Then call mcp__sprawl__send_message with to=\\\"${FORGE_NAME}\\\", body containing your findings (mention any bugs you spot in the staged files). Then call mcp__sprawl__report_status with state=complete, summary=\\\"review done\\\".\". Do not set branch.', interrupt=false."
        e2e_send_user_prompt "$SESSION" "$REVIEW_PROMPT"

        local REVIEWER_STATE="" REVIEWER_NAME=""
        ELAPSED=0
        while [ "$ELAPSED" -lt 240 ]; do
            local f n p sub
            while IFS= read -r f; do
                [ -z "$f" ] && continue
                n=$(jq -r '.name // empty' "$f" 2>/dev/null || true)
                p=$(jq -r '.parent // empty' "$f" 2>/dev/null || true)
                sub=$(jq -r '.subagent // false' "$f" 2>/dev/null || true)
                if [ "$p" = "$FORGE_NAME" ] && [ "$sub" = "true" ]; then
                    REVIEWER_STATE="$f"
                    REVIEWER_NAME="$n"
                    break
                fi
            done < <(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null)
            [ -n "$REVIEWER_NAME" ] && break
            sleep 2
            ELAPSED=$((ELAPSED + 2))
        done

        if [ -z "$REVIEWER_NAME" ]; then
            fail "code-reviewer subagent did not appear within 240s"
        else
            pass "code-reviewer subagent spawned (name=$REVIEWER_NAME)"
            local REVIEWER_WT
            REVIEWER_WT=$(jq -r '.worktree // empty' "$REVIEWER_STATE" 2>/dev/null || true)
            if [ "$REVIEWER_WT" = "$FORGE_WT" ]; then
                pass "reviewer.worktree == forge.worktree ($REVIEWER_WT)"
            else
                fail "reviewer.worktree ($REVIEWER_WT) != forge.worktree ($FORGE_WT)"
            fi

            local REVIEWER_ACT="$SPRAWL_ROOT/.sprawl/agents/$REVIEWER_NAME/activity.ndjson"
            local ACT_END=$((SECONDS + 240))
            local DIFF_SEEN=0 MSG_SEEN=0
            while [ "$SECONDS" -lt "$ACT_END" ]; do
                if [ -f "$REVIEWER_ACT" ]; then
                    if [ "$DIFF_SEEN" -eq 0 ] && grep -qF "git diff --cached" "$REVIEWER_ACT"; then
                        DIFF_SEEN=1
                    fi
                    if [ "$MSG_SEEN" -eq 0 ] && grep -q "mcp__sprawl__send_message" "$REVIEWER_ACT" \
                        && grep -qF "$FORGE_NAME" "$REVIEWER_ACT"; then
                        MSG_SEEN=1
                    fi
                fi
                if [ "$DIFF_SEEN" -eq 1 ] && [ "$MSG_SEEN" -eq 1 ]; then
                    break
                fi
                sleep 2
            done
            if [ "$DIFF_SEEN" -eq 1 ]; then
                pass "reviewer activity contains 'git diff --cached' tool_use"
            else
                fail "reviewer did NOT run 'git diff --cached' within 240s"
                [ -f "$REVIEWER_ACT" ] && tail -40 "$REVIEWER_ACT" >&2 || echo "  (activity missing)" >&2
            fi
            if [ "$MSG_SEEN" -eq 1 ]; then
                pass "reviewer activity contains send_message targeting $FORGE_NAME"
            else
                fail "reviewer did NOT send_message to $FORGE_NAME within 240s"
            fi

            # --- Phase 7: Depth-2 no-recursion canary (WARN-only) ---
            echo ""
            echo "=== Phase 7: depth-2 no-recursion canary (observational) ==="
            local RECURSE_HIT=0
            if [ -f "$REVIEWER_ACT" ]; then
                # Look at first ~50 lines (early frames) for any subagent spawn.
                if head -100 "$REVIEWER_ACT" 2>/dev/null \
                    | grep "mcp__sprawl__spawn" \
                    | grep -q '"subagent"[[:space:]]*:[[:space:]]*true'; then
                    RECURSE_HIT=1
                fi
            fi
            if [ "$RECURSE_HIT" -eq 1 ]; then
                echo "  WARN: reviewer auto-spawned a subagent at depth 2 — file follow-up to harden the prompt"
            else
                echo "  (depth-2 canary clean: reviewer did not auto-spawn a subagent)"
            fi
        fi
    fi

    e2e_print_results
}
