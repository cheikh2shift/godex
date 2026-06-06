package providers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/cheikh2shift/godex/internal/config"
)

const DefaultGeminiModel = "gemini-2.5-flash"

type geminiProvider struct {
	client           *genai.Client
	cfg              *config.Provider
	model            string
	temperature      *float64
	cancelMu         sync.Mutex
	mu               sync.Mutex
	cancelFunc       context.CancelFunc
	cancelGen        uint64
	contextLimit     int
	promptTokens     int
	completionTokens int
	messages         []Message
	statusCh         chan<- string
}

func init() {
	Register("gemini", newGeminiProvider)
}

func newGeminiProvider(cfg *config.Provider) (Provider, error) {
	model := cfg.Model
	if model == "" {
		model = DefaultGeminiModel
	}

	backend := strings.ToLower(strings.TrimSpace(cfg.Params["backend"]))
	clientCfg := &genai.ClientConfig{}

	switch backend {
	case "", "gemini", "geminiapi":
		apiKey, err := resolveAPIKey(cfg)
		if err != nil {
			return nil, err
		}
		clientCfg.APIKey = apiKey
		clientCfg.Backend = genai.BackendGeminiAPI
	case "vertex", "vertexai":
		clientCfg.Backend = genai.BackendVertexAI
		if project := strings.TrimSpace(cfg.Params["project"]); project != "" {
			clientCfg.Project = project
		}
		if location := strings.TrimSpace(cfg.Params["location"]); location != "" {
			clientCfg.Location = location
		}
	default:
		return nil, fmt.Errorf("unknown gemini backend %q", backend)
	}

	client, err := genai.NewClient(context.Background(), clientCfg)
	if err != nil {
		return nil, err
	}

	p := &geminiProvider{
		client:       client,
		cfg:          cfg,
		model:        model,
		temperature:  cfg.Temperature,
		contextLimit: 0,
	}

	if cfg.ContextLimit > 0 {
		p.contextLimit = cfg.ContextLimit
	} else {
		if err := p.fetchModelInfo(); err != nil {
			fmt.Printf("[Gemini] Warning: could not fetch model info: %v\n", err)
		}
	}

	return p, nil
}

func (g *geminiProvider) fetchModelInfo() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	modelName := g.model
	if !strings.HasPrefix(modelName, "models/") {
		modelName = "models/" + modelName
	}

	modelInfo, err := g.client.Models.Get(ctx, modelName, nil)
	if err != nil {
		return err
	}

	if modelInfo != nil {
		g.contextLimit = int(modelInfo.InputTokenLimit)
	}

	return nil
}

func (g *geminiProvider) Send(ctx context.Context, prompt string) (string, error) {
	g.mu.Lock()
	history := append([]Message(nil), g.messages...)
	g.mu.Unlock()

	var config *genai.GenerateContentConfig
	if g.temperature != nil {
		config = &genai.GenerateContentConfig{
			Temperature: genai.Ptr(float32(*g.temperature)),
		}
	}

	g.cancelMu.Lock()
	ctx, cancel := context.WithCancel(ctx)
	g.cancelGen++
	gen := g.cancelGen
	g.cancelFunc = cancel
	g.cancelMu.Unlock()

	defer func() {
		g.cancelMu.Lock()
		if g.cancelGen == gen {
			g.cancelFunc = nil
		}
		g.cancelMu.Unlock()
	}()

	finalPrompt := prompt
	if len(history) > 0 {
		finalPrompt = buildMessageTranscript(history, prompt)
	}
	resp, err := g.client.Models.GenerateContent(ctx, g.model, genai.Text(finalPrompt), config)
	if err != nil {
		return "", err
	}

	g.mu.Lock()
	if resp.UsageMetadata != nil {
		g.promptTokens += int(resp.UsageMetadata.PromptTokenCount)
		g.completionTokens += int(resp.UsageMetadata.CandidatesTokenCount)
	}
	g.mu.Unlock()

	return extractText(resp), nil
}

func (g *geminiProvider) Close() error {
	return nil
}

func (g *geminiProvider) Tools() []Tool {
	return nil
}

func (g *geminiProvider) SetThinkCallback(fn func(string)) {
}

func (g *geminiProvider) Cancel() {
	g.cancelMu.Lock()
	defer g.cancelMu.Unlock()
	if g.cancelFunc != nil {
		g.cancelFunc()
	}
}

func (g *geminiProvider) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	return "", fmt.Errorf("Gemini provider does not support direct tool calls")
}

func (g *geminiProvider) ContextLimit() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.contextLimit
}

func (g *geminiProvider) TokenUsage() (input int, output int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.promptTokens, g.completionTokens
}

func (g *geminiProvider) Reset() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	cfg := g.cfg
	clientCfg := &genai.ClientConfig{}

	backend := strings.ToLower(strings.TrimSpace(cfg.Params["backend"]))
	switch backend {
	case "", "gemini", "geminiapi":
		apiKey, err := resolveAPIKey(cfg)
		if err != nil {
			return err
		}
		clientCfg.APIKey = apiKey
		clientCfg.Backend = genai.BackendGeminiAPI
	case "vertex", "vertexai":
		clientCfg.Backend = genai.BackendVertexAI
		if project := strings.TrimSpace(cfg.Params["project"]); project != "" {
			clientCfg.Project = project
		}
		if location := strings.TrimSpace(cfg.Params["location"]); location != "" {
			clientCfg.Location = location
		}
	}

	client, err := genai.NewClient(context.Background(), clientCfg)
	if err != nil {
		return err
	}

	g.client = client
	g.promptTokens = 0
	g.completionTokens = 0
	g.messages = nil
	return nil
}

func (g *geminiProvider) SetStatusChannel(ch chan<- string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.statusCh = ch
}

func (g *geminiProvider) SetMessages(messages []Message) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.messages = append([]Message(nil), messages...)
	return nil
}

func (g *geminiProvider) AppendMessages(messages []Message) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	seen := make(map[string]struct{}, len(g.messages))
	for _, msg := range g.messages {
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
		g.messages = append(g.messages, msg)
		seen[key] = struct{}{}
	}
	return nil
}

func (g *geminiProvider) SupportsNativeToolCalls() bool {
	return false
}

func buildMessageTranscript(messages []Message, prompt string) string {
	var b strings.Builder
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		content := strings.TrimSpace(msg.Content)
		if role == "" || content == "" {
			continue
		}
		if role == "assistant" {
			b.WriteString("Assistant: ")
		} else {
			b.WriteString("User: ")
		}
		b.WriteString(content)
		b.WriteByte('\n')
	}
	b.WriteString("User: ")
	b.WriteString(prompt)
	return b.String()
}

func extractText(resp *genai.GenerateContentResponse) string {
	if resp == nil || len(resp.Candidates) == 0 {
		return ""
	}
	var combined strings.Builder
	for _, candidate := range resp.Candidates {
		if candidate.Content == nil {
			continue
		}
		for _, part := range candidate.Content.Parts {
			if part.Text == "" {
				continue
			}
			if combined.Len() > 0 {
				combined.WriteString("\n")
			}
			combined.WriteString(part.Text)
		}
	}
	return strings.TrimSpace(combined.String())
}

func resolveAPIKey(cfg *config.Provider) (string, error) {
	if cfg.APIKey != "" {
		return cfg.APIKey, nil
	}
	if cfg.APIKeyEnv != "" {
		if key := os.Getenv(cfg.APIKeyEnv); key != "" {
			return key, nil
		}
		return "", fmt.Errorf("environment variable %s is empty", cfg.APIKeyEnv)
	}
	if key := os.Getenv("GEMINI_API_KEY"); key != "" {
		return key, nil
	}
	if key := os.Getenv("GOOGLE_API_KEY"); key != "" {
		return key, nil
	}
	return "", fmt.Errorf("missing API key for provider %q", cfg.Name)
}

func (g *geminiProvider) SetModel(model string, contextLimit int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.model = model
	g.contextLimit = contextLimit
	return nil
}
