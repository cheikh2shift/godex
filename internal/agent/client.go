package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/cheikh2shift/godex/internal/config"
	"github.com/cheikh2shift/godex/internal/mcp"
	"github.com/cheikh2shift/godex/internal/providers"
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

func CancelPrompt(provider *config.Provider) {
	if provider == nil {
		return
	}
	key := cacheKey(provider)
	providerCacheMu.Lock()
	p, ok := providerCache[key]
	providerCacheMu.Unlock()
	if !ok {
		return
	}
	p.Cancel()

	mcpExecutorsMu.Lock()
	key = cacheKey(provider)
	if executor, ok := mcpExecutors[key]; ok {
		delete(mcpExecutors, key)
		executor.Close()
	}
	mcpExecutorsMu.Unlock()
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
	if cfg == nil {
		return
	}
	providerCacheMu.Lock()
	var p providers.Provider
	var ok bool
	for key, provider := range providerCache {
		if strings.HasPrefix(key, cfg.Type+"|"+cfg.Name+"|") {
			p = provider
			delete(providerCache, key)
			ok = true
			break
		}
	}
	providerCacheMu.Unlock()
	if ok {
		log.Printf("[agent] Closing provider for %s/%s", cfg.Type, cfg.Name)
		_ = p.Close()
	}

	mcpExecutorsMu.Lock()
	var executor *mcp.MCPToolExecutor
	for key, ex := range mcpExecutors {
		if strings.HasPrefix(key, cfg.Type+"|"+cfg.Name+"|") {
			executor = ex
			delete(mcpExecutors, key)
			break
		}
	}
	mcpExecutorsMu.Unlock()
	if executor != nil {
		_ = executor.Close()
	}
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

type ProviderConfig struct {
	Type  string
	Name  string
	Model string
}

func GetProviderFromConfig(cfg *ProviderConfig) (providers.Provider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	provider := &config.Provider{
		Type:  cfg.Type,
		Name:  cfg.Name,
		Model: cfg.Model,
	}
	return GetProvider(provider)
}
