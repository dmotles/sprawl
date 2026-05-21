#!/usr/bin/env bash
# test-ask-user-question-e2e.sh — End-to-end gate for the
# `mcp__sprawl__ask_user_question` round-trip (QUM-527).
#
# What this script does:
#   1. Spins up an isolated /tmp sandbox (plain git repo + .sprawl/
#      root-name), mirroring the safety guards in sprawl-test-env.sh.
#   2. Launches `sprawl enter` in a detached tmux session so the
#      bubbletea TUI has a pseudo-terminal to render into.
#   3. Waits for the TUI to render (tree panel shows 'weave (idle)').
#   4. Attaches a phantom tmux client (QUM-327 workaround) so
#      `send-keys` delivers into the bubbletea input buffer.
#   5. Types a user prompt instructing root weave to call
#      `mcp__sprawl__spawn` with type=manager and a structured prompt
#      that tells the spawned manager to call
#      `mcp__sprawl__ask_user_question` then `mcp__sprawl__report_status`.
#      The single-select question's three options carry a unique
#      `AUQ-PROBE-<token>-{alpha,beta,gamma}` sentinel. The manager's
#      name is auto-allocated by the supervisor's name pool.
#   6. Polls .sprawl/agents/ for the first manager-type state file.
#   7. Waits for the modal indicator (`is asking`) to appear in the TUI —
#      proves: spawn happened, manager's claude booted, manager called
#      `ask_user_question`, the supervisor enqueued, the TUI consumer
#      received OnEnqueue, and the statusbar SetPendingQuestions segment
#      rendered.
#   8. Sends `Down` + `Enter` to select the SECOND option ("…-beta"),
#      defeating any "always default-cursor" buggy implementation.
#   9. Polls the manager's .sprawl/agents/<manager>.json for
#      `last_report_message` containing the unique
#      `AUQ-PROBE-<token>-beta` sentinel — proves the `QuestionResponse`
#      JSON crossed back through the MCP tool to the manager (it had to
#      receive the response to call report_status with that exact label).
#  10. Asserts the modal indicator cleared after Resolve (statusbar
#      segment depth drops to 0).
#  11. Phase 2 (QUM-611 Esc-cancel wedge regression guard) spawns a fresh
#      manager that records a pre-question baseline via report_status,
#      then calls ask_user_question, then unconditionally records a
#      post-question report_status. The script asserts the modal appears,
#      sends a SINGLE Esc keypress (NOT a selection), then asserts (a)
#      the modal closes within 10s, (b) the manager's last_report_message
#      advances from the pre-sentinel to the post-sentinel within 30s —
#      proving the blocked MCP call returned and the manager's next turn
#      fired (un-wedged).
#  12. Cleanup: kill the tmux session, remove the sandbox.
#
# Eligibility note: this exercises BOTH allow-paths of the eligibility
# gate — the root-weave allow-path (phase 0, QUM-535) and the manager
# allow-path (slice 2b, QUM-527). Phase 0 drives weave directly to call
# ask_user_question and proves the persisted `type=root` on weave's
# state record survives the disk-backed Status() lookup. Phase 1 (the
# original test) drives weave to spawn a manager child and has the
# manager call the tool. Unit tests in
# `internal/sprawlmcp/server_askquestion_test.go` cover the
# engineer/researcher reject paths.
#
# Gate: if `claude` is missing and SPRAWL_E2E_SKIP_NO_CLAUDE=1, skip.
# Otherwise fail fast — the TUI cannot initialize without claude.
#
# Auth recovery (QUM-411): when this script is invoked from inside a
# Claude Code SDK Bash tool subprocess, the SDK strips
# CLAUDE_CODE_OAUTH_TOKEN from the child env. Walk up ancestors until
# we find a process whose environ still contains the token. HARNESS-ONLY
# shim — production sprawl Go code must NOT replicate this.
#
# Usage: bash scripts/test-ask-user-question-e2e.sh
#
# NOTE: creates a real tmux session and at least two real claude
# subprocesses (weave + manager). Do not run in parallel with other
# TUI-mode e2e scripts.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# --- Recover CLAUDE_CODE_OAUTH_TOKEN from an ancestor env (QUM-411) ---
if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
    _scan_pid=$$
    for _ in 1 2 3 4 5 6 7 8; do
        _parent=$(awk '{print $4}' "/proc/$_scan_pid/stat" 2>/dev/null || true)
        [ -z "$_parent" ] || [ "$_parent" = "0" ] && break
        if [ -r "/proc/$_parent/environ" ]; then
            _recovered=$(tr '\0' '\n' < "/proc/$_parent/environ" \
                | grep '^CLAUDE_CODE_OAUTH_TOKEN=' | cut -d= -f2- || true)
            if [ -n "$_recovered" ]; then
                export CLAUDE_CODE_OAUTH_TOKEN="$_recovered"
                echo "  (recovered CLAUDE_CODE_OAUTH_TOKEN from ancestor pid=$_parent)"
                break
            fi
        fi
        _scan_pid=$_parent
    done
    unset _scan_pid _parent _recovered
fi

# --- Strip agent-identity leakage from the harness ---
# If this script is invoked from inside an in-flight agent worktree, the
# parent process exports SPRAWL_AGENT_IDENTITY=<agent>. That leaks into
# the sandbox `sprawl enter`'s supervisor, which then mis-identifies the
# root weave as the harness agent. Force a clean sandbox identity.
unset SPRAWL_AGENT_IDENTITY

# --- Dedicated tmux socket for sandbox isolation (QUM-325) ---
SPRAWL_TMUX_SOCKET="${SPRAWL_TMUX_SOCKET:-sprawl-auq-e2e-$$}"
export SPRAWL_TMUX_SOCKET
_stmux() { tmux ${SPRAWL_TMUX_SOCKET:+-L "$SPRAWL_TMUX_SOCKET"} "$@"; }

# --- Preflight ---

if ! command -v claude >/dev/null 2>&1; then
    if [ "${SPRAWL_E2E_SKIP_NO_CLAUDE:-}" = "1" ]; then
        echo "SKIP: claude binary not found on PATH (SPRAWL_E2E_SKIP_NO_CLAUDE=1 set)"
        exit 0
    fi
    echo "FATAL: claude binary not found on PATH — ask-user-question e2e requires a real claude" >&2
    echo "       Set SPRAWL_E2E_SKIP_NO_CLAUDE=1 to skip this test." >&2
    exit 1
fi

if ! command -v tmux >/dev/null 2>&1; then
    echo "FATAL: tmux binary not found on PATH" >&2
    exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
    echo "FATAL: jq binary not found on PATH (used to read manager state.json)" >&2
    exit 1
fi

echo "=== Building sprawl ==="
make -C "$REPO_ROOT" build >/dev/null
SPRAWL_BIN="$REPO_ROOT/sprawl"
if [ ! -x "$SPRAWL_BIN" ]; then
    echo "FATAL: sprawl binary not found at $SPRAWL_BIN" >&2
    exit 1
fi

# --- Sandbox under /tmp/ ---

SPRAWL_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/sprawl-auq-e2e-XXXXXX")
SPRAWL_ROOT_REAL="$(cd "$SPRAWL_ROOT" 2>/dev/null && pwd -P || echo "$SPRAWL_ROOT")"
case "$SPRAWL_ROOT_REAL" in
    /tmp/*) ;;
    *)
        echo "FATAL: sandbox SPRAWL_ROOT=$SPRAWL_ROOT_REAL not under /tmp/; aborting" >&2
        exit 1
        ;;
esac
SPRAWL_ROOT="$SPRAWL_ROOT_REAL"

git -C "$SPRAWL_ROOT" init -b main --quiet
git -C "$SPRAWL_ROOT" -c user.name="Test" -c user.email="test@test" \
    commit --allow-empty -m "init" --quiet
mkdir -p "$SPRAWL_ROOT/.sprawl"
echo "weave" > "$SPRAWL_ROOT/.sprawl/root-name"

SESSION="sprawl-auq-e2e-$(head -c4 /dev/urandom | xxd -p)"
STDERR_LOG="$SPRAWL_ROOT/.sprawl/tui-stderr.log"

# Unique probe sentinel so we can't pick up a false positive from
# unrelated text in the pane or other agents' state files.
PROBE="AUQ-PROBE-$$-$(date +%s)"
PROBE_ALPHA="${PROBE}-alpha"
PROBE_BETA="${PROBE}-beta"
PROBE_GAMMA="${PROBE}-gamma"

# Phase 0 sentinels — weave-as-caller (QUM-535). Disjoint from the
# manager probes so a partial pass can't be confused for the other phase.
WEAVE_PROBE="AUQ-WEAVE-PROBE-$$-$(date +%s)"
WEAVE_PROBE_A="${WEAVE_PROBE}-aye"
WEAVE_PROBE_B="${WEAVE_PROBE}-bee"
WEAVE_PROBE_C="${WEAVE_PROBE}-cee"
WEAVE_STATE="$SPRAWL_ROOT/.sprawl/agents/weave.json"

# The manager's name is auto-allocated by agent.AllocateName from the
# manager pool ("tower", "forge", "bastion", …); we discover it at
# runtime by polling .sprawl/agents/ for the first new *.json that
# isn't the synthesized weave state.
MANAGER_STATE=""
MANAGER_NAME=""

echo "  SPRAWL_BIN=$SPRAWL_BIN"
echo "  SPRAWL_ROOT=$SPRAWL_ROOT"
echo "  SESSION=$SESSION"
echo "  PROBE=$PROBE"
echo ""

# --- Test infra ---

PASS_COUNT=0
FAIL_COUNT=0
pass() { PASS_COUNT=$((PASS_COUNT + 1)); echo "  PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); echo "  FAIL: $1" >&2; }

capture_pane() { _stmux capture-pane -t "$1" -p 2>/dev/null || true; }

wait_for_pattern() {
    local session="$1" pattern="$2" timeout="$3"
    local elapsed=0
    while [ "$elapsed" -lt "$timeout" ]; do
        if capture_pane "$session" | grep -qF "$pattern"; then
            return 0
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    return 1
}

# wait_for_pattern_re — same as wait_for_pattern but uses egrep regex.
wait_for_pattern_re() {
    local session="$1" pattern="$2" timeout="$3"
    local elapsed=0
    while [ "$elapsed" -lt "$timeout" ]; do
        if capture_pane "$session" | grep -qE "$pattern"; then
            return 0
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    return 1
}

# wait_for_state_field_path <state_file> <field> <substring> <timeout>
# Polls an arbitrary state file until jq -r ".$field" contains $substring.
wait_for_state_field_path() {
    local state_path="$1" field="$2" needle="$3" timeout="$4"
    local elapsed=0 value
    while [ "$elapsed" -lt "$timeout" ]; do
        if [ -f "$state_path" ]; then
            value=$(jq -r ".${field} // empty" "$state_path" 2>/dev/null || true)
            if [ -n "$value" ] && [[ "$value" == *"$needle"* ]]; then
                return 0
            fi
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    return 1
}

# wait_for_state_field <field> <substring> <timeout>
# Polls $MANAGER_STATE until jq -r ".$field" contains $substring.
wait_for_state_field() {
    wait_for_state_field_path "$MANAGER_STATE" "$1" "$2" "$3"
}

PHANTOM_PID=""
cleanup() {
    local rc=$?
    if [ -n "${PHANTOM_PID:-}" ]; then
        kill "$PHANTOM_PID" 2>/dev/null || true
    fi
    if [ -n "${SPRAWL_TMUX_SOCKET:-}" ]; then
        tmux -L "$SPRAWL_TMUX_SOCKET" kill-server 2>/dev/null || true
        rm -f -- "/tmp/tmux-$(id -u)/$SPRAWL_TMUX_SOCKET" 2>/dev/null || true
    fi
    case "$SPRAWL_ROOT" in
        /tmp/*)
            local attempt
            for attempt in 1 2 3 4 5; do
                if rm -rf -- "$SPRAWL_ROOT" 2>/dev/null; then
                    break
                fi
                sleep 1
            done
            if [ -d "$SPRAWL_ROOT" ]; then
                echo "  WARN: cleanup could not fully remove $SPRAWL_ROOT (stragglers under .sprawl/agents/); watchdog will reap" >&2
            fi
            ;;
    esac
    exit "$rc"
}
trap cleanup EXIT INT TERM HUP

# QUM-458 layer 1: setsid'd watchdog reaps the sandbox if the driver dies via SIGKILL.
# shellcheck source=lib/sandbox-traps.sh
. "$(dirname "$0")/lib/sandbox-traps.sh"
sandbox_install_watchdog "$$" "$SPRAWL_TMUX_SOCKET" "$SPRAWL_ROOT"

# --- Launch the TUI ---

echo "=== Launching sprawl enter ==="
_stmux new-session -d -s "$SESSION" -x 200 -y 50 \
    "SPRAWL_ROOT='$SPRAWL_ROOT' '$SPRAWL_BIN' enter 2>'$STDERR_LOG'"
_stmux set-option -t "$SESSION" window-size manual >/dev/null
_stmux resize-window -t "$SESSION" -x 200 -y 50 >/dev/null

if wait_for_pattern_re "$SESSION" "weave \\(idle\\)" 45; then
    pass "TUI rendered ('weave (idle)' visible in tree panel)"
else
    fail "TUI did not render 'weave (idle)' within 45s"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -30 >&2
    echo "  stderr log tail:" >&2
    [ -f "$STDERR_LOG" ] && tail -20 "$STDERR_LOG" >&2
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

if capture_pane "$SESSION" | grep -q "trust this folder" 2>/dev/null; then
    _stmux send-keys -t "$SESSION" "1" Enter
    sleep 1
fi

sleep 3

# --- Attach phantom tmux client (QUM-327 workaround) ---

echo ""
echo "=== Attaching phantom tmux client (QUM-327 workaround) ==="
script -q -c "tmux ${SPRAWL_TMUX_SOCKET:+-L $SPRAWL_TMUX_SOCKET} attach -t '$SESSION' -d" /dev/null >/dev/null 2>&1 &
PHANTOM_PID=$!
sleep 1

# --- Phase 0: drive weave directly to call ask_user_question (QUM-535) ---
#
# Proves weave-as-caller passes the eligibility gate: weave's persisted
# state file must have type="root" so the disk-backed Supervisor.Status()
# lookup at internal/sprawlmcp/server.go:askUserQuestionEligibility
# accepts it. End-to-end path: weave-tui claude → ask_user_question MCP
# tool → eligibility gate (reads weave.json from disk) → supervisor
# enqueue → TUI modal → user keypress → ResolveQuestion → weave claude
# unblocks → weave calls report_status with the selected label → weave's
# state.json last_report_message becomes the assertion surface.

echo ""
echo "=== Phase 0: driving weave directly to call ask_user_question (QUM-535 regression guard) ==="

WEAVE_PROMPT="Call mcp__sprawl__ask_user_question with questions=[{question:\"Weave-as-caller probe (${WEAVE_PROBE})\",multi_select:false,options:[{label:\"${WEAVE_PROBE_A}\"},{label:\"${WEAVE_PROBE_B}\"},{label:\"${WEAVE_PROBE_C}\"}]}]. Parse the QuestionResponse JSON, extract answers[0].selected[0], then call mcp__sprawl__report_status with state=working and summary set to that exact extracted label. Do nothing else."

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
    cat "$WEAVE_STATE" 2>/dev/null | sed 's/^/    /' >&2 || echo "    <missing>" >&2
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

sleep 2

echo ""
echo "=== Sending keys: Down, Enter (select option 2: $WEAVE_PROBE_B) ==="
_stmux send-keys -t "$SESSION" Down
sleep 0.3
_stmux send-keys -t "$SESSION" Enter

echo ""
echo "=== Waiting for weave to report the selected label ==="
if wait_for_state_field_path "$WEAVE_STATE" "last_report_message" "$WEAVE_PROBE_B" 240; then
    pass "weave state.last_report_message contains '$WEAVE_PROBE_B' (round-trip via weave succeeded)"
else
    fail "weave state.last_report_message did not surface '$WEAVE_PROBE_B' within 240s"
    echo "  current last_report_message:" >&2
    jq -r '.last_report_message // "<unset>"' "$WEAVE_STATE" 2>/dev/null >&2 || true
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

# --- Phase 1: drive root weave to spawn a manager-type child ---
#
# The manager-type caller passes the slice-2b eligibility gate. End-
# to-end path: weave-tui claude → spawn MCP tool → supervisor.Spawn →
# manager subprocess → manager claude → ask_user_question MCP tool →
# supervisor.AskUserQuestion (blocks) → QuestionConsumer.OnEnqueue →
# TUI modal renders → user keypresses from this script →
# QuestionAnsweredMsg → supervisor.ResolveQuestion → manager claude
# unblocks → manager calls report_status with the selected label →
# state.json last_report_message becomes our assertion surface.
#
# The manager's prompt is wrapped in a XML-fence so weave reads it as
# data to forward verbatim into the spawn tool args, not as
# instructions to itself. Weave's instructions are above the fence;
# the manager's instructions are inside the fence.

echo ""
echo "=== Driving weave to spawn the manager ==="

# Model after scripts/test-bridge-lifecycle-e2e.sh's known-good spawn
# prompt pattern: single line, literal STEP N enumeration, embedded
# inner-prompt in single-quoted form inside outer double-quotes. The
# manager's name is auto-allocated by the supervisor (first unused
# entry in the manager pool — likely "tower").
SPAWN_PROMPT="Call mcp__sprawl__spawn with family='engineering', type='manager', branch='qum-527-auq-test', and prompt set to exactly: 'You are an automated QUM-527 probe. STEP 1: call mcp__sprawl__ask_user_question with questions=[{question:\"Pick a probe (${PROBE})\",multi_select:false,options:[{label:\"${PROBE_ALPHA}\"},{label:\"${PROBE_BETA}\"},{label:\"${PROBE_GAMMA}\"}]}]. STEP 2: parse the QuestionResponse JSON, extract answers[0].selected[0]. STEP 3: call mcp__sprawl__report_status with state=complete summary=<that exact extracted label>. STEP 4: Stop. Do nothing else.'"

# QUM-432: send the prompt text, pause, then send Enter so the TUI's
# paste classifier doesn't reclassify Enter as an embedded newline.
_stmux send-keys -t "$SESSION" "$SPAWN_PROMPT"
sleep 0.5
_stmux send-keys -t "$SESSION" Enter

# --- Wait for the spawn to land (any manager-pool state file appears) ---

echo ""
echo "=== Waiting for spawn to land (poll $SPRAWL_ROOT/.sprawl/agents/ for a new *.json) ==="
ELAPSED=0
SPAWN_LANDED=0
while [ "$ELAPSED" -lt 180 ]; do
    # Pick the first agent state file that has type=manager. This is the
    # auto-allocated manager spawned by weave.
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
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

# --- Wait for the modal indicator ---
#
# The statusbar segment is set by app.go's QuestionsAvailableMsg
# handler and reads "🔔 <agent> is asking (Ctrl-Q)". We match on
# "is asking" — the bullet-emoji and Ctrl-Q hint may wrap at narrow
# widths but "is asking" always appears.

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
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

# Give the modal a beat to fully render before we send keys.
sleep 2

# --- Send keypresses: Down then Enter to select option 2 (beta) ---
#
# Cursor starts at option index 0 (alpha). One Down advances to index 1
# (beta). Enter confirms the single-question batch and emits
# QuestionAnsweredMsg, which the AppModel routes to
# supervisor.ResolveQuestion. We pick the SECOND option (not first or
# last) to defeat any "always default-cursor" buggy implementation.

echo ""
echo "=== Sending keys: Down, Enter (select option 2: $PROBE_BETA) ==="
_stmux send-keys -t "$SESSION" Down
sleep 0.3
_stmux send-keys -t "$SESSION" Enter

# --- Assert the manager received the response and reported the label ---
#
# The QuestionResponse JSON is returned to the manager's blocked MCP
# tool call. The manager's claude then calls report_status with the
# selected label as the summary — writing it into
# .sprawl/agents/auq-tester.json:last_report_message.

echo ""
echo "=== Waiting for manager to report the selected label ==="
if wait_for_state_field "last_report_message" "$PROBE_BETA" 240; then
    pass "manager state.last_report_message contains '$PROBE_BETA' (round-trip succeeded)"
else
    fail "manager state.last_report_message did not surface '$PROBE_BETA' within 240s"
    echo "  current last_report_message:" >&2
    jq -r '.last_report_message // "<unset>"' "$MANAGER_STATE" 2>/dev/null >&2 || true
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
fi

# --- Sanity check: modal indicator should clear after Resolve. ---
#
# After the question resolves, the statusbar depth drops to 0 and the
# pendingQuestions segment is omitted from View(). Give the AppModel a
# few ticks to redraw.

echo ""
echo "=== Verifying modal indicator cleared after Resolve ==="
sleep 3
if capture_pane "$SESSION" | grep -qE "is asking"; then
    fail "statusbar still shows 'is asking' after Resolve — queue not draining"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -20 >&2
else
    pass "statusbar 'is asking' segment cleared after Resolve"
fi

# --- Phase 2: Esc-cancel path (QUM-611 wedge regression guard) ---
#
# Plain Esc inside the modal must call Supervisor.CancelQuestion so the
# blocked MCP tool returns immediately and the caller's turn finalizes.
# Without the QUM-611 fix, DismissQuestionMsg only hides the modal and the
# MCP call remains parked indefinitely — the user-visible wedge.
#
# The phase drives root weave to spawn a fresh manager that calls
# ask_user_question, asserts the modal appears, sends a single Esc keypress
# (NOT a selection), then asserts (a) the modal closes, (b) the manager's
# blocked MCP call returns within 2s (proven by the manager's claude
# emitting its next turn — state.last_report_message updates with a fresh
# value), all within 30s.

echo ""
echo "=== Phase 2: Esc-cancel path (QUM-611 wedge regression guard) ==="

PROBE2="AUQ-CANCEL-PROBE-$$-$(date +%s)"
PROBE2_PRE="${PROBE2}-before-question"
PROBE2_POST="${PROBE2}-after-cancel"
PROBE2_ALPHA="${PROBE2}-alpha"
PROBE2_BETA="${PROBE2}-beta"
PROBE2_GAMMA="${PROBE2}-gamma"

# Spawn a fresh manager. The manager must:
#  STEP 1: call report_status with summary=<pre sentinel> so we have a
#          known-good baseline value to detect "the manager's next turn fired".
#  STEP 2: call ask_user_question. The blocked MCP call only returns on the
#          user side via Resolve OR Cancel. With QUM-611 fixed, Esc cancels;
#          the response carries Outcome=session_ended (no .selected[0]).
#  STEP 3: regardless of how the call returned (answered or cancelled), call
#          report_status with summary=<post sentinel>. We don't depend on the
#          response shape — the proof of un-wedge is "the next turn fired".
#  STEP 4: Stop.
SPAWN_PROMPT2="Call mcp__sprawl__spawn with family='engineering', type='manager', branch='qum-611-auq-cancel-test', and prompt set to exactly: 'You are an automated QUM-611 probe. STEP 1: call mcp__sprawl__report_status with state=working summary=\"${PROBE2_PRE}\". STEP 2: call mcp__sprawl__ask_user_question with questions=[{question:\"Esc-cancel probe (${PROBE2})\",multi_select:false,options:[{label:\"${PROBE2_ALPHA}\"},{label:\"${PROBE2_BETA}\"},{label:\"${PROBE2_GAMMA}\"}]}]. STEP 3: regardless of the response value, call mcp__sprawl__report_status with state=complete summary=\"${PROBE2_POST}\". STEP 4: Stop. Do nothing else.'"

_stmux send-keys -t "$SESSION" "$SPAWN_PROMPT2"
sleep 0.5
_stmux send-keys -t "$SESSION" Enter

# Discover the second manager by polling for a manager state file that did
# not exist before phase 2. Capture the set of pre-existing manager state
# files, then poll until a new one appears.
echo ""
echo "=== Waiting for phase 2 manager to spawn ==="
PRE_EXISTING_MANAGERS="|$(find "$SPRAWL_ROOT/.sprawl/agents" -maxdepth 1 -name '*.json' 2>/dev/null | sort | tr '\n' '|')"
MANAGER2_STATE=""
MANAGER2_NAME=""
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
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

# Wait for the baseline report_status so we can detect "next turn fired"
# unambiguously: post-Esc, last_report_message must transition from the
# pre-sentinel to the post-sentinel.
echo ""
echo "=== Waiting for phase 2 manager's pre-question baseline report ==="
if wait_for_state_field_path "$MANAGER2_STATE" "last_report_message" "$PROBE2_PRE" 120; then
    pass "phase 2 manager pre-question baseline observed"
else
    fail "phase 2 manager never reported pre-question baseline within 120s"
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

echo ""
echo "=== Waiting for phase 2 modal to appear ==="
if wait_for_pattern "$SESSION" "is asking" 180; then
    pass "phase 2 modal appeared (manager called ask_user_question)"
else
    fail "phase 2 modal never appeared within 180s"
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
    echo "==============================="
    echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

sleep 2

# Send a SINGLE Esc keypress — must NOT pick an option. With QUM-611 fixed
# this fires DismissQuestionMsg{Hard:true} → CancelQuestion → MCP call
# returns OutcomeSessionEnded → manager's claude continues to STEP 3.
echo ""
echo "=== Sending single Esc keypress (QUM-611 cancel path) ==="
# tmux buffers a lone ESC for `escape-time` (default 500ms) waiting for a
# possible CSI follow-up; flatten that on this server so the byte lands
# promptly. bubbletea v2 itself emits KeyEscape as soon as the buffered
# escape sequence parser times out (~25ms).
_stmux set-option -g escape-time 0 >/dev/null 2>&1 || true
# Bubbletea's ultraviolet decoder waits for its 50ms EscTimeout before
# finalizing a lone ESC as KeyEscape; we give it ample headroom (1.5s)
# after the single-byte send.
_stmux send-keys -t "$SESSION" Escape
sleep 1.5

# Assertion A: the modal indicator clears.
echo ""
echo "=== Asserting modal closes after Esc ==="
ELAPSED=0
MODAL_CLEARED=0
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

# Assertion B+C: the manager's NEXT turn fires (post-sentinel surfaces in
# last_report_message), proving (b) the blocked MCP call returned and (c)
# the manager's claude is no longer parked. 30s budget.
echo ""
echo "=== Asserting manager's MCP call returned and next turn fired ==="
if wait_for_state_field_path "$MANAGER2_STATE" "last_report_message" "$PROBE2_POST" 30; then
    pass "manager's last_report_message advanced to '$PROBE2_POST' within 30s (un-wedge confirmed)"
else
    fail "manager's last_report_message did NOT advance to post-sentinel within 30s — wedge persists (QUM-611 regression)"
    echo "  current last_report_message:" >&2
    jq -r '.last_report_message // "<unset>"' "$MANAGER2_STATE" 2>/dev/null >&2 || true
    echo "  pane tail:" >&2
    capture_pane "$SESSION" | tail -40 >&2
fi

# --- Summary ---

echo ""
echo "==============================="
echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "==============================="

if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
fi
exit 0
