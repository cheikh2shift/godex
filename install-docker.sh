#!/bin/bash

set -e

GITHUB_REPO="https://raw.githubusercontent.com/cheikh2shift/godex/main"
GITHUB_API="https://api.github.com/repos/cheikh2shift/godex"
IMAGE_NAME="godex-sandbox:latest"

detect_os() {
    case "$(uname -s)" in
        Linux*)  echo "linux" ;;
        Darwin*) echo "darwin" ;;
        *)       echo "linux" ;;
    esac
}

detect_arch() {
    case "$(uname -m)" in
        x86_64)  echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *)       echo "amd64" ;;
    esac
}

echo "GoDex Docker Sandbox Installer"
echo "=============================="
echo ""

INSTALL_DIR="${1:-$HOME/godex-docker}"
OS=$(detect_os)
ARCH=$(detect_arch)

echo "Installing GoDex Docker tools to: $INSTALL_DIR"
echo "Detected platform: $OS-$ARCH"
echo ""

mkdir -p "$INSTALL_DIR"
cd "$INSTALL_DIR"

echo "Downloading Dockerfile..."
curl -fsSL "${GITHUB_REPO}/Dockerfile" -o Dockerfile

echo "Downloading docker-compose.yml..."
curl -fsSL "${GITHUB_REPO}/docker-compose.yml" -o docker-compose.yml

echo "Downloading nginx.conf..."
curl -fsSL "${GITHUB_REPO}/nginx.conf" -o nginx.conf

echo ""
echo "Downloading latest godex binary..."
LATEST_VERSION=$(curl -fsSL "${GITHUB_API}/releases/latest" | grep '"tag_name"' | sed 's/.*v\([0-9.]*\).*/\1/')
if [ -z "$LATEST_VERSION" ]; then
    echo "Warning: Could not fetch latest version, using 'latest' tag"
    LATEST_VERSION="latest"
fi

BINARY_NAME="godex-${OS}-${ARCH}"
if [ "$OS" = "windows" ]; then
    BINARY_NAME="${BINARY_NAME}.exe"
fi

BINARY_URL="https://github.com/cheikh2shift/godex/releases/download/v${LATEST_VERSION}/${BINARY_NAME}"

echo "Downloading ${BINARY_NAME} v${LATEST_VERSION}..."
curl -fsSL "$BINARY_URL" -o godex || {
    echo "Warning: Failed to download binary, will build from source"
}

if [ ! -f godex ] || [ ! -x godex ]; then
    echo ""
    echo "Building godex from source..."
    if command -v go &> /dev/null; then
        GOOS=$OS GOARCH=$ARCH CGO_ENABLED=0 go build -ldflags="-s -w" -o godex ./cmd/godex
    else
        echo "Error: Go is not installed and binary download failed"
        exit 1
    fi
fi

chmod +x godex

echo ""
echo "Building Docker image (this may take a few minutes)..."
docker build -t "${IMAGE_NAME}" .

echo ""
echo "Setup complete!"
echo ""
echo "To run GoDex in your project directory:"
echo ""
echo "  # Add this alias to ~/.bashrc or ~/.zshrc:"
echo "  alias godex='docker compose -f ${INSTALL_DIR}/docker-compose.yml up'"
echo ""
echo "  # Then run from any directory:"
echo "  godex"
echo ""
echo "This will:"
echo "  - Mount your current directory as the working directory"
echo "  - Store config in a Docker volume"
echo ""
echo "First run will prompt you to configure your LLM provider."
echo ""
