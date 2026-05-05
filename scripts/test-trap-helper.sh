#!/usr/bin/env bash
# test-trap-helper.sh — QUM-458 layer 1 test for the shared sandbox-traps
# helper. Verifies that sandbox_install_watchdog spawns a setsid'd companion
# that reaps SPRAWL_ROOT (and tries to kill the tmux server) after the driver
# is SIGKILL'd.
#
# Red phase: scripts/lib/sandbox-traps.sh does not exist yet; sourcing fails
# and the test exits non-zero.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HELPER="$REPO_ROOT/scripts/lib/sandbox-traps.sh"

if [ ! -r "$HELPER" ]; then
    echo "FAIL: $HELPER does not exist (QUM-458 layer 1 helper not yet implemented)" >&2
    exit 1
fi

# shellcheck source=/dev/null
. "$HELPER"

FAKE_ROOT="$(mktemp -d /tmp/sprawl-test-traphelper-XXXXXX)"
FAKE_TMUX_DIR="$(mktemp -d /tmp/tmux-test-traphelper-XXXXXX)"
FAKE_SOCKET="sandbox-traphelper-$$"
touch "$FAKE_TMUX_DIR/$FAKE_SOCKET"

cleanup_test() {
    # Kill the driver in case the test failed between spawn and watchdog
    # install (otherwise it leaks for 60s).
    kill -9 "${DRIVER_PID:-}" 2>/dev/null || true
    rm -rf -- "$FAKE_ROOT" "$FAKE_TMUX_DIR" 2>/dev/null || true
}
trap cleanup_test EXIT

# Spawn a fake driver: setsid'd sleep that the watchdog will poll.
setsid bash -c 'sleep 60' </dev/null >/dev/null 2>&1 &
DRIVER_PID=$!

# Install the watchdog. Contract: arms a setsid'd background reaper that
# polls $DRIVER_PID and runs cleanup when it dies.
sandbox_install_watchdog "$DRIVER_PID" "$FAKE_SOCKET" "$FAKE_ROOT"

sleep 1

# Kill the driver — emulates SIGKILL of the bash test driver.
kill -9 "$DRIVER_PID" 2>/dev/null || true

# Wait up to 10s for the watchdog to remove FAKE_ROOT.
for _ in 1 2 3 4 5 6 7 8 9 10; do
    if [ ! -d "$FAKE_ROOT" ]; then
        break
    fi
    sleep 1
done

if [ -d "$FAKE_ROOT" ]; then
    echo "FAIL: watchdog did not remove $FAKE_ROOT after driver SIGKILL" >&2
    exit 1
fi

echo "PASS: watchdog reaped FAKE_ROOT after driver death"
exit 0
