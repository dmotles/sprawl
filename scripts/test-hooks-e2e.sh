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
# Case 5 (QUM-951) additionally exercises `sprawl hooks verify`: whether the
# guard chain is actually ARMED for the current working tree.
#
# Needs only git + the built ./sprawl binary (no claude, no sandbox).
#
# Manual validation of the QUM-951 gate (AC6), run from a scratch clone:
#   git -c core.hooksPath=/nonexistent commit --allow-empty -m probe   # rc=0, no hooks run
#   git config core.hooksPath /nonexistent && make validate            # FAILS at hooks-armed
#   git config --unset core.hooksPath && make validate                 # green again
set -euo pipefail

# Assertion-count floors: a run that asserts nothing must not exit 0. Both are
# HARDCODED, never derived from the run — a derived floor silently follows
# coverage down, which is the failure it exists to prevent. Raise them in the
# same commit as any change to the number of assert calls below.
#
# MIN_CASE5_ASSERTIONS exists because a whole-suite total is a SUM: deleting ten
# Case-1 assertions while adding ten to Case 5 still satisfies it. The guard
# coverage this issue is about is per-case, so the floor is too.
MIN_ASSERTIONS=163
MIN_CASE5_ASSERTIONS=133

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
# assert_line/assert_out/assert_reason — bind an assertion to an EXACT expected
# string in the last verify report. Deliberately not fuzzy greps: a predicate
# like `grep -q 'pre-commit'` is true in every scenario in this suite, armed or
# disarmed, so it has zero discriminating power.
assert_line() { assert "$1" "echo \"\$VOUT\" | grep -qxF '$2'"; }   # whole line
assert_out() { assert "$1" "echo \"\$VOUT\" | grep -qF '$2'"; }     # substring
assert_reason() { assert "$1" "echo \"\$VOUT\" | grep -qxF 'REASON: $2'"; }

# reasons_of <report> — the sorted, comma-joined REASON codes in a report.
# Comparing these (rather than whole reports) is what makes the "distinct
# failure modes" assertion test the reason and not merely the differing paths.
reasons_of() { echo "$1" | sed -n 's/^REASON: //p' | sort | tr '\n' ','; }

# rmtmp <path> — rm, but only ever inside our own /tmp workdir. Destructive-var
# guardrail: assert the path is ours BEFORE deleting, never trust the variable.
rmtmp() {
	[[ "$1" == "$WORKDIR"/* ]] || {
		echo "FATAL: refusing to remove a path outside $WORKDIR: $1" >&2
		exit 1
	}
	rm -rf "$1"
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
		(cd "$repo" && env -u SPRAWL_AGENT_IDENTITY GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null git commit -q --no-verify -m "$msg") >/dev/null 2>&1 || rc=$?
	else
		(cd "$repo" && env GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null SPRAWL_AGENT_IDENTITY="$identity" git commit -q --no-verify -m "$msg") >/dev/null 2>&1 || rc=$?
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
	# Hermetic, for the same reason verify_in is: a host with a stray global
	# core.hooksPath would otherwise let E2b pass for the host's reason rather
	# than for the local setting the case just wrote.
	LAST_STDERR="$(cd "$repo" && env GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
		SPRAWL_AGENT_IDENTITY="$identity" git commit -q -m "$msg" 2>&1)" || LAST_RC=$?
}

# verify_in <dir> — run `sprawl hooks verify` from dir. Sets globals VRC and
# VOUT (merged stdout+stderr). Must be called directly, NOT in a $() subshell,
# or the globals are lost. Asserting on the captured rc (never on `$(cmd)`,
# which passes on empty output) is deliberate.
#
# Hermetic by construction: the host's global/system gitconfig is pinned out, so
# the result cannot depend on whose machine ran the suite (a developer with a
# stray global core.hooksPath would otherwise break the negative controls), and
# the agent identity is pinned so it does not depend on whether an agent or a
# human invoked it. HERMETIC_GLOBAL is redirected only by the case that
# deliberately exercises a global-scope override.
#
# An empty report is treated as a SETUP FAILURE, not as a result: a crash also
# exits nonzero, and without this every positive control could be satisfied by a
# panic instead of by a detection.
HERMETIC_GLOBAL=/dev/null
VRC=0
VOUT=""
verify_in() {
	local dir="$1"
	if [[ ! -d "$dir" ]]; then
		fail "verify_in precondition: $dir is not a directory"
		exit 1
	fi
	VRC=0
	VOUT="$(cd "$dir" && env GIT_CONFIG_GLOBAL="$HERMETIC_GLOBAL" GIT_CONFIG_SYSTEM=/dev/null \
		SPRAWL_AGENT_IDENTITY=e2e "$SPRAWL_BIN" hooks verify 2>&1)" || VRC=$?
	if [[ -z "$VOUT" ]]; then
		fail "verify_in precondition: 'hooks verify' produced NO output (rc=$VRC) — that is a crash, not a verdict"
		exit 1
	fi
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
# Case 5 (QUM-951): `sprawl hooks verify` — is the guard chain armed HERE, now?
#
# git runs NO hooks and exits 0 when core.hooksPath names a path that is not a
# populated directory; likewise for a dangling symlink or a hook that is present
# but not executable. It never warns. Every scenario below is stated with its
# control direction inline: a POSITIVE CONTROL is the defect present and verify
# MUST fire; a NEGATIVE CONTROL is a correct install and verify MUST stay quiet.
# Both arms are required — a check that always fires is as useless as one that
# never does.
#
# `pre-commit` and `reference-transaction` are separate guards with separate
# failure modes, and an implementation that inspects only `pre-commit` and
# hardcodes the other line must not survive this suite. `reference-transaction`
# is the class git does not skip under `--no-verify`, so it is the one that
# matters most. Precisely: MISSING, DANGLING, NOT-EXECUTABLE, NO-GUARD and
# GUARD-UNREACHABLE are each exercised at BOTH hook points; NOT-A-FILE is
# exercised at `pre-commit` only, and the two GUARD-UNREACHABLE <why> values at
# one point each (`missing` at pre-commit, `not executable` at ref-transaction).
#
# --- THE REPORT FORMAT IS LOAD-BEARING. Assertions below grep it exactly. ---
# The whole report goes to STDOUT (it is this command's data product, not
# progress chatter) and is asserted there specifically, so a future refactor
# cannot quietly move it to stderr while `2>&1` hides the change.
#
#   cwd: <abs>
#   git top-level: <abs>
#   git common dir: <abs>
#   linked worktree: yes | no
#   core.hooksPath: <value> | (unset)
#     set by: <scope> <ABSOLUTE origin path>      (one per scope; last wins)
#     winning scope: <scope> <ABSOLUTE origin path>
#   resolved hooks dir: <abs>                     (exact line)
#   hooks dir state: OK | MISSING | NOT-A-DIRECTORY
#     <point>: OK -> <realpath> (mode <perm>, guard <helper> reachable)
#     <point>: MISSING <path>
#     <point>: DANGLING <path> -> <target>
#     <point>: NOT-EXECUTABLE <path> (mode <perm>)
#     <point>: NOT-A-FILE <path>
#     <point>: NO-GUARD <path>
#     <point>: GUARD-UNREACHABLE <path> (helper <abs> <why>)
#     <point>: HOOKS-DIR-MISSING <path>
#   REASON: <point>:<STATUS> | hooks-dir:<STATUS>  (zero or more, machine-readable)
#   VERDICT: ARMED | DISARMED | UNKNOWN
#   FIX: <command>                                (one per remedy, when not ARMED)
#
# "guard <helper> reachable" has a DEFINITION, and it is not "the hook file
# mentions a guard". Both install arrangements dispatch to a helper script
# sitting beside the hook's real path — `sprawl hooks install` writes
# `sprawl-guard-main-*` into the hooks dir, and `make hooks` symlinks to
# `scripts/pre-commit`, which invokes `$here/guard-main-commit`. So a hook can
# name a guard that has been deleted or chmod-ed non-executable and still look
# healthy. Reachable therefore means: the hook body references the helper AND
# that helper exists beside the hook's REAL path AND is executable. Deleting the
# helper alone is a real disarm vector and has its own positive control below.
#
# The helper name sets are fixed, because the two arrangements use different
# basenames and an implementation cannot guess them:
#   pre-commit            -> sprawl-guard-main-commit | guard-main-commit
#   reference-transaction -> sprawl-guard-main-ref    | guard-main-ref
# A body referencing NONE of its set is NO-GUARD; the <helper> printed is the
# basename that matched. <why> is exactly one of: `missing` | `not executable`.
#
# When `hooks dir state` is MISSING or NOT-A-DIRECTORY, BOTH hook points report
# HOOKS-DIR-MISSING and each emits its own REASON. They are never omitted — an
# absent field reads as fine, which is the whole failure mode here.
#
# Exit codes are asserted EXACTLY, never as `-ne 0`: 0 = ARMED, 1 = DISARMED,
# 2 = UNKNOWN (the check could not run). Conflating "I found a defect" with "I
# crashed" is the same false-green this issue is about, so a Go panic (exit 2 by
# accident) must not be able to satisfy a DISARMED assertion.
#
# NOTE: `git config --show-origin` prints a RELATIVE origin (`file:.git/config`)
# in the MAIN checkout — relative to the repo TOP-LEVEL, not to cwd, and it stays
# `.git/config` even when git is run from a nested subdirectory. (From a linked
# worktree git prints an absolute origin already. Both verified against git
# 2.34.1.) The assertions below require the ABSOLUTE path, deliberately: a
# fleet-wide guard failure that does not say which file on disk set it costs a
# long hunt. Do not weaken these to match git's raw output, and do NOT absolutize
# against cwd — E9b runs from a subdirectory precisely to catch that.
#
# NOTE: the one-shot `git -c core.hooksPath=... commit` form from the defect
# statement is structurally INVISIBLE to `hooks verify` — it exists only for the
# duration of that one git process. This suite does not and cannot cover it;
# that form is addressed by the CLAUDE.md prohibition, not by this check.
echo "== Case 5: hooks verify — is the guard armed? (QUM-951) =="
CASE5_START=$((PASS + FAIL))
R5="$WORKDIR/verify"
new_repo "$R5"
HD5="$(hooks_dir "$R5")"
(cd "$R5" && "$SPRAWL_BIN" hooks install >/dev/null 2>&1)
# A root-identity commit, so `git worktree add` in E10 has a valid HEAD. Without
# it E10 depends on E2b's commit having succeeded, and a failure there would
# cascade into E10/E11 as confusing, misattributed reds.
commit_verify_as weave "$R5" case5-base
assert "E5 setup: base commit exists so the later worktree add can succeed" "[[ \$LAST_RC -eq 0 ]]"

# E1 NEGATIVE CONTROL: correctly-installed hooks, core.hooksPath unset.
verify_in "$R5"
assert "E1 negative control (correct install): verify exits 0" "[[ \$VRC -eq 0 ]]"
assert "E1 verdict is ARMED" "echo \"\$VOUT\" | grep -qxF 'VERDICT: ARMED'"
assert "E1 report emits no REASON lines when armed" "[[ -z \"\$(reasons_of \"\$VOUT\")\" ]]"
assert "E1 report emits no FIX lines when armed" "! echo \"\$VOUT\" | grep -q '^FIX: '"
# The report is this command's data product, so it belongs on stdout. Asserted
# on stdout ALONE — `verify_in` merges the streams, which would hide a move.
assert "E1 report is written to STDOUT, not stderr" \
	"(cd '$R5' && env GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null SPRAWL_AGENT_IDENTITY=e2e '$SPRAWL_BIN' hooks verify 2>/dev/null) | grep -qxF 'VERDICT: ARMED'"
# The probe must print its SUBJECT, not just its verdict — and bind every
# statement to the exact path it is talking about.
assert_line "E1 report prints the exact hooks dir it resolved" "resolved hooks dir: $HD5"
assert_line "E1 report states the hooks dir is OK" "hooks dir state: OK"
assert_out "E1 report says core.hooksPath is unset" "core.hooksPath: (unset)"
assert_line "E1 report prints the top-level it resolved" "git top-level: $R5"
# Negative arm for the worktree flag — E10 asserts `yes`; without this an
# implementation that hardcodes either value survives.
assert_line "E1 main checkout reports linked worktree: no" "linked worktree: no"
# The OK line must carry the mode and the reachable-guard clause, not just the
# path — otherwise an implementation that never checks either passes.
assert_out "E1 pre-commit OK line names its real path, mode and reachable guard" "  pre-commit: OK -> $HD5/pre-commit (mode 0755, guard sprawl-guard-main-commit reachable)"
assert_out "E1 reference-transaction OK line names its real path, mode and reachable guard" "  reference-transaction: OK -> $HD5/reference-transaction (mode 0755, guard sprawl-guard-main-ref reachable)"
# Both captures are guarded with -n first: `[[ 0 -lt N ]]` is TRUE, so without
# the -n guard this would PASS when the chain line is absent but VERDICT is not.
# `|| true` is required under `set -e`: a grep that matches nothing exits 1 and
# would abort the whole suite at this assignment. The -n guards below are what
# turn "no match" into a FAIL rather than a silent pass.
E1_CHAIN_LN="$(echo "$VOUT" | grep -n 'resolved hooks dir' | head -1 | cut -d: -f1 || true)"
E1_VERDICT_LN="$(echo "$VOUT" | grep -n '^VERDICT:' | head -1 | cut -d: -f1 || true)"
assert "E1 report prints the chain BEFORE the verdict" \
	"[[ -n \"\$E1_CHAIN_LN\" && -n \"\$E1_VERDICT_LN\" && \$E1_CHAIN_LN -lt \$E1_VERDICT_LN ]]"

# E2 POSITIVE CONTROL: core.hooksPath → a path that does not exist.
git -C "$R5" config core.hooksPath /nonexistent-hooks-dir-qum951
verify_in "$R5"
E2_OUT="$VOUT"
assert "E2 positive control (hooksPath → nonexistent): verify exits 1 (DISARMED, not a crash)" "[[ \$VRC -eq 1 ]]"
assert "E2 verdict is DISARMED" "echo \"\$VOUT\" | grep -qxF 'VERDICT: DISARMED'"
assert_line "E2 report resolves the offending hooksPath" "resolved hooks dir: /nonexistent-hooks-dir-qum951"
assert_line "E2 report states the hooks dir is MISSING" "hooks dir state: MISSING"
assert_out "E2 names WHICH config set it, by ABSOLUTE path" "  set by: local $R5/.git/config"
assert_reason "E2 reason code names the hooks dir" "hooks-dir:MISSING"
assert_reason "E2 reason code covers pre-commit" "pre-commit:HOOKS-DIR-MISSING"
assert_reason "E2 reason code covers reference-transaction" "reference-transaction:HOOKS-DIR-MISSING"
# The stdout pin must cover the FAILING path too: an implementation that reports
# to stdout when armed and stderr when not would satisfy the E1 pin alone, and
# verify_in's 2>&1 merge would hide it.
assert "E2 DISARMED report is also written to STDOUT, not stderr" \
	"(cd '$R5' && env GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null SPRAWL_AGENT_IDENTITY=e2e '$SPRAWL_BIN' hooks verify 2>/dev/null || true) | grep -qxF 'VERDICT: DISARMED'"

# E2b PAIRING ASSERTION: while disarmed, a non-root commit to the protected
# branch SUCCEEDS. Without this the E2 failure would be indistinguishable from a
# check that fires on anything; and without the E2c positive control below,
# rc=0 here would be indistinguishable from a guard that never fires at all.
commit_verify_as engineer "$R5" bypass-while-disarmed
assert "E2b defect is real: non-root commit to main SUCCEEDS while disarmed" "[[ \$LAST_RC -eq 0 ]]"

# E2c NEGATIVE CONTROL (restore): unset → verify quiet AND the guard fires again.
git -C "$R5" config --unset core.hooksPath
verify_in "$R5"
assert "E2c negative control (hooksPath unset): verify exits 0 again" "[[ \$VRC -eq 0 ]]"
commit_verify_as engineer "$R5" blocked-again
assert "E2c guard CAN fire: non-root commit to main is ABORTED when armed" "[[ \$LAST_RC -ne 0 ]]"
assert "E2c abort came from the QUM-808 guard, not some other failure" "echo \"\$LAST_STDERR\" | grep -q 'QUM-808 guard'"

# E2d POSITIVE CONTROL: core.hooksPath set at GLOBAL scope. This is the
# realistic fleet-wide disarm vector — one stray global setting voids the guard
# in every repo on the box, and the report must name the global file.
git config --file "$WORKDIR/gitconfig-global" core.hooksPath /nonexistent-global-qum951
HERMETIC_GLOBAL="$WORKDIR/gitconfig-global"
verify_in "$R5"
assert "E2d positive control (hooksPath at GLOBAL scope): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_out "E2d report attributes it to the global scope by absolute path" "  set by: global $WORKDIR/gitconfig-global"
assert_line "E2d report resolves the global value" "resolved hooks dir: /nonexistent-global-qum951"
# Without this, an implementation that hardcodes `winning scope: local ...`
# passes every other assertion and blames the wrong file in the one incident
# this case exists for.
assert_line "E2d names GLOBAL as the winning scope" "  winning scope: global $WORKDIR/gitconfig-global"
HERMETIC_GLOBAL=/dev/null
verify_in "$R5"
assert "E2e negative control (global scope cleared): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E2f: PRECEDENCE. Two scopes set core.hooksPath at once — global names a broken
# path, local names the good one. git obeys the LAST (local), so the tree is
# ARMED, and the report must say which scope won. Without a case that sets two
# scopes simultaneously, an implementation that reports the FIRST origin as the
# winner survives, and would blame the wrong file during a real incident.
HERMETIC_GLOBAL="$WORKDIR/gitconfig-global"
git -C "$R5" config core.hooksPath "$HD5"
verify_in "$R5"
assert "E2f negative control (local overrides a broken global): verify exits 0" "[[ \$VRC -eq 0 ]]"
assert_out "E2f report lists the losing global scope too" "  set by: global $WORKDIR/gitconfig-global"
assert_out "E2f report lists the winning local scope" "  set by: local $R5/.git/config"
assert_line "E2f report names LOCAL as the winner, not the first one listed" "  winning scope: local $R5/.git/config"
git -C "$R5" config --unset core.hooksPath
HERMETIC_GLOBAL=/dev/null
verify_in "$R5"
assert "E2g negative control (both scopes cleared): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E3 POSITIVE CONTROL: core.hooksPath → a real but EMPTY directory.
mkdir -p "$WORKDIR/empty-hooks"
git -C "$R5" config core.hooksPath "$WORKDIR/empty-hooks"
verify_in "$R5"
assert "E3 positive control (hooksPath → empty dir): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_line "E3 report states the empty dir itself is OK" "hooks dir state: OK"
assert_out "E3 report binds pre-commit MISSING to the empty dir" "  pre-commit: MISSING $WORKDIR/empty-hooks/pre-commit"
assert_out "E3 report binds reference-transaction MISSING to the empty dir" "  reference-transaction: MISSING $WORKDIR/empty-hooks/reference-transaction"
assert_reason "E3 reason code is pre-commit MISSING" "pre-commit:MISSING"
git -C "$R5" config --unset core.hooksPath

# E4 POSITIVE CONTROL: core.hooksPath → a FILE. This is the exact worktree
# `.git/hooks` case: in a linked worktree `.git` is a file, so `.git/hooks`
# cannot be a directory, and git accepts the value silently anyway.
: >"$WORKDIR/hooks-is-a-file"
git -C "$R5" config core.hooksPath "$WORKDIR/hooks-is-a-file"
verify_in "$R5"
assert "E4 positive control (hooksPath → a file): verify exits 1" "[[ \$VRC -eq 1 ]]"
# Bound to our own classification token, NOT the bare phrase "not a directory" —
# that is ENOTDIR's strerror, so any leaked syscall error would match it.
assert_line "E4 report classifies the hooks dir NOT-A-DIRECTORY" "hooks dir state: NOT-A-DIRECTORY"
assert_line "E4 report names the file it resolved" "resolved hooks dir: $WORKDIR/hooks-is-a-file"
assert_reason "E4 reason code names the hooks dir" "hooks-dir:NOT-A-DIRECTORY"
assert_reason "E4 pre-commit is reported unreachable, not silently omitted" "pre-commit:HOOKS-DIR-MISSING"
assert_reason "E4 reference-transaction is reported unreachable too" "reference-transaction:HOOKS-DIR-MISSING"
git -C "$R5" config --unset core.hooksPath

# E4b POSITIVE CONTROL: the hooks dir is fine, but the hook NAME is a directory.
# git will not execute it. Without this, NOT-A-FILE is a declared-but-never-
# exercised element of the report format, i.e. an untested branch.
mkdir -p "$WORKDIR/dirhooks/pre-commit"
git -C "$R5" config core.hooksPath "$WORKDIR/dirhooks"
verify_in "$R5"
assert "E4b positive control (hook path is a DIRECTORY): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_out "E4b report binds NOT-A-FILE to the offending path" "  pre-commit: NOT-A-FILE $WORKDIR/dirhooks/pre-commit"
assert_reason "E4b reason code is pre-commit NOT-A-FILE" "pre-commit:NOT-A-FILE"
git -C "$R5" config --unset core.hooksPath
verify_in "$R5"
assert "E4c negative control (hooksPath cleared): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E5 POSITIVE CONTROL: pre-commit simply absent from the common hooks dir.
mv "$HD5/pre-commit" "$WORKDIR/saved-pre-commit"
verify_in "$R5"
E5_OUT="$VOUT"
assert "E5 positive control (pre-commit absent): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_out "E5 report binds MISSING to the pre-commit path" "  pre-commit: MISSING $HD5/pre-commit"
assert_reason "E5 reason code is pre-commit MISSING" "pre-commit:MISSING"
# The OTHER hook must still be reported OK — an implementation that condemns
# every hook whenever any one is broken would pass a weaker assertion here.
assert_out "E5 reference-transaction is still reported OK (per-hook, not blanket)" "  reference-transaction: OK -> $HD5/reference-transaction"
assert "E5 emits no reason for the healthy reference-transaction" "! echo \"\$VOUT\" | grep -q '^REASON: reference-transaction:'"
# Remediation is asserted as an anchored FIX: line, not as a bare substring
# anywhere in the report — "make hooks" appearing in a paragraph of prose is not
# an actionable next step, and a substring match cannot tell the two apart.
assert_line "E5 emits an actionable FIX line naming make hooks" "FIX: make hooks   # run this from the MAIN checkout, not a worktree"
mv "$WORKDIR/saved-pre-commit" "$HD5/pre-commit"
verify_in "$R5"
assert "E5b negative control (pre-commit restored): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E5c POSITIVE CONTROL, MIRRORED: reference-transaction absent. This is the
# --no-verify-proof backstop; it must be checked in its own right.
mv "$HD5/reference-transaction" "$WORKDIR/saved-ref-transaction"
verify_in "$R5"
assert "E5c positive control (reference-transaction absent): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_out "E5c report binds MISSING to the reference-transaction path" "  reference-transaction: MISSING $HD5/reference-transaction"
assert_reason "E5c reason code is reference-transaction MISSING" "reference-transaction:MISSING"
assert_out "E5c pre-commit is still reported OK (per-hook, not blanket)" "  pre-commit: OK -> $HD5/pre-commit"
assert "E5c emits no reason for the healthy pre-commit" "! echo \"\$VOUT\" | grep -q '^REASON: pre-commit:'"
mv "$WORKDIR/saved-ref-transaction" "$HD5/reference-transaction"
verify_in "$R5"
assert "E5d negative control (reference-transaction restored): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E6 POSITIVE CONTROL: a DANGLING symlink. This is the fleet-wide vector — one
# shared symlink into the main checkout's working tree is the only guard for
# every linked worktree, and deleting its target disarms all of them at once.
#
# The negative arm reproduces the REAL `make hooks` arrangement: the hook is
# symlinked into a directory where its guard helper sits beside it. An earlier
# version of this case moved the hook out ALONE, which strands the helper — the
# hook dispatches via `dirname "$(readlink -f "$0")"`, so that state is actually
# disarmed and the "negative control" was misaimed. Keeping the helper adjacent
# is what makes this arm a control rather than a second positive case.
mkdir -p "$WORKDIR/realhooks"
mv "$HD5/pre-commit" "$WORKDIR/realhooks/pre-commit"
cp -a "$HD5/sprawl-guard-main-commit" "$WORKDIR/realhooks/sprawl-guard-main-commit"
ln -s "$WORKDIR/realhooks/pre-commit" "$HD5/pre-commit"
verify_in "$R5"
assert "E6 negative control (live symlink, helper adjacent): verify exits 0 through a symlink" "[[ \$VRC -eq 0 ]]"
assert_out "E6 report follows the symlink and prints its real target" "  pre-commit: OK -> $WORKDIR/realhooks/pre-commit"
# E6a POSITIVE CONTROL: same symlink, but the helper is NOT beside the real
# path. Resolving the link without following through to the helper reports ARMED
# here, and the hook would fail at commit time.
rmtmp "$WORKDIR/realhooks/sprawl-guard-main-commit"
verify_in "$R5"
assert "E6a positive control (symlinked hook, helper stranded): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_reason "E6a reason code is pre-commit GUARD-UNREACHABLE" "pre-commit:GUARD-UNREACHABLE"
rmtmp "$WORKDIR/realhooks/pre-commit"
verify_in "$R5"
assert "E6b positive control (dangling symlink): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_out "E6b report binds DANGLING to both the link and its target" "  pre-commit: DANGLING $HD5/pre-commit -> $WORKDIR/realhooks/pre-commit"
assert_reason "E6b reason code is pre-commit DANGLING" "pre-commit:DANGLING"
rmtmp "$HD5/pre-commit"
(cd "$R5" && "$SPRAWL_BIN" hooks install >/dev/null 2>&1)
# Precondition, asserted rather than assumed: if the reinstall did not recreate
# the hook, the next case's red would misattribute the cause to verify.
assert "E6c precondition: reinstall recreated an executable pre-commit" "[[ -x '$HD5/pre-commit' ]]"
verify_in "$R5"
assert "E6c negative control (reinstalled after dangling): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E6d POSITIVE CONTROL, MIRRORED: dangling reference-transaction.
mv "$HD5/reference-transaction" "$WORKDIR/ref-link-target"
ln -s "$WORKDIR/ref-link-target" "$HD5/reference-transaction"
rmtmp "$WORKDIR/ref-link-target"
verify_in "$R5"
assert "E6d positive control (dangling reference-transaction): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_reason "E6d reason code is reference-transaction DANGLING" "reference-transaction:DANGLING"
rmtmp "$HD5/reference-transaction"
(cd "$R5" && "$SPRAWL_BIN" hooks install >/dev/null 2>&1)
assert "E6e precondition: reinstall recreated an executable reference-transaction" "[[ -x '$HD5/reference-transaction' ]]"
verify_in "$R5"
assert "E6e negative control (reinstalled): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E7 POSITIVE CONTROL: hook present but NOT EXECUTABLE — git treats it exactly
# like absent, and an `ls` of the hooks dir looks perfectly healthy.
chmod -x "$HD5/pre-commit"
verify_in "$R5"
E7_OUT="$VOUT"
assert "E7 positive control (pre-commit not executable): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_out "E7 report binds NOT-EXECUTABLE to the pre-commit path" "  pre-commit: NOT-EXECUTABLE $HD5/pre-commit"
assert_reason "E7 reason code is pre-commit NOT-EXECUTABLE" "pre-commit:NOT-EXECUTABLE"
chmod +x "$HD5/pre-commit"
verify_in "$R5"
assert "E7b negative control (executable bit restored): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E7c POSITIVE CONTROL, MIRRORED: non-executable reference-transaction.
chmod -x "$HD5/reference-transaction"
verify_in "$R5"
assert "E7c positive control (reference-transaction not executable): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_out "E7c report binds NOT-EXECUTABLE to the reference-transaction path" "  reference-transaction: NOT-EXECUTABLE $HD5/reference-transaction"
assert_reason "E7c reason code is reference-transaction NOT-EXECUTABLE" "reference-transaction:NOT-EXECUTABLE"
chmod +x "$HD5/reference-transaction"
verify_in "$R5"
assert "E7d negative control (executable bit restored): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E8: distinct failure modes must be distinguishable BY THE REASON, not merely
# by the blob differing (the blobs differ anyway, because the paths differ). A
# single generic "hooks are broken" reason would pass a whole-output comparison
# and still cost an operator the entire hunt.
E2_REASONS="$(reasons_of "$E2_OUT")"
E5_REASONS="$(reasons_of "$E5_OUT")"
E7_REASONS="$(reasons_of "$E7_OUT")"
assert "E8 every failure mode emits at least one reason code" "[[ -n \"\$E2_REASONS\" && -n \"\$E5_REASONS\" && -n \"\$E7_REASONS\" ]]"
assert "E8 hooksPath-nonexistent and hook-absent have DIFFERENT reasons" "[[ \"\$E2_REASONS\" != \"\$E5_REASONS\" ]]"
assert "E8 hook-absent and not-executable have DIFFERENT reasons" "[[ \"\$E5_REASONS\" != \"\$E7_REASONS\" ]]"
assert "E8 hooksPath-nonexistent and not-executable have DIFFERENT reasons" "[[ \"\$E2_REASONS\" != \"\$E7_REASONS\" ]]"

# E9: a RELATIVE core.hooksPath resolves against the repo top-level, not cwd.
# Run from a nested subdirectory so the two resolutions actually differ — from
# the top-level they are the same directory and the test would prove nothing.
cp -a "$HD5" "$R5/relhooks"
mkdir -p "$R5/sub/deep"
git -C "$R5" config core.hooksPath relhooks
verify_in "$R5/sub/deep"
assert "E9 negative control (relative hooksPath, run from a SUBDIR): verify exits 0" "[[ \$VRC -eq 0 ]]"
assert_line "E9 relative value resolves against the TOP-LEVEL, not cwd" "resolved hooks dir: $R5/relhooks"
# E9b POSITIVE CONTROL: a relative value naming a directory that does not exist.
git -C "$R5" config core.hooksPath nosuchrelhooks
verify_in "$R5/sub/deep"
assert "E9b positive control (relative hooksPath → nonexistent): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_line "E9b report resolves the bad relative value absolutely" "resolved hooks dir: $R5/nosuchrelhooks"
# Run from a SUBDIR: git prints `file:.git/config`, relative to the TOP-LEVEL.
# Absolutizing that against cwd yields $R5/sub/deep/.git/config — a path that
# does not exist. This assertion is the only thing that catches that.
assert_out "E9b absolutizes the config origin against the TOP-LEVEL, not cwd" "  set by: local $R5/.git/config"
git -C "$R5" config --unset core.hooksPath
rmtmp "$R5/relhooks"
verify_in "$R5"
assert "E9c negative control (relative hooksPath cleared): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E10 (AC3): the check must behave identically from a LINKED WORKTREE, where
# `.git` is a file and the hooks live in the shared common dir.
# Assert the setup's own exit code. Discarding it would let a failed worktree
# add (e.g. a repo with no valid HEAD) cascade into E10/E11 as reds that look
# like verify defects.
WT_RC=0
git -C "$R5" worktree add -q -b wt-verify "$WORKDIR/wt5" >/dev/null 2>&1 || WT_RC=$?
assert "E10 precondition: git worktree add succeeded" "[[ \$WT_RC -eq 0 ]]"
assert "E10 precondition: .git in a linked worktree is a FILE, not a directory" "[[ -f '$WORKDIR/wt5/.git' && ! -d '$WORKDIR/wt5/.git' ]]"
verify_in "$WORKDIR/wt5"
assert "E10 negative control (armed, from a worktree): verify exits 0" "[[ \$VRC -eq 0 ]]"
assert_line "E10 worktree resolves the shared COMMON hooks dir" "resolved hooks dir: $HD5"
assert_out "E10 worktree report says it is a linked worktree" "linked worktree: yes"

# E11 POSITIVE CONTROL: break the ONE shared hook and the worktree must see it.
mv "$HD5/pre-commit" "$WORKDIR/saved-shared-pre-commit"
verify_in "$WORKDIR/wt5"
assert "E11 positive control (shared hook deleted): worktree verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_out "E11 worktree report binds MISSING to the SHARED path" "  pre-commit: MISSING $HD5/pre-commit"
assert_reason "E11 worktree reason code is pre-commit MISSING" "pre-commit:MISSING"
mv "$WORKDIR/saved-shared-pre-commit" "$HD5/pre-commit"
verify_in "$WORKDIR/wt5"
assert "E11b negative control (shared hook restored): worktree verify exits 0" "[[ \$VRC -eq 0 ]]"

# E12 POSITIVE CONTROL: a hook that exists, is executable, and runs no guard at
# all. Existence is not armament.
mv "$HD5/pre-commit" "$WORKDIR/saved-noguard-pre-commit"
printf '#!/bin/sh\nexit 0\n' >"$HD5/pre-commit"
chmod +x "$HD5/pre-commit"
verify_in "$R5"
assert "E12 positive control (hook present but runs no guard): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_out "E12 report binds NO-GUARD to the pre-commit path" "  pre-commit: NO-GUARD $HD5/pre-commit"
assert_reason "E12 reason code is pre-commit NO-GUARD" "pre-commit:NO-GUARD"
rmtmp "$HD5/pre-commit"
mv "$WORKDIR/saved-noguard-pre-commit" "$HD5/pre-commit"
verify_in "$R5"
assert "E12b negative control (real guard restored): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E12c POSITIVE CONTROL, MIRRORED: reference-transaction present, executable,
# and running no guard. This is the --no-verify-proof backstop, so the content
# check matters more here than anywhere. Without this case, an implementation
# that content-checks pre-commit and accepts reference-transaction on existence
# alone passes the entire suite.
mv "$HD5/reference-transaction" "$WORKDIR/saved-noguard-ref"
printf '#!/bin/sh\nexit 0\n' >"$HD5/reference-transaction"
chmod +x "$HD5/reference-transaction"
verify_in "$R5"
assert "E12c positive control (reference-transaction runs no guard): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_out "E12c report binds NO-GUARD to the reference-transaction path" "  reference-transaction: NO-GUARD $HD5/reference-transaction"
assert_reason "E12c reason code is reference-transaction NO-GUARD" "reference-transaction:NO-GUARD"
rmtmp "$HD5/reference-transaction"
mv "$WORKDIR/saved-noguard-ref" "$HD5/reference-transaction"
verify_in "$R5"
assert "E12d negative control (real ref guard restored): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E14 POSITIVE CONTROL: the hook is present, executable, and NAMES its guard —
# but the helper it dispatches to has been deleted. Every file `ls` shows looks
# right; the guard is gone. A substring match on the hook body reports ARMED
# here, which is why "reachable" is defined as "the helper exists beside the
# hook's real path and is executable".
mv "$HD5/sprawl-guard-main-commit" "$WORKDIR/saved-helper-commit"
verify_in "$R5"
assert "E14 positive control (guard helper deleted, hook intact): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_out "E14 report binds GUARD-UNREACHABLE to the hook and names the missing helper" "  pre-commit: GUARD-UNREACHABLE $HD5/pre-commit (helper $HD5/sprawl-guard-main-commit missing)"
assert_reason "E14 reason code is pre-commit GUARD-UNREACHABLE" "pre-commit:GUARD-UNREACHABLE"
mv "$WORKDIR/saved-helper-commit" "$HD5/sprawl-guard-main-commit"
verify_in "$R5"
assert "E14b negative control (helper restored): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E14c POSITIVE CONTROL, MIRRORED: the ref-guard helper loses its executable bit.
chmod -x "$HD5/sprawl-guard-main-ref"
verify_in "$R5"
assert "E14c positive control (ref guard helper not executable): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_out "E14c report names the non-executable helper" "  reference-transaction: GUARD-UNREACHABLE $HD5/reference-transaction (helper $HD5/sprawl-guard-main-ref not executable)"
assert_reason "E14c reason code is reference-transaction GUARD-UNREACHABLE" "reference-transaction:GUARD-UNREACHABLE"
chmod +x "$HD5/sprawl-guard-main-ref"
verify_in "$R5"
assert "E14d negative control (helper bit restored): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E15 POSITIVE CONTROL: the hook exists, is executable, and MENTIONS its guard —
# in a comment. A mention is not a dispatch. Matching the bare name reported a
# hook gutted to `# disabled: <guard>` + `exit 0` as ARMED: a hook that runs
# nothing while validate went green.
mv "$HD5/pre-commit" "$WORKDIR/saved-mention-pre-commit"
printf '#!/bin/sh\n# disabled: sprawl-guard-main-commit\nexit 0\n' >"$HD5/pre-commit"
chmod +x "$HD5/pre-commit"
verify_in "$R5"
assert "E15 positive control (guard named only in a comment): verify exits 1" "[[ \$VRC -eq 1 ]]"
assert_reason "E15 reason code is pre-commit NO-GUARD" "pre-commit:NO-GUARD"
rmtmp "$HD5/pre-commit"
mv "$WORKDIR/saved-mention-pre-commit" "$HD5/pre-commit"
verify_in "$R5"
assert "E15b negative control (real dispatch restored): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E16: the `make hooks` arrangement, where the hook IS the guard — the hooks
# entry symlinks straight to the guard script, so there is no dispatch to find
# and the guard's only self-mention is a header comment. Armament must be
# structural: rewording that comment is a zero-behaviour change and must not
# flip the verdict. Keying on the comment hard-failed validate for every
# worktree at once, with a FIX that could not fix it.
mkdir -p "$WORKDIR/mh/scripts"
printf '#!/bin/sh\n# guard-main-ref (QUM-837)\nexit 0\n' >"$WORKDIR/mh/scripts/guard-main-ref"
chmod +x "$WORKDIR/mh/scripts/guard-main-ref"
mv "$HD5/reference-transaction" "$WORKDIR/saved-selfhook-ref"
ln -s "$WORKDIR/mh/scripts/guard-main-ref" "$HD5/reference-transaction"
verify_in "$R5"
assert "E16 negative control (hook IS the guard, make hooks style): verify exits 0" "[[ \$VRC -eq 0 ]]"
assert_out "E16 report resolves the hook to the guard script itself" "  reference-transaction: OK -> $WORKDIR/mh/scripts/guard-main-ref"
# Reword the ONLY self-mention in the file.
printf '#!/bin/sh\n# main-ref guard (QUM-837)\nexit 0\n' >"$WORKDIR/mh/scripts/guard-main-ref"
chmod +x "$WORKDIR/mh/scripts/guard-main-ref"
verify_in "$R5"
assert "E16b negative control (comment reworded): verdict is UNCHANGED, still 0" "[[ \$VRC -eq 0 ]]"
assert "E16b prose edit did not manufacture a reason code" "[[ -z \"\$(reasons_of \"\$VOUT\")\" ]]"
rmtmp "$HD5/reference-transaction"
mv "$WORKDIR/saved-selfhook-ref" "$HD5/reference-transaction"
verify_in "$R5"
assert "E16c negative control (original ref hook restored): verify exits 0 again" "[[ \$VRC -eq 0 ]]"

# E13 POSITIVE CONTROL: outside a git repo the check CANNOT run. It must say so
# and exit 2 (UNKNOWN) — never 0, and never 1, because "I could not determine"
# is not "I found a defect" and is certainly not "everything is fine".
mkdir -p "$WORKDIR/notarepo"
# Precondition, asserted rather than assumed: the scenario is only meaningful if
# this directory really is outside any repo.
assert "E13 precondition: the scratch dir is genuinely not inside a git repo" \
	"! git -C '$WORKDIR/notarepo' rev-parse --git-dir >/dev/null 2>&1"
verify_in "$WORKDIR/notarepo"
assert "E13 positive control (not a git repo): verify exits 2 (UNKNOWN, not 0 or 1)" "[[ \$VRC -eq 2 ]]"
assert "E13 verdict is UNKNOWN" "echo \"\$VOUT\" | grep -qxF 'VERDICT: UNKNOWN'"
assert "E13 degrades LOUDLY: never claims ARMED" "! echo \"\$VOUT\" | grep -q 'VERDICT: ARMED'"

CASE5_RAN=$((PASS + FAIL - CASE5_START))
# ---------------------------------------------------------------------------
echo
echo "RESULTS: $PASS passed, $FAIL failed ($CASE5_RAN of them in Case 5)"
if [[ $((PASS + FAIL)) -lt $MIN_ASSERTIONS ]]; then
	echo "FATAL: only $((PASS + FAIL)) assertions ran, expected at least $MIN_ASSERTIONS." >&2
	echo "       A suite that stops asserting must not report success." >&2
	exit 1
fi
if [[ $CASE5_RAN -lt $MIN_CASE5_ASSERTIONS ]]; then
	echo "FATAL: Case 5 ran only $CASE5_RAN assertions, expected at least $MIN_CASE5_ASSERTIONS." >&2
	echo "       The guard-armament coverage must not be traded away against other cases." >&2
	exit 1
fi
[[ "$FAIL" -eq 0 ]]
