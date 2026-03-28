package modelquery

import (
	"context"
	"encoding/json"
	"fmt"
)

// OllamaTagsResponse represents the response from GET /api/tags
type OllamaTagsResponse struct {
	Models []OllamaModel `json:"models"`
}

// OllamaModel represents a model from the Ollama API.
type OllamaModel struct {
	Name       string             `json:"name"`
	Model      string             `json:"model"`
	Size       int64              `json:"size"`
	Digest     string             `json:"digest"`
	ModifiedAt string             `json:"modified_at"`
	Details    OllamaModelDetails `json:"details"`
}

// OllamaModelDetails contains additional details about an Ollama model.
type OllamaModelDetails struct {
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

// listOllamaModels queries a local Ollama instance for available models.
func listOllamaModels(ctx context.Context, p Provider) ([]Model, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}

	url := endpoint + "/api/tags"
	if !hasScheme(endpoint) {
		url = "http://" + endpoint + "/api/tags"
	}

	data, err := doRequest(ctx, "GET", url, "", nil)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}

	var resp OllamaTagsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("ollama: failed to parse response: %w", err)
	}

	models := make([]Model, 0, len(resp.Models))
	for _, m := range resp.Models {
		name := m.Name
		if name == "" {
			name = m.Model
		}
		models = append(models, Model{
			ID:          name,
			Name:        name,
			Description: formatOllamaDetails(m.Details),
			ContextLen:  0,
		})
	}

	return models, nil
}

// formatOllamaDetails formats Ollama model details into a description string.
func formatOllamaDetails(d OllamaModelDetails) string {
	if d.ParameterSize != "" {
		return fmt.Sprintf("%s %s (%s)", d.Family, d.ParameterSize, d.QuantizationLevel)
	}
	return d.Family
}

// hasScheme checks if a URL has a scheme prefix.
func hasScheme(s string) bool {
	return len(s) >= 7 && (s[:7] == "http://" || s[:8] == "https://")
}
