package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cheikh-seck/godex/internal/config"
)

const (
	DefaultOllamaModel = "minimax-m2.5:cloud"
	defaultOllamaBase  = "http://localhost:11434"
	ollamaKeepAlive    = "30m"
)

type ollamaProvider struct {
	baseURL     string
	model       string
	temperature *float64
	client      *http.Client
	messages    []map[string]string
	mu          sync.Mutex
	OnThink     func(string)
	cancelFunc  context.CancelFunc
}

func init() {
	Register("ollama", newOllamaProvider)
}

func newOllamaProvider(cfg *config.Provider) (Provider, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultOllamaModel
	}
	baseURL := strings.TrimSpace(cfg.Endpoint)
	if baseURL == "" {
		baseURL = strings.TrimSpace(cfg.Params["base_url"])
	}
	if baseURL == "" {
		baseURL = defaultOllamaBase
	}

	return &ollamaProvider{
		baseURL:     strings.TrimRight(baseURL, "/"),
		model:       model,
		temperature: cfg.Temperature,
		client: &http.Client{
			Timeout: 10 * time.Minute,
		},
		messages: []map[string]string{},
	}, nil
}

func (o *ollamaProvider) Send(ctx context.Context, prompt string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.messages = append(o.messages, map[string]string{
		"role":    "user",
		"content": prompt,
	})

	ctx, o.cancelFunc = context.WithCancel(ctx)

	endpoint := o.baseURL + "/api/chat"
	reqBody := map[string]any{
		"model":      o.model,
		"messages":   o.messages,
		"stream":     true,
		"keep_alive": ollamaKeepAlive,
	}
	if o.temperature != nil {
		reqBody["options"] = map[string]any{
			"temperature": *o.temperature,
		}
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama responded with %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	decoder := json.NewDecoder(resp.Body)
	var fullResponse strings.Builder
	var hasContent bool

	for {
		var chunk struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done bool `json:"done"`
		}
		if err := decoder.Decode(&chunk); err != nil {
			break
		}

		if chunk.Message.Content != "" {
			hasContent = true
			fullResponse.WriteString(chunk.Message.Content)
			if o.OnThink != nil {
				o.OnThink(chunk.Message.Content)
			}
		}

		if chunk.Done {
			break
		}
	}

	response := strings.TrimSpace(fullResponse.String())
	if response == "" && !hasContent {
		return "", fmt.Errorf("ollama returned empty response")
	}

	o.messages = append(o.messages, map[string]string{
		"role":    "assistant",
		"content": response,
	})

	return response, nil
}

func (o *ollamaProvider) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	endpoint := o.baseURL + "/api/chat"
	reqBody := map[string]any{
		"model":      o.model,
		"messages":   []map[string]string{},
		"stream":     false,
		"keep_alive": "0",
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	o.messages = nil
	return nil
}

func (o *ollamaProvider) Tools() []Tool {
	return nil
}

func (o *ollamaProvider) SetThinkCallback(fn func(string)) {
	o.OnThink = fn
}

func (o *ollamaProvider) Cancel() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cancelFunc != nil {
		o.cancelFunc()
	}
}

func (o *ollamaProvider) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	return "", fmt.Errorf("Ollama provider does not support direct tool calls, use MCP servers configured in provider")
}
