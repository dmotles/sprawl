#!/usr/bin/env bash
# test-notify-e2e.sh - End-to-end smoke test for the parent-notification contract.
#
# QUM-310: `[inbox]` notification to the root weave pane must fire for every
# caller of `messages.Send` when `SPRAWL_MESSAGING=legacy` and the recipient
# is the root weave — not just the CLI path. This test drives the regression
# target (`internal/agentops/report.go` → `messages.Send`) by running
# `sprawl report done` from a simulated child agent's identity and asserting
# the weave tmux pane receives an `[inbox] New message from <child>` line.
#
# What it does:
#   1. Builds ./sprawl (via scripts/sprawl-test-env.sh).
#   2. Sets up an isolated sandbox under /tmp/; asserts $SPRAWL_ROOT is there.
#      This also runs `sprawl init --detached` which creates a tmux session
#      with SPRAWL_MESSAGING=legacy set on it.
#   3. Waits for the weave root-loop to reach its launch phase.
#   4. Manually creates a child agent state file listing `weave` as parent.
#   5. Runs `sprawl report done` in a subshell with the child's identity and
#      SPRAWL_MESSAGING=legacy exported.
#   6. tmux capture-pane on the weave window; asserts
#        `[inbox] New message from <child>`
#      appears. Also asserts `sprawl report done` itself exited zero.
#   7. Cleans up via the sanctioned sprawl_sandbox_destroy helper.
#
# Requires a real `claude` binary on PATH (for the weave root-loop to boot
# without thrashing). If absent:
#   - SPRAWL_E2E_SKIP_NO_CLAUDE=1 → skip (exit 0)
#   - otherwise                   → exit 1
#
# Usage:
#   bash scripts/test-notify-e2e.sh
#
# NOTE: This test touches a real tmux session. Do not run it in parallel
# with other tmux-mode e2e scripts.
set -euo pipefail

# QUM-337: this script drives `sprawl report done` and other deprecated
# CLI paths on purpose to verify the QUM-310 notify contract. Suppress
# the per-process deprecation warning so the captured tmux pane output
# stays clean during the M13 cutover soak. Slated for deletion in 2.5.
export SPRAWL_QUIET_DEPRECATIONS=1

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# --- Preflight: claude binary ---

if ! command -v claude >/dev/null 2>&1; then
    if [ "${SPRAWL_E2E_SKIP_NO_CLAUDE:-}" = "1" ]; then
        echo "SKIP: claude binary not found on PATH (SPRAWL_E2E_SKIP_NO_CLAUDE=1 set)"
        exit 0
    fi
    echo "FATAL: claude binary not found on PATH — notify e2e requires a real claude" >&2
    echo "       Set SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip this test instead." >&2
    exit 1
fi

# --- Preflight: tmux binary ---

if ! command -v tmux >/dev/null 2>&1; then
    echo "FATAL: tmux binary not found on PATH" >&2
    exit 1
fi

# --- Run from a safe cwd (sprawl-test-env.sh refuses to run from worktrees) ---

WRAP_DIR=$(mktemp -d /tmp/sprawl-notify-e2e-XXXXXX)
case "$WRAP_DIR" in
    /tmp/*) ;;
    *)
        echo "FATAL: wrapper dir $WRAP_DIR is not under /tmp/; refusing to continue" >&2
        exit 1
        ;;
esac
cd "$WRAP_DIR"

capture_pane() {
    tmux capture-pane -t "$1" -pS - 2>/dev/null || true
}

echo "=== Setting up sandbox via sprawl-test-env.sh ==="
# shellcheck disable=SC1090
eval "$(bash "$REPO_ROOT/scripts/sprawl-test-env.sh")"

: "${SPRAWL_BIN:?sprawl-test-env.sh did not export SPRAWL_BIN}"
: "${SPRAWL_NAMESPACE:?sprawl-test-env.sh did not export SPRAWL_NAMESPACE}"
: "${SPRAWL_ROOT:?sprawl-test-env.sh did not export SPRAWL_ROOT}"

# Hard guard: refuse to continue unless SPRAWL_ROOT is under /tmp/.
case "$SPRAWL_ROOT" in
    /tmp/*) ;;
    *)
        echo "FATAL: SPRAWL_ROOT=$SPRAWL_ROOT not under /tmp/; aborting" >&2
        exit 1
        ;;
esac

SESSION="$SPRAWL_NAMESPACE"
WINDOW="weave"
PANE_TARGET="${SESSION}:${WINDOW}"

FAIL_COUNT=0
PASS_COUNT=0
pass() { PASS_COUNT=$((PASS_COUNT + 1)); echo "  PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); echo "  FAIL: $1" >&2; }

cleanup() {
    if declare -F sprawl_sandbox_destroy >/dev/null 2>&1; then
        sprawl_sandbox_destroy || true
    fi
    case "$WRAP_DIR" in
        /tmp/*) rm -rf -- "$WRAP_DIR" ;;
    esac
}
trap cleanup EXIT

# --- Verify the tmux session exists ---

echo ""
echo "=== Pre-check: tmux session created ==="
if tmux has-session -t "$SESSION" 2>/dev/null; then
    pass "tmux session '$SESSION' is alive"
else
    fail "tmux session '$SESSION' not created by sprawl init --detached"
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    echo "==============================="
    exit 1
fi

# --- Wait for weave root-loop to reach its launch phase ---

echo ""
echo "=== Pre-check: weave root-loop started ==="
TIMEOUT=15
elapsed=0
while [ "$elapsed" -lt "$TIMEOUT" ]; do
    if capture_pane "$SESSION" | grep -q "\[root-loop\] starting session"; then
        pass "weave root-loop printed 'starting session' marker"
        break
    fi
    sleep 1
    elapsed=$((elapsed + 1))
done
if [ "$elapsed" -ge "$TIMEOUT" ]; then
    fail "weave root-loop did not print 'starting session' within ${TIMEOUT}s"
    echo "  Pane tail:" >&2
    capture_pane "$SESSION" | tail -30 >&2
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    echo "==============================="
    exit 1
fi

# --- Advance claude past the "trust this folder" prompt so subsequent
#     send-keys pokes render in claude's interactive TUI input box (which is
#     what `tmux capture-pane` reports). In a fresh /tmp/ sandbox claude
#     always boots at the trust prompt. ---

echo ""
echo "=== Advance claude past trust prompt ==="
TRUST_TIMEOUT=20
elapsed=0
while [ "$elapsed" -lt "$TRUST_TIMEOUT" ]; do
    if capture_pane "$SESSION" | grep -q "trust this folder"; then
        tmux send-keys -t "$PANE_TARGET" "1" Enter
        sleep 1
        break
    fi
    sleep 1
    elapsed=$((elapsed + 1))
done

# Wait for the interactive TUI to render (input box + auto-mode footer).
INTERACTIVE_TIMEOUT=20
elapsed=0
while [ "$elapsed" -lt "$INTERACTIVE_TIMEOUT" ]; do
    if capture_pane "$SESSION" | grep -qE "auto mode on|for shortcuts|\? for"; then
        pass "claude reached interactive TUI state"
        break
    fi
    sleep 1
    elapsed=$((elapsed + 1))
done
if [ "$elapsed" -ge "$INTERACTIVE_TIMEOUT" ]; then
    fail "claude did not reach interactive TUI within ${INTERACTIVE_TIMEOUT}s"
    echo "  Pane tail:" >&2
    capture_pane "$SESSION" | tail -20 >&2
fi

# --- Sanity-check: SPRAWL_MESSAGING=legacy is set on the weave session. ---
# (Propagated by QUM-305; if missing the whole notify path is skipped and the
# test below will fail for a different reason. Fail fast here.)

echo ""
echo "=== Pre-check: SPRAWL_MESSAGING=legacy on weave session ==="
SESSION_ENV="$(tmux show-environment -t "$SESSION" SPRAWL_MESSAGING 2>/dev/null || true)"
if echo "$SESSION_ENV" | grep -q '^SPRAWL_MESSAGING=legacy$'; then
    pass "tmux session env has SPRAWL_MESSAGING=legacy ($SESSION_ENV)"
else
    fail "weave session does not have SPRAWL_MESSAGING=legacy (got: ${SESSION_ENV:-<unset>}) — QUM-305 regression?"
fi

# --- Snapshot the pane *before* we trigger the report, so we can diff after ---

PANE_BEFORE_FILE="$(mktemp /tmp/notify-e2e-before.XXXXXX)"
capture_pane "$SESSION" > "$PANE_BEFORE_FILE"

# --- Register a fake child agent in state, with weave as parent ---

CHILD_NAME="notifykid"
CHILD_STATE_DIR="$SPRAWL_ROOT/.sprawl/agents"
mkdir -p "$CHILD_STATE_DIR"
CHILD_STATE_FILE="$CHILD_STATE_DIR/${CHILD_NAME}.json"
cat > "$CHILD_STATE_FILE" <<JSON
{
  "name": "${CHILD_NAME}",
  "type": "engineer",
  "family": "engineering",
  "parent": "weave",
  "prompt": "e2e notify test",
  "branch": "notify-e2e",
  "worktree": "${SPRAWL_ROOT}",
  "tmux_session": "${SESSION}",
  "tmux_window": "${CHILD_NAME}",
  "status": "active",
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "tree_path": "weave├${CHILD_NAME}"
}
JSON

# --- Run `sprawl report done` as the child. This exercises the regression
#     target: internal/agentops/report.go → messages.Send. On current main
#     the CLI path does not build a WithNotify callback here, so weave's
#     pane should NOT receive an [inbox] line. After the QUM-310 fix, it
#     should. ---

echo ""
echo "=== Trigger: child runs 'sprawl report done' ==="
# QUM-323: include a distinctive body token so we can assert weave's next
# prompt contains the drained body (not just the [inbox] notifier hint).
DRAIN_TOKEN="DRAIN-BODY-TOKEN-$$-$(date +%s)"
REPORT_LOG="$(mktemp /tmp/notify-e2e-report.XXXXXX)"
set +e
env \
    SPRAWL_AGENT_IDENTITY="$CHILD_NAME" \
    SPRAWL_ROOT="$SPRAWL_ROOT" \
    SPRAWL_NAMESPACE="$SPRAWL_NAMESPACE" \
    SPRAWL_MESSAGING=legacy \
    SPRAWL_TEST_MODE=1 \
    "$SPRAWL_BIN" report done "e2e notification test $DRAIN_TOKEN" \
    > "$REPORT_LOG" 2>&1
REPORT_RC=$?
set -e
if [ "$REPORT_RC" -eq 0 ]; then
    pass "sprawl report done exited 0"
else
    fail "sprawl report done exited non-zero ($REPORT_RC)"
    echo "  report stdout/stderr:" >&2
    sed 's/^/    /' "$REPORT_LOG" >&2
fi

# --- Give the tmux send-keys call time to render on the pane ---

sleep 2

PANE_AFTER="$(capture_pane "$SESSION")"
PANE_DELTA="$(diff "$PANE_BEFORE_FILE" <(echo "$PANE_AFTER") | grep '^>' || true)"

# --- Core assertion: [inbox] line appears in the weave pane ---

echo ""
echo "=== Test: weave pane received [inbox] line from child ==="
INBOX_PATTERN="\[inbox\] New message from ${CHILD_NAME}"
if echo "$PANE_AFTER" | grep -qE "$INBOX_PATTERN"; then
    pass "pane contains '[inbox] New message from $CHILD_NAME'"
else
    fail "pane does NOT contain '[inbox] New message from $CHILD_NAME' — QUM-310 regression"
    echo "  --- pane DELTA (lines added since report) ---" >&2
    if [ -n "$PANE_DELTA" ]; then
        echo "$PANE_DELTA" | sed 's/^/    /' >&2
    else
        echo "    (no new lines in pane)" >&2
    fi
    echo "  --- end delta ---" >&2
    echo "  report output:" >&2
    sed 's/^/    /' "$REPORT_LOG" >&2
fi

# --- QUM-323: assert the drained body token reaches weave's pane ---
#     The [inbox] line above is the QUM-310 legacy notifier (synchronous
#     send-keys hint). QUM-323 adds a 2s poller that injects a fully-
#     rendered `[inbox] You received N message(s)...` flush prompt into
#     the pane via `tmux send-keys -l`. The body of that prompt contains
#     the `sprawl report done` summary — and therefore DRAIN_TOKEN.
#     Wait up to 15s for the drain tick to fire + the send-keys to land.

echo ""
echo "=== Test: QUM-323 drained body token reaches weave's pane ==="
DRAIN_TIMEOUT=15
elapsed=0
while [ "$elapsed" -lt "$DRAIN_TIMEOUT" ]; do
    if capture_pane "$SESSION" | grep -q "$DRAIN_TOKEN"; then
        pass "pane contains DRAIN_TOKEN '$DRAIN_TOKEN' (QUM-323 drain fired)"
        break
    fi
    sleep 1
    elapsed=$((elapsed + 1))
done
if [ "$elapsed" -ge "$DRAIN_TIMEOUT" ]; then
    fail "DRAIN_TOKEN '$DRAIN_TOKEN' did NOT reach weave's pane within ${DRAIN_TIMEOUT}s — QUM-323 regression"
    echo "  --- pane DELTA ---" >&2
    diff "$PANE_BEFORE_FILE" <(capture_pane "$SESSION") | grep '^>' | sed 's/^/    /' >&2 || true
    echo "  --- end delta ---" >&2
fi

# --- Cleanup scratch files ---
rm -f "$PANE_BEFORE_FILE" "$REPORT_LOG"

# --- Summary ---

echo ""
echo "==============================="
echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "==============================="

if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
fi
exit 0
