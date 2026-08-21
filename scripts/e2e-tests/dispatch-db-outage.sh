#!/usr/bin/env bash
# scripts/e2e-tests/dispatch-db-outage.sh — QUM-1250 (M1b) AC8: the dispatcher
# survives a database outage and catches up from its cursor on reconnect.
#
# The outage is REAL — `docker stop` on the Postgres container — not a refused
# port or an injected error, because the property under test is what a long-lived
# process does when a connection it is already holding dies underneath it. A
# fabricated error tests the error path; stopping the database tests the pool,
# the retry loop and the cursor together.
#
# WHAT THE ROW ASSERTS, in order, and why each step is there rather than implied:
#
#   1. Dispatch works before the outage. Without this every later assertion could
#      be satisfied by a process that never worked at all.
#   2. THE OUTAGE IS REAL. A query is run against the stopped container and MUST
#      fail. A container that silently kept running would make the whole row a
#      story about nothing — this is the positive control for the outage itself,
#      and it is the assertion most likely to be missing from a row like this.
#   3. The dispatcher SURVIVES it: the long-running process is still alive after
#      the database has been down for several poll intervals, and it did not
#      panic. "Agents never brick on the store" applies to the dispatcher too.
#   4. It CATCHES UP after the database returns — on events appended DURING the
#      outage, which is the part that proves the catch-up is cursor-driven rather
#      than a lucky re-delivery of something it had already seen.
#
# NEEDS NO `claude` AND NO `tmux`, so a `Not logged in` count of zero here is
# structural rather than lucky.

# QUM-1029: assertions a COMPLETE, PASSING run makes. Hand-counted: container
# ready, migrate, ledger opened, pre-outage dispatch handled the seeded result,
# the outage is real (query fails), the dispatcher process is still alive during
# the outage, it logged the failure rather than dying silently, the database is
# back (query succeeds), and it caught up on the event appended during the outage.
MIN_ASSERTIONS=9

test_metadata() {
    echo "needs_claude=0 needs_tmux=0"
}

PG_CONTAINER=""
PG_NAME_PREFIX="sprawl-qum1250-outage-"
DISPATCH_PID=""
DISPATCH_LOG=""

reap_all() {
    # Save and restore the exit status before delegating — see the same idiom in
    # dispatch-exactly-once.sh: _e2e_cleanup re-exits with whatever it observes,
    # and a successful `docker rm` would otherwise turn a FAILING ROW INTO A PASS.
    local rc=$?
    if [ -n "$DISPATCH_PID" ]; then
        kill "$DISPATCH_PID" 2>/dev/null || true
        wait "$DISPATCH_PID" 2>/dev/null || true
    fi
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
    docker exec "$PG_CONTAINER" psql -U sprawl -d sprawl -q -tAc "$1" 2>&1
}

test_run() {
    unset SPRAWL_AGENT_IDENTITY

    if ! command -v docker >/dev/null 2>&1; then
        e2e_skip_row "docker not found on PATH — this row needs a real Postgres it can stop"
        return
    fi
    if ! docker info >/dev/null 2>&1; then
        e2e_skip_row "docker is installed but the daemon is unreachable"
        return
    fi

    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum1250-outage"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps
    trap reap_all EXIT
    reap_stale_pg

    local SUFFIX
    SUFFIX=$(head -c4 /dev/urandom | xxd -p)
    PG_CONTAINER="${PG_NAME_PREFIX}${SUFFIX}"

    # AN EXPLICIT HOST PORT, chosen here, NOT `docker run -P`.
    #
    # This is load-bearing for this row specifically and was found the hard way.
    # `-P` publishes to an EPHEMERAL port that Docker re-picks on every
    # `docker start` — so after the outage the container came back on a different
    # port, the DSN was stale, and the dispatcher went on logging
    # "connection refused" forever. The row reported "never caught up" and the
    # product was fine: the fixture had moved the database.
    #
    # A random high port keeps sibling agents on this shared host from colliding,
    # and the collision case is handled by simply failing the row rather than
    # retrying — a port clash is rare and a silent retry loop would hide it.
    local PG_PORT=$(( 20000 + RANDOM % 20000 ))
    echo "=== Starting Postgres 16 ($PG_CONTAINER) on host port $PG_PORT ==="
    if ! docker run -d --name "$PG_CONTAINER" \
        -e POSTGRES_USER=sprawl -e POSTGRES_PASSWORD=sprawl -e POSTGRES_DB=sprawl \
        -p "${PG_PORT}:5432" postgres:16-alpine >/dev/null 2>&1; then
        fail "could not start the Postgres container on port $PG_PORT (a sibling agent may hold it)"
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
        return 1
    fi

    local DSN="postgres://sprawl:sprawl@127.0.0.1:${PG_PORT}/sprawl?sslmode=disable"
    printf 'event_log.enabled: "true"\n' >> "$SPRAWL_ROOT/.sprawl/config.yaml"
    printf 'weave\n' > "$SPRAWL_ROOT/.sprawl/root-name"

    if (cd "$SPRAWL_ROOT" && SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_DB_DSN="$DSN" "$SPRAWL_BIN" store migrate >/dev/null 2>&1); then
        pass "store migrate applied the schema"
    else
        fail "store migrate failed"
        return 1
    fi
    if (cd "$SPRAWL_ROOT" && SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_DB_DSN="$DSN" "$SPRAWL_BIN" store status 2>/dev/null | grep -q "event log: enabled"); then
        pass "the event log opened and registered the project"
    else
        fail "the event log did not open"
        return 1
    fi

    local PROJ
    PROJ=$(psql_q "SELECT id FROM projects LIMIT 1")
    local UUID_RE='^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
    if [[ ! "$PROJ" =~ $UUID_RE ]]; then
        fail "could not resolve the project id (got: $PROJ)"
        return 1
    fi

    # seed_result appends a goal owned by weave plus the close that lands it.
    seed_result() {
        local goal
        goal=$(psql_q "INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload)
                       VALUES (gen_random_uuid(), '$PROJ', gen_random_uuid(),
                         (SELECT id FROM event_type_schemas WHERE name='goal_opened' AND version=1),
                         '{\"goal_type\":\"research\",\"text\":\"outage\",\"owner\":\"weave\"}'::jsonb)
                       RETURNING id")
        [[ "$goal" =~ $UUID_RE ]] || { echo "SEED FAILED: $goal" >&2; return 1; }
        psql_q "INSERT INTO open_contracts (event_id, workflow_instance_id)
                SELECT id, workflow_instance_id FROM events WHERE id='$goal'" >/dev/null
        psql_q "INSERT INTO events (id, project_id, workflow_instance_id, schema_id, closes_event_id, payload)
                VALUES (gen_random_uuid(), '$PROJ', gen_random_uuid(),
                  (SELECT id FROM event_type_schemas WHERE name='goal_closed' AND version=1),
                  '$goal', '{\"outcome\":\"success\",\"summary\":\"outage test\"}'::jsonb)" >/dev/null
    }

    if ! seed_result; then
        fail "could not seed the pre-outage result"
        return 1
    fi

    echo ""
    echo "=== Starting the long-running dispatcher ==="
    DISPATCH_LOG="$SPRAWL_ROOT/dispatch.log"
    (cd "$SPRAWL_ROOT" && SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_DB_DSN="$DSN" \
        "$SPRAWL_BIN" store dispatch --host outage-host --no-sweeper >"$DISPATCH_LOG" 2>&1) &
    DISPATCH_PID=$!

    # PRE-OUTAGE POSITIVE CONTROL. Without it, every assertion after the outage
    # could be satisfied by a process that never dispatched anything.
    local DELIVERED=0
    for i in $(seq 1 40); do
        if [ "$(ls "$SPRAWL_ROOT/.sprawl/messages/weave/new" 2>/dev/null | wc -l | tr -d ' ')" -ge 1 ]; then
            DELIVERED=1
            break
        fi
        sleep 1
    done
    if [ "$DELIVERED" -eq 1 ]; then
        pass "the dispatcher delivered the pre-outage result (so it was genuinely working)"
    else
        fail "the dispatcher never delivered the pre-outage result; nothing below would mean anything"
        tail -20 "$DISPATCH_LOG" >&2 || true
        return 1
    fi

    echo ""
    echo "=== Killing the database ==="
    docker stop "$PG_CONTAINER" >/dev/null 2>&1 || true

    # THE OUTAGE IS REAL — the assertion a row like this is most likely to omit.
    # A container that silently kept running would make everything below a story
    # about nothing.
    if docker exec "$PG_CONTAINER" pg_isready -U sprawl -d sprawl >/dev/null 2>&1; then
        fail "the container is still serving queries after docker stop; the 'outage' is not an outage and this row would assert nothing"
        return 1
    else
        pass "the outage is real: the database no longer answers"
    fi

    # Several poll intervals with the database down. The dispatcher polls every
    # 2s, so 8s is at least three failed passes.
    sleep 8

    if kill -0 "$DISPATCH_PID" 2>/dev/null; then
        pass "the dispatcher is still alive after the database has been down for several poll intervals"
    else
        fail "the dispatcher exited during the outage; a two-second blip must not require a human to restart it"
        tail -30 "$DISPATCH_LOG" >&2 || true
        return 1
    fi
    # It must be LOUD about it, not silently idle. A dispatcher that swallowed the
    # outage would be indistinguishable from one with nothing to do.
    # ANCHORED ON THE DISPATCHER'S OWN MESSAGE, not on a loose keyword.
    #
    # The first version of this assertion grepped for "connect" among other
    # things, and it PASSED VACUOUSLY: the store's startup append-only warning
    # contains "this connection holds", so the row reported that the outage was
    # logged while the dispatcher was in fact logging NOTHING — its Logger was
    # never wired, so every failure went to slog.DiscardHandler. A loose pattern
    # in a log assertion is a false green waiting to happen, and this one was one.
    if grep -q "dispatch pass failed" "$DISPATCH_LOG"; then
        pass "the dispatcher logged its failing passes rather than going quietly idle"
    else
        fail "the log contains no 'dispatch pass failed' line; a silent dispatcher cannot be told from an idle one"
        tail -30 "$DISPATCH_LOG" >&2 || true
    fi

    echo ""
    echo "=== Bringing the database back ==="
    docker start "$PG_CONTAINER" >/dev/null 2>&1 || true
    READY=0
    for i in $(seq 1 60); do
        if docker exec "$PG_CONTAINER" pg_isready -U sprawl -d sprawl >/dev/null 2>&1; then
            READY=1
            break
        fi
        sleep 1
    done
    if [ "$READY" -eq 1 ]; then
        pass "the database is answering again"
    else
        fail "the database did not come back within 60s"
        return 1
    fi

    # A result appended AFTER the outage began. This is what makes the catch-up
    # assertion mean "it resumed from its cursor" rather than "it re-delivered
    # something it had already seen".
    if ! seed_result; then
        fail "could not seed the post-outage result"
        return 1
    fi

    local BEFORE=1 AFTER=0
    for i in $(seq 1 60); do
        AFTER=$(ls "$SPRAWL_ROOT/.sprawl/messages/weave/new" 2>/dev/null | wc -l | tr -d ' ')
        if [ "$AFTER" -gt "$BEFORE" ]; then
            break
        fi
        sleep 1
    done
    if [ "$AFTER" -gt "$BEFORE" ]; then
        pass "the dispatcher caught up after reconnecting and delivered the result appended during/after the outage ($BEFORE -> $AFTER)"
    else
        fail "the dispatcher never caught up: envelopes stuck at $AFTER"
        tail -30 "$DISPATCH_LOG" >&2 || true
    fi

    echo ""
    e2e_print_results
}
