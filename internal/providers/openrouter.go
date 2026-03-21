package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cheikh-seck/godex/internal/config"
)

const defaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
const maxRetries = 6
const retryDelay = 35 * time.Second

type openRouterModelInfo struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	ContextLength float64 `json:"context_length"`
}

type openRouterModelsResponse struct {
	Data []openRouterModelInfo `json:"data"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openRouterProvider struct {
	baseURL          string
	model            string
	apiKey           string
	temperature      *float64
	client           *http.Client
	messages         []map[string]interface{}
	tools            []map[string]interface{}
	cancelMu         sync.Mutex
	cancelFunc       context.CancelFunc
	cancelGen        uint64
	contextLimit     int
	promptTokens     int
	completionTokens int
	OnThink          func(string)
	mu               sync.Mutex
}

func init() {
	Register("openrouter", newOpenRouterProvider)
}

func GetOpenRouterContextLimit(model string) (int, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(defaultOpenRouterBaseURL + "/models")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("failed to fetch models: status %d", resp.StatusCode)
	}

	var result openRouterModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	modelLower := strings.ToLower(model)
	for _, m := range result.Data {
		if strings.ToLower(m.ID) == modelLower {
			return int(m.ContextLength), nil
		}
	}

	return 0, fmt.Errorf("model not found: %s", model)
}

func newOpenRouterProvider(cfg *config.Provider) (Provider, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, fmt.Errorf("openrouter provider requires a model")
	}

	baseURL := strings.TrimSpace(cfg.Endpoint)
	if baseURL == "" {
		baseURL = defaultOpenRouterBaseURL
	}

	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		envKey := "OPENROUTER_API_KEY"

		if cfg.APIKeyEnv != "" {
			envKey = cfg.APIKeyEnv
		}

		apiKey = os.Getenv(envKey)
	}

	contextLimit := cfg.ContextLimit

	if limit, err := GetOpenRouterContextLimit(model); err == nil {
		contextLimit = limit
	}

	return &openRouterProvider{
		baseURL:      baseURL,
		model:        model,
		apiKey:       apiKey,
		temperature:  cfg.Temperature,
		contextLimit: contextLimit,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}, nil
}

func (p *openRouterProvider) Send(ctx context.Context, prompt string) (string, error) {
	// Extract the actual user input from the prompt (everything after "User request:" or "User asked:")
	userInput := extractUserInput(prompt)

	p.mu.Lock()
	// Append user message with just the extracted user input (not full prompt)
	p.messages = append(p.messages, map[string]interface{}{
		"role":    "user",
		"content": userInput,
	})
	messages := make([]map[string]interface{}, len(p.messages))
	copy(messages, p.messages)
	p.mu.Unlock()

	reqBody := map[string]interface{}{
		"model":    p.model,
		"messages": messages,
	}

	if len(p.tools) > 0 {
		reqBody["tools"] = p.tools
		reqBody["response_format"] = map[string]interface{}{
			"type": "json_object",
		}
	}

	if p.temperature != nil {
		reqBody["temperature"] = *p.temperature
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			fmt.Printf("\n[OPENROUTER] Waiting %v before retry (attempt %d/%d)...\n", retryDelay, attempt, maxRetries)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(retryDelay):
			}
			fmt.Printf("\n[OPENROUTER] Retrying request (attempt %d/%d)...\n", attempt, maxRetries)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return "", fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
		req.Header.Set("HTTP-Referer", "https://github.com/cheikh2shift/godex")
		req.Header.Set("X-Title", "GoDex")

		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			continue
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		}

		var response struct {
			Choices []struct {
				Message struct {
					Content   string     `json:"content"`
					ToolCalls []toolCall `json:"tool_calls,omitempty"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}

		if err := json.Unmarshal(bodyBytes, &response); err != nil {
			return "", fmt.Errorf("failed to decode response: %w", err)
		}

		if len(response.Choices) == 0 {
			return "", fmt.Errorf("no choices in response")
		}

		choice := response.Choices[0]
		p.mu.Lock()
		p.promptTokens = response.Usage.PromptTokens
		p.completionTokens = response.Usage.CompletionTokens

		// Track assistant response in history
		if len(choice.Message.ToolCalls) > 0 {
			assistantContent := ""
			for _, tc := range choice.Message.ToolCalls {
				assistantContent += fmt.Sprintf("\n[TOOL_CALL: %s | %s]", tc.Function.Name, tc.Function.Arguments)
			}
			p.messages = append(p.messages, map[string]interface{}{
				"role":    "assistant",
				"content": assistantContent,
			})
			p.mu.Unlock()
			return assistantContent, nil
		}
		p.messages = append(p.messages, map[string]interface{}{
			"role":    "assistant",
			"content": choice.Message.Content,
		})
		p.mu.Unlock()

		return choice.Message.Content, nil
	}

	return "", lastErr
}

// extractUserInput extracts the actual user input from the full prompt
// by finding "User request:" or "User asked:" and returning everything after it
func extractUserInput(prompt string) string {
	// Try to find "User request:" first (new format)
	if idx := strings.Index(prompt, "User request:"); idx != -1 {
		return strings.TrimSpace(prompt[idx+len("User request:"):])
	}
	// Fall back to "User asked:" (follow-up format)
	if idx := strings.Index(prompt, "User asked:"); idx != -1 {
		return strings.TrimSpace(prompt[idx+len("User asked:"):])
	}
	// Fall back to returning the whole prompt if markers not found
	return prompt
}

func (p *openRouterProvider) SetThinkCallback(fn func(string)) {
	p.OnThink = fn
}

func (p *openRouterProvider) Cancel() {
	p.cancelMu.Lock()
	defer p.cancelMu.Unlock()
	if p.cancelFunc != nil {
		p.cancelGen++
		p.cancelFunc()
	}
}

func (p *openRouterProvider) Tools() []Tool {
	return nil
}

func (p *openRouterProvider) SetTools(tools []Tool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tools = make([]map[string]interface{}, len(tools))
	for i, t := range tools {
		p.tools[i] = map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.InputSchema,
			},
		}
	}
}

func (p *openRouterProvider) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	return "", fmt.Errorf("tools not supported in openrouter provider")
}

func (p *openRouterProvider) SubmitToolResult(toolCallID, result string) {
	// No-op: not tracking conversation history
}

func (p *openRouterProvider) Close() error {
	return nil
}

func (p *openRouterProvider) ContextLimit() int {
	return p.contextLimit
}

func (p *openRouterProvider) TokenUsage() (int, int) {
	return p.promptTokens, p.completionTokens
}

func (p *openRouterProvider) Reset() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = []map[string]interface{}{}
	p.promptTokens = 0
	p.completionTokens = 0
	return nil
}

func (p *openRouterProvider) SupportsNativeToolCalls() bool {
	return true
}
