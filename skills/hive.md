---
name: godex-hive
description: Set up a GoDex Hive multi-agent network. Use when creating a network of GoDex agents that can delegate tasks between each other.
---

# GoDex Hive

## Instructions

Hive is a multi-agent system where GoDex instances coordinate through task delegation. One instance acts as master, others as workers.

### How Hive Works

- **Master**: Coordinates tasks, communicates with LLM, delegates work to workers
- **Workers**: Execute delegated tasks, can further delegate to other workers
- **Communication**: WebSocket over localhost (127.0.0.1)
- **Discovery**: JSON instance files in shared directory

### Instance Discovery

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

Instances discover each other by reading this directory.

### Delegation Protocol

1. Connect to worker via WebSocket at `ws://127.0.0.1:{port}/ws`
2. Send delegate message:
   ```json
   {"type": "delegate", "id": "request-id", "prompt": "task description"}
   ```
3. Worker processes task and returns:
   ```json
   {"type": "result", "id": "request-id", "result": "..."}
   ```

### Implementing the Server

The Hive server implements the MCP tool interface with these tools:

**hive_list** - Returns all instance JSON files from the discovery directory

**hive_delegate** - Accepts:
- `prompt`: Task description (required)
- `target_id`: Specific instance ID (optional)
- `required_tools`: Array of required MCP server names (optional)

Selection logic when `target_id` is omitted:
1. If `required_tools` specified: pick worker with most matching MCP servers
2. Otherwise: pick worker with most available context tokens

### Required Components

1. **Manager** - Handles instance registration, discovery, delegation
2. **WebSocket Server** - Accepts connections, processes delegate messages
3. **MCP Server** - Exposes hive_list and hive_delegate tools
4. **Results Channel** - Returns delegate results to caller

### Key Implementation Details

- Each instance runs an HTTP server with a `/ws` endpoint for WebSocket connections
- Authentication uses the shared hive code as token (sent via WebSocket handshake)
- The handler function processes prompts through the agent's LLM
- Results are returned synchronously over the WebSocket connection

### Starting Instances

Any GoDex instance with `--hive "code"` becomes part of the network. The first instance acts as master by default.

## Examples

### Setting up a worker

1. Start godex with `--hive "secret"`
2. Instance creates JSON file in hive discovery directory
3. Other instances can now delegate tasks to this worker

### Implementing delegation

```go
// Connect to worker
conn, err := net.Dial("tcp", "127.0.0.1:" + workerPort)

// WebSocket handshake with hive code as token
// Send delegate message
req := map[string]string{
    "type":   "delegate",
    "id":     "unique-id",
    "prompt": "your task",
}

// Read result from worker response
```
