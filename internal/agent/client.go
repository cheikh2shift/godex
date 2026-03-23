package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/cheikh-seck/godex/internal/config"
	"github.com/cheikh-seck/godex/internal/mcp"
	"github.com/cheikh-seck/godex/internal/providers"
)

var (
	providerCache   = map[string]providers.Provider{}
	providerCacheMu sync.Mutex
	mcpExecutors    = map[string]*mcp.MCPToolExecutor{}
	mcpExecutorsMu  sync.Mutex
)

type SendResult struct {
	Content   string
	ToolCalls []ToolCallResult
}

type ToolCallResult struct {
	ID        string
	Name      string
	Arguments string
}

func (t *ToolCallResult) ParseArguments() (map[string]interface{}, error) {
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(t.Arguments), &result); err != nil {
		return nil, err
	}
	return result, nil
}

// SendPrompt dispatches the prompt to the configured provider via the registry.
func SendPrompt(ctx context.Context, provider *config.Provider, prompt string) (string, error) {
	if provider == nil {
		return "", fmt.Errorf("no provider provided")
	}

	p, err := GetProvider(provider)
	if err != nil {
		return "", err
	}

	resp, err := p.Send(ctx, prompt)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp), nil
}

// SendPromptWithThink dispatches the prompt with a thinking callback.
func SendPromptWithThink(ctx context.Context, provider *config.Provider, prompt string, onThink func(string)) (string, error) {
	if provider == nil {
		return "", fmt.Errorf("no provider provided")
	}

	p, err := GetProvider(provider)
	if err != nil {
		return "", err
	}

	p.SetThinkCallback(onThink)
	defer p.SetThinkCallback(nil)

	resp, err := p.Send(ctx, prompt)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp), nil
}

func CancelPrompt(provider *config.Provider) {
	if provider == nil {
		return
	}
	p, err := GetProvider(provider)
	if err != nil {
		return
	}
	p.Cancel()

	mcpExecutorsMu.Lock()
	key := cacheKey(provider)
	if executor, ok := mcpExecutors[key]; ok {
		delete(mcpExecutors, key)
		executor.Close()
	}
	mcpExecutorsMu.Unlock()
}

// CallTool executes an MCP tool call.
func CallTool(ctx context.Context, provider *config.Provider, toolName string, args map[string]interface{}) (string, error) {
	if provider == nil {
		return "", fmt.Errorf("no provider provided")
	}

	p, err := GetProvider(provider)
	if err != nil {
		return "", err
	}

	return p.CallTool(ctx, toolName, args)
}

// GetTools returns the available MCP tools for a provider.
func GetTools(provider *config.Provider) []providers.Tool {
	if provider == nil {
		return nil
	}

	p, err := GetProvider(provider)
	if err != nil {
		return nil
	}

	return p.Tools()
}

func SetProviderTools(provider *config.Provider, tools []providers.Tool) {
	p, err := GetProvider(provider)
	if err != nil {
		return
	}
	if pt, ok := p.(interface{ SetTools([]providers.Tool) }); ok {
		pt.SetTools(tools)
	}
}

func SubmitProviderToolResult(provider *config.Provider, toolCallID, result string) {
	p, err := GetProvider(provider)
	if err != nil {
		return
	}
	if pt, ok := p.(interface{ SubmitToolResult(string, string) }); ok {
		pt.SubmitToolResult(toolCallID, result)
	}
}

func RegisterMCPExecutor(provider *config.Provider, executor *mcp.MCPToolExecutor) {
	if provider == nil || executor == nil {
		return
	}
	key := cacheKey(provider)
	mcpExecutorsMu.Lock()
	mcpExecutors[key] = executor
	mcpExecutorsMu.Unlock()
}

func CloseProvider(cfg *config.Provider) {
	key := cacheKey(cfg)
	providerCacheMu.Lock()
	p, ok := providerCache[key]
	if ok {
		delete(providerCache, key)
	}
	providerCacheMu.Unlock()
	if ok {
		_ = p.Close()
	}

	mcpExecutorsMu.Lock()
	if executor, ok := mcpExecutors[key]; ok {
		delete(mcpExecutors, key)
		_ = executor.Close()
	}
	mcpExecutorsMu.Unlock()
}

func GetProvider(cfg *config.Provider) (providers.Provider, error) {
	key := cacheKey(cfg)
	providerCacheMu.Lock()
	defer providerCacheMu.Unlock()
	if p, ok := providerCache[key]; ok {
		return p, nil
	}
	p, err := providers.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	providerCache[key] = p
	return p, nil
}

func cacheKey(cfg *config.Provider) string {
	return fmt.Sprintf("%s|%s|%s|%s", cfg.Type, cfg.Name, cfg.Model, cfg.Endpoint)
}
