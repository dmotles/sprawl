#!/usr/bin/env bash
# scripts/e2e-tests/store-lifecycle-live.sh — QUM-1249 (M1a) AC1: `sprawl` with
# the store enabled records lifecycle events for a real spawn/retire cycle,
# queryable via psql.
#
# Stands up a REAL Postgres 16 container, points a sandbox session at it, drives a
# real spawn and retire through weave's MCP tools, and then asserts the events
# with psql inside the container — deliberately NOT through any Go code from this
# repo. That is the whole point of the AC: the log has to be readable by an
# operator with a SQL client, not merely by the process that wrote it.
#
# WHAT IS ASSERTED, and what is not:
#
#   * run_started and run_finished rows exist for the spawned child, under the
#     project resolved from the sandbox repo's remote. This is the AC.
#   * The rows are readable via psql, with a schema join, so the seeded
#     event_type_schemas rows are exercised too.
#   * open_contracts is EMPTY afterwards. In M1a nothing the lifecycle path emits
#     opens a contract, so a non-empty projection here means something opened one
#     and never closed it — the shape the sweeper reads as a stall.
#
#   * NOT asserted: agent_spawned / agent_retired as an open/close CONTRACT pair.
#     Those two seed schemas ship in M1a but are not emitted yet — closing the
#     pair needs the spawn event's id at retire time, which needs either an
#     AgentState field or a read path, and the read path is M1b. See the QUM-1249
#     issue comment recording that deviation. A green run here is not evidence
#     for the contract pair.
#
# Docker-gated. The gate is a pre-flight `docker info` plus a container that this
# row creates and reaps itself, on a random host port so concurrent agents on this
# host cannot collide.

# QUM-1029: the number of assertions a COMPLETE, PASSING run of this row makes.
# Hand-counted: container ready, migrate ok, TUI rendered, spawn turn, child
# state file, retire turn, project row, run_started present, run_finished
# present, run_finished carries provenance, open_contracts empty, no spill (the
# DB was reachable throughout).
MIN_ASSERTIONS=12

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

PG_CONTAINER=""
# Shared prefix so a container leaked by a killed run is reaped by the next one.
PG_NAME_PREFIX="sprawl-qum1249-pg-"

# reap_pg removes the container this row created, then runs the harness's own
# cleanup.
#
# CHAINED DELIBERATELY. e2e_install_cleanup_traps installs _e2e_cleanup on EXIT,
# and `trap reap_pg EXIT` REPLACES it — which would silently stop the sandbox
# from being torn down, on a shared /tmp, for every run of this row. Container
# first so `docker rm` happens while the sandbox still exists.
reap_pg() {
    # CAPTURE THE EXIT STATUS FIRST, and restore it before delegating.
    #
    # _e2e_cleanup does `local rc=$?` and ends with `exit "$rc"` — it re-exits
    # with whatever status it observes. `docker rm ... || true` succeeds, so
    # without the save/restore that 0 becomes the row's exit status and a FAILING
    # ROW REPORTS PASS. Measured, not theorised: a control that forced an early
    # `fail` + `return 1` printed "FAIL: CONTROL..." and the driver still
    # reported "1 passed, 0 failed" with exit 0.
    #
    # `( exit "$rc" )` is the idiom for putting a specific status back into $?
    # for the next command to read.
    local rc=$?
    if [ -n "$PG_CONTAINER" ]; then
        docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
    fi
    ( exit "$rc" )
    _e2e_cleanup
}

# reap_stale_pg removes containers left by a previous run that was killed before
# its trap could fire. A trap cannot cover SIGKILL, so leak recovery has to be
# somebody's job at START rather than only at exit.
reap_stale_pg() {
    local stale
    stale=$(docker ps -aq --filter "name=^${PG_NAME_PREFIX}" 2>/dev/null || true)
    if [ -n "$stale" ]; then
        echo "  reaping $(echo "$stale" | wc -l) stale container(s) from an earlier killed run"
        echo "$stale" | xargs -r docker rm -f >/dev/null 2>&1 || true
    fi
}

# psql_q runs a query inside the container and echoes the tuple-only result.
psql_q() {
    docker exec "$PG_CONTAINER" psql -U sprawl -d sprawl -tAc "$1" 2>&1
}

test_run() {
    unset SPRAWL_AGENT_IDENTITY

    if ! command -v docker >/dev/null 2>&1; then
        e2e_skip_row "docker not found on PATH — this row needs a real Postgres to query with psql"
        return
    fi
    if ! docker info >/dev/null 2>&1; then
        e2e_skip_row "docker is installed but the daemon is unreachable"
        return
    fi

    e2e_recover_oauth_token
    e2e_setup_tmux_socket "sprawl-store-lifecycle-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum1249-lifecycle"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps
    # Replaces the harness EXIT trap with one that reaps the container AND calls
    # the harness cleanup. See reap_pg.
    trap reap_pg EXIT
    reap_stale_pg

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi

    # Random name AND random host port: sibling agents run this host
    # concurrently, and a fixed port is a collision waiting to happen.
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

    # Wait for readiness rather than sleeping: a fixed sleep either flakes or
    # wastes time, and pg_isready is the container's own answer.
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

    # Enable the store. The DSN goes in the environment, never in the tracked
    # project config.
    printf 'event_log.enabled: "true"\n' >> "$SPRAWL_ROOT/.sprawl/config.yaml"

    echo ""
    echo "=== Applying event-log migrations ==="
    if (cd "$SPRAWL_ROOT" && SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_DB_DSN="$DSN" "$SPRAWL_BIN" store migrate); then
        pass "store migrate applied the schema"
    else
        fail "store migrate failed"
        return 1
    fi

    local SESSION="sprawl-store-lifecycle-$SUFFIX"
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
    echo "=== Driving a real spawn ==="
    e2e_send_user_prompt "$SESSION" \
        "Use the spawn tool to create one engineer agent with the prompt 'echo hello then stop'. Do not do anything else."
    if wait_for_pattern "$SESSION" "Completed in" 180; then
        pass "weave completed the spawn turn"
    else
        fail "weave did not complete the spawn turn within 180s"
        capture_pane "$SESSION" | tail -40 >&2
        return 1
    fi

    # Read the child's name off disk rather than off the pane: the state file is
    # written by spawn itself, so it cannot report a child that does not exist,
    # whereas the pane renders text weave chose.
    local CHILD=""
    local j
    for j in $(seq 1 30); do
        CHILD=$(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' -type f 2>/dev/null \
            | xargs -r -n1 basename 2>/dev/null | sed 's/\.json$//' | grep -v '^weave$' | head -1)
        [ -n "$CHILD" ] && break
        sleep 1
    done
    if [ -n "$CHILD" ]; then
        pass "a child agent was spawned: $CHILD"
    else
        fail "no child agent state file appeared under .sprawl/agents"
        ls -la "$SPRAWL_ROOT/.sprawl/agents" >&2 2>/dev/null || true
        return 1
    fi

    # Let the child's runtime come up and emit run_started.
    sleep 10

    echo ""
    echo "=== Driving a real retire ==="
    # Asserted on the CHILD'S STATE FILE DISAPPEARING, not on the pane.
    #
    # Three reasons, and the first two are corrections of earlier attempts:
    #
    #   1. `wait_for_pattern "Completed in"` is unfalsifiable here — it greps the
    #      current pane with no anchor, so it matches the SPAWN turn's leftover
    #      text and returns on the first poll.
    #   2. Counting "Completed in" occurrences does not work either: it is a
    #      status-bar label showing one value at a time, so the count never
    #      exceeds 1. (Measured on the sibling row: "completions stuck at 1".)
    #   3. The state file is minted by `retire` itself, its removal is monotonic,
    #      and it asserts the thing this row actually needs — that the retire
    #      HAPPENED — rather than that a turn ended. A turn that ended without
    #      retiring anything would satisfy a pane grep and not this.
    local STATE_FILE="$SPRAWL_ROOT/.sprawl/agents/$CHILD.json"
    if [ ! -f "$STATE_FILE" ]; then
        fail "the child's state file is already gone before the retire prompt, so its removal cannot evidence the retire"
        return 1
    fi
    e2e_send_user_prompt "$SESSION" \
        "Use the retire tool to retire the agent named $CHILD with abandon=true. Do not do anything else."

    local RETIRE_WAITED=0
    while [ "$RETIRE_WAITED" -lt 180 ]; do
        [ -f "$STATE_FILE" ] || break
        sleep 3
        RETIRE_WAITED=$((RETIRE_WAITED + 3))
    done
    if [ ! -f "$STATE_FILE" ]; then
        pass "the child was really retired (its state file was removed after ${RETIRE_WAITED}s)"
    else
        fail "the child's state file still exists after 180s, so the retire did not complete"
        capture_pane "$SESSION" | tail -40 >&2
    fi

    # Let the subscriber drain and write run_finished.
    sleep 8

    echo ""
    echo "=== Querying the event log with psql (no Go code from this repo) ==="
    local PROJECTS
    PROJECTS=$(psql_q "SELECT count(*) FROM projects;")
    echo "    projects: $PROJECTS"
    if [ "$PROJECTS" = "1" ]; then
        pass "exactly one project row was registered"
    else
        fail "expected 1 project row, got '$PROJECTS'"
    fi

    # Joined against event_type_schemas so the seeded definitions are exercised
    # too: a schema_id that resolved in Go but was never seeded would show up
    # here as a zero count rather than as a working query.
    local RUN_STARTED
    RUN_STARTED=$(psql_q "SELECT count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'run_started';")
    echo "    run_started: $RUN_STARTED"
    if [ -n "$RUN_STARTED" ] && [ "$RUN_STARTED" -ge 1 ] 2>/dev/null; then
        pass "run_started events are present and queryable via psql ($RUN_STARTED)"
    else
        fail "no run_started events in the log (got '$RUN_STARTED')"
        psql_q "SELECT s.name, count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id GROUP BY s.name;" >&2 || true
    fi

    local RUN_FINISHED
    RUN_FINISHED=$(psql_q "SELECT count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'run_finished';")
    echo "    run_finished: $RUN_FINISHED"
    if [ -n "$RUN_FINISHED" ] && [ "$RUN_FINISHED" -ge 1 ] 2>/dev/null; then
        pass "run_finished events are present and queryable via psql ($RUN_FINISHED)"
    else
        fail "no run_finished events in the log (got '$RUN_FINISHED') — the retire path did not drain the ledger subscriber"
        psql_q "SELECT s.name, count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id GROUP BY s.name;" >&2 || true
    fi

    # Provenance: a run_finished with no git_sha is a row that records the run
    # happened without recording what it ran against, which is most of the value.
    local WITH_SHA
    WITH_SHA=$(psql_q "SELECT count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'run_finished' AND e.payload ? 'git_sha' AND e.payload->>'git_sha' <> '';")
    echo "    run_finished carrying git_sha: $WITH_SHA"
    if [ -n "$WITH_SHA" ] && [ "$WITH_SHA" -ge 1 ] 2>/dev/null; then
        pass "run_finished carries git provenance"
    else
        fail "no run_finished row carries a non-empty git_sha (got '$WITH_SHA')"
    fi

    # M1a's lifecycle path opens no contracts, so a non-empty projection here
    # means something opened one and never closed it.
    local OPEN_CONTRACTS
    OPEN_CONTRACTS=$(psql_q "SELECT count(*) FROM open_contracts;")
    echo "    open_contracts: $OPEN_CONTRACTS"
    if [ "$OPEN_CONTRACTS" = "0" ]; then
        pass "open_contracts is empty (nothing M1a emits opens a contract)"
    else
        fail "open_contracts holds $OPEN_CONTRACTS row(s); an unclosed contract is what the sweeper reads as a stall"
        psql_q "SELECT * FROM open_contracts;" >&2 || true
    fi

    # The negative control for the whole row: with the database reachable
    # throughout, NOTHING should have spilled. A populated spill directory here
    # would mean the events above arrived by a path this row is not testing.
    local SPILL_DIR="$SPRAWL_ROOT/.sprawl/logs/ledger-spill"
    local SPILLED=0
    if [ -d "$SPILL_DIR" ]; then
        SPILLED=$(find "$SPILL_DIR" -maxdepth 1 -name '*.ndjson' -type f 2>/dev/null | wc -l)
    fi
    if [ "$SPILLED" -eq 0 ]; then
        pass "nothing spilled (the database was reachable throughout, so the events above came from the live path)"
    else
        fail "$SPILLED spill file(s) exist despite a reachable database — some events took the degraded path, so the psql assertions above may be measuring a partial log"
        ls -la "$SPILL_DIR" >&2 || true
    fi

    e2e_print_results
}
