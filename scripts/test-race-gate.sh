#!/usr/bin/env bash
#
# test-race-gate.sh (QUM-972)
#
# Guards the race-detection gate itself. `make validate` is the single gate every
# agent and every commit is told to trust, and until QUM-972 it ran `go test`
# WITHOUT `-race` — so nine live data races (three in internal/backend, six in
# internal/rootinit, one of the latter a PRODUCTION defect) sat behind a
# permanently green validate. That regression is SILENT in both directions:
#
#   * drop `-race` from validate and nothing fails; races simply stop being
#     detected, and the next symptom is a production concurrency bug.
#   * keep `-race` in validate but land in an environment where it is inert
#     (CGO_ENABLED=0, no C toolchain, a hostile GOFLAGS) and, again, nothing
#     fails.
#
# No amount of reading the Makefile detects the second class, so this script
# checks both:
#
#   [1] WIRING   — read from `make -n validate`, not from grepping Makefile text,
#                  so it survives refactors a text grep would miss. EVERY
#                  `go test` line in validate's expansion must carry `-race`.
#   [2] BEHAVIOUR — the flags validate actually uses are run against a planted
#                  race and against a clean control, in a scratch module. This
#                  re-demonstrates falsifiability ON EVERY RUN (QUM-953) rather
#                  than relying on a transcript nobody re-reads.
#
# Pure shell + go, no claude/tmux. Runs in `make validate`.
#
# Seam for demonstrating [1] can fail: set RACE_GATE_MAKEFILE to the path of an
# alternative makefile (e.g. a copy with `-race` removed) and this script reads
# the wiring from that file instead. Nothing else consults it.

# NOT `set -e`: every assertion must be reported, not just the first. Following
# scripts/test-wirelog-helpers-unit.sh.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
MAKEFILE=${RACE_GATE_MAKEFILE:-$REPO_ROOT/Makefile}
TMPBASE=${TMPDIR:-/tmp}
SCRATCH=$(mktemp -d "$TMPBASE/sprawl-race-gate.XXXXXX") || {
  echo "FATAL: mktemp -d failed under $TMPBASE" >&2
  exit 1
}
# An empty SCRATCH would make FIXTURE "/fixture" and cd the go-test legs to /.
case "$SCRATCH" in
  /*) ;;
  *) echo "FATAL: mktemp returned a non-absolute path: '$SCRATCH'" >&2; exit 1 ;;
esac

# Bump when assertions are added or removed. A hardcoded literal, NOT derived
# from anything in this script: a floor computed from the corpus it measures is
# satisfied by an empty corpus, which is the exact false-green it exists to stop.
MIN_ASSERTIONS=15

PASSES=0
FAILURES=0

cleanup() {
  # /tmp hygiene: assert the path prefix before ANY delete. Never rm a glob.
  case "$SCRATCH" in
    "$TMPBASE"/sprawl-race-gate.*) rm -rf "$SCRATCH" ;;
    *) echo "REFUSING to delete unexpected path: $SCRATCH" >&2 ;;
  esac
}
trap cleanup EXIT

ok()   { PASSES=$((PASSES+1));   echo "  PASS: $1"; }
fail() { FAILURES=$((FAILURES+1)); echo "  FAIL: $1" >&2; }

echo "=== [0] preconditions"
# An exported RACE_GATE_MAKEFILE would silently redirect ALL of group [1] to
# another file. It exists only for the falsifiability demo, so an active seam is
# announced loudly AND forced nonzero: a seam that can change the verdict in
# silence is the same false-green class this script guards.
if [ -n "${RACE_GATE_MAKEFILE:-}" ]; then
  echo "!!! RACE_GATE_MAKEFILE seam ACTIVE: group [1] reads $RACE_GATE_MAKEFILE, not the repo Makefile." >&2
  fail "RACE_GATE_MAKEFILE seam is set — this run cannot certify the repo Makefile (demo mode)"
fi
if [ -f "$MAKEFILE" ]; then
  ok "makefile under test exists: ${MAKEFILE#"$REPO_ROOT"/}"
else
  fail "makefile under test does not exist: $MAKEFILE"
  echo "=== race-gate results: $PASSES passed / $FAILURES failed ===" >&2
  exit 1
fi

echo "=== [1] wiring — read from make -n, not from Makefile text"

VALIDATE_DRY=$(make -C "$REPO_ROOT" -f "$MAKEFILE" -n validate 2>&1)
VALIDATE_RC=$?
if [ "$VALIDATE_RC" -eq 0 ]; then
  ok "make -n validate expands cleanly"
else
  fail "make -n validate failed (rc=$VALIDATE_RC): $(printf '%s' "$VALIDATE_DRY" | tail -3 | tr '\n' ' ')"
fi

# Any line that INVOKES go test, not just lines that start with it. Anchoring at
# ^ was a proven false-green: `CGO_ENABLED=0 go test ./internal/...` alongside the
# -race line left the "every line carries -race" assertion reporting 14/0 PASS.
# `env`, `time`, and `cd x && go test` prefixes have the same shape, and
# CGO_ENABLED=0 is precisely the degradation this file's header names.
#
# KNOWN LIMIT: a `go test` invoked from inside a shell script that validate runs
# is invisible to `make -n`. No current validate step does that (checked
# leak-scan, test-gitignore-classes, test-wirelog-helpers-unit,
# test-e2e-matrix-unit), but a future one would need auditing by hand.
GO_TEST_LINES=$(printf '%s\n' "$VALIDATE_DRY" | grep -E '(^|[[:space:]]|;|&|\|)go test([[:space:]]|$)')

if [ -n "$GO_TEST_LINES" ]; then
  ok "make -n validate runs at least one 'go test' ($(printf '%s\n' "$GO_TEST_LINES" | wc -l | tr -d ' ') line(s))"
else
  fail "make -n validate runs NO 'go test' at all — validate has stopped testing the tree"
fi

# EVERY go test line, not merely one of them. "At least one carries -race" would
# let someone re-add a bare `test` prerequisite alongside `test-race`, so half
# the run is uninstrumented while the summary reads as full coverage.
BARE=$(printf '%s\n' "$GO_TEST_LINES" | grep -vE '(^|[[:space:]])-race([[:space:]]|$)')
if [ -n "$GO_TEST_LINES" ] && [ -z "$BARE" ]; then
  ok "every 'go test' line in make -n validate carries -race"
else
  fail "make -n validate has 'go test' line(s) WITHOUT -race: $(printf '%s' "$BARE" | tr '\n' '|')"
fi

# Pin the scope. Narrowing to a hand-picked package subset must be a deliberate,
# test-updating act, not a quiet edit — a subset silently stops covering any
# newly-added concurrent package.
RACE_LINES=$(printf '%s\n' "$GO_TEST_LINES" | grep -E '(^|[[:space:]])-race([[:space:]]|$)')
# EVERY race line, not just the first: a second `go test -race ./internal/foo/`
# added to validate would otherwise narrow the scope invisibly, which is exactly
# what this leg exists to prevent.
NARROW=$(printf '%s\n' "$RACE_LINES" | grep -vE '(^|[[:space:]])\./\.\.\.([[:space:]]|$)')
if [ -n "$RACE_LINES" ] && [ -z "$NARROW" ]; then
  ok "every -race run in validate covers ./... (whole tree)"
else
  fail "validate has a -race run that does not cover ./... — scope narrowed to: ${NARROW:-<no -race line at all>}"
fi
# Flag extraction below uses the first race line; with the assertion above, all
# of them carry ./..., so the first is representative.
RACE_LINE=$(printf '%s\n' "$RACE_LINES" | head -1)

# The standalone target must work too: it is what an agent runs ad hoc, and what
# a future CI step would call.
for tgt in test-race test-race-gate; do
  if make -C "$REPO_ROOT" -f "$MAKEFILE" -n "$tgt" >/dev/null 2>&1; then
    ok "make -n $tgt resolves"
  else
    fail "make -n $tgt failed — target missing"
  fi
done

# A resolvable target nobody calls is not a gate. Without this, test-race-gate
# could be silently unwired from validate and this whole script would stop
# running while still passing when invoked by hand.
if printf '%s\n' "$VALIDATE_DRY" | grep -q 'test-race-gate.sh'; then
  ok "make validate actually invokes scripts/test-race-gate.sh"
else
  fail "make validate does not invoke scripts/test-race-gate.sh — this gate is unwired"
fi

TEST_RACE_DRY=$(make -C "$REPO_ROOT" -f "$MAKEFILE" -n test-race 2>/dev/null)
# Same invocation-position match as GO_TEST_LINES above, not a ^ anchor — kept
# consistent so the next reader is not left wondering why the two differ.
if printf '%s\n' "$TEST_RACE_DRY" | grep -qE '(^|[[:space:]]|;|&|\|)go test.*(^|[[:space:]])-race([[:space:]]|$)'; then
  ok "make test-race passes -race"
else
  fail "make test-race does not pass -race: $(printf '%s' "$TEST_RACE_DRY" | tr '\n' '|')"
fi

# .PHONY is grepped from the text on purpose: make exposes no query for it, and a
# missing entry only bites when a same-named file appears in the tree.
PHONY_LINE=$(grep -E '^\.PHONY:' "$MAKEFILE" | tr '\n' ' ')
for tgt in test-race test-race-gate; do
  if printf '%s' "$PHONY_LINE" | grep -qE "(^|[[:space:]])$tgt([[:space:]]|$)"; then
    ok ".PHONY declares $tgt"
  else
    fail ".PHONY does not declare $tgt"
  fi
done

echo "=== [2] behaviour — validate's own flags, against a planted race"

# Extract the flag words validate actually uses: everything between `go test`
# and the first package pattern.
#
# The behavioural legs below therefore run validate's own FLAGS plus THIS SHELL'S
# AMBIENT ENV. That is what catches a missing C toolchain or an exported
# CGO_ENABLED=0 / hostile GOFLAGS, which no amount of string-matching can. Be
# precise about the limit: a per-recipe-line env prefix (`CGO_ENABLED=0 go test
# -race ./...`) is NOT reproduced here, because the extractor deliberately skips
# everything before `go test`. Group [1] would not flag that line either, since it
# does carry -race. In practice it dies loudly at `go test` ("-race requires
# cgo"), so it is a loud failure rather than a false green — but it is not this
# leg that catches it.
# Stop at the first token that is neither a flag nor the value of a
# value-taking flag. Breaking only on `./*` would swallow a package pattern like
# `github.com/dmotles/sprawl/...` into FLAGS and then pass it to the fixture
# go test, failing the behavioural legs for the wrong reason.
# Separated-form value flags only. The `[ "${prev#*=}" = "$prev" ]` guard below
# skips this branch when prev already carries its value inline: with `-count=1`,
# consulting this list would consume the FOLLOWING token as the value, i.e. eat
# `./...` into FLAGS. That then runs the whole fixture module in both behavioural
# legs, so the racy leg passes for the wrong reason and the clean control fails —
# reported as a Makefile defect when it is an extractor defect.
VALUE_FLAGS=" -run -count -timeout -tags -parallel -p -cpu -gcflags -ldflags "
FLAGS=()
if [ -n "$RACE_LINE" ]; then
  # shellcheck disable=SC2206  # deliberate word-splitting of a make recipe line
  TOKENS=($RACE_LINE)
  # Skip the leading `go test`, plus anything before it (an env-var prefix).
  start=0
  for ((i = 0; i < ${#TOKENS[@]} - 1; i++)); do
    if [ "${TOKENS[i]}" = "go" ] && [ "${TOKENS[i + 1]}" = "test" ]; then
      start=$((i + 2))
      break
    fi
  done
  prev=""
  for ((i = start; i < ${#TOKENS[@]}; i++)); do
    tok=${TOKENS[i]}
    if [ "${tok:0:1}" = "-" ]; then
      FLAGS+=("$tok")
    elif [ -n "$prev" ] && [ "${prev#*=}" = "$prev" ] && [[ $VALUE_FLAGS == *" $prev "* ]]; then
      FLAGS+=("$tok")
    else
      break
    fi
    prev=$tok
  done
fi
if [ ${#FLAGS[@]} -gt 0 ]; then
  ok "extracted validate's go test flags: ${FLAGS[*]}"
else
  fail "could not extract any go test flags from validate's -race line: ${RACE_LINE:-<none>}"
  # Fall back to a bare -race so the behavioural legs still run. They are
  # independently informative: "the toolchain CAN detect races, the Makefile
  # just isn't asking it to" is a different and more useful diagnosis than a
  # skipped leg, and a skip here would be the very false-green this file guards.
  FLAGS=(-race)
  echo "  note: falling back to bare '-race' for the behavioural legs below"
fi

FIXTURE="$SCRATCH/fixture"
mkdir -p "$FIXTURE/racy" "$FIXTURE/clean"
cat >"$FIXTURE/go.mod" <<'EOF'
module racegatefixture

go 1.21
EOF
cat >"$FIXTURE/racy/racy_test.go" <<'EOF'
package racy

import (
	"sync"
	"testing"
)

func TestPlantedRace(t *testing.T) {
	x := 0
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				x++
			}
		}()
	}
	wg.Wait()
	_ = x
}
EOF
cat >"$FIXTURE/clean/clean_test.go" <<'EOF'
package clean

import (
	"sync"
	"testing"
)

func TestNoRace(t *testing.T) {
	x := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				mu.Lock()
				x++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	_ = x
}
EOF

# Fixtures are stat'd: a fixture that never materialised produces the same green
# as a real pass (the QUM-989 lesson).
FIXTURES_OK=1
for f in "$FIXTURE/go.mod" "$FIXTURE/racy/racy_test.go" "$FIXTURE/clean/clean_test.go"; do
  if [ ! -s "$f" ]; then
    FIXTURES_OK=0
    fail "fixture missing or empty: ${f#"$SCRATCH"/}"
  fi
done
if [ "$FIXTURES_OK" -eq 1 ]; then
  ok "all three scratch fixtures materialised non-empty"
fi

RACY_OUT=$(cd "$FIXTURE" && go test "${FLAGS[@]}" -count=1 ./racy/ 2>&1)
RACY_RC=$?
# BOTH conditions are required. A compile error, a bad go.mod, a missing
# toolchain and a real detection all exit non-zero; only the literal
# "WARNING: DATA RACE" makes the non-zero exit attributable to the detector.
if [ "$RACY_RC" -ne 0 ] && printf '%s' "$RACY_OUT" | grep -q 'WARNING: DATA RACE'; then
  ok "these flags detect a planted race (non-zero exit + WARNING: DATA RACE)"
else
  fail "these flags did NOT detect a planted race (rc=$RACY_RC): $(printf '%s' "$RACY_OUT" | tail -5 | tr '\n' '|')"
fi

# The clean control is not optional: without it, any environment that makes the
# racy leg fail for the WRONG reason reads as a pass.
CLEAN_OUT=$(cd "$FIXTURE" && go test "${FLAGS[@]}" -count=1 ./clean/ 2>&1)
CLEAN_RC=$?
if [ "$CLEAN_RC" -eq 0 ]; then
  ok "these flags leave a race-free control green"
else
  fail "these flags failed the race-free control (rc=$CLEAN_RC) — the racy result above is not attributable: $(printf '%s' "$CLEAN_OUT" | tail -5 | tr '\n' '|')"
fi

TOTAL=$((PASSES + FAILURES))
echo
echo "=== race-gate results: $PASSES passed / $FAILURES failed ==="
if [ "$TOTAL" -lt "$MIN_ASSERTIONS" ]; then
  echo "  FAIL: only $TOTAL assertions ran, expected at least $MIN_ASSERTIONS — a case died early and this run measured less than it claims" >&2
  exit 1
fi
if [ "$FAILURES" -gt 0 ]; then
  exit 1
fi
exit 0
