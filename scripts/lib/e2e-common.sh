#!/usr/bin/env bash
# scripts/lib/e2e-common.sh — Shared scaffolding for matrix-driven e2e tests
# (QUM-616 Wave 1). Sourceable; idempotent / re-source-safe.

[[ -n "${_E2E_COMMON_SH:-}" ]] && return 0
_E2E_COMMON_SH=1

# Resolve repo root once at source time, using parameter expansion so the lib
# is sourceable even when PATH is scrubbed (the matrix driver's preflight
# unit tests deliberately drop PATH before invoking the driver).
_e2e_self="${BASH_SOURCE[0]}"
case "$_e2e_self" in
    /*) _e2e_self_dir="${_e2e_self%/*}" ;;
    */*) _e2e_self_dir="$PWD/${_e2e_self%/*}" ;;
    *) _e2e_self_dir="$PWD" ;;
esac
_e2e_scripts_dir="${_e2e_self_dir%/*}"
E2E_COMMON_REPO_ROOT="${_e2e_scripts_dir%/*}"
unset _e2e_self _e2e_self_dir _e2e_scripts_dir
: "${REPO_ROOT:=$E2E_COMMON_REPO_ROOT}"

PASS_COUNT=0
FAIL_COUNT=0

pass() {
    PASS_COUNT=$((PASS_COUNT + 1))
    echo "  PASS: $1"
}

fail() {
    FAIL_COUNT=$((FAIL_COUNT + 1))
    echo "  FAIL: $1" >&2
}

# QUM-1029: a row that asserts NOTHING must not report PASS. This used to
# return non-zero only when FAIL_COUNT>0, so a row that recorded neither a pass
# nor a fail rendered "0 passed, 0 failed", returned 0, and scripts/e2e-matrix.sh
# — which reads a row's EXIT STATUS ONLY and is structurally unable to see how
# much the row asserted — counted it in `pass_count` and in the
# "=== Matrix: N/N passed ===" line CLAUDE.md tells readers to scrape.
#
# The floor is PER-ROW and CALLER-SUPPLIED: each row declares a top-level
# `MIN_ASSERTIONS=<n>`. Deliberately NOT derived from anything measured here —
# a floor computed from the corpus it checks is satisfied by an empty corpus,
# which is the very defect. For the same reason an UNDECLARED floor fails
# rather than defaulting: a default would let every existing row keep the
# defect silently. So does a declared 0, which is that default wearing a
# declaration.
#
# A breach is an ORDINARY FAILURE (the row returns 1, the driver buckets it
# FAIL and exits 1). It is deliberately not a skip (3) or an internal-invariant
# violation (4): a row that asserted nothing is a defect in the row, not an
# unmet precondition and not a driver fault.
#
# What to declare: the minimum of PASS_COUNT+FAIL_COUNT over the paths that
# COMPLETE THE ROW SUCCESSFULLY — reaching this function having called fail()
# zero times and without an early return. Deliberately NOT the minimum over all
# paths that reach here: a path that already called fail(), or that bailed
# early, has already failed the row, so a floor that also fires on it costs a
# line of extra diagnosis and nothing else. The only way a floor can turn an
# HONEST run red is by exceeding what a legitimately-passing path asserts —
# hence the minimum is taken over passing paths only. (Taking it over all paths
# collapses almost every row to 1 or 0 via its `launch failed -> print -> return
# 1` shortcut, which eliminates nothing.)
#
# On that green path: an if/else whose arms are pass/fail contributes 1; an
# asymmetric if/else whose other arm only echoes a note contributes 0; a loop
# contributes 0 unless it iterates a fixed literal list; a block that a
# successful run can skip entirely contributes 0.
#
# Only builtins here: the matrix driver's own unit suite invokes it with PATH
# scrubbed, and `${MIN_ASSERTIONS-}` (not a bare reference) keeps `set -u` from
# aborting the row before this can diagnose it.
e2e_print_results() {
    echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
    local declared=${MIN_ASSERTIONS-}
    local observed=$((PASS_COUNT + FAIL_COUNT))
    local breach=""
    case "$declared" in
        '')
            breach="the row declared no MIN_ASSERTIONS floor — add a top-level MIN_ASSERTIONS=<n> naming the fewest assertions a complete run of this row makes (QUM-1029)"
            ;;
        # Compared as a STRING, never evaluated: `$(( ))` on a caller-supplied
        # value executes any command substitution inside it.
        #
        # The 10-digit cap is not cosmetic. `[ x -lt y ]` parses with strtoimax
        # and ERRORS on a value past int64; inside an `if` that error status is
        # merely "false", so the shortfall arm below would record no breach and
        # the row would PASS — a declaration that reads as a floor and enforces
        # nothing, which is the exact class this function exists to close. No
        # row will ever make a billion assertions, so anything that long is a
        # typo, not a floor.
        *[!0-9]* | 0* | ??????????*)
            breach="MIN_ASSERTIONS='$declared' is not a plausible floor — it must be a positive whole number below 10 digits (0 is satisfied by a row that asserts nothing; a value past int64 makes the comparison itself error out and silently pass) (QUM-1029)"
            ;;
        *)
            # Gated on FAIL_COUNT: a row that already recorded a failure has
            # already failed, and reporting a shortfall as its LAST stderr line
            # would summarise a genuine defect as a floor breach — on a row with
            # a floor of 25 that is what every early failure would look like.
            # The declaration-validation arms above stay unconditional: a
            # malformed declaration is a defect in its own right regardless of
            # how the run went.
            if [ "$FAIL_COUNT" -eq 0 ] && [ "$observed" -lt "$declared" ]; then
                breach="only $observed assertion(s) ran but MIN_ASSERTIONS=$declared — the row measured less than it claims (QUM-1029)"
            fi
            ;;
    esac
    if [ -n "$breach" ]; then
        echo "  FAIL: $breach" >&2
        return 1
    fi
    # QUM-957. Deliberately NOT gated on FAIL_COUNT, unlike the shortfall arm
    # above: FAIL_COUNT==0 with PASS_COUNT>0 IS the vacuous green this catches —
    # a row whose negative assertions were all satisfied by an unreadable pane.
    # Its message is distinct from the floor breach on purpose; "the row measured
    # less than it claims" and "tmux was unreachable" are different diagnoses
    # with different remedies, and conflating them sends the reader to the wrong
    # one. Placed after the floor arms so a malformed declaration still reports
    # first, and before the FAIL_COUNT arm so the faults are always printed.
    if ! capture_pane_assert_no_faults; then
        return 1
    fi
    if [ "$FAIL_COUNT" -gt 0 ]; then
        return 1
    fi
    return 0
}

# QUM-411: walk up to 8 ancestors via /proc/<pid>/stat parent field and try to
# recover CLAUDE_CODE_OAUTH_TOKEN from each ancestor's environ. HARNESS-ONLY.
#
# QUM-974/QUM-973: the return status is meaningful. 0 iff a token is present
# afterwards (already set, or recovered from an ancestor); nonzero when the
# walk exhausted all 8 ancestors and found nothing. Every call site in
# scripts/e2e-tests/ invokes this as a bare statement under the driver's
# `set -euo pipefail` (scripts/e2e-matrix.sh:5, inherited into run_row's
# subshell), so a nonzero return here aborts that row IMMEDIATELY, before it
# launches any session — exactly the "fail loudly and immediately, do not
# proceed to launch" QUM-973 requires, with no per-call-site `if` needed.
# This is a hard hard-failure (ordinary row FAIL, driver exit 1), never a
# skip: it does not call e2e_skip_row and never touches $E2E_SKIP_FILE, so it
# cannot be laundered into the QUM-952 skip bucket.
#
# ${SPRAWL_E2E_MATRIX_DEBUG_OAUTH_SCAN_PID:-$$} is a QUM-974 test-only debug
# seam (registered in scripts/test-e2e-matrix-unit.sh's UNIT_SCRUBBED_VARS)
# letting the unit suite point the ancestor walk at a PID whose parent is
# known (e.g. pid 1, whose /proc/1/stat parent field is 0) instead of this
# process's REAL ancestor chain — which may or may not carry a token
# depending on the host running the test, making the failure path otherwise
# non-deterministic. Never set outside the unit suite.
e2e_recover_oauth_token() {
    if [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
        return 0
    fi
    local scan_pid=${SPRAWL_E2E_MATRIX_DEBUG_OAUTH_SCAN_PID:-$$} parent recovered
    for _ in 1 2 3 4 5 6 7 8; do
        parent=$(awk '{print $4}' "/proc/$scan_pid/stat" 2>/dev/null || true)
        if [ -z "$parent" ] || [ "$parent" = "0" ]; then
            break
        fi
        if [ -r "/proc/$parent/environ" ]; then
            recovered=$(tr '\0' '\n' < "/proc/$parent/environ" \
                | grep '^CLAUDE_CODE_OAUTH_TOKEN=' | cut -d= -f2- || true)
            if [ -n "$recovered" ]; then
                export CLAUDE_CODE_OAUTH_TOKEN="$recovered"
                echo "  (recovered CLAUDE_CODE_OAUTH_TOKEN from ancestor pid=$parent)"
                return 0
            fi
        fi
        scan_pid=$parent
    done
    echo "FATAL: could not recover CLAUDE_CODE_OAUTH_TOKEN from any of 8 ancestors (QUM-974)." >&2
    echo "       This row will fail with 'Not logged in' rather than proceeding tokenless." >&2
    echo "       This usually means the run was launched detached (setsid/nohup), which" >&2
    echo "       reparents to init and severs the /proc ancestor chain this walk depends" >&2
    echo "       on (QUM-973) — /proc/1/environ is unreadable by this user, so the walk" >&2
    echo "       finds nothing once it reaches init." >&2
    echo "       Fix: launch without setsid/nohup, or export CLAUDE_CODE_OAUTH_TOKEN" >&2
    echo "       explicitly before invoking the harness (also required for a legitimate" >&2
    echo "       detached run)." >&2
    return 1
}

# QUM-325: dedicated tmux socket for sandbox isolation.
e2e_setup_tmux_socket() {
    local prefix=${1:-sprawl-e2e}
    SPRAWL_TMUX_SOCKET="${SPRAWL_TMUX_SOCKET:-${prefix}-$$}"
    export SPRAWL_TMUX_SOCKET
}

_stmux() {
    tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"
}

# QUM-952: a skipped row must never be indistinguishable from a passed one.
# `exit 0` cannot mean both, so a skip exits 77 (the autotools/`make check` SKIP
# convention) AND records its reason in the driver-owned sentinel named by
# $E2E_SKIP_FILE. The driver requires BOTH before it will believe a skip, so a
# row that merely happens to exit 77 cannot forge one.
E2E_SKIP_EXIT=77

# Declare the current row skipped and abort it. UNCONDITIONAL by design: the
# decision of *whether* a missing dependency is skippable belongs to the caller
# (e.g. e2e_require_claude_or_skip consults SPRAWL_E2E_SKIP_NO_CLAUDE); this
# function only signals the outcome. Do not gate it on that variable.
e2e_skip_row() {
    local reason=${1:-unspecified}
    # $E2E_SKIP_FILE is unset when the lib is sourced outside the driver; a skip
    # must still be signalled. The `|| true` matters under `set -e`: a failed
    # redirect would exit 1 before `exit 77`, turning a skip into a failure.
    if [ -n "${E2E_SKIP_FILE:-}" ]; then
        { printf '%s\n' "$reason" >>"$E2E_SKIP_FILE"; } 2>/dev/null || true
    fi
    echo "SKIP: $reason"
    exit "$E2E_SKIP_EXIT"
}

# QUM-1118: distinct from BOTH a skip (E2E_SKIP_EXIT=77, "nothing measured and
# that's fine") and an ordinary row failure (1, "the product is broken"). An
# unfit environment means nothing was measured and that is NOT acceptable, so
# it gets its own exit code the driver never confuses with either.
E2E_ENV_UNFIT_EXIT=5

# QUM-1108/QUM-1135/QUM-1045: distinct again from all of the above. 5 means the
# HOST cannot host a run (no disk); 6 means the host is fine but no usable
# CREDENTIAL reached `claude`, so every needs_claude row would fail with
# 'Not logged in' and none of those failures would be about the product.
# Deliberately NOT 3 (a skip) and NOT 1 (a row failure): the whole point of
# QUM-1108 is that a run which asserts nothing must not be reportable as
# either. Never downgrade this to 3 or 1 to "simplify" scraping.
E2E_AUTH_UNFIT_EXIT=6

# Threshold basis (QUM-1118): the 2026-08-06 incident died with `/` at 3.4G
# (3482MB) free, taking 13 of 19 concurrent rows down inside `go build` with
# ENOSPC. Default is that failure point plus ~18% margin, rounded to a clean
# 4096MB (4GiB), applied uniformly to every filesystem checked — the incident
# gives no reason to trust one filesystem's margin over another's.
E2E_MIN_FREE_MB_DEFAULT=4096

# e2e_free_mb KIND PATH — echo free space at PATH in MB, or return 1 (echoing
# nothing) if it cannot be read. KIND selects a debug-seam override
# (SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP / _REPO) so unit tests can drive this
# without ever touching a real filesystem, per the repo's assertion-rigor rule
# against filling a real disk to test a disk check.
#
# The seam's value may be a bare number OR a path to a file whose first line
# is read FRESH on every call. The file form is what lets a test simulate
# space disappearing BETWEEN two checks in the same driver process (mid-run
# exhaustion, QUM-1118 AC4) — a static env var cannot change value mid-process,
# but a row can rewrite a file on real disk from inside its own subshell, and
# the next e2e_check_disk_space call (in the parent driver, not the row's
# subshell) will read the new value. This never touches disk SPACE, only a
# tiny marker file's contents, so it is not "actually filling the disk".
#
# A SET-BUT-UNUSABLE seam (a typo, an unwritten/empty file, a nonexistent
# path) returns 2 rather than silently falling back to measuring the real
# filesystem — the same "refuse to guess" rule e2e_check_disk_space applies to
# SPRAWL_E2E_MIN_FREE_MB. Silently measuring reality here would make the
# healthy-path assertions pass for a reason they do not name: the operator's
# real (usually healthy) disk, not the seam under test. A successfully
# resolved seam is logged unconditionally too, for the same reason the
# threshold override is: an active seam can defeat this whole precondition,
# and that must never happen with no trace.
e2e_free_mb() {
    local kind="$1" path="$2" kb var raw val
    case "$kind" in
        tmp) var=SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_TMP ;;
        repo) var=SPRAWL_E2E_MATRIX_DEBUG_FREE_MB_REPO ;;
        *) var= ;;
    esac
    if [ -n "$var" ] && [ -n "${!var:-}" ]; then
        raw="${!var}"
        # Trim surrounding whitespace so " 10" / "10 " / a trailing newline
        # left by a naive file writer are treated as plain numbers.
        val="$raw"
        val="${val#"${val%%[![:space:]]*}"}"
        val="${val%"${val##*[![:space:]]}"}"
        case "$val" in
            '' | *[!0-9]*)
                # Not a bare number — try it as a seam FILE, first line only.
                if [ -r "$raw" ]; then
                    val=$(head -n1 "$raw" 2>/dev/null)
                    val="${val#"${val%%[![:space:]]*}"}"
                    val="${val%"${val##*[![:space:]]}"}"
                else
                    val=""
                fi
                ;;
        esac
        case "$val" in
            '' | *[!0-9]*)
                echo "FATAL: \$$var='$raw' is neither a whole number of MB nor a readable file containing one — refusing to silently measure the real filesystem instead of the seam under test (QUM-1118)" >&2
                return 2
                ;;
            *)
                echo "WARN: disk-space precondition '$kind' reading overridden by debug seam \$$var (resolved to ${val}MB; real df bypassed) — QUM-1118" >&2
                echo "$val"
                return 0
                ;;
        esac
    fi
    kb=$(df -Pk "$path" 2>/dev/null | awk 'NR==2 {print $4}') || return 1
    [ -n "$kb" ] || return 1
    case "$kb" in *[!0-9]*) return 1 ;; esac
    echo $((kb / 1024))
}

# e2e_check_disk_space — QUM-1118. FAILS LOUDLY (E2E_ENV_UNFIT_EXIT), never
# skips, when any filesystem this harness writes to has less free space than
# the threshold. Checks BOTH ${TMPDIR:-/tmp} (every sandbox root, tmux socket,
# and go build's own scratch dir live here) and $REPO_ROOT (GOCACHE and the
# built binary live here) because on this host they are measured to be
# DIFFERENT devices — the same reason the 2026-08-06 incident was first
# misdiagnosed as build-cache pressure on the wrong filesystem.
#
# SPRAWL_E2E_MIN_FREE_MB overrides the threshold for constrained environments.
# The override is logged UNCONDITIONALLY — not only when it lowers the bar —
# so a caller who sets it can never be silently unaware the check ran with a
# different number than the documented default. A non-numeric override fails
# loudly rather than silently falling back to the default: a typo'd override
# must not quietly re-enable the check it was meant to relax (or disable one
# meant to tighten it).
#
# DRIVER-LEVEL ONLY (code review finding): this function `exit`s the process
# it runs in. scripts/e2e-matrix.sh calls it in its own subshell and
# propagates the status explicitly (`( . "$LIB"; e2e_check_disk_space ) ||
# exit $?`) — it is never sourced directly into the driver's own namespace,
# because e2e-common.sh's re-source guard and capture-pane.sh's per-owner
# ledger vars are plain shell variables that would otherwise make every
# row's own `. "$LIB"` a silent no-op. If a ROW ever called this directly,
# the `exit` would only terminate that row's own run_row subshell, and the
# driver would bucket it as an ordinary FAIL (rc 5, no skip sentinel) —
# exactly the misclassification this function exists to prevent. Do not call
# it from inside a row.
e2e_check_disk_space() {
    # df/awk are absent only when a caller has deliberately stripped PATH to
    # test something else entirely (this suite's own [10]/[15] preflight
    # fixtures do exactly that) — every real e2e run has both, and every row
    # already needs a full PATH for git/tmux/claude/go regardless of this
    # check. Treat "the tool to check is missing" as distinct from "the tool
    # ran and found too little space": warn loudly and proceed rather than
    # fabricating a disk-space verdict this process cannot actually measure.
    if ! command -v df >/dev/null 2>&1 || ! command -v awk >/dev/null 2>&1; then
        echo "WARN: df and/or awk not found on PATH — cannot evaluate the QUM-1118 disk-space precondition; proceeding without it (a tooling gap, not a disk-space fact)" >&2
        return 0
    fi
    local threshold="$E2E_MIN_FREE_MB_DEFAULT"
    if [ -n "${SPRAWL_E2E_MIN_FREE_MB:-}" ]; then
        case "$SPRAWL_E2E_MIN_FREE_MB" in
            *[!0-9]*)
                echo "FATAL: SPRAWL_E2E_MIN_FREE_MB='$SPRAWL_E2E_MIN_FREE_MB' is not a whole number of MB — refusing to guess, environment unfit (QUM-1118)" >&2
                exit "$E2E_ENV_UNFIT_EXIT"
                ;;
        esac
        echo "WARN: disk-space precondition threshold overridden by SPRAWL_E2E_MIN_FREE_MB=${SPRAWL_E2E_MIN_FREE_MB} (default ${E2E_MIN_FREE_MB_DEFAULT}MB) — QUM-1118" >&2
        threshold="$SPRAWL_E2E_MIN_FREE_MB"
    fi

    local tmp_path="${TMPDIR:-/tmp}"
    local repo_path="${REPO_ROOT:-$E2E_COMMON_REPO_ROOT}"
    local kind path free unfit=0 kind_path
    for kind_path in "tmp:$tmp_path" "repo:$repo_path"; do
        kind="${kind_path%%:*}"
        path="${kind_path#*:}"
        if free=$(e2e_free_mb "$kind" "$path"); then
            if [ "$free" -lt "$threshold" ]; then
                echo "FATAL: ENVIRONMENT UNFIT — $path has ${free}MB free, below the ${threshold}MB threshold (QUM-1118)" >&2
                unfit=1
            fi
        else
            echo "FATAL: cannot read free space on $path (df failed) — environment unfit to run e2e rows (QUM-1118)" >&2
            unfit=1
        fi
    done
    if [ "$unfit" -eq 1 ]; then
        echo "FATAL: refusing to run — this is NOT a skip (nothing was measured, and that is unacceptable) and NOT a row failure (the product was never exercised); exiting ${E2E_ENV_UNFIT_EXIT} (QUM-1118)" >&2
        exit "$E2E_ENV_UNFIT_EXIT"
    fi
    return 0
}

# e2e_selected_rows_need_claude ROWFILE... — echo how many of the named row
# files declare needs_claude=1; return 0 if that count is >0, 1 otherwise.
#
# A TEXTUAL scan, deliberately, not a `. "$row_file"; test_metadata`. Sourcing
# rows at driver level is the exact fault recorded at scripts/e2e-matrix.sh's
# disk-check call site: the lib's re-source guard and the capture-pane ledger
# are plain shell variables, so a driver-level source makes every row's own
# `. "$LIB"` a silent no-op and disables the QUM-957 per-row ledger truncation.
# Pure bash builtins only (no grep): the driver's PATH-scrubbed preflight unit
# fixtures run with PATH=/nonexistent, and this must not be the thing that
# breaks there.
#
# Failure direction is deliberate. An over-match (the string in a comment)
# costs one cheap local probe that was not strictly needed. An under-match
# silently returns the harness to its pre-QUM-1108 behaviour. Over-matching is
# the safe side, so this matches the string anywhere in the file.
e2e_selected_rows_need_claude() {
    local f line count=0
    for f in "$@"; do
        [ -r "$f" ] || continue
        while IFS= read -r line || [ -n "$line" ]; do
            case "$line" in
                *needs_claude=1*)
                    count=$((count + 1))
                    break
                    ;;
            esac
        done <"$f"
    done
    echo "$count"
    [ "$count" -gt 0 ]
}

# _e2e_auth_preflight_fatal CAUSE ROWCOUNT [EXTRA] — the single exit-6 banner.
# Factored out so the two failure classes (no token reached the harness at all;
# the probe could not confirm a credential) cannot drift into two differently
# worded contracts. NOTHING derived from the probe's OUTPUT is ever passed in
# here: $cause is built from the exit status and a three-way shape
# classification only. This is a public repo and that output can carry a
# credential.
_e2e_auth_preflight_fatal() {
    local cause="$1" total="$2" extra="${3:-}"
    {
        echo "FATAL: AUTH PREFLIGHT FAILED — 'claude' is installed but no usable credential reached it (QUM-1108)"
        echo "       cause: $cause"
        [ -n "$extra" ] && echo "       note:  $extra"
        echo "       This is NOT a skip (nothing was measured, and that is unacceptable) and NOT a"
        echo "       row failure (the product was never exercised). Refusing to run $total row(s);"
        echo "       exiting ${E2E_AUTH_UNFIT_EXIT}. No row is reported FAIL for this."
        echo "       This is the state the per-row needs_claude gate cannot see: it keys on the"
        echo "       binary being ABSENT, so a claude installed but unauthenticated never trips"
        echo "       it. That is why this check exists and why it runs before any row."
        echo "       NOTE: this proves a credential is PRESENT, not that it is VALID — an expired or"
        echo "       revoked token still passes this preflight and still fails rows with 'Not logged in'."
        echo "       Fix: export CLAUDE_CODE_OAUTH_TOKEN, or create a repo-root .env and run via"
        echo "       scripts/run-claude / \$SPRAWL_CLAUDE. Check \$SPRAWL_ROOT: run-claude resolves"
        echo "       .env from it when set and from the repo root otherwise, so an exported"
        echo "       \$SPRAWL_ROOT changes which .env is read. A detached launch (setsid/nohup)"
        echo "       severs the /proc ancestor chain the token recovery walks (QUM-973)."
        echo "       Setup: .claude/skills/e2e-testing-sandboxing/SKILL.md"
        echo "       Do NOT hide claude from PATH and do NOT set SPRAWL_E2E_SKIP_NO_CLAUDE — neither"
        echo "       is the remedy for this state, and both only buy a vacuous all-skip run."
    } >&2
    exit "$E2E_AUTH_UNFIT_EXIT"
}

# e2e_check_claude_auth ROWFILE... — QUM-1108 Part 1 / QUM-1135 / QUM-1045.
# ONE credential check per BATCH, before any row runs.
#
# WHAT THIS PROVES, AND WHAT IT DOES NOT. It proves a credential is PRESENT.
# It does NOT prove the credential is VALID: measured on claude 2.1.226, a
# garbage token reports `"loggedIn": true`. So this collapses the "no
# credential reached the harness" class (a stripped agent subshell, a missing
# .env, a detached launch severing the QUM-411 /proc ancestor walk) and
# NARROWS the QUM-1108 misdiagnosis hazard without eliminating it — an expired
# or revoked token still passes here and still fails rows with 'Not logged in'.
# Do not let this function, its banner, or the docs claim otherwise.
#
# WHY IT ASSERTS ON CONTENT AND NOT JUST ON $?. `claude auth status`'s exit
# convention is a CLI contract this repo does not own and can change under it.
# A probe that trusts $? alone is inert the moment a CLI exits 0 while
# reporting no credential — which is precisely QUM-1135's named case. Both are
# checked, and the unit suite drives all four states through stubs.
#
# ORDER IS LOAD-BEARING: e2e_recover_oauth_token runs FIRST. Claude Code
# strips CLAUDE_CODE_OAUTH_TOKEN from an agent's Bash subshell (QUM-518), so a
# probe that asks before recovering reports "not logged in" on a perfectly
# healthy host. Its stderr is suppressed here because at BATCH level this
# function's own banner is the authoritative diagnostic; its per-ROW
# enforcement at scripts/e2e-matrix.sh's needs_claude gate is untouched.
#
# DRIVER-LEVEL ONLY, for the same reason e2e_check_disk_space is: it `exit`s
# the process it runs in. The driver calls it in its own subshell and
# propagates the status. Never call it from inside a row.
e2e_check_claude_auth() {
    if [ "${SPRAWL_E2E_SKIP_AUTH_PROBE:-}" = "1" ]; then
        echo "WARN: auth preflight DISABLED by SPRAWL_E2E_SKIP_AUTH_PROBE=1 — a 'Not logged in' row failure below is an auth problem, not a product regression (QUM-1108)" >&2
        return 0
    fi

    local need total="$#"
    need=$(e2e_selected_rows_need_claude "$@") || {
        echo "=== Matrix: auth preflight not required (no selected row declares needs_claude=1) ==="
        return 0
    }

    # claude ABSENT stays entirely owned by the per-row needs_claude gate and
    # by SPRAWL_E2E_SKIP_NO_CLAUDE. Behaviour in that state is unchanged.
    if ! command -v claude >/dev/null 2>&1; then
        echo "=== Matrix: auth preflight skipped (no claude on PATH — the per-row needs_claude gate owns this case) ==="
        return 0
    fi

    # RECOVERY FAILURE IS A PREFLIGHT FAILURE, and this arm is not optional.
    #
    # Found by running a detached (setsid) batch live: without this, the
    # preflight printed "credential present" and the first row aborted anyway.
    # The two were measuring DIFFERENT auth paths. The probe binary is
    # scripts/run-claude, which sources .env directly; every needs_claude row
    # instead depends on this ancestor walk, which a detached launch severs
    # (QUM-973) — and the driver's per-row needs_claude gate hard-fails a row
    # whose recovery returns nonzero, BEFORE it launches anything. So when
    # recovery fails, every needs_claude row is already doomed no matter what
    # the probe would say, and reporting OK is a false all-clear in precisely
    # the scenario this preflight exists to catch.
    #
    # Checking it here converts N doomed rows into one cheap check, which is
    # the entire cost this issue set out to remove. Its own stderr is
    # suppressed because the banner below is the batch-level diagnostic; the
    # per-row call and its return contract (QUM-974/QUM-973) are untouched.
    local recovered=1
    e2e_recover_oauth_token >/dev/null 2>&1 && recovered=0
    if [ "$recovered" -ne 0 ]; then
        _e2e_auth_preflight_fatal \
            "no token reached the harness — the QUM-411 /proc ancestor walk found none in 8 ancestors and CLAUDE_CODE_OAUTH_TOKEN is unset" \
            "$total" \
            "A detached launch (setsid/nohup) reparents to init and severs the ancestor chain the recovery walks (QUM-973). Every needs_claude row would abort on this individually; this stops the batch once instead."
    fi

    local bin
    if [ -n "${SPRAWL_E2E_MATRIX_DEBUG_AUTH_PROBE_BIN:-}" ]; then
        bin="$SPRAWL_E2E_MATRIX_DEBUG_AUTH_PROBE_BIN"
        echo "WARN: auth preflight probe binary overridden by debug seam \$SPRAWL_E2E_MATRIX_DEBUG_AUTH_PROBE_BIN (real claude bypassed) — QUM-1108" >&2
    elif [ -n "${SPRAWL_CLAUDE:-}" ]; then
        # An explicitly set $SPRAWL_CLAUDE is honoured VERBATIM and never
        # substituted (code review, F1). Falling back to a bare `claude` here
        # would make the preflight probe a binary NO ROW WILL RUN —
        # e2e_launch_tui resolves `${SPRAWL_CLAUDE:-…}` with no such fallback —
        # so a typo'd or stale path would produce a green preflight over a
        # harness that cannot launch anything. That is the misconfiguration
        # class this check exists to catch, so it is fatal rather than papered
        # over.
        bin="$SPRAWL_CLAUDE"
        if [ ! -x "$bin" ]; then
            _e2e_auth_preflight_fatal \
                "\$SPRAWL_CLAUDE is set to '$bin', which is not executable — refusing to substitute a different binary, because no row would run the substitute" \
                "$total" \
                "Rows resolve \$SPRAWL_CLAUDE with no fallback, so probing PATH 'claude' instead would green this misconfiguration. Fix or unset \$SPRAWL_CLAUDE."
        fi
    else
        bin="$REPO_ROOT/scripts/run-claude"
        [ -x "$bin" ] || bin=claude
    fi

    # `--json` is pinned EXPLICITLY rather than trusted as a default: a default
    # that can flip is not a contract.
    #
    # $out is NEVER echoed, on any path. It is the one variable in this
    # function that can carry a credential, and this is a public repo. Only
    # two derived facts leave here: the exit status, and which of three
    # recognised shapes the output had.
    local out rc=0
    out=$(timeout 30 "$bin" auth status --json 2>&1) || rc=$?

    local compact="${out//[[:space:]]/}"
    if [ "$rc" -eq 0 ]; then
        case "$compact" in
            *'"loggedIn":true'*)
                echo "=== Matrix: auth preflight OK (credential present; $need of $total selected row(s) declare needs_claude=1) ==="
                return 0
                ;;
        esac
    fi

    local cause
    if [ "$rc" -ne 0 ]; then
        cause="the probe exited $rc — it could not report an auth status at all"
        [ "$rc" -eq 124 ] && cause="$cause (124 = it timed out)"
        # 127 is a TOOLING gap, not a credential one, and the remedy block
        # below is entirely about credentials — say so, or the reader follows
        # six lines of .env advice for a missing binary (code review, F2).
        [ "$rc" -eq 127 ] && cause="$cause (127 = the probe binary or \`timeout\` was not found — a TOOLING problem, not a credential one)"
    elif [ -z "$compact" ]; then
        cause="the probe exited 0 and produced no output at all"
    else
        cause="the probe exited 0 but reported NO credential — exit status alone is not evidence"
    fi

    _e2e_auth_preflight_fatal "$cause" "$total"
}

e2e_require_claude_or_skip() {
    local name=${1:-test}
    if command -v claude >/dev/null 2>&1; then
        return 0
    fi
    if [ "${SPRAWL_E2E_SKIP_NO_CLAUDE:-}" = "1" ]; then
        e2e_skip_row "claude binary not found on PATH (required by $name; SPRAWL_E2E_SKIP_NO_CLAUDE=1 set)"
    fi
    # The FATAL branch must NOT touch the sentinel: a hard failure that also
    # wrote it could be laundered into a skip by any stale-read bug upstream.
    echo "FATAL: claude binary not found on PATH (required by $name)" >&2
    echo "       Set SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip this test instead (the skip is" >&2
    echo "       reported as a skip, not a pass, and exits nonzero -- it does not" >&2
    echo "       discharge a mandatory-gate obligation)." >&2
    # QUM-1108: the auth blind-spot paragraph used to live HERE, inside the
    # binary-absent branch — so it printed only when claude was missing, i.e.
    # only when auth was NOT the problem. It now lives in
    # e2e_check_claude_auth's banner, which fires in the state it describes.
    # What remains below is the sentence that IS relevant to this branch.
    echo "       Never hide claude from PATH to force a skip: that converts a" >&2
    echo "       credential problem into this one and buys a vacuous all-skip run." >&2
    echo "       A credential problem is stopped earlier, by the auth preflight" >&2
    echo "       (exit 6), and never reaches this branch." >&2
    exit 1
}

e2e_require_tmux() {
    if ! command -v tmux >/dev/null 2>&1; then
        echo "FATAL: tmux binary not found on PATH" >&2
        exit 1
    fi
}

e2e_require_jq() {
    if ! command -v jq >/dev/null 2>&1; then
        echo "FATAL: jq binary not found on PATH" >&2
        exit 1
    fi
}

e2e_build_sprawl() {
    if [ -n "${SPRAWL_BIN:-}" ] && [ -x "$SPRAWL_BIN" ]; then
        export SPRAWL_BIN
        return 0
    fi
    make -C "$REPO_ROOT" build >/dev/null
    SPRAWL_BIN="$REPO_ROOT/sprawl"
    export SPRAWL_BIN
    if [ ! -x "$SPRAWL_BIN" ]; then
        echo "FATAL: sprawl binary not found at $SPRAWL_BIN" >&2
        exit 1
    fi
}

e2e_make_sandbox_root() {
    local prefix=${1:-sprawl-e2e}
    local d
    d=$(mktemp -d "${TMPDIR:-/tmp}/${prefix}-XXXXXX")
    local real
    real="$(cd "$d" 2>/dev/null && pwd -P || echo "$d")"
    case "$real" in
        /tmp/*) ;;
        *)
            echo "FATAL: sandbox SPRAWL_ROOT=$real not under /tmp/; aborting" >&2
            exit 1
            ;;
    esac
    SPRAWL_ROOT="$real"
    export SPRAWL_ROOT
    # QUM-1181: scripts/run-claude (the QUM-518 auth shim that e2e_launch_tui
    # now points SPRAWL_CLAUDE at) resolves its env file as
    # "${SPRAWL_ROOT:-<script-dir>}/.env" — i.e. the SANDBOX, not the repo.
    # Centralizing the copy here means every row that calls this function
    # gets a working shim without opting in per row. cp -p preserves the
    # 0600 mode CLAUDE.md requires .env to carry. A repo with no .env leaves
    # the sandbox with none too — deliberately: this must not manufacture a
    # credential, only relay one that already exists (see the negative-
    # direction AC on QUM-1181: no .env must still fail or skip loudly, not
    # silently pass).
    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    # QUM-1119 rework (QA `sentry` finding): the ".sprawl" marker
    # cmd/sandbox_gc.go's discoverSandboxTmpDirs keys on used to be created
    # only by e2e_init_sandbox_repo below, a SEPARATE call nine rows never
    # make (attach-blocks, capture-pane-liveness, drain-row-inject, handoff,
    # idle-interrupt-inject, merge-reuse, qum903-false-thinking,
    # recall-sendnow, replay-echo) — their roots had no marker and were
    # permanently unreapable at any age. Creating it here instead makes the
    # marker a true invariant of "a root e2e_make_sandbox_root created",
    # closing the gap structurally for every row rather than by enumerating
    # them. e2e_init_sandbox_repo's own `mkdir -p .sprawl` below is now a
    # harmless no-op for callers that also init a repo.
    mkdir -p "$SPRAWL_ROOT/.sprawl"
}

e2e_init_sandbox_repo() {
    git -C "$SPRAWL_ROOT" init -b main --quiet
    git -C "$SPRAWL_ROOT" config user.name "Test"
    git -C "$SPRAWL_ROOT" config user.email "test@test"
    # Code review (zone, F3): e2e_make_sandbox_root may have already copied a
    # real credential into $SPRAWL_ROOT/.env before this git repo exists. No
    # row stages the sandbox tree wholesale today, but this repo is driven by
    # a live claude agent — belt-and-suspenders so a future `git add -A`
    # inside the sandbox cannot pick it up.
    echo ".env" >> "$SPRAWL_ROOT/.gitignore"
    git -C "$SPRAWL_ROOT" add .gitignore
    git -C "$SPRAWL_ROOT" commit --allow-empty -m "init" --quiet
    mkdir -p "$SPRAWL_ROOT/.sprawl"
    echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"
}

_e2e_cleanup() {
    local rc=$?
    if [ -n "${PHANTOM_PID:-}" ]; then
        kill "$PHANTOM_PID" 2>/dev/null || true
    fi
    # QUM-957: the capture-fault ledger and its per-subshell stderr spools.
    capture_pane_cleanup
    if [ -n "${SPRAWL_TMUX_SOCKET:-}" ]; then
        tmux -L "$SPRAWL_TMUX_SOCKET" kill-server 2>/dev/null || true
        rm -f -- "/tmp/tmux-$(id -u)/$SPRAWL_TMUX_SOCKET" 2>/dev/null || true
    fi
    case "${SPRAWL_ROOT:-}" in
        /tmp/*)
            local attempt
            for attempt in 1 2 3 4 5; do
                if rm -rf -- "$SPRAWL_ROOT" 2>/dev/null; then
                    break
                fi
                sleep 1
            done
            if [ -d "$SPRAWL_ROOT" ]; then
                echo "  WARN: cleanup could not fully remove $SPRAWL_ROOT (watchdog will reap)" >&2
            fi
            ;;
    esac
    exit "$rc"
}

e2e_install_cleanup_traps() {
    trap _e2e_cleanup EXIT INT TERM HUP
    # QUM-458 layer 1: setsid'd watchdog reaps if driver dies via SIGKILL.
    local libdir
    libdir="$(dirname "${BASH_SOURCE[0]}")"
    # shellcheck source=sandbox-traps.sh
    . "$libdir/sandbox-traps.sh"
    sandbox_install_watchdog "$$" "${SPRAWL_TMUX_SOCKET:-}" "${SPRAWL_ROOT:-}"
}

# QUM-957: capture_pane / capture_pane_ansi / capture_pane_scrollback /
# capture_pane_{,ansi_}best_effort / capture_pane_dump / e2e_require_session_alive
# / e2e_pane_lacks / capture_pane_assert_no_faults / e2e_capture_fault_reset all
# live here. Their contract, and why a return code alone cannot enforce it in this
# harness, is documented at the top of that file.
#
# Resolved as a SIBLING of this file, not through $E2E_COMMON_REPO_ROOT: the
# matrix driver's unit suite copies this lib into a fixture tree at
# `<fixture>/lib/e2e-common.sh`, where the derived "repo root" is /tmp and
# `$root/scripts/lib/` does not exist. `${BASH_SOURCE[0]%/*}` is the only path
# that is right in both layouts, and a scrubbed PATH cannot break it.
# shellcheck source=capture-pane.sh
. "${BASH_SOURCE[0]%/*}/capture-pane.sh"

# The three wait_for_* helpers below ABORT with rc 2 on a capture fault rather
# than polling to their deadline. A dead session can never match, so the poll is
# pure waste — and its eventual "timed out" message would be the last thing on
# stderr, burying the fault that actually explains it. Every caller uses these in
# an `if`, where 2 reads as falsy exactly like the 1 they returned before.
#
# Two consequences of matching through a variable rather than `capture_pane |
# grep`, neither of which affects any pattern in the tree today, both of which
# the next author should know:
#
#   1. An EMPTY pane now feeds grep one empty line where the old form fed zero
#      bytes, so a pattern that can match the empty string (`^`, `.*`, `^ *$`)
#      would now match on a blank pane.
#   2. `$( )` strips trailing newlines, so a pattern anchored on the pane's
#      trailing blank rows would no longer match.
#
# Every literal pattern passed to these three across scripts/e2e-tests/ was
# enumerated and none is of either shape.
#
# `pane=$(capture_pane ...) || rc=$?`, not a bare assignment: under `set -e` a
# bare assignment from a FAILING command substitution kills the caller AT THE
# ASSIGNMENT, so the `rc` check below would never run and the fault would surface
# as a bare nonzero exit with no classification. The `||` makes it a list, which
# `set -e` does not act on. Every current call site is `if`-guarded (where `set
# -e` is suppressed anyway), so this is defence against the next plain call.
wait_for_pattern() {
    local session="$1" pattern="$2" timeout="$3"
    local elapsed=0 pane crc=0
    while [ "$elapsed" -lt "$timeout" ]; do
        crc=0
        pane=$(capture_pane "$session") || crc=$?
        [ "$crc" -eq 0 ] || return 2
        if printf '%s\n' "$pane" | grep -qE "$pattern"; then
            # QUM-671: emit a parseable elapsed-time record so consumers
            # (e.g. the S3 startup-time regression gate fed by
            # `wake-live.sh`'s TUI-rendered wait) have a comparable
            # number. Format is `WAIT_FOR_PATTERN_ELAPSED <secs> <pattern>`
            # — fixed prefix so a future scraper can grep without
            # ambiguity. Backward compatible: existing callers only
            # inspect the return code.
            echo "WAIT_FOR_PATTERN_ELAPSED ${elapsed}s pattern=${pattern}"
            return 0
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    return 1
}

wait_for_pattern_fast() {
    local session="$1" pattern="$2" timeout="$3"
    local start="$SECONDS"
    local end=$((SECONDS + timeout))
    local pane crc=0
    while [ "$SECONDS" -lt "$end" ]; do
        crc=0
        pane=$(capture_pane "$session") || crc=$?
        [ "$crc" -eq 0 ] || return 2
        if printf '%s\n' "$pane" | grep -qE "$pattern"; then
            # QUM-671: see wait_for_pattern above. Mirrored here so any
            # consumer that switches between the slow and fast variants
            # gets identical elapsed-time telemetry.
            echo "WAIT_FOR_PATTERN_ELAPSED $((SECONDS - start))s pattern=${pattern}"
            return 0
        fi
        sleep 0.2
    done
    return 1
}

wait_for_substring_fast() {
    local session="$1" needle="$2" timeout="$3"
    local end=$((SECONDS + timeout))
    local pane crc=0
    while [ "$SECONDS" -lt "$end" ]; do
        crc=0
        pane=$(capture_pane "$session") || crc=$?
        [ "$crc" -eq 0 ] || return 2
        if printf '%s\n' "$pane" | grep -qF "$needle"; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

# e2e_wait_maildir_substring TO NEEDLE [TIMEOUT] — poll agent TO's Maildir for
# NEEDLE, as a literal substring. Returns 0 when it lands, 1 on timeout, and 2
# when the CALL is malformed.
#
# QUM-1186. This is the suite's generic observability probe, and it replaces
# one: rows used to drive an agent to self-report a TOKEN and then poll
# `.sprawl/agents/<n>/state.json` for that token in the self-report field. The
# tool and the field are both deleted, and the reason they are deleted applies
# to the probe as much as to them — a self-report is an agent's claim about
# itself, not evidence. What this reads instead is a file the DELIVERY PATH
# wrote.
#
# The maildir and not the queue: send_message writes BOTH a Maildir entry under
# .sprawl/messages/<to>/ and an agentloop queue entry under
# .sprawl/agents/<to>/queue/pending/. The queue entry is consumed on delivery;
# the maildir entry is durable and survives it. Searching new/ alone would make
# the probe go red exactly when the recipient successfully drained its inbox.
#
# The three refusals are not defensive padding — each closes a way for this
# probe to succeed without observing anything:
#
#   empty NEEDLE — `grep -rqF ""` matches EVERY file, so an empty sentinel
#     succeeds against any non-empty maildir forever. Reachable, not
#     theoretical: rows build sentinels with `head -c4 /dev/urandom | xxd -p`,
#     which yields the empty string when xxd is missing.
#   empty TO — collapses the path to .sprawl/messages/, i.e. every agent's
#     mailbox, so any traffic between any two agents satisfies the row.
#   unset SPRAWL_ROOT — makes the path relative, so a probe run from the repo
#     searches the LIVE .sprawl/messages/ instead of the sandbox.
#
# Returning 2 rather than 1 for these matters: every caller uses this in an
# `if`, where both read as failure, but a run that refused and a run that
# genuinely waited out its timeout have different diagnoses.
#
# -F, never -E: sentinels are opaque tokens, and a regex match would let a row
# pass on a token it never sent (`probe.a1b2` matching `probe-a1b2`).
e2e_wait_maildir_substring() {
    local to="$1" needle="$2" timeout="${3:-120}"
    if [ -z "${SPRAWL_ROOT:-}" ]; then
        echo "  e2e_wait_maildir_substring: SPRAWL_ROOT is unset — refusing to search a relative path (it would read the live .sprawl/, not the sandbox)" >&2
        return 2
    fi
    if [ -z "$to" ]; then
        echo "  e2e_wait_maildir_substring: empty recipient — refusing to search every agent's mailbox" >&2
        return 2
    fi
    if [ -z "$needle" ]; then
        echo "  e2e_wait_maildir_substring: empty needle — refusing, an empty sentinel matches every message ever sent" >&2
        return 2
    fi
    # Only new/ cur/ archive/ — deliberately NOT the whole mailbox root.
    # internal/messages/messages.go drops a copy of every SENT message under
    # messages/<from>/sent/, so a recursive grep over the root can be satisfied
    # by a message the recipient ITSELF sent. Where `to` is also the sender —
    # weave asserting on its own mailbox, which several rows do — that turns
    # "the delivery path wrote this" into "the asserting side minted this",
    # which is the exact class of claim this probe replaced. `tmp/` is excluded
    # for a second reason: it is the pre-rename staging dir, so matching there
    # would assert on a message that had not landed yet.
    local elapsed=0 sub found
    while :; do
        for sub in new cur archive; do
            if [ -d "$SPRAWL_ROOT/.sprawl/messages/$to/$sub" ] &&
                grep -rqF -- "$needle" "$SPRAWL_ROOT/.sprawl/messages/$to/$sub" 2>/dev/null; then
                echo "WAIT_MAILDIR_ELAPSED ${elapsed}s to=${to}"
                return 0
            fi
        done
        # Budget-check BEFORE sleeping, not after. The naive form slept its full
        # poll interval past the deadline, so a `timeout 1` call took 2s. That
        # is not cosmetic: [19b] makes four not-found calls, every one of which
        # a [16] nested child inherits, and it cost ~32s of `make validate` —
        # shrinking [16b]'s documented 25x child-timeout margin to ~2.2x and
        # putting that arm one loaded host away from a spurious rc 124.
        elapsed=$((elapsed + 2))
        [ "$elapsed" -lt "$timeout" ] || return 1
        sleep 2
    done
}

# _e2e_weave_lock_diagnostic LOCK ATTEMPTS — explain who is still holding the
# lock. The file body is the last PID that successfully ACQUIRED (rootinit
# writes it after locking), which is not necessarily the current holder, so the
# live holder is reported separately and best-effort.
_e2e_weave_lock_diagnostic() {
    local lock="$1" attempts="$2"
    echo "  weave.lock still held: $lock (attempts=$attempts)" >&2
    local recorded=""
    [ -r "$lock" ] && recorded=$(tr -dc '0-9' <"$lock" 2>/dev/null | head -c 12)
    if [ -n "$recorded" ]; then
        echo "  recorded PID (lock file body): $recorded" >&2
        if command -v ps >/dev/null 2>&1; then
            ps -o pid=,ppid=,etime=,args= -p "$recorded" >&2 2>/dev/null || \
                echo "    (PID $recorded is gone — the body is stale)" >&2
        fi
    else
        echo "  recorded PID: none (lock file body empty or unreadable)" >&2
    fi
    local live=""
    if command -v fuser >/dev/null 2>&1; then
        live=$(fuser "$lock" 2>/dev/null | tr -s ' ')
    fi
    if [ -z "$live" ] && command -v lsof >/dev/null 2>&1; then
        live=$(lsof -t -- "$lock" 2>/dev/null | tr '\n' ' ')
    fi
    if [ -n "$live" ]; then
        echo "  live holder PID(s): $live" >&2
        local p
        for p in $live; do
            command -v ps >/dev/null 2>&1 && ps -o pid=,ppid=,etime=,args= -p "$p" >&2 2>/dev/null
        done
    else
        echo "  live holder PID(s): unknown (fuser/lsof unavailable or reported nothing)" >&2
    fi
    return 0
}

# e2e_wait_weave_lock_free [ROOT] [TIMEOUT_SECS] — QUM-948. Block until
# `<ROOT>/.sprawl/memory/weave.lock` is acquirable, or fail.
#
# Why a poll and not a sleep: `tmux kill-session` only signals the pane, while
# the flock rootinit.AcquireWeaveLock holds is released by the kernel when the
# dying process's fd closes. A kill-then-relaunch path that sleeps a fixed 2s
# and then launches loses the race whenever teardown takes longer than that,
# and `sprawl enter` dies with "another weave session is already running".
# Lengthening the sleep would only trade a fast flake for a slow one and would
# hide a genuinely leaked lock; so we retry with backoff to a bounded deadline
# and FAIL loudly (never hang, never silently succeed) if the lock outlives it.
#
# Probe-only: `flock -n` releases immediately, so this holds nothing itself and
# cannot become the blocker for the launch it precedes.
e2e_wait_weave_lock_free() {
    local root="${1:-${SPRAWL_ROOT:-}}"
    local timeout="${2:-${SPRAWL_E2E_LOCK_WAIT_SECS:-30}}"
    if [ -z "$root" ]; then
        echo "  WARN: e2e_wait_weave_lock_free called with no root — skipping weave.lock wait" >&2
        return 0
    fi
    # A non-numeric timeout would reach `$((SECONDS + timeout))`, where bash
    # treats it as a VARIABLE NAME: under the driver's `set -u` that aborts the
    # whole ROW with a bare "10s: unbound variable" naming neither the knob nor
    # the row. "10s" and "1e3" are exactly what an operator reaches for.
    if ! [[ "$timeout" =~ ^[0-9]+$ ]]; then
        echo "  WARN: SPRAWL_E2E_LOCK_WAIT_SECS='$timeout' is not a whole number of seconds — using 30" >&2
        timeout=30
    fi
    local lock="$root/.sprawl/memory/weave.lock"
    # Absent lock file (or absent memory dir) means nothing can be holding it.
    # Checked rather than probed on purpose: `flock -n` would CREATE the file,
    # and fails with a distinct "cannot open" status when the dir is missing.
    [ -f "$lock" ] || return 0
    if ! command -v flock >/dev/null 2>&1; then
        echo "  WARN: flock(1) not found — cannot wait for weave.lock release; relaunch may race teardown" >&2
        return 0
    fi

    local start="$SECONDS"
    local end=$((SECONDS + timeout))
    local delay="0.2" attempts=0 rc=0
    while :; do
        attempts=$((attempts + 1))
        # -E 9 separates "held by someone else" (9) from "cannot open" (66):
        # without it both are 1 and an unprobeable path would look like
        # contention forever.
        rc=0
        flock -n -E 9 -x "$lock" true 2>/dev/null || rc=$?
        if [ "$rc" -eq 0 ]; then
            echo "WEAVE_LOCK_WAIT_ELAPSED $((SECONDS - start))s attempts=${attempts} path=${lock}"
            return 0
        fi
        if [ "$rc" -ne 9 ]; then
            echo "  WARN: flock probe on $lock exited $rc (not contention) — not waiting" >&2
            return 0
        fi
        [ "$SECONDS" -lt "$end" ] || break
        sleep "$delay"
        # Exponential backoff capped at 2s: responsive on the common fast
        # release, cheap over a long wait.
        case "$delay" in
        0.2) delay="0.4" ;;
        0.4) delay="0.8" ;;
        0.8) delay="1.6" ;;
        *) delay="2" ;;
        esac
    done
    # Report what we actually waited, not just the nominal deadline: the last
    # backoff sleep can carry us up to 2s past it.
    echo "  FAIL: weave.lock not released within the ${timeout}s deadline (waited $((SECONDS - start))s) — treating as a leaked lock, not waiting further" >&2
    _e2e_weave_lock_diagnostic "$lock" "$attempts"
    return 1
}

e2e_launch_tui() {
    local session="$1"
    local cols="${2:-200}"
    local rows="${3:-50}"
    # QUM-1181: extra "KEY=VAL" tokens (space-separated) inserted into the
    # tmux command string ahead of the sprawl binary, for the handful of rows
    # that need an additional env var in the launched pane (e.g.
    # SPRAWL_ENABLE_TEST_TOOLS=1). Optional and empty by default so every
    # existing caller is unaffected.
    local extra_env="${4:-}"
    # QUM-1181: default to the QUM-518 auth shim so every e2e_launch_tui
    # caller authenticates without opting in. A caller that has already
    # exported SPRAWL_CLAUDE (several rows do, to smuggle it into the tmux
    # server's inherited environment) keeps that value; this makes the
    # forwarding explicit and asserted instead of relying on that inheritance.
    local claude_bin="${SPRAWL_CLAUDE:-$REPO_ROOT/scripts/run-claude}"
    local stderr_log="${SPRAWL_ROOT}/.sprawl/tui-stderr.log"
    # QUM-948: a relaunch on a root whose previous weave is still tearing down
    # would die with "another weave session is already running". Wait for the
    # flock to actually be released instead of guessing with a fixed sleep.
    if ! e2e_wait_weave_lock_free "$SPRAWL_ROOT"; then
        echo "  FAIL: weave.lock still held before launching session $session — refusing to launch into a doomed acquire" >&2
        return 1
    fi
    # Code review (zone, F2): SPRAWL_CLAUDE now resolves silently (a stale
    # caller-exported value overrides the default with nothing logging which
    # one won), so every row log records the binary the pane actually
    # launches — the same reason SPRAWL_ROOT is already echoed by callers.
    echo "  SPRAWL_CLAUDE=$claude_bin"
    _stmux new-session -d -s "$session" -x "$cols" -y "$rows" \
        "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_CLAUDE='$claude_bin'${extra_env:+ $extra_env} '$SPRAWL_BIN' enter 2>'$stderr_log'"
    _stmux set-option -t "$session" window-size manual >/dev/null
    _stmux resize-window -t "$session" -x "$cols" -y "$rows" >/dev/null
    # QUM-656: tree migrated from a left-pane "weave (idle)" row into the
    # header orbital row rendered as `weave ──●`. We wait for the root token
    # as proxy for "supervisor data has propagated to the tree renderer".
    if ! wait_for_pattern "$session" "weave " 45; then
        echo "  FAIL: TUI did not render 'weave' root in header tree within 45s" >&2
        echo "  pane tail:" >&2
        capture_pane "$session" | tail -30 >&2
        echo "  stderr log tail:" >&2
        [ -f "$stderr_log" ] && tail -20 "$stderr_log" >&2
        # QUM-948: a residual TOCTOU between our probe and sprawl's own acquire
        # would otherwise present as 45s of blank pane with no explanation.
        if [ -f "$stderr_log" ] && grep -qF "already running" "$stderr_log"; then
            echo "  NOTE: stderr says another weave session is already running — the weave.lock" >&2
            echo "        was released after our probe but before sprawl acquired it, or a" >&2
            echo "        second session is using this SPRAWL_ROOT." >&2
        fi
        return 1
    fi
    return 0
}

# QUM-327 phantom client workaround: detached tmux sessions deliver input
# only when at least one client is attached. `script -q -c "tmux attach -d"`
# keeps a non-interactive attachment alive without stealing the user's tty.
e2e_attach_phantom_client() {
    local session="$1"
    script -q -c "tmux ${SPRAWL_TMUX_SOCKET:+-L $SPRAWL_TMUX_SOCKET} attach -t $session -d" /dev/null &
    PHANTOM_PID=$!
    export PHANTOM_PID
    sleep 1
}

# e2e_resolve_pane_process <root_pid> <comm-needle> — print the pid of the
# process AT or UNDER <root_pid> whose /proc comm matches <comm-needle>.
#
#   rc 0 -> match; the pid is on stdout
#   rc 1 -> <root_pid> is usable but nothing under it matches (the real
#           "the process we launched is not there" signal — a caller must
#           treat this as a failure, never as a skip)
#   rc 2 -> <root_pid> is missing, malformed, or has no /proc entry: the pane
#           itself is gone, which is a different diagnosis from rc 1
#
# QUM-1277: callers used to walk `pgrep -P $PANE_PID` for a matching CHILD.
# Whether that child exists is a property of the PANE SHELL, not of the process
# being looked for: given e2e_launch_tui's single command string
# ("VAR=x '/path/bin' arg 2>'log'"), zsh exec-optimizes it — so the pane pid IS
# the target and has no children — while dash and bash fork. tmux's
# default-shell here is /bin/zsh, so paste-coalesce failed on this host and
# would have passed on a forking one. Being SELF-INCLUSIVE and RECURSIVE is what
# makes this shell-independent: it never asks what the shell did.
#
# The search is confined to the pane's own subtree deliberately. A pid resolved
# from a global scan could belong to another agent's concurrent run or to a
# leaked session on the same root, and callers go on to signal what they
# resolve — so a broader fallback would trade a diagnosable failure for a run
# that may pass against the wrong process.
#
# /proc/<pid>/comm is truncated to TASK_COMM_LEN-1 = 15 bytes, so the needle is
# truncated to match (e2e-matrix.sh builds `sprawl-matrix-<row>` binaries for
# needs_build_tags rows, e.g. `sprawl-matrix-wake-live` -> `sprawl-matrix-w`).
# Two binaries agreeing in their first 15 bytes are indistinguishable here.
e2e_resolve_pane_process() {
    local root_pid="${1:-}" needle="${2:-}"
    case "$root_pid" in
        '' | *[!0-9]*) return 2 ;;
    esac
    [ -n "$needle" ] || return 2
    [ -d "/proc/$root_pid" ] || return 2
    needle="${needle:0:15}"

    # Cursor-indexed queue rather than array reslicing: O(n) instead of O(n^2),
    # and no empty-array expansion to trip over under `set -u`. The node budget
    # bounds the walk if pid reuse makes `seen` miss a revisit.
    local queue=("$root_pid") cursor=0 budget=4096 seen=" "
    local pid comm kids kid
    while [ "$cursor" -lt "${#queue[@]}" ]; do
        pid="${queue[$cursor]}"
        cursor=$((cursor + 1))
        budget=$((budget - 1))
        [ "$budget" -gt 0 ] || return 1
        case "$seen" in
            *" $pid "*) continue ;;
        esac
        seen="$seen$pid "

        comm=$(cat "/proc/$pid/comm" 2>/dev/null || true)
        if [ "$comm" = "$needle" ]; then
            printf '%s\n' "$pid"
            return 0
        fi

        # Every task dir, not just task/<pid>: a Go program's children can be
        # forked from any thread, so a single-task read would miss them.
        for kids in "/proc/$pid/task/"*/children; do
            [ -r "$kids" ] || continue
            for kid in $(cat "$kids" 2>/dev/null || true); do
                queue+=("$kid")
            done
        done
    done
    return 1
}

e2e_send_user_prompt() {
    # QUM-432: the TUI's paste classifier reclassifies an Enter arriving
    # < 10ms after a printable key as an embedded newline (stripped-
    # bracketed-paste burst). Pause between the text and the Enter so the
    # submit lands as a discrete keystroke. Send text without -l so tmux
    # key-name parsing keeps the original test-drain/test-handoff behavior.
    local session="$1" text="$2"
    _stmux send-keys -t "$session" "$text"
    sleep 0.5
    _stmux send-keys -t "$session" Enter
}
