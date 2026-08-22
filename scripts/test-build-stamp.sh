#!/usr/bin/env bash
# Unit gate for the QUM-1287 reproducible link stamp.
#
# WHY THIS EXISTS. `make build`'s LDFLAGS used to carry
# `-X main.date=$(shell date -u ...)`. That value changes on every invocation, so
# the link output could never be reused: every `make validate` — i.e. every
# pre-commit hook run, for every agent — relinked both binaries even when
# nothing had changed. Measured on this host at the time of the change: two
# consecutive `make build` runs on an unchanged tree produced
# c09b93836c1f87b1 and 05e72c06a4da9111. Deriving the stamp from the COMMIT date
# instead makes the link a function of the tree, so an unchanged tree relinks to
# the identical bytes.
#
# The property is easy to lose again and silent when lost — a rebuilt binary
# still works — so it is asserted rather than remembered.
#
# WHAT `built:` NOW MEANS. It is the committer date of HEAD, not wall-clock
# build time (the reproducible-builds SOURCE_DATE_EPOCH convention). On a dirty
# tree it therefore under-reports: a binary built from uncommitted work claims
# its parent commit's time. That is inherent to cacheability, and nothing in the
# tree branches on the value — `sprawl version` prints it and that is all.
#
# COST, and why it is in `validate` anyway. This suite runs four `make build`
# invocations and CLOBBERS the worktree's ./sprawl and ./hubd — two of those
# builds (B3) deliberately vary the stamp and are therefore guaranteed
# uncacheable, so it adds a few seconds to the very gate QUM-1287 is trimming.
# That is the honest trade: the relink saving is ~2.2s per validate and this
# costs about the same, so the deliverable is the byte-identity property, not
# wall clock. It is in `validate` rather than a heavier target for the reason
# test-e2e-matrix-unit is: a regression test guarding a silent, invisible
# property is worthless if it only runs when somebody remembers. Note also that
# mid-run the tree holds a binary stamped `built: 2026-01-01...`; B4 runs last
# specifically so the tree is left with a normally stamped one.
#
# This suite prescribes the SHAPE of the fix as well as its behaviour: B1c and
# B3 both drive an overridable `DATE` make variable. That is deliberate — the
# override is what lets a release or a reproducibility check pin the stamp — but
# it means inlining $(shell git log ...) straight into LDFLAGS, or using
# `override DATE`, fails these legs as "control did NOT fire" rather than as a
# property violation. Both directions fail closed.
#
# Self-contained. Run as: bash scripts/test-build-stamp.sh
# Needs bash, go, make, git, sha256sum. No claude, no tmux, no network beyond
# whatever the Go module cache already has.
#
# EXIT CODES:
#   0  every assertion ran and passed
#   1  >=1 assertion failed, or the assertion-count floor was not met
#   2  usage / internal error (could not locate the repo root)
#  77  skipped: an unmet precondition (no `go`, no `git`, or not a checkout with
#      at least one commit — with no commit there is no date to derive, so the
#      byte-identity leg would be trivially true and would read as a pass).
#      NEVER 0.

set +e # Deliberately tolerate failed assertions so we report ALL of them.

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd) || {
	echo "cannot resolve repo root"
	exit 2
}
cd "$REPO_ROOT" || {
	echo "cannot cd to repo root: $REPO_ROOT"
	exit 2
}

# Assertion-count floor. A hardcoded literal, NOT derived from anything this
# suite measures — a floor computed from the corpus it checks is satisfied by an
# empty corpus. Update it in the same commit as any change to the assertions.
MIN_ASSERTIONS=7

PASS=0
FAIL=0

pass() {
	PASS=$((PASS + 1))
	echo "  PASS: $1"
}
fail() {
	FAIL=$((FAIL + 1))
	echo "  FAIL: $1"
}

for tool in go make git sha256sum; do
	command -v "$tool" >/dev/null 2>&1 || {
		echo "SKIP: no '$tool' on PATH; cannot exercise the build stamp"
		exit 77
	}
done
git rev-parse HEAD >/dev/null 2>&1 || {
	echo "SKIP: not a git checkout with at least one commit; there is no commit date to derive, so byte-identity here would be vacuous"
	exit 77
}

# The expression the Makefile is expected to use. Duplicated deliberately: B2
# below compares the Makefile's expansion against an INDEPENDENTLY computed
# value, so a Makefile that stamped some other stable-but-wrong thing (say the
# empty string, or the author date) fails instead of agreeing with itself.
#
# `format-local:` is LOAD-BEARING and `format:` is a measured trap. `--date=format:`
# renders the committer's OWN recorded offset and ignores TZ entirely; only
# `format-local:` honours it. Every commit in this repo today carries +00:00, so
# the two agree here and `format:` would look correct — but on a commit made at a
# non-UTC offset (a cherry-pick, an outside contributor) `format:` yields a LOCAL
# wall-clock time suffixed with a literal `Z`, i.e. a value wrong by the offset
# yet self-consistent, which both this suite and the Makefile would then agree on.
# Measured: TZ=America/New_York with format: gave 2026-08-22T02:08:28Z,
# format-local: gave 2026-08-21T22:08:28Z.
EXPECTED_STAMP=$(TZ=UTC0 git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ)

# ---------------------------------------------------------------------------
# B1: the ldflags themselves are stable across two expansions.
#
# Cheapest possible form of the property — no compile — and the one that names
# the actual defect. `make print-ldflags` is an introspection seam in the same
# spirit as `print-validate-steps` and `lint-cache-dir`.
# ---------------------------------------------------------------------------
# `looks_like_ldflags` is not decoration. Compared as bare strings, two IDENTICAL
# error messages ("No rule to make target 'print-ldflags'") satisfy `$LD1 =
# $LD2` — observed, on the commit that had no such target: B1 passed while the
# seam did not exist at all. So every leg that compares expansions first demands
# the output actually be an ldflags line.
looks_like_ldflags() {
	printf '%s' "$1" | grep -q -- '-X main\.version='
}

LD1=$(make --no-print-directory print-ldflags 2>&1)
LD2=$(make --no-print-directory print-ldflags 2>&1)
if looks_like_ldflags "$LD1" && [ "$LD1" = "$LD2" ]; then
	pass "B1 LDFLAGS expand identically twice in a row"
else
	fail "B1 LDFLAGS differ between two expansions, or are not an ldflags line at all — the link cannot be cached. first=[$LD1] second=[$LD2]"
fi

# B1c — POSITIVE CONTROL for B1, and it is not a tautology over `make`: it
# re-runs the identical comparison against a Makefile copy whose DATE line is
# the pre-QUM-1287 wall-clock form, with %N nanoseconds so two expansions differ
# deterministically and no `sleep` is needed. If this does not fire, B1 above is
# incapable of failing and means nothing.
#
# The copy is run with `-C "$REPO_ROOT"`, per the Makefile's own warning that
# recipes resolve helper scripts from $(CURDIR): a copy invoked from /tmp would
# die at rc 127 for the wrong reason and look like a fired control.
CTL_DIR=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-build-stamp-XXXXXX") || {
	echo "mktemp -d failed; cannot build the B1c control"
	exit 2
}
# The scratch path must be under /tmp or under a non-empty TMPDIR before the
# `rm -rf` below is armed. The second pattern is built CONDITIONALLY on purpose:
# with TMPDIR unset, "${TMPDIR%/}"/* expands to the pattern /* which matches
# every absolute path, so the guard admitted /home/coder/evil — verified. A guard
# that reads as defence for an rm -rf and provides none is worse than none.
scratch_is_safe() {
	case "$1" in
	/tmp/*) return 0 ;;
	esac
	# TMPDIR="/" would strip to "" and degenerate the pattern back to /*.
	[ -n "${TMPDIR:-}" ] && [ "$TMPDIR" != "/" ] || return 1
	case "$1" in
	"${TMPDIR%/}"/*) return 0 ;;
	esac
	return 1
}
scratch_is_safe "$CTL_DIR" || {
	echo "refusing to use scratch dir outside /tmp or \$TMPDIR: $CTL_DIR"
	exit 2
}
trap 'rm -rf "$CTL_DIR"' EXIT

CTL_MK="$CTL_DIR/Makefile-wallclock"
sed 's|^DATE .*=.*$|DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%S.%NZ)|' "$REPO_ROOT/Makefile" >"$CTL_MK"
if grep -q '%NZ' "$CTL_MK"; then
	CLD1=$(make --no-print-directory -f "$CTL_MK" -C "$REPO_ROOT" print-ldflags 2>&1)
	CLD2=$(make --no-print-directory -f "$CTL_MK" -C "$REPO_ROOT" print-ldflags 2>&1)
	if looks_like_ldflags "$CLD1" && [ "$CLD1" != "$CLD2" ]; then
		pass "B1c control fired: a wall-clock DATE makes two expansions differ, so B1 can fail"
	else
		fail "B1c control did NOT fire — with a wall-clock DATE the two expansions were [$CLD1] and [$CLD2]. B1 is therefore unproven and may be incapable of failing. (If these are make errors rather than ldflags, the control Makefile is broken, not the property.)"
	fi
else
	fail "B1c could not build the wall-clock control Makefile (the DATE line did not match the sed pattern) — B1 is unproven"
fi

# ---------------------------------------------------------------------------
# B2: the stamp is derived from the commit, not from the clock, and not from
# nothing. Guards the degenerate fix of simply deleting the date.
# ---------------------------------------------------------------------------
if printf '%s' "$LD1" | grep -qF -- "-X main.date=$EXPECTED_STAMP"; then
	pass "B2 LDFLAGS stamp main.date with HEAD's committer date ($EXPECTED_STAMP)"
else
	fail "B2 LDFLAGS do not carry -X main.date=$EXPECTED_STAMP; got [$LD1]"
fi

# ---------------------------------------------------------------------------
# B3: sensitivity control for B4. Two builds whose ONLY difference is the date
# stamp must produce different hashes. Without this, B4's equality is equally
# consistent with sha256sum comparing a cached artifact to itself, or with the
# stamp having been dropped from the link entirely.
# ---------------------------------------------------------------------------
# `build_and_hash` fails the caller's leg on a non-zero build instead of hashing
# whatever binary happened to be left behind. Without this, a tree that does not
# COMPILE reported B4/B4b as PASS: both builds failed, ./sprawl was still B3's
# fixture, and the two hashes were trivially equal. A -n guard catches a missing
# binary, never a stale one.
BUILD_RC=0
build_and_hash() {
	# $1: optional DATE override ("" for the Makefile's own derivation)
	if [ -n "$1" ]; then
		make --no-print-directory build DATE="$1" >/dev/null 2>&1
	else
		make --no-print-directory build >/dev/null 2>&1
	fi
	BUILD_RC=$?
	[ "$BUILD_RC" -eq 0 ] || return 1
	sha256sum "$2" | cut -d' ' -f1
}

H_A=$(build_and_hash 2026-01-01T00:00:00Z sprawl)
H_A_RC=$?
H_B=$(build_and_hash 2026-01-01T00:00:01Z sprawl)
H_B_RC=$?
if [ "$H_A_RC" -ne 0 ] || [ "$H_B_RC" -ne 0 ]; then
	fail "B3 control could not run: 'make build' failed (rc $H_A_RC/$H_B_RC). The tree does not compile, so nothing here measures the stamp."
elif [ -n "$H_A" ] && [ "$H_A" != "$H_B" ]; then
	pass "B3 control fired: a one-second change in the stamp changes the binary hash, so B4 below can fail"
else
	fail "B3 control did NOT fire: two builds one second apart in stamp hashed $H_A and $H_B. Either the stamp no longer reaches the link or the hash probe is broken — B4 proves nothing."
fi

# ---------------------------------------------------------------------------
# B4: the gate. Two consecutive `make build` runs on an unchanged tree produce
# byte-identical binaries. Drives the real recipe, not a hand-rolled `go build`,
# or it would assert nothing about the Makefile.
#
# Runs last so the tree is left holding a normally-stamped binary rather than
# one of B3's fixtures.
# ---------------------------------------------------------------------------
S1=$(build_and_hash "" sprawl)
S1_RC=$?
D1=$(sha256sum hubd 2>/dev/null | cut -d' ' -f1)
S2=$(build_and_hash "" sprawl)
S2_RC=$?
D2=$(sha256sum hubd 2>/dev/null | cut -d' ' -f1)
if [ "$S1_RC" -ne 0 ] || [ "$S2_RC" -ne 0 ]; then
	fail "B4 could not run: 'make build' failed (rc $S1_RC/$S2_RC)"
	fail "B4b could not run: 'make build' failed (rc $S1_RC/$S2_RC)"
elif [ -n "$S1" ] && [ "$S1" = "$S2" ]; then
	pass "B4 two consecutive 'make build' runs produced a byte-identical ./sprawl ($S1)"
else
	fail "B4 ./sprawl differs across two consecutive builds of an unchanged tree ($S1 vs $S2) — the link is not cacheable"
fi
# B4b is the NEGATIVE CONTROL for the hash probe, not a gate on this change:
# ./hubd is built by `go build ./cmd/hubd` with no -ldflags at all, so its bytes
# never depended on the date stamp and it stayed quiet in the red run — the
# "subject known clean, probe must stay silent" half. It CANNOT go red for a
# QUM-1287 regression; only for a build failure or a nondeterministic toolchain.
# Kept for that reason, and labelled so nobody reads it as coverage.
if [ "$S1_RC" -ne 0 ] || [ "$S2_RC" -ne 0 ]; then
	: # already reported above; do not count B4b twice
elif [ -n "$D1" ] && [ "$D1" = "$D2" ]; then
	pass "B4b negative control stayed quiet: ./hubd (no -ldflags) is byte-identical across two builds"
else
	fail "B4b negative control FIRED on a clean subject: ./hubd, which carries no date stamp, differs across two builds ($D1 vs $D2). Suspect the toolchain or the build, not the stamp."
fi

# ---------------------------------------------------------------------------
# B5: the consumer still works. The stamp's only surface is `sprawl version`'s
# `built:` line, so this is what makes B2 a statement about the product rather
# than about a make variable.
# ---------------------------------------------------------------------------
BUILT=$(./sprawl version 2>&1 | sed -n 's/^built: //p')
if [ "$BUILT" = "$EXPECTED_STAMP" ]; then
	pass "B5 'sprawl version' reports built: $BUILT"
else
	fail "B5 'sprawl version' reported built: [$BUILT], want [$EXPECTED_STAMP] — the stamp is not reaching the CLI"
fi

# ---------------------------------------------------------------------------
# Summary + assertion-count floor.
# ---------------------------------------------------------------------------
TOTAL=$((PASS + FAIL))
echo "=== build-stamp results: $PASS passed / $FAIL failed ==="
if [ "$TOTAL" -lt "$MIN_ASSERTIONS" ]; then
	echo "  FAIL: only $TOTAL assertions ran, expected at least $MIN_ASSERTIONS — this run measured less than it claims"
	exit 1
fi
[ "$FAIL" -eq 0 ] || exit 1
exit 0
