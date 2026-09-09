#!/usr/bin/env bash
# scripts/e2e-tests/store-pg-integration.sh — Postgres integration row for every
# suite behind the `store_pg` build tag.
#
# Three packages, listed in STORE_PG_PACKAGES below:
#   internal/store  — QUM-1249 (M1a) event log
#   internal/engine — QUM-1252 (M3a) workflow engine step runner
#   internal/uiapi  — QUM-1349 read-API pool bounds (statement_timeout, MaxConns)
# Add a package to that array (and bump MIN_ASSERTIONS) when a new store_pg
# suite lands; there is deliberately one row for the tag, not one per package,
# because they share a single Postgres container configuration.
#
# Thin wrapper (KEEP IT DUMB, per e2e-matrix.sh) around those suites. They stand
# up a REAL Postgres 16 container via testcontainers, migrate the Appendix A M1a
# schema into a per-test isolated schema, and assert: the schema shape, the
# append-only GRANTs (AC3), the pinned-schema payload rejection (AC2), the
# open_contracts drop/rebuild/anti-join equality (AC4), and — M3a — that a step's
# side effect and its checkpoint event share ONE commit, via
# pg_xact_commit_timestamp() (which is why the container runs with
# track_commit_timestamp=on).
#
# WHY THIS WRAPPER EXISTS AT ALL — the 77 obligation.
# A Go test that cannot reach Docker calls t.Skip, and a skipped Go test exits
# 0. So `go test` alone reports a green package on a host with no Docker while
# asserting nothing, which is exactly the vacuous-green class the repo's
# testing-practices forbids. This row converts that into the autotools SKIP
# convention: exit 77, never 0, when Docker/PG is unavailable.
#
# Two gates, deliberately both:
#   1. Pre-flight `docker info` BEFORE go test — the fast, honest path.
#   2. SPRAWL_STORE_PG_REQUIRED=1 exported into the go test run, which turns
#      every in-Go skip into a hard failure. Without it, a container that fails
#      to start for a reason OTHER than "no Docker" (bad image tag, OOM kill, a
#      wait strategy that never matches) would be folded into a silent skip and
#      the row would pass having measured nothing. Gate 1 answers "is Docker
#      there"; gate 2 answers "did the run that Docker permitted actually run".
#
# Needs only the Go toolchain and Docker — no claude, tmux, or jq.

# QUM-1029: the number of assertions a COMPLETE, PASSING run of this row makes.
# One symmetric gate per store_pg package. The Go suites' own assertions are not
# counted here: this row cannot see them, and a floor must never be derived from
# a number the harness did not observe.
#
# The gate is per-package rather than one `go test` over all of them, so a
# package that vanishes from the loop drops the count below the floor instead of
# being absorbed by the others' passes. Bump this when adding a package below.
MIN_ASSERTIONS=3

# Packages carrying store_pg-tagged suites. Each runs as its own test binary and
# therefore stands up its own container.
STORE_PG_PACKAGES=(internal/store internal/engine internal/uiapi)

test_metadata() {
    echo ""
}

test_run() {
    echo "== store-pg-integration: event-log schema, append-only grants, appender, engine step atomicity, read-API pool bounds =="

    if ! command -v docker >/dev/null 2>&1; then
        e2e_skip_row "docker not found on PATH — the event-log integration suite needs a Postgres container"
        return
    fi
    if ! docker info >/dev/null 2>&1; then
        e2e_skip_row "docker is installed but the daemon is unreachable — cannot start the Postgres container"
        return
    fi

    # SPRAWL_STORE_PG_REQUIRED=1: any in-Go skip is now a failure, so this row
    # can never report a pass over a suite that skipped itself.
    for pkg in "${STORE_PG_PACKAGES[@]}"; do
        if SPRAWL_STORE_PG_REQUIRED=1 go test -tags store_pg -count=1 -v "$REPO_ROOT/$pkg/"; then
            pass "store_pg integration suite: $pkg"
        else
            fail "store_pg integration suite failed: $pkg"
        fi
    done
    e2e_print_results
}
