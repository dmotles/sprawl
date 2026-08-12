#!/usr/bin/env bash
# Unit tests for the matrix-driven e2e harness foundation (QUM-616 Wave 1).
#
# These tests intentionally fail until the implementation lands:
#   scripts/lib/e2e-common.sh
#   scripts/e2e-matrix.sh
#   scripts/e2e-tests/merge-reuse.sh
#   Makefile targets: test-e2e-matrix, test-e2e-matrix-%
#
# Self-contained. Run as: bash scripts/test-e2e-matrix-unit.sh
# No external deps beyond bash, mktemp, grep, cp.

set +e  # Deliberately tolerate failed assertions so we report ALL failures.

# Resolve repo root from this script's location (scripts/<this>).
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)
cd "$REPO_ROOT" || { echo "cannot cd to repo root: $REPO_ROOT"; exit 2; }

LIB="$REPO_ROOT/scripts/lib/e2e-common.sh"
DRIVER="$REPO_ROOT/scripts/e2e-matrix.sh"
ROW="$REPO_ROOT/scripts/e2e-tests/merge-reuse.sh"
ORIG_MERGE="$REPO_ROOT/scripts/test-merge-reuse-e2e.sh"
MAKEFILE="$REPO_ROOT/Makefile"

PASS=0
FAIL=0

# Assertion-count floor. A hardcoded literal, NOT derived from anything this suite
# measures: a floor computed from the corpus it checks is satisfied by an empty
# corpus, which is the exact false-green it exists to stop.
#
# Why this is here at all: until QUM-997 this suite — ~245 assertions across 16
# sections, inside `make validate`, i.e. the merge/push gate — had NO floor on its
# own totals. Its summary was `if [ "$FAIL" -gt 0 ]`, so `0 passed / 0 failed` exited
# 0, and any section dying early reported green. Measured by truncating the suite
# after section [1]: `=== unit results: 2 passed / 0 failed ===`, exit 0 — a run that
# checked 2 of 245 things and was indistinguishable from a full pass.
#
# Do not mistake [16b]'s `NESTED-FLOOR:` line for this. That is a PARITY check
# comparing a child run's count to the parent's for equality, and `0 == 0` satisfies
# it perfectly; it says nothing about either run being non-empty. That confusion has
# already caused this defect to be closed once as "already fixed" on the strength of
# grep hits for the word "floor" — see /testing-practices § *A grep that matches your
# vocabulary has not necessarily found your mechanism*.
#
# Measured at de22410: 245; 246 after QUM-1155 repointed [15p] at the e2e-matrix
# skill and added its existence precondition + the two cut-stays-cut assertions;
# 248 once [15p] also pinned the TUI mandate and the discoverability principle.
# Stable across repeated runs. Environment-independent by
# construction (the claude/skip paths are driven with PATH=/nonexistent rather than
# by probing the host). Bump it when assertions are added or removed; a suite-size
# figure is branch-relative, so re-measure rather than trusting this comment.
#
# 283 once QUM-1029 added section [17] (35 assertions: the per-row assertion floor),
# then 287 with the two cases review added: the past-int64 declaration (F1) and
# 17k, which pins that a genuine failure is not re-summarised as a floor breach (F2).
# Measured on the RED-first run — [17]'s totals are pass/fail-invariant, every one of
# its assertions fires exactly once in either direction, so the figure does not move
# when the implementation lands.
# 401 once QUM-957 added section [18] (114 assertions: capture_pane must not
# swallow a tmux failure). Re-measured on a full green run, not derived by
# arithmetic on the red one. Section [18] is pass/fail-invariant in the same way
# [17] is — every arm is a symmetric if/else or a helper that records exactly one
# verdict in either direction, and the one `for` loop iterates a fixed literal
# list — so the figure does not move when an arm flips. The EXCEPTION is
# deliberate: [18]'s outer fixture guard (unreadable lib, failed mktemp, failed
# fake-tmux build) records a single fail in place of ~90 assertions, and the floor
# is exactly what turns that into a red instead of a suspiciously small green.
#
# 454 once QUM-1186 lane 5 added section [19] (53 assertions: the e2e suite's
# own observability probe, its corpus lint, and the accounting for the row this
# lane deleted). Re-measured on a FULL GREEN run — 454 passed / 0 failed —
# never derived by arithmetic on the red one, per the paragraph above. Section
# [19] is pass/fail-invariant in the same way [17] and [18] are: every arm is a
# symmetric if/else, and its two `while` loops iterate fixed literal lists. The
# deliberate exceptions are the fixture guards ([19b]'s eight, [19c]'s six,
# [19d]'s three, [19e]'s four), which each emit one `fail` PER LOST ARM rather
# than one for the whole block, precisely so the count does not move when a
# fixture cannot be built.
#
# 462 once QUM-1186 lane 5 converted [19d]'s `idle-reclaim` PROSE pin into three
# ARTIFACT pins (the row exists / declares a positive MIN_ASSERTIONS / names
# StopAfterTurn) plus five controls: four positive (no floor, MIN_ASSERTIONS=0,
# missing subject, missing file) and one negative (the real row must satisfy the
# floor check, or the arms are green only because the check is lenient). Net +8
# over 454. Re-measured on a full green run.
#
# 462 currently EQUALS the observed count exactly, which is an invitation to
# "simplify" this into something derived. Do not. A floor computed from the corpus
# it checks is satisfied by an empty corpus — the very defect — and a floor that
# tracks coverage follows it DOWN, so deleting assertions would silently stop
# being detectable. The equality is a coincidence of the last measurement, not a
# rule: raise it deliberately when you add assertions, and if you remove some,
# lower it deliberately and say why. Same treatment as QUM-1029's per-row floor.
# 479 once QUM-1186 lane 5 extended [19c] with the never-existed-name scan over
# the TRACKED TREE (git ls-files minus docs/archive/ minus P19_EXCLUDE): eight
# arms (corpus floor + its control, exclusion accounting, token-list floor,
# canonical-absence bound, the scan itself, unreadable accounting, exemption
# ceiling) plus five controls (planted skill-shaped markdown fires; clean
# markdown stays quiet; an empty token list reports clean on a known-dirty
# subject; the canonical-absence check fires on a set shipping the name; the
# whole-file P19-INERT-ROW exemption does not leak outside scripts/e2e-tests/).
# Net +13 over 466, then +1 for the exemption-ceiling control code review asked
# for (the ceiling now counts LINE-LEVEL exemptions only, so it needs an arm
# proving the counter can reach 1). Re-measured on a FULL GREEN run — 480
# passed / 0 failed.
#
# 486 once QA's Category-3 sweep landed: the inert-row floor-annotation arm and
# its corpus floor (+2), and four controls for it (bare floor flagged;
# annotated not flagged; annotation WRAPPED across lines not flagged; a live
# row's reachable floor not flagged). Re-measured on a FULL GREEN run.
#
# 489 with the maildir/queue mismatch arm and its two controls (a planted
# mismatch flagged; a genuine queue assertion with no maildir call NOT flagged,
# which is what keeps it from becoming a corpus-wide "queue" ban).
#
# 496 with the decline-to-judge arm: the clean-corpus verdict, the marker's
# ceiling, and five controls (an unmarked site flagged; the same message with the
# marker not flagged; an ordinary fail not flagged; the same language in a COMMENT
# not flagged; the marker counter distinguishing a marked site from an unmarked
# one). Re-measured on a FULL GREEN run — 496 passed / 0 failed.
#
# 498 with the two controls code review's findings asked for: the anchor's lower
# boundary (an `else fail "` site must be flagged, the direction that loses coverage
# rather than the three that over-match) and the unreadable-member false green (one
# chmod 000 ahead of the rows in sort order used to abort a single awk and green the
# arm with six real violators present). Re-measured on a FULL GREEN run.
#
# 499 with the maildir/queue scan's own unreadable-member control — the mutation that
# proved the fix above showed that sibling silently tolerating the same defect, so it
# was swept in the same diff rather than left as a known instance. Re-measured on a
# FULL GREEN run.
# 498 after QUM-1197 items 2/5 un-skipped idle-reclaim-busy: the [19d] pin that
# asserted the matrix table names QUM-1197 as the busy row's blocker was DELETED,
# because the blocker is gone and a table naming a fixed hazard is a false record.
# One pin removed = one assertion. Lowering a floor is the direction that hides
# things, so it is stated here rather than left to be inferred from the number.
# Re-measured on a FULL GREEN run.
# 526 once QUM-1118 added section [20] (22 assertions: the disk-space
# precondition — healthy/unfit verdicts for each filesystem checked
# independently and both together, the boundary at the default 4096MB
# threshold, the SPRAWL_E2E_MIN_FREE_MB override and its malformed-value
# rejection, and two full driver-integration runs proving a startup-time and a
# mid-run exhaustion each abort with exit 5 before/between rows rather than
# reporting an ordinary row FAIL) PLUS 6 from [16]'s own machinery
# auto-scaling with the two new SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP/_REPO
# seams QUM-1118 registers (1 [16a] pass + 2 [16b] passes per seam x 2 seams).
# 532 after code review (F1-F9 in .sprawl/agents/ratz/findings/qum-1118-1119-review.md)
# fixed a real regression (F1: driver-level sourcing of $LIB silently disabled
# cross-row fault-ledger isolation) and its 3 new assertions (20j), plus F2's
# fix (seams now warn-and-refuse-to-guess rather than silently falling back to
# real df) restructured 20a into a proper positive+negative pair and added a
# true no-seam negative control (20a2), net +3. Section [20] is pass/fail-
# invariant in the same way [17]/[18]/[19] are: every arm is a symmetric
# assertion pair (positive result + its distinguishing message content), so
# the count does not move when the implementation's behaviour flips.
# Re-measured on a FULL GREEN run — 532 passed / 0 failed.
# 559 once QUM-974/QUM-973/QUM-1181 added sections [21] (15 assertions: the
# e2e_recover_oauth_token return-contract fix — fast path, forced-failure
# diagnostic content, a real-ancestor regression control, and a
# driver-integration proof that a failed recovery aborts the row as an
# ordinary FAIL rather than a QUM-952 skip) and [22] (9 assertions:
# e2e_launch_tui's SPRAWL_CLAUDE default/override/extra-env forwarding,
# e2e_make_sandbox_root's .env copy with mode preserved, its negative
# direction — no .env manufactures none — and an end-to-end tie between both
# fixes proving "no auth" still fails loudly rather than silently passing) —
# 24 assertions — PLUS 3 from [16]'s own machinery auto-scaling with the one
# new seam these sections register, SPRAWL_E2E_MATRIX_DEBUG_OAUTH_SCAN_PID (1
# [16a] pass + 2 [16b] passes, same accounting QUM-1118's two seams used
# above). Re-measured on a FULL GREEN run — 559 passed / 0 failed.
MIN_ASSERTIONS=559
# A [16b] nested child deliberately does NOT re-run section [16] (recursing would
# fork-bomb, and counting there would corrupt the parity comparison), so it asserts
# strictly fewer things and needs its own floor. Measured at de22410: 237; 238 after
# QUM-1155, then 240 with [15p]'s two new pins (see the parent floor above). It is
# the same number [16] emits as `NESTED-FLOOR:`. Keeping it a separate literal rather
# than reusing the parent's is the point — a child floor derived from the parent's
# count would be the parity check again, and parity is what `0 == 0` satisfies.
# 275 once QUM-1029 added section [17], which the child DOES run; 279 with F1 and 17k.
# 454 once lane 5's [19d] artifact-pin conversion landed; 446 before it. Both
# measured with UNIT_NESTED_SEAM_CHECK set, minus the deliberate 16c fail.
# 446 once QUM-1186 lane 5 added section [19], which the child DOES run.
# Measured with UNIT_NESTED_SEAM_CHECK set: "446 passed / 1 failed", minus the
# deliberate 16c fail, per the recipe below.
# 393 once QUM-957 added section [18], which the child DOES run. Measured, not
# derived by subtracting [16]'s size from the parent. To re-measure: run this file
# with UNIT_NESTED_SEAM_CHECK set to any value and SUBTRACT ONE from the total —
# without a live nonce 16c's deliberate fail fires, so a hand-driven child reads
# exactly one higher than a real [16b] child ("393 passed / 1 failed" here).
# 472 once lane 5's [19c] never-existed tree scan landed (+14; the child DOES
# run [19]). Measured with UNIT_NESTED_SEAM_CHECK set: "472 passed / 1 failed",
# the one fail being 16c's deliberate one, per the recipe above.
# 478 once the inert-row floor-annotation arm landed (+6; the child DOES run
# [19]), then 481 with the maildir/queue mismatch arm and its two controls.
# Measured with UNIT_NESTED_SEAM_CHECK set: "481 passed / 1 failed", the one
# fail being 16c's deliberate one.
# 488 once the decline-to-judge arm landed (+7; the child DOES run [19], so it
# gains all seven of the parent's new assertions). Measured with
# UNIT_NESTED_SEAM_CHECK set: "488 passed / 1 failed", the one fail being 16c's
# deliberate one.
# 490 with the anchor-boundary and unreadable-member controls (+2; the child DOES
# run [19]). Measured with UNIT_NESTED_SEAM_CHECK set: "490 passed / 1 failed", the
# one fail being 16c's deliberate one.
# 491 with the maildir/queue unreadable control (+1). Measured with
# UNIT_NESTED_SEAM_CHECK set: "491 passed / 1 failed", the one fail being 16c's.
# 490 after the same QUM-1197 pin deletion (-1). Measured with
# UNIT_NESTED_SEAM_CHECK set.
# 512 once QUM-1118 added section [20], which the child DOES run (+22, same as
# the parent floor above — section [20] does not reference
# UNIT_NESTED_SEAM_CHECK, so it behaves identically in a nested child).
# Measured with UNIT_NESTED_SEAM_CHECK set: "512 passed / 1 failed", the one
# fail being 16c's deliberate one, per the recipe above.
# 518 after the same code-review fixes as the parent floor above (+6, the
# child DOES run [20]). Measured directly (UNIT_NESTED_SEAM_CHECK set to a
# valid nonce, not via [16b]'s deliberate-bad-nonce recipe): "518 passed / 0
# failed".
# 542 once sections [21]/[22] landed (+24, same as the parent floor above —
# neither section references UNIT_NESTED_SEAM_CHECK, so the child runs both
# in full).
MIN_ASSERTIONS_NESTED=542

# Pin the temp root. This suite runs inside `make validate` and therefore inside
# the pre-commit hook, so it must not inherit the committing agent's TMPDIR:
# several /tmp-anchored guards here and in scripts/lib/e2e-common.sh (which hard
# exits unless the sandbox root is under /tmp/) would otherwise fail for reasons
# unrelated to the caller's change, blocking every commit and leaking fixture
# dirs. Pinning makes the gate independent of the environment that invoked it.
if [ ! -d /tmp ]; then
	echo "fatal: /tmp is not a directory; this suite requires it" >&2
	exit 2
fi
export TMPDIR=/tmp
UNIT_TMP_ROOT=${TMPDIR%/}

# The variables this suite must never inherit, and the single place they are
# registered. Every entry either makes the driver misbehave on purpose (the
# debug seams) or redirects state the driver owns (the skip sentinel), so an
# inherited value flips this suite's negative controls green for a reason that
# has nothing to do with the code under test.
#
# They are removed from THIS process's environment rather than scrubbed per
# call site, because the suite invokes the driver from four places and three of
# them cannot route through _unit_run_env: [7] and [8] run the real $DRIVER (so
# TMPDIR must not be a fixture dir) and [10] uses PATH=/nonexistent with an
# absolute bash and its own fixture tree. A per-site scrub is a rule the next
# call site has to remember; unsetting is a rule it cannot forget.
#
# Adding a debug seam to scripts/e2e-matrix.sh? Register it here — [16a] reads
# the seam names out of the driver and fails if one is missing.
UNIT_SCRUBBED_VARS=(
	SPRAWL_E2E_SKIP_NO_CLAUDE
	E2E_SKIP_FILE
	SPRAWL_E2E_MATRIX_DEBUG_TALLY_SKEW
	SPRAWL_E2E_MATRIX_DEBUG_STALE_SENTINEL
	# QUM-957. Redirects state the harness owns, exactly like E2E_SKIP_FILE: an
	# operator with this exported hands every fixture row a ledger it did not
	# create, so a fault from a previous run fails an unrelated row and section
	# [18]'s empty-ledger negative controls go red for a reason that has nothing
	# to do with the code under test.
	E2E_CAPTURE_FAULT_FILE
	# QUM-1118. Debug seams for e2e_check_disk_space's free-space reading — an
	# inherited value here would make section [20]'s healthy-environment
	# baselines (20a/20k) silently measure whatever the operator's shell
	# happened to export instead of the code under test.
	SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP
	SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_REPO
	# QUM-1118. A production override, not a `_DEBUG_` seam (so [16a] will not
	# auto-discover it), but it needs the same scrubbing for the same reason
	# SPRAWL_E2E_SKIP_NO_CLAUDE does: an inherited value would flip section
	# [20]'s negative controls (20a/20b/20d/20k, none of which set it) for a
	# reason that has nothing to do with the code under test.
	SPRAWL_E2E_MIN_FREE_MB
	# QUM-974. Test-only seam for e2e_recover_oauth_token's ancestor-walk
	# starting pid — an inherited value here would make section [21]'s
	# negative-direction cases (21b/21d, which explicitly set it to force a
	# deterministic failure) fire for a reason that has nothing to do with
	# the code under test, and would falsely arm 21b/21d for the wrong pid
	# in a caller's shell that happens to export it.
	SPRAWL_E2E_MATRIX_DEBUG_OAUTH_SCAN_PID
)
UNIT_SCRUB_ARGS=()
for _v in "${UNIT_SCRUBBED_VARS[@]}"; do
	UNIT_SCRUB_ARGS+=(-u "$_v")
done
unset "${UNIT_SCRUBBED_VARS[@]}"

# MIN_ASSERTIONS is scrubbed from the DRIVER's environment but deliberately NOT
# registered above: this suite assigns its own MIN_ASSERTIONS at the top of the
# file, and the `unset` on the registry would destroy that floor. An operator
# with `export MIN_ASSERTIONS=...` would otherwise have the export attribute
# survive our assignment and hand the [17] fixture rows a floor they never
# declared, turning the undeclared-floor case green for a reason unrelated to
# the code under test.
UNIT_SCRUB_ARGS+=(-u MIN_ASSERTIONS)

# This file's own path, for the nested self-check in [16b]. Derived from
# BASH_SOURCE, not $0: the Makefile invokes the suite by a relative path and
# the cd above has already moved, so $0 resolves only by coincidence of cwd.
UNIT_SELF="$SCRIPT_DIR/$(basename -- "${BASH_SOURCE[0]}")"

pass() {
	PASS=$((PASS + 1))
	echo "  PASS: $1"
}

# Print an advisory line WITHOUT counting it as a pass. A no-op guard must not
# inflate the pass total: that would let coverage silently drop while the suite
# still reports all-green, which is the defect class this file exists to catch.
note() {
	echo "  NOTE: $1"
}

fail() {
	FAIL=$((FAIL + 1))
	echo "  FAIL: $1" >&2
}

assert_true() {
	# $1 = description, remaining args = command
	local desc=$1
	shift
	if "$@" >/dev/null 2>&1; then
		pass "$desc"
	else
		fail "$desc (cmd: $*)"
	fi
}

echo "=== QUM-616 Wave 1 unit tests ==="

# ----------------------------------------------------------------------------
# 1. Library file exists & sources cleanly
# ----------------------------------------------------------------------------
echo "[1] library file present and sources cleanly"
if [ -r "$LIB" ]; then
	pass "scripts/lib/e2e-common.sh exists and is readable"
	(
		set -e
		# shellcheck disable=SC1090
		. "$LIB"
	)
	if [ $? -eq 0 ]; then
		pass "sourcing scripts/lib/e2e-common.sh exits 0"
	else
		fail "sourcing scripts/lib/e2e-common.sh failed"
	fi
else
	fail "scripts/lib/e2e-common.sh not readable"
fi

# ----------------------------------------------------------------------------
# 2. Expected helper functions defined after sourcing
# ----------------------------------------------------------------------------
echo "[2] expected helper functions are defined"
EXPECTED_FUNCS=(
	e2e_recover_oauth_token
	e2e_setup_tmux_socket
	e2e_skip_row
	e2e_require_claude_or_skip
	e2e_require_tmux
	e2e_require_jq
	e2e_build_sprawl
	e2e_make_sandbox_root
	e2e_init_sandbox_repo
	e2e_install_cleanup_traps
	capture_pane
	wait_for_pattern
	wait_for_pattern_fast
	wait_for_substring_fast
	e2e_launch_tui
	e2e_attach_phantom_client
	e2e_send_user_prompt
	pass
	fail
	e2e_print_results
)
for fn in "${EXPECTED_FUNCS[@]}"; do
	(
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		declare -F "$fn" >/dev/null 2>&1
	)
	if [ $? -eq 0 ]; then
		pass "function defined: $fn"
	else
		fail "function NOT defined: $fn"
	fi
done

# ----------------------------------------------------------------------------
# 3. e2e_make_sandbox_root creates /tmp dir and exports SPRAWL_ROOT
# ----------------------------------------------------------------------------
echo "[3] e2e_make_sandbox_root creates /tmp dir and exports SPRAWL_ROOT"
(
	# shellcheck disable=SC1090
	. "$LIB" >/dev/null 2>&1 || exit 99
	e2e_make_sandbox_root "matrix-unit-test" >/dev/null 2>&1 || exit 1
	case "$SPRAWL_ROOT" in
		/tmp/*) : ;;
		*) exit 2 ;;
	esac
	[ -d "$SPRAWL_ROOT" ] || exit 3
	# clean up only if under /tmp
	case "$SPRAWL_ROOT" in
		/tmp/*) rm -rf "$SPRAWL_ROOT" ;;
	esac
	exit 0
)
case $? in
	0) pass "e2e_make_sandbox_root: SPRAWL_ROOT under /tmp/ and dir exists" ;;
	*) fail "e2e_make_sandbox_root: SPRAWL_ROOT misconfigured or missing" ;;
esac

# ----------------------------------------------------------------------------
# 4. e2e_require_claude_or_skip signals a skip DISTINGUISHABLY (QUM-952).
#
#    This section used to assert `rc == 0`, which pinned the QUM-952 bug: exit 0
#    cannot mean both "passed" and "skipped", so the driver counted every skipped
#    row as a pass. The contract is now rc 77 (autotools SKIP convention) PLUS a
#    non-empty sentinel file at $E2E_SKIP_FILE — double-keyed so a row that
#    merely happens to exit 77 cannot forge a skip.
# ----------------------------------------------------------------------------
echo "[4] e2e_require_claude_or_skip signals skip via rc 77 + sentinel"
# mktemp, not a predictable name: /tmp is world-writable, and a pre-existing or
# symlinked entry would either break `: >` confusingly or truncate someone
# else's file.
sentinel=$(mktemp "$UNIT_TMP_ROOT/e2e-matrix-unit-sentinel.XXXXXX")
out=$(
	set +e
	# Use a subshell rather than re-execing bash, since PATH=/nonexistent
	# would break `bash -c`. The function still sees an empty PATH for its
	# own `command -v claude` lookup.
	(
		export PATH=/nonexistent
		export SPRAWL_E2E_SKIP_NO_CLAUDE=1
		export E2E_SKIP_FILE="$sentinel"
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_require_claude_or_skip "fixture"
	) 2>&1
)
rc=$?
if [ $rc -eq 77 ]; then
	pass "skip path exits 77, not 0"
else
	fail "skip path want rc=77, got rc=$rc out=$out"
fi
# Anchored: the FATAL branch's hint text also contains the word "skip", so an
# unanchored case-insensitive match cannot tell the two branches apart.
if echo "$out" | grep -q '^SKIP'; then
	pass "skip path prints an anchored SKIP line"
else
	fail "skip path printed no SKIP marker out=$out"
fi
if [ -s "$sentinel" ] && grep -q "fixture" "$sentinel"; then
	pass "skip path writes a non-empty sentinel naming the caller"
else
	fail "skip sentinel empty or missing caller name: '$(cat "$sentinel" 2>/dev/null)'"
fi

# 4b. NEGATIVE CONTROL / H4: the FATAL branch (no SPRAWL_E2E_SKIP_NO_CLAUDE)
#     must NOT write the sentinel. Otherwise a hard failure could be laundered
#     into a skip by any stale-read bug in the driver.
: >"$sentinel"
out=$(
	set +e
	(
		export PATH=/nonexistent
		unset SPRAWL_E2E_SKIP_NO_CLAUDE
		export E2E_SKIP_FILE="$sentinel"
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_require_claude_or_skip "fixture"
	) 2>&1
)
rc=$?
if [ $rc -eq 1 ] && echo "$out" | grep -q "FATAL"; then
	pass "4b: without the skip env var the gate is still a hard FATAL (rc 1)"
else
	fail "4b: want rc=1 with FATAL, got rc=$rc out=$out"
fi
if [ -s "$sentinel" ]; then
	fail "4b: FATAL branch wrote the skip sentinel: '$(cat "$sentinel")'"
else
	pass "4b: FATAL branch leaves the skip sentinel empty"
fi

# 4c. With E2E_SKIP_FILE unset (lib sourced outside the driver) the helper must
#     still exit 77 and must not trip `set -u`. A `set -u` abort would surface as
#     rc 1, so the rc check alone discriminates; stderr is left visible so a
#     failure here is diagnosable rather than a bare rc.
out=$(
	set +e
	(
		set -u
		export PATH=/nonexistent
		export SPRAWL_E2E_SKIP_NO_CLAUDE=1
		unset E2E_SKIP_FILE
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null || exit 99
		e2e_require_claude_or_skip "fixture"
	) 2>&1
)
rc=$?
if [ $rc -eq 77 ]; then
	pass "4c: skip still exits 77 with E2E_SKIP_FILE unset under set -u"
else
	fail "4c: want rc=77, got rc=$rc out=$out"
fi

# 4d. THIRD LEG OF THE TRUTH TABLE: when claude IS present the helper must
#     return 0 and leave the sentinel untouched. Without this, an implementation
#     that unconditionally writes the sentinel and exits 77 would satisfy every
#     other assertion in this file — the gate would then skip everything forever,
#     which is a worse version of the bug being fixed. A stub on a private PATH
#     keeps this hermetic instead of depending on the host having claude.
stub_bin=$(mktemp -d "$UNIT_TMP_ROOT/e2e-matrix-unit-stubbin.XXXXXX")
printf '#!/bin/sh\nexit 0\n' >"$stub_bin/claude"
chmod +x "$stub_bin/claude"
: >"$sentinel"
out=$(
	set +e
	(
		export PATH="$stub_bin"
		export SPRAWL_E2E_SKIP_NO_CLAUDE=1
		export E2E_SKIP_FILE="$sentinel"
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_require_claude_or_skip "fixture"
		echo "REACHED"
	) 2>&1
)
rc=$?
if [ $rc -eq 0 ] && echo "$out" | grep -q "REACHED"; then
	pass "4d: with claude present the gate returns 0 and execution continues"
else
	fail "4d: want rc=0 and continued execution, got rc=$rc out=$out"
fi
if [ -s "$sentinel" ]; then
	fail "4d: sentinel written even though claude is present: '$(cat "$sentinel")'"
else
	pass "4d: sentinel untouched when claude is present"
fi
rm -f "$sentinel"
rm -rf "$stub_bin"

# ----------------------------------------------------------------------------
# 5. PASS_COUNT and FAIL_COUNT initialized to 0
# ----------------------------------------------------------------------------
echo "[5] counters initialized to 0"
(
	# shellcheck disable=SC1090
	. "$LIB" >/dev/null 2>&1 || exit 99
	[ "${PASS_COUNT:-unset}" = "0" ] || exit 1
	[ "${FAIL_COUNT:-unset}" = "0" ] || exit 2
)
case $? in
	0) pass "PASS_COUNT and FAIL_COUNT both 0 after sourcing" ;;
	*) fail "PASS_COUNT/FAIL_COUNT not initialized to 0 (rc=$?)" ;;
esac

# ----------------------------------------------------------------------------
# 6. pass and fail increment counters
# ----------------------------------------------------------------------------
echo "[6] pass and fail increment counters"
(
	# shellcheck disable=SC1090
	. "$LIB" >/dev/null 2>&1 || exit 99
	pass "x" >/dev/null 2>&1
	pass "x" >/dev/null 2>&1
	fail "y" >/dev/null 2>&1
	[ "${PASS_COUNT}" -eq 2 ] || exit 1
	[ "${FAIL_COUNT}" -eq 1 ] || exit 2
)
case $? in
	0) pass "pass x2 + fail x1 yields PASS_COUNT=2 FAIL_COUNT=1" ;;
	*) fail "counter increment broken (rc=$?)" ;;
esac

# ----------------------------------------------------------------------------
# 7. Driver --list discovers merge-reuse
# ----------------------------------------------------------------------------
echo "[7] driver --list discovers merge-reuse"
out=$(bash "$DRIVER" --list 2>&1)
rc=$?
if [ $rc -eq 0 ] && echo "$out" | grep -qx "merge-reuse"; then
	pass "driver --list lists merge-reuse"
else
	fail "driver --list rc=$rc out=$out"
fi

# ----------------------------------------------------------------------------
# 8. Driver unknown row exits 2
# ----------------------------------------------------------------------------
echo "[8] driver unknown row exits 2 with stderr"
stderr_file=$(mktemp 2>/dev/null || echo "/tmp/e2e-matrix-unit-stderr.$$")
bash "$DRIVER" definitely-not-a-row >/dev/null 2>"$stderr_file"
rc=$?
stderr_content=$(cat "$stderr_file" 2>/dev/null)
rm -f "$stderr_file"
if [ $rc -eq 2 ] && [ -n "$stderr_content" ]; then
	pass "unknown row exits 2 and writes to stderr"
else
	fail "unknown row rc=$rc stderr='$stderr_content'"
fi

# ----------------------------------------------------------------------------
# 10. Metadata flags are honored via fixture rows (preflight skip + no-flags run)
# ----------------------------------------------------------------------------
echo "[10] metadata flags are honored via fixture rows"

if [ ! -r "$LIB" ] || [ ! -r "$DRIVER" ]; then
	fail "metadata fixture test skipped (lib or driver missing)"
else
	FIXDIR=$(mktemp -d 2>/dev/null)
	if [ -z "$FIXDIR" ] || [ ! -d "$FIXDIR" ]; then
		fail "could not mktemp fixture dir"
	else
		mkdir -p "$FIXDIR/lib" "$FIXDIR/e2e-tests"
		if ! cp "$LIB" "$FIXDIR/lib/e2e-common.sh" 2>/dev/null; then
			fail "[10] setup: could not copy e2e-common.sh into the fixture"
		fi
		# QUM-957: e2e-common.sh sources capture-pane.sh as a sibling, so this
		# hand-rolled fixture needs it too — same requirement _unit_mk_fixture_tree
		# already documents. A missing sibling here aborts the whole fixture
		# driver with an opaque "No such file or directory" rather than a named
		# cause, so a failed copy is checked (code review finding) rather than
		# left to surface as an unrelated-looking failure three tests later.
		if ! cp "$REPO_ROOT/scripts/lib/capture-pane.sh" "$FIXDIR/lib/capture-pane.sh" 2>/dev/null; then
			fail "[10] setup: could not copy capture-pane.sh into the fixture"
		fi
		if ! cp "$DRIVER" "$FIXDIR/e2e-matrix.sh" 2>/dev/null; then
			fail "[10] setup: could not copy e2e-matrix.sh into the fixture"
		fi

		# Fixture A: needs_claude=1 — should SKIP under SPRAWL_E2E_SKIP_NO_CLAUDE=1
		cat >"$FIXDIR/e2e-tests/_unit_fixture_claude.sh" <<'EOF'
test_metadata() { echo "needs_claude=1"; }
test_run() { echo "SHOULD NOT RUN"; exit 1; }
EOF

		# Fixture B: no flags — should run test_run and print RAN
		cat >"$FIXDIR/e2e-tests/_unit_fixture_noflags.sh" <<'EOF'
test_metadata() { echo ""; }
test_run() { echo "RAN"; }
EOF

		# Test 10a: claude-required fixture skipped — and NOT counted as a pass.
		# Resolve bash by absolute path so the PATH=/nonexistent prefix can
		# scope the modified PATH to the driver process without breaking
		# the `bash` lookup itself (see comment on test 4 above).
		# QUM-952: this used to assert rc == 0, i.e. it pinned the bug.
		# TMPDIR is scoped to the fixture dir so the driver's skip sentinel is
		# reaped with it: cleanup uses `rm`, which this scrubbed PATH removes.
		BASH_ABS=$(command -v bash)
		out=$(
			set +e
			PATH=/nonexistent TMPDIR="$FIXDIR" SPRAWL_E2E_SKIP_NO_CLAUDE=1 \
				"$BASH_ABS" "$FIXDIR/e2e-matrix.sh" _unit_fixture_claude 2>&1
		)
		rc=$?
		# rc 3 exactly, not merely nonzero: rc 1 would mean the driver counted
		# the skip as a failure, which is a different (also wrong) behavior.
		if [ $rc -eq 3 ] && ! echo "$out" | grep -q "SHOULD NOT RUN"; then
			pass "needs_claude=1 fixture skipped under SPRAWL_E2E_SKIP_NO_CLAUDE=1 (exit 3)"
		else
			fail "needs_claude fixture want rc=3, got rc=$rc out=$out"
		fi
		if echo "$out" | grep -q '^SKIP _unit_fixture_claude$'; then
			pass "10a: the skipped row gets its own SKIP verdict line"
		else
			fail "10a: no 'SKIP _unit_fixture_claude' verdict line out=$out"
		fi
		if echo "$out" | grep -q '^PASS _unit_fixture_claude$'; then
			fail "10a: a skipped row was reported as PASS"
		else
			pass "10a: a skipped row is not reported as PASS"
		fi

		# Test 10b: no-flags fixture actually runs
		out=$(
			set +e
			bash "$FIXDIR/e2e-matrix.sh" _unit_fixture_noflags 2>&1
		)
		rc=$?
		if [ $rc -eq 0 ] && echo "$out" | grep -q "RAN"; then
			pass "no-flags fixture executes test_run"
		else
			fail "no-flags fixture rc=$rc out=$out"
		fi

		rm -rf "$FIXDIR"
	fi
fi

# ----------------------------------------------------------------------------
# 11. Makefile targets exist
# ----------------------------------------------------------------------------
echo "[11] Makefile targets exist"
if grep -E '^test-e2e-matrix:' "$MAKEFILE" >/dev/null 2>&1; then
	pass "Makefile defines test-e2e-matrix target"
else
	fail "Makefile missing test-e2e-matrix target"
fi
if grep -E '^test-e2e-matrix-%:' "$MAKEFILE" >/dev/null 2>&1; then
	pass "Makefile defines test-e2e-matrix-% pattern target"
else
	fail "Makefile missing test-e2e-matrix-% pattern target"
fi
# QUM-947: this file is only a real gate if it actually runs. Assert its own
# wiring, so the "runs on every commit" property cannot be silently reverted
# while the suite keeps reporting all-green — that would be this very file's
# defect class turned on itself.
if grep -E '^test-e2e-matrix-unit:' "$MAKEFILE" >/dev/null 2>&1; then
	pass "Makefile defines test-e2e-matrix-unit target"
else
	fail "Makefile missing test-e2e-matrix-unit target"
fi
if grep -E '^validate:.*[[:space:]]test-e2e-matrix-unit([[:space:]]|$)' "$MAKEFILE" >/dev/null 2>&1; then
	pass "Makefile validate target depends on test-e2e-matrix-unit"
else
	fail "Makefile validate target no longer runs test-e2e-matrix-unit — this suite would stop gating commits"
fi

# ----------------------------------------------------------------------------
# 12. Original merge-reuse script unmodified
# ----------------------------------------------------------------------------
echo "[12] original test-merge-reuse-e2e.sh untouched"
if [ -r "$ORIG_MERGE" ]; then
	pass "scripts/test-merge-reuse-e2e.sh still exists"
	if grep -q "QUM-511 reproduced" "$ORIG_MERGE"; then
		pass "scripts/test-merge-reuse-e2e.sh still contains QUM-511 sentinel"
	else
		fail "scripts/test-merge-reuse-e2e.sh missing QUM-511 sentinel — script was modified!"
	fi
else
	# The guard's intent is "nobody silently EDITS the legacy fallback script",
	# not "the legacy script must exist forever". CLAUDE.md states the
	# scripts/test-*-e2e.sh fallbacks will be deleted once the matrix rows have
	# soaked. Since QUM-947 wired this file into `make validate` (and therefore
	# into the pre-commit hook), treating that planned deletion as a hard failure
	# would break `git commit` in every worktree until someone noticed.
	#
	# But the branch must still ASSERT something, or the guard evaporates with
	# the pass count still reading green. So: deletion is allowed, and required
	# to be COHERENT — the Makefile must not still invoke a script that is gone.
	if grep -q 'scripts/test-merge-reuse-e2e\.sh' "$MAKEFILE"; then
		fail "test-merge-reuse-e2e.sh deleted but Makefile still invokes it — stale target"
	else
		pass "legacy merge-reuse fallback removed coherently (Makefile reference gone too)"
	fi
fi

# ----------------------------------------------------------------------------
# 13. Row file exists and declares required functions
# ----------------------------------------------------------------------------
echo "[13] merge-reuse row file present with test_metadata + test_run"
if [ -r "$ROW" ]; then
	pass "scripts/e2e-tests/merge-reuse.sh exists"
	if grep -qE '^test_metadata\(\)' "$ROW"; then
		pass "merge-reuse.sh declares test_metadata()"
	else
		fail "merge-reuse.sh missing test_metadata()"
	fi
	if grep -qE '^test_run\(\)' "$ROW"; then
		pass "merge-reuse.sh declares test_run()"
	else
		fail "merge-reuse.sh missing test_run()"
	fi
else
	fail "scripts/e2e-tests/merge-reuse.sh not readable"
fi

# ----------------------------------------------------------------------------
# 14. QUM-947: multi-row invocation, summary denominator, fail-fast validation.
#
#     The driver used to read only $1 and silently discard $2..$N, then print
#     "=== Matrix: 1/1 passed ===" and exit 0 — so `e2e-matrix.sh a b c` ran ONE
#     row and reported success. A false green in the mandatory-gate harness.
#
#     Governing principle (QUM-943): a harness that reports pass/fail must be
#     unable to report pass for work it did not do. These tests therefore assert
#     on OBSERVABLE PER-ROW SIDE EFFECTS — one marker file per row that actually
#     executed, plus a count of per-row verdict lines cross-checked against the
#     reported denominator — and never on the summary text alone. A driver that
#     lies about its denominator would also lie about its summary.
#
#     14b is the negative control and is load-bearing: a suite that only ever
#     observes success cannot detect a harness that always reports success.
# ----------------------------------------------------------------------------
echo "[14] QUM-947 multi-row invocation, denominator, and fail-fast validation"

# Write a fixture row that records its own execution and exits with a chosen
# code. $1=e2e-tests dir, $2=row name, $3=exit code returned by test_run.
# UNIT_MARKER_DIR is dereferenced with :? so a missing env var fails the row
# loudly rather than silently touching some relative path.
_unit_mk_marker_row() {
	cat >"$1/$2.sh" <<EOF
test_metadata() { echo ""; }
test_run() {
	: >"\${UNIT_MARKER_DIR:?UNIT_MARKER_DIR unset}/$2"
	echo "RAN $2"
	return $3
}
EOF
}

# Build a self-contained fixture driver tree at $1 (lib + e2e-tests + markers).
_unit_mk_fixture_tree() {
	mkdir -p "$1/lib" "$1/e2e-tests" "$1/markers" || return 1
	cp "$LIB" "$1/lib/e2e-common.sh" || return 1
	# QUM-957: e2e-common.sh sources capture-pane.sh as a sibling, so the fixture
	# tree needs it too. Copied rather than symlinked, and NOT optional: without
	# it the fixture rows source a lib that fails halfway, which is how this
	# omission stayed invisible while a `declare -F` guard papered over it.
	cp "$REPO_ROOT/scripts/lib/capture-pane.sh" "$1/lib/capture-pane.sh" || return 1
	cp "$DRIVER" "$1/e2e-matrix.sh" || return 1
}

# Empty the marker dir and PROVE it is empty. A silently-failing reset would
# let the positive assertions in 14d/14e pass on stale markers left by 14a/14c —
# which is the very vacuity this section exists to rule out. The path is
# asserted to live under /tmp/ before anything is deleted, and `find -delete`
# (not a `rm` glob) keeps an empty variable from ever naming a parent dir.
_unit_reset_markers() {
	local d=${1:?_unit_reset_markers: marker dir required}
	case "$d" in
		"$UNIT_TMP_ROOT"/*) : ;;
		*)
			fail "_unit_reset_markers refuses to clean '$d' (not under $UNIT_TMP_ROOT/)"
			return 1
			;;
	esac
	find "$d" -mindepth 1 -maxdepth 1 -delete 2>/dev/null
	local leftover
	leftover=$(ls -A "$d" 2>/dev/null)
	if [ -n "$leftover" ]; then
		fail "marker dir $d not empty after reset: $leftover"
	fi
}

# Run a fixture driver, capturing stdout, stderr and exit code SEPARATELY into
# the globals _RC/_OUT/_ERR, so tests can assert that errors go to stderr.
# $1=fixture dir, $2=marker dir ("" to leave UNIT_MARKER_DIR unset), rest=argv.
_unit_run() {
	local fix=${1:?} mdir=$2
	shift 2
	_unit_run_env "$fix" "$mdir" "" "$@"
}

# As _unit_run, but with an extra env prefix ($3, space-separated VAR=VAL) so a
# run can scrub PATH or set SPRAWL_E2E_SKIP_NO_CLAUDE for the driver process
# only. bash is resolved absolutely so a scrubbed PATH cannot break the exec.
#
# The `env -u` prefix is built from UNIT_SCRUBBED_VARS (the registry at the top
# of this file), so there is one authority rather than two lists that can drift.
# The registry is already unset process-wide, which is what actually protects
# the call sites that cannot route through here; this prefix additionally covers
# anything inside the suite that exports one of those names mid-run.
_unit_run_env() {
	local fix=${1:?} mdir=$2 envs=$3
	shift 3
	local of ef bash_abs
	of=$(mktemp) && ef=$(mktemp) || return 1
	bash_abs=$(command -v bash)
	# TMPDIR is pointed at the fixture dir so the driver's skip sentinel lands
	# there: its EXIT-trap cleanup uses `rm`, which is absent on the
	# PATH-scrubbed runs below, and without this the sentinel would leak into
	# the shared /tmp on every such run. The fixture dir is removed by the
	# prefix-guarded teardown, so the sentinel goes with it.
	#
	# $envs and the ${mdir:+...} expansion are deliberately UNQUOTED: each must
	# split into separate VAR=VAL words for `env`, and mdir must vanish entirely
	# when empty. Do not "fix" this by quoting them.
	# shellcheck disable=SC2086
	env "${UNIT_SCRUB_ARGS[@]}" "TMPDIR=$fix" \
		$envs ${mdir:+UNIT_MARKER_DIR=$mdir} \
		"$bash_abs" "$fix/e2e-matrix.sh" "$@" >"$of" 2>"$ef"
	_RC=$?
	_OUT=$(cat "$of")
	_ERR=$(cat "$ef")
	rm -f "$of" "$ef"
}

# Assert a row did ("yes") or did not ("no") execute, via its marker file.
# $1=marker dir, $2=row name, $3=yes|no, $4=description.
_unit_assert_ran() {
	if [ "$3" = "yes" ]; then
		if [ -f "$1/$2" ]; then pass "$4"; else fail "$4 (marker $1/$2 absent)"; fi
	else
		if [ -f "$1/$2" ]; then fail "$4 (marker $1/$2 present)"; else pass "$4"; fi
	fi
}

# Assert the exact summary line. $1=output, $2=expected passed,
# $3=expected requested (the denominator), $4=description.
# Coupling to this literal is deliberate: the string IS the contract QUM-947 is
# about, and asserting the denominator is impossible without it.
_unit_assert_summary() {
	if printf '%s\n' "$1" | grep -qF "=== Matrix: $2/$3 passed ==="; then
		pass "$4"
	else
		fail "$4 (want '=== Matrix: $2/$3 passed ===') out=$1"
	fi
}

# Assert NO summary line was printed at all (used for the exit-2 arg-error
# paths, which must bail before pretending to have run a matrix).
_unit_assert_no_summary() {
	if printf '%s\n' "$1" | grep -qF ' passed ==='; then
		fail "$2 (a summary was printed on an arg-error path) out=$1"
	else
		pass "$2"
	fi
}

# Cross-check the number of per-row verdict lines against an EXPECTED total —
# not against a value derived from the same loop that produced them. This is
# what structurally prevents "reports pass for work it did not do": the
# verdict-line count, the marker files and the denominator must all agree.
_unit_assert_verdict_lines() {
	local n
	n=$(printf '%s\n' "$1" | grep -cE '^(PASS|FAIL) ')
	if [ "$n" -eq "$2" ]; then
		pass "$3"
	else
		fail "$3 (want $2 verdict lines, got $n) out=$1"
	fi
}

if [ ! -r "$LIB" ] || [ ! -r "$DRIVER" ]; then
	fail "QUM-947 multi-row tests skipped (lib or driver missing)"
else
	FIXOK=$(mktemp -d 2>/dev/null)
	FIXFAIL=$(mktemp -d 2>/dev/null)
	FIXEMPTY=$(mktemp -d 2>/dev/null)
	if [ -z "$FIXOK" ] || [ ! -d "$FIXOK" ] ||
		[ -z "$FIXFAIL" ] || [ ! -d "$FIXFAIL" ] ||
		[ -z "$FIXEMPTY" ] || [ ! -d "$FIXEMPTY" ]; then
		fail "could not mktemp QUM-947 fixture dirs"
	else
		fixture_setup_ok=1

		# FIXOK holds exactly three always-passing rows, so `all` and the
		# no-args default are exactly assertable as 3/3.
		_unit_mk_fixture_tree "$FIXOK" || fixture_setup_ok=0
		_unit_mk_marker_row "$FIXOK/e2e-tests" _unit_fixture_m1 0 || fixture_setup_ok=0
		_unit_mk_marker_row "$FIXOK/e2e-tests" _unit_fixture_m2 0 || fixture_setup_ok=0
		_unit_mk_marker_row "$FIXOK/e2e-tests" _unit_fixture_m3 0 || fixture_setup_ok=0
		MOK="$FIXOK/markers"

		# FIXFAIL is identical except the MIDDLE row fails.
		_unit_mk_fixture_tree "$FIXFAIL" || fixture_setup_ok=0
		_unit_mk_marker_row "$FIXFAIL/e2e-tests" _unit_fixture_m1 0 || fixture_setup_ok=0
		_unit_mk_marker_row "$FIXFAIL/e2e-tests" _unit_fixture_mfail 1 || fixture_setup_ok=0
		_unit_mk_marker_row "$FIXFAIL/e2e-tests" _unit_fixture_m3 0 || fixture_setup_ok=0
		MFAIL="$FIXFAIL/markers"

		# FIXEMPTY has a valid tree but ZERO discoverable rows.
		_unit_mk_fixture_tree "$FIXEMPTY" || fixture_setup_ok=0

		if [ "$fixture_setup_ok" -ne 1 ]; then
			fail "QUM-947 fixture setup failed (mkdir/cp/row write) — assertions below are not meaningful"
		fi

		# --- 14a: three names in ONE invocation run all three rows ----------
		# This is the direct QUM-947 regression guard. Pre-fix it reported
		# "1/1 passed" with exit 0 after having run only m1.
		_unit_reset_markers "$MOK"
		_unit_run "$FIXOK" "$MOK" _unit_fixture_m1 _unit_fixture_m2 _unit_fixture_m3
		if [ "$_RC" -eq 0 ]; then
			pass "14a: 3-row invocation exits 0"
		else
			fail "14a: 3-row invocation rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_ran "$MOK" _unit_fixture_m1 yes "14a: row 1 of 3 executed"
		_unit_assert_ran "$MOK" _unit_fixture_m2 yes "14a: row 2 of 3 executed"
		_unit_assert_ran "$MOK" _unit_fixture_m3 yes "14a: row 3 of 3 executed"
		_unit_assert_summary "$_OUT" 3 3 "14a: summary denominator equals the 3 rows requested"
		_unit_assert_verdict_lines "$_OUT" 3 "14a: exactly 3 per-row verdict lines"

		# --- 14b: NEGATIVE CONTROL — one row fails => 2/3 and nonzero exit --
		# Proves the suite can tell 2/3 from 3/3, that a mid-list failure does
		# not abort the remainder, and that the failing row's body was actually
		# reached (its marker exists) rather than the row being rejected.
		_unit_reset_markers "$MFAIL"
		_unit_run "$FIXFAIL" "$MFAIL" _unit_fixture_m1 _unit_fixture_mfail _unit_fixture_m3
		if [ "$_RC" -eq 1 ]; then
			pass "14b: one failing row of three exits 1"
		else
			fail "14b: want rc=1, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_summary "$_OUT" 2 3 "14b: summary reports 2/3 (not 3/3, not 2/2)"
		_unit_assert_verdict_lines "$_OUT" 3 "14b: exactly 3 per-row verdict lines despite the failure"
		if printf '%s\n' "$_OUT" | grep -q '^FAIL _unit_fixture_mfail$'; then
			pass "14b: failing row named in output"
		else
			fail "14b: failing row not named out=$_OUT"
		fi
		_unit_assert_ran "$MFAIL" _unit_fixture_m1 yes "14b: row before the failing row executed"
		_unit_assert_ran "$MFAIL" _unit_fixture_mfail yes "14b: the failing row's body was reached"
		_unit_assert_ran "$MFAIL" _unit_fixture_m3 yes "14b: row after the failing row still executed"

		# --- 14c: single row still works (guards the pre-QUM-947 happy path) -
		_unit_reset_markers "$MOK"
		_unit_run "$FIXOK" "$MOK" _unit_fixture_m1
		if [ "$_RC" -eq 0 ]; then
			pass "14c: single row exits 0"
		else
			fail "14c: single row rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_summary "$_OUT" 1 1 "14c: single row reports 1/1"
		_unit_assert_verdict_lines "$_OUT" 1 "14c: exactly 1 per-row verdict line"
		_unit_assert_ran "$MOK" _unit_fixture_m1 yes "14c: the named row executed"
		_unit_assert_ran "$MOK" _unit_fixture_m2 no "14c: unnamed row m2 did not execute"
		_unit_assert_ran "$MOK" _unit_fixture_m3 no "14c: unnamed row m3 did not execute"

		# --- 14d: `all` runs every discovered row ---------------------------
		_unit_reset_markers "$MOK"
		_unit_run "$FIXOK" "$MOK" all
		if [ "$_RC" -eq 0 ]; then
			pass "14d: 'all' exits 0"
		else
			fail "14d: 'all' rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_summary "$_OUT" 3 3 "14d: 'all' reports 3/3"
		_unit_assert_verdict_lines "$_OUT" 3 "14d: 'all' emits 3 per-row verdict lines"
		_unit_assert_ran "$MOK" _unit_fixture_m1 yes "14d: 'all' executed m1"
		_unit_assert_ran "$MOK" _unit_fixture_m2 yes "14d: 'all' executed m2"
		_unit_assert_ran "$MOK" _unit_fixture_m3 yes "14d: 'all' executed m3"

		# --- 14e: no args behaves as `all` ----------------------------------
		_unit_reset_markers "$MOK"
		_unit_run "$FIXOK" "$MOK"
		if [ "$_RC" -eq 0 ]; then
			pass "14e: no args exits 0"
		else
			fail "14e: no args rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_summary "$_OUT" 3 3 "14e: no args reports 3/3"
		_unit_assert_ran "$MOK" _unit_fixture_m1 yes "14e: no args executed m1"
		_unit_assert_ran "$MOK" _unit_fixture_m2 yes "14e: no args executed m2"
		_unit_assert_ran "$MOK" _unit_fixture_m3 yes "14e: no args executed m3"

		# --- 14f: unknown name among valid ones => exit 2, NOTHING ran ------
		# The m1-did-not-run assertion is load-bearing: it proves ALL names are
		# validated BEFORE any row executes (fail fast), rather than the driver
		# dying partway through a multi-row run.
		_unit_reset_markers "$MOK"
		_unit_run "$FIXOK" "$MOK" _unit_fixture_m1 definitely-not-a-row _unit_fixture_m3
		if [ "$_RC" -eq 2 ]; then
			pass "14f: unknown row among valid ones exits 2"
		else
			fail "14f: want rc=2, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		if printf '%s\n' "$_ERR" | grep -q "unknown row 'definitely-not-a-row'"; then
			pass "14f: the unknown name is reported on stderr"
		else
			fail "14f: unknown name not on stderr err=$_ERR out=$_OUT"
		fi
		_unit_assert_no_summary "$_OUT" "14f: no summary line printed on the unknown-row path"
		_unit_assert_ran "$MOK" _unit_fixture_m1 no \
			"14f: fail-fast — valid row BEFORE the typo did not execute"
		_unit_assert_ran "$MOK" _unit_fixture_m3 no \
			"14f: fail-fast — valid row AFTER the typo did not execute"

		# --- 14g: `all` may not be combined with explicit names -------------
		# Checked in both argument orders and as `all all`, so a fix that only
		# special-cases $1 (the original bug's shape) cannot slip through.
		for _g in "all _unit_fixture_m1" "_unit_fixture_m1 all" "all all"; do
			_unit_reset_markers "$MOK"
			# shellcheck disable=SC2086
			_unit_run "$FIXOK" "$MOK" $_g
			if [ "$_RC" -eq 2 ]; then
				pass "14g: '$_g' exits 2"
			else
				fail "14g: '$_g' want rc=2, got rc=$_RC out=$_OUT err=$_ERR"
			fi
			if [ -n "$_ERR" ]; then
				pass "14g: '$_g' explains itself on stderr"
			else
				fail "14g: '$_g' wrote nothing to stderr"
			fi
			_unit_assert_ran "$MOK" _unit_fixture_m1 no "14g: '$_g' ran no rows"
		done

		# --- 14h: duplicates run twice; denominator always matches argv -----
		# Deduplicating would shrink the denominator below what was requested,
		# which is precisely the QUM-947 bug class.
		_unit_reset_markers "$MOK"
		_unit_run "$FIXOK" "$MOK" _unit_fixture_m1 _unit_fixture_m1
		ran_lines=$(printf '%s\n' "$_OUT" | grep -c '^RAN _unit_fixture_m1$')
		if [ "$_RC" -eq 0 ] && [ "$ran_lines" -eq 2 ]; then
			pass "14h: duplicate name executes the row twice"
		else
			fail "14h: rc=$_RC ran_lines=$ran_lines (want rc=0, 2) out=$_OUT"
		fi
		_unit_assert_summary "$_OUT" 2 2 "14h: duplicate name reports 2/2, not 1/1"
		_unit_assert_verdict_lines "$_OUT" 2 "14h: 2 verdict lines for 2 requested rows"

		# 4 args including a duplicate — pins that `requested` comes from argv
		# length and not from some post-validation deduplicated array.
		_unit_reset_markers "$MOK"
		_unit_run "$FIXOK" "$MOK" _unit_fixture_m1 _unit_fixture_m2 _unit_fixture_m1 _unit_fixture_m3
		_unit_assert_summary "$_OUT" 4 4 "14h: 4 args with a duplicate reports 4/4"
		_unit_assert_verdict_lines "$_OUT" 4 "14h: 4 verdict lines for 4 requested rows"

		# --- 14i: --list unchanged, and rejects extra args before listing ---
		_unit_run "$FIXOK" "" --list
		list_lines=$(printf '%s\n' "$_OUT" | grep -c '^_unit_fixture_m[123]$')
		if [ "$_RC" -eq 0 ] && [ "$list_lines" -eq 3 ]; then
			pass "14i: --list exits 0 and lists all three fixture rows"
		else
			fail "14i: --list rc=$_RC list_lines=$list_lines (want 0, 3) out=$_OUT"
		fi
		for _l in "--list _unit_fixture_m1" "_unit_fixture_m1 --list" "--list --list"; do
			_unit_reset_markers "$MOK"
			# shellcheck disable=SC2086
			_unit_run "$FIXOK" "$MOK" $_l
			if [ "$_RC" -eq 2 ]; then
				pass "14i: '$_l' exits 2 rather than discarding an argument"
			else
				fail "14i: '$_l' want rc=2, got rc=$_RC out=$_OUT err=$_ERR"
			fi
			# Must be rejected BEFORE doing its work — same fail-fast rule.
			if printf '%s\n' "$_OUT" | grep -q '^_unit_fixture_m1$'; then
				fail "14i: '$_l' printed the row list before erroring"
			else
				pass "14i: '$_l' printed no list before erroring"
			fi
			_unit_assert_ran "$MOK" _unit_fixture_m1 no "14i: '$_l' ran no rows"
		done

		# --- 14j: row names are validated, not merely tested for readability -
		# `-r "$ROWS_DIR/$name.sh"` alone is satisfied by `../lib/e2e-common`,
		# which would make the driver source and partially execute the shared
		# library as if it were a row. Names must be plain basenames.
		for _bad in "../lib/e2e-common" "foo/bar" "" "." ".."; do
			_unit_reset_markers "$MOK"
			_unit_run "$FIXOK" "$MOK" "$_bad"
			if [ "$_RC" -eq 2 ]; then
				pass "14j: rejects row name '$_bad' with exit 2"
			else
				fail "14j: row name '$_bad' want rc=2, got rc=$_RC out=$_OUT err=$_ERR"
			fi
			_unit_assert_no_summary "$_OUT" "14j: no summary printed for row name '$_bad'"
		done
		# ...and an invalid name alongside valid ones still runs nothing.
		_unit_reset_markers "$MOK"
		_unit_run "$FIXOK" "$MOK" _unit_fixture_m1 ../lib/e2e-common
		if [ "$_RC" -eq 2 ]; then
			pass "14j: traversal name among valid ones exits 2"
		else
			fail "14j: traversal among valid names rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_ran "$MOK" _unit_fixture_m1 no "14j: fail-fast — no rows ran"

		# --- 14k: zero discoverable rows must NOT report a green 0/0 --------
		# "0/0 passed, exit 0" is the same false-green shape as QUM-947: the
		# harness claiming success for work it did not do.
		for _z in "all" ""; do
			# shellcheck disable=SC2086
			_unit_run "$FIXEMPTY" "" $_z
			_desc="empty rows dir with '${_z:-<no args>}'"
			if [ "$_RC" -ne 0 ]; then
				pass "14k: $_desc exits nonzero"
			else
				fail "14k: $_desc exited 0 out=$_OUT err=$_ERR"
			fi
			_unit_assert_no_summary "$_OUT" "14k: $_desc prints no 'passed' summary"
			if [ -n "$_ERR" ]; then
				pass "14k: $_desc explains itself on stderr"
			else
				fail "14k: $_desc wrote nothing to stderr"
			fi
		done

		# Teardown. Each path is asserted to live under the mktemp root before
		# removal, so an unset/garbled variable can never name a parent dir.
		for _d in "$FIXOK" "$FIXFAIL" "$FIXEMPTY"; do
			case "$_d" in
				"$UNIT_TMP_ROOT"/*) rm -rf "$_d" ;;
				*) note "refusing to remove fixture dir '$_d' (not under $UNIT_TMP_ROOT/)" ;;
			esac
		done
	fi
fi

# ----------------------------------------------------------------------------
# 15. QUM-952: a SKIPPED row must never be indistinguishable from a passed one.
#
#     Pre-fix, `e2e_require_claude_or_skip` did `exit 0`, so a row that asserted
#     NOTHING landed in pass_count: `SPRAWL_E2E_SKIP_NO_CLAUDE=1` with no claude
#     on PATH printed a fully green `Matrix: 33/33 passed` and exited 0. CLAUDE.md
#     *instructs* agents to set that variable, so the mandatory-gate harness was
#     permanently green while validating nothing.
#
#     Contract asserted here:
#       * skip is signalled by rc 77 AND a non-empty $E2E_SKIP_FILE sentinel
#         (double-keyed: a row that merely exits 77 cannot forge a skip);
#       * passed / failed / skipped are three separate buckets that sum to
#         `requested` (the QUM-947 denominator);
#       * any skip forces a nonzero exit (3 when there are no outright failures,
#         1 when there are) — a mandatory gate that ran nothing is not satisfied;
#       * the skip is loud: a `SKIP <row>` verdict line plus a stderr banner.
#
#     Every positive assertion below is paired with a negative control, because a
#     suite that only ever observes the fixed behavior cannot detect a harness
#     that always reports success.
# ----------------------------------------------------------------------------
echo "[15] QUM-952 skip accounting: skipped is not passed"

# Write a fixture row with explicit metadata and an explicit body. $1=e2e-tests
# dir, $2=row name, $3=test_metadata output, $4=body of test_run (raw shell).
# Bodies must use bash builtins only: several runs below scrub PATH.
_unit_mk_row() {
	cat >"$1/$2.sh" <<EOF
test_metadata() { echo "$3"; }
test_run() {
$4
}
EOF
}

# Marker-touching body for row $1, used by the fixtures below.
_unit_marker_body() {
	printf '\t: >"${UNIT_MARKER_DIR:?UNIT_MARKER_DIR unset}/%s"\n\techo "RAN %s"\n' "$1" "$1"
}

# Assert the exact breakdown line. $1=out $2=passed $3=failed $4=skipped
# $5=requested $6=desc. The literal is the contract: it is the only place
# skip_count surfaces, so coupling to it is deliberate.
_unit_assert_breakdown() {
	local want="=== Matrix breakdown: $2 passed, $3 failed, $4 skipped / $5 requested ==="
	if printf '%s\n' "$1" | grep -qF "$want"; then
		pass "$6"
	else
		fail "$6 (want '$want') out=$1"
	fi
}

# _unit_assert_no_summary greps only for the canonical line; the breakdown line
# would slip past it. rc-2 and rc-4 paths must print neither.
_unit_assert_no_breakdown() {
	if printf '%s\n' "$1" | grep -q 'Matrix breakdown'; then
		fail "$2 (a breakdown line was printed) out=$1"
	else
		pass "$2"
	fi
}

# Count verdict lines INCLUDING skips. Deliberately a separate helper:
# relaxing _unit_assert_verdict_lines to accept SKIP would weaken the QUM-947
# assertions in 14a/14d, which would then be satisfied by three skips.
_unit_assert_verdict_lines_any() {
	local n
	n=$(printf '%s\n' "$1" | grep -cE '^(PASS|FAIL|SKIP) [A-Za-z0-9_-]+$')
	if [ "$n" -eq "$2" ]; then
		pass "$3"
	else
		fail "$3 (want $2 verdict lines, got $n) out=$1"
	fi
}

# Count per-row SKIP verdict lines. Anchored to a whole row name so the lib's
# own `SKIP: <reason>` diagnostic can never be miscounted as a verdict.
_unit_assert_skip_lines() {
	local n
	n=$(printf '%s\n' "$1" | grep -cE '^SKIP [A-Za-z0-9_-]+$')
	if [ "$n" -eq "$2" ]; then
		pass "$3"
	else
		fail "$3 (want $2 SKIP verdict lines, got $n) out=$1"
	fi
}

# The banner is the "loud" half of the requirement: present on stderr, naming
# the row, and stating that a skip asserts nothing. The row name must appear on
# a banner line WITH its reason — matching the name anywhere in stderr would
# also be satisfied by the `=== Matrix: skipped rows:` line, which proves
# nothing about the banner.
_unit_assert_skip_banner() {
	if printf '%s\n' "$1" | grep -q 'SKIPPED' &&
		printf '%s\n' "$1" | grep -qE "^!!! +$2: .+"; then
		pass "$3"
	else
		fail "$3 (no banner line '!!!   $2: <reason>') err=$1"
	fi
}

_unit_assert_no_skip_banner() {
	if printf '%s\n' "$1" | grep -q 'SKIPPED'; then
		fail "$2 (a SKIPPED banner was printed) text=$1"
	else
		pass "$2"
	fi
}

if [ ! -r "$LIB" ] || [ ! -r "$DRIVER" ]; then
	fail "QUM-952 skip-accounting tests skipped (lib or driver missing)"
else
	FIXSKIP=$(mktemp -d 2>/dev/null)
	if [ -z "$FIXSKIP" ] || [ ! -d "$FIXSKIP" ]; then
		fail "could not mktemp QUM-952 fixture dir"
	else
		skip_setup_ok=1
		_unit_mk_fixture_tree "$FIXSKIP" || skip_setup_ok=0
		RD="$FIXSKIP/e2e-tests"
		MSK="$FIXSKIP/markers"

		# needs_claude=1: the driver's preflight must skip it before test_run.
		_unit_mk_row "$RD" _unit_fixture_skip "needs_claude=1" \
			"$(_unit_marker_body _unit_fixture_skip)
	echo \"SHOULD NOT RUN\"
	return 1" || skip_setup_ok=0
		_unit_mk_marker_row "$RD" _unit_fixture_m1 0 || skip_setup_ok=0
		_unit_mk_marker_row "$RD" _unit_fixture_m2 0 || skip_setup_ok=0
		_unit_mk_marker_row "$RD" _unit_fixture_fail 1 || skip_setup_ok=0
		# Exits 77 without writing the sentinel: a crash must not forge a skip.
		_unit_mk_marker_row "$RD" _unit_fixture_bare77 77 || skip_setup_ok=0
		# Writes the sentinel but returns 0 — unreachable in correct code, so it
		# must be an internal error, never a PASS.
		_unit_mk_row "$RD" _unit_fixture_sneak "" \
			"$(_unit_marker_body _unit_fixture_sneak)
	printf 'sneaky\n' >\"\${E2E_SKIP_FILE:?}\"
	return 0" || skip_setup_ok=0
		# Writes the sentinel and returns 1 — rc must win over the sentinel.
		_unit_mk_row "$RD" _unit_fixture_skipfail "" \
			"$(_unit_marker_body _unit_fixture_skipfail)
	printf 'claimed-skip\n' >\"\${E2E_SKIP_FILE:?}\"
	return 1" || skip_setup_ok=0
		# Records the sentinel path the driver exported, to pin the contract.
		_unit_mk_row "$RD" _unit_fixture_probe "" \
			"	printf '%s\n' \"\${E2E_SKIP_FILE:?}\" >\"\${UNIT_MARKER_DIR:?}/_unit_fixture_probe\"
	return 0" || skip_setup_ok=0
		# needs_claude=1 but PASSES — used with a claude stub on PATH to prove the
		# preflight lets a row through when claude is present.
		_unit_mk_row "$RD" _unit_fixture_needsclaude_ok "needs_claude=1" \
			"$(_unit_marker_body _unit_fixture_needsclaude_ok)
	return 0" || skip_setup_ok=0
		# Calls the skip protocol DIRECTLY, independent of claude absence: this is
		# the hermetic pin on e2e_skip_row's own contract (and on a row's ability
		# to declare a mid-body skip through the sanctioned helper).
		_unit_mk_row "$RD" _unit_fixture_libskip "" \
			"	e2e_skip_row \"fixture-declared-reason\"
	$(_unit_marker_body _unit_fixture_libskip)
	return 0" || skip_setup_ok=0

		# Declares a skip and then makes the sentinel un-truncatable, so the NEXT
		# row's classification would run on stale content. Uses chmod (real PATH
		# on this case) — the driver must refuse rather than trust the file.
		# Writes a sentinel that satisfies `[ -s ]` but carries no reason.
		_unit_mk_row "$RD" _unit_fixture_blankskip "" \
			"	printf '\n   \n' >\"\${E2E_SKIP_FILE:?}\"
	return 77" || skip_setup_ok=0
		# ...and one whose reason is not on the first line.
		_unit_mk_row "$RD" _unit_fixture_lateskip "" \
			"	printf '\nlate-reason\n' >\"\${E2E_SKIP_FILE:?}\"
	return 77" || skip_setup_ok=0

		# (e2e_skip_row cannot be used here: it exits immediately, so the chmod
		# would never run. This writes the same sentinel shape by hand.)
		_unit_mk_row "$RD" _unit_fixture_lockskip "" \
			"	printf 'stale-reason\n' >\"\${E2E_SKIP_FILE:?}\"
	chmod 0444 \"\${E2E_SKIP_FILE:?}\"
	return 77" || skip_setup_ok=0
		# Un-truncatable but EMPTY: isolates the un-writable fault from the
		# stale-content one, which lockskip above triggers simultaneously.
		_unit_mk_row "$RD" _unit_fixture_lockempty "" \
			"	chmod 0444 \"\${E2E_SKIP_FILE:?}\"
	return 0" || skip_setup_ok=0

		# A claude stub on a private PATH: hermetic "claude is present" without
		# depending on the host, and without letting a real claude be invoked.
		STUBBIN="$FIXSKIP/stubbin"
		mkdir -p "$STUBBIN" && printf '#!/bin/sh\nexit 0\n' >"$STUBBIN/claude" &&
			chmod +x "$STUBBIN/claude" || skip_setup_ok=0

		# Separate tree for `all`-mode: exactly one skipping row + one passing row,
		# so `all` (whose `requested` comes from discover_rows, a different code
		# path) is exactly assertable as 1/2.
		FIXALL=$(mktemp -d 2>/dev/null)
		if [ -z "$FIXALL" ] || [ ! -d "$FIXALL" ]; then
			skip_setup_ok=0
		else
			_unit_mk_fixture_tree "$FIXALL" || skip_setup_ok=0
			_unit_mk_row "$FIXALL/e2e-tests" _unit_fixture_askip "needs_claude=1" \
				"	echo \"SHOULD NOT RUN\"
	return 1" || skip_setup_ok=0
			_unit_mk_marker_row "$FIXALL/e2e-tests" _unit_fixture_aok 0 || skip_setup_ok=0
		fi

		if [ "$skip_setup_ok" -ne 1 ]; then
			fail "QUM-952 fixture setup failed — assertions below are not meaningful"
		fi

		NOCLAUDE="PATH=/nonexistent SPRAWL_E2E_SKIP_NO_CLAUDE=1"

		# --- 15a: the headline bug --------------------------------------------
		# Pre-fix: `PASS _unit_fixture_skip`, `Matrix: 1/1 passed`, exit 0.
		_unit_reset_markers "$MSK"
		_unit_run_env "$FIXSKIP" "$MSK" "$NOCLAUDE" _unit_fixture_skip
		if [ "$_RC" -eq 3 ]; then
			pass "15a: wholly-skipped run exits 3 (nonzero)"
		else
			fail "15a: want rc=3, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_summary "$_OUT" 0 1 "15a: canonical summary reports 0/1, not 1/1"
		_unit_assert_breakdown "$_OUT" 0 0 1 1 "15a: breakdown reports the skip"
		_unit_assert_skip_lines "$_OUT" 1 "15a: exactly one SKIP verdict line"
		_unit_assert_skip_banner "$_ERR" _unit_fixture_skip "15a: stderr banner names the skipped row"
		if printf '%s\n' "$_ERR" | grep -qi 'asserts nothing'; then
			pass "15a: banner states that a skip asserts nothing"
		else
			fail "15a: banner lacks the 'asserts nothing' statement err=$_ERR"
		fi
		# The gate keys on claude being ABSENT and never probes auth, so an
		# installed-but-unauthenticated claude does not skip — it runs and fails
		# with "Not logged in", which reads as a product regression. The banner is
		# where a reader lands, so it must say the flag is not that remedy and
		# that hiding claude from PATH to force a skip is not a substitute.
		if printf '%s\n' "$_ERR" | grep -qi 'unauthenticated' &&
			printf '%s\n' "$_ERR" | grep -q 'PATH'; then
			pass "15a: banner warns the flag does not cover an unauthenticated claude"
		else
			fail "15a: banner lacks the unauthenticated-claude caveat err=$_ERR"
		fi
		if printf '%s\n' "$_OUT" | grep -q '^PASS '; then
			fail "15a: a PASS verdict line was printed for a skipped row out=$_OUT"
		else
			pass "15a: no PASS verdict line for a skipped row"
		fi
		_unit_assert_ran "$MSK" _unit_fixture_skip no "15a: the skipped row's body never ran"

		# --- 15b: NEGATIVE CONTROL — skip bucket keys on the operator ack -----
		# Same fixture, same missing claude, but no SPRAWL_E2E_SKIP_NO_CLAUDE:
		# must be a FAIL, not a skip. Proves the suite can tell the two apart.
		_unit_reset_markers "$MSK"
		_unit_run_env "$FIXSKIP" "$MSK" "PATH=/nonexistent" _unit_fixture_skip
		if [ "$_RC" -eq 1 ]; then
			pass "15b: without the skip env var the row FAILs (rc 1)"
		else
			fail "15b: want rc=1, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_breakdown "$_OUT" 0 1 0 1 "15b: breakdown counts it as failed, not skipped"
		_unit_assert_no_skip_banner "$_ERR" "15b: no skip banner on the FATAL path"

		# --- 15c: composes with the QUM-947 denominator contract --------------
		_unit_reset_markers "$MSK"
		_unit_run_env "$FIXSKIP" "$MSK" "$NOCLAUDE" \
			_unit_fixture_m1 _unit_fixture_skip _unit_fixture_m2
		if [ "$_RC" -eq 3 ]; then
			pass "15c: partial skip exits 3"
		else
			fail "15c: want rc=3, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_summary "$_OUT" 2 3 "15c: canonical summary reports 2/3 (skip not counted as pass)"
		_unit_assert_breakdown "$_OUT" 2 0 1 3 "15c: breakdown sums to the 3 requested rows"
		_unit_assert_verdict_lines_any "$_OUT" 3 "15c: one verdict line per requested row"
		_unit_assert_skip_lines "$_OUT" 1 "15c: exactly one of the three is a SKIP"
		# The canonical summary must stay uniquely identifiable. Several lines
		# share the `=== Matrix: ` prefix (the QUM-947 selection banner, the
		# failed-rows and skipped-rows lines), so that prefix alone is NOT a
		# usable anchor — the full `N/M passed ===` shape is, and there must be
		# exactly one of it per run. Guards against a future line colliding.
		canon=$(printf '%s\n' "$_OUT" | grep -cE '^=== Matrix: [0-9]+/[0-9]+ passed ===$')
		if [ "$canon" -eq 1 ]; then
			pass "15c: exactly one canonical 'N/M passed' summary line per run"
		else
			fail "15c: want 1 canonical summary line, got $canon out=$_OUT"
		fi
		_unit_assert_ran "$MSK" _unit_fixture_m1 yes "15c: row before the skip executed"
		_unit_assert_ran "$MSK" _unit_fixture_m2 yes "15c: row after the skip still executed"
		_unit_assert_ran "$MSK" _unit_fixture_skip no "15c: the skipped row's body never ran"

		# --- 15d: failure dominates a skip in the exit code -------------------
		_unit_reset_markers "$MSK"
		_unit_run_env "$FIXSKIP" "$MSK" "$NOCLAUDE" _unit_fixture_fail _unit_fixture_skip
		if [ "$_RC" -eq 1 ]; then
			pass "15d: a failure alongside a skip exits 1, not 3"
		else
			fail "15d: want rc=1, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_breakdown "$_OUT" 0 1 1 2 "15d: breakdown reports 1 failed + 1 skipped of 2"
		if printf '%s\n' "$_ERR" | grep -q 'failed rows:'; then
			pass "15d: failed-rows line still printed"
		else
			fail "15d: failed-rows line missing err=$_ERR"
		fi
		_unit_assert_skip_banner "$_ERR" _unit_fixture_skip "15d: skip banner printed alongside the failure"

		# --- 15e: NEGATIVE CONTROL — bare rc 77 cannot forge a skip -----------
		# Without this, "rc 77 => skip" lets any row that happens to exit 77
		# launder itself into the skip bucket.
		_unit_reset_markers "$MSK"
		_unit_run "$FIXSKIP" "$MSK" _unit_fixture_bare77
		if [ "$_RC" -eq 1 ]; then
			pass "15e: rc 77 without a sentinel is a FAIL (rc 1)"
		else
			fail "15e: want rc=1, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_breakdown "$_OUT" 0 1 0 1 "15e: bare rc 77 counts as failed, skip count 0"
		_unit_assert_no_skip_banner "$_ERR" "15e: no skip banner for bare rc 77"

		# --- 15f: NEGATIVE CONTROL — sentinel written but rc 0 ----------------
		# Unreachable in correct code, hence the ideal loud assertion: it is
		# QUM-952 one level up ("the row thought it skipped; driver said pass").
		_unit_reset_markers "$MSK"
		_unit_run "$FIXSKIP" "$MSK" _unit_fixture_sneak
		if [ "$_RC" -eq 4 ]; then
			pass "15f: sentinel-with-rc-0 is an internal error (rc 4)"
		else
			fail "15f: want rc=4, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		if printf '%s\n' "$_OUT" | grep -q '^PASS _unit_fixture_sneak$'; then
			fail "15f: sentinel-with-rc-0 was reported as PASS out=$_OUT"
		else
			pass "15f: sentinel-with-rc-0 is not reported as PASS"
		fi
		# Grep the specific wording: `[ -n "$_ERR" ]` would be satisfied by the
		# fixture's own noise or by the ordinary failed-rows line.
		if printf '%s\n' "$_ERR" | grep -qi 'internal error'; then
			pass "15f: internal inconsistency named as an internal error on stderr"
		else
			fail "15f: no 'internal error' on stderr err=$_ERR"
		fi
		_unit_assert_no_summary "$_OUT" "15f: no canonical summary on the internal-error path"
		_unit_assert_no_breakdown "$_OUT" "15f: no breakdown line on the internal-error path"

		# --- 15g: NEGATIVE CONTROL — rc wins over the sentinel ----------------
		_unit_reset_markers "$MSK"
		_unit_run "$FIXSKIP" "$MSK" _unit_fixture_skipfail
		if [ "$_RC" -eq 1 ]; then
			pass "15g: a failing row that writes the sentinel still FAILs"
		else
			fail "15g: want rc=1, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_breakdown "$_OUT" 0 1 0 1 "15g: sentinel+rc1 counts as failed, skip count 0"

		# --- 15h: the sentinel must be reset PER ROW --------------------------
		# A skip followed by a bare-77 row: if the driver does not truncate, the
		# stale sentinel launders the second row's failure into a skip. Invisible
		# in single-row testing, which is why it gets its own case.
		_unit_reset_markers "$MSK"
		_unit_run_env "$FIXSKIP" "$MSK" "$NOCLAUDE" _unit_fixture_skip _unit_fixture_bare77
		_unit_assert_breakdown "$_OUT" 0 1 1 2 \
			"15h: stale sentinel does not launder the following row into a skip"
		if printf '%s\n' "$_OUT" | grep -q '^FAIL _unit_fixture_bare77$'; then
			pass "15h: the bare-77 row after a skip is reported FAIL"
		else
			fail "15h: bare-77 row after a skip not reported FAIL out=$_OUT"
		fi
		if [ "$_RC" -eq 1 ]; then
			pass "15h: failure after a skip still dominates the exit code"
		else
			fail "15h: want rc=1, got rc=$_RC out=$_OUT err=$_ERR"
		fi

		# --- 15i: the driver exports a usable sentinel path -------------------
		_unit_reset_markers "$MSK"
		_unit_run "$FIXSKIP" "$MSK" _unit_fixture_probe
		probe_path=$(cat "$MSK/_unit_fixture_probe" 2>/dev/null)
		# Asserted against the fixture-scoped TMPDIR, not a bare "/tmp/*": the
		# latter is the driver's default and so could not fail. Honoring TMPDIR
		# is what lets a caller confine the sentinel to a directory it reaps.
		case "$probe_path" in
			"$FIXSKIP"/*)
				pass "15i: driver puts E2E_SKIP_FILE under the caller's TMPDIR"
				;;
			*)
				fail "15i: E2E_SKIP_FILE outside the caller's TMPDIR: '$probe_path' rc=$_RC err=$_ERR"
				;;
		esac
		# The sentinel is driver-owned scratch: it must not survive the run.
		# An empty probe_path is an explicit failure, not a free pass — otherwise
		# this assertion would report success precisely when the probe broke.
		if [ -z "$probe_path" ]; then
			fail "15i: no sentinel path recorded, so removal is unverifiable"
		elif [ -e "$probe_path" ]; then
			fail "15i: sentinel $probe_path leaked after the driver exited"
		else
			pass "15i: sentinel removed when the driver exits"
		fi

		# --- 15j: the pass+fail+skip == requested identity is enforced --------
		# Exercised through a debug seam, because an invariant with no way to
		# violate it is decorative rather than tested.
		_unit_reset_markers "$MSK"
		_unit_run_env "$FIXSKIP" "$MSK" "SPRAWL_E2E_MATRIX_DEBUG_TALLY_SKEW=1" _unit_fixture_m1
		if [ "$_RC" -eq 4 ]; then
			pass "15j: a skewed tally is an internal error (rc 4)"
		else
			fail "15j: want rc=4, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_no_summary "$_OUT" "15j: no green summary line printed on a skewed tally"
		_unit_assert_no_breakdown "$_OUT" "15j: no breakdown line printed on a skewed tally"
		if printf '%s\n' "$_ERR" | grep -qi 'internal error'; then
			pass "15j: skewed tally reported as an internal error on stderr"
		else
			fail "15j: skewed tally not reported as an internal error err=$_ERR"
		fi
		# The seam must skew the tally AFTER the loop, not short-circuit before it:
		# otherwise rc 4 could be produced without the invariant ever being
		# evaluated, and the check would be decorative.
		if printf '%s\n' "$_OUT" | grep -q '^PASS _unit_fixture_m1$'; then
			pass "15j: the row still ran — the invariant is checked post-loop"
		else
			fail "15j: no per-row verdict, so the seam short-circuited the run out=$_OUT"
		fi
		_unit_assert_ran "$MSK" _unit_fixture_m1 yes "15j: the row's body still executed under the seam"
		# NEGATIVE CONTROL for the seam itself: without the env var, same
		# invocation is a clean pass. Proves 15j's rc 4 came from the skew.
		_unit_reset_markers "$MSK"
		_unit_run "$FIXSKIP" "$MSK" _unit_fixture_m1
		if [ "$_RC" -eq 0 ]; then
			pass "15j: without the debug seam the same run is a clean pass"
		else
			fail "15j: control run rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_breakdown "$_OUT" 1 0 0 1 "15j: control run breakdown is 1 passed of 1"

		# --- 15k: arg-error paths print neither summary nor breakdown ----------
		_unit_reset_markers "$MSK"
		_unit_run "$FIXSKIP" "$MSK" definitely-not-a-row
		if [ "$_RC" -eq 2 ]; then
			pass "15k: unknown row still exits 2 (arg errors outrank skip accounting)"
		else
			fail "15k: want rc=2, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_no_summary "$_OUT" "15k: no canonical summary on the arg-error path"
		_unit_assert_no_breakdown "$_OUT" "15k: no breakdown line on the arg-error path"

		# --- 15l: EVERY skipped row is named, not just the first or last -------
		_unit_reset_markers "$MSK"
		_unit_run_env "$FIXSKIP" "$MSK" "$NOCLAUDE" _unit_fixture_skip _unit_fixture_libskip
		if [ "$_RC" -eq 3 ]; then
			pass "15l: two skipped rows exit 3"
		else
			fail "15l: want rc=3, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_summary "$_OUT" 0 2 "15l: canonical summary reports 0/2"
		_unit_assert_breakdown "$_OUT" 0 0 2 2 "15l: breakdown reports 2 skipped of 2"
		_unit_assert_skip_lines "$_OUT" 2 "15l: two SKIP verdict lines"
		_unit_assert_skip_banner "$_ERR" _unit_fixture_skip "15l: banner names the first skipped row"
		_unit_assert_skip_banner "$_ERR" _unit_fixture_libskip "15l: banner names the second skipped row"

		# The same row twice: skips must not be deduplicated either, or the
		# denominator shrinks below the request (the QUM-947 bug class).
		_unit_reset_markers "$MSK"
		_unit_run_env "$FIXSKIP" "$MSK" "$NOCLAUDE" _unit_fixture_skip _unit_fixture_skip
		if [ "$_RC" -eq 3 ]; then
			pass "15l: a duplicated skipping row exits 3"
		else
			fail "15l: duplicate skip want rc=3, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_summary "$_OUT" 0 2 "15l: duplicated skip reports 0/2, not 0/1"
		_unit_assert_breakdown "$_OUT" 0 0 2 2 "15l: duplicated skip counts twice"
		_unit_assert_skip_lines "$_OUT" 2 "15l: two SKIP verdict lines for the duplicated row"

		# --- 15m: e2e_skip_row is the sanctioned protocol, usable directly -----
		# No PATH scrub: this pins the skip protocol itself, decoupled from the
		# claude-absence simulation, and proves a row body can declare its own
		# skip. The reason string must reach the operator.
		_unit_reset_markers "$MSK"
		_unit_run "$FIXSKIP" "$MSK" _unit_fixture_libskip
		if [ "$_RC" -eq 3 ]; then
			pass "15m: a row calling e2e_skip_row is skipped (rc 3)"
		else
			fail "15m: want rc=3, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_breakdown "$_OUT" 0 0 1 1 "15m: e2e_skip_row lands in the skip bucket"
		_unit_assert_ran "$MSK" _unit_fixture_libskip no "15m: e2e_skip_row aborts the row body"
		# Row name AND reason on the SAME stderr line: a lib-level `echo >&2` from
		# the row subshell would satisfy a bare reason grep without the driver's
		# banner ever attributing the reason to a row.
		if printf '%s\n' "$_ERR" | grep -q '_unit_fixture_libskip.*fixture-declared-reason'; then
			pass "15m: the banner attributes the reason to the skipped row on one line"
		else
			fail "15m: no single stderr line carries both row and reason err=$_ERR"
		fi

		# --- 15n: claude PRESENT must be a PASS, never a skip ------------------
		# THIRD LEG OF THE TRUTH TABLE. Without it, an implementation that always
		# skips satisfies every other assertion here — a strictly worse bug than
		# the one being fixed, and one this suite must be able to see.
		#
		# CLAUDE_CODE_OAUTH_TOKEN=stub-token: QUM-974 centralized an auth-recovery
		# check in run_row right after the claude-presence gate this section
		# tests, and PATH here is scrubbed to ONLY $STUBBIN (no awk/tr/cut), so
		# the /proc ancestor walk that check would otherwise attempt cannot run
		# at all. Pre-setting the token hits e2e_recover_oauth_token's own fast
		# path (already-set, no walk needed), keeping this section's fixture
		# decoupled from that unrelated precondition.
		_unit_reset_markers "$MSK"
		_unit_run_env "$FIXSKIP" "$MSK" "PATH=$STUBBIN CLAUDE_CODE_OAUTH_TOKEN=stub-token" _unit_fixture_needsclaude_ok
		if [ "$_RC" -eq 0 ]; then
			pass "15n: needs_claude row with claude present exits 0"
		else
			fail "15n: want rc=0, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_breakdown "$_OUT" 1 0 0 1 "15n: breakdown reports 1 passed, 0 skipped"
		_unit_assert_skip_lines "$_OUT" 0 "15n: no SKIP verdict line when claude is present"
		_unit_assert_no_skip_banner "$_ERR" "15n: no skip banner when claude is present"
		_unit_assert_ran "$MSK" _unit_fixture_needsclaude_ok yes "15n: the row's body actually ran"

		# --- 15o: `all` mode accounts skips too --------------------------------
		# On the `all` branch `requested` comes from discover_rows, a different
		# code path feeding the same sum invariant.
		MALL="$FIXALL/markers"
		_unit_reset_markers "$MALL"
		_unit_run_env "$FIXALL" "$MALL" "$NOCLAUDE" all
		if [ "$_RC" -eq 3 ]; then
			pass "15o: 'all' with one skipping row exits 3"
		else
			fail "15o: want rc=3, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_summary "$_OUT" 1 2 "15o: 'all' reports 1/2 passed"
		_unit_assert_breakdown "$_OUT" 1 0 1 2 "15o: 'all' breakdown sums to the 2 discovered rows"
		_unit_assert_skip_lines "$_OUT" 1 "15o: 'all' emits one SKIP verdict line"
		_unit_assert_skip_banner "$_ERR" _unit_fixture_askip "15o: 'all' banner names the skipped row"
		_unit_assert_ran "$MALL" _unit_fixture_aok yes "15o: 'all' still ran the non-skipping row"
		if printf '%s\n' "$_OUT" | grep -q 'SHOULD NOT RUN'; then
			fail "15o: 'all' executed the skipped row's body out=$_OUT"
		else
			pass "15o: 'all' did not execute the skipped row's body"
		fi

		# --- 15r: a sentinel with no readable REASON is no corroboration --------
		# `[ -s ]` is a byte-count test, so a bare newline satisfies it. A skip
		# whose reason is blank (or a synthesized "unspecified") is a row nobody
		# can triage — an entry that looks accounted-for and isn't, which is a
		# small instance of exactly what QUM-952 exists to prevent. The rule is
		# therefore about corroboration, not bytes: rc 77 is the row's *claim*,
		# and a sentinel carrying no reason corroborates nothing, so it is
		# treated identically to an absent sentinel — FAIL.
		_unit_reset_markers "$MSK"
		_unit_run "$FIXSKIP" "$MSK" _unit_fixture_blankskip
		if [ "$_RC" -eq 1 ]; then
			pass "15r: rc 77 with a reason-less sentinel FAILs (rc 1)"
		else
			fail "15r: want rc=1, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_breakdown "$_OUT" 0 1 0 1 "15r: reason-less sentinel is not counted as a skip"
		if printf '%s\n' "$_OUT" | grep -q '^SKIP '; then
			fail "15r: a reason-less sentinel produced a SKIP verdict out=$_OUT"
		else
			pass "15r: no SKIP verdict for a reason-less sentinel"
		fi
		if printf '%s\n' "$_ERR" | grep -qi 'unspecified'; then
			fail "15r: a placeholder reason was synthesized instead of failing err=$_ERR"
		else
			pass "15r: no placeholder reason invented"
		fi
		_unit_assert_no_skip_banner "$_ERR" "15r: no skip banner for a reason-less sentinel"

		# Control for 15r: a blank FIRST line followed by a real reason is still a
		# genuine skip. Pins that the discriminator is "has a readable reason",
		# not "byte 0 is not a newline" — and that the fix did not simply move the
		# brittleness from `[ -s ]` onto the first line.
		_unit_reset_markers "$MSK"
		_unit_run "$FIXSKIP" "$MSK" _unit_fixture_lateskip
		if [ "$_RC" -eq 3 ]; then
			pass "15r: a reason on a later sentinel line is still a skip (rc 3)"
		else
			fail "15r: want rc=3, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_breakdown "$_OUT" 0 0 1 1 "15r: later-line reason lands in the skip bucket"
		_unit_assert_skip_banner "$_ERR" _unit_fixture_lateskip "15r: banner carries the later-line reason"
		if printf '%s\n' "$_ERR" | grep -qF 'late-reason'; then
			pass "15r: the actual reason text is reported"
		else
			fail "15r: reason text missing err=$_ERR"
		fi

		# --- 15q: a sentinel that cannot be RESET must fail fast, not launder ---
		# The per-row truncation is what stops a stale sentinel from turning the
		# next row's genuine crash into a skip. If that truncation fails and the
		# driver carries on, the stale reason is reused and a real failure is
		# reported as a skip (exit 3 instead of 1) — the QUM-952 bug wearing a
		# different hat. The driver already refuses to run when it cannot CREATE
		# the sentinel; being unable to RESET it is the same untrustworthy state.
		_unit_reset_markers "$MSK"
		_unit_run "$FIXSKIP" "$MSK" _unit_fixture_lockskip _unit_fixture_bare77
		if [ "$_RC" -eq 4 ]; then
			pass "15q: an un-resettable sentinel is an internal error (rc 4)"
		else
			fail "15q: want rc=4, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		if printf '%s\n' "$_OUT" | grep -q '^SKIP _unit_fixture_bare77$'; then
			fail "15q: a crashing row was laundered into a SKIP by a stale sentinel out=$_OUT"
		else
			pass "15q: the following row was not laundered into a SKIP"
		fi
		_unit_assert_no_summary "$_OUT" "15q: no summary once sentinel state is untrustworthy"
		if printf '%s\n' "$_ERR" | grep -qi 'internal error'; then
			pass "15q: the un-resettable sentinel is reported as an internal error"
		else
			fail "15q: un-resettable sentinel not reported as an internal error err=$_ERR"
		fi
		# The row above makes the sentinel un-writable AND leaves content, so it
		# is caught whichever half of the guard is intact. This one makes it
		# un-writable while leaving it EMPTY, so only the failed write can be
		# what notices — otherwise 15q would keep passing with that half removed.
		_unit_reset_markers "$MSK"
		_unit_run "$FIXSKIP" "$MSK" _unit_fixture_lockempty _unit_fixture_m1
		if [ "$_RC" -eq 4 ]; then
			pass "15q: an un-writable but empty sentinel is an internal error (rc 4)"
		else
			fail "15q: want rc=4 for an empty un-writable sentinel, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_ran "$MSK" _unit_fixture_m1 no "15q: the row after an un-writable sentinel never executed"
		_unit_assert_no_summary "$_OUT" "15q: no summary when the sentinel cannot be written"
		# Attribute the abort to the SECOND row, or an exit 4 raised anywhere
		# before the first row would satisfy the three assertions above.
		if printf '%s\n' "$_ERR" | grep -q "internal error.*before row '_unit_fixture_m1'"; then
			pass "15q: the failed write is reported against the row about to run"
		else
			fail "15q: the failed write is not attributed to the next row err=$_ERR"
		fi
		if printf '%s\n' "$_OUT" | grep -q '^PASS _unit_fixture_lockempty$'; then
			pass "15q: the row that made the sentinel un-writable ran and was classified"
		else
			fail "15q: the first row did not run, so nothing made the sentinel un-writable out=$_OUT"
		fi

		# --- 15s: a stale reason that survives the reset must abort, not launder -
		# 15q induces a reset that FAILS. This induces the other fault the same
		# guard exists for: a reset that reports success and leaves the previous
		# row's reason in place anyway (the file, or something writing to it,
		# outlived the truncate). Left unnoticed, the next row's rc 77 crash is
		# corroborated by a reason it never wrote and is laundered into a skip —
		# QUM-952 again, one row over. Not stageable without privileges
		# (append-only attrs, a directory, a procfs or FIFO sentinel all make the
		# truncate itself fail, i.e. 15q's arm), so it is induced through a debug
		# seam, exactly as 15j induces a skewed tally.
		_unit_reset_markers "$MSK"
		_unit_run_env "$FIXSKIP" "$MSK" "SPRAWL_E2E_MATRIX_DEBUG_STALE_SENTINEL=1" \
			_unit_fixture_libskip _unit_fixture_bare77
		if [ "$_RC" -eq 4 ]; then
			pass "15s: a reason surviving the reset is an internal error (rc 4)"
		else
			fail "15s: want rc=4, got rc=$_RC out=$_OUT err=$_ERR"
		fi
		# The laundering itself, asserted directly: bare77 exits 77 having
		# written nothing, so the only reason available to it is the stale one.
		if printf '%s\n' "$_OUT" | grep -q '^SKIP _unit_fixture_bare77$'; then
			fail "15s: a crashing row was laundered into a SKIP by a stale reason out=$_OUT"
		else
			pass "15s: the row after the stale reason is not laundered into a SKIP"
		fi
		# The seam must leave the reason for the guard to read, not manufacture
		# the abort ahead of the loop: the first row still ran and was classified.
		if printf '%s\n' "$_OUT" | grep -q '^SKIP _unit_fixture_libskip$'; then
			pass "15s: the row that wrote the reason ran and was classified normally"
		else
			fail "15s: the first row did not run, so no reason was left to survive out=$_OUT"
		fi
		_unit_assert_verdict_lines_any "$_OUT" 1 "15s: the row facing a dirty sentinel gets no verdict at all"
		_unit_assert_ran "$MSK" _unit_fixture_bare77 no "15s: the row facing a dirty sentinel never executed"
		_unit_assert_no_summary "$_OUT" "15s: no summary once a reason of unknown provenance is in play"
		_unit_assert_no_breakdown "$_OUT" "15s: no breakdown line printed on a surviving reason"
		# Same line, not merely both present: the stale reason must be reported
		# against the row that was about to run, which is what tells a reader
		# whose reason it is not.
		if printf '%s\n' "$_ERR" | grep -q "internal error.*before row '_unit_fixture_bare77'"; then
			pass "15s: the surviving reason is reported against the row about to run"
		else
			fail "15s: the surviving reason is not attributed to the next row err=$_ERR"
		fi
		# Control: with the reason cleared as normal, bare77's uncorroborated rc
		# 77 is a plain failure — so the rc 4 above is the induced fault, not a
		# guard that fires unconditionally. The overlap with 15h is deliberate:
		# the value here is being 15s's own invocation MINUS the seam, which is
		# what attributes the abort to the seam. Do not de-duplicate it away.
		_unit_reset_markers "$MSK"
		_unit_run "$FIXSKIP" "$MSK" _unit_fixture_libskip _unit_fixture_bare77
		if [ "$_RC" -eq 1 ]; then
			pass "15s: with the reason cleared, the next row's rc 77 is a failure (rc 1)"
		else
			fail "15s: control run rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_breakdown "$_OUT" 0 1 1 2 "15s: control run breakdown is 1 failed + 1 skipped of 2"
		# Second control, with the reset suppressed but NO reason left behind by
		# either row: the abort must be attributable to the surviving content and
		# nothing else. Without this, an abort keyed on "not the first row"
		# rather than on the sentinel's contents satisfies every assertion above.
		_unit_reset_markers "$MSK"
		_unit_run_env "$FIXSKIP" "$MSK" "SPRAWL_E2E_MATRIX_DEBUG_STALE_SENTINEL=1" \
			_unit_fixture_m1 _unit_fixture_m2
		if [ "$_RC" -eq 0 ]; then
			pass "15s: a suppressed reset with nothing to survive is a clean run (rc 0)"
		else
			fail "15s: nothing-to-survive control rc=$_RC out=$_OUT err=$_ERR"
		fi
		_unit_assert_breakdown "$_OUT" 2 0 0 2 "15s: nothing-to-survive control breakdown is 2 passed of 2"
		_unit_assert_ran "$MSK" _unit_fixture_m2 yes "15s: both rows ran when no reason survived"

		# --- 15p: the skip contract, and the QUM-1155 retentions in CLAUDE.md,
		#          must stay documented where agents read them ---
		# NOTE FOR ANYONE GREPPING "what pins CLAUDE.md?": it is this block, in
		# the e2e-matrix unit suite. Three of its legs assert nothing about the
		# matrix at all — they pin the auth pointer, the TUI-validation mandate
		# and the discoverability principle on the always-loaded surface. They
		# live here because this is where the skip contract's own CLAUDE.md
		# assertions already were, not because they are e2e concerns.
		# Same philosophy as [11]'s self-wiring check: a fix that leaves the
		# agent-facing instructions lying has not landed. Assert the NEW contract
		# WORDING, not the issue key — grepping 'QUM-952' would already pass
		# against the text that documents the bug. That reasoning is why this
		# gate is not keyed on a filename either: QUM-1155 moved the skip
		# contract out of CLAUDE.md into the e2e-matrix skill, and the gate
		# followed the CONTRACT to its new home rather than being deleted with
		# the old one.
		#
		# The forbidden-phrase leg below is trivially true against a MISSING
		# file, which is a false-green path: rename the target and the old
		# gate went green having read nothing. So the target's existence is a
		# hard precondition, every required literal is COUNTED rather than
		# merely probed, and the count is printed on success — a check that
		# says nothing when it passes forces every caller to build its own
		# probe, and home-made probes are what keep failing here.
		#
		# grep -c counts LINES, not occurrences. The counts below are therefore
		# asserted as ">= 1", never pinned to an exact number: 'Not logged in'
		# appears 3 times on 2 lines in the target, so an exact-count assertion
		# would pin a number that means something other than it appears to.
		_15p_target="$REPO_ROOT/.claude/skills/e2e-matrix/SKILL.md"
		if [ -f "$_15p_target" ]; then
			pass "15p: skip-contract target exists ($_15p_target)"
		else
			fail "15p: skip-contract target is missing: $_15p_target — every phrase assertion below would be vacuous"
		fi
		# Forbidden: the pre-QUM-952 claim that a skipped row is reported as a
		# pass. This leg is FAIL-CLOSED, and that is not a precaution — it was
		# demonstrated live: a QA agent deleted CLAUDE.md outright and this leg
		# printed `pass`. `grep -q` exits 2 on a missing file, and branching on
		# "non-zero" reads that error as "the phrase is absent", so the gate
		# reports a clean bill of health having read nothing. Three of the four
		# original legs failed in that state; this one stayed green.
		#
		# So the three exit statuses are distinguished explicitly: 0 present
		# (fail), 1 genuinely absent (pass), anything else an ERROR (fail). Do
		# not collapse this back into an if/else on grep's exit status.
		_15p_rc=0
		grep -q 'skipped row is currently reported as' "$_15p_target" || _15p_rc=$?
		if [ "$_15p_rc" -eq 1 ]; then
			pass "15p: the e2e-matrix skill no longer documents skip-as-PASS"
		elif [ "$_15p_rc" -eq 0 ]; then
			fail "15p: the e2e-matrix skill still says a skipped row is reported as PASS"
		else
			fail "15p: cannot establish the absence of skip-as-PASS — grep exited $_15p_rc on '$_15p_target' (missing or unreadable), so this leg checked NOTHING"
		fi
		# Required, counted. 'unauthenticated' / 'Not logged in' / 'never hide'
		# are the installed-but-unauthenticated state and its forbidden
		# workaround: the gate keys on presence and never probes auth, so a row
		# in that state fails with "Not logged in" and is trivially misread as a
		# product regression. Hiding claude from PATH converts it into the
		# all-skip vacuum.
		_15p_checked=0
		_15p_missing=""
		for _15p_lit in \
			'Matrix breakdown' \
			'exit 3' \
			'unauthenticated' \
			'Not logged in' \
			'never hide'; do
			_15p_n=0
			if [ -f "$_15p_target" ]; then
				_15p_n=$(grep -ci -- "$_15p_lit" "$_15p_target" || true)
			fi
			if [ "${_15p_n:-0}" -ge 1 ]; then
				_15p_checked=$((_15p_checked + 1))
			else
				_15p_missing="$_15p_missing '$_15p_lit'"
			fi
		done
		# Assertion-count floor: a loop that silently iterated zero times would
		# otherwise report a clean run having checked nothing.
		if [ "$_15p_checked" -eq 5 ] && [ -z "$_15p_missing" ]; then
			pass "15p: checked $_15p_checked/5 required literals in .claude/skills/e2e-matrix/SKILL.md (skip contract + unauthenticated state + PATH-hiding ban)"
		else
			fail "15p: the e2e-matrix skill is missing required skip-contract literals:$_15p_missing (matched $_15p_checked/5) in $_15p_target"
		fi
		# The cut must stay cut. CLAUDE.md is the always-loaded surface QUM-1155
		# shrank; the skip contract living in BOTH places would silently
		# re-grow it, and nothing else in the pipeline would notice.
		#
		# FAIL-CLOSED for the same reason as the leg above, and it is worth
		# saying why the reason recurs: EVERY absence assertion in this file is
		# one missing file away from being vacuous, because "grep found nothing"
		# and "grep could not look" are the same branch unless you separate
		# them. This is an absence assertion, so it separates them. The next
		# absence assertion added here must do so too.
		_15p_rc=0
		grep -q 'Matrix breakdown' "$REPO_ROOT/CLAUDE.md" || _15p_rc=$?
		if [ "$_15p_rc" -eq 1 ]; then
			pass "15p: the skip contract is not duplicated back into the always-loaded CLAUDE.md"
		elif [ "$_15p_rc" -eq 0 ]; then
			fail "15p: CLAUDE.md carries the skip-contract prose again — it belongs only in $_15p_target"
		else
			fail "15p: cannot establish that the cut stayed cut — grep exited $_15p_rc on '$REPO_ROOT/CLAUDE.md' (missing or unreadable), so this leg checked NOTHING"
		fi
		# ...with exactly one deliberate exception. A fresh agent hitting this
		# error has not loaded the skill yet, so the pointer has to survive in
		# the always-loaded surface. Asserted, not merely tolerated, so that
		# deleting it in a future tidy-up fails here rather than silently.
		if grep -qi 'Not logged in' "$REPO_ROOT/CLAUDE.md"; then
			pass "15p: CLAUDE.md keeps the 'Not logged in' pointer for agents that have loaded no skill yet"
		else
			fail "15p: CLAUDE.md dropped the 'Not logged in' pointer — an agent hitting that error has no always-loaded route to the fix"
		fi
		# QUM-1155 kept exactly three things on the always-loaded surface that
		# it could have pushed into a skill: the auth pointer above, the
		# TUI-validation mandate, and the principle that explains why either is
		# here. Only the first was pinned. A future cut could delete the other
		# two and every gate in this pipeline would stay green — which is the
		# defect QUM-1155 exists to remove, so leaving them unpinned made the
		# cut an instance of its own defect class. These two legs close that.
		#
		# Not keyed on the bare phrase 'TUI validation is mandatory': that ALSO
		# matches the skills-index entry ("/tui-testing — before changing the
		# TUI; TUI validation is mandatory there."), so it survives deletion of
		# the mandate itself and the guard goes on passing while the thing it
		# guards is gone. Measured, not assumed — with the mandate deleted, a
		# grep for the short phrase still returns a line.
		#
		# But the longer 'mandatory for all TUI-related changes' is NOT immune
		# on its own either, and the first draft of this leg wrongly claimed it
		# was. Any single-phrase key is a whole-FILE assertion: reword the index
		# entry to carry the long phrase and the leg passes with line 30 gone.
		# So the key is a SINGLE-LINE co-location — the mandate must appear on
		# the same line as the paragraph opener that owns it, and that line must
		# also carry the /tui-testing pointer. That is a property of where the
		# mandate lives, not of what some other line happens to say today, and
		# it additionally pins the requirement's route to its procedure: a
		# mandate with no pointer to the harness is half the rule.
		#
		# Deliberately a BRE, not -F: the '.*' between the three anchors is the
		# co-location operator and is the entire point. The three literals were
		# picked to contain no BRE metacharacters, so nothing else needs
		# quoting — check that before editing any of them.
		#
		# WHAT THIS LEG STILL LETS THROUGH, written after watching it go red:
		# it is a substring check, so it cannot detect NEGATION. Rewriting the
		# line to "No longer: TUI validation was once mandatory for all
		# TUI-related changes … /tui-testing" keeps it green. Detecting that is
		# unbounded and not attempted. It also does not check that line 30 is
		# in the Build & validate section, only that the three parts share one
		# line.
		#
		# These are presence assertions rather than absence assertions, but
		# they are FAIL-CLOSED on rc >= 2 for the same reason the absence legs
		# above are: "the sentence is gone" and "I could not read the file"
		# are the same branch unless you separate them, and only the first is
		# a finding about the document. The rc >= 2 arms of both legs were
		# exercised with `chmod 000 CLAUDE.md` — note that control is
		# HOST-DEPENDENT and silently proves nothing as root, who can read a
		# 000 file; the uid-independent form is to point the path at something
		# nonexistent. Re-run the robust form if you touch these arms.
		_15p_tuikey='Validating a change is more than running validate.*mandatory for all TUI-related changes.*/tui-testing'
		_15p_rc=0
		grep -q -- "$_15p_tuikey" "$REPO_ROOT/CLAUDE.md" || _15p_rc=$?
		if [ "$_15p_rc" -eq 0 ]; then
			pass "15p: CLAUDE.md states the TUI-validation mandate and its /tui-testing pointer on one line with the paragraph opener that owns it"
		elif [ "$_15p_rc" -eq 1 ]; then
			fail "15p: CLAUDE.md lost the TUI-validation mandate, its /tui-testing pointer, or their co-location — no line of '$REPO_ROOT/CLAUDE.md' matches '$_15p_tuikey'. The skills-index entry is not a substitute: a skill's trigger line announces that a rule exists but cannot state it with force. If you reworded the mandate on purpose, update the key in this file rather than deleting this leg"
		else
			fail "15p: cannot establish that the TUI mandate survives — grep exited $_15p_rc on '$REPO_ROOT/CLAUDE.md' (missing or unreadable), so this leg checked NOTHING"
		fi
		# The principle itself, keyed on three literals unique to the skills
		# preamble: the claim, the sentence naming the two retentions it
		# licenses, and the rule. Three rather than one because the sentence
		# is only load-bearing whole — the first draft pinned the opening and
		# closing clauses only, which left the MIDDLE sentence (the one
		# carrying the referents) deletable while both keys survived. That
		# leaves 'Do not tidy them away.' with no antecedent, which is exactly
		# the state being guarded against. Counted and printed for the same
		# reason the literal loop above is: a check that says nothing when it
		# passes forces every caller to build its own probe.
		#
		# All three emit ONE assertion between them, not three, so the floors
		# moved by +1 for this leg regardless of how many literals it holds.
		#
		# -F is REQUIRED here, not stylistic. Without it 'the *requirement*
		# stays here' is a BRE in which ' *' means "zero or more spaces" — it
		# would still match today's file, so the bug would be latent rather
		# than loud. Do not "simplify" the -F away.
		#
		# WHAT THIS LEG STILL LETS THROUGH: substring presence, so a negated
		# or hedged rewrite that retains the three phrases passes; and it does
		# not check that the three remain on ONE line or in the Skills
		# section, so splitting them across the file would pass.
		_15p_pcount=0
		_15p_pmissing=""
		_15p_perr=""
		for _15p_plit in \
			'the *requirement* stays here' \
			'stated in this file rather than delegated' \
			'Do not tidy them away'; do
			_15p_rc=0
			grep -qF -- "$_15p_plit" "$REPO_ROOT/CLAUDE.md" || _15p_rc=$?
			if [ "$_15p_rc" -eq 0 ]; then
				_15p_pcount=$((_15p_pcount + 1))
			elif [ "$_15p_rc" -eq 1 ]; then
				_15p_pmissing="$_15p_pmissing '$_15p_plit'"
			else
				_15p_perr="$_15p_perr '$_15p_plit' (rc=$_15p_rc)"
			fi
		done
		if [ -n "$_15p_perr" ]; then
			fail "15p: cannot establish that the discoverability principle survives — matched $_15p_pcount of 3 and then grep could not read '$REPO_ROOT/CLAUDE.md' for$_15p_perr, so this leg's verdict rests on nothing"
		elif [ "$_15p_pcount" -eq 3 ] && [ -z "$_15p_pmissing" ]; then
			pass "15p: checked $_15p_pcount/3 discoverability-principle literals in $REPO_ROOT/CLAUDE.md (the requirement stays on the always-loaded surface; only the procedure moves to the skill)"
		else
			fail "15p: CLAUDE.md lost the discoverability principle:$_15p_pmissing (matched $_15p_pcount/3) — without it the retentions above read as arbitrary exceptions and the next cut tidies them away"
		fi
		unset _15p_target _15p_checked _15p_missing _15p_lit _15p_n _15p_rc
		unset _15p_pcount _15p_pmissing _15p_perr _15p_plit _15p_tuikey

		for _d in "$FIXSKIP" "$FIXALL"; do
			case "$_d" in
				"$UNIT_TMP_ROOT"/*) rm -rf "$_d" ;;
				*) note "refusing to remove fixture dir '$_d' (not under $UNIT_TMP_ROOT/)" ;;
			esac
		done
	fi
fi

# ----------------------------------------------------------------------------
# 16. This suite's verdict must not depend on the environment that ran it.
#
#     Same idea as [11]'s Makefile self-wiring check and [15p]'s check that
#     the skip contract is documented where agents read it, pointed at this file: a gate whose result the caller can flip is
#     not a gate. A driver debug seam exported in the invoking shell reaches
#     every driver child that did not scrub it, and the assertions fed by those
#     children then pass or fail for reasons unrelated to the code under test.
#
#     16a is static and derived from the driver, so a seam added later and left
#     unregistered fails here rather than quietly joining the inherited-env
#     surface. 16b is the behavioural proof: it re-runs this whole suite once
#     per seam, with that seam exported, and demands the same verdict. Only 16b
#     would have caught the measured failure, because the [10] call sites are
#     invisible to any list of scrubbed names.
# ----------------------------------------------------------------------------
echo "[16] suite environment hygiene"

# The recursion guard is a NONCE PATH, not a boolean, and the nonce is created
# by the parent for one run. A caller who exports UNIT_NESTED_SEAM_CHECK=1
# would otherwise silently disable this whole section and still see a green
# suite — which is precisely the fault [16] exists to detect, so an unbacked
# guard value has to fail loudly instead.
if [ -n "${UNIT_NESTED_SEAM_CHECK:-}" ]; then
	if [ -r "$UNIT_NESTED_SEAM_CHECK" ] &&
		grep -q '^nested-seam-check$' "$UNIT_NESTED_SEAM_CHECK" 2>/dev/null; then
		# A 16b child. Recursing would fork-bomb, and a `pass` here would
		# corrupt the coverage comparison below, so this branch counts nothing.
		# The floor is emitted at the SAME point the parent reads its own, so
		# adding a section after [16] cannot skew the comparison.
		note "16: nested child — [16] intentionally not re-run"
		echo "NESTED-FLOOR: $PASS"
	else
		fail "16: UNIT_NESTED_SEAM_CHECK is set in the invoking environment without a live nonce — [16] would have been disabled by the caller"
	fi
else
	_nested_floor=$PASS
	_parent_fail_on_entry=$FAIL

	# --- 16a: every seam the driver reads is registered for scrubbing --------
	_seams=$(grep -ohE 'SPRAWL_E2E_MATRIX_DEBUG_[A-Z0-9_]+' "$DRIVER" "$LIB" 2>/dev/null | sort -u)
	if [ -z "$_seams" ]; then
		# No hits means the probe broke, not that the driver has no seams: the
		# loop below would then be vacuous and report all-green.
		fail "16a: no SPRAWL_E2E_MATRIX_DEBUG_* seams found in the driver or lib — the probe broke, and every 16b assertion below silently disappears with it"
	fi
	for _s in $_seams; do
		_found=0
		for _r in "${UNIT_SCRUBBED_VARS[@]}"; do
			[ "$_r" = "$_s" ] && _found=1
		done
		if [ "$_found" -eq 1 ]; then
			pass "16a: an exported $_s cannot invert this suite's negative controls"
		else
			fail "16a: driver seam $_s is unregistered — an exported value would invert this suite's negative controls"
		fi
	done
	# The registry is only a single authority if the one call site that still
	# scrubs per-invocation is built from it. Without this, that prefix becomes
	# unobservable once the process-wide unset lands, and the two lists it was
	# meant to merge can drift again with nothing to notice.
	if grep -q '"\${UNIT_SCRUB_ARGS\[@\]}"' "$UNIT_SELF"; then
		pass "16a: _unit_run_env's scrub is built from the registry, not a second list"
	else
		fail "16a: a hand-written scrub list has reappeared — the registry is no longer the only authority"
	fi

	# --- 16b: an exported seam cannot change this suite's verdict ------------
	# One child per seam, never both at once: each seam aborts the driver with
	# rc 4 by a different route, so a combined child could not say which caused
	# it. The value is passed on the child's command line rather than inherited,
	# so the section stages its own hostile input instead of waiting for an
	# operator to supply one.
	#
	# The list is the DRIVER's seams, not the whole registry: the other two
	# registry entries cannot reach a driver child (e2e-matrix.sh assigns
	# E2E_SKIP_FILE from mktemp unconditionally, and every site that needs
	# SPRAWL_E2E_SKIP_NO_CLAUDE sets it on the child's own command line), so a
	# child for either would cost 1.1s to assert a property nothing can break.
	# 16a covers them statically for free.
	_nonce=$(mktemp "$UNIT_TMP_ROOT/e2e-matrix-unit-nonce.XXXXXX" 2>/dev/null)
	_timeout=$(command -v timeout)
	if [ -z "$_nonce" ] || ! printf 'nested-seam-check\n' >"$_nonce" 2>/dev/null; then
		fail "16b: cannot stage the recursion nonce — nested check not run"
	elif [ ! -r "$UNIT_SELF" ]; then
		fail "16b: cannot locate this suite at '$UNIT_SELF' — nested check not run"
	else
		for _s in $_seams; do
			_clog=$(mktemp "$UNIT_TMP_ROOT/e2e-matrix-unit-child.XXXXXX") || {
				fail "16b: cannot mktemp child log for $_s — neither claim was checked for it"
				continue
			}
			# timeout is insurance: a regression in the recursion guard above
			# would otherwise hang `make validate` inside the pre-commit hook.
			#
			# The budget is 90s, raised from 30s in QUM-1186 lane 5. Measured child
			# at that commit: 5.2s, i.e. a ~17x margin. The old
			# comment claimed "a 25x margin on a ~1.2s child" — that figure was
			# measured before section [19] existed and had silently decayed to
			# ~2.2x, because [19]'s probe controls are dominated by deliberate
			# not-found waits that every child inherits. The probe's off-by-one
			# sleep was fixed in the same lane (it slept a full poll interval
			# PAST the deadline), which brought the child back down, but the
			# margin is re-stated as a MEASUREMENT rather than a boast: re-measure
			# it when you add to [19], because rc 124 lands in the failure branch
			# below, whose text names a verdict change. A slow child there reads
			# as "the debug seam changed this suite's verdict" — a false
			# diagnosis on an arm that owns none of the cause. Check the rc in
			# the message before believing that attribution.
			# shellcheck disable=SC2086
			env "UNIT_NESTED_SEAM_CHECK=$_nonce" "$_s=1" \
				${_timeout:+"$_timeout" -k 5 90} bash "$UNIT_SELF" >"$_clog" 2>&1
			_crc=$?
			_cres=$(grep -E '^=== unit results: [0-9]+ passed / [0-9]+ failed ===$' "$_clog" | tail -1)
			_cfloor=$(sed -n 's/^NESTED-FLOOR: \([0-9]*\)$/\1/p' "$_clog" | tail -1)
			# When the parent is already failing, the child inherits those
			# failures and the label below would misattribute them.
			_caveat=""
			if [ "$_parent_fail_on_entry" -gt 0 ]; then
				_caveat=" (NOTE: the parent already had $_parent_fail_on_entry failures — this may be pre-existing rather than an env-hygiene regression)"
			fi
			if [ "$_crc" -eq 0 ] && [ -n "$_cres" ]; then
				pass "16b: an exported $_s cannot change this suite's verdict or coverage from any call site"
			else
				fail "16b: exporting $_s changed this suite's verdict$_caveat (child rc=$_crc, '$_cres'); child failures: $(grep '^  FAIL' "$_clog" | tr '\n' '|')"
			fi
			# A child that exited 0 having skipped whole sections satisfies the
			# check above, so its coverage must match the parent's exactly —
			# `-ge` would tolerate coverage shrinking.
			if [ -n "$_cfloor" ] && [ "$_cfloor" -eq "$_nested_floor" ]; then
				pass "16b: the child run with $_s exported asserted the same $_nested_floor things"
			else
				fail "16b: child with $_s exported asserted ${_cfloor:-<no floor reported>}, want exactly $_nested_floor"
			fi
			rm -f "$_clog"
		done

		# --- 16c: a naive guard value fails loudly instead of skipping [16] ---
		# 16b's recursion guard is itself an inherited variable, so a caller who
		# exports it would skip all of [16] and still see green — this section's
		# own defect, one level up. What is provable is bounded: the guard is a
		# path whose contents the child checks, and a caller can write that same
		# literal, so a DELIBERATE bypass remains possible and no inheritable
		# token can fix that (the caller is the parent, from the child's point of
		# view). What 16c asserts is the accident that actually happens — a bare
		# `1`, a stale path, a leftover export — and it is the only assertion
		# that notices if the guard is relaxed back to a truthiness test.
		_clog=$(mktemp "$UNIT_TMP_ROOT/e2e-matrix-unit-child.XXXXXX") || fail "16c: cannot mktemp child log — the guard was not checked"
		if [ -n "$_clog" ]; then
			# shellcheck disable=SC2086
			env "UNIT_NESTED_SEAM_CHECK=$_nonce.not-a-real-nonce" \
				${_timeout:+"$_timeout" -k 5 30} bash "$UNIT_SELF" >"$_clog" 2>&1
			_crc=$?
			if [ "$_crc" -ne 0 ] && grep -q 'disabled by the caller' "$_clog"; then
				pass "16c: an unbacked recursion guard value fails the run instead of skipping [16]"
			else
				fail "16c: an unbacked recursion guard silently disabled [16] (child rc=$_crc)"
			fi
			rm -f "$_clog"
		fi
	fi
	rm -f "$_nonce"
fi

# ----------------------------------------------------------------------------
# 17. QUM-1029: a row that asserts NOTHING must not report PASS.
#
#     `e2e_print_results` (scripts/lib/e2e-common.sh) printed
#     "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ===" and returned
#     non-zero ONLY when FAIL_COUNT>0. A row that recorded neither a pass nor a
#     fail therefore rendered "0 passed, 0 failed", returned 0, and the driver
#     — which reads a row's EXIT STATUS ONLY and is structurally unable to see
#     how much the row asserted — counted it in `pass_count` and in the
#     "=== Matrix: N/N passed ===" line CLAUDE.md tells readers to scrape.
#     Same governing principle as [14] and [15] (QUM-943): a harness that
#     reports pass/fail must be unable to report pass for work it did not do.
#
#     The floor is PER-ROW and CALLER-SUPPLIED — each row declares a top-level
#     `MIN_ASSERTIONS=<n>`. It is deliberately NOT derived from anything the
#     harness measures: an aggregate-derived floor is satisfied by `0 == 0`,
#     which is the very defect. An UNDECLARED floor is a failure too (17e),
#     because a default would let the 33 pre-existing rows keep the defect
#     silently, and a declared 0 / empty / negative / non-numeric floor is
#     rejected (17f, 17g) because each of those is the defect wearing a
#     declaration.
#
#     A floor breach is an ORDINARY FAILURE: driver exit 1, not the skip (3) or
#     internal-invariant (4) statuses. So every negative case below asserts
#     `rc -eq 1` exactly — `-ne 0` would be satisfied by 2/3/4, and in
#     particular by exit 2 from a fixture that failed to write, which would let
#     the whole section pass having run nothing.
#
#     These cases drive REAL fixture rows through the REAL driver — the fixture
#     tree copies $LIB, so the modified aggregator is what runs. A test of the
#     floor that never drives a zero-assertion row proves nothing, so 17d is the
#     load-bearing case and 17h is its attributability baseline.
# ----------------------------------------------------------------------------
echo "[17] QUM-1029 per-row assertion floor: a row that asserts nothing cannot PASS"

# Write a fixture row that makes $4 pass() and $5 fail() calls and then calls
# the shared aggregator. $3 is emitted verbatim above test_run (the
# MIN_ASSERTIONS declaration, or "" to declare none, or a whole pre-fix
# aggregator for the 17h baseline).
#
# The heredoc delimiter is UNQUOTED so $2/$3/$4/$5 interpolate, and that is safe
# for $3's own `$PASS_COUNT` references: bash does not re-expand a parameter's
# VALUE. Everything meant to survive into the generated row is backslashed.
#
# The counting loop uses `while` + $((...)) rather than `seq` or `((i++))`: this
# suite drives the driver with PATH scrubbed in places so fixtures must stay to
# builtins, and `((i++))` returns 1 on the 0->1 step, which `set -e` would take
# as a row failure.
_unit_mk_assert_row() {
	cat >"$1/$2.sh" <<EOF || return 1
$3
test_metadata() { echo ""; }
test_run() {
	: >"\${UNIT_MARKER_DIR:?UNIT_MARKER_DIR unset}/$2"
	local i=0
	while [ "\$i" -lt $4 ]; do i=\$((i + 1)); pass "$2 assertion \$i"; done
	i=0
	while [ "\$i" -lt $5 ]; do i=\$((i + 1)); fail "$2 negative \$i"; done
	e2e_print_results
}
EOF
}

# A floor breach must be a DIAGNOSIS, not a crash. Without this, an
# implementation that dereferences $MIN_ASSERTIONS bare would satisfy the
# undeclared-floor case twice over — `set -u` aborts the row with a non-zero
# status AND prints the literal string MIN_ASSERTIONS on stderr — while the
# aggregator never ran and nothing about the floor was exercised. Ditto
# `[ 0 -lt abc ]`, which exits 2 with "integer expression expected".
#
# Keyed on bash's OWN diagnostic format (`<source>: line <n>: <message>`) and
# not on the phrases alone: a legitimate floor diagnostic that quotes the
# declaration it rejected could contain "integer expression expected" or
# "command not found" as text, and would then fail for the opposite of the
# reason this exists.
_unit_assert_no_shell_error() {
	if printf '%s\n' "$1" | grep -qE ': line [0-9]+:.*(unbound variable|command not found|syntax error|integer expression expected)'; then
		fail "$2 (the row died on a shell error, not on a floor diagnostic) err=$1"
	else
		pass "$2"
	fi
}

# Assert the row's own verdict line. The driver prints exactly one of
# `PASS <row>` / `FAIL <row>` / `SKIP <row>`, so this discriminates a failure
# from a skip on the observable per-row side effect rather than on rc alone.
_unit_assert_verdict() {
	if printf '%s\n' "$1" | grep -qx "$2 $3"; then
		pass "$4"
	else
		fail "$4 (no '$2 $3' verdict line) out=$1"
	fi
}

if [ ! -r "$LIB" ] || [ ! -r "$DRIVER" ]; then
	fail "17: QUM-1029 floor tests skipped (lib or driver missing)"
else
	FIXFLOOR=$(mktemp -d "$UNIT_TMP_ROOT/e2e-matrix-unit-floor.XXXXXX" 2>/dev/null)
	if [ -z "$FIXFLOOR" ] || [ ! -d "$FIXFLOOR" ]; then
		fail "17: could not mktemp the QUM-1029 fixture dir"
	else
		floor_setup_ok=1
		_unit_mk_fixture_tree "$FIXFLOOR" || floor_setup_ok=0
		FRD="$FIXFLOOR/e2e-tests"
		MFLOOR="$FIXFLOOR/markers"

		# An arithmetic-injection payload. An implementation using
		# `(( total < MIN_ASSERTIONS ))` or `$((MIN_ASSERTIONS))` EXECUTES the
		# command substitution inside it; the marker it would drop is asserted
		# absent in 17g.
		_floor_inject='MIN_ASSERTIONS='"'"'x[$(: >"$UNIT_MARKER_DIR/pwned")]'"'"''

		#                   dir    row name            preamble             pass fail
		_unit_mk_assert_row "$FRD" _unit_floor_exact   "MIN_ASSERTIONS=5"   5 0 || floor_setup_ok=0
		_unit_mk_assert_row "$FRD" _unit_floor_over    "MIN_ASSERTIONS=5"   7 0 || floor_setup_ok=0
		# Two-digit floor and a 4-count on purpose: single digits collide with
		# the mktemp suffix and with the driver's own exit codes, so `grep 5`
		# and `grep 3` could both be satisfied by a temp path in the output.
		_unit_mk_assert_row "$FRD" _unit_floor_short   "MIN_ASSERTIONS=17"  4 0 || floor_setup_ok=0
		_unit_mk_assert_row "$FRD" _unit_floor_zero    "MIN_ASSERTIONS=1"   0 0 || floor_setup_ok=0
		_unit_mk_assert_row "$FRD" _unit_floor_undecl  ""                   3 0 || floor_setup_ok=0
		_unit_mk_assert_row "$FRD" _unit_floor_zerodecl "MIN_ASSERTIONS=0"  0 0 || floor_setup_ok=0
		_unit_mk_assert_row "$FRD" _unit_floor_metfail "MIN_ASSERTIONS=2"   1 1 || floor_setup_ok=0
		# Genuine failure, far short of a high floor — the shortfall must stay
		# quiet so the real defect is the last thing on stderr.
		_unit_mk_assert_row "$FRD" _unit_floor_shortfail "MIN_ASSERTIONS=25" 0 1 || floor_setup_ok=0
		# Bad declarations, each with FIVE real assertions: the count can never
		# be the reason these fail, so only validation of the declaration can be.
		_unit_mk_assert_row "$FRD" _unit_floor_junk    "MIN_ASSERTIONS=abc" 5 0 || floor_setup_ok=0
		_unit_mk_assert_row "$FRD" _unit_floor_empty   "MIN_ASSERTIONS="    5 0 || floor_setup_ok=0
		_unit_mk_assert_row "$FRD" _unit_floor_neg     "MIN_ASSERTIONS=-1"  5 0 || floor_setup_ok=0
		_unit_mk_assert_row "$FRD" _unit_floor_inject  "$_floor_inject"     5 0 || floor_setup_ok=0
		# Past int64: `[ -lt ]` errors on it, and an error inside an `if` is just
		# "false", so an unguarded implementation records no breach and PASSES.
		_unit_mk_assert_row "$FRD" _unit_floor_huge    "MIN_ASSERTIONS=99999999999999999999" 5 0 || floor_setup_ok=0

		# 17h baseline: the PRE-FIX aggregator, re-defined by the row itself so
		# nothing in the copied lib has to be mutated (rows are sourced AFTER
		# the lib, so the row's definition wins).
		_unit_mk_assert_row "$FRD" _unit_floor_baseline 'MIN_ASSERTIONS=1
e2e_print_results() {
	echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
	if [ "$FAIL_COUNT" -gt 0 ]; then return 1; fi
	return 0
}' 0 0 || floor_setup_ok=0

		if [ "$floor_setup_ok" -ne 1 ]; then
			# Skip rather than warn-and-continue: assertions run against a
			# half-written fixture tree pass for reasons of their own.
			fail "17: fixture setup failed (mkdir/cp/row write) — floor cases not run"
		else
			# --- 17a: floor 5, ran 5 -> passes (met exactly) --------------------
			_unit_reset_markers "$MFLOOR"
			_unit_run "$FIXFLOOR" "$MFLOOR" _unit_floor_exact
			if [ "$_RC" -eq 0 ]; then
				pass "17a: a row meeting its declared floor exactly passes"
			else
				fail "17a: floor 5 / 5 assertions was rejected (rc=$_RC) err=$_ERR"
			fi

			# --- 17b: floor 5, ran 7 -> passes (it is a floor, not an equality) --
			_unit_reset_markers "$MFLOOR"
			_unit_run "$FIXFLOOR" "$MFLOOR" _unit_floor_over
			if [ "$_RC" -eq 0 ]; then
				pass "17b: over-satisfying the floor passes (a floor, not an equality)"
			else
				fail "17b: floor 5 / 7 assertions was rejected (rc=$_RC) err=$_ERR"
			fi

			# --- 17c: floor 17, ran 4 -> fails, naming BOTH numbers -------------
			_unit_reset_markers "$MFLOOR"
			_unit_run "$FIXFLOOR" "$MFLOOR" _unit_floor_short
			if [ "$_RC" -eq 1 ]; then
				pass "17c: a row short of its declared floor fails the driver as an ordinary failure"
			else
				fail "17c: floor 17 / 4 assertions gave rc=$_RC (want 1 — not a skip(3) or internal error(4)) out=$_OUT"
			fi
			_unit_assert_summary "$_OUT" 0 1 "17c: the short row is NOT counted as passed"
			_unit_assert_verdict "$_OUT" FAIL _unit_floor_short "17c: the short row gets a FAIL verdict line, not SKIP"
			_unit_assert_ran "$MFLOOR" _unit_floor_short yes "17c: the short row really did execute"
			# Both numbers, as standalone tokens, on ONE line that also names
			# MIN_ASSERTIONS — the last clause is what stops a stderr line quoting
			# a mktemp path like `…-floor.aB17xY4z` from satisfying this by itself.
			# The word-bounding is not pedantry either: it catches an
			# implementation that reports 5 observed instead of 4 because its own
			# fail() incremented FAIL_COUNT before comparing.
			if printf '%s\n' "$_ERR" | grep -F 'MIN_ASSERTIONS' |
				grep -qE '(^|[^0-9])(17([^0-9]|$).*[^0-9]4([^0-9]|$)|4([^0-9]|$).*[^0-9]17([^0-9]|$))'; then
				pass "17c: one stderr line names both the declared floor (17) and the observed count (4)"
			else
				fail "17c: no stderr line names both the floor and the observed count err=$_ERR"
			fi
			_unit_assert_no_shell_error "$_ERR" "17c: the short row was diagnosed, not crashed"

			# --- 17d: floor 1, ran 0 -> fails. THE case this issue exists for. ---
			_unit_reset_markers "$MFLOOR"
			_unit_run "$FIXFLOOR" "$MFLOOR" _unit_floor_zero
			if [ "$_RC" -eq 1 ]; then
				pass "17d: a row that asserts NOTHING fails the driver as an ordinary failure"
			else
				fail "17d: a zero-assertion row gave rc=$_RC (want 1) out=$_OUT"
			fi
			_unit_assert_summary "$_OUT" 0 1 "17d: the zero-assertion row is NOT counted as passed"
			_unit_assert_verdict "$_OUT" FAIL _unit_floor_zero "17d: the zero-assertion row gets a FAIL verdict line, not SKIP"
			_unit_assert_ran "$MFLOOR" _unit_floor_zero yes "17d: the zero-assertion row really did execute"
			# Proves the row asserted nothing rather than dying before it could —
			# and pins that the "=== Results:" tally is still printed on the
			# floor-breach path, so the run stays diagnosable.
			if printf '%s\n' "$_OUT" | grep -qF '0 passed, 0 failed'; then
				pass "17d: the row genuinely recorded no assertions (its own tally says so)"
			else
				fail "17d: the fixture row did not actually assert nothing out=$_OUT"
			fi
			_unit_assert_no_shell_error "$_ERR" "17d: the zero-assertion row was diagnosed, not crashed"

			# --- 17e: no declaration at all -> fails ----------------------------
			# Without this, adding the floor would leave every pre-existing row
			# undeclared and therefore still defective, silently.
			_unit_reset_markers "$MFLOOR"
			_unit_run "$FIXFLOOR" "$MFLOOR" _unit_floor_undecl
			if [ "$_RC" -eq 1 ]; then
				pass "17e: a row declaring no floor fails even though it asserted 3 things"
			else
				fail "17e: an undeclared floor gave rc=$_RC (want 1) out=$_OUT"
			fi
			if printf '%s\n' "$_ERR" | grep -qF 'MIN_ASSERTIONS'; then
				pass "17e: the diagnostic names MIN_ASSERTIONS so the row author knows what to add"
			else
				fail "17e: the undeclared-floor diagnostic does not name MIN_ASSERTIONS err=$_ERR"
			fi
			_unit_assert_no_shell_error "$_ERR" "17e: the undeclared floor was diagnosed, not crashed"

			# --- 17f: MIN_ASSERTIONS=0 -> fails ---------------------------------
			_unit_reset_markers "$MFLOOR"
			_unit_run "$FIXFLOOR" "$MFLOOR" _unit_floor_zerodecl
			if [ "$_RC" -eq 1 ]; then
				pass "17f: a declared floor of 0 is rejected (a zero floor is the defect declared)"
			else
				fail "17f: MIN_ASSERTIONS=0 gave rc=$_RC (want 1) out=$_OUT"
			fi
			_unit_assert_no_shell_error "$_ERR" "17f: the zero floor was diagnosed, not crashed"

			# --- 17g: malformed declarations -> fail, and never get evaluated ----
			# Each of these rows asserts FIVE things, so a count shortfall can never
			# be the reason it fails: only rejection of the declaration itself can.
			# `-1` and the empty string matter because `[ 5 -lt -1 ]` is merely
			# false and `[ 5 -lt "" ]` errors-and-is-false, so both sail through an
			# implementation that validates "is an integer" or "is set".
			for _bad in _unit_floor_junk _unit_floor_empty _unit_floor_neg _unit_floor_inject _unit_floor_huge; do
				_unit_reset_markers "$MFLOOR"
				_unit_run "$FIXFLOOR" "$MFLOOR" "$_bad"
				if [ "$_RC" -eq 1 ]; then
					pass "17g: $_bad — a malformed floor is rejected rather than silently treated as 0"
				else
					fail "17g: $_bad gave rc=$_RC (want 1) out=$_OUT"
				fi
				_unit_assert_no_shell_error "$_ERR" "17g: $_bad was diagnosed, not crashed"
				if [ "$_bad" = _unit_floor_inject ]; then
					_unit_assert_ran "$MFLOOR" pwned no "17g: the floor value is compared, never evaluated as arithmetic"
				fi
			done

			# --- 17i: a met floor must not MASK a real failure ------------------
			# floor 2, one pass + one fail: observed 2 satisfies the floor, but the
			# row failed and must still fail — and must fail for THAT reason, which
			# is what the three discriminators below check. Counting only
			# PASS_COUNT would give 1 < 2 and fail on the floor instead.
			_unit_reset_markers "$MFLOOR"
			_unit_run "$FIXFLOOR" "$MFLOOR" _unit_floor_metfail
			if [ "$_RC" -eq 1 ]; then
				pass "17i: satisfying the floor does not mask a recorded fail()"
			else
				fail "17i: a row with a fail() gave rc=$_RC (want 1) out=$_OUT"
			fi
			if printf '%s\n' "$_OUT" | grep -qF '=== Results: 1 passed, 1 failed ==='; then
				pass "17i: the observed count includes fails — 1 pass + 1 fail meets a floor of 2"
			else
				fail "17i: the tally line does not read '1 passed, 1 failed' out=$_OUT"
			fi
			if printf '%s\n' "$_ERR" | grep -qF 'MIN_ASSERTIONS'; then
				fail "17i: a met floor still emitted a floor diagnostic — the row failed for the wrong reason err=$_ERR"
			else
				pass "17i: a met floor emits no floor diagnostic; the fail() alone is the reason"
			fi

			# --- 17k: a genuine failure is not re-summarised as a floor breach ---
		# A row that fails at step 2 of 25 has already failed. Emitting
		# "only 1 assertion(s) ran but MIN_ASSERTIONS=25" as the LAST stderr
		# line would restate a real defect as a coverage complaint — on the
		# big rows that is what EVERY early failure would look like. So the
		# shortfall arm is gated on FAIL_COUNT; the declaration-validation
		# arms are not, because a malformed declaration is its own defect.
		_unit_reset_markers "$MFLOOR"
		_unit_run "$FIXFLOOR" "$MFLOOR" _unit_floor_shortfail
		if [ "$_RC" -eq 1 ]; then
			pass "17k: a row that fails far short of its floor still fails"
		else
			fail "17k: a failing short row gave rc=$_RC (want 1) out=$_OUT"
		fi
		if printf '%s\n' "$_ERR" | grep -qF 'MIN_ASSERTIONS'; then
			fail "17k: the real failure was re-summarised as a floor breach err=$_ERR"
		else
			pass "17k: no floor diagnostic competes with the row's own failure"
		fi

		# --- 17h: attributability baseline ----------------------------------
			# NOT a positive or negative control in the QUM-1154 sense (its subject
			# has the defect and the probe must stay QUIET, which is neither
			# direction). It is a baseline: the row re-defines the PRE-FIX
			# aggregator over the lib's and asserts nothing, and MUST come back
			# PASS. That is what makes 17c-17g's red attributable to the aggregator
			# rather than to some accident of the fixture harness — without it,
			# 17d is unfalsifiable.
			#
			# It also pins, deliberately, that enforcement is LIB-LOCAL and a row
			# can shadow it. A row can already bypass any gate by returning 0 (see
			# [16c] on deliberate bypass); what the floor closes is the accident.
			# If enforcement is ever moved into scripts/e2e-matrix.sh, this case
			# must be re-designed, not deleted.
			_unit_reset_markers "$MFLOOR"
			_unit_run "$FIXFLOOR" "$MFLOOR" _unit_floor_baseline
			if [ "$_RC" -eq 0 ]; then
				pass "17h: baseline — the PRE-FIX aggregator still reports a zero-assertion row as PASS"
			else
				fail "17h: baseline did not reproduce the defect (rc=$_RC); 17c-17g are not attributable err=$_ERR"
			fi
		fi

		case "$FIXFLOOR" in
			"$UNIT_TMP_ROOT"/*) rm -rf "$FIXFLOOR" ;;
			*) note "refusing to remove fixture dir '$FIXFLOOR' (not under $UNIT_TMP_ROOT/)" ;;
		esac
	fi
fi

# --- 17j: every real row declares a floor AND can be reached by it -----------
# Three fixed assertions, not three per row, so the suite's own floor stays
# stable as rows are added.
#
# The e2e_print_results check is the one that is easy to leave out and is
# load-bearing: a per-row floor STRUCTURALLY CANNOT REACH a row that never
# calls the aggregator (scripts/e2e-tests/merge-reuse.sh was exactly that — no
# pass/fail, no aggregator, verdict resting entirely on return codes). Without
# it, pasting a MIN_ASSERTIONS line at the top of such a row makes it look
# accounted-for while the floor remains unreachable, which is this bug one
# level up. The first assertion is the vacuity guard: a glob matching nothing
# would otherwise report "every row declares a floor" having checked none.
_floor_rows_seen=0
_floor_rows_missing=""
_floor_rows_unreachable=""
for _r in "$REPO_ROOT"/scripts/e2e-tests/*.sh; do
	[ -e "$_r" ] || continue
	_floor_rows_seen=$((_floor_rows_seen + 1))
	# Counted, not just matched: a SECOND declaration later in the file would
	# win at source time and go unnoticed, so "declares a floor" has to mean
	# exactly one.
	if [ "$(grep -cE '^MIN_ASSERTIONS=[1-9][0-9]*$' "$_r")" -ne 1 ]; then
		_floor_rows_missing="$_floor_rows_missing ${_r##*/}"
	fi
	# `^[^#]*` requires the token to be reached without crossing a `#`, so a
	# row that merely MENTIONS the aggregator in a comment — including the
	# comment an author writes to explain why their row doesn't call it — does
	# not satisfy this.
	if ! grep -qE '^[^#]*e2e_print_results' "$_r"; then
		_floor_rows_unreachable="$_floor_rows_unreachable ${_r##*/}"
	fi
done
if [ "$_floor_rows_seen" -gt 0 ]; then
	pass "17j: found $_floor_rows_seen row(s) under scripts/e2e-tests/ to check"
else
	fail "17j: no rows found under scripts/e2e-tests/ — the checks below would be vacuous"
fi
if [ -z "$_floor_rows_missing" ]; then
	pass "17j: every row declares exactly one top-level MIN_ASSERTIONS=<positive integer>"
else
	fail "17j: row(s) declare no MIN_ASSERTIONS floor, or more than one:$_floor_rows_missing"
fi
if [ -z "$_floor_rows_unreachable" ]; then
	pass "17j: every row calls e2e_print_results, so the declared floor can actually reach it"
else
	fail "17j: row(s) never call e2e_print_results, so their floor is unreachable:$_floor_rows_unreachable"
fi

# ----------------------------------------------------------------------------
# 18. QUM-957: capture_pane must not swallow a tmux failure
# ----------------------------------------------------------------------------
echo "[18] QUM-957 capture fault: an unreachable pane cannot satisfy an absence assertion"

# The pre-fix helper was
#     capture_pane() { _stmux capture-pane -t "$1" -p 2>/dev/null || true; }
# which threw away BOTH halves of the evidence: `2>/dev/null` the diagnostic and
# `|| true` the status. A capture against a dead session therefore returned
# empty-stdout-with-exit-0, and every "pattern must NOT appear" verdict in the
# harness was satisfied by there being no pane to look at.
#
# Two mechanism facts drive the design these arms pin, and both were measured
# rather than assumed:
#
#   1. scripts/e2e-matrix.sh runs each row as `if run_row ...`, and an `if`
#      condition suppresses `set -e` for everything inside it. A bare
#      `return 1` from capture_pane aborts nothing.
#   2. Nearly every call site is `capture_pane X | grep -q PAT`. In a pipeline
#      the LHS's status is DISCARDED, and even an `exit` from the LHS kills only
#      that pipeline element — grep still sees EOF and the pipeline's status is
#      grep's. So neither a status nor a hard exit closes the hole.
#
# Hence the fault is recorded in a FILE (a subshell cannot assign to its
# parent's variable but can append to an inherited path) and the aggregator —
# the one function with unconditional authority to fail a row, reached on every
# non-crash path — fails the row on a non-empty ledger. 18c is that arm and is
# the headline of this section.
#
# Equally load-bearing, and the reason a naive fix regresses: an EMPTY PANE IS
# NOT A FAULT. If capture failure and empty output are collapsed, a site that
# legitimately reads a blank pane starts failing, someone re-adds `|| true`, and
# the defect returns blessed by a commit. 18d/18e/18f are the arms that keep
# empty-but-live silent, and 18n is the guard that turns `make validate` red if
# the swallowed idiom is ever reintroduced anywhere under scripts/.

CAPLIB="$REPO_ROOT/scripts/lib/capture-pane.sh"

# Fake tmux, keyed on $UNIT_FAKETMUX_MODE so one shim covers every arm:
#   dead  — no reachable server/session: capture-pane and has-session both fail
#   empty — LIVE session whose pane is legitimately blank (exit 0, no stdout)
#   text  — live session with content
#
# Real tmux is deliberately not used here. Makefile's test-e2e-matrix-unit is in
# `validate` precisely because it needs neither claude nor tmux, and section
# [16] re-runs this whole suite once per debug seam, so anything touching a real
# server would run 3x and drag timing into the merge gate. The arms that
# genuinely require a real live pane live in
# scripts/e2e-tests/capture-pane-liveness.sh, which declares needs_tmux=1.
#
# The shim parses `-t <session>` rather than indexing $3: the plain, `-e` (ANSI)
# and `-S` (scrollback) capture forms put the session at different positions, and
# an arm that reported the wrong session name would defeat the print-your-subject
# rule these tests exist to enforce.
#
# It also prints UNIT_FAKETMUX_TOKEN on every invocation, which 18-fixture keys
# on. Without that, "dead mode exits 1" is equally satisfied by the REAL tmux
# finding no server, and every positive arm below would be riding on a shim that
# was never actually on PATH.
_unit_mk_faketmux() {
	local dir=$1
	mkdir -p "$dir/bin" || return 1
	cat >"$dir/bin/tmux" <<'FAKETMUX' || return 1
#!/usr/bin/env bash
mode=${UNIT_FAKETMUX_MODE:-dead}
if [ "${1:-}" = "-L" ]; then shift 2; fi
cmd=${1:-}
session=""
ansi=no
scrollback=""
prev=""
for a in "$@"; do
	case "$a" in
		-e) ansi=yes ;;
	esac
	if [ "$prev" = "-t" ]; then session=$a; fi
	if [ "$prev" = "-S" ]; then scrollback=$a; fi
	prev=$a
done
echo "UNIT_FAKETMUX_TOKEN mode=$mode cmd=$cmd session=$session ansi=$ansi scrollback=$scrollback" >&2
case "$mode:$cmd" in
	dead:*)
		echo "can't find session: $session" >&2
		exit 1
		;;
	*:has-session) exit 0 ;;
	empty:capture-pane) exit 0 ;;
	text:capture-pane)
		if [ "$ansi" = yes ]; then printf 'UNIT_ANSI_MARKER\n'; fi
		if [ -n "$scrollback" ]; then printf 'UNIT_SCROLLBACK_MARKER %s\n' "$scrollback"; fi
		printf 'UNIT_PANE_MARKER line one\nsecond line\n'
		exit 0
		;;
	*) exit 0 ;;
esac
FAKETMUX
	chmod +x "$dir/bin/tmux" || return 1
}

# Run a snippet in a fresh bash with the shim on PATH and the lib sourced,
# capturing stdout/stderr/rc separately into _RC/_OUT/_ERR.
# $1=mode, $2=ledger path ("" leaves E2E_CAPTURE_FAULT_FILE UNSET), $3=snippet.
#
# The snippet goes to a FILE rather than `bash -c`: these snippets contain both
# quote styles and a command substitution (18j's injection payload), and nesting
# them into a -c string is exactly where such a payload gets mis-escaped and the
# arm silently stops testing what it names.
#
# _CRASHED is set when the probe could not even run its snippet — a failed
# source (99) or a missing function (127). Every arm below that asserts the
# ABSENCE of a string must consult it: absence is also what a probe that printed
# nothing produces, so without this a crash reads as a pass on exactly the arms
# whose whole purpose is to catch a vacuous pass.
_unit_cap_probe() {
	local mode=$1 ledger=$2 snippet=$3
	local sf of ef
	sf=$(mktemp "$UNIT_TMP_ROOT/e2e-cap-probe.XXXXXX") || return 1
	of=$(mktemp "$UNIT_TMP_ROOT/e2e-cap-out.XXXXXX") || return 1
	ef=$(mktemp "$UNIT_TMP_ROOT/e2e-cap-err.XXXXXX") || return 1
	{
		printf '%s\n' 'set -uo pipefail'
		printf '. %q || exit 99\n' "$LIB"
		printf '%s\n' "$snippet"
	} >"$sf"
	env "PATH=$UNIT_CAP_FIX/bin:$PATH" \
		"UNIT_FAKETMUX_MODE=$mode" \
		${ledger:+"E2E_CAPTURE_FAULT_FILE=$ledger"} \
		"UNIT_MARKER_DIR=$UNIT_CAP_FIX/markers" \
		"TMPDIR=$UNIT_CAP_FIX" \
		"SPRAWL_TMUX_SOCKET=unit-fake-socket" \
		"$UNIT_CAP_BASH" "$sf" >"$of" 2>"$ef"
	_RC=$?
	_OUT=$(cat "$of")
	_ERR=$(cat "$ef")
	_CRASHED=no
	case "$_RC" in
		99 | 127) _CRASHED=yes ;;
	esac
	if printf '%s\n' "$_ERR" | grep -qE ': line [0-9]+: .*(command not found|unbound variable)'; then
		_CRASHED=yes
	fi
	rm -f -- "$sf" "$of" "$ef"
}

# As _unit_cap_probe, but presenting an inherited ledger OWNER ($3), the way a
# script invoked from inside another capture-using script would see it.
_unit_cap_probe_owned() {
	local mode=$1 ledger=$2 owner=$3 snippet=$4
	local sf of ef
	sf=$(mktemp "$UNIT_TMP_ROOT/e2e-cap-probe.XXXXXX") || return 1
	of=$(mktemp "$UNIT_TMP_ROOT/e2e-cap-out.XXXXXX") || return 1
	ef=$(mktemp "$UNIT_TMP_ROOT/e2e-cap-err.XXXXXX") || return 1
	{
		printf '%s\n' 'set -uo pipefail'
		printf '. %q || exit 99\n' "$LIB"
		printf '%s\n' "$snippet"
	} >"$sf"
	env "PATH=$UNIT_CAP_FIX/bin:$PATH" \
		"UNIT_FAKETMUX_MODE=$mode" \
		"E2E_CAPTURE_FAULT_FILE=$ledger" \
		"E2E_CAPTURE_LEDGER_OWNER=$owner" \
		"UNIT_MARKER_DIR=$UNIT_CAP_FIX/markers" \
		"TMPDIR=$UNIT_CAP_FIX" \
		"SPRAWL_TMUX_SOCKET=unit-fake-socket" \
		"$UNIT_CAP_BASH" "$sf" >"$of" 2>"$ef"
	_RC=$?
	_OUT=$(cat "$of")
	_ERR=$(cat "$ef")
	_CRASHED=no
	case "$_RC" in
		99 | 127) _CRASHED=yes ;;
	esac
	rm -f -- "$sf" "$of" "$ef"
}

# As _unit_cap_probe, but with NOTHING writable: both the requested ledger and
# the TMPDIR the fallback is derived from point at $1 (a mode-500 dir). That is
# the only way to reach the lib's "no writable ledger" state — pointing just
# E2E_CAPTURE_FAULT_FILE at an unwritable path takes the FALLBACK, which
# succeeds, so an arm written that way proves the fallback works and nothing more.
_unit_cap_probe_nowrite() {
	local nowrite=$1 snippet=$2
	local sf of ef
	# The script and the output files must live somewhere writable — only the
	# LEDGER paths are being denied, not the fixture harness.
	sf=$(mktemp "$UNIT_TMP_ROOT/e2e-cap-probe.XXXXXX") || return 1
	of=$(mktemp "$UNIT_TMP_ROOT/e2e-cap-out.XXXXXX") || return 1
	ef=$(mktemp "$UNIT_TMP_ROOT/e2e-cap-err.XXXXXX") || return 1
	{
		printf '%s\n' 'set -uo pipefail'
		printf '. %q || exit 99\n' "$LIB"
		printf '%s\n' "$snippet"
	} >"$sf"
	env "PATH=$UNIT_CAP_FIX/bin:$PATH" \
		"UNIT_FAKETMUX_MODE=dead" \
		"E2E_CAPTURE_FAULT_FILE=$nowrite/ledger" \
		"UNIT_MARKER_DIR=$UNIT_CAP_FIX/markers" \
		"TMPDIR=$nowrite" \
		"SPRAWL_TMUX_SOCKET=unit-fake-socket" \
		"$UNIT_CAP_BASH" "$sf" >"$of" 2>"$ef"
	_RC=$?
	_OUT=$(cat "$of")
	_ERR=$(cat "$ef")
	_CRASHED=no
	case "$_RC" in
		99 | 127) _CRASHED=yes ;;
	esac
	rm -f -- "$sf" "$of" "$ef"
}

# Assert $1 contains (want=yes) or does not contain (want=no) fixed string $2.
# NEEDLES MUST BE SINGLE-LINE: $1 is fed through grep line by line, so a needle
# containing a newline can never match — which would make a want=no arm pass
# unconditionally, the exact failure mode this section exists to catch.
_unit_cap_has() {
	local hay=$1 needle=$2 want=$3 desc=$4
	if printf '%s\n' "$hay" | grep -qF -- "$needle"; then
		if [ "$want" = yes ]; then pass "$desc"; else fail "$desc (found '$needle') text=$hay"; fi
	else
		if [ "$want" = no ]; then pass "$desc"; else fail "$desc (no '$needle') text=$hay"; fi
	fi
}

# Assert on a `LABEL=<rc>` marker line the probe snippet echoed.
# $1=text, $2=label, $3=zero|nonzero|<literal>, $4=description.
#
# Why this exists rather than `_unit_cap_has "$_OUT" 'X_RC=0' no`: a probe that
# CRASHED prints no marker at all, and "does not contain X_RC=0" is then
# satisfied by silence. Every rc arm therefore proves the marker is PRESENT
# first and judges its value second, and reports a missing marker as its own
# distinct failure rather than as a pass.
_unit_cap_rc() {
	local hay=$1 label=$2 want=$3 desc=$4
	# A crashed probe proves nothing in EITHER direction. `want=nonzero` is the
	# trap: bash's own 127 for a missing function satisfies it perfectly, so
	# every "returns nonzero on a dead session" arm would pass against a lib
	# that does not exist — and would keep passing against a stub that does
	# nothing. This guard is what makes those arms discriminate.
	if [ "${_CRASHED:-no}" = yes ]; then
		fail "$desc (probe crashed before it could exercise the code under test; rc=$_RC err=$_ERR)"
		return 1
	fi
	local line
	line=$(printf '%s\n' "$hay" | grep -oE "$label=[0-9]+" | head -1)
	if [ -z "$line" ]; then
		fail "$desc (no '$label=<rc>' marker — the probe never got that far; rc=$_RC crashed=$_CRASHED err=$_ERR)"
		return 1
	fi
	local got=${line#*=}
	case "$want" in
		zero) if [ "$got" -eq 0 ]; then pass "$desc"; else fail "$desc (got $label=$got, want 0) out=$hay err=$_ERR"; fi ;;
		nonzero) if [ "$got" -ne 0 ]; then pass "$desc"; else fail "$desc (got $label=$got, want nonzero) out=$hay"; fi ;;
		*) if [ "$got" = "$want" ]; then pass "$desc"; else fail "$desc (got $label=$got, want $want) out=$hay err=$_ERR"; fi ;;
	esac
}

# As _unit_cap_has with want=no, but refuses to call a crashed probe a pass.
_unit_cap_lacks() {
	local hay=$1 needle=$2 desc=$3
	if [ "${_CRASHED:-no}" = yes ]; then
		fail "$desc (probe crashed, so absence proves nothing; rc=$_RC err=$_ERR)"
		return 1
	fi
	_unit_cap_has "$hay" "$needle" no "$desc"
}

UNIT_CAP_BASH=$(command -v bash)
UNIT_CAP_FIX=$(mktemp -d "$UNIT_TMP_ROOT/e2e-matrix-unit-cap.XXXXXX" 2>/dev/null)
if [ ! -r "$LIB" ]; then
	fail "18: scripts/lib/e2e-common.sh not readable — QUM-957 arms not run"
elif [ -z "$UNIT_CAP_FIX" ] || [ ! -d "$UNIT_CAP_FIX" ]; then
	fail "18: could not mktemp the QUM-957 fixture dir — arms not run"
elif ! _unit_mk_faketmux "$UNIT_CAP_FIX" || ! mkdir -p "$UNIT_CAP_FIX/markers"; then
	fail "18: could not build the fake-tmux fixture — arms not run"
else
	CAPLEDGER="$UNIT_CAP_FIX/fault-ledger"

	# Prove the fixture itself works before trusting any arm built on it.
	# Identity, not behaviour: "dead mode exits 1" is equally true of the real
	# tmux with no server running, so the token is what distinguishes a shim
	# that is genuinely on PATH from one that silently is not.
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" 'tmux -L x capture-pane -t unit-fixture-check -p; echo "FIXTURE_RC=$?"'
	_unit_cap_has "$_ERR" 'UNIT_FAKETMUX_TOKEN' yes "18-fixture: the tmux on PATH is the unit shim, not the real binary"
	_unit_cap_has "$_ERR" 'session=unit-fixture-check' yes "18-fixture: the shim resolves -t <session> correctly"
	_unit_cap_rc "$_OUT" FIXTURE_RC 1 "18-fixture: the shim fails in dead mode"
	_unit_cap_probe empty "$CAPLEDGER" 'tmux -L x capture-pane -t unit-fixture-check -p; echo "FIXTURE_RC=$?"'
	_unit_cap_rc "$_OUT" FIXTURE_RC 0 "18-fixture: the shim succeeds in empty mode"

	# --- 18a POSITIVE CONTROL (defect present -> probe MUST fire) ----------
	# Dead session: capture_pane must report the failure in its status.
	# Pre-fix this returned 0 because of `|| true`.
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" \
		'capture_pane no-such-session-18a >/dev/null; echo "CAP_RC=$?"'
	_unit_cap_rc "$_OUT" CAP_RC nonzero "18a POSITIVE CONTROL: capture_pane against a dead session returns nonzero"

	# --- 18b POSITIVE CONTROL: it must PRINT ITS SUBJECT, not just a verdict
	# The resolved session name, the actual tmux exit status, and tmux's own
	# diagnostic. Pre-fix all three were discarded (`2>/dev/null`).
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" 'capture_pane no-such-session-18b >/dev/null || true'
	_unit_cap_has "$_ERR" 'no-such-session-18b' yes "18b POSITIVE CONTROL: the fault diagnostic names the resolved session"
	_unit_cap_has "$_ERR" 'tmux exit' yes "18b POSITIVE CONTROL: the fault diagnostic reports the actual tmux exit status"
	_unit_cap_has "$_ERR" "can't find session" yes "18b POSITIVE CONTROL: tmux's own stderr survives instead of going to /dev/null"
	_unit_cap_has "$_ERR" 'unit-fake-socket' yes "18b POSITIVE CONTROL: the fault diagnostic names the tmux socket it used"
	# tmux's stderr must reach the OPERATOR's stderr, never the caller's stdout.
	# A `2>&1`-shaped fix would turn "can't find session" into what looks like
	# pane content, and a presence assertion somewhere would then match text
	# tmux invented about its own failure.
	_unit_cap_lacks "$_OUT" "can't find session" "18b POSITIVE CONTROL: tmux's error text does NOT leak into stdout as fake pane content"

	# --- 18c POSITIVE CONTROL, THE HEADLINE -------------------------------
	# The vacuous green, reproduced exactly: an absence assertion phrased as
	# `capture_pane X | grep -q PAT`, which passes because the pane is
	# unreadable, and a row that then reports all-green. The pipeline discards
	# capture_pane's status, so only the aggregator can catch this.
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" '
MIN_ASSERTIONS=1
if capture_pane no-such-session-18c | grep -q "Session Error"; then
	fail "18c fixture: banner present"
else
	pass "18c fixture: no error banner on screen"
fi
e2e_print_results
echo "AGG_RC=$?"'
	_unit_cap_rc "$_OUT" AGG_RC nonzero "18c POSITIVE CONTROL: a row whose only assertion was satisfied by an unreadable pane does NOT pass"
	_unit_cap_has "$_ERR" 'FAIL' yes "18c POSITIVE CONTROL: the aggregator records the capture fault as a FAIL"
	_unit_cap_has "$_ERR" 'no-such-session-18c' yes "18c POSITIVE CONTROL: the aggregator's breach names the session that could not be read"
	# Distinct message from the QUM-1029 floor breach: a reader must never be
	# told "the row measured less than it claims" when the truth is "tmux was
	# unreachable". Those are different diagnoses with different remedies.
	_unit_cap_lacks "$_ERR" 'MIN_ASSERTIONS' "18c: the capture-fault breach is not mis-reported as a MIN_ASSERTIONS shortfall"

	# --- 18d NEGATIVE CONTROL (subject known clean -> probe MUST stay quiet)
	# A LIVE session with a legitimately empty pane. This is the arm that
	# protects against the `|| true` regression: if it goes red, someone
	# collapsed "tmux succeeded and the pane is empty" into "tmux failed".
	: >"$CAPLEDGER"
	_unit_cap_probe empty "$CAPLEDGER" '
MIN_ASSERTIONS=1
out=$(capture_pane live-but-blank-18d); rc=$?
echo "CAP_RC=$rc CAP_BYTES=${#out}"
pass "18d fixture: assertion made"
e2e_print_results
echo "AGG_RC=$?"'
	_unit_cap_rc "$_OUT" CAP_RC zero "18d NEGATIVE CONTROL: an empty pane on a LIVE session returns 0"
	_unit_cap_rc "$_OUT" CAP_BYTES 0 "18d NEGATIVE CONTROL: an empty live pane yields empty output, not fabricated content"
	_unit_cap_lacks "$_ERR" 'CAPTURE FAULT' "18d NEGATIVE CONTROL: an empty-but-live pane emits no fault diagnostic"
	_unit_cap_rc "$_OUT" AGG_RC zero "18d NEGATIVE CONTROL: an empty-but-live pane does not fail the row"
	if [ -s "$CAPLEDGER" ]; then
		fail "18d NEGATIVE CONTROL: an empty-but-live pane wrote to the fault ledger: $(cat "$CAPLEDGER")"
	else
		pass "18d NEGATIVE CONTROL: an empty-but-live pane leaves the fault ledger empty"
	fi

	# --- 18e NEGATIVE CONTROL: content passes through unchanged -----------
	# stdout must not be rerouted through a variable that strips trailing blank
	# pane rows, and tmux's stderr must not be folded into stdout.
	: >"$CAPLEDGER"
	_unit_cap_probe text "$CAPLEDGER" 'capture_pane live-with-text-18e; echo "CAP_RC=$?"'
	_unit_cap_has "$_OUT" 'UNIT_PANE_MARKER line one' yes "18e NEGATIVE CONTROL: pane content reaches stdout unchanged"
	_unit_cap_has "$_OUT" 'second line' yes "18e NEGATIVE CONTROL: every captured line reaches stdout, not just the first"
	_unit_cap_rc "$_OUT" CAP_RC zero "18e NEGATIVE CONTROL: a successful capture returns 0"
	_unit_cap_lacks "$_OUT" 'UNIT_FAKETMUX_TOKEN' "18e NEGATIVE CONTROL: tmux's stderr is not folded into the captured content"

	# --- 18f capture_pane_ansi: same contract, both directions ------------
	# Two rows (pending-dim-bright.sh, qum1000-refused-slash.sh) carried their
	# own `capture-pane -e -p 2>/dev/null || true` copies, and both use it for
	# ABSENCE-of-an-attribute verdicts — the same defect, invisible to any fix
	# that only touches capture_pane. The `-e` flag is asserted to reach tmux so
	# capture_pane_ansi cannot be a silent alias that drops it.
	: >"$CAPLEDGER"
	_unit_cap_probe text "$CAPLEDGER" 'capture_pane_ansi live-18f; echo "ANSI_RC=$?"'
	_unit_cap_rc "$_OUT" ANSI_RC zero "18f NEGATIVE CONTROL: capture_pane_ansi returns 0 on a live pane"
	_unit_cap_has "$_OUT" 'UNIT_ANSI_MARKER' yes "18f NEGATIVE CONTROL: capture_pane_ansi passes -e through to tmux"
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" 'capture_pane_ansi dead-18f >/dev/null; echo "ANSI_RC=$?"'
	_unit_cap_rc "$_OUT" ANSI_RC nonzero "18f POSITIVE CONTROL: capture_pane_ansi against a dead session returns nonzero"
	if [ -s "$CAPLEDGER" ]; then
		pass "18f POSITIVE CONTROL: a faulting capture_pane_ansi records the fault in the ledger"
	else
		fail "18f POSITIVE CONTROL: capture_pane_ansi faulted but wrote no ledger entry, so the row would still read green"
	fi

	# --- 18g capture_pane_scrollback -------------------------------------
	# paste-coalesce (row and legacy driver) captures scrollback with
	# `-p -S -200` inline, bypassing capture_pane entirely. Without a shared
	# form for it those two sites keep the swallow.
	: >"$CAPLEDGER"
	_unit_cap_probe text "$CAPLEDGER" 'capture_pane_scrollback live-18g 200; echo "SB_RC=$?"'
	_unit_cap_rc "$_OUT" SB_RC zero "18g NEGATIVE CONTROL: capture_pane_scrollback returns 0 on a live pane"
	_unit_cap_has "$_OUT" 'UNIT_SCROLLBACK_MARKER -200' yes "18g NEGATIVE CONTROL: capture_pane_scrollback passes -S -<lines> through to tmux"
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" 'capture_pane_scrollback dead-18g 200 >/dev/null; echo "SB_RC=$?"'
	_unit_cap_rc "$_OUT" SB_RC nonzero "18g POSITIVE CONTROL: capture_pane_scrollback against a dead session returns nonzero"

	# --- 18h the sanctioned opt-out --------------------------------------
	# Teardown diagnostics capture panes they have just killed on purpose. They
	# need a NAMED opt-out, because the alternative an author reaches for is
	# `|| true`, and that is the defect. capture_pane_best_effort is quiet and
	# ledger-free by contract. The rc marker plus _CRASHED is what keeps a stub
	# that does nothing from satisfying this.
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" '
MIN_ASSERTIONS=1
out=$(capture_pane_best_effort already-killed-18h); rc=$?
echo "BE_RC=$rc BE_BYTES=${#out}"
pass "18h fixture: assertion made"
e2e_print_results
echo "AGG_RC=$?"'
	_unit_cap_rc "$_OUT" BE_RC zero "18h: capture_pane_best_effort tolerates a dead session by contract"
	_unit_cap_rc "$_OUT" BE_BYTES 0 "18h: capture_pane_best_effort yields empty output for a dead session"
	_unit_cap_rc "$_OUT" AGG_RC zero "18h: capture_pane_best_effort does not fail the row"
	if [ -s "$CAPLEDGER" ]; then
		fail "18h: capture_pane_best_effort wrote to the fault ledger: $(cat "$CAPLEDGER")"
	else
		pass "18h: capture_pane_best_effort leaves the fault ledger empty"
	fi
	# ...and it must still be a real capture on a live pane, not a stub.
	: >"$CAPLEDGER"
	_unit_cap_probe text "$CAPLEDGER" 'capture_pane_best_effort live-18h; echo "BE_RC=$?"'
	_unit_cap_has "$_OUT" 'UNIT_PANE_MARKER line one' yes "18h NEGATIVE CONTROL: capture_pane_best_effort still returns real content from a live pane"

	# --- 18h2 capture_pane_dump: the forensic form, now at 51 call sites ----
	# 51 is measured, not estimated (this comment said "~45" first and was wrong):
	#   grep -rn 'capture_pane_dump' scripts --include='*.sh' \
	#     | grep -vE '^[^:]*:[0-9]+:[[:space:]]*#' \
	#     | grep -v '/lib/capture-pane.sh:' | grep -v 'test-e2e-matrix-unit.sh:' | wc -l
	# It replaced `capture_pane "$S" | tail -N >&2`, which under `set -euo
	# pipefail` killed the driver AT THE DUMP once capture_pane went loud —
	# skipping the summary and the capture-fault gate that would have explained
	# the abort. So the contract is: never fails, never faults, and SAYS when
	# there was no pane rather than printing nothing and letting the reader
	# assume an empty one.
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" '
set -e
capture_pane_dump dead-18h2 20
echo "DUMP_RC=$?"'
	_unit_cap_rc "$_OUT" DUMP_RC zero "18h2 POSITIVE CONTROL: capture_pane_dump on a dead session returns 0 under set -e (it cannot abort the driver at a forensic dump)"
	_unit_cap_has "$_ERR" 'nothing captured' yes "18h2 POSITIVE CONTROL: a dump with no pane SAYS so, instead of printing nothing and reading as an empty pane"
	if [ -s "$CAPLEDGER" ]; then
		fail "18h2: capture_pane_dump wrote to the fault ledger — a forensic dump on an already-failed path must not add a verdict: $(cat "$CAPLEDGER")"
	else
		pass "18h2: capture_pane_dump leaves the fault ledger empty"
	fi
	: >"$CAPLEDGER"
	_unit_cap_probe text "$CAPLEDGER" '
set -e
capture_pane_dump live-18h2 20
echo "DUMP_RC=$?"'
	_unit_cap_has "$_ERR" 'UNIT_PANE_MARKER line one' yes "18h2 NEGATIVE CONTROL: capture_pane_dump sends real pane content to stderr"
	_unit_cap_lacks "$_OUT" 'UNIT_PANE_MARKER line one' "18h2 NEGATIVE CONTROL: the dump goes to stderr, not stdout (it must not be mistaken for captured content)"

	# --- 18i e2e_require_session_alive, both directions -------------------
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" \
		'e2e_require_session_alive dead-sess-18i; echo "ALIVE_RC=$?"'
	_unit_cap_rc "$_OUT" ALIVE_RC nonzero "18i POSITIVE CONTROL: e2e_require_session_alive returns nonzero for a dead session"
	_unit_cap_has "$_ERR" 'FAIL' yes "18i POSITIVE CONTROL: e2e_require_session_alive records a fail() for a dead session"
	_unit_cap_has "$_OUT" 'has-session exit=' yes "18i: the liveness probe prints its subject (the actual has-session status)"
	: >"$CAPLEDGER"
	_unit_cap_probe empty "$CAPLEDGER" \
		'e2e_require_session_alive live-sess-18i; echo "ALIVE_RC=$?"'
	_unit_cap_rc "$_OUT" ALIVE_RC zero "18i NEGATIVE CONTROL: e2e_require_session_alive returns 0 for a live session"
	_unit_cap_has "$_OUT" 'PASS' yes "18i NEGATIVE CONTROL: e2e_require_session_alive records a pass() for a live session"

	# --- 18j e2e_pane_lacks: absence is only provable on a readable pane ---
	# All three directions. The first is the one the whole issue is about:
	# "the pattern is absent" and "I could not look" must not be the same
	# verdict.
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" \
		'e2e_pane_lacks dead-sess-18j "Session Error" "no error banner"; echo "LACKS_RC=$?"'
	_unit_cap_rc "$_OUT" LACKS_RC nonzero "18j POSITIVE CONTROL: e2e_pane_lacks on a dead session FAILS rather than passing vacuously"
	# "absent" and "I could not look" must read differently to a human too, not
	# just differ in a status: the whole defect is a reader trusting the former
	# when the latter happened.
	_unit_cap_has "$_ERR" 'CANNOT JUDGE' yes "18j POSITIVE CONTROL: the dead-session verdict says absence is UNPROVEN, not proven"
	_unit_cap_has "$_OUT" 'capture exit=' yes "18j: the absence probe prints its subject before judging"
	: >"$CAPLEDGER"
	_unit_cap_probe empty "$CAPLEDGER" \
		'e2e_pane_lacks live-sess-18j "Session Error" "no error banner"; echo "LACKS_RC=$?"'
	_unit_cap_rc "$_OUT" LACKS_RC zero "18j NEGATIVE CONTROL: e2e_pane_lacks passes when the pane is live and the pattern absent"
	_unit_cap_has "$_OUT" 'PASS' yes "18j NEGATIVE CONTROL: e2e_pane_lacks records a pass() on a live pane with the pattern absent"
	: >"$CAPLEDGER"
	_unit_cap_probe text "$CAPLEDGER" \
		'e2e_pane_lacks live-sess-18j "UNIT_PANE_MARKER" "marker absent"; echo "LACKS_RC=$?"'
	_unit_cap_rc "$_OUT" LACKS_RC nonzero "18j: e2e_pane_lacks fails when the pattern IS present on a live pane"

	# --- 18k POSITIVE CONTROL: one fault, one diagnostic ------------------
	# A 240s wait_for_pattern against a dead session polls hundreds of times.
	# An unthrottled diagnostic would emit hundreds of blocks and bury the very
	# verdict it is trying to surface — a defect that reads as noise gets
	# ignored, so throttling is part of the fix, not a nicety. All 41 attempts'
	# stderr is captured (no `2>&1` inside the loop): throttling to the FIRST
	# fault would otherwise be indistinguishable from emitting nothing.
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" '
MIN_ASSERTIONS=1
i=0
while [ "$i" -lt 41 ]; do i=$((i + 1)); capture_pane repeat-sess-18k >/dev/null || true; done
pass "18k fixture: assertion made"
e2e_print_results
echo "AGG_RC=$?"'
	_unit_cap_rc "$_OUT" AGG_RC nonzero "18k POSITIVE CONTROL: 41 faulting captures still fail the row"
	_capblocks=$(printf '%s\n' "$_ERR" | grep -cF 'CAPTURE FAULT')
	if [ "${_capblocks:-0}" -eq 1 ]; then
		pass "18k: repeated faults on one session print exactly one diagnostic block (got $_capblocks)"
	else
		fail "18k: repeated faults on one session printed ${_capblocks:-0} diagnostic blocks, want 1"
	fi
	_caplines=$(grep -c . "$CAPLEDGER" 2>/dev/null)
	if [ "${_caplines:-0}" -eq 1 ]; then
		pass "18k: the fault ledger holds one line per faulting session, not one per attempt"
	else
		fail "18k: the fault ledger holds ${_caplines:-0} line(s) after 41 faults on one session, want 1"
	fi
	# Two distinct dead sessions must both be recorded — a throttle keyed on
	# "have I ever faulted" instead of "have I faulted for THIS session" would
	# hide the second, and a row that reads two panes would name only one.
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" '
capture_pane sess-alpha-18k >/dev/null || true
capture_pane sess-beta-18k >/dev/null || true'
	_unit_cap_has "$_ERR" 'sess-alpha-18k' yes "18k: the first faulting session is named"
	_unit_cap_has "$_ERR" 'sess-beta-18k' yes "18k: a second, DIFFERENT faulting session is also named (the throttle is per session)"

	# --- 18l POSITIVE CONTROL: a session name is never evaluated ----------
	# Session names reach this code from row scripts and from $SESSION-shaped
	# variables. An implementation that put one inside $(( )) or eval would
	# execute the payload; the marker it would drop is asserted absent. Mirrors
	# [17g]'s arithmetic-injection case.
	#
	# The paired positive control below is what makes the absence meaningful:
	# without it, a broken marker dir would satisfy this arm just as well as a
	# safe implementation.
	: >"$CAPLEDGER"
	rm -f -- "$UNIT_CAP_FIX/markers/cap-pwned"
	_unit_cap_probe dead "$CAPLEDGER" 'eval ": >\"\$UNIT_MARKER_DIR/cap-pwned\""; echo "CTRL_RC=$?"'
	if [ -e "$UNIT_CAP_FIX/markers/cap-pwned" ]; then
		pass "18l POSITIVE CONTROL: the injection detector fires when the payload really does run"
	else
		fail "18l POSITIVE CONTROL: the injection payload could not drop its marker at all, so 18l's absence check proves nothing"
	fi
	rm -f -- "$UNIT_CAP_FIX/markers/cap-pwned"
	_unit_cap_probe dead "$CAPLEDGER" \
		'capture_pane '"'"'x[$(: >"$UNIT_MARKER_DIR/cap-pwned")]'"'"' >/dev/null 2>&1 || true
e2e_pane_lacks '"'"'y[$(: >"$UNIT_MARKER_DIR/cap-pwned")]'"'"' zzz desc >/dev/null 2>&1 || true
e2e_require_session_alive '"'"'z[$(: >"$UNIT_MARKER_DIR/cap-pwned")]'"'"' >/dev/null 2>&1 || true
echo "INJ_DONE=0"'
	_unit_cap_rc "$_OUT" INJ_DONE 0 "18l: the injection probe ran to completion (so the absence check below is not vacuous)"
	if [ -e "$UNIT_CAP_FIX/markers/cap-pwned" ]; then
		fail "18l: a session name was EVALUATED — the injected command substitution ran"
	else
		pass "18l: a session name is never evaluated (no injection marker dropped)"
	fi

	# --- 18m wait_for_* must abort on fault, not burn the clock -----------
	# A dead session can never match, so polling to the deadline wastes the
	# timeout and buries the diagnostic under the eventual timeout message.
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" \
		'wait_for_pattern dead-sess-18m "anything" 4 >/dev/null 2>&1; echo "WFP_RC=$?"'
	_unit_cap_rc "$_OUT" WFP_RC 2 "18m POSITIVE CONTROL: wait_for_pattern aborts on a capture fault (rc 2) instead of polling to its deadline"
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" \
		'wait_for_pattern_fast dead-sess-18m "anything" 4 >/dev/null 2>&1; echo "WFPF_RC=$?"'
	_unit_cap_rc "$_OUT" WFPF_RC 2 "18m POSITIVE CONTROL: wait_for_pattern_fast aborts on a capture fault (rc 2)"
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" \
		'wait_for_substring_fast dead-sess-18m "anything" 4 >/dev/null 2>&1; echo "WFSF_RC=$?"'
	_unit_cap_rc "$_OUT" WFSF_RC 2 "18m POSITIVE CONTROL: wait_for_substring_fast aborts on a capture fault (rc 2)"
	: >"$CAPLEDGER"
	_unit_cap_probe text "$CAPLEDGER" \
		'wait_for_pattern live-18m "UNIT_PANE_MARKER" 5 >/dev/null 2>&1; echo "WFP_RC=$?"'
	_unit_cap_rc "$_OUT" WFP_RC zero "18m NEGATIVE CONTROL: wait_for_pattern still matches on a live pane"
	: >"$CAPLEDGER"
	_unit_cap_probe text "$CAPLEDGER" \
		'wait_for_pattern_fast live-18m "UNIT_PANE_MARKER" 5 >/dev/null 2>&1; echo "WFPF_RC=$?"'
	_unit_cap_rc "$_OUT" WFPF_RC zero "18m NEGATIVE CONTROL: wait_for_pattern_fast still matches on a live pane"
	: >"$CAPLEDGER"
	_unit_cap_probe text "$CAPLEDGER" \
		'wait_for_substring_fast live-18m "UNIT_PANE_MARKER" 5 >/dev/null 2>&1; echo "WFSF_RC=$?"'
	_unit_cap_rc "$_OUT" WFSF_RC zero "18m NEGATIVE CONTROL: wait_for_substring_fast still matches on a live pane"
	# A live pane that simply does not contain the pattern must still TIME OUT
	# (rc 1), not be reported as a fault. Collapsing "no match yet" into "could
	# not look" is the mirror image of the defect.
	: >"$CAPLEDGER"
	_unit_cap_probe empty "$CAPLEDGER" \
		'wait_for_pattern_fast live-18m "NEVER_APPEARS" 1 >/dev/null 2>&1; echo "WFPF_RC=$?"'
	_unit_cap_rc "$_OUT" WFPF_RC 1 "18m NEGATIVE CONTROL: a live pane with no match times out (rc 1), it is not reported as a fault"

	# --- 18n the ledger must work with E2E_CAPTURE_FAULT_FILE UNSET --------
	# The nine legacy scripts/test-*-e2e.sh drivers are the whole reason the
	# shared lib exists, and none of them will set this variable; several run
	# under `set -u`, where a bare dereference aborts the driver outright. So
	# the default path is part of the contract, not an implementation detail.
	_unit_cap_probe text "" '
out=$(capture_pane live-18n); rc=$?
echo "CAP_RC=$rc"
echo "LEDGER_SET=${E2E_CAPTURE_FAULT_FILE:+1}"'
	_unit_cap_rc "$_OUT" CAP_RC zero "18n NEGATIVE CONTROL: capture_pane works with E2E_CAPTURE_FAULT_FILE unset (the legacy drivers never set it)"
	_unit_cap_has "$_OUT" 'LEDGER_SET=1' yes "18n: the lib supplies a default ledger path when the caller sets none"
	_unit_cap_probe dead "" '
capture_pane dead-18n >/dev/null 2>&1 || true
if [ -s "$E2E_CAPTURE_FAULT_FILE" ]; then echo "DEFAULT_LEDGER_WRITTEN=1"; else echo "DEFAULT_LEDGER_WRITTEN=0"; fi
capture_pane_assert_no_faults >/dev/null 2>&1; echo "GATE_RC=$?"
rm -f -- "$E2E_CAPTURE_FAULT_FILE"'
	_unit_cap_has "$_OUT" 'DEFAULT_LEDGER_WRITTEN=1' yes "18n POSITIVE CONTROL: a fault is recorded at the default ledger path when the caller sets none"
	_unit_cap_rc "$_OUT" GATE_RC nonzero "18n POSITIVE CONTROL: capture_pane_assert_no_faults returns nonzero after a fault"
	_unit_cap_probe empty "" 'capture_pane_assert_no_faults >/dev/null 2>&1; echo "GATE_RC=$?"'
	_unit_cap_rc "$_OUT" GATE_RC zero "18n NEGATIVE CONTROL: capture_pane_assert_no_faults returns 0 when nothing faulted"

	# --- 18o one row's fault must not fail the next row -------------------
	# NOT via a per-process path, which would be the wrong mechanism to pin:
	# scripts/e2e-matrix.sh runs rows as `( . "$LIB"; ... )` SUBSHELLS, where
	# `$$` is the DRIVER's pid, so every row in one invocation derives the SAME
	# default ledger path — and the driver has no parallelism (no `&`, no `wait`,
	# no `xargs -P`), so that is fine. What actually protects row B from row A's
	# dead session is the TRUNCATE at source time, and that is what this pins.
	# The ledger path is passed EXPLICITLY, and that is the whole point: an
	# earlier draft of this arm let each probe derive its own default, where `$$`
	# differs per probe process — so the "leftover" was written to a path the
	# second probe never read and the arm passed under a mutation that removed
	# the truncation entirely. Sharing the path is what makes it a test.
	printf 'session=row-a-leftover-18o tmux_exit=1 form=capture-pane -p\n' >"$CAPLEDGER"
	_unit_cap_probe empty "$CAPLEDGER" '
MIN_ASSERTIONS=1
pass "18o fixture: row B asserted something"
e2e_print_results
echo "AGG_RC=$?"'
	_unit_cap_rc "$_OUT" AGG_RC zero "18o: a stale ledger from a previous row (or a recycled pid) does not fail the next row"
	_unit_cap_lacks "$_ERR" 'row-a-leftover-18o' "18o: the next row's output does not report a previous row's fault as its own"
	# ...but a LIVE owner's ledger must be appended to, not cleared: a nested
	# capture-using script that truncated its parent's ledger would hand the
	# parent a clean bill of health it did not earn. $$ here is this suite's own
	# pid, which is by definition alive.
	printf 'session=parent-fault-18o tmux_exit=1 form=capture-pane -p\n' >"$CAPLEDGER"
	_unit_cap_probe_owned dead "$CAPLEDGER" "$$" '
capture_pane nested-18o >/dev/null 2>&1 || true
echo "LEDGER_LINES=$(grep -c . "$E2E_CAPTURE_FAULT_FILE")"'
	_unit_cap_rc "$_OUT" LEDGER_LINES 2 "18o: a nested script APPENDS to a live owner's ledger instead of erasing the parent's faults"
	# An unrecordable fault is the sneakiest route back to the defect: it needs no
	# `|| true` anywhere, it just forgets. Two distinct cases, and the first was
	# initially mistaken for the second:
	#   (a) the REQUESTED path is unwritable -> fall back, still record, still fail
	#   (b) NOTHING is writable -> nothing can be recorded, so the run must be
	#       declared unprovable rather than clean.
	mkdir -p "$UNIT_CAP_FIX/nowrite" && chmod 500 "$UNIT_CAP_FIX/nowrite"
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$UNIT_CAP_FIX/nowrite/ledger" '
MIN_ASSERTIONS=1
capture_pane dead-18o >/dev/null 2>&1 || true
pass "18o fixture: assertion made"
e2e_print_results
echo "AGG_RC=$?"'
	_unit_cap_rc "$_OUT" AGG_RC nonzero "18o POSITIVE CONTROL (a): a fault whose REQUESTED ledger is unwritable still fails the row, via the fallback"
	_unit_cap_has "$_ERR" 'not writable' yes "18o POSITIVE CONTROL (a): the fallback is announced rather than silent"
	# (b) needs BOTH paths refused, so TMPDIR — which is where the fallback is
	# derived from — has to point at the unwritable dir too. Without this the
	# probe takes the fallback and the arm proves only what (a) already proved.
	# No assertion is made at all here: PASS_COUNT=0 with a met floor of 0 is
	# impossible, so the row is driven to the one state where nothing but the
	# ledger verdict can fail it.
	: >"$CAPLEDGER"
	_unit_cap_probe_nowrite "$UNIT_CAP_FIX/nowrite" '
MIN_ASSERTIONS=1
capture_pane dead-18o-b >/dev/null 2>&1 || true
pass "18o fixture: assertion made"
e2e_print_results
echo "AGG_RC=$?"'
	_unit_cap_rc "$_OUT" AGG_RC nonzero "18o POSITIVE CONTROL (b): with NO writable ledger anywhere, the run is failed rather than trusted"
	_unit_cap_has "$_ERR" 'cannot demonstrate' yes "18o POSITIVE CONTROL (b): the verdict says the run cannot be shown clean, not that it is clean"
	# ...and the same no-ledger state fails a run that never captured anything at
	# all. "I have no evidence" is not "I have good news": with no ledger there is
	# no way to tell a clean run from one whose faults were lost, so both fail.
	_unit_cap_probe_nowrite "$UNIT_CAP_FIX/nowrite" 'capture_pane_assert_no_faults >/dev/null 2>&1; echo "GATE_RC=$?"'
	_unit_cap_rc "$_OUT" GATE_RC nonzero "18o POSITIVE CONTROL (b): the gate fails on an unprovable run even when nothing faulted"
	chmod 700 "$UNIT_CAP_FIX/nowrite" 2>/dev/null

	# --- 18p e2e_capture_fault_reset: resettable, by exactly one row -------
	# scripts/e2e-tests/capture-pane-liveness.sh faults ON PURPOSE and so must
	# clear its own ledger before the aggregator. That escape hatch is the one
	# thing here that could launder a real fault, so it must clear only what has
	# already happened — a reset that permanently disarms the mechanism would be
	# the defect with a nicer name.
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" '
MIN_ASSERTIONS=1
capture_pane reset-sess-18p >/dev/null 2>&1 || true
e2e_capture_fault_reset
pass "18p fixture: assertion made"
e2e_print_results
echo "AGG_RC=$?"'
	_unit_cap_rc "$_OUT" AGG_RC zero "18p: e2e_capture_fault_reset clears the ledger so a deliberate fault does not fail its own row"
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" '
MIN_ASSERTIONS=1
capture_pane reset-sess-18p-first >/dev/null 2>&1 || true
e2e_capture_fault_reset
capture_pane reset-sess-18p-second >/dev/null 2>&1 || true
pass "18p fixture: assertion made"
e2e_print_results
echo "AGG_RC=$?"'
	_unit_cap_rc "$_OUT" AGG_RC nonzero "18p POSITIVE CONTROL: a fault AFTER a reset still fails the row (reset clears history, it does not disarm the gate)"

	# --- 18q2 the ledger must not litter a shared /tmp without bound -------
	# The ledger is created at SOURCE time — that is how its writability is
	# probed, and how a leftover from a recycled pid is neutralised — so a process
	# with no EXIT trap leaves a zero-byte file behind even when nothing faults.
	# One full run of this suite left 877 of them before the reaper existed, in a
	# /tmp CLAUDE.md says is shared with other agents and with host tooling.
	#
	# Lazy creation is deliberately NOT the fix: losing the cleanup costs a
	# zero-byte file, losing the ledger costs a verdict. So the litter is made
	# bounded by the live process set instead.
	#
	# Both directions matter equally here. Reaping a LIVE owner's ledger would
	# erase faults it has not yet reported — the defect this whole section exists
	# to stop, wearing housekeeping as a disguise.
	mkdir -p "$UNIT_CAP_FIX/reap"
	: >"$UNIT_CAP_FIX/reap/e2e-capture-fault.999999"
	: >"$UNIT_CAP_FIX/reap/e2e-capture-fault.999999.err.1"
	printf 'session=live-parent tmux_exit=1 form=x\n' >"$UNIT_CAP_FIX/reap/e2e-capture-fault.$$"
	: >"$UNIT_CAP_FIX/reap/unrelated-file"
	_unit_cap_probe empty "$UNIT_CAP_FIX/reap/e2e-capture-fault.self" 'echo "REAP_DONE=0"'
	_unit_cap_rc "$_OUT" REAP_DONE 0 "18q2: the reap probe ran (so the assertions below are not vacuous)"
	if [ -e "$UNIT_CAP_FIX/reap/e2e-capture-fault.999999" ]; then
		fail "18q2 POSITIVE CONTROL: a ledger whose owning pid is gone was NOT reaped — the litter grows without bound in a shared /tmp"
	else
		pass "18q2 POSITIVE CONTROL: a ledger whose owning pid is gone is reaped"
	fi
	if [ -e "$UNIT_CAP_FIX/reap/e2e-capture-fault.999999.err.1" ]; then
		fail "18q2 POSITIVE CONTROL: the dead owner's stderr spool was left behind"
	else
		pass "18q2 POSITIVE CONTROL: the dead owner's stderr spool is reaped with it"
	fi
	if [ -s "$UNIT_CAP_FIX/reap/e2e-capture-fault.$$" ]; then
		pass "18q2 NEGATIVE CONTROL: a LIVE owner's ledger and its recorded fault are untouched"
	else
		fail "18q2 NEGATIVE CONTROL: a live owner's ledger was reaped or emptied — that erases a verdict, not a temp file"
	fi
	if [ -e "$UNIT_CAP_FIX/reap/unrelated-file" ]; then
		pass "18q2 NEGATIVE CONTROL: a file not matching the ledger prefix is left alone"
	else
		fail "18q2 NEGATIVE CONTROL: the reaper deleted a file outside its own prefix"
	fi

	# --- 18u POSITIVE CONTROL: `set -e` must not swallow the verdict ------
	# Measured, because it is counter-intuitive and it bit this very fix:
	#
	#   set -e; f() { return 3; }; g() { local p rc; p=$(f); rc=$?; echo REACHED; }
	#   g   ->  exits 3. REACHED is never printed.
	#
	# `set -e` fires on the ASSIGNMENT, so a helper that captures into a
	# variable and then inspects `$?` never reaches its own diagnosis — the
	# caller dies at the assignment and the reader gets a bare nonzero exit
	# where the fault verdict should have been. That is this defect wearing a
	# different hat: the evidence exists and is discarded.
	#
	# Every call site in the tree today is `if helper ...` / `if ! helper ...`,
	# where `set -e` is suppressed for the whole function body — so this is a
	# latent trap rather than a live one. Pinned anyway: the next plain call is
	# a one-line edit away, and nine of the drivers now sourcing this lib run
	# under `set -euo pipefail`.
	#
	# The discriminator is which text survives. If the assignment aborts,
	# capture_pane's own fault block is on stderr but e2e_pane_lacks' verdict
	# never runs, so `CANNOT JUDGE` and the `absence probe` subject line are
	# both absent. The helper must be called PLAINLY here: an `if`, a `||` or a
	# pipeline would suppress `set -e` and the arm would pass either way.
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" 'set -e
e2e_pane_lacks dead-sess-18u "Session Error" "no error banner"'
	_unit_cap_has "$_OUT" 'absence probe' yes "18u POSITIVE CONTROL: under set -e, e2e_pane_lacks still prints its subject (the caller is not killed at the assignment)"
	_unit_cap_has "$_ERR" 'CANNOT JUDGE' yes "18u POSITIVE CONTROL: under set -e, e2e_pane_lacks still reaches its verdict instead of dying silently"
	: >"$CAPLEDGER"
	_unit_cap_probe dead "$CAPLEDGER" 'set -e
wait_for_pattern dead-sess-18u "anything" 4 >/dev/null'
	if [ "$_RC" -eq 2 ]; then
		pass "18u POSITIVE CONTROL: under set -e, wait_for_pattern's own rc 2 reaches the caller (not tmux's raw status from an aborted assignment)"
	else
		fail "18u POSITIVE CONTROL: under set -e, wait_for_pattern gave the caller rc=$_RC, want 2 — the assignment aborted before the fault could be classified; err=$_ERR"
	fi
fi

# --- 18q: the shared capture lib exists and is sourceable standalone -------
# The nine legacy scripts/test-*-e2e.sh drivers do NOT source e2e-common.sh, so
# a fix confined to that file would leave nine live `make` targets producing
# vacuous greens while QUM-957 read Done. One small lib both can source is what
# makes the fix reach all of them from a single mechanism.
echo "[18q] the shared capture-pane lib is present and sourceable on its own"
if [ -r "$CAPLIB" ]; then
	pass "18q: scripts/lib/capture-pane.sh exists and is readable"
else
	fail "18q: scripts/lib/capture-pane.sh not readable — the 9 legacy drivers have nothing to source"
fi
(
	# Under `set -u` on purpose: most legacy drivers run with it, and a lib that
	# dereferences an unset var bare would abort them at source time.
	set -u
	# shellcheck disable=SC1090
	. "$CAPLIB" >/dev/null 2>&1
)
if [ $? -eq 0 ]; then
	pass "18q: capture-pane.sh sources cleanly under set -u"
	_caplib_sourceable=yes
else
	fail "18q: capture-pane.sh does not source cleanly under set -u"
	_caplib_sourceable=no
fi
for fn in capture_pane capture_pane_ansi capture_pane_scrollback \
	capture_pane_best_effort e2e_require_session_alive e2e_pane_lacks \
	e2e_capture_fault_reset capture_pane_assert_no_faults; do
	if [ "$_caplib_sourceable" != yes ]; then
		# Distinct message: a failed source is a different diagnosis from a
		# missing function, and reporting it eight times as the latter would
		# send a reader hunting for definitions that are all present.
		fail "18q: cannot check whether capture-pane.sh defines $fn (the lib did not source)"
		continue
	fi
	(
		# shellcheck disable=SC1090
		. "$CAPLIB" >/dev/null 2>&1 || exit 99
		declare -F "$fn" >/dev/null 2>&1
	)
	if [ $? -eq 0 ]; then
		pass "18q: capture-pane.sh defines $fn"
	else
		fail "18q: capture-pane.sh does NOT define $fn"
	fi
done

# --- 18r POSITIVE CONTROL: the swallowed idiom is gone, and cannot come back
# This is the arm that blocks the realistic regression. If someone quiets a
# newly-loud call site with `|| true`, `make validate` goes red and the commit
# that would have blessed the defect cannot land.
echo "[18r] no capture-pane call site swallows its status or its diagnostic"
# SCOPE LIMIT, stated because it is invisible from the arm's green: this scans
# `$REPO_ROOT/scripts` ONLY. A pane capture added anywhere else is unguarded and
# every arm below still prints PASS — a clean scan of the wrong corpus, which is
# this section's own defect class. Verified at this commit that nothing outside
# scripts/ captures a pane (the only hits are prose in
# internal/config/errors{,_test}.go comments), so the limit costs nothing today
# and is a trap tomorrow. Widen the root here if that changes.
_cap_sites=$(grep -rlE '(^|[^a-z_])capture_pane|capture-pane' "$REPO_ROOT/scripts" 2>/dev/null | sort)
_cap_site_count=$(printf '%s\n' "$_cap_sites" | grep -c .)
# Non-vacuity FIRST: a corpus scan that found nothing satisfies every
# "zero offenders" assertion below perfectly and reports green.
if [ "${_cap_site_count:-0}" -ge 20 ]; then
	pass "18r: found $_cap_site_count file(s) referencing a pane capture to scan"
else
	fail "18r: only ${_cap_site_count:-0} file(s) reference a pane capture — the scans below would be vacuous"
fi
# TWO patterns, because the two halves of the swallow have different scopes and a
# single pattern gets one of them wrong. Both were verified against bash rather
# than reasoned about:
#
#   `2>/dev/null` binds to ONE PIPELINE ELEMENT. In
#       capture_pane "$S" | grep -q x 2>/dev/null
#   it redirects grep's stderr, and grep writes none — the capture's diagnostic
#   still reaches the operator. That spelling is NOT the defect, and **40** of
#   them exist in the tree as harmless cargo-cult. So this pattern keeps `[^|]*`:
#   the redirect must be in the same element as the capture to count.
#
#   That 40 is MEASURED, and the derivation is here so the next reader re-derives
#   it rather than trusting it — it was first written as "~45", which was wrong
#   when written (not drift: the count is identical at the QUM-957 baseline and
#   at HEAD). Re-derive with:
#     grep -rnE '(capture-pane|capture_pane[a-z_]*).*\|.*2>/dev/null' scripts \
#       | grep -vE '^[^:]*:[0-9]+:[[:space:]]*#' \
#       | grep -v '/test-e2e-matrix-unit.sh:' | wc -l
#
#   `|| true` binds to the WHOLE AND-OR LIST, so
#       capture_pane "$S" | tail -30 >&2 || true
#   really does discard the capture's contribution to the status, and under
#   `pipefail` that contribution is the only reason the line is nonzero. That
#   spelling IS the defect, it is what someone reaches for to quiet a
#   newly-loud site, and `[^|]*` cannot see it. So it gets `.*`.
#
# Line continuations are joined first: the two notify-tui counters spelled the
# defect across three physical lines, so a line-oriented scan reported a clean
# green while a genuinely vacuous "expect 0 banners" assertion sat in the tree.
#
# Comment lines are excluded: the fix's own documentation QUOTES the defective
# one-liner so a future reader can recognise it, and a scan that could not tell a
# quotation from a call would fail on its own explanation.
_cap_swallow=""
while IFS= read -r _f; do
	[ -n "$_f" ] || continue
	case "$_f" in
		*/test-e2e-matrix-unit.sh | */lib/capture-pane.sh) continue ;;
	esac
	_cap_hits=$(sed -e ':a' -e '/\\$/{N;s/\\\n//;ba}' "$_f" 2>/dev/null \
		| grep -nE '(capture-pane|capture_pane[a-z_]*)([^|]*2>/dev/null|.*\|\| *true)' \
		| grep -vE '^[0-9]+:[[:space:]]*#' \
		| grep -vE 'capture_pane(_ansi)?_best_effort' || true)
	if [ -n "$_cap_hits" ]; then
		_cap_swallow="$_cap_swallow
${_f##*/}: $(printf '%s' "$_cap_hits" | tr '\n' '~')"
	fi
done <<CAPSWALLOW
$_cap_sites
CAPSWALLOW
if [ -z "$_cap_swallow" ]; then
	pass "18r: no pane capture under scripts/ discards its own stderr or its own exit status"
else
	fail "18r: the QUM-957 swallow is still present (or was reintroduced):$_cap_swallow"
fi
# The scan above cannot see a swallow written across a continuation unless the
# join works, and a join that silently did nothing would make it green. Proven on
# a fixture rather than assumed, since that is exactly how the notify-tui
# counters hid: three physical lines, one logical statement.
_cap_join_fixture=$(mktemp "$UNIT_TMP_ROOT/e2e-cap-join.XXXXXX")
printf '%s\n' 'capture_pane "$s" \' '    | grep -c x \' '    || true' >"$_cap_join_fixture"
if sed -e ':a' -e '/\\$/{N;s/\\\n//;ba}' "$_cap_join_fixture" \
	| grep -qE '(capture-pane|capture_pane[a-z_]*)([^|]*2>/dev/null|.*\|\| *true)'; then
	pass "18r POSITIVE CONTROL: the scan sees a swallow spelled across line continuations"
else
	fail "18r POSITIVE CONTROL: the scan CANNOT see a continuation-spelled swallow — its green above is not attributable"
fi
# ...and it must NOT report the harmless spelling. This control is load-bearing,
# not tidiness: **40** lines in the tree are `capture_pane X | grep -q y
# 2>/dev/null`, where the redirect binds to grep and discards nothing. An arm that
# flagged all 40 would be read as noisy, and the next person to look would exempt
# it wholesale — which is how an anti-regression arm dies quietly while still
# printing PASS. Keep this control whenever the pattern above is touched.
#
# The figure is measured, not estimated, and the derivation is at the pattern
# above. It said "about 45" first, which was wrong by 12% — and a reader who
# checks a supporting number, finds it off, and then discounts the argument
# attached to it is exactly the reader this control has to convince. The argument
# stands on its own; the number should not be its weakest part.
printf '%s\n' 'if capture_pane "$s" | grep -q x 2>/dev/null; then :; fi' >"$_cap_join_fixture"
if sed -e ':a' -e '/\\$/{N;s/\\\n//;ba}' "$_cap_join_fixture" \
	| grep -qE '(capture-pane|capture_pane[a-z_]*)([^|]*2>/dev/null|.*\|\| *true)'; then
	fail "18r NEGATIVE CONTROL: the scan flags '| grep -q x 2>/dev/null', where the redirect binds to grep and discards nothing"
else
	pass "18r NEGATIVE CONTROL: the scan does not flag a 2>/dev/null that binds to grep rather than to the capture"
fi
rm -f -- "$_cap_join_fixture"
# The library is exempted from the scan above and pinned by COUNT here instead.
# It has to contain the idiom: capture_pane_best_effort and its ANSI twin ARE the
# sanctioned opt-out, so discarding tmux's stderr is their whole job. A blanket
# exemption would leave the one file that matters most unscanned, and a
# per-line-content exemption is what someone reintroducing the defect would
# reach for. A count is neither: adding a third quiet capture to that file fails
# this arm and forces the author to say why here.
_cap_lib_quiet=$(grep -vE '^[[:space:]]*#' "$CAPLIB" 2>/dev/null | grep -cE 'capture-pane[^|]*2>/dev/null')
if [ "${_cap_lib_quiet:-0}" -eq 2 ]; then
	pass "18r: scripts/lib/capture-pane.sh discards tmux stderr in exactly the 2 sanctioned opt-out helpers"
else
	fail "18r: scripts/lib/capture-pane.sh has ${_cap_lib_quiet:-0} quiet capture(s), want exactly 2 (capture_pane_best_effort and capture_pane_ansi_best_effort) — a new one needs justifying"
fi
# One definition, not eleven. A local redefinition shadows the shared one at
# call time, so a copy left behind is not merely untidy — it silently reinstates
# the defect for that whole file. All three bash function spellings are matched.
_cap_defs=$(grep -rnE '^[[:space:]]*(function[[:space:]]+)?capture_(pane|ansi)[a-z_]*[[:space:]]*\(\)?' "$REPO_ROOT/scripts" 2>/dev/null \
	| grep -vE '^[^:]*:[0-9]+:[[:space:]]*#' \
	| grep -v '/lib/capture-pane.sh:' \
	| grep -v '/test-e2e-matrix-unit.sh:' || true)
if [ -z "$_cap_defs" ]; then
	pass "18r: the capture helpers are defined only in scripts/lib/capture-pane.sh"
else
	fail "18r: a capture helper is redefined outside the shared lib, shadowing the fix:
$_cap_defs"
fi

# --- 18s POSITIVE CONTROL: every capture_pane caller reaches a fault gate ---
# The ledger only fails a row if something reads it. A driver that captures
# panes but never calls an aggregator arm keeps the vacuous green.
echo "[18s] every capture_pane caller reaches a fault gate"
_cap_ungated=""
_cap_gated_seen=0
_cap_legacy_expected=$(ls "$REPO_ROOT"/scripts/test-*-e2e.sh 2>/dev/null | xargs -r grep -lE '(^|[^a-z_])capture_pane' 2>/dev/null | grep -c . )
if [ "${_cap_legacy_expected:-0}" -ge 9 ]; then
	pass "18s: found $_cap_legacy_expected legacy scripts/test-*-e2e.sh driver(s) that capture panes"
else
	fail "18s: only ${_cap_legacy_expected:-0} legacy driver(s) capture panes — expected at least 9, so the gate scan below would be vacuous"
fi
while IFS= read -r _f; do
	[ -n "$_f" ] || continue
	case "$_f" in
		*/lib/capture-pane.sh | */lib/e2e-common.sh | */test-e2e-matrix-unit.sh) continue ;;
		# Rows under e2e-tests/ inherit the gate from e2e_print_results in the
		# shared lib, which every row already calls ([17j] enforces that).
		*/e2e-tests/*) continue ;;
	esac
	# Comment lines are stripped first, so a driver that merely NAMES the gate in
	# a comment does not count. Position within the line is not constrained: the
	# real call sites are `if ! capture_pane_assert_no_faults; then` at top level
	# and an indented `e2e_print_results` inside a row's test_run.
	if grep -vE '^[[:space:]]*#' "$_f" 2>/dev/null \
		| grep -qE '(^|[^a-z_])(capture_pane_assert_no_faults|e2e_print_results)([^a-z_]|$)'; then
		_cap_gated_seen=$((_cap_gated_seen + 1))
	else
		_cap_ungated="$_cap_ungated ${_f##*/}"
	fi
done <<CAPSITES
$_cap_sites
CAPSITES
if [ "$_cap_gated_seen" -ge "${_cap_legacy_expected:-9}" ]; then
	pass "18s: $_cap_gated_seen standalone driver(s) gate on a capture-fault check at top level"
else
	fail "18s: only $_cap_gated_seen standalone driver(s) gate on a capture-fault check — expected at least ${_cap_legacy_expected:-9}"
fi
if [ -z "$_cap_ungated" ]; then
	pass "18s: no capture_pane caller is left without a fault gate"
else
	fail "18s: capture_pane caller(s) never check for a capture fault, so a dead pane still reads green:$_cap_ungated"
fi

# --- 18t: the real-tmux control row exists --------------------------------
# The unit arms above use a shim, which cannot prove the contract against real
# tmux. The negative control that matters most — a genuinely live pane that is
# genuinely blank — needs a real server, so it lives in a matrix row.
echo "[18t] the real-tmux capture-pane liveness row exists and declares its needs"
CAPROW="$REPO_ROOT/scripts/e2e-tests/capture-pane-liveness.sh"
if [ -r "$CAPROW" ]; then
	pass "18t: scripts/e2e-tests/capture-pane-liveness.sh exists"
	if grep -qE '^MIN_ASSERTIONS=[1-9][0-9]{0,8}$' "$CAPROW"; then
		pass "18t: the liveness row declares a positive MIN_ASSERTIONS floor"
	else
		fail "18t: the liveness row declares no usable MIN_ASSERTIONS floor"
	fi
	if grep -q 'needs_tmux=1' "$CAPROW"; then
		pass "18t: the liveness row declares needs_tmux=1"
	else
		fail "18t: the liveness row does not declare needs_tmux=1"
	fi
	if grep -q 'needs_claude=1' "$CAPROW"; then
		fail "18t: the liveness row declares needs_claude=1 — it must not need claude, or it cannot run when claude is unavailable"
	else
		pass "18t: the liveness row does not need claude"
	fi
	# It is the one file allowed to reset the ledger, and it must actually do
	# so: it faults on purpose, so without the reset it would fail itself.
	if grep -qE '^[[:space:]]*e2e_capture_fault_reset' "$CAPROW"; then
		pass "18t: the liveness row calls e2e_capture_fault_reset (it faults on purpose)"
	else
		fail "18t: the liveness row never calls e2e_capture_fault_reset, so its deliberate fault would fail itself"
	fi
else
	fail "18t: scripts/e2e-tests/capture-pane-liveness.sh missing — the real-tmux control arms do not exist"
	fail "18t: the liveness row declares no MIN_ASSERTIONS floor (row missing)"
	fail "18t: the liveness row does not declare needs_tmux=1 (row missing)"
	fail "18t: the liveness row's claude-independence is unverified (row missing)"
	fail "18t: the liveness row's fault reset is unverified (row missing)"
fi
# The reset is the only thing here that can launder a real fault, so its spread
# is pinned as a SUBSET of an allowlist: a new legitimate user is then a
# deliberate edit to this list rather than a mystery red.
_capreset_stray=""
while IFS= read -r _f; do
	[ -n "$_f" ] || continue
	case "$_f" in
		*/scripts/lib/capture-pane.sh | */scripts/e2e-tests/capture-pane-liveness.sh | */scripts/test-e2e-matrix-unit.sh) continue ;;
		*) _capreset_stray="$_capreset_stray ${_f##*/}" ;;
	esac
done <<CAPRESET
$(grep -rlE '^[[:space:]]*e2e_capture_fault_reset' "$REPO_ROOT/scripts" 2>/dev/null | sort)
CAPRESET
if [ -z "$_capreset_stray" ]; then
	pass "18t: e2e_capture_fault_reset is confined to the lib that defines it and the one row that faults on purpose"
else
	fail "18t: e2e_capture_fault_reset appears in unexpected file(s), where it could launder a real fault:$_capreset_stray"
fi

if [ -n "${UNIT_CAP_FIX:-}" ]; then
	case "$UNIT_CAP_FIX" in
		"$UNIT_TMP_ROOT"/e2e-matrix-unit-cap.*) rm -rf -- "$UNIT_CAP_FIX" ;;
		*) echo "  NOTE: refusing to remove unexpected fixture dir '$UNIT_CAP_FIX'" >&2 ;;
	esac
fi

# ============================================================================
# [19] QUM-1186 lane 5 — the e2e suite's own observability probe
# ============================================================================
# `report_status` was this suite's generic observability probe: a row drove an
# agent to call it with a token, then polled `.sprawl/agents/<n>/state.json`'s
# `last_report_message` for that token and concluded "the thing happened". That
# subject is deleted, and the reason it is deleted — a self-report is not
# evidence — applies to the probe as much as to the tool. The replacement is an
# OBSERVED fact: the sentinel body landing in the recipient's Maildir, written
# by the delivery path itself.
#
# Why these arms live HERE rather than in the rows they serve: a matrix row
# needs a real claude and minutes of wall-clock, so its probe's ability to fail
# would be established once, by hand, and thereafter inherited. These arms are
# deterministic, sub-second, and run inside `make validate`, so the probe
# re-earns its ability to fail on every commit. What they deliberately do NOT
# cover is the plumbing that PRODUCES the sentinel — that is the rows' job, and
# the gap is recorded on QUM-1186 rather than papered over here.
#
# NOTHING in this section may launch a sandbox, build a binary, or start tmux.
# The first draft did (it executed six real e2e drivers), and the cost did not
# land here: pre-existing [16] re-runs this whole suite under a 30s budget, so
# [19] timed those children out and [16b] reported "the debug seam changed this
# suite's verdict" — a false diagnosis on an arm this lane does not own. Every
# execution below is gated behind a static precondition that proves the subject
# is on its millisecond path first.
#
# EVERY arm below carries a planted control. Two earlier drafts of this section
# shipped controls aimed at something ADJACENT to the mechanism they certified
# — a scan whose unreadable-file counter was incremented inside `$( )` and
# discarded on every real path while its control called it directly, and a
# "pin" whose control asserted that `grep -v` removes what it is asked to
# remove. Both were green. The rule that came out of it: a control must call
# the SAME function, on the SAME channel, as the arm it certifies.

echo "[19a] the shared maildir-sentinel probe exists"
if grep -qE '^[[:space:]]*e2e_wait_maildir_substring[[:space:]]*\(\)' "$LIB"; then
	pass "19a: scripts/lib/e2e-common.sh defines e2e_wait_maildir_substring"
else
	fail "19a: scripts/lib/e2e-common.sh does not define e2e_wait_maildir_substring — the rows migrated off last_report_message have no shared probe to migrate ONTO, so each would grow its own private copy (QUM-1186)"
fi

# --- 19b: the probe's controls, each with a stated direction ----------------
# The probe's contract, which these arms pin exactly:
#   0 — the sentinel is in the recipient's maildir
#   1 — it is not (poll timed out)
#   2 — the CALL is malformed: empty recipient, empty needle, or no sandbox
#
# Every arm asserts an EXACT rc, never merely non-zero. `!= 0` was the first
# draft and it was not a check: with the function missing, all six positive
# controls passed on 127 (command not found). Once the function exists, any
# internal crash — an unbound variable under a caller's `set -u`, a missing
# binary — returns non-zero too and would keep re-satisfying them. An exact rc
# binds the verdict to the probe's own judgement.
#
# Separating 2 from 1 is what makes the no-sandbox arm meaningful: with
# SPRAWL_ROOT unset a naive implementation searches $PWD/.sprawl/messages/,
# which in this worktree is a real populated maildir. Such an implementation
# returns 1 ("searched the wrong tree and missed"), not 2 ("refused"), and only
# an exact-rc assertion can tell those apart.
#
# Run via `env` + `bash -c` rather than a subshell: e2e-common.sh defines its
# own pass()/fail(), which would otherwise clobber this suite's counters. The
# outer `timeout` is a real timeout(1) — the `1` passed to the function is its
# poll budget, and if a future implementation mishandles that argument the
# command substitution would block `make validate` forever with no diagnostic.
_p19_probe() {
	local root="$1" to="$2" needle="$3"
	local envset
	if [ "$root" = "__UNSET__" ]; then
		envset="-u SPRAWL_ROOT"
	else
		envset="SPRAWL_ROOT=$root"
	fi
	# shellcheck disable=SC2086
	timeout -k 2 10 env $envset bash -c '
		. "$1" >/dev/null 2>&1 || exit 90
		command -v e2e_wait_maildir_substring >/dev/null 2>&1 || exit 91
		e2e_wait_maildir_substring "$2" "$3" 1
	' _ "$LIB" "$to" "$needle" >/dev/null 2>&1 </dev/null
	echo $?
}

# Distinguish "the probe judged" from "the probe never ran". Without this a
# crash reads as a correct refusal.
_p19_rc_label() {
	case "$1" in
		90) echo "rc=90 (scripts/lib/e2e-common.sh failed to source)" ;;
		91) echo "rc=91 (e2e_wait_maildir_substring is not defined)" ;;
		124 | 137) echo "rc=$1 (the probe HUNG and was killed — it never judged anything)" ;;
		126 | 127) echo "rc=$1 (command not found — the probe never ran)" ;;
		*) echo "rc=$1" ;;
	esac
}

_p19_expect_rc() {
	local want="$1" got="$2" what="$3" why="$4"
	if [ "$got" = "$want" ]; then
		pass "19b: $what"
	else
		fail "19b: $what — expected rc=$want, got $(_p19_rc_label "$got"). $why"
	fi
}

echo "[19b] the maildir probe fires when the sentinel is absent and stays quiet when it is present"
P19_FIX=""
P19_FIX_OK=0
if P19_FIX=$(mktemp -d "$UNIT_TMP_ROOT/e2e-matrix-unit-p19.XXXXXX" 2>/dev/null) && [ -d "$P19_FIX" ]; then
	# Every fixture write is checked. Unchecked setup is the classic way an
	# absence-shaped control goes green on nothing: with the fixture missing,
	# all six positive controls below would pass against an empty directory and
	# the run would read "6 of 8 controls green" for a fixture that never
	# existed.
	P19_FIX_OK=1
	mkdir -p "$P19_FIX/.sprawl/messages/weave/new" \
		"$P19_FIX/.sprawl/messages/weave/cur" \
		"$P19_FIX/.sprawl/messages/weave/sent" \
		"$P19_FIX/.sprawl/messages/other/new" 2>/dev/null || P19_FIX_OK=0
	printf '{"body":"probe-a1b2 hello"}\n' >"$P19_FIX/.sprawl/messages/weave/new/1.json" 2>/dev/null || P19_FIX_OK=0
	printf '{"body":"probe-delivered-c3d4"}\n' >"$P19_FIX/.sprawl/messages/weave/cur/2.json" 2>/dev/null || P19_FIX_OK=0
	printf '{"body":"probe-wrongbox-e5f6"}\n' >"$P19_FIX/.sprawl/messages/other/new/3.json" 2>/dev/null || P19_FIX_OK=0
	printf '{"body":"probe-outbox-7777"}\n' >"$P19_FIX/.sprawl/messages/weave/sent/4.json" 2>/dev/null || P19_FIX_OK=0
	[ -s "$P19_FIX/.sprawl/messages/weave/new/1.json" ] || P19_FIX_OK=0
fi
if [ "$P19_FIX_OK" -eq 1 ]; then
	# NEGATIVE control (subject known clean): the sentinel IS there, so the
	# probe must stay quiet. Without this arm a probe hardwired to `return 1`
	# would satisfy every positive control below and look rigorous.
	_p19_expect_rc 0 "$(_p19_probe "$P19_FIX" weave probe-a1b2)" \
		"probe returns 0 when the sentinel is in the recipient's new/ (negative control: it can still succeed)" \
		"Every migrated row would hang to its full timeout and fail."

	# NEGATIVE control: send_message writes to new/ and delivery moves the
	# entry to cur/. A probe that only reads new/ would go red the moment the
	# agent it is watching actually drained its inbox — i.e. it would fail
	# precisely when the system worked.
	_p19_expect_rc 0 "$(_p19_probe "$P19_FIX" weave probe-delivered-c3d4)" \
		"probe still finds a sentinel after delivery moved it to cur/ (negative control: the maildir entry is the durable one)" \
		"The probe would go red exactly when the recipient successfully consumed the message."

	# POSITIVE control (defect planted: the send never happened).
	_p19_expect_rc 1 "$(_p19_probe "$P19_FIX" weave probe-never-sent-9999)" \
		"probe returns 1 when the sentinel was never delivered (positive control: absent sentinel)" \
		"If it returned 0 it cannot fail, and every row using it is a vacuous green."

	# POSITIVE control (defect planted: delivered to the wrong agent). A probe
	# that grepped the whole messages/ tree would be satisfiable by any traffic
	# between any two agents, which is not the claim any row is making.
	_p19_expect_rc 1 "$(_p19_probe "$P19_FIX" weave probe-wrongbox-e5f6)" \
		"probe returns 1 when the sentinel landed in another agent's maildir (positive control: wrong recipient)" \
		"The recipient half of every row's claim would be unasserted."

	# POSITIVE control (defect planted: recipient has no maildir at all).
	_p19_expect_rc 1 "$(_p19_probe "$P19_FIX" nosuchagent probe-a1b2)" \
		"probe returns 1 when the recipient's maildir does not exist (positive control: no such mailbox)" \
		"A row could then pass against an agent that was never spawned."

	# POSITIVE control (defect planted: the sentinel computation produced an
	# empty string). `grep -rqF ""` matches EVERY file, so an empty needle makes
	# the probe succeed against any non-empty maildir forever.
	#
	# HONEST SCOPE, corrected after review: no CURRENT call site can reach this
	# — all of them prefix a literal (`"PHASE1-PROBE-READY-${SUFFIX1}"`), so a
	# missing `xxd` yields a NON-UNIQUE sentinel rather than an empty one, which
	# is a different defect this refusal does not catch. The arm guards the
	# probe's contract against a future caller that passes a bare variable, and
	# it is kept because it is free — not because today's rows are exposed.
	_p19_expect_rc 2 "$(_p19_probe "$P19_FIX" weave "")" \
		"probe refuses an empty needle (positive control: an empty sentinel must not match everything)" \
		"Every row whose sentinel silently came out empty would pass forever."

	# POSITIVE control (defect planted: the recipient name computed empty). The
	# docstring names three refusals; this was the one nobody had watched fire,
	# and by its own reasoning it is the worst of the three — an empty `to`
	# collapses the path to `.sprawl/messages/`, so ANY traffic between ANY two
	# agents satisfies the row.
	_p19_expect_rc 2 "$(_p19_probe "$P19_FIX" "" probe-a1b2)" \
		"probe refuses an empty recipient (positive control: an empty recipient must not search every mailbox)" \
		"A row could then be satisfied by unrelated traffic between two other agents."

	# POSITIVE control (defect planted: the needle is present only in the
	# recipient's OWN outbox). internal/messages drops a copy of every sent
	# message under messages/<from>/sent/, so a recursive grep over the mailbox
	# root can be satisfied by a message the asserting side minted itself —
	# which is precisely the class of claim this probe replaced.
	_p19_expect_rc 1 "$(_p19_probe "$P19_FIX" weave probe-outbox-7777)" \
		"probe ignores the recipient's own sent/ outbox (positive control: a self-sent copy must not satisfy it)" \
		"Where the recipient is also the sender, the probe would assert on an artifact the asserting side wrote."

	# POSITIVE control (defect planted: the row forgot to set up its sandbox).
	# rc=2 and not 1: rc 1 would mean the probe searched SOME maildir and merely
	# missed, which is a different and much worse answer than refusing. (An
	# earlier comment claimed the searched path would be this worktree's real
	# `.sprawl/messages` — false: that directory exists only at the repo root,
	# not inside an agent worktree. The rc distinction is what matters.)
	_p19_expect_rc 2 "$(_p19_probe __UNSET__ weave probe-a1b2)" \
		"probe refuses to run with SPRAWL_ROOT unset (positive control: no sandbox)" \
		"An rc of 1 would mean it searched a maildir anyway — under an unset SPRAWL_ROOT, the live repo's."

	# POSITIVE control (defect planted: the probe matches as a REGEX). Sentinels
	# are opaque tokens; a probe using `grep -q` rather than `grep -qF` would
	# let `probe.a1b2` match the planted `probe-a1b2`, so a row could pass on a
	# token it never sent.
	_p19_expect_rc 1 "$(_p19_probe "$P19_FIX" weave 'probe.a1b2')" \
		"probe matches the needle literally, not as a regex (positive control: 'probe.a1b2' must not match 'probe-a1b2')" \
		"A row's sentinel could be matched by a token it never sent."
else
	# One fail per lost arm, so the count is preserved and the summary cannot
	# read as a smaller-but-green run.
	fail "19b: the probe fixture could not be built under $UNIT_TMP_ROOT — the sentinel-present negative control did not run"
	fail "19b: fixture missing — the delivered-to-cur negative control did not run"
	fail "19b: fixture missing — the absent-sentinel positive control did not run"
	fail "19b: fixture missing — the wrong-recipient positive control did not run"
	fail "19b: fixture missing — the no-such-mailbox positive control did not run"
	fail "19b: fixture missing — the empty-needle positive control did not run"
	fail "19b: fixture missing — the empty-recipient positive control did not run"
	fail "19b: fixture missing — the own-outbox positive control did not run"
	fail "19b: fixture missing — the unset-SPRAWL_ROOT positive control did not run"
	fail "19b: fixture missing — the literal-match positive control did not run"
fi

# --- 19c: the corpus lint --------------------------------------------------
# The highest-value arm in this section, and the only control that covers 100%
# of the corpus. Two distinct failure classes:
#
#   dead ASSERTION — a row reads `.last_report_message`. Loud: it times out.
#   dead ADVERTISEMENT — a row's live agent PROMPT still names `report_status`.
#     Quiet and much worse: the agent is told to call a tool that does not
#     exist, improvises something else, and the row can still go green having
#     exercised a different path. This is the shape tower named as "the
#     deletion reaches the implementation but not the advertisement", and no
#     live run detects it.
#
# The whitelist arm generalises it: any `mcp__sprawl__<name>` a script tells an
# agent to call must be a tool that actually exists. It found
# `mcp__sprawl__messages_send` — a tool that has NEVER existed — in four
# scripts that had been passing on the model guessing the right name.
#
# The tokens are BARE, not `mcp__sprawl__`-prefixed. The prefixed form was the
# first draft and it half-missed its own stated target: scripts/e2e-tests/
# notify-tui.sh named `report_status` nine times and never once in prefixed
# form, so it tripped the scan only by coincidence, through `last_report_`.
# Bare tokens also catch the comment and header rot — a row whose prose still
# describes the old probe is the same defect as one whose code does.
#
# THE EXEMPTION, and why it is a marker rather than a heuristic: the skip
# rationales lane 1 wrote (`e2e_skip_row "…drove the wake gate through BOTH
# delegate and send_message…"`) exist precisely to name the deleted tools.
# They are the skip accounting; rewording them into compliance would destroy
# the explanation. Same for a handful of historical phase labels. So two
# exemptions, both explicit and greppable — a line calling `e2e_skip_row`, and
# a line carrying a literal `P19-ALLOW` comment. A context heuristic ("is this
# line prose?") was rejected: a future prose style drifts past it silently,
# whereas adding a marker is a deliberate edit that shows up in review.
P19_FORBIDDEN='report_status delegate messages_send last_report status_change interrupt='

# NAMES THAT NEVER EXISTED, scanned over the whole TRACKED TREE rather than only
# `scripts/`. This set is deliberately narrower than P19_FORBIDDEN, and the
# corpus deliberately wider, because the two properties trade off:
#
#   A formerly-existing name (`report_status`, `delegate`, `last_report`,
#   `status_change`) has legitimate HISTORY to recount. Retrospective prose
#   naming it is not an advertisement, and eight such lines exist today in
#   .claude/skills/ alone — load-bearing obligation prose in the e2e-matrix
#   table, which a reviewer reads. Scanning those with P19_FORBIDDEN would
#   require sprinkling P19-ALLOW markers through them and roughly doubling
#   P19_EXEMPT_CEILING, which destroys what that ceiling measures.
#
#   A name that NEVER existed has no history to recount, anywhere. Every
#   occurrence is an error by construction, so the wide corpus costs nothing.
#
# This is the gap that let `mcp__sprawl__messages_send` survive in
# .claude/skills/testing-practices/SKILL.md (taught as the mechanism of the
# LIVE drain-row-inject row) and in Makefile:315 — the same defect this section
# already catches inside `scripts/`, sitting one directory outside its corpus.
# A skill is loaded into agent context, so the next probe author writes the
# nonexistent name and the model silently guesses `send_message`.
#
# THE BOUNDARY, stated rather than inherited: formerly-existed names remain
# unguarded OUTSIDE `scripts/**/*.sh`. That is deliberate, not an oversight.
#
# Membership cannot be derived — "never existed" is a claim about history, and
# deriving it would mean `git log -S` over the full history on every
# `make validate`. Instead the list is hardcoded and BOUNDED: the arm below
# asserts every member is absent from the canonical tool set, so if someone
# ever ships a real `messages_send` this section fails and names its own remedy.
P19_NEVER_EXISTED='messages_send'

# Scripts deliberately outside the corpus, matched on REPO-RELATIVE PATH rather
# than basename — a basename exclusion would silently drop a future matrix row
# that happened to share a name. tower's ruling on the pre-matrix standalone
# duplicates (QUM-1186 lane 5) is that they exit 77 at the top rather than
# being migrated twice or deleted in this slice: a script that runs nothing
# cannot drift and cannot false-green, so its residual tokens are inert. [19e]
# is what keeps that true. This suite excludes itself because its own fixtures
# must contain the forbidden tokens in order to control the scan.
P19_EXCLUDE='scripts/test-ask-user-question-e2e.sh
scripts/test-notify-tui-e2e.sh
scripts/test-drain-row-inject-e2e.sh
scripts/test-wake-live-e2e.sh
scripts/test-bridge-lifecycle-e2e.sh
scripts/test-merge-reuse-e2e.sh
scripts/test-e2e-matrix-unit.sh'
P19_EXCLUDE_N=7

# Both scans report an unreadable file ON THE SAME CHANNEL as a violation,
# rather than bumping a counter. The counter form was a defect: the scans are
# called inside `$( )`, so every increment on a real path was discarded in the
# subshell, while the control called the scan directly and the increment stuck.
# A green control certifying a mechanism disabled on every path it guards.
# A row that calls `e2e_skip_row` before its first assertion runs NOTHING: the
# helper exits 77 and the rest of test_run is unreachable. Two such rows exist
# (complete-lifecycle, wake-on-traffic — lane 1 skipped them because half their
# subject is deleted), and their bodies are the blueprint for re-hosting them,
# so rewording every mention of the deleted tools out of them would destroy the
# thing that makes the re-host possible. They are exempt as WHOLE FILES, on the
# same argument as the standalone drivers: an inert script cannot false-green.
#
# "before its first assertion" is the load-bearing part. A row that asserts and
# THEN skips has already run, and must not be exempt — that is the near-miss
# this predicate is written to reject, and it is controlled below.
_p19_is_skipped_row() {
	local f="$1"
	[ -r "$f" ] || return 1
	# Only a matrix ROW can be inert this way.
	case "$f" in
		*/scripts/e2e-tests/*.sh) ;;
		*) return 1 ;;
	esac
	# TWO conditions, both required, because either alone is wrong:
	#
	#   the MARKER declares intent, and is greppable in review;
	#   the UNCONDITIONAL skip proves the mechanism — `e2e_skip_row` at exactly
	#   one indent level, i.e. at the top of test_run rather than nested in an
	#   `if`.
	#
	# The first draft compared SOURCE LINE NUMBERS — first `e2e_skip_row` before
	# first `pass|fail` — and that silently exempted LIVE rows. Precondition
	# guards ("no pgrep on PATH", "/proc unreadable") are conditional skips that
	# legitimately sit near the top, above the first assertion, in rows that run
	# all their assertions on any normal host. It exempted `idle-reclaim.sh` —
	# the very row [19d] pins as StopAfterTurn's successor — hiding a
	# forbidden-token hit in it, and the ceiling comment was wrong by two before
	# anyone noticed. Line ORDER is not execution reachability.
	# THREE conditions. Each closes a distinct hole, and dropping any one of them
	# re-opens a defect that has actually occurred here:
	#
	#   marker            — intent, declared and greppable in review.
	#   unconditional     — `e2e_skip_row` at exactly one indent level, so a
	#                       CONDITIONAL precondition guard cannot masquerade as
	#                       inertness. This is the hole the first draft had.
	#   before-assertions — the skip precedes the first pass/fail, so a row that
	#                       asserts and THEN skips is not treated as inert. This
	#                       is the hole the first draft closed, and it must not
	#                       be lost while closing the other.
	grep -qE '^# P19-INERT-ROW' "$f" 2>/dev/null || return 1
	local skip_ln assert_ln
	skip_ln=$(grep -nE '^    e2e_skip_row[[:space:]]+["'"'"']' "$f" 2>/dev/null | head -1 | cut -d: -f1)
	[ -n "$skip_ln" ] || return 1
	assert_ln=$(grep -nE '^[[:space:]]*(pass|fail)[[:space:]]+"' "$f" 2>/dev/null | head -1 | cut -d: -f1)
	[ -n "$assert_ln" ] || return 0
	[ "$skip_ln" -lt "$assert_ln" ]
}

# Token scan, parameterized on the token list in $1 so the tree corpus can run
# the NARROWER `P19_NEVER_EXISTED` set through the SAME code path — and the same
# exemptions, the same unreadable accounting — that every control below already
# exercises. A second copy of this function would be a second thing to control.
#
# `-I` on the grep: the tree corpus contains a tracked binary, and without it
# grep emits `Binary file … matches` with no line number, which corrupts the
# `n:body` parse below. It is behaviour-neutral for the `.sh` corpus. The cost
# is that a binary is reported SILENTLY CLEAN — the unreadable accounting does
# not cover that case, so "a clean verdict covers all of it" is overstated by
# exactly the tracked binaries.
_p19_scan_tokens() {
	local toks="$1"
	shift
	local f tok base line n body
	for f in "$@"; do
		# Repo-relative label, computed inline rather than in a helper: the tree
		# corpus is ~939 files and a `$( )` per file is measurable on every
		# `make validate`. A BASENAME was unambiguous for `scripts/**/*.sh` and
		# useless here, where eleven distinct files are named SKILL.md.
		# Fixtures live outside REPO_ROOT and keep their basename, so every
		# control's expectations are unchanged.
		case "$f" in
			"$REPO_ROOT"/*) base="${f#"$REPO_ROOT"/}" ;;
			*) base="${f##*/}" ;;
		esac
		if [ ! -r "$f" ]; then
			printf '__UNREADABLE__:%s\n' "$base"
			continue
		fi
		if _p19_is_skipped_row "$f"; then
			printf '__EXEMPT__:%s:0:whole-file (row skips before it asserts)\n' "$base"
			continue
		fi
		for tok in $toks; do
			while IFS= read -r line; do
				[ -n "$line" ] || continue
				n="${line%%:*}"
				body="${line#*:}"
				case "$body" in
					*e2e_skip_row* | *P19-ALLOW*)
						printf '__EXEMPT__:%s:%s:%s\n' "$base" "$n" "$tok"
						continue
						;;
				esac
				printf '%s:%s:%s\n' "$base" "$n" "$tok"
			done <<FTOK
$(grep -InF -- "$tok" "$f" 2>/dev/null)
FTOK
		done
	done
}

# The original entry point, unchanged in behaviour: the script corpus is scanned
# with the full forbidden set.
_p19_scan_forbidden() { _p19_scan_tokens "$P19_FORBIDDEN" "$@"; }

_p19_scan_unknown_tools() {
	local f base name
	for f in "$@"; do
		if [ ! -r "$f" ]; then
			printf '__UNREADABLE__:%s\n' "${f##*/}"
			continue
		fi
		# Same whole-file exemption as the forbidden-token scan. Applying it to
		# only one of the two scans left the inert rows reported by the other,
		# which reads as a live defect in a script that runs nothing.
		if _p19_is_skipped_row "$f"; then
			printf '__EXEMPT__:%s:unknown-tool scan (row skips before it asserts)\n' "${f##*/}"
			continue
		fi
		base="${f##*/}"
		while IFS= read -r name; do
			[ -n "$name" ] || continue
			case " $P19_CANONICAL " in
				*" $name "*) ;;
				*) printf '%s:%s\n' "$base" "$name" ;;
			esac
		done <<UNKTOOLS
$(grep -oE 'mcp__sprawl__[a-z_][a-z_0-9]*' "$f" 2>/dev/null | sed 's/^mcp__sprawl__//' | sort -u)
UNKTOOLS
	done
}

_p19_unreadable_lines() { printf '%s\n' "$1" | grep -c '^__UNREADABLE__:' || true; }
_p19_exempt_lines() { printf '%s\n' "$1" | grep -c '^__EXEMPT__:' || true; }
# LINE-LEVEL exemptions only — the whole-file `P19-INERT-ROW` entries carry
# `:0:whole-file` and are counted by `_p19_exempt_lines` instead.
#
# This is a FUNCTION rather than an inline pipeline because the tree ceiling and
# its positive control must run the SAME expression, per the rule at the top of
# [19a]. The first draft inlined it at the arm and TEXTUALLY COPIED the same
# characters into the control: mutating the arm's counter left the control
# green, still printing "the exemption ceiling counts a planted line-level
# exemption". A copy is not the same expression — that distinction is the entire
# content of the rule, and this is where the rule's own remediation broke it.
_p19_line_level_exempt_lines() {
	printf '%s\n' "$1" | grep '^__EXEMPT__:' | grep -vc ':0:whole-file' || true
}

# True when $1 is an INERT row that declares a MIN_ASSERTIONS floor and does NOT
# say the floor is unreachable.
#
# `e2e_skip_row` exits 77 WITHOUT calling `e2e_print_results`, so a row that
# skips at the top never reaches its floor: every assertion in its body could be
# deleted and the row would exit 77 and look identical. The floor is still worth
# declaring — it tells the re-host what the row owes — but a bare
# `MIN_ASSERTIONS=N` above an unconditional skip READS as an enforced gate and
# is not one. That is a claim larger than the check, in the file whose whole job
# is to catch claims larger than their checks.
#
# Deliberately keyed on the row being inert (`_p19_is_skipped_row`, the SAME
# predicate the exemptions use) rather than on a filename list, so a future
# skip-at-the-top row inherits the requirement instead of being missed.
# The annotation is matched against the file with NEWLINES FLATTENED. A
# line-based grep missed `idle-reclaim-busy.sh`, whose annotation wraps as
# "…Never\n# reached while the skip is in place" — the one row that had done
# this correctly since before the check existed. A checker that fails only on
# correctly-annotated files would have driven the fix in the wrong direction
# (reflow the compliant row) instead of at the checker.
_p19_floor_unannotated() {
	local f="$1"
	_p19_is_skipped_row "$f" || return 1
	grep -qE '^MIN_ASSERTIONS=' "$f" 2>/dev/null || return 1
	tr '\n' ' ' <"$f" 2>/dev/null | tr -s ' #' ' #' | grep -qiE 'never *#? *reached' && return 1
	return 0
}
_p19_violation_lines() { printf '%s\n' "$1" | grep -vE '^(__UNREADABLE__|__EXEMPT__):' | grep -c . || true; }

# Derivation of the canonical tool set, factored out so the arms below can run
# it against a planted input rather than asserting a property of the live tree
# that nothing in this lane can move.
_p19_derive_canonical() {
	{
		grep -hoE '"name":[[:space:]]*"[a-z_][a-z_0-9]*"' "$@" 2>/dev/null |
			sed -E 's/.*"([a-z_][a-z_0-9]*)"$/\1/'
		grep -hoE 'injectToolName[[:space:]]*=[[:space:]]*"[a-z_][a-z_0-9]*"' "$@" 2>/dev/null |
			sed -E 's/.*"([a-z_][a-z_0-9]*)"$/\1/'
	} | sort -u | tr '\n' ' '
}
# Echoes the names from $2... that are MISSING from the set in $1.
_p19_canon_missing() {
	local set=" $1 " t out=""
	shift
	for t in "$@"; do
		case "$set" in
			*" $t "*) ;;
			*) out="$out $t" ;;
		esac
	done
	printf '%s' "$out"
}
# Echoes the names from $2... that are PRESENT in the set in $1.
_p19_canon_present() {
	local set=" $1 " t out=""
	shift
	for t in "$@"; do
		case "$set" in
			*" $t "*) out="$out $t" ;;
		esac
	done
	printf '%s' "$out"
}

echo "[19c] no e2e script advertises a deleted or non-existent sprawl tool"

# The canonical tool set is derived from the Go source that DEFINES it, not
# from the shell corpus being measured — a whitelist derived from the corpus it
# checks accepts everything the corpus already does, which is the defect.
#
# NOTE this bounds the set from BELOW, not in shape: the `"name":` regex would
# absorb a future input-schema property literally named "name". The absence arm
# is what bounds the specific defect.
P19_CANONICAL=$(_p19_derive_canonical \
	"$REPO_ROOT/internal/sprawlmcp/tools.go" \
	"$REPO_ROOT/internal/sprawlmcp/tools_inject_on.go")

_p19_missing=$(_p19_canon_missing "$P19_CANONICAL" send_message spawn merge retire)
if [ -z "$_p19_missing" ]; then
	pass "19c: the canonical tool set was derived from internal/sprawlmcp/ (contains send_message, spawn, merge, retire)"
else
	fail "19c: could not derive the canonical MCP tool set from internal/sprawlmcp/tools.go — missing:$_p19_missing (got '$P19_CANONICAL')"
fi
# POSITIVE control (defect planted: the derivation reads nothing). Same
# function, same arguments shape — a derivation that silently came back empty
# would make the whitelist arm flag every tool in the tree, which is loud and
# in the safe direction, but the diagnosis would be fifty lines instead of one.
if [ -n "$(_p19_canon_missing "$(_p19_derive_canonical /dev/null)" send_message spawn merge retire)" ]; then
	pass "19c: positive control — the derivation check fires when the tool source yields nothing"
else
	fail "19c: positive control FAILED — the derivation check passed against an empty tool source, so it cannot detect a broken derivation"
fi

# `report_status` and `delegate` must be ABSENT from the derived set. This is
# the arm that BOUNDS the whitelist: if it were broken, the unknown-tool scan
# would accept the deleted tools and [19c] would go green on an incomplete
# deletion.
_p19_present=$(_p19_canon_present "$P19_CANONICAL" report_status delegate)
if [ -z "$_p19_present" ]; then
	pass "19c: report_status and delegate are absent from the canonical tool set"
else
	fail "19c: the canonical tool set still contains:$_p19_present — the lane-1 deletion did not reach internal/sprawlmcp/tools.go, and the whitelist arm below would accept them"
fi
# POSITIVE control (defect planted: a tool source that still defines
# report_status). Same function, same channel.
if [ -n "$(_p19_canon_present "a b report_status c" report_status delegate)" ]; then
	pass "19c: positive control — the absence check fires on a set that still contains report_status"
else
	fail "19c: positive control FAILED — the absence check stayed quiet on a set containing report_status, so it cannot bound the whitelist"
fi

P19_CORPUS=()
_p19_excluded=0
while IFS= read -r _f; do
	[ -n "$_f" ] || continue
	_rel="${_f#"$REPO_ROOT"/}"
	case "
$P19_EXCLUDE
" in
		*"
$_rel
"*)
			_p19_excluded=$((_p19_excluded + 1))
			continue
			;;
	esac
	P19_CORPUS+=("$_f")
done <<P19FILES
$(find "$REPO_ROOT/scripts" -name '*.sh' -type f 2>/dev/null | sort)
P19FILES
# A corpus floor, for the same reason every row has an assertion floor: a `find`
# that came back empty makes both scans below report zero violations, which is
# indistinguishable from a clean tree. Hardcoded, never derived from the corpus,
# and set just under the measured size (65 at the time of writing) rather than
# at half of it — a floor of 30 would have let half the tree vanish unnoticed,
# and the exclusion list is applied BEFORE the count, so growing that list
# shrinks the measured corpus with no other signal.
P19_CORPUS_FLOOR=60
if [ "${#P19_CORPUS[@]}" -ge "$P19_CORPUS_FLOOR" ]; then
	pass "19c: the scan corpus holds ${#P19_CORPUS[@]} scripts under scripts/ (floor $P19_CORPUS_FLOOR)"
else
	fail "19c: the scan corpus holds only ${#P19_CORPUS[@]} scripts — the scans below would report clean against an empty corpus"
fi
# POSITIVE control (defect planted: find returned nothing).
if [ 0 -ge "$P19_CORPUS_FLOOR" ]; then
	fail "19c: positive control FAILED — the corpus floor is satisfied by an empty corpus, which is the exact defect a floor exists to stop"
else
	pass "19c: positive control — the corpus floor is non-zero, so it is not satisfied by an empty corpus"
fi
# The path-based exclusion is easy to break silently: a REPO_ROOT with a
# trailing slash, or a symlinked worktree, makes the prefix strip a no-op, no
# entry matches, and the six standalone drivers quietly re-enter the corpus.
if [ "$_p19_excluded" -eq "$P19_EXCLUDE_N" ]; then
	pass "19c: the exclusion list matched all $P19_EXCLUDE_N of its entries (the repo-relative path strip works)"
else
	fail "19c: the exclusion list matched $_p19_excluded of $P19_EXCLUDE_N entries — the repo-relative path strip is not resolving, so the corpus is not the set this section believes it is"
fi

_p19_hits=$(_p19_scan_forbidden "${P19_CORPUS[@]}")
_p19_hits_n=$(_p19_violation_lines "$_p19_hits")
if [ "$_p19_hits_n" -eq 0 ]; then
	# The token set is INTERPOLATED, not spelled out. The hand-written list here
	# said "report_status, delegate, messages_send, last_report or status_change"
	# and silently stopped being the truth the moment `interrupt=` joined
	# P19_FORBIDDEN — a pass message claiming less than it checked, which is the
	# same class as one claiming more.
	pass "19c: no script under scripts/ names any of: $P19_FORBIDDEN"
else
	fail "19c: $_p19_hits_n line(s) still advertise deleted or non-existent symbols — a live agent told to call a deleted tool improvises and the row can still go green:
$(printf '%s\n' "$_p19_hits" | grep -vE '^(__UNREADABLE__|__EXEMPT__):' | sed 's/^/      /')"
fi

# The COUNT includes the names the forbidden scan also reports; only the
# RENDERED list drops them. Deduping at count time made this arm print "every
# mcp__sprawl__<tool> named in scripts/ is a tool that exists" while
# `mcp__sprawl__messages_send` — a tool that has never existed — was in the
# tree: a green whose truth was conditional on a different arm being red.
_p19_unknown=$(_p19_scan_unknown_tools "${P19_CORPUS[@]}")
_p19_unknown_n=$(_p19_violation_lines "$_p19_unknown")
if [ "$_p19_unknown_n" -eq 0 ]; then
	pass "19c: every mcp__sprawl__<tool> named in scripts/ is a tool that exists"
else
	_p19_unknown_shown=$(printf '%s\n' "$_p19_unknown" | grep -vE '^(__UNREADABLE__|__EXEMPT__):' | grep -vE ":($(printf '%s' "$P19_FORBIDDEN" | tr ' ' '|'))\$")
	_p19_unknown_dropped=$((_p19_unknown_n - $(printf '%s\n' "$_p19_unknown_shown" | grep -c . || true)))
	fail "19c: $_p19_unknown_n script/tool pair(s) instruct a live agent to call an MCP tool that does not exist ($_p19_unknown_dropped of them are prefixed spellings already listed above):
$(printf '%s\n' "$_p19_unknown_shown" | sed 's/^/      /')"
fi

# --- the never-existed scan, over the tracked tree --------------------------
# TRACKED files, not a `find`: agent worktrees share a filesystem and carry
# other agents' untracked scratch output, so a find-based corpus would make this
# verdict a function of files nobody committed. Tracked-only is also the right
# semantic boundary — the defect class is "content a future author reads".
# `docs/archive/` is pruned: it is a dated record of what was true then, and
# correcting it would falsify the record. If git is unavailable the corpus comes
# back empty and the floor arm below fails loudly, which is the right direction.
P19_TREE=()
_p19_tree_excluded=0
while IFS= read -r _rel; do
	[ -n "$_rel" ] || continue
	case "$_rel" in
		docs/archive/*) continue ;;
	esac
	case "
$P19_EXCLUDE
" in
		*"
$_rel
"*)
			_p19_tree_excluded=$((_p19_tree_excluded + 1))
			continue
			;;
	esac
	P19_TREE+=("$REPO_ROOT/$_rel")
done <<P19TREEFILES
$(git -C "$REPO_ROOT" ls-files 2>/dev/null)
P19TREEFILES

# Floor 1, the corpus. Hardcoded just under the measured size (939 after
# exclusions at the time of writing), on the same argument as P19_CORPUS_FLOOR:
# a floor at half the tree would let half the tree vanish unnoticed.
P19_TREE_FLOOR=880
if [ "${#P19_TREE[@]}" -ge "$P19_TREE_FLOOR" ]; then
	pass "19c: the tree corpus holds ${#P19_TREE[@]} tracked files (floor $P19_TREE_FLOOR)"
else
	fail "19c: the tree corpus holds only ${#P19_TREE[@]} tracked files — git ls-files is not resolving, and the never-existed scan below would report clean against an empty corpus"
fi
if [ 0 -ge "$P19_TREE_FLOOR" ]; then
	fail "19c: positive control FAILED — the tree-corpus floor is satisfied by an empty corpus"
else
	pass "19c: positive control — the tree-corpus floor is non-zero, so it is not satisfied by an empty corpus"
fi
if [ "$_p19_tree_excluded" -eq "$P19_EXCLUDE_N" ]; then
	pass "19c: the tree corpus applied all $P19_EXCLUDE_N exclusions (the inert standalone drivers and this suite)"
else
	fail "19c: the tree corpus applied $_p19_tree_excluded of $P19_EXCLUDE_N exclusions — the exclusion list is not the set this section believes it is"
fi

# Floor 2, the token list. An empty list makes _p19_scan_tokens iterate zero
# times and report clean over any corpus — the `0 == 0` failure mode one layer
# in from the corpus floor. Controlled below (PC: the hole is real).
if [ -n "$P19_NEVER_EXISTED" ]; then
	pass "19c: the never-existed token list is non-empty ($P19_NEVER_EXISTED)"
else
	fail "19c: the never-existed token list is EMPTY — the tree scan below iterates nothing and reports clean over any corpus"
fi

# The bound on the hardcoded list: a never-existed name must not be a live tool.
_p19_ne_present=$(_p19_canon_present "$P19_CANONICAL" $P19_NEVER_EXISTED)
if [ -z "$_p19_ne_present" ]; then
	pass "19c: every name in P19_NEVER_EXISTED is absent from the canonical tool set (so the list has not rotted into forbidding a real tool)"
else
	fail "19c: P19_NEVER_EXISTED names a tool that NOW EXISTS:$_p19_ne_present — internal/sprawlmcp/ ships it, so strike that name from P19_NEVER_EXISTED"
fi

_p19_tree_hits=$(_p19_scan_tokens "$P19_NEVER_EXISTED" "${P19_TREE[@]}")
_p19_tree_hits_n=$(_p19_violation_lines "$_p19_tree_hits")
if [ "$_p19_tree_hits_n" -eq 0 ]; then
	pass "19c: no tracked file names a sprawl tool that has never existed ($P19_NEVER_EXISTED)"
else
	fail "19c: $_p19_tree_hits_n line(s) in the tracked tree name a tool that has NEVER existed — an agent reading them writes the nonexistent name and the model silently guesses the real one:
$(printf '%s\n' "$_p19_tree_hits" | grep -vE '^(__UNREADABLE__|__EXEMPT__):' | sed 's/^/      /')"
fi

_p19_tree_unread_n=$(_p19_unreadable_lines "$_p19_tree_hits")
if [ "$_p19_tree_unread_n" -eq 0 ]; then
	pass "19c: every file in the tree corpus was readable, so a clean verdict covers all of it"
else
	fail "19c: $_p19_tree_unread_n tree-corpus file(s) were unreadable — they contributed zero violations, which is indistinguishable from being clean"
fi

# Ceiling of ZERO, counting LINE-LEVEL exemptions only. The whole-file
# `P19-INERT-ROW` exemptions are deliberately excluded from this count: all
# three that exist are inert matrix rows that `P19_EXEMPT_CEILING` already
# governs, none of them contains a never-existed name, and counting them here
# would mean adding or removing a legitimate inert row trips this arm with the
# wrong diagnosis ("the marker is being used to silence an advertisement").
#
# Zero is the right ceiling because a name that never existed has no history to
# recount, so no line in the tracked tree has a reason to claim exemption from
# this scan. Measured: zero today.
#
# NOTE the line-level exemption also matches `e2e_skip_row`, whose rationale
# ("skip accounting must be allowed to name the tools it explains") is
# script-specific and meaningless in markdown or a Makefile. A prose line
# mentioning `e2e_skip_row` beside a never-existed name would be silently
# exempt. The ceiling of 0 is what bounds that, so do not raise it without
# splitting the two markers apart first.
_p19_tree_exempt_n=$(_p19_line_level_exempt_lines "$_p19_tree_hits")
if [ "$_p19_tree_exempt_n" -eq 0 ]; then
	pass "19c: no line in the tracked tree claims exemption from the never-existed scan (ceiling 0; the $(_p19_exempt_lines "$_p19_tree_hits") whole-file inert-row exemptions are counted by P19_EXEMPT_CEILING instead)"
else
	fail "19c: $_p19_tree_exempt_n line(s) claim a line-level exemption from the never-existed scan, over the ceiling of 0 — a name that never existed has no history to recount, so the marker is silencing an advertisement:
$(printf '%s\n' "$_p19_tree_hits" | grep '^__EXEMPT__:' | grep -v ':0:whole-file' | sed 's/^/      /')"
fi
# POSITIVE control for the ceiling, through `_p19_line_level_exempt_lines` — the
# SAME FUNCTION the arm above calls, not a copy of its text. A planted
# LINE-LEVEL exemption must be counted, and a planted WHOLE-FILE one must not.
# Without it, "zero today" is indistinguishable from a counter that can only
# ever be zero; without the shared function, the control certifies its own copy.
if [ "$(_p19_line_level_exempt_lines "__EXEMPT__:docs/planted.md:9:messages_send
__EXEMPT__:scripts/e2e-tests/inert.sh:0:whole-file (row skips before it asserts)")" -eq 1 ]; then
	pass "19c: positive control — the exemption ceiling counts a planted line-level exemption and does NOT count a planted whole-file one"
else
	fail "19c: positive control FAILED — the exemption counter cannot distinguish a line-level claim from a whole-file inert row, so its zero proves nothing"
fi

# --- a maildir gate must not describe itself as a queue ----------------------
# `e2e_wait_maildir_substring` checks a DURABLE maildir envelope. The queue is
# the TRANSIENT surface it replaced — consumed on delivery, which is why polling
# it made assertions a race against the drain. A gate that calls the maildir
# helper and then says "queue" in its verdict tells the next reader the row is
# still racing, and (as happened in death-observability's phase 4) leaves the
# failure diagnostic dumping a directory that is no longer under test, so a real
# red prints nothing useful.
#
# Deliberately NARROW: only a pass/fail within 3 lines of a maildir call, only
# on the word "queue". A corpus-wide "queue" ban would flag ~25 legitimate
# assertions about the TUI prompt queue and the pending queue — the same
# mistake as a lint that flags every legitimate mention of a deleted tool.
#
# PER FILE with `__UNREADABLE__` on the violation channel, for the reason recorded
# at the decline-to-judge predicate below: the single-awk form this used to have is
# aborted by gawk on the first unreadable operand, and the `2>/dev/null` plus `$( )`
# threw away both the diagnosis and the status, so one chmod 000 ahead of the rows in
# sort order made this arm report clean over the whole corpus. Measured by mutation
# on 2026-08-10: `chmod 000 scripts/always-loaded-budget.sh` reddened the two token
# scans and the decline-to-judge arm, and this one stayed SILENT — the sibling I had
# copied the single-awk shape from carried the same hole.
_p19_maildir_says_queue() {
	local _f
	for _f in "$@"; do
		if [ ! -r "$_f" ]; then
			printf '__UNREADABLE__:%s\n' "${_f##*/}"
			continue
		fi
		awk '/e2e_wait_maildir_substring/{w=3; next} w>0 {w--; if ($0 ~ /^[[:space:]]*(pass|fail) "/ && tolower($0) ~ /queue/) print FILENAME":"FNR}' "$_f" </dev/null
	done
}
_p19_mq=$(_p19_maildir_says_queue "${P19_CORPUS[@]}")
_p19_mq_n=$(printf '%s\n' "$_p19_mq" | grep -c . || true)
if [ "$_p19_mq_n" -eq 0 ]; then
	pass "19c: no maildir-backed gate describes its verdict as a queue"
else
	fail "19c: $_p19_mq_n gate(s) call e2e_wait_maildir_substring but report on a 'queue' — the durable surface described as the transient one it replaced:
$(printf '%s\n' "$_p19_mq" | sed 's/^/      /')"
fi

# --- an inert row's MIN_ASSERTIONS floor must SAY it is unreachable ----------
_p19_inert_seen=0
_p19_floor_bad=""
for _f in "${P19_CORPUS[@]}"; do
	_p19_is_skipped_row "$_f" || continue
	_p19_inert_seen=$((_p19_inert_seen + 1))
	if _p19_floor_unannotated "$_f"; then
		_p19_floor_bad="$_p19_floor_bad ${_f##*/}"
	fi
done
# Corpus floor for THIS loop, for the same reason every scan has one: if
# `_p19_is_skipped_row` stopped matching, the loop would examine nothing and the
# arm below would report clean — indistinguishable from every row being
# annotated. TWO inert rows exist today: idle-reclaim-busy was un-skipped on
# 2026-08-11 when the QUM-1197 (c) ruling made its P8 control passable, so the
# floor moved 3 -> 2 in that commit.
if [ "$_p19_inert_seen" -ge 2 ]; then
	pass "19c: the inert-row floor check examined $_p19_inert_seen inert row(s) (floor 2)"
else
	fail "19c: the inert-row floor check examined only $_p19_inert_seen inert row(s) (floor 2) — _p19_is_skipped_row is not matching, so the arm below would report clean against an empty set"
fi
if [ -z "$_p19_floor_bad" ]; then
	pass "19c: every inert row's MIN_ASSERTIONS floor records that it is never reached"
else
	fail "19c: inert row(s) declare a MIN_ASSERTIONS floor that reads as an enforced gate but can never be reached ($_p19_floor_bad) — e2e_skip_row exits before e2e_print_results, so every assertion in those bodies could be deleted unnoticed. Say so at the declaration, as idle-reclaim-busy.sh does"
fi

# --- a `fail` that declines to judge must say the hard fail is deliberate ----
# QA found this on idle-interrupt-inject's /proc precondition: the message said
# "this is a refusal to render a verdict", which is the DEFINITION of a skip,
# while the mechanism was `fail` + `e2e_print_results; return 1` — a hard red.
# Both choices are defensible and the row keeps `fail` on purpose: `e2e_skip_row`
# exits 77 BEFORE `e2e_print_results`, so skipping would make that row's
# MIN_ASSERTIONS floor unenforceable — the exact hazard annotated on
# complete-lifecycle and wake-on-traffic. What is NOT defensible is leaving the
# choice unstated, because the next person to meet the red reads a message that
# says "no verdict" from a mechanism that rendered one, and concludes the row is
# broken. So: decline-to-judge language in a `fail` message requires the literal
# marker `HARD FAIL BY DESIGN` in the same message.
#
# Deliberately NARROW on both axes. Only lines that INVOKE `fail "` — every
# spelling in the corpus: line-start, `|| fail "`, `&& fail "`, `then fail "`,
# `else fail "`, `; fail "` and a case arm's `) fail "`. The first draft covered
# only the first three and code review probed the gap: `[ -z "$PID" ] && fail "…"`
# is the same precondition idiom one operator over, and `else fail "` occurs 13
# times under scripts/. A comment line is never matched, because a comment may
# discuss lapsed preconditions freely (two in idle-reclaim-busy.sh do, and flagging
# prose would make this a ban on explaining yourself).
#
# And only a fixed phrase set. The loose form (refus|premise|verdict|precondition|
# skip) was measured at 24 hits including legitimate ones — "no refusal text within
# 90s", "wire assertions skipped", "refusing to use SHUTDOWN_ROOT outside /tmp".
#
# KNOWINGLY OUT OF SCOPE, measured and rejected rather than overlooked. A
# `vacuous|aborting|cannot assert|could not establish` family exists — six sites,
# among them idle-continuation.sh:241 and :252, recall-sendnow.sh:187,
# pause-lifecycle.sh:468 — and adding it was tried: it also flags messages that
# report a REAL defect using the same words, e.g. capture-pane-liveness.sh:191,
# whose positive control fires with "the QUM-957 swallow is back, and every
# negative pane assertion in the harness is vacuous again". That is a verdict, not
# a refusal to render one, and demanding HARD FAIL BY DESIGN there would be wrong.
# So this arm does NOT close that family: it is a spelling check over messages that
# deny a verdict in one of the listed ways. Anyone widening it must re-measure the
# false positives, not just the new true ones. `not an AC-1 violation` IS included:
# it is a verbatim synonym of `not a product verdict`, which was already in the set,
# and it appears five times in notif-stacked-restart.sh — four of them within 20
# lines of a site this arm already flagged.
#
# SAME-LINE BY CONSTRUCTION: the scan is line-based, so a message whose phrase and
# marker land on different physical lines would be a false positive with no way to
# satisfy the arm except unwrapping. Zero `fail "` openings in the corpus wrap
# today (measured: no odd-quote-count openings). Do not wrap these messages.
#
# ONE predicate, two modes. The first draft had two awks that each re-spelled the
# anchor and the phrase match; tightening one and not the other would have made
# the scan blind while the site count still read 3 — a floor certifying its own
# copy of what it floors, one position over from the control-copies-production
# defect QA found in this file this week. Mode "unmarked" filters, it does not
# re-derive.
#
# PER FILE, and an unreadable member is reported ON THE SAME CHANNEL as a
# violation — the shape the two scans above document, and which the first draft of
# this one regressed away from. It handed all ~65 corpus paths to a SINGLE awk with
# `2>/dev/null`: gawk is fatal on an unreadable operand and never opens the rest,
# stderr was discarded and the status thrown away by `$( )`, so one unreadable file
# sorted ahead of the rows returned an empty violation list and the arm PASSED with
# six real violators in the corpus. Code review measured it: unreadable-first gave
# `[]`, unreadable-last gave `[violator.sh:2]`. </dev/null keeps awk off inherited
# stdin (a hang inside the pre-commit hook rather than a red), and stderr is NOT
# suppressed any more, so a broken regex prints its diagnosis instead of reading
# clean.
_P19_DECLINE_RE='refusal to render a verdict|no verdict|not a product verdict|premise is unestablished|precondition lapsed|lapsed precondition|precondition does not hold|cannot run without|not an ac-1 violation'
_p19_decline_scan() {
	local mode="$1"; shift
	local _f
	for _f in "$@"; do
		if [ ! -r "$_f" ]; then
			printf '__UNREADABLE__:%s\n' "${_f##*/}"
			continue
		fi
		awk -v re="$_P19_DECLINE_RE" -v mode="$mode" '
			/^[[:space:]]*#/ { next }
			$0 !~ /(^[[:space:]]*|\|\|[[:space:]]*|&&[[:space:]]*|;[[:space:]]*|\)[[:space:]]*|then[[:space:]]+|else[[:space:]]+)fail "/ { next }
			tolower($0) !~ re { next }
			mode == "unmarked" && $0 ~ /HARD FAIL BY DESIGN/ { next }
			{ print FILENAME":"FNR }
		' "$_f" </dev/null
	done
}
_p19_decline_unmarked() { _p19_decline_scan unmarked "$@"; }
_p19_decline_marked_n() {
	local _all _unm
	_all=$(_p19_decline_scan all "$@" | grep -c . || true)
	_unm=$(_p19_decline_scan unmarked "$@" | grep -c . || true)
	echo $((_all - _unm))
}
# NO floor on the number of candidate sites in the live corpus, deliberately, and
# this is the one design note worth reading. A floor on "how many messages use
# decline-to-judge language" counts exactly the population the arm asks authors to
# fix: convert one site to `e2e_skip_row`, or reword a message so it stops claiming
# no-verdict, and the floor reds with the false diagnosis "the phrase set stopped
# matching". It would also be an aggregate — three unrelated sites appearing
# elsewhere would mask the phrase set rotting for this file's two. Rot is caught
# instead by the POSITIVE CONTROL below, which plants an unmarked site of the real
# shape and must be flagged, through this same predicate; the file set itself is
# floored by P19_CORPUS's own corpus floor above, and an unreadable member is
# reported by the scan itself as `__UNREADABLE__:<base>`, which fails the arm rather
# than shrinking its coverage silently. (That sentence used to claim "the
# readability arm below" — there is no such arm for this scan, and the claim was
# covering the false green described at the predicate.)
#
# The marker gets a CEILING for the same reason the P19-ALLOW exemption does: it is
# a literal anyone can append, so without one "adding a marker shows up in review"
# is a hope. Measured at this commit: THIRTEEN marked sites — idle-interrupt-inject.sh's
# two lapsed-premise gates, notif-stacked-restart.sh's seven idle-precondition and
# broken-measurement gates, and idle-reclaim-busy.sh's P5a, P5b plus two QUM-1197
# item-5 additions. The ceiling moved 11 -> 12 when that row was rewritten, and
# 12 -> 13 in the QA-rework commit that gave P5 a WIRE precondition of its own
# (its OS-level check alone could not tell the turn-open axis from the
# work-outstanding one — the axes had silently collapsed). The rewrite:
# its new P7 phase declines to render a verdict when the work-outstanding premise
# is unestablished (an OS-level live task AND the wire ordering showing the turn
# closed with work still outstanding), and those gates hard-fail rather than skip
# for the reason stated at the declaration — e2e_skip_row exits before
# e2e_print_results, so a skip would leave the row floor unenforceable. Raised
# here rather than only in the log, which is the whole point of the ceiling.
# idle-reclaim-busy is an inert row and is NOT exempted: this arm is about message
# honesty, not assertion reachability, and its body is the blueprint for the
# re-host, so exempting it would let the class return silently the day it comes back.
_p19_dec_marked_n=$(_p19_decline_marked_n "${P19_CORPUS[@]}")
if [ "$_p19_dec_marked_n" -le 13 ]; then
	pass "19c: $_p19_dec_marked_n fail site(s) claim HARD FAIL BY DESIGN (ceiling 13)"
else
	fail "19c: $_p19_dec_marked_n fail site(s) claim HARD FAIL BY DESIGN, above the measured ceiling of 13 — a new hard-fail-on-unmet-premise gate was added. That may be right, but raise the ceiling in the same diff so it is visible in review rather than only in the log"
fi
_p19_dec_bad=$(_p19_decline_unmarked "${P19_CORPUS[@]}")
if [ -z "$_p19_dec_bad" ]; then
	pass "19c: every fail message that declines to render a verdict states that the hard fail is deliberate"
else
	fail "19c: fail message(s) describe themselves as declining to judge but hard-fail the row, with no statement that this was chosen — a reader of the red cannot tell an unmet premise from a product defect. Add 'HARD FAIL BY DESIGN' and say why (keeping the floor enforceable), or convert the site to e2e_skip_row and annotate the floor:
$(printf '%s\n' "$_p19_dec_bad" | sed 's/^/      /')"
fi

# The exemption is a marker anyone can add to a live agent prompt to silence a
# genuine advertisement, so it needs a ceiling for the same reason every row
# needs an assertion floor: without one, "adding a marker shows up in review"
# is a hope rather than a check. Measured at this commit: zero P19-ALLOW markers
# in the corpus, and THREE whole-file exemptions, each carrying an explicit
# `# P19-INERT-ROW` marker — `complete-lifecycle` and `wake-on-traffic` (lane 1
# skipped both with rc 77; their bodies are the blueprint for re-hosting them)
# and `idle-reclaim-busy` (lane 3 skipped it, blocked on QUM-1197). Each
# exempted row is named in the pass message, so growing this set is visible in
# the log and not only in the diff.
#
# MEASURED, NOT PADDED. This was 7 against a measured 3, which is four
# exemptions of silent headroom — a marker could be added to four more lines
# and this arm would still pass, which is exactly the "adding a marker shows up
# in review" hope the ceiling exists to replace. The sibling ceiling on the tree
# corpus is 0 for the same reason. Set it to what is measured; when a fourth
# exemption is genuinely needed, raise it deliberately and name the row here.
P19_EXEMPT_CEILING=3
_p19_exempt_n=$(_p19_exempt_lines "$_p19_hits")
if [ "$_p19_exempt_n" -le "$P19_EXEMPT_CEILING" ]; then
	pass "19c: $_p19_exempt_n exemption(s) claimed (ceiling $P19_EXEMPT_CEILING):$(printf '%s\n' "$_p19_hits" | grep '^__EXEMPT__:' | cut -d: -f2 | sort -u | tr '\n' ' ')"
else
	fail "19c: $_p19_exempt_n line(s) claim the exemption, over the ceiling of $P19_EXEMPT_CEILING — the marker is being used to silence advertisements rather than to preserve skip rationales:
$(printf '%s\n' "$_p19_hits" | grep '^__EXEMPT__:' | sed 's/^/      /')"
fi

_p19_unread_n=$(($(_p19_unreadable_lines "$_p19_hits") + $(_p19_unreadable_lines "$_p19_unknown")))
if [ "$_p19_unread_n" -eq 0 ]; then
	pass "19c: every script in the corpus was readable, so a clean verdict covers all of it"
else
	fail "19c: $_p19_unread_n corpus file(s) were unreadable — they contributed zero violations, which is indistinguishable from being clean"
fi

# The scans' own controls. Direction: the fixture is a subject where the defect
# IS present, so each scan MUST fire. Every one of these calls the scan through
# the SAME command substitution the real arms use.
if [ "$P19_FIX_OK" -eq 1 ]; then
	_p19_bad="$P19_FIX/planted-defect.sh"
	{
		echo '# planted: every forbidden token, one per line'
		for _tok in $P19_FORBIDDEN; do echo "echo $_tok"; done
		echo 'echo mcp__sprawl__no_such_tool'
	} >"$_p19_bad"
	_p19_ctl=$(_p19_scan_forbidden "$_p19_bad")
	_p19_ctl_n=$(_p19_violation_lines "$_p19_ctl")
	_p19_want=$(printf '%s\n' $P19_FORBIDDEN | grep -c . || true)
	if [ "$_p19_ctl_n" = "$_p19_want" ]; then
		pass "19c: positive control — the forbidden-token scan fires once per token ($_p19_ctl_n/$_p19_want) against a planted fixture"
	else
		fail "19c: positive control FAILED — the forbidden-token scan reported $_p19_ctl_n of $_p19_want planted tokens, so its clean verdict on the real corpus proves nothing"
	fi
	case "$(_p19_scan_unknown_tools "$_p19_bad")" in
		*no_such_tool*)
			pass "19c: positive control — the unknown-tool scan fires on a planted mcp__sprawl__no_such_tool"
			;;
		*)
			fail "19c: positive control FAILED — the unknown-tool scan did not flag a planted mcp__sprawl__no_such_tool"
			;;
	esac
	# Negative control for the whitelist: a live tool must NOT be flagged, or
	# the arm would be firing on everything and its silence on the real corpus
	# would be luck.
	printf 'echo mcp__sprawl__send_message\n' >"$P19_FIX/clean-subject.sh"
	if [ "$(_p19_violation_lines "$(_p19_scan_unknown_tools "$P19_FIX/clean-subject.sh")")" -eq 0 ]; then
		pass "19c: negative control — the unknown-tool scan stays quiet on a real tool (mcp__sprawl__send_message)"
	else
		fail "19c: negative control FAILED — the unknown-tool scan flags mcp__sprawl__send_message, which exists"
	fi
	# Positive control for the unreadable-file accounting, through the same
	# command substitution as the real call.
	if [ "$(_p19_unreadable_lines "$(_p19_scan_forbidden "$P19_FIX/definitely-not-here.sh")")" -eq 1 ]; then
		pass "19c: positive control — the scan reports an unreadable file instead of passing over it"
	else
		fail "19c: positive control FAILED — an unreadable file was skipped silently, so the corpus-coverage arm cannot fail"
	fi
	# Both directions of the exemption. Without the first, adding P19-ALLOW to
	# a line would not actually exempt it and the skip rationales could not be
	# kept; without the second, the exemption is a hole anyone can fall into.
	printf 'e2e_skip_row "the delegate half is deleted; report_status is gone"\n# P19-ALLOW: historical phase label mentioning delegate\n' >"$P19_FIX/exempt-subject.sh"
	if [ "$(_p19_violation_lines "$(_p19_scan_forbidden "$P19_FIX/exempt-subject.sh")")" -eq 0 ]; then
		pass "19c: negative control — an e2e_skip_row rationale and a P19-ALLOW line are exempt from the token scan"
	else
		fail "19c: negative control FAILED — the exemption does not apply, so lane 1's skip rationales cannot name the tools they exist to explain"
	fi
	printf 'echo "a plain line naming report_status"\n' >"$P19_FIX/unexempt-subject.sh"
	if [ "$(_p19_violation_lines "$(_p19_scan_forbidden "$P19_FIX/unexempt-subject.sh")")" -eq 1 ]; then
		pass "19c: positive control — an ordinary line naming report_status is NOT exempt"
	else
		fail "19c: positive control FAILED — the exemption swallowed an ordinary line, so any script could evade the scan"
	fi
	# The whole-file exemption, both directions. The near-miss is the one that
	# matters: a row that asserts and THEN skips has already run, so exempting
	# it would hide live advertisements behind a skip that never applied to
	# them.
	mkdir -p "$P19_FIX/scripts/e2e-tests"
	printf '# P19-INERT-ROW\ntest_run() {\n    e2e_skip_row "subject deleted"\n    echo "blueprint still names report_status and delegate"\n    pass "unreachable"\n}\n' >"$P19_FIX/scripts/e2e-tests/inert-row.sh"
	if [ "$(_p19_violation_lines "$(_p19_scan_forbidden "$P19_FIX/scripts/e2e-tests/inert-row.sh")")" -eq 0 ]; then
		pass "19c: negative control — a row that skips before asserting is exempt as a whole file"
	else
		fail "19c: negative control FAILED — an inert skipped row is not exempt, so lane 1's skip rationale and re-host blueprint cannot survive"
	fi
	# The tokens sit on their OWN line, not on the e2e_skip_row line: that line
	# is exempt anyway, so putting them there would have made this control pass
	# for a reason unrelated to the whole-file rule.
	mkdir -p "$P19_FIX/scripts/e2e-tests"
	printf '# P19-INERT-ROW\ntest_run() {\n    pass "this row really ran"\n    echo "still advertises report_status and delegate"\n    e2e_skip_row "late skip"\n}\n' >"$P19_FIX/scripts/e2e-tests/late-skip-row.sh"
	if [ "$(_p19_violation_lines "$(_p19_scan_forbidden "$P19_FIX/scripts/e2e-tests/late-skip-row.sh")")" -eq 2 ]; then
		pass "19c: positive control — a row that asserts BEFORE it skips is NOT exempt (near-miss)"
	else
		fail "19c: positive control FAILED — a row that already ran was treated as inert, so any row could evade the scan by skipping after its assertions"
	fi
	# The defect the first draft actually had: a CONDITIONAL skip (a precondition
	# guard) sitting above the first assertion in a row that runs normally.
	printf '# P19-INERT-ROW\ntest_run() {\n    if ! command -v pgrep >/dev/null; then\n        e2e_skip_row "no pgrep"\n    fi\n    echo "live row still naming report_status"\n    pass "this row runs"\n}\n' >"$P19_FIX/scripts/e2e-tests/conditional-skip-row.sh"
	if [ "$(_p19_violation_lines "$(_p19_scan_forbidden "$P19_FIX/scripts/e2e-tests/conditional-skip-row.sh")")" -eq 1 ]; then
		pass "19c: positive control — a row whose skip is CONDITIONAL is NOT exempt, even with the marker (precondition guards do not make a row inert)"
	else
		fail "19c: positive control FAILED — a conditional precondition skip exempted a live row; this is the defect that hid a forbidden token in idle-reclaim.sh"
	fi
	# And the marker alone must not be enough.
	printf '# P19-INERT-ROW\ntest_run() {\n    echo "claims inert but names report_status and never skips"\n    pass "runs"\n}\n' >"$P19_FIX/scripts/e2e-tests/marker-only-row.sh"
	if [ "$(_p19_violation_lines "$(_p19_scan_forbidden "$P19_FIX/scripts/e2e-tests/marker-only-row.sh")")" -eq 1 ]; then
		pass "19c: positive control — the marker alone does not exempt a row that never calls e2e_skip_row"
	else
		fail "19c: positive control FAILED — the marker alone exempted a row that runs, so the exemption is self-certified"
	fi

	# --- controls for the never-existed scan over the tree corpus ------------
	# The red-first evidence (the scan firing on the two REAL hits at
	# .claude/skills/testing-practices/SKILL.md:434 and Makefile:315) is not
	# reproducible once those are fixed, so it cannot be this arm's standing
	# control. These are.
	mkdir -p "$P19_FIX/skills/planted" "$P19_FIX/docs"
	# POSITIVE control (defect planted in a SKILL-shaped markdown file, which is
	# the surface the corpus was widened to reach). TWO DISTINCT names and an
	# EQUALITY, so a scan that only ever matched one of them fails this arm.
	# The first draft passed `"$P19_NEVER_EXISTED messages_send no_such_tool_xyz"`
	# with a `-ge 2` threshold — but `messages_send` is already IN
	# P19_NEVER_EXISTED, so line 1 was counted twice and a scan blind to the
	# second name still scored 2. A control whose direction is right and whose
	# discrimination is not what its comment claims is this lane's own defect.
	printf 'The child calls `messages_send` to weave.\nThen `mcp__sprawl__no_such_tool_xyz`.\n' \
		>"$P19_FIX/skills/planted/SKILL.md"
	if [ "$(_p19_violation_lines "$(_p19_scan_tokens "$P19_NEVER_EXISTED no_such_tool_xyz" "$P19_FIX/skills/planted/SKILL.md")")" -eq 2 ]; then
		pass "19c: positive control — the never-existed scan fires on planted names in a markdown skill file"
	else
		fail "19c: positive control FAILED — the never-existed scan stayed quiet on a skill file naming a nonexistent tool, so its clean verdict on the tracked tree proves nothing"
	fi
	# NEGATIVE control: a markdown subject naming only REAL tools. Without this,
	# the control above is satisfied by an arm that fires on everything.
	printf 'Call `send_message`, then `mcp__sprawl__spawn`, then messages_read.\n' \
		>"$P19_FIX/skills/planted/clean.md"
	if [ "$(_p19_violation_lines "$(_p19_scan_tokens "$P19_NEVER_EXISTED" "$P19_FIX/skills/planted/clean.md")")" -eq 0 ]; then
		pass "19c: negative control — the never-existed scan stays quiet on markdown naming only tools that exist"
	else
		fail "19c: negative control FAILED — the never-existed scan flags real tool names, so its silence on the tracked tree is luck"
	fi
	# POSITIVE control for the token-list floor: the hole it guards is REAL. An
	# empty list reports clean against the very fixture proven dirty above.
	if [ "$(_p19_violation_lines "$(_p19_scan_tokens "" "$P19_FIX/skills/planted/SKILL.md")")" -eq 0 ]; then
		pass "19c: positive control — an EMPTY token list reports clean against a known-dirty subject, which is what the non-empty check exists to stop"
	else
		fail "19c: positive control FAILED — an empty token list still reported violations, so the non-empty check is guarding a hole that does not exist and the real hole is elsewhere"
	fi
	# POSITIVE control for the canonical-absence arm (defect planted: a tool set
	# that ships the never-existed name).
	if [ -n "$(_p19_canon_present "a messages_send b" $P19_NEVER_EXISTED)" ]; then
		pass "19c: positive control — the canonical-absence check fires on a tool set that ships a never-existed name"
	else
		fail "19c: positive control FAILED — the canonical-absence check stayed quiet on a set containing messages_send, so P19_NEVER_EXISTED cannot be bounded"
	fi
	# NEGATIVE control specific to WIDENING the corpus: the whole-file
	# P19-INERT-ROW exemption is gated to scripts/e2e-tests/*.sh, and must not
	# leak — otherwise any doc could silence this scan by pasting the marker.
	printf '# P19-INERT-ROW\n    e2e_skip_row "not a row"\nprose naming messages_send\n' \
		>"$P19_FIX/docs/pretend-inert.md"
	if [ "$(_p19_violation_lines "$(_p19_scan_tokens "$P19_NEVER_EXISTED" "$P19_FIX/docs/pretend-inert.md")")" -eq 1 ]; then
		pass "19c: negative control — a non-row file carrying P19-INERT-ROW is NOT whole-file exempt, so a doc cannot silence the scan by pasting the marker"
	else
		fail "19c: negative control FAILED — the whole-file row exemption leaked outside scripts/e2e-tests/, so any tracked file could evade the never-existed scan"
	fi

	# --- controls for the maildir/queue mismatch, both directions -----------
	# POSITIVE (defect planted: death-observability's phase-4 shape verbatim).
	printf 'if e2e_wait_maildir_substring weave "$WRAP" 180; then\n    pass "wrapper landed in weave queue"\nfi\n' \
		>"$P19_FIX/scripts/e2e-tests/maildir-says-queue.sh"
	if [ -n "$(_p19_maildir_says_queue "$P19_FIX/scripts/e2e-tests/maildir-says-queue.sh")" ]; then
		pass "19c: positive control — a maildir gate reporting on a 'queue' is flagged"
	else
		fail "19c: positive control FAILED — the maildir/queue mismatch scan stayed quiet on a planted mismatch"
	fi
	# NEGATIVE: a legitimate queue assertion with no maildir call nearby must NOT
	# be flagged, or the scan is a corpus-wide "queue" ban by accident.
	printf 'if wait_for_pattern "$S" "x" 30; then\n    pass "both queued prompts executed after the Esc abort (queue survived)"\nfi\n' \
		>"$P19_FIX/scripts/e2e-tests/legit-queue.sh"
	if [ -z "$(_p19_maildir_says_queue "$P19_FIX/scripts/e2e-tests/legit-queue.sh")" ]; then
		pass "19c: negative control — a genuine queue assertion with no maildir call is not flagged"
	else
		fail "19c: negative control FAILED — the scan flags any 'queue' assertion, so it would red the ~25 legitimate ones in the corpus"
	fi
	# POSITIVE control for THIS scan's unreadable handling, added when the mutation
	# that proved the decline-to-judge fix showed this sibling silently tolerating
	# the same defect. Unreadable file FIRST — the order that used to lose.
	printf 'if e2e_wait_maildir_substring weave "$W" 30; then\n    pass "landed in queue"\nfi\n' \
		>"$P19_FIX/scripts/e2e-tests/unreadable-mq.sh"
	chmod 000 "$P19_FIX/scripts/e2e-tests/unreadable-mq.sh"
	if [ "$(_p19_maildir_says_queue "$P19_FIX/scripts/e2e-tests/unreadable-mq.sh" "$P19_FIX/scripts/e2e-tests/maildir-says-queue.sh" | grep -c .)" -eq 2 ]; then
		pass "19c: positive control — the maildir/queue scan reports an unreadable member and still scans the violator after it"
	else
		fail "19c: positive control FAILED — one unreadable corpus member aborts or silences the maildir/queue scan, so a chmod 000 would green it over the whole corpus"
	fi
	chmod 644 "$P19_FIX/scripts/e2e-tests/unreadable-mq.sh"

	# --- controls for the inert-row floor annotation, both directions -------
	# Both call `_p19_floor_unannotated`, the SAME predicate the arm above uses.
	_p19_inert_body='test_run() {\n    e2e_skip_row "subject deleted"\n    pass "unreachable"\n}\n'
	# POSITIVE control (defect planted: a bare floor above an unconditional
	# skip, which is exactly what complete-lifecycle and wake-on-traffic carried).
	# shellcheck disable=SC2059
	printf "# P19-INERT-ROW\nMIN_ASSERTIONS=9\n$_p19_inert_body" >"$P19_FIX/scripts/e2e-tests/bare-floor-row.sh"
	if _p19_floor_unannotated "$P19_FIX/scripts/e2e-tests/bare-floor-row.sh"; then
		pass "19c: positive control — an inert row declaring a bare MIN_ASSERTIONS floor is flagged"
	else
		fail "19c: positive control FAILED — a bare unreachable floor was not flagged, so the arm's clean verdict proves nothing"
	fi
	# NEGATIVE control: the same row, annotated. Without this the arm could be
	# flagging every inert row and its silence on the real corpus would be luck.
	# shellcheck disable=SC2059
	printf "# P19-INERT-ROW\n# Never reached while the skip is in place.\nMIN_ASSERTIONS=9\n$_p19_inert_body" >"$P19_FIX/scripts/e2e-tests/annotated-floor-row.sh"
	if _p19_floor_unannotated "$P19_FIX/scripts/e2e-tests/annotated-floor-row.sh"; then
		fail "19c: negative control FAILED — an annotated inert row is still flagged, so the arm cannot be satisfied and the annotation is worthless"
	else
		pass "19c: negative control — an inert row whose floor records that it is never reached is not flagged"
	fi
	# NEGATIVE control for the WRAPPED annotation — the case that actually bit.
	# The first draft grepped line-by-line and flagged `idle-reclaim-busy.sh`,
	# whose annotation wraps across a comment-continuation line. Without this
	# fixture the check silently regresses to line-based the next time someone
	# simplifies it, and the only signal would be a red on the one row that had
	# been right all along.
	# shellcheck disable=SC2059
	printf "# P19-INERT-ROW\n# declared for the restored row. Never\n# reached while the skip is in place.\nMIN_ASSERTIONS=9\n$_p19_inert_body" >"$P19_FIX/scripts/e2e-tests/wrapped-floor-row.sh"
	if _p19_floor_unannotated "$P19_FIX/scripts/e2e-tests/wrapped-floor-row.sh"; then
		fail "19c: negative control FAILED — an annotation wrapped across a comment-continuation line was not recognised, so the check flags the rows that did it right"
	else
		pass "19c: negative control — an inert row whose 'never reached' annotation wraps across lines is not flagged"
	fi
	# NEAR-MISS: a LIVE row (no unconditional skip) with a bare floor must NOT
	# be flagged — its floor IS reachable and enforced, and demanding the
	# annotation there would be wrong.
	printf '#!/usr/bin/env bash\nMIN_ASSERTIONS=9\ntest_run() {\n    pass "this row really runs"\n}\n' >"$P19_FIX/scripts/e2e-tests/live-floor-row.sh"
	if _p19_floor_unannotated "$P19_FIX/scripts/e2e-tests/live-floor-row.sh"; then
		fail "19c: near-miss control FAILED — a LIVE row's reachable floor was flagged as unreachable, so the check is keying on the floor rather than on inertness"
	else
		pass "19c: near-miss control — a live row's reachable MIN_ASSERTIONS floor is not flagged"
	fi

	# --- controls for the decline-to-judge marker, all four directions -------
	# All four call `_p19_decline_unmarked`, the SAME predicate the arm uses.
	# POSITIVE (defect planted: idle-interrupt-inject.sh:296's shape, unmarked).
	printf 'if [ -z "$SLEEP_PID" ]; then\n    fail "no live sleep appeared, so the premise is unestablished — this is a refusal to render a verdict, not evidence that urgency is broken"\nfi\n' \
		>"$P19_FIX/scripts/e2e-tests/declines-unmarked.sh"
	if [ -n "$(_p19_decline_unmarked "$P19_FIX/scripts/e2e-tests/declines-unmarked.sh")" ]; then
		pass "19c: positive control — a fail that calls itself a refusal to render a verdict, with no deliberate-fail statement, is flagged"
	else
		fail "19c: positive control FAILED — the decline-to-judge scan stayed quiet on a planted unmarked site, so its clean verdict proves nothing"
	fi
	# NEGATIVE: the same message plus the marker must NOT be flagged, or the
	# requirement is unsatisfiable and the arm's silence would be luck.
	printf 'if [ -z "$SLEEP_PID" ]; then\n    fail "no live sleep appeared, so the premise is unestablished — this is a refusal to render a verdict. HARD FAIL BY DESIGN: failing rather than skipping keeps this row assertion floor enforceable"\nfi\n' \
		>"$P19_FIX/scripts/e2e-tests/declines-marked.sh"
	if [ -z "$(_p19_decline_unmarked "$P19_FIX/scripts/e2e-tests/declines-marked.sh")" ]; then
		pass "19c: negative control — the same message carrying HARD FAIL BY DESIGN is not flagged"
	else
		fail "19c: negative control FAILED — a marked site is still flagged, so the marker cannot satisfy the arm and the requirement is unmeetable"
	fi
	# NEAR-MISS: an ordinary fail must NOT be flagged, or this is a corpus-wide
	# ban on the word `fail` rather than a check on decline-to-judge language.
	printf 'fail "the pane never rendered the prompt within 30s"\n' \
		>"$P19_FIX/scripts/e2e-tests/ordinary-fail.sh"
	if [ -z "$(_p19_decline_unmarked "$P19_FIX/scripts/e2e-tests/ordinary-fail.sh")" ]; then
		pass "19c: near-miss control — an ordinary fail message is not flagged"
	else
		fail "19c: near-miss control FAILED — the scan keys on the fail call itself rather than on decline-to-judge language, so it would red most of the corpus"
	fi
	# NEAR-MISS the other way: the same language in a COMMENT must not be
	# flagged. This is the case that would red idle-reclaim-busy.sh's two
	# explanatory comments today, i.e. the check punishing the file that
	# documented the class.
	printf '# P5b: PRECONDITION LAPSED, not a product verdict. Explained here on purpose.\npass "phase ran"\n' \
		>"$P19_FIX/scripts/e2e-tests/decline-in-comment.sh"
	if [ -z "$(_p19_decline_unmarked "$P19_FIX/scripts/e2e-tests/decline-in-comment.sh")" ]; then
		pass "19c: near-miss control — decline-to-judge language in a comment is not flagged"
	else
		fail "19c: near-miss control FAILED — the scan flags prose, so explaining a lapsed precondition in a comment would become a violation"
	fi
	# POSITIVE control for the ANCHOR's lower boundary. The three near-miss
	# controls above all probe over-matching; this one probes the direction that
	# actually loses coverage — a spelling the anchor does not know silently
	# stops being scanned. `else fail "` was missed by the first draft and occurs
	# 13 times under scripts/, so it is the one worth pinning.
	printf 'if [ -n "$PID" ]; then\n    pass "busy"\nelse fail "the idle precondition does not hold, so no verdict is possible"\nfi\n' \
		>"$P19_FIX/scripts/e2e-tests/declines-else-arm.sh"
	if [ -n "$(_p19_decline_unmarked "$P19_FIX/scripts/e2e-tests/declines-else-arm.sh")" ]; then
		pass "19c: positive control — an unmarked site spelled 'else fail \"' is flagged, so the anchor covers more than line-start"
	else
		fail "19c: positive control FAILED — 'else fail \"' is not matched, so any site reformatted onto an else arm leaves the scan without a red"
	fi
	# POSITIVE control for the UNREADABLE case, which is the false green code
	# review measured: one unreadable member handed to a single awk aborted the
	# whole scan, and the arm passed with real violators in the corpus. The
	# unreadable file is placed FIRST, which is the losing order.
	printf 'fail "the premise is unestablished, so no verdict"\n' \
		>"$P19_FIX/scripts/e2e-tests/unreadable-probe.sh"
	chmod 000 "$P19_FIX/scripts/e2e-tests/unreadable-probe.sh"
	if [ "$(_p19_decline_unmarked "$P19_FIX/scripts/e2e-tests/unreadable-probe.sh" "$P19_FIX/scripts/e2e-tests/declines-unmarked.sh" | grep -c .)" -eq 2 ]; then
		pass "19c: positive control — an unreadable corpus member is reported on the violation channel AND the readable violator after it is still scanned"
	else
		fail "19c: positive control FAILED — an unreadable member either goes unreported or aborts the scan of the files after it, so one chmod 000 in the corpus would turn this arm green with real violators present"
	fi
	chmod 644 "$P19_FIX/scripts/e2e-tests/unreadable-probe.sh"
	# POSITIVE control for the CEILING's counter, which is otherwise a number
	# nobody has watched move: it must count the marked fixture and not the
	# unmarked one. Without this, a counter stuck at 0 would satisfy the ceiling
	# on every run — the `0 <= 6` form of the `0 == 0` false green.
	if [ "$(_p19_decline_marked_n "$P19_FIX/scripts/e2e-tests/declines-marked.sh" "$P19_FIX/scripts/e2e-tests/declines-unmarked.sh")" -eq 1 ]; then
		pass "19c: positive control — the HARD FAIL BY DESIGN counter counts a marked site and not an unmarked one"
	else
		fail "19c: positive control FAILED — the marker counter cannot distinguish a marked site from an unmarked one, so its ceiling proves nothing"
	fi
else
	fail "19c: no fixture dir — the forbidden-token scan ran with no positive control, so a clean verdict is not attributable"
	fail "19c: no fixture dir — the unknown-tool scan ran with no positive control"
	fail "19c: no fixture dir — the unknown-tool scan ran with no negative control"
	fail "19c: no fixture dir — the unreadable-file reporting ran with no positive control"
	fail "19c: no fixture dir — the exemption's negative control did not run"
	fail "19c: no fixture dir — the exemption's positive control did not run"
	fail "19c: no fixture dir — the whole-file skip exemption's negative control did not run"
	fail "19c: no fixture dir — the whole-file skip exemption's near-miss positive control did not run"
	fail "19c: no fixture dir — the conditional-skip positive control did not run"
	fail "19c: no fixture dir — the marker-alone positive control did not run"
	fail "19c: no fixture dir — the never-existed scan ran with no positive control, so a clean verdict over the tracked tree is not attributable"
	fail "19c: no fixture dir — the never-existed scan ran with no negative control"
	fail "19c: no fixture dir — the empty-token-list positive control did not run"
	fail "19c: no fixture dir — the canonical-absence positive control did not run"
	fail "19c: no fixture dir — the whole-file exemption leak negative control did not run"
	fail "19c: no fixture dir — the maildir/queue mismatch positive control did not run"
	fail "19c: no fixture dir — the maildir/queue mismatch negative control did not run"
	fail "19c: no fixture dir — the maildir/queue unreadable-member positive control did not run"
	fail "19c: no fixture dir — the inert-row bare-floor positive control did not run"
	fail "19c: no fixture dir — the inert-row annotated-floor negative control did not run"
	fail "19c: no fixture dir — the wrapped-annotation negative control did not run"
	fail "19c: no fixture dir — the live-row floor near-miss control did not run"
	fail "19c: no fixture dir — the decline-to-judge positive control did not run"
	fail "19c: no fixture dir — the decline-to-judge marked negative control did not run"
	fail "19c: no fixture dir — the ordinary-fail near-miss control did not run"
	fail "19c: no fixture dir — the comment-only near-miss control did not run"
	fail "19c: no fixture dir — the else-arm anchor positive control did not run"
	fail "19c: no fixture dir — the unreadable-member positive control did not run"
	fail "19c: no fixture dir — the HARD FAIL BY DESIGN counter's positive control did not run"
fi

# --- 19d: the StopAfterTurn hand-off must not evaporate quietly -------------
# scripts/e2e-tests/report-then-send.sh is DELETED by this lane: it existed
# solely to pin Real.ReportStatus's StopAfterTurn call, and that caller is
# gone. Deleting a row is itself a table edit, and the honest accounting lives
# in the matrix table — which says the coverage re-homes onto `idle-reclaim`
# and that until that row exists this is "a deliberate reduction in coverage,
# not as coverage". THAT INTERIM ENDED in lane 3: the row exists and passes, so
# the pins below now hold the table to the CURRENT truth — where the coverage
# landed, and why its busy half is skipped — rather than to a reduction that is
# no longer real.
#
# That sentence is currently the ONLY thing standing between the hand-off and
# silence, and prose does not fail. This arm makes it fail. Pinned as
# SUBSTRINGS, never byte-equality against a frozen copy: this slice has already
# produced one fake assertion of exactly that shape.
#
# Each pin gets a real pair, because none of them can go red from anything in
# this lane — the text landed in lane 4 and is already at HEAD. `_p19_pin_check`
# is the single mechanism; the arm runs it on the live file (must return 0) and
# on a copy with the phrase stripped (must return 1). An earlier draft asserted
# instead that `grep -v` had removed what it was asked to remove, which is a
# property of grep and not of this pin.
echo "[19d] the deleted report-then-send row's StopAfterTurn coverage is still handed off in writing"
P19_SKILL="$REPO_ROOT/.claude/skills/e2e-matrix/SKILL.md"

_p19_pin_check() {
	grep -qF -- "$2" "$1" 2>/dev/null
}

_p19_pin() {
	local phrase="$1" what="$2" why="$3" copy="$P19_FIX/skill-stripped.md"
	if [ "$P19_FIX_OK" -ne 1 ]; then
		fail "19d: $what could not be checked — no fixture dir, so the pin would have no control"
		return
	fi
	grep -vF -- "$phrase" "$P19_SKILL" >"$copy" 2>/dev/null
	# An unwritten copy makes `grep -qF` return 2 — non-zero, so the control
	# below would fall through to a PASS having examined a file that does not
	# exist. Same class as the unreadable-file defect, one layer in.
	if [ ! -s "$copy" ]; then
		fail "19d: $what could not be controlled — the stripped copy of the skill was not written, so the pin has no planted subject"
		return
	fi
	if ! _p19_pin_check "$P19_SKILL" "$phrase"; then
		fail "19d: $what — $why"
	elif _p19_pin_check "$copy" "$phrase"; then
		fail "19d: positive control FAILED — the pin for $what still returned 0 against a copy with the phrase removed, so it is not reading the file it is given"
	else
		pass "19d: $what (positive control: the same check returns non-zero on a copy with the phrase stripped)"
	fi
}

if [ -r "$P19_SKILL" ]; then
	# QUM-1186 lane 5: the `idle-reclaim` PHRASE pin that used to sit here is
	# gone, replaced by artifact pins below. See the block after this one for
	# why — in short, the hazard inverted and a phrase can no longer catch it.
	#
	# The INTERIM THIS ARM GUARDED IS OVER, so the phrase it used to pin is
	# gone from the table on purpose. `idle-reclaim` exists, passes, and carries
	# an enforced MIN_ASSERTIONS floor, so "a deliberate reduction in coverage"
	# would now be a false record — the coverage is not reduced, it is landed.
	#
	# The arm is repointed rather than deleted, because the thing it protects
	# has not gone away: the hand-off must still be non-silent. What must be
	# discoverable from the table has simply changed from "this coverage is
	# missing, deliberately" to "this coverage landed HERE, and its other half
	# is blocked, for THIS reason". Deleting the arm would leave the second
	# statement resting on prose again, which is what [19d] exists to prevent.
	# QUM-1186 lane 3.
	# NOTE the failure texts below name their own successor. Both of these pins
	# have an EXPIRY DATE: they assert a state of the world that ends when
	# QUM-1197 lands and the busy row is un-skipped. A gate that asserts "X is
	# blocked" fails when the project SUCCEEDS — the most confusing direction to
	# fail in — so it is only acceptable if it tells the next person what to do
	# when it expires. This arm cost one line of diagnosis instead of an
	# archaeology session because its message quoted the phrase it pinned; these
	# go one better and say what to do about it.
	_p19_pin 'idle-reclaim-busy' \
		"the matrix table names idle-reclaim-busy, so the row that gates the busy half is discoverable" \
		"the busy-agent control would be unreachable from the table that owes it"
	# The QUM-1197 hazard pin is DELETED, not repointed: it expired by its own
	# text on 2026-08-11 when the row was un-skipped, and a table that keeps
	# naming a hazard as blocking is a false record. The row's own existence and
	# its positive MIN_ASSERTIONS are what the pin above now rests on.
	_p19_pin 'report-then-send' \
		"the matrix table still names report-then-send, so the deletion is traceable from the table" \
		"the row's removal is now untraceable from the table it was removed from"
else
	fail "19d: .claude/skills/e2e-matrix/SKILL.md is unreadable — the idle-reclaim-busy record is unpinned"
	fail "19d: the QUM-1197 hazard record is unpinned (skill unreadable)"
	fail "19d: the report-then-send removal record is unpinned (skill unreadable)"
fi
# --- 19d(ii): StopAfterTurn's coverage is an ARTIFACT now, not a promise -----
# QUM-1186 lane 5. The pin above used to assert that the matrix table contained
# the phrase "a deliberate reduction in coverage". That was the right pin while
# `idle-reclaim` did not exist: prose was the ONLY surface that could carry
# "this coverage is missing on purpose", and the hazard was someone forgetting
# to write the row.
#
# THE HAZARD HAS INVERTED. The row exists. The live risk is no longer "nobody
# writes it" but "someone deletes it, or quietly guts its floor" — and a phrase
# pin cannot catch either. So the pin moves onto the thing itself, matching the
# shape of [18t], which pins the real-tmux control row rather than a sentence
# describing it. Same gate, opposite hazard, and it cannot rot the way a
# sentence can.
#
# Note what deliberately did NOT move: `report-then-send` stays a prose pin
# above, because a deleted file leaves no artifact behind except the table row
# that records its removal. Prose is the correct surface when the subject is an
# absence.
#
# These pins have no expiry date, which is the point — unlike the phrase pins
# above, whose premise ends when QUM-1197 lands, an artifact pin holds for as
# long as the row should exist.
echo "[19d] StopAfterTurn's e2e coverage exists as a row, not as a sentence"
P19_RECLAIM_ROW="$REPO_ROOT/scripts/e2e-tests/idle-reclaim.sh"

# One mechanism per property, so each pin's control runs the SAME code the pin
# runs. A control that takes a different path is how this section already
# shipped one green certifying a disabled mechanism.
_p19_row_readable() { [ -r "$1" ]; }
_p19_row_has_floor() { grep -qE '^MIN_ASSERTIONS=[1-9][0-9]{0,8}$' "$1" 2>/dev/null; }
_p19_row_names() { grep -qF -- "$2" "$1" 2>/dev/null; }

if _p19_row_readable "$P19_RECLAIM_ROW"; then
	pass "19d: scripts/e2e-tests/idle-reclaim.sh exists — StopAfterTurn's coverage is a row the driver runs"
else
	fail "19d: scripts/e2e-tests/idle-reclaim.sh is missing — StopAfterTurn has NO e2e coverage and the driver globs this directory, so nothing else will say so"
fi
if _p19_row_has_floor "$P19_RECLAIM_ROW"; then
	pass "19d: the idle-reclaim row declares a positive MIN_ASSERTIONS floor"
else
	fail "19d: the idle-reclaim row declares no usable MIN_ASSERTIONS floor — it could run, assert nothing, and be counted as coverage"
fi
if _p19_row_names "$P19_RECLAIM_ROW" StopAfterTurn; then
	pass "19d: the idle-reclaim row names StopAfterTurn, so its subject is traceable from the row"
else
	fail "19d: the idle-reclaim row no longer names StopAfterTurn — the row may still pass while having drifted off the primitive it inherited"
fi

# Controls. A converted pin is a NEW assertion, not a moved one, so each earns
# its ability to fail here rather than inheriting the phrase pin's reputation.
if [ "$P19_FIX_OK" -eq 1 ]; then
	printf '#!/usr/bin/env bash\n# a row with no floor and no subject\ntest_run() { :; }\n' >"$P19_FIX/hollow-row.sh"
	if _p19_row_has_floor "$P19_FIX/hollow-row.sh"; then
		fail "19d: positive control FAILED — the floor check passed a row declaring no MIN_ASSERTIONS"
	else
		pass "19d: positive control — the floor check fires on a row declaring no MIN_ASSERTIONS"
	fi
	printf 'MIN_ASSERTIONS=0\n' >"$P19_FIX/zero-floor-row.sh"
	if _p19_row_has_floor "$P19_FIX/zero-floor-row.sh"; then
		fail "19d: positive control FAILED — the floor check accepted MIN_ASSERTIONS=0, which is satisfied by a row that asserts nothing"
	else
		pass "19d: positive control — the floor check rejects MIN_ASSERTIONS=0"
	fi
	if _p19_row_names "$P19_FIX/hollow-row.sh" StopAfterTurn; then
		fail "19d: positive control FAILED — the subject check matched a row that does not name StopAfterTurn"
	else
		pass "19d: positive control — the subject check fires on a row that does not name StopAfterTurn"
	fi
	if _p19_row_readable "$P19_FIX/definitely-no-such-row.sh"; then
		fail "19d: positive control FAILED — the existence check passed a file that does not exist"
	else
		pass "19d: positive control — the existence check fires on a missing row"
	fi
	# Negative control: the real row must satisfy the floor check, or the three
	# arms above are green only because they are lenient.
	if _p19_row_has_floor "$P19_RECLAIM_ROW"; then
		pass "19d: negative control — the floor check accepts the real idle-reclaim row (it is not rejecting everything)"
	else
		fail "19d: negative control FAILED — the floor check rejects the real row, so its verdicts above are not attributable"
	fi
else
	fail "19d: no fixture dir — the floor check ran with no positive control"
	fail "19d: no fixture dir — the zero-floor control did not run"
	fail "19d: no fixture dir — the subject check ran with no positive control"
	fail "19d: no fixture dir — the existence check ran with no positive control"
	fail "19d: no fixture dir — the floor check ran with no negative control"
fi

if [ -e "$REPO_ROOT/scripts/e2e-tests/report-then-send.sh" ]; then
	fail "19d: scripts/e2e-tests/report-then-send.sh still exists — the driver globs scripts/e2e-tests/*.sh, so the row whose subject was deleted is still in the run list"
else
	pass "19d: scripts/e2e-tests/report-then-send.sh is gone from the driver's glob"
fi

# --- 19e: the pre-matrix standalone duplicates skip loudly ------------------
# tower's ruling (QUM-1186 lane 5): the six pre-matrix standalone drivers are
# not migrated twice and not deleted in this slice — they exit 77 at the top,
# naming the matrix row that supersedes them. A script that runs nothing cannot
# drift out of sync with the row it duplicates and cannot false-green.
#
# The skip message is pinned on a FIXED PHRASE plus the row name, never the row
# name alone: `test-notify-tui-e2e.sh` contains the substring "notify-tui" in
# its own banner, so the bare-name check PASSED against the unmigrated script.
# Measured on the red run — an assertion that could not fail, in the section
# written to eliminate assertions that cannot fail.
#
# Execution is gated behind the static pin. These six build a binary, allocate
# a /tmp sandbox and start tmux if they are NOT skipping, so running one to
# find out whether it skips is the wrong order: the static check proves the
# subject is on its millisecond path, and only then is it executed.
P19_SUPERSEDE_PHRASE='superseded by matrix row'

# Does FILE advertise itself as superseded by matrix row $2? Factored out so
# [19e]'s leak-resistance arm can be controlled with planted files instead of
# waiting for a red moment during implementation that nobody would witness.
_p19_declares_supersede() {
	grep -qF "$P19_SUPERSEDE_PHRASE '$2'" "$1" 2>/dev/null
}
_p19_is_skipping_driver() {
	grep -qF "$P19_SUPERSEDE_PHRASE" "$1" 2>/dev/null
}

echo "[19e] the pre-matrix standalone drivers exit 77 and name their successor row"
_p19_standalones='test-ask-user-question-e2e.sh:ask-user-question
test-notify-tui-e2e.sh:notify-tui
test-drain-row-inject-e2e.sh:drain-row-inject
test-wake-live-e2e.sh:wake-live
test-bridge-lifecycle-e2e.sh:wake-live
test-merge-reuse-e2e.sh:merge-reuse'
while IFS=: read -r _s _row; do
	[ -n "$_s" ] || continue
	_sp="$REPO_ROOT/scripts/$_s"
	if [ ! -r "$_sp" ]; then
		fail "19e: $_s is missing — expected it present and skipping, per tower's ruling that deleting these is a separate issue"
		fail "19e: $_s's skip is unverified (file missing)"
		continue
	fi
	if _p19_declares_supersede "$_sp" "$_row"; then
		pass "19e: $_s names the matrix row that supersedes it (\`$_row\`)"
	else
		fail "19e: $_s does not carry the phrase \"$P19_SUPERSEDE_PHRASE '$_row'\" — a reader told the coverage is gone is not told where it went"
		fail "19e: $_s was NOT executed to confirm it exits 77 — without the skip header it would build a binary and allocate a sandbox from inside make validate"
		continue
	fi
	# `-k 5`: these spawn tmux servers and a built binary that outlive a bare
	# SIGTERM. `</dev/null` so a subject that grows a `read` cannot eat the
	# remaining rows of this loop's here-doc and silently shorten it.
	_out=$(timeout -k 5 20 bash "$_sp" 2>&1 </dev/null)
	_rc=$?
	case "$_rc" in
		77)
			pass "19e: $_s exits 77 (skip), so it cannot false-green and cannot drift from $_row"
			;;
		124 | 137)
			fail "19e: $_s carries the skip header but did NOT exit within 20s (rc=$_rc) — it is doing real work despite advertising a skip"
			;;
		*)
			fail "19e: $_s exited $_rc, not 77 — a floorless duplicate of $_row with no aggregator behind it, or a failure for an unrelated reason"
			;;
	esac
done <<P19STANDALONES
$_p19_standalones
P19STANDALONES

# The one place tower's option (b) can still produce a vacuous green:
# scripts/test-leak-resistance-e2e.sh drives drivers as subjects and asserts an
# ABSENCE about each (no orphan proc, no stale socket, no residual dir). An
# absence is satisfied perfectly by a subject that never started — the script's
# own SETUP_FAIL guard exists because that already happened once. A subject
# that now exits 77 in milliseconds is exactly that shape.
#
# Scope of the claim, stated precisely: it detects a subject that skips FOR THE
# REASON THIS LANE INTRODUCED — one carrying the supersede phrase. A subject
# that exits 77 for some other unmet precondition carries no such phrase and
# reads as clean here. That residue is leak-resistance's own SETUP_FAIL guard
# to catch, and it does; this arm exists because a supersede-skip is silent,
# instant, and introduced deliberately.
echo "[19e] the leak-resistance harness does not drive a subject that skips"
P19_LEAK="$REPO_ROOT/scripts/test-leak-resistance-e2e.sh"
if [ -r "$P19_LEAK" ]; then
	_p19_leak_bad=""
	_p19_leak_missing=""
	_p19_leak_n=0
	while IFS= read -r _tgt; do
		[ -n "$_tgt" ] || continue
		_p19_leak_n=$((_p19_leak_n + 1))
		if [ ! -r "$REPO_ROOT/scripts/$_tgt" ]; then
			# An unresolvable subject path must not read as clean: grep would
			# return non-zero and the subject would be recorded as fine.
			_p19_leak_missing="$_p19_leak_missing $_tgt"
		elif _p19_is_skipping_driver "$REPO_ROOT/scripts/$_tgt"; then
			_p19_leak_bad="$_p19_leak_bad $_tgt"
		fi
	done <<P19LEAKTGT
$(grep -oE '^run_case[[:space:]]+"[^"]+"' "$P19_LEAK" 2>/dev/null | sed -E 's/.*"([^"]+)"/\1/')
P19LEAKTGT
	_p19_expected=$(grep -oE '^EXPECTED_CASES=[0-9]+' "$P19_LEAK" 2>/dev/null | head -1 | sed 's/.*=//')
	# Assert the extraction found something BEFORE trusting its emptiness: an
	# indented run_case, an unquoted first argument or a rename would yield an
	# empty list, the loop above would never execute, and the arms below would
	# print PASS having examined nothing.
	if [ -n "$_p19_expected" ] && [ "$_p19_leak_n" = "$_p19_expected" ]; then
		pass "19e: extracted all $_p19_leak_n leak-resistance subjects (matches its own EXPECTED_CASES=$_p19_expected)"
	else
		fail "19e: extracted $_p19_leak_n leak-resistance subject(s) but EXPECTED_CASES='$_p19_expected' — the extraction examined a different set than the script runs, so its verdict is not attributable"
	fi
	if [ -z "$_p19_leak_missing" ]; then
		pass "19e: every leak-resistance subject resolves to a readable script"
	else
		fail "19e: leak-resistance subject(s) do not resolve under scripts/:$_p19_leak_missing — they were examined as if clean"
	fi
	if [ -z "$_p19_leak_bad" ]; then
		pass "19e: no leak-resistance subject is a driver superseded by a matrix row"
	else
		fail "19e: leak-resistance drives subject(s) that exit 77 in milliseconds:$_p19_leak_bad — every leak assertion about them is an absence satisfied by a scenario that never started"
	fi
	# EXPECTED_CASES is leak-resistance's own assertion floor. tower's third
	# requirement: it moves with the truth, never toward it. Cross-checked with
	# a looser regex than the extraction above, so a subject the extraction
	# missed still shows up as a mismatch here.
	_p19_cases=$(grep -cE '^run_case[[:space:]]' "$P19_LEAK" 2>/dev/null || true)
	if [ -n "$_p19_expected" ] && [ "$_p19_cases" = "$_p19_expected" ]; then
		pass "19e: leak-resistance's EXPECTED_CASES=$_p19_expected matches its $_p19_cases run_case call(s)"
	else
		fail "19e: leak-resistance declares EXPECTED_CASES='$_p19_expected' but makes $_p19_cases run_case call(s) — its floor no longer matches what it runs"
	fi
else
	fail "19e: scripts/test-leak-resistance-e2e.sh is unreadable — cannot check whether it drives a skipping subject"
	fail "19e: leak-resistance's subject resolution is unverified (file unreadable)"
	fail "19e: leak-resistance's subject extraction is unverified (file unreadable)"
	fail "19e: leak-resistance's EXPECTED_CASES floor is unchecked (file unreadable)"
fi
# Controls for the two detectors [19e] rests on, planted rather than waited
# for. "I will watch it go red during implementation" is the mechanism this
# section replaces: it depends on someone looking at one particular moment.
if [ "$P19_FIX_OK" -eq 1 ]; then
	{
		echo '#!/usr/bin/env bash'
		echo "# $P19_SUPERSEDE_PHRASE 'notify-tui'"
		echo 'exit 77'
	} >"$P19_FIX/skipping-driver.sh"
	printf '#!/usr/bin/env bash\necho doing real work\n' >"$P19_FIX/working-driver.sh"
	if _p19_is_skipping_driver "$P19_FIX/skipping-driver.sh"; then
		pass "19e: positive control — the skipping-subject detector fires on a planted superseded driver"
	else
		fail "19e: positive control FAILED — the skipping-subject detector missed a planted superseded driver, so its clean verdict on leak-resistance proves nothing"
	fi
	if _p19_is_skipping_driver "$P19_FIX/working-driver.sh"; then
		fail "19e: negative control FAILED — the skipping-subject detector fires on a driver that does real work"
	else
		pass "19e: negative control — the skipping-subject detector stays quiet on a driver that does real work"
	fi
	if _p19_declares_supersede "$P19_FIX/skipping-driver.sh" notify-tui; then
		pass "19e: positive control — the successor-row check matches the row named in a planted skip header"
	else
		fail "19e: positive control FAILED — the successor-row check missed the row named in a planted skip header"
	fi
	# The NEAR-MISS control, aimed at the historical defect rather than a
	# distant one: the original bare-name check passed on the unmigrated
	# test-notify-tui-e2e.sh because its own banner contains "notify-tui". The
	# planted subject reproduces that exactly — row name present, phrase absent.
	printf '#!/usr/bin/env bash\n# this is the notify-tui driver\nexit 0\n' >"$P19_FIX/near-miss-driver.sh"
	if _p19_declares_supersede "$P19_FIX/near-miss-driver.sh" notify-tui; then
		fail "19e: negative control FAILED — the successor-row check matched a driver that merely CONTAINS the row name, which is the exact defect it replaced"
	else
		pass "19e: negative control — the successor-row check rejects a driver that merely contains the row name (near-miss)"
	fi
	if _p19_declares_supersede "$P19_FIX/skipping-driver.sh" some-other-row; then
		fail "19e: negative control FAILED — the successor-row check matched a row the planted header does not name, so it is not checking the row at all"
	else
		pass "19e: negative control — the successor-row check rejects a row the header does not name"
	fi
else
	fail "19e: no fixture dir — the skipping-subject detector ran with no positive control"
	fail "19e: no fixture dir — the skipping-subject detector ran with no negative control"
	fail "19e: no fixture dir — the successor-row check ran with no positive control"
	fail "19e: no fixture dir — the successor-row check ran with no near-miss negative control"
	fail "19e: no fixture dir — the successor-row check ran with no wrong-row negative control"
fi

if [ -n "$P19_FIX" ] && [ -d "$P19_FIX" ]; then
	case "$P19_FIX" in
		"$UNIT_TMP_ROOT"/e2e-matrix-unit-p19.*) rm -rf -- "$P19_FIX" ;;
		*) echo "  NOTE: refusing to remove unexpected fixture dir '$P19_FIX'" >&2 ;;
	esac
fi

# ----------------------------------------------------------------------------
# 20. QUM-1118: a disk-space precondition that FAILS (never skips) when a
#     filesystem the harness writes to is too full, distinct from BOTH a skip
#     (3/77 — nothing measured, and that's fine) and an ordinary row failure
#     (1 — the product is broken). Re-checked before every row, so exhaustion
#     arising mid-run is reported through the SAME path rather than as a
#     cascade of unrelated row FAILs.
#
#     20a-20g/20k drive e2e_check_disk_space directly, in a subshell, using the
#     SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP/_REPO seams — never a real filled
#     disk, per the repo's assertion-rigor rule. 20h/20i drive the REAL driver
#     end to end through a fixture tree, because the startup-vs-mid-run
#     distinction is a property of the LOOP in scripts/e2e-matrix.sh, not of
#     the helper function alone: a test that only calls the helper cannot show
#     row A ran, row B did not, and the abort is not misreported as row B
#     FAILing.
# ----------------------------------------------------------------------------
echo "[20] QUM-1118 disk-space precondition"

# 20a: healthy environment (via debug seams) -> rc 0. The seams are now
# logged unconditionally when they resolve successfully (code-review finding:
# a seam that can silently defeat the whole precondition must never do so
# with no trace, same rule as the SPRAWL_E2E_MIN_FREE_MB override) — so this
# run does NOT emit nothing, but it must emit ONLY those two WARN lines and
# never the FATAL/ENVIRONMENT UNFIT banner. That FATAL-banner absence is the
# negative control for every other case in this section: if
# e2e_check_disk_space ever printed it unconditionally, this would catch it.
out=$(
	(
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP=999999
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_REPO=999999
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_check_disk_space
	) 2>&1
)
rc=$?
if [ "$rc" -eq 0 ]; then
	pass "20a: e2e_check_disk_space returns 0 when every filesystem is healthy"
else
	fail "20a: e2e_check_disk_space rc=$rc on a healthy environment (want 0); output: $out"
fi
case "$out" in
	*FATAL* | *"ENVIRONMENT UNFIT"*)
		fail "20a: a healthy run emitted a FATAL/ENVIRONMENT UNFIT line it should not have: $out"
		;;
	*)
		pass "20a: a healthy run never emits FATAL/ENVIRONMENT UNFIT (negative control for 20b/20d/20c/20e/20f/20h/20i)"
		;;
esac
n=$(printf '%s\n' "$out" | grep -c "WARN.*debug seam")
if [ "$n" -eq 2 ]; then
	pass "20a: both active debug seams are logged (never silently defeat the precondition)"
else
	fail "20a: expected exactly 2 seam-usage WARN lines, got $n; out=$out"
fi

# 20a2: TRUE negative control — no seam set at all, so e2e_free_mb falls
# through to the REAL df on this host (which, per make validate's own
# preconditions, has well above the 4096MB default free on both filesystems).
# This is the case that must emit NOTHING: no seam WARN (none is active) and
# no FATAL (the real host is healthy).
out=$(
	(
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_check_disk_space
	) 2>&1
)
rc=$?
if [ "$rc" -eq 0 ]; then
	pass "20a2: with no seam set, a real (unfaked) healthy filesystem returns 0"
else
	fail "20a2: rc=$rc with no seam set on the real host (want 0 — is this host actually below 4096MB free? out=$out)"
fi
if [ -z "$out" ]; then
	pass "20a2: with no seam active, nothing is printed at all (no seam to warn about, no unfit filesystem to report)"
else
	fail "20a2: expected empty output with no seam set, got: $out"
fi

# 20b: unfit TMP filesystem -> exits exactly E2E_ENV_UNFIT_EXIT (5), and the
# message names the path, the free MB, and the threshold MB.
out=$(
	(
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP=10
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_REPO=999999
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_check_disk_space
	) 2>&1
)
rc=$?
if [ "$rc" -eq 5 ]; then
	pass "20b: an unfit tmp filesystem exits exactly 5 (E2E_ENV_UNFIT_EXIT), not 1/3/4/77"
else
	fail "20b: an unfit tmp filesystem exited rc=$rc, want 5; output: $out"
fi
case "$out" in
	*"ENVIRONMENT UNFIT"*"/tmp"*"10MB"*"4096MB"*)
		pass "20b: message names the path, the free MB, and the threshold MB"
		;;
	*)
		fail "20b: message missing path/free/threshold detail; got: $out"
		;;
esac

# 20c: BOTH filesystems unfit -> each gets its own FATAL line, not just the
# first. A short-circuit-on-first-failure regression would leave the caller
# unable to see the repo filesystem is also unfit.
out=$(
	(
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP=1
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_REPO=1
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_check_disk_space
	) 2>&1
)
n=$(printf '%s\n' "$out" | grep -c "ENVIRONMENT UNFIT")
if [ "$n" -eq 2 ]; then
	pass "20c: both unfit filesystems are each named in their own FATAL line, not just the first"
else
	fail "20c: expected 2 'ENVIRONMENT UNFIT' lines when both filesystems are unfit, got $n; out=$out"
fi

# 20d: unfit REPO filesystem (tmp healthy) -> also exits 5, message names it.
out=$(
	(
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP=999999
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_REPO=5
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_check_disk_space
	) 2>&1
)
rc=$?
if [ "$rc" -eq 5 ]; then
	pass "20d: an unfit REPO filesystem (tmp healthy) also exits 5"
else
	fail "20d: repo-unfit rc=$rc, want 5; out=$out"
fi
case "$out" in
	*"ENVIRONMENT UNFIT"*"5MB"*"4096MB"*)
		pass "20d: message names the repo path's free MB and threshold"
		;;
	*)
		fail "20d: message missing repo-path detail; got: $out"
		;;
esac

# 20e: SPRAWL_E2E_MIN_FREE_MB actually changes the verdict (an otherwise-unfit
# 100MB passes once the threshold is lowered to 50), and is logged loudly —
# naming both the override value and the default it replaced, unconditionally.
out=$(
	(
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP=100
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_REPO=999999
		export SPRAWL_E2E_MIN_FREE_MB=50
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_check_disk_space
	) 2>&1
)
rc=$?
if [ "$rc" -eq 0 ]; then
	pass "20e: SPRAWL_E2E_MIN_FREE_MB override actually changes the pass/fail verdict"
else
	fail "20e: override did not take effect; rc=$rc out=$out"
fi
case "$out" in
	*"WARN"*"SPRAWL_E2E_MIN_FREE_MB=50"*"default 4096"*)
		pass "20e: the override is logged loudly, naming both the override value and the default"
		;;
	*)
		fail "20e: override was not logged (or not loudly); out=$out"
		;;
esac

# 20f: a non-numeric override fails LOUDLY rather than silently falling back
# to the default — a typo'd override must not quietly re-enable (or disable)
# the check it was meant to relax (or tighten).
out=$(
	(
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP=999999
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_REPO=999999
		export SPRAWL_E2E_MIN_FREE_MB=not-a-number
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_check_disk_space
	) 2>&1
)
rc=$?
if [ "$rc" -eq 5 ]; then
	pass "20f: a non-numeric SPRAWL_E2E_MIN_FREE_MB override fails loudly (exit 5) rather than silently defaulting"
else
	fail "20f: malformed override rc=$rc, want 5; out=$out"
fi

# 20k: boundary at the default threshold. >= threshold is fit; one MB under is
# not — the off-by-one direction that would otherwise let the exact failure
# point of the 2026-08-06 incident (which this section's constant is derived
# from) slip through as "fit".
out=$(
	(
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP=4096
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_REPO=999999
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_check_disk_space
	) 2>&1
)
rc=$?
if [ "$rc" -eq 0 ]; then
	pass "20k: exactly the default threshold (4096MB) is treated as fit"
else
	fail "20k: exactly-at-threshold rc=$rc, want 0; out=$out"
fi
out=$(
	(
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP=4095
		export SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_REPO=999999
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_check_disk_space
	) 2>&1
)
rc=$?
if [ "$rc" -eq 5 ]; then
	pass "20k: one MB below the default threshold (4095MB) is treated as unfit"
else
	fail "20k: one-below-threshold rc=$rc, want 5; out=$out"
fi

# 20h: driver-integration, STARTUP. An unfit environment before any row runs
# exits 5, no row's marker is written, the row-selection banner is never
# printed, and no pass/fail summary line is printed (that summary means a run
# completed; this run never started).
P20_FIX=$(mktemp -d "$UNIT_TMP_ROOT/e2e-matrix-unit-p20.XXXXXX" 2>/dev/null)
if [ -n "$P20_FIX" ] && _unit_mk_fixture_tree "$P20_FIX"; then
	_unit_mk_marker_row "$P20_FIX/e2e-tests" rowA 0
	_unit_mk_marker_row "$P20_FIX/e2e-tests" rowB 0
	_unit_run_env "$P20_FIX" "$P20_FIX/markers" \
		"SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP=10 SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_REPO=999999" \
		rowA rowB
	if [ "$_RC" -eq 5 ]; then
		pass "20h: a startup-unfit driver run exits exactly 5"
	else
		fail "20h: startup-unfit driver run exited $_RC, want 5; out=$_OUT err=$_ERR"
	fi
	_unit_assert_ran "$P20_FIX/markers" rowA no "20h: row A never ran (environment unfit before any row)"
	_unit_assert_ran "$P20_FIX/markers" rowB no "20h: row B never ran (environment unfit before any row)"
	_unit_assert_no_summary "$_OUT$_ERR" "20h: no pass/fail summary line printed on a startup environment-unfit abort"
	case "$_OUT" in
		*"running 2 row(s)"*)
			fail "20h: the row-selection banner was printed even though the environment was unfit before any row ran"
			;;
		*)
			pass "20h: the row-selection banner is never reached when the environment is unfit at startup"
			;;
	esac
else
	fail "20h: could not build the fixture tree — startup-unfit path was not exercised"
	fail "20h: could not build the fixture tree — no-row-ran assertion (rowA) was not exercised"
	fail "20h: could not build the fixture tree — no-row-ran assertion (rowB) was not exercised"
	fail "20h: could not build the fixture tree — no-summary assertion was not exercised"
	fail "20h: could not build the fixture tree — banner-not-printed assertion was not exercised"
fi
if [ -n "$P20_FIX" ] && [ -d "$P20_FIX" ]; then
	case "$P20_FIX" in
		"$UNIT_TMP_ROOT"/e2e-matrix-unit-p20.*) rm -rf -- "$P20_FIX" ;;
		*) echo "  NOTE: refusing to remove unexpected fixture dir '$P20_FIX'" >&2 ;;
	esac
fi

# 20i: driver-integration, MID-RUN. Row A starts while the environment is
# healthy, runs to completion and leaves its marker — then, as its LAST
# action, rewrites a seam FILE to a too-low value. The seam is a file (not a
# bare env-var number) specifically so a value can change BETWEEN two checks
# in the same long-lived driver PROCESS without ever touching real disk space:
# row A runs in run_row's subshell, but the file it writes is real, so the
# PARENT driver's next e2e_check_disk_space call (before row B) reads it
# fresh. Row B must never run, and must never be reported as an ordinary FAIL
# — the whole point of QUM-1118 is that this is environment-unfit, not a row
# failure.
P20B_FIX=$(mktemp -d "$UNIT_TMP_ROOT/e2e-matrix-unit-p20b.XXXXXX" 2>/dev/null)
if [ -n "$P20B_FIX" ] && _unit_mk_fixture_tree "$P20B_FIX"; then
	mkdir -p "$P20B_FIX/markers"
	SEAM20B="$P20B_FIX/free-mb-tmp-seam"
	echo 999999 >"$SEAM20B"
	cat >"$P20B_FIX/e2e-tests/rowA.sh" <<EOF
MIN_ASSERTIONS=1
test_metadata() { echo ""; }
test_run() {
	: >"\${UNIT_MARKER_DIR:?UNIT_MARKER_DIR unset}/rowA"
	pass "rowA ran"
	echo 1 >"$SEAM20B"
	e2e_print_results
}
EOF
	_unit_mk_marker_row "$P20B_FIX/e2e-tests" rowB 0
	_unit_run_env "$P20B_FIX" "$P20B_FIX/markers" \
		"SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP=$SEAM20B SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_REPO=999999" \
		rowA rowB
	if [ "$_RC" -eq 5 ]; then
		pass "20i: mid-run exhaustion (between row A and row B) exits exactly 5, same code as a startup-time failure"
	else
		fail "20i: mid-run exhaustion exited $_RC, want 5; out=$_OUT err=$_ERR"
	fi
	_unit_assert_ran "$P20B_FIX/markers" rowA yes "20i: row A, which ran while the environment was still healthy, ran and left its marker"
	_unit_assert_ran "$P20B_FIX/markers" rowB no "20i: row B never ran once the environment went unfit between rows"
	case "$_OUT" in
		*"FAIL rowB"*)
			fail "20i: row B was reported as an ordinary FAIL rather than environment-unfit"
			;;
		*)
			pass "20i: row B is never classified as an ordinary row FAIL — the abort preempts classification entirely"
			;;
	esac
	_unit_assert_no_summary "$_OUT$_ERR" "20i: no pass/fail summary line printed when disk exhaustion is detected mid-run"
else
	fail "20i: could not build the fixture tree — mid-run exhaustion path was not exercised"
	fail "20i: could not build the fixture tree — row-A-ran assertion was not exercised"
	fail "20i: could not build the fixture tree — row-B-never-ran assertion was not exercised"
	fail "20i: could not build the fixture tree — not-classified-as-FAIL assertion was not exercised"
	fail "20i: could not build the fixture tree — no-summary assertion was not exercised"
fi
if [ -n "$P20B_FIX" ] && [ -d "$P20B_FIX" ]; then
	case "$P20B_FIX" in
		"$UNIT_TMP_ROOT"/e2e-matrix-unit-p20b.*) rm -rf -- "$P20B_FIX" ;;
		*) echo "  NOTE: refusing to remove unexpected fixture dir '$P20B_FIX'" >&2 ;;
	esac
fi

# 20j: REGRESSION TEST for a defect found in code review of this very section
# (QUM-1118) — an earlier revision sourced $LIB directly at driver top level
# so e2e_check_disk_space's own `exit` could propagate without a subshell.
# e2e-common.sh's re-source guard and capture-pane.sh's per-owner ledger vars
# (E2E_CAPTURE_FAULT_FILE / E2E_CAPTURE_LEDGER_OWNER) are plain shell
# variables, so a `( . "$LIB"; ... )` subshell forked from a driver that had
# ALREADY sourced $LIB inherits them — and inherits them live, so run_row's
# own `. "$LIB"` returns at the guard with NOTHING re-initialised. The thing
# that stops happening is capture-pane.sh's source-time TRUNCATION of the
# capture-fault ledger, and every row shares the same default ledger path (see
# capture-pane.sh's own comment on `$$` staying constant across `( )`
# subshells) — so row A's fault would fail every row after it, the exact
# misattributed-FAIL class QUM-1118 exists to end.
#
# Row A deliberately logs one fault to $E2E_CAPTURE_FAULT_FILE (which
# correctly fails row A itself — it really did fault) and row B makes none of
# its own. Row B must still see a CLEAN ledger, because a correct driver
# re-truncates it fresh for row B's own subshell.
P20J_FIX=$(mktemp -d "$UNIT_TMP_ROOT/e2e-matrix-unit-p20j.XXXXXX" 2>/dev/null)
if [ -n "$P20J_FIX" ] && _unit_mk_fixture_tree "$P20J_FIX"; then
	mkdir -p "$P20J_FIX/markers"
	cat >"$P20J_FIX/e2e-tests/rowA.sh" <<'EOF'
MIN_ASSERTIONS=1
test_metadata() { echo ""; }
test_run() {
	pass "rowA ran"
	echo "SIMULATED capture fault from row A" >>"$E2E_CAPTURE_FAULT_FILE"
	e2e_print_results
}
EOF
	cat >"$P20J_FIX/e2e-tests/rowB.sh" <<'EOF'
MIN_ASSERTIONS=1
test_metadata() { echo ""; }
test_run() {
	: >"${UNIT_MARKER_DIR:?UNIT_MARKER_DIR unset}/rowB"
	pass "rowB ran"
	e2e_print_results
}
EOF
	_unit_run_env "$P20J_FIX" "$P20J_FIX/markers" "" rowA rowB
	_unit_assert_ran "$P20J_FIX/markers" rowB yes "20j: row B ran at all (a false FAIL and a never-ran row look different in the marker, so this pins which one happened)"
	case "$_OUT" in
		*"PASS rowB"*)
			pass "20j: row B, which made no capture fault of its own, is NOT failed by row A's fault (ledger isolation across rows)"
			;;
		*"FAIL rowB"*)
			fail "20j: row B was FAILed — row A's capture fault leaked across rows via an unisolated ledger; out=$_OUT"
			;;
		*)
			fail "20j: no PASS/FAIL verdict line for rowB at all; out=$_OUT err=$_ERR"
			;;
	esac
	case "$_OUT" in
		*"FAIL rowA"*)
			pass "20j: row A, which DID fault, is correctly FAILed itself (the fixture's own fault is real, not a false negative)"
			;;
		*)
			fail "20j: row A was not FAILed despite deliberately logging a fault — the fixture's positive control didn't fire; out=$_OUT"
			;;
	esac
else
	fail "20j: could not build the fixture tree — ledger cross-row isolation was not exercised"
	fail "20j: could not build the fixture tree — row A's own-fault positive control was not exercised"
fi
if [ -n "$P20J_FIX" ] && [ -d "$P20J_FIX" ]; then
	case "$P20J_FIX" in
		"$UNIT_TMP_ROOT"/e2e-matrix-unit-p20j.*) rm -rf -- "$P20J_FIX" ;;
		*) echo "  NOTE: refusing to remove unexpected fixture dir '$P20J_FIX'" >&2 ;;
	esac
fi

echo "[21] QUM-974/QUM-973 e2e_recover_oauth_token return contract"

# 21a: CLAUDE_CODE_OAUTH_TOKEN already present -> returns 0 immediately,
# without walking (the "(recovered ... from ancestor" line only fires when
# the WALK finds it; printing it here would mean the fast path didn't take).
out=$(
	(
		unset SPRAWL_E2E_MATRIX_DEBUG_OAUTH_SCAN_PID
		export CLAUDE_CODE_OAUTH_TOKEN="qum974-preset-$$"
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_recover_oauth_token
		rc=$?
		echo "TOKEN=$CLAUDE_CODE_OAUTH_TOKEN"
		exit $rc
	) 2>&1
)
rc=$?
if [ "$rc" -eq 0 ]; then
	pass "21a: a pre-set CLAUDE_CODE_OAUTH_TOKEN returns 0"
else
	fail "21a: rc=$rc with CLAUDE_CODE_OAUTH_TOKEN already set (want 0); out=$out"
fi
case "$out" in
	*"recovered CLAUDE_CODE_OAUTH_TOKEN from ancestor"*)
		fail "21a: the fast path printed the ancestor-recovery line — it walked when it should not have; out=$out"
		;;
	*)
		pass "21a: the fast path does not walk (no ancestor-recovery line printed)"
		;;
esac
case "$out" in
	*"TOKEN=qum974-preset-$$"*)
		pass "21a: the pre-set token value is left untouched"
		;;
	*)
		fail "21a: the pre-set token value was altered; out=$out"
		;;
esac

# 21b: CLAUDE_CODE_OAUTH_TOKEN unset and no ancestor carries one -> returns
# NONZERO and prints a loud, actionable diagnostic (QUM-974/QUM-973). This
# must not depend on this host's REAL ancestor chain, which may or may not
# carry a token — SPRAWL_E2E_MATRIX_DEBUG_OAUTH_SCAN_PID (a test-only debug
# seam, registered below) points the walk at pid 1 instead. /proc/1/stat's
# parent field is 0, so the walk breaks on its first iteration having read no
# environ at all — deterministic regardless of the host running this suite.
# out is examined only for the sentinel words below, never for a raw token
# value — CLAUDE_CODE_OAUTH_TOKEN must never reach a log, even a synthetic
# one, on any path including this test's own failure messages.
out=$(
	(
		unset CLAUDE_CODE_OAUTH_TOKEN
		export SPRAWL_E2E_MATRIX_DEBUG_OAUTH_SCAN_PID=1
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_recover_oauth_token
		rc=$?
		if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
			echo "TOKEN_STATE=unset"
		else
			echo "TOKEN_STATE=SET-BUG"
		fi
		exit $rc
	) 2>&1
)
rc=$?
if [ "$rc" -ne 0 ]; then
	pass "21b: no available token returns nonzero (rc=$rc)"
else
	fail "21b: rc=0 with no ancestor carrying a token — a failed recovery must not report success; out=$out"
fi
case "$out" in
	*"FATAL"*"could not recover CLAUDE_CODE_OAUTH_TOKEN"*"8 ancestors"*)
		pass "21b: the diagnostic names the failure and the 8-ancestor walk"
		;;
	*)
		fail "21b: no loud diagnostic naming the failed walk; out=$out"
		;;
esac
case "$out" in
	*"setsid"*"nohup"*)
		pass "21b: the diagnostic names the detached-launch cause (setsid/nohup — QUM-973)"
		;;
	*)
		fail "21b: the diagnostic does not name setsid/nohup as the likely cause; out=$out"
		;;
esac
case "$out" in
	*"Not logged in"*)
		pass "21b: the diagnostic names the downstream symptom ('Not logged in') so a reader does not misdiagnose it as a product regression"
		;;
	*)
		fail "21b: the diagnostic does not name 'Not logged in'; out=$out"
		;;
esac
case "$out" in
	*"TOKEN_STATE=unset"*)
		pass "21b: a failed recovery leaves CLAUDE_CODE_OAUTH_TOKEN unset rather than exporting a bogus value"
		;;
	*)
		fail "21b: CLAUDE_CODE_OAUTH_TOKEN was set despite a failed recovery; out=$out"
		;;
esac

# 21c: regression control — the walk still recovers a token from a REAL
# ancestor's environ when the chain is intact (unchanged behavior from
# QUM-411). The holder is a freshly EXEC'd process (`env VAR=val bash -c`,
# not a forked-then-exported subshell): /proc/<pid>/environ reflects a
# process's environment as of its last execve(2) and is NOT guaranteed to
# pick up an `export` a live, never-re-exec'd process performs on itself
# afterward, so a fork+export holder is not a reliable fixture here. The
# holder execs, then backgrounds a plain `sleep` as its own child; the seam
# points the walk at that CHILD, so the walk's first iteration reads the
# child's PARENT (the holder) — the same one-hop relationship the walk
# exploits in production when a Bash-tool subshell's immediate parent still
# carries the token. Never echoes the recovered value — only whether it
# MATCHES the known fixture value — so a real token can never reach this
# suite's output even if some other mechanism caused a mismatch.
P21_TOK="qum974-positive-$$"
P21_PIDFILE=$(mktemp "$UNIT_TMP_ROOT/e2e-matrix-unit-p21.XXXXXX" 2>/dev/null)
if [ -n "$P21_PIDFILE" ]; then
	env CLAUDE_CODE_OAUTH_TOKEN="$P21_TOK" bash -c '
		sleep 30 &
		echo $! >"$1"
		wait
	' _ "$P21_PIDFILE" &
	P21_HOLDER_JOB=$!
	P21_CHILD_PID=""
	for _ in 1 2 3 4 5 6 7 8 9 10; do
		if [ -s "$P21_PIDFILE" ]; then
			P21_CHILD_PID=$(cat "$P21_PIDFILE")
			break
		fi
		sleep 0.2
	done
	if [ -n "$P21_CHILD_PID" ] && [ -d "/proc/$P21_CHILD_PID" ]; then
		out=$(
			(
				unset CLAUDE_CODE_OAUTH_TOKEN
				export SPRAWL_E2E_MATRIX_DEBUG_OAUTH_SCAN_PID="$P21_CHILD_PID"
				# shellcheck disable=SC1090
				. "$LIB" >/dev/null 2>&1 || exit 99
				e2e_recover_oauth_token
				rc=$?
				if [ "$CLAUDE_CODE_OAUTH_TOKEN" = "$P21_TOK" ]; then
					echo "TOKEN_MATCH"
				else
					echo "TOKEN_MISMATCH"
				fi
				exit $rc
			) 2>&1
		)
		rc=$?
		if [ "$rc" -eq 0 ]; then
			pass "21c: an intact ancestor chain still recovers a token (regression control for QUM-411)"
		else
			fail "21c: rc=$rc recovering from a live ancestor holding a token; out=$out"
		fi
		case "$out" in
			*TOKEN_MATCH*)
				pass "21c: the recovered value matches the ancestor's actual token"
				;;
			*)
				fail "21c: recovered value did not match the fixture token (mismatch or something else was recovered)"
				;;
		esac
	else
		fail "21c: could not observe the holder child's pid in time — regression control not exercised"
		fail "21c: could not observe the holder child's pid in time — recovered-value assertion not exercised"
	fi
	kill "$P21_HOLDER_JOB" 2>/dev/null
	wait "$P21_HOLDER_JOB" 2>/dev/null
	rm -f "$P21_PIDFILE"
else
	fail "21c: could not mktemp a pidfile — regression control not exercised"
	fail "21c: could not mktemp a pidfile — recovered-value assertion not exercised"
fi

# 21d: driver-integration — a row declaring needs_claude=1, exactly like
# every real row in scripts/e2e-tests/, is aborted by the CENTRALIZED check
# in scripts/e2e-matrix.sh's run_row (right after e2e_require_claude_or_skip)
# the instant recovery fails. This is NOT a bare-statement-plus-set-e effect:
# run_row's whole subshell is invoked as `run_row "$name" || rc=$?` in the
# main loop, and bash suspends errexit for the ENTIRE body of a command used
# as the left operand of `||` — a bare failing call deep inside would NOT
# abort anything on its own (verified: reverting run_row's explicit `if ! ...;
# then exit 1; fi` back to a bare call reproduces exactly the false-continue
# this section's fixture caught before the centralized fix landed). The
# centralized check must use an explicit `exit`, which is why it does.
#
# Row B, an ordinary passing row, must still run afterward (an ordinary FAIL
# does not abort the whole driver the way QUM-1118's environment-unfit exit
# does), and row A must be reported as FAIL, never SKIP — QUM-973's AC that
# this must not interact with the QUM-952 skip path.
#
# A fake `claude` on a fixture-private PATH prefix lets this run on any host
# regardless of whether a real claude binary is installed — needs_claude=1
# must reach e2e_require_claude_or_skip's SUCCESS branch (claude present) so
# execution reaches the actual code this section tests; the absent-binary
# path is already covered by [4].
P21D_FIX=$(mktemp -d "$UNIT_TMP_ROOT/e2e-matrix-unit-p21d.XXXXXX" 2>/dev/null)
if [ -n "$P21D_FIX" ] && _unit_mk_fixture_tree "$P21D_FIX"; then
	mkdir -p "$P21D_FIX/markers" "$P21D_FIX/fakebin"
	printf '#!/usr/bin/env bash\nexit 0\n' >"$P21D_FIX/fakebin/claude"
	chmod +x "$P21D_FIX/fakebin/claude"
	cat >"$P21D_FIX/e2e-tests/rowA.sh" <<'EOF'
MIN_ASSERTIONS=1
test_metadata() { echo "needs_claude=1"; }
test_run() {
	: >"${UNIT_MARKER_DIR:?UNIT_MARKER_DIR unset}/rowA-launched-session"
	pass "unreachable — recovery should have aborted the row before test_run ran"
	e2e_print_results
}
EOF
	_unit_mk_marker_row "$P21D_FIX/e2e-tests" rowB 0
	# CLAUDE_CODE_OAUTH_TOKEN= (empty), not `-u CLAUDE_CODE_OAUTH_TOKEN`: GNU
	# env requires all -u/-i OPTIONS to precede any NAME=VALUE assignment, and
	# _unit_run_env's own "TMPDIR=$fix" assignment (built in ahead of $envs)
	# already ends option-parsing — a -u placed in $envs here would be taken
	# as env's own COMMAND name and fail with "env: '-u': No such file or
	# directory". An empty value is equivalent for this function's own
	# `[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]` check.
	_unit_run_env "$P21D_FIX" "$P21D_FIX/markers" \
		"PATH=$P21D_FIX/fakebin:$PATH CLAUDE_CODE_OAUTH_TOKEN= SPRAWL_E2E_MATRIX_DEBUG_OAUTH_SCAN_PID=1" \
		rowA rowB
	_unit_assert_ran "$P21D_FIX/markers" rowA-launched-session no "21d: row A never reached past the failed recovery call (no session was launched)"
	_unit_assert_ran "$P21D_FIX/markers" rowB yes "21d: row B still ran after row A's ordinary failure (the driver does not abort the whole run the way an environment-unfit exit does)"
	case "$_OUT$_ERR" in
		*"FAIL rowA"*)
			pass "21d: row A is reported as an ordinary FAIL"
			;;
		*)
			fail "21d: row A was not reported as FAIL; out=$_OUT err=$_ERR"
			;;
	esac
	case "$_OUT$_ERR" in
		*"SKIP rowA"*)
			fail "21d: row A was reported as SKIP — a failed recovery must never be laundered into the QUM-952 skip bucket; out=$_OUT err=$_ERR"
			;;
		*)
			pass "21d: row A is never reported as SKIP"
			;;
	esac
	if [ "$_RC" -eq 1 ]; then
		pass "21d: the driver exits exactly 1 (an ordinary row failure), not a skip/usage/internal-invariant code"
	else
		fail "21d: driver exited $_RC, want 1; out=$_OUT err=$_ERR"
	fi
else
	fail "21d: could not build the fixture tree — row-A-aborted assertion was not exercised"
	fail "21d: could not build the fixture tree — row-B-still-ran assertion was not exercised"
	fail "21d: could not build the fixture tree — FAIL-rowA assertion was not exercised"
	fail "21d: could not build the fixture tree — not-SKIP assertion was not exercised"
	fail "21d: could not build the fixture tree — driver-exit-code assertion was not exercised"
fi
if [ -n "$P21D_FIX" ] && [ -d "$P21D_FIX" ]; then
	case "$P21D_FIX" in
		"$UNIT_TMP_ROOT"/e2e-matrix-unit-p21d.*) rm -rf -- "$P21D_FIX" ;;
		*) echo "  NOTE: refusing to remove unexpected fixture dir '$P21D_FIX'" >&2 ;;
	esac
fi

echo "[22] QUM-1181 e2e_launch_tui SPRAWL_CLAUDE forwarding + sandbox .env copy"

# A fake tmux that RECORDS the full "new-session" invocation (every argument,
# including the trailing command string carrying the env-var prefix this
# section is actually testing) to a log file, and answers "capture-pane"
# with the "weave " token immediately so wait_for_pattern's internal poll
# succeeds on its FIRST attempt — this section asserts the command string
# e2e_launch_tui BUILDS, not real claude/tmux behavior, so it must not
# actually wait out any real poll interval.
_unit_mk_faketmux_launch() {
	local dir=$1 logfile=$2
	mkdir -p "$dir/bin" || return 1
	cat >"$dir/bin/tmux" <<FAKETMUX || return 1
#!/usr/bin/env bash
if [ "\${1:-}" = "-L" ]; then shift 2; fi
cmd=\${1:-}
case "\$cmd" in
	new-session)
		printf '%s\n' "\$*" >> "$logfile"
		exit 0
		;;
	capture-pane)
		printf 'weave --o--\n'
		exit 0
		;;
	*)
		exit 0
		;;
esac
FAKETMUX
	chmod +x "$dir/bin/tmux" || return 1
}

# $1=extra e2e_launch_tui args appended after session/cols/rows (may be
# empty), $2=SPRAWL_CLAUDE value to export before sourcing the lib (may be
# empty to leave it unset). Result left in _LAUNCH_LOG (the recorded
# new-session command line) and _LAUNCH_RC.
_unit_run_launch_tui() {
	local extra_args=$1 claude_val=$2
	local fdir logfile
	fdir=$(mktemp -d "$UNIT_TMP_ROOT/e2e-matrix-unit-p22.XXXXXX" 2>/dev/null) || return 1
	logfile=$(mktemp "$UNIT_TMP_ROOT/e2e-matrix-unit-p22log.XXXXXX" 2>/dev/null) || return 1
	_unit_mk_faketmux_launch "$fdir" "$logfile" || return 1
	(
		unset SPRAWL_TMUX_SOCKET
		if [ -n "$claude_val" ]; then
			export SPRAWL_CLAUDE="$claude_val"
		else
			unset SPRAWL_CLAUDE
		fi
		export SPRAWL_ROOT="$fdir"
		export SPRAWL_BIN="/nonexistent-sprawl-binary-never-invoked"
		PATH="$fdir/bin:$PATH"
		export PATH
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		# shellcheck disable=SC2086
		e2e_launch_tui unit-p22-session 111 22 $extra_args
	) >/dev/null 2>&1
	_LAUNCH_RC=$?
	_LAUNCH_LOG=$(cat "$logfile" 2>/dev/null)
	rm -rf -- "$fdir" 2>/dev/null
	rm -f -- "$logfile" 2>/dev/null
}

# 22a: SPRAWL_CLAUDE unset in the caller's environment -> the command string
# defaults it to $REPO_ROOT/scripts/run-claude, the QUM-518 auth shim.
_unit_run_launch_tui "" ""
case "$_LAUNCH_LOG" in
	*"SPRAWL_CLAUDE='$REPO_ROOT/scripts/run-claude'"*)
		pass "22a: e2e_launch_tui defaults SPRAWL_CLAUDE to \$REPO_ROOT/scripts/run-claude when unset"
		;;
	*)
		fail "22a: default SPRAWL_CLAUDE not found in the launch command; log=$_LAUNCH_LOG rc=$_LAUNCH_RC"
		;;
esac

# 22b: SPRAWL_CLAUDE already exported by the caller -> that value is forwarded
# VERBATIM, and the default path is NOT substituted in its place.
_unit_run_launch_tui "" "/custom/qum1181/run-claude-override"
case "$_LAUNCH_LOG" in
	*"SPRAWL_CLAUDE='/custom/qum1181/run-claude-override'"*)
		pass "22b: a caller-exported SPRAWL_CLAUDE is forwarded verbatim"
		;;
	*)
		fail "22b: caller-exported SPRAWL_CLAUDE was not forwarded; log=$_LAUNCH_LOG rc=$_LAUNCH_RC"
		;;
esac
case "$_LAUNCH_LOG" in
	*"$REPO_ROOT/scripts/run-claude"*)
		fail "22b: the default run-claude path leaked into the command even though the caller overrode SPRAWL_CLAUDE; log=$_LAUNCH_LOG"
		;;
	*)
		pass "22b: the default path is not substituted when the caller already set SPRAWL_CLAUDE"
		;;
esac

# 22c: the optional 4th arg (extra env tokens, e.g. for
# SPRAWL_ENABLE_TEST_TOOLS=1) is inserted into the command string — this is
# the mechanism that lets wake-live.sh / liveness-transitions.sh converge
# onto this shared helper instead of hand-rolling their own launch.
_unit_run_launch_tui "SPRAWL_ENABLE_TEST_TOOLS=1" ""
case "$_LAUNCH_LOG" in
	*"SPRAWL_ENABLE_TEST_TOOLS=1"*)
		pass "22c: an extra-env token passed as e2e_launch_tui's 4th argument reaches the launch command"
		;;
	*)
		fail "22c: the extra-env token did not reach the launch command; log=$_LAUNCH_LOG rc=$_LAUNCH_RC"
		;;
esac

# 22d/22e: e2e_make_sandbox_root copies $REPO_ROOT/.env into the new
# SPRAWL_ROOT with cp -p (mode preserved) when the repo has one — the
# scripts/run-claude shim resolves its env file as
# "${SPRAWL_ROOT:-...}/.env", i.e. the SANDBOX, not the repo, so this is the
# other half of QUM-1181 alongside the SPRAWL_CLAUDE forwarding above.
P22_REPO=$(mktemp -d "$UNIT_TMP_ROOT/e2e-matrix-unit-p22repo.XXXXXX" 2>/dev/null)
if [ -n "$P22_REPO" ]; then
	printf 'CLAUDE_CODE_OAUTH_TOKEN=qum1181-fixture-token\n' >"$P22_REPO/.env"
	chmod 0600 "$P22_REPO/.env"
	out=$(
		(
			export REPO_ROOT="$P22_REPO"
			# shellcheck disable=SC1090
			. "$LIB" >/dev/null 2>&1 || exit 99
			e2e_make_sandbox_root "qum1181-p22"
			echo "ROOT=$SPRAWL_ROOT"
		) 2>&1
	)
	P22_ROOT=$(printf '%s\n' "$out" | sed -n 's/^ROOT=//p' | tail -1)
	if [ -n "$P22_ROOT" ] && [ -f "$P22_ROOT/.env" ] && diff -q "$P22_REPO/.env" "$P22_ROOT/.env" >/dev/null 2>&1; then
		pass "22d: e2e_make_sandbox_root copies a present .env into the new SPRAWL_ROOT byte-for-byte"
	else
		fail "22d: .env was not copied into the sandbox root (or its content differs); root=$P22_ROOT out=$out"
	fi
	if [ -n "$P22_ROOT" ] && [ -f "$P22_ROOT/.env" ]; then
		P22_MODE=$(stat -c '%a' "$P22_ROOT/.env" 2>/dev/null)
		if [ "$P22_MODE" = "600" ]; then
			pass "22d: the copied .env preserves the 0600 mode CLAUDE.md requires (cp -p, not a plain cp)"
		else
			fail "22d: copied .env mode is '$P22_MODE', want 600; root=$P22_ROOT"
		fi
	else
		fail "22d: copied .env is missing — mode assertion not exercised"
	fi
	[ -n "$P22_ROOT" ] && [ -d "$P22_ROOT" ] && case "$P22_ROOT" in
		/tmp/*) rm -rf -- "$P22_ROOT" ;;
	esac
else
	fail "22d: could not build the fixture repo — .env-copy assertion was not exercised"
	fail "22d: could not build the fixture repo — mode-preservation assertion was not exercised"
fi
rm -rf -- "$P22_REPO" 2>/dev/null

# 22e (negative direction, QUM-1181's own AC): a repo with NO .env leaves the
# sandbox with none too. This function must relay a credential that already
# exists, never manufacture one — the failure mode this issue exists to
# prevent is a fix that makes every row pass regardless of whether auth is
# actually configured.
P22_REPO_NOENV=$(mktemp -d "$UNIT_TMP_ROOT/e2e-matrix-unit-p22noenv.XXXXXX" 2>/dev/null)
if [ -n "$P22_REPO_NOENV" ]; then
	out=$(
		(
			export REPO_ROOT="$P22_REPO_NOENV"
			# shellcheck disable=SC1090
			. "$LIB" >/dev/null 2>&1 || exit 99
			e2e_make_sandbox_root "qum1181-p22-noenv"
			echo "ROOT=$SPRAWL_ROOT"
		) 2>&1
	)
	P22N_ROOT=$(printf '%s\n' "$out" | sed -n 's/^ROOT=//p' | tail -1)
	if [ -n "$P22N_ROOT" ] && [ ! -e "$P22N_ROOT/.env" ]; then
		pass "22e: a repo with no .env leaves the sandbox root with none — no credential is manufactured"
	else
		fail "22e: a .env appeared in the sandbox despite the repo having none; root=$P22N_ROOT out=$out"
	fi
	[ -n "$P22N_ROOT" ] && [ -d "$P22N_ROOT" ] && case "$P22N_ROOT" in
		/tmp/*) rm -rf -- "$P22N_ROOT" ;;
	esac
else
	fail "22e: could not build the no-.env fixture repo — negative-direction assertion was not exercised"
fi
rm -rf -- "$P22_REPO_NOENV" 2>/dev/null

# 22f: end-to-end tie between QUM-1181 and QUM-974/QUM-973 — a host with no
# .env AND no ancestor-recoverable token still fails LOUDLY, exactly like a
# real row's shape (e2e_make_sandbox_root then e2e_recover_oauth_token).
# This is the concrete demonstration of QUM-1181's own AC: "a run on a host
# with no .env still fails or skips loudly — it must not silently pass."
out=$(
	(
		unset CLAUDE_CODE_OAUTH_TOKEN
		export SPRAWL_E2E_MATRIX_DEBUG_OAUTH_SCAN_PID=1
		export REPO_ROOT="$UNIT_TMP_ROOT"
		P22G=$(mktemp -d "$UNIT_TMP_ROOT/e2e-matrix-unit-p22g.XXXXXX") || exit 98
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_make_sandbox_root "qum1181-p22g"
		[ -e "$SPRAWL_ROOT/.env" ] && echo "UNEXPECTED_ENV_PRESENT"
		e2e_recover_oauth_token
		rc=$?
		rm -rf -- "$SPRAWL_ROOT" "$P22G" 2>/dev/null
		exit $rc
	) 2>&1
)
rc=$?
if [ "$rc" -ne 0 ]; then
	pass "22f: no .env + no ancestor token still fails loudly end-to-end (rc=$rc), never silently passes"
else
	fail "22f: end-to-end no-auth path returned 0 — this is the exact vacuous-pass failure mode QUM-1181 warns against; out=$out"
fi
case "$out" in
	*UNEXPECTED_ENV_PRESENT*)
		fail "22f: a .env appeared from nowhere in the sandbox during the end-to-end run; out=$out"
		;;
	*)
		pass "22f: no .env was manufactured during the end-to-end run"
		;;
esac

# Summary
# ----------------------------------------------------------------------------
echo
echo "=== unit results: $PASS passed / $FAIL failed ==="
_total=$((PASS + FAIL))
# A nested [16b] child skips section [16], so it is held to its own lower floor.
# Keyed on the guard being SET at all, not on the nonce validating: the unbacked-value
# branch runs exactly the same assertions plus one deliberate `fail`, and 16c needs
# that run to fail on the FAIL count with its own message rather than on the floor.
_floor=$MIN_ASSERTIONS
if [ -n "${UNIT_NESTED_SEAM_CHECK:-}" ]; then
	_floor=$MIN_ASSERTIONS_NESTED
fi
if [ "$_total" -lt "$_floor" ]; then
	echo "  FAIL: only $_total assertions ran, expected at least $_floor — a section died early, so this run measured less than it claims and its green is not attributable" >&2
	exit 1
fi
if [ "$FAIL" -gt 0 ]; then
	exit 1
fi
exit 0
