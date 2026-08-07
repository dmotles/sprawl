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
MIN_ASSERTIONS=248
# A [16b] nested child deliberately does NOT re-run section [16] (recursing would
# fork-bomb, and counting there would corrupt the parity comparison), so it asserts
# strictly fewer things and needs its own floor. Measured at de22410: 237; 238 after
# QUM-1155, then 240 with [15p]'s two new pins (see the parent floor above). It is
# the same number [16] emits as `NESTED-FLOOR:`. Keeping it a separate literal rather
# than reusing the parent's is the point — a child floor derived from the parent's
# count would be the parity check again, and parity is what `0 == 0` satisfies.
MIN_ASSERTIONS_NESTED=240

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
)
UNIT_SCRUB_ARGS=()
for _v in "${UNIT_SCRUBBED_VARS[@]}"; do
	UNIT_SCRUB_ARGS+=(-u "$_v")
done
unset "${UNIT_SCRUBBED_VARS[@]}"

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
		_unit_reset_markers "$MSK"
		_unit_run_env "$FIXSKIP" "$MSK" "PATH=$STUBBIN" _unit_fixture_needsclaude_ok
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
			# would otherwise hang `make validate` inside the pre-commit hook. 30s
			# is a 25x margin on a ~1.2s child, and caps the worst case across all
			# children at well under a minute. rc 124 lands in the failure branch
			# below, whose text names a verdict change — see the rc in the message
			# before believing that attribution.
			# shellcheck disable=SC2086
			env "UNIT_NESTED_SEAM_CHECK=$_nonce" "$_s=1" \
				${_timeout:+"$_timeout" -k 5 30} bash "$UNIT_SELF" >"$_clog" 2>&1
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
