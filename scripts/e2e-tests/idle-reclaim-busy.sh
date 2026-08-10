#!/usr/bin/env bash
# scripts/e2e-tests/idle-reclaim-busy.sh — QUM-1197 hazard row.
#
# The half of the idle-reaper coverage that CANNOT run today. Its sibling
# idle-reclaim.sh proves the reaper reclaims an idle agent (real PID/RSS
# evidence, and it passes). This row is the other direction: a child kept
# demonstrably busy must NOT be reclaimed — and it fails, because the reaper
# reaps agents that are mid-tool-call.
#
# WHY THIS IS ITS OWN ROW rather than a skip at the end of idle-reclaim.sh:
# e2e_skip_row exits 77 WITHOUT calling e2e_print_results, so a row that ends
# in a skip never reaches the MIN_ASSERTIONS floor. Its assertions would be
# executed but never enforced — someone could delete all of them and the row
# would still exit 77 and look identical. Splitting means the passing half is a
# real gate with a real floor, and the skip means what a skip is supposed to
# mean: this row asserted nothing.
#
# The phases are PRESENT below the skip, not deleted — see the note in test_run
# for why the harness requires that. What they do:
#
#   P5  NEGATIVE CONTROL, direction MUST STAY QUIET. Spawn a child, wait until a
#       real `sleep` process exists in its tree — an OS-level fact that the tool
#       call is in flight, checked at BOTH ends of the threshold window — and
#       assert its claude PID survives and it is not stamped `idle`.
#
#       That precondition is the load-bearing part, and it was learned the hard
#       way. The first version asserted only that a child we had TOLD to run
#       `sleep 90` was alive later. That cannot distinguish "the reaper spares
#       busy agents" from "the model finished early and was legitimately
#       reclaimed" — it measured our own prompt, not the product. AN
#       INSTRUCTION TO AN AGENT IS NOT AN OBSERVATION OF AN AGENT; it is the
#       same error as trusting a self-report, one level up. It produced a red
#       that had to be withdrawn. Keep the /proc precondition, and keep the
#       "precondition lapsed => no verdict in either direction" arm: a control
#       that can say "I don't know" is worth more than one that always answers.
#
#   P6  POSITIVE CONTROL for the knob: relaunch with idle_reclaim.after=0 and
#       show a comparably idle child is NOT reclaimed — which is what separates
#       "the reaper did it" from "children die here anyway".
#
# Do NOT un-skip this while the reaper is disabled by default. It would fail on
# a hazard we already know about, and a row that is expected to fail trains
# every reader to skip past its failures.

# QUM-1029: declared for the restored row (P5a, P5b, P5c, P6a, P6b). Never
# reached today — e2e_skip_row exits before e2e_print_results — and that is
# precisely why the passing assertions live in the sibling row instead.
# QUM-1029: declared for the restored row (P5a, P5b, P5c, P6a, P6b). Never
# reached while the skip is in place — e2e_skip_row exits before
# e2e_print_results — which is exactly why the PASSING assertions live in the
# sibling idle-reclaim row rather than behind this skip.
MIN_ASSERTIONS=5

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# Idle threshold and sweep cadence for phases 1-5. Short enough to keep the row
# under a few minutes, long enough that a child's own spawn turn (which can run
# 30s+) does not trip it while still working.
IR_THRESHOLD_SECS=30
IR_SWEEP_SECS=5

# ir_write_config writes the sandbox config with the given idle threshold.
# $1 = a Go duration for idle_reclaim.after ("30s", or "0" to disable).
ir_write_config() {
    local after="$1"
    mkdir -p "$SPRAWL_ROOT/.sprawl"
    cat > "$SPRAWL_ROOT/.sprawl/config.yaml" <<EOF
idle_reclaim.after: "${after}"
idle_reclaim.sweep: "${IR_SWEEP_SECS}s"
EOF
}

# ir_find_child_by_branch echoes the state.json path of the non-weave agent
# whose .branch is $1, polling up to 180s.
ir_find_child_by_branch() {
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

# ir_child_pid echoes the PID of the claude process whose cwd is the agent's
# worktree ($1). Resolved from /proc rather than from a sprawl data structure:
# the whole point of this row is that the answer comes from the OS.
ir_child_pid() {
    local worktree="$1" pid cwd
    [ -n "$worktree" ] || return 1
    for pid in $(pgrep -x claude 2>/dev/null || true); do
        cwd=$(readlink -f "/proc/$pid/cwd" 2>/dev/null || true)
        if [ -n "$cwd" ] && [ "$cwd" = "$(readlink -f "$worktree" 2>/dev/null)" ]; then
            printf '%s\n' "$pid"
            return 0
        fi
    done
    return 1
}

# ir_wait_child_pid polls ir_child_pid for up to $2 seconds.
ir_wait_child_pid() {
    local worktree="$1" timeout="${2:-180}" elapsed=0 pid
    while [ "$elapsed" -lt "$timeout" ]; do
        if pid=$(ir_child_pid "$worktree"); then
            printf '%s\n' "$pid"
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

# ir_rss_kb echoes VmRSS in kB for $1, or "" if the process is gone.
ir_rss_kb() {
    awk '/^VmRSS:/ {print $2}' "/proc/$1/status" 2>/dev/null || true
}

# ir_wait_status polls the state file until .status == $2, up to $3 seconds.
ir_wait_status() {
    local state_file="$1" expected="$2" timeout="${3:-180}" elapsed=0 status=""
    while [ "$elapsed" -lt "$timeout" ]; do
        status=$(jq -r '.status // empty' "$state_file" 2>/dev/null || true)
        [ "$status" = "$expected" ] && return 0
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

# ir_wait_pid_gone polls `kill -0` until it FAILS, up to $2 seconds.
ir_wait_pid_gone() {
    local pid="$1" timeout="${2:-120}" elapsed=0
    while [ "$elapsed" -lt "$timeout" ]; do
        kill -0 "$pid" 2>/dev/null || return 0
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

# ir_spawn_child sends weave a spawn prompt for branch $1 with child prompt $2.
ir_spawn_child() {
    local session="$1" branch="$2" body="$3" tag="$4"
    local prompt="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='$branch', and prompt set to exactly: '$body'. Then reply '${tag} ok' and nothing else."
    _stmux send-keys -t "$session" "$prompt"
    sleep 0.5
    _stmux send-keys -t "$session" Enter
}

test_run() {
    # SKIP FIRST, then the phases. The body below is UNREACHABLE today and that
    # is deliberate rather than sloppy: scripts/test-e2e-matrix-unit.sh section
    # [17j] fails any row that never calls e2e_print_results, because a floor
    # that the aggregator never reaches is a floor that enforces nothing. The
    # established convention for a blocked row in this repo (report-then-send,
    # wake-on-traffic, complete-lifecycle) is to keep the body and skip at the
    # top — which also means restoring this row is deleting one line rather
    # than reconstructing it from git history.
    e2e_skip_row "idle-reclaim-busy: BLOCKED by QUM-1197 (Urgent) — the idle reaper reaps agents that are mid-tool-call, so idle_reclaim.after ships defaulted to 0 (DISABLED) and this control cannot pass. Reproduced twice on a clean host: a child with a live 'sleep' still in its process tree was torn down, because the in_turn signal the predicate consumes does not see a child executing a long tool call. QUM-1197 also BLOCKS QUM-1187 (the merge quiescence gate reads the same signal, so a merge could be permitted over a working child). DO NOT set idle_reclaim.after until QUM-1197 lands. The reclaim path itself IS covered and passing — see the idle-reclaim row, which proves an idle agent's subprocess is genuinely returned to the OS and revives as a new pid. Restore this row by deleting the e2e_skip_row call below; its phases are intact underneath."

    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-idle-reclaim-busy-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum1186-busy"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if ! command -v pgrep >/dev/null 2>&1; then
        e2e_skip_row "idle-reclaim-busy: pgrep is not on PATH, so no PID can be resolved from /proc. Every assertion in this row is an OS-level liveness check; without pgrep the row would assert nothing."
    fi
    if [ ! -r /proc/self/status ]; then
        e2e_skip_row "idle-reclaim-busy: /proc is not readable, so RSS and cwd cannot be resolved. This row's whole claim is that reclamation is observed at the OS level."
    fi

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SUFFIX
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    local SESSION="sprawl-idle-reclaim-busy-e2e-$SUFFIX"

    ir_write_config "${IR_THRESHOLD_SECS}s"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo "  idle_reclaim.after=${IR_THRESHOLD_SECS}s idle_reclaim.sweep=${IR_SWEEP_SECS}s"

    echo ""
    echo "=== Launching sprawl enter (reaper ENABLED) ==="
    if ! e2e_launch_tui "$SESSION" 200 50; then
        e2e_print_results
        return 1
    fi
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION"

    # ----- Phase 5: NEGATIVE CONTROL — a busy agent is not reaped ------------
    # PROBE DIRECTION: MUST STAY QUIET. Same probe as P3, subject known clean.
    echo ""
    echo "=== Phase 5: NEGATIVE CONTROL — a busy child survives the whole threshold window ==="
    local BRANCH_BUSY="qum1186-busy-${SUFFIX}"
    # 10x the threshold: the window we observe is ~1.5x the threshold, so the
    # tool call has ample headroom to outlive it. The first version used 3x and
    # the sleep still ended first, which cost a whole run to a precondition
    # failure rather than a verdict.
    local BUSY_SECS=$((IR_THRESHOLD_SECS * 10))
    ir_spawn_child "$SESSION" "$BRANCH_BUSY" \
        "You are a QUM-1186 busy-agent control. Run exactly one Bash command: sleep ${BUSY_SECS}. When it finishes, reply BUSY_DONE and stop. Do not call any other tools." \
        "SPAWNB_${SUFFIX}"

    local STATE_BUSY WORKTREE_BUSY PID_BUSY
    if ! STATE_BUSY=$(ir_find_child_by_branch "$BRANCH_BUSY"); then
        fail "P5a: no busy-control child appeared within 180s for branch $BRANCH_BUSY"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    WORKTREE_BUSY=$(jq -r '.worktree // empty' "$STATE_BUSY")
    if ! PID_BUSY=$(ir_wait_child_pid "$WORKTREE_BUSY" 180); then
        fail "P5a: no claude process with cwd=$WORKTREE_BUSY appeared within 180s"
        pgrep -af claude >&2 || true
        e2e_print_results
        return 1
    fi

    # THE CONTROL'S OWN PRECONDITION, and the reason this row was rewritten:
    # an INSTRUCTION to an agent is not an OBSERVATION of an agent. Asserting
    # "a child we told to sleep 90 is alive later" cannot distinguish a reaper
    # that spares busy agents from a model that finished early and was
    # legitimately reclaimed — the control would have been measuring our own
    # prompt. So wait for a real `sleep` process inside the child's tree: that
    # is an OS-level fact that the tool call is genuinely in flight.
    local SLEEP_PID=""
    local sp_elapsed=0
    while [ "$sp_elapsed" -lt 180 ]; do
        SLEEP_PID=$(pgrep -P "$PID_BUSY" -f "sleep" 2>/dev/null | head -1 || true)
        if [ -z "$SLEEP_PID" ]; then
            # The CLI may run Bash under an intermediate shell, so also accept a
            # `sleep <BUSY_SECS>` anywhere under this claude's descendants.
            SLEEP_PID=$(pgrep -f "sleep ${BUSY_SECS}" 2>/dev/null | head -1 || true)
        fi
        [ -n "$SLEEP_PID" ] && break
        sleep 2
        sp_elapsed=$((sp_elapsed + 2))
    done
    if [ -z "$SLEEP_PID" ]; then
        fail "P5a: no live 'sleep ${BUSY_SECS}' process appeared within 180s, so the busy child was never observably mid-tool-call. This control CANNOT run without that precondition — asserting on a child we merely instructed to be busy would measure our prompt, not the reaper."
        pgrep -af 'sleep|claude' >&2 || true
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    pass "P5a: busy control is OBSERVABLY mid-tool-call (claude PID=$PID_BUSY, live sleep PID=$SLEEP_PID)"

    # Sleep past the threshold + a sweep, while the child is demonstrably in a
    # turn. The reaper must observe the turn and refuse.
    sleep $((IR_THRESHOLD_SECS + IR_SWEEP_SECS * 3))

    # Decompose the two ways this can end badly, because they mean opposite
    # things and conflating them is how the first version of this row reported a
    # defect that was never established:
    #
    #   claude GONE          -> product failure. It was observably mid-tool-call
    #                           when the window opened, so it was reaped at work.
    #   claude alive, no sleep -> the control's precondition lapsed. No verdict is
    #                           available in either direction; say so and fail
    #                           rather than bank an unearned pass.
    if ! kill -0 "$PID_BUSY" 2>/dev/null; then
        fail "P5b: busy child PID $PID_BUSY was killed. It was OBSERVABLY mid-tool-call when this window opened (live sleep PID $SLEEP_PID), so the reaper tore down an agent that was doing work. The predicate's turn term is not blocking the reap."
        cat "$STATE_BUSY" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    local SLEEP_STILL
    SLEEP_STILL=$(pgrep -P "$PID_BUSY" -f "sleep" 2>/dev/null | head -1 || true)
    if [ -z "$SLEEP_STILL" ] && ! kill -0 "$SLEEP_PID" 2>/dev/null; then
        fail "P5b: PRECONDITION LAPSED, not a product verdict. The child's claude PID $PID_BUSY is alive, but no 'sleep' remains in its process tree, so it was not observably busy for the whole ${IR_THRESHOLD_SECS}s+ window. A pass here would be unearned — the reaper may simply have had nothing to reap yet."
        e2e_print_results
        return 1
    fi

    if kill -0 "$PID_BUSY" 2>/dev/null; then
        pass "P5b: busy child PID $PID_BUSY still alive after $((IR_THRESHOLD_SECS + IR_SWEEP_SECS * 3))s (> the ${IR_THRESHOLD_SECS}s threshold), with its sleep PID $SLEEP_PID still in flight — the reaper did not cut off a running turn"
    else
        fail "P5b: busy child PID $PID_BUSY was killed while mid-turn. The predicate's InTurn term is not blocking the reap, and work is being destroyed."
        cat "$STATE_BUSY" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    local BUSY_STATUS
    BUSY_STATUS=$(jq -r '.status // empty' "$STATE_BUSY" 2>/dev/null || true)
    if [ "$BUSY_STATUS" != "idle" ]; then
        pass "P5c: busy child status is '$BUSY_STATUS', not 'idle'"
    else
        fail "P5c: busy child was stamped 'idle' while mid-turn"
        e2e_print_results
        return 1
    fi

    # ----- Phase 6: POSITIVE CONTROL for the knob ----------------------------
    # Direction: with the reaper DISABLED, P3's measurement must NOT reproduce.
    # This is what distinguishes "the reaper reclaimed it" from "children die
    # here for some other reason".
    echo ""
    echo "=== Phase 6: POSITIVE CONTROL — with idle_reclaim.after=0 an idle child is NOT reclaimed ==="
    _stmux kill-session -t "$SESSION" 2>/dev/null || true
    sleep 2
    ir_write_config "0"
    local SESSION2="${SESSION}-off"
    if ! e2e_launch_tui "$SESSION2" 200 50; then
        fail "P6: could not relaunch sprawl enter with the reaper disabled"
        e2e_print_results
        return 1
    fi
    if capture_pane "$SESSION2" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION2" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION2"

    local BRANCH_OFF="qum1186-off-${SUFFIX}"
    ir_spawn_child "$SESSION2" "$BRANCH_OFF" \
        "You are a QUM-1186 reaper-disabled control. Reply with exactly the word READY and then stop. Do not call any tools and do not write any files." \
        "SPAWNC_${SUFFIX}"

    local STATE_OFF WORKTREE_OFF PID_OFF
    if ! STATE_OFF=$(ir_find_child_by_branch "$BRANCH_OFF"); then
        fail "P6a: no reaper-disabled control child appeared within 180s for branch $BRANCH_OFF"
        capture_pane "$SESSION2" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    WORKTREE_OFF=$(jq -r '.worktree // empty' "$STATE_OFF")
    if ! PID_OFF=$(ir_wait_child_pid "$WORKTREE_OFF" 180); then
        fail "P6a: no claude process with cwd=$WORKTREE_OFF appeared within 180s"
        pgrep -af claude >&2 || true
        e2e_print_results
        return 1
    fi
    pass "P6a: control child spawned with the reaper disabled (PID=$PID_OFF)"

    sleep $((IR_THRESHOLD_SECS * 2 + IR_SWEEP_SECS * 3))
    local OFF_STATUS
    OFF_STATUS=$(jq -r '.status // empty' "$STATE_OFF" 2>/dev/null || true)
    if kill -0 "$PID_OFF" 2>/dev/null && [ "$OFF_STATUS" != "idle" ]; then
        pass "P6b: with idle_reclaim.after=0 the child is still alive (PID $PID_OFF, status '$OFF_STATUS') after $((IR_THRESHOLD_SECS * 2))s — so P3's reap was caused by the reaper, not by the harness"
    else
        fail "P6b: an idle child was reclaimed (alive=$(kill -0 "$PID_OFF" 2>/dev/null && echo yes || echo no), status '$OFF_STATUS') even though idle_reclaim.after=0. Either 0 does not disable the reaper — in which case the knob is a lie — or P3's reap was never the reaper's doing, in which case P3 proves nothing."
        cat "$STATE_OFF" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    e2e_print_results
}

