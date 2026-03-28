# Hive - Multi-Agent Task Delegation

Hive enables GoDex instances to work together by delegating tasks between a master and worker agents.

## Overview

- **Master**: Coordinates tasks, communicates with LLM, delegates work to workers
- **Workers**: Execute delegated tasks, can further delegate to other workers
- **Communication**: WebSocket over localhost (127.0.0.1)
- **Discovery**: JSON instance files in shared directory

## Starting a Hive

```bash
# Master instance
godex --hive "your-secret-code"

# Worker instances (same code joins the hive)
godex --hive "your-secret-code"
```

All instances with the same code form a hive. The first instance becomes the master by default.

## Tools

### hive_delegate

Delegate a task to another hive instance.

```json
{
  "name": "hive_delegate",
  "arguments": {
    "prompt": "Analyze this code file and explain what it does",
    "required_tools": ["filesystem", "bash"],
    "target_id": "optional-specific-instance-id"
  }
}
```

**Parameters:**
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `prompt` | string | Yes | Task description for the worker |
| `target_id` | string | No | Specific worker ID (auto-selects if omitted) |
| `required_tools` | array | No | MCP servers the worker must have |

### hive_list

List all available hive instances.

```json
{
  "name": "hive_list",
  "arguments": {}
}
```

Returns array of instances with: `id`, `name`, `model`, `port`, `mcp_servers`, `started_at`

## Instance Selection

When `target_id` is omitted, selection logic:

1. If `required_tools` specified: pick worker with most matching MCP servers
2. Otherwise: pick worker with most available context tokens

## How It Works

### Discovery

Each instance writes a JSON file to `{baseDir}/hive/instances/{codeHash}/{instanceID}.json`:

```json
{
  "id": "uuid",
  "name": "Worker-1",
  "model": "nemotron",
  "port": 49152,
  "mcp_servers": ["filesystem", "bash"],
  "started_at": "2024-01-01T00:00:00Z",
  "pid": 12345
}
```

Instances periodically check for other instances by reading this directory.

### Delegation Flow

1. Master calls `hive_delegate` with task prompt
2. Master connects to worker via WebSocket at `ws://127.0.0.1:{port}/ws`
3. Sends message: `{"type": "delegate", "id": "request-id", "prompt": "..."}`
4. Worker executes task via its LLM and tools
5. Worker returns: `{"type": "result", "id": "request-id", "result": "..."}`
6. Master receives result and continues processing

### Task Execution

Each WebSocket connection handles **one task at a time**. The worker:
1. Receives the delegated prompt
2. Runs the full agent loop with its configured tools
3. Returns the final result
4. Connection closes

If a worker receives multiple delegations, they run in parallel (separate goroutines).

## Response Handling

When a worker completes a task:

1. Result is sent back to master via WebSocket
2. Master's main loop receives the result via `Results()` channel
3. If user is typing, the prompt is cancelled
4. Result is displayed immediately
5. User can continue interacting

## Architecture

```
                    ┌──────────────┐
                    │   Master     │
                    │  (user CLI)  │
                    └──────┬───────┘
                           │ delegate
                    ┌──────▼───────┐
        ┌───────────│  Worker 1   │───────────┐
        │           └─────────────┘           │
        │                                    │
   ┌────▼────┐                         ┌────▼────┐
   │ Task A  │                         │ Task B  │
   └─────────┘                         └─────────┘
```

## Use Cases

- **Parallel processing**: Delegate independent tasks to multiple workers
- **Specialized agents**: Workers with specific MCP tools (e.g., webscraper, database)
- **Long-running tasks**: Offload heavy computation without blocking master
- **Skill-based routing**: Match tasks to workers with required capabilities

## Security

- Hive code acts as shared secret
- Only instances with correct code can connect
- All communication over localhost
- Each instance maintains own permissions

## Common Issues

| Issue | Cause | Solution |
|-------|-------|----------|
| No instances found | Wrong code or workers not started | Use same `--hive` code |
| Delegation fails | Worker crashed or tool missing | Check worker logs |
| Instance stuck | Worker still processing | Wait or restart worker |
