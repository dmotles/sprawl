#!/usr/bin/env bash
# scripts/always-loaded-budget.sh — resolve the set of instruction files an
# agent in this repo unavoidably loads, and report the total against a ceiling.
#
# WHY THIS EXISTS
#
# The always-loaded line count was revised four times in one day — 768, 963,
# 1040, then 211 corrected to 170 — each revision found by someone checking what
# the previous figure had assumed, each moving the same direction. The last one
# happened inside a document arguing for line-count discipline, written from a
# sense of how long the file felt. That is one artifact type failing four times:
# a figure asserted in prose, in front of the thing it describes, with nothing
# that fails when it is wrong. This script is the replacement mechanism.
#
# A conf file holding the ceiling is STILL a hardcoded number and relocating the
# figure changes nothing on its own. What makes this not the same artifact is
# that the number is a BOUND THAT FAILS WHEN CROSSED, derived against a set the
# script resolves rather than one a human remembered.
#
# WHAT IT RESOLVES
#
#   * The nearest-ancestor CLAUDE.md, and EVERY CLAUDE.local.md from --root up
#     to the sprawl root. Those two rules differ, and the asymmetry is measured,
#     not assumed: three live agent context headers (weave at the repo root, two
#     managers in worktrees) show root CLAUDE.local.md loading alongside the
#     worktree copy while root CLAUDE.md does not load at all when a worktree
#     copy exists. `--check-manifest` is the tripwire for that model.
#   * Transitive @-imports, counted ONCE PER INJECTION PATH.
#   * INJECTED COPIES, not distinct files. CLAUDE.local.md exists at the repo
#     root and is copied into every agent worktree by .sprawl/config.yaml's
#     worktree.setup hook; both load. Counting distinct files hid that doubling
#     for the entire audit, and is the single error this script most exists to
#     prevent.
#   * Out-of-tree files ($CLAUDE_CONFIG_DIR/CLAUDE.md and the auto-memory index)
#     reported separately and EXCLUDED from the enforced total — they load, and
#     we do not control them.
#
# WHAT IT DELIBERATELY DOES NOT RESOLVE
#
# Prose read-instructions are not detected by understanding them. Detecting
# "this sentence tells an agent to go read that file" is natural-language
# classification, and this repo already built and rejected a deterministic prose
# parser for a structurally identical problem (see /testing-practices, "The
# non-asserting fallback"): it acquired four blind spots of the same class it
# was built to detect, one blinding 462 lines across five harnesses while every
# aggregate counter stayed byte-identical. Instead the CONSTRUCTION is banned
# and the ban is enforced lexically — see check_reads() below.
#
# Exit: 0 within budget and no violations · 1 over ceiling, a violation, an
# unresolvable import, a manifest mismatch, or zero injections resolved ·
# 2 usage or config error · 77 a required external tool is absent (skip, and a
# skip asserts NOTHING).

set -uo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

die_usage() {
	echo "error: $1" >&2
	echo "usage: always-loaded-budget.sh [--root DIR] [--conf FILE] [--ceiling N] [--allow FILE] [--check-manifest FILE] [--no-manifest]" >&2
	exit 2
}

if ! command -v git >/dev/null 2>&1; then
	# 77, not 0. The exit status is the only signal `make` and non-reading
	# callers see, and a skip asserts nothing (QUM-952).
	echo "SKIP: git is not on PATH — cannot resolve the always-loaded set; NOTHING was measured" >&2
	exit 77
fi

ROOT=""
CONF="$SCRIPT_DIR/always-loaded-budget.conf"
ALLOW="$SCRIPT_DIR/always-loaded-budget.allow"
CEILING=""
MANIFEST=""
NO_MANIFEST=0
# The recorded manifest describes THIS repo's injection set, so it is checked by
# DEFAULT whenever we are measuring this repo (see the SPRAWL_ROOT comparison
# below). Absence is trivially true: if an expected always-loaded file is
# renamed, moved or deleted, a resolver without this precondition reports a
# smaller total and exits green, and that reads identically to a real pass.
# Every recorded entry must be derived, or the run fails.
DEFAULT_MANIFEST="$SCRIPT_DIR/testdata/always-loaded-manifest.observed"

while [ $# -gt 0 ]; do
	case "$1" in
	--root)
		ROOT=${2:-}
		[ -n "$ROOT" ] || die_usage "--root needs a non-empty value"
		shift 2 || die_usage "--root needs a value"
		;;
	--conf)
		CONF=${2:-}
		shift 2 || die_usage "--conf needs a value"
		;;
	--allow)
		ALLOW=${2:-}
		shift 2 || die_usage "--allow needs a value"
		;;
	--ceiling)
		CEILING=${2:-}
		shift 2 || die_usage "--ceiling needs a value"
		;;
	--check-manifest)
		MANIFEST=${2:-}
		shift 2 || die_usage "--check-manifest needs a value"
		;;
	--no-manifest)
		NO_MANIFEST=1
		shift
		;;
	-h | --help)
		sed -n '2,53p' "${BASH_SOURCE[0]}"
		exit 0
		;;
	*) die_usage "unknown argument '$1'" ;;
	esac
done

[ -n "$ROOT" ] || ROOT=$(git rev-parse --show-toplevel 2>/dev/null) ||
	die_usage "no --root given and the current directory is not a git repository"
[ -d "$ROOT" ] || die_usage "--root '$ROOT' is not a directory"
ROOT=$(cd "$ROOT" && pwd)

git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1 ||
	die_usage "--root '$ROOT' is not inside a git repository"

# The sprawl root is the parent of the COMMON git dir, so this is correct from a
# worktree and from the main checkout alike — derived, where "worktrees live at
# <root>/.sprawl/worktrees/" would be asserted.
GIT_COMMON=$(cd "$ROOT" && cd "$(git rev-parse --git-common-dir)" && pwd)
SPRAWL_ROOT=$(dirname "$GIT_COMMON")

# Default the manifest in when we are measuring the repo the manifest was
# recorded against — never when pointed at some other tree, whose injection set
# it says nothing about.
OWN_COMMON=$(cd "$SCRIPT_DIR" && cd "$(git rev-parse --git-common-dir 2>/dev/null || echo .)" && pwd)
if [ -z "$MANIFEST" ] && [ "$NO_MANIFEST" -eq 0 ] && [ "$SPRAWL_ROOT" = "$(dirname "$OWN_COMMON")" ]; then
	# Deleting the recorded manifest must not silently disable the tripwire —
	# that is the same absence-is-trivially-true failure one level up, and it
	# would look exactly like a clean run. Opting out is explicit or not at all.
	[ -f "$DEFAULT_MANIFEST" ] ||
		die_usage "the recorded manifest '$DEFAULT_MANIFEST' is missing; re-record it from a live agent's context header, or pass --no-manifest to measure without the tripwire"
	MANIFEST="$DEFAULT_MANIFEST"
fi

# ---------------------------------------------------------------------------
# Ceiling
# ---------------------------------------------------------------------------
if [ -n "$CEILING" ]; then
	[[ "$CEILING" =~ ^[0-9]+$ ]] || die_usage "--ceiling must be a non-negative integer, got '$CEILING'"
else
	[ -f "$CONF" ] || die_usage "ceiling config '$CONF' not found (and no --ceiling given)"
	# shellcheck disable=SC1090
	CEILING=$(sed -n 's/^[[:space:]]*CEILING[[:space:]]*=[[:space:]]*\([^[:space:]#]*\).*/\1/p' "$CONF" | tail -1)
	[ -n "$CEILING" ] || die_usage "no CEILING= in '$CONF'"
	[[ "$CEILING" =~ ^[0-9]+$ ]] || die_usage "CEILING in '$CONF' must be a non-negative integer, got '$CEILING'"
fi

[ -f "$ALLOW" ] || die_usage "mention allowlist '$ALLOW' not found"

WORK=$(mktemp -d) || {
	echo "error: could not create a working directory" >&2
	exit 2
}
case "$WORK" in
/tmp/* | /var/folders/*) ;;
*)
	echo "error: refusing to run with a work dir outside /tmp ('$WORK')" >&2
	exit 2
	;;
esac
trap 'rm -rf "$WORK"' EXIT

TRACKED="$WORK/tracked"
# NOT `|| : >"$TRACKED"`. Swallowing this error leaves the tracked list empty,
# which makes is_tracked() always false, which makes BOTH read-check legs
# structurally unable to fire — and the run still reports green. Same argument
# as the discovery floor below: an instrument blind to its target must refuse to
# report rather than report nothing found.
if ! git -C "$SPRAWL_ROOT" ls-files >"$TRACKED" 2>"$WORK/ls-files.err"; then
	echo "error: 'git ls-files' failed in '$SPRAWL_ROOT' — the read-instruction check would be structurally unable to fire, so this run is refused rather than reported green:" >&2
	sed 's/^/  /' "$WORK/ls-files.err" >&2
	exit 1
fi

# is_tracked PATH — tracked in the SPRAWL ROOT's index. Not the worktree's: the
# paths named inside an always-loaded file are repo-relative, and a worktree's
# index is an accident of when it branched.
is_tracked() { grep -qxF -- "$1" "$TRACKED"; }

# is_allowlisted PATH — literal comparison against the first whitespace-
# delimited field of each non-comment allowlist line. Deliberately NOT a regex
# built from the path: a metacharacter in a filename would silently widen the
# match, and a too-wide allowlist entry is a check that stops checking.
ALLOWED="$WORK/allowed"
awk '{sub(/#.*/, "")} NF {print $1}' "$ALLOW" >"$ALLOWED"
is_allowlisted() { grep -qxF -- "$1" "$ALLOWED"; }

# norm PATH — display/manifest form: relative to the sprawl root, with the
# worktree under test rendered as the literal token <worktree> so a manifest
# recorded by one agent is comparable against another's.
norm() {
	local p=$1
	if [ "$ROOT" != "$SPRAWL_ROOT" ] && [ "${p#"$ROOT"/}" != "$p" ]; then
		echo "<worktree>/${p#"$ROOT"/}"
	elif [ "${p#"$SPRAWL_ROOT"/}" != "$p" ]; then
		echo "${p#"$SPRAWL_ROOT"/}"
	else
		echo "$p"
	fi
}

# count_lines FILE — awk, not `wc -l`: wc counts newlines, so it drops a final
# line that has no trailing newline.
count_lines() { awk 'END{print NR+0}' "$1"; }

# ---------------------------------------------------------------------------
# Injection resolution
# ---------------------------------------------------------------------------
INJ="$WORK/injections" # lines \t words \t chars \t normpath \t realpath \t kind
: >"$INJ"
IMPORTS_RESOLVED=0
RESOLVE_ERR=""
OOT_IMPORTS="$WORK/oot-imports"
: >"$OOT_IMPORTS"

record() {
	# $1 realpath, $2 kind (base|import)
	local f=$1 kind=$2 l w c
	l=$(count_lines "$f")
	w=$(wc -w <"$f" | tr -d ' ')
	c=$(wc -c <"$f" | tr -d ' ')
	printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$l" "$w" "$c" "$(norm "$f")" "$f" "$kind" >>"$INJ"
}

# walk_imports FILE CHAIN — record FILE's transitive @-imports. CHAIN is the
# newline-separated stack of files already open on THIS path; a file already in
# the chain is a cycle and is skipped without being counted. A file reachable by
# two distinct paths (a diamond) is counted for each, because each is a separate
# injection.
walk_imports() {
	local file=$1 chain=$2 dir tok target
	dir=$(dirname "$file")
	while IFS= read -r tok; do
		[ -n "$tok" ] || continue
		case "$tok" in
		'~/'*) target="$HOME/${tok#\~/}" ;;
		/*) target="$tok" ;;
		*) target="$dir/$tok" ;;
		esac
		if [ ! -f "$target" ]; then
			RESOLVE_ERR="$RESOLVE_ERR
  $(norm "$file") @-imports '$tok', which does not resolve to a file (looked at $target)"
			continue
		fi
		target=$(cd "$(dirname "$target")" && printf '%s/%s' "$(pwd)" "$(basename "$target")")
		case "
$chain
" in
		*"
$target
"*) continue ;; # cycle on this path
		esac
		IMPORTS_RESOLVED=$((IMPORTS_RESOLVED + 1))
		# An import that resolves OUTSIDE the sprawl root (`@~/...`, `@/abs/...`)
		# loads, but is not ours to edit and has no portable manifest form — same
		# class as the user-global CLAUDE.md. Reported, never enforced.
		if [ "${target#"$SPRAWL_ROOT"/}" = "$target" ]; then
			printf '%s\t%s\t%s\n' "$(count_lines "$target")" "$target" "@-imported by $(norm "$file")" >>"$OOT_IMPORTS"
			continue
		fi
		record "$target" import
		walk_imports "$target" "$chain
$target"
		# The import token grammar is WHITESPACE-DELIMITED by design: `@a b.md`
		# is one token `a` and hard-fails as unresolvable. Failing loud on a
		# space beats guessing where the path ends, and guessing is how a
		# lexical check turns into a parser.
	done < <(grep -oE '(^|[[:space:]])@[^[:space:]]+' "$file" 2>/dev/null | sed 's/^[[:space:]]*@//')
}

add_injection() {
	local f=$1
	record "$f" base
	walk_imports "$f" "$f"
}

# Ancestor chain: --root first, then up to and including the sprawl root.
DIRS=()
d=$ROOT
while :; do
	DIRS+=("$d")
	[ "$d" = "$SPRAWL_ROOT" ] && break
	[ "$d" = "/" ] && break
	nd=$(dirname "$d")
	[ "$nd" = "$d" ] && break
	d=$nd
done

# CLAUDE.md: NEAREST ancestor only.
for d in "${DIRS[@]}"; do
	if [ -f "$d/CLAUDE.md" ]; then
		add_injection "$d/CLAUDE.md"
		break
	fi
done
# CLAUDE.local.md: accumulates at EVERY level.
for d in "${DIRS[@]}"; do
	[ -f "$d/CLAUDE.local.md" ] && add_injection "$d/CLAUDE.local.md"
done

INJECTIONS=$(wc -l <"$INJ" | tr -d ' ')
IN_TREE=$(awk -F'\t' '{s+=$1} END{print s+0}' "$INJ")

# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------
echo "always-loaded instruction budget"
echo "  cwd:          $(pwd)"
echo "  --root:       $ROOT"
echo "  sprawl root:  $SPRAWL_ROOT"
echo "  HEAD:         $(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo '(none)')"
echo "  dirty files:  $(git -C "$ROOT" status --porcelain 2>/dev/null | wc -l | tr -d ' ')"
echo
echo "in-tree injections (enforced) — LINES WORDS CHARS INJECTION:"
if [ "$INJECTIONS" -gt 0 ]; then
	awk -F'\t' '{printf "  %6d %7d %8d  %s%s\n", $1, $2, $3, $4, ($6=="import" ? " (@-import)" : "")}' "$INJ"
fi
echo "  @-imports resolved: $IMPORTS_RESOLVED"
echo "  in-tree total: $IN_TREE lines across $INJECTIONS injections"

# Discovery floor. A resolver that finds nothing must refuse to report: a
# plausible-looking total produced by an instrument blind to its target is the
# whole failure family this script replaces.
if [ "$INJECTIONS" -eq 0 ]; then
	echo
	echo "ALWAYS-LOADED: FAIL in_tree=0 ceiling=$CEILING violations=0 injections=0"
	echo "error: resolved 0 in-tree injections under '$ROOT' — refusing to report a budget the instrument could not have measured" >&2
	exit 1
fi

if [ -n "$RESOLVE_ERR" ]; then
	echo
	echo "ALWAYS-LOADED: FAIL in_tree=$IN_TREE ceiling=$CEILING violations=0 injections=$INJECTIONS"
	echo "error: unresolvable @-import(s):$RESOLVE_ERR" >&2
	exit 1
fi

# Out-of-tree: reported, never enforced. They load and we do not control them.
CFGDIR=${CLAUDE_CONFIG_DIR:-$HOME/.claude}
echo
echo "out-of-tree (reported, NOT enforced) — LINES  FILE:"
OOT_FOUND=0
if [ -f "$CFGDIR/CLAUDE.md" ]; then
	echo "  $(count_lines "$CFGDIR/CLAUDE.md")  $CFGDIR/CLAUDE.md"
	OOT_FOUND=1
fi
while IFS= read -r m; do
	[ -n "$m" ] || continue
	# No prose statistic here. A measured-elsewhere figure printed on every run
	# is unfalsifiable by this tool and is the artifact type it exists to
	# replace; the accuracy finding lives in the doc, which can carry its
	# derivation rule.
	echo "  $(count_lines "$m")  $m  [auto-memory index; its linked satellites are NOT injected]"
	OOT_FOUND=1
done < <(find "$CFGDIR/projects" -maxdepth 3 -name MEMORY.md -type f 2>/dev/null | sort)
if [ -s "$OOT_IMPORTS" ]; then
	awk -F'\t' '{printf "  %s  %s  [%s]\n", $1, $2, $3}' "$OOT_IMPORTS"
	OOT_FOUND=1
fi
[ "$OOT_FOUND" -eq 1 ] || echo "  (none found under $CFGDIR)"
if [ ! -s "$TRACKED" ]; then
	# Legitimate for a fresh repo, so not a failure — but never silent, because
	# it is the state in which the read-instruction check cannot fire.
	echo "  note: 0 tracked files in $SPRAWL_ROOT — the read-instruction check cannot fire against this tree"
fi
if [ -f "$SPRAWL_ROOT/.claude/settings.json" ] && grep -q claudeMdExcludes "$SPRAWL_ROOT/.claude/settings.json" 2>/dev/null; then
	echo "  note: .claude/settings.json configures claudeMdExcludes; three independent context-header observations show the user-global CLAUDE.md loading anyway. Recorded, not chased."
fi

# ---------------------------------------------------------------------------
# The read-instruction ban.
#
#   An always-loaded file may @-import another file, or point at on-demand
#   material (a skill, a docs/ page). It may NOT mandate a read of a file it
#   does not import.
#
# THIS IS A NARROW LEXICAL CHECK OVER A HANDFUL OF FILES, NOT NATURAL-LANGUAGE
# PARSING, AND IT MUST STAY THAT WAY. Do not "improve" it into a general prose
# parser that decides whether a sentence means "go read this" — that is the
# artifact /testing-practices records as built, blind in four ways, and
# rejected. Both legs below are pure token matching:
#
#   (a) mention — a git-tracked .md named in backticks and not @-imported.
#       Deliberately broader than the rule: it fires on pointers too, and the
#       allowlist is where a human writes down "this is a pointer, not a
#       mandate". That direction is chosen on purpose — over-firing costs one
#       allowlist line, under-firing ships a nondeterministic instruction
#       surface that nothing downstream can distinguish.
#   (b) mandate — an imperative read verb IMMEDIATELY followed by a backticked
#       git-tracked path of any extension. The adjacency is load-bearing: real
#       CLAUDE.md lines contain the word "read" and an unrelated backticked
#       tracked path far apart on the same line, and a line-scoped rule fires on
#       those. Skill pointers (`/testing-practices`) need no special case — they
#       are not tracked paths.
# ---------------------------------------------------------------------------
VIOLATIONS="$WORK/violations"
: >"$VIOLATIONS"

check_reads() {
	local file=$1 norm_file=$2 imported=$3 lineno line tok
	lineno=0
	while IFS= read -r line; do
		lineno=$((lineno + 1))

		# (a) mention of a tracked .md
		while IFS= read -r tok; do
			[ -n "$tok" ] || continue
			case "$tok" in *.md) ;; *) continue ;; esac
			is_tracked "$tok" || continue
			grep -qxF -- "$tok" "$imported" && continue
			is_allowlisted "$tok" && continue
			echo "$norm_file:$lineno	$tok	names a tracked .md it does not @-import" >>"$VIOLATIONS"
		done < <(printf '%s\n' "$line" | grep -oE '`[^`]+`' | tr -d '`')

		# (b) imperative read verb immediately followed by a backticked path
		while IFS= read -r tok; do
			[ -n "$tok" ] || continue
			is_tracked "$tok" || continue
			grep -qxF -- "$tok" "$imported" && continue
			echo "$norm_file:$lineno	$tok	mandates a read of a tracked path it does not @-import" >>"$VIOLATIONS"
		done < <(printf '%s\n' "$line" |
			grep -oEi '(read|re-read|consult)( +the)? +`[^`]+`' |
			sed -E 's/.*`([^`]*)`$/\1/')
	done <"$file"
}

# The imported set, as repo-relative paths, so leg (a)/(b) can exempt them.
IMPORTED="$WORK/imported"
awk -F'\t' '$6=="import"{print $5}' "$INJ" | while IFS= read -r p; do
	echo "${p#"$SPRAWL_ROOT"/}"
	[ "$ROOT" != "$SPRAWL_ROOT" ] && echo "${p#"$ROOT"/}"
done >"$IMPORTED"

while IFS=$'\t' read -r _l _w _c np rp _k; do
	check_reads "$rp" "$np" "$IMPORTED"
done <"$INJ"

# Dedup per (file:line, target): both legs can match the same mandate, and that
# is one problem to fix, not two.
sort -u -t$'\t' -k1,2 "$VIOLATIONS" -o "$VIOLATIONS"
NVIOL=$(wc -l <"$VIOLATIONS" | tr -d ' ')

if [ "$NVIOL" -gt 0 ]; then
	echo
	echo "read-instruction violations (an always-loaded file may @-import, or point at"
	echo "on-demand material, but may not mandate a read of a file it does not import):"
	while IFS=$'\t' read -r where target why; do
		extra=""
		if [ -f "$SPRAWL_ROOT/$target" ]; then
			extra=" [+$(count_lines "$SPRAWL_ROOT/$target") lines if obeyed]"
		fi
		echo "  $where $why: \`$target\`$extra"
	done <"$VIOLATIONS"
fi

# ---------------------------------------------------------------------------
# Manifest tripwire. The injection MODEL is a claim about a harness version we
# do not control; a check that fires when the injected set changes SHAPE
# survives the next version, which a model of why it currently has this shape
# does not.
# ---------------------------------------------------------------------------
MANIFEST_FAIL=0
if [ -z "$MANIFEST" ]; then
	# Never silent. A disengaged tripwire that prints nothing is indistinguishable
	# from a tripwire that passed.
	echo
	if [ "$NO_MANIFEST" -eq 1 ]; then
		echo "manifest check: SKIPPED (--no-manifest) — the injected set was not compared against any recording"
	else
		echo "manifest check: SKIPPED — --root '$ROOT' is not this script's own repo, and a manifest recorded elsewhere says nothing about this tree"
	fi
else
	[ -f "$MANIFEST" ] || die_usage "--check-manifest file '$MANIFEST' not found"
	# Two legitimate perspectives exist and both are observed: an agent in a
	# worktree loads <worktree>/CLAUDE.md plus BOTH CLAUDE.local.md copies, while
	# weave at the main checkout loads the root pair. Recording only one makes the
	# other false-fail, and the printed "re-record" remedy would then break the
	# first — a ping-pong between two correct readings, after which the next
	# operator concludes the tripwire is noise. So the manifest carries a
	# [worktree] and a [root] section and we select on where --root points. A
	# section-less manifest is taken whole, which is what the fixtures use.
	if [ "$ROOT" = "$SPRAWL_ROOT" ]; then MSECTION=root; else MSECTION=worktree; fi
	awk -F'\t' '{print $4}' "$INJ" | sort >"$WORK/derived"
	if grep -qE '^\[[a-z]+\]$' "$MANIFEST"; then
		awk -v want="[$MSECTION]" '
			/^\[[a-z]+\]$/ { on = ($0 == want); next }
			on' "$MANIFEST" | grep -vE '^[[:space:]]*(#|$)' | sed 's/[[:space:]]*$//' | sort >"$WORK/recorded"
		if [ ! -s "$WORK/recorded" ]; then
			echo
			echo "error: manifest '$MANIFEST' has no [$MSECTION] section — the perspective being measured has never been recorded, so this run cannot be checked" >&2
			exit 1
		fi
	else
		grep -vE '^[[:space:]]*(#|$)' "$MANIFEST" | sed 's/[[:space:]]*$//' | sort >"$WORK/recorded"
	fi
	echo
	echo "manifest check against $MANIFEST [$MSECTION perspective]:"
	if diff -u "$WORK/recorded" "$WORK/derived" >"$WORK/mdiff"; then
		echo "  derived injection set matches the recorded manifest"
	else
		MANIFEST_FAIL=1
		comm -13 "$WORK/recorded" "$WORK/derived" | sed 's/^/  derived but NOT recorded: /'
		comm -23 "$WORK/recorded" "$WORK/derived" | sed 's/^/  recorded but NOT derived (renamed, moved or deleted — absence is trivially true, so this is a FAILURE, not a smaller budget): /'
		echo "  the injected set changed shape — confirm against a live agent's context header, then re-record $MANIFEST" >&2
	fi
fi

# ---------------------------------------------------------------------------
# Verdict. Exactly one line matching ^ALWAYS-LOADED: , on stdout.
# ---------------------------------------------------------------------------
STATUS=OK
RC=0
if [ "$IN_TREE" -gt "$CEILING" ] || [ "$NVIOL" -gt 0 ] || [ "$MANIFEST_FAIL" -ne 0 ]; then
	STATUS=FAIL
	RC=1
fi

if [ "$IN_TREE" -gt "$CEILING" ]; then
	echo
	echo "over the ceiling by $((IN_TREE - CEILING)) lines. A ceiling that fires without saying"
	echo "which file grew is a ceiling people disable, so:"
	awk -F'\t' 'NR==1||$1>m{m=$1;p=$4} END{printf "  largest contributor: %s (%d lines)\n", p, m}' "$INJ"
fi

echo
echo "ALWAYS-LOADED: $STATUS in_tree=$IN_TREE ceiling=$CEILING violations=$NVIOL injections=$INJECTIONS"
exit "$RC"
