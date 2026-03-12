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
```

### 2. Implement Required Interface

```go
type Provider interface {
	Name() string
	Send(ctx context.Context, prompt string) (string, error)
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
