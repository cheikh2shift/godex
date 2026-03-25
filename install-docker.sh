#!/bin/bash

set -e

GITHUB_REPO="https://raw.githubusercontent.com/cheikh2shift/godex/main"

echo "GoDex Docker Sandbox Installer"
echo "=============================="
echo ""

INSTALL_DIR="${1:-$HOME/godex-docker}"

echo "Installing GoDex Docker tools to: $INSTALL_DIR"
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
echo "Setup complete!"
echo ""
echo "To run GoDex in your project directory:"
echo ""
echo "  docker compose -f $INSTALL_DIR/docker-compose.yml up"
echo ""
echo "Or add this alias to your shell (~/.bashrc or ~/.zshrc):"
echo ""
echo "  alias godex='docker compose -f $INSTALL_DIR/docker-compose.yml up'"
echo ""
echo "This will:"
echo "  - Build the GoDex container with Python, Node.js, Go, Rust, and more"
echo "  - Mount your current directory as the working directory"
echo "  - Store config in a Docker volume"
echo ""
echo "First run will prompt you to configure your LLM provider."
echo ""
