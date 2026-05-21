#!/usr/bin/env bash
# scripts/e2e-tests/drain-row-inject.sh — QUM-569 regression guard.
# Migrated from scripts/test-drain-row-inject-e2e.sh (which remains in place).
# Drives a real claude child to call mcp__sprawl__messages_send so the
# Send → defaultNotifier → WakeForDelivery → claude prompt-inject →
# drain-row citation pipeline is exercised end-to-end.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-drain-e2e"

    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum569"
    e2e_install_cleanup_traps

    git -C "$SPRAWL_ROOT" init -b main --quiet
    git -C "$SPRAWL_ROOT" -c user.name="Test" -c user.email="test@test" \
        commit --allow-empty -m "init" --quiet
    mkdir -p "$SPRAWL_ROOT/.sprawl"
    echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"

    # scripts/run-claude shim needs .env in the sandbox to rehydrate auth
    # for spawned child subshells.
    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi

    local SESSION="sprawl-drain-e2e-$(head -c4 /dev/urandom | xxd -p)"
    local PROBE="DRAIN-PROBE-$$-$(date +%s)"
    local BRANCH_SUFFIX
    BRANCH_SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    local STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo "  PROBE=$PROBE"
    echo ""

    # Ensure spawned child subshells can rehydrate CLAUDE_CODE_OAUTH_TOKEN.
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

    echo ""
    echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
    e2e_attach_phantom_client "$SESSION"

    echo ""
    echo "=== Driving weave to spawn the drain-probe child ==="
    local SPAWN_PROMPT
    SPAWN_PROMPT="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum-569-drain-probe-${BRANCH_SUFFIX}', and prompt set to exactly: 'You are an automated QUM-569 probe. STEP 1: IMMEDIATELY call mcp__sprawl__messages_send with to=\"weave\", body=\"DRAIN-PROBE-SENTINEL: ${PROBE}\". STEP 2: call mcp__sprawl__report_status with state=complete, summary=\"drain probe sent\". STEP 3: Stop. Do nothing else. Do not read any files, do not run any commands.'"
    e2e_send_user_prompt "$SESSION" "$SPAWN_PROMPT"

    echo ""
    echo "=== Waiting for spawn to land (poll .sprawl/agents/ for new *.json) ==="
    local ELAPSED=0
    local SPAWN_LANDED=0
    local CHILD_STATE=""
    local CHILD_NAME=""
    while [ "$ELAPSED" -lt 180 ]; do
        local candidate local_name
        while IFS= read -r candidate; do
            [ -z "$candidate" ] && continue
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
    if [ "$SPAWN_LANDED" -eq 1 ]; then
        pass "child spawned (name=$CHILD_NAME, state=$CHILD_STATE)"
    else
        fail "no non-weave state file appeared within 180s — weave's claude did not call spawn"
        echo "  agents dir:" >&2
        ls -la "$SPRAWL_ROOT/.sprawl/agents/" >&2 2>/dev/null || true
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    echo ""
    echo "=== Waiting for inbox banner (sanity check, maildir watcher) ==="
    if wait_for_pattern_fast "$SESSION" "inbox: [0-9]+ new message" 60; then
        pass "inbox banner appeared in weave's viewport (maildir watcher still alive)"
    else
        fail "inbox banner never appeared within 60s — child may not have called messages_send"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
        echo "  child state:" >&2
        sed 's/^/    /' "$CHILD_STATE" >&2 2>/dev/null || echo "    <missing>" >&2
        e2e_print_results
        return 1
    fi

    echo ""
    echo "=== Primary assertion: drain-row prompt-inject from $CHILD_NAME ==="
    # em-dash is U+2014 — use fixed-substring grep via wait_for_substring_fast.
    local DRAIN_NEEDLE="From ${CHILD_NAME} — mcp__sprawl__messages_read(id="
    if wait_for_substring_fast "$SESSION" "$DRAIN_NEEDLE" 90; then
        pass "drain-row citation '$DRAIN_NEEDLE...' appeared in weave's pane (QUM-555/QUM-323 path live)"
    else
        fail "drain-row citation '$DRAIN_NEEDLE...' did NOT appear in weave's pane within 90s"
        echo "  Send → defaultNotifier → WakeForDelivery → claude prompt-inject path is broken" >&2
        echo "  pane tail (80 lines):" >&2
        capture_pane "$SESSION" | tail -80 >&2
        echo "  child state:" >&2
        sed 's/^/    /' "$CHILD_STATE" >&2 2>/dev/null || echo "    <missing>" >&2
        echo "  weave state:" >&2
        sed 's/^/    /' "$SPRAWL_ROOT/.sprawl/agents/weave.json" >&2 2>/dev/null || echo "    <missing>" >&2
    fi

    e2e_print_results
}
