#!/usr/bin/env bash
# scripts/e2e-tests/notify-tui.sh — QUM-312/QUM-559/QUM-565/QUM-471 regression
# guards. Migrated from scripts/test-notify-tui-e2e.sh (which remains in place
# until soak completes; do not edit the original — see QUM-616 Wave 2A).
#
# Test A: a state.json-only write. Asserts the TUI badge does NOT rise, no
#         inbox banner surfaces, and no drain notification appears.
#         QUM-1186 NOTE — this test's subject was "a self-report write";
#         that tool and its state fields are deleted, so the fixture now just
#         flips `status`. The surviving claim is the general one the QUM-559
#         contract was a special case of: a state.json mutation is NOT a
#         notification trigger, only the maildir is. Recorded as a DELIBERATE
#         REDUCTION: the row no longer covers the self-report write shape,
#         because that shape no longer exists.
# Test B: a direct maildir envelope write (the shape send_message produces).
#         Asserts
#         the TUI picks up the maildir rise on its 2s tick and renders both
#         (a) the 'inbox: N new message[s]' banner (QUM-473 §3) and
#         (b) the '(1)' unread badge on the weave row.

# QUM-1029: the number of assertions a COMPLETE, PASSING run of this row
# makes. QUM-1186 lowered this from 14, deliberately: three green-path
# assertions were DELETED, not migrated. Test A's unconditional "wrote
# state.json" pass, and its jq-vs-grep read-back pair (which counted as one),
# together formed a CIRCLE — the script wrote a file and then asserted the file
# contained what it had just written. Test C's setup announcement was the third.
#
# Two announcements of a similar shape SURVIVE inside this floor (`:272` the
# maildir envelope write, `:394` the activity.ndjson write) and the distinction
# is deliberate rather than an oversight: each is immediately followed by an
# assertion about how the TUI REACTED to that write, so a failed write fails the
# next gate. The deleted ones were followed only by a read-back of themselves.
# Circular, not merely unconditional, is the property that made them worthless.
#
# A floor left at 14 would have been one no honest run could meet; a floor
# lowered without this note would be indistinguishable from quietly dropping
# coverage.
MIN_ASSERTIONS=11

test_metadata() {
    echo "needs_claude=1 needs_tmux=1"
}

# Count occurrences of an inbox-banner pattern in the current pane capture.
# QUM-473 §3 unified the banner format across both emit sites:
#   - "inbox: N new message[s]"             (from AgentTreeMsg rise-detector)
#   - "inbox: N new message[s] from <sender>" (from InboxArrivalMsg notifier)
# QUM-465: a single send_async to weave must produce exactly one of these.
count_inbox_banners() {
    local session="$1"
    # QUM-957: the capture's status is CHECKED, and a fault yields -1 rather than
    # 0. This used to be `capture_pane | grep -cE ... || true`: against a dead
    # session grep read an empty stream, `grep -c` printed 0, and `|| true` pinned
    # rc 0 — so "expect exactly 0 banners" (Test A is ALL of that shape) passed
    # with no pane to look at. -1 can satisfy no expectation, and capture_pane has
    # already recorded the fault, so the row fails either way.
    local pane
    if ! pane=$(capture_pane "$session"); then
        echo "-1"
        return 1
    fi
    # WARNING to the next author: the -1 is safe ONLY because every expectation
    # in this row is `-eq 0`, `-eq 1` or an upper bound, and -1 satisfies none of
    # them. That safety lives at the CALL SITES, not here. A future `-ge 0`,
    # `-le N` or `-lt N` comparison would silently ACCEPT -1 and reinstate the
    # vacuous pass this exists to stop — `[ -1 -gt 1 ]` is already false, so an
    # upper-bound check on a dead pane records nothing. The capture-fault ledger
    # would still fail the row, but that is defence in depth, not the guarantee.
    printf '%s\n' "$pane" | grep -cE "inbox: [0-9]+ new message" || true
}

# QUM-555/QUM-556/QUM-557/QUM-562: count message-class drain rows surfaced in
# weave's viewport, anchored on `mcp__sprawl__messages_read(id=<id>)` which is
# present only on async / interrupt message lines. (QUM-1186 deleted the
# hidden ephemeral status-ping envelope class that used to be the other kind of
# line here and did NOT cite the read tool; send_message envelopes are now the
# only class, and all of them cite it.)
count_drain_notifications() {
    local session="$1"
    local sender="$2"
    # QUM-957: the capture's status is CHECKED, and a fault yields -1 rather than
    # 0. This used to be `capture_pane | grep -cE ... || true`: against a dead
    # session grep read an empty stream, `grep -c` printed 0, and `|| true` pinned
    # rc 0 — so "expect exactly 0 banners" (Test A is ALL of that shape) passed
    # with no pane to look at. -1 can satisfy no expectation, and capture_pane has
    # already recorded the fault, so the row fails either way.
    local pane
    if ! pane=$(capture_pane "$session"); then
        echo "-1"
        return 1
    fi
    # WARNING to the next author: the -1 is safe ONLY because every expectation
    # in this row is `-eq 0`, `-eq 1` or an upper bound, and -1 satisfies none of
    # them. That safety lives at the CALL SITES, not here. A future `-ge 0`,
    # `-le N` or `-lt N` comparison would silently ACCEPT -1 and reinstate the
    # vacuous pass this exists to stop — `[ -1 -gt 1 ]` is already false, so an
    # upper-bound check on a dead pane records nothing. The capture-fault ledger
    # would still fail the row, but that is defence in depth, not the guarantee.
    printf '%s\n' "$pane" | grep -cE "(✉|⚡) (\\[interrupt\\] )?From $sender — mcp__sprawl__messages_read\\(id=[^)]+\\)" || true
}

# QUM-559: poll for `timeout` seconds and fail (return 1) if a weave
# unread-badge ever appears. Returns 0 iff no `weave[^│]*\([1-9]` badge ever
# shows during the sample window.
wait_for_no_badge_rise() {
    local session="$1" timeout="$2"
    local end=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$end" ]; do
        if capture_pane "$session" | grep -qE "weave[^│]*\([1-9]"; then
            return 1
        fi
        sleep 0.2
    done
    return 0
}

test_run() {
    e2e_recover_oauth_token
    e2e_setup_tmux_socket "sprawl-notify-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-tui-notify-e2e"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    local SESSION="sprawl-notify-tui-$(head -c4 /dev/urandom | xxd -p)"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo ""

    # QUM-471: unified runtime is the default; if the handle re-enqueues into
    # the runtime queue, EventTurnStarted is skipped by TUIAdapter and the
    # prompt body never reaches the viewport. count_inbox_banners (QUM-465)
    # must continue to show exactly 1 banner per delivery.
    echo "=== Launching sprawl enter in tmux ==="
    if ! e2e_launch_tui "$SESSION" 200 50; then
        return 1
    fi
    pass "TUI rendered (weave root pill visible in header tree)"

    # Advance past any first-run trust prompt (QUM-310 gotcha).
    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi

    # Give the first AgentTreeMsg tick a moment to land so rootUnread starts
    # at 0 before we trigger the first message.
    sleep 3

    # --- Register a fake child agent in state (CHILD_NAME=sandbox-child, tower
    #     convention to avoid pretend-child-identity leaks into outer sessions
    #     — see QUM-311 / /e2e-testing-sandboxing).
    local CHILD_NAME="sandbox-child"
    local CHILD_STATE_DIR="$SPRAWL_ROOT/.sprawl/agents"
    local CHILD_STATE_FILE="$CHILD_STATE_DIR/${CHILD_NAME}.json"
    mkdir -p "$CHILD_STATE_DIR"
    cat > "$CHILD_STATE_FILE" <<JSON
{
  "name": "${CHILD_NAME}",
  "type": "engineer",
  "family": "engineering",
  "parent": "weave",
  "prompt": "tui notify e2e test",
  "branch": "tui-notify-e2e",
  "worktree": "${SPRAWL_ROOT}",
  "status": "active",
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "tree_path": "weave├${CHILD_NAME}"
}
JSON

    # --- Test A: a state.json-only write ---
    #
    # QUM-559, generalised by QUM-1186: a state-only write has no maildir
    # side effect at all. The TUI's AgentTreeMsg
    # poll reads state.json for display only — it does NOT use state-file
    # changes as a notification trigger. So this state-only write must NOT
    # raise the badge, must NOT surface an `inbox: N new message` banner, and
    # must NOT cause a drain notification citing
    # `mcp__sprawl__messages_read` to appear.
    echo ""
    echo "=== Test A: state.json-only write must not trigger any notification ==="
    local BANNERS_BEFORE_A DRAINS_BEFORE_A
    BANNERS_BEFORE_A=$(count_inbox_banners "$SESSION")
    DRAINS_BEFORE_A=$(count_drain_notifications "$SESSION" "$CHILD_NAME")

    cat > "$CHILD_STATE_FILE" <<JSON
{
  "name": "${CHILD_NAME}",
  "type": "engineer",
  "family": "engineering",
  "parent": "weave",
  "prompt": "tui notify e2e test",
  "branch": "tui-notify-e2e",
  "worktree": "${SPRAWL_ROOT}",
  "status": "done",
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "tree_path": "weave├${CHILD_NAME}"
}
JSON
    # QUM-1186: two assertions used to sit here and both are DELETED, not
    # migrated, because neither was ever a check of sprawl. One was an
    # unconditional `pass` announcing that the `cat` above had run; the other
    # read the file back with jq and asserted it contained what this script had
    # just written to it. They tested `cat` and `jq`. MIN_ASSERTIONS drops with
    # them — a floor left at 14 would have been satisfied by 12 real assertions
    # plus nothing.

    # QUM-559: badge must NOT rise — state-only writes don't touch the maildir.
    if wait_for_no_badge_rise "$SESSION" 5; then
        pass "QUM-559: weave row stayed at no unread badge after a state.json-only write"
    else
        fail "QUM-559: weave row showed an unread badge after a state.json-only write (maildir leak)"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -30 >&2
    fi

    # QUM-559: banner delta must be 0.
    sleep 5
    local BANNERS_AFTER_A DELTA_A
    BANNERS_AFTER_A=$(count_inbox_banners "$SESSION")
    DELTA_A=$((BANNERS_AFTER_A - BANNERS_BEFORE_A))
    if [ "$DELTA_A" -eq 0 ]; then
        pass "QUM-559: zero banner-count delta after a state.json-only write"
    else
        fail "QUM-559: banner-count delta = $DELTA_A (before=$BANNERS_BEFORE_A, after=$BANNERS_AFTER_A); expected 0"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
    fi

    # QUM-559: no maildir-style drain notification from $CHILD_NAME must appear.
    local DRAINS_AFTER_A
    DRAINS_AFTER_A=$(count_drain_notifications "$SESSION" "$CHILD_NAME")
    if [ "$DRAINS_AFTER_A" -eq "$DRAINS_BEFORE_A" ]; then
        pass "QUM-559: no maildir-drain notification from '$CHILD_NAME' (delta=0)"
    else
        fail "QUM-559: maildir-drain notification from '$CHILD_NAME' appeared after a state.json-only write (delta=$((DRAINS_AFTER_A - DRAINS_BEFORE_A)))"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
    fi

    # --- Test B: a direct maildir envelope write (what send_message produces) ---
    #
    # QUM-565: schema mirrors internal/messages/messages.go Send(). Atomic
    # write: tmp/ then rename into new/. Also drop a sent/-copy under the
    # sender's mailbox and pre-create cur/+archive/ so downstream
    # MarkRead/Archive don't ENOENT during this run.
    echo ""
    echo "=== Test B: simulated send_message to weave → badge rises to (1) ==="
    local BANNERS_BEFORE_B
    BANNERS_BEFORE_B=$(count_inbox_banners "$SESSION")

    local WEAVE_MBOX="$SPRAWL_ROOT/.sprawl/messages/weave"
    local SENDER_MBOX="$SPRAWL_ROOT/.sprawl/messages/$CHILD_NAME"
    mkdir -p "$WEAVE_MBOX/tmp" "$WEAVE_MBOX/new" "$WEAVE_MBOX/cur" "$WEAVE_MBOX/archive"
    mkdir -p "$SENDER_MBOX/sent"

    local NS_NOW HEX_SUFFIX SHORT_ID MSG_ID MSG_TS MSG_FILE
    NS_NOW="$(python3 -c 'import time; print(time.time_ns())' 2>/dev/null || date +%s%N)"
    HEX_SUFFIX="$(head -c 4 /dev/urandom | xxd -p)"
    SHORT_ID="$(head -c 3 /dev/urandom | xxd -p | tr 'A-Z' 'a-z' | head -c 3)"
    MSG_ID="${NS_NOW}.${CHILD_NAME}.${HEX_SUFFIX}"
    MSG_TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    MSG_FILE="${MSG_ID}.json"

    cat > "$WEAVE_MBOX/tmp/$MSG_FILE" <<JSON
{
  "id": "${MSG_ID}",
  "shortId": "${SHORT_ID}",
  "from": "${CHILD_NAME}",
  "to": "weave",
  "subject": "tui e2e subject",
  "body": "tui e2e body B",
  "timestamp": "${MSG_TS}"
}
JSON
    mv "$WEAVE_MBOX/tmp/$MSG_FILE" "$WEAVE_MBOX/new/$MSG_FILE"
    cp "$WEAVE_MBOX/new/$MSG_FILE" "$SENDER_MBOX/sent/$MSG_FILE"
    pass "simulated send_message: wrote maildir envelope (id=$SHORT_ID) atomically into weave/new/"

    if wait_for_pattern_fast "$SESSION" "weave[^│]*\\(1\\)" 15; then
        pass "QUM-559: weave row shows '(1)' unread badge after first real maildir delivery"
    else
        fail "weave row did NOT rise to '(1)' after a simulated send_message"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -30 >&2
    fi

    # QUM-565: drain-row inject assertion intentionally NOT made here. The
    # direct maildir-envelope write above exercises the TUI's maildir watcher
    # (banner + badge) but bypasses the internal/messages.Send()/WakeForDelivery
    # arm — so the drain row never lands. The drain pipeline is exercised live
    # by scripts/test-drain-row-inject-e2e.sh and unit-tested in
    # internal/runtime/unified_delivery_send_message_test.go.

    # QUM-465 / QUM-555: assert exactly ONE inbox banner was added by Test B.
    # Sample max-over-window — weave's response can scroll Test A's banner out
    # of the viewport before a single post-settle sample would capture it.
    local BANNERS_MAX_B=$BANNERS_BEFORE_B
    local BANNER_SAMPLE_END=$((SECONDS + 10))
    local BANNERS_NOW
    while [ "$SECONDS" -lt "$BANNER_SAMPLE_END" ]; do
        BANNERS_NOW=$(count_inbox_banners "$SESSION")
        if [ "$BANNERS_NOW" -gt "$BANNERS_MAX_B" ]; then
            BANNERS_MAX_B=$BANNERS_NOW
        fi
        sleep 0.2
    done
    local DELTA_B=$((BANNERS_MAX_B - BANNERS_BEFORE_B))
    if [ "$DELTA_B" -eq 1 ]; then
        pass "QUM-465: exactly 1 banner added by Test B delivery (delta=$DELTA_B)"
    else
        fail "QUM-465: Test B produced $DELTA_B banners (before=$BANNERS_BEFORE_B, max=$BANNERS_MAX_B); expected exactly 1"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
    fi

    # --- Test C: QUM-665 liveness-driven icon flip from idle to working ---
    #
    # This header described the BLOCKED-state flow — seeding a self-reported
    # "blocked" state, asserting a blocked dot, asserting a revert to blocked —
    # for ten lines, immediately above the note recording that QUM-1186 made
    # "blocked" underivable. The mechanism changed and its advertisement did
    # not; read in isolation it describes assertions this row cannot make.
    #
    # What the row actually does. Reuses sandbox-child at the IDLE baseline
    # (⏳), the only baseline `DeriveIconState` can still produce:
    #   1) Initial render shows the idle dot color on the sandbox-child row.
    #   2) After writing a single activity.ndjson entry with TS=now, the row's
    #      dot flips to the working color within ~3s (one 2s tree-rebuild tick
    #      plus margin).
    #   3) After ~3s with no further activity (>2s past last activity, the
    #      RecentActivityWindow), the dot reverts to the idle color.
    #
    # Step 3 is WEAKER than it looks, and the note below says why: baseline and
    # revert-target are now the same glyph, so this cannot distinguish
    # "reverted to what it was" from "fell back to idle". Do not cite it as
    # evidence for the former.
    #
    # We grep for the ANSI escape sequences around the "●" glyph on the
    # sandbox-child row. NewTheme builds these from the dark palette. If the
    # ANSI grep approach proves too fragile in CI, the fallback documented in
    # the spec is to invoke `sprawl status` and assert via JSON — but that path
    # requires Status to expose in_autonomous_turn / last_activity_at (QUM-665
    # surface) so isn't strictly cheaper.
    echo ""
    echo "=== Test C: QUM-665 liveness-driven icon flip (idle → working → idle) ==="

    # QUM-1186 — FORCED REDUCTION, recorded rather than absorbed. The baseline
    # used to be the "blocked" icon, seeded by writing a self-reported blocked
    # state into the fixture. `DeriveIconState` (internal/tui/tree.go) no longer
    # has any branch that can return "blocked": the fallback switch on the
    # self-reported state was deleted with the field, so the ⏸ glyph is now
    # unreachable and an assertion expecting it could never pass.
    #
    # The baseline is therefore idle (⏳), and what is LOST is real: the old row
    # proved the glyph reverted to its PRIOR, non-idle state after
    # RecentActivityWindow expired. Baseline and revert-target are now the same
    # glyph, so the row proves the flip and the revert but can no longer
    # distinguish "reverted to what it was" from "fell back to idle". Nothing
    # can recover that half while "blocked" is underivable.
    cat > "$CHILD_STATE_FILE" <<JSON
{
  "name": "${CHILD_NAME}",
  "type": "engineer",
  "family": "engineering",
  "parent": "weave",
  "prompt": "tui notify e2e test",
  "branch": "tui-notify-e2e",
  "worktree": "${SPRAWL_ROOT}",
  "status": "active",
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "tree_path": "weave├${CHILD_NAME}"
}
JSON
    # No `pass` here: the deleted one asserted that the heredoc above had run.

    # Wait for the tree to pick up the state change (one 2s tick + margin).
    sleep 3

    # Extract the first orbital-state glyph appearing on the sandbox-child row.
    # capture_pane (lib/e2e-common.sh) strips color via tmux `-p`. The orbital
    # pill renderer (internal/tui/tree_orbital.go) emits one of the glyphs
    # below per state: ⚙=working, ⏳=idle, ✓=done, ✗=failure. ⏸ (blocked) stays
    # in the character class deliberately: `DeriveIconState` can no longer
    # return it (QUM-1186, see Test C), so matching it here is how an
    # unexpected resurrection would show up as a glyph mismatch rather than as
    # a row that silently fails to match anything.
    extract_child_glyph() {
        local session="$1" child="$2"
        capture_pane "$session" \
            | grep -aoE "${child} [⚙⏳⏸✓✗]" \
            | head -1 \
            | awk '{print $2}'
    }

    # Baseline: assert the idle glyph (⏳) renders on the sandbox-child row.
    local BASELINE_GLYPH
    BASELINE_GLYPH=$(extract_child_glyph "$SESSION" "$CHILD_NAME")
    if [ "$BASELINE_GLYPH" = "⏳" ]; then
        pass "QUM-665 baseline: sandbox-child renders the idle glyph (⏳)"
    else
        fail "expected ⏳ glyph for the freshly-seeded sandbox-child, got: $BASELINE_GLYPH"
        capture_pane "$SESSION" | tail -10 >&2
    fi

    # Write a single activity entry with TS=now.
    local ACTIVITY_DIR="${SPRAWL_ROOT}/.sprawl/agents/${CHILD_NAME}"
    mkdir -p "$ACTIVITY_DIR"
    local ACTIVITY_NOW
    ACTIVITY_NOW="$(date -u +%Y-%m-%dT%H:%M:%S.%6NZ)"
    printf '{"ts":"%s","kind":"tool_use","tool":"Bash","summary":"test"}\n' \
        "$ACTIVITY_NOW" \
        > "${ACTIVITY_DIR}/activity.ndjson"
    pass "Test C: wrote activity.ndjson entry (ts=${ACTIVITY_NOW})"

    # Poll up to 3s for the glyph to become ⚙ (working).
    local end=$((SECONDS + 3))
    local FLIPPED=0
    while [ "$SECONDS" -lt "$end" ]; do
        g=$(extract_child_glyph "$SESSION" "$CHILD_NAME")
        if [ "$g" = "⚙" ]; then
            FLIPPED=1
            break
        fi
        sleep 0.2
    done
    if [ "$FLIPPED" -eq 1 ]; then
        pass "QUM-665 flip: idle → working glyph (⚙) within 3s of activity write"
    else
        fail "QUM-665 flip: glyph did not become ⚙ within 3s; last seen='$(extract_child_glyph "$SESSION" "$CHILD_NAME")'"
        capture_pane "$SESSION" | tail -10 >&2
    fi

    # Wait past the 30s RecentActivityWindow (QUM-692 widened from 2s).
    sleep 31

    # Poll up to 5s for the glyph to revert to ⏳ (idle).
    end=$((SECONDS + 5))
    local REVERTED=0
    while [ "$SECONDS" -lt "$end" ]; do
        g=$(extract_child_glyph "$SESSION" "$CHILD_NAME")
        if [ "$g" = "⏳" ]; then
            REVERTED=1
            break
        fi
        sleep 0.2
    done
    if [ "$REVERTED" -eq 1 ]; then
        pass "QUM-665 reverted: glyph back to ⏳ (idle) after window expired"
    else
        fail "QUM-665 reverted: glyph did not revert to ⏳; last seen='$(extract_child_glyph "$SESSION" "$CHILD_NAME")'"
        capture_pane "$SESSION" | tail -10 >&2
    fi

    echo ""
    e2e_print_results
}
