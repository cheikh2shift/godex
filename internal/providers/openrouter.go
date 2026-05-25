package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cheikh2shift/godex/internal/config"
)

const defaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
const maxRetries = 6
const retryDelay = 35 * time.Second
const defaultMaxHistoryMessages = -1

func getMaxHistoryMessages() int {
	if val := os.Getenv("MAX_MESSAGE_WINDOW"); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxHistoryMessages
}

type openRouterModelInfo struct {
	ID            string `json:"id"`
	CanonicalSlug string `json:"canonical_slug"`
	Name          string `json:"name"`
	Architecture  struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
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

type responseToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type reasoningDetail struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Format string `json:"format"`
	Index  int    `json:"index"`
}

type openRouterProvider struct {
	temperature      *float64
	client           *http.Client
	pendingToolCalls map[string]string // tool_call_id -> function_name for tracking
	cancelFunc       context.CancelFunc
	OnThink          func(string)
	statusCh         chan<- string
	baseURL          string
	model            string
	apiKey           string
	systemPrompt     string
	messages         []map[string]interface{}
	tools            []map[string]interface{}
	cancelGen        uint64
	contextLimit     int
	promptTokens     int
	completionTokens int
	cancelMu         sync.Mutex
	mu               sync.Mutex
	visionChecked    bool
	visionSupported  bool
}

func (p *openRouterProvider) emitStatus(msg string) {
	p.mu.Lock()
	ch := p.statusCh
	p.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
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
		baseURL:          baseURL,
		model:            model,
		apiKey:           apiKey,
		temperature:      cfg.Temperature,
		contextLimit:     contextLimit,
		pendingToolCalls: make(map[string]string),
		client: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}, nil
}

func (p *openRouterProvider) Send(ctx context.Context, prompt string) (string, error) {
	systemPrompt, userInput := SplitSystemUserPrompt(prompt)
	if userInput == "" {
		userInput = strings.TrimSpace(prompt)
	}

	p.mu.Lock()
	if systemPrompt != "" {
		p.systemPrompt = systemPrompt
	}
	// Track only the user input in history
	if userInput != "" {
		p.messages = append(p.messages, map[string]interface{}{
			"role":    "user",
			"content": userInput,
		})
	}
	// Sliding window: keep only the last N messages (negative = unlimited)
	maxHistory := getMaxHistoryMessages()
	if maxHistory > 0 && len(p.messages) > maxHistory {
		p.messages = p.messages[len(p.messages)-maxHistory:]
	}
	messages := make([]map[string]interface{}, len(p.messages))
	copy(messages, p.messages)
	p.mu.Unlock()

	systemMsg := p.systemPrompt
	if systemMsg == "" {
		systemMsg = buildSystemWorkingDirMessage(prompt)
	}
	if systemMsg != "" {
		messages = prependSystemMessage(messages, systemMsg)
	}

	reqBody := map[string]interface{}{
		"model": p.model,
		"input": buildResponsesInput(messages),
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
			p.emitStatus(fmt.Sprintf("OpenRouter: waiting %v before retry (%d/%d)", retryDelay, attempt, maxRetries))
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(retryDelay):
			}
			p.emitStatus(fmt.Sprintf("OpenRouter: retrying request (%d/%d)", attempt, maxRetries))
		}

		req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/responses", bytes.NewReader(body))
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
			if attempt < maxRetries {
				p.emitStatus(fmt.Sprintf("OpenRouter: retryable error: %v", lastErr))
			}
			continue
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
			if attempt < maxRetries {
				p.emitStatus(fmt.Sprintf("OpenRouter: retryable error: %v", lastErr))
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		}

		var response struct {
			Choices []struct {
				Message struct {
					Content          string            `json:"content"`
					Reasoning        string            `json:"reasoning"`
					ReasoningDetails []reasoningDetail `json:"reasoning_details,omitempty"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}

		content, reasoningContent, toolCalls, usage, ok := parseResponsesOutput(bodyBytes)
		if !ok {
			if err := json.Unmarshal(bodyBytes, &response); err != nil {
				return "", fmt.Errorf("failed to decode response: %w", err)
			}

			if len(response.Choices) == 0 {
				return "", fmt.Errorf("no choices in response")
			}

			choice := response.Choices[0]
			content = pickMessageContent(choice.Message.Content, choice.Message.Reasoning, choice.Message.ReasoningDetails)
			reasoningContent = choice.Message.Reasoning
		}

		if reasoningContent != "" && p.OnThink != nil {
			p.OnThink(reasoningContent)
		}

		p.mu.Lock()

		if ok {
			p.promptTokens = usage.InputTokens
			p.completionTokens = usage.OutputTokens
		} else {
			p.promptTokens = response.Usage.PromptTokens
			p.completionTokens = response.Usage.CompletionTokens
		}

		// Track assistant response in history
		if len(toolCalls) > 0 {
			assistantContent := ""
			for _, tc := range toolCalls {
				assistantContent += fmt.Sprintf("\n[TOOL_CALL: %s | %s]", tc.Name, tc.Arguments)
				if tc.ID != "" {
					p.pendingToolCalls[tc.ID] = tc.Name
				}
			}
			p.messages = append(p.messages, map[string]interface{}{
				"role":    "assistant",
				"content": assistantContent,
			})
			p.mu.Unlock()
			return renderToolCalls(toolCalls), nil
		}
		p.messages = append(p.messages, map[string]interface{}{
			"role":    "assistant",
			"content": content,
		})
		p.mu.Unlock()

		return content, nil
	}

	return "", lastErr
}

func buildResponsesInput(messages []map[string]interface{}) []map[string]interface{} {
	input := make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		role = strings.TrimSpace(role)
		content = strings.TrimSpace(content)
		if role == "" || content == "" {
			continue
		}
		contentType := "input_text"
		if role == "assistant" {
			contentType = "output_text"
		}
		input = append(input, map[string]interface{}{
			"type": "message",
			"role": role,
			"content": []map[string]interface{}{
				{
					"type": contentType,
					"text": content,
				},
			},
		})
	}
	return input
}

func buildSystemWorkingDirMessage(prompt string) string {
	marker := "- Current working directory:"
	idx := strings.Index(prompt, marker)
	if idx < 0 {
		return ""
	}
	rest := prompt[idx+len(marker):]
	line := rest
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		line = rest[:nl]
	}
	wd := strings.TrimSpace(line)
	if wd == "" {
		return ""
	}
	return fmt.Sprintf("You are operating in this working directory: %s", wd)
}

func prependSystemMessage(messages []map[string]interface{}, content string) []map[string]interface{} {
	content = strings.TrimSpace(content)
	if content == "" {
		return messages
	}

	size := len(messages) + 1

	if size > 64*1024*1024 {

		// value too large
		return messages
	}

	out := make([]map[string]interface{}, 0, size)
	out = append(out, map[string]interface{}{
		"role":    "system",
		"content": content,
	})
	out = append(out, messages...)
	return out
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
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.InputSchema,
		}
	}
}

func (p *openRouterProvider) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	return "", fmt.Errorf("tools not supported in openrouter provider")
}

func (p *openRouterProvider) SubmitToolResult(toolCallID, result string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Look up the function name for this tool call
	funcName := p.pendingToolCalls[toolCallID]
	delete(p.pendingToolCalls, toolCallID)

	// Append tool result as a user message (tool role) for proper conversation tracking
	p.messages = append(p.messages, map[string]interface{}{
		"role":         "tool",
		"tool_call_id": toolCallID,
		"name":         funcName,
		"content":      result,
	})

	// Sliding window: keep only the last N messages (negative = unlimited)
	maxHistory := getMaxHistoryMessages()
	if maxHistory > 0 && len(p.messages) > maxHistory {
		p.messages = p.messages[len(p.messages)-maxHistory:]
	}
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
	p.pendingToolCalls = make(map[string]string)
	p.promptTokens = 0
	p.completionTokens = 0
	p.visionChecked = false
	p.visionSupported = false
	return nil
}

func (p *openRouterProvider) SetMessages(messages []Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.messages = make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		content := msg.Content
		if role == "" || content == "" {
			continue
		}
		p.messages = append(p.messages, map[string]interface{}{
			"role":    role,
			"content": content,
		})
	}
	maxHistory := getMaxHistoryMessages()
	if maxHistory > 0 && len(p.messages) > maxHistory {
		p.messages = p.messages[len(p.messages)-maxHistory:]
	}
	p.pendingToolCalls = make(map[string]string)
	p.promptTokens = 0
	p.completionTokens = 0
	return nil
}

func (p *openRouterProvider) AppendMessages(messages []Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	seen := make(map[string]struct{}, len(p.messages))
	for _, msg := range p.messages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
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
		p.messages = append(p.messages, map[string]interface{}{
			"role":    role,
			"content": content,
		})
		seen[key] = struct{}{}
	}
	maxHistory := getMaxHistoryMessages()
	if maxHistory > 0 && len(p.messages) > maxHistory {
		p.messages = p.messages[len(p.messages)-maxHistory:]
	}
	return nil
}

func (p *openRouterProvider) SupportsNativeToolCalls() bool {
	return true
}

func (p *openRouterProvider) SetStatusChannel(ch chan<- string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statusCh = ch
}

func pickMessageContent(content, reasoning string, details []reasoningDetail) string {
	candidates := []string{
		content,
		reasoning,
	}
	for _, detail := range details {
		if strings.TrimSpace(detail.Text) != "" {
			candidates = append(candidates, detail.Text)
		}
	}
	for _, candidate := range candidates {
		parsed := parseMaybeJSON(candidate)
		if parsed != "" {
			return parsed
		}
	}
	return ""
}

func parseMaybeJSON(input string) string {
	text := strings.TrimSpace(input)
	if text == "" {
		return ""
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(text), &obj); err == nil {
		if message, ok := obj["message"].(string); ok && strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message)
		}
		if summary, ok := obj["summary"].(string); ok && strings.TrimSpace(summary) != "" {
			return strings.TrimSpace(summary)
		}
		if reasoning, ok := obj["reasoning"].(string); ok && strings.TrimSpace(reasoning) != "" {
			return strings.TrimSpace(reasoning)
		}
		if thought, ok := obj["thought"].(string); ok && strings.TrimSpace(thought) != "" {
			return strings.TrimSpace(thought)
		}
		if status, ok := obj["status"].(string); ok && strings.TrimSpace(status) != "" {
			return strings.TrimSpace(status)
		}
		pretty, err := json.MarshalIndent(obj, "", "  ")
		if err == nil {
			return strings.TrimSpace(string(pretty))
		}
	}
	return text
}

type responsesUsage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

func parseResponsesOutput(body []byte) (string, string, []responseToolCall, responsesUsage, bool) {
	var resp struct {
		Output []map[string]interface{} `json:"output"`
		Usage  struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", nil, responsesUsage{}, false
	}
	if len(resp.Output) == 0 {
		return "", "", nil, responsesUsage{}, false
	}
	var textParts []string
	var reasoningParts []string
	var toolCalls []responseToolCall

	for _, item := range resp.Output {
		typ, _ := item["type"].(string)
		switch typ {
		case "message":
			if content, ok := item["content"].([]interface{}); ok {
				for _, c := range content {
					ci, ok := c.(map[string]interface{})
					if !ok {
						continue
					}
					ct, _ := ci["type"].(string)
					if ct == "output_text" || ct == "text" || ct == "input_text" {
						if txt, ok := ci["text"].(string); ok && strings.TrimSpace(txt) != "" {
							textParts = append(textParts, strings.TrimSpace(txt))
						}
					} else if ct == "reasoning" || ct == "thinking" {
						if txt, ok := ci["text"].(string); ok && strings.TrimSpace(txt) != "" {
							reasoningParts = append(reasoningParts, strings.TrimSpace(txt))
						}
					}
				}
			}
			if reasoning, ok := item["reasoning"].(string); ok && strings.TrimSpace(reasoning) != "" {
				reasoningParts = append(reasoningParts, strings.TrimSpace(reasoning))
			}
		case "output_text":
			if txt, ok := item["text"].(string); ok && strings.TrimSpace(txt) != "" {
				textParts = append(textParts, strings.TrimSpace(txt))
			}
		case "reasoning":
			if txt, ok := item["summary"].(string); ok && strings.TrimSpace(txt) != "" {
				reasoningParts = append(reasoningParts, strings.TrimSpace(txt))
			}
		case "function_call":
			name, _ := item["name"].(string)
			id, _ := item["id"].(string)
			argsRaw, _ := item["arguments"]
			argsStr := ""
			switch v := argsRaw.(type) {
			case string:
				argsStr = v
			case map[string]interface{}:
				if b, err := json.Marshal(v); err == nil {
					argsStr = string(b)
				}
			}
			if strings.TrimSpace(name) != "" {
				toolCalls = append(toolCalls, responseToolCall{
					ID:        id,
					Name:      name,
					Arguments: argsStr,
				})
			}
		}
	}

	if len(toolCalls) > 0 {
		return "", "", toolCalls, responsesUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		}, true
	}

	joined := strings.TrimSpace(strings.Join(textParts, "\n"))
	reasoning := strings.TrimSpace(strings.Join(reasoningParts, "\n"))
	if joined == "" && reasoning != "" {
		joined = reasoning
	}

	return parseMaybeJSON(joined), reasoning, nil, responsesUsage{
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
		TotalTokens:  resp.Usage.TotalTokens,
	}, true
}

func renderToolCalls(calls []responseToolCall) string {
	var b strings.Builder
	for i, tc := range calls {
		call := map[string]interface{}{
			"name": tc.Name,
		}
		if strings.TrimSpace(tc.Arguments) != "" {
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err == nil {
				call["arguments"] = args
			} else {
				call["arguments"] = map[string]interface{}{"_raw": tc.Arguments}
			}
		} else {
			call["arguments"] = map[string]interface{}{}
		}
		payload, err := json.Marshal(call)
		if err != nil {
			continue
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.Write(payload)
	}
	return b.String()
}

func (p *openRouterProvider) SetModel(model string, contextLimit int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.model = model
	p.contextLimit = contextLimit
	p.visionChecked = false
	p.visionSupported = false
	return nil
}

func (p *openRouterProvider) SupportsVision(ctx context.Context) (bool, error) {
	p.mu.Lock()
	if p.visionChecked {
		supported := p.visionSupported
		p.mu.Unlock()
		return supported, nil
	}
	p.mu.Unlock()

	supported, err := openRouterModelSupportsVision(ctx, p.baseURL, p.model, p.apiKey)
	p.mu.Lock()
	p.visionChecked = true
	p.visionSupported = supported
	p.mu.Unlock()
	return supported, err
}

func openRouterModelSupportsVision(ctx context.Context, baseURL, model, apiKey string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("failed to fetch models: status %d", resp.StatusCode)
	}

	var result openRouterModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}

	modelLower := strings.ToLower(model)
	for _, m := range result.Data {
		if strings.ToLower(m.ID) == modelLower ||
			strings.ToLower(m.CanonicalSlug) == modelLower ||
			strings.ToLower(m.Name) == modelLower {
			for _, modality := range m.Architecture.InputModalities {
				if strings.EqualFold(modality, "image") {
					return true, nil
				}
			}
			return false, nil
		}
	}

	return false, fmt.Errorf("model not found: %s", model)
}
