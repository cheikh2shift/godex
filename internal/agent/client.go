package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

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

type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]interface{}
}

type ToolCallResponse struct {
	ID     string
	Result string
	Err    error
}

func LaunchToolsInParallel(ctx context.Context, provider *config.Provider, toolCalls []ToolCall) []ToolCallResponse {
	if len(toolCalls) == 0 {
		return nil
	}

	providerName := "unknown"
	if provider != nil {
		providerName = provider.Name
	}

	fmt.Printf("\n")
	fmt.Printf("  %s  %s  %s\n", "╔═══════════════════════════════════════════════════╗", "\033[1;36m🚀 LAUNCHING TOOLS IN PARALLEL\033[0m", "")
	fmt.Printf("  %s  %s  %s\n", "║", fmt.Sprintf("\033[1;33mProvider:\033[0m %s", providerName), "                    ║")
	fmt.Printf("  %s  %s  %s\n", "║", fmt.Sprintf("\033[1;33mTotal:\033[0m %d tools", len(toolCalls)), "                                  ║")
	fmt.Printf("  %s  %s  %s\n", "╚═══════════════════════════════════════════════════╝", "                           ", "")

	cyan := "\033[1;36m"
	green := "\033[1;32m"
	yellow := "\033[1;33m"
	reset := "\033[0m"

	for i, tc := range toolCalls {
		arrow := "➤"
		argsJSON, _ := json.Marshal(tc.Arguments)
		fmt.Printf("  %s %s[%d]%s %s%s%s %s%s%s\n",
			arrow, cyan, i+1, reset,
			green, tc.Name, reset,
			yellow, string(argsJSON), reset)
	}
	fmt.Println()

	var wg sync.WaitGroup
	responses := make([]ToolCallResponse, len(toolCalls))
	resultCh := make(chan struct {
		index    int
		response ToolCallResponse
	}, len(toolCalls))

	startTime := time.Now()

	for i, tc := range toolCalls {
		wg.Add(1)
		go func(index int, call ToolCall) {
			defer wg.Done()

			select {
			case resultCh <- struct {
				index    int
				response ToolCallResponse
			}{index, ToolCallResponse{ID: call.ID, Result: "⏳ running..."}}:
			default:
			}

			select {
			case <-ctx.Done():
				resultCh <- struct {
					index    int
					response ToolCallResponse
				}{index, ToolCallResponse{ID: call.ID, Err: ctx.Err()}}
				return
			default:
			}

			var result string
			var err error

			if provider != nil {
				result, err = CallTool(ctx, provider, call.Name, call.Arguments)
			} else {
				err = fmt.Errorf("no provider configured")
			}

			response := ToolCallResponse{
				ID:     call.ID,
				Result: result,
				Err:    err,
			}

			select {
			case resultCh <- struct {
				index    int
				response ToolCallResponse
			}{index, response}:
			default:
			}
		}(i, tc)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	for result := range resultCh {
		responses[result.index] = result.response
	}

	elapsed := time.Since(startTime)

	fmt.Printf("\n")
	fmt.Printf("  %s  %s  %s\n", "╔═══════════════════════════════════════════════════╗", "\033[1;32m✓ TOOLS COMPLETED\033[0m", "")
	fmt.Printf("  %s  %s  %s\n", "║", fmt.Sprintf("\033[1;33mTime:\033[0m %v", elapsed), "                                        ║")
	fmt.Printf("  %s  %s  %s\n", "╚═══════════════════════════════════════════════════╝", "                           ", "")

	for i, resp := range responses {
		statusIcon := "✓"
		if resp.Err != nil {
			statusIcon = "✗"
		}
		statusColor := green
		if resp.Err != nil {
			statusColor = "\033[1;31m"
		}

		resultPreview := resp.Result
		if len(resultPreview) > 60 {
			resultPreview = resultPreview[:60] + "..."
		}

		name := toolCalls[i].Name
		if len(name) > 25 {
			name = name[:25] + "..."
		}

		fmt.Printf("  %s %s[%d]%s %s%-25s%s %s%s%s\n",
			statusIcon,
			cyan, i+1, reset,
			statusColor, name, reset,
			yellow, resultPreview, reset)
	}
	fmt.Println()

	if ctx.Err() != nil {
		fmt.Printf("  ⚠️  %s\n", ctx.Err())
	}

	return responses
}
