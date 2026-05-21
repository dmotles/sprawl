#!/usr/bin/env bash
# scripts/e2e-tests/handoff.sh — QUM-329 regression guard.
# Migrated from scripts/test-handoff-e2e.sh (which remains in place).
# Drives weave to call mcp__sprawl__handoff and asserts the full
# teardown / restart / session-id-rotation pipeline fires.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

# --- Test-local helpers (single-test scope, not promoted to lib). ---

# find_claude_pids — newline-separated pids of claude subprocesses scoped
# to this sandbox by the --system-prompt-file argv pointing under SPRAWL_ROOT.
_handoff_find_claude_pids() {
    pgrep -af 'claude' 2>/dev/null | awk -v root="$SPRAWL_ROOT" '
        $0 ~ "stream-json" && index($0, root) > 0 { print $1 }
    '
}

_handoff_find_claude_pid() {
    _handoff_find_claude_pids | head -1
}

_handoff_find_claude_pid_for_sid() {
    local want_sid="$1"
    pgrep -af 'claude' 2>/dev/null | awk -v root="$SPRAWL_ROOT" -v sid="$want_sid" '
        $0 ~ "stream-json" && index($0, root) > 0 && index($0, sid) > 0 { print $1; exit }
    '
}

_handoff_pid_is_live() {
    local pid="$1"
    [ -n "$pid" ] || return 1
    [ -r "/proc/$pid/status" ] || return 1
    local state
    state=$(awk '/^State:/ { print $2; exit }' "/proc/$pid/status" 2>/dev/null)
    case "$state" in
        Z|"") return 1 ;;
        *) return 0 ;;
    esac
}

_handoff_session_id_for_pid() {
    local pid="$1"
    if [ -z "$pid" ] || [ ! -r "/proc/$pid/cmdline" ]; then
        echo ""
        return
    fi
    tr '\0' '\n' < "/proc/$pid/cmdline" | awk '
        /^--session-id$/ { getline sid; print sid; exit }
        /^--session-id=/ { sub(/^--session-id=/, ""); print; exit }
    '
}

test_run() {
    e2e_recover_oauth_token
    e2e_setup_tmux_socket "sprawl-handoff-e2e"

    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-handoff-e2e"
    e2e_install_cleanup_traps

    git -C "$SPRAWL_ROOT" init -b main --quiet
    git -C "$SPRAWL_ROOT" -c user.name="Test" -c user.email="test@test" \
        commit --allow-empty -m "init" --quiet
    mkdir -p "$SPRAWL_ROOT/.sprawl" "$SPRAWL_ROOT/.sprawl/memory" "$SPRAWL_ROOT/.sprawl/state"
    echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"

    local SESSION="sprawl-handoff-e2e-$(head -c4 /dev/urandom | xxd -p)"
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

    local OLD_PID OLD_SID
    OLD_PID="$(_handoff_find_claude_pid)"
    if [ -z "$OLD_PID" ]; then
        fail "could not locate initial claude subprocess"
        echo "  pgrep output:" >&2
        pgrep -af 'claude' >&2 || true
        e2e_print_results
        return 1
    fi
    OLD_SID="$(_handoff_session_id_for_pid "$OLD_PID")"
    if [ -z "$OLD_SID" ]; then
        fail "could not extract --session-id for pid $OLD_PID"
        e2e_print_results
        return 1
    fi
    echo "  old claude pid=$OLD_PID session-id=$OLD_SID"

    local OLD_LAST_SID_FILE="$SPRAWL_ROOT/.sprawl/memory/last-session-id"
    if [ ! -f "$OLD_LAST_SID_FILE" ]; then
        fail "last-session-id file not created by TUI launch"
        e2e_print_results
        return 1
    fi
    local OLD_LAST_SID
    OLD_LAST_SID="$(cat "$OLD_LAST_SID_FILE")"
    echo "  last-session-id (pre-handoff) = $OLD_LAST_SID"

    echo ""
    echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
    e2e_attach_phantom_client "$SESSION"

    echo ""
    echo "=== Firing handoff via MCP ==="
    local HANDOFF_PROMPT="Call the mcp__sprawl__handoff tool with a short summary 'QUM-329 e2e test handoff'."
    e2e_send_user_prompt "$SESSION" "$HANDOFF_PROMPT"
    sleep 2

    echo ""
    echo "=== Post-handoff assertions ==="

    local HANDOFF_SIGNAL="$SPRAWL_ROOT/.sprawl/memory/handoff-signal"

    # 1. handoff fired (signal file or session summary).
    local ELAPSED=0 SIGNAL_APPEARED=0
    while [ "$ELAPSED" -lt 90 ]; do
        if [ -f "$HANDOFF_SIGNAL" ]; then
            SIGNAL_APPEARED=1
            break
        fi
        if ls "$SPRAWL_ROOT/.sprawl/memory/sessions/"*.md >/dev/null 2>&1; then
            SIGNAL_APPEARED=1
            break
        fi
        sleep 1
        ELAPSED=$((ELAPSED + 1))
    done
    if [ "$SIGNAL_APPEARED" -eq 1 ]; then
        pass "handoff fired (signal file or session summary observed)"
    else
        fail "handoff never fired within 90s (claude didn't call handoff; see pane tail)"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
    fi

    # 2. TUI restart evidence (pane banner / status indicator / stderr log).
    local TUI_LOG_GLOB="$SPRAWL_ROOT/.sprawl/logs/tui-stderr-*.log"
    local restart_evidence_seen=""
    ELAPSED=0
    while [ "$ELAPSED" -lt 60 ]; do
        if capture_pane "$SESSION" | grep -qE "Session restarting.*handoff|restart [0-9]+s"; then
            restart_evidence_seen="pane"
            break
        fi
        # shellcheck disable=SC2086
        if ls $TUI_LOG_GLOB 2>/dev/null | head -1 | xargs -r grep -l "handoff signal detected, restarting" >/dev/null 2>&1; then
            restart_evidence_seen="tui-log"
            break
        fi
        sleep 1
        ELAPSED=$((ELAPSED + 1))
    done
    if [ -n "$restart_evidence_seen" ]; then
        pass "TUI triggered handoff restart (evidence=$restart_evidence_seen)"
    else
        fail "TUI never triggered handoff restart within 60s (QUM-329 regression)"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
        echo "  tui-stderr log tail:" >&2
        # shellcheck disable=SC2086
        ls $TUI_LOG_GLOB 2>/dev/null | head -1 | xargs -r tail -30 >&2 || true
    fi

    # 3. Old claude terminated.
    ELAPSED=0
    local OLD_GONE=0 STILL_PID=""
    while [ "$ELAPSED" -lt 60 ]; do
        STILL_PID="$(_handoff_find_claude_pid_for_sid "$OLD_SID")"
        if [ -z "$STILL_PID" ] || ! _handoff_pid_is_live "$STILL_PID"; then
            OLD_GONE=1
            break
        fi
        sleep 1
        ELAPSED=$((ELAPSED + 1))
    done
    if [ "$OLD_GONE" -eq 1 ]; then
        pass "old claude (session-id $OLD_SID) terminated after handoff"
    else
        fail "old claude (session-id $OLD_SID, pid $STILL_PID) still alive 60s after handoff (QUM-329 regression — teardown never ran)"
    fi

    # 4. New live claude with different session-id (or resume-mode).
    local NEW_PID="" NEW_SID=""
    ELAPSED=0
    local SPRAWL_ENTER_PID
    SPRAWL_ENTER_PID="$(pgrep -af "$SPRAWL_BIN enter" 2>/dev/null | awk -v root="$SPRAWL_ROOT" 'index($0, root) > 0 { print $1; exit }')"
    if [ -z "$SPRAWL_ENTER_PID" ]; then
        SPRAWL_ENTER_PID="$(awk '{ print $4 }' "/proc/$OLD_PID/stat" 2>/dev/null)"
    fi
    while [ "$ELAPSED" -lt 180 ]; do
        local CANDIDATES=""
        if [ -n "$SPRAWL_ENTER_PID" ]; then
            CANDIDATES="$(pgrep -P "$SPRAWL_ENTER_PID" -f claude 2>/dev/null || true)"
        fi
        CANDIDATES="$CANDIDATES
$(_handoff_find_claude_pids)"
        local CAND_PID CAND_SID
        while IFS= read -r CAND_PID; do
            [ -z "$CAND_PID" ] && continue
            [ "$CAND_PID" = "$OLD_PID" ] && continue
            if ! _handoff_pid_is_live "$CAND_PID"; then
                continue
            fi
            CAND_SID="$(_handoff_session_id_for_pid "$CAND_PID")"
            if [ -z "$CAND_SID" ]; then
                NEW_PID="$CAND_PID"
                NEW_SID="(resume)"
                break
            fi
            if [ "$CAND_SID" != "$OLD_SID" ]; then
                NEW_PID="$CAND_PID"
                NEW_SID="$CAND_SID"
                break
            fi
        done <<< "$CANDIDATES"
        if [ -n "$NEW_PID" ]; then
            break
        fi
        sleep 1
        ELAPSED=$((ELAPSED + 1))
    done
    if [ -n "$NEW_PID" ]; then
        pass "new claude pid=$NEW_PID session-id=$NEW_SID (differs from old pid=$OLD_PID sid=$OLD_SID)"
    else
        fail "no new live claude subprocess within 180s (QUM-329 regression)"
        echo "  sprawl enter pid: $SPRAWL_ENTER_PID" >&2
        echo "  children of sprawl enter:" >&2
        pgrep -P "$SPRAWL_ENTER_PID" -af >&2 || true
        echo "  all claude in sandbox (cmdline match):" >&2
        _handoff_find_claude_pids | while IFS= read -r p; do
            echo "    pid=$p sid=$(_handoff_session_id_for_pid "$p") live=$(_handoff_pid_is_live "$p" && echo yes || echo no)" >&2
        done
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -20 >&2
    fi

    # 5. handoff-signal removed.
    if [ -f "$HANDOFF_SIGNAL" ]; then
        fail "handoff-signal file NOT removed post-consumption (side-fix regression — see QUM-329 comment)"
    else
        pass "handoff-signal file removed by FinalizeHandoff"
    fi

    # 6. last-session-id changed (cleared or rewritten).
    local NEW_LAST_SID
    NEW_LAST_SID="$(cat "$OLD_LAST_SID_FILE" 2>/dev/null || echo "")"
    if [ "$NEW_LAST_SID" != "$OLD_LAST_SID" ]; then
        pass "last-session-id changed ($OLD_LAST_SID -> ${NEW_LAST_SID:-<cleared>})"
    else
        fail "last-session-id did not change ($OLD_LAST_SID == $NEW_LAST_SID)"
    fi

    e2e_print_results
}
