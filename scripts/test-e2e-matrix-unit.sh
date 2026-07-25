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
# 4. e2e_require_claude_or_skip honors SPRAWL_E2E_SKIP_NO_CLAUDE=1
# ----------------------------------------------------------------------------
echo "[4] e2e_require_claude_or_skip honors skip env var"
out=$(
	set +e
	# Use a subshell rather than re-execing bash, since PATH=/nonexistent
	# would break `bash -c`. The function still sees an empty PATH for its
	# own `command -v claude` lookup.
	(
		export PATH=/nonexistent
		export SPRAWL_E2E_SKIP_NO_CLAUDE=1
		# shellcheck disable=SC1090
		. "$LIB" >/dev/null 2>&1 || exit 99
		e2e_require_claude_or_skip "fixture"
	) 2>&1
)
rc=$?
if [ $rc -eq 0 ] && echo "$out" | grep -qi "SKIP"; then
	pass "skip path returns 0 with SKIP in output"
else
	fail "skip path rc=$rc out=$out"
fi

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
		cp "$LIB" "$FIXDIR/lib/e2e-common.sh" 2>/dev/null
		cp "$DRIVER" "$FIXDIR/e2e-matrix.sh" 2>/dev/null

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

		# Test 10a: claude-required fixture skipped.
		# Resolve bash by absolute path so the PATH=/nonexistent prefix can
		# scope the modified PATH to the driver process without breaking
		# the `bash` lookup itself (see comment on test 4 above).
		BASH_ABS=$(command -v bash)
		out=$(
			set +e
			PATH=/nonexistent SPRAWL_E2E_SKIP_NO_CLAUDE=1 "$BASH_ABS" "$FIXDIR/e2e-matrix.sh" _unit_fixture_claude 2>&1
		)
		rc=$?
		if [ $rc -eq 0 ] && echo "$out" | grep -qi "SKIP" && ! echo "$out" | grep -q "SHOULD NOT RUN"; then
			pass "needs_claude=1 fixture skipped under SPRAWL_E2E_SKIP_NO_CLAUDE=1"
		else
			fail "needs_claude fixture rc=$rc out=$out"
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
	local of ef
	of=$(mktemp) && ef=$(mktemp) || return 1
	if [ -n "$mdir" ]; then
		UNIT_MARKER_DIR="$mdir" bash "$fix/e2e-matrix.sh" "$@" >"$of" 2>"$ef"
	else
		bash "$fix/e2e-matrix.sh" "$@" >"$of" 2>"$ef"
	fi
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

# Summary
# ----------------------------------------------------------------------------
echo
echo "=== unit results: $PASS passed / $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
	exit 1
fi
exit 0
