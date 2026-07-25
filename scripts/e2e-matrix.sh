#!/usr/bin/env bash
# scripts/e2e-matrix.sh — QUM-616 Wave 1 matrix-driven e2e harness driver.
# KEEP THIS DUMB. Discovers scripts/e2e-tests/*.sh rows, sources each in a
# subshell, applies preflights from test_metadata, runs test_run.
set -euo pipefail

# Resolve SCRIPT_DIR via parameter expansion so the driver works even when
# external utilities like dirname/cd are not on PATH (some unit tests scrub
# PATH to assert preflights fire before any heavy work).
_self="${BASH_SOURCE[0]}"
case "$_self" in
    /*) SCRIPT_DIR="${_self%/*}" ;;
    */*) SCRIPT_DIR="$PWD/${_self%/*}" ;;
    *) SCRIPT_DIR="$PWD" ;;
esac
REPO_ROOT="${SCRIPT_DIR%/*}"
LIB="$SCRIPT_DIR/lib/e2e-common.sh"
ROWS_DIR="$SCRIPT_DIR/e2e-tests"

discover_rows() {
    local f name names=()
    for f in "$ROWS_DIR"/*.sh; do
        [ -e "$f" ] || continue
        name=${f##*/}
        name=${name%.sh}
        names+=("$name")
    done
    # Bash globbing returns alphabetical order already; emit as-is.
    local n
    for n in "${names[@]}"; do
        printf '%s\n' "$n"
    done
}

# --- argument parsing (QUM-947) ----------------------------------------------
# Accepted forms:
#   e2e-matrix.sh                 -> run every discovered row
#   e2e-matrix.sh all             -> run every discovered row (must be the only arg)
#   e2e-matrix.sh --list          -> print row names, exit 0 (must be the only arg)
#   e2e-matrix.sh r1 r2 r3 ...    -> run exactly those rows, in the order given
#
# DENOMINATOR CONTRACT: the summary reports passed/REQUESTED, where REQUESTED is
# the number of rows selected — i.e. the argv count when explicit names are
# given. It can never be smaller than what the caller asked for. Before QUM-947
# this driver read only $1 and silently discarded $2..$N, so `e2e-matrix.sh a b c`
# ran ONE row and printed "Matrix: 1/1 passed": a false green in the harness the
# CLAUDE.md mandatory-test table depends on.
#
# Deliberate choices:
#
#  * Validate EVERY name up front and run nothing if any is bad (fail fast).
#    Rows are minutes-long live tmux/claude tests; a typo in arg 3 must not cost
#    a full run of args 1-2 before exiting 2. Every bad name is reported, not
#    just the first, so one re-run fixes them all.
#  * Duplicates are NOT deduplicated: `r1 r1` runs r1 twice and reports out of 2.
#    Collapsing them would shrink the denominator below the request, which is the
#    exact bug class above. Re-running a row is safe (per-row sandbox; the
#    needs_build_tags binary is named per row).
#  * `all` and `--list` are whole-invocation modes, not row names, so each must
#    be the ONLY argument — mixing can only double-run or silently drop an
#    argument. They are checked in EVERY position, not just $1, since "only $1
#    was ever looked at" is the original defect.
#  * Row names must be plain basenames. A bare `-r` readability test is satisfied
#    by e.g. `../lib/e2e-common`, which would make the driver source and execute
#    the shared library as though it were a row.
#  * Zero selected rows is an error, never a green "0/0 passed" — that is the
#    same false-green shape: reporting success for work not done.

if [ "$#" -eq 0 ]; then
    set -- all
fi

for arg in "$@"; do
    if [ "$arg" = "--list" ]; then
        if [ "$#" -ne 1 ]; then
            echo "error: --list must be the only argument" >&2
            exit 2
        fi
        discover_rows
        exit 0
    fi
done

for arg in "$@"; do
    if [ "$arg" = "all" ] && [ "$#" -ne 1 ]; then
        echo "error: 'all' must be the only argument (it already selects every row)" >&2
        echo "hint: pass 'all' by itself, or name only the rows you want" >&2
        exit 2
    fi
done

if [ "$1" = "all" ]; then
    mapfile -t selected < <(discover_rows)
    if [ "${#selected[@]}" -eq 0 ]; then
        echo "error: no rows discovered in $ROWS_DIR — refusing to report a vacuous pass" >&2
        exit 2
    fi
else
    selected=("$@")
    bad_count=0
    for arg in "$@"; do
        case "$arg" in
            '' | *[!A-Za-z0-9_-]*)
                echo "error: invalid row name '$arg' (expected a plain row name matching [A-Za-z0-9_-]+)" >&2
                bad_count=$((bad_count + 1))
                continue
                ;;
        esac
        if [ ! -r "$ROWS_DIR/$arg.sh" ]; then
            echo "error: unknown row '$arg' (no $ROWS_DIR/$arg.sh)" >&2
            bad_count=$((bad_count + 1))
        fi
    done
    if [ "$bad_count" -gt 0 ]; then
        echo "hint: run '$_self --list' to see the available rows" >&2
        exit 2
    fi
fi

# If sibling e2e-tests dir exists next to a fixture driver but lib is the
# original one in repo, fall back to repo lib if local sibling lib missing.
if [ ! -r "$LIB" ] && [ -r "$SCRIPT_DIR/lib/e2e-common.sh" ]; then
    LIB="$SCRIPT_DIR/lib/e2e-common.sh"
fi

run_row() {
    local name="$1"
    local row_file="$ROWS_DIR/$name.sh"
    (
        # shellcheck disable=SC1090
        . "$LIB"
        # shellcheck disable=SC1090
        . "$row_file"
        local meta
        meta=$(test_metadata 2>/dev/null || true)
        case " $meta " in
            *" needs_claude=1 "*) e2e_require_claude_or_skip "$name" ;;
        esac
        case " $meta " in
            *" needs_tmux=1 "*) e2e_require_tmux ;;
        esac
        case " $meta " in
            *" needs_jq=1 "*) e2e_require_jq ;;
        esac
        case " $meta " in
            *" needs_build_tags=sprawl_test "*)
                go build -tags sprawl_test -o "$REPO_ROOT/sprawl-matrix-$name" "$REPO_ROOT" >/dev/null
                export SPRAWL_BIN="$REPO_ROOT/sprawl-matrix-$name"
                ;;
        esac
        test_run
    )
}

pass_count=0
fail_count=0
requested=${#selected[@]}
failed_rows=""

# Echo the selection before running anything, so a truncated or unexpected
# selection is visible without having to reverse-engineer it from the summary.
echo "=== Matrix: running $requested row(s): ${selected[*]} ==="

for name in "${selected[@]}"; do
    if run_row "$name"; then
        echo "PASS $name"
        pass_count=$((pass_count + 1))
    else
        echo "FAIL $name"
        fail_count=$((fail_count + 1))
        failed_rows="$failed_rows $name"
    fi
done

# Denominator is `requested`, never a count derived from the loop above.
echo "=== Matrix: $pass_count/$requested passed ==="
if [ "$fail_count" -gt 0 ]; then
    echo "=== Matrix: failed rows:$failed_rows ===" >&2
    exit 1
fi
exit 0
