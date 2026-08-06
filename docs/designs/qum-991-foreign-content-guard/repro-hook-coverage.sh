#!/usr/bin/env bash
#
# Usage:  SCRIPTS=/abs/path/to/sprawl/scripts bash repro-hook-coverage.sh
#
# QUM-991 AC-4 harness: independently validate the three-hook coverage table,
# and validate the reference-transaction feasibility claims for a NEW content
# guard (can a `prepared`-phase hook see the commit's tree?).
#
# Exit-code semantics (unlike repro-binary-blindness.sh, these ARE conventional):
# every assertion here expects the guard stack's DOCUMENTED behaviour, so 0 =
# all 23 assertions held, 1 = a real discrepancy, 4 = the harness did not run the
# expected number of assertions (count mismatch, in either direction).
#
# Every section contributes the same number of assertions on every branch, so a
# discrepancy always exits 1 and can never masquerade as a count shortfall. Note
# this holds only while `ok`/`no` return 0 — see the note on those functions.
#
# /tmp hygiene: mktemp -d root, removed only behind a literal-prefix `case`
# guard. No rm globs.
set -uo pipefail

SCRIPTS="${SCRIPTS:?SCRIPTS=/abs/path/to/scripts required}"
TMP_ROOT="$(mktemp -d /tmp/qum991-hooks.XXXXXX)"
cleanup() {
	case "$TMP_ROOT" in
	/tmp/qum991-hooks.*) rm -rf "$TMP_ROOT" ;;
	*) echo "REFUSING to clean unexpected path '$TMP_ROOT'" >&2 ;;
	esac
}
trap cleanup EXIT

PASSES=0 FAILS=0 ASSERTIONS=0
# Both MUST return 0. Most sites here are `cond && ok "..." || no "..."`, where a
# nonzero `ok` would run the `no` arm too and double-count: appending `return 1` to
# `ok` yields 23 ok / 13 FAIL / 36 assertions. They return 0 today because each
# ends in an arithmetic assignment, which is not obvious — so do not "modernise"
# the trailing statement to `((VAR++))`, which returns 1 when the pre-increment
# value is 0. The exact count check at the bottom is the backstop.
ok() {
	printf '  ok   %s\n' "$1"
	PASSES=$((PASSES + 1))
	ASSERTIONS=$((ASSERTIONS + 1))
}
no() {
	printf '  FAIL %s\n' "$1"
	FAILS=$((FAILS + 1))
	ASSERTIONS=$((ASSERTIONS + 1))
}
note() { printf '\n=== %s\n' "$*"; }

mkrepo() { # $1=name -> echoes path
	local r="$TMP_ROOT/$1"
	mkdir -p "$r"
	git -C "$r" init -q -b main
	git -C "$r" config user.email probe@example.invalid
	git -C "$r" config user.name probe
	echo seed >"$r/seed.txt"
	git -C "$r" add seed.txt
	git -C "$r" -c core.hooksPath=/dev/null commit -qm seed --no-verify 2>/dev/null ||
		git -C "$r" commit -qm seed --no-verify
	echo "$r"
}

# ---------------------------------------------------------------------------
note '1. guard-main-commit (pre-commit): fires on main for a child agent?'
R=$(mkrepo gmc)
ln -sf "$SCRIPTS/guard-main-commit" "$R/.git/hooks/pre-commit"
echo a >"$R/a.txt"
git -C "$R" add a.txt
out=$(SPRAWL_AGENT_IDENTITY=probe git -C "$R" commit -m x 2>&1)
rc=$?
[ "$rc" -ne 0 ] && ok "blocks child-agent commit on main (rc=$rc)" || no "did NOT block (rc=$rc): $out"
case "$out" in *QUM-808*) ok "message cites QUM-808" ;; *) no "message lacks QUM-808: $out" ;; esac

note '1b. guard-main-commit is SKIPPED by --no-verify'
out=$(SPRAWL_AGENT_IDENTITY=probe git -C "$R" commit -m x --no-verify 2>&1)
rc=$?
[ "$rc" -eq 0 ] && ok "--no-verify BYPASSES the pre-commit guard (commit landed, rc=0)" ||
	no "--no-verify did not bypass (rc=$rc): $out"

note '1c. guard-main-commit: weave and empty identity are allowed'
echo b >"$R/b.txt"
git -C "$R" add b.txt
out=$(SPRAWL_AGENT_IDENTITY=weave git -C "$R" commit -m weave 2>&1)
rc=$?
[ "$rc" -eq 0 ] && ok "weave allowed on main (rc=0)" || no "weave blocked (rc=$rc): $out"
echo c >"$R/c.txt"
git -C "$R" add c.txt
out=$(env -u SPRAWL_AGENT_IDENTITY git -C "$R" commit -m human 2>&1)
rc=$?
[ "$rc" -eq 0 ] && ok "unset identity (human) allowed on main (rc=0)" || no "human blocked (rc=$rc): $out"

note '1d. guard-main-commit is content-blind (passes a forbidden binary on a feature branch)'
git -C "$R" checkout -q -b feature
python3 -c "open('$R/x.bin','wb').write(b'\x00ACMEGLOBALCORP\x00'+bytes(range(256)))"
git -C "$R" add x.bin
out=$(SPRAWL_AGENT_IDENTITY=probe git -C "$R" commit -m bin 2>&1)
rc=$?
[ "$rc" -eq 0 ] && ok "content-blind: binary commit on a feature branch passes (rc=0)" ||
	no "unexpectedly blocked (rc=$rc): $out"

# ---------------------------------------------------------------------------
note '2. guard-main-ref (reference-transaction): NOT skippable by --no-verify'
R=$(mkrepo gmr)
ln -sf "$SCRIPTS/guard-main-ref" "$R/.git/hooks/reference-transaction"
echo a >"$R/a.txt"
git -C "$R" add a.txt
# Baseline captured BEFORE the blocked commit — the only value that can prove
# the ref did not move. Must stay above the commit attempt.
before=$(git -C "$R" rev-parse main)
out=$(SPRAWL_AGENT_IDENTITY=probe git -C "$R" commit -m x --no-verify 2>&1)
rc=$?
[ "$rc" -ne 0 ] && ok "blocks child-agent commit to main EVEN WITH --no-verify (rc=$rc)" ||
	no "did NOT block under --no-verify (rc=$rc): $out"
case "$out" in *"ref updates aborted by hook"*) ok "git reports 'ref updates aborted by hook'" ;;
*) no "git did not report ref-update abortion: $out" ;; esac
# Compare the ref AFTER the blocked commit against the one captured BEFORE it.
# Both arms assert, so this section's count is the same either way.
# Previously this was two identical `rev-parse main` calls that were never
# compared, followed by an UNCONDITIONAL `ok` — so it could not fail, and with
# the hook removed it cheerfully printed "did not advance (still <new sha>)"
# quoting the very commit it had advanced to. Comparing those two calls to each
# other would have been tautological; only a pre-commit baseline can catch this.
after=$(git -C "$R" rev-parse main)
if [ "$after" = "$before" ]; then
	ok "main ref did not advance (still $before)"
else
	no "main ref ADVANCED $before -> $after despite the guard"
fi

# NOTE: this row deliberately resets to HEAD — a NO-OP reset, chosen so the row
# isolates "is the ref update rejected?" from any tree movement. That is why it
# prints staged=[] status=[] and looks like the tree was untouched. It is NOT
# evidence that a rejected reset leaves the tree alone: see repro-design-probes.sh
# section 6d, which resets to HEAD~1 and measures the working tree being rewound
# v2 -> v1 while main correctly does not move. Do not read 2b as contradicting 6d.
note '2b. guard-main-ref: blocks reset --hard on main too (no-op reset; see 6d for the clobber)'
out=$(SPRAWL_AGENT_IDENTITY=probe git -C "$R" reset --hard HEAD 2>&1)
rc=$?
[ "$rc" -ne 0 ] && ok "reset --hard on main is rejected (rc=$rc)" || no "reset --hard allowed (rc=$rc)"
printf '  info reset --hard first line: %s\n' "$(printf '%s' "$out" | head -1)"
printf '  info index/worktree AFTER the rejected reset: staged=[%s] status=[%s]\n' \
	"$(git -C "$R" diff --cached --name-only | tr '\n' ' ')" \
	"$(git -C "$R" status --porcelain | tr '\n' ' ')"

note '2c. guard-main-ref: other branches unaffected'
SPRAWL_AGENT_IDENTITY=probe git -C "$R" checkout -q -b feature
echo feat >"$R/feat.txt"
git -C "$R" add feat.txt
out=$(SPRAWL_AGENT_IDENTITY=probe git -C "$R" commit -qm x --no-verify 2>&1)
rc=$?
[ "$rc" -eq 0 ] && ok "commit on a feature branch passes (rc=0)" || no "feature branch blocked (rc=$rc): $out"

note '2d. guard-main-ref is content-blind (binary on feature branch passes)'
python3 -c "open('$R/x.bin','wb').write(b'\x00ACMEGLOBALCORP\x00'+bytes(range(256)))"
git -C "$R" add x.bin
out=$(SPRAWL_AGENT_IDENTITY=probe git -C "$R" commit -m bin --no-verify 2>&1)
rc=$?
[ "$rc" -eq 0 ] && ok "content-blind: binary passes (rc=0)" || no "unexpectedly blocked: $out"

# ---------------------------------------------------------------------------
note '3. guard-employer-leak: skippable by --no-verify'
R=$(mkrepo gel)
TERMS="$TMP_ROOT/terms"
printf 'synthetic-employer:ci:ACMEGLOBALCORP\n' >"$TERMS"
cat >"$R/.git/hooks/pre-commit" <<EOF
#!/usr/bin/env bash
exec "$SCRIPTS/guard-employer-leak"
EOF
chmod +x "$R/.git/hooks/pre-commit"
export SPRAWL_FORBIDDEN_TERMS_FILE="$TERMS"
git -C "$R" checkout -q -b feature
printf 'ACMEGLOBALCORP internal\n' >"$R/leak.txt"
git -C "$R" add leak.txt
out=$(git -C "$R" commit -m leak 2>&1)
rc=$?
[ "$rc" -ne 0 ] && ok "blocks a text leak via pre-commit (rc=$rc)" || no "did not block (rc=$rc): $out"
out=$(git -C "$R" commit -m leak --no-verify 2>&1)
rc=$?
[ "$rc" -eq 0 ] && ok "--no-verify BYPASSES the leak guard (rc=0) — every content layer is skippable" ||
	no "--no-verify did not bypass (rc=$rc): $out"

note '3b. guard-employer-leak: absent list => no-op exit 0'
out=$(cd "$R" && env -u SPRAWL_FORBIDDEN_TERMS_FILE SPRAWL_FORBIDDEN_TERMS_FILE="$TMP_ROOT/nope" "$SCRIPTS/guard-employer-leak" 2>&1)
rc=$?
[ "$rc" -eq 0 ] && ok "absent list => exit 0 no-op" || no "absent list rc=$rc: $out"

# ---------------------------------------------------------------------------
note '4. FEASIBILITY: can a `prepared`-phase reference-transaction hook see the commit tree?'
R=$(mkrepo rtfeas)
cat >"$R/.git/hooks/reference-transaction" <<EOF
#!/usr/bin/env bash
out="$R/rt-probe.log"
EOF
cat >>"$R/.git/hooks/reference-transaction" <<'EOF'
phase="${1:-}"
[ "$phase" = prepared ] || { cat >/dev/null; exit 0; }
while read -r old new ref; do
  echo "PHASE=$phase ref=$ref old=$old new=$new" >>"$out"
  [ "$ref" = "refs/heads/main" ] || continue
  # Does the new commit object already exist and is its tree readable?
  if git cat-file -e "$new" 2>/dev/null; then
    echo "  new-object-exists=yes type=$(git cat-file -t "$new")" >>"$out"
    if [ "$old" = "0000000000000000000000000000000000000000" ]; then
      echo "  names=$(git diff-tree -r --root --name-only --no-commit-id "$new" | tr '\n' ' ')" >>"$out"
      echo "  numstat=$(git diff-tree -r --root --numstat --no-commit-id "$new" | tr '\n\t' '; ')" >>"$out"
    else
      echo "  names=$(git diff-tree -r --name-only --no-commit-id "$old" "$new" | tr '\n' ' ')" >>"$out"
      echo "  numstat=$(git diff-tree -r --numstat --no-commit-id "$old" "$new" | tr '\n\t' '; ')" >>"$out"
    fi
  else
    echo "  new-object-exists=NO" >>"$out"
  fi
  # Can --cached be used instead? (the index question)
  echo "  cached-names=$(git diff --cached --name-only 2>/dev/null | tr '\n' ' ')" >>"$out"
done
exit 0
EOF
chmod +x "$R/.git/hooks/reference-transaction"
echo hello >"$R/text.txt"
python3 -c "open('$R/tfplan.bin','wb').write(b'\x00ACMEGLOBALCORP\x00'+bytes(range(256))*4)"
git -C "$R" add text.txt tfplan.bin
git -C "$R" commit -qm "two files" --no-verify
LOG="$R/rt-probe.log"
if [ -f "$LOG" ]; then
	echo "  --- rt-probe.log ---"
	sed 's/^/  | /' "$LOG"
	grep -q 'new-object-exists=yes' "$LOG" && ok "commit object IS readable in the prepared phase" ||
		no "commit object NOT readable in prepared phase"
	# Anchored to the line start: unanchored 'names=' also matches the
	# 'cached-names=' line below, so this diff-tree assertion could be
	# satisfied entirely by the --cached line — the very substitution §4
	# warns is the trap. Two leading spaces come from the hook's echo.
	if grep -qE '^ +names=.*tfplan\.bin' "$LOG"; then
		ok "git diff-tree in prepared phase lists the new paths"
	else
		no "diff-tree did not list paths: $(grep -E '^ +names=' "$LOG")"
	fi
	# The probe hook renders its output through `tr '\n\t' '; '` — newline->';'
	# and tab->' ' — so a binary numstat row reads ';- - tfplan.bin;'. The
	# previous pattern ('-; -; tfplan.bin') had that mapping transposed and so
	# could never match; because its fallback only printf'd an 'info' line, the
	# permanent miss was invisible and the run still reported 0 FAIL.
	# (.*;)? tolerates either diff-tree ordering of the two paths.
	if grep -qE 'numstat=(.*;)?- - tfplan\.bin;' "$LOG"; then
		ok "diff-tree --numstat reports '-\t-' for the binary (usable signal)"
	else
		no "numstat did not report '- -' for the binary: $(grep numstat "$LOG")"
	fi
	# The index is STILL FULLY POPULATED in the prepared phase — measured, see
	# the rt-probe.log dump above and decision.md §4. The previous assertion
	# ('cached-names=$') claimed the opposite, and its soft-degrade hid the
	# contradiction. That --cached LOOKS usable here is precisely the trap: it
	# is unrelated to the ref being updated for reset/merge/rebase/fetch, so it
	# would pass an implementer's tests and fail in production. Use diff-tree.
	# No '$' anchor: tr leaves a trailing space.
	if grep -qE 'cached-names=text\.txt tfplan\.bin' "$LOG"; then
		ok "the INDEX is ALSO populated at prepared phase (--cached LOOKS usable — the trap; use diff-tree)"
	else
		no "cached-names did not list the staged paths: $(grep cached-names "$LOG")"
	fi
	printf '  info ALL ref lines seen in one commit txn: %s\n' "$(grep -c '^PHASE=' "$LOG")"
else
	# One `no` per assertion the if-branch would have run, so the section
	# contributes 4 either way. Without this the floor (== the maximum) is
	# breached by a REAL discrepancy and the run exits 4 "floor not met"
	# instead of 1 "discrepancy" — misclassifying the very failure this
	# section exists to detect. Keep these four in sync with the branch above.
	no "rt-probe.log was never written — hook did not fire"
	no "diff-tree paths unverifiable — hook did not fire"
	no "numstat binary signal unverifiable — hook did not fire"
	no "index state at prepared phase unverifiable — hook did not fire"
fi

# ---------------------------------------------------------------------------
note '5. core.hooksPath bypass (QUM-951 context): does it disable both hooks?'
R=$(mkrepo hp)
ln -sf "$SCRIPTS/guard-main-commit" "$R/.git/hooks/pre-commit"
ln -sf "$SCRIPTS/guard-main-ref" "$R/.git/hooks/reference-transaction"
HOOKDIR="$TMP_ROOT/emptyhooks"
mkdir -p "$HOOKDIR"
echo z >"$R/z.txt"
git -C "$R" add z.txt

# This section is the LOAD-BEARING measurement of the whole harness: the
# core.hooksPath bypass is the reason the reference-transaction content-guard
# steer was declined (decision.md §2.1/§4). It was a single soft-degrading
# `... && ok "...disables BOTH hooks..." || printf '  info ...'` whose `||` arm
# neither counted nor failed; see 5e17d4c for the full symptom.
#
# It also could not falsify its own wording. "BOTH" was never measured: one
# commit with the override in place cannot distinguish "both hooks silenced"
# from "neither hook was ever live". So the claim is decomposed into two
# positive controls (each guard demonstrably fires in THIS repo) plus the
# bypass, and the override dir's emptiness is asserted rather than assumed — a
# typo'd path is a substitution, not an unset var, so `set -u` cannot catch it,
# and it made the old assertion pass vacuously (measured: rc=0, commit lands).
#
# Forced-degrade recipes, both watched failing before this was watched passing:
#   M1 (fixture premise): ln -sf "$SCRIPTS/guard-main-commit" "$HOOKDIR/pre-commit"
#   M2 (causal factor, stronger): delete the `-c core.hooksPath=...` flag below
# Measured for each: pre-fix 19 ok / 0 FAIL / 19 -> exit 4; post-fix a FAIL row
# with the total held at the invariant -> exit 1.
if [ -d "$HOOKDIR" ] && [ -z "$(ls -A "$HOOKDIR")" ]; then
	ok "override dir exists and is EMPTY (the bypass is not passing vacuously)"
else
	no "override dir '$HOOKDIR' missing or non-empty: [$(ls -A "$HOOKDIR" 2>&1 | tr '\n' ' ')]"
fi

# Positive control 1: without the override, the PRE-COMMIT guard fires.
#
# Both controls key on the guard's own QUM citation, not merely on rc != 0. That
# is not belt-and-braces: an earlier draft of this section asserted only rc != 0
# and, with the pre-commit symlink deliberately removed, still reported
# "pre-commit guard IS live (rc=128)" and exited 0 — because the plain commit
# sailed past the missing pre-commit hook and was rejected downstream by the REF
# guard instead. rc != 0 cannot attribute the rejection to a specific hook, so it
# could not falsify its own claim. The citation can (rc is kept in the message as
# a secondary discriminator: 1 = pre-commit, 128 = ref transaction).
#
# The `0:*` arm MUST come first — this is load-bearing, not stylistic. `git`'s own
# success line echoes the commit subject, so `commit -m "QUM-808"` with both hooks
# removed yields rc=0 AND output containing "QUM-808" (measured: `[main 3516bc5]
# QUM-808`). With the arms swapped that run reports `ok ... (rc=0, cites QUM-808)`
# — a false green. Matching rc=0 before the citation makes the pattern
# unreachable in that case.
out=$(SPRAWL_AGENT_IDENTITY=probe git -C "$R" commit -m z 2>&1)
rc=$?
case "$rc:$out" in
0:*) no "control FAILED: nothing blocked the plain commit, so the bypass below proves nothing: $(printf '%s' "$out" | head -1)" ;;
*QUM-808*) ok "control: pre-commit guard IS live in this repo (rc=$rc, cites QUM-808)" ;;
*) no "control FAILED: blocked (rc=$rc) but NOT by the pre-commit guard — misattribution: $(printf '%s' "$out" | head -1)" ;;
esac

# Positive control 2: --no-verify skips pre-commit, so a rejection here can only
# come from the reference-transaction guard.
out=$(SPRAWL_AGENT_IDENTITY=probe git -C "$R" commit -m z --no-verify 2>&1)
rc=$?
case "$rc:$out" in
0:*) no "control FAILED: ref guard did not fire under --no-verify, so 'BOTH' below is unmeasurable: $(printf '%s' "$out" | head -1)" ;;
*QUM-837*) ok "control: reference-transaction guard IS live in this repo (rc=$rc, --no-verify, cites QUM-837)" ;;
*) no "control FAILED: blocked (rc=$rc) but NOT by the ref-transaction guard — misattribution: $(printf '%s' "$out" | head -1)" ;;
esac

# The bypass itself. A PLAIN commit (no --no-verify) is deliberate: it must pass
# the pre-commit hook AND open a ref transaction, so rc=0 means both guards were
# silenced — which only follows because the two controls above just proved both
# were live on this very repo.
#
# Stage a FRESH change rather than relying on the controls having left the index
# intact. A blocked commit does leave it staged (measured), but that is a PREMISE of
# the two commits above, and if either unexpectedly lands, the index empties and
# this commit fails with "nothing to commit" (rc=1) while reporting "core.hooksPath
# did NOT disable the hooks" — a true failure with a false stated cause, which is
# the same misdiagnosis class this section exists to avoid. Re-`add`ing the same
# content is not enough (if the control committed it, there is nothing left to
# stage), so write new content: that yields a staged change either way. Count-neutral.
echo z2 >"$R/z.txt"
git -C "$R" add z.txt
out=$(SPRAWL_AGENT_IDENTITY=probe git -C "$R" -c core.hooksPath="$HOOKDIR" commit -m z 2>&1)
rc=$?
if [ "$rc" -eq 0 ]; then
	ok "core.hooksPath override disables BOTH hooks (rc=0) — a bypass no phase choice fixes"
else
	no "core.hooksPath did NOT disable the hooks (rc=$rc) — decision.md §2.1/§4 rests on this measurement: $(printf '%s' "$out" | head -1)"
fi

# ---------------------------------------------------------------------------
note "SUMMARY: $PASSES ok, $FAILS FAIL, $ASSERTIONS assertions"
# EXACT, not a floor with slack. Every section is now branch-invariant, so 23 is
# simultaneously the minimum and the maximum and `-ne` also catches an
# accidental double-count, which `-lt` cannot. Bump this literal deliberately
# when adding an assertion; do NOT lower it to accommodate a FAIL — a FAIL is
# supposed to exit 1 below, and lowering the count is how that becomes invisible.
if [ "$ASSERTIONS" -ne 23 ]; then
	echo "FATAL: assertion count mismatch: expected exactly 23, got $ASSERTIONS" >&2
	exit 4
fi
[ "$FAILS" -eq 0 ] || exit 1
exit 0
