#!/bin/sh

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
if [ "$OS" = "windows" ]; then
    FILENAME="${FILENAME}.exe"
fi

# Get latest tag
LATEST_TAG=$(curl -sL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed 's/.*"v\([^"]*\)".*/\1/')
if [ -z "$LATEST_TAG" ]; then
    echo "Failed to get latest release version"
    exit 1
fi

URL="https://github.com/${REPO}/releases/download/v${LATEST_TAG}/${FILENAME}"

echo "Downloading ${FILENAME}..."
curl -sSL -o "${BINARY_NAME}" "$URL"

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
        grep -E "^    - name:" "$config_path" | sed "s/.*- name://" | tr -d " \""
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

_godex_completion() {
    local cur prev words cword
    _init_completion || return
    
    case "${prev}" in
    --provider)
        local providers
        providers=$(godex_get_providers)
        COMPREPLY=($(compgen -W "$providers" -- "${cur}"))
        return
        ;;
    --config)
        _filedir yaml yml
        return
        ;;
    --completion)
        COMPREPLY=($(compgen -W "bash zsh fish" -- "${cur}"))
        return
        ;;
    esac
    
    local flags
    flags=$(godex_get_flags_with_desc)
    
    if [[ -z "${cur}" ]]; then
        # Empty input - show all flags
        while IFS=: read -r flag desc; do
            COMPREPLY+=("$flag")
        done <<< "$flags"
    elif [[ "${cur}" == -* ]]; then
        # User started with dash - filter flags
        local filter="${cur}"
        while IFS=: read -r flag desc; do
            if [[ "$flag" == "$filter"* ]]; then
                COMPREPLY+=("$flag")
            fi
        done <<< "$flags"
    else
        # Show providers for non-flag input
        local providers
        providers=$(godex_get_providers)
        while IFS= read -r p; do
            COMPREPLY+=("$p")
        done <<< "$providers"
    fi
}

complete -F _godex_completion godex
'

setup_completion() {
    echo ""
    echo "Setup shell completion?"
    echo "1) Bash"
    echo "2) Zsh"
    echo "3) Fish"
    echo "4) Skip"
    printf "Select option (1-4): "
    
    if [ -n "$CI" ]; then
        # Non-interactive mode
        choice=4
    else
        read -r choice
    fi
    
    case "$choice" in
        1)
            COMPLETION_DIR="$HOME/.bash_completion.d"
            mkdir -p "$COMPLETION_DIR"
            echo "$GODEX_BASH_COMPLETION" > "$COMPLETION_DIR/godex"
            
            # Add source line to bashrc if not already present
            if ! grep -q "bash_completion.d/godex" "$HOME/.bashrc" 2>/dev/null; then
                echo "" >> "$HOME/.bashrc"
                echo "# godex shell completion" >> "$HOME/.bashrc"
                echo "for f in \$HOME/.bash_completion.d/*; do source \$f; done" >> "$HOME/.bashrc"
            fi
            echo "Bash completion installed. Run 'source ~/.bashrc' or start a new shell."
            ;;
        2)
            COMPLETION_DIR="$HOME/.zsh/completions"
            mkdir -p "$COMPLETION_DIR"
            echo "$GODEX_BASH_COMPLETION" > "$COMPLETION_DIR/_godex"
            
            # Add to fpath if not already present
            if ! grep -q "zsh/completions" "$HOME/.zshrc" 2>/dev/null; then
                echo "" >> "$HOME/.zshrc"
                echo "# godex shell completion" >> "$HOME/.zshrc"
                echo "fpath=(\$HOME/.zsh/completions \$fpath)" >> "$HOME/.zshrc"
                echo "autoload -U compinit && compinit" >> "$HOME/.zshrc"
            fi
            echo "Zsh completion installed. Run 'source ~/.zshrc' or start a new shell."
            ;;
        3)
            FISH_COMPLETION_DIR="$HOME/.config/fish/completions"
            mkdir -p "$FISH_COMPLETION_DIR"
            cat > "$FISH_COMPLETION_DIR/godex.fish" << 'FISH_EOF'
# fish completion for godex
function __godex_complete_providers
    set -l config_path ~/.godex/providers.yaml
    if test -f "$config_path"
        grep -E "^    - name:" "$config_path" | sed "s/.*- name://" | tr -d " \""
    end
end

complete -c godex -l config -r -f
complete -c godex -l provider -x -a "(__godex_complete_providers)"
complete -c godex -l wizard -s w -n "__fish_use_subcommand" -f
complete -c godex -l prompt -s p -x
complete -c godex -l auto-confirm -s y -f
complete -c godex -l version -s v -f
complete -c godex -l debug -s d -f
complete -c godex -l completion -x -a "bash zsh fish" -d "Generate shell completion"
FISH_EOF
            echo "Fish completion installed. Restart fish or run 'godex --completion fish | source'."
            ;;
        *)
            echo "Skipped. You can setup completion later with: godex --completion <bash|zsh|fish>"
            ;;
    esac
}

setup_completion
