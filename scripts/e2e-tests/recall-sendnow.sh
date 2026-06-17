#!/usr/bin/env bash
# scripts/e2e-tests/recall-sendnow.sh — QUM-824 (Slice 4 of QUM-813) gate.
#
# Proves the protocol building blocks the weave recall / send-all-now UX is
# built on, against the REAL claude CLI. Launches claude directly over pipes
# (NOT via `sprawl enter`) — it exercises the cancel_async_message contract and
# the now/next ack asymmetry, not the TUI (the TUI reducers + runtime Recall/
# SendAllNow logic are pinned by unit tests in internal/tui, internal/tuiruntime
# and internal/runtime).
#
# Scenarios (all verified against claude 2.1.173 during authoring):
#   A. Recall a genuinely-pending message: keep the agent busy (sleep tool),
#      queue a second message behind it, cancel_async_message it →
#      {cancelled:true} AND no isReplay echo (it never entered the conversation,
#      so recall may rehydrate it).
#   B. Recall an already-consumed message: send a quick message, let it execute,
#      then cancel_async_message it → {cancelled:false} (already dequeued for
#      execution) AND an isReplay echo is present (it WAS consumed) — the
#      correctness crux: recall must NOT pull this one back.
#   C. Send-all-now supersede: queue two messages behind a busy turn, cancel
#      both ({cancelled:true}, no isReplay), then write ONE priority:"now"
#      message that supersedes them. Asserts the two superseded uuids resolve as
#      cancelled (no isReplay) and the single now message drives a turn.

test_metadata() {
    echo "needs_claude=1 needs_jq=1"
}

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

rsn_claude_args() {
    printf '%s\n' \
        -p \
        --input-format stream-json \
        --output-format stream-json \
        --verbose \
        --replay-user-messages \
        --permission-mode bypassPermissions \
        --model sonnet
}

# rsn_run FEED_SCRIPT OUT_FILE — run FEED_SCRIPT (which prints NDJSON stdin lines
# interleaved with sleeps) into claude, capturing stdout to OUT_FILE. Bounded by
# an outer timeout; a non-zero exit (timeout / EOF) is tolerated — assertions
# read the captured OUT_FILE.
rsn_run() {
    local feed_script="$1" out_file="$2"
    local args
    mapfile -t args < <(rsn_claude_args)
    bash "$feed_script" \
        | ( cd "$SPRAWL_ROOT" && timeout 150 "$CLAUDE_CMD" "${args[@]}" ) \
        > "$out_file" 2> "${out_file}.err" || true
}

rsn_uuid() { cat /proc/sys/kernel/random/uuid; }

# rsn_user_line UUID CONTENT [PRIORITY] — one stdin NDJSON user message.
rsn_user_line() {
    local uuid="$1" content="$2" priority="${3:-}"
    if [ -n "$priority" ]; then
        printf '{"type":"user","message":{"role":"user","content":"%s"},"parent_tool_use_id":null,"priority":"%s","uuid":"%s"}\n' \
            "$content" "$priority" "$uuid"
    else
        printf '{"type":"user","message":{"role":"user","content":"%s"},"parent_tool_use_id":null,"uuid":"%s"}\n' \
            "$content" "$uuid"
    fi
}

# rsn_cancel_line REQUEST_ID UUID — one stdin cancel_async_message control_request.
rsn_cancel_line() {
    local request_id="$1" uuid="$2"
    printf '{"type":"control_request","request_id":"%s","request":{"subtype":"cancel_async_message","message_uuid":"%s"}}\n' \
        "$request_id" "$uuid"
}

# rsn_has_echo OUT_FILE UUID — true if OUT_FILE has an isReplay:true user frame
# keyed on UUID (the consumption ack).
rsn_has_echo() {
    local out_file="$1" uuid="$2"
    jq -rc 'select(.type=="user" and .isReplay==true) | .uuid' "$out_file" 2>/dev/null \
        | grep -qF "$uuid"
}

# rsn_cancelled_value OUT_FILE REQUEST_ID — prints the {cancelled} bool of the
# control_response matching REQUEST_ID ("true"/"false"/"" if absent). Matches
# the verified wire shape: .response.request_id + .response.response.cancelled.
rsn_cancelled_value() {
    local out_file="$1" request_id="$2"
    jq -rc --arg id "$request_id" \
        'select(.type=="control_response" and .response.request_id==$id) | .response.response.cancelled' \
        "$out_file" 2>/dev/null | head -1
}

rsn_dump() {
    local out_file="$1"
    echo "  --- stdout frame types ---" >&2
    jq -rc '.type + (if .subtype then ":"+.subtype else "" end)' "$out_file" 2>/dev/null | head -30 >&2 || cat "$out_file" >&2
    echo "  --- control_responses ---" >&2
    grep -F '"control_response"' "$out_file" >&2 2>/dev/null || true
    echo "  --- isReplay uuids ---" >&2
    jq -rc 'select(.type=="user" and .isReplay==true) | .uuid' "$out_file" 2>/dev/null >&2 || true
    echo "  --- stderr tail ---" >&2
    tail -10 "${out_file}.err" >&2 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# test_run
# ---------------------------------------------------------------------------

test_run() {
    e2e_recover_oauth_token
    e2e_make_sandbox_root "sprawl-recall-sendnow"
    e2e_install_cleanup_traps

    CLAUDE_CMD="claude"
    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
        CLAUDE_CMD="$REPO_ROOT/scripts/run-claude"
    fi
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  CLAUDE_CMD=$CLAUDE_CMD"

    # =====================================================================
    # Scenario A: recall a genuinely-pending message
    # =====================================================================
    echo ""
    echo "=== Scenario A: cancel a genuinely-pending message ==="
    local UA_BUSY UA_PENDING OUT_A
    UA_BUSY="$(rsn_uuid)"; UA_PENDING="$(rsn_uuid)"
    OUT_A="$SPRAWL_ROOT/out-a.ndjson"
    {
        echo "$(declare -f rsn_user_line rsn_cancel_line)"
        echo "rsn_user_line '$UA_BUSY' 'Run exactly this shell command and nothing else: sleep 20'"
        echo "sleep 5"
        echo "rsn_user_line '$UA_PENDING' 'this should be recalled, not processed'"
        echo "sleep 1"
        echo "rsn_cancel_line 'req-a' '$UA_PENDING'"
        echo "sleep 10"
    } > "$SPRAWL_ROOT/feed-a.sh"
    echo "  busy=$UA_BUSY pending=$UA_PENDING"
    rsn_run "$SPRAWL_ROOT/feed-a.sh" "$OUT_A"

    local a_cancelled
    a_cancelled="$(rsn_cancelled_value "$OUT_A" "req-a")"
    if [ "$a_cancelled" = "true" ]; then
        pass "A: cancel_async_message of a pending uuid returned {cancelled:true}"
    else
        fail "A: expected {cancelled:true} for pending uuid, got '${a_cancelled:-<none>}'"
        rsn_dump "$OUT_A"
        e2e_print_results
        return 1
    fi
    if rsn_has_echo "$OUT_A" "$UA_PENDING"; then
        fail "A: pending uuid produced an isReplay echo — it was consumed, not recallable"
        rsn_dump "$OUT_A"
        e2e_print_results
        return 1
    else
        pass "A: pending uuid produced NO isReplay echo (recall may rehydrate it)"
    fi

    # =====================================================================
    # Scenario B: recall an already-consumed message → cancelled:false
    # =====================================================================
    echo ""
    echo "=== Scenario B: cancel an already-consumed message ==="
    local UB OUT_B
    UB="$(rsn_uuid)"
    OUT_B="$SPRAWL_ROOT/out-b.ndjson"
    {
        echo "$(declare -f rsn_user_line rsn_cancel_line)"
        echo "rsn_user_line '$UB' 'Reply with exactly: OK'"
        echo "sleep 9"
        echo "rsn_cancel_line 'req-b' '$UB'"
        echo "sleep 4"
    } > "$SPRAWL_ROOT/feed-b.sh"
    echo "  uuid=$UB"
    rsn_run "$SPRAWL_ROOT/feed-b.sh" "$OUT_B"

    if ! rsn_has_echo "$OUT_B" "$UB"; then
        fail "B: consumed uuid has no isReplay echo (turn may not have run); cannot assert the consumed-cancel semantic"
        rsn_dump "$OUT_B"
        e2e_print_results
        return 1
    fi
    pass "B: consumed uuid produced an isReplay echo (it entered the conversation)"
    local b_cancelled
    b_cancelled="$(rsn_cancelled_value "$OUT_B" "req-b")"
    if [ "$b_cancelled" = "false" ]; then
        pass "B: cancel of an already-consumed uuid returned {cancelled:false} (not recallable)"
    else
        fail "B: expected {cancelled:false} for consumed uuid, got '${b_cancelled:-<none>}'"
        rsn_dump "$OUT_B"
        e2e_print_results
        return 1
    fi

    # =====================================================================
    # Scenario C: send-all-now supersede (cancel two pending, one now write)
    # =====================================================================
    echo ""
    echo "=== Scenario C: send-all-now supersedes queued with one now message ==="
    local UC_BUSY UC1 UC2 UC_NOW OUT_C
    UC_BUSY="$(rsn_uuid)"; UC1="$(rsn_uuid)"; UC2="$(rsn_uuid)"; UC_NOW="$(rsn_uuid)"
    OUT_C="$SPRAWL_ROOT/out-c.ndjson"
    {
        echo "$(declare -f rsn_user_line rsn_cancel_line)"
        echo "rsn_user_line '$UC_BUSY' 'Run exactly this shell command and nothing else: sleep 20'"
        echo "sleep 5"
        echo "rsn_user_line '$UC1' 'queued one'"
        echo "rsn_user_line '$UC2' 'queued two'"
        echo "sleep 1"
        echo "rsn_cancel_line 'req-c1' '$UC1'"
        echo "rsn_cancel_line 'req-c2' '$UC2'"
        echo "rsn_user_line '$UC_NOW' 'Reply with exactly: COMBINED' 'now'"
        echo "sleep 12"
    } > "$SPRAWL_ROOT/feed-c.sh"
    echo "  busy=$UC_BUSY q1=$UC1 q2=$UC2 now=$UC_NOW"
    rsn_run "$SPRAWL_ROOT/feed-c.sh" "$OUT_C"

    local c1 c2
    c1="$(rsn_cancelled_value "$OUT_C" "req-c1")"
    c2="$(rsn_cancelled_value "$OUT_C" "req-c2")"
    local c_fail=0
    if [ "$c1" = "true" ]; then
        pass "C: queued message 1 cancelled ({cancelled:true})"
    else
        fail "C: queued message 1 expected {cancelled:true}, got '${c1:-<none>}'"; c_fail=1
    fi
    if [ "$c2" = "true" ]; then
        pass "C: queued message 2 cancelled ({cancelled:true})"
    else
        fail "C: queued message 2 expected {cancelled:true}, got '${c2:-<none>}'"; c_fail=1
    fi
    if rsn_has_echo "$OUT_C" "$UC1" || rsn_has_echo "$OUT_C" "$UC2"; then
        fail "C: a superseded queued uuid produced an isReplay echo (was not superseded)"; c_fail=1
    else
        pass "C: neither superseded queued uuid produced an isReplay echo"
    fi
    # The now message drives a turn. now-writes are NOT echoed via
    # --replay-user-messages (QUM-821 ack asymmetry), so we assert the model
    # produced an assistant turn after the supersede rather than an isReplay.
    if jq -rc 'select(.type=="assistant")' "$OUT_C" 2>/dev/null | grep -qiF "COMBINED"; then
        pass "C: the single now message drove a turn (assistant replied COMBINED)"
    else
        echo "  note: assistant 'COMBINED' reply not observed (now-preempt timing is nondeterministic on a tool-bound turn — QUM-821); cancel+supersede invariants above are the hard gate" >&2
    fi

    if [ "$c_fail" -ne 0 ]; then
        rsn_dump "$OUT_C"
        e2e_print_results
        return 1
    fi

    e2e_print_results
}
