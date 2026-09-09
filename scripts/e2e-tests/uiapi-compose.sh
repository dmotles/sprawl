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
# WHY `web` IS BROUGHT UP (QA F2, and a correction).
# This header used to argue that `web` was deliberately NOT started, because it
# "is not a dependency of the API" and building it "would make this row fail for
# reasons in someone else's slice". That reasoning is exactly what let the worst
# defect of the slice ship: Contract Amendment #3 reshaped the /api/fleet and
# /api/usage envelopes after the UI was written, the UI kept reading the old
# keys, and BOTH sides were green — the Go suite against a pgx.Rows fake, the
# webui suite against hand-written fakes carrying the superseded shapes. Nothing
# compared the two, so the browser got `undefined`, FleetView threw during
# render, and with no error boundary React unmounted the whole application. Two
# of the six required views were a blank page and every gate said PASS.
#
# A row that stops at the API cannot see that class of defect, and neither can
# any number of unit tests on either side: the contract is the thing under test,
# and it only exists where the two meet. So the row now drives a REAL BROWSER
# against nginx and asserts each view renders a string that can only have come
# from the seed. "It would fail for reasons in someone else's slice" was the
# argument against; that a cross-slice break has nowhere else to be caught is
# the argument for, and it is the stronger one.
#
# The browser is the host chromium driven with --dump-dom, not a new npm
# dependency: the assertion needs a rendered DOM, not a test framework, and
# adding playwright to slice B'"'"'s package.json to get one would be a large,
# permanent cost for a single gate.
#
# The override publishes a loopback port on BOTH uiapi (so the API assertions
# stay direct, and an API failure is not misreported as a proxy failure) and web
# (ports: !override, because the tracked file binds a fixed 8080 that would
# collide between concurrent agents).
#
# Needs Docker, `docker compose`, curl, jq, python3 and chromium. No claude and
# no tmux.

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
# Then the browser half (QA F2) — each renders THROUGH nginx, in chromium,
# against the live stack, and each needle can only have come from the seed:
#
#   A15 /api/events carries project_name through the proxy   <- the QA F3 gate
#   A16 /goals renders the seeded goal type
#   A17 /workflows renders BOTH derived states
#   A18 /fleet renders the seeded agent
#   A19 /ledger renders the seeded event type
#   A20 /usage renders the run-total spend                   <- QUM-1247, in the UI
#   A21 /inbox renders the seeded question text
#
# Update this number in the same commit as any change to that list. It does not
# self-adjust, and a floor above what a passing run asserts turns an honest run
# red.
MIN_ASSERTIONS=21

# Deadline for the API to answer /healthz, covering `docker compose up --build`
# on a cold cache (the Go build plus a Postgres first-boot). Generous on purpose:
# a timeout here is a row failure, and a flaky one is worse than a slow one.
UIAPI_READY_TIMEOUT=${SPRAWL_E2E_UIAPI_READY_TIMEOUT:-300}

# Deadline for a single view to render its seeded data in the browser.
UIAPI_RENDER_TIMEOUT=${SPRAWL_E2E_UIAPI_RENDER_TIMEOUT:-60}

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

# uiapi_render PATH publishes the rendered DOM as UIAPI_DOM.
#
# --dump-dom prints the DOM *after* scripts have run, which is the whole point:
# these views are a client-side SPA, so curl through nginx returns an empty
# shell and would assert nothing about whether the view works. Verified before
# this was written: a page whose text is set by JS dumps as the JS-set value.
#
# A global, not stdout, for the reason recorded on uiapi_get: a `dom=$(...)`
# call site runs in a subshell.
#
# --user-data-dir keeps the profile inside this row's tmpdir so concurrent rows
# (and the operator's own browser) do not share state. --no-sandbox is required
# to run as root in CI containers.
UIAPI_DOM=""
uiapi_render() {
    UIAPI_DOM=$(timeout 90 chromium --headless --no-sandbox --disable-gpu \
        --disable-dev-shm-usage --dump-dom --virtual-time-budget=15000 \
        --user-data-dir="$UIAPI_TMPDIR/chrome" \
        "http://127.0.0.1:$WEB_PORT$1" 2>/dev/null || true)
}

# uiapi_view_renders PATH NEEDLE [NEEDLE...] — 0 when every needle is present in
# the rendered DOM, 1 otherwise. Retries until UIAPI_RENDER_TIMEOUT, because a
# first paint has to fetch its data over the network and a fixed budget that is
# generous enough never to flake is also generous enough to make the row slow.
#
# On failure it prints the DOM's size and its <main> text, which is what
# distinguishes the two failures that matter: a SHELL-ONLY dump (React threw and
# unmounted the application — the F1 signature, ~450 bytes with no nav) from a
# rendered view that is missing the data.
uiapi_view_renders() {
    local path="$1"; shift
    local waited=0 needle missing
    while :; do
        uiapi_render "$path"
        missing=""
        for needle in "$@"; do
            case "$UIAPI_DOM" in
                *"$needle"*) ;;
                *) missing="$needle" ;;
            esac
        done
        [ -z "$missing" ] && return 0
        [ "$waited" -ge "$UIAPI_RENDER_TIMEOUT" ] && break
        sleep 3
        waited=$((waited + 3))
    done
    echo "  (GET $path rendered ${#UIAPI_DOM} bytes without $missing)" >&2
    echo "  (a dump of only a few hundred bytes with no nav means React threw and unmounted the app)" >&2
    printf '%s' "$UIAPI_DOM" | tr -d '\n' | grep -o '<main.*</main>' | head -c 600 >&2 || true
    echo >&2
    return 1
}

uiapi_psql() {
    uiapi_compose exec -T -e PGPASSWORD=sprawl_dev_password db \
        psql -v ON_ERROR_STOP=1 -U sprawl_owner -d sprawl -qtAX "$@"
}

uiapi_teardown() {
    if [ -n "${UIAPI_PROJECT:-}" ]; then
        echo "== tearing down $UIAPI_PROJECT =="
        uiapi_compose logs --no-color --tail 40 uiapi web 2>/dev/null || true
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
    # chromium is a hard precondition, not an optional extra: without it the
    # browser half is exactly the coverage whose absence let QA F1 ship, and a
    # row that quietly drops it would report a green that means less than it did
    # before. Skipping the whole row (77) is the honest outcome.
    if ! command -v chromium >/dev/null 2>&1; then
        e2e_skip_row "chromium not found on PATH — this row renders each view in a real browser, and the API half alone cannot see a UI/API contract break"
    fi
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
    WEB_PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
    if [ -z "$UIAPI_PORT" ] || [ -z "$WEB_PORT" ] || [ "$UIAPI_PORT" = "$WEB_PORT" ]; then
        fail "could not reserve two distinct local ports"
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
  # !override, not an append: the tracked file binds a FIXED 8080 on all
  # interfaces, which collides between concurrent agents on one host and would
  # publish this fixture off-box. Compose merges port lists by appending unless
  # told otherwise, so without the tag both bindings survive.
  web:
    ports: !override
      - "127.0.0.1:$WEB_PORT:8080"
YAML

    echo "-- bringing up db, migrate, grant, uiapi (:$UIAPI_PORT) and web (:$WEB_PORT) in project $UIAPI_PROJECT"
    if ! uiapi_compose up -d --build db migrate grant uiapi web >"$UIAPI_TMPDIR/up.log" 2>&1; then
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

    # ------------------------------------------------------------------
    # The browser half. Everything above proves the API; nothing above can
    # prove the API and the UI agree, which is the defect class QA F2 found.
    # ------------------------------------------------------------------

    # Wait for nginx before blaming a view for a stack that is not up yet.
    local web_waited=0 web_up=0 web_status
    while [ "$web_waited" -lt "$UIAPI_READY_TIMEOUT" ]; do
        web_status=$(curl -sS -m 10 -o /dev/null -w '%{http_code}' \
            "http://127.0.0.1:$WEB_PORT/goals" 2>/dev/null || echo 000)
        if [ "$web_status" = "200" ]; then
            web_up=1
            break
        fi
        sleep 2
        web_waited=$((web_waited + 2))
    done
    if [ "$web_up" != "1" ]; then
        fail "nginx never served the SPA within ${UIAPI_READY_TIMEOUT}s (last status $web_status) — the browser assertions below were NOT measured"
        e2e_print_results
        return
    fi

    # A15. The proxy path, and QA F3: a ledger row that names only a uuid is
    # unattributable, and the ledger deliberately shows every project at once.
    # Asserted through nginx rather than against uiapi directly, so it also
    # proves /api survives the reverse proxy the browser is required to use.
    #
    # The assertion is over the SET of names, not over the newest row: the
    # ledger is deliberately all-projects and newest-first, so row 0 belongs to
    # whichever project wrote last. (Measured — the first version of this gate
    # asserted [0] and failed with 'widget', which was the correct answer to the
    # wrong question.) Requiring both names also makes it a real attribution
    # check: a hard-coded label would give one name for every row.
    local proxied_names
    proxied_names=$(curl -sS -m 20 "http://127.0.0.1:$WEB_PORT/api/events?limit=50" 2>/dev/null |
        jq -r '[.events[].project_name] | unique | join(",")' 2>/dev/null || true)
    if [ "$proxied_names" = "sprawl,widget" ]; then
        pass "/api/events names both projects through nginx (sprawl,widget) — a ledger row is attributable"
    else
        fail "/api/events through nginx gave project_name set [$proxied_names], want 'sprawl,widget' — the ledger's PROJECT column renders an em dash without it"
    fi

    # A16-A21. Each needle can only have come from the seed, so none of them can
    # be satisfied by the static shell nginx serves before React runs.
    if uiapi_view_renders /goals "ship-slice-c"; then
        pass "/goals renders the seeded goal type in a real browser"
    else
        fail "/goals did not render the seeded goal type"
    fi

    # Both states, because a workflows view that hard-coded either one would
    # pass a single-needle assertion while deriving nothing.
    if uiapi_view_renders /workflows "in flight" "settled"; then
        pass "/workflows renders BOTH derived states (in flight and settled)"
    else
        fail "/workflows did not render both derived states"
    fi

    if uiapi_view_renders /fleet "ratz"; then
        pass "/fleet renders the seeded agent name in a real browser"
    else
        fail "/fleet did not render the seeded agent — the envelope key or the row type does not match what the API serves"
    fi

    if uiapi_view_renders /ledger "run_finished" "sprawl"; then
        pass "/ledger renders the seeded event type and its project name"
    else
        fail "/ledger did not render the seeded event type and project name"
    fi

    # The QUM-1247 value, all the way to the pixel: 0.30 is the run total and
    # 0.40 is the per-turn sum, so a UI that renders the wrong one fails here on
    # the NUMBER rather than on a shape.
    if uiapi_view_renders /usage '$0.30'; then
        pass "/usage renders \$0.30, the run_finished session total (not \$0.40, the cumulative turn sum)"
    else
        fail "/usage did not render \$0.30 — either the view is blank or it is billing the turn sum"
    fi

    if uiapi_view_renders /inbox "Is the contract final?"; then
        pass "/inbox renders the seeded question text in a real browser"
    else
        fail "/inbox did not render the seeded question"
    fi

    e2e_print_results
}
