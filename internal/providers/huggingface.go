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

const defaultHFBaseURL = "https://router.huggingface.co/v1"

type huggingfaceProvider struct {
	baseURL     string
	model       string
	apiKey      string
	temperature *float64
	client      *http.Client
	cancelMu    sync.Mutex
	cancelFunc  context.CancelFunc
	cancelGen   uint64
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
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
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

func (h *huggingfaceProvider) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	return "", fmt.Errorf("huggingface provider does not support direct tool calls")
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
	return nil
}

func (h *huggingfaceProvider) SupportsNativeToolCalls() bool {
	return false
}
