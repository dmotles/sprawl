#!/usr/bin/env bash
# test-subagent-model-e2e.sh — End-to-end gate for the QUM-756 sub-agent
# model: a same-worktree, same-branch helper that shares its parent's
# filesystem state and is reaped on parent retire (cascade=true).
#
# Phases (driven by scripts/e2e-tests/subagent-model.sh):
#   1. Spawn a tower manager + sub-engineer; assert no new worktree
#      created, sub.worktree == tower.worktree, sub.branch == tower.branch,
#      and peek surfaces a "subagent" badge.
#   2. Sub commits to the shared worktree; commit appears in tower's
#      branch from tower's worktree.
#   3. Retire sub with cascade=false; sub goes retired but the parent
#      worktree and branch are preserved, the sub's commit included.
#   4. Live MCP error paths: weave (root) cannot host sub-agents;
#      spawn(subagent=true, branch=...) is rejected.
#   5. Spawn a fresh sub then cascade-retire the tower; both go retired
#      and the worktree count drops by one.
#   6. Code-reviewer dogfood: engineer manager stages a buggy diff,
#      spawns a reviewer sub, reviewer reads `git diff --cached` and
#      sends findings back via mcp__sprawl__send_message.
#   7. Observational depth-2 canary (WARN-only): the reviewer must not
#      auto-spawn a sub-agent. If it does, we emit a WARN line — never
#      fail — so the soak telemetry surfaces drift in the prompt.
#
# Gate: if `claude` is missing and SPRAWL_E2E_SKIP_NO_CLAUDE=1, skip.
#
# Usage: bash scripts/test-subagent-model-e2e.sh
#
# NOTE: creates a real tmux session and multiple real claude
# subprocesses (weave + tower + sub + reviewer). Do not run in parallel
# with other TUI-mode e2e scripts.

set -euo pipefail

_self="${BASH_SOURCE[0]}"
SCRIPT_DIR="$(cd "$(dirname "$_self")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
LIB="$SCRIPT_DIR/lib/e2e-common.sh"
ROW="$SCRIPT_DIR/e2e-tests/subagent-model.sh"

# shellcheck source=lib/e2e-common.sh
. "$LIB"
# shellcheck source=e2e-tests/subagent-model.sh
. "$ROW"

e2e_require_claude_or_skip "subagent-model-e2e"
e2e_require_tmux
e2e_require_jq

test_run
