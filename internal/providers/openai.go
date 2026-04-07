package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cheikh2shift/godex/internal/config"
)

const (
	DefaultOpenAIModel = "gpt-5.4"
	CodexBaseURL       = "https://chatgpt.com/backend-api"
	PlatformBaseURL    = "https://api.openai.com/v1"
)

type openaiProvider struct {
	client           *http.Client
	cfg              *config.Provider
	model            string
	temperature      *float64
	mu               sync.Mutex
	cancelFunc       context.CancelFunc
	cancelGen        uint64
	contextLimit     int
	promptTokens     int
	completionTokens int
	messages         []Message
	statusCh         chan<- string
	baseURL          string
	thinkCallback    func(string)
	isCodex          bool
}

func init() {
	Register("openai", newOpenAIProvider)
}

func newOpenAIProvider(cfg *config.Provider) (Provider, error) {
	model := cfg.Model
	if model == "" {
		model = DefaultOpenAIModel
	}

	baseURL := strings.TrimSuffix(cfg.Endpoint, "/")
	if baseURL == "" {
		baseURL = PlatformBaseURL
	}

	apiKey, err := resolveOpenAIAPIKey(cfg)
	if err != nil {
		return nil, err
	}

	isCodex := isCodexToken(apiKey)
	if isCodex {
		baseURL = CodexBaseURL
	}

	p := &openaiProvider{
		client:       &http.Client{Timeout: 120 * time.Second},
		cfg:          cfg,
		model:        model,
		temperature:  cfg.Temperature,
		contextLimit: 128000,
		baseURL:      baseURL,
		isCodex:      isCodex,
	}

	return p, nil
}

func isCodexToken(apiKey string) bool {
	if apiKey == "" {
		return false
	}
	// Codex OAuth tokens are JWTs starting with "eyJ"
	if !strings.HasPrefix(apiKey, "eyJ") {
		return false
	}
	parts := strings.Split(apiKey, ".")
	if len(parts) != 3 {
		return false
	}
	decoded, err := decodeJWTPayload(parts[1])
	if err != nil {
		// If decode fails but it's a JWT, assume it's Codex OAuth
		return true
	}
	return decoded != nil && decoded["https://api.openai.com/auth"] != nil
}

func decodeJWTPayload(payload string) (map[string]interface{}, error) {
	decoded := make([]byte, len(payload))
	if _, err := base64.RawURLEncoding.Decode(decoded, []byte(payload)); err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if err := json.Unmarshal(decoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func resolveOpenAIAPIKey(cfg *config.Provider) (string, error) {
	if cfg.APIKey != "" {
		return cfg.APIKey, nil
	}
	if cfg.APIKeyEnv != "" {
		if key := os.Getenv(cfg.APIKeyEnv); key != "" {
			return key, nil
		}
		return "", fmt.Errorf("environment variable %s is empty", cfg.APIKeyEnv)
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		return key, nil
	}
	return "", fmt.Errorf("missing API key for provider %q", cfg.Name)
}

func (o *openaiProvider) Send(ctx context.Context, prompt string) (string, error) {
	o.mu.Lock()
	history := append([]Message(nil), o.messages...)
	o.mu.Unlock()

	o.mu.Lock()
	ctx, cancel := context.WithCancel(ctx)
	o.cancelGen++
	gen := o.cancelGen
	o.cancelFunc = cancel
	o.mu.Unlock()

	defer func() {
		o.mu.Lock()
		if o.cancelGen == gen {
			o.cancelFunc = nil
		}
		o.mu.Unlock()
	}()

	messages := buildOpenAIMessages(history, prompt)

	var jsonBody []byte
	var endpoint string

	if o.isCodex {
		accountID := o.extractAccountID()
		body := map[string]interface{}{
			"model":    o.model,
			"messages": messages,
			"store":    false,
		}
		jsonBody, _ = json.Marshal(body)
		endpoint = o.baseURL + "/responses"

		apiKey, _ := resolveOpenAIAPIKey(o.cfg)
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(jsonBody)))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)
		if accountID != "" {
			req.Header.Set("chatgpt-account-id", accountID)
		}

		resp, err := o.client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		var response map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			return "", err
		}

		if resp.StatusCode != 200 {
			errMsg := "unknown error"
			if err, ok := response["error"].(map[string]interface{}); ok {
				if msg, ok := err["message"].(string); ok {
					errMsg = msg
				}
			}
			return "", fmt.Errorf("Codex API error: %s", errMsg)
		}

		return o.parseCodexResponse(response)
	}

	body := map[string]interface{}{
		"model":       o.model,
		"messages":    messages,
		"temperature": o.temperature,
	}

	jsonBody, _ = json.Marshal(body)
	endpoint = o.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(jsonBody)))
	if err != nil {
		return "", err
	}

	apiKey, _ := resolveOpenAIAPIKey(o.cfg)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := o.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var response map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", err
	}

	if resp.StatusCode != 200 {
		errMsg := "unknown error"
		if err, ok := response["error"].(map[string]interface{}); ok {
			if msg, ok := err["message"].(string); ok {
				errMsg = msg
			}
		}
		return "", fmt.Errorf("OpenAI API error: %s", errMsg)
	}

	choices, ok := response["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return "", fmt.Errorf("no response from OpenAI")
	}

	firstChoice, ok := choices[0].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid response choice")
	}

	msg, ok := firstChoice["message"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid message in response")
	}

	var content string
	var reasoningContent string

	if contentVal, ok := msg["content"].(string); ok {
		content = contentVal
	} else if contentArr, ok := msg["content"].([]interface{}); ok {
		var textParts []string
		for _, c := range contentArr {
			ci, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			ct, _ := ci["type"].(string)
			if ct == "output_text" || ct == "text" {
				if txt, ok := ci["text"].(string); ok {
					textParts = append(textParts, txt)
				}
			} else if ct == "reasoning" {
				if txt, ok := ci["text"].(string); ok {
					reasoningContent = txt
				}
			}
		}
		content = strings.Join(textParts, "")
	}

	if reasoningContent == "" {
		if reasoning, ok := msg["reasoning"].(string); ok {
			reasoningContent = reasoning
		}
	}

	o.mu.Lock()
	hasThink := o.thinkCallback != nil
	o.mu.Unlock()

	if hasThink && reasoningContent != "" {
		o.mu.Lock()
		o.thinkCallback(reasoningContent)
		o.mu.Unlock()
	}

	if usage, ok := response["usage"].(map[string]interface{}); ok {
		o.mu.Lock()
		if promptTokens, ok := usage["prompt_tokens"].(float64); ok {
			o.promptTokens += int(promptTokens)
		}
		if completionTokens, ok := usage["completion_tokens"].(float64); ok {
			o.completionTokens += int(completionTokens)
		}
		o.mu.Unlock()
	}

	return content, nil
}

func buildOpenAIMessages(history []Message, prompt string) []map[string]interface{} {
	var messages []map[string]interface{}

	for _, msg := range history {
		role := msg.Role
		if role == "" {
			continue
		}
		messages = append(messages, map[string]interface{}{
			"role":    role,
			"content": msg.Content,
		})
	}

	messages = append(messages, map[string]interface{}{
		"role":    "user",
		"content": prompt,
	})

	return messages
}

func (o *openaiProvider) Close() error {
	return nil
}

func (o *openaiProvider) Tools() []Tool {
	return nil
}

func (o *openaiProvider) SetThinkCallback(fn func(string)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.thinkCallback = fn
}

func (o *openaiProvider) Cancel() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cancelFunc != nil {
		o.cancelFunc()
	}
}

func (o *openaiProvider) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	return "", fmt.Errorf("OpenAI provider does not support direct tool calls")
}

func (o *openaiProvider) ContextLimit() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.contextLimit
}

func (o *openaiProvider) TokenUsage() (input int, output int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.promptTokens, o.completionTokens
}

func (o *openaiProvider) Reset() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.promptTokens = 0
	o.completionTokens = 0
	o.messages = nil
	return nil
}

func (o *openaiProvider) SetStatusChannel(ch chan<- string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.statusCh = ch
}

func (o *openaiProvider) SetMessages(messages []Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.messages = append([]Message(nil), messages...)
	return nil
}

func (o *openaiProvider) AppendMessages(messages []Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	seen := make(map[string]struct{}, len(o.messages))
	for _, msg := range o.messages {
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
		o.messages = append(o.messages, msg)
		seen[key] = struct{}{}
	}
	return nil
}

func (o *openaiProvider) SupportsNativeToolCalls() bool {
	return true
}

func (o *openaiProvider) SetModel(model string, contextLimit int) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.model = model
	o.contextLimit = contextLimit
	return nil
}

func (o *openaiProvider) extractAccountID() string {
	apiKey, err := resolveOpenAIAPIKey(o.cfg)
	if err != nil || apiKey == "" {
		return ""
	}

	parts := strings.Split(apiKey, ".")
	if len(parts) != 3 {
		return ""
	}

	decoded, err := decodeJWTPayload(parts[1])
	if err != nil {
		return ""
	}

	auth, ok := decoded["https://api.openai.com/auth"].(map[string]interface{})
	if !ok {
		return ""
	}

	accountID, _ := auth["chatgpt_account_id"].(string)
	return accountID
}

func (o *openaiProvider) parseCodexResponse(response map[string]interface{}) (string, error) {
	var textParts []string
	var reasoningContent string

	output, ok := response["output"].([]interface{})
	if !ok {
		return "", fmt.Errorf("invalid Codex response: no output")
	}

	for _, item := range output {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		typ, _ := itemMap["type"].(string)
		if typ == "message" {
			content, ok := itemMap["content"].([]interface{})
			if !ok {
				continue
			}
			for _, c := range content {
				ci, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				ct, _ := ci["type"].(string)
				if ct == "output_text" || ct == "text" {
					if txt, ok := ci["text"].(string); ok {
						textParts = append(textParts, txt)
					}
				} else if ct == "reasoning" || ct == "thinking" {
					if txt, ok := ci["text"].(string); ok {
						reasoningContent = txt
					}
				}
			}
		} else if typ == "reasoning" {
			if summary, ok := itemMap["summary"].(string); ok {
				reasoningContent = summary
			}
		} else if typ == "output_text" {
			if txt, ok := itemMap["text"].(string); ok {
				textParts = append(textParts, txt)
			}
		}
	}

	content := strings.Join(textParts, "")

	o.mu.Lock()
	hasThink := o.thinkCallback != nil
	o.mu.Unlock()

	if hasThink && reasoningContent != "" {
		o.mu.Lock()
		o.thinkCallback(reasoningContent)
		o.mu.Unlock()
	}

	if content == "" {
		return "", fmt.Errorf("empty response from Codex")
	}

	return content, nil
}
