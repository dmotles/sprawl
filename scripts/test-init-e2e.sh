#!/usr/bin/env bash
# test-init-e2e.sh - End-to-end smoke test for `sprawl init --detached` (tmux mode).
#
# Guards against the QUM-261 class of regression: a change to
# internal/claude/ or cmd/rootloop.go silently breaks the interactive tmux
# path even though `make validate` is green. The entire unit-test matrix
# for that code uses a fake /bin/sh and cannot observe TTY semantics.
#
# What this script does:
#   1. Builds ./sprawl.
#   2. Sets up an isolated sandbox under /tmp/ via scripts/sprawl-test-env.sh
#      (which internally runs `sprawl init --detached`).
#   3. Polls the resulting tmux pane until either a success marker or an
#      assertion-failing marker appears, bounded by a timeout.
#   4. Asserts the pane does NOT contain known-bad output:
#        - "when using --print"            (QUM-261 symptom)
#        - "Input must be provided"        (QUM-261 symptom)
#        - repeated "[root-loop] session ended, restarting"   (bash loop thrash)
#      and DOES contain evidence of interactive Claude startup.
#   5. Cleans up via the sanctioned sprawl_sandbox_destroy helper.
#
# Requires a real `claude` binary on PATH. If absent:
#   - SPRAWL_E2E_SKIP_NO_CLAUDE=1 → skip (exit 0) with a clear message
#   - otherwise                   → exit 1 with a clear message
#
# Usage:
#   bash scripts/test-init-e2e.sh
set -euo pipefail

# QUM-337: this script intentionally exercises the deprecated `sprawl init`
# CLI path. Suppress the per-process deprecation warning so the captured
# output stays clean during the M13 cutover soak. Slated for deletion in
# Phase 2.5 alongside the tmux-mode removal.
export SPRAWL_QUIET_DEPRECATIONS=1

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# --- Preflight: claude binary ---

if ! command -v claude >/dev/null 2>&1; then
    if [ "${SPRAWL_E2E_SKIP_NO_CLAUDE:-}" = "1" ]; then
        echo "SKIP: claude binary not found on PATH (SPRAWL_E2E_SKIP_NO_CLAUDE=1 set)"
        exit 0
    fi
    echo "FATAL: claude binary not found on PATH — init e2e requires a real claude" >&2
    echo "       Set SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip this test instead." >&2
    exit 1
fi

# --- Preflight: tmux binary ---

if ! command -v tmux >/dev/null 2>&1; then
    echo "FATAL: tmux binary not found on PATH" >&2
    exit 1
fi

# --- Run from a safe cwd (sprawl-test-env.sh refuses to run from worktrees) ---

WRAP_DIR=$(mktemp -d /tmp/sprawl-init-e2e-XXXXXX)
# Guard the wrapper cleanup path: only rm -rf if WRAP_DIR is under /tmp/.
case "$WRAP_DIR" in
    /tmp/*) ;;
    *)
        echo "FATAL: wrapper dir $WRAP_DIR is not under /tmp/; refusing to continue" >&2
        exit 1
        ;;
esac
cd "$WRAP_DIR"

# --- Capture pane helper (used before $SPRAWL_NAMESPACE is set too, so take a
# --- session name as arg) ---

capture_pane() {
    tmux capture-pane -t "$1" -pS - 2>/dev/null || true
}

# --- Set up sandbox. This builds sprawl and runs `sprawl init --detached`. ---

echo "=== Setting up sandbox via sprawl-test-env.sh ==="
# shellcheck disable=SC1090
eval "$(bash "$REPO_ROOT/scripts/sprawl-test-env.sh")"

# sprawl-test-env.sh exports: SPRAWL_BIN, SPRAWL_ROOT, SPRAWL_NAMESPACE, TEST_NS
: "${SPRAWL_BIN:?sprawl-test-env.sh did not export SPRAWL_BIN}"
: "${SPRAWL_NAMESPACE:?sprawl-test-env.sh did not export SPRAWL_NAMESPACE}"
: "${SPRAWL_ROOT:?sprawl-test-env.sh did not export SPRAWL_ROOT}"

SESSION="$SPRAWL_NAMESPACE"

# --- Extra cleanup trap: the sprawl-test-env.sh trap handles SPRAWL_ROOT and
# --- the namespace'd tmux session. We add removal of our WRAP_DIR. Chain it
# --- rather than overwrite by invoking sprawl_sandbox_destroy explicitly. ---

FAIL_COUNT=0
PASS_COUNT=0

pass() { PASS_COUNT=$((PASS_COUNT + 1)); echo "  PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); echo "  FAIL: $1" >&2; }

cleanup() {
    # Defensive: sprawl_sandbox_destroy reasserts /tmp/ guard internally.
    if declare -F sprawl_sandbox_destroy >/dev/null 2>&1; then
        sprawl_sandbox_destroy || true
    fi
    # Remove wrapper dir (under /tmp/ per guard above).
    case "$WRAP_DIR" in
        /tmp/*) rm -rf -- "$WRAP_DIR" ;;
    esac
}
trap cleanup EXIT

# --- Verify the tmux session exists ---

echo ""
echo "=== Test: tmux session created ==="
if tmux has-session -t "$SESSION" 2>/dev/null; then
    pass "tmux session '$SESSION' is alive"
else
    fail "tmux session '$SESSION' not created by sprawl init --detached"
    echo ""
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    echo "==============================="
    exit 1
fi

# --- Wait for root-loop to enter its launch phase ---
#
# The bash loop in init.go prints "[root-loop] starting session <id>" right
# before exec'ing claude. Seeing that means rootinit.Prepare succeeded.

echo ""
echo "=== Test: root-loop started ==="
TIMEOUT=15
elapsed=0
while [ "$elapsed" -lt "$TIMEOUT" ]; do
    if capture_pane "$SESSION" | grep -q "\[root-loop\] starting session"; then
        pass "root-loop printed 'starting session' marker"
        break
    fi
    sleep 1
    elapsed=$((elapsed + 1))
done
if [ "$elapsed" -ge "$TIMEOUT" ]; then
    fail "root-loop did not print 'starting session' within ${TIMEOUT}s"
    echo "  Pane content:" >&2
    capture_pane "$SESSION" | tail -30 >&2
fi

# --- Give claude time to produce interactive UI. The QUM-261 bug surfaced
# --- within ~1s (claude printed the --print error and exited); tmux mode
# --- claude typically renders its interactive box within a couple seconds. ---

sleep 8

PANE="$(capture_pane "$SESSION")"

# --- Assertion 1: NO --print mode error (QUM-261) ---

echo ""
echo "=== Test: no --print mode error ==="
if echo "$PANE" | grep -q "when using --print"; then
    fail "pane contains 'when using --print' — QUM-261 regression"
    echo "  Offending lines:" >&2
    echo "$PANE" | grep -n "when using --print" | head -5 >&2
else
    pass "pane does not contain 'when using --print'"
fi

if echo "$PANE" | grep -q "Input must be provided"; then
    fail "pane contains 'Input must be provided' — QUM-261 regression"
else
    pass "pane does not contain 'Input must be provided'"
fi

# --- Assertion 2: bash loop is NOT thrashing ---
#
# One "[root-loop] session ended, restarting" is possible on a clean exit,
# but in the QUM-261 bug the loop cycled many times per second because
# claude failed immediately. >2 occurrences in ~10s of runtime is bad.

echo ""
echo "=== Test: bash restart loop not thrashing ==="
LOOP_COUNT=$(echo "$PANE" | grep -c "\[root-loop\] session ended, restarting" || true)
FAIL_COUNT_LOOP=$(echo "$PANE" | grep -c "\[root-loop\] session failed" || true)
if [ "$LOOP_COUNT" -gt 2 ] || [ "$FAIL_COUNT_LOOP" -gt 2 ]; then
    fail "root-loop thrashing (session ended x$LOOP_COUNT, session failed x$FAIL_COUNT_LOOP)"
    echo "  Pane tail:" >&2
    echo "$PANE" | tail -30 >&2
else
    pass "root-loop not thrashing (session ended x$LOOP_COUNT, session failed x$FAIL_COUNT_LOOP)"
fi

# --- Assertion 3: interactive claude UI is visible ---
#
# We pick markers that are stable across Claude Code versions and localized
# editions. "? for shortcuts" is the footer hint in the interactive prompt.
# Fallback markers cover the trust prompt and the default input box glyphs.

echo ""
echo "=== Test: interactive claude UI visible ==="
INTERACTIVE_MARKER=""
for marker in "for shortcuts" "Accessing workspace" "trust this folder" "Bypassing Permissions" "? for"; do
    if echo "$PANE" | grep -qF "$marker"; then
        INTERACTIVE_MARKER="$marker"
        break
    fi
done
if [ -n "$INTERACTIVE_MARKER" ]; then
    pass "pane shows interactive marker: '$INTERACTIVE_MARKER'"
else
    fail "pane has no recognised interactive-claude marker"
    echo "  Pane tail (for debugging — update marker list if claude UI changed):" >&2
    echo "$PANE" | tail -20 >&2
fi

# --- Summary ---

echo ""
echo "==============================="
echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "==============================="

if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
fi
exit 0
