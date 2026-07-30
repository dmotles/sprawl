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
        # Exit 77 (autotools SKIP), not 0: a skip asserts nothing, and the exit
        # status is the only signal `make` and any non-reading caller sees. Same
        # rule as QUM-952 — the flag acknowledges the diagnostic, not the
        # obligation.
        echo "SKIP: claude not on PATH and SPRAWL_E2E_SKIP_NO_CLAUDE=1 — NOTHING was asserted" >&2
        exit 77
    fi
    echo "FAIL: claude not on PATH (set SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip)" >&2
    exit 1
fi

UID_VAL="$(id -u)"
PASS=0
FAIL=0
# Setup failures are counted SEPARATELY from assertion failures. A case whose
# sandbox never appeared did not pass and did not fail — it never ran, and every
# leak assertion in it is vacuous. Folding the two together is what let this
# harness certify leak-freedom it had never observed (see run_case).
SETUP_FAIL=0
# Assertion-count floor: one verdict per run_case call below, hardcoded rather
# than derived, so a case that silently stops being invoked is caught.
EXPECTED_CASES=3

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
    #
    # saw_sandbox is the POSITIVE CONTROL for this case's negative assertions. Every
    # assertion in leak_check is an absence ("no orphan proc", "no stale socket", "no
    # residual dir"), and absence is satisfied perfectly by a scenario that never
    # started. Without this flag a driver dying in milliseconds — the documented
    # `Not logged in` failure mode — produced `3 passed, 0 failed` and exit 0, i.e.
    # this harness certified leak-freedom it had never observed. Measured with three
    # stub drivers that print "Not logged in" and exit 1: 3 PASS, exit 0.
    local waited=0
    local saw_sandbox=0
    while [ "$waited" -lt 60 ]; do
        if ls -d "/tmp/${prefix}"* >/dev/null 2>&1; then
            saw_sandbox=1
            break
        fi
        if ! kill -0 "$driver" 2>/dev/null; then
            break
        fi
        sleep 1
        waited=$((waited + 1))
    done

    local drc=0
    if kill -0 "$driver" 2>/dev/null; then
        kill -9 "$driver" 2>/dev/null || true
        wait "$driver" 2>/dev/null || true
        echo "  driver SIGKILL'd after ${waited}s"
    else
        wait "$driver" 2>/dev/null
        drc=$?
        echo "  driver exited before SIGKILL window (rc=$drc)"
    fi

    # A second chance at the positive control: a driver that came up and tore down
    # entirely between two polls is unlikely but not impossible, so re-check rather
    # than condemning it on the poll alone.
    if [ "$saw_sandbox" -eq 0 ] && ls -d "/tmp/${prefix}"* >/dev/null 2>&1; then
        saw_sandbox=1
    fi
    if [ "$saw_sandbox" -eq 0 ]; then
        echo "  SETUP-FAIL: $label never created a /tmp/${prefix}* sandbox (driver rc=$drc)." >&2
        echo "    The leak assertions for this case are VACUOUS — there was nothing to leak." >&2
        echo "    This is 'the scenario never started', NOT 'the scenario was leak-free'." >&2
        echo "    Last 15 lines of the driver log:" >&2
        tail -15 /tmp/leak-resistance-driver.log >&2 || true
        SETUP_FAIL=$((SETUP_FAIL + 1))
        return
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
echo "=== Summary: $PASS passed, $FAIL failed, $SETUP_FAIL never ran ==="
TOTAL=$((PASS + FAIL + SETUP_FAIL))
if [ "$TOTAL" -ne "$EXPECTED_CASES" ]; then
    echo "  FAIL: $TOTAL cases accounted for, expected $EXPECTED_CASES — a run_case call vanished, so this run measured less than it claims" >&2
    exit 1
fi
if [ "$SETUP_FAIL" -ne 0 ]; then
    echo "  FAIL: $SETUP_FAIL case(s) never started, so their leak assertions asserted nothing — this run does NOT certify leak-freedom" >&2
    exit 1
fi
if [ "$FAIL" -ne 0 ]; then
    exit 1
fi
exit 0
