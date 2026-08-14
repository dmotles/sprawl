#!/usr/bin/env bash
# Unit gate for the QUM-1223 golangci-lint pin.
#
# WHY THIS EXISTS. `make validate` is this repo's gate and is what the
# pre-commit hook runs, but until QUM-1223 the `fmt`, `fmt-check` and `lint`
# targets invoked a bare `golangci-lint`, resolved via PATH. So the strictness
# of the gate was a property of the host, not of the repo. The 2026-08-13
# migration made it stricter and failed loudly; the same drift the other way is
# silent. Worse, measured: a decoy executable named `golangci-lint` earlier on
# PATH that exits 0 takes over the gate completely — `make lint` exited 0 over
# 6 real findings.
#
# A green `make lint` proves NOTHING about the pin on its own: if the pinned
# and PATH binaries are the same version, "it passed" is equally consistent
# with the pin doing nothing at all. So A2 below makes them DIFFER, which is
# the only form of that check with any power.
#
# Self-contained. Run as: bash scripts/test-lint-pin.sh
# Needs bash, go, make, grep, sed, mktemp. No claude, no tmux, no network
# beyond whatever the Go module cache already has.
#
# EXIT CODES:
#   0  every assertion ran and passed
#   1  >=1 assertion failed, or the assertion-count floor was not met
#   2  usage / internal error (could not locate the repo root)
#  77  skipped: an unmet precondition (no `go` toolchain). NEVER 0 — a skip
#      that exits 0 makes `make` see success over a harness that asserted
#      nothing.

set +e # Deliberately tolerate failed assertions so we report ALL of them.

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd) || {
	echo "cannot resolve repo root"
	exit 2
}
cd "$REPO_ROOT" || {
	echo "cannot cd to repo root: $REPO_ROOT"
	exit 2
}

MAKEFILE="$REPO_ROOT/Makefile"
GOLANGCI_CONFIG="$REPO_ROOT/.golangci.yml"

# Assertion-count floor. A hardcoded literal, NOT derived from anything this
# suite measures — a floor computed from the corpus it checks is satisfied by an
# empty corpus, which is the exact false-green it exists to stop. Update it in
# the same commit as any change to the number of assertions below.
MIN_ASSERTIONS=10

PASS=0
FAIL=0

pass() {
	PASS=$((PASS + 1))
	echo "  PASS: $1"
}
fail() {
	FAIL=$((FAIL + 1))
	echo "  FAIL: $1"
}

# Expected pinned version, read from the Makefile so this suite cannot drift
# from it silently. A1 then asserts the resolved binary actually reports it.
EXPECTED_VERSION=$(sed -n 's/^GOLANGCI_LINT_VERSION ?= v\(.*\)$/\1/p' "$MAKEFILE" | head -1)

command -v go >/dev/null 2>&1 || {
	echo "SKIP: no 'go' toolchain on PATH; cannot exercise the pin"
	exit 77
}
[ -n "$EXPECTED_VERSION" ] || {
	echo "  FAIL: could not read GOLANGCI_LINT_VERSION from $MAKEFILE"
	echo "=== lint-pin results: 0 passed / 1 failed ==="
	exit 1
}

echo "=== lint-pin gate (expected golangci-lint v$EXPECTED_VERSION) ==="

# ---------------------------------------------------------------------------
# A1: the version the Makefile's pin actually resolves to.
# ---------------------------------------------------------------------------
RESOLVED=$(make -s -f "$MAKEFILE" -n lint 2>/dev/null | grep -o 'golangci-lint@v[0-9.]*' | head -1)
if [ "$RESOLVED" = "golangci-lint@v$EXPECTED_VERSION" ]; then
	pass "A1 make lint resolves the pinned $RESOLVED"
else
	fail "A1 make lint does not resolve the pinned version: got '$RESOLVED', want 'golangci-lint@v$EXPECTED_VERSION'"
fi

# ---------------------------------------------------------------------------
# A2: THE PIN BINDS. Put a DIFFERENT golangci-lint earlier on PATH and prove
# the pinned one still runs. This is the AC's positive control, and it is
# aimed correctly: the decoy is verified to be reachable (A2a) before its
# absence from the real run is credited as evidence (A2b/A2c).
# ---------------------------------------------------------------------------
DECOY_DIR=$(mktemp -d /tmp/lint-pin-decoy.XXXXXX) || {
	echo "  FAIL: mktemp -d failed"
	echo "=== lint-pin results: $PASS passed / $((FAIL + 1)) failed ==="
	exit 1
}
trap 'rm -rf "$DECOY_DIR"' EXIT
cat >"$DECOY_DIR/golangci-lint" <<'DECOY'
#!/bin/sh
echo "SPRAWL-LINT-PIN-DECOY-SENTINEL ran with: $*"
exit 0
DECOY
chmod +x "$DECOY_DIR/golangci-lint"

# A2a — the decoy is genuinely reachable and genuinely wins a PATH lookup.
# Without this leg, A2b passing is indistinguishable from a decoy that never
# could have run.
if PATH="$DECOY_DIR:$PATH" golangci-lint --version 2>&1 | grep -q 'SPRAWL-LINT-PIN-DECOY-SENTINEL'; then
	pass "A2a decoy is reachable and wins a bare PATH lookup (control is aimed correctly)"
else
	fail "A2a decoy did NOT win a bare PATH lookup — A2b/A2c below would prove nothing"
fi

# A2b — with the decoy first on PATH, `make lint` must NOT run it.
LINT_OUT=$(cd "$REPO_ROOT" && PATH="$DECOY_DIR:$PATH" make lint 2>&1)
LINT_RC=$?
if echo "$LINT_OUT" | grep -q 'SPRAWL-LINT-PIN-DECOY-SENTINEL'; then
	fail "A2b make lint ran the PATH decoy instead of the pinned binary — the pin does not bind"
else
	pass "A2b make lint ignored the PATH decoy"
fi
if [ "$LINT_RC" -eq 0 ]; then
	pass "A2c make lint still passed with the decoy on PATH (rc=0)"
else
	fail "A2c make lint returned $LINT_RC with the decoy on PATH; expected 0"
fi

# A2d — same for fmt-check, which is a separate target with its own
# invocation. A decoy that exits 0 silently satisfies `test -z`, so an
# unpinned fmt-check is a false green in exactly the same way as lint.
FMT_OUT=$(cd "$REPO_ROOT" && PATH="$DECOY_DIR:$PATH" make fmt-check 2>&1)
FMT_RC=$?
if echo "$FMT_OUT" | grep -q 'SPRAWL-LINT-PIN-DECOY-SENTINEL'; then
	fail "A2d make fmt-check ran the PATH decoy instead of the pinned binary"
elif [ "$FMT_RC" -eq 0 ]; then
	pass "A2d make fmt-check ignored the PATH decoy and passed"
else
	fail "A2d make fmt-check returned $FMT_RC with the decoy on PATH; expected 0"
fi

# ---------------------------------------------------------------------------
# A3: no dependence on $HOME/go/bin. The dotfiles add only ~/.local/bin, so a
# pin that quietly needed go/bin would break for the next operator.
# ---------------------------------------------------------------------------
SCRUBBED=$(printf '%s' "$PATH" | tr ':' '\n' | grep -v "^$HOME/go/bin$" | grep -v "^$HOME/.local/bin$" | paste -sd: -)
SCRUB_RC_OUT=$(cd "$REPO_ROOT" && env PATH="$SCRUBBED" make lint 2>&1)
SCRUB_RC=$?
if [ "$SCRUB_RC" -eq 0 ]; then
	pass "A3 make lint passes with \$HOME/go/bin and \$HOME/.local/bin off PATH"
else
	fail "A3 make lint failed (rc=$SCRUB_RC) with \$HOME/go/bin and \$HOME/.local/bin off PATH: $(printf '%s' "$SCRUB_RC_OUT" | tail -3)"
fi

# ---------------------------------------------------------------------------
# A4: no blanket G703 disable in .golangci.yml. The QUM-1223 judgment is that
# the three flagged sites are false positives IN KIND; a repo-wide disable
# would also silence a future site whose taint source really is untrusted, and
# nobody would see it happen.
# ---------------------------------------------------------------------------
if grep -qE '(^|[^A-Za-z0-9])G703([^0-9]|$)' "$GOLANGCI_CONFIG" 2>/dev/null; then
	fail "A4 .golangci.yml mentions G703 — a repo-wide disable hides future genuinely-tainted sites"
else
	pass "A4 .golangci.yml carries no blanket G703 exclusion"
fi

# ---------------------------------------------------------------------------
# A5: every //#nosec in the tree names a rule ID and carries a reason. A bare
# //#nosec suppresses every gosec rule at that line, which is the repo-wide
# disable spelled locally.
# ---------------------------------------------------------------------------
# Scanned via `git ls-files`, NOT a recursive grep over $REPO_ROOT. An earlier
# draft filtered the recursive results with `grep -v '/.sprawl/'` to skip agent
# state — but every agent worktree IS itself under a `/.sprawl/` path
# (.sprawl/worktrees/<name>), so that filter discarded 3 of 3 real matches and
# A5 could never fire. Measured: raw 3, after filter 0, gate still green.
# git ls-files is scoped to tracked files in this worktree by construction.
NOSEC_HITS=$(git ls-files -z '*.go' | xargs -0 grep -n '//#nosec' 2>/dev/null || true)
BAD_NOSEC=$(printf '%s\n' "$NOSEC_HITS" | grep '//#nosec' | grep -vE '//#nosec [A-Z][0-9]+ -- .' || true)
if [ -z "$NOSEC_HITS" ]; then
	# Not a pass: this suite exists partly to police these directives, and a
	# scan that finds none has probably stopped scanning.
	fail "A5 found NO //#nosec directives at all — the scan is not measuring anything (QUM-1223 added three)"
elif [ -z "$BAD_NOSEC" ]; then
	pass "A5 every //#nosec names a rule ID and gives a reason ($(printf '%s\n' "$NOSEC_HITS" | grep -c '//#nosec') found)"
else
	fail "A5 found //#nosec without a rule ID and reason:"
	printf '%s\n' "$BAD_NOSEC" | sed 's/^/        /'
fi

# ---------------------------------------------------------------------------
# A6: no bare `golangci-lint` left in the fmt/fmt-check/lint recipes. This is
# the regression that re-introduces the whole defect, and it is a one-word
# edit away at all times.
# ---------------------------------------------------------------------------
BARE=$(sed -n '/^fmt:/,/^test:/p' "$MAKEFILE" | grep -nE '(^|[^$(])(\s|^)golangci-lint ' || true)
if [ -z "$BARE" ]; then
	pass "A6 fmt/fmt-check/lint recipes contain no bare golangci-lint invocation"
else
	fail "A6 a bare golangci-lint invocation is back in the Makefile:"
	printf '%s\n' "$BARE" | sed 's/^/        /'
fi

# ---------------------------------------------------------------------------
# A7: the pinned binary really does carry gofumpt and goimports, so pinning
# golangci-lint pins formatting too. Makes the "confirm, don't assume"
# requirement durable instead of a one-time observation.
# ---------------------------------------------------------------------------
FORMATTERS=$(cd "$REPO_ROOT" && go run "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$EXPECTED_VERSION" formatters 2>/dev/null)
if printf '%s' "$FORMATTERS" | grep -q 'gofumpt' && printf '%s' "$FORMATTERS" | grep -q 'goimports'; then
	pass "A7 pinned binary reports gofumpt and goimports as enabled formatters"
else
	fail "A7 pinned binary did not report both gofumpt and goimports as enabled"
fi

# ---------------------------------------------------------------------------
# Summary + assertion-count floor.
# ---------------------------------------------------------------------------
TOTAL=$((PASS + FAIL))
echo "=== lint-pin results: $PASS passed / $FAIL failed ==="
if [ "$TOTAL" -lt "$MIN_ASSERTIONS" ]; then
	echo "  FAIL: only $TOTAL assertions ran, expected at least $MIN_ASSERTIONS — this run measured less than it claims"
	exit 1
fi
[ "$FAIL" -eq 0 ] || exit 1
exit 0
