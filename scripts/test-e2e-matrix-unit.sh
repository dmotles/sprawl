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

pass() {
	PASS=$((PASS + 1))
	echo "  PASS: $1"
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
	fail "scripts/test-merge-reuse-e2e.sh missing"
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
# Summary
# ----------------------------------------------------------------------------
echo
echo "=== unit results: $PASS passed / $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
	exit 1
fi
exit 0
