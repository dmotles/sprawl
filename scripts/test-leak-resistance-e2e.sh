#!/usr/bin/env bash
# test-leak-resistance-e2e.sh — QUM-458 §6 validation harness.
#
# For each TUI-mode e2e driver script:
#   1. Run it backgrounded.
#   2. Wait long enough for the sandbox + claude subprocess to come up.
#   3. SIGKILL the driver.
#   4. Wait for the defense-in-depth layers to reap.
#   5. Assert ZERO of: orphan claude procs, stale tmux sockets, leaked /tmp dirs.
#
# Gate: SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip when claude is not on PATH.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if ! command -v claude >/dev/null 2>&1; then
    if [ "${SPRAWL_E2E_SKIP_NO_CLAUDE:-0}" = "1" ]; then
        echo "skip: claude not on PATH and SPRAWL_E2E_SKIP_NO_CLAUDE=1"
        exit 0
    fi
    echo "FAIL: claude not on PATH (set SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip)" >&2
    exit 1
fi

UID_VAL="$(id -u)"
PASS=0
FAIL=0

# leak_check <pattern_prefix> — assert zero residue of the named test sandbox.
leak_check() {
    local prefix="$1"
    local rc=0

    if pgrep -fa "claude.*--system-prompt-file=/tmp/${prefix}" >/dev/null 2>&1; then
        echo "  LEAK: orphan claude with --system-prompt-file under /tmp/${prefix}*" >&2
        pgrep -fa "claude.*--system-prompt-file=/tmp/${prefix}" >&2 || true
        rc=1
    fi
    if ls "/tmp/tmux-${UID_VAL}/${prefix}"* >/dev/null 2>&1; then
        echo "  LEAK: stale tmux socket /tmp/tmux-${UID_VAL}/${prefix}*" >&2
        ls "/tmp/tmux-${UID_VAL}/${prefix}"* >&2 || true
        rc=1
    fi
    if ls -d "/tmp/${prefix}"* >/dev/null 2>&1; then
        echo "  LEAK: residual /tmp/${prefix}* directory" >&2
        ls -d "/tmp/${prefix}"* >&2 || true
        rc=1
    fi
    return $rc
}

dump_diagnostics() {
    echo "--- diagnostics ---" >&2
    ps -ef | grep -E 'sprawl|claude' | grep -v grep >&2 || true
    echo "--- /tmp/tmux-* sockets ---" >&2
    ls /tmp/tmux-*/sprawl-* 2>/dev/null >&2 || true
    echo "--- /tmp/sprawl-* dirs ---" >&2
    ls -d /tmp/sprawl-* 2>/dev/null >&2 || true
    echo "-------------------" >&2
}

run_case() {
    local script="$1"
    local prefix="$2"
    local label="$3"

    echo "=== $label ==="
    bash "$REPO_ROOT/scripts/$script" >/tmp/leak-resistance-driver.log 2>&1 &
    local driver=$!

    # Poll up to 60s for the sandbox dir to appear, then SIGKILL ASAP. On fast
    # boxes the driver may finish cleanly before we can SIGKILL — that's fine,
    # we still run the leak assertions: any clean exit must also be leak-free.
    local waited=0
    while [ "$waited" -lt 60 ]; do
        if ls -d "/tmp/${prefix}"* >/dev/null 2>&1; then
            break
        fi
        if ! kill -0 "$driver" 2>/dev/null; then
            break
        fi
        sleep 1
        waited=$((waited + 1))
    done

    if kill -0 "$driver" 2>/dev/null; then
        kill -9 "$driver" 2>/dev/null || true
        wait "$driver" 2>/dev/null || true
        echo "  driver SIGKILL'd after ${waited}s"
    else
        wait "$driver" 2>/dev/null || true
        echo "  driver exited cleanly before SIGKILL window — running leak-check anyway"
    fi
    sleep 10

    if leak_check "$prefix"; then
        echo "  PASS: $label"
        PASS=$((PASS + 1))
    else
        echo "  FAIL: $label leaked" >&2
        dump_diagnostics
        FAIL=$((FAIL + 1))
    fi
}

run_case "test-handoff-e2e.sh"     "sprawl-handoff-e2e-"   "handoff-e2e"
run_case "test-notify-tui-e2e.sh"  "sprawl-notify-e2e-"    "notify-tui-e2e"
run_case "test-tui-e2e.sh"         "sprawl-tui-e2e-"       "tui-e2e"

echo ""
echo "=== Summary: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -ne 0 ]; then
    exit 1
fi
exit 0
