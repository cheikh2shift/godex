# Hive Network

GoDex supports a Hive network mode where multiple instances can work together, with the ability to delegate tasks to idle agents.

## Overview

In Hive mode:
- One instance acts as the **master** - coordinates tasks and communicates with the LLM
- Additional instances are **workers** - execute delegated tasks
- Workers can delegate tasks to other idle workers, creating a network of agents

## Configuration

Enable Hive mode by using the `--hive` flag with a shared secret:

```bash
godex --hive "my-secret-hive-code"
```

Workers automatically join the hive when they start with the same `--hive` secret.

## How It Works

### Delegation

When the master agent encounters a task, it can delegate to a worker using the `hive_delegate` tool:

```json
{
  "name": "hive_delegate",
  "arguments": {
    "prompt": "Analyze this code file and explain what it does",
    "required_tools": ["filesystem", "bash"]
  }
}
```

**Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `prompt` | string | The task to delegate (required) |
| `target_id` | string | Specific worker ID (optional, auto-selects if omitted) |
| `required_tools` | array | MCP server names required for the task (optional) |

### Instance Selection

When `target_id` is omitted, the system automatically selects the best worker:

1. If `required_tools` is specified, picks the worker with the most matching MCP servers
2. Falls back to the worker with the most available context tokens

### Available Tools

| Tool | Description |
|------|-------------|
| `hive_delegate` | Delegate a task to another hive instance |
| `hive_list` | List all available hive instances |

### Listing Instances

```json
{
  "name": "hive_list",
  "arguments": {}
}
```

Returns information about each instance:
- `id` - Unique instance identifier
- `name` - Human-readable name
- `model` - LLM model in use
- `mcp_servers` - Available MCP server names (for skill matching)
- `port` - Instance port
- `started_at` - When the instance started

## Task Flow

1. Master receives a prompt from the user
2. Master decides to delegate a subtask to a worker
3. Master calls `hive_delegate` with the task
4. Worker executes the task with full tool access
5. Worker sends results back to master
6. Master receives results and continues processing

## Use Cases

- **Parallel research**: Delegate web scraping tasks to multiple workers
- **Code analysis**: Workers analyze different files simultaneously  
- **Long-running tasks**: Workers handle time-consuming operations while master coordinates
- **Skill-based routing**: Delegate tasks to workers with specific MCP server capabilities

## Security

- Workers only accept connections with the correct hive code.
- Communication happens over localhost WebSocket connections
- Each instance maintains its own allowed paths and permissions

## Architecture

```
User ──> Master ──(delegate)──> Worker 1
                           └──> Worker 2
                           └──> Worker N
```

**Master**: Coordinates tasks, communicates with LLM, receives results
**Workers**: Execute delegated tasks, can further delegate to idle workers

## Example Session

```
$ godex
GoDex - Connected to ollama (nemotron)
Hive: 3 instances online
MCP Servers: filesystem, bash, webscraper

> Analyze the entire src directory

[Swift Comet 31] Hive: delegated to Worker-2

● Sending output to LLM...

[Swift Comet 31] Hive worker completed:
<markdown rendered results>

● Thinking -
```

## Troubleshooting

### No workers available

- Ensure all instances use the same `HIVE_CODE`
- Check that workers started successfully
- Verify firewall settings allow localhost connections

### Delegation fails

- Check worker is still running
- Verify the target worker has required MCP servers
- Review the instance logs for errors
