# MCP Integration Guide

GoDex supports integrating with external MCP (Model Context Protocol) servers to extend its capabilities. This document explains how to configure external MCP servers.

## Configuration

External MCP servers are configured in your `~/.godex/providers.yaml` file under each provider's `mcp_servers` section.

You can also manage MCP servers from the CLI:

```bash
godex mcp add --provider my-local-identifier --name filesystem --allowed-path /home/user/project
godex mcp add --provider my-local-identifier --name webscraper --allowed-url https://docs.example.com
godex mcp add --provider my-local-identifier --name myserver --command /usr/local/bin/my-mcp --args --flag --allowed-path /tmp
godex mcp remove --provider my-local-identifier --name myserver
```

### MCPServer Structure

Each MCP server is defined with the following properties:

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | A friendly name for the server |
| `command` | string | The command to run the MCP server executable |
| `args` | []string | Optional command-line arguments |
| `env` | []string | Optional environment variables |
| `transport` | string | Transport type (e.g., `stdio`, `sse`) |
| `allowed_paths` | []string | Paths the server is allowed to access |

### Example Configuration

```yaml
providers:
  - name: ollama
    type: ollama
    endpoint: http://localhost:11434
    model: minimax-m2.7:cloud
    description: Ollama with Chrome MCP
    mcp_servers:
      - name: chrome
        command: "/usr/local/bin/chrome-mcp"
        transport: "stdio"

      # Example: A GitHub MCP server
      - name: github
        command: "npx"
        args: ["-y", "@modelcontextprotocol/server-github"]
        transport: "stdio"
        env:
          - "GITHUB_PERSONAL_ACCESS_TOKEN=$GITHUB_TOKEN"

default_provider: ollama
```

## Built-in MCP Servers

GoDex includes several built-in MCP tools that are implemented as inline Go servers. These servers don't require external process execution - they're built directly into the Go codebase.

### Bash

Execute shell commands on your system.

```yaml
mcp_servers:
  - name: bash
    allowed_paths:
      - "/home/user/projects"
      - "/tmp"
```

### Filesystem

Perform file operations (read, write, list, delete, search).

```yaml
mcp_servers:
  - name: filesystem
    allowed_paths:
      - "/home/user/projects"
```

### Webscraper

Fetch URLs and extract content from web pages with JavaScript rendering support.

```yaml
mcp_servers:
  - name: webscraper
    allowed_urls:
      - "https://example.com"
      - "https://*.github.io"
```

## External MCP Servers

## Built-in filesystem as an MCP server (stdio)

GoDex can run its built-in filesystem implementation as a standalone MCP server over stdio:

```bash
godex mcp serve filesystem --allowed-path /home/user/project --allowed-path /tmp
```

Details: [MCP_BUILTIN_FILESYSTEM_SERVER.md](MCP_BUILTIN_FILESYSTEM_SERVER.md)

### Chrome MCP Controller

Control Chrome tabs via a browser extension. Requires the [chrome-mcp](https://github.com/cheikh2shift/chrome-mcp) server and Chrome extension.

**Features:**
- List and manage connected Chrome tabs
- Execute JavaScript in target tabs
- Extract page structure and content
- Find elements with CSS/XPath selectors
- Take screenshots

**Configuration:**

```yaml
providers:
  - name: ollama
    type: ollama
    endpoint: http://localhost:11434
    model: minimax-m2.7:cloud
    mcp_servers:
      - name: chrome
        command: "/usr/local/bin/chrome-mcp"
        transport: "stdio"

default_provider: ollama
```

**Available Tools:**

| Tool | Description |
|------|-------------|
| `list_connected_tabs` | List all tabs connected via extension |
| `get_tab_info` | Get detailed tab information |
| `get_page_structure` | Get structured DOM overview |
| `extract_page_content` | Extract readable text, links, forms |
| `get_page_source` | Get raw HTML source |
| `find_elements` | Find elements with CSS/XPath |
| `execute_script` | Execute JavaScript in tab |
| `get_element_details` | Get element styles and position |
| `wait_for_element` | Wait for element to appear |
| `take_screenshot` | Capture page screenshot |

**Usage:**

1. Install the Chrome extension from `chrome-mcp/extension/`
2. Start the extension's HTTP server via the popup
3. Connect tabs you want to control
4. Use godex to interact with the tab

### How Built-in Servers Work

The built-in servers are created inline within GoDex itself using:
- `mcp.NewFileSystemServer(paths)` for filesystem operations
- `mcp.NewBashServer(paths)` for command execution
- `mcp.NewWebScraperServer(urls)` for web scraping

The `allowed_paths` (or `allowed_urls` for webscraper) field restricts what paths/URLs the respective server can access. If not specified, reasonable defaults are applied.

> **Note:** External MCP servers run as separate processes. Configure them via the `command` and `args` fields.

## Environment Variables

You can reference environment variables in your MCP server configuration using `$VAR_NAME` syntax:

```yaml
mcp_servers:
  - name: myserver
    command: "my-mcp-server"
    env:
      - "API_KEY=$MY_API_KEY"
      - "DATABASE_URL=$DB_URL"
```

Make sure these environment variables are set before starting GoDex.

## Transport Types

### stdio

The default transport type. Communicates with the MCP server via standard input/output.

```yaml
transport: "stdio"
```

### SSE (Server-Sent Events)

For HTTP-based MCP servers.

```yaml
transport: "sse"
url: "http://localhost:8080/sse"
```

## Security

- Only use MCP servers from trusted sources
- Review the `allowed_paths` configuration to restrict filesystem access
- Be cautious when passing sensitive data via environment variables
- MCP servers run with the same permissions as the GoDex process

## Troubleshooting

### Server fails to start

- Verify the command path is correct
- Check that required dependencies are installed
- Review the server logs for error messages

### Tools not available

- Ensure the MCP server started successfully
- Check that the server configuration is valid YAML
- Verify the transport type matches the server's requirements

### Permission denied errors

- Check that the paths in `allowed_paths` exist
- Verify file/directory permissions
- Ensure the MCP server process has necessary access rights
