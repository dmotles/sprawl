#!/usr/bin/env bash
# scripts/e2e-tests/idle-reclaim-busy.sh — the idle reaper's NEGATIVE half.
#
# Its sibling idle-reclaim.sh proves the reaper reclaims an idle agent, with real
# PID/RSS evidence. This row is the other direction: an agent that is NOT done
# must not be reclaimed. Both directions are needed — "the busy agent survived"
# is satisfied by a mechanism that never acts, and "the idle agent was reaped" is
# satisfied by one that reaps everything.
#
# TWO AXES, and QUM-1197 is the proof they are different questions. The row used
# to conflate them, and its skip text asserted a mechanism that was refuted:
#
#   IS A TURN OPEN?        A foreground tool call. The turn stays open, and
#                          `in_turn` blocks the reap. This is P5. (The old skip
#                          text claimed in_turn cannot see a child mid-tool-call.
#                          That was investigated across five runs and NOT
#                          supported: QUM-1186 lane 3 made InTurnObserved the
#                          union of the session probe and the phase machine.)
#
#   IS WORK OUTSTANDING?   A BACKGROUNDED tool call, or a live sidechain. The
#                          turn CLOSES — `stop=end_turn` — and every one of the
#                          six original terms read `idle` HONESTLY while the work
#                          ran. That is the real defect QUM-1197 found: recorded,
#                          a child backgrounded `sleep 900`, was reaped 34s later,
#                          and 866 seconds of live work died with it. This is P7,
#                          and it is what the seventh term (`work_outstanding`)
#                          exists for.
#
# A precondition that only checks "a live sleep exists in the child's tree" is
# satisfied by BOTH shapes, which is why it could not tell them apart. P7's
# precondition therefore has a WIRE half as well as an OS half: a
# background_tasks_changed frame with a non-empty task set, FOLLOWED by the turn's
# terminal frame. That ordering IS "the turn closed with work outstanding".
#
# VALIDATION STATE, 2026-08-11, stated here because a row nobody has watched fail
# is a claim rather than a gate:
#
#   WATCHED GREEN — yes. 11 passed / 0 failed after the QA rework, and the two
#   axes are now separately ATTRIBUTED in the same run: P5c reads
#   `blocker=in_turn` for the foreground child, P7c reads
#   `blocker=work_outstanding` for the backgrounded one.
#
#   WATCHED RED — ONE mutation of four. QA (`sentry`) deleted the
#   work_outstanding term from the verdict table on a quiet host and the row
#   failed with the reap record naming the work it destroyed
#   (`work_outstanding=busy n=1 local_bash:bxiilz51r:age=36s`). The other three —
#   ingest disabled so the term reads `unobservable`; term unconditionally busy
#   (P8 must fire); the in_turn term deleted (the rewritten P5 must fire) — are
#   still row-unwatched. SEVEN attempts across them all died at
#   `P5a: no busy-control child appeared within 180s`, which is the QUM-1212 host
#   condition and NOT the mutation. A red for the wrong reason proves nothing, so
#   none of those counts.
#
#   The remaining three ARE watched at unit level, which is weaker: it shows the
#   terms are falsifiable, not that this row would catch their removal. Whoever
#   next gets a quiet host owes them — one row run each, mutations named above.
#
# Phases:
#
#   P5  TURN-OPEN axis, direction MUST STAY QUIET. Spawn a child, wait until a
#       real `sleep` process exists in its tree — an OS-level fact that the tool
#       call is in flight, checked at BOTH ends of the threshold window — and
#       assert its claude PID survives and it is not stamped `idle`.
#
#       That precondition is load-bearing, and it was learned the hard way. The
#       first version asserted only that a child we had TOLD to run `sleep 90`
#       was alive later. That cannot distinguish "the reaper spares busy agents"
#       from "the model finished early and was legitimately reclaimed" — it
#       measured our own prompt, not the product. AN INSTRUCTION TO AN AGENT IS
#       NOT AN OBSERVATION OF AN AGENT; it is the same error as trusting a
#       self-report, one level up. It produced a red that had to be withdrawn.
#       Keep the /proc precondition, and keep the "precondition lapsed => no
#       verdict in either direction" arm: a control that can say "I don't know"
#       is worth more than one that always answers.
#
#   P6  POSITIVE CONTROL for the knob: relaunch with idle_reclaim.after=0 and
#       show a comparably idle child is NOT reclaimed — which is what separates
#       "the reaper did it" from "children die here anyway".
#
#   P7  WORK-OUTSTANDING axis, direction MUST STAY QUIET, and the phase this row
#       was rewritten for. A child backgrounds a tool call and ENDS ITS TURN;
#       it must survive the threshold window. P7c is the part that makes the
#       survival evidence rather than luck: the refusal record must name
#       `blocker=work_outstanding`. Without it the child could have survived
#       because some other term happened to block, and the row would bank a pass
#       the new term never earned.
#
#   P8  POSITIVE CONTROL for the term, in the same session and window: a child
#       with NO outstanding work IS still reaped. This is the control against the
#       measured failure mode of a work-outstanding term — too eager to say busy,
#       so the reaper never fires, and the mechanism looks safe because it does
#       nothing. P6 controls the knob; only P8 controls the term.
#
# QUM-1029: a complete, passing run of the body below makes ELEVEN assertions —
# P5a, P5b, P5c, P5d, P6a, P6b, P7a, P7b, P7c, P7d, P8. Counted against the written
# body, not derived arithmetically: only `pass` calls on the single success path
# count, and the hard-fail precondition gates count because a green run passes
# through them.
#
# REACHED: this row runs, so the floor is enforced. It was unreachable for as
# long as the row was skipped (e2e_skip_row exits before e2e_print_results), which
# is why the passing assertions lived in the sibling idle-reclaim row. If this row
# is ever skipped again, say so here — a floor the aggregator never reaches
# enforces nothing, and an annotation claiming otherwise is a false record.
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

# ir_reaper_log echoes the path of the log the reaper's records actually land in.
#
# This is not the file e2e_launch_tui redirects to. The harness points the child's
# stderr at $SPRAWL_ROOT/.sprawl/tui-stderr.log, but `sprawl enter` re-redirects
# FD 2 into $SPRAWL_ROOT/.sprawl/logs/tui-stderr-<ts>.log once the TUI starts, so
# the harness's file holds only pre-TUI output. Grepping the wrong one returns
# nothing and reads as "the record does not exist" — it cost a reader exactly that
# during QUM-1197.
ir_reaper_log() {
    ls -t "$SPRAWL_ROOT"/.sprawl/logs/tui-stderr-*.log 2>/dev/null | head -1
}

# ir_wire_log echoes the newest session wire log for agent $1.
ir_wire_log() {
    ls -t "$SPRAWL_ROOT"/.sprawl/logs/sessions/"$1"/*.ndjson 2>/dev/null | head -1
}

# ir_wire_work_outstanding_at_close greps a child's wire log for the ordering
# that IS this row's subject: a background_tasks_changed frame carrying a
# non-empty task set, FOLLOWED by that turn's terminal `result` frame.
#
# Carries its own parse floor. A scan that fails must not read as a scan that
# came back clean — during this issue a broken jq pipeline returned a confident
# ZERO twice over a corpus that contained 1,615 of these frames. Exit codes:
#   0 = the ordering was observed;  1 = parsed fine, ordering absent;
#   2 = could not parse anything, so NO claim is available in either direction.
ir_wire_work_outstanding_at_close() {
    local log="$1"
    [ -n "$log" ] && [ -r "$log" ] || return 2
    # The wire log wraps each frame as {"ts","dir","seq","raw":"<escaped JSON>"},
    # so the inner keys appear as \"type\" rather than "type". Unescaping first
    # makes this work against BOTH encodings instead of silently matching neither
    # — which is exactly what happened on the first live run, where this function
    # returned "could not parse" and the floor below refused to render a verdict
    # rather than reporting the ordering absent.
    awk '
        { line = $0; gsub(/\\/, "", line) }
        line ~ /"subtype":"background_tasks_changed"/ {
            parsed++
            # A non-empty task set always carries at least one task_id; the drain
            # frame is tasks:[] and carries none.
            pending = (line ~ /"task_id"/) ? 1 : 0
            next
        }
        line ~ /"type":"result"/ {
            parsed++
            if (pending) { closed = 1 }
        }
        END {
            if (parsed == 0) { exit 2 }
            if (closed)      { exit 0 }
            exit 1
        }
    ' "$log"
}

# ir_dump_agent_records prints the reaper records for agent $1 from log $2 to
# stderr, bounded, with a real "none" fallback.
#
# It exists because both failure dumps had the same bug and QA caught it:
#
#   grep "agent=$N" "$LOG" >&2 | tail -20 || echo "  (none)" >&2
#
# The `>&2` redirects grep's stdout BEFORE the pipe, so `tail` reads EOF: the
# bound is inert and every matching record prints, and `||` then tests tail's
# status (0), so the fallback can never fire. Measured against a 25-record log:
# the broken form emits 25 lines where 20 was intended, and emits NOTHING at all
# when there are no records. The fixed form emits 20 and "  (none)".
#
# That matters because this dump only ever runs on a red, i.e. exactly when
# someone needs it to explain the failure.
ir_dump_agent_records() {
    local name="$1" log="$2" recs=""
    # Three outcomes, not two. "nothing came back" from a MISSING log and from a
    # log that genuinely has no record for this agent mean opposite things — the
    # first says the probe looked in the wrong place, the second is evidence about
    # the product — and a dump that renders them identically sends the reader
    # down the wrong path at the one moment they are relying on it.
    if [ -z "$log" ]; then
        echo "  (NO LOG PATH resolved — the record could not be looked for at all, so this red says nothing about agent=$name)" >&2
        return
    fi
    if [ ! -r "$log" ]; then
        echo "  (log '$log' is MISSING or unreadable — the record could not be looked for, so this red says nothing about agent=$name)" >&2
        return
    fi
    recs=$(grep "agent=$name" "$log" | tail -20 || true)
    if [ -n "$recs" ]; then
        printf '%s\n' "$recs" >&2
    else
        echo "  (log '$log' is readable and contains NO record for agent=$name — the agent was never assessed)" >&2
    fi
}

# ir_wire_turn_still_open answers P5's axis question from the wire: has a `result`
# frame arrived since the child's most recent `system/init`? If not, the turn is
# still open.
#
# Same encoding hazard and the same floor as ir_wire_work_outstanding_at_close:
# the log wraps frames as {"ts","dir","seq","raw":"<escaped JSON>"}, so unescape
# before matching, and report "could not parse" apart from "the turn closed".
# Exit codes: 0 = still open; 1 = parsed, and the turn has closed; 2 = nothing
# could be parsed, so NO claim is available.
ir_wire_turn_still_open() {
    local log="$1"
    [ -n "$log" ] && [ -r "$log" ] || return 2
    awk '
        { line = $0; gsub(/\\/, "", line) }
        line ~ /"subtype":"init"/   { parsed++; open = 1; next }
        line ~ /"type":"result"/    { parsed++; open = 0 }
        END {
            if (parsed == 0) { exit 2 }
            if (open)        { exit 0 }
            exit 1
        }
    ' "$log"
}

test_run() {
    # UN-SKIPPED 2026-08-11, on the QUM-1197 (c)+provenance ruling. The skip that
    # stood here asserted a REFUTED mechanism as fact — that in_turn cannot see a
    # child mid-tool-call — which five runs did not support; the real defect was
    # the missing work_outstanding term, and P7 below is its gate. If this row must
    # be blocked again, skip at the TOP of test_run (the body stays, so restoring
    # is deleting one line), name the ACTUAL blocker, and restore the
    # P19-INERT-ROW marker plus the inert-row bookkeeping in
    # scripts/test-e2e-matrix-unit.sh in the same commit.
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
    # NOT `sleep`. QUM-1197 QA (F2): the CLI backgrounds a long foreground
    # `sleep` on this host, the turn CLOSES, and P5's subject silently became a
    # WORK-OUTSTANDING case — i.e. P7's axis, with in_turn=idle in the record at
    # both samples. The row's two axes collapsed into one and the turn-open axis
    # was untested while P5b's text asserted the opposite. `timeout N tail -f
    # /dev/null` is a real foreground wait that stays in the turn.
    ir_spawn_child "$SESSION" "$BRANCH_BUSY" \
        "You are a QUM-1186 turn-open control. Run exactly one Bash command in the FOREGROUND (do NOT background it, do not set run_in_background): timeout ${BUSY_SECS} tail -f /dev/null. Wait for it. When it finishes, reply BUSY_DONE and stop. Call no other tools." \
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
        SLEEP_PID=$(pgrep -f "timeout ${BUSY_SECS} tail" 2>/dev/null | head -1 || true)
        [ -n "$SLEEP_PID" ] && break
        sleep 2
        sp_elapsed=$((sp_elapsed + 2))
    done
    if [ -z "$SLEEP_PID" ]; then
        fail "P5a: no live 'timeout ${BUSY_SECS} tail' process appeared within 180s, so the busy child was never observably mid-tool-call. This control CANNOT run without that precondition — asserting on a child we merely instructed to be busy would measure our prompt, not the reaper. HARD FAIL BY DESIGN: reported as a red rather than a skip so that this gate is still a gate when the row is re-hosted — e2e_skip_row exits 77 before e2e_print_results, which would leave the restored row's MIN_ASSERTIONS floor unenforced. An unmet premise here is host or model timing, not a reaper defect."
        pgrep -af 'tail|claude' >&2 || true
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # The WIRE half of P5's precondition, and the reason F2 was possible without
    # it: an OS-level live process is satisfied by BOTH axes. Only the absence of
    # a turn terminal — no `result` since this child's last init — establishes
    # that the turn is still OPEN, which is what P5 claims to test. Same parse
    # floor discipline as P7a: "could not parse" is reported apart from "the turn
    # closed", because a scan that fails is not a scan that came back clean.
    local BUSY_NAME BUSY_WIRE
    BUSY_NAME=$(jq -r '.name // empty' "$STATE_BUSY")
    # POLL, do not sample once. A freshly spawned child's log can hold only
    # control frames for a few seconds, so "cannot tell yet" (rc 2) is a TIMING
    # artifact at t=0, not an unmet premise — and hard-failing on it makes this
    # gate fail for a reason that has nothing to do with the axis it checks.
    # Found the honest way: a mutation run that was supposed to red on P8 red on
    # this instead. Only a rc-2 that PERSISTS to the deadline is a real
    # "cannot tell", and rc 1 (the turn has closed) is decisive immediately.
    local wire_rc2=2 w_elapsed=0
    while [ "$w_elapsed" -lt 120 ]; do
        BUSY_WIRE=$(ir_wire_log "$BUSY_NAME")
        ir_wire_turn_still_open "$BUSY_WIRE"
        wire_rc2=$?
        [ "$wire_rc2" -ne 2 ] && break
        sleep 3
        w_elapsed=$((w_elapsed + 3))
    done
    case "$wire_rc2" in
        0) : ;;
        2)
            fail "P5a: the wire log for '$BUSY_NAME' still could not be parsed after ${w_elapsed}s (log='$BUSY_WIRE'), so whether the turn is open is unknown and NO verdict is available in either direction. HARD FAIL BY DESIGN."
            e2e_print_results
            return 1
            ;;
        *)
            fail "P5a: the child's turn has already CLOSED (a result frame followed its last init), so this subject is a work-outstanding case — P7's axis — not the turn-open one P5 tests. That collapse is QUM-1197 F2, and it is why this precondition exists. HARD FAIL BY DESIGN: precondition unmet, not a reaper verdict."
            e2e_print_results
            return 1
            ;;
    esac
    pass "P5a: busy control is OBSERVABLY mid-tool-call AND its turn is still OPEN (claude PID=$PID_BUSY, foreground wait PID=$SLEEP_PID)"

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
    SLEEP_STILL=$(pgrep -f "timeout ${BUSY_SECS} tail" 2>/dev/null | head -1 || true)
    if [ -z "$SLEEP_STILL" ] && ! kill -0 "$SLEEP_PID" 2>/dev/null; then
        fail "P5b: PRECONDITION LAPSED, not a product verdict. The child's claude PID $PID_BUSY is alive, but no 'timeout ${BUSY_SECS} tail' remains in its process tree, so it was not observably busy for the whole ${IR_THRESHOLD_SECS}s+ window. A pass here would be unearned — the reaper may simply have had nothing to reap yet. HARD FAIL BY DESIGN, as at P5a: a red rather than a 77, so the floor of the re-hosted row stays enforceable. Host or model timing is the likelier cause than the reaper."
        e2e_print_results
        return 1
    fi

    if kill -0 "$PID_BUSY" 2>/dev/null; then
        pass "P5b: busy child PID $PID_BUSY still alive after $((IR_THRESHOLD_SECS + IR_SWEEP_SECS * 3))s (> the ${IR_THRESHOLD_SECS}s threshold), with its foreground wait PID $SLEEP_PID still in flight — the reaper did not cut off a running turn"
    else
        fail "P5b: busy child PID $PID_BUSY was killed while mid-turn. The predicate's InTurn term is not blocking the reap, and work is being destroyed."
        cat "$STATE_BUSY" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    # P5's attribution, the twin of P7c. Without it P5b's survival is unattributed:
    # the child could have survived because work_outstanding or quiescent blocked,
    # and the row would credit the turn-open axis for a refusal it did not earn —
    # which is exactly how F2 went unnoticed. Exact token, so
    # `in_turn_unobservable` cannot satisfy it: that reading means the observation
    # broke, not that a turn was seen.
    local BUSY_REAPER_LOG
    BUSY_REAPER_LOG=$(ir_reaper_log)
    if [ -n "$BUSY_REAPER_LOG" ] && grep -q "agent=$BUSY_NAME .*blocker=in_turn " "$BUSY_REAPER_LOG"; then
        pass "P5c: the refusal record attributes the refusal to the TURN-OPEN axis (agent=$BUSY_NAME blocker=in_turn)"
    else
        fail "P5c: no refusal record naming agent=$BUSY_NAME with blocker=in_turn in '$BUSY_REAPER_LOG'. P5b's survival is therefore not attributable to the turn term — another term may have blocked, which is the axis collapse QUM-1197 F2 describes. Records for this agent:"
        ir_dump_agent_records "$BUSY_NAME" "$BUSY_REAPER_LOG"
        e2e_print_results
        return 1
    fi

    local BUSY_STATUS
    BUSY_STATUS=$(jq -r '.status // empty' "$STATE_BUSY" 2>/dev/null || true)
    if [ "$BUSY_STATUS" != "idle" ]; then
        pass "P5d: busy child status is '$BUSY_STATUS', not 'idle'"
    else
        fail "P5d: busy child was stamped 'idle' while mid-turn"
        e2e_print_results
        return 1
    fi

    # ----- Phase 7: WORK OUTSTANDING — the axis QUM-1197 actually found ------
    # PROBE DIRECTION: MUST STAY QUIET (the child must survive).
    #
    # Different question from P5. Here the child BACKGROUNDS its tool call and
    # ends its turn, so `in_turn` reads idle honestly and only the
    # work_outstanding term stands between the reaper and live work.
    echo ""
    echo "=== Phase 7: a child with BACKGROUNDED work ends its turn and must survive ==="
    local BRANCH_BG="qum1197-bg-${SUFFIX}"
    local BG_SECS=$((IR_THRESHOLD_SECS * 10))
    ir_spawn_child "$SESSION" "$BRANCH_BG" \
        "You are a QUM-1197 work-outstanding control. Run exactly one Bash command IN THE BACKGROUND (run_in_background: true): sleep ${BG_SECS}. Then reply BG_STARTED and stop immediately — do not wait for it, and call no other tools." \
        "SPAWND_${SUFFIX}"

    local STATE_BG WORKTREE_BG PID_BG BG_NAME
    if ! STATE_BG=$(ir_find_child_by_branch "$BRANCH_BG"); then
        fail "P7a: no work-outstanding child appeared within 180s for branch $BRANCH_BG"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    BG_NAME=$(jq -r '.name // empty' "$STATE_BG")
    WORKTREE_BG=$(jq -r '.worktree // empty' "$STATE_BG")
    if ! PID_BG=$(ir_wait_child_pid "$WORKTREE_BG" 180); then
        fail "P7a: no claude process with cwd=$WORKTREE_BG appeared within 180s"
        pgrep -af claude >&2 || true
        e2e_print_results
        return 1
    fi

    # OS half of the precondition: the backgrounded command is really running.
    local BG_SLEEP_PID="" bg_elapsed=0
    while [ "$bg_elapsed" -lt 180 ]; do
        BG_SLEEP_PID=$(pgrep -f "sleep ${BG_SECS}" 2>/dev/null | head -1 || true)
        [ -n "$BG_SLEEP_PID" ] && break
        sleep 2
        bg_elapsed=$((bg_elapsed + 2))
    done
    if [ -z "$BG_SLEEP_PID" ]; then
        fail "P7a: no live 'sleep ${BG_SECS}' appeared within 180s, so no work was ever outstanding and this control cannot run. HARD FAIL BY DESIGN rather than a skip: e2e_skip_row exits 77 before e2e_print_results, which would leave this row's MIN_ASSERTIONS floor unenforced. An unmet premise here is host or model timing, not a reaper defect."
        pgrep -af 'sleep|claude' >&2 || true
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi

    # WIRE half — and this is what separates P7 from P5. A live `sleep` alone is
    # satisfied by a FOREGROUND call too; only the frame ordering establishes that
    # the turn CLOSED with work still outstanding.
    local BG_WIRE
    BG_WIRE=$(ir_wire_log "$BG_NAME")
    local wire_elapsed=0 wire_rc=1
    while [ "$wire_elapsed" -lt 120 ]; do
        BG_WIRE=$(ir_wire_log "$BG_NAME")
        ir_wire_work_outstanding_at_close "$BG_WIRE"
        wire_rc=$?
        [ "$wire_rc" -eq 0 ] && break
        sleep 3
        wire_elapsed=$((wire_elapsed + 3))
    done
    if [ "$wire_rc" -eq 2 ]; then
        fail "P7a: the wire log for '$BG_NAME' could not be parsed at all (log='$BG_WIRE'), so NO claim is available in either direction. A scan that fails is not a scan that came back clean. HARD FAIL BY DESIGN."
        e2e_print_results
        return 1
    fi
    if [ "$wire_rc" -ne 0 ]; then
        fail "P7a: parsed the wire log for '$BG_NAME' but never saw a background_tasks_changed frame with a non-empty task set FOLLOWED by a result frame, so the turn did not close with work outstanding — this is P5's shape, not P7's, and the two are different questions. HARD FAIL BY DESIGN: precondition unmet, not a reaper verdict."
        e2e_print_results
        return 1
    fi
    pass "P7a: turn CLOSED with work outstanding — wire shows background_tasks_changed(non-empty) then result, and 'sleep ${BG_SECS}' is live (claude PID=$PID_BG, sleep PID=$BG_SLEEP_PID)"

    # Past the threshold plus several sweeps: an unprotected agent is reaped here.
    sleep $((IR_THRESHOLD_SECS + IR_SWEEP_SECS * 3))

    if ! kill -0 "$PID_BG" 2>/dev/null; then
        fail "P7b: work-outstanding child PID $PID_BG was killed. Its turn had closed but 'sleep ${BG_SECS}' was live, so the reaper destroyed work the agent intended to return to — exactly the 866-second loss QUM-1197 records. The work_outstanding term is not blocking the reap."
        cat "$STATE_BG" >&2 2>/dev/null || true
        ir_reaper_log >&2 || true
        e2e_print_results
        return 1
    fi
    if ! kill -0 "$BG_SLEEP_PID" 2>/dev/null; then
        fail "P7b: PRECONDITION LAPSED, not a product verdict. claude PID $PID_BG is alive but 'sleep ${BG_SECS}' (PID $BG_SLEEP_PID) is gone, so work was not outstanding for the whole window and a pass would be unearned. HARD FAIL BY DESIGN, as at P7a."
        e2e_print_results
        return 1
    fi
    pass "P7b: work-outstanding child PID $PID_BG and its backgrounded 'sleep' both survived $((IR_THRESHOLD_SECS + IR_SWEEP_SECS * 3))s (> the ${IR_THRESHOLD_SECS}s threshold)"

    # P7c is what makes P7b evidence rather than luck. Without it the child could
    # have survived because `quiescent` or `in_turn` happened to block, and the row
    # would bank a pass the new term never earned.
    local REAPER_LOG
    REAPER_LOG=$(ir_reaper_log)
    if [ -z "$REAPER_LOG" ] || [ ! -r "$REAPER_LOG" ]; then
        fail "P7c: no reaper log under \$SPRAWL_ROOT/.sprawl/logs/tui-stderr-*.log, so the refusal record cannot be read and P7b's survival is unattributed. HARD FAIL BY DESIGN."
        e2e_print_results
        return 1
    fi
    # Exact token: 'blocker=work_outstanding ' with the trailing space, because
    # 'blocker=work_outstanding_unobservable' is a DIFFERENT reading — it means
    # the set could not be observed at all, and a row that accepted it would be
    # green because the mechanism is broken.
    if grep -q "agent=$BG_NAME .*blocker=work_outstanding " "$REAPER_LOG"; then
        pass "P7c: the refusal record attributes the refusal to the new term (agent=$BG_NAME blocker=work_outstanding)"
    else
        fail "P7c: no refusal record naming agent=$BG_NAME with blocker=work_outstanding in $REAPER_LOG. P7b's survival is therefore NOT attributable to the work_outstanding term — some other term may have blocked, or the term read 'unobservable' (which is the mechanism failing safe, not working). Records for this agent:"
        ir_dump_agent_records "$BG_NAME" "$REAPER_LOG"
        e2e_print_results
        return 1
    fi

    local BG_STATUS
    BG_STATUS=$(jq -r '.status // empty' "$STATE_BG" 2>/dev/null || true)
    if [ "$BG_STATUS" != "idle" ]; then
        pass "P7d: work-outstanding child status is '$BG_STATUS', not 'idle'"
    else
        fail "P7d: work-outstanding child was stamped 'idle' while its backgrounded work was live"
        e2e_print_results
        return 1
    fi

    # ----- Phase 8: POSITIVE CONTROL for the term ----------------------------
    # Direction: MUST FIRE. In the SAME session and window, an agent with nothing
    # outstanding is still reclaimed. This is the control against the measured
    # failure mode of a work-outstanding term: too eager to say busy, so the
    # reaper never fires and the mechanism looks safe because it does nothing.
    # P6 controls the knob; only this controls the term.
    echo ""
    echo "=== Phase 8: POSITIVE CONTROL — an agent with NO outstanding work is still reaped ==="
    local BRANCH_CLEAN="qum1197-clean-${SUFFIX}"
    ir_spawn_child "$SESSION" "$BRANCH_CLEAN" \
        "You are a QUM-1197 positive control. Reply with exactly the word READY and then stop. Call no tools and write no files." \
        "SPAWNE_${SUFFIX}"

    local STATE_CLEAN WORKTREE_CLEAN PID_CLEAN
    if ! STATE_CLEAN=$(ir_find_child_by_branch "$BRANCH_CLEAN"); then
        fail "P8: no positive-control child appeared within 180s for branch $BRANCH_CLEAN"
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    WORKTREE_CLEAN=$(jq -r '.worktree // empty' "$STATE_CLEAN")
    if ! PID_CLEAN=$(ir_wait_child_pid "$WORKTREE_CLEAN" 180); then
        fail "P8: no claude process with cwd=$WORKTREE_CLEAN appeared within 180s"
        pgrep -af claude >&2 || true
        e2e_print_results
        return 1
    fi
    if ir_wait_pid_gone "$PID_CLEAN" $((IR_THRESHOLD_SECS * 3 + IR_SWEEP_SECS * 3)); then
        pass "P8: the no-work child (PID $PID_CLEAN) WAS reclaimed — the term protects work without disabling the reaper"
    else
        fail "P8: the no-work child (PID $PID_CLEAN) was never reclaimed. A work_outstanding term that blocks everything is a mechanism that never acts, and P7's 'the busy agent survived' would then be worth nothing."
        cat "$STATE_CLEAN" >&2 2>/dev/null || true
        ir_reaper_log >&2 || true
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

