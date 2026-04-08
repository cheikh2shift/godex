# Developer Guide

## Adding New MCP Servers

### Inline Go MCP Server

Create a new file in `internal/mcp/` (e.g., `mydb.go`):

```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

type MyDBServer struct {
	allowedPaths []string
	tools        []Tool
}

func NewMyDBServer(allowedPaths []string) *MyDBServer {
	return &MyDBServer{
		allowedPaths: allowedPaths,
		tools: []Tool{
			{
				Name:        "query",
				Description: "Execute a database query",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"sql":{"type":"string"}},"required":["sql"]}`),
			},
		},
	}
}

func (s *MyDBServer) Tools() []Tool {
	return s.tools
}

func (s *MyDBServer) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	switch name {
	case "query":
		return s.runQuery(args)
	default:
		return "", fmt.Errorf("unknown tool: %s", name")
	}
}

func (s *MyDBServer) runQuery(args map[string]interface{}) (string, error) {
	sql, ok := args["sql"].(string)
	if !ok {
		return "", fmt.Errorf("sql is required")
	}
	// Implement your logic here
	return "result", nil
}

func (s *MyDBServer) AllowedPaths() []string {
	return s.allowedPaths
}

func (s *MyDBServer) AddPath(ctx context.Context, path string) error {
	s.allowedPaths = append(s.allowedPaths, path)
	return nil
}

func (s *MyDBServer) Close() error {
	return nil
}
```

### Register the MCP Server

In `cmd/godex/main.go`, add your server in the `initMCPServers` function:

```go
} else if serverConfig.Name == "mydb" {
	paths = uniqueStrings(paths)
	server = mcp.NewMyDBServer(paths)
}
```

### External MCP Server

Add to your `providers.yaml`:

```yaml
mcp_servers:
  - name: myserver
    command: npx
    args:
      - "-y"
      - "@some/mcp-server"
```

## Adding New Providers

### 1. Create Provider Implementation

Create `internal/providers/myprovider.go`:

```go
package providers

import (
	"context"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/cheikh-seck/godex/internal/config"
)

type myProvider struct {
	model       string
	temperature *float64
	client      *http.Client
	messages    []map[string]string
	mu          sync.Mutex
	OnThink     func(string)
	cancelFunc  context.CancelFunc
}

func init() {
	Register("myprovider", newMyProvider)
}

func newMyProvider(cfg *config.Provider) (Provider, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "default-model"
	}

	return &myProvider{
		model:       model,
		temperature: cfg.Temperature,
		client:      &http.Client{},
		messages:    []map[string]string{},
	}, nil
}

func (p *myProvider) Name() string { return "myprovider" }

func (p *myProvider) Send(ctx context.Context, prompt string) (string, error) {
	p.mu.Lock()
	p.messages = append(p.messages, map[string]string{"role": "user", "content": prompt})
	p.mu.Unlock()

	// Implement API call here
	// Return response string

	p.mu.Lock()
	p.messages = append(p.messages, map[string]string{"role": "assistant", "content": response})
	p.mu.Unlock()

	return response, nil
}

func (p *myProvider) Cancel() {
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
}

func (p *myProvider) SetMessages(messages []Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = []map[string]string{}
	for _, msg := range messages {
		if msg.Role == "" || msg.Content == "" {
			continue
		}
		p.messages = append(p.messages, map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}
	return nil
}

func (p *myProvider) AppendMessages(messages []Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, msg := range messages {
		if msg.Role == "" || msg.Content == "" {
			continue
		}
		p.messages = append(p.messages, map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}
	return nil
}
```

### 2. Implement Required Interface

```go
type Provider interface {
	Send(ctx context.Context, prompt string) (string, error)
	SetThinkCallback(func(string))
	Cancel()
	Tools() []Tool
	CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error)
	Close() error
	ContextLimit() int
	TokenUsage() (input int, output int)
	Reset() error
	SetMessages(messages []Message) error
	AppendMessages(messages []Message) error
	SupportsNativeToolCalls() bool
}
```

```go
type Message struct {
	Role    string
	Content string
}
```

## Project Structure

```
cmd/godex/
  main.go           # CLI entry point
internal/
  agent/            # Agent logic
  config/           # Config loading
  context/          # Context utilities
  mcp/              # MCP server implementations
    bash.go
    filesystem.go
    webscraper.go
  providers/        # LLM providers
    gemini.go
    ollama.go
  wizard/           # Config wizard
```

## Building

```bash
go build -o godex ./cmd/godex
```

## OpenAI Provider

### WebSocket Support

The OpenAI provider uses WebSocket by default for the `responses` API, with automatic HTTP fallback if WebSocket fails.

**Struct fields:**

```go
type openaiProvider struct {
    client           *http.Client
    cfg              *config.Provider
    model            string
    temperature      *float64
    mu               sync.Mutex
    cancelFunc       context.CancelFunc
    cancelGen        uint64
    contextLimit     int
    promptTokens     int
    completionTokens int
    messages         []Message
    statusCh         chan<- string
    baseURL          string
    thinkCallback    func(string)
    isCodex          bool
    apiKey           string
    clientID         string
    wsConn           *websocket.Conn      // WebSocket connection
    wsMu             sync.Mutex           // Mutex for WebSocket
    responseID       string                // previous_response_id for incremental对话
}
```

**Endpoints:**

- Codex: `wss://chatgpt.com/backend-api/responses`
- Platform: `wss://api.openai.com/v1/responses`

**HTTP fallback:** If WebSocket connection fails, the provider automatically falls back to HTTP.
