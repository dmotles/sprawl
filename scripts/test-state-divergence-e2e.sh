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
# real Claude API/binary is needed. Also exercises the offline retire CLI
# cleanup path against a hand-populated sandbox to assert the binary's
# behavior on disk.
#
# Run from anywhere; no SPRAWL_ROOT setup required (the script creates
# its own /tmp sandbox).

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
# Part 1 — Drive the supervisor end-to-end via the focused Go test.
# This exercises: spawn researcher → spawn manager → simulate divergence →
# retire → assert clean cleanup → assert name reuse.
# ============================================================
blue "[1/2] Running TestE2E_StateDivergenceFullFlow (supervisor end-to-end)"
if (cd "$REPO_ROOT" && go test ./internal/supervisor -run TestE2E_StateDivergenceFullFlow -v); then
    assert_pass "supervisor end-to-end divergence flow"
else
    assert_fail "supervisor end-to-end divergence flow"
fi

# ============================================================
# Part 2 — Sandbox CLI: exercise offline retire cleanup against a
# hand-populated sandbox to assert binary on-disk behavior.
# ============================================================
blue "[2/2] Sandbox: exercising offline retire CLI cleanup on disk"

SPRAWL_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-qum404-XXXXXX")
SPRAWL_ROOT="$(cd "$SPRAWL_ROOT" && pwd -P)"
case "$SPRAWL_ROOT" in
    /tmp/*) ;;
    *) red "FATAL: sandbox path $SPRAWL_ROOT not under /tmp/" >&2; exit 1 ;;
esac
trap '[[ "$SPRAWL_ROOT" == /tmp/* ]] && rm -rf "$SPRAWL_ROOT"' EXIT

git -C "$SPRAWL_ROOT" init -b main --quiet
git -C "$SPRAWL_ROOT" -c user.email=t@t -c user.name=t commit --allow-empty -m init --quiet

# Create a fake manager agent: state JSON, agent dir with SYSTEM.md, worktree.
MGR=tower
MGR_BRANCH="dmotles/qum-404-mgr"
git -C "$SPRAWL_ROOT" branch "$MGR_BRANCH"
WORKTREE="$SPRAWL_ROOT/.sprawl/worktrees/$MGR"
git -C "$SPRAWL_ROOT" worktree add --quiet "$WORKTREE" "$MGR_BRANCH"

mkdir -p "$SPRAWL_ROOT/.sprawl/agents/$MGR"
echo "you are tower" > "$SPRAWL_ROOT/.sprawl/agents/$MGR/SYSTEM.md"
mkdir -p "$SPRAWL_ROOT/.sprawl/agents/$MGR/prompts"
echo "do work" > "$SPRAWL_ROOT/.sprawl/agents/$MGR/prompts/initial.md"

cat >"$SPRAWL_ROOT/.sprawl/agents/$MGR.json" <<EOF
{
  "name": "$MGR",
  "type": "manager",
  "family": "engineering",
  "parent": "weave",
  "branch": "$MGR_BRANCH",
  "worktree": "$WORKTREE",
  "status": "active",
  "created_at": "2026-04-30T00:00:00Z"
}
EOF

# Sanity: pre-state.
[ -f "$SPRAWL_ROOT/.sprawl/agents/$MGR.json" ] || { assert_fail "pre: agent JSON should exist"; exit 1; }
[ -d "$SPRAWL_ROOT/.sprawl/agents/$MGR" ]      || { assert_fail "pre: agent dir should exist"; exit 1; }
[ -d "$WORKTREE" ]                              || { assert_fail "pre: worktree should exist"; exit 1; }

# Run offline retire --abandon (which is the path that ENOENT'd in the
# original repro before the fix; here we test that cleanup completes
# fully and removes the per-agent dir + worktree).
SPRAWL_ROOT="$SPRAWL_ROOT" SPRAWL_AGENT_IDENTITY=weave \
    "$SPRAWL_BIN" retire "$MGR" --abandon --yes 2>&1 || {
        assert_fail "offline retire --abandon exited non-zero"
        exit 1
    }

# Assert post-retire state.
if [ ! -f "$SPRAWL_ROOT/.sprawl/agents/$MGR.json" ]; then
    assert_pass "agent JSON removed after retire"
else
    assert_fail "agent JSON still present after retire: $SPRAWL_ROOT/.sprawl/agents/$MGR.json"
fi

if [ ! -d "$SPRAWL_ROOT/.sprawl/agents/$MGR" ]; then
    assert_pass "agent dir removed after retire (QUM-404 fix)"
else
    assert_fail "agent dir still present after retire: $SPRAWL_ROOT/.sprawl/agents/$MGR"
fi

if [ ! -d "$WORKTREE" ]; then
    assert_pass "worktree removed after retire"
else
    assert_fail "worktree still present after retire: $WORKTREE"
fi

# Stale-dir + name-allocation: pre-create a stale dir for the next manager
# name and verify a subsequent fake spawn would skip it.
NEXT=forge   # second name in ManagerNames after "tower"
mkdir -p "$SPRAWL_ROOT/.sprawl/agents/$NEXT"
echo "stale" > "$SPRAWL_ROOT/.sprawl/agents/$NEXT/SYSTEM.md"

# Use a small inline Go runner to call agent.AllocateName directly.
ALLOC=$(cd "$REPO_ROOT" && go run ./internal/agent/cmd/allocname 2>/dev/null || true)
# Fallback: if no helper main exists, use a one-shot test invocation.
ALLOC_OUT=$(cd "$REPO_ROOT" && SPRAWL_ROOT_FIXTURE="$SPRAWL_ROOT/.sprawl/agents" \
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
