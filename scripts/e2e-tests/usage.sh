#!/usr/bin/env bash
# scripts/e2e-tests/usage.sh — QUM-368 per-turn token usage capture regression
# guard.
#
# Boots the TUI in the sandbox, drives one weave turn, and asserts the usage
# NDJSON log at .sprawl/logs/usage/weave/<session_id>.ndjson contains at least
# one well-formed record with all 13 required schema keys.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

# Required schema keys per QUM-368 (always emitted, never omitempty).
USAGE_REQUIRED_KEYS=(
    timestamp
    agent_name
    agent_type
    agent_family
    parent_name
    session_id
    branch
    model
    input_tokens
    output_tokens
    cache_read_input_tokens
    cache_creation_input_tokens
    total_cost_usd
)

test_run() {
    # Drive as root weave, not whatever identity the harness inherited.
    unset SPRAWL_AGENT_IDENTITY

    e2e_recover_oauth_token
    e2e_setup_tmux_socket "sprawl-usage-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum368-usage"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi

    if ! command -v jq >/dev/null 2>&1; then
        fail "jq is required for usage e2e schema validation"
        return 1
    fi

    local SESSION="sprawl-usage-e2e-$(head -c4 /dev/urandom | xxd -p)"
    local STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo ""

    echo "=== Launching sprawl enter ==="
    if ! e2e_launch_tui "$SESSION" 200 50; then
        return 1
    fi
    pass "TUI rendered"

    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3

    e2e_attach_phantom_client "$SESSION"
    sleep 2

    # Drive a single weave turn — short prompt, just enough to trigger one
    # EventTurnCompleted.
    echo ""
    echo "=== Driving one weave turn ==="
    e2e_send_user_prompt "$SESSION" "say hi in three words"

    # Wait for turn completion — look for the "Completed in" status line the
    # statusbar shows after EventTurnCompleted, with a generous timeout for
    # the model to respond.
    if wait_for_pattern "$SESSION" "Completed in" 120; then
        pass "weave turn completed"
    else
        fail "weave turn did not complete within 120s"
        capture_pane "$SESSION" | tail -40 >&2
        return 1
    fi

    # Give the usage subscriber goroutine a moment to flush.
    sleep 2

    # Locate the usage NDJSON file under .sprawl/logs/usage/weave/.
    local USAGE_DIR="$SPRAWL_ROOT/.sprawl/logs/usage/weave"
    if [ ! -d "$USAGE_DIR" ]; then
        fail "usage dir does not exist: $USAGE_DIR"
        ls -la "$SPRAWL_ROOT/.sprawl/logs/" 2>&1 | tail -20 >&2 || true
        return 1
    fi

    local USAGE_FILES
    USAGE_FILES=$(find "$USAGE_DIR" -maxdepth 1 -name '*.ndjson' -type f 2>/dev/null)
    if [ -z "$USAGE_FILES" ]; then
        fail "no .ndjson files under $USAGE_DIR"
        ls -la "$USAGE_DIR" >&2 || true
        return 1
    fi

    local USAGE_FILE
    USAGE_FILE=$(echo "$USAGE_FILES" | head -1)
    pass "found usage log: $USAGE_FILE"

    local RECORD_COUNT
    RECORD_COUNT=$(grep -c '^{' "$USAGE_FILE" || true)
    if [ "$RECORD_COUNT" -lt 1 ]; then
        fail "expected ≥1 record in $USAGE_FILE, got $RECORD_COUNT"
        cat "$USAGE_FILE" >&2 || true
        return 1
    fi
    pass "usage log has $RECORD_COUNT record(s)"

    # Schema validation on the first record: every required key must be
    # present (jq `has(key)`).
    local FIRST
    FIRST=$(head -1 "$USAGE_FILE")
    if ! echo "$FIRST" | jq -e . >/dev/null 2>&1; then
        fail "first record is not well-formed JSON: $FIRST"
        return 1
    fi
    pass "first record is well-formed JSON"

    local missing=()
    for key in "${USAGE_REQUIRED_KEYS[@]}"; do
        if ! echo "$FIRST" | jq -e --arg k "$key" 'has($k)' >/dev/null 2>&1; then
            missing+=("$key")
        fi
    done
    if [ "${#missing[@]}" -eq 0 ]; then
        pass "all 13 required schema keys present in first record"
    else
        fail "missing schema keys: ${missing[*]}"
        echo "  record: $FIRST" >&2
    fi

    # Sanity-check a few semantic constraints:
    #  - agent_name == "weave"
    #  - input_tokens + output_tokens > 0
    #  - total_cost_usd >= 0
    local AGENT_NAME INPUT_TOKENS OUTPUT_TOKENS COST
    AGENT_NAME=$(echo "$FIRST" | jq -r '.agent_name')
    INPUT_TOKENS=$(echo "$FIRST" | jq -r '.input_tokens')
    OUTPUT_TOKENS=$(echo "$FIRST" | jq -r '.output_tokens')
    COST=$(echo "$FIRST" | jq -r '.total_cost_usd')

    if [ "$AGENT_NAME" = "weave" ]; then
        pass "agent_name = weave"
    else
        fail "expected agent_name=weave, got '$AGENT_NAME'"
    fi

    if [ "$(echo "$INPUT_TOKENS + $OUTPUT_TOKENS > 0" | bc -l 2>/dev/null || echo 0)" = "1" ]; then
        pass "input_tokens+output_tokens > 0 (input=$INPUT_TOKENS, output=$OUTPUT_TOKENS)"
    else
        fail "expected input_tokens+output_tokens > 0, got input=$INPUT_TOKENS, output=$OUTPUT_TOKENS"
    fi

    if [ "$(echo "$COST >= 0" | bc -l 2>/dev/null || echo 0)" = "1" ]; then
        pass "total_cost_usd >= 0 ($COST)"
    else
        fail "expected total_cost_usd >= 0, got $COST"
    fi

    echo ""
    e2e_print_results
}
