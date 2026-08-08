#!/usr/bin/env bash
# scripts/lib/capture-pane.sh — tmux pane capture that cannot hide a tmux
# failure (QUM-957). Sourceable; idempotent / re-source-safe.
#
# WHAT THIS REPLACES
#
#   capture_pane() { _stmux capture-pane -t "$1" -p 2>/dev/null || true; }
#
# That one-liner appeared in eleven places and threw away both halves of the
# evidence: `2>/dev/null` the diagnostic and `|| true` the exit status. Against a
# dead session — TUI panicked, never launched, session name typo'd, server gone —
# it returned EMPTY STDOUT WITH EXIT 0. Every assertion of the form "pattern must
# NOT appear in the pane" was therefore satisfied by there being no pane to look
# at, and rows whose checks are mostly negative inverted into unconditional
# greens. A row with many such assertions is the one a reader trusts most.
#
# THE DISTINCTION THIS LIBRARY EXISTS TO PRESERVE
#
#   "tmux succeeded and the pane is empty"  is NOT an error.
#   "tmux failed"                           IS an error.
#
# Collapsing them is how the defect comes back: a call site that legitimately
# reads a blank pane starts failing, someone quiets it with `|| true`, and the
# commit that does so looks like a fix. So an empty capture on a live session
# returns 0, writes nothing, and says nothing. Only a nonzero tmux status is a
# fault. scripts/test-e2e-matrix-unit.sh section [18] pins both directions, and
# scripts/e2e-tests/capture-pane-liveness.sh pins them against real tmux.
#
# WHY A LEDGER FILE AND NOT A RETURN CODE
#
# Two measured facts about this harness, either of which defeats the obvious fix:
#
#   1. scripts/e2e-matrix.sh runs each row as `if run_row "$name"; then`, and an
#      `if` condition suppresses `set -e` for everything inside it. A bare
#      `return 1` from capture_pane aborts nothing.
#   2. Nearly every call site is `capture_pane X | grep -q PAT`. A pipeline
#      DISCARDS the left-hand side's status, and even an `exit` from the LHS
#      kills only that pipeline element — grep still reads EOF and the pipeline's
#      status is grep's.
#
# So the status is necessary but not sufficient. A fault is additionally recorded
# in a file whose path is inherited by every subshell: a subshell cannot assign
# to its parent's variable, but it can append to a path the parent chose. The
# aggregator — e2e_print_results, or capture_pane_assert_no_faults in drivers
# that have their own — then fails the row. That is the only layer with
# unconditional authority over a row's verdict, and it is reached on every
# non-crash path.
#
# Requires a `_stmux` in scope; supplies the standard one if the caller has none.

[[ -n "${_E2E_CAPTURE_PANE_SH:-}" ]] && return 0
_E2E_CAPTURE_PANE_SH=1

if ! declare -F _stmux >/dev/null 2>&1; then
    _stmux() { tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"; }
fi

# Choose and truncate the fault ledger, once, in the PARENT shell — the only
# place the choice can be recorded for the subshells that will write to it.
#
# Truncation is what actually keeps one row's fault from failing the next. Rows
# run as `( . "$LIB"; ... )` SUBSHELLS of scripts/e2e-matrix.sh, where `$$` is
# the driver's PID, so every row derives the SAME default path — and the driver
# has no parallelism, so sequential truncation is sufficient and correct. It also
# covers PID recycling: a stale ledger from a dead process with the same PID
# would otherwise fail an innocent row for a fault it never had.
#
# But truncation must not erase a LIVE parent's faults, which is possible the
# moment a capture-using script invokes another one. Ownership is stamped
# alongside the path and the truncation is skipped when the inherited owner is
# still this process tree's, so a nested script appends to its parent's ledger
# instead of clearing it.
#
# Writability is PROBED rather than assumed. An unrecordable fault is the
# sneakiest route back to the defect: it needs no `|| true` anywhere, it just
# forgets. So a refused path falls back, and if BOTH refuse the run is marked
# unprovable and capture_pane_assert_no_faults fails unconditionally — "I cannot
# show this run was clean" must never read as "this run was clean".
#
# Only builtins: e2e-common.sh (which sources this) is itself sourced with PATH
# scrubbed by the matrix driver's own unit suite, so mktemp/touch/rm are not
# available at this point.
_E2E_CAPTURE_LEDGER_OK=yes
_e2e_capture_ledger_init() {
    local want=${E2E_CAPTURE_FAULT_FILE:-}
    local fallback="${TMPDIR:-/tmp}/e2e-capture-fault.$$"
    local inherited_owner=${E2E_CAPTURE_LEDGER_OWNER:-}
    [ -n "$want" ] || want=$fallback
    local p
    for p in "$want" "$fallback"; do
        # An inherited ledger from a still-live owner is APPENDED to, not
        # truncated: clearing it would delete the parent's faults and hand the
        # parent a clean bill of health it did not earn.
        if [ -n "$inherited_owner" ] && [ "$p" = "$want" ] && [ -d "/proc/$inherited_owner" ]; then
            if { : >>"$p"; } 2>/dev/null; then
                E2E_CAPTURE_FAULT_FILE=$p
                export E2E_CAPTURE_FAULT_FILE E2E_CAPTURE_LEDGER_OWNER
                return 0
            fi
        elif { : >"$p"; } 2>/dev/null; then
            if [ "$p" != "$want" ]; then
                echo "  WARN: capture-fault ledger '$want' is not writable; using '$p'." >&2
                echo "        A fault that cannot be recorded silently restores the QUM-957 vacuous green," >&2
                echo "        so this falls back rather than continuing without a ledger." >&2
            fi
            E2E_CAPTURE_FAULT_FILE=$p
            E2E_CAPTURE_LEDGER_OWNER=$$
            export E2E_CAPTURE_FAULT_FILE E2E_CAPTURE_LEDGER_OWNER
            return 0
        fi
    done
    echo "  WARN: no writable capture-fault ledger ('$want' and '$fallback' both refused)." >&2
    echo "        This run cannot PROVE its pane captures succeeded, so every negative pane" >&2
    echo "        result in it is unattributable. The run is failed rather than trusted." >&2
    E2E_CAPTURE_FAULT_FILE=$want
    E2E_CAPTURE_LEDGER_OWNER=$$
    export E2E_CAPTURE_FAULT_FILE E2E_CAPTURE_LEDGER_OWNER
    _E2E_CAPTURE_LEDGER_OK=no
    return 1
}
# Assigned in the PARENT shell, so the aggregator — which also runs in the
# parent — can read it. That is the one thing a subshell could not do, and the
# reason the fault records themselves need a file.
_e2e_capture_ledger_init || true

# Record a capture failure and explain it once per session.
#
# Throttled per SESSION, not per process: a 240s wait_for_pattern against a dead
# session polls hundreds of times, and hundreds of identical blocks would bury
# the verdict they exist to surface — a diagnostic that reads as noise gets
# scrolled past. Throttling on "have I faulted at all" instead would hide the
# second of two dead sessions, so the ledger itself is the throttle state (a
# subshell cannot keep a counter its parent will see).
_e2e_capture_fault() {
    local session=$1 rc=$2 errfile=$3 form=$4
    local ledger=${E2E_CAPTURE_FAULT_FILE:-}
    local seen=no
    if [ -n "$ledger" ] && [ -f "$ledger" ] && grep -qF -- "session=$session " "$ledger" 2>/dev/null; then
        seen=yes
    fi
    if [ "$seen" = yes ]; then
        return 0
    fi
    # One line per faulting SESSION. A line per attempt would make the row's
    # summary "capture FAULTED 240 time(s)" for one dead pane polled to a
    # deadline, which reads as a flood rather than as one fact.
    if [ -n "$ledger" ]; then
        { printf 'session=%s tmux_exit=%s form=%s\n' "$session" "$rc" "$form" >>"$ledger"; } 2>/dev/null
    fi
    local said=""
    [ -n "$errfile" ] && [ -r "$errfile" ] && said=$(tr '\n' ' ' <"$errfile" 2>/dev/null)
    {
        echo "  CAPTURE FAULT: tmux could not read the pane, so any \"must NOT appear\" verdict"
        echo "                 against it would be vacuous rather than true."
        echo "    session:   $session"
        echo "    socket:    ${SPRAWL_TMUX_SOCKET:-(tmux default)}"
        echo "    form:      $form"
        echo "    tmux exit: $rc"
        echo "    tmux said: ${said:-(nothing on stderr)}"
        echo "    (further faults for this session are recorded but not re-printed)"
    } >&2
    return 0
}

# The one capture primitive. $1 = a label for the diagnostic ("pane",
# "pane -e", ...); remaining args are passed to `tmux capture-pane` verbatim.
#
# tmux's stderr goes to a FILE, never to `2>&1`: folding it into stdout on a
# SUCCESSFUL capture would turn a tmux warning into what a caller reads as pane
# content, and a presence assertion could then match text tmux invented about
# its own state. stdout is left connected directly to the caller so a successful
# capture is byte-identical to the pre-fix helper's output — no command
# substitution, so no stripped trailing blank pane rows.
_e2e_capture_run() {
    local form=$1
    shift
    local session="" prev=""
    local a
    for a in "$@"; do
        [ "$prev" = "-t" ] && session=$a
        prev=$a
    done
    # $BASHPID, not $$: capture_pane runs inside pipeline subshells, and two
    # captures in one pipeline would otherwise share (and truncate) one errfile.
    local errfile="${E2E_CAPTURE_FAULT_FILE:-${TMPDIR:-/tmp}/e2e-capture-fault.$$}.err.${BASHPID:-$$}"
    local rc
    if { : >"$errfile"; } 2>/dev/null; then
        _stmux capture-pane "$@" 2>"$errfile"
        rc=$?
    else
        # No spool available. tmux's stderr goes straight through to the
        # operator's rather than to /dev/null: the diagnostic is the thing this
        # library exists to stop discarding, and losing it because a temp file
        # could not be created would be the original defect with a new cause.
        # The trade-off is interleaving with the pane content's consumer, which
        # is strictly better than silence.
        errfile=""
        _stmux capture-pane "$@"
        rc=$?
    fi
    if [ "$rc" -ne 0 ]; then
        _e2e_capture_fault "$session" "$rc" "$errfile" "$form"
    fi
    # Truncate with a builtin FIRST: `rm` is external and this library is
    # sourced on PATH-scrubbed runs, so the unlink is best-effort while the
    # truncation is not. Without it a spool would keep one row's tmux stderr
    # visible to the next capture that reused the path.
    if [ -n "$errfile" ]; then
        { : >"$errfile"; } 2>/dev/null
        rm -f -- "$errfile" 2>/dev/null
    fi
    return "$rc"
}

# capture_pane SESSION — the visible pane, as text.
# Returns 0 and the pane's contents on success (an empty pane is a SUCCESS with
# empty output); returns tmux's nonzero status and records a fault otherwise.
capture_pane() {
    _e2e_capture_run "capture-pane -p" -t "$1" -p
}

# capture_pane_ansi SESSION — as capture_pane, with escape sequences retained,
# for assertions about rendered attributes (dim/bright/faint).
capture_pane_ansi() {
    _e2e_capture_run "capture-pane -e -p" -t "$1" -e -p
}

# capture_pane_scrollback SESSION LINES — as capture_pane, including the last
# LINES rows of scrollback history.
capture_pane_scrollback() {
    local session=$1 lines=${2:-200}
    case "$lines" in
        '' | *[!0-9]*)
            echo "  FATAL: capture_pane_scrollback: LINES must be a whole number, got '$lines'" >&2
            return 2
            ;;
    esac
    _e2e_capture_run "capture-pane -p -S -$lines" -t "$session" -p -S "-$lines"
}

# capture_pane_best_effort SESSION — the SANCTIONED opt-out. Always returns 0,
# yields nothing when the pane is unreadable, and records no fault.
#
# It exists so that the handful of sites which capture a pane they have just
# killed on purpose (teardown diagnostics) have a NAMED, greppable, reviewable
# way to say so. The alternative an author reaches for is `|| true`, and that is
# the defect. Never use this where the capture's content decides a verdict.
capture_pane_best_effort() {
    _stmux capture-pane -t "$1" -p 2>/dev/null
    return 0
}

# capture_pane_ansi_best_effort SESSION — as capture_pane_best_effort, with
# escape sequences retained. Same contract, same warning.
capture_pane_ansi_best_effort() {
    _stmux capture-pane -t "$1" -e -p 2>/dev/null
    return 0
}

# capture_pane_dump SESSION [LINES] [--ansi] — forensic pane dump to stderr, for
# a path that has ALREADY decided a verdict. Never fails, never faults the row.
#
# This replaces `capture_pane "$S" | tail -N >&2`, which is a trap in every
# driver that runs `set -euo pipefail`: `pipefail` promotes the capture's new
# nonzero status to the pipeline's, and `set -e` then kills the script AT THE
# DUMP — skipping the summary and, worse, skipping the capture-fault gate that
# would have explained the abort. Nine drivers plus scripts/test-tui-e2e.sh are
# `set -euo pipefail`; matrix rows are immune only because e2e-matrix.sh runs
# them as an `if` condition.
#
# It is also the honest answer to a dump against a pane that is legitimately
# gone: it SAYS the pane could not be read, where `| tail -N >&2` printed
# nothing and left the reader to assume an empty pane.
#
# Do not use it where the content decides a verdict — that is what capture_pane
# and e2e_pane_lacks are for.
capture_pane_dump() {
    local session=$1 lines=${2:-30} mode=${3:-}
    local out
    if [ "$mode" = "--ansi" ]; then
        out=$(capture_pane_ansi_best_effort "$session")
    else
        out=$(capture_pane_best_effort "$session")
    fi
    case "$lines" in
        '' | *[!0-9]*) lines=30 ;;
    esac
    if [ -z "$out" ]; then
        echo "  (pane dump: nothing captured for session '$session' — it is empty or gone)" >&2
        return 0
    fi
    printf '%s\n' "$out" | tail -n "$lines" >&2
    return 0
}

# e2e_require_session_alive SESSION [CONTEXT] — liveness precondition for
# absence assertions. Records a pass()/fail() and returns 0/1.
#
# Prints the subject (session, socket, the actual has-session status) BEFORE it
# judges, so a misaimed probe is visible in the log rather than inferred from a
# verdict about the wrong session.
e2e_require_session_alive() {
    local session=$1 context=${2:-}
    local rc
    _stmux has-session -t "$session" 2>/dev/null
    rc=$?
    echo "  liveness probe: session='$session' socket='${SPRAWL_TMUX_SOCKET:-(tmux default)}' has-session exit=$rc"
    if [ "$rc" -eq 0 ]; then
        pass "tmux session '$session' is alive${context:+ ($context)}"
        return 0
    fi
    fail "tmux session '$session' is NOT alive${context:+ ($context)} — has-session exit=$rc; every absence assertion against it would be vacuous"
    return 1
}

# e2e_pane_lacks SESSION PATTERN DESC — assert PATTERN (an ERE) does not appear
# on SESSION's visible pane. Records a pass()/fail() and returns 0/1.
#
# Use this instead of `if capture_pane S | grep -q PAT; then fail; else pass; fi`.
# That idiom cannot tell "the pattern is absent" from "I could not look", and
# reports the second as the first. Here an unreadable pane is its own verdict,
# worded so a reader cannot mistake it for proof of absence.
e2e_pane_lacks() {
    local session=$1 pattern=$2 desc=$3
    local pane rc=0
    # capture_pane's own fault diagnostic is deliberately NOT suppressed here:
    # the verdict below says the pane was unreadable, and the diagnostic is what
    # says WHY. Only the pane text is captured; stderr goes to the operator.
    rc=0
    pane=$(capture_pane "$session") || rc=$?
    echo "  absence probe: session='$session' capture exit=$rc bytes=${#pane} pattern='$pattern'"
    if [ "$rc" -ne 0 ]; then
        fail "$desc — CANNOT JUDGE: the pane could not be read (tmux exit=$rc), so the pattern's absence is UNPROVEN, not proven"
        return 1
    fi
    if printf '%s\n' "$pane" | grep -qE -- "$pattern"; then
        fail "$desc — pattern '$pattern' IS present on the live pane"
        return 1
    fi
    pass "$desc"
    return 0
}

# capture_pane_assert_no_faults — the row-level gate, for drivers with their own
# aggregator. Prints every recorded fault and returns 1 if there were any.
#
# scripts/lib/e2e-common.sh calls this from e2e_print_results, so matrix rows get
# it for free. The nine standalone scripts/test-*-e2e.sh drivers do not source
# e2e-common.sh and call it from their own summary block.
capture_pane_assert_no_faults() {
    local ledger=${E2E_CAPTURE_FAULT_FILE:-}
    # No ledger at all means no evidence either way, and "no evidence" must not
    # read as "clean" — that is this whole issue in one line. Unconditional, and
    # deliberately checked BEFORE the emptiness test: an absent ledger is also an
    # empty one.
    if [ "${_E2E_CAPTURE_LEDGER_OK:-yes}" != yes ]; then
        {
            echo "  FAIL: no writable capture-fault ledger for this run (QUM-957)."
            echo "        A pane capture that failed could not have been recorded, so this run"
            echo "        cannot demonstrate that its \"must NOT appear on the pane\" results were"
            echo "        measured rather than merely unobserved. Failing rather than trusting."
        } >&2
        return 1
    fi
    if [ -z "$ledger" ] || [ ! -s "$ledger" ]; then
        return 0
    fi
    local n
    n=$(grep -c . "$ledger" 2>/dev/null) || n="?"
    {
        echo "  FAIL: tmux capture FAULTED $n time(s) during this run (QUM-957)."
        echo "        Every \"must NOT appear on the pane\" check made against an unreadable"
        echo "        pane was satisfied by the absence of a pane, not by the absence of the"
        echo "        pattern. This run's negative results are NOT evidence."
        sed 's/^/          /' "$ledger" 2>/dev/null
    } >&2
    return 1
}

# capture_pane_cleanup — remove this process's ledger and stderr spools.
#
# Call it from a driver's EXIT trap. The ledger is CREATED at source time (that
# is how writability is probed and how a pid-recycled leftover is neutralised),
# so a driver that never faults still leaves a zero-byte file behind — and this
# lib is sourced by hundreds of short-lived processes in the unit suite, which
# turned into 877 of them in a /tmp that CLAUDE.md says is shared with other
# agents and with host tooling.
#
# Path-guarded before deleting, per the repo's /tmp rules: assert, then delete,
# and never a bare glob on an unvalidated variable. `rm` is external, so this is
# best-effort by nature — which is fine, because losing the cleanup only costs a
# zero-byte file, whereas losing the LEDGER would cost a verdict. That asymmetry
# is why the ledger is not created lazily to avoid the litter.
capture_pane_cleanup() {
    local ledger=${E2E_CAPTURE_FAULT_FILE:-}
    [ -n "$ledger" ] || return 0
    case "$ledger" in
        /tmp/* | "${TMPDIR:-/nonexistent}"/*) : ;;
        *) return 0 ;;
    esac
    command -v rm >/dev/null 2>&1 || return 0
    rm -f -- "$ledger" "$ledger".err.* 2>/dev/null
    return 0
}

# e2e_capture_fault_reset — clear the recorded faults.
#
# For the ONE row that faults on purpose (scripts/e2e-tests/capture-pane-liveness.sh
# drives a dead session as its positive control) and must therefore not fail
# itself. It clears history only: a fault occurring after a reset still fails the
# row, so this cannot be used to disarm the mechanism ahead of time. Section
# [18t] of the unit suite pins the set of files allowed to call it.
e2e_capture_fault_reset() {
    local ledger=${E2E_CAPTURE_FAULT_FILE:-}
    [ -n "$ledger" ] || return 0
    { : >"$ledger"; } 2>/dev/null
    return 0
}
