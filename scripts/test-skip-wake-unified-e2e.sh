#!/usr/bin/env bash
# test-skip-wake-unified-e2e.sh — End-to-end gate for QUM-438.
#
# Asserts that under the unified runtime path, messages.Send routed via the
# in-process supervisor RecipientResolver SKIPS the legacy `.wake` sentinel
# file (because UnifiedRuntime drives wake/interrupt in-memory), AND that
# the message still lands in the recipient's maildir for next-turn pickup.
# Also asserts the legacy-runtime path still writes the wake file (no
# regression).
#
# Why this is a Go-driven test rather than a pure bash sandbox:
# The resolver is wired ONLY inside the in-process `sprawl enter`
# supervisor (cmd/enter.go). Driving the wake-file emission from a real
# bash sandbox would require automating a live `claude` TTY to invoke the
# `mcp__sprawl__send_async` tool — too brittle to script reliably. The
# Go integration test uses the same production code paths (real
# *RuntimeRegistry, real messages.Send, real on-disk sprawl root) and
# asserts the same end-state (wake-file presence/absence). It runs fast
# and deterministically.
#
# Usage:
#   bash scripts/test-skip-wake-unified-e2e.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

echo "QUM-438 e2e: running supervisor recipient-resolver integration tests..."
go test ./internal/supervisor/... -run 'E2E_QUM438' -count=1 -v

echo
echo "QUM-438 e2e: running messages-package resolver wiring tests..."
go test ./internal/messages/... -run 'RecipientResolver|WithoutWakeFileBeatsResolver|NoRecipientResolver|SetRecipientResolver' -count=1 -v

echo
echo "QUM-438 e2e: PASS"
