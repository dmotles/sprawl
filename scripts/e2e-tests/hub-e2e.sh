#!/usr/bin/env bash
# scripts/e2e-tests/hub-e2e.sh — QUM-911 Hub Phase 1 capstone e2e row.
#
# Thin wrapper (KEEP IT DUMB, per e2e-matrix.sh) around the Go capstone test
# behind the `hub_e2e` build tag. That test stands up a REAL local hubd process
# (built from cmd/hubd, in-memory store, no cloud), mints a host bearer token
# over the real /login -> CreateHostToken browser flow, ships a deterministic
# seq'd wire log through the REAL host tailer (internal/hubtail), and subscribes
# with the REAL generated Connect client as the browser stand-in. It proves
# live-tail, the running/idle pill data source, and zero-gap/zero-dupe reconnect
# across a subscriber network blip and a hubd restart.
#
# Needs only the Go toolchain — no claude, tmux, or jq — so it runs unguarded in
# the matrix. See docs/design/hub/13-p1-local-e2e-and-manual-walkthrough.md.

test_metadata() {
    echo ""
}

test_run() {
    echo "== hub-e2e: local hubd live-tail + reconnect zero-gap proof =="
    if go test -tags hub_e2e -count=1 -v "$REPO_ROOT/internal/hub/e2e/"; then
        pass "hub-e2e capstone (live-tail + blip + hubd-restart, zero-gap/dupe)"
    else
        fail "hub-e2e capstone go test failed"
    fi
    e2e_print_results
}
