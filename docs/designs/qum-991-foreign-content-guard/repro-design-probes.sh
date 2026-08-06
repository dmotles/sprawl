#!/usr/bin/env bash
#
# Usage:  REPO_ROOT=/abs/path/to/a/sprawl/worktree bash repro-design-probes.sh
#
# QUM-991 design probes: measure the mechanics a new guard would depend on.
#   6a. `git add -f` of a gitignored path — is it visible to an index-side guard?
#   6b. already-tracked paths — what --diff-filter=A vs =AM sees on modify.
#   6c. `git diff --cached --numstat` binary signal vs a .gitattributes override.
#   6d. does a rejected `reset --hard` on main still clobber the working tree?
#   6e. how wide is "the whole repo" — the allow-list too-broad measurement.
#
# Exit-code semantics: this script MEASURES and PRINTS; it asserts nothing and
# always exits 0. Its output is the evidence — read it, do not gate on it. Each
# probe prints its own "=> CONCLUSION:" line. Section 6d is the source of the
# guard-main-ref working-tree finding (see repro-hook-coverage.sh 2b's note).
set -uo pipefail

REPO_ROOT="${REPO_ROOT:?REPO_ROOT=/abs/path/to/sprawl/worktree required}"
TMP_ROOT="$(mktemp -d /tmp/qum991-design.XXXXXX)"
cleanup() {
	case "$TMP_ROOT" in
	/tmp/qum991-design.*) rm -rf "$TMP_ROOT" ;;
	*) echo "REFUSING to clean unexpected path '$TMP_ROOT'" >&2 ;;
	esac
}
trap cleanup EXIT

note() { printf '\n=== %s\n' "$*"; }
R="$TMP_ROOT/repo"
mkdir -p "$R"
git -C "$R" init -q -b main
git -C "$R" config user.email probe@example.invalid
git -C "$R" config user.name probe
printf '*.log\n*.tfplan\n' >"$R/.gitignore"
printf 'tracked\n' >"$R/keep.txt"
printf 'old log line\n' >"$R/already.log"
git -C "$R" add -f .gitignore keep.txt already.log
git -C "$R" commit -qm seed --no-verify

note '6a. git add -f of a gitignored path — visible to an index-side guard?'
printf 'ACMEGLOBALCORP apply output\n' >"$R/apply4.log"
git -C "$R" add apply4.log 2>&1 | sed 's/^/  plain-add: /'
printf '  after plain add, staged=[%s]\n' "$(git -C "$R" diff --cached --name-only | tr '\n' ' ')"
git -C "$R" add -f apply4.log
printf '  after add -f,  staged=[%s]\n' "$(git -C "$R" diff --cached --name-only | tr '\n' ' ')"
printf '  --diff-filter=A: [%s]\n' "$(git -C "$R" diff --cached --name-only --diff-filter=A | tr '\n' ' ')"
echo '  => CONCLUSION: `git add -f` bypasses .gitignore but NOT the index, so an'
echo '     index-side (or tree-side) guard sees add -f exactly like a plain add.'
git -C "$R" reset -q
rm -f "$R/apply4.log"

note '6b. already-tracked path modified — what the diff-filters see'
printf 'old log line\nnew ACMEGLOBALCORP line\n' >"$R/already.log"
git -C "$R" add already.log
printf '  --diff-filter=A  (adds only):     [%s]\n' "$(git -C "$R" diff --cached --name-only --diff-filter=A | tr '\n' ' ')"
printf '  --diff-filter=AM (adds+mods):     [%s]\n' "$(git -C "$R" diff --cached --name-only --diff-filter=AM | tr '\n' ' ')"
echo '  => CONCLUSION: filter=A ignores modifications to an already-tracked'
echo '     forbidden-class path. A path-class deny-list keyed on =A would let a'
echo '     second commit to an existing *.log through; keyed on =AM it re-flags'
echo '     every legitimate edit to an already-accepted file forever.'
git -C "$R" reset -q
printf 'old log line\n' >"$R/already.log"

note '6c. binary numstat signal, and whether .gitattributes can defeat it'
python3 -c "open('$R/blob.bin','wb').write(b'\x00ACMEGLOBALCORP\x00'+bytes(range(256))*4)"
git -C "$R" add blob.bin
printf '  numstat (default):        [%s]\n' "$(git -C "$R" diff --cached --numstat | tr '\t' ' ' | tr '\n' ';')"
printf '  ls-files --eol:           [%s]\n' "$(git -C "$R" ls-files --eol blob.bin)"
printf '*.bin diff\n' >"$R/.gitattributes"
printf '  numstat (diff attr set):  [%s]\n' "$(git -C "$R" diff --cached --numstat | tr '\t' ' ' | tr '\n' ';')"
printf '  "+" content lines now:    %s\n' "$(git -C "$R" diff --cached --unified=0 --no-color --src-prefix=a/ --dst-prefix=b/ -- blob.bin | grep -c '^+[^+]' || true)"
echo '  => CONCLUSION: a `diff` gitattribute forces text treatment, so numstat'
echo '     stops reporting "-\t-". A guard keyed on the numstat "-\t-" signal is'
echo '     defeatable by a committed .gitattributes line. Prefer a direct NUL'
echo '     probe on the blob (git cat-file) if that matters; but note the same'
echo '     attribute would ALSO make guard-employer-leak start seeing the file.'
rm -f "$R/.gitattributes"
git -C "$R" reset -q
rm -f "$R/blob.bin"

note '6d. does a REJECTED `reset --hard` on main still clobber the working tree?'
S="$TMP_ROOT/resetrepo"
mkdir -p "$S"
git -C "$S" init -q -b main
git -C "$S" config user.email p@e.invalid
git -C "$S" config user.name p
echo v1 >"$S/f.txt"
git -C "$S" add f.txt
git -C "$S" commit -qm c1 --no-verify
echo v2 >"$S/f.txt"
git -C "$S" add f.txt
git -C "$S" commit -qm c2 --no-verify
echo "UNCOMMITTED-WORK" >"$S/dirty.txt"
ln -sf "$REPO_ROOT/scripts/guard-main-ref" "$S/.git/hooks/reference-transaction"
out=$(SPRAWL_AGENT_IDENTITY=probe git -C "$S" reset --hard HEAD~1 2>&1)
rc=$?
printf '  reset --hard HEAD~1 rc=%s (first line: %s)\n' "$rc" "$(printf '%s' "$out" | head -1)"
printf '  main still at: %s (%s)\n' "$(git -C "$S" rev-parse --short main)" "$(git -C "$S" log -1 --format=%s main)"
printf '  f.txt content now: %s   (was v2 before the reset)\n' "$(cat "$S/f.txt")"
printf '  untracked dirty.txt still present: %s\n' "$([ -f "$S/dirty.txt" ] && echo yes || echo NO)"

note '6e. ALLOW-LIST TOO-BROAD MEASUREMENT: top-level entries in the real tree'
(
	cd "$REPO_ROOT" || exit 0
	printf '  tracked top-level entries: %s\n' "$(git ls-files | awk -F/ '{print $1}' | sort -u | wc -l)"
	printf '  they are: %s\n' "$(git ls-files | awk -F/ '{print $1}' | sort -u | tr '\n' ' ')"
	printf '  tracked binary files (git grep -I complement, sample): %s\n' \
		"$(git ls-files | while read -r f; do
			if [ -f "$f" ] && ! git check-attr -z diff -- "$f" >/dev/null 2>&1; then :; fi
			case "$(file -b --mime-encoding "$f" 2>/dev/null)" in binary) echo "$f" ;; esac
		done | wc -l)"
	printf '  tracked *.log / tfplan* / *.tfplan / plan.out: %s\n' \
		"$(git ls-files '*.log' 'tfplan*' '*.tfplan' 'plan.out' | wc -l)"
)
echo
echo "done"
