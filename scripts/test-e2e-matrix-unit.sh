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
# 396 once QUM-957 added section [18] (109 assertions: capture_pane must not
# swallow a tmux failure). Re-measured on a full green run, not derived by
# arithmetic on the red one. Section [18] is pass/fail-invariant in the same way
# [17] is — every arm is a symmetric if/else or a helper that records exactly one
# verdict in either direction, and the one `for` loop iterates a fixed literal
# list — so the figure does not move when an arm flips. The EXCEPTION is
# deliberate: [18]'s outer fixture guard (unreadable lib, failed mktemp, failed
# fake-tmux build) records a single fail in place of ~90 assertions, and the floor
# is exactly what turns that into a red instead of a suspiciously small green.
MIN_ASSERTIONS=396
# A [16b] nested child deliberately does NOT re-run section [16] (recursing would
# fork-bomb, and counting there would corrupt the parity comparison), so it asserts
# strictly fewer things and needs its own floor. Measured at de22410: 237; 238 after
# QUM-1155, then 240 with [15p]'s two new pins (see the parent floor above). It is
# the same number [16] emits as `NESTED-FLOOR:`. Keeping it a separate literal rather
# than reusing the parent's is the point — a child floor derived from the parent's
# count would be the parity check again, and parity is what `0 == 0` satisfies.
# 275 once QUM-1029 added section [17], which the child DOES run; 279 with F1 and 17k.
# 388 once QUM-957 added section [18], which the child DOES run. Measured, not
# derived by subtracting [16]'s size from the parent. To re-measure: run this file
# with UNIT_NESTED_SEAM_CHECK set to any value and SUBTRACT ONE from the total —
# without a live nonce 16c's deliberate fail fires, so a hand-driven child reads
# exactly one higher than a real [16b] child ("388 passed / 1 failed" here).
MIN_ASSERTIONS_NESTED=388

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
		"TMPDIR=$nowrite" \
		"UNIT_MARKER_DIR=$UNIT_CAP_FIX/markers" \
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

	# --- 18h2 capture_pane_dump: the forensic form, now at ~45 sites --------
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
#   still reaches the operator. That spelling is NOT the defect, and ~45 of them
#   exist in the tree as harmless cargo-cult. So this pattern keeps `[^|]*`: the
#   redirect must be in the same element as the capture to count.
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
# ...and it must not report the harmless spelling, or the arm becomes noise that
# gets exempted wholesale.
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
