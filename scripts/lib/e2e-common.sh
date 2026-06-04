#!/usr/bin/env bash
# scripts/lib/e2e-common.sh — Shared scaffolding for matrix-driven e2e tests
# (QUM-616 Wave 1). Sourceable; idempotent / re-source-safe.

[[ -n "${_E2E_COMMON_SH:-}" ]] && return 0
_E2E_COMMON_SH=1

# Resolve repo root once at source time, using parameter expansion so the lib
# is sourceable even when PATH is scrubbed (the matrix driver's preflight
# unit tests deliberately drop PATH before invoking the driver).
_e2e_self="${BASH_SOURCE[0]}"
case "$_e2e_self" in
    /*) _e2e_self_dir="${_e2e_self%/*}" ;;
    */*) _e2e_self_dir="$PWD/${_e2e_self%/*}" ;;
    *) _e2e_self_dir="$PWD" ;;
esac
_e2e_scripts_dir="${_e2e_self_dir%/*}"
E2E_COMMON_REPO_ROOT="${_e2e_scripts_dir%/*}"
unset _e2e_self _e2e_self_dir _e2e_scripts_dir
: "${REPO_ROOT:=$E2E_COMMON_REPO_ROOT}"

PASS_COUNT=0
FAIL_COUNT=0

pass() {
    PASS_COUNT=$((PASS_COUNT + 1))
    echo "  PASS: $1"
}

fail() {
    FAIL_COUNT=$((FAIL_COUNT + 1))
    echo "  FAIL: $1" >&2
}

e2e_print_results() {
    echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
    if [ "$FAIL_COUNT" -gt 0 ]; then
        return 1
    fi
    return 0
}

# QUM-411: walk up to 8 ancestors via /proc/<pid>/stat parent field and try to
# recover CLAUDE_CODE_OAUTH_TOKEN from each ancestor's environ. HARNESS-ONLY.
e2e_recover_oauth_token() {
    if [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
        return 0
    fi
    local scan_pid=$$ parent recovered
    for _ in 1 2 3 4 5 6 7 8; do
        parent=$(awk '{print $4}' "/proc/$scan_pid/stat" 2>/dev/null || true)
        if [ -z "$parent" ] || [ "$parent" = "0" ]; then
            break
        fi
        if [ -r "/proc/$parent/environ" ]; then
            recovered=$(tr '\0' '\n' < "/proc/$parent/environ" \
                | grep '^CLAUDE_CODE_OAUTH_TOKEN=' | cut -d= -f2- || true)
            if [ -n "$recovered" ]; then
                export CLAUDE_CODE_OAUTH_TOKEN="$recovered"
                echo "  (recovered CLAUDE_CODE_OAUTH_TOKEN from ancestor pid=$parent)"
                return 0
            fi
        fi
        scan_pid=$parent
    done
    return 0
}

# QUM-325: dedicated tmux socket for sandbox isolation.
e2e_setup_tmux_socket() {
    local prefix=${1:-sprawl-e2e}
    SPRAWL_TMUX_SOCKET="${SPRAWL_TMUX_SOCKET:-${prefix}-$$}"
    export SPRAWL_TMUX_SOCKET
}

_stmux() {
    tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"
}

e2e_require_claude_or_skip() {
    local name=${1:-test}
    if command -v claude >/dev/null 2>&1; then
        return 0
    fi
    if [ "${SPRAWL_E2E_SKIP_NO_CLAUDE:-}" = "1" ]; then
        echo "SKIP: claude binary not found on PATH (SPRAWL_E2E_SKIP_NO_CLAUDE=1 set)"
        exit 0
    fi
    echo "FATAL: claude binary not found on PATH (required by $name)" >&2
    echo "       Set SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip this test instead." >&2
    exit 1
}

e2e_require_tmux() {
    if ! command -v tmux >/dev/null 2>&1; then
        echo "FATAL: tmux binary not found on PATH" >&2
        exit 1
    fi
}

e2e_require_jq() {
    if ! command -v jq >/dev/null 2>&1; then
        echo "FATAL: jq binary not found on PATH" >&2
        exit 1
    fi
}

e2e_build_sprawl() {
    if [ -n "${SPRAWL_BIN:-}" ] && [ -x "$SPRAWL_BIN" ]; then
        export SPRAWL_BIN
        return 0
    fi
    make -C "$REPO_ROOT" build >/dev/null
    SPRAWL_BIN="$REPO_ROOT/sprawl"
    export SPRAWL_BIN
    if [ ! -x "$SPRAWL_BIN" ]; then
        echo "FATAL: sprawl binary not found at $SPRAWL_BIN" >&2
        exit 1
    fi
}

e2e_make_sandbox_root() {
    local prefix=${1:-sprawl-e2e}
    local d
    d=$(mktemp -d "${TMPDIR:-/tmp}/${prefix}-XXXXXX")
    local real
    real="$(cd "$d" 2>/dev/null && pwd -P || echo "$d")"
    case "$real" in
        /tmp/*) ;;
        *)
            echo "FATAL: sandbox SPRAWL_ROOT=$real not under /tmp/; aborting" >&2
            exit 1
            ;;
    esac
    SPRAWL_ROOT="$real"
    export SPRAWL_ROOT
}

e2e_init_sandbox_repo() {
    git -C "$SPRAWL_ROOT" init -b main --quiet
    git -C "$SPRAWL_ROOT" config user.name "Test"
    git -C "$SPRAWL_ROOT" config user.email "test@test"
    git -C "$SPRAWL_ROOT" commit --allow-empty -m "init" --quiet
    mkdir -p "$SPRAWL_ROOT/.sprawl"
    echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"
}

_e2e_cleanup() {
    local rc=$?
    if [ -n "${PHANTOM_PID:-}" ]; then
        kill "$PHANTOM_PID" 2>/dev/null || true
    fi
    if [ -n "${SPRAWL_TMUX_SOCKET:-}" ]; then
        tmux -L "$SPRAWL_TMUX_SOCKET" kill-server 2>/dev/null || true
        rm -f -- "/tmp/tmux-$(id -u)/$SPRAWL_TMUX_SOCKET" 2>/dev/null || true
    fi
    case "${SPRAWL_ROOT:-}" in
        /tmp/*)
            local attempt
            for attempt in 1 2 3 4 5; do
                if rm -rf -- "$SPRAWL_ROOT" 2>/dev/null; then
                    break
                fi
                sleep 1
            done
            if [ -d "$SPRAWL_ROOT" ]; then
                echo "  WARN: cleanup could not fully remove $SPRAWL_ROOT (watchdog will reap)" >&2
            fi
            ;;
    esac
    exit "$rc"
}

e2e_install_cleanup_traps() {
    trap _e2e_cleanup EXIT INT TERM HUP
    # QUM-458 layer 1: setsid'd watchdog reaps if driver dies via SIGKILL.
    local libdir
    libdir="$(dirname "${BASH_SOURCE[0]}")"
    # shellcheck source=sandbox-traps.sh
    . "$libdir/sandbox-traps.sh"
    sandbox_install_watchdog "$$" "${SPRAWL_TMUX_SOCKET:-}" "${SPRAWL_ROOT:-}"
}

capture_pane() {
    _stmux capture-pane -t "$1" -p 2>/dev/null || true
}

wait_for_pattern() {
    local session="$1" pattern="$2" timeout="$3"
    local elapsed=0
    while [ "$elapsed" -lt "$timeout" ]; do
        if capture_pane "$session" | grep -qE "$pattern"; then
            # QUM-671: emit a parseable elapsed-time record so consumers
            # (e.g. the S3 startup-time regression gate fed by
            # `recover-live.sh`'s TUI-rendered wait) have a comparable
            # number. Format is `WAIT_FOR_PATTERN_ELAPSED <secs> <pattern>`
            # — fixed prefix so a future scraper can grep without
            # ambiguity. Backward compatible: existing callers only
            # inspect the return code.
            echo "WAIT_FOR_PATTERN_ELAPSED ${elapsed}s pattern=${pattern}"
            return 0
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    return 1
}

wait_for_pattern_fast() {
    local session="$1" pattern="$2" timeout="$3"
    local start="$SECONDS"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        if capture_pane "$session" | grep -qE "$pattern"; then
            # QUM-671: see wait_for_pattern above. Mirrored here so any
            # consumer that switches between the slow and fast variants
            # gets identical elapsed-time telemetry.
            echo "WAIT_FOR_PATTERN_ELAPSED $((SECONDS - start))s pattern=${pattern}"
            return 0
        fi
        sleep 0.2
    done
    return 1
}

wait_for_substring_fast() {
    local session="$1" needle="$2" timeout="$3"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        if capture_pane "$session" | grep -qF "$needle"; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

e2e_launch_tui() {
    local session="$1"
    local cols="${2:-200}"
    local rows="${3:-50}"
    local stderr_log="${SPRAWL_ROOT}/.sprawl/tui-stderr.log"
    _stmux new-session -d -s "$session" -x "$cols" -y "$rows" \
        "SPRAWL_ROOT='$SPRAWL_ROOT' '$SPRAWL_BIN' enter 2>'$stderr_log'"
    _stmux set-option -t "$session" window-size manual >/dev/null
    _stmux resize-window -t "$session" -x "$cols" -y "$rows" >/dev/null
    # QUM-656: tree migrated from a left-pane "weave (idle)" row into the
    # header orbital row rendered as `weave ──●`. We wait for the root token
    # as proxy for "supervisor data has propagated to the tree renderer".
    if ! wait_for_pattern "$session" "weave " 45; then
        echo "  FAIL: TUI did not render 'weave' root in header tree within 45s" >&2
        echo "  pane tail:" >&2
        capture_pane "$session" | tail -30 >&2
        echo "  stderr log tail:" >&2
        [ -f "$stderr_log" ] && tail -20 "$stderr_log" >&2
        return 1
    fi
    return 0
}

# QUM-327 phantom client workaround: detached tmux sessions deliver input
# only when at least one client is attached. `script -q -c "tmux attach -d"`
# keeps a non-interactive attachment alive without stealing the user's tty.
e2e_attach_phantom_client() {
    local session="$1"
    script -q -c "tmux ${SPRAWL_TMUX_SOCKET:+-L $SPRAWL_TMUX_SOCKET} attach -t $session -d" /dev/null &
    PHANTOM_PID=$!
    export PHANTOM_PID
    sleep 1
}

e2e_send_user_prompt() {
    # QUM-432: the TUI's paste classifier reclassifies an Enter arriving
    # < 10ms after a printable key as an embedded newline (stripped-
    # bracketed-paste burst). Pause between the text and the Enter so the
    # submit lands as a discrete keystroke. Send text without -l so tmux
    # key-name parsing keeps the original test-drain/test-handoff behavior.
    local session="$1" text="$2"
    _stmux send-keys -t "$session" "$text"
    sleep 0.5
    _stmux send-keys -t "$session" Enter
}
