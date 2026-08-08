#!/usr/bin/env bash
# scripts/e2e-tests/qum1000-refused-slash.sh — QUM-1000 live gate.
#
# Proves against the REAL claude, through the interactive weave TUI, that a slash
# command the CLI REFUSES leaves NO permanent ghost row in the pending zone.
#
# The refusal class this exercises: `/status` is a real CLI builtin that the
# sdk-cli entrypoint declines ("/status isn't available in this environment."),
# and it is NOT in sprawl's slash-command registry, so the TUI passes it through
# to the CLI as an ordinary user message. The CLI answers with assistant text and
# NO isReplay echo — and that echo is the only consumption ack (QUM-817), so
# before the fix the pending-zone entry (QUM-833) never settled and a dim
# `› /status` row sat in the prompt area indefinitely.
#
# DO NOT substitute `/perf` here: QUM-934 will register it in sprawl, which makes
# the reproduction vanish without fixing anything. DO NOT substitute `/help` (it
# IS registered locally) or `/model` / `/context` / `/cost` (the CLI ACCEPTS those,
# they echo, and they settled correctly before the fix — an assertion written
# against them is green pre-fix and exercises nothing). `/model` appears below only
# as the must-still-work control.
#
# How "no ghost row" is asserted, per QUM-832's rendering contract: lipgloss
# renders a committed (settled) user bubble with a BOLD attribute (SGR 1) and a
# pending-zone bubble with a FAINT attribute (SGR 2). tmux's escape-preserving
# capture (`capture-pane -e`) normalizes the span as
# `\x1b[<attr>m\x1b[38;5;<fg>m<text>`, so we key on the attribute escape on the
# USER-BUBBLE line carrying the command (identified by the `›` prompt-block prefix —
# see user_row, and note the CLI's refusal text contains the token `/status` too).
# Faint after the turn has ended == the ghost.
#
# Falsifiability (QUM-953): this row was run against the parent commit — i.e. with
# the settleNeverAcked sweep absent — and FAILED at "the /status row is no longer
# faint", with the capture showing the persistent `\x1b[2m` span. It is not an
# assertion nobody has watched fail. The wire assertion below also proves the
# precondition rather than assuming it: the CLI genuinely emitted no isReplay echo
# for that submission's uuid, so the settle came from the sweep and not from a
# late ack.
#
# The wire log is parsed with jq, never grepped: `raw` embeds tool_result payloads
# containing arbitrary text, so a grep would match strings this test itself wrote
# into the log.

# Assertion-count floor, now ENFORCED BY THE SHARED AGGREGATOR: QUM-1029 moved
# the check into e2e_print_results (scripts/lib/e2e-common.sh), which reads this
# declaration and fails the row when the observed count falls below it. Until
# then this row carried the check itself and was the only e2e row with a floor
# at all; that local copy is gone, the declaration stays.
#
# 13 is the count of assertions a COMPLETE, PASSING run of this row makes.
#
# TWO HONEST LIMITS, stated because the skill's own rule is that a guard with no
# reachable failure path is not a check, and a comment that implies otherwise is
# the defect:
#
#  1. This floor CANNOT change today's verdict. Every path that reaches it with
#     fewer than 13 assertions has already called fail() (the minimum is 12, via
#     AC1b's else), so the run already exits nonzero on its own. The floor is
#     future-proofing against the QUM-997 structural-early-return class — a new
#     `return` or a helper that dies before its assertions — NOT the closing of a
#     live hole. The red-first bump-to-14 proves the mechanism fires; it does not
#     prove the configured 13 can fire on a genuine short run today.
#  2. Counting PASS+FAIL immunizes every EQUAL-ARITY pass→fail substitution, so a
#     genuine failure normally reports itself alone. The one exception is AC1b's
#     else, which is arity-REDUCING (1 assertion where the if-branch yields 2):
#     that path legitimately produces both its own failure and a floor failure.
MIN_ASSERTIONS=13

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

# QUM-957: capture_pane_ansi comes from scripts/lib/capture-pane.sh (sourced by
# e2e-common.sh). The copy that used to live here — `capture-pane -e -p
# 2>/dev/null || true` — returned empty-stdout-with-exit-0 for a dead session, so
# the attribute checks below reported "the attribute is absent" when the truth was
# "there was no pane to read". An EMPTY pane on a LIVE session is still a success
# and still silent; only a tmux failure is not.

# A user bubble is the ONLY line that carries both the `›` prompt-block prefix and
# the submitted text, so every assertion below keys on that pair. Anchoring matters:
# the bare token `/status` also appears in the CLI's refusal text ("/status isn't
# available in this environment."), so an unanchored attribute check could pass on
# the assistant line while the prompt row was still a ghost — and could fail for a
# rendering reason unrelated to this fix. The `›` and the text are NOT adjacent in
# the escape-preserving capture (lipgloss emits attribute + colour escapes between
# them), so this is a two-grep conjunction rather than one anchored pattern.
#
# user_row SESSION SENTINEL — the captured bubble line(s) carrying SENTINEL.
user_row() {
    capture_pane_ansi "$1" | grep -aF "$2" | grep -aF "›"
}

# user_row_present SESSION SENTINEL
user_row_present() {
    [ -n "$(user_row "$1" "$2")" ]
}

# row_has_attr SESSION SENTINEL ATTR — true if the bubble line carrying SENTINEL
# also carries the SGR attribute escape \x1b[ATTRm (1=bold/settled, 2=faint/pending).
#
# Why the attribute is the RIGHT thing to assert, not merely a proxy that could
# drift: theme.go's two styles are `Foreground(pal.UserPrompt).Bold(true)` and
# `Foreground(pal.UserPrompt).Faint(true)` — SAME foreground, differing in exactly
# one bit. The SGR attribute is therefore the sole physical carrier of the
# pending/committed distinction, so there is no second visual channel in which a
# rendering could look wrong to a user while this assertion reads right. (Contrast
# QUM-925 slice B, where a ZWSP + gutter rendered pixel-identical to committed
# while all the row's assertions stayed green — that needs an independent visual
# dimension to exist, and here one does not.) The settle assertions also require
# BOTH legs: faint absent AND bold present.
#
# Two known couplings to renderUserPromptBlock, so the next reader does not have to
# rediscover them. Both fail LOUD (a missed match times out the wait and fails the
# row) rather than green, which is the safe direction — but both would fail for a
# rendering reason unrelated to this fix:
#
#  - this greps the whole bubble LINE, not the span bound to SENTINEL. It is sound
#    only because renderUserPromptBlock applies ONE style to the entire block, so
#    the `›` gutter and the text always carry the same attribute (verified in both
#    captures: `\x1b[2m` on both spans pre-fix, `\x1b[1m` on both post-fix). If the
#    gutter is ever styled independently of the text, re-anchor to the text span.
#  - user_row requires SENTINEL and `›` on the SAME line, but the gutter is emitted
#    only for i == 0; continuation lines get "  ". A sentinel that WRAPPED would
#    make user_row empty. It cannot wrap at the 200-col pane this row launches with,
#    which is the only reason the AC3 prose sentinel is safe.
row_has_attr() {
    user_row "$1" "$2" | grep -qaP "\x1b\[${3}m"
}

# wait_row_attr SESSION SENTINEL ATTR TIMEOUT
wait_row_attr() {
    local end=$((SECONDS + $4))
    while [ "$SECONDS" -lt "$end" ]; do
        if row_has_attr "$1" "$2" "$3"; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

# wait_row_not_attr SESSION SENTINEL ATTR TIMEOUT — poll until the bubble line no
# longer carries the attribute. The presence guard is load-bearing: without it the
# assertion would pass once the row scrolled out of the pane, having observed
# nothing.
wait_row_not_attr() {
    local end=$((SECONDS + $4))
    while [ "$SECONDS" -lt "$end" ]; do
        if user_row_present "$1" "$2" && ! row_has_attr "$1" "$2" "$3"; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

# wire_log — the newest weave wire log in the sandbox, or empty.
wire_log() {
    ls -t "$SPRAWL_ROOT"/.sprawl/logs/sessions/weave/*.ndjson 2>/dev/null | head -1
}

# outbound_uuid_for TEXT — the uuid of the stdin user frame whose content equals
# TEXT exactly. jq-parsed; content may be a string or a block array (only the
# string form is matched — the commands this row submits are plain text).
#
# Direction vocabulary, verified against a real log rather than assumed:
# dir=="in" is the frame written INTO the CLI's stdin (sprawl → CLI, e.g. the
# initialize control_request and every user submission), dir=="out" is the frame
# read out of the CLI's stdout (CLI → sprawl, e.g. the isReplay echo).
outbound_uuid_for() {
    local log text="$1"
    log="$(wire_log)"
    [ -n "$log" ] || return 1
    jq -rs --arg t "$text" '
        [ .[]
          | select(.dir == "in")
          | (.raw | fromjson? // empty)
          | select(.type == "user")
          | select((.message.content | if type == "string" then . else "" end) == $t)
          | .uuid // empty
        ] | last // empty
    ' "$log" 2>/dev/null
}

# replay_echo_count UUID — inbound isReplay user frames carrying UUID. Prints the
# count on success and returns nonzero WITHOUT printing on any failure: a jq error
# must not be laundered into "0", which is exactly the value AC1b wants to see.
replay_echo_count() {
    local log uuid="$1"
    log="$(wire_log)"
    [ -n "$log" ] || return 1
    jq -rs --arg u "$uuid" '
        [ .[]
          | select(.dir == "out")
          | (.raw | fromjson? // empty)
          | select(.type == "user")
          | select(.isReplay == true)
          | select(.uuid == $u)
        ] | length
    ' "$log"
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-qum1000-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum1000"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SUFFIX SESSION
    SUFFIX="$(head -c4 /dev/urandom | xxd -p)"
    SESSION="sprawl-qum1000-e2e-${SUFFIX}"

    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo ""

    echo "=== Launching sprawl enter in tmux ==="
    if ! e2e_launch_tui "$SESSION" 200 50; then
        return 1
    fi
    pass "TUI rendered (weave root pill visible)"

    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3
    e2e_attach_phantom_client "$SESSION"

    echo ""
    echo "=== AC1: a REFUSED builtin (/status) leaves no ghost pending row ==="
    e2e_send_user_prompt "$SESSION" "/status"

    # The row must APPEAR first — otherwise "no faint row" would pass trivially
    # for a submission that never rendered at all (the vacuity trap in the ACs).
    # Anchored on the user-bubble shape, so the CLI's refusal TEXT (which also
    # contains "/status") cannot satisfy it.
    if wait_row_attr "$SESSION" "/status" "[12]" 30; then
        pass "the /status submission rendered as a row (assertion is not vacuous)"
    else
        fail "/status never rendered within 30s"
        capture_pane "$SESSION" | tail -30 >&2
        e2e_print_results
        return 1
    fi

    # The CLI's refusal, arriving as ordinary assistant text, is the precondition
    # this row exists to exercise. If the CLI ever starts ACCEPTING /status this
    # assertion fails loudly rather than the row silently testing nothing.
    if wait_for_pattern "$SESSION" "available in this environment" 90; then
        pass "the CLI REFUSED /status (refusal text rendered) — the strand precondition holds"
    else
        fail "no refusal text within 90s; the CLI may have accepted /status, in which case this row no longer exercises the bug"
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    # THE GATE: once the turn has ended, the row must have settled — i.e. it is no
    # longer FAINT (SGR 2). Pre-fix it stays faint forever.
    if wait_row_not_attr "$SESSION" "/status" 2 90; then
        pass "the /status row is no longer faint: the pending-zone entry settled (no ghost)"
    else
        fail "the /status row is STILL FAINT after the turn ended — the ghost pending-zone entry persists"
        user_row "$SESSION" "/status" | cat -v >&2 || true
        capture_pane "$SESSION" | tail -30 >&2
        e2e_print_results
        return 1
    fi

    if wait_row_attr "$SESSION" "/status" 1 30; then
        pass "the /status row rendered BOLD (committed transcript styling)"
    else
        fail "the /status row never rendered bold/committed"
        user_row "$SESSION" "/status" | cat -v >&2 || true
        e2e_print_results
        return 1
    fi

    echo ""
    echo "=== AC1b (wire): the CLI emitted NO isReplay echo for that submission ==="
    local status_uuid echoes
    # `|| true`: the driver runs rows under `set -euo pipefail`, so a helper that
    # returns nonzero (no wire log yet) would kill the row before its own
    # diagnostic fail() and before e2e_print_results.
    status_uuid="$(outbound_uuid_for "/status" || true)"
    if [ -n "$status_uuid" ]; then
        pass "found the outbound /status stdin frame on the wire (uuid=$status_uuid)"
        echoes="$(replay_echo_count "$status_uuid" || echo JQFAIL)"
        if [ "$echoes" = "JQFAIL" ]; then
            fail "replay_echo_count failed to parse the wire log — NOT counted as zero echoes"
        elif [ "$echoes" = "0" ]; then
            pass "zero isReplay echoes for that uuid — the settle came from the sweep, not a late ack"
        else
            fail "expected 0 isReplay echoes for the refused command, got $echoes"
        fi
    else
        fail "could not locate the outbound /status frame in the wire log (jq parse); wire assertions skipped"
    fi

    echo ""
    echo "=== AC2: no regression for ACCEPTED input (/model settles exactly once) ==="
    e2e_send_user_prompt "$SESSION" "/model"
    if wait_row_attr "$SESSION" "/model" "[12]" 30; then
        pass "/model rendered"
    else
        fail "/model never rendered within 30s"
        capture_pane "$SESSION" | tail -20 >&2
    fi
    if wait_row_not_attr "$SESSION" "/model" 2 90; then
        pass "/model settled (not faint) — the accepted-command path is unbroken"
    else
        fail "/model left a faint pending row"
        user_row "$SESSION" "/model" | cat -v >&2 || true
    fi
    local model_uuid model_echoes
    model_uuid="$(outbound_uuid_for "/model" || true)"
    if [ -n "$model_uuid" ]; then
        model_echoes="$(replay_echo_count "$model_uuid" || echo JQFAIL)"
        if [ "$model_echoes" = "1" ]; then
            pass "/model was acked by exactly ONE isReplay echo (settles once, via the real ack)"
        else
            fail "/model isReplay echo count = $model_echoes, want exactly 1"
        fi
    else
        fail "could not locate the outbound /model frame in the wire log"
    fi

    echo ""
    echo "=== AC3: ordinary prose still renders and settles (QUM-832) ==="
    local PROSE="QUM1000PROSE${SUFFIX}"
    e2e_send_user_prompt "$SESSION" "Reply with exactly: $PROSE"
    if wait_row_attr "$SESSION" "$PROSE" "[12]" 60; then
        pass "prose prompt rendered"
    else
        fail "prose prompt never rendered within 60s"
        capture_pane "$SESSION" | tail -20 >&2
    fi
    if wait_row_not_attr "$SESSION" "$PROSE" 2 120; then
        pass "prose prompt settled (not faint)"
    else
        fail "prose prompt left a faint pending row"
        user_row "$SESSION" "$PROSE" | cat -v >&2 || true
    fi

    echo ""
    echo "=== AC4: the settled /status row did not resurrect or duplicate ==="
    # The presence guard matters: after two more turns the row may have scrolled
    # out of the pane, and "no faint row" would then pass having observed nothing.
    if ! user_row_present "$SESSION" "/status"; then
        fail "the /status row is no longer in the pane — cannot assert it stayed settled (scrolled off)"
    elif row_has_attr "$SESSION" "/status" 2; then
        fail "a faint /status row reappeared after two further turns"
        user_row "$SESSION" "/status" | cat -v >&2 || true
    else
        pass "no faint /status row after two further turns"
    fi

    echo ""
    echo "=== pane capture (plain) ==="
    capture_pane "$SESSION" | tail -40

    e2e_print_results
}
