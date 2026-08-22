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
#   2  usage / internal error (could not locate the repo root, or mktemp handed
#      back a scratch path outside /tmp so cleanup was refused)
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
MIN_ASSERTIONS=33

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
#
# The QUM-1232 legs (A10/A11) DO lint several extra times, and they get away with
# it because the machine-wide lock is not where this file previously assumed: it
# is `$TMPDIR/golangci-lint.lock` (pkg/commands/run.go:492 in v2.12.2), not
# anything under the cache. Measured both directions on this host: a held lock at
# $PRIVATE/golangci-lint.lock plus TMPDIR=$PRIVATE reports "parallel
# golangci-lint is running" and the tool exits 3 (seen as "exit status 3"; note
# `go run` then reports 1 of its own, so an rc of 1 from these legs is not
# self-describing); the same held lock with the default TMPDIR prints
# "0 issues.". So every A10/A11 run below sets a private TMPDIR and
# takes a private lock — it does NOT widen the window for any other agent. That
# is also why none of them may use --allow-parallel-runners, which is still
# forbidden.
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
# QUM-1287 REWROTE this block. It used to argue at length that a
# sentinel-in-output assertion here was unreachable, because the recipe was
# `@test -z "$$($(GOLANGCI_LINT) fmt --diff ./...)"` and the linter's stdout was
# consumed by the inner $( ) and never reached an outer capture. That was true and
# is now false: the recipe captures the output and PRINTS it (it also propagates
# the exit status, which it previously discarded — see F1/F2 below). So the
# sentinel leg is now live, and asserting it is strictly stronger than the
# exit-status-only form, which could not distinguish "the pin bound" from "the
# decoy ran and happened to satisfy the check".
#
# Scoped via FMT_SCOPE for the same reason A2b/A2c use LINT_SCOPE: this asks
# WHICH BINARY ran, not what it found.
FMT_OUT=$(cd "$REPO_ROOT" && PATH="$DECOY_DIR:$PATH" make fmt-check FMT_SCOPE="$LINT_SCOPE" 2>&1)
FMT_RC=$?
if [ "$FMT_RC" -eq 0 ]; then
	pass "A2d make fmt-check ignored the PATH decoy and passed"
else
	fail "A2d make fmt-check returned $FMT_RC with the decoy on PATH; expected 0. Either the pin does not bind for fmt-check, or the tree genuinely needs formatting: $(printf '%s' "$FMT_OUT" | tail -3)"
fi
if echo "$FMT_OUT" | grep -q 'SPRAWL-LINT-PIN-DECOY-SENTINEL'; then
	fail "A2e make fmt-check ran the PATH decoy instead of the pinned binary — the pin does not bind for fmt-check"
else
	pass "A2e make fmt-check output carries no decoy sentinel"
fi
# A2f — POSITIVE CONTROL for A2e, aimed at the sentinel appearing. The decoy IS
# the tool here (GOLANGCI_LINT points at it), so the sentinel MUST show up in the
# captured output. Without this, A2e is indistinguishable from a recipe whose
# output never reaches the caller — the exact false-green this block used to
# document as unavoidable.
FMT_CTL=$(cd "$REPO_ROOT" && make fmt-check GOLANGCI_LINT="$DECOY_DIR/golangci-lint" FMT_SCOPE="$LINT_SCOPE" 2>&1)
if echo "$FMT_CTL" | grep -q 'SPRAWL-LINT-PIN-DECOY-SENTINEL'; then
	pass "A2f control fired: with the decoy AS the formatter its sentinel is visible, so A2e can fail"
else
	fail "A2f control did NOT fire: the decoy's sentinel was invisible even when it was the formatter, so A2e proves nothing. Output: $(printf '%s' "$FMT_CTL" | tail -3)"
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
	fail "A5 found NO //#nosec directives at all — the scan is not measuring anything (QUM-1223 added three; QUM-1227 deleted one with internal/agent/claude.go, leaving two)"
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
# GOLANGCI_LINT_CACHE is set explicitly because this leg invokes the tool
# DIRECTLY rather than through make: run as `bash scripts/test-lint-pin.sh` it
# would otherwise fall back to the machine-wide shared cache (QUM-1232). Nothing
# is analysed here — `formatters` only prints configuration — so there is no
# poisoning path either way; this keeps the file's premise ("no leg touches the
# shared cache") literally true.
FORMATTERS=$(cd "$REPO_ROOT" && env GOLANGCI_LINT_CACHE="$REPO_ROOT/.golangci-cache" \
	go run "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$EXPECTED_VERSION" formatters 2>/dev/null)
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
# A8-A11: CACHE ISOLATION (QUM-1232).
#
# golangci-lint's cache defaults to os.UserCacheDir()/golangci-lint — one
# namespace for every worktree and every agent on this host. Two sibling
# worktrees of this repo with identical content produce IDENTICAL cache keys,
# because file paths in the hash are relativized to the module path
# (internal/cache/cache.go:157-181), while the cached issue carries the ABSOLUTE
# filename of whichever worktree produced it (pkg/goanalysis/runners_cache.go
# :41-50). The observable result, reproduced by A10b below: your worktree's own
# real finding is reported ONLY as `../<other-worktree>/main.go:3:6` and ZERO
# times at any path inside your tree. A finding attributed away from your tree
# cannot be fixed by you and will not be noticed — that is a false GREEN wearing
# a false red's clothes, and it is the direction that matters.
#
# A8 pins the WIRING (the Makefile resolves a cache dir inside this worktree);
# A10c pins that the wiring's SCHEME — a `.golangci-cache` dir inside each
# worktree — actually defeats the poisoning. Neither claim stands alone: A8
# without A10c is a value nobody proved works, A10c without A8 is a property
# the build does not use. A10b is the positive control that makes A10c's
# silence mean something.
#
# All of these lint a THROWAWAY module under /tmp, never this tree. A planted
# violation inside the repo would be found by `make lint`/`fmt-check`
# themselves, i.e. it would break the gate it is trying to measure.
# ---------------------------------------------------------------------------

SCRATCH_ROOT=$(mktemp -d /tmp/lint-pin-cache.XXXXXX) || {
	echo "  FAIL: mktemp -d failed for the cache-isolation fixtures"
	echo "=== lint-pin results: $PASS passed / $((FAIL + 1)) failed ==="
	exit 1
}
# Assert the path is ours and under /tmp BEFORE any rm -rf is armed on it. Never
# trust a variable's value when destroying files (CLAUDE.md).
case "$SCRATCH_ROOT" in
/tmp/lint-pin-cache.*) ;;
*)
	echo "  FAIL: mktemp returned an unexpected path, refusing to arm cleanup: $SCRATCH_ROOT"
	echo "=== lint-pin results: $PASS passed / $((FAIL + 1)) failed ==="
	exit 2
	;;
esac
trap 'rm -rf "$DECOY_DIR" "$SCRATCH_ROOT"' EXIT

# Sequence number for per-call TMPDIRs below. One TMPDIR per lint call keeps
# go-build work dirs from accumulating and removes the only channel by which a
# deliberately-poisoned run could reach a later leg.
LINT_LEG=0

# The fixture: one package with one unused function. Caught by `unused`, which is
# on by default, so the fixture needs no .golangci.yml of its own — deliberately,
# since a copy of this repo's config would couple these legs to config edits they
# are not about. Expected finding: main.go:3:6.
mk_fixture() {
	mkdir -p "$1" || return 1
	cat >"$1/go.mod" <<'FIXMOD'
module example.com/m

go 1.21
FIXMOD
	cat >"$1/main.go" <<'FIXSRC'
package main

func unusedFn() {}

func main() {}
FIXSRC
}

# Rewrites the fixture with the violation REMOVED. Used by A11b, whose whole job
# is to show A11a tracks the code rather than being a constant-true grep.
mk_fixture_clean() {
	cat >"$1/main.go" <<'FIXCLEAN'
package main

func main() {}
FIXCLEAN
}

# Lints $1 with cache dir $2. Sets SL_OUT/SL_RC. TMPDIR is private per call so
# the machine-wide lock is not touched (see the LINT_SCOPE comment above).
#
# SL_OUT is a PLAIN global assignment on purpose: `local SL_OUT=$(...)` would make
# the following $? the status of `local`, not of the command substitution.
scratch_lint() {
	LINT_LEG=$((LINT_LEG + 1))
	local tmp="$SCRATCH_ROOT/tmpdir-$LINT_LEG"
	mkdir -p "$tmp" "$2" || {
		SL_OUT="scratch_lint: could not create $tmp or $2"
		SL_RC=99
		return
	}
	SL_OUT=$(cd "$1" && env TMPDIR="$tmp" GOLANGCI_LINT_CACHE="$2" \
		go run "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$EXPECTED_VERSION" run ./... 2>&1)
	SL_RC=$?
}

# scratch_fmt is scratch_lint's `fmt --diff` sibling, same private-TMPDIR
# discipline. Used by C2 to compare what the two subcommands can SEE.
scratch_fmt() {
	LINT_LEG=$((LINT_LEG + 1))
	local tmp="$SCRATCH_ROOT/tmpdir-$LINT_LEG"
	mkdir -p "$tmp" "$2" || {
		SL_OUT="scratch_fmt: could not create $tmp or $2"
		SL_RC=99
		return
	}
	SL_OUT=$(cd "$1" && env TMPDIR="$tmp" GOLANGCI_LINT_CACHE="$2" \
		go run "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$EXPECTED_VERSION" fmt --diff ./... 2>&1)
	SL_RC=$?
}

# ---------------------------------------------------------------------------
# A8: the Makefile resolves a cache dir INSIDE this worktree — derived from the
# TREE, and immune to a hostile inherited value.
#
# Three deliberate choices, each of which is what gives this leg power:
#
#  * It reads a `lint-cache-dir` introspection target instead of grepping the
#    Makefile text, so it measures the value a recipe would actually get. A
#    relative derivation is not merely wrong but fatal — the linter refuses to
#    start: "build cache is required, but could not be located:
#    GOLANGCI_LINT_CACHE is not an absolute path".
#  * It runs from /tmp, NOT from $REPO_ROOT. This script cd's to $REPO_ROOT at
#    the top, so a `$(CURDIR)`-based derivation would resolve to the wanted
#    string if this leg ran from $REPO_ROOT, and would pass while being wrong —
#    the bug being excluded is a cache keyed to where make was INVOKED rather
#    than to which tree it is linting. From /tmp that form yields
#    /tmp/.golangci-cache and fails, which is the point.
#    (`make -C` would not discriminate either: -C resets CURDIR too.)
#  * It runs with a HOSTILE inherited GOLANGCI_LINT_CACHE. Isolation here is a
#    safety property, not an operator preference: a stale value in the
#    environment must not be able to un-isolate every target in the worktree,
#    so the Makefile assigns with `:=` and this leg proves the assignment wins.
# ---------------------------------------------------------------------------
RESOLVED_CACHE=$(cd /tmp && env GOLANGCI_LINT_CACHE=/tmp/lint-pin-hostile-inherited \
	make -s -f "$MAKEFILE" lint-cache-dir 2>/dev/null | tail -1)
if [ "$RESOLVED_CACHE" = "$REPO_ROOT/.golangci-cache" ]; then
	pass "A8 Makefile resolves GOLANGCI_LINT_CACHE inside this worktree, from any cwd and over a hostile inherited value ($RESOLVED_CACHE)"
else
	fail "A8 Makefile does not resolve a per-worktree cache: got '$RESOLVED_CACHE', want '$REPO_ROOT/.golangci-cache'"
fi

# ---------------------------------------------------------------------------
# A8b: the value is EXPORTED, i.e. it reaches the recipe's child processes.
# Without `export` the variable is decoration: it resolves identically inside
# make and the linter keeps using the shared cache, defect fully live.
#
# Same probe as A8 but with GOLANGCI_LINT_CACHE UNSET, and the difference is the
# whole point. GNU make re-exports any variable that was present in the
# environment at startup, even after overriding it — so A8's hostile-env probe
# would pass with `export` deleted. Measured exactly that: with `export` removed,
# A8 PASSED and only this leg fired. The two legs are not redundant; each covers
# the other's blind spot.
#
# Deliberately NOT implemented as "find files newer than a marker under the cache
# dir after a make lint". Tried and rejected, measured: a warm cache legitimately
# writes nothing (Go's cache only refreshes entry mtimes about hourly), so that
# form went red on a correct tree. Value (A8) plus export (A8b) plus the scheme
# actually working (A10c) is the chain; an mtime probe is not part of it.
# ---------------------------------------------------------------------------
EXPORTED_CACHE=$(cd /tmp && env -u GOLANGCI_LINT_CACHE make -s -f "$MAKEFILE" lint-cache-dir 2>/dev/null | tail -1)
if [ "$EXPORTED_CACHE" = "$REPO_ROOT/.golangci-cache" ]; then
	pass "A8b GOLANGCI_LINT_CACHE is exported into recipe children ($EXPORTED_CACHE)"
else
	fail "A8b a recipe's child sees GOLANGCI_LINT_CACHE as '$EXPORTED_CACHE', want '$REPO_ROOT/.golangci-cache' — the Makefile is missing 'export', so the linter still uses the machine-wide shared cache"
fi

# ---------------------------------------------------------------------------
# A8c: an inherited $MAKEFILES cannot move the cache out of the worktree.
#
# A8's hostile value only poisons GOLANGCI_LINT_CACHE, and `:=` beats that. This
# is the OTHER inherited variable, and it wins against a naive derivation: GNU
# make prepends every file named in $MAKEFILES to MAKEFILE_LIST, so a
# `firstword` form resolves to that file's directory instead — measured, it
# printed /tmp/.../extra.mk's dir, i.e. outside the worktree, un-isolated again
# (two worktrees with the same $MAKEFILES re-share) and outside the free-reaping
# story too. The decoy is asserted to have been READ (A8c-a), so its failure to
# move the cache is evidence rather than a no-op.
# ---------------------------------------------------------------------------
MAKEFILES_DECOY_DIR="$DECOY_DIR/makefiles-decoy"
mkdir -p "$MAKEFILES_DECOY_DIR"
printf 'SPRAWL_MAKEFILES_DECOY := yes\ndecoy-probe:\n\t@printf %%s "$(SPRAWL_MAKEFILES_DECOY)"\n' \
	>"$MAKEFILES_DECOY_DIR/Makefile"
DECOY_READ=$(cd /tmp && env MAKEFILES="$MAKEFILES_DECOY_DIR/Makefile" make -s -f "$MAKEFILE" decoy-probe 2>/dev/null | tail -1)
POISONED_CACHE=$(cd /tmp && env MAKEFILES="$MAKEFILES_DECOY_DIR/Makefile" make -s -f "$MAKEFILE" lint-cache-dir 2>/dev/null | tail -1)
if [ "$DECOY_READ" != "yes" ]; then
	fail "A8c-a the \$MAKEFILES decoy was not read by make (got '$DECOY_READ'), so the result below is not evidence of anything"
elif [ "$POISONED_CACHE" = "$REPO_ROOT/.golangci-cache" ]; then
	pass "A8c an inherited \$MAKEFILES (read: proven) does not move the cache out of the worktree"
else
	fail "A8c an inherited \$MAKEFILES moved the cache to '$POISONED_CACHE' — worktrees sharing that env var share a cache again, and it escapes 'git worktree remove' reaping. Derive from \$(lastword \$(filter %Makefile,\$(MAKEFILE_LIST)))"
fi

# ---------------------------------------------------------------------------
# A9: the cache dir is ignored, and reaping therefore needs no new code.
#
# WHY THIS IS THE WHOLE REAPING STORY. Measured: `git worktree remove` succeeds
# and deletes the tree when it contains a populated GITIGNORED directory, with
# and without --force — ignored files do not count as untracked for that check.
# So an in-worktree cache is already reaped by every teardown path
# (agentops/gc.go DefaultRemoveWorktree, agentops/helpers.go RealWorktreeRemove,
# the supervisor's spawn rollback), and `worktree.teardown` stays empty. A9a is
# the precondition that makes that free: un-ignore the dir and `git worktree
# remove` starts refusing, which is a much worse failure than a stale cache.
# ---------------------------------------------------------------------------
#
# Asserted through `-v`, which names the SOURCE of the match, because a plain
# `-q` cannot tell the tracked `.gitignore` from this host's global
# core.excludesFile or `.git/info/exclude`. On a host whose global excludes
# happened to carry this pattern, deleting the `.gitignore` block would leave a
# `-q` form green while the shipped tree was unprotected — passing with the
# defect present.
A9A_SOURCE=$(git -C "$REPO_ROOT" check-ignore -v --no-index .golangci-cache/x 2>/dev/null)
case "$A9A_SOURCE" in
.gitignore:*)
	pass "A9a .golangci-cache/ is ignored by the tracked .gitignore (${A9A_SOURCE%%	*}), so git worktree remove still reaps the worktree"
	;;
"")
	fail "A9a .golangci-cache/ is NOT ignored at all — its files become untracked and 'git worktree remove' will refuse"
	;;
*)
	fail "A9a .golangci-cache/ is ignored, but by '${A9A_SOURCE%%	*}' rather than the tracked .gitignore — that protects this host only, not the shipped tree"
	;;
esac

# A9b0 — CONTROL for A9b, aimed in both directions, because A9b's own green is
# otherwise indistinguishable from a probe that can never fire:
#
#   * liveness — a path this repo definitely ignores must come back ignored.
#     `git check-ignore` exits 128 on error, and 128 is not 0, so a broken git
#     reads as "not ignored" i.e. as A9b PASSING. This is the leg that notices.
#   * firing — under a deliberately widened `.golangci*` pattern, supplied
#     through core.excludesFile so the tree is never edited, the over-match arm
#     of A9b MUST trip. Measured: rc=0 for .golangci.yml under that pattern.
# ---------------------------------------------------------------------------
WIDE_EXCLUDES="$DECOY_DIR/wide-excludes"
printf '%s\n' '.golangci*' >"$WIDE_EXCLUDES"
if ! git -C "$REPO_ROOT" check-ignore -q --no-index .sprawl/x 2>/dev/null; then
	fail "A9b0 liveness: git check-ignore does not report the known-ignored .sprawl/x as ignored — it is erroring, and A9b's green below would mean nothing"
elif ! git -C "$REPO_ROOT" -c core.excludesFile="$WIDE_EXCLUDES" check-ignore -q --no-index .golangci.yml 2>/dev/null; then
	fail "A9b0 firing: a deliberately widened '.golangci*' pattern did NOT make the probe report .golangci.yml as ignored, so A9b cannot detect an over-match"
else
	pass "A9b0 the over-match probe is live and fires under a widened '.golangci*' pattern (A9b below is aimed)"
fi

# A9b — NEGATIVE CONTROL on the pattern's precision, in both directions that a
# careless widening would break: a sibling FILE whose name merely starts with the
# same prefix, and the linter config itself.
A9B_OVERMATCH=""
for probe in .golangci-cache-notes.md .golangci.yml; do
	if git -C "$REPO_ROOT" check-ignore -q --no-index "$probe" 2>/dev/null; then
		A9B_OVERMATCH="$A9B_OVERMATCH $probe"
	fi
done
if [ -z "$A9B_OVERMATCH" ]; then
	pass "A9b the ignore pattern does not reach .golangci-cache-notes.md or .golangci.yml"
else
	fail "A9b the ignore pattern over-matches:$A9B_OVERMATCH — a widened glob would silently un-track the linter config"
fi

# A9c — nothing is tracked under the cache path, so the ignore cannot orphan a
# real file. Guarded by a positive control on the same code path first: if
# `git ls-files` cannot see a file we KNOW is tracked, an empty result below
# means nothing.
if [ -z "$(git -C "$REPO_ROOT" ls-files .golangci.yml)" ]; then
	fail "A9c control failed: git ls-files cannot see the tracked .golangci.yml, so an empty result proves nothing"
elif [ -z "$(git -C "$REPO_ROOT" ls-files .golangci-cache)" ]; then
	pass "A9c nothing is tracked under .golangci-cache (control: ls-files does see .golangci.yml)"
else
	fail "A9c files ARE tracked under .golangci-cache; the ignore rule would strand them: $(git -C "$REPO_ROOT" ls-files .golangci-cache | head -3 | tr '\n' ' ')"
fi

# A9d — the cache is excluded from the DOCKER BUILD CONTEXT too. `.dockerignore`
# is independent of `.gitignore`, and the hub Dockerfiles do `COPY . .` with the
# repo root as context: without this line, ~33M of cache entries carrying
# serialized issue text and absolute host paths get sent to the daemon and baked
# into a leakable build layer. A text assertion, because there is no cheap way to
# ask docker without a daemon — the failure it guards is a deleted line, which
# text catches.
if [ ! -f "$REPO_ROOT/.dockerignore" ]; then
	fail "A9d no .dockerignore at the repo root, so this leg cannot check the build context (did the file move?)"
elif grep -qxF '.golangci-cache/' "$REPO_ROOT/.dockerignore"; then
	pass "A9d .dockerignore excludes .golangci-cache/ from the docker build context"
else
	fail "A9d .dockerignore does NOT exclude .golangci-cache/ — the hub Dockerfiles COPY . . from the repo root, so the cache and its absolute host paths enter the build layer"
fi

# ---------------------------------------------------------------------------
# A10a: VACUITY GUARD. The fixture's violation is real and reportable with a
# fresh private cache. Every leg below greps for this exact finding, so if this
# leg is silent none of them measure anything.
# ---------------------------------------------------------------------------
STALE_WT="$SCRATCH_ROOT/stale/wt"
if ! mk_fixture "$STALE_WT"; then
	fail "A10a could not create the fixture module under $STALE_WT"
else
	scratch_lint "$STALE_WT" "$STALE_WT/.golangci-cache"
	# The anchored grep carries this claim on its own. SL_RC is only reported, not
	# asserted: these legs go through `go run`, which collapses every nonzero child
	# status to 1, so rc=1 is equally consistent with a build or config failure.
	if printf '%s\n' "$SL_OUT" | grep -q '^main\.go:3:6:'; then
		pass "A10a fixture violation is reported at main.go:3:6 with a cold private cache"
	else
		fail "A10a fixture violation was NOT reported (rc=$SL_RC) — every leg below is vacuous: $(printf '%s' "$SL_OUT" | tail -3)"
	fi
fi

# ---------------------------------------------------------------------------
# A10b: POSITIVE CONTROL, DEFECT PRESENT. Two sibling trees with identical
# content, ONE shared cache. wtB's own finding must come back attributed to
# wtA's path, and must NOT appear at any path inside wtB. This is the bug, and
# it is what gives A10c's silence its meaning.
#
# It asserts an upstream behaviour, so a future GOLANGCI_LINT_VERSION bump that
# fixes the poisoning upstream will turn this red. That is the correct place for
# that conversation: whoever bumps the pin must then decide whether A10c still
# has a control, not discover later that it never had one.
# ---------------------------------------------------------------------------
POISON_A="$SCRATCH_ROOT/poison/wtA"
POISON_B="$SCRATCH_ROOT/poison/wtB"
SHARED_CACHE="$SCRATCH_ROOT/poison/shared-cache"
if ! mk_fixture "$POISON_A" || ! mk_fixture "$POISON_B"; then
	fail "A10b could not create the sibling fixture modules under $SCRATCH_ROOT/poison"
else
	scratch_lint "$POISON_A" "$SHARED_CACHE"
	POISON_A_OUT="$SL_OUT"
	scratch_lint "$POISON_B" "$SHARED_CACHE"
	POISON_B_OUT="$SL_OUT"
	if printf '%s\n' "$POISON_B_OUT" | grep -q 'wtA/main\.go' &&
		! printf '%s\n' "$POISON_B_OUT" | grep -q '^main\.go:'; then
		pass "A10b shared cache DOES misattribute wtB's finding to wtA ($(printf '%s\n' "$POISON_B_OUT" | grep -o '[^ ]*wtA/main\.go:[0-9:]*' | head -1)) — A10c's control is aimed correctly"
	else
		fail "A10b could not reproduce cross-worktree misattribution with a shared cache under golangci-lint v$EXPECTED_VERSION, so A10c below proves NOTHING about isolation. If the pin moved, decide whether A10c still has a control — do not read this as the /false-red lock entry. wtA said: $(printf '%s' "$POISON_A_OUT" | tail -2) / wtB said: $(printf '%s' "$POISON_B_OUT" | tail -2)"
	fi
fi

# ---------------------------------------------------------------------------
# A10c: THE GUARD, DEFECT ABSENT. Same two trees, but each linted with a
# `.golangci-cache` inside itself — the exact scheme A8 pins in the Makefile.
# wtB must now report its OWN path and must not mention wtA at all.
# ---------------------------------------------------------------------------
ISO_A="$SCRATCH_ROOT/isolated/wtA"
ISO_B="$SCRATCH_ROOT/isolated/wtB"
if ! mk_fixture "$ISO_A" || ! mk_fixture "$ISO_B"; then
	fail "A10c could not create the sibling fixture modules under $SCRATCH_ROOT/isolated"
else
	scratch_lint "$ISO_A" "$ISO_A/.golangci-cache"
	ISO_A_OUT="$SL_OUT"
	scratch_lint "$ISO_B" "$ISO_B/.golangci-cache"
	ISO_B_OUT="$SL_OUT"
	# The wtA leg is asserted too, not assumed: if it errored it wrote no cache,
	# and this leg would quietly degenerate into a second copy of A10a while still
	# printing that isolation held.
	if ! printf '%s\n' "$ISO_A_OUT" | grep -q '^main\.go:3:6:'; then
		fail "A10c the wtA run did not report its own finding, so it populated no cache and this leg is a duplicate of A10a: $(printf '%s' "$ISO_A_OUT" | tail -3)"
	elif printf '%s\n' "$ISO_B_OUT" | grep -q '^main\.go:3:6:' &&
		! printf '%s\n' "$ISO_B_OUT" | grep -q 'wtA'; then
		pass "A10c per-worktree caches keep wtB's finding in wtB and never name wtA"
	else
		fail "A10c per-worktree caches did not stop the misattribution: $(printf '%s' "$ISO_B_OUT" | tail -3)"
	fi
fi

# ---------------------------------------------------------------------------
# A11a: STALENESS. A second run over the SAME tree with a WARM cache must still
# report the finding, not replay a cached clean. Same fixture and same cache dir
# as A10a, so this run is genuinely warm rather than nominally so.
#
# Its positive control is A10b: the same grep, on a subject where the cache does
# lie, where it fires.
# ---------------------------------------------------------------------------
if [ -d "$STALE_WT" ]; then
	# A11w — WARMTH CONTROL. Both legs below claim something about a warm cache.
	# If GOLANGCI_LINT_CACHE stopped being honoured, all three runs are cold and
	# BOTH still pass while measuring nothing about caching. This is the leg that
	# establishes the premise instead of asserting it in a comment.
	if [ -n "$(find "$STALE_WT/.golangci-cache" -type f 2>/dev/null | head -1)" ]; then
		pass "A11w A10a's run populated the fixture's private cache, so the runs below are genuinely warm"
	else
		fail "A11w the fixture's private cache is empty after A10a, so A11a/A11b below are cold runs and prove nothing about staleness"
	fi

	scratch_lint "$STALE_WT" "$STALE_WT/.golangci-cache"
	if printf '%s\n' "$SL_OUT" | grep -q '^main\.go:3:6:'; then
		pass "A11a second run over the same tree still reports the finding (no replayed clean)"
	else
		fail "A11a second run over an UNCHANGED tree lost the finding (rc=$SL_RC) — this is the false GREEN: $(printf '%s' "$SL_OUT" | tail -3)"
	fi

	# A11b — NEGATIVE CONTROL for A11a. Remove the violation, run a THIRD time
	# against the same warm cache: it must go quiet. If it still reports the
	# finding, the cache is pinning stale findings and A11a's green above was a
	# constant, not a measurement.
	mk_fixture_clean "$STALE_WT"
	scratch_lint "$STALE_WT" "$STALE_WT/.golangci-cache"
	if [ "$SL_RC" -eq 0 ] && printf '%s\n' "$SL_OUT" | grep -q '^0 issues\.$'; then
		pass "A11b removing the violation makes the warm-cache run go quiet (rc=0, '0 issues.')"
	else
		fail "A11b warm cache replayed a finding over code that no longer contains it (rc=$SL_RC) — A11a is not measuring the code: $(printf '%s' "$SL_OUT" | tail -3)"
	fi
else
	fail "A11w fixture missing at $STALE_WT; A10a must have failed to create it"
	fail "A11a fixture missing at $STALE_WT; A10a must have failed to create it"
	fail "A11b fixture missing at $STALE_WT; A10a must have failed to create it"
fi

# ---------------------------------------------------------------------------
# F (QUM-1287): fmt-check must not be able to pass without checking.
#
# `fmt-check`'s recipe was `@test -z "$$($(GOLANGCI_LINT) fmt --diff ./...)"`,
# which captures stdout and THROWS THE EXIT STATUS AWAY. So any tool failure
# that prints nothing to stdout was a green: measured on the parent commit,
#   make fmt-check GOLANGCI_LINT='false'                            -> rc=0
#   make fmt-check GOLANGCI_LINT='sh -c "echo boom >&2; exit 7" --'  -> rc=0, printed "boom"
# i.e. the repo's formatting gate could silently not happen. These two legs are
# the non-asserting fallback CLAUDE.md forbids, in the gate itself.
#
# They are also the cheapest assertions in this file: neither runs the real
# linter, and both are scoped to one small package for the same reason A2b/A2c
# are (see the LINT_SCOPE note at the top of this file).
# ---------------------------------------------------------------------------
F1_OUT=$(cd "$REPO_ROOT" && make fmt-check GOLANGCI_LINT='false' FMT_SCOPE="$LINT_SCOPE" 2>&1)
F1_RC=$?
if [ "$F1_RC" -ne 0 ]; then
	pass "F1 fmt-check FAILS when the formatter cannot run at all (rc=$F1_RC)"
else
	fail "F1 fmt-check returned 0 with a formatter that exits non-zero and prints nothing. The formatting check did not run and reported success: $(printf '%s' "$F1_OUT" | tail -2)"
fi

F2_OUT=$(cd "$REPO_ROOT" && make fmt-check GOLANGCI_LINT='sh -c "echo boom >&2; exit 7" --' FMT_SCOPE="$LINT_SCOPE" 2>&1)
F2_RC=$?
if [ "$F2_RC" -ne 0 ]; then
	pass "F2 fmt-check propagates a formatter failure that only wrote to stderr (rc=$F2_RC)"
else
	fail "F2 fmt-check returned 0 over a formatter that exited 7; the diagnostic went to stderr and was ignored"
fi

# F3 — NEGATIVE control for F1/F2, subject known clean: with the REAL pinned
# formatter, the same invocation must stay quiet. Without this, F1/F2 are equally
# consistent with a recipe that always fails.
F3_OUT=$(cd "$REPO_ROOT" && make fmt-check FMT_SCOPE="$LINT_SCOPE" 2>&1)
F3_RC=$?
if [ "$F3_RC" -eq 0 ]; then
	pass "F3 negative control stayed quiet: fmt-check passes on a correctly formatted subject"
else
	fail "F3 negative control FIRED: fmt-check returned $F3_RC on $LINT_SCOPE with the real formatter. Either that package genuinely needs 'make fmt', or the recipe now fails unconditionally and F1/F2 above prove nothing: $(printf '%s' "$F3_OUT" | tail -3)"
fi

# ---------------------------------------------------------------------------
# C1 (QUM-1287): the EXISTENCE half of the premise for keeping fmt-check as a
# second pass over the tree. The argument is written out once, in the Makefile's
# `fmt-check` comment; in short, `run` only loads files satisfying the host's
# build constraints while `fmt` walks the source, so collapsing the two would
# silently stop checking the files only `fmt` can see. C1 asserts those files
# still exist; C2 below asserts the behaviour that makes their existence matter.
# ---------------------------------------------------------------------------
IGNORED=$(cd "$REPO_ROOT" && go list -f '{{.IgnoredGoFiles}}' ./... 2>/dev/null | grep -v '^\[\]$')
if [ -n "$IGNORED" ]; then
	pass "C1 premise holds: build-constraint-excluded files exist ($(printf '%s\n' "$IGNORED" | wc -l) packages), so 'lint' cannot see everything 'fmt-check' does"
else
	fail "C1 the premise for keeping fmt-check as a separate pass has GONE: no build-constraint-excluded files remain, so 'golangci-lint run' may now cover the whole tree. This is not a broken tree — re-evaluate collapsing fmt-check into lint (QUM-1287)."
fi

# C2 (QUM-1287): the BEHAVIOURAL half of the premise, and the load-bearing one.
# C1 above proves the tag-gated files still exist; it does not prove that `fmt`
# inspects them while `run` does not. Without C2, a golangci-lint that stopped
# formatting build-constraint-excluded files would leave C1 green while
# `fmt-check` silently stopped adding any coverage and the recorded "cannot
# collapse" finding quietly became wrong. Replaces the earlier C1c, which only
# proved the `go list` probe was not a tautology — subsumed by this.
#
# Costs three linter invocations on a two-file scratch module, each with a
# private TMPDIR so the machine-wide lock is never touched (see the LINT_SCOPE
# note at the top of this file). Paid deliberately: it is the assertion that
# makes the decision self-maintaining.
C2_DIR="$SCRATCH_ROOT/premise"
mkdir -p "$C2_DIR"
printf 'module premise\n\ngo 1.26\n' >"$C2_DIR/go.mod"
cp "$GOLANGCI_CONFIG" "$C2_DIR/.golangci.yml"
printf 'package premise\n\nfunc Ordinary() {}\n' >"$C2_DIR/ordinary.go"
# Misformatted, and excluded from every build by an unsatisfiable constraint.
printf '//go:build never\n\npackage premise\n\nfunc  Gated( ) {\n\tx :=  1\n\t_ = x\n}\n' >"$C2_DIR/gated.go"

scratch_fmt "$C2_DIR" "$SCRATCH_ROOT/premise-cache"
if echo "$SL_OUT" | grep -q 'gated.go'; then
	pass "C2a 'fmt --diff' DOES inspect a build-constraint-excluded file (reported gated.go)"
else
	fail "C2a 'fmt --diff' did NOT report the misformatted, build-excluded gated.go. The premise for keeping fmt-check as a second pass is gone: it no longer covers anything lint misses. Re-evaluate collapsing it (QUM-1287). Output: $(printf '%s' "$SL_OUT" | tail -3)"
fi

scratch_lint "$C2_DIR" "$SCRATCH_ROOT/premise-cache"
if echo "$SL_OUT" | grep -q 'gated.go'; then
	fail "C2b 'run' now DOES see build-constraint-excluded files, so fmt-check may be redundant after all. This is good news, not a broken tree: re-evaluate collapsing fmt-check into lint (QUM-1287). Output: $(printf '%s' "$SL_OUT" | tail -3)"
else
	pass "C2b 'run' stays silent about the build-constraint-excluded file, so lint cannot cover what fmt-check does"
fi

# C2c — POSITIVE CONTROL for C2b, aimed at `run` REPORTING the file: the same
# misformatted content with the build constraint removed must be reported. If it
# is not, C2b's silence means "run does not report formatting at all" (or the
# fixture is broken) rather than "run cannot see gated files", and C2b would then
# be incapable of firing on the change it guards.
rm -f "$C2_DIR/gated.go"
printf 'package premise\n\nfunc  Ungated( ) {\n\tx :=  1\n\t_ = x\n}\n' >"$C2_DIR/ungated.go"
scratch_lint "$C2_DIR" "$SCRATCH_ROOT/premise-cache"
if echo "$SL_OUT" | grep -q 'ungated.go'; then
	pass "C2c control fired: 'run' DOES report the identical violation once the build constraint is gone, so C2b can fail"
else
	fail "C2c control did NOT fire: 'run' ignored the same misformatted content with no build constraint. C2b's silence proves nothing about build constraints. Output: $(printf '%s' "$SL_OUT" | tail -3)"
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
