#!/usr/bin/env bash
# scripts/e2e-tests/ask-user-question.sh — QUM-527/QUM-535/QUM-611 e2e
# regression guard, migrated onto the matrix harness (QUM-616 Wave 2C).
#
# Three phases:
#   Phase 0 (QUM-535): weave-as-root caller passes the disk-backed
#                      eligibility gate.
#   Phase 1 (QUM-527): root → spawned manager calls ask_user_question,
#                      user selects option 2 (beta), response round-trips.
#   Phase 2 (QUM-611): Esc-cancel unblocks the parked MCP call (un-wedge).
#
# The original standalone driver, scripts/test-ask-user-question-e2e.sh, was
# deleted once this row proved flake-free (QUM-1183).

# QUM-1029: the number of assertions a COMPLETE, PASSING run of this row
# makes. Thirteen symmetric pass/fail gates, all on the green path. The spawn-detect while-loops hold no assertions — the gate sits after the loop, on a flag.
MIN_ASSERTIONS=13

test_metadata() {
    echo "needs_claude=1 needs_tmux=1 needs_jq=1"
}

test_run() {
    e2e_recover_oauth_token
    unset SPRAWL_AGENT_IDENTITY
    e2e_setup_tmux_socket "sprawl-auq-e2e"
    e2e_build_sprawl
    e2e_make_sandbox_root "sprawl-auq-e2e"
    e2e_init_sandbox_repo
    e2e_install_cleanup_traps

    local SESSION="sprawl-auq-e2e-$(head -c4 /dev/urandom | xxd -p)"
    local STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"

    local PROBE="AUQ-PROBE-$$-$(date +%s)"
    local PROBE_ALPHA="${PROBE}-alpha"
    local PROBE_BETA="${PROBE}-beta"
    local PROBE_GAMMA="${PROBE}-gamma"

    local WEAVE_PROBE="AUQ-WEAVE-PROBE-$$-$(date +%s)"
    local WEAVE_PROBE_A="${WEAVE_PROBE}-aye"
    local WEAVE_PROBE_B="${WEAVE_PROBE}-bee"
    local WEAVE_PROBE_C="${WEAVE_PROBE}-cee"
    local WEAVE_ANSWER_FILE="$SPRAWL_ROOT/auq-weave-answer.txt"

    echo "  SPRAWL_BIN=$SPRAWL_BIN"
    echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
    echo "  SESSION=$SESSION"
    echo "  PROBE=$PROBE"

    # QUM-1186: this row used to answer "did the caller receive the
    # QuestionResponse?" by polling the caller's state.json for the
    # self-reported summary field. That field is deleted along with the tool
    # that wrote it, and the reason it is deleted applies here too: a
    # self-report is the agent's claim about itself, not evidence that the
    # JSON crossed back.
    #
    # Two replacements, because the two callers differ in what is observable:
    #
    #   phase 0, weave-as-caller — weave is the ROOT and has no parent to
    #     message, and `send_message` to yourself is not a supported shape. So
    #     weave WRITES the extracted label to a file in the sandbox. A file the
    #     agent could only create by having the label in hand is a strictly
    #     stronger observation than a self-report, and unlike a pane assertion
    #     it cannot be broken by line wrapping.
    #   phases 1 and 2, manager-as-caller — the manager's parent is weave, so
    #     it sends the label and the row observes it landing in weave's
    #     maildir. This claim is STRONGER than the one it replaces: the label
    #     now has to traverse a real delivery path, not a self-write.
    wait_for_answer_file() {
        local path="$1" needle="$2" timeout="$3"
        local elapsed=0
        while [ "$elapsed" -lt "$timeout" ]; do
            if [ -f "$path" ] && grep -qF -- "$needle" "$path" 2>/dev/null; then
                return 0
            fi
            sleep 1
            elapsed=$((elapsed + 1))
        done
        return 1
    }

    echo ""
    echo "=== Launching sprawl enter ==="
    e2e_launch_tui "$SESSION" 200 50 || {
        e2e_print_results
        return 1
    }
    pass "TUI rendered ('weave (idle)' visible in tree panel)"

    if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
        _stmux send-keys -t "$SESSION" "1" Enter
        sleep 1
    fi
    sleep 3

    echo ""
    echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
    e2e_attach_phantom_client "$SESSION"

    # --- Phase 0: weave-as-caller (QUM-535) ---
    echo ""
    echo "=== Phase 0: driving weave directly to call ask_user_question (QUM-535) ==="

    local WEAVE_PROMPT="Call mcp__sprawl__ask_user_question with questions=[{question:\"Weave-as-caller probe (${WEAVE_PROBE})\",multi_select:false,options:[{label:\"${WEAVE_PROBE_A}\"},{label:\"${WEAVE_PROBE_B}\"},{label:\"${WEAVE_PROBE_C}\"}]}]. Parse the QuestionResponse JSON, extract answers[0].selected[0], then use the Write tool to create the file ${WEAVE_ANSWER_FILE} whose entire contents are that exact extracted label. Do nothing else."

    _stmux send-keys -t "$SESSION" "$WEAVE_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    echo ""
    echo "=== Waiting for weave's ask_user_question modal to appear ==="
    if wait_for_pattern "$SESSION" "is asking" 240; then
        pass "TUI shows 'is asking' indicator for weave-as-caller (eligibility gate accepted root)"
    else
        fail "modal indicator never appeared within 240s — weave-as-caller was rejected by eligibility gate (QUM-535 regression)"
        echo "  weave state on disk:" >&2
        sed 's/^/    /' "$SPRAWL_ROOT/.sprawl/agents/weave.json" >&2 2>/dev/null || echo "    <missing>" >&2
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    sleep 2

    echo ""
    echo "=== Sending keys: Down, Enter (select option 2: $WEAVE_PROBE_B) ==="
    _stmux send-keys -t "$SESSION" Down
    sleep 0.3
    _stmux send-keys -t "$SESSION" Enter

    echo ""
    echo "=== Waiting for weave to write the selected label to the answer file ==="
    if wait_for_answer_file "$WEAVE_ANSWER_FILE" "$WEAVE_PROBE_B" 240; then
        pass "weave wrote '$WEAVE_PROBE_B' to the answer file (round-trip via weave succeeded)"
    else
        fail "weave did not write '$WEAVE_PROBE_B' to $WEAVE_ANSWER_FILE within 240s"
        echo "  answer file contents:" >&2
        cat "$WEAVE_ANSWER_FILE" 2>/dev/null >&2 || echo "    <missing>" >&2
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
    fi

    echo ""
    echo "=== Verifying modal indicator cleared after Resolve (phase 0) ==="
    sleep 3
    if capture_pane "$SESSION" | grep -qE "is asking"; then
        fail "statusbar still shows 'is asking' after Resolve in phase 0"
    else
        pass "statusbar 'is asking' segment cleared after Resolve (phase 0)"
    fi

    # --- Phase 1: weave → manager spawn (QUM-527) ---
    echo ""
    echo "=== Phase 1: driving weave to spawn a manager (QUM-527) ==="

    local SPAWN_PROMPT="Call mcp__sprawl__spawn with family='engineering', type='manager', branch='qum-527-auq-test', and prompt set to exactly: 'You are an automated QUM-527 probe. STEP 1: call mcp__sprawl__ask_user_question with questions=[{question:\"Pick a probe (${PROBE})\",multi_select:false,options:[{label:\"${PROBE_ALPHA}\"},{label:\"${PROBE_BETA}\"},{label:\"${PROBE_GAMMA}\"}]}]. STEP 2: parse the QuestionResponse JSON, extract answers[0].selected[0]. STEP 3: call mcp__sprawl__send_message with to=\"weave\" and body=<that exact extracted label>. STEP 4: Stop. Do nothing else.'"

    _stmux send-keys -t "$SESSION" "$SPAWN_PROMPT"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    echo ""
    echo "=== Waiting for manager spawn to land ==="
    local MANAGER_STATE=""
    local MANAGER_NAME=""
    local ELAPSED=0
    local SPAWN_LANDED=0
    while [ "$ELAPSED" -lt 180 ]; do
        while IFS= read -r candidate; do
            [ -z "$candidate" ] && continue
            if [ -f "$candidate" ] && jq -e '.type == "manager"' "$candidate" >/dev/null 2>&1; then
                MANAGER_STATE="$candidate"
                MANAGER_NAME=$(jq -r '.name' "$MANAGER_STATE")
                SPAWN_LANDED=1
                break
            fi
        done < <(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null)
        [ "$SPAWN_LANDED" -eq 1 ] && break
        sleep 2
        ELAPSED=$((ELAPSED + 2))
    done
    if [ "$SPAWN_LANDED" -eq 1 ]; then
        pass "manager spawned (name=$MANAGER_NAME, state=$MANAGER_STATE)"
    else
        fail "no manager-type state file appeared within 180s — weave's claude did not call spawn"
        echo "  agents dir:" >&2
        ls -la "$SPRAWL_ROOT/.sprawl/agents/" >&2 2>/dev/null || true
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    echo ""
    echo "=== Waiting for ask_user_question modal to appear ==="
    if wait_for_pattern "$SESSION" "is asking" 240; then
        pass "TUI shows 'is asking' indicator (modal/statusbar active)"
    else
        fail "modal indicator never appeared within 240s — manager did not call ask_user_question OR TUI consumer not wired"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
        echo "  manager state:" >&2
        cat "$MANAGER_STATE" 2>/dev/null | sed 's/^/    /' >&2
        e2e_print_results
        return 1
    fi

    sleep 2

    echo ""
    echo "=== Sending keys: Down, Enter (select option 2: $PROBE_BETA) ==="
    _stmux send-keys -t "$SESSION" Down
    sleep 0.3
    _stmux send-keys -t "$SESSION" Enter

    echo ""
    echo "=== Waiting for the manager to deliver the selected label to weave ==="
    if e2e_wait_maildir_substring weave "$PROBE_BETA" 240; then
        pass "manager delivered '$PROBE_BETA' to weave's maildir (round-trip succeeded)"
    else
        fail "'$PROBE_BETA' did not reach weave's maildir within 240s"
        echo "  weave maildir:" >&2
        find "$SPRAWL_ROOT/.sprawl/messages/weave" -type f 2>/dev/null | tail -10 >&2 || echo "    <missing>" >&2
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
    fi

    echo ""
    echo "=== Verifying modal indicator cleared after Resolve (phase 1) ==="
    sleep 3
    if capture_pane "$SESSION" | grep -qE "is asking"; then
        fail "statusbar still shows 'is asking' after Resolve — queue not draining"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -20 >&2
    else
        pass "statusbar 'is asking' segment cleared after Resolve (phase 1)"
    fi

    # --- Phase 2: Esc-cancel wedge regression (QUM-611) ---
    echo ""
    echo "=== Phase 2: Esc-cancel path (QUM-611 wedge regression guard) ==="

    local PROBE2="AUQ-CANCEL-PROBE-$$-$(date +%s)"
    local PROBE2_PRE="${PROBE2}-before-question"
    local PROBE2_POST="${PROBE2}-after-cancel"
    local PROBE2_ALPHA="${PROBE2}-alpha"
    local PROBE2_BETA="${PROBE2}-beta"
    local PROBE2_GAMMA="${PROBE2}-gamma"

    local SPAWN_PROMPT2="Call mcp__sprawl__spawn with family='engineering', type='manager', branch='qum-611-auq-cancel-test', and prompt set to exactly: 'You are an automated QUM-611 probe. STEP 1: call mcp__sprawl__send_message with to=\"weave\" and body=\"${PROBE2_PRE}\". STEP 2: call mcp__sprawl__ask_user_question with questions=[{question:\"Esc-cancel probe (${PROBE2})\",multi_select:false,options:[{label:\"${PROBE2_ALPHA}\"},{label:\"${PROBE2_BETA}\"},{label:\"${PROBE2_GAMMA}\"}]}]. STEP 3: regardless of the response value, call mcp__sprawl__send_message with to=\"weave\" and body=\"${PROBE2_POST}\". STEP 4: Stop. Do nothing else.'"

    _stmux send-keys -t "$SESSION" "$SPAWN_PROMPT2"
    sleep 0.5
    _stmux send-keys -t "$SESSION" Enter

    echo ""
    echo "=== Waiting for phase 2 manager to spawn ==="
    local PRE_EXISTING_MANAGERS
    PRE_EXISTING_MANAGERS="|$(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null | sort | tr '\n' '|')"
    local MANAGER2_STATE=""
    local MANAGER2_NAME=""
    ELAPSED=0
    while [ "$ELAPSED" -lt 180 ]; do
        while IFS= read -r candidate; do
            [ -z "$candidate" ] && continue
            case "$PRE_EXISTING_MANAGERS" in
                *"|${candidate}|"*) continue ;;
            esac
            if [ -f "$candidate" ] && jq -e '.type == "manager"' "$candidate" >/dev/null 2>&1; then
                MANAGER2_STATE="$candidate"
                MANAGER2_NAME=$(jq -r '.name' "$MANAGER2_STATE")
                break 2
            fi
        done < <(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null)
        sleep 2
        ELAPSED=$((ELAPSED + 2))
    done
    if [ -n "$MANAGER2_NAME" ]; then
        pass "phase 2 manager spawned (name=$MANAGER2_NAME)"
    else
        fail "phase 2 manager never spawned within 180s"
        echo "  agents dir:" >&2
        ls -la "$SPRAWL_ROOT/.sprawl/agents/" >&2 2>/dev/null || true
        e2e_print_results
        return 1
    fi

    echo ""
    echo "=== Waiting for phase 2 manager's pre-question baseline sentinel ==="
    if e2e_wait_maildir_substring weave "$PROBE2_PRE" 120; then
        pass "phase 2 manager pre-question baseline observed"
    else
        fail "phase 2 manager's pre-question baseline sentinel never reached weave within 120s"
        e2e_print_results
        return 1
    fi

    echo ""
    echo "=== Waiting for phase 2 modal to appear ==="
    if wait_for_pattern "$SESSION" "is asking" 180; then
        pass "phase 2 modal appeared (manager called ask_user_question)"
    else
        fail "phase 2 modal never appeared within 180s"
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
        e2e_print_results
        return 1
    fi

    sleep 2

    echo ""
    echo "=== Sending single Esc keypress (QUM-611 cancel path) ==="
    _stmux set-option -g escape-time 0 >/dev/null 2>&1 || true
    _stmux send-keys -t "$SESSION" Escape
    sleep 1.5

    echo ""
    echo "=== Asserting modal closes after Esc ==="
    ELAPSED=0
    local MODAL_CLEARED=0
    while [ "$ELAPSED" -lt 10 ]; do
        if ! capture_pane "$SESSION" | grep -qE "is asking"; then
            MODAL_CLEARED=1
            break
        fi
        sleep 1
        ELAPSED=$((ELAPSED + 1))
    done
    if [ "$MODAL_CLEARED" -eq 1 ]; then
        pass "modal closed after Esc"
    else
        fail "modal still showing 'is asking' 10s after Esc — DismissQuestionMsg not firing"
    fi

    echo ""
    echo "=== Asserting manager's MCP call returned and next turn fired ==="
    if e2e_wait_maildir_substring weave "$PROBE2_POST" 30; then
        pass "manager delivered the post-cancel sentinel '$PROBE2_POST' within 30s (un-wedge confirmed)"
    else
        fail "the post-cancel sentinel did NOT reach weave within 30s — wedge persists (QUM-611 regression)"
        echo "  weave maildir:" >&2
        find "$SPRAWL_ROOT/.sprawl/messages/weave" -type f 2>/dev/null | tail -10 >&2 || echo "    <missing>" >&2
        echo "  pane tail:" >&2
        capture_pane "$SESSION" | tail -40 >&2
    fi

    e2e_print_results
}
