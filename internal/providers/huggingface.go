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

	"github.com/cheikh2shift/godex/internal/config"
)

const defaultHFBaseURL = "https://router.huggingface.co/v1"

type huggingfaceProvider struct {
	temperature *float64
	client      *http.Client
	cancelFunc  context.CancelFunc
	statusCh    chan<- string
	baseURL     string
	model       string
	apiKey      string
	messages    []Message
	cancelGen   uint64
	cancelMu    sync.Mutex
	mu          sync.Mutex
}

func init() {
	Register("huggingface", newHuggingFaceProvider)
}

func newHuggingFaceProvider(cfg *config.Provider) (Provider, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, fmt.Errorf("huggingface provider requires a model")
	}

	baseURL := strings.TrimSpace(cfg.Endpoint)
	if baseURL == "" {
		baseURL = defaultHFBaseURL
	}

	apiKey, err := resolveHFAPIKey(cfg)
	if err != nil {
		return nil, err
	}

	return &huggingfaceProvider{
		baseURL:     strings.TrimRight(baseURL, "/"),
		model:       model,
		apiKey:      apiKey,
		temperature: cfg.Temperature,
		client: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}, nil
}

func (h *huggingfaceProvider) Send(ctx context.Context, prompt string) (string, error) {
	h.mu.Lock()
	history := append([]Message(nil), h.messages...)
	h.mu.Unlock()

	h.cancelMu.Lock()
	ctx, cancel := context.WithCancel(ctx)
	h.cancelGen++
	gen := h.cancelGen
	h.cancelFunc = cancel
	h.cancelMu.Unlock()
	defer func() {
		h.cancelMu.Lock()
		if h.cancelGen == gen {
			h.cancelFunc = nil
		}
		h.cancelMu.Unlock()
	}()

	endpoint := buildHFEndpoint(h.baseURL)
	reqBody := map[string]any{
		"model": h.model,
	}
	if len(history) > 0 {
		reqBody["messages"] = buildHFMessages(history, prompt)
	} else {
		reqBody["messages"] = []map[string]string{
			{"role": "user", "content": prompt},
		}
	}
	if h.temperature != nil {
		reqBody["temperature"] = *h.temperature
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
	req.Header.Set("Authorization", "Bearer "+h.apiKey)

	resp, err := h.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		bodyText := strings.TrimSpace(string(body))
		if resp.StatusCode == http.StatusGone && strings.Contains(strings.ToLower(h.baseURL), "api-inference.huggingface.co") {
			return "", fmt.Errorf("huggingface endpoint %s is deprecated; use https://router.huggingface.co/v1", h.baseURL)
		}
		if resp.StatusCode == http.StatusBadRequest && strings.Contains(bodyText, "model_not_supported") {
			return "", fmt.Errorf("huggingface model not supported by enabled providers; pick a model with Inference Providers or append :fastest/:provider and ensure providers are enabled in your HF account")
		}
		return "", fmt.Errorf("huggingface responded with %d: %s", resp.StatusCode, bodyText)
	}

	if text := parseHFChatResponse(body); text != "" {
		return text, nil
	}
	if text := parseHFGeneratedText(body); text != "" {
		return text, nil
	}

	return "", fmt.Errorf("huggingface returned unexpected response: %s", strings.TrimSpace(string(body)))
}

func (h *huggingfaceProvider) Close() error {
	return nil
}

func (h *huggingfaceProvider) Tools() []Tool {
	return nil
}

func (h *huggingfaceProvider) SetThinkCallback(fn func(string)) {
}

func (h *huggingfaceProvider) Cancel() {
	h.cancelMu.Lock()
	defer h.cancelMu.Unlock()
	if h.cancelFunc != nil {
		h.cancelFunc()
	}
}

func (h *huggingfaceProvider) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	return "", fmt.Errorf("huggingface provider does not support direct tool calls")
}

func (h *huggingfaceProvider) SetStatusChannel(ch chan<- string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.statusCh = ch
}

func buildHFEndpoint(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.Contains(base, "/chat/completions") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

func parseHFChatResponse(body []byte) string {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	if len(resp.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content)
}

func parseHFGeneratedText(body []byte) string {
	var arrayResp []struct {
		GeneratedText string `json:"generated_text"`
	}
	if err := json.Unmarshal(body, &arrayResp); err == nil && len(arrayResp) > 0 {
		return strings.TrimSpace(arrayResp[0].GeneratedText)
	}

	var objResp struct {
		GeneratedText string `json:"generated_text"`
	}
	if err := json.Unmarshal(body, &objResp); err == nil {
		return strings.TrimSpace(objResp.GeneratedText)
	}

	return ""
}

func resolveHFAPIKey(cfg *config.Provider) (string, error) {
	if cfg.APIKey != "" {
		return cfg.APIKey, nil
	}
	if cfg.APIKeyEnv != "" {
		if key := os.Getenv(cfg.APIKeyEnv); key != "" {
			return key, nil
		}
		return "", fmt.Errorf("environment variable %s is empty", cfg.APIKeyEnv)
	}
	if key := os.Getenv("HF_TOKEN"); key != "" {
		return key, nil
	}
	return "", fmt.Errorf("huggingface API key not set (set api_key, api_key_env, or HF_TOKEN)")
}

func (h *huggingfaceProvider) ContextLimit() int {
	return 0
}

func (h *huggingfaceProvider) TokenUsage() (input int, output int) {
	return 0, 0
}

func (h *huggingfaceProvider) Reset() error {
	h.mu.Lock()
	h.messages = nil
	h.mu.Unlock()
	return nil
}

func (h *huggingfaceProvider) SetMessages(messages []Message) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append([]Message(nil), messages...)
	return nil
}

func (h *huggingfaceProvider) AppendMessages(messages []Message) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := make(map[string]struct{}, len(h.messages))
	for _, msg := range h.messages {
		role := strings.TrimSpace(msg.Role)
		content := msg.Content
		if role == "" || content == "" {
			continue
		}
		seen[role+"\x00"+content] = struct{}{}
	}
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		content := msg.Content
		if role == "" || content == "" {
			continue
		}
		key := role + "\x00" + content
		if _, ok := seen[key]; ok {
			continue
		}
		h.messages = append(h.messages, msg)
		seen[key] = struct{}{}
	}
	return nil
}

func (h *huggingfaceProvider) SupportsNativeToolCalls() bool {
	return false
}

func buildHFMessages(messages []Message, prompt string) []map[string]string {
	payload := make([]map[string]string, 0, len(messages)+1)
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		content := strings.TrimSpace(msg.Content)
		if role == "" || content == "" {
			continue
		}
		payload = append(payload, map[string]string{
			"role":    role,
			"content": content,
		})
	}
	payload = append(payload, map[string]string{
		"role":    "user",
		"content": prompt,
	})
	return payload
}

func (h *huggingfaceProvider) SetModel(model string, contextLimit int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.model = model
	return nil
}
