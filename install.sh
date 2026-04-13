#!/bin/sh

set -e

REPO="cheikh2shift/godex"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
BINARY_NAME="godex"

# Detect version from API
get_version() {
    # Try API first, fall back to parsing HTML
    curl -sL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name"' | sed 's/.*"v\([^"]*\)".*/\1/'
}

# VERSION can be passed as arg, env var, or auto-detected
if [ -n "$1" ]; then
    LATEST_TAG="$1"
elif [ -n "$VERSION" ]; then
    LATEST_TAG="$VERSION"
else
    LATEST_TAG=$(get_version)
    if [ -z "$LATEST_TAG" ]; then
        # Fallback: parse release page
        LATEST_TAG=$(curl -sL "https://github.com/${REPO}/releases" 2>/dev/null | grep -o 'releases/tag/[^"]*' | head -1 | sed 's/.*tag\///')
    fi
    if [ -z "$LATEST_TAG" ]; then
        echo "Failed to get latest release version"
        exit 1
    fi
fi

LATEST_TAG="${LATEST_TAG#v}"

echo "LATEST_TAG=$LATEST_TAG"

# Detect OS and architecture
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
echo "Detected OS=$OS ARCH=$ARCH"

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


URL="https://github.com/${REPO}/releases/download/v${LATEST_TAG}/${FILENAME}"
SHA_URL="https://github.com/${REPO}/releases/download/v${LATEST_TAG}/${SHA_FILENAME}"

echo "Using version v${LATEST_TAG}"

echo "Downloading ${FILENAME}..."
curl -sSL -o "${FILENAME}" "$URL"

echo "Downloading SHA256 checksum..."
curl -sSL -o "${SHA_FILENAME}" "$SHA_URL"

echo "Verifying checksum..."
if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c "${SHA_FILENAME}"
elif command -v shasum >/dev/null 2>&1; then
    EXPECTED_HASH=$(cat "${SHA_FILENAME}" | cut -d' ' -f1)
    ACTUAL_HASH=$(shasum -a 256 "${FILENAME}" | cut -d' ' -f1)
    if [ "$EXPECTED_HASH" = "$ACTUAL_HASH" ]; then
        echo "Checksum verified"
    else
        echo "Checksum mismatch!"
        exit 1
    fi
elif command -v openssl >/dev/null 2>&1; then
    EXPECTED_HASH=$(cat "${SHA_FILENAME}" | cut -d' ' -f1)
    ACTUAL_HASH=$(openssl dgst -sha256 "${FILENAME}" | sed 's/.* //')
    if [ "$EXPECTED_HASH" = "$ACTUAL_HASH" ]; then
        echo "Checksum verified"
    else
        echo "Checksum mismatch!"
        exit 1
    fi
else
    echo "Warning: Neither sha256sum nor shasum nor openssl found. Skipping checksum verification."
    echo "Consider installing coreutils (Linux) or using Homebrew (macOS) for security."
fi

# Clean up checksum file
rm -f "${SHA_FILENAME}"

# Rename to generic name for installation
mv "${FILENAME}" "${BINARY_NAME}"
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

godex_get_config_path() {
    local default_path="${HOME}/.godex/providers.yaml"
    for ((i=1; i<cword; i++)); do
        if [[ "${words[i]}" == "--config" ]] && [[ -n "${words[i+1]}" ]] && [[ "${words[i+1]}" != --* ]]; then
            echo "${words[i+1]}"
            return
        fi
    done
    echo "$default_path"
}

godex_get_providers() {
    local config_path=$(godex_get_config_path)
    if [[ -f "$config_path" ]]; then
        grep -E "^    - name:" "$config_path" | sed "s/.*- name://" | tr -d " "
    fi
}

godex_get_default_provider() {
    local config_path=$(godex_get_config_path)
    if [[ -f "$config_path" ]]; then
        grep -E "^    - name:" "$config_path" | head -1 | sed "s/.*- name://" | tr -d " "
    fi
}

godex_get_models() {
    local config_path=$(godex_get_config_path)
    local provider_name="${1:-}"
    local query="${2:-}"
    if [[ -f "$config_path" ]]; then
        if [[ -z "$provider_name" ]]; then
            provider_name=$(godex_get_default_provider)
        fi
        godex --completion models "$config_path" "$provider_name" "$query"
    fi
}

godex_get_mcp_servers() {
    local config_path=$(godex_get_config_path)
    local provider_name="${1:-}"
    if [[ -z "$provider_name" ]]; then
        provider_name=$(godex_get_default_provider)
    fi
    if [[ -f "$config_path" ]]; then
        godex --completion mcp-servers "$config_path" "$provider_name"
    fi
}

godex_get_flags_with_desc() {
    echo "--config:provider configuration YAML"
    echo "--provider:provider name to use"
    echo "--model:override provider model"
    echo "--hive:enable hive mode with a shared secret"
    echo "--wizard:launch provider configuration wizard"
    echo "--prompt:run a single prompt non-interactively"
    echo "--auto-confirm:auto-run suggested commands"
    echo "--version:print version information"
    echo "--debug:enable debug mode"
    echo "--completion:generate shell completion"
    echo "--llama-server:external llama.cpp server URL"
    echo "mcp:manage MCP servers (subcommand)"
}

_cgodex_completion() {
    local cur prev words cword
    _init_completion || return

    if [[ "${words[1]}" == "mcp" ]]; then
        if [[ $cword -eq 2 ]]; then
            COMPREPLY=($(compgen -W "add remove" -- "${cur}"))
            return 0
        fi
        case "$prev" in
            --name)
                local provider_name=""
                local i
                for ((i=1; i<cword; i++)); do
                    if [[ "${words[i]}" == "--provider" ]] && [[ -n "${words[i+1]}" ]] && [[ "${words[i+1]}" != --* ]]; then
                        provider_name="${words[i+1]}"
                        break
                    fi
                done
                if [[ "${words[2]}" == "remove" ]]; then
                    local servers=$(godex_get_mcp_servers "$provider_name")
                    COMPREPLY=($(compgen -W "$servers" -- "$cur"))
                else
                    COMPREPLY=($(compgen -W "filesystem bash webscraper" -- "$cur"))
                fi
                return 0
                ;;
            --provider)
                local providers=$(godex_get_providers)
                if [[ -n "$providers" ]]; then
                    COMPREPLY=($(compgen -W "$providers" -- "$cur"))
                fi
                return 0
                ;;
            --config)
                COMPREPLY=($(compgen -d -S/ -- "$cur"))
                COMPREPLY+=($(compgen -f -X '!*.yaml' -X '!*.yml' -- "$cur"))
                return 0
                ;;
            --allowed-path)
                COMPREPLY=($(compgen -d -S/ -- "$cur"))
                return 0
                ;;
        esac
        if [[ "$cur" == -* ]]; then
            local mcp_flags="--provider --name --command --args --env --transport --allowed-path --allowed-url"
            COMPREPLY=($(compgen -W "$mcp_flags" -- "$cur"))
            return 0
        fi
    fi
    
    case "$prev" in
        --provider)
            local providers=$(godex_get_providers)
            if [[ -n "$providers" ]]; then
                COMPREPLY=($(compgen -W "$providers" -- "$cur"))
            fi
            return 0
            ;;
        --config)
            COMPREPLY=($(compgen -d -S/ -- "$cur"))
            COMPREPLY+=($(compgen -f -X '!*.yaml' -X '!*.yml' -- "$cur"))
            return 0
            ;;
        --model)
            local provider_name=""
            local i
            for ((i=1; i<cword; i++)); do
                if [[ "${words[i]}" == "--provider" ]] && [[ -n "${words[i+1]}" ]] && [[ "${words[i+1]}" != --* ]]; then
                    provider_name="${words[i+1]}"
                    break
                fi
            done
            if [[ -z "$provider_name" ]]; then
                provider_name=$(godex_get_default_provider)
            fi
            while IFS= read -r model; do
                COMPREPLY+=("$model")
            done < <(godex --completion models "$(godex_get_config_path)" "$provider_name" "${cur}")
            return 0
            ;;
    esac
    
    if [[ "$cur" == -* ]]; then
        local flags=$(godex_get_flags_with_desc | cut -d: -f1)
        COMPREPLY=($(compgen -W "$flags" -- "$cur"))
    else
        local providers=$(godex_get_providers)
        if [[ -n "$providers" ]]; then
            COMPREPLY=($(compgen -W "$providers mcp" -- "$cur"))
        else
            COMPREPLY=($(compgen -W "mcp" -- "$cur"))
        fi
    fi
}

complete -F _cgodex_completion godex
'

# Install bash completion
if [ -d "/etc/bash_completion.d" ] && [ -w "/etc/bash_completion.d" ]; then
    echo "$GODEX_BASH_COMPLETION" > "/etc/bash_completion.d/godex"
    echo "Bash completion installed to /etc/bash_completion.d/godex"
elif [ -n "$BASH_COMPLETION_USER_DIR" ] && [ -d "$BASH_COMPLETION_USER_DIR" ]; then
    echo "$GODEX_BASH_COMPLETION" > "${BASH_COMPLETION_USER_DIR}/godex"
    echo "Bash completion installed to ${BASH_COMPLETION_USER_DIR}/godex"
elif [ -d "$HOME/.bash_completion.d" ]; then
    echo "$GODEX_BASH_COMPLETION" > "$HOME/.bash_completion.d/godex"
    echo "Bash completion installed to $HOME/.bash_completion.d/godex"
else
    mkdir -p "$HOME/.bash_completion.d"
    echo "$GODEX_BASH_COMPLETION" > "$HOME/.bash_completion.d/godex"
    echo "Bash completion installed to $HOME/.bash_completion.d/godex"
fi

echo "Installation complete!"
