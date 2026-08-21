#!/usr/bin/env bash
# scripts/e2e-tests/dispatch-exactly-once.sh — QUM-1250 (M1b) AC1 and AC2, plus
# the notification open/close cycle, against a REAL Postgres and the REAL
# `sprawl` binary.
#
# WHAT THIS ROW BUYS OVER THE UNIT SUITE, which is the only reason it exists:
# internal/store's integration tests drive the dispatcher as a Go object with the
# handlers wired by the test. This row runs `sprawl store dispatch` — the actual
# command an operator or M3a invokes — so it exercises the WIRING: that the
# handlers are registered for the right event types, that the cursor lands where
# the command says it does, that the adapters write artifacts the recipient's own
# drain can read, and that the claim consumer is shared rather than per-host.
# Every one of those is a decision made in cmd/store_dispatch.go and asserted
# nowhere else.
#
# It also asserts the two properties whose failure mode is a DUPLICATE SIDE
# EFFECT, which is the class the whole milestone is about:
#
#   AC1  crash between the side effect and the cursor advance -> no repeat.
#        Simulated honestly: delete the cursor directory, which is exactly the
#        state a kill -9 before the cursor write leaves, and re-run.
#   AC2  two dispatchers, one Postgres -> each event acted on once. Run with two
#        different --host values against one database.
#
# NEEDS NO `claude` AND NO `tmux`. The dispatch path is a plain process against a
# database, so this row cannot fail for auth reasons — which is worth stating
# because a `Not logged in` count of zero here is structural rather than lucky.
#
# POSITIVE CONTROLS ARE INSIDE THE ROW, not left to a reviewer: it asserts that
# the notification IS delivered before asserting it is not delivered twice, and
# that the sweeper IS reached before asserting it pokes nothing. A row that only
# counted absences would pass against a dispatcher that did nothing at all.

# QUM-1029: assertions a COMPLETE, PASSING run makes. Hand-counted against the
# pass/fail pairs below: docker ready, migrate, first dispatch handled 1,
# envelope present, envelope body names the event, queue entry present and
# async-class, owner_notify outstanding, per-recipient claim present, shared
# consumer claim present, cursor file written, AC1 no-duplicate after cursor
# reset, AC1 no duplicate envelope, AC2 second host handled nothing new, AC2 one
# owner_notify total, ack closes the contract, sweeper reached and poked nothing,
# plus the ledger-opened and seeding-succeeded preconditions (both of which are
# real assertions: the row silently measured nothing without them).
MIN_ASSERTIONS=20

test_metadata() {
    echo "needs_claude=0 needs_tmux=0"
}

PG_CONTAINER=""
PG_NAME_PREFIX="sprawl-qum1250-pg-"

# reap_pg chains to the harness cleanup rather than replacing it.
#
# The exit status is captured FIRST and restored before delegating: _e2e_cleanup
# does `local rc=$?` and re-exits with what it observes, and `docker rm ... ||
# true` succeeds — so without the save/restore a FAILING ROW REPORTS PASS. That
# is not theoretical; it was measured on this row's sibling (store-lifecycle-live)
# and is why the idiom exists.
reap_pg() {
    local rc=$?
    if [ -n "$PG_CONTAINER" ]; then
        docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
    fi
    ( exit "$rc" )
    _e2e_cleanup
}

# A trap cannot cover SIGKILL, so leak recovery is somebody's job at START too.
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

# dispatch runs one pass of the real command as $1 (the host identity).
dispatch_once() {
    local host="$1"
    (cd "$SPRAWL_ROOT" && SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_DB_DSN="$DSN" \
        "$SPRAWL_BIN" store dispatch --once --host "$host" 2>&1)
}

test_run() {
    unset SPRAWL_AGENT_IDENTITY

    if ! command -v docker >/dev/null 2>&1; then
        e2e_skip_row "docker not found on PATH — this row needs a real Postgres"
        return
    fi
    if ! docker info >/dev/null 2>&1; then
        e2e_skip_row "docker is installed but the daemon is unreachable"
        return
    fi

    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum1250-exactly-once"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps
    trap reap_pg EXIT
    reap_stale_pg

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
    local PG_PORT
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

    DSN="postgres://sprawl:sprawl@127.0.0.1:${PG_PORT}/sprawl?sslmode=disable"
    printf 'event_log.enabled: "true"\n' >> "$SPRAWL_ROOT/.sprawl/config.yaml"
    printf 'weave\n' > "$SPRAWL_ROOT/.sprawl/root-name"

    echo ""
    echo "=== Applying event-log migrations ==="
    if (cd "$SPRAWL_ROOT" && SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_DB_DSN="$DSN" "$SPRAWL_BIN" store migrate >/dev/null 2>&1); then
        pass "store migrate applied the schema and published the seed event types"
    else
        fail "store migrate failed"
        return 1
    fi

    # OPEN THE LEDGER ONCE BEFORE SEEDING.
    #
    # `store migrate` applies the schema but does NOT register the project — the
    # projects row is written by ensureProject when the ledger first OPENS. The
    # first version of this row seeded straight after migrate, so `SELECT id FROM
    # projects` returned nothing, every insert failed, and the row reported
    # "scanned 1, handled 0". Worth recording because the guard below did not
    # catch it either: psql_q folds stderr into its output, so the empty-check saw
    # a non-empty ERROR string and passed.
    if (cd "$SPRAWL_ROOT" && SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_DB_DSN="$DSN" "$SPRAWL_BIN" store status 2>/dev/null | grep -q "event log: enabled"); then
        pass "the event log opened and registered the project"
    else
        fail "the event log did not open; nothing below could be seeded against a project"
        return 1
    fi

    # Seed a landing result: a goal owned by weave, and the close that lands it.
    # Written with psql rather than through any Go code from this repo, so the row
    # does not depend on the appender it is testing the consumers of.
    local PROJ GOAL CLOSE
    PROJ=$(psql_q "SELECT id FROM projects LIMIT 1")
    GOAL=$(psql_q "INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload)
                   VALUES (gen_random_uuid(), '$PROJ', gen_random_uuid(),
                     (SELECT id FROM event_type_schemas WHERE name='goal_opened' AND version=1),
                     '{\"goal_type\":\"research\",\"text\":\"e2e\",\"owner\":\"weave\"}'::jsonb)
                   RETURNING id")
    psql_q "INSERT INTO open_contracts (event_id, workflow_instance_id)
            SELECT id, workflow_instance_id FROM events WHERE id='$GOAL'" >/dev/null
    CLOSE=$(psql_q "INSERT INTO events (id, project_id, workflow_instance_id, schema_id, closes_event_id, payload)
                    VALUES (gen_random_uuid(), '$PROJ', gen_random_uuid(),
                      (SELECT id FROM event_type_schemas WHERE name='goal_closed' AND version=1),
                      '$GOAL', '{\"outcome\":\"success\",\"summary\":\"e2e done\"}'::jsonb)
                    RETURNING id")
    # UUID-SHAPED, not merely non-empty. psql_q folds stderr into its output, so a
    # failed insert yields a non-empty ERROR string — which an emptiness check
    # accepts, leaving the row asserting against events that do not exist. That
    # happened; hence the shape check.
    local UUID_RE='^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
    if [[ ! "$PROJ" =~ $UUID_RE ]] || [[ ! "$GOAL" =~ $UUID_RE ]] || [[ ! "$CLOSE" =~ $UUID_RE ]]; then
        fail "could not seed the goal/close pair (project=$PROJ goal=$GOAL close=$CLOSE)"
        return 1
    fi
    pass "seeded an owned goal and the result that closes it"

    echo ""
    echo "=== First dispatch pass (host-a) ==="
    local OUT
    OUT=$(dispatch_once "host-a")
    echo "$OUT" | grep -E "^host:|^dispatch pass|^reconcile|^sweep|^limits" || true

    # POSITIVE CONTROL FIRST: the notification must actually happen, or every
    # no-duplicate assertion below is vacuous.
    if echo "$OUT" | grep -qE "^dispatch pass: scanned [0-9]+, handled 1,"; then
        pass "the first pass handled exactly one event (the landing result)"
    else
        fail "the first pass did not handle the landing result: $(echo "$OUT" | grep '^dispatch pass' || echo 'no pass line')"
        return 1
    fi

    local MAILDIR="$SPRAWL_ROOT/.sprawl/messages/weave/new"
    local ENV_COUNT
    ENV_COUNT=$(ls "$MAILDIR" 2>/dev/null | wc -l | tr -d ' ')
    if [ "$ENV_COUNT" = "1" ]; then
        pass "the notification envelope was written to weave's maildir"
    else
        fail "weave's maildir holds $ENV_COUNT envelopes, want 1"
    fi
    # The body must name the closing event, because that is what makes the
    # 300-rune summary sufficient: the recipient reads the detail from the log.
    if grep -q "$CLOSE" "$MAILDIR"/*.json 2>/dev/null; then
        pass "the envelope body names the event that landed"
    else
        fail "the envelope body does not name the closing event $CLOSE"
    fi

    # The queue entry is what the recipient's own drain reads. An envelope with no
    # entry is never injected.
    local PENDING="$SPRAWL_ROOT/.sprawl/agents/weave/queue/pending"
    local PEND_COUNT
    PEND_COUNT=$(ls "$PENDING" 2>/dev/null | wc -l | tr -d ' ')
    if [ "$PEND_COUNT" = "1" ]; then
        pass "a queue entry was written where the recipient's drain reads it"
    else
        fail "weave's pending queue holds $PEND_COUNT entries, want 1"
    fi
    # ASYNC, never interrupt: a coordination nudge must not preempt a turn.
    if ls "$PENDING" 2>/dev/null | grep -q -- "-async-"; then
        pass "the queue entry is async-class, so it cannot preempt a turn"
    else
        fail "the queue entry is not async-class: $(ls "$PENDING" 2>/dev/null | tr '\n' ' ')"
    fi

    if [ "$(psql_q "SELECT count(*) FROM open_contracts oc
                    JOIN events e ON e.id=oc.event_id
                    JOIN event_type_schemas s ON s.id=e.schema_id
                    WHERE s.name='owner_notify'")" = "1" ]; then
        pass "the owner_notify contract is OUTSTANDING, so a lost delivery is sweepable"
    else
        fail "no outstanding owner_notify contract; a lost delivery would be invisible"
    fi

    # The claim keys are decisions made in cmd/store_dispatch.go and asserted
    # nowhere else.
    if [ "$(psql_q "SELECT count(*) FROM event_claims WHERE consumer='notify:weave'")" = "1" ]; then
        pass "the notification claim is keyed per recipient (notify:weave)"
    else
        fail "no per-recipient notification claim: $(psql_q "SELECT string_agg(consumer, ',') FROM event_claims")"
    fi
    if [ "$(psql_q "SELECT count(*) FROM event_claims WHERE consumer='dispatcher'")" -ge "1" ]; then
        pass "the dispatch claim uses the shared, host-independent consumer name"
    else
        fail "no shared 'dispatcher' claim; a per-host consumer would let every host act on every event"
    fi

    local CURSOR="$SPRAWL_ROOT/.sprawl/store/dispatch/cursor-dispatcher.json"
    if [ -f "$CURSOR" ]; then
        pass "the cursor landed where the command documents it ($(basename "$CURSOR"))"
    else
        fail "no cursor at $CURSOR — 'delete this to re-scan' is the documented recovery and it must be findable"
    fi

    echo ""
    echo "=== AC1: crash between the side effect and the cursor advance ==="
    # Deleting the cursor directory IS the post-crash state: the side effect
    # happened, the position was never recorded. Nothing but event_claims can
    # prevent the repeat.
    rm -rf "$SPRAWL_ROOT/.sprawl/store/dispatch"
    OUT=$(dispatch_once "host-a")
    echo "$OUT" | grep -E "^dispatch pass" || true
    if echo "$OUT" | grep -qE "^dispatch pass: scanned [0-9]+, handled 0,"; then
        pass "AC1: after a full re-scan the dispatcher acted on nothing (claims absorbed the replay)"
    else
        fail "AC1: the re-scan repeated work: $(echo "$OUT" | grep '^dispatch pass')"
    fi
    local ENV2
    ENV2=$(ls "$MAILDIR" 2>/dev/null | wc -l | tr -d ' ')
    if [ "$ENV2" = "1" ]; then
        pass "AC1: no duplicate notification was delivered ($ENV2 envelope)"
    else
        fail "AC1: weave's maildir now holds $ENV2 envelopes, want 1 — the result was delivered twice"
    fi

    echo ""
    echo "=== AC2: a second dispatcher on the same Postgres ==="
    # A DIFFERENT host, its OWN cursor (a fresh sandbox path would be a different
    # story about files, so the cursor is reset instead to force a full re-scan),
    # sharing the claim table.
    rm -rf "$SPRAWL_ROOT/.sprawl/store/dispatch"
    OUT=$(dispatch_once "host-b")
    echo "$OUT" | grep -E "^host:|^dispatch pass" || true
    if echo "$OUT" | grep -qE "^dispatch pass: scanned [0-9]+, handled 0,"; then
        pass "AC2: the second host acted on nothing already claimed by the first"
    else
        fail "AC2: the second host repeated the first host's work: $(echo "$OUT" | grep '^dispatch pass')"
    fi
    if [ "$(psql_q "SELECT count(*) FROM events e JOIN event_type_schemas s ON s.id=e.schema_id WHERE s.name='owner_notify'")" = "1" ]; then
        pass "AC2: exactly one owner_notify exists across two dispatchers"
    else
        fail "AC2: $(psql_q "SELECT count(*) FROM events e JOIN event_type_schemas s ON s.id=e.schema_id WHERE s.name='owner_notify'") owner_notify events across two dispatchers, want 1"
    fi

    echo ""
    echo "=== The ack closes the contract at the recipient's turn boundary ==="
    psql_q "INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload)
            VALUES (gen_random_uuid(), '$PROJ', gen_random_uuid(),
              (SELECT id FROM event_type_schemas WHERE name='turn_finished' AND version=1),
              '{\"agent_name\":\"weave\",\"session_id\":\"s1\",\"input_tokens\":1,\"output_tokens\":2}'::jsonb)" >/dev/null
    OUT=$(dispatch_once "host-a")
    echo "$OUT" | grep -E "^dispatch pass" || true
    if [ "$(psql_q "SELECT count(*) FROM open_contracts oc
                    JOIN events e ON e.id=oc.event_id
                    JOIN event_type_schemas s ON s.id=e.schema_id
                    WHERE s.name='owner_notify'")" = "0" ]; then
        pass "the recipient's turn boundary closed the notification contract"
    else
        fail "the owner_notify contract is still outstanding after weave took a turn; the sweeper would re-deliver a result the owner already saw"
    fi

    echo ""
    echo "=== The sweeper is reached, and is INERT in a standalone run ==="
    # BOTH halves. "poked nothing" alone is satisfied by a sweeper that never
    # ran, so the row first asserts it was REACHED (it reports a considered
    # count) and only then that it poked nothing — which is the documented
    # consequence of turn state being unobservable outside a sprawl session.
    if echo "$OUT" | grep -qE "^sweep: considered [1-9]"; then
        pass "the sweeper ran and considered at least one open contract"
    else
        fail "the sweeper never considered anything, so the inertness assertion below would be vacuous: $(echo "$OUT" | grep '^sweep' || echo 'no sweep line')"
    fi
    if echo "$OUT" | grep -qE "^sweep: considered [0-9]+, poked 0,"; then
        pass "the sweeper poked nothing, as a standalone run documents (turn state is unobservable here)"
    else
        fail "a standalone sweeper POKED something; turn state is unobservable outside a sprawl session, so it cannot know the owner is idle: $(echo "$OUT" | grep '^sweep')"
    fi

    echo ""
    e2e_print_results
}
