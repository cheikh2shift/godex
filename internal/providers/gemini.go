package providers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"google.golang.org/genai"

	"github.com/cheikh-seck/godex/internal/config"
)

const DefaultGeminiModel = "gemini-2.5-flash"

type geminiProvider struct {
	client      *genai.Client
	model       string
	temperature *float64
	cancelMu    sync.Mutex
	cancelFunc  context.CancelFunc
	cancelGen   uint64
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

	return &geminiProvider{
		client:      client,
		model:       model,
		temperature: cfg.Temperature,
	}, nil
}

func (g *geminiProvider) Send(ctx context.Context, prompt string) (string, error) {
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

	resp, err := g.client.Models.GenerateContent(ctx, g.model, genai.Text(prompt), config)
	if err != nil {
		return "", err
	}
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

func (g *geminiProvider) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	return "", fmt.Errorf("Gemini provider does not support direct tool calls")
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
