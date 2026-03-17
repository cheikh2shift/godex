package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/client"
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

func contains(slice []string, val string) bool {
	for _, v := range slice {
		if strings.Contains(v, val) {
			return true
		}
	}
	return false
}

func listTools(ctx context.Context, mcpClient *client.Client) ([]Tool, error) {
	result, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, err
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

func (m *MCPToolExecutor) RemovePath(ctx context.Context, path string) error {
	found := -1
	for i, p := range m.server.AllowedPaths {
		if p == path {
			found = i
			break
		}
	}
	if found == -1 {
		return fmt.Errorf("path not found: %s", path)
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
	result, err := m.mcpClient.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      name,
			Arguments: arguments,
		},
	})
	if err != nil {
		return "", err
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

func (m *MCPToolExecutor) Close() error {
	if m.mcpClient != nil {
		return m.mcpClient.Close()
	}
	return nil
}
