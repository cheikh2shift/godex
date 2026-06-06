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

type MCPToolExecutor struct { //betteralign:ignore
	lastRequestID int64 // Must be first for 64-bit alignment on 32-bit platforms - it's accessed atomically
	mcpClient     *client.Client
	serverName    string
	workingDir    string

	server atomic.Pointer[MCPServer]
	tools  []Tool

	mu          sync.RWMutex
	requestIDMu sync.Mutex
}

func NewMCPServer(ctx context.Context, server MCPServer, workingDir string) (*MCPToolExecutor, error) {
	executor := &MCPToolExecutor{
		serverName: server.Name,
		workingDir: workingDir,
	}
	serverCopy := server
	executor.server.Store(&serverCopy)

	if err := executor.connect(ctx); err != nil {
		return nil, err
	}

	return executor, nil
}

func (m *MCPToolExecutor) connect(ctx context.Context) error {
	serverPtr := m.server.Load()
	if serverPtr == nil {
		return fmt.Errorf("server not initialized")
	}
	server := *serverPtr
	m.mu.Lock()
	defer m.mu.Unlock()
	mcpClient, tools, err := connect(ctx, server, buildArgs(server, m.workingDir))
	if err != nil {
		return err
	}
	m.mcpClient = mcpClient
	m.tools = tools
	return nil
}

func connect(ctx context.Context, server MCPServer, args []string) (*client.Client, []Tool, error) {
	mcpClient, err := client.NewStdioMCPClient(server.Command, server.Env, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create MCP client for %s: %w", server.Name, err)
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
		return nil, nil, fmt.Errorf("failed to initialize MCP client for %s: %w", server.Name, err)
	}

	tools, err := listTools(ctx, mcpClient)
	if err != nil {
		_ = mcpClient.Close()
		return nil, nil, fmt.Errorf("failed to list tools for %s: %w", server.Name, err)
	}

	return mcpClient, tools, nil
}

// pingClient checks if the MCP client is still responsive
func pingClient(ctx context.Context, client client.MCPClient) error {
	// Create a context with 2 second deadline
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	return client.Ping(pingCtx)
}

// ensureConnected pings the client and reconnects if needed
func (m *MCPToolExecutor) ensureConnected(ctx context.Context) error {
	m.mu.Lock()
	client := m.mcpClient
	wd := m.workingDir
	if client != nil {
		if pingClient(ctx, client) == nil {
			m.mu.Unlock()
			return nil
		}
		// Ping failed, close and reconnect
		_ = client.Close()
		m.mcpClient = nil
	}
	// Do not hold the lock till connecting
	m.mu.Unlock()

	serverPtr := m.server.Load()
	if serverPtr == nil {
		return fmt.Errorf("server not initialized")
	}
	server := *serverPtr
	client, tools, err := connect(ctx, server, buildArgs(server, wd))
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Guard against another goroutine having reconnected while we were unlocked
	if m.mcpClient != nil { // assume it's ok
		_ = client.Close()
		return nil
	}

	m.mcpClient, m.tools = client, tools
	return nil
}

func buildArgs(server MCPServer, workingDir string) []string {
	paths := server.AllowedPaths
	if len(paths) == 0 {
		paths = []string{workingDir}
	}

	if server.Name == "filesystem" || contains(server.Args, "server-filesystem") {
		args := []string{"-y", "@modelcontextprotocol/server-filesystem"}
		args = append(args, paths...)
		return args
	}

	if len(server.Args) > 0 {
		return server.Args
	}

	return nil
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
	serverPtr := m.server.Load()
	if serverPtr == nil {
		return ""
	}
	return serverPtr.Name
}

func (m *MCPToolExecutor) Tools() []Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tools
}

func (m *MCPToolExecutor) AddPath(ctx context.Context, path string) error {
	serverPtr := m.server.Load()
	if serverPtr == nil {
		return fmt.Errorf("server not initialized")
	}
	server := *serverPtr
	if func() bool {
		for _, p := range server.AllowedPaths {
			if p == path {
				return true
			}
		}

		server.AllowedPaths = append(server.AllowedPaths, path)
		m.server.Store(&server)

		m.mu.Lock()
		defer m.mu.Unlock()
		_ = m.mcpClient.Close()
		m.mcpClient = nil
		return false
	}() {
		return nil
	}

	if err := m.ensureConnected(ctx); err != nil {
		return fmt.Errorf("failed to reconnect with new path: %w", err)
	}

	return nil
}

func (m *MCPToolExecutor) AddURL(ctx context.Context, url string) error {
	return m.AddPath(ctx, url)
}

func (m *MCPToolExecutor) TempAddPath(path string) {
	serverPtr := m.server.Load()
	if serverPtr == nil {
		return
	}
	server := *serverPtr
	for _, p := range server.AllowedPaths {
		if p == path {
			return
		}
	}

	server.AllowedPaths = append(server.AllowedPaths, path)
	m.server.Store(&server)
}

func (m *MCPToolExecutor) RemovePath(ctx context.Context, path string) error {
	if func() bool {
		serverPtr := m.server.Load()
		if serverPtr == nil {
			return true
		}
		server := *serverPtr
		found := -1
		for i, p := range server.AllowedPaths {
			if p == path {
				found = i
				break
			}
		}

		if found == -1 {
			return true
		}

		server.AllowedPaths = append(server.AllowedPaths[:found], server.AllowedPaths[found+1:]...)
		m.server.Store(&server)

		m.mu.Lock()
		defer m.mu.Unlock()
		_ = m.mcpClient.Close()
		m.mcpClient = nil
		return false
	}() {
		return nil
	}

	if err := m.ensureConnected(ctx); err != nil {
		return fmt.Errorf("failed to reconnect after removing path: %w", err)
	}

	return nil
}

func (m *MCPToolExecutor) RemoveURL(ctx context.Context, url string) error {
	return m.RemovePath(ctx, url)
}

func (m *MCPToolExecutor) AllowedPaths() []string {
	serverPtr := m.server.Load()
	if serverPtr != nil {
		paths := serverPtr.AllowedPaths
		if len(paths) != 0 {
			return paths
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return []string{m.workingDir}
}

func (m *MCPToolExecutor) CallTool(ctx context.Context, name string, arguments map[string]any) (string, error) {
	return m.callToolWithRetry(ctx, name, arguments, 0)
}

func (m *MCPToolExecutor) callToolWithRetry(ctx context.Context, name string, arguments map[string]any, attempt int) (string, error) {
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
			m.mu.Lock()
			_ = m.mcpClient.Close()
			m.mcpClient = nil
			m.mu.Unlock()
			if reconnectErr := m.connect(ctx); reconnectErr == nil {
				return m.callToolWithRetry(ctx, name, arguments, attempt+1)
			}
		}
		return "", err
	}

	return result, nil
}

func (m *MCPToolExecutor) callToolWithCancellation(ctx context.Context, requestID int64, name string, arguments map[string]any) (string, error) {
	var t transport.Interface
	m.mu.RLock()
	if client := m.mcpClient; client != nil {
		t = client.GetTransport()
	}
	m.mu.RUnlock()
	if t == nil {
		return "", fmt.Errorf("nil client")
	}

	request := transport.JSONRPCRequest{
		JSONRPC: mcp.JSONRPC_VERSION,
		ID:      mcp.NewRequestId(requestID),
		Method:  "tools/call",
		Params: map[string]any{
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
		sendCancelledNotification(context.Background(), t, requestID, ctx.Err().Error())
		return "", ctx.Err()
	case response := <-resultCh:
		return m.parseToolResponse(response)
	case err := <-errCh:
		return "", err
	}
}

func sendCancelledNotification(ctx context.Context, t transport.Interface, requestID int64, reason string) {
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
	m.mu.Lock()
	client := m.mcpClient
	m.mcpClient = nil
	m.mu.Unlock()
	if client != nil {
		return closeWithTimeout(client, 2*time.Second)
	}
	return nil
}

func closeWithTimeout(client *client.Client, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		done <- client.Close()
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return nil
	}
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
	serverPtr := m.server.Load()
	if serverPtr == nil {
		return ""
	}
	server := *serverPtr
	var b strings.Builder
	b.WriteString("MCP Server: " + server.Name + "\n")
	b.WriteString("Command: " + server.Command + "\n")
	b.WriteString("Args: " + strings.Join(server.Args, " ") + "\n")
	b.WriteString("Allowed Paths: " + strings.Join(server.AllowedPaths, ", ") + "\n")
	return b.String()
}
