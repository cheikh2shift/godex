package modelquery

import (
	"context"
	"encoding/json"
	"fmt"
)

// OpenRouterModelsResponse represents the response from GET /api/v1/models
type OpenRouterModelsResponse struct {
	Data []OpenRouterModel `json:"data"`
}

// OpenRouterModel represents a model from the OpenRouter API.
type OpenRouterModel struct {
	ID              string                 `json:"id"`
	CanonicalSlug   string                 `json:"canonical_slug"`
	Name            string                 `json:"name"`
	Created         int64                  `json:"created"`
	Description     string                 `json:"description"`
	ContextLength   int                    `json:"context_length"`
	Architecture    OpenRouterArchitecture `json:"architecture"`
	Pricing         OpenRouterPricing      `json:"pricing"`
	TopProvider     OpenRouterTopProvider  `json:"top_provider"`
	SupportedParams []string               `json:"supported_parameters"`
	DefaultParams   map[string]interface{} `json:"default_parameters"`
	ExpirationDate  string                 `json:"expiration_date"`
}

// OpenRouterArchitecture describes the model's technical capabilities.
type OpenRouterArchitecture struct {
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
	Tokenizer        string   `json:"tokenizer"`
	InstructType     string   `json:"instruct_type"`
}

// OpenRouterPricing contains pricing information for the model.
type OpenRouterPricing struct {
	Prompt            string `json:"prompt"`
	Completion        string `json:"completion"`
	Request           string `json:"request"`
	Image             string `json:"image"`
	WebSearch         string `json:"web_search"`
	InternalReasoning string `json:"internal_reasoning"`
	InputCacheRead    string `json:"input_cache_read"`
	InputCacheWrite   string `json:"input_cache_write"`
}

// OpenRouterTopProvider contains information about the top provider.
type OpenRouterTopProvider struct {
	ContextLength       int  `json:"context_length"`
	MaxCompletionTokens int  `json:"max_completion_tokens"`
	IsModerated         bool `json:"is_moderated"`
}

// listOpenRouterModels queries OpenRouter for available models.
func listOpenRouterModels(ctx context.Context, p Provider) ([]Model, error) {
	url := "https://openrouter.ai/api/v1/models"

	data, err := doRequest(ctx, "GET", url, "", nil)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}

	var resp OpenRouterModelsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("openrouter: failed to parse response: %w", err)
	}

	models := make([]Model, 0, len(resp.Data))
	for _, m := range resp.Data {
		models = append(models, Model{
			ID:          m.ID,
			Name:        m.Name,
			Description: truncateDescription(m.Description),
			ContextLen:  m.ContextLength,
		})
	}

	return models, nil
}

// truncateDescription shortens descriptions longer than 200 characters.
func truncateDescription(desc string) string {
	if len(desc) <= 200 {
		return desc
	}
	// Find last space before 200 to avoid cutting words
	for i := 200; i > 150 && i < len(desc); i-- {
		if desc[i] == ' ' {
			return desc[:i] + "..."
		}
	}
	return desc[:200] + "..."
}
