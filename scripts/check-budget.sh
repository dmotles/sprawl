#!/usr/bin/env bash
#
# check-budget.sh (QUM-1289)
#
# Enforces `make check`'s time budget. TWO MECHANISMS, and only one of them is a
# timing comparison — which is the whole design:
#
#   --structural   HARD FAIL. Every step in CHECK_STEPS must declare a budget in
#                  scripts/testdata/check-budget.conf; no declaration may name a
#                  step that is not in CHECK_STEPS; the declared sum must stay
#                  within the ceiling. This is the real assertion. It is what
#                  stops `check` decaying back into the merge gate one addition
#                  at a time, which is exactly how the current single-gate state
#                  arose. Being structural, it cannot flake on host load, so it
#                  can be hard without ever blocking an honest commit.
#
#   --elapsed      LOUD WARN, ALWAYS EXIT 0. dmotles's decision, 2026-08-23:
#                  "loud budget overage (NOT BLOCKING)". A hard wall-clock fail
#                  in the commit path would train agents to bypass the hook,
#                  which is strictly worse than a slow gate — and this host has
#                  ~20% measured wall-clock noise from co-tenancy alone, so an
#                  honest commit would sometimes be refused for someone else's
#                  load.
#
# Why the warn is not the forbidden silently-succeeding fallback: the assertion
# for "the budget is respected" lives in --structural, which is hard. The warn is
# a report, and it is deliberately impossible to mistake for success (a !!!
# banner, the word OVER, the dominant step, and the tracking issue).
#
# RESIDUAL RISK, stated rather than left to be discovered: a step whose DECLARED
# budget is honest but whose ACTUAL runtime regresses will only warn. Nothing
# hard-fails on wall clock. --structural catches any newly-added step, and
# `make validate` stays fully hard-gated at merge.

set -uo pipefail

MODE=""
STEPS=""
CONF=""
CEILING=45
ELAPSED=""
BUDGET=60
TIMINGS=""

while [ $# -gt 0 ]; do
  case "$1" in
    --structural) MODE=structural; shift ;;
    --steps)      STEPS=${2:-}; shift 2 ;;
    --conf)       CONF=${2:-}; shift 2 ;;
    --ceiling)    CEILING=${2:-45}; shift 2 ;;
    --elapsed)    MODE=elapsed; ELAPSED=${2:-}; shift 2 ;;
    --budget)     BUDGET=${2:-60}; shift 2 ;;
    --timings)    TIMINGS=${2:-}; shift 2 ;;
    *) echo "check-budget: unknown argument '$1'" >&2; exit 2 ;;
  esac
done

# ---------------------------------------------------------------- structural
if [ "$MODE" = structural ]; then
  [ -n "$CONF" ] || { echo "check-budget: --structural needs --conf" >&2; exit 2; }
  [ -f "$CONF" ] || { echo "check-budget: conf '$CONF' does not exist" >&2; exit 2; }
  [ -n "$STEPS" ] || { echo "check-budget: --structural needs a non-empty --steps" >&2; exit 2; }

  problems=0
  sum=0

  # Parse declarations. A malformed value is a PROBLEM, never a skip: skipping it
  # would leave that step effectively unbudgeted while the file looks complete.
  declared_names=""
  while read -r name secs rest; do
    case "$name" in ''|'#'*) continue ;; esac
    if [ -n "${rest:-}" ]; then
      echo "check-budget: '$CONF': trailing junk after '$name $secs': $rest" >&2
      problems=$((problems + 1)); continue
    fi
    case "$secs" in
      ''|*[!0-9]*)
        echo "check-budget: '$CONF': step '$name' has a non-numeric budget '$secs' — a step with an unparseable budget is effectively unbudgeted" >&2
        problems=$((problems + 1)); continue ;;
    esac
    declared_names="$declared_names $name"
    sum=$((sum + secs))
  done < "$CONF"

  # Every step must be declared.
  for s in $STEPS; do
    case " $declared_names " in
      *" $s "*) ;;
      *) echo "check-budget: step '$s' is in CHECK_STEPS but declares no budget in $CONF — add '$s <seconds>'. This is the check that stops the commit gate growing back into the merge gate one addition at a time." >&2
         problems=$((problems + 1)) ;;
    esac
  done

  # No declaration may name a step that is not in CHECK_STEPS: a stale entry
  # inflates the sum and hides real headroom.
  for d in $declared_names; do
    case " $STEPS " in
      *" $d "*) ;;
      *) echo "check-budget: $CONF declares '$d', which is not in CHECK_STEPS — a stale declaration inflates the sum and hides headroom" >&2
         problems=$((problems + 1)) ;;
    esac
  done

  if [ "$sum" -gt "$CEILING" ]; then
    echo "check-budget: declared budgets sum to ${sum}s, over the ${CEILING}s ceiling. Make something faster or move it to the merge gate; do NOT raise the ceiling to fit." >&2
    problems=$((problems + 1))
  fi

  if [ "$problems" -gt 0 ]; then
    echo "check-budget: $problems problem(s) — FAILING. See the rule above CHECK_STEPS in the Makefile for which gate a check belongs in." >&2
    exit 1
  fi
  echo "check-budget: OK — $(printf '%s\n' $STEPS | grep -c .) step(s), ${sum}s declared against a ${CEILING}s ceiling"
  exit 0
fi

# ------------------------------------------------------------------- elapsed
if [ "$MODE" = elapsed ]; then
  case "$ELAPSED" in
    ''|*[!0-9]*) echo "check-budget: --elapsed needs whole seconds, got '$ELAPSED'" >&2; exit 2 ;;
  esac

  if [ "$ELAPSED" -le "$BUDGET" ]; then
    echo "check: ${ELAPSED}s against a ${BUDGET}s budget — OK"
    exit 0
  fi

  over=$((ELAPSED - BUDGET))
  # Name the dominant step, so the reader learns what to fix rather than what to
  # raise. forge's condition, and the difference between a useful warning and noise.
  dominant=""
  if [ -n "$TIMINGS" ] && [ -f "$TIMINGS/baseline.observed" ]; then
    dominant=$(grep '^step' "$TIMINGS/baseline.observed" 2>/dev/null \
                 | awk -F'\t' '{print $3"\t"$2}' | sort -rn | head -1 \
                 | awk -F'\t' '{printf "%s %.1fs", $2, $1}')
  fi

  echo "" >&2
  echo "!!! check: ${ELAPSED}s against a ${BUDGET}s budget — OVER by ${over}s" >&2
  [ -n "$dominant" ] && echo "!!!   dominant step: $dominant" >&2
  echo "!!!   the known gap is internal/supervisor's test time, tracked by QUM-1307." >&2
  echo "!!!   If your commit touches internal/supervisor or its dependents, this" >&2
  echo "!!!   overage is expected and is NOT a defect in your change." >&2
  echo "!!!   NOT BLOCKING — your commit proceeds. 'sprawl merge' still runs the" >&2
  echo "!!!   full 'make validate', so nothing reaches main on the strength of this." >&2
  echo "" >&2
  exit 0
fi

echo "check-budget: need --structural or --elapsed" >&2
exit 2
