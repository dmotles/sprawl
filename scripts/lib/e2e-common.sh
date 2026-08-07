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

# QUM-1029: a row that asserts NOTHING must not report PASS. This used to
# return non-zero only when FAIL_COUNT>0, so a row that recorded neither a pass
# nor a fail rendered "0 passed, 0 failed", returned 0, and scripts/e2e-matrix.sh
# — which reads a row's EXIT STATUS ONLY and is structurally unable to see how
# much the row asserted — counted it in `pass_count` and in the
# "=== Matrix: N/N passed ===" line CLAUDE.md tells readers to scrape.
#
# The floor is PER-ROW and CALLER-SUPPLIED: each row declares a top-level
# `MIN_ASSERTIONS=<n>`. Deliberately NOT derived from anything measured here —
# a floor computed from the corpus it checks is satisfied by an empty corpus,
# which is the very defect. For the same reason an UNDECLARED floor fails
# rather than defaulting: a default would let every existing row keep the
# defect silently. So does a declared 0, which is that default wearing a
# declaration.
#
# A breach is an ORDINARY FAILURE (the row returns 1, the driver buckets it
# FAIL and exits 1). It is deliberately not a skip (3) or an internal-invariant
# violation (4): a row that asserted nothing is a defect in the row, not an
# unmet precondition and not a driver fault.
#
# What to declare: the minimum of PASS_COUNT+FAIL_COUNT over the paths that
# COMPLETE THE ROW SUCCESSFULLY — reaching this function having called fail()
# zero times and without an early return. Deliberately NOT the minimum over all
# paths that reach here: a path that already called fail(), or that bailed
# early, has already failed the row, so a floor that also fires on it costs a
# line of extra diagnosis and nothing else. The only way a floor can turn an
# HONEST run red is by exceeding what a legitimately-passing path asserts —
# hence the minimum is taken over passing paths only. (Taking it over all paths
# collapses almost every row to 1 or 0 via its `launch failed -> print -> return
# 1` shortcut, which eliminates nothing.)
#
# On that green path: an if/else whose arms are pass/fail contributes 1; an
# asymmetric if/else whose other arm only echoes a note contributes 0; a loop
# contributes 0 unless it iterates a fixed literal list; a block that a
# successful run can skip entirely contributes 0.
#
# Only builtins here: the matrix driver's own unit suite invokes it with PATH
# scrubbed, and `${MIN_ASSERTIONS-}` (not a bare reference) keeps `set -u` from
# aborting the row before this can diagnose it.
e2e_print_results() {
    echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
    local declared=${MIN_ASSERTIONS-}
    local observed=$((PASS_COUNT + FAIL_COUNT))
    local breach=""
    case "$declared" in
        '')
            breach="the row declared no MIN_ASSERTIONS floor — add a top-level MIN_ASSERTIONS=<n> naming the fewest assertions a complete run of this row makes (QUM-1029)"
            ;;
        # Compared as a STRING, never evaluated: `$(( ))` on a caller-supplied
        # value executes any command substitution inside it.
        *[!0-9]* | 0*)
            breach="MIN_ASSERTIONS='$declared' is not a positive whole number, so it is not a floor — a floor of 0 is satisfied by a row that asserts nothing (QUM-1029)"
            ;;
        *)
            if [ "$observed" -lt "$declared" ]; then
                breach="only $observed assertion(s) ran but MIN_ASSERTIONS=$declared — the row measured less than it claims (QUM-1029)"
            fi
            ;;
    esac
    if [ -n "$breach" ]; then
        echo "  FAIL: $breach" >&2
        return 1
    fi
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

# QUM-952: a skipped row must never be indistinguishable from a passed one.
# `exit 0` cannot mean both, so a skip exits 77 (the autotools/`make check` SKIP
# convention) AND records its reason in the driver-owned sentinel named by
# $E2E_SKIP_FILE. The driver requires BOTH before it will believe a skip, so a
# row that merely happens to exit 77 cannot forge one.
E2E_SKIP_EXIT=77

# Declare the current row skipped and abort it. UNCONDITIONAL by design: the
# decision of *whether* a missing dependency is skippable belongs to the caller
# (e.g. e2e_require_claude_or_skip consults SPRAWL_E2E_SKIP_NO_CLAUDE); this
# function only signals the outcome. Do not gate it on that variable.
e2e_skip_row() {
    local reason=${1:-unspecified}
    # $E2E_SKIP_FILE is unset when the lib is sourced outside the driver; a skip
    # must still be signalled. The `|| true` matters under `set -e`: a failed
    # redirect would exit 1 before `exit 77`, turning a skip into a failure.
    if [ -n "${E2E_SKIP_FILE:-}" ]; then
        { printf '%s\n' "$reason" >>"$E2E_SKIP_FILE"; } 2>/dev/null || true
    fi
    echo "SKIP: $reason"
    exit "$E2E_SKIP_EXIT"
}

e2e_require_claude_or_skip() {
    local name=${1:-test}
    if command -v claude >/dev/null 2>&1; then
        return 0
    fi
    if [ "${SPRAWL_E2E_SKIP_NO_CLAUDE:-}" = "1" ]; then
        e2e_skip_row "claude binary not found on PATH (required by $name; SPRAWL_E2E_SKIP_NO_CLAUDE=1 set)"
    fi
    # The FATAL branch must NOT touch the sentinel: a hard failure that also
    # wrote it could be laundered into a skip by any stale-read bug upstream.
    echo "FATAL: claude binary not found on PATH (required by $name)" >&2
    echo "       Set SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip this test instead (the skip is" >&2
    echo "       reported as a skip, not a pass, and exits nonzero -- it does not" >&2
    echo "       discharge a mandatory-gate obligation)." >&2
    echo "       This gate keys on ABSENCE only and never probes auth: if claude is" >&2
    echo "       installed but unauthenticated the gate does not fire, the row runs, and" >&2
    echo "       it fails with 'Not logged in'. The flag is not the remedy for that, and" >&2
    echo "       never hide claude from PATH to force a skip." >&2
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
            # `wake-live.sh`'s TUI-rendered wait) have a comparable
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

# _e2e_weave_lock_diagnostic LOCK ATTEMPTS — explain who is still holding the
# lock. The file body is the last PID that successfully ACQUIRED (rootinit
# writes it after locking), which is not necessarily the current holder, so the
# live holder is reported separately and best-effort.
_e2e_weave_lock_diagnostic() {
    local lock="$1" attempts="$2"
    echo "  weave.lock still held: $lock (attempts=$attempts)" >&2
    local recorded=""
    [ -r "$lock" ] && recorded=$(tr -dc '0-9' <"$lock" 2>/dev/null | head -c 12)
    if [ -n "$recorded" ]; then
        echo "  recorded PID (lock file body): $recorded" >&2
        if command -v ps >/dev/null 2>&1; then
            ps -o pid=,ppid=,etime=,args= -p "$recorded" >&2 2>/dev/null || \
                echo "    (PID $recorded is gone — the body is stale)" >&2
        fi
    else
        echo "  recorded PID: none (lock file body empty or unreadable)" >&2
    fi
    local live=""
    if command -v fuser >/dev/null 2>&1; then
        live=$(fuser "$lock" 2>/dev/null | tr -s ' ')
    fi
    if [ -z "$live" ] && command -v lsof >/dev/null 2>&1; then
        live=$(lsof -t -- "$lock" 2>/dev/null | tr '\n' ' ')
    fi
    if [ -n "$live" ]; then
        echo "  live holder PID(s): $live" >&2
        local p
        for p in $live; do
            command -v ps >/dev/null 2>&1 && ps -o pid=,ppid=,etime=,args= -p "$p" >&2 2>/dev/null
        done
    else
        echo "  live holder PID(s): unknown (fuser/lsof unavailable or reported nothing)" >&2
    fi
    return 0
}

# e2e_wait_weave_lock_free [ROOT] [TIMEOUT_SECS] — QUM-948. Block until
# `<ROOT>/.sprawl/memory/weave.lock` is acquirable, or fail.
#
# Why a poll and not a sleep: `tmux kill-session` only signals the pane, while
# the flock rootinit.AcquireWeaveLock holds is released by the kernel when the
# dying process's fd closes. A kill-then-relaunch path that sleeps a fixed 2s
# and then launches loses the race whenever teardown takes longer than that,
# and `sprawl enter` dies with "another weave session is already running".
# Lengthening the sleep would only trade a fast flake for a slow one and would
# hide a genuinely leaked lock; so we retry with backoff to a bounded deadline
# and FAIL loudly (never hang, never silently succeed) if the lock outlives it.
#
# Probe-only: `flock -n` releases immediately, so this holds nothing itself and
# cannot become the blocker for the launch it precedes.
e2e_wait_weave_lock_free() {
    local root="${1:-${SPRAWL_ROOT:-}}"
    local timeout="${2:-${SPRAWL_E2E_LOCK_WAIT_SECS:-30}}"
    if [ -z "$root" ]; then
        echo "  WARN: e2e_wait_weave_lock_free called with no root — skipping weave.lock wait" >&2
        return 0
    fi
    # A non-numeric timeout would reach `$((SECONDS + timeout))`, where bash
    # treats it as a VARIABLE NAME: under the driver's `set -u` that aborts the
    # whole ROW with a bare "10s: unbound variable" naming neither the knob nor
    # the row. "10s" and "1e3" are exactly what an operator reaches for.
    if ! [[ "$timeout" =~ ^[0-9]+$ ]]; then
        echo "  WARN: SPRAWL_E2E_LOCK_WAIT_SECS='$timeout' is not a whole number of seconds — using 30" >&2
        timeout=30
    fi
    local lock="$root/.sprawl/memory/weave.lock"
    # Absent lock file (or absent memory dir) means nothing can be holding it.
    # Checked rather than probed on purpose: `flock -n` would CREATE the file,
    # and fails with a distinct "cannot open" status when the dir is missing.
    [ -f "$lock" ] || return 0
    if ! command -v flock >/dev/null 2>&1; then
        echo "  WARN: flock(1) not found — cannot wait for weave.lock release; relaunch may race teardown" >&2
        return 0
    fi

    local start="$SECONDS"
    local end=$((SECONDS + timeout))
    local delay="0.2" attempts=0 rc=0
    while :; do
        attempts=$((attempts + 1))
        # -E 9 separates "held by someone else" (9) from "cannot open" (66):
        # without it both are 1 and an unprobeable path would look like
        # contention forever.
        rc=0
        flock -n -E 9 -x "$lock" true 2>/dev/null || rc=$?
        if [ "$rc" -eq 0 ]; then
            echo "WEAVE_LOCK_WAIT_ELAPSED $((SECONDS - start))s attempts=${attempts} path=${lock}"
            return 0
        fi
        if [ "$rc" -ne 9 ]; then
            echo "  WARN: flock probe on $lock exited $rc (not contention) — not waiting" >&2
            return 0
        fi
        [ "$SECONDS" -lt "$end" ] || break
        sleep "$delay"
        # Exponential backoff capped at 2s: responsive on the common fast
        # release, cheap over a long wait.
        case "$delay" in
        0.2) delay="0.4" ;;
        0.4) delay="0.8" ;;
        0.8) delay="1.6" ;;
        *) delay="2" ;;
        esac
    done
    # Report what we actually waited, not just the nominal deadline: the last
    # backoff sleep can carry us up to 2s past it.
    echo "  FAIL: weave.lock not released within the ${timeout}s deadline (waited $((SECONDS - start))s) — treating as a leaked lock, not waiting further" >&2
    _e2e_weave_lock_diagnostic "$lock" "$attempts"
    return 1
}

e2e_launch_tui() {
    local session="$1"
    local cols="${2:-200}"
    local rows="${3:-50}"
    local stderr_log="${SPRAWL_ROOT}/.sprawl/tui-stderr.log"
    # QUM-948: a relaunch on a root whose previous weave is still tearing down
    # would die with "another weave session is already running". Wait for the
    # flock to actually be released instead of guessing with a fixed sleep.
    if ! e2e_wait_weave_lock_free "$SPRAWL_ROOT"; then
        echo "  FAIL: weave.lock still held before launching session $session — refusing to launch into a doomed acquire" >&2
        return 1
    fi
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
        # QUM-948: a residual TOCTOU between our probe and sprawl's own acquire
        # would otherwise present as 45s of blank pane with no explanation.
        if [ -f "$stderr_log" ] && grep -qF "already running" "$stderr_log"; then
            echo "  NOTE: stderr says another weave session is already running — the weave.lock" >&2
            echo "        was released after our probe but before sprawl acquired it, or a" >&2
            echo "        second session is using this SPRAWL_ROOT." >&2
        fi
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
