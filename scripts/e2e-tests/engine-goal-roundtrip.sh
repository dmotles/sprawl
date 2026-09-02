#!/usr/bin/env bash
# scripts/e2e-tests/engine-goal-roundtrip.sh — QUM-1252 (M3a): a RESEARCH goal
# opened by weave becomes a real agent, with no human in the loop.
#
# This is the row for the engine's START LEG, end to end, against a real
# Postgres and a real `sprawl enter` session:
#
#   create_goal  ->  goal_opened  ->  (dispatcher)  spawn_requested
#                ->  (session dispatcher)  spawn_intent -> a real worktree
#                ->  spawn_committed
#
# WHY IT CANNOT BE A GO TEST. Every unit test in this slice wires its own
# handler table, so a handler registered for the wrong event type — or, since
# QUM-1252, one registered on a path that has no supervisor — is green there and
# broken here. Only a real `sprawl enter` assembles the production stack, and
# only a real Postgres makes the claim machinery load-bearing.
#
# ===========================================================================
# WHAT IS ASSERTED, AND THE ONE THING THAT WOULD OTHERWISE GO UNMEASURED
# ===========================================================================
#
# The chain assertion is that a spawn_intent, a spawn_committed and a real agent
# follow the request. Its control was RUN AND FIRED: forcing dispatchSpawner to
# return nil, so no spawn handler is registered on the session path, fails the
# row with "no spawn_intent appeared within 120s". That is the defect this row
# exists for — a wiring hole no unit test sees, because every unit test builds
# its own handler table.
#
# The row also reads the agent name out of the spawn_requested PAYLOAD and
# requires a state file at exactly that name, rather than "some agent appeared":
# a divergence there leaves the intent matching nothing and, past the
# reconciler's grace period, produces spawn_failed for a live agent.
#
# BUT THAT ASSERTION DOES NOT PROVE THE NAME WAS PINNED, and the header says so
# because the control was run and did NOT fire. Building with the pinned branch
# of resolveSpawnName disabled — every spawn allocating locally — left this row
# at 17 passed / 0 failed, exit 0. Both sides read the same partitioned pool
# from the same agents dir, so a local allocation lands on the same name the
# dispatcher's PoolNamer chose; agreement here is structural, not evidence, and
# no in-row observable separates the two. Pinning is covered at the unit level
# (agentops.TestPrepareSpawnAs_*, agent.TestReserveName), each with its own
# recorded control.
#
# The second such assertion is the PARENT. The goal's owner must become the
# spawned agent's parent, and the supervisor derives the parent from the caller
# identity rather than from a request field. Get that wrong and the agent is
# parented to whoever ran the dispatcher; it still spawns, still works, and its
# result notification goes to the wrong agent.
#
# NOT asserted here: the CLOSE leg (report_result -> goal_closed -> the owner's
# notification). It depends on a researcher model choosing to call a tool, which
# is a behavioural wait of unbounded length, and folding it in would make a
# deterministic wiring row flake on model mood. It is a separate row. A green run
# here is evidence the goal STARTED, and is not evidence it can finish.
#
# Docker-gated, same shape as the sibling store rows: a container this row
# creates and reaps itself, on a random name and a random host port, so
# concurrent agents on one host do not collide.
#
# WHAT THE MODEL ASSERTION DOES NOT ESTABLISH (QUM-1337). It pins that the
# engine-spawned researcher launches on `--model opus`. Its control was RUN AND
# FIRED: `model: sonnet` in internal/card/seeds/slim-researcher.md fails the row
# with "the researcher's command line does not carry '--model opus' (got: …
# --model sonnet …)" at 17 passed / 1 failed, which also shows the card IS being
# read at launch on the tree that control was run against. It does NOT
# distinguish "the card was consulted" from "the card lookup failed and the
# compiled-in default was used", because rootinit.DefaultAgentModel is ALSO
# opus — so a regression that silently stops reading cards passes this
# assertion. Stated rather than glossed: the failure scenario QUM-1337 opens
# with is exactly that regression, and this row narrows it without closing it.
# Closing it needs a researcher card whose model differs from the default, which
# is a seed decision outside this row.

# QUM-1029: the number of assertions a COMPLETE, PASSING run of this row makes.
# Hand-counted: container ready, migrate ok, TUI rendered, create_goal turn
# completed, goal_opened present, goal_opened owned by weave, spawn_requested
# present, spawn_requested names a researcher, its branch is a goal/ branch,
# spawn_intent present, intent carries the same name, spawn_committed present,
# the state file exists AT THE LOG'S NAME, its parent is weave, its branch
# matches the log's branch, the researcher launched on its card's model
# (QUM-1337), the goal contract is still open, and nothing spilled.
#
# 17 before QUM-1337 added the model assertion.
MIN_ASSERTIONS=18

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

PG_CONTAINER=""
PG_NAME_PREFIX="sprawl-qum1252-pg-"

# reap_pg removes this row's container and then runs the harness cleanup.
#
# CHAINED, NOT REPLACED, and the exit status is carried across by hand — see the
# long note on the same function in store-lifecycle-live.sh, which was written
# from a measured false-green: _e2e_cleanup re-exits with whatever $? it
# observes, so an unguarded `docker rm ... || true` turns a FAILING row into a
# passing one.
reap_pg() {
    local rc=$?
    if [ -n "$PG_CONTAINER" ]; then
        docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
    fi
    ( exit "$rc" )
    _e2e_cleanup
}

# reap_stale_pg cleans up after a run that was SIGKILLed before its trap fired.
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

# wait_for_event polls until at least one event of the named type exists, up to
# a deadline, and echoes the final count. Polling rather than sleeping because
# the whole chain is asynchronous: the dispatcher's poll is 2s, but the turn that
# precedes it is a model.
wait_for_event() {
    local type_name="$1" deadline="$2" waited=0 n=""
    while [ "$waited" -lt "$deadline" ]; do
        n=$(psql_q "SELECT count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = '$type_name';")
        if [ -n "$n" ] && [ "$n" -ge 1 ] 2>/dev/null; then
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
    # Skipped rather than failed: without pgrep the model assertion below cannot
    # resolve the researcher's process, and the row would fail red pointing at
    # the product for a missing host tool.
    if ! command -v pgrep >/dev/null 2>&1; then
        e2e_skip_row "pgrep not found on PATH — the model assertion cannot resolve the researcher's claude process"
        return
    fi

    e2e_recover_oauth_token
    e2e_setup_tmux_socket "sprawl-engine-goal-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum1252-goal"
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

    echo ""
    echo "=== Applying event-log migrations ==="
    if (cd "$SPRAWL_ROOT" && SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_DB_DSN="$DSN" "$SPRAWL_BIN" store migrate); then
        pass "store migrate applied the schema"
    else
        fail "store migrate failed"
        return 1
    fi

    local SESSION="sprawl-engine-goal-$SUFFIX"
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
    # create_goal is LOG-DRIVEN: it appends goal_opened and returns. Nothing is
    # running when the turn ends, which is exactly why the assertions below are
    # about the DATABASE and the FILESYSTEM rather than about the pane — the pane
    # can only show that weave called a tool.
    e2e_send_user_prompt "$SESSION" \
        "Use the create_goal tool to open one goal with goal_type RESEARCH and the text 'Summarize what the README says this project does.'. Do not spawn anything yourself and do not do anything else."
    if wait_for_pattern "$SESSION" "Completed in" 240; then
        pass "weave completed the create_goal turn"
    else
        fail "weave did not complete the create_goal turn within 240s"
        capture_pane "$SESSION" | tail -40 >&2
        return 1
    fi

    echo ""
    echo "=== The log's side of the chain ==="
    local GOALS
    if GOALS=$(wait_for_event goal_opened 60); then
        pass "a goal_opened event is in the log ($GOALS)"
    else
        fail "no goal_opened event appeared within 60s (got '$GOALS') — create_goal did not append, so nothing downstream can be measured"
        psql_q "SELECT s.name, count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id GROUP BY s.name;" >&2 || true
        return 1
    fi

    # The OWNER is what becomes the spawned agent's parent further down. Asserted
    # here as well as on the state file so a mismatch downstream can be
    # attributed: same value, two ends of the chain.
    local GOAL_OWNER
    GOAL_OWNER=$(psql_q "SELECT e.payload->>'owner' FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'goal_opened' ORDER BY e.seq LIMIT 1;")
    echo "    goal owner: $GOAL_OWNER"
    if [ "$GOAL_OWNER" = "weave" ]; then
        pass "the goal records weave as its owner"
    else
        fail "the goal's owner is '$GOAL_OWNER', want weave — the spawned agent would be parented to the wrong agent and its result would be reported to it"
    fi

    # The dispatcher's leg. 120s rather than a handful of polls: the dispatcher
    # polls every 2s, but it is competing with a live session's own work.
    local REQUESTS
    if REQUESTS=$(wait_for_event spawn_requested 120); then
        pass "the dispatcher turned the goal into a spawn_requested ($REQUESTS)"
    else
        fail "no spawn_requested event appeared within 120s (got '$REQUESTS') — goal_opened was appended but nothing handled it, which is the quietest failure this loop has: the event is scanned, skipped, and the cursor advances"
        psql_q "SELECT s.name, count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id GROUP BY s.name;" >&2 || true
        return 1
    fi

    # EVERYTHING BELOW HANGS OFF THIS NAME. It is read out of the log, and the
    # filesystem is then required to match it — not the other way around.
    local LOG_NAME LOG_TYPE LOG_BRANCH
    LOG_NAME=$(psql_q "SELECT e.payload->>'agent_name' FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'spawn_requested' ORDER BY e.seq LIMIT 1;")
    LOG_TYPE=$(psql_q "SELECT e.payload->>'agent_type' FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'spawn_requested' ORDER BY e.seq LIMIT 1;")
    LOG_BRANCH=$(psql_q "SELECT e.payload->>'branch' FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'spawn_requested' ORDER BY e.seq LIMIT 1;")
    echo "    the log asked for: name=$LOG_NAME type=$LOG_TYPE branch=$LOG_BRANCH"

    if [ "$LOG_TYPE" = "researcher" ]; then
        pass "a RESEARCH goal asked for a researcher"
    else
        fail "the request names agent_type '$LOG_TYPE', want researcher — a RESEARCH goal put the wrong kind of agent on the work, which looks exactly like success"
    fi

    case "$LOG_BRANCH" in
        goal/*)
            pass "the request carries a goal/ branch ($LOG_BRANCH)"
            ;;
        *)
            fail "the request's branch is '$LOG_BRANCH', want a goal/<agent>-<id> branch — an empty or malformed branch is refused by the spawner, so the goal would stop here"
            ;;
    esac

    # The write-ahead. Its ORDER is the mechanism (intent -> resource ->
    # committed), and its presence is what makes a crashed spawn reconcilable.
    local INTENTS
    if INTENTS=$(wait_for_event spawn_intent 120); then
        pass "the spawn write-ahead appended a spawn_intent ($INTENTS)"
    else
        fail "no spawn_intent appeared within 120s (got '$INTENTS') — either nothing consumed spawn_requested, or a spawn happened WITHOUT its write-ahead, which leaves an unattributable worktree"
        return 1
    fi

    # The intent must carry the SAME name the request did. This is the pairing
    # the reconciler depends on; a spawner that renamed in between would satisfy
    # every count above.
    local INTENT_NAME
    INTENT_NAME=$(psql_q "SELECT e.payload->>'agent_name' FROM events e JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'spawn_intent' ORDER BY e.seq LIMIT 1;")
    if [ -n "$LOG_NAME" ] && [ "$INTENT_NAME" = "$LOG_NAME" ]; then
        pass "the intent records the same agent name as the request ($INTENT_NAME)"
    else
        fail "the intent names '$INTENT_NAME' but the request named '$LOG_NAME' — the reconciler matches intents to local agents BY NAME, so a mismatch makes this intent match nothing forever"
    fi

    local COMMITS
    if COMMITS=$(wait_for_event spawn_committed 180); then
        pass "the spawn was committed to the log ($COMMITS)"
    else
        fail "no spawn_committed appeared within 180s (got '$COMMITS') — the intent is open, so the reconciler will eventually declare this spawn failed"
        psql_q "SELECT * FROM open_contracts;" >&2 || true
    fi

    echo ""
    echo "=== The filesystem must match the name the LOG chose ==="
    # Read the state file AT THE LOG'S NAME, deliberately not "some agent
    # appeared": a divergence between the log's name and the filesystem's is
    # what leaves a spawn_intent matching nothing, and past the reconciler's
    # grace period it produces spawn_failed for a live agent.
    #
    # WHAT THIS ASSERTION DOES NOT PROVE, because the control was RUN and did
    # NOT fire: it does not prove the name was PINNED. Building with
    # resolveSpawnName's pinned branch disabled (`pinnedName == "" || true`, so
    # every spawn allocates locally) left this row at 17 passed / 0 failed,
    # exit 0. Both sides read the same partitioned pool from the same agents
    # dir, so a local allocation picks the very name the dispatcher's PoolNamer
    # picked — agreement here is structural, not evidence. There is no in-row
    # observable that separates the two, since the divergence needs the pool
    # state to change between the request and the spawn. Pinning is covered at
    # the unit level instead (agentops.TestPrepareSpawnAs_* and
    # agent.TestReserveName, both with recorded controls).
    #
    # The control that DOES fire for the second half of this chain: forcing
    # dispatchSpawner to return nil (no spawn handler registered) fails the row
    # with "no spawn_intent appeared within 120s".
    local STATE_FILE="$SPRAWL_ROOT/.sprawl/agents/$LOG_NAME.json"
    local waited=0
    while [ "$waited" -lt 120 ]; do
        [ -f "$STATE_FILE" ] && break
        sleep 3
        waited=$((waited + 3))
    done
    if [ -n "$LOG_NAME" ] && [ -f "$STATE_FILE" ]; then
        pass "an agent exists at the log's name, so the intent matches a local agent: $LOG_NAME (after ${waited}s)"
    else
        fail "no state file at $SPRAWL_ROOT/.sprawl/agents/$LOG_NAME.json — the log named an agent that does not exist locally under that name; if some OTHER agent was created, the intent matches nothing and the reconciler will report a spawn_failed for a live agent"
        ls -la "$SPRAWL_ROOT/.sprawl/agents" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    # jq is not assumed: these are flat scalar fields and grep -o is enough,
    # which keeps this row's precondition set to docker + claude + tmux.
    local ST_PARENT ST_BRANCH
    ST_PARENT=$(grep -o '"parent"[[:space:]]*:[[:space:]]*"[^"]*"' "$STATE_FILE" | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
    ST_BRANCH=$(grep -o '"branch"[[:space:]]*:[[:space:]]*"[^"]*"' "$STATE_FILE" | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
    echo "    on disk: parent=$ST_PARENT branch=$ST_BRANCH"

    if [ "$ST_PARENT" = "$GOAL_OWNER" ]; then
        pass "the spawned agent is parented to the goal's owner ($ST_PARENT)"
    else
        fail "the agent's parent is '$ST_PARENT' but the goal's owner is '$GOAL_OWNER' — the caller identity did not reach the spawn, so this agent's result notification will be delivered, successfully, to the wrong agent"
    fi

    if [ -n "$LOG_BRANCH" ] && [ "$ST_BRANCH" = "$LOG_BRANCH" ]; then
        pass "the agent is on the branch the log asked for ($ST_BRANCH)"
    else
        fail "the agent is on branch '$ST_BRANCH' but the log asked for '$LOG_BRANCH'"
    fi

    # THE MODEL THE CARD NAMES (QUM-1337).
    #
    # Read off the LAUNCHED PROCESS, not off the state file: the engine pins no
    # `model` on spawn_requested (deliberately — see goalspawn.go), so the state
    # file has no model field at all and asserting on it would assert nothing.
    # `--model` on the child's own command line is the end of the chain and the
    # only place the resolved answer is observable.
    #
    # PID recipe is engine-goal-stall-poke's `pids_for`, not
    # death-observability.sh's: match the SESSION ID and then require
    # /proc/<pid>/comm == claude, and take every match. A bare `pgrep -af claude`
    # matches harness subshells and killed the harness in 3 of 4 runs (QUM-1334).
    local ST_SESSION MODEL_PIDS CMDLINE p
    ST_SESSION=$(grep -o '"session_id"[[:space:]]*:[[:space:]]*"[^"]*"' "$STATE_FILE" | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
    CMDLINE=""
    if [ -n "$ST_SESSION" ]; then
        MODEL_PIDS=$(pgrep -f -- "$ST_SESSION" 2>/dev/null || true)
        for p in $MODEL_PIDS; do
            if [ "$(cat "/proc/$p/comm" 2>/dev/null || true)" = "claude" ]; then
                CMDLINE=$(tr '\0' ' ' < "/proc/$p/cmdline" 2>/dev/null || true)
                break
            fi
        done
    fi
    echo "    the researcher's claude was launched as: ${CMDLINE:-<not resolved>}"
    # RESEARCHER_CARD_MODEL is the `model:` line of internal/card/seeds/
    # slim-researcher.md, hard-coded rather than read back from the card so that
    # editing the card and re-running is a control that FIRES. Reading the
    # expectation from the same card the product reads would make the two move
    # together and assert nothing.
    if printf '%s' "$CMDLINE" | grep -q -- "--model opus"; then
        pass "the engine-spawned researcher launched on the model its card names (opus)"
    else
        fail "the researcher's command line does not carry '--model opus' (got: ${CMDLINE:-<no claude process resolved for session $ST_SESSION>}) — a card-lookup regression downgrades every engine-spawned researcher silently, and the only symptom is worse research"
    fi

    # The goal contract is STILL OPEN, and that is the correct state: nothing has
    # closed it, because this row deliberately stops before report_result. It is
    # also the negative control for the close leg — a goal that closed itself
    # here would mean something is closing contracts nobody reported on.
    local OPEN_GOALS
    OPEN_GOALS=$(psql_q "SELECT count(*) FROM open_contracts oc JOIN events e ON e.id = oc.event_id JOIN event_type_schemas s ON s.id = e.schema_id WHERE s.name = 'goal_opened';")
    echo "    open goal contracts: $OPEN_GOALS"
    if [ -n "$OPEN_GOALS" ] && [ "$OPEN_GOALS" -ge 1 ] 2>/dev/null; then
        pass "the goal contract is still open, as it must be until the researcher reports ($OPEN_GOALS)"
    else
        fail "no goal_opened contract is open (got '$OPEN_GOALS') — either the projection did not record it, or something closed a goal that nobody reported a result for"
        psql_q "SELECT * FROM open_contracts;" >&2 || true
    fi

    # The negative control for the whole row: with the database reachable
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
