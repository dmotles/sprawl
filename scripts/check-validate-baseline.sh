#!/usr/bin/env bash
#
# check-validate-baseline.sh (QUM-1286)
#
# Validates scripts/testdata/validate-baseline.observed — the recorded `make
# validate` timing baseline.
#
# WHY A CHECKER AND NOT A DOC. The tree's previous validate baseline was prose in
# a Makefile comment, undated and unchecked, and nobody could tell whether it
# still described the tree — see the CONFOUND note in scripts/validate-timed.sh
# for why the size of its drift is NOT derivable from it. A number a human must
# remember to update is a number that goes stale silently, and a stale
# measurement that reads as current is worse than none, because a reader reasons
# from it. So the baseline lives in a data file with a checker wired into `make
# validate`, on the same pattern as
# scripts/testdata/always-loaded-manifest.observed.
#
# The load-bearing assertion is the STEP SET: it must equal the live
# VALIDATE_STEPS exactly, both directions. That is what makes a rot IMPOSSIBLE to
# ship quietly rather than merely discouraged — add or remove a validate step and
# this fails until the baseline is re-measured in the same commit. A calendar age
# check alone would not do it: the step list is what changes when validate
# changes.
#
# WHAT IT DELIBERATELY DOES NOT CHECK: today's durations against the recorded
# ones. Duration is bimodal by construction (test-race carries no -count=1, and
# GOLANGCI_LINT_CACHE is per-worktree) and this host runs several agents
# concurrently, so a percentage threshold on wall clock would be a flake
# generator, not a gate. Do not add one. The magnitudes behind that claim are in
# the baseline itself, dated; this comment deliberately quotes none, because an
# unchecked number in a comment is the artifact this file replaces.
#
# EXIT CODES
#   0  baseline is well-formed, dated, in-window, leak-clean, and matches the
#      live step set
#   1  a check failed (the reason is printed, one line per failure)
#   2  usage error / the baseline file is unreadable
#
# There is deliberately no skip path: an unreadable baseline is exit 2, never 0.
#
# PORTABILITY: the date legs use GNU `date -d`. On a host without it every date
# check reports "not a parseable date" and validate goes red for an environmental
# reason rather than a real one. That is acceptable here because this repo is
# Linux-only in practice (the same assumption the e2e harness already makes for
# setsid/flock), and it fails LOUDLY rather than silently skipping — but if that
# ever stops being true, this is the line to revisit.
#
# SPRAWL_BASELINE_MAX_AGE_DAYS is a knob for testing the age leg. Note what it
# cannot do: it does not affect the step-set assertion, which is the load-bearing
# gate here, so turning the age window up does not disarm drift detection.
#
# THE BOOTSTRAP CIRCULARITY, and why `bootstrap=true` exists.
#
# Two validate steps assert properties of a file validate itself produces, so a
# drifted step set fails BOTH `make validate` and `make validate-baseline`, and
# there is no way to record a fresh baseline — while the file says DO NOT
# HAND-EDIT. Escaping that needs exactly one promotion of a file measured while
# the tree was still inconsistent, and such a file necessarily records a FAILING
# step (rc != 0), whose duration is a time-to-failure rather than the cost of the
# work.
#
# So a file carrying `bootstrap=true` — written by the driver itself, only when a
# step actually failed, never by hand — is accepted here with a loud NOTE on
# every run rather than rejected. That is not a silent success: it prints on
# every single `make validate` until it is gone, which is the opposite of silent.
# `--final` is the arm that refuses it, and `make validate-baseline` uses
# `--final` on its confirming pass, so the state that gets COMMITTED is always
# from a fully green run. A bootstrap file cannot persist quietly.
#
# usage: check-validate-baseline.sh [--final] <baseline-file> [expected-steps-file]
# With no expected-steps-file, the live step set is read from
# `make print-validate-steps` in the repo containing this script.
set -uo pipefail
export LC_ALL=C

# The staleness window. Deliberately generous: a short window turns `make
# validate` red for a calendar reason in the middle of somebody's unrelated
# commit, which trains people to bypass validate. A warn-only age check is worse
# still — that is the silently-succeeding fallback CLAUDE.md forbids. So the
# window is long enough to be real rather than a treadmill, and the step-set
# assertion above is what catches actual drift.
MAX_AGE_DAYS=${SPRAWL_BASELINE_MAX_AGE_DAYS:-180}

REQUIRED_KEYS="recorded cores go os_arch go_cache lint_cache total_wall_s driver_overhead_s"
MIN_PKG_ROWS=5

FAILURES=0
bad() {
  FAILURES=$((FAILURES + 1))
  echo "check-validate-baseline: $1" >&2
}

FINAL=0
if [ "${1:-}" = "--final" ]; then
  FINAL=1
  shift
fi
if [ $# -lt 1 ] || [ $# -gt 2 ]; then
  echo "usage: check-validate-baseline.sh [--final] <baseline-file> [expected-steps-file]" >&2
  exit 2
fi
FILE=$1
if [ ! -r "$FILE" ] || [ ! -s "$FILE" ]; then
  echo "check-validate-baseline: baseline missing or empty: $FILE" >&2
  echo "check-validate-baseline: record one with 'make validate-baseline'" >&2
  exit 2
fi

if [ $# -eq 2 ]; then
  EXPECTED_STEPS=$(cat "$2")
else
  HERE=$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)
  REPO=$(cd "$HERE/.." && pwd)
  EXPECTED_STEPS=$(env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL \
    make --no-print-directory -C "$REPO" print-validate-steps 2>/dev/null)
  if [ -z "$EXPECTED_STEPS" ]; then
    echo "check-validate-baseline: could not read the live step list via 'make print-validate-steps'" >&2
    exit 2
  fi
fi

# --- required keys
for k in $REQUIRED_KEYS; do
  if ! grep -qE "^$k=..*" "$FILE"; then
    bad "missing or empty required key: $k="
  fi
done

# --- dated, parseable, not in the future, in window
RECORDED=$(sed -n 's/^recorded=\([0-9][0-9-]*\)$/\1/p' "$FILE" | head -1)
if [ -z "$RECORDED" ]; then
  bad "recorded= is absent or not a YYYY-MM-DD date — an UNDATED baseline is exactly the defect this file exists to prevent"
else
  REC_S=$(date -d "$RECORDED" +%s 2>/dev/null)
  if [ -z "$REC_S" ]; then
    bad "recorded=$RECORDED is not a parseable date"
  else
    NOW_S=$(date +%s)
    if [ "$REC_S" -gt "$NOW_S" ]; then
      bad "recorded=$RECORDED is in the future — the baseline claims a measurement that has not happened"
    else
      AGE_DAYS=$(((NOW_S - REC_S) / 86400))
      if [ "$AGE_DAYS" -gt "$MAX_AGE_DAYS" ]; then
        bad "baseline is ${AGE_DAYS}d old (limit ${MAX_AGE_DAYS}d) — re-measure with 'make validate-baseline' and commit the result"
      fi
    fi
  fi
fi

# --- step rows: set equality, ORDER, duplicates, and exit status
#
# Order matters now and did not before. Since validate became a serialized recipe
# driven by $(VALIDATE_STEPS), that variable determines EXECUTION ORDER, so a
# reorder changes what validate does. A set comparison (`sort -u` both sides)
# cannot see a reorder, and cannot see a duplicate either.
BSTEPS_ORDERED=$(sed -n 's/^step\t\([^\t]*\)\t.*/\1/p' "$FILE")
ESTEPS_ORDERED=$(printf '%s\n' "$EXPECTED_STEPS" | grep .)
BSTEPS=$(printf '%s\n' "$BSTEPS_ORDERED" | sort -u)
ESTEPS=$(printf '%s\n' "$ESTEPS_ORDERED" | sort -u)
if [ -z "$BSTEPS_ORDERED" ]; then
  bad "baseline records no 'step' rows at all"
else
  MISSING=$(comm -13 <(printf '%s\n' "$BSTEPS") <(printf '%s\n' "$ESTEPS") | tr '\n' ' ')
  EXTRA=$(comm -23 <(printf '%s\n' "$BSTEPS") <(printf '%s\n' "$ESTEPS") | tr '\n' ' ')
  if [ -n "$MISSING" ]; then
    bad "baseline is missing a live validate step: $MISSING — validate's step list changed, so the recorded timings describe a different gate; re-measure with 'make validate-baseline'"
  fi
  if [ -n "$EXTRA" ]; then
    bad "baseline records a step that validate no longer runs: $EXTRA — re-measure with 'make validate-baseline'"
  fi
  # Duplicates: a step recorded twice would also satisfy set equality.
  DUPES=$(printf '%s\n' "$BSTEPS_ORDERED" | sort | uniq -d | tr '\n' ' ')
  if [ -n "$DUPES" ]; then
    bad "baseline records a step more than once: $DUPES"
  fi
  # Order, checked only once the sets agree, so a drifted set reports as drift
  # rather than as a confusing order mismatch.
  if [ -z "$MISSING" ] && [ -z "$EXTRA" ] && [ -z "$DUPES" ] &&
    [ "$BSTEPS_ORDERED" != "$ESTEPS_ORDERED" ]; then
    bad "baseline records validate's steps in a DIFFERENT ORDER than VALIDATE_STEPS: baseline=[$(printf '%s' "$BSTEPS_ORDERED" | tr '\n' ' ')] live=[$(printf '%s' "$ESTEPS_ORDERED" | tr '\n' ' ')] — order is execution order since validate became a serialized recipe, so this is a real change to what the gate does"
  fi
fi

# Every step row must carry an rc, and it must be 0. A failing step exits EARLY,
# so its duration is time-to-failure rather than the cost of the work — recording
# that as a baseline is a wrong number, not a missing one. RECORDING_PASS exists
# for `make validate-baseline`'s bootstrap pass only; see the Makefile.
NO_RC=$(awk -F'\t' '$1=="step" && NF<4 {print $2}' "$FILE" | tr '\n' ' ')
if [ -n "$NO_RC" ]; then
  bad "step row(s) carry no exit status: $NO_RC — re-record with 'make validate-baseline' (the rc column was added by QUM-1286 rework; an older baseline predates it)"
fi
IS_BOOTSTRAP=0
grep -qx 'bootstrap=true' "$FILE" && IS_BOOTSTRAP=1
BAD_RC=$(awk -F'\t' '$1=="step" && NF>=4 && $4+0 != 0 {printf "%s(rc=%s) ", $2, $4}' "$FILE")
if [ "$IS_BOOTSTRAP" -eq 1 ] && [ "$FINAL" -eq 1 ]; then
  bad "this baseline is marked bootstrap=true — it was measured while the tree was inconsistent and records a failing step ($BAD_RC). A bootstrap file must never be the committed end state; re-run 'make validate-baseline' and let its confirming pass replace it"
elif [ -n "$BAD_RC" ]; then
  if [ "$IS_BOOTSTRAP" -eq 1 ]; then
    echo "check-validate-baseline: NOTE — PROVISIONAL BASELINE (bootstrap=true): step(s) $BAD_RC failed when this was recorded, so their durations are times-to-failure, not costs of the work. Finish the recovery with 'make validate-baseline'." >&2
  else
    bad "step row(s) record a FAILING step: $BAD_RC — that duration is a time-to-failure, not the cost of the work. Re-record from a green run with 'make validate-baseline'"
  fi
fi
if [ "$IS_BOOTSTRAP" -eq 1 ] && [ -z "$BAD_RC" ]; then
  bad "bootstrap=true is set but no step row records a failure — the marker is written by the driver only when a step failed, so this file has been hand-edited"
fi

# --- the recorded run must have BYPASSED the package cache.
#
# Not a style preference. A warm run serves ~43 of 44 packages from the cache and
# can therefore report a duration for exactly ONE of them, so a per-package
# baseline recorded warm is a table of absences that reads like a table of fast
# packages. `make validate-baseline` bypasses the cache (TEST_RACE_FLAGS=-count=1)
# for precisely this reason, and go_cache=cold is the recorded evidence that it
# did. Note the asymmetry this pins: a warm figure is not a weaker baseline, it is
# a DIFFERENT measurement, and the two must not be compared.
GO_CACHE=$(sed -n 's/^go_cache=\(.*\)$/\1/p' "$FILE" | head -1)
if [ "$GO_CACHE" != "cold" ]; then
  bad "go_cache=$GO_CACHE — a baseline must be recorded with the package cache BYPASSED, or almost every package reports no duration at all. Re-record with 'make validate-baseline'"
fi

# --- per-package rows
NPKG=$(grep -c '^pkg	' "$FILE")
[ -n "$NPKG" ] || NPKG=0
if [ "$NPKG" -lt "$MIN_PKG_ROWS" ]; then
  bad "baseline records $NPKG per-package rows, want at least $MIN_PKG_ROWS"
fi

# --- numeric sanity. An all-zero artifact satisfied every check above, because
# they only asserted non-emptiness — and an all-zero table is precisely what a
# dry run produces, which is the shape the driver's -n/-q/-t guard exists to
# prevent. A baseline of zeros is not a cheap gate, it is an unmeasured one.
TOTAL_WALL=$(sed -n 's/^total_wall_s=\(.*\)$/\1/p' "$FILE" | head -1)
if ! awk -v v="${TOTAL_WALL:-0}" 'BEGIN{exit !(v+0 > 0)}'; then
  bad "total_wall_s='${TOTAL_WALL:-<absent>}' is not greater than zero — this artifact records no measurement at all"
fi
STEP_SUM=$(awk -F'\t' '$1=="step"{s+=$3} END{printf "%.2f", s+0}' "$FILE")
if ! awk -v v="$STEP_SUM" 'BEGIN{exit !(v+0 > 0)}'; then
  bad "every recorded step duration is zero (sum=$STEP_SUM) — an all-zero table is what a DRY RUN produces, not a measurement"
fi
ZERO_PKG=$(awk -F'\t' '$1=="pkg" && $3+0 <= 0 {printf "%s ", $2}' "$FILE")
if [ -n "$ZERO_PKG" ]; then
  bad "package row(s) record a non-positive duration: $ZERO_PKG — a package cannot take zero time; this is an unmeasured row"
fi

# --- leak hygiene. This repo is PUBLIC, and a timing artifact is exactly the
# kind of file that picks up a checkout path or a host alias on its way in.
if grep -nE '/home/|/Users/|/root/' "$FILE" >/dev/null; then
  bad "contains a home-directory path (leak hazard in a PUBLIC repo): $(grep -nE '/home/|/Users/|/root/' "$FILE" | head -1)"
fi
if grep -qiE '^(host|hostname|fqdn)=' "$FILE"; then
  bad "records a hostname (leak hazard in a PUBLIC repo)"
fi

if [ "$FAILURES" -gt 0 ]; then
  echo "check-validate-baseline: $FAILURES problem(s) in ${FILE##*/}" >&2
  exit 1
fi
echo "check-validate-baseline: ${FILE##*/} OK (recorded=$RECORDED, $(printf '%s\n' "$BSTEPS" | grep -c .) steps, $NPKG packages)"
exit 0
