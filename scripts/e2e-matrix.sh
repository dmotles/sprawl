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
#
# --- skip accounting (QUM-952) -----------------------------------------------
# A skipped row used to `exit 0` (scripts/lib/e2e-common.sh), which this driver
# read as a pass: with SPRAWL_E2E_SKIP_NO_CLAUDE=1 and no claude on PATH, all 33
# rows printed "Matrix: 33/33 passed" and exited 0 while asserting nothing.
# CLAUDE.md *instructs* agents to set that variable, so the mandatory-gate
# harness was permanently green. A skip is now its own bucket, signalled by
# rc 77 plus a reason recorded in the $E2E_SKIP_FILE sentinel, and classified
# strictly in this precedence order:
#
#   | row rc | sentinel   | verdict                                       |
#   |--------|------------|-----------------------------------------------|
#   | 0      | no reason  | PASS                                          |
#   | 0      | has reason | INTERNAL ERROR (exit 4) — never a PASS         |
#   | 77     | has reason | SKIP                                          |
#   | 77     | no reason  | FAIL — a crash cannot forge a skip            |
#   | other  | any        | FAIL — rc wins over the sentinel               |
#
# "has reason" means the sentinel contains at least one line with a
# non-whitespace character — NOT merely that the file has bytes. `[ -s ]` is a
# byte count, so a bare newline passes it; see the scan in the loop below.
#
# The two unreachable rows are treated DIFFERENTLY ON PURPOSE. The principle is
# missing corroboration vs contradiction:
#
#  * rc 77 with no reason is a MISSING corroboration. rc 77 is only the row's
#    *claim* to have skipped; the sentinel is the evidence. With no evidence we
#    distrust the claim and FAIL. That also fails safe — the cost is re-running
#    a row, never believing an unvalidated one — and a skip whose reason is
#    blank is a row nobody can triage, i.e. an entry that looks accounted-for
#    and isn't, which is a small instance of this very bug.
#  * rc 0 with a reason is a CONTRADICTION between two signals from the same
#    row, and nothing can adjudicate it. Refusing to classify (exit 4) is the
#    only honest response: it is QUM-952 one level up — "the row believed it
#    skipped, the driver called it a pass" — and counting it as a pass would
#    keep the bucket sum intact while hiding the mistake.
#
# SCRAPING THE OUTPUT: several lines share the `=== Matrix: ` prefix — the
# QUM-947 selection banner (`running N row(s)`), the canonical summary, and the
# failed-rows / skipped-rows lines. So that prefix alone does NOT identify the
# summary. Anchor on the full shape instead: `^=== Matrix: [0-9]+/[0-9]+ passed
# ===$` for the canonical summary (exactly one per run), and
# `^=== Matrix breakdown: ` for the three-bucket line.
#
# EXIT CODES:
#   0   every requested row actually executed and passed
#   1   at least one row failed (dominates skips)
#   2   usage / argument error — no row ran
#   3   at least one row skipped, none failed
#   4   internal invariant violation (bucket sum, or the rc-0+sentinel row)
#   5   environment unfit (QUM-1118) — a disk-space precondition failed, either
#       before any row ran or mid-run between rows. Distinct from BOTH 3 (skip:
#       nothing measured, and that's fine) and 1 (a row genuinely failed): here
#       nothing was measured and that is NOT acceptable. Never downgrade this to
#       3 or 1 to "simplify" scraping — that is precisely the vacuity this exists
#       to end.
#   77  reserved as a ROW's skip signal; never this driver's own exit status
#
# Why 3 rather than 0 on a partial skip: this driver is the mandatory gate the
# CLAUDE.md touched-file table points at, and its exit status is the only signal
# `make` and any non-reading caller sees. SPRAWL_E2E_SKIP_NO_CLAUDE=1
# acknowledges the *diagnostic* ("don't hard-fail with a confusing FATAL"), not
# the *obligation* — a row that ran nothing has not validated anything, so
# `make test-e2e-matrix-<row>` must not report success for it. Do NOT add
# `|| true` or a `-` prefix to the Makefile recipes to work around this: that
# would re-hide real failures too.

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

# QUM-1118: fail loudly and DISTINCTLY (not a skip, not a row failure) before
# any row runs if the environment cannot host the run — see the 2026-08-06
# incident this exists to prevent, where 13 of 19 rows died inside `go build`
# with ENOSPC and were reported as ordinary FAILs. e2e_check_disk_space is
# re-invoked at the top of the per-row loop too, so exhaustion arising mid-run
# is reported through this exact same path rather than surfacing as an
# unrelated cascade of row failures.
#
# Run in its OWN subshell — deliberately never sourced into the driver's own
# top-level namespace — and its exit status propagated explicitly.
# e2e-common.sh's re-source guard (_E2E_COMMON_SH) and its sibling fault-
# ledger lib's per-owner tracking vars are plain shell variables; sourcing
# $LIB directly at driver level made every later `. "$LIB"` inside run_row's
# subshell (which inherits the driver's exported vars) a silent no-op, which
# in turn disabled the QUM-957 per-row fault-ledger TRUNCATION — a fault
# recorded in row A then failed every row after it, exactly the
# misattributed-FAIL class QUM-1118 exists to end. Confirmed with a 2-row
# fixture before landing this form: row A poisons the ledger, row B FAILed
# sourcing directly, PASSes here.
( . "$LIB"; e2e_check_disk_space ) || exit $?

run_row() {
    local name="$1"
    local row_file="$ROWS_DIR/$name.sh"
    (
        # shellcheck disable=SC1090
        . "$LIB"
        # QUM-1029: the assertion floor is whatever the ROW declares, never
        # what the caller's environment happens to hold. An exported
        # MIN_ASSERTIONS would otherwise hand a row that declares none a floor
        # it never wrote, turning the undeclared case green. Unset before the
        # row is sourced so its own declaration still wins.
        unset MIN_ASSERTIONS
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
skip_count=0
requested=${#selected[@]}
failed_rows=""
skipped_rows=""
skip_report=""

# The skip sentinel. Rows are sourced in a subshell and inherit it via the
# environment; e2e_skip_row writes its reason here before exiting 77.
# mktemp is preferred but optional: some unit tests run this driver with PATH
# scrubbed to prove the preflights fire before any heavy work, so every later
# touch of this file uses bash builtins only (`: >`, `[ -s ]`, `read`).
E2E_SKIP_FILE=$(mktemp "${TMPDIR:-/tmp}/e2e-matrix-skip.XXXXXX" 2>/dev/null || true)
if [ -z "$E2E_SKIP_FILE" ]; then
    # mktemp is unavailable (PATH-scrubbed runs). The fallback name is
    # predictable, and `>` follows symlinks, so in a world-writable /tmp someone
    # could pre-place a link and have us truncate an arbitrary writable file.
    # noclobber makes the create fail rather than clobber.
    E2E_SKIP_FILE="${TMPDIR:-/tmp}/e2e-matrix-skip.$$"
    if ! (set -C; : >"$E2E_SKIP_FILE") 2>/dev/null; then
        echo "error: cannot create the skip sentinel at $E2E_SKIP_FILE" >&2
        echo "hint: something already exists at that path; remove it. Skips cannot be" >&2
        echo "      distinguished from passes without a sentinel, so refusing to run." >&2
        exit 2
    fi
fi
export E2E_SKIP_FILE
if ! : >"$E2E_SKIP_FILE" 2>/dev/null; then
    echo "error: cannot write the skip sentinel at $E2E_SKIP_FILE" >&2
    echo "hint: skips cannot be distinguished from passes without it; refusing to run" >&2
    exit 2
fi
# An EXIT trap that does not call `exit` leaves the script's status untouched,
# so this cannot rewrite a nonzero verdict into 0. `|| true` keeps a missing
# `rm` (PATH-scrubbed runs) from tripping errexit. Signals are trapped too, or
# an interrupted run leaks the sentinel.
trap 'rm -f "$E2E_SKIP_FILE" 2>/dev/null || true' EXIT INT TERM HUP

# Echo the selection before running anything, so a truncated or unexpected
# selection is visible without having to reverse-engineer it from the summary.
echo "=== Matrix: running $requested row(s): ${selected[*]} ==="

# The per-row truncate, factored out ONLY so the debug seam below can suppress
# it: without a way to leave a previous row's reason in place, the emptiness
# post-condition on the guard in the loop is unexercisable, and an unexercised
# guard is decoration (same argument as SPRAWL_E2E_MATRIX_DEBUG_TALLY_SKEW).
# Redirection order is load-bearing: `>` is applied before `2>/dev/null`, so
# bash's own diagnostic still reaches real stderr when the write fails.
# Deliberately NOT used for the create/writability preflight above — that one
# is a different fault with a different exit status (2) and no post-condition.
reset_skip_sentinel() {
    if [ "${SPRAWL_E2E_MATRIX_DEBUG_STALE_SENTINEL:-}" = "1" ]; then
        # Debug seam: report success without clearing anything, so whatever the
        # previous row wrote survives into the next row's classification.
        return 0
    fi
    : >"$E2E_SKIP_FILE" 2>/dev/null || return 1
    return 0
}

for name in "${selected[@]}"; do
    # QUM-1118: re-check before EVERY row, not just once at the top of this
    # file. A long run can exhaust disk between rows; this call aborts the
    # whole driver (exit 5, see e2e_check_disk_space) rather than letting the
    # next row run and fail for a reason that has nothing to do with it.
    # Subshelled for the same reason as the startup check above — never
    # sourced into the driver's own namespace.
    ( . "$LIB"; e2e_check_disk_space ) || exit $?

    # Truncate PER ROW, and REFUSE TO CONTINUE if that fails. A stale sentinel
    # would launder the next row's failure into a skip (exit 3 instead of 1),
    # hiding a real failure — so a truncation error leaves classification
    # untrustworthy, exactly like being unable to create the sentinel above.
    # Swallowing it with `|| true` and then reading the file would reintroduce
    # QUM-952 wearing a different hat. Note bash reports a failed redirection
    # itself, so `2>/dev/null` does not silence it; the explicit message below
    # is what says *why* the run is aborting.
    if ! reset_skip_sentinel || [ -s "$E2E_SKIP_FILE" ]; then
        echo "internal error: cannot reset the skip sentinel $E2E_SKIP_FILE before row '$name'" >&2
        echo "               (the write failed, or its content survived the reset);" >&2
        echo "               stale content would misclassify this row, so refusing to continue" >&2
        exit 4
    fi

    # `rc=0; cmd || rc=$?` is the errexit-safe capture: the `||` suppresses
    # errexit exactly as the previous `if` form did, and $? is the row's own
    # status. A bare `run_row "$name"; rc=$?` would abort the driver.
    rc=0
    run_row "$name" || rc=$?

    # The discriminator below is "did the row leave a READABLE REASON", not
    # "does the sentinel have bytes". `[ -s ]` alone is a byte count, so a bare
    # newline satisfies it, and a skip whose reason is blank (or a synthesized
    # placeholder) is a row nobody can triage — an entry that looks
    # accounted-for and isn't. So: scan for the first line containing a
    # non-whitespace character; if there is none, `skip_reason` stays empty and
    # the row is treated exactly as if it had written nothing at all.
    skip_reason=""
    if [ -s "$E2E_SKIP_FILE" ]; then
        while IFS= read -r _line; do
            case "$_line" in
                *[![:space:]]*)
                    skip_reason="$_line"
                    break
                    ;;
            esac
        done <"$E2E_SKIP_FILE"
    fi

    if [ "$rc" -eq 0 ] && [ -n "$skip_reason" ]; then
        # Unreachable in correct code — see the precedence table above.
        echo "internal error: row '$name' recorded a skip ('$skip_reason') but exited 0;" >&2
        echo "               refusing to classify it as either a pass or a skip" >&2
        exit 4
    fi

    if [ "$rc" -eq 0 ]; then
        echo "PASS $name"
        pass_count=$((pass_count + 1))
    elif [ "$rc" -eq 77 ] && [ -n "$skip_reason" ]; then
        echo "SKIP $name"
        skip_count=$((skip_count + 1))
        skipped_rows="$skipped_rows $name"
        skip_report="${skip_report}!!!   $name: $skip_reason
"
    else
        echo "FAIL $name"
        fail_count=$((fail_count + 1))
        failed_rows="$failed_rows $name"
    fi
done

if [ "${SPRAWL_E2E_MATRIX_DEBUG_TALLY_SKEW:-}" = "1" ]; then
    # Debug seam: skew the tally AFTER the loop so the invariant below is what
    # detects it. An invariant with no way to violate it is untestable, and an
    # untested invariant is decoration.
    pass_count=$((pass_count + 1))
fi

# Every requested row must land in exactly one bucket. Checked BEFORE any
# summary is printed: a "N/N passed" line next to an error message is the very
# false green this driver exists to avoid.
if [ "$((pass_count + fail_count + skip_count))" -ne "$requested" ]; then
    echo "internal error: bucket tally $pass_count passed + $fail_count failed + $skip_count skipped" >&2
    echo "               does not equal the $requested row(s) requested; results are not trustworthy" >&2
    exit 4
fi

# Denominator is `requested`, never a count derived from the loop above.
# This line is byte-stable (QUM-947 contract): `passed` means actually executed
# and passed, so a skip now shows up here as a shortfall.
echo "=== Matrix: $pass_count/$requested passed ==="
echo "=== Matrix breakdown: $pass_count passed, $fail_count failed, $skip_count skipped / $requested requested ==="

if [ "$skip_count" -gt 0 ]; then
    {
        echo "!!! Matrix: $skip_count of $requested row(s) SKIPPED — those rows asserted nothing:"
        # One line per skipped row, "<row>: <reason>", already assembled above.
        printf '%s' "$skip_report"
        echo "!!! A skipped row asserts nothing and does NOT discharge a mandatory-gate"
        echo "!!! obligation. Re-run it with a real, AUTHENTICATED 'claude' before claiming"
        echo "!!! the touched-file matrix row was validated."
        echo "!!! NOTE: this gate keys on claude being ABSENT and never probes auth. An"
        echo "!!! installed-but-unauthenticated claude does NOT skip — the row runs and"
        echo "!!! fails with 'Not logged in', which is an auth problem, not a product"
        echo "!!! regression. SPRAWL_E2E_SKIP_NO_CLAUDE is not the remedy for that, and"
        echo "!!! never hide claude from PATH to force a skip: that only buys this"
        echo "!!! vacuous all-skip run."
    } >&2
fi

if [ "$fail_count" -gt 0 ]; then
    echo "=== Matrix: failed rows:$failed_rows ===" >&2
    exit 1
fi
if [ "$skip_count" -gt 0 ]; then
    echo "=== Matrix: skipped rows:$skipped_rows ===" >&2
    exit 3
fi
exit 0
