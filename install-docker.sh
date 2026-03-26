#!/bin/bash

set -e

GITHUB_REPO="https://raw.githubusercontent.com/cheikh2shift/godex/main"
IMAGE_NAME="godex-sandbox:latest"

echo "GoDex Docker Sandbox Installer"
echo "=============================="
echo ""

INSTALL_DIR="${1:-$HOME/godex-docker}"

echo "Installing GoDex Docker tools to: $INSTALL_DIR"
echo ""

mkdir -p "$INSTALL_DIR"
cd "$INSTALL_DIR"

echo "Downloading docker-compose.yml..."
curl -fsSL "${GITHUB_REPO}/docker-compose.yml" -o docker-compose.yml

echo "Downloading nginx.conf..."
curl -fsSL "${GITHUB_REPO}/nginx.conf" -o nginx.conf

echo "Downloading Dockerfile..."
curl -fsSL "${GITHUB_REPO}/Dockerfile" -o Dockerfile

echo ""
echo "Building Docker image (this may take a few minutes)..."
docker build -t "${IMAGE_NAME}" -f "${INSTALL_DIR}/Dockerfile" .

echo ""
echo "Setup complete!"
echo ""
echo "To run GoDex in your project directory:"
echo ""
echo "  docker compose -f ${INSTALL_DIR}/docker-compose.yml up"
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
