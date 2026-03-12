**NOTICE** : Please help me identify any security issues with this program.

# GoDex - AI-Powered CLI Agent

![GoDex Screenshot](screen.gif)

GoDex is a CLI tool that interfaces with Ollama (and other LLM providers) through a TUI, with built-in MCP support for filesystem and bash commands.

## Requirements

- **Go 1.25.7+** - Build from source
- **Ollama** - For the default LLM backend (or use Gemini)

### Ollama Setup

1. **Install Ollama**: Follow instructions at https://github.com/ollama/ollama

2. **Start Ollama server**:
   ```bash
   ollama serve
   ```

3. **Pull a model** (recommended: codeqwen or codellama):
   ```bash
   ollama pull codeqwen
   # or
   ollama pull codellama
   ```

4. **Verify Ollama is running**:
   ```bash
   curl http://localhost:11434
   ```

## Configuration

GoDex reads provider configuration from `~/.godex/providers.yaml`.

## Installation

### Quick Install (Linux/macOS)

```bash
curl -sSL https://raw.githubusercontent.com/cheikh-seck/godex/main/install.sh | sh
```

### Manual Download

Download the latest release from [GitHub Releases](https://github.com/cheikh-seck/godex/releases):

| OS | Architecture | File |
|----|-------------|------|
| Linux | AMD64 | `godex-linux-amd64` |
| Linux | ARM64 | `godex-linux-arm64` |
| macOS | AMD64 | `godex-darwin-amd64` |
| macOS | ARM64 | `godex-darwin-arm64` |
| Windows | AMD64 | `godex-windows-amd64.exe` |

Example:
```bash
# Linux
curl -L -o godex https://github.com/cheikh-seck/godex/releases/latest/download/godex-linux-amd64
chmod +x godex
sudo mv godex /usr/local/bin/

# macOS
curl -L -o godex https://github.com/cheikh-seck/godex/releases/latest/download/godex-darwin-arm64
chmod +x godex
sudo mv godex /usr/local/bin/
```

### Quick Setup

Run the wizard to generate the config:
```bash
go run ./cmd/godex --wizard
```

### Manual Configuration

Create `~/.godex/providers.yaml`:

```yaml
providers:
  - name: ollama
    type: ollama
    endpoint: http://localhost:11434
    model: minimax-m2.5:cloud
    description: Ollama with codeqwen
    temperature: 0.2
    mcp_servers:
      - name: filesystem # enable file exploring
      - name: bash # enable command execution

default_provider: ollama
```

### Configuration Options

| Field | Description |
|-------|-------------|
| `name` | Provider identifier |
| `type` | Provider type: `ollama` or `gemini` |
| `endpoint` | Ollama URL (e.g., `http://localhost:11434`) |
| `model` | Model name (e.g., `codeqwen`, `codellama`, `minimax-m2.5:cloud`) |
| `description` | Human-readable description |
| `temperature` | LLM temperature (0.0-1.0) |
| `max_tool_rounds` | Max tool call rounds (default: 10) |
| `tool_timeout` | Tool execution timeout in seconds (default: 180) |
| `api_key_env` | Environment variable for API key (Gemini) |
| `api_key` | Direct API key (not recommended) |
| `mcp_servers` | List of MCP servers to enable |

### MCP Servers

GoDex includes built-in MCP servers:

| Server | Description |
|--------|-------------|
| `filesystem` | Read, write, list directories, create/delete files |
| `bash` | Run shell commands, Python, Node.js |
| `webscraper` | Fetch URLs with JavaScript rendering, search HTML, extract links |

#### Adding Allowed Paths

By default, MCP servers only allow access to the current working directory. Add more allowed paths:

```yaml
mcp_servers:
  - name: filesystem
    allowed_paths:
      - /home/user/project1
      - /home/user/project2
  - name: bash
    allowed_paths:
      - /home/user/project1
  - name: webscraper
    allowed_urls:
      - https://example.com
      - https://docs.example.com
```

## Usage

### Quick Install

```bash
# Linux/macOS
curl -sSL https://raw.githubusercontent.com/cheikh-seck/godex/main/install.sh | sh

# Or build from source
go build -o godex ./cmd/godex
sudo mv godex /usr/local/bin/
```

### Run

```bash
# Run the CLI
godex

# Run with custom config
godex --config /path/to/providers.yaml

# Run a single prompt (non-interactive)
godex --prompt "list files in current directory"
```

### Commands in TUI

- `/help` - Show help
- `/paths` - Show allowed MCP paths
- `/add-path <path>` - Add allowed path
- `/tools` - Show available MCP tools
- `/exit` or `/quit` - Exit
- Up/Down arrows - Command history
- Tab - Autocomplete `/` commands

### Example Session

```
$ godex
GoDex - Connected to ollama (codeqwen)
MCP Servers: 2

> list files in this directory
[tool call: list_directory]
...
```

## Building

```bash
go build -o godex ./cmd/godex
./godex
```


