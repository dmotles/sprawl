#!/usr/bin/env bash
# smoke-test-merge.sh — Validates the squash+rebase+fast-forward merge strategy
# creates proper git ancestry so that `git branch --merged` works.
#
# This test uses pure git commands in a temp repo to verify the core hypothesis:
# after squash+rebase+ff-merge, the agent branch appears in `git branch --merged`.

set -euo pipefail

PASS=0
FAIL=0

pass() {
    echo "  PASS: $1"
    PASS=$((PASS + 1))
}

fail() {
    echo "  FAIL: $1"
    FAIL=$((FAIL + 1))
}

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

echo "=== Smoke Test: Merge Strategy (squash+rebase+ff-merge) ==="
echo "Working in $TMPDIR"
echo

# -------------------------------------------------------------------
# Setup: create a repo with main branch and an agent branch
# -------------------------------------------------------------------
echo "--- Setup ---"
git init "$TMPDIR/repo" --initial-branch=main >/dev/null 2>&1
cd "$TMPDIR/repo"
git config user.email "test@test.com"
git config user.name "Test"

echo "base content" > base.txt
git add base.txt
git commit -m "initial commit" >/dev/null 2>&1

OLD_MAIN=$(git rev-parse HEAD)

# Create agent branch with multiple commits
git checkout -b dendra/test-agent >/dev/null 2>&1
echo "feature 1" > feature1.txt
git add feature1.txt
git commit -m "agent commit 1" >/dev/null 2>&1

echo "feature 2" > feature2.txt
git add feature2.txt
git commit -m "agent commit 2" >/dev/null 2>&1

echo "feature 3" > feature3.txt
git add feature3.txt
git commit -m "agent commit 3" >/dev/null 2>&1

AGENT_HEAD_BEFORE=$(git rev-parse HEAD)
AGENT_COMMIT_COUNT=$(git rev-list --count main..HEAD)

echo "  Main at: $OLD_MAIN"
echo "  Agent branch: dendra/test-agent ($AGENT_COMMIT_COUNT commits ahead)"
echo

# -------------------------------------------------------------------
# Test 1: Squash + Rebase + FF-Merge
# -------------------------------------------------------------------
echo "--- Test 1: Squash + Rebase + Fast-Forward Merge ---"

# Step 1: Find merge base
MERGE_BASE=$(git merge-base main dendra/test-agent)

# Step 2: Squash (reset --soft to merge base, then commit)
git reset --soft "$MERGE_BASE" >/dev/null 2>&1
git commit -m "squashed: agent work" >/dev/null 2>&1

SQUASHED_SHA=$(git rev-parse HEAD)
SQUASH_COMMIT_COUNT=$(git rev-list --count main..HEAD)

if [ "$SQUASH_COMMIT_COUNT" = "1" ]; then
    pass "Squash produced exactly 1 commit (was $AGENT_COMMIT_COUNT)"
else
    fail "Squash should produce 1 commit, got $SQUASH_COMMIT_COUNT"
fi

# Step 3: Rebase onto main
git rebase main >/dev/null 2>&1

REBASED_SHA=$(git rev-parse HEAD)

# Step 4: Fast-forward merge on main
git checkout main >/dev/null 2>&1
git merge --ff-only dendra/test-agent >/dev/null 2>&1

NEW_MAIN=$(git rev-parse HEAD)

# -------------------------------------------------------------------
# Verify: ancestry and branch --merged
# -------------------------------------------------------------------
echo
echo "--- Verification ---"

# 1. Main advanced
if [ "$NEW_MAIN" != "$OLD_MAIN" ]; then
    pass "Main branch advanced from $OLD_MAIN to $NEW_MAIN"
else
    fail "Main did not advance"
fi

# 2. Fast-forward: old main is ancestor of new main
if git merge-base --is-ancestor "$OLD_MAIN" "$NEW_MAIN"; then
    pass "Fast-forward: old main is ancestor of new main"
else
    fail "Not a fast-forward: old main is not ancestor of new main"
fi

# 3. Main and agent branch point to same commit
AGENT_NOW=$(git rev-parse dendra/test-agent)
if [ "$NEW_MAIN" = "$AGENT_NOW" ]; then
    pass "Main and agent branch point to same commit"
else
    fail "Main ($NEW_MAIN) != agent branch ($AGENT_NOW)"
fi

# 4. Linear history (no merge commits)
MERGE_COMMITS=$(git log --merges --oneline "$OLD_MAIN".."$NEW_MAIN" | wc -l)
if [ "$MERGE_COMMITS" = "0" ]; then
    pass "Linear history: no merge commits"
else
    fail "Found $MERGE_COMMITS merge commits (expected 0)"
fi

# 5. THE KEY TEST: git branch --merged includes the agent branch
MERGED_BRANCHES=$(git branch --merged main)
if echo "$MERGED_BRANCHES" | grep -q "dendra/test-agent"; then
    pass "git branch --merged includes dendra/test-agent"
else
    fail "git branch --merged does NOT include dendra/test-agent"
    echo "    Merged branches: $MERGED_BRANCHES"
fi

# 6. Files from agent are present on main
if [ -f feature1.txt ] && [ -f feature2.txt ] && [ -f feature3.txt ]; then
    pass "All agent files present on main"
else
    fail "Missing agent files on main"
fi

# -------------------------------------------------------------------
# Test 2: Zero-commit case (no-op)
# -------------------------------------------------------------------
echo
echo "--- Test 2: Zero-Commit Case (No-Op) ---"

# Agent branch and main are at the same commit now
MERGE_BASE2=$(git merge-base main dendra/test-agent)
AGENT_HEAD2=$(git rev-parse dendra/test-agent)

if [ "$MERGE_BASE2" = "$AGENT_HEAD2" ]; then
    pass "Zero-commit detected: merge-base == agent HEAD"
else
    fail "Expected zero-commit case, but merge-base ($MERGE_BASE2) != agent HEAD ($AGENT_HEAD2)"
fi

# -------------------------------------------------------------------
# Test 3: Multiple merges (agent continues working after merge)
# -------------------------------------------------------------------
echo
echo "--- Test 3: Agent Continues After Merge ---"

git checkout dendra/test-agent >/dev/null 2>&1
echo "more work" > feature4.txt
git add feature4.txt
git commit -m "agent commit after merge" >/dev/null 2>&1

PRE_MERGE_MAIN=$(git -C "$TMPDIR/repo" rev-parse main)
AGENT_HEAD3=$(git rev-parse HEAD)

# Squash new work
MERGE_BASE3=$(git merge-base main dendra/test-agent)
git reset --soft "$MERGE_BASE3" >/dev/null 2>&1
git commit -m "squashed: more agent work" >/dev/null 2>&1

# Rebase and ff-merge
git rebase main >/dev/null 2>&1
git checkout main >/dev/null 2>&1
git merge --ff-only dendra/test-agent >/dev/null 2>&1

FINAL_MAIN=$(git rev-parse HEAD)

if [ "$FINAL_MAIN" != "$PRE_MERGE_MAIN" ]; then
    pass "Second merge advanced main"
else
    fail "Second merge did not advance main"
fi

if git merge-base --is-ancestor "$PRE_MERGE_MAIN" "$FINAL_MAIN"; then
    pass "Second merge is also a fast-forward"
else
    fail "Second merge is not a fast-forward"
fi

if [ -f feature4.txt ]; then
    pass "New agent work present on main after second merge"
else
    fail "New agent work missing from main"
fi

MERGED_BRANCHES2=$(git branch --merged main)
if echo "$MERGED_BRANCHES2" | grep -q "dendra/test-agent"; then
    pass "git branch --merged still includes dendra/test-agent after second merge"
else
    fail "git branch --merged does NOT include dendra/test-agent after second merge"
fi

# -------------------------------------------------------------------
# Summary
# -------------------------------------------------------------------
echo
echo "=== Results: $PASS passed, $FAIL failed ==="

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
