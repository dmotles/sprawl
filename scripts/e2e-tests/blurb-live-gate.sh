#!/usr/bin/env bash
# scripts/e2e-tests/blurb-live-gate.sh — QUM-899 live acceptance gate.
#
# Spawns a real researcher agent with a substantive, specific task, lets it
# work against a real claude, then asserts the auto-generated capability
# BLURB appears (non-empty) in the agent's on-disk state.json and reflects
# what the agent knows / was doing. Also confirms BlurbAt is persisted
# (restart-survivable by construction) and prints the blurb for the human
# to eyeball ("does it answer what it knows / was doing").
#
# This is the QUM-899 AC #6 live readout. It is NOT a mandatory matrix row
# (it costs a real claude blurb generation); run it explicitly:
#   make build && bash scripts/e2e-matrix.sh blurb-live-gate
# Requires: claude on PATH, tmux, jq.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# Wait until the named child's state.json has a non-empty .blurb (timeout $2s).
blurb_wait_nonempty() {
    local state_file="$1" timeout="${2:-180}" elapsed=0 blurb=""
    while [ "$elapsed" -lt "$timeout" ]; do
        blurb=$(jq -r '.blurb // empty' "$state_file" 2>/dev/null || true)
        if [ -n "$blurb" ]; then
            return 0
        fi
        sleep 3
        elapsed=$((elapsed + 3))
    done
    return 1
}

# Find a non-weave child state.json by branch. Echoes path.
blurb_find_child_by_branch() {
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
    e2e_setup_tmux_socket "sprawl-blurb-live-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum899"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SESSION="sprawl-blurb-live-e2e-$(head -c4 /dev/urandom | xxd -p)"
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

    # ----- Spawn a researcher with a SUBSTANTIVE, specific task --------------
    echo ""
    echo "=== Spawn researcher with a substantive task ==="
    local BRANCH="qum899-blurb-${SUFFIX}"
    # A concrete task so the generated blurb has real subject matter to capture.
    local PROMPT_BODY="You are researching how Go's context.Context cancellation propagates through goroutines for issue QUM-899. First call mcp__sprawl__report_status with state=working and summary=\"investigating context cancellation\". Then use ToolSearch/available tools to think briefly about context.WithCancel and context.WithTimeout propagation, and write 2-3 sentences of findings as your report. Then call mcp__sprawl__report_status with state=complete and summary=\"context cancellation findings written\". Do not write files."
    local SPAWN_PROMPT="Call mcp__sprawl__spawn with family='product', type='researcher', branch='$BRANCH', and prompt set to exactly: '$PROMPT_BODY'. Then reply 'SPAWN_${SUFFIX} ok' and nothing else."
    _stmux send-keys -t "$SESSION" "$SPAWN_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    local STATE NAME
    if ! STATE=$(blurb_find_child_by_branch "$BRANCH"); then
        fail "no child state appeared within 180s for branch $BRANCH"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    NAME=$(jq -r '.name' "$STATE")
    pass "child spawned (name=$NAME)"

    # ----- AC1: initial blurb appears within seconds -------------------------
    echo ""
    echo "=== AC1/AC6: capability blurb generated + persisted ==="
    if blurb_wait_nonempty "$STATE" 200; then
        local BLURB BLURB_AT
        BLURB=$(jq -r '.blurb // empty' "$STATE")
        BLURB_AT=$(jq -r '.blurb_at // empty' "$STATE")
        pass "blurb persisted to state.json (non-empty)"
        echo "  --- BLURB (${#BLURB} chars) ------------------------------------"
        echo "  $BLURB"
        echo "  --- BlurbAt: $BLURB_AT"
        echo "  ----------------------------------------------------------------"
        if [ "${#BLURB}" -gt 600 ]; then
            fail "blurb exceeds sane cap (${#BLURB} chars > 600)"
        else
            pass "blurb within length cap (${#BLURB} chars)"
        fi
        if echo "$BLURB" | grep -qiE "context|cancel|research|goroutine|QUM-899"; then
            pass "blurb references the agent's actual task subject"
        else
            echo "  NOTE: blurb did not match expected keywords — eyeball above."
        fi
    else
        fail "no non-empty blurb appeared within 200s"
        cat "$STATE" >&2 2>/dev/null || true
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # ----- AC4: persistence watermark present (restart-survivable) -----------
    echo ""
    echo "=== AC4: BlurbAt watermark persisted ==="
    local BAT
    BAT=$(jq -r '.blurb_at // empty' "$STATE")
    if [ -n "$BAT" ] && [ "$BAT" != "0001-01-01T00:00:00Z" ]; then
        pass "blurb_at watermark persisted ($BAT) — survives restart by construction"
    else
        fail "blurb_at watermark missing/zero"
    fi

    e2e_print_results
}
