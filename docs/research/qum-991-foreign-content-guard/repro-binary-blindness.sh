#!/usr/bin/env bash
#
# QUM-991 AC-1 reproduction harness: is scripts/guard-employer-leak blind to
# binary staged content?
#
# Usage:  GUARD=/abs/path/to/scripts/guard-employer-leak bash repro-binary-blindness.sh
#
# Builds a throwaway git repo under /tmp with a SYNTHETIC forbidden-terms list
# (via $SPRAWL_FORBIDDEN_TERMS_FILE) and stages four fixtures, reporting the
# guard's verdict + exit code for each. Row (a) is the positive control: if it
# does not BLOCK, the harness is wrong and every other row is meaningless.
#
# !! EXIT-CODE SEMANTICS — READ BEFORE WIRING THIS ANYWHERE !!
# This script exits 0 on a successful RUN, not on a successful VERDICT: nonzero
# vs zero here does NOT mean fail vs pass. The `ASSERT FAIL` rows it prints are
# the EXPECTED FINDING (the guard failing to catch binary content), so a
# "passing" run is one that prints exactly 3 of them. Consequently:
#
#   DO NOT wire this into `make validate` as-is — it would report green forever
#   and green would mean "the gap is still open", the exact false-green shape
#   this repo's testing conventions exist to prevent.
#
# If you ever do want it gated, INVERT THE ASSERTIONS FIRST: make
# expect-not-caught the failing condition, so that a future guard which
# actually FIXES binary blindness turns these rows red and forces this script
# to be updated rather than silently continuing to assert a gap that closed.
# The only hard failures are harness faults (a fixture that did not materialise,
# or one that never reached the index) and drift in the expected finding — the
# run must print exactly the 3 `ASSERT FAIL` rows (b), (c), (d), no more and no
# fewer, and that is now checked rather than merely asserted in this comment.
# Both exit 5. Plus the assertion-count check, which exits 4 unless exactly 14 ran.
#
# /tmp hygiene: the scratch root is created by mktemp -d under /tmp and is only
# removed after a `case` guard asserts the literal prefix (pattern lifted from
# _unit_reset_markers in scripts/test-e2e-matrix-unit.sh). No rm globs.
set -uo pipefail

GUARD="${GUARD:?GUARD=/abs/path/to/scripts/guard-employer-leak required}"
[ -x "$GUARD" ] || {
	echo "FATAL: guard not executable: $GUARD" >&2
	exit 2
}

TMP_ROOT="$(mktemp -d /tmp/qum991-repro.XXXXXX)"
cleanup() {
	case "$TMP_ROOT" in
	/tmp/qum991-repro.*) rm -rf "$TMP_ROOT" ;;
	*) echo "REFUSING to clean unexpected path '$TMP_ROOT'" >&2 ;;
	esac
}
trap cleanup EXIT

# ---- synthetic forbidden-terms list (no real term ever appears here) --------
TERMS="$TMP_ROOT/forbidden-terms"
cat >"$TERMS" <<'EOF'
# category:matchtype:term
synthetic-employer:ci:ACMEGLOBALCORP
synthetic-subscription:exact:11111111-2222-3333-4444-555555555555
EOF
export SPRAWL_FORBIDDEN_TERMS_FILE="$TERMS"

REPO="$TMP_ROOT/repo"
mkdir -p "$REPO"
git -C "$REPO" init -q
git -C "$REPO" config user.email probe@example.invalid
git -C "$REPO" config user.name probe
# Keep the fleet guards inert in the scratch repo.
export SPRAWL_AGENT_IDENTITY=""

PASSES=0
FAILS=0
ASSERTIONS=0

# The rows whose `ASSERT FAIL` IS the expected finding (see the header). Tracked
# SEPARATELY from $FAILS, because $FAILS is also bumped by FIXTURE/STAGED
# harness faults: a bare `FAILS -eq 3` would be satisfied by 2 real findings
# plus 1 fixture fault, the classic compensating-errors false green. The row
# LABELS are compared too, not just the count — row (a) is the positive
# control, and "(a) (c) (d)" is three findings but a meaningless run.
EXPECTED_GAP_ROWS='(b) (c) (d) '
GAP_FAILS=0
GAP_ROWS=''

note() { printf '\n=== %s\n' "$*"; }
verdict() { # $1=label
	local rc out
	out="$(cd "$REPO" && "$GUARD" 2>&1)"
	rc=$?
	if [ "$rc" -eq 0 ]; then
		printf 'ROW %s: guard exit=%d verdict=PASSED (not caught)\n' "$1" "$rc"
	else
		printf 'ROW %s: guard exit=%d verdict=BLOCKED\n' "$1" "$rc"
		printf '%s\n' "$out" | sed 's/^/    | /'
	fi
	return $rc
}
expect() { # $1=label $2=expected(block|pass) $3=actual rc
	ASSERTIONS=$((ASSERTIONS + 1))
	if { [ "$2" = block ] && [ "$3" -ne 0 ]; } || { [ "$2" = pass ] && [ "$3" -eq 0 ]; }; then
		printf '  ASSERT ok   %s (expected %s)\n' "$1" "$2"
		PASSES=$((PASSES + 1))
	else
		printf '  ASSERT FAIL %s (expected %s, rc=%s)\n' "$1" "$2" "$3"
		FAILS=$((FAILS + 1))
		GAP_FAILS=$((GAP_FAILS + 1))
		GAP_ROWS="$GAP_ROWS${1%% *} "
	fi
}
# Assert a fixture actually materialised and is non-empty BEFORE reading a
# verdict (QUM-991's methodological warning: a missing fixture makes `git add`
# fail and the guard trivially "pass", which reads exactly like the finding).
assert_fixture() { # $1=path $2=min bytes
	local sz
	if [ ! -f "$1" ]; then
		printf '  FIXTURE FAIL %s does not exist\n' "$1"
		FAILS=$((FAILS + 1))
		ASSERTIONS=$((ASSERTIONS + 1))
		return 1
	fi
	# 2>&1 so a real failure's reason lands IN the FAIL row below rather than
	# interleaving ahead of it; the non-numeric arm catches it either way.
	sz=$(stat -c %s "$1" 2>&1)
	ASSERTIONS=$((ASSERTIONS + 1))
	# A failed `stat` leaves $sz empty, and `[ "" -lt N ]` errors with "integer
	# expression expected" and returns 2 — which is FALSE, so control would fall
	# through to the success print and a broken measurement would read as `ok`
	# (measured: four `FIXTURE ok  ...:  bytes` rows, 14 assertions, exit 0, fully
	# green). Same unfalsifiable-assertion class as the soft-degrades fixed
	# elsewhere in this directory, one layer down; fail loudly instead. The trigger
	# is realistic, not hypothetical: BSD/macOS `stat` has no `-c`.
	case "$sz" in
	'' | *[!0-9]*)
		printf '  FIXTURE FAIL could not size %s (stat gave %s) — measurement broken\n' "$1" "${sz:-<empty>}"
		FAILS=$((FAILS + 1))
		return 1
		;;
	esac
	if [ "$sz" -lt "$2" ]; then
		printf '  FIXTURE FAIL %s is %s bytes (< %s)\n' "$1" "$sz" "$2"
		FAILS=$((FAILS + 1))
		return 1
	fi
	printf '  FIXTURE ok   %s: %s bytes; file(1)=%s\n' "$(basename "$1")" "$sz" "$(file -b "$1")"
	PASSES=$((PASSES + 1))
}
assert_git_added() { # $1=path-in-repo
	ASSERTIONS=$((ASSERTIONS + 1))
	if git -C "$REPO" diff --cached --name-only | grep -qxF "$1"; then
		printf '  STAGED ok   %s is in the index\n' "$1"
		PASSES=$((PASSES + 1))
	else
		printf '  STAGED FAIL %s NOT in the index — verdict below is meaningless\n' "$1"
		FAILS=$((FAILS + 1))
	fi
}
show_diff_shape() { # $1=path
	printf '  git diff --cached --numstat: %s\n' "$(git -C "$REPO" diff --cached --numstat -- "$1" | tr '\t' ' ')"
	printf '  git diff --cached header lines:\n'
	git -C "$REPO" diff --cached -- "$1" | head -6 | sed 's/^/    > /'
	printf '  count of "+" content lines in staged diff for this path: %s\n' \
		"$(git -C "$REPO" diff --cached --unified=0 --no-color --src-prefix=a/ --dst-prefix=b/ -- "$1" | grep -c '^+[^+]' || true)"
}

# Seed one benign tracked commit so the repo has a HEAD.
echo "hello world" >"$REPO/README.md"
git -C "$REPO" add README.md
git -C "$REPO" commit -qm "seed" --no-verify
git -C "$REPO" reset -q

# ---------------------------------------------------------------- ROW (a) ----
note 'ROW (a) POSITIVE CONTROL: text log containing a listed term'
printf 'starting apply\nsubscription=11111111-2222-3333-4444-555555555555\ndone\n' >"$REPO/apply.log"
assert_fixture "$REPO/apply.log" 20
git -C "$REPO" add apply.log
assert_git_added apply.log
show_diff_shape apply.log
verdict a
expect "(a) text log with listed term is BLOCKED" block $?
git -C "$REPO" reset -q
rm -f "$REPO/apply.log"

# ---------------------------------------------------------------- ROW (b) ----
note 'ROW (b) zip archive (ZIP_DEFLATED) containing a listed term'
python3 - "$REPO/tfplan.zip" <<'PY'
import sys, zipfile
p = sys.argv[1]
with zipfile.ZipFile(p, "w", zipfile.ZIP_DEFLATED) as z:
    z.writestr("plan.txt",
        "resource_group = ACMEGLOBALCORP-rg\n"
        "subscription_id = 11111111-2222-3333-4444-555555555555\n" * 40)
PY
assert_fixture "$REPO/tfplan.zip" 100
git -C "$REPO" add tfplan.zip
assert_git_added tfplan.zip
show_diff_shape tfplan.zip
verdict b
expect "(b) zip with listed term is BLOCKED" block $?
git -C "$REPO" reset -q

# ---------------------------------------------------------------- ROW (c) ----
note 'ROW (c) NUL-containing binary with the term as LITERAL plaintext'
python3 - "$REPO/tfplan.bin" <<'PY'
import sys
p = sys.argv[1]
blob = (b"\x00\x01\x02\x00PK-not-really\x00"
        b"ACMEGLOBALCORP\x00"
        b"11111111-2222-3333-4444-555555555555\x00"
        + bytes(range(256)) * 8)
open(p, "wb").write(blob)
PY
assert_fixture "$REPO/tfplan.bin" 100
# Prove the term really is present as literal bytes in the fixture.
ASSERTIONS=$((ASSERTIONS + 1))
if grep -qa 'ACMEGLOBALCORP' "$REPO/tfplan.bin" && grep -qa '11111111-2222-3333-4444-555555555555' "$REPO/tfplan.bin"; then
	echo "  FIXTURE ok   both terms present as literal plaintext bytes (grep -a)"
	PASSES=$((PASSES + 1))
else
	echo "  FIXTURE FAIL terms NOT literally present in tfplan.bin"
	FAILS=$((FAILS + 1))
fi
ASSERTIONS=$((ASSERTIONS + 1))
if grep -qU $'\x00' "$REPO/tfplan.bin" 2>/dev/null || python3 -c "import sys;sys.exit(0 if b'\x00' in open(sys.argv[1],'rb').read() else 1)" "$REPO/tfplan.bin"; then
	echo "  FIXTURE ok   contains NUL bytes"
	PASSES=$((PASSES + 1))
else
	echo "  FIXTURE FAIL no NUL bytes"
	FAILS=$((FAILS + 1))
fi
git -C "$REPO" add tfplan.bin
assert_git_added tfplan.bin
show_diff_shape tfplan.bin
verdict c
expect "(c) NUL binary with literal plaintext term is BLOCKED" block $?

# ---------------------------------------------------------------- ROW (d) ----
note 'ROW (d) commit BOTH binaries, then whole-tree --all scan'
git -C "$REPO" add tfplan.zip tfplan.bin
git -C "$REPO" commit -qm "add binaries" --no-verify
printf '  tracked files: %s\n' "$(git -C "$REPO" ls-files | tr '\n' ' ')"
out="$(cd "$REPO" && "$GUARD" --all 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then
	printf 'ROW d: guard --all exit=%d verdict=PASSED (not caught)\n' "$rc"
else
	printf 'ROW d: guard --all exit=%d verdict=BLOCKED\n' "$rc"
	printf '%s\n' "$out" | sed 's/^/    | /'
fi
expect "(d) whole-tree --all catches committed binaries" block $rc

# Control for row (d): does --all catch a committed TEXT term? (proves --all
# itself is wired up and the list is being read in whole-tree mode)
note 'ROW (d-control) commit a TEXT file with a listed term, re-run --all'
printf 'subscription=11111111-2222-3333-4444-555555555555\n' >"$REPO/notes.txt"
assert_fixture "$REPO/notes.txt" 10
git -C "$REPO" add notes.txt
git -C "$REPO" commit -qm "add text" --no-verify
out="$(cd "$REPO" && "$GUARD" --all 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then
	printf 'ROW d-control: guard --all exit=%d verdict=PASSED\n' "$rc"
else
	printf 'ROW d-control: guard --all exit=%d verdict=BLOCKED\n' "$rc"
	printf '%s\n' "$out" | sed 's/^/    | /'
fi
expect "(d-control) whole-tree --all catches committed TEXT term" block $rc

# ---- assertion-count check -------------------------------------------------
note "SUMMARY: $PASSES ok, $FAILS unexpected, $ASSERTIONS assertions"
# EXACT, not a floor. The count is branch-invariant at 14: every counting site is
# a straight-line statement (no call is wrapped in an `if`/`&&`/`||`/loop),
# assert_fixture bumps on BOTH of its arms so its early `return 1` cannot skip a
# count, and the script is `set -uo pipefail` with NO `-e`, so a nonzero helper
# return never aborts the remaining assertions. Verified empirically: forcing a
# fixture missing still reports 14.
#
# The previous `-lt 12` left two assertions of slack against a real count of 14,
# so two could be deleted and the run would still exit 0, fully green (measured:
# deleting one gave `13 assertions`, exit 0). Nothing exploited it, but that is
# the same shape as the soft-degrading assertions fixed elsewhere in this
# directory. Bump this literal deliberately when adding a row; `-ne` also catches
# an accidental double-count, which a floor cannot.
if [ "$ASSERTIONS" -ne 14 ]; then
	echo "FATAL: assertion count mismatch: expected exactly 14, got $ASSERTIONS" >&2
	exit 4
fi
echo "(NOTE: 'ASSERT FAIL' rows above are the FINDING, not a harness error —"
echo " they record that the guard did not block content it arguably should."
echo " Expect exactly 3 of them: rows (b), (c), (d). This script exits 0 on a"
echo " successful RUN, not a successful VERDICT — see the exit-code semantics"
echo " block in the header before wiring it into any gate.)"

# The header has always CLAIMED "expect exactly 3"; until now nothing checked
# it, so a positive-control regression printing 4-5 rows still exited 0. These
# two checks make the claim an assertion. They are meta-checks, like the floor
# above, and so are deliberately NOT counted in $ASSERTIONS.
HARNESS_FAULTS=$((FAILS - GAP_FAILS))
if [ "$HARNESS_FAULTS" -ne 0 ]; then
	echo "FATAL: $HARNESS_FAULTS harness fault(s) (FIXTURE/STAGED) — NOT the expected" >&2
	echo "       finding. A fixture that never materialised or never reached the index" >&2
	echo "       makes the guard trivially 'pass', which reads exactly like the finding," >&2
	echo "       so every verdict above is meaningless. Fix the harness." >&2
	exit 5
fi
if [ "$GAP_FAILS" -ne 3 ] || [ "$GAP_ROWS" != "$EXPECTED_GAP_ROWS" ]; then
	echo "FATAL: expected exactly 3 ASSERT FAIL rows [$EXPECTED_GAP_ROWS], got $GAP_FAILS [$GAP_ROWS]." >&2
	echo "       Either the guard now catches binary content (GOOD — invert the assertions" >&2
	echo "       per the exit-code header before reusing this), or the harness drifted." >&2
	echo "       Either way this script is stale and must not be cited as evidence." >&2
	exit 5
fi
exit 0
