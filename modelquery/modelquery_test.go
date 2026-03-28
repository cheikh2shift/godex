//go:build integration
// +build integration

package modelquery

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestOllamaListModels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p := Provider{
		Type:     ProviderOllama,
		Endpoint: "http://localhost:11434",
	}

	models, err := ListModels(ctx, p)
	if err != nil {
		t.Skipf("Ollama not available: %v", err)
	}

	if len(models) == 0 {
		t.Fatal("expected at least one model")
	}

	for _, m := range models {
		if m.ID == "" {
			t.Error("model ID should not be empty")
		}
		t.Logf("Model: ID=%s Name=%s Description=%s", m.ID, m.Name, m.Description)
	}
}

func TestOllamaSearchModels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p := Provider{
		Type:     ProviderOllama,
		Endpoint: "http://localhost:11434",
	}

	models, err := SearchModels(ctx, p, "llama")
	if err != nil {
		t.Skipf("Ollama not available: %v", err)
	}

	t.Logf("Found %d models matching 'llama'", len(models))
	for _, m := range models {
		t.Logf("Model: ID=%s Name=%s", m.ID, m.Name)
	}
}

func TestOpenRouterListModels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	p := Provider{
		Type: ProviderOpenRouter,
	}

	models, err := ListModels(ctx, p)
	if err != nil {
		t.Skipf("OpenRouter not available: %v", err)
	}

	if len(models) == 0 {
		t.Fatal("expected at least one model")
	}

	// Check for common expected models
	var hasGemini, hasGPT, hasClaude bool
	for _, m := range models {
		if contains(m.ID, "gemini") {
			hasGemini = true
		}
		if contains(m.ID, "gpt") {
			hasGPT = true
		}
		if contains(m.ID, "claude") {
			hasClaude = true
		}
	}

	t.Logf("Total models: %d", len(models))
	t.Logf("Has Gemini models: %v", hasGemini)
	t.Logf("Has GPT models: %v", hasGPT)
	t.Logf("Has Claude models: %v", hasClaude)

	if !hasGemini && !hasGPT && !hasClaude {
		t.Error("expected at least one of gemini, gpt, or claude models")
	}
}

func TestOpenRouterSearchModels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	p := Provider{
		Type: ProviderOpenRouter,
	}

	models, err := SearchModels(ctx, p, "claude")
	if err != nil {
		t.Skipf("OpenRouter not available: %v", err)
	}

	if len(models) == 0 {
		t.Fatal("expected at least one model matching 'claude'")
	}

	t.Logf("Found %d models matching 'claude'", len(models))
	for _, m := range models {
		t.Logf("Model: ID=%s Name=%s ContextLen=%d", m.ID, m.Name, m.ContextLen)
	}
}

func TestGeminiListModels(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY environment variable not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p := Provider{
		Type:   ProviderGemini,
		APIKey: apiKey,
	}

	models, err := ListModels(ctx, p)
	if err != nil {
		t.Skipf("Gemini not available: %v", err)
	}

	if len(models) == 0 {
		t.Fatal("expected at least one model")
	}

	// Check for expected models
	var hasGemini20, hasFlash bool
	for _, m := range models {
		if contains(m.ID, "gemini-2.0") {
			hasGemini20 = true
		}
		if contains(m.ID, "flash") {
			hasFlash = true
		}
	}

	t.Logf("Total models: %d", len(models))
	t.Logf("Has Gemini 2.0 models: %v", hasGemini20)
	t.Logf("Has Flash models: %v", hasFlash)

	for _, m := range models[:5] {
		t.Logf("Model: ID=%s Name=%s ContextLen=%d", m.ID, m.Name, m.ContextLen)
	}
}

func TestGeminiSearchModels(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY environment variable not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p := Provider{
		Type:   ProviderGemini,
		APIKey: apiKey,
	}

	models, err := SearchModels(ctx, p, "2.0")
	if err != nil {
		t.Skipf("Gemini not available: %v", err)
	}

	t.Logf("Found %d models matching '2.0'", len(models))
	for _, m := range models {
		t.Logf("Model: ID=%s Name=%s", m.ID, m.Name)
	}
}

func TestOpenRouterListModelsWithAPIKey(t *testing.T) {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		t.Log("OPENROUTER_API_KEY not set, using unauthenticated request")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	p := Provider{
		Type:   ProviderOpenRouter,
		APIKey: apiKey,
	}

	models, err := ListModels(ctx, p)
	if err != nil {
		t.Skipf("OpenRouter not available: %v", err)
	}

	t.Logf("Total models available: %d", len(models))
}

func TestProviderTypes(t *testing.T) {
	tests := []struct {
		providerType ProviderType
		want         string
	}{
		{ProviderOllama, "ollama"},
		{ProviderOpenRouter, "openrouter"},
		{ProviderGemini, "gemini"},
	}

	for _, tt := range tests {
		if string(tt.providerType) != tt.want {
			t.Errorf("ProviderType = %v, want %v", tt.providerType, tt.want)
		}
	}
}

func TestModelStruct(t *testing.T) {
	m := Model{
		ID:          "test-model",
		Name:        "Test Model",
		Description: "A test model",
		ContextLen:  4096,
	}

	if m.ID != "test-model" {
		t.Errorf("Model.ID = %v, want test-model", m.ID)
	}
	if m.Name != "Test Model" {
		t.Errorf("Model.Name = %v, want Test Model", m.Name)
	}
	if m.ContextLen != 4096 {
		t.Errorf("Model.ContextLen = %v, want 4096", m.ContextLen)
	}
}

func TestProviderStruct(t *testing.T) {
	p := Provider{
		Type:     ProviderOllama,
		Endpoint: "http://localhost:11434",
		APIKey:   "test-key",
	}

	if p.Type != ProviderOllama {
		t.Errorf("Provider.Type = %v, want ollama", p.Type)
	}
	if p.Endpoint != "http://localhost:11434" {
		t.Errorf("Provider.Endpoint = %v, want http://localhost:11434", p.Endpoint)
	}
	if p.APIKey != "test-key" {
		t.Errorf("Provider.APIKey = %v, want test-key", p.APIKey)
	}
}
