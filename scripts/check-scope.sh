#!/usr/bin/env bash
#
# check-scope.sh (QUM-1289)
#
# Resolves the set of Go packages `make check` must test for a given change, and
# prints them one per line on stdout. Diagnostics go to stderr.
#
# WHY THIS IS DEPENDENCY-AWARE, AND WHY THE OBVIOUS DEPENDENCY-AWARE VERSION IS
# STILL WRONG. Two plausible implementations both report green over a real
# defect:
#
#   (i)  "packages containing edited files". Edit a shared helper and its
#        dependents are never built.
#   (ii) reverse closure over `go list -f {{.Deps}}`. .Deps is the NON-TEST
#        transitive closure, so a package importing a helper only from its
#        _test.go is absent from it. Verified in this tree:
#        internal/rootinit's .Deps does NOT contain internal/testutil, but its
#        .TestImports does.
#
# So the graph here closes over Imports + TestImports + XTestImports, and
# scripts/test-check-scope-unit.sh keeps a live positive control that watches
# BOTH wrong implementations miss a pinned test-only dependent.
#
# DIRECTION OF SAFETY: under-scoping is the false-green direction, so anything
# unrecognised WIDENS to the whole module rather than narrowing. An empty scope
# from Go-ish input is an error, never a pass. A change with no Go bearing at all
# exits 77 (skip), never 0.
#
# Usage:
#   check-scope.sh                     # scope from the git index + worktree
#   check-scope.sh --paths "a.go b.go"  # scope from an explicit path list (tests)
#   check-scope.sh --report            # also print the skip report to stderr
#
# Exit: 0 scope printed · 77 no Go bearing (skip) · 1 error / empty-from-Go-input

set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel) || exit 1
cd "$REPO_ROOT" || exit 1

EXPLICIT_PATHS=""
REPORT=0
while [ $# -gt 0 ]; do
  case "$1" in
    --paths) EXPLICIT_PATHS=${2:-}; shift 2 ;;
    --report) REPORT=1; shift ;;
    *) echo "check-scope: unknown argument '$1'" >&2; exit 1 ;;
  esac
done

# Build tags that hide files from a default `go list`. Every tag in the tree must
# appear here or its files are a blind spot: a tag-gated importer of an edited
# helper would be invisible. Keep in sync with .golangci.yml's run.build-tags and
# any new tag added anywhere.
TAG_SETS=("" "hub_e2e store_pg" "integration sprawl_test" "doclint")

# --- collect the changed paths -------------------------------------------
if [ -n "$EXPLICIT_PATHS" ]; then
  CHANGED=$(printf '%s\n' $EXPLICIT_PATHS)
else
  # The hook tests the WORKTREE, so union the index, the unstaged diff and new
  # untracked files. -M so both sides of a rename are seen.
  #
  # NOTE: scripts/pre-commit unsets GIT_INDEX_FILE (QUM-836) before invoking the
  # gate, and under a PARTIAL commit git points that at a temporary index. So a
  # `git diff --cached` run after that unset reads the WRONG index. pre-commit
  # therefore computes the paths BEFORE the unset and exports them as
  # SPRAWL_CHECK_SCOPE_PATHS; prefer that when present.
  if [ -n "${SPRAWL_CHECK_SCOPE_PATHS:-}" ]; then
    CHANGED=$(printf '%s\n' "$SPRAWL_CHECK_SCOPE_PATHS" | tr ' ' '\n')
  else
    CHANGED=$( { git diff --cached --name-only -M
                 git diff --name-only -M
                 git ls-files --others --exclude-standard; } | sort -u )
  fi
fi
CHANGED=$(printf '%s\n' "$CHANGED" | grep -v '^$' | sort -u)

if [ -z "$CHANGED" ]; then
  echo "check-scope: no changed paths — nothing to scope" >&2
  exit 77
fi

# --- the module's package graph, unioned over all tag sets --------------
GRAPH=$(mktemp) || exit 1
ALLPKGS=$(mktemp) || exit 1
trap 'rm -f "$GRAPH" "$ALLPKGS"' EXIT
FMT='{{.ImportPath}}{{range .Imports}} {{.}}{{end}}{{range .TestImports}} {{.}}{{end}}{{range .XTestImports}} {{.}}{{end}}'
for tags in "${TAG_SETS[@]}"; do
  if [ -z "$tags" ]; then
    go list -e -f "$FMT" ./... 2>/dev/null
  else
    go list -e -tags "$tags" -f "$FMT" ./... 2>/dev/null
  fi
done | sort -u > "$GRAPH"
cut -d' ' -f1 "$GRAPH" | sort -u > "$ALLPKGS"
ALL_N=$(grep -c . "$ALLPKGS")
if [ "$ALL_N" -lt 20 ]; then
  echo "check-scope: 'go list ./...' yielded only $ALL_N packages — the graph is broken, refusing to emit a scope that would silently be almost empty" >&2
  exit 1
fi

widen_all() { cat "$ALLPKGS"; }

# resolve_pkg maps a file path to its package, but ONLY if that package really
# exists in this module. `go list -e` deliberately tolerates errors and still
# prints an ImportPath for a directory that does not exist, so accepting its
# output blindly produced a bogus seed that survived to the end of the BFS and
# then filtered out — yielding an EMPTY scope where the correct answer is
# "unknown, widen". Checking against ALLPKGS is what makes the failure widen
# instead of narrow.
resolve_pkg() {
  local d ip
  d=$(dirname "$1")
  ip=$(go list -e -f '{{.ImportPath}}' "./$d" 2>/dev/null)
  [ -n "$ip" ] || return 0
  grep -qxF "$ip" "$ALLPKGS" || return 0
  printf '%s' "$ip"
}

# --- map changed paths to seed packages ---------------------------------
SEEDS=""
NONGO=""
WIDEN=0
for p in $CHANGED; do
  case "$p" in
    go.mod|go.sum|go.work|go.work.sum)
      echo "check-scope: $p changed — widening to the whole module" >&2
      WIDEN=1 ;;
    proto/*|buf*.yaml|buf*.yml)
      echo "check-scope: $p changed (wire contract) — widening to the whole module" >&2
      WIDEN=1 ;;
    *.go|*.s|*.c|*.h)
      ip=$(resolve_pkg "$p")
      if [ -n "$ip" ]; then SEEDS="$SEEDS $ip"; else
        echo "check-scope: could not resolve a real package for '$p' — widening" >&2
        WIDEN=1
      fi ;;
    scripts/guard-*|scripts/pre-commit)
      # internal/githooks execs these scripts, so its tests are the dependents.
      SEEDS="$SEEDS github.com/dmotles/sprawl/internal/githooks"
      NONGO="$NONGO $p" ;;
    Makefile|scripts/*|.claude/*|docs/*|*.md|.sprawl/config.yaml|.gitignore|.golangci.yml)
      NONGO="$NONGO $p" ;;
    *)
      # Data a package may go:embed or read from testdata, or something this
      # table has never seen. Try the directory; widen if that fails.
      ip=$(resolve_pkg "$p")
      if [ -n "$ip" ]; then SEEDS="$SEEDS $ip"
      else
        echo "check-scope: unrecognised path '$p' — widening to the whole module (under-scoping is the unsafe direction)" >&2
        WIDEN=1
      fi ;;
  esac
done

if [ "$WIDEN" -eq 1 ]; then
  [ "$REPORT" -eq 1 ] && {
    echo "check scope: WIDENED to all $ALL_N packages" >&2
    echo "check scope: SKIPPED 0 packages" >&2
    echo "check: this is the COMMIT gate, not the merge gate. 'sprawl merge' runs 'make validate'." >&2
  }
  widen_all
  exit 0
fi

SEEDS=$(printf '%s\n' $SEEDS | grep -v '^$' | sort -u)

if [ -z "$SEEDS" ]; then
  if [ -n "$NONGO" ]; then
    echo "check-scope: no Go packages are affected by this change." >&2
    echo "check-scope: non-Go paths: $(printf '%s' "$NONGO" | tr -s ' ')" >&2
    echo "check-scope: these are gated at MERGE by make validate, not here." >&2
    exit 77
  fi
  echo "check-scope: Go-ish inputs mapped to zero packages — refusing to report an empty scope as success" >&2
  exit 1
fi

# --- reverse BFS to a fixed point --------------------------------------
FRONTIER=$SEEDS
CLOSURE=$SEEDS
while [ -n "$FRONTIER" ]; do
  NEXT=$(awk -v frontier="$FRONTIER" '
    BEGIN { n = split(frontier, f, "\n"); for (i = 1; i <= n; i++) if (f[i] != "") want[f[i]] = 1 }
    {
      for (i = 2; i <= NF; i++) if ($i in want) { print $1; break }
    }' "$GRAPH" | sort -u)
  NEW=$(comm -23 <(printf '%s\n' "$NEXT" | sort -u | grep -v '^$') \
                 <(printf '%s\n' "$CLOSURE" | sort -u | grep -v '^$'))
  [ -z "$NEW" ] && break
  CLOSURE=$(printf '%s\n%s\n' "$CLOSURE" "$NEW" | sort -u | grep -v '^$')
  FRONTIER=$NEW
done

CLOSURE=$(printf '%s\n' "$CLOSURE" | sort -u | grep -v '^$')
# Keep only real module packages (a seed may name a stdlib or external import).
CLOSURE=$(comm -12 <(printf '%s\n' "$CLOSURE") "$ALLPKGS")
SCOPE_N=$(printf '%s\n' "$CLOSURE" | grep -c .)

if [ "$SCOPE_N" -eq 0 ]; then
  echo "check-scope: closure came out empty despite Go seeds — refusing to report that as success" >&2
  exit 1
fi

# Effectively-everything: same cost, simpler argv, and say so.
THRESHOLD=$(( ALL_N * 80 / 100 ))
if [ "$SCOPE_N" -ge "$THRESHOLD" ]; then
  [ "$REPORT" -eq 1 ] && {
    echo "check scope: $SCOPE_N of $ALL_N packages — effectively the whole module, running ./..." >&2
    echo "check scope: SKIPPED 0 packages" >&2
    echo "check: this is the COMMIT gate, not the merge gate. 'sprawl merge' runs 'make validate'." >&2
  }
  widen_all
  exit 0
fi

if [ "$REPORT" -eq 1 ]; then
  echo "check scope: seeds ($(printf '%s\n' "$SEEDS" | grep -c .)):" >&2
  printf '%s\n' "$SEEDS" | sed 's/^/  /' >&2
  echo "check scope: closure to test ($SCOPE_N of $ALL_N):" >&2
  printf '%s\n' "$CLOSURE" | sed 's/^/  /' >&2
  # The full list, never a count — a count is not auditable.
  SKIPPED=$(comm -23 "$ALLPKGS" <(printf '%s\n' "$CLOSURE"))
  echo "check scope: SKIPPED $(printf '%s\n' "$SKIPPED" | grep -c .) packages — NOT tested by this commit gate:" >&2
  printf '%s\n' "$SKIPPED" | grep -v '^$' | sed 's/^/  /' >&2
  [ -n "$NONGO" ] && {
    echo "check scope: non-Go changes with no Go scope: $(printf '%s' "$NONGO" | tr -s ' ')" >&2
    echo "check scope:   -> gated at merge by make validate" >&2
  }
  echo "check: this is the COMMIT gate, not the merge gate. 'sprawl merge' runs 'make validate'." >&2
fi

printf '%s\n' "$CLOSURE"
exit 0
