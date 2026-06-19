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
# Injection is a direct maildir-envelope write (mirrors notify-tui Test B /
# internal/messages Send): two messages from two senders land in weave's
# new/, the 2s maildir watcher peeks them while weave is idle, drains them as
# stacked <system-notification> envelopes, weave's claude consumes (isReplay),
# and the pending-zone settle relocates them into the committed transcript.

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
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3

    echo ""
    echo "=== L1: inject 2 stacked system-notification frames (senders aldous/bilbo) ==="
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
    echo "=== L7: restart the session and assert replay parity (single emission) ==="
    # Kill the TUI/claude and relaunch on the same SPRAWL_ROOT. The new session
    # resumes weave's claude (--resume) and replays the transcript via
    # LoadTranscript → replay.go, which peels the same <system-notification>
    # frames through the shared classifier. A double-render would resurface the
    # raw tag or a duplicate citation.
    _stmux kill-session -t "$SESSION" 2>/dev/null || true
    sleep 2

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
