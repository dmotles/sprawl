#!/usr/bin/env bash
# scripts/e2e-tests/idle-continuation.sh — QUM-929 (repointed from QUM-815/812/817).
#
# UPSTREAM-REGRESSION DETECTOR. QUM-929 deleted sprawl's [auto-continue] stdin
# injection because the claude CLI self-resumes on background-task completion in
# every timing case (proven on 2.1.219 by 4 direct control experiments + a 6-week
# wire corpus with zero stranded payloads). That means the "an idle bg completion
# still drives a turn" property is now owned ENTIRELY by the CLI. This row is the
# only thing standing between a future CLI that stops self-resuming and silently
# stranded background work in production.
#
# Contract asserted against the REAL claude CLI, with weave IDLE and ZERO
# keystrokes after the background task is started:
#
#   A. NATIVE SELF-RESUME (hard) — a new system/init AND a new turn `result`
#      appear after the bg task completes. Nothing in sprawl can manufacture
#      these anymore, so this fails loudly if the CLI stops self-resuming.
#   B. CLI STILL SIGNALS COMPLETION (hard, diagnostic split from A) — a NEW
#      system/task_notification is emitted on the wire, and its monotonic wire
#      `seq` is GREATER than turn 1's terminal result — which is how "the
#      completion landed while idle" is proven (the pane's idle glyph is not
#      reliably capturable). If turn 1 outran the bg task this fails as a
#      TEST-SETUP error rather than mis-blaming upstream.
#   C. ZERO [auto-continue] INJECTION (hard) — no dir:"in" frame contains the
#      sentinel; the QUM-929 deletion has not regressed.
#   D. ZERO stdin user frames of ANY wording (hard) — catches a re-added nudge
#      that was merely reworded.
#   E. The RETAINED observation still reaches sprawl (hard) — the notification is
#      recorded in weave's activity.ndjson for TUI rendering + telemetry; only the
#      stdin injection was deleted. (Which TUI item it renders as is deliberately
#      not pinned here — see the note at assertion E.)
#   F. Balanced lifecycle (hard) — the TUI is not wedged in Streaming/Thinking
#      after the autonomous turn settles.
#
# Why D is not flake-prone: every stdin `type:"user"` frame comes from
# UnifiedRuntime.writeMessage. In this sandbox during the idle window there are no
# keystrokes, no children (⇒ no maildir/status_change ⇒ drainPendingToStdin and
# PostTurnSweep early-return), no delegated tasks (⇒ feedTasks writes nothing), and
# the supervisor heartbeat that used to be the remaining injector on this path was
# deleted outright by QUM-1071, so nothing periodic can land inside this ~90s
# window at all.
#
# Wire log: $SPRAWL_ROOT/.sprawl/logs/sessions/weave/*.ndjson, one JSON envelope
# per line: {"ts":…,"dir":"in"|"out","raw":"<escaped frame>"} — dir:"in" is
# sprawl→CLI (i.e. anything sprawl injects), dir:"out" is the CLI→sprawl tee,
# which sprawl structurally cannot forge.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# wire_log echoes the path of weave's NEWEST raw NDJSON session log (empty if
# absent). Newest-by-mtime, not alphabetical: a mid-run session restart would
# otherwise pin an older UUID and quietly zero every counter below.
wire_log() {
    ls -t "$SPRAWL_ROOT"/.sprawl/logs/sessions/weave/*.ndjson 2>/dev/null | head -1
}

# count_out_frames SUBTYPE — CLI→sprawl system frames of the given subtype.
# `fromjson? // empty` tolerates the occasional torn/partial frame record without
# aborting the scan or spewing jq errors.
count_out_frames() {
    local f; f=$(wire_log)
    [ -z "$f" ] && { echo -1; return; }
    jq -rc --arg sub "$1" \
        'select(.dir=="out") | .raw | (fromjson? // empty)
         | select(.type=="system" and .subtype==$sub) | 1' "$f" 2>/dev/null | wc -l | tr -d ' '
}

# count_in_user_frames — stdin user-message frames sprawl wrote to the CLI.
count_in_user_frames() {
    local f; f=$(wire_log)
    [ -z "$f" ] && { echo -1; return; }
    jq -rc 'select(.dir=="in") | .raw | (fromjson? // empty) | select(.type=="user") | 1' \
        "$f" 2>/dev/null | wc -l | tr -d ' '
}

# count_autocontinue_in — stdin frames carrying the [auto-continue] sentinel.
# Greps the escaped raw string (robust to frame shape changes).
count_autocontinue_in() {
    local f n; f=$(wire_log)
    [ -z "$f" ] && { echo -1; return; }
    n=$(jq -rc 'select(.dir=="in") | .raw' "$f" 2>/dev/null | grep -cF '[auto-continue]') || n=0
    echo "$n"
}

# last_seq_of TYPE [SUBTYPE] — the wire-log `seq` (single monotonic counter over
# the whole file) of the LAST CLI→sprawl frame of the given type/subtype; -1 if
# none, or if the log carries no usable `seq` (legacy/regressed writer). Used to
# order the turn-1 terminal against the task_notification, which is
# how the "the completion landed while idle" precondition is proven — the pane's
# idle glyph is not reliably visible in a captured pane.
last_seq_of() {
    local f s; f=$(wire_log)
    [ -z "$f" ] && { echo -1; return; }
    s=$(jq -rc --arg t "$1" --arg sub "${2:-}" \
        'select(.dir=="out") | {seq:.seq, m:(.raw | (fromjson? // empty))}
         | select(.m.type==$t) | select($sub=="" or .m.subtype==$sub) | .seq' \
        "$f" 2>/dev/null | tail -1)
    # jq prints the literal "null" for an envelope with no `seq` key (older
    # writers omitted it and transcript.go still reads those logs). "null"
    # sails past a -z test, and every downstream `[ -lt ]` then ERRORS and
    # evaluates FALSE — silently skipping the non-vacuity aborts that keep this
    # row from passing while measuring nothing. Force the abort sentinel unless
    # the value is genuinely an integer.
    [[ "$s" =~ ^[0-9]+$ ]] || s=-1
    echo "$s"
}

# assert_no_injection LABEL BASELINE_IN_USER — the C/D gate, factored so it can be
# re-checked after a later observation window (catching a late-firing nudge).
# Returns 1 on failure (caller must propagate).
assert_no_injection() {
    local label="$1" baseline="$2" ac_in in_user
    ac_in=$(count_autocontinue_in)
    if [ "$ac_in" -ne 0 ]; then
        fail "[$label] sprawl wrote ${ac_in} [auto-continue] frame(s) to the CLI stdin — the QUM-929 deletion has regressed"
        return 1
    fi
    in_user=$(count_in_user_frames)
    if [ "$in_user" -ne "$baseline" ]; then
        fail "[$label] sprawl wrote $((in_user - baseline)) stdin user frame(s) during the idle window (baseline=${baseline} now=${in_user}) — a continuation nudge was re-added, possibly under different wording"
        jq -rc 'select(.dir=="in") | .raw' "$(wire_log)" 2>/dev/null | tail -5 >&2
        return 1
    fi
    pass "[$label] zero [auto-continue] frames AND zero stdin user frames of any wording (baseline ${baseline} held)"
}

# Count "kind":"result" entries in weave's activity.ndjson (one per completed
# turn).
count_results() {
    local f="$1" n
    [ -f "$f" ] || { echo 0; return; }
    # grep -c prints the count AND exits 1 when there are zero matches, so
    # capture the printed "0" and swallow the non-zero exit (avoid emitting a
    # second line).
    n=$(grep -c '"kind":"result"' "$f" 2>/dev/null) || n=0
    echo "$n"
}

# wait_results_ge FILE TARGET TIMEOUT — poll until the result count reaches
# TARGET or the deadline elapses. Returns 0 on success.
wait_results_ge() {
    local f="$1" target="$2" timeout="$3"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        if [ "$(count_results "$f")" -ge "$target" ]; then
            return 0
        fi
        sleep 1
    done
    return 1
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-idlecont-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum929"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    # Real claude needs auth; the run-claude shim re-hydrates the token from
    # $SPRAWL_ROOT/.env in the spawned shell (QUM-518).
    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SUFFIX SESSION STDERR_LOG
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    SESSION="sprawl-idlecont-${SUFFIX}"
    STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"
    local WEAVE_ACT="$SPRAWL_ROOT/.sprawl/agents/weave/activity.ndjson"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"

    echo ""
    echo "=== Launching sprawl enter (real claude via run-claude shim) ==="
    _stmux new-session -d -s "$SESSION" -x 200 -y 50 \
        "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_CLAUDE='$SPRAWL_CLAUDE' '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
    _stmux set-option -t "$SESSION" window-size manual >/dev/null
    _stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

    if ! wait_for_pattern "$SESSION" "weave " 45; then
        fail "TUI did not render within 45s"
        capture_pane "$SESSION" | tail -30 >&2
        [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
        e2e_print_results
        return 1
    fi
    pass "TUI rendered (weave root visible)"
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION"

    # =====================================================================
    # Drive: weave starts a short-lived background task, then ends its turn.
    # =====================================================================
    # sleep 25 (not 12): turn 1 must END well before the task completes, or the
    # completion lands mid-turn, there is no idle window, and assertion A would
    # false-alarm "CLI behavior change" (a false FAIL is as costly as a false PASS
    # for a regression detector).
    local PROBE="BGDONE_${SUFFIX}"
    local PROMPT="Use the Bash tool with run_in_background=true and command exactly: sleep 25; echo ${PROBE}. Start it in the background and then IMMEDIATELY end your turn — do not wait for it, do not call BashOutput, reply with just the word STARTED and stop."

    echo ""
    echo "=== Turn 1: start background task, then go idle ==="
    e2e_send_user_prompt "$SESSION" "$PROMPT"

    # Wait for the first (sprawl) turn to complete → 1 result.
    if ! wait_results_ge "$WEAVE_ACT" 1 90; then
        fail "first turn never produced a result (weave did not run the prompt)"
        capture_pane "$SESSION" | tail -40 >&2
        [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
        e2e_print_results
        return 1
    fi
    local RESULTS_AFTER_T1 INITS_BEFORE IN_USER_BEFORE NOTIFS_BEFORE
    RESULTS_AFTER_T1=$(count_results "$WEAVE_ACT")
    INITS_BEFORE=$(count_out_frames init)
    IN_USER_BEFORE=$(count_in_user_frames)
    NOTIFS_BEFORE=$(count_out_frames task_notification)

    # NON-VACUITY PRECONDITION. Turn 1 necessarily produced at least one out-init
    # and one in-user frame (the prompt). If the wire log can't be read, every
    # counter is -1/0 and assertions C/D would pass while measuring nothing.
    if [ "$INITS_BEFORE" -lt 1 ] || [ "$IN_USER_BEFORE" -lt 1 ]; then
        fail "wire-log counters are not live (out-inits=${INITS_BEFORE}, in-user-frames=${IN_USER_BEFORE}; both must be >=1 after turn 1) — log=$(wire_log). Assertions below would be vacuous; aborting."
        e2e_print_results
        return 1
    fi
    pass "Turn 1 completed (results=${RESULTS_AFTER_T1}, out-inits=${INITS_BEFORE}, in-user-frames=${IN_USER_BEFORE}, task-notifs=${NOTIFS_BEFORE}); wire-log counters live"

    # Turn 1's terminal `result` seq — the ordering anchor for the HARD idle-window
    # precondition asserted after the completion arrives (below).
    local T1_RESULT_SEQ
    T1_RESULT_SEQ=$(last_seq_of result)
    if [ "$T1_RESULT_SEQ" -lt 0 ]; then
        fail "no CLI result frame found in the wire log after turn 1 (log=$(wire_log)) — cannot order the bg completion against the turn boundary; aborting rather than testing vacuously"
        e2e_print_results
        return 1
    fi
    pass "turn 1 terminal result at wire seq=${T1_RESULT_SEQ} (idle-window anchor)"

    # =====================================================================
    # A. NATIVE SELF-RESUME. WITHOUT any further keystroke, the bg task
    # (sleep 25) completes while idle and the CLI must open a turn ON ITS OWN.
    # Since QUM-929 sprawl injects nothing, a new init + a new result can only
    # come from the CLI. This is the upstream-regression gate.
    # =====================================================================
    echo ""
    echo "=== A. CLI-native self-resume: bg completion must drive a turn with NO prompt and NO injection ==="
    local TARGET=$((RESULTS_AFTER_T1 + 1))
    if ! wait_results_ge "$WEAVE_ACT" "$TARGET" 90; then
        fail "CLI did NOT natively self-resume on background-task completion (results stuck at $(count_results "$WEAVE_ACT"), want >=${TARGET}). Since QUM-929 sprawl injects no [auto-continue] nudge, this is a REAL CLI BEHAVIOR CHANGE / regression — background work will strand. Re-verify against the current claude version before touching sprawl."
        # Distinguish an upstream regression from a test-setup artifact BEFORE the
        # ordering gate below gets a chance to run: if the notification arrived at a
        # seq at or below turn 1's result, the completion landed mid-turn and there
        # was no idle window to test.
        echo "  --- triage: notifs=$(count_out_frames task_notification) notif_seq=$(last_seq_of system task_notification) vs turn-1 result seq=${T1_RESULT_SEQ} ---" >&2
        echo "  (if notif_seq is -1 the CLI never signalled completion; if it is <= ${T1_RESULT_SEQ} this is a TEST-SETUP failure — turn 1 outran the bg task — NOT an upstream regression)" >&2
        echo "  --- weave activity.ndjson tail ---" >&2
        [ -f "$WEAVE_ACT" ] && tail -25 "$WEAVE_ACT" >&2
        echo "  --- pane tail ---" >&2
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    local INITS_AFTER
    INITS_AFTER=$(count_out_frames init)
    if [ "$INITS_AFTER" -le "$INITS_BEFORE" ]; then
        fail "no new system/init on the wire after the bg completion (before=${INITS_BEFORE} after=${INITS_AFTER}) — the CLI did not open an autonomous turn, so the result-count rise did not come from a self-resume"
        e2e_print_results
        return 1
    fi
    pass "CLI self-resumed with zero input: results>=${TARGET}, out-inits ${INITS_BEFORE}→${INITS_AFTER}"

    # =====================================================================
    # B. The CLI still emits the completion signal sprawl observes for the
    # ↻ marker + telemetry. Split from A so a contract change is diagnosable.
    # =====================================================================
    local NOTIFS_AFTER
    NOTIFS_AFTER=$(count_out_frames task_notification)
    if [ "$NOTIFS_AFTER" -le "$NOTIFS_BEFORE" ]; then
        fail "no NEW system/task_notification on the wire for this completion (before=${NOTIFS_BEFORE} after=${NOTIFS_AFTER}) — the CLI's completion signal is gone; sprawl's ↻ auto-continued marker + task telemetry are now blind (CLI contract change)"
        e2e_print_results
        return 1
    fi
    pass "CLI emitted a new system/task_notification (${NOTIFS_BEFORE}→${NOTIFS_AFTER}); sprawl's observation input is intact"

    # HARD idle-window precondition, now that the notification exists: it must have
    # arrived AFTER turn 1's terminal result, i.e. genuinely while weave was idle.
    # If turn 1 outran the bg task the completion landed mid-turn, there was no idle
    # window, and A would have blamed upstream for a test-setup artifact.
    local NOTIF_SEQ
    NOTIF_SEQ=$(last_seq_of system task_notification)
    if [ "$NOTIF_SEQ" -le "$T1_RESULT_SEQ" ]; then
        fail "the task_notification (seq=${NOTIF_SEQ}) did not arrive after turn 1's result (seq=${T1_RESULT_SEQ}) — the completion landed mid-turn, so there was no idle window. TEST-SETUP failure, not a CLI regression: lengthen the bg sleep."
        e2e_print_results
        return 1
    fi
    pass "bg completion landed while IDLE (notif seq=${NOTIF_SEQ} > turn-1 result seq=${T1_RESULT_SEQ}) — a real idle window was tested"

    # The background task actually ran (sentinel in activity or wire log).
    if grep -rqF "$PROBE" "$WEAVE_ACT" "$SPRAWL_ROOT/.sprawl/logs/sessions/weave" 2>/dev/null; then
        pass "background task sentinel '${PROBE}' observed (bg task completed, result reached the session)"
    else
        echo "  (note: sentinel '${PROBE}' not located in logs; self-resume still proven by init + result-count rise)"
    fi

    # =====================================================================
    # C/D. ZERO sprawl injection. C keys on the sentinel; D is wording-proof.
    # =====================================================================
    echo ""
    echo "=== C/D. Zero sprawl stdin injection in the idle window ==="
    if ! assert_no_injection "C/D" "$IN_USER_BEFORE"; then
        e2e_print_results
        return 1
    fi

    # =====================================================================
    # E. The RETAINED observation still reaches sprawl. QUM-929 deleted only the
    # stdin injection; the notification is still observed, published, and recorded
    # for the TUI + telemetry. weave's activity.ndjson is the deterministic proof.
    #
    # NOT asserted here: which TUI item the frame renders as. On the current CLI a
    # run_in_background Bash notification carries a tool_use_id, so QUM-914 routes
    # it to TaskCompletedMsg (finishing the Bash/Agent group) rather than to the
    # ↻ auto-continued marker, which needs a notification with NO tool_use_id.
    # Pinning ↻ from a bg Bash task would encode a false expectation; that chain is
    # unit-covered (protocol_mapping_test, app_test, replay_test).
    # =====================================================================
    echo ""
    echo "=== E. Retained task_notification observation reaches sprawl (TUI + telemetry) ==="
    if ! grep -qa '"summary":"task_notification"' "$WEAVE_ACT" 2>/dev/null; then
        fail "no task_notification entry in weave's activity.ndjson — the OBSERVATION that QUM-929 deliberately RETAINED (TUI rendering + telemetry) is no longer reaching sprawl"
        echo "  --- task_notification frames on the wire ---" >&2
        jq -rc 'select(.dir=="out") | .raw | (fromjson? // empty)
                | select(.type=="system" and .subtype=="task_notification")' \
            "$(wire_log)" >&2 2>/dev/null
        e2e_print_results
        return 1
    fi
    pass "task_notification observed and recorded by sprawl (retained observation intact)"

    # =====================================================================
    # F. Balanced start/complete: the TUI must not be wedged in a streaming
    # state after the CLI's autonomous turn settles.
    # =====================================================================
    echo ""
    echo "=== F. Balanced lifecycle: no streaming wedge after the autonomous turn ==="
    sleep 4
    if capture_pane "$SESSION" | grep -qiE "Streaming\\.\\.\\.|Thinking\\.\\.\\.|esc to interrupt"; then
        fail "TUI appears wedged in a streaming/thinking state after the autonomous turn (unbalanced lifecycle)"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi
    pass "TUI settled (no streaming wedge) — the CLI's autonomous turn emitted a balanced lifecycle"

    # Free extension of the observation window: re-check C/D now that several more
    # seconds have elapsed, catching a late-firing nudge.
    if ! assert_no_injection "C/D re-check" "$IN_USER_BEFORE"; then
        e2e_print_results
        return 1
    fi

    e2e_print_results
}
