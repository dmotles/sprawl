#!/usr/bin/env bash
# scripts/test-e2e-lockwait-unit.sh — pure-local unit tests for the e2e harness'
# weave.lock release wait (QUM-948).
#
# Why this exists: the e2e rows that kill a session and relaunch `sprawl enter`
# on the SAME SPRAWL_ROOT used to rely on a fixed `sleep 2` for the weave.lock
# flock to be released. flock is released by the kernel on fd-close, not on
# `tmux kill-session`, so under load teardown outruns the sleep and the relaunch
# dies with "another weave session is already running". The replacement is a
# bounded retry/poll (`e2e_wait_weave_lock_free` in scripts/lib/e2e-common.sh).
#
# The properties that matter are the ones invisible in a green row, and a wrong
# retry loop fails OPEN — it would paper over the flake while making a genuinely
# leaked lock look like a slow success. So this file pins, specifically:
#   * it waits materially longer than the old `sleep 2` (T4, with T5 as the
#     in-file negative control proving the fixture reproduces the bug);
#   * it BACKS OFF rather than spinning, and really re-probes (attempt counts);
#   * a leaked/never-released lock still FAILS, bounded, naming the holder (T6);
#   * it holds nothing itself (T7) and never false-fails when flock(1) is
#     missing (T11);
#   * `e2e_launch_tui` waits BEFORE it launches, and the old lock sleep is gone
#     from the caller (T12/T14).
#
# No claude, no tmux, no sandbox — just bash + coreutils + flock(1). Skips with
# exit 77 (QUM-952/QUM-997: a skip asserted nothing and must not exit 0) when
# flock(1) is absent. Run as: bash scripts/test-e2e-lockwait-unit.sh

set +e # Deliberately tolerate failed assertions so we report ALL failures.

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)
COMMON="$REPO_ROOT/scripts/lib/e2e-common.sh"
ROW_RESTART="$REPO_ROOT/scripts/e2e-tests/notif-stacked-restart.sh"

for f in "$COMMON" "$ROW_RESTART"; do
	if [ ! -f "$f" ]; then
		echo "  FAIL: $f not found — cannot test what is not there" >&2
		exit 1
	fi
done

if ! command -v flock >/dev/null 2>&1; then
	echo "SKIP: flock(1) not installed — weave.lock wait unit tests need it"
	exit 77
fi

# QUM-997 finding 1: an unchecked `mktemp -d` under `set +e` leaves every
# fixture path rooted at "/", which makes assertions pass vacuously.
TMPPARENT=$(mktemp -d) || {
	echo "  FAIL: mktemp -d failed — refusing to run with unrooted fixtures" >&2
	exit 1
}
case "$TMPPARENT" in
/tmp/*) : ;;
*)
	echo "  FAIL: TMPPARENT '$TMPPARENT' is not under /tmp — refusing (CLAUDE.md /tmp hygiene)" >&2
	exit 1
	;;
esac

# Holder PIDs are registered in a FILE, not a variable: cases run in `( )`
# subshells and `$( )` command substitutions, so a parent-side variable would
# never see them and the cleanup below would have nothing to kill.
HOLDERFILE="$TMPPARENT/holders"
: >"$HOLDERFILE"
cleanup() {
	local p
	while read -r p; do
		[ -n "$p" ] && kill -9 "$p" 2>/dev/null
	done <"$HOLDERFILE"
	case "$TMPPARENT" in
	/tmp/*) rm -rf -- "$TMPPARENT" ;;
	esac
	return 0
}
trap cleanup EXIT

# Assertion ledger: cases run in subshells, so counters cannot roll up in
# variables. A subshell that dies early contributes no lines and the floor
# below catches it — otherwise this script could pass while asserting nothing.
LEDGER="$TMPPARENT/ledger"
: >"$LEDGER"
FIXTURE_ERRORS="$TMPPARENT/fixture-errors"
: >"$FIXTURE_ERRORS"
# Bump when assertions are added or removed. Zero slack, deliberately.
MIN_ASSERTIONS=50

_lw_pass() {
	echo P >>"$LEDGER"
	echo "  PASS: $1"
}

_lw_fail() {
	echo F >>"$LEDGER"
	echo "  FAIL: $1" >&2
}

# A broken FIXTURE is not an assertion. It goes in its own ledger so it cannot
# pad $TOTAL and offset a died-early case 1:1 against the floor below; it fails
# the run on its own.
_lw_fixture_fail() {
	echo "F" >>"$FIXTURE_ERRORS"
	echo "  FIXTURE ERROR: $1" >&2
}

assert_eq() {
	if [ "$2" = "$3" ]; then
		_lw_pass "$1 (=$3)"
	else
		_lw_fail "$1: want '$2', got '$3'"
	fi
}

# yn RC — normalize an exit status to 1 (zero/true) or 0 (nonzero/false) so
# assert_eq compares a stable token rather than a raw status.
yn() { if [ "$1" -eq 0 ]; then echo 1; else echo 0; fi; }

# make_root MODE — fresh sandbox-shaped root. Echoes the root.
#   1     .sprawl/memory/weave.lock exists
#   0     .sprawl/memory exists, no lock file
#   nodir neither exists (the fresh-first-launch shape)
make_root() {
	local mode="$1" root
	root=$(mktemp -d "$TMPPARENT/root.XXXXXX") || return 1
	case "$mode" in
	nodir) : ;;
	*) mkdir -p "$root/.sprawl/memory" || return 1 ;;
	esac
	if [ "$mode" = "1" ]; then
		: >"$root/.sprawl/memory/weave.lock" || return 1
	fi
	echo "$root"
}

# hold_lock LOCKPATH SECONDS — hold an exclusive flock for SECONDS and record
# the holder PID in the lock file body, the way rootinit.AcquireWeaveLock does.
# Echoes the holder PID.
#
# The holder is a SINGLE process that owns the fd and blocks in bash's `read`
# builtin — deliberately not `flock -c "sleep N"`, because there the sleep child
# inherits the locked fd, so killing flock(1) leaves the lock HELD by an orphan.
# The fifo is opened read-write so the open does not block and never sees EOF.
hold_lock() {
	local lock="$1" secs="$2"
	local fifo="$lock.fifo.$$.$RANDOM"
	mkfifo "$fifo" || return 1
	bash -c "exec 9>'$lock'; flock -x 9 || exit 1; exec 3<>'$fifo'; read -r -t $secs _ <&3; exit 0" \
		>/dev/null 2>&1 &
	local pid=$!
	echo "$pid" >>"$HOLDERFILE"
	# Block until the lock is genuinely held, else the case below races its own
	# fixture rather than measuring the code under test.
	local i held=0
	for i in $(seq 1 200); do
		if ! flock -n -E 9 -x "$lock" true 2>/dev/null; then
			held=1
			break
		fi
		sleep 0.05
	done
	if [ "$held" -ne 1 ]; then
		_lw_fixture_fail "hold_lock never acquired $lock — the case below measures nothing"
		return 1
	fi
	# Record the PID in the body (AcquireWeaveLock writes it after locking).
	echo "$pid" >"$lock"
	echo "$pid"
}

echo "=== [1] short-circuit cases: nothing to wait for ==="
(
	# shellcheck disable=SC1090
	. "$COMMON"

	root=$(make_root 0) || _lw_fixture_fail "make_root 0 failed"
	start=$SECONDS
	e2e_wait_weave_lock_free "$root" 30
	assert_eq "T1: no lock file -> success" "0" "$?"
	assert_eq "T1: no lock file -> returns immediately" "1" "$(yn "$((SECONDS - start < 2 ? 0 : 1))")"
	assert_eq "T1: probe did not create the lock file" "1" \
		"$(yn "$([ -e "$root/.sprawl/memory/weave.lock" ] && echo 1 || echo 0)")"

	root=$(make_root nodir) || _lw_fixture_fail "make_root nodir failed"
	e2e_wait_weave_lock_free "$root" 30
	assert_eq "T2: no .sprawl/memory dir (fresh sandbox) -> success" "0" "$?"
	assert_eq "T2: probe did not create .sprawl/memory" "1" \
		"$(yn "$([ -d "$root/.sprawl/memory" ] && echo 1 || echo 0)")"

	root=$(make_root 1) || _lw_fixture_fail "make_root 1 failed"
	start=$SECONDS
	e2e_wait_weave_lock_free "$root" 30
	assert_eq "T3: lock file present but unheld -> success" "0" "$?"
	assert_eq "T3: unheld lock -> returns immediately" "1" "$(yn "$((SECONDS - start < 2 ? 0 : 1))")"
)

echo "=== [2] the QUM-948 regression: release after longer than the old sleep 2 ==="
(
	# shellcheck disable=SC1090
	. "$COMMON"

	root=$(make_root 1) || _lw_fixture_fail "make_root 1 failed"
	lock="$root/.sprawl/memory/weave.lock"
	holder=$(hold_lock "$lock" 60)

	# T5 (negative control, runs FIRST on the same fixture): the OLD harness
	# behavior — fixed `sleep 2` then a single probe — must FAIL here. This is
	# what proves T4's fixture reproduces the bug rather than passing vacuously.
	sleep 2
	flock -n -E 9 -x "$lock" true 2>/dev/null
	assert_eq "T5: negative control — old 'sleep 2 + single probe' still finds the lock held" "9" "$?"

	# Release ~4s from now: well past the old sleep 2, well inside the deadline.
	# (The holder's own 60s timeout is a backstop, not the release mechanism, so
	# a slow box cannot make the lock disappear early and pass T4 vacuously.)
	(
		sleep 4
		kill -9 "$holder" 2>/dev/null
	) &

	start=$SECONDS
	out=$(e2e_wait_weave_lock_free "$root" 30 2>&1)
	rc=$?
	elapsed=$((SECONDS - start))
	assert_eq "T4: lock released after the old sleep window -> success (waited ${elapsed}s)" "0" "$rc"
	assert_eq "T4: waited materially longer than the old sleep 2 (>=3s)" "1" \
		"$(yn "$((elapsed >= 3 ? 0 : 1))")"
	assert_eq "T4: returned on release, well inside the 30s deadline" "1" \
		"$(yn "$((elapsed < 25 ? 0 : 1))")"
	assert_eq "T4: emitted a parseable elapsed record" "1" \
		"$(yn "$(printf '%s' "$out" | grep -q "WEAVE_LOCK_WAIT_ELAPSED" && echo 0 || echo 1)")"
	attempts=$(printf '%s' "$out" | sed -n 's/.*attempts=\([0-9]\{1,\}\).*/\1/p' | tail -1)
	assert_eq "T4: really re-probed (attempts>1, got '${attempts:-none}')" "1" \
		"$(yn "$([ -n "$attempts" ] && [ "$attempts" -gt 1 ] && echo 0 || echo 1)")"

	# T7: probe-only — the helper must not be holding the lock afterwards.
	flock -n -E 9 -x "$lock" true 2>/dev/null
	assert_eq "T7: helper holds nothing after success" "0" "$?"
)

echo "=== [3] a genuinely leaked lock fails, bounded, naming the holder ==="
(
	# shellcheck disable=SC1090
	. "$COMMON"

	root=$(make_root 1) || _lw_fixture_fail "make_root 1 failed"
	lock="$root/.sprawl/memory/weave.lock"
	holder=$(hold_lock "$lock" 300)

	start=$SECONDS
	out=$(e2e_wait_weave_lock_free "$root" 3 2>&1)
	rc=$?
	elapsed=$((SECONDS - start))
	assert_eq "T6: leaked lock -> nonzero (fails the row, does not pass)" "1" \
		"$(yn "$([ "$rc" -ne 0 ] && echo 0 || echo 1)")"
	assert_eq "T6: leaked lock -> bounded, did not hang (${elapsed}s)" "1" \
		"$(yn "$((elapsed < 10 ? 0 : 1))")"
	assert_eq "T6: leaked lock -> actually waited out the deadline" "1" \
		"$(yn "$((elapsed >= 3 ? 0 : 1))")"
	assert_eq "T6: diagnostic names the full lock path" "1" \
		"$(yn "$(printf '%s' "$out" | grep -qF "$lock" && echo 0 || echo 1)")"
	assert_eq "T6: diagnostic names the holding PID ($holder)" "1" \
		"$(yn "$(printf '%s' "$out" | grep -qw "$holder" && echo 0 || echo 1)")"
	# Backoff, not a spin: a 0.2s-doubling-to-2s poll makes ~4-6 attempts in
	# 3s; a tight retry loop would make dozens.
	attempts=$(printf '%s' "$out" | sed -n 's/.*attempts=\([0-9]\{1,\}\).*/\1/p' | tail -1)
	assert_eq "T6: failure text reports the elapsed wait, not just the nominal deadline" "1" \
		"$(yn "$(printf '%s' "$out" | grep -qi "waited" && echo 0 || echo 1)")"
	assert_eq "T6: diagnostic reports the attempt count (got '${attempts:-none}')" "1" \
		"$(yn "$([ -n "$attempts" ] && [ "$attempts" -ge 2 ] && echo 0 || echo 1)")"
	assert_eq "T6: backed off rather than spinning (attempts<=15 over 3s)" "1" \
		"$(yn "$([ -n "$attempts" ] && [ "$attempts" -le 15 ] && echo 0 || echo 1)")"

	# T8: env-var override honored, and it bounds the wait.
	start=$SECONDS
	SPRAWL_E2E_LOCK_WAIT_SECS=1 e2e_wait_weave_lock_free "$root" >/dev/null 2>&1
	rc=$?
	elapsed=$((SECONDS - start))
	assert_eq "T8: SPRAWL_E2E_LOCK_WAIT_SECS honored -> nonzero" "1" \
		"$(yn "$([ "$rc" -ne 0 ] && echo 0 || echo 1)")"
	assert_eq "T8: SPRAWL_E2E_LOCK_WAIT_SECS honored -> bounded near 1s (${elapsed}s)" "1" \
		"$(yn "$((elapsed < 6 ? 0 : 1))")"

	kill -9 "$holder" 2>/dev/null
)

echo "=== [4] degradation: no flock(1) must not hard-fail every row ==="
(
	# shellcheck disable=SC1090
	. "$COMMON"

	root=$(make_root 1) || _lw_fixture_fail "make_root 1 failed"
	lock="$root/.sprawl/memory/weave.lock"
	holder=$(hold_lock "$lock" 300)

	# A real bin dir with the coreutils the helper may use, but NO flock — an
	# empty PATH would also remove sleep/ps/grep and could not distinguish
	# "warned about flock" from "the whole branch blew up".
	NOFLOCK="$TMPPARENT/noflock-bin"
	mkdir -p "$NOFLOCK"
	for c in sleep cat grep sed ps head tr fuser; do
		src=$(command -v "$c" 2>/dev/null) && ln -sf "$src" "$NOFLOCK/$c"
	done
	[ -e "$NOFLOCK/flock" ] && _lw_fixture_fail "stub bin unexpectedly contains flock"

	out=$(PATH="$NOFLOCK" e2e_wait_weave_lock_free "$root" 3 2>&1)
	rc=$?
	assert_eq "T11: flock(1) missing -> success (degrades, never false-fails)" "0" "$rc"
	assert_eq "T11: flock(1) missing -> warns about flock" "1" \
		"$(yn "$(printf '%s' "$out" | grep -qi "flock" && echo 0 || echo 1)")"

	kill -9 "$holder" 2>/dev/null
)

echo "=== [5] structural: the wait is wired in and the fixed sleep is gone ==="
(
	body=$(awk '/^e2e_launch_tui\(\) \{/,/^\}/' "$COMMON")
	waitline=$(printf '%s\n' "$body" | grep -n "e2e_wait_weave_lock_free" | head -1 | cut -d: -f1)
	newline=$(printf '%s\n' "$body" | grep -n "new-session" | head -1 | cut -d: -f1)
	assert_eq "T12: e2e_launch_tui calls e2e_wait_weave_lock_free" "1" \
		"$(yn "$([ -n "$waitline" ] && echo 0 || echo 1)")"
	assert_eq "T12: the wait precedes the tmux new-session" "1" \
		"$(yn "$([ -n "$waitline" ] && [ -n "$newline" ] && [ "$waitline" -lt "$newline" ] && echo 0 || echo 1)")"

	# T13: the default deadline is generous. Asserted structurally rather than
	# by making the suite wait 30s for it.
	assert_eq "T13: default deadline is SPRAWL_E2E_LOCK_WAIT_SECS:-30" "1" \
		"$(yn "$(grep -q 'SPRAWL_E2E_LOCK_WAIT_SECS:-30' "$COMMON" && echo 0 || echo 1)")"

	# T14: the whole point of QUM-948 — no relaunch path may sit on a fixed
	# sleep for lock release. The row's kill-session must not be followed by a
	# bare sleep standing in for the wait.
	#
	# Scope, stated plainly: this checks ONE file, within 2 lines of its
	# kill-session. It is not a repo-wide enforcement of the AC and cannot be
	# — the other rows' post-kill sleeps are legitimate (process-reap settles
	# before a `--resume`), so a widened grep would go red on correct code.
	# T12 is what covers every caller, structurally: the wait lives inside
	# e2e_launch_tui, so no row can relaunch without it.
	sleeps_after_kill=$(grep -A2 "kill-session" "$ROW_RESTART" | grep -cE '^[[:space:]]*sleep [0-9]')
	assert_eq "T14: notif-stacked-restart has no fixed sleep after kill-session" "0" "$sleeps_after_kill"
	assert_eq "T14 control: the row still has the kill-session this is about" "1" \
		"$(yn "$(grep -q "kill-session" "$ROW_RESTART" && echo 0 || echo 1)")"
)

echo "=== [6] strict mode: rows are sourced under set -euo pipefail ==="
(
	# The driver sources rows under `set -euo pipefail` (scripts/e2e-matrix.sh),
	# so an unguarded var read or a bare nonzero `flock` would abort the ROW,
	# not just the helper. Exercise all three legs: short-circuit, absent dir,
	# and the deadline (nonzero) path.
	sroot=$(make_root 1) || _lw_fixture_fail "make_root 1 failed"
	bash -c "set -euo pipefail; . '$COMMON'; e2e_wait_weave_lock_free '$sroot' 3; echo OK" \
		>"$TMPPARENT/strict1.out" 2>&1
	assert_eq "T15: strict-mode clean on the unheld-lock leg" "OK" "$(tail -1 "$TMPPARENT/strict1.out")"

	nroot=$(make_root nodir) || _lw_fixture_fail "make_root nodir failed"
	bash -c "set -euo pipefail; . '$COMMON'; e2e_wait_weave_lock_free '$nroot' 3; echo OK" \
		>"$TMPPARENT/strict2.out" 2>&1
	assert_eq "T15: strict-mode clean on the absent-dir leg" "OK" "$(tail -1 "$TMPPARENT/strict2.out")"

	hroot=$(make_root 1) || _lw_fixture_fail "make_root 1 failed"
	hlock="$hroot/.sprawl/memory/weave.lock"
	hholder=$(hold_lock "$hlock" 300)
	bash -c "set -euo pipefail; . '$COMMON'; if e2e_wait_weave_lock_free '$hroot' 2; then echo UNEXPECTED_OK; else echo EXPECTED_FAIL; fi" \
		>"$TMPPARENT/strict3.out" 2>&1
	assert_eq "T15: strict-mode clean on the deadline leg (nonzero, no abort)" "EXPECTED_FAIL" \
		"$(tail -1 "$TMPPARENT/strict3.out")"
	kill -9 "$hholder" 2>/dev/null
)

echo "=== [7] a malformed timeout knob must not abort the row ==="
(
	# shellcheck disable=SC1090
	. "$COMMON"

	root=$(make_root 1) || _lw_fixture_fail "make_root 1 failed"
	# `$((SECONDS + timeout))` treats a non-numeric operand as a VARIABLE NAME,
	# so under the driver's `set -u` a typo'd knob aborts the whole row with a
	# bare "abc: unbound variable" that names neither the knob nor the row.
	# "10s" and "1e3" are exactly the spellings an operator reaches for.
	for bad in abc 10s 1e3; do
		out=$(SPRAWL_E2E_LOCK_WAIT_SECS="$bad" e2e_wait_weave_lock_free "$root" 2>&1)
		rc=$?
		assert_eq "T16: SPRAWL_E2E_LOCK_WAIT_SECS='$bad' -> still succeeds on a free lock" "0" "$rc"
		assert_eq "T16: SPRAWL_E2E_LOCK_WAIT_SECS='$bad' -> warns instead of aborting" "1" \
			"$(yn "$(printf '%s' "$out" | grep -qi "SPRAWL_E2E_LOCK_WAIT_SECS" && echo 0 || echo 1)")"
		assert_eq "T16: SPRAWL_E2E_LOCK_WAIT_SECS='$bad' -> no 'unbound variable' abort" "1" \
			"$(yn "$(printf '%s' "$out" | grep -q "unbound variable" && echo 1 || echo 0)")"
	done

	# Strict mode is where the abort actually bites: prove the row survives.
	bash -c "set -euo pipefail; . '$COMMON'; SPRAWL_E2E_LOCK_WAIT_SECS=10s e2e_wait_weave_lock_free '$root'; echo OK" \
		>"$TMPPARENT/strict-badknob.out" 2>&1
	assert_eq "T16: strict-mode clean with a malformed knob" "OK" "$(tail -1 "$TMPPARENT/strict-badknob.out")"
)

echo "=== [8] an unprobeable lock is a WARN, not contention and not a hard fail ==="
(
	# shellcheck disable=SC1090
	. "$COMMON"

	root=$(make_root 1) || _lw_fixture_fail "make_root 1 failed"
	lock="$root/.sprawl/memory/weave.lock"
	# mode 000 (we are not root) makes flock's open fail with rc 66 — NOT the
	# rc 9 that means contention. This is the one leg that hands a lock we
	# cannot reason about back as "clear", so the policy needs pinning: a
	# future edit flipping it to `return 1` would turn every such row into a
	# hard failure, and nothing else would notice.
	chmod 000 "$lock"
	if flock -n -E 9 -x "$lock" true 2>/dev/null; then
		_lw_fixture_fail "chmod 000 did not make the lock unprobeable (running as root?)"
	fi
	start=$SECONDS
	out=$(e2e_wait_weave_lock_free "$root" 5 2>&1)
	rc=$?
	elapsed=$((SECONDS - start))
	assert_eq "T17: unprobeable lock (rc 66) -> success, not treated as contention" "0" "$rc"
	assert_eq "T17: unprobeable lock -> did not wait out the deadline" "1" \
		"$(yn "$((elapsed < 3 ? 0 : 1))")"
	assert_eq "T17: unprobeable lock -> warns, naming the probe status" "1" \
		"$(yn "$(printf '%s' "$out" | grep -qi "not contention" && echo 0 || echo 1)")"
	chmod 644 "$lock"
)

echo "=== [9] a failed wait refuses to launch at all ==="
(
	# shellcheck disable=SC1090
	. "$COMMON"

	# T18: T12 only asserts textual ordering. This asserts the BEHAVIOUR the
	# call site claims — "refusing to launch into a doomed acquire" — by
	# stubbing _stmux and proving new-session is never reached.
	root=$(make_root 1) || _lw_fixture_fail "make_root 1 failed"
	lock="$root/.sprawl/memory/weave.lock"
	holder=$(hold_lock "$lock" 300)

	TMUX_CALLS="$TMPPARENT/stmux-calls"
	: >"$TMUX_CALLS"
	_stmux() {
		echo "$*" >>"$TMUX_CALLS"
		return 0
	}
	SPRAWL_ROOT="$root"
	SPRAWL_BIN="/nonexistent/sprawl"
	export SPRAWL_ROOT SPRAWL_BIN
	out=$(SPRAWL_E2E_LOCK_WAIT_SECS=1 e2e_launch_tui "doomed-session" 80 24 2>&1)
	rc=$?
	assert_eq "T18: e2e_launch_tui fails when the lock is leaked" "1" \
		"$(yn "$([ "$rc" -ne 0 ] && echo 0 || echo 1)")"
	assert_eq "T18: e2e_launch_tui never reached tmux new-session" "0" \
		"$(grep -c "new-session" "$TMUX_CALLS")"
	assert_eq "T18: the refusal names the session it declined to launch" "1" \
		"$(yn "$(printf '%s' "$out" | grep -q "doomed-session" && echo 0 || echo 1)")"

	kill -9 "$holder" 2>/dev/null
)

TOTAL=$(wc -l <"$LEDGER" | tr -d ' ')
PASS=$(grep -c P "$LEDGER")
FAIL=$(grep -c F "$LEDGER")
# QUM-997 finding 2: the summary's own gates must not be the thing that breaks.
# A non-integer here makes `[ -lt ]` error out AND evaluate false, skipping both
# gates below, so validate the counters before trusting them.
for v in TOTAL PASS FAIL; do
	if ! [[ "${!v}" =~ ^[0-9]+$ ]]; then
		echo "  FAIL: ledger counter $v='${!v}' is not an integer — results are unreliable" >&2
		exit 1
	fi
done
FIXERR=$(wc -l <"$FIXTURE_ERRORS" | tr -d ' ')
echo
echo "=== weave.lock wait unit results: $PASS passed / $FAIL failed ($FIXERR fixture errors) ==="
if [ "$FIXERR" != "0" ]; then
	echo "  FAIL: $FIXERR fixture error(s) — some case measured nothing" >&2
	exit 1
fi
if [ "$TOTAL" -lt "$MIN_ASSERTIONS" ]; then
	echo "  FAIL: only $TOTAL assertions ran, expected at least $MIN_ASSERTIONS — a case died early and this run measured less than it claims" >&2
	exit 1
fi
if [ "$FAIL" -gt 0 ]; then
	exit 1
fi
exit 0
