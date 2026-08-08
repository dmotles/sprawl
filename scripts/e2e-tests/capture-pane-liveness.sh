#!/usr/bin/env bash
# scripts/e2e-tests/capture-pane-liveness.sh — QUM-957 control arms against REAL
# tmux.
#
# The defect: `capture_pane() { _stmux capture-pane -t "$1" -p 2>/dev/null || true; }`
# discarded tmux's stderr and its exit status, so a capture against a dead
# session returned empty-stdout-with-exit-0 and every "pattern must NOT appear on
# the pane" assertion in the harness was satisfied by there being no pane.
#
# Section [18] of scripts/test-e2e-matrix-unit.sh pins the whole contract against
# a fake tmux, because that suite runs inside `make validate` and must not depend
# on tmux. What a shim cannot establish is what the REAL binary does, and the arm
# that matters most is precisely a real-world one:
#
#   NEGATIVE CONTROL (subject known clean -> the probe MUST stay quiet)
#     A genuinely live tmux session whose pane is genuinely blank. If this goes
#     red, someone collapsed "tmux succeeded and the pane is empty" into "tmux
#     failed" — at which point a legitimately-empty call site starts failing,
#     the next author quiets it with `|| true`, and the defect returns with a
#     commit blessing it. That is the realistic regression, and this is the arm
#     that catches it.
#
#   POSITIVE CONTROL (defect present -> the probe MUST fire)
#     The same session after `kill-session`. Pre-fix: rc 0, no output, and
#     e2e_pane_lacks' ancestor idiom reported PASS. Post-fix: nonzero, a
#     diagnostic naming the session, a ledger entry, and a failed row.
#
# No claude, no sprawl binary, no TUI: the failure mode needs only a MISSING TMUX
# SESSION. That keeps this row fast and keeps it runnable when claude is
# unavailable — the rows that need claude are inert while other agents are active
# (QUM-1143), and a control arm that inherits that constraint is a control arm
# nobody runs.
#
# This row FAULTS ON PURPOSE, so it is the one file allowed to call
# e2e_capture_fault_reset. Section [18t] pins that allowlist.

# QUM-1029: assertions a COMPLETE, PASSING run makes. Counted over the green
# path only, and verified against a real run rather than by arithmetic:
#   Arm 1  4 — blank-pane rc, blank-pane emptiness, ledger clean, e2e_pane_lacks quiet
#   Arm 2  3 — marker captured, e2e_pane_lacks fires on a present pattern, scrollback ok
#   Arm 3  4 — post-kill rc, diagnostic, ledger entry, e2e_pane_lacks fails
#   Arm 4  2 — best-effort stays quiet, a fault after the reset is recorded again
# The two early `return 1` bails (session would not create / survived kill-session)
# have already called fail(), so they are outside the minimum by the same rule the
# aggregator documents.
MIN_ASSERTIONS=13

test_metadata() {
    echo "needs_tmux=1"
}

# Run a helper that records its own pass()/fail() in a SUBSHELL and report only
# its status, so a deliberately-failing probe cannot pollute this row's counters.
# $1 = expected: "pass" or "fail"; $2 = description; rest = command.
_cpl_expect() {
    local expected=$1 desc=$2
    shift 2
    local out rc
    out=$( ("$@") 2>&1 )
    rc=$?
    printf '%s\n' "$out" | sed 's/^/      | /'
    if [ "$expected" = pass ]; then
        if [ "$rc" -eq 0 ]; then pass "$desc"; else fail "$desc (helper returned $rc, want 0)"; fi
    else
        if [ "$rc" -ne 0 ]; then pass "$desc"; else fail "$desc (helper returned 0, want nonzero)"; fi
    fi
}

# Print the subject before judging it. Three separate misaimed controls in this
# work stream were caught by exactly this, and by nothing else: a probe that
# names only its verdict cannot be checked against the thing it looked at.
_cpl_subject() {
    local label=$1 session=$2
    local hs
    _stmux has-session -t "$session" 2>/dev/null
    hs=$?
    echo "  --- subject ($label) ---"
    echo "    session:          $session"
    echo "    socket:           ${SPRAWL_TMUX_SOCKET:-(tmux default)}"
    echo "    has-session exit: $hs"
    echo "    ledger:           ${E2E_CAPTURE_FAULT_FILE:-(unset)}"
    # grep -c prints 0 AND exits 1 when nothing matches, so `|| echo 0` would
    # print the count twice. Capture first, then default.
    local lines
    lines=$(grep -c . "${E2E_CAPTURE_FAULT_FILE:-/nonexistent}" 2>/dev/null)
    echo "    ledger lines:     ${lines:-0}"
    echo "    live sessions:    $(_stmux list-sessions 2>&1 | tr '\n' ';')"
}

test_run() {
    e2e_require_tmux
    e2e_setup_tmux_socket "sprawl-capture-liveness"
    e2e_make_sandbox_root "sprawl-qum957"
    e2e_install_cleanup_traps

    local SESSION="sprawl-capture-liveness-$$"
    local MARKER="CPL_MARKER_$$"

    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo ""

    # A genuinely live session whose pane is genuinely blank. `clear` then an
    # idle loop: no shell prompt, no banner, nothing for tmux to report — the
    # state the pre-fix helper was indistinguishable from a dead session in.
    echo "=== Arm 1: NEGATIVE CONTROL — live session, legitimately EMPTY pane ==="
    if ! _stmux new-session -d -s "$SESSION" -x 80 -y 24 \
        'sh -c "clear; while :; do sleep 1; done"'; then
        fail "could not create the tmux session — the control arms cannot run"
        e2e_print_results
        return 1
    fi
    # tmux reports the session before the pane's `clear` has necessarily landed.
    # Poll for genuine emptiness rather than sleeping a guess; if it never
    # settles, say so instead of asserting against a transient banner.
    local waited=0 pane=""
    while [ "$waited" -lt 15 ]; do
        pane=$(capture_pane "$SESSION" | tr -d ' \n')
        [ -z "$pane" ] && break
        sleep 1
        waited=$((waited + 1))
    done
    _cpl_subject "live, blank pane" "$SESSION"

    local out rc
    out=$(capture_pane "$SESSION")
    rc=$?
    echo "    capture exit=$rc bytes=${#out}"
    if [ "$rc" -eq 0 ]; then
        pass "NEGATIVE CONTROL: capture_pane on a LIVE session returns 0 (exit=$rc)"
    else
        fail "NEGATIVE CONTROL: capture_pane on a LIVE session returned $rc — an empty pane is being treated as a tmux failure, which is the regression this row exists to catch"
    fi
    if [ -z "$(printf '%s' "$out" | tr -d ' \n')" ]; then
        pass "NEGATIVE CONTROL: the blank pane yields no content (nothing fabricated)"
    else
        fail "NEGATIVE CONTROL: expected a blank pane after ${waited}s but captured ${#out} bytes; the arms below would not be testing an EMPTY pane: $(printf '%s' "$out" | head -c 200)"
    fi
    if [ -s "${E2E_CAPTURE_FAULT_FILE:-/nonexistent}" ]; then
        fail "NEGATIVE CONTROL: an empty-but-live pane wrote to the capture-fault ledger: $(cat "$E2E_CAPTURE_FAULT_FILE")"
    else
        pass "NEGATIVE CONTROL: an empty-but-live pane leaves the capture-fault ledger clean"
    fi
    _cpl_expect pass "NEGATIVE CONTROL: e2e_pane_lacks stays quiet on a live, blank pane" \
        e2e_pane_lacks "$SESSION" "Session Error" "no error banner on a blank live pane"

    # A live pane WITH content: the helper must still be able to see things, or
    # the arms above would be satisfied by a capture that never works at all.
    echo ""
    echo "=== Arm 2: NEGATIVE CONTROL — live session with real content ==="
    _stmux send-keys -t "$SESSION" "echo $MARKER" Enter
    if wait_for_substring_fast "$SESSION" "$MARKER" 15; then
        pass "NEGATIVE CONTROL: real pane content is captured from a live session"
    else
        fail "NEGATIVE CONTROL: marker '$MARKER' never appeared — capture_pane cannot read a live pane at all, so this row's absence arms prove nothing"
    fi
    _cpl_subject "live, with content" "$SESSION"
    _cpl_expect fail "e2e_pane_lacks FIRES when the pattern IS present on a live pane" \
        e2e_pane_lacks "$SESSION" "$MARKER" "marker must be absent (deliberately false)"
    if capture_pane_scrollback "$SESSION" 200 >/dev/null; then
        pass "NEGATIVE CONTROL: capture_pane_scrollback succeeds against a live session"
    else
        fail "NEGATIVE CONTROL: capture_pane_scrollback failed against a LIVE session (tmux exit $?)"
    fi

    # --- POSITIVE CONTROL: the defect's own conditions --------------------
    echo ""
    echo "=== Arm 3: POSITIVE CONTROL — the session is killed; every capture must go loud ==="
    _stmux kill-session -t "$SESSION" 2>/dev/null
    # kill-session signals the pane; wait until tmux genuinely cannot find it,
    # so a slow teardown cannot make this arm assert against a still-live pane.
    waited=0
    while [ "$waited" -lt 15 ] && _stmux has-session -t "$SESSION" 2>/dev/null; do
        sleep 1
        waited=$((waited + 1))
    done
    _cpl_subject "after kill-session" "$SESSION"
    if _stmux has-session -t "$SESSION" 2>/dev/null; then
        fail "POSITIVE CONTROL: the session survived kill-session after ${waited}s — the arms below would be testing a LIVE pane and would prove nothing"
        e2e_print_results
        return 1
    fi

    local err_spool="$SPRAWL_ROOT/capture-fault.err"
    out=$(capture_pane "$SESSION" 2>"$err_spool")
    rc=$?
    echo "    capture exit=$rc bytes=${#out}"
    if [ "$rc" -ne 0 ]; then
        pass "POSITIVE CONTROL: capture_pane against a killed session returns nonzero (exit=$rc)"
    else
        fail "POSITIVE CONTROL: capture_pane against a killed session returned 0 — the QUM-957 swallow is back, and every negative pane assertion in the harness is vacuous again"
    fi
    if grep -qF "$SESSION" "$err_spool" 2>/dev/null && grep -qF "tmux exit" "$err_spool" 2>/dev/null; then
        pass "POSITIVE CONTROL: the fault diagnostic names the session and tmux's exit status"
    else
        fail "POSITIVE CONTROL: the fault diagnostic did not name both the session and the tmux exit status: $(head -c 400 "$err_spool" 2>/dev/null)"
    fi
    if grep -qF "session=$SESSION" "${E2E_CAPTURE_FAULT_FILE:-/nonexistent}" 2>/dev/null; then
        pass "POSITIVE CONTROL: the fault is recorded in the ledger, so the row cannot report green"
    else
        fail "POSITIVE CONTROL: no ledger entry for the killed session; the aggregator would let this row pass"
    fi
    _cpl_expect fail "POSITIVE CONTROL: e2e_pane_lacks FAILS on a killed session instead of passing vacuously" \
        e2e_pane_lacks "$SESSION" "Session Error" "no error banner on a dead pane"

    # The sanctioned opt-out, exercised where it is actually used: a teardown
    # diagnostic reading a pane it just killed. It must stay silent, or the next
    # author reaches for `|| true` and reinstates the defect.
    echo ""
    echo "=== Arm 4: the named opt-out for deliberately-dead panes ==="
    e2e_capture_fault_reset
    capture_pane_best_effort "$SESSION" >/dev/null
    rc=$?
    if [ "$rc" -eq 0 ] && [ ! -s "${E2E_CAPTURE_FAULT_FILE:-/nonexistent}" ]; then
        pass "capture_pane_best_effort on a killed session is quiet and ledger-free (exit=$rc)"
    else
        fail "capture_pane_best_effort on a killed session was not quiet (exit=$rc, ledger: $(cat "${E2E_CAPTURE_FAULT_FILE:-/dev/null}" 2>/dev/null))"
    fi

    # The reset clears HISTORY; it must not disarm the gate. Proven by faulting
    # again after it and observing the ledger repopulate — otherwise this row's
    # own use of the reset would be a hole big enough to hide the whole defect.
    capture_pane "$SESSION" >/dev/null 2>&1
    if [ -s "${E2E_CAPTURE_FAULT_FILE:-/nonexistent}" ]; then
        pass "a fault AFTER e2e_capture_fault_reset is recorded again (the reset clears history, it does not disarm the gate)"
    else
        fail "a fault after e2e_capture_fault_reset was NOT recorded — the reset permanently disarms the mechanism"
    fi

    # This row faulted on purpose. Clear the ledger LAST, immediately before the
    # aggregator, so its deliberate positive controls do not fail it.
    e2e_capture_fault_reset

    e2e_print_results
}
