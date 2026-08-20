#!/usr/bin/env bash
# scripts/e2e-tests/store-degraded.sh — QUM-1249 (M1a) AC5: the event log is
# enabled and the database is unreachable.
#
# Boots the TUI in a sandbox with `event_log.enabled: true` and a DSN pointing at
# a refused port, drives one real weave turn, and asserts the three things AC5
# asks for:
#
#   1. THE AGENT KEEPS RUNNING. This is the load-bearing assertion. Everything
#      else in the store is worth nothing if enabling it can wedge an agent when
#      the database goes away, and a unit test cannot establish it — the failure
#      mode is a real subprocess hanging on a real dial timeout.
#   2. Telemetry SPILLS to .sprawl/logs/ledger-spill/ rather than vanishing.
#   3. `sprawl store doctor` REPORTS the outage rather than looking healthy, and
#      does not print the DSN while doing it.
#
# WHAT THIS ROW DOES NOT COVER, stated because AC5 names it and a reader will
# look for it: the "goal-open fails loudly" leg. M1a ships no product surface
# that opens a goal — no CLI command and no MCP tool — because goal semantics are
# M3. There is therefore nothing for an e2e row to drive. That leg is asserted at
# the unit level instead (internal/store: TestDegraded_GoalOpenFailsLoudlyWithAHint,
# TestDegraded_GoalCloseAlsoFailsLoudly, TestOpen_UnreachableDatabase...), and it
# should get an e2e leg here when M3 adds the goal tool. Do not read a green run
# of this row as covering it.
#
# Needs no Docker: a refused port IS the unreachable database, which makes this
# row cheaper and more deterministic than one that stops a container.

# QUM-1029: the number of assertions a COMPLETE, PASSING run of this row makes.
# Hand-counted: TUI rendered, turn completed, spill dir exists, spill file found,
# >=1 telemetry record, record carries a reason, record is a telemetry type (not
# a contract type), doctor reports degraded, doctor names the DSN source, doctor
# does not leak the DSN, agent still alive after the outage.
MIN_ASSERTIONS=11

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

# A DSN whose host refuses immediately. connect_timeout=1 keeps a genuine
# regression (a per-append dial retry) from turning this row into a 10-minute
# hang instead of a failure.
UNREACHABLE_DSN='postgres://nobody:nothing@127.0.0.1:1/nosuchdb?sslmode=disable&connect_timeout=1'

test_run() {
    unset SPRAWL_AGENT_IDENTITY

    e2e_recover_oauth_token
    e2e_setup_tmux_socket "sprawl-store-degraded-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum1249-degraded"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    if ! command -v jq >/dev/null 2>&1; then
        fail "jq is required to validate spill records"
        return 1
    fi

    # Enable the store in the sandbox's project config. The DSN deliberately
    # does NOT go here — .sprawl/config.yaml is a tracked file in a public repo,
    # and the store refuses to read a DSN from it at all.
    printf 'event_log.enabled: "true"\n' >> "$SPRAWL_ROOT/.sprawl/config.yaml"
    echo "  config: event_log.enabled=true"
    echo "  dsn:    <refused port> (value withheld: it is a credential shape)"

    local SESSION="sprawl-store-degraded-$(head -c4 /dev/urandom | xxd -p)"
    local SPILL_DIR="$SPRAWL_ROOT/.sprawl/logs/ledger-spill"

    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo ""

    echo "=== Launching sprawl enter with an unreachable event log ==="
    if ! e2e_launch_tui "$SESSION" 200 50 "SPRAWL_DB_DSN='$UNREACHABLE_DSN'"; then
        fail "the TUI did not come up with the store enabled and the database unreachable — enabling the store must never prevent a session from starting"
        return 1
    fi
    pass "TUI rendered with the store enabled and the database unreachable"

    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION"
    sleep 2

    echo ""
    echo "=== Driving one weave turn (the agent must be unaffected) ==="
    e2e_send_user_prompt "$SESSION" "say hi in three words"

    # THE LOAD-BEARING ASSERTION. A store that blocks on an unreachable database
    # shows up here and only here.
    if wait_for_pattern "$SESSION" "Completed in" 120; then
        pass "weave completed a turn with the event log unreachable (agents do not brick on the store)"
    else
        fail "weave did not complete a turn within 120s with the event log unreachable — the store is blocking the agent"
        capture_pane "$SESSION" | tail -40 >&2
        echo "  stderr tail:" >&2
        tail -30 "$SPRAWL_ROOT/.sprawl/tui-stderr.log" 2>/dev/null >&2 || true
        return 1
    fi

    # Let the ledger subscriber flush.
    sleep 3

    echo ""
    echo "=== Asserting telemetry spilled rather than vanishing ==="
    if [ -d "$SPILL_DIR" ]; then
        pass "spill directory exists: $SPILL_DIR"
    else
        fail "no spill directory at $SPILL_DIR — telemetry was silently DROPPED, which is the one outcome degraded mode forbids"
        # Diagnostics on THIS path specifically. A missing spill has several
        # indistinguishable causes — the flag not read, no Ledger built, the
        # emitter not wired, the spill path wrong — and without these a failure
        # here costs a whole re-run to localise.
        {
            echo "  --- .sprawl/logs ---"
            ls -la "$SPRAWL_ROOT/.sprawl/logs/" 2>&1 || true
            echo "  --- .sprawl/config.yaml (the flag) ---"
            cat "$SPRAWL_ROOT/.sprawl/config.yaml" 2>&1 || true
            echo "  --- store status as the session saw it ---"
            (cd "$SPRAWL_ROOT" && SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_DB_DSN="$UNREACHABLE_DSN" "$SPRAWL_BIN" store status 2>&1) || true
            echo "  --- tui stderr: store/event-log lines ---"
            grep -iE "store|event log|ledger|spill|remote" "$SPRAWL_ROOT/.sprawl/tui-stderr.log" 2>/dev/null | tail -20 || true
        } >&2
        return 1
    fi

    local SPILL_FILE
    SPILL_FILE=$(find "$SPILL_DIR" -maxdepth 1 -name '*.ndjson' -type f 2>/dev/null | head -1)
    if [ -n "$SPILL_FILE" ]; then
        pass "found spill file: $SPILL_FILE"
    else
        fail "no .ndjson spill file under $SPILL_DIR"
        ls -la "$SPILL_DIR" >&2 || true
        return 1
    fi

    local RECORDS
    RECORDS=$(grep -c '^{' "$SPILL_FILE" 2>/dev/null || true)
    [ -n "$RECORDS" ] || RECORDS=0
    if [ "$RECORDS" -ge 1 ]; then
        pass "spill file holds $RECORDS record(s)"
    else
        fail "spill file $SPILL_FILE holds no records"
        return 1
    fi

    # A spilled record without a reason cannot be triaged on replay: a transient
    # outage and a permanent rejection would look identical.
    local REASON
    REASON=$(jq -r 'select(.reason != null and .reason != "") | .reason' "$SPILL_FILE" 2>/dev/null | head -1)
    if [ -n "$REASON" ]; then
        pass "spilled record carries a reason: ${REASON:0:60}"
    else
        fail "no spilled record carries a non-empty reason — a replay cannot tell a transient outage from a permanent rejection"
        head -3 "$SPILL_FILE" >&2 || true
    fi

    # The spill must contain TELEMETRY and must NOT contain a contract type.
    # This is the discriminating assertion: a store that spilled everything
    # would satisfy every leg above while making goals invisible to other hosts.
    local NAMES TELEMETRY CONTRACTS
    NAMES=$(jq -r '.schema_name' "$SPILL_FILE" 2>/dev/null | sort -u | tr '\n' ' ')
    TELEMETRY=$(jq -r 'select(.schema_name == "run_started" or .schema_name == "turn_finished" or .schema_name == "run_finished") | .schema_name' "$SPILL_FILE" 2>/dev/null | wc -l)
    CONTRACTS=$(jq -r 'select(.schema_name == "goal_opened" or .schema_name == "goal_closed") | .schema_name' "$SPILL_FILE" 2>/dev/null | wc -l)
    if [ "$TELEMETRY" -ge 1 ] && [ "$CONTRACTS" -eq 0 ]; then
        pass "spill holds telemetry only (types: $NAMES) — contract events are refused, not spilled"
    else
        fail "spill composition is wrong: $TELEMETRY telemetry record(s), $CONTRACTS contract record(s) (types: $NAMES). A contract recorded only in a local file is invisible to every other host"
    fi

    echo ""
    echo "=== Asserting store doctor reports the outage ==="
    local DOCTOR
    DOCTOR=$(cd "$SPRAWL_ROOT" && SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_DB_DSN="$UNREACHABLE_DSN" "$SPRAWL_BIN" store doctor 2>&1 || true)
    echo "$DOCTOR" | sed 's/^/    /'

    if echo "$DOCTOR" | grep -qiE '^connection:.*(degraded|unreachable)'; then
        pass "store doctor reports the connection as degraded"
    else
        fail "store doctor did not report a degraded connection; a host whose every event is spilling must not read as healthy"
    fi

    if echo "$DOCTOR" | grep -q 'SPRAWL_DB_DSN'; then
        pass "store doctor names the DSN source (localises the misconfiguration)"
    else
        fail "store doctor does not say where the DSN came from"
    fi

    # Public-repo hygiene, asserted on real output: the password must not appear.
    # `nothing` is the password in UNREACHABLE_DSN.
    if echo "$DOCTOR" | grep -qF 'nobody:nothing'; then
        fail "store doctor printed the DSN credentials — this repo is PUBLIC and terminal transcripts get pasted into issues"
    else
        pass "store doctor did not print the DSN credentials"
    fi

    echo ""
    echo "=== Asserting the agent is still alive after the outage ==="
    e2e_send_user_prompt "$SESSION" "say bye in two words"
    if wait_for_pattern "$SESSION" "Completed in" 120; then
        pass "weave completed a SECOND turn with the event log still unreachable"
    else
        fail "weave could not complete a second turn — the store degrades progressively rather than staying out of the way"
        capture_pane "$SESSION" | tail -30 >&2
    fi

    e2e_print_results
}
