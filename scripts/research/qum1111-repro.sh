#!/usr/bin/env bash
# scripts/research/qum1111-repro.sh — QUM-1111 LIVE REPRO ATTEMPT (research only).
#
# NOT a matrix row. Not wired into `make validate`. This is ghost's research
# harness for QUM-1111 and is expected to be deleted or rewritten by whoever
# lands the fix.
#
# WHAT IT TRIES TO REPRODUCE
#   A prompt submitted mid-turn (queues at priority "next") and then flushed
#   with Ctrl+G (send-all-now: cancel uuid A, resubmit identical text as a new
#   uuid B at priority "now") is received and answered by the model, but the TUI
#   keeps rendering it as a PENDING (faint) pending-zone bubble forever.
#
# THE SHAPE MATTERS (from the live report, not the simple case):
#   * long turn with real TOOL CALLS in flight (not pure streaming), and
#   * an EARLIER mid-turn message in the SAME turn already settled normally,
#     so the turn is NOT "acked-nothing" and QUM-1000's settleNeverAcked sweep
#     is out of scope by design.
#
# TWO INDEPENDENT ASSERTIONS ON THE STUCK STATE (deliberately not one):
#   A1 SGR: the sentinel's `›` glyph line carries faint (\x1b[2m) rather than
#      bold (\x1b[1m). This is the ONLY styling delta for a USER bubble —
#      QUM-925 F3's `┊` vs `│` gutter exists on SystemNotificationItem only, so
#      a user bubble has NO SGR-strip-surviving styling differentiator. That is
#      a real coverage gap in the pending-dim contract, recorded in the findings.
#   A2 STRUCTURE (survives an SGR strip): buildRender appends the pending zone
#      AFTER the committed items. So a still-pending bubble sits BELOW the
#      assistant's reply to it; a settled one sits ABOVE. Asserting the ORDER of
#      the sentinel line vs the assistant's answer token is SGR-independent and
#      cannot be satisfied by mere containment.
#
# BUS→REDUCER OBSERVABILITY
#   The session wire log shows only the CLI side. Whether the RuntimeEvent was
#   (a) published, (b) delivered to the tui-viewport subscriber, (c) processed
#   by the reducer, are THREE different claims. $SPRAWL_QUM1111_TRACE turns on
#   temporary instrumentation (internal/qum1111trace) that records all three.
#
# Usage: bash scripts/research/qum1111-repro.sh [iterations]
#   Exit 0 = NOT reproduced across all iterations (every Ctrl+G'd bubble settled)
#   Exit 1 = REPRODUCED (a bubble stayed pending) or harness failure

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/e2e-common.sh
. "$REPO_ROOT/scripts/lib/e2e-common.sh"

ITERS="${1:-4}"

capture_ansi() { _stmux capture-pane -t "$1" -e -p 2>/dev/null || true; }

# ---------------------------------------------------------------------------
# Pure evaluators. These take PANE TEXT on stdin rather than a session, so the
# self-test below can drive them with synthetic panes and prove each one CAN
# report the broken verdict. An assertion nobody has watched fail is a claim,
# not a check (CLAUDE.md).
# ---------------------------------------------------------------------------

# attr_of SENTINEL  (ANSI pane text on stdin) -> faint | bold | none
# renderUserPromptBlock puts `›` on the first line of a user prompt block and
# on nothing else, so keying on the glyph line cannot match assistant prose that
# happens to quote the sentinel.
attr_of() {
    local ln
    ln="$(grep -aF "$1" | grep -aF '›' || true)"
    [ -z "$ln" ] && { echo none; return; }
    if grep -qaP "\x1b\[2m" <<<"$ln"; then echo faint; return; fi
    if grep -qaP "\x1b\[1m" <<<"$ln"; then echo bold; return; fi
    echo none
}

# order_of SENTINEL ANSWER  (PLAIN pane text on stdin) -> settled | pending | na
# buildRender appends the pending zone AFTER the committed items, so a bubble
# still held in the zone renders BELOW the model's own answer to it, and a
# settled one renders ABOVE. Both texts are present either way — only the ORDER
# discriminates, which is why this is not a containment assertion and why it
# survives an SGR strip.
order_of() {
    local sent="$1" ans="$2" plain b a
    plain="$(cat)"
    b="$(grep -n -- "› ${sent}" <<<"$plain" | tail -1 | cut -d: -f1)"
    a="$(grep -n -- "${ans}" <<<"$plain" | grep -v -- '› ' | tail -1 | cut -d: -f1)"
    if [ -z "$b" ] || [ -z "$a" ]; then echo na; return; fi
    if [ "$b" -lt "$a" ]; then echo settled; else echo pending; fi
}

# self_test — negative controls. Drives both evaluators with synthetic panes and
# demands each report the BROKEN verdict for a broken pane and the WORKING
# verdict for a working one. Without this, a run that reports "not reproduced"
# is indistinguishable from a run whose evaluators never fire.
self_test() {
    local esc; esc=$(printf '\033')
    local ok=1 got

    got="$(printf '%s[2m\xe2\x80\xba%s[0m SENT1 pending\n' "$esc" "$esc" | attr_of SENT1)"
    [ "$got" = faint ] || { echo "  SELFTEST FAIL: attr_of faint -> $got" >&2; ok=0; }
    got="$(printf '%s[1m\xe2\x80\xba%s[0m SENT1 committed\n' "$esc" "$esc" | attr_of SENT1)"
    [ "$got" = bold ] || { echo "  SELFTEST FAIL: attr_of bold -> $got" >&2; ok=0; }
    got="$(printf 'no bubble here SENT1\n' | attr_of SENT1)"
    [ "$got" = none ] || { echo "  SELFTEST FAIL: attr_of none -> $got" >&2; ok=0; }

    # BROKEN pane: answer committed above, bubble stranded in the zone tail.
    got="$(printf 'header\n  ANSTOK\n\xe2\x80\xba SENT1 hello\n' | order_of SENT1 ANSTOK)"
    [ "$got" = pending ] || { echo "  SELFTEST FAIL: order_of pending -> $got" >&2; ok=0; }
    # WORKING pane: bubble relocated into the committed transcript, answer after.
    got="$(printf 'header\n\xe2\x80\xba SENT1 hello\n  ANSTOK\n' | order_of SENT1 ANSTOK)"
    [ "$got" = settled ] || { echo "  SELFTEST FAIL: order_of settled -> $got" >&2; ok=0; }

    if [ "$ok" = 1 ]; then
        pass "self-test: both evaluators distinguish a pending pane from a settled one (negative control)"
        return 0
    fi
    fail "self-test: evaluators did not distinguish pending from settled — harness is not measuring anything"
    return 1
}

# bubble_attr SESSION SENTINEL -> faint | bold | none
bubble_attr() { capture_ansi "$1" | attr_of "$2"; }

# wait_attr SESSION SENTINEL WANT TIMEOUT
wait_attr() {
    local end=$((SECONDS + $4))
    while [ "$SECONDS" -lt "$end" ]; do
        [ "$(bubble_attr "$1" "$2")" = "$3" ] && return 0
        sleep 0.3
    done
    return 1
}

busy() { capture_pane "$1" | grep -qiE "thinking|streaming"; }

wait_idle() {
    local end=$((SECONDS + $2))
    while [ "$SECONDS" -lt "$end" ]; do
        busy "$1" || return 0
        sleep 0.5
    done
    return 1
}

# launch_tui_traced SESSION — e2e_launch_tui plus SPRAWL_QUM1111_TRACE in the
# child env (the shared helper does not forward it).
launch_tui_traced() {
    # A taller pane keeps the sentinel bubble inside the TUI viewport: a long
    # tool-bound turn prints ~12 results, and at 50 rows the M2 bubble scrolls
    # out, which the evaluators can only report as attr=none — indistinguishable
    # from "vanished". Override with QUM1111_ROWS.
    local session="$1" cols=200 rows="${QUM1111_ROWS:-50}"
    local stderr_log="${SPRAWL_ROOT}/.sprawl/tui-stderr.log"
    e2e_wait_weave_lock_free "$SPRAWL_ROOT" || return 1
    _stmux new-session -d -s "$session" -x "$cols" -y "$rows" \
        "SPRAWL_ROOT='$SPRAWL_ROOT' SPRAWL_QUM1111_TRACE='$TRACE_ENV' SPRAWL_CLAUDE='$SPRAWL_CLAUDE' '$SPRAWL_BIN' enter 2>'$stderr_log'"
    _stmux set-option -t "$session" window-size manual >/dev/null
    _stmux resize-window -t "$session" -x "$cols" -y "$rows" >/dev/null
    if ! wait_for_pattern "$session" "weave " 45; then
        echo "  FAIL: TUI did not render within 45s" >&2
        capture_pane "$session" | tail -30 >&2
        [ -f "$stderr_log" ] && tail -20 "$stderr_log" >&2
        return 1
    fi
}

REPRODUCED=0
EVALUABLE=0

run_iteration() {
    local session="$1" n="$2" tag M1 M2
    tag="$(head -c4 /dev/urandom | xxd -p)"
    M1="SETTLEA${tag}"   # earlier mid-turn msg — must settle NORMALLY
    M2="STUCKB${tag}"    # the Ctrl+G'd msg — the one under test
    local ANS="ANSWERED${tag}"  # token the model is asked to emit in reply to M2

    echo ""
    echo "=== iteration ${n}/${ITERS}  M1=${M1}  M2=${M2} ==="
    echo "TRACE-MARK iteration=${n} M1=${M1} M2=${M2}" >>"$TRACE"

    # 1. A long, TOOL-BOUND turn (matches the live shape: MCP/tool calls in
    #    flight, not pure token streaming).
    e2e_send_user_prompt "$session" \
        "Run this exact bash command TWELVE times, each in its OWN separate Bash tool call, printing each result: awk 'BEGIN{s=0;for(i=0;i<90000000;i++)s+=i;print s}' . Do not combine them into one call and do not use a loop. IMPORTANT: if additional user messages arrive while you are doing this, answer them in one short line and then CONTINUE with the remaining commands until all twelve are done."
    local bstart=$((SECONDS + 60)) started=0
    while [ "$SECONDS" -lt "$bstart" ]; do
        if busy "$session"; then started=1; break; fi
        sleep 0.3
    done
    if [ "$started" != "1" ]; then
        fail "iter ${n}: busy turn never started (no thinking/streaming label within 60s)"
        capture_pane "$session" | tail -20 >&2
        return 1
    fi
    sleep 3

    # 2. M1 mid-turn at priority next. It must SETTLE inside this turn —
    #    that is the "turn already acked something" precondition.
    e2e_send_user_prompt "$session" "${M1}: reply with the single letter a"
    if wait_attr "$session" "$M1" faint 20; then
        echo "  (M1 rendered pending/faint as expected)"
    else
        echo "  note: iter ${n}: never observed M1 faint (attr=$(bubble_attr "$session" "$M1")) — timing miss" >&2
    fi
    if wait_attr "$session" "$M1" bold 90; then
        pass "iter ${n}: precondition — earlier mid-turn msg M1 settled normally (turn is NOT acked-nothing)"
    else
        echo "  note: iter ${n}: M1 did not settle within 90s (attr=$(bubble_attr "$session" "$M1")); precondition NOT established this iteration" >&2
    fi

    # 3. Still mid-turn? If the turn ended, this iteration cannot exercise the
    #    Ctrl+G-during-turn window at all — say so rather than asserting.
    if ! busy "$session"; then
        echo "  note: iter ${n}: turn ended before M2 could be queued — window missed, iteration inconclusive" >&2
        wait_idle "$session" 60
        return 0
    fi

    # 4. M2 mid-turn, then Ctrl+G before the turn ends.
    e2e_send_user_prompt "$session" "${M2}: reply with exactly the token ${ANS} and nothing else, then carry on"
    sleep 2
    if ! busy "$session"; then
        echo "  note: iter ${n}: turn ended before Ctrl+G — window missed, iteration inconclusive" >&2
        wait_idle "$session" 60
        return 0
    fi
    echo "TRACE-MARK ctrl-g iteration=${n} M2=${M2}" >>"$TRACE"
    _stmux send-keys -t "$session" C-g

    # 5. Let everything settle, then judge.
    wait_idle "$session" 180 || echo "  note: iter ${n}: session still busy at 180s" >&2
    sleep 3

    local attr; attr="$(bubble_attr "$session" "$M2")"
    if [ "$attr" = "bold" ] || [ "$attr" = "faint" ]; then
        EVALUABLE=$((EVALUABLE + 1))
    fi
    if [ "$attr" = "bold" ]; then
        pass "iter ${n}: A1(SGR) Ctrl+G'd bubble ${M2} settled bright (bold)"
    elif [ "$attr" = "faint" ]; then
        fail "iter ${n}: *** REPRODUCED *** A1(SGR) Ctrl+G'd bubble ${M2} is STILL FAINT after the turn ended"
        REPRODUCED=1
    else
        fail "iter ${n}: A1(SGR) no user-bubble glyph line found for ${M2} (attr=none) — vanished or scrolled off"
        capture_pane "$session" | tail -40 >&2
    fi

    # A2 STRUCTURE — SGR-independent; see order_of.
    local verdict
    verdict="$(capture_pane "$session" | order_of "$M2" "$ANS")"
    case "$verdict" in
        settled) pass "iter ${n}: A2(structure, SGR-stripped) ${M2} bubble renders ABOVE its own answer ${ANS} — it left the pending zone" ;;
        pending) fail "iter ${n}: *** REPRODUCED *** A2(structure, SGR-stripped) ${M2} bubble renders BELOW its own answer ${ANS} — still in the pending zone tail"; REPRODUCED=1 ;;
        *)       echo "  note: iter ${n}: A2 not evaluable (bubble and/or answer token not on the pane)" >&2 ;;
    esac
}

main() {
    if ! self_test; then e2e_print_results; return 1; fi

    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-qum1111"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum1111"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    [ -f "$REPO_ROOT/.env" ] && cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    TRACE="${QUM1111_TRACE_OUT:-$SPRAWL_ROOT/qum1111-trace.log}"
    export TRACE
    : >"$TRACE"
    # QUM1111_NO_TRACE=1 launches with the trace package INERT (no file, so no
    # I/O and no mutex on the publish/pump paths). This is the control for
    # "did my instrumentation create or widen the race?" — the A1/A2 pane
    # assertions do not depend on the trace, so the run still has a verdict.
    TRACE_ENV="$TRACE"
    if [ "${QUM1111_NO_TRACE:-}" = "1" ]; then
        TRACE_ENV=""
        echo "  (control run: tracing DISABLED)"
    fi
    export TRACE_ENV

    local SESSION="sprawl-qum1111-$(head -c4 /dev/urandom | xxd -p)"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  TRACE=$TRACE"
    echo "  SESSION=$SESSION"

    launch_tui_traced "$SESSION" || { e2e_print_results; return 1; }
    pass "TUI rendered"
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter; sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION"

    local i
    for i in $(seq 1 "$ITERS"); do
        run_iteration "$SESSION" "$i" || true
    done

    echo ""
    echo "=== pane (plain) tail ==="
    capture_pane "$SESSION" | tail -50
    echo ""
    echo "=== trace: user-message lifecycle lines ==="
    grep -aE "TRACE-MARK|SendAllNow|cancelPendingUser|markConsumed|tui.reducer|ZoneSettle|ZoneDrop|bus.DROP" "$TRACE" | tail -200 || true

    # Preserve artifacts outside the sandbox before the cleanup trap fires.
    if [ -n "${QUM1111_ARTIFACT_DIR:-}" ]; then
        mkdir -p "$QUM1111_ARTIFACT_DIR"
        cp -a "$TRACE" "$QUM1111_ARTIFACT_DIR/trace.log" 2>/dev/null || true
        capture_ansi "$SESSION" >"$QUM1111_ARTIFACT_DIR/pane-ansi.txt" 2>/dev/null || true
        capture_pane "$SESSION" >"$QUM1111_ARTIFACT_DIR/pane-plain.txt" 2>/dev/null || true
        cp -a "$SPRAWL_ROOT/.sprawl/logs" "$QUM1111_ARTIFACT_DIR/logs" 2>/dev/null || true
        echo "  artifacts -> $QUM1111_ARTIFACT_DIR"
    fi

    echo ""
    # A run in which NO iteration was evaluable has not observed the phenomenon
    # either way. Reporting that as "not reproduced" would be the classic
    # non-asserting fallback: a clean-looking verdict backed by zero
    # observations. Say INCONCLUSIVE and fail the run.
    if [ "$REPRODUCED" = "1" ]; then
        echo "=== VERDICT: REPRODUCED ($EVALUABLE/$ITERS iterations evaluable) ==="
    elif [ "$EVALUABLE" -eq 0 ]; then
        echo "=== VERDICT: INCONCLUSIVE — 0/$ITERS iterations were evaluable (sentinel never" \
             "found on the pane; usually the TUI viewport scrolled it away — retry with" \
             "QUM1111_ROWS=120) ==="
        return 1
    else
        echo "=== VERDICT: not reproduced ($EVALUABLE/$ITERS iterations evaluable) ==="
    fi
    # Assertion-count floor (CLAUDE.md): a run that asserted nothing is a
    # harness failure, not a clean non-repro.
    if [ "$((PASS_COUNT + FAIL_COUNT))" -lt 2 ]; then
        echo "  FAIL: assertion-count floor — only $((PASS_COUNT + FAIL_COUNT)) assertions ran" >&2
        return 1
    fi
    e2e_print_results
}

main "$@"
