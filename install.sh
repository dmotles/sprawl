#!/bin/sh
# Sprawl installer — downloads and installs the sprawl binary.
# Usage: curl -fsSL https://raw.githubusercontent.com/dmotles/sprawl/main/install.sh | sh
# Or:    curl -fsSL https://raw.githubusercontent.com/dmotles/sprawl/main/install.sh | sh -s -- --version v0.1.0

set -eu

GITHUB_REPO="dmotles/sprawl"
BINARY_NAME="sprawl"
DEFAULT_INSTALL_DIR="$HOME/.local/bin"
CHECKSUM_FILE="checksums.txt"

# ---- Platform detection ----

detect_os() {
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$os" in
    linux) echo "linux" ;;
    darwin) echo "darwin" ;;
    *) echo "Error: unsupported OS: $os" >&2; exit 1 ;;
  esac
}

detect_arch() {
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) echo "x86_64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) echo "Error: unsupported architecture: $arch" >&2; exit 1 ;;
  esac
}

# ---- Helpers ----

build_archive_name() {
  _os="$1" _arch="$2" _tag="$3"
  _version="${_tag#v}"
  echo "${BINARY_NAME}_${_version}_${_os}_${_arch}.tar.gz"
}

# ---- Download helpers ----

download() {
  url="$1" dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$dest" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$dest" "$url"
  else
    echo "Error: curl or wget is required" >&2; exit 1
  fi
}

# ---- Version resolution ----

resolve_version() {
  if [ -n "${VERSION:-}" ]; then
    echo "$VERSION"
    return
  fi
  tag=""
  if command -v curl >/dev/null 2>&1; then
    tag="$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" \
      | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')"
  elif command -v wget >/dev/null 2>&1; then
    tag="$(wget -qO- "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" \
      | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')"
  fi
  if [ -z "$tag" ]; then
    echo "Error: could not resolve latest version. Set VERSION=vX.Y.Z and retry." >&2
    exit 1
  fi
  echo "$tag"
}

# ---- Checksum verification ----

verify_checksum() {
  archive="$1" checksums="$2"
  archive_name="$(basename "$archive")"
  expected="$(grep "${archive_name}\$" "$checksums" | head -1 | cut -d' ' -f1)"
  if [ -z "$expected" ]; then
    echo "Error: archive not found in checksums file" >&2; exit 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$archive" | cut -d' ' -f1)"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$archive" | cut -d' ' -f1)"
  else
    echo "Warning: no sha256 tool found, skipping verification" >&2
    return 0
  fi
  if [ "$expected" != "$actual" ]; then
    echo "Error: checksum mismatch for $(basename "$archive")" >&2
    echo "  expected: $expected" >&2
    echo "  actual:   $actual" >&2
    exit 1
  fi
}

# ---- Sudo handling ----

maybe_sudo() {
  if [ -w "$INSTALL_DIR" ]; then
    "$@"
  elif command -v sudo >/dev/null 2>&1; then
    echo "Elevated permissions required to install to $INSTALL_DIR"
    sudo "$@"
  else
    echo "Error: $INSTALL_DIR is not writable and sudo is not available" >&2
    exit 1
  fi
}

# ---- Main install logic (wrapped to prevent partial-download execution) ----

execute() {
  VERSION="" INSTALL_DIR="$DEFAULT_INSTALL_DIR"
  while [ $# -gt 0 ]; do
    case "$1" in
      --version)
        [ $# -ge 2 ] || { echo "Error: --version requires a value" >&2; exit 1; }
        VERSION="$2"; shift 2 ;;
      --install-dir)
        [ $# -ge 2 ] || { echo "Error: --install-dir requires a value" >&2; exit 1; }
        INSTALL_DIR="$2"; shift 2 ;;
      *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
  done

  # Check prerequisites
  if ! command -v tar >/dev/null 2>&1; then
    echo "Error: 'tar' is required but not found" >&2; exit 1
  fi
  if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
    echo "Error: 'curl' or 'wget' is required but neither was found" >&2; exit 1
  fi

  OS="$(detect_os)"
  ARCH="$(detect_arch)"
  TAG="$(resolve_version)"
  ARCHIVE="$(build_archive_name "$OS" "$ARCH" "$TAG")"
  BASE_URL="https://github.com/${GITHUB_REPO}/releases/download/${TAG}"

  echo "Installing ${BINARY_NAME} ${TAG} (${OS}/${ARCH})..."

  WORK_DIR="$(mktemp -d)"
  trap '[ -n "$WORK_DIR" ] && rm -rf "$WORK_DIR"' EXIT

  download "${BASE_URL}/${ARCHIVE}" "${WORK_DIR}/${ARCHIVE}"
  download "${BASE_URL}/${CHECKSUM_FILE}" "${WORK_DIR}/${CHECKSUM_FILE}"
  verify_checksum "${WORK_DIR}/${ARCHIVE}" "${WORK_DIR}/${CHECKSUM_FILE}"

  tar -xzf "${WORK_DIR}/${ARCHIVE}" -C "$WORK_DIR"

  if [ ! -d "$INSTALL_DIR" ]; then
    maybe_sudo mkdir -p "$INSTALL_DIR"
  fi
  maybe_sudo install -m 755 "${WORK_DIR}/${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"

  echo "Installed ${BINARY_NAME} to ${INSTALL_DIR}/${BINARY_NAME}"

  # Check if install dir is in PATH
  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) ;;
    *)
      echo ""
      echo "Warning: ${INSTALL_DIR} is not in your PATH."
      shell_name="$(basename "${SHELL:-/bin/sh}")"
      case "$shell_name" in
        zsh)  rc_file="~/.zshrc" ;;
        bash) rc_file="~/.bashrc" ;;
        *)    rc_file="your shell's rc file" ;;
      esac
      echo "Add it by running:"
      echo "  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ${rc_file}"
      echo ""
      ;;
  esac

  echo "Run '${BINARY_NAME} init' to get started."
}

if [ "${SPRAWL_INSTALL_TESTING:-}" != "1" ]; then
  execute "$@"
fi
