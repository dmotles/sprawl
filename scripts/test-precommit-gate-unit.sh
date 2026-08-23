#!/usr/bin/env bash
#
# test-precommit-gate-unit.sh (QUM-1289)
#
# Guards which gate scripts/pre-commit runs, and — the part with real blast
# radius — its fallback when `check` does not exist.
#
# WHY THE FALLBACK IS NOT OPTIONAL. Both install paths symlink ONE file:
# `make hooks` does .git/hooks/pre-commit -> <main checkout>/scripts/pre-commit,
# and .sprawl/config.yaml's worktree.setup does the same into the shared
# git-common-dir hooks. So the hook every agent runs is MAIN's copy, not their
# own branch's. The moment this change reaches main, every existing worktree on a
# pre-QUM-1289 branch would run `make check` against a Makefile that has no such
# target: `No rule to make target 'check'`, exit 2, EVERY COMMIT BLOCKED, for
# every agent, until they rebase. There were live worktrees in exactly that state
# when this landed.
#
# So pre-commit probes for the target and falls back to `make validate`. The
# fallback direction matters: it fails toward MORE checking, never less.
#
# AND THE LOUD ARM MUST NOT READ AS SUCCESS (forge's condition). A fallback that
# quietly ran a different gate than the one it announced would be the same
# false-green class this repo keeps finding. So the fallback is asserted to say
# what it did, why, and that it is the slower-not-weaker gate.
#
# Pure shell. No claude, no tmux, no go.

set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
HOOK=$REPO_ROOT/scripts/pre-commit

MIN_ASSERTIONS=12

TMPBASE=${TMPDIR:-/tmp}
SCRATCH=$(mktemp -d "$TMPBASE/sprawl-precommit-gate.XXXXXX") || {
  echo "FATAL: mktemp -d failed under $TMPBASE" >&2; exit 1; }
case "$SCRATCH" in
  /*) ;;
  *) echo "FATAL: mktemp returned a non-absolute path: '$SCRATCH'" >&2; exit 1 ;;
esac
cleanup() {
  case "$SCRATCH" in
    "$TMPBASE"/sprawl-precommit-gate.*) rm -rf "$SCRATCH" ;;
    *) echo "WARN: refusing to remove unexpected SCRATCH '$SCRATCH'" >&2 ;;
  esac
}
trap cleanup EXIT

PASSES=0
FAILURES=0
pass() { PASSES=$((PASSES + 1)); echo "  PASS: $1"; }
fail() { FAILURES=$((FAILURES + 1)); echo "  FAIL: $1" >&2; }

echo "=== pre-commit gate-selection gate (QUM-1289) ==="

# A fake `make` on PATH that records its goals and can be told to fail the probe.
# The guards are stubbed too: this suite is about GATE SELECTION, not about the
# guards, which have their own coverage.
mkbin() {
  mkdir -p "$SCRATCH/bin"
  cat > "$SCRATCH/bin/make" <<'FAKE'
#!/usr/bin/env bash
echo "FAKEMAKE-GOALS: $*" >> "$FAKE_LOG"
for a in "$@"; do
  case "$a" in
    print-check-steps) [ "${FAKE_HAS_CHECK:-1}" = 1 ] && exit 0 || exit 2 ;;
    print-validate-steps) [ "${FAKE_HAS_VALIDATE:-1}" = 1 ] && exit 0 || exit 2 ;;
  esac
done
exit 0
FAKE
  chmod +x "$SCRATCH/bin/make"
  for g in guard-main-commit guard-employer-leak; do
    printf '#!/usr/bin/env bash\nexit 0\n' > "$SCRATCH/bin/$g"
    chmod +x "$SCRATCH/bin/$g"
  done
}
mkbin

# The hook resolves its guards relative to its own real path, so run a copy that
# sits next to the stubs.
cp "$HOOK" "$SCRATCH/bin/pre-commit"
chmod +x "$SCRATCH/bin/pre-commit"

run_hook() {
  FAKE_LOG=$SCRATCH/log
  : > "$FAKE_LOG"
  ( cd "$REPO_ROOT" && FAKE_LOG="$FAKE_LOG" FAKE_HAS_CHECK="$1" FAKE_HAS_VALIDATE="$2" \
      PATH="$SCRATCH/bin:$PATH" bash "$SCRATCH/bin/pre-commit" 2>&1 )
}

# --- [1] happy path: check exists -> run check, no fallback noise --------
out=$(run_hook 1 1); rc=$?
goals=$(grep -h 'FAKEMAKE-GOALS' "$SCRATCH/log" | tail -1)
if [ "$rc" -eq 0 ]; then pass "[1] hook exits 0 when the gate passes"
else fail "[1] hook exited $rc on the happy path"; fi
case "$goals" in
  *check*) pass "[1] the hook invoked 'make check' when the target exists" ;;
  *) fail "[1] the hook did not invoke 'make check'; last goals were: $goals" ;;
esac
case "$goals" in
  *validate*) fail "[1] the hook ran 'validate' even though 'check' exists — the split does nothing" ;;
  *) pass "[1] the hook did NOT run the full validate when check was available" ;;
esac
case "$out" in
  *"falling back"*) fail "[1] the hook printed a fallback notice on the happy path — that reads as a problem where there is none" ;;
  *) pass "[1] no spurious fallback notice on the happy path" ;;
esac

# --- [2] the fallback: check absent -> run validate, LOUDLY --------------
out=$(run_hook 0 1); rc=$?
goals=$(grep -h 'FAKEMAKE-GOALS' "$SCRATCH/log")
if [ "$rc" -eq 0 ]; then pass "[2] the fallback path still exits 0 when the gate passes"
else fail "[2] the fallback path exited $rc"; fi
if printf '%s\n' "$goals" | grep -q 'validate'; then
  pass "[2] with 'check' absent the hook fell back to 'make validate'"
else
  fail "[2] with 'check' absent the hook did NOT run validate — a commit would be ungated. Goals: $goals"
fi
if printf '%s\n' "$goals" | grep -qE 'GOALS: check$|GOALS: .* check$'; then
  fail "[2] the hook still tried to run 'check' after the probe said it does not exist"
else
  pass "[2] the hook did not attempt the missing 'check' target"
fi

# The loud arm must be unmistakable, and must not read as success.
loud=0
case "$out" in *"falling back"*) loud=$((loud+1)) ;; esac
case "$out" in *"QUM-1289"*|*"pre-QUM-1289"*) loud=$((loud+1)) ;; esac
case "$out" in *"Slower, not weaker"*|*"slower, not weaker"*) loud=$((loud+1)) ;; esac
if [ "$loud" -eq 3 ]; then
  pass "[2] the fallback is LOUD: says it fell back, why, and that it is slower-not-weaker"
else
  fail "[2] the fallback notice is not loud enough ($loud of 3 markers) — a fallback that quietly runs a different gate than it announced is the false-green class this repo keeps finding"
fi

# --- [3] no gate at all -> the commit must still be REFUSED -------------
# The hook does not probe for `validate`: if it is missing, `make validate`
# itself fails and the hook exits non-zero. What matters is the OUTCOME — a
# commit is never allowed through with nothing having gated it — so that is what
# this asserts, rather than asserting a particular message.
run_hook_nomake() {
  FAKE_LOG=$SCRATCH/log
  : > "$FAKE_LOG"
  cat > "$SCRATCH/bin/make" <<'NOGATE'
#!/usr/bin/env bash
echo "FAKEMAKE-GOALS: $*" >> "$FAKE_LOG"
for a in "$@"; do
  case "$a" in print-check-steps) exit 2 ;; esac
done
echo "make: *** No rule to make target 'validate'.  Stop." >&2
exit 2
NOGATE
  chmod +x "$SCRATCH/bin/make"
  ( cd "$REPO_ROOT" && FAKE_LOG="$FAKE_LOG" PATH="$SCRATCH/bin:$PATH" \
      bash "$SCRATCH/bin/pre-commit" 2>&1 )
}
out=$(run_hook_nomake); rc=$?
if [ "$rc" -ne 0 ]; then
  pass "[3] with no usable gate the hook exits non-zero, so the commit is refused (rc=$rc)"
else
  fail "[3] with no usable gate the hook exited 0 — that is a commit with nothing having gated it, the worst possible fallback"
fi
case "$out" in
  *"No rule to make target"*|*"refusing"*|*"REFUSING"*)
    pass "[3] the refusal is not silent — the reason reaches the operator" ;;
  *) fail "[3] the hook refused silently, so an operator cannot tell why the commit was blocked" ;;
esac
mkbin
cp "$HOOK" "$SCRATCH/bin/pre-commit"
chmod +x "$SCRATCH/bin/pre-commit"

# --- [4] ordering: the scope must be captured BEFORE the GIT_* unset -----
# A structural assertion on the script text, because the defect it guards is
# invisible at runtime on a plain `git commit`: under a PARTIAL commit git points
# GIT_INDEX_FILE at a temporary index, so a scope computed after the unset reads
# the wrong tree and silently tests the wrong packages.
scope_line=$(grep -n 'SPRAWL_CHECK_SCOPE_PATHS="\$(' "$HOOK" | head -1 | cut -d: -f1)
unset_line=$(grep -n '^unset GIT_DIR' "$HOOK" | head -1 | cut -d: -f1)
if [ -n "$scope_line" ] && [ -n "$unset_line" ]; then
  if [ "$scope_line" -lt "$unset_line" ]; then
    pass "[4] the scope paths are captured (line $scope_line) BEFORE the GIT_* unset (line $unset_line)"
  else
    fail "[4] the scope paths are captured AFTER the GIT_* unset — under a partial commit that reads the WRONG index, so the gate would test the wrong packages. This works fine on a plain 'git commit', which is how it survives review."
  fi
else
  fail "[4] could not locate both the scope capture and the GIT_* unset in $HOOK (scope='$scope_line' unset='$unset_line')"
fi

# --- [5] the two commit guards still run, and still run FIRST ------------
guard_line=$(grep -n 'guard-main-commit' "$HOOK" | head -1 | cut -d: -f1)
gate_line=$(grep -n '^make "\$gate"' "$HOOK" | head -1 | cut -d: -f1)
if [ -n "$guard_line" ] && [ -n "$gate_line" ] && [ "$guard_line" -lt "$gate_line" ]; then
  pass "[5] guard-main-commit still runs (line $guard_line), before the gate (line $gate_line)"
else
  fail "[5] guard-main-commit no longer runs before the gate — QUM-808's main-commit guard must precede anything that could look green"
fi

echo "=== Results: $PASSES passed, $FAILURES failed ==="
observed=$((PASSES + FAILURES))
if [ "$observed" -lt "$MIN_ASSERTIONS" ]; then
  echo "  FAIL: only $observed assertion(s) ran but MIN_ASSERTIONS=$MIN_ASSERTIONS — this gate measured less than it claims (QUM-1029)" >&2
  exit 1
fi
[ "$FAILURES" -eq 0 ] || exit 1
exit 0
