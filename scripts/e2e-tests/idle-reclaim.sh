#!/usr/bin/env bash
# scripts/e2e-tests/idle-reclaim.sh — QUM-1186 lane 3 idle-reaper gate.
#
# Re-homes the coverage that died with `report-then-send` (QUM-866). That row
# existed solely to pin Real.ReportStatus's StopAfterTurn call; report_status is
# deleted and (*Real).maybeReclaimIdle is now StopAfterTurn's ONLY production
# caller, so the e2e coverage follows the call rather than evaporating.
#
# What this row proves that no unit test can: that reclamation is an OBSERVED
# fact about the operating system. AgentRuntime.SubprocessAlive() is an
# in-process nil check on a handle — it is exactly the field-assertion this
# slice exists to get away from, and a probe built on it would go green with
# 280MB still resident. Every liveness claim below is `kill -0` against a PID
# resolved from /proc, and the revival claim is a DIFFERENT pid, not the absence
# of the old one.
#
# Phases (P3/P5 are the same probe pointed in opposite directions, which is what
# makes either of them evidence):
#   1. spawn an idle child; resolve its real claude PID from /proc and record RSS.
#   2. precondition: that PID is alive.
#   3. wait past the threshold — disk status must reach `idle` AND `kill -0`
#      must FAIL. Probe direction: MUST FIRE.
#   4. send_message the reclaimed child; a NEW and DIFFERENT PID must answer.
#      Asserting merely that the old PID is gone is much weaker — it cannot tell
#      reclamation from a crash.
#   5. NEGATIVE CONTROL, same probe: a child kept busy across the whole
#      threshold window must still be alive and must NOT be `idle`. Direction:
#      MUST STAY QUIET. Without this, P3 could be passing because the harness
#      kills children for some unrelated reason.
#   6. POSITIVE CONTROL for the knob: relaunch with idle_reclaim.after=0 and
#      show a comparably idle child is NOT reclaimed. Without this, P3 could be
#      passing for a reason that has nothing to do with the reaper.
#
# The threshold is set through the sandbox's .sprawl/config.yaml, which NewReal
# reads ONCE at startup — so it must be written before every `sprawl enter`
# launch, and phase 6 needs a relaunch rather than a config edit.

# QUM-1029: the number of assertions a COMPLETE, PASSING run makes. Counted
# from the pass sites below, each of which is on the single success path (every
# alternative fails and returns): P1, P2, P3a, P3b, P4a, P4b, P5a, P5b, P5c,
# P6a, P6b. Eleven.
MIN_ASSERTIONS=11

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
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-idle-reclaim-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum1186-idle"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if ! command -v pgrep >/dev/null 2>&1; then
        e2e_skip_row "idle-reclaim: pgrep is not on PATH, so no PID can be resolved from /proc. Every assertion in this row is an OS-level liveness check; without pgrep the row would assert nothing."
    fi
    if [ ! -r /proc/self/status ]; then
        e2e_skip_row "idle-reclaim: /proc is not readable, so RSS and cwd cannot be resolved. This row's whole claim is that reclamation is observed at the OS level."
    fi

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SUFFIX
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    local SESSION="sprawl-idle-reclaim-e2e-$SUFFIX"

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

    # ----- Phase 1: an idle child ------------------------------------------
    echo ""
    echo "=== Phase 1: spawn a child that answers once and then goes quiet ==="
    local BRANCH_IDLE="qum1186-idle-${SUFFIX}"
    ir_spawn_child "$SESSION" "$BRANCH_IDLE" \
        "You are a QUM-1186 idle-reclaim probe. Reply with exactly the word READY and then stop. Do not call any tools and do not write any files." \
        "SPAWN_${SUFFIX}"

    local STATE_IDLE NAME_IDLE WORKTREE_IDLE
    if ! STATE_IDLE=$(ir_find_child_by_branch "$BRANCH_IDLE"); then
        fail "P1: no child state appeared within 180s for branch $BRANCH_IDLE"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    NAME_IDLE=$(jq -r '.name' "$STATE_IDLE")
    WORKTREE_IDLE=$(jq -r '.worktree // empty' "$STATE_IDLE")
    pass "P1: idle child spawned (name=$NAME_IDLE worktree=$WORKTREE_IDLE)"

    # ----- Phase 2: resolve the REAL pid and its RSS -------------------------
    echo ""
    echo "=== Phase 2: resolve the child's claude PID from /proc and record RSS ==="
    local PID_IDLE RSS_BEFORE
    if ! PID_IDLE=$(ir_wait_child_pid "$WORKTREE_IDLE" 180); then
        fail "P2: no claude process with cwd=$WORKTREE_IDLE appeared within 180s"
        pgrep -af claude >&2 || true
        e2e_print_results
        return 1
    fi
    RSS_BEFORE=$(ir_rss_kb "$PID_IDLE")
    pass "P2: child claude PID=$PID_IDLE alive, VmRSS=${RSS_BEFORE:-unknown} kB"

    # ----- Phase 3: the reap, observed at the OS level -----------------------
    # PROBE DIRECTION: MUST FIRE. Its quiet-direction twin is P5.
    echo ""
    echo "=== Phase 3: after the threshold the subprocess is GONE and the agent rests idle ==="
    local REAP_BUDGET=$((IR_THRESHOLD_SECS + IR_SWEEP_SECS + 120))
    if ir_wait_status "$STATE_IDLE" "idle" "$REAP_BUDGET"; then
        pass "P3a: disk Status=idle (the reaper rested it via StopAfterTurn(stopReasonIdleReclaim))"
    else
        fail "P3a: disk Status did not reach 'idle' within ${REAP_BUDGET}s (got '$(jq -r '.status // empty' "$STATE_IDLE" 2>/dev/null || true)')"
        cat "$STATE_IDLE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    if ir_wait_pid_gone "$PID_IDLE" 120; then
        pass "P3b: kill -0 $PID_IDLE FAILS — the ~${RSS_BEFORE:-?} kB of RSS was actually returned to the OS"
    else
        fail "P3b: PID $PID_IDLE is STILL ALIVE after the agent rested idle. The status was stamped but the subprocess was not reclaimed — this is the memory leak the row exists to catch, and a SubprocessAlive()-based probe would not have seen it. VmRSS now: $(ir_rss_kb "$PID_IDLE") kB"
        e2e_print_results
        return 1
    fi

    # ----- Phase 4: revival brings back a DIFFERENT process ------------------
    echo ""
    echo "=== Phase 4: send_message revives the agent as a NEW process ==="
    _stmux send-keys -t "$SESSION" "Call mcp__sprawl__send_message with to='${NAME_IDLE}' and body='wake up and reply OK'. Then reply 'SENT_${SUFFIX} ok' and nothing else."
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    if ir_wait_status "$STATE_IDLE" "active" 180; then
        pass "P4a: disk Status back to 'active' after a message to a reclaimed agent (no wake_if_offline needed)"
    else
        fail "P4a: reclaimed agent did not return to 'active' within 180s (got '$(jq -r '.status // empty' "$STATE_IDLE" 2>/dev/null || true)'). A reclaimed agent that cannot be revived is worse than one that was never reaped."
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    local PID_AFTER
    if ! PID_AFTER=$(ir_wait_child_pid "$WORKTREE_IDLE" 180); then
        fail "P4b: no claude process reappeared for $WORKTREE_IDLE within 180s after revival"
        pgrep -af claude >&2 || true
        e2e_print_results
        return 1
    fi
    if [ "$PID_AFTER" != "$PID_IDLE" ]; then
        pass "P4b: a NEW process answers (PID $PID_IDLE -> $PID_AFTER); revival is a real relaunch, not a stale handle"
    else
        fail "P4b: the revived agent reports the SAME PID ($PID_AFTER) as before the reap. Either the process was never reclaimed, or the PID was recycled — either way the reclaim-then-revive claim is unproven."
        e2e_print_results
        return 1
    fi

    # ----- Phase 5: NEGATIVE CONTROL — a busy agent is not reaped ------------
    # PROBE DIRECTION: MUST STAY QUIET. Same probe as P3, subject known clean.
    echo ""
    echo "=== Phase 5: NEGATIVE CONTROL — a busy child survives the whole threshold window ==="
    local BRANCH_BUSY="qum1186-busy-${SUFFIX}"
    local BUSY_SECS=$((IR_THRESHOLD_SECS * 3))
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
    pass "P5a: busy control spawned (PID=$PID_BUSY, running sleep ${BUSY_SECS})"

    # Sleep past the threshold + a sweep, while the child is demonstrably in a
    # turn. The reaper must observe InTurn and refuse.
    sleep $((IR_THRESHOLD_SECS + IR_SWEEP_SECS * 3))

    if kill -0 "$PID_BUSY" 2>/dev/null; then
        pass "P5b: busy child PID $PID_BUSY still alive after $((IR_THRESHOLD_SECS + IR_SWEEP_SECS * 3))s (> the ${IR_THRESHOLD_SECS}s threshold) — the reaper did not cut off a running turn"
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
