#!/usr/bin/env bash
# scripts/e2e-tests/engine-goal-close-rework.sh — QUM-1252 (M3a): the engine's
# CLOSE leg, and the REWORK that follows a result the owner rejects.
#
# The sibling row `engine-goal-roundtrip` deliberately stops once the goal has
# STARTED, and its header says in as many words that a green run there "is
# evidence the goal STARTED, and is not evidence it can finish." This row is
# the other half:
#
#   report_result -> goal_closed  ->  (dispatcher)  owner_notify
#                 -> request_rework -> rework_requested (follows_event_id)
#                 ->  (dispatcher)  a SECOND spawn_requested, fresh agent
#
# ===========================================================================
# WHY IT CANNOT BE A GO TEST
# ===========================================================================
#
# The Postgres integration suite in internal/store drives RequestRework and the
# ReworkHandler directly, with a handler table the test itself builds. That
# proves the mechanism; it cannot prove the WIRING, because the production
# handler table is assembled in cmd/store_dispatch.go and nothing under
# `go test` ever reads it. A rework handler registered for the wrong schema, or
# omitted from the session dispatch path, is green in that suite and broken
# here — the same class of hole the sibling row exists for, one schema over.
#
# It also cannot prove that a real agent, holding a real card, reads the rework
# prompt as a task. `openGoalsForAgentSQL` treating `rework_requested` as
# goal-shaped is asserted at the Go level; that the agent this row spawns is
# actually handed that contract is not.
#
# ===========================================================================
# THE BEHAVIOURAL WAIT, STATED PLAINLY
# ===========================================================================
#
# Two legs of this row wait on a MODEL choosing to call a tool: the researcher
# calling report_result, and weave calling request_rework. That is why the
# sibling row excluded the close leg. It is admitted here rather than avoided,
# because the alternative — appending goal_closed by hand — would skip the only
# part of the close leg that has ever been wrong (the agent finding its own
# contract), and would leave AC2 and AC3 with no live coverage at all.
#
# The mitigations are: a trivially completable goal (summarize a two-line
# README), an explicit instruction in the prompt, and generous deadlines. The
# residual risk is a row that fails for model mood rather than for a defect.
# **A failure on the report_result or request_rework wait is therefore not
# automatically a product regression** — read the pane dump the failure prints
# before blaming the diff (see /false-red).
#
# ===========================================================================
# WHAT IS AND IS NOT ASSERTED
# ===========================================================================
#
# ASSERTED: the goal_closed lands and CLOSES the contract (open_contracts drops
# to zero for goal_opened); the owner is notified; the rework opens a new
# contract carrying follows_event_id back to the closed goal, on the SAME
# workflow instance; and the dispatcher turns it into a second spawn_requested
# naming a DIFFERENT agent — which is the whole observable difference between
# discard_and_redo and doing nothing.
#
# NOT ASSERTED: that the reworking agent closes the rework contract. That would
# be a second full behavioural wait on top of two, and it is covered against a
# real Postgres by TestReworkPg_TheReworkingAgentSeesAndClosesTheReworkContract.
# This row stops where the sibling row stops, one contract later.
#
# NOT ASSERTED, because the log cannot answer it: WHO closed the goal. This row
# was written with an attribution assertion and it failed on the first run
# ("the close names '' but the goal was given to 'ghost'") — CloseGoalForAgent
# authorizes with the agent's name and then drops it, and goal_closed has no
# field to hold it. Filed as QUM-1330; not fixed here, since it is a seed change
# outside this row's slice. The assertion in its place is closes_event_id.
#
# NOT ASSERTED: re_engage_original. It has no live path to exercise — but be
# precise about WHY, because the loose version of this sentence ("refused at
# both layers") was wrong and is corrected here per QUM-1338: it is REFUSED at
# one layer (store.RequestRework, and again in ReworkHandler) and merely
# UNREACHABLE at the other (sprawlmcp's rework tool hardcodes discard_and_redo
# and has no re_engagement parameter to supply anything else). The effect is the
# same today; the mechanism is not, and a reader who believes both layers refuse
# it will not notice when the tool grows the parameter.
#
# CONTROL. The rework leg's control was run and FIRED: removing
# `"rework_requested": rework` from dispatchHandlerSet in cmd/store_dispatch.go
# and rebuilding fails this row at "no second spawn_requested appeared within
# 180s (got '1')" while every assertion before it still passes — precisely the
# quiet failure the row exists for, since the rework event IS in the log and
# looks correct. The close leg's control is structural and is stated at its
# assertion: the sibling row asserts the contract is still OPEN at the same
# point in the chain, so the two rows are each other's controls on that value.
#
# Docker-gated, same shape as the sibling rows: a container this row creates and
# reaps itself, on a random name and a random host port, so concurrent agents on
# one host do not collide.
#
# QUM-1029: the number of assertions a COMPLETE, PASSING run of this row makes.
# Hand-counted, in order: container ready, migrate ok, TUI rendered, create_goal
# turn completed, goal_opened present, spawn_requested present, the researcher's
# state file exists at the log's name, goal_closed present, the file-backed result
# reached the log as an artifact over the payload cap (QUM-1347), the payload
# carries the text rather than the path (QUM-1347), the close discharges
# the goal's own contract, the goal contract is closed, the owner was notified,
# request_rework turn completed, rework_requested present, the rework follows
# the closed goal, the rework is on the goal's instance, a rework contract is
# open, `sprawl goals` lists it (QUM-1336), a second spawn_requested exists, it
# names a different agent, and nothing spilled.
#
# 19 before QUM-1336 added the `sprawl goals` assertion; 20 before QUM-1347
# added the two file-backed-result assertions.
MIN_ASSERTIONS=22

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

PG_CONTAINER=""
PG_NAME_PREFIX="sprawl-qum1252r-pg-"

# reap_pg removes this row's container and then runs the harness cleanup.
# CHAINED, NOT REPLACED, and the exit status is carried across by hand: an
# unguarded `docker rm ... || true` here turns a FAILING row into a passing one,
# because _e2e_cleanup re-exits with whatever $? it observes. Measured on
# store-lifecycle-live.sh, which carries the long-form note.
reap_pg() {
    local rc=$?
    if [ -n "$PG_CONTAINER" ]; then
        docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
    fi
    ( exit "$rc" )
    _e2e_cleanup
}

# A trap cannot cover SIGKILL, so leak recovery has to happen at START too.
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

# wait_for_count polls until at least $3 events of the named type exist, and
# echoes the final count. Polling rather than sleeping because every leg of this
# chain is asynchronous: the dispatcher polls every 2s, but the turn in front of
# it is a model.
wait_for_count() {
    local type_name="$1" deadline="$2" want="$3" waited=0 n=""
    while [ "$waited" -lt "$deadline" ]; do
        n=$(psql_q "SELECT count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = '$type_name';")
        if [ -n "$n" ] && [ "$n" -ge "$want" ] 2>/dev/null; then
            echo "$n"
            return 0
        fi
        sleep 3
        waited=$((waited + 3))
    done
    echo "${n:-0}"
    return 1
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

    e2e_recover_oauth_token
    e2e_setup_tmux_socket "sprawl-engine-rework-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum1252-rework"
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

    # A two-line README, so "summarize the README" is a task a researcher can
    # finish in one turn. The behavioural wait is the main flake source in this
    # row and this is the cheapest lever on it.
    printf 'sandbox-project\n\nA scratch repo used by an e2e row. It does nothing.\n' \
        > "$SPRAWL_ROOT/README.md"

    echo ""
    echo "=== Applying event-log migrations ==="
    if (cd "$SPRAWL_ROOT" && SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_DB_DSN="$DSN" "$SPRAWL_BIN" store migrate); then
        pass "store migrate applied the schema"
    else
        fail "store migrate failed"
        return 1
    fi

    local SESSION="sprawl-engine-rework-$SUFFIX"
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
        "Use the create_goal tool to open one goal with goal_type RESEARCH and the text 'Read README.md and summarize in one sentence what this project is. Then build a long report file in your worktree by running: { for i in 1 2 3 4 5; do cat README.md; done; echo YOUR_SUMMARY; } > result.md  — replacing YOUR_SUMMARY with your one-sentence summary. Then call report_result with summary_file set to result.md, and do NOT pass summary.'. Do not spawn anything yourself and do not do anything else."
    if wait_for_pattern "$SESSION" "Completed in" 240; then
        pass "weave completed the create_goal turn"
    else
        fail "weave did not complete the create_goal turn within 240s"
        capture_pane "$SESSION" | tail -40 >&2
        return 1
    fi

    local GOALS
    if GOALS=$(wait_for_count goal_opened 60 1); then
        pass "a goal_opened event is in the log ($GOALS)"
    else
        fail "no goal_opened event appeared within 60s (got '$GOALS') — create_goal did not append, so nothing downstream can be measured"
        psql_q "SELECT s.name, count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id GROUP BY s.name;" >&2 || true
        return 1
    fi

    local REQUESTS
    if REQUESTS=$(wait_for_count spawn_requested 120 1); then
        pass "the dispatcher turned the goal into a spawn_requested ($REQUESTS)"
    else
        fail "no spawn_requested event appeared within 120s (got '$REQUESTS') — the start leg is broken, so nothing this row exists to measure can run"
        psql_q "SELECT s.name, count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id GROUP BY s.name;" >&2 || true
        return 1
    fi

    local FIRST_NAME
    FIRST_NAME=$(psql_q "SELECT e.payload->>'agent_name' FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'spawn_requested' ORDER BY e.seq LIMIT 1;")
    echo "    the log spawned: $FIRST_NAME"

    local STATE_FILE="$SPRAWL_ROOT/.sprawl/agents/$FIRST_NAME.json"
    local waited=0
    while [ "$waited" -lt 120 ]; do
        [ -f "$STATE_FILE" ] && break
        sleep 3
        waited=$((waited + 3))
    done
    if [ -n "$FIRST_NAME" ] && [ -f "$STATE_FILE" ]; then
        pass "the researcher exists at the log's name: $FIRST_NAME (after ${waited}s)"
    else
        fail "no state file at $STATE_FILE — the researcher never started, so the close leg cannot be measured"
        ls -la "$SPRAWL_ROOT/.sprawl/agents" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    echo ""
    echo "=== THE CLOSE LEG: the researcher reports, and the contract closes ==="
    # 900s: this waits on a spawned agent booting, reading a file, and choosing
    # to call a tool. See the header's note on behavioural waits before reading
    # a timeout here as a regression.
    local CLOSES
    if CLOSES=$(wait_for_count goal_closed 900 1); then
        pass "the researcher's report_result appended a goal_closed ($CLOSES)"
    else
        fail "no goal_closed appeared within 900s (got '$CLOSES') — either the researcher never reported, or it reported and report_result refused because it could not find its own contract, which is the silent shape: the agent believes it finished and the goal stays open forever"
        psql_q "SELECT s.name, count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id GROUP BY s.name;" >&2 || true
        psql_q "SELECT * FROM open_contracts;" >&2 || true
        capture_pane "$SESSION" | tail -30 >&2
        e2e_print_results
        return 1
    fi

    # THE FILE-BACKED RESULT (QUM-1347). The researcher was told to write its
    # report to a file and pass `summary_file`, and the file is deliberately
    # larger than the 8KiB events_payload_thin_ck budget. Two things can only be
    # observed here: that the tool read the file at all (a path stored instead
    # of its content would leave no artifact), and that a result too big for a
    # payload still lands instead of being refused by the CHECK. The unit suite
    # cannot see either — it has no CHECK and no real agent.
    local SUMMARY_ARTIFACT_BYTES
    SUMMARY_ARTIFACT_BYTES=$(psql_q "SELECT COALESCE(octet_length(a.content), 0) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id LEFT JOIN artifacts a ON a.id = e.artifact_id WHERE s.name = 'goal_closed' ORDER BY e.seq LIMIT 1;")
    echo "    the close's artifact holds: ${SUMMARY_ARTIFACT_BYTES} bytes"
    if [ "${SUMMARY_ARTIFACT_BYTES:-0}" -gt 8192 ]; then
        pass "the file's CONTENT (${SUMMARY_ARTIFACT_BYTES} bytes, over the 8KiB payload cap) reached the log as an artifact"
    else
        fail "the close references ${SUMMARY_ARTIFACT_BYTES:-0} artifact bytes — a file-backed result over the payload cap must be stored whole; either report_result kept the path instead of the content, or the researcher ignored summary_file"
        psql_q "SELECT left(e.payload->>'summary', 200), e.artifact_id FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'goal_closed' ORDER BY e.seq LIMIT 1;" >&2 || true
    fi

    # The inline remnant is not the path: a tool that stored `result.md` would
    # satisfy nothing above but would still look like a summary here.
    local SUMMARY_INLINE
    SUMMARY_INLINE=$(psql_q "SELECT left(COALESCE(e.payload->>'summary', ''), 200) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'goal_closed' ORDER BY e.seq LIMIT 1;")
    if [ -n "$SUMMARY_INLINE" ] && [ "$SUMMARY_INLINE" != "result.md" ]; then
        pass "the payload carries the result's text, not the path it came from"
    else
        fail "the close's summary payload is '$SUMMARY_INLINE' — the PATH was persisted instead of the content, so the log's record of this goal dies with the worktree"
    fi

    # WHAT THIS IS *NOT*. It was written first as an ATTRIBUTION assertion — the
    # close must name the agent that was spawned for the goal — and it failed on
    # the first run with "the close names '' but the goal was given to 'ghost'".
    # That is a real gap, not a row bug: CloseGoalForAgent takes the agent's name,
    # uses it to authorize the close, and then drops it, and the goal_closed seed
    # has no field to put it in. Filed as QUM-1330; deliberately not fixed here,
    # since it is a seed change outside this row's slice.
    #
    # So this asserts the property the design DOES guarantee: the close names the
    # goal as the contract it discharges. Without it a close can land, look
    # correct, and discharge some OTHER contract — which the count below cannot
    # distinguish from the right one once more than one contract is in play, and
    # a rework chain always puts more than one in play.
    local CLOSED_TARGET
    CLOSED_TARGET=$(psql_q "SELECT COALESCE(e.closes_event_id::text, '') FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'goal_closed' ORDER BY e.seq LIMIT 1;")
    local GOAL_EVENT_ID
    GOAL_EVENT_ID=$(psql_q "SELECT e.id FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'goal_opened' ORDER BY e.seq LIMIT 1;")
    echo "    the close discharges: $CLOSED_TARGET (the goal is $GOAL_EVENT_ID)"
    if [ -n "$GOAL_EVENT_ID" ] && [ "$CLOSED_TARGET" = "$GOAL_EVENT_ID" ]; then
        pass "the close names the goal as the contract it discharges"
    else
        fail "the close's closes_event_id is '$CLOSED_TARGET' but the goal is '$GOAL_EVENT_ID' — the appender deletes from open_contracts BY closes_event_id, so a wrong value here discharges a contract nobody reported on and leaves this one open forever"
    fi

    # The sibling row asserts this same count is >= 1 at its own end. The two
    # rows are therefore each other's control on this value: if this assertion
    # could not distinguish an open contract from a closed one, one of the two
    # rows would be green against the wrong state.
    local OPEN_GOALS
    OPEN_GOALS=$(psql_q "SELECT count(*) FROM open_contracts oc JOIN events e ON e.id = oc.event_id JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'goal_opened';")
    echo "    open goal_opened contracts: $OPEN_GOALS"
    if [ "$OPEN_GOALS" = "0" ]; then
        pass "the goal contract is discharged — the close deleted it from open_contracts"
    else
        fail "$OPEN_GOALS goal_opened contract(s) still open after a goal_closed — the event landed but the projection did not follow it, so 'sprawl goals' will report finished work as outstanding forever"
        psql_q "SELECT * FROM open_contracts;" >&2 || true
    fi

    local NOTIFIES
    if NOTIFIES=$(wait_for_count owner_notify 180 1); then
        pass "the owner was notified of the result ($NOTIFIES)"
    else
        fail "no owner_notify appeared within 180s (got '$NOTIFIES') — the goal closed and the owner was never told, which looks identical to work still in progress"
        psql_q "SELECT s.name, count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id GROUP BY s.name;" >&2 || true
    fi

    echo ""
    echo "=== THE REWORK LEG: weave rejects the result ==="
    echo "    rejecting goal event: $GOAL_EVENT_ID"

    e2e_send_user_prompt "$SESSION" \
        "Use the request_rework tool with goal_event_id $GOAL_EVENT_ID and reason 'the summary does not say who the project is for'. Do not spawn anything yourself and do not do anything else."
    if wait_for_pattern "$SESSION" "Completed in" 240; then
        pass "weave completed the request_rework turn"
    else
        fail "weave did not complete the request_rework turn within 240s"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    local REWORKS
    if REWORKS=$(wait_for_count rework_requested 60 1); then
        pass "a rework_requested is in the log ($REWORKS)"
    else
        fail "no rework_requested appeared within 60s (got '$REWORKS') — the owner rejected a result and the log does not know it"
        capture_pane "$SESSION" | tail -30 >&2
        e2e_print_results
        return 1
    fi

    # The link is the COLUMN, not a payload field. Read it back from Postgres so
    # the assertion is about what the database stored, not about what the writer
    # believed it wrote.
    local FOLLOWS INSTANCE_MATCH
    FOLLOWS=$(psql_q "SELECT COALESCE(e.follows_event_id::text, '') FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'rework_requested' ORDER BY e.seq LIMIT 1;")
    echo "    the rework follows: $FOLLOWS"
    if [ -n "$GOAL_EVENT_ID" ] && [ "$FOLLOWS" = "$GOAL_EVENT_ID" ]; then
        pass "the rework is linked to the rejected goal by follows_event_id"
    else
        fail "the rework's follows_event_id is '$FOLLOWS', want the rejected goal '$GOAL_EVENT_ID' — an unlinked rework is an orphan contract: nothing can say what it is redoing, and the handler cannot recover the original task"
    fi

    INSTANCE_MATCH=$(psql_q "SELECT count(*) FROM events r JOIN event_type_schemas rs ON rs.id = r.schema_id JOIN events g ON g.id = r.follows_event_id WHERE rs.name = 'rework_requested' AND r.workflow_instance_id = g.workflow_instance_id;")
    if [ "$INSTANCE_MATCH" -ge 1 ] 2>/dev/null; then
        pass "the rework landed on the goal's own workflow instance ($INSTANCE_MATCH)"
    else
        fail "the rework is on a different workflow instance than the goal it follows (got '$INSTANCE_MATCH') — the rework would be invisible in the goal's log, and 'sprawl workflows' would show two unrelated instances for one piece of work"
    fi

    local OPEN_REWORKS
    OPEN_REWORKS=$(psql_q "SELECT count(*) FROM open_contracts oc JOIN events e ON e.id = oc.event_id JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'rework_requested';")
    echo "    open rework contracts: $OPEN_REWORKS"
    if [ "$OPEN_REWORKS" -ge 1 ] 2>/dev/null; then
        pass "the rework opened a contract of its own ($OPEN_REWORKS)"
    else
        fail "no rework contract is open (got '$OPEN_REWORKS') — without one the rework is telemetry: nothing reports the work as outstanding and nothing demands a close"
        psql_q "SELECT * FROM open_contracts;" >&2 || true
    fi

    # THE OPERATOR CAN SEE IT (QUM-1336). The assertion above proves the
    # contract is open in the PROJECTION; this one proves the operator's own
    # command reports it. They came apart: `sprawl goals` enumerated goal_opened
    # and agent_spawned only, so this exact state — a rework outstanding, an
    # agent working it — printed "no goals are outstanding." plus the note that
    # invites the operator to walk away. A row that only reads Postgres cannot
    # see that, because the projection was right the whole time.
    local GOALS_OUT
    GOALS_OUT=$(cd "$SPRAWL_ROOT" && SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_DB_DSN="$DSN" "$SPRAWL_BIN" goals 2>&1) || true
    echo "    sprawl goals said:"
    printf '%s\n' "$GOALS_OUT" | sed 's/^/      /'
    if printf '%s' "$GOALS_OUT" | grep -q "outstanding, oldest first" &&
       printf '%s' "$GOALS_OUT" | grep -q "type:.*rework"; then
        pass "sprawl goals lists the outstanding rework"
    else
        fail "sprawl goals did not list the outstanding rework — an operator asking what the fleet still owes is told nothing is, while an agent is mid-rework"
    fi

    # The whole observable difference between discard_and_redo and doing
    # nothing. This is the assertion whose control was run and fired.
    local SECOND
    if SECOND=$(wait_for_count spawn_requested 180 2); then
        pass "the dispatcher turned the rework into a second spawn_requested ($SECOND)"
    else
        fail "no second spawn_requested appeared within 180s (got '$SECOND') — the rework_requested is in the log and looks correct, but nothing consumed it, so the owner's rejection produced no work. This is the row's reason for existing: the event is scanned, skipped, and the cursor advances."
        psql_q "SELECT s.name, count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id GROUP BY s.name;" >&2 || true
        e2e_print_results
        return 1
    fi

    local SECOND_NAME
    SECOND_NAME=$(psql_q "SELECT e.payload->>'agent_name' FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'spawn_requested' ORDER BY e.seq OFFSET 1 LIMIT 1;")
    echo "    the rework was handed to: $SECOND_NAME"
    if [ -n "$SECOND_NAME" ] && [ "$SECOND_NAME" != "$FIRST_NAME" ]; then
        pass "the rework went to a fresh agent, not the one whose result was rejected ($SECOND_NAME)"
    else
        fail "the rework was handed to '$SECOND_NAME' and the rejected result came from '$FIRST_NAME' — discard_and_redo means a fresh agent; re-engaging the original is a policy this build REFUSES, so seeing it here means the refusal was bypassed"
    fi

    # Negative control for the whole row: with the database reachable
    # throughout, nothing should have spilled. A populated spill directory means
    # some of the events asserted above arrived by the degraded path, so the
    # chain this row claims to have measured is partly unmeasured.
    local SPILL_DIR="$SPRAWL_ROOT/.sprawl/logs/ledger-spill"
    local SPILLED=0
    if [ -d "$SPILL_DIR" ]; then
        SPILLED=$(find "$SPILL_DIR" -maxdepth 1 -name '*.ndjson' -type f 2>/dev/null | wc -l)
    fi
    if [ "$SPILLED" -eq 0 ]; then
        pass "nothing spilled (the database was reachable throughout, so the chain above came from the live path)"
    else
        fail "$SPILLED spill file(s) exist despite a reachable database — some events took the degraded path, so the assertions above may be measuring a partial log"
        ls -la "$SPILL_DIR" >&2 || true
    fi

    e2e_print_results
}
