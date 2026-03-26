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
DOLLAR='$'
printf '  UID=%s{UID:-%s(id -u)} GID=%s{GID:-%s(id -g)} WORKSPACE_DIR="%s{PWD}" docker compose -f %s{HOME}/godex-docker/docker-compose.yml up -d && docker attach godex && docker compose -f %s{HOME}/godex-docker/docker-compose.yml down\n' "$DOLLAR" "$DOLLAR" "$DOLLAR" "$DOLLAR" "$DOLLAR" "$DOLLAR" "$DOLLAR"
echo ""
echo "  # Add this alias to ~/.bashrc or ~/.zshrc:"
printf '  alias godex='\''UID=%s{UID:-%s(id -u)} GID=%s{GID:-%s(id -g)} WORKSPACE_DIR="%s{PWD}" docker compose -f %s{HOME}/godex-docker/docker-compose.yml up -d && docker attach godex && docker compose -f %s{HOME}/godex-docker/docker-compose.yml down'\''\n' "$DOLLAR" "$DOLLAR" "$DOLLAR" "$DOLLAR" "$DOLLAR" "$DOLLAR" "$DOLLAR"
echo ""
echo "  # Or add this function for clearer behavior:"
echo "  godex() {"
printf '    UID=%s{UID:-%s(id -u)} GID=%s{GID:-%s(id -g)} WORKSPACE_DIR="%s{PWD}" docker compose -f %s{HOME}/godex-docker/docker-compose.yml up -d \\\n' "$DOLLAR" "$DOLLAR" "$DOLLAR" "$DOLLAR" "$DOLLAR" "$DOLLAR"
echo "      && docker attach godex \\"
echo "      && docker compose -f ${INSTALL_DIR}/docker-compose.yml down"
echo "  }"
echo ""
echo "  # Then run from any directory:"
echo "  godex"
echo ""
echo "This will:"
echo "  - Mount your current directory as the working directory"
echo "  - Store config in a Docker volume"
echo ""
echo "First run will prompt you to configure your LLM provider."
echo "If the screen looks empty after attaching, press any letter key to trigger TUI redraw."
echo "If Ollama runs on the host, it must listen on 0.0.0.0:11434 for the proxy to reach it."
echo "Optional: snippet (README section):"
echo "  https://github.com/cheikh2shift/godex#ollama-host-firewall-optional"
echo ""
echo "Edit provider config stored in the Docker volume:"
echo "  docker run --rm -it --entrypoint sh -v godex-config:/godex-home/.godex ${IMAGE_NAME} -lc 'ls -la /godex-home/.godex'"
echo "  docker run --rm -it --entrypoint sh -v godex-config:/godex-home/.godex ${IMAGE_NAME} -lc 'nano /godex-home/.godex/providers.yaml'"
echo "  docker run --rm -it --entrypoint sh -v godex-config:/godex-home/.godex ${IMAGE_NAME} -lc 'vi /godex-home/.godex/providers.yaml'"
echo "  docker run --rm -it --entrypoint sh -v godex-config:/godex-home/.godex ${IMAGE_NAME} -lc 'vim /godex-home/.godex/providers.yaml'"
echo ""
echo -e "\033[0;32mInstall complete.\033[0m"
