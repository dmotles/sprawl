#!/usr/bin/env bash
#
# test-doclint-split-unit.sh (QUM-1289)
#
# Guards the doc-lint split. Two tests in ./cmd — TestSkillsGoSymbolBanListIsDead
# and TestSkillsDoNotNameDeadGoSymbols — are DOCUMENTATION linters: they read
# every tracked .go file and regex-scan every SKILL.md for identifiers QUM-1186
# deleted. Measured on an 8-core host, cold:
#
#     TestSkillsGoSymbolBanListIsDead    1.63s plain   32.57s under -race
#     TestSkillsDoNotNameDeadGoSymbols   0.42s plain    4.91s under -race
#     whole ./cmd package                              43.49s under -race
#
# 37.5s of ./cmd's 43.5s, and ./cmd is reverse-reachable from ~46 of 51
# packages, so that cost lands in nearly every dependency closure. That made
# them the dominant cost of QUM-1289's 60s change-scoped commit gate.
#
# So they move behind the `doclint` build tag and run at MERGE, without -race.
#
# THE FAILURE MODE THIS SCRIPT EXISTS FOR: moving a test behind a build tag that
# nothing ever runs is DELETING COVERAGE while looking like an optimisation, and
# it is invisible in both directions — the fast gate goes green because the test
# is gone, and no one notices the merge gate never picked it up. So [3] and [4]
# below assert the merge step exists AND that running it really executes both
# tests. [4] is the load-bearing one: [3] alone passes for a step that runs zero
# tests and exits 0.
#
#   [1] The DEFAULT ./cmd test binary must NOT contain either test.
#   [2] The `doclint`-tagged binary MUST contain both.
#   [3] A validate step must exist that runs them, read from `make -n`, never
#       from grepping Makefile text (the QUM-972 lesson in test-race-gate.sh).
#   [4] Running that step must actually EXECUTE both, observed by name.
#   WHAT CLOSES THE OTHER VACUITY ROUTES, since this gate does not check them:
#   the moved tests carry their own floors. goSourceFiles fatals below 100
#   tracked .go files, TestSkillsGoSymbolBanListIsDead fatals below 5 banned
#   symbols, and skillDocs fatals on zero skill files per root. So an empty ban
#   list or a broken tree walk fails loudly rather than passing vacuously, and
#   -count=1 (in the Makefile step) stops a cached PASS standing in for a run.
#
#   KNOWN LIMIT of [5]: it scans only the moved file. The moved tests also call
#   skillDocs, bannedRefRE and repoRootFromTest, which live in untagged siblings.
#   Concurrency added THERE — parallelising skillDocs' file reads is an obvious
#   future optimisation — is executed by these tests under no -race and this
#   probe would not see it. Stated rather than silently assumed.
#
#   [5] -race must be ABSENT from that step, and the moved file must contain no
#       concurrency primitives. [5] is what makes dropping -race a checkable
#       invariant rather than a claim in a comment: the justification is that
#       these tests have no goroutines, channels or sync, so the race detector
#       cannot observe anything. If someone later adds concurrency to that file,
#       this fires and says -race is owed again.
#
# Pure shell + go. No claude, no tmux. Runs in `make validate`.
#
# Seam for demonstrating [3]/[4] can fail: DOCLINT_MAKEFILE points at an
# alternative makefile. Nothing else consults it.

# NOT `set -e`: every assertion must be reported, not just the first.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
MAKEFILE=${DOCLINT_MAKEFILE:-$REPO_ROOT/Makefile}
DOCLINT_TAG=doclint
DOCLINT_STEP=test-doclint
SELF_STEP=test-doclint-split-unit
MOVED_FILE=cmd/skills_doclint_test.go
TEST_A=TestSkillsGoSymbolBanListIsDead
TEST_B=TestSkillsDoNotNameDeadGoSymbols

# Bump when assertions are added or removed. A hardcoded literal, NOT derived
# from anything this script measures: a floor computed from its own corpus is
# satisfied by an empty corpus.
MIN_ASSERTIONS=19

TMPBASE=${TMPDIR:-/tmp}
SCRATCH=$(mktemp -d "$TMPBASE/sprawl-doclint-split.XXXXXX") || {
  echo "FATAL: mktemp -d failed under $TMPBASE" >&2
  exit 1
}
case "$SCRATCH" in
  /*) ;;
  *) echo "FATAL: mktemp returned a non-absolute path: '$SCRATCH'" >&2; exit 1 ;;
esac
cleanup() {
  # /tmp hygiene: assert the prefix before ANY delete. Never rm a glob.
  case "$SCRATCH" in
    "$TMPBASE"/sprawl-doclint-split.*) rm -rf "$SCRATCH" ;;
    *) echo "WARN: refusing to remove unexpected SCRATCH '$SCRATCH'" >&2 ;;
  esac
}
trap cleanup EXIT

PASSES=0
FAILURES=0

pass() { PASSES=$((PASSES + 1)); echo "  PASS: $1"; }
fail() { FAILURES=$((FAILURES + 1)); echo "  FAIL: $1" >&2; }

echo "=== doc-lint split gate (QUM-1289) ==="
echo "  makefile: $MAKEFILE"

# [0] preconditions. A seam that changes the verdict in SILENCE is the false-green
# class this gate exists to guard, so say so loudly — following
# scripts/test-race-gate.sh's precondition block.
if [ -n "${DOCLINT_MAKEFILE:-}" ]; then
  echo "  NOTE: DOCLINT_MAKEFILE is set — reading wiring from '$MAKEFILE', NOT the repo Makefile." >&2
  echo "        This is the demonstration seam. A verdict from this run says nothing about the repo." >&2
fi
if [ ! -f "$MAKEFILE" ]; then
  echo "FATAL: makefile '$MAKEFILE' does not exist" >&2
  exit 1
fi

# --- [1] the default binary must not carry the doc-lint tests ---------------
# NOTE: a build failure is reported on STDOUT ("FAIL ... [build failed]"), not
# stderr, so a non-empty result is NOT evidence the probe worked. Without the
# rc and build-failed checks below, both [1] absence assertions pass BECAUSE the
# package does not compile.
default_list=$(cd "$REPO_ROOT" && go test -list '.*' ./cmd/ 2>/dev/null)
default_rc=$?
default_count=$(printf '%s\n' "$default_list" | grep -cE '^Test|^Fuzz|^Benchmark')
if [ "$default_rc" -ne 0 ] || printf '%s\n' "$default_list" | grep -q 'build failed' \
   || [ "$default_count" -lt 20 ]; then
  fail "[1] pre-condition: 'go test -list' on ./cmd did not yield a usable list (rc=$default_rc, ${default_count} test names) — cannot tell a real absence from a broken or non-compiling probe"
else
  pass "[1a] control: 'go test -list' on ./cmd listed $default_count tests and exited 0"
  for t in "$TEST_A" "$TEST_B"; do
    if printf '%s\n' "$default_list" | grep -qx "$t"; then
      fail "[1] $t is still in the DEFAULT ./cmd binary — it must be behind the '$DOCLINT_TAG' tag (it costs the commit gate ~37s under -race)"
    else
      pass "[1] $t absent from the default ./cmd binary"
    fi
  done
  # Negative control: a test that must STAY in the default binary. Without this,
  # [1] is satisfied by a ./cmd whose tests all vanished.
  if printf '%s\n' "$default_list" | grep -qx TestSkillsCitedPathsExist; then
    pass "[1b] negative control: TestSkillsCitedPathsExist still IS in the default binary"
  else
    fail "[1b] negative control failed: TestSkillsCitedPathsExist is missing from the default ./cmd binary — the split removed more than the two doc-lint tests"
  fi
fi

# --- [2] the tagged binary must carry them ---------------------------------
tagged_list=$(cd "$REPO_ROOT" && go test -tags "$DOCLINT_TAG" -list '.*' ./cmd/ 2>/dev/null)
for t in "$TEST_A" "$TEST_B"; do
  if printf '%s\n' "$tagged_list" | grep -qx "$t"; then
    pass "[2] $t present in the -tags $DOCLINT_TAG binary"
  else
    fail "[2] $t is absent from the -tags $DOCLINT_TAG binary too. Read with [1]: if [1] passed, this test exists in no binary at all and the coverage was DELETED, not moved"
  fi
done

# --- [3] a validate step must run them, read from make -n ------------------
step_expansion=$(cd "$REPO_ROOT" && make -n -f "$MAKEFILE" "$DOCLINT_STEP" 2>/dev/null)
if [ -z "$step_expansion" ]; then
  fail "[3] 'make -n $DOCLINT_STEP' expanded to nothing — no merge-gate step runs the doc-lint tests"
else
  case "$step_expansion" in
    *"-tags"*"$DOCLINT_TAG"*) pass "[3] $DOCLINT_STEP passes -tags $DOCLINT_TAG" ;;
    *) fail "[3] $DOCLINT_STEP does not pass -tags $DOCLINT_TAG, so it cannot reach the moved tests" ;;
  esac
  validate_steps=$(cd "$REPO_ROOT" && make -n -f "$MAKEFILE" print-validate-steps 2>/dev/null)
  case "$validate_steps" in
    *"$DOCLINT_STEP"*) pass "[3] $DOCLINT_STEP is listed in VALIDATE_STEPS" ;;
    *) fail "[3] $DOCLINT_STEP exists but is NOT in VALIDATE_STEPS — nothing would ever run it" ;;
  esac
  # This gate must itself be wired in, or it can be unhooked from validate and
  # keep passing whenever someone runs it by hand.
  case "$validate_steps" in
    *"$SELF_STEP"*) pass "[3] $SELF_STEP (this gate) is itself listed in VALIDATE_STEPS" ;;
    *) fail "[3] $SELF_STEP is NOT in VALIDATE_STEPS — this gate could be silently unhooked and still pass by hand" ;;
  esac
  phony=$(cd "$REPO_ROOT" && grep -h '^\.PHONY:' "$MAKEFILE" 2>/dev/null)
  for tgt in "$DOCLINT_STEP" "$SELF_STEP"; do
    case " $phony " in
      *" $tgt "*) pass "[3] $tgt is declared .PHONY" ;;
      *) fail "[3] $tgt is not declared .PHONY — a same-named file would make it a silent no-op" ;;
    esac
  done
fi

# --- [4] the step must actually EXECUTE both tests -------------------------
# The load-bearing assertion. [3] passes for a step that runs zero tests.
# CRITICAL: run THE STEP'S OWN command, recovered from `make -n`, with -v
# appended. Do NOT re-implement the invocation here.
#
# The first version of this gate hardcoded `-run "^($TEST_A|$TEST_B)$"` — the
# same two variables it then asserted on — so the Makefile's actual -run regex
# was never read. A reviewer mutated Makefile's regex to
# `TestSkillsGoSymbolBanListIsDeadTYPO` and this gate reported
# "both doc-lint tests execute under the merge gate (2/2)" / 15 passed, 0 failed
# while the merge step executed ZERO tests. `go test` exits 0 on a zero-test
# run, so nothing else in the tree caught it either. That is precisely the
# invisible-in-both-directions failure this gate is for.
#
# Reading the command from the step means a -run typo, a package-path typo, an
# added -short, or a dropped tag all surface here.
if [ -z "$step_expansion" ]; then
  fail "[4] cannot execute the step: 'make -n $DOCLINT_STEP' expanded to nothing"
else
  step_cmd=$(printf '%s\n' "$step_expansion" | grep -E '^[[:space:]]*go test ' | head -1)
  if [ -z "$step_cmd" ]; then
    fail "[4] $DOCLINT_STEP's expansion contains no 'go test' line — cannot execute what the merge gate would run"
  else
    pass "[4a] recovered the step's own go-test command from make -n"
    run_out=$(cd "$REPO_ROOT" && eval "$step_cmd -v" 2>&1)
    run_rc=$?
    ran=0
    for t in "$TEST_A" "$TEST_B"; do
      if printf '%s\n' "$run_out" | grep -qE "^(=== RUN|--- PASS|--- FAIL)[[:space:]]+$t\$"; then
        ran=$((ran + 1))
        pass "[4] $t was really executed by THE MERGE STEP'S OWN command"
      else
        fail "[4] $t did NOT execute when running the merge step's own command — the merge gate reports green having run nothing. Check $DOCLINT_STEP's -run regex and package path in the Makefile"
      fi
    done
    if [ "$ran" -eq 2 ] && [ "$run_rc" -eq 0 ]; then
      pass "[4] the merge step really executes both doc-lint tests and they pass (2/2, rc=0)"
    else
      fail "[4] merge step executed $ran of 2 doc-lint tests (rc=$run_rc)"
    fi
  fi
fi

# --- [5] -race absent, and that absence is justified structurally ----------
# Guard the empty case FIRST. An empty $step_expansion matches the catch-all
# arm below, so without this the "-race is absent" assertion PASSES when the
# step does not exist at all — a check that reports green for a missing subject.
# Found by running this gate red before implementing anything.
if [ -z "$step_expansion" ]; then
  fail "[5] cannot judge -race: 'make -n $DOCLINT_STEP' expanded to nothing, so there is no step to inspect"
else
  case "$step_expansion" in
    *-race*) fail "[5] $DOCLINT_STEP carries -race — that is the 20x cost this split removed (32.57s vs 1.63s for $TEST_A)" ;;
    *) pass "[5] $DOCLINT_STEP does not carry -race" ;;
  esac
fi
# Dropping -race is only safe because these tests have no concurrency at all.
# Assert that rather than trusting the comment, so the justification cannot rot.
#
# conc_probe greps CODE only. Comments are stripped first, because the first
# version of this check matched the file's own sentence explaining that it has
# "no goroutines, no channels, no sync package and no t.Parallel" — a probe that
# fired on the prose describing the absence it was looking for.
#
# Known limit, stated rather than hidden: stripping `//` also truncates a line
# at a `//` inside a string literal, which could mask a primitive appearing
# after one on the same line. That is why the positive control below is not
# optional — it is the only thing establishing this probe can still fire.
#
# The alternation deliberately covers the NAMED-function goroutine form as well
# as the anonymous one. The first version matched only `go func`, so a plain
# `go doWork()` — the ordinary case — slipped through entirely, as did
# `atomic.Int64`. Each alternation has its own fixture below; a control that
# plants only `go func` exercises one branch and passes a typo in any other.
#
# The goroutine alternations require a CALL PAREN. Without it, `\bgo[[:space:]]+`
# matched the English words "go files" inside a t.Fatalf string literal in the
# file under test — comment-stripping does not remove string literals. A
# goroutine is always `go <expr>(`, so demanding the paren removes that whole
# class of false positive without narrowing what it detects. Both directions are
# controlled below.
CONC_RE='\bgo[[:space:]]+[[:alnum:]_.]*\(|\bchan\b|\bsync\.|\batomic\.|\berrgroup\b|t\.Parallel'
conc_probe() {
  sed -e 's;//.*$;;' "$1" | grep -nE "$CONC_RE"
}
if [ -f "$REPO_ROOT/$MOVED_FILE" ]; then
  if conc_probe "$REPO_ROOT/$MOVED_FILE" >/dev/null 2>&1; then
    fail "[5] $MOVED_FILE now contains a concurrency primitive (goroutine/chan/sync/t.Parallel) — -race is owed again; either revert the concurrency or put this step back under -race"
  else
    pass "[5] $MOVED_FILE contains no concurrency primitives, so -race could not observe anything there"
  fi
  # Positive control, run EVERY time rather than trusted from a transcript: the
  # same probe against a copy with a goroutine planted MUST fire. Without this,
  # "no primitives found" is indistinguishable from a probe that cannot find any.
  # One positive control PER ALTERNATION. A single `go func` fixture leaves a
  # typo in any other branch undetected.
  probe_fired=0
  probe_total=0
  while IFS='|' read -r label snippet; do
    probe_total=$((probe_total + 1))
    printf 'package cmd\n\nfunc plantedForControl() {\n%s\n}\n' "$snippet" > "$SCRATCH/conc_$probe_total.go"
    if conc_probe "$SCRATCH/conc_$probe_total.go" >/dev/null 2>&1; then
      probe_fired=$((probe_fired + 1))
    else
      fail "[5] positive control FAILED for '$label': the concurrency probe did not fire on: $snippet"
    fi
  done <<'FIXTURES'
anonymous goroutine|go func() {}()
named goroutine|go doWork()
channel|var c chan int
sync package|var wg sync.WaitGroup
atomic|var n atomic.Int64
errgroup|var g errgroup.Group
t.Parallel|t.Parallel()
FIXTURES
  if [ "$probe_fired" -eq "$probe_total" ] && [ "$probe_total" -eq 7 ]; then
    pass "[5] positive controls: the probe fires on all $probe_total concurrency forms"
  else
    fail "[5] positive controls: probe fired on only $probe_fired of $probe_total concurrency forms"
  fi
  # Negative control: the probe must stay quiet on a file with no primitives.
  printf 'package cmd\n\nfunc quietForControl() int { return 1 }\n' > "$SCRATCH/quiet_fixture.go"
  if conc_probe "$SCRATCH/quiet_fixture.go" >/dev/null 2>&1; then
    fail "[5] negative control FAILED: the concurrency probe fired on a file with no concurrency — it over-matches, so its verdict on $MOVED_FILE means nothing"
  else
    pass "[5] negative control: the concurrency probe stays quiet on a clean file"
  fi
else
  fail "[5] $MOVED_FILE does not exist — the moved tests are not where this gate expects them"
fi

# --- results ---------------------------------------------------------------
echo "=== Results: $PASSES passed, $FAILURES failed ==="
observed=$((PASSES + FAILURES))
if [ "$observed" -lt "$MIN_ASSERTIONS" ]; then
  echo "  FAIL: only $observed assertion(s) ran but MIN_ASSERTIONS=$MIN_ASSERTIONS — this gate measured less than it claims (QUM-1029)" >&2
  exit 1
fi
[ "$FAILURES" -eq 0 ] || exit 1
exit 0
