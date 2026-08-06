#!/usr/bin/env bash
#
# test-gitignore-classes.sh (QUM-989)
#
# Guards the leak-safety ignore patterns in .gitignore: terraform plan output
# and the broad *.log rule. The regression this exists for is SILENT — someone
# tidies .gitignore, a pattern stops matching, nothing fails, and the next
# symptom is an infrastructure artifact staged into a PUBLIC repo. Reading the
# ignore file cannot detect that; only staging real fixtures can.
#
# Runs in `make validate`. Pure shell + git, no claude/tmux, no new dependency.
#
# Every assertion demonstrates it CAN fail ON EVERY RUN (QUM-953), via three
# scratch cases rather than a transcript nobody re-reads:
#
#   LIVE     — the repo's current .gitignore. Hazards must be ignored, keeps
#              must stage.
#   CONTROL  — the same file with the QUM-989 pattern lines DELETED. Every
#              hazard must stage. If it doesn't, the patterns under test are
#              not what is doing the work and the LIVE result is meaningless.
#              (A `git show HEAD:.gitignore` control would be one-shot: after
#              this lands, HEAD *is* the live file and the control degenerates
#              into comparing a file to itself, silently green forever.)
#   NONEG    — the same file with the negation line DELETED. The one tracked
#              *.log must flip to ignored, proving the negation is load-bearing
#              and correctly ordered after `*.log`.
#   OVERBROAD— the same file with each directory pattern replaced by the
#              substring "simplification" someone will eventually reach for
#              (`findings/` -> `*findings*`, `scratch/` -> `*scratch*`). Every
#              precision fixture must flip to ignored. CONTROL only ever
#              DELETES patterns, so it gives teeth to the hazard assertions
#              only — the precision assertions pass identically in LIVE and
#              CONTROL and would otherwise be falsifiable only in a transcript
#              nobody re-reads, which is the exact thing this header rejects.
#
# Fixtures are stat'd on creation: a fixture that never materialised produces
# the same green as a real pass (QUM-989 comment thread; QUM-953).
set -euo pipefail
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE

REPO_ROOT=$(git rev-parse --show-toplevel)
TMPBASE=${TMPDIR:-/tmp}
SCRATCH=$(mktemp -d "$TMPBASE/sprawl-gitignore-test.XXXXXX")
ASSERTIONS=0
FAILURES=0

cleanup() {
  # /tmp hygiene: assert the path prefix before ANY delete. Never rm a glob.
  case "$SCRATCH" in
    "$TMPBASE"/sprawl-gitignore-test.*) rm -rf "$SCRATCH" ;;
    *) echo "REFUSING to delete unexpected path: $SCRATCH" >&2 ;;
  esac
}
trap cleanup EXIT

ok()   { ASSERTIONS=$((ASSERTIONS+1)); echo "  PASS: $1"; }
fail() { ASSERTIONS=$((ASSERTIONS+1)); FAILURES=$((FAILURES+1)); echo "  FAIL: $1" >&2; }

# Scratch repos must not inherit ~/.gitignore_global or system config, or the
# verdict depends on whose machine ran it.
git_scratch() { git -c core.excludesFile=/dev/null "$@"; }

# The pattern lines under test. Deleting exactly these must break the LIVE
# result — that is what makes CONTROL a real control rather than a formality.
GUARDED_PATTERNS=(
  'tfplan*'
  '*.tfplan'
  'plan.out'
  '*.log'
  'findings/'
  'scratch/'
)
# The single intentionally-tracked log, negated back in after the broad *.log.
NEGATED='docs/research/m13-phase1-evidence/ec6-live-handoff-stderr.log'

# Artifacts that must never be stageable. Root AND nested twins: terraform runs
# from deploy/hub/infra/terraform/azure/, so a root-anchored pattern would pass
# a root-only corpus while leaking the realistic case.
HAZARDS=(
  tfplan
  tfplan5
  terraform.tfplan
  plan.out
  acrbuild2.log
  deploy/hub/infra/terraform/azure/tfplan
  deploy/hub/infra/terraform/azure/tfplan5
  deploy/hub/infra/terraform/azure/terraform.tfplan
  deploy/hub/infra/terraform/azure/plan.out
  deploy/hub/infra/terraform/azure/apply4.log
  nested/deep/build.log
  # `findings/` is UNANCHORED, so it must bite at every depth — agents work in
  # worktrees and drop a findings/ dir wherever they happen to be. The nested
  # fixture is the one that matters: an anchored `/findings/` would pass a
  # root-only corpus while leaving the realistic case wide open.
  findings/notes.md
  sub/findings/x.sh
  # `scratch/` is UNANCHORED for the same reason as `findings/`: agents in the
  # merge-safety series each drop a 404-line Go harness at scratch/mergeharness/
  # in whatever worktree they are standing in. Root-and-nested twins, so an
  # anchored `/scratch/` cannot pass on the root fixture alone.
  scratch/mergeharness/main.go
  sub/scratch/mergeharness/main.go
)
# Paths that must remain stageable. Most are real tracked files an over-broad
# pattern (`*log*`, `*plan*`, `*.out`) would silently swallow — the cost side of
# the broad-*.log tradeoff. Two are synthetic precision fixtures, marked below.
KEEPS=(
  "$NEGATED"
  CHANGELOG.md
  cmd/logs.go
  internal/memory/sessionlog.go
  docs/designs/hub/13-implementation-plan.md
  deploy/hub/spike/logs/.gitkeep
  deploy/hub/infra/terraform/azure/.terraform.lock.hcl
  # Real, and specifically at risk: merged onto the integration branch at the
  # same time as `findings/`. Pins that the QUM-991 decision dir is unaffected.
  docs/designs/qum-991-foreign-content-guard/decision.md
  # SYNTHETIC precision fixture (not tracked): `findings/` is a DIRECTORY
  # pattern, so a mere substring must not match. Guards against someone
  # "simplifying" it to `*findings*`.
  docs/findings-summary.md
  # SYNTHETIC precision fixtures (not tracked): `scratch/` is a DIRECTORY
  # pattern matched against a WHOLE path component, so it must not bite a mere
  # substring (docs/scratchpad.md) nor a component that merely starts with it
  # (scratchpad/). Those are two different over-match directions — `*scratch*`
  # and `scratch*/` respectively — so both need a fixture. Note the repo also
  # carries a separate literal `az-scratch/` rule, so az-scratch/ is NOT a
  # usable keep fixture even though `scratch/` alone would leave it stageable.
  docs/scratchpad.md
  scratchpad/f.txt
)
# The KEEPS that exist purely to pin over-match precision. Every one of these
# must flip to ignored under OVERBROAD ([6]) — that is what makes their LIVE
# assertions falsifiable on every run rather than in a transcript.
#
# Every entry MUST also appear in KEEPS: stage_against only materialises
# HAZARDS + KEEPS, and [6] asserts ABSENCE from the staged set, so an entry
# missing from KEEPS is absent because it was never created and [6] reports
# PASS while testing nothing. Enforced by the membership drift guard at the
# bottom of this file, not by convention — the count literals cannot catch it
# (a swap keeps both counts intact).
PRECISION_FIXTURES=(
  docs/findings-summary.md
  docs/scratchpad.md
  scratchpad/f.txt
)

# Stage every fixture against a given .gitignore; echo the sorted staged set.
# `git add -A` is used deliberately — it is the hazard under test — but only
# ever inside a throwaway scratch repo, never in a real worktree.
stage_against() { # $1=gitignore file  $2=case name
  local gi="$1" name="$2" r="$SCRATCH/$2" f
  mkdir -p "$r"
  git_scratch -C "$r" init -q
  cp "$gi" "$r/.gitignore"
  for f in "${HAZARDS[@]}" "${KEEPS[@]}"; do
    mkdir -p "$r/$(dirname "$f")"
    printf 'fixture\n' > "$r/$f"
    stat "$r/$f" >/dev/null || { echo "fixture never materialised: $f" >&2; exit 1; }
  done
  git_scratch -C "$r" add -A
  git_scratch -C "$r" diff --cached --name-only | grep -v '^\.gitignore$' | sort || true
}

LIVE_GI="$SCRATCH/live.gitignore"
CONTROL_GI="$SCRATCH/control.gitignore"
NONEG_GI="$SCRATCH/noneg.gitignore"
OVERBROAD_GI="$SCRATCH/overbroad.gitignore"
cp "$REPO_ROOT/.gitignore" "$LIVE_GI"

# Build CONTROL by deleting the guarded pattern lines (exact-line match).
cp "$LIVE_GI" "$CONTROL_GI"
for p in "${GUARDED_PATTERNS[@]}"; do
  grep -vxF "$p" "$CONTROL_GI" > "$SCRATCH/t" && mv "$SCRATCH/t" "$CONTROL_GI"
done
grep -vxF "!$NEGATED" "$LIVE_GI" > "$NONEG_GI"

# Build OVERBROAD by replacing each directory pattern with its substring
# "simplification". Exact whole-line match via awk, not sed: the patterns
# contain `/` and the replacements contain `*`, both of which need escaping in
# a sed s/// and neither of which needs any in an awk string compare.
cp "$LIVE_GI" "$OVERBROAD_GI"
for p in findings scratch; do
  awk -v from="$p/" -v to="*$p*" '$0==from{print to; next}{print}' \
    "$OVERBROAD_GI" > "$SCRATCH/t" && mv "$SCRATCH/t" "$OVERBROAD_GI"
done

echo "=== [0] the patterns under test are present, and stripping them changes the file"
for p in "${GUARDED_PATTERNS[@]}"; do
  if grep -qxF "$p" "$LIVE_GI"; then
    ok ".gitignore contains $p"
  else
    fail ".gitignore is missing $p — leak-safety pattern was removed"
  fi
done
if grep -qxF "!$NEGATED" "$LIVE_GI"; then
  ok ".gitignore contains the negation for $NEGATED"
else
  fail ".gitignore is missing the negation for $NEGATED"
fi
if ! cmp -s "$LIVE_GI" "$CONTROL_GI"; then
  ok "control differs from live (the negative control has teeth)"
else
  fail "control is identical to live — nothing was stripped, [2] would be vacuous"
fi

echo "=== [1] LIVE: every artifact class is ignored, every tracked path still stages"
LIVE=$(stage_against "$LIVE_GI" live)
WANT=$(printf '%s\n' "${KEEPS[@]}" | sort)
if [ "$LIVE" = "$WANT" ]; then
  ok "staged set is exactly the ${#KEEPS[@]} keep fixtures, zero hazards"
else
  fail "staged set differs from the keep set"
  diff <(echo "$WANT") <(echo "$LIVE") || true
fi
for f in "${HAZARDS[@]}"; do
  if grep -qxF "$f" <<<"$LIVE"; then fail "STAGED hazard $f"; else ok "ignores $f"; fi
done
for f in "${KEEPS[@]}"; do
  if grep -qxF "$f" <<<"$LIVE"; then ok "still stages $f"; else fail "WRONGLY ignores $f"; fi
done

echo "=== [2] CONTROL (patterns stripped): every hazard must stage"
CONTROL=$(stage_against "$CONTROL_GI" control)
if [ -n "$CONTROL" ]; then
  ok "control produced output (the harness is functioning)"
else
  fail "control produced NO output — harness broken, [1] is vacuous"
fi
for f in "${HAZARDS[@]}"; do
  if grep -qxF "$f" <<<"$CONTROL"; then
    ok "control stages $f (the pattern is what ignores it)"
  else
    fail "control did NOT stage $f — something OTHER than the tested pattern ignores it"
  fi
done

echo "=== [3] NONEG (negation stripped): the tracked log must flip to ignored"
NONEG=$(stage_against "$NONEG_GI" noneg)
# The negation assertion below is a NEGATIVE one — it passes when $NEGATED is
# absent from the staged set. An empty scan satisfies that trivially, and
# stage_against ends in `|| true`, so without this guard a totally broken scan
# reports PASS. Same guard as the ones in [2] and [6]; this section was missed.
if [ -n "$NONEG" ]; then
  ok "noneg produced output (the harness is functioning)"
else
  fail "noneg produced NO output — harness broken, the negation check below is vacuous"
fi
if grep -qxF "$NEGATED" <<<"$NONEG"; then
  fail "negation removal changed nothing — the negation is not load-bearing or is misordered"
else
  ok "without the negation, $NEGATED is ignored (negation is load-bearing)"
fi

echo "=== [4] no tracked file in the real repo is ignored by these patterns"
# Scoped deliberately to the QUM-989 patterns rather than a whole-tree
# ls-files -i -c baseline: that baseline has pre-existing entries and also
# consults ~/.gitignore_global, so it is machine-dependent and would send the
# next person to edit the expectation instead of investigating.
# --non-matching makes check-ignore emit a record for EVERY input path, which
# lets the record count be asserted. Without that, a broken pipeline (bad -C,
# an older git, a missing flag) yields empty output that is indistinguishable
# from a clean tree, and this section reports PASS having scanned nothing.
# Fields are NUL-separated (source, linenum, pattern, pathname), so `xargs -0
# -n4` reassembles them into one tab-delimited line per path. Deliberately NOT
# `tr '\0' '\t'`: -z records contain no newlines, so that collapses the whole
# scan into a single line and any failure output buries the real offender.
# --no-index is REQUIRED — check-ignore silently SKIPS paths in the index, and
# every path here is tracked by construction.
N_TRACKED=$(git_scratch -C "$REPO_ROOT" ls-files | grep -c . || true)
RECORDS=$(git_scratch -C "$REPO_ROOT" ls-files -z \
  | git_scratch -C "$REPO_ROOT" check-ignore -z --stdin --no-index -v --non-matching \
  | xargs -0 -n4 printf '%s\t%s\t%s\t%s\n')
N_RECORDS=$(printf '%s' "$RECORDS" | grep -c . || true)
if [ "$N_RECORDS" -eq "$N_TRACKED" ]; then
  ok "check-ignore scanned all $N_TRACKED tracked paths (the scan actually ran)"
else
  fail "check-ignore emitted $N_RECORDS records for $N_TRACKED tracked paths — scan did not run"
fi

# Compare the matched PATTERN field exactly against the guarded list. Matching
# on the field rather than regexing the concatenated blob means an unrelated
# future pattern (e.g. `**/logs/`) cannot produce a spurious hit, and it needs
# no `*`-escaping — the previous `${GUARDED_PATTERNS[*]//\*/\\*}` left `.` as a
# regex wildcard, so `\*.log` also matched `**/logs/`.
SWALLOWED=$(printf '%s\n' "$RECORDS" | while IFS=$'\t' read -r _src _ln pat path; do
  if printf '%s\n' "${GUARDED_PATTERNS[@]}" | grep -qxF -- "$pat"; then
    printf '%s\t%s\n' "$pat" "$path"
  fi
done)
if [ -z "$SWALLOWED" ]; then
  ok "no tracked file is ignored by the QUM-989 patterns"
else
  fail "tracked files are ignored by the QUM-989 patterns (pattern -> path)"
  echo "$SWALLOWED"
fi

echo "=== [5] direct check-ignore probes — the unanchored findings/ and scratch/ semantics"
# --no-index on EVERY probe: check-ignore silently skips paths present in the
# index, so without it a probe over a tracked path can never report "ignored"
# and is structurally incapable of failing. None of the probe paths below are
# tracked today, but the flag must not depend on that staying true.
for p in findings/notes.md sub/findings/x.sh \
         scratch/mergeharness/main.go sub/scratch/mergeharness/main.go; do
  if git_scratch -C "$REPO_ROOT" check-ignore -q --no-index "$p"; then
    ok "check-ignore: $p IS ignored"
  else
    fail "check-ignore: $p is NOT ignored — the pattern guarding it (findings/ or scratch/) is missing or wrongly anchored"
  fi
done
# Precision: these are DIRECTORY patterns and must not match a mere substring.
for p in "${PRECISION_FIXTURES[@]}"; do
  if git_scratch -C "$REPO_ROOT" check-ignore -q --no-index "$p"; then
    fail "check-ignore: $p is ignored — a directory pattern is over-matching"
  else
    ok "check-ignore: $p is NOT ignored (directory pattern, not substring)"
  fi
done

echo "=== [6] OVERBROAD (directory patterns widened to substrings): precision fixtures must flip"
if ! cmp -s "$LIVE_GI" "$OVERBROAD_GI"; then
  ok "overbroad differs from live (the widening actually applied)"
else
  fail "overbroad is identical to live — nothing was widened, the checks below are vacuous"
fi
OVERBROAD=$(stage_against "$OVERBROAD_GI" overbroad)
# Same guard as [2] and [3]: the assertions below are NEGATIVE (they pass when
# a path is ABSENT from the staged set), and stage_against ends in `|| true`,
# so a totally broken scan would report PASS without this.
if [ -n "$OVERBROAD" ]; then
  ok "overbroad produced output (the harness is functioning)"
else
  fail "overbroad produced NO output — harness broken, the precision checks below are vacuous"
fi
for f in "${PRECISION_FIXTURES[@]}"; do
  if grep -qxF "$f" <<<"$OVERBROAD"; then
    fail "overbroad still stages $f — it is not a precision fixture; the LIVE assertion for it cannot fail"
  else
    ok "overbroad ignores $f (the LIVE precision assertion for it has teeth)"
  fi
done

# Assertion-count floor. Hardcoded literals, NOT derived from the arrays: a
# floor computed from ${#HAZARDS[@]} stays green with an empty corpus, which is
# exactly the false-green this floor exists to prevent.
[ "${#GUARDED_PATTERNS[@]}" -eq 6  ] || { echo "CORPUS DRIFT: ${#GUARDED_PATTERNS[@]} patterns, expected 6" >&2; exit 1; }
[ "${#HAZARDS[@]}"          -eq 15 ] || { echo "CORPUS DRIFT: ${#HAZARDS[@]} hazards, expected 15" >&2; exit 1; }
[ "${#KEEPS[@]}"            -eq 11 ] || { echo "CORPUS DRIFT: ${#KEEPS[@]} keeps, expected 11" >&2; exit 1; }
[ "${#PRECISION_FIXTURES[@]}" -eq 3 ] || { echo "CORPUS DRIFT: ${#PRECISION_FIXTURES[@]} precision fixtures, expected 3" >&2; exit 1; }
# Membership, not just counts: [6] iterates PRECISION_FIXTURES but stage_against
# only materialises HAZARDS + KEEPS, so a precision fixture missing from KEEPS is
# absent from the staged set by construction and [6] passes vacuously (watched:
# 66 assertions, 0 failures, exit 0, with the fixture deleted from KEEPS). A hard
# exit like the drift literals above, so it does not move the floor.
for f in "${PRECISION_FIXTURES[@]}"; do
  printf '%s\n' "${KEEPS[@]}" | grep -qxF -- "$f" \
    || { echo "CORPUS DRIFT: precision fixture $f is not in KEEPS — [6] would pass vacuously" >&2; exit 1; }
done
echo "=== $ASSERTIONS assertions, $FAILURES failures ==="
# 67 = [0] 8 + [1] 27 + [2] 16 + [3] 2 + [4] 2 + [5] 7 + [6] 5. Hand-computed.
[ "$ASSERTIONS" -eq 67 ] || { echo "FLOOR BREACH: $ASSERTIONS assertions ran, expected 67" >&2; exit 1; }
[ "$FAILURES" -eq 0 ] || exit 1
echo "gitignore-classes: OK"
