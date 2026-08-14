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

# A2b and A3 each run `make lint`, and golangci-lint's lock is MACHINE-WIDE
# across every worktree and agent on this host (see /false-red § "parallel
# golangci-lint is running"). Linting ./... three more times per `make validate`
# would widen the window in which every other agent's validate hits that lock —
# i.e. it would manufacture a known false-red for everyone else.
#
# So these two assertions lint ONE small package instead. That costs them
# nothing: both ask WHICH BINARY ran, not what it found, and neither claim
# depends on the scope. `validate`'s own `lint` prerequisite still covers ./...
LINT_SCOPE=${LINT_SCOPE:-./internal/hooks/...}

pass() {
	PASS=$((PASS + 1))
	echo "  PASS: $1"
}
fail() {
	FAIL=$((FAIL + 1))
	echo "  FAIL: $1"
}

# Expected pinned version, read from the Makefile so this suite cannot drift
# from it silently.
#
# NOTE what A1 does and does not do: it compares this against the recipe TEXT
# from `make -n lint`, and both sides therefore come from the same Makefile
# line. A1 is not vacuous — reverting `lint:` to a bare `golangci-lint` empties
# RESOLVED and fails it — but it is a check on the WIRING, not a check that the
# resolved binary reports this version. A7 is the one that actually executes the
# pinned binary.
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
# A1: the version the Makefile's pin resolves to in the recipe.
#
# Both GOLANGCI_LINT and GOLANGCI_LINT_VERSION use `?=`, so the environment CAN
# override the pin (`GOLANGCI_LINT=golangci-lint make validate` restores the old
# PATH behaviour). That is a deliberate escape hatch for a one-off experiment,
# and it is not a hole in the gate: A1 runs `make -n lint` in the SAME
# environment, so an override that un-pins the recipe is reported here rather
# than silently honoured.
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
LINT_OUT=$(cd "$REPO_ROOT" && PATH="$DECOY_DIR:$PATH" make lint LINT_SCOPE="$LINT_SCOPE" 2>&1)
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

# A2d — same for fmt-check, which is a separate target with its own invocation.
#
# Deliberately asserts ONLY on the exit status, with no sentinel-in-output
# branch. fmt-check's recipe is `@test -z "$$($(GOLANGCI_LINT) fmt --diff ...)"`,
# so the linter's stdout is captured by the INNER $( ) and consumed by
# `test -z`; it never reaches an outer capture. A sentinel branch here would be
# unreachable code masquerading as an assertion — measured in a scratch dir with
# an unpinned recipe and the decoy on PATH: rc=2, and the sentinel was INVISIBLE
# in the captured output.
#
# The exit status is a sound discriminator anyway, and note WHY it is not the
# same tautology as A2c: the decoy prints a line to stdout and exits 0, so under
# an unpinned fmt-check `test -z` sees NON-empty output and fails the target.
# So an unpinned fmt-check is red here, a pinned one green.
FMT_OUT=$(cd "$REPO_ROOT" && PATH="$DECOY_DIR:$PATH" make fmt-check 2>&1)
FMT_RC=$?
if [ "$FMT_RC" -eq 0 ]; then
	pass "A2d make fmt-check ignored the PATH decoy and passed"
else
	fail "A2d make fmt-check returned $FMT_RC with the decoy on PATH; expected 0. Either the pin does not bind for fmt-check, or the tree genuinely needs formatting: $(printf '%s' "$FMT_OUT" | tail -2)"
fi

# ---------------------------------------------------------------------------
# A3: no dependence on $HOME/go/bin. The dotfiles add only ~/.local/bin, so a
# pin that quietly needed go/bin would break for the next operator.
# ---------------------------------------------------------------------------
# $HOME must be non-empty or the scrub patterns degrade to "^/go/bin$" and
# "^/.local/bin$", which match nothing — leaving A3 asserting "make lint passes
# with the normal PATH", a green that measures nothing. Same shape as the two
# false-greens already found in this file, so it gets a guard rather than a
# comment.
if [ -z "${HOME:-}" ]; then
	fail "A3 \$HOME is empty, so the PATH scrub would match nothing and this assertion would measure nothing"
else
	# Also scrubs $HOME/.local/bin, which is where this host's golangci-lint
	# actually lives — stronger than the AC's go/bin-only requirement, and it is
	# what gives A3 its power here.
	SCRUBBED=$(printf '%s' "$PATH" | tr ':' '\n' | grep -v "^$HOME/go/bin$" | grep -v "^$HOME/.local/bin$" | paste -sd: -)
	SCRUB_RC_OUT=$(cd "$REPO_ROOT" && env PATH="$SCRUBBED" make lint LINT_SCOPE="$LINT_SCOPE" 2>&1)
	SCRUB_RC=$?
	if [ "$SCRUB_RC" -eq 0 ]; then
		pass "A3 make lint passes with \$HOME/go/bin and \$HOME/.local/bin off PATH"
	else
		fail "A3 make lint failed (rc=$SCRUB_RC) with \$HOME/go/bin and \$HOME/.local/bin off PATH: $(printf '%s' "$SCRUB_RC_OUT" | tail -3)"
	fi
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
# Implemented by DELETING every $(GOLANGCI_LINT) from the range and failing on
# any surviving `golangci-lint`, rather than by a regex that tries to describe
# what a bare invocation looks like. The earlier regex form —
#   grep -nE '(^|[^$(])(\s|^)golangci-lint '
# required whitespace or line-start immediately before the name, and so missed
# BOTH real unpinned shapes: `$$(golangci-lint fmt --diff` (preceded by `(`,
# which is the literal pre-QUM-1223 fmt-check line) and `@golangci-lint`
# (preceded by `@`, idiomatic throughout this Makefile). Measured: reverting
# only fmt-check to the bare form left that grep returning rc=1, i.e. the
# regression undetected and A6 printing PASS. Only recipe lines (tab-indented)
# are considered, so the explanatory comments above may name the tool freely.
BARE=$(sed -n '/^fmt:/,/^test:/p' "$MAKEFILE" | grep '^	' | sed 's/\$(GOLANGCI_LINT)//g' | grep -n 'golangci-lint' || true)
if [ -z "$BARE" ]; then
	pass "A6 fmt/fmt-check/lint recipes invoke only the pinned \$(GOLANGCI_LINT)"
else
	fail "A6 a bare golangci-lint invocation is back in the Makefile recipes:"
	printf '%s\n' "$BARE" | sed 's/^/        /'
fi

# ---------------------------------------------------------------------------
# A7: the pinned binary really does carry gofumpt and goimports, so pinning
# golangci-lint pins formatting too. Makes the "confirm, don't assume"
# requirement durable instead of a one-time observation.
# ---------------------------------------------------------------------------
FORMATTERS=$(cd "$REPO_ROOT" && go run "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$EXPECTED_VERSION" formatters 2>/dev/null)
# Scoped to the "Enabled by your configuration" block. `formatters` prints an
# Enabled block AND a Disabled block, so an unscoped grep cannot tell them
# apart — measured: an A7-shaped grep over the whole output matches gci, gofmt,
# golines and swaggo, all of which are DISABLED here. That made the old A7 a
# false green: a .golangci.yml edit that disabled gofumpt would have left it
# passing while printing "reports ... as enabled formatters".
ENABLED=$(printf '%s\n' "$FORMATTERS" | sed -n '/^Enabled/,/^Disabled/p' | grep -v '^Disabled')
if [ -z "$ENABLED" ]; then
	fail "A7 could not parse an Enabled block out of \`formatters\` output — the scan is not measuring anything"
elif printf '%s\n' "$ENABLED" | grep -q '^gofumpt:' && printf '%s\n' "$ENABLED" | grep -q '^goimports:'; then
	pass "A7 pinned binary reports gofumpt and goimports as ENABLED formatters (so the pin pins formatting)"
else
	fail "A7 pinned binary did not report both gofumpt and goimports in its Enabled block"
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
