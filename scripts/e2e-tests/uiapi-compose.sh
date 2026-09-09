#!/usr/bin/env bash
# scripts/e2e-tests/uiapi-compose.sh — the slice C-API row (QUM-1349).
#
# Stands up deploy/webui/compose.yaml for real (Postgres 16, `sprawl store
# migrate`, the sprawl_ro GRANT, and the uiapi container built from this tree),
# seeds events with psql, and asserts that all six endpoints return them.
#
# WHY A COMPOSE ROW AND NOT MORE UNIT TESTS.
# internal/uiapi's unit suite drives the handlers through httptest against a
# pgx.Rows fake, so it proves the Go — the mapping, the envelopes, the 400s, the
# leak suppression. What it structurally CANNOT prove is that the SQL is valid
# SQL, that it runs at all under the SELECT-only sprawl_ro role, or that it
# returns the rows it claims to. A fake pool accepts any string. Six queries
# with CTEs, FULL OUTER JOINs, DISTINCT ON and jsonb casts had never touched a
# database before this row existed.
#
# THE ASSERTION THIS ROW EXISTS FOR (A9).
# /api/usage must report cost from run_finished and never from turn_finished.
# The CLI reports per-turn cost CUMULATIVELY for the session, so summing turns
# overstates spend by a measured 4-10x (QUM-1247). The seed makes the two
# answers DIFFERENT NUMBERS on purpose: turn costs 0.10 + 0.30 sum to 0.40,
# while the session's run_finished total is 0.30. A regression to per-turn
# summing therefore fails on the value, not on a query-text grep — which is all
# the unit test can do.
#
# WHY THE SEED IS RAW SQL.
# The seed inserts events directly rather than driving the appender, and
# maintains open_contracts explicitly, because that projection is written by the
# appender in the same transaction as the append and there is no trigger to do
# it. That makes the seed a statement of what the appender WOULD write; it is
# not a test of the appender, which store-pg-integration already covers.
#
# It does NOT insert into event_type_schemas: `sprawl store migrate` syncs the
# embedded seeds (internal/store/seeds/*.json) into that table, so every type
# this row needs is already registered with its real json_schema and its real
# spillable flag. Resolving the schema_id BY NAME rather than inserting a
# fixture also means the row exercises the same name->schema join the handlers
# do. (Measured: an earlier draft inserted its own rows and died on
# `duplicate key ... (goal_opened, 1) already exists` — which is how this
# comment stopped being a guess.)
#
# `web` (slice B's frontend, zone's file) is deliberately NOT brought up. It is
# not a dependency of the API, and building it would make this row fail for
# reasons in someone else's slice. The override file publishes a port on uiapi
# for the duration of the row instead of going through nginx.
#
# Needs Docker, `docker compose`, curl, jq and python3. No claude and no tmux.

# QUM-1029: the number of assertions a COMPLETE, PASSING run makes. Counted by
# hand from the symmetric pass/fail gates in test_run below and listed so a
# reader can check the arithmetic rather than trust it:
#
#   A1  the API becomes healthy                      (assert_healthy)
#   A2  /api/events returns the seeded event
#   A3  ?project_id= includes A and EXCLUDES B
#   A4  ?before_seq= excludes the newest event
#   A5  ?limit=0 is a 400
#   A6  /api/goals shows the open goal, not the closed one
#   A7  /api/inbox shows the open question
#   A8  /api/workflows derives in_flight AND settled
#   A9  /api/fleet attributes both turns to the agent
#   A10 /api/fleet makes no liveness claim
#   A11 /api/usage bills the run total, not the turn sum   <- the QUM-1247 gate
#   A12 /api/usage ?bucket=week is a 400
#   A13 POST /api/events is a 405
#   A14 no 5xx body carries the database's error text
#
# Update this number in the same commit as any change to that list. It does not
# self-adjust, and a floor above what a passing run asserts turns an honest run
# red.
MIN_ASSERTIONS=14

# Deadline for the API to answer /healthz, covering `docker compose up --build`
# on a cold cache (the Go build plus a Postgres first-boot). Generous on purpose:
# a timeout here is a row failure, and a flaky one is worse than a slow one.
UIAPI_READY_TIMEOUT=${SPRAWL_E2E_UIAPI_READY_TIMEOUT:-300}

test_metadata() {
    echo "needs_jq=1"
}

# compose PROJECT-SCOPED. Every docker invocation in this row goes through this
# wrapper, so no command can touch a stack this row did not create.
uiapi_compose() {
    docker compose -p "$UIAPI_PROJECT" \
        -f "$REPO_ROOT/deploy/webui/compose.yaml" \
        -f "$UIAPI_TMPDIR/override.yaml" "$@"
}

# uiapi_get PATH publishes the response as UIAPI_STATUS and UIAPI_BODY.
#
# It returns NOTHING on stdout, on purpose. The obvious shape — echo the body,
# set the status in a variable — cannot work: every caller would write
# `body=$(uiapi_get ...)`, which runs the function in a SUBSHELL, so the status
# assignment dies with it and the next `$UIAPI_STATUS` read is either stale or
# (under the harness's `set -u`) an unbound-variable abort. Measured: the first
# run of this row printed "UIAPI_STATUS: unbound variable" 70 times and reported
# the API unreachable while its own logs showed it serving.
UIAPI_STATUS=""
UIAPI_BODY=""
uiapi_get() {
    UIAPI_STATUS=$(curl -sS -m 20 -o "$UIAPI_TMPDIR/body" \
        -w '%{http_code}' "http://127.0.0.1:$UIAPI_PORT$1" 2>/dev/null || echo 000)
    UIAPI_BODY=$(cat "$UIAPI_TMPDIR/body" 2>/dev/null || true)
}

# uiapi_json PATH JQ_FILTER -> the filter's result, or "" when the request was
# not a 200. Never silently substitutes a value: a non-200 yields an empty
# string, which fails every comparison it feeds rather than matching a zero.
uiapi_json() {
    uiapi_get "$1"
    if [ "$UIAPI_STATUS" != "200" ]; then
        echo "  (GET $1 returned HTTP $UIAPI_STATUS: $(printf '%s' "$UIAPI_BODY" | head -c 200))" >&2
        return 0
    fi
    printf '%s' "$UIAPI_BODY" | jq -r "$2" 2>/dev/null || true
}

uiapi_psql() {
    uiapi_compose exec -T -e PGPASSWORD=sprawl_dev_password db \
        psql -v ON_ERROR_STOP=1 -U sprawl_owner -d sprawl -qtAX "$@"
}

uiapi_teardown() {
    if [ -n "${UIAPI_PROJECT:-}" ]; then
        echo "== tearing down $UIAPI_PROJECT =="
        uiapi_compose logs --no-color --tail 40 uiapi 2>/dev/null || true
        uiapi_compose down -v --remove-orphans >/dev/null 2>&1 || true
    fi
    # Belt and braces on the destructive-var rule: assert the path is ours and
    # under /tmp before removing it, never trust the variable's value.
    if [ -n "${UIAPI_TMPDIR:-}" ] && [ -d "$UIAPI_TMPDIR" ]; then
        case "$UIAPI_TMPDIR" in
            /tmp/*) rm -rf "$UIAPI_TMPDIR" ;;
            *) echo "WARN: refusing to remove '$UIAPI_TMPDIR' — not under /tmp" >&2 ;;
        esac
    fi
}

# The seed. Two projects, so the project filter has something to EXCLUDE rather
# than merely something to return — a filter asserted only against the project
# it selects passes while filtering nothing.
#
# Costs are the point: turn_finished carries 0.10 then 0.30 (cumulative, as the
# CLI reports it), summing to 0.40, while run_finished states the session total
# of 0.30. See the A9 note in the header.
uiapi_seed_sql() {
    cat <<'SQL'
BEGIN;

INSERT INTO projects (id, remote_url) VALUES
  ('a0000000-0000-0000-0000-000000000001', 'git@github.com:dmotles/sprawl.git'),
  ('b0000000-0000-0000-0000-000000000002', 'https://github.com/other/widget.git');

-- schema_id(name) resolves a registered type to its NEWEST version, matching
-- how the handlers join on name. A missing type raises a NOT NULL violation on
-- events.schema_id, so a renamed or unregistered type fails the seed loudly
-- instead of quietly inserting an event no view can classify.
CREATE OR REPLACE FUNCTION pg_temp.schema_id(text) RETURNS uuid LANGUAGE sql AS
  $$ SELECT id FROM event_type_schemas WHERE name = $1 ORDER BY version DESC LIMIT 1 $$;

-- Instance wf1: an open goal and an open question. Stays in_flight.
INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload, at) VALUES
  ('e0000000-0000-0000-0000-000000000001',
   'a0000000-0000-0000-0000-000000000001', '11111111-1111-1111-1111-111111111111',
   pg_temp.schema_id('goal_opened'),
   '{"goal_type":"ship-slice-c","owner":"ratz"}'::jsonb, '2026-09-09T10:00:00Z'),
  ('e0000000-0000-0000-0000-000000000002',
   'a0000000-0000-0000-0000-000000000001', '11111111-1111-1111-1111-111111111111',
   pg_temp.schema_id('user_question'),
   '{"asker":"ratz","question":"Is the contract final?","context":""}'::jsonb, '2026-09-09T10:05:00Z');

-- open_contracts is maintained by the appender in the append's own transaction;
-- there is no trigger, so the seed writes what the appender would have written.
INSERT INTO open_contracts (event_id, workflow_instance_id, opened_at) VALUES
  ('e0000000-0000-0000-0000-000000000001', '11111111-1111-1111-1111-111111111111', '2026-09-09T10:00:00Z'),
  ('e0000000-0000-0000-0000-000000000002', '11111111-1111-1111-1111-111111111111', '2026-09-09T10:05:00Z');

-- Instance wf2: a goal opened AND closed. No open_contracts row, so it must
-- report `settled` and must still be listed.
INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload, at) VALUES
  ('e0000000-0000-0000-0000-000000000003',
   'a0000000-0000-0000-0000-000000000001', '22222222-2222-2222-2222-222222222222',
   pg_temp.schema_id('goal_opened'),
   '{"goal_type":"already-done","owner":"ratz"}'::jsonb, '2026-09-09T09:00:00Z');
INSERT INTO events (id, project_id, workflow_instance_id, schema_id, closes_event_id, payload, at) VALUES
  ('e0000000-0000-0000-0000-000000000004',
   'a0000000-0000-0000-0000-000000000001', '22222222-2222-2222-2222-222222222222',
   pg_temp.schema_id('goal_closed'),
   'e0000000-0000-0000-0000-000000000003',
   '{"outcome":"ok"}'::jsonb, '2026-09-09T09:30:00Z');

-- Two turns, with CUMULATIVE per-turn costs, and one run_finished carrying the
-- session total. 0.10 + 0.30 = 0.40 is the wrong answer; 0.30 is the right one.
INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload, at) VALUES
  ('e0000000-0000-0000-0000-000000000005',
   'a0000000-0000-0000-0000-000000000001', '11111111-1111-1111-1111-111111111111',
   pg_temp.schema_id('turn_finished'),
   '{"agent_name":"ratz","session_id":"sess-1","input_tokens":100,"output_tokens":10,"cost_usd":0.10}'::jsonb,
   '2026-09-09T10:10:00Z'),
  ('e0000000-0000-0000-0000-000000000006',
   'a0000000-0000-0000-0000-000000000001', '11111111-1111-1111-1111-111111111111',
   pg_temp.schema_id('turn_finished'),
   '{"agent_name":"ratz","session_id":"sess-1","input_tokens":200,"output_tokens":20,"cost_usd":0.30}'::jsonb,
   '2026-09-09T10:20:00Z'),
  ('e0000000-0000-0000-0000-000000000007',
   'a0000000-0000-0000-0000-000000000001', '11111111-1111-1111-1111-111111111111',
   pg_temp.schema_id('run_finished'),
   '{"agent_name":"ratz","session_id":"sess-1","outcome":"ok","cost_usd":0.30,"turns":2}'::jsonb,
   '2026-09-09T10:25:00Z');

-- A turn with no agent_name: unattributable, so /api/fleet must exclude it
-- rather than invent an agent called "".
INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload, at) VALUES
  ('e0000000-0000-0000-0000-000000000008',
   'a0000000-0000-0000-0000-000000000001', '11111111-1111-1111-1111-111111111111',
   pg_temp.schema_id('turn_finished'),
   '{"session_id":"sess-1","input_tokens":1,"output_tokens":1}'::jsonb, '2026-09-09T10:26:00Z');

-- Project B. Nothing that reads project A may return this.
INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload, at) VALUES
  ('e0000000-0000-0000-0000-000000000009',
   'b0000000-0000-0000-0000-000000000002', '33333333-3333-3333-3333-333333333333',
   pg_temp.schema_id('turn_finished'),
   '{"agent_name":"someone-else","session_id":"sess-2","input_tokens":7,"output_tokens":7}'::jsonb,
   '2026-09-09T10:30:00Z');

COMMIT;
SQL
}

test_run() {
    echo "== uiapi-compose: six read endpoints against a real Postgres =="

    # jq is not in this list: `needs_jq=1` above makes the driver preflight it.
    for tool in docker curl python3; do
        if ! command -v "$tool" >/dev/null 2>&1; then
            e2e_skip_row "$tool not found on PATH — this row stands up the compose stack and queries it over HTTP"
        fi
    done
    if ! docker info >/dev/null 2>&1; then
        e2e_skip_row "docker is installed but the daemon is unreachable — cannot start the compose stack"
    fi
    if ! docker compose version >/dev/null 2>&1; then
        e2e_skip_row "the docker compose plugin is unavailable — this row needs 'docker compose', not docker alone"
    fi

    UIAPI_TMPDIR=$(mktemp -d "/tmp/sprawl-e2e-uiapi.XXXXXX") || {
        e2e_skip_row "cannot create a temp dir for the compose override"
    }
    UIAPI_PROJECT="sprawl-uiapi-e2e-$$"
    trap uiapi_teardown EXIT

    # A free ephemeral port, asked of the kernel rather than guessed: a
    # hard-coded one collides with a co-tenant row and fails for a reason that
    # has nothing to do with the API.
    UIAPI_PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
    if [ -z "$UIAPI_PORT" ]; then
        fail "could not reserve a local port"
        e2e_print_results
        return
    fi

    # The override exists so the tracked compose file keeps its property that
    # the API publishes no port. Loopback-bound: this is a test fixture, not a
    # service, and it must not be reachable off-host.
    cat >"$UIAPI_TMPDIR/override.yaml" <<YAML
services:
  uiapi:
    ports:
      - "127.0.0.1:$UIAPI_PORT:8080"
YAML

    echo "-- bringing up db, migrate, grant, uiapi on port $UIAPI_PORT (project $UIAPI_PROJECT)"
    if ! uiapi_compose up -d --build db migrate grant uiapi >"$UIAPI_TMPDIR/up.log" 2>&1; then
        echo "---- compose up output ----"
        tail -60 "$UIAPI_TMPDIR/up.log"
        fail "compose up failed — the stack never started, so nothing below was measured"
        e2e_print_results
        return
    fi

    # A1. Not a bare wait: the API refuses to boot on an unmigrated database or
    # a missing reader, so /healthz answering 200 already proves migrate and
    # grant succeeded and that every Config reader was wired.
    local waited=0 healthy=0
    while [ "$waited" -lt "$UIAPI_READY_TIMEOUT" ]; do
        if [ "$(uiapi_json /healthz '.status')" = "ok" ]; then
            healthy=1
            break
        fi
        sleep 2
        waited=$((waited + 2))
    done
    if [ "$healthy" = "1" ]; then
        pass "the API answered /healthz ok after ${waited}s (so it migrated, connected as sprawl_ro, and wired every reader)"
    else
        fail "the API never answered /healthz within ${UIAPI_READY_TIMEOUT}s"
        e2e_print_results
        return
    fi

    echo "-- seeding events"
    if ! uiapi_seed_sql | uiapi_psql -f - >"$UIAPI_TMPDIR/seed.log" 2>&1; then
        tail -30 "$UIAPI_TMPDIR/seed.log"
        fail "seeding failed — every assertion below would have been vacuous"
        e2e_print_results
        return
    fi

    local got

    # A2. The ledger returns the seeded rows at all, with the type resolved
    # through the schema join rather than left as a uuid.
    got=$(uiapi_json '/api/events?limit=50' '[.events[] | select(.type=="goal_opened")] | length')
    if [ "$got" = "2" ]; then
        pass "/api/events returned both seeded goal_opened events with the type name resolved"
    else
        fail "/api/events returned $got goal_opened events, want 2"
    fi

    # A3. BOTH directions. A project filter asserted only against the project it
    # selects passes while filtering nothing at all.
    got=$(uiapi_json '/api/events?limit=50&project_id=a0000000-0000-0000-0000-000000000001' \
        '[.events[].project_id] | unique | join(",")')
    if [ "$got" = "a0000000-0000-0000-0000-000000000001" ]; then
        pass "/api/events?project_id= returned project A's events and none of project B's"
    else
        fail "/api/events?project_id= returned project ids [$got], want only project A's"
    fi

    # A4. The keyset cursor. seq is assigned by the database, so the cursor is
    # read back rather than assumed.
    local newest
    newest=$(uiapi_json '/api/events?limit=1' '.events[0].seq')
    got=$(uiapi_json "/api/events?limit=50&before_seq=$newest" \
        "[.events[] | select(.seq >= $newest)] | length")
    if [ -n "$newest" ] && [ "$got" = "0" ]; then
        pass "/api/events?before_seq=$newest excluded seq >= $newest (keyset cursor is strictly-less-than)"
    else
        fail "/api/events?before_seq=$newest returned $got events at or past the cursor, want 0"
    fi

    # A5. A limit the endpoint cannot serve is a refusal, not a silent default.
    uiapi_get '/api/events?limit=0'
    if [ "$UIAPI_STATUS" = "400" ]; then
        pass "/api/events?limit=0 was refused with 400 rather than defaulted"
    else
        fail "/api/events?limit=0 returned HTTP $UIAPI_STATUS, want 400"
    fi

    # A6. Both directions again: presence is the open state, so the CLOSED goal
    # must be absent. A query over `events` instead of `open_contracts` returns
    # both and would pass a presence-only check.
    local open_goals closed_goals
    open_goals=$(uiapi_json '/api/goals' '[.goals[] | select(.goal_type=="ship-slice-c")] | length')
    closed_goals=$(uiapi_json '/api/goals' '[.goals[] | select(.goal_type=="already-done")] | length')
    if [ "$open_goals" = "1" ] && [ "$closed_goals" = "0" ]; then
        pass "/api/goals listed the open goal and not the closed one"
    else
        fail "/api/goals: open goal count=$open_goals (want 1), closed goal count=$closed_goals (want 0)"
    fi

    # A7. The inbox, with the derived project label — which also proves
    # remote_url was shortened server-side rather than passed through.
    local q_count q_project
    q_count=$(uiapi_json '/api/inbox' '.questions | length')
    q_project=$(uiapi_json '/api/inbox' '.questions[0].project_name')
    if [ "$q_count" = "1" ] && [ "$q_project" = "sprawl" ]; then
        pass "/api/inbox returned the open question, labelled with the derived project name 'sprawl'"
    else
        fail "/api/inbox: question count=$q_count (want 1), project_name='$q_project' (want 'sprawl')"
    fi

    # A8. The derived state, on both sides of the rule, and the settled instance
    # must be PRESENT — the CLI's HAVING clause would hide it.
    local wf1_state wf2_state
    wf1_state=$(uiapi_json '/api/workflows' \
        '.workflows[] | select(.workflow_instance_id=="11111111-1111-1111-1111-111111111111") | .state')
    wf2_state=$(uiapi_json '/api/workflows' \
        '.workflows[] | select(.workflow_instance_id=="22222222-2222-2222-2222-222222222222") | .state')
    if [ "$wf1_state" = "in_flight" ] && [ "$wf2_state" = "settled" ]; then
        pass "/api/workflows derived in_flight for the open instance and settled for the closed one, listing both"
    else
        fail "/api/workflows: wf1 state='$wf1_state' (want in_flight), wf2 state='$wf2_state' (want settled)"
    fi

    # A9. Attribution and the token sums, plus the exclusion of the
    # agent_name-less turn: 3 turn_finished events exist for project A and only
    # 2 of them can be attributed.
    local turns fleet_rows
    turns=$(uiapi_json '/api/fleet' '.fleet[] | select(.agent_name=="ratz") | .turn_count')
    fleet_rows=$(uiapi_json '/api/fleet' '.fleet | length')
    if [ "$turns" = "2" ] && [ "$fleet_rows" = "2" ]; then
        pass "/api/fleet attributed 2 turns to ratz and excluded the turn with no agent_name (2 rows: ratz + someone-else)"
    else
        fail "/api/fleet: ratz turn_count=$turns (want 2), row count=$fleet_rows (want 2 — one per agent, the unattributable turn excluded)"
    fi

    # A10. The honesty constraint, asserted against the wire format rather than
    # the struct: no key in any fleet row may claim liveness.
    got=$(uiapi_json '/api/fleet' '[.fleet[] | keys[]] | unique | map(select(. == "alive" or . == "online" or . == "running" or . == "status")) | length')
    if [ "$got" = "0" ]; then
        pass "/api/fleet made no liveness claim (no alive/online/running/status key), which turn_finished cannot support"
    else
        fail "/api/fleet exposed $got liveness-claiming key(s) — the log cannot tell a retired agent from a wedged one"
    fi

    # A11. THE QUM-1247 GATE, and the reason this row exists. The seed makes the
    # two answers different numbers: summing turn_finished.cost_usd gives 0.40,
    # the session's run_finished total is 0.30.
    local cost
    cost=$(uiapi_json '/api/usage?bucket=day&project_id=a0000000-0000-0000-0000-000000000001' \
        '.usage[0].cost_usd')
    if [ "$cost" = "0.3" ]; then
        pass "/api/usage billed the run_finished session total (0.30), not the cumulative turn sum (0.40) — QUM-1247"
    else
        fail "/api/usage reported cost_usd=$cost, want 0.3. A value of 0.4 means per-turn costs are being summed, which overstates spend 4-10x (QUM-1247)"
    fi

    # A12. The bucket is a closed set, checked before it reaches date_trunc.
    uiapi_get '/api/usage?bucket=week'
    if [ "$UIAPI_STATUS" = "400" ]; then
        pass "/api/usage?bucket=week was refused with 400 rather than silently answered by the day"
    else
        fail "/api/usage?bucket=week returned HTTP $UIAPI_STATUS, want 400"
    fi

    # A13. Registered as "GET /path", so a write verb is refused by the mux
    # itself and no handler runs. This is a read-only service.
    local post_status
    post_status=$(curl -sS -m 20 -o /dev/null -w '%{http_code}' -X POST \
        "http://127.0.0.1:$UIAPI_PORT/api/events" 2>/dev/null || true)
    if [ "$post_status" = "405" ]; then
        pass "POST /api/events was refused with 405 — the read API answers reads only"
    else
        fail "POST /api/events returned HTTP $post_status, want 405"
    fi

    # A14. This surface is unauthenticated in v1 and a pgx error names tables,
    # columns and roles. Asserted over every endpoint's body, including the 400s
    # above, because an error path is where detail escapes.
    local leaked=0 path
    for path in /api/events /api/goals /api/inbox /api/workflows /api/fleet /api/usage \
        '/api/events?limit=0' '/api/usage?bucket=week' '/api/goals?project_id=nope'; do
        uiapi_get "$path"
        if printf '%s' "$UIAPI_BODY" | grep -qiE 'sprawl_ui_login|sprawl_ro|relation "|SQLSTATE|pgx:|event_type_schemas'; then
            echo "  LEAK on $path: $(printf '%s' "$UIAPI_BODY" | head -c 200)"
            leaked=$((leaked + 1))
        fi
    done
    if [ "$leaked" = "0" ]; then
        pass "no response body carried a role name, table name or driver error across 9 endpoints and error paths"
    else
        fail "$leaked response body/bodies leaked database detail on an unauthenticated surface"
    fi

    e2e_print_results
}
