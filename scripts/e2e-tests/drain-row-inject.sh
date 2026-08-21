#!/usr/bin/env bash
# scripts/e2e-tests/drain-row-inject.sh — QUM-569 regression guard.
# Migrated from scripts/test-drain-row-inject-e2e.sh, which was deleted once
# this row proved flake-free (QUM-1183).
# Drives a real claude child to call mcp__sprawl__send_message so the
# Send → defaultNotifier → WakeForDelivery → claude prompt-inject →
# drain-row citation pipeline is exercised end-to-end.

# QUM-1029: the number of assertions a COMPLETE, PASSING run of this row
# makes. The unconditional pass after launch plus three symmetric gates, plus
# the three QUM-1276 --effort argv gates (weave low, child low, neither
# medium) added below the spawn gate.
MIN_ASSERTIONS=7

# --- Test-local helpers (single-test scope, not promoted to lib). ---

# _drain_claude_argv <agent-name> — newline-separated argv of the claude
# subprocess launched for <agent-name>, scoped to this sandbox by the
# --system-prompt-file argv pointing at that agent's SYSTEM.md under
# SPRAWL_ROOT (state.WriteSystemPrompt). Empty output + rc 1 when not found.
_drain_claude_argv() {
    local want="$SPRAWL_ROOT/.sprawl/agents/$1/SYSTEM.md" pid
    pid=$(pgrep -af 'claude' 2>/dev/null | awk -v p="$want" 'index($0, p) > 0 { print $1; exit }')
    [ -n "$pid" ] && [ -r "/proc/$pid/cmdline" ] || return 1
    tr '\0' '\n' < "/proc/$pid/cmdline"
}

# _drain_argv_effort — echoes the value following the first --effort token on
# stdin (one argv token per line), or nothing when the flag is absent.
_drain_argv_effort() {
    awk '/^--effort$/ { getline v; print v; exit }'
}

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# wait_for_maildir_sentinel <mailbox_dir> <sentinel> <timeout_secs>
# Polls for a maildir envelope whose body carries <sentinel>. Searches the WHOLE
# mailbox root, not just new/ + cur/: the read path renames new/ -> cur/
# (messages markRead) and messages_archive renames cur/ -> archive/, and weave's
# claude may call messages_archive unprompted after following the drain-row
# citation. Scanning only new/+cur/ would reintroduce a narrower version of the
# very race this gate was rewritten to avoid. Over the whole root the surface is
# genuinely monotone: no path deletes the envelope.
# Returns 0 on found, 1 on timeout — never 0 on a missing directory.
wait_for_maildir_sentinel() {
    local dir="$1" sentinel="$2" timeout="$3" elapsed=0
    while [ "$elapsed" -lt "$timeout" ]; do
        if grep -rqF -- "$sentinel" "$dir" 2>/dev/null; then
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
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
    pass "TUI rendered (weave root pill visible in header tree)"

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
    SPAWN_PROMPT="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum-569-drain-probe-${BRANCH_SUFFIX}', and prompt set to exactly: 'You are an automated QUM-569 probe. STEP 1: IMMEDIATELY call mcp__sprawl__send_message with to=\"weave\", body=\"DRAIN-PROBE-SENTINEL: ${PROBE}\". STEP 2: Stop. Do nothing else. Do not read any files, do not run any commands.'"
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
    echo "=== QUM-1276: --effort low reached the real claude argv ==="
    # A unit test asserting SessionSpec.Effort proves nothing about the flag a
    # real subprocess received. Both weave and the child are known live here
    # (weave drove the spawn; the child's state file just landed), so a pid we
    # cannot resolve is a failure, never a skip.
    local WEAVE_ARGV CHILD_ARGV WEAVE_EFFORT CHILD_EFFORT
    # The state file can land a beat before the subprocess is up, so poll.
    local ARGV_WAIT=0
    while [ "$ARGV_WAIT" -lt 30 ]; do
        WEAVE_ARGV=$(_drain_claude_argv weave || true)
        CHILD_ARGV=$(_drain_claude_argv "$CHILD_NAME" || true)
        [ -n "$WEAVE_ARGV" ] && [ -n "$CHILD_ARGV" ] && break
        sleep 2
        ARGV_WAIT=$((ARGV_WAIT + 2))
    done
    WEAVE_EFFORT=$(printf '%s\n' "$WEAVE_ARGV" | _drain_argv_effort)
    CHILD_EFFORT=$(printf '%s\n' "$CHILD_ARGV" | _drain_argv_effort)

    if [ "$WEAVE_EFFORT" = "low" ]; then
        pass "weave claude argv carries --effort low"
    else
        fail "weave claude argv effort = '${WEAVE_EFFORT:-<absent>}', want low"
        printf '%s\n' "$WEAVE_ARGV" | sed 's/^/    /' >&2
    fi

    if [ "$CHILD_EFFORT" = "low" ]; then
        pass "child $CHILD_NAME claude argv carries --effort low"
    else
        fail "child $CHILD_NAME claude argv effort = '${CHILD_EFFORT:-<absent>}', want low"
        printf '%s\n' "$CHILD_ARGV" | sed 's/^/    /' >&2
    fi

    # Catches a partial edit: one of the two paths still on the old default.
    if [ "$WEAVE_EFFORT" != "medium" ] && [ "$CHILD_EFFORT" != "medium" ]; then
        pass "neither weave nor $CHILD_NAME carries the old --effort medium default"
    else
        fail "stale --effort medium survives (weave='$WEAVE_EFFORT' child='$CHILD_EFFORT')"
    fi

    echo ""
    echo "=== Sanity gate: the child's message became durable in weave's maildir ==="
    # QUM-925: this gate used to wait on the "inbox: N new message" status-bar
    # banner. That banner is TRANSIENT — it is a SetTransientLabel, cleared not by a
    # timer but by the next turn start (setTurnState's Idle->Thinking edge), which
    # the injected frame now triggers immediately — and QUM-925 made delivery
    # INSTANT (producer poke straight to stdin
    # instead of a 2s idle-gated TUI poll), so the banner now routinely comes and
    # goes inside one poll interval. The gate raced a label whose lifetime our own
    # change shortened, and failed while the primary assertion below still passed.
    #
    # It is still a real gate — it checks exactly what its failure message claims,
    # that the child called send_message — but against a DURABLE surface instead
    # of a transient one: messages.Send writes the envelope tmp/ -> new/ (an atomic
    # rename) and no path ever deletes it, only renames it within the mailbox root
    # (new/ -> cur/ on read, cur/ -> archive/ on messages_archive). So the
    # sentinel's presence under the root is monotone once true and cannot be raced
    # by fast delivery. Note this gate covers no sprawl DELIVERY code — that is the
    # primary assertion's job, immediately below.
    if wait_for_maildir_sentinel "$SPRAWL_ROOT/.sprawl/messages/weave" "$PROBE" 60; then
        pass "child's send_message envelope landed durably in weave's maildir (sentinel $PROBE)"
    else
        fail "no maildir envelope carrying $PROBE within 60s — child may not have called send_message"
        echo "  weave maildir:" >&2
        find "$SPRAWL_ROOT/.sprawl/messages/weave" -type f >&2 2>/dev/null || echo "    <missing>" >&2
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
