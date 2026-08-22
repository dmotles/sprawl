#!/usr/bin/env bash
#
# test-validate-timing-unit.sh (QUM-1286)
#
# Unit suite for the `make validate` per-step timing instrumentation
# (scripts/validate-timed.sh) and for the recorded baseline
# (scripts/testdata/validate-baseline.observed, checked by
# scripts/check-validate-baseline.sh).
#
# WHAT SILENT REGRESSION THIS GUARDS
#
# A timing wrapper is a textbook non-asserting fallback waiting to happen: it
# captures a step's duration and loses its exit status, so `make validate`
# prints a pretty table and exits 0 over a failed step. Nothing else in the tree
# would notice — the pre-commit hook only reads validate's exit code, and a
# green exit is exactly what a swallowed failure produces. Sections [2]/[3]/[4]
# are that property: a failing step must make the driver exit with THAT step's
# code, name THAT step, and skip everything after it.
#
# Section [9] is the second regression, and it is the one QUM-1286 exists
# because of: the only recorded validate baseline was undated prose in a
# Makefile comment, and it drifted ~25s with nobody noticing. The baseline is
# now a checked artifact whose step set must equal the live VALIDATE_STEPS, so
# adding or removing a validate step forces a re-measurement in the same commit.
#
# Section [8] is the third, and the least obvious. scripts/test-race-gate.sh
# reads validate's wiring from `make -n validate` and asserts every `go test`
# line carries -race over ./... . Its own header records the known limit: a
# `go test` invoked from inside a shell script is invisible to `make -n`. This
# instrumentation therefore had to keep `go test -race` in a Makefile recipe and
# rely on GNU make's recursion special case (a recipe line containing the
# literal $(MAKE) is executed even under -n, with n handed down via MAKEFLAGS).
# "Simplify" that to a bare `make` and the race gate goes blind while staying
# green. [8] fires when that happens.
#
# THE DRIVER'S OUTPUT CONTRACT (invented here, asserted here, and implemented by
# scripts/validate-timed.sh — written down so the next reader is not
# reverse-engineering these regexes):
#
#   per step (on completion)  "  %7.2fs  <step-name>"
#   total banner             "=== validate timing (total <N.NN>s) ==="
#   cache line               "  cache: lint=<warm|cold> go_test=<warm|cold|unknown>"
#   package summary          "  test packages: total=<N> cached=<N> ran=<N> notests=<N>"
#   top-N list               "  top packages by duration:" then "    %7.2fs  <import path>"
#   failure                  "=== validate FAILED at step <name> (rc=<N>) after <N.NN>s ==="
#   interruption             "=== validate INTERRUPTED during step <name> after <N.NN>s (signal <N>) ==="
#   empty parse              "  FAIL: captured step <name> produced NO package result lines"
#
# WHY THE FIXTURES CALL THE DRIVER DIRECTLY, AND WHY MOST OF THEM FAKE `make`
#
# GNU make exits **2** for any recipe failure — it never propagates the recipe's
# own status (measured: a recipe doing `exit 42` yields `make rc=2`). That is true
# twice over here, and it cost two drafts:
#
#   * driving the fixtures through a `validate` TARGET made every exit-code leg
#     unsatisfiable, because the suite was reading make's status, not the
#     driver's. So the driver is invoked directly — which is also exactly how the
#     real Makefile's recipe invokes it.
#   * with real make as the child, the driver can only ever observe 2, so a leg
#     asserting "the child's status is propagated verbatim" cannot tell faithful
#     propagation from a remap-to-2. The tempting repair (assert rc != 0) throws
#     away the whole property: `exit 0` also satisfies "not 42".
#
# So the exit-code legs inject a FAKE make via MAKE_CMD (`$SCRATCH/fakemake`,
# which runs a per-target script and exits with its status), and one leg in [2]
# drives REAL make end to end so the actual invocation shape is covered too.
# MAKE_CMD is a production seam, not a test-only hook: the real Makefile sets it
# to the literal $(MAKE).
#
# Pure bash + make + coreutils. No claude, no tmux, no network, no Go build.
# Reads the real tree only in the legs marked LIVE.
#
# NOT `set -e`: every assertion must be reported, not just the first. Same
# choice, for the same reason, as scripts/test-race-gate.sh.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel) || {
  echo "FATAL: not in a git repo" >&2
  exit 1
}
DRIVER="$REPO_ROOT/scripts/validate-timed.sh"
CHECKER="$REPO_ROOT/scripts/check-validate-baseline.sh"
BASELINE="$REPO_ROOT/scripts/testdata/validate-baseline.observed"
SAMPLE="$REPO_ROOT/scripts/testdata/validate-timing/go-test-sample.txt"
GARBAGE="$REPO_ROOT/scripts/testdata/validate-timing/go-test-garbage.txt"

TMPBASE=${TMPDIR:-/tmp}
SCRATCH=$(mktemp -d "$TMPBASE/sprawl-validate-timing.XXXXXX") || {
  echo "FATAL: mktemp -d failed under $TMPBASE" >&2
  exit 1
}
case "$SCRATCH" in
  /*) ;;
  *)
    echo "FATAL: mktemp returned a non-absolute path: '$SCRATCH'" >&2
    exit 1
    ;;
esac

cleanup() {
  # /tmp hygiene: assert the prefix before ANY delete, never rm a glob.
  case "$SCRATCH" in
    "$TMPBASE"/sprawl-validate-timing.*) rm -rf "$SCRATCH" ;;
    *) echo "REFUSING to delete unexpected path: $SCRATCH" >&2 ;;
  esac
}
trap cleanup EXIT

# Hand-computed EQUALITY gate, a hardcoded literal. NOT derived from any array
# in this script: a floor computed from the corpus it measures is satisfied by an
# empty corpus, which is the exact false-green it exists to stop.
#
# It is deliberately set to the EXACT total rather than to a loose minimum, so a
# leg that silently becomes conditional is caught as well as an early death.
# Adding an assertion means bumping this in the same commit.
#   [0] 3  [1] 5  [2] 8  [3] 4  [4] 3  [5] 4  [6] 5  [7] 4  [8] 8  [9] 10  [10] 5  [11] 5
MIN_ASSERTIONS=64

PASSES=0
FAILURES=0
ok() {
  PASSES=$((PASSES + 1))
  echo "  PASS: $1"
}
fail() {
  FAILURES=$((FAILURES + 1))
  echo "  FAIL: $1" >&2
}

# --no-print-directory is load-bearing, not cosmetic: `make -C` writes
# "Entering directory ..." to STDOUT, and the red-first run of this suite proved
# those two lines alone satisfied the "print-validate-steps yields N steps"
# assertion against a Makefile that had no such target. They also corrupt every
# awk/sed parse of a run's output.
#
# The env scrub matters because this script runs INSIDE `make validate`:
# MAKEFLAGS/MFLAGS/MAKELEVEL are in its environment, and an inherited -n or -k
# would silently change what every leg measures.
run_make() {
  env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL -u SPRAWL_VALIDATE_CAPTURE_STEPS \
    -u SPRAWL_VALIDATE_TIMING_OUT make --no-print-directory "$@"
}

# ---------------------------------------------------------------- fixtures ----

# A stand-in for make. The last argument is the target (that is the shape the
# driver invokes: `<make> -C dir -f file <target>`), and the target's behaviour is
# a plain script in $STUB_DIR. It exists so the exit-code legs can assert the
# driver propagates its child's EXACT status — see the header.
cat >"$SCRATCH/fakemake" <<'FAKEEOF'
#!/usr/bin/env bash
set -uo pipefail
target=${!#}
script="$STUB_DIR/$target.sh"
if [ ! -f "$script" ]; then
  echo "fakemake: no such target: $target" >&2
  exit 2
fi
bash "$script"
FAKEEOF

# new_fixture <dir>
new_fixture() {
  local dir=$1
  mkdir -p "$dir/stub" "$dir/out"
}

# append_step <dir> <name> <shell-line...>
append_step() {
  local dir=$1 name=$2
  shift 2
  printf '%s\n' '#!/usr/bin/env bash' "$@" >"$dir/stub/$name.sh"
}

# drive <dir> [VAR=VAL ...] -- <steps...>
# Sets DRIVE_OUT / DRIVE_RC from the DRIVER itself, not from make.
drive() {
  local dir=$1
  shift
  local -a envs=()
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do
    envs+=("$1")
    shift
  done
  [ "${1:-}" = "--" ] && shift
  DRIVE_OUT=$(env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL \
    SPRAWL_VALIDATE_MAKE_F="$dir/Makefile" \
    SPRAWL_VALIDATE_MAKE_C="$dir" \
    SPRAWL_VALIDATE_TIMING_OUT="$dir/out" \
    STUB_DIR="$dir/stub" \
    MAKE_CMD="bash $SCRATCH/fakemake" \
    "${envs[@]}" bash "$DRIVER" "$@" 2>&1)
  DRIVE_RC=$?
}

# drive_real <dir> -- <steps...> — the integration shape: a real Makefile, real
# make as the child. Cannot assert an exact child status (make collapses to 2),
# so it asserts non-zero plus correct attribution.
drive_real() {
  local dir=$1
  shift
  [ "${1:-}" = "--" ] && shift
  DRIVE_OUT=$(env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL \
    SPRAWL_VALIDATE_MAKE_F="$dir/Makefile" \
    SPRAWL_VALIDATE_MAKE_C="$dir" \
    SPRAWL_VALIDATE_TIMING_OUT="$dir/out" \
    MAKE_CMD="make --no-print-directory" \
    bash "$DRIVER" "$@" 2>&1)
  DRIVE_RC=$?
}

echo "=== [0] preconditions"
if [ -r "$DRIVER" ]; then
  ok "driver exists and is readable: scripts/validate-timed.sh"
else
  fail "driver missing or unreadable: $DRIVER"
fi
if [ -n "${EPOCHREALTIME:-}" ]; then
  ok "bash provides EPOCHREALTIME (sub-second timing available)"
else
  fail "bash does not provide EPOCHREALTIME — need bash >= 5 for sub-second timing"
fi
if [ -d "$SCRATCH" ]; then
  ok "scratch dir created under $TMPBASE"
else
  fail "scratch dir missing: $SCRATCH"
fi

echo "=== [1] the step list has ONE source of truth (LIVE)"
STEPS_LIVE=$(run_make -C "$REPO_ROOT" print-validate-steps 2>/dev/null)
STEPS_RC=$?
if [ "$STEPS_RC" -eq 0 ] && [ -n "$STEPS_LIVE" ] &&
  ! printf '%s\n' "$STEPS_LIVE" | grep -qE 'make(\[[0-9]+\])?:|directory'; then
  ok "make print-validate-steps yields $(printf '%s\n' "$STEPS_LIVE" | grep -c .) step(s)"
else
  fail "make print-validate-steps produced nothing usable (rc=$STEPS_RC) — the introspection seam is missing or printing make's own noise"
fi
# ONE STEP PER LINE is a contract three later legs depend on ([9]'s step-set
# comparison and its missing-step mutation). A space-separated single line would
# make that mutation a silent no-op, so it is asserted rather than assumed.
if [ -n "$STEPS_LIVE" ] && ! printf '%s\n' "$STEPS_LIVE" | grep -q '[[:space:]]'; then
  ok "print-validate-steps emits exactly one step per line, no embedded whitespace"
else
  fail "print-validate-steps does not emit one bare step per line — [9]'s step-set legs would compare the wrong things"
fi
MAKEFILE_TEXT="$REPO_ROOT/Makefile"
# validate's recipe, extracted from the Makefile source. Read from the source
# rather than from `make -n` on purpose: the recursion special case means `make
# -n validate` output contains the DRIVER's stdout, so a `make -n` grep here
# could be satisfied by a string the driver minted (provenance).
VALIDATE_RECIPE=$(awk '/^validate:/{f=1;next} f&&/^\t/{print;next} f&&!/^\t/{exit}' "$MAKEFILE_TEXT")
if [ "$(grep -cE '^VALIDATE_STEPS[[:space:]]*[:+]?=' "$MAKEFILE_TEXT")" -ge 1 ] &&
  printf '%s\n' "$VALIDATE_RECIPE" | grep -q 'VALIDATE_STEPS'; then
  ok "Makefile declares VALIDATE_STEPS and validate's recipe drives it (single source of truth)"
else
  fail "validate's recipe does not reference \$(VALIDATE_STEPS) — the step list has a second, hand-maintained copy"
fi
if printf '%s\n' "$VALIDATE_RECIPE" | grep -qF '$(MAKE)'; then
  ok "validate's recipe uses the literal \$(MAKE) (keeps sub-steps visible to make -n)"
else
  fail "validate's recipe does not contain the literal \$(MAKE) — make -n validate will NOT expand the steps and scripts/test-race-gate.sh goes blind while staying green"
fi
if printf '%s\n' "$VALIDATE_RECIPE" | grep -q 'validate-timed'; then
  ok "validate's recipe drives scripts/validate-timed.sh"
else
  fail "validate's recipe does not invoke scripts/validate-timed.sh — instrumentation is unwired"
fi

echo "=== [2] a failing step is not swallowed"
F2="$SCRATCH/f2"
new_fixture "$F2"
append_step "$F2" s-ok "touch $F2/ok.sentinel"
append_step "$F2" s-fail "touch $F2/fail.sentinel" "exit 42"
append_step "$F2" s-never "touch $F2/never.sentinel"
drive "$F2" -- s-ok s-fail s-never
F2_OUT=$DRIVE_OUT
if [ "$DRIVE_RC" -eq 42 ]; then
  ok "driver propagates the failing step's exit code (42)"
else
  fail "driver returned $DRIVE_RC, want 42 — a failing step's exit status was lost: $(printf '%s' "$DRIVE_OUT" | tail -3 | tr '\n' '|')"
fi
if printf '%s\n' "$F2_OUT" | grep -q 'FAILED at step s-fail'; then
  ok "driver names the failing step (s-fail)"
else
  fail "driver did not attribute the failure to s-fail: $(printf '%s' "$DRIVE_OUT" | tail -3 | tr '\n' '|')"
fi
if [ -e "$F2/ok.sentinel" ]; then
  ok "the step before the failure ran"
else
  fail "s-ok never ran — the driver is not executing steps in order"
fi
if [ -e "$F2/fail.sentinel" ] && [ ! -e "$F2/never.sentinel" ]; then
  ok "the failing step ran and the step after it did NOT (fail-fast preserved)"
else
  fail "fail-fast broken: fail.sentinel=$([ -e "$F2/fail.sentinel" ] && echo yes || echo no) never.sentinel=$([ -e "$F2/never.sentinel" ] && echo yes || echo no)"
fi
if printf '%s\n' "$F2_OUT" | grep -q '=== validate timing'; then
  ok "a partial timing table is still printed on failure"
else
  fail "no timing table on failure — the breakdown is lost exactly when it is most useful"
fi
F2B="$SCRATCH/f2b"
new_fixture "$F2B"
append_step "$F2B" p-one 'true'
append_step "$F2B" p-two 'true'
drive "$F2B" -- p-one p-two
if [ "$DRIVE_RC" -eq 0 ]; then
  ok "an all-passing run exits 0"
else
  fail "all-passing run exited $DRIVE_RC — the driver invents failures: $(printf '%s' "$DRIVE_OUT" | tail -3 | tr '\n' '|')"
fi
# Gated on the run having actually produced a table, and on the SAME grep having
# been shown to fire above: a bare "the string is absent" leg passes when the
# driver prints nothing at all.
if printf '%s\n' "$DRIVE_OUT" | grep -q '=== validate timing' &&
  printf '%s\n' "$F2_OUT" | grep -q 'FAILED at step' &&
  ! printf '%s\n' "$DRIVE_OUT" | grep -q 'FAILED at step'; then
  ok "an all-passing run prints no FAILED-at-step line (and the same grep DID fire on the failing run)"
else
  fail "all-pass/failure discrimination inconclusive — table present=$(printf '%s\n' "$DRIVE_OUT" | grep -c '=== validate timing'), FAILED on pass-run=$(printf '%s\n' "$DRIVE_OUT" | grep -c 'FAILED at step'), FAILED on fail-run=$(printf '%s\n' "$F2_OUT" | grep -c 'FAILED at step')"
fi

# The integration shape: a real Makefile and real make as the child. It cannot
# pin an exact child status (make collapses every recipe failure to 2), so it
# pins the two properties that survive: non-zero out, right step named.
F2C="$SCRATCH/f2c"
mkdir -p "$F2C/out"
cat >"$F2C/Makefile" <<'EOF'
.PHONY: r-ok r-fail
r-ok:
	@true
r-fail:
	@exit 42
EOF
drive_real "$F2C" -- r-ok r-fail
if [ "$DRIVE_RC" -ne 0 ] && printf '%s\n' "$DRIVE_OUT" | grep -q 'FAILED at step r-fail'; then
  ok "real make as the child: a failing target still exits non-zero (rc=$DRIVE_RC) and is attributed to r-fail"
else
  fail "real-make integration leg: rc=$DRIVE_RC, attribution=$(printf '%s\n' "$DRIVE_OUT" | grep -c 'FAILED at step r-fail')"
fi

echo "=== [3] attribution is positional, not hardcoded"
F3A="$SCRATCH/f3a"
new_fixture "$F3A"
append_step "$F3A" first 'exit 7'
append_step "$F3A" middle 'true'
append_step "$F3A" last 'true'
drive "$F3A" -- first middle last
if [ "$DRIVE_RC" -eq 7 ]; then
  ok "first-step failure: rc 7 preserved"
else
  fail "first-step failure returned $DRIVE_RC, want 7"
fi
if printf '%s\n' "$DRIVE_OUT" | grep -q 'FAILED at step first'; then
  ok "first-step failure: names 'first'"
else
  fail "first-step failure blamed the wrong step: $(printf '%s\n' "$DRIVE_OUT" | grep -o 'FAILED at step.*' | head -1)"
fi
F3B="$SCRATCH/f3b"
new_fixture "$F3B"
append_step "$F3B" first 'true'
append_step "$F3B" middle 'true'
append_step "$F3B" last 'exit 9'
drive "$F3B" -- first middle last
if [ "$DRIVE_RC" -eq 9 ]; then
  ok "last-step failure: rc 9 preserved"
else
  fail "last-step failure returned $DRIVE_RC, want 9"
fi
if printf '%s\n' "$DRIVE_OUT" | grep -q 'FAILED at step last'; then
  ok "last-step failure: names 'last'"
else
  fail "last-step failure blamed the wrong step: $(printf '%s\n' "$DRIVE_OUT" | grep -o 'FAILED at step.*' | head -1)"
fi

echo "=== [4] a CAPTURED (tee'd) step's exit code survives the pipe"
F4="$SCRATCH/f4"
new_fixture "$F4"
append_step "$F4" cap-step "cat $SAMPLE" "exit 3"
drive "$F4" SPRAWL_VALIDATE_CAPTURE_STEPS=cap-step -- cap-step
if [ "$DRIVE_RC" -eq 3 ]; then
  ok "captured step: exit code 3 survives the tee pipeline"
else
  fail "captured step returned $DRIVE_RC, want 3 — the pipeline ate the exit status (the QUM-1286 hazard): $(printf '%s' "$DRIVE_OUT" | tail -3 | tr '\n' '|')"
fi
if printf '%s\n' "$DRIVE_OUT" | grep -q 'FAILED at step cap-step'; then
  ok "captured step: failure attributed to cap-step"
else
  fail "captured step: failure not attributed"
fi
if [ -s "$F4/out/cap-step.txt" ]; then
  ok "captured step: transcript written non-empty"
else
  fail "captured step: no transcript at $F4/out/cap-step.txt"
fi

echo "=== [5] timing resolution is sub-second, not whole-second"
F5="$SCRATCH/f5"
new_fixture "$F5"
append_step "$F5" sleeper 'sleep 0.4'
drive "$F5" -- sleeper
S5=$(printf '%s\n' "$DRIVE_OUT" | awk '$2=="sleeper"{print $1}' | tr -d 's' | tail -1)
T5=$(printf '%s\n' "$DRIVE_OUT" | sed -n 's/.*=== validate timing (total \([0-9.]*\)s).*/\1/p' | tail -1)
if [ -n "$S5" ] && awk -v v="$S5" 'BEGIN{exit !(v>=0.2)}'; then
  ok "sleeper step measured >= 0.2s (got ${S5}s)"
else
  fail "sleeper step measured '${S5:-<none>}' — want >= 0.2s"
fi
if [ -n "$S5" ] && awk -v v="$S5" 'BEGIN{exit !(v<10)}'; then
  ok "sleeper step measured < 10s (got ${S5}s) — not a wall-clock-of-everything bug"
else
  fail "sleeper step measured '${S5:-<none>}' — implausibly large"
fi
# The whole-second-granularity control. Gated on non-empty: an empty S5 satisfies
# both inequalities of a bare != 0 test, which is the vacuity class this suite
# was already caught by once.
if [ -n "$S5" ] && [ "$S5" != "0" ] && [ "$S5" != "0.00" ]; then
  ok "sleeper step is not reported as zero (SECONDS-granularity control)"
else
  fail "sleeper step reported as '${S5:-<none>}' — timing has whole-second granularity or was not reported"
fi
if [ -n "$T5" ] && [ -n "$S5" ] && awk -v t="$T5" -v s="$S5" 'BEGIN{exit !(t>=s)}'; then
  ok "total (${T5}s) >= the single step it contains (${S5}s)"
else
  fail "total '${T5:-<none>}' is not >= step '${S5:-<none>}'"
fi

echo "=== [6] per-package parse of the test transcript"
F6="$SCRATCH/f6"
new_fixture "$F6"
append_step "$F6" cap-step "cat $SAMPLE"
drive "$F6" SPRAWL_VALIDATE_CAPTURE_STEPS=cap-step -- cap-step
TOPPKG=$(printf '%s\n' "$DRIVE_OUT" | sed -n 's|^ *[0-9.]*s  \(github.com/[^ ]*\)$|\1|p' | head -1)
if [ "$TOPPKG" = "github.com/dmotles/sprawl/internal/bravo" ]; then
  ok "top package by duration is bravo (12.500s)"
else
  fail "top package is '${TOPPKG:-<none>}', want .../internal/bravo"
fi
PKGSUM=$(printf '%s\n' "$DRIVE_OUT" | grep -o 'test packages:.*' | head -1)
if printf '%s' "$PKGSUM" | grep -qE '(^|[^0-9])cached=2([^0-9]|$)'; then
  ok "(cached) packages counted as cached, exactly 2"
else
  fail "cached count wrong: '${PKGSUM:-<no summary line>}'"
fi
if printf '%s' "$PKGSUM" | grep -qE '(^|[^0-9])total=8([^0-9]|$)'; then
  ok "package total counted as 8 (ok+FAIL lines; [no test files] excluded)"
else
  fail "package total wrong: '${PKGSUM:-<no summary line>}'"
fi
# Scoped to the top-N BLOCK, not to the whole run: the captured transcript is
# streamed to stdout too, so a bare grep for the package name over DRIVE_OUT is
# satisfied by the driver echoing go's own line back — a vacuous pass that this
# leg was caught giving before the parser worked at all.
TOPBLOCK=$(printf '%s\n' "$DRIVE_OUT" | sed -n 's|^ *[0-9.]*s  \(github.com/[^ ]*\)$|\1|p')
if printf '%s\n' "$TOPBLOCK" | grep -q 'internal/hotel'; then
  ok "a mid-table package (hotel, 7.250s) appears in the top-5 block"
else
  fail "hotel missing from the top-5 block (block=[$(printf '%s' "$TOPBLOCK" | tr '\n' ' ')])"
fi
F6B="$SCRATCH/f6b"
new_fixture "$F6B"
append_step "$F6B" cap-step "cat $GARBAGE"
drive "$F6B" SPRAWL_VALIDATE_CAPTURE_STEPS=cap-step -- cap-step
if [ "$DRIVE_RC" -ne 0 ] && printf '%s\n' "$DRIVE_OUT" | grep -q 'NO package result lines'; then
  ok "a captured step yielding zero packages fails loudly (rc=$DRIVE_RC) rather than reporting an empty table as success"
else
  fail "zero-package parse exited $DRIVE_RC without saying so — the per-package measurement can silently measure nothing"
fi

echo "=== [7] cache-warmth probe discriminates cold from warm"
F7="$SCRATCH/f7"
new_fixture "$F7"
append_step "$F7" noop 'true'
mkdir -p "$F7/coldcache"
drive "$F7" GOLANGCI_LINT_CACHE="$F7/coldcache" -- noop
COLD_LINE=$(printf '%s\n' "$DRIVE_OUT" | grep -o 'cache:.*' | head -1)
if printf '%s' "$COLD_LINE" | grep -q 'lint=cold'; then
  ok "empty GOLANGCI_LINT_CACHE reports lint=cold"
else
  fail "empty lint cache did not report cold: '${COLD_LINE:-<no cache line>}'"
fi
if [ -n "$COLD_LINE" ] && ! printf '%s' "$COLD_LINE" | grep -q 'lint=warm'; then
  ok "the cold run does NOT also claim warm (tokens are mutually exclusive)"
else
  fail "cold run reported warm too, or produced no cache line: '${COLD_LINE:-<no cache line>}'"
fi
mkdir -p "$F7/warmcache/sub" && echo x >"$F7/warmcache/sub/entry"
drive "$F7" GOLANGCI_LINT_CACHE="$F7/warmcache" -- noop
WARM_LINE=$(printf '%s\n' "$DRIVE_OUT" | grep -o 'cache:.*' | head -1)
if printf '%s' "$WARM_LINE" | grep -q 'lint=warm'; then
  ok "populated GOLANGCI_LINT_CACHE reports lint=warm"
else
  fail "populated lint cache did not report warm: '${WARM_LINE:-<no cache line>}'"
fi
if [ -n "$WARM_LINE" ] && ! printf '%s' "$WARM_LINE" | grep -q 'lint=cold'; then
  ok "the warm run does NOT also claim cold"
else
  fail "warm run reported cold too, or produced no cache line: '${WARM_LINE:-<no cache line>}'"
fi

echo "=== [8] make -n fidelity — the scripts/test-race-gate.sh compatibility guard (LIVE)"
# The -race assertion is derived from `make -n test-race`, whose recipe contains
# no $(MAKE) and so is genuinely only expanded, never executed. Deriving it from
# `make -n validate` would be a provenance failure: that output contains the
# driver's own stdout.
DRY_TESTRACE=$(run_make -C "$REPO_ROOT" -n test-race 2>&1)
RACE_LINE=$(printf '%s\n' "$DRY_TESTRACE" | grep -E '(^|[[:space:]]|;|&|\|)go test([[:space:]]|$)' | grep -E '(^|[[:space:]])-race([[:space:]]|$)' | head -1)
if [ -n "$RACE_LINE" ] && printf '%s\n' "$RACE_LINE" | grep -qE '(^|[[:space:]])\./\.\.\.([[:space:]]|$)'; then
  ok "make -n test-race expands a 'go test ... -race ... ./...' line"
else
  fail "make -n test-race has no 'go test -race ./...' line: '${RACE_LINE:-<none>}'"
fi
# Provenance closure: the driver must not be able to MINT a 'go test' string, or
# the next leg proves nothing about the Makefile.
# COMMENT LINES ARE EXCLUDED, and that is a correction rather than a loophole:
# the property is that no EXECUTABLE line of the driver can emit a `go test`
# string, so that a `go test` line in validate's expansion is necessarily the
# Makefile's. Matching the whole file also matched the driver's prose about the
# orphaned `go test -race ./...` it now reaps — a false ALARM, which is the
# cheaper direction to be wrong in but still wrong. The `-r` guard keeps an
# unreadable driver a failure rather than a clean scan.
if [ -r "$DRIVER" ] && ! grep -vE '^[[:space:]]*#' "$DRIVER" | grep -q 'go test'; then
  ok "no executable line of the driver contains 'go test' — a 'go test' line in validate's expansion can only come from the Makefile"
else
  fail "an executable line of the driver contains 'go test' (or the driver is unreadable) — [8]'s next leg could be satisfied by driver-minted output"
fi
LIVE_DRY=$(env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL \
  SPRAWL_VALIDATE_TIMING_OUT="$SCRATCH/live-dry" \
  make --no-print-directory -C "$REPO_ROOT" -n validate 2>&1)
if [ -n "$RACE_LINE" ] && printf '%s\n' "$LIVE_DRY" | grep -qF "$RACE_LINE"; then
  ok "make -n validate STILL expands that exact go test -race line (race gate can see it)"
else
  fail "make -n validate no longer shows test-race's 'go test -race' line — scripts/test-race-gate.sh group [1] is BLIND and will stay green"
fi
# Gated on the banner being producible at all: [2] already proved this exact
# string appears in a real run, so its absence here is about -n, not about a
# renamed banner.
if printf '%s\n' "$F2_OUT" | grep -q '=== validate timing' &&
  ! printf '%s\n' "$LIVE_DRY" | grep -q '=== validate timing'; then
  ok "make -n validate prints no timing table, though the banner is demonstrably producible (driver is passthrough under -n)"
else
  fail "dry-run passthrough inconclusive: banner in real run=$(printf '%s\n' "$F2_OUT" | grep -c '=== validate timing'), banner in make -n=$(printf '%s\n' "$LIVE_DRY" | grep -c '=== validate timing')"
fi
if [ ! -e "$SCRATCH/live-dry" ]; then
  ok "make -n validate wrote no timing artifacts (a dry run must not touch the filesystem)"
else
  fail "make -n validate created $SCRATCH/live-dry — the driver writes files during a dry run"
fi
# Positive control for the recursion special case: with a bare `make` instead of
# the literal $(MAKE), make -n does NOT execute the recipe, so sub-step recipes
# vanish from the expansion. This is exactly the regression that blinds the race
# gate, and the assertion above cannot be trusted without seeing it fire.
F8="$SCRATCH/f8"
mkdir -p "$F8"
for tok in bare literal; do
  case $tok in
    bare) inv='make' ;;
    literal) inv='$(MAKE)' ;;
  esac
  cat >"$F8/Makefile-$tok" <<EOF
.PHONY: validate inner
validate:
	@$inv -f \$(THIS) inner
inner:
	@echo INNER_RECIPE_MARKER
EOF
done
D8_BARE=$(run_make -C "$F8" -f "$F8/Makefile-bare" THIS="$F8/Makefile-bare" -n validate 2>&1)
D8_LIT=$(run_make -C "$F8" -f "$F8/Makefile-literal" THIS="$F8/Makefile-literal" -n validate 2>&1)
if printf '%s\n' "$D8_LIT" | grep -q 'INNER_RECIPE_MARKER' &&
  ! printf '%s\n' "$D8_BARE" | grep -q 'INNER_RECIPE_MARKER'; then
  ok "positive control fired: the \$(MAKE)-literal form exposes sub-recipes to make -n, a bare 'make' does not"
else
  fail "make -n recursion control inconclusive: literal marker=$(printf '%s\n' "$D8_LIT" | grep -c INNER_RECIPE_MARKER), bare marker=$(printf '%s\n' "$D8_BARE" | grep -c INNER_RECIPE_MARKER)"
fi
# make does NOT propagate -f to sub-makes, so the driver must be told which
# makefile to re-enter (SPRAWL_VALIDATE_MAKE_F). If it fell back to the default
# Makefile in $CWD, scripts/test-race-gate.sh's RACE_GATE_MAKEFILE seam — which
# runs `make -f <copy> -n validate` to demonstrate a failure — would silently
# stop being able to demonstrate anything.
F8C="$SCRATCH/f8c"
mkdir -p "$F8C/out"
cat >"$F8C/Makefile-alt" <<'EOF'
.PHONY: inner
inner:
	@echo FROM_ALT_FILE
EOF
cat >"$F8C/Makefile" <<'EOF'
.PHONY: inner
inner:
	@echo FROM_DEFAULT_FILE
EOF
D8C=$(env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL \
  SPRAWL_VALIDATE_MAKE_F="$F8C/Makefile-alt" SPRAWL_VALIDATE_MAKE_C="$F8C" \
  SPRAWL_VALIDATE_TIMING_OUT="$F8C/out" MAKE_CMD="make --no-print-directory" \
  bash "$DRIVER" inner 2>&1)
if printf '%s\n' "$D8C" | grep -q 'FROM_ALT_FILE' &&
  ! printf '%s\n' "$D8C" | grep -q 'FROM_DEFAULT_FILE'; then
  ok "the driver re-enters the makefile named by SPRAWL_VALIDATE_MAKE_F, not the default one in \$CWD (RACE_GATE_MAKEFILE seam preserved)"
else
  fail "the driver ignored SPRAWL_VALIDATE_MAKE_F and read the default Makefile — test-race-gate.sh's RACE_GATE_MAKEFILE seam is now inert: $(printf '%s' "$D8C" | tr '\n' '|')"
fi

# The RACE_GATE_MAKEFILE seam, from this side. scripts/test-race-gate.sh proves
# it can fail by running `make -f <copy> -n validate` on a copy with -race
# removed; that only demonstrates anything if the copy's expansion still REACHES
# `go test`. It did not: with the driver resolved from the makefile's own
# directory, a copy in /tmp looked for /tmp/scripts/validate-timed.sh, died at rc
# 127, and every group-[1] leg failed for an unrelated reason — red, and
# evidentially worthless.
F8D="$SCRATCH/f8d"
mkdir -p "$F8D"
sed 's/go test -race /go test /' "$MAKEFILE_TEXT" >"$F8D/Makefile-norace"
D8D=$(run_make -C "$REPO_ROOT" -f "$F8D/Makefile-norace" -n validate 2>&1)
if printf '%s\n' "$D8D" | grep -qE '(^|[[:space:]])go test([[:space:]]|$)' &&
  ! printf '%s\n' "$D8D" | grep -q 'No such file or directory'; then
  ok "a makefile COPY outside the repo still expands a go test line (RACE_GATE_MAKEFILE seam can still demonstrate a failure)"
else
  fail "a makefile copy outside the repo lost its go test line — test-race-gate.sh's demo mode now fails for the wrong reason and proves nothing: $(printf '%s' "$D8D" | tail -2 | tr '\n' '|')"
fi

echo "=== [9] the recorded baseline is a checked artifact, not a remembered one (LIVE)"
CHECKER_OK=0
if [ -r "$CHECKER" ]; then
  ok "baseline checker exists: scripts/check-validate-baseline.sh"
  CHECKER_OK=1
else
  fail "baseline checker missing: $CHECKER"
fi
BASELINE_OK=0
if [ -r "$BASELINE" ]; then
  BASELINE_OK=1
fi
CHK_OUT=$(bash "$CHECKER" "$BASELINE" 2>&1)
CHK_RC=$?
if [ "$CHECKER_OK" -eq 1 ] && [ "$BASELINE_OK" -eq 1 ] && [ "$CHK_RC" -eq 0 ]; then
  ok "the committed baseline passes its own checker (negative control: a subject known clean stays quiet)"
else
  fail "committed baseline does not pass its checker (checker=$CHECKER_OK baseline=$BASELINE_OK rc=$CHK_RC): $(printf '%s' "$CHK_OUT" | tr '\n' '|')"
fi
BSTEPS=$(sed -n 's/^step\t\([^\t]*\)\t.*/\1/p' "$BASELINE" 2>/dev/null | sort)
if [ -n "$BSTEPS" ] && [ "$BSTEPS" = "$(printf '%s\n' "$STEPS_LIVE" | sort)" ]; then
  ok "baseline step set equals the live VALIDATE_STEPS (no drift)"
else
  fail "baseline step set differs from live VALIDATE_STEPS: only-in-baseline=[$(comm -23 <(printf '%s\n' "$BSTEPS") <(printf '%s\n' "$STEPS_LIVE" | sort) | tr '\n' ' ')] only-in-makefile=[$(comm -13 <(printf '%s\n' "$BSTEPS") <(printf '%s\n' "$STEPS_LIVE" | sort) | tr '\n' ' ')]"
fi
NPKG=$(grep -c '^pkg	' "$BASELINE" 2>/dev/null || echo 0)
[ -n "$NPKG" ] || NPKG=0
if [ "$NPKG" -ge 5 ]; then
  ok "baseline records at least 5 packages by duration (got $NPKG)"
else
  fail "baseline records $NPKG pkg rows, want >= 5"
fi
# The property in the issue title: DATED. An undated measurement that reads as
# current is the whole reason this issue exists.
BDATE=$(sed -n 's/^recorded=\([0-9][0-9-]*\)$/\1/p' "$BASELINE" 2>/dev/null | head -1)
if [ -n "$BDATE" ] && date -d "$BDATE" +%s >/dev/null 2>&1; then
  ok "baseline carries a parseable recorded= date ($BDATE)"
else
  fail "baseline has no parseable recorded= date (got '${BDATE:-<none>}') — an undated baseline is the defect this issue exists to fix"
fi
if [ -n "$BDATE" ] && [ "$(date -d "$BDATE" +%s 2>/dev/null || echo 0)" -le "$(date +%s)" ]; then
  ok "baseline recorded= date is not in the future"
else
  fail "baseline recorded= date '${BDATE:-<none>}' is in the future or unparseable"
fi
if [ "$BASELINE_OK" -eq 1 ] && ! grep -qE '/home/|/Users/' "$BASELINE"; then
  ok "baseline contains no home-directory path (PUBLIC repo leak hygiene)"
else
  fail "baseline is unreadable, or contains a home-directory path — leak hazard in a PUBLIC repo"
fi
# --- positive controls. Each requires the checker to be PRESENT, to have
# accepted the clean file above, and to reject the mutant with a reason naming
# the defect. "Any non-zero exit" would also be satisfied by 127/file-not-found.
pc() { # pc <label> <mutant-file> <reason-regex>
  local label=$1 mut=$2 want=$3 out rc
  if [ "$CHECKER_OK" -ne 1 ] || [ "$CHK_RC" -ne 0 ]; then
    fail "positive control '$label' cannot run: checker absent or it rejects the clean baseline"
    return
  fi
  if cmp -s "$BASELINE" "$mut"; then
    fail "positive control '$label' MUTATED NOTHING — the mutant is byte-identical to the baseline"
    return
  fi
  out=$(bash "$CHECKER" "$mut" 2>&1)
  rc=$?
  if [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -qEi "$want"; then
    ok "positive control: checker rejects $label (rc=$rc, reason matches /$want/)"
  else
    fail "checker ACCEPTED $label or gave the wrong reason (rc=$rc): $(printf '%s' "$out" | tr '\n' '|')"
  fi
}
MUT="$SCRATCH/baseline-nostep.observed"
grep -v "^step	$(printf '%s\n' "$STEPS_LIVE" | head -1)	" "$BASELINE" >"$MUT" 2>/dev/null
pc "a baseline missing a live step" "$MUT" 'missing a live validate step'
MUT2="$SCRATCH/baseline-leak.observed"
{
  cat "$BASELINE" 2>/dev/null
  echo "# measured in /home/someone/checkout"
} >"$MUT2"
pc "a baseline containing a home path" "$MUT2" 'home|path|leak'
MUT3="$SCRATCH/baseline-future.observed"
sed "s/^recorded=.*/recorded=$(date -d '+400 days' +%Y-%m-%d)/" "$BASELINE" >"$MUT3" 2>/dev/null
pc "a baseline dated in the future" "$MUT3" 'future|date'

echo "=== [10] interruption is reported, and the step's whole tree is reaped"
F10="$SCRATCH/f10"
new_fixture "$F10"
# `kill -0` first, and FAIL rather than sleep if the pid is not there: an unset
# SPRAWL_VALIDATE_DRIVER_PID otherwise burns the whole sleep and reports two
# unattributable failures. The outer `timeout` makes a non-delivered signal fail
# deterministically instead of racing the sleep under fleet load.
#
# The backgrounded sleep is the LEAK PROBE. Before the driver ran each step in
# its own process group, TERM reached only the driver's direct child, so this
# grandchild survived — and because it inherited the command-substitution pipe,
# DRIVE_OUT below blocked for its full lifetime. That was ~27s of every single
# `make validate`, and the "exits 143" leg alone was perfectly happy with it.
append_step "$F10" victim \
  'test -n "${SPRAWL_VALIDATE_DRIVER_PID:-}" || { echo NO_DRIVER_PID; exit 66; }' \
  'kill -0 "$SPRAWL_VALIDATE_DRIVER_PID" || { echo PID_NOT_LIVE; exit 67; }' \
  'sleep 3007 &' \
  'echo "VICTIM_CHILD=$!"' \
  'kill -0 "$!" && echo VICTIM_CHILD_LIVE' \
  'echo KILL_TARGET_LIVE' \
  'kill -TERM "$SPRAWL_VALIDATE_DRIVER_PID"' \
  'wait'
DRIVE_OUT=$(timeout 20 env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL \
  SPRAWL_VALIDATE_MAKE_F="$F10/Makefile" SPRAWL_VALIDATE_MAKE_C="$F10" \
  SPRAWL_VALIDATE_TIMING_OUT="$F10/out" STUB_DIR="$F10/stub" \
  MAKE_CMD="bash $SCRATCH/fakemake" \
  bash "$DRIVER" victim 2>&1)
DRIVE_RC=$?
if printf '%s\n' "$DRIVE_OUT" | grep -q 'KILL_TARGET_LIVE'; then
  ok "the driver exports a live SPRAWL_VALIDATE_DRIVER_PID (the signal had a real target)"
else
  fail "no live driver pid to signal — the legs below would measure nothing: $(printf '%s' "$DRIVE_OUT" | tail -2 | tr '\n' '|')"
fi
if [ "$DRIVE_RC" -eq 143 ]; then
  ok "a TERM'd run exits 143 (128+15), not 0 and not the timeout's 124"
else
  fail "TERM'd run exited $DRIVE_RC, want 143 — an interrupted validate can report success (124 = the driver ignored the signal, or a leaked child held the pipe open)"
fi
if printf '%s\n' "$DRIVE_OUT" | grep -q 'INTERRUPTED during step victim'; then
  ok "the in-flight step is named on interruption (this is the git-commit-timeout diagnosis)"
else
  fail "interruption did not name the in-flight step: $(printf '%s' "$DRIVE_OUT" | tail -3 | tr '\n' '|')"
fi
# The positive control for the leak leg: the grandchild must be shown to have
# EXISTED, or "it is gone now" is a statement about a process that never ran.
VICTIM_PID=$(printf '%s\n' "$DRIVE_OUT" | sed -n 's/^VICTIM_CHILD=\([0-9]*\)$/\1/p' | head -1)
if [ -n "$VICTIM_PID" ] && printf '%s\n' "$DRIVE_OUT" | grep -q 'VICTIM_CHILD_LIVE'; then
  ok "positive control: the step's grandchild (pid $VICTIM_PID) was observed alive before the interrupt"
else
  fail "no live grandchild was ever observed (pid='${VICTIM_PID:-<none>}') — the reap leg below would pass vacuously"
fi
# Bounded wait: reaping is a signal delivery, not an atomic act.
REAPED=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if [ -z "$VICTIM_PID" ] || ! kill -0 "$VICTIM_PID" 2>/dev/null; then
    REAPED=1
    break
  fi
  command sleep 0.5
done
if [ -n "$VICTIM_PID" ] && [ "$REAPED" -eq 1 ]; then
  ok "the interrupted step's whole process tree was reaped — no orphan survives the driver"
else
  fail "the step's grandchild (pid ${VICTIM_PID:-<none>}) SURVIVED the driver — an interrupted validate leaks the in-flight step's process tree (e.g. a whole go test run), and the leak also stalls this suite"
  kill -TERM "$VICTIM_PID" 2>/dev/null || true
fi

echo "=== [11] the machine-readable artifact is never partial"
F11="$SCRATCH/f11"
new_fixture "$F11"
append_step "$F11" a-ok 'true'
append_step "$F11" b-bad 'exit 5'
append_step "$F11" c-ok 'true'
drive "$F11" -- a-ok b-bad c-ok
if [ ! -e "$F11/out/baseline.observed" ]; then
  ok "a run that stopped early wrote NO baseline artifact (a partial baseline is a wrong number, not an absent one)"
else
  fail "a failed run wrote $F11/out/baseline.observed with $(grep -c '^step	' "$F11/out/baseline.observed") of 3 steps — promoting that would record a baseline for a gate that was never fully run"
fi
# Refusing to WRITE a partial artifact is only half the property: a promotion
# guard that merely asks "does an artifact exist?" then promotes the previous
# run's file. Measured happening — a warm-cache artifact from an earlier run was
# promoted over a run that had correctly declined to record, which is a stale
# measurement dressed as a current one.
echo "stale=yes" >"$F11/out/baseline.observed"
drive "$F11" -- a-ok b-bad c-ok
if [ ! -e "$F11/out/baseline.observed" ]; then
  ok "a run that stopped early DELETES any previous artifact, so a stale one cannot be promoted in its place"
else
  fail "a previous artifact survived an incomplete run ($(cat "$F11/out/baseline.observed" | head -1)) — a promotion guard would record a measurement from a different run"
fi

F11B="$SCRATCH/f11b"
new_fixture "$F11B"
append_step "$F11B" a-ok 'true'
append_step "$F11B" b-ok 'true'
drive "$F11B" -- a-ok b-ok
if [ -s "$F11B/out/baseline.observed" ] &&
  [ "$(grep -c '^step	' "$F11B/out/baseline.observed")" -eq 2 ]; then
  ok "a complete run DOES write the artifact, with every step (negative control for the leg above)"
else
  fail "a complete run wrote no usable artifact — the leg above would pass for the wrong reason"
fi
# Tolerate mode exists solely so `make validate-baseline` can still record when
# the two SELF-REFERENTIAL baseline steps are failing. It must not become a
# silent-success path: the run continues, and still exits non-zero.
F11C="$SCRATCH/f11c"
new_fixture "$F11C"
append_step "$F11C" t-first 'true'
append_step "$F11C" t-bad 'exit 7'
append_step "$F11C" t-last "touch $F11C/last.sentinel"
drive "$F11C" SPRAWL_VALIDATE_TOLERATE_STEPS=t-bad -- t-first t-bad t-last
if [ -e "$F11C/last.sentinel" ] && [ -s "$F11C/out/baseline.observed" ]; then
  ok "a TOLERATED step's failure does not stop the run, so a complete artifact is still recorded"
else
  fail "tolerate mode did not continue past the failure: last.sentinel=$([ -e "$F11C/last.sentinel" ] && echo yes || echo no), artifact=$([ -s "$F11C/out/baseline.observed" ] && echo yes || echo no)"
fi
if [ "$DRIVE_RC" -eq 7 ]; then
  ok "tolerate mode still exits non-zero with the tolerated step's code (7) — it defers the stop, it does not forgive the failure"
else
  fail "tolerate mode exited $DRIVE_RC, want 7 — tolerating a step turned a real failure green, which is the exact class this whole suite exists to stop"
fi

TOTAL=$((PASSES + FAILURES))
echo
echo "=== validate-timing unit results: $PASSES passed / $FAILURES failed ==="
if [ "$TOTAL" -ne "$MIN_ASSERTIONS" ]; then
  echo "  FAIL: $TOTAL assertions ran, expected exactly $MIN_ASSERTIONS — a leg died early or was added without bumping the gate, so this run measured something other than it claims" >&2
  exit 1
fi
if [ "$FAILURES" -gt 0 ]; then
  exit 1
fi
exit 0
