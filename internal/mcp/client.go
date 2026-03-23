package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

type MCPServer struct {
	Name         string   `yaml:"name"`
	Command      string   `yaml:"command"`
	Args         []string `yaml:"args,omitempty"`
	Env          []string `yaml:"env,omitempty"`
	Transport    string   `yaml:"transport,omitempty"`
	AllowedPaths []string `yaml:"allowed_paths,omitempty"`
}

type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

type MCPToolExecutor struct {
	serverName string
	server     MCPServer
	mcpClient  *client.Client
	tools      []Tool
	workingDir string

	lastRequestID int64
	requestIDMu   sync.Mutex
}

func NewMCPServer(ctx context.Context, server MCPServer, workingDir string) (*MCPToolExecutor, error) {
	executor := &MCPToolExecutor{
		serverName: server.Name,
		server:     server,
		workingDir: workingDir,
	}

	if err := executor.connect(ctx); err != nil {
		return nil, err
	}

	return executor, nil
}

func (m *MCPToolExecutor) connect(ctx context.Context) error {
	args := m.buildArgs()

	mcpClient, err := client.NewStdioMCPClient(m.server.Command, m.server.Env, args...)
	if err != nil {
		return fmt.Errorf("failed to create MCP client for %s: %w", m.server.Name, err)
	}

	_, err = mcpClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: "2024-11-05",
			ClientInfo: mcp.Implementation{
				Name:    "godex",
				Version: "1.0.0",
			},
		},
	})
	if err != nil {
		_ = mcpClient.Close()
		return fmt.Errorf("failed to initialize MCP client for %s: %w", m.server.Name, err)
	}

	tools, err := listTools(ctx, mcpClient)
	if err != nil {
		_ = mcpClient.Close()
		return fmt.Errorf("failed to list tools for %s: %w", m.server.Name, err)
	}

	m.mcpClient = mcpClient
	m.tools = tools
	return nil
}

// ping checks if the MCP client is still responsive
func (m *MCPToolExecutor) ping(ctx context.Context) error {
	if m.mcpClient == nil {
		return fmt.Errorf("client is nil")
	}

	// Create a context with 2 second deadline
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	return m.mcpClient.Ping(pingCtx)
}

// ensureConnected pings the client and reconnects if needed
func (m *MCPToolExecutor) ensureConnected(ctx context.Context) error {
	if m.mcpClient == nil {
		return m.connect(ctx)
	}

	if err := m.ping(ctx); err != nil {
		// Ping failed, close and reconnect
		_ = m.mcpClient.Close()
		m.mcpClient = nil

		if err := m.connect(ctx); err != nil {
			return fmt.Errorf("failed to reconnect after ping failure: %w", err)
		}
	}
	return nil
}

func (m *MCPToolExecutor) buildArgs() []string {
	paths := m.server.AllowedPaths
	if len(paths) == 0 {
		paths = []string{m.workingDir}
	}

	if m.server.Name == "filesystem" || contains(m.server.Args, "server-filesystem") {
		args := []string{"-y", "@modelcontextprotocol/server-filesystem"}
		args = append(args, paths...)
		return args
	}

	if len(m.server.Args) > 0 {
		return m.server.Args
	}

	return nil
}

func (m *MCPToolExecutor) buildTransport() string {
	if m.server.Transport == "stdio" {
		return "stdio"
	}
	return "stdio"
}

func listTools(ctx context.Context, mcpClient *client.Client) ([]Tool, error) {
	result, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list tools: %w", err)
	}

	var tools []Tool
	for _, t := range result.Tools {
		inputSchema, _ := json.Marshal(t.InputSchema)
		tools = append(tools, Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: inputSchema,
		})
	}

	return tools, nil
}

func (m *MCPToolExecutor) Name() string {
	return m.server.Name
}

func (m *MCPToolExecutor) Tools() []Tool {
	return m.tools
}

func (m *MCPToolExecutor) AddPath(ctx context.Context, path string) error {
	for _, p := range m.server.AllowedPaths {
		if p == path {
			return nil
		}
	}

	m.server.AllowedPaths = append(m.server.AllowedPaths, path)

	_ = m.mcpClient.Close()

	if err := m.connect(ctx); err != nil {
		return fmt.Errorf("failed to reconnect with new path: %w", err)
	}

	return nil
}

func (m *MCPToolExecutor) AddURL(ctx context.Context, url string) error {
	return m.AddPath(ctx, url)
}

func (m *MCPToolExecutor) TempAddPath(path string) {
	for _, p := range m.server.AllowedPaths {
		if p == path {
			return
		}
	}

	m.server.AllowedPaths = append(m.server.AllowedPaths, path)
}

func (m *MCPToolExecutor) RemovePath(ctx context.Context, path string) error {
	found := -1
	for i, p := range m.server.AllowedPaths {
		if p == path {
			found = i
			break
		}
	}

	if found == -1 {
		return nil
	}

	m.server.AllowedPaths = append(m.server.AllowedPaths[:found], m.server.AllowedPaths[found+1:]...)

	_ = m.mcpClient.Close()

	if err := m.connect(ctx); err != nil {
		return fmt.Errorf("failed to reconnect after removing path: %w", err)
	}

	return nil
}

func (m *MCPToolExecutor) RemoveURL(ctx context.Context, url string) error {
	return m.RemovePath(ctx, url)
}

func (m *MCPToolExecutor) AllowedPaths() []string {
	paths := m.server.AllowedPaths
	if len(paths) == 0 {
		return []string{m.workingDir}
	}
	return paths
}

func (m *MCPToolExecutor) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (string, error) {
	return m.callToolWithRetry(ctx, name, arguments, 0)
}

func (m *MCPToolExecutor) callToolWithRetry(ctx context.Context, name string, arguments map[string]interface{}, attempt int) (string, error) {
	maxRetries := 1

	if attempt >= maxRetries {
		return "", fmt.Errorf("tool call failed after %d retries", maxRetries)
	}

	if err := m.ensureConnected(ctx); err != nil {
		return "", fmt.Errorf("failed to ensure client is connected: %w", err)
	}

	requestID := atomic.AddInt64(&m.lastRequestID, 1)

	result, err := m.callToolWithCancellation(ctx, requestID, name, arguments)
	if err != nil {
		if ctx.Err() == context.Canceled || ctx.Err() == context.DeadlineExceeded {
			return "", ctx.Err()
		}

		if isRetryableError(err.Error()) && attempt < maxRetries {
			_ = m.mcpClient.Close()
			m.mcpClient = nil
			if reconnectErr := m.connect(ctx); reconnectErr == nil {
				return m.callToolWithRetry(ctx, name, arguments, attempt+1)
			}
		}
		return "", err
	}

	return result, nil
}

func (m *MCPToolExecutor) callToolWithCancellation(ctx context.Context, requestID int64, name string, arguments map[string]interface{}) (string, error) {
	t := m.mcpClient.GetTransport()

	request := transport.JSONRPCRequest{
		JSONRPC: mcp.JSONRPC_VERSION,
		ID:      mcp.NewRequestId(requestID),
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name":      name,
			"arguments": arguments,
		},
	}

	resultCh := make(chan *transport.JSONRPCResponse, 1)
	errCh := make(chan error, 1)

	go func() {
		response, err := t.SendRequest(ctx, request)
		if err != nil {
			errCh <- err
		} else {
			resultCh <- response
		}
	}()

	select {
	case <-ctx.Done():
		m.sendCancelledNotification(context.Background(), requestID, ctx.Err().Error())
		return "", ctx.Err()
	case response := <-resultCh:
		return m.parseToolResponse(response)
	case err := <-errCh:
		return "", err
	}
}

func (m *MCPToolExecutor) sendCancelledNotification(ctx context.Context, requestID int64, reason string) {
	t := m.mcpClient.GetTransport()

	notification := mcp.JSONRPCNotification{
		JSONRPC: mcp.JSONRPC_VERSION,
		Notification: mcp.Notification{
			Method: "notifications/cancelled",
		},
	}
	if notification.Params.AdditionalFields == nil {
		notification.Params.AdditionalFields = make(map[string]any)
	}
	notification.Params.AdditionalFields["requestId"] = requestID
	notification.Params.AdditionalFields["reason"] = reason

	_ = t.SendNotification(ctx, notification)
}

func (m *MCPToolExecutor) parseToolResponse(response *transport.JSONRPCResponse) (string, error) {
	if response.Error != nil {
		return "", response.Error.AsError()
	}

	var result mcp.CallToolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return "", fmt.Errorf("failed to parse tool result: %w", err)
	}

	if result.IsError {
		var errMsg string
		for _, content := range result.Content {
			if textContent, ok := content.(mcp.TextContent); ok {
				errMsg += textContent.Text
			}
		}
		return "", fmt.Errorf("tool error: %s", errMsg)
	}

	var output strings.Builder
	for _, content := range result.Content {
		if textContent, ok := content.(mcp.TextContent); ok {
			output.WriteString(textContent.Text)
		}
	}
	return output.String(), nil
}

func isRetryableError(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	retryablePatterns := []string{
		"invalid response format",
		"parse error",
		"unexpected token",
		"json",
	}
	for _, pattern := range retryablePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func (m *MCPToolExecutor) Close() error {
	if m.mcpClient != nil {
		return m.mcpClient.Close()
	}
	return nil
}

func contains(slice []string, val string) bool {
	for _, v := range slice {
		if strings.Contains(v, val) {
			return true
		}
	}
	return false
}

func (m *MCPToolExecutor) GetContext() string {
	var b strings.Builder
	b.WriteString("MCP Server: " + m.server.Name + "\n")
	b.WriteString("Command: " + m.server.Command + "\n")
	b.WriteString("Args: " + strings.Join(m.server.Args, " ") + "\n")
	b.WriteString("Allowed Paths: " + strings.Join(m.server.AllowedPaths, ", ") + "\n")
	return b.String()
}
