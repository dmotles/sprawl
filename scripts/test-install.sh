#!/usr/bin/env bash
# test-install.sh - Unit tests for install.sh
#
# Sources install.sh with SPRAWL_INSTALL_TESTING=1 to load functions without
# executing the installer, then exercises each function in isolation.
#
# Usage:
#   bash scripts/test-install.sh
#
# NOTE: chmod +x this file after creation.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INSTALL_SCRIPT="$REPO_ROOT/install.sh"

# --- Test infrastructure ---

PASS_COUNT=0
FAIL_COUNT=0

pass() {
    PASS_COUNT=$((PASS_COUNT + 1))
    echo "  PASS: $1"
}

fail() {
    FAIL_COUNT=$((FAIL_COUNT + 1))
    echo "  FAIL: $1" >&2
}

assert_eq() {
    local expected="$1" actual="$2" msg="$3"
    if [ "$expected" = "$actual" ]; then
        pass "$msg"
    else
        fail "$msg (expected '$expected', got '$actual')"
    fi
}

assert_match() {
    local pattern="$1" actual="$2" msg="$3"
    if echo "$actual" | grep -qE "$pattern"; then
        pass "$msg"
    else
        fail "$msg (pattern '$pattern' not matched in '$actual')"
    fi
}

assert_contains() {
    local needle="$1" haystack="$2" msg="$3"
    if echo "$haystack" | grep -qF "$needle"; then
        pass "$msg"
    else
        fail "$msg ('$needle' not found in '$haystack')"
    fi
}

assert_exit_zero() {
    local msg="$1"
    shift
    # Run in subshell to prevent exit from killing the test runner
    if (SPRAWL_INSTALL_TESTING=1 && . "$INSTALL_SCRIPT" && "$@") >/dev/null 2>&1; then
        pass "$msg"
    else
        fail "$msg (command failed: $*)"
    fi
}

assert_exit_nonzero() {
    local msg="$1"
    shift
    # Run in subshell to prevent exit from killing the test runner
    if (SPRAWL_INSTALL_TESTING=1 && . "$INSTALL_SCRIPT" && "$@") >/dev/null 2>&1; then
        fail "$msg (command succeeded unexpectedly: $*)"
    else
        pass "$msg"
    fi
}

# --- Preflight ---

if [ ! -f "$INSTALL_SCRIPT" ]; then
    echo "FATAL: install.sh not found at $INSTALL_SCRIPT" >&2
    echo "This test file is meant to test install.sh once it exists." >&2
    exit 1
fi

# Source install.sh without executing — the guard checks SPRAWL_INSTALL_TESTING.
SPRAWL_INSTALL_TESTING=1
export SPRAWL_INSTALL_TESTING
# shellcheck source=/dev/null
. "$INSTALL_SCRIPT"

# --- Test 1: detect_os returns linux or darwin ---

echo "=== Test 1: detect_os ==="

DETECTED_OS=$(detect_os)
case "$DETECTED_OS" in
    linux|darwin)
        pass "detect_os returned '$DETECTED_OS'"
        ;;
    *)
        fail "detect_os returned unexpected value '$DETECTED_OS'"
        ;;
esac

echo ""

# --- Test 2: detect_arch returns x86_64 or arm64 ---

echo "=== Test 2: detect_arch ==="

DETECTED_ARCH=$(detect_arch)
case "$DETECTED_ARCH" in
    x86_64|arm64)
        pass "detect_arch returned '$DETECTED_ARCH'"
        ;;
    *)
        fail "detect_arch returned unexpected value '$DETECTED_ARCH'"
        ;;
esac

echo ""

# --- Test 3: Archive name construction (linux/x86_64) ---

echo "=== Test 3: Archive name — linux x86_64 ==="

ARCHIVE_NAME=$(build_archive_name "linux" "x86_64" "v0.1.0")
assert_eq "sprawl_0.1.0_linux_x86_64.tar.gz" "$ARCHIVE_NAME" \
    "build_archive_name linux x86_64 v0.1.0"

echo ""

# --- Test 4: Archive name construction (darwin/arm64) ---

echo "=== Test 4: Archive name — darwin arm64 ==="

ARCHIVE_NAME=$(build_archive_name "darwin" "arm64" "v0.2.1")
assert_eq "sprawl_0.2.1_darwin_arm64.tar.gz" "$ARCHIVE_NAME" \
    "build_archive_name darwin arm64 v0.2.1"

echo ""

# --- Test 5: Checksum file is always checksums.txt ---

echo "=== Test 5: Checksum filename ==="

# CHECKSUM_FILE is a constant defined in install.sh.
# If install.sh uses a function, call it; otherwise check the variable directly.
if type build_checksum_url >/dev/null 2>&1; then
    # If there is a helper, verify the filename portion ends with checksums.txt
    CHECKSUM_URL=$(build_checksum_url "v0.1.0")
    assert_match "checksums\\.txt$" "$CHECKSUM_URL" \
        "checksum URL ends with checksums.txt"
else
    # Fall back to checking the constant
    assert_eq "checksums.txt" "${CHECKSUM_FILE:-}" \
        "CHECKSUM_FILE constant is checksums.txt"
fi

echo ""

# --- Test 6: resolve_version with VERSION env var ---

echo "=== Test 6: resolve_version with VERSION set ==="

RESOLVED=$(VERSION="v1.2.3" resolve_version)
assert_eq "v1.2.3" "$RESOLVED" \
    "resolve_version returns VERSION env var when set"

echo ""

# --- Test 7: verify_checksum succeeds with correct checksum ---

echo "=== Test 7: verify_checksum — correct checksum ==="

TMPDIR_TEST=$(mktemp -d "${TMPDIR:-/tmp}/install-test-XXXXXX")
trap 'rm -rf "$TMPDIR_TEST"' EXIT

# Create a test archive file with known content
echo "test archive content" > "$TMPDIR_TEST/test.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
    EXPECTED_SHA=$(sha256sum "$TMPDIR_TEST/test.tar.gz" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
    EXPECTED_SHA=$(shasum -a 256 "$TMPDIR_TEST/test.tar.gz" | awk '{print $1}')
else
    echo "FATAL: no sha256 tool available for tests" >&2; exit 1
fi

# Create a checksums file in the expected format (sha256sum output format)
echo "$EXPECTED_SHA  test.tar.gz" > "$TMPDIR_TEST/checksums.txt"

assert_exit_zero "verify_checksum succeeds with correct checksum" \
    verify_checksum "$TMPDIR_TEST/test.tar.gz" "$TMPDIR_TEST/checksums.txt"

echo ""

# --- Test 8: verify_checksum fails with wrong checksum ---

echo "=== Test 8: verify_checksum — wrong checksum ==="

echo "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  test.tar.gz" \
    > "$TMPDIR_TEST/bad-checksums.txt"

assert_exit_nonzero "verify_checksum fails with wrong checksum" \
    verify_checksum "$TMPDIR_TEST/test.tar.gz" "$TMPDIR_TEST/bad-checksums.txt"

echo ""

# --- Test 9: DEFAULT_INSTALL_DIR contains HOME ---

echo "=== Test 9: DEFAULT_INSTALL_DIR contains HOME ==="

assert_contains ".local/bin" "$DEFAULT_INSTALL_DIR" \
    "DEFAULT_INSTALL_DIR contains .local/bin"

echo ""

# --- Test 10: Archive name with version missing v prefix ---

echo "=== Test 10: Archive name — version without v prefix ==="

ARCHIVE_NAME=$(build_archive_name "linux" "arm64" "0.3.0")
assert_eq "sprawl_0.3.0_linux_arm64.tar.gz" "$ARCHIVE_NAME" \
    "build_archive_name handles version without v prefix"

echo ""

# --- Test 11: Required functions exist ---

echo "=== Test 11: Required functions exist ==="

for fn in detect_os detect_arch build_archive_name resolve_version verify_checksum download maybe_sudo execute; do
    if type "$fn" >/dev/null 2>&1; then
        pass "function $fn exists"
    else
        fail "function $fn is missing"
    fi
done

echo ""

# --- Test 12: install.sh is valid POSIX sh ---

echo "=== Test 12: POSIX sh validity ==="

# First try shellcheck if available, then fall back to sh -n
if command -v shellcheck >/dev/null 2>&1; then
    if shellcheck -s sh "$INSTALL_SCRIPT" >/dev/null 2>&1; then
        pass "install.sh passes shellcheck (POSIX sh)"
    else
        fail "install.sh has shellcheck warnings"
        shellcheck -s sh "$INSTALL_SCRIPT" 2>&1 | head -20 >&2
    fi
else
    # Fall back to syntax check
    if sh -n "$INSTALL_SCRIPT" 2>/dev/null; then
        pass "install.sh passes sh -n syntax check"
    else
        fail "install.sh has syntax errors"
    fi
fi

echo ""

# --- Summary ---

echo "==============================="
echo "  Results: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "==============================="

if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
fi
