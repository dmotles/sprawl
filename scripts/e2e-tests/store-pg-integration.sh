#!/usr/bin/env bash
# scripts/e2e-tests/store-pg-integration.sh — QUM-1249 (M1a) event-log store
# Postgres integration row.
#
# Thin wrapper (KEEP IT DUMB, per e2e-matrix.sh) around the Go suite behind the
# `store_pg` build tag. That suite stands up a REAL Postgres 16 container via
# testcontainers, migrates the Appendix A M1a schema into a per-test isolated
# schema, and asserts the schema shape, the append-only GRANTs (AC3), the
# pinned-schema payload rejection (AC2), and the open_contracts
# drop/rebuild/anti-join equality (AC4).
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
# One symmetric gate on the go test exit status. The Go suite's own assertions
# are not counted here: this row cannot see them, and a floor must never be
# derived from a number the harness did not observe.
MIN_ASSERTIONS=1

test_metadata() {
    echo ""
}

test_run() {
    echo "== store-pg-integration: event-log schema, append-only grants, appender =="

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
    if SPRAWL_STORE_PG_REQUIRED=1 go test -tags store_pg -count=1 -v "$REPO_ROOT/internal/store/"; then
        pass "store_pg integration suite (Appendix A schema, append-only grants, appender txn)"
    else
        fail "store_pg integration suite failed"
    fi
    e2e_print_results
}
