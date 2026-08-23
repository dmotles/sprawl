#!/usr/bin/env bash
#
# test-check-scope-unit.sh (QUM-1289)
#
# Guards scripts/check-scope.sh, the dependency-aware change-scoper behind
# `make check`'s test step. This is the sharpest edge in QUM-1289: a gate that
# silently narrows its own scope is worse than a slow one, because it reports
# green over a package it never built.
#
# TWO WRONG IMPLEMENTATIONS ARE PLAUSIBLE, AND BOTH GO GREEN OVER A REAL DEFECT:
#
#   (i)  "packages containing edited files" — no dependency awareness at all.
#   (ii) reverse closure over `go list -f {{.Deps}}` — dependency-aware, and
#        STILL wrong, because .Deps is the NON-TEST transitive closure. A package
#        that imports a helper only from its _test.go does not appear in it.
#
# The live fixture below is pinned to a real pair that discriminates BOTH:
# internal/testutil/eventually.go is imported by internal/rootinit and
# internal/supervisor from _test.go ONLY (verified: `go list -f {{.Deps}}
# ./internal/rootinit` does not contain testutil, while {{.TestImports}} does).
# So under (i) and (ii) the scope is just {internal/testutil}, a planted failure
# in internal/rootinit never runs, and the gate passes.
#
# The pair is HARDCODED, and [P0] FAILS — never skips — if either package stops
# importing the other from a _test.go. A control whose subject varies with
# unrelated tree state is not a control (QUM-1286's lesson).
#
# Pure shell + go. No claude, no tmux.

set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
SCOPER=$REPO_ROOT/scripts/check-scope.sh

# The pinned live fixture.
HELPER_PKG=github.com/dmotles/sprawl/internal/testutil
HELPER_FILE=internal/testutil/eventually.go
DEPENDENT_PKG=github.com/dmotles/sprawl/internal/rootinit
DEPENDENT_DIR=internal/rootinit
UNRELATED_DIR=internal/worktree
UNRELATED_PKG=github.com/dmotles/sprawl/internal/worktree

# Hardcoded literal. A full run makes exactly this many assertions.
MIN_ASSERTIONS=15

TMPBASE=${TMPDIR:-/tmp}
SCRATCH=$(mktemp -d "$TMPBASE/sprawl-check-scope.XXXXXX") || {
  echo "FATAL: mktemp -d failed under $TMPBASE" >&2; exit 1; }
case "$SCRATCH" in
  /*) ;;
  *) echo "FATAL: mktemp returned a non-absolute path: '$SCRATCH'" >&2; exit 1 ;;
esac
cleanup() {
  case "$SCRATCH" in
    "$TMPBASE"/sprawl-check-scope.*) rm -rf "$SCRATCH" ;;
    *) echo "WARN: refusing to remove unexpected SCRATCH '$SCRATCH'" >&2 ;;
  esac
}
trap cleanup EXIT

PASSES=0
FAILURES=0
pass() { PASSES=$((PASSES + 1)); echo "  PASS: $1"; }
fail() { FAILURES=$((FAILURES + 1)); echo "  FAIL: $1" >&2; }

echo "=== check-scope gate (QUM-1289) ==="

# --- [P0] the fixture's premise, asserted not assumed --------------------
premise_ok=1
deps=$(cd "$REPO_ROOT" && go list -f '{{range .Deps}}{{println .}}{{end}}' "./$DEPENDENT_DIR" 2>/dev/null)
timports=$(cd "$REPO_ROOT" && go list -f '{{range .TestImports}}{{println .}}{{end}}' "./$DEPENDENT_DIR" 2>/dev/null)
if printf '%s\n' "$timports" | grep -qx "$HELPER_PKG"; then
  pass "[P0] premise: $DEPENDENT_PKG imports $HELPER_PKG from a _test.go"
else
  fail "[P0] premise BROKEN: $DEPENDENT_PKG no longer test-imports $HELPER_PKG — this fixture no longer discriminates the wrong implementations; re-pin it to a live test-only import pair"
  premise_ok=0
fi
if printf '%s\n' "$deps" | grep -qx "$HELPER_PKG"; then
  fail "[P0] premise BROKEN: $HELPER_PKG is now in $DEPENDENT_PKG's .Deps (a non-test import appeared), so implementation (ii) would no longer be wrong here and this fixture proves less than it claims"
  premise_ok=0
else
  pass "[P0] premise: $HELPER_PKG is absent from $DEPENDENT_PKG's .Deps, so a .Deps-based scoper misses it"
fi

# --- helpers -------------------------------------------------------------
# naive_scope_i: implementation (i), the dirs-of-edited-files scoper.
naive_scope_i() {
  local paths="$1" d out=""
  for p in $paths; do
    d=$(dirname "$p")
    out="$out $(cd "$REPO_ROOT" && go list -e -f '{{.ImportPath}}' "./$d" 2>/dev/null)"
  done
  printf '%s\n' $out | sort -u | grep -v '^$'
}
# naive_scope_ii: implementation (ii), reverse closure over .Deps only.
naive_scope_ii() {
  local seeds; seeds=$(naive_scope_i "$1")
  local all; all=$(cd "$REPO_ROOT" && go list -e -f '{{.ImportPath}}{{range .Deps}} {{.}}{{end}}' ./... 2>/dev/null)
  { printf '%s\n' "$seeds"
    printf '%s\n' "$all" | while read -r line; do
      pkg=${line%% *}
      for s in $seeds; do
        case " ${line#* } " in *" $s "*) echo "$pkg" ;; esac
      done
    done
  } | sort -u | grep -v '^$'
}

# --- [1] positive control: BOTH wrong implementations miss the dependent --
if [ "$premise_ok" -eq 1 ]; then
  scope_i=$(naive_scope_i "$HELPER_FILE")
  if printf '%s\n' "$scope_i" | grep -qx "$DEPENDENT_PKG"; then
    fail "[1] positive control did not reproduce the defect: implementation (i) unexpectedly included $DEPENDENT_PKG"
  else
    pass "[1] positive control: implementation (i) MISSES $DEPENDENT_PKG when only $HELPER_FILE changed — this is the defect the scoper must not have"
  fi
  scope_ii=$(naive_scope_ii "$HELPER_FILE")
  if printf '%s\n' "$scope_ii" | grep -qx "$DEPENDENT_PKG"; then
    fail "[1] positive control did not reproduce the defect: implementation (ii) unexpectedly included $DEPENDENT_PKG"
  else
    pass "[1] positive control: implementation (ii), reverse closure over .Deps, ALSO misses $DEPENDENT_PKG"
  fi
fi

# --- [2] the real scoper must include the test-only dependent ------------
if [ ! -x "$SCOPER" ]; then
  fail "[2] $SCOPER does not exist or is not executable"
else
  real_scope=$("$SCOPER" --paths "$HELPER_FILE" 2>/dev/null)
  rc=$?
  if [ "$rc" -ne 0 ]; then
    fail "[2] check-scope.sh exited $rc on a single-file Go change"
  else
    pass "[2a] check-scope.sh exited 0 on a Go change"
  fi
  for want in "$HELPER_PKG" "$DEPENDENT_PKG" "github.com/dmotles/sprawl/internal/supervisor"; do
    if printf '%s\n' "$real_scope" | grep -qx "$want"; then
      pass "[2] scope includes $want"
    else
      fail "[2] scope MISSES $want — a change to $HELPER_FILE would leave it untested while the gate reported green"
    fi
  done

  # --- [3] negative control: an unrelated change must NOT widen ----------
  unrelated_scope=$("$SCOPER" --paths "$UNRELATED_DIR/worktree.go" 2>/dev/null)
  if printf '%s\n' "$unrelated_scope" | grep -qx "$UNRELATED_PKG"; then
    pass "[3] negative control: an unrelated change still includes its own package"
  else
    fail "[3] negative control: scope for $UNRELATED_DIR does not even include $UNRELATED_PKG"
  fi
  if printf '%s\n' "$unrelated_scope" | grep -qx "$DEPENDENT_PKG"; then
    fail "[3] negative control FAILED: a change to $UNRELATED_DIR pulled in $DEPENDENT_PKG — the scoper over-widens, so [2] passing means nothing"
  else
    pass "[3] negative control: a change to $UNRELATED_DIR does NOT pull in $DEPENDENT_PKG"
  fi

  # --- [4] non-Go-only input must SKIP (77), never silently pass --------
  "$SCOPER" --paths "README.md" >/dev/null 2>&1
  rc=$?
  if [ "$rc" -eq 77 ]; then
    pass "[4] a non-Go-only change exits 77 (skip), not 0"
  else
    fail "[4] a non-Go-only change exited $rc; must be 77 so a skip can never read as a pass"
  fi

  # --- [5] go.mod must widen to everything -----------------------------
  modscope=$("$SCOPER" --paths "go.mod" 2>/dev/null)
  mod_n=$(printf '%s\n' "$modscope" | grep -c .)
  all_n=$(cd "$REPO_ROOT" && go list ./... 2>/dev/null | grep -c .)
  if [ "$mod_n" -ge "$all_n" ] && [ "$all_n" -gt 20 ]; then
    pass "[5] a go.mod change widens the scope to the whole module ($mod_n of $all_n)"
  else
    fail "[5] a go.mod change scoped to $mod_n packages of $all_n — a dependency change must widen to everything"
  fi

  # --- [6] an unrecognised path must widen, not narrow -----------------
  unk=$("$SCOPER" --paths "some/unknown/thing.xyz" 2>/dev/null)
  unk_n=$(printf '%s\n' "$unk" | grep -c .)
  if [ "$unk_n" -ge "$all_n" ]; then
    pass "[6] an unrecognised path widens to everything ($unk_n) rather than narrowing"
  else
    fail "[6] an unrecognised path scoped to $unk_n of $all_n — unknown inputs must widen, since under-scoping is the false-green direction"
  fi

  # --- [7] it must PRINT what it skipped -------------------------------
  skiprep=$("$SCOPER" --paths "$UNRELATED_DIR/worktree.go" --report 2>&1)
  if printf '%s\n' "$skiprep" | grep -q 'SKIPPED'; then
    pass "[7] --report names the packages it skipped"
  else
    fail "[7] --report does not print a SKIPPED list — a gate that narrows its scope silently is worse than a slow one"
  fi
  if printf '%s\n' "$skiprep" | grep -qi 'commit gate'; then
    pass "[7] --report states this is the commit gate, not the merge gate"
  else
    fail "[7] --report does not say it is only the commit gate, so a green check could be cited as 'validated'"
  fi
fi

echo "=== Results: $PASSES passed, $FAILURES failed ==="
observed=$((PASSES + FAILURES))
if [ "$observed" -lt "$MIN_ASSERTIONS" ]; then
  echo "  FAIL: only $observed assertion(s) ran but MIN_ASSERTIONS=$MIN_ASSERTIONS — this gate measured less than it claims (QUM-1029)" >&2
  exit 1
fi
[ "$FAILURES" -eq 0 ] || exit 1
exit 0
