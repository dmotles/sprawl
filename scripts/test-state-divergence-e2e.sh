#!/usr/bin/env bash
# test-state-divergence-e2e.sh — E2E validation for QUM-404
#
# Acceptance scenario from the issue:
#   1. spawn a researcher
#   2. researcher spawns a manager (grandchild scenario)
#   3. assert <manager>.json exists
#   4. simulate state divergence by deleting <manager>.json
#   5. retire the manager via the supervisor
#   6. assert dir + worktree are gone, name is freed
#
# Implementation: drives the supervisor end-to-end via a focused Go test
# (TestE2E_StateDivergenceFullFlow) using a no-op runtime starter, so no
# real Claude API/binary is needed.
#
# QUM-565: a former "Part 2" exercised the offline retire-CLI
# (`--abandon`) against a hand-populated sandbox. That CLI is being
# deleted in Phase 2.3b of M13 and Part 1 already drives the same retire
# code path via the supervisor end-to-end, so Part 2 was redundant.
#
# Run from anywhere; no SPRAWL_ROOT setup required.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SPRAWL_BIN="$REPO_ROOT/sprawl"

if [ ! -x "$SPRAWL_BIN" ]; then
    echo "FATAL: sprawl binary not found at $SPRAWL_BIN — run 'make build' first" >&2
    exit 1
fi

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
blue()  { printf '\033[34m%s\033[0m\n' "$*"; }

PASS=0
FAIL=0

assert_pass() { PASS=$((PASS + 1)); green "  PASS: $1"; }
assert_fail() { FAIL=$((FAIL + 1)); red   "  FAIL: $1"; }

# ============================================================
# Drive the supervisor end-to-end via the focused Go test.
# This exercises: spawn researcher → spawn manager → simulate divergence →
# retire → assert clean cleanup → assert name reuse.
# ============================================================
blue "Running TestE2E_StateDivergenceFullFlow (supervisor end-to-end)"
if (cd "$REPO_ROOT" && go test ./internal/supervisor -run TestE2E_StateDivergenceFullFlow -v); then
    assert_pass "supervisor end-to-end divergence flow"
else
    assert_fail "supervisor end-to-end divergence flow"
fi

# ============================================================
# AllocateName: verify stale agent dirs don't block name reuse.
# Lightweight unit-level invocation; no sandbox needed.
# ============================================================
blue "Running AllocateName unit test (stale-dir name allocation)"
ALLOC_OUT=$(cd "$REPO_ROOT" && \
    go test ./internal/agent -run '^TestAllocateName_SkipsExistingDirectories$' -v 2>&1 | tail -20)

if echo "$ALLOC_OUT" | grep -q -- '--- PASS'; then
    assert_pass "AllocateName skips existing directories (unit-level)"
else
    assert_fail "AllocateName test did not pass: $ALLOC_OUT"
fi

# ============================================================
# Summary
# ============================================================
echo
blue "==== Summary ===="
echo "Passed: $PASS"
echo "Failed: $FAIL"

if [ "$FAIL" -gt 0 ]; then
    red "FAILED"
    exit 1
fi
green "OK — QUM-404 acceptance criteria validated end-to-end"
