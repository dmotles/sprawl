#!/usr/bin/env bash
# scripts/e2e-tests/idle-interrupt-inject.sh — QUM-619 + QUM-821 guard.
#
# QUM-821 rewrote send_message(now=true) content delivery: it no longer
# issues a bare Session.Interrupt for delivery. Instead the inbound message is
# written to the CLI stdin at priority "now" (cancel-and-replace urgency) via
# the cooperative WakeForDelivery path. The bare interrupt frame is reserved
# for Esc-abort only and never carries content.
#
# Two scenarios against a real claude binary:
#
#   PHASE 1 — idle recipient (QUM-619 regression):
#     weave sends send_message(now=true) to an idle child; the now-priority
#     stdin write must wake it so it reads the message and ACKs. Pre-QUM-619 the
#     bare interrupt cancelled the just-injected notification turn and dropped it.
#
#   PHASE 2 — mid-turn recipient (QUM-821 urgency + storm regression gate):
#     The child is driven into a long single turn (foreground `sleep`). While
#     mid-turn, weave sends an urgent send_message(now=true). Assertions:
#       (a) the child's urgent ACK reaches weave (now-priority preempts the
#           in-flight turn — empirically it ACKs well before the sleep ends).
#       (b) STORM REGRESSION GATE: the child's raw NDJSON shows a BOUNDED number
#           of now-priority stdin writes for that single urgent send. A now
#           message's isReplay ack is NOT GUARANTEED (QUM-1068 measured 51 of 54
#           echoed, so 3 were not), so without the synchronous mark-on-write fix
#           an un-acked entry stays in pending/ and PostTurnSweep re-injects it
#           every turn (~1990 writes / 1989 turns in 68s). One urgent send must
#           produce a handful of writes, not thousands.
#           (QUM-821 originally recorded this as "a now message yields no
#           isReplay ack" — measured false by QUM-1068; the gate is unaffected,
#           only the stated reason changed.)
#
# Esc-abort-carries-no-content is verified at the unit layer (QUM-821:
# TestInterrupt_CarriesNoContent + supervisor now-priority drain tests); the
# bare interrupt frame structurally cannot carry content.

# A single urgent now-send to a busy child should produce ~1 now-priority stdin
# write (empirically exactly 1 post-fix). Keep a tight bound to catch a partial
# re-inject regression; a storm is thousands, so even a few is suspicious.
NOW_WRITE_STORM_BOUND=5

# QUM-1029: the number of assertions a COMPLETE, PASSING run of this row
# makes. Nine pass sites, all green-reachable; the final chain's other arms
# fail. QUM-1186 lane 5 raised this from 8, and the story is worth keeping
# because the first attempt was wrong. The nested empirical if/else deciding
# whether `now` had PREEMPTED the in-flight turn echoed on BOTH arms, so it
# contributed 0 and could not fail — the primary interrupt-semantics row
# asserted nothing about urgency. Converting it to a pass/fail gate made it
# fire, which then showed the asserted behaviour is BIMODAL (9s and 43s on two
# clean runs) because mid-tool abandonment is the CLI's call, not sprawl's. The
# ninth assertion is therefore a DIFFERENT gate: sprawl must ISSUE the
# now-priority write promptly, mid-turn. That is the half sprawl owns, it holds
# every run, and the timing stayed as a diagnostic.
#
# 10 since QA's Category-3 sweep: PHASE 2 now ASSERTS its own premise. It used
# to sleep 14s and then claim the child was "still inside its ${BUSY_SECS}s
# turn" without ever checking — so an improvising model that never called Bash
# left the child idle and the phase green on a false premise. The tenth
# assertion is the /proc precondition (a live `sleep` under the child's claude
# PID); the both-ends check folds into the existing now-write gate rather than
# adding an eleventh.
MIN_ASSERTIONS=10

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# wait_for_new_child polls .sprawl/agents for a child whose name is not already
# seen. Echoes "name|statefile" on success; returns 1 on timeout.
wait_for_new_child() {
    local timeout="$1"; shift
    local seen=" $* "
    local elapsed=0 candidate local_name
    while [ "$elapsed" -lt "$timeout" ]; do
        while IFS= read -r candidate; do
            [ -z "$candidate" ] && continue
            local_name=$(jq -r '.name // empty' "$candidate" 2>/dev/null || true)
            [ -z "$local_name" ] && continue
            [ "$local_name" = "weave" ] && continue
            case "$seen" in *" $local_name "*) continue ;; esac
            echo "${local_name}|${candidate}"
            return 0
        done < <(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null)
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

# QUM-1186: readiness used to be read from the child's state.json
# self-reported summary field, which is deleted along with the tool that wrote
# it. It is now observed as a message the child actually delivered to weave —
# e2e_wait_maildir_substring, scripts/lib/e2e-common.sh. That is a strictly
# better gate for THIS row in particular: the thing it needs to know before
# sending an interrupt is that the child's delivery path is up, and the new
# probe observes exactly that, where the old one observed only a self-claim.

# count_now_writes counts now-priority stdin user-message frames written to a
# child's raw NDJSON session log (the storm regression signal).
count_now_writes() {
    local agent="$1"
    local f
    # Newest-by-mtime, not alphabetical: a mid-run session restart would
    # otherwise pin a pre-restart UUID and silently zero the storm gate.
    f=$(ls -t "$SPRAWL_ROOT"/.sprawl/logs/sessions/"$agent"/*.ndjson 2>/dev/null | head -1)
    [ -z "$f" ] && { echo "-1"; return; }
    jq -rc 'select(.dir=="in") | .raw | (fromjson? // empty) | select(.type=="user" and .priority=="now") | 1' "$f" 2>/dev/null | wc -l | tr -d ' '
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-idleint-e2e"

    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum821"
    e2e_install_cleanup_traps

    git -C "$SPRAWL_ROOT" init -b main --quiet
    git -C "$SPRAWL_ROOT" -c user.name="Test" -c user.email="test@test" \
        commit --allow-empty -m "init" --quiet
    mkdir -p "$SPRAWL_ROOT/.sprawl"
    echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"
    [ -f "$REPO_ROOT/.env" ] && cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"

    local SESSION="sprawl-idleint-e2e-$(head -c4 /dev/urandom | xxd -p)"
    local PROBE="IDLE-INTERRUPT-PROBE-$$-$(date +%s)"
    local NOW_PROBE="URGENT-NOW-PROBE-$$-$(date +%s)"
    local SUFFIX1 SUFFIX2
    SUFFIX1="$(head -c4 /dev/urandom | xxd -p)"
    SUFFIX2="$(head -c4 /dev/urandom | xxd -p)"

    echo "  SPRAWL_ROOT=$SPRAWL_ROOT  SESSION=$SESSION"
    echo "  PROBE=$PROBE  NOW_PROBE=$NOW_PROBE"
    echo ""

    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    echo "=== Launching sprawl enter ==="
    if ! e2e_launch_tui "$SESSION" 200 50; then
        return 1
    fi
    pass "TUI rendered ('weave' root visible in header tree)"

    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION"

    # ---------------------------------------------------------------------
    # PHASE 1 — idle recipient (QUM-619 regression).
    # ---------------------------------------------------------------------
    echo ""
    echo "=== PHASE 1: spawn idle probe child ==="
    local P1_CHILD P1_SPAWN
    P1_CHILD="You are an automated QUM-821 idle probe. STEP 1: IMMEDIATELY call mcp__sprawl__send_message with to=\"weave\" and body=\"PHASE1-PROBE-READY-${SUFFIX1}\". STEP 2: Stop your turn and wait. STEP 3 (next turn, on an inbound system-notification): call mcp__sprawl__messages_read. If the body contains \"${PROBE}\", call mcp__sprawl__send_message with to=\"weave\" and body=\"IDLE-PROBE-ACK: <copy the body you just read here>\". Then stop. Do nothing else; do not read files; do not run commands."
    P1_SPAWN="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum821-idle-${SUFFIX1}', and prompt set to exactly the following text (do not modify it): '${P1_CHILD}'"
    e2e_send_user_prompt "$SESSION" "$P1_SPAWN"

    local CHILD1 CHILD1_NAME CHILD1_STATE
    if ! CHILD1=$(wait_for_new_child 180 weave); then
        fail "phase-1 child did not spawn within 180s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results; return 1
    fi
    CHILD1_NAME="${CHILD1%%|*}"; CHILD1_STATE="${CHILD1#*|}"
    pass "phase-1 child spawned (name=$CHILD1_NAME)"

    if e2e_wait_maildir_substring weave "PHASE1-PROBE-READY-${SUFFIX1}" 120; then
        pass "phase-1 child reached idle (its readiness message was delivered to weave)"
    else
        fail "phase-1 child's readiness message never reached weave within 120s"
        sed 's/^/    /' "$CHILD1_STATE" >&2 2>/dev/null || true
        e2e_print_results; return 1
    fi
    sleep 3  # let the runtime fully park before the interrupt.

    echo ""
    echo "=== Driving weave to send now=true (now-priority) to idle $CHILD1_NAME ==="
    e2e_send_user_prompt "$SESSION" \
        "Call mcp__sprawl__send_message with to='${CHILD1_NAME}', body='${PROBE}', and now=true. Do nothing else. Do not read files, do not run commands."

    # QUM-1186 lane 5 — A DEFECT THIS MIGRATION INTRODUCED, recorded because the
    # mechanism generalises. These two gates used to wait on the pane citation
    # `From <child> — mcp__sprawl__messages_read(id=`, which was SPECIFIC when
    # the child's only message to weave was its ACK. This lane replaced the
    # child's deleted self-report readiness step with a `send_message` sentinel
    # to weave — and that sentinel renders the SAME citation. So both gates began
    # matching the readiness message instead of the ACK, passed instantly, and
    # the preemption timing computed from them read 0s.
    #
    # It produced a GREEN. The row was caught only by the storm gate below,
    # which counts now-priority stdin writes and correctly reported 0 for an
    # urgent send that had not happened yet.
    #
    # The general hazard in the substitution recipe: a sentinel added to satisfy
    # one probe can SATISFY A DIFFERENT PROBE that was keyed on the same
    # observable being rare. Both gates now key on the unique ACK BODY in
    # weave's maildir, which no other message in this row produces.
    #
    # Recorded reduction: the pane-citation half — "the drain row RENDERED in
    # weave's viewport" — is no longer asserted here. It is asserted directly by
    # the drain-row-inject row, whose entire subject is that citation.
    if e2e_wait_maildir_substring weave "IDLE-PROBE-ACK" 120; then
        pass "idle recipient woken via now-priority delivery (its ACK reached weave)"
    else
        fail "idle child's 'IDLE-PROBE-ACK' did NOT reach weave within 120s"
        capture_pane "$SESSION" | tail -80 >&2
        sed 's/^/    /' "$CHILD1_STATE" >&2 2>/dev/null || true
        e2e_print_results; return 1
    fi

    # ---------------------------------------------------------------------
    # PHASE 2 — mid-turn recipient (QUM-821 urgency + storm regression gate).
    # ---------------------------------------------------------------------
    echo ""
    echo "=== PHASE 2: spawn mid-turn probe child ==="
    local BUSY_SECS=40
    local P2_CHILD P2_SPAWN
    P2_CHILD="You are an automated QUM-821 mid-turn probe. STEP 1: IMMEDIATELY call mcp__sprawl__send_message with to=\"weave\" and body=\"PHASE2-PROBE-READY-${SUFFIX2}\". STEP 2: stop your turn. Whenever a system-notification about a new message arrives, call mcp__sprawl__messages_read. If the body contains \"GO-BUSY\", call the Bash tool to run exactly this foreground command: sleep ${BUSY_SECS}. If the body contains \"${NOW_PROBE}\", call mcp__sprawl__send_message with to=\"weave\" and body=\"URGENT-NOW-ACK\". Do nothing else; do not read files."
    P2_SPAWN="Call mcp__sprawl__spawn with family='engineering', type='engineer', branch='qum821-midturn-${SUFFIX2}', and prompt set to exactly the following text (do not modify it): '${P2_CHILD}'"
    e2e_send_user_prompt "$SESSION" "$P2_SPAWN"

    local CHILD2 CHILD2_NAME CHILD2_STATE
    if ! CHILD2=$(wait_for_new_child 180 weave "$CHILD1_NAME"); then
        fail "phase-2 child did not spawn within 180s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results; return 1
    fi
    CHILD2_NAME="${CHILD2%%|*}"; CHILD2_STATE="${CHILD2#*|}"
    pass "phase-2 child spawned (name=$CHILD2_NAME)"

    if e2e_wait_maildir_substring weave "PHASE2-PROBE-READY-${SUFFIX2}" 120; then
        pass "phase-2 child reached idle (its readiness message was delivered to weave)"
    else
        fail "phase-2 child's readiness message never reached weave within 120s"
        sed 's/^/    /' "$CHILD2_STATE" >&2 2>/dev/null || true
        e2e_print_results; return 1
    fi
    sleep 3

    echo ""
    echo "=== Driving $CHILD2_NAME into a long mid-turn (GO-BUSY → sleep ${BUSY_SECS}) ==="
    e2e_send_user_prompt "$SESSION" \
        "Call mcp__sprawl__send_message with to='${CHILD2_NAME}', body='GO-BUSY', and now=false. Do nothing else."
    # QUM-1186 lane 5 / QA Category-3 sweep. This was a blind `sleep 14`, and the
    # gate below then claimed the now-write landed "with the child still inside
    # its ${BUSY_SECS}s turn" — a statement about the child's state that this row
    # never checked. If the model improvised and never called Bash, the child is
    # IDLE: the now-write lands immediately, and both that gate and the ACK gate
    # after it pass with the phase's premise false. AN INSTRUCTION TO AN AGENT IS
    # NOT AN OBSERVATION OF AN AGENT — the same lesson recorded at
    # idle-reclaim-busy.sh:31-38, whose /proc precondition this copies.
    echo "  waiting for the child to be OBSERVABLY inside its sleep turn"
    local CHILD2_WORKTREE CHILD2_PID="" SLEEP_PID="" bp_elapsed=0 bp_cand bp_cwd bp_want
    CHILD2_WORKTREE=$(jq -r '.worktree // empty' "$CHILD2_STATE" 2>/dev/null || true)
    bp_want=$(readlink -f "$CHILD2_WORKTREE" 2>/dev/null || true)
    while [ "$bp_elapsed" -lt 90 ]; do
        if [ -z "$CHILD2_PID" ] && [ -n "$bp_want" ]; then
            # Resolve the child's claude PID from /proc rather than from any
            # sprawl data structure: the point is an OS-level fact, and a PID
            # read out of the thing under test would be circular.
            for bp_cand in $(pgrep -x claude 2>/dev/null || true); do
                bp_cwd=$(readlink -f "/proc/$bp_cand/cwd" 2>/dev/null || true)
                if [ -n "$bp_cwd" ] && [ "$bp_cwd" = "$bp_want" ]; then
                    CHILD2_PID="$bp_cand"
                    break
                fi
            done
        fi
        if [ -n "$CHILD2_PID" ]; then
            SLEEP_PID=$(pgrep -P "$CHILD2_PID" -f "sleep" 2>/dev/null | head -1 || true)
            if [ -z "$SLEEP_PID" ]; then
                # The CLI may run Bash under an intermediate shell, so also accept
                # a `sleep ${BUSY_SECS}` below this claude. Only consulted once the
                # child's own PID is known, so a co-tenant agent's sleep on this
                # shared host cannot satisfy the precondition on its own.
                SLEEP_PID=$(pgrep -f "sleep ${BUSY_SECS}" 2>/dev/null | head -1 || true)
            fi
        fi
        [ -n "$SLEEP_PID" ] && break
        sleep 2
        bp_elapsed=$((bp_elapsed + 2))
    done
    if [ -z "$SLEEP_PID" ]; then
        fail "no live 'sleep ${BUSY_SECS}' process appeared under $CHILD2_NAME within 90s, so the child was never observably mid-turn. PHASE 2's premise is unestablished and its assertions below would be measuring our own prompt rather than sprawl's urgency path — this is a refusal to render a verdict, not evidence that urgency is broken. (If pgrep is absent or /proc unreadable on this host, that is the cause; both are required to observe this at the OS level.)"
        pgrep -af 'sleep|claude' >&2 || true
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results; return 1
    fi
    pass "phase-2 child is OBSERVABLY mid-tool-call before the urgent send (claude PID=$CHILD2_PID, live sleep PID=$SLEEP_PID)"
    local BUSY_START=$SECONDS

    echo ""
    echo "=== Sending urgent now=true (now-priority) to mid-turn $CHILD2_NAME ==="
    e2e_send_user_prompt "$SESSION" \
        "Call mcp__sprawl__send_message with to='${CHILD2_NAME}', body='${NOW_PROBE}', and now=true. Do nothing else. Do not read files, do not run commands."
    local URGENT_SENT=$SECONDS

    echo ""
    echo "=== PHASE 2a(i): sprawl ISSUES the now-priority write while the child is mid-turn ==="
    # QUM-1186 lane 5. This replaces a gate that asserted the child's ACK arrived
    # before its ${BUSY_SECS}s sleep could end — i.e. that the turn was actually
    # PREEMPTED. Measured twice on a clean host, that outcome is BIMODAL: ACK at
    # 9s (preempted) on one run, 43s (only after the sleep ended) on the next.
    #
    # The reason is a split in who owns what. sprawl's contract is to write the
    # message to stdin at priority "now" immediately — cancel-and-replace
    # urgency, no separate interrupt frame (internal/supervisor/drain.go:162).
    # Whether the CLI can then abandon a FOREGROUND Bash tool that is already
    # running is upstream behaviour sprawl does not control, and evidently it
    # cannot do so reliably. Asserting the ACK timing therefore asserts someone
    # else's non-contract, and turns an honest run red on a coin flip — which is
    # exactly what "a floor above what a legitimately-passing path asserts" means.
    #
    # So the gate moves to the half sprawl owns and can guarantee: the
    # now-priority frame must appear in the child's wire log PROMPTLY, while the
    # turn is still in flight. If sprawl deferred the write to turn end, this
    # fails — and that is the regression QUM-821 is about.
    local NW_DEADLINE=$((SECONDS + 15))
    local NW_EARLY=0
    while [ "$SECONDS" -lt "$NW_DEADLINE" ]; do
        if [ "$(count_now_writes "$CHILD2_NAME")" -ge 1 ]; then
            NW_EARLY=1
            break
        fi
        sleep 1
    done
    # BOTH ENDS of the window. The precondition above proves the child was
    # mid-tool-call when the urgent message was sent; this proves the same sleep
    # was STILL running when the now-write was observed. Without it, "with the
    # child still inside its turn" is asserted at a moment nobody checked — the
    # narrower version of the same defect the precondition fixes.
    local NW_STILL_BUSY=0
    kill -0 "$SLEEP_PID" 2>/dev/null && NW_STILL_BUSY=1
    if [ "$NW_EARLY" -eq 1 ] && [ "$NW_STILL_BUSY" -eq 1 ]; then
        pass "sprawl issued the now-priority stdin write $((SECONDS - URGENT_SENT))s after the urgent send, with the child still inside its ${BUSY_SECS}s turn (sleep PID $SLEEP_PID still live at observation)"
    elif [ "$NW_EARLY" -eq 1 ]; then
        fail "the now-priority write landed, but the child's 'sleep ${BUSY_SECS}' had already exited by the time it was observed, so 'while the turn was in flight' is NOT established. This is a lapsed precondition — NO VERDICT on sprawl's urgency path in either direction. Re-run; if it repeats, the ${BUSY_SECS}s window is too short for this host."
    else
        fail "no now-priority write reached $CHILD2_NAME's wire log within 15s of the urgent send — sprawl deferred an urgent delivery instead of injecting it mid-turn (QUM-821)"
    fi

    echo ""
    echo "=== PHASE 2a: mid-turn child's urgent ACK reaches weave ==="
    # Unique body, for the reason recorded at the phase-1 gate above.
    if e2e_wait_maildir_substring weave "URGENT-NOW-ACK" 120; then
        local ACK_AT=$SECONDS
        pass "mid-turn recipient delivered the now-priority urgent message (ACK rendered)"
        echo "  EMPIRICAL: time-to-ACK from urgent-send=$((ACK_AT - URGENT_SENT))s, from busy-start≈$((ACK_AT - BUSY_START))s (sleep=${BUSY_SECS}s)"
        # QUM-1186 lane 5. This was an asymmetric if/else whose BOTH arms only
        # echoed — one saying "'now' PREEMPTED the in-flight turn", the other
        # "'now' reordered at the iteration boundary". It contributed 0
        # assertions and could not fail, which means THE PRIMARY
        # INTERRUPT-SEMANTICS ROW NEVER ASSERTED THAT PREEMPTION HAPPENED.
        #
        # That matters more after QUM-1186 than before. The ACK-rendered gate
        # above passes whether the message preempted the turn or merely queued
        # behind the ${BUSY_SECS}s sleep, so with the preemption arm inert the
        # row is green under both "now preempts" and "now does nothing but
        # eventually arrive" — including the specific degradation this slice
        # could have introduced, where a stale legacy urgency key left in the
        # prompt makes the agent's send fail and it silently retries without
        # urgency.
        #
        # The threshold is the same one the echo used, now load-bearing: an ACK
        # landing more than 8s before the sleep could have ended cannot be
        # explained by waiting the turn out.
        # DIAGNOSTIC, deliberately not a gate — see PHASE 2a(i) above for why.
        # Both outcomes are legitimate: sprawl's now-write is prompt either way
        # (asserted there), and whether the CLI abandons an in-flight foreground
        # tool is upstream. Measured both ways on a clean host, 9s and 43s.
        # DO NOT convert this back into a pass/fail gate without first
        # establishing that the CLI guarantees mid-tool abandonment; a gate here
        # fails on a coin flip and its red says nothing about sprawl.
        if [ "$((ACK_AT - BUSY_START))" -lt "$((BUSY_SECS - 8))" ]; then
            echo "  EMPIRICAL: ACK at $((ACK_AT - BUSY_START))s ⇒ the CLI abandoned the in-flight tool and the turn was genuinely preempted."
        else
            echo "  EMPIRICAL: ACK at $((ACK_AT - BUSY_START))s into a ${BUSY_SECS}s sleep ⇒ the CLI ran the foreground tool to completion and applied the now-write at the turn boundary. sprawl's half is still asserted above."
        fi
    else
        fail "mid-turn child's 'URGENT-NOW-ACK' did NOT reach weave within 120s"
        capture_pane "$SESSION" | tail -80 >&2
        sed 's/^/    /' "$CHILD2_STATE" >&2 2>/dev/null || true
        e2e_print_results; return 1
    fi

    echo ""
    echo "=== PHASE 2b: STORM regression gate (bounded now-priority writes) ==="
    local NW
    NW=$(count_now_writes "$CHILD2_NAME")
    echo "  now-priority stdin writes to $CHILD2_NAME = $NW (bound ${NOW_WRITE_STORM_BOUND})"
    if [ "$NW" -lt 0 ]; then
        fail "could not read $CHILD2_NAME NDJSON to count now-writes"
        e2e_print_results; return 1
    elif [ "$NW" -eq 0 ]; then
        # The urgent send was confirmed delivered above (Phase 2a ACK), so the
        # child's NDJSON must contain at least one now-priority write; zero means
        # a jq/parse problem reading the log, not a storm.
        fail "0 now-priority writes counted despite a confirmed urgent delivery — NDJSON parse/read issue"
        e2e_print_results; return 1
    elif [ "$NW" -le "$NOW_WRITE_STORM_BOUND" ]; then
        pass "now-priority delivery is bounded ($NW writes) — no re-inject storm (QUM-821)"
    else
        fail "now-priority write count $NW exceeds bound ${NOW_WRITE_STORM_BOUND} — re-inject storm regression (QUM-821)"
        e2e_print_results; return 1
    fi

    e2e_print_results
}
