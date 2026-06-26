#!/usr/bin/env bash
#
# test-hooks-e2e.sh (QUM-842)
#
# CLI-level end-to-end round-trip for `sprawl hooks install` / `uninstall`.
# Exercises a throwaway git repo (with and without a pre-existing user
# pre-commit hook):
#   install → non-root --no-verify commit to the protected branch is ABORTED
#   → root (weave) and human (empty identity) commits SUCCEED → uninstall →
#   all Sprawl-owned files/blocks gone, user's original hook intact, non-root
#   commit no longer blocked.
#
# Needs only git + the built ./sprawl binary (no claude, no sandbox).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SPRAWL_BIN="${SPRAWL_BIN:-$REPO_ROOT/sprawl}"

if [[ ! -x "$SPRAWL_BIN" ]]; then
	echo "FATAL: sprawl binary not found at $SPRAWL_BIN (run 'make build' first)" >&2
	exit 1
fi

PASS=0
FAIL=0
pass() {
	PASS=$((PASS + 1))
	echo "  PASS: $1"
}
fail() {
	FAIL=$((FAIL + 1))
	echo "  FAIL: $1" >&2
}
assert() {
	if eval "$2"; then pass "$1"; else fail "$1 (cmd: $2)"; fi
}

WORKDIR="$(mktemp -d "/tmp/sprawl-hooks-e2e.XXXXXX")"
cleanup() {
	# Destructive-var guardrail: only ever rm under /tmp.
	[[ "$WORKDIR" == /tmp/* ]] || exit 1
	rm -rf "$WORKDIR"
}
trap cleanup EXIT

# new_repo <dir> — init a git repo with a deterministic default branch.
new_repo() {
	local dir="$1"
	git init -q -b main "$dir"
	git -C "$dir" config user.email e2e@example.com
	git -C "$dir" config user.name e2e
}

hooks_dir() { echo "$1/.git/hooks"; }

# commit_as <identity> <repo> <msg> — attempt a --no-verify commit; echo exit code.
# identity="" simulates a human (SPRAWL_AGENT_IDENTITY unset).
commit_as() {
	local identity="$1" repo="$2" msg="$3"
	echo "change-$RANDOM" >>"$repo/file.txt"
	git -C "$repo" add -A
	local rc=0
	if [[ -z "$identity" ]]; then
		(cd "$repo" && env -u SPRAWL_AGENT_IDENTITY git commit -q --no-verify -m "$msg") >/dev/null 2>&1 || rc=$?
	else
		(cd "$repo" && SPRAWL_AGENT_IDENTITY="$identity" git commit -q --no-verify -m "$msg") >/dev/null 2>&1 || rc=$?
	fi
	echo "$rc"
}

# commit_verify_as <identity> <repo> <msg> — a NORMAL commit (pre-commit hook
# runs). Sets globals LAST_RC and LAST_STDERR (must be called directly, NOT in a
# $() subshell, or the globals are lost). Exercises the QUM-808 pre-commit guard
# specifically (the --no-verify path skips it).
LAST_RC=0
LAST_STDERR=""
commit_verify_as() {
	local identity="$1" repo="$2" msg="$3"
	echo "change-$RANDOM" >>"$repo/file.txt"
	git -C "$repo" add -A
	LAST_RC=0
	LAST_STDERR="$(cd "$repo" && SPRAWL_AGENT_IDENTITY="$identity" git commit -q -m "$msg" 2>&1)" || LAST_RC=$?
}

# ---------------------------------------------------------------------------
echo "== Case 1: fresh repo, no pre-existing hooks =="
R1="$WORKDIR/fresh"
new_repo "$R1"
HD1="$(hooks_dir "$R1")"
(cd "$R1" && "$SPRAWL_BIN" hooks install >/dev/null 2>&1)

assert "commit-guard helper created executable" "[[ -x '$HD1/sprawl-guard-main-commit' ]]"
assert "ref-guard helper created executable" "[[ -x '$HD1/sprawl-guard-main-ref' ]]"
assert "pre-commit hook created executable" "[[ -x '$HD1/pre-commit' ]]"
assert "reference-transaction hook created executable" "[[ -x '$HD1/reference-transaction' ]]"
assert "manifest written" "[[ -f '$HD1/.sprawl-hooks-manifest.json' ]]"
assert "manifest records protected branch main" "grep -q '\"protectedBranch\": \"main\"' '$HD1/.sprawl-hooks-manifest.json'"

assert "non-root --no-verify commit is ABORTED (ref-guard)" "[[ \$(commit_as engineer '$R1' evil) -ne 0 ]]"
# Normal commit exercises the QUM-808 pre-commit guard specifically.
commit_verify_as engineer "$R1" evil2
assert "non-root normal commit is ABORTED by the pre-commit guard" "[[ \$LAST_RC -ne 0 ]]"
assert "pre-commit guard emits the QUM-808 message" "echo \"\$LAST_STDERR\" | grep -q 'QUM-808 guard'"
assert "root (weave) commit SUCCEEDS" "[[ \$(commit_as weave '$R1' weave-ok) -eq 0 ]]"
assert "human (empty identity) commit SUCCEEDS" "[[ \$(commit_as '' '$R1' human-ok) -eq 0 ]]"

(cd "$R1" && "$SPRAWL_BIN" hooks uninstall >/dev/null 2>&1)
assert "uninstall removed commit-guard helper" "[[ ! -e '$HD1/sprawl-guard-main-commit' ]]"
assert "uninstall removed ref-guard helper" "[[ ! -e '$HD1/sprawl-guard-main-ref' ]]"
assert "uninstall removed created pre-commit" "[[ ! -e '$HD1/pre-commit' ]]"
assert "uninstall removed created reference-transaction" "[[ ! -e '$HD1/reference-transaction' ]]"
assert "uninstall removed manifest" "[[ ! -e '$HD1/.sprawl-hooks-manifest.json' ]]"
assert "non-root commit no longer blocked after uninstall" "[[ \$(commit_as engineer '$R1' now-ok) -eq 0 ]]"

# ---------------------------------------------------------------------------
echo "== Case 2: repo WITH a pre-existing user pre-commit hook =="
R2="$WORKDIR/existing"
new_repo "$R2"
HD2="$(hooks_dir "$R2")"
USER_HOOK=$'#!/bin/sh\necho "USER PRECOMMIT"\nexit 0\n'
printf '%s' "$USER_HOOK" >"$HD2/pre-commit"
chmod +x "$HD2/pre-commit"
USER_HASH="$(sha256sum "$HD2/pre-commit" | cut -d' ' -f1)"

(cd "$R2" && "$SPRAWL_BIN" hooks install >/dev/null 2>&1)
assert "user content preserved (one managed block appended)" "[[ \$(grep -c 'sprawl-managed (do not edit)' '$HD2/pre-commit') -eq 1 ]]"
assert "user's original line still present" "grep -q 'USER PRECOMMIT' '$HD2/pre-commit'"
assert "manifest marks pre-commit as appended" "grep -A1 '\"pre-commit\"' '$HD2/.sprawl-hooks-manifest.json' | grep -q 'appended'"

# Idempotency: re-install does not duplicate the block.
(cd "$R2" && "$SPRAWL_BIN" hooks install >/dev/null 2>&1)
assert "re-install keeps exactly one managed block" "[[ \$(grep -c 'sprawl-managed (do not edit)' '$HD2/pre-commit') -eq 1 ]]"

assert "non-root --no-verify commit is ABORTED (existing-hook repo)" "[[ \$(commit_as engineer '$R2' evil) -ne 0 ]]"
# M1: the chained pre-commit guard must run FIRST — a user hook ending in
# `exit 0` must not render it inert on a normal commit.
commit_verify_as engineer "$R2" evil2
assert "chained pre-commit guard fires before user 'exit 0'" "[[ \$LAST_RC -ne 0 ]]"
assert "chained guard emits the QUM-808 message" "echo \"\$LAST_STDERR\" | grep -q 'QUM-808 guard'"

(cd "$R2" && "$SPRAWL_BIN" hooks uninstall >/dev/null 2>&1)
NEW_HASH="$(sha256sum "$HD2/pre-commit" | cut -d' ' -f1)"
assert "user pre-commit restored byte-for-byte" "[[ '$NEW_HASH' == '$USER_HASH' ]]"
assert "managed block fully stripped" "[[ \$(grep -c 'sprawl-managed' '$HD2/pre-commit') -eq 0 ]]"
assert "user pre-commit still executable" "[[ -x '$HD2/pre-commit' ]]"

# ---------------------------------------------------------------------------
echo "== Case 3: uninstall when nothing is installed (safe, exit 0) =="
R3="$WORKDIR/clean"
new_repo "$R3"
rc=0
(cd "$R3" && "$SPRAWL_BIN" hooks uninstall) >/dev/null 2>&1 || rc=$?
assert "uninstall on clean repo exits 0" "[[ $rc -eq 0 ]]"

# ---------------------------------------------------------------------------
echo "== Case 4: --branch override protects a non-default branch =="
R4="$WORKDIR/branch"
new_repo "$R4"
git -C "$R4" checkout -q -b develop
(cd "$R4" && "$SPRAWL_BIN" hooks install --branch develop >/dev/null 2>&1)
assert "manifest records overridden branch develop" "grep -q '\"protectedBranch\": \"develop\"' '$(hooks_dir "$R4")/.sprawl-hooks-manifest.json'"
assert "non-root commit to develop is ABORTED" "[[ \$(commit_as engineer '$R4' evil) -ne 0 ]]"

# ---------------------------------------------------------------------------
echo
echo "RESULTS: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]]
