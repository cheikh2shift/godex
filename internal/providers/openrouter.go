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

type openRouterProvider struct {
	baseURL          string
	model            string
	apiKey           string
	temperature      *float64
	client           *http.Client
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
		apiKey = os.Getenv("OPENROUTER_API_KEY")
	}

	return &openRouterProvider{
		baseURL:     baseURL,
		model:       model,
		apiKey:      apiKey,
		temperature: cfg.Temperature,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}, nil
}

func (p *openRouterProvider) Send(ctx context.Context, prompt string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	reqBody := map[string]interface{}{
		"model": p.model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	if p.temperature != nil {
		reqBody["temperature"] = *p.temperature
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
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
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(response.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	p.promptTokens = response.Usage.PromptTokens
	p.completionTokens = response.Usage.CompletionTokens

	return response.Choices[0].Message.Content, nil
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

func (p *openRouterProvider) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	return "", fmt.Errorf("tools not supported in openrouter provider")
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
	return nil
}
