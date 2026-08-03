#!/usr/bin/env bash
# scripts/e2e-tests/notif-stacked-restart.sh — QUM-833 live regression guard.
#
# Reproduces the QUM-833 double-render: supervisor-injected
# <system-notification> frames must render with the system glyph/colour
# treatment (peeled, distinct, EXACTLY ONCE) — never as a raw user bubble
# leaking the <system-notification ...> tag, and never doubled. Covers:
#   L1: >=2 stacked notifications mid-session → distinct system-styled lines,
#       no raw tag leak (the live double-render signature).
#   L7: after a session restart (replay path), the same notifications render
#       identically — single emission, no raw bubble (replay/live parity).
#
# Injection is a direct queue-envelope write (mirrors agentloop.Enqueue): two
# entries from two senders land in weave's queue pending/ while weave is idle,
# they are drained as stacked <system-notification> frames, weave's claude
# consumes them (isReplay), and the pending-zone settle relocates them into the
# committed transcript.
#
# QUM-925 changed WHAT drains them, so the old description here is stale: the 2s
# idle-gated TUI poll this comment used to name is DELETED. Because the entries
# are written straight to disk there is no in-process producer to poke, so they
# now arrive via WeaveRuntimeHandle.runInboxRedrainTicker (5s) — which makes this
# row the redrain ticker's only live coverage.
#
# L0/L2 additionally make this row the primary live evidence for QUM-925 AC 1
# ("an idle weave receiving a system frame enters a turn"): L0 asserts the idle
# precondition rather than assuming it from timing, and L2 asserts turn ENTRY
# from the CLI's own result frames rather than from a pane citation.

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

# enqueue_pending <seq> <sender> <shortid> <body>
# Writes a canonical async entry into weave's harness queue pending/ dir — the
# exact surface peekAndDrainCmd → ListPending reads to build the
# <system-notification> flush prompt. Mirrors agentloop.Enqueue's on-disk schema
# (internal/agentloop/queue.go canonicalName + inboxprompt.Entry json tags).
enqueue_pending() {
    local seq="$1" sender="$2" shortid="$3" body="$4"
    local pending entry_id seq10
    pending="$SPRAWL_ROOT/.sprawl/agents/weave/queue/pending"
    mkdir -p "$pending"
    seq10="$(printf '%010d' "$seq")"
    entry_id="$(date +%s%N).${sender}.$(head -c 4 /dev/urandom | xxd -p)"
    cat > "$pending/${seq10}-async-${entry_id}.json" <<JSON
{
  "seq": ${seq},
  "id": "${entry_id}",
  "short_id": "${shortid}",
  "class": "async",
  "from": "${sender}",
  "subject": "qum833 stacked notif",
  "body": "${body}",
  "enqueued_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
JSON
}

# activity_lines <activity.ndjson> — current line count, 0 when absent. Used to
# take an OFFSET before the injection so the assertion afterwards is about turns
# that happened AFTER it, not about a total.
# The `< "$f"` redirect form is used so wc prints a bare number (no filename), and
# its status is taken directly rather than through a pipe — a `wc | tr` pipeline
# reports TR's status, so a wc failure would yield an EMPTY string rather than 0,
# and an empty offset silently degrades L2 from attribution to a total.
activity_lines() {
    local f="$1" n
    [ -f "$f" ] || { echo 0; return; }
    n=$(wc -l < "$f" 2>/dev/null) || n=0
    echo "${n:-0}"
}

# count_results_after <activity.ndjson> <line_offset> — number of COMPLETED weave
# turns recorded beyond <line_offset>. One "kind":"result" entry is appended per
# terminal result frame from the CLI, so this is a durable, monotone, CLI-sourced
# turn counter.
#
# Prints "ERR" (not 0) when the file cannot be read. grep exits 1 for no-match but
# 2 for a read error, and collapsing both to 0 would make an unreadable activity
# file indistinguishable from "weave took no turns" — i.e. it would make L0 pass
# for the wrong reason. Callers must treat non-numeric output as a setup failure.
count_results_after() {
    local f="$1" off="$2" out n rc
    [ -f "$f" ] || { echo 0; return; }
    # The read status must be taken where it is OBSERVABLE. In
    # `n=$(tail file | grep -c ...)`, `$?` is GREP's status, so an unreadable file
    # makes tail fail, grep sees empty input and exits 1 (no-match), and the helper
    # prints a real-looking 0. A watched control (chmod 000) caught that in the
    # first draft. `${PIPESTATUS[0]}` does NOT fix it and was removed: the pipeline
    # ran in a command-substitution SUBSHELL so its PIPESTATUS is invisible here,
    # and the intervening `rc=$?` assignment clobbers PIPESTATUS anyway — it was
    # dead code masquerading as a guard (also caught by a watched control).
    #
    # So tail is run on its own, and only its status decides. The -r test below is
    # kept for the better error message, NOT as the protection: it cannot cover a
    # file that is readable at the test and fails at the read (removed in between,
    # EIO, path became a directory, or running as uid 0 where chmod 000 is still
    # readable).
    [ -r "$f" ] || { echo "count_results_after: $f not readable" >&2; echo "ERR"; return; }
    if ! out=$(tail -n "+$((off + 1))" "$f" 2>/dev/null); then
        echo "count_results_after: read failed on $f" >&2
        echo "ERR"
        return
    fi
    # `|| rc=$?` keeps the NORMAL no-match path (grep exits 1) from aborting under a
    # caller's `set -e`. It survives today only because the driver invokes rows as
    # `run_row ... || rc=$?`, which suppresses errexit; this makes the helper
    # independent of that.
    n=$(printf '%s\n' "$out" | grep -c '"kind":"result"') || rc=$?
    rc=${rc:-0}
    if [ "$rc" -gt 1 ]; then
        echo "count_results_after: grep exited $rc on $f" >&2
        echo "ERR"
        return
    fi
    echo "${n:-0}"
}

# wait_results_after <file> <offset> <target> <timeout>
#   0 = target reached · 1 = timeout · 2 = measurement broken (unreadable file)
# Exit 2 is kept distinct from exit 1 so the caller can say "setup is broken"
# instead of misreporting it as a product regression.
wait_results_after() {
    local f="$1" off="$2" target="$3" timeout="$4" n
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        n="$(count_results_after "$f" "$off")"
        case "$n" in
            ''|*[!0-9]*) return 2 ;;
        esac
        if [ "$n" -ge "$target" ]; then
            return 0
        fi
        sleep 1
    done
    return 1
}

# assert_no_raw_tag <session> — the QUM-833 regression signature is the raw
# "<system-notification" tag leaking into the chat as a user bubble.
assert_no_raw_tag() {
    local session="$1" phase="$2"
    if capture_pane "$session" | grep -qF "<system-notification"; then
        fail "$phase: raw <system-notification ...> tag leaked into the chat (QUM-833 double-render)"
        echo "  pane tail:" >&2
        capture_pane "$session" | tail -40 >&2
        return 1
    fi
    pass "$phase: no raw <system-notification> tag in the chat (rendered system-styled, not a raw bubble)"
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-notif833-e2e"

    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-qum833"
    e2e_install_cleanup_traps
    e2e_init_sandbox_repo

    if [ -f "$REPO_ROOT/.env" ]; then
        cp -p "$REPO_ROOT/.env" "$SPRAWL_ROOT/.env"
    fi
    export SPRAWL_CLAUDE="$REPO_ROOT/scripts/run-claude"

    local SESSION="sprawl-notif833-$(head -c4 /dev/urandom | xxd -p)"
    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo ""

    echo "=== Launching sprawl enter ==="
    if ! e2e_launch_tui "$SESSION" 200 50; then
        return 1
    fi
    pass "TUI rendered"
    # TRUST_KEYS makes the ONE keystroke path in this row explicit. L0's claim is
    # "weave is idle and nothing typed drove it there"; if the trust prompt ever
    # fires, "1"+Enter goes into the TUI and could submit a real user turn, which
    # would be an unattributed turn source for L2. L0 reports the count rather than
    # asserting zero unconditionally, and L0's own completed-turn check catches the
    # case where those keys did start a turn.
    local TRUST_KEYS=0
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        TRUST_KEYS=2
        sleep 1
    fi
    sleep 3

    echo ""
    echo "=== L0: AC-1 precondition — weave is IDLE and has taken NO turn ==="
    # QUM-925 AC 1 is "an idle weave receiving a system frame enters a turn,
    # identically to a user message." This row is the strongest live evidence for
    # it, but only if weave was genuinely IDLE when the entries landed — otherwise
    # it proves delivery-and-render and says nothing about idle-triggers-a-turn.
    # Previously that precondition was merely LIKELY from timing (a sleep 3 and the
    # absence of send-keys). These two checks make it ASSERTED.
    local WEAVE_ACTIVITY="$SPRAWL_ROOT/.sprawl/agents/weave/activity.ndjson"
    # POSITIVE CONTROL on the path. count_results_after returns 0 for a MISSING
    # file, so "zero completed turns" on its own cannot tell "weave took no turn"
    # from "this path is wrong". Requiring the file to EXIST separates those:
    # NewWeaveRuntimeHandle opens it O_APPEND|O_CREATE at construction, so it is
    # present (and empty) from the moment weave's handle is built.
    #
    # It really is EMPTY here, and that is the point — measured, not assumed: an
    # earlier version of this gate demanded >=1 entry on the theory that the CLI's
    # system/init lands in the ring, and it failed the row with "0 entries". The
    # ring records protocol messages off the EventBus, and an idle weave that has
    # never turned has produced none. So the liveness half of the control cannot be
    # taken here; it is L2 that supplies it, by observing this same counter go
    # non-zero later in the row. Path-exists now + same-counter-non-zero later is
    # what makes this zero meaningful rather than vacuous.
    if [ ! -f "$WEAVE_ACTIVITY" ]; then
        fail "L0 SETUP: weave's activity.ndjson does not exist — wrong path or weave's runtime handle was never constructed, so L0's zero and L2's count would both be vacuous. This is a setup/plumbing failure, NOT an AC-1 violation."
        echo "  expected path: $WEAVE_ACTIVITY" >&2
        ls -la "$SPRAWL_ROOT/.sprawl/agents/weave/" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi
    local ACTIVITY_ENTRIES
    ACTIVITY_ENTRIES="$(activity_lines "$WEAVE_ACTIVITY")"
    local RESULTS_BEFORE
    RESULTS_BEFORE="$(count_results_after "$WEAVE_ACTIVITY" 0)"
    case "$RESULTS_BEFORE" in
        ''|*[!0-9]*)
            fail "L0 SETUP: could not count completed turns (got '$RESULTS_BEFORE') — measurement broken, not an AC-1 violation"
            e2e_print_results
            return 1
            ;;
    esac
    if [ "$RESULTS_BEFORE" -ne 0 ]; then
        fail "L0: weave had already completed $RESULTS_BEFORE turn(s) before injection — the idle precondition does not hold, so this row cannot carry AC-1"
        e2e_print_results
        return 1
    fi
    # ...and it is not mid-turn right now either. Sampled repeatedly rather than
    # once: the status bar renders a label only for Thinking/Streaming/Complete, so
    # a single sample can catch a gap and read "idle" for a weave that is in fact
    # in-turn — absence-of-indicator failing toward green. The busy strings are the
    # ones the TUI really emits ("esc: interrupt" per shorthelp.go, NOT the
    # "esc to interrupt" that appears nowhere in the tree).
    #
    # KNOWN LIMITATION: this is a NEGATIVE assertion with no positive control — the
    # row never demonstrates BUSY_RE can match, so if all three strings change it
    # passes silently for a mid-turn weave. It is deliberately the SECONDARY leg for
    # that reason. The load-bearing idle evidence is the completed-turn count, which
    # does have a liveness control (L2 observes the same counter go non-zero).
    # Note "esc: interrupt" is composed at render time (shorthelp.go's
    # b.Key+": "+b.Hint), not a literal in the tree, so it is the most fragile of
    # the three.
    local BUSY_RE="Streaming\\.\\.\\.|Thinking\\.\\.\\.|esc: interrupt"
    local s
    for s in 1 2 3 4; do
        if capture_pane "$SESSION" | grep -qiE "$BUSY_RE"; then
            fail "L0: weave is mid-turn before injection (busy indicator present on sample $s) — idle precondition does not hold"
            capture_pane "$SESSION" | tail -20 >&2
            e2e_print_results
            return 1
        fi
        sleep 1
    done
    pass "L0: weave idle at injection time — activity.ndjson present with $ACTIVITY_ENTRIES entries, 0 completed turns, no busy indicator across 4 samples, keystrokes sent so far: $TRUST_KEYS"

    echo ""
    echo "=== L1: inject 2 stacked system-notification frames (senders aldous/bilbo) ==="
    # Offset taken immediately before the injection so L2 asserts a turn completed
    # AFTER this point, rather than asserting a total. That makes L2 attribution-
    # based: it stays correct even if L0 is later relaxed, and it cannot be
    # satisfied by a turn that predates the notification.
    local ACTIVITY_OFFSET RESULTS_AT_OFFSET
    ACTIVITY_OFFSET="$(activity_lines "$WEAVE_ACTIVITY")"
    # Re-take the completed-turn baseline AT the offset, not just at L0. L0 ran a
    # few seconds earlier, and the row may have sent "1"+Enter at the trust prompt
    # (TRUST_KEYS), which could submit a real user turn — a second, unattributed
    # turn source for L2. Requiring zero completed turns at the offset too means any
    # such turn that finished before this point fails LOUDLY here instead of being
    # silently credited to the notification.
    RESULTS_AT_OFFSET="$(count_results_after "$WEAVE_ACTIVITY" 0)"
    if [ "$RESULTS_AT_OFFSET" != "0" ]; then
        fail "L1 SETUP: $RESULTS_AT_OFFSET completed turn(s) exist at the injection offset (keystrokes sent: $TRUST_KEYS) — a turn from another source would be miscredited to the notification, so L2 cannot attribute. NOT an AC-1 violation."
        e2e_print_results
        return 1
    fi
    enqueue_pending 1 "aldous" "a83" "QUM833 first stacked notification"
    enqueue_pending 2 "bilbo" "b91" "QUM833 second stacked notification"
    pass "enqueued 2 async entries into weave's pending queue (shortIds a83, b91)"

    # The drain renders each unread message as a distinct system-notification
    # citation: "From <sender> — mcp__sprawl__messages_read(id=<shortId>)".
    local NEEDLE1="From aldous — mcp__sprawl__messages_read(id=a83)"
    local NEEDLE2="From bilbo — mcp__sprawl__messages_read(id=b91)"

    if wait_for_substring_fast "$SESSION" "$NEEDLE1" 120; then
        pass "L1: first notification rendered system-styled ($NEEDLE1)"
    else
        fail "L1: first notification citation did not appear within 120s"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    if wait_for_substring_fast "$SESSION" "$NEEDLE2" 60; then
        pass "L1: second notification rendered system-styled, distinct ($NEEDLE2)"
    else
        fail "L1: second (stacked) notification citation did not appear — peel-loop drift?"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    # The bug signature: a raw <system-notification ...> tag rendered as a user
    # bubble. Must be absent.
    assert_no_raw_tag "$SESSION" "L1" || { e2e_print_results; return 1; }

    echo ""
    echo "=== L2: AC-1 — the injected frame drove a turn on an idle weave ==="
    # This is the assertion that distinguishes "a turn started" from "the write
    # happened", and it is why L0 above exists. The two L1 citation assertions read
    # weave's PANE, and since QUM-925 the pending zone renders the citation from
    # sprawl's OWN EventUserMessageSent publish — so a pane citation would still
    # appear if the CLI never took a turn at all. It is a weak predicate for AC 1.
    #
    # "kind":"result" in activity.ndjson is not: it is written from the CLI's
    # terminal result frame, i.e. from the CLI -> sprawl direction, which sprawl
    # cannot forge. Durable and monotone, so unlike a status-bar label it cannot be
    # raced by fast delivery (the defect this row's sibling drain-row-inject hit).
    #
    # ATTRIBUTION, stated as what the code enforces rather than as "zero keystrokes"
    # (it reports TRUST_KEYS, it does not assert it is 0):
    #   - zero completed turns at L0, AND zero again at the injection offset;
    #   - no busy indicator across L0's 4 samples;
    #   - the count is taken strictly BEYOND the offset.
    # Residual window, acknowledged rather than papered over: a turn from another
    # source that began before L0, evaded all 4 busy samples, and completed after
    # the offset would be miscredited. Only the trust-prompt keystrokes could
    # produce one, TRUST_KEYS reports whether any were sent, and both zero-turn
    # gates would have to miss it.
    #
    # SCOPE, stated honestly: a "result" entry is a turn COMPLETION. Completion
    # implies entry, so it is valid AC-1 evidence, but it is strictly stronger than
    # entry — a turn that starts and then faults would fail here. That direction is
    # loud, not silent, so it is the safe way to be wrong.
    wait_results_after "$WEAVE_ACTIVITY" "$ACTIVITY_OFFSET" 1 120
    case $? in
        0)
            pass "L2: a turn completed AFTER the injection (offset ${ACTIVITY_OFFSET}, completed turns before injection: $RESULTS_BEFORE, keystrokes: $TRUST_KEYS) — AC-1: idle weave + system frame => turn"
            ;;
        2)
            fail "L2 SETUP: the turn-counting measurement broke mid-run (activity.ndjson unreadable) — NOT an AC-1 violation"
            e2e_print_results
            return 1
            ;;
        *)
            # A file that vanished mid-run counts as 0, which would otherwise be
            # reported as a product regression. Separate the two before blaming AC-1.
            if [ ! -f "$WEAVE_ACTIVITY" ]; then
                fail "L2 SETUP: activity.ndjson disappeared mid-run — measurement gone, NOT an AC-1 violation"
                e2e_print_results
                return 1
            fi
            fail "L2: weave completed no turn within 120s of the injection — an idle weave did NOT take a turn on a system frame (AC-1 violated)"
            echo "  activity.ndjson tail:" >&2
            tail -20 "$WEAVE_ACTIVITY" >&2 2>/dev/null || echo "    <missing>" >&2
            e2e_print_results
            return 1
            ;;
    esac

    echo ""
    echo "=== L7: restart the session and assert replay parity (single emission) ==="
    # Kill the TUI/claude and relaunch on the same SPRAWL_ROOT. The new session
    # resumes weave's claude (--resume) and replays the transcript via
    # LoadTranscript → replay.go, which peels the same <system-notification>
    # frames through the shared classifier. A double-render would resurface the
    # raw tag or a duplicate citation.
    # QUM-948: no fixed sleep here. kill-session only signals the pane; weave's
    # flock on .sprawl/memory/weave.lock is released when the dying process's fd
    # closes, which under load takes longer than any constant we could pick.
    # e2e_launch_tui below polls for the release (e2e_wait_weave_lock_free) and
    # fails loudly if the lock is genuinely leaked.
    _stmux kill-session -t "$SESSION" 2>/dev/null || true

    local SESSION2="${SESSION}-r"
    if ! e2e_launch_tui "$SESSION2" 200 50; then
        fail "L7: TUI did not relaunch after restart"
        e2e_print_results
        return 1
    fi
    pass "L7: session relaunched (transcript replay path)"
    if capture_pane "$SESSION2" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION2" "1" Enter
        sleep 1
    fi

    if wait_for_substring_fast "$SESSION2" "$NEEDLE1" 60; then
        pass "L7: first notification replayed system-styled"
    else
        fail "L7: first notification did not replay within 60s"
        echo "  pane tail:" >&2
        capture_pane "$SESSION2" | tail -60 >&2
        e2e_print_results
        return 1
    fi
    assert_no_raw_tag "$SESSION2" "L7" || { e2e_print_results; return 1; }

    e2e_print_results
}
