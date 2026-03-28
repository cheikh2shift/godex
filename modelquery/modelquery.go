// Package modelquery provides a unified interface to query available models
// from different LLM providers (Ollama, OpenRouter, Gemini).
package modelquery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// ProviderType represents the type of LLM provider.
type ProviderType string

const (
	ProviderOllama     ProviderType = "ollama"
	ProviderOpenRouter ProviderType = "openrouter"
	ProviderGemini     ProviderType = "gemini"
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
	switch p.Type {
	case ProviderOllama:
		return listOllamaModels(ctx, p)
	case ProviderOpenRouter:
		return listOpenRouterModels(ctx, p)
	case ProviderGemini:
		return listGeminiModels(ctx, p)
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", p.Type)
	}
}

// SearchModels queries the provider and filters models matching the query.
// It searches in both model ID and name fields.
//
// Example usage:
//
//	models, err := modelquery.SearchModels(ctx, provider, "claude")
//	if err != nil {
//	    log.Fatal(err)
//	}
func SearchModels(ctx context.Context, p Provider, query string) ([]Model, error) {
	models, err := ListModels(ctx, p)
	if err != nil {
		return nil, err
	}

	query = normalizeQuery(query)
	if query == "" {
		return models, nil
	}

	var result []Model
	for _, m := range models {
		if contains(m.ID, query) || contains(m.Name, query) || contains(m.Description, query) {
			result = append(result, m)
		}
	}
	return result, nil
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
