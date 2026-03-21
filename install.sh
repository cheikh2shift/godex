:"#!/bin/sh

set -e

REPO="cheikh2shift/godex"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
BINARY_NAME="godex"

# Detect OS and architecture
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
    x86_64)
        ARCH="amd64"
        ;;
    aarch64|arm64)
        ARCH="arm64"
        ;;
    *)
        echo "Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

# Map OS names
case "$OS" in
    darwin)
        OS="darwin"
        ;;
    linux)
        OS="linux"
        ;;
    *)
        echo "Unsupported OS: $OS"
        exit 1
        ;;
esac

# If running in CI or explicit override, use the target
if [ -n "$TARGET_OS" ]; then
    OS="$TARGET_OS"
fi
if [ -n "$TARGET_ARCH" ]; then
    ARCH="$TARGET_ARCH"
fi

FILENAME="${BINARY_NAME}-${OS}-${ARCH}"
SHA_FILENAME="${FILENAME}.sha256"
if [ "$OS" = "windows" ]; then
    FILENAME="${FILENAME}.exe"
    SHA_FILENAME="${FILENAME}.sha256"
fi

# Get latest tag
LATEST_TAG=$(curl -sL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed 's/.*"v\([^"]*\)".*/\1/')
if [ -z "$LATEST_TAG" ]; then
    echo "Failed to get latest release version"
    exit 1
fi

URL="https://github.com/${REPO}/releases/download/v${LATEST_TAG}/${FILENAME}"
SHA_URL="https://github.com/${REPO}/releases/download/v${LATEST_TAG}/${SHA_FILENAME}"

echo "Downloading ${FILENAME}..."
curl -sSL -o "${BINARY_NAME}" "$URL"

echo "Downloading SHA256 checksum..."
curl -sSL -o "${BINARY_NAME}.sha256" "$SHA_URL"

echo "Verifying checksum..."
if command -v sha256sum >/dev/null 2>&1; then
    echo "$(cat ${BINARY_NAME}.sha256)  ${BINARY_NAME}" | sha256sum -c -
elif command -v shasum >/dev/null 2>&1; then
    echo "$(cat ${BINARY_NAME}.sha256)  ${BINARY_NAME}" | shasum -a 256 -c -
else
    echo "Warning: Neither sha256sum nor shasum found. Skipping checksum verification."
    echo "Consider installing coreutils (Linux) or using Homebrew (macOS) for security."
fi

# Clean up checksum file
rm -f "${BINARY_NAME}.sha256"

chmod +x "${BINARY_NAME}"

# Create install directory if it doesn't exist
mkdir -p "$INSTALL_DIR"

# Move to install directory
if [ -w "$INSTALL_DIR" ]; then
    mv "${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"
    echo "Installed to ${INSTALL_DIR}/${BINARY_NAME}"
else
    echo "Cannot write to ${INSTALL_DIR}, installed to current directory"
    echo "Move it manually: mv ${BINARY_NAME} ${INSTALL_DIR}/"
fi

# Setup shell completion
GODEX_BASH_COMPLETION='#!/bin/bash
# shellcheck disable=SC2034
PROG="godex"

godex_get_providers() {
    local config_path="${HOME}/.godex/providers.yaml"
    if [[ -f "$config_path" ]]; then
        grep -E "^    - name:" "$config_path" | sed "s/.*- name://" | tr -d " \"
    fi
}

godex_get_flags_with_desc() {
    echo "--config:provider configuration YAML"
    echo "--provider:provider name to use"
    echo "--wizard:launch provider configuration wizard"
    echo "--prompt:run a single prompt non-interactively"
    echo "--auto-confirm:auto-run suggested commands"
    echo "--version:print version information"
    echo "--debug:enable debug mode"
    echo "--completion:generate shell completion"
}

_cgodex_completion() {
    local cur prev words cword
    _init_completion || return
    
    ca...