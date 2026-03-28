package modelquery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// GeminiModelsResponse represents the response from the Gemini API.
type GeminiModelsResponse struct {
	Models []GeminiModel `json:"models"`
}

// GeminiModel represents a model from the Gemini API.
type GeminiModel struct {
	Name                       string   `json:"name"`
	Version                    string   `json:"version"`
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description"`
	InputTokenLimit            int      `json:"inputTokenLimit"`
	OutputTokenLimit           int      `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	Temperature                *float64 `json:"temperature,omitempty"`
	TopP                       *float64 `json:"topP,omitempty"`
	TopK                       *int     `json:"topK,omitempty"`
}

// listGeminiModels queries Google's Gemini API for available models.
func listGeminiModels(ctx context.Context, p Provider) ([]Model, error) {
	apiKey := p.APIKey
	if apiKey == "" {
		return nil, fmt.Errorf("gemini: API key required")
	}

	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = "https://generativelanguage.googleapis.com/v1beta"
	}

	url := fmt.Sprintf("%s/models?key=%s", strings.TrimSuffix(endpoint, "/"), apiKey)

	data, err := doRequest(ctx, "GET", url, "", nil)
	if err != nil {
		return nil, fmt.Errorf("gemini: %w", err)
	}

	var resp GeminiModelsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("gemini: failed to parse response: %w", err)
	}

	models := make([]Model, 0, len(resp.Models))
	for _, m := range resp.Models {
		// Skip base models that aren't meant for direct use
		if !isDirectlyUsable(m) {
			continue
		}

		name := extractModelName(m.Name)
		models = append(models, Model{
			ID:          name,
			Name:        m.DisplayName,
			Description: m.Description,
			ContextLen:  m.InputTokenLimit,
		})
	}

	return models, nil
}

// isDirectlyUsable returns true if the model is meant for direct use (not a base model).
func isDirectlyUsable(m GeminiModel) bool {
	// Models that support 'generateContent' are directly usable
	for _, method := range m.SupportedGenerationMethods {
		if method == "generateContent" {
			return true
		}
	}
	return false
}

// extractModelName extracts the model ID from the full resource name.
// e.g., "models/gemini-2.0-flash" -> "gemini-2.0-flash"
func extractModelName(fullName string) string {
	if strings.HasPrefix(fullName, "models/") {
		return strings.TrimPrefix(fullName, "models/")
	}
	return fullName
}
