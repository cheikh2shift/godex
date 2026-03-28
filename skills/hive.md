---
name: godex-hive
description: Set up a GoDex Hive multi-agent network. Use when creating a network of GoDex agents that can delegate tasks between each other.
---

# GoDex Hive

## Instructions

Hive is a peer-to-peer multi-agent system where GoDex instances delegate tasks to each other. Any instance can delegate work to any other instance.

### How Hive Works

- **Peers**: All instances are equal - any can delegate to any other
- **Discovery**: Instances find each other via JSON files in a shared directory
- **Communication**: WebSocket over localhost (127.0.0.1)
- **Authentication**: Shared secret (hive code) validates connections

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
  "pid": 12345,
  "max_tokens": 32000
}
```

Instances discover peers by reading this directory.

### Authentication

Each instance has a shared secret (hive code). This is used to:

1. **Compute directory path**: `hash(hive_code) = codeHash` - determines where instance files are stored
2. **WebSocket handshake**: Token sent during connection, validated by peer

WebSocket handshake example:
```
GET /ws HTTP/1.1
Host: localhost:{port}
Authorization: {hive_code}
Upgrade: websocket
```

Peers validate the token by computing the same hash and comparing.

### Delegation Protocol

1. **Connect** to peer via WebSocket at `ws://127.0.0.1:{port}/ws`
2. **Authenticate** with hive code as Bearer token
3. **Send** delegate message:
   ```json
   {"type": "delegate", "id": "request-id", "prompt": "task description"}
   ```
4. **Receive** result:
   ```json
   {"type": "result", "id": "request-id", "result": "...", "error": ""}
   ```

### Required Components

1. **Manager** - Handles instance registration, discovery, delegation
2. **WebSocket Server** - Accepts connections, validates auth, processes messages
3. **MCP Server** - Exposes hive_list and hive_delegate tools
4. **Results Channel** - Returns delegate results to caller

### Key Implementation Details

1. **Starting the Server**
   - Listen on random available port: `net.Listen("tcp", "127.0.0.1:0")`
   - Register instance by writing JSON file to discovery directory

2. **WebSocket Handler**
   - Validate Authorization header against local hive code
   - Read delegate message, process via LLM handler
   - Return result or error

3. **Delegation (Async)**
   - Connect to target peer
   - Send delegate message
   - Return immediately (non-blocking)
   - Listen on results channel for completion

4. **Instance Selection**
   When `target_id` is omitted:
   - If `required_tools` specified: pick peer with most matching MCP servers
   - Otherwise: pick peer with most available context tokens

### MCP Tools

**hive_list** - Returns all instance JSON files from discovery directory

**hive_delegate** - Accepts:
- `prompt`: Task description (required)
- `target_id`: Specific instance ID (optional)
- `required_tools`: Array of required MCP server names (optional)

### Code Structure

```
internal/hive/
├── manager.go      # Manager, delegation, discovery
├── ws.go           # WebSocket read/write, handshake
├── names.go        # Random human names for instances
└── ...

internal/mcp/
└── hive.go         # MCP server with hive_list, hive_delegate
```

## Examples

### Implementing delegation

```go
// Connect to peer
conn, err := net.Dial("tcp", "127.0.0.1:"+peerPort)
if err != nil {
    return err
}

// WebSocket handshake with hive code as token
// Send: GET /ws ... Authorization: {hive_code}

// Send delegate message
req := wireMessage{
    Type:   "delegate",
    ID:     uuid.New(),
    Prompt: "your task",
}
payload, _ := json.Marshal(req)
writeWSMessage(conn, payload)

// Read result
respRaw, _ := readWSMessage(conn)
var resp wireMessage
json.Unmarshal(respRaw, &resp)

// resp.Result contains the answer
// resp.Error contains any error message
```

### Starting a peer

```go
manager, err := hive.NewManager(
    "your-secret-code",   // hive code (shared secret)
    "./data",             // base directory
    "nemotron",           // model name
    32000,                // max tokens
    []string{"filesystem", "bash"},  // available MCP servers
    statusCh,             // channel for status updates
    handlerFn,            // function to process prompts via LLM
)
```

### Selecting best peer

```go
// In hive_delegate tool
instances, _ := manager.Instances()

target := selectBestInstance(instances, selfID, requiredTools)

manager.DelegateAsync(ctx, target.ID, prompt)
```
