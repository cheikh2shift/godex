package agent

import (
	"context"
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
