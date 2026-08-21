#!/usr/bin/env bash
#
# check-validate-baseline.sh (QUM-1286)
#
# Validates scripts/testdata/validate-baseline.observed — the recorded `make
# validate` timing baseline.
#
# WHY A CHECKER AND NOT A DOC. The tree's previous validate baseline was prose in
# a Makefile comment. It rotted: `internal/supervisor` grew ~33% and the recorded
# whole-suite figure drifted ~25s, and nobody found out until someone happened to
# re-measure. A number a human must remember to update is a number that goes
# stale silently, and a stale measurement that reads as current is worse than
# none. So the baseline lives in a data file with a checker wired into `make
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
# ones. Duration is bimodal by construction (test-race carries no -count=1;
# GOLANGCI_LINT_CACHE is per-worktree, measured 18.6s cold vs 1.6s warm) and this
# host runs several agents concurrently, so a percentage threshold on wall clock
# would be a flake generator, not a gate. Do not add one.
#
# EXIT CODES
#   0  baseline is well-formed, dated, in-window, leak-clean, and matches the
#      live step set
#   1  a check failed (the reason is printed, one line per failure)
#   2  usage error / the baseline file is unreadable
#
# There is deliberately no skip path: an unreadable baseline is exit 2, never 0.
#
# usage: check-validate-baseline.sh <baseline-file> [expected-steps-file]
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

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
  echo "usage: check-validate-baseline.sh <baseline-file> [expected-steps-file]" >&2
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

# --- step set equality, both directions
BSTEPS=$(sed -n 's/^step\t\([^\t]*\)\t.*/\1/p' "$FILE" | sort -u)
ESTEPS=$(printf '%s\n' "$EXPECTED_STEPS" | grep . | sort -u)
if [ -z "$BSTEPS" ]; then
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
