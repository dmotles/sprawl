#!/usr/bin/env bash
# scripts/test-wirelog-helpers-unit.sh — pure-local unit tests for the wire-log
# counting/ordering helpers hand-rolled inside the e2e row scripts.
#
# Why this exists: those helpers feed NON-VACUITY guards (e.g.
# idle-continuation.sh's "aborting rather than testing vacuously" abort). A
# helper that returns a non-integer (`null`, empty) makes bash's integer
# comparison error out AND evaluate false, which silently SKIPS the guard and
# lets the row print `pass` having measured nothing. That is the exact
# detector-that-cannot-detect failure the rows exist to prevent, so the guards
# themselves need to be gated rather than trusted.
#
# No claude, no tmux, no sandbox — just bash + jq + mktemp. Self-skips when jq
# is absent. Run as: bash scripts/test-wirelog-helpers-unit.sh

set +e # Deliberately tolerate failed assertions so we report ALL failures.

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)

IDLE_CONT="$REPO_ROOT/scripts/e2e-tests/idle-continuation.sh"
IDLE_INT="$REPO_ROOT/scripts/e2e-tests/idle-interrupt-inject.sh"

if ! command -v jq >/dev/null 2>&1; then
	echo "SKIP: jq not installed — wire-log helper unit tests need it"
	exit 0
fi

# Every fixture root lives under one parent tempdir so a single trap cleans up.
TMPPARENT=$(mktemp -d)
trap 'rm -rf "$TMPPARENT"' EXIT

# Assertion ledger. Each case runs in a subshell (so it can source a row file in
# isolation), so counters cannot roll up in variables. Every assertion appends a
# line here instead, which also gives us a floor check: a subshell that dies
# early — bad fixture, renamed row script — contributes no lines, and the floor
# below catches it. Without that, this script could itself pass while asserting
# nothing, which is precisely the failure it exists to prevent.
LEDGER="$TMPPARENT/ledger"
: >"$LEDGER"
# Bump when assertions are added or removed.
MIN_ASSERTIONS=54

pass() {
	echo P >>"$LEDGER"
	echo "  PASS: $1"
}

fail() {
	echo F >>"$LEDGER"
	echo "  FAIL: $1" >&2
}

assert_eq() {
	# $1 = description, $2 = want, $3 = got
	if [ "$2" = "$3" ]; then
		pass "$1 (=$3)"
	else
		fail "$1: want '$2', got '$3'"
	fi
}

assert_int() {
	# $1 = description, $2 = got. Single line, integer (optionally negative).
	if [ "$(printf '%s' "$2" | wc -l | tr -d ' ')" != "0" ]; then
		fail "$1: multi-line output '$2'"
	elif [[ "$2" =~ ^-?[0-9]+$ ]]; then
		pass "$1 (=$2)"
	else
		fail "$1: not an integer, got '$2'"
	fi
}

# make_root LINES... — a fresh SPRAWL_ROOT whose weave wire log holds LINES.
# Echoes the root. (mktemp, not a counter: these run in $( ) subshells, so a
# parent-side counter would never advance and every root would collide.)
make_root() {
	local root
	root=$(mktemp -d "$TMPPARENT/root.XXXXXX")
	mkdir -p "$root/.sprawl/logs/sessions/weave"
	printf '%s\n' "$@" >"$root/.sprawl/logs/sessions/weave/session.ndjson"
	echo "$root"
}

# empty_root — a SPRAWL_ROOT with no wire log at all.
empty_root() {
	mktemp -d "$TMPPARENT/root.XXXXXX"
}

# Envelope fixtures, shaped like the real wire log (internal/transcript):
# a valid outer NDJSON record with an ISO-8601 string `ts`, and the CLI frame
# escaped inside `.raw` retaining its trailing newline delimiter.
ENV_INIT_SEQ3='{"ts":"2026-07-25T00:00:01Z","dir":"out","seq":3,"raw":"{\"type\":\"system\",\"subtype\":\"init\"}\n"}'
ENV_RESULT_SEQ5='{"ts":"2026-07-25T00:00:02Z","dir":"out","seq":5,"raw":"{\"type\":\"result\",\"subtype\":\"success\"}\n"}'
ENV_RESULT_SEQ9='{"ts":"2026-07-25T00:00:03Z","dir":"out","seq":9,"raw":"{\"type\":\"result\",\"subtype\":\"success\"}\n"}'
ENV_NOTIF_SEQ12='{"ts":"2026-07-25T00:00:04Z","dir":"out","seq":12,"raw":"{\"type\":\"system\",\"subtype\":\"task_notification\"}\n"}'
ENV_IN_USER_SEQ13='{"ts":"2026-07-25T00:00:05Z","dir":"in","seq":13,"raw":"{\"type\":\"user\",\"priority\":\"now\"}\n"}'
# Legacy/regressed shape: no `seq` key at all (transcript.go still supports it;
# such logs exist on disk today).
ENV_RESULT_NOSEQ='{"ts":"2026-07-25T00:00:02Z","dir":"out","raw":"{\"type\":\"result\",\"subtype\":\"success\"}\n"}'
ENV_NOTIF_NOSEQ='{"ts":"2026-07-25T00:00:03Z","dir":"out","raw":"{\"type\":\"system\",\"subtype\":\"task_notification\"}\n"}'
ENV_RESULT_SEQSTR='{"ts":"2026-07-25T00:00:02Z","dir":"out","seq":"abc","raw":"{\"type\":\"result\",\"subtype\":\"success\"}\n"}'
ENV_IN_AUTOCONT='{"ts":"2026-07-25T00:00:06Z","dir":"in","seq":14,"raw":"{\"type\":\"user\",\"text\":\"[auto-continue] go\"}\n"}'
# A torn crash remnant. transcript.go documents that these can sit in the
# MIDDLE of a file after a resume, so the fixtures below place it there.
ENV_TORN='{"ts":"2026-07-25T00:00:07Z","dir":"out","seq":15,"raw":"{\"type\":\"resul'
# Valid outer envelope whose INNER frame is truncated — the shape the helpers'
# `fromjson? // empty` actually tolerates.
ENV_INNER_TORN='{"ts":"2026-07-25T00:00:08Z","dir":"out","seq":16,"raw":"{\"type\":\"resul"}'

echo "=== wire-log helper unit tests ==="

# ----------------------------------------------------------------------------
# 1. last_seq_of on a SEQ-LESS log must return -1 and the L239-shaped guard
#    must FIRE. jq prints the literal `null` for a missing key; "null" sails
#    past a -z test and makes `[ "$v" -lt 0 ]` error + evaluate false.
# ----------------------------------------------------------------------------
echo "[1] last_seq_of: seq-less log → -1 and the non-vacuity guard fires"
(
	SPRAWL_ROOT=$(make_root "$ENV_INIT_SEQ3" "$ENV_RESULT_NOSEQ")
	export SPRAWL_ROOT
	# shellcheck disable=SC1090
	. "$IDLE_CONT"

	got=$(last_seq_of result)
	assert_eq "last_seq_of result on a seq-less log returns -1" "-1" "$got"

	# The real L239 guard, verbatim in shape. Must fire (guard taken) AND must
	# not emit a bash integer-expression error.
	err=$( { [ "$got" -lt 0 ] && true; } 2>&1 )
	taken=$( { [ "$got" -lt 0 ] && echo TAKEN; } 2>/dev/null )
	assert_eq "L239-shaped abort guard fires on a seq-less log" "TAKEN" "$taken"
	assert_eq "L239-shaped guard emits no bash integer error" "" "$err"

)

# ----------------------------------------------------------------------------
# 2. Second call site (system/task_notification). In the real row L239 aborts
#    first whenever the anchor is -1, so this is a helper-level check: the
#    L298-shaped comparison must evaluate cleanly (no bash error) and take the
#    TEST-SETUP branch rather than silently printing pass.
# ----------------------------------------------------------------------------
echo "[2] last_seq_of system task_notification: seq-less log → -1, L298 gate evaluates"
(
	SPRAWL_ROOT=$(make_root "$ENV_RESULT_NOSEQ" "$ENV_NOTIF_NOSEQ")
	export SPRAWL_ROOT
	# shellcheck disable=SC1090
	. "$IDLE_CONT"

	t1=$(last_seq_of result)
	notif=$(last_seq_of system task_notification)
	assert_eq "last_seq_of system task_notification on a seq-less log returns -1" "-1" "$notif"

	err=$( { [ "$notif" -le "$t1" ] && true; } 2>&1 )
	assert_eq "L298-shaped gate emits no bash integer error" "" "$err"
	taken=$( { [ "$notif" -le "$t1" ] && echo TAKEN; } 2>/dev/null )
	assert_eq "L298-shaped gate fires (-1 <= -1) instead of silently passing" "TAKEN" "$taken"

)

# ----------------------------------------------------------------------------
# 3. Non-numeric seq is treated as absent.
# ----------------------------------------------------------------------------
echo "[3] last_seq_of: non-numeric seq → -1"
(
	SPRAWL_ROOT=$(make_root "$ENV_RESULT_SEQSTR")
	export SPRAWL_ROOT
	# shellcheck disable=SC1090
	. "$IDLE_CONT"
	assert_eq "last_seq_of result with seq=\"abc\" returns -1" "-1" "$(last_seq_of result)"
)

# ----------------------------------------------------------------------------
# 4/5. Happy path is NOT regressed: last match wins, type/subtype filtering and
#      the dir=="out" restriction still hold.
# ----------------------------------------------------------------------------
echo "[4] last_seq_of: happy path (last match wins, filters intact)"
(
	SPRAWL_ROOT=$(make_root "$ENV_INIT_SEQ3" "$ENV_RESULT_SEQ5" "$ENV_RESULT_SEQ9" \
		"$ENV_NOTIF_SEQ12" "$ENV_IN_USER_SEQ13")
	export SPRAWL_ROOT
	# shellcheck disable=SC1090
	. "$IDLE_CONT"
	assert_eq "last_seq_of result returns the LAST result seq" "9" "$(last_seq_of result)"
	assert_eq "last_seq_of system task_notification" "12" "$(last_seq_of system task_notification)"
	assert_eq "last_seq_of system init (subtype filter)" "3" "$(last_seq_of system init)"
	assert_eq "last_seq_of user ignores dir==in frames" "-1" "$(last_seq_of user)"
	assert_eq "last_seq_of on a type with no frames" "-1" "$(last_seq_of nosuchtype)"
)

echo "[5] last_seq_of: no wire log at all → -1"
(
	SPRAWL_ROOT=$(empty_root)
	export SPRAWL_ROOT
	# shellcheck disable=SC1090
	. "$IDLE_CONT"
	assert_eq "last_seq_of result with no log returns -1" "-1" "$(last_seq_of result)"
)

# ----------------------------------------------------------------------------
# 6. Sibling helpers: the audit, encoded. Every counter must yield exactly one
#    integer line for every fixture — normal, seq-less, torn (mid-file, where a
#    crash remnant really lands), and missing — and must keep the -1 = "could
#    not read" sentinel distinct from a genuine 0.
# ----------------------------------------------------------------------------
echo "[6] sibling counters always return a single integer line"
(
	ACT=""
	run_counters() {
		local label="$1"
		assert_int "count_out_frames init [$label]" "$(count_out_frames init)"
		assert_int "count_in_user_frames [$label]" "$(count_in_user_frames)"
		assert_int "count_autocontinue_in [$label]" "$(count_autocontinue_in)"
		assert_int "count_results [$label]" "$(count_results "$ACT")"
	}

	SPRAWL_ROOT=$(make_root "$ENV_INIT_SEQ3" "$ENV_RESULT_SEQ9" "$ENV_IN_USER_SEQ13" "$ENV_IN_AUTOCONT")
	export SPRAWL_ROOT
	# shellcheck disable=SC1090
	. "$IDLE_CONT"

	# count_results reads weave's activity.ndjson, not the wire log. Cover both
	# the has-matches and the zero-matches-in-an-existing-file branches — the
	# latter is where `grep -c` prints 0 but exits non-zero.
	mkdir -p "$SPRAWL_ROOT/.sprawl/agents/weave"
	ACT="$SPRAWL_ROOT/.sprawl/agents/weave/activity.ndjson"
	printf '%s\n' '{"kind":"result"}' '{"kind":"assistant"}' '{"kind":"result"}' >"$ACT"
	run_counters "normal"
	assert_eq "count_out_frames init counts the init frame" "1" "$(count_out_frames init)"
	assert_eq "count_in_user_frames counts both stdin user frames" "2" "$(count_in_user_frames)"
	assert_eq "count_autocontinue_in counts the sentinel frame" "1" "$(count_autocontinue_in)"
	assert_eq "count_results counts result entries" "2" "$(count_results "$ACT")"
	printf '%s\n' '{"kind":"assistant"}' >"$ACT"
	assert_eq "count_results on a file with zero matches" "0" "$(count_results "$ACT")"
	assert_eq "count_results on a missing file" "0" "$(count_results "$SPRAWL_ROOT/nope.ndjson")"

	SPRAWL_ROOT=$(make_root "$ENV_RESULT_NOSEQ" "$ENV_NOTIF_NOSEQ")
	ACT="$SPRAWL_ROOT/.sprawl/agents/weave/activity.ndjson"
	run_counters "seq-less"

	# A valid envelope with a truncated INNER frame — the shape `fromjson? //
	# empty` is there to absorb. Surrounding frames must still be counted.
	SPRAWL_ROOT=$(make_root "$ENV_INIT_SEQ3" "$ENV_INNER_TORN" "$ENV_IN_USER_SEQ13")
	ACT="$SPRAWL_ROOT/.sprawl/agents/weave/activity.ndjson"
	run_counters "torn inner frame"
	assert_eq "count_out_frames survives a torn inner frame" "1" "$(count_out_frames init)"
	assert_eq "count_in_user_frames survives a torn inner frame" "1" "$(count_in_user_frames)"

	# A torn OUTER line mid-file. jq's stream parse aborts there, so frames
	# after the tear are dropped — the counters UNDERCOUNT, which can make a
	# row FALSE-FAIL and mis-blame upstream. last_seq_of degrades safely (the
	# -1 abort sentinel, not a plausible-looking stale seq). Pinned here so the
	# behavior is documented rather than discovered during an incident; the fix
	# belongs to the shared wire-log lib, QUM-946.
	SPRAWL_ROOT=$(make_root "$ENV_INIT_SEQ3" "$ENV_TORN" "$ENV_INIT_SEQ3" "$ENV_NOTIF_SEQ12")
	ACT="$SPRAWL_ROOT/.sprawl/agents/weave/activity.ndjson"
	run_counters "torn outer line mid-file"
	assert_eq "QUM-946: torn outer line undercounts (frames after the tear dropped)" "1" "$(count_out_frames init)"
	assert_eq "torn outer line: last_seq_of reports not-found (safe abort, not a silent pass)" "-1" "$(last_seq_of system task_notification)"

	SPRAWL_ROOT=$(empty_root)
	ACT="$SPRAWL_ROOT/nope.ndjson"
	run_counters "no log"
	assert_eq "count_out_frames sentinel for an unreadable log" "-1" "$(count_out_frames init)"
	assert_eq "count_in_user_frames sentinel for an unreadable log" "-1" "$(count_in_user_frames)"
	assert_eq "count_autocontinue_in sentinel for an unreadable log" "-1" "$(count_autocontinue_in)"

)

# ----------------------------------------------------------------------------
# 7. The near-duplicate helper in the idle-interrupt-inject row. Same integer
#    contract, same sentinel, and the same newest-by-mtime log selection as
#    wire_log — an alphabetically-first pick lands on a pre-restart log and
#    silently zeroes the storm gate.
# ----------------------------------------------------------------------------
echo "[7] idle-interrupt-inject count_now_writes: integer contract, sentinel, newest log"
(
	SPRAWL_ROOT=$(make_root "$ENV_IN_USER_SEQ13" "$ENV_RESULT_NOSEQ")
	export SPRAWL_ROOT
	# shellcheck disable=SC1090
	. "$IDLE_INT"
	assert_int "count_now_writes weave [normal]" "$(count_now_writes weave)"
	assert_eq "count_now_writes counts the priority:now frame" "1" "$(count_now_writes weave)"

	SPRAWL_ROOT=$(make_root "$ENV_IN_USER_SEQ13" "$ENV_INNER_TORN" "$ENV_IN_USER_SEQ13")
	assert_int "count_now_writes weave [torn inner frame]" "$(count_now_writes weave)"
	assert_eq "count_now_writes counts frames on BOTH sides of a torn inner frame" "2" "$(count_now_writes weave)"

	SPRAWL_ROOT=$(make_root "$ENV_INIT_SEQ3")
	assert_eq "count_now_writes on a log with no now-writes" "0" "$(count_now_writes weave)"

	# Two logs: the post-restart one (newest mtime) carries the now-write but
	# sorts alphabetically LAST.
	SPRAWL_ROOT=$(make_root "$ENV_INIT_SEQ3")
	logdir="$SPRAWL_ROOT/.sprawl/logs/sessions/weave"
	mv "$logdir/session.ndjson" "$logdir/aaa.ndjson"
	touch -t 202001010000 "$logdir/aaa.ndjson" || fail "fixture: touch -t failed, mtime ordering unreliable"
	printf '%s\n' "$ENV_IN_USER_SEQ13" >"$logdir/zzz.ndjson"
	assert_eq "count_now_writes reads the NEWEST log, not the alphabetically first" "1" "$(count_now_writes weave)"

	SPRAWL_ROOT=$(empty_root)
	assert_int "count_now_writes weave [no log]" "$(count_now_writes weave)"
	assert_eq "count_now_writes sentinel for an unreadable log" "-1" "$(count_now_writes weave)"
)

TOTAL=$(wc -l <"$LEDGER" | tr -d ' ')
PASS=$(grep -c P "$LEDGER")
FAIL=$(grep -c F "$LEDGER")
echo
echo "=== wire-log helper unit results: $PASS passed / $FAIL failed ==="
if [ "$TOTAL" -lt "$MIN_ASSERTIONS" ]; then
	echo "  FAIL: only $TOTAL assertions ran, expected at least $MIN_ASSERTIONS — a case died early and this run measured less than it claims" >&2
	exit 1
fi
if [ "$FAIL" -gt 0 ]; then
	exit 1
fi
exit 0
