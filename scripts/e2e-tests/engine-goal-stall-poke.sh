#!/usr/bin/env bash
# scripts/e2e-tests/engine-goal-stall-poke.sh — QUM-1252 (M3a, AC5): kill the
# agent working a goal, and the stall sweeper pokes it back to life.
#
#   create_goal -> spawn_requested -> a real researcher
#     -> SIGKILL its claude       -> status=died  (an OS fact, not a flag)
#     -> the session sweeper      -> goal_poke, addressed to the RESEARCHER
#
# The delivery leg (dispatchadapt.WakeInjector actually reviving the agent) is
# OBSERVED AND PRINTED HERE BUT NOT ASSERTED — see WHAT IS DELIBERATELY NOT
# ASSERTED below.
#
# ===========================================================================
# WHY THIS ROW EXISTS AT ALL, AND WHY IT IS NOT COVERED BY THE OTHER FIVE
# ===========================================================================
#
# The matrix's store rows say plainly that they do NOT cover an EFFECTIVE
# sweeper: turn state is unobservable outside a sprawl session, so a standalone
# `sprawl store dispatch` sweeper is inert by construction and those rows assert
# that it is inert. Every gate in internal/store/sweeper.go is therefore
# unreachable there, and the delivery leg — waking an OFFLINE agent through the
# supervisor — has no coverage at all outside this row.
#
# The unit suite cannot substitute. `AgentRuntime.SubprocessAlive()` is an
# in-process nil check on a handle, so a hermetic test proves the sweeper pokes
# a struct it was handed. Here the death is `kill -9` on a PID resolved from the
# agent's own session id — an OS fact, which is the part of the chain a hermetic
# test cannot reach.
#
# AND THE DEFECT IT WOULD HAVE CAUGHT. Until QUM-1252's AC5 fix, the candidate
# query measured staleness against the goal's OWNER. A goal's owner is who the
# RESULT IS REPORTED TO — weave — and weave takes turns constantly, so every
# goal it had handed to a worker read as permanently fresh and this scenario
# produced NO poke at all. That is why the poke's `target` is asserted against the researcher's name
# and separately against the owner's: a row that only asserted "a goal_poke
# exists" would have passed on the broken code the moment any goal stalled.
#
# ===========================================================================
# WHAT IS DELIBERATELY NOT ASSERTED
# ===========================================================================
#
# THE GOAL COMPLETING. The issue's AC says "goal completes"; that final step is
# a revived model choosing to call report_result, an unbounded behavioural wait,
# and folding it in would make a deterministic recovery row flake on model mood.
# The close leg has its own row (engine-goal-close-rework).
#
# THE REVIVAL ITSELF, as of weave's (ii) ruling on QUM-1252. This row originally
# asserted a NEW and DIFFERENT PID after the poke, and that assertion was
# measured at 3 of 4 runs: one run left the agent at `status=resume_failed`,
# which is QUM-1333 — the sweeper's poked set keeps `resume_failed` while
# RecoverAgents' boot accept-set excludes it, so the poke is recorded and the
# wake cannot land. That is a PRODUCT question (dmotles has an open ResumeFailed
# decision), not a harness defect, and an intermittent assertion is worse than
# no assertion: it teaches readers to re-run until green, which is precisely how
# a real regression gets absorbed as "the flaky one".
#
# So the revival is OBSERVED and PRINTED as a diagnostic and contributes ZERO to
# the assertion count. It is NOT a silently-succeeding fallback: it records
# neither a pass nor a fail of its own. It is not entirely inert either
# (QUM-1340) — its non-revival arm calls capture_pane, and a nonzero tmux status
# there writes the fault ledger, which reddens the row. That is a diagnostic
# about the HARNESS (the weave session died too), never a verdict on the
# revival. The wake leg has its own row filed as QUM-1335, blocked by QUM-1333.
#
# WHAT IS ASSERTED is the part sprawl controls end to end and that is
# deterministic across every run measured: the death is observed as an OS fact,
# the poke is emitted, and it is addressed to the right agent with the right
# owner. Only the OWNER half of that last pair has an aimed watched failure
# recorded on QUM-1252 (QUM-1340): under the original owner-sweep defect the row
# dies earlier, at the `wait_for_event goal_poke` gate, printing `FAIL: no
# goal_poke appeared` — so that mutation never reaches the target assertion. The
# target assertion is not vacuous (splitting gating from delivery would fire
# it), but it has not been watched failing, and saying otherwise overstated it.
#
# TIMING. `dispatchSweepInterval` is a hard-coded 2 minutes in
# cmd/store_dispatch.go, so a low `goal_stall.after` shortens the THRESHOLD but
# not the poll. The waits below are sized for two full sweep intervals; do not
# "optimise" them down without changing that constant.
#
# Docker-gated, and it starts and reaps its own container on a random name, the
# same shape as its sibling engine rows.

# QUM-1029: the number of assertions a COMPLETE, PASSING run of this row makes.
# Hand-counted: container ready, migrate ok, TUI rendered, create_goal turn
# completed, goal_opened present, spawn_requested present, a state file at the
# log's name, the researcher's PID resolved, SIGKILL reaped it, status=died,
# a goal_poke appeared, the poke targets the RESEARCHER, and the poke's owner is
# still weave. Thirteen. The revival block at the end of test_run is a
# DIAGNOSTIC, not an assertion — it neither passes nor fails, so it contributes
# 0 (weave's (ii) ruling; the reasoning is under WHAT IS DELIBERATELY NOT
# ASSERTED). It was 14 while that block asserted.
MIN_ASSERTIONS=13

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

PG_CONTAINER=""
PG_NAME_PREFIX="sprawl-qum1252-stall-pg-"

# reap_pg chains the harness cleanup rather than replacing it, carrying $?
# across by hand — see the long note on the same function in
# store-lifecycle-live.sh, written from a measured false-green.
reap_pg() {
    local rc=$?
    if [ -n "$PG_CONTAINER" ]; then
        docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
    fi
    ( exit "$rc" )
    _e2e_cleanup
}

reap_stale_pg() {
    local stale
    stale=$(docker ps -aq --filter "name=^${PG_NAME_PREFIX}" 2>/dev/null || true)
    if [ -n "$stale" ]; then
        echo "  reaping $(echo "$stale" | wc -l) stale container(s) from an earlier killed run"
        echo "$stale" | xargs -r docker rm -f >/dev/null 2>&1 || true
    fi
}

psql_q() {
    docker exec "$PG_CONTAINER" psql -U sprawl -d sprawl -tAc "$1" 2>&1
}

wait_for_event() {
    local type_name="$1" deadline="$2" waited=0 n=""
    while [ "$waited" -lt "$deadline" ]; do
        n=$(psql_q "SELECT count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = '$type_name';")
        if [ -n "$n" ] && [ "$n" -ge 1 ] 2>/dev/null; then
            echo "$n"
            return 0
        fi
        sleep 5
        waited=$((waited + 5))
    done
    echo "${n:-0}"
    return 1
}

# pids_for resolves an agent's claude subprocess by matching the session id in
# its state file, and then requiring the match to actually BE a claude — its
# /proc/<pid>/comm, not merely the string "claude" somewhere in its command
# line.
#
# BOTH HALVES ARE LOAD-BEARING, and the second one was learned the hard way.
# death-observability.sh's recipe greps `pgrep -af claude` and takes the FIRST
# match, which is two separate hazards here: this sandbox also runs the e2e
# driver's own shell, whose command line quotes the row's source (and therefore
# the session id) and whose snapshot path contains "/.claude/", so it matches
# both patterns while being nothing to do with the agent. Killing it reaps a
# process, satisfies "SIGKILL reaped PID", and leaves the researcher running —
# which is exactly what three of the first four runs of this row did: the TUI
# kept rendering `ghost ⚙` counting up, the disk status stayed `active`, and
# the row then failed 120s later waiting for a death that was never inflicted.
# A first-match-wins pick over an unordered pgrep is also why it was
# INTERMITTENT rather than simply broken, which cost a mutation control that
# had to be retracted (QUM-1252).
#
# So: filter on comm == claude, and emit EVERY match rather than the first.
# A session id belongs to exactly one agent, so every claude bearing it is that
# agent's, and killing all of them is both more correct and free of the
# ordering nondeterminism.
#
# It does NOT close the TOCTOU window: between the comm read and the kill a pid
# can exit and be reused, so the comm filter narrows the mis-kill risk rather
# than eliminating it. Do not read the paragraph above as if it did.
#
# The rc contract is "the session id resolved", NOT "at least one pid was
# emitted": the explicit `return 0` is there because `[ … ] && echo` as the
# loop's last command would otherwise make a run that emitted three pids
# return 1 whenever the FOURTH candidate failed the comm test. Callers that
# care about emptiness must test the output, and the `-z "$PIDS"` check at the
# kill site does exactly that.
pids_for() {
    local name="$1"
    local sid p
    sid=$(jq -r '.session_id // empty' "$SPRAWL_ROOT/.sprawl/agents/${name}.json" 2>/dev/null || true)
    [ -z "$sid" ] && return 1
    for p in $(pgrep -f -- "$sid" 2>/dev/null || true); do
        [ "$(cat "/proc/$p/comm" 2>/dev/null || true)" = "claude" ] && echo "$p"
    done
    return 0
}

status_of() {
    jq -r '.status // empty' "$SPRAWL_ROOT/.sprawl/agents/${1}.json" 2>/dev/null || true
}

test_run() {
    unset SPRAWL_AGENT_IDENTITY

    if ! command -v docker >/dev/null 2>&1; then
        e2e_skip_row "docker not found on PATH — this row needs a real Postgres for the event log"
        return
    fi
    if ! docker info >/dev/null 2>&1; then
        e2e_skip_row "docker is installed but the daemon is unreachable"
        return
    fi
    if ! command -v jq >/dev/null 2>&1; then
        e2e_skip_row "jq not found on PATH — the PID recipe reads the agent's session id from its state file"
        return
    fi
    if ! command -v pgrep >/dev/null 2>&1; then
        e2e_skip_row "pgrep not found on PATH — the PID recipe cannot resolve the researcher's claude"
        return
    fi

    e2e_recover_oauth_token
    e2e_setup_tmux_socket "sprawl-engine-stall-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum1252-stall"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps
    trap reap_pg EXIT
    reap_stale_pg

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi

    local SUFFIX
    SUFFIX=$(head -c4 /dev/urandom | xxd -p)
    PG_CONTAINER="${PG_NAME_PREFIX}${SUFFIX}"

    echo "=== Starting Postgres 16 ($PG_CONTAINER) ==="
    if ! docker run -d --name "$PG_CONTAINER" \
        -e POSTGRES_USER=sprawl -e POSTGRES_PASSWORD=sprawl -e POSTGRES_DB=sprawl \
        -P postgres:16-alpine >/dev/null 2>&1; then
        fail "could not start the Postgres container"
        return 1
    fi

    local PG_PORT=""
    PG_PORT=$(docker port "$PG_CONTAINER" 5432/tcp 2>/dev/null | head -1 | sed 's/.*://')
    if [ -z "$PG_PORT" ]; then
        fail "could not resolve the container's mapped port"
        return 1
    fi

    local READY=0 i
    for i in $(seq 1 60); do
        if docker exec "$PG_CONTAINER" pg_isready -U sprawl -d sprawl >/dev/null 2>&1; then
            READY=1
            break
        fi
        sleep 1
    done
    if [ "$READY" -eq 1 ]; then
        pass "Postgres is accepting connections on host port $PG_PORT"
    else
        fail "Postgres did not become ready within 60s"
        docker logs "$PG_CONTAINER" 2>&1 | tail -20 >&2 || true
        return 1
    fi

    local DSN="postgres://sprawl:sprawl@127.0.0.1:${PG_PORT}/sprawl?sslmode=disable"
    printf 'event_log.enabled: "true"\n' >> "$SPRAWL_ROOT/.sprawl/config.yaml"
    # 30s rather than the 30m default: the THRESHOLD is what this row shortens.
    # The sweep POLL is a hard-coded 2m (cmd/store_dispatch.go), which is what
    # the deadlines below are actually sized against.
    printf 'goal_stall.after: "30s"\n' >> "$SPRAWL_ROOT/.sprawl/config.yaml"

    echo ""
    echo "=== Applying event-log migrations ==="
    if (cd "$SPRAWL_ROOT" && SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_DB_DSN="$DSN" "$SPRAWL_BIN" store migrate); then
        pass "store migrate applied the schema"
    else
        fail "store migrate failed"
        return 1
    fi

    local SESSION="sprawl-engine-stall-$SUFFIX"
    echo ""
    echo "=== Launching sprawl enter against the live event log ==="
    if ! e2e_launch_tui "$SESSION" 200 50 "SPRAWL_DB_DSN='$DSN'"; then
        fail "the TUI did not come up with a live event log"
        return 1
    fi
    pass "TUI rendered with the event log connected"

    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION"
    sleep 2

    echo ""
    echo "=== weave opens a RESEARCH goal ==="
    e2e_send_user_prompt "$SESSION" \
        "Use the create_goal tool to open one goal with goal_type RESEARCH and the text 'Summarize what the README says this project does.'. Do not spawn anything yourself and do not do anything else."
    if wait_for_pattern "$SESSION" "Completed in" 240; then
        pass "weave completed the create_goal turn"
    else
        fail "weave did not complete the create_goal turn within 240s"
        capture_pane "$SESSION" | tail -40 >&2
        return 1
    fi

    local GOALS
    if GOALS=$(wait_for_event goal_opened 60); then
        pass "a goal_opened event is in the log ($GOALS)"
    else
        fail "no goal_opened event appeared within 60s (got '$GOALS') — nothing downstream can be measured"
        return 1
    fi

    local REQUESTS
    if REQUESTS=$(wait_for_event spawn_requested 120); then
        pass "the dispatcher turned the goal into a spawn_requested ($REQUESTS)"
    else
        fail "no spawn_requested appeared within 120s (got '$REQUESTS')"
        psql_q "SELECT s.name, count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id GROUP BY s.name;" >&2 || true
        return 1
    fi

    local LOG_NAME
    LOG_NAME=$(psql_q "SELECT e.payload->>'agent_name' FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'spawn_requested' ORDER BY e.seq LIMIT 1;")
    echo "    the log assigned the goal to: $LOG_NAME"

    local STATE_FILE="$SPRAWL_ROOT/.sprawl/agents/$LOG_NAME.json"
    local waited=0
    # ST is captured inside the loop and asserted on OUTSIDE it, rather than
    # re-read. The assertion has to require `active` for the same reason the
    # loop waits for it (see the note at the kill site), and a re-read is a
    # second observation: on the 180s timeout the old form asserted only that
    # the file existed while its pass message claimed `active`, so a state
    # still at `starting` passed here and then got SIGKILLed mid-handshake.
    local ST=""
    while [ "$waited" -lt 180 ]; do
        ST=$(status_of "$LOG_NAME")
        [ -f "$STATE_FILE" ] && [ "$ST" = "active" ] && break
        sleep 3
        waited=$((waited + 3))
    done
    if [ -n "$LOG_NAME" ] && [ -f "$STATE_FILE" ] && [ "$ST" = "active" ]; then
        pass "the researcher exists at the log's name and is active: $LOG_NAME (after ${waited}s)"
    else
        fail "no active agent at $STATE_FILE within 180s (status='$ST') — there is nothing to kill, so the rest of this row would measure nothing"
        ls -la "$SPRAWL_ROOT/.sprawl/agents" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    echo ""
    echo "=== SIGKILL the researcher's claude ==="
    # Waiting for `active` above is deliberate: killing during claude's --init
    # handshake classifies as Faulted rather than Died, which is a different
    # gate in the sweeper and would make this row measure the wrong thing.
    local PIDS=""
    waited=0
    while [ "$waited" -lt 60 ]; do
        PIDS=$(pids_for "$LOG_NAME" || true)
        [ -n "$PIDS" ] && break
        sleep 3
        waited=$((waited + 3))
    done
    if [ -n "$PIDS" ]; then
        pass "resolved the researcher's claude PID(s)=$(echo $PIDS | tr '\n' ' ')"
        # DIAGNOSTIC, not an assertion: print what is about to be killed. When
        # this row fails on the death gate below, the first question is always
        # "did it kill the right thing", and without this the answer is
        # unrecoverable after the sandbox is collected.
        for _p in $PIDS; do
            echo "    killing pid=$_p comm=$(cat "/proc/$_p/comm" 2>/dev/null) cmd=$(tr '\0' ' ' < "/proc/$_p/cmdline" 2>/dev/null | cut -c1-160)"
        done
    else
        fail "could not resolve a claude PID for $LOG_NAME — without an OS-level death this row cannot distinguish a real crash from a flag"
        pgrep -af claude >&2 || true
        e2e_print_results
        return 1
    fi
    # PID is the pre-death identity the revival check compares against. With
    # more than one match (an agent mid-resume can briefly have two), the
    # revival assertion below wants "not any of the old ones", so keep the set.
    #
    # NON-EMPTINESS IS LOAD-BEARING and is established by the `fail … return 1`
    # above, not here. `grep -qx` against an empty OLD_PIDS matches nothing, so
    # the revival assertion would accept the FIRST pid it ever sees as proof of
    # a new one — vacuous. Do not move or soften that early return.
    local OLD_PIDS="$PIDS"

    for _p in $PIDS; do kill -9 "$_p" 2>/dev/null || true; done
    local kill_end=$((SECONDS + 10))
    local still
    while [ "$SECONDS" -lt "$kill_end" ]; do
        still=""
        for _p in $PIDS; do kill -0 "$_p" 2>/dev/null && still="yes"; done
        [ -z "$still" ] && break
        sleep 0.5
    done
    still=""
    for _p in $PIDS; do kill -0 "$_p" 2>/dev/null && still="$still $_p"; done
    if [ -n "$still" ]; then
        fail "SIGKILL did not reap PID(s)$still within 10s"
    else
        pass "SIGKILL reaped the researcher's claude PID(s)"
    fi

    # Same capture-once discipline: the loop's observation IS the assertion's
    # subject. `died` is not terminal — QUM-1333 has resume_failed reachable
    # from here — so re-reading would let an onward transition fail the row
    # with a message about a state the row never gated on.
    waited=0
    ST=""
    while [ "$waited" -lt 120 ]; do
        ST=$(status_of "$LOG_NAME")
        [ "$ST" = "died" ] && break
        sleep 3
        waited=$((waited + 3))
    done
    if [ "$ST" = "died" ]; then
        pass "the researcher's disk state transitioned to status=died (after ${waited}s)"
    else
        fail "the researcher is '$ST' after 120s, want died — the sweeper's turn gate reads this, so without it the poke path is not being exercised"
        cat "$STATE_FILE" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    echo ""
    echo "=== The sweeper must notice and poke ==="
    # 300s = the 30s threshold plus two full 2-minute sweep intervals plus
    # slack. A shorter deadline measures the poll, not the sweeper.
    local POKES
    if POKES=$(wait_for_event goal_poke 300); then
        pass "the sweeper emitted a goal_poke for the stalled goal ($POKES)"
    else
        fail "no goal_poke appeared within 300s (got '$POKES') — the goal's worker is dead and its contract is open, which is precisely the state the sweeper exists for"
        psql_q "SELECT s.name, count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id GROUP BY s.name;" >&2 || true
        e2e_print_results
        return 1
    fi

    # THE TWO ASSERTIONS THIS ROW IS FOR. `target` is the agent the poke is
    # addressed to and `owner` is the agent the result is reported to; before
    # AC5 they were the same field doing both jobs, and the consequence was not
    # a mis-addressed poke but NO POKE AT ALL, because weave's own constant
    # activity made every goal it had handed to a worker look fresh. Asserting
    # both separately is what makes this row able to tell them apart.
    local POKE_TARGET POKE_OWNER
    POKE_TARGET=$(psql_q "SELECT e.payload->>'target' FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'goal_poke' ORDER BY e.seq LIMIT 1;")
    POKE_OWNER=$(psql_q "SELECT e.payload->>'owner' FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'goal_poke' ORDER BY e.seq LIMIT 1;")
    echo "    poke: target=$POKE_TARGET owner=$POKE_OWNER"
    if [ -n "$LOG_NAME" ] && [ "$POKE_TARGET" = "$LOG_NAME" ]; then
        pass "the poke is addressed to the agent doing the work ($POKE_TARGET)"
    else
        fail "the poke's target is '$POKE_TARGET', want the assignee '$LOG_NAME' — a poke delivered to the owner tells a healthy manager to get on with work it is not doing, and leaves the dead agent dead"
    fi
    if [ "$POKE_OWNER" = "weave" ]; then
        pass "the poke still records weave as the contract owner"
    else
        fail "the poke's owner is '$POKE_OWNER', want weave — the contract owner is who the result is reported to and it is not the same field as the target"
    fi

    echo ""
    echo "=== DIAGNOSTIC (NOT AN ASSERTION): did the delivery revive it? ==="
    # READ THIS BEFORE RESTORING A pass/fail HERE. This block deliberately
    # neither passes nor fails, per weave's (ii) ruling on QUM-1252; the full
    # reasoning is in the header under WHAT IS DELIBERATELY NOT ASSERTED. In
    # short: as an assertion it was measured at 3 of 4 runs, the fourth landing
    # on QUM-1333 (`resume_failed` is in the sweeper's poked set and outside
    # RecoverAgents' boot accept-set, so the wake cannot land), which is an open
    # product question rather than a harness defect. It is printed because the
    # observation is still the most useful thing in the log when the wake leg
    # misbehaves, and because a row that stopped LOOKING would make QUM-1333
    # invisible on the one host that reproduces it.
    #
    # The measure remains "a NEW and DIFFERENT PID", not "the old one is gone" —
    # the old PID is gone by construction, so the absence form would report a
    # revival with nothing revived. Keep that if this ever becomes an assertion
    # again.
    local NEWPID=""
    waited=0
    while [ "$waited" -lt 180 ]; do
        NEWPID=$(pids_for "$LOG_NAME" 2>/dev/null | head -1 || true)
        # "not any of the ones we killed" — a single-PID comparison would call a
        # surviving sibling of the killed process a revival.
        if [ -n "$NEWPID" ] && ! echo "$OLD_PIDS" | grep -qx "$NEWPID"; then
            break
        fi
        NEWPID=""
        sleep 5
        waited=$((waited + 5))
    done
    if [ -n "$NEWPID" ] && kill -0 "$NEWPID" 2>/dev/null; then
        echo "    observed: the poke revived the researcher — new PID=$NEWPID (was $(echo $OLD_PIDS | tr '\n' ' ')), after ${waited}s"
    else
        echo "    observed: NOT running again within 180s of the poke (pid='$NEWPID', status='$(status_of "$LOG_NAME")') — QUM-1333 if the status is resume_failed. NOT counted as a failure of this row."
        pgrep -af claude >&2 || true
        capture_pane "$SESSION" | tail -30 >&2
    fi

    e2e_print_results
}
