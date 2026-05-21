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

arg="${1:-all}"

if [ "$arg" = "--list" ]; then
    discover_rows
    exit 0
fi

if [ "$arg" = "all" ]; then
    mapfile -t selected < <(discover_rows)
else
    if [ ! -r "$ROWS_DIR/$arg.sh" ]; then
        echo "error: unknown row '$arg' (no $ROWS_DIR/$arg.sh)" >&2
        exit 2
    fi
    selected=("$arg")
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
total=${#selected[@]}

for name in "${selected[@]}"; do
    if run_row "$name"; then
        echo "PASS $name"
        pass_count=$((pass_count + 1))
    else
        echo "FAIL $name"
        fail_count=$((fail_count + 1))
    fi
done

echo "=== Matrix: $pass_count/$total passed ==="
if [ "$fail_count" -gt 0 ]; then
    exit 1
fi
exit 0
