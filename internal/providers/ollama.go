package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cheikh2shift/godex/internal/config"
)

const (
	DefaultOllamaModel = "minimax-m2.7:cloud"
	defaultOllamaBase  = "http://localhost:11434"
	ollamaKeepAlive    = "30m"
	ollamaMaxRetries   = 5
	ollamaRetryDelay   = 10 * time.Second
)

type ollamaProvider struct {
	baseURL          string
	model            string
	cfg              *config.Provider
	temperature      *float64
	client           *http.Client
	systemPrompt     string
	messages         []map[string]string
	mu               sync.Mutex
	sendMu           sync.Mutex
	OnThink          func(string)
	cancelFunc       context.CancelFunc
	cancelGen        uint64
	contextLimit     int
	promptTokens     int
	completionTokens int
	statusCh         chan<- string
	visionChecked    bool
	visionSupported  bool
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

	p := &ollamaProvider{
		baseURL:     strings.TrimRight(baseURL, "/"),
		model:       model,
		cfg:         cfg,
		temperature: cfg.Temperature,
		client: &http.Client{
			Timeout: 10 * time.Minute,
		},
		messages:     []map[string]string{},
		contextLimit: 0,
	}

	if strings.Contains(model, "hf.co") {
		if err := p.fetchHuggingFaceContext(model); err != nil {
			fmt.Printf("[Ollama] Warning: could not fetch HuggingFace model info: %v\n", err)
		}
	} else {
		if err := p.fetchModelInfo(); err != nil {
			fmt.Printf("[Ollama] Warning: could not fetch model info: %v\n", err)
		}
	}

	return p, nil
}

func (o *ollamaProvider) fetchHuggingFaceContext(model string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	model = strings.TrimPrefix(model, "hf.co/")
	if idx := strings.Index(model, ":"); idx > 0 {
		model = model[:idx]
	}

	url := fmt.Sprintf("https://huggingface.co/api/models/%s", model)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HuggingFace API returned status %d", resp.StatusCode)
	}

	var info struct {
		Config struct {
			MaxModelLength        int `json:"max_model_length"`
			MaxPositionEmbeddings int `json:"max_position_embeddings"`
		} `json:"config"`
		GGUF struct {
			ContextLength int `json:"context_length"`
		} `json:"gguf"`
		TransformersInfo struct {
			ModelType string `json:"model_type"`
		} `json:"transformers_info"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return err
	}

	if info.GGUF.ContextLength > 0 {
		o.contextLimit = info.GGUF.ContextLength
	} else if info.Config.MaxModelLength > 0 {
		o.contextLimit = info.Config.MaxModelLength
	} else if info.Config.MaxPositionEmbeddings > 0 {
		o.contextLimit = info.Config.MaxPositionEmbeddings
	}

	return nil
}

func (o *ollamaProvider) fetchModelInfo() error {
	if err := o.fetchOllamaLibraryContext(); err != nil {
		fmt.Printf("[Ollama] Warning: could not fetch library context: %v\n", err)
	}
	return nil
}

func (o *ollamaProvider) fetchOllamaLibraryContext() error {
	modelName := o.model
	if idx := strings.Index(modelName, ":"); idx > 0 {
		modelName = modelName[:idx]
	}

	url := fmt.Sprintf("https://ollama.com/library/%s/tags", modelName)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("library page returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	re := regexp.MustCompile(`(\d+)[Kk]\s*context\s*window`)
	matches := re.FindStringSubmatch(string(body))
	if len(matches) >= 2 {
		contextStr := matches[1]
		if val, err := strconv.Atoi(contextStr); err == nil {
			o.contextLimit = val * 1000
		}
	}

	return nil
}

func GetOllamaContextLimit(model string) (int, error) {
	modelName := model
	if idx := strings.Index(modelName, ":"); idx > 0 {
		modelName = modelName[:idx]
	}

	url := fmt.Sprintf("https://ollama.com/library/%s/tags", modelName)
	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("library page returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	re := regexp.MustCompile(`(\d+)[Kk]\s*context\s*window`)
	matches := re.FindStringSubmatch(string(body))
	if len(matches) >= 2 {
		contextStr := matches[1]
		if val, err := strconv.Atoi(contextStr); err == nil {
			return val * 1000, nil
		}
	}

	return 0, fmt.Errorf("could not find context limit for model %s", model)
}

func (o *ollamaProvider) Send(ctx context.Context, prompt string) (string, error) {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()

	o.mu.Lock()
	systemPrompt, userInput := splitSystemUserPrompt(prompt)
	if systemPrompt != "" {
		o.systemPrompt = systemPrompt
	}
	if o.systemPrompt != "" {
		o.ensureSystemMessage(o.systemPrompt)
	}
	if userInput == "" {
		userInput = strings.TrimSpace(prompt)
	}
	if userInput != "" {
		o.messages = append(o.messages, map[string]string{
			"role":    "user",
			"content": userInput,
		})
	}

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

	var response string
	var totalPromptTokens int
	var totalCompletionTokens int
	var lastErr error

	for attempt := 0; attempt <= ollamaMaxRetries; attempt++ {
		if attempt > 0 {
			fmt.Printf("\n[Ollama] API Error, cooling down and retrying\n")
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(ollamaRetryDelay):
			}
		}

		result, err := o.doSend(ctx, endpoint, payload)
		if err != nil {
			lastErr = err
			if isRetryableError(err) {
				continue
			}
			return "", err
		}

		response, totalPromptTokens, totalCompletionTokens, lastErr = result.response, result.totalPromptTokens, result.totalCompletionTokens, nil
		break
	}

	if lastErr != nil {
		return "", lastErr
	}

	o.mu.Lock()
	o.messages = append(o.messages, map[string]string{
		"role":    "assistant",
		"content": response,
	})
	o.promptTokens = totalPromptTokens
	o.completionTokens = totalCompletionTokens
	o.mu.Unlock()

	return response, nil
}

func (o *ollamaProvider) ensureSystemMessage(system string) {
	if strings.TrimSpace(system) == "" {
		return
	}
	if len(o.messages) > 0 {
		if role := o.messages[0]["role"]; role == "system" {
			if o.messages[0]["content"] == system {
				return
			}
			o.messages[0]["content"] = system
			return
		}
	}
	o.messages = append([]map[string]string{{"role": "system", "content": system}}, o.messages...)
}

type sendResult struct {
	response              string
	totalPromptTokens     int
	totalCompletionTokens int
}

func (o *ollamaProvider) doSend(ctx context.Context, endpoint string, payload []byte) (sendResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return sendResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return sendResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return sendResult{}, &ollamaError{
			statusCode: resp.StatusCode,
			message:    strings.TrimSpace(string(body)),
		}
	}

	decoder := json.NewDecoder(resp.Body)
	var fullResponse strings.Builder
	var hasContent bool
	var totalPromptTokens int
	var totalCompletionTokens int
	for {
		var chunk struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			PromptEvalCount int  `json:"prompt_eval_count"`
			EvalCount       int  `json:"eval_count"`
			Done            bool `json:"done"`
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

		if totalPromptTokens == 0 && chunk.PromptEvalCount > 0 {
			totalPromptTokens = chunk.PromptEvalCount
		}

		if totalCompletionTokens == 0 && chunk.EvalCount > 0 {
			totalCompletionTokens = chunk.EvalCount
		}

		if chunk.Done {
			break
		}
	}

	response := strings.TrimSpace(fullResponse.String())
	if response == "" && !hasContent {
		return sendResult{}, fmt.Errorf("ollama returned empty response")
	}

	return sendResult{response: response, totalPromptTokens: totalPromptTokens, totalCompletionTokens: totalCompletionTokens}, nil
}

type ollamaError struct {
	statusCode int
	message    string
}

func (e *ollamaError) Error() string {
	return fmt.Sprintf("ollama responded with %d: %s", e.statusCode, e.message)
}

func isRetryableError(err error) bool {
	if ollamaErr, ok := err.(*ollamaError); ok {
		return ollamaErr.statusCode >= 500
	}
	return false
}

func (o *ollamaProvider) Close() error {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()

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

	o.mu.Lock()
	o.messages = nil
	o.mu.Unlock()
	return nil
}

func (o *ollamaProvider) Tools() []Tool {
	return nil
}

func (o *ollamaProvider) SetThinkCallback(fn func(string)) {
	o.OnThink = fn
}

func (o *ollamaProvider) SetStatusChannel(ch chan<- string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.statusCh = ch
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

func (o *ollamaProvider) ContextLimit() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.contextLimit
}

func (o *ollamaProvider) TokenUsage() (input int, output int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.promptTokens, o.completionTokens
}

func (o *ollamaProvider) Reset() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.client = &http.Client{
		Timeout: 10 * time.Minute,
	}
	o.messages = []map[string]string{}
	o.promptTokens = 0
	o.completionTokens = 0
	o.visionChecked = false
	o.visionSupported = false
	return nil
}

func (o *ollamaProvider) SetMessages(messages []Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.messages = make([]map[string]string, 0, len(messages))
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		content := msg.Content
		if role == "" || content == "" {
			continue
		}
		o.messages = append(o.messages, map[string]string{
			"role":    role,
			"content": content,
		})
	}
	return nil
}

func (o *ollamaProvider) AppendMessages(messages []Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	seen := make(map[string]struct{}, len(o.messages))
	for _, msg := range o.messages {
		role := msg["role"]
		content := msg["content"]
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
		o.messages = append(o.messages, map[string]string{
			"role":    role,
			"content": content,
		})
		seen[key] = struct{}{}
	}
	return nil
}

func (o *ollamaProvider) SupportsNativeToolCalls() bool {
	return false
}

func (o *ollamaProvider) SetModel(model string, contextLimit int) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.model = model
	o.contextLimit = contextLimit
	o.visionChecked = false
	o.visionSupported = false
	return nil
}

func (o *ollamaProvider) SupportsVision(ctx context.Context) (bool, error) {
	o.mu.Lock()
	if o.visionChecked {
		supported := o.visionSupported
		o.mu.Unlock()
		return supported, nil
	}
	o.mu.Unlock()

	supported, err := o.detectVisionSupport(ctx)
	o.mu.Lock()
	o.visionChecked = true
	o.visionSupported = supported
	o.mu.Unlock()
	return supported, err
}

func (o *ollamaProvider) detectVisionSupport(ctx context.Context) (bool, error) {
	endpoint := o.baseURL
	if endpoint == "" {
		endpoint = defaultOllamaBase
	}
	endpoint = strings.TrimRight(endpoint, "/") + "/api/show"

	reqBody := map[string]string{
		"name": o.model,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("failed to fetch model info: HTTP %d", resp.StatusCode)
	}
	var result struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}
	for _, cap := range result.Capabilities {
		if strings.EqualFold(cap, "vision") {
			return true, nil
		}
	}
	return false, nil
}
