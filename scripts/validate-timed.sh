#!/usr/bin/env bash
#
# validate-timed.sh (QUM-1286)
#
# Drives `make validate`'s steps one at a time and reports a per-step wall-clock
# breakdown, a total, per-package timings for the captured test step, and the
# cache state every one of those numbers is conditional on.
#
# WHY THIS EXISTS. `make validate` was perceived to be getting slower and that
# perception was unfalsifiable: no per-step timing anywhere, no CI running
# validate, and the tree's only recorded baseline was undated prose in a Makefile
# comment which had silently drifted ~25s. Concretely it costs every agent
# independently — the pre-commit hook runs validate, validate now outruns an
# agent's default 2-minute Bash timeout, and `git commit` gets killed mid-hook
# with no indication of which step it died in. The INTERRUPTED banner below is
# that indication.
#
# THE ONE PROPERTY THAT MATTERS. A timing wrapper that captures a step's
# duration and loses its exit status turns validate into a non-asserting
# fallback: a pretty table and exit 0 over a failed step, which nothing
# downstream can distinguish from success (the hook only reads the exit code).
# So: every step's status is propagated verbatim, the first failure stops the
# run, and the failing step is named. scripts/test-validate-timing-unit.sh
# sections [2]/[3]/[4] hold that down, including the tee-eats-exit-status case.
#
# WHAT IT DELIBERATELY DOES NOT DO. It does not change what validate runs, and
# it asserts nothing about durations. Duration here is bimodal by design —
# `test-race` deliberately carries no -count=1, so an unchanged tree re-runs
# from the package cache, and GOLANGCI_LINT_CACHE is per-worktree (measured 18.6s
# cold vs 1.6s warm) — and this box runs several agents at once. A threshold on
# wall clock would be a flake generator, so the numbers are reported and the
# CACHE STATE is reported beside them. A timing without its cache state is not a
# baseline.
#
# CONTRACT WITH make. The caller's recipe must invoke this via the literal
# $(MAKE) in MAKE_CMD. GNU make executes a recipe line containing that literal
# even under -n (handing `n` down via MAKEFLAGS), which is what keeps validate's
# sub-steps visible to `make -n validate` — and therefore what keeps
# scripts/test-race-gate.sh's wiring assertions able to see the race flag at all.
# Its header records the matching limit from the other side: a test invocation
# living inside a shell script is invisible to `make -n`. Under a dry run this
# script is a pure passthrough: no timing, no probes, no files written.
#
# Environment:
#   MAKE_CMD                        how to re-enter make (expanded $(MAKE) + flags)
#   SPRAWL_VALIDATE_MAKE_F          makefile to re-enter; -f is NOT propagated by make
#   SPRAWL_VALIDATE_MAKE_C          directory to re-enter
#   SPRAWL_VALIDATE_TIMING_OUT      artifact dir (default <repo>/.validate-timings)
#   SPRAWL_VALIDATE_CAPTURE_STEPS   space-separated steps whose output is parsed
#   SPRAWL_VALIDATE_TOP_N           how many packages to list (default 5)
#
# NOT `set -e`: the whole point is to observe a step's failure and keep going far
# enough to report it.
set -uo pipefail

# EPOCHREALTIME renders with the locale's decimal separator; a comma would make
# every awk comparison below silently wrong.
export LC_ALL=C

MAKE_CMD=${MAKE_CMD:-make}
read -r -a MAKE_ARR <<<"$MAKE_CMD"
MK_F=${SPRAWL_VALIDATE_MAKE_F:-}
MK_C=${SPRAWL_VALIDATE_MAKE_C:-$PWD}
CAPTURE_STEPS=${SPRAWL_VALIDATE_CAPTURE_STEPS:-test-race}
TOP_N=${SPRAWL_VALIDATE_TOP_N:-5}

if [ $# -eq 0 ]; then
  echo "usage: validate-timed.sh <step> [step...]" >&2
  exit 2
fi
STEPS=("$@")

# ------------------------------------------------------------ dry-run mode ----
# GNU make puts single-letter options in MAKEFLAGS without a leading dash, so a
# dry run shows up as `n` (possibly with other letters) in the first word.
is_dry_run() {
  local flags=${MAKEFLAGS:-} first
  first=${flags%% *}
  case "$first" in
    -*) ;;
    *n*) return 0 ;;
  esac
  case " $flags " in
    *" --dry-run "* | *" --just-print "* | *" --recon "*) return 0 ;;
  esac
  return 1
}

make_step() {
  if [ -n "$MK_F" ]; then
    "${MAKE_ARR[@]}" -C "$MK_C" -f "$MK_F" "$1"
  else
    "${MAKE_ARR[@]}" -C "$MK_C" "$1"
  fi
}

if is_dry_run; then
  # Passthrough. Each sub-make inherits `n` and prints its recipe, which is what
  # keeps `make -n validate` a faithful expansion. Emit nothing of our own and
  # touch no files: a dry run that writes is not a dry run.
  rc=0
  for step in "${STEPS[@]}"; do
    make_step "$step" || rc=$?
  done
  exit "$rc"
fi

# ------------------------------------------------------------- artifact dir ----
if [ -n "${SPRAWL_VALIDATE_TIMING_OUT:-}" ]; then
  OUT=$SPRAWL_VALIDATE_TIMING_OUT
else
  OUT=$(git -C "$MK_C" rev-parse --show-toplevel 2>/dev/null || echo "$MK_C")/.validate-timings
fi
mkdir -p "$OUT" || {
  echo "validate-timed: cannot create artifact dir $OUT" >&2
  exit 2
}

# Children signal this to make an interruption reportable rather than mysterious.
export SPRAWL_VALIDATE_DRIVER_PID=$$

now() { printf '%s' "${EPOCHREALTIME:-0}"; }
since() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.2f", b-a}'; }

STEP_NAMES=()
STEP_SECS=()
STEP_RCS=()
CURRENT_STEP=""
CHILD_PID=""
RUN_T0=$(now)
CAPTURED_ANY=0
CAPTURED_FILES=()

lint_cache_state() {
  local dir=${GOLANGCI_LINT_CACHE:-}
  if [ -z "$dir" ]; then
    dir=$( (make_step lint-cache-dir) 2>/dev/null | tail -1)
  fi
  if [ -z "$dir" ] || [ ! -d "$dir" ]; then
    echo cold
    return
  fi
  if [ -n "$(find "$dir" -type f -print -quit 2>/dev/null)" ]; then
    echo warm
  else
    echo cold
  fi
}
# Probed BEFORE any step runs: `lint` populates the cache, so a probe taken
# afterwards would report warm on every run and the field would be decoration.
LINT_CACHE=$(lint_cache_state)

# Package accounting, filled in by parse_captured below. `unknown` is a real
# value and must never render as 0 — a diagnostic surface must not print a
# number it did not measure.
PKG_TOTAL=0
PKG_CACHED=0
PKG_RAN=0
PKG_NOTESTS=0
PKG_TOP=""
GO_CACHE=unknown

# Parses the package result lines of the captured step's transcript. Shapes:
#   ok  <TAB>import/path<TAB>1.234s
#   ok  <TAB>import/path<TAB>(cached)
#   FAIL<TAB>import/path<TAB>0.512s
#   ?   <TAB>import/path<TAB>[no test files]
# `(cached)` is the per-package Go-cache warmth signal, and it is free — which is
# why this parses the human transcript instead of adding a machine-readable flag
# or a new tool dependency. It also means a failing step's output stays readable
# to whoever has to fix it.
parse_captured() {
  local f=$1
  local counts
  # $1 is trimmed before matching: go's own output pads the verdict column
  # ("ok  <TAB>") so a bare $1=="ok" comparison matches NOTHING and only the
  # unpadded FAIL lines get counted. Measured against the fixture transcript,
  # that bug reported total=1 for 8 packages — a wrong number, not an absent
  # one, which is the shape nobody re-checks.
  counts=$(awk -F'\t' '
    { v=$1; sub(/[ \t]+$/,"",v) }
    NF>=3 && (v=="ok" || v=="FAIL") {
      total++
      if ($3=="(cached)") { cached++ } else { ran++ }
      next
    }
    NF>=3 && v=="?" { notests++ }
    END { printf "%d %d %d %d", total+0, cached+0, ran+0, notests+0 }
  ' "$f")
  read -r PKG_TOTAL PKG_CACHED PKG_RAN PKG_NOTESTS <<<"$counts"
  PKG_TOP=$(awk -F'\t' '
    { v=$1; sub(/[ \t]+$/,"",v) }
    NF>=3 && (v=="ok" || v=="FAIL") && $3 ~ /^[0-9.]+s$/ {
      d=$3; sub(/s$/,"",d); printf "%.3f\t%s\n", d, $2
    }
  ' "$f" | sort -rn | head -n "$TOP_N")
  if [ "$PKG_TOTAL" -gt 0 ]; then
    if [ "$PKG_CACHED" -gt 0 ]; then GO_CACHE=warm; else GO_CACHE=cold; fi
  fi
}

print_table() {
  local total i
  total=$(since "$RUN_T0" "$(now)")
  printf '=== validate timing (total %ss) ===\n' "$total"
  for i in "${!STEP_NAMES[@]}"; do
    printf '  %7.2fs  %s%s\n' "${STEP_SECS[$i]}" "${STEP_NAMES[$i]}" \
      "$([ "${STEP_RCS[$i]}" -eq 0 ] || printf '  <-- FAILED rc=%s' "${STEP_RCS[$i]}")"
  done
  printf '  cache: lint=%s go_test=%s\n' "$LINT_CACHE" "$GO_CACHE"
  if [ "$CAPTURED_ANY" -eq 1 ]; then
    printf '  test packages: total=%s cached=%s ran=%s notests=%s\n' \
      "$PKG_TOTAL" "$PKG_CACHED" "$PKG_RAN" "$PKG_NOTESTS"
    if [ -n "$PKG_TOP" ]; then
      printf '  top packages by duration:\n'
      printf '%s\n' "$PKG_TOP" | while IFS=$'\t' read -r d p; do
        printf '    %7.2fs  %s\n' "$d" "$p"
      done
    fi
  fi
  printf '  artifacts: %s\n' "$OUT"
  write_baseline "$total"
}

# The machine-readable sibling of the table, in the format
# scripts/check-validate-baseline.sh understands. `make validate-baseline`
# promotes this file into scripts/testdata/. Deliberately records only cores /
# go version / os-arch: this is a PUBLIC repo, so no hostname and no paths.
write_baseline() {
  local total=$1 i
  {
    echo "# RECORDED BASELINE for \`make validate\`. Generated by"
    echo "# scripts/validate-timed.sh; promote a fresh one with 'make validate-baseline'."
    echo "# Checked by scripts/check-validate-baseline.sh, which runs inside validate."
    echo "#"
    echo "# DO NOT HAND-EDIT, and do not treat these numbers as a budget. Nothing"
    echo "# asserts a duration: this host runs several agents at once, and duration is"
    echo "# bimodal by construction, so a threshold here would be a flake generator."
    echo "# What IS asserted is that the step set below still equals VALIDATE_STEPS,"
    echo "# that the measurement is dated and in-window, and that no checkout path"
    echo "# leaked into this public repo. See QUM-1286."
    echo "#"
    echo "# go_cache=cold is REQUIRED, not incidental: a warm run serves ~43 of 44"
    echo "# packages from the package cache and can report a duration for one of them,"
    echo "# so a warm per-package baseline is a table of absences. lint_cache is"
    echo "# reported rather than required — GOLANGCI_LINT_CACHE is per-worktree, so a"
    echo "# fresh agent worktree pays cold and an established one does not."
    echo "#"
    echo "# Fields: total_wall_s is measured end to end. driver_overhead_s is this"
    echo "# script's own cost (probes and bookkeeping) and excludes the two structural"
    echo "# costs of stepwise timing, which are one duplicate warm 'build' (validate"
    echo "# runs it as a step and again as hooks-armed's prerequisite) and one make"
    echo "# parse per step."
    echo "recorded=$(date -u +%Y-%m-%d)"
    echo "cores=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo unknown)"
    echo "go=$(go env GOVERSION 2>/dev/null || echo unknown)"
    echo "os_arch=$(go env GOOS 2>/dev/null || echo unknown)/$(go env GOARCH 2>/dev/null || echo unknown)"
    echo "go_cache=$GO_CACHE"
    echo "lint_cache=$LINT_CACHE"
    echo "total_wall_s=$total"
    echo "driver_overhead_s=$(printf '%s\n' "${STEP_SECS[@]:-0}" | awk -v t="$total" '{s+=$1} END{d=t-s; if(d<0)d=0; printf "%.2f", d}')"
    for i in "${!STEP_NAMES[@]}"; do
      printf 'step\t%s\t%s\n' "${STEP_NAMES[$i]}" "${STEP_SECS[$i]}"
    done
    [ -n "$PKG_TOP" ] && printf '%s\n' "$PKG_TOP" | while IFS=$'\t' read -r d p; do
      printf 'pkg\t%s\t%s\n' "$p" "$d"
    done
  } >"$OUT/baseline.observed"
}

on_signal() {
  local num=$1
  [ -n "$CHILD_PID" ] && kill -TERM "$CHILD_PID" 2>/dev/null
  printf '=== validate INTERRUPTED during step %s after %ss (signal %s) ===\n' \
    "${CURRENT_STEP:-<none>}" "$(since "$RUN_T0" "$(now)")" "$num"
  print_table
  # Never 0. An interrupted validate that exits 0 is the same false green as a
  # swallowed step failure.
  exit $((128 + num))
}
# The step runs as a BACKGROUND job and is collected with `wait`, specifically so
# these traps can fire promptly: bash defers a trap until a FOREGROUND child
# returns, so a foreground step would swallow the signal for as long as it kept
# running — which is exactly the multi-minute step whose interruption we need to
# report.
trap 'on_signal 15' TERM
trap 'on_signal 2' INT

is_captured() {
  case " $CAPTURE_STEPS " in
    *" $1 "*) return 0 ;;
  esac
  return 1
}

RC=0
FAILED_STEP=""
for step in "${STEPS[@]}"; do
  CURRENT_STEP=$step
  t0=$(now)
  if is_captured "$step"; then
    capfile="$OUT/$step.txt"
    rcfile="$OUT/.rc.$step"
    rm -f "$rcfile"
    # Streams AND preserves the exit status without depending on pipefail: the
    # status is carried out of the subshell in a file rather than inferred from
    # the pipeline. `tee` is what makes a multi-minute step still watchable.
    {
      {
        make_step "$step" 2>&1
        echo $? >"$rcfile"
      } | tee "$capfile"
    } &
    CHILD_PID=$!
    wait "$CHILD_PID"
    rc=$(cat "$rcfile" 2>/dev/null || echo 1)
    case "$rc" in
      '' | *[!0-9]*) rc=1 ;;
    esac
    rm -f "$rcfile"
    CAPTURED_ANY=1
    CAPTURED_FILES+=("$capfile")
    parse_captured "$capfile"
  else
    make_step "$step" &
    CHILD_PID=$!
    wait "$CHILD_PID"
    rc=$?
  fi
  CHILD_PID=""
  STEP_NAMES+=("$step")
  STEP_SECS+=("$(since "$t0" "$(now)")")
  STEP_RCS+=("$rc")
  if [ "$rc" -ne 0 ]; then
    RC=$rc
    FAILED_STEP=$step
    break
  fi
done
CURRENT_STEP=""

# Non-vacuity. A captured step that produced no package result lines means the
# per-package measurement measured NOTHING, and an empty top-5 table reads
# exactly like a fast run. Fail loudly instead.
VACUOUS=0
if [ "$CAPTURED_ANY" -eq 1 ] && [ "$PKG_TOTAL" -eq 0 ]; then
  VACUOUS=1
fi

if [ -n "$FAILED_STEP" ]; then
  printf '=== validate FAILED at step %s (rc=%s) after %ss ===\n' \
    "$FAILED_STEP" "$RC" "$(since "$RUN_T0" "$(now)")"
fi
if [ "$VACUOUS" -eq 1 ]; then
  printf '  FAIL: captured step %s produced NO package result lines — the per-package measurement measured nothing\n' \
    "${CAPTURED_FILES[-1]##*/}" >&2
fi
print_table
if [ "$VACUOUS" -eq 1 ] && [ "$RC" -eq 0 ]; then
  RC=1
fi
exit "$RC"
