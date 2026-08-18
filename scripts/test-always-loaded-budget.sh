#!/usr/bin/env bash
# scripts/test-always-loaded-budget.sh — unit tests for the always-loaded
# instruction-budget resolver (scripts/always-loaded-budget.sh).
#
# Why this exists: the resolver replaces a number that careful people got wrong
# four times in one day (768 -> 963 -> 1040 -> 211 -> 170), each revision found
# by checking what the previous one assumed. The replacement is only worth
# anything if it has been WATCHED FAILING. The red-first evidence — which
# assertion was watched failing, against what partial implementation, and what
# it printed — is recorded in
# docs/archive/budget-resolver.md, not claimed here.
#
# HARD BOUNDARY: this suite reads NOTHING from the real tree. Every assertion
# runs against a throwaway git fixture, and CLAUDE_CONFIG_DIR is always pointed
# at an empty fixture dir so a resolver that falls back to $HOME/.claude is
# caught rather than silently making the suite environment-dependent. That
# boundary is what makes it safe to put this in `make validate` while the live
# gate (`make always-loaded-budget`) stays standalone — otherwise an edit to the
# real CLAUDE.md would fail validate for an unrelated author.
#
# No claude, no tmux, no sandbox — bash + git + mktemp.
# Run as: bash scripts/test-always-loaded-budget.sh

set +e # Deliberately tolerate failed assertions so we report ALL failures.

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
BUDGET="$SCRIPT_DIR/always-loaded-budget.sh"

if ! command -v git >/dev/null 2>&1; then
	# 77, not 0: a skip asserts nothing, and the exit status is the only signal
	# `make` sees. Same rule as QUM-952.
	echo "SKIP: git not installed — the resolver's fixtures are real git repos; NOTHING was asserted" >&2
	exit 77
fi
if [ ! -x "$BUDGET" ]; then
	echo "  FAIL: $BUDGET missing or not executable — nothing to test" >&2
	exit 1
fi

# One parent tempdir, CHECKED. Under `set +e` a failed mktemp leaves TMPPARENT
# empty, every fixture root becomes '/', and the ledger becomes '/ledger' —
# unwritable — so every assertion would pass or fail vacuously and the floor
# that exists to catch exactly that could not fire either.
TMPPARENT=$(mktemp -d)
if [ -z "$TMPPARENT" ] || [ ! -d "$TMPPARENT" ]; then
	echo "  FAIL: could not create a tempdir (TMPDIR=${TMPDIR:-/tmp}) — every fixture root would be '/' and every assertion would be vacuous" >&2
	exit 1
fi
# /tmp hygiene: assert the path before destroying it, never a glob.
case "$TMPPARENT" in
/tmp/* | /var/folders/*) ;;
*)
	echo "  FAIL: refusing to run with TMPPARENT='$TMPPARENT' outside /tmp — cleanup would delete an unexpected path" >&2
	exit 1
	;;
esac
trap 'rm -rf "$TMPPARENT"' EXIT

LEDGER="$TMPPARENT/ledger"
: >"$LEDGER"
if [ ! -w "$LEDGER" ]; then
	echo "  FAIL: ledger $LEDGER is not writable — no assertion could be recorded, so the floor below would measure nothing" >&2
	exit 1
fi

# Per-block minima, not just a grand total. A single sum lets any one block die
# at its first line while the other blocks carry the run over the floor — which
# is the false green the floor exists to prevent. Each block runs in a subshell
# (so counters cannot roll up in variables) and tags its ledger lines with
# $BLOCK. Bump the number when you add or remove an assertion in that block.
EXPECT_BLOCKS="
multiplicity:12
antidedup:4
discovery:8
imports:24
readcheck:13
mention:6
ceiling:11
usage:10
report:10
manifest:15
tracked:66
"

BLOCK=unset
pass() {
	echo "P $BLOCK" >>"$LEDGER"
	echo "  PASS: $1"
}
fail() {
	echo "F $BLOCK" >>"$LEDGER"
	echo "  FAIL: $1" >&2
}
assert_eq() {
	if [ "$2" = "$3" ]; then pass "$1 (=$3)"; else fail "$1: want '$2', got '$3'"; fi
}
assert_contains() {
	case "$2" in
	*"$3"*) pass "$1" ;;
	*) fail "$1: output did not contain '$3'. Got:
$2" ;;
	esac
}
assert_not_contains() {
	case "$2" in
	*"$3"*) fail "$1: output unexpectedly contained '$3'. Got:
$2" ;;
	*) pass "$1" ;;
	esac
}
# assert_line_both — the two facts must appear on the SAME line. A whole-output
# `case` match cannot tell "the report names the largest contributor" from "the
# report mentions that filename somewhere", and for a diagnostic that is the
# entire usefulness.
assert_line_both() {
	# $1 desc, $2 haystack, $3 needle-a, $4 needle-b
	if printf '%s\n' "$2" | grep -F -- "$3" | grep -qF -- "$4"; then
		pass "$1"
	else
		fail "$1: no single line contained both '$3' and '$4'. Got:
$2"
	fi
}
# assert_setup — a broken fixture must be LOUD and COUNTED, not misfiled as a
# resolver defect. Without this, `git worktree add` failing yields a path that
# does not exist and section 1 reports 'want 42, got 7', which reads exactly
# like a resolver bug.
assert_setup() {
	# $1 desc, $2.. = test args, e.g. -d "$wt"
	local desc=$1
	shift
	if test "$@"; then pass "setup: $desc"; else fail "setup FAILED: $desc — the scenario never started"; fi
}

# ---------------------------------------------------------------------------
# Fixture helpers
# ---------------------------------------------------------------------------

new_repo() {
	local r
	r=$(mktemp -d "$TMPPARENT/repo.XXXXXX") || return 1
	[ -n "$r" ] && [ -d "$r" ] || return 1
	git -C "$r" init -q -b main >/dev/null 2>&1 || return 1
	git -C "$r" config user.email t@t.invalid
	git -C "$r" config user.name t
	git -C "$r" config commit.gpgsign false
	git -C "$r" commit -q --allow-empty -m init >/dev/null 2>&1 || return 1
	echo "$r"
}

# add_worktree REPO NAME — the real layout: a git worktree nested UNDER the repo
# root at .sprawl/worktrees/<name>, which is what produces the CLAUDE.local.md
# double-injection.
add_worktree() {
	mkdir -p "$1/.sprawl/worktrees" || return 1
	git -C "$1" worktree add -q -b "wt-$2" "$1/.sprawl/worktrees/$2" >/dev/null 2>&1 || return 1
	echo "$1/.sprawl/worktrees/$2"
}

nlines() {
	local f=$1 n=$2 i
	: >"$f"
	for ((i = 1; i <= n; i++)); do echo "line $i" >>"$f"; done
}

# prepend LINE FILE — portable; `sed -i '1i text'` is GNU-only and the BSD form
# needs a backup suffix, so it would silently no-op on macOS.
prepend() {
	printf '%s\n' "$1" | cat - "$2" >"$2.tmp" && mv "$2.tmp" "$2"
}

# track REPO PATH... — commit into the SPRAWL ROOT's index. This is the oracle
# for files that live at or above the sprawl root, and for the paths NAMED inside
# an always-loaded file by the read-instruction check (those are repo-relative
# references, for which the root index is the right authority).
#
# It is NOT the oracle for a file injected from a worktree — see track_wt_ok.
track() {
	local r=$1
	shift
	git -C "$r" add -- "$@" >/dev/null 2>&1 || return 1
	git -C "$r" commit -q -m fixture >/dev/null 2>&1
	return 0
}

# track_ok REPO PATH... — track, and ASSERT it took. Since QUM-1155 the enforced
# set is tracked-only, so an untracked fixture CLAUDE.md resolves to zero
# enforced injections and trips the discovery floor — which reads exactly like a
# resolver defect. One ledger line per call, so each block's floor moves by the
# number of calls it makes.
#
# The count comparison is not decoration: `git ls-files -- a b c` prints a line
# for any path that IS tracked, so a `-n` test passes on a PARTIAL track, and the
# block's headline totals depend on the paths that silently failed. It asserts
# the INDEX, matching the resolver's own `git ls-files` oracle — a resolver that
# switched to `git ls-tree HEAD` would diverge here undetected.
track_ok() {
	local r=$1
	shift
	local want=$#
	if track "$r" "$@" && [ "$(git -C "$r" ls-files -- "$@" | wc -l | tr -d ' ')" = "$want" ]; then
		pass "setup: tracked all $want of: $*"
	else
		fail "setup FAILED: did not track all of '$*' — enforced totals in this block would be wrong, and would read as a resolver defect"
	fi
}

# track_wt_ok WORKTREE PATH... — track on the WORKTREE's own branch, and assert
# it took. Enforcement asks the checkout a file was INJECTED FROM, so tracking a
# name at the sprawl root does not make the worktree copy enforced. That
# asymmetry is the point: it is what lets the gate fail on a file an agent added
# on its branch, which the root index (main) knows nothing about.
track_wt_ok() {
	local w=$1
	shift
	local want=$#
	git -C "$w" add -- "$@" >/dev/null 2>&1
	git -C "$w" commit -q -m fixture >/dev/null 2>&1
	if [ "$(git -C "$w" ls-files -- "$@" | wc -l | tr -d ' ')" = "$want" ]; then
		pass "setup: tracked all $want on the worktree branch: $*"
	else
		fail "setup FAILED: did not track all of '$*' on the worktree branch — the worktree injections would all be excluded"
	fi
}

conf() { echo "CEILING=$2" >"$1"; }

# EMPTY_CFG — an EXISTING but empty out-of-tree config dir. Pointing the default
# at a nonexistent path would let a resolver that falls back to $HOME/.claude
# read the developer's real home undetected.
EMPTY_CFG="$TMPPARENT/empty-claude-config"
mkdir -p "$EMPTY_CFG"

# EMPTY_ALLOW — every run gets an empty fixture allowlist unless the case passes
# its own. Without this the suite silently used the REAL
# scripts/always-loaded-budget.allow, so adding a path to it in the tree could
# quietly stop a fixture violation from firing and the suite would still be
# green. It is the file header's HARD BOUNDARY claim, made true.
EMPTY_ALLOW="$TMPPARENT/empty-allow"
: >"$EMPTY_ALLOW"

RC=0
OUT=""
ERROUT=""
run_budget() {
	local root=$1
	shift
	# stdout and stderr captured SEPARATELY: "the verdict line is on stdout" is
	# part of the contract (make/CI scrape it) and a 2>&1 capture cannot
	# constrain it.
	local errfile="$TMPPARENT/stderr.$$"
	# --allow first so a case's own --allow (parsed later) still wins.
	OUT=$(CLAUDE_CONFIG_DIR="${FIXTURE_CLAUDE_CONFIG_DIR:-$EMPTY_CFG}" \
		"$BUDGET" --root "$root" --allow "$EMPTY_ALLOW" "$@" 2>"$errfile")
	RC=$?
	ERROUT=$(cat "$errfile")
	rm -f "$errfile"
}

verdict_field() {
	printf '%s\n' "$OUT" | grep -E '^ALWAYS-LOADED: ' | sed -E "s/.* $1=([^ ]+).*/\1/"
}

echo "=== always-loaded budget resolver: unit tests ==="

# ---------------------------------------------------------------------------
# 1. Injection multiplicity — the axis the whole tool exists to get right.
#    The copies are DIFFERENT SIZES on purpose: with identical copies you cannot
#    distinguish "counted two injections" from "measured one file and doubled
#    it", which is the confusion that hid the doubling for the whole audit.
# ---------------------------------------------------------------------------
echo "--- injection multiplicity ---"
(
	BLOCK=multiplicity
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	wt=$(add_worktree "$repo" a)
	assert_setup "worktree fixture exists" -d "$wt"
	nlines "$repo/CLAUDE.local.md" 7
	mkdir -p "$repo/.sprawl" "$repo/docs"
	nlines "$repo/.sprawl/CLAUDE.local.md" 3 # mid-level: separates "walk up" from "root+worktree"
	nlines "$wt/CLAUDE.md" 30
	nlines "$wt/CLAUDE.local.md" 5
	# Enforcement is tracked-only, and tracked-ness is a property of the NAME in
	# the ROOT index: <worktree>/CLAUDE.md is enforced because `CLAUDE.md` is
	# tracked at the root. The three root copies below exist to put those names
	# in the index; each is either shadowed or a descendant, so none is counted —
	# which the totals in this block would catch immediately if one were.
	nlines "$repo/CLAUDE.md" 999
	nlines "$repo/.sprawl/CLAUDE.md" 500
	nlines "$repo/docs/CLAUDE.md" 500
	track_ok "$repo" CLAUDE.md CLAUDE.local.md .sprawl/CLAUDE.md .sprawl/CLAUDE.local.md docs/CLAUDE.md
	track_wt_ok "$wt" CLAUDE.md CLAUDE.local.md
	c="$TMPPARENT/c1.conf"
	conf "$c" 1000

	# The ancestor CLAUDE.md copies are DELETED for the first run and restored for
	# the second, so the pair below is a real before/after and not the same
	# command twice. Tracked-ness survives deleting the working-tree file (it is a
	# property of the index), which is what lets the names stay tracked across
	# both runs.
	rm "$repo/CLAUDE.md" "$repo/.sprawl/CLAUDE.md" "$repo/docs/CLAUDE.md"
	run_budget "$wt" --conf "$c"
	assert_eq "CLAUDE.local.md accumulates at EVERY level: 7 + 3 + 5, plus a 30-line CLAUDE.md" "45" "$(verdict_field in_tree)"
	assert_eq "four in-tree injection sites resolved" "4" "$(verdict_field injections)"
	assert_eq "clean fixture exits 0" "0" "$RC"

	# CLAUDE.md resolves to the NEAREST ancestor only while CLAUDE.local.md
	# accumulates. Same directory, same walk, opposite treatment — measured from
	# three live agent context headers (weave at the repo root, two managers in
	# worktrees), not inferred. Restoring 999 + 500 lines of ancestor CLAUDE.md
	# must not move the total by one line.
	nlines "$repo/CLAUDE.md" 999
	nlines "$repo/.sprawl/CLAUDE.md" 500
	nlines "$repo/docs/CLAUDE.md" 500
	run_budget "$wt" --conf "$c"
	assert_eq "ancestor CLAUDE.md files are NOT counted when a nearer one exists" "45" "$(verdict_field in_tree)"

	# Negative control for the opposite implementation error: a resolver using
	# `find -name 'CLAUDE*.md'` under the root would sweep in a DESCENDANT.
	mkdir -p "$wt/docs"
	nlines "$wt/docs/CLAUDE.md" 500
	run_budget "$wt" --conf "$c"
	assert_eq "a descendant CLAUDE.md is not loaded and not counted" "45" "$(verdict_field in_tree)"
	assert_not_contains "the descendant CLAUDE.md is absent from the breakdown" "$OUT" "docs/CLAUDE.md"

	rm "$wt/CLAUDE.local.md"
	run_budget "$wt" --conf "$c"
	assert_eq "removing the worktree copy leaves the two ancestor copies (7+3+30)" "40" "$(verdict_field in_tree)"
	assert_eq "site count drops from 4 to 3" "3" "$(verdict_field injections)"
)
(
	BLOCK=antidedup
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	wt=$(add_worktree "$repo" a)
	nlines "$repo/CLAUDE.local.md" 5
	cp "$repo/CLAUDE.local.md" "$wt/CLAUDE.local.md"
	nlines "$wt/CLAUDE.md" 10
	nlines "$repo/CLAUDE.md" 77 # shadowed; exists to put the NAME in the root index
	track_ok "$repo" CLAUDE.md CLAUDE.local.md
	track_wt_ok "$wt" CLAUDE.md CLAUDE.local.md
	c="$TMPPARENT/c2.conf"
	conf "$c" 1000
	run_budget "$wt" --conf "$c"
	assert_eq "byte-identical CLAUDE.local.md copies are NOT deduplicated (5+5+10)" "20" "$(verdict_field in_tree)"
)

# ---------------------------------------------------------------------------
# 2. Blind-instrument control. A resolver that finds nothing must refuse to
#    report, not print a plausible 0 and exit green — "a confident number
#    produced by an instrument blind to its target" is the whole failure family.
# ---------------------------------------------------------------------------
echo "--- discovery floor / positive control ---"
(
	BLOCK=discovery
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	c="$TMPPARENT/c3.conf"
	conf "$c" 1000
	run_budget "$repo" --conf "$c"
	assert_eq "an empty repo must NOT exit 0 (blind instrument)" "1" "$RC"
	assert_contains "empty repo names the zero-resolution" "$OUT$ERROUT" "resolved 0 in-tree injections"

	nlines "$repo/CLAUDE.md" 40
	track_ok "$repo" CLAUDE.md
	run_budget "$repo" --conf "$c"
	assert_eq "the resolver finds a file it is pointed at (main checkout, 40 lines)" "40" "$(verdict_field in_tree)"

	# F1: if `git ls-files` fails, the tracked list is empty, is_tracked() is
	# always false, and BOTH read-check legs become structurally unable to fire
	# while the run reports green. Corrupt the index and demand a refusal.
	nlines "$repo/CLAUDE.md" 40
	cp "$repo/.git/index" "$TMPPARENT/index.bak" 2>/dev/null
	printf 'not an index' >"$repo/.git/index"
	run_budget "$repo" --conf "$c"
	assert_eq "a git ls-files failure refuses the run instead of voiding the read check" "1" "$RC"
	assert_contains "the refusal says why" "$OUT$ERROUT" "structurally unable to fire"
	cp "$TMPPARENT/index.bak" "$repo/.git/index" 2>/dev/null || rm -f "$repo/.git/index"

	# wc -l undercounts a file with no trailing newline.
	nlines "$repo/CLAUDE.md" 39
	printf 'line 40 with no trailing newline' >>"$repo/CLAUDE.md"
	run_budget "$repo" --conf "$c"
	assert_eq "a final line without a trailing newline still counts" "40" "$(verdict_field in_tree)"
)

# ---------------------------------------------------------------------------
# 3. @-import walker. The real repo has ZERO @-imports today, so without these
#    fixtures the walker could be entirely broken and nothing would show it.
# ---------------------------------------------------------------------------
echo "--- @-import resolution ---"
(
	BLOCK=imports
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	c="$TMPPARENT/c6.conf"
	conf "$c" 1000

	nlines "$repo/b.md" 10
	nlines "$repo/CLAUDE.md" 9
	prepend "@b.md" "$repo/CLAUDE.md"
	track_ok "$repo" b.md CLAUDE.md
	run_budget "$repo" --conf "$c"
	assert_eq "A@->B chain counts both (10+10)" "20" "$(verdict_field in_tree)"
	assert_contains "the report states how many imports resolved" "$OUT" "@-imports resolved: 1"
	assert_eq "an imported file is its own injection" "2" "$(verdict_field injections)"

	nlines "$repo/c.md" 10
	nlines "$repo/b.md" 9
	prepend "@c.md" "$repo/b.md"
	track_ok "$repo" c.md
	run_budget "$repo" --conf "$c"
	assert_eq "transitive A->B->C counts all three" "30" "$(verdict_field in_tree)"
	assert_contains "the import counter is not stuck at 1" "$OUT" "@-imports resolved: 2"
)
(
	BLOCK=imports
	# Diamond: D is reached via two distinct import paths and must be counted
	# for each — injections, not distinct files.
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	c="$TMPPARENT/c7.conf"
	conf "$c" 1000
	nlines "$repo/d.md" 10
	printf '@d.md\n' >"$repo/b.md"
	printf '@d.md\n' >"$repo/c.md"
	printf '@b.md\n@c.md\n' >"$repo/CLAUDE.md"
	track_ok "$repo" CLAUDE.md b.md c.md d.md
	run_budget "$repo" --conf "$c"
	assert_eq "a diamond counts the shared import once per path (2+1+1+10+10)" "24" "$(verdict_field in_tree)"
	assert_contains "the diamond resolves four imports, not three" "$OUT" "@-imports resolved: 4"
)
(
	BLOCK=imports
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	c="$TMPPARENT/c8.conf"
	conf "$c" 1000
	printf '@b.md\n' >"$repo/CLAUDE.md"
	printf '@CLAUDE.md\n' >"$repo/b.md"
	track_ok "$repo" CLAUDE.md b.md
	run_budget "$repo" --conf "$c"
	assert_eq "an import cycle terminates and exits 0" "0" "$RC"
	assert_eq "a cycle counts each file once per chain (1+1)" "2" "$(verdict_field in_tree)"
)
(
	BLOCK=imports
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	c="$TMPPARENT/c9.conf"
	conf "$c" 1000
	printf '@nope.md\n' >"$repo/CLAUDE.md"
	track_ok "$repo" CLAUDE.md
	run_budget "$repo" --conf "$c"
	assert_eq "a dangling @-import fails rather than skipping" "1" "$RC"
	assert_contains "the dangling import names the unresolved path" "$OUT$ERROUT" "nope.md"
)
(
	BLOCK=imports
	# An import resolving OUTSIDE the sprawl root loads, but is not ours to edit
	# and has no portable manifest form — same class as the user-global
	# CLAUDE.md. Reported, never folded into the enforced total.
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	outside=$(mktemp -d "$TMPPARENT/outside.XXXXXX")
	nlines "$outside/global.md" 50
	nlines "$repo/CLAUDE.md" 9
	prepend "@$outside/global.md" "$repo/CLAUDE.md"
	track_ok "$repo" CLAUDE.md
	c="$TMPPARENT/c9b.conf"
	conf "$c" 1000
	run_budget "$repo" --conf "$c"
	assert_eq "an @-import outside the sprawl root is NOT in the enforced total" "10" "$(verdict_field in_tree)"
	assert_line_both "it is reported as out-of-tree with its size" "$OUT" "$outside/global.md" "50"
)

# ---------------------------------------------------------------------------
# 4. The read-instruction ban. Two narrow lexical legs:
#      (a) mention  — a tracked .md named in backticks and not @-imported
#      (b) mandate  — an imperative read verb immediately followed by a
#                     backticked tracked path of ANY extension
#    Leg (b) is exercised on a tracked NON-.md path so it cannot be satisfied by
#    an implementation that only ships leg (a).
# ---------------------------------------------------------------------------
echo "--- read-instruction ban ---"
(
	BLOCK=readcheck
	repo=$(new_repo)
	wt=$(add_worktree "$repo" a)
	assert_setup "read-check worktree fixture exists" -d "$wt"
	c="$TMPPARENT/c10.conf"
	conf "$c" 1000
	mkdir -p "$repo/docs" "$repo/scripts"
	nlines "$repo/docs/x.md" 5
	nlines "$repo/scripts/helper.sh" 2
	# The root CLAUDE.md exists only to put the NAME in the root index, which is
	# what makes the worktree copy enforced. It is shadowed by that copy and never
	# counted — the totals elsewhere in this suite would catch it if it were.
	nlines "$repo/CLAUDE.md" 3
	track_ok "$repo" CLAUDE.md docs/x.md scripts/helper.sh
	# The worktree CLAUDE.md is rewritten by each case below; tracking it once
	# here is enough, since tracked-ness is a property of the index, not content.
	nlines "$wt/CLAUDE.md" 1
	track_wt_ok "$wt" CLAUDE.md
	# `track` cannot fail silently here: three negative controls below assert
	# violations=0, and an empty index would satisfy all of them for the wrong
	# reason.
	assert_setup "the read-check targets are actually tracked" -n "$(git -C "$repo" ls-files docs/x.md scripts/helper.sh)"

	# The violation is on line 3 of a 5-line file, so "CLAUDE.md:3" cannot be
	# satisfied by a per-file breakdown rendering path:linecount.
	{
		echo "filler one"
		echo "filler two"
		echo 'Read `docs/x.md` for project context.'
		echo "filler four"
		echo "filler five"
	} >"$wt/CLAUDE.md"
	run_budget "$wt" --conf "$c"
	assert_eq "a mandated read of a tracked, unimported file fails the run" "1" "$RC"
	assert_eq "one target reported once, even though both legs match it" "1" "$(verdict_field violations)"
	assert_line_both "the violation names file:line AND the target on one line" "$OUT$ERROUT" "CLAUDE.md:3" "docs/x.md"

	# Leg (b) alone: a tracked NON-.md path under a read mandate. Leg (a) cannot
	# fire here, so an implementation shipping only leg (a) fails this.
	printf 'Read `scripts/helper.sh` before running anything.\n' >"$wt/CLAUDE.md"
	run_budget "$wt" --conf "$c"
	assert_eq "an imperative read of a tracked NON-.md path fires" "1" "$(verdict_field violations)"
	assert_contains "the non-.md violation names the script" "$OUT$ERROUT" "scripts/helper.sh"

	# Same sentence, @-imported: allowed by the rule.
	printf '@../../../docs/x.md\nRead `docs/x.md` for project context.\n' >"$wt/CLAUDE.md"
	run_budget "$wt" --conf "$c"
	assert_eq "the same read instruction is silent once the file IS @-imported" "0" "$(verdict_field violations)"

	# Untracked target: not ours to govern.
	nlines "$repo/docs/untracked.md" 3
	printf 'Read `docs/untracked.md` first.\n' >"$wt/CLAUDE.md"
	run_budget "$wt" --conf "$c"
	assert_eq "an untracked target does not fire" "0" "$(verdict_field violations)"

	# Skill pointer: allowed, and free — not a tracked path.
	printf 'Read `/testing-practices` before writing any tests.\n' >"$wt/CLAUDE.md"
	run_budget "$wt" --conf "$c"
	assert_eq "a skill pointer does not fire" "0" "$(verdict_field violations)"

	# A tracked NON-.md path mentioned WITHOUT a read mandate: neither leg may
	# fire. This is the control that separates "checks the .md suffix" from
	# "checks the verb", and it reproduces the shape of the real CLAUDE.md line
	# that a line-scoped loose rule false-positives on: the word "read" and a
	# backticked tracked path on the same line, far apart, no mandate between.
	printf 'A parent-commit control proves a failure is pre-existing; read the skill before reviewing any assertion (worked example: `scripts/helper.sh`).\n' >"$wt/CLAUDE.md"
	run_budget "$wt" --conf "$c"
	assert_eq "a distant backticked tracked path on a line containing 'read' does not fire" "0" "$(verdict_field violations)"
)
(
	BLOCK=mention
	# Leg (a) alone: a bare mention of a tracked .md with no read verb at all.
	# The check fails toward RED — the allowlist is the escape hatch.
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	c="$TMPPARENT/c11.conf"
	conf "$c" 1000
	mkdir -p "$repo/docs"
	nlines "$repo/docs/pointer.md" 5
	printf 'See `docs/pointer.md` for the punchlist.\n' >"$repo/CLAUDE.md"
	track "$repo" docs/pointer.md CLAUDE.md

	run_budget "$repo" --conf "$c"
	assert_eq "a bare mention of a tracked .md fires (fail-toward-red)" "1" "$(verdict_field violations)"

	# Coupling control: docs/todo/punchlist.md is a REAL entry in the tree's
	# scripts/always-loaded-budget.allow. A fixture mentioning it must still fire,
	# or the suite is reading the real allowlist and its verdicts depend on an
	# unrelated file.
	# The ONE deliberate real-tree read in this suite, and the header's single
	# exception: the control below is only a control while this path really is
	# allowlisted in the tree. Without this assertion it keeps passing (it runs
	# under EMPTY_ALLOW) while controlling nothing.
	assert_setup "docs/todo/punchlist.md is still a real allowlist entry" \
		-n "$(awk '{sub(/#.*/, "")} NF {print $1}' "$SCRIPT_DIR/always-loaded-budget.allow" | grep -Fx docs/todo/punchlist.md)"
	mkdir -p "$repo/docs/todo"
	nlines "$repo/docs/todo/punchlist.md" 4
	printf 'See `docs/todo/punchlist.md` for the punchlist.\n' >"$repo/CLAUDE.md"
	track "$repo" docs/todo/punchlist.md CLAUDE.md
	run_budget "$repo" --conf "$c"
	assert_eq "a path allowlisted in the REAL tree still fires against a fixture" "1" "$(verdict_field violations)"
	printf 'See `docs/pointer.md` for the punchlist.\n' >"$repo/CLAUDE.md"
	track "$repo" CLAUDE.md

	allow="$TMPPARENT/allow11"
	echo "docs/pointer.md  # on-demand pointer, not a mandated read" >"$allow"
	run_budget "$repo" --conf "$c" --allow "$allow"
	assert_eq "an allowlisted mention is silent" "0" "$(verdict_field violations)"

	: >"$allow"
	run_budget "$repo" --conf "$c" --allow "$allow"
	assert_eq "removing the allowlist entry re-fires (mutation on the allowlist)" "1" "$(verdict_field violations)"
)

# ---------------------------------------------------------------------------
# 5. Ceiling. A conf file is still a hardcoded number; what makes it not the
#    same artifact is that it FAILS when crossed, and says which file to cut.
# ---------------------------------------------------------------------------
echo "--- ceiling ---"
(
	BLOCK=ceiling
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	nlines "$repo/big.md" 30
	nlines "$repo/CLAUDE.md" 9
	prepend "@big.md" "$repo/CLAUDE.md"
	# `other.md` is a control, not scenery: a resolver that globs every .md in
	# the tree would count it and every total below would move.
	nlines "$repo/other.md" 7
	track_ok "$repo" CLAUDE.md big.md other.md
	c="$TMPPARENT/c12.conf"

	run_budget "$repo" --conf "$TMPPARENT/does-not-exist.conf"
	assert_eq "a missing conf file exits 2" "2" "$RC"

	echo "# no ceiling here" >"$c"
	run_budget "$repo" --conf "$c"
	assert_eq "a conf without CEILING= exits 2" "2" "$RC"

	echo "CEILING=abc" >"$c"
	run_budget "$repo" --conf "$c"
	assert_eq "a non-integer CEILING exits 2" "2" "$RC"

	conf "$c" 10
	run_budget "$repo" --conf "$c" --ceiling 40
	assert_eq "--ceiling overrides the conf file" "40" "$(verdict_field ceiling)"
	assert_eq "total == ceiling is OK (<=, not <)" "0" "$RC"
	assert_not_contains "an unreferenced .md in the tree is not swept in" "$OUT" "other.md"

	run_budget "$repo" --conf "$c" --ceiling 39
	assert_eq "total == ceiling+1 fails" "1" "$RC"
	# Two facts on one line, and one of them is big.md's OWN size. A diagnostic
	# that merely names a file, or names the wrong file, cannot satisfy both:
	# CLAUDE.md (the entry point, and the naive answer) is 10 lines, big.md is 30.
	assert_line_both "the ceiling failure names the LARGEST contributor, not just a filename" "$OUT$ERROUT" "largest contributor" "big.md"
	assert_line_both "the largest-contributor line carries that file's own size" "$OUT$ERROUT" "largest contributor" "30"
)

# ---------------------------------------------------------------------------
# 6. Usage errors and the environment skip.
# ---------------------------------------------------------------------------
echo "--- usage and skip ---"
(
	BLOCK=usage
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	nlines "$repo/CLAUDE.md" 5
	c="$TMPPARENT/c13.conf"
	conf "$c" 100

	nonrepo=$(mktemp -d "$TMPPARENT/plain.XXXXXX")
	run_budget "$nonrepo" --conf "$c"
	assert_eq "a non-git directory is a usage error (exit 2), not a skip" "2" "$RC"

	run_budget "$TMPPARENT/no-such-dir" --conf "$c"
	assert_eq "a --root that does not exist exits 2" "2" "$RC"

	run_budget "$repo" --conf "$c" --frobnicate
	assert_eq "an unknown flag exits 2" "2" "$RC"

	run_budget "$repo" --conf "$c" --ceiling notanumber
	assert_eq "a non-integer --ceiling exits 2" "2" "$RC"

	run_budget "$repo" --conf "$c" --allow "$TMPPARENT/no-such-allowlist"
	assert_eq "a missing --allow file exits 2 (symmetric with --conf)" "2" "$RC"

	# --help is a hardcoded line RANGE over this file's own header, so it
	# truncates silently every time the header grows. Pin both ends: a section
	# from the middle, and the header's last line.
	helpout=$(CLAUDE_CONFIG_DIR="$EMPTY_CFG" "$BUDGET" --help 2>&1)
	assert_contains "--help reaches the tracked-only policy section" "$helpout" "WHAT IT ENFORCES VS WHAT IT MERELY REPORTS"
	assert_contains "--help runs to the END of the header, not a stale line number" "$helpout" "skip asserts NOTHING"

	# The environment skip: 77 when git specifically is absent. A blanket
	# PATH=/nonexistent would also remove bash (the `#!/usr/bin/env bash`
	# shebang resolves it via PATH, exit 127) and every coreutil, so a 77 would
	# not be evidence about git. Build a PATH holding symlinks to everything the
	# resolver needs EXCEPT git.
	nogit="$TMPPARENT/nogit-bin"
	mkdir -p "$nogit"
	for _b in bash env grep sed awk wc cat sort comm mktemp rm dirname basename find head tr; do
		_p=$(command -v "$_b") && ln -sf "$_p" "$nogit/$_b"
	done
	assert_setup "the git-free PATH still has bash and grep" -x "$nogit/grep"
	out=$(PATH="$nogit" CLAUDE_CONFIG_DIR="$EMPTY_CFG" \
		"$BUDGET" --root "$repo" --conf "$c" 2>&1)
	rc=$?
	if [ "$rc" = "77" ]; then
		pass "git absent from PATH exits 77 (skip), not 0"
	else
		fail "git absent from PATH: want exit 77, got '$rc'. Output: $out"
	fi
)

# ---------------------------------------------------------------------------
# 7. Report shape: the in-tree / out-of-tree split and the per-file breakdown.
# ---------------------------------------------------------------------------
echo "--- report shape ---"
(
	BLOCK=report
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	nlines "$repo/imported.md" 12
	nlines "$repo/CLAUDE.md" 27
	prepend "@imported.md" "$repo/CLAUDE.md"
	track_ok "$repo" CLAUDE.md imported.md
	c="$TMPPARENT/c14.conf"
	conf "$c" 100

	cfgdir=$(mktemp -d "$TMPPARENT/cc.XXXXXX")
	# 61, deliberately NOT 28: with equal counts the in-tree per-file breakdown
	# could be absent entirely and the assertions below would match the
	# out-of-tree line instead.
	nlines "$cfgdir/CLAUDE.md" 61
	mkdir -p "$cfgdir/projects/p/memory"
	nlines "$cfgdir/projects/p/memory/MEMORY.md" 7
	export FIXTURE_CLAUDE_CONFIG_DIR="$cfgdir"

	run_budget "$repo" --conf "$c"
	assert_eq "exactly one ALWAYS-LOADED verdict line" "1" "$(printf '%s\n' "$OUT" | grep -c '^ALWAYS-LOADED: ')"
	assert_eq "the verdict line is on stdout, not stderr" "0" "$(printf '%s\n' "$ERROUT" | grep -c '^ALWAYS-LOADED: ')"
	assert_contains "an out-of-tree file appears in the report" "$OUT" "$cfgdir/CLAUDE.md"
	assert_contains "the out-of-tree memory index appears in the report" "$OUT" "MEMORY.md"
	assert_not_contains "no out-of-tree fallback to the developer's real \$HOME" "$OUT" "$HOME/.claude"

	# Requirement 4: a per-file breakdown, not just a total — on a PASSING run.
	# A ceiling that fires without saying which file grew is a ceiling people
	# disable, and a breakdown that only appears on failure is no help before it.
	assert_line_both "a passing run emits a per-file line for CLAUDE.md with its count" "$OUT" "CLAUDE.md" "28"
	assert_line_both "a passing run emits a per-file line for the @-imported file" "$OUT" "imported.md" "12"

	# Mutation along the axis: inflate the out-of-tree file massively. The
	# enforced verdict must not move.
	nlines "$cfgdir/CLAUDE.md" 10000
	run_budget "$repo" --conf "$c"
	assert_eq "a 10000-line out-of-tree file does not change the enforced verdict" "0" "$RC"
	unset FIXTURE_CLAUDE_CONFIG_DIR
)

# ---------------------------------------------------------------------------
# 8. Manifest tripwire. The injection MODEL is a claim about a harness version
#    we do not control; the tripwire is what survives the next version.
# ---------------------------------------------------------------------------
echo "--- manifest tripwire ---"
(
	BLOCK=manifest
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	wt=$(add_worktree "$repo" a)
	assert_setup "manifest worktree fixture exists" -d "$wt"
	nlines "$repo/CLAUDE.local.md" 7
	nlines "$wt/CLAUDE.md" 30
	nlines "$wt/CLAUDE.local.md" 5
	# 4 lines: this is also the [root] perspective's CLAUDE.md further down.
	nlines "$repo/CLAUDE.md" 4
	track_ok "$repo" CLAUDE.md CLAUDE.local.md
	track_wt_ok "$wt" CLAUDE.md CLAUDE.local.md
	c="$TMPPARENT/c15.conf"
	conf "$c" 1000
	man="$TMPPARENT/manifest15"
	printf 'CLAUDE.local.md\n<worktree>/CLAUDE.local.md\n<worktree>/CLAUDE.md\n' >"$man"

	run_budget "$wt" --conf "$c" --check-manifest "$man"
	assert_eq "a derived injection set matching the recorded manifest exits 0" "0" "$RC"

	printf 'CLAUDE.local.md\n<worktree>/CLAUDE.local.md\n<worktree>/CLAUDE.md\nAGENTS.md\n' >"$man"
	run_budget "$wt" --conf "$c" --check-manifest "$man"
	assert_eq "a manifest entry the resolver does not derive fails" "1" "$RC"
	assert_contains "the mismatch tells you to re-record the manifest" "$OUT$ERROUT" "re-record"

	# The recorded manifest is defaulted in only when measuring the repo it was
	# recorded against. A fixture tree is not that repo, so a plain run must not
	# silently check this repo's manifest against a foreign injection set —
	# otherwise every assertion in this suite is coupled to the real tree and the
	# file header's HARD BOUNDARY claim is false.
	run_budget "$wt" --conf "$c"
	assert_contains "a foreign tree is not checked against this repo's manifest" "$OUT$ERROUT" "manifest check: SKIPPED"
	assert_not_contains "and the skip is not dressed up as a pass" "$OUT$ERROUT" "matches the recorded manifest"

	# Two perspectives, selected by where --root points. Recording only one
	# false-fails the other, and the "re-record" remedy would then break the
	# first.
	sman="$TMPPARENT/manifest15s"
	printf '[worktree]\n<worktree>/CLAUDE.md\n<worktree>/CLAUDE.local.md\nCLAUDE.local.md\n[root]\nCLAUDE.md\nCLAUDE.local.md\n' >"$sman"
	run_budget "$wt" --conf "$c" --check-manifest "$sman"
	assert_eq "a sectioned manifest selects the [worktree] perspective from a worktree" "0" "$RC"
	run_budget "$repo" --conf "$c" --check-manifest "$sman"
	assert_eq "the same manifest selects the [root] perspective from the main checkout" "0" "$RC"
	printf '[worktree]\n<worktree>/CLAUDE.md\n<worktree>/CLAUDE.local.md\nCLAUDE.local.md\n' >"$sman"
	run_budget "$repo" --conf "$c" --check-manifest "$sman"
	assert_eq "an unrecorded perspective fails rather than passing unchecked" "1" "$RC"
	assert_contains "the unrecorded perspective is named" "$OUT$ERROUT" "no [root] section"

	printf 'CLAUDE.local.md\n<worktree>/CLAUDE.md\n' >"$man"
	run_budget "$wt" --conf "$c" --check-manifest "$man"
	assert_eq "a derived injection missing from the manifest fails" "1" "$RC"
	# Same line, not merely present in the output: the per-file breakdown above
	# names every injection, so a whole-output match here would pass vacuously
	# with the mismatch detail entirely absent (watched: mutation M28).
	assert_line_both "the missing-entry failure names the un-recorded injection" "$OUT$ERROUT" "derived but NOT recorded" "<worktree>/CLAUDE.local.md"
)

# ---------------------------------------------------------------------------
# 9. Tracked-only enforcement (QUM-1155). The ENFORCED set is the injected files
#    that are git-tracked in the SPRAWL ROOT's index. CLAUDE.local.md is
#    gitignored and per-user: it loads here and does not exist in a fresh clone,
#    so enforcing it makes `make validate` pass or fail as a function of whose
#    checkout ran it, and makes the recorded manifest list entries a clean clone
#    cannot derive. Untracked always-loaded files are still REPORTED with their
#    sizes and counted on the verdict line — excluded, never hidden.
# ---------------------------------------------------------------------------
echo "--- tracked-only enforcement ---"
(
	BLOCK=tracked
	# The real repo's shape in miniature: a tracked CLAUDE.md at both levels, an
	# untracked CLAUDE.local.md at both.
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	wt=$(add_worktree "$repo" a)
	assert_setup "worktree fixture exists" -d "$wt"
	nlines "$repo/CLAUDE.md" 11
	nlines "$repo/CLAUDE.local.md" 7
	nlines "$wt/CLAUDE.md" 30
	nlines "$wt/CLAUDE.local.md" 5
	c="$TMPPARENT/c16.conf"
	conf "$c" 1000
	track_ok "$repo" CLAUDE.md
	track_wt_ok "$wt" CLAUDE.md
	assert_setup "CLAUDE.local.md is deliberately NOT tracked at the root" -z "$(git -C "$repo" ls-files CLAUDE.local.md)"
	assert_setup "nor on the worktree branch — untracked in BOTH indexes" -z "$(git -C "$wt" ls-files CLAUDE.local.md)"

	run_budget "$wt" --conf "$c"
	# 30/1/2 do NOT discriminate on their own: a basename blocklist ("skip
	# anything called CLAUDE.local.md") — the most tempting wrong fix, given the
	# motivating bug — produces all three. Subshells 2 and 4 are what kill it,
	# via files that no name match would catch. Do not delete them thinking they
	# are variants of this one.
	assert_eq "untracked CLAUDE.local.md copies are excluded from the enforced total" "30" "$(verdict_field in_tree)"
	assert_eq "only the tracked injection is an enforced site" "1" "$(verdict_field injections)"
	assert_eq "the excluded copies are COUNTED on the verdict line, not dropped" "2" "$(verdict_field untracked)"
	# Path AND exclusion marker on ONE line. A size-only check cannot tell which
	# TABLE the row is in, so an implementation that subtracts the file from the
	# total while still rendering it as enforced would pass — and that report
	# contradicts its own verdict line, which is the state an operator hits.
	assert_line_both "the excluded worktree copy is marked NOT enforced where it is listed" "$OUT" "<worktree>/CLAUDE.local.md" "NOT enforced"
	assert_line_both "the excluded root copy is reported with its size, not vanished" "$OUT" "CLAUDE.local.md" "7"
	assert_contains "the report states the tracked-only policy" "$OUT" "GIT-TRACKED"
	assert_eq "an otherwise-clean tracked-only run exits 0" "0" "$RC"

	# The ceiling must be compared against the ENFORCED total, which is the whole
	# gate. 35 sits between the enforced 30 and the unfiltered 42: a resolver that
	# reports 30 but still compares 42 fails here and nowhere else in this block.
	cmid="$TMPPARENT/c16mid.conf"
	conf "$cmid" 35
	run_budget "$wt" --conf "$cmid"
	assert_eq "the ceiling is compared against the enforced total, not the raw one" "0" "$RC"
	assert_not_contains "and no over-ceiling diagnostic is printed" "$OUT$ERROUT" "over the ceiling"

	# The fresh-clone rc=1 this slice removes, reproduced as an assertion: a
	# manifest recording the untracked copies (which is what the pre-QUM-1155
	# resolver derived) must now FAIL, because a clean clone cannot derive them.
	man="$TMPPARENT/manifest16"
	printf '<worktree>/CLAUDE.md\n' >"$man"
	run_budget "$wt" --conf "$c" --check-manifest "$man"
	assert_eq "a tracked-only manifest matches" "0" "$RC"
	printf '<worktree>/CLAUDE.md\n<worktree>/CLAUDE.local.md\nCLAUDE.local.md\n' >"$man"
	run_budget "$wt" --conf "$c" --check-manifest "$man"
	assert_eq "a manifest recording untracked copies fails (the fresh-clone rc=1)" "1" "$RC"
	assert_line_both "and it names an entry a clean clone cannot derive" "$OUT$ERROUT" "recorded but NOT derived" "CLAUDE.local.md"
)
(
	BLOCK=tracked
	# Shadowing is a fact about the HARNESS; tracking is a policy about
	# ENFORCEMENT, and conflating them silently substitutes a file no agent
	# loads. An untracked NEARER CLAUDE.md must still shadow the tracked
	# ancestor — it is excluded from the total, not replaced by the ancestor.
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	wt=$(add_worktree "$repo" a)
	assert_setup "worktree fixture exists" -d "$wt"
	mkdir -p "$repo/.sprawl" "$repo/docs"
	nlines "$repo/CLAUDE.md" 40
	nlines "$repo/CLAUDE.local.md" 6
	nlines "$repo/docs/x.md" 5
	nlines "$repo/.sprawl/CLAUDE.md" 9 # NEARER than $repo/CLAUDE.md, and untracked
	c="$TMPPARENT/c17.conf"
	conf "$c" 1000
	# The tracked/untracked assignment is INVERTED from subshell 1 on purpose:
	# here CLAUDE.local.md is the tracked file and a CLAUDE.md is the untracked
	# one, so nothing in this scenario can be satisfied by a filename rule.
	track_ok "$repo" CLAUDE.md CLAUDE.local.md docs/x.md
	assert_setup "the nearer CLAUDE.md is untracked" -z "$(git -C "$repo" ls-files .sprawl/CLAUDE.md)"

	run_budget "$wt" --conf "$c"
	assert_eq "an untracked NEARER CLAUDE.md still shadows the tracked ancestor (6, not 46)" "6" "$(verdict_field in_tree)"
	assert_eq "the shadowing copy is counted as excluded, not silently dropped" "1" "$(verdict_field untracked)"
	assert_line_both "and it is reported with its size, not vanished" "$OUT" ".sprawl/CLAUDE.md" "9"

	# Per-machine determinism, the whole point: a mandated read inside an
	# untracked always-loaded file must NOT fire, or validate goes red on the one
	# machine that has the file and green everywhere else.
	printf 'Read `docs/x.md` first.\n' >"$repo/.sprawl/CLAUDE.md"
	run_budget "$wt" --conf "$c"
	assert_eq "a mandated read inside an untracked always-loaded file does not fire" "0" "$(verdict_field violations)"
	# Re-asserted: without it, an exclusion that broke on this path would make the
	# total 7 and BOTH assertions either side would still pass.
	assert_eq "and the shrunk untracked file is still excluded from the total" "6" "$(verdict_field in_tree)"
	assert_eq "and the run is clean" "0" "$RC"
)
(
	BLOCK=tracked
	# The floor must still refuse an empty ENFORCED set — but say which of the
	# two states it is, or the operator hunts a discovery bug that is not there.
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	nlines "$repo/CLAUDE.md" 40
	c="$TMPPARENT/c18.conf"
	conf "$c" 1000
	assert_setup "nothing in the fixture is tracked" -z "$(git -C "$repo" ls-files)"
	run_budget "$repo" --conf "$c"
	assert_eq "an all-untracked tree does NOT exit 0" "1" "$RC"
	assert_contains "the floor's existing wording is preserved" "$OUT$ERROUT" "resolved 0 in-tree injections"
	# On the SAME line as the floor message. An unscoped substring match would be
	# satisfied by the policy banner and the untracked= field, which this run
	# prints regardless — so it would stop discriminating the moment the feature
	# lands, which is precisely when it needs to.
	assert_line_both "and the floor message itself distinguishes an empty ENFORCED set from an empty tree" "$OUT$ERROUT" "resolved 0 in-tree injections" "untracked"
)
(
	BLOCK=tracked
	# An @-import from a tracked file to an untracked file is excluded too:
	# the enforced set is uniformly tracked-only, whatever route reached it.
	# This also pins that EDITING a tracked file does not un-track it — an
	# implementation keying off `git status --porcelain` would get this wrong.
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	nlines "$repo/CLAUDE.md" 11
	c="$TMPPARENT/c19.conf"
	conf "$c" 1000
	track_ok "$repo" CLAUDE.md
	nlines "$repo/local-notes.md" 50
	prepend "@local-notes.md" "$repo/CLAUDE.md"
	assert_setup "the import target is untracked" -z "$(git -C "$repo" ls-files local-notes.md)"

	run_budget "$repo" --conf "$c"
	assert_eq "an untracked @-import is excluded from the enforced total (12, not 62)" "12" "$(verdict_field in_tree)"
	assert_eq "the tracked base is still the only enforced site" "1" "$(verdict_field injections)"
	assert_contains "the import still RESOLVED — exclusion is not a walker failure" "$OUT" "@-imports resolved: 1"
	assert_eq "the excluded import is counted, not silently dropped" "1" "$(verdict_field untracked)"
	assert_line_both "the excluded import is reported with its size" "$OUT" "local-notes.md" "50"

	# Mutation on the axis, and the fresh-clone state: track the same file and it
	# comes back into the enforced set. Also pins that `untracked=0` is PRINTED on
	# a fully-tracked tree — verdict_field returns the whole line when a field is
	# absent, so an implementation that omits the field when zero is otherwise
	# invisible here, and zero is what a clean clone reports.
	track_ok "$repo" local-notes.md
	run_budget "$repo" --conf "$c"
	assert_eq "tracking the import folds it back into the enforced total" "62" "$(verdict_field in_tree)"
	assert_eq "a fully-tracked tree reports untracked=0, it does not omit the field" "0" "$(verdict_field untracked)"
)
(
	BLOCK=tracked
	# Tracked-ness is asked of the checkout the file was INJECTED FROM, not
	# globally of the sprawl root's index. This is the case the promotion into
	# `validate` exists for: an agent adds an always-loaded file ON ITS BRANCH and
	# runs validate IN ITS WORKTREE. The root index is main's, so a root-index-only
	# oracle answers "untracked" and the gate cannot fail on the budget bust the
	# agent just created — it would only fire after the merge, from the root
	# checkout, which is the one place nobody is looking.
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	nlines "$repo/CLAUDE.md" 11
	track_ok "$repo" CLAUDE.md
	wt=$(add_worktree "$repo" a)
	assert_setup "worktree fixture exists" -d "$wt"
	c="$TMPPARENT/c20.conf"
	conf "$c" 250

	nlines "$wt/big.md" 500
	printf '@big.md\n' >"$wt/CLAUDE.md"
	git -C "$wt" add -- big.md CLAUDE.md >/dev/null 2>&1
	git -C "$wt" commit -q -m 'branch adds a large always-loaded import' >/dev/null 2>&1
	assert_setup "big.md IS tracked on the branch" -n "$(git -C "$wt" ls-files big.md)"
	assert_setup "big.md is NOT in the root index (this is the whole point)" -z "$(git -C "$repo" ls-files big.md)"

	run_budget "$wt" --conf "$c"
	assert_eq "a branch-added always-loaded file is ENFORCED from its own worktree" "501" "$(verdict_field in_tree)"
	assert_eq "and it is an enforced injection site, not an excluded one" "2" "$(verdict_field injections)"
	assert_eq "nothing was diverted into the untracked bucket" "0" "$(verdict_field untracked)"
	assert_eq "so the gate FAILS on the budget bust the branch just created" "1" "$RC"
	assert_line_both "and names the branch-added file as the largest contributor" "$OUT$ERROUT" "largest contributor" "big.md"

	# The determinism property must SURVIVE this: a gitignored per-user file is
	# untracked in the worktree index too, so it stays excluded. If asking the
	# worktree re-enforced CLAUDE.local.md, we would be back to the fresh-clone
	# rc=1 this whole change removed.
	nlines "$wt/CLAUDE.local.md" 7
	printf 'CLAUDE.local.md\n' >"$repo/.gitignore"
	track_ok "$repo" .gitignore
	run_budget "$wt" --conf "$c"
	assert_eq "a gitignored per-user file is STILL excluded when asking the worktree" "501" "$(verdict_field in_tree)"
	assert_eq "and is still reported as excluded" "1" "$(verdict_field untracked)"
)
(
	BLOCK=tracked
	# "Excluded, never hidden" has to be true for the whole subtree. A TRACKED file
	# reached only through an untracked parent genuinely loads; excluding it from
	# the enforced total is right (its presence is per-machine, because its parent
	# is), but dropping it from every table and every counter is the report lying
	# about what it saw.
	repo=$(new_repo)
	assert_setup "repo fixture exists" -d "$repo/.git"
	nlines "$repo/CLAUDE.md" 10
	nlines "$repo/shared.md" 400
	track_ok "$repo" CLAUDE.md shared.md
	c="$TMPPARENT/c21.conf"
	conf "$c" 1000

	printf '@shared.md\n' >"$repo/CLAUDE.local.md" # untracked parent
	assert_setup "the parent is untracked" -z "$(git -C "$repo" ls-files CLAUDE.local.md)"
	assert_setup "but its import target IS tracked" -n "$(git -C "$repo" ls-files shared.md)"

	run_budget "$repo" --conf "$c"
	assert_eq "the tracked file under an untracked parent stays OUT of the total" "10" "$(verdict_field in_tree)"
	assert_line_both "but it is REPORTED with its size, not hidden" "$OUT" "shared.md" "400"
	assert_eq "and both it and its parent are counted as excluded" "2" "$(verdict_field untracked)"
	assert_contains "the walker still reports having resolved the import" "$OUT" "@-imports resolved: 1"

	# A dangling @-import inside an untracked file must NOT fail the run: that file
	# is per-machine, so failing on it re-creates the per-checkout verdict this
	# change exists to remove.
	printf '@nope-not-here.md\n' >"$repo/CLAUDE.local.md"
	run_budget "$repo" --conf "$c"
	assert_eq "a dangling import inside an untracked file does not fail the run" "0" "$RC"
)

# ---------------------------------------------------------------------------
TOTAL=$(wc -l <"$LEDGER" 2>/dev/null | tr -d ' ')
PASS=$(grep -c '^P ' "$LEDGER" 2>/dev/null)
FAIL=$(grep -c '^F ' "$LEDGER" 2>/dev/null)
echo
echo "=== always-loaded budget unit results: $PASS passed / $FAIL failed ==="
# Validate as integers BEFORE comparing: a non-integer makes bash's `-lt` print
# an error AND evaluate false, skipping both gates and exiting 0.
for _n in TOTAL PASS FAIL; do
	if ! [[ "${!_n}" =~ ^[0-9]+$ ]]; then
		echo "  FAIL: $_n is '${!_n}', not an integer — the ledger was unreadable, so neither the floor nor the failure gate could evaluate; refusing to report success" >&2
		exit 1
	fi
done

FLOOR_BROKEN=0
for spec in $EXPECT_BLOCKS; do
	name=${spec%%:*}
	min=${spec##*:}
	got=$(grep -c "^[PF] $name\$" "$LEDGER" 2>/dev/null)
	if ! [[ "$got" =~ ^[0-9]+$ ]] || [ "$got" -lt "$min" ]; then
		echo "  FAIL: block '$name' recorded $got assertions, expected at least $min — it died early and this run measured less than it claims" >&2
		FLOOR_BROKEN=1
	fi
done
if [ "$FLOOR_BROKEN" -ne 0 ]; then
	exit 1
fi
if [ "$FAIL" -gt 0 ]; then
	exit 1
fi
exit 0
