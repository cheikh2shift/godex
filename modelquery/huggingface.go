package modelquery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// HuggingFaceModel represents a model from the HuggingFace API.
type HuggingFaceModel struct {
	ID           string   `json:"id"`
	ModelID      string   `json:"modelId"`
	Author       string   `json:"author"`
	CreatedAt    string   `json:"createdAt"`
	LastModified string   `json:"lastModified"`
	Private      bool     `json:"private"`
	Downloads    int      `json:"downloads"`
	Guarded      bool     `json:"guarded"`
	Likes        int      `json:"likes"`
	Tags         []string `json:"tags"`
	PipelineTag  string   `json:"pipeline_tag"`
	Siblings     []struct {
		Rfilename string `json:"rfilename"`
	} `json:"siblings"`
	Autotemp string `json:"__variant"`
}

// HuggingFaceResponse represents the response from HuggingFace API.
type HuggingFaceResponse []HuggingFaceModel

// listHuggingFaceModels queries HuggingFace for available GGUF-compatible models.
func listHuggingFaceModels(ctx context.Context, p Provider, query string) ([]Model, error) {
	url := "https://huggingface.co/api/models"
	queryParams := map[string]string{
		"pipeline_tag": "text-generation",
		"filter":       "gguf",
		"sort":         "downloads",
		"direction":    "-1",
		"limit":        "10",
		"full":         "false",
	}
	if query != "" {
		queryParams["search"] = query
	}
	url = addParams(url, queryParams)

	data, err := doRequest(ctx, "GET", url, "", nil)
	if err != nil {
		return nil, fmt.Errorf("huggingface: %w", err)
	}

	var resp HuggingFaceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("huggingface: failed to parse response: %w", err)
	}

	models := make([]Model, 0, len(resp))
	for _, m := range resp {
		displayName := m.ID
		if idx := strings.LastIndex(displayName, "/"); idx >= 0 {
			displayName = displayName[idx+1:]
		}

		description := fmt.Sprintf("by %s | %s | downloads: %d", m.Author, m.PipelineTag, m.Downloads)
		if m.Likes > 100 {
			description = fmt.Sprintf("★ %d likes | %s", m.Likes, description)
		}

		models = append(models, Model{
			ID:          m.ID,
			Name:        displayName,
			Description: description,
			ContextLen:  0,
		})
	}

	return models, nil
}
