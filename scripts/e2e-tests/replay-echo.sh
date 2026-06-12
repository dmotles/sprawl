#!/usr/bin/env bash
# scripts/e2e-tests/replay-echo.sh — QUM-814 (Slice 0 of QUM-813) keystone gate.
#
# Proves the --replay-user-messages consumption-ack against the REAL claude
# CLI before anything downstream depends on it. This row launches claude
# directly over pipes (NOT via `sprawl enter`) — it exercises the protocol
# keystone, not the TUI.
#
# Scenarios:
#   A. Single isReplay echo  : write one stdin user message carrying a uuid →
#                              assert a {type:"user", isReplay:true} echo with
#                              the SAME uuid appears on stdout.
#   B. Multi-message coalesced: write 3 user messages back-to-back (distinct
#                              uuids) → assert 3 DISTINCT isReplay echoes, one
#                              per uuid (design §8 Risk #2: per-message identity
#                              is preserved even when the CLI coalesces the
#                              queue into a single model turn).

test_metadata() {
    echo "needs_claude=1 needs_jq=1"
}

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

# replay_claude_args — the launch line sprawl uses (adapter.go) plus the new
# --replay-user-messages flag. bypassPermissions + a non-interactive model so
# the run never blocks on a prompt.
replay_claude_args() {
    printf '%s\n' \
        -p \
        --input-format stream-json \
        --output-format stream-json \
        --verbose \
        --replay-user-messages \
        --permission-mode bypassPermissions \
        --model sonnet
}

# replay_run INPUT_FILE OUT_FILE — feed INPUT_FILE on stdin, hold stdin open
# for a grace window so the CLI emits its isReplay echoes before EOF, then
# close stdin (so `-p` exits) under an outer timeout. Best-effort: a non-zero
# exit (timeout / EOF) is tolerated; assertions read the captured OUT_FILE.
replay_run() {
    local input_file="$1" out_file="$2" grace="${3:-12}"
    local args
    mapfile -t args < <(replay_claude_args)
    { cat "$input_file"; sleep "$grace"; } \
        | ( cd "$SPRAWL_ROOT" && timeout 120 "$CLAUDE_CMD" "${args[@]}" ) \
        > "$out_file" 2> "${out_file}.err" || true
}

# replay_uuid — a fresh RFC-4122 uuid from the kernel.
replay_uuid() {
    cat /proc/sys/kernel/random/uuid
}

# replay_user_line UUID CONTENT — one stdin NDJSON user message.
replay_user_line() {
    local uuid="$1" content="$2"
    printf '{"type":"user","message":{"role":"user","content":"%s"},"parent_tool_use_id":null,"uuid":"%s"}\n' \
        "$content" "$uuid"
}

# replay_has_echo OUT_FILE UUID — true if OUT_FILE has an isReplay:true user
# frame keyed on UUID.
replay_has_echo() {
    local out_file="$1" uuid="$2"
    jq -rc 'select(.type=="user" and .isReplay==true) | .uuid' "$out_file" 2>/dev/null \
        | grep -qF "$uuid"
}

# ---------------------------------------------------------------------------
# test_run
# ---------------------------------------------------------------------------

test_run() {
    e2e_recover_oauth_token
    e2e_make_sandbox_root "sprawl-replay-echo"
    e2e_install_cleanup_traps

    # Resolve the claude launcher. Prefer the run-claude shim when a repo .env
    # exists (re-hydrates CLAUDE_CODE_OAUTH_TOKEN in this subshell, QUM-518);
    # otherwise fall back to claude on PATH (token recovered into env above).
    CLAUDE_CMD="claude"
    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
        CLAUDE_CMD="$REPO_ROOT/scripts/run-claude"
    fi
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  CLAUDE_CMD=$CLAUDE_CMD"

    # =====================================================================
    # Scenario A: single isReplay echo, same uuid
    # =====================================================================
    echo ""
    echo "=== Scenario A: single isReplay echo ==="
    local UA OUT_A
    UA="$(replay_uuid)"
    OUT_A="$SPRAWL_ROOT/out-a.ndjson"
    replay_user_line "$UA" "Reply with exactly: OK" > "$SPRAWL_ROOT/in-a.ndjson"
    echo "  uuid=$UA"
    replay_run "$SPRAWL_ROOT/in-a.ndjson" "$OUT_A"

    if replay_has_echo "$OUT_A" "$UA"; then
        pass "A: isReplay:true echo with same uuid ($UA) appeared on stdout"
    else
        fail "A: no isReplay:true echo for uuid=$UA"
        echo "  --- stdout frame types ---" >&2
        jq -rc '.type + (if .subtype then ":"+.subtype else "" end)' "$OUT_A" 2>/dev/null | head -20 >&2 || cat "$OUT_A" >&2
        echo "  --- stderr tail ---" >&2
        tail -10 "${OUT_A}.err" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    # =====================================================================
    # Scenario B: 3 quick messages → 3 distinct isReplay echoes (§8 Risk #2)
    # =====================================================================
    echo ""
    echo "=== Scenario B: multi-message coalesced (3 quick → 3 distinct echoes) ==="
    local U1 U2 U3 OUT_B
    U1="$(replay_uuid)"; U2="$(replay_uuid)"; U3="$(replay_uuid)"
    OUT_B="$SPRAWL_ROOT/out-b.ndjson"
    {
        replay_user_line "$U1" "msg 1"
        replay_user_line "$U2" "msg 2"
        replay_user_line "$U3" "msg 3"
    } > "$SPRAWL_ROOT/in-b.ndjson"
    echo "  uuids: $U1 $U2 $U3"
    replay_run "$SPRAWL_ROOT/in-b.ndjson" "$OUT_B"

    local missing=0 u
    for u in "$U1" "$U2" "$U3"; do
        if replay_has_echo "$OUT_B" "$u"; then
            pass "B: distinct isReplay echo for uuid=$u"
        else
            fail "B: missing isReplay echo for uuid=$u"
            missing=1
        fi
    done
    if [ "$missing" -ne 0 ]; then
        echo "  --- isReplay echoes seen ---" >&2
        jq -rc 'select(.type=="user" and .isReplay==true) | .uuid' "$OUT_B" 2>/dev/null >&2 || true
        echo "  --- stderr tail ---" >&2
        tail -10 "${OUT_B}.err" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    e2e_print_results
}
