# MCP Integration Guide

GoDex supports integrating with external MCP (Model Context Protocol) servers to extend its capabilities. This document explains how to configure external MCP servers.

## Configuration

External MCP servers are configured in your `config.yaml` file under the `mcp` section.

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
mcp:
  servers:
    # Example: A custom filesystem server
    filesystem:
      command: "npx"
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/Users/myuser/docs"]
      transport: "stdio"
      allowed_paths:
        - "/Users/myuser/docs"
        - "/Users/myuser/shared"

    # Example: A GitHub MCP server
    github:
      command: "npx"
      args: ["-y", "@modelcontextprotocol/server-github"]
      transport: "stdio"
      env:
        - "GITHUB_PERSONAL_ACCESS_TOKEN=$GITHUB_TOKEN"
```

User: Can you read the contents of /home/user/projects/README.md?
## Built-in MCP Servers

GoDex includes several built-in MCP tools that are implemented as inline Go servers. These servers don't require external process execution - they're built directly into the Go codebase.

### Bash

Execute shell commands on your system.

```yaml
mcp:
  servers:
    bash:
      allowed_paths:
        - "/home/user/projects"
        - "/tmp"
```

### Filesystem

Perform file operations (read, write, list, delete, search).

```yaml
mcp:
  servers:
    filesystem:
      allowed_paths:
        - "/home/user/projects"
```

### Webscraper

Fetch URLs and extract content from web pages with JavaScript rendering support.

```yaml
mcp:
  servers:
    webscraper:
      allowed_urls:
        - "https://example.com"
        - "https://*.github.io"
```

### How It Works

The built-in servers are created inline within GoDex itself using:
- `mcp.NewFileSystemServer(paths)` for filesystem operations
- `mcp.NewBashServer(paths)` for command execution
- `mcp.NewWebScraperServer(urls)` for web scraping

The `allowed_paths` (or `allowed_urls` for webscraper) field restricts what paths/URLs the respective server can access. If not specified, reasonable defaults are applied.

> **Note:** You can also configure external MCP servers by specifying the `command` and `args` fields to run external MCP server processes.


GoDex: (uses filesystem MCP server to read the file)
# Contents of README.md
...
```

## Environment Variables

You can reference environment variables in your MCP server configuration using `$VAR_NAME` syntax:

```yaml
mcp:
  servers:
    myserver:
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
