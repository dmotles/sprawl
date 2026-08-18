#!/usr/bin/env bash
# scripts/e2e-tests/merge-reuse.sh — QUM-511/QUM-489 regression guard.
# Migrated from scripts/test-merge-reuse-e2e.sh, which was deleted once this
# row proved flake-free (QUM-1183).
# Pure shell — no claude required.

# QUM-1029: the count of assertions a COMPLETE run makes. This row is
# fail-fast — every check that fails calls fail() and returns immediately
# WITHOUT reaching e2e_print_results, because each later step depends on the
# previous one having worked — so the only path that reaches the aggregator is
# the full green path, and the floor can be exact rather than a lower bound.
#
# Until QUM-1029 this row called neither pass/fail nor e2e_print_results: its
# verdict rested entirely on return codes, which made it the one row a per-row
# floor could not reach at all. It was migrated rather than given a floor of 0,
# because a floor of 0 is the defect wearing a declaration.
MIN_ASSERTIONS=9

test_metadata() {
    echo ""
}

test_run() {
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-merge-reuse-e2e"
    e2e_install_cleanup_traps

    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SPRAWL_BIN=$SPRAWL_BIN"

    # --- Bootstrap repo ---
    git -C "$SPRAWL_ROOT" init -b main --quiet
    git -C "$SPRAWL_ROOT" config user.name "Test"
    git -C "$SPRAWL_ROOT" config user.email "test@test"
    echo "base" > "$SPRAWL_ROOT/base.txt"
    echo ".sprawl/" > "$SPRAWL_ROOT/.gitignore"
    git -C "$SPRAWL_ROOT" add base.txt .gitignore
    git -C "$SPRAWL_ROOT" commit -q -m "initial"

    mkdir -p "$SPRAWL_ROOT/.sprawl/agents"
    echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"

    local HEAD_BEFORE
    HEAD_BEFORE=$(git -C "$SPRAWL_ROOT" rev-parse HEAD)
    echo "  HEAD_BEFORE=$HEAD_BEFORE"

    # --- Hand-craft agent engX on branch B1 with one commit ---
    local AGENT_NAME="engX"
    local AGENT_WT="$SPRAWL_ROOT/.sprawl/worktrees/$AGENT_NAME"
    mkdir -p "$(dirname "$AGENT_WT")"

    git -C "$SPRAWL_ROOT" worktree add -b B1 "$AGENT_WT" >/dev/null
    git -C "$AGENT_WT" config user.name "engX"
    git -C "$AGENT_WT" config user.email "engx@test"
    echo "foo content" > "$AGENT_WT/foo.txt"
    git -C "$AGENT_WT" add foo.txt
    git -C "$AGENT_WT" commit -q -m "engX adds foo on B1"

    cat > "$SPRAWL_ROOT/.sprawl/agents/${AGENT_NAME}.json" <<EOF
{
  "name": "$AGENT_NAME",
  "type": "engineer",
  "family": "engineering",
  "parent": "weave",
  "prompt": "irrelevant for this test",
  "branch": "B1",
  "worktree": "$AGENT_WT",
  "status": "done",
  "created_at": "2026-05-07T00:00:00Z"
}
EOF

    cat > "$SPRAWL_ROOT/.sprawl/agents/weave.json" <<EOF
{
  "name": "weave",
  "type": "weave",
  "family": "weave",
  "parent": "root",
  "prompt": "",
  "branch": "main",
  "worktree": "$SPRAWL_ROOT",
  "status": "active",
  "created_at": "2026-05-07T00:00:00Z"
}
EOF

    export SPRAWL_AGENT_IDENTITY="weave"

    echo ""
    echo "=== Step 3: sprawl merge engX (first time, B1 → main) ==="
    cd "$SPRAWL_ROOT"
    # The status checked here is the MERGE's, not sed's. It used to be
    # `merge ... | sed ... || {...}`, which reads the exit status of the last
    # element of the pipeline (`sed`, always 0 here) with no `pipefail` set —
    # so a non-zero merge could never be detected and this check could never
    # fire.
    local MERGE1_OUTPUT MERGE1_RC=0
    MERGE1_OUTPUT=$("$SPRAWL_BIN" merge --no-validate "$AGENT_NAME" 2>&1) || MERGE1_RC=$?
    printf '%s\n' "$MERGE1_OUTPUT" | sed 's/^/    /'
    if [ "$MERGE1_RC" -eq 0 ]; then
        pass "first merge (B1 -> main) returned zero"
    else
        fail "first merge returned non-zero (rc=$MERGE1_RC)"
        return 1
    fi

    local HEAD1
    HEAD1=$(git -C "$SPRAWL_ROOT" rev-parse HEAD)
    echo "  HEAD1=$HEAD1"
    if [ "$HEAD1" != "$HEAD_BEFORE" ]; then
        pass "integration HEAD advanced after the first merge"
    else
        fail "integration HEAD did not advance after the first merge"
        return 1
    fi
    if git -C "$SPRAWL_ROOT" show HEAD --stat | grep -q "foo.txt"; then
        pass "the first merge included foo.txt"
    else
        fail "the first merge did not include foo.txt"
        git -C "$SPRAWL_ROOT" show HEAD --stat >&2
        return 1
    fi

    echo ""
    echo "=== Step 4: simulate branch reuse — engX checks out B2 with new commit ==="
    git -C "$AGENT_WT" checkout -q -b B2
    echo "bar content" > "$AGENT_WT/bar.txt"
    git -C "$AGENT_WT" add bar.txt
    git -C "$AGENT_WT" commit -q -m "engX adds bar on B2 (branch reuse)"

    local STATE_BRANCH
    STATE_BRANCH=$(grep '"branch"' "$SPRAWL_ROOT/.sprawl/agents/${AGENT_NAME}.json" | head -1 | sed 's/.*"branch": *"\([^"]*\)".*/\1/')
    if [ "$STATE_BRANCH" = "B1" ]; then
        pass "state.branch is still the stale B1 after the worktree moved to B2"
    else
        fail "test setup broken — state branch is $STATE_BRANCH, expected B1"
        return 1
    fi
    echo "  state.branch is still '$STATE_BRANCH' (stale — the worktree moved without it)"
    echo "  agent worktree HEAD is now on:"
    git -C "$AGENT_WT" rev-parse --abbrev-ref HEAD | sed 's/^/    /'

    echo ""
    echo "=== Step 5: sprawl merge engX (after the worktree branch swap) ==="
    # The rc is captured OUTSIDE the command substitution. It used to be
    # `MERGE_OUTPUT=$(... || { echo ...; return 1; })`, where the `return 1`
    # executes in the substitution's own subshell and merely ends it — so
    # test_run carried on past a failed second merge.
    local MERGE_OUTPUT MERGE2_RC=0
    MERGE_OUTPUT=$("$SPRAWL_BIN" merge --no-validate "$AGENT_NAME" 2>&1) || MERGE2_RC=$?
    printf '%s\n' "$MERGE_OUTPUT" | sed 's/^/    /'
    if [ "$MERGE2_RC" -eq 0 ]; then
        pass "second merge (after the worktree branch swap) returned zero"
    else
        fail "second merge returned non-zero (rc=$MERGE2_RC)"
        return 1
    fi

    local HEAD2
    HEAD2=$(git -C "$SPRAWL_ROOT" rev-parse HEAD)
    echo "  HEAD2=$HEAD2"

    if [ "$HEAD2" != "$HEAD1" ]; then
        pass "integration HEAD advanced after the worktree branch swap (QUM-511)"
    else
        fail "QUM-511 reproduced: integration HEAD did NOT advance after the worktree branch swap — merge no-op'd on the stale agentState.Branch=B1 instead of resolving the worktree's current branch (B2)"
        return 1
    fi

    if git -C "$SPRAWL_ROOT" show HEAD --stat | grep -q "bar.txt"; then
        pass "the second merge included bar.txt from B2"
    else
        fail "the second merge did not include bar.txt from B2"
        git -C "$SPRAWL_ROOT" show HEAD --stat >&2
        return 1
    fi

    if echo "$MERGE_OUTPUT" | grep -q "Nothing to merge"; then
        fail "merge reported 'Nothing to merge' but B2 has new commits"
        return 1
    else
        pass "merge did not report 'Nothing to merge'"
    fi

    if echo "$MERGE_OUTPUT" | grep -q "B2"; then
        pass "the merge summary names the resolved branch B2"
    else
        fail "the merge summary should mention the resolved branch B2"
        echo "      output was:" >&2
        echo "$MERGE_OUTPUT" >&2
        return 1
    fi

    echo ""
    e2e_print_results
}
