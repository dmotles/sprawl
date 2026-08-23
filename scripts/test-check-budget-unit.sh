#!/usr/bin/env bash
#
# test-check-budget-unit.sh (QUM-1289)
#
# Guards scripts/check-budget.sh, the thing that stops `make check` decaying back
# into the merge gate one addition at a time — which is exactly how the current
# single-gate state arose.
#
# TWO MECHANISMS, AND ONLY ONE IS A TIMING COMPARISON. dmotles's decision was
# "loud budget overage, NOT BLOCKING", and a warn that could be mistaken for
# success would violate CLAUDE.md's no-silently-succeeding-fallback rule. So:
#
#   (a) STRUCTURAL, HARD FAIL. Every step in CHECK_STEPS must declare a budget in
#       scripts/testdata/check-budget.conf; a declaration may not name a step that
#       is not in CHECK_STEPS; and the declared sum may not exceed the ceiling.
#       This is the real assertion. It cannot flake on host load, and it can be
#       hard without ever blocking an honest commit.
#
#   (b) WALL CLOCK, LOUD WARN, EXIT 0. Per dmotles. Names the dominant step and
#       points at the follow-up issue. Never blocks.
#
# The residual risk, stated rather than discovered later: a step whose DECLARED
# budget is honest but whose ACTUAL runtime regresses will only warn. (a) catches
# any newly-added step, and `make validate` stays fully hard-gated at merge.
#
# Pure shell. No claude, no tmux, no go.

set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
BUDGET=$REPO_ROOT/scripts/check-budget.sh

MIN_ASSERTIONS=19

TMPBASE=${TMPDIR:-/tmp}
SCRATCH=$(mktemp -d "$TMPBASE/sprawl-check-budget.XXXXXX") || {
  echo "FATAL: mktemp -d failed under $TMPBASE" >&2; exit 1; }
case "$SCRATCH" in
  /*) ;;
  *) echo "FATAL: mktemp returned a non-absolute path: '$SCRATCH'" >&2; exit 1 ;;
esac
cleanup() {
  case "$SCRATCH" in
    "$TMPBASE"/sprawl-check-budget.*) rm -rf "$SCRATCH" ;;
    *) echo "WARN: refusing to remove unexpected SCRATCH '$SCRATCH'" >&2 ;;
  esac
}
trap cleanup EXIT

PASSES=0
FAILURES=0
pass() { PASSES=$((PASSES + 1)); echo "  PASS: $1"; }
fail() { FAILURES=$((FAILURES + 1)); echo "  FAIL: $1" >&2; }

echo "=== check-budget gate (QUM-1289) ==="

if [ ! -x "$BUDGET" ]; then
  fail "[0] $BUDGET does not exist or is not executable"
  echo "=== Results: $PASSES passed, $FAILURES failed ==="
  exit 1
fi
pass "[0] check-budget.sh exists and is executable"

# --- (a) structural mode -------------------------------------------------
mkconf() { printf '%s\n' "$@" > "$SCRATCH/conf"; }

# The happy path.
mkconf "alpha 10" "beta 20"
"$BUDGET" --structural --steps "alpha beta" --conf "$SCRATCH/conf" --ceiling 45 >/dev/null 2>&1
if [ $? -eq 0 ]; then
  pass "[a1] a fully-declared step list within the ceiling passes"
else
  fail "[a1] a fully-declared step list within the ceiling was rejected"
fi

# An UNDECLARED step must hard-fail. This is the "one addition at a time" defence.
mkconf "alpha 10"
out=$("$BUDGET" --structural --steps "alpha beta" --conf "$SCRATCH/conf" --ceiling 45 2>&1)
if [ $? -ne 0 ]; then
  pass "[a2] an undeclared step HARD FAILS"
  case "$out" in
    *beta*) pass "[a2] the failure names the undeclared step" ;;
    *) fail "[a2] the failure does not name 'beta', so it is not actionable" ;;
  esac
else
  fail "[a2] an undeclared step was accepted — a new step could be added to the commit gate with no budget at all"
fi

# A declaration for a step NOT in CHECK_STEPS must fail: a stale conf silently
# inflates the sum and hides real headroom.
mkconf "alpha 10" "ghost 30"
if ! "$BUDGET" --structural --steps "alpha" --conf "$SCRATCH/conf" --ceiling 45 >/dev/null 2>&1; then
  pass "[a3] a declaration naming a non-existent step HARD FAILS"
else
  fail "[a3] a stale declaration was accepted"
fi

# Over the ceiling must hard-fail, and say the sum.
mkconf "alpha 30" "beta 30"
out=$("$BUDGET" --structural --steps "alpha beta" --conf "$SCRATCH/conf" --ceiling 45 2>&1)
if [ $? -ne 0 ]; then
  pass "[a4] a declared sum over the ceiling HARD FAILS"
  case "$out" in
    *60*) pass "[a4] the failure prints the offending sum" ;;
    *) fail "[a4] the failure does not print the sum (60), so the reader cannot see how far over it is" ;;
  esac
else
  fail "[a4] a declared sum of 60 against a ceiling of 45 was accepted"
fi

# A malformed declaration must not be silently skipped.
mkconf "alpha notanumber" "beta 10"
if ! "$BUDGET" --structural --steps "alpha beta" --conf "$SCRATCH/conf" --ceiling 45 >/dev/null 2>&1; then
  pass "[a5] a non-numeric budget HARD FAILS rather than being ignored"
else
  fail "[a5] a non-numeric budget was silently skipped — that step would then be effectively unbudgeted"
fi

# An EMPTY conf against a non-empty step list must fail, not vacuously pass.
: > "$SCRATCH/conf"
if ! "$BUDGET" --structural --steps "alpha" --conf "$SCRATCH/conf" --ceiling 45 >/dev/null 2>&1; then
  pass "[a6] an empty conf against a non-empty step list HARD FAILS"
else
  fail "[a6] an empty conf passed — the gate is satisfied by declaring nothing"
fi

# --- (b) wall-clock mode: loud, but never blocking ---------------------
out=$("$BUDGET" --elapsed 30 --budget 60 --timings "$SCRATCH/none" 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then
  pass "[b1] under budget exits 0"
else
  fail "[b1] under budget exited $rc"
fi
case "$out" in
  *OVER*) fail "[b1] under-budget output claims it is OVER" ;;
  *) pass "[b1] under-budget output does not claim an overage" ;;
esac

out=$("$BUDGET" --elapsed 90 --budget 60 --timings "$SCRATCH/none" 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then
  pass "[b2] OVER budget still exits 0 (dmotles: NOT BLOCKING)"
else
  fail "[b2] over budget exited $rc — it must warn, not block"
fi
# Loud enough that it cannot be mistaken for success (forge's condition).
loud=0
case "$out" in *OVER*) loud=$((loud+1)) ;; esac
case "$out" in *'!!!'*) loud=$((loud+1)) ;; esac
# NOT QUM-1307: that pointer is now conditional on supervisor actually being the
# dominant package (see [b4]). "dominant" is what is always present.
case "$out" in *dominant*) loud=$((loud+1)) ;; esac
if [ "$loud" -eq 3 ]; then
  pass "[b2] the overage warning is LOUD: says OVER, uses a !!! banner, and names the dominant cost"
else
  fail "[b2] the overage warning is not loud enough ($loud of 3 markers) — a warn that reads like success is the failure mode dmotles's 'loud' guards against"
fi
case "$out" in
  *"NOT BLOCKING"*) pass "[b2] the warning states it is not blocking, so nobody reads it as a refused commit" ;;
  *) fail "[b2] the warning does not say it is non-blocking" ;;
esac

# It must name the DOMINANT step when timings are available (forge's condition).
mkdir -p "$SCRATCH/t"
printf 'total_wall_s=90\nstep\tcheap-thing\t1.00\t0\nstep\tcheck-test-race\t70.00\t0\n' \
  > "$SCRATCH/t/baseline.observed"
out=$("$BUDGET" --elapsed 90 --budget 60 --timings "$SCRATCH/t" 2>&1)
case "$out" in
  *check-test-race*) pass "[b3] the warning names the dominant step from the timings" ;;
  *) fail "[b3] the warning does not name the dominant step, so the reader learns what to fix from nothing" ;;
esac

# --- [b4] the overage attribution must be DERIVED, not hardcoded ----------
# It used to state "the known gap is internal/supervisor, tracked by QUM-1307"
# unconditionally. QA measured that wrong in the COMMON case: on the smallest
# realistic Go closure the dominant package is internal/hub and supervisor is not
# in scope at all. A warning that blames the wrong package while calling the
# overage "expected" and "NOT a defect in your change" misdirects the reader,
# which is worse than saying nothing.
mkdir -p "$SCRATCH/t2"
mkpkg() { printf 'total_wall_s=%s\nstep\tcheck-test-race\t45.00\t0\npkg\t%s\t%s\n' "$1" "$2" "$3" > "$SCRATCH/t2/baseline.observed"; }

mkpkg 61 github.com/dmotles/sprawl/internal/hub 33.590
out=$("$BUDGET" --elapsed 61 --budget 60 --timings "$SCRATCH/t2" 2>&1)
case "$out" in
  *internal/hub*) pass "[b4] names the ACTUAL dominant package read from the timings" ;;
  *) fail "[b4] does not name internal/hub, the dominant package in the timings it was handed" ;;
esac
case "$out" in
  *QUM-1307*) fail "[b4] cites QUM-1307 when the dominant package is NOT internal/supervisor — the misattribution QA flagged" ;;
  *) pass "[b4] does NOT cite QUM-1307 when supervisor is not the dominant package" ;;
esac

mkpkg 70 github.com/dmotles/sprawl/internal/supervisor 50.400
out=$("$BUDGET" --elapsed 70 --budget 60 --timings "$SCRATCH/t2" 2>&1)
case "$out" in
  *QUM-1307*) pass "[b4] negative control: DOES cite QUM-1307 when supervisor is dominant" ;;
  *) fail "[b4] negative control failed: supervisor is dominant but QUM-1307 is absent, so that branch never runs" ;;
esac

out=$("$BUDGET" --elapsed 70 --budget 60 --timings "$SCRATCH/definitely-absent" 2>&1)
case "$out" in
  *"dominant package: unknown"*) pass "[b4] reports the dominant package as UNKNOWN rather than omitting the line" ;;
  *) fail "[b4] omits the dominant-package line when timings are absent — a missing line reads as 'nothing notable here'" ;;
esac

echo "=== Results: $PASSES passed, $FAILURES failed ==="
observed=$((PASSES + FAILURES))
if [ "$observed" -lt "$MIN_ASSERTIONS" ]; then
  echo "  FAIL: only $observed assertion(s) ran but MIN_ASSERTIONS=$MIN_ASSERTIONS — this gate measured less than it claims (QUM-1029)" >&2
  exit 1
fi
[ "$FAILURES" -eq 0 ] || exit 1
exit 0
