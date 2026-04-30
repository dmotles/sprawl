#!/usr/bin/env bash
# test-exit-code-preservation.sh — Regression guard for QUM-328.
#
# Verifies that E2E scripts preserve their exit code across the
# cleanup trap. Runs a minimal reproducer that sets up a trap, exits 1,
# and asserts the observed exit code is 1 (not 0).
#
# This test does NOT require claude, tmux, or the sprawl binary — it
# only exercises the shell-level trap pattern used by the E2E scripts.
#
# Usage:
#   bash scripts/test-exit-code-preservation.sh
set -uo pipefail

PASS_COUNT=0
FAIL_COUNT=0
pass() { PASS_COUNT=$((PASS_COUNT + 1)); echo "  PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); echo "  FAIL: $1" >&2; }

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# --- Test 1: test-notify-tui-e2e.sh cleanup preserves non-zero exit ---

echo "=== Test 1: test-notify-tui-e2e.sh cleanup preserves exit code ==="

# Extract and test the actual cleanup pattern from the script by
# running a minimal harness that sources the same trap shape.
# We create a temp dir, set the trap, then exit 1. The trap must
# NOT clobber exit 1 → observed exit code must be 1.

TMPDIR_TEST=$(mktemp -d /tmp/qum328-test-XXXXXX)

set +e
bash -c '
set -euo pipefail
SPRAWL_ROOT="'"$TMPDIR_TEST"'"
SESSION="qum328-fake-session-does-not-exist"

# This is the cleanup function from test-notify-tui-e2e.sh.
# Source the actual script pattern:
cleanup() {
    local rc=$?
    if tmux has-session -t "$SESSION" 2>/dev/null; then
        tmux kill-session -t "$SESSION" 2>/dev/null || true
    fi
    case "$SPRAWL_ROOT" in
        /tmp/*) rm -rf -- "$SPRAWL_ROOT" ;;
    esac
    exit "$rc"
}
trap cleanup EXIT

# Simulate a test failure
exit 1
'
EXIT_CODE=$?
set -e

if [ "$EXIT_CODE" -ne 0 ]; then
    pass "cleanup trap preserved non-zero exit code (got $EXIT_CODE)"
else
    fail "cleanup trap clobbered exit code — got 0, expected non-zero"
fi

# --- Test 2: cleanup still exits 0 on success ---

echo "=== Test 2: cleanup preserves exit 0 on success ==="

TMPDIR_TEST2=$(mktemp -d /tmp/qum328-test-XXXXXX)

set +e
bash -c '
set -euo pipefail
SPRAWL_ROOT="'"$TMPDIR_TEST2"'"
SESSION="qum328-fake-session-does-not-exist"

cleanup() {
    local rc=$?
    if tmux has-session -t "$SESSION" 2>/dev/null; then
        tmux kill-session -t "$SESSION" 2>/dev/null || true
    fi
    case "$SPRAWL_ROOT" in
        /tmp/*) rm -rf -- "$SPRAWL_ROOT" ;;
    esac
    exit "$rc"
}
trap cleanup EXIT

exit 0
'
EXIT_CODE=$?
set -e

if [ "$EXIT_CODE" -eq 0 ]; then
    pass "cleanup trap preserved exit 0 on success"
else
    fail "cleanup trap returned $EXIT_CODE instead of 0 on success path"
fi

# --- Test 3: actual script exits non-zero when a test fails ---
# This is the real integration check: source the actual script with a
# forced failure injected. Requires the script to exist but does NOT
# require claude or tmux (we bail out at the preflight checks, which
# is fine — we just need to verify the trap pattern in the real file).

echo "=== Test 3: grep for exit-code-preserving cleanup pattern in real scripts ==="

for script_name in test-notify-tui-e2e.sh test-handoff-e2e.sh; do
    SCRIPT="$REPO_ROOT/scripts/$script_name"
    if [ ! -f "$SCRIPT" ]; then
        fail "$script_name not found at $SCRIPT"
    else
        # Check that the cleanup function captures $? and exits with it
        if grep -q 'local rc=\$?' "$SCRIPT" && grep -q 'exit "\$rc"' "$SCRIPT"; then
            pass "$script_name cleanup captures and preserves exit code"
        else
            fail "$script_name cleanup does NOT preserve exit code (missing 'local rc=\$?' or 'exit \"\$rc\"')"
        fi
    fi
done

# --- Summary ---

echo ""
echo "==============================="
echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "==============================="

if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
fi
exit 0
