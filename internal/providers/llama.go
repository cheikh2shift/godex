package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cheikh2shift/godex/internal/config"
)

const (
	defaultLlamaServerTimeout = 10 * time.Minute
	llamaMaxRetries           = 3
	llamaRetryDelay           = 5 * time.Second
)

type llamaProvider struct {
	baseURL          string
	model            string
	modelIsHF        bool
	cfg              *config.Provider
	temperature      *float64
	client           *http.Client
	messages         []map[string]interface{}
	mu               sync.Mutex
	sendMu           sync.Mutex
	OnThink          func(string)
	cancelFunc       context.CancelFunc
	cancelGen        uint64
	contextLimit     int
	promptTokens     int
	completionTokens int
	statusCh         chan<- string
	tools            []map[string]interface{}
	pendingToolCalls map[string]string

	serverCmd     *exec.Cmd
	serverProcess *os.Process
	serverPort    int
	serverMu      sync.Mutex
}

func init() {
	Register("llama", newLlamaProvider)
	Register("llamacpp", newLlamaProvider)
}

func findFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port, nil
}

func detectLlamaServer() (string, error) {
	paths := []string{}

	godexDir := os.Getenv("GODEX_DIR")
	if godexDir != "" {
		paths = append(paths, filepath.Join(godexDir, "llama-server"))
	}

	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		paths = append(paths,
			filepath.Join(homeDir, ".godex", "llama-server"),
			filepath.Join(homeDir, ".godex", "bin", "llama-server"),
		)
	}

	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			if err := os.Chmod(p, 0755); err == nil {
				if _, err := exec.LookPath(p); err == nil || fileExists(p) {
					return p, nil
				}
			}
		}
	}

	if path, err := exec.LookPath("llama-server"); err == nil {
		return path, nil
	}

	for _, p := range paths {
		if fileExists(p) {
			return p, nil
		}
	}

	return "", fmt.Errorf("llama-server not found")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func resolveModelPath(model string) (string, bool, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false, fmt.Errorf("empty model name")
	}

	if fileExists(model) {
		return model, true, nil
	}

	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		godexModelPath := filepath.Join(homeDir, ".godex", "models", model)
		if fileExists(godexModelPath) {
			return godexModelPath, true, nil
		}

		entries, err := os.ReadDir(filepath.Join(homeDir, ".godex", "models"))
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(model)) {
					return filepath.Join(homeDir, ".godex", "models", entry.Name()), true, nil
				}
			}
		}
	}

	return model, false, nil
}

func newLlamaProvider(cfg *config.Provider) (Provider, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "qwen2.5-coder-7b Q4_K_M"
	}

	modelPath, isLocal, err := resolveModelPath(model)
	if err != nil {
		return nil, err
	}

	serverPath, err := detectLlamaServer()
	if err != nil {
		return nil, fmt.Errorf("llama.cpp provider requires llama-server: %w", err)
	}

	port, err := findFreePort()
	if err != nil {
		return nil, fmt.Errorf("failed to find free port: %w", err)
	}

	p := &llamaProvider{
		baseURL:          fmt.Sprintf("http://localhost:%d", port),
		model:            modelPath,
		modelIsHF:        !isLocal,
		cfg:              cfg,
		temperature:      cfg.Temperature,
		client:           &http.Client{Timeout: defaultLlamaServerTimeout},
		messages:         []map[string]interface{}{},
		contextLimit:     cfg.ContextLimit,
		pendingToolCalls: make(map[string]string),
		serverPort:       port,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p.startServer(ctx, serverPath); err != nil {
		return nil, fmt.Errorf("failed to start llama-server: %w", err)
	}

	if p.contextLimit == 0 {
		if err := p.fetchModelInfo(ctx); err != nil {
			fmt.Printf("[llama.cpp] Warning: could not fetch model info: %v\n", err)
		}
	}

	return p, nil
}

func (p *llamaProvider) startServer(ctx context.Context, serverPath string) error {
	p.serverMu.Lock()
	defer p.serverMu.Unlock()

	args := []string{}

	if p.modelIsHF {
		args = append(args, "-hf", p.model)
	} else {
		args = append(args, "-m", p.model)
	}

	args = append(args,
		"--port", strconv.Itoa(p.serverPort),
		"--host", "127.0.0.1",
	)
	if p.contextLimit > 0 {
		args = append(args, "-c", strconv.Itoa(p.contextLimit))
	}
	args = append(args, "--log-disable")

	cmd := exec.CommandContext(ctx, serverPath, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start llama-server: %w", err)
	}

	p.serverCmd = cmd
	p.serverProcess = cmd.Process

	waitCtx, waitCancel := context.WithTimeout(ctx, 15*time.Second)
	defer waitCancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-waitCtx.Done():
			p.killServer()
			return fmt.Errorf("timeout waiting for llama-server to start")
		case <-ticker.C:
			if p.serverProcess == nil || p.serverProcess.Pid <= 0 {
				continue
			}
			resp, err := p.client.Get(p.baseURL + "/health")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode < 500 {
					return nil
				}
			}
		}
	}
}

func (p *llamaProvider) killServer() {
	p.serverMu.Lock()
	defer p.serverMu.Unlock()

	if p.serverProcess != nil {
		p.serverProcess.Kill()
		p.serverProcess = nil
	}
	if p.serverCmd != nil {
		p.serverCmd.Wait()
		p.serverCmd = nil
	}
}

func (p *llamaProvider) fetchModelInfo(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
	if err != nil {
		return err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if len(result.Data) > 0 {
		p.contextLimit = result.Data[0].ContextLength
	}

	return nil
}

func (p *llamaProvider) Send(ctx context.Context, prompt string) (string, error) {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()

	p.mu.Lock()
	p.messages = append(p.messages, map[string]interface{}{
		"role":    "user",
		"content": prompt,
	})
	messages := make([]map[string]interface{}, len(p.messages))
	copy(messages, p.messages)

	ctx, cancel := context.WithCancel(ctx)
	p.cancelGen++
	gen := p.cancelGen
	p.cancelFunc = cancel
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		if p.cancelGen == gen {
			p.cancelFunc = nil
		}
		p.mu.Unlock()
	}()

	var response string
	var totalPromptTokens, totalCompletionTokens int
	var lastErr error

	for attempt := 0; attempt <= llamaMaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(llamaRetryDelay):
			}
		}

		result, err := p.doSend(ctx, messages)
		if err != nil {
			lastErr = err
			continue
		}

		response = result.response
		totalPromptTokens = result.totalPromptTokens
		totalCompletionTokens = result.totalCompletionTokens
		lastErr = nil
		break
	}

	if lastErr != nil {
		return "", lastErr
	}

	p.mu.Lock()
	p.messages = append(p.messages, map[string]interface{}{
		"role":    "assistant",
		"content": response,
	})
	p.promptTokens = totalPromptTokens
	p.completionTokens = totalCompletionTokens
	p.mu.Unlock()

	return response, nil
}

type llamaSendResult struct {
	response              string
	totalPromptTokens     int
	totalCompletionTokens int
}

func (p *llamaProvider) doSend(ctx context.Context, messages []map[string]interface{}) (llamaSendResult, error) {
	reqBody := map[string]interface{}{
		"model":    p.model,
		"messages": messages,
		"stream":   true,
	}

	if p.temperature != nil {
		reqBody["temperature"] = *p.temperature
	}

	if len(p.tools) > 0 {
		reqBody["tools"] = p.tools
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return llamaSendResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return llamaSendResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return llamaSendResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return llamaSendResult{}, fmt.Errorf("llama-server responded with %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var fullResponse strings.Builder
	var hasContent bool
	var totalPromptTokens, totalCompletionTokens int
	var reasoningContent string
	var toolCalls []llamaResponseToolCall

	reader := io.Reader(resp.Body)
	lineReader := newLineReader(reader)

	for {
		line, err := lineReader.ReadLine()
		if err != nil {
			break
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Role             string `json:"role"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]

			if choice.Delta.Content != "" {
				hasContent = true
				fullResponse.WriteString(choice.Delta.Content)
				if p.OnThink != nil {
					p.OnThink(choice.Delta.Content)
				}
			}

			if choice.Delta.ReasoningContent != "" {
				reasoningContent += choice.Delta.ReasoningContent
			}

			if choice.FinishReason == "tool_calls" && len(chunk.Message.ToolCalls) > 0 {
				for _, tc := range chunk.Message.ToolCalls {
					toolCalls = append(toolCalls, llamaResponseToolCall{
						ID:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					})
					p.pendingToolCalls[tc.ID] = tc.Function.Name
				}
			}
		}

		if totalPromptTokens == 0 && chunk.Usage.PromptTokens > 0 {
			totalPromptTokens = chunk.Usage.PromptTokens
		}
		if totalCompletionTokens == 0 && chunk.Usage.CompletionTokens > 0 {
			totalCompletionTokens = chunk.Usage.CompletionTokens
		}
	}

	if reasoningContent != "" && p.OnThink != nil {
		p.OnThink(reasoningContent)
	}

	if len(toolCalls) > 0 {
		var b strings.Builder
		for i, tc := range toolCalls {
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
		return llamaSendResult{response: b.String(), totalPromptTokens: totalPromptTokens, totalCompletionTokens: totalCompletionTokens}, nil
	}

	response := strings.TrimSpace(fullResponse.String())
	if response == "" && !hasContent {
		return llamaSendResult{}, fmt.Errorf("llama-server returned empty response")
	}

	return llamaSendResult{response: response, totalPromptTokens: totalPromptTokens, totalCompletionTokens: totalCompletionTokens}, nil
}

type lineReader struct {
	reader io.Reader
	buf    []byte
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{reader: r, buf: make([]byte, 0, 4096)}
}

func (lr *lineReader) ReadLine() (string, error) {
	for {
		if len(lr.buf) > 0 {
			for i, b := range lr.buf {
				if b == '\n' {
					line := string(lr.buf[:i])
					lr.buf = lr.buf[i+1:]
					if len(line) > 0 && line[len(line)-1] == '\r' {
						line = line[:len(line)-1]
					}
					return line, nil
				}
			}
		}

		tmp := make([]byte, 4096)
		n, err := lr.reader.Read(tmp)
		if err != nil && err != io.EOF {
			return "", err
		}
		if n == 0 && err == io.EOF {
			if len(lr.buf) > 0 {
				line := string(lr.buf)
				lr.buf = lr.buf[:0]
				if len(line) > 0 && line[len(line)-1] == '\r' {
					line = line[:len(line)-1]
				}
				return line, nil
			}
			return "", io.EOF
		}
		lr.buf = append(lr.buf, tmp[:n]...)
		if err == io.EOF {
			continue
		}
	}
}

type llamaResponseToolCall struct {
	ID        string
	Name      string
	Arguments string
}

func (p *llamaProvider) Close() error {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()

	p.killServer()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = nil
	return nil
}

func (p *llamaProvider) Tools() []Tool {
	return nil
}

func (p *llamaProvider) SetThinkCallback(fn func(string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.OnThink = fn
}

func (p *llamaProvider) SetStatusChannel(ch chan<- string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statusCh = ch
}

func (p *llamaProvider) Cancel() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
}

func (p *llamaProvider) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	return "", fmt.Errorf("llama.cpp provider does not support direct tool calls, use MCP servers")
}

func (p *llamaProvider) ContextLimit() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.contextLimit
}

func (p *llamaProvider) TokenUsage() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.promptTokens, p.completionTokens
}

func (p *llamaProvider) Reset() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.client = &http.Client{Timeout: defaultLlamaServerTimeout}
	p.messages = []map[string]interface{}{}
	p.promptTokens = 0
	p.completionTokens = 0
	p.pendingToolCalls = make(map[string]string)
	return nil
}

func (p *llamaProvider) SetMessages(messages []Message) error {
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
	return nil
}

func (p *llamaProvider) AppendMessages(messages []Message) error {
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
	return nil
}

func (p *llamaProvider) SupportsNativeToolCalls() bool {
	return true
}

func (p *llamaProvider) SetModel(model string, contextLimit int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.model = model
	p.contextLimit = contextLimit
	return nil
}

func GetLlamaContextLimitFromModel(model string) (int, error) {
	return 0, fmt.Errorf("context limit detection from model name not implemented for llama.cpp")
}

func parseModelContext(model string) int {
	re := regexp.MustCompile(`-(\d+)k[_-]?ctx`)
	matches := re.FindStringSubmatch(strings.ToLower(model))
	if len(matches) >= 2 {
		if val, err := strconv.Atoi(matches[1]); err == nil {
			return val * 1000
		}
	}

	knownModels := map[string]int{
		"qwen2.5-coder":  8192,
		"qwen2.5":        8192,
		"qwen2":          8192,
		"llama3":         8192,
		"llama3.1":       128 * 1000,
		"llama3.2":       128 * 1000,
		"codellama":      16384,
		"mistral":        32768,
		"mixtral":        32768,
		"phi":            4096,
		"phi3":           4096,
		"gemma":          8192,
		"gemma2":         8192,
		"stablelm":       8192,
		"deepseek-coder": 16384,
		"deepseek":       16384,
	}

	modelLower := strings.ToLower(model)
	for prefix, ctx := range knownModels {
		if strings.Contains(modelLower, prefix) {
			return ctx
		}
	}

	return 4096
}
