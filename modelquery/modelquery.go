// Package modelquery provides a unified interface to query available models
// from different LLM providers (Ollama, OpenRouter, Gemini).
package modelquery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	PlatformBaseURL = "https://api.openai.com/v1"
	CodexBaseURL    = "https://chatgpt.com/backend-api"
)

// ProviderType represents the type of LLM provider.
type ProviderType string

const (
	ProviderOllama      ProviderType = "ollama"
	ProviderOpenRouter  ProviderType = "openrouter"
	ProviderGemini      ProviderType = "gemini"
	ProviderHuggingFace ProviderType = "huggingface"
	ProviderOpenAI      ProviderType = "openai"
)

// Model represents a machine learning model with its metadata.
type Model struct {
	ID          string // Unique identifier (e.g., "llama3", "google/gemini-2.0-flash")
	Name        string // Human-readable name
	Description string // Description of the model
	ContextLen  int    // Context window size in tokens (0 if unknown)
}

// Provider represents configuration for connecting to an LLM provider.
type Provider struct {
	Type     ProviderType // Provider type: ollama, openrouter, gemini
	Endpoint string       // Base URL for the API
	APIKey   string       // API key (optional for some providers)
}

// ListModels queries the provider for a list of available models.
// It returns a slice of Model or an error if the request fails.
//
// Example usage:
//
//	models, err := modelquery.ListModels(ctx, provider)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	for _, m := range models {
//	    fmt.Println(m.ID, m.Name)
//	}
func ListModels(ctx context.Context, p Provider) ([]Model, error) {
	return ListModelsWithQuery(ctx, p, "")
}

func ListModelsWithQuery(ctx context.Context, p Provider, query string) ([]Model, error) {
	switch p.Type {
	case ProviderOllama:
		return listOllamaModels(ctx, p)
	case ProviderOpenRouter:
		return listOpenRouterModels(ctx, p, query)
	case ProviderGemini:
		return listGeminiModels(ctx, p)
	case ProviderHuggingFace:
		return listHuggingFaceModels(ctx, p, query)
	case ProviderOpenAI:
		return listOpenAIModels(ctx, p)
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", p.Type)
	}
}

func SearchModels(ctx context.Context, p Provider, query string) ([]Model, error) {
	return SearchModelsWithQuery(ctx, p, query)
}

func SearchModelsWithQuery(ctx context.Context, p Provider, query string) ([]Model, error) {
	query = normalizeQuery(query)
	if query == "" {
		return ListModelsWithQuery(ctx, p, "")
	}

	switch p.Type {
	case ProviderOllama:
		models, err := listOllamaModels(ctx, p)
		if err != nil {
			return nil, err
		}
		return filterModels(models, query), nil
	case ProviderOpenRouter:
		models, err := listOpenRouterModels(ctx, p, "")
		if err != nil {
			return nil, err
		}
		return filterModels(models, query), nil
	case ProviderGemini:
		models, err := listGeminiModels(ctx, p)
		if err != nil {
			return nil, err
		}
		return filterModels(models, query), nil
	case ProviderHuggingFace:
		return listHuggingFaceModels(ctx, p, query)
	case ProviderOpenAI:
		models, err := listOpenAIModels(ctx, p)
		if err != nil {
			return nil, err
		}
		return filterModels(models, query), nil
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", p.Type)
	}
}

func filterModels(models []Model, query string) []Model {
	var result []Model
	for _, m := range models {
		if contains(m.ID, query) || contains(m.Name, query) || contains(m.Description, query) {
			result = append(result, m)
		}
	}
	return result
}

// normalizeQuery lowercases and trims the query string.
func normalizeQuery(q string) string {
	var result []byte
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			result = append(result, c)
		}
	}
	return string(result)
}

// contains checks if s contains substr (case-insensitive).
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if equalFold(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

// equalFold is a case-insensitive string comparison.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// doRequest performs an HTTP request and decodes the response.
func doRequest(ctx context.Context, method, url string, apiKey string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	respBody, _ := io.ReadAll(resp.Body)
	return respBody, nil
}

// addParams adds query parameters to a URL.
func addParams(baseURL string, params map[string]string) string {
	if len(params) == 0 {
		return baseURL
	}
	values := url.Values{}
	for k, v := range params {
		values.Set(k, v)
	}
	return baseURL + "?" + values.Encode()
}

func listOpenAIModels(ctx context.Context, p Provider) ([]Model, error) {
	isCodex := isCodexToken(p.APIKey)

	if isCodex {
		return getCodexModels(), nil
	}

	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = PlatformBaseURL
	}
	endpoint = strings.TrimSuffix(endpoint, "/")

	data, err := doRequest(ctx, "GET", endpoint+"/models", p.APIKey, nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Data []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}

	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}

	var models []Model
	for _, m := range response.Data {
		if m.Object != "model" {
			continue
		}
		contextLen := fetchModelContextLimit(ctx, p.APIKey, endpoint, m.ID)
		models = append(models, Model{
			ID:          m.ID,
			Name:        m.ID,
			Description: "Owned by: " + m.OwnedBy,
			ContextLen:  contextLen,
		})
	}

	return models, nil
}

func fetchModelContextLimit(ctx context.Context, apiKey, baseURL, modelID string) int {
	modelName := modelID
	if idx := strings.Index(modelID, "-"); idx > 0 {
		modelName = modelID[:idx]
	}
	if !strings.HasPrefix(modelID, "gpt-") && !strings.HasPrefix(modelID, "o") {
		modelName = modelID
	}

	url := fmt.Sprintf("https://platform.openai.com/docs/models/%s", modelName)
	data, err := doRequest(ctx, "GET", url, "", nil)
	if err != nil {
		return getDefaultContextLimit(modelID)
	}

	re := regexp.MustCompile(`(\d{1,3}(?:,\d{3})*|\d+)\s*[Kk]\s*context\s*window`)
	matches := re.FindStringSubmatch(string(data))
	if len(matches) >= 2 {
		contextStr := strings.ReplaceAll(matches[1], ",", "")
		if val, err := strconv.Atoi(contextStr); err == nil {
			return val * 1000
		}
	}

	return getDefaultContextLimit(modelID)
}

func getCodexModels() []Model {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	models := []Model{
		{ID: "gpt-5.4", Name: "gpt-5.4", Description: "Flagship frontier model for professional work"},
		{ID: "gpt-5.4-mini", Name: "gpt-5.4-mini", Description: "Fast, efficient mini model for responsive coding"},
		{ID: "gpt-5.3-codex", Name: "gpt-5.3-codex", Description: "Industry-leading coding model"},
		{ID: "gpt-5.3-codex-spark", Name: "gpt-5.3-codex-spark", Description: "Text-only research preview for real-time coding"},
		{ID: "gpt-5.2-codex", Name: "gpt-5.2-codex", Description: "Advanced coding model for real-world engineering"},
		{ID: "gpt-5.2", Name: "gpt-5.2", Description: "Previous general-purpose model for coding and agentic tasks"},
		{ID: "gpt-5.1-codex-max", Name: "gpt-5.1-codex-max", Description: "Optimized for long-horizon, agentic coding tasks"},
		{ID: "gpt-5.1", Name: "gpt-5.1", Description: "Great for coding and agentic tasks across domains"},
		{ID: "gpt-5.1-codex", Name: "gpt-5.1-codex", Description: "Optimized for long-running, agentic coding tasks"},
		{ID: "gpt-5-codex", Name: "gpt-5-codex", Description: "Version tuned for long-running, agentic coding tasks"},
		{ID: "gpt-5", Name: "gpt-5", Description: "Reasoning model for coding and agentic tasks"},
	}

	for i := range models {
		models[i].ContextLen = fetchModelContextLimit(ctx, "", "", models[i].ID)
	}

	return models
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

func GetModelContextLimit(model string) int {
	return getDefaultContextLimit(model)
}

func getDefaultContextLimit(model string) int {
	limits := map[string]int{
		"gpt-5.4":             200000,
		"gpt-5.4-mini":        200000,
		"gpt-5.3-codex":       200000,
		"gpt-5.3-codex-spark": 128000,
		"gpt-5.2-codex":       200000,
		"gpt-5.2":             128000,
		"gpt-5.1-codex-max":   1000000,
		"gpt-5.1-codex":       200000,
		"gpt-5.1":             128000,
		"gpt-5-codex":         128000,
		"gpt-5":               128000,
		"gpt-4o":              128000,
		"gpt-4o-mini":         128000,
		"gpt-4":               32000,
		"gpt-4-turbo":         128000,
		"gpt-3.5-turbo":       16385,
		"o1":                  200000,
		"o1-mini":             128000,
		"o1-preview":          128000,
		"o3":                  200000,
		"o3-mini":             200000,
		"o4-mini":             128000,
	}

	if limit, ok := limits[model]; ok {
		return limit
	}

	return 128000
}
