# Built-in filesystem MCP server (`godex mcp serve filesystem`)

GoDex contains a built-in filesystem tool implementation.

This page documents how to run that built-in filesystem as a **standalone MCP server over stdio**, so other AI agents/clients can connect to it.

## Run

```bash
godex mcp serve filesystem
```

## Permissions (roots + allowlist)

This server supports common MCP permission workflows used by other agents:

- **Client roots** (recommended): when the connected client supports MCP *roots*, GoDex will request the roots list and treat them as allowed paths.
- **Explicit allowlist**: pass `--allowed-path` (repeatable) to add allowed paths up front.

Examples:

```bash
# Explicit allowlist
godex mcp serve filesystem --allowed-path /home/user/project --allowed-path /tmp

# Disable roots usage (only use explicit allowlist / cwd fallback)
godex mcp serve filesystem --use-roots=false --allowed-path /tmp
```

`--auto-confirm` (unsafe) will automatically expand allowed paths when a request hits a restricted path.

## Configure another agent/client

Any MCP client that can launch stdio servers can run:

```text
command: godex
args: ["mcp", "serve", "filesystem", "--allowed-path", "/path/to/workspace"]
transport: stdio
```

## Tools

The server exposes a subset of GoDex’s filesystem tools, including:

- `read_file`, `write_file`, `list_directory`
- `create_directory`, `delete_file`
- `search_files`, `search_in_file`, `search_directory_text`, `search_file_text`
- `get_file_info`, `read_file_line_range`
- `replace_first_in_file`, `replace_line_range`, `insert_at_line`
- `list_allowed_paths`

## Add to OpenCode

### Via config file

In your `opencode.json` (project) or `~/.config/opencode/opencode.json` (global):

```jsonc
{
  "mcp": {
    "godex-filesystem": {
      "type": "local",
      "command": ["godex", "mcp", "serve", "filesystem", "--allowed-path", "/path/to/workspace"],
      "enabled": true
    }
  }
}
```

Then restart OpenCode.

### Via CLI

```bash
npx @anthropic/anthropic-cli mcp add
# Or if opencode has the same tool:
opencode mcp add
```

## Add to Claude Desktop

Edit `claude_desktop_config.json` (location varies by OS):
- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Linux: `~/.config/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`

```json
{
  "mcpServers": {
    "godex-filesystem": {
      "command": "godex",
      "args": ["mcp", "serve", "filesystem", "--allowed-path", "/path/to/workspace"],
      "env": {}
    }
  }
}
```

## Add to Claude Code

```bash
claude mcp add godex-filesystem --transport stdio -- godex mcp serve filesystem --allowed-path /path/to/workspace
```

Or via config in `.mcp.json` (project) or `~/.claude.json` (global):

```jsonc
{
  "mcp": {
    "godex-filesystem": {
      "command": ["godex", "mcp", "serve", "filesystem", "--allowed-path", "/path/to/workspace"],
      "env": {}
    }
  }
}
```

## Add to Codex

Run:

```bash
codex mcp add godex-filesystem -- godex mcp serve filesystem --allowed-path /path/to/workspace
```

Or edit `~/.codex/config.toml`:

```toml
[mcp_servers.godex-filesystem]
command = "godex"
args = ["mcp", "serve", "filesystem", "--allowed-path", "/path/to/workspace"]
```

For workspace-specific config, create `.codex/config.toml` in your project.

## Add to Cursor

Cursor doesn't have a CLI command for adding MCP servers. Edit `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "godex-filesystem": {
      "command": "godex",
      "args": ["mcp", "serve", "filesystem", "--allowed-path", "/path/to/workspace"]
    }
  }
}
```

Or add via Settings > Tools & MCP > Add > Enter custom server details.

## Add to VS Code

### Via Command Palette
1. Press `Ctrl+Shift+P` (or `Cmd+Shift+P` on Mac)
2. Search **MCP: Add Server**
3. Select **Local** (stdio)
4. Enter command: `godex mcp serve filesystem --allowed-path /path/to/workspace`

### Via CLI

```bash
code --add-mcp '{"name":"godex-filesystem","command":"godex","args":["mcp","serve","filesystem","--allowed-path","/path/to/workspace"]}'
```

### Via config file
Edit `.vscode/mcp.json` (workspace) or run **MCP: Open User Configuration** to edit global config:

```json
{
  "servers": {
    "godex-filesystem": {
      "command": "godex",
      "args": ["mcp", "serve", "filesystem", "--allowed-path", "/path/to/workspace"]
    }
  }
}
```

