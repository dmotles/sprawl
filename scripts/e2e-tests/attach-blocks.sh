#!/usr/bin/env bash
# scripts/e2e-tests/attach-blocks.sh — QUM-860 (Slice A) multimodal G4 gate.
#
# Proves that the --replay-user-messages consumption-ack contract holds for a
# MULTIMODAL user message — one whose message.content is an ARRAY of content
# blocks (a base64 image block + a text block), not a bare string. This is the
# G4 acceptance criterion: the real claude CLI, launched in stream-json input
# mode, must ingest the array-content turn AND echo it back as an
# {type:"user", isReplay:true} frame with the SAME uuid intact.
#
# Like replay-echo.sh, this row launches claude directly over pipes (NOT via
# `sprawl enter`) — it exercises the protocol keystone for image blocks, not
# the TUI. The image-then-text block ordering mirrors the /attach assembly
# contract (design §4).

test_metadata() {
    echo "needs_claude=1 needs_jq=1"
}

# 1x1 transparent PNG, base64 — the smallest valid image block payload.
ATTACH_PNG_B64="iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M8AAAMEAQD3A02NAAAAAElFTkSuQmCC"

# ---------------------------------------------------------------------------
# Helpers (modeled on replay-echo.sh)
# ---------------------------------------------------------------------------

# attach_claude_args — the launch line sprawl uses (adapter.go) plus
# --replay-user-messages. bypassPermissions + a non-interactive model so the
# run never blocks on a prompt.
attach_claude_args() {
    printf '%s\n' \
        -p \
        --input-format stream-json \
        --output-format stream-json \
        --verbose \
        --replay-user-messages \
        --permission-mode bypassPermissions \
        --model sonnet
}

# attach_run INPUT_FILE OUT_FILE [GRACE] — feed INPUT_FILE on stdin, hold stdin
# open for a grace window so the CLI emits its isReplay echo before EOF, then
# close stdin (so `-p` exits) under an outer timeout. Best-effort: a non-zero
# exit (timeout / EOF) is tolerated; assertions read the captured OUT_FILE.
attach_run() {
    local input_file="$1" out_file="$2" grace="${3:-12}"
    local args
    mapfile -t args < <(attach_claude_args)
    { cat "$input_file"; sleep "$grace"; } \
        | ( cd "$SPRAWL_ROOT" && timeout 120 "$CLAUDE_CMD" "${args[@]}" ) \
        > "$out_file" 2> "${out_file}.err" || true
}

# attach_uuid — a fresh RFC-4122 uuid from the kernel.
attach_uuid() {
    cat /proc/sys/kernel/random/uuid
}

# attach_blocks_line UUID — one stdin NDJSON user message whose content is an
# ARRAY of blocks: [image (base64 png), text]. Image-then-text ordering.
attach_blocks_line() {
    local uuid="$1"
    printf '{"type":"user","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"%s"}},{"type":"text","text":"Reply with exactly: OK"}]},"parent_tool_use_id":null,"uuid":"%s"}\n' \
        "$ATTACH_PNG_B64" "$uuid"
}

# attach_has_echo OUT_FILE UUID — true if OUT_FILE has an isReplay:true user
# frame keyed on UUID.
attach_has_echo() {
    local out_file="$1" uuid="$2"
    jq -rc 'select(.type=="user" and .isReplay==true) | .uuid' "$out_file" 2>/dev/null \
        | grep -qF "$uuid"
}

# ---------------------------------------------------------------------------
# test_run
# ---------------------------------------------------------------------------

test_run() {
    e2e_recover_oauth_token
    e2e_make_sandbox_root "sprawl-attach-blocks"
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
    # G4: array-content (multimodal) user message → isReplay echo, same uuid
    # =====================================================================
    echo ""
    echo "=== G4: multimodal isReplay echo (image+text blocks, same uuid) ==="
    local U OUT
    U="$(attach_uuid)"
    OUT="$SPRAWL_ROOT/out.ndjson"
    attach_blocks_line "$U" > "$SPRAWL_ROOT/in.ndjson"
    echo "  uuid=$U"
    attach_run "$SPRAWL_ROOT/in.ndjson" "$OUT"

    if attach_has_echo "$OUT" "$U"; then
        pass "G4: isReplay:true echo with same uuid ($U) for an array-content turn"
    else
        fail "G4: no isReplay:true echo for uuid=$U (multimodal array-content turn)"
        echo "  --- stdout frame types ---" >&2
        jq -rc '.type + (if .subtype then ":"+.subtype else "" end)' "$OUT" 2>/dev/null | head -20 >&2 || cat "$OUT" >&2
        echo "  --- stderr tail ---" >&2
        tail -10 "${OUT}.err" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    e2e_print_results
}
