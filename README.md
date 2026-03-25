> [!WARNING]
> I love adding features, please add any requests to the issue tab.

# GoDex - AI Agent

![GoDex Screenshot](screen3.gif)

GoDex is an AI agent that interfaces with Ollama, Gemini, Hugging Face, OpenRouter (and other LLM providers) through a TUI, with built-in MCP support.

Orchestration and parallel tasks? open another terminal tab and start a new instance of `godex`.

## Requirements

- **Go 1.25.7+** - Build from source
- **Ollama** - For the default LLM backend (or use Gemini, Hugging Face, or OpenRouter)

### Ollama Setup

1. **Install Ollama**: Follow instructions at https://github.com/ollama/ollama

2. **Start Ollama server**:
   ```bash
   ollama serve
   ```

3. **Pull a model** (recommended: nemotron-3-super:cloud or minimax-m2.7:cloud):
   ```bash
   ollama pull nemotron-3-super:cloud
   # or
   ollama pull minimax-m2.7:cloud
   ```

4. **Verify Ollama is running**:
   ```bash
   curl http://localhost:11434
   ```

### OpenRouter Setup

1. **Get an API key**: Sign up at https://openrouter.ai/keys

2. **Set the environment variable**:
   ```bash
   export OPENROUTER_API_KEY=sk-or-v1-...
   ```

3. **Run the wizard** to configure:
   ```bash
   godex --wizard
   ```
   Select `openrouter` as the provider type and choose from 100+ available models.

## Configuration

GoDex reads provider configuration from `~/.godex/providers.yaml`.

## Installation

### Build from Source

```bash
git clone https://github.com/cheikh2shift/godex.git
cd godex
go build -o godex ./cmd/godex
sudo mv godex /usr/local/bin/
```

### Quick Install (Linux/macOS)

```bash
curl -sSL https://raw.githubusercontent.com/cheikh2shift/godex/main/install.sh | sh
```

### Manual Download

Download from [GitHub Releases](https://github.com/cheikh2shift/godex/releases):

| OS | Architecture | File |
|----|-------------|------|
| Linux | AMD64 | `godex-linux-amd64` |
| Linux | ARM64 | `godex-linux-arm64` |
| macOS | AMD64 | `godex-darwin-amd64` |
| macOS | ARM64 | `godex-darwin-arm64` |
| Windows | AMD64 | `godex-windows-amd64.exe` |

Example:

**Linux (AMD64):**
```bash
curl -L -o godex https://github.com/cheikh2shift/godex/releases/latest/download/godex-linux-amd64
chmod +x godex
sudo mv godex /usr/local/bin/
```

**macOS (Intel):**
```bash
curl -L -o godex https://github.com/cheikh2shift/godex/releases/latest/download/godex-darwin-amd64
chmod +x godex
sudo mv godex /usr/local/bin/
```

**macOS (Apple Silicon):**
```bash
curl -L -o godex https://github.com/cheikh2shift/godex/releases/latest/download/godex-darwin-arm64
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
| `type` | Provider type: `ollama`, `gemini`, `huggingface`, or `openrouter` |
| `endpoint` | Base URL for provider (Ollama: `http://localhost:11434`, Hugging Face: `https://router.huggingface.co/v1`, OpenRouter: `https://openrouter.ai/api/v1`) |
| `model` | Model name (e.g., `nemotron-3-super:cloud`, `codellama`, `minimax-m2.7:cloud`; Hugging Face supports routing suffixes like `:fastest` or `:provider`) |
| `description` | Human-readable description |
| `temperature` | LLM temperature (0.0-1.0) |
| `max_tool_rounds` | Max tool call rounds (default: 10) |
| `tool_timeout` | Tool execution timeout in seconds (default: 180) |
| `api_key_env` | Environment variable for API key (Gemini/Hugging Face/OpenRouter) |
| `api_key` | Direct API key (not recommended) |
| `mcp_servers` | List of MCP servers to enable |
| `context_limit` | Context window size in tokens (auto-detected for OpenRouter) |

### MCP Servers

GoDex includes built-in MCP servers:

| Server | Description |
|--------|-------------|
| `filesystem` | Read, write, list directories, create/delete files |
| `bash` | Run shell commands, Python, Node.js |
| `webscraper` | Fetch URLs with JavaScript rendering, search HTML, extract links |

For detailed MCP configuration including external servers, see [MCP.md](MCP.md).

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
# Build from source (recommended)
go build -o godex ./cmd/godex
sudo mv godex /usr/local/bin/

# Or use install script (requires release)
curl -sSL https://raw.githubusercontent.com/cheikh2shift/godex/main/install.sh | sh
```

### Run

```bash
# Run the TUI (uses default provider from config)
godex

# Run with custom config file
godex --config /path/to/providers.yaml

# Run with specific provider (must exist in config)
godex --provider ollama

# Run with custom config and specific provider
godex --config /path/to/providers.yaml --provider gemini

# Run a single prompt (non-interactive)
godex --prompt "list files in current directory"

# Run wizard to create config
godex --wizard
```

### Shell Completion

Enable tab completion for `godex` commands and provider names:

**Bash** (add to `~/.bashrc`):
```bash
source <(godex --completion bash)
```

**Zsh** (add to `~/.zshrc`):
```bash
source <(godex --completion zsh)
```

**Fish**:
```bash
godex --completion fish | source
```

After sourcing, pressing Tab will show:
- All available flags with descriptions
- Provider names when using `--provider`
- File paths when using `--config`

### Commands in TUI

- `/help` - Show help
- `/paths` - Show allowed MCP paths
- `/add-path <filesys|url> <path>` - Add allowed path
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

## Running Securely with Docker

GoDex can be run in an isolated Docker container with a pre-configured sandbox environment containing common tools (Python, Node.js, Go, Rust, etc.).

### Quick Install

```bash
curl -sSL https://raw.githubusercontent.com/cheikh2shift/godex/main/install-docker.sh | bash
```

### Why Use Docker?

Running GoDex in Docker provides:
- **Isolation** - GoDex operates only within the mounted workspace directory
- **No host pollution** - Tools and changes stay contained
- **Consistent environment** - Same tools available regardless of host system
- **Safety** - Test configurations without risking your host system

### Manual Setup

```bash
# Download files
curl -fsSL https://raw.githubusercontent.com/cheikh2shift/godex/main/Dockerfile -o Dockerfile
curl -fsSL https://raw.githubusercontent.com/cheikh2shift/godex/main/docker-compose.yml -o docker-compose.yml

# Create workspace directory
mkdir -p workspace

# Run
docker compose up
```

### Usage

1. **First run** - The container will launch the wizard to configure your provider:
   ```bash
   docker compose up
   ```
   Configure your Ollama/OpenRouter/etc. settings when prompted.

2. **Subsequent runs** - Your config is persisted in a Docker volume:
   ```bash
   docker compose up
   ```

3. **Access your workspace** - The `./workspace` directory in your project is mounted at `/workspace` inside the container. Create or edit files there before running godex.

4. **Custom provider config** - If you have an existing `~/.godex/providers.yaml`, copy it to the workspace:
   ```bash
   cp ~/.godex/providers.yaml ./workspace/providers.yaml
   docker compose run --rm godex godex --config /workspace/providers.yaml
   ```

### Included Tools

The sandbox includes:
- Python 3, pip, pytest, black, flake8
- Node.js, npm
- Go, Rust (rustc, cargo)
- Git, curl, wget
- Build tools: make, cmake, gcc, g++
- Utilities: htop, tree, jq, ripgrep, fd, fzf, vim, nano

### Security Notes

- GoDex can only access files within the `./workspace` directory (read-write)
- Container runs as non-root user (UID 1000)
- Most Linux capabilities dropped; only `NET_RAW` and `NET_BIND_SERVICE` allowed
- No new privileges allowed
- Root filesystem is read-only
- `/tmp` and `/run` use tmpfs (memory-only, non-persistent)
- Process and file limits enforced
- Provider credentials are stored in the `godex-config` volume
- Use `docker compose down -v` to completely remove all data

## Troubleshooting

### Ollama Model Not Found

If you get an error like `{"error":"model 'qwen3-coder-next:cloud' not found"}`, it means the model hasn't been pulled yet. Run:

```bash
ollama pull <model-name>
```

Then test it works with:

```bash
ollama run <model-name>
```

### Ollama Not Running

Make sure Ollama is running in the background. You can start it with:

```bash
ollama serve
```

### Connection Issues

If GoDex can't connect to Ollama, check that the Ollama API is accessible at `http://localhost:11434`.



---

For developers: [DEV.md](DEV.md) - Guide to adding new MCP servers and providers
