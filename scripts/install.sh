#!/usr/bin/env bash
set -euo pipefail

REPO="carsteneu/yesmem"
INSTALL_DIR="${YESMEM_INSTALL_DIR:-$HOME/.local/bin}"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

info()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
error() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# Detect OS
case "$(uname -s)" in
    Linux*)  OS=linux ;;
    Darwin*) OS=darwin ;;
    *)       error "Unsupported OS: $(uname -s). YesMem supports Linux and macOS." ;;
esac

# Detect architecture
case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *)             error "Unsupported architecture: $(uname -m). YesMem supports amd64 and arm64." ;;
esac

info "Detected ${OS}/${ARCH}"

# Get latest version from GitHub API
info "Checking latest release..."
LATEST=$(curl -fsSL --connect-timeout 10 --max-time 20 "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
[ -z "$LATEST" ] && error "Could not determine latest version. Check https://github.com/${REPO}/releases"
VERSION="${LATEST#v}"
info "Latest version: ${LATEST}"

# Check if already installed and up to date
if command -v yesmem &>/dev/null; then
    CURRENT=$(yesmem version 2>/dev/null || echo "unknown")
    if [ "$CURRENT" = "$VERSION" ] || [ "$CURRENT" = "$LATEST" ]; then
        info "YesMem ${VERSION} is already installed and up to date."
        exit 0
    fi
    info "Upgrading from ${CURRENT} to ${VERSION}"
fi

# Download binary and checksums
ASSET="yesmem_${VERSION}_${OS}_${ARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${LATEST}/${ASSET}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${LATEST}/checksums.txt"

info "Downloading ${ASSET}..."
curl -fsSL --connect-timeout 10 --max-time 120 -o "${TMP_DIR}/${ASSET}" "$DOWNLOAD_URL" || error "Download failed. Check https://github.com/${REPO}/releases/tag/${LATEST}"
curl -fsSL --connect-timeout 10 --max-time 20 -o "${TMP_DIR}/checksums.txt" "$CHECKSUMS_URL" || error "Checksums download failed."

# Verify checksum
info "Verifying checksum..."
cd "$TMP_DIR"
EXPECTED=$(grep "${ASSET}" checksums.txt | awk '{print $1}')
[ -z "$EXPECTED" ] && error "No checksum found for ${ASSET}"
if command -v sha256sum &>/dev/null; then
    ACTUAL=$(sha256sum "${ASSET}" | awk '{print $1}')
elif command -v shasum &>/dev/null; then
    ACTUAL=$(shasum -a 256 "${ASSET}" | awk '{print $1}')
else
    error "No sha256sum or shasum found. Cannot verify download."
fi
[ "$EXPECTED" != "$ACTUAL" ] && error "Checksum mismatch! Expected ${EXPECTED}, got ${ACTUAL}"
info "Checksum verified."

# Extract and install
info "Installing to ${INSTALL_DIR}..."
tar -xzf "${ASSET}"
mkdir -p "$INSTALL_DIR"
mv yesmem "$INSTALL_DIR/yesmem"
chmod +x "$INSTALL_DIR/yesmem"

# Verify installation
if ! "${INSTALL_DIR}/yesmem" version &>/dev/null; then
    error "Installation verification failed."
fi

INSTALLED_VERSION=$("${INSTALL_DIR}/yesmem" version 2>/dev/null || echo "$VERSION")
info "YesMem ${INSTALLED_VERSION} installed successfully."

# Check PATH
if ! echo "$PATH" | tr ':' '\n' | grep -q "^${INSTALL_DIR}$"; then
    printf '\n\033[1;33mNote:\033[0m %s is not in your PATH.\n' "$INSTALL_DIR"
    printf 'Add it to your shell profile:\n'
    printf '  export PATH="%s:$PATH"\n\n' "$INSTALL_DIR"
fi

printf '\nNext step:\n'
printf '  yesmem setup\n\n'
